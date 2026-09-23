import { For, Show, createSignal } from "solid-js";
import { validateAuthorizationUrl } from "../api/authorization-url";
import { useAppState } from "../app/state";
import { Icon, errorMessage, formatTime } from "../components/common";
import type { ConnectionsQuery } from "../pages/__generated__/ConnectionsQuery.graphql";
import type { ConnectionsSubjectFragment$data } from "../pages/__generated__/ConnectionsSubjectFragment.graphql";
import type { ConnectionsConnectProviderMutation } from "../pages/__generated__/ConnectionsConnectProviderMutation.graphql";
import type { ConnectionsRevokeProviderMutation } from "../pages/__generated__/ConnectionsRevokeProviderMutation.graphql";
import type { ConnectionsEnqueueSyncMutation } from "../pages/__generated__/ConnectionsEnqueueSyncMutation.graphql";
import { legacyCallbackUrl } from "../routing/legacy";
import { privateConsentEligible, privateConsentValue } from "./model";

type Catalog = ConnectionsQuery["response"]["providerCatalog"][number];
type Connection = NonNullable<ConnectionsSubjectFragment$data["providerConnections"]>["edges"][number]["node"];
type Connect = (variables: ConnectionsConnectProviderMutation["variables"]) => Promise<ConnectionsConnectProviderMutation["response"]>;
type Revoke = (variables: ConnectionsRevokeProviderMutation["variables"]) => Promise<ConnectionsRevokeProviderMutation["response"]>;
type Enqueue = (variables: ConnectionsEnqueueSyncMutation["variables"]) => Promise<ConnectionsEnqueueSyncMutation["response"]>;
type AuthMethod = "PUBLIC" | "TOKEN" | "OAUTH2";

const statusLabels: Record<string, string> = {
  active: "연결됨", disabled: "비활성", revoked: "해제됨", pending: "연결 대기", error: "확인 필요",
};

function providerMonogram(provider: string): string {
  const labels: Record<string, string> = { github: "GH", gitlab: "GL", codeberg: "CB" };
  return labels[provider.toLowerCase()] ?? provider.slice(0, 2).toUpperCase();
}

function providerLogoClass(provider: string): string {
  return provider === "github" || provider === "gitlab" || provider === "codeberg"
    ? `provider-${provider}` : "provider-other";
}

function authMethods(provider: Catalog): AuthMethod[] {
  const methods: AuthMethod[] = ["PUBLIC"];
  if (provider.supportsToken) methods.push("TOKEN");
  if (provider.supportsOAuth) methods.push("OAUTH2");
  return methods;
}

function throwPayloadErrors(errors: readonly { message: string }[]): void {
  if (errors.length) throw new Error(errors.map((error) => error.message).join("; "));
}

function ProviderConnectForm(props: {
  subjectID: string; provider: Catalog; index: number; reload: () => void; connect: Connect;
}) {
  const app = useAppState();
  const methods = () => authMethods(props.provider);
  const [authMethod, setAuthMethod] = createSignal<AuthMethod>("PUBLIC");
  const [busy, setBusy] = createSignal(false);
  const [failure, setFailure] = createSignal<string>();
  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    if (!form.reportValidity() || busy()) return;
    const values = new FormData(form);
    setFailure(undefined);
    setBusy(true);
    try {
      const result = (await props.connect({ input: {
        subjectID: props.subjectID,
        providerID: props.provider.id,
        authMethod: authMethod(),
        ...(authMethod() === "TOKEN" ? { token: String(values.get("token") ?? "") } : {}),
        includePrivate: privateConsentValue(props.provider.supportsPrivateData,
          authMethod().toLowerCase(), values.get("includePrivate") === "true"),
        ...(authMethod() === "OAUTH2" ? { redirectURI: legacyCallbackUrl("connections") } : {}),
      } })).connectProvider;
      throwPayloadErrors(result.errors);
      if (result.authorizationURL) {
        location.assign(validateAuthorizationUrl(result.authorizationURL, import.meta.env.DEV));
        return;
      }
      app.showToast(`${props.provider.name} 연결을 시작했습니다.`);
      props.reload();
    } catch (error) {
      const message = errorMessage(error);
      setFailure(message);
      app.showToast(message, "error");
    } finally {
      setBusy(false);
    }
  };
  const consentVisible = () => privateConsentEligible(props.provider.supportsPrivateData, authMethod().toLowerCase());
  const consentId = () => `provider-private-consent-${props.index}`;
  return <form class="provider-connect-form" novalidate onSubmit={submit}>
    <label><span class="sr-only">{props.provider.name} 연결 방식</span>
      <select name="authMethod" aria-label={`${props.provider.name} 연결 방식`} disabled={busy()}
        onChange={(event) => setAuthMethod(event.currentTarget.value as AuthMethod)}>
        <For each={methods()}>{(method) => <option value={method}>
          {method === "OAUTH2" ? "OAuth" : method === "TOKEN" ? "Access token" : "공개 데이터"}
        </option>}</For>
      </select>
    </label>
    <label class="provider-token-field" hidden={authMethod() !== "TOKEN"}>
      <span class="sr-only">{props.provider.name} access token</span>
      <input name="token" type="password" autocomplete="off" maxlength="4096"
        placeholder="Access token" required={authMethod() === "TOKEN"} />
    </label>
    <Show when={props.provider.supportsPrivateData}>
      <label class="private-consent-field" hidden={!consentVisible()}>
        <input id={consentId()} name="includePrivate" type="checkbox" value="true"
          required={consentVisible()} aria-required={consentVisible() ? "true" : "false"}
          aria-describedby={`${consentId()}-help`} />
        <span><strong>비공개 활동 포함에 동의합니다.</strong><small id={`${consentId()}-help`}>
          Provider의 최소 권한으로 비공개 저장소의 활동 날짜와 집계량을 가져옵니다.
          비공개 원문과 저장소 이름은 공개 잔디밭에 표시하지 않습니다.
        </small></span>
      </label>
    </Show>
    <p class="inline-error" role="alert" hidden={!failure()}>{failure()}</p>
    <button class="primary-button wide" type="submit" disabled={busy()} aria-busy={busy() ? "true" : undefined}>
      {busy() ? "연결 준비 중…" : <>{props.provider.name} 연결 <Icon name="arrow" /></>}
    </button>
  </form>;
}

