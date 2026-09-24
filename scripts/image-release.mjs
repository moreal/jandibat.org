// External commands are the only I/O boundary; archive hashes, image IDs and
// registry manifest digests are deliberately recorded as distinct identities.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';

const names = ['api', 'worker', 'maintenance', 'web', 'restore-tools'];
const hash = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const json = path => JSON.parse(readFileSync(path, 'utf8'));
function command(program, args, allowFailure = false) {
  const result = spawnSync(program, args, { encoding: 'utf8', maxBuffer: 128 * 1024 * 1024 });
  if (!allowFailure && result.status !== 0) throw new Error(`${program} failed: ${result.error || result.stderr}`);
  return result;
}
function save(path, value) { writeFileSync(path, JSON.stringify(value, null, 2) + '\n'); }

function scan(name, target, imageId, manifestDigest, evidence) {
  const identity = manifestDigest || imageId;
  const prefix = join(evidence, `${name}-${identity.replace(':', '-')}`);
  const receipt = { name, target, imageId, manifestDigest, sbom: prefix + '.spdx.json', syft: prefix + '.syft.json', grype: prefix + '.grype.json' };
  command('syft', [target, '-o', `spdx-json=${receipt.sbom}`, '-o', `syft-json=${receipt.syft}`]);
  const result = command('grype', [target, '--fail-on', 'high', '-o', 'json'], true);
  writeFileSync(receipt.grype, result.stdout || '');
  assert.equal(result.status, 0, `high/critical scan failed for ${target}: ${result.stderr}`);
  assert.match(json(receipt.sbom).spdxVersion, /^SPDX-/);
  const syftSource = json(receipt.syft).source?.metadata;
  const report = json(receipt.grype);
  for (const source of [syftSource, report.source?.target]) {
    assert.equal(source?.imageID, imageId, `scan image ID must equal imported archive: ${target}`);
    if (manifestDigest) assert.equal(source?.manifestDigest, manifestDigest, `scan manifest must equal deploy digest: ${target}`);
  }
  assert.ok(Array.isArray(report.matches), 'Grype must include match results');
  assert.ok(!report.matches.some(m => /^(high|critical)$/i.test(m.vulnerability?.severity)), 'high/critical finding');
  save(prefix + '.release.json', receipt);
  return receipt;
}

function importArchive(name, archive, evidence) {
  assert.ok(names.includes(name));
  mkdirSync(evidence, { recursive: true });
  if (name !== 'restore-tools') command('sh', ['scripts/test-image-contract.sh', '--archive', name, archive]);
  const manifest = JSON.parse(command('tar', ['-xOf', archive, 'manifest.json']).stdout);
  assert.equal(manifest.length, 1);
  const configBytes = command('tar', ['-xOf', archive, manifest[0].Config]).stdout;
  const config = JSON.parse(configBytes);
  const imageId = hash(configBytes);
  command('docker', ['load', '--input', archive]);
  const [loaded] = JSON.parse(command('docker', ['image', 'inspect', imageId]).stdout);
  assert.equal(loaded.Id, imageId);
  assert.equal(loaded.Architecture, 'amd64');
  assert.equal(loaded.Os, 'linux');
  assert.equal(loaded.Config.User ?? '', config.config.User ?? '');
  assert.deepEqual(loaded.Config.Entrypoint, config.config.Entrypoint);
  if (name !== 'restore-tools') assert.match(loaded.Config.User, /^[1-9][0-9]*:[1-9][0-9]*$/);
  save(join(evidence, `${name}-${imageId.replace(':', '-')}.inspect.json`), loaded);
  scan(name, `docker:${imageId}`, imageId, undefined, evidence);
  const layout = join(evidence, name);
  // Precompute the exact compressed v2 manifest before any registry mutation.
  command('skopeo', ['copy', '--format', 'v2s2', '--dest-compress', `docker-archive:${archive}`, `dir:${layout}`]);
  const candidate = json(join(layout, 'manifest.json'));
  assert.equal(candidate.config.digest, imageId);
  save(join(evidence, `${name}.json`), { name, archive, archiveHash: hash(readFileSync(archive)), imageId, layout });
}

