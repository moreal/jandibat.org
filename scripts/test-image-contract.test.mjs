import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
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
]) {
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-image-contract-'));
  try {
    const root = join(scratch, 'root');
    for (const path of [
      'busybox', `bin/${programs[name]}`,
      `nix/store/fixture-payload/bin/${programs[name]}`,
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
      for (const path of ['busybox', `bin/${programs[name]}`, 'etc/ssl/certs/ca-certificates.crt']) {
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
    const commands = {
      nix: `#!/bin/sh
case "$1" in
  eval) case "$*" in *builtins.currentSystem*) echo x86_64-linux;; esac ;;
  build) for arg do case "$arg" in .#*-image) name=\${arg#.#}; name=\${name%-image}; echo "$FIXTURES/$name.tar";; esac; done ;;
esac
`,
      sha256sum: '#!/bin/sh\necho "abc  $1"\n',
      sleep: '#!/bin/sh\nexit 0\n',
      docker: `#!/bin/sh
case "$1" in
  load) echo 'Loaded image: fixture';;
  run) echo web-fixture-id;;
  exec)
    case "$*" in
      *'/healthz'*) case "$FAIL_MODE" in health*) exit 1;; esac; echo ok;;
      *'/config.json'*) echo '{"apiBaseUrl":"https://api.example.test"}';;
      *'http://127.0.0.1:8080/'*) echo 'HTTP/1.1 200 OK' >&2; echo 'Content-Security-Policy: connect-src https://wrong.example.test' >&2;;
    esac ;;
  inspect) if [ "$FAIL_MODE" = health127 ]; then echo 'exited 127'; else echo 'exited 1'; fi;;
  logs) echo 'nginx: permission denied token=SUPER_SECRET_VALUE' >&2;;
esac
`,
    };
    for (const [name, contents] of Object.entries(commands)) {
      const path = join(bin, name);
      writeFileSync(path, contents);
      chmodSync(path, 0o755);
    }
    return spawnSync('sh', [validator], {
      cwd: scratch, encoding: 'utf8',
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FIXTURES: scratch, FAIL_MODE: mode },
    });
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

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
  assert.doesNotMatch(result.stderr, /SUPER_SECRET_VALUE/);
});

test('web failure context preserves a nonstandard numeric exit code', () => {
  const result = runtimeFailure('health127');
  assert.equal(result.status, 1);
  assert.match(result.stderr, /web container: status=exited exit=127/);
});
