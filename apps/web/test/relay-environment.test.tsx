import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, fetchQuery } from "relay-runtime";
import App from "../src/App";
import { createRelayEnvironment } from "../src/relay/environment";
import query from "./__generated__/RelayCompatQuery.graphql";

const appBoundary = vi.hoisted(() => ({ environment: undefined as Environment | undefined }));

vi.mock("@solidjs/router", () => ({ useLocation: () => ({ pathname: "/" }) }));

vi.mock("../src/router", async () => {
  const { useRelayEnvironment } = await import("solid-relay");
  return {
    Router: () => {
      appBoundary.environment = useRelayEnvironment()() as Environment;
      return <span data-testid="app-router">ready</span>;
    },
  };
});

const subjectID = "U3ViamVjdDox";

afterEach(() => {
  cleanup();
  appBoundary.environment = undefined;
  vi.unstubAllGlobals();
});

function executeQuery(environment: ReturnType<typeof createRelayEnvironment>) {
  return new Promise<unknown>((resolve, reject) => {
    fetchQuery(environment, query, { id: subjectID }).subscribe({
      next: resolve,
      error: reject,
    });
  });
}

it("sends a named Relay operation to the configured GraphQL URL with browser cookies", async () => {
  let request: Request | undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    request = new Request(input, init);
    return Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Before" },
    } });
  }));

  const environment = createRelayEnvironment("https://api.example.test/prefix");
  await executeQuery(environment);

  expect(request?.url).toBe("https://api.example.test/prefix/graphql");
  expect(request?.method).toBe("POST");
  expect(request?.credentials).toBe("include");
  expect(request?.headers.get("Content-Type")).toBe("application/json");
  expect(request?.headers.get("Accept")).toBe("application/json");
  expect(request?.headers.has("Authorization")).toBe(false);
  const body = JSON.parse(await request!.text()) as Record<string, unknown>;
  expect(body.operationName).toBe("RelayCompatQuery");
  expect(body.variables).toEqual({ id: subjectID });
  expect(typeof body.query).toBe("string");
  expect(String(body.query)).toContain("query RelayCompatQuery");
});

it("aborts an outstanding GraphQL fetch when Relay unsubscribes", async () => {
  let signal: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
    signal = init?.signal as AbortSignal;
    return new Promise<Response>(() => {});
  }));

  const environment = createRelayEnvironment("");
  const subscription = fetchQuery(environment, query, { id: subjectID }).subscribe({});
  expect(signal?.aborted).toBe(false);
  subscription.unsubscribe();
  expect(signal?.aborted).toBe(true);
});

it("rejects HTTP failures without surfacing response content or credentials", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(
    '{"detail":"sensitive-token"}',
    { status: 503, headers: { "Content-Type": "application/problem+json" } },
  )));

  await expect(executeQuery(createRelayEnvironment("")))
    .rejects.toThrow("GraphQL request failed (503).");
});

it("rejects GraphQL errors on HTTP 200 without echoing server-supplied text", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({
    errors: [{ message: "sensitive-token" }],
  })));

  await expect(executeQuery(createRelayEnvironment("")))
    .rejects.toThrow("GraphQL request failed.");
});

it("does not echo network exception text into Relay errors", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => {
    throw new Error("GraphQL request failed: sensitive-token");
  }));

  await expect(executeQuery(createRelayEnvironment("")))
    .rejects.toThrow(/^GraphQL request failed\.$/);
});

it("creates the application Relay provider only after runtime config resolves", async () => {
  let resolveConfig!: (response: Response) => void;
  let request: Request | undefined;
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    request = new Request(input, init);
    if (request.url.endsWith("/config.json")) {
      return new Promise<Response>((resolve) => { resolveConfig = resolve; });
    }
    return Promise.resolve(Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Before" },
    } }));
  }));

  const view = render(() => <App />);
  expect(view.queryByTestId("app-router")).toBeNull();
  expect(appBoundary.environment).toBeUndefined();
  await waitFor(() => expect(resolveConfig).toBeTypeOf("function"));
  resolveConfig(Response.json({ apiBaseUrl: "https://api.example.test/prefix" }));
  await waitFor(() => expect(view.getByTestId("app-router")).toBeDefined());
  expect(appBoundary.environment).toBeInstanceOf(Environment);
  await executeQuery(appBoundary.environment!);
  expect(request?.url).toBe("https://api.example.test/prefix/graphql");
});
