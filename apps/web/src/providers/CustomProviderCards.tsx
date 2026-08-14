import type { CustomProviderDto } from "@jandibat/contracts";
import { For, Show, createSignal } from "solid-js";
import { api } from "../api/client";
import { useAppState } from "../app/state";
import { Icon, errorMessage, formatTime } from "../components/common";

function CustomProviderCard(props: {
  subject: string;
  provider: CustomProviderDto;
  reload: () => void;
  select: (provider: CustomProviderDto) => void;
  showSecret: (secret: string) => void;
}) {
  const app = useAppState();
  const [busy, setBusy] = createSignal<"toggle" | "rotate" | "delete">();
  const status = () => props.provider.status === "active" ? "active" : "disabled";

  const toggle = async () => {
    setBusy("toggle");
    try {
      await api.updateCustomProvider(props.subject, props.provider.id, {
        status: status() === "active" ? "disabled" : "active",
      });
      app.showToast(status() === "active"
        ? "데이터 소스를 중지했습니다."
        : "데이터 소스를 다시 활성화했습니다.");
      props.reload();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
      setBusy(undefined);
    }
  };

  const rotate = async () => {
    if (!confirm("Provider key를 재발급하면 이전 key는 즉시 사용할 수 없습니다. 계속할까요?")) return;
    setBusy("rotate");
    try {
      const result = await api.rotateCustomProviderKey(props.subject, props.provider.id);
      props.showSecret(result.ingestionKey);
      app.showToast("새 Provider key를 발급했습니다.");
    } catch (error) {
      app.showToast(errorMessage(error), "error");
    } finally {
      setBusy(undefined);
    }
  };

  const remove = async () => {
    if (!confirm("이 데이터 소스를 삭제할까요? 연결된 활동 기록도 더 이상 표시되지 않을 수 있습니다.")) return;
    setBusy("delete");
    try {
      await api.deleteCustomProvider(props.subject, props.provider.id);
      app.showToast("데이터 소스를 삭제했습니다.");
      props.reload();
    } catch (error) {
      app.showToast(errorMessage(error), "error");
      setBusy(undefined);
    }
  };

  return (
    <article class="custom-provider-card">
      <div>
        <span class="custom-icon" aria-hidden="true">
          {props.provider.name.slice(0, 1).toUpperCase()}
        </span>
        <div>
          <h3>
            {props.provider.name}{" "}
            <small class={`status-pill status-${status()}`} data-status={status()}>
              {status() === "active" ? "사용 중" : "중지됨"}
            </small>
          </h3>
          <p>{props.provider.description || `${props.provider.allowedActions.join(", ")} 활동`}</p>
        </div>
      </div>
      <p class="provider-endpoint">
        <span>식별자 · 마지막 입력 {formatTime(props.provider.lastIngestedAt, "아직 입력하지 않음")}</span>
        <code>{props.provider.key}</code>
      </p>
      <div class="card-actions">
        <button
          class="secondary-button"
          type="button"
          disabled={status() === "disabled" || Boolean(busy())}
          onClick={() => props.select(props.provider)}
        >
          활동 입력
        </button>
        <button class="text-button" type="button" disabled={Boolean(busy())} onClick={toggle}>
          {busy() === "toggle" ? "처리 중…" : status() === "active" ? "중지" : "다시 사용"}
        </button>
        <button class="text-button" type="button" disabled={Boolean(busy())} onClick={rotate}>
          {busy() === "rotate" ? "재발급 중…" : "Key 재발급"}
        </button>
        <button
          class="icon-button danger"
          type="button"
          disabled={Boolean(busy())}
          aria-label={`${props.provider.name} 삭제`}
          onClick={remove}
        >
          <Icon name="trash" />
        </button>
      </div>
    </article>
  );
}

export function CustomProviderCards(props: {
  subject: string;
  providers: readonly CustomProviderDto[];
  reload: () => void;
  select: (provider: CustomProviderDto) => void;
  showSecret: (secret: string) => void;
}) {
  return (
    <Show
      when={props.providers.length > 0}
      fallback={(
        <div class="empty-list">
          <span class="empty-sprout" aria-hidden="true" />
          <h3>아직 만든 데이터 소스가 없어요.</h3>
          <p>오른쪽 양식에서 첫 번째 기록 종류를 만들어보세요.</p>
        </div>
      )}
    >
      <For each={props.providers}>
        {(provider) => (
          <CustomProviderCard
            subject={props.subject}
            provider={provider}
            reload={props.reload}
            select={props.select}
            showSecret={props.showSecret}
          />
        )}
      </For>
    </Show>
  );
}
