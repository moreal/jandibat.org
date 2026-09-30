// Supplemental imported-image evidence. License identifiers never grant release approval.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseApkInstalled, containedFile, apkOwner, hashPath, elf, parseOsRelease,
  validateCandidate, atomicEvidence, removeScratch } from './probe-restore-runtime-candidate.mjs';

const root = fileURLToPath(new URL('../', import.meta.url));
const preflightFile = 'docs/evidence/restore-runtime/preflight-36411980538.json';
const preflightHash = 'a963547f904eff70405115c0371717f59fdd856585081d31f59365a6bd2b6878';
const preflightSource = '6da761b20045d8fbb88ebaf186fab2e13b37c6e0';
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const json = path => JSON.parse(readFileSync(path, 'utf8'));
const fileHash = path => sha(readFileSync(path));
const digest = value => /^sha256:[a-f0-9]{64}$/.test(value ?? '');
const sorted = items => [...items].sort((a, b) => JSON.stringify(a).localeCompare(JSON.stringify(b)));
const runtimeStages = Object.freeze({
  preflight: 'restore-runtime-preflight', scan: 'restore-runtime-scan',
  importedConfig: 'restore-runtime-imported-config', daemonOciCopy: 'restore-runtime-daemon-oci-copy',
  ociScratch: 'restore-runtime-oci-scratch', skopeoCopy: 'restore-runtime-skopeo-copy',
  ociIndex: 'restore-runtime-oci-index', ociManifest: 'restore-runtime-oci-manifest',
  ociConfigDigest: 'restore-runtime-oci-config-digest', ociConfigBlob: 'restore-runtime-oci-config-blob',
  ociUnpack: 'restore-runtime-oci-unpack', finalInventory: 'restore-runtime-final-inventory',
  cleanup: 'restore-runtime-cleanup', sidecarWrite: 'restore-runtime-sidecar-write',
});
let runtimeStage = runtimeStages.preflight;
function recordRuntimeFailure() {
  if (!process.env.IMAGE_VALIDATION_STAGE_FILE || !Object.values(runtimeStages).includes(runtimeStage)) return;
  try { writeFileSync(process.env.IMAGE_VALIDATION_STAGE_FILE, `${runtimeStage}\n`); }
  catch { /* A missing/unwritable marker cannot disclose diagnostics or grant acceptance. */ }
}
function command(program, args) {
  const result = spawnSync(program, args, { cwd: root, encoding: 'utf8', maxBuffer: 128 * 1024 * 1024 });
  assert.equal(result.status, 0, 'runtime evidence tool failed');
  return result.stdout;
}
export function loadPreflight() {
  const bytes = readFileSync(join(root, preflightFile));
  assert.equal(sha(bytes), preflightHash, 'frozen preflight artifact hash');
  const candidate = JSON.parse(bytes);
  validateCandidate(candidate, preflightSource);
  assert.equal(candidate.status, 'candidate');
  return candidate;
}
function regularHash(rootfs, path) {
  assert.ok(lstatSync(containedFile(rootfs, path, false, true)).isFile(), 'copied file must be regular');
  return hashPath(rootfs, path);
}
function filesUnder(rootfs, prefix) {
  const directory = containedFile(rootfs, prefix, false, true);
  assert.ok(lstatSync(directory).isDirectory(), 'copied payload directory');
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = `${prefix}/${entry.name}`;
    assert.ok(/^[A-Za-z0-9._-]+$/.test(entry.name), 'copied payload filename');
    return entry.isDirectory() ? filesUnder(rootfs, path) : [regularHash(rootfs, path)];
  });
}
export function inspectFinalInventory(rootfs, sbom, candidate, imageId) {
  assert.ok(digest(imageId), 'imported image identity');
  assert.equal(sbom.source?.metadata?.imageID, imageId, 'SBOM imported image identity');
  assert.ok(Array.isArray(sbom.artifacts), 'Syft artifacts');
  assert.deepEqual(regularHash(rootfs, candidate.packageDb.path), candidate.packageDb, 'regular preserved APK database');
  const apk = parseApkInstalled(rootfs);
  assert.deepEqual(apk.packageDb, candidate.packageDb, 'preserved APK database');
  assert.deepEqual(sorted(apk.packages), sorted(candidate.licenseDeclarations), 'APK license declarations');
  assert.deepEqual(sorted(apk.ownership), sorted(candidate.ownership), 'APK ownership declarations');
  const packages = sbom.artifacts.filter(pkg => pkg.type === 'apk').map(({ name, version, type }) => ({ name, version, type }));
  assert.ok(packages.length, 'nonempty Syft APK catalog');
  assert.deepEqual(sorted(packages), sorted(apk.packages.map(({ name, version }) => ({ name, version, type: 'apk' }))), 'Syft APK catalog equality');
  assert.deepEqual(sorted(packages), sorted(candidate.packages), 'frozen APK catalog');
  assert.deepEqual(parseOsRelease(rootfs), candidate.runtime.osRelease, 'preserved OS identity');
  const ownedFiles = apk.ownership.map(item => {
    const entry = containedFile(rootfs, item.path, false, true);
    const stat = lstatSync(entry);
    if (stat.isSymbolicLink()) {
      const target = readlinkSync(entry);
      // The parser has already checked containment, including the one metadata-only pseudo-fs alias.
      return { ...item, kind: 'symlink', target };
    }
    assert.ok(stat.isFile(), 'owned regular file');
    return { ...item, kind: 'file', ...hashPath(rootfs, item.path) };
  });
  const baseDependencies = candidate.baseDependencies.map(item => {
    const owner = apkOwner(rootfs, apk.ownership, item.path);
    assert.deepEqual(owner, item, 'canonical base ELF owner');
    elf(rootfs, item.path);
    return { ...owner, ...hashPath(rootfs, item.path) };
  });
  const nativeFiles = candidate.donor.nativeFiles.map(item => {
    assert.deepEqual(regularHash(rootfs, item.path), item, 'donor native bytes');
    const inspected = elf(rootfs, item.path, item.path === '/cockroach/cockroach');
    const closure = candidate.donor.elfClosure.find(entry => entry.path === item.path);
    assert.deepEqual(inspected.needed, closure.needed, 'donor ELF closure');
    if (item.path === '/cockroach/cockroach') assert.equal(inspected.interpreter, candidate.donor.interpreter, 'donor loader');
    return { ...item, ...inspected };
  });
  const donorLicenses = candidate.donor.licenses.map(item => {
    assert.deepEqual(regularHash(rootfs, item.path), item, 'donor license/notice bytes');
    return item;
  });
  const trust = candidate.trust.map(item => {
    assert.deepEqual(hashPath(rootfs, item.path), item, 'base-owned trust bytes');
    const owner = apkOwner(rootfs, apk.ownership, item.path);
    return { ...item, ...owner };
  });
  const copiedFiles = ['/jandibat-api', '/jandibat-maintenance', '/busybox', '/workspace/bin/backup-tools']
    .map(path => regularHash(rootfs, path));
  for (const prefix of ['/workspace/db/migrations', '/workspace/scripts']) {
    copiedFiles.push(...filesUnder(rootfs, prefix));
  }
  return { packageManager: 'apk', packageDb: apk.packageDb, packages, licenseDeclarations: apk.packages,
    ownedFiles, baseDependencies, nativeFiles, donorLicenses, trust, copiedFiles };
}

