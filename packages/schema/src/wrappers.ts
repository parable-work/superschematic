/**
 * The rules `Validate<T, C>` adds, which every generated decoder checks:
 * `min` and `max` bound a number, `minLength` and `maxLength` a text,
 * `listMin` and `listMax` a list's length, and `pattern` is a regular
 * expression a text must match. `precision` and `scale` bound a
 * `Generic.Decimal`'s digits, in all and after the point, and make a DB
 * column `NUMERIC(p,s)`; `dimensions` is an `Embedding.Vector`'s length,
 * and makes a DB column `vector(n)`.
 */
export type ValidateConfig = {
  readonly min?: number;
  readonly max?: number;
  /** Largest multipart upload in bytes; only on a file-upload scalar field. */
  readonly uploadMaxBytes?: number;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly listMin?: number;
  readonly listMax?: number;
  readonly pattern?: string;
  readonly format?: string;
  /** The digits a `Generic.Decimal` holds in all; only on a `Generic.Decimal` field. */
  readonly precision?: number;
  /** The digits a `Generic.Decimal` holds after the point, 0 unless set; needs `precision`. */
  readonly scale?: number;
  /** The length of every `Embedding.Vector` value; only on an `Embedding.Vector` field. */
  readonly dimensions?: number;
};

/**
 * The phantom base of every wrapper below: it carries the wrapped type and
 * is never set. A wrapper is an intersection with T, from which a
 * conditional type cannot take T back; through the base it can
 * (`F extends Wrapped<infer T> ? T : F`), so a mapped type unwraps a field's
 * type, as `@superschematic/stack` types an environment's `env`.
 */
export type Wrapped<T> = { readonly __wrapped: T };

/**
 * The decoders fill in `V` when a payload leaves the field out. On a required date, time or timestamp field of a
 * DB table, `"now"` is the time of the insert: the column's DEFAULT is CURRENT_TIMESTAMP, CURRENT_DATE or
 * CURRENT_TIME, and an ORM create may leave the field unset. No other such column has a default.
 */
export type Default<T, V> = T & Wrapped<T> & { readonly __default: V };
export type PlatformDefault<T> = T & Wrapped<T> & { readonly __platformDefault: true };
/** The field may be absent or null; `name?: T` means the same. */
export type Nullable<T> = T | null;
/** `T` with the rules of `C` (ValidateConfig), which every generated decoder checks. */
export type Validate<T, C extends ValidateConfig> = T & Wrapped<T> & { readonly __validate: C };
/** A value kept out of logs: mask helpers clear it, and an `@envVars` loader marks it secret. */
export type Secret<T> = T & Wrapped<T> & { readonly __secret: true };
/**
 * An `@envVars` field the service reads again while it runs: the env loaders
 * hold it in a Live handle whose `apply` takes a new value with no restart.
 * A `Secret<T>` field cannot be one; a credential changes with a restart.
 */
export type Runtime<T> = T & Wrapped<T> & { readonly __runtime: true };
/**
 * A field that is present with the value null and nothing else, such as a
 * time that is not known and must say so. It takes no wrapper, default or
 * rule, and is no list element or Record value.
 */
export type RequiredNull = null;
/**
 * A discriminated-union field type (D149), declared as an exported alias:
 * `export type StepResult = Variant<"kind", { verify: VerifyResult; review: ReviewResult }>`.
 * A field of type `StepResult` holds the member its sibling field `kind`
 * names by value: a `VerifyResult` while `kind` is `"verify"`, a
 * `ReviewResult` while it is `"review"`, and nothing while `kind` holds
 * any other value or none. `By` names a string or enum field of every type
 * that holds such a field, and each key of `Arms` is a value of it. Every
 * generated validator checks the field by its sibling (the rule
 * `variant`), a Postgres or SQLite CHECK holds its column to the arms, and
 * each ORM and SDK types it as the union of the arms. A variant union is
 * one field's type: no list, map, argument or result is of it.
 */
export type Variant<By extends string, Arms extends { readonly [value: string]: object }> = Arms[keyof Arms] &
  Wrapped<Arms[keyof Arms]> & { readonly __variantBy: By };
