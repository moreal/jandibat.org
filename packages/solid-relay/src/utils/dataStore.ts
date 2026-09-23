import { type Accessor, createStore, untrack } from "solid-js";
import { useDataStores } from "../RelayEnvironment";

export type FieldSetter<T> = <K extends keyof T>(
	key: K,
	value: T[K] | ((previous: T[K]) => T[K]),
) => void;

/**
 * A reactive data store containing the result of a query or fragment.
 *
 * Call `()` to read the value.
 *
 * @param T - The type of the value stored in the data store.
 */
export type DataStore<T> =
	| {
			(): T;
			readonly latest: T;
			readonly error: undefined;
			readonly pending: false;
	  }
	| {
			(): undefined;
			readonly latest: undefined;
			readonly error: unknown;
			readonly pending: false;
	  }
	| {
			(): undefined;
			readonly latest: undefined;
			readonly error: undefined;
			readonly pending: true;
	  };

export const createDataStore = <
	T extends {
		readonly data: unknown;
		readonly error: unknown;
		readonly pending: boolean;
	},
>(
	init: T,
	identityAccessor?: Accessor<object | undefined>,
): [DataStore<T>, FieldSetter<T>] => {
	const [store, setStore] = createStableStore(
		init,
		untrack(() => identityAccessor?.()),
	);

	const readData = () => {
		const error = Reflect.get(store, "error");
		if (error) throw error;
		return Reflect.get(store, "data");
	};

	Object.defineProperties(readData, {
		latest: {
			get: () => Reflect.get(store, "data"),
		},
		error: {
			get: () => Reflect.get(store, "error"),
		},
		pending: {
			get: () => Reflect.get(store, "pending"),
		},
	});

	return [readData as unknown as DataStore<T>, setStore];
};

function createStableStore<
	T extends {
		readonly data: unknown;
		readonly error: unknown;
		readonly pending: boolean;
	},
>(init: T, identity: object | undefined): [T, FieldSetter<T>] {
	const stores = useDataStores();
	if (identity) {
		const existing = stores?.get(identity);
		if (existing) return existing as [T, FieldSetter<T>];
	}
	const store = createStore<T>(init as never);
	const setField: FieldSetter<T> = (key, value) => {
		store[1]((current) => ({
			...current,
			[key]: typeof value === "function" ? (value as (previous: T[typeof key]) => T[typeof key])(current[key]) : value,
		}));
	};
	const result: [T, FieldSetter<T>] = [store[0] as T, setField];
	if (identity) stores?.set(identity, result);
	return result;
}
