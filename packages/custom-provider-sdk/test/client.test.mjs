import assert from "node:assert/strict";
import test from "node:test";

import {
  createCustomProviderClient,
  createIdempotencyKey,
  CustomProviderApiError,
  CustomProviderNetworkError,
  CustomProviderResponseError,
  CustomProviderValidationError,
} from "../src/index.ts";

const providerId = "018f0000-0000-7000-8000-000000000001";
const ingestionKey = "sdk-fixture-ingestion-key-value-0001";
const now = () => new Date("2026-08-12T12:00:00Z");

function event(overrides = {}) {
  return {
    eventId: "reading-2026-08-12",
    date: "2026-08-12",
    action: "read",
    metric: { name: "count", value: 1 },
    metadata: { title: "A safe book" },
    observedAt: "2026-08-12T11:00:00Z",
    ...overrides,
  };
}

test("pushes the fixed versioned schema to the fixed provider endpoint", async () => {
  let captured;
  const client = createCustomProviderClient({
    apiBaseUrl: "https://api.example.test/base/",
    providerId,
    ingestionKey,
    now,
    fetch: async (url, init) => {
      captured = { url, init };
      return Response.json(
        { accepted: 1, duplicates: 0, rejected: 0, rejections: [] },
        { status: 202 },
      );
    },
  });

  const response = await client.push([event()], {
    idempotencyKey: "reading-request-0001",
  });

  assert.deepEqual(response, {
    accepted: 1,
    duplicates: 0,
    rejected: 0,
    rejections: [],
  });
  assert.equal(
    captured.url,
    `https://api.example.test/base/v1/custom-providers/${providerId}/activities:ingest`,
  );
  assert.equal(captured.init.method, "POST");
  assert.equal(captured.init.credentials, "omit");
  assert.equal(captured.init.redirect, "error");
  assert.equal(captured.init.referrerPolicy, "no-referrer");
  assert.equal(captured.init.headers["X-Jandibat-Provider-Key"], ingestionKey);
  assert.equal(captured.init.headers["Idempotency-Key"], "reading-request-0001");
  assert.deepEqual(JSON.parse(captured.init.body), {
    schemaVersion: "1.0",
    events: [event()],
  });
});

test("accepts no custom endpoint and validates the API base before any request", () => {
  for (const apiBaseUrl of [
    "http://api.example.test",
    "javascript:alert(1)",
    "https://user:secret@api.example.test",
    "https://api.example.test?endpoint=https://evil.test",
    "https://api.example.test/#fragment",
    " https://api.example.test",
    "https://api.example.test/%0d%0aheader",
  ]) {
    assert.throws(
      () => createCustomProviderClient({ apiBaseUrl, providerId, ingestionKey }),
      CustomProviderValidationError,
    );
  }
  assert.throws(
    () => createCustomProviderClient({
      apiBaseUrl: "https://api.example.test",
      providerId,
      ingestionKey,
      endpoint: "https://evil.example/ingest",
    }),
    CustomProviderValidationError,
  );
});

test("requires a persisted idempotency key and rejects endpoint-like push options", async () => {
  const client = createCustomProviderClient({
    apiBaseUrl: "https://api.example.test",
    providerId,
    ingestionKey,
    now,
    fetch: async () => assert.fail("invalid options must not reach fetch"),
  });
  await assert.rejects(
    client.push([event()], {}),
    CustomProviderValidationError,
  );
  await assert.rejects(
    client.push([event()], {
      idempotencyKey: "request-key-0001",
      endpoint: "https://evil.example/ingest",
    }),
    CustomProviderValidationError,
  );
  assert.match(createIdempotencyKey(), /^[0-9a-f-]{36}$/);
});

test("validates provider credentials without disclosing their value", () => {
  for (const config of [
    { providerId: "../other-provider", ingestionKey },
    { providerId, ingestionKey: "too-short" },
    { providerId, ingestionKey: `${"a".repeat(32)}\nX-Injected: yes` },
  ]) {
    let thrown;
    try {
      createCustomProviderClient({
        apiBaseUrl: "https://api.example.test",
        ...config,
      });
    } catch (error) {
      thrown = error;
    }
    assert.ok(thrown instanceof CustomProviderValidationError);
    assert.equal(String(thrown).includes(config.ingestionKey), false);
  }
});

test("keeps the ingestion key out of client serialization and API errors", async () => {
  const client = createCustomProviderClient({
    apiBaseUrl: "https://api.example.test",
    providerId,
    ingestionKey,
    now,
    fetch: async () => Response.json({
      type: "about:blank",
      title: ingestionKey,
      detail: ingestionKey,
      status: 401,
      code: "unauthorized",
      requestId: "request-1",
    }, { status: 401 }),
  });
  assert.equal(JSON.stringify(client), "{}");

  let thrown;
  try {
    await client.push([event()], { idempotencyKey: "request-key-0001" });
  } catch (error) {
    thrown = error;
  }
  assert.ok(thrown instanceof CustomProviderApiError);
  assert.equal(thrown.status, 401);
  assert.equal(thrown.code, "unauthorized");
  assert.equal(thrown.requestId, "request-1");
  assert.equal(String(thrown).includes(ingestionKey), false);
  assert.equal(JSON.stringify(thrown).includes(ingestionKey), false);
});

test("maps fetch failures to a stable secret-free network error", async () => {
  const client = createCustomProviderClient({
    apiBaseUrl: "https://api.example.test",
    providerId,
    ingestionKey,
    now,
    fetch: async () => {
      throw new Error(`upstream echoed ${ingestionKey}`);
    },
  });
  await assert.rejects(
    client.push([event()], { idempotencyKey: "request-key-0001" }),
    (error) =>
      error instanceof CustomProviderNetworkError &&
      !String(error).includes(ingestionKey),
  );
});

test("rejects malformed success responses instead of trusting JSON", async () => {
  const client = createCustomProviderClient({
    apiBaseUrl: "https://api.example.test",
    providerId,
    ingestionKey,
    now,
    fetch: async () => Response.json(
      { accepted: 2, duplicates: 0, rejected: 0, rejections: [] },
      { status: 202 },
    ),
  });
  await assert.rejects(
    client.push([event()], { idempotencyKey: "request-key-0001" }),
    CustomProviderResponseError,
  );
});
