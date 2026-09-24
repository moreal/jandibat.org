import { randomBytes } from "node:crypto";
import { writeFile } from "node:fs/promises";

const baseURL = new URL(process.env.SECURITY_BASE_URL ?? "http://127.0.0.1:8080");
const subject = process.env.SECURITY_SUBJECT ?? "octocat";
const privateSubject = process.env.SECURITY_PRIVATE_SUBJECT ?? "";
const allowedOrigin = process.env.SECURITY_ALLOWED_ORIGIN ?? "";
const ownerCookie = normalizeCookie(process.env.SECURITY_OWNER_SESSION_COOKIE ?? "");
const nonOwnerCookie = normalizeCookie(process.env.SECURITY_NONOWNER_SESSION_COOKIE ?? "");
const isolated = process.env.SECURITY_ISOLATED_FIXTURE === "true";
const requireExtended = process.env.SECURITY_REQUIRE_EXTENDED === "true";
const outputFile = process.env.SECURITY_OUTPUT_FILE;
const timeoutMs = positiveInteger("SECURITY_TIMEOUT_MS", 5000);
const hostileOrigin = "https://not-allowlisted.invalid";
const checks = [];
const startedAt = new Date();
const activityDay = startedAt.toISOString().slice(0, 10);
const snapshotQuery = "query SecurityActivitySnapshot($subject: String!, $range: DateRangeInput!, $timezone: TimeZone!) { subject(handleOrID: $subject) { handle activitySnapshot(range: $range, timezone: $timezone) { revision generatedAt dataUpdatedAt } } }";
const subjectQuery = "query SecuritySubject($subject: String!) { subject(handleOrID: $subject) { handle } }";

await check("health security headers", "/healthz", {}, async (response, body) => {
  expect(response.ok, `status ${response.status}`);
  commonHeaders(response);
  expect(!containsSecretShape(body), "response resembles a secret");
});

await check("hostile CORS origin is not reflected", "/healthz", { headers: { Origin: hostileOrigin } }, async (response) => {
  expect(response.headers.get("access-control-allow-origin") !== hostileOrigin, "untrusted origin was reflected");
});

await check("hostile preflight is not allowed", "/graphql", {
  method: "OPTIONS",
  headers: { Origin: hostileOrigin, "Access-Control-Request-Method": "DELETE", "Access-Control-Request-Headers": "Authorization,X-Unlisted" },
}, async (response) => {
  expect(response.headers.get("access-control-allow-origin") !== hostileOrigin, "hostile preflight origin was reflected");
});

if (allowedOrigin) {
  await check("allowlisted preflight exposes only the documented matrix", "/graphql", {
    method: "OPTIONS",
    headers: { Origin: allowedOrigin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "Authorization,Content-Type" },
  }, async (response) => {
    expect(response.status === 204, `status ${response.status}`);
    expect(response.headers.get("access-control-allow-origin") === allowedOrigin, "allowlisted origin was not returned exactly");
    expect(response.headers.get("access-control-allow-methods") === "POST", "unexpected method allowlist");
    expect(response.headers.get("access-control-allow-headers") === "Authorization,Content-Type,Idempotency-Key,X-Jandibat-Provider-Key", "unexpected header allowlist");
    expect(response.headers.get("access-control-allow-credentials") === "true", "credentialed preflight is disabled");
  });
  await check("removed REST domain preflight is not granted", "/v1/subjects", {
    method: "OPTIONS",
    headers: { Origin: allowedOrigin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "Content-Type" },
  }, async (response) => {
    expect(response.status !== 204, "removed REST path returned a successful preflight");
    expect(response.headers.get("access-control-allow-origin") !== allowedOrigin, "removed REST path received a CORS grant");
  });
} else {
  notRun("allowlisted preflight exposes only the documented matrix", "SECURITY_ALLOWED_ORIGIN is missing");
  notRun("removed REST domain preflight is not granted", "SECURITY_ALLOWED_ORIGIN is missing");
}

await check("cookie mutation rejects hostile Origin with CSRF", "/graphql", {
  method: "POST",
  headers: { Origin: hostileOrigin, Cookie: "jandibat_session=invalid-csrf-fixture", "Content-Type": "application/json" },
  body: JSON.stringify({ query: "mutation SecurityCSRFProbe { signOut { errors { code } } }", operationName: "SecurityCSRFProbe" }),
}, async (response, body) => {
  expect(response.status === 403, `status ${response.status}`);
  expect(problemCode(body) === "csrf", "response is not the stable csrf problem");
});

await check("oversized request body is rejected before authentication", "/graphql", {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ query: "mutation SecurityOversize($email: String!) { requestMagicLink(input: {email: $email}) { accepted } }", operationName: "SecurityOversize", variables: { email: `${"a".repeat(1024 * 1024)}@example.invalid` } }),
}, async (response) => expect(response.status === 413, `status ${response.status}`), 15000);

