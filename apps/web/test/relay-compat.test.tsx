import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import {
  Environment,
  Network,
  Observable,
  RecordSource,
  Store,
} from "relay-runtime";
import {
  RelayProvider,
  createRelayFragment,
  createRelayMutation,
  createRelayQuery,
} from "../src/relay";
import type { RelayCompatQuery } from "./__generated__/RelayCompatQuery.graphql";
import type { RelayCompatSubjectFragment$key } from "./__generated__/RelayCompatSubjectFragment.graphql";
import type { RelayCompatUpdateSubjectMutation } from "./__generated__/RelayCompatUpdateSubjectMutation.graphql";
import query from "./__generated__/RelayCompatQuery.graphql";
import fragment from "./__generated__/RelayCompatSubjectFragment.graphql";
import mutation from "./__generated__/RelayCompatUpdateSubjectMutation.graphql";

afterEach(cleanup);

const subjectID = "U3ViamVjdDox";

function SubjectName(props: { subject: () => RelayCompatSubjectFragment$key | null }) {
  const subject = createRelayFragment<RelayCompatSubjectFragment$key>(fragment, props.subject);
  return <span data-testid="subject-name">{subject()?.displayName}</span>;
}

it("shares one normalized Subject between fragment consumers and releases Relay resources on disposal", async () => {
  let activeNetworkSubscriptions = 0;
  const environment = new Environment({
    network: Network.create((operation) => Observable.create((sink) => {
      activeNetworkSubscriptions += 1;
      if (operation.name === "RelayCompatQuery") {
        sink.next({ data: { subject: { __typename: "Subject", id: subjectID, displayName: "Before" } } });
        // Leave the query stream open so root disposal must cancel it.
      } else if (operation.name === "RelayCompatUpdateSubjectMutation") {
        sink.next({ data: { updateSubject: {
          errors: [],
          subject: { __typename: "Subject", id: subjectID, displayName: "After" },
        } } });
        sink.complete();
      } else {
        sink.error(new Error(`Unexpected operation: ${operation.name}`));
      }
      return () => { activeNetworkSubscriptions -= 1; };
    })),
    store: new Store(new RecordSource()),
  });

  let retainedOperations = 0;
  let storeSubscriptions = 0;
  const originalRetain = environment.retain.bind(environment);
  vi.spyOn(environment, "retain").mockImplementation((operation) => {
    retainedOperations += 1;
    const disposable = originalRetain(operation);
    return { dispose: () => { retainedOperations -= 1; disposable.dispose(); } };
  });
  const originalSubscribe = environment.subscribe.bind(environment);
  vi.spyOn(environment, "subscribe").mockImplementation((snapshot, callback) => {
    storeSubscriptions += 1;
    const disposable = originalSubscribe(snapshot, callback);
    return { dispose: () => { storeSubscriptions -= 1; disposable.dispose(); } };
  });

  let updateSubject!: (variables: RelayCompatUpdateSubjectMutation["variables"]) => Promise<unknown>;
  function TestApp() {
    const data = createRelayQuery<RelayCompatQuery>(query, { id: subjectID });
    updateSubject = createRelayMutation<RelayCompatUpdateSubjectMutation>(mutation);
    const subject = () => data()?.subject ?? null;
    return <>
      <SubjectName subject={subject} />
      <SubjectName subject={subject} />
    </>;
  }

  const view = render(() => <RelayProvider environment={environment}><TestApp /></RelayProvider>);
  await waitFor(() => expect(view.getAllByTestId("subject-name").map((node) => node.textContent))
    .toEqual(["Before", "Before"]));
  expect(retainedOperations).toBeGreaterThan(0);
  expect(storeSubscriptions).toBeGreaterThan(0);
  expect(activeNetworkSubscriptions).toBe(1);

  await updateSubject({ input: { id: subjectID, displayName: "After" } });
  await waitFor(() => expect(view.getAllByTestId("subject-name").map((node) => node.textContent))
    .toEqual(["After", "After"]));

  view.unmount();
  expect(retainedOperations).toBe(0);
  expect(storeSubscriptions).toBe(0);
  expect(activeNetworkSubscriptions).toBe(0);
});

