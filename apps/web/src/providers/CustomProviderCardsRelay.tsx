import { For, Show, createSignal } from "solid-js";
import { useAppState } from "../app/state";
import { Icon, errorMessage, formatTime } from "../components/common";
import type { CustomSubjectFragment$data } from "../pages/__generated__/CustomSubjectFragment.graphql";
import type { CustomUpdateProviderMutation } from "../pages/__generated__/CustomUpdateProviderMutation.graphql";
import type { CustomRotateProviderKeyMutation } from "../pages/__generated__/CustomRotateProviderKeyMutation.graphql";
import type { CustomDeleteProviderMutation } from "../pages/__generated__/CustomDeleteProviderMutation.graphql";
import updateMutation from "../pages/__generated__/CustomUpdateProviderMutation.graphql";
import deleteMutation from "../pages/__generated__/CustomDeleteProviderMutation.graphql";
import { createRelayMutation } from "../relay";

type Provider = NonNullable<CustomSubjectFragment$data["customProviders"]>["edges"][number]["node"];
type Rotate = (variables: CustomRotateProviderKeyMutation["variables"]) => Promise<CustomRotateProviderKeyMutation["response"]>;

function throwPayloadErrors(errors: readonly { message: string }[]): void {
  if (errors.length) throw new Error(errors.map((error) => error.message).join("; "));
}

function CustomProviderCard(props: { provider: Provider; select: (provider: Provider) => void;
  showSecret: (secret: string) => void; clearSecret: () => void; reload: () => void; rotateKey: Rotate }) {
  const app = useAppState();
  const update = createRelayMutation<CustomUpdateProviderMutation>(updateMutation);
  const remove = createRelayMutation<CustomDeleteProviderMutation>(deleteMutation);
  const [busy, setBusy] = createSignal<"toggle" | "rotate" | "delete">();
  const status = () => props.provider.status.toLowerCase() === "active" ? "active" : "disabled";
  const toggle = async () => {
    if (busy()) return;
    const nextStatus = status() === "active" ? "DISABLED" : "ACTIVE";
    setBusy("toggle");
    try {
      const payload = (await update({ input: { id: props.provider.id, status: nextStatus } })).updateCustomProvider;
      throwPayloadErrors(payload.errors);
      if (!payload.provider) throw new Error("상태를 바꾸지 못했습니다.");
      app.showToast(nextStatus === "DISABLED" ? "데이터 소스를 중지했습니다." : "데이터 소스를 다시 활성화했습니다.");
      props.reload();
    } catch (error) { app.showToast(errorMessage(error), "error"); }
    finally { setBusy(undefined); }
  };
  const rotate = async () => {
    if (busy() || !confirm("Provider key를 재발급하면 이전 key는 즉시 사용할 수 없습니다. 계속할까요?")) return;
    setBusy("rotate");
    props.clearSecret();
    try {
      const payload = (await props.rotateKey({ input: { id: props.provider.id } })).rotateCustomProviderKey;
      throwPayloadErrors(payload.errors);
      if (!payload.ingestionKey) throw new Error("Provider key를 재발급하지 못했습니다.");
      props.showSecret(payload.ingestionKey);
      app.showToast("새 Provider key를 발급했습니다.");
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) app.showToast(errorMessage(error), "error");
    }
    finally { setBusy(undefined); }
  };
  const deleteProvider = async () => {
    if (busy() || !confirm("이 데이터 소스를 삭제할까요? 연결된 활동 기록도 더 이상 표시되지 않을 수 있습니다.")) return;
    setBusy("delete");
    try {
      const payload = (await remove({ input: { id: props.provider.id } })).deleteCustomProvider;
      throwPayloadErrors(payload.errors);
      if (payload.deletedProviderID !== props.provider.id) throw new Error("데이터 소스를 삭제하지 못했습니다.");
      app.showToast("데이터 소스를 삭제했습니다.");
      props.reload();
    } catch (error) { app.showToast(errorMessage(error), "error"); }
    finally { setBusy(undefined); }
  };
  return <article class="custom-provider-card">
    <div><span class="custom-icon" aria-hidden="true">{props.provider.name.slice(0, 1).toUpperCase()}</span>
      <div><h3>{props.provider.name} <small class={`status-pill status-${status()}`} data-status={status()}>
        {status() === "active" ? "사용 중" : "중지됨"}</small></h3>
        <p>{props.provider.description || `${props.provider.allowedActions.join(", ")} 활동`}</p></div>
    </div>
    <p class="provider-endpoint"><span>식별자 · 마지막 수정 {formatTime(props.provider.updatedAt, "아직 수정하지 않음")}</span>
      <code>{props.provider.slug}</code></p>
    <div class="card-actions">
      <button class="secondary-button" type="button" disabled={status() === "disabled" || Boolean(busy())}
        onClick={() => props.select(props.provider)}>활동 입력</button>
      <button class="text-button" type="button" disabled={Boolean(busy())} onClick={toggle}>
        {busy() === "toggle" ? "처리 중…" : status() === "active" ? "중지" : "다시 사용"}</button>
      <button class="text-button" type="button" disabled={Boolean(busy())} onClick={rotate}>
        {busy() === "rotate" ? "재발급 중…" : "Key 재발급"}</button>
      <button class="icon-button danger" type="button" disabled={Boolean(busy())}
        aria-label={`${props.provider.name} 삭제`} onClick={deleteProvider}><Icon name="trash" /></button>
    </div>
  </article>;
}

export function CustomProviderCardsRelay(props: { providers: readonly Provider[];
  select: (provider: Provider) => void; showSecret: (secret: string) => void; clearSecret: () => void;
  reload: () => void; rotateKey: Rotate }) {
  return <Show when={props.providers.length > 0} fallback={<div class="empty-list">
    <span class="empty-sprout" aria-hidden="true" /><h3>아직 만든 데이터 소스가 없어요.</h3>
    <p>오른쪽 양식에서 첫 번째 기록 종류를 만들어보세요.</p></div>}>
    <For each={props.providers}>{(provider) => <CustomProviderCard provider={provider} select={props.select}
      showSecret={props.showSecret} clearSecret={props.clearSecret} reload={props.reload} rotateKey={props.rotateKey} />}</For>
  </Show>;
}
