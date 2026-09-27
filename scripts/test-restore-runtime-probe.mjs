import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../', import.meta.url));
const script = join(root, 'scripts/probe-restore-runtime-candidate.sh');
const sourceSha = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim();
const runtimeIndexDigest = 'sha256:6acf5a19a988abdaf0f3d30247561431a206034e702871442bed66a2c68cc1a2';
const digest = 'sha256:1e9870bd8b72e908eff47d5f4ffcdca068b2765fbb22223cc5a68c989902d195';
const donorDigest = 'sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
const donorChildDigest = `sha256:${'b'.repeat(64)}`;
const candidateSource = `cgr.dev/chainguard/glibc-dynamic:latest@${runtimeIndexDigest}`;
const donorSource = `cockroachdb/cockroach:v26.2.5@${donorDigest}`;
const bootstrapDigest = '6ef912d18d9f40e46546ffcf5777cecf93405b3f5f4dcde60f2878c10bdc2327';
const runtimeManifestRaw = `{"runtime-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}\n`;
const donorManifestRaw = `{"donor-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${donorChildDigest}"}]}\n`;
const runtimeMetadata = { source: candidateSource, indexDigest: runtimeIndexDigest, amd64Digest: digest };
const runtimeSkopeoRef = `docker://cgr.dev/chainguard/glibc-dynamic@${runtimeIndexDigest}`;
const donorSkopeoRef = `docker://cockroachdb/cockroach@${donorDigest}`;
const runtimeChildRef = `docker://cgr.dev/chainguard/glibc-dynamic@${digest}`;
const donorChildRef = `docker://cockroachdb/cockroach@${donorChildDigest}`;

function pinnedLinuxPreload(extra = '') {
  return `const crypto = require('node:crypto');
const { syncBuiltinESMExports } = require('node:module');
Object.defineProperty(process, 'platform', { value: 'linux' });
Object.defineProperty(process, 'arch', { value: 'x64' });
const originalHash = crypto.createHash;
crypto.createHash = function (...args) {
  const hash = originalHash(...args);
  const update = hash.update;
  const finish = hash.digest;
  let testIndex = false;
  let donorIndex = false;
  hash.update = function (data, ...rest) {
    testIndex = String(data).includes('test-index');
    donorIndex = String(data).includes('donor-test-index');
    return update.call(this, data, ...rest);
  };
  hash.digest = function (encoding) {
    return donorIndex && encoding === 'hex' ? '${donorDigest.slice(7)}'
      : testIndex && encoding === 'hex' ? '${runtimeIndexDigest.slice(7)}'
      : finish.call(this, encoding);
  };
  return hash;
};
${extra}
syncBuiltinESMExports();
`;
}

function evidence() {
  return {
    schema: 2, sourceSha, status: 'candidate', packageManager: 'apk',
    runtime: { source: candidateSource, indexDigest: runtimeIndexDigest,
      amd64Digest: digest, os: 'linux', architecture: 'amd64',
      osRelease: { ID: 'wolfi', VERSION_ID: '20230201' } },
    donor: { source: donorSource,
      amd64Digest: donorChildDigest, interpreter: '/lib64/ld-linux-x86-64.so.2',
      needed: ['libvendor.so'],
      elfClosure: [
        { path: '/cockroach/cockroach', needed: ['libvendor.so'] },
        { path: '/cockroach/lib/libvendor.so', needed: ['libc.so.6'] },
      ],
      nativeFiles: [
        { path: '/cockroach/cockroach', sha256: 'c'.repeat(64) },
        { path: '/cockroach/lib/libvendor.so', sha256: '2'.repeat(64) },
      ],
      nativeDecisions: [
        { path: '/cockroach/cockroach', sha256: 'c'.repeat(64), decision: 'include', reason: 'main-executable' },
        { path: '/cockroach/lib/libvendor.so', sha256: '2'.repeat(64), decision: 'include', reason: 'vendor-elf-library' },
        { path: '/cockroach/licenses/LICENSE', sha256: 'd'.repeat(64), decision: 'exclude', reason: 'license-copied-separately' },
      ],
      licenses: [{ path: '/cockroach/licenses/LICENSE', sha256: 'd'.repeat(64) }] },
    packages: [{ name: 'glibc', version: '2.44-r0', type: 'apk' }],
    packageDb: { path: '/usr/lib/apk/db/installed', sha256: 'e'.repeat(64) },
    licenseDeclarations: [{ name: 'glibc', version: '2.44-r0', license: 'LGPL-2.1-or-later' }],
    ownership: [{ path: '/lib64/ld-linux-x86-64.so.2', package: 'glibc' },
      { path: '/usr/lib64/libc.so.6', package: 'glibc' }],
    baseDependencies: [
      { path: '/lib64/ld-linux-x86-64.so.2', resolvedPath: '/lib64/ld-linux-x86-64.so.2', package: 'glibc' },
      { path: '/usr/lib64/libc.so.6', resolvedPath: '/usr/lib64/libc.so.6', package: 'glibc' },
    ],
    trust: [{ path: '/etc/pki/tls/certs/ca-bundle.crt', sha256: 'f'.repeat(64) }],
    applets: { bootstrapSourceSha256: bootstrapDigest,
      names: ['awk', 'chmod', 'cp', 'mktemp', 'rm', 'sed', 'sha256sum', 'sh', 'tail', 'tr'] },
    scan: { tool: 'grype', failOn: 'high', matches: [], databaseBuilt: '2026-09-26T00:00:00Z' },
    toolVersions: { skopeo: '1.24', syft: '1.0', grype: '1.0', umoci: '0.4', readelf: '2.42' },
  };
}

