#!/usr/bin/env node
// Disposable fixture observer: opaque TCP relay, never a TLS terminator.
import { writeFileSync, renameSync, unlinkSync } from 'node:fs';
import { createServer, connect } from 'node:net';

const [counterPath, listenText, upstreamText] = process.argv.slice(2);
const port = (text) => /^(?:[1-9][0-9]{0,4})$/.test(text ?? '') && Number(text) <= 65535 ? Number(text) : null;
const listenPort = port(listenText);
const upstreamPort = port(upstreamText);
if (process.argv.length !== 5 || !counterPath || listenPort === null || upstreamPort === null) {
  process.exit(2);
}

let accepted = 0;
let sent = 0;
let received = 0;
const sockets = new Set();
function saveCount() {
  if ([accepted, sent, received].some((value) => !Number.isSafeInteger(value) || value > 999999999999)) throw new Error('counter overflow');
  const temporary = `${counterPath}.tmp.${process.pid}`;
  writeFileSync(temporary, `${accepted} ${sent} ${received}\n`, { flag: 'wx', mode: 0o600 });
  renameSync(temporary, counterPath);
}

try { saveCount(); } catch { process.exit(1); }
function stopOnCounterFailure() {
  try { unlinkSync(counterPath); } catch { /* liveness check also fails closed */ }
  for (const socket of sockets) socket.destroy();
  server.close();
  process.exit(1);
}
const server = createServer((client) => {
  accepted += 1;
  try { saveCount(); } catch { client.destroy(); stopOnCounterFailure(); }
  const upstream = connect(upstreamPort, '127.0.0.1');
  sockets.add(client);
  sockets.add(upstream);
  const closePair = () => { client.destroy(); upstream.destroy(); };
  client.on('error', closePair);
  upstream.on('error', closePair);
  client.on('close', () => sockets.delete(client));
  upstream.on('close', () => sockets.delete(upstream));
  client.on('data', (bytes) => { sent += bytes.length; try { saveCount(); } catch { stopOnCounterFailure(); } });
  upstream.on('data', (bytes) => { received += bytes.length; try { saveCount(); } catch { stopOnCounterFailure(); } });
  client.pipe(upstream);
  upstream.pipe(client);
});
server.on('error', () => { process.exitCode = 1; });
server.listen(listenPort, '127.0.0.1', () => { process.stdout.write('READY\n'); });
process.on('SIGTERM', () => {
  for (const socket of sockets) socket.destroy();
  server.close();
});
