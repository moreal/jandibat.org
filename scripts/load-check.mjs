import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";

const baseURL = new URL(process.env.LOAD_BASE_URL ?? "http://127.0.0.1:8080");
const subject = process.env.LOAD_SUBJECT ?? "octocat";
const durationSeconds = optionalPositiveInteger("LOAD_DURATION_SECONDS");
const requestCount = durationSeconds ? undefined : positiveInteger("LOAD_REQUESTS", 600);
const concurrency = positiveInteger("LOAD_CONCURRENCY", 20);
const timeoutMs = positiveInteger("LOAD_TIMEOUT_MS", 5000);
const maximumErrorRate = numberBetween("LOAD_MAX_ERROR_RATE", 0.001, 0, 1);
const outputFile = process.env.LOAD_OUTPUT_FILE;
const authorization = process.env.LOAD_AUTHORIZATION;
const ingestProviderID = process.env.LOAD_INGEST_PROVIDER_ID;
const ingestProviderKey = process.env.LOAD_INGEST_PROVIDER_KEY;
const requireIngest = process.env.LOAD_REQUIRE_INGEST === "true";
const ingestEvents = positiveInteger("LOAD_INGEST_EVENTS_PER_REQUEST", 100);
const startedAt = new Date();
const activityDate = startedAt.toISOString().slice(0, 10);
const activityFrom = new Date(startedAt.getTime() - 29 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
const activityQuery = "query LoadActivitySnapshot($subject: String!, $range: DateRangeInput!, $timezone: TimeZone!) { subject(handleOrID: $subject) { handle activitySnapshot(range: $range, timezone: $timezone) { revision generatedAt dataUpdatedAt total } } }";

if (Boolean(ingestProviderID) !== Boolean(ingestProviderKey)) {
  throw new Error("LOAD_INGEST_PROVIDER_ID and LOAD_INGEST_PROVIDER_KEY must be provided together");
}
if (requireIngest && !ingestProviderID) {
  throw new Error("LOAD_REQUIRE_INGEST=true requires a dedicated custom provider ID and key");
}

const targets = [
  target("health", "/healthz", "GET", "LOAD_HEALTH_RPS", 1, "LOAD_HEALTH_P95_MS", 100, "LOAD_HEALTH_P99_MS", 250),
  target("activities", "/graphql", "POST", "LOAD_ACTIVITY_RPS", 50, "LOAD_ACTIVITY_P95_MS", 300, "LOAD_ACTIVITY_P99_MS", 800),
  target("render", `/v1/render/${encodeURIComponent(subject)}.svg`, "GET", "LOAD_RENDER_RPS", 10, "LOAD_RENDER_P95_MS", 500, "LOAD_RENDER_P99_MS", 1200),
];
if (ingestProviderID) {
  targets.push(target("ingest", `/v1/custom-providers/${encodeURIComponent(ingestProviderID)}/activities:ingest`, "POST", "LOAD_INGEST_RPS", 20, "LOAD_INGEST_P95_MS", 700, "LOAD_INGEST_P99_MS", 2000));
}

const observations = new Map(targets.map((item) => [item.name, []]));
const failures = [];
const ingestion = { submitted: 0, accepted: 0, duplicates: 0, rejected: 0, invalidResponses: 0 };
let attempted = 0;

if (durationSeconds) await runRateProfile();
else await runRequestCountProfile();

const summaries = targets.map((item) => summarize(item));
const errorRate = failures.length / Math.max(attempted, 1);
const result = {
  schemaVersion: 1,
  startedAt: startedAt.toISOString(),
  finishedAt: new Date().toISOString(),
  baseOrigin: baseURL.origin,
  subject,
  profile: durationSeconds
    ? { mode: "rate", durationSeconds, concurrency, rates: Object.fromEntries(targets.map((item) => [item.name, item.rps])) }
    : { mode: "request-count", requestCount, concurrency },
  attempted,
  errorRate: round(errorRate),
  maximumErrorRate,
  endpoints: summaries,
  ingestion: ingestProviderID ? { ...ingestion, partitionMatches: ingestion.submitted === ingestion.accepted + ingestion.duplicates + ingestion.rejected } : undefined,
  failures: failures.slice(0, 50),
};
const partitionFailed = ingestProviderID && ingestion.submitted !== ingestion.accepted + ingestion.duplicates + ingestion.rejected;
result.result = errorRate <= maximumErrorRate && summaries.every((summary) => summary.passed) && !partitionFailed ? "passed" : "failed";
const serialized = `${JSON.stringify(result, null, 2)}\n`;
if (outputFile) await writeFile(outputFile, serialized, { mode: 0o600 });
process.stdout.write(serialized);
if (result.result !== "passed") process.exitCode = 1;

async function runRequestCountProfile() {
  let cursor = 0;
  await Promise.all(Array.from({ length: Math.min(concurrency, requestCount) }, async () => {
    while (true) {
      const index = cursor++;
      if (index >= requestCount) return;
      await issue(targets[index % targets.length], index);
    }
  }));
}

async function runRateProfile() {
  const launched = new Map(targets.map((item) => [item.name, 0]));
  const inFlight = new Set();
  const profileStarted = performance.now();
  while ((performance.now() - profileStarted) / 1000 < durationSeconds) {
    const elapsedSeconds = (performance.now() - profileStarted) / 1000;
    for (const item of targets) {
      const desired = Math.floor(elapsedSeconds * item.rps);
      while (launched.get(item.name) < desired && inFlight.size < concurrency) {
        const sequence = launched.get(item.name);
        launched.set(item.name, sequence + 1);
        const promise = issue(item, sequence).finally(() => inFlight.delete(promise));
        inFlight.add(promise);
      }
    }
    await delay(20);
  }
  await Promise.all(inFlight);
  for (const item of targets) {
    const expected = Math.floor(durationSeconds * item.rps);
    const actual = launched.get(item.name);
    if (actual < expected * 0.99) failures.push({ endpoint: item.name, error: "load-generator-saturation", expected, actual });
  }
}

async function issue(item, sequence) {
  attempted += 1;
  const headers = { Accept: item.name === "render" ? "image/svg+xml" : "application/json" };
  if (authorization) headers.Authorization = authorization;
  let body;
  if (item.name === "activities") {
    headers["Content-Type"] = "application/json";
    body = JSON.stringify({ query: activityQuery, operationName: "LoadActivitySnapshot", variables: { subject, range: { from: activityFrom, to: activityDate }, timezone: "UTC" } });
  } else if (item.name === "ingest") {
    headers["Content-Type"] = "application/json";
    headers["X-Jandibat-Provider-Key"] = ingestProviderKey;
    headers["Idempotency-Key"] = `load-${startedAt.getTime()}-${sequence}-${randomUUID()}`;
    const events = Array.from({ length: ingestEvents }, (_, index) => ({
      eventId: `load-${startedAt.getTime()}-${sequence}-${index}`,
      date: new Date().toISOString().slice(0, 10),
      action: "load-test",
      metric: { name: "count", value: 1 },
    }));
    ingestion.submitted += events.length;
    body = JSON.stringify({ schemaVersion: "1.0", events });
  }

  const requestStarted = performance.now();
  try {
    const response = await fetch(new URL(item.path, baseURL), {
      method: item.method,
      headers,
      body,
      redirect: "error",
      signal: AbortSignal.timeout(timeoutMs),
    });
    const responseBody = await response.text();
    observations.get(item.name).push(performance.now() - requestStarted);
    if (!response.ok) failures.push({ endpoint: item.name, status: response.status });
    else if (item.name === "activities" && !validActivityResponse(responseBody)) failures.push({ endpoint: item.name, error: "invalid-graphql-response" });
    if (item.name === "ingest" && response.ok) recordIngestionResponse(responseBody);
  } catch (error) {
    observations.get(item.name).push(performance.now() - requestStarted);
    failures.push({ endpoint: item.name, error: error instanceof Error ? error.name : "unknown" });
  }
}

function validActivityResponse(body) {
  try {
    const result = JSON.parse(body);
    return !result.errors && result.data?.subject?.handle === subject &&
      typeof result.data.subject.activitySnapshot?.revision === "string" && result.data.subject.activitySnapshot.revision.length > 0 &&
      typeof result.data.subject.activitySnapshot.generatedAt === "string";
  } catch {
    return false;
  }
}

function recordIngestionResponse(body) {
  try {
    const value = JSON.parse(body);
    for (const field of ["accepted", "duplicates", "rejected"]) {
      if (!Number.isSafeInteger(value[field]) || value[field] < 0) throw new Error(`invalid ${field}`);
      ingestion[field] += value[field];
    }
  } catch {
    ingestion.invalidResponses += 1;
    failures.push({ endpoint: "ingest", error: "invalid-ingestion-response" });
  }
}

function target(name, path, method, rpsName, rpsDefault, p95Name, p95Default, p99Name, p99Default) {
  return {
    name, path, method,
    rps: positiveInteger(rpsName, rpsDefault),
    maximumP95Ms: positiveInteger(p95Name, p95Default),
    maximumP99Ms: positiveInteger(p99Name, p99Default),
  };
}

function summarize(item) {
  const values = observations.get(item.name).sort((left, right) => left - right);
  const p95Ms = percentile(values, 0.95);
  const p99Ms = percentile(values, 0.99);
  return {
    endpoint: item.name,
    requests: values.length,
    p50Ms: round(percentile(values, 0.5)),
    p95Ms: round(p95Ms),
    p99Ms: round(p99Ms),
    maximumP95Ms: item.maximumP95Ms,
    maximumP99Ms: item.maximumP99Ms,
    passed: p95Ms <= item.maximumP95Ms && p99Ms <= item.maximumP99Ms,
  };
}

function positiveInteger(name, fallback) {
  const value = Number(process.env[name] ?? fallback);
  if (!Number.isSafeInteger(value) || value < 1) throw new Error(`${name} must be a positive integer`);
  return value;
}

function optionalPositiveInteger(name) {
  if (process.env[name] === undefined || process.env[name] === "") return undefined;
  return positiveInteger(name, 1);
}

function numberBetween(name, fallback, minimum, maximum) {
  const value = Number(process.env[name] ?? fallback);
  if (!Number.isFinite(value) || value < minimum || value > maximum) throw new Error(`${name} must be between ${minimum} and ${maximum}`);
  return value;
}

function percentile(values, quantile) {
  if (values.length === 0) return Number.POSITIVE_INFINITY;
  return values[Math.min(values.length - 1, Math.ceil(values.length * quantile) - 1)];
}

function round(value) {
  return Number.isFinite(value) ? Math.round(value * 1000) / 1000 : null;
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}
