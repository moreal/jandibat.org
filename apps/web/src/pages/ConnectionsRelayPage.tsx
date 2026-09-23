import { Match, Show, Switch, createEffect, createSignal } from "solid-js";
import { defaultApiBaseUrl } from "../api/client";
import { providerCallbackLocationHasData, providerCallbackResultFromUrl, providerCallbackSafeLocation } from "../api/provider-callback";
import { useAppState } from "../app/state";
import { ErrorCallout, Icon, LoadingCards, PageIntro } from "../components/common";
import { ProviderCardsRelay } from "../providers/ProviderCardsRelay";
import { createRelayEphemeralMutation, createRelayMutation, createRelayPaginationFragment, createRelayQuery } from "../relay";
import { OwnerGate, OwnerSubjectSelect } from "../subjects/OwnerGate";
import type { ConnectionsQuery } from "./__generated__/ConnectionsQuery.graphql";
import type { ConnectionsSubjectFragment$key } from "./__generated__/ConnectionsSubjectFragment.graphql";
import type { ConnectionsSubjectFragment$data } from "./__generated__/ConnectionsSubjectFragment.graphql";
import type { ConnectionsSubjectRefetchQuery } from "./__generated__/ConnectionsSubjectRefetchQuery.graphql";
import type { ConnectionsConnectProviderMutation } from "./__generated__/ConnectionsConnectProviderMutation.graphql";
import type { ConnectionsRevokeProviderMutation } from "./__generated__/ConnectionsRevokeProviderMutation.graphql";
import type { ConnectionsEnqueueSyncMutation } from "./__generated__/ConnectionsEnqueueSyncMutation.graphql";
import query from "./__generated__/ConnectionsQuery.graphql";
import fragment from "./__generated__/ConnectionsSubjectFragment.graphql";
import connectMutation from "./__generated__/ConnectionsConnectProviderMutation.graphql";
import revokeMutation from "./__generated__/ConnectionsRevokeProviderMutation.graphql";
import enqueueMutation from "./__generated__/ConnectionsEnqueueSyncMutation.graphql";

const PAGE_SIZE = 25;

function ConnectionsContent(props: {
  subject: NonNullable<ConnectionsQuery["response"]["subject"]>;
  catalog: ConnectionsQuery["response"]["providerCatalog"];
  retry: () => void;
  onReady?: () => void;
}) {
  const connections = createRelayPaginationFragment<ConnectionsSubjectRefetchQuery, ConnectionsSubjectFragment$key>(
    fragment, () => props.subject,
  );
  type ConnectionPage = NonNullable<ConnectionsSubjectFragment$data["providerConnections"]>;
  const [lastPage, setLastPage] = createSignal<{ subjectID: string; page: ConnectionPage } | null>(null);
  const [activeAction, setActiveAction] = createSignal<"refresh" | "next">();
  const [requestError, setRequestError] = createSignal<{ error: Error; action: "refresh" | "next" }>();
  const livePage = () => {
    if (connections.error || connections.pending) return null;
    const data = connections();
    return data?.id === props.subject.id ? data.providerConnections ?? null : null;
  };
  createEffect(() => ({ page: livePage(), subjectID: props.subject.id }), ({ page, subjectID }) => {
    if (page) setLastPage({ subjectID, page });
  });
  const page = () => {
    if (activeAction() === "refresh") return null;
    const current = livePage();
    if (current) return current;
    const previous = lastPage();
    return activeAction() === "next" && previous?.subjectID === props.subject.id ? previous.page : null;
  };
  createEffect(
    () => Boolean(livePage()),
    (ready) => { if (ready) props.onReady?.(); },
  );
  const connectPublic = createRelayMutation<ConnectionsConnectProviderMutation>(connectMutation);
  const connectCredential = createRelayEphemeralMutation<ConnectionsConnectProviderMutation>(
    defaultApiBaseUrl(), connectMutation,
  );
  const connect = (variables: ConnectionsConnectProviderMutation["variables"]) =>
    variables.input.authMethod === "PUBLIC" ? connectPublic(variables) : connectCredential(variables);
  const revoke = createRelayMutation<ConnectionsRevokeProviderMutation>(revokeMutation);
  const enqueue = createRelayMutation<ConnectionsEnqueueSyncMutation>(enqueueMutation);
  const refreshConnections = () => {
    setRequestError(undefined);
    setActiveAction("refresh");
    connections.refetch({ id: props.subject.id, count: PAGE_SIZE, cursor: null }, {
      onComplete: (error) => {
        if (error) setRequestError({ error, action: "refresh" });
        else setActiveAction(undefined);
      },
    });
  };
  const loadNext = () => {
    setRequestError(undefined);
    setActiveAction("next");
    connections.loadNext(PAGE_SIZE, {
      onComplete: (error) => {
        if (error) setRequestError({ error, action: "next" });
        else setActiveAction(undefined);
      },
    });
  };
  const retryRequest = () => {
    if (requestError()?.action === "next") loadNext();
    else refreshConnections();
  };
  const failure = () => requestError()?.error ?? connections.error;
  return <>
    <Show when={failure()}>{(error) => <ErrorCallout error={error()} retry={retryRequest} />}</Show>
    <Show when={page()} fallback={!failure() && (connections.pending
      ? <LoadingCards message="Provider 연결 정보를 불러오는 중입니다." count={3} />
      : <ErrorCallout error={new Error("이 잔디밭의 연결 정보를 볼 수 없습니다.")} retry={props.retry} />)}>
    {(currentPage) => <>
      <ProviderCardsRelay subjectID={props.subject.id} catalog={props.catalog}
        connections={currentPage().edges.map(({ node }) => node)}
        reload={refreshConnections} connect={connect} revoke={revoke} enqueue={enqueue} />
      <Show when={connections.hasNext}>
        <button class="secondary-button" type="button" disabled={connections.isLoadingNext}
          onClick={loadNext}>
          {connections.isLoadingNext ? "더 불러오는 중…" : "연결 더 보기"}
        </button>
      </Show>
    </>}
    </Show>
  </>;
}

