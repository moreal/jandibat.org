import type { ActivityTimelineResponseDto } from "@jandibat/contracts";
import { useNavigate } from "@solidjs/router";
import { Match, Show, Switch, createEffect, createMemo, createSignal } from "solid-js";
import { useAppState } from "../app/state";
import { ErrorCallout, Icon } from "../components/common";
import { buildHeatmapCalendar, todayDateKey } from "../heatmap/calendar";
import { Heatmap } from "../heatmap/Heatmap";
import { createRelayQuery } from "../relay";
import type { ExploreActivityQuery } from "./__generated__/ExploreActivityQuery.graphql";
import query from "./__generated__/ExploreActivityQuery.graphql";

function activityLong(value: unknown): number {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new Error("Invalid activity Long");
  }
  const number = Number(value);
  if (!Number.isSafeInteger(number)) throw new Error("Activity Long exceeds safe integer range");
  return number;
}

function safeSum(total: number, value: number): number {
  const sum = total + value;
  if (!Number.isSafeInteger(sum)) throw new Error("Activity sum exceeds safe integer range");
  return sum;
}

function timelineFromSnapshot(
  subject: NonNullable<ExploreActivityQuery["response"]["subject"]>,
  timezone: string,
): ActivityTimelineResponseDto {
  const snapshot = subject.activitySnapshot;
  let total = 0;
  const environmentTotals = new Map<string, number>();
  const days = snapshot.days.map((day) => {
    const count = activityLong(day.count);
    total = safeSum(total, count);
    return {
      date: day.date,
      count,
      level: day.level,
      entries: day.entries.map((entry) => {
        const value = activityLong(entry.metricValue);
        environmentTotals.set(entry.environmentID, safeSum(environmentTotals.get(entry.environmentID) ?? 0, value));
        return {
          environmentId: entry.environmentID,
          action: entry.action,
          metric: { name: entry.metricName, value },
          metadata: Object.fromEntries(entry.metadata.map((item) => [item.key, item.value])),
        };
      }),
    };
  });
  return {
    subject: subject.handle,
    timezone,
    from: snapshot.range.from,
    to: snapshot.range.to,
    generatedAt: snapshot.generatedAt,
    // ActivitySnapshot SDL has no refresh-status field, so the legacy stale banner cannot be derived here.
    stale: false,
    days,
    environments: snapshot.environments.map((environment) => ({
      id: environment.id,
      key: environment.key,
      name: environment.name,
      scope: environment.scope === "subject" ? "subject" : "global",
      metadata: Object.fromEntries(environment.metadata.map((item) => [item.key, item.value])),
    })),
  };
}

function browserTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function ActivityResult(props: { subject: string }) {
  const [retryNonce, setRetryNonce] = createSignal(0);
  const timezone = browserTimezone();
  const range = createMemo(() => {
    const { from, to } = buildHeatmapCalendar([], todayDateKey());
    return { from, to };
  });
  const result = createRelayQuery<ExploreActivityQuery>(query, () => ({
    handle: props.subject,
    range: range(),
    timezone,
  }), { fetchKey: retryNonce });
  const projection = createMemo(() => {
    if (result.error || result.pending) return { kind: "loading" } as const;
    const data = result();
    if (!data) return { kind: "loading" } as const;
    if (!data.subject) return { kind: "missing" } as const;
    try {
      return { kind: "ready", timeline: timelineFromSnapshot(data.subject, timezone) } as const;
    } catch {
      return { kind: "invalid" } as const;
    }
  });

  return (
    <Switch>
      <Match when={result.error}>
        <ErrorCallout error={result.error} retry={() => setRetryNonce((value) => value + 1)} />
      </Match>
      <Match when={projection().kind === "loading"}>
        <div class="heatmap-loading" role="status">
          <div><span /><span /><span /><span /></div>
          <p>@{props.subject}의 기록을 불러오는 중…</p>
        </div>
      </Match>
      <Match when={projection().kind === "invalid"}>
        <ErrorCallout error={new Error("활동 데이터를 표시할 수 없어요. 잠시 후 다시 시도해 주세요.")} />
      </Match>
      <Match when={projection().kind === "ready"}>
        <Heatmap timeline={(projection() as Extract<ReturnType<typeof projection>, { kind: "ready" }>).timeline} />
      </Match>
      <Match when={projection().kind === "missing"}>
        <div class="empty-heatmap" role="alert">
          <span class="empty-sprout" aria-hidden="true" />
          <h2>@{props.subject}의 공개 잔디밭을 찾을 수 없어요.</h2>
          <p>프로필 주소를 확인하거나 공개 설정을 확인해 주세요.</p>
        </div>
      </Match>
    </Switch>
  );
}

