import { Match, Show, Switch, createEffect, createSignal, onSettled } from "solid-js";
import { api, defaultApiBaseUrl } from "../api/client";
import { useAppState } from "../app/state";
import { CopyButton } from "../components/CopyButton";
import { ErrorCallout, Icon, LoadingCards, PageIntro, errorMessage } from "../components/common";
import { todayDateKey } from "../heatmap/calendar";
import { CustomProviderCardsRelay } from "../providers/CustomProviderCardsRelay";
import { createRelayEphemeralMutation, createRelayPaginationFragment, createRelayQuery } from "../relay";
import { useAuthEpoch } from "../relay/auth-epoch";
import { OwnerGate, OwnerSubjectSelect } from "../subjects/OwnerGate";
import type { CustomQuery } from "./__generated__/CustomQuery.graphql";
import type { CustomSubjectFragment$key, CustomSubjectFragment$data } from "./__generated__/CustomSubjectFragment.graphql";
import type { CustomSubjectRefetchQuery } from "./__generated__/CustomSubjectRefetchQuery.graphql";
import type { CustomCreateProviderMutation } from "./__generated__/CustomCreateProviderMutation.graphql";
import type { CustomRotateProviderKeyMutation } from "./__generated__/CustomRotateProviderKeyMutation.graphql";
import query from "./__generated__/CustomQuery.graphql";
import fragment from "./__generated__/CustomSubjectFragment.graphql";
import createMutation from "./__generated__/CustomCreateProviderMutation.graphql";
import rotateMutation from "./__generated__/CustomRotateProviderKeyMutation.graphql";

const PAGE_SIZE = 25;
type Provider = NonNullable<CustomSubjectFragment$data["customProviders"]>["edges"][number]["node"];

function throwPayloadErrors(errors: readonly { message: string }[]): void {
  if (errors.length) throw new Error(errors.map((error) => error.message).join("; "));
}

function CustomContent(props: { subject: NonNullable<CustomQuery["response"]["subject"]>;
  selectedID: string | undefined; select: (provider: Provider) => void; clearSelection: () => void;
  showSecret: (key: string) => void; clearSecret: () => void;
  rotateKey: (variables: CustomRotateProviderKeyMutation["variables"]) => Promise<CustomRotateProviderKeyMutation["response"]>;
  retry: () => void; refreshRef: (refresh: () => void) => void;
  setAuthorized: (value: boolean) => void }) {
  const providers = createRelayPaginationFragment<CustomSubjectRefetchQuery, CustomSubjectFragment$key>(fragment, () => props.subject);
  type Page = NonNullable<CustomSubjectFragment$data["customProviders"]>;
  const [lastPage, setLastPage] = createSignal<Page | null>(null);
  const [requestError, setRequestError] = createSignal<{ error: Error; action: "refresh" | "next" }>();
  const livePage = () => requestError()?.action !== "refresh" && !providers.error && !providers.pending
    ? providers()?.customProviders ?? null : null;
  createEffect(livePage, (page) => { if (page) setLastPage(page); });
  createEffect(livePage, (page) => { props.setAuthorized(Boolean(page)); });
  const page = () => livePage() ?? lastPage();
  const refresh = () => { setRequestError(undefined); setLastPage(null);
    providers.refetch({ id: props.subject.id, count: PAGE_SIZE, cursor: null }, {
      onComplete: (error) => { if (error) setRequestError({ error, action: "refresh" }); },
    });
  };
  props.refreshRef(refresh);
  const loadNext = () => { setRequestError(undefined);
    providers.loadNext(PAGE_SIZE, { onComplete: (error) => { if (error) setRequestError({ error, action: "next" }); } });
  };
  const retryRequest = () => requestError()?.action === "next" ? loadNext() : refresh();
  const failure = () => requestError()?.error ?? providers.error;
  const selected = () => page()?.edges.map(({ node }) => node).find((node) => node.id === props.selectedID);
  return <>
    <Show when={failure()}>{(error) => <ErrorCallout error={error()} retry={retryRequest} />}</Show>
    <Show when={page()} fallback={!failure() && (providers.pending
      ? <LoadingCards message="커스텀 데이터 소스를 불러오는 중입니다." />
      : <ErrorCallout error={new Error("이 잔디밭의 데이터 소스를 볼 수 없습니다.")} retry={props.retry} />)}>
      {(currentPage) => <>
        <CustomProviderCardsRelay providers={currentPage().edges.map(({ node }) => node)} select={props.select}
          showSecret={props.showSecret} clearSecret={props.clearSecret} rotateKey={props.rotateKey} reload={refresh} />
        <Show when={providers.hasNext}><button class="secondary-button" type="button"
          disabled={providers.isLoadingNext} onClick={loadNext}>
          {providers.isLoadingNext ? "더 불러오는 중…" : "데이터 소스 더 보기"}
        </button></Show>
      </>}
    </Show>
    <Show when={selected()}>{(provider) => <CustomIngest provider={provider()} onDone={props.clearSelection} />}</Show>
  </>;
}

