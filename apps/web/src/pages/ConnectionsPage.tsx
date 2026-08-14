import type { ProviderCatalogItemDto, ProviderConnectionDto } from "@jandibat/contracts";
import { Match, Switch, createEffect, createSignal } from "solid-js";
import { api } from "../api/client";
import {
  providerCallbackLocationHasData,
  providerCallbackResultFromUrl,
  providerCallbackSafeLocation,
} from "../api/provider-callback";
import { useAppState } from "../app/state";
import { ErrorCallout, Icon, LoadingCards, PageIntro } from "../components/common";
import { ProviderCards } from "../providers/ProviderCards";
import { OwnerGate, OwnerSubjectSelect } from "../subjects/OwnerGate";

type ProviderState =
  | { kind: "loading" }
  | { kind: "ready"; catalog: readonly ProviderCatalogItemDto[]; connections: readonly ProviderConnectionDto[] }
  | { kind: "error"; error: unknown };

function ConnectionsWorkspace(props: { subject: string }) {
  const app = useAppState();
  const [providers, setProviders] = createSignal<ProviderState>({ kind: "loading" });
  const callbackResult = providerCallbackResultFromUrl(location.href);
  let callbackAnnounced = false;
  let controller: AbortController | undefined;

  if (providerCallbackLocationHasData(location.href)) {
    history.replaceState(null, "", providerCallbackSafeLocation(location.href));
  }

  const load = async (subject = props.subject) => {
    controller?.abort();
    controller = new AbortController();
    setProviders({ kind: "loading" });
    try {
      const [catalog, connections] = await Promise.all([
        api.listProviderCatalog(controller.signal),
        api.listConnections(subject, controller.signal),
      ]);
      setProviders({
        kind: "ready",
        catalog: catalog.providers,
        connections: connections.connections,
      });
      if (callbackResult && !callbackAnnounced) {
        callbackAnnounced = true;
        app.showToast("Provider 연결을 완료했습니다.");
      }
    } catch (error) {
      if (error instanceof DOMException && error.name === "AbortError") return;
      setProviders({ kind: "error", error });
    }
  };

  createEffect(
    () => props.subject,
    (subject) => {
      void load(subject);
      return () => controller?.abort();
    },
  );

  return (
    <section class="page section-shell narrow">
      <PageIntro
        eyebrow="Connect your world"
        title={<>하나의 잔디밭,<br /><em>여러 개의 시작점.</em></>}
        description="사용하는 서비스를 연결하면 활동을 안전하게 가져와 하나의 흐름으로 보여드려요."
      />
      <OwnerSubjectSelect />
      <div class="connection-toolbar">
        <p><span class="pulse-dot" />연결 정보는 암호화되어 저장됩니다.</p>
        <a href="/custom" class="text-link">직접 데이터 만들기 <Icon name="arrow" /></a>
      </div>
      <div
        class="provider-grid"
        aria-live="polite"
        aria-busy={providers().kind === "loading" ? "true" : undefined}
        data-state={providers().kind === "ready" || providers().kind === "error" ? "ready" : undefined}
      >
        <Switch>
          <Match when={providers().kind === "loading"}>
            <LoadingCards message="Provider 연결 정보를 불러오는 중입니다." count={3} />
          </Match>
          <Match when={providers().kind === "ready"}>
            <ProviderCards
              subject={props.subject}
              catalog={(providers() as Extract<ProviderState, { kind: "ready" }>).catalog}
              connections={(providers() as Extract<ProviderState, { kind: "ready" }>).connections}
              reload={() => void load()}
            />
          </Match>
          <Match when={providers().kind === "error"}>
            <ErrorCallout
              error={(providers() as Extract<ProviderState, { kind: "error" }>).error}
              retry={() => void load()}
            />
          </Match>
        </Switch>
      </div>
    </section>
  );
}

export function ConnectionsPage() {
  return (
    <OwnerGate>
      {(subject) => <ConnectionsWorkspace subject={subject} />}
    </OwnerGate>
  );
}
