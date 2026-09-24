import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync, existsSync, cpSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, basename } from 'node:path';
import test from 'node:test';
import { createHash } from 'node:crypto';

const root = resolve(import.meta.dirname, '..');
const names = ['api', 'worker', 'maintenance', 'web', 'restore-tools'];
const sha = 'a'.repeat(40);
const digest = 'sha256:' + 'b'.repeat(64);
const fileSha = path => createHash('sha256').update(readFileSync(path)).digest('hex');
const previousDigest = 'sha256:' + 'c'.repeat(64);
const ref = name => `example.invalid/release/${name}@${digest}`;
const yaml = file => JSON.parse(execFileSync('ruby', ['-rjson', '-ryaml', '-e', 'puts JSON.generate(YAML.safe_load(File.read(ARGV[0]), permitted_classes: [Symbol]))', file]));
const script = name => join(root, 'scripts', name);
function fixture(t) {
  const dir = mkdtempSync(join(tmpdir(), 'image-release-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, 'bin'));
  return dir;
}
function executable(dir, name, body) {
  writeFileSync(join(dir, 'bin', name), `#!${process.execPath}\n` + body, { mode: 0o755 });
}
function invoke(file, env, args = []) {
  return spawnSync(file.endsWith('.mjs') ? process.execPath : 'sh', [script(file), ...args], {
    cwd: root, env: { ...process.env, ...env }, encoding: 'utf8', timeout: 30000,
  });
}
function deployment(t, overrides = {}) {
  const dir = fixture(t);
  writeFileSync(join(dir, 'env'), '');
  executable(dir, 'docker', `
const fs = require('node:fs'); const args = process.argv.slice(2);
const files = args.flatMap((v, i) => v === '--env-file' ? [args[i+1]] : []);
const state = Object.assign({}, ...files.map(f => Object.fromEntries(fs.readFileSync(f, 'utf8').trim().split('\\n').filter(Boolean).map(l => l.split('=')))));
for (const key of Object.keys(state)) if (process.env[key]) state[key] = process.env[key];
fs.appendFileSync(process.env.TRACE, JSON.stringify({args, state}) + '\\n');
if (process.env.FAIL_SERVICE && args.includes(process.env.FAIL_SERVICE) && args.includes('run')) process.exit(1);
`);
  executable(dir, 'curl', 'process.exit(0);');
  const env = {
    PATH: `${join(dir, 'bin')}:${process.env.PATH}`, TRACE: join(dir, 'trace'),
    STAGING_ENV_FILE: join(dir, 'env'), STAGING_STATE_FILE: join(dir, 'state'),
    BUILD_SHA: sha, REGION: 'fixture', ...Object.fromEntries(names.map(n => [n.replace('-', '_').toUpperCase() + '_IMAGE', ref(n)])),
    ...overrides,
  };
  return { dir, env, trace: () => readFileSync(env.TRACE, 'utf8').trim().split('\n').map(JSON.parse) };
}

test('workflows build Nix archives and gate release on local and registry evidence', () => {
  for (const file of ['ci.yml', 'deploy-staging.yml']) {
    const workflow = yaml(join(root, '.github/workflows', file));
    const steps = Object.values(workflow.jobs).flatMap(job => job.steps || []);
    for (const step of steps) {
      assert.doesNotMatch(step.run || '', /docker\s+build(?:x)?\s|apps\/(?:api|web)\/Dockerfile/);
      assert.doesNotMatch(step.uses || '', /setup-(?:node|go)/);
    }
    const imageJob = workflow.jobs[file === 'ci.yml' ? 'images' : 'build'];
    assert.equal(imageJob['runs-on'], 'ubuntu-24.04');
    assert.ok(imageJob.steps.some(s => s.uses?.startsWith('DeterminateSystems/nix-installer-action@')));
    assert.ok(imageJob.steps.some(s => s.run?.includes('build-release-images.sh')));
    assert.ok(imageJob.steps.some(s => s.uses?.startsWith('actions/upload-artifact@') && s.with['if-no-files-found'] === 'error'));
    if (file === 'deploy-staging.yml') {
      assert.ok(imageJob.steps.some(s => s.run?.includes('image-release.mjs publish')));
      for (const name of names) assert.ok(imageJob.outputs[name.replace('-', '_') + '_ref']);
      assert.deepEqual(workflow.jobs.deploy.needs, 'build');
    }
  }
});

test('Compose gives every workload its own image and migration preflight inputs', () => {
  const { services } = yaml(join(root, 'deploy/staging/compose.yaml'));
  for (const name of names.slice(0, 4)) {
    assert.equal(services[name].image, '${' + name.toUpperCase() + '_IMAGE:?' + name.toUpperCase() + '_IMAGE is required}');
    assert.equal(services[name].read_only, true);
    assert.equal(services[name].entrypoint, undefined, 'use the archive entrypoint');
    assert.equal(services[name].environment.MIGRATION_DATABASE_URL, undefined);
  }
  assert.ok(services.migrate.environment.MIGRATION_DATABASE_URL);
  assert.equal(services.migrate.environment.DATABASE_URL, undefined);
  assert.equal(services.migrate.environment.MIGRATIONS_DIR, '/workspace/db/migrations');
  assert.equal(services.migrate.environment.TMPDIR, '/tmp');
  assert.ok(services.migrate.tmpfs.some(m => m.startsWith('/tmp:')));
  assert.ok(services['verify-runtime-roles'].entrypoint.includes('/workspace/scripts/db-verify-runtime-roles.sh'));
});

test('make check executes the host runtime contract', () => {
  const result = spawnSync('make', ['--no-print-directory', '-n', 'check'], { cwd: root, encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.ok(result.stdout.split('\n').includes('sh scripts/test-image-runtime-contract.sh'));
});

test('restore image packages the named database payload and checks it after import', () => {
  const dockerfile = readFileSync(join(root, 'deploy/restore-tools.Dockerfile'), 'utf8');
  const builder = readFileSync(script('build-release-images.sh'), 'utf8');
  assert.match(dockerfile, /^COPY db\/migrations\/ \/workspace\/db\/migrations\/$/m);
  assert.match(dockerfile, /^COPY scripts\/ \/workspace\/scripts\/$/m);
  assert.match(builder, /cp db\/migrations\/\*\.sql "\$restore_context\/db\/migrations\/"/);
  assert.match(builder, /for file in db-migrate-url\.sh db-configure-runtime-roles\.sh db-verify-runtime-roles\.sh db-bootstrap-roles\.sh; do/);
  assert.match(builder, /cp "scripts\/\$file" "\$restore_context\/scripts\/\$file"/);
  assert.match(builder, /node scripts\/image-release\.mjs import restore-tools[^\n]*\n(?:[^\n]*\n)*?sh scripts\/test-restore-tools-payload\.sh/);
});

test('restore payload validation rejects an archive missing the baseline migration', t => {
  const dir = fixture(t);
  executable(dir, 'docker', `
const args = process.argv.slice(2);
if (args.includes('find')) { console.log('/workspace/db/migrations/0002_ingest_reservation_token.sql'); process.exit(0); }
process.exit(2);
`);
  const result = invoke('test-restore-tools-payload.sh', { PATH: `${join(dir, 'bin')}:${process.env.PATH}` }, ['sha256:' + 'b'.repeat(64)]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /0001_baseline\.sql/);
});

test('CI exercises runtime secrets and worker OAuth after isolated test database migrations and grants', () => {
  const steps = yaml(join(root, '.github/workflows/ci.yml')).jobs.migrations.steps;
  const runtime = steps.findIndex(s => s.run?.includes('make image-runtime-contract-test'));
  const migrate = steps.findIndex(s => s.run?.includes('make db-migrate'));
  const grants = steps.findIndex(s => s.run?.includes('make db-runtime-roles-test'));
  assert.ok(runtime > grants && grants > migrate && migrate >= 0, 'runtime gate must follow migrations and role grants');
  assert.equal(steps[runtime].env.TASK3_WORKER_OAUTH_TEST_DSN, 'postgresql://jandibat_worker@127.0.0.1:26257/jandibat?sslmode=disable');
});

for (const [file, args, forwarded] of [
  ['rotate-credentials.sh', ['--dry-run'], ['reencrypt', '--dry-run']],
  ['purge-expired-data.sh', ['--as-of', '2026-09-24T00:00:00Z', '--dry-run'], ['retention', '--as-of', '2026-09-24T00:00:00Z', '--dry-run']],
  ['verify-deletion.sh', ['--request-id', 'fixture-request'], ['verify-deletion', '--request-id', 'fixture-request']],
]) {
  test(`${file} invokes the maintenance image executable and preserves explicit overrides`, t => {
    // Trace the real exec attempt: the host intentionally lacks the image binary.
    const result = spawnSync('sh', ['-x', script(file), ...args], {
      env: { PATH: process.env.PATH, MAINTENANCE_BIN: '' }, encoding: 'utf8', timeout: 5000,
    });
    assert.match(result.stderr, /\+ exec \/bin\/maintenance /);
    const dir = fixture(t);
    executable(dir, 'maintenance', 'process.stdout.write(JSON.stringify(process.argv.slice(2)));');
    const override = invoke(file, { MAINTENANCE_BIN: join(dir, 'bin/maintenance') }, args);
    assert.equal(override.status, 0, override.stderr);
    assert.deepEqual(JSON.parse(override.stdout), forwarded);
  });
}

test('staging HMAC replacement instruction matches the parser encoding and length', () => {
  const example = readFileSync(join(root, 'deploy/staging/.env.staging.example'), 'utf8');
  const value = example.split('\n').find(line => line.startsWith('DELETED_IDENTITY_HMAC_KEYS=')).split('=').slice(1).join('=');
  assert.deepEqual(Object.values(JSON.parse(value)), ['replace-with-distinct-unpadded-standard-base64-for-exactly-32-bytes']);
});

test('deployment records all refs and completes migration, GRANT, negative role tests before rollout', t => {
  const f = deployment(t);
  const result = invoke('deploy-staging.sh', f.env);
  assert.equal(result.status, 0, result.stderr);
  const trace = f.trace();
  const index = service => trace.findIndex(e => e.args.includes('run') && e.args.includes(service));
  const rollout = trace.findIndex(e => e.args.includes('up'));
  assert.ok(index('migrate') >= 0);
  assert.ok(index('grant-runtime-roles') > index('migrate'));
  assert.ok(index('verify-runtime-roles') > index('grant-runtime-roles'));
  assert.ok(rollout > index('verify-runtime-roles'));
  for (const name of names) assert.equal(trace[rollout].state[name.replace('-', '_').toUpperCase() + '_IMAGE'], ref(name));
});

for (const name of names) {
  test(`deployment rejects mutable ${name} image before Docker access`, t => {
    const key = name.replace('-', '_').toUpperCase() + '_IMAGE';
    const f = deployment(t, { [key]: `example.invalid/${name}:latest` });
    assert.notEqual(invoke('deploy-staging.sh', f.env).status, 0);
    assert.equal(existsSync(f.env.TRACE), false);
  });
}

test('failed role verification restores every previous ref despite exported candidate environment', t => {
  const f = deployment(t, { FAIL_SERVICE: 'verify-runtime-roles' });
  const old = Object.fromEntries(names.map(n => [n.replace('-', '_').toUpperCase() + '_IMAGE', ref(n).replace(digest, previousDigest)]));
  const state = Object.entries({ ...old, BUILD_SHA: 'd'.repeat(40), REGION: 'fixture' }).map(([k,v]) => `${k}=${v}`).join('\n') + '\n';
  writeFileSync(f.env.STAGING_STATE_FILE, state);
  const result = invoke('deploy-staging.sh', f.env);
  assert.equal(result.status, 1, result.stderr);
  assert.equal(readFileSync(f.env.STAGING_STATE_FILE, 'utf8'), state);
  const rollouts = f.trace().filter(e => e.args.includes('up'));
  assert.equal(rollouts.length, 1);
  for (const [key,value] of Object.entries(old)) assert.equal(rollouts[0].state[key], value);
});

test('legacy incomplete rollback state blocks deployment before changing state or Docker access', t => {
  const f = deployment(t);
  const old = `API_IMAGE=${ref('api')}\nWEB_IMAGE=${ref('web')}\nBUILD_SHA=${sha}\nREGION=fixture\n`;
  writeFileSync(f.env.STAGING_STATE_FILE, old);
  assert.equal(invoke('deploy-staging.sh', f.env).status, 2);
  assert.equal(readFileSync(f.env.STAGING_STATE_FILE, 'utf8'), old);
  assert.equal(existsSync(f.env.TRACE), false);
});

test('failed port-changing deployment probes the restored release ports', t => {
  const f = deployment(t, { FAIL_SERVICE: 'verify-runtime-roles', API_PORT: '19081', WEB_PORT: '19080' });
  const old = { ...Object.fromEntries(names.map(n => [n.replace('-', '_').toUpperCase() + '_IMAGE', ref(n).replace(digest, previousDigest)])), BUILD_SHA: 'd'.repeat(40), REGION: 'fixture', API_PORT: '18091', WEB_PORT: '18090' };
  writeFileSync(f.env.STAGING_STATE_FILE, Object.entries(old).map(([k,v]) => `${k}=${v}`).join('\n') + '\n');
  f.env.PORT_TRACE = join(f.dir, 'ports');
  executable(f.dir, 'curl', `require('node:fs').appendFileSync(process.env.PORT_TRACE, process.argv.at(-1) + '\\n');`);
  assert.equal(invoke('deploy-staging.sh', f.env).status, 1);
  assert.deepEqual(readFileSync(f.env.PORT_TRACE, 'utf8').trim().split('\n'), ['http://127.0.0.1:18091/healthz', 'http://127.0.0.1:18090/healthz']);
});

test('rollback rehearsal restores worker-only changes and re-promotes all candidate refs', t => {
  const f = deployment(t);
  const previous = { ...Object.fromEntries(names.map(n => [n.replace('-', '_').toUpperCase() + '_IMAGE', ref(n)])), WORKER_IMAGE: ref('worker').replace(digest, previousDigest), BUILD_SHA: 'd'.repeat(40), REGION: 'fixture' };
  writeFileSync(f.env.STAGING_STATE_FILE + '.rollback', Object.entries(previous).map(([k,v]) => `${k}=${v}`).join('\n'));
  const result = invoke('rehearse-staging-rollback.sh', f.env);
  assert.equal(result.status, 0, result.stderr);
  const rollouts = f.trace().filter(e => e.args.includes('up'));
  assert.equal(rollouts.length, 2);
  assert.equal(rollouts[0].state.WORKER_IMAGE, previous.WORKER_IMAGE);
  assert.equal(rollouts[1].state.WORKER_IMAGE, ref('worker'));
  const evidence = JSON.parse(result.stdout.trim().split('\n').at(-1));
  assert.equal(evidence.previousWorker, previous.WORKER_IMAGE);
  assert.equal(evidence.candidateMaintenance, ref('maintenance'));
});

function registryFixture(t, mode = 'matching') {
  const dir = fixture(t);
  const evidence = join(dir, 'evidence');
  mkdirSync(evidence);
  const manifest = JSON.stringify({ schemaVersion: 2, config: { digest }, layers: [] });
  const manifestDigest = 'sha256:' + createHash('sha256').update(manifest).digest('hex');
  for (const name of names) {
    const layout = join(evidence, name);
    mkdirSync(layout);
    writeFileSync(join(layout, 'manifest.json'), manifest);
    writeFileSync(join(evidence, `${name}.json`), JSON.stringify({ name, imageId: digest, layout }));
  }
  executable(dir, 'skopeo', `
const fs = require('node:fs'); const args = process.argv.slice(2);
fs.appendFileSync(process.env.TRACE, JSON.stringify(['skopeo', ...args]) + '\\n');
if (args[0] === 'inspect') {
 const published = fs.existsSync(process.env.PUBLISHED) && fs.readFileSync(process.env.PUBLISHED, 'utf8').split('\\n').includes(args.at(-1));
 if (process.env.MODE === 'tag-race' && !args.at(-1).includes('@') && published) process.stdout.write('{}');
 else {
 if (process.env.REGISTRY_DIAGNOSTIC) { console.error(process.env.REGISTRY_DIAGNOSTIC); process.exit(1); }
 if (process.env.MODE.startsWith('skopeo-1.24.1-') && !args.at(-1).includes('@') && !published) {
   const reference = args.at(-1).slice('docker://'.length);
   const split = reference.lastIndexOf(':');
   const message = 'Error parsing image name "docker://' + reference + '": reading manifest ' + reference.slice(split + 1) + ' in ' + reference.slice(0, split) + ': ' + (process.env.MODE.endsWith('manifest') ? 'manifest unknown' : 'name unknown');
   console.error('time="2026-09-24T13:30:54+09:00" level=fatal msg=' + JSON.stringify(message));
   process.exit(1);
 }
 if (process.env.MODE === 'auth') { console.error('unauthorized'); process.exit(1); }
 if (process.env.MODE === 'auth-name-in-path') { console.error('reading manifest at ghcr.io/name_unknown/image: unauthorized'); process.exit(1); }
 if (process.env.MODE === 'transport') { console.error('dial tcp: lookup registry: no such host'); process.exit(1); }
 if (process.env.MODE === 'absent-unauthorized') { console.error('NAME_UNKNOWN: unauthorized'); process.exit(1); }
 if (process.env.MODE === 'absent-denied') { console.error('NAME_UNKNOWN: access denied'); process.exit(1); }
 if (process.env.MODE === 'absent-transport') { console.error('NAME_UNKNOWN: transport failure: connection reset by peer'); process.exit(1); }
 if (process.env.MODE === 'mixed-auth-codes') { console.error(JSON.stringify({errors:[{code:'NAME_UNKNOWN',message:'repository absent'},{code:'UNAUTHORIZED',message:'authentication required'}]})); process.exit(1); }
 if (process.env.MODE === 'mixed-unknown-codes') { console.error(JSON.stringify({errors:[{code:'NAME_UNKNOWN',message:'repository absent'},{code:'UNKNOWN',message:'unclassified registry error'}]})); process.exit(1); }
 if (process.env.MODE === 'tag-race' && !args.at(-1).includes('@') && !published) { console.error('manifest unknown'); process.exit(1); }
 if (process.env.MODE === 'absent' && !args.at(-1).includes('@') && !published) { console.error('manifest unknown'); process.exit(1); }
 if (process.env.MODE === 'new-repository' && !args.at(-1).includes('@') && !published) { console.error('NAME_UNKNOWN: repository name not known to registry'); process.exit(1); }
 if (process.env.MODE === 'structured-name' && !args.at(-1).includes('@') && !published) { console.error(JSON.stringify({errors:[{code:'NAME_UNKNOWN',message:'repository name not known to registry'}]})); process.exit(1); }
 if (process.env.MODE === 'structured-manifest' && !args.at(-1).includes('@') && !published) { console.error(JSON.stringify({errors:[{code:'MANIFEST_UNKNOWN',message:'manifest unknown'}]})); process.exit(1); }
 if (process.env.MODE === 'structured-reordered' && !args.at(-1).includes('@') && !published) { console.error(JSON.stringify({errors:[{message:'manifest unknown',code:'MANIFEST_UNKNOWN'}]})); process.exit(1); }
 process.stdout.write(process.env.MODE === 'mismatch' ? '{}' : process.env.MANIFEST);
 }
}
if (args[0] === 'copy' && args.at(-1).startsWith('docker:')) fs.appendFileSync(process.env.PUBLISHED, args.at(-1) + '\\n');
if (args[0] === 'copy' && args.at(-1).startsWith('dir:')) {
 const dir = args.at(-1).slice(4); fs.mkdirSync(dir, {recursive:true}); fs.writeFileSync(dir + '/manifest.json', process.env.MANIFEST);
}
`);
  executable(dir, 'syft', `
const fs = require('node:fs'); const args = process.argv.slice(2);
fs.appendFileSync(process.env.TRACE, JSON.stringify(['syft', ...args]) + '\\n');
for (let i=0;i<args.length;i++) if (args[i] === '-o') {
 const [format, file] = args[++i].split('=');
 if (format === 'spdx-json' && process.env.MODE === 'missing-sbom') continue;
 fs.writeFileSync(file, JSON.stringify(format === 'spdx-json' ? {spdxVersion:'SPDX-2.3',packages:[]} : {source:{metadata:{imageID:process.env.IMAGE_ID,manifestDigest:process.env.MODE === 'wrong-target' ? 'sha256:'+'e'.repeat(64) : process.env.MANIFEST_DIGEST}}}));
}
`);
  executable(dir, 'grype', `
const fs = require('node:fs'); const args = process.argv.slice(2);
fs.appendFileSync(process.env.TRACE, JSON.stringify(['grype', ...args]) + '\\n');
if (process.env.MODE === 'missing-receipt' && args[0].includes('/restore-tools@')) {
 const first = fs.readdirSync(process.env.EVIDENCE).find(file => /^api-.*\\.release\\.json$/.test(file));
 fs.unlinkSync(process.env.EVIDENCE + '/' + first);
}
if (process.env.MODE === 'mismatched-receipt' && args[0].includes('/restore-tools@')) {
 const first = fs.readdirSync(process.env.EVIDENCE).find(file => /^api-.*\\.release\\.json$/.test(file));
 const path = process.env.EVIDENCE + '/' + first;
 const receipt = JSON.parse(fs.readFileSync(path)); receipt.imageId = 'sha256:'+'f'.repeat(64); fs.writeFileSync(path, JSON.stringify(receipt));
}
process.stdout.write(JSON.stringify({matches:process.env.MODE === 'scan-fail' ? [{vulnerability:{severity:'High'}}] : [],source:{target:{imageID:process.env.IMAGE_ID,manifestDigest:process.env.MANIFEST_DIGEST}}}));
if (process.env.MODE === 'scan-fail' || (process.env.MODE === 'partial-scan' && args[0].includes('/restore-tools@'))) process.exit(2);
`);
  const env = { PATH: `${join(dir, 'bin')}:${process.env.PATH}`, MODE: mode, TRACE: join(dir, 'trace'), PUBLISHED: join(dir, 'published'), EVIDENCE: evidence, MANIFEST: manifest, MANIFEST_DIGEST: manifestDigest, IMAGE_ID: digest, GITHUB_SHA: sha, GITHUB_REPOSITORY: 'Owner/Repo', GITHUB_OUTPUT: join(dir, 'outputs') };
  return { dir, evidence, env, manifestDigest, run: () => invoke('image-release.mjs', env, ['publish', evidence]), trace: () => existsSync(env.TRACE) ? readFileSync(env.TRACE,'utf8').trim().split('\n').map(JSON.parse) : [] };
}

for (const mode of ['matching', 'absent', 'new-repository', 'structured-name', 'structured-manifest', 'structured-reordered', 'skopeo-1.24.1-manifest', 'skopeo-1.24.1-name']) {
  test(`registry ${mode}: publish all five immutable refs, with exact digest scan evidence`, t => {
    const f = registryFixture(t, mode);
    const result = f.run();
    assert.equal(result.status, 0, result.stderr);
    const trace = f.trace();
    assert.equal(trace.filter(a => a[0] === 'skopeo' && a[1] === 'copy').length, mode === 'matching' ? 0 : 5);
    for (const name of names) {
      const target = `registry:ghcr.io/owner/repo/${name}@${f.manifestDigest}`;
      assert.ok(trace.some(a => a[0] === 'syft' && a[1] === target));
      assert.ok(trace.some(a => a[0] === 'grype' && a[1] === target && a.includes('--fail-on') && a.includes('high')));
      assert.ok(readFileSync(f.env.GITHUB_OUTPUT,'utf8').includes(`${name.replace('-', '_')}_ref=ghcr.io/owner/repo/${name}@${f.manifestDigest}`));
      const receipt = JSON.parse(readFileSync(join(f.evidence, `${name}-${f.manifestDigest.replace(':', '-')}.release.json`)));
      assert.equal(receipt.target, target);
      assert.equal(receipt.imageId, digest);
      assert.equal(receipt.manifestDigest, f.manifestDigest);
      for (const key of ['spdx', 'syft', 'grype']) {
        assert.equal(receipt.artifacts[key].file, basename(receipt.artifacts[key].file));
        assert.match(receipt.artifacts[key].sha256, /^[0-9a-f]{64}$/);
        assert.equal(receipt.artifacts[key].sha256, fileSha(join(f.evidence, receipt.artifacts[key].file)));
      }
    }
    const release = JSON.parse(readFileSync(join(f.evidence, 'release.json')));
    assert.equal(release.schemaVersion, 1);
    assert.equal(release.sourceSha, sha);
    assert.deepEqual(release.images.map(image => image.name).sort(), ['api','maintenance','restore-tools','web','worker']);
    for (const image of release.images) {
      assert.equal(image.tag.split(':').at(-1), release.sourceSha);
      assert.match(image.ref, /@sha256:[0-9a-f]{64}$/);
      assert.equal(image.scanReceipt, basename(image.scanReceipt));
      assert.equal(image.scanReceiptSha256, fileSha(join(f.evidence, image.scanReceipt)));
    }
    const moved = join(f.dir, 'relocated');
    cpSync(f.evidence, moved, { recursive: true });
    const validated = invoke('image-release.mjs', f.env, ['validate', moved]);
    assert.equal(validated.status, 0, validated.stderr);
  });
}

for (const mode of ['tag-race', 'missing-receipt', 'mismatched-receipt', 'partial-scan']) {
  test(`registry ${mode} leaves GITHUB_OUTPUT untouched`, t => {
    const f = registryFixture(t, 'absent');
    f.env.MODE = mode;
    const result = f.run();
    assert.notEqual(result.status, 0);
    assert.equal(existsSync(f.env.GITHUB_OUTPUT), false);
  });
}

test('relocated evidence rejects absolute paths and altered artifact bytes', t => {
  const f = registryFixture(t);
  f.env.RELEASE_SECRET_CANARY = 'release-secret-canary-5e3ad5c04e8b';
  assert.equal(f.run().status, 0);
  for (const file of ['release.json', ...names.map(name => `${name}-${f.manifestDigest.replace(':', '-')}.release.json`)]) {
    const bytes = readFileSync(join(f.evidence, file), 'utf8');
    assert.ok(!bytes.includes(f.dir), `${file} contains an absolute runner path`);
    assert.ok(!bytes.includes(f.env.RELEASE_SECRET_CANARY), `${file} contains a secret canary`);
  }
  const moved = join(f.dir, 'relocated');
  cpSync(f.evidence, moved, { recursive: true });
  const releasePath = join(moved, 'release.json');
  const release = JSON.parse(readFileSync(releasePath));
  release.images[0].scanReceipt = join(moved, release.images[0].scanReceipt);
  writeFileSync(releasePath, JSON.stringify(release));
  assert.notEqual(invoke('image-release.mjs', f.env, ['validate', moved]).status, 0);
  release.images[0].scanReceipt = basename(release.images[0].scanReceipt);
  writeFileSync(releasePath, JSON.stringify(release));
  const receipt = JSON.parse(readFileSync(join(moved, release.images[0].scanReceipt)));
  writeFileSync(join(moved, receipt.artifacts.spdx.file), 'altered');
  assert.notEqual(invoke('image-release.mjs', f.env, ['validate', moved]).status, 0);
});

// Full wrapper observed from the flake-locked Skopeo 1.24.1 against a
// disposable local OCI fixture (canonical MANIFEST_UNKNOWN/NAME_UNKNOWN).
const wrappedAbsence = 'time="2026-09-24T13:30:54+09:00" level=fatal msg=' + JSON.stringify(
  `Error parsing image name "docker://ghcr.io/owner/repo/api:${sha}": reading manifest ${sha} in ghcr.io/owner/repo/api: manifest unknown`,
);
for (const [label, diagnostic] of [
  ['different reference', wrappedAbsence.replaceAll('/api:', '/worker:')],
  ['different manifest', wrappedAbsence.replace(`reading manifest ${sha}`, `reading manifest ${'b'.repeat(40)}`)],
  ['auth suffix', wrappedAbsence.replace('manifest unknown', 'manifest unknown: unauthorized')],
  ['network suffix', wrappedAbsence.replace('manifest unknown', 'manifest unknown: HTTP 503 Service Unavailable')],
  ['second error', wrappedAbsence + '\nunauthorized'],
  ['wrong severity', wrappedAbsence.replace('level=fatal', 'level=warning')],
  ['unknown wrapper', wrappedAbsence.replace('Error parsing image name', 'unverified error wrapper')],
]) {
  test(`registry wrapped ${label} fails closed without a copy`, t => {
    const f = registryFixture(t);
    f.env.REGISTRY_DIAGNOSTIC = diagnostic;
    assert.notEqual(f.run().status, 0);
    assert.equal(f.trace().filter(a => a[1] === 'copy').length, 0);
    assert.equal(existsSync(f.env.GITHUB_OUTPUT), false);
  });
}

for (const [label, diagnostic] of [
  ['invalid credentials', 'NAME_UNKNOWN: invalid credentials'],
  ['HTTP 503', 'NAME_UNKNOWN: HTTP 503 Service Unavailable'],
  ['trailing text', 'NAME_UNKNOWN: repository name not known to registry; unexpected failure'],
  ['second line', 'NAME_UNKNOWN: repository name not known to registry\nunexpected additional failure'],
  ['structured invalid credentials', '{"errors":[{"code":"NAME_UNKNOWN","message":"invalid credentials"}]}'],
  ['structured extra detail', '{"errors":[{"code":"NAME_UNKNOWN","message":"repository name not known to registry","detail":"HTTP 503"}]}'],
  ['structured extra top-level error', '{"errors":[{"code":"NAME_UNKNOWN"}],"error":"HTTP 503"}'],
  ['structured plus text', '{"errors":[{"code":"NAME_UNKNOWN"}]}\nHTTP 503 Service Unavailable'],
  ['text plus structured', 'NAME_UNKNOWN: repository name not known to registry\n{"errors":[{"code":"NAME_UNKNOWN"}]}'],
  ['multiple absence errors', '{"errors":[{"code":"NAME_UNKNOWN"},{"code":"MANIFEST_UNKNOWN"}]}'],
  ['duplicate errors member', '{"errors":[{"code":"NAME_UNKNOWN"}],"errors":[{"code":"NAME_UNKNOWN"}]}'],
]) {
  test(`registry ambiguous ${label} fails closed without a copy`, t => {
    const f = registryFixture(t);
    f.env.REGISTRY_DIAGNOSTIC = diagnostic;
    assert.notEqual(f.run().status, 0);
    assert.equal(f.trace().filter(a => a[1] === 'copy').length, 0);
    assert.equal(existsSync(f.env.GITHUB_OUTPUT), false);
  });
}
for (const mode of ['mismatch', 'auth', 'auth-name-in-path', 'transport', 'absent-unauthorized', 'absent-denied', 'absent-transport', 'mixed-auth-codes', 'mixed-unknown-codes', 'scan-fail', 'wrong-target', 'missing-sbom']) {
  test(`registry ${mode} fails closed without exporting deployment refs`, t => {
    const f = registryFixture(t, mode);
    const result = f.run();
    assert.notEqual(result.status, 0);
    assert.equal(existsSync(f.env.GITHUB_OUTPUT), false);
    if (!['scan-fail','wrong-target','missing-sbom'].includes(mode)) assert.equal(f.trace().filter(a => a[1] === 'copy').length, 0);
  });
}
test('publish rejects shortened or mutable Git tags before registry access', t => {
  const f = registryFixture(t);
  f.env.GITHUB_SHA = 'latest';
  assert.notEqual(f.run().status, 0);
  assert.equal(f.trace().length, 0);
});

test('restore archive with omitted User imports Docker default empty User and scans exact image ID', t => {
  const f = registryFixture(t);
  const config = { architecture: 'amd64', os: 'linux', config: { Entrypoint: ['/cockroach/cockroach'] } };
  const bytes = JSON.stringify(config);
  const imageId = 'sha256:' + createHash('sha256').update(bytes).digest('hex');
  f.env.IMAGE_ID = imageId;
  f.env.MANIFEST = JSON.stringify({schemaVersion:2, config:{digest:imageId}, layers:[]});
  f.env.INSPECT_CONFIG = JSON.stringify({Id:imageId, Architecture:'amd64', Os:'linux', Config:{...config.config, User:''}});
  writeFileSync(join(f.dir, 'config.json'), bytes);
  writeFileSync(join(f.dir, 'manifest.json'), JSON.stringify([{Config:'config.json', Layers:[], RepoTags:[]} ]));
  const archive = join(f.dir, 'restore.tar');
  execFileSync('tar', ['-cf', archive, '-C', f.dir, 'config.json', 'manifest.json']);
  executable(f.dir, 'docker', `
const args = process.argv.slice(2);
if (args[0] === 'load') process.exit(0);
if (args[0] === 'image' && args[1] === 'inspect' && args[2] === process.env.IMAGE_ID) console.log('[' + process.env.INSPECT_CONFIG + ']');
else process.exit(2);
`);
  const result = invoke('image-release.mjs', f.env, ['import', 'restore-tools', archive, f.evidence]);
  assert.equal(result.status, 0, result.stderr);
  const receipt = JSON.parse(readFileSync(join(f.evidence, `restore-tools-${imageId.replace(':','-')}.release.json`)));
  assert.equal(receipt.target, `docker:${imageId}`);
  assert.ok(f.trace().some(a => a[0] === 'grype' && a[1] === `docker:${imageId}`));
});

for (const failure of ['none', 'build', 'create', 'collision']) {
const uncertainOwnership = ['create', 'collision'].includes(failure);
test(`release packaging ${uncertainOwnership ? 'preserves uncertain builder after' : 'cleans owned builder after'} ${failure === 'none' ? 'success' : failure + ' failure'}`, t => {
  const dir = fixture(t);
  const env = { PATH: `${join(dir, 'bin')}:${process.env.PATH}`, TRACE: join(dir, 'trace'), BUILDER_STATE: join(dir,'builder'), FIXTURE_ARCHIVE: join(dir,'archive.tar'), FAILURE: failure };
  executable(dir, 'nix', `console.log(process.argv[2] === 'eval' ? 'x86_64-linux' : process.env.FIXTURE_ARCHIVE);`);
  executable(dir, 'make', '');
  // Image import/scan has its own real-program fixture; this shell fixture
  // isolates builder lifecycle from Nix build, image import and payload checks.
  executable(dir, 'node', '');
  executable(dir, 'sh', `
const {spawnSync} = require('node:child_process');
const args = process.argv.slice(2);
if (args[0] === 'scripts/test-restore-tools-payload.sh') process.exit(0);
const result = spawnSync('/bin/sh', args, {stdio:'inherit', env:process.env});
process.exit(result.status ?? 1);
`);
  executable(dir, 'skopeo', '');
  executable(dir, 'docker', `
const fs = require('node:fs'); const a = process.argv.slice(2);
fs.appendFileSync(process.env.TRACE, JSON.stringify(a)+'\\n');
if (a[0] !== 'buildx') process.exit(2);
if (a[1] === 'version') { console.log('github.com/docker/buildx v0.33.0 fixture'); process.exit(0); }
if (a[1] === 'create') {
 if (a[a.indexOf('--driver')+1] !== 'docker-container') process.exit(3);
 const name = a[a.indexOf('--name')+1];
 if (process.env.FAILURE === 'collision') { fs.writeFileSync(process.env.BUILDER_STATE, name); console.error('existing instance: another owner won the race'); process.exit(8); }
 fs.writeFileSync(process.env.BUILDER_STATE, name); console.log(name); process.exit(process.env.FAILURE === 'create' ? 8 : 0);
}
if (a[1] === 'inspect') { if (!fs.existsSync(process.env.BUILDER_STATE)) process.exit(4); console.log('docker-container'); process.exit(0); }
if (a[1] === 'build') {
 if (!fs.existsSync(process.env.BUILDER_STATE) || a[a.indexOf('--builder')+1] !== fs.readFileSync(process.env.BUILDER_STATE,'utf8')) { console.error('Docker exporter is not supported for the docker driver'); process.exit(5); }
 const context = a.at(-1);
 const scripts = fs.readdirSync(context + '/scripts').sort();
 const migrations = fs.readdirSync(context + '/db/migrations').sort();
 if (JSON.stringify(scripts) !== JSON.stringify(['db-bootstrap-roles.sh','db-configure-runtime-roles.sh','db-migrate-url.sh','db-verify-runtime-roles.sh'])) process.exit(10);
 if (JSON.stringify(migrations) !== JSON.stringify(fs.readdirSync('db/migrations').filter(f => f.endsWith('.sql')).sort())) process.exit(11);
 for (const file of scripts) if (!(fs.statSync(context + '/scripts/' + file).mode & 0o111)) process.exit(12);
 for (const file of migrations) if (fs.statSync(context + '/db/migrations/' + file).mode & 0o222) process.exit(13);
 for (const path of ['/db/migrations','/scripts']) if (fs.statSync(context + path).mode & 0o222) process.exit(14);
 if (process.env.FAILURE === 'build') process.exit(9);
 process.exit(0);
}
if (a[1] === 'rm') { if (a.at(-1) !== fs.readFileSync(process.env.BUILDER_STATE,'utf8')) process.exit(6); fs.unlinkSync(process.env.BUILDER_STATE); process.exit(0); }
process.exit(7);
`);
  const result = invoke('build-release-images.sh', env, [join(dir, 'evidence')]);
  assert.equal(result.status, uncertainOwnership ? 8 : failure === 'build' ? 9 : 0, result.stderr);
  const calls = readFileSync(env.TRACE,'utf8').trim().split('\n').map(JSON.parse);
  const creation = calls.find(a => a[1] === 'create');
  assert.match(creation[creation.indexOf('--driver-opt')+1], /^image=moby\/buildkit:v[0-9.]+@sha256:[0-9a-f]{64}$/);
  const builder = creation[creation.indexOf('--name')+1];
  assert.equal(calls.filter(a => a[1] === 'rm').length, uncertainOwnership ? 0 : 1);
  if (uncertainOwnership) assert.equal(readFileSync(env.BUILDER_STATE, 'utf8'), builder);
  else assert.equal(calls.at(-1).at(-1), builder);
  assert.equal(existsSync(env.BUILDER_STATE), uncertainOwnership);
});
}

for (const forbidden of [
  'docker build --file apps/api/Dockerfile apps/api',
  'docker buildx build --file apps/web/Dockerfile .',
  'corepack prepare yarn@4.0.0',
  'yarn install',
]) {
  test(`authority guard rejects hidden release script compilation: ${forbidden}`, t => {
    const dir = fixture(t);
    mkdirSync(join(dir, 'workflows'));
    writeFileSync(join(dir, 'workflows/ci.yml'), 'jobs: {}\n');
    writeFileSync(join(dir, 'build-release-images.sh'), forbidden + '\n');
    const result = invoke('check-ci-version-authority.sh', {}, [join(dir, 'workflows'), dir]);
    assert.equal(result.status, 1, result.stdout + result.stderr);
  });
}

for (const instruction of ['run go build ./...', 'RuN yarn build', 'add https://example.invalid/source.tar /src']) {
  test(`restore packaging guard rejects case-insensitive Dockerfile instruction: ${instruction}`, t => {
    const dir = fixture(t);
    mkdirSync(join(dir,'workflows'));
    writeFileSync(join(dir,'workflows/ci.yml'), 'jobs: {}\n');
    const dockerfile = join(dir,'restore-tools.Dockerfile');
    writeFileSync(dockerfile, 'FROM example.invalid/base@' + digest + '\n' + instruction + '\n');
    const result = invoke('check-ci-version-authority.sh', {}, [join(dir,'workflows'), dir, dockerfile]);
    assert.equal(result.status, 1, result.stdout + result.stderr);
  });
}
