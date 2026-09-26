#!/bin/sh
set -eu
node --input-type=module <<'JS'
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, chmodSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';

const dir = mkdtempSync(join(tmpdir(), 'backup-connection-test-'));
const fake = join(dir, 'cockroach');
const stateFile = join(dir, 'state.json');
const script = 'scripts/db-bootstrap-backup-connection.sh';
const catalog = 'a'.repeat(64);
const access = 'ACCESS-SENTINEL-!';
const secret = 'SECRET-SENTINEL-☃&=+%';
const endpoint = 'https://storage.example.test:9009';
const base = {
  BACKUP_BOOTSTRAP_DATABASE_URL: 'postgresql://jandibat_backup_bootstrap@localhost/defaultdb',
  BACKUP_S3_ENDPOINT: endpoint,
  BACKUP_S3_REGION: 'us-east-1',
  BACKUP_S3_BUCKET: 'test-bucket',
  BACKUP_S3_PREFIX: 'snapshots/v1',
  BACKUP_S3_PATH_STYLE: 'true',
  BACKUP_S3_ACCESS_KEY_ID: access,
  BACKUP_S3_SECRET_ACCESS_KEY: secret,
};
const fakeSource = String.raw`#!/usr/bin/env node
const fs = require('fs');
const sql = fs.readFileSync(0, 'utf8');
const file = process.env.FAKE_STATE;
const s = JSON.parse(fs.readFileSync(file, 'utf8'));
s.calls.push({sql, argv: process.argv.slice(2)});
const save = () => fs.writeFileSync(file, JSON.stringify(s));
const bad = () => { save(); process.stderr.write(s.fault === 'leak_sql' ? 'SECRET-SENTINEL-☃&=+% SECRET-SENTINEL-%E2%98%83%26%3D%2B%25\n' : 'opaque SQL failure SQLSTATE: 40001\n'); process.exit(1); };
if (sql.includes('AS live_count') && sql.includes('AS policy_count')) {
  if (s.fault === 'classify_sql') bad();
  const stateHeader = 'actor\tlive_count\tpolicy_count\tpolicy_version\tinput_digest\tcatalog_digest\tlive_digest\n';
  process.stdout.write(s.fault === 'state_reordered' ? 'BEGIN\nSET\n' : 'SET\nBEGIN\n');
  process.stdout.write(stateHeader);
  if (s.fault === 'duplicate_rows') process.stdout.write('x\t0\t0\t\t\t\t\n');
  if (s.fault !== 'zero_rows') process.stdout.write((s.actor || 'jandibat_backup_bootstrap') + '\t' +
    (s.liveCount ?? (s.pair ? 1 : 0)) + '\t' + (s.policyCount ?? (s.pair ? 1 : 0)) + '\t' +
    (s.version ?? (s.pair ? '1' : '')) + '\t' + (s.pair?.input || s.absentInput || '') + '\t' +
    (s.pair?.catalog || s.absentCatalog || '') + '\t' + (s.liveDigest ?? (s.pair?.catalog || '')) + '\n');
  if (s.fault !== 'state_missing_commit') process.stdout.write('COMMIT\n');
  save(); process.exit(0);
}
if (sql.includes('CREATE EXTERNAL CONNECTION jandibat_backup_v1')) {
  if (s.pair || s.fault === 'before_insert' || s.fault === 'concurrent_conflict' || s.fault === 'leak_sql') bad();
  const match = sql.match(/VALUES \('jandibat_backup_v1', 1, '([0-9a-f]{64})'/);
  if (!match || !sql.includes('BEGIN;') || !sql.includes('COMMIT;') || !sql.includes('connection_live_digest')) bad();
  if (s.fault === 'before_commit') bad();
  s.pair = {input: match[1], catalog: '${catalog}'};
  if (s.fault === 'uncertain_commit') bad();
  save(); process.stdout.write(s.createOutput ?? ((sql.includes('SET autocommit_before_ddl = false') ? 'SET\nSET\n' : 'SET\n') + 'BEGIN\nCREATE EXTERNAL CONNECTION\nINSERT 0 1\nCOMMIT\n')); process.exit(0);
}
if (sql.includes('CHECK EXTERNAL CONNECTION')) {
  if (s.fault === 'check_sql') bad();
  const rows = s.checkRows || ['1\tlocal\ttrue\t\t1 MiB\t1 MiB/s\t1 MiB/s\ttrue'];
  process.stdout.write(s.checkHeader || 'node\tlocality\tok\terror\ttransferred\tread_speed\twrite_speed\tcan_delete');
  process.stdout.write('\n' + rows.join('\n') + (rows.length ? '\n' : ''));
  if (s.fault === 'drift_after_check') s.liveDigest = 'b'.repeat(64);
  save(); process.exit(0);
}
if (sql.includes('SHOW GRANTS ON EXTERNAL CONNECTION')) {
  if (s.fault === 'grant_read_sql') bad();
  process.stdout.write('grantee\tprivilege_type\tis_grantable\n');
  process.stdout.write('root\tALL\tfalse\n');
  process.stdout.write('jandibat_backup_bootstrap\tDROP\ttrue\n');
  process.stdout.write('jandibat_backup_bootstrap\tUSAGE\ttrue\n');
  if (s.grants?.runner) process.stdout.write('jandibat_backup_runner\tUSAGE\tfalse\n');
  if (s.grants?.verifier) process.stdout.write('jandibat_backup_verifier\tUSAGE\tfalse\n');
  if (s.fault === 'extra_grant') process.stdout.write('public\tUSAGE\tfalse\n');
  if (s.fault === 'duplicate_grant') process.stdout.write('root\tALL\tfalse\n');
  save(); process.exit(0);
}
if (sql.includes('GRANT USAGE ON EXTERNAL CONNECTION')) {
  if (s.fault === 'grant_sql') bad();
  if (!sql.includes('BEGIN;') || !sql.includes('COMMIT;')) bad();
  if (s.fault !== 'final_grant_missing') s.grants = {runner: true, verifier: true};
  if (s.fault === 'final_digest_drift') s.liveDigest = 'b'.repeat(64);
  save(); process.stdout.write(s.grantOutput ?? ((sql.includes('SET autocommit_before_ddl = false') ? 'SET\n' : '') + 'BEGIN\nGRANT\nCOMMIT\n')); process.exit(0);
}
bad();
`;
writeFileSync(fake, fakeSource, {mode: 0o700});
chmodSync(fake, 0o700);
const clean = () => ({calls: [], pair: null, grants: {runner:false, verifier:false}});
const save = s => writeFileSync(stateFile, JSON.stringify(s));
const load = () => JSON.parse(readFileSync(stateFile, 'utf8'));
const run = (overrides={}) => spawnSync('sh', [script], {
  env: {...process.env, ...base, ...overrides, COCKROACH_SQL_BIN: fake, FAKE_STATE: stateFile},
  encoding: 'utf8',
});
const assertPrivate = result => {
  const output = result.stdout + result.stderr;
  for (const sentinel of [access, secret, endpoint, 'ACCESS-SENTINEL-%21', 'SECRET-SENTINEL-%E2%98%83%26%3D%2B%25'])
    assert(!output.includes(sentinel), 'outward output leaked a synthetic input');
  const s = load();
  for (const call of s.calls) {
    const args = call.argv.join(' ');
    for (const sentinel of [access, secret, endpoint, 'ACCESS-SENTINEL-%21', 'SECRET-SENTINEL-%E2%98%83%26%3D%2B%25'])
      assert(!args.includes(sentinel), 'client argv leaked a synthetic input');
  }
};
const invoke = (s, overrides={}) => { save(s); const r=run(overrides); assertPrivate(r); return {r, s:load()}; };
const expectRefusal = (name, s, overrides={}, status=2) => {
  s=structuredClone(s); s.calls=[];
  const {r, s:after} = invoke(s, overrides);
  assert.equal(r.status, status, name);
  assert(!after.calls.some(c => /CREATE EXTERNAL CONNECTION|GRANT USAGE ON EXTERNAL CONNECTION/.test(c.sql)), name + ' mutated SQL');
  return after;
};
try {
  let result = invoke(clean());
  assert.equal(result.r.status, 0, 'fresh create');
  assert.equal(result.s.grants.runner, true);
  assert.equal(result.s.grants.verifier, true);
  const uri = 's3://test-bucket/snapshots/v1?AWS_ACCESS_KEY_ID=ACCESS-SENTINEL-%21&AWS_SECRET_ACCESS_KEY=SECRET-SENTINEL-%E2%98%83%26%3D%2B%25&AWS_ENDPOINT=https%3A%2F%2Fstorage.example.test%3A9009&AWS_REGION=us-east-1&AWS_USE_PATH_STYLE=true';
  const expected = createHash('sha256').update('jandibat-backup-policy-v1\n'+uri).digest('hex');
  assert.equal(result.s.pair.input, expected, 'canonical digest');
  assert(result.s.calls.find(c=>c.sql.includes('CREATE EXTERNAL CONNECTION')).sql.includes(uri), 'canonical URI');
  const longAccess = '0'.repeat(64);
  const longUri = 's3://test-bucket/snapshots/v1?AWS_ACCESS_KEY_ID=' + longAccess + '&AWS_SECRET_ACCESS_KEY=SECRET-SENTINEL-%E2%98%83%26%3D%2B%25&AWS_ENDPOINT=https%3A%2F%2Fstorage.example.test%3A9009&AWS_REGION=us-east-1&AWS_USE_PATH_STYLE=true';
  const longExpected = createHash('sha256').update('jandibat-backup-policy-v1\n' + longUri).digest('hex');
  const longResult = invoke(clean(), {BACKUP_S3_ACCESS_KEY_ID: longAccess});
  assert.equal(longResult.r.status, 0, 'long repeated-byte credential accepted');
  assert.equal(longResult.s.pair.input, longExpected, 'long repeated-byte credential digest');
  assert(longResult.s.calls.find(c=>c.sql.includes('CREATE EXTERNAL CONNECTION')).sql.includes(longUri), 'long repeated-byte credential URI');
  result = invoke(result.s);
  assert.equal(result.r.status, 0, 'same input rerun');
  assert.equal(result.s.calls.filter(c=>c.sql.includes('CREATE EXTERNAL CONNECTION')).length, 1, 'no duplicate create');
  for (const [name, key, value] of [
    ['endpoint','BACKUP_S3_ENDPOINT','https://other.example.test'],
    ['region','BACKUP_S3_REGION','eu-west-2'], ['bucket','BACKUP_S3_BUCKET','other-bucket'],
    ['prefix','BACKUP_S3_PREFIX','snapshots/v2'], ['path style','BACKUP_S3_PATH_STYLE','false'],
    ['access key','BACKUP_S3_ACCESS_KEY_ID','OTHER-ACCESS'], ['secret key','BACKUP_S3_SECRET_ACCESS_KEY','OTHER-SECRET'],
  ]) expectRefusal(name, result.s, {[key]:value});
  for (const [name,key,value] of [
    ['userinfo','BACKUP_S3_ENDPOINT','https://user@storage.example.test'],
    ['bad port','BACKUP_S3_ENDPOINT','https://storage.example.test:65536'],
    ['preencoded','BACKUP_S3_ENDPOINT','https://storage%2Eexample.test'],
    ['endpoint path','BACKUP_S3_ENDPOINT','https://storage.example.test/path'],
    ['endpoint query','BACKUP_S3_ENDPOINT','https://storage.example.test?q=1'],
    ['endpoint fragment','BACKUP_S3_ENDPOINT','https://storage.example.test#x'],
    ['prefix traversal','BACKUP_S3_PREFIX','snapshots/../other'],
    ['prefix empty segment','BACKUP_S3_PREFIX','snapshots//other'],
    ['control credential','BACKUP_S3_SECRET_ACCESS_KEY','line\nbreak'],
    ['control endpoint','BACKUP_S3_ENDPOINT','https://storage.example.test\t'],
  ]) expectRefusal(name, clean(), {[key]:value});
  for (const [name,mutate] of [
    ['missing live', s=>s.liveCount=0], ['missing metadata',s=>s.policyCount=0],
    ['duplicate live',s=>s.liveCount=2], ['duplicate result',s=>s.fault='duplicate_rows'],
    ['zero rows',s=>s.fault='zero_rows'], ['bad version',s=>s.version='2'],
    ['wrong identity',s=>s.actor='root'], ['live digest drift',s=>s.liveDigest='b'.repeat(64)],
    ['bad stored digest',s=>s.pair.input='bad'],
    ['reordered state acknowledgment',s=>s.fault='state_reordered'],
    ['missing state COMMIT',s=>s.fault='state_missing_commit'],
  ]) { const s=structuredClone(result.s); s.calls=[]; mutate(s); expectRefusal(name,s); }
  for (const [name,mutate] of [
    ['absent version',s=>s.version='1'],
    ['absent input digest',s=>s.absentInput='b'.repeat(64)],
    ['absent catalog digest',s=>s.absentCatalog='b'.repeat(64)],
    ['absent live digest',s=>s.liveDigest='b'.repeat(64)],
  ]) { const s=clean(); mutate(s); expectRefusal(name,s); }
  for (const [name,createOutput] of [
    ['missing CREATE acknowledgment','SET\nSET\nBEGIN\nINSERT 0 1\nCOMMIT\n'],
    ['missing INSERT acknowledgment','SET\nSET\nBEGIN\nCREATE EXTERNAL CONNECTION\nCOMMIT\n'],
    ['missing CREATE COMMIT','SET\nSET\nBEGIN\nCREATE EXTERNAL CONNECTION\nINSERT 0 1\n'],
    ['reordered CREATE acknowledgments','SET\nSET\nBEGIN\nINSERT 0 1\nCREATE EXTERNAL CONNECTION\nCOMMIT\n'],
    ['empty CREATE stdout',''],
  ]) { const {r,s}=invoke({...clean(),createOutput}); assert.equal(r.status,1,name); assert(!s.calls.some(c=>c.sql.includes('CHECK EXTERNAL CONNECTION')),name+' advanced to CHECK'); }
  for (const fault of ['before_insert','before_commit','concurrent_conflict','uncertain_commit','leak_sql']) {
    const {r,s} = invoke({...clean(),fault});
    assert.equal(r.status,1,fault);
    assert.equal(!!s.pair, fault==='uncertain_commit',fault+' atomicity');
    if (fault==='uncertain_commit') { s.fault=''; const retry=invoke(s); assert.equal(retry.r.status,0,'uncertain commit explicit rerun'); }
  }
  for (const [name,checkRows,checkHeader] of [
    ['empty check',[],undefined], ['failed node',['1\tlocal\tfalse\tfail\t0\t0\t0\tfalse'],undefined],
    ['unknown columns',undefined,'node\tok'],
  ]) { const s=structuredClone(result.s); s.calls=[]; s.grants={runner:false,verifier:false}; s.checkRows=checkRows; s.checkHeader=checkHeader; const {r,s:after}=invoke(s); assert.equal(r.status,1,name); assert(!after.calls.some(c=>c.sql.includes('GRANT USAGE ON EXTERNAL CONNECTION')),name+' granted'); }
  for (const fault of ['extra_grant','duplicate_grant','grant_sql','grant_read_sql']) {
    const s=structuredClone(result.s); s.calls=[]; s.grants={runner:false,verifier:false}; s.fault=fault;
    const {r}=invoke(s); assert.equal(r.status, fault==='extra_grant'||fault==='duplicate_grant' ? 2 : 1, fault);
  }
  for (const [name,grantOutput] of [
    ['missing GRANT acknowledgment','SET\nBEGIN\nCOMMIT\n'],
    ['missing GRANT COMMIT','SET\nBEGIN\nGRANT\n'],
    ['reordered GRANT acknowledgments','SET\nGRANT\nBEGIN\nCOMMIT\n'],
    ['empty GRANT stdout',''],
  ]) { const s=structuredClone(result.s); s.calls=[]; s.grants={runner:false,verifier:false}; s.grantOutput=grantOutput;
    const {r}=invoke(s); assert.equal(r.status,1,name); }
  for (const [fault,status] of [['drift_after_check',2],['final_digest_drift',2],['final_grant_missing',2]]) {
    const s=structuredClone(result.s); s.calls=[]; s.grants={runner:false,verifier:false}; s.fault=fault;
    const {r,s:after}=invoke(s); assert.equal(r.status,status,fault);
    if (fault==='drift_after_check') assert(!after.calls.some(c=>c.sql.includes('GRANT USAGE ON EXTERNAL CONNECTION')), 'drift after CHECK granted');
  }
  { const s=structuredClone(result.s); s.calls=[]; s.grants={runner:true,verifier:false};
    const {r, s:after}=invoke(s); assert.equal(r.status,0,'partial grants resume');
    assert.equal(after.grants.verifier,true,'partial grants completed'); }
  assert(!result.s.calls.some(c=>/\b(?:ALTER|DROP|UPSERT|UPDATE)\b/.test(c.sql)), 'forbidden mutation SQL');
  console.log('backup connection fake state-machine checks passed');
} finally { rmSync(dir,{recursive:true,force:true}); }
JS
