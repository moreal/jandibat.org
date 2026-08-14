import assert from "node:assert/strict";
import test from "node:test";

import {
  privateConsentEligible,
  privateConsentValue,
} from "../src/providers/model.ts";

test("private collection requires capability, credential auth, and explicit consent", () => {
  for (const method of ["oauth2", "token"]) {
    assert.equal(privateConsentEligible(true, method), true);
    assert.equal(privateConsentValue(true, method, false), false);
    assert.equal(privateConsentValue(true, method, true), true);
    assert.equal(privateConsentValue(false, method, true), false);
  }
  assert.equal(privateConsentEligible(true, "none"), false);
  assert.equal(privateConsentValue(true, "none", true), false);
});
