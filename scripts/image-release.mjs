// External commands are the only I/O boundary; archive hashes, image IDs and
// registry manifest digests are deliberately recorded as distinct identities.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join, resolve } from 'node:path';

const names = ['api', 'worker', 'maintenance', 'web', 'restore-tools'];
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const fileHash = path => hash(readFileSync(path)).slice('sha256:'.length);
const json = path => JSON.parse(readFileSync(path, 'utf8'));
function command(program, args, allowFailure = false) {
  const result = spawnSync(program, args, { encoding: 'utf8', maxBuffer: 128 * 1024 * 1024 });
  if (!allowFailure && result.status !== 0) throw new Error(`${program} failed: ${result.error || result.stderr}`);
  return result;
}
function save(path, value) { writeFileSync(path, JSON.stringify(value, null, 2) + '\n'); }

function verifyScanArtifacts(paths, imageId, manifestDigest, target) {
  assert.match(json(paths.spdx).spdxVersion, /^SPDX-/);
  const syftSource = json(paths.syft).source?.metadata;
  const report = json(paths.grype);
  for (const source of [syftSource, report.source?.target]) {
    assert.equal(source?.imageID, imageId, `scan image ID must equal imported archive: ${target}`);
    if (manifestDigest) assert.equal(source?.manifestDigest, manifestDigest, `scan manifest must equal deploy digest: ${target}`);
  }
  assert.ok(Array.isArray(report.matches), 'Grype must include match results');
  assert.ok(!report.matches.some(m => /^(high|critical)$/i.test(m.vulnerability?.severity)), 'high/critical finding');
}

function scan(name, target, imageId, manifestDigest, evidence) {
  const identity = manifestDigest || imageId;
  const prefix = join(evidence, `${name}-${identity.replace(':', '-')}`);
  const paths = { spdx: prefix + '.spdx.json', syft: prefix + '.syft.json', grype: prefix + '.grype.json' };
  command('syft', [target, '-o', `spdx-json=${paths.spdx}`, '-o', `syft-json=${paths.syft}`]);
  const result = command('grype', [target, '--fail-on', 'high', '-o', 'json'], true);
  writeFileSync(paths.grype, result.stdout || '');
  assert.equal(result.status, 0, `high/critical scan failed for ${target}: ${result.stderr}`);
  verifyScanArtifacts(paths, imageId, manifestDigest, target);
  const receipt = { name, target, imageId, manifestDigest: manifestDigest ?? null,
    artifacts: Object.fromEntries(Object.entries(paths).map(([key, path]) => [key, { file: basename(path), sha256: fileHash(path) }])) };
  save(prefix + '.release.json', receipt);
  return basename(prefix + '.release.json');
}

