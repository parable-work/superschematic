import pg from 'pg';
import { currentCaller } from './caller.js';
import { FieldEncryptionError, newRowKey, parseFieldKeys, type FieldCell, type FieldKeyring, type FieldKeys } from './fieldcrypt.js';
import { columnKey, decodeRow, decodeValue, encodeParam, postgresTypeParsers, withTypeParsers, type RowField } from './pgcodec.js';
import { queriesOf, type QueryDefinition } from './queries.js';
import { parseArray } from './queue.js';
import { likePattern, searchTerms } from './search.js';
import { SecretValue, revealed } from './secret.js';
import { uuidFromDb, uuidToDb, uuidValue } from './uuid.js';

/*
The generated repositories' runtime (D79): a DB service's TypeScript types
package carries each table as data in its ./orm entry, its columns, what
each holds, and its statements on Postgres, which the generator writes from
the one source the Go ORM's come from (internal/generator/sqlutil). ormOf
runs them over a pg Pool, filling each statement as the Go ORM fills it, in
the same order, so the two languages run the same SQL: a test holds them to
it statement for statement.

A repository reads and writes a table's rows through the value codec
(./pgcodec): a row is the table's columns by member, a to-one relation's
foreign key as <field>Id, a UUID in its base62 wire form, a BIGINT a number,
a TIMESTAMPTZ a Date, a DATE its YYYY-MM-DD text, a JSON column its value.

The acting user stamps createdBy, updatedBy and deletedBy. An ORM opened
with no user of its own, as an API's Deps.orm is, acts as the caller of the
operation being run (D87): a generated router runs each implementation
within its caller (./caller), so an operation's writes record who called,
and a job's, a worker's and a manual route's record no one.
orm.withUser(id) or orm.as(principal) returns the ORM acting as that user,
or as no one for null, wherever it runs, and orm.asSystem() as the
database's system actor, which a job's or a worker's writes act as. A
write the Go ORM refuses without one, a create of a table with createdBy
and any delete of a soft-deletable table, throws NoUserError here too, and
so does an update of a table with updatedBy when the DB service sets
requireUpdatedBy. updatedAt is the time of each update; createdAt the
database's default.

A soft-deletable table, one with deletedAt, stamps deletedAt (and
deletedBy) on deleteOne and deleteMany, and leaves its deleted rows out of
every read unless the options include them; hardDeleteOne removes a row.

orm.transaction(fn) runs fn with repositories bound to one connection, and
query and exec for raw SQL on it, and rolls back when fn throws. Its query
takes a pg query config too, so enqueue(tx, queue, message) writes a queue's
message in the transaction. transaction(fn, { isolation, readOnly }) begins
at an isolation level, read only or not, and tx.transaction(fn) nests a
savepoint, which a throw rolls back to without ending the transaction
(D89). query(text, values, { decode: table }), on the ORM and on a
transaction, reads its rows as the table's repository reads them.

upsertOne(input, on, update) inserts a row, or updates the row that holds
its unique key, and returns the row; upsertOneResult returns it with what
the upsert did, inserted, updated or unchanged; and upsertMany(inputs, on,
fields) upserts many rows in one statement, a batch at a time, each row
that holds an input's key taking the input's value of each field, and
returns each input's row and status (D89). With onlyIfChanged, an upsert
leaves a row whose fields already hold the update's values as it was:
unstamped, its triggers unfired, and unchanged. A write that may take more
than one statement runs in a transaction of its own when none is open, and
so does nearest with a vector index's settings (efSearch, iterativeScan,
maxScanTuples, probes, maxProbes), which it sets for the transaction.
findMany(filter, { count: false }) reads the rows without counting them.

A table's Encrypted<T> columns (D85) hold envelopes, which a write seals
and a read opens with the DB service's field keys (createOrm(pool, {
fieldKeys })), bound to the table, the field and the row's key: a create
of such a table generates a UUID key the input leaves unset, a read holds
each value in a SecretValue, and reencrypt reseals the envelopes of an
older key under the current one. No filter holds such a column; updateMany
cannot set one. An upsert seals an insert's under the input's key, and
sets an update's on the row it updates by a second statement, sealed
under that row's key, when the value differs from what the row holds
(D89).

A read selects some members and includes relations (D90), as the Go ORM's
field selection and hydration read them: select lists the members a row
holds, and include loads a to-one relation's row, under the relation's
field, and a one-to-many relation's rows, a @hasMany field's or those of
another table's relation that points at the row, each with what to read of
it in turn. A relation's rows load in one statement for all of a read's
rows, the target's find with no count, and leave a soft-deleted target
out. A filter reaches through a to-one relation, its member the target's
filter, and anyOf ORs filters of the same table.

A projection (D90) is read through a repository of its own, which a
table's reads run over the view: getOne, findOne, findMany and findPage.

The @versioned history readers are the Go ORM's alone for now.
*/

/** A column of a table, as a types package's ./orm entry describes it: a RowField, and what the repositories write and filter. */
export interface TableColumn extends RowField {
  /** The schema field the column stores: a to-one relation's field for its foreign key, "customer" for customerId. */
  readonly field: string;
  /** The column qualified by its table, as a condition and an order name it. */
  readonly qualified: string;
  /** The primary key. */
  readonly key?: boolean;
  /** A column the database fills unless a write sets it (AutoGenerate). */
  readonly auto?: boolean;
  /** Internal metadata, such as _version, which no write sets. */
  readonly internal?: boolean;
  /** The audit field the column is. */
  readonly audit?: 'createdAt' | 'updatedAt' | 'createdBy' | 'updatedBy' | 'deletedAt' | 'deletedBy';
  /** The target type of a to-one relation, whose key the column holds. */
  readonly relation?: string;
  /** The schema's default, which a create that leaves the member unset writes. */
  readonly default?: unknown;
  /** The operations of the column's filter, in the order their conditions are written. */
  readonly filter?: readonly string[];
  /** A column findPage orders by. */
  readonly sortable?: boolean;
  /** An Encrypted<T> field's column, which holds its envelope (D85). */
  readonly encrypted?: boolean;
  /** A vector(n) column's n, the length of the vectors nearest takes (D84). */
  readonly dimensions?: number;
  /** The format, of the column or a value %s, an upsert only if changed compares its values as, when not the value itself: a point's text (D89). */
  readonly compare?: string;
}

/** A table's statements on Postgres, sqlutil.TableStatements: format strings of %s and %d alone. */
export interface TableStatements {
  readonly columns: string;
  readonly qualifiedColumns: string;
  /** One column of a selection, the column %s, unquoted, qualified as qualifiedColumns qualifies each (D90). */
  readonly selectColumn?: string;
  readonly getOne: string;
  /** The columns %s of the row whose key is $1 (D90). */
  readonly getOneFields?: string;
  readonly getMany: string;
  readonly live: string;
  readonly liveWhere: string;
  readonly liveCondition: string;
  readonly findOne: string;
  readonly findMany: string;
  readonly count: string;
  readonly page: string;
  readonly pageAfterKey: string;
  readonly pageAfter: string;
  readonly pageOrder: string;
  readonly pageTiebreak: string;
  readonly pageKeyExists: string;
  readonly insert: string;
  readonly insertMany: string;
  readonly update: string;
  readonly returning: string;
  readonly updateMany: string;
  readonly updatedAt: string;
  readonly updatedBy: string;
  readonly upsert: string;
  readonly softDelete: string;
  readonly softDeleteMany: string;
  readonly delete: string;
  readonly deleteMany: string;
  /** A table with an Encrypted<T> column's: the rows another key sealed, and their reseal (D85). */
  readonly stale?: string;
  readonly reseal?: string;
  /** The rows nearest a vector, on a table with a vector column (D84). */
  readonly nearest?: string;
}

/** The parts of statements written around a column, sqlutil.Clauses: format strings whose first %s is the column. */
export interface Clauses {
  readonly eq: string;
  readonly gt: string;
  readonly gte: string;
  readonly lt: string;
  readonly lte: string;
  readonly ilike: string;
  readonly in: string;
  readonly isNull: string;
  readonly isNotNull: string;
  readonly search: string;
  readonly exists: string;
  readonly notExists: string;
  readonly set: string;
  readonly setNull: string;
  readonly pageKey: string;
  readonly placeholder: string;
  readonly default: string;
  readonly where: string;
  readonly and: string;
  readonly orderBy: string;
  readonly orderTerm: string;
  readonly limit: string;
  readonly offset: string;
  /**
   * An upsert's (D89): the assignment of the value the row it would have
   * inserted holds, that value, the condition of an update only if changed
   * and of one that compares nothing, and NULL, the value an update that
   * sets a column NULL compares.
   */
  readonly setExcluded: string;
  readonly excluded: string;
  readonly changed: string;
  readonly never: string;
  readonly null: string;
  /** nearest's excludeIds: the column is none of the placeholders. */
  readonly notIn: string;
  /** What joins an anyOf's filters, and what wraps a filter's conditions and then the filters, %s (D90). */
  readonly or?: string;
  readonly group?: string;
}

/** A unique key an upsert is keyed by: its conflict target and the assignment of an update that sets nothing. */
export interface UniqueKeyDefinition {
  readonly conflict: string;
  readonly keep: string;
  /** The read of the rows that hold the keys of the tuples %s, which an upsert only if changed reads back (D89); absent for an expression key. */
  readonly rows?: string;
  /** A unique index over an expression, whose rows an upsert cannot match to its inputs by their columns. */
  readonly expression?: boolean;
}

/** A to-one relation of another table that points at a table: the filter member that probes for it. */
export interface ReferenceDefinition {
  /** The filter's member, referencedBy<Source><Field>. */
  readonly filter: string;
  /** The probe, the rows of the source that point at the row. */
  readonly probe: string;
  /** What leaves the source's deleted rows out of the probe, on a soft-deletable source. */
  readonly live?: string;
}

/** A table, as a types package's ./orm entry describes it. */
export interface TableDefinition {
  /** The table type. */
  readonly name: string;
  /** The table, quoted for SQL where it must be. */
  readonly table: string;
  /** The primary key's member. */
  readonly key: string;
  /** The columns, in the order every statement lists them. */
  readonly columns: readonly TableColumn[];
  readonly statements: TableStatements;
  /** A table with deletedAt, which a delete stamps. */
  readonly softDelete?: boolean;
  /** A @versioned table whose hard deletes set the history actor. */
  readonly historyActor?: boolean;
  /** The search column, qualified, of a table with search fields. */
  readonly search?: string;
  /** The unique keys by name, their fields joined by ",". */
  readonly uniqueKeys: Readonly<Record<string, UniqueKeyDefinition>>;
  readonly referencedBy?: readonly ReferenceDefinition[];
  /** The to-one relations a read includes and a filter reaches through, in column order (D90). */
  readonly relations?: readonly RelationDefinition[];
  /** The one-to-many relations a read includes (D90). */
  readonly many?: readonly ManyDefinition[];
}

/** A to-one relation of a table to another of its database (D90). */
export interface RelationDefinition {
  /** The relation field: the member a read includes the target's row under, and a filter reaches through. */
  readonly field: string;
  /** The row's member that holds the foreign key, <field>Id. */
  readonly column: string;
  /** The member of the ORM that holds the target's repository. */
  readonly target: string;
  /** A filter through the relation: the foreign key in the keys of the target's rows of the WHERE %s. */
  readonly probe: string;
}

/** A one-to-many relation of a table (D90): the rows of another table whose foreign key holds a row's key. */
export interface ManyDefinition {
  /** The member a read includes the rows under. */
  readonly name: string;
  /** The member of the ORM that holds the other table's repository. */
  readonly target: string;
  /** The other table's member that holds the foreign key, <field>Id. */
  readonly column: string;
}

/** How nearest orders rows, on a database with a vector column (D84). */
export interface NearestDefinition {
  /** Each metric's distance of the vector column %s from the parameter $%d. */
  readonly metrics: Readonly<Record<VectorMetric, string>>;
  /** The rows nearest reads unless told. */
  readonly defaultK: number;
  /** The settings nearest's options set for its transaction (D89): the statement that sets the settings %s, and each setting. */
  readonly settings?: { readonly statement: string; readonly each: readonly NearestSettingDefinition[] };
}

/** A setting of a vector index's scan an option of nearest sets, sqlutil.NearestSetting (D89). */
export interface NearestSettingDefinition {
  readonly option: 'efSearch' | 'iterativeScan' | 'maxScanTuples' | 'probes' | 'maxProbes';
  /** The call that sets it to the parameter $%d for the transaction. */
  readonly sql: string;
  /** The values of the option the setting takes; absent for an integer from min to max. A value outside them leaves the setting as it is. */
  readonly values?: readonly string[];
  readonly min?: number;
  readonly max?: number;
}