function validate(value) {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-test-'));
  try {
    const path = join(dir, 'candidate.json');
    writeFileSync(path, JSON.stringify(value));
    return spawnSync('sh', [script, '--validate-evidence', path], { cwd: root, encoding: 'utf8' });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

const apkDb = `P:glibc\nV:2.44-r0\nL:LGPL-2.1-or-later\nF:usr/lib\nR:libc.so.6\n\nP:ld-linux\nV:2.44-r0\nL:LGPL-2.1-or-later\nF:usr/lib\nR:ld-linux-x86-64.so.2\n`;

function inspectApk(database = apkDb, options = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-apk-test-'));
  try {
    const db = join(dir, 'usr/lib/apk/db/installed');
    mkdirSync(join(db, '..'), { recursive: true });
    if (database !== null) writeFileSync(db, database);
    for (const path of ['usr/lib/libc.so.6', 'usr/lib/ld-linux-x86-64.so.2']) {
      const file = join(dir, path);
      mkdirSync(join(file, '..'), { recursive: true });
      writeFileSync(file, path);
    }
    if (options.symlink) symlinkSync(options.symlink, join(dir, 'usr/lib/ld-linux-escape.so.2'));
    const args = options.owner ? ['--inspect-apk-owner', dir, options.owner] : ['--inspect-apk-db', dir];
    const result = spawnSync('sh', [script, ...args], { cwd: root, encoding: 'utf8' });
    return { ...result, value: result.status === 0 ? JSON.parse(result.stdout) : null };
  } finally { rmSync(dir, { recursive: true, force: true }); }
}

test('APK installed DB yields exact hashed packages, licenses and owned paths', () => {
  const result = inspectApk();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.value.packageDb.path, '/usr/lib/apk/db/installed');
  assert.equal(result.value.packageDb.sha256, createHash('sha256').update(apkDb).digest('hex'));
  assert.deepEqual(result.value.packages, [
    { name: 'glibc', version: '2.44-r0', license: 'LGPL-2.1-or-later' },
    { name: 'ld-linux', version: '2.44-r0', license: 'LGPL-2.1-or-later' },
  ]);
  assert.deepEqual(result.value.ownership, [
    { path: '/usr/lib/libc.so.6', package: 'glibc' },
    { path: '/usr/lib/ld-linux-x86-64.so.2', package: 'ld-linux' },
  ]);
});

for (const [label, db] of [
  ['absent database', null], ['empty database', ''], ['oversized database', 'x'.repeat(16 * 1024 * 1024 + 1)],
  ['malformed package name', apkDb.replace('P:glibc', 'P:../glibc')],
  ['malformed version', apkDb.replace('V:2.44-r0', 'V:')],
  ['missing license', apkDb.replace('L:LGPL-2.1-or-later\n', '')],
  ['ambiguous license', apkDb.replace('L:LGPL-2.1-or-later', 'L:UNKNOWN')],
  ['duplicate license', apkDb.replace('L:LGPL-2.1-or-later', 'L:MIT\nL:Apache-2.0')],
  ['duplicate package identity', `${apkDb}\n${apkDb}`],
  ['directory traversal', apkDb.replace('F:usr/lib', 'F:../../usr/lib')],
  ['absolute ownership directory', apkDb.replace('F:usr/lib', 'F:/usr/lib')],
  ['file traversal', apkDb.replace('R:libc.so.6', 'R:../libc.so.6')],
  ['duplicate owned file', apkDb.replace('R:libc.so.6', 'R:libc.so.6\nR:libc.so.6')],
  ['missing owned file', apkDb.replace('R:libc.so.6', 'R:missing.so')],
]) {
  test(`rejects APK ${label} without printing DB contents`, () => {
    const result = inspectApk(db);
    assert.notEqual(result.status, 0, `accepted ${label}`);
    assert.doesNotMatch(result.stderr, /LGPL|glibc|password|SENSITIVE-MARKER/i);
  });
}

test('rejects APK owned symlink escaping extracted root', () => {
  const result = inspectApk(apkDb.replace('R:ld-linux-x86-64.so.2', 'R:ld-linux-escape.so.2'),
    { symlink: '../../../../outside/SENSITIVE-MARKER' });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stderr, /SENSITIVE-MARKER|outside/i);
});

test('rejects APK owned symlink resolving to a directory', () => {
  const result = inspectApk(apkDb.replace('R:ld-linux-x86-64.so.2', 'R:ld-linux-escape.so.2'),
    { symlink: '.' });
  assert.notEqual(result.status, 0);
});

test('APK owner lookup accepts a symlink only when its canonical bytes have a direct package owner', () => {
  const result = inspectApk(apkDb + 'R:ld-linux-escape.so.2\n',
    { symlink: '/usr/lib/ld-linux-x86-64.so.2', owner: '/usr/lib/ld-linux-escape.so.2' });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(result.value, { path: '/usr/lib/ld-linux-escape.so.2',
    resolvedPath: '/usr/lib/ld-linux-x86-64.so.2', package: 'ld-linux' });
});

test('APK owner lookup rejects an owned symlink alias when canonical target bytes are unowned', () => {
  const result = inspectApk(apkDb.replace('R:ld-linux-x86-64.so.2', 'R:ld-linux-escape.so.2'),
    { symlink: '/usr/lib/ld-linux-x86-64.so.2', owner: '/usr/lib/ld-linux-escape.so.2' });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stderr, /ld-linux|package|SENSITIVE-MARKER/i);
});

