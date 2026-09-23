import {
	__internal,
	type CacheConfig,
	type Disposable,
	type FetchPolicy,
	type GraphQLResponse,
	type GraphQLTaggedNode,
	getRequest,
	Observable,
	type OperationDescriptor,
	type OperationType,
	type ReaderFragment,
	type Subscription,
	type VariablesOf,
} from "relay-runtime";
import { observeFragment } from "relay-runtime/experimental.js";
import type { KeyType } from "relay-runtime/store/RelayStoreTypes.js";
import { type Accessor, createEffect, createMemo, createSignal } from "solid-js";
import { getQueryCache, type QueryCacheEntry } from "../queryCache";
import { useRelayEnvironment } from "../RelayEnvironment";
import { access, type MaybeAccessor } from "../utils/access";
import { createMemoOperationDescriptor } from "../utils/createMemoOperationDescriptor";
import { createDataStore, type DataStore } from "../utils/dataStore";
import { getQueryRef } from "../utils/getQueryRef";
import { cleanSnapshot } from "../utils/snapshot";

type QueryResult<T> =
	| {
			data: T;
			error: undefined;
			pending: false;
	  }
	| {
			data: undefined;
			error: unknown;
			pending: false;
	  }
	| {
			data: undefined;
			error: undefined;
			pending: boolean;
	  };

/**
 * Reads query data and subscribes to store updates for the current component.
 *
 * It fetches according to the provided fetch policy and returns a reactive
 * data store containing the query response.
 *
 * @param gqlQuery - GraphQL query document.
 * @param variables - Query variables or an accessor for reactive variables.
 * @param options.fetchPolicy - Query fetch policy.
 * @param options.fetchKey - Change this key to retry the same query and variables.
 * @param options.networkCacheConfig - Network cache configuration.
 * @param options.deferStream - Reserved for API compatibility; this browser build does not stream SSR.
 * @returns A `DataStore` containing the query data state.
 */
export function createLazyLoadQuery<TQuery extends OperationType>(
	gqlQuery: MaybeAccessor<GraphQLTaggedNode>,
	variables: MaybeAccessor<VariablesOf<TQuery>>,
	options?: {
		fetchPolicy?: MaybeAccessor<FetchPolicy | undefined>;
		fetchKey?: MaybeAccessor<string | number | null | undefined>;
		networkCacheConfig?: MaybeAccessor<CacheConfig | undefined>;
		deferStream?: boolean;
	},
): DataStore<TQuery["response"]> {
	const environment = useRelayEnvironment();
	const operation = createMemoOperationDescriptor(gqlQuery, variables, options?.networkCacheConfig);
	const fetchObservable = createMemo(() => {
		const op = operation();
		const env = environment();
		if (!op || !env) return;
		return __internal.fetchQuery(env, op);
	});

	return createLazyLoadQueryInternal({
		query: operation,
		fragment: () => getRequest(access(gqlQuery)).fragment,
		fetchObservable,
		fetchKey: () => access(options?.fetchKey),
		fetchPolicy: () => access(options?.fetchPolicy),
		deferStream: options?.deferStream,
	});
}

export function createLazyLoadQueryInternal<TQuery extends OperationType>(params: {
	query: Accessor<OperationDescriptor | undefined>;
	fragment: Accessor<ReaderFragment>;
	fetchObservable: Accessor<Observable<GraphQLResponse> | null | undefined>;
	fetchKey?: Accessor<string | number | null | undefined>;
	fetchPolicy?: Accessor<FetchPolicy | undefined>;
	/** Retained for the upstream API; browser rendering does not use SSR streaming. */
	deferStream?: boolean;
}): DataStore<TQuery["response"]> {
	const environment = useRelayEnvironment();
	const queryCache = createMemo(() => getQueryCache(environment()));

	const isLiveQuery = createMemo(
		() => params.query()?.request.node.params.metadata.live !== undefined,
	);
	const fetchPolicy = createMemo(
		() => params.fetchPolicy?.() ?? (isLiveQuery() ? "store-and-network" : "store-or-network"),
	);
	const cacheKey = createMemo(() => {
		const query = params.query();
		if (!query) return;

		return [fetchPolicy(), query.request.identifier, params.fetchKey?.()]
			.filter((v) => v != null)
			.join("-");
	});
	const cacheEntry = createMemo(() => {
		const operation = params.query();
		const key = cacheKey();
		if (!operation || !key) return;

		const cache = queryCache();
		const existing = cache.get(key);
		if (existing != null) return existing;

		const queryAvailablility = environment().check(operation);
		const queryStatus = queryAvailablility.status;
		const hasFullQuery = queryStatus === "available";

		const shouldFetch = (() => {
			switch (fetchPolicy()) {
				case "store-only":
					return false;
				case "store-or-network":
					return !hasFullQuery;
				case "store-and-network":
				case "network-only":
					return true;
			}
		})();

		let entry: QueryCacheEntry | undefined;
		if (shouldFetch) {
			const source = params.fetchObservable();
			const [fetchError, setFetchError] = createSignal<{ value: unknown } | undefined>();
			let subscription: Subscription | undefined;
			let retainCount = 0;
			let retention: Disposable | undefined;
			entry = {
				resource: {},
				error: () => fetchError()?.value,
				retain: (environment) => {
					retainCount++;
					if (retainCount === 1) {
						retention = environment.retain(operation);
						// Both ordinary and preloaded sources have already been wired to
						// Relay's normalization pipeline before reaching this accessor.
						subscription = source?.subscribe({
							error: (error: unknown) => setFetchError({ value: error }),
						});
					}
					return {
						dispose: () => {
							retainCount = Math.max(retainCount - 1, 0);
							if (retainCount === 0) {
								retention?.dispose();
								subscription?.unsubscribe();
								cache.delete(key);
							}
						},
					};
				},
			};
			cache.set(key, entry);
		}

		return entry;
	});

	createEffect(() => ({ entry: cacheEntry(), env: environment() }), ({ entry, env }) => {
		if (!entry) return;
		const retention = entry.retain(env);
		return () => retention.dispose();
	});

	const [result, setResult] = createDataStore<QueryResult<TQuery["response"]>>(
		{
			data: undefined,
			error: undefined,
			pending: false,
		},
		() => cacheEntry()?.resource,
	);

	createEffect(
		() => ({
			operation: params.query(),
			env: environment(),
			fragment: params.fragment(),
			fetchError: cacheEntry()?.error?.(),
		}),
		({ operation, env, fragment, fetchError }) => {
			setResult("data", undefined);
			setResult("error", undefined);
			setResult("pending", false);
			if (fetchError !== undefined) {
				setResult("error", fetchError);
				return;
			}
			if (!operation || !env) return;

			setResult("pending", true);
			const fragmentSubscription = observeFragment(
				env,
				fragment,
				getQueryRef(operation) as unknown as KeyType<TQuery["response"]>,
			).subscribe({
				next(state) {
					switch (state.state) {
						case "ok":
							setResult("error", undefined);
							setResult("pending", false);
							setResult("data", cleanSnapshot(state.value));
							break;
						case "error":
							setResult("data", undefined);
							setResult("error", state.error);
							setResult("pending", false);
							break;
						case "loading":
							setResult("data", undefined);
							setResult("error", undefined);
							setResult("pending", true);
							break;
					}
				},
			});
			return () => fragmentSubscription.unsubscribe();
		},
	);

	return result;
}
