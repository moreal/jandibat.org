import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import { RelayProvider, createRelayPaginationFragment, createRelayQuery } from "../src/relay";
import type { RelayPaginationCompatQuery } from "./__generated__/RelayPaginationCompatQuery.graphql";
import type { RelayPaginationSubjectFragment$key } from "./__generated__/RelayPaginationSubjectFragment.graphql";
import type { RelayPaginationSubjectRefetchQuery } from "./__generated__/RelayPaginationSubjectRefetchQuery.graphql";
import query from "./__generated__/RelayPaginationCompatQuery.graphql";
import fragment from "./__generated__/RelayPaginationSubjectFragment.graphql";

afterEach(cleanup);

const subjectID = "U3ViamVjdDox";
const firstID = "UHJvdmlkZXJDb25uZWN0aW9uOjE=";
const secondID = "UHJvdmlkZXJDb25uZWN0aW9uOjI=";

function edge(id: string, status: string, cursor: string) {
  return { cursor, node: { __typename: "ProviderConnection", id, status } };
}

function page(edges: ReturnType<typeof edge>[], hasNextPage: boolean) {
  return { edges, pageInfo: {
    hasNextPage, hasPreviousPage: false,
    startCursor: edges[0]?.cursor ?? null,
    endCursor: edges.at(-1)?.cursor ?? null,
  } };
}

it("appends a cursor page once, refetches the connection, and releases subscriptions on unmount", async () => {
  const requested: Array<{ name: string; cursor: unknown; count: unknown }> = [];
  let activeNetwork = 0;
  const environment = new Environment({
    network: Network.create((operation, variables) => Observable.create((sink) => {
      activeNetwork += 1;
      requested.push({ name: operation.name, cursor: variables.cursor, count: variables.count });
      if (operation.name === "RelayPaginationCompatQuery") {
        sink.next({ data: { subject: {
          __typename: "Subject", id: subjectID,
          providerConnections: page([edge(firstID, "ACTIVE", "cursor-1")], true),
        } } });
      } else if (operation.name === "RelayPaginationSubjectRefetchQuery" && variables.cursor === "cursor-1") {
        sink.next({ data: { node: {
          __typename: "Subject", id: subjectID,
          providerConnections: page([edge(secondID, "PENDING", "cursor-2")], false),
        } } });
      } else if (operation.name === "RelayPaginationSubjectRefetchQuery" && variables.cursor == null) {
        sink.next({ data: { node: {
          __typename: "Subject", id: subjectID,
          providerConnections: page([edge(firstID, "UPDATED", "cursor-1")], false),
        } } });
      } else {
        sink.error(new Error(`Unexpected operation ${operation.name}`));
      }
      sink.complete();
      return () => { activeNetwork -= 1; };
    })),
    store: new Store(new RecordSource()),
  });

  let retained = 0;
  let storeSubscriptions = 0;
  const originalRetain = environment.retain.bind(environment);
  vi.spyOn(environment, "retain").mockImplementation((operation) => {
    retained += 1;
    const disposable = originalRetain(operation);
    return { dispose: () => { retained -= 1; disposable.dispose(); } };
  });
  const originalSubscribe = environment.subscribe.bind(environment);
  vi.spyOn(environment, "subscribe").mockImplementation((snapshot, callback) => {
    storeSubscriptions += 1;
    const disposable = originalSubscribe(snapshot, callback);
    return { dispose: () => { storeSubscriptions -= 1; disposable.dispose(); } };
  });

  let connections!: ReturnType<typeof createRelayPaginationFragment<RelayPaginationSubjectRefetchQuery, RelayPaginationSubjectFragment$key>>;
  function TestApp() {
    const subject = createRelayQuery<RelayPaginationCompatQuery>(query, { id: subjectID, count: 1 });
    connections = createRelayPaginationFragment<RelayPaginationSubjectRefetchQuery, RelayPaginationSubjectFragment$key>(
      fragment, () => subject()?.subject,
    );
    return <ul>{connections()?.providerConnections?.edges.map(({ node }) =>
      <li data-testid="connection" data-id={node.id}>{node.status}</li>)}</ul>;
  }

  const view = render(() => <RelayProvider environment={environment}><TestApp /></RelayProvider>);
  await waitFor(() => expect(view.getAllByTestId("connection").map((node) => node.textContent)).toEqual(["ACTIVE"]));
  expect(connections.hasNext).toBe(true);
  expect(retained).toBeGreaterThan(0);
  expect(storeSubscriptions).toBeGreaterThan(0);

  connections.loadNext(1);
  await waitFor(() => expect(view.getAllByTestId("connection").map((node) => node.getAttribute("data-id")))
    .toEqual([firstID, secondID]));
  expect(connections.hasNext).toBe(false);
  expect(requested.map(({ cursor }) => cursor)).toEqual([null, "cursor-1"]);

  connections.refetch({ count: 1, cursor: null, id: subjectID });
  await waitFor(() => expect(view.getAllByTestId("connection").map((node) => node.textContent)).toEqual(["UPDATED"]));
  expect(requested.map(({ cursor }) => cursor)).toEqual([null, "cursor-1", null]);

  view.unmount();
  expect(activeNetwork).toBe(0);
  expect(retained).toBe(0);
  expect(storeSubscriptions).toBe(0);
});
