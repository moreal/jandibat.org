import { useLocation } from "@solidjs/router";
import {
  Match,
  Show,
  Switch,
  createEffect,
  createSignal,
  onSettled,
  type ParentProps,
} from "solid-js";
import { AppStateProvider, useAppState } from "./app/state";
import { loadRuntimeConfig } from "./runtime-config";
import { Router } from "./router";
import { migrateLegacyHashLocation } from "./routing/legacy";
import "./styles.css";

type RuntimeState =
  | { status: "loading" }
  | { status: "ready" }
  | { status: "error"; error: Error };

const navigation = [
  { path: "/", label: "잔디밭" },
  { path: "/connections", label: "연결" },
  { path: "/auth", label: "로그인" },
  { path: "/custom", label: "커스텀 데이터" },
  { path: "/embed", label: "임베드" },
] as const;

function routeLabel(pathname: string): string {
  if (pathname.startsWith("/connections")) return "연결";
  if (pathname.startsWith("/auth")) return "로그인";
  if (pathname.startsWith("/custom")) return "커스텀 데이터";
  if (pathname.startsWith("/embed")) return "임베드";
  return "잔디밭";
}

function activePath(pathname: string, path: string): boolean {
  return path === "/"
    ? pathname === "/" || pathname.startsWith("/explore")
    : pathname === path || pathname.startsWith(`${path}/`);
}

function AppShell(props: ParentProps) {
  const location = useLocation();
  const state = useAppState();

  createEffect(
    () => location.pathname,
    (pathname) => {
      document.title = `${routeLabel(pathname)} · jandibat.org`;
      window.scrollTo({ top: 0, behavior: "instant" });
    },
  );

  const links = () => navigation.map((item) => (
    <a
      href={item.path}
      class={activePath(location.pathname, item.path) ? "is-active" : undefined}
      aria-current={activePath(location.pathname, item.path) ? "page" : undefined}
    >
      <span>{item.label}</span>
    </a>
  ));

  return (
    <>
      <a class="skip-link" href="#content">본문으로 바로가기</a>
      <header class="site-header">
        <a class="brand" href="/" aria-label="jandibat.org 홈">
          <span class="brand-seed" aria-hidden="true" />
          <span>jandibat<em>.org</em></span>
        </a>
        <nav class="desktop-nav" aria-label="주요 메뉴">{links()}</nav>
        <a class="header-cta" href="/auth">시작하기 <span aria-hidden="true">→</span></a>
      </header>
      <main id="content" tabindex="-1">{props.children}</main>
      <nav class="mobile-nav" aria-label="모바일 주요 메뉴">{links()}</nav>
      <footer class="site-footer">
        <p><span class="brand-seed small" aria-hidden="true" /> 기록이 자라는 곳, jandibat.org</p>
        <p>Open source · AGPL-3.0</p>
      </footer>
      <Show when={state.toast()}>
        <div
          class="toast"
          role={state.toast()?.tone === "error" ? "alert" : "status"}
          aria-live={state.toast()?.tone === "error" ? "assertive" : "polite"}
          data-state="open"
          data-tone={state.toast()?.tone}
        >
          {state.toast()?.message}
        </div>
      </Show>
    </>
  );
}

function RuntimeError(props: { error: Error; retry: () => void }) {
  return (
    <main class="bootstrap-error" role="alert">
      <h1>앱 설정을 확인해 주세요.</h1>
      <p>{props.error.message}</p>
      <button class="secondary-button" type="button" onClick={props.retry}>다시 시도</button>
    </main>
  );
}

export default function App() {
  const [runtime, setRuntime] = createSignal<RuntimeState>({ status: "loading" });
  let attempt = 0;

  const load = () => {
    const currentAttempt = ++attempt;
    setRuntime({ status: "loading" });
    void loadRuntimeConfig().then(
      () => {
        if (currentAttempt === attempt) {
          migrateLegacyHashLocation();
          setRuntime({ status: "ready" });
        }
      },
      (error: unknown) => {
        if (currentAttempt !== attempt) return;
        setRuntime({
          status: "error",
          error: error instanceof Error ? error : new Error("알 수 없는 설정 오류입니다."),
        });
      },
    );
  };

  onSettled(() => {
    load();
    return () => {
      attempt += 1;
    };
  });

  return (
    <Switch>
      <Match when={runtime().status === "loading"}>
        <main class="bootstrap-error" role="status"><p>앱 설정을 불러오는 중…</p></main>
      </Match>
      <Match when={runtime().status === "error"}>
        <RuntimeError
          error={(runtime() as Extract<RuntimeState, { status: "error" }>).error}
          retry={load}
        />
      </Match>
      <Match when={runtime().status === "ready"}>
        <AppStateProvider>
          <Router>{(props) => <AppShell>{props.children}</AppShell>}</Router>
        </AppStateProvider>
      </Match>
    </Switch>
  );
}
