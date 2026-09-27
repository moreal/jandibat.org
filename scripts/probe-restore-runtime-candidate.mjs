import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { closeSync, constants, existsSync, fchmodSync, fstatSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, readlinkSync, readdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir, type as osType } from 'node:os';
import { dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const runtimeSource = 'cockroachdb/cockroach:v26.2.7@sha256:9464ae30465b887295459b98d129a76d074caeaa4c86836d369c6d2fdddd1685';
const donorSource = runtimeSource;
const bootstrapSourceSha256 = '6ef912d18d9f40e46546ffcf5777cecf93405b3f5f4dcde60f2878c10bdc2327';
const applets = ['awk', 'chmod', 'cp', 'mktemp', 'rm', 'sed', 'sha256sum', 'sh', 'tail', 'tr'];
const probeStages = Object.freeze({
  bootstrap: 'bootstrap-source', runtimeIndex: 'runtime-index',
  donorRawFetch: 'donor-raw-fetch', donorManifestParse: 'donor-manifest-parse',
  donorPinCheck: 'donor-pin-check', donorChildSelection: 'donor-amd64-child',
  donorFallbackInspect: 'donor-fallback-inspect',
  scratchAllocation: 'scratch-allocation', scratchCleanup: 'scratch-cleanup',
  runtimeUnpack: 'runtime-unpack', donorUnpack: 'donor-unpack', runtimeSbom: 'runtime-sbom',
  baseScan: 'base-scan', vendorInspection: 'vendor-inspection',
  osRelease: 'os-release', packageCatalog: 'package-catalog', rpmdbInventory: 'rpmdb-inventory',
  trustInventory: 'trust-inventory', baseLicenseInventory: 'base-license-inventory',
  appletInventory: 'applet-inventory', scanEvidence: 'scan-evidence', toolVersions: 'tool-versions',
  dossierIdentity: 'dossier-identity', donorNativeLicense: 'donor-native-license',
  elfClosure: 'elf-closure', packageOwnership: 'package-ownership',
  toolVersionValidation: 'tool-version-validation', dossierStatus: 'dossier-status',
});
const cleanupFailureCategories = Object.freeze({
  EACCES: 'permission-denied', EPERM: 'permission-denied', EBUSY: 'busy',
  ENOTEMPTY: 'not-empty', EROFS: 'read-only-filesystem',
});
let inspectionStage;
let resolvedRuntime;
let cleanupFailure;
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const fileSha = path => sha(readFileSync(path));
const digest = value => /^sha256:[a-f0-9]{64}$/.test(value ?? '');
const hex = value => /^[a-f0-9]{64}$/.test(value ?? '');
const absolute = value => typeof value === 'string' && /^\/[A-Za-z0-9._/+,-]+$/.test(value) && !value.includes('..');
const required = (value, label) => assert.ok(value, `missing ${label}`);

function command(name, args, options = {}) {
  const result = spawnSync(name, args, { cwd: root, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options });
  if (result.error || (!options.allowFailure && result.status !== 0)) throw new Error(`${name} failed`);
  return result;
}

function pathsUnder(directory, prefix = '') {
  if (!existsSync(directory)) return [];
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    const name = `${prefix}/${entry.name}`;
    return entry.isDirectory() ? pathsUnder(path, name) : [name];
  });
}

function containedFile(rootfs, path) {
  let target = resolve(rootfs, `.${path}`);
  for (let i = 0; i < 16; i++) {
    if (!target.startsWith(`${rootfs}/`)) throw new Error('path escaped inspected root');
    const stat = lstatSync(target);
    if (!stat.isSymbolicLink()) {
      if (!stat.isFile()) throw new Error('inspected path is not a file');
      return target;
    }
    const link = readlinkSync(target);
    target = link.startsWith('/') ? resolve(rootfs, `.${link}`) : resolve(dirname(target), link);
  }
  throw new Error('symlink depth exceeded');
}

function regular(rootfs, path) {
  try { containedFile(rootfs, path); return true; } catch { return false; }
}

function hashPath(rootfs, path) {
  return { path, sha256: fileSha(containedFile(rootfs, path)) };
}

