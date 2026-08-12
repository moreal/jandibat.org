import assert from "node:assert/strict";
import test from "node:test";

import {
  validateAuthorizationUrl,
  validateRuntimeApiBaseUrl,
} from "../src/api/authorization-url.ts";

test("accepts absolute HTTPS provider authorization URLs", () => {
  assert.equal(
    validateAuthorizationUrl("https://github.com/login/oauth/authorize?client_id=test"),
    "https://github.com/login/oauth/authorize?client_id=test",
  );
});

test("blocks executable, opaque, relative, and credential-bearing URLs", () => {
  for (const value of [
    "javascript:alert(1)",
    "data:text/html,malicious",
    "//evil.example/authorize",
    "/oauth/authorize",
    "https://user:password@provider.example/authorize",
  ]) {
    assert.throws(() => validateAuthorizationUrl(value));
  }
});

test("allows HTTP loopback only when explicitly enabled for development", () => {
  for (const value of [
    "http://localhost:8080/oauth",
    "http://127.0.0.1:8080/oauth",
    "http://[::1]:8080/oauth",
  ]) {
    assert.throws(() => validateAuthorizationUrl(value));
    assert.equal(validateAuthorizationUrl(value, true), value);
  }

  assert.throws(() => validateAuthorizationUrl("http://provider.example/oauth", true));
  assert.throws(() => validateAuthorizationUrl("http://localhost.evil.example/oauth", true));
});

test("runtime API base accepts HTTPS origins and path prefixes", () => {
  assert.equal(
    validateRuntimeApiBaseUrl("https://api.example.test/v1/"),
    "https://api.example.test/v1",
  );
  assert.equal(validateRuntimeApiBaseUrl(""), "");
});

test("runtime API base rejects executable schemes and URL decorations", () => {
  for (const value of [
    "javascript:alert(1)",
    "https://user@example.test",
    "https://api.example.test?redirect=evil",
    "https://api.example.test/#fragment",
    " https://api.example.test",
  ]) {
    assert.throws(() => validateRuntimeApiBaseUrl(value));
  }
});

test("runtime API base limits plain HTTP to explicit development loopback", () => {
  assert.equal(
    validateRuntimeApiBaseUrl("http://localhost:8080", true),
    "http://localhost:8080",
  );
  assert.throws(() => validateRuntimeApiBaseUrl("http://localhost:8080"));
  assert.throws(() => validateRuntimeApiBaseUrl("http://api.example.test", true));
});
