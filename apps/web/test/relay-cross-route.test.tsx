import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it } from "vitest";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import {
  RelayProvider,
  createRelayFragment,
  createRelayMutation,
  createRelayQuery,
} from "../src/relay";
import type { RelayCompatQuery } from "./__generated__/RelayCompatQuery.graphql";
import type { RelayCompatSubjectFragment$key } from "./__generated__/RelayCompatSubjectFragment.graphql";
import type { RelayCompatUpdateSubjectMutation } from "./__generated__/RelayCompatUpdateSubjectMutation.graphql";
import type { RelayCrossRouteSubjectQuery } from "./__generated__/RelayCrossRouteSubjectQuery.graphql";
import type { RelayCrossRouteSubjectFragment$key } from "./__generated__/RelayCrossRouteSubjectFragment.graphql";
import exploreQuery from "./__generated__/RelayCompatQuery.graphql";
import exploreFragment from "./__generated__/RelayCompatSubjectFragment.graphql";
import subjectQuery from "./__generated__/RelayCrossRouteSubjectQuery.graphql";
import subjectFragment from "./__generated__/RelayCrossRouteSubjectFragment.graphql";
import updateMutation from "./__generated__/RelayCompatUpdateSubjectMutation.graphql";

afterEach(cleanup);

const subjectID = "U3ViamVjdDox";

// Models an explore card and a subject page mounted under one app-level Relay
// provider. Their distinct operations must converge on the same Subject Node.
it("updates two route consumers of one Subject Node from a single mutation without refetch", async () => {
  const operations: string[] = [];
  const environment = new Environment({
    network: Network.create((operation) => Observable.create((sink) => {
      operations.push(operation.name);
      switch (operation.name) {
        case "RelayCompatQuery":
        case "RelayCrossRouteSubjectQuery":
          sink.next({ data: { subject: {
            __typename: "Subject", id: subjectID, displayName: "Before",
          } } });
          break;
        case "RelayCompatUpdateSubjectMutation":
          sink.next({ data: { updateSubject: {
            errors: [],
            subject: { __typename: "Subject", id: subjectID, displayName: "After" },
          } } });
          break;
        default:
          sink.error(new Error(`Unexpected operation: ${operation.name}`));
          return;
      }
      sink.complete();
    })),
    store: new Store(new RecordSource()),
  });

  let updateSubject!: (variables: RelayCompatUpdateSubjectMutation["variables"]) => Promise<unknown>;
  function ExploreRoute() {
    const query = createRelayQuery<RelayCompatQuery>(exploreQuery, { id: subjectID });
    const data = createRelayFragment<RelayCompatSubjectFragment$key>(
      exploreFragment, () => query()?.subject ?? null,
    );
    return <span data-testid="explore-subject">{data()?.displayName}</span>;
  }
  function SubjectRoute() {
    const query = createRelayQuery<RelayCrossRouteSubjectQuery>(subjectQuery, { id: subjectID });
    const data = createRelayFragment<RelayCrossRouteSubjectFragment$key>(
      subjectFragment, () => query()?.subject ?? null,
    );
    updateSubject = createRelayMutation<RelayCompatUpdateSubjectMutation>(updateMutation);
    return <span data-testid="subject-page">{data()?.displayName}</span>;
  }

  const view = render(() => <RelayProvider environment={environment}>
    <ExploreRoute />
    <SubjectRoute />
  </RelayProvider>);
  await waitFor(() => {
    expect(view.getByTestId("explore-subject").textContent).toBe("Before");
    expect(view.getByTestId("subject-page").textContent).toBe("Before");
  });
  const initialQueryOperations = operations.filter((name) => name !== "RelayCompatUpdateSubjectMutation");
  expect(initialQueryOperations).toEqual(["RelayCompatQuery", "RelayCrossRouteSubjectQuery"]);

  await updateSubject({ input: { id: subjectID, displayName: "After" } });
  await waitFor(() => {
    expect(view.getByTestId("explore-subject").textContent).toBe("After");
    expect(view.getByTestId("subject-page").textContent).toBe("After");
  });
  expect(operations).toEqual([...initialQueryOperations, "RelayCompatUpdateSubjectMutation"]);
  view.unmount();
});
