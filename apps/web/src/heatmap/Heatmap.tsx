import type {
  ActivityDayDto,
  ActivityEntryDto,
  ActivityTimelineResponseDto,
  EnvironmentDto,
} from "@jandibat/contracts";
import { For, Show, createMemo, createSignal } from "solid-js";
import {
  buildHeatmapCalendar,
  formatActivityDate,
  heatmapSummary,
} from "./calendar";

type TooltipState = {
  message: string;
  left: number;
  top: number;
};

function environmentName(
  entry: ActivityEntryDto,
  environments: ReadonlyMap<string, EnvironmentDto>,
): string {
  return environments.get(entry.environmentId)?.name ?? entry.environmentId;
}

function dayLabel(
  day: ActivityDayDto,
  environments: ReadonlyMap<string, EnvironmentDto>,
): string {
  const detail = day.entries
    .map((entry) => (
      `${environmentName(entry, environments)} ${entry.action} ${entry.metric.value} ${entry.metric.name}`
    ))
    .join(", ");
  return `${formatActivityDate(day.date)}: 활동 ${day.count}개${detail ? ` · ${detail}` : ""}`;
}

function levelClass(level: number): number {
  if (!Number.isFinite(level)) return 0;
  return Math.max(0, Math.min(4, Math.round(level)));
}