test('APK owner lookup rejects a symlink alias owned by a different package than target bytes', () => {
  const database = apkDb.replace('\n\nP:ld-linux', '\nR:ld-linux-escape.so.2\n\nP:ld-linux');
  const result = inspectApk(database,
    { symlink: '/usr/lib/ld-linux-x86-64.so.2', owner: '/usr/lib/ld-linux-escape.so.2' });
  assert.notEqual(result.status, 0);
});

test('accepts a complete candidate dossier', () => {
  const result = validate(evidence());
  assert.equal(result.status, 0, result.stderr);
});

test('accepts a candidate inventory bound to the current bootstrap source', () => {
  const value = evidence();
  value.applets.bootstrapSourceSha256 = createHash('sha256')
    .update(readFileSync(join(root, 'scripts/db-bootstrap-roles.sh'))).digest('hex');
  const result = validate(value);
  assert.equal(result.status, 0, 'probe rejects the current bootstrap source inventory');
});

test('accepts a root-level Cockroach third-party notice as donor licensing evidence', () => {
  const value = evidence();
  const notice = { path: '/cockroach/THIRD-PARTY-NOTICES.txt', sha256: '4'.repeat(64) };
  value.donor.licenses = [notice];
  value.donor.nativeDecisions[2] = { ...notice, decision: 'exclude', reason: 'license-copied-separately' };
  const result = validate(value);
  assert.equal(result.status, 0, result.stderr);
});

