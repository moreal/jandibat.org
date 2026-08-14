import { validateRuntimeApiBaseUrl } from "./api/authorization-url";

type RuntimeConfigPayload = {
  apiBaseUrl?: unknown;
};

export async function loadRuntimeConfig(): Promise<void> {
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
