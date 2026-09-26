import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, readlinkSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const runtimeSource = 'registry.access.redhat.com/ubi10/ubi-micro';
const donorSource = 'cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282';
const bootstrapSourceSha256 = 'cd6df5adfa0fcc557b50c85fe51e577631cdcaba1fb2eab2a15ba904399c0d60';
const applets = ['awk', 'chmod', 'cp', 'mktemp', 'rm', 'sed', 'sha256sum', 'sh', 'tail', 'tr'];
const sha = bytes => createHash('sha256').update(bytes).digest('hex');
const fileSha = path => sha(readFileSync(path));
const digest = value => /^sha256:[a-f0-9]{64}$/.test(value ?? '');
const hex = value => /^[a-f0-9]{64}$/.test(value ?? '');
const absolute = value => typeof value === 'string' && /^\/[A-Za-z0-9._/+,-]+$/.test(value) && !value.includes('..');
const required = (value, label) => assert.ok(value, `missing ${label}`);

function command(name, args, options = {}) {
  const result = spawnSync(name, args, { cwd: root, encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, ...options });
  if (result.error || (!options.allowFailure && result.status !== 0)) throw new Error(`${name} failed`);
  return result;
}

function pathsUnder(directory, prefix = '') {
  if (!existsSync(directory)) return [];
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = join(directory, entry.name);
    const name = `${prefix}/${entry.name}`;
    return entry.isDirectory() ? pathsUnder(path, name) : [name];
  });
}

function containedFile(rootfs, path) {
  let target = resolve(rootfs, `.${path}`);
  for (let i = 0; i < 16; i++) {
    if (!target.startsWith(`${rootfs}/`)) throw new Error('path escaped inspected root');
    const stat = lstatSync(target);
    if (!stat.isSymbolicLink()) {
      if (!stat.isFile()) throw new Error('inspected path is not a file');
      return target;
    }
    const link = readlinkSync(target);
    target = link.startsWith('/') ? resolve(rootfs, `.${link}`) : resolve(dirname(target), link);
  }
  throw new Error('symlink depth exceeded');
}

function regular(rootfs, path) {
  try { containedFile(rootfs, path); return true; } catch { return false; }
}

function hashPath(rootfs, path) {
  return { path, sha256: fileSha(containedFile(rootfs, path)) };
}

function parseOsRelease(rootfs) {
  const path = join(rootfs, 'etc/os-release');
  const lines = readFileSync(path, 'utf8').split('\n');
  const fields = Object.fromEntries(lines.map(line => line.match(/^([A-Z_]+)="?([^"\n]*)"?$/)).filter(Boolean).map(match => [match[1], match[2]]));
  return { ID: fields.ID, VERSION_ID: fields.VERSION_ID };
}

function resolveImage(source, requireIndex) {
  const raw = command('skopeo', ['inspect', '--raw', `docker://${source}`]).stdout;
  const manifest = JSON.parse(raw);
  const indexDigest = `sha256:${sha(raw)}`;
  const pinned = source.match(/@(?<digest>sha256:[a-f0-9]{64})$/)?.groups?.digest;
  if (pinned) assert.equal(indexDigest, pinned, 'donor digest mismatch');
  if (Array.isArray(manifest.manifests)) {
    const child = manifest.manifests.find(item => item.platform?.os === 'linux' && item.platform?.architecture === 'amd64');
    required(child && digest(child.digest), 'linux/amd64 child digest');
    return { indexDigest, amd64Digest: child.digest };
  }
  if (requireIndex) throw new Error('missing linux/amd64 child digest');
  required(pinned && pinned === indexDigest, 'pinned donor manifest');
  const inspected = JSON.parse(command('skopeo', ['inspect', `docker://${source}`]).stdout);
  assert.equal(inspected.Os, 'linux');
  assert.equal(inspected.Architecture, 'amd64');
  return { indexDigest, amd64Digest: indexDigest };
}

function unpack(source, childDigest, directory) {
  const image = `${source.split('@')[0].split(':')[0]}@${childDigest}`;
  const layout = join(directory, 'oci');
  const bundle = join(directory, 'bundle');
  mkdirSync(directory, { recursive: true });
  command('skopeo', ['copy', `docker://${image}`, `oci:${layout}:candidate`]);
  command('umoci', ['unpack', '--rootless', '--image', `${layout}:candidate`, bundle]);
  return join(bundle, 'rootfs');
}

function elf(rootfs, path) {
  const target = resolve(rootfs, `.${path}`);
  const program = command('readelf', ['-l', target]).stdout;
  const dynamic = command('readelf', ['-d', target]).stdout;
  const interpreter = program.match(/Requesting program interpreter:\s*([^\]]+)/)?.[1];
  const needed = [...dynamic.matchAll(/\(NEEDED\).*\[([^\]]+)\]/g)].map(match => match[1]);
  required(interpreter && absolute(interpreter), 'ELF interpreter');
  required(needed.length, 'ELF dependencies');
  return { interpreter, needed };
}