it("rejects a synchronous mutation network error without retaining the request", async () => {
  let activeNetworkSubscriptions = 0;
  const environment = new Environment({
    network: Network.create(() => Observable.create((sink) => {
      activeNetworkSubscriptions += 1;
      sink.error(new Error("offline"));
      return () => { activeNetworkSubscriptions -= 1; };
    })),
    store: new Store(new RecordSource()),
  });
  let updateSubject!: (variables: RelayCompatUpdateSubjectMutation["variables"]) => Promise<unknown>;
  function TestApp() {
    updateSubject = createRelayMutation<RelayCompatUpdateSubjectMutation>(mutation);
    return <span>ready</span>;
  }

  const view = render(() => <RelayProvider environment={environment}><TestApp /></RelayProvider>);
  await expect(updateSubject({ input: { id: subjectID, displayName: "After" } }))
    .rejects.toThrow("offline");
  expect(activeNetworkSubscriptions).toBe(0);
  view.unmount();
});

it("settles a failed query and recovers when its variables change", async () => {
  const nextID = "U3ViamVjdDoy";
  let activeNetworkSubscriptions = 0;
  const attemptedIDs: unknown[] = [];
  const environment = new Environment({
    network: Network.create((operation, variables) => Observable.create((sink) => {
      attemptedIDs.push(variables.id);
      activeNetworkSubscriptions += 1;
      if (variables.id === subjectID) {
        sink.error(new Error("offline"));
      } else if (operation.name === "RelayCompatQuery") {
        queueMicrotask(() => {
          sink.next({ data: { subject: { __typename: "Subject", id: nextID, displayName: "Recovered" } } });
          sink.complete();
        });
      }
      return () => { activeNetworkSubscriptions -= 1; };
    })),
    store: new Store(new RecordSource()),
  });
  let result!: ReturnType<typeof createRelayQuery<RelayCompatQuery>>;
  let changeID!: (value: string) => void;
  function TestApp() {
    const [id, setID] = createSignal(subjectID);
    changeID = setID;
    result = createRelayQuery<RelayCompatQuery>(query, () => ({ id: id() }));
    return <SubjectName subject={() => result.error ? null : result()?.subject ?? null} />;
  }

  const view = render(() => <RelayProvider environment={environment}><TestApp /></RelayProvider>);
  await waitFor(() => expect(result.error).toBeInstanceOf(Error));
  expect(result.pending).toBe(false);
  expect(activeNetworkSubscriptions).toBe(0);
  changeID(nextID);
  await waitFor(() => expect(attemptedIDs).toEqual([subjectID, nextID]));
  await waitFor(() => expect(result.error).toBeUndefined());
  expect(environment.getStore().getSource().get(nextID)).toMatchObject({ displayName: "Recovered" });
  expect(result.pending).toBe(false);
  await waitFor(() => expect(view.getByTestId("subject-name").textContent).toBe("Recovered"));
  expect(result.error).toBeUndefined();
  view.unmount();
  expect(activeNetworkSubscriptions).toBe(0);
});

it("retries the same normalized query when its fetch key changes", async () => {
  let attempts = 0;
  let activeNetworkSubscriptions = 0;
  const environment = new Environment({
    network: Network.create(() => Observable.create((sink) => {
      attempts += 1;
      activeNetworkSubscriptions += 1;
      if (attempts === 1) {
        sink.error(new Error("offline"));
      } else {
        sink.next({ data: { subject: { __typename: "Subject", id: subjectID, displayName: "Retried" } } });
        sink.complete();
      }
      return () => { activeNetworkSubscriptions -= 1; };
    })),
    store: new Store(new RecordSource()),
  });
  let result!: ReturnType<typeof createRelayQuery<RelayCompatQuery>>;
  let retry!: () => void;
  function TestApp() {
    const [fetchKey, setFetchKey] = createSignal(0);
    retry = () => setFetchKey((value) => value + 1);
    result = createRelayQuery<RelayCompatQuery>(query, { id: subjectID }, {
      fetchKey,
      fetchPolicy: "network-only",
    });
    return <SubjectName subject={() => result.error ? null : result()?.subject ?? null} />;
  }

  const view = render(() => <RelayProvider environment={environment}><TestApp /></RelayProvider>);
  await waitFor(() => expect(result.error).toBeInstanceOf(Error));
  expect(activeNetworkSubscriptions).toBe(0);
  retry();
  await waitFor(() => expect(view.getByTestId("subject-name").textContent).toBe("Retried"));
  expect(attempts).toBe(2);
  view.unmount();
  expect(activeNetworkSubscriptions).toBe(0);
});
