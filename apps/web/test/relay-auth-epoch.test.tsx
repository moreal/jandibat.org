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

afterEach(() => {
  cleanup();
  boundary.environment = undefined;
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("replaces a private Relay store after sign-out and again when another account signs in", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/config.json")) return Response.json({ apiBaseUrl: "" });
    return Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
    } });
  }));
  let currentAccount: string | undefined = "A";
  vi.spyOn(api, "getCurrentSession").mockImplementation(async () => {
    if (!currentAccount) throw new ApiError("unauthorized", 401);
    return auth(currentAccount);
  });
  const listSubjects = vi.spyOn(api, "listSubjects").mockResolvedValue({
    subjects: [], pageInfo: { hasNextPage: false },
  });
  vi.spyOn(api, "signOut").mockImplementation(async () => { currentAccount = undefined; });
  vi.spyOn(api, "beginPasskeyAuthentication").mockResolvedValue({
    ceremonyId: "test", expiresAt: "2026-09-25T00:00:00Z", publicKey: {},
  });
  vi.spyOn(api, "finishPasskeyAuthentication").mockImplementation(async () => {
    currentAccount = "B";
    return auth("B");
  });

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
  expect(listSubjects).toHaveBeenCalledTimes(2);
});

it("does not replace the Relay store when sign-out fails", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ apiBaseUrl: "" })));
  vi.spyOn(api, "getCurrentSession").mockResolvedValue(auth("A"));
  vi.spyOn(api, "listSubjects").mockResolvedValue({ subjects: [], pageInfo: { hasNextPage: false } });
  const signOut = vi.spyOn(api, "signOut").mockRejectedValue(new Error("offline"));

  const view = render(() => <App />);
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" })).toBeTruthy());
  const environment = boundary.environment;
  fireEvent.click(view.getByRole("button", { name: "로그아웃" }));
  await waitFor(() => expect(signOut).toHaveBeenCalledOnce());
  await waitFor(() => expect(view.getByRole("button", { name: "로그아웃" }).hasAttribute("disabled")).toBe(false));
  expect(boundary.environment).toBe(environment);
});

it("drops the private store after an anonymous auth epoch signal from another tab", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).endsWith("/config.json")
    ? Response.json({ apiBaseUrl: "" })
    : Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
    } })));
  let currentAccount = "A";
  vi.spyOn(api, "getCurrentSession").mockImplementation(async () => auth(currentAccount));
  vi.spyOn(api, "listSubjects").mockResolvedValue({ subjects: [], pageInfo: { hasNextPage: false } });

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
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ apiBaseUrl: "" })));
  vi.spyOn(api, "consumeMagicLink").mockRejectedValue(new ApiError("invalid link", 401));
  vi.spyOn(api, "getCurrentSession").mockRejectedValue(new ApiError("unauthorized", 401));

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
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => String(input).endsWith("/config.json")
    ? Response.json({ apiBaseUrl: "" })
    : Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
    } })));
  let completeExchange!: (result: AuthResultDto) => void;
  vi.spyOn(api, "consumeMagicLink").mockReturnValue(new Promise((resolve) => { completeExchange = resolve; }));
  vi.spyOn(api, "getCurrentSession").mockResolvedValue(auth("B"));
  const listSubjects = vi.spyOn(api, "listSubjects").mockResolvedValue({
    subjects: [], pageInfo: { hasNextPage: false },
  });

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
  expect(listSubjects).toHaveBeenCalledOnce();
  history.replaceState(null, "", "/");
});
