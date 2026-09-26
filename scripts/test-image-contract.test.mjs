import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const validator = fileURLToPath(new URL('./test-image-contract.sh', import.meta.url));
const programs = { api: 'server', worker: 'worker', maintenance: 'maintenance', web: 'web-start' };

// Exercise the real archive boundary: root tar entries become bin/... after
// normalization, while a Nix store entry retains a prefix before /bin/.
function inspectFixture(name, extraPath, certificatePaths = [
  'etc/ssl/certs/ca-certificates.crt',
  'nix/store/fixture-ca/etc/ssl/certs/ca-certificates.crt',
], includeProxy = name === 'api') {
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-image-contract-'));
  try {
    const root = join(scratch, 'root');
    for (const path of [
      'busybox', `bin/${programs[name]}`,
      `nix/store/fixture-payload/bin/${programs[name]}`,
      ...(includeProxy ? ['bin/metrics-proxy', 'nix/store/fixture-payload/bin/metrics-proxy'] : []),
      ...certificatePaths,
      ...(extraPath ? [extraPath] : []),
    ]) {
      const target = join(root, path);
      mkdirSync(dirname(target), { recursive: true });
      writeFileSync(target, 'fixture\n');
    }
    execFileSync('tar', ['-cf', join(scratch, 'layer.tar'), '-C', root, '.']);
    writeFileSync(join(scratch, 'config.json'), JSON.stringify({
      architecture: 'amd64', os: 'linux', created: '1970-01-01T00:00:01Z',
      config: { User: '101:101', Entrypoint: [`/bin/${programs[name]}`], Env: ['PATH=/bin'] },
    }));
    writeFileSync(join(scratch, 'manifest.json'), JSON.stringify([{
      Config: 'config.json', RepoTags: [`jandibat-${name}:nix`], Layers: ['layer.tar'],
    }]));
    const archive = join(scratch, 'image.tar');
    execFileSync('tar', ['-cf', archive, '-C', scratch, 'config.json', 'manifest.json', 'layer.tar']);
    return spawnSync('sh', [validator, '--archive', name, archive], { encoding: 'utf8' });
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

test('accepts the root-relative certificate archive member', () => {
  const result = inspectFixture('api', undefined, ['etc/ssl/certs/ca-certificates.crt']);
  assert.equal(result.status, 0, result.stderr);
});

test('rejects a Nix-store certificate without the root archive member', () => {
  const result = inspectFixture('api', undefined, [
    'nix/store/fixture-ca/etc/ssl/certs/ca-certificates.crt',
  ]);
  assert.equal(result.status, 1, `accepted archive without root certificate: ${result.stdout}`);
  assert.match(result.stderr, /AssertionError/);
});

test('API rejects an archive without its root metrics-proxy executable', () => {
  const result = inspectFixture('api', undefined, undefined, false);
  assert.equal(result.status, 1, `accepted API archive without proxy: ${result.stdout}`);
  assert.match(result.stderr, /metrics proxy executable/);
});

for (const name of ['worker', 'maintenance', 'web']) {
  for (const prefix of ['', 'nix/store/fixture-unwanted/']) {
    test(`${name} rejects metrics-proxy leakage from ${prefix || 'root'}`, () => {
      const result = inspectFixture(name, `${prefix}bin/metrics-proxy`);
      assert.equal(result.status, 1, `accepted proxy in ${name}: ${result.stdout}`);
      assert.match(result.stderr, /metrics proxy executable|metrics-proxy payload leaked/);
    });
  }
}

for (const name of Object.keys(programs)) {
  test(`${name} accepts its own root and Nix-store executable`, () => {
    const result = inspectFixture(name);
    assert.equal(result.status, 0, result.stderr);
  });
}

for (const prefix of ['', 'nix/store/fixture-unwanted/']) {
  for (const program of ['go', 'node', 'npm', 'yarn', 'gcc', 'cc', 'rustc']) {
    const path = `${prefix}bin/${program}`;
    test(`rejects build tool ${path}`, () => {
      const result = inspectFixture('api', path);
      assert.equal(result.status, 1, `accepted forbidden ${path}: ${result.stdout}`);
      assert.match(result.stderr, /AssertionError/);
    });
  }
  for (const [name, expected] of Object.entries(programs)) {
    for (const program of ['server', 'worker', 'maintenance'].filter(value => value !== expected)) {
      const path = `${prefix}bin/${program}`;
      test(`${name} rejects unrelated process ${path}`, () => {
        const result = inspectFixture(name, path);
        assert.equal(result.status, 1, `accepted unrelated ${path}: ${result.stdout}`);
        assert.match(result.stderr, new RegExp(`unrelated ${program} payload leaked`));
      });
    }
  }
}

// The external Nix and Docker boundaries are replaced; the smoke script and
// its archive validation, phase tracking, and failure handler run unchanged.
function runtimeFailure(mode) {
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-image-smoke-'));
  try {
    for (const name of Object.keys(programs)) {
      const root = join(scratch, name);
      for (const path of ['busybox', `bin/${programs[name]}`,
        ...(name === 'api' ? ['bin/metrics-proxy'] : []),
        'etc/ssl/certs/ca-certificates.crt']) {
        const target = join(root, path);
        mkdirSync(dirname(target), { recursive: true });
        writeFileSync(target, 'fixture\n');
      }
      execFileSync('tar', ['-cf', join(scratch, `${name}-layer.tar`), '-C', root, '.']);
      writeFileSync(join(scratch, 'config.json'), JSON.stringify({
        architecture: 'amd64', os: 'linux', created: '1970-01-01T00:00:01Z',
        config: { User: '101:101', Entrypoint: [`/bin/${programs[name]}`], Env: ['PATH=/bin'] },
      }));
      writeFileSync(join(scratch, 'manifest.json'), JSON.stringify([{
        Config: 'config.json', RepoTags: [`jandibat-${name}:nix`], Layers: [`${name}-layer.tar`],
      }]));
      execFileSync('tar', ['-cf', join(scratch, `${name}.tar`), '-C', scratch,
        'config.json', 'manifest.json', `${name}-layer.tar`]);
    }
    const bin = join(scratch, 'bin');
    mkdirSync(bin);
    writeFileSync(join(scratch, 'docker-runs'), '');
    writeFileSync(join(scratch, 'nix-builds'), '');
    const commands = {
      nix: `#!/bin/sh
case "$1" in
  eval) case "$*" in *builtins.currentSystem*) echo x86_64-linux;; esac ;;
  build)
    printf '%s\n' "$*" >>"$FIXTURES/nix-builds"
    offline=false rebuild=false
    for arg do
      case "$arg" in
        --offline) offline=true;;
        --rebuild) rebuild=true;;
        .#*-image) name=\${arg#.#}; name=\${name%-image}; echo "$FIXTURES/$name.tar";;
      esac
    done
    if [ "$FAIL_MODE" = archive-cache-miss ] && [ "$offline" = true ] && [ "$rebuild" = true ]; then
      echo 'GNU Bash source unavailable without substituters' >&2
      exit 88
    fi ;;
esac
`,
      sha256sum: `#!/bin/sh
if [ "$FAIL_MODE" = archive-hash-mismatch ]; then
  count=0
  [ ! -f "$FIXTURES/hash-count" ] || count=$(cat "$FIXTURES/hash-count")
  count=$((count + 1))
  echo "$count" >"$FIXTURES/hash-count"
  if [ "$count" -eq 2 ]; then echo "def  $1"; exit; fi
fi
echo "abc  $1"
`,
      sleep: '#!/bin/sh\nexit 0\n',
      docker: `#!/bin/sh
case "$1" in
  load) echo 'Loaded image: fixture';;
  run)
    printf '%s\n' "$*" >>"$FIXTURES/docker-runs"
    count=0
    [ ! -f "$FIXTURES/run-count" ] || count=$(cat "$FIXTURES/run-count")
    count=$((count + 1))
    echo "$count" >"$FIXTURES/run-count"
    case "$count" in
      1) echo proxy-fixture-id;;
      2) echo web-fixture-id;;
      3) echo readonly-fixture-id;;
      4)
        if [ "$FAIL_MODE" = db-migration-memory ]; then
          case " $* " in *' --max-sql-memory=256MiB '*) touch "$FIXTURES/db-migration-memory-ok";; esac
        fi
        echo db-fixture-id;;
      5) echo api-fixture-id;;
      6) echo worker-fixture-id;;
      7) echo maintenance-fixture-id;;
    esac;;
  exec)
    case "$*" in
      *'proxy-fixture-id'*'/metrics'*) echo 'HTTP/1.1 403 Forbidden' >&2; exit 1;;
      *'CREATE DATABASE image_smoke'*) if [ "$FAIL_MODE" = db-create ] || [ "$FAIL_MODE" = archive-cache-miss ]; then exit 1; fi;;
      *'/migrate.sh'*)
        if [ "$FAIL_MODE" = db-migration-memory ] && [ ! -f "$FIXTURES/db-migration-memory-ok" ]; then exit 1; fi;;
      *'/healthz'*) case "$FAIL_MODE" in health*) exit 1;; esac; echo ok;;
      *'/config.json'*)
        if [ "$FAIL_MODE" = config ]; then echo '{"apiBaseUrl":"https://wrong.example.test"}';
        else echo '{"apiBaseUrl":"https://api.example.test"}'; fi;;
      *'http://127.0.0.1:8080/')
        echo 'HTTP/1.1 200 OK' >&2
        if [ "$FAIL_MODE" = headers ]; then echo 'Content-Security-Policy: connect-src https://wrong.example.test' >&2
        else echo "Content-Security-Policy: connect-src 'self' https://api.example.test" >&2; fi;;
      *'api-fixture-id'*'/livez'*) if [ "$FAIL_MODE" = api-health ]; then exit 1; fi; echo ok;;
    esac ;;
  inspect)
    case "$*" in
      *State.Status*State.ExitCode*)
        if [ "$FAIL_MODE" = readonly-marker ]; then
          case "$*" in *readonly-fixture-id*) echo 'exited 1';; *web-fixture-id*) echo 'running 0';; esac
        elif [ "$FAIL_MODE" = health127 ]; then echo 'exited 127'
        else echo 'exited 1'; fi;;
      *State.Running*) echo false;;
      *State.ExitCode*) echo 1;;
    esac;;
  logs)
    case "$*" in
      *api-fixture-id*) echo 'api: connection failed token=SUPER_SECRET_VALUE' >&2;;
      *readonly-fixture-id*)
        if [ "$FAIL_MODE" = readonly-marker ]; then
          echo "sh: can't create /tmp/runtime-config.json: Read-only file system token=SUPER_SECRET_VALUE" >&2
        else echo 'runtime output directory must exist and be writable' >&2; fi;;
      *) echo 'nginx: permission denied token=SUPER_SECRET_VALUE' >&2;;
    esac;;
  compose) echo '{"services":{"cockroach":{"image":"cockroach-fixture"}}}';;
  network) echo smoke-network-fixture;;
esac
`,
    };
    for (const [name, contents] of Object.entries(commands)) {
      const path = join(bin, name);
      writeFileSync(path, contents);
      chmodSync(path, 0o755);
    }
    const result = spawnSync('sh', [validator], {
      cwd: scratch, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FIXTURES: scratch, FAIL_MODE: mode },
    });
    return { ...result,
      dockerRuns: readFileSync(join(scratch, 'docker-runs'), 'utf8'),
      nixBuilds: readFileSync(join(scratch, 'nix-builds'), 'utf8').trim().split('\n'),
    };
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

test('archive rebuild can realize a missing input and reaches container smoke', () => {
  const result = runtimeFailure('archive-cache-miss');
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /image smoke failed: database fixture create schema/);
  assert.doesNotMatch(result.stderr, /GNU Bash source unavailable/);
  assert.match(result.dockerRuns, /cockroach-fixture/);
  for (const [index, name] of Object.keys(programs).entries()) {
    assert.equal(result.nixBuilds[index * 2], `build --no-link --print-out-paths .#${name}-image`);
    assert.equal(result.nixBuilds[index * 2 + 1], `build --rebuild --option sandbox true --no-link .#${name}-image`);
  }
  assert.equal(result.nixBuilds.length, 8);
});

test('archive rebuild rejects a changed SHA before Docker load', () => {
  const result = runtimeFailure('archive-hash-mismatch');
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /image smoke failed: archive rebuild: api/);
  assert.equal(result.dockerRuns, '');
});

test('image smoke migrations advance with enough fixture SQL memory', () => {
  const result = runtimeFailure('db-migration-memory');
  assert.equal(result.status, 1, result.stderr);
  assert.match(result.stderr, /image smoke failed: dependency loss readyz: api/);
});

test('API proxy launches explicitly with only metrics configuration before the DB fixture starts', () => {
  const result = runtimeFailure('db-create');
  const lines = result.dockerRuns.trim().split('\n');
  const proxy = lines.find(line => line.includes('--entrypoint /bin/metrics-proxy'));
  assert.ok(proxy, 'API image was never run as /bin/metrics-proxy');
  assert.match(proxy, /-e METRICS_SOURCE=api/);
  assert.match(proxy, /-e METRICS_LISTEN_ADDR=127\.0\.0\.1:9090/);
  assert.match(proxy, /-e METRICS_TOKEN_FILE=\/run\/secrets\/metrics-token/);
  assert.doesNotMatch(proxy, /DATABASE_URL|COCKROACH_METRICS_|APP_ENV/);
  assert.ok(lines.indexOf(proxy) < lines.findIndex(line => line.includes('cockroach-fixture')));
});

test('failed web health probe reports its phase and safe container context', () => {
  const result = runtimeFailure('health');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: web healthz/);
  assert.match(result.stderr, /web container: status=exited exit=1/);
  assert.match(result.stderr, /web logs:.*permission denied/);
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE/);
});

