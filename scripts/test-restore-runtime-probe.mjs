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
const digest = `sha256:${'a'.repeat(64)}`;
const donorDigest = 'sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
const bootstrapDigest = '6ef912d18d9f40e46546ffcf5777cecf93405b3f5f4dcde60f2878c10bdc2327';

function evidence() {
  return {
    schema: 1, sourceSha, status: 'candidate',
    runtime: { source: 'registry.access.redhat.com/ubi10/ubi-micro', indexDigest: digest,
      amd64Digest: digest, os: 'linux', architecture: 'amd64',
      osRelease: { ID: 'rhel', VERSION_ID: '10.0' } },
    donor: { source: `cockroachdb/cockroach:v26.2.5@${donorDigest}`,
      amd64Digest: donorDigest, interpreter: '/lib64/ld-linux-x86-64.so.2',
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

test('malformed registry tool output replaces the artifact without leaking diagnostics', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-tool-'));
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
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'runtime-index' });
    assert.doesNotMatch(result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER|password/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('donor raw inspection uses a digest-only reference before parsing tool output', () => {
  const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-donor-tool-'));
  try {
    const bin = join(dir, 'bin');
    mkdirSync(bin);
    const platform = join(dir, 'platform.cjs');
    writeFileSync(platform, "Object.defineProperty(process, 'platform', {value:'linux'}); Object.defineProperty(process, 'arch', {value:'x64'});\n");
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  docker://registry.access.redhat.com/ubi10/ubi-micro)
    printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
  docker://cockroachdb/cockroach@${donorDigest})
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
    assert.equal(readFileSync(trace, 'utf8').trim(), `docker://cockroachdb/cockroach@${donorDigest}`);
    assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'donor-manifest-parse' });
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
      writeFileSync(platform, "Object.defineProperty(process, 'platform', {value:'linux'}); Object.defineProperty(process, 'arch', {value:'x64'});\n");
      const skopeo = join(bin, 'skopeo');
      writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  docker://registry.access.redhat.com/ubi10/ubi-micro)
    printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
  *) ${donorCommand} ;;
esac
`);
      chmodSync(skopeo, 0o755);
      const path = join(dir, 'candidate.json');
      writeFileSync(path, JSON.stringify(evidence()));
      const result = spawnSync('sh', [script, path], {
        cwd: root, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${platform}` },
      });
      assert.notEqual(result.status, 0);
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
        { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage });
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
      writeFileSync(preload, `const crypto = require('node:crypto');
const { syncBuiltinESMExports } = require('node:module');
Object.defineProperty(process, 'platform', { value: 'linux' });
Object.defineProperty(process, 'arch', { value: 'x64' });
const originalHash = crypto.createHash;
crypto.createHash = function (...args) {
  const hash = originalHash(...args);
  const originalUpdate = hash.update;
  const originalDigest = hash.digest;
  let donorIndex = false;
  hash.update = function (data, ...rest) {
    donorIndex = String(data).includes('donor-test-index');
    return originalUpdate.call(this, data, ...rest);
  };
  hash.digest = function (encoding) {
    return donorIndex && encoding === 'hex' ? '${donorDigest.slice(7)}' : originalDigest.call(this, encoding);
  };
  return hash;
};
syncBuiltinESMExports();
`);
      const skopeo = join(bin, 'skopeo');
      writeFileSync(skopeo, `#!/bin/sh
ref=$3
if [ "$2" != --raw ]; then ref=$2; fi
case "$ref" in
  docker://registry.access.redhat.com/ubi10/ubi-micro)
    printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
  docker://cockroachdb/cockroach@${donorDigest})
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
        Array(requestCount).fill(`docker://cockroachdb/cockroach@${donorDigest}`));
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
        { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage });
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
    writeFileSync(preload, `const fs = require('node:fs');
const crypto = require('node:crypto');
const { syncBuiltinESMExports } = require('node:module');
Object.defineProperty(process, 'platform', { value: 'linux' });
Object.defineProperty(process, 'arch', { value: 'x64' });
const originalHash = crypto.createHash;
crypto.createHash = function (...args) {
  const hash = originalHash(...args);
  const originalUpdate = hash.update;
  const originalDigest = hash.digest;
  let donorIndex = false;
  hash.update = function (data, ...rest) {
    donorIndex = String(data).includes('donor-test-index');
    return originalUpdate.call(this, data, ...rest);
  };
  hash.digest = function (encoding) {
    return donorIndex && encoding === 'hex' ? '${donorDigest.slice(7)}' : originalDigest.call(this, encoding);
  };
  return hash;
};
const originalMkdtemp = fs.mkdtempSync;
fs.mkdtempSync = function (prefix, ...rest) {
  if (prefix.includes('jandibat-runtime-probe-')) {
    fs.writeFileSync(process.env.SCRATCH_REACHED_FILE, '1');
    throw new Error('SENSITIVE-MARKER');
  }
  return originalMkdtemp.call(this, prefix, ...rest);
};
syncBuiltinESMExports();
`);
    const skopeo = join(bin, 'skopeo');
    writeFileSync(skopeo, `#!/bin/sh
case "$3" in
  docker://registry.access.redhat.com/ubi10/ubi-micro)
    printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
  *) printf '%s\\n' '{"donor-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
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
      { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage: 'scratch-allocation' });
    assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER/i);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

for (const [missing, stage] of [
  ['rpmdb', 'rpmdb-inventory'],
  ['trust', 'trust-inventory'],
  ['scan', 'scan-evidence'],
  ['db-status', 'scan-evidence'],
  ['tool-versions', 'tool-versions'],
]) {
  const label = { scan: 'scanner exit without finding', 'db-status': 'Grype database status failure',
    'tool-versions': 'tool version failure' }[missing] ?? `missing ${missing} evidence`;
  test(`${label} reports only its fixed stage`, () => {
    const dir = mkdtempSync(join(tmpdir(), 'jandibat-probe-evidence-'));
    try {
      const runtime = join(dir, 'runtime');
      const donor = join(dir, 'donor');
      const files = [
        [join(runtime, 'etc/os-release'), 'ID="rhel"\nVERSION_ID="10.0"\n'],
        [join(runtime, 'lib64/ld-linux-x86-64.so.2'), 'loader'],
        [join(runtime, 'usr/lib64/libc.so.6'), 'libc'],
        [join(runtime, 'usr/share/licenses/glibc/LICENSES'), 'base license'],
        [join(donor, 'cockroach/cockroach'), 'cockroach'],
        [join(donor, 'cockroach/lib/libvendor.so'), 'vendor library'],
        [join(donor, 'cockroach/licenses/LICENSE'), 'donor license'],
      ];
      if (missing !== 'rpmdb') files.push([join(runtime, 'usr/lib/sysimage/rpm/rpmdb.sqlite'), 'rpmdb']);
      if (missing !== 'trust') files.push([join(runtime, 'etc/ssl/certs/ca-certificates.crt'), 'trust']);
      for (const [path, bytes] of files) {
        mkdirSync(join(path, '..'), { recursive: true });
        writeFileSync(path, bytes);
      }
      const preload = join(dir, 'preload.cjs');
      writeFileSync(preload, `const crypto = require('node:crypto');
const { syncBuiltinESMExports } = require('node:module');
Object.defineProperty(process, 'platform', { value: 'linux' });
Object.defineProperty(process, 'arch', { value: 'x64' });
const originalHash = crypto.createHash;
crypto.createHash = function (...args) {
  const hash = originalHash(...args);
  const update = hash.update;
  const finish = hash.digest;
  let donorIndex = false;
  hash.update = function (data, ...rest) {
    donorIndex = String(data).includes('donor-test-index');
    return update.call(this, data, ...rest);
  };
  hash.digest = function (encoding) {
    return donorIndex && encoding === 'hex' ? '${donorDigest.slice(7)}' : finish.call(this, encoding);
  };
  return hash;
};
syncBuiltinESMExports();
`);
      const bin = join(dir, 'bin');
      mkdirSync(bin);
      const fakeTool = `#!/bin/sh
tool=$(basename "$0")
if [ "$1" = --version ]; then
  if [ "$tool" = syft ] && [ "$PROBE_FAIL_VERSION" = 1 ]; then
    printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 35
  fi
  printf '%s\\n' 'fixture version'; exit 0
fi
case "$tool" in
  skopeo)
    if [ "$1" = copy ]; then exit 0; fi
    case "$3" in
      docker://registry.access.redhat.com/ubi10/ubi-micro)
        printf '%s\\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
      docker://cockroachdb/cockroach@${donorDigest})
        printf '%s\\n' '{"donor-test-index":true,"manifests":[{"platform":{"os":"linux","architecture":"amd64"},"digest":"${digest}"}]}' ;;
      *) printf '%s\\n' 'SENSITIVE-MARKER' >&2; exit 31 ;;
    esac ;;
  umoci)
    mkdir -p "$5/rootfs"
    case "$5" in
      */runtime/bundle) cp -R "$PROBE_FIXTURE_RUNTIME/." "$5/rootfs/" ;;
      */donor/bundle) cp -R "$PROBE_FIXTURE_DONOR/." "$5/rootfs/" ;;
      *) exit 32 ;;
    esac ;;
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
      -d) case "$2" in */cockroach) printf '%s\\n' '(NEEDED) Shared library: [libvendor.so]' ;; */libvendor.so) printf '%s\\n' '(NEEDED) Shared library: [libc.so.6]' ;; esac ;;
    esac ;;
  rpm) printf '%s' 'glibc' ;;