export function Heatmap(props: { timeline: ActivityTimelineResponseDto }) {
  const calendar = createMemo(() => buildHeatmapCalendar(props.timeline.days, props.timeline.to));
  const visibleDays = createMemo(() => props.timeline.days.filter(
    (day) => day.date >= props.timeline.from && day.date <= props.timeline.to,
  ));
  const summary = createMemo(() => heatmapSummary(visibleDays(), props.timeline.to));
  const environmentMap = createMemo(() => new Map(
    props.timeline.environments.map((environment) => [environment.id, environment]),
  ));
  const monthLabels = createMemo(() => new Map(
    calendar().monthLabels.map(({ label, column }) => [column, label]),
  ));
  const visibleEnvironments = createMemo(() => {
    const totals = new Map<string, number>();
    for (const day of visibleDays()) {
      for (const entry of day.entries) {
        totals.set(entry.environmentId, (totals.get(entry.environmentId) ?? 0) + entry.metric.value);
      }
    }
    return props.timeline.environments
      .filter((environment) => totals.has(environment.id))
      .sort((left, right) => (totals.get(right.id) ?? 0) - (totals.get(left.id) ?? 0))
      .map((environment) => ({
        environment,
        total: totals.get(environment.id) ?? 0,
      }));
  });
  const [tooltip, setTooltip] = createSignal<TooltipState>();

  const showTooltip = (target: HTMLButtonElement) => {
    const message = target.dataset.tooltip;
    if (!message) return;
    const bounds = target.getBoundingClientRect();
    const width = 260;
    setTooltip({
      message,
      left: Math.max(8, Math.min(window.innerWidth - width - 8, bounds.left + bounds.width / 2 - width / 2)),
      top: Math.max(8, bounds.top - 48),
    });
  };

  const navigateGrid = (event: KeyboardEvent) => {
    if (event.key === "Escape") {
      setTooltip(undefined);
      return;
    }
    const target = event.currentTarget as HTMLButtonElement;
    const grid = target.closest(".heatmap-grid");
    if (!grid) return;
    const cells = Array.from(
      grid.querySelectorAll<HTMLButtonElement>("button.heatmap-cell[data-date]"),
    ).sort((left, right) => (left.dataset.date ?? "").localeCompare(right.dataset.date ?? ""));
    const movement: Partial<Record<string, number>> = {
      ArrowUp: -1,
      ArrowDown: 1,
      ArrowLeft: -7,
      ArrowRight: 7,
    };
    const offset = movement[event.key];
    if (offset === undefined) return;
    const next = cells[cells.indexOf(target) + offset];
    if (!next) return;
    event.preventDefault();
    target.tabIndex = -1;
    next.tabIndex = 0;
    next.focus();
  };

  const generatedAt = () => new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(props.timeline.generatedAt));

  return (
    <>
      <div class="heatmap-content">
        <Show when={props.timeline.stale}>
          <div class="callout stale-callout" role="status">
            <strong>일부 데이터가 최신이 아닐 수 있어요.</strong>
            <p>외부 서비스 갱신에 실패해 마지막으로 저장된 기록을 보여드립니다.</p>
          </div>
        </Show>
        <Show when={visibleDays().every((day) => day.count === 0)}>
          <div class="callout empty-timeline-callout" role="status">
            <strong>아직 표시할 활동이 없어요.</strong>
            <p>Provider를 연결하거나 커스텀 활동을 추가하면 이 기간에 기록이 나타납니다.</p>
          </div>
        </Show>

        <div class="heatmap-heading">
          <div>
            <p class="eyebrow">최근 1년의 기록</p>
            <h2><span class="subject-mark">@{props.timeline.subject}</span>의 잔디밭</h2>
          </div>
          <div class="heatmap-total">
            <strong>{summary().total.toLocaleString("ko-KR")}</strong>
            <span>번의 활동</span>
          </div>
        </div>

        <div
          class="heatmap-scroll"
          tabindex="0"
          aria-label={`${props.timeline.subject}의 최근 1년 활동 그래프. 가로로 스크롤할 수 있습니다.`}
        >
          <div class="heatmap-chart">
            <div class="heatmap-months" aria-hidden="true">
              <For each={Array.from({ length: 53 }, (_, column) => column)}>
                {(column) => <span class="heatmap-month">{monthLabels().get(column) ?? ""}</span>}
              </For>
            </div>
            <div class="heatmap-body">
              <div class="heatmap-weekdays" aria-hidden="true">
                <span>월</span><span>수</span><span>금</span>
              </div>
              <div class="heatmap-grid" role="grid" aria-label="일별 활동">
                <For each={calendar().weeks}>
                  {(week) => (
                    <div class="heatmap-week" role="row">
                      <For each={week.cells}>
                        {(cell) => {
                          const day = createMemo(() => cell.day ?? {
                            date: cell.date,
                            count: 0,
                            level: 0,
                            entries: [],
                          });
                          const label = createMemo(() => dayLabel(day(), environmentMap()));
                          return (
                            <Show when={cell.inRange} fallback={<span class="heatmap-cell is-outside" aria-hidden="true" />}>
                              <button
                                class={`heatmap-cell level-${levelClass(day().level)}`}
                                type="button"
                                role="gridcell"
                                tabindex={day().date === props.timeline.to ? 0 : -1}
                                aria-label={label()}
                                aria-describedby="heatmap-tooltip"
                                data-tooltip={label()}
                                data-date={day().date}
                                onPointerOver={(event) => showTooltip(event.currentTarget)}
                                onPointerOut={() => setTooltip(undefined)}
                                onClick={(event) => showTooltip(event.currentTarget)}
                                onFocus={(event) => showTooltip(event.currentTarget)}
                                onBlur={() => setTooltip(undefined)}
                                onKeyDown={navigateGrid}
                              />
                            </Show>
                          );
                        }}
                      </For>
                    </div>
                  )}
                </For>
              </div>
            </div>
          </div>
        </div>

        <div class="heatmap-footer">
          <p>
            {props.timeline.timezone} 기준 · {props.timeline.from} — {props.timeline.to}<br />
            마지막 생성 {generatedAt()}
          </p>
          <div class="heatmap-legend" aria-label="활동 강도 범례">
            <span>적음</span>
            <For each={[0, 1, 2, 3, 4]}>{(level) => <i class={`level-${level}`} />}</For>
            <span>많음</span>
          </div>
        </div>

        <dl class="summary-grid">
          <div><dt>활동한 날</dt><dd>{summary().activeDays}<span>일</span></dd></div>
          <div><dt>현재 연속 기록</dt><dd>{summary().currentStreak}<span>일</span></dd></div>
          <div>
            <dt>가장 활발한 날</dt>
            <Show
              when={summary().bestDay && summary().bestDay!.count > 0 ? summary().bestDay : undefined}
              fallback={<dd class="summary-date">—</dd>}
            >
              {(bestDay) => (
                <dd class="summary-date">{bestDay().date}<span>{bestDay().count}회</span></dd>
              )}
            </Show>
          </div>
        </dl>

        <Show when={visibleEnvironments().length > 0}>
          <ul class="environment-list" aria-label="환경별 활동 합계">
            <For each={visibleEnvironments()}>
              {(item) => (
                <li>
                  <span>{item.environment.name}</span>
                  <strong>{item.total.toLocaleString("ko-KR")}</strong>
                </li>
              )}
            </For>
          </ul>
        </Show>
      </div>
      <div
        class="heatmap-tooltip"
        id="heatmap-tooltip"
        role="tooltip"
        data-state={tooltip() ? "open" : "closed"}
        hidden={!tooltip()}
        style={tooltip()
          ? `width:260px;left:${tooltip()!.left}px;top:${tooltip()!.top}px`
          : undefined}
      >
        {tooltip()?.message}
      </div>
    </>
  );
}