export function verifyImportedScan(evidence, imageId) {
  assert.ok(digest(imageId), 'imported image identity');
  const receiptFile = `restore-tools-${imageId.replace(':', '-')}.release.json`;
  const receipt = json(join(evidence, receiptFile));
  assert.deepEqual(Object.keys(receipt).sort(), ['artifacts', 'imageId', 'manifestDigest', 'name', 'target']);
  assert.deepEqual([receipt.name, receipt.target, receipt.imageId, receipt.manifestDigest],
    ['restore-tools', `docker:${imageId}`, imageId, null]);
  assert.deepEqual(Object.keys(receipt.artifacts).sort(), ['grype', 'spdx', 'syft']);
  const artifacts = {};
  for (const [kind, artifact] of Object.entries(receipt.artifacts)) {
    assert.deepEqual(Object.keys(artifact).sort(), ['file', 'sha256'], 'sanitized artifact reference');
    assert.equal(artifact.file, `restore-tools-${imageId.replace(':', '-')}.${kind}.json`, 'portable scan artifact name');
    assert.match(artifact.sha256, /^[a-f0-9]{64}$/);
    assert.equal(fileHash(join(evidence, artifact.file)), artifact.sha256, 'scan artifact hash');
    artifacts[kind] = json(join(evidence, artifact.file));
  }
  assert.match(artifacts.spdx.spdxVersion, /^SPDX-/);
  assert.equal(artifacts.syft.source?.metadata?.imageID, imageId);
  assert.equal(artifacts.grype.source?.target?.imageID, imageId);
  assert.ok(Array.isArray(artifacts.grype.matches));
  assert.ok(!artifacts.grype.matches.some(match => /^(high|critical)$/i.test(match.vulnerability?.severity)), 'unchanged high threshold');
  return { artifacts, receipt, receiptFile, receiptHash: fileHash(join(evidence, receiptFile)) };
}

