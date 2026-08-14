import { Show, createSignal, onSettled } from "solid-js";
import { Icon } from "./common";
import { useAppState } from "../app/state";

export function CopyButton(props: {
  value: string;
  label: string;
  copiedLabel: string;
  successMessage: string;
  failureMessage: string;
}) {
  const app = useAppState();
  const [copied, setCopied] = createSignal(false);
  let timer: number | undefined;

  onSettled(() => () => {
    if (timer !== undefined) window.clearTimeout(timer);
  });

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(props.value);
      setCopied(true);
      app.showToast(props.successMessage);
      if (timer !== undefined) window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        setCopied(false);
        timer = undefined;
      }, 1600);
    } catch {
      app.showToast(props.failureMessage, "error");
    }
  };

  return (
    <button
      type="button"
      aria-label={copied() ? props.copiedLabel : props.label}
      data-copied={copied() ? "true" : undefined}
      onClick={copy}
    >
      <Show when={copied()} fallback={<Icon name="copy" />}>
        <Icon name="check" />
      </Show>
    </button>
  );
}