/** What a database's ./orm entry gives ormOf beside its tables. */
export interface OrmDefinition {
  readonly clauses: Clauses;
  readonly nearest?: NearestDefinition;
  /** The rows findMany and findPage read unless told. */
  readonly defaultLimit: number;
  /** The rows a one-to-many include reads of a relation for a read's rows, as the Go ORM's hydration does (D90). */
  readonly manyLimit?: number;
  /** The CTE a versioned table's hard delete sets the history actor through, and the statement that clears it. */
  readonly historyActorCTE: string;
  readonly clearHistoryActor: string;
  /** Who asSystem() acts as: the DB service's outputs.sql.systemActor; DEFAULT_SYSTEM_ACTOR unless set (D87). */
  readonly systemActor?: SystemActor;
  /** An update of a table with updatedBy needs an acting user, as a create of a table with createdBy does (D87). */
  readonly requireUpdatedBy?: boolean;
  /** The tables, by the member of the ORM that holds each one's repository. */
  readonly tables: Readonly<Record<string, TableDefinition>>;
  /** The declared queries, by name, which the ORM's queries member runs (D86). */
  readonly queries?: Readonly<Record<string, QueryDefinition>>;
}

/** A database's system actor, which asSystem() acts as (D87): a user's id and the name an app shows for it. */
export interface SystemActor {
  readonly id: string;
  readonly name: string;
}

/**
 * The system actor of a database whose DB service names none: the fixed id
 * the generated Go APIs export as SystemUserID. An audit field holds a
 * user's id with no foreign key, so it needs no row of the user table.
 */
export const DEFAULT_SYSTEM_ACTOR: SystemActor = { id: '00000000-0000-4000-8000-000000000000', name: 'system' };

/** The system actor asSystem() acts as, its id in the base62 wire form a row holds. */
export function systemActorOf(orm: OrmDefinition): SystemActor {
  const actor = orm.systemActor ?? DEFAULT_SYSTEM_ACTOR;
  return { id: uuidFromDb(actor.id), name: actor.name };
}

/** What a repository runs a statement with: a pg Pool, PoolClient or Client. */
export interface OrmQueryable {
  query(config: { text: string; values: unknown[]; types?: unknown }): Promise<{ rows: unknown[]; rowCount: number | null }>;
}

/** What ormOf needs of a pg Pool: its queries and a connection of its own for a transaction. */
export interface OrmPool extends OrmQueryable {
  connect(): Promise<OrmQueryable & { release(err?: Error | boolean): void }>;
}

/** A query config a transaction's query takes, as pg's does. */
export interface OrmQueryConfig {
  readonly text: string;
  readonly values?: readonly unknown[];
  readonly rowMode?: 'array';
  readonly types?: unknown;
}

/** What a transaction's query resolves to: the rows and how many rows the statement changed. */
export interface OrmQueryResult<R> {
  readonly rows: R[];
  readonly rowCount: number | null;
}

/** The error of a read or a write that names no row: getOne, updateOne, deleteOne of a key no live row holds. */
export class NotFoundError extends Error {
  constructor(message = 'record not found') {
    super(message);
    this.name = 'NotFoundError';
  }
}

/** The error of a write that stamps the acting user with none: orm.withUser(id) or orm.as(principal) gives one. */
export class NoUserError extends Error {
  constructor(
    message = 'no acting user: the write stamps who made it; act as one with orm.withUser(id), orm.as(principal) or orm.asSystem(), or write in an operation, whose caller acts'
  ) {
    super(message);
    this.name = 'NoUserError';
  }
}

/** findPage's error for an after key that holds no key, or whose row no longer exists in an order by another column. */
export class PageKeyError extends Error {
  constructor(message = 'the page key names no row of its sort') {
    super(message);
    this.name = 'PageKeyError';
  }
}

/**
 * The filters of a column, by its kind, as the Go ORM's (D79): each set
 * member is one condition, all of them ANDed. in with no values adds none;
 * between is [min, max], both included; isNull, on a column that may be
 * null, true for IS NULL and false for IS NOT NULL.
 */
export interface UuidFilter<T = string> {
  readonly eq?: T;
  readonly in?: readonly T[];
}
export interface StringFilter<T = string> {
  readonly eq?: T;
  readonly in?: readonly T[];
  /** Contains the text, in any case: ILIKE %text%. */
  readonly ilike?: string;
}
/**
 * A Generic.JSON column's filter: eq and in compare JSON values as JSONB
 * does, an object equal to another whatever the order of its keys and the
 * string "1" apart from the number 1. isNull, on a column that may be null,
 * tests SQL NULL, never the JSON null.
 */
export interface JsonFilter<T = unknown> {
  readonly eq?: T;
  readonly in?: readonly T[];
}
export interface NumberFilter<T = number> {
  readonly eq?: T;
  readonly gt?: T;
  readonly gte?: T;
  readonly lt?: T;
  readonly lte?: T;
  readonly between?: readonly [T, T];
  readonly in?: readonly T[];
}
export interface DateTimeFilter<T = Date> {
  readonly eq?: T;
  readonly gt?: T;
  readonly gte?: T;
  readonly lt?: T;
  readonly lte?: T;
  readonly between?: readonly [T, T];
  readonly in?: readonly T[];
}
export interface DateFilter<T = string> {
  readonly eq?: T;
  readonly gt?: T;
  readonly gte?: T;
  readonly lt?: T;
  readonly lte?: T;
  readonly between?: readonly [T, T];
  readonly in?: readonly T[];
}
/** A duration's filter, its values a duration's text; Postgres compares intervals with a day as 24 hours and a month as 30 days. */
export interface DurationFilter<T = string> {
  readonly eq?: T;
  readonly gt?: T;
  readonly gte?: T;
  readonly lt?: T;
  readonly lte?: T;
  readonly between?: readonly [T, T];
}
export interface BoolFilter {
  readonly eq?: boolean;
  readonly in?: readonly boolean[];
}
/** A location's filter, which tests presence alone: Postgres has no equality for a POINT. */
export type PointFilter = Record<never, never>;
/** What a column that may be null adds to its filter. */
export interface NullFilter {
  readonly isNull?: boolean;
}
/** A probe for the rows another table's relation points from: exists true keeps the rows some live row points at, false those none does. */
export interface ReferencedByFilter {
  readonly exists: boolean;
  /** Counts soft-deleted rows of the other table too. */
  readonly includeDeleted?: boolean;
}

/**
 * A relation a read includes (D90), as a table's ./orm entry types it: the
 * target's row, the target's own relations, and how many rows a row has of
 * it: one, one or none (a relation that may be null, or a soft-deletable
 * target, whose deleted rows a read leaves out), or many.
 */
export interface Related<Row, Relations, Kind extends 'one' | 'optional' | 'many'> {
  readonly row: Row;
  readonly relations: Relations;
  readonly kind: Kind;
}

/** What a read includes: each relation by its member, true for its rows, or what to read of them. */
export type IncludeOf<R> = {
  readonly [K in keyof R]?: R[K] extends Related<infer Row, infer Nested, 'one' | 'optional' | 'many'> ? boolean | ReadOptions<Row, Nested> : never;
};

/** What a read selects and includes (D90): the members its rows hold, every one unless set, and the relations it loads. */
export interface ReadOptions<Row = unknown, R = unknown> {
  /** The members each row holds; the foreign key of a relation the read includes is read too, and left out of the row. */
  readonly select?: readonly (keyof Row & string)[];
  /** The relations each row holds, loaded with one statement per relation for all of the read's rows. */
  readonly include?: IncludeOf<R>;
}

/** The row a read with options O resolves to: the members it selects, and the relations it includes. */
export type Loaded<Row, R, O> = Selected<Row, O extends { readonly select: infer S } ? S : undefined> &
  IncludedOf<R, O extends { readonly include: infer I } ? I : undefined>;

type Selected<Row, S> = S extends readonly (infer M)[] ? Pick<Row, M & keyof Row> : Row;

type IncludedOf<R, I> = I extends object
  ? { -readonly [K in keyof I & keyof R as I[K] extends false | null | undefined ? never : K]: LoadedRelated<R[K], I[K]> }
  : unknown;

type LoadedRelated<Rel, Spec> =
  Rel extends Related<infer Row, infer Nested, infer Kind>
    ? Kind extends 'many'
      ? Loaded<Row, Nested, Spec>[]
      : Kind extends 'optional'
        ? Loaded<Row, Nested, Spec> | null
        : Loaded<Row, Nested, Spec>
    : never;

/**
 * A read's options O, refused where they name an option T does not have,
 * or include a relation R does not have, at any depth: a read's options
 * are inferred as they are written, so the check an object literal takes
 * against its type is written out here.
 */
export type Checked<O, T, R> = O &
  { readonly [K in Exclude<keyof O, keyof T>]: never } &
  (O extends { readonly include: infer I } ? { readonly include: CheckedInclude<I, R> } : unknown);

type CheckedInclude<I, R> = {
  readonly [K in keyof I]: K extends keyof R
    ? R[K] extends Related<infer Row, infer Nested, 'one' | 'optional' | 'many'>
      ? I[K] extends object
        ? Checked<I[K], ReadOptions<Row, Nested>, Nested>
        : I[K]
      : never
    : never;
};

/** The order of findOne and findMany: a column's member, ascending or descending. */
export interface OrderBy<F extends string> {
  readonly field: F;
  readonly desc?: boolean;
}

/** The options of findOne and findMany. */
export interface FindOptions<F extends string, Row = unknown, R = unknown> extends ReadOptions<Row, R> {
  /** The order, by each column in turn. */
  readonly orderBy?: readonly OrderBy<F>[];
  /** At most this many rows, 100 unless set; findMany alone. */
  readonly limit?: number;
  /** Skips this many rows first; findMany alone. */
  readonly offset?: number;
  /** Reads soft-deleted rows too. */
  readonly includeDeleted?: boolean;
  /** false reads the rows without counting them first: findMany's total is then undefined (D89). */
  readonly count?: boolean;
}

/**
 * findMany's total under options O: undefined when O's count is false
 * (D89), a number when it counts, and either when O's count may be either.
 */
export type TotalOf<O> = O extends { readonly count?: infer C } ? (C extends false ? undefined : number) : number;

/** The order findPage reads rows in: a sort field, ascending or descending; ties break by the key in the same direction. */
export interface PageSortOf<S extends string> {
  readonly field: S;
  readonly desc?: boolean;
}

/** The options of findPage. */
export interface PageOptions<S extends string, K, Row = unknown, R = unknown> extends ReadOptions<Row, R> {
  /** The order; the key ascending unless set. */
  readonly sort?: PageSortOf<S>;
  /** The key of the last row of the page before; the first page without it. */
  readonly after?: K | null;
  /** The rows a page holds, 1 or more; 100 unless set. */
  readonly limit?: number;
  /** Reads soft-deleted rows too. */
  readonly includeDeleted?: boolean;
}

/** A page of rows in findPage's order, and the key of its last row when more follow. */
export interface RowPage<R, K> {
  readonly rows: R[];
  readonly next: K | null;
}

/**
 * What an upsert did with a row (D89): inserted it; updated the row that
 * held its key; or left that row as it was, since the update set no field,
 * or, with onlyIfChanged, since each field it set already held its value.
 */
export type UpsertStatus = 'inserted' | 'updated' | 'unchanged';

/** A row an upsert wrote or found, and what it did with it. */
export interface UpsertResult<Row> {
  readonly row: Row;
  readonly status: UpsertStatus;
}

/** The options of upsertOne and upsertOneResult. */
export interface UpsertOptions {
  /**
   * Updates the row that holds the key only when a field the update sets
   * differs from what the row holds, IS DISTINCT FROM; a row it leaves as
   * it was keeps its updatedAt and updatedBy, fires no trigger, and is
   * unchanged (D89). An encrypted field compares its plaintext. A key over
   * an expression cannot read such a row back, and is refused.
   */
  readonly onlyIfChanged?: boolean;
}

/** The options of upsertMany. */
export interface UpsertManyOptions extends UpsertOptions {
  /** The most rows one statement writes, 1 or more; as many as Postgres binds the parameters of unless set, fewer when the table has more columns. */
  readonly batch?: number;
}

/** The members upsertMany takes from each input into the row that holds its key: an update's, but updatedBy, which it stamps. */
export type UpsertField<Update> = Exclude<keyof Update & string, 'updatedBy'>;

/**
 * The reads of a table, and of a projection (D90). Each read's options
 * may select members and include relations, and its rows hold what they
 * name: Loaded<Row, Relations, O>.
 */
