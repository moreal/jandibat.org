import type {
  AuthMethodDto,
  CustomProviderDto,
  ProviderCatalogItemDto,
  ProviderConnectionDto,
} from "@jandibat/contracts";

function appendTextElement<K extends keyof HTMLElementTagNameMap>(
  document: Document,
  parent: Node,
  tag: K,
  text: string,
  className?: string,
): HTMLElementTagNameMap[K] {
  const element = document.createElement(tag);
  if (className) element.className = className;
  element.textContent = text;
  parent.appendChild(element);
  return element;
}

function appendIcon(document: Document, parent: Node, value: string): void {
  const icon = appendTextElement(document, parent, "span", value);
  icon.setAttribute("aria-hidden", "true");
}

function formatTime(value?: string | null): string {
  if (!value) return "아직 입력하지 않음";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat("ko-KR", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

function providerMonogram(provider: string): string {
  const labels: Record<string, string> = { github: "GH", gitlab: "GL", codeberg: "CB" };
  return labels[provider.toLowerCase()] ?? provider.slice(0, 2).toUpperCase();
}

function providerLogoClass(provider: string): string {
  return provider === "github" || provider === "gitlab" || provider === "codeberg"
    ? `provider-${provider}`
    : "provider-other";
}

function isAuthMethod(value: string): value is AuthMethodDto {
  return value === "oauth2" || value === "token" || value === "none";
}

export function privateConsentEligible(
  supportsPrivateData: boolean,
  authMethod: string,
): boolean {
  return supportsPrivateData && (authMethod === "oauth2" || authMethod === "token");
}

export function privateConsentValue(
  supportsPrivateData: boolean,
  authMethod: string,
  checked: boolean,
): boolean {
  return privateConsentEligible(supportsPrivateData, authMethod) && checked;
}

function appendProviderEmptyState(document: Document, container: HTMLElement): void {
  const empty = document.createElement("div");
  empty.className = "empty-list";
  empty.setAttribute("role", "status");
  const sprout = document.createElement("span");
  sprout.className = "empty-sprout";
  sprout.setAttribute("aria-hidden", "true");
  empty.append(sprout);
  appendTextElement(document, empty, "h2", "연결할 수 있는 Provider가 없어요.");
  appendTextElement(document, empty, "p", "잠시 후 다시 시도해 주세요.");
  const retry = appendTextElement(document, empty, "button", "다시 시도", "text-button");
  retry.type = "button";
  retry.dataset.action = "retry-connections";
  container.append(empty);
}

function appendPrivateConsent(
  document: Document,
  form: HTMLFormElement,
  index: number,
  visible: boolean,
): void {
  const consentId = `provider-private-consent-${index}`;
  const helpId = `${consentId}-help`;
  const label = document.createElement("label");
  label.className = "private-consent-field";
  label.hidden = !visible;
  const input = document.createElement("input");
  input.id = consentId;
  input.name = "includePrivate";
  input.type = "checkbox";
  input.value = "true";
  input.required = visible;
  input.setAttribute("aria-required", String(visible));
  input.setAttribute("aria-describedby", helpId);
  const copy = document.createElement("span");
  appendTextElement(document, copy, "strong", "비공개 활동 포함에 동의합니다.");
  const help = appendTextElement(
    document,
    copy,
    "small",
    "Provider의 최소 권한으로 비공개 저장소의 활동 날짜와 집계량을 가져옵니다. 비공개 원문과 저장소 이름은 공개 잔디밭에 표시하지 않습니다.",
  );
  help.id = helpId;
  label.append(input, copy);
  form.append(label);
}

function appendProviderForm(
  document: Document,
  parent: HTMLElement,
  provider: ProviderCatalogItemDto,
  authMethods: readonly AuthMethodDto[],
  index: number,
): void {
  const form = document.createElement("form");
  form.className = "provider-connect-form";
  form.dataset.providerConnect = provider.id;
  form.dataset.supportsPrivate = String(provider.supportsPrivateData);
  form.noValidate = true;

  const methodLabel = document.createElement("label");
  appendTextElement(document, methodLabel, "span", `${provider.name} 연결 방식`, "sr-only");
  const select = document.createElement("select");
  select.name = "authMethod";
  select.setAttribute("aria-label", `${provider.name} 연결 방식`);
  select.disabled = authMethods.length === 0;
  for (const method of authMethods) {
    const option = document.createElement("option");
    option.value = method;
    option.textContent = method === "oauth2" ? "OAuth" : method === "token" ? "Access token" : "공개 데이터";
    select.append(option);
  }
  methodLabel.append(select);
  form.append(methodLabel);

  const tokenLabel = document.createElement("label");
  tokenLabel.className = "provider-token-field";
  tokenLabel.hidden = true;
  appendTextElement(document, tokenLabel, "span", `${provider.name} access token`, "sr-only");
  const token = document.createElement("input");
  token.name = "token";
  token.type = "password";
  token.autocomplete = "off";
  token.maxLength = 4096;
  token.placeholder = "Access token";
  tokenLabel.append(token);
  form.append(tokenLabel);

  if (provider.supportsPrivateData) {
    appendPrivateConsent(
      document,
      form,
      index,
      privateConsentEligible(provider.supportsPrivateData, authMethods[0] ?? ""),
    );
  }

  const error = document.createElement("p");
  error.className = "inline-error";
  error.setAttribute("role", "alert");
  error.dataset.providerError = "";
  error.hidden = true;
  form.append(error);

  const submit = document.createElement("button");
  submit.className = "primary-button wide";
  submit.type = "submit";
  submit.disabled = authMethods.length === 0;
  submit.append(document.createTextNode(`${provider.name} 연결 `));
  appendIcon(document, submit, "→");
  form.append(submit);
  parent.append(form);
}

/**
 * Renders all API-provided provider fields through DOM text/attribute APIs.
 * No provider string is ever concatenated into parsed HTML.
 */
export function renderProviderCards(
  container: HTMLElement,
  catalog: readonly ProviderCatalogItemDto[],
  connections: readonly ProviderConnectionDto[],
): void {
  const document = container.ownerDocument;
  container.replaceChildren();
  const supported = catalog.filter((provider) => provider.category === "git-hosting");
  if (supported.length === 0) {
    appendProviderEmptyState(document, container);
    return;
  }
  const byProvider = new Map(connections.map((connection) => [connection.providerId, connection]));
  const statusLabel: Record<string, string> = {
    active: "연결됨",
    disabled: "비활성",
    revoked: "해제됨",
    pending: "연결 대기",
    error: "확인 필요",
    disconnected: "연결 안 됨",
  };

  supported.forEach((provider, index) => {
    const connection = byProvider.get(provider.id);
    const status = connection?.status ?? "disconnected";
    const safeStatus = Object.hasOwn(statusLabel, status) ? status : "error";
    const authMethods = provider.authMethods.filter(isAuthMethod);
    const article = document.createElement("article");
    article.className = "provider-card";
    if (connection) article.classList.add("is-connected");

    const top = document.createElement("div");
    top.className = "provider-card-top";
    const logo = appendTextElement(document, top, "span", providerMonogram(provider.id), "provider-logo");
    logo.classList.add(providerLogoClass(provider.id));
    const statusPill = document.createElement("span");
    statusPill.className = `status-pill status-${safeStatus}`;
    statusPill.append(document.createElement("i"), document.createTextNode(statusLabel[status] ?? "확인 필요"));
    top.append(statusPill);
    article.append(top);

    appendTextElement(document, article, "h2", provider.name);
    appendTextElement(
      document,
      article,
      "p",
      provider.supportsPrivateData
        ? "공개 활동을 기본으로 가져오며, 비공개 활동은 별도 동의 후 포함할 수 있습니다."
        : "공개 활동을 잔디밭에 모읍니다.",
    );
    if (connection) {
      appendTextElement(
        document,
        article,
        "p",
        connection.privateDataEnabled ? "비공개 활동 포함 동의됨" : "공개 활동만 수집",
        "connection-privacy",
      );
      if (connection.lastError) {
        appendTextElement(document, article, "p", connection.lastError, "inline-error");
      }
    }

    const metadata = document.createElement("div");
    metadata.className = "provider-meta";
    appendTextElement(
      document,
      metadata,
      "span",
      connection ? `${connection.authMethod} · 마지막 동기화` : "지원 연결 방식",
    );
    appendTextElement(
      document,
      metadata,
      "strong",
      connection ? formatTime(connection.lastSyncedAt) : authMethods.join(" · "),
    );
    article.append(metadata);

    const actions = document.createElement("div");
    actions.className = "card-actions";
    if (connection) {
      const sync = document.createElement("button");
      sync.className = "secondary-button";
      sync.type = "button";
      sync.dataset.action = "sync-provider";
      sync.dataset.id = connection.id;
      appendIcon(document, sync, "↻");
      sync.append(document.createTextNode(" 동기화"));
      const disconnect = document.createElement("button");
      disconnect.className = "icon-button danger";
      disconnect.type = "button";
      disconnect.dataset.action = "disconnect-provider";
      disconnect.dataset.id = connection.id;
      disconnect.setAttribute("aria-label", `${provider.name} 연결 해제`);
      appendIcon(document, disconnect, "×");
      actions.append(sync, disconnect);
    } else {
      appendProviderForm(document, actions, provider, authMethods, index);
    }
    article.append(actions);
    container.append(article);
  });
}

function appendCustomEmptyState(document: Document, container: HTMLElement): void {
  const empty = document.createElement("div");
  empty.className = "empty-list";
  const sprout = document.createElement("span");
  sprout.className = "empty-sprout";
  sprout.setAttribute("aria-hidden", "true");
  empty.append(sprout);
  appendTextElement(document, empty, "h3", "아직 만든 데이터 소스가 없어요.");
  appendTextElement(document, empty, "p", "오른쪽 양식에서 첫 번째 기록 종류를 만들어보세요.");
  container.append(empty);
}

function appendCustomActionButton(
  document: Document,
  parent: HTMLElement,
  text: string,
  action: string,
  id: string,
  className = "text-button",
): HTMLButtonElement {
  const button = appendTextElement(document, parent, "button", text, className);
  button.type = "button";
  button.dataset.action = action;
  button.dataset.id = id;
  return button;
}

/** Renders custom-provider runtime data without an HTML string boundary. */
export function renderCustomProviderCards(
  container: HTMLElement,
  providers: readonly CustomProviderDto[],
): void {
  const document = container.ownerDocument;
  container.replaceChildren();
  if (providers.length === 0) {
    appendCustomEmptyState(document, container);
    return;
  }
  for (const provider of providers) {
    const status = provider.status === "active" ? "active" : "disabled";
    const firstAction = provider.allowedActions[0] ?? "activity";
    const article = document.createElement("article");
    article.className = "custom-provider-card";
    const headingRow = document.createElement("div");
    const customIcon = appendTextElement(
      document,
      headingRow,
      "span",
      provider.name.slice(0, 1).toUpperCase(),
      "custom-icon",
    );
    customIcon.setAttribute("aria-hidden", "true");
    const headingCopy = document.createElement("div");
    const heading = document.createElement("h3");
    heading.append(document.createTextNode(`${provider.name} `));
    const pill = appendTextElement(
      document,
      heading,
      "small",
      status === "active" ? "사용 중" : "중지됨",
      `status-pill status-${status}`,
    );
    pill.dataset.status = status;
    headingCopy.append(heading);
    appendTextElement(
      document,
      headingCopy,
      "p",
      provider.description || `${provider.allowedActions.join(", ")} 활동`,
    );
    headingRow.append(headingCopy);
    article.append(headingRow);

    const endpoint = document.createElement("p");
    endpoint.className = "provider-endpoint";
    appendTextElement(document, endpoint, "span", `식별자 · 마지막 입력 ${formatTime(provider.lastIngestedAt)}`);
    appendTextElement(document, endpoint, "code", provider.key);
    article.append(endpoint);

    const actions = document.createElement("div");
    actions.className = "card-actions";
    const select = appendCustomActionButton(document, actions, "활동 입력", "select-custom", provider.id, "secondary-button");
    select.dataset.name = provider.name;
    select.dataset.customAction = firstAction;
    select.disabled = status === "disabled";
    const toggle = appendCustomActionButton(
      document,
      actions,
      status === "active" ? "중지" : "다시 사용",
      "toggle-custom",
      provider.id,
    );
    toggle.dataset.status = status;
    appendCustomActionButton(document, actions, "Key 재발급", "rotate-custom-key", provider.id);
    const remove = appendCustomActionButton(document, actions, "", "delete-custom", provider.id, "icon-button danger");
    remove.setAttribute("aria-label", `${provider.name} 삭제`);
    appendIcon(document, remove, "×");
    article.append(actions);
    container.append(article);
  }
}
