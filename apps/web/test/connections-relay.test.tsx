import type { SubjectDto } from "@jandibat/contracts";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import { api } from "../src/api/client";
import { AppStateProvider, useAppState } from "../src/app/state";
import { ConnectionsPage } from "../src/pages/ConnectionsPage";
import { AuthEpochContext } from "../src/relay/auth-epoch";
import { RelayProvider } from "../src/relay";

const subjectID = "U3ViamVjdDox";
const connectionID = "UHJvdmlkZXJDb25uZWN0aW9uOjE=";
const owner: SubjectDto = {
  id: subjectID, handle: "garden", displayName: "Garden", timezone: "UTC", isPublic: true,
  createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z",
};

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete window.__JANDIBAT_CONFIG__;
  history.replaceState(null, "", "/");
});

function ToastProbe() {
  const app = useAppState();
  return <span data-testid="app-toast">{app.toast()?.message}</span>;
}

type ResponseFixture = { data: Record<string, unknown> } | Error;
function mount(
  respond: (name: string, variables: Record<string, unknown>) => ResponseFixture | Promise<ResponseFixture>,
  ownedSubjects: SubjectDto[] = [owner],
) {
  vi.spyOn(api, "getCurrentSession").mockRejectedValue(new Error("REST session called"));
  vi.spyOn(api, "listSubjects").mockRejectedValue(new Error("REST subjects called"));
  // A route using the old REST domain client must fail this test.
  vi.spyOn(api, "listProviderCatalog").mockRejectedValue(new Error("REST provider catalog called"));
  vi.spyOn(api, "listConnections").mockRejectedValue(new Error("REST connections called"));
  const operations: Array<{ name: string; variables: Record<string, unknown> }> = [];
  const environment = new Environment({
    network: Network.create((operation, variables) => Observable.create((sink) => {
      if (operation.name === "AuthViewerQuery") {
        sink.next({ data: { viewer: { user: { id: "user-1", primaryEmail: "owner@example.test",
          status: "active", emailVerifiedAt: null }, currentSession: { __typename: "Session",
          id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z",
          revokedAt: null } } } });
        sink.complete();
        return;
      }
      if (operation.name === "AuthSubjectsQuery") {
        sink.next({ data: { viewer: { subjects: { edges: ownedSubjects.map((subject) => ({ node: {
          __typename: "Subject", ...subject,
        } })), pageInfo: { hasNextPage: false, endCursor: null } } } } });
        sink.complete();
        return;
      }
      operations.push({ name: operation.name, variables });
      const deliver = (result: ResponseFixture) => {
        if (result instanceof Error) sink.error(result);
        else { sink.next(result); sink.complete(); }
      };
      const result = respond(operation.name, variables);
      if (result instanceof Promise) void result.then(deliver, sink.error.bind(sink));
      else deliver(result);
    })),
    store: new Store(new RecordSource()),
  });
  const view = render(() => (
    <AuthEpochContext value={{ environment: () => environment, beginAuthenticatedSession: () => undefined, authenticated: () => false, signedOut: () => false, takeNotice: () => undefined }}>
      <RelayProvider environment={environment}>
        <AppStateProvider><ToastProbe /><ConnectionsPage /></AppStateProvider>
      </RelayProvider>
    </AuthEpochContext>
  ));
  return { view, environment, operations };
}

function connection(status = "active") {
  return { __typename: "ProviderConnection", id: connectionID, providerID: "github", authMethod: "public", status,
    privateDataEnabled: false, lastSyncedAt: null };
}

function initialData(edges: unknown[] = []) {
  return { data: { providerCatalog: [{
    id: "github", name: "GitHub", description: "Code hosting", kind: "builtin", category: "git-hosting",
    supportsOAuth: true, supportsToken: true, supportsPrivateData: true,
  }], subject: { __typename: "Subject", id: subjectID, providerConnections: {
    edges, pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
  } } } };
}

it("loads owner-only provider cards from a Relay catalog and connection page, not REST", async () => {
  const { view, operations } = mount(() => initialData([{ cursor: "cursor-1", node: connection() }]));
  await waitFor(() => expect(view.getByText("GitHub")).toBeTruthy());
  expect(view.getByText("연결됨")).toBeTruthy();
  expect(operations.map(({ name }) => name)).toEqual(["ConnectionsQuery"]);
});

