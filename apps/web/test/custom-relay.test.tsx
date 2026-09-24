import type { SubjectDto } from "@jandibat/contracts";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import { createSignal } from "solid-js";
import { api } from "../src/api/client";
import { AppStateProvider } from "../src/app/state";
import { CustomPage } from "../src/pages/CustomPage";
import { RelayProvider } from "../src/relay";
import { AuthEpochContext } from "../src/relay/auth-epoch";

const subjectID = "U3ViamVjdDox";
const owner: SubjectDto = {
  id: subjectID, handle: "garden", displayName: "Garden", timezone: "UTC", isPublic: true,
  createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z",
};
const providerID = "Q3VzdG9tUHJvdmlkZXI6MQ==";
const ingestProviderID = "00000000-0000-0000-0000-000000000001";
const provider = { __typename: "CustomProvider", id: providerID, ingestProviderID,
  slug: "reading", name: "Reading", description: "Books", status: "active", allowedActions: ["read"],
  createdAt: "2026-09-24T00:00:00Z", updatedAt: "2026-09-24T00:00:00Z" };
const pageInfo = { hasNextPage: false, hasPreviousPage: false, startCursor: "cursor-1", endCursor: "cursor-1" };

function page(edges: unknown[] = [{ cursor: "cursor-1", node: provider }], hasNextPage = false) {
  return { data: { subject: { __typename: "Subject", id: subjectID, customProviders: {
    edges, pageInfo: { ...pageInfo, hasNextPage },
  } } } };
}

it("renders untrusted custom-provider fields as text in the Relay card", async () => {
  const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;
  const unsafe = { ...provider, name: attack, slug: attack, description: attack, allowedActions: [attack] };
  const { view } = mount(() => page([{ cursor: "cursor-1", node: unsafe }]));

  await waitFor(() => expect(view.getByText(attack, { selector: "h3" })).toBeTruthy());
  expect(view.container.querySelector("img")).toBeNull();
  expect(view.container.querySelector("[autofocus]")).toBeNull();
  expect(view.container.querySelector(".custom-provider-card code")?.textContent).toBe(attack);
});

function mount(respond: (name: string, variables: Record<string, unknown>) => { data: Record<string, unknown> } | Error | null,
  subjects: SubjectDto[] = [owner]) {
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
        sink.next({ data: { viewer: { subjects: { edges: subjects.map((subject) => ({ node: {
          __typename: "Subject", ...subject,
        } })), pageInfo: { hasNextPage: false, endCursor: null } } } } });
        sink.complete();
        return;
      }
      operations.push({ name: operation.name, variables });
      const result = respond(operation.name, variables);
      if (result === null) return;
      if (result instanceof Error) { sink.error(result); return; }
      sink.next(result); sink.complete();
    })),
    store: new Store(new RecordSource()),
  });
  const [epochEnvironment, setEpochEnvironment] = createSignal(environment);
  const view = render(() => <AuthEpochContext value={{ environment: epochEnvironment, beginAuthenticatedSession: () => undefined, authenticated: () => false,
    signedOut: () => false, takeNotice: () => undefined }}>
    <RelayProvider environment={environment}><AppStateProvider><CustomPage /></AppStateProvider></RelayProvider>
  </AuthEpochContext>);
  return { view, environment, operations, switchAccount: () => setEpochEnvironment(new Environment({
    network: Network.create(() => Observable.create((sink) => sink.error(new Error("new account network")))),
    store: new Store(new RecordSource()),
  })) };
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

it("loads owner custom providers from a nullable Relay page rather than REST", async () => {
  const { view, operations } = mount(() => page());
  await waitFor(() => expect(view.getByText("Reading")).toBeTruthy());
  expect(operations.map(({ name }) => name)).toEqual(["CustomQuery"]);
});

