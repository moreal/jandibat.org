import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../', import.meta.url));
const script = join(root, 'scripts/probe-restore-runtime-candidate.sh');
const sourceSha = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim();
const runtimeIndexDigest = 'sha256:e3a5632d7ae8a97e06f634522d06187f12793e90ac0d7b51bc671c83a96d8eda';
const digest = 'sha256:d22eab33a72231f57ca04dbb296eb0d1d248bd37b8d5edb6bb18f4bbb80517ea';
const donorDigest = 'sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
const donorChildDigest = `sha256:${'b'.repeat(64)}`;
const candidateSource = `registry.access.redhat.com/ubi10/ubi-minimal:latest@${runtimeIndexDigest}`;
const donorSource = `cockroachdb/cockroach:v26.2.5@${donorDigest}`;
const bootstrapDigest = '6ef912d18d9f40e46546ffcf5777cecf93405b3f5f4dcde60f2878c10bdc2327';
const runtimeManifestRaw = `{"runtime-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}\n`;
const donorManifestRaw = `{"donor-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${donorChildDigest}"}]}\n`;
const runtimeMetadata = { source: candidateSource, indexDigest: runtimeIndexDigest, amd64Digest: digest };
const runtimeSkopeoRef = `docker://registry.access.redhat.com/ubi10/ubi-minimal@${runtimeIndexDigest}`;
const donorSkopeoRef = `docker://cockroachdb/cockroach@${donorDigest}`;
const runtimeChildRef = `docker://registry.access.redhat.com/ubi10/ubi-minimal@${digest}`;
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
    schema: 1, sourceSha, status: 'candidate',
    runtime: { source: candidateSource, indexDigest: runtimeIndexDigest,
      amd64Digest: digest, os: 'linux', architecture: 'amd64',
      osRelease: { ID: 'rhel', VERSION_ID: '10.0' } },
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
    packages: [{ name: 'glibc', version: '2.39', type: 'rpm' }],
    rpmdb: [{ path: '/usr/lib/sysimage/rpm/rpmdb.sqlite', sha256: 'e'.repeat(64) }],
    ownership: [{ path: '/lib64/ld-linux-x86-64.so.2', package: 'glibc' },
      { path: '/usr/lib64/libc.so.6', package: 'glibc' }],
    trust: [{ path: '/etc/pki/tls/certs/ca-bundle.crt', sha256: 'f'.repeat(64) }],
    baseLicenses: [{ path: '/usr/share/licenses/glibc/LICENSES', sha256: '1'.repeat(64) }],
    applets: { bootstrapSourceSha256: bootstrapDigest,
      names: ['awk', 'chmod', 'cp', 'mktemp', 'rm', 'sed', 'sha256sum', 'sh', 'tail', 'tr'] },
    scan: { tool: 'grype', failOn: 'high', matches: [], databaseBuilt: '2026-09-26T00:00:00Z' },
    toolVersions: { skopeo: '1.24', syft: '1.0', grype: '1.0', umoci: '0.4', readelf: '2.42', rpm: '4.19' },
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
  ['non-amd64 runtime', e => { e.runtime.architecture = 'arm64'; }],
  ['empty RPM catalog', e => { e.packages = []; }],
  ['hidden RPM type', e => { e.packages[0].type = 'binary'; }],
  ['missing loader owner', e => { e.ownership.shift(); }],
  ['missing rpmdb', e => { e.rpmdb = []; }],
  ['missing donor native files', e => { e.donor.nativeFiles = []; }],
  ['missing donor license', e => { e.donor.licenses = []; }],
  ['missing base license', e => { e.baseLicenses = []; }],
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
    assert.deepEqual(rejected, { schema: 1, sourceSha, status: 'rejected', phase: 'inspection' });
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection' });
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'runtime-index' });
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'runtime-index' });
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'donor-manifest-parse', runtime: runtimeMetadata });
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
        { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata });
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
        { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata });
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'scratch-allocation', runtime: runtimeMetadata });
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
  ['rpmdb', 'rpmdb-inventory'],
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
        [join(runtime, 'etc/os-release'), 'ID="rhel"\nVERSION_ID="10.0"\n'],
        [join(runtime, 'lib64/ld-linux-x86-64.so.2'), 'loader'],
        [join(runtime, 'usr/lib64/libc.so.6'), 'libc'],
        [join(runtime, 'usr/share/licenses/glibc/LICENSES'), 'base license'],
        [join(donor, 'cockroach/cockroach'), 'cockroach'],
        [join(donor, 'cockroach/lib/libvendor.so'), 'vendor library'],
      ];
      if (missing !== 'donor-license') files.push([join(donor, 'cockroach/licenses/LICENSE'), 'donor license']);
      if (missing === 'elf-closure') files.push([join(runtime, 'usr/lib64/notso'), 'invalid ELF dependency fixture']);
      if (missing !== 'rpmdb') files.push([join(runtime, 'usr/lib/sysimage/rpm/rpmdb.sqlite'), 'rpmdb']);
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
  return value && value.schema === 1 && value.runtime && value.donor && value.packages && value.status === 'candidate'
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
    printf '%s\\n' '{"artifacts":[{"type":"rpm","name":"glibc","version":"2.39"}]}' > "$output" ;;
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
  rpm) if [ "$PROBE_BAD_OWNER" = 1 ]; then printf '%s' 'missing-package'; else printf '%s' 'glibc'; fi ;;
esac
`;
      for (const name of ['skopeo', 'umoci', 'syft', 'grype', 'readelf', 'rpm']) {
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
          PROBE_FAIL_DB_STATUS: missing === 'db-status' ? '1' : '0',
          PROBE_FAIL_VERSION: missing === 'tool-versions' ? '1' : '0',
          PROBE_EMPTY_VERSION: missing === 'empty-version' ? '1' : '0',
          PROBE_BAD_OWNER: missing === 'ownership' ? '1' : '0',
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
        assert.ok(dossier.rpmdb.length && dossier.trust.length && dossier.donor.nativeFiles.length);
        if (missing === 'readonly-cleanup') assert.equal(readFileSync(outsideSentinel, 'utf8'), 'outside untouched\n');
      } else {
        assert.notEqual(result.status, 0);
        assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
          { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage, runtime: runtimeMetadata,
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
