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
 * On a table's field, names the column it is stored in, which a to-one
 * relation's foreign key is itself (`@column("categoryId")`), in place of
 * the field's name in snake case (D85): a SQL identifier, quoted wherever
 * it must be. On a projection column, names the `alias.field` it reads
 * instead, or computes it with a SQL function the migrations own; a
 * function column has the field's declared type and nullability, which the
 * function must honor.
 */
export function column(_nameOrSource: string | ProjectionFunctionCall): PropertyDecorator {
  return noopPropertyDecorator;
}

/**
 * Names the table a DB type is stored in, in place of its name in snake
 * case (D85), as when the schema adopts a database that names its tables
 * otherwise: `@table("lineItem")`. A SQL identifier, quoted wherever it
 * must be.
 */
export function table(_name: string): ClassDecorator {
  return noopClassDecorator;
}

/**
 * An index's access method. `hnsw` and `ivfflat` index an `Embedding.Vector`
 * column with `dimensions`, and name the metric its nearest-neighbour
 * queries order by in `ops`: `vector_cosine_ops`, `vector_l2_ops` or
 * `vector_ip_ops`.
 */
export type IndexMethod = "btree" | "gin" | "gist" | "brin" | "hnsw" | "ivfflat";

export interface IndexOptions {
  readonly unique?: boolean;
  /** A purpose name: the index is `idx_<table>_<name>`, or `uq_` when unique. */
  readonly name?: string;
  /**
   * The access method. Unset picks one from the columns' types: GIST for
   * ltree and geometric types, GIN for JSONB, arrays and tsvector, else
   * BTREE.
   */
  readonly method?: IndexMethod;
  /**
   * The operator class every key is indexed with, such as `gin_trgm_ops`; on
   * a vector index, the metric's: `vector_cosine_ops`, `vector_l2_ops` or
   * `vector_ip_ops`.
   */
  readonly ops?: string;
  /** Makes the index partial: a raw SQL predicate over the table's columns, without WHERE. */
  readonly where?: string;
  /**
   * Indexes a raw SQL expression over the table's columns, such as
   * `lower(email)`, instead of the keys' columns. The keys name the fields it
   * reads, and the index's default name.
   */
  readonly expression?: string;
  /**
   * How a migration plan builds the index on a table that holds data: true,
   * the default, builds it concurrently, outside a transaction; false builds
   * it inside the step's transaction, which blocks writes while it builds.
   */
  readonly concurrently?: boolean;
  /**
   * An `hnsw` index's links per vector, pgvector's `m`: more find closer
   * neighbours and cost memory and build time. From 2 to 100; 16 unless set.
   */
  readonly m?: number;
  /**
   * An `hnsw` index's candidates per vector while it builds, pgvector's
   * `ef_construction`: at least twice `m`, up to 1000; 64 unless set.
   */
  readonly efConstruction?: number;
  /** An `ivfflat` index's lists, pgvector's `lists`: from 1 to 32768; 100 unless set. */
  readonly lists?: number;
}

/**
 * An index over the listed fields, unique when asked. The engine keeps an
 * engine type's index too, over its fields alone: method, ops, where,
 * expression and concurrently are for tables.
 */
export function index<T>(
  _keys: readonly (keyof T)[],
  _uniqueOrOptions?: boolean | IndexOptions,
): ClassDecorator {
  return noopClassDecorator;
}

/** The options of a piece of raw SQL a DB service runs (@rawSql). */
export interface RawSqlOptions {
  /** Names the piece in the migration model: a lowercase identifier, unique in the service. */
  readonly id: string;
  /**
   * The SQL: one or more statements separated by semicolons. A plan runs the
   * piece again when it changes, so each statement must be able to run again
   * over what it made: CREATE OR REPLACE, CREATE ... IF NOT EXISTS,
   * DROP ... IF EXISTS, REFRESH, COMMENT ON, GRANT, REVOKE,
   * INSERT ... ON CONFLICT, or a DO block.
   */
  readonly up: string;
  /** The SQL a plan runs when the piece is no longer declared, under the same rules. */
  readonly down?: string;
  /**
   * Where the piece runs: `"tables"`, after the tables and before the
   * indexes; `"indexes"`, the default, after every object the schema
   * declares; or another piece's id, right after that piece.
   */
  readonly after?: "tables" | "indexes" | (string & {});
  /** The dialects it runs on; unset is every dialect the service is built for. */
  readonly dialects?: readonly ("postgres" | "sqlite")[];
}

/**
 * Raw SQL the DB service runs after the objects its schema declares: a
 * function, a materialized view, an extension. create.sql includes it, and a
 * migration plan runs it when it is new or changes, and its down when it is
 * gone. The class it is written on is only where it lives.
 */
export function rawSql(_options: RawSqlOptions): ClassDecorator {
  return noopClassDecorator;
}

/** Keeps every version of a row: a history table, and writes fenced by version. */
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