function atomicEvidence(output, evidence) {
  const temporary = `${output}.${randomUUID()}.tmp`;
  try {
    writeFileSync(temporary, JSON.stringify(evidence, null, 2) + '\n', { mode: 0o644, flag: 'wx' });
    renameSync(temporary, output);
  } finally { rmSync(temporary, { force: true }); }
}

function parseOsRelease(rootfs) {
  const path = join(rootfs, 'etc/os-release');
  const lines = readFileSync(path, 'utf8').split('\n');
  const fields = Object.fromEntries(lines.map(line => line.match(/^([A-Z_]+)="?([^"\n]*)"?$/)).filter(Boolean).map(match => [match[1], match[2]]));
  return { ID: fields.ID, VERSION_ID: fields.VERSION_ID };
}

function imageRepository(source) {
  const name = source.split('@')[0];
  const tagSeparator = name.lastIndexOf(':');
  return tagSeparator > name.lastIndexOf('/') ? name.slice(0, tagSeparator) : name;
}

function skopeoSource(source) {
  const pinned = source.match(/@(sha256:[a-f0-9]{64})$/)?.[1];
  return pinned ? `${imageRepository(source)}@${pinned}` : source;
}

function resolveImage(source, requireIndex, stages = {}) {
  if (stages.fetch) inspectionStage = stages.fetch;
  const raw = command('skopeo', ['inspect', '--raw', `docker://${skopeoSource(source)}`]).stdout;
  if (stages.parse) inspectionStage = stages.parse;
  const manifest = JSON.parse(raw);
  if (stages.pin) inspectionStage = stages.pin;
  const indexDigest = `sha256:${sha(raw)}`;
  const pinned = source.match(/@(?<digest>sha256:[a-f0-9]{64})$/)?.groups?.digest;
  if (pinned) assert.equal(indexDigest, pinned, 'donor digest mismatch');
  if (Array.isArray(manifest.manifests)) {
    if (stages.child) inspectionStage = stages.child;
    const child = manifest.manifests.find(item => item.platform?.os === 'linux' && item.platform?.architecture === 'amd64');
    required(child && digest(child.digest), 'linux/amd64 child digest');
    return { indexDigest, amd64Digest: child.digest };
  }
  if (requireIndex) throw new Error('missing linux/amd64 child digest');
  if (stages.fallback) inspectionStage = stages.fallback;
  required(pinned && pinned === indexDigest, 'pinned donor manifest');
  const inspected = JSON.parse(command('skopeo', ['inspect', `docker://${skopeoSource(source)}`]).stdout);
  assert.equal(inspected.Os, 'linux');
  assert.equal(inspected.Architecture, 'amd64');
  return { indexDigest, amd64Digest: indexDigest };
}

function unpack(source, childDigest, directory) {
  const image = `${imageRepository(source)}@${childDigest}`;
  const layout = join(directory, 'oci');
  const bundle = join(directory, 'bundle');
  mkdirSync(directory, { recursive: true });
  command('skopeo', ['copy', `docker://${image}`, `oci:${layout}:candidate`]);
  command('umoci', ['unpack', '--rootless', '--image', `${layout}:candidate`, bundle]);
  return join(bundle, 'rootfs');
}

function repairScratchDirectories(scratch) {
  const rootStat = lstatSync(scratch);
  if (!rootStat.isDirectory() || rootStat.uid !== process.getuid()) throw new Error('unsafe scratch root');
  const visit = (directory, anchoredPath, isRoot = false) => {
    const suffix = relative(scratch, directory);
    if (isAbsolute(suffix) || suffix === '..' || suffix.startsWith(`..${sep}`)) throw new Error('scratch path escaped');
    const fd = openSync(anchoredPath, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    try {
      const stat = fstatSync(fd);
      if (!stat.isDirectory() || stat.uid !== rootStat.uid || stat.dev !== rootStat.dev ||
          (isRoot && stat.ino !== rootStat.ino)) throw new Error('unsafe scratch directory');
      fchmodSync(fd, (stat.mode & 0o7777) | 0o700);
      // The production Linux probe traverses the already-open directory, not a re-resolved path.
      // Darwin is used only by the synthetic test harness, which emulates process.platform.
      if (osType() === 'Linux' && !existsSync('/proc/self/fd')) throw new Error('descriptor traversal unavailable');
      const anchor = osType() === 'Linux' ? `/proc/self/fd/${fd}` : directory;
      for (const entry of readdirSync(anchor, { withFileTypes: true })) {
        if (entry.isDirectory()) visit(join(directory, entry.name), join(anchor, entry.name));
      }
    } finally { closeSync(fd); }
  };
  visit(scratch, scratch, true);
}

function removeScratch(scratch) {
  try { rmSync(scratch, { recursive: true, force: true }); }
  catch (error) {
    if (error?.code !== 'EACCES' && error?.code !== 'EPERM') throw error;
    repairScratchDirectories(scratch);
    rmSync(scratch, { recursive: true, force: true });
  }
}

function elf(rootfs, path, executable = false) {
  const target = containedFile(rootfs, path);
  const header = command('readelf', ['-h', target]).stdout;
  required(/Machine:\s*Advanced Micro Devices X86-64/.test(header), 'amd64 ELF');
  const program = command('readelf', ['-l', target]).stdout;
  const dynamic = command('readelf', ['-d', target]).stdout;
  const interpreter = program.match(/Requesting program interpreter:\s*([^\]]+)/)?.[1];
  const needed = [...dynamic.matchAll(/\(NEEDED\).*\[([^\]]+)\]/g)].map(match => match[1]);
  if (executable) {
    required(interpreter && absolute(interpreter), 'ELF interpreter');
    required(needed.length, 'ELF dependencies');
  }
  return { interpreter, needed };
}