function owner(rootfs, path) {
  const result = command('rpm', ['--root', rootfs, '-qf', '--qf', '%{NAME}', path], { allowFailure: true });
  return result.status === 0 ? result.stdout.trim() : '';
}

function inventory(rootfs, directories) {
  return directories.flatMap(dir => pathsUnder(join(rootfs, dir), `/${dir}`))
    .filter(path => regular(rootfs, path)).map(path => hashPath(rootfs, path));
}

function validate(evidence, sourceSha) {
  assert.deepEqual(Object.keys(evidence).sort(), ['applets', 'baseLicenses', 'donor', 'ownership', 'packages', 'rpmdb', 'runtime', 'scan', 'schema', 'sourceSha', 'status', 'toolVersions', 'trust']);
  assert.equal(evidence.schema, 1);
  assert.equal(evidence.sourceSha, sourceSha);
  assert.ok(['candidate', 'rejected'].includes(evidence.status));
  assert.equal(evidence.runtime.source, runtimeSource);
  required(digest(evidence.runtime.indexDigest), 'runtime index digest');
  required(digest(evidence.runtime.amd64Digest), 'runtime amd64 child digest');
  assert.equal(evidence.runtime.os, 'linux');
  assert.equal(evidence.runtime.architecture, 'amd64');
  required(evidence.runtime.osRelease?.ID && evidence.runtime.osRelease?.VERSION_ID, 'OS release');
  assert.equal(evidence.donor.source, donorSource);
  required(digest(evidence.donor.amd64Digest), 'donor amd64 digest');
  required(absolute(evidence.donor.interpreter), 'ELF interpreter');
  required(evidence.donor.needed?.length, 'ELF dependencies');
  for (const key of ['nativeFiles', 'licenses']) {
    required(evidence.donor[key]?.length, `donor ${key}`);
    for (const item of evidence.donor[key]) required(absolute(item.path) && hex(item.sha256), `donor ${key} hash`);
  }
  required(evidence.packages?.length, 'RPM package inventory');
  const rpmNames = new Set();
  for (const pkg of evidence.packages) {
    assert.equal(pkg.type, 'rpm');
    required(pkg.name && pkg.version, 'RPM package identity');
    rpmNames.add(pkg.name);
  }
  for (const key of ['rpmdb', 'trust', 'baseLicenses']) {
    required(evidence[key]?.length, key);
    for (const item of evidence[key]) required(absolute(item.path) && hex(item.sha256), `${key} path/hash`);
  }
  required(evidence.rpmdb.some(item => /\/(rpmdb\.sqlite|Packages)$/.test(item.path)), 'RPM database file');
  const baseNeeded = evidence.donor.needed.filter(name => !evidence.donor.nativeFiles.some(item => item.path.endsWith(`/${name}`)));
  for (const path of [evidence.donor.interpreter, ...baseNeeded.map(name => evidence.ownership.find(item => item.path.endsWith(`/${name}`))?.path)]) {
    const ownership = evidence.ownership.find(item => item.path === path);
    required(ownership && rpmNames.has(ownership.package), `package owner for ${path}`);
  }
  assert.equal(evidence.applets.bootstrapSourceSha256, bootstrapSourceSha256);
  required(evidence.applets.names?.length, 'BusyBox applets');
  assert.equal(evidence.scan.tool, 'grype');
  assert.equal(evidence.scan.failOn, 'high');
  required(evidence.scan.databaseBuilt, 'Grype database metadata');
  assert.ok(Array.isArray(evidence.scan.matches));
  assert.deepEqual(Object.keys(evidence.toolVersions ?? {}).sort(), ['grype', 'readelf', 'rpm', 'skopeo', 'syft', 'umoci']);
  for (const value of Object.values(evidence.toolVersions)) required(typeof value === 'string' && value.length > 0 && value.length < 160, 'tool version');
  if (evidence.scan.matches.some(match => ['High', 'Critical'].includes(match.severity))) assert.equal(evidence.status, 'rejected');
  if (evidence.status === 'candidate') assert.equal(evidence.scan.matches.filter(match => ['High', 'Critical'].includes(match.severity)).length, 0);
  return evidence;
}

