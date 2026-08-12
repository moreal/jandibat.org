const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

/**
 * Treat provider authorization URLs as untrusted API data before navigation.
 * Production accepts HTTPS only. Development may additionally use plain HTTP
 * on an exact loopback hostname so local OAuth fixtures remain usable.
 */
export function validateAuthorizationUrl(
  value: string,
  allowDevelopmentLoopbackHttp = false,
): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("OAuth 연결 주소가 올바른 절대 URL이 아닙니다.");
  }

  if (url.username || url.password) {
    throw new Error("OAuth 연결 주소에는 사용자 정보를 포함할 수 없습니다.");
  }

  if (url.protocol === "https:") return url.href;
  if (
    allowDevelopmentLoopbackHttp &&
    url.protocol === "http:" &&
    LOOPBACK_HOSTS.has(url.hostname)
  ) {
    return url.href;
  }

  throw new Error("안전하지 않은 OAuth 연결 주소를 차단했습니다.");
}

/**
 * Validate the API base supplied by deployment configuration. Query strings,
 * fragments, and credentials are intentionally excluded: this value is an API
 * origin with an optional path prefix, not an arbitrary navigation URL.
 */
export function validateRuntimeApiBaseUrl(
  value: string,
  allowDevelopmentLoopbackHttp = false,
): string {
  if (value === "") return "";
  if (value !== value.trim()) {
    throw new Error("API 기본 주소 앞뒤에 공백을 포함할 수 없습니다.");
  }

  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("API 기본 주소가 올바른 절대 URL이 아닙니다.");
  }

  if (url.username || url.password || url.search || url.hash) {
    throw new Error("API 기본 주소에는 사용자 정보, query 또는 fragment를 포함할 수 없습니다.");
  }

  const secure = url.protocol === "https:";
  const localDevelopment =
    allowDevelopmentLoopbackHttp &&
    url.protocol === "http:" &&
    LOOPBACK_HOSTS.has(url.hostname);
  if (!secure && !localDevelopment) {
    throw new Error("API 기본 주소는 HTTPS여야 합니다.");
  }

  return url.href.replace(/\/$/, "");
}
