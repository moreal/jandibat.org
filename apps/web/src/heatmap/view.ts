import type {
  ActivityDayDto,
  ActivityEntryDto,
  ActivityTimelineResponseDto,
  EnvironmentDto,
} from "@jandibat/contracts";
import {
  buildHeatmapCalendar,
  formatActivityDate,
  heatmapSummary,
} from "./calendar.ts";

function appendTextElement<K extends keyof HTMLElementTagNameMap>(
  document: Document,
  parent: Node,
  tag: K,
  text: string,
  className?: string,
): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (className) element.className = className;
  element.textContent = text;
  parent.appendChild(element);
  return element;
}

function environmentName(
  entry: ActivityEntryDto,
  environments: ReadonlyMap<string, EnvironmentDto>,
): string {
  return environments.get(entry.environmentId)?.name ?? entry.environmentId;
}

function entryLabel(
  entry: ActivityEntryDto,
  environments: ReadonlyMap<string, EnvironmentDto>,
): string {
  return `${environmentName(entry, environments)} ${entry.action} ${entry.metric.value} ${entry.metric.name}`;
}

function dayLabel(
  day: ActivityDayDto,
  environments: ReadonlyMap<string, EnvironmentDto>,
): string {
  const detail = day.entries.map((entry) => entryLabel(entry, environments)).join(", ");
  return `${formatActivityDate(day.date)}: 활동 ${day.count}개${detail ? ` · ${detail}` : ""}`;
}

function levelClass(level: number): number {
  if (!Number.isFinite(level)) return 0;
  return Math.max(0, Math.min(4, Math.round(level)));
}

function appendEnvironmentLegend(
  document: Document,
  container: HTMLElement,
  days: readonly ActivityDayDto[],
  environments: readonly EnvironmentDto[],
): void {
  const totals = new Map<string, number>();
  for (const day of days) {
    for (const entry of day.entries) {
      totals.set(
        entry.environmentId,
        (totals.get(entry.environmentId) ?? 0) + entry.metric.value,
      );
    }
  }
  const visible = environments
    .filter((environment) => totals.has(environment.id))
    .sort((left, right) => (totals.get(right.id) ?? 0) - (totals.get(left.id) ?? 0));
  if (visible.length === 0) return;
  const list = document.createElement("ul");
  list.className = "environment-list";
  list.setAttribute("aria-label", "환경별 활동 합계");
  for (const environment of visible) {
    const item = document.createElement("li");
    appendTextElement(document, item, "span", environment.name);
    appendTextElement(
      document,
      item,
      "strong",
      (totals.get(environment.id) ?? 0).toLocaleString("ko-KR"),
    );
    list.append(item);
  }
  container.append(list);
}

function appendCallout(
  document: Document,
  container: HTMLElement,
  className: string,
  title: string,
  copy: string,
): void {
  const callout = document.createElement("div");
  callout.className = `callout ${className}`;
  callout.setAttribute("role", "status");
  appendTextElement(document, callout, "strong", title);
  appendTextElement(document, callout, "p", copy);
  container.append(callout);
}

/**
 * Renders API timeline data exclusively through DOM text and attribute APIs.
 * Runtime values never cross an HTML parsing boundary.
 */
