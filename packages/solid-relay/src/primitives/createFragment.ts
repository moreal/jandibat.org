import type {
	GraphQLResponse,
	GraphQLTaggedNode,
	Observer,
	Subscribable,
	Subscription,
} from "relay-runtime";
import { observeFragment } from "relay-runtime/experimental.js";
import type {
	ArrayKeyType,
	ArrayKeyTypeData,
	FragmentState,
	KeyType,
	KeyTypeData,
} from "relay-runtime/store/RelayStoreTypes.js";
import { type Accessor, createEffect, snapshot } from "solid-js";
import { useRelayEnvironment } from "../RelayEnvironment";
import { createDataStore, type DataStore } from "../utils/dataStore";
import { cleanSnapshot } from "../utils/snapshot";

type FragmentResult<T> =
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
 * Reads fragment data from a fragment key and subscribes to updates.
 *
 * Use this primitive when a parent query or fragment passes a generated
 * `...Fragment$key` reference into your component.
 *
 * @param fragment - GraphQL fragment document.
 * @param key - Fragment key accessor passed from a parent operation.
 * @param options.deferStream - Whether to defer the SSR stream until the data is resolved.
 * @returns A `DataStore` containing the fragment data state.
 */
export function createFragment<TKey extends KeyType>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<KeyTypeData<TKey>>;
export function createFragment<TKey extends KeyType>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey | null | undefined>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<KeyTypeData<TKey> | null | undefined>;
export function createFragment<TKey extends ArrayKeyType>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<ArrayKeyTypeData<TKey>>;
export function createFragment<TKey extends MaybeArray<ArrayKeyType>>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<MaybeArray<ArrayKeyTypeData<RequiredArray<TKey>>>>;
export function createFragment<TKey extends ArrayKeyType>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey | null | undefined>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<ArrayKeyTypeData<TKey> | null | undefined>;
export function createFragment<TKey extends MaybeArray<ArrayKeyType>>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey | null | undefined>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<MaybeArray<ArrayKeyTypeData<RequiredArray<TKey>>> | null | undefined>;
export function createFragment<TKey extends KeyType | ArrayKeyType>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey | null | undefined>,
	options?: {
		deferStream?: boolean;
	},
): DataStore<Data<TKey> | null | undefined> {
	return createFragmentInternal(fragment, key, undefined, options);
}

export type MaybeArray<T> =
	T extends ReadonlyArray<unknown> ? ReadonlyArray<T[number] | null | undefined> : never;
type RequiredArray<T> =
	T extends ReadonlyArray<(infer U) | null | undefined> ? ReadonlyArray<U> : never;

type Data<TKey extends KeyType | ArrayKeyType | MaybeArray<ArrayKeyType>> = TKey extends KeyType
	? KeyTypeData<TKey>
	: TKey extends MaybeArray<ArrayKeyType>
		? ArrayKeyTypeData<RequiredArray<TKey>>
		: TKey extends ArrayKeyType
			? ArrayKeyTypeData<TKey>
			: never;

export function createFragmentInternal<
	TKey extends KeyType | ArrayKeyType | MaybeArray<ArrayKeyType>,
>(
	fragment: GraphQLTaggedNode,
	key: Accessor<TKey | null | undefined>,
	options?: Accessor<{
		parentOperation: Subscribable<GraphQLResponse> | null | undefined;
	}>,
	_createResourceOptions?: {
		deferStream?: boolean;
	},
): DataStore<Data<TKey> | null | undefined> {
	const environment = useRelayEnvironment();
	// Give this fragment a stable store identity without Solid 1's Resource API.
	const identity = {};
	const [store, setResult] = createDataStore<FragmentResult<unknown>>(
		{
			data: undefined,
			error: undefined,
			pending: false,
		},
		() => identity,
	);
	createEffect(
		() => ({
			environment: environment(),
			key: snapshot(key()),
			parentOperation: options?.().parentOperation,
		}),
		({ environment, key, parentOperation }) => {
			let active = true;
			let fragmentSubscription: Subscription | undefined;
			let operationSubscription: Subscription | undefined;

			setResult("data", undefined);
			setResult("error", undefined);
			setResult("pending", Boolean(key));
			if (!key) return;

			const observe = () => {
				if (!active) return;
				fragmentSubscription = observeFragment(environment, fragment, key as KeyType).subscribe({
					next(res: FragmentState<unknown>) {
						if (!active) return;
						switch (res.state) {
							case "ok":
								setResult("data", cleanSnapshot(res.value));
								setResult("error", undefined);
								setResult("pending", false);
								break;
							case "error":
								setResult("data", undefined);
								setResult("error", res.error);
								setResult("pending", false);
								break;
							case "loading":
								setResult("data", undefined);
								setResult("error", undefined);
								setResult("pending", true);
								break;
						}
					},
				} satisfies Observer<FragmentState<unknown>>);
			};

			if (parentOperation) {
				operationSubscription = parentOperation.subscribe({
					complete: observe,
					error(error: Error) {
						if (!active) return;
						setResult("error", error);
						setResult("pending", false);
					},
				});
			} else {
				observe();
			}
			return () => {
				active = false;
				operationSubscription?.unsubscribe();
				fragmentSubscription?.unsubscribe();
			};
		},
	);

	return store as DataStore<Data<TKey> | null | undefined>;
}