function CustomIngest(props: { provider: Provider; onDone: () => void }) {
  const app = useAppState();
  const [busy, setBusy] = createSignal(false);
  const ingest = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity() || busy()) return;
    const values = new FormData(form);
    setBusy(true);
    try {
      const note = String(values.get("note") ?? "");
      const result = await api.ingestCustomActivities(props.provider.ingestProviderID,
        String(values.get("ingestionKey") ?? ""), {
          schemaVersion: "1.0", events: [{ eventId: crypto.randomUUID(), date: String(values.get("date") ?? ""),
            action: String(values.get("action") ?? "activity"), metric: { name: "count", value: Number(values.get("count") ?? 0) },
            metadata: note ? { note } : undefined }],
        });
      const detail = result.rejections[0]?.detail;
      app.showToast(result.rejected > 0
        ? `활동 ${result.accepted}건 반영, ${result.rejected}건 거절됐습니다.${detail ? ` ${detail}` : ""}`
        : `활동 ${result.accepted}건을 반영했습니다.`, result.rejected > 0 ? "error" : "success");
      form.reset(); props.onDone();
    } catch (error) { app.showToast(errorMessage(error), "error"); }
    finally { setBusy(false); }
  };
  return <section class="ingest-panel" id="ingest-panel">
    <div><p class="eyebrow">Add activity</p><h2>{props.provider.name} 기록 입력</h2>
      <p>특정 날짜의 활동량을 잔디밭에 더합니다.</p></div>
    <form class="inline-form" onSubmit={ingest}>
      <label>Provider key<input type="password" name="ingestionKey" autocomplete="off" minlength="32" maxlength="2048" required /></label>
      <label>활동 종류<input name="action" maxlength="64" value={props.provider.allowedActions[0] ?? "activity"} required /></label>
      <label>날짜<input type="date" name="date" max={todayDateKey()} value={todayDateKey()} required /></label>
      <label>활동량<input type="number" name="count" min="0" max="1000000" step="1" value="1" required /></label>
      <label>메모<input name="note" maxlength="1000" placeholder="선택 입력" /></label>
      <button class="primary-button" type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
        {busy() ? "추가 중…" : "기록 추가"}</button>
    </form>
  </section>;
}

