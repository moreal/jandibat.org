import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { closeSync, constants, existsSync, fchmodSync, fstatSync, lstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, readlinkSync, readdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir, type as osType } from 'node:os';
import { isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const runtimeSource = 'registry.access.redhat.com/ubi10/ubi-minimal:latest@sha256:e3a5632d7ae8a97e06f634522d06187f12793e90ac0d7b51bc671c83a96d8eda';
const donorSource = 'cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
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
  osRelease: 'os-release', packageCatalog: 'package-catalog', apkdbInventory: 'apkdb-inventory',
  trustInventory: 'trust-inventory',
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
  if (!absolute(path)) throw new Error('invalid inspected path');
  const base = resolve(rootfs);
  let pending = path.slice(1).split('/');
  let parts = [];
  let links = 0;
  while (pending.length) {
    const segment = pending.shift();
    if (segment === '' || segment === '.') continue;
    if (segment === '..') {
      if (!parts.length) throw new Error('path escaped inspected root');
      parts.pop();
      continue;
    }
    const target = join(base, ...parts, segment);
    const stat = lstatSync(target);
    if (stat.isSymbolicLink()) {
      if (++links > 32) throw new Error('symlink depth exceeded');
      const link = readlinkSync(target);
      if (link.startsWith('/')) parts = [];
      pending = [...link.split('/'), ...pending];
    } else {
      if (pending.length && !stat.isDirectory()) throw new Error('inspected path parent is not a directory');
      parts.push(segment);
      if (!pending.length && !stat.isFile()) throw new Error('inspected path is not a file');
    }
  }
  const target = join(base, ...parts);
  if (!lstatSync(target).isFile()) throw new Error('inspected path is not a file');
  return target;
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

const apkDbPath = '/usr/lib/apk/db/installed';
const apkDbMaxBytes = 16 * 1024 * 1024;
const apkFieldMax = 8192;
const apkPackageMax = 4096;
const apkOwnedMax = 200000;
const apkName = /^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$/;
const apkVersion = /^[A-Za-z0-9][A-Za-z0-9._+~:-]{0,159}$/;
const apkLicense = /^[A-Za-z0-9][A-Za-z0-9 ._+~:()\/-]{0,511}$/;
const apkPathPart = /^[A-Za-z0-9._+@=,-]+$/;

function apkOwnedPath(directory, file) {
  const segments = [...(directory ? directory.split('/') : []), file];
  required(segments.length && segments.every(part => part !== '.' && part !== '..' && apkPathPart.test(part)), 'APK ownership path');
  return `/${segments.join('/')}`;
}

function parseApkInstalled(rootfs) {
  const dbFile = containedFile(rootfs, apkDbPath);
  const dbStat = lstatSync(dbFile);
  required(dbStat.size > 0 && dbStat.size <= apkDbMaxBytes, 'APK database size');
  const bytes = readFileSync(dbFile);
  assert.ok(bytes.length > 0 && bytes.length <= apkDbMaxBytes && !bytes.includes(0), 'APK database bytes');
  const source = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  const packages = [];
  const ownership = [];
  const names = new Set();
  const paths = new Set();
  let record = [];
  const finish = () => {
    if (!record.length) return;
    const fields = new Map();
    let directory;
    let owned = 0;
    for (const line of record) {
      const match = line.match(/^([A-Za-z]):(.*)$/);
      required(match && line.length <= apkFieldMax, 'APK field');
      const [, key, value] = match;
      if (['P', 'V', 'L'].includes(key)) {
        required(!fields.has(key), 'duplicate APK identity field');
        fields.set(key, value);
      } else if (key === 'F') {
        required(value && value.split('/').every(part => part !== '.' && part !== '..' && apkPathPart.test(part)), 'APK directory');
        directory = value;
      } else if (key === 'R') {
        required(fields.has('P') && directory !== undefined && apkPathPart.test(value) && value !== '.' && value !== '..', 'APK filename');
        const path = apkOwnedPath(directory, value);
        required(!paths.has(path), 'duplicate APK owned path');
        containedFile(rootfs, path);
        paths.add(path);
        ownership.push({ path, package: fields.get('P') });
        owned++;
        required(ownership.length <= apkOwnedMax, 'APK owned-file bound');
      }
    }
    const name = fields.get('P');
    const version = fields.get('V');
    const license = fields.get('L');
    required(apkName.test(name ?? '') && apkVersion.test(version ?? ''), 'APK package identity');
    required(apkLicense.test(license ?? '') && !/^(?:unknown|noassertion|none|unspecified)$/i.test(license), 'APK license declaration');
    required(owned > 0 && ownership.slice(-owned).every(item => item.package === name), 'APK file owner');
    required(!names.has(name), 'duplicate APK package');
    names.add(name);
    packages.push({ name, version, license });
    required(packages.length <= apkPackageMax, 'APK package bound');
    record = [];
  };
  for (const line of source.split('\n')) {
    if (line === '') finish();
    else { required(line.length <= apkFieldMax && !line.includes('\r'), 'APK line'); record.push(line); }
  }
  finish();
  required(packages.length, 'APK package inventory');
  return { packageDb: hashPath(rootfs, apkDbPath), packages, ownership };
}

function canonicalRootPath(rootfs, path) {
  return `/${relative(resolve(rootfs), containedFile(rootfs, path)).split(sep).join('/')}`;
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

function apkOwner(rootfs, ownership, path) {
  const target = canonicalRootPath(rootfs, path);
  const direct = ownership.find(item => item.path === target);
  required(direct, 'direct APK owner for canonical bytes');
  const alias = ownership.find(item => item.path === path);
  required(!alias || alias.package === direct.package, 'matching APK symlink alias owner');
  return { path, resolvedPath: target, package: direct.package };
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
  assert.deepEqual(Object.keys(evidence).sort(), ['applets', 'baseDependencies', 'donor', 'licenseDeclarations', 'ownership', 'packageDb', 'packageManager', 'packages', 'runtime', 'scan', 'schema', 'sourceSha', 'status', 'toolVersions', 'trust']);
  assert.equal(evidence.schema, 2);
  assert.equal(evidence.packageManager, 'apk');
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
  required(evidence.packages?.length, 'APK package inventory');
  const packageNames = new Set();
  const syftIdentities = new Set();
  for (const pkg of evidence.packages) {
    assert.deepEqual(Object.keys(pkg).sort(), ['name', 'type', 'version']);
    assert.equal(pkg.type, 'apk');
    required(apkName.test(pkg.name ?? '') && apkVersion.test(pkg.version ?? ''), 'Syft APK package identity');
    required(!packageNames.has(pkg.name), 'duplicate Syft APK package');
    packageNames.add(pkg.name);
    syftIdentities.add(`${pkg.name}\u0000${pkg.version}`);
  }
  setStage(probeStages.apkdbInventory);
  assert.deepEqual(Object.keys(evidence.packageDb ?? {}).sort(), ['path', 'sha256']);
  assert.equal(evidence.packageDb.path, apkDbPath);
  required(hex(evidence.packageDb.sha256), 'APK database hash');
  required(Array.isArray(evidence.licenseDeclarations) && evidence.licenseDeclarations.length === evidence.packages.length, 'APK license declarations');
  const declarations = new Set();
  for (const item of evidence.licenseDeclarations) {
    assert.deepEqual(Object.keys(item).sort(), ['license', 'name', 'version']);
    required(apkName.test(item.name ?? '') && apkVersion.test(item.version ?? ''), 'APK database package identity');
    required(apkLicense.test(item.license ?? '') && !/^(?:unknown|noassertion|none|unspecified)$/i.test(item.license), 'APK license declaration');
    const identity = `${item.name}\u0000${item.version}`;
    required(!declarations.has(identity), 'duplicate APK database package');
    declarations.add(identity);
  }
  setStage(probeStages.packageCatalog);
  assert.deepEqual([...syftIdentities].sort(), [...declarations].sort(), 'Syft APK catalog differs from installed database');
  setStage(probeStages.trustInventory);
  required(evidence.trust?.length, 'base trust');
  for (const item of evidence.trust) required(absolute(item.path) && hex(item.sha256), 'trust path/hash');
  setStage(probeStages.packageOwnership);
  required(Array.isArray(evidence.ownership) && evidence.ownership.length, 'APK owned files');
  const ownedPaths = new Set();
  for (const item of evidence.ownership) {
    assert.deepEqual(Object.keys(item).sort(), ['package', 'path']);
    required(absolute(item.path) && packageNames.has(item.package), 'APK owned path');
    required(!ownedPaths.has(item.path), 'duplicate APK owned path');
    ownedPaths.add(item.path);
  }
  const baseNeeded = evidence.donor.elfClosure.flatMap(item => item.needed)
    .filter(name => !evidence.donor.nativeFiles.some(item => item.path.endsWith(`/${name}`)));
  required(Array.isArray(evidence.baseDependencies) && evidence.baseDependencies.length === 1 + new Set(baseNeeded).size, 'base ELF dependencies');
  const requiredNames = new Set(baseNeeded);
  const dependencyPaths = new Set();
  for (const dependency of evidence.baseDependencies) {
    assert.deepEqual(Object.keys(dependency).sort(), ['package', 'path', 'resolvedPath']);
    required(absolute(dependency.path) && absolute(dependency.resolvedPath), 'base dependency path');
    required(!dependencyPaths.has(dependency.path), 'duplicate base dependency');
    dependencyPaths.add(dependency.path);
    required(packageNames.has(dependency.package) && evidence.ownership.some(item =>
      item.path === dependency.resolvedPath && item.package === dependency.package), 'APK-owned base dependency');
    if (dependency.path !== evidence.donor.interpreter) {
      const name = dependency.path.split('/').at(-1);
      required(requiredNames.has(name), 'unexpected base library');
      requiredNames.delete(name);
    }
  }
  required(dependencyPaths.has(evidence.donor.interpreter) && requiredNames.size === 0, 'loader and base libraries');
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
  assert.deepEqual(Object.keys(evidence.toolVersions ?? {}).sort(), ['grype', 'readelf', 'skopeo', 'syft', 'umoci']);
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
    inspectionStage = probeStages.elfClosure;
    for (const entry of elfClosure) for (const name of entry.needed)
      required(/^[A-Za-z0-9._+-]+\.so(\.[A-Za-z0-9._+-]+)?$/.test(name), 'ELF dependency name');
    const baseNeeded = [...new Set(elfClosure.flatMap(item => item.needed)
      .filter(name => !nativePaths.some(path => path.endsWith(`/${name}`))))];
    const basePaths = [donorElf.interpreter, ...baseNeeded.map(name => runtimeFiles.find(path => path.endsWith(`/${name}`)))];
    required(basePaths.every(Boolean), 'base native dependency');
    inspectionStage = probeStages.apkdbInventory;
    const apk = parseApkInstalled(runtimeRoot);
    inspectionStage = probeStages.packageOwnership;
    const baseDependencies = basePaths.map(path => apkOwner(runtimeRoot, apk.ownership, path));
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
    const toolVersions = Object.fromEntries(['skopeo', 'syft', 'grype', 'umoci', 'readelf']
      .map(name => [name, command(name, ['--version']).stdout.trim().split('\n')[0].slice(0, 159)]));
    inspectionStage = probeStages.osRelease;
    const osRelease = parseOsRelease(runtimeRoot);
    inspectionStage = probeStages.packageCatalog;
    const packages = catalog.artifacts.filter(pkg => pkg.type === 'apk').map(pkg => ({ name: pkg.name, version: pkg.version, type: 'apk' }));
    inspectionStage = probeStages.trustInventory;
    const trust = ['/etc/ssl/certs/ca-certificates.crt', '/etc/pki/tls/certs/ca-bundle.crt', '/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem']
      .filter(path => regular(runtimeRoot, path)).map(path => hashPath(runtimeRoot, path));
    inspectionStage = probeStages.appletInventory;
    const appletEvidence = { bootstrapSourceSha256, names: applets };
    inspectionStage = probeStages.scanEvidence;
    const scanEvidence = { tool: 'grype', failOn: 'high',
      matches: findings.matches.map(match => ({ id: match.vulnerability.id, severity: match.vulnerability.severity,
        package: match.artifact?.name ?? '', fixState: match.vulnerability.fix?.state ?? '' })),
      databaseBuilt };
    inspectionStage = probeStages.dossierIdentity;
    const evidence = {
      schema: 2, sourceSha, status: 'candidate', packageManager: 'apk',
      runtime: { source: runtimeSource, ...runtime, os: 'linux', architecture: 'amd64', osRelease },
      donor: { source: donorSource, amd64Digest: donor.amd64Digest, ...donorElf, elfClosure, nativeFiles, nativeDecisions,
        licenses: donorLicenses },
      packages, packageDb: apk.packageDb, licenseDeclarations: apk.packages,
      ownership: apk.ownership, baseDependencies, trust,
      applets: appletEvidence, scan: scanEvidence, toolVersions,
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
} else if (process.argv[2] === '--inspect-apk-db') {
  try {
    assert.equal(process.argv.length, 4);
    process.stdout.write(JSON.stringify(parseApkInstalled(resolve(process.argv[3]))) + '\n');
  } catch {
    process.stderr.write('restore runtime probe rejected: invalid APK inventory\n');
    process.exitCode = 1;
  }
} else if (process.argv[2] === '--inspect-apk-owner') {
  try {
    assert.equal(process.argv.length, 5);
    const rootfs = resolve(process.argv[3]);
    const apk = parseApkInstalled(rootfs);
    process.stdout.write(JSON.stringify(apkOwner(rootfs, apk.ownership, process.argv[4])) + '\n');
  } catch {
    process.stderr.write('restore runtime probe rejected: invalid APK owner\n');
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
        atomicEvidence(process.argv[2], { schema: 2, sourceSha, status: 'rejected', phase: 'inspection',
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
