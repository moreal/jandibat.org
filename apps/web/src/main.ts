import type {
  ActivityHeatmapResponseDto,
  AuthResultDto,
  ProviderIdDto,
  SubjectDto,
} from "@jandibat/contracts";
import { ApiError, api, defaultApiBaseUrl } from "./api/client";
import { validateAuthorizationUrl } from "./api/authorization-url";
import {
  providerCallbackLocationHasData,
  providerCallbackResultFromUrl,
  providerCallbackSafeLocation,
} from "./api/provider-callback";
import {
  magicLinkLocationHasToken,
  magicLinkRedirectUrl,
  magicLinkSafeLocation,
  magicLinkTokenFromUrl,
} from "./auth/magic-link";
import { createPasskey, getPasskey, passkeyAvailable } from "./auth/passkey";
import { buildHeatmapCalendar, todayDateKey } from "./heatmap/calendar";
import { bindHeatmapTooltip, renderHeatmap } from "./heatmap/view";
import {
  privateConsentEligible,
  privateConsentValue,
  renderCustomProviderCards,
  renderProviderCards,
} from "./providers/view";
import {
  createSubjectInput,
  preferredOwnedSubject,
  SubjectInputError,
} from "./subjects/onboarding";
import {
  escapeHtml,
  escapeMarkdownDestination,
  escapeMarkdownLabel,
  icon,
} from "./ui/html";

type Route = "explore" | "connections" | "auth" | "custom" | "embed";

const routeLabels: Record<Route, string> = {
  explore: "잔디밭",
  connections: "연결",
  auth: "로그인",
  custom: "커스텀 데이터",
  embed: "임베드",
};

const root = document.querySelector<HTMLDivElement>("#app");
if (!root) throw new Error("#app element is required");

let requestController: AbortController | undefined;
let pendingMagicLinkToken: string | undefined;
let toastTimer: number | undefined;
let toastHideTimer: number | undefined;
let toastFrame: number | undefined;
const OWNER_SUBJECT_STORAGE_KEY = "jandibat:owner-subject";
const EXPLORE_SUBJECT_STORAGE_KEY = "jandibat:explore-subject";
let currentSubject = localStorage.getItem(OWNER_SUBJECT_STORAGE_KEY)?.trim() ?? "";
let exploreSubject = localStorage.getItem(EXPLORE_SUBJECT_STORAGE_KEY)?.trim() ?? "";
let ownedSubjects: SubjectDto[] = [];

function currentRoute(): Route {
  if (magicLinkLocationHasToken(location.href)) return "auth";
  const candidate = location.hash.slice(1).split(/[/?]/)[0];
  return candidate in routeLabels ? (candidate as Route) : "explore";
}

function shellMarkup(): string {
  const nav = (Object.entries(routeLabels) as Array<[Route, string]>)
    .map(
      ([route, label]) =>
        `<a href="#${route}" data-nav="${route}"><span>${escapeHtml(label)}</span></a>`,
    )
    .join("");
  return `
    <a class="skip-link" href="#content">본문으로 바로가기</a>
    <header class="site-header">
      <a class="brand" href="#explore" aria-label="jandibat.org 홈">
        <span class="brand-seed" aria-hidden="true"></span>
        <span>jandibat<em>.org</em></span>
      </a>
      <nav class="desktop-nav" aria-label="주요 메뉴">${nav}</nav>
      <a class="header-cta" href="#auth">시작하기 ${icon("arrow")}</a>
    </header>
    <main id="content" tabindex="-1"></main>
    <nav class="mobile-nav" aria-label="모바일 주요 메뉴">${nav}</nav>
    <footer class="site-footer">
      <p><span class="brand-seed small" aria-hidden="true"></span> 기록이 자라는 곳, jandibat.org</p>
      <p>Open source · AGPL-3.0</p>
    </footer>
    <div id="toast" class="toast" role="status" aria-live="polite" data-state="closed" hidden></div>
  `;
}

root.innerHTML = shellMarkup();
const content = document.querySelector<HTMLElement>("#content")!;

function setActiveNavigation(route: Route): void {
  document.querySelectorAll<HTMLElement>("[data-nav]").forEach((link) => {
    const active = link.dataset.nav === route;
    link.classList.toggle("is-active", active);
    if (active) link.setAttribute("aria-current", "page");
    else link.removeAttribute("aria-current");
  });
  document.title = `${routeLabels[route]} · jandibat.org`;
}

function toast(message: string, tone: "success" | "error" = "success"): void {
  const element = document.querySelector<HTMLElement>("#toast");
  if (!element) return;
  if (toastTimer !== undefined) window.clearTimeout(toastTimer);
  if (toastHideTimer !== undefined) window.clearTimeout(toastHideTimer);
  if (toastFrame !== undefined) window.cancelAnimationFrame(toastFrame);

  const wasHidden = element.hidden;
  element.textContent = message;
  element.dataset.tone = tone;
  element.setAttribute("role", tone === "error" ? "alert" : "status");
  element.setAttribute("aria-live", tone === "error" ? "assertive" : "polite");
  element.hidden = false;
  if (wasHidden) {
    element.dataset.state = "closed";
    toastFrame = window.requestAnimationFrame(() => {
      element.dataset.state = "open";
      toastFrame = undefined;
    });
  } else {
    element.dataset.state = "open";
  }

  const duration = Math.max(4200, Math.min(8000, message.length * 95));
  toastTimer = window.setTimeout(() => {
    element.dataset.state = "closed";
    toastTimer = undefined;
    toastHideTimer = window.setTimeout(() => {
      element.hidden = true;
      toastHideTimer = undefined;
    }, 180);
  }, duration);
}

function preferredScrollBehavior(): ScrollBehavior {
  return matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth";
}

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    return error.retryAfter
      ? `${error.message} (${error.retryAfter} 후 다시 시도할 수 있습니다.)`
      : error.message;
  }
  if (error instanceof Error) return error.message;
  return "예상하지 못한 오류가 발생했습니다.";
}

function textElement<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  text: string,
  className?: string,
): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (className) element.className = className;
  element.textContent = text;
  return element;
}

function createErrorCallout(error: unknown, retryAction?: string): HTMLElement {
  const callout = document.createElement("div");
  callout.className = "callout error-callout";
  callout.setAttribute("role", "alert");
  const copy = document.createElement("div");
  copy.append(
    textElement("strong", "불러오지 못했어요"),
    textElement("p", errorMessage(error)),
  );
  callout.append(copy);
  if (retryAction) {
    const retry = textElement("button", "다시 시도", "text-button");
    retry.type = "button";
    retry.dataset.action = retryAction;
    callout.append(retry);
  }
  return callout;
}