export function ExplorePage(props: { subject?: string }) {
  const app = useAppState();
  const navigate = useNavigate();
  const subject = createMemo(() => (props.subject ?? app.exploreSubject()).trim());
  createEffect(() => subject(), (handle) => {
    if (handle) app.setExploreSubject(handle);
  });

  const search = (event: SubmitEvent) => {
    event.preventDefault();
    const handle = String(
      new FormData(event.currentTarget as HTMLFormElement).get("subject") ?? "",
    ).trim();
    if (!handle) return;
    app.setExploreSubject(handle);
    navigate(`/explore/${encodeURIComponent(handle)}`);
  };

  return (
    <>
      <section class="hero">
        <div class="hero-copy">
          <p class="eyebrow">Your work, in full color.</p>
          <h1>매일의 작은 기록이<br /><em>나만의 잔디밭</em>이<span class="hero-ending"> 됩니다.</span></h1>
          <p class="hero-description">
            GitHub부터 독서, 운동까지. 흩어진 활동을 한곳에 모아 오래 보고 싶은 기록으로 남겨보세요.
          </p>
          <form class="subject-search" id="subject-search" onSubmit={search}>
            <label for="subject">공개 프로필 찾아보기</label>
            <div>
              <span aria-hidden="true">@</span>
              <input
                id="subject"
                name="subject"
                autocomplete="off"
                autocapitalize="none"
                spellcheck={false}
                placeholder="github-user"
                maxlength="64"
                pattern="[A-Za-z0-9][A-Za-z0-9._-]*"
                value={subject()}
                required
              />
              <button class="primary-button" type="submit">
                잔디밭 보기 <Icon name="arrow" />
              </button>
            </div>
          </form>
          <div class="trust-row" aria-label="지원하는 데이터 소스">
            <span>GITHUB</span><span>GITLAB</span><span>CODEBERG</span><span>+ YOUR DATA</span>
          </div>
        </div>
        <aside class="hero-note" aria-label="제품 소개">
          <span class="note-number">365</span><span>DAYS</span>
          <p>오늘 심은 기록은<br />내일의 방향이 됩니다.</p>
          <i aria-hidden="true" />
        </aside>
      </section>

      <section class="heatmap-section section-shell" aria-live="polite">
        <Show when={subject()} fallback={
            <div class="heatmap-loading" role="status">
              <div><span /><span /><span /><span /></div>
              <p>보고 싶은 공개 프로필을 입력해 주세요.</p>
            </div>
          }>
          {(handle) => <ActivityResult subject={handle()} />}
        </Show>
      </section>

      <section class="feature-strip" aria-label="jandibat의 특징">
        <article><span>01</span><h2>한눈에</h2><p>일 년의 흐름을 한 화면에서 발견하세요.</p></article>
        <article><span>02</span><h2>모두 함께</h2><p>코드 밖의 활동도 같은 언어로 기록하세요.</p></article>
        <article><span>03</span><h2>어디서나</h2><p>README와 블로그에 살아 있는 기록을 공유하세요.</p></article>
      </section>
    </>
  );
}
