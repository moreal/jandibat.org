import assert from "node:assert/strict";
import test from "node:test";

import {
  createIngestPayload,
  CustomProviderValidationError,
  MAX_CUSTOM_PROVIDER_PAYLOAD_BYTES,
} from "../src/index.ts";

const now = new Date("2026-08-12T12:00:00Z");

function validEvent(overrides = {}) {
  return {
    eventId: "event-1",
    date: "2026-08-12",
    action: "read",
    metric: { name: "count", value: 1 },
    ...overrides,
  };
}

function validationIssues(events) {
  try {
    createIngestPayload(events, now);
  } catch (error) {
    assert.ok(error instanceof CustomProviderValidationError);
    return error.issues;
  }
  assert.fail("expected validation failure");
}

test("creates only the push-only 1.0 request shape", () => {
  const input = {
    ...validEvent(),
    ignoredEndpoint: "https://evil.example/collect",
    ignoredCredential: "must-not-be-sent",
  };
  const { payload, body } = createIngestPayload([input], now);
  assert.deepEqual(payload, {
    schemaVersion: "1.0",
    events: [validEvent()],
  });
  assert.equal(body.includes("ignoredEndpoint"), false);
  assert.equal(body.includes("ignoredCredential"), false);
});

test("enforces batch, identifier, action, metric, and observedAt boundaries", () => {
  assert.equal(validationIssues([])[0].code, "invalid_batch_size");
  assert.equal(validationIssues(Array.from({ length: 1001 }, () => validEvent()))[0].code, "invalid_batch_size");

  const issues = validationIssues([validEvent({
    eventId: "x".repeat(256),
    action: "x".repeat(65),
    metric: { name: "x".repeat(65), value: 1_000_001 },
    observedAt: "2026-08-12 12:00:00",
  })]);
  assert.deepEqual(
    new Set(issues.map((item) => item.path)),
    new Set([
      "/events/0/eventId",
      "/events/0/action",
      "/events/0/metric/name",
      "/events/0/metric/value",
      "/events/0/observedAt",
    ]),
  );
});

test("enforces real date format and the server's ten-year/one-day window", () => {
  for (const date of ["2026-02-30", "2016-08-11", "2026-08-14", "2026-8-12"]) {
    assert.ok(
      validationIssues([validEvent({ date })]).some((item) =>
        item.code === "invalid_date" || item.code === "date_out_of_range"),
    );
  }
  assert.doesNotThrow(() => createIngestPayload([
    validEvent({ date: "2016-08-12" }),
    validEvent({ eventId: "event-2", date: "2026-08-13" }),
  ], now));
});

test("enforces metadata property and Unicode value limits", () => {
  const tooMany = Object.fromEntries(
    Array.from({ length: 51 }, (_, index) => [`key-${index}`, "value"]),
  );
  assert.ok(
    validationIssues([validEvent({ metadata: tooMany })])
      .some((item) => item.code === "too_many_properties"),
  );
  assert.ok(
    validationIssues([validEvent({ metadata: { note: "🌱".repeat(1001) } })])
      .some((item) => item.code === "too_long"),
  );
  assert.ok(
    validationIssues([validEvent({ metadata: { note: 123 } })])
      .some((item) => item.code === "invalid_type"),
  );
  assert.ok(
    validationIssues([validEvent({ metadata: { "bad key": "value" } })])
      .some((item) => item.code === "invalid_property_name"),
  );
});

test("rejects a valid-shaped request whose encoded JSON exceeds 1 MiB", () => {
  const metadata = Object.fromEntries(
    Array.from({ length: 50 }, (_, index) => [
      `field-${index}`,
      "한".repeat(1000),
    ]),
  );
  const events = Array.from({ length: 8 }, (_, index) =>
    validEvent({ eventId: `large-${index}`, metadata }),
  );
  const issues = validationIssues(events);
  assert.equal(issues[0].code, "payload_too_large");
  assert.match(issues[0].message, new RegExp(String(MAX_CUSTOM_PROVIDER_PAYLOAD_BYTES)));
});
