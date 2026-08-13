import assert from "node:assert/strict";
import test from "node:test";
import { parseHTML } from "linkedom";

import { renderHeatmap } from "../src/heatmap/view.ts";

function container() {
  const { document } = parseHTML("<!doctype html><html><body><main></main></body></html>");
  return document.querySelector("main");
}

test("API strings remain text and DOM attributes at the heatmap boundary", () => {
  const attack = `<img src=x onerror="alert(1)">\" autofocus onfocus="alert(2)`;
  const root = container();
  renderHeatmap(root, {
    subject: attack,
    timezone: attack,
    from: "2025-08-13",
    to: "2026-08-12",
    generatedAt: "2026-08-12T00:00:00Z",
    stale: false,
    environments: [{
      id: "environment-1",
      key: "malicious",
      name: attack,
      scope: "global",
      metadata: {},
    }],
    days: [{
      date: "2026-08-12",
      count: 1,
      level: 4,
      entries: [{
        environmentId: "environment-1",
        action: attack,
        metric: { name: attack, value: 1 },
        metadata: {},
      }],
    }, {
      date: `2026-08-11${attack}`,
      count: 99,
      level: 1,
      entries: [],
    }],
  });

  assert.equal(root.querySelector("img"), null);
  assert.equal(root.querySelector("[autofocus]"), null);
  assert.match(root.textContent, /<img src=x onerror="alert\(1\)">/);
  const cell = root.querySelector('[data-date="2026-08-12"]');
  assert.ok(cell);
  assert.equal(cell.getAttribute("aria-describedby"), "heatmap-tooltip");
  assert.equal(cell.tabIndex, -1);
  assert.match(cell.dataset.tooltip, /<img src=x onerror="alert\(1\)">/);
  assert.equal(cell.getAttribute("aria-label"), cell.dataset.tooltip);
  assert.match(root.querySelector(".summary-date").textContent, /2026-08-11<img/);
  const tooltip = root.querySelector('[role="tooltip"]');
  assert.ok(tooltip);
  assert.equal(tooltip.dataset.state, "closed");
  assert.equal(tooltip.hidden, true);
  assert.ok(root.querySelector(".heatmap-content"));
  assert.equal(root.querySelectorAll(".heatmap-month").length, 53);
});