export function assembleRuntimeSidecar(inventory, candidate, scanned, sourceSha, imageId) {
  assert.match(sourceSha, /^[a-f0-9]{40}$/);
  assert.ok(digest(imageId));
  assert.equal(scanned.receipt.imageId, imageId);
  return { schemaVersion: 1, sourceSha, imageId, status: 'passed', licenseReview: { status: 'license-review-pending' },
    preflight: { file: basename(preflightFile), sha256: preflightHash, sourceSha: preflightSource,
      runId: '36411980538', artifactId: '10964941196' },
    runtime: candidate.runtime, donor: { source: candidate.donor.source, amd64Digest: candidate.donor.amd64Digest },
    sbom: scanned.receipt.artifacts.syft, scanReceipt: { file: scanned.receiptFile, sha256: scanned.receiptHash },
    ...inventory };
}

export function produceRuntimeEvidence(imageId, evidence) {
  runtimeStage = runtimeStages.preflight;
  const output = join(evidence, 'restore-tools-runtime.json');
  // An unsuccessful retry must not leave a previous passing sidecar.
  if (existsSync(output)) { assert.ok(lstatSync(output).isFile(), 'regular runtime sidecar'); rmSync(output); }
  assert.ok(digest(imageId), 'imported image identity');
  assert.equal(process.platform, 'linux', 'Linux imported-image evidence required');
  assert.equal(process.arch, 'x64', 'amd64 imported-image evidence required');
  const sourceSha = command('git', ['rev-parse', 'HEAD']).trim();
  assert.match(sourceSha, /^[a-f0-9]{40}$/);
  if (process.env.GITHUB_SHA) assert.equal(sourceSha, process.env.GITHUB_SHA, 'CI source identity');
  const candidate = loadPreflight();
  runtimeStage = runtimeStages.importedConfig;
  const imported = json(join(evidence, 'restore-tools.json'));
  assert.equal(imported.name, 'restore-tools');
  assert.equal(imported.imageId, imageId);
  assert.equal(resolve(imported.layout), join(evidence, 'restore-tools'), 'imported layout path');
  runtimeStage = runtimeStages.scan;
  const scanned = verifyImportedScan(evidence, imageId);
  runtimeStage = runtimeStages.importedConfig;
  assert.equal(json(join(imported.layout, 'manifest.json')).config.digest, imageId);
  const [loaded] = JSON.parse(command('docker', ['image', 'inspect', imageId]));
  assert.equal(loaded.Id, imageId);
  assert.equal(loaded.Os, 'linux');
  assert.equal(loaded.Architecture, 'amd64');
  assert.equal(loaded.Config.User, '65532:65532');
  runtimeStage = runtimeStages.ociScratch;
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-final-runtime-'));
  let sidecar;
  try {
    const layout = join(scratch, 'oci');
    // The daemon transport reads the already-imported ID, never a mutable tag or
    // a potentially changed candidate layer layout. OCI conversion preserves
    // config bytes (checked below) while allowing Umoci's manifest media type.
    runtimeStage = runtimeStages.skopeoCopy;
    command('skopeo', ['copy', '--format', 'oci', `docker-daemon:${imageId}`, `oci:${layout}:final`]);
    runtimeStage = runtimeStages.ociIndex;
    const ociIndex = json(join(layout, 'index.json'));
    assert.equal(ociIndex.manifests.length, 1);
    runtimeStage = runtimeStages.ociManifest;
    const ociManifest = json(join(layout, 'blobs/sha256', ociIndex.manifests[0].digest.slice(7)));
    runtimeStage = runtimeStages.ociConfigDigest;
    assert.equal(ociManifest.config.digest, imageId, 'unpacked image config identity');
    runtimeStage = runtimeStages.ociConfigBlob;
    assert.equal(`sha256:${fileHash(join(layout, 'blobs/sha256', imageId.slice(7)))}`, imageId);
    runtimeStage = runtimeStages.ociUnpack;
    command('umoci', ['unpack', '--rootless', '--image', `${layout}:final`, join(scratch, 'bundle')]);
    runtimeStage = runtimeStages.finalInventory;
    const inventory = inspectFinalInventory(join(scratch, 'bundle/rootfs'), scanned.artifacts.syft, candidate, imageId);
    sidecar = assembleRuntimeSidecar(inventory, candidate, scanned, sourceSha, imageId);
  } finally {
    const inspectionStage = runtimeStage;
    runtimeStage = runtimeStages.cleanup;
    removeScratch(scratch);
    runtimeStage = inspectionStage;
  }
  // Write only after inspection and cleanup pass; external diagnostics never enter the sidecar.
  runtimeStage = runtimeStages.sidecarWrite;
  atomicEvidence(output, sidecar);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    assert.equal(process.argv.length, 4);
    const evidence = resolve(process.argv[3]);
    mkdirSync(evidence, { recursive: true });
    produceRuntimeEvidence(process.argv[2], evidence);
  } catch {
    recordRuntimeFailure();
    process.stderr.write('restore-tools runtime evidence rejected: imported image inspection failed\n');
    process.exitCode = 1;
  }
}
