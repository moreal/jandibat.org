import assert from "node:assert/strict";
import test from "node:test";

import { legacyHashRoutePath } from "../src/routing/legacy.ts";

test("maps legacy hash routes to browser routes", () => {
  assert.equal(legacyHashRoutePath("https://app.example.test/#explore"), "/");
  assert.equal(
    legacyHashRoutePath("https://app.example.test/#explore/my%20garden"),
    "/explore/my%20garden",
  );
  assert.equal(
    legacyHashRoutePath("https://app.example.test/#connections?status=connected"),
    "/connections",
  );
  assert.equal(legacyHashRoutePath("https://app.example.test/#custom"), "/custom");
  assert.equal(legacyHashRoutePath("https://app.example.test/#unknown"), undefined);
});

test("malformed legacy percent encoding cannot abort application startup", () => {
  assert.equal(
    legacyHashRoutePath("https://app.example.test/#explore/bad%name"),
    "/explore/bad%25name",
  );
});