await check("public activity cache boundary", "/graphql", graphQLRequest(snapshotQuery, "SecurityActivitySnapshot", { subject, range: { from: activityDay, to: activityDay }, timezone: "UTC" }), async (response, body) => {
  expect(response.ok, `status ${response.status}`);
  commonHeaders(response);
  expect(response.headers.get("content-type")?.startsWith("application/json"), "unexpected content type");
  expectPrivateCache(response);
  const payload = parseGraphQL(body);
  expect(payload.data?.subject?.handle === subject && Boolean(payload.data.subject.activitySnapshot?.revision), "public activity snapshot is unavailable");
  expect(!containsSecretShape(body), "response resembles a secret");
});

await check("self-contained SVG", `/v1/render/${encodeURIComponent(subject)}.svg`, {}, async (response, body) => {
  expect(response.ok, `status ${response.status}`);
  commonHeaders(response);
  expect(response.headers.get("content-type")?.startsWith("image/svg+xml"), "unexpected SVG content type");
  expect(Boolean(response.headers.get("content-security-policy")), "missing Content-Security-Policy");
  for (const pattern of [/<script\b/iu, /\son[a-z]+\s*=/iu, /(?:href|src)\s*=\s*["'](?:https?:)?\/\//iu, /javascript\s*:/iu]) expect(!pattern.test(body), `unsafe SVG pattern ${pattern}`);
});

if (privateSubject && ownerCookie && nonOwnerCookie && allowedOrigin) {
  const path = "/graphql";
  const privateRequest = graphQLRequest(subjectQuery, "SecuritySubject", { subject: privateSubject });
  await check("private owner response is never publicly cacheable", path, { ...privateRequest, headers: { ...privateRequest.headers, Cookie: ownerCookie, Origin: allowedOrigin || undefined } }, async (response, body) => {
    expect(response.ok, `status ${response.status}`);
    expectPrivateCache(response);
    expect(parseGraphQL(body).data?.subject?.handle === privateSubject, "owner cannot read private subject");
  });
  await check("anonymous caller cannot read private subject", path, privateRequest, async (response, body) => {
    expect(response.ok, `status ${response.status}`);
    expectPrivateCache(response);
    expect(parseGraphQL(body).data?.subject == null, "anonymous caller read private subject");
  });
  await check("non-owner cannot read private subject", path, { ...privateRequest, headers: { ...privateRequest.headers, Cookie: nonOwnerCookie, Origin: allowedOrigin || undefined } }, async (response, body) => {
    expect(response.ok, `status ${response.status}`);
    expectPrivateCache(response);
    expect(parseGraphQL(body).data?.subject == null, "non-owner read private subject");
  });
} else {
  for (const name of ["private owner response is never publicly cacheable", "anonymous caller cannot read private subject", "non-owner cannot read private subject"]) {
    notRun(name, "SECURITY_PRIVATE_SUBJECT, isolated owner/non-owner cookies, and SECURITY_ALLOWED_ORIGIN are required");
  }
}

const magicToken = process.env.SECURITY_MAGIC_LINK_TOKEN ?? "";
if (isolated && magicToken && allowedOrigin) {
  let issuedCookie = "";
  await check("magic link fixture succeeds exactly once", "/v1/auth/magic-link/consume", {
    method: "POST", headers: { Origin: allowedOrigin, "Content-Type": "application/json" }, body: JSON.stringify({ token: magicToken }),
  }, async (response) => {
    expect(response.status === 200, `status ${response.status}`);
    issuedCookie = response.headers.get("set-cookie") ?? "";
    expect(/jandibat_session=/u.test(issuedCookie), "session cookie was not issued");
  });
  await check("magic link replay is rejected", "/v1/auth/magic-link/consume", {
    method: "POST", headers: { Origin: allowedOrigin, "Content-Type": "application/json" }, body: JSON.stringify({ token: magicToken }),
  }, async (response) => expect([400, 401, 409].includes(response.status), `status ${response.status}`));
} else {
  notRun("magic link fixture succeeds exactly once", "isolated fixture guard, allowlisted origin, or one-time token is missing");
  notRun("magic link replay is rejected", "isolated fixture guard, allowlisted origin, or one-time token is missing");
}

if (isolated && process.env.SECURITY_ENABLE_SLOW_BODY === "true") {
  await slowBodyCheck();
} else notRun("slow request body is terminated by the ingress/server deadline", "isolated fixture and SECURITY_ENABLE_SLOW_BODY=true are required");

const logVerifierURL = process.env.SECURITY_LOG_CANARY_VERIFY_URL ?? "";
const logVerifierBearer = process.env.SECURITY_LOG_CANARY_VERIFY_BEARER ?? "";
if (isolated && logVerifierURL && logVerifierBearer) await logCanaryCheck(logVerifierURL, logVerifierBearer);
else notRun("credential-shaped log canary is absent from structured logs", "isolated fixture and external log verifier credentials are required");

const failed = checks.filter((item) => item.result === "failed");
const notRunChecks = checks.filter((item) => item.result === "not-run");
const result = failed.length > 0 || (requireExtended && notRunChecks.length > 0) ? "failed" : notRunChecks.length > 0 ? "partial" : "passed";
const report = { schemaVersion: 2, startedAt: startedAt.toISOString(), finishedAt: new Date().toISOString(), baseOrigin: baseURL.origin, subject, isolatedFixture: isolated, requireExtended, result, checks };
const serialized = `${JSON.stringify(report, null, 2)}\n`;
if (outputFile) await writeFile(outputFile, serialized, { mode: 0o600 });
process.stdout.write(serialized);
if (result === "failed") process.exitCode = 1;

async function check(name, path, init, assertion, perCheckTimeout = timeoutMs) {
  const started = performance.now();
  try {
    const headers = Object.fromEntries(Object.entries(init.headers ?? {}).filter(([, value]) => value !== undefined));
    const response = await fetch(new URL(path, baseURL), { ...init, headers, redirect: "error", signal: AbortSignal.timeout(perCheckTimeout) });
    const body = await response.text();
    await assertion(response, body);
    checks.push({ name, result: "passed", status: response.status, durationMs: round(performance.now() - started) });
  } catch (error) {
    checks.push({ name, result: "failed", durationMs: round(performance.now() - started), detail: safeError(error) });
  }
}

function notRun(name, reason) { checks.push({ name, result: "not-run", reason }); }

async function slowBodyCheck() {
  const timeout = positiveInteger("SECURITY_SLOW_BODY_TIMEOUT_MS", 30000);
  const started = performance.now();
  let timer;
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode('{"email":"'));
      timer = setInterval(() => controller.enqueue(new Uint8Array([97])), 1000);
    },
    cancel() { clearInterval(timer); },
  });
  try {
    const response = await fetch(new URL("/graphql", baseURL), { method: "POST", headers: { "Content-Type": "application/json" }, body, duplex: "half", signal: AbortSignal.timeout(timeout) });
    clearInterval(timer);
    expect([408, 413].includes(response.status), `status ${response.status}`);
    checks.push({ name: "slow request body is terminated by the ingress/server deadline", result: "passed", status: response.status, durationMs: round(performance.now() - started) });
  } catch (error) {
    clearInterval(timer);
    checks.push({ name: "slow request body is terminated by the ingress/server deadline", result: "failed", durationMs: round(performance.now() - started), detail: safeError(error) });
  }
}