export interface ReadRepository<Row, Filter, OrderField extends string, SortField extends string, Key, PageKey, Relations = unknown> {
  /** The live row whose key is id; NotFoundError when there is none. */
  getOne<const O extends ReadOptions<Row, Relations> = {}>(id: Key, options?: Checked<O, ReadOptions<Row, Relations>, Relations>): Promise<Loaded<Row, Relations, O>>;
  /** The live rows whose keys are among ids, by key, in its wire form; a key no live row holds is absent. */
  getManyByIds(ids: readonly Key[]): Promise<Map<Key, Row>>;
  /** The first row filter finds, in the options' order, or null. */
  findOne<const O extends FindOptions<OrderField, Row, Relations> = {}>(
    filter?: Filter | null,
    options?: Checked<O, FindOptions<OrderField, Row, Relations>, Relations>
  ): Promise<Loaded<Row, Relations, O> | null>;
  /** The rows filter finds, in the options' order, a limit of them at a time, and how many it finds in all, unless count is false (D89). */
  findMany<const O extends FindOptions<OrderField, Row, Relations> = {}>(
    filter?: Filter | null,
    options?: Checked<O, FindOptions<OrderField, Row, Relations>, Relations>
  ): Promise<{ rows: Loaded<Row, Relations, O>[]; total: TotalOf<O> }>;
  /** A page of the rows filter finds, in the sort's order, after the key of the page before (D60, amended). */
  findPage<const O extends PageOptions<SortField, PageKey, Row, Relations> = {}>(
    filter?: Filter | null,
    options?: Checked<O, PageOptions<SortField, PageKey, Row, Relations>, Relations>
  ): Promise<RowPage<Loaded<Row, Relations, O>, PageKey>>;
}

/** The repository of a table. */
export interface Repository<
  Row,
  Create,
  Update,
  Filter,
  OrderField extends string,
  SortField extends string,
  UniqueKey extends readonly string[],
  Key,
  PageKey,
  Relations = unknown,
> extends ReadRepository<Row, Filter, OrderField, SortField, Key, PageKey, Relations> {
  /** Inserts a row; an optional member left unset takes its column's default. */
  createOne(input: Create): Promise<Row>;
  /** Inserts the rows in one statement, DEFAULT for each column a row leaves unset that another sets. */
  createMany(inputs: readonly Create[]): Promise<Row[]>;
  /** Sets the members update sets, null setting a column NULL, on the live row whose key is id; NotFoundError when there is none. */
  updateOne(id: Key, update: Update): Promise<Row>;
  /** Sets the members update sets on the live rows filter finds, and resolves to how many. */
  updateMany(filter: Filter | null, update: Update): Promise<number>;
  /**
   * Inserts input, or, when a row already holds its value of the unique key
   * on, applies update to that row instead, and returns the row (D79). An
   * update that sets no member leaves the row as it is. On a
   * soft-deletable table, a deleted row that holds the key is left as it is,
   * and upsertOne throws NotFoundError.
   */
  upsertOne(input: Create, on: UniqueKey, update: Update, options?: UpsertOptions): Promise<Row>;
  /** upsertOne, and what it did: inserted, updated or unchanged (D89). */
  upsertOneResult(input: Create, on: UniqueKey, update: Update, options?: UpsertOptions): Promise<UpsertResult<Row>>;
  /**
   * Inserts each input, or, when a row already holds its value of the
   * unique key on, sets each of update's fields of that row to the value
   * the input would have inserted, and returns each input's row and status,
   * in the inputs' order (D89). One statement writes a batch of rows; more
   * rows than a batch take a statement per batch, in one transaction. An
   * update of no field leaves each such row as it is; a deleted row of a
   * soft-deletable table that holds a key throws NotFoundError, as
   * upsertOne does. Two inputs of one batch that hold one key fail the
   * statement. A key over an expression is refused: the rows the statement
   * returns are matched to the inputs by the key's columns.
   */
  upsertMany(inputs: readonly Create[], on: UniqueKey, update: readonly UpsertField<Update>[], options?: UpsertManyOptions): Promise<UpsertResult<Row>[]>;
  /** Deletes the live row whose key is id, stamping it on a soft-deletable table; NotFoundError when there is none. */
  deleteOne(id: Key): Promise<void>;
  /** Deletes the rows filter finds, stamping them on a soft-deletable table, and resolves to how many. */
  deleteMany(filter: Filter | null): Promise<number>;
}

/** What the repository of a table with an Encrypted<T> column adds (D85). */
export interface EncryptedRepository {
  /**
   * Reseals under the current field key every envelope another key of the
   * keyring sealed, batch rows at a time, deleted rows among them, and
   * resolves to how many rows it resealed. Run it after a rotation puts a
   * new key first, before the old one leaves the secret.
   */
  reencrypt(options?: { readonly batch?: number }): Promise<number>;
}

/**
 * A metric nearest orders rows by (D84): cosine distance, 1 less the
 * cosine similarity; Euclidean (L2) distance; or the inner product,
 * largest first, whose distance is its negative. An hnsw or ivfflat index
 * with the metric's operator class, vector_cosine_ops, vector_l2_ops or
 * vector_ip_ops, serves it.
 */
export type VectorMetric = 'cosine' | 'l2' | 'ip';

/**
 * How a vector index's scan goes on past the candidates it reads first
 * (pgvector 0.8 and later, D89): off reads efSearch candidates, or an
 * ivfflat index's probes lists, and keeps those the filter keeps, so a
 * filter that keeps few rows may leave fewer than k; relaxed_order scans
 * on until it has k rows or has read maxScanTuples tuples (maxProbes
 * lists), in an order a little out of the distance's; strict_order, which
 * an hnsw index alone takes, scans on in the distance's order.
 */
export type IterativeScan = 'off' | 'relaxed_order' | 'strict_order';

/** The options of nearest. */
export interface NearestOptions<Filter, Key = string> {
  /** The most rows nearest reads, 1 or more; 10 unless set. */
  readonly k?: number;
  /** The metric; cosine unless set. */
  readonly metric?: VectorMetric;
  /** The rows nearest reads from, as findMany's filter keeps them. */
  readonly filter?: Filter | null;
  /** Reads soft-deleted rows too. */
  readonly includeDeleted?: boolean;
  /** Leaves out the rows of these keys: the row whose vector is asked for, say (D89). */
  readonly excludeIds?: readonly Key[];
  /**
   * The settings of the vector index's scan (D89), which nearest sets for
   * its transaction, in a transaction of its own when none is open, and
   * which in a transaction already open hold until it ends, as SET LOCAL's
   * do. efSearch is hnsw's ef_search, the candidates a scan reads, 1 to
   * 1000 (40 unless set); maxScanTuples the tuples an iterative scan reads
   * at most (20000); probes ivfflat's lists a scan reads, 1 to 32768 (1);
   * maxProbes the lists an iterative one reads at most. iterativeScan sets
   * both indexes' iterative scan, ivfflat's but to strict_order, which it
   * has not.
   */
  readonly efSearch?: number;
  readonly iterativeScan?: IterativeScan;
  readonly maxScanTuples?: number;
  readonly probes?: number;
  readonly maxProbes?: number;
}

/** A row nearest read, and its distance from the vector by the metric. */
export interface Neighbour<Row> {
  readonly row: Row;
  readonly distance: number;
}

/** What the repository of a table with a vector column adds (D84). */
export interface VectorRepository<Row, Filter, VectorField extends string, Key = string> {
  /**
   * The rows filter keeps whose field is nearest vector, by the metric,
   * nearest first, at most k of them, each with its distance. A vector
   * index serves the order and finds approximate neighbours; a filter that
   * keeps few rows may leave fewer than k. A row whose field is null has no
   * distance and is left out.
   */
  nearest(field: VectorField, vector: readonly number[], options?: NearestOptions<Filter, Key>): Promise<Neighbour<Row>[]>;
}

/** The repository of a soft-deletable table, which can remove a row too. */
export interface SoftDeleteRepository<
  Row,
  Create,
  Update,
  Filter,
  OrderField extends string,
  SortField extends string,
  UniqueKey extends readonly string[],
  Key,
  PageKey,
  Relations = unknown,
> extends Repository<Row, Create, Update, Filter, OrderField, SortField, UniqueKey, Key, PageKey, Relations> {
  /** Removes the row whose key is id, deleted or not; NotFoundError when there is none. */
  hardDeleteOne(id: Key): Promise<void>;
}

/** What createOrm and ormOf take beside the pool. */
export interface OrmOptions {
  /**
   * The acting user's id, as withUser takes one, or null for no one.
   * Unset, the ORM acts as the caller of the operation being run (D87).
   */
  readonly user?: string | null;
  /**
   * The keys of the database's Encrypted<T> fields (D85): the DB service's
   * key secret, as its text, the SecretValue an env config holds it in, or
   * a keyring. A read or a write of such a field without them throws
   * FieldEncryptionError.
   */
  readonly fieldKeys?: FieldKeys;
}

/** Who acts: a principal, as ctx.principal holds one. */
export interface Actor {
  readonly subject: string;
}

/** The isolation level a transaction runs at (D89); the server's default, read committed, unless set. */
export type IsolationLevel = 'read committed' | 'repeatable read' | 'serializable';

/** The options of an ORM's transaction (D89). */
export interface TransactionOptions {
  readonly isolation?: IsolationLevel;
  /** Begins it READ ONLY: a write in it fails. */
  readonly readOnly?: boolean;
}

/** The options of a raw query. */
export interface OrmQueryOptions {
  /**
   * Reads each row as a table's repository reads it, by its columns' names
   * (D89): a table's definition, which opens its Encrypted<T> columns too,
   * or the fields to read. A column of the definition the row does not
   * hold is left out, and one the definition does not name is dropped.
   */
  readonly decode?: TableDefinition | readonly RowField[];
}

/** The members an ORM and a transaction hold beside their repositories. */
export interface OrmMembers<R> {
  /**
   * The acting user's id, or undefined: for an ORM opened with no user of
   * its own, the caller of the operation being run (D87).
   */
  readonly actingUser: string | undefined;
  /** The ORM acting as the user id, which stamps createdBy, updatedBy and deletedBy, or as no one for null, wherever it runs. */
  withUser(id: string | null | undefined): this;
  /** The ORM acting as the principal's subject, or as no one without a principal, wherever it runs. */
  as(principal: Actor | null | undefined): this;
  /** The ORM acting as the database's system actor (systemActorOf), for a job's or a worker's writes (D87). */
  asSystem(): this;
  /** Runs fn in a transaction, with repositories bound to it, and commits; rolls back when fn throws. options sets its isolation level and makes it read only (D89). */
  transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>, options?: TransactionOptions): Promise<T>;
  /** Runs a statement: a text and its values, or a pg query config, as enqueue passes one; options.decode reads its rows as a table's repository does (D89). */
  query<Row = Record<string, unknown>>(text: string | OrmQueryConfig, values?: readonly unknown[], options?: OrmQueryOptions): Promise<OrmQueryResult<Row>>;
  /** Runs a statement and resolves to how many rows it changed. */
  exec(text: string, values?: readonly unknown[]): Promise<number>;
}

/** An ORM: its repositories, by table, and the members of OrmMembers. */
export type Orm<R> = R & OrmMembers<R>;

/** A transaction: its repositories, bound to it, and raw SQL on it. */
export type OrmTx<R> = R &
  Omit<OrmMembers<R>, 'transaction'> & {
    /**
     * Runs fn in a transaction nested in this one, a savepoint (D89): when
     * fn throws, its writes are rolled back and this transaction goes on,
     * as it may after a statement of fn's failed.
     */
    transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>): Promise<T>;
  };

/** The members a repository's table may not take, which an ORM holds itself. */
export const ORM_MEMBERS: readonly string[] = ['actingUser', 'withUser', 'as', 'asSystem', 'transaction', 'query', 'exec', 'queries'];

/**
 * Fills a format string's %s and %d, in order, with args, and %% with %, as
 * Go's fmt.Sprintf fills the statements sqlutil writes.
 */
export function fill(format: string, ...args: readonly unknown[]): string {
  let i = 0;
  return format.replace(/%[sd%]/gu, verb => {
    if (verb === '%%') return '%';
    if (i >= args.length) throw new Error(`fill: ${JSON.stringify(format)} needs more than ${args.length} arguments`);
    return String(args[i++]);
  });
}

const INTERVAL = 1186;
const INTERVAL_ARRAY = 1187;
const POINT = 600;
const POINT_ARRAY = 1017;

