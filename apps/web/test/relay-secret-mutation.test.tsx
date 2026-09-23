import { cleanup, render } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import {
  Environment,
  Network,
  Observable,
  RecordSource,
  Store,
  type ConcreteRequest,
} from "relay-runtime";
import { RelayProvider, createRelayEphemeralMutation } from "../src/relay";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function secretMutation(name: string, field: string, scalar = "ingestionKey"): ConcreteRequest {
  const args = [{ defaultValue: null, kind: "LocalArgument", name: "input" }] as const;
  const selections = [{
    alias: null,
    args: [{ kind: "Variable", name: "input", variableName: "input" }],
    concreteType: `${field}Payload`,
    kind: "LinkedField",
    name: field,
    plural: false,
    selections: [{ alias: null, args: null, kind: "ScalarField", name: scalar, storageKey: null }],
    storageKey: null,
  }] as const;
  return {
    fragment: { argumentDefinitions: args, kind: "Fragment", metadata: null, name, selections, type: "Mutation", abstractKey: null },
    kind: "Request",
    operation: { argumentDefinitions: args, kind: "Operation", name, selections },
    params: {
      cacheID: name,
      id: null,
      metadata: {},
      name,
      operationKind: "mutation",
      text: `mutation ${name}($input: SecretInput!) { ${field}(input: $input) { ${scalar} } }`,
    },
  } as ConcreteRequest;
}

function unusedEnvironment(source: RecordSource): Environment {
  return new Environment({
    network: Network.create(() => Observable.create((sink) => {
      sink.error(new Error("Secret mutation must not use Relay network."));
    })),
    store: new Store(source),
  });
}

it("returns create and rotate keys once without normalizing them into Relay", async () => {
  const marker = "synthetic-only-secret-marker";
  const source = new RecordSource();
  const seen: Array<{ url: string; init: RequestInit }> = [];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init: RequestInit) => {
    seen.push({ url, init });
    const body = JSON.parse(String(init.body)) as { operationName: string };
    const field = body.operationName === "CreateCustomProviderProbe" ? "createCustomProvider" : "rotateCustomProviderKey";
    return new Response(JSON.stringify({ data: { [field]: { ingestionKey: marker } } }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  }));

  let create!: (variables: { input: { subjectID: string } }) => Promise<{ createCustomProvider: { ingestionKey: string } }>;
  let rotate!: (variables: { input: { id: string } }) => Promise<{ rotateCustomProviderKey: { ingestionKey: string } }>;
  function App() {
    create = createRelayEphemeralMutation<{
      variables: { input: { subjectID: string } };
      response: { createCustomProvider: { ingestionKey: string } };
    }>("https://api.example.test/base/", secretMutation("CreateCustomProviderProbe", "createCustomProvider"));
    rotate = createRelayEphemeralMutation<{
      variables: { input: { id: string } };
      response: { rotateCustomProviderKey: { ingestionKey: string } };
    }>("https://api.example.test/base/", secretMutation("RotateCustomProviderKeyProbe", "rotateCustomProviderKey"));
    return <span>ready</span>;
  }
  const view = render(() => <RelayProvider environment={unusedEnvironment(source)}><App /></RelayProvider>);
  expect((await create({ input: { subjectID: "synthetic-subject" } })).createCustomProvider.ingestionKey).toBe(marker);
  expect((await rotate({ input: { id: "synthetic-provider" } })).rotateCustomProviderKey.ingestionKey).toBe(marker);
  expect(JSON.stringify(source.toJSON())).not.toContain(marker);
  expect(seen).toHaveLength(2);
  expect(seen.map(({ url }) => url)).toEqual(["https://api.example.test/base/graphql", "https://api.example.test/base/graphql"]);
  for (const { init } of seen) {
    expect(init.method).toBe("POST");
    expect(init.credentials).toBe("include");
    expect(new Headers(init.headers).get("Content-Type")).toBe("application/json");
    const body = JSON.parse(String(init.body)) as { query: string; operationName: string; variables: unknown };
    expect(body.query).toContain(`mutation ${body.operationName}`);
    expect(body.variables).toBeTruthy();
  }
  view.unmount();
});

it("rejects HTTP and GraphQL errors without exposing response content", async () => {
  const marker = "synthetic-only-secret-marker";
  let status = 503;
  vi.stubGlobal("fetch", vi.fn(async () => status === 503
    ? new Response(marker, { status })
    : new Response(JSON.stringify({ errors: [{ message: marker }], data: null }), { status: 200 })));
  let create!: (variables: { input: { subjectID: string } }) => Promise<unknown>;
  const view = render(() => {
    create = createRelayEphemeralMutation("https://api.example.test", secretMutation("CreateCustomProviderProbe", "createCustomProvider"));
    return <span>ready</span>;
  });
  await expect(create({ input: { subjectID: "synthetic-subject" } })).rejects.toThrow("GraphQL request failed (503).");
  status = 200;
  await expect(create({ input: { subjectID: "synthetic-subject" } })).rejects.toThrow("GraphQL request failed.");
  view.unmount();
});

it("aborts a secret request when its Solid owner is disposed", async () => {
  let signal: AbortSignal | undefined;
  vi.stubGlobal("fetch", vi.fn((_url: string, init: RequestInit) => {
    signal = init.signal as AbortSignal;
    return new Promise<Response>((_resolve, reject) => {
      signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")));
    });
  }));
  let create!: (variables: { input: { subjectID: string } }) => Promise<unknown>;
  const view = render(() => {
    create = createRelayEphemeralMutation("https://api.example.test", secretMutation("CreateCustomProviderProbe", "createCustomProvider"));
    return <span>ready</span>;
  });
  const pending = create({ input: { subjectID: "synthetic-subject" } });
  view.unmount();
  expect(signal?.aborted).toBe(true);
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
});

it("keeps a credential-bearing mutation input out of Relay's record source", async () => {
  const token = "synthetic-only-provider-token";
  const source = new RecordSource();
  let transmittedToken: unknown;
  vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
    const body = JSON.parse(String(init.body)) as { variables: { input: { token: unknown } } };
    transmittedToken = body.variables.input.token;
    return new Response(JSON.stringify({ data: { connectProvider: { accepted: true } } }), { status: 200 });
  }));
  let connect!: (variables: { input: { token: string } }) => Promise<{ connectProvider: { accepted: boolean } }>;
  const view = render(() => <RelayProvider environment={unusedEnvironment(source)}>
    {(() => {
      connect = createRelayEphemeralMutation<{
        variables: { input: { token: string } };
        response: { connectProvider: { accepted: boolean } };
      }>("https://api.example.test", secretMutation("ConnectProviderProbe", "connectProvider", "accepted"));
      return <span>ready</span>;
    })()}
  </RelayProvider>);
  expect((await connect({ input: { token } })).connectProvider.accepted).toBe(true);
  expect(transmittedToken).toBe(token);
  expect(JSON.stringify(source.toJSON())).not.toContain(token);
  view.unmount();
});
