export type QueryParam<T> = T & { readonly __queryParam: true };
export type EncryptedField<T> = T & { readonly __encrypted: true };

/** Which operation lists a reference's targets, and which of their fields it holds and shows (D61). */
export type RefersToConfig<Target> = {
  /** The operation that lists the targets: "listProducts" or "ProductCatalog.listProducts". The API's one operation that lists them when unsaid. */
  readonly options?: string;
  /** The target's field the value holds: "id" unsaid. */
  readonly value?: keyof Target & string;
  /** The target's field that names an item: its @display titleField unsaid. */
  readonly label?: keyof Target & string;
};

/**
 * A value that names an item of Target, as an operation of this API lists
 * it (D61): `RefersTo<Identity.UUID, ProductView>`, or with its config,
 * `RefersTo<Identity.UUID, ProductView, { options: "listProducts" }>`. A
 * list refers element by element: `RefersTo<Identity.UUID, ProductView>[]`.
 */
export type RefersTo<T, Target, C extends RefersToConfig<Target> = {}> = T & {
  readonly __refersTo: { readonly target: Target; readonly config: C };
};

/**
 * The operation's search text: an optional query parameter whose words each
 * item's searchable text contains (D62). An operation takes one.
 */
export type SearchQuery<T extends string = string> = (T & { readonly __queryParam: true; readonly __search: true }) | null;
