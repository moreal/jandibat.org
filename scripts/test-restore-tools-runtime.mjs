import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';

const root = resolve(import.meta.dirname, '..');
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const imageId = `sha256:${'b'.repeat(64)}`;
const sourceSha = 'a'.repeat(40);
const producer = join(root, 'scripts/restore-tools-runtime.mjs');
async function implementation() {
  assert.ok(existsSync(producer), 'missing imported-image runtime evidence producer');
  return import(producer);
}
function fixture(t) {
  const directory = mkdtempSync(join(tmpdir(), 'jandibat-runtime-inventory-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const rootfs = join(directory, 'rootfs');
  function file(path, bytes) {
    const destination = join(rootfs, path);
    mkdirSync(resolve(destination, '..'), { recursive: true });
    writeFileSync(destination, bytes);
    return { path, sha256: hash(bytes) };
  }
  const database = 'P:glibc\nV:2.44-r6\nL:LGPL-2.1-or-later\nF:usr/lib\nR:libc.so.6\nR:ld-linux-x86-64.so.2\n\nP:ca-certificates-bundle\nV:20260909-r1\nL:MPL-2.0 AND MIT\nF:etc/ssl/certs\nR:ca-certificates.crt\n';
  const packageDb = file('/usr/lib/apk/db/installed', database);
  file('/usr/lib/libc.so.6', 'libc');
  file('/usr/lib/ld-linux-x86-64.so.2', 'loader');
  const native = file('/cockroach/cockroach', 'cockroach');
  const notice = file('/licenses/LICENSE', 'donor notice');
  const trust = file('/etc/ssl/certs/ca-certificates.crt', 'base CA');
  file('/etc/os-release', 'ID=wolfi\nVERSION_ID=20230201\n');
  file('/jandibat-api', 'api');
  file('/jandibat-maintenance', 'maintenance');
  file('/busybox', 'busybox');
  file('/workspace/bin/backup-tools', 'backup-tools');
  file('/workspace/db/migrations/0001.sql', 'migration');
  file('/workspace/scripts/db-migrate-url.sh', 'script');
  mkdirSync(join(directory, 'bin'));
  writeFileSync(join(directory, 'bin/readelf'), `#!${process.execPath}\nconst args = process.argv.slice(2); if (args[0] === '-h') console.log('Machine: Advanced Micro Devices X86-64'); if (args[0] === '-l' && args.at(-1).endsWith('/cockroach/cockroach')) console.log('[Requesting program interpreter: /usr/lib/ld-linux-x86-64.so.2]'); if (args[0] === '-d' && args.at(-1).endsWith('/cockroach/cockroach')) console.log('(NEEDED) [libc.so.6]');\n`, { mode: 0o755 });
  const declarations = [{ name: 'glibc', version: '2.44-r6', license: 'LGPL-2.1-or-later' },
    { name: 'ca-certificates-bundle', version: '20260909-r1', license: 'MPL-2.0 AND MIT' }];
  const packages = declarations.map(({ name, version }) => ({ name, version, type: 'apk' }));
  const ownership = [{ path: '/usr/lib/libc.so.6', package: 'glibc' },
    { path: '/usr/lib/ld-linux-x86-64.so.2', package: 'glibc' },
    { path: trust.path, package: 'ca-certificates-bundle' }];
  const candidate = { runtime: { osRelease: { ID: 'wolfi', VERSION_ID: '20230201' } },
    donor: { source: 'fixture-donor', amd64Digest: imageId, interpreter: '/usr/lib/ld-linux-x86-64.so.2', nativeFiles: [native], licenses: [notice],
      elfClosure: [{ path: native.path, needed: ['libc.so.6'] }] },
    packageDb, packages, licenseDeclarations: declarations, ownership, trust: [trust],
    baseDependencies: [
      { path: '/usr/lib/ld-linux-x86-64.so.2', resolvedPath: '/usr/lib/ld-linux-x86-64.so.2', package: 'glibc' },
      { path: '/usr/lib/libc.so.6', resolvedPath: '/usr/lib/libc.so.6', package: 'glibc' },
    ] };
  const sbom = { source: { metadata: { imageID: imageId } }, artifacts: packages };
  return { directory, rootfs, file, candidate, sbom };
}
async function inspect(t, mutate = () => {}) {
  const f = fixture(t);
  const { inspectFinalInventory } = await implementation();
  mutate(f);
  // readelf is the external Linux tooling boundary; all file/parser/hash work is real.
  const previous = process.env.PATH;
  process.env.PATH = `${join(f.directory, 'bin')}:${previous}`;
  try { return inspectFinalInventory(f.rootfs, f.sbom, f.candidate, imageId); }
  finally { process.env.PATH = previous; }
}

test('final runtime inventory hashes real owned bytes and preserves exact APK/Syft declarations', async t => {
  const result = await inspect(t);
  assert.deepEqual(result.packages, [{ name: 'glibc', version: '2.44-r6', type: 'apk' },
    { name: 'ca-certificates-bundle', version: '20260909-r1', type: 'apk' }]);
  assert.equal(result.ownedFiles[0].sha256, '16c8c6eb85e05438f5d6c60ff9869072a3a3b1618aa1481ac7a0cb049f06f51d');
  assert.equal(result.baseDependencies.length, 2);
  assert.deepEqual(result.licenseDeclarations[0], { name: 'glibc', version: '2.44-r6', license: 'LGPL-2.1-or-later' });
  assert.ok(result.copiedFiles.some(file => file.path === '/workspace/bin/backup-tools'));
});

for (const [label, target] of [
  ['root-relative absolute', '/usr/lib/os-release'],
  ['relative', '../usr/lib/os-release'],
]) test(`final runtime OS identity accepts an in-image ${label} symlink`, async t => {
  const result = await inspect(t, f => {
    const path = join(f.rootfs, 'etc/os-release');
    f.file('/usr/lib/os-release', readFileSync(path));
    rmSync(path);
    symlinkSync(target, path);
  });
  assert.equal(result.packages.length, 2);
});

for (const label of ['escaping relative', 'absolute host']) {
  test(`final runtime OS identity rejects an ${label} symlink even when host bytes match`, async t => {
    await assert.rejects(() => inspect(t, f => {
      const path = join(f.rootfs, 'etc/os-release');
      const host = join(f.directory, 'host-os-release');
      // Matching bytes make an accidental host read pass OS identity validation.
      writeFileSync(host, readFileSync(path));
      rmSync(path);
      symlinkSync(label === 'absolute host' ? host : '../../host-os-release', path);
    }));
  });
}

test('owned directory symlink records metadata without transferring ownership to target bytes', async t => {
  const result = await inspect(t, f => {
    const path = join(f.rootfs, 'usr/lib/apk/db/installed');
    const bytes = readFileSync(path, 'utf8') + 'F:\nR:lib\n';
    writeFileSync(path, bytes);
    symlinkSync('usr/lib', join(f.rootfs, 'lib'));
    f.candidate.packageDb.sha256 = hash(bytes);
    f.candidate.ownership.push({ path: '/lib', package: 'ca-certificates-bundle' });
  });
  assert.deepEqual(result.ownedFiles.at(-1), { path: '/lib', package: 'ca-certificates-bundle', kind: 'symlink', target: 'usr/lib' });
  assert.equal(result.baseDependencies.find(item => item.path.endsWith('/libc.so.6')).package, 'glibc');
});

for (const [label, mutate] of [
  ['missing DB', f => rmSync(join(f.rootfs, 'usr/lib/apk/db/installed'))],
  ['changed DB', f => f.file('/usr/lib/apk/db/installed', 'P:SECRET-MARKER\n')],
  ['symlinked DB with unchanged bytes', f => {
    const path = join(f.rootfs, 'usr/lib/apk/db/installed');
    f.file('/db-copy', readFileSync(path));
    rmSync(path);
    symlinkSync('/db-copy', path);
  }],
  ['empty APK catalog', f => { f.sbom.artifacts = []; }],
  ['wrong package version', f => { f.sbom.artifacts[0].version = 'wrong'; }],
  ['duplicate APK catalog', f => f.sbom.artifacts.push(f.sbom.artifacts[0])],
  ['wrong Syft image ID', f => { f.sbom.source.metadata.imageID = `sha256:${'c'.repeat(64)}`; }],
  ['missing owned file', f => rmSync(join(f.rootfs, 'usr/lib/libc.so.6'))],
  ['missing ELF owner', f => { f.candidate.baseDependencies[0].package = 'no-owner'; }],
  ['escaping library symlink', f => { rmSync(join(f.rootfs, 'usr/lib/libc.so.6')); symlinkSync('../../../../SECRET-MARKER', join(f.rootfs, 'usr/lib/libc.so.6')); }],
  ['changed donor native', f => f.file('/cockroach/cockroach', 'changed')],
  ['changed donor notice', f => f.file('/licenses/LICENSE', 'changed')],
  ['changed base trust', f => f.file('/etc/ssl/certs/ca-certificates.crt', 'changed')],
  ['changed license declaration', f => { f.candidate.licenseDeclarations[0].license = 'MIT'; }],
]) test(`final runtime inventory rejects ${label}`, async t => {
  await implementation();
  await assert.rejects(() => inspect(t, mutate));
});

test('runtime producer rejects malformed image identity with sanitized diagnostics and no sidecar', t => {
  const f = fixture(t);
  const result = spawnSync(process.execPath, [producer, 'sha256:SECRET-MARKER', f.directory], { encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /restore-tools runtime evidence rejected/);
  assert.doesNotMatch(result.stderr, /SECRET-MARKER/);
  assert.equal(existsSync(join(f.directory, 'restore-tools-runtime.json')), false);
});

test('frozen preflight is the exact sanitized accepted Linux artifact', async () => {
  const { loadPreflight } = await implementation();
  const candidate = loadPreflight();
  assert.equal(candidate.sourceSha, '6da761b20045d8fbb88ebaf186fab2e13b37c6e0');
  assert.equal(candidate.packages.length, 7);
  assert.equal(candidate.ownership.length, 77);
  assert.equal(candidate.baseDependencies.length, 8);
  assert.equal(candidate.donor.amd64Digest, 'sha256:c95fcef260b068d00d3715e707e75641ef5ec8b4275854e5cdb987b50824a3bf');
});

function scanFixture(t, mutate = () => {}) {
  const f = fixture(t);
  const prefix = `restore-tools-${imageId.replace(':', '-')}`;
  const artifacts = { syft: f.sbom, spdx: { spdxVersion: 'SPDX-2.3' },
    grype: { source: { target: { imageID: imageId } }, matches: [], descriptor: { db: { built: '2026-09-28T06:42:30Z' } } } };
  mutate(artifacts);
  const references = {};
  for (const [kind, value] of Object.entries(artifacts)) {
    const bytes = JSON.stringify(value);
    const file = `${prefix}.${kind}.json`;
    writeFileSync(join(f.directory, file), bytes);
    references[kind] = { file, sha256: hash(bytes) };
  }
  const receipt = { name: 'restore-tools', target: `docker:${imageId}`, imageId, manifestDigest: null, artifacts: references };
  const receiptFile = `${prefix}.release.json`;
  writeFileSync(join(f.directory, receiptFile), JSON.stringify(receipt));
  return { ...f, receipt, receiptFile };
}

test('runtime sidecar binds source, imported image, relative SBOM hash and pending license status', async t => {
  const { verifyImportedScan, assembleRuntimeSidecar } = await implementation();
  assert.equal(typeof verifyImportedScan, 'function', 'missing exact-image scan receipt checker');
  assert.equal(typeof assembleRuntimeSidecar, 'function', 'missing bound runtime sidecar assembly');
  const f = scanFixture(t);
  const scanned = verifyImportedScan(f.directory, imageId);
  const result = assembleRuntimeSidecar({ copiedFiles: [] }, f.candidate, scanned, sourceSha, imageId);
  assert.equal(result.sourceSha, sourceSha);
  assert.equal(result.imageId, imageId);
  assert.equal(result.status, 'passed');
  assert.deepEqual(result.licenseReview, { status: 'license-review-pending' });
  assert.deepEqual(result.sbom, f.receipt.artifacts.syft);
  assert.equal(result.scanReceipt.file, f.receiptFile);
  assert.equal(result.scanReceipt.sha256, hash(JSON.stringify(f.receipt)));
  assert.doesNotMatch(JSON.stringify(result), new RegExp(f.directory));
});

for (const [label, mutate, mutateFile] of [
  ['wrong Syft source', a => { a.syft.source.metadata.imageID = `sha256:${'c'.repeat(64)}`; }],
  ['wrong Grype source', a => { a.grype.source.target.imageID = `sha256:${'c'.repeat(64)}`; }],
  ['High finding even with rehashed receipt', a => a.grype.matches.push({ vulnerability: { severity: 'High' } })],
  ['Critical finding even with rehashed receipt', a => a.grype.matches.push({ vulnerability: { severity: 'Critical' } })],
  ['missing Grype matches', a => { delete a.grype.matches; }],
  ['modified SBOM bytes', () => {}, f => writeFileSync(join(f.directory, f.receipt.artifacts.syft.file), '{}')],
  ['nonportable SBOM path', () => {}, f => {
    f.receipt.artifacts.syft.file = '../SECRET-MARKER.json';
    writeFileSync(join(f.directory, f.receiptFile), JSON.stringify(f.receipt));
  }],
  ['extra raw receipt field', () => {}, f => {
    f.receipt.artifacts.syft.raw = 'SECRET-MARKER';
    writeFileSync(join(f.directory, f.receiptFile), JSON.stringify(f.receipt));
  }],
]) test(`runtime scan receipt rejects ${label}`, async t => {
  const { verifyImportedScan } = await implementation();
  assert.equal(typeof verifyImportedScan, 'function');
  const f = scanFixture(t, mutate);
  mutateFile?.(f);
  assert.throws(() => verifyImportedScan(f.directory, imageId));
});

test('failed producer retry removes its old sidecar without leaking diagnostics', t => {
  const f = fixture(t);
  const output = join(f.directory, 'restore-tools-runtime.json');
  writeFileSync(output, '{"status":"passed"}');
  const result = spawnSync(process.execPath, [producer, 'sha256:SECRET-MARKER', f.directory], { encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.equal(existsSync(output), false);
  assert.equal(result.stdout, '');
  assert.doesNotMatch(result.stderr, /SECRET-MARKER/);
});

test('runtime producer extracts only the immutable imported Docker image and sanitizes tool failure', async t => {
  const { loadPreflight } = await implementation();
  const f = scanFixture(t, a => { a.syft.artifacts = loadPreflight().packages; });
  const layout = join(f.directory, 'restore-tools');
  mkdirSync(layout);
  writeFileSync(join(layout, 'manifest.json'), JSON.stringify({ config: { digest: imageId } }));
  writeFileSync(join(f.directory, 'restore-tools.json'), JSON.stringify({ name: 'restore-tools', imageId, layout }));
  const trace = join(f.directory, 'trace');
  const preload = join(f.directory, 'linux.cjs');
  writeFileSync(preload, `Object.defineProperty(process, 'platform', { value: 'linux' }); Object.defineProperty(process, 'arch', { value: 'x64' });`);
  writeFileSync(join(f.directory, 'bin/docker'), `#!${process.execPath}\nconsole.log(JSON.stringify([{Id:'${imageId}',Os:'linux',Architecture:'amd64',Config:{User:'65532:65532'}}]));`, { mode: 0o755 });
  writeFileSync(join(f.directory, 'bin/skopeo'), `#!${process.execPath}\nrequire('node:fs').writeFileSync('${trace}',JSON.stringify(process.argv.slice(2))); console.error('SECRET-MARKER'); process.exit(1);`, { mode: 0o755 });
  const result = spawnSync(process.execPath, [producer, imageId, f.directory], {
    encoding: 'utf8', env: { ...process.env, GITHUB_SHA: '', NODE_OPTIONS: `--require=${preload}`,
      PATH: `${join(f.directory, 'bin')}:${process.env.PATH}` },
  });
  assert.notEqual(result.status, 0);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /restore-tools runtime evidence rejected/);
  assert.doesNotMatch(result.stderr, /SECRET-MARKER/);
  assert.equal(existsSync(join(f.directory, 'restore-tools-runtime.json')), false);
  const args = JSON.parse(readFileSync(trace, 'utf8'));
  assert.equal(args[0], 'copy');
  assert.ok(args.includes('--format') && args.includes('oci'));
  assert.ok(args.includes(`docker-daemon:${imageId}`), 'rootfs must come from imported immutable image ID');
});

// Synthetic Linux/tool/preflight boundaries exercise phase propagation, not
// real-image eligibility. Rootfs, inventories, receipts and hashes stay real.
async function diagnosticFixture(t, phase) {
  const { loadPreflight } = await implementation();
  const f = fixture(t);
  const frozen = loadPreflight();
  const candidate = { ...frozen, ...f.candidate, runtime: frozen.runtime,
    donor: { ...frozen.donor, ...f.candidate.donor, source: frozen.donor.source,
      amd64Digest: frozen.donor.amd64Digest, needed: ['libc.so.6'], nativeDecisions: [
        { ...f.candidate.donor.nativeFiles[0], decision: 'include', reason: 'main-executable' },
      ] }, scan: { ...frozen.scan, matches: [] } };
  const candidateBytes = JSON.stringify(candidate);
  const configBytes = JSON.stringify({ architecture: 'amd64', os: 'linux', config: { User: '65532:65532' } });
  const id = `sha256:${hash(configBytes)}`;
  const prefix = `restore-tools-${id.replace(':', '-')}`;
  const artifacts = { syft: { ...f.sbom, source: { metadata: { imageID: id } } },
    spdx: { spdxVersion: 'SPDX-2.3' }, grype: { source: { target: { imageID: id } }, matches: [] } };
  const references = {};
  for (const [kind, value] of Object.entries(artifacts)) {
    const bytes = JSON.stringify(value);
    const file = `${prefix}.${kind}.json`;
    writeFileSync(join(f.directory, file), bytes);
    references[kind] = { file, sha256: hash(bytes) };
  }
  writeFileSync(join(f.directory, `${prefix}.release.json`), JSON.stringify({ name: 'restore-tools',
    target: `docker:${id}`, imageId: id, manifestDigest: null, artifacts: references }));
  const layout = join(f.directory, 'restore-tools');
  mkdirSync(layout);
  writeFileSync(join(layout, 'manifest.json'), JSON.stringify({ config: { digest: id } }));
  writeFileSync(join(f.directory, 'restore-tools.json'), JSON.stringify({ name: 'restore-tools', imageId: id, layout }));
  writeFileSync(join(f.directory, 'restore-tools-runtime.json'), '{"status":"passed"}');
  const sentinel = ['SYNTHETIC', randomUUID(), 'postgresql:', '//fixture-user:fixture-pass@fixture.invalid/private/path'].join('-');
  const preload = join(f.directory, 'diagnostic.cjs');
  writeFileSync(preload, `const fs = require('node:fs'); const crypto = require('node:crypto');
const {syncBuiltinESMExports} = require('node:module');
Object.defineProperty(process,'platform',{value:'linux'}); Object.defineProperty(process,'arch',{value:'x64'});
const originalRead = fs.readFileSync; const candidate = ${JSON.stringify(candidateBytes)};
fs.readFileSync = function(path,...args) {
 if (String(path) === ${JSON.stringify(join(root, 'docs/evidence/restore-runtime/preflight-36411980538.json'))}) {
  if (process.env.DIAGNOSTIC_PHASE === 'preflight') throw new Error(process.env.DIAGNOSTIC_SENTINEL);
  return Buffer.from(candidate);
 }
 if (process.env.DIAGNOSTIC_PHASE === 'scan' && String(path).endsWith('.syft.json')) throw new Error(process.env.DIAGNOSTIC_SENTINEL);
 if (process.env.DIAGNOSTIC_PHASE === 'final-inventory' && String(path).includes('/bundle/rootfs/usr/lib/apk/db/installed')) throw new Error(process.env.DIAGNOSTIC_SENTINEL);
 return originalRead.call(this,path,...args);
};
// Only the frozen preflight I/O boundary is synthetic; other digests are real.
const originalHash = crypto.createHash;
crypto.createHash = function(...args) {
 const instance = originalHash(...args); const update = instance.update; const digest = instance.digest; let fixture=false;
 instance.update = function(bytes,...rest) { fixture = String(bytes) === candidate; return update.call(this,bytes,...rest); };
 instance.digest = function(encoding) { return fixture && encoding === 'hex' ? 'a963547f904eff70405115c0371717f59fdd856585081d31f59365a6bd2b6878' : digest.call(this,encoding); }; return instance;
};
const originalScratch = fs.mkdtempSync;
fs.mkdtempSync = function(prefix,...args) { return originalScratch.call(this,String(prefix).includes('jandibat-final-runtime-') ? ${JSON.stringify(join(f.directory, 'scratch-'))} : prefix,...args); };
const originalRemove = fs.rmSync;
fs.rmSync = function(path,options) { if (process.env.DIAGNOSTIC_PHASE === 'cleanup' && options?.recursive && String(path).startsWith(${JSON.stringify(join(f.directory, 'scratch-'))})) throw new Error(process.env.DIAGNOSTIC_SENTINEL); return originalRemove.call(this,path,options); };
const originalWrite = fs.writeFileSync;
fs.writeFileSync = function(path,...args) {
 if (process.env.DIAGNOSTIC_MARKER_DENIED && String(path).endsWith('/.image-validation-stage')) throw new Error(process.env.DIAGNOSTIC_SENTINEL);
 if (process.env.DIAGNOSTIC_PHASE === 'sidecar-write' && String(path).startsWith(${JSON.stringify(join(f.directory, 'restore-tools-runtime.json.'))})) throw new Error(process.env.DIAGNOSTIC_SENTINEL);
 return originalWrite.call(this,path,...args);
};
syncBuiltinESMExports();`);
  function executable(name, body) {
    writeFileSync(join(f.directory, 'bin', name), `#!${process.execPath}\n${body}`, { mode: 0o755 });
  }
  executable('nix', `const {spawnSync}=require('node:child_process'); const result=spawnSync(${JSON.stringify(process.execPath)},[${JSON.stringify(producer)},${JSON.stringify(id)},${JSON.stringify(f.directory)}],{stdio:'inherit'}); process.exit(result.status ?? 99);`);
  executable('docker', `if (process.env.DIAGNOSTIC_PHASE === 'imported-config') { console.log(process.env.DIAGNOSTIC_SENTINEL); console.error(process.env.DIAGNOSTIC_SENTINEL); process.exit(62); } console.log(JSON.stringify([{Id:${JSON.stringify(id)},Os:'linux',Architecture:'amd64',Config:{User:'65532:65532'}}]));`);
  executable('skopeo', `const fs=require('node:fs'); const path=require('node:path');
if (process.env.DIAGNOSTIC_PHASE === 'daemon-oci-copy') { console.log(process.env.DIAGNOSTIC_SENTINEL); console.error(process.env.DIAGNOSTIC_SENTINEL); process.exit(63); }
const destination=process.argv.at(-1).slice(4,-':final'.length); fs.mkdirSync(path.join(destination,'blobs/sha256'),{recursive:true});
const config=${JSON.stringify(configBytes)}; const manifest=JSON.stringify({config:{digest:${JSON.stringify(id)}}}); const manifestId=require('node:crypto').createHash('sha256').update(manifest).digest('hex');
fs.writeFileSync(path.join(destination,'index.json'),JSON.stringify({manifests:[{digest:'sha256:'+manifestId}]}));
fs.writeFileSync(path.join(destination,'blobs/sha256',manifestId),manifest); fs.writeFileSync(path.join(destination,'blobs/sha256',${JSON.stringify(id.slice(7))}),config);`);
  executable('umoci', `const fs=require('node:fs'); if (process.env.DIAGNOSTIC_PHASE === 'oci-unpack') { console.log(process.env.DIAGNOSTIC_SENTINEL); console.error(process.env.DIAGNOSTIC_SENTINEL); process.exit(64); } fs.cpSync(${JSON.stringify(f.rootfs)},process.argv.at(-1)+'/rootfs',{recursive:true});`);
  return { ...f, sentinel, env: { ...process.env, PATH: `${join(f.directory, 'bin')}:${process.env.PATH}`,
    GITHUB_SHA: '', NODE_OPTIONS: `--require=${preload}`, DIAGNOSTIC_PHASE: phase, DIAGNOSTIC_SENTINEL: sentinel } };
}

for (const [phase, label] of [
  ['preflight', 'preflight'], ['scan', 'scan'], ['imported-config', 'imported config'],
  ['daemon-oci-copy', 'daemon OCI copy'], ['oci-unpack', 'OCI unpack'],
  ['final-inventory', 'final inventory'], ['cleanup', 'cleanup'], ['sidecar-write', 'sidecar write'],
]) test(`runtime CI diagnostic localizes ${phase} with a fixed safe annotation`, async t => {
  const f = await diagnosticFixture(t, phase);
  const result = spawnSync('sh', [join(root, 'scripts/run-image-validation-ci.sh'), f.directory], { cwd: root, env: f.env, encoding: 'utf8' });
  assert.notEqual(result.status, 0);
  assert.equal(result.stdout, '');
  assert.equal(result.stderr, `::error::Image validation failed during restore runtime ${label}.\n`);
  assert.equal(readFileSync(join(f.directory, '.image-validation-stage'), 'utf8'), `restore-runtime-${phase}\n`);
  assert.equal(existsSync(join(f.directory, 'restore-tools-runtime.json')), false, 'failed rerun retained stale success');
  assert.ok(!JSON.stringify(readdirSync(f.directory).filter(name => !name.startsWith('.')).map(name =>
    { try { return readFileSync(join(f.directory, name), 'utf8'); } catch { return ''; } })).includes(f.sentinel), 'raw diagnostic entered public artifact');
  const diff = spawnSync('git', ['-c', 'core.fsmonitor=false', 'diff', '--no-ext-diff'], { cwd: root, encoding: 'utf8' });
  assert.equal(diff.status, 0);
  assert.ok(!diff.stdout.includes(f.sentinel), 'synthetic credentials entered Git diff');
});

test('runtime CI diagnostic leaves successful synthetic evidence and pending license semantics unchanged', async t => {
  const f = await diagnosticFixture(t, 'none');
  const result = spawnSync('sh', [join(root, 'scripts/run-image-validation-ci.sh'), f.directory], { cwd: root, env: f.env, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout + result.stderr, '');
  const sidecar = JSON.parse(readFileSync(join(f.directory, 'restore-tools-runtime.json'), 'utf8'));
  assert.equal(sidecar.status, 'passed');
  assert.equal(sidecar.licenseReview.status, 'license-review-pending');
  assert.ok(!JSON.stringify(sidecar).includes(f.sentinel));
});

test('runtime CI diagnostic still rejects when writing the phase marker fails', async t => {
  const f = await diagnosticFixture(t, 'daemon-oci-copy');
  const result = spawnSync('sh', [join(root, 'scripts/run-image-validation-ci.sh'), f.directory], {
    cwd: root, env: { ...f.env, DIAGNOSTIC_MARKER_DENIED: '1' }, encoding: 'utf8',
  });
  assert.notEqual(result.status, 0);
  assert.equal(result.stdout, '');
  assert.equal(result.stderr, '::error::Image validation failed during setup.\n');
  assert.ok(!result.stderr.includes(f.sentinel));
  assert.equal(existsSync(join(f.directory, 'restore-tools-runtime.json')), false);
});

for (const kind of ['absent', 'empty', 'unknown', 'multiline', 'extra-newline', 'nul', 'oversized']) {
  test(`runtime CI diagnostic fails closed on a ${kind} phase marker`, t => {
    const f = fixture(t);
    const sentinel = `SYNTHETIC-${randomUUID()}`;
    const marker = { empty: '', unknown: sentinel, multiline: `restore-runtime-scan\n${sentinel}`,
      'extra-newline': 'restore-runtime-scan\n\n', nul: 'restore-runtime-scan\0',
      oversized: 'restore-runtime-scan' + 'x'.repeat(1000) }[kind];
    writeFileSync(join(f.directory, 'bin/nix'), `#!${process.execPath}\nif (process.env.DIAGNOSTIC_MARKER) require('node:fs').writeFileSync(process.env.IMAGE_VALIDATION_STAGE_FILE,Buffer.from(process.env.DIAGNOSTIC_MARKER,'base64')); console.error(process.env.DIAGNOSTIC_SENTINEL); process.exit(67);`, { mode: 0o755 });
    const result = spawnSync('sh', [join(root, 'scripts/run-image-validation-ci.sh'), f.directory], {
      cwd: root, encoding: 'utf8', env: { ...process.env, PATH: `${join(f.directory, 'bin')}:${process.env.PATH}`,
        DIAGNOSTIC_MARKER: marker === undefined ? '' : Buffer.from(marker).toString('base64'), DIAGNOSTIC_SENTINEL: sentinel },
    });
    assert.equal(result.status, 67);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '::error::Image validation failed during setup.\n');
    assert.ok(!result.stderr.includes(sentinel));
    assert.ok(readFileSync(join(f.directory, '.image-validation-output'), 'utf8').includes(sentinel));
  });
}
