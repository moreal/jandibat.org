import type { ActivityTimelineResponseDto } from "@jandibat/contracts";
import { useNavigate } from "@solidjs/router";
import { Match, Switch, createEffect, createMemo, createSignal } from "solid-js";
import { api } from "../api/client";
import { useAppState } from "../app/state";
import { ErrorCallout, Icon } from "../components/common";
import { buildHeatmapCalendar, todayDateKey } from "../heatmap/calendar";
import { Heatmap } from "../heatmap/Heatmap";

type TimelineState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; timeline: ActivityTimelineResponseDto }
  | { kind: "error"; error: unknown };

export function ExplorePage(props: { subject?: string }) {
  const app = useAppState();
  const navigate = useNavigate();
  const subject = createMemo(() => (props.subject ?? app.exploreSubject()).trim());
  const [timeline, setTimeline] = createSignal<TimelineState>({ kind: "idle" });
  let controller: AbortController | undefined;

  const load = async (handle: string) => {
    controller?.abort();
    if (!handle) {
      setTimeline({ kind: "idle" });
      return;
    }
    app.setExploreSubject(handle);
    controller = new AbortController();
    setTimeline({ kind: "loading" });
    const calendar = buildHeatmapCalendar([], todayDateKey());
    try {
      const response = await api.getActivities(handle, {
        from: calendar.from,
        to: calendar.to,
        signal: controller.signal,
      });
      setTimeline({ kind: "ready", timeline: response });
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      setTimeline({ kind: "error", error });
    }
  };

  createEffect(
    () => subject(),
    (handle) => {
      void load(handle);
      return () => controller?.abort();
    },
  );

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
        <Switch>
          <Match when={timeline().kind === "idle"}>
            <div class="heatmap-loading" role="status">
              <div><span /><span /><span /><span /></div>
              <p>보고 싶은 공개 프로필을 입력해 주세요.</p>
            </div>
          </Match>
          <Match when={timeline().kind === "loading"}>
            <div class="heatmap-loading" role="status">
              <div><span /><span /><span /><span /></div>
              <p>@{subject()}의 기록을 불러오는 중…</p>
            </div>
          </Match>
          <Match when={timeline().kind === "ready"}>
            <Heatmap
              timeline={(timeline() as Extract<TimelineState, { kind: "ready" }>).timeline}
            />
          </Match>
          <Match when={timeline().kind === "error"}>
            <ErrorCallout
              error={(timeline() as Extract<TimelineState, { kind: "error" }>).error}
              retry={() => void load(subject())}
            />
            <div class="empty-heatmap">
              <span class="empty-sprout" aria-hidden="true" />
              <h2>@{subject()}의 잔디밭을 기다리고 있어요.</h2>
              <p>API가 연결되면 최근 1년 활동이 이곳에 표시됩니다.</p>
            </div>
          </Match>
        </Switch>
      </section>

      <section class="feature-strip" aria-label="jandibat의 특징">
        <article><span>01</span><h2>한눈에</h2><p>일 년의 흐름을 한 화면에서 발견하세요.</p></article>
        <article><span>02</span><h2>모두 함께</h2><p>코드 밖의 활동도 같은 언어로 기록하세요.</p></article>
        <article><span>03</span><h2>어디서나</h2><p>README와 블로그에 살아 있는 기록을 공유하세요.</p></article>
      </section>
    </>
  );
}
