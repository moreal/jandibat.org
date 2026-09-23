import { validateRuntimeApiBaseUrl } from "./authorization-url";

export function defaultApiBaseUrl(): string {
  const runtime = window.__JANDIBAT_CONFIG__?.apiBaseUrl;
  if (runtime !== undefined) {
    return validateRuntimeApiBaseUrl(runtime, import.meta.env.DEV);
  }
  const configured = import.meta.env.VITE_API_BASE_URL as string | undefined;
  if (configured) return validateRuntimeApiBaseUrl(configured, import.meta.env.DEV);
  return import.meta.env.DEV ? "http://localhost:8080" : "";
}