async function logCanaryCheck(verifierURL, bearer) {
  const marker = `magic-token-${randomBytes(32).toString("base64url")}`;
  await fetch(new URL("/v1/auth/magic-link/consume", baseURL), { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token: marker }), signal: AbortSignal.timeout(timeoutMs) });
  const verify = new URL(verifierURL);
  verify.searchParams.set("marker", marker);
  await check("credential-shaped log canary is absent from structured logs", verify, { headers: { Authorization: `Bearer ${bearer}` } }, async (response, body) => {
    expect(response.ok, `verifier status ${response.status}`);
    const payload = JSON.parse(body);
    expect(payload.found === false, "log verifier found the raw credential canary");
  });
}

function expectPrivateCache(response) {
  const value = response.headers.get("cache-control")?.toLowerCase() ?? "";
  expect(value.includes("private") && value.includes("no-store"), `unsafe Cache-Control ${JSON.stringify(value)}`);
}
function graphQLRequest(query, operationName, variables) {
  return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ query, operationName, variables }) };
}
function parseGraphQL(body) {
  try {
    const payload = JSON.parse(body);
    expect(!payload.errors, "GraphQL returned errors");
    return payload;
  } catch {
    throw new Error("invalid GraphQL response");
  }
}
function commonHeaders(response) { expect(response.headers.get("x-content-type-options")?.toLowerCase() === "nosniff", "missing X-Content-Type-Options: nosniff"); }
function containsSecretShape(value) { return /(-----BEGIN [A-Z ]+ PRIVATE KEY-----|ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})/u.test(value); }
function problemCode(body) { try { return JSON.parse(body).code; } catch { return ""; } }
function normalizeCookie(value) { if (!value) return ""; return value.includes("=") ? value : `jandibat_session=${value}`; }
function expect(condition, message) { if (!condition) throw new Error(message); }
function safeError(error) { return error instanceof Error ? error.message.replace(/[A-Za-z0-9_-]{32,}/gu, "[redacted]").slice(0, 240) : "unknown failure"; }
function positiveInteger(name, fallback) { const value = Number(process.env[name] ?? fallback); if (!Number.isSafeInteger(value) || value < 1) throw new Error(`${name} must be a positive integer`); return value; }
function round(value) { return Math.round(value * 1000) / 1000; }
