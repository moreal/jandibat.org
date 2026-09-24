import type { ActivityTimelineResponseDto } from "@jandibat/contracts";
import { cleanup, render } from "@solidjs/testing-library";
import { afterEach, describe, expect, it } from "vitest";
import { Heatmap } from "../src/heatmap/Heatmap";

afterEach(cleanup);

const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;

describe("Solid component trust boundaries", () => {
  it("renders heatmap activity strings as text and safe attributes", () => {
    const timeline: ActivityTimelineResponseDto = {
      subject: attack,
      timezone: attack,
      from: "2025-08-13",
      to: "2026-08-12",
      generatedAt: "2026-08-12T00:00:00Z",
      stale: false,
      environments: [{
        id: "environment-1", key: "malicious", name: attack, scope: "global", metadata: {},
      }],
      days: [{
        date: "2026-08-12", count: 1, level: 4,
        entries: [{
          environmentId: "environment-1", action: attack,
          metric: { name: attack, value: 1 }, metadata: {},
        }],
      }],
    };

    const { container } = render(() => <Heatmap timeline={timeline} />);

    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("[autofocus]")).toBeNull();
    expect(container.textContent).toContain(attack);
    const cell = container.querySelector<HTMLButtonElement>('[data-date="2026-08-12"]');
    expect(cell?.getAttribute("aria-label")).toContain(attack);
    expect(container.querySelectorAll(".heatmap-month")).toHaveLength(53);
  });
});
