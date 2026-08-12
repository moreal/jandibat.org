import assert from "node:assert/strict";
import test from "node:test";

import {
  creationOptionsFromJson,
  PasskeyInputError,
  requestOptionsFromJson,
} from "../src/auth/passkey.ts";

test("decodes WebAuthn creation and request binary members", () => {
  const creation = creationOptionsFromJson({
    challenge: "AQID",
    rp: { id: "app.example.test", name: "jandibat.org" },
    user: {
      id: "BAUG",
      name: "person@example.test",
      displayName: "Person",
    },
    pubKeyCredParams: [{ type: "public-key", alg: -7 }],
    excludeCredentials: [{ type: "public-key", id: "BwgJ" }],
  });
  assert.deepEqual([...new Uint8Array(creation.challenge)], [1, 2, 3]);
  assert.deepEqual([...new Uint8Array(creation.user.id)], [4, 5, 6]);
  assert.deepEqual(
    [...new Uint8Array(creation.excludeCredentials[0].id)],
    [7, 8, 9],
  );

  const request = requestOptionsFromJson({
    challenge: "AQID",
    allowCredentials: [{ type: "public-key", id: "BAUG" }],
  });
  assert.deepEqual([...new Uint8Array(request.challenge)], [1, 2, 3]);
  assert.deepEqual(
    [...new Uint8Array(request.allowCredentials[0].id)],
    [4, 5, 6],
  );
});

test("rejects malformed or oversized binary options before invoking WebAuthn", () => {
  for (const challenge of ["", "not+base64", "A", "A".repeat(16_385)]) {
    assert.throws(
      () => requestOptionsFromJson({ challenge }),
      PasskeyInputError,
    );
  }
  assert.throws(
    () => creationOptionsFromJson({ challenge: "AQID", user: { id: "BAUG" } }),
    PasskeyInputError,
  );
  assert.throws(
    () =>
      requestOptionsFromJson({
        challenge: "AQID",
        allowCredentials: [{ type: "password", id: "BAUG" }],
      }),
    PasskeyInputError,
  );
});
