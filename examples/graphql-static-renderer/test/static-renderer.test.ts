import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, unlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { renderToFiles } from "../src/index.ts";

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    subject: { handle: "alice", displayName: "Alice" },
    range: { from: "2026-01-01", to: "2026-01-02" },
    days: [
      { date: "2026-01-01", count: "2", level: 1 },
      { date: "2026-01-02", count: "0", level: 0 },
    ],
    total: "2",
    longestStreak: 1,
    generatedAt: "2026-01-03T00:00:00Z",
    dataUpdatedAt: "2026-01-02T23:00:00Z",
    revision: "sha256:one",
    ...overrides,
  };
}

function fakeFetch(responses: Array<{ status?: number; body: unknown }>) {
  const requests: Array<{ url: string; init: RequestInit; body: { query: string; variables: unknown } }> = [];
  const fetcher: typeof fetch = async (url, init) => {
    requests.push({ url: String(url), init: init ?? {}, body: JSON.parse(String(init?.body)) });
    const next = responses.shift();
    assert.ok(next, "unexpected additional GraphQL request");
    return new Response(JSON.stringify(next.body), {
      status: next.status ?? 200,
      headers: { "content-type": "application/json" },
    });
  };
  return { fetcher, requests };
}

function options(output: string) {
  return {
    endpoint: "https://api.example.com/graphql",
    subject: "alice",
    from: "2026-01-01",
    to: "2026-01-02",
    timezone: "Asia/Seoul",
    environmentIDs: ["github", "custom:work"],
    output,
  };
}

test("one GraphQL query writes visible provenance and sidecar metadata", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([{ body: { data: { subject: { activitySnapshot: snapshot() } } } }]);

  const result = await renderToFiles(options(output), fake.fetcher);
  const html = await readFile(output, "utf8");
  const metadata = JSON.parse(await readFile(`${output}.json`, "utf8"));
  assert.equal(result.status, "written");
  assert.equal(result.generatedAt, "2026-01-03T00:00:00Z");
  assert.match(html, /2026-01-03T00:00:00Z/);
  assert.match(html, /2026-01-02T23:00:00Z/);
  assert.match(html, /2026-01-01/);
  assert.deepEqual(metadata, {
    generatedAt: "2026-01-03T00:00:00Z",
    dataUpdatedAt: "2026-01-02T23:00:00Z",
    revision: "sha256:one",
    renderedSubjectLabel: "Alice",
  });
  assert.equal(fake.requests.length, 1);
  const request = fake.requests[0]!;
  assert.equal(request.url, "https://api.example.com/graphql");
  assert.equal(request.init.method, "POST");
  assert.match(request.body.query, /activitySnapshot/);
  assert.match(request.body.query, /generatedAt\s+dataUpdatedAt\s+revision/);
  assert.deepEqual(request.body.variables, {
    subject: "alice",
    range: { from: "2026-01-01", to: "2026-01-02" },
    timezone: "Asia/Seoul",
    environmentIDs: ["github", "custom:work"],
  });
});

test("unchanged revision skips artifact replacement even when generatedAt advances", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
    { body: { data: { subject: { activitySnapshot: snapshot({ generatedAt: "2026-01-04T00:00:00Z" }) } } } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  const firstHtml = await readFile(output, "utf8");
  const firstSidecar = await readFile(`${output}.json`, "utf8");

  const result = await renderToFiles(options(output), fake.fetcher);
  assert.equal(result.status, "unchanged revision");
  assert.equal(result.generatedAt, "2026-01-04T00:00:00Z");
  assert.equal(await readFile(output, "utf8"), firstHtml);
  assert.equal(await readFile(`${output}.json`, "utf8"), firstSidecar);
});

test("nullable dataUpdatedAt remains null in metadata and is omitted from visible page", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([{ body: { data: { subject: { activitySnapshot: snapshot({ dataUpdatedAt: null, total: "0" }) } } } }]);
  await renderToFiles(options(output), fake.fetcher);
  const html = await readFile(output, "utf8");
  const metadata = JSON.parse(await readFile(`${output}.json`, "utf8"));
  assert.equal(metadata.dataUpdatedAt, null);
  assert.match(html, /Generated at/);
  assert.doesNotMatch(html, /Data updated at/);
});

test("GraphQL errors leave an existing artifact and sidecar untouched", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
    { body: { errors: [{ message: "private data" }], data: { subject: null } } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  const firstHtml = await readFile(output, "utf8");
  const firstSidecar = await readFile(`${output}.json`, "utf8");
  await assert.rejects(() => renderToFiles(options(output), fake.fetcher), /GraphQL request failed/);
  assert.equal(await readFile(output, "utf8"), firstHtml);
  assert.equal(await readFile(`${output}.json`, "utf8"), firstSidecar);
});

test("missing HTML is rebuilt even when its prior sidecar has the same revision", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  await unlink(output);
  const result = await renderToFiles(options(output), fake.fetcher);
  assert.equal(result.status, "written");
  assert.match(await readFile(output, "utf8"), /Alice activity/);
});

test("HTML escapes untrusted subject text and GraphQL HTTP failures do not write files", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot({ subject: { handle: "alice", displayName: "<script>alert(1)</script>" } }) } } } },
    { status: 503, body: { error: "offline" } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  assert.match(await readFile(output, "utf8"), /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  await unlink(output);
  await unlink(`${output}.json`);
  await assert.rejects(() => renderToFiles(options(output), fake.fetcher), /GraphQL HTTP request failed \(503\)/);
  await assert.rejects(() => readFile(output, "utf8"), { code: "ENOENT" });
});

test("malformed numeric snapshot fields cannot inject HTML or replace an existing artifact", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const malicious = snapshot({
    revision: "sha256:two",
    longestStreak: "</p><script>alert(1)</script>",
    days: [{ date: "2026-01-01", count: "2", level: "</td><script>alert(1)</script>" }],
  });
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
    { body: { data: { subject: { activitySnapshot: malicious } } } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  const priorHtml = await readFile(output, "utf8");
  const priorMetadata = await readFile(`${output}.json`, "utf8");
  await assert.rejects(() => renderToFiles(options(output), fake.fetcher), /valid snapshot/);
  assert.equal(await readFile(output, "utf8"), priorHtml);
  assert.equal(await readFile(`${output}.json`, "utf8"), priorMetadata);
});

test("same revision with a changed display name replaces the visible artifact", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "jandibat-static-"));
  t.after(() => rm(directory, { recursive: true }));
  const output = join(directory, "activity.html");
  const fake = fakeFetch([
    { body: { data: { subject: { activitySnapshot: snapshot() } } } },
    { body: { data: { subject: { activitySnapshot: snapshot({ subject: { handle: "alice", displayName: "Alicia" } }) } } } },
  ]);
  await renderToFiles(options(output), fake.fetcher);
  const result = await renderToFiles(options(output), fake.fetcher);
  assert.equal(result.status, "written");
  assert.match(await readFile(output, "utf8"), /Alicia activity/);
  const metadata = JSON.parse(await readFile(`${output}.json`, "utf8"));
  assert.equal(metadata.renderedSubjectLabel, "Alicia");
});
