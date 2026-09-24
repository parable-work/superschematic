const noopPropertyDecorator: PropertyDecorator = () => {};
const noopClassDecorator: ClassDecorator = () => {};

export interface VersionedPruneReference {
  /** Referencing table name (snake_case, as in DDL). */
  readonly table: string;
  /** Referencing table's column holding the versioned row key. */
  readonly keyColumn: string;
  /** Referencing table's column holding the pinned _version. */
  readonly versionColumn: string;
}

export interface VersionedOptions {
  readonly retentionDays?: number;
  readonly partitionBy?: "month";
  /**
   * Keep history rows whose (key, _version) pair is referenced by these tables
   * out of the generated prune function, so externally pinned row images
   * survive retention. Requires retentionDays.
   *
   * Takes one reference or a list of them: a versioned table can have several
   * independent readers of its historical rows, and each needs its own
   * exclusion. Declaring a second reference never displaces the first.
   *
   * Verification is syntactic only. It checks that this implies retentionDays,
   * that every identifier is snake_case (they are interpolated into DDL), and
   * that no reference is repeated. It cannot check the dangerous direction: a
   * table that declares retentionDays and has as-of readers but does NOT
   * declare the exclusion that protects them prunes their pinned images with
   * no diagnostic. Nothing in superschematic knows who reads a table's history, so that
   * one stays on the schema author and on repo-local tests.
   */
  readonly pruneKeepReferencedBy?: VersionedPruneReference | readonly VersionedPruneReference[];
}

/**
 * One `column = setting` binding of a @projection row rule. `column` is an
 * `alias.field` reference to a schema field (camelCase): the alias is `base`
 * for the projection's source table or a `@join` alias. superschematic
 * resolves it to the SQL column and the cast; the declaration never carries
 * SQL.
 *
 * Generated as `column = current_setting(setting)::<column type>`. An unset
 * setting makes `current_setting` raise, so the view fails closed.
 * `optional: true` reads `NULLIF(current_setting(setting, true), '')`
 * instead, so an unset or empty setting matches no row; use it inside
 * `anyOf`, where another binding can admit the row.
 */
export interface ProjectionSettingBinding {
  readonly column: string;
  readonly setting: string;
  readonly optional?: boolean;
}

/** A structured call to a SQL function the migrations own. */
export interface ProjectionFunctionCall {
  /** `schema.function`; never SQL text. */
  readonly function: string;
  /** `alias.field` references, passed in order. No literals or expressions. */
  readonly args: readonly string[];
}

/** A literal the column is compared to. */
export type ProjectionLiteral = string | number | boolean;

/**
 * One row rule of a @projection view, all ANDed:
 *
 * - a binding `{ column, setting, optional? }`;
 * - `{ anyOf: [binding, binding, ...] }`, of which one must hold;
 * - a literal rule `{ column, isNull: true }`, `{ column, notNull: true }` or
 *   `{ column, equals: literal }`, which reads no setting;
 * - a function rule `{ function, args, requiredSettings }`: the function must
 *   return true, and every listed setting must be set.
 */
export type ProjectionPredicate = (
  | ProjectionSettingBinding
  | { readonly anyOf: readonly ProjectionSettingBinding[] }
  | { readonly column: string; readonly isNull: true }
  | { readonly column: string; readonly notNull: true }
  | { readonly column: string; readonly equals: ProjectionLiteral }
  | (ProjectionFunctionCall & { readonly requiredSettings: readonly string[] })
) & {
  /**
   * Apply the rule only to rows where `when.column` equals `when.equals`;
   * every other row passes. Generated as
   * `(guard IS DISTINCT FROM 'literal' OR rule)`.
   */
  readonly when?: { readonly column: string; readonly equals: string };
};

/**
 * One ORDER BY term of a `collapse`: either `rank`, the column's literal
 * values in winning order (every other value ranks last), or a `direction`
 * with an optional `nulls` placement.
 */
export type ProjectionOrder =
  | { readonly column: string; readonly rank: readonly string[] }
  | {
      readonly column: string;
      readonly direction?: "asc" | "desc";
      readonly nulls?: "first" | "last";
    };

export interface ProjectionCollapse {
  /** The `alias.field` columns that identify one row (DISTINCT ON). */
  readonly by: readonly string[];
  /** Ranks the rows that share a key; the first wins. */
  readonly order?: readonly ProjectionOrder[];
}

export interface ProjectionOptions {
  /** The Postgres schema the view is created in; readers address it as `pool.name`. */
  readonly pool: string;
  /** The view's name inside the pool. */
  readonly name: string;
  /**
   * The 14-digit version stamp of the generated migration. An applied
   * migration is never rewritten: a changed declaration takes a new stamp.
   */
  readonly migration: string;
  /** The row rules, all ANDed, in order. */
  readonly where?: readonly ProjectionPredicate[];
  /**
   * Keep one row per key: the view is generated with `DISTINCT ON (by)` and
   * `ORDER BY by, order`, so the first row in that order wins.
   */
  readonly collapse?: ProjectionCollapse;
}

/**
 * Declares a projection view: a read-only relation over the source table
 * `TSource` (addressed as `base`) and any `@join` tables. The class fields
 * are the view's columns; each reads `base.<field>` unless `@column` says
 * otherwise. superschematic generates the Postgres view, its migration and
 * its Arrow schema from this one declaration. Only DB schemas declare
 * projections, and a projection is never a table or a language type.
 */
export function projection<TSource>(_options: ProjectionOptions): ClassDecorator {
  return noopClassDecorator;
}

/**
 * Adds a joined table to a @projection under `alias`. `on` maps
 * `alias.field` to `otherAlias.field` equalities (schema field names, not
 * SQL). `kind` defaults to "inner"; a "left" join makes every column read
 * through the alias nullable.
 */
export function join<TTable>(
  _alias: string,
  _on: Readonly<Record<string, string>>,
  _kind?: "inner" | "left",
): ClassDecorator {
  return noopClassDecorator;
}

/**
 * Names the `alias.field` a projection column reads, or computes it with a
 * SQL function the migrations own. A function column has the field's
 * declared type and nullability; the function must honor them.
 */
export function column(_source: string | ProjectionFunctionCall): PropertyDecorator {
  return noopPropertyDecorator;
}

export interface IndexOptions {
  readonly unique?: boolean;
  readonly name?: string;
}

export function index<T>(
  _keys: readonly (keyof T)[],
  _uniqueOrOptions?: boolean | IndexOptions,
): ClassDecorator {
  return noopClassDecorator;
}

export function versioned<TFunction extends Function>(target: TFunction): void;
export function versioned(_options?: VersionedOptions): ClassDecorator;
export function versioned<TFunction extends Function>(_arg?: VersionedOptions | TFunction): ClassDecorator | void {
  if (typeof _arg === "function") {
    return;
  }
  return noopClassDecorator;
}

export const key: PropertyDecorator = noopPropertyDecorator;
export const unique: PropertyDecorator = noopPropertyDecorator;
export const searchField: PropertyDecorator = noopPropertyDecorator;
export const jsonField: PropertyDecorator = noopPropertyDecorator;

/**
 * Marks a field that @source projections are expected to carry. A projection
 * omitting the field draws a verification warning instead of the usual
 * silent removal.
 */
export const sourceMustProject: PropertyDecorator = noopPropertyDecorator;