/** The options of a queue: how often a failed message is retried, and when, and how long a done one stays. */
export type QueueOptions = {
  /** How many times a message whose handler fails is handled again before it is dead; five unless set. */
  readonly retries?: number;
  /**
   * How long a message that failed once waits before it is due again, a duration of whole seconds (`"30s"`, `"5m"`);
   * thirty seconds unless set. Each later failure doubles the wait, up to maxBackoff.
   */
  readonly backoff?: string;
  /** The longest wait a failure doubles to, a duration of whole seconds, the backoff or more; an hour unless set, or the backoff when that is longer. */
  readonly maxBackoff?: string;
  /** How long a claim holds a message unless its worker extends it, a duration of whole seconds; a minute unless set. */
  readonly lease?: string;
  /** How long a done message stays before a worker removes it, a duration of whole seconds (`"168h"`); `"0s"` removes it when it is done; a week unless set. Dead messages stay. */
  readonly retain?: string;
  /**
   * The message's field that keys it, a required scalar or enum field. It takes effect with `onDuplicate`
   * `"replace"` or `"skip"`, and with `serialize`.
   */
  readonly key?: string;
  /**
   * What an enqueue does when a ready message holds its key: `"append"` adds another, as a queue without a key
   * does; `"replace"` overwrites the ready one with the new message, due at once with no attempts; `"skip"`
   * keeps the ready one and adds none. `"append"` unless set.
   */
  readonly onDuplicate?: QueueOnDuplicate;
  /** Hold the queue to one claim per key at a time: no worker claims a message whose key another claim holds. */
  readonly serialize?: boolean;
};

/** What an enqueue does when a ready message holds its key. */
export type QueueOnDuplicate = "append" | "replace" | "skip";

/**
 * Declares a queue of the DB service whose class is its message
 * (docs/stack-model.md, section 8.8). The class's fields are the message's;
 * the database holds the queue in a table of its own, and an API's
 * `@worker` handles its messages:
 *
 * ```ts
 * @queue({ retries: 5, backoff: "30s" })
 * export abstract class OrderPlaced {
 *   orderId: Identity.UUID;
 * }
 * ```
 */
export function queue(_options?: QueueOptions): ClassDecorator {
  return noopClassDecorator;
}

/** A write to a table's rows that an `@enqueueOnChange` trigger enqueues on. */
export type EnqueueWrite = "insert" | "update" | "delete";

/**
 * A condition the row must meet for the trigger to enqueue: a field that is null, one that is not, or one that
 * equals a literal (an enum's member, a text, a number or a boolean). The row is the one written, or for a
 * delete the one deleted.
 */
export type EnqueueCondition<TTable = any> =
  | { readonly field: TableField<TTable>; readonly isNull: true }
  | { readonly field: TableField<TTable>; readonly notNull: true }
  | { readonly field: TableField<TTable>; readonly equals: string | number | boolean };

/** A field of the table, by its name: any name unless the table's class is given as a type argument. */
export type TableField<TTable> = keyof NoInfer<TTable> & string;

/** The options of `@enqueueOnChange`. */
export interface EnqueueOnChangeOptions<TMessage, TTable = any> {
  /** The writes that enqueue. */
  readonly on: readonly EnqueueWrite[];
  /** The table's fields whose change makes an update enqueue; every update enqueues unless set. */
  readonly fields?: readonly TableField<TTable>[];
  /** The conditions the row must meet, all of them. */
  readonly when?: readonly EnqueueCondition<TTable>[];
  /** Each field of the message the trigger sets, and the table's field whose value it takes. */
  readonly message: { readonly [K in keyof TMessage]?: TableField<TTable> };
}

/**
 * Enqueues a message of a queue of the DB service after a write to the table's rows, whatever wrote it: a
 * trigger of the database runs in the writer's transaction, so the message commits with the write. Under the
 * queue's `onDuplicate`, repeated writes keep one ready message per key:
 *
 * ```ts
 * @enqueueOnChange(EmbedLineItem, {
 *   on: ["insert", "update"],
 *   fields: ["category"],
 *   when: [{ field: "category", notNull: true }],
 *   message: { lineItemId: "id" },
 * })
 * export abstract class LineItem { ... }
 * ```
 */
export function enqueueOnChange<TMessage, TTable = any>(
  _queue: abstract new (...args: never[]) => TMessage,
  _options: EnqueueOnChangeOptions<TMessage, TTable>,
): ClassDecorator {
  return noopClassDecorator;
}

export interface VersionGraphOptions {
  /**
   * Prefixes the generated graph tables (snake_case) and types (PascalCase).
   * Defaults to the root's name.
   */
  readonly name?: string;
  /** Recorded on every commit of the graph. Defaults to 0. */
  readonly schemaEpoch?: number;
  /**
   * A commit this many commits past the nearest snapshot on its chain is
   * snapshotted, so reading its tree stops there. Positive; defaults to 64.
   */
  readonly snapshotEvery?: number;
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

/** The table's primary key; in the engine, a unique field. */
export const key: PropertyDecorator = noopPropertyDecorator;
/** A unique constraint on the column; in the engine, unique within a namespace, which lookup reads by. */
export const unique: PropertyDecorator = noopPropertyDecorator;
/** Joins the field into the table's trigram-indexed `search_text` column. */
export const searchField: PropertyDecorator = noopPropertyDecorator;
/** Stores the field as JSONB. */
export const jsonField: PropertyDecorator = noopPropertyDecorator;

/**
 * Marks a field that @source projections are expected to carry. A projection
 * omitting the field draws a verification warning instead of the usual
 * silent removal.
 */
export const sourceMustProject: PropertyDecorator = noopPropertyDecorator;