function ConnectionsWorkspace(props: { subject: string }) {
  const app = useAppState();
  const [retryNonce, setRetryNonce] = createSignal(0);
  const callbackResult = providerCallbackResultFromUrl(location.href);
  if (providerCallbackLocationHasData(location.href)) {
    history.replaceState(null, "", providerCallbackSafeLocation(location.href));
  }
  let callbackAnnounced = false;
  const announceCallback = () => {
    if (!callbackResult || callbackAnnounced) return;
    callbackAnnounced = true;
    app.showToast("Provider 연결을 완료했습니다.");
  };

  const result = createRelayQuery<ConnectionsQuery>(query, () => ({
    subject: props.subject, count: PAGE_SIZE, cursor: null,
  }), { fetchKey: retryNonce });
  const reload = () => setRetryNonce((value) => value + 1);

  return <section class="page section-shell narrow">
    <PageIntro eyebrow="Connect your world"
      title={<>하나의 잔디밭,<br /><em>여러 개의 시작점.</em></>}
      description="사용하는 서비스를 연결하면 활동을 안전하게 가져와 하나의 흐름으로 보여드려요." />
    <OwnerSubjectSelect />
    <div class="connection-toolbar">
      <p><span class="pulse-dot" />연결 정보는 암호화되어 저장됩니다.</p>
      <a href="/custom" class="text-link">직접 데이터 만들기 <Icon name="arrow" /></a>
    </div>
    <div class="provider-grid" aria-live="polite" aria-busy={result.pending ? "true" : undefined}
      data-state={result.error || (!result.pending && result()) ? "ready" : undefined}>
      <Switch>
        <Match when={result.error}><ErrorCallout error={result.error} retry={reload} /></Match>
        <Match when={result.pending || (!result.error && !result())}>
          <LoadingCards message="Provider 연결 정보를 불러오는 중입니다." count={3} />
        </Match>
        <Match when={!result.error && !result()?.subject}>
          <ErrorCallout error={new Error("이 잔디밭의 연결 정보를 볼 수 없습니다.")} retry={reload} />
        </Match>
        <Match when={!result.error && result()?.subject}>
          {(subject) => <ConnectionsContent subject={subject()} catalog={result()!.providerCatalog}
            retry={reload} onReady={announceCallback} />}
        </Match>
      </Switch>
    </div>
  </section>;
}

export function ConnectionsPage() {
  return <OwnerGate>{(subject) => <ConnectionsWorkspace subject={subject} />}</OwnerGate>;
}
