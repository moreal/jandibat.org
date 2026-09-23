import { onSettled } from "solid-js";
import { getRequest, type GraphQLTaggedNode, type MutationParameters } from "relay-runtime";

/**
 * Execute a credential-bearing mutation outside Relay's normalized store.
 * Relay can retain secrets in mutation input storage keys as well as responses.
 * The caller owns any returned secret and must clear its UI state on subject or
 * account changes and on unmount. Outstanding requests abort with this owner.
 */
export function createRelayEphemeralMutation<TMutation extends MutationParameters>(
  apiBaseUrl: string,
  mutation: GraphQLTaggedNode,
): (variables: TMutation["variables"]) => Promise<TMutation["response"]> {
  const request = getRequest(mutation);
  const { name, text } = request.params;
  if (request.params.operationKind !== "mutation" || !name || !text) {
    throw new Error("A named GraphQL mutation with generated text is required.");
  }

  const controllers = new Set<AbortController>();
  onSettled(() => () => {
    for (const controller of controllers) controller.abort();
    controllers.clear();
  });

  const endpoint = `${apiBaseUrl.replace(/\/$/, "")}/graphql`;
  return async (variables) => {
    const controller = new AbortController();
    controllers.add(controller);
    try {
      let response: Response;
      try {
        response = await fetch(endpoint, {
          method: "POST",
          credentials: "include",
          cache: "no-store",
          headers: { Accept: "application/json", "Content-Type": "application/json" },
          body: JSON.stringify({ query: text, operationName: name, variables }),
          signal: controller.signal,
        });
      } catch (error) {
        if (controller.signal.aborted || (error instanceof DOMException && error.name === "AbortError")) {
          throw new DOMException("Aborted", "AbortError");
        }
        throw new Error("GraphQL request failed.");
      }
      if (controller.signal.aborted) throw new DOMException("Aborted", "AbortError");
      if (!response.ok) throw new Error(`GraphQL request failed (${response.status}).`);

      let payload: unknown;
      try {
        payload = await response.json();
      } catch {
        throw new Error("GraphQL request failed.");
      }
      if (controller.signal.aborted) throw new DOMException("Aborted", "AbortError");
      if (typeof payload !== "object" || payload === null || Array.isArray(payload) ||
        ("errors" in payload && Array.isArray(payload.errors) && payload.errors.length > 0) ||
        !("data" in payload) || typeof payload.data !== "object" || payload.data === null || Array.isArray(payload.data)) {
        throw new Error("GraphQL request failed.");
      }
      return payload.data as TMutation["response"];
    } finally {
      controllers.delete(controller);
    }
  };
}
