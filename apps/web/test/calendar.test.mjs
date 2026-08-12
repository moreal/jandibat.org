import assert from "node:assert/strict";
import test from "node:test";

import {
  buildHeatmapCalendar,
  formatDateKey,
  heatmapSummary,
  parseDateKey,
} from "../src/heatmap/calendar.ts";

function activity(date, count, level = 1) {
  return { date, count, level, entries: [] };
}

test("date keys round-trip and reject impossible calendar dates", () => {
  assert.equal(formatDateKey(parseDateKey("2024-02-29")), "2024-02-29");
  assert.throws(() => parseDateKey("2023-02-29"), /Invalid date key/);
  assert.throws(() => parseDateKey("2026-8-1"), /Invalid date key/);
});

test("the heatmap always covers exactly the latest 365 days in 53 week columns", () => {
  const calendar = buildHeatmapCalendar([], "2026-08-12");
  const visible = calendar.weeks.flatMap((week) => week.cells).filter((cell) => cell.inRange);

  assert.equal(calendar.weeks.length, 53);
  assert.equal(visible.length, 365);
  assert.equal(calendar.from, "2025-08-13");
  assert.equal(calendar.to, "2026-08-12");
  assert.equal(visible.at(0)?.date, calendar.from);
  assert.equal(visible.at(-1)?.date, calendar.to);
});

test("summary totals, best day, and streak use the requested timeline boundary", () => {
  const days = [
    activity("2026-08-08", 9, 4),
    activity("2026-08-10", 1),
    activity("2026-08-11", 2, 2),
    activity("2026-08-12", 0, 0),
  ];

  const summary = heatmapSummary(days, "2026-08-12");
  assert.equal(summary.total, 12);
  assert.equal(summary.activeDays, 3);
  assert.equal(summary.bestDay?.date, "2026-08-08");
  assert.equal(summary.currentStreak, 2);
});
