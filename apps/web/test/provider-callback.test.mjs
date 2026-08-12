import assert from "node:assert/strict";
import test from "node:test";

import {
  providerCallbackLocationHasData,
  providerCallbackResultFromUrl,
  providerCallbackSafeLocation,
} from "../src/api/provider-callback.ts";

test("accepts only the backend OAuth completion marker on the connections route", () => {
  assert.equal(
    providerCallbackResultFromUrl(
      "https://app.example.test/?status=connected#connections",
    ),
    "connected",
  );
  assert.equal(
    providerCallbackResultFromUrl(
      "https://app.example.test/?status=connected#explore",
    ),
    undefined,
  );
  assert.equal(
    providerCallbackResultFromUrl(
      "https://app.example.test/?status=failed&error_description=%3Cscript%3E#connections",
    ),
    undefined,
  );
});

test("removes the one-shot marker without retaining provider error text", () => {
  assert.equal(
    providerCallbackLocationHasData(
      "https://app.example.test/base?error_description=secret#connections",
    ),
    true,
  );
  assert.equal(
    providerCallbackSafeLocation(
      "https://app.example.test/base?campaign=kept&status=connected&error_description=secret#connections",
    ),
    "/base?campaign=kept#connections",
  );
  assert.equal(
    providerCallbackSafeLocation(
      "https://app.example.test/base?status=connected#connections",
    ).includes("connected"),
    false,
  );
});
