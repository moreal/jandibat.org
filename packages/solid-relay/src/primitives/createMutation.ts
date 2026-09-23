import {
	commitMutation,
	type Disposable,
	type GraphQLTaggedNode,
	type MutationConfig,
	type MutationParameters,
} from "relay-runtime";
import { type Accessor, createSignal } from "solid-js";
import { useRelayEnvironment } from "../RelayEnvironment";

/**
 * Creates a mutation commit function and an in-flight state accessor.
 *
 * The returned commit function forwards to Relay's `commitMutation` and
 * keeps `isMutationInFlight` updated while one or more commits are active.
 *
 * @param mutation - GraphQL mutation document.
 * @returns A tuple of `[commitMutation, isMutationInFlight]`.
 */
export function createMutation<TMutation extends MutationParameters>(
	mutation: GraphQLTaggedNode,
): [(config: Omit<MutationConfig<TMutation>, "mutation">) => Disposable, Accessor<boolean>] {
	const environment = useRelayEnvironment();
	const inFlightMutations = new Set<object>();
	const [isMutationInFlight, setIsMutationInFlight] = createSignal(false);

	const cleanup = (token: object) => {
		inFlightMutations.delete(token);
		setIsMutationInFlight(inFlightMutations.size > 0);
	};

	const commit = (config: Omit<MutationConfig<TMutation>, "mutation">) => {
		const token = {};
		inFlightMutations.add(token);
		setIsMutationInFlight(true);
		try {
			return commitMutation(environment(), {
				...config,
				mutation,
				onCompleted: (response, errors) => {
					cleanup(token);
					config.onCompleted?.(response, errors);
				},
				onError: (error) => {
					cleanup(token);
					config.onError?.(error);
				},
				onUnsubscribe: () => {
					cleanup(token);
					config.onUnsubscribe?.();
				},
			});
		} catch (error) {
			cleanup(token);
			throw error;
		}
	};

	return [commit, isMutationInFlight];
}