export function renderHeatmap(
  container: HTMLElement,
  timeline: ActivityTimelineResponseDto,
): void {
  const document = container.ownerDocument;
  container.replaceChildren();
  const content = document.createElement("div");
  content.className = "heatmap-content";
  container.append(content);
  const calendar = buildHeatmapCalendar(timeline.days, timeline.to);
  const visible = timeline.days.filter(
    (day) => day.date >= timeline.from && day.date <= timeline.to,
  );
  const summary = heatmapSummary(visible, timeline.to);
  const empty = visible.every((day) => day.count === 0);
  const environments = new Map(
    timeline.environments.map((environment) => [environment.id, environment]),
  );

  const monthLabelsByColumn = new Map(
    calendar.monthLabels.map(({ label, column }) => [column, label]),
  );
  const generatedAt = new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(timeline.generatedAt));

  if (timeline.stale) {
    appendCallout(
      document,
      content,
      "stale-callout",
      "일부 데이터가 최신이 아닐 수 있어요.",
      "외부 서비스 갱신에 실패해 마지막으로 저장된 기록을 보여드립니다.",
    );
  }
  if (empty) {
    appendCallout(
      document,
      content,
      "empty-timeline-callout",
      "아직 표시할 활동이 없어요.",
      "Provider를 연결하거나 커스텀 활동을 추가하면 이 기간에 기록이 나타납니다.",
    );
  }

  const heading = document.createElement("div");
  heading.className = "heatmap-heading";
  const headingCopy = document.createElement("div");
  appendTextElement(document, headingCopy, "p", "최근 1년의 기록", "eyebrow");
  const title = document.createElement("h2");
  appendTextElement(document, title, "span", `@${timeline.subject}`, "subject-mark");
  title.append(document.createTextNode("의 잔디밭"));
  headingCopy.append(title);
  const total = document.createElement("div");
  total.className = "heatmap-total";
  appendTextElement(document, total, "strong", summary.total.toLocaleString("ko-KR"));
  appendTextElement(document, total, "span", "번의 활동");
  heading.append(headingCopy, total);
  content.append(heading);

  const scroll = document.createElement("div");
  scroll.className = "heatmap-scroll";
  scroll.tabIndex = 0;
  scroll.setAttribute(
    "aria-label",
    `${timeline.subject}의 최근 1년 활동 그래프. 가로로 스크롤할 수 있습니다.`,
  );
  const chart = document.createElement("div");
  chart.className = "heatmap-chart";
  const months = document.createElement("div");
  months.className = "heatmap-months";
  months.setAttribute("aria-hidden", "true");
  for (let column = 0; column < 53; column += 1) {
    appendTextElement(
      document,
      months,
      "span",
      monthLabelsByColumn.get(column) ?? "",
      "heatmap-month",
    );
  }

  const body = document.createElement("div");
  body.className = "heatmap-body";
  const weekdays = document.createElement("div");
  weekdays.className = "heatmap-weekdays";
  weekdays.setAttribute("aria-hidden", "true");
  for (const weekday of ["월", "수", "금"]) {
    appendTextElement(document, weekdays, "span", weekday);
  }
  const grid = document.createElement("div");
  grid.className = "heatmap-grid";
  grid.setAttribute("role", "grid");
  grid.setAttribute("aria-label", "일별 활동");
  for (const week of calendar.weeks) {
    const column = document.createElement("div");
    column.className = "heatmap-week";
    column.setAttribute("role", "row");
    for (const cell of week.cells) {
      if (!cell.inRange) {
        const outside = document.createElement("span");
        outside.className = "heatmap-cell is-outside";
        outside.setAttribute("aria-hidden", "true");
        column.append(outside);
        continue;
      }
      const day = cell.day ?? { date: cell.date, count: 0, level: 0, entries: [] };
      const label = dayLabel(day, environments);
      const button = document.createElement("button");
      button.className = `heatmap-cell level-${levelClass(day.level)}`;
      button.type = "button";
      button.setAttribute("role", "gridcell");
      button.tabIndex = -1;
      button.setAttribute("aria-label", label);
      button.setAttribute("aria-describedby", "heatmap-tooltip");
      button.dataset.tooltip = label;
      button.dataset.date = day.date;
      column.append(button);
    }
    grid.append(column);
  }
  body.append(weekdays, grid);
  chart.append(months, body);
  scroll.append(chart);
  content.append(scroll);

  const tooltip = document.createElement("div");
  tooltip.className = "heatmap-tooltip";
  tooltip.id = "heatmap-tooltip";
  tooltip.setAttribute("role", "tooltip");
  tooltip.dataset.state = "closed";
  tooltip.hidden = true;
  container.append(tooltip);

  const footer = document.createElement("div");
  footer.className = "heatmap-footer";
  const timestamp = document.createElement("p");
  timestamp.append(
    document.createTextNode(`${timeline.timezone} 기준 · ${timeline.from} — ${timeline.to}`),
    document.createElement("br"),
    document.createTextNode(`마지막 생성 ${generatedAt}`),
  );
  const legend = document.createElement("div");
  legend.className = "heatmap-legend";
  legend.setAttribute("aria-label", "활동 강도 범례");
  appendTextElement(document, legend, "span", "적음");
  for (let level = 0; level <= 4; level += 1) {
    const swatch = document.createElement("i");
    swatch.className = `level-${level}`;
    legend.append(swatch);
  }
  appendTextElement(document, legend, "span", "많음");
  footer.append(timestamp, legend);
  content.append(footer);

  const summaries = document.createElement("dl");
  summaries.className = "summary-grid";
  const appendSummary = (term: string, value: string, suffix?: string, className?: string) => {
    const item = document.createElement("div");
    appendTextElement(document, item, "dt", term);
    const detail = appendTextElement(document, item, "dd", value, className);
    if (suffix) appendTextElement(document, detail, "span", suffix);
    summaries.append(item);
    return detail;
  };
  appendSummary("활동한 날", String(summary.activeDays), "일");
  appendSummary("현재 연속 기록", String(summary.currentStreak), "일");
  if (summary.bestDay && summary.bestDay.count > 0) {
    appendSummary("가장 활발한 날", summary.bestDay.date, `${summary.bestDay.count}회`, "summary-date");
  } else {
    appendSummary("가장 활발한 날", "—", undefined, "summary-date");
  }
  content.append(summaries);
  appendEnvironmentLegend(document, content, visible, timeline.environments);
}