it("does not display owner cards when the GraphQL custom page is null", async () => {
  const { view } = mount(() => ({ data: { subject: { __typename: "Subject", id: subjectID, customProviders: null } } }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("볼 수 없습니다"));
  expect(view.queryByText("Reading")).toBeNull();
});

it("does not submit a create mutation when the owner-only custom page is null", async () => {
  const request = vi.fn(async () => Response.json({ data: { createCustomProvider: { errors: [], provider: { id: providerID },
    ingestionKey: "must-not-be-issued" } } }));
  vi.stubGlobal("fetch", request);
  const { view } = mount(() => ({ data: { subject: { __typename: "Subject", id: subjectID, customProviders: null } } }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("볼 수 없습니다"));
  await fireEvent.input(view.getByLabelText("이름"), { target: { value: "Writing" } });
  await fireEvent.input(view.getByLabelText("식별자"), { target: { value: "writing" } });
  await fireEvent.input(view.getByLabelText("활동 종류"), { target: { value: "write" } });
  await fireEvent.submit(view.container.querySelector(".stack-form")!);
  expect(request).not.toHaveBeenCalled();
  expect(view.getByRole("button", { name: /데이터 소스 만들기/ }).hasAttribute("disabled")).toBe(true);
});

it("loads every custom-provider edge through the cursor without duplicates", async () => {
  const second = { ...provider, id: "Q3VzdG9tUHJvdmlkZXI6Mg==", ingestProviderID: "00000000-0000-0000-0000-000000000002",
    slug: "running", name: "Running" };
  const { view, operations } = mount((name, variables) => {
    if (name === "CustomQuery") return page(undefined, true);
    if (name === "CustomSubjectRefetchQuery" && variables.cursor === "cursor-1") return { data: {
      node: { __typename: "Subject", id: subjectID, customProviders: {
        edges: [{ cursor: "cursor-2", node: second }],
        pageInfo: { hasNextPage: false, hasPreviousPage: true, startCursor: "cursor-2", endCursor: "cursor-2" },
      } },
    } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "데이터 소스 더 보기" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "데이터 소스 더 보기" }));
  await waitFor(() => expect(view.getByText("Running")).toBeTruthy());
  expect(view.container.querySelectorAll(".custom-provider-card")).toHaveLength(2);
  expect(view.queryByRole("button", { name: "데이터 소스 더 보기" })).toBeNull();
  expect(operations.map(({ name }) => name)).toEqual(["CustomQuery", "CustomSubjectRefetchQuery"]);
  expect(operations[1].variables.cursor).toBe("cursor-1");
});

it("keeps the one-time create key outside Relay and clears it on subject change", async () => {
  const marker = "synthetic-secret-keep-out-of-store";
  const secondOwner = { ...owner, id: "U3ViamVjdDoy", handle: "other", displayName: "Other" };
  let request: { operationName: string; variables: Record<string, unknown> } | undefined;
  vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
    request = JSON.parse(String(init.body)) as typeof request;
    return Response.json({ data: { createCustomProvider: { errors: [], provider: { id: providerID }, ingestionKey: marker } } });
  }));
  const { view, environment, operations } = mount((name, variables) => {
    if (name === "CustomQuery") return variables.subject === "other" ? { data: { subject: {
      __typename: "Subject", id: secondOwner.id, customProviders: { edges: [], pageInfo },
    } } } : page();
    if (name === "CustomSubjectRefetchQuery") return { data: { node: page().data.subject } };
    throw new Error(`Unexpected operation ${name}`);
  }, [owner, secondOwner]);
  await waitFor(() => expect(view.getByRole("combobox", { name: "관리할 잔디밭" })).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "garden" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.getByText("Reading")).toBeTruthy());
  await fireEvent.input(view.getByLabelText("이름"), { target: { value: "Writing" } });
  await fireEvent.input(view.getByLabelText("식별자"), { target: { value: "writing" } });
  await fireEvent.input(view.getByLabelText("활동 종류"), { target: { value: "write" } });
  await fireEvent.submit(view.container.querySelector(".stack-form")!);
  await waitFor(() => expect(view.getByText(marker)).toBeTruthy());
  expect(request?.operationName).toBe("CustomCreateProviderMutation");
  expect(request?.variables.input).toMatchObject({ subjectID, slug: "writing", name: "Writing", allowedActions: ["write"] });
  expect(operations.some(({ name }) => name === "CustomCreateProviderMutation")).toBe(false);
  expect(JSON.stringify(environment.getStore().getSource().toJSON())).not.toContain(marker);
  expect(JSON.stringify(localStorage)).not.toContain(marker);
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "other" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.queryByText(marker)).toBeNull());
  expect(view.queryByText("Reading")).toBeNull();
});

it("does not show the previous owner's cards while the next subject query is pending", async () => {
  const secondOwner = { ...owner, id: "U3ViamVjdDoy", handle: "other", displayName: "Other" };
  const { view } = mount((name, variables) => {
    if (name === "CustomQuery" && variables.subject === "garden") return page();
    if (name === "CustomQuery" && variables.subject === "other") return null;
    throw new Error(`Unexpected operation ${name}`);
  }, [owner, secondOwner]);
  await waitFor(() => expect(view.getByRole("combobox", { name: "관리할 잔디밭" })).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "garden" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.getByText("Reading")).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "other" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.getByRole("status").textContent).toContain("불러오는 중"));
  expect(view.queryByText("Reading")).toBeNull();
});