function pageIntro(eyebrow: string, title: string, description: string): string {
  return `<header class="page-intro"><p class="eyebrow">${eyebrow}</p><h1>${title}</h1><p>${description}</p></header>`;
}

function deviceTimezone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
}

function selectOwnerSubject(subject: SubjectDto): void {
  currentSubject = subject.handle;
  localStorage.setItem(OWNER_SUBJECT_STORAGE_KEY, subject.handle);
}

function clearOwnerSubject(): void {
  currentSubject = "";
  localStorage.removeItem(OWNER_SUBJECT_STORAGE_KEY);
}

function setOwnedSubjects(subjects: SubjectDto[]): SubjectDto | undefined {
  ownedSubjects = subjects;
  const selected = preferredOwnedSubject(subjects, currentSubject);
  if (selected) selectOwnerSubject(selected);
  else clearOwnerSubject();
  return selected;
}

function ownerSubjectCreateMarkup(): string {
  return `<div class="form-card">
    <p class="eyebrow">Create your garden</p>
    <h2>첫 잔디밭을 만들어 주세요.</h2>
    <p>Provider와 커스텀 데이터를 연결할 공개 프로필을 먼저 만듭니다.</p>
    <form class="stack-form" data-subject-create novalidate>
      <label for="owner-subject-handle">프로필 식별자</label>
      <div class="input-prefix"><span>@</span><input id="owner-subject-handle" name="handle" autocomplete="username" maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" placeholder="my-garden" required></div>
      <small>영문자·숫자로 시작하고 점, 밑줄, 하이픈을 사용할 수 있어요.</small>
      <label for="owner-subject-display-name">표시 이름 <span>(선택)</span></label>
      <input id="owner-subject-display-name" name="displayName" maxlength="100" placeholder="나의 잔디밭">
      <p class="inline-error" data-subject-error role="alert" hidden></p>
      <button class="primary-button wide" type="submit">잔디밭 만들기 ${icon("arrow")}</button>
    </form>
  </div>`;
}

function ownerSubjectSelectElement(): HTMLFormElement {
  const form = document.createElement("form");
  form.className = "connection-toolbar";
  form.dataset.subjectSelect = "";
  const label = document.createElement("label");
  label.htmlFor = "owner-subject-select";
  label.append(textElement("strong", "관리할 잔디밭"));
  const select = document.createElement("select");
  select.id = "owner-subject-select";
  select.name = "subject";
  select.required = true;
  const placeholder = textElement("option", "선택해 주세요");
  placeholder.value = "";
  placeholder.disabled = true;
  placeholder.selected = !currentSubject;
  select.append(placeholder);
  for (const subject of ownedSubjects) {
    const option = textElement("option", subject.displayName || `@${subject.handle}`);
    option.value = subject.handle;
    option.selected = subject.handle === currentSubject;
    select.append(option);
  }
  const submit = textElement("button", "선택", "secondary-button");
  submit.type = "submit";
  form.append(label, select, submit);
  return form;
}

function mountOwnerSubjectSelects(container: ParentNode): void {
  container.querySelectorAll<HTMLElement>("[data-owner-subject-select-slot]").forEach((slot) => {
    slot.replaceWith(ownerSubjectSelectElement());
  });
}

function ownerGateMarkup(kind: "signed-out" | "create" | "select"): string {
  const body = kind === "signed-out"
    ? `<div class="form-card"><p class="eyebrow">Sign in required</p><h2>로그인 후 관리할 수 있어요.</h2><p>소유한 잔디밭을 확인한 뒤에만 Provider와 커스텀 데이터 요청을 보냅니다.</p><a class="primary-button wide" href="#auth">로그인하기 ${icon("arrow")}</a></div>`
    : kind === "create"
      ? ownerSubjectCreateMarkup()
      : `<div class="form-card"><p class="eyebrow">Choose your garden</p><h2>관리할 잔디밭을 선택해 주세요.</h2><p>선택하기 전에는 소유자 전용 기능이 비활성화됩니다.</p><div data-owner-subject-select-slot></div></div>`;
  return `<section class="page section-shell narrow">${pageIntro("Owner workspace", "내 잔디밭을<br><em>안전하게 관리하세요.</em>", "인증된 계정이 소유한 프로필만 연결과 커스텀 데이터에 사용할 수 있습니다.")}${body}</section>`;
}

function bindOwnerSubjectControls(): void {
  content.querySelector<HTMLFormElement>("[data-subject-select]")?.addEventListener("submit", (event) => {
    event.preventDefault();
    const handle = String(new FormData(event.currentTarget as HTMLFormElement).get("subject") ?? "");
    const subject = ownedSubjects.find((candidate) => candidate.handle === handle);
    if (!subject) return;
    selectOwnerSubject(subject);
    void renderRoute();
  });

  content.querySelector<HTMLFormElement>("[data-subject-create]")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const values = new FormData(form);
    const errorElement = form.querySelector<HTMLElement>("[data-subject-error]");
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]");
    if (errorElement) errorElement.hidden = true;

    let input;
    try {
      input = createSubjectInput(
        String(values.get("handle") ?? ""),
        String(values.get("displayName") ?? ""),
        deviceTimezone(),
      );
    } catch (error) {
      const message = errorMessage(error);
      if (errorElement) {
        errorElement.textContent = message;
        errorElement.hidden = false;
      }
      if (error instanceof SubjectInputError) {
        const field = form.elements.namedItem(error.field);
        if (field instanceof HTMLElement) field.focus();
      }
      return;
    }

    if (button) setButtonBusy(button, true, "만드는 중…");
    try {
      const created = await api.createSubject(input);
      ownedSubjects = [...ownedSubjects, created];
      selectOwnerSubject(created);
      toast(`@${created.handle} 잔디밭을 만들었습니다.`);
      await renderRoute();
    } catch (error) {
      const message = errorMessage(error);
      if (errorElement) {
        errorElement.textContent = message;
        errorElement.hidden = false;
      }
      if (button) setButtonBusy(button, false);
    }
  });
}

