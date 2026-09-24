import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { Show } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import { api } from "../src/api/client";
import { createPasskey, getPasskey } from "../src/auth/passkey";
import { AppStateProvider } from "../src/app/state";
import { readRelayQuery } from "../src/auth/relay-query";
import { readOwnedSessions, readOwnedSubjects, SignedOutError } from "../src/auth/owner-viewer";
import { AuthPage } from "../src/pages/AuthPage";
import type { AuthViewerQuery } from "../src/pages/__generated__/AuthViewerQuery.graphql";
import viewerQuery from "../src/pages/__generated__/AuthViewerQuery.graphql";
import { AuthEpochContext, createAuthEpoch } from "../src/relay/auth-epoch";
import { RelayProvider } from "../src/relay";
import { createRelayEnvironment } from "../src/relay/environment";

vi.mock("../src/auth/passkey", () => ({
  passkeyAvailable: () => true,
  getPasskey: vi.fn(async () => ({ id: "credential-secret", response: { signature: "private-signature" } })),
  createPasskey: vi.fn(async () => ({ id: "credential-secret", response: { attestationObject: "private-key" } })),
}));

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  history.replaceState(null, "", "/");
});

function mount() {
  let epoch!: ReturnType<typeof createAuthEpoch>;
  const view = render(() => {
    epoch = createAuthEpoch("");
    return (
      <AuthEpochContext value={epoch}>
        <Show when={epoch.environment()} keyed>
          {(environment) => (
            <RelayProvider environment={environment}>
              <AppStateProvider><AuthPage /></AppStateProvider>
            </RelayProvider>
          )}
        </Show>
      </AuthEpochContext>
    );
  });
  return Object.assign(view, { getEnvironment: () => epoch.environment(), getEpoch: () => epoch });
}

it("loads the authenticated account and current-session expiry from Viewer, without REST domain calls", async () => {
  vi.spyOn(api, "getCurrentSession").mockRejectedValue(new Error("REST session called"));
  vi.spyOn(api, "listSubjects").mockRejectedValue(new Error("REST subjects called"));
  const operations: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as { operationName: string };
    operations.push(request.operationName);
    return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active",
        emailVerifiedAt: "2026-09-24T00:00:00Z" },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox",
        createdAt: "2026-09-24T00:00:00Z", expiresAt: "2026-09-25T00:00:00Z",
        revokedAt: null },
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
  }));

  const view = mount();
  await waitFor(() => expect(view.getByText("owner@example.test")).toBeTruthy());
  expect(view.getByText(/2026/)).toBeTruthy();
  expect(operations).toContain("AuthViewerQuery");
});

it("rotates the account store for passkey sign-in before the user ID is known", async () => {
  let epoch!: ReturnType<typeof createAuthEpoch>;
  const view = render(() => {
    epoch = createAuthEpoch("");
    return <p>auth epoch</p>;
  });
  const before = epoch.environment();
  epoch.beginAuthenticatedSession("passkey");
  await waitFor(() => expect(epoch.environment()).not.toBe(before));
  const after = epoch.environment();
  expect(epoch.authenticated("user-2")).toBe(false);
  expect(epoch.environment()).toBe(after);
  view.unmount();
});

it("resolves a synchronously completed Relay query without retaining its subscription", async () => {
  const environment = new Environment({
    network: Network.create(() => Observable.create((sink) => {
      sink.next({ data: { viewer: null } });
      sink.complete();
    })),
    store: new Store(new RecordSource()),
  });
  await expect(readRelayQuery<AuthViewerQuery>(environment, viewerQuery, {}))
    .resolves.toEqual({ viewer: null });
});

it("traverses session pages beyond 100 before returning the account session list", async () => {
  const requestedCursors: Array<string | null> = [];
  const environment = new Environment({
    network: Network.create((_operation, variables) => Observable.create((sink) => {
      const cursor = (variables.cursor as string | null) ?? null;
      requestedCursors.push(cursor);
      const indexes = cursor ? [100] : Array.from({ length: 100 }, (_, index) => index);
      sink.next({ data: { viewer: { sessions: {
        edges: indexes.map((index) => ({ node: { __typename: "Session", id: `session-${index}`,
          createdAt: "2026-09-24T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z",
          revokedAt: null, lastSeenAt: null } })),
        pageInfo: { hasNextPage: !cursor, endCursor: cursor ? "page-two" : "page-one" },
      } } } });
      sink.complete();
    })),
    store: new Store(new RecordSource()),
  });
  const sessions = await readOwnedSessions(environment);
  expect(sessions).toHaveLength(101);
  expect(sessions.at(-1)?.id).toBe("session-100");
  expect(requestedCursors).toEqual([null, "page-one"]);
});

