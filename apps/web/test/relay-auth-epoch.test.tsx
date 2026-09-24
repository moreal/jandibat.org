import type { AuthResultDto } from "@jandibat/contracts";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { createEffect } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, fetchQuery } from "relay-runtime";
import App from "../src/App";
import { api, ApiError } from "../src/api/client";
import query from "./__generated__/RelayCompatQuery.graphql";

const boundary = vi.hoisted(() => ({ environment: undefined as Environment | undefined }));

vi.mock("@solidjs/router", () => ({ useLocation: () => ({ pathname: "/auth" }) }));
vi.mock("../src/auth/passkey", () => ({
  passkeyAvailable: () => true,
  getPasskey: async () => ({}),
  createPasskey: async () => ({}),
}));
vi.mock("../src/router", async () => {
  const { AuthPage } = await import("../src/pages/AuthPage");
  const { useAppState } = await import("../src/app/state");
  const { useRelayEnvironment } = await import("solid-relay");
  return {
    Router: () => {
      const environment = useRelayEnvironment();
      const app = useAppState();
      createEffect(environment, (value) => { boundary.environment = value as Environment; });
      return <><AuthPage /><p data-testid="toast">{app.toast()?.message}</p></>;
    },
  };
});

const subjectID = "U3ViamVjdDox";

function auth(userID: string): AuthResultDto {
  return {
    user: {
      id: userID,
      primaryEmail: `${userID}@example.test`,
      emailVerifiedAt: "2026-09-24T00:00:00Z",
      status: "active",
      createdAt: "2026-09-24T00:00:00Z",
      updatedAt: "2026-09-24T00:00:00Z",
    },
    session: {
      id: `session-${userID}`,
      userId: userID,
      current: true,
      createdAt: "2026-09-24T00:00:00Z",
      expiresAt: "2026-09-25T00:00:00Z",
    },
  };
}

function seedPrivateSubject(environment: Environment): Promise<void> {
  return new Promise((resolve, reject) => {
    fetchQuery(environment, query, { id: subjectID }).subscribe({
      next: () => resolve(),
      error: reject,
    });
  });
}