/**
 * The types each statement of a repository reads with: the pool's
 * (postgresTypeParsers), and an INTERVAL and a POINT as their text, which
 * the codec reads, whatever the pool was opened with.
 */
const ormTypes = withTypeParsers(pg.types, {
  ...postgresTypeParsers,
  [INTERVAL]: value => value,
  [INTERVAL_ARRAY]: parseArray,
  [POINT]: value => value,
  [POINT_ARRAY]: parseArray,
});

/** Runs a repository's statements: on a pool, or on the connection of a transaction. */
interface Runner {
  readonly queryable: OrmQueryable;
  readonly inTransaction: boolean;
  /** The pool a write that takes more than one statement opens a transaction on, outside one (D89). */
  readonly pool?: OrmPool;
}

async function run(runner: Runner, text: string, values: unknown[]): Promise<{ rows: Record<string, unknown>[]; rowCount: number }> {
  const result = await runner.queryable.query({ text, values, types: ormTypes });
  return { rows: result.rows as Record<string, unknown>[], rowCount: result.rowCount ?? 0 };
}

type Values = Record<string, unknown>;

/** The alias an upsert returns whether it inserted a row as, sqlutil.UpsertInserted (D89). */
const UPSERT_INSERTED = '_inserted';

/** The most parameters Postgres binds in one statement, and the most an upsert's update stamps beside its rows (sqlutil.MaxParameters, sqlutil.UpsertStamps). */
const MAX_PARAMETERS = 65535;
const UPSERT_STAMPS = 2;

/** Each isolation level's SQL, as sqlutil.BeginTransaction writes it (D89). */
const ISOLATION_LEVELS: Readonly<Record<IsolationLevel, string>> = {
  'read committed': 'READ COMMITTED',
  'repeatable read': 'REPEATABLE READ',
  serializable: 'SERIALIZABLE',
};

/** The statement that begins a transaction of the options, sqlutil.BeginTransaction's. */
function beginStatement(options: TransactionOptions = {}): string {
  let begin = 'BEGIN';
  if (options.isolation !== undefined) {
    const level = ISOLATION_LEVELS[options.isolation];
    if (level === undefined) throw new Error(`orm: a transaction's isolation is ${Object.keys(ISOLATION_LEVELS).join(', ')}, not ${JSON.stringify(options.isolation)}`);
    begin += ` ISOLATION LEVEL ${level}`;
  }
  if (options.readOnly) begin += ' READ ONLY';
  return begin;
}

/**
 * Runs fn on a connection of pool, in a transaction begin opens, and
 * commits; rolls back when fn throws. A connection whose transaction could
 * not be ended is not returned to the pool.
 */
async function inTransaction<T>(pool: OrmPool, begin: string, fn: (client: OrmQueryable) => Promise<T>): Promise<T> {
  const client = await pool.connect();
  let broken: Error | undefined;
  try {
    await client.query({ text: begin, values: [] });
    let out: T;
    try {
      out = await fn(client);
    } catch (error) {
      try {
        await client.query({ text: 'ROLLBACK', values: [] });
      } catch (rollbackError) {
        broken = rollbackError instanceof Error ? rollbackError : new Error(String(rollbackError));
      }
      throw error;
    }
    try {
      await client.query({ text: 'COMMIT', values: [] });
    } catch (error) {
      broken = error instanceof Error ? error : new Error(String(error));
      throw error;
    }
    return out;
  } finally {
    client.release(broken ?? false);
  }
}

/** The text of a unique key's values in a row, each a column's value in its wire form, as an upsert matches the rows it returns to its inputs (D89); undefined when a value is unset. */
function keyText(columns: readonly TableColumn[], values: readonly unknown[]): string | undefined {
  const parts: unknown[] = [];
  for (const [i, column] of columns.entries()) {
    const value = values[i];
    if (value === undefined) return undefined;
    const wire = decodeValue(value, column);
    parts.push(wire instanceof Date ? wire.toISOString() : wire);
  }
  return JSON.stringify(parts);
}

/** What a read selects and includes, as the runtime reads it: a member of include is true or the options of a relation's read. */
interface ReadSpec {
  readonly select?: readonly string[];
  readonly include?: Readonly<Record<string, unknown>>;
}

/** The columns a read selects, in order, and the members the caller selected, which every other column member leaves the rows for. */
interface Selection {
  readonly columns: readonly TableColumn[];
  readonly kept?: ReadonlySet<string>;
}

/**
 * Who an ORM acts as: a user's id, no one, or CALLER, the caller of the
 * operation being run, for an ORM opened with no user of its own (D87).
 */
const CALLER = Symbol('caller');
type Acting = string | undefined | typeof CALLER;

/**
 * The user acting resolves to now. A caller whose subject is no UUID, a
 * custom authenticator's, is no user an audit field can hold, so it binds
 * no one; as(principal) still refuses one.
 */
function actingUser(acting: Acting): string | undefined {
  if (acting !== CALLER) return acting;
  const subject = currentCaller();
  return subject !== undefined && uuidValue(subject) !== undefined ? subject : undefined;
}

/** The repository of one table over a runner, acting as whom acting resolves to. */
class TableRepository {
  private readonly encrypted: readonly TableColumn[];
  /** The repositories of the ORM's tables, by member, which an include reads through (D90). */
  peers: Readonly<Record<string, TableRepository>> = {};

  constructor(
    private readonly orm: OrmDefinition,
    readonly def: TableDefinition,
    private readonly runner: Runner,
    private readonly acting: Acting,
    private readonly keys: FieldKeyring | undefined
  ) {
    this.encrypted = def.columns.filter(c => c.encrypted);
  }

  /** The acting user, resolved when a statement is written. */
  private get user(): string | undefined {
    return actingUser(this.acting);
  }

  private peer(member: string): TableRepository {
    const peer = this.peers[member];
    if (!peer) throw new Error(`orm: ${this.def.name}'s relation reads ${member}, which the ORM has no repository of`);
    return peer;
  }

  /** Whether options select or include anything: the reads that do list their columns as the Go ORM's field selection does. */
  private static selects(options: ReadSpec): boolean {
    return options.select !== undefined || options.include !== undefined;
  }

  /**
   * The columns a read selects (D90), as the Go ORM's field selection
   * lists them: the members select names, or every one, then the foreign
   * key of each to-one relation it includes, and need, in column order;
   * then the key, last, when a one-to-many relation it includes maps its
   * rows by it, an encrypted column is opened under it, or a page's next
   * key holds it (page).
   */
  private selection(options: ReadSpec, need: readonly string[], page: boolean): Selection {
    const include = options.include ?? {};
    const relations = this.def.relations ?? [];
    const many = this.def.many ?? [];
    for (const name of Object.keys(include)) {
      if (!relations.some(r => r.field === name) && !many.some(m => m.name === name)) {
        const names = [...relations.map(r => r.field), ...many.map(m => m.name)];
        throw new Error(`orm: ${this.def.name} includes ${names.length ? names.join(', ') : 'no relation'}, not ${JSON.stringify(name)}`);
      }
    }
    const wanted = new Set<string>();
    if (options.select) {
      for (const member of options.select) {
        if (!this.def.columns.some(c => c.name === member)) throw new Error(`orm: ${this.def.name} selects its members, not ${JSON.stringify(member)}`);
        wanted.add(member);
      }
    } else {
      for (const column of this.def.columns) wanted.add(column.name);
    }
    for (const rel of relations) if (include[rel.field]) wanted.add(rel.column);
    for (const member of need) wanted.add(member);
    const columns = this.def.columns.filter(c => wanted.has(c.name));
    const key = this.keyColumn();
    const keyed = page || many.some(m => include[m.name]) || columns.some(c => c.encrypted);
    if (keyed && !wanted.has(key.name)) columns.push(key);
    if (columns.length === 0) throw new Error(`orm: a read of ${this.def.name} selects no member`);
    return { columns, kept: options.select ? new Set(options.select) : undefined };
  }

  /** The text of a selection: each column as selectColumn qualifies it. */
  private selectText(selection: Selection): string {
    const format = this.def.statements.selectColumn;
    if (format === undefined) throw new Error(`orm: ${this.def.name}'s definition predates select and include; regenerate its types package`);
    return selection.columns.map(c => fill(format, columnKey(c.column))).join(', ');
  }

  /** rows with each column member the caller did not select taken out: what a read reads only to include or open with. */
  private strip(rows: Values[], selection: Selection): void {
    const kept = selection.kept;
    if (!kept) return;
    for (const row of rows) {
      for (const column of selection.columns) if (!kept.has(column.name)) delete row[column.name];
    }
  }

