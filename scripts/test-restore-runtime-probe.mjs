import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../', import.meta.url));
const script = join(root, 'scripts/probe-restore-runtime-candidate.sh');
const sourceSha = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim();
const digest = `sha256:${'a'.repeat(64)}`;
const donorDigest = 'sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
const bootstrapDigest = 'cd6df5adfa0fcc557b50c85fe51e577631cdcaba1fb2eab2a15ba904399c0d60';

function evidence() {
  return {
    schema: 1, sourceSha, status: 'candidate',
    runtime: { source: 'registry.access.redhat.com/ubi10/ubi-micro', indexDigest: digest,
      amd64Digest: digest, os: 'linux', architecture: 'amd64',
      osRelease: { ID: 'rhel', VERSION_ID: '10.0' } },
    donor: { source: `cockroachdb/cockroach:v26.2.5@${donorDigest}`,
      amd64Digest: donorDigest, interpreter: '/lib64/ld-linux-x86-64.so.2',
      needed: ['libc.so.6'], nativeFiles: [{ path: '/cockroach/cockroach', sha256: 'c'.repeat(64) }],
      licenses: [{ path: '/cockroach/licenses/LICENSE', sha256: 'd'.repeat(64) }] },
    packages: [{ name: 'glibc', version: '2.39', type: 'rpm' }],
    rpmdb: [{ path: '/usr/lib/sysimage/rpm/rpmdb.sqlite', sha256: 'e'.repeat(64) }],
    ownership: [{ path: '/lib64/ld-linux-x86-64.so.2', package: 'glibc' },
      { path: '/usr/lib64/libc.so.6', package: 'glibc' }],
    trust: [{ path: '/etc/pki/tls/certs/ca-bundle.crt', sha256: 'f'.repeat(64) }],
    baseLicenses: [{ path: '/usr/share/licenses/glibc/LICENSES', sha256: '1'.repeat(64) }],
    applets: { bootstrapSourceSha256: bootstrapDigest,
      names: ['awk', 'chmod', 'mktemp', 'rm', 'tail', 'tr'] },
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
  ['High finding', e => { e.scan.matches = [{ severity: 'High', id: 'CVE-TEST' }]; }],
  ['Critical finding', e => { e.scan.matches = [{ severity: 'Critical', id: 'CVE-TEST' }]; }],
  ['wrong bootstrap source digest', e => { e.applets.bootstrapSourceSha256 = '0'.repeat(64); }],
  ['wrong source SHA', e => { e.sourceSha = '0'.repeat(40); }],
  ['secret-bearing evidence', e => { e.password = 'do-not-upload'; }],
  ['missing tool versions', e => { delete e.toolVersions; }],
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
