import { afterEach, expect, it, vi } from "vitest";
import { FetchJandibatApi, api } from "../src/api/client";

const authResult = {
  user: {
    id: "user-1", primaryEmail: "owner@example.test", emailVerifiedAt: null,
    status: "active", createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z",
  },
  session: {
    id: "session-1", userId: "user-1", current: true,
    createdAt: "2026-09-24T00:00:00Z", expiresAt: "2026-09-25T00:00:00Z",
    lastSeenAt: null, revokedAt: null, userAgent: null, ipAddress: null,
  },
};

afterEach(() => {
  delete window.__JANDIBAT_CONFIG__;
  vi.unstubAllGlobals();
});

it("exposes HTTP edge calls without the superseded REST domain API", () => {
  const legacyDomainOperations = [
    "getActivities", "listProviderCatalog", "listConnections", "connectProvider",
    "disconnectProvider", "syncProvider", "getSyncJob", "getCurrentSession",
    "signOut", "listSubjects", "createSubject", "requestSubjectDeletion",
    "beginPasskeyRegistration", "finishPasskeyRegistration",
    "beginPasskeyAuthentication", "finishPasskeyAuthentication",
    "listCustomProviders", "createCustomProvider", "updateCustomProvider",
    "deleteCustomProvider", "rotateCustomProviderKey", "requestMagicLink",
  ];
  for (const operation of legacyDomainOperations) {
    expect((api as unknown as Record<string, unknown>)[operation], operation).toBeUndefined();
  }
  expect(api.consumeMagicLink).toBeTypeOf("function");
  expect(api.ingestCustomActivities).toBeTypeOf("function");
});

it("uses runtime config loaded after the default client module was imported", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return Response.json(authResult);
  }));

  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/prefix" };
  await api.consumeMagicLink("link-token");

  expect(requests).toEqual(["https://api.example.test/prefix/v1/auth/magic-link/consume"]);
});

it("keeps an explicit client base fixed when runtime config changes", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return Response.json(authResult);
  }));
  const client = new FetchJandibatApi("https://fixed.example.test/api/");
  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/other" };

  await client.consumeMagicLink("link-token");

  expect(requests).toEqual(["https://fixed.example.test/api/v1/auth/magic-link/consume"]);
});

it("sends custom ingest only to its HTTP edge with the provider key outside the URL", async () => {
  const requests: Array<{ url: string; init: RequestInit | undefined }> = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    requests.push({ url: String(input), init });
    return Response.json({ accepted: 1, rejected: 0, duplicates: 0, rejections: [] });
  }));

  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/base" };
  await api.ingestCustomActivities("provider/1", "synthetic-key", {
    schemaVersion: "1.0",
    events: [{ eventId: "event-1", date: "2026-09-24", action: "read", metric: { name: "count", value: 1 } }],
  }, "idempotency-1");

  expect(requests).toHaveLength(1);
  expect(requests[0].url).toBe("https://api.example.test/base/v1/custom-providers/provider%2F1/activities:ingest");
  expect(requests[0].init?.method).toBe("POST");
  expect(requests[0].init?.credentials).toBe("include");
  const headers = new Headers(requests[0].init?.headers);
  expect(headers.get("X-Jandibat-Provider-Key")).toBe("synthetic-key");
  expect(headers.get("Idempotency-Key")).toBe("idempotency-1");
  expect(requests[0].url).not.toContain("synthetic-key");
});
