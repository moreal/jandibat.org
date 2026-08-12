const MAGIC_LINK_TOKEN_KEYS = ["token", "magic_token"] as const;
const MAGIC_LINK_TOKEN_PATTERN = /^[A-Za-z0-9_-]{32,2048}$/;

function tokenFrom(params: URLSearchParams): string | undefined {
  for (const key of MAGIC_LINK_TOKEN_KEYS) {
    const token = params.get(key);
    if (token && MAGIC_LINK_TOKEN_PATTERN.test(token)) return token;
  }
  return undefined;
}

/**
 * Reads a magic-link token from the URL fragment without sending it to the
 * server. Query parameters are deliberately never accepted as credentials:
 * they can be observed by an ingress, CDN, access log, or referrer before the
 * browser application has an opportunity to sanitize the address.
 */
export function magicLinkTokenFromUrl(value: string): string | undefined {
  const url = new URL(value);
  const fragment = url.hash.slice(1);

  if (fragment) {
    const questionMark = fragment.indexOf("?");
    const fragmentParams = new URLSearchParams(
      questionMark >= 0 ? fragment.slice(questionMark + 1) : fragment,
    );
    const token = tokenFrom(fragmentParams);
    if (token) return token;
  }

  return undefined;
}

export function magicLinkLocationHasToken(value: string): boolean {
  const url = new URL(value);
  if (MAGIC_LINK_TOKEN_KEYS.some((key) => url.searchParams.has(key))) return true;
  const fragment = url.hash.slice(1);
  if (!fragment) return false;
  const questionMark = fragment.indexOf("?");
  const fragmentParams = new URLSearchParams(
    questionMark >= 0 ? fragment.slice(questionMark + 1) : fragment,
  );
  return MAGIC_LINK_TOKEN_KEYS.some((key) => fragmentParams.has(key));
}

/** Returns a same-origin location that contains neither token nor token value. */
export function magicLinkSafeLocation(value: string): string {
  const url = new URL(value);
  for (const key of MAGIC_LINK_TOKEN_KEYS) url.searchParams.delete(key);
  return `${url.pathname}${url.search}#auth`;
}

/** Builds the exact callback URL accepted by the backend redirect allowlist. */
export function magicLinkRedirectUrl(value: string): string {
  const current = new URL(value);
  const redirect = new URL(current.pathname, current.origin);
  redirect.searchParams.set("auth", "magic");
  redirect.hash = "auth";
  return redirect.href;
}
