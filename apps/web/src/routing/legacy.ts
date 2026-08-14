import { magicLinkTokenFromUrl } from "../auth/magic-link.ts";

const legacyRoutes = new Set(["explore", "connections", "auth", "custom", "embed"]);
let pendingMagicLinkToken: string | undefined;

function appPath(path: string): string {
  const base = import.meta.env.BASE_URL.replace(/\/$/, "");
  return `${base}${path}` || "/";
}

function canonicalSegment(value: string): string {
  try {
    return encodeURIComponent(decodeURIComponent(value));
  } catch {
    return encodeURIComponent(value);
  }
}

export function legacyHashRoutePath(value: string): string | undefined {
  const url = new URL(value);
  const fragment = url.hash.slice(1);
  const routeText = fragment.split("?")[0] ?? "";
  const [route, ...segments] = routeText.split("/");
  if (!legacyRoutes.has(route)) return undefined;
  return route === "explore"
    ? segments.length > 0 && segments[0]
      ? `/explore/${canonicalSegment(segments[0])}`
      : "/"
    : `/${route}`;
}

/**
 * Converts links issued by the pre-Solid hash router before Solid Router reads
 * the initial URL. Magic-link credentials are captured in memory and removed
 * from the address in the same synchronous history operation.
 */
export function migrateLegacyHashLocation(): void {
  const url = new URL(location.href);
  const fragment = url.hash.slice(1);
  const routeText = fragment.split("?")[0] ?? "";
  const [route] = routeText.split("/");
  if (!legacyRoutes.has(route)) return;

  if (route === "auth") {
    pendingMagicLinkToken = magicLinkTokenFromUrl(url.href);
  }

  url.searchParams.delete("token");
  url.searchParams.delete("magic_token");
  const suffix = url.searchParams.size > 0 ? `?${url.searchParams.toString()}` : "";
  const path = legacyHashRoutePath(url.href);
  if (!path) return;
  history.replaceState(null, "", `${appPath(path)}${suffix}`);
}

export function takePendingMagicLinkToken(): string | undefined {
  const token = pendingMagicLinkToken;
  pendingMagicLinkToken = undefined;
  return token;
}

/** Callback URL retained for the backend's existing exact redirect allowlist. */
export function legacyCallbackUrl(route: "auth" | "connections"): string {
  const callback = new URL(import.meta.env.BASE_URL, location.origin);
  if (route === "auth") callback.searchParams.set("auth", "magic");
  callback.hash = route;
  return callback.href;
}
