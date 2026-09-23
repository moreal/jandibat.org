import type { GraphQLTaggedNode, MutationParameters } from "relay-runtime";
import { createMutation } from "solid-relay";
export { createRelayEphemeralMutation } from "./secret-mutation";

// Pages use this project-owned boundary, never solid-relay directly.
export {
  RelayEnvironmentProvider as RelayProvider,
  createLazyLoadQuery as createRelayQuery,
  createFragment as createRelayFragment,
  createPaginationFragment as createRelayPaginationFragment,
  createRefetchableFragment as createRelayRefetchableFragment,
} from "solid-relay";

export function createRelayMutation<TMutation extends MutationParameters>(mutation: GraphQLTaggedNode) {
  const [commit] = createMutation<TMutation>(mutation);
  return (variables: TMutation["variables"]): Promise<TMutation["response"]> =>
    new Promise((resolve, reject) => {
      commit({
        variables,
        onCompleted: (response, errors) => {
          if (errors?.length) {
            reject(new Error(errors.map((error) => error.message).join("; ")));
            return;
          }
          resolve(response);
        },
        onError: reject,
      });
    });
}