it("requests a magic link through an ephemeral mutation without storing the email", async () => {
  vi.spyOn(api, "requestMagicLink").mockRejectedValue(new Error("REST magic link called"));
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    return Response.json({ data: request.operationName === "AuthRequestMagicLinkMutation"
      ? { requestMagicLink: { errors: [], accepted: true } }
      : { viewer: null } });
  }));

  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /로그인 링크 받기/ })).toBeTruthy());
  await fireEvent.input(view.getByRole("textbox", { name: "이메일 주소" }),
    { target: { value: "private@example.test" } });
  await fireEvent.submit(view.container.querySelector("form")!);
  await waitFor(() => expect(view.getByText(/이메일을 확인해 주세요/)).toBeTruthy());
  expect(requests.find(({ operationName }) => operationName === "AuthRequestMagicLinkMutation")?.variables)
    .toMatchObject({ input: { email: "private@example.test" } });
  expect(JSON.stringify(view.getEnvironment().getStore().getSource().toJSON()))
    .not.toContain("private@example.test");
});

it("treats an expired currentSession as signed out even when Viewer has a user", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ data: { viewer: {
    user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
    currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2020-01-01T00:00:00Z",
      expiresAt: "2020-01-02T00:00:00Z", revokedAt: null },
  } } })));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /로그인 링크 받기/ })).toBeTruthy());
  expect(view.queryByText("owner@example.test")).toBeNull();
});

it("does not authorize a Viewer with an invalid currentSession expiry", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ data: { viewer: {
    user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
    currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
      expiresAt: "not-a-date", revokedAt: null },
  } } })));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /로그인 링크 받기/ })).toBeTruthy());
  expect(view.queryByText("owner@example.test")).toBeNull();
});

it("shows sign-in for a stale cookie only when AuthViewerQuery returns UNAUTHENTICATED", async () => {
  const operations: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const name = (JSON.parse(String(init?.body)) as { operationName: string }).operationName;
    operations.push(name);
    return name === "AuthRequestMagicLinkMutation"
      ? Response.json({ data: { requestMagicLink: { errors: [], accepted: true } } })
      : Response.json({ errors: [{ message: "Request denied", extensions: { code: "UNAUTHENTICATED" } }],
        data: { viewer: null } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /로그인 링크 받기/ })).toBeTruthy());
  expect(view.queryByText("다시 시도")).toBeNull();
  await fireEvent.input(view.getByRole("textbox", { name: "이메일 주소" }),
    { target: { value: "owner@example.test" } });
  await fireEvent.submit(view.container.querySelector("form")!);
  await waitFor(() => expect(view.getByText(/이메일을 확인해 주세요/)).toBeTruthy());
  expect(operations).toContain("AuthRequestMagicLinkMutation");
});

it("keeps non-auth GraphQL errors on the retry path", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({
    errors: [{ message: "Request failed", extensions: { code: "INTERNAL" } }],
    data: { viewer: null },
  })));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "다시 시도" })).toBeTruthy());
  expect(view.getByRole("alert").textContent).toContain("GraphQL request failed");
});

it("classifies an UNAUTHENTICATED owner page from the production Relay environment", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({
    errors: [{ message: "Request denied", extensions: { code: "UNAUTHENTICATED" } }],
    data: { viewer: null },
  })));
  await expect(readOwnedSubjects(createRelayEnvironment(""))).rejects.toBeInstanceOf(SignedOutError);
  await expect(readOwnedSessions(createRelayEnvironment(""))).rejects.toBeInstanceOf(SignedOutError);
});

it("does not finish a pending passkey sign-in after the auth route unmounts", async () => {
  let releaseCredential!: (value: unknown) => void;
  vi.mocked(getPasskey).mockImplementationOnce(() => new Promise((resolve) => {
    releaseCredential = resolve as (value: unknown) => void;
  }));
  const operations: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const name = (JSON.parse(String(init?.body)) as { operationName: string }).operationName;
    operations.push(name);
    if (name === "AuthBeginPasskeySignInMutation") return Response.json({ data: {
      beginPasskeySignIn: { errors: [], options: { ceremonyID: "ceremony-secret", publicKeyJSON: "{}",
        expiresAt: "2099-09-25T00:00:00Z" } },
    } });
    return Response.json({ data: { viewer: null } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /Passkey로 로그인/ })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: /Passkey로 로그인/ }));
  await waitFor(() => expect(releaseCredential).toBeTypeOf("function"));
  const before = view.getEnvironment();
  view.unmount();
  releaseCredential({ id: "late-secret", response: { signature: "late-signature" } });
  await Promise.resolve();
  await Promise.resolve();
  expect(operations).not.toContain("AuthFinishPasskeySignInMutation");
  expect(view.getEnvironment()).toBe(before);
});