test('donor notice inventory hashes conventional root-level files and skips unrelated text', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-licenses-'));
  try {
    const files = {
      'cockroach/LICENSE': 'license bytes\n',
      'cockroach/THIRD-PARTY-NOTICES.txt': 'third party notices\n',
      'cockroach/NOTICES': 'additional notices\n',
      'cockroach/README.md': 'unrelated instructions\n',
      'licenses/MIT': 'MIT text\n',
    };
    for (const [name, content] of Object.entries(files)) {
      const path = join(dir, name);
      mkdirSync(join(path, '..'), { recursive: true });
      writeFileSync(path, content);
    }
    const result = spawnSync('sh', [script, '--inspect-donor-licenses', dir], { cwd: root, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    const got = JSON.parse(result.stdout);
    const expected = ['cockroach/LICENSE', 'cockroach/THIRD-PARTY-NOTICES.txt', 'cockroach/NOTICES', 'licenses/MIT']
      .map(name => ({ path: `/${name}`, sha256: createHash('sha256').update(files[name]).digest('hex') }))
      .sort((a, b) => a.path.localeCompare(b.path));
    assert.deepEqual(got.sort((a, b) => a.path.localeCompare(b.path)), expected);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

for (const [name, mutate] of [
  ['mutable-only runtime reference', e => { delete e.runtime.amd64Digest; }],
  ['old UBI runtime source', e => { e.runtime.source = 'registry.access.redhat.com/ubi10/ubi-minimal:latest@sha256:e3a5632d7ae8a97e06f634522d06187f12793e90ac0d7b51bc671c83a96d8eda'; }],
  ['wrong runtime amd64 child', e => { e.runtime.amd64Digest = `sha256:${'a'.repeat(64)}`; }],
  ['non-amd64 runtime', e => { e.runtime.architecture = 'arm64'; }],
  ['empty APK catalog', e => { e.packages = []; }],
  ['hidden APK type', e => { e.packages[0].type = 'binary'; }],
  ['APK package name drift', e => { e.packages[0].name = 'other'; }],
  ['APK package version drift', e => { e.packages[0].version = '2.45-r0'; }],
  ['missing APK license', e => { e.licenseDeclarations = []; }],
  ['ambiguous APK license', e => { e.licenseDeclarations[0].license = 'UNKNOWN'; }],
  ['missing loader owner', e => { e.ownership.shift(); }],
  ['missing APK database', e => { delete e.packageDb; }],
  ['missing donor native files', e => { e.donor.nativeFiles = []; }],
  ['missing donor license', e => { e.donor.licenses = []; }],
  ['old RPM schema', e => { e.schema = 1; }],
  ['missing trust', e => { e.trust = []; }],
  ['High finding', e => { e.scan.matches = [{ severity: 'High', id: 'CVE-TEST', package: 'glibc', fixState: 'unknown' }]; }],
  ['Critical finding', e => { e.scan.matches = [{ severity: 'Critical', id: 'CVE-TEST', package: 'glibc', fixState: 'unknown' }]; }],
  ['wrong bootstrap source digest', e => { e.applets.bootstrapSourceSha256 = '0'.repeat(64); }],
  ['runtime index differs from pinned source', e => { e.runtime.indexDigest = `sha256:${'a'.repeat(64)}`; }],
  ['donor source differs from pinned donor', e => { e.donor.source = e.runtime.source; }],
  ['wrong source SHA', e => { e.sourceSha = '0'.repeat(40); }],
  ['secret-bearing evidence', e => { e.password = 'do-not-upload'; }],
  ['missing tool versions', e => { delete e.toolVersions; }],
  ['incomplete BusyBox applets', e => { e.applets.names = ['sh']; }],
  ['malformed scan match', e => { e.scan.matches = [{}]; }],
  ['unrelated native file', e => { e.donor.nativeFiles.push({ path: '/cockroach/unrelated', sha256: '3'.repeat(64) }); }],
  ['missing transitive closure', e => { e.donor.elfClosure.pop(); }],
  ['unowned transitive library', e => { e.ownership = e.ownership.filter(item => !item.path.endsWith('/libc.so.6')); }],
  ['OS-only donor license', e => { e.donor.licenses = [{ path: '/usr/share/licenses/glibc/LICENSES', sha256: 'd'.repeat(64) }]; }],
  ['missing native inclusion decision', e => { e.donor.nativeDecisions = e.donor.nativeDecisions.filter(item => item.path !== '/cockroach/lib/libvendor.so'); }],
  ['vendor ELF marked excluded', e => { e.donor.nativeDecisions[1].decision = 'exclude'; }],
]) {
  test(`rejects ${name} even with otherwise clean scan`, () => {
    const value = evidence();
    mutate(value);
    const result = validate(value);
    assert.notEqual(result.status, 0, `accepted ${name}`);
  });
}

test('workflow is dispatch-only, read-only, and uploads a single sanitized file', () => {
  const workflow = readFileSync(join(root, '.github/workflows/restore-runtime-probe.yml'), 'utf8');
  assert.match(workflow, /workflow_dispatch:/);
  assert.match(workflow, /contents: read/);
  assert.match(workflow, /persist-credentials: false/);
  assert.match(workflow, /nix develop \.#images --command sh scripts\/probe-restore-runtime-candidate\.sh/);
  assert.match(workflow, /candidate\.json/);
  assert.doesNotMatch(workflow, /\b(secrets|packages: write|docker login|skopeo copy .*docker:\/\/.*docker:\/\/|publish|deploy)\b/i);
  assert.doesNotMatch(workflow, /workflow_call:|push:|pull_request:|inputs:/);
});

test('unsupported host writes a sanitized rejected artifact without registry access', () => {
  if (process.platform === 'linux' && process.arch === 'x64') return;
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-host-'));
  try {
    const path = join(dir, 'candidate.json');
    const result = spawnSync('sh', [script, path], { cwd: root, encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    const rejected = JSON.parse(readFileSync(path, 'utf8'));
    assert.deepEqual(rejected, { schema: 2, sourceSha, status: 'rejected', phase: 'inspection' });
    assert.doesNotMatch(JSON.stringify(rejected) + result.stderr, /password|token|secret/i);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test('failure replaces a stale candidate atomically', () => {
  if (process.platform === 'linux' && process.arch === 'x64') return;
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-stale-'));
  try {
    const path = join(dir, 'candidate.json');
    writeFileSync(path, JSON.stringify(evidence()));
    const result = spawnSync('sh', [script, path], { cwd: root, encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 2, sourceSha, status: 'rejected', phase: 'inspection' });
    assert.equal(readdirSync(dir).length, 1, 'temporary evidence file remains');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('malformed scanner-shaped JSON cannot appear in stderr', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-malformed-'));
  try {
    const path = join(dir, 'scan.json');
    writeFileSync(path, '{"matches":[{"password":"SENSITIVE-MARKER"}]}');
    const result = spawnSync('sh', [script, '--validate-evidence', path], { cwd: root, encoding: 'utf8' });
    assert.notEqual(result.status, 0);
    assert.doesNotMatch(result.stderr, /SENSITIVE-MARKER|password|matches/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('early invalid runtime digest omits runtime metadata and raw diagnostics', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-tool-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    const platform = join(dir, 'platform.cjs');
    writeFileSync(platform, "Object.defineProperty(process, 'platform', {value:'linux'}); Object.defineProperty(process, 'arch', {value:'x64'});\n");
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, '#!/bin/sh\nprintf \'%s\\n\' \'{"password":"SENSITIVE-MARKER","manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"sha256:invalid"}]}\'\n');
    chmodSync(skopeo, 0o755);
    const path = join(dir, 'candidate.json');
    writeFileSync(path, JSON.stringify(evidence()));
    const result = spawnSync('sh', [script, path], {
      cwd: root, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${platform}` },
    });
    assert.notEqual(result.status, 0);
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage: 'runtime-index' });
    assert.doesNotMatch(result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER|password/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('early malformed runtime JSON atomically replaces stale evidence without leaking diagnostics', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-malformed-runtime-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    const platform = join(dir, 'platform.cjs');
    writeFileSync(platform, "Object.defineProperty(process, 'platform', {value:'linux'}); Object.defineProperty(process, 'arch', {value:'x64'});\n");
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, '#!/bin/sh\nprintf \'{"password":"SENSITIVE-MARKER"\'\n');
    chmodSync(skopeo, 0o755);
    const path = join(dir, 'candidate.json');
    writeFileSync(path, JSON.stringify(evidence()));
    const result = spawnSync('sh', [script, path], {
      cwd: root, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${platform}` },
    });
    assert.notEqual(result.status, 0);
    const rejected = readFileSync(path, 'utf8');
    assert.deepEqual(JSON.parse(rejected),
      { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage: 'runtime-index' });
    assert.deepEqual(readdirSync(dir).sort(), ['bin', 'candidate.json', 'platform.cjs']);
    assert.doesNotMatch(result.stdout + result.stderr + rejected, /SENSITIVE-MARKER|password|SyntaxError/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('donor raw inspection uses a digest-only reference before parsing tool output', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-donor-tool-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    const platform = join(dir, 'platform.cjs');
    writeFileSync(platform, pinnedLinuxPreload());
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  ${runtimeSkopeoRef}) printf '%s\\n' '${runtimeManifestRaw.trimEnd()}' ;;
  ${donorSkopeoRef})
    printf '%s\\n' "$3" > "$PROBE_REF_TRACE"
    printf '%s\\n' '{"password":"SENSITIVE-MARKER"' ;;
  *) printf '%s\\n' "$3" > "$PROBE_REF_TRACE"
     printf '%s\\n' 'SENSITIVE-MARKER' >&2
     exit 31 ;;
esac
`);
    chmodSync(skopeo, 0o755);
    const path = join(dir, 'candidate.json');
    const trace = join(dir, 'donor-reference');
    writeFileSync(path, JSON.stringify(evidence()));
    const result = spawnSync('sh', [script, path], {
      cwd: root, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${platform}`,
        PROBE_REF_TRACE: trace },
    });
    assert.notEqual(result.status, 0);
    assert.equal(readFileSync(trace, 'utf8').trim(), donorSkopeoRef);
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage: 'donor-manifest-parse', runtime: runtimeMetadata });
    assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER|password/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

for (const [name, donorCommand, stage] of [
  ['fetch', "printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 31", 'donor-raw-fetch'],
  ['pin mismatch', `printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}'`, 'donor-pin-check'],
]) {
  test(`donor ${name} failure reports a fixed stage without leaking tool output`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-donor-stage-'));
    try {
      const bin = join(dir, 'bin');
      mkdirSync(bin);
      const platform = join(dir, 'platform.cjs');
      writeFileSync(platform, pinnedLinuxPreload());
      const skopeo = join(bin, 'skopeo');
      writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  ${runtimeSkopeoRef}) printf '%s\\n' '${runtimeManifestRaw.trimEnd()}' ;;
  ${donorSkopeoRef}) ${donorCommand} ;;
  *) printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 32 ;;
esac
`);
      chmodSync(skopeo, 0o755);
      const path = join(dir, 'candidate.json');
      writeFileSync(path, JSON.stringify(evidence()));
      const result = spawnSync('sh', [script, path], {
        cwd: root, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${platform}`,
          },
      });
      assert.notEqual(result.status, 0);
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
        { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata });
      assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER/i);
    } finally { rmSync(dir, { recursive: true, force: true }); }
  });
}