async function requireOwnerSubject(): Promise<string | undefined> {
  content.innerHTML = `<section class="page section-shell narrow"><div class="heatmap-loading" role="status"><p>소유한 잔디밭을 확인하는 중…</p></div></section>`;
  requestController?.abort();
  requestController = new AbortController();
  try {
    await api.getCurrentSession(requestController.signal);
    const response = await api.listSubjects(requestController.signal);
    const selected = setOwnedSubjects(response.subjects);
    if (selected) return selected.handle;
    content.innerHTML = ownerGateMarkup(response.subjects.length === 0 ? "create" : "select");
    mountOwnerSubjectSelects(content);
    bindOwnerSubjectControls();
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") return undefined;
    if (error instanceof ApiError && error.status === 401) {
      ownedSubjects = [];
      clearOwnerSubject();
      content.innerHTML = ownerGateMarkup("signed-out");
      return undefined;
    }
    const section = document.createElement("section");
    section.className = "page section-shell narrow";
    section.append(createErrorCallout(error, "retry-owner-subject"));
    content.replaceChildren(section);
  }
  return undefined;
}

async function renderExplore(subject = exploreSubject): Promise<void> {
  exploreSubject = subject.trim();
  if (exploreSubject) localStorage.setItem(EXPLORE_SUBJECT_STORAGE_KEY, exploreSubject);
  else localStorage.removeItem(EXPLORE_SUBJECT_STORAGE_KEY);
  const calendar = buildHeatmapCalendar([], todayDateKey());

  content.innerHTML = `
    <section class="hero">
      <div class="hero-copy">
        <p class="eyebrow">Your work, in full color.</p>
        <h1>매일의 작은 기록이<br><em>나만의 잔디밭</em>이<span class="hero-ending"> 됩니다.</span></h1>
        <p class="hero-description">GitHub부터 독서, 운동까지. 흩어진 활동을 한곳에 모아 오래 보고 싶은 기록으로 남겨보세요.</p>
        <form class="subject-search" id="subject-search">
          <label for="subject">공개 프로필 찾아보기</label>
          <div><span aria-hidden="true">@</span><input id="subject" name="subject" autocomplete="off" spellcheck="false" maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" required><button class="primary-button" type="submit">잔디밭 보기 ${icon("arrow")}</button></div>
        </form>
        <div class="trust-row" aria-label="지원하는 데이터 소스"><span>GITHUB</span><span>GITLAB</span><span>CODEBERG</span><span>+ YOUR DATA</span></div>
      </div>
      <aside class="hero-note" aria-label="제품 소개">
        <span class="note-number">365</span><span>DAYS</span>
        <p>오늘 심은 기록은<br>내일의 방향이 됩니다.</p>
        <i aria-hidden="true"></i>
      </aside>
    </section>
    <section class="heatmap-section section-shell" aria-live="polite">
      <div class="heatmap-loading" role="status">
        <div><span></span><span></span><span></span><span></span></div>
        <p data-explore-loading-copy></p>
      </div>
    </section>
    <section class="feature-strip" aria-label="jandibat의 특징">
      <article><span>01</span><h2>한눈에</h2><p>일 년의 흐름을 한 화면에서 발견하세요.</p></article>
      <article><span>02</span><h2>모두 함께</h2><p>코드 밖의 활동도 같은 언어로 기록하세요.</p></article>
      <article><span>03</span><h2>어디서나</h2><p>README와 블로그에 살아 있는 기록을 공유하세요.</p></article>
    </section>
  `;

  const subjectInput = content.querySelector<HTMLInputElement>("#subject");
  if (subjectInput) subjectInput.value = exploreSubject;
  const loadingCopy = content.querySelector<HTMLElement>("[data-explore-loading-copy]");
  if (loadingCopy) {
    loadingCopy.textContent = exploreSubject
      ? `@${exploreSubject}의 기록을 불러오는 중…`
      : "보고 싶은 공개 프로필을 입력해 주세요.";
  }

  content.querySelector<HTMLFormElement>("#subject-search")?.addEventListener("submit", (event) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget as HTMLFormElement);
    void renderExplore(String(data.get("subject") ?? ""));
  });

  if (!exploreSubject) return;

  requestController?.abort();
  requestController = new AbortController();
  const heatmapSection = content.querySelector<HTMLElement>(".heatmap-section");
  try {
    const response = await api.getActivities(exploreSubject, {
      from: calendar.from,
      to: calendar.to,
      signal: requestController.signal,
    });
    if (!heatmapSection) return;
    renderHeatmap(heatmapSection, response);
    bindHeatmapTooltip(heatmapSection);
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") return;
    if (!heatmapSection) return;
    const empty = document.createElement("div");
    empty.className = "empty-heatmap";
    const sprout = document.createElement("span");
    sprout.className = "empty-sprout";
    sprout.setAttribute("aria-hidden", "true");
    empty.append(
      sprout,
      textElement("h2", `@${exploreSubject}의 잔디밭을 기다리고 있어요.`),
      textElement("p", "API가 연결되면 최근 1년 활동이 이곳에 표시됩니다."),
    );
    heatmapSection.replaceChildren(createErrorCallout(error, "retry-explore"), empty);
  }
}

