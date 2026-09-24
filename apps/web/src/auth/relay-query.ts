import type { Environment, GraphQLTaggedNode, OperationType, Subscription, VariablesOf } from "relay-runtime";
import { fetchQuery } from "relay-runtime";

/** Resolve one normalized Relay operation; the caller owns cancellation. */
export function readRelayQuery<TQuery extends OperationType>(
  environment: Environment,
  operation: GraphQLTaggedNode,
  variables: VariablesOf<TQuery>,
  signal?: AbortSignal,
): Promise<TQuery["response"]> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    let subscription: Subscription | undefined;
    let settled = false;
    const abort = () => {
      if (settled) return;
      settled = true;
      subscription?.unsubscribe();
      reject(new DOMException("Aborted", "AbortError"));
    };
    signal?.addEventListener("abort", abort, { once: true });
    subscription = fetchQuery<TQuery>(environment, operation, variables, { fetchPolicy: "network-only" })
      .subscribe({
        next: (response) => {
          if (settled) return;
          settled = true;
          signal?.removeEventListener("abort", abort);
          resolve(response);
          queueMicrotask(() => subscription?.unsubscribe());
        },
        error: (error: unknown) => {
          if (settled) return;
          settled = true;
          signal?.removeEventListener("abort", abort);
          reject(error);
        },
      });
    if (signal?.aborted) abort();
  });
}