export function bindHeatmapTooltip(container: HTMLElement): void {
  const tooltip = container.querySelector<HTMLElement>(".heatmap-tooltip");
  if (!tooltip) return;
  const cells = Array.from(
    container.querySelectorAll<HTMLButtonElement>("button.heatmap-cell[data-tooltip]"),
  ).sort((left, right) =>
    (left.dataset.date ?? "").localeCompare(right.dataset.date ?? ""),
  );
  cells.at(-1)?.setAttribute("tabindex", "0");
  let hideTimer: number | undefined;
  let revealFrame: number | undefined;

  const show = (target: HTMLElement, animate: boolean) => {
    const message = target.dataset.tooltip;
    if (!message) return;
    if (hideTimer !== undefined) {
      window.clearTimeout(hideTimer);
      hideTimer = undefined;
    }
    if (revealFrame !== undefined) {
      window.cancelAnimationFrame(revealFrame);
      revealFrame = undefined;
    }
    const wasHidden = tooltip.hidden;
    tooltip.textContent = message;
    if (wasHidden) tooltip.dataset.state = animate ? "closed" : "open";
    tooltip.hidden = false;
    const bounds = target.getBoundingClientRect();
    const width = Math.min(360, Math.max(160, tooltip.offsetWidth));
    tooltip.style.width = `${width}px`;
    tooltip.style.left = `${Math.max(8, Math.min(window.innerWidth - width - 8, bounds.left + bounds.width / 2 - width / 2))}px`;
    tooltip.style.top = `${Math.max(8, bounds.top - tooltip.offsetHeight - 10)}px`;
    if (wasHidden && animate) {
      revealFrame = window.requestAnimationFrame(() => {
        tooltip.dataset.state = "open";
        revealFrame = undefined;
      });
    } else {
      tooltip.dataset.state = "open";
    }
  };

  const hide = (animate: boolean) => {
    if (revealFrame !== undefined) {
      window.cancelAnimationFrame(revealFrame);
      revealFrame = undefined;
    }
    if (hideTimer !== undefined) {
      window.clearTimeout(hideTimer);
      hideTimer = undefined;
    }
    tooltip.dataset.state = "closed";
    if (!animate) {
      tooltip.hidden = true;
      return;
    }
    hideTimer = window.setTimeout(() => {
      tooltip.hidden = true;
      hideTimer = undefined;
    }, 140);
  };

  container.addEventListener("pointerover", (event) => {
    const target = (event.target as HTMLElement).closest<HTMLElement>("[data-tooltip]");
    if (target) show(target, true);
  });
  container.addEventListener("pointerout", (event) => {
    const related = event.relatedTarget;
    if (!(related instanceof Element) || !related.closest("[data-tooltip]")) hide(true);
  });
  container.addEventListener("pointerleave", () => hide(true));
  container.addEventListener("click", (event) => {
    const target = (event.target as HTMLElement).closest<HTMLElement>("[data-tooltip]");
    if (target) show(target, true);
  });
  container.addEventListener("focusin", (event) => {
    const target = (event.target as HTMLElement).closest<HTMLElement>("[data-tooltip]");
    if (target) show(target, false);
  });
  container.addEventListener("focusout", () => hide(false));
  container.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      hide(false);
      return;
    }
    const target = (event.target as HTMLElement).closest<HTMLButtonElement>(
      "button.heatmap-cell[data-tooltip]",
    );
    if (!target) return;
    const current = cells.indexOf(target);
    const movement: Partial<Record<string, number>> = {
      ArrowUp: -1,
      ArrowDown: 1,
      ArrowLeft: -7,
      ArrowRight: 7,
    };
    const offset = movement[event.key];
    if (offset === undefined) return;
    const next = cells[current + offset];
    if (!next) return;
    event.preventDefault();
    target.tabIndex = -1;
    next.tabIndex = 0;
    next.focus();
  });
}