it("does not reuse the previous owner's cached cards for a null custom page", async () => {
  const secondOwner = { ...owner, id: "U3ViamVjdDoy", handle: "other", displayName: "Other" };
  const { view, operations } = mount((name, variables) => {
    if (name === "CustomQuery" && variables.subject === "garden") return page();
    if (name === "CustomQuery" && variables.subject === "other") return { data: { subject: {
      __typename: "Subject", id: secondOwner.id, customProviders: null,
    } } };
    throw new Error(`Unexpected operation ${name}`);
  }, [owner, secondOwner]);
  await waitFor(() => expect(view.getByRole("combobox", { name: "관리할 잔디밭" })).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "other" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("볼 수 없습니다"));
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "garden" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(view.getByText("Reading")).toBeTruthy());
  await fireEvent.change(view.getByRole("combobox", { name: "관리할 잔디밭" }), { target: { value: "other" } });
  await fireEvent.submit(view.container.querySelector(".connection-toolbar")!);
  await waitFor(() => expect(operations.some(({ name, variables }) => name === "CustomQuery" && variables.subject === "other")).toBe(true));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("볼 수 없습니다"));
  expect(view.queryByText("Reading")).toBeNull();
  expect(view.getByRole("button", { name: /데이터 소스 만들기/ }).hasAttribute("disabled")).toBe(true);
  expect(operations.filter(({ name, variables }) => name === "CustomQuery" && variables.subject === "other")).toHaveLength(1);
});

