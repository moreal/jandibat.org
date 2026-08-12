import { validateRuntimeApiBaseUrl } from "./api/authorization-url";
import "./styles.css";

type RuntimeConfigPayload = {
  apiBaseUrl?: unknown;
};

async function loadRuntimeConfig(): Promise<void> {
  const response = await fetch("/config.json", {
    cache: "no-store",
    credentials: "same-origin",
    headers: { Accept: "application/json" },
  });
  if (!response.ok) {
    throw new Error(`런타임 설정을 불러오지 못했습니다. (${response.status})`);
  }

  const payload = (await response.json()) as RuntimeConfigPayload;
  if (payload.apiBaseUrl !== undefined && typeof payload.apiBaseUrl !== "string") {
    throw new Error("런타임 API 기본 주소의 형식이 올바르지 않습니다.");
  }

  window.__JANDIBAT_CONFIG__ = payload.apiBaseUrl === undefined
    ? {}
    : {
        apiBaseUrl: validateRuntimeApiBaseUrl(
          payload.apiBaseUrl,
          import.meta.env.DEV,
        ),
      };
}

function showBootstrapError(error: unknown): void {
  const root = document.querySelector<HTMLDivElement>("#app");
  if (!root) return;
  const main = document.createElement("main");
  main.className = "bootstrap-error";
  main.setAttribute("role", "alert");
  const heading = document.createElement("h1");
  heading.textContent = "앱 설정을 확인해 주세요.";
  const message = document.createElement("p");
  message.textContent = error instanceof Error ? error.message : "알 수 없는 설정 오류입니다.";
  main.append(heading, message);
  root.replaceChildren(main);
}

void loadRuntimeConfig()
  .then(() => import("./main"))
  .catch(showBootstrapError);