it("connects with a token outside the Node store and refreshes one connection edge", async () => {
  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/prefix" };
  let connected = false;
  let credentialEndpoint: string | undefined;
  let credentialRequest: { operationName: string; variables: Record<string, unknown> } | undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, options?: RequestInit) => {
    credentialEndpoint = String(input);
    credentialRequest = JSON.parse(String(options?.body)) as typeof credentialRequest;
    connected = true;
    return Response.json({ data: { connectProvider: {
      errors: [], authorizationURL: null, connection: connection(),
    } } });
  }));
  const { view, environment, operations } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData();
    if (name === "ConnectionsSubjectRefetchQuery" && connected) {
      return { data: { node: { __typename: "Subject", id: subjectID, providerConnections: {
        edges: [{ cursor: "cursor-1", node: connection() }],
        pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: "cursor-1", endCursor: "cursor-1" },
      } } } };
    }
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결/ })).toBeTruthy());
  const method = view.getByRole("combobox", { name: "GitHub 연결 방식" });
  await fireEvent.change(method, { target: { value: "TOKEN" } });
  const token = view.container.querySelector<HTMLInputElement>('input[name="token"]')!;
  await fireEvent.input(token, { target: { value: "test-token-value" } });
  const consent = view.getByRole("checkbox", { name: /비공개 활동 포함에 동의합니다/ });
  await fireEvent.click(consent);
  await fireEvent.submit(view.container.querySelector(".provider-connect-form")!);
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy());
  expect(operations.map(({ name }) => name)).toEqual(["ConnectionsQuery", "ConnectionsSubjectRefetchQuery"]);
  expect(credentialEndpoint).toBe("https://api.example.test/prefix/graphql");
  expect(credentialRequest?.operationName).toBe("ConnectionsConnectProviderMutation");
  const mutationInput = credentialRequest?.variables.input as Record<string, unknown>;
  expect(mutationInput).toMatchObject({ subjectID, providerID: "github", authMethod: "TOKEN", includePrivate: true });
  expect(mutationInput.token).toBe("test-token-value");
  const tokenPersisted = JSON.stringify(environment.getStore().getSource().toJSON()).includes("test-token-value");
  expect(tokenPersisted).toBe(false);
  expect(view.container.querySelectorAll(".provider-card")).toHaveLength(1);
});

it("keeps owner-only cards hidden when the GraphQL subject connection is null", async () => {
  const { view } = mount(() => ({ data: { providerCatalog: initialData().data.providerCatalog,
    subject: { __typename: "Subject", id: subjectID, providerConnections: null } } }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("연결 정보를 볼 수 없습니다"));
  expect(view.container.querySelector(".provider-card")).toBeNull();
});

it("revokes a connection by Relay ID and refreshes without a stale connected card", async () => {
  vi.stubGlobal("confirm", () => true);
  const { view, operations } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (name === "ConnectionsRevokeProviderMutation") return { data: {
      revokeProviderConnection: { errors: [], revokedConnectionID: connectionID },
    } };
    if (name === "ConnectionsSubjectRefetchQuery") return { data: { node: {
      __typename: "Subject", id: subjectID, providerConnections: {
        edges: [], pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
      },
    } } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "GitHub 연결 해제" }));
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
  expect(view.queryByRole("button", { name: "GitHub 연결 해제" })).toBeNull();
  expect(operations.map(({ name }) => name)).toEqual([
    "ConnectionsQuery", "ConnectionsRevokeProviderMutation", "ConnectionsSubjectRefetchQuery",
  ]);
  expect(operations[1].variables).toEqual({ input: { id: connectionID } });
  vi.unstubAllGlobals();
});

it("does not turn a typed revoke error into a removed card", async () => {
  vi.stubGlobal("confirm", () => true);
  const { view, operations } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (name === "ConnectionsRevokeProviderMutation") return { data: {
      revokeProviderConnection: { errors: [{ code: "CONFLICT", message: "Still busy", field: null }], revokedConnectionID: null },
    } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "GitHub 연결 해제" }));
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" }).hasAttribute("disabled")).toBe(false));
  expect(operations.map(({ name }) => name)).toEqual(["ConnectionsQuery", "ConnectionsRevokeProviderMutation"]);
  vi.unstubAllGlobals();
});

it("enqueues one manual sync with a fresh idempotency key", async () => {
  const { view, operations } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (name === "ConnectionsEnqueueSyncMutation") return { data: { enqueueManualSync: {
      errors: [], job: { __typename: "SyncJob", id: "U3luY0pvYjox", status: "queued" },
    } } };
    if (name === "ConnectionsSubjectRefetchQuery") return { data: { node: {
      __typename: "Subject", id: subjectID, providerConnections: {
        edges: [{ cursor: "cursor-1", node: connection() }],
        pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: "cursor-1", endCursor: "cursor-1" },
      },
    } } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "동기화" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "동기화" }));
  await waitFor(() => expect(operations.some(({ name }) => name === "ConnectionsSubjectRefetchQuery")).toBe(true));
  const input = operations.find(({ name }) => name === "ConnectionsEnqueueSyncMutation")!.variables.input as Record<string, unknown>;
  expect(input.connectionID).toBe(connectionID);
  expect(typeof input.idempotencyKey).toBe("string");
  expect((input.idempotencyKey as string).length).toBeGreaterThanOrEqual(8);
});