it("uses the raw ingestProviderID only for the HTTP activity edge", async () => {
  const ingest = vi.spyOn(api, "ingestCustomActivities").mockResolvedValue({ accepted: 1, rejected: 0, duplicates: 0, rejections: [] });
  const { view } = mount(() => page());
  await waitFor(() => expect(view.getByRole("button", { name: "활동 입력" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "활동 입력" }));
  const form = view.container.querySelector(".inline-form")!;
  await fireEvent.input(form.querySelector('input[name="ingestionKey"]')!, { target: { value: "a".repeat(32) } });
  await fireEvent.submit(form);
  await waitFor(() => expect(ingest).toHaveBeenCalled());
  expect(ingest.mock.calls[0][0]).toBe(ingestProviderID);
  expect(ingest.mock.calls[0][0]).not.toBe(providerID);
});

it("normalizes a status update by Relay ID and removes a deleted card after refetch", async () => {
  vi.stubGlobal("confirm", () => true);
  let deleted = false;
  const { view, operations } = mount((name) => {
    if (name === "CustomQuery") return page();
    if (name === "CustomUpdateProviderMutation") return { data: { updateCustomProvider: {
      errors: [], provider: { __typename: "CustomProvider", id: providerID, name: "Reading",
        description: "Books", status: "disabled", allowedActions: ["read"], updatedAt: "2026-09-24T01:00:00Z" },
    } } };
    if (name === "CustomDeleteProviderMutation") { deleted = true; return { data: {
      deleteCustomProvider: { errors: [], deletedProviderID: providerID },
    } }; }
    if (name === "CustomSubjectRefetchQuery") return { data: { node: { __typename: "Subject", id: subjectID,
      customProviders: { edges: deleted ? [] : [{ cursor: "cursor-1", node: { ...provider, status: "disabled" } }], pageInfo },
    } } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "중지" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "중지" }));
  await waitFor(() => expect(view.getByText("중지됨")).toBeTruthy());
  expect(operations.find(({ name }) => name === "CustomUpdateProviderMutation")?.variables).toEqual({
    input: { id: providerID, status: "DISABLED" },
  });
  await fireEvent.click(view.getByRole("button", { name: "Reading 삭제" }));
  await waitFor(() => expect(view.queryByText("Reading")).toBeNull());
  expect(view.getByText("아직 만든 데이터 소스가 없어요.")).toBeTruthy();
  expect(operations.find(({ name }) => name === "CustomDeleteProviderMutation")?.variables).toEqual({ input: { id: providerID } });
});

it("does not treat a typed deletion error as a removed provider", async () => {
  vi.stubGlobal("confirm", () => true);
  const { view, operations } = mount((name) => {
    if (name === "CustomQuery") return page();
    if (name === "CustomDeleteProviderMutation") return { data: { deleteCustomProvider: {
      errors: [{ code: "CONFLICT", message: "Still linked", field: null }], deletedProviderID: null,
    } } };
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "Reading 삭제" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Reading 삭제" }));
  await waitFor(() => expect(view.getByRole("button", { name: "Reading 삭제" }).hasAttribute("disabled")).toBe(false));
  expect(view.getByText("Reading")).toBeTruthy();
  expect(operations.map(({ name }) => name)).toEqual(["CustomQuery", "CustomDeleteProviderMutation"]);
});

it("hides a deleted provider when its successful mutation is followed by a failed refresh", async () => {
  vi.stubGlobal("confirm", () => true);
  const { view, operations } = mount((name) => {
    if (name === "CustomQuery") return page();
    if (name === "CustomDeleteProviderMutation") return { data: { deleteCustomProvider: {
      errors: [], deletedProviderID: providerID,
    } } };
    if (name === "CustomSubjectRefetchQuery") return new Error("refresh offline");
    throw new Error(`Unexpected operation ${name}`);
  });
  await waitFor(() => expect(view.getByRole("button", { name: "Reading 삭제" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Reading 삭제" }));
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("refresh offline"));
  expect(view.queryByText("Reading")).toBeNull();
  expect(operations.map(({ name }) => name)).toEqual([
    "CustomQuery", "CustomDeleteProviderMutation", "CustomSubjectRefetchQuery",
  ]);
});

it("rotates the key outside Relay and clears the one-time value on unmount", async () => {
  vi.stubGlobal("confirm", () => true);
  const marker = "synthetic-rotated-key";
  let request: { operationName: string; variables: Record<string, unknown> } | undefined;
  vi.stubGlobal("fetch", vi.fn(async (_url: string, init: RequestInit) => {
    request = JSON.parse(String(init.body)) as typeof request;
    return Response.json({ data: { rotateCustomProviderKey: { errors: [], ingestionKey: marker,
      createdAt: "2026-09-24T01:00:00Z" } } });
  }));
  const { view, environment, operations } = mount(() => page());
  await waitFor(() => expect(view.getByRole("button", { name: "Key 재발급" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Key 재발급" }));
  await waitFor(() => expect(view.getByText(marker)).toBeTruthy());
  expect(request?.operationName).toBe("CustomRotateProviderKeyMutation");
  expect(request?.variables).toEqual({ input: { id: providerID } });
  expect(operations.map(({ name }) => name)).toEqual(["CustomQuery"]);
  expect(JSON.stringify(environment.getStore().getSource().toJSON())).not.toContain(marker);
  view.unmount();
  expect(document.body.textContent).not.toContain(marker);
  expect(JSON.stringify(localStorage)).not.toContain(marker);
});

it("discards a late rotated key after an account epoch switch", async () => {
  vi.stubGlobal("confirm", () => true);
  let resolveResponse!: (response: Response) => void;
  vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { resolveResponse = resolve; })));
  const { view, switchAccount } = mount(() => page());
  await waitFor(() => expect(view.getByRole("button", { name: "Key 재발급" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Key 재발급" }));
  switchAccount();
  resolveResponse(Response.json({ data: { rotateCustomProviderKey: {
    errors: [], ingestionKey: "late-secret-marker", createdAt: "2026-09-24T01:00:00Z",
  } } }));
  await waitFor(() => expect(view.getByRole("button", { name: "Key 재발급" }).hasAttribute("disabled")).toBe(false));
  expect(view.queryByText("late-secret-marker")).toBeNull();
});

it("clears an earlier one-time key when a new rotation is rejected", async () => {
  vi.stubGlobal("confirm", () => true);
  let rotations = 0;
  vi.stubGlobal("fetch", vi.fn(async () => {
    rotations += 1;
    return Response.json({ data: { rotateCustomProviderKey: rotations === 1
      ? { errors: [], ingestionKey: "first-only-key", createdAt: "2026-09-24T01:00:00Z" }
      : { errors: [{ code: "CONFLICT", message: "Key locked", field: null }], ingestionKey: null, createdAt: null },
    } });
  }));
  const { view } = mount(() => page());
  await waitFor(() => expect(view.getByRole("button", { name: "Key 재발급" })).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Key 재발급" }));
  await waitFor(() => expect(view.getByText("first-only-key")).toBeTruthy());
  await fireEvent.click(view.getByRole("button", { name: "Key 재발급" }));
  await waitFor(() => expect(rotations).toBe(2));
  expect(view.queryByText("first-only-key")).toBeNull();
});