function formatTime(value?: string | null): string {
  if (!value) return "아직 동기화하지 않음";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

async function renderConnections(): Promise<void> {
  const callbackResult = providerCallbackResultFromUrl(location.href);
  if (providerCallbackLocationHasData(location.href)) {
    history.replaceState(null, "", providerCallbackSafeLocation(location.href));
  }
  const ownerSubject = await requireOwnerSubject();
  if (!ownerSubject) return;
  content.innerHTML = `
    <section class="page section-shell narrow">
      ${pageIntro("Connect your world", "하나의 잔디밭,<br><em>여러 개의 시작점.</em>", "사용하는 서비스를 연결하면 활동을 안전하게 가져와 하나의 흐름으로 보여드려요.")}
      <div data-owner-subject-select-slot></div>
      <div class="connection-toolbar"><p><span class="pulse-dot"></span>연결 정보는 암호화되어 저장됩니다.</p><a href="#custom" class="text-link">직접 데이터 만들기 ${icon("arrow")}</a></div>
      <div class="provider-grid" id="provider-grid" aria-live="polite" aria-busy="true"><p class="sr-only" role="status">Provider 연결 정보를 불러오는 중입니다.</p><div class="card-skeleton" aria-hidden="true"></div><div class="card-skeleton" aria-hidden="true"></div><div class="card-skeleton" aria-hidden="true"></div></div>
    </section>`;
  mountOwnerSubjectSelects(content);
  bindOwnerSubjectControls();

  requestController?.abort();
  requestController = new AbortController();
  const grid = content.querySelector<HTMLElement>("#provider-grid");
  try {
    const [catalog, connections] = await Promise.all([
      api.listProviderCatalog(requestController.signal),
      api.listConnections(ownerSubject, requestController.signal),
    ]);
    if (grid) {
      grid.removeAttribute("aria-busy");
      renderProviderCards(grid, catalog.providers, connections.connections);
      if (callbackResult) toast("Provider 연결을 완료했습니다.");
      grid.querySelectorAll<HTMLFormElement>("[data-provider-connect]").forEach((form) => {
        const method = form.elements.namedItem("authMethod") as HTMLSelectElement;
        const token = form.elements.namedItem("token") as HTMLInputElement;
        const includePrivate = form.elements.namedItem("includePrivate") as HTMLInputElement | null;
        const tokenField = token.closest<HTMLElement>(".provider-token-field");
        const consentField = includePrivate?.closest<HTMLElement>(".private-consent-field");
        const updateConnectionFields = () => {
          const tokenRequired = method.value === "token";
          if (tokenField) tokenField.hidden = !tokenRequired;
          token.required = tokenRequired;

          const consentRequired = privateConsentEligible(
            form.dataset.supportsPrivate === "true",
            method.value,
          );
          if (consentField) consentField.hidden = !consentRequired;
          if (includePrivate) {
            includePrivate.required = consentRequired;
            includePrivate.setAttribute("aria-required", String(consentRequired));
            if (!consentRequired) includePrivate.checked = false;
          }
        };
        method.addEventListener("change", updateConnectionFields);
        updateConnectionFields();
        form.addEventListener("submit", async (event) => {
          event.preventDefault();
          const provider = form.dataset.providerConnect as ProviderIdDto | undefined;
          const button = form.querySelector<HTMLButtonElement>("button[type=submit]");
          const errorElement = form.querySelector<HTMLElement>("[data-provider-error]");
          if (!provider || !button) return;
          if (!form.reportValidity()) return;
          if (errorElement) errorElement.hidden = true;
          setButtonBusy(button, true, "연결 준비 중…");
          try {
            const authMethod = method.value as "oauth2" | "token" | "none";
            const privateConsent = privateConsentValue(
              form.dataset.supportsPrivate === "true",
              authMethod,
              includePrivate?.checked === true,
            );
            const result = await api.connectProvider(ownerSubject, provider, {
              authMethod,
              token: authMethod === "token" ? token.value : undefined,
              includePrivate: privateConsent,
              redirectUri: authMethod === "oauth2" ? `${location.origin}${location.pathname}#connections` : undefined,
            });
            if (result.authorizationUrl) {
              location.assign(
                validateAuthorizationUrl(result.authorizationUrl, import.meta.env.DEV),
              );
              return;
            }
            toast(`${catalog.providers.find((item) => item.id === provider)?.name ?? provider} 연결을 시작했습니다.`);
            await renderConnections();
          } catch (error) {
            const message = errorMessage(error);
            if (errorElement) {
              errorElement.textContent = message;
              errorElement.hidden = false;
            }
            toast(message, "error");
            setButtonBusy(button, false);
          }
        });
      });
    }
  } catch (error) {
    if (error instanceof DOMException && error.name === "AbortError") return;
    if (grid) {
      grid.removeAttribute("aria-busy");
      grid.replaceChildren(createErrorCallout(error, "retry-connections"));
      grid.dataset.state = "ready";
    }
  }
}

function authMarkup(authenticated: boolean, subjectsReady = true): string {
  const unavailable = !passkeyAvailable();
  if (authenticated) {
    const subjectControls = subjectsReady
      ? ownedSubjects.length === 0
        ? ownerSubjectCreateMarkup()
        : '<div data-owner-subject-select-slot></div>'
      : "";
    return `
      <section class="auth-page">
        <div class="auth-story">
          <a class="mini-brand" href="#explore"><span class="brand-seed" aria-hidden="true"></span>jandibat.org</a>
          <div><p class="eyebrow">Signed in</p><h1>오늘의 기록을<br><em>이어갈 시간이에요.</em></h1><p>연결과 커스텀 데이터 소스를 관리하거나 새로운 Passkey를 등록하세요.</p></div>
          <blockquote>“꾸준함은 특별한 하루가 아니라<br>평범한 날들의 합입니다.”</blockquote>
        </div>
        <div class="auth-panel">
          <div class="auth-card account-card">
            <p class="eyebrow">Your account</p>
            <h2 data-auth-email></h2>
            <p>이 세션은 <span data-auth-expires></span>까지 유효합니다.</p>
            <dl class="account-meta"><div><dt>이메일 상태</dt><dd data-auth-email-status></dd></div><div><dt>계정 상태</dt><dd data-auth-account-status></dd></div></dl>
            ${subjectControls}
            <a class="primary-button wide" href="#connections">Provider 관리 ${icon("arrow")}</a>
            <button class="secondary-button wide" type="button" data-action="passkey-register" ${unavailable ? "disabled" : ""}>이 기기에 Passkey 등록</button>
            ${currentSubject ? `<button class="danger-button wide" type="button" data-action="delete-subject">선택한 잔디밭 삭제 요청</button>` : ""}
            <button class="text-button wide" type="button" data-action="sign-out">로그아웃</button>
          </div>
        </div>
      </section>`;
  }
  return `
    <section class="auth-page">
      <div class="auth-story">
        <a class="mini-brand" href="#explore"><span class="brand-seed" aria-hidden="true"></span>jandibat.org</a>
        <div><p class="eyebrow">Welcome back</p><h1>기록은 이어질 때<br><em>더 선명해집니다.</em></h1><p>비밀번호 없이 안전하게 로그인하고, 어디서든 나의 잔디밭을 이어가세요.</p></div>
        <blockquote>“꾸준함은 특별한 하루가 아니라<br>평범한 날들의 합입니다.”</blockquote>
      </div>
      <div class="auth-panel">
        <div class="auth-card">
          <p class="eyebrow">Sign in</p><h2>다시 만나서 반가워요.</h2><p>가장 편한 방법을 선택하세요.</p>
          <button class="passkey-button" type="button" data-action="passkey-signin" ${unavailable ? "disabled" : ""}><span class="passkey-symbol" aria-hidden="true">⌁</span><span><strong>Passkey로 로그인</strong><small>${unavailable ? "이 환경에서는 사용할 수 없어요" : "Touch ID, Face ID 또는 보안 키"}</small></span>${icon("arrow")}</button>
          <div class="or"><span>또는 이메일로</span></div>
          <form id="magic-link-form" class="stack-form" novalidate>
            <label for="email">이메일 주소</label>
            <input id="email" name="email" type="email" autocomplete="email" maxlength="320" placeholder="you@example.com" aria-describedby="magic-link-error" required>
            <p class="inline-error" id="magic-link-error" role="alert" hidden></p>
            <button class="primary-button wide" type="submit">로그인 링크 받기 ${icon("arrow")}</button>
          </form>
          <p class="auth-help">처음이신가요? 이메일 링크로 계정이 자동 생성됩니다.</p>
          <details><summary>이 기기에 Passkey 등록하기</summary><p>먼저 이메일 링크로 로그인하세요. 로그인한 계정 화면에서 Passkey를 추가할 수 있습니다.</p></details>
        </div>
      </div>
    </section>`;
}

function renderAuthContent(auth?: AuthResultDto, subjectsReady = true): void {
  content.innerHTML = authMarkup(Boolean(auth), subjectsReady);
  if (!auth) return;
  const email = content.querySelector<HTMLElement>("[data-auth-email]");
  const expires = content.querySelector<HTMLElement>("[data-auth-expires]");
  const emailStatus = content.querySelector<HTMLElement>("[data-auth-email-status]");
  const accountStatus = content.querySelector<HTMLElement>("[data-auth-account-status]");
  if (email) email.textContent = auth.user.primaryEmail;
  if (expires) expires.textContent = formatTime(auth.session.expiresAt);
  if (emailStatus) emailStatus.textContent = auth.user.emailVerifiedAt ? "확인됨" : "확인 대기";
  if (accountStatus) accountStatus.textContent = auth.user.status;
  mountOwnerSubjectSelects(content);
}

function takeMagicLinkToken(): string | undefined {
  const fragmentAwareToken = magicLinkTokenFromUrl(location.href);
  const locationHasToken = magicLinkLocationHasToken(location.href);
  if (locationHasToken) {
    history.replaceState(null, "", magicLinkSafeLocation(location.href));
  }
  if (fragmentAwareToken) {
    pendingMagicLinkToken = fragmentAwareToken;
    return pendingMagicLinkToken;
  }
  return pendingMagicLinkToken;
}

async function renderAuth(): Promise<void> {
  content.innerHTML = `<section class="page section-shell narrow"><div class="heatmap-loading" role="status"><p>로그인 상태를 확인하는 중…</p></div></section>`;
  let auth: AuthResultDto | undefined;
  const token = takeMagicLinkToken();
  try {
    if (token) {
      auth = await api.consumeMagicLink(token);
      pendingMagicLinkToken = undefined;
      toast("이메일을 확인하고 로그인했습니다.");
    } else {
      auth = await api.getCurrentSession();
    }
    if (auth) {
      const response = await api.listSubjects();
      setOwnedSubjects(response.subjects);
    }
  } catch (error) {
    if (
      token &&
      error instanceof ApiError &&
      error.status >= 400 &&
      error.status < 500 &&
      error.status !== 429
    ) {
      pendingMagicLinkToken = undefined;
    }
    if (!(error instanceof ApiError && error.status === 401 && !token)) {
      renderAuthContent(auth, false);
      const overlay = document.createElement("div");
      overlay.className = "auth-overlay-error";
      overlay.append(createErrorCallout(error, "retry-auth"));
      content.append(overlay);
      bindAuthForm();
      return;
    }
  }
  renderAuthContent(auth);
  bindAuthForm();
  bindOwnerSubjectControls();
}

function bindAuthForm(): void {
  content.querySelector<HTMLFormElement>("#magic-link-form")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const button = form.querySelector<HTMLButtonElement>("button");
    const errorElement = form.querySelector<HTMLElement>("#magic-link-error");
    const email = String(new FormData(form).get("email") ?? "");
    if (!form.reportValidity()) return;
    if (errorElement) errorElement.hidden = true;
    if (button) setButtonBusy(button, true, "보내는 중…");
    try {
      await api.requestMagicLink({
        email,
        redirectUri: magicLinkRedirectUrl(location.href),
      });
      const success = document.createElement("div");
      success.className = "success-state";
      const mark = textElement("span", "✓");
      mark.setAttribute("aria-hidden", "true");
      const copy = document.createElement("p");
      copy.append(
        textElement("strong", email),
        document.createTextNode("으로 로그인 링크를 보냈어요. 링크는 잠시 후 만료됩니다."),
      );
      success.append(mark, textElement("h3", "이메일을 확인해 주세요."), copy);
      form.replaceChildren(success);
    } catch (error) {
      const message = errorMessage(error);
      if (errorElement) {
        errorElement.textContent = message;
        errorElement.hidden = false;
      }
      toast(message, "error");
      if (button) setButtonBusy(button, false);
    }
  });
}

