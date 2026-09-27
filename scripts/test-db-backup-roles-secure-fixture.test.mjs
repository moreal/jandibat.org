import assert from 'node:assert/strict';
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

const fakeEffectiveLogConfig = `sinks:
  file-groups:
    default:
    ops:
    sql-audit:
    security:
    sql-auth:
    sql-exec:
  stderr:
capture-stray-errors:
  enable: true
`;

test('fixture keeps the reviewed effective log routes and every audit channel', () => {
  assert.ok(existsSync('scripts/fixtures/cockroach-backup-logging.yaml'), 'reviewed logging policy missing');
  const config = readFileSync('scripts/fixtures/cockroach-backup-logging.yaml', 'utf8');
  assert.equal(config, `file-defaults:
  redact: false
  buffered-writes: false
sinks:
  file-groups:
    default:
      channels:
        INFO: "all except [OPS, SENSITIVE_ACCESS, USER_ADMIN, PRIVILEGES, SESSIONS, SQL_EXEC]"
    ops:
      channels:
        WARNING: [OPS]
    sql-audit:
      channels: [SENSITIVE_ACCESS]
      auditable: true
    security:
      channels: [USER_ADMIN, PRIVILEGES]
      auditable: true
    sql-auth:
      channels: [SESSIONS]
      auditable: true
    sql-exec:
      channels: [SQL_EXEC]
      auditable: true
  stderr:
    channels: "all except OPS"
    filter: INFO
    redact: false
`);
});

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
      encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'true', PATH: `${dir}:${process.env.PATH}` },
    });
    assert.equal(result.status, 77);
    assert.match(result.stderr, /SKIP 77/);
    assert.doesNotMatch(result.stderr, /docker-invoked/);
    assert.doesNotMatch(result.stderr, /::error/);
    assert.equal(result.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('fixture startup failures report ordered fixed phases without leaking details', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-key-'));
  try {
    const fake = {
      uname: '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n',
      docker: `#!/bin/sh
if [ "$TEST_FAILURE" = db-cleanup ] && [ "$1" = rm ]; then exit 99; fi
if [ "$TEST_FAILURE" = image ]; then
  [ "$1 $2" = 'image inspect' ] && exit 1
  if [ "$1" = pull ]; then echo 'synthetic-secret /synthetic/private/path' >&2; exit 99; fi
fi
case "$*" in
  *"cert create-ca"*|*"cert create-node"*|*"cert create-client"*)
    if [ "$TEST_FAILURE" = certificate ] && [ "$1" = run ]; then
      case "$*" in *"cert create-node"*) echo 'synthetic-secret /synthetic/private/path' >&2; exit 99;; esac
    fi
    for arg do
      case "$arg" in type=bind,src=*,dst=/certs) cert_dir=\${arg#type=bind,src=}; cert_dir=\${cert_dir%,dst=/certs};; esac
    done
    case "$*" in
      *"cert create-ca"*) [ "$TEST_FAILURE" = ca ] || printf 'ca' >"$cert_dir/ca.crt";;
      *"cert create-node"*)
        printf 'node' >"$cert_dir/node.crt"
        [ "$TEST_FAILURE" = key ] || printf 'key' >"$cert_dir/node.key";;
    esac
    exit 0;;
  *"start-single-node"*)
    echo 'synthetic-secret /synthetic/private/path' >&2
    exit 99;;
  *"debug check-log-config"*)
    [ "$TEST_FAILURE" = log ] && exit 99
    sed -n 'p' "$TEST_EFFECTIVE"
    exit 0;;
esac
exit 0
`,
      s3proxy: '#!/bin/sh\nexit 0\n',
      mc: '#!/bin/sh\nexit 0\n',
      openssl: '#!/bin/sh\necho synthetic-secret /synthetic/private/path >&2\n[ "$TEST_FAILURE" = pkcs ] && exit 99\nexit 0\n',
    };
    for (const [name, source] of Object.entries(fake)) {
      writeFileSync(join(dir, name), source);
      chmodSync(join(dir, name), 0o700);
    }
    const effective = join(dir, 'effective.yaml');
    writeFileSync(effective, fakeEffectiveLogConfig);
    for (const [failure, reason, phases, annotationPhase] of [
      ['image', 'Cockroach image availability', ['Cockroach image availability'], 'cockroach-image'],
      ['certificate', 'certificate generation', ['Cockroach image availability', 'certificate generation'], 'certificate-generation'],
      ['key', 'host node key unreadable', ['Cockroach image availability', 'certificate generation', 'host node key readability'], 'host-node-key'],
      ['ca', 'CA certificate copy', ['Cockroach image availability', 'certificate generation', 'host node key readability', 'CA certificate copy'], 'ca-certificate-copy'],
      ['pkcs', 'PKCS#12 creation', ['Cockroach image availability', 'certificate generation', 'host node key readability', 'CA certificate copy', 'PKCS#12 creation'], 'pkcs12-creation'],
      ['log', 'log config validation', ['Cockroach image availability', 'certificate generation', 'host node key readability', 'CA certificate copy', 'PKCS#12 creation', 'log config validation'], 'log-config-validation'],
      ['db', 'DB start', ['Cockroach image availability', 'certificate generation', 'host node key readability', 'CA certificate copy', 'PKCS#12 creation', 'log config validation', 'S3Proxy startup', 'DB start'], 'db-start'],
      ['db-cleanup', 'DB start', ['Cockroach image availability', 'certificate generation', 'host node key readability', 'CA certificate copy', 'PKCS#12 creation', 'log config validation', 'S3Proxy startup', 'DB start'], 'db-start'],
    ]) {
      const result = spawnSync('sh', ['scripts/test-db-backup-roles-secure.sh'], {
        encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'true', TEST_FAILURE: failure, TEST_EFFECTIVE: effective, PATH: `${dir}:${process.env.PATH}` },
      });
      assert.equal(result.status, 1, failure);
      assert.ok(result.stderr.includes(`RED: ${reason} (details redacted)`), failure);
      assert.deepEqual([...result.stderr.matchAll(/PHASE: ([^\n]+)/g)].map((match) => match[1]), phases, failure);
      assert.deepEqual([...result.stderr.matchAll(/^::error[^\n]*$/gm)].map((match) => match[0]), [
        `::error title=Secure backup fixture failure::phase=${annotationPhase}`,
      ], failure);
      assert.doesNotMatch(result.stderr, /synthetic-secret|\/synthetic\/private\/path|\/certs\/|\/ca-only\/|backup-fixture-key-/, failure);
      assert.equal(result.stdout, '', failure);
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('unexpected setup exit has one fixed CI annotation', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-unexpected-'));
  try {
    const fake = {
      uname: '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n',
      docker: '#!/bin/sh\nexit 0\n',
      s3proxy: '#!/bin/sh\nexit 0\n',
      mc: '#!/bin/sh\nexit 0\n',
      openssl: '#!/bin/sh\nexit 0\n',
      mkdir: '#!/bin/sh\nexit 99\n',
    };
    for (const [name, source] of Object.entries(fake)) {
      writeFileSync(join(dir, name), source);
      chmodSync(join(dir, name), 0o700);
    }
    const result = spawnSync('sh', ['scripts/test-db-backup-roles-secure.sh'], {
      encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'true', PATH: `${dir}:${process.env.PATH}` },
    });
    assert.equal(result.status, 99);
    assert.deepEqual([...result.stderr.matchAll(/^::error[^\n]*$/gm)].map((match) => match[0]), [
      '::error title=Secure backup fixture failure::phase=setup',
    ]);
    assert.doesNotMatch(result.stderr, /backup-fixture-unexpected-/);
    assert.equal(result.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('cleanup annotates its own failure and leaves successful CI runs unannotated', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-cleanup-annotation-'));
  try {
    for (const [name, source] of Object.entries({
      uname: '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n',
      docker: '#!/bin/sh\nexit 0\n',
      s3proxy: '#!/bin/sh\nexit 0\n',
      mc: '#!/bin/sh\nexit 0\n',
      openssl: '#!/bin/sh\nexit 0\n',
      rm: '#!/bin/sh\n/bin/rm "$@"\n[ "${TEST_CLEANUP_FAILURE:-}" = true ] && exit 1\nexit 0\n',
    })) {
      writeFileSync(join(dir, name), source);
      chmodSync(join(dir, name), 0o700);
    }
    const fixture = readFileSync('scripts/test-db-backup-roles-secure.sh', 'utf8');
    const cleanupTrap = '\ntrap cleanup EXIT\n';
    assert.ok(fixture.includes(cleanupTrap), 'fixture cleanup trap missing');
    const setupAndCleanup = `${fixture.split(cleanupTrap)[0]}${cleanupTrap}\nexit 0\n`;
    const run = (cleanupFailure) => spawnSync('sh', ['-c', setupAndCleanup], {
      encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'true', TEST_CLEANUP_FAILURE: cleanupFailure ? 'true' : 'false', PATH: `${dir}:${process.env.PATH}` },
    });
    const failed = run(true);
    assert.equal(failed.status, 1);
    assert.match(failed.stderr, /RED: secure fixture private cleanup incomplete \(details redacted\)/);
    assert.deepEqual([...failed.stderr.matchAll(/^::error[^\n]*$/gm)].map((match) => match[0]), [
      '::error title=Secure backup fixture failure::phase=cleanup',
    ]);
    assert.doesNotMatch(failed.stderr, /backup-fixture-cleanup-annotation-/);
    assert.equal(failed.stdout, '');
    const successful = run(false);
    assert.equal(successful.status, 0, successful.stderr);
    assert.doesNotMatch(successful.stderr, /::error/);
    assert.equal(successful.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('certificate containers use the host identity so the node key is readable', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-cert-owner-'));
  try {
    const fake = {
      uname: '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n',
      docker: `#!/bin/sh
case "$*" in
  *"cert create-ca"*|*"cert create-node"*|*"cert create-client"*)
    for arg do
      case "$arg" in
        type=bind,src=*,dst=/certs) cert_dir=\${arg#type=bind,src=}; cert_dir=\${cert_dir%,dst=/certs};;
        --user) expect_user=true;;
        "$TEST_HOST_ID") [ "\${expect_user:-}" = true ] && host_user=true;;
      esac
    done
    [ "\${host_user:-}" = true ] || exit 0
    case "$*" in
      *"cert create-ca"*) printf 'ca' >"$cert_dir/ca.crt";;
      *"cert create-node"*) printf 'node' >"$cert_dir/node.crt"; printf 'key' >"$cert_dir/node.key";;
      *"cert create-client"*) printf 'client' >"$cert_dir/client.root.crt";;
    esac
    printf '%s\n' "$*" >>"$TEST_CERT_RUNS"
    exit 0;;
  *"debug check-log-config"*) printf '%s\n' "$@" >"$TEST_LOG_VALIDATE_ARGS"; sed -n 'p' "$TEST_EFFECTIVE"; exit 0;;
  *"start-single-node"*) printf '%s\n' "$@" >"$TEST_LOG_START_ARGS"; exit 99;;
esac
exit 0
`,
      s3proxy: '#!/bin/sh\nexit 0\n',
      mc: '#!/bin/sh\nexit 0\n',
      openssl: '#!/bin/sh\nexit 0\n',
    };
    for (const [name, source] of Object.entries(fake)) {
      writeFileSync(join(dir, name), source);
      chmodSync(join(dir, name), 0o700);
    }
    const hostId = `${process.getuid()}:${process.getgid()}`;
    const certRuns = join(dir, 'cert-runs');
    const validateArgs = join(dir, 'validate-args');
    const startArgs = join(dir, 'start-args');
    const effective = join(dir, 'effective.yaml');
    writeFileSync(effective, fakeEffectiveLogConfig);
    const result = spawnSync('sh', ['scripts/test-db-backup-roles-secure.sh'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, TEST_HOST_ID: hostId, TEST_CERT_RUNS: certRuns, TEST_LOG_VALIDATE_ARGS: validateArgs, TEST_LOG_START_ARGS: startArgs, TEST_EFFECTIVE: effective },
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /RED: DB start \(details redacted\)/);
    assert.deepEqual([...result.stderr.matchAll(/PHASE: ([^\n]+)/g)].map((match) => match[1]), [
      'Cockroach image availability', 'certificate generation', 'host node key readability',
      'CA certificate copy', 'PKCS#12 creation', 'log config validation', 'S3Proxy startup', 'DB start',
    ]);
    const runs = readFileSync(certRuns, 'utf8').trim().split('\n');
    assert.equal(runs.length, 3);
    for (const run of runs) assert.equal(run.split(' ').filter((arg) => arg === '--user').length, 1);
    for (const argsFile of [validateArgs, startArgs]) {
      assert.ok(existsSync(argsFile), `${argsFile} missing`);
      const args = readFileSync(argsFile, 'utf8');
      assert.match(args, /--log-config-file=\/fixture-logging\/cockroach-backup-logging\.yaml/);
      assert.match(args, /dst=\/fixture-logging,readonly/);
    }
    assert.doesNotMatch(result.stderr, /backup-fixture-cert-owner-|synthetic-secret|\/certs\/|\/ca-only\//);
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

test('fixture sentinel scanner detects raw and RFC 3986 encoded credentials without printing them', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-sentinel-'));
  try {
    const log = join(dir, 'sink.log');
    const run = () => spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_has_sentinel "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, LOG_DIR: dir, synthetic_access: 'fixture+access', synthetic_secret: 'fixture secret' },
    });
    writeFileSync(log, 'no private value\n');
    assert.equal(run().status, 1);
    for (const exposed of ['fixture+access', 'fixture%2Baccess', 'fixture secret', 'fixture%20secret']) {
      writeFileSync(log, `private ${exposed}\n`);
      const result = run();
      assert.equal(result.status, 0, exposed);
      assert.equal(result.stdout, '');
      assert.equal(result.stderr, '');
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('fixture sentinel scanner detects 48 repeated reserved bytes after percent encoding', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-long-encoded-'));
  try {
    writeFileSync(join(dir, 'sink.log'), '%2B'.repeat(48));
    const result = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_has_sentinel "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, LOG_DIR: dir, synthetic_secret: '+'.repeat(48) },
    });
    assert.equal(result.status, 0);
    assert.equal(result.stdout, '');
    assert.equal(result.stderr, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('custom S3 CREATE failure reports only validated SQLSTATE and fixed category after sentinel scan', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-diagnostic-'));
  try {
    const fixture = readFileSync('scripts/test-db-backup-roles-secure.sh', 'utf8');
    const start = fixture.indexOf('\ndiagnose_custom_s3_create() {');
    const end = fixture.indexOf('\ncleanup() {', start);
    assert.ok(start >= 0 && end > start, 'custom S3 CREATE diagnostic missing');
    const diagnostic = fixture.slice(start, end);
    const capture = join(dir, 'capture');
    const calls = join(dir, 'calls');
    const secret = 'private+credential';
    const run = (output, extraEnv = {}) => {
      writeFileSync(capture, output);
      writeFileSync(calls, '');
      const result = spawnSync('sh', ['-c', `. scripts/backup-fixture-client.sh
fail() { echo "RED: $1 (details redacted)" >&2; exit 1; }
fixture_dir=$TEST_FIXTURE_DIR
probe_uri=$TEST_PROBE_URI
sql_as() {
 [ "$1" = root ] || return 2
 case "$2" in
  "CREATE EXTERNAL CONNECTION jandibat_root_diagnostic AS '$TEST_PROBE_URI';")
   printf 'root-create\\n' >>"$TEST_CALLS"
   [ "$TEST_ROOT_OUTPUT" = empty ] || printf '%s\\n' "$TEST_ROOT_OUTPUT" >&2
   [ "$TEST_ROOT_CREATE" = success ];;
  'DROP EXTERNAL CONNECTION jandibat_root_diagnostic;')
   printf 'root-drop\\n' >>"$TEST_CALLS"
   [ "$TEST_DROP_OUTPUT" = empty ] || printf '%s\\n' "$TEST_DROP_OUTPUT" >&2
   [ "$TEST_ROOT_DROP" = success ];;
  *) printf 'unexpected\\n' >>"$TEST_CALLS"; return 2;;
 esac
}
${diagnostic}
diagnose_custom_s3_create "$CAPTURE"`], {
        encoding: 'utf8', env: { ...process.env, CAPTURE: capture, TEST_FIXTURE_DIR: dir, TEST_CALLS: calls,
          TEST_PROBE_URI: 's3://disposable-backup/fixture-only?AWS_SECRET_ACCESS_KEY=private%2Bcredential',
          synthetic_secret: secret, TEST_ROOT_CREATE: 'failure', TEST_ROOT_DROP: 'success',
          TEST_ROOT_OUTPUT: 'private root detail', TEST_DROP_OUTPUT: 'empty', ...extraEnv },
      });
      return { ...result, calls: readFileSync(calls, 'utf8') };
    };
    for (const [output, expected] of [
      ['', 'RED: custom S3 CREATE probe capture=empty root=failure category=unknown SQLSTATE=unavailable (details redacted)\n'],
      ['ERROR: operation failed\nSQLSTATE: 57014\nprivate path /tmp/hidden\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=sql SQLSTATE=57014 (details redacted)\n'],
      ['docker: Error response from daemon: private path /tmp/hidden\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=client-launch SQLSTATE=unavailable (details redacted)\n'],
      ['invalid URL escape in s3://private-host/hidden\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=uri SQLSTATE=unavailable (details redacted)\n'],
      ['x509: certificate signed by unknown authority at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=tls SQLSTATE=unavailable (details redacted)\n'],
      ['S3 API error: NoSuchBucket at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=storage SQLSTATE=unavailable (details redacted)\n'],
      ['S3 API error: AccessDenied at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=storage SQLSTATE=unavailable (details redacted)\n'],
      ['dial tcp: connection refused at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=transport-client SQLSTATE=unavailable (details redacted)\n'],
      ['opaque client failure at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=unknown SQLSTATE=unavailable (details redacted)\n'],
      ['x509: certificate error and NoSuchBucket at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=unknown SQLSTATE=unavailable (details redacted)\n'],
      ['SQLSTATE: 57014 and x509: certificate error at private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=unknown SQLSTATE=57014 (details redacted)\n'],
      ['SQLSTATE: 57014X private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=unknown SQLSTATE=unavailable (details redacted)\n'],
      ['SQLSTATE: 57014x private-host\n', 'RED: custom S3 CREATE probe capture=nonempty root=failure category=unknown SQLSTATE=unavailable (details redacted)\n'],
    ]) {
      const result = run(output);
      assert.equal(result.status, 1);
      assert.equal(result.stderr, expected);
      assert.equal(result.stdout, '');
      assert.equal(result.calls, 'root-create\n');
    }
    for (const form of [secret, 'private%2Bcredential']) {
      const result = run(`SQLSTATE: 57014\n${form}\n`);
      assert.equal(result.status, 1);
      assert.equal(result.stderr, 'RED: custom S3 CREATE diagnostic exposed synthetic credential (details redacted)\n');
      assert.equal(result.stdout, '');
      assert.equal(result.calls, '');
    }
    const rootSuccess = run('opaque client failure\n', { TEST_ROOT_CREATE: 'success', TEST_ROOT_OUTPUT: 'empty' });
    assert.equal(rootSuccess.status, 1);
    assert.equal(rootSuccess.stderr, 'RED: custom S3 CREATE probe capture=nonempty root=success category=unknown SQLSTATE=unavailable (details redacted)\n');
    assert.equal(rootSuccess.stdout, '');
    assert.equal(rootSuccess.calls, 'root-create\nroot-drop\n');
    const rootOutputSentinel = run('opaque client failure\n', { TEST_ROOT_CREATE: 'success', TEST_ROOT_OUTPUT: secret });
    assert.equal(rootOutputSentinel.status, 1);
    assert.equal(rootOutputSentinel.stderr, 'RED: custom S3 CREATE diagnostic exposed synthetic credential (details redacted)\n');
    assert.equal(rootOutputSentinel.calls, 'root-create\nroot-drop\n');
    const rootFailureSentinel = run('opaque client failure\n', { TEST_ROOT_OUTPUT: secret });
    assert.equal(rootFailureSentinel.status, 1);
    assert.equal(rootFailureSentinel.stderr, 'RED: custom S3 CREATE diagnostic exposed synthetic credential (details redacted)\n');
    assert.equal(rootFailureSentinel.calls, 'root-create\n');
    const failedDrop = run('opaque client failure\n', { TEST_ROOT_CREATE: 'success', TEST_ROOT_OUTPUT: 'empty', TEST_ROOT_DROP: 'failure' });
    assert.equal(failedDrop.status, 1);
    assert.equal(failedDrop.stderr, 'RED: custom S3 CREATE root diagnostic DROP incomplete (details redacted)\n');
    assert.equal(failedDrop.calls, 'root-create\nroot-drop\n');
    const dropOutputSentinel = run('opaque client failure\n', { TEST_ROOT_CREATE: 'success', TEST_ROOT_OUTPUT: 'empty', TEST_DROP_OUTPUT: secret });
    assert.equal(dropOutputSentinel.status, 1);
    assert.equal(dropOutputSentinel.stderr, 'RED: custom S3 CREATE diagnostic exposed synthetic credential (details redacted)\n');
    assert.equal(dropOutputSentinel.calls, 'root-create\nroot-drop\n');
    writeFileSync(join(dir, 'awk'), '#!/bin/sh\necho "private diagnostic failure" >&2\nexit 2\n');
    chmodSync(join(dir, 'awk'), 0o700);
    const inspectionFailure = run('opaque client failure\n', { PATH: `${dir}:${process.env.PATH}` });
    assert.equal(inspectionFailure.status, 1);
    assert.equal(inspectionFailure.stderr, 'RED: custom S3 CREATE diagnostic inspection unavailable (details redacted)\n');
    assert.equal(inspectionFailure.stdout, '');
    rmSync(join(dir, 'awk'));
    writeFileSync(join(dir, 'grep'), '#!/bin/sh\ncase "$1" in -Eiq) echo "private diagnostic failure" >&2; exit 2;; esac\nexec /usr/bin/grep "$@"\n');
    chmodSync(join(dir, 'grep'), 0o700);
    const matcherFailure = run('opaque client failure\n', { PATH: `${dir}:${process.env.PATH}` });
    assert.equal(matcherFailure.status, 1);
    assert.equal(matcherFailure.stderr, 'RED: custom S3 CREATE diagnostic inspection unavailable (details redacted)\n');
    assert.equal(matcherFailure.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('effective log policy requires every configured file sink and stray capture', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-sinks-'));
  try {
    const effective = join(dir, 'effective.yaml');
    writeFileSync(effective, `sinks:
  file-groups:
    default:
      channels: {INFO: [DEV]}
    ops:
      channels: {WARNING: [OPS]}
    sql-audit:
      channels: {INFO: [SENSITIVE_ACCESS]}
    security:
      channels: {INFO: [USER_ADMIN, PRIVILEGES]}
    sql-auth:
      channels: {INFO: [SESSIONS]}
    sql-exec:
      channels: {INFO: [SQL_EXEC]}
  stderr:
    channels: {INFO: [DEV]}
capture-stray-errors:
  enable: true
`);
    const sinks = ['cockroach.test.log', 'cockroach-ops.test.log', 'cockroach-sql-audit.test.log',
      'cockroach-security.test.log', 'cockroach-sql-auth.test.log', 'cockroach-sql-exec.test.log',
      'cockroach-stderr.log', 'container.log'];
    for (const name of sinks) writeFileSync(join(dir, name), ['cockroach-ops.test.log', 'cockroach-stderr.log', 'container.log'].includes(name) ? '' : 'event\n');
    const run = () => spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_check_log_sinks "$EFFECTIVE" "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, EFFECTIVE: effective, LOG_DIR: dir },
    });
    assert.equal(run().status, 0);
    const canonical = readFileSync(effective, 'utf8');
    writeFileSync(effective, canonical.replace('    security:', '\n    security:'));
    assert.equal(run().status, 0, 'blank line in effective YAML rejected');
    writeFileSync(effective, canonical);
    for (const name of sinks) {
      rmSync(join(dir, name));
      assert.equal(run().status, 1, `missing ${name} accepted`);
      writeFileSync(join(dir, name), ['cockroach-ops.test.log', 'cockroach-stderr.log', 'container.log'].includes(name) ? '' : 'event\n');
    }
    writeFileSync(join(dir, 'cockroach-sql-auth.test.log'), '');
    assert.equal(run().status, 1, 'empty SQL auth sink accepted');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('secure DB cleanup leaves the container for log scanning before removal', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-quiesce-'));
  try {
    const calls = join(dir, 'docker-calls');
    writeFileSync(join(dir, 'docker'), `#!/bin/sh
printf '%s\n' "$1" >>"$TEST_DOCKER_CALLS"
case "$1" in
  cp) printf 'DB log\n' >"$LOG_DIR/cockroach.test.log"; exit 0;;
  stop|wait|logs|rm) exit 0;;
  container) [ "$2" = inspect ] && exit 1;;
  info) exit 0;;
esac
exit 99
`);
    chmodSync(join(dir, 'docker'), 0o700);
    const result = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_quiesce_collect_logs fixture-db "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, LOG_DIR: dir, TEST_DOCKER_CALLS: calls },
    });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(readFileSync(calls, 'utf8').trim().split('\n'), ['stop', 'wait', 'cp', 'logs']);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('secure DB cleanup fails closed when final logs cannot be copied', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-copy-failure-'));
  try {
    const calls = join(dir, 'docker-calls');
    writeFileSync(join(dir, 'docker'), `#!/bin/sh
printf '%s\n' "$1" >>"$TEST_DOCKER_CALLS"
case "$1" in
  cp) exit 1;;
  stop|wait|logs|rm) exit 0;;
  container) [ "$2" = inspect ] && exit 1;;
  info) exit 0;;
esac
exit 99
`);
    chmodSync(join(dir, 'docker'), 0o700);
    const result = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_quiesce_collect_logs fixture-db "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, LOG_DIR: dir, TEST_DOCKER_CALLS: calls },
    });
    assert.equal(result.status, 1);
    assert.deepEqual(readFileSync(calls, 'utf8').trim().split('\n'), ['stop', 'wait', 'cp', 'logs']);
    assert.doesNotMatch(result.stderr, /backup-fixture-copy-failure-/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('secure DB cleanup rejects an empty server-log copy', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-empty-logs-'));
  try {
    writeFileSync(join(dir, 'docker'), '#!/bin/sh\ncase "$1" in stop|wait|cp|logs|rm) exit 0;; esac\nexit 99\n');
    chmodSync(join(dir, 'docker'), 0o700);
    const result = spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_quiesce_collect_logs fixture-db "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, LOG_DIR: dir },
    });
    assert.equal(result.status, 1);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('failed DB start still quiesces and removes its created disposable container', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-partial-start-'));
  try {
    const calls = join(dir, 'lifecycle');
    const marker = join(dir, 'db-created');
    writeFileSync(join(dir, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n');
    writeFileSync(join(dir, 'docker'), `#!/bin/sh
case "$*" in
  *"cert create-ca"*|*"cert create-node"*|*"cert create-client"*)
    for arg do case "$arg" in type=bind,src=*,dst=/certs) cert_dir=\${arg#type=bind,src=}; cert_dir=\${cert_dir%,dst=/certs};; esac; done
    case "$*" in
      *"cert create-ca"*) printf ca >"$cert_dir/ca.crt";;
      *"cert create-node"*) printf node >"$cert_dir/node.crt"; printf key >"$cert_dir/node.key";;
    esac
    exit 0;;
  *"debug check-log-config"*) sed -n 'p' "$TEST_EFFECTIVE"; exit 0;;
  *"start-single-node"*) printf '%s\\n' start >>"$TEST_CALLS"; touch "$TEST_MARKER"; exit 99;;
esac
case "$1" in
  image) exit 0;;
  container) [ -f "$TEST_MARKER" ] && exit 0; exit 1;;
  stop|wait|cp|logs|rm)
    printf '%s\\n' "$1" >>"$TEST_CALLS"
    [ "$1" = rm ] && rm -f "$TEST_MARKER"
    exit 0;;
esac
exit 99
`);
    writeFileSync(join(dir, 's3proxy'), '#!/bin/sh\nexit 0\n');
    writeFileSync(join(dir, 'mc'), '#!/bin/sh\nexit 0\n');
    writeFileSync(join(dir, 'openssl'), '#!/bin/sh\nexit 0\n');
    for (const name of ['uname', 'docker', 's3proxy', 'mc', 'openssl']) chmodSync(join(dir, name), 0o700);
    const effective = join(dir, 'effective.yaml');
    writeFileSync(effective, fakeEffectiveLogConfig);
    const result = spawnSync('sh', ['scripts/test-db-backup-roles-secure.sh'], {
      encoding: 'utf8', env: { ...process.env, PATH: `${dir}:${process.env.PATH}`, TEST_CALLS: calls, TEST_MARKER: marker, TEST_EFFECTIVE: effective },
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /RED: DB start \(details redacted\)/);
    assert.deepEqual(readFileSync(calls, 'utf8').trim().split('\n'), ['start', 'stop', 'wait', 'cp', 'logs', 'rm']);
    assert.equal(existsSync(marker), false);
    assert.equal(result.stdout, '');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('audit marker check requires CREATE SQL, security and denied sensitive read events', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-audit-'));
  try {
    const files = {
      'cockroach-sql-exec.test.log': 'SQL_EXEC CREATE EXTERNAL CONNECTION\n',
      'cockroach-security.test.log': 'PRIVILEGES grant recorded\n',
      'cockroach-sql-audit.test.log': 'SENSITIVE_ACCESS denied system.external_connections 42501\n',
    };
    const run = () => spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_audit_markers "$LOG_DIR"'], {
      encoding: 'utf8', env: { ...process.env, LOG_DIR: dir },
    });
    for (const [name, content] of Object.entries(files)) writeFileSync(join(dir, name), content);
    assert.equal(run().status, 0);
    for (const name of Object.keys(files)) {
      writeFileSync(join(dir, name), 'unrelated event\n');
      assert.equal(run().status, 1, name);
      writeFileSync(join(dir, name), files[name]);
    }
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
        name: 'metadata-schema', fn: 'fixture_check_metadata_grants schema',
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_bootstrap\tUSAGE\tfalse\njandibat_backup_runner\tUSAGE\tfalse\njandibat_backup_verifier\tUSAGE\tfalse\n',
        mutations: ['jandibat_backup_bootstrap\tUSAGE\ttrue', 'jandibat_backup_runner\tUSAGE\ttrue'],
      },
      {
        name: 'metadata-table', fn: 'fixture_check_metadata_grants table',
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_bootstrap\tSELECT\tfalse\njandibat_backup_bootstrap\tINSERT\tfalse\n',
        mutations: ['jandibat_backup_bootstrap\tINSERT\ttrue', 'jandibat_backup_runner\tSELECT\tfalse'],
      },
      {
        name: 'metadata-view', fn: 'fixture_check_metadata_grants view',
        valid: 'grantee\tprivilege_type\tis_grantable\njandibat_backup_bootstrap\tSELECT\tfalse\n',
        mutations: ['jandibat_backup_bootstrap\tSELECT\ttrue', 'jandibat_backup_runner\tSELECT\tfalse'],
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
          : name === 'database' ? 'jandibat_backup_runner\tBACKUP\tfalse'
            : name === 'metadata-schema' ? 'jandibat_backup_bootstrap\tUSAGE\tfalse'
              : name === 'metadata-table' ? 'jandibat_backup_bootstrap\tINSERT\tfalse'
                : name === 'metadata-view' ? 'jandibat_backup_bootstrap\tSELECT\tfalse'
                  : 'jandibat_backup_runner\tUSAGE\tfalse';
        writeFileSync(path, mutation.includes(sourceLine.split('\t').slice(0, 2).join('\t'))
          ? valid.replace(sourceLine, mutation) : valid + mutation + '\n');
        assert.equal(run().status, 1, `${name} accepted ${mutation}`);
      }
    }
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('metadata schema grant check accepts only three non-grantable USAGE roles', () => {
  const dir = mkdtempSync(join(tmpdir(), 'backup-fixture-schema-grants-'));
  try {
    const path = join(dir, 'schema.tsv');
    const header = 'grantee\tprivilege_type\tis_grantable\n';
    const rows = [
      'jandibat_backup_bootstrap\tUSAGE\tfalse',
      'jandibat_backup_runner\tUSAGE\tfalse',
      'jandibat_backup_verifier\tUSAGE\tfalse',
    ];
    const run = (grantRows, grantHeader = header) => {
      writeFileSync(path, grantHeader + grantRows.join('\n') + '\n');
      return spawnSync('sh', ['-c', '. scripts/backup-fixture-client.sh; fixture_check_metadata_grants schema "$GRANT_FILE"'], {
        encoding: 'utf8', env: { ...process.env, GRANT_FILE: path },
      });
    };

    assert.equal(run(rows).status, 0, 'exact schema grant set rejected');
    for (const role of ['bootstrap', 'runner', 'verifier']) {
      const grantee = `jandibat_backup_${role}`;
      assert.equal(run(rows.filter((row) => !row.startsWith(`${grantee}\t`))).status, 1, `missing ${role} accepted`);
      assert.equal(run(rows.map((row) => row.startsWith(`${grantee}\t`) ? `${grantee}\tUSAGE\ttrue` : row)).status, 1, `grantable ${role} accepted`);
    }
    assert.equal(run([...rows, 'jandibat_backup_runner\tSELECT\tfalse']).status, 1, 'extra privilege accepted');
    assert.equal(run([...rows, 'public\tUSAGE\tfalse']).status, 1, 'extra grantee accepted');
    assert.equal(run(rows, 'grantee\tprivilege_type\tis_grantable\textra\n').status, 1, 'four-field header accepted');
    assert.equal(run(rows.map((row) => row.startsWith('jandibat_backup_runner\t') ? `${row}\textra` : row)).status, 1, 'four-field data row accepted');
    assert.equal(run([...rows, rows[1]]).status, 1, 'duplicate runner grant accepted');
  } finally { rmSync(dir, { recursive: true, force: true }); }
});