function installGraphQLSession(
  account: () => string | undefined,
  changeAccount?: (value: string | undefined) => void,
  failSignOut = false,
) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/config.json")) return Response.json({ apiBaseUrl: "" });
    const name = (JSON.parse(String(init?.body)) as { operationName: string }).operationName;
    if (name === "RelayCompatQuery") return Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
    } });
    if (name === "AuthSubjectsQuery") return Response.json({ data: { viewer: account() ? {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } : null } });
    if (name === "AuthSessionsQuery") return Response.json({ data: { viewer: account() ? {
      sessions: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } : null } });
    if (name === "AuthSignOutMutation") {
      if (failSignOut) return Response.json({ errors: [{ message: "offline" }] }, { status: 503 });
      changeAccount?.(undefined);
      return Response.json({ data: { signOut: { errors: [], session: {
        __typename: "Session", id: "session-A", revokedAt: "2026-09-24T01:00:00Z",
      } } } });
    }
    if (name === "AuthBeginPasskeySignInMutation") return Response.json({ data: {
      beginPasskeySignIn: { errors: [], options: { ceremonyID: "ceremony-1", publicKeyJSON: "{}",
        expiresAt: "2099-09-25T00:00:00Z" } },
    } });
    if (name === "AuthFinishPasskeySignInMutation") {
      changeAccount?.("B");
      return Response.json({ data: { finishPasskeySignIn: { errors: [], session: {
        __typename: "Session", id: "session-B", expiresAt: "2099-09-25T00:00:00Z",
      } } } });
    }
    const currentAccount = account();
    return Response.json({ data: { viewer: currentAccount ? {
      user: { id: currentAccount, primaryEmail: `${currentAccount}@example.test`,
        status: "active", emailVerifiedAt: "2026-09-24T00:00:00Z" },
      currentSession: { __typename: "Session", id: `session-${currentAccount}`,
        createdAt: "2026-09-24T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } : null } });
  }));
}

afterEach(() => {
  cleanup();
  boundary.environment = undefined;
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("replaces a private Relay store after sign-out and again when another account signs in", async () => {
  let currentAccount: string | undefined = "A";
  installGraphQLSession(() => currentAccount, (value) => { currentAccount = value; });

  const view = render(() => <App />);
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" })).toBeTruthy());
  const accountAEnvironment = boundary.environment!;
  await seedPrivateSubject(accountAEnvironment);
  expect(accountAEnvironment.getStore().getSource().get(subjectID)).toBeDefined();

  fireEvent.click(view.getByRole("button", { name: "로그아웃" }));
  await waitFor(() => expect(view.getByRole("button", { name: /Passkey로 로그인/ })).toBeTruthy());
  await waitFor(() => expect(boundary.environment).not.toBe(accountAEnvironment));
  await waitFor(() => expect(view.getByTestId("toast").textContent).toBe("안전하게 로그아웃했습니다."));
  const signedOutEnvironment = boundary.environment!;
  expect(signedOutEnvironment.getStore().getSource().get(subjectID)).toBeUndefined();

  fireEvent.click(view.getByRole("button", { name: /Passkey로 로그인/ }));
  await waitFor(() => expect(view.getByText("B@example.test")).toBeTruthy());
  await waitFor(() => expect(boundary.environment).not.toBe(signedOutEnvironment));
  await waitFor(() => expect(view.getByTestId("toast").textContent).toBe("Passkey로 로그인했습니다."));
  expect(boundary.environment!.getStore().getSource().get(subjectID)).toBeUndefined();
});

it("does not replace the Relay store when sign-out fails", async () => {
  installGraphQLSession(() => "A", undefined, true);

  const view = render(() => <App />);
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" })).toBeTruthy());
  const environment = boundary.environment;
  fireEvent.click(view.getByRole("button", { name: "로그아웃" }));
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" }).hasAttribute("disabled")).toBe(false));
  expect(boundary.environment).toBe(environment);
});

it("drops the private store after an anonymous auth epoch signal from another tab", async () => {
  let currentAccount = "A";
  installGraphQLSession(() => currentAccount);

  const view = render(() => <App />);
  await waitFor(() => expect(view.getByText("A@example.test")).toBeTruthy());
  const oldEnvironment = boundary.environment!;
  await seedPrivateSubject(oldEnvironment);
  expect(oldEnvironment.getStore().getSource().get(subjectID)).toBeDefined();

  currentAccount = "B";
  window.dispatchEvent(new StorageEvent("storage", {
    key: "jandibat:auth-epoch", newValue: "anonymous-signal-from-another-tab",
  }));
  await waitFor(() => expect(view.getByText("B@example.test")).toBeTruthy());
  expect(boundary.environment).not.toBe(oldEnvironment);
  expect(boundary.environment!.getStore().getSource().get(subjectID)).toBeUndefined();
});

it("does not rotate the store when a magic-link exchange fails", async () => {
  const token = "a".repeat(32);
  history.replaceState(null, "", `/#auth?token=${token}`);
  installGraphQLSession(() => undefined);
  vi.spyOn(api, "consumeMagicLink").mockRejectedValue(new ApiError("invalid link", 401));

  const view = render(() => <App />);
  await waitFor(() => expect(view.getByText("invalid link")).toBeTruthy());
  expect(boundary.environment).toBeInstanceOf(Environment);
  expect(localStorage.getItem("jandibat:auth-epoch")).toBeNull();
  expect(location.href).not.toContain(token);
  history.replaceState(null, "", "/");
});

it("discards an existing private store immediately after a magic-link exchange succeeds", async () => {
  const token = "b".repeat(32);
  history.replaceState(null, "", `/#auth?token=${token}`);
  installGraphQLSession(() => "B");
  let completeExchange!: (result: AuthResultDto) => void;
  vi.spyOn(api, "consumeMagicLink").mockReturnValue(new Promise((resolve) => { completeExchange = resolve; }));

  const view = render(() => <App />);
  await waitFor(() => expect(completeExchange).toBeTypeOf("function"));
  await waitFor(() => expect(boundary.environment).toBeInstanceOf(Environment));
  const oldEnvironment = boundary.environment!;
  await seedPrivateSubject(oldEnvironment);
  expect(oldEnvironment.getStore().getSource().get(subjectID)).toBeDefined();

  completeExchange(auth("B"));
  await waitFor(() => expect(view.getByText("B@example.test")).toBeTruthy());
  await waitFor(() => expect(view.getByTestId("toast").textContent).toBe("이메일을 확인하고 로그인했습니다."));
  expect(boundary.environment).not.toBe(oldEnvironment);
  expect(boundary.environment!.getStore().getSource().get(subjectID)).toBeUndefined();
  expect(location.href).not.toContain(token);
  history.replaceState(null, "", "/");
});
