import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { once } from "node:events";
import test from "node:test";

async function withServer(handler, run) {
  const requests = [];
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    requests.push({ method: request.method, path: request.url, headers: request.headers, body });
    handler(request, response, body);
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  try {
    return await run(`http://127.0.0.1:${server.address().port}`, requests);
  } finally {
    server.close();
    await once(server, "close");
  }
}

async function runScript(file, env) {
  const child = spawn(process.execPath, [new URL(file, import.meta.url).pathname], { env: { ...process.env, ...env }, stdio: ["ignore", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += chunk; });
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  const [code] = await once(child, "exit");
  assert.equal(stderr, "");
  return { code, report: JSON.parse(stdout) };
}

test("load checker sends ActivitySnapshot GraphQL POST and rejects GraphQL errors", async () => {
  await withServer((request, response) => {
    response.setHeader("Content-Type", request.url === "/v1/render/octocat.svg" ? "image/svg+xml" : "application/json");
    response.end(request.url === "/graphql" ? JSON.stringify({ errors: [{ message: "fixture failure" }], data: { subject: null } }) : "{}");
  }, async (baseURL, requests) => {
    const { code, report } = await runScript("load-check.mjs", {
      LOAD_BASE_URL: baseURL, LOAD_REQUESTS: "3", LOAD_CONCURRENCY: "1", LOAD_MAX_ERROR_RATE: "0",
    });
    assert.equal(code, 1);
    assert.equal(report.result, "failed");
    assert.equal(report.failures[0]?.endpoint, "activities");
    const activity = requests.find((request) => request.path === "/graphql");
    assert.ok(activity);
    assert.equal(activity.method, "POST");
    assert.equal(activity.headers["content-type"], "application/json");
    const payload = JSON.parse(activity.body);
    assert.equal(payload.variables.subject, "octocat");
    assert.match(payload.query, /activitySnapshot/);
  });
});

test("security smoke checks GraphQL query privacy and hostile mutation CSRF", async () => {
  await withServer((request, response, body) => {
    response.setHeader("X-Content-Type-Options", "nosniff");
    response.setHeader("Cache-Control", "private, no-store");
    response.setHeader("Content-Type", "application/json");
    if (request.method === "OPTIONS") {
      if (request.headers.origin === "https://app.example.test" && request.url === "/graphql" && request.headers["access-control-request-method"] === "POST") {
        response.setHeader("Access-Control-Allow-Origin", "https://app.example.test");
        response.setHeader("Access-Control-Allow-Credentials", "true");
        response.setHeader("Access-Control-Allow-Methods", "POST");
        response.setHeader("Access-Control-Allow-Headers", "Authorization,Content-Type,Idempotency-Key,X-Jandibat-Provider-Key");
      }
      response.statusCode = request.url === "/graphql" && request.headers["access-control-request-method"] === "POST" ? 204 : 404; response.end(); return;
    }
    if (request.url === "/graphql" && request.headers.origin === "https://not-allowlisted.invalid") {
      response.statusCode = 403; response.end('{"code":"csrf"}'); return;
    }
    if (request.url === "/graphql" && body.length > 1_000_000) {
      response.statusCode = 413; response.end("{}"); return;
    }
    if (request.url === "/graphql") {
      response.end('{"data":{"subject":{"handle":"octocat","activitySnapshot":{"revision":"fixture","generatedAt":"2026-08-13T00:00:00Z","dataUpdatedAt":null}}}}');
      return;
    }
    if (request.url === "/v1/render/octocat.svg") {
      response.setHeader("Content-Type", "image/svg+xml"); response.setHeader("Content-Security-Policy", "default-src 'none'"); response.end("<svg xmlns=\"http://www.w3.org/2000/svg\"/>"); return;
    }
    if (request.url === "/healthz") { response.end("{}"); return; }
    response.statusCode = 404; response.end("{}");
  }, async (baseURL, requests) => {
    const { code, report } = await runScript("security-smoke.mjs", { SECURITY_BASE_URL: baseURL, SECURITY_SUBJECT: "octocat", SECURITY_ALLOWED_ORIGIN: "https://app.example.test" });
    assert.equal(code, 0, JSON.stringify(report.checks.filter((check) => check.result === "failed")));
    assert.equal(report.checks.find((check) => check.name === "public activity cache boundary")?.result, "passed");
    assert.equal(report.checks.find((check) => check.name === "cookie mutation rejects hostile Origin with CSRF")?.result, "passed");
    assert.equal(report.checks.find((check) => check.name === "removed REST domain preflight is not granted")?.result, "passed");
    assert.ok(requests.some((request) => request.path === "/v1/subjects" && request.method === "OPTIONS" && request.headers.origin === "https://app.example.test"));
    assert.ok(requests.some((request) => request.path === "/graphql" && request.method === "POST" && JSON.parse(request.body).query.includes("activitySnapshot")));
    assert.ok(requests.some((request) => request.path === "/graphql" && request.method === "POST" && request.headers.origin === "https://not-allowlisted.invalid" && JSON.parse(request.body).query.includes("signOut")));
    assert.ok(!requests.some((request) => request.path.startsWith("/v1/activities/") || request.path.startsWith("/v1/subjects/") || request.path === "/v1/auth/sessions" || request.path === "/v1/auth/magic-link/request"));
  });
});

test("private GraphQL probes are not run without a configured allowed Origin", async () => {
  await withServer((request, response, body) => {
    response.setHeader("X-Content-Type-Options", "nosniff");
    response.setHeader("Cache-Control", "private, no-store");
    response.setHeader("Content-Type", "application/json");
    if (request.method === "OPTIONS") { response.statusCode = 204; response.end(); return; }
    if (request.url === "/graphql" && request.headers.origin === "https://not-allowlisted.invalid") {
      response.statusCode = 403; response.end('{"code":"csrf"}'); return;
    }
    if (request.url === "/graphql" && body.length > 1_000_000) {
      response.statusCode = 413; response.end("{}"); return;
    }
    if (request.url === "/graphql") { response.end('{"data":{"subject":{"handle":"octocat","activitySnapshot":{"revision":"fixture"}}}}'); return; }
    if (request.url === "/v1/render/octocat.svg") {
      response.setHeader("Content-Type", "image/svg+xml"); response.setHeader("Content-Security-Policy", "default-src 'none'"); response.end("<svg/>"); return;
    }
    response.end("{}");
  }, async (baseURL, requests) => {
    const { report } = await runScript("security-smoke.mjs", {
      SECURITY_BASE_URL: baseURL, SECURITY_PRIVATE_SUBJECT: "hidden", SECURITY_OWNER_SESSION_COOKIE: "owner-cookie", SECURITY_NONOWNER_SESSION_COOKIE: "other-cookie",
    });
    for (const name of ["private owner response is never publicly cacheable", "anonymous caller cannot read private subject", "non-owner cannot read private subject"]) {
      assert.equal(report.checks.find((check) => check.name === name)?.result, "not-run");
    }
    assert.ok(!requests.some((request) => request.path === "/graphql" && request.body.includes('"subject":"hidden"')));
  });
});