function validate(evidence) {
  const release = json(join(evidence, 'release.json'));
  assert.equal(release.schemaVersion, 1);
  assert.match(release.sourceSha || '', /^[0-9a-f]{40}$/);
  assert.deepEqual(Object.keys(release).sort(), ['images', 'schemaVersion', 'sourceSha']);
  assert.ok(Array.isArray(release.images));
  assert.deepEqual(release.images.map(image => image.name).sort(), [...names].sort());
  for (const image of release.images) {
    assert.deepEqual(Object.keys(image).sort(), ['imageId', 'manifestDigest', 'name', 'ref', 'scanReceipt', 'scanReceiptSha256', 'tag']);
    assert.match(image.imageId, /^sha256:[0-9a-f]{64}$/);
    assert.match(image.manifestDigest, /^sha256:[0-9a-f]{64}$/);
    assert.equal(image.tag, image.ref.replace(`@${image.manifestDigest}`, `:${release.sourceSha}`));
    assert.match(image.ref, /^ghcr\.io\/[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+\/[a-z-]+@sha256:[0-9a-f]{64}$/);
    assert.ok(image.ref.endsWith(`/${image.name}@${image.manifestDigest}`));
    assert.equal(image.scanReceipt, basename(image.scanReceipt));
    assert.match(image.scanReceipt, new RegExp(`^${image.name}-${image.manifestDigest.replace(':', '-') }\\.release\\.json$`));
    assert.match(image.scanReceiptSha256, /^[0-9a-f]{64}$/);
    const receiptPath = join(evidence, image.scanReceipt);
    assert.equal(fileHash(receiptPath), image.scanReceiptSha256);
    const receipt = json(receiptPath);
    assert.deepEqual(Object.keys(receipt).sort(), ['artifacts', 'imageId', 'manifestDigest', 'name', 'target']);
    assert.equal(receipt.name, image.name);
    assert.equal(receipt.target, `registry:${image.ref}`);
    assert.equal(receipt.imageId, image.imageId);
    assert.equal(receipt.manifestDigest, image.manifestDigest);
    assert.deepEqual(Object.keys(receipt.artifacts).sort(), ['grype', 'spdx', 'syft']);
    const paths = {};
    for (const [kind, artifact] of Object.entries(receipt.artifacts)) {
      assert.deepEqual(Object.keys(artifact).sort(), ['file', 'sha256']);
      assert.equal(artifact.file, basename(artifact.file));
      assert.match(artifact.file, new RegExp(`^${image.name}-${image.manifestDigest.replace(':', '-') }\\.${kind}\\.json$`));
      assert.match(artifact.sha256, /^[0-9a-f]{64}$/);
      paths[kind] = join(evidence, artifact.file);
      assert.equal(fileHash(paths[kind]), artifact.sha256);
    }
    verifyScanArtifacts(paths, image.imageId, image.manifestDigest, receipt.target);
  }
}

function importArchive(name, archive, evidence) {
  assert.ok(names.includes(name));
  mkdirSync(evidence, { recursive: true });
  if (name !== 'restore-tools') command('sh', ['scripts/test-image-contract.sh', '--archive', name, archive]);
  const manifest = JSON.parse(command('tar', ['-xOf', archive, 'manifest.json']).stdout);
  assert.equal(manifest.length, 1);
  const configBytes = command('tar', ['-xOf', archive, manifest[0].Config]).stdout;
  const config = JSON.parse(configBytes);
  const imageId = hash(configBytes);
  command('docker', ['load', '--input', archive]);
  const [loaded] = JSON.parse(command('docker', ['image', 'inspect', imageId]).stdout);
  assert.equal(loaded.Id, imageId);
  assert.equal(loaded.Architecture, 'amd64');
  assert.equal(loaded.Os, 'linux');
  assert.equal(loaded.Config.User ?? '', config.config.User ?? '');
  assert.deepEqual(loaded.Config.Entrypoint, config.config.Entrypoint);
  if (name !== 'restore-tools') assert.match(loaded.Config.User, /^[1-9][0-9]*:[1-9][0-9]*$/);
  save(join(evidence, `${name}-${imageId.replace(':', '-')}.inspect.json`), loaded);
  scan(name, `docker:${imageId}`, imageId, undefined, evidence);
  const layout = join(evidence, name);
  // Precompute the exact compressed v2 manifest before any registry mutation.
  command('skopeo', ['copy', '--format', 'v2s2', '--dest-compress', `docker-archive:${archive}`, `dir:${layout}`]);
  const candidate = json(join(layout, 'manifest.json'));
  assert.equal(candidate.config.digest, imageId);
  save(join(evidence, `${name}.json`), { name, archive, archiveHash: hash(readFileSync(archive)), imageId, layout });
}

function isCanonicalAbsence(diagnostic, reference) {
  const response = diagnostic.trim();
  // Captured with flake-locked Skopeo 1.24.1 against a local OCI registry
  // returning canonical NAME_UNKNOWN and MANIFEST_UNKNOWN. Match the whole
  // fatal record and both reference occurrences, never strip arbitrary prefixes.
  const timestamp = response.match(/^time="(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2}))" /)?.[1];
  const tagged = reference.match(/^(.+):([0-9a-f]{40})$/);
  if (timestamp && tagged) {
    for (const absence of ['manifest unknown', 'name unknown']) {
      const message = `Error parsing image name "docker://${reference}": reading manifest ${tagged[2]} in ${tagged[1]}: ${absence}`;
      if (response === `time="${timestamp}" level=fatal msg=${JSON.stringify(message)}`) return true;
    }
  }
  // Exact Distribution/OCI absence codes and their canonical messages only.
  // Unknown wrappers, details or wording need real registry evidence before
  // they can be added. Do not infer absence from a substring or denylist.
  for (const [code, message] of [
    ['NAME_UNKNOWN', 'repository name not known to registry'],
    ['MANIFEST_UNKNOWN', 'manifest unknown'],
  ]) {
    const renderedCode = code.toLowerCase().replace('_', ' ');
    if ([code, renderedCode, `${code}: ${message}`, `${renderedCode}: ${message}`].includes(response)) return true;
    // Match the entire one-error JSON envelope. A generic JSON.parse would
    // silently discard duplicate keys, hiding an ambiguous second error.
    // Only code and optional canonical message (in either order) are allowed.
    const codeField = String.raw`"code"\s*:\s*"${code}"`;
    const messageField = String.raw`"message"\s*:\s*"${message}"`;
    const fields = String.raw`${codeField}(?:\s*,\s*${messageField})?|${messageField}\s*,\s*${codeField}`;
    const envelope = new RegExp(String.raw`^\{\s*"errors"\s*:\s*\[\s*\{\s*(?:${fields})\s*\}\s*\]\s*\}$`);
    if (envelope.test(response)) return true;
  }
  return false;
}

function existingManifest(reference) {
  const result = command('skopeo', ['inspect', '--raw', `docker://${reference}`], true);
  if (result.status === 0) return hash(result.stdout);
  if (!result.error && !result.signal && isCanonicalAbsence(result.stderr || '', reference)) return null;
  throw new Error(`cannot inspect immutable tag ${reference}: ${result.stderr || result.error}`);
}

function publish(evidence) {
  const { GITHUB_SHA: sha, GITHUB_REPOSITORY: repository, GITHUB_OUTPUT: output } = process.env;
  assert.match(sha || '', /^[0-9a-f]{40}$/, 'release tag must be the full Git SHA');
  assert.match(repository || '', /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/);
  assert.ok(output, 'GITHUB_OUTPUT required');
  const candidates = names.map(name => {
    const candidate = json(join(evidence, `${name}.json`));
    assert.equal(candidate.name, name);
    assert.match(candidate.imageId, /^sha256:[0-9a-f]{64}$/);
    const raw = readFileSync(join(candidate.layout, 'manifest.json'));
    assert.equal(JSON.parse(raw).config.digest, candidate.imageId);
    const manifestDigest = hash(raw);
    const image = `ghcr.io/${repository.toLowerCase()}/${name}`;
    const tag = `${image}:${sha}`;
    const existing = existingManifest(tag);
    assert.ok(existing === null || existing === manifestDigest, `immutable SHA tag mismatch: ${tag}`);
    return { ...candidate, manifestDigest, tag, existing, ref: `${image}@${manifestDigest}` };
  });
  // Workflow concurrency serializes writers. Check all tags before pushing any,
  // and check again at each write; existing SHA tags are never repointed.
  for (const candidate of candidates) {
    const current = existingManifest(candidate.tag);
    assert.ok(current === null || current === candidate.manifestDigest, `immutable SHA tag mismatch: ${candidate.tag}`);
    if (current === null) command('skopeo', ['copy', '--preserve-digests', `dir:${candidate.layout}`, `docker://${candidate.tag}`]);
    candidate.scanReceipt = scan(candidate.name, `registry:${candidate.ref}`, candidate.imageId, candidate.manifestDigest, evidence);
    assert.equal(existingManifest(candidate.ref), candidate.manifestDigest, 'registry changed candidate manifest');
    assert.equal(existingManifest(candidate.tag), candidate.manifestDigest, 'registry changed immutable SHA tag');
  }
  // Export nothing until every workload AND restore tool has passing evidence.
  save(join(evidence, 'release.json'), { schemaVersion: 1, sourceSha: sha, images: candidates.map(({ name, tag, ref, imageId, manifestDigest, scanReceipt }) =>
    ({ name, tag, ref, imageId, manifestDigest, scanReceipt, scanReceiptSha256: fileHash(join(evidence, scanReceipt)) })) });
  validate(evidence);
  appendFileSync(output, candidates.map(c => `${c.name.replace('-', '_')}_ref=${c.ref}\n`).join(''));
}

try {
  const [operation, ...args] = process.argv.slice(2);
  if (operation === 'import' && args.length === 3) importArchive(args[0], resolve(args[1]), resolve(args[2]));
  else if (operation === 'publish' && args.length === 1) publish(resolve(args[0]));
  else if (operation === 'validate' && args.length === 1) validate(resolve(args[0]));
  else throw new Error('usage: image-release.mjs import NAME ARCHIVE EVIDENCE | publish EVIDENCE | validate EVIDENCE');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
