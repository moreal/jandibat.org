import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, readdirSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import test from 'node:test';
import { createHash } from 'node:crypto';

const root = resolve(import.meta.dirname, '..');
const image = 'sha256:' + 'a'.repeat(64);
const donor = 'cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
function fixture(t, failure = '') {
  const dir = mkdtempSync(join(tmpdir(), 'restore-secure-test-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  mkdirSync(join(dir, 'bin')); mkdirSync(join(dir, 'private')); mkdirSync(join(dir, 'evidence'));
  const sbomFile = `restore-tools-${image.replace(':', '-')}.syft.json`;
  writeFileSync(join(dir, 'evidence', sbomFile), JSON.stringify({ source: { metadata: { imageID: image } } }));
  const executable = (name, body) => writeFileSync(join(dir, 'bin', name), `#!${process.execPath}\n${body}`, { mode: 0o755 });
  executable('uname', `console.log(process.argv[2] === '-s' ? (process.env.FAILURE === 'non-linux' ? 'Darwin' : 'Linux') : 'x86_64');`);
  executable('sleep', '');
  executable('docker', `
const fs = require('node:fs'); const a = process.argv.slice(2);
const stdin = a[0] === 'run' && a.includes(process.env.CLIENT_IMAGE) && process.env.FAILURE !== 'stdin-ignored' ? fs.readFileSync(0, 'utf8') : '';
const envFile = a.includes('--env-file') ? a[a.indexOf('--env-file') + 1] : '';
const env = envFile ? fs.readFileSync(envFile, 'utf8') : '';
fs.appendFileSync(process.env.TRACE, JSON.stringify({args:a, stdin, env}) + '\\n');
const fail = process.env.FAILURE;
if (a[0] === 'image') { console.log(fail === 'default-user' ? 'root' : '65532:65532'); process.exit(0); }
if (a[0] === 'network') {
 if (a[1] === 'create') { if (fail === 'network-create') process.exit(1); console.log('b'.repeat(64)); }
 else if (fail === 'cleanup-network') process.exit(1);
 process.exit(0);
}
if (a[0] === 'create') { if (fail === 'server-create') process.exit(1); console.log('c'.repeat(64)); process.exit(0); }
if (a[0] === 'start' || a[0] === 'rm' || a[0] === 'pull' || a[0] === 'ps') process.exit(0);
if (a[0] !== 'run') process.exit(2);
if (a.includes(process.env.DONOR)) {
 const mount = a[a.indexOf('--mount') + 1]; const path = mount.match(/src=([^,]+)/)[1];
 if (a[a.indexOf('--entrypoint')+1] === '/bin/sh') { fs.chmodSync(path + '/client.root.key', 0o400); process.exit(0); }
 for (const file of ['ca.crt','ca.key','node.crt','node.key','client.root.crt','client.root.key']) fs.writeFileSync(path + '/' + file, 'fixture certificate');
 process.exit(0);
}
if (!a.includes(process.env.CLIENT_IMAGE)) process.exit(3);
if (a.includes('--cidfile')) fs.writeFileSync(a[a.indexOf('--cidfile')+1], 'd'.repeat(64));
if (!a.includes('-i') || a[a.indexOf('--entrypoint')+1] !== '/bin/sh' || a.includes('--user') || !a.includes('--read-only') || !a.includes('--cap-drop=ALL') || !a.includes('--security-opt=no-new-privileges') || !a.includes('--tmpfs')) process.exit(4);
if (!env.includes('sslmode=verify-full')) process.exit(5);
if (a.at(-1).includes('default-user')) { console.log('65532:65532'); process.exit(0); }
if (fail === 'stdin-ignored') process.exit(0);
if (env.includes('sslrootcert=/credentials/wrong-ca.crt')) {
 if (fail === 'tls-success-with-diagnostic') { console.error('x509: certificate signed by unknown authority'); process.exit(0); }
 if (fail === 'wrong-ca-accepted') { console.log('marker\\nrestore-tools-ready'); process.exit(0); }
 console.error(fail === 'tls-unreachable' ? 'dial tcp: connection refused' : 'x509: certificate signed by unknown authority'); process.exit(1);
}
if (env.includes('@wrong-host:')) {
 if (fail === 'wrong-host-accepted') { console.log('marker\\nrestore-tools-ready'); process.exit(0); }
 console.error('x509: certificate is valid for secure-db, not wrong-host'); process.exit(1);
}
if (env.includes('COCKROACH_URL=postgresql://jandibat_api:') && fs.existsSync(process.env.TRACE + '.rotated') && !env.includes('ROTATED_SYNTHETIC')) {
 if (fail === 'old-password-accepted') { console.log('marker\\nrestore-tools-ready'); process.exit(0); }
 console.error('password authentication failed for user jandibat_api'); process.exit(1);
}
if (stdin.includes('ALTER USER')) fs.writeFileSync(process.env.TRACE + '.rotated', 'yes');
if (fail === 'secret-output') { console.error(env); process.exit(0); }
if (stdin.includes('restore-tools-ready')) console.log('marker\\nrestore-tools-ready');
else if (stdin.includes('restore-tools-migrations')) console.log('marker\\nrestore-tools-migrations');
else if (stdin.includes('restore-tools-grants')) console.log('marker\\nrestore-tools-grants');
else if (stdin.includes('current_user')) console.log('current_user\\njandibat_api');
process.exit(0);
`);
  const env = { ...process.env, PATH: `${join(dir, 'bin')}:${process.env.PATH}`, TMPDIR: join(dir, 'private'), TRACE: join(dir, 'trace'), FAILURE: failure, CLIENT_IMAGE: image, DONOR: donor };
  const run = (id = image) => spawnSync('sh', [join(root, 'scripts/test-restore-tools-secure.sh'), id, join(dir, 'evidence')], { cwd: root, env, encoding: 'utf8', timeout: 30000 });
  return { dir, run, calls: () => existsSync(env.TRACE) ? readFileSync(env.TRACE, 'utf8').trim().split('\n').map(JSON.parse) : [], result: () => JSON.parse(readFileSync(join(dir, 'evidence/restore-tools-secure.json'))) };
}

test('secure proof uses the exact immutable packaged client and isolates every SQL invocation', t => {
  const f = fixture(t); const result = f.run();
  assert.equal(result.status, 0, result.stderr);
  const calls = f.calls(); const clients = calls.filter(c => c.args[0] === 'run' && c.args.includes(image));
  assert.ok(clients.length >= 15);
  for (const {args, env} of clients) {
    assert.ok(args.includes('-i')); assert.ok(args.includes('--read-only'));
    assert.ok(args.includes('--cap-drop=ALL')); assert.ok(args.includes('--security-opt=no-new-privileges'));
    assert.equal(args[args.indexOf('--entrypoint')+1], '/bin/sh'); assert.equal(args.includes('--user'), false);
    assert.match(args[args.indexOf('--tmpfs')+1], /^\/tmp:.*mode=1777/);
    const mounts = args.flatMap((v,i) => v === '--mount' ? [args[i+1]] : []);
    assert.equal(mounts.length, 1); assert.match(mounts[0], /dst=\/credentials,readonly$/);
    assert.match(env, /sslmode=verify-full/); assert.doesNotMatch(args.join(' '), /postgresql:|SYNTHETIC|password=/);
  }
  for (const script of ['db-bootstrap-roles.sh','db-migrate-url.sh'])
    assert.equal(clients.filter(c => c.args.at(-1).includes('/workspace/scripts/' + script)).length, 2);
  for (const script of ['db-configure-runtime-roles.sh','db-verify-runtime-roles.sh'])
    assert.equal(clients.filter(c => c.args.at(-1).includes('/workspace/scripts/' + script)).length, 1);
  const network = calls.find(c => c.args[0] === 'network' && c.args[1] === 'create').args;
  assert.ok(network.includes('--internal'));
  const server = calls.find(c => c.args[0] === 'create').args;
  assert.ok(calls.some(c => c.args.includes(donor) && c.args.includes('chown 65532:65532 /certs/client.root.key; chmod 0400 /certs/client.root.key')));
  assert.ok(server.includes(donor)); assert.equal(server.some(a => a === '-p' || a.startsWith('--publish')), false);
  assert.ok(server.includes('--network-alias') && server.includes('wrong-host'));
  assert.deepEqual(calls.filter(c => c.args[0] === 'network' && c.args[1] === 'rm').map(c => c.args.at(-1)), ['b'.repeat(64)]);
  assert.ok(calls.some(c => c.args[0] === 'rm' && c.args.at(-1) === 'c'.repeat(64)));
  assert.equal(f.result().imageId, image); assert.equal(f.result().serverDonorDigest, donor.split('@')[1]);
  assert.match(f.result().sourceSha, /^[0-9a-f]{40}$/); assert.equal(f.result().phase, 'passed');
  const sbomFile = `restore-tools-${image.replace(':', '-')}.syft.json`;
  assert.equal(f.result().schema, 2);
  assert.deepEqual(f.result().sbom, { file: sbomFile,
    sha256: createHash('sha256').update(readFileSync(join(f.dir, 'evidence', sbomFile))).digest('hex') });
  assert.ok(Object.values(f.result().checks).every(v => v === true));
  assert.deepEqual(readdirSync(join(f.dir, 'private')), []);
  assert.deepEqual(readdirSync(join(f.dir, 'evidence')).sort(), ['restore-tools-secure.json', sbomFile].sort());
  assert.doesNotMatch(result.stdout + result.stderr + JSON.stringify(f.result()), /postgresql|SYNTHETIC|x509|password authentication/);
});

for (const failure of ['missing', 'wrong-image']) test(`secure proof rejects ${failure} local SBOM before Docker`, t => {
  const f = fixture(t);
  const path = join(f.dir, 'evidence', `restore-tools-${image.replace(':', '-')}.syft.json`);
  if (failure === 'missing') rmSync(path);
  else writeFileSync(path, JSON.stringify({ source: { metadata: { imageID: 'sha256:' + 'b'.repeat(64) } } }));
  const result = f.run();
  assert.notEqual(result.status, 0);
  assert.equal(f.calls().length, 0);
  assert.equal(existsSync(join(f.dir, 'evidence/restore-tools-secure.json')), false);
});

for (const failure of ['default-user','stdin-ignored','wrong-ca-accepted','wrong-host-accepted','tls-unreachable','tls-success-with-diagnostic','old-password-accepted','cleanup-network','secret-output','network-create','server-create']) {
  test(`secure proof fails closed and cleans owned resources for ${failure}`, t => {
    const f = fixture(t, failure); const result = f.run();
    assert.notEqual(result.status, 0); assert.match(result.stderr, /restore-tools secure proof failed:/);
    assert.notEqual(f.result().phase, 'passed');
    assert.doesNotMatch(result.stdout + result.stderr + JSON.stringify(f.result()), /postgresql|SYNTHETIC|x509|password authentication/);
    assert.deepEqual(readdirSync(join(f.dir, 'private')), []);
    const calls = f.calls();
    if (failure === 'stdin-ignored') assert.equal(calls.filter(c => c.args.includes(image) && c.args.at(-1).includes('exec cockroach sql')).length, 20);
    if (failure === 'network-create') assert.equal(calls.some(c => c.args[0] === 'network' && c.args[1] === 'rm'), false);
    if (failure === 'server-create') assert.equal(calls.some(c => c.args[0] === 'rm' && c.args.at(-1) === 'c'.repeat(64)), false);
  });
}

for (const id of ['restore-tools:latest', 'sha256:abc', 'sha256:' + 'A'.repeat(64), image + '\n']) {
  test('secure proof rejects a mutable or malformed image ID before Docker', t => {
    const f = fixture(t); const result = f.run(id);
    assert.equal(result.status, 2); assert.equal(f.calls().length, 0);
  });
}

test('unsupported host cannot leave a stale passing secure-proof result', t => {
  const f = fixture(t, 'non-linux');
  writeFileSync(join(f.dir, 'evidence/restore-tools-secure.json'), '{"phase":"passed"}');
  assert.equal(f.run().status, 77);
  assert.equal(existsSync(join(f.dir, 'evidence/restore-tools-secure.json')), false);
  assert.equal(f.calls().length, 0);
});
