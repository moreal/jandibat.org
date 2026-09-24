import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { Show } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { AppStateProvider } from "../src/app/state";
import { AuthEpochContext, createAuthEpoch } from "../src/relay/auth-epoch";
import { RelayProvider } from "../src/relay";
import { OwnerGate } from "../src/subjects/OwnerGate";

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mount() {
  return render(() => {
    const epoch = createAuthEpoch("");
    return <AuthEpochContext value={epoch}>
      <Show when={epoch.environment()} keyed>{(environment) =>
        <RelayProvider environment={environment}>
          <AppStateProvider><OwnerGate>{(handle) => <p data-testid="owned">{handle}</p>}</OwnerGate></AppStateProvider>
        </RelayProvider>}
      </Show>
    </AuthEpochContext>;
  });
}

it("traverses a second owner page before selecting the persisted 101st subject", async () => {
  localStorage.setItem("jandibat:owner-subject", "garden-100");
  const requests: Array<{ name: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as { operationName: string; variables: Record<string, unknown> };
    requests.push({ name: request.operationName, variables: request.variables });
    if (request.operationName === "AuthViewerQuery") return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
    if (request.operationName === "AuthSubjectsQuery") {
      const second = request.variables.cursor === "page-one";
      const indexes = second ? [100] : Array.from({ length: 100 }, (_, index) => index);
      return Response.json({ data: { viewer: { subjects: {
        edges: indexes.map((index) => ({ node: { __typename: "Subject", id: `subject-${index}`,
          handle: `garden-${index}`, displayName: null, timezone: "UTC", isPublic: false,
          createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z" } })),
        pageInfo: { hasNextPage: !second, endCursor: second ? "page-two" : "page-one" },
      } } } });
    }
    throw new Error(`Unexpected ${request.operationName}`);
  }));

  const view = mount();
  await waitFor(() => expect(view.getByTestId("owned").textContent).toBe("garden-100"));
  expect(requests.map(({ name }) => name)).toEqual([
    "AuthViewerQuery", "AuthViewerQuery", "AuthSubjectsQuery", "AuthSubjectsQuery",
  ]);
  expect(requests.at(-1)?.variables.cursor).toBe("page-one");
});

it("creates a first subject through GraphQL and opens the owner workspace", async () => {
  const requests: Array<{ operationName: string; variables: Record<string, unknown> }> = [];
  vi.stubGlobal("fetch", vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
    const request = JSON.parse(String(init?.body)) as typeof requests[number];
    requests.push(request);
    if (request.operationName === "AuthViewerQuery") return Response.json({ data: { viewer: {
      user: { id: "user-1", primaryEmail: "owner@example.test", status: "active", emailVerifiedAt: null },
      currentSession: { __typename: "Session", id: "U2Vzc2lvbjox", createdAt: "2026-09-24T00:00:00Z",
        expiresAt: "2099-09-25T00:00:00Z", revokedAt: null },
    } } });
    if (request.operationName === "AuthCreateSubjectMutation") return Response.json({ data: { createSubject: {
      errors: [], subject: { __typename: "Subject", id: "U3ViamVjdDox", handle: "new-garden",
        displayName: "New Garden", timezone: "UTC", isPublic: true,
        createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z" },
    } } });
    return Response.json({ data: { viewer: {
      subjects: { edges: [], pageInfo: { hasNextPage: false, endCursor: null } },
    } } });
  }));
  const view = mount();
  await waitFor(() => expect(view.getByRole("button", { name: /잔디밭 만들기/ })).toBeTruthy());
  await fireEvent.input(view.getByRole("textbox", { name: "프로필 식별자" }),
    { target: { value: "new-garden" } });
  await fireEvent.input(view.getByRole("textbox", { name: /표시 이름/ }),
    { target: { value: "New Garden" } });
  await fireEvent.submit(view.container.querySelector("form")!);
  await waitFor(() => expect(view.getByTestId("owned").textContent).toBe("new-garden"));
  expect(requests.find(({ operationName }) => operationName === "AuthCreateSubjectMutation")?.variables.input)
    .toMatchObject({ handle: "new-garden", displayName: "New Garden", isPublic: true });
});
