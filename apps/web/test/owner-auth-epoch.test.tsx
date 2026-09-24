import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { Show, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, fetchQuery } from "relay-runtime";
import { AppStateProvider } from "../src/app/state";
import { AuthEpochContext, createAuthEpoch } from "../src/relay/auth-epoch";
import { RelayProvider } from "../src/relay";
import { OwnerGate } from "../src/subjects/OwnerGate";
import query from "./__generated__/RelayCompatQuery.graphql";

const subjectID = "U3ViamVjdDox";

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("OwnerGate revalidation drops account A's private Relay store for B and on 401", async () => {
  let currentAccount: string | undefined = "A";
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const name = (JSON.parse(String(init?.body)) as { operationName: string }).operationName;
    if (name === "RelayCompatQuery") return Response.json({ data: {
      subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
    } });
    if (name === "AuthViewerQuery") return Response.json({ data: { viewer: currentAccount ? {
      user: { id: currentAccount, primaryEmail: `${currentAccount}@example.test`,
        status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: `session-${currentAccount}`,
        createdAt: "2026-09-24T00:00:00Z", expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } : null } });
    return Response.json({ data: { viewer: currentAccount ? { subjects: {
      edges: [{ node: { __typename: "Subject", id: currentAccount === "B" ? "U3ViamVjdDoy" : subjectID,
        handle: currentAccount === "B" ? "garden-b" : "garden-a",
        displayName: null, timezone: "UTC", isPublic: false,
        createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z" } }],
      pageInfo: { hasNextPage: false, endCursor: "end" },
    } } : null } });
  }));

  let setVisible!: (value: boolean) => void;
  let environment!: Environment;
  const TestApp = () => {
    const epoch = createAuthEpoch("");
    const [visible, set] = createSignal(true);
    setVisible = set;
    return (
      <AuthEpochContext value={epoch}>
        <Show when={epoch.environment()} keyed>
          {(currentEnvironment) => {
            environment = currentEnvironment;
            return (
              <RelayProvider environment={currentEnvironment}>
                <AppStateProvider>
                  <Show when={visible()}>
                    <OwnerGate>{(handle) => <p data-testid="owned">{handle}</p>}</OwnerGate>
                  </Show>
                </AppStateProvider>
              </RelayProvider>
            );
          }}
        </Show>
      </AuthEpochContext>
    );
  };

  const view = render(() => <TestApp />);
  await waitFor(() => expect(view.getByTestId("owned").textContent).toBe("garden-a"));
  const accountAEnvironment = environment;
  await new Promise<void>((resolve, reject) => fetchQuery(accountAEnvironment, query, { id: subjectID })
    .subscribe({ next: () => resolve(), error: reject }));
  expect(accountAEnvironment.getStore().getSource().get(subjectID)).toBeDefined();

  setVisible(false);
  await waitFor(() => expect(view.queryByTestId("owned")).toBeNull());
  currentAccount = "B";
  setVisible(true);
  await waitFor(() => expect(view.getByTestId("owned").textContent).toBe("garden-b"));
  expect(environment).not.toBe(accountAEnvironment);
  expect(environment.getStore().getSource().get(subjectID)).toBeUndefined();

  const accountBEnvironment = environment;
  setVisible(false);
  await waitFor(() => expect(view.queryByTestId("owned")).toBeNull());
  currentAccount = undefined;
  setVisible(true);
  await waitFor(() => expect(view.getByText("로그인 후 관리할 수 있어요.")).toBeTruthy());
  expect(environment).not.toBe(accountBEnvironment);
  expect(environment.getStore().getSource().get(subjectID)).toBeUndefined();
});