esac
`;
      for (const name of ['skopeo', 'umoci', 'syft', 'grype', 'readelf', 'rpm']) {
        const path = join(bin, name);
        writeFileSync(path, fakeTool);
        chmodSync(path, 0o755);
      }
      const path = join(dir, 'candidate.json');
      writeFileSync(path, JSON.stringify(evidence()));
      const result = spawnSync('sh', [script, path], {
        cwd: root, encoding: 'utf8',
        env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, NODE_OPTIONS: `--require=${preload}`,
          PROBE_FIXTURE_RUNTIME: runtime, PROBE_FIXTURE_DONOR: donor,
          PROBE_FAIL_SCAN: missing === 'scan' ? '1' : '0',
          PROBE_FAIL_DB_STATUS: missing === 'db-status' ? '1' : '0',
          PROBE_FAIL_VERSION: missing === 'tool-versions' ? '1' : '0' },
      });
      assert.notEqual(result.status, 0);
      assert.deepEqual(JSON.parse(readFileSync(path, 'utf8')),
        { schema: 1, sourceSha, status: 'rejected', phase: 'inspection', stage });
      assert.doesNotMatch(result.stdout + result.stderr + readFileSync(path, 'utf8'), /SENSITIVE-MARKER/i);
    } finally { rmSync(dir, { recursive: true, force: true }); }
  });
}