function showSecret(value: string): void {
  const panel = content.querySelector<HTMLElement>("#secret-result");
  const code = content.querySelector<HTMLElement>("#secret-value");
  if (!panel || !code) return;
  code.textContent = value;
  panel.hidden = false;
  panel.scrollIntoView({ behavior: preferredScrollBehavior(), block: "center" });
}

async function renderCustom(): Promise<void> {
  const ownerSubject = await requireOwnerSubject();
  if (!ownerSubject) return;
  content.innerHTML = `
    <section class="page section-shell narrow">
      ${pageIntro("Make it yours", "코드 밖의 하루도<br><em>같은 잔디밭 위에.</em>", "독서, 운동, 글쓰기처럼 나에게 중요한 활동을 직접 정의하고 기록하세요.")}
      <div data-owner-subject-select-slot></div>
      <div class="custom-layout">
        <div><div class="section-title"><div><p class="eyebrow">Your providers</p><h2>나의 데이터 소스</h2></div></div><div id="custom-list" class="custom-list" aria-live="polite" aria-busy="true"><p class="sr-only" role="status">커스텀 데이터 소스를 불러오는 중입니다.</p><div class="card-skeleton" aria-hidden="true"></div></div></div>
        <aside class="form-card">
          <p class="eyebrow">New provider</p><h2>새 데이터 소스</h2>
          <form id="custom-provider-form" class="stack-form">
            <label for="custom-name">이름</label><input id="custom-name" name="name" placeholder="예: 독서" maxlength="100" required>
            <label for="custom-slug">식별자</label><div class="input-prefix"><span>source/</span><input id="custom-slug" name="key" placeholder="reading" pattern="[a-z0-9][a-z0-9_-]*" maxlength="64" required></div><small>영문 소문자나 숫자로 시작하고 하이픈, 밑줄을 사용할 수 있어요.</small>
            <label for="custom-action">활동 종류</label><input id="custom-action" name="action" placeholder="예: read" maxlength="64" required>
            <label for="custom-description">설명 <span>(선택)</span></label><textarea id="custom-description" name="description" rows="3" maxlength="500" placeholder="어떤 활동을 기록할지 적어주세요."></textarea>
            <button class="primary-button wide" type="submit">데이터 소스 만들기 ${icon("arrow")}</button>
          </form>
        </aside>
      </div>
      <section class="ingest-panel" id="ingest-panel" hidden>
        <div><p class="eyebrow">Add activity</p><h2><span id="ingest-name"></span> 기록 입력</h2><p>특정 날짜의 활동량을 잔디밭에 더합니다.</p></div>
        <form id="ingest-form" class="inline-form"><input type="hidden" name="providerId"><label>Provider key<input type="password" name="ingestionKey" autocomplete="off" minlength="32" maxlength="2048" required></label><label>활동 종류<input name="action" maxlength="64" required></label><label>날짜<input type="date" name="date" max="${todayDateKey()}" value="${todayDateKey()}" required></label><label>활동량<input type="number" name="count" min="0" max="1000000" step="1" value="1" required></label><label>메모<input name="note" maxlength="1000" placeholder="선택 입력"></label><button class="primary-button" type="submit">기록 추가</button></form>
      </section>
      <section class="secret-result" id="secret-result" role="status" aria-live="polite" hidden><div><p class="eyebrow">Shown once</p><h2>Provider key를 지금 저장하세요.</h2><p>보안을 위해 이 값은 다시 표시되지 않습니다.</p></div><div class="code-box"><code id="secret-value"></code><button type="button" aria-label="Provider key 복사" data-action="copy-secret">${icon("copy")}</button></div></section>
    </section>`;
  mountOwnerSubjectSelects(content);
  bindOwnerSubjectControls();

  const list = content.querySelector<HTMLElement>("#custom-list");
  const load = async () => {
    try {
      const response = await api.listCustomProviders(ownerSubject);
      if (list) {
        list.removeAttribute("aria-busy");
        renderCustomProviderCards(list, response.providers);
      }
    } catch (error) {
      if (list) {
        list.removeAttribute("aria-busy");
        list.replaceChildren(createErrorCallout(error, "retry-custom"));
        list.dataset.state = "ready";
      }
    }
  };
  await load();

  content.querySelector<HTMLFormElement>("#custom-provider-form")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const values = new FormData(form);
    const button = form.querySelector<HTMLButtonElement>("button[type=submit]");
    if (button) setButtonBusy(button, true, "만드는 중…");
    try {
      const created = await api.createCustomProvider(ownerSubject, {
        name: String(values.get("name") ?? ""),
        key: String(values.get("key") ?? ""),
        allowedActions: [String(values.get("action") ?? "activity")],
        description: String(values.get("description") ?? "") || undefined,
      });
      form.reset();
      showSecret(created.ingestionKey);
      toast("데이터 소스를 만들었습니다. Provider key를 안전하게 저장하세요.");
      await load();
    } catch (error) {
      toast(errorMessage(error), "error");
    } finally {
      if (button) setButtonBusy(button, false);
    }
  });

  content.querySelector<HTMLFormElement>("#ingest-form")?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = event.currentTarget as HTMLFormElement;
    const values = new FormData(form);
    const button = form.querySelector<HTMLButtonElement>("button");
    if (button) setButtonBusy(button, true, "추가 중…");
    try {
      const note = String(values.get("note") ?? "");
      const result = await api.ingestCustomActivities(String(values.get("providerId") ?? ""), String(values.get("ingestionKey") ?? ""), {
        schemaVersion: "1.0",
        events: [{
          eventId: crypto.randomUUID(),
          date: String(values.get("date") ?? ""),
          action: String(values.get("action") ?? "activity"),
          metric: { name: "count", value: Number(values.get("count") ?? 0) },
          metadata: note ? { note } : undefined,
        }],
      });
      const rejectionDetail = result.rejections[0]?.detail;
      toast(result.rejected > 0 ? `활동 ${result.accepted}건 반영, ${result.rejected}건 거절됐습니다.${rejectionDetail ? ` ${rejectionDetail}` : ""}` : `활동 ${result.accepted}건을 반영했습니다.` , result.rejected > 0 ? "error" : "success");
      form.reset();
      const panel = content.querySelector<HTMLElement>("#ingest-panel");
      if (panel) panel.hidden = true;
    } catch (error) {
      toast(errorMessage(error), "error");
    } finally {
      if (button) setButtonBusy(button, false);
    }
  });
}

