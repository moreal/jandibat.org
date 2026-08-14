import type { Element } from "solid-js";
import { ApiError } from "../api/client";

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.retryAfter
      ? `${error.message} (${error.retryAfter} 후 다시 시도할 수 있습니다.)`
      : error.message;
  }
  if (error instanceof Error) return error.message;
  return "예상하지 못한 오류가 발생했습니다.";
}

export function formatTime(
  value?: string | null,
  empty = "아직 동기화하지 않음",
): string {
  if (!value) return empty;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

export function preferredScrollBehavior(): ScrollBehavior {
  return matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
}

export function PageIntro(props: {
  eyebrow: string;
  title: Element;
  description: string;
}) {
  return (
    <header class="page-intro">
      <p class="eyebrow">{props.eyebrow}</p>
      <h1>{props.title}</h1>
      <p>{props.description}</p>
    </header>
  );
}

export function ErrorCallout(props: { error: unknown; retry?: () => void }) {
  return (
    <div class="callout error-callout" role="alert">
      <div>
        <strong>불러오지 못했어요</strong>
        <p>{errorMessage(props.error)}</p>
      </div>
      {props.retry ? (
        <button class="text-button" type="button" onClick={props.retry}>다시 시도</button>
      ) : null}
    </div>
  );
}

export function LoadingCards(props: { message: string; count?: number }) {
  const count = props.count ?? 1;
  return (
    <>
      <p class="sr-only" role="status">{props.message}</p>
      {Array.from({ length: count }, (_, index) => (
        <div class="card-skeleton" aria-hidden="true" data-skeleton={index} />
      ))}
    </>
  );
}

export function Icon(props: { name: "arrow" | "check" | "copy" | "sync" | "trash" }) {
  const values = {
    arrow: "→",
    check: "✓",
    copy: "⧉",
    sync: "↻",
    trash: "×",
  } as const;
  return <span aria-hidden="true">{values[props.name]}</span>;
}