  /**
   * The rows filter finds, as findMany reads them with no count, at most
   * limit, with the relations spec includes and need's members besides
   * what it selects: an include's read of a relation's rows (D90). The
   * caller strips them once it has mapped them.
   */
  private async related(filter: Values, spec: ReadSpec, need: readonly string[], limit: number): Promise<{ rows: Values[]; selection: Selection }> {
    const selection = this.selection(spec, need, false);
    const args: unknown[] = [];
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, 0)), false);
    const s = this.def.statements;
    const { rows } = await run(this.runner, fill(s.findMany, this.selectText(selection), where, '', fill(this.clauses.limit, limit)), args);
    const decoded = rows.map(row => this.decode(row));
    await this.hydrate(decoded, spec.include);
    return { rows: decoded, selection };
  }

  /**
   * Loads the relations include names into rows (D90), as the Go ORM
   * hydrates them: each to-one relation in column order, with one read of
   * the rows the rows' foreign keys name, then each one-to-many relation,
   * with one read of the rows whose foreign key holds one of the rows'
   * keys. A key no live row holds leaves the relation null; a row with no
   * related rows holds an empty list.
   */
  private async hydrate(rows: Values[], include: Readonly<Record<string, unknown>> | undefined): Promise<void> {
    if (!include) return;
    const specOf = (value: unknown): ReadSpec => (value === true ? {} : (value as ReadSpec));
    for (const rel of this.def.relations ?? []) {
      if (!include[rel.field]) continue;
      const ids = distinct(rows.map(row => row[rel.column]));
      const found = new Map<unknown, Values>();
      if (ids.length > 0) {
        const target = this.peer(rel.target);
        const key = target.def.key;
        const read = await target.related({ [key]: { in: ids } }, specOf(include[rel.field]), [key], ids.length);
        for (const row of read.rows) found.set(row[key], row);
        target.strip(read.rows, read.selection);
      }
      for (const row of rows) {
        const id = row[rel.column];
        row[rel.field] = id === null || id === undefined ? null : (found.get(id) ?? null);
      }
    }
    for (const many of this.def.many ?? []) {
      if (!include[many.name]) continue;
      const keys = distinct(rows.map(row => row[this.def.key]));
      const groups = new Map<unknown, Values[]>();
      if (keys.length > 0) {
        const child = this.peer(many.target);
        const read = await child.related({ [many.column]: { in: keys } }, specOf(include[many.name]), [many.column], this.orm.manyLimit ?? 1000000);
        for (const row of read.rows) {
          const group = groups.get(row[many.column]);
          if (group) group.push(row);
          else groups.set(row[many.column], [row]);
        }
        child.strip(read.rows, read.selection);
      }
      for (const row of rows) row[many.name] = groups.get(row[this.def.key]) ?? [];
    }
  }

  private get clauses(): Clauses {
    return this.orm.clauses;
  }

  private column(member: string): TableColumn {
    const column = this.def.columns.find(c => c.name === member);
    if (!column) throw new Error(`orm: ${this.def.name} has no column ${member}`);
    return column;
  }

  private keyColumn(): TableColumn {
    return this.column(this.def.key);
  }

  private audit(name: NonNullable<TableColumn['audit']>): TableColumn | undefined {
    return this.def.columns.find(c => c.audit === name);
  }

  private decode(row: Record<string, unknown>): Values {
    return this.open(decodeRow<Values>(row, this.def.columns));
  }

  /** A row a raw query read, as the repository reads one: its columns decoded and its encrypted ones opened (D89). */
  read(row: Record<string, unknown>): Values {
    return this.decode(row);
  }

  /**
   * Runs fn with the repository in a transaction: this one in the
   * transaction it runs in, else one bound to a transaction of its own on
   * the pool, which fn's return commits and its throw rolls back (D89).
   */
  private async atomically<T>(fn: (repo: TableRepository) => Promise<T>): Promise<T> {
    if (this.runner.inTransaction) return fn(this);
    const pool = this.runner.pool;
    if (!pool) throw new Error(`orm: ${this.def.name} has no pool to open a transaction on`);
    return inTransaction(pool, 'BEGIN', client =>
      fn(new TableRepository(this.orm, this.def, { queryable: client, inTransaction: true }, this.acting, this.keys))
    );
  }

  /** The cell of column in the row whose key is key, in its wire or database form. */
  private cell(column: TableColumn, key: unknown): FieldCell {
    const keyColumn = this.keyColumn();
    return { table: this.def.name, field: column.field, row: keyColumn.uuid ? uuidToDb(key) : String(key) };
  }

  private ring(column: TableColumn): FieldKeyring {
    if (!this.keys) throw new FieldEncryptionError('no-keys', { table: this.def.name, field: column.field });
    return this.keys;
  }

  /** row with each encrypted member opened into a SecretValue, under the row's key. */
  private open(row: Values): Values {
    for (const column of this.encrypted) {
      const value = row[column.name];
      if (value === null || value === undefined) continue;
      row[column.name] = new SecretValue(this.ring(column).open(this.cell(column, row[this.def.key]), String(value)));
    }
    return row;
  }

  /** The envelope of an encrypted member's value, a string or a SecretValue of one, in the row whose key is key. */
  private seal(column: TableColumn, value: unknown, key: unknown, what: string): string {
    const plaintext = revealed(value);
    if (typeof plaintext !== 'string') throw new Error(`orm: ${what} ${this.def.name}: ${column.name} is Encrypted<string>, and takes a string or a SecretValue of one`);
    if (key === undefined || key === null || key === '') {
      throw new Error(`orm: ${what} ${this.def.name}: ${this.def.key} is required, since ${column.name}'s envelope is bound to it`);
    }
    return this.ring(column).seal(this.cell(column, key), plaintext);
  }

  /**
   * input with its key, which a table with an encrypted column generates
   * when input leaves a UUID key unset (D85), and each encrypted member
   * sealed under it; input itself for any other table.
   */
  private sealed(input: Values, what: string): Values {
    if (this.encrypted.length === 0) return input;
    const out = { ...input };
    const key = this.keyColumn();
    const unset = (v: unknown): boolean => v === undefined || v === null || v === '';
    if (key.uuid && unset(out[key.name])) out[key.name] = newRowKey();
    for (const column of this.encrypted) {
      const value = out[column.name];
      if (value === undefined || value === null) continue;
      out[column.name] = this.seal(column, value, out[key.name], what);
    }
    return out;
  }

  /** update with each encrypted member it sets sealed for the row whose key is id. */
  private sealedUpdate(update: Values, id: unknown, what: string): Values {
    if (this.encrypted.length === 0 || !update) return update;
    const out = { ...update };
    for (const column of this.encrypted) {
      const value = out[column.name];
      if (value === undefined || value === null) continue;
      out[column.name] = this.seal(column, value, id, what);
    }
    return out;
  }

  /** The first encrypted member update sets to a value, or undefined. */
  private setsEncrypted(update: Values | null | undefined): TableColumn | undefined {
    return update ? this.encrypted.find(c => update[c.name] !== undefined && update[c.name] !== null) : undefined;
  }

  private encode(value: unknown, column: TableColumn, what: string): unknown {
    try {
      return encodeParam(value, column);
    } catch (error) {
      throw new Error(`orm: ${this.def.name}.${column.name}: ${what}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
    }
  }

  private requireUser(): string {
    if (this.user === undefined) throw new NoUserError();
    return this.user;
  }

  /** The filter's conditions, each value appended to args, its placeholder numbered after offset (Go's conditions). */
  conditions(filter: Values | null | undefined, args: unknown[], offset: number): string[] {
    if (!filter) return [];
    const c = this.clauses;
    const conditions: string[] = [];
    let n = offset;
    const ordered = [...this.def.columns.filter(col => !col.relation), ...this.def.columns.filter(col => col.relation)];
    for (const column of ordered) {
      const ops = column.filter;
      const member = filter[column.name] as Values | null | undefined;
      if (!ops || ops.length === 0 || member === undefined || member === null) continue;
      const q = column.qualified;
      for (const op of ops) {
        const value = member[op];
        if (value === undefined || value === null) continue;
        switch (op) {
          case 'eq':
          case 'gt':
          case 'gte':
          case 'lt':
          case 'lte':
            conditions.push(fill(c[op as 'eq' | 'gt' | 'gte' | 'lt' | 'lte'], q, ++n));
            args.push(this.encode(value, column, op));
            break;
          case 'ilike':
            conditions.push(fill(c.ilike, q, ++n));
            args.push(`%${String(value)}%`);
            break;
          case 'in': {
            const values = value as readonly unknown[];
            if (values.length === 0) break;
            const placeholders = values.map(v => {
              args.push(this.encode(v, column, op));
              return fill(c.placeholder, ++n);
            });
            conditions.push(fill(c.in, q, placeholders.join(', ')));
            break;
          }
          case 'between': {
            const [min, max] = value as readonly [unknown, unknown];
            conditions.push(fill(c.gte, q, ++n));
            args.push(this.encode(min, column, op));
            conditions.push(fill(c.lte, q, ++n));
            args.push(this.encode(max, column, op));
            break;
          }
          case 'isNull':
            conditions.push(fill(value ? c.isNull : c.isNotNull, q));
            break;
        }
      }
    }
    // A filter through a to-one relation (D90): the foreign key in the
    // keys of the target's live rows its filter finds.
    for (const rel of this.def.relations ?? []) {
      const through = filter[rel.field] as Values | null | undefined;
      if (through === undefined || through === null) continue;
      const target = this.peer(rel.target);
      const before = args.length;
      const inner = target.conditions(through, args, n);
      n += args.length - before;
      if (target.def.softDelete) inner.push(target.def.statements.liveCondition);
      conditions.push(fill(rel.probe, target.whereOf(inner)));
    }
    for (const ref of this.def.referencedBy ?? []) {
      const probe = filter[ref.filter] as { exists?: boolean; includeDeleted?: boolean } | null | undefined;
      if (!probe) continue;
      let text = ref.probe;
      if (ref.live && !probe.includeDeleted) text += ref.live;
      conditions.push(fill(probe.exists ? c.exists : c.notExists, text));
    }
    const search = filter.searchText;
    if (this.def.search && typeof search === 'string') {
      for (const term of searchTerms(search)) {
        conditions.push(fill(c.search, this.def.search, ++n));
        args.push(likePattern(term));
      }
    }
    // anyOf ORs filters of the table (D90), each one's conditions ANDed.
    // A filter of no condition keeps every row, and so does the whole,
    // whose parameters then go too; it is the last condition, so no
    // placeholder follows theirs.
    const anyOf = filter.anyOf as readonly (Values | null | undefined)[] | null | undefined;
    if (anyOf && anyOf.length > 0) {
      const start = args.length;
      const branches: string[] = [];
      let every = false;
      for (const branch of anyOf) {
        const before = args.length;
        const inner = this.conditions(branch, args, n);
        n += args.length - before;
        if (inner.length === 0) {
          every = true;
          continue;
        }
        branches.push(fill(this.group, inner.join(c.and)));
      }
      if (every) args.length = start;
      else conditions.push(fill(this.group, branches.join(c.or ?? ' OR ')));
    }
    return conditions;
  }

  private get group(): string {
    return this.clauses.group ?? '(%s)';
  }

  whereOf(conditions: readonly string[]): string {
    return conditions.length === 0 ? '' : this.clauses.where + conditions.join(this.clauses.and);
  }

  /** The WHERE of a filter, then the soft-delete condition unless deleted rows are asked for, as Go's FindMany writes it. */
  private liveWhere(where: string, includeDeleted: boolean | undefined): string {
    if (!this.def.softDelete || includeDeleted) return where;
    return where === '' ? this.def.statements.liveWhere : where + this.def.statements.live;
  }

  private orderBy(orderBy: readonly OrderBy<string>[] | undefined): string {
    if (!orderBy || orderBy.length === 0) return '';
    const terms = orderBy.map(o => {
      const column = this.column(o.field);
      if (column.relation) throw new Error(`orm: ${this.def.name} orders by its columns, not the relation ${o.field}`);
      return fill(this.clauses.orderTerm, column.qualified, o.desc ? 'DESC' : 'ASC');
    });
    return this.clauses.orderBy + terms.join(', ');
  }

  async getOne(id: unknown, options: ReadSpec = {}): Promise<Values> {
    const s = this.def.statements;
    let text = s.getOne;
    let selection: Selection | undefined;
    if (TableRepository.selects(options)) {
      if (s.getOneFields === undefined) throw new Error(`orm: ${this.def.name}'s definition predates select and include; regenerate its types package`);
      selection = this.selection(options, [], false);
      text = fill(s.getOneFields, this.selectText(selection));
    }
    const { rows } = await run(this.runner, text + s.live, [this.encode(id, this.keyColumn(), 'key')]);
    if (rows.length === 0) throw new NotFoundError(`${this.def.name} ${String(id)} not found`);
    return (await this.loaded(rows, options, selection))[0]!;
  }

  /** rows decoded, with the relations options include, and only the members it selects. */
  private async loaded(rows: readonly Record<string, unknown>[], options: ReadSpec, selection: Selection | undefined): Promise<Values[]> {
    const decoded = rows.map(row => this.decode(row));
    await this.hydrate(decoded, options.include);
    if (selection) this.strip(decoded, selection);
    return decoded;
  }

  async getManyByIds(ids: readonly unknown[]): Promise<Map<unknown, Values>> {
    const out = new Map<unknown, Values>();
    if (ids.length === 0) return out;
    const key = this.keyColumn();
    const seen = new Set<unknown>();
    const values: unknown[] = [];
    for (const id of ids) {
      const value = this.encode(id, key, 'key');
      if (seen.has(value)) continue;
      seen.add(value);
      values.push(value);
    }
    const placeholders = values.map((_, i) => fill(this.clauses.placeholder, i + 1)).join(', ');
    const s = this.def.statements;
    const { rows } = await run(this.runner, fill(s.getMany, placeholders) + s.live, values);
    for (const row of rows) {
      const decoded = this.decode(row);
      out.set(decoded[this.def.key], decoded);
    }
    return out;
  }

  async findOne(filter: Values | null | undefined, options: FindOptions<string> & ReadSpec = {}): Promise<Values | null> {
    // A read that selects or includes lists its columns, as the Go ORM's
    // field selection does; one that does neither selects every column.
    const selection = TableRepository.selects(options) ? this.selection(options, [], false) : undefined;
    const args: unknown[] = [];
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, 0)), options.includeDeleted);
    const s = this.def.statements;
    const columns = selection ? this.selectText(selection) : s.columns;
    const { rows } = await run(this.runner, fill(s.findOne, columns, where, this.orderBy(options.orderBy)), args);
    return rows.length === 0 ? null : (await this.loaded(rows, options, selection))[0]!;
  }

  async findMany(filter: Values | null | undefined, options: FindOptions<string> & ReadSpec = {}): Promise<{ rows: Values[]; total?: number }> {
    const selection = TableRepository.selects(options) ? this.selection(options, [], false) : undefined;
    const args: unknown[] = [];
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, 0)), options.includeDeleted);
    const c = this.clauses;
    const limit = options.limit !== undefined && options.limit > 0 ? options.limit : this.orm.defaultLimit;
    let limitOffset = fill(c.limit, limit);
    if (options.offset !== undefined && options.offset > 0) limitOffset += fill(c.offset, options.offset);
    const s = this.def.statements;
    // The count first, as the Go ORM runs it, unless the options skip it
    // (D89).
    let total: number | undefined;
    if (options.count !== false) {
      const counted = await run(this.runner, fill(s.count, where), args);
      total = Number((counted.rows[0] as { count?: unknown } | undefined)?.count ?? 0);
    }
    const columns = selection ? this.selectText(selection) : s.qualifiedColumns;
    const { rows } = await run(this.runner, fill(s.findMany, columns, where, this.orderBy(options.orderBy), limitOffset), args);
    return { rows: await this.loaded(rows, options, selection), total };
  }

  async findPage(filter: Values | null | undefined, options: PageOptions<string, Values> & ReadSpec = {}): Promise<RowPage<Values, Values>> {
    if (options.limit !== undefined && options.limit < 0) throw new Error(`orm: a page of ${this.def.name} rows holds 1 or more, not ${options.limit}`);
    const limit = options.limit ? options.limit : this.orm.defaultLimit;
    const key = this.keyColumn();
    const field = options.sort?.field ?? this.def.key;
    const column = this.def.columns.find(c => c.name === field && (c.key || c.sortable));
    if (!column) {
      const fields = [this.def.key, ...this.def.columns.filter(c => c.sortable && !c.key).map(c => c.name)];
      throw new Error(`orm: ${this.def.name} sorts by ${fields.join(', ')}, not ${JSON.stringify(field)}`);
    }
    const byKey = column.key === true;
    const s = this.def.statements;
    const args: unknown[] = [];
    const conditions = this.conditions(filter, args, 0);
    if (this.def.softDelete && !options.includeDeleted) conditions.push(s.liveCondition);
    const [comparison, direction] = options.sort?.desc ? ['<', 'DESC'] : ['>', 'ASC'];
    let after: unknown;
    if (options.after) {
      const value = options.after[this.def.key];
      if (value === undefined || value === null || value === '' || (key.uuid && (typeof value !== 'string' || uuidValue(value) === undefined))) {
        throw new PageKeyError(`the ${this.def.name} key holds no ${this.def.key}`);
      }
      after = this.encode(value, key, 'page key');
      args.push(after);
      const placeholder = fill(this.clauses.placeholder, args.length);
      conditions.push(
        byKey
          ? fill(s.pageAfterKey, comparison, placeholder)
          : fill(s.pageAfter, column.qualified, comparison, fill(this.clauses.pageKey, column.column), placeholder, placeholder)
      );
    }
    let order = fill(s.pageOrder, column.qualified, direction);
    if (!byKey) order += fill(s.pageTiebreak, direction);
    // A page reads its rows' keys whatever it selects: the next key holds
    // the last one's.
    const selection = TableRepository.selects(options) ? this.selection(options, [], true) : undefined;
    const columns = selection ? this.selectText(selection) : s.qualifiedColumns;
    const { rows } = await run(this.runner, fill(s.page, columns, this.whereOf(conditions), order, limit + 1), args);
    if (rows.length === 0 && options.after && !byKey) {
      // A key whose row no longer exists places no row: only then is the
      // page empty without being the last.
      const exists = await run(this.runner, s.pageKeyExists, [after]);
      if (!(exists.rows[0] as { exists?: unknown } | undefined)?.exists) {
        throw new PageKeyError(`no ${this.def.name} row has the key's ${this.def.key}`);
      }
    }
    // The relations load for every row read, the one past the page among
    // them, as the Go ORM's FindPage loads them; the next key is read
    // before the members the caller did not select leave the rows.
    const decoded = rows.map(row => this.decode(row));
    await this.hydrate(decoded, options.include);
    const kept = decoded.slice(0, limit);
    const next = decoded.length > limit ? { [this.def.key]: kept[limit - 1]![this.def.key] } : null;
    if (selection) this.strip(kept, selection);
    return { rows: kept, next };
  }

  async nearest(field: string, vector: readonly number[], options: NearestOptions<Values> = {}): Promise<Neighbour<Values>[]> {
    const column = this.def.columns.find(c => c.name === field && c.vector);
    const s = this.def.statements;
    const nearest = this.orm.nearest;
    if (!column || !s.nearest || !nearest) throw new Error(`orm: ${this.def.name} has no vector field ${JSON.stringify(field)}`);
    if (!Array.isArray(vector) || vector.length !== column.dimensions) {
      throw new Error(`orm: ${this.def.name}.${field} holds vectors of ${column.dimensions} dimensions, not ${Array.isArray(vector) ? vector.length : 'none'}`);
    }
    const metric = options.metric ?? 'cosine';
    const distance = nearest.metrics[metric];
    if (distance === undefined) throw new Error(`orm: nearest orders by ${Object.keys(nearest.metrics).join(', ')}, not ${JSON.stringify(metric)}`);
    if (options.k !== undefined && options.k < 0) throw new Error(`orm: nearest reads 1 or more ${this.def.name} rows, not ${options.k}`);
    const k = options.k ? options.k : nearest.defaultK;
    // The filter's conditions, then the keys it leaves out, then the
    // soft-delete condition, then a vector that may be null's presence;
    // the vector is the last parameter, as the Go ORM's Nearest writes it.
    const args: unknown[] = [];
    const conditions = this.conditions(options.filter, args, 0);
    if (options.excludeIds && options.excludeIds.length > 0) {
      const key = this.keyColumn();
      const placeholders = options.excludeIds.map(id => {
        args.push(this.encode(id, key, 'excludeIds'));
        return fill(this.clauses.placeholder, args.length);
      });
      conditions.push(fill(this.clauses.notIn, key.qualified, placeholders.join(', ')));
    }
    if (this.def.softDelete && !options.includeDeleted) conditions.push(s.liveCondition);
    if (!column.required) conditions.push(fill(this.clauses.isNotNull, column.qualified));
    args.push(this.encode(vector, column, 'nearest'));
    const by = fill(distance, column.qualified, args.length);
    const text = fill(s.nearest, s.qualifiedColumns, by, this.whereOf(conditions), by, k);
    // The index's settings, set for the transaction the read runs in, one
    // of its own when none is open (D89).
    const settings = this.nearestSettings(options);
    const read = async (repo: TableRepository): Promise<Neighbour<Values>[]> => {
      if (settings) await run(repo.runner, settings.text, settings.values);
      const { rows } = await run(repo.runner, text, args);
      return rows.map(row => ({ row: repo.decode(row), distance: Number(row._distance) }));
    };
    return settings ? this.atomically(read) : read(this);
  }

  /** The statement that sets the vector index's settings the options name, and its values, or undefined when they name none. */
  private nearestSettings(options: NearestOptions<Values, unknown>): { text: string; values: string[] } | undefined {
    const settings = this.orm.nearest?.settings;
    if (!settings) return undefined;
    const calls: string[] = [];
    const values: string[] = [];
    for (const setting of settings.each) {
      const value = options[setting.option];
      if (value === undefined || value === null) continue;
      if (setting.values) {
        const takes = settings.each.filter(s => s.option === setting.option).some(s => s.values?.includes(String(value)));
        if (typeof value !== 'string' || !takes) {
          const all = [...new Set(settings.each.filter(s => s.option === setting.option).flatMap(s => s.values ?? []))];
          throw new Error(`orm: nearest's ${setting.option} is ${all.join(', ')}, not ${JSON.stringify(value)}`);
        }
        if (!setting.values.includes(value)) continue;
      } else if (typeof value !== 'number' || !Number.isInteger(value) || value < (setting.min ?? 1) || value > (setting.max ?? Number.MAX_SAFE_INTEGER)) {
        throw new Error(`orm: nearest's ${setting.option} is a whole number from ${setting.min ?? 1} to ${setting.max ?? Number.MAX_SAFE_INTEGER}, not ${JSON.stringify(value)}`);
      }
      values.push(String(value));
      calls.push(fill(setting.sql, values.length));
    }
    return calls.length === 0 ? undefined : { text: fill(settings.statement, calls.join(', ')), values };
  }

  /** The members a create writes, stamped with the acting user: createdBy needs one, as the Go ORM's CreateOne does. */
  private stamped(input: Values): Values {
    const out = { ...input };
    if (this.audit('createdBy')) out[this.audit('createdBy')!.name] = this.requireUser();
    const updatedBy = this.audit('updatedBy');
    if (updatedBy) {
      if (updatedBy.required || this.orm.requireUpdatedBy) out[updatedBy.name] = this.requireUser();
      else if (this.user !== undefined) out[updatedBy.name] = this.user;
    }
    return out;
  }

  /**
   * Whether a create writes the column, and what: the member when set, else
   * its default, as Go writes an unset enum's. null sets a required JSON
   * column to the JSON null.
   */
  private insertValue(input: Values, column: TableColumn): { set: boolean; value?: unknown } {
    const value = input[column.name];
    if (value === null && jsonNull(column)) return { set: true, value: this.encode(value, column, 'create') };
    if (value !== undefined && value !== null && !(column.default !== undefined && value === '')) {
      return { set: true, value: this.encode(value, column, 'create') };
    }
    if (column.default !== undefined) return { set: true, value: this.encode(column.default, column, 'default') };
    if (column.required && !column.auto && !column.key) {
      throw new Error(`orm: create ${this.def.name}: ${column.name} is required`);
    }
    return { set: false };
  }

  /** Whether a create writes the column at all: never createdAt, updatedAt or internal metadata. */
  private insertable(column: TableColumn): boolean {
    return !column.internal && column.audit !== 'createdAt' && column.audit !== 'updatedAt';
  }

  /** The columns, placeholders and values of one row's insert, in CreateOne's order: the key, the fields, the relations. */
  private insertOne(input: Values, args: unknown[]): { columns: string[]; placeholders: string[] } {
    const columns: string[] = [];
    const placeholders: string[] = [];
    const add = (column: TableColumn, value: unknown): void => {
      columns.push(column.column);
      args.push(value);
      placeholders.push(fill(this.clauses.placeholder, args.length));
    };
    const key = this.keyColumn();
    const keyValue = this.insertValue(input, key);
    if (keyValue.set) add(key, keyValue.value);
    for (const column of this.def.columns) {
      if (column.key || column.relation || !this.insertable(column)) continue;
      const v = this.insertValue(input, column);
      if (v.set) add(column, v.value);
    }
    for (const column of this.def.columns) {
      if (!column.relation) continue;
      const v = this.insertValue(input, column);
      if (v.set) add(column, v.value);
    }
    return { columns, placeholders };
  }

  async createOne(input: Values): Promise<Values> {
    const args: unknown[] = [];
    const { columns, placeholders } = this.insertOne(this.sealed(this.stamped(input), 'create'), args);
    const s = this.def.statements;
    const { rows } = await run(this.runner, fill(s.insert, columns.join(', '), placeholders.join(', '), s.columns), args);
    return this.decode(rows[0]!);
  }

  async createMany(inputs: readonly Values[]): Promise<Values[]> {
    if (inputs.length === 0) return [];
    const order = this.insertOrder();
    const rows = inputs.map(input => {
      const stamped = this.sealed(this.stamped(input), 'create');
      return order.map(column => this.insertValue(stamped, column));
    });
    const args: unknown[] = [];
    const { columns, tuples } = this.insertTuples(order, rows, args);
    const s = this.def.statements;
    const result = await run(this.runner, fill(s.insertMany, columns.join(', '), tuples.join(', '), s.columns), args);
    return result.rows.map(row => this.decode(row));
  }

  /**
   * The columns a batch's rows have cells for, in CreateMany's order: the
   * fields, the key at its place among them, then the relations
   * (sqlutil.TableModel.InsertColumns).
   */
  private insertOrder(): TableColumn[] {
    return [...this.def.columns.filter(c => !c.relation && (c.key || this.insertable(c))), ...this.def.columns.filter(c => c.relation)];
  }

  /**
   * The columns and tuples of a batch insert of rows, each a cell per
   * column of order, each value appended to args: a column any row sets is
   * inserted, DEFAULT for a row that leaves it unset.
   */
  private insertTuples(order: readonly TableColumn[], rows: readonly { set: boolean; value?: unknown }[][], args: unknown[]): { columns: string[]; tuples: string[] } {
    const inserted = order.map((_, i) => rows.some(row => row[i]!.set));
    const columns = order.filter((_, i) => inserted[i]).map(c => c.column);
    const tuples = rows.map(row => {
      const cells: string[] = [];
      row.forEach((cell, i) => {
        if (!inserted[i]) return;
        if (cell.set) {
          args.push(cell.value);
          cells.push(fill(this.clauses.placeholder, args.length));
        } else {
          cells.push(this.clauses.default);
        }
      });
      return `(${cells.join(', ')})`;
    });
    return { columns, tuples };
  }

  /**
   * The assignments of update, each value appended to args, in UpdateOne's
   * order: the fields, the relations' keys. stamps adds updatedAt and
   * updatedBy, as UpdateOne writes them.
   */
  private setClauses(update: Values, args: unknown[], stamps: boolean, compared?: { current: string[]; next: string[] }): string[] {
    const sets: string[] = [];
    const c = this.clauses;
    const settable = (column: TableColumn): boolean => !column.key && !column.audit && !column.internal;
    const ordered = [...this.def.columns.filter(col => !col.relation && settable(col)), ...this.def.columns.filter(col => col.relation)];
    for (const column of ordered) {
      if (!(column.name in update)) continue;
      const value = update[column.name];
      if (value === undefined) continue;
      if (value === null && !jsonNull(column)) {
        if (column.required) throw new Error(`orm: update ${this.def.name}: ${column.name} cannot be null`);
        sets.push(fill(c.setNull, column.column));
        // An upsert only if changed compares each column it sets with the
        // value it sets (D89).
        compared?.current.push(this.compareAs(column, column.qualified));
        compared?.next.push(c.null);
        continue;
      }
      args.push(this.encode(value, column, 'update'));
      sets.push(fill(c.set, column.column, args.length));
      compared?.current.push(this.compareAs(column, column.qualified));
      compared?.next.push(this.compareAs(column, fill(c.placeholder, args.length)));
    }
    if (stamps) this.stamp(sets, update, args);
    return sets;
  }

  /** value, the column or a value of it, as an upsert only if changed compares the column (D89). */
  private compareAs(column: TableColumn, value: string): string {
    return column.compare ? fill(column.compare, value) : value;
  }

  private stamp(sets: string[], update: Values, args: unknown[]): void {
    const s = this.def.statements;
    if (this.audit('updatedAt')) {
      args.push(new Date());
      sets.push(fill(s.updatedAt, args.length));
    }
    const updatedBy = this.audit('updatedBy');
    if (updatedBy) {
      const by = update[updatedBy.name] ?? this.user;
      if (by !== undefined && by !== null) {
        args.push(this.encode(by, updatedBy, 'updatedBy'));
        sets.push(fill(s.updatedBy, args.length));
      } else if (this.orm.requireUpdatedBy) {
        throw new NoUserError();
      }
    }
  }

  async updateOne(id: unknown, update: Values): Promise<Values> {
    const args: unknown[] = [];
    const sets = this.setClauses(this.sealedUpdate(update, id, 'update'), args, true);
    if (sets.length === 0) throw new Error(`orm: update ${this.def.name}: no fields to update`);
    args.push(this.encode(id, this.keyColumn(), 'key'));
    const s = this.def.statements;
    const text = fill(s.update, sets.join(', '), args.length) + s.live + s.returning;
    const { rows } = await run(this.runner, text, args);
    if (rows.length === 0) throw new NotFoundError(`${this.def.name} ${String(id)} not found`);
    return this.decode(rows[0]!);
  }

  async updateMany(filter: Values | null, update: Values): Promise<number> {
    const encrypted = this.setsEncrypted(update);
    if (encrypted) {
      throw new Error(`orm: updateMany of ${this.def.name} cannot set ${encrypted.name}, an Encrypted<T> field: each row's envelope is bound to its key; set it with updateOne`);
    }
    const args: unknown[] = [];
    const sets = this.setClauses(update, args, true);
    if (sets.length === 0) throw new Error(`orm: update ${this.def.name}: no fields to update`);
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, args.length)), false);
    const { rowCount } = await run(this.runner, fill(this.def.statements.updateMany, sets.join(', '), where), args);
    return rowCount;
  }

  async upsertOne(input: Values, on: readonly string[], update: Values, options: UpsertOptions = {}): Promise<Values> {
    return (await this.upsertOneResult(input, on, update, options)).row;
  }

  async upsertOneResult(input: Values, on: readonly string[], update: Values, options: UpsertOptions = {}): Promise<UpsertResult<Values>> {
    const name = on.join(',');
    const key = this.uniqueKey(name);
    const onlyIfChanged = options.onlyIfChanged === true;
    if (onlyIfChanged && !key.rows) throw this.expressionKey('upsertOne', name);
    const change = update ?? {};
    // An encrypted field the update sets is sealed under the key of the row
    // it updates, which only the upsert's statement names, so a second
    // statement sets it (D89); either the upsert or a read of a row it left
    // as it was may need one, in its transaction.
    const sealing = this.encrypted.filter(c => change[c.name] !== undefined && change[c.name] !== null);
    const write = (repo: TableRepository): Promise<UpsertResult<Values>> => repo.upsertRow(input, name, key, change, sealing, onlyIfChanged);
    return sealing.length > 0 || onlyIfChanged ? this.atomically(write) : write(this);
  }

  async upsertMany(inputs: readonly Values[], on: readonly string[], update: readonly string[], options: UpsertManyOptions = {}): Promise<UpsertResult<Values>[]> {
    const name = on.join(',');
    const key = this.uniqueKey(name);
    if (!key.rows) throw this.expressionKey('upsertMany', name);
    const named = new Set(update ?? []);
    for (const member of named) {
      const column = this.def.columns.find(c => c.name === member);
      if (!column || column.key || column.audit || column.internal) {
        throw new Error(`orm: upsertMany of ${this.def.name} sets the fields an update sets, not ${JSON.stringify(member)}`);
      }
    }
    // In UpdateOne's order, whatever the order update names them in: the
    // fields, then the relations' keys.
    const fields = [...this.def.columns.filter(c => !c.relation), ...this.def.columns.filter(c => c.relation)].filter(c => named.has(c.name));
    if (options.batch !== undefined && !(Number.isInteger(options.batch) && options.batch > 0)) {
      throw new Error(`orm: upsertMany writes 1 or more ${this.def.name} rows a statement, not ${options.batch}`);
    }
    if (inputs.length === 0) return [];
    // As many rows as Postgres binds the parameters of, or fewer.
    const most = Math.max(1, Math.floor((MAX_PARAMETERS - UPSERT_STAMPS) / this.insertOrder().length));
    const batch = Math.min(options.batch ?? most, most);
    const onlyIfChanged = options.onlyIfChanged === true;
    const write = (repo: TableRepository): Promise<UpsertResult<Values>[]> => repo.upsertBatches(inputs, name, key, fields, batch, onlyIfChanged);
    return inputs.length > batch || onlyIfChanged || fields.some(c => c.encrypted) ? this.atomically(write) : write(this);
  }

  private uniqueKey(name: string): UniqueKeyDefinition {
    const key = this.def.uniqueKeys[name];
    if (!key) throw new Error(`orm: ${this.def.name} has no unique key ${JSON.stringify(name)}; its keys are ${Object.keys(this.def.uniqueKeys).join('; ')}`);
    return key;
  }

  private expressionKey(method: string, name: string): Error {
    return new Error(
      `orm: ${method} of ${this.def.name} on ${name}, a unique index over an expression, cannot read back the rows it leaves as they were or match the rows it returns to its inputs by their columns; upsert on another key, or with upsertOne alone`
    );
  }

  /** The columns of the unique key name, in its fields' order. */
  private keyColumns(name: string): TableColumn[] {
    return name.split(',').map(field => {
      const column = this.def.columns.find(c => c.field === field);
      if (!column) throw new Error(`orm: ${this.def.name}'s unique key ${name} names no column ${field}`);
      return column;
    });
  }

  /** The WHERE of an upsert's update: the live rows, and, only if changed, the rows whose columns current differ from the values next (D89). */
  private upsertWhere(current: readonly string[], next: readonly string[], onlyIfChanged: boolean): string {
    const conditions: string[] = [];
    if (this.def.softDelete) conditions.push(this.def.statements.liveCondition);
    if (onlyIfChanged) conditions.push(current.length === 0 ? this.clauses.never : fill(this.clauses.changed, current.join(', '), next.join(', ')));
    return this.whereOf(conditions);
  }

  /** upsertOneResult on the repository's runner: the upsert, a read of a row it left as it was, and the second statement of an encrypted field. */
  private async upsertRow(input: Values, name: string, key: UniqueKeyDefinition, update: Values, sealing: readonly TableColumn[], onlyIfChanged: boolean): Promise<UpsertResult<Values>> {
    const args: unknown[] = [];
    const sealed = this.sealed(this.stamped(input), 'upsert');
    const { columns, placeholders } = this.insertOne(sealed, args);
    const plain = { ...update };
    for (const column of sealing) delete plain[column.name];
    const compared = { current: [] as string[], next: [] as string[] };
    const sets = this.setClauses(plain, args, false, compared);
    // An update that sets no field leaves the row as it is, stamps and
    // all, and returns it.
    const assignments = sets.length === 0 ? [key.keep] : [...sets];
    if (sets.length > 0) this.stamp(assignments, update, args);
    const s = this.def.statements;
    const where = this.upsertWhere(compared.current, compared.next, onlyIfChanged);
    const { rows } = await run(this.runner, fill(s.upsert, columns.join(', '), `(${placeholders.join(', ')})`, key.conflict, assignments.join(', '), where), args);
    let result: UpsertResult<Values>;
    if (rows.length > 0) {
      result = { row: this.decode(rows[0]!), status: rows[0]![UPSERT_INSERTED] === true ? 'inserted' : sets.length > 0 ? 'updated' : 'unchanged' };
    } else if (!onlyIfChanged) {
      throw new NotFoundError(`the ${this.def.name} row that holds the key ${name} is deleted`);
    } else {
      const keyColumns = this.keyColumns(name);
      const [found] = await this.rowsOfKeys(name, key, keyColumns, [keyColumns.map(c => this.insertValue(sealed, c))]);
      result = { row: found!, status: 'unchanged' };
    }
    return this.resealed(result, update, sealing);
  }

  /** upsertMany on the repository's runner, a batch at a time. */
  private async upsertBatches(
    inputs: readonly Values[],
    name: string,
    key: UniqueKeyDefinition,
    fields: readonly TableColumn[],
    batch: number,
    onlyIfChanged: boolean
  ): Promise<UpsertResult<Values>[]> {
    const order = this.insertOrder();
    const keyColumns = this.keyColumns(name);
    const at = keyColumns.map(c => order.indexOf(c));
    const plain = fields.filter(c => !c.encrypted);
    const sealing = fields.filter(c => c.encrypted);
    const c = this.clauses;
    const s = this.def.statements;
    const results: UpsertResult<Values>[] = [];
    for (let start = 0; start < inputs.length; start += batch) {
      const chunk = inputs.slice(start, start + batch);
      const cells = chunk.map(input => {
        const sealed = this.sealed(this.stamped(input), 'upsert');
        return order.map(column => this.insertValue(sealed, column));
      });
      const args: unknown[] = [];
      const { columns, tuples } = this.insertTuples(order, cells, args);
      // Each field takes the value the input would have inserted.
      const sets = plain.map(column => fill(c.setExcluded, column.column, column.column));
      const current = plain.map(column => this.compareAs(column, column.qualified));
      const next = plain.map(column => this.compareAs(column, fill(c.excluded, column.column)));
      const assignments = sets.length === 0 ? [key.keep] : [...sets];
      if (sets.length > 0) this.stamp(assignments, {}, args);
      const where = this.upsertWhere(current, next, onlyIfChanged);
      const { rows } = await run(this.runner, fill(s.upsert, columns.join(', '), tuples.join(', '), key.conflict, assignments.join(', '), where), args);
      // A row is its input's by the key's values; an input that sets none
      // of them, whose key the database generates, takes the next row whose
      // key no input holds, as the rows come back in the inputs' order.
      const waiting = new Map<string, number[]>();
      const unkeyed: number[] = [];
      cells.forEach((row, i) => {
        const text = keyText(keyColumns, at.map(j => (row[j]!.set ? row[j]!.value : undefined)));
        if (text === undefined) unkeyed.push(i);
        else waiting.set(text, [...(waiting.get(text) ?? []), i]);
      });
      const matched: (UpsertResult<Values> | undefined)[] = chunk.map(() => undefined);
      for (const raw of rows) {
        const text = keyText(keyColumns, keyColumns.map(column => raw[columnKey(column.column)] ?? null));
        const i = (text === undefined ? undefined : waiting.get(text)?.shift()) ?? unkeyed.shift();
        if (i === undefined) throw new Error(`orm: upsertMany of ${this.def.name} wrote a row whose ${name} no input holds`);
        matched[i] = { row: this.decode(raw), status: raw[UPSERT_INSERTED] === true ? 'inserted' : sets.length > 0 ? 'updated' : 'unchanged' };
      }
      // A row it did not return is one it left as it was, only if changed,
      // which it reads back by its key, or a deleted one.
      const left = matched.flatMap((result, i) => (result === undefined ? [i] : []));
      if (left.length > 0) {
        if (!onlyIfChanged) throw new NotFoundError(`the ${this.def.name} row that holds the ${name} of input ${start + left[0]!} is deleted`);
        const found = await this.rowsOfKeys(name, key, keyColumns, left.map(i => at.map(j => cells[i]![j]!)));
        left.forEach((i, n) => {
          matched[i] = { row: found[n]!, status: 'unchanged' };
        });
      }
      for (const [i, result] of matched.entries()) {
        const input = chunk[i]!;
        const update: Values = {};
        for (const column of sealing) update[column.name] = input[column.name] ?? null;
        results.push(await this.resealed(result!, update, sealing));
      }
    }
    return results;
  }

  /**
   * The rows that hold the keys of the cells, each a key column's insert
   * cell, in their order, which an upsert only if changed left as they
   * were: NotFoundError for a key no live row holds (D89).
   */
  private async rowsOfKeys(name: string, key: UniqueKeyDefinition, keyColumns: readonly TableColumn[], keys: readonly { set: boolean; value?: unknown }[][]): Promise<Values[]> {
    const args: unknown[] = [];
    const tuples = keys.map(cells => {
      const placeholders = cells.map(cell => {
        if (!cell.set) throw new NotFoundError(`no ${this.def.name} row holds an unset ${name}`);
        args.push(cell.value);
        return fill(this.clauses.placeholder, args.length);
      });
      return `(${placeholders.join(', ')})`;
    });
    const { rows } = await run(this.runner, fill(key.rows!, tuples.join(', ')), args);
    const byKey = new Map<string, Record<string, unknown>>();
    for (const raw of rows) {
      const text = keyText(keyColumns, keyColumns.map(column => raw[columnKey(column.column)] ?? null));
      if (text !== undefined) byKey.set(text, raw);
    }
    const deletedAt = this.audit('deletedAt');
    return keys.map(cells => {
      const raw = byKey.get(keyText(keyColumns, cells.map(cell => cell.value))!);
      if (!raw || (deletedAt && raw[columnKey(deletedAt.column)] !== null && raw[columnKey(deletedAt.column)] !== undefined)) {
        throw new NotFoundError(`the ${this.def.name} row that holds the key ${name} is deleted`);
      }
      return this.decode(raw);
    });
  }

  /**
   * result, with each encrypted field of update the row it updated holds
   * another value of set by a second statement, sealed under the row's key,
   * and updated (D89). A row the upsert inserted holds the input's already.
   */
  private async resealed(result: UpsertResult<Values>, update: Values, sealing: readonly TableColumn[]): Promise<UpsertResult<Values>> {
    if (result.status === 'inserted' || sealing.length === 0) return result;
    const change: Values = {};
    for (const column of sealing) {
      const wanted = update[column.name];
      if (wanted === undefined) continue;
      const held = result.row[column.name];
      const same = wanted === null ? held === null || held === undefined : held !== null && held !== undefined && revealed(held) === revealed(wanted);
      if (!same) change[column.name] = wanted;
    }
    if (Object.keys(change).length === 0) return result;
    const updatedBy = this.audit('updatedBy');
    if (updatedBy && update[updatedBy.name] !== undefined) change[updatedBy.name] = update[updatedBy.name];
    return { row: await this.updateOne(result.row[this.def.key], change), status: 'updated' };
  }

  async deleteOne(id: unknown): Promise<void> {
    if (!this.def.softDelete) return this.hardDeleteOne(id);
    const user = this.requireUser();
    const key = this.encode(id, this.keyColumn(), 'key');
    const deletedBy = this.audit('deletedBy');
    const args = deletedBy ? [new Date(), this.encode(user, deletedBy, 'deletedBy'), key] : [new Date(), key];
    const { rowCount } = await run(this.runner, this.def.statements.softDelete, args);
    if (rowCount === 0) throw new NotFoundError(`${this.def.name} ${String(id)} not found`);
  }

  async deleteMany(filter: Values | null): Promise<number> {
    const s = this.def.statements;
    if (this.def.softDelete) {
      const user = this.requireUser();
      const deletedBy = this.audit('deletedBy');
      const args: unknown[] = deletedBy ? [new Date(), this.encode(user, deletedBy, 'deletedBy')] : [new Date()];
      const where = this.liveWhere(this.whereOf(this.conditions(filter, args, args.length)), false);
      const { rowCount } = await run(this.runner, fill(s.softDeleteMany, where), args);
      return rowCount;
    }
    const args: unknown[] = this.def.historyActor ? [this.actor()] : [];
    const where = this.whereOf(this.conditions(filter, args, args.length));
    const text = (this.def.historyActor ? this.orm.historyActorCTE : '') + fill(s.deleteMany, where);
    const { rowCount } = await run(this.runner, text, args);
    await this.clearActor();
    return rowCount;
  }

  async hardDeleteOne(id: unknown): Promise<void> {
    const key = this.encode(id, this.keyColumn(), 'key');
    const s = this.def.statements;
    const args = this.def.historyActor ? [this.actor(), key] : [key];
    const text = (this.def.historyActor ? this.orm.historyActorCTE : '') + s.delete;
    const { rowCount } = await run(this.runner, text, args);
    await this.clearActor();
    if (rowCount === 0) throw new NotFoundError(`${this.def.name} ${String(id)} not found`);
  }

  async reencrypt(options: { readonly batch?: number } = {}): Promise<number> {
    const s = this.def.statements;
    if (this.encrypted.length === 0 || !s.stale || !s.reseal) throw new Error(`orm: ${this.def.name} has no Encrypted<T> field to reencrypt`);
    const ring = this.ring(this.encrypted[0]!);
    const batch = options.batch !== undefined && options.batch > 0 ? Math.trunc(options.batch) : this.orm.defaultLimit;
    const key = columnKey(this.keyColumn().column);
    let resealed = 0;
    for (;;) {
      const { rows } = await run(this.runner, s.stale, [ring.currentPrefix, batch]);
      if (rows.length === 0) return resealed;
      for (const row of rows) {
        const held = this.encrypted.map(c => row[columnKey(c.column)] ?? null);
        const sealed = this.encrypted.map((c, i) => {
          const envelope = held[i];
          if (envelope === null || ring.isCurrent(String(envelope))) return envelope;
          const cell = this.cell(c, row[key]);
          return ring.seal(cell, ring.open(cell, String(envelope)));
        });
        const { rowCount } = await run(this.runner, s.reseal, [...sealed, row[key], ...held]);
        resealed += rowCount;
      }
    }
  }

  /** The history actor of a hard delete: the acting user, hyphenated, or null. */
  private actor(): string | null {
    return this.user === undefined ? null : uuidToDb(this.user);
  }

  /** Empties the history actor after a hard delete in a transaction, so a later statement in it does not record the same actor. */
  private async clearActor(): Promise<void> {
    if (this.def.historyActor && this.runner.inTransaction) await run(this.runner, this.orm.clearHistoryActor, []);
  }
}