it("loads the next owner-only connection page without duplicating edges", async () => {
  const secondID = "UHJvdmlkZXJDb25uZWN0aW9uOjI=";
  const { view, operations } = mount((name, variables) => {
    const catalog = [initialData().data.providerCatalog[0], {
      id: "gitlab", name: "GitLab", description: "Code hosting", kind: "builtin", category: "git-hosting",
      supportsOAuth: true, supportsToken: true, supportsPrivateData: false,
    }];
    if (name === "ConnectionsQuery") return { data: { providerCatalog: catalog, subject: {
      __typename: "Subject", id: subjectID, providerConnections: {
        edges: [{ cursor: "cursor-1", node: connection() }],
        pageInfo: { hasNextPage: true, hasPreviousPage: false, startCursor: "cursor-1", endCursor: "cursor-1" },
      },
    } } };
    if (name === "ConnectionsSubjectRefetchQuery" && variables.cursor === "cursor-1") return { data: { node: {
      __typename: "Subject", id: subjectID, providerConnections: {
        edges: [{ cursor: "cursor-2", node: { ...connection(), id: secondID, providerID: "gitlab" } }],
        pageInfo: { hasNextPage: false, hasPreviousPage: true, startCursor: "cursor-2", endCursor: "cursor-2" },
      },
    } } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "연결 더 보기" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "연결 더 보기" }));
  await waitFor(() => expect(view.getByRole("button", { name: "GitLab 연결 해제" })).toBeTruthy());
  expect(view.container.querySelectorAll(".provider-card.is-connected")).toHaveLength(2);
  expect(view.queryByRole("button", { name: "연결 더 보기" })).toBeNull();
  expect(operations.map(({ name }) => name)).toEqual(["ConnectionsQuery", "ConnectionsSubjectRefetchQuery"]);
  expect(operations[1].variables.cursor).toBe("cursor-1");
});

it("surfaces a failed connection refetch and retries without a stale card", async () => {
  let refetches = 0;
  const { view, operations } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (name === "ConnectionsEnqueueSyncMutation") return { data: { enqueueManualSync: {
      errors: [], job: { __typename: "SyncJob", id: "U3luY0pvYjox", status: "queued" },
    } } };
    if (name === "ConnectionsSubjectRefetchQuery") {
      refetches += 1;
      return refetches === 1 ? new Error("refetch offline") : { data: { node: {
        __typename: "Subject", id: subjectID, providerConnections: {
          edges: [], pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
        },
      } } };
    }
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "동기화" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "동기화" }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("refetch offline"));
  await fireEvent.click(view.getByRole("button", { name: "다시 시도" }));
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
  expect(operations.map(({ name }) => name)).toEqual([
    "ConnectionsQuery", "ConnectionsEnqueueSyncMutation", "ConnectionsSubjectRefetchQuery", "ConnectionsSubjectRefetchQuery",
  ]);
});

it("hides a revoked card when its post-mutation refetch fails", async () => {
  vi.stubGlobal("confirm", () => true);
  let refetches = 0;
  const { view } = mount((name) => {
    if (name === "ConnectionsQuery") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (name === "ConnectionsRevokeProviderMutation") return { data: {
      revokeProviderConnection: { errors: [], revokedConnectionID: connectionID },
    } };
    if (name === "ConnectionsSubjectRefetchQuery") {
      refetches += 1;
      return refetches === 1 ? new Error("refetch offline") : { data: { node: {
        __typename: "Subject", id: subjectID, providerConnections: {
          edges: [], pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
        },
      } } };
    }
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "GitHub 연결 해제" }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("refetch offline"));
  expect(view.queryByRole("button", { name: "GitHub 연결 해제" })).toBeNull();
  await fireEvent.click(view.getByRole("button", { name: "다시 시도" }));
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
});

