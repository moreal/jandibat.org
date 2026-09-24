import { cleanup, render, waitFor } from "@solidjs/testing-library";
import { afterEach, expect, it } from "vitest";
import { AppStateProvider, useAppState } from "../src/app/state";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

it("keeps subject handles as preferences without a second server-subject cache", async () => {
  let state!: ReturnType<typeof useAppState>;
  render(() => <AppStateProvider>{(() => {
    state = useAppState();
    return <span>{state.currentSubject()}</span>;
  })()}</AppStateProvider>);

  expect("ownedSubjects" in state).toBe(false);
  expect("setOwnedSubjects" in state).toBe(false);
  state.selectOwnerSubject({ handle: "garden" });
  await waitFor(() => expect(state.currentSubject()).toBe("garden"));
  expect(localStorage.getItem("jandibat:owner-subject")).toBe("garden");
  state.clearOwnerSubject();
  await waitFor(() => expect(state.currentSubject()).toBe(""));
});