for (const [name, donorManifest, stage, requestCount] of [
  ['amd64 child selection', `{"donor-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"arm64"},"digest":"${digest}"}]}`, 'donor-amd64-child', 1],
  ['single-manifest fallback', '{"donor-test-index":true,"schemaVersion":2}', 'donor-fallback-inspect', 2],
]) {
  test(`donor ${name} failure reports a fixed stage`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-donor-platform-'));
    try {
      const bin = join(dir, 'bin');
      mkdirSync(bin);
      const preload = join(dir, 'preload.cjs');
      writeFileSync(preload, pinnedLinuxPreload());
      const skopeo = join(bin, 'skopeo');
      writeFileSync(skopeo, `#!/bin/sh
ref=$3
if [ "$2" != --raw ]; then ref=$2; fi
case "$ref" in
  ${runtimeSkopeoRef}) printf '%s\\n' '${runtimeManifestRaw.trimEnd()}' ;;
  ${donorSkopeoRef})
    printf '%s\\n' "$ref" >> "$PROBE_REF_TRACE"
    if [ "$2" = --raw ]; then
      printf '%s\\n' '${donorManifest}'
    else
      printf '%s\\n' '{"password":"SENSITIVE-MARKER"'
    fi ;;
  *) printf '%s\\n' "$ref" >> "$PROBE_REF_TRACE"
     printf '%s\\n' 'SENSITIVE-MARKER' >&2
     exit 31 ;;
esac
`);
      chmodSync(skopeo, 0o755);
      const path = join(dir, 'candidate.json');
      const trace = join(dir, 'donor-references');
      writeFileSync(path, JSON.stringify(evidence()));
      const result = spawnSync('sh', [script, path], {
        cwd: root, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${preload}`,
          PROBE_REF_TRACE: trace },
      });
      assert.notEqual(result.status, 0);
      assert.deepEqual(readFileSync(trace, 'utf8').trim().split('\n'),
        Array(requestCount).fill(donorSkopeoRef));
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
        { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata });
      assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER|password/i);
    } finally { rmSync(dir, { recursive: true, force: true }); }
  });
}

test('scratch allocation failure reports its own stage without leaking the exception', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-scratch-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    const preload = join(dir, 'preload.cjs');
    writeFileSync(preload, pinnedLinuxPreload(`const fs = require('node:fs');
const originalMkdtemp = fs.mkdtempSync;
fs.mkdtempSync = function (prefix, ...rest) {
  if (prefix.includes('jandibat-runtime-probe-')) {
    fs.writeFileSync(process.env.SCRATCH_REACHED_FILE, '1');
    throw new Error('SENSITIVE-MARKER');
  }
  return originalMkdtemp.call(this, prefix, ...rest);
};`));
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  ${runtimeSkopeoRef}) printf '%s\\n' '${runtimeManifestRaw.trimEnd()}' ;;
  ${donorSkopeoRef}) printf '%s\\n' '${donorManifestRaw.trimEnd()}' ;;
  *) printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 31 ;;
esac
`);
    chmodSync(skopeo, 0o755);
    const path = join(dir, 'candidate.json');
    const scratchReached = join(dir, 'scratch-reached');
    writeFileSync(path, JSON.stringify(evidence()));
    const result = spawnSync('sh', [script, path], {
      cwd: root, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${preload}`,
        SCRATCH_REACHED_FILE: scratchReached },
    });
    assert.notEqual(result.status, 0);
    assert.equal(readFileSync(scratchReached, 'utf8'), '1', 'probe did not reach scratch allocation');
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage: 'scratch-allocation', runtime: runtimeMetadata });
    assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

for (const [missing, stage] of [
  ['complete', null],
  ['readonly-cleanup', null],
  ['cleanup-eacces', 'scratch-cleanup'],
  ['cleanup-unknown', 'scratch-cleanup'],
  ['trust-and-cleanup', 'scratch-cleanup'],
  ['repair-chmod-fails', 'scratch-cleanup'],
  ['repair-retry-fails', 'scratch-cleanup'],
  ['cleanup-nonpermission', 'scratch-cleanup'],
  ['apkdb', 'apkdb-inventory'],
  ['empty-apk-catalog', 'package-catalog'],
  ['syft-version-drift', 'package-catalog'],
  ['loader-owner', 'package-ownership'],
  ['trust', 'trust-inventory'],
  ['scan', 'scan-evidence'],
  ['db-status', 'scan-evidence'],
  ['tool-versions', 'tool-versions'],
  ['identity', 'dossier-identity'],
  ['donor-license', 'donor-native-license'],
  ['elf-closure', 'elf-closure'],
  ['ownership', 'package-ownership'],
  ['empty-version', 'tool-version-validation'],
]) {
  const label = { scan: 'scanner exit without finding', 'db-status': 'Grype database status failure',
    'tool-versions': 'tool version failure', complete: 'synthetic split-image dossier',
    'readonly-cleanup': 'read-only unpack cleanup preserving outside symlink target',
    'cleanup-eacces': 'EACCES scratch cleanup failure after validation',
    'cleanup-unknown': 'unknown scratch cleanup failure after validation',
    'trust-and-cleanup': 'scratch cleanup failure with pending trust rejection',
    'repair-chmod-fails': 'repair chmod failure',
    'repair-retry-fails': 'repair retry failure',
    'cleanup-nonpermission': 'non-permission cleanup failure without repair',
    identity: 'invalid dossier identity', 'donor-license': 'missing donor license',
    'elf-closure': 'invalid ELF dependency name', ownership: 'unaccounted package owner',
    'empty-version': 'empty collected tool version' }[missing] ?? `missing ${missing} evidence`;
  test(['complete', 'readonly-cleanup'].includes(missing)
    ? `${label} yields a coherent candidate dossier` : `${label} reports only its fixed stage`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-evidence-'));
    const repairInjected = ['repair-chmod-fails', 'repair-retry-fails', 'cleanup-nonpermission'].includes(missing);
    try {
      const runtime = join(dir, 'runtime-image');
      const donor = join(dir, 'donor-image');
      const files = [
        [join(runtime, 'etc/os-release'), 'ID="wolfi"\nVERSION_ID="20230201"\n'],
        [join(runtime, 'lib64/ld-linux-x86-64.so.2'), 'loader'],
        [join(runtime, 'usr/lib64/libc.so.6'), 'libc'],
        [join(runtime, 'usr/share/licenses/glibc/LICENSES'), 'base license'],
        [join(donor, 'cockroach/cockroach'), 'cockroach'],
        [join(donor, 'cockroach/lib/libvendor.so'), 'vendor library'],
      ];
      if (missing !== 'donor-license') files.push([join(donor, 'cockroach/licenses/LICENSE'), 'donor license']);
      if (missing === 'elf-closure') files.push([join(runtime, 'usr/lib64/notso'), 'invalid ELF dependency fixture']);
      if (missing !== 'apkdb') files.push([join(runtime, 'usr/lib/apk/db/installed'), `P:glibc\nV:2.44-r0\nL:LGPL-2.1-or-later\nF:lib64\n${missing === 'loader-owner' ? '' : 'R:ld-linux-x86-64.so.2\n'}F:usr/lib64\n${missing === 'ownership' ? '' : 'R:libc.so.6\n'}`]);
      if (missing !== 'trust' && missing !== 'trust-and-cleanup') files.push([join(runtime, 'etc/ssl/certs/ca-certificates.crt'), 'trust']);
      for (const [path, bytes] of files) {
        mkdirSync(join(path, '..'), { recursive: true });
        writeFileSync(path, bytes);
      }
      const preload = join(dir, 'preload.cjs');
      const cleanupInjected = missing === 'cleanup-eacces' || missing === 'cleanup-unknown' || missing === 'trust-and-cleanup';
      const preloadExtra = missing === 'identity' ? `const originalKeys = Object.keys;
Object.keys = function (value) {
  const keys = originalKeys(value);
  return value && value.schema === 2 && value.runtime && value.donor && value.packages && value.status === 'candidate'
    ? [...keys, 'unexpected-field'] : keys;
};` : missing === 'readonly-cleanup' ? `const fs = require('node:fs');
const { tmpdir } = require('node:os');
const originalRm = fs.rmSync;
let firstScratchRemoval = true;
fs.rmSync = function (path, options) {
  if (firstScratchRemoval && String(path).startsWith(tmpdir() + '/jandibat-runtime-probe-')) {
    firstScratchRemoval = false;
    throw Object.assign(new Error('SENSITIVE-MARKER'), { code: 'EACCES' });
  }
  return originalRm.call(this, path, options);
};` : repairInjected ? `const fs = require('node:fs');
const { tmpdir } = require('node:os');
const originalRm = fs.rmSync;
const originalFchmod = fs.fchmodSync;
let scratchAttempts = 0;
fs.rmSync = function (path, options) {
  if (String(path).startsWith(tmpdir() + '/jandibat-runtime-probe-')) {
    scratchAttempts++;
    fs.writeFileSync(process.env.PROBE_CLEANUP_TRACE, String(path));
    if (scratchAttempts === 1) throw Object.assign(new Error('SENSITIVE-MARKER'), {
      code: process.env.PROBE_REPAIR_KIND === 'nonpermission' ? 'EIO' : 'EACCES' });
    if (process.env.PROBE_REPAIR_KIND === 'retry') throw Object.assign(new Error('SENSITIVE-MARKER'), { code: 'EBUSY' });
  }
  return originalRm.call(this, path, options);
};
fs.fchmodSync = function (fd, mode) {
  fs.writeFileSync(process.env.PROBE_REPAIR_MARKER, 'repair attempted');
  if (process.env.PROBE_REPAIR_KIND === 'chmod') throw Object.assign(new Error('SENSITIVE-MARKER'), { code: 'EIO' });
  return originalFchmod.call(this, fd, mode);
};` : cleanupInjected ? `const fs = require('node:fs');
const { tmpdir } = require('node:os');
const originalRm = fs.rmSync;
fs.rmSync = function (path, options) {
  if (String(path).startsWith(tmpdir() + '/jandibat-runtime-probe-')) {
    if (process.env.PROBE_CLEANUP_CODE === 'EACCES') fs.writeFileSync(process.env.PROBE_CLEANUP_TRACE, String(path));
    else {
      originalRm.call(this, path, options);
      fs.writeFileSync(process.env.PROBE_CLEANUP_TRACE, '1');
    }
    throw Object.assign(new Error('SENSITIVE-MARKER'), { code: process.env.PROBE_CLEANUP_CODE });
  }
  return originalRm.call(this, path, options);
};` : '';
      writeFileSync(preload, pinnedLinuxPreload(preloadExtra));
      const bin = join(dir, 'bin');
      mkdirSync(bin);
      const fakeTool = `#!/bin/sh
tool=$(basename "$0")
if [ "$1" = --version ]; then
  if [ "$tool" = syft ] && [ "$PROBE_FAIL_VERSION" = 1 ]; then
    printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 35
  fi
  if [ "$tool" = syft ] && [ "$PROBE_EMPTY_VERSION" = 1 ]; then exit 0; fi
  printf '%s\\n' 'fixture version'; exit 0
fi
case "$tool" in
  skopeo)
    if [ "$1" = copy ]; then printf '%s\\n' "$2" >> "$PROBE_COPY_TRACE"; exit 0; fi
    case "$3" in
      ${runtimeSkopeoRef}) printf '%s\\n' '${runtimeManifestRaw.trimEnd()}' ;;
      ${donorSkopeoRef}) printf '%s\\n' '${donorManifestRaw.trimEnd()}' ;;
      *) printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 31 ;;
    esac ;;
  umoci)
    mkdir -p "$5/rootfs"
    case "$5" in
      */runtime/bundle) cp -R "$PROBE_FIXTURE_RUNTIME/." "$5/rootfs/" ;;
      */donor/bundle) cp -R "$PROBE_FIXTURE_DONOR/." "$5/rootfs/" ;;
      *) exit 32 ;;
    esac
    if [ "$PROBE_READONLY_UNPACK" = 1 ]; then
      mkdir -p "$5/rootfs/cockroach/readonly/nested"
      printf '%s\\n' 'fixture' > "$5/rootfs/cockroach/readonly/nested/file"
      ln -s "$PROBE_OUTSIDE_SENTINEL" "$5/rootfs/cockroach/outside-link"
      chmod 0500 "$5/rootfs/cockroach/readonly/nested"
      chmod 0500 "$5/rootfs/cockroach/readonly"
      chmod 0500 "$5/rootfs/cockroach"
    fi ;;
  syft)
    output=$(printf '%s' "$3" | cut -d= -f2-)
    if [ "$PROBE_EMPTY_APK_CATALOG" = 1 ]; then
      printf '%s\\n' '{"artifacts":[]}' > "$output"
    elif [ "$PROBE_SYFT_VERSION_DRIFT" = 1 ]; then
      printf '%s\\n' '{"artifacts":[{"type":"apk","name":"glibc","version":"2.45-r0"}]}' > "$output"
    else
      printf '%s\\n' '{"artifacts":[{"type":"apk","name":"glibc","version":"2.44-r0"}]}' > "$output"
    fi ;;
  grype)
    if [ "$1" = db ]; then
      if [ "$PROBE_FAIL_DB_STATUS" = 1 ]; then printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 36; fi
      printf '%s\\n' 'Built: 2026-09-27T00:00:00Z';
    else printf '%s\\n' '{"matches":[]}'
      if [ "$PROBE_FAIL_SCAN" = 1 ]; then exit 34; fi
    fi ;;
  readelf)
    case "$1" in
      -h) case "$2" in */cockroach|*/libvendor.so) printf '%s\\n' 'Machine: Advanced Micro Devices X86-64' ;; *) exit 1 ;; esac ;;
      -l) case "$2" in */cockroach) printf '%s\\n' 'Requesting program interpreter: /lib64/ld-linux-x86-64.so.2]' ;; esac ;;
      -d) case "$2" in */cockroach) printf '%s\\n' '(NEEDED) Shared library: [libvendor.so]' ;; */libvendor.so)
        if [ "$PROBE_BAD_ELF" = 1 ]; then printf '%s\\n' '(NEEDED) Shared library: [notso]';
        else printf '%s\\n' '(NEEDED) Shared library: [libc.so.6]'; fi ;;
      esac ;;
    esac ;;