it("never shows the prior owner's connection while switching subjects", async () => {
  const secondOwner = { ...owner, id: "U3ViamVjdDoy", handle: "second", displayName: "Second" };
  localStorage.setItem("jandibat:owner-subject", "garden");
  let releaseSecond!: (value: ResponseFixture) => void;
  const pendingSecond = new Promise<ResponseFixture>((resolve) => { releaseSecond = resolve; });
  const { view, operations } = mount((name, variables) => {
    if (name !== "ConnectionsQuery") throw new Error(`Unexpected operation ${name}`);
    if (variables.subject === "garden") return initialData([{ cursor: "cursor-1", node: connection() }]);
    if (variables.subject === "second") return pendingSecond;
    throw new Error(`Unexpected subject ${String(variables.subject)}`);
  }, [owner, secondOwner]);
  await waitFor(() => expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "second" } });
  await fireEvent.submit(view.container.querySelector("form.connection-toolbar")!);
  await waitFor(() => expect(operations.some(({ variables }) => variables.subject === "second")).toBe(true));
  expect(view.queryByRole("button", { name: "GitHub 연결 해제" })).toBeNull();
  releaseSecond({ data: { providerCatalog: initialData().data.providerCatalog,
    subject: { __typename: "Subject", id: secondOwner.id, providerConnections: {
      edges: [], pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
    } },
  } });
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
  expect(view.queryByRole("button", { name: "GitHub 연결 해제" })).toBeNull();
});

it("keeps the first page visible and retries a failed loadNext page", async () => {
  let pageRequests = 0;
  const secondID = "UHJvdmlkZXJDb25uZWN0aW9uOjI=";
  const { view } = mount((name) => {
    if (name === "ConnectionsQuery") return { data: { providerCatalog: [
      initialData().data.providerCatalog[0],
      { id: "gitlab", name: "GitLab", description: "Code hosting", kind: "builtin", category: "git-hosting",
        supportsOAuth: true, supportsToken: true, supportsPrivateData: false },
    ], subject: { __typename: "Subject", id: subjectID, providerConnections: {
      edges: [{ cursor: "cursor-1", node: connection() }],
      pageInfo: { hasNextPage: true, hasPreviousPage: false, startCursor: "cursor-1", endCursor: "cursor-1" },
    } } } };
    if (name === "ConnectionsSubjectRefetchQuery") {
      pageRequests += 1;
      return pageRequests === 1 ? new Error("page offline") : { data: { node: {
        __typename: "Subject", id: subjectID, providerConnections: {
          edges: [{ cursor: "cursor-2", node: { ...connection(), id: secondID, providerID: "gitlab" } }],
          pageInfo: { hasNextPage: false, hasPreviousPage: true, startCursor: "cursor-2", endCursor: "cursor-2" },
        },
      } } };
    }
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "연결 더 보기" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "연결 더 보기" }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("page offline"));
  expect(view.getByRole("button", { name: "GitHub 연결 해제" })).toBeTruthy();
  await fireEvent.click(view.getByRole("button", { name: "다시 시도" }));
  await waitFor(() => expect(view.getByRole("button", { name: "GitLab 연결 해제" })).toBeTruthy());
  expect(view.container.querySelectorAll(".provider-card.is-connected")).toHaveLength(2);
});

it("treats an unknown connection status as an error without adding its text to CSS classes", async () => {
  const { view } = mount(() => initialData([{ cursor: "cursor-1", node: connection("unexpected malicious") }]));
  await waitFor(() => expect(view.getByText("확인 필요")).toBeTruthy());
  const pill = view.container.querySelector(".status-pill");
  expect(pill?.className).toBe("status-pill status-error");
});

it("shows a recoverable empty state when the built-in catalog is empty", async () => {
  const { view } = mount(() => ({ data: {
    providerCatalog: [], subject: { __typename: "Subject", id: subjectID, providerConnections: {
      edges: [], pageInfo: { hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null },
    } },
  } }));
  await waitFor(() => expect(view.getByText("연결할 수 있는 Provider가 없어요.")).toBeTruthy());
  expect(view.getByRole("button", { name: "다시 시도" })).toBeTruthy();
});

it("retries a failed GraphQL catalog request without falling back to REST", async () => {
  let attempts = 0;
  const { view, operations } = mount(() => {
    attempts += 1;
    return attempts === 1 ? new Error("offline") : initialData();
  });
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("offline"));
  await fireEvent.click(view.getByRole("button", { name: "다시 시도" }));
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
  expect(operations.map(({ name }) => name)).toEqual(["ConnectionsQuery", "ConnectionsQuery"]);
});

it("scrubs an OAuth callback marker immediately but announces success only after the owner query succeeds", async () => {
  history.replaceState(null, "", "/?status=connected#connections");
  let attempts = 0;
  const { view } = mount(() => {
    attempts += 1;
    return attempts === 1 ? new Error("offline") : initialData();
  });
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("offline"));
  expect(location.search).toBe("");
  expect(view.getByTestId("app-toast").textContent).toBe("");
  await fireEvent.click(view.getByRole("button", { name: "다시 시도" }));
  await waitFor(() => expect(view.getByRole("button", { name: /GitHub 연결$/ })).toBeTruthy());
  expect(view.getByTestId("app-toast").textContent).toBe("Provider 연결을 완료했습니다.");
});
