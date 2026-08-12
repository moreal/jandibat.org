import assert from "node:assert/strict";
import test from "node:test";

import {
  magicLinkLocationHasToken,
  magicLinkRedirectUrl,
  magicLinkSafeLocation,
  magicLinkTokenFromUrl,
} from "../src/auth/magic-link.ts";

const fragmentToken = "a".repeat(43);
const queryToken = "b".repeat(43);

test("reads a magic-link token from the route fragment", () => {
  assert.equal(
    magicLinkTokenFromUrl(`https://app.example.test/#auth?token=${fragmentToken}`),
    fragmentToken,
  );
  assert.equal(
    magicLinkTokenFromUrl(`https://app.example.test/#magic_token=${queryToken}`),
    queryToken,
  );
});

test("a fragment token is read while a token-named query is ignored", () => {
  assert.equal(
    magicLinkTokenFromUrl(
      `https://app.example.test/?token=${queryToken}#auth?token=${fragmentToken}`,
    ),
    fragmentToken,
  );
});

test("query tokens are never consumed and are removed during sanitization", () => {
  const value = `https://app.example.test/sign-in?campaign=welcome&magic_token=${queryToken}`;
  assert.equal(magicLinkTokenFromUrl(value), undefined);
  assert.equal(magicLinkLocationHasToken(value), true);
  assert.equal(magicLinkSafeLocation(value), "/sign-in?campaign=welcome#auth");
});

test("the sanitized location cannot retain fragment or query token material", () => {
  const safe = magicLinkSafeLocation(
    `https://app.example.test/?token=${queryToken}#auth?token=${fragmentToken}`,
  );
  assert.equal(safe, "/#auth");
  assert.equal(safe.includes(queryToken), false);
  assert.equal(safe.includes(fragmentToken), false);
});

test("unrelated fragments do not produce a token", () => {
  assert.equal(
    magicLinkTokenFromUrl("https://app.example.test/#explore"),
    undefined,
  );
});

test("rejects malformed and out-of-contract token values", () => {
  for (const token of ["short", "a".repeat(31), "a".repeat(2049), `${"a".repeat(31)}+`]) {
    assert.equal(
      magicLinkTokenFromUrl(`https://app.example.test/#auth?token=${encodeURIComponent(token)}`),
      undefined,
    );
    assert.equal(
      magicLinkLocationHasToken(
        `https://app.example.test/#auth?token=${encodeURIComponent(token)}`,
      ),
      true,
    );
  }
});

test("builds the exact allowlisted redirect for root and path-prefix deployments", () => {
  assert.equal(
    magicLinkRedirectUrl("https://app.example.test/"),
    "https://app.example.test/?auth=magic#auth",
  );
  assert.equal(
    magicLinkRedirectUrl("https://app.example.test/jandibat/"),
    "https://app.example.test/jandibat/?auth=magic#auth",
  );
});

test("redirect construction cannot carry token or unrelated URL state", () => {
  const redirect = magicLinkRedirectUrl(
    `https://app.example.test/sign-in?token=${queryToken}&campaign=welcome#auth?token=${fragmentToken}`,
  );
  assert.equal(redirect, "https://app.example.test/sign-in?auth=magic#auth");
  assert.equal(redirect.includes(queryToken), false);
  assert.equal(redirect.includes(fragmentToken), false);
  assert.equal(redirect.includes("campaign"), false);
});