function CustomWorkspace(props: { subject: string }) {
  const app = useAppState();
  const authEpoch = useAuthEpoch();
  const [retryNonce, setRetryNonce] = createSignal(0);
  const [createBusy, setCreateBusy] = createSignal(false);
  const [canCreate, setCanCreate] = createSignal(false);
  const [selectedID, setSelectedID] = createSignal<string>();
  const [secret, setSecret] = createSignal<string>();
  let refresh: (() => void) | undefined;
  let scope = 0;
  createEffect(() => [props.subject, authEpoch.environment()] as const, () => {
    scope += 1; setSelectedID(undefined); setSecret(undefined); setCanCreate(false);
  });
  onSettled(() => () => { scope += 1; setSecret(undefined); });
  const result = createRelayQuery<CustomQuery>(query,
    () => ({ subject: props.subject, count: PAGE_SIZE, cursor: null }), { fetchKey: retryNonce });
  const createProvider = createRelayEphemeralMutation<CustomCreateProviderMutation>(defaultApiBaseUrl(), createMutation);
  const rotateMutationRequest = createRelayEphemeralMutation<CustomRotateProviderKeyMutation>(defaultApiBaseUrl(), rotateMutation);
  const rotateKey = async (variables: CustomRotateProviderKeyMutation["variables"]) => {
    const requestScope = scope;
    const response = await rotateMutationRequest(variables);
    if (requestScope !== scope) throw new DOMException("Aborted", "AbortError");
    return response;
  };
  const reload = () => setRetryNonce((value) => value + 1);
  const showSecret = (value: string) => { setSecret(value); window.requestAnimationFrame(() =>
    document.getElementById("secret-result")?.scrollIntoView({ behavior: "auto", block: "center" })); };
  const selectProvider = (provider: Provider) => { setSelectedID(provider.id); window.requestAnimationFrame(() =>
    document.getElementById("ingest-panel")?.scrollIntoView({ behavior: "auto", block: "center" })); };
  const create = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity() || createBusy() || !canCreate() || result.error || !result()?.subject) return;
    const values = new FormData(form);
    const requestScope = scope;
    setCreateBusy(true); setSecret(undefined);
    try {
      const payload = (await createProvider({ input: {
        subjectID: result()!.subject!.id, name: String(values.get("name") ?? ""),
        slug: String(values.get("key") ?? ""), allowedActions: [String(values.get("action") ?? "activity")],
        description: String(values.get("description") ?? "") || null,
      } })).createCustomProvider;
      if (requestScope !== scope) return;
      throwPayloadErrors(payload.errors);
      if (!payload.provider || !payload.ingestionKey) throw new Error("데이터 소스를 만들지 못했습니다.");
      form.reset(); showSecret(payload.ingestionKey);
      app.showToast("데이터 소스를 만들었습니다. Provider key를 안전하게 저장하세요."); refresh?.();
    } catch (error) {
      if (requestScope === scope && !(error instanceof DOMException && error.name === "AbortError")) {
        setSecret(undefined); app.showToast(errorMessage(error), "error");
      }
    } finally { if (requestScope === scope) setCreateBusy(false); }
  };
  return <section class="page section-shell narrow">
    <PageIntro eyebrow="Make it yours" title={<>코드 밖의 하루도<br /><em>같은 잔디밭 위에.</em></>}
      description="독서, 운동, 글쓰기처럼 나에게 중요한 활동을 직접 정의하고 기록하세요." />
    <OwnerSubjectSelect />
    <div class="custom-layout">
      <div><div class="section-title"><div><p class="eyebrow">Your providers</p><h2>나의 데이터 소스</h2></div></div>
        <div class="custom-list" aria-live="polite" aria-busy={result.pending ? "true" : undefined}
          data-state={result.error || (!result.pending && result()) ? "ready" : undefined}>
          <Switch>
            <Match when={result.error}><ErrorCallout error={result.error} retry={reload} /></Match>
            <Match when={result.pending || (!result.error && !result())}>
              <LoadingCards message="커스텀 데이터 소스를 불러오는 중입니다." />
            </Match>
            <Match when={!result.error && !result()?.subject}>
              <ErrorCallout error={new Error("이 잔디밭의 데이터 소스를 볼 수 없습니다.")} retry={reload} />
            </Match>
            <Match when={!result.error && result()?.subject}>
              {(subject) => <CustomContent subject={subject()} selectedID={selectedID()} select={selectProvider}
                clearSelection={() => setSelectedID(undefined)} showSecret={showSecret}
                clearSecret={() => setSecret(undefined)} rotateKey={rotateKey}
                retry={reload} refreshRef={(value) => { refresh = value; }} setAuthorized={setCanCreate} />}
            </Match>
          </Switch>
        </div>
      </div>
      <aside class="form-card"><p class="eyebrow">New provider</p><h2>새 데이터 소스</h2>
        <form class="stack-form" onSubmit={create}>
          <label for="custom-name">이름</label>
          <input id="custom-name" name="name" placeholder="예: 독서" maxlength="100" required />
          <label for="custom-slug">식별자</label>
          <div class="input-prefix"><span>source/</span><input id="custom-slug" name="key" placeholder="reading"
            pattern="[a-z0-9][a-z0-9_-]*" maxlength="64" required /></div>
          <small>영문 소문자나 숫자로 시작하고 하이픈, 밑줄을 사용할 수 있어요.</small>
          <label for="custom-action">활동 종류</label>
          <input id="custom-action" name="action" placeholder="예: read" maxlength="64" required />
          <label for="custom-description">설명 <span>(선택)</span></label>
          <textarea id="custom-description" name="description" rows="3" maxlength="500"
            placeholder="어떤 활동을 기록할지 적어주세요." />
          <button class="primary-button wide" type="submit" disabled={createBusy() || !canCreate()}
            aria-busy={createBusy() ? "true" : undefined}>
            {createBusy() ? "만드는 중…" : <>데이터 소스 만들기 <Icon name="arrow" /></>}
          </button>
        </form>
      </aside>
    </div>
    <Show when={secret()}>{(value) => <section class="secret-result" id="secret-result" role="status" aria-live="polite">
      <div><p class="eyebrow">Shown once</p><h2>Provider key를 지금 저장하세요.</h2>
        <p>보안을 위해 이 값은 다시 표시되지 않습니다.</p></div>
      <div class="code-box"><code>{value()}</code><CopyButton value={value()} label="Provider key 복사"
        copiedLabel="Provider key 복사 완료" successMessage="Provider key를 복사했습니다."
        failureMessage="복사하지 못했습니다. 값을 직접 선택해 주세요." /></div>
    </section>}</Show>
  </section>;
}

export function CustomPage() {
  return <OwnerGate>{(subject) => <CustomWorkspace subject={subject} />}</OwnerGate>;
}
