import { For, Show, createMemo, createSignal, untrack } from "solid-js";
import { defaultApiBaseUrl } from "../api/runtime-base";
import { useAppState } from "../app/state";
import { CopyButton } from "../components/CopyButton";
import { PageIntro } from "../components/common";
import { buildEmbedValues, type EmbedFormat } from "../embed/model";

const formats: ReadonlyArray<{ id: EmbedFormat; label: string }> = [
  { id: "markdown", label: "Markdown" },
  { id: "html", label: "HTML" },
  { id: "url", label: "URL" },
];

function appBaseUrl(): string {
  return new URL(import.meta.env.BASE_URL, location.origin).href;
}

export function EmbedPage() {
  const app = useAppState();
  const suggestedSubject = untrack(() => app.currentSubject() || app.exploreSubject());
  const [subject, setSubject] = createSignal(suggestedSubject);
  const [title, setTitle] = createSignal(
    suggestedSubject ? `${suggestedSubject}'s activity` : "Activity heatmap",
  );
  const [theme, setTheme] = createSignal("system");
  const [weekStart, setWeekStart] = createSignal("sunday");
  const [cellSize, setCellSize] = createSignal(11);
  const [showLegend, setShowLegend] = createSignal(true);
  const [format, setFormat] = createSignal<EmbedFormat>("markdown");
  const [previewFailed, setPreviewFailed] = createSignal(false);

  const values = createMemo(() => buildEmbedValues({
    subject: subject(),
    title: title(),
    theme: theme(),
    weekStart: weekStart(),
    cellSize: cellSize(),
    showLegend: showLegend(),
    apiBaseUrl: defaultApiBaseUrl() || location.origin,
    appBaseUrl: appBaseUrl(),
  }));

  const selectTab = (next: EmbedFormat, focus = false) => {
    setFormat(next);
    if (focus) document.querySelector<HTMLButtonElement>(`#embed-tab-${next}`)?.focus();
  };

  const moveTab = (event: KeyboardEvent, current: number) => {
    const next = event.key === "ArrowRight"
      ? (current + 1) % formats.length
      : event.key === "ArrowLeft"
        ? (current - 1 + formats.length) % formats.length
        : event.key === "Home"
          ? 0
          : event.key === "End"
            ? formats.length - 1
            : -1;
    if (next < 0) return;
    event.preventDefault();
    selectTab(formats[next]!.id, true);
  };

  return (
    <section class="page section-shell narrow">
      <PageIntro
        eyebrow="Share your garden"
        title={<>당신의 기록을<br /><em>어디서나 살아 있게.</em></>}
        description="README나 블로그에 붙여 넣을 수 있는 SVG 주소를 만들어보세요. 활동이 동기화될 때마다 자동으로 업데이트됩니다."
      />
      <div class="embed-layout">
        <form class="form-card stack-form" onSubmit={(event) => event.preventDefault()}>
          <label for="embed-subject">프로필 식별자</label>
          <div class="input-prefix">
            <span>@</span>
            <input
              id="embed-subject"
              name="subject"
              value={subject()}
              onInput={(event) => {
                setSubject(event.currentTarget.value);
                setPreviewFailed(false);
              }}
              placeholder="my-garden"
              maxlength="64"
              pattern="[A-Za-z0-9][A-Za-z0-9._-]*"
              required
            />
          </div>
          <label for="embed-title">대체 텍스트</label>
          <input
            id="embed-title"
            name="title"
            value={title()}
            onInput={(event) => setTitle(event.currentTarget.value)}
            maxlength="200"
            required
          />
          <label for="embed-theme">테마</label>
          <select id="embed-theme" name="theme" value={theme()} onChange={(event) => {
            setTheme(event.currentTarget.value);
            setPreviewFailed(false);
          }}>
            <option value="system">시스템</option>
            <option value="light">라이트</option>
            <option value="dark">다크</option>
            <option value="github-light">GitHub 라이트</option>
            <option value="github-dark">GitHub 다크</option>
          </select>
          <label for="embed-week-start">한 주의 시작</label>
          <select id="embed-week-start" name="weekStart" value={weekStart()} onChange={(event) => {
            setWeekStart(event.currentTarget.value);
            setPreviewFailed(false);
          }}>
            <option value="sunday">일요일</option>
            <option value="monday">월요일</option>
          </select>
          <label for="embed-cell-size">셀 크기 <output>{cellSize()}px</output></label>
          <input
            id="embed-cell-size"
            name="cellSize"
            type="range"
            min="6"
            max="32"
            value={cellSize()}
            onInput={(event) => {
              setCellSize(event.currentTarget.valueAsNumber);
              setPreviewFailed(false);
            }}
          />
          <label class="check-row">
            <input
              name="showLegend"
              type="checkbox"
              checked={showLegend()}
              onChange={(event) => {
                setShowLegend(event.currentTarget.checked);
                setPreviewFailed(false);
              }}
            />
            <span>강도 범례 표시</span>
          </label>
        </form>
        <div class="embed-output">
          <div class="preview-card">
            <div class="browser-dots" aria-hidden="true"><i /><i /><i /></div>
            <div class="embed-preview">
              <Show when={values()} fallback={<p>SVG 미리보기는 유효한 공개 프로필 식별자를 입력하면 표시됩니다.</p>}>
                {(embed) => <Show when={!previewFailed()} fallback={<p>공개 프로필의 SVG 미리보기를 불러오지 못했습니다.</p>}>
                  <img
                    src={embed().url}
                    alt={title() || "활동 히트맵"}
                    onLoad={() => setPreviewFailed(false)}
                    onError={() => setPreviewFailed(true)}
                  />
                </Show>}
              </Show>
            </div>
          </div>
          <div class="code-tabs" role="tablist" aria-label="임베드 코드 형식">
            <For each={formats}>{(item, index) => (
              <button
                id={`embed-tab-${item.id}`}
                type="button"
                role="tab"
                aria-controls="embed-code-panel"
                aria-selected={format() === item.id ? "true" : "false"}
                tabindex={format() === item.id ? 0 : -1}
                onClick={() => selectTab(item.id)}
                onKeyDown={(event) => moveTab(event, index())}
              >{item.label}</button>
            )}</For>
          </div>
          <div
            class="code-box"
            id="embed-code-panel"
            role="tabpanel"
            aria-labelledby={`embed-tab-${format()}`}
          >
            <Show when={values()} fallback={<code>유효한 프로필 식별자를 입력해 주세요.</code>}>
              {(embed) => <>
                <code>{embed()[format()]}</code>
                <CopyButton
                  value={embed()[format()]}
                  label="코드 복사"
                  copiedLabel="복사됨"
                  successMessage="임베드 코드를 복사했습니다."
                  failureMessage="코드를 복사하지 못했습니다."
                />
              </>}
            </Show>
          </div>
          <p class="code-help">임베드는 공개 프로필에서만 보입니다. 렌더 URL에는 인증 정보가 포함되지 않습니다.</p>
        </div>
      </div>
    </section>
  );
}
