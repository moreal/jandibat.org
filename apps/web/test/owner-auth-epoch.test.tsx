import type { AuthResultDto, SubjectDto } from "@jandibat/contracts";
import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { Show, createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, fetchQuery } from "relay-runtime";
import { api, ApiError } from "../src/api/client";
import { AppStateProvider } from "../src/app/state";
import { AuthEpochContext, createAuthEpoch } from "../src/relay/auth-epoch";
import { RelayProvider } from "../src/relay";
import { OwnerGate } from "../src/subjects/OwnerGate";
import query from "./__generated__/RelayCompatQuery.graphql";

const subjectID = "U3ViamVjdDox";

function auth(userID: string): AuthResultDto {
  return {
    user: {
      id: userID,
      primaryEmail: `${userID}@example.test`,
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

function subject(handle: string): SubjectDto {
  return {
    id: subjectID,
    handle,
    displayName: handle,
    timezone: "UTC",
    isPublic: false,
    createdAt: "2026-09-24T00:00:00Z",
    updatedAt: "2026-09-24T00:00:00Z",
  };
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("OwnerGate revalidation drops account A's private Relay store for B and on 401", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => Response.json({ data: {
    subject: { __typename: "Subject", id: subjectID, displayName: "Account A private" },
  } })));
  let currentAccount: string | undefined = "A";
  vi.spyOn(api, "getCurrentSession").mockImplementation(async () => {
    if (!currentAccount) throw new ApiError("unauthorized", 401);
    return auth(currentAccount);
  });
  vi.spyOn(api, "listSubjects").mockImplementation(async () => ({
    subjects: [subject(currentAccount === "B" ? "garden-b" : "garden-a")],
    pageInfo: { hasNextPage: false },
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
