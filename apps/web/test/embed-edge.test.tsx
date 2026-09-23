import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { afterEach, expect, it, vi } from "vitest";
import { AppStateProvider } from "../src/app/state";
import { EmbedPage } from "../src/pages/EmbedPage";

afterEach(() => {
  cleanup();
  localStorage.clear();
  delete window.__JANDIBAT_CONFIG__;
  vi.unstubAllGlobals();
});

function mount(subject: string) {
  localStorage.setItem("jandibat:owner-subject", subject);
  window.__JANDIBAT_CONFIG__ = { apiBaseUrl: "https://api.example.test/prefix" };
  const fetch = vi.fn(() => { throw new Error("Unexpected network request"); });
  vi.stubGlobal("fetch", fetch);
  return { view: render(() => <AppStateProvider><EmbedPage /></AppStateProvider>), fetch };
}

it.each(["  \r\n\t  ", "bad handle", "../garden"])(
  "does not preview or copy an invalid persisted subject (%s)",
  (subject) => {
    const { view, fetch } = mount(subject);
    expect(view.container.querySelector(".embed-preview img")).toBeNull();
    expect(view.queryByRole("button", { name: "코드 복사" })).toBeNull();
    expect(view.container.textContent).not.toContain("/v1/render/.svg");
    expect(fetch).not.toHaveBeenCalled();
  },
);

it("uses the validated runtime path prefix for SVG preview and copy without a domain API request", async () => {
  const { view, fetch } = mount("garden-1");
  const preview = view.container.querySelector<HTMLImageElement>(".embed-preview img");
  expect(preview?.src).toBe(
    "https://api.example.test/prefix/v1/render/garden-1.svg?theme=system&weekStart=sunday&cellSize=11&showLegend=true",
  );
  const copied: string[] = [];
  vi.stubGlobal("navigator", {
    ...navigator,
    clipboard: { writeText: async (value: string) => { copied.push(value); } },
  });
  await fireEvent.click(view.getByRole("button", { name: "코드 복사" }));
  expect(copied).toEqual([
    "[![garden-1's activity](https://api.example.test/prefix/v1/render/garden-1.svg?theme=system&weekStart=sunday&cellSize=11&showLegend=true)](http://localhost:3000/explore/garden-1)",
  ]);
  expect(fetch).not.toHaveBeenCalled();
});