/** Whether null is a value of the column: the JSON null, in a JSON column that may not be SQL NULL. */
function jsonNull(column: TableColumn): boolean {
  return Boolean((column.json || column.jsonText) && column.required);
}

/** The values of values that are neither null nor undefined, each once, in order. */
function distinct(values: readonly unknown[]): unknown[] {
  const seen = new Set<unknown>();
  const out: unknown[] = [];
  for (const value of values) {
    if (value === null || value === undefined || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

function repositories(orm: OrmDefinition, runner: Runner, acting: Acting, keys: FieldKeyring | undefined): Record<string, TableRepository> {
  const out: Record<string, TableRepository> = {};
  for (const [member, def] of Object.entries(orm.tables)) {
    if (ORM_MEMBERS.includes(member)) throw new Error(`orm: the table ${def.name} would be the ORM's own member ${member}`);
    out[member] = new TableRepository(orm, def, runner, acting, keys);
  }
  // An include and a filter through a relation read the target through
  // its repository on the same runner, acting as the same user (D90).
  for (const repository of Object.values(out)) repository.peers = out;
  return out;
}

/** The ORM's queries member over runner, on a database that declares queries (D86). */
function declaredQueries(orm: OrmDefinition, runner: Runner): { queries?: unknown } {
  if (!orm.queries) return {};
  return { queries: queriesOf(orm.queries, (text, values) => run(runner, text, values), orm.defaultLimit) };
}

function userOf(id: string | null | undefined): string | undefined {
  if (id === null || id === undefined || id === '') return undefined;
  if (uuidValue(id) === undefined) throw new Error(`orm: the acting user ${JSON.stringify(id)} is no UUID`);
  return id;
}

/**
 * The raw query of an ORM or a transaction over runner: a text and its
 * values, or a pg query config, its rows decoded as a table's repository,
 * acting as whom acting resolves to with the field keys, reads them when
 * options say so (D89).
 */
function queryOf(orm: OrmDefinition, runner: Runner, acting: Acting, keys: FieldKeyring | undefined) {
  return async <Row>(text: string | OrmQueryConfig, values?: readonly unknown[], options?: OrmQueryOptions): Promise<OrmQueryResult<Row>> => {
    const config = typeof text === 'string' ? { text, values: [...(values ?? [])] } : { ...text, values: [...(text.values ?? values ?? [])] };
    const decode = options?.decode;
    if (decode === undefined) {
      const result = await runner.queryable.query(config);
      return { rows: result.rows as Row[], rowCount: result.rowCount };
    }
    // The rows read as a repository reads them, with its types.
    const result = await runner.queryable.query({ ...config, types: config.types ?? ormTypes });
    const read: (row: Record<string, unknown>) => unknown = Array.isArray(decode)
      ? row => decodeRow(row, decode as readonly RowField[])
      : row => new TableRepository(orm, decode as TableDefinition, runner, acting, keys).read(row);
    return { rows: (result.rows as Record<string, unknown>[]).map(read) as Row[], rowCount: result.rowCount };
  };
}

/**
 * A transaction over client, acting as whom acting resolves to, with the
 * field keys, nested in depth savepoints, 0 for a transaction of its own
 * (D89). A savepoint acts as the transaction it is nested in, the caller
 * (D87) among them.
 */
function txOf<R>(orm: OrmDefinition, client: OrmQueryable, acting: Acting, keys: FieldKeyring | undefined, depth = 0): OrmTx<R> {
  const runner: Runner = { queryable: client, inTransaction: true };
  const query = queryOf(orm, runner, acting, keys);
  return {
    ...repositories(orm, runner, acting, keys),
    ...declaredQueries(orm, runner),
    get actingUser() {
      return actingUser(acting);
    },
    withUser: (id: string | null | undefined) => txOf<R>(orm, client, userOf(id), keys, depth),
    as: (principal: Actor | null | undefined) => txOf<R>(orm, client, userOf(principal?.subject), keys, depth),
    asSystem: () => txOf<R>(orm, client, systemActorOf(orm).id, keys, depth),
    query,
    exec: async (text: string, values?: readonly unknown[]) => (await query(text, values)).rowCount ?? 0,
    // A nested transaction is a savepoint, named for its depth, as
    // sqlutil.Savepoint writes it: released when fn returns, rolled back to
    // and released when it throws, which leaves this transaction going on.
    async transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>): Promise<T> {
      const savepoint = `sp_${depth + 1}`;
      await client.query({ text: `SAVEPOINT ${savepoint}`, values: [] });
      let out: T;
      try {
        out = await fn(txOf<R>(orm, client, acting, keys, depth + 1));
      } catch (error) {
        await client.query({ text: `ROLLBACK TO SAVEPOINT ${savepoint}`, values: [] });
        await client.query({ text: `RELEASE SAVEPOINT ${savepoint}`, values: [] });
        throw error;
      }
      await client.query({ text: `RELEASE SAVEPOINT ${savepoint}`, values: [] });
      return out;
    },
  } as unknown as OrmTx<R>;
}

/**
 * The repositories of a database's tables over pool, as its types package's
 * ./orm entry describes them, acting as the user options names, or as the
 * user it is when a string, or as no one for null; with no user, as the
 * caller of the operation being run (D87). createOrm there calls it with
 * the definitions and types the result. The field keys are read once, and
 * a malformed secret throws FieldEncryptionError here.
 */
export function ormOf<R>(pool: OrmPool, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  const opts: OrmOptions = typeof options === 'object' && options !== null ? options : { user: options };
  const keys = opts.fieldKeys === undefined ? undefined : parseFieldKeys(opts.fieldKeys);
  // A system actor that is no UUID throws here, not at a job's first write.
  systemActorOf(orm);
  return ormWith<R>(pool, orm, opts.user === undefined ? CALLER : userOf(opts.user), keys);
}

function ormWith<R>(pool: OrmPool, orm: OrmDefinition, acting: Acting, keys: FieldKeyring | undefined): Orm<R> {
  const runner: Runner = { queryable: pool, inTransaction: false, pool };
  const query = queryOf(orm, runner, acting, keys);
  return {
    ...repositories(orm, runner, acting, keys),
    ...declaredQueries(orm, runner),
    get actingUser() {
      return actingUser(acting);
    },
    withUser: (id: string | null | undefined) => ormWith<R>(pool, orm, userOf(id), keys),
    as: (principal: Actor | null | undefined) => ormWith<R>(pool, orm, userOf(principal?.subject ?? null), keys),
    asSystem: () => ormWith<R>(pool, orm, systemActorOf(orm).id, keys),
    async transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>, options?: TransactionOptions): Promise<T> {
      return inTransaction(pool, beginStatement(options), client => fn(txOf<R>(orm, client, acting, keys)));
    },
    query,
    exec: async (text: string, values?: readonly unknown[]) => (await query(text, values)).rowCount ?? 0,
  } as unknown as Orm<R>;
}