esac
`;
      for (const name of ['skopeo', 'umoci', 'syft', 'grype', 'readelf']) {
        const path = join(bin, name);
        writeFileSync(path, fakeTool);
        chmodSync(path, 0o755);
      }
      const path = join(dir, 'candidate.json');
      const copyTrace = join(dir, 'copies');
      const cleanupTrace = join(dir, 'cleanup-called');
      const repairMarker = join(dir, 'repair-attempted');
      const outsideSentinel = join(dir, 'outside-sentinel');
      writeFileSync(outsideSentinel, 'outside untouched\n');
      writeFileSync(path, JSON.stringify(evidence()));
      const result = spawnSync('sh', [script, path], {
        cwd: root, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${preload}`,
          PROBE_FIXTURE_RUNTIME: runtime, PROBE_FIXTURE_DONOR: donor,
          PROBE_READONLY_UNPACK: missing === 'readonly-cleanup' ? '1' : '0',
          PROBE_OUTSIDE_SENTINEL: outsideSentinel,
          PROBE_COPY_TRACE: copyTrace, PROBE_CLEANUP_TRACE: cleanupTrace,
          PROBE_REPAIR_MARKER: repairMarker,
          PROBE_REPAIR_KIND: missing === 'repair-chmod-fails' ? 'chmod'
            : missing === 'repair-retry-fails' ? 'retry' : 'nonpermission',
          PROBE_CLEANUP_CODE: missing === 'cleanup-unknown' ? 'SENSITIVE-MARKER' : 'EACCES',
          PROBE_FAIL_SCAN: missing === 'scan' ? '1' : '0',
          PROBE_EMPTY_APK_CATALOG: missing === 'empty-apk-catalog' ? '1' : '0',
          PROBE_SYFT_VERSION_DRIFT: missing === 'syft-version-drift' ? '1' : '0',
          PROBE_FAIL_DB_STATUS: missing === 'db-status' ? '1' : '0',
          PROBE_FAIL_VERSION: missing === 'tool-versions' ? '1' : '0',
          PROBE_EMPTY_VERSION: missing === 'empty-version' ? '1' : '0',
          PROBE_BAD_ELF: missing === 'elf-closure' ? '1' : '0' },
      });
      assert.deepEqual(readFileSync(copyTrace, 'utf8').trim().split('\n'),
        [runtimeChildRef, donorChildRef]);
      if (missing === 'cleanup-unknown') assert.equal(readFileSync(cleanupTrace, 'utf8'), '1');
      if (missing === 'cleanup-eacces' || missing === 'trust-and-cleanup')
        assert.ok(readFileSync(cleanupTrace, 'utf8').startsWith(join(tmpdir(), 'jandibat-runtime-probe-')));
      if (missing === 'cleanup-nonpermission') assert.equal(readdirSync(dir).includes('repair-attempted'), false);
      if (missing === 'complete' || missing === 'readonly-cleanup') {
        assert.equal(result.status, 0, `${result.stderr} ${readFileSync(path, 'utf8')}`);
        const dossier = JSON.parse(readFileSync(path, 'utf8'));
        assert.equal(dossier.status, 'candidate');
        assert.equal(dossier.runtime.source, candidateSource);
        assert.equal(dossier.donor.source, donorSource);
        assert.equal(dossier.runtime.indexDigest, runtimeIndexDigest);
        assert.equal(dossier.runtime.amd64Digest, digest);
        assert.equal(dossier.donor.amd64Digest, donorChildDigest);
        assert.notEqual(dossier.runtime.amd64Digest, dossier.donor.amd64Digest);
        assert.equal(dossier.packageManager, 'apk');
        assert.equal(dossier.packageDb.path, '/usr/lib/apk/db/installed');
        assert.ok(dossier.licenseDeclarations.length && dossier.trust.length && dossier.donor.nativeFiles.length);
        if (missing === 'readonly-cleanup') assert.equal(readFileSync(outsideSentinel, 'utf8'), 'outside untouched\n');
      } else {
        assert.notEqual(result.status, 0);
        assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
          { schema: 2, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata,
            ...(cleanupInjected || repairInjected ? { cleanupFailure: missing === 'cleanup-unknown' || missing === 'repair-chmod-fails' || missing === 'cleanup-nonpermission'
              ? 'unknown' : missing === 'repair-retry-fails' ? 'busy' : 'permission-denied' } : {}) });
      }
      assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER/i);
    } finally {
      const trace = join(dir, 'cleanup-called');
      if (readdirSync(dir).includes('cleanup-called') &&
          (repairInjected || missing === 'cleanup-eacces' || missing === 'trust-and-cleanup')) {
        const scratch = readFileSync(trace, 'utf8');
        if (scratch.startsWith(join(tmpdir(), 'jandibat-runtime-probe-'))) rmSync(scratch, { recursive: true, force: true });
      }
      rmSync(dir, { recursive: true, force: true });
    }
  });
}