function embedValues(form: HTMLFormElement): { url: string; markdown: string; html: string } {
  const values = new FormData(form);
  const subject = String(values.get("subject") ?? "").trim();
  const theme = String(values.get("theme") ?? "system");
  const weekStart = String(values.get("weekStart") ?? "sunday");
  const cellSize = Math.max(6, Math.min(32, Number(values.get("cellSize") ?? 11)));
  const showLegend = values.get("showLegend") === "on";
  const title = String(values.get("title") ?? "").trim() || `${subject}'s activity`;
  const baseUrl = defaultApiBaseUrl() || location.origin;
  const params = new URLSearchParams({
    theme,
    weekStart,
    cellSize: String(cellSize),
    showLegend: String(showLegend),
  });
  const url = `${baseUrl}/v1/render/${encodeURIComponent(subject)}.svg?${params.toString()}`;
  const profileUrl = `${location.origin}${location.pathname}#explore/${encodeURIComponent(subject)}`;
  const markdownTitle = escapeMarkdownLabel(title);
  const markdownUrl = escapeMarkdownDestination(url);
  const markdownProfileUrl = escapeMarkdownDestination(profileUrl);
  return {
    url,
    markdown: `[![${markdownTitle}](${markdownUrl})](${markdownProfileUrl})`,
    html: `<a href="${escapeHtml(profileUrl)}"><img src="${escapeHtml(url)}" alt="${escapeHtml(title)}"></a>`,
  };
}

