import { __internal, type GraphQLTaggedNode, getRequest, type OperationType } from "relay-runtime";
import { createEffect, createMemo, createSignal } from "solid-js";
import invariant from "tiny-invariant";
import type { PreloadedQuery } from "../loadQuery";
import { useRelayEnvironment } from "../RelayEnvironment";
import { access, type MaybeAccessor } from "../utils/access";
import { createMemoOperationDescriptor } from "../utils/createMemoOperationDescriptor";
import type { DataStore } from "../utils/dataStore";
import { createLazyLoadQueryInternal } from "./createLazyLoadQuery";

type MaybePromise<T> = T | Promise<T>;

/**
 * Consumes a `PreloadedQuery` reference and returns reactive query data.
 *
 * When the provided reference changes, the previous one is disposed automatically.
 *
 * @param query - GraphQL query document matching the preloaded reference.
 * @param preloadedQuery - Query reference (or promise/accessor resolving to one).
 * @returns A `DataStore` containing the query data state.
 */
export function createPreloadedQuery<TQuery extends OperationType>(
	query: GraphQLTaggedNode,
	preloadedQuery: MaybeAccessor<MaybePromise<PreloadedQuery<TQuery>>>,
): DataStore<TQuery["response"]>;
export function createPreloadedQuery<TQuery extends OperationType>(
	query: GraphQLTaggedNode,
	preloadedQuery: MaybeAccessor<MaybePromise<PreloadedQuery<TQuery> | null | undefined>>,
): DataStore<TQuery["response"] | null | undefined>;
export function createPreloadedQuery<TQuery extends OperationType>(
	query: GraphQLTaggedNode,
	preloadedQuery: MaybeAccessor<MaybePromise<PreloadedQuery<TQuery> | null | undefined>>,
): DataStore<TQuery["response"] | null | undefined> {
	const environment = useRelayEnvironment();
	const [maybePreloaded, setMaybePreloaded] = createSignal<PreloadedQuery<TQuery> | null | undefined>();
	const [preloadError, setPreloadError] = createSignal<{ value: unknown } | undefined>();
	createEffect(
		() => access(preloadedQuery),
		(value) => {
			let active = true;
			let current: PreloadedQuery<TQuery> | null | undefined;
			setMaybePreloaded(undefined);
			setPreloadError(undefined);
			const accept = (resolved: PreloadedQuery<TQuery> | null | undefined) => {
				if (!active) {
					resolved?.controls?.value.dispose();
					return;
				}
				current = resolved;
				setMaybePreloaded(() => resolved);
			};
			if (value instanceof Promise) {
				void value.then(accept, (error: unknown) => {
					if (active) setPreloadError({ value: error });
				});
			} else {
				accept(value);
			}
			return () => {
				active = false;
				current?.controls?.value.dispose();
			};
		},
	);
	const resolvedPreloaded = createMemo(() => {
		const error = preloadError();
		if (error) throw error.value;
		return maybePreloaded();
	});
	const operation = createMemoOperationDescriptor(
		query,
		() => resolvedPreloaded()?.variables,
		() => resolvedPreloaded()?.networkCacheConfig ?? undefined,
	);

	return createLazyLoadQueryInternal({
		query: operation,
		fragment: () => getRequest(query).fragment,
		fetchKey: () => resolvedPreloaded()?.fetchKey,
		fetchPolicy: () => resolvedPreloaded()?.fetchPolicy,
		fetchObservable: () => {
			const preloaded = resolvedPreloaded();
			const op = operation();
			if (!preloaded || !op) return;

			invariant(
				preloaded.controls == null || !preloaded.controls?.value.isDisposed(),
				"usePreloadedQuery(): Expected preloadedQuery to not be disposed yet. " +
					"This is because disposing the query marks it for future garbage " +
					"collection, and as such query results may no longer be present in the Relay store.",
			);

			const fallback = __internal.fetchQuery(environment(), op);
			if (preloaded.controls?.value.source == null) return fallback;

			invariant(
				preloaded.controls == null || environment() === preloaded.controls.value.environment,
				"usePreloadedQuery(): usePreloadedQuery was passed a preloaded query " +
					"that was created with a different environment than the one that is currently in context.",
			);
			return preloaded.controls.value.source.ifEmpty(fallback);
		},
	});
}
