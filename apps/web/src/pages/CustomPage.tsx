import type { CustomProviderDto } from "@jandibat/contracts";
import { Match, Show, Switch, createEffect, createSignal } from "solid-js";
import { api } from "../api/client";
import { useAppState } from "../app/state";
import { CopyButton } from "../components/CopyButton";
import { ErrorCallout, Icon, LoadingCards, PageIntro, errorMessage, preferredScrollBehavior } from "../components/common";
import { todayDateKey } from "../heatmap/calendar";
import { CustomProviderCards } from "../providers/CustomProviderCards";
import { OwnerGate, OwnerSubjectSelect } from "../subjects/OwnerGate";

type CustomState =
  | { kind: "loading" }
  | { kind: "ready"; providers: readonly CustomProviderDto[] }
  | { kind: "error"; error: unknown };

function CustomWorkspace(props: { subject: string }) {
  const app = useAppState();
  const [providers, setProviders] = createSignal<CustomState>({ kind: "loading" });
  const [createBusy, setCreateBusy] = createSignal(false);
  const [ingestBusy, setIngestBusy] = createSignal(false);
  const [selected, setSelected] = createSignal<CustomProviderDto>();
  const [secret, setSecret] = createSignal<string>();
  let controller: AbortController | undefined;

  const load = async (subject = props.subject) => {
    controller?.abort();
    controller = new AbortController();
    setProviders({ kind: "loading" });
    try {
      const response = await api.listCustomProviders(subject, controller.signal);
      setProviders({ kind: "ready", providers: response.providers });
      const selectedProvider = selected();
      if (selectedProvider && !response.providers.some((provider) => provider.id === selectedProvider.id)) {
        setSelected(undefined);
      }
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      setProviders({ kind: "error", error });
    }
  };

  createEffect(
    () => props.subject,
    (subject) => {
      setSelected(undefined);
      setSecret(undefined);
      void load(subject);
      return () => controller?.abort();
    },
  );

  const selectProvider = (provider: CustomProviderDto) => {
    setSelected(provider);
    window.requestAnimationFrame(() => {
      document.getElementById("ingest-panel")?.scrollIntoView({
        behavior: preferredScrollBehavior(),
        block: "center",
      });
    });
  };

  const revealSecret = (value: string) => {
    setSecret(value);
    window.requestAnimationFrame(() => {
      document.getElementById("secret-result")?.scrollIntoView({
        behavior: preferredScrollBehavior(),
        block: "center",
      });
    });
  };

  const createProvider = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity()) return;
    const values = new FormData(form);
    setCreateBusy(true);
    try {
      const created = await api.createCustomProvider(props.subject, {
        name: String(values.get("name") ?? ""),
        key: String(values.get("key") ?? ""),
        allowedActions: [String(values.get("action") ?? "activity")],
        description: String(values.get("description") ?? "") || undefined,
      });
      form.reset();
      revealSecret(created.ingestionKey);
      app.showToast("데이터 소스를 만들었습니다. Provider key를 안전하게 저장하세요.");
      await load();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setCreateBusy(false);
    }
  };

  const ingest = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity() || !selected()) return;
    const values = new FormData(form);
    setIngestBusy(true);
    try {
      const note = String(values.get("note") ?? "");
      const result = await api.ingestCustomActivities(
        selected()!.id,
        String(values.get("ingestionKey") ?? ""),
        {
          schemaVersion: "1.0",
          events: [{
            eventId: crypto.randomUUID(),
            date: String(values.get("date") ?? ""),
            action: String(values.get("action") ?? "activity"),
            metric: { name: "count", value: Number(values.get("count") ?? 0) },
            metadata: note ? { note } : undefined,
          }],
        },
      );
      const detail = result.rejections[0]?.detail;
      app.showToast(
        result.rejected > 0
          ? `활동 ${result.accepted}건 반영, ${result.rejected}건 거절됐습니다.${detail ? ` ${detail}` : ""}`
          : `활동 ${result.accepted}건을 반영했습니다.`,
        result.rejected > 0 ? "error" : "success",
      );
      form.reset();
      setSelected(undefined);
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setIngestBusy(false);
    }
  };

  return (
    <section class="page section-shell narrow">
      <PageIntro
        eyebrow="Make it yours"
        title={<>코드 밖의 하루도<br /><em>같은 잔디밭 위에.</em></>}
        description="독서, 운동, 글쓰기처럼 나에게 중요한 활동을 직접 정의하고 기록하세요."
      />
      <OwnerSubjectSelect />
      <div class="custom-layout">
        <div>
          <div class="section-title">
            <div><p class="eyebrow">Your providers</p><h2>나의 데이터 소스</h2></div>
          </div>
          <div
            class="custom-list"
            aria-live="polite"
            aria-busy={providers().kind === "loading" ? "true" : undefined}
            data-state={providers().kind === "ready" || providers().kind === "error" ? "ready" : undefined}
          >
            <Switch>
              <Match when={providers().kind === "loading"}>
                <LoadingCards message="커스텀 데이터 소스를 불러오는 중입니다." />
              </Match>
              <Match when={providers().kind === "ready"}>
                <CustomProviderCards
                  subject={props.subject}
                  providers={(providers() as Extract<CustomState, { kind: "ready" }>).providers}
                  reload={() => void load()}
                  select={selectProvider}
                  showSecret={revealSecret}
                />
              </Match>
              <Match when={providers().kind === "error"}>
                <ErrorCallout
                  error={(providers() as Extract<CustomState, { kind: "error" }>).error}
                  retry={() => void load()}
                />
              </Match>
            </Switch>
          </div>
        </div>
        <aside class="form-card">
          <p class="eyebrow">New provider</p><h2>새 데이터 소스</h2>
          <form class="stack-form" onSubmit={createProvider}>
            <label for="custom-name">이름</label>
            <input id="custom-name" name="name" placeholder="예: 독서" maxlength="100" required />
            <label for="custom-slug">식별자</label>
            <div class="input-prefix">
              <span>source/</span>
              <input
                id="custom-slug"
                name="key"
                placeholder="reading"
                pattern="[a-z0-9][a-z0-9_-]*"
                maxlength="64"
                required
              />
            </div>
            <small>영문 소문자나 숫자로 시작하고 하이픈, 밑줄을 사용할 수 있어요.</small>
            <label for="custom-action">활동 종류</label>
            <input id="custom-action" name="action" placeholder="예: read" maxlength="64" required />
            <label for="custom-description">설명 <span>(선택)</span></label>
            <textarea
              id="custom-description"
              name="description"
              rows="3"
              maxlength="500"
              placeholder="어떤 활동을 기록할지 적어주세요."
            />
            <button
              class="primary-button wide"
              type="submit"
              disabled={createBusy()}
              aria-busy={createBusy() ? "true" : undefined}
            >
              {createBusy() ? "만드는 중…" : <>데이터 소스 만들기 <Icon name="arrow" /></>}
            </button>
          </form>
        </aside>
      </div>

      <Show when={selected()}>
        {(provider) => (
          <section class="ingest-panel" id="ingest-panel">
            <div>
              <p class="eyebrow">Add activity</p>
              <h2>{provider().name} 기록 입력</h2>
              <p>특정 날짜의 활동량을 잔디밭에 더합니다.</p>
            </div>
            <form class="inline-form" onSubmit={ingest}>
              <label>Provider key
                <input type="password" name="ingestionKey" autocomplete="off" minlength="32" maxlength="2048" required />
              </label>
              <label>활동 종류
                <input name="action" maxlength="64" value={provider().allowedActions[0] ?? "activity"} required />
              </label>
              <label>날짜
                <input type="date" name="date" max={todayDateKey()} value={todayDateKey()} required />
              </label>
              <label>활동량
                <input type="number" name="count" min="0" max="1000000" step="1" value="1" required />
              </label>
              <label>메모<input name="note" maxlength="1000" placeholder="선택 입력" /></label>
              <button
                class="primary-button"
                type="submit"
                disabled={ingestBusy()}
                aria-busy={ingestBusy() ? "true" : undefined}
              >
                {ingestBusy() ? "추가 중…" : "기록 추가"}
              </button>
            </form>
          </section>
        )}
      </Show>

      <Show when={secret()}>
        {(value) => (
          <section class="secret-result" id="secret-result" role="status" aria-live="polite">
            <div>
              <p class="eyebrow">Shown once</p>
              <h2>Provider key를 지금 저장하세요.</h2>
              <p>보안을 위해 이 값은 다시 표시되지 않습니다.</p>
            </div>
            <div class="code-box">
              <code>{value()}</code>
              <CopyButton
                value={value()}
                label="Provider key 복사"
                copiedLabel="Provider key 복사 완료"
                successMessage="Provider key를 복사했습니다."
                failureMessage="복사하지 못했습니다. 값을 직접 선택해 주세요."
              />
            </div>
          </section>
        )}
      </Show>
    </section>
  );
}

export function CustomPage() {
  return (
    <OwnerGate>
      {(subject) => <CustomWorkspace subject={subject} />}
    </OwnerGate>
  );
}