it("does not finish pending passkey registration after an auth epoch switch", async () => {
  let releaseCredential!: (value: unknown) => void;
  vi.mocked(createPasskey).mockImplementationOnce(() => new Promise((resolve) => {
    releaseCredential = resolve as (value: unknown) => void;
  }));
  const operations: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const name = (JSON.parse(String(init?.body)) as { operationName: string }).operationName;
    operations.push(name);
    if (name === "AuthBeginPasskeyRegistrationMutation") return Response.json({ data: {
      beginPasskeyRegistration: { errors: [], options: { ceremonyID: "ceremony-secret", publicKeyJSON: "{}",
        expiresAt: "2099-09-25T00:00:00Z" } },
    } });
    if (name === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    if (name === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "이 기기에 Passkey 등록" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "이 기기에 Passkey 등록" }));
  await waitFor(() => expect(releaseCredential).toBeTypeOf("function"));
  const before = view.getEnvironment();
  view.getEpoch().beginAuthenticatedSession("passkey");
  await waitFor(() => expect(view.getEnvironment()).not.toBe(before));
  releaseCredential({ id: "late-secret", response: { attestationObject: "late-key" } });
  await Promise.resolve();
  await Promise.resolve();
  expect(operations).not.toContain("AuthFinishPasskeyRegistrationMutation");
});

it("uses ephemeral passkey mutations and rotates the store before fetching a session-only sign-in result", async () => {
  vi.spyOn(api, "beginPasskeyAuthentication").mockRejectedValue(new Error("REST passkey called"));
  vi.spyOn(api, "finishPasskeyAuthentication").mockRejectedValue(new Error("REST passkey called"));
  let signedIn = false;
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    if (request.operationName === "AuthBeginPasskeySignInMutation") return Response.json({ data: {
      beginPasskeySignIn: { errors: [], options: { ceremonyID: "ceremony-secret",
        publicKeyJSON: "{}", expiresAt: "2099-09-25T00:00:00Z" } },
    } });
    if (request.operationName === "AuthFinishPasskeySignInMutation") {
      signedIn = true;
      return Response.json({ data: { finishPasskeySignIn: { errors: [], session: {
        __typename: "Session", id: "U2Vzc2lvbjox", expiresAt: "2099-09-25T00:00:00Z",
      } } } });
    }
    if (request.operationName === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    if (request.operationName === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    return Response.json({ data: { viewer: signedIn ? {
      user: { id: "user-2", primaryEmail: "second@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } : null } });
  }));

  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /Passkey로 로그인/ })).toBeTruthy());
  const anonymousStore = view.getEnvironment();
  await fireEvent.click(view.getByRole("button", { name: /Passkey로 로그인/ }));
  await waitFor(() => expect(view.getByText("second@example.test")).toBeTruthy());
  expect(view.getEnvironment()).not.toBe(anonymousStore);
  const finish = requests.find(({ operationName }) => operationName === "AuthFinishPasskeySignInMutation");
  expect(finish?.variables.input).toMatchObject({ ceremonyID: "ceremony-secret" });
  expect(JSON.stringify(finish?.variables)).toContain("private-signature");
  expect(JSON.stringify(view.getEnvironment().getStore().getSource().toJSON()))
    .not.toMatch(/private-signature|ceremony-secret|credential-secret/);
});

it("signs out through Relay and drops the old normalized account store", async () => {
  vi.spyOn(api, "signOut").mockRejectedValue(new Error("REST sign-out called"));
  let signedIn = true;
  const names: string[] = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as { operationName: string };
    names.push(request.operationName);
    if (request.operationName === "AuthSignOutMutation") {
      signedIn = false;
      return Response.json({ data: { signOut: { errors: [], session: {
        __typename: "Session", id: "U2Vzc2lvbjox", revokedAt: "2026-09-24T01:00:00Z",
      } } } });
    }
    if (request.operationName === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    if (request.operationName === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    return Response.json({ data: { viewer: signedIn ? {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } : null } });
  }));

  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" })).toBeTruthy());
  const before = view.getEnvironment();
  await fireEvent.click(view.getByRole("button", { name: "로그아웃" }));
  await waitFor(() => expect(view.getByRole("button", { name: /로그인 링크 받기/ })).toBeTruthy());
  expect(names).toContain("AuthSignOutMutation");
  expect(view.getEnvironment()).not.toBe(before);
  expect(JSON.stringify(view.getEnvironment().getStore().getSource().toJSON()))
    .not.toContain("owner@example.test");
});