function ConnectedActions(props: {
  provider: Catalog; connection: Connection; reload: () => void; revoke: Revoke; enqueue: Enqueue;
}) {
  const app = useAppState();
  const [busyAction, setBusyAction] = createSignal<"sync" | "disconnect">();
  const sync = async () => {
    if (busyAction()) return;
    setBusyAction("sync");
    try {
      const result = (await props.enqueue({ input: {
        connectionID: props.connection.id, idempotencyKey: crypto.randomUUID(),
      } })).enqueueManualSync;
      throwPayloadErrors(result.errors);
      app.showToast(`동기화 작업을 등록했습니다. (${result.job?.status ?? "PENDING"})`);
      props.reload();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setBusyAction(undefined);
    }
  };
  const disconnect = async () => {
    if (busyAction() || !confirm("이 연결을 해제할까요? 이미 가져온 공개 활동은 유지됩니다.")) return;
    setBusyAction("disconnect");
    try {
      const result = (await props.revoke({ input: { id: props.connection.id } })).revokeProviderConnection;
      throwPayloadErrors(result.errors);
      app.showToast("Provider 연결을 해제했습니다.");
      props.reload();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setBusyAction(undefined);
    }
  };
  return <div class="card-actions">
    <button class="secondary-button" type="button" disabled={Boolean(busyAction())}
      aria-busy={busyAction() === "sync" ? "true" : undefined} onClick={sync}>
      <Icon name="sync" /> {busyAction() === "sync" ? "동기화 중…" : "동기화"}
    </button>
    <button class="icon-button danger" type="button" disabled={Boolean(busyAction())}
      aria-label={`${props.provider.name} 연결 해제`} onClick={disconnect}><Icon name="trash" /></button>
  </div>;
}

export function ProviderCardsRelay(props: {
  subjectID: string;
  catalog: ConnectionsQuery["response"]["providerCatalog"];
  connections: readonly Connection[];
  reload: () => void;
  connect: Connect;
  revoke: Revoke;
  enqueue: Enqueue;
}) {
  const supported = () => props.catalog.filter((provider) => provider.category === "git-hosting");
  const connectionFor = (provider: Catalog) => props.connections.find((item) => item.providerID === provider.id);
  return <Show when={supported().length > 0} fallback={<div class="empty-list" role="status">
    <span class="empty-sprout" aria-hidden="true" /><h2>연결할 수 있는 Provider가 없어요.</h2>
    <p>잠시 후 다시 시도해 주세요.</p><button class="text-button" type="button" onClick={props.reload}>다시 시도</button>
  </div>}>
    <For each={supported()}>{(provider, index) => {
      const connection = () => connectionFor(provider);
      const status = () => connection()?.status ?? "disconnected";
      const statusClass = () => {
        const value = status().toLowerCase();
        return Object.hasOwn(statusLabels, value) ? value : "error";
      };
      const methods = () => authMethods(provider);
      return <article class={`provider-card${connection() ? " is-connected" : ""}`}>
        <div class="provider-card-top"><span class={`provider-logo ${providerLogoClass(provider.id)}`} aria-hidden="true">
          {providerMonogram(provider.id)}</span><span class={`status-pill status-${statusClass()}`}>
            <i />{connection() ? statusLabels[statusClass()] : "연결 안 됨"}</span></div>
        <h2>{provider.name}</h2>
        <p>{provider.supportsPrivateData
          ? "공개 활동을 기본으로 가져오며, 비공개 활동은 별도 동의 후 포함할 수 있습니다."
          : "공개 활동을 잔디밭에 모읍니다."}</p>
        <Show when={connection()}>{(item) => <p class="connection-privacy">
          {item().privateDataEnabled ? "비공개 활동 포함 동의됨" : "공개 활동만 수집"}
        </p>}</Show>
        <div class="provider-meta"><span>{connection()
          ? `${connection()!.authMethod} · 마지막 동기화` : "지원 연결 방식"}</span>
          <strong>{connection() ? formatTime(connection()!.lastSyncedAt)
            : methods().map((method) => method === "PUBLIC" ? "공개 데이터" : method === "TOKEN" ? "Access token" : "OAuth").join(" · ")}</strong></div>
        <Show when={connection()} fallback={<div class="card-actions"><ProviderConnectForm
          subjectID={props.subjectID} provider={provider} index={index()} reload={props.reload} connect={props.connect}
        /></div>}>
          {(item) => <ConnectedActions provider={provider} connection={item()} reload={props.reload}
            revoke={props.revoke} enqueue={props.enqueue} />}
        </Show>
      </article>;
    }}</For>
  </Show>;
}
