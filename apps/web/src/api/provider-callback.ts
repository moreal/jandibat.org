export type ProviderCallbackResult = "connected";
const CALLBACK_PARAMETERS = ["status", "error", "error_description", "code", "state"] as const;

function isConnectionsRoute(url: URL): boolean {
  return url.hash.slice(1).split(/[/?]/)[0] === "connections";
}

/**
 * Reads only the completion marker emitted by the backend's exact allowlisted
 * OAuth callback. Provider-supplied error text is never reflected into the UI.
 */
export function providerCallbackResultFromUrl(
  value: string,
): ProviderCallbackResult | undefined {
  const url = new URL(value);
  if (!isConnectionsRoute(url)) return undefined;
  return url.searchParams.get("status") === "connected" ? "connected" : undefined;
}

export function providerCallbackLocationHasData(value: string): boolean {
  const url = new URL(value);
  return isConnectionsRoute(url) && CALLBACK_PARAMETERS.some((key) => url.searchParams.has(key));
}

/** Removes the one-shot OAuth completion marker while preserving app state. */
export function providerCallbackSafeLocation(value: string): string {
  const url = new URL(value);
  for (const parameter of CALLBACK_PARAMETERS) {
    url.searchParams.delete(parameter);
  }
  return `${url.pathname}${url.search}${url.hash || "#connections"}`;
}