function isCanonicalAbsence(diagnostic) {
  const response = diagnostic.trim();
  // Exact Distribution/OCI absence codes and their canonical messages only.
  // Unknown wrappers, details or wording need real registry evidence before
  // they can be added. Do not infer absence from a substring or denylist.
  for (const [code, message] of [
    ['NAME_UNKNOWN', 'repository name not known to registry'],
    ['MANIFEST_UNKNOWN', 'manifest unknown'],
  ]) {
    const renderedCode = code.toLowerCase().replace('_', ' ');
    if ([code, renderedCode, `${code}: ${message}`, `${renderedCode}: ${message}`].includes(response)) return true;
    // Match the entire one-error JSON envelope. A generic JSON.parse would
    // silently discard duplicate keys, hiding an ambiguous second error.
    // Only code and optional canonical message (in either order) are allowed.
    const codeField = String.raw`"code"\s*:\s*"${code}"`;
    const messageField = String.raw`"message"\s*:\s*"${message}"`;
    const fields = String.raw`${codeField}(?:\s*,\s*${messageField})?|${messageField}\s*,\s*${codeField}`;
    const envelope = new RegExp(String.raw`^\{\s*"errors"\s*:\s*\[\s*\{\s*(?:${fields})\s*\}\s*\]\s*\}$`);
    if (envelope.test(response)) return true;
  }
  return false;
}

function existingManifest(reference) {
  const result = command('skopeo', ['inspect', '--raw', `docker://${reference}`], true);
  if (result.status === 0) return hash(result.stdout);
  if (!result.error && !result.signal && isCanonicalAbsence(result.stderr || '')) return null;
  throw new Error(`cannot inspect immutable tag ${reference}: ${result.stderr || result.error}`);
}

function publish(evidence) {
  const { GITHUB_SHA: sha, GITHUB_REPOSITORY: repository, GITHUB_OUTPUT: output } = process.env;
  assert.match(sha || '', /^[0-9a-f]{40}$/, 'release tag must be the full Git SHA');
  assert.match(repository || '', /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/);
  assert.ok(output, 'GITHUB_OUTPUT required');
  const candidates = names.map(name => {
    const candidate = json(join(evidence, `${name}.json`));
    assert.equal(candidate.name, name);
    assert.match(candidate.imageId, /^sha256:[0-9a-f]{64}$/);
    const raw = readFileSync(join(candidate.layout, 'manifest.json'));
    assert.equal(JSON.parse(raw).config.digest, candidate.imageId);
    const manifestDigest = hash(raw);
    const image = `ghcr.io/${repository.toLowerCase()}/${name}`;
    const tag = `${image}:${sha}`;
    const existing = existingManifest(tag);
    assert.ok(existing === null || existing === manifestDigest, `immutable SHA tag mismatch: ${tag}`);
    return { ...candidate, manifestDigest, tag, existing, ref: `${image}@${manifestDigest}` };
  });
  // Workflow concurrency serializes writers. Check all tags before pushing any,
  // and check again at each write; existing SHA tags are never repointed.
  for (const candidate of candidates) {
    const current = existingManifest(candidate.tag);
    assert.ok(current === null || current === candidate.manifestDigest, `immutable SHA tag mismatch: ${candidate.tag}`);
    if (current === null) command('skopeo', ['copy', '--preserve-digests', `dir:${candidate.layout}`, `docker://${candidate.tag}`]);
    assert.equal(existingManifest(candidate.ref), candidate.manifestDigest, 'registry changed candidate manifest');
    scan(candidate.name, `registry:${candidate.ref}`, candidate.imageId, candidate.manifestDigest, evidence);
  }
  // Export nothing until every workload AND restore tool has passing evidence.
  appendFileSync(output, candidates.map(c => `${c.name.replace('-', '_')}_ref=${c.ref}\n`).join(''));
  save(join(evidence, 'release.json'), candidates.map(({ name, ref, imageId, manifestDigest }) => ({ name, ref, imageId, manifestDigest })));
}

try {
  const [operation, ...args] = process.argv.slice(2);
  if (operation === 'import' && args.length === 3) importArchive(args[0], resolve(args[1]), resolve(args[2]));
  else if (operation === 'publish' && args.length === 1) publish(resolve(args[0]));
  else throw new Error('usage: image-release.mjs import NAME ARCHIVE EVIDENCE | publish EVIDENCE');
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
