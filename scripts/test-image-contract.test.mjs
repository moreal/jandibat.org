import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
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