test('failed CSP assertion reports the phase and bounded header context', () => {
  const result = runtimeFailure('headers');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: web CSP headers/);
  assert.match(result.stderr, /web headers:.*Content-Security-Policy/);
  assert.match(result.stderr, /web CSP: connect-src mismatch/);
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE/);
});

test('missing no-tmpfs marker reports the stopped negative web container only', () => {
  const result = runtimeFailure('readonly-marker');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: web read-only without tmpfs required marker/);
  assert.match(result.stderr, /web read-only without tmpfs container: status=exited exit=1/);
  assert.match(result.stderr, /web read-only without tmpfs logs:.*read-only file system.*can't create/);
  assert.doesNotMatch(result.stderr, /^web (?:container|logs|headers):/m);
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE|runtime-config\.json/);
});

test('failed web config assertion reports a fixed reason without the response body', () => {
  const result = runtimeFailure('config');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: web config.json body/);
  assert.match(result.stderr, /web config.json: apiBaseUrl mismatch/);
  assert.doesNotMatch(result.stderr, /wrong.example.test/);
});

test('API health timeout reports safe context for the API container', () => {
  const result = runtimeFailure('api-health');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: api livez/);
  assert.match(result.stderr, /api container: status=exited exit=1/);
  assert.match(result.stderr, /api logs: failed/);
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE/);
});

test('database failure does not attribute web logs to the database step', () => {
  const result = runtimeFailure('db-create');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: database fixture create schema/);
  assert.doesNotMatch(result.stderr, /web (container|logs|headers):/);
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE/);
});

test('dependency-loss readiness failure names the API container', () => {
  const result = runtimeFailure('dependency-ready');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /image smoke failed: dependency loss readyz: api/);
  assert.match(result.stderr, /api container: status=exited exit=1/);
  assert.match(result.stderr, /api logs: failed/);
  assert.doesNotMatch(result.stderr, /web logs:/);
});

test('web failure context preserves a nonstandard numeric exit code', () => {
  const result = runtimeFailure('health127');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /web container: status=exited exit=127/);
});
