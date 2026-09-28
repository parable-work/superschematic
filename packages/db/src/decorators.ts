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
   * Verification checks that this implies retentionDays, that every
   * identifier is snake_case (they are interpolated into DDL), and that no
   * reference is repeated. When the table is a DB type of the same schema, it
   * also checks that keyColumn has the versioned key's type and that
   * versionColumn is a Generic.Int64 column; a table outside the schema is
   * checked syntactically only. It cannot check the dangerous direction: a
   * table that declares retentionDays and has as-of readers but does NOT
   * declare the exclusion that protects them prunes their pinned images with
   * no diagnostic. Nothing in superschematic knows who reads a table's history, so that
   * one stays on the schema author and on repo-local tests.
   */
  readonly pruneKeepReferencedBy?: VersionedPruneReference | readonly VersionedPruneReference[];
  /**
   * Fields (schema field names) left out of every history image: the row an
   * insert or update records and a delete's tombstone. The history readers
   * return their zero value. The key, deletedAt and relations cannot be
   * excluded, and a @graphMember may exclude only fields whose conflict unit
   * is "excluded" and the audit fields.
   */
  readonly exclude?: readonly string[];
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

/**
 * Gives a DB table the _version column, the trigger that bumps it on every
 * update and the fenced writes UpdateOneIfVersion and DeleteOneIfVersion,
 * with no history. @versioned implies it, so a type carries one or the other.
 */
export const optimistic: ClassDecorator = noopClassDecorator;

/** A schema class, referenced as a value in a decorator argument. */
export type SchemaClass = abstract new (...args: never[]) => unknown;

export interface VersionGraphOptions {
  /**
   * Prefixes the generated graph tables (snake_case) and types (PascalCase).
   * Defaults to the root's name.
   */
  readonly name?: string;
  /** Recorded on every commit of the graph. Defaults to 0. */
  readonly schemaEpoch?: number;
}

/**
 * Containment: the field `key` holds the parent row's entityKey, and `of` is
 * a member type of the same graph, the member itself included.
 */
export interface GraphParent {
  readonly key: string;
  readonly of: SchemaClass;
}

export interface GraphMemberOptions {
  /** The graph's root: the @versionGraph type. */
  readonly graph: SchemaClass;
  readonly parent?: GraphParent;
  /** An Int64 field that orders siblings. */
  readonly order?: string;
  /** At most one live row of the kind per ref. */
  readonly singleton?: boolean;
}

/**
 * The merge unit of a graph member's field. `atomic` (the default) is the
 * whole field; `keyed` is each top-level key of a JSON object; `jsonSchema`
 * treats a JSON Schema object as units; `excluded` is not content.
 */
export type ConflictUnitStrategy = "atomic" | "keyed" | "jsonSchema" | "excluded";

/**
 * Marks the root of a version graph: the stable identity its member rows
 * and generated graph tables reference. The root has one UUID @key and is
 * never overlaid. superschematic generates the graph's ref, commit and
 * patch tables and their enums from the declarations.
 */
export function versionGraph<TFunction extends Function>(target: TFunction): void;
export function versionGraph(_options?: VersionGraphOptions): ClassDecorator;
export function versionGraph<TFunction extends Function>(_arg?: VersionGraphOptions | TFunction): ClassDecorator | void {
  if (typeof _arg === "function") {
    return;
  }
  return noopClassDecorator;
}

/**
 * Marks an entity kind of a version graph. The member must be @versioned,
 * with one UUID @key, exactly one relation to the root and no deletedAt.
 * superschematic adds its entityKey, ref and deletedOnRef fields.
 */
export function graphMember(_options: GraphMemberOptions): ClassDecorator {
  return noopClassDecorator;
}

/** Sets the merge unit of a graph member's field. */
export function conflictUnit(_strategy: ConflictUnitStrategy): PropertyDecorator {
  return noopPropertyDecorator;
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