it("revokes a noncurrent session by Relay ID and removes it from the account list", async () => {
  const revokedID = "U2Vzc2lvbjoy";
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    if (request.operationName === "AuthRevokeSessionMutation") return Response.json({ data: {
      revokeSession: { errors: [], session: { __typename: "Session", id: revokedID,
        revokedAt: "2026-09-24T01:00:00Z" } },
    } });
    if (request.operationName === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    if (request.operationName === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [{ node: { __typename: "Session", id: "U2Vzc2lvbjox",
        createdAt: "2026-09-24T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z",
        revokedAt: null, lastSeenAt: null } }, { node: { __typename: "Session", id: revokedID,
        createdAt: "2026-09-23T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z",
        revokedAt: null, lastSeenAt: null } }],
        pageInfo: { hasNextPage: false, endCursor: "end" } },
    } } });
    return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
  }));

  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "다른 세션 해지" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "다른 세션 해지" }));
  await waitFor(() => expect(view.queryByRole("button", { name: "다른 세션 해지" })).toBeNull());
  expect(requests.find(({ operationName }) => operationName === "AuthRevokeSessionMutation")?.variables)
    .toEqual({ input: { id: revokedID } });
});

it("registers a passkey with ephemeral ceremony and credential mutations", async () => {
  vi.spyOn(api, "beginPasskeyRegistration").mockRejectedValue(new Error("REST registration called"));
  vi.spyOn(api, "finishPasskeyRegistration").mockRejectedValue(new Error("REST registration called"));
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    if (request.operationName === "AuthBeginPasskeyRegistrationMutation") return Response.json({ data: {
      beginPasskeyRegistration: { errors: [], options: { ceremonyID: "registration-secret",
        publicKeyJSON: "{}", expiresAt: "2099-09-25T00:00:00Z" } },
    } });
    if (request.operationName === "AuthFinishPasskeyRegistrationMutation") return Response.json({ data: {
      finishPasskeyRegistration: { errors: [], credential: { id: "key-1", label: "이 기기",
        createdAt: "2026-09-24T00:00:00Z" } },
    } });
    if (request.operationName === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    if (request.operationName === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "이 기기에 Passkey 등록" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "이 기기에 Passkey 등록" }));
  await waitFor(() => expect(requests.some(({ operationName }) =>
    operationName === "AuthFinishPasskeyRegistrationMutation")).toBe(true));
  const finish = requests.find(({ operationName }) => operationName === "AuthFinishPasskeyRegistrationMutation");
  expect(finish?.variables.input).toMatchObject({ ceremonyID: "registration-secret", label: "이 기기" });
  expect(JSON.stringify(finish?.variables)).toContain("private-key");
  expect(JSON.stringify(view.getEnvironment().getStore().getSource().toJSON()))
    .not.toMatch(/private-key|registration-secret|credential-secret/);
});

it("requests selected subject deletion by Relay ID instead of a REST handle", async () => {
  vi.spyOn(api, "requestSubjectDeletion").mockRejectedValue(new Error("REST deletion called"));
  vi.stubGlobal("confirm", () => true);
  const subjectID = "U3ViamVjdDox";
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    if (request.operationName === "AuthRequestSubjectDeletionMutation") return Response.json({ data: {
      requestSubjectDeletion: { errors: [], request: { requestID: "deletion-1", status: "pending" } },
    } });
    if (request.operationName === "AuthSubjectsQuery") return Response.json({ data: { viewer: {
      subjects: { edges: [{ node: { __typename: "Subject", id: subjectID,
        handle: "garden", displayName: "Garden", timezone: "UTC", isPublic: true,
        createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z" } }],
        pageInfo: { hasNextPage: false, endCursor: "end" } },
    } } });
    if (request.operationName === "AuthSessionsQuery") return Response.json({ data: { viewer: {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
    return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: "선택한 잔디밭 삭제 요청" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "선택한 잔디밭 삭제 요청" }));
  await waitFor(() => expect(view.getByRole("button", { name: "삭제 요청됨" })).toBeTruthy());
  expect(requests.find(({ operationName }) => operationName === "AuthRequestSubjectDeletionMutation")?.variables)
    .toEqual({ input: { subjectID } });
});