function probe(output) {
  assert.equal(process.platform, 'linux', 'probe requires Linux');
  assert.equal(process.arch, 'x64', 'probe requires amd64');
  const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
  assert.match(sourceSha, /^[a-f0-9]{40}$/);
  assert.equal(fileSha(join(root, 'scripts/db-bootstrap-roles.sh')), bootstrapSourceSha256, 'bootstrap source changed; refresh applet inventory');
  const runtime = resolveImage(runtimeSource, true);
  const donor = resolveImage(donorSource, false);
  const scratch = mkdtempSync(join(tmpdir(), 'jandibat-runtime-probe-'));
  try {
    const runtimeRoot = unpack(runtimeSource, runtime.amd64Digest, join(scratch, 'runtime'));
    const donorRoot = unpack(donorSource, donor.amd64Digest, join(scratch, 'donor'));
    const baseSyft = join(scratch, 'base.syft.json');
    command('syft', [`dir:${runtimeRoot}`, '-o', `syft-json=${baseSyft}`]);
    const catalog = JSON.parse(readFileSync(baseSyft, 'utf8'));
    const scan = command('grype', [`sbom:${baseSyft}`, '--fail-on', 'high', '-o', 'json'], { allowFailure: true });
    const findings = JSON.parse(scan.stdout);
    const donorElf = elf(donorRoot, '/cockroach/cockroach');
    const runtimeFiles = pathsUnder(runtimeRoot);
    const ownership = [donorElf.interpreter, ...donorElf.needed.map(name => runtimeFiles.find(path => path.endsWith(`/${name}`)))]
      .filter(Boolean).map(path => ({ path, package: owner(runtimeRoot, path) }));
    const donorFiles = pathsUnder(donorRoot);
    const nativeFiles = ['/cockroach/cockroach', ...donorElf.needed.map(name => donorFiles.find(path => path.startsWith('/cockroach/') && path.endsWith(`/${name}`))).filter(Boolean)]
      .map(path => hashPath(donorRoot, path));
    const dbStatus = command('grype', ['db', 'status']).stdout;
    const databaseBuilt = dbStatus.match(/^Built:\s*(\S+)/m)?.[1] ?? '';
    const toolVersions = Object.fromEntries(['skopeo', 'syft', 'grype', 'umoci', 'readelf', 'rpm']
      .map(name => [name, command(name, ['--version']).stdout.trim().split('\n')[0].slice(0, 159)]));
    const evidence = {
      schema: 1, sourceSha, status: 'candidate',
      runtime: { source: runtimeSource, ...runtime, os: 'linux', architecture: 'amd64', osRelease: parseOsRelease(runtimeRoot) },
      donor: { source: donorSource, amd64Digest: donor.amd64Digest, ...donorElf, nativeFiles,
        licenses: inventory(donorRoot, ['cockroach/licenses', 'licenses', 'usr/share/licenses']) },
      packages: catalog.artifacts.filter(pkg => pkg.type === 'rpm').map(pkg => ({ name: pkg.name, version: pkg.version, type: 'rpm' })),
      rpmdb: inventory(runtimeRoot, ['usr/lib/sysimage/rpm', 'var/lib/rpm']), ownership,
      trust: ['/etc/ssl/certs/ca-certificates.crt', '/etc/pki/tls/certs/ca-bundle.crt', '/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem']
        .filter(path => regular(runtimeRoot, path)).map(path => hashPath(runtimeRoot, path)),
      baseLicenses: inventory(runtimeRoot, ['usr/share/licenses']),
      applets: { bootstrapSourceSha256, names: applets },
      scan: { tool: 'grype', failOn: 'high',
        matches: findings.matches.map(match => ({ id: match.vulnerability.id, severity: match.vulnerability.severity,
          package: match.artifact?.name ?? '', fixState: match.vulnerability.fix?.state ?? '' })),
        databaseBuilt }, toolVersions,
    };
    if (scan.status !== 0 || evidence.scan.matches.some(match => ['High', 'Critical'].includes(match.severity))) evidence.status = 'rejected';
    try { validate(evidence, sourceSha); } catch { evidence.status = 'rejected'; }
    writeFileSync(output, JSON.stringify(evidence, null, 2) + '\n', { mode: 0o644 });
    validate(evidence, sourceSha);
    if (evidence.status !== 'candidate') throw new Error('candidate rejected');
  } finally { rmSync(scratch, { recursive: true, force: true }); }
}

try {
  if (process.argv[2] === '--validate-evidence') {
    assert.equal(process.argv.length, 4);
    const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
    validate(JSON.parse(readFileSync(process.argv[3], 'utf8')), sourceSha);
  } else {
    assert.equal(process.argv.length, 3);
    probe(process.argv[2]);
  }
} catch (error) {
  if (process.argv[2] !== '--validate-evidence' && process.argv.length === 3 && !existsSync(process.argv[2])) {
    const sourceSha = command('git', ['rev-parse', 'HEAD']).stdout.trim();
    writeFileSync(process.argv[2], JSON.stringify({ schema: 1, sourceSha, status: 'rejected', phase: 'inspection' }) + '\n', { mode: 0o644 });
  }
  process.stderr.write(`restore runtime probe rejected: ${error.message?.replace(/[^A-Za-z0-9 _/.:,-]/g, '') ?? 'invalid evidence'}\n`);
  process.exitCode = 1;
}
