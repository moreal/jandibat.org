import type { Disposable, IEnvironment } from "relay-runtime";
import type { Accessor } from "solid-js";

export type QueryCacheEntry = {
	resource: object;
	error?: Accessor<unknown>;
	retain: (environment: IEnvironment) => Disposable;
};

const caches = new WeakMap<IEnvironment, Map<string, QueryCacheEntry>>();

export function getQueryCache(environment: IEnvironment): Map<string, QueryCacheEntry> {
	let cache = caches.get(environment);
	if (!cache) {
		cache = new Map();
		caches.set(environment, cache);
	}
	return cache;
}
