import { afterEach, expect, it, vi } from "vitest";
import { FetchJandibatApi, api } from "../src/api/client";

afterEach(() => {
  delete window.__JANDIBAT_CONFIG__;
  vi.unstubAllGlobals();
});

it("uses runtime config loaded after the default client module was imported", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return Response.json({ providers: [] });
  }));

  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/prefix" };
  await api.listProviderCatalog();

  expect(requests).toEqual(["https://api.example.test/prefix/v1/providers"]);
});

it("keeps an explicit client base fixed when runtime config changes", async () => {
  const requests: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    requests.push(String(input));
    return Response.json({ providers: [] });
  }));
  const client = new FetchJandibatApi("https://fixed.example.test/api/");
  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/other" };

  await client.listProviderCatalog();

  expect(requests).toEqual(["https://fixed.example.test/api/v1/providers"]);
});
