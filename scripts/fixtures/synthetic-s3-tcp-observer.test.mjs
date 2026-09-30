import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, statSync } from 'node:fs';
import { createServer, connect } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

const observer = 'scripts/fixtures/synthetic-s3-tcp-observer.mjs';
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
async function listen(server) {
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  return server.address().port;
}
async function freePort() {
  const server = createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  return port;
}
async function waitReady(child, counter, getOutput) {
  for (let i = 0; i < 100; i++) {
    if (child.exitCode !== null) throw new Error('observer exited early');
    try { if (readFileSync(counter, 'utf8') === '0 0 0\n' && getOutput() === 'READY\n') return; } catch { /* startup */ }
    await delay(10);
  }
  throw new Error('observer did not create a counter');
}
async function stop(child) {
  if (child.exitCode !== null) return;
  const closed = new Promise((resolve) => child.once('exit', resolve));
  child.kill('SIGTERM');
  await closed;
}

test('opaque observer forwards both directions and counts only accepted TCP connections', async () => {
  const dir = mkdtempSync(join(tmpdir(), 's3-tcp-observer-'));
  const upstream = createServer((socket) => {
    socket.on('data', (bytes) => socket.write(Buffer.concat([Buffer.from('echo:'), bytes])));
    socket.on('end', () => socket.end());
  });
  const upstreamPort = await listen(upstream);
  const listenPort = await freePort();
  const counter = join(dir, 'counter');
  let child;
  try {
    child = spawn(process.execPath, [observer, counter, String(listenPort), String(upstreamPort)], { stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    child.stdout.on('data', (bytes) => { output += bytes; });
    child.stderr.on('data', (bytes) => { output += bytes; });
    await waitReady(child, counter, () => output);
    const payload = 'private request credential and path';
    const response = await new Promise((resolve, reject) => {
      const socket = connect(listenPort, '127.0.0.1');
      socket.once('error', reject);
      socket.once('connect', () => socket.write(payload));
      socket.once('data', (bytes) => { socket.end(); resolve(bytes.toString('utf8')); });
    });
    assert.equal(response, `echo:${payload}`);
    assert.equal(readFileSync(counter, 'utf8'), `${1} ${Buffer.byteLength(payload)} ${Buffer.byteLength(response)}\n`);
    assert.equal(statSync(counter).mode & 0o777, 0o600);
    assert.doesNotMatch(output, /private|credential|path/);
    assert.match(output, /^READY\n$/);
  } finally {
    if (child) await stop(child);
    await new Promise((resolve) => upstream.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});

test('invalid invocation emits no argument, path or credentials', async () => {
  const dir = mkdtempSync(join(tmpdir(), 's3-tcp-invalid-'));
  try {
    const child = spawn(process.execPath, [observer, join(dir, 'private-credential'), 'bad-port', '9010'], { stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    child.stdout.on('data', (bytes) => { output += bytes; });
    child.stderr.on('data', (bytes) => { output += bytes; });
    const exitCode = await new Promise((resolve) => child.once('exit', resolve));
    assert.notEqual(exitCode, 0);
    assert.doesNotMatch(output, /private|credential|s3-tcp-invalid-/);
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('counter write failure fatally stops observer and existing opaque streams', async () => {
  const dir = mkdtempSync(join(tmpdir(), 's3-tcp-counter-failure-'));
  const upstream = createServer((socket) => { socket.on('error', () => {}); });
  const upstreamPort = await listen(upstream);
  const listenPort = await freePort();
  const counter = join(dir, 'counter');
  let child;
  let first;
  try {
    child = spawn(process.execPath, [observer, counter, String(listenPort), String(upstreamPort)], { stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    child.stdout.on('data', (bytes) => { output += bytes; });
    child.stderr.on('data', (bytes) => { output += bytes; });
    await waitReady(child, counter, () => output);
    first = connect(listenPort, '127.0.0.1');
    first.on('error', () => {});
    await new Promise((resolve) => first.once('connect', resolve));
    for (let i = 0; i < 100 && readFileSync(counter, 'utf8') !== '1 0 0\n'; i++) await delay(10);
    assert.equal(readFileSync(counter, 'utf8'), '1 0 0\n');
    mkdirSync(`${counter}.tmp.${child.pid}`);
    const exited = new Promise((resolve) => child.once('exit', resolve));
    const second = connect(listenPort, '127.0.0.1');
    second.on('error', () => {});
    const exitCode = await Promise.race([exited, delay(700).then(() => { throw new Error('observer remained alive with stale counter'); })]);
    second.destroy();
    assert.equal(exitCode, 1);
    assert.equal(first.destroyed, true);
    assert.doesNotMatch(output, /private|credential|path|counter-failure/);
  } finally {
    if (first) first.destroy();
    if (child) await stop(child);
    await new Promise((resolve) => upstream.close(resolve));
    rmSync(dir, { recursive: true, force: true });
  }
});
