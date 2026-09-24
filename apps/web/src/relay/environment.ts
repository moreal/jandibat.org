import {
  Environment,
  Network,
  Observable,
  RecordSource,
  Store,
} from "relay-runtime";
import type { GraphQLResponse } from "relay-runtime";

export class GraphQLNetworkError extends Error {
  readonly code?: "UNAUTHENTICATED";

  constructor(message: string, code?: "UNAUTHENTICATED") {
    super(message);
    this.code = code;
  }
}

function unauthenticatedCode(payload: unknown): "UNAUTHENTICATED" | undefined {
  if (typeof payload !== "object" || payload === null || Array.isArray(payload) || !("errors" in payload) ||
    !Array.isArray(payload.errors) || payload.errors.length !== 1) return undefined;
  const error: unknown = payload.errors[0];
  if (typeof error !== "object" || error === null || Array.isArray(error) || !("extensions" in error)) return undefined;
  const extensions: unknown = error.extensions;
  return typeof extensions === "object" && extensions !== null && !Array.isArray(extensions) &&
    "code" in extensions && extensions.code === "UNAUTHENTICATED" ? "UNAUTHENTICATED" : undefined;
}

/** A fresh normalized store is scoped to the application bootstrap/session. */
export function createRelayEnvironment(apiBaseUrl: string): Environment {
  const endpoint = `${apiBaseUrl.replace(/\/$/, "")}/graphql`;
  return new Environment({
    network: Network.create((operation, variables) => Observable.create((sink) => {
      if (!operation.name || !operation.text) {
        sink.error(new Error("A named GraphQL operation is required."));
        return;
      }

      const controller = new AbortController();
      void fetch(endpoint, {
        method: "POST",
        credentials: "include",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          query: operation.text,
          operationName: operation.name,
          variables,
        }),
        signal: controller.signal,
      }).then(async (response) => {
        if (!response.ok) {
          throw new GraphQLNetworkError(`GraphQL request failed (${response.status}).`);
        }
        const payload: unknown = await response.json();
        if (typeof payload !== "object" || payload === null || Array.isArray(payload) ||
          ("errors" in payload && Array.isArray(payload.errors) && payload.errors.length > 0) ||
          !("data" in payload)) {
          throw new GraphQLNetworkError("GraphQL request failed.", unauthenticatedCode(payload));
        }
        sink.next(payload as GraphQLResponse);
        sink.complete();
      }).catch((error: unknown) => {
        if (controller.signal.aborted) return;
        sink.error(error instanceof GraphQLNetworkError
          ? error
          : new GraphQLNetworkError("GraphQL request failed."));
      });
      return () => controller.abort();
    })),
    store: new Store(new RecordSource()),
  });
}