function renderEmbed(): void {
  const suggestedSubject = currentSubject || exploreSubject;
  const suggestedTitle = suggestedSubject ? `${suggestedSubject}'s activity` : "Activity heatmap";
  content.innerHTML = `
    <section class="page section-shell narrow">
      ${pageIntro("Share your garden", "당신의 기록을<br><em>어디서나 살아 있게.</em>", "README나 블로그에 붙여 넣을 수 있는 SVG 주소를 만들어보세요. 활동이 동기화될 때마다 자동으로 업데이트됩니다.")}
      <div class="embed-layout">
        <form id="embed-form" class="form-card stack-form">
          <label for="embed-subject">프로필 식별자</label><div class="input-prefix"><span>@</span><input id="embed-subject" name="subject" placeholder="my-garden" maxlength="64" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" required></div>
          <label for="embed-title">대체 텍스트</label><input id="embed-title" name="title" maxlength="200" required>
          <label for="embed-theme">테마</label><select id="embed-theme" name="theme"><option value="system">시스템</option><option value="light">라이트</option><option value="dark">다크</option><option value="github-light">GitHub 라이트</option><option value="github-dark">GitHub 다크</option></select>
          <label for="embed-week-start">한 주의 시작</label><select id="embed-week-start" name="weekStart"><option value="sunday">일요일</option><option value="monday">월요일</option></select>
          <label for="embed-cell-size">셀 크기 <output id="cell-size-output">11px</output></label><input id="embed-cell-size" name="cellSize" type="range" min="6" max="32" value="11">
          <label class="check-row"><input name="showLegend" type="checkbox" checked><span>강도 범례 표시</span></label>
        </form>
        <div class="embed-output">
          <div class="preview-card"><div class="browser-dots" aria-hidden="true"><i></i><i></i><i></i></div><div class="embed-preview"><img id="embed-preview-image" alt="생성된 활동 히트맵 미리보기"><p id="embed-preview-fallback">SVG 미리보기는 API 연결 후 표시됩니다.</p></div></div>
          <div class="code-tabs" role="tablist" aria-label="임베드 코드 형식"><button id="embed-tab-markdown" type="button" role="tab" aria-controls="embed-code-panel" aria-selected="true" tabindex="0" data-code-tab="markdown">Markdown</button><button id="embed-tab-html" type="button" role="tab" aria-controls="embed-code-panel" aria-selected="false" tabindex="-1" data-code-tab="html">HTML</button><button id="embed-tab-url" type="button" role="tab" aria-controls="embed-code-panel" aria-selected="false" tabindex="-1" data-code-tab="url">URL</button></div>
          <div class="code-box" id="embed-code-panel" role="tabpanel" aria-labelledby="embed-tab-markdown"><code id="embed-code"></code><button type="button" aria-label="코드 복사" data-action="copy-embed">${icon("copy")}</button></div>
          <p class="code-help">공개 프로필의 렌더 URL에는 인증 정보가 포함되지 않습니다.</p>
        </div>
      </div>
    </section>`;

  const form = content.querySelector<HTMLFormElement>("#embed-form");
  const subjectInput = content.querySelector<HTMLInputElement>("#embed-subject");
  const titleInput = content.querySelector<HTMLInputElement>("#embed-title");
  if (subjectInput) subjectInput.value = suggestedSubject;
  if (titleInput) titleInput.value = suggestedTitle;
  const code = content.querySelector<HTMLElement>("#embed-code");
  const preview = content.querySelector<HTMLImageElement>("#embed-preview-image");
  const fallback = content.querySelector<HTMLElement>("#embed-preview-fallback");
  const sizeOutput = content.querySelector<HTMLOutputElement>("#cell-size-output");
  let format: "markdown" | "html" | "url" = "markdown";

  const update = () => {
    if (!form) return;
    const output = embedValues(form);
    if (sizeOutput) sizeOutput.value = `${String(new FormData(form).get("cellSize") ?? "11")}px`;
    if (code) code.textContent = output[format];
    if (preview) {
      preview.src = output.url;
      preview.alt = String(new FormData(form).get("title") ?? "활동 히트맵");
    }
  };
  form?.addEventListener("input", update);
  preview?.addEventListener("load", () => {
    preview.hidden = false;
    if (fallback) fallback.hidden = true;
  });
  preview?.addEventListener("error", () => {
    preview.hidden = true;
    if (fallback) fallback.hidden = false;
  });
  const tabs = Array.from(content.querySelectorAll<HTMLButtonElement>("[data-code-tab]"));
  const selectTab = (button: HTMLButtonElement, focus = false) => {
      format = button.dataset.codeTab as typeof format;
      tabs.forEach((tab) => {
        const selected = tab === button;
        tab.setAttribute("aria-selected", String(selected));
        tab.tabIndex = selected ? 0 : -1;
      });
      const panel = content.querySelector<HTMLElement>("#embed-code-panel");
      if (panel) panel.setAttribute("aria-labelledby", button.id);
      update();
      if (focus) button.focus();
  };
  tabs.forEach((button) => {
    button.addEventListener("click", () => selectTab(button));
    button.addEventListener("keydown", (event) => {
      const current = tabs.indexOf(button);
      const nextIndex = event.key === "ArrowRight"
        ? (current + 1) % tabs.length
        : event.key === "ArrowLeft"
          ? (current - 1 + tabs.length) % tabs.length
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? tabs.length - 1
              : -1;
      if (nextIndex < 0) return;
      event.preventDefault();
      selectTab(tabs[nextIndex]!, true);
    });
  });
  update();
}

type BusyButtonState = {
  contents: DocumentFragment;
  minWidth: string;
};

const busyButtonStates = new WeakMap<HTMLButtonElement, BusyButtonState>();
const copyFeedbackTimers = new WeakMap<HTMLButtonElement, number>();

function showCopyFeedback(button: HTMLButtonElement, label: string): void {
  const previousTimer = copyFeedbackTimers.get(button);
  if (previousTimer !== undefined) window.clearTimeout(previousTimer);
  button.dataset.copied = "true";
  button.setAttribute("aria-label", label);
  button.innerHTML = icon("check");
  const timer = window.setTimeout(() => {
    delete button.dataset.copied;
    button.setAttribute("aria-label", button.dataset.action === "copy-secret" ? "Provider key 복사" : "코드 복사");
    button.innerHTML = icon("copy");
    copyFeedbackTimers.delete(button);
  }, 1600);
  copyFeedbackTimers.set(button, timer);
}

function setButtonBusy(button: HTMLButtonElement, busy: boolean, label = "처리 중…"): void {
  if (busy) {
    if (button.disabled || busyButtonStates.has(button)) return;
    const contents = document.createDocumentFragment();
    while (button.firstChild) contents.append(button.firstChild);
    busyButtonStates.set(button, {
      contents,
      minWidth: button.style.minWidth,
    });
    button.style.minWidth = `${Math.ceil(button.getBoundingClientRect().width)}px`;
    button.textContent = label;
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
  } else {
    const state = busyButtonStates.get(button);
    if (state) {
      button.replaceChildren(state.contents);
      button.style.minWidth = state.minWidth;
      busyButtonStates.delete(button);
    }
    button.disabled = false;
    button.removeAttribute("aria-busy");
  }
}

