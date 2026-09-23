import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { Environment, Network, Observable, RecordSource, Store } from "relay-runtime";
import { AppStateProvider } from "../src/app/state";
import { ExplorePage } from "../src/pages/ExplorePage";
import { RelayProvider } from "../src/relay";

vi.mock("@solidjs/router", () => ({ useNavigate: () => () => undefined }));

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const subjectID = "U3ViamVjdDox";

function environmentFor(
  respond: (variables: Record<string, unknown>) => { data: Record<string, unknown> },
  requests: Record<string, unknown>[],
) {
  return new Environment({
    network: Network.create((_operation, variables) => Observable.create((sink) => {
      requests.push(variables);
      sink.next(respond(variables));
      sink.complete();
    })),
    store: new Store(new RecordSource()),
  });
}

function renderExplore(environment: Environment, subject: () => string) {
  return render(() => (
    <RelayProvider environment={environment}>
      <AppStateProvider><ExplorePage subject={subject()} /></AppStateProvider>
    </RelayProvider>
  ));
}

it("renders an authorized activity snapshot through Relay and refetches when the handle changes", async () => {
  const untrackedReads: string[] = [];
  const warn = console.warn.bind(console);
  const warnSpy = vi.spyOn(console, "warn").mockImplementation((...args: unknown[]) => {
    const message = String(args[0]);
    if (message.includes("STRICT_READ_UNTRACKED")) untrackedReads.push(message);
    else warn(...args);
  });
  const requests: Record<string, unknown>[] = [];
  const [handle, setHandle] = createSignal("garden");
  const environment = environmentFor((variables) => ({
    data: {
      subject: {
        __typename: "Subject",
        id: subjectID,
        handle: variables.handle,
        timezone: "Pacific/Honolulu",
        activitySnapshot: {
          subject: { __typename: "Subject", id: subjectID, handle: variables.handle },
          range: variables.range,
          days: [{
            date: (variables.range as { to: string }).to,
            count: "2",
            level: 2,
            entries: [{ environmentID: "github", action: "commit", metricName: "commits", metricValue: "2", metadata: [] }],
          }],
          environments: [{ id: "github", key: "github", name: "GitHub", scope: "global", metadata: [] }],
          total: "2",
          longestStreak: 1,
          generatedAt: "2026-09-24T00:00:00Z",
          dataUpdatedAt: "2026-09-23T00:00:00Z",
          revision: "snapshot-1",
        },
      },
    },
  }), requests);

  const view = renderExplore(environment, handle);
  await waitFor(() => expect(view.container.querySelector(".heatmap-heading h2")?.textContent).toBe("@garden의 잔디밭"));
  expect(view.getByRole("gridcell", { name: /GitHub commit 2 commits/ })).toBeTruthy();
  expect(view.container.querySelector(".heatmap-total strong")?.textContent).toBe("2");
  expect(requests).toHaveLength(1);
  expect(requests[0].handle).toBe("garden");
  expect(requests[0].timezone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
  expect(view.container.querySelector(".heatmap-footer")?.textContent)
    .toContain(`${Intl.DateTimeFormat().resolvedOptions().timeZone} 기준`);
  expect(localStorage.getItem("jandibat:explore-subject")).toBe("garden");

  setHandle("second");
  await waitFor(() => expect(view.container.querySelector(".heatmap-heading h2")?.textContent).toBe("@second의 잔디밭"));
  expect(requests).toHaveLength(2);
  expect(requests[1].handle).toBe("second");
  expect(untrackedReads).toEqual([]);
  warnSpy.mockRestore();
});

it("keeps an unknown or private subject out of the heatmap", async () => {
  const requests: Record<string, unknown>[] = [];
  const environment = environmentFor(() => ({ data: { subject: null } }), requests);
  const view = renderExplore(environment, () => "private-garden");

  await waitFor(() => expect(view.getByRole("alert")).toBeTruthy());
  expect(view.queryByRole("grid")).toBeNull();
  expect(requests).toHaveLength(1);
});

it("retries the same snapshot after a network error", async () => {
  let attempts = 0;
  const environment = new Environment({
    network: Network.create((_operation, variables) => Observable.create((sink) => {
      attempts += 1;
      if (attempts === 1) {
        sink.error(new Error("offline"));
      } else {
        sink.next({ data: { subject: {
          __typename: "Subject", id: subjectID, handle: "garden", timezone: "UTC",
          activitySnapshot: {
            subject: { __typename: "Subject", id: subjectID, handle: "garden" },
            range: variables.range,
            generatedAt: "2026-09-24T00:00:00Z", dataUpdatedAt: null, revision: "empty-1",
            days: [], environments: [], total: 0, longestStreak: 0,
          },
        } } });
        sink.complete();
      }
    })),
    store: new Store(new RecordSource()),
  });

  const view = renderExplore(environment, () => "garden");
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("offline"));
  view.getByRole("button", { name: "다시 시도" }).click();
  await waitFor(() => expect(view.container.querySelector(".heatmap-heading h2")?.textContent).toBe("@garden의 잔디밭"));
  expect(attempts).toBe(2);
});

it.each(["9007199254740992", "-1", "not-a-number"])(
  "shows a safe visualization error for an invalid activity Long %s",
  async (count) => {
    const environment = environmentFor((variables) => ({ data: { subject: {
      __typename: "Subject", id: subjectID, handle: "garden",
      activitySnapshot: {
        range: variables.range,
        generatedAt: "2026-09-24T00:00:00Z", dataUpdatedAt: null, revision: "bad-value",
        days: [{ date: (variables.range as { to: string }).to, count, level: 1, entries: [] }],
        environments: [],
      },
    } } }), []);
    const view = renderExplore(environment, () => "garden");
    await waitFor(() => expect(view.getByRole("alert").textContent).toContain("활동 데이터를 표시할 수 없어요"));
    expect(view.queryByRole("grid")).toBeNull();
  },
);

it.each([
  { cause: "aggregate total", first: "9007199254740991", metric: "1" },
  { cause: "metric value", first: "1", metric: "9007199254740992" },
])("rejects an unsafe $cause before rendering the heatmap", async ({ first, metric }) => {
  const environment = environmentFor((variables) => ({ data: { subject: {
    __typename: "Subject", id: subjectID, handle: "garden",
    activitySnapshot: {
      range: variables.range,
      generatedAt: "2026-09-24T00:00:00Z", dataUpdatedAt: null, revision: "bad-sum",
      days: [
        { date: (variables.range as { from: string }).from, count: first, level: 4, entries: [] },
        { date: (variables.range as { to: string }).to, count: "1", level: 1,
          entries: [{ environmentID: "github", action: "commit", metricName: "commits", metricValue: metric, metadata: [] }] },
      ],
      environments: [{ id: "github", key: "github", name: "GitHub", scope: "global", metadata: [] }],
    },
  } } }), []);
  const view = renderExplore(environment, () => "garden");
  await waitFor(() => expect(view.getByRole("alert").textContent).toContain("활동 데이터를 표시할 수 없어요"));
  expect(view.queryByRole("grid")).toBeNull();
});
