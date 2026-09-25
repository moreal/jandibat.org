import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

test('Linux CI runs an isolated secure backup-role fixture', () => {
  const workflow = readFileSync('.github/workflows/ci.yml', 'utf8');
  const fixture = readFileSync('scripts/test-db-backup-roles-secure.sh', 'utf8');
  const shell = readFileSync('flake.nix', 'utf8');

  assert.ok(/run: nix develop \.#backup-fixture --command sh scripts\/test-db-backup-roles-secure\.sh/.test(workflow), 'Linux CI fixture step missing');
  assert.ok(/run: nix develop --command node --test scripts\/test-db-backup-roles-secure-fixture\.test\.mjs/.test(workflow), 'focused boundary test is not wired');
  assert.ok(/backup-fixture =/.test(shell) && /s3proxy minio-client openssl/.test(shell), 'pinned fixture tools missing');
  for (const [name, pattern] of [
    ['native Linux guard', /uname -s[\s\S]*uname -m/],
    ['secure Cockroach pin', /cockroachdb\/cockroach:v26\.2\.5@sha256:/],
    ['authenticated synthetic S3', /s3proxy --properties/],
    ['signature checking', /aws-v2-or-v4/],
    ['privilege rejection', /CREATE EXTERNAL CONNECTION/],
    ['real S3 privilege probe', /jandibat_privilege_probe/],
    ['connection check', /CHECK EXTERNAL CONNECTION/],
    ['system grants', /SHOW SYSTEM GRANTS/],
    ['database grant boundary', /SHOW GRANTS ON DATABASE jandibat/],
    ['authenticated identity proof', /SELECT current_user\(\)/],
    ['RESTORE privilege negative', /RESTORE DATABASE jandibat/],
    ['admin privilege negative', /CREATE USER jandibat_forbidden_/],
    ['safe connection fingerprint', /sha256\(connection_uri\)/],
    ['post-rejection connection check', /post-rejection CHECK/],
    ['missing privilege mutation', /missing-permission/],
    ['malformed URI mutation', /malformed-uri/],
    ['credential rotation mutation', /credential-rotation/],
    ['conflicting connection mutation', /conflicting-connection/],
    ['rerun', /second run/],
    ['server-log redaction', /server-log sentinel/],
  ]) assert.ok(pattern.test(fixture), `${name} gate missing`);
  assert.ok(!/B2_|BACKBLAZE_|PRODUCTION_DATABASE_URL/.test(fixture), 'production endpoint accepted');
});

test('non-Linux host reports a non-passing skip without contacting Docker', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-platform-'));
  try {
    writeFileSync(join(dir, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Darwin;; -m) echo arm64;; esac\n');
    writeFileSync(join(dir, 'docker'), '#!/bin/sh\necho docker-invoked >&2; exit 99\n');
    chmodSync(join(dir, 'uname'), 0o700);
    chmodSync(join(dir, 'docker'), 0o700);
    const result = spawnSync('sh', ['scripts/test-db-backup-roles-secure.sh'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}` },
    });
    assert.equal(result.status, 77);
    assert.match(result.stderr, /SKIP 77/);
    assert.doesNotMatch(result.stderr, /docker-invoked/);
    assert.equal(result.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('bootstrap, runner and verifier have no root URL or private key', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-client-'));
  try {
    writeFileSync(join(dir, 'docker'), '#!/bin/sh\nprintf "%s\\n" "$@" >"$TEST_DOCKER_ARGS"\n');
    chmodSync(join(dir, 'docker'), 0o700);
    const baseEnv = {
      ...process.env, PATH: `${dir}:${process.env.PATH}`, TEST_DOCKER_ARGS: join(dir, 'args'),
      FIXTURE_DIR: dir, FIXTURE_IMAGE: 'fixture-image',
      FIXTURE_ROOT_URL: 'root-only-url', FIXTURE_MIGRATION_URL: 'migration-url',
      FIXTURE_API_URL: 'api-url', FIXTURE_WORKER_URL: 'worker-url', FIXTURE_MAINTENANCE_URL: 'maintenance-url',
      FIXTURE_BOOTSTRAP_URL: 'bootstrap-url', FIXTURE_RUNNER_URL: 'runner-url', FIXTURE_VERIFIER_URL: 'verifier-url',
      FIXTURE_PASS_A: 'a', FIXTURE_PASS_B: 'b', FIXTURE_PASS_C: 'c', FIXTURE_PASS_D: 'd',
      FIXTURE_PASS_E: 'e', FIXTURE_PASS_F: 'f', FIXTURE_PASS_G: 'g',
      FIXTURE_ACCESS: 'fixture-access', FIXTURE_SECRET: 'fixture-secret',
    };
    const prepare = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_write_env'], { env: baseEnv, encoding: 'utf8' });
    assert.equal(prepare.status, 0, prepare.stderr);
    for (const role of ['bootstrap', 'runner', 'verifier']) {
      const result = spawnSync('sh', ['-c', `. scripts/backup-fixture-client.sh; fixture_client ${role} /workspace/test.sh`], { env: baseEnv, encoding: 'utf8' });
      assert.equal(result.status, 0, result.stderr);
      const args = readFileSync(join(dir, 'args'), 'utf8');
      const roleEnv = readFileSync(join(dir, `${role}.env`), 'utf8');
      assert.match(args, new RegExp(`${role}\\.env`));
      assert.match(args, /ca-only/);
      assert.doesNotMatch(args, /root\.env|root-certs/);
      assert.ok(!args.includes(`src=${dir}/certs`), `${role} received root certificate directory`);
      assert.doesNotMatch(roleEnv, /root-only-url|COCKROACH_ROOT_URL/);
      if (role !== 'bootstrap') assert.doesNotMatch(roleEnv, /fixture-access|fixture-secret/);
    }
    const root = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_client root /workspace/test.sh'], { env: baseEnv, encoding: 'utf8' });
    assert.equal(root.status, 0, root.stderr);
    assert.match(readFileSync(join(dir, 'args'), 'utf8'), /root\.env/);
    assert.match(readFileSync(join(dir, 'root.env'), 'utf8'), /root-only-url/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('cleanup fails when Docker still retains the secure DB container', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-cleanup-'));
  try {
    writeFileSync(join(dir, 'docker'), '#!/bin/sh\ncase "$1 $2" in "stop "*) exit 1;; "info "*) exit 0;; "container inspect") exit 0;; esac\nexit 99\n');
    writeFileSync(join(dir, 'sleep'), '#!/bin/sh\nexit 0\n');
    chmodSync(join(dir, 'docker'), 0o700);
    chmodSync(join(dir, 'sleep'), 0o700);
    const result = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_stop_container fixture-db'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}` },
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /cleanup incomplete/);
    assert.doesNotMatch(result.stderr, /fixture-secret|root-only-url/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('grant policy rejects extra authority and grant option', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-grants-'));
  try {
    const grants = [
      {
        name: 'system', fn: 'fixture_check_system_grants',
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_bootstrap\tEXTERNALCONNECTION\tfalse\n',
        mutations: ['jandibat_backup_bootstrap\tEXTERNALCONNECTION\ttrue', 'jandibat_backup_runner\tMODIFYCLUSTERSETTING\tfalse'],
      },
      {
        name: 'database', fn: 'fixture_check_database_grants',
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_runner\tBACKUP\tfalse\n',
        mutations: ['jandibat_backup_runner\tBACKUP\ttrue', 'jandibat_backup_runner\tCREATE\tfalse'],
      },
      {
        name: 'connection', fn: 'fixture_check_connection_grants',
        // v26.2 SHOW GRANTS includes root ALL even for a non-root creator.
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_bootstrap\tDROP\ttrue\njandibat_backup_bootstrap\tUSAGE\ttrue\njandibat_backup_runner\tUSAGE\tfalse\njandibat_backup_verifier\tUSAGE\tfalse\nroot\tALL\tf\n',
        mutations: ['jandibat_backup_runner\tUSAGE\ttrue', 'jandibat_backup_runner\tUPDATE\tfalse', 'jandibat_backup_verifier\tUSAGE\ttrue', 'jandibat_backup_bootstrap\tALL\ttrue', 'public\tUSAGE\tfalse', 'root\tALL\tf', 'root\tALL\ttrue'],
      },
    ];
    for (const { name, fn, valid, mutations } of grants) {
      const path = join(dir, `${name}.tsv`);
      const run = () => spawnSync('sh', ['-c', `. scripts/backup-fixture-client.sh; ${fn} "$GRANT_FILE"`], {
        encoding: 'utf8', env: { ...process.env, GRANT_FILE: path },
      });
      writeFileSync(path, valid);
      assert.equal(run().status, 0, `${name} valid grant set rejected`);
      for (const mutation of mutations) {
        const sourceLine = name === 'system' ? 'jandibat_backup_bootstrap\tEXTERNALCONNECTION\tfalse'
          : name === 'database' ? 'jandibat_backup_runner\tBACKUP\tfalse' : 'jandibat_backup_runner\tUSAGE\tfalse';
        writeFileSync(path, mutation.includes(sourceLine.split('\t').slice(0, 2).join('\t'))
          ? valid.replace(sourceLine, mutation) : valid + mutation + '\n');
        assert.equal(run().status, 1, `${name} accepted ${mutation}`);
      }
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