async function handleAction(button: HTMLButtonElement): Promise<void> {
  const action = button.dataset.action;
  if (!action) return;
  if (action === "retry-explore") return void renderExplore();
  if (action === "retry-connections") return void renderConnections();
  if (action === "retry-custom") return void renderCustom();
  if (action === "retry-auth") return void renderAuth();
  if (action === "retry-owner-subject") return void renderRoute();

  if (action === "sync-provider") {
    const id = button.dataset.id;
    if (!id) return;
    setButtonBusy(button, true, "동기화 중…");
    try {
      const job = await api.syncProvider(currentSubject, id);
      toast(`동기화 작업을 등록했습니다. (${job.status})`);
      await renderConnections();
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "disconnect-provider") {
    const id = button.dataset.id;
    if (!id || !confirm("이 연결을 해제할까요? 이미 가져온 공개 활동은 유지됩니다.")) return;
    setButtonBusy(button, true);
    try {
      await api.disconnectProvider(currentSubject, id);
      toast("Provider 연결을 해제했습니다.");
      await renderConnections();
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "passkey-signin" || action === "passkey-register") {
    setButtonBusy(button, true, action === "passkey-signin" ? "Passkey 확인 중…" : "등록 준비 중…");
    try {
      if (action === "passkey-signin") {
        const options = await api.beginPasskeyAuthentication();
        const credential = await getPasskey(options.publicKey);
        await api.finishPasskeyAuthentication(options.ceremonyId, credential);
        toast("Passkey로 로그인했습니다.");
        await renderAuth();
      } else {
        const options = await api.beginPasskeyRegistration("이 기기");
        const credential = await createPasskey(options.publicKey);
        await api.finishPasskeyRegistration(options.ceremonyId, credential, "이 기기");
        toast("이 기기에 Passkey를 등록했습니다.");
      }
    } catch (error) {
      toast(errorMessage(error), "error");
    } finally {
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "sign-out") {
    setButtonBusy(button, true, "로그아웃 중…");
    try {
      await api.signOut();
      ownedSubjects = [];
      clearOwnerSubject();
      toast("안전하게 로그아웃했습니다.");
      await renderAuth();
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "delete-subject") {
    if (
      !currentSubject ||
      !confirm(`@${currentSubject} 잔디밭과 소유 데이터를 삭제 요청할까요? 요청은 비동기로 처리되며 완료 후 복구할 수 없습니다.`)
    ) {
      return;
    }
    setButtonBusy(button, true, "삭제 요청 중…");
    try {
      await api.requestSubjectDeletion(currentSubject);
      setButtonBusy(button, false);
      button.textContent = "삭제 요청됨";
      button.disabled = true;
      button.setAttribute("aria-disabled", "true");
      toast("잔디밭 삭제 요청을 접수했습니다.");
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "select-custom") {
    const panel = content.querySelector<HTMLElement>("#ingest-panel");
    const form = content.querySelector<HTMLFormElement>("#ingest-form");
    const name = content.querySelector<HTMLElement>("#ingest-name");
    if (panel) panel.hidden = false;
    if (name) name.textContent = button.dataset.name ?? "활동";
    const idInput = form?.elements.namedItem("providerId") as HTMLInputElement | null;
    const actionInput = form?.elements.namedItem("action") as HTMLInputElement | null;
    if (idInput) idInput.value = button.dataset.id ?? "";
    if (actionInput) actionInput.value = button.dataset.customAction ?? "activity";
    panel?.scrollIntoView({ behavior: preferredScrollBehavior(), block: "center" });
    return;
  }

  if (action === "delete-custom") {
    const id = button.dataset.id;
    if (!id || !confirm("이 데이터 소스를 삭제할까요? 연결된 활동 기록도 더 이상 표시되지 않을 수 있습니다.")) return;
    try {
      await api.deleteCustomProvider(currentSubject, id);
      toast("데이터 소스를 삭제했습니다.");
      await renderCustom();
    } catch (error) {
      toast(errorMessage(error), "error");
    }
    return;
  }

  if (action === "toggle-custom") {
    const id = button.dataset.id;
    const status = button.dataset.status;
    if (!id || (status !== "active" && status !== "disabled")) return;
    setButtonBusy(button, true);
    try {
      await api.updateCustomProvider(currentSubject, id, {
        status: status === "active" ? "disabled" : "active",
      });
      toast(status === "active" ? "데이터 소스를 중지했습니다." : "데이터 소스를 다시 활성화했습니다.");
      await renderCustom();
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "rotate-custom-key") {
    const id = button.dataset.id;
    if (!id || !confirm("Provider key를 재발급하면 이전 key는 즉시 사용할 수 없습니다. 계속할까요?")) return;
    setButtonBusy(button, true, "재발급 중…");
    try {
      const result = await api.rotateCustomProviderKey(currentSubject, id);
      showSecret(result.ingestionKey);
      toast("새 Provider key를 발급했습니다.");
      setButtonBusy(button, false);
    } catch (error) {
      toast(errorMessage(error), "error");
      setButtonBusy(button, false);
    }
    return;
  }

  if (action === "copy-secret") {
    const value = content.querySelector<HTMLElement>("#secret-value")?.textContent ?? "";
    try {
      await navigator.clipboard.writeText(value);
      showCopyFeedback(button, "Provider key 복사 완료");
      toast("Provider key를 복사했습니다.");
    } catch {
      toast("복사하지 못했습니다. 값을 직접 선택해 주세요.", "error");
    }
    return;
  }

  if (action === "copy-embed") {
    const value = content.querySelector<HTMLElement>("#embed-code")?.textContent ?? "";
    try {
      await navigator.clipboard.writeText(value);
      showCopyFeedback(button, "코드 복사 완료");
      toast("클립보드에 복사했습니다.");
    } catch {
      toast("복사하지 못했습니다. 코드를 직접 선택해 주세요.", "error");
    }
  }
}

async function renderRoute(): Promise<void> {
  const route = currentRoute();
  setActiveNavigation(route);
  requestController?.abort();
  switch (route) {
    case "explore": {
      const hashSubject = decodeURIComponent(location.hash.split("/")[1] ?? "");
      await renderExplore(hashSubject || exploreSubject);
      break;
    }
    case "connections":
      await renderConnections();
      break;
    case "auth":
      await renderAuth();
      break;
    case "custom":
      await renderCustom();
      break;
    case "embed":
      renderEmbed();
      break;
  }
  window.scrollTo({ top: 0, behavior: "instant" });
}

content.addEventListener("click", (event) => {
  const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-action]");
  if (button) void handleAction(button);
});

window.addEventListener("hashchange", () => void renderRoute());
if (!location.hash) history.replaceState(null, "", "#explore");
void renderRoute();

export type AppBootstrappedTypes = {
  contracts: ActivityHeatmapResponseDto;
};