function owner(rootfs, path) {
  const result = command('rpm', ['--root', rootfs, '-qf', '--qf', '%{NAME}', path], { allowFailure: true });
  return result.status === 0 ? result.stdout.trim() : '';
}

function inventory(rootfs, directories) {
  return directories.flatMap(dir => pathsUnder(join(rootfs, dir), `/${dir}`))
    .filter(path => regular(rootfs, path)).map(path => hashPath(rootfs, path));
}

function isDonorLicensePath(path) {
  if (/^\/(cockroach\/licenses|licenses)\//i.test(path) || /^\/usr\/share\/licenses\/cockroach[^/]*\//i.test(path)) return true;
  if (!path.startsWith('/cockroach/')) return false;
  const name = path.split('/').at(-1);
  return /^(?:licen[sc]es?|copying|notices?|third[-_]party[-_](?:licen[sc]es?|notices?))(?:[._-].*)?$/i.test(name);
}

function donorLicenseInventory(rootfs, files) {
  return files.filter(path => isDonorLicensePath(path) && regular(rootfs, path))
    .map(path => hashPath(rootfs, path));
}

function validate(evidence, sourceSha, setStage = () => {}) {
  setStage(probeStages.dossierIdentity);
  assert.deepEqual(Object.keys(evidence).sort(), ['applets', 'baseLicenses', 'donor', 'ownership', 'packages', 'rpmdb', 'runtime', 'scan', 'schema', 'sourceSha', 'status', 'toolVersions', 'trust']);
  assert.equal(evidence.schema, 1);
  assert.equal(evidence.sourceSha, sourceSha);
  assert.ok(['candidate', 'rejected'].includes(evidence.status));
  assert.equal(evidence.runtime.source, runtimeSource);
  required(digest(evidence.runtime.indexDigest), 'runtime index digest');
  assert.equal(evidence.runtime.indexDigest, runtimeSource.match(/@(sha256:[a-f0-9]{64})$/)?.[1], 'runtime pinned index digest');
  required(digest(evidence.runtime.amd64Digest), 'runtime amd64 child digest');
  assert.equal(evidence.runtime.os, 'linux');
  assert.equal(evidence.runtime.architecture, 'amd64');
  setStage(probeStages.osRelease);
  required(evidence.runtime.osRelease?.ID && evidence.runtime.osRelease?.VERSION_ID, 'OS release');
  setStage(probeStages.dossierIdentity);
  assert.equal(evidence.donor.source, donorSource);
  required(digest(evidence.donor.amd64Digest), 'donor amd64 digest');
  if (donorSource === runtimeSource) assert.equal(evidence.donor.amd64Digest, evidence.runtime.amd64Digest, 'shared image amd64 child');
  setStage(probeStages.donorNativeLicense);
  required(absolute(evidence.donor.interpreter), 'ELF interpreter');
  required(evidence.donor.needed?.length, 'ELF dependencies');
  for (const key of ['nativeFiles', 'licenses']) {
    required(evidence.donor[key]?.length, `donor ${key}`);
    for (const item of evidence.donor[key]) required(absolute(item.path) && hex(item.sha256), `donor ${key} hash`);
  }
  required(evidence.donor.nativeFiles.some(item => item.path === '/cockroach/cockroach'), 'Cockroach executable');
  required(evidence.donor.licenses.every(item => isDonorLicensePath(item.path)), 'Cockroach license paths');
  required(evidence.donor.licenses.some(item => /(?:licen[sc]es?|copying|notices?|third[-_]party[-_](?:licen[sc]es?|notices?))(?:[._-].*)?$/i.test(item.path.split('/').at(-1))), 'Cockroach license or notice');
  const nativePaths = evidence.donor.nativeFiles.map(item => item.path);
  assert.equal(new Set(nativePaths).size, nativePaths.length, 'duplicate vendor native path');
  for (const path of nativePaths) required(path === '/cockroach/cockroach' || /^\/cockroach\/.+\.so(\.[A-Za-z0-9._-]+)?$/.test(path), 'vendor native path');
  required(evidence.donor.nativeDecisions?.length, 'vendor native decisions');
  const decisions = evidence.donor.nativeDecisions;
  assert.equal(new Set(decisions.map(item => item.path)).size, decisions.length, 'duplicate vendor decision');
  assert.deepEqual(decisions.filter(item => item.decision === 'include').map(item => item.path).sort(), [...nativePaths].sort(), 'vendor inclusion decisions');
  for (const item of decisions) {
    required(absolute(item.path) && item.path.startsWith('/cockroach/') && hex(item.sha256), 'vendor decision path/hash');
    if (item.decision === 'include') {
      const native = evidence.donor.nativeFiles.find(file => file.path === item.path);
      required(native && native.sha256 === item.sha256, 'included native bytes');
      assert.equal(item.reason, item.path === '/cockroach/cockroach' ? 'main-executable' : 'vendor-elf-library');
    } else {
      assert.equal(item.decision, 'exclude');
      required(item.path !== '/cockroach/cockroach' && !/\.so(\.[A-Za-z0-9._-]+)?$/.test(item.path), 'excluded vendor ELF');
      if (item.reason === 'license-copied-separately') {
        required(evidence.donor.licenses.some(file => file.path === item.path && file.sha256 === item.sha256), 'separate license bytes');
      } else assert.equal(item.reason, 'non-ELF');
    }
  }
  setStage(probeStages.elfClosure);
  required(Array.isArray(evidence.donor.elfClosure), 'vendor ELF closure');
  assert.deepEqual(evidence.donor.elfClosure.map(item => item.path).sort(), [...nativePaths].sort(), 'vendor ELF closure paths');
  const mainElf = evidence.donor.elfClosure.find(item => item.path === '/cockroach/cockroach');
  assert.deepEqual(mainElf.needed, evidence.donor.needed, 'main ELF dependencies');
  for (const entry of evidence.donor.elfClosure) {
    required(Array.isArray(entry.needed), 'ELF needed list');
    for (const name of entry.needed) required(/^[A-Za-z0-9._+-]+\.so(\.[A-Za-z0-9._+-]+)?$/.test(name), 'ELF dependency name');
  }
  setStage(probeStages.packageCatalog);
  required(evidence.packages?.length, 'RPM package inventory');
  const rpmNames = new Set();
  for (const pkg of evidence.packages) {
    assert.equal(pkg.type, 'rpm');
    required(pkg.name && pkg.version, 'RPM package identity');
    rpmNames.add(pkg.name);
  }
  for (const key of ['rpmdb', 'trust', 'baseLicenses']) {
    setStage({ rpmdb: probeStages.rpmdbInventory, trust: probeStages.trustInventory,
      baseLicenses: probeStages.baseLicenseInventory }[key]);
    required(evidence[key]?.length, key);
    for (const item of evidence[key]) required(absolute(item.path) && hex(item.sha256), `${key} path/hash`);
  }
  setStage(probeStages.rpmdbInventory);
  required(evidence.rpmdb.some(item => /\/(rpmdb\.sqlite|Packages)$/.test(item.path)), 'RPM database file');
  setStage(probeStages.packageOwnership);
  const baseNeeded = evidence.donor.elfClosure.flatMap(item => item.needed)
    .filter(name => !evidence.donor.nativeFiles.some(item => item.path.endsWith(`/${name}`)));
  for (const path of [evidence.donor.interpreter, ...baseNeeded.map(name => evidence.ownership.find(item => item.path.endsWith(`/${name}`))?.path)]) {
    const ownership = evidence.ownership.find(item => item.path === path);
    required(ownership && rpmNames.has(ownership.package), `package owner for ${path}`);
  }
  setStage(probeStages.appletInventory);
  assert.equal(evidence.applets.bootstrapSourceSha256, bootstrapSourceSha256);
  assert.deepEqual(evidence.applets.names, applets, 'frozen BusyBox applets');
  setStage(probeStages.scanEvidence);
  assert.equal(evidence.scan.tool, 'grype');
  assert.equal(evidence.scan.failOn, 'high');
  required(evidence.scan.databaseBuilt, 'Grype database metadata');
  assert.ok(Array.isArray(evidence.scan.matches));
  for (const match of evidence.scan.matches) {
    assert.deepEqual(Object.keys(match).sort(), ['fixState', 'id', 'package', 'severity']);
    required(/^[A-Za-z0-9._:-]+$/.test(match.id), 'vulnerability ID');
    required(['Critical', 'High', 'Medium', 'Low', 'Negligible', 'Unknown'].includes(match.severity), 'severity');
    required(typeof match.package === 'string' && match.package.length > 0, 'matched package');
    required(typeof match.fixState === 'string', 'fix state');
  }
  setStage(probeStages.toolVersionValidation);
  assert.deepEqual(Object.keys(evidence.toolVersions ?? {}).sort(), ['grype', 'readelf', 'rpm', 'skopeo', 'syft', 'umoci']);
  for (const value of Object.values(evidence.toolVersions)) required(typeof value === 'string' && value.length > 0 && value.length < 160, 'tool version');
  setStage(probeStages.dossierStatus);
  if (evidence.scan.matches.some(match => ['High', 'Critical'].includes(match.severity))) assert.equal(evidence.status, 'rejected');
  if (evidence.status === 'candidate') assert.equal(evidence.scan.matches.filter(match => ['High', 'Critical'].includes(match.severity)).length, 0);
  return evidence;
}

function probe() {
  assert.equal(process.platform, 'linux', 'probe requires Linux');
  assert.equal(process.arch, 'x64', 'probe requires amd64');
  const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
  assert.match(sourceSha, /^[a-f0-9]{40}$/);
  inspectionStage = probeStages.bootstrap;
  assert.equal(fileSha(join(root, 'scripts/db-bootstrap-roles.sh')), bootstrapSourceSha256, 'bootstrap source changed; refresh applet inventory');
  inspectionStage = probeStages.runtimeIndex;
  const runtime = resolveImage(runtimeSource, true);
  required(typeof runtime.indexDigest === 'string' && digest(runtime.indexDigest) &&
    typeof runtime.amd64Digest === 'string' && digest(runtime.amd64Digest), 'resolved runtime digests');
  resolvedRuntime = { source: runtimeSource, indexDigest: runtime.indexDigest, amd64Digest: runtime.amd64Digest };
  const donor = resolveImage(donorSource, false, {
    fetch: probeStages.donorRawFetch, parse: probeStages.donorManifestParse,
    pin: probeStages.donorPinCheck, child: probeStages.donorChildSelection,
    fallback: probeStages.donorFallbackInspect,
  });
  inspectionStage = probeStages.scratchAllocation;
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-runtime-probe-'));
  try {
    inspectionStage = probeStages.runtimeUnpack;
    const runtimeRoot = unpack(runtimeSource, runtime.amd64Digest, join(scratch, 'runtime'));
    inspectionStage = probeStages.donorUnpack;
    const donorRoot = unpack(donorSource, donor.amd64Digest, join(scratch, 'donor'));
    inspectionStage = probeStages.runtimeSbom;
    const baseSyft = join(scratch, 'base.syft.json');
    command('syft', [`dir:${runtimeRoot}`, '-o', `syft-json=${baseSyft}`]);
    const catalog = JSON.parse(readFileSync(baseSyft, 'utf8'));
    inspectionStage = probeStages.baseScan;
    const scan = command('grype', [`sbom:${baseSyft}`, '--fail-on', 'high', '-o', 'json'], { allowFailure: true });
    const findings = JSON.parse(scan.stdout);
    inspectionStage = probeStages.vendorInspection;
    const donorElf = elf(donorRoot, '/cockroach/cockroach', true);
    const runtimeFiles = pathsUnder(runtimeRoot);
    const donorFiles = pathsUnder(donorRoot);
    for (const path of donorFiles.filter(path => path.startsWith('/cockroach/'))) {
      if (path === '/cockroach/cockroach' || /\.so(\.[A-Za-z0-9._-]+)?$/.test(path)) required(regular(donorRoot, path), 'unreadable vendor native file');
    }
    const vendorPaths = donorFiles.filter(path => path.startsWith('/cockroach/') && regular(donorRoot, path));
    const vendorElfPaths = vendorPaths.filter(path => command('readelf', ['-h', containedFile(donorRoot, path)], { allowFailure: true }).status === 0);
    for (const path of vendorElfPaths) required(path === '/cockroach/cockroach' || /\.so(\.[A-Za-z0-9._-]+)?$/.test(path), 'unclassified vendor ELF');
    for (const path of vendorPaths) required(!/\.so(\.[A-Za-z0-9._-]+)?$/.test(path) || vendorElfPaths.includes(path), 'unclassified vendor native file');
    const nativePaths = ['/cockroach/cockroach', ...vendorElfPaths.filter(path => path !== '/cockroach/cockroach')];
    const elfClosure = nativePaths.map(path => ({ path, needed: path === '/cockroach/cockroach' ? donorElf.needed : elf(donorRoot, path).needed }));
    const nativeFiles = nativePaths.map(path => hashPath(donorRoot, path));
    const baseNeeded = [...new Set(elfClosure.flatMap(item => item.needed)
      .filter(name => !nativePaths.some(path => path.endsWith(`/${name}`))))];
    const basePaths = [donorElf.interpreter, ...baseNeeded.map(name => runtimeFiles.find(path => path.endsWith(`/${name}`)))];
    required(basePaths.every(Boolean), 'base native dependency');
    const ownership = basePaths.map(path => ({ path, package: owner(runtimeRoot, path) }));
    const donorLicenses = donorLicenseInventory(donorRoot, donorFiles);
    const licensePaths = donorLicenses.map(item => item.path);
    const nativeDecisions = vendorPaths.map(path => ({ ...hashPath(donorRoot, path),
      decision: nativePaths.includes(path) ? 'include' : 'exclude',
      reason: path === '/cockroach/cockroach' ? 'main-executable' : nativePaths.includes(path) ? 'vendor-elf-library'
        : licensePaths.includes(path) ? 'license-copied-separately' : 'non-ELF' }));
    inspectionStage = probeStages.scanEvidence;
    const dbStatus = command('grype', ['db', 'status']).stdout;
    const databaseBuilt = dbStatus.match(/^Built:\s*(\S+)/m)?.[1] ?? '';
    inspectionStage = probeStages.toolVersions;
    const toolVersions = Object.fromEntries(['skopeo', 'syft', 'grype', 'umoci', 'readelf', 'rpm']
      .map(name => [name, command(name, ['--version']).stdout.trim().split('\n')[0].slice(0, 159)]));
    inspectionStage = probeStages.osRelease;
    const osRelease = parseOsRelease(runtimeRoot);
    inspectionStage = probeStages.packageCatalog;
    const packages = catalog.artifacts.filter(pkg => pkg.type === 'rpm').map(pkg => ({ name: pkg.name, version: pkg.version, type: 'rpm' }));
    inspectionStage = probeStages.rpmdbInventory;
    const rpmdb = inventory(runtimeRoot, ['usr/lib/sysimage/rpm', 'var/lib/rpm']);
    inspectionStage = probeStages.trustInventory;
    const trust = ['/etc/ssl/certs/ca-certificates.crt', '/etc/pki/tls/certs/ca-bundle.crt', '/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem']
      .filter(path => regular(runtimeRoot, path)).map(path => hashPath(runtimeRoot, path));
    inspectionStage = probeStages.baseLicenseInventory;
    const baseLicenses = inventory(runtimeRoot, ['usr/share/licenses']);
    inspectionStage = probeStages.appletInventory;
    const appletEvidence = { bootstrapSourceSha256, names: applets };
    inspectionStage = probeStages.scanEvidence;
    const scanEvidence = { tool: 'grype', failOn: 'high',
      matches: findings.matches.map(match => ({ id: match.vulnerability.id, severity: match.vulnerability.severity,
        package: match.artifact?.name ?? '', fixState: match.vulnerability.fix?.state ?? '' })),
      databaseBuilt };
    inspectionStage = probeStages.dossierIdentity;
    const evidence = {
      schema: 1, sourceSha, status: 'candidate',
      runtime: { source: runtimeSource, ...runtime, os: 'linux', architecture: 'amd64', osRelease },
      donor: { source: donorSource, amd64Digest: donor.amd64Digest, ...donorElf, elfClosure, nativeFiles, nativeDecisions,
        licenses: donorLicenses },
      packages, rpmdb, ownership, trust, baseLicenses, applets: appletEvidence, scan: scanEvidence, toolVersions,
    };
    inspectionStage = probeStages.scanEvidence;
    const hasBlockingFinding = evidence.scan.matches.some(match => ['High', 'Critical'].includes(match.severity));
    if (scan.status !== 0 && !hasBlockingFinding) throw new Error('scanner failed without blocking match');
    if (hasBlockingFinding) evidence.status = 'rejected';
    inspectionStage = probeStages.dossierIdentity;
    validate(evidence, sourceSha, stage => { inspectionStage = stage; });
    return evidence;
  } finally {
    try { removeScratch(scratch); }
    catch (error) {
      inspectionStage = probeStages.scratchCleanup;
      cleanupFailure = Object.hasOwn(cleanupFailureCategories, error?.code)
        ? cleanupFailureCategories[error.code] : 'unknown';
      throw new Error('scratch cleanup failed');
    }
  }
}

if (process.argv[2] === '--inspect-donor-licenses') {
  try {
    assert.equal(process.argv.length, 4);
    const rootfs = resolve(process.argv[3]);
    process.stdout.write(JSON.stringify(donorLicenseInventory(rootfs, pathsUnder(rootfs))) + '\n');
  } catch {
    process.stderr.write('restore runtime probe rejected: invalid donor inventory\n');
    process.exitCode = 1;
  }
} else if (process.argv[2] === '--validate-evidence') {
  try {
    assert.equal(process.argv.length, 4);
    const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
    validate(JSON.parse(readFileSync(process.argv[3], 'utf8')), sourceSha);
  } catch {
    process.stderr.write('restore runtime probe rejected: invalid evidence\n');
    process.exitCode = 1;
  }
} else {
  try {
    assert.equal(process.argv.length, 3);
    const evidence = probe();
    atomicEvidence(process.argv[2], evidence);
    if (evidence.status !== 'candidate') process.exitCode = 1;
  } catch {
    if (process.argv.length === 3) {
      try {
        const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
        atomicEvidence(process.argv[2], { schema: 1, sourceSha, status: 'rejected', phase: 'inspection',
          ...(Object.values(probeStages).includes(inspectionStage) ? { stage: inspectionStage } : {}),
          ...(inspectionStage === probeStages.scratchCleanup && cleanupFailure ? { cleanupFailure } : {}),
          ...(resolvedRuntime && resolvedRuntime.source === runtimeSource &&
            digest(resolvedRuntime.indexDigest) && digest(resolvedRuntime.amd64Digest) ? { runtime: resolvedRuntime } : {}) });
      } catch { /* the output path itself may be unusable */ }
    }
    process.stderr.write('restore runtime probe rejected: inspection failed\n');
    process.exitCode = 1;
  }
}
