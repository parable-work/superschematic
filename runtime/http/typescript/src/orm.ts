import pg from 'pg';
import { currentCaller } from './caller.js';
import { FieldEncryptionError, newRowKey, parseFieldKeys, type FieldCell, type FieldKeyring, type FieldKeys } from './fieldcrypt.js';
import { columnKey, decodeRow, encodeParam, postgresTypeParsers, withTypeParsers, type RowField } from './pgcodec.js';
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
message in the transaction.

A table's Encrypted<T> columns (D85) hold envelopes, which a write seals
and a read opens with the DB service's field keys (createOrm(pool, {
fieldKeys })), bound to the table, the field and the row's key: a create
of such a table generates a UUID key the input leaves unset, a read holds
each value in a SecretValue, and reencrypt reseals the envelopes of an
older key under the current one. No filter holds such a column; updateMany
cannot set one, and upsertOne can only by the primary key, since each
row's envelope is bound to that row's key.

Field selection, include and one-to-many loading, and the @versioned
history readers are the Go ORM's alone for now; a relation's rows load
through the target's getManyByIds.
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
}

/** A table's statements on Postgres, sqlutil.TableStatements: format strings of %s and %d alone. */
export interface TableStatements {
  readonly columns: string;
  readonly qualifiedColumns: string;
  readonly getOne: string;
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
}

/** A unique key an upsert is keyed by: its conflict target and the assignment of an update that sets nothing. */
export interface UniqueKeyDefinition {
  readonly conflict: string;
  readonly keep: string;
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
}

/** How nearest orders rows, on a database with a vector column (D84). */
export interface NearestDefinition {
  /** Each metric's distance of the vector column %s from the parameter $%d. */
  readonly metrics: Readonly<Record<VectorMetric, string>>;
  /** The rows nearest reads unless told. */
  readonly defaultK: number;
}

/** What a database's ./orm entry gives ormOf beside its tables. */
export interface OrmDefinition {
  readonly clauses: Clauses;
  readonly nearest?: NearestDefinition;
  /** The rows findMany and findPage read unless told. */
  readonly defaultLimit: number;
  /** The CTE a versioned table's hard delete sets the history actor through, and the statement that clears it. */
  readonly historyActorCTE: string;
  readonly clearHistoryActor: string;
  /** Who asSystem() acts as: the DB service's outputs.sql.systemActor; DEFAULT_SYSTEM_ACTOR unless set (D87). */
  readonly systemActor?: SystemActor;
  /** An update of a table with updatedBy needs an acting user, as a create of a table with createdBy does (D87). */
  readonly requireUpdatedBy?: boolean;
  /** The tables, by the member of the ORM that holds each one's repository. */
  readonly tables: Readonly<Record<string, TableDefinition>>;
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

/** The order of findOne and findMany: a column's member, ascending or descending. */
export interface OrderBy<F extends string> {
  readonly field: F;
  readonly desc?: boolean;
}

/** The options of findOne and findMany. */
export interface FindOptions<F extends string> {
  /** The order, by each column in turn. */
  readonly orderBy?: readonly OrderBy<F>[];
  /** At most this many rows, 100 unless set; findMany alone. */
  readonly limit?: number;
  /** Skips this many rows first; findMany alone. */
  readonly offset?: number;
  /** Reads soft-deleted rows too. */
  readonly includeDeleted?: boolean;
}

/** The order findPage reads rows in: a sort field, ascending or descending; ties break by the key in the same direction. */
export interface PageSortOf<S extends string> {
  readonly field: S;
  readonly desc?: boolean;
}

/** The options of findPage. */
export interface PageOptions<S extends string, K> {
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

/** The repository of a table. */
export interface Repository<Row, Create, Update, Filter, OrderField extends string, SortField extends string, UniqueKey extends readonly string[], Key, PageKey> {
  /** The live row whose key is id; NotFoundError when there is none. */
  getOne(id: Key): Promise<Row>;
  /** The live rows whose keys are among ids, by key, in its wire form; a key no live row holds is absent. */
  getManyByIds(ids: readonly Key[]): Promise<Map<Key, Row>>;
  /** The first row filter finds, in the options' order, or null. */
  findOne(filter?: Filter | null, options?: FindOptions<OrderField>): Promise<Row | null>;
  /** The rows filter finds, in the options' order, a limit of them at a time, and how many it finds in all. */
  findMany(filter?: Filter | null, options?: FindOptions<OrderField>): Promise<{ rows: Row[]; total: number }>;
  /** A page of the rows filter finds, in the sort's order, after the key of the page before (D60, amended). */
  findPage(filter?: Filter | null, options?: PageOptions<SortField, PageKey>): Promise<RowPage<Row, PageKey>>;
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
  upsertOne(input: Create, on: UniqueKey, update: Update): Promise<Row>;
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

/** The options of nearest. */
export interface NearestOptions<Filter> {
  /** The most rows nearest reads, 1 or more; 10 unless set. */
  readonly k?: number;
  /** The metric; cosine unless set. */
  readonly metric?: VectorMetric;
  /** The rows nearest reads from, as findMany's filter keeps them. */
  readonly filter?: Filter | null;
  /** Reads soft-deleted rows too. */
  readonly includeDeleted?: boolean;
}

/** A row nearest read, and its distance from the vector by the metric. */
export interface Neighbour<Row> {
  readonly row: Row;
  readonly distance: number;
}

/** What the repository of a table with a vector column adds (D84). */
export interface VectorRepository<Row, Filter, VectorField extends string> {
  /**
   * The rows filter keeps whose field is nearest vector, by the metric,
   * nearest first, at most k of them, each with its distance. A vector
   * index serves the order and finds approximate neighbours; a filter that
   * keeps few rows may leave fewer than k. A row whose field is null has no
   * distance and is left out.
   */
  nearest(field: VectorField, vector: readonly number[], options?: NearestOptions<Filter>): Promise<Neighbour<Row>[]>;
}

/** The repository of a soft-deletable table, which can remove a row too. */
export interface SoftDeleteRepository<Row, Create, Update, Filter, OrderField extends string, SortField extends string, UniqueKey extends readonly string[], Key, PageKey>
  extends Repository<Row, Create, Update, Filter, OrderField, SortField, UniqueKey, Key, PageKey> {
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
  /** Runs fn in a transaction, with repositories bound to it, and commits; rolls back when fn throws. */
  transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>): Promise<T>;
}

/** An ORM: its repositories, by table, and the members of OrmMembers. */
export type Orm<R> = R & OrmMembers<R>;

/** A transaction: its repositories, bound to it, and raw SQL on it. */
export type OrmTx<R> = R &
  Omit<OrmMembers<R>, 'transaction'> & {
    /** Runs a statement in the transaction: a text and its values, or a pg query config, as enqueue passes one. */
    query<Row = Record<string, unknown>>(text: string | OrmQueryConfig, values?: readonly unknown[]): Promise<OrmQueryResult<Row>>;
    /** Runs a statement in the transaction and resolves to how many rows it changed. */
    exec(text: string, values?: readonly unknown[]): Promise<number>;
  };

/** The members a repository's table may not take, which an ORM holds itself. */
export const ORM_MEMBERS: readonly string[] = ['actingUser', 'withUser', 'as', 'asSystem', 'transaction', 'query', 'exec'];

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
}

async function run(runner: Runner, text: string, values: unknown[]): Promise<{ rows: Record<string, unknown>[]; rowCount: number }> {
  const result = await runner.queryable.query({ text, values, types: ormTypes });
  return { rows: result.rows as Record<string, unknown>[], rowCount: result.rowCount ?? 0 };
}

type Values = Record<string, unknown>;

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

  constructor(
    private readonly orm: OrmDefinition,
    private readonly def: TableDefinition,
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
    return conditions;
  }

  private whereOf(conditions: readonly string[]): string {
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

  async getOne(id: unknown): Promise<Values> {
    const s = this.def.statements;
    const { rows } = await run(this.runner, s.getOne + s.live, [this.encode(id, this.keyColumn(), 'key')]);
    if (rows.length === 0) throw new NotFoundError(`${this.def.name} ${String(id)} not found`);
    return this.decode(rows[0]!);
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

  async findOne(filter: Values | null | undefined, options: FindOptions<string> = {}): Promise<Values | null> {
    const args: unknown[] = [];
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, 0)), options.includeDeleted);
    const s = this.def.statements;
    const { rows } = await run(this.runner, fill(s.findOne, s.columns, where, this.orderBy(options.orderBy)), args);
    return rows.length === 0 ? null : this.decode(rows[0]!);
  }

  async findMany(filter: Values | null | undefined, options: FindOptions<string> = {}): Promise<{ rows: Values[]; total: number }> {
    const args: unknown[] = [];
    const where = this.liveWhere(this.whereOf(this.conditions(filter, args, 0)), options.includeDeleted);
    const c = this.clauses;
    const limit = options.limit !== undefined && options.limit > 0 ? options.limit : this.orm.defaultLimit;
    let limitOffset = fill(c.limit, limit);
    if (options.offset !== undefined && options.offset > 0) limitOffset += fill(c.offset, options.offset);
    const s = this.def.statements;
    // The count first, as the Go ORM runs it.
    const counted = await run(this.runner, fill(s.count, where), args);
    const total = Number((counted.rows[0] as { count?: unknown } | undefined)?.count ?? 0);
    const { rows } = await run(this.runner, fill(s.findMany, s.qualifiedColumns, where, this.orderBy(options.orderBy), limitOffset), args);
    return { rows: rows.map(row => this.decode(row)), total };
  }

  async findPage(filter: Values | null | undefined, options: PageOptions<string, Values> = {}): Promise<RowPage<Values, Values>> {
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
    const { rows } = await run(this.runner, fill(s.page, s.qualifiedColumns, this.whereOf(conditions), order, limit + 1), args);
    if (rows.length === 0 && options.after && !byKey) {
      // A key whose row no longer exists places no row: only then is the
      // page empty without being the last.
      const exists = await run(this.runner, s.pageKeyExists, [after]);
      if (!(exists.rows[0] as { exists?: unknown } | undefined)?.exists) {
        throw new PageKeyError(`no ${this.def.name} row has the key's ${this.def.key}`);
      }
    }
    const decoded = rows.map(row => this.decode(row));
    if (decoded.length <= limit) return { rows: decoded, next: null };
    const kept = decoded.slice(0, limit);
    return { rows: kept, next: { [this.def.key]: kept[limit - 1]![this.def.key] } };
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
    // The filter's conditions, then the soft-delete condition, then a
    // vector that may be null's presence; the vector is the last parameter,
    // as the Go ORM's Nearest writes it.
    const args: unknown[] = [];
    const conditions = this.conditions(options.filter, args, 0);
    if (this.def.softDelete && !options.includeDeleted) conditions.push(s.liveCondition);
    if (!column.required) conditions.push(fill(this.clauses.isNotNull, column.qualified));
    args.push(this.encode(vector, column, 'nearest'));
    const by = fill(distance, column.qualified, args.length);
    const { rows } = await run(this.runner, fill(s.nearest, s.qualifiedColumns, by, this.whereOf(conditions), by, k), args);
    return rows.map(row => ({ row: this.decode(row), distance: Number(row._distance) }));
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
    // Each row's cells in CreateMany's order: the fields, the key at its
    // place among them, then the relations; a column any row sets is
    // inserted, DEFAULT for a row that leaves it unset.
    const order = [
      ...this.def.columns.filter(c => !c.relation && (c.key || this.insertable(c))),
      ...this.def.columns.filter(c => c.relation),
    ];
    const rows = inputs.map(input => {
      const stamped = this.sealed(this.stamped(input), 'create');
      return order.map(column => this.insertValue(stamped, column));
    });
    const inserted = order.map((_, i) => rows.some(row => row[i]!.set));
    const columns = order.filter((_, i) => inserted[i]).map(c => c.column);
    const args: unknown[] = [];
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
    const s = this.def.statements;
    const result = await run(this.runner, fill(s.insertMany, columns.join(', '), tuples.join(', '), s.columns), args);
    return result.rows.map(row => this.decode(row));
  }

  /**
   * The assignments of update, each value appended to args, in UpdateOne's
   * order: the fields, the relations' keys. stamps adds updatedAt and
   * updatedBy, as UpdateOne writes them.
   */
  private setClauses(update: Values, args: unknown[], stamps: boolean): string[] {
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
        continue;
      }
      args.push(this.encode(value, column, 'update'));
      sets.push(fill(c.set, column.column, args.length));
    }
    if (stamps) this.stamp(sets, update, args);
    return sets;
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

  async upsertOne(input: Values, on: readonly string[], update: Values): Promise<Values> {
    const name = on.join(',');
    const key = this.def.uniqueKeys[name];
    if (!key) throw new Error(`orm: ${this.def.name} has no unique key ${JSON.stringify(name)}; its keys are ${Object.keys(this.def.uniqueKeys).join('; ')}`);
    const args: unknown[] = [];
    const sealed = this.sealed(this.stamped(input), 'upsert');
    const encrypted = this.setsEncrypted(update);
    if (encrypted && name !== this.def.key) {
      throw new Error(
        `orm: upsertOne of ${this.def.name} on ${name} cannot set ${encrypted.name}, an Encrypted<T> field: the row it updates has a key of its own, which its envelope is bound to; upsert on ${this.def.key}, or find the row and updateOne it`
      );
    }
    const { columns, placeholders } = this.insertOne(sealed, args);
    let sets = this.setClauses(this.sealedUpdate(update ?? {}, sealed[this.def.key], 'upsert'), args, false);
    if (sets.length === 0) {
      // An update that sets no field leaves the row as it is, stamps and
      // all, and returns it.
      sets = [key.keep];
    } else {
      this.stamp(sets, update, args);
    }
    const s = this.def.statements;
    const { rows } = await run(this.runner, fill(s.upsert, columns.join(', '), placeholders.join(', '), key.conflict, sets.join(', ')), args);
    if (rows.length === 0) throw new NotFoundError(`the ${this.def.name} row that holds the key ${name} is deleted`);
    return this.decode(rows[0]!);
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

function repositories(orm: OrmDefinition, runner: Runner, acting: Acting, keys: FieldKeyring | undefined): Record<string, TableRepository> {
  const out: Record<string, TableRepository> = {};
  for (const [member, def] of Object.entries(orm.tables)) {
    if (ORM_MEMBERS.includes(member)) throw new Error(`orm: the table ${def.name} would be the ORM's own member ${member}`);
    out[member] = new TableRepository(orm, def, runner, acting, keys);
  }
  return out;
}

function userOf(id: string | null | undefined): string | undefined {
  if (id === null || id === undefined || id === '') return undefined;
  if (uuidValue(id) === undefined) throw new Error(`orm: the acting user ${JSON.stringify(id)} is no UUID`);
  return id;
}

/** A transaction over client, acting as whom acting resolves to, with the field keys. */
function txOf<R>(orm: OrmDefinition, client: OrmQueryable, acting: Acting, keys: FieldKeyring | undefined): OrmTx<R> {
  const runner: Runner = { queryable: client, inTransaction: true };
  const query = async <Row>(text: string | OrmQueryConfig, values?: readonly unknown[]): Promise<OrmQueryResult<Row>> => {
    const config = typeof text === 'string' ? { text, values: [...(values ?? [])] } : { ...text, values: [...(text.values ?? values ?? [])] };
    const result = await client.query(config);
    return { rows: result.rows as Row[], rowCount: result.rowCount };
  };
  return {
    ...repositories(orm, runner, acting, keys),
    get actingUser() {
      return actingUser(acting);
    },
    withUser: (id: string | null | undefined) => txOf<R>(orm, client, userOf(id), keys),
    as: (principal: Actor | null | undefined) => txOf<R>(orm, client, userOf(principal?.subject), keys),
    asSystem: () => txOf<R>(orm, client, systemActorOf(orm).id, keys),
    query,
    exec: async (text: string, values?: readonly unknown[]) => (await query(text, values)).rowCount ?? 0,
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
  const runner: Runner = { queryable: pool, inTransaction: false };
  return {
    ...repositories(orm, runner, acting, keys),
    get actingUser() {
      return actingUser(acting);
    },
    withUser: (id: string | null | undefined) => ormWith<R>(pool, orm, userOf(id), keys),
    as: (principal: Actor | null | undefined) => ormWith<R>(pool, orm, userOf(principal?.subject ?? null), keys),
    asSystem: () => ormWith<R>(pool, orm, systemActorOf(orm).id, keys),
    async transaction<T>(fn: (tx: OrmTx<R>) => Promise<T>): Promise<T> {
      const client = await pool.connect();
      // A connection whose transaction could not be ended is not returned
      // to the pool.
      let broken: Error | undefined;
      try {
        await client.query({ text: 'BEGIN', values: [] });
        let out: T;
        try {
          out = await fn(txOf<R>(orm, client, acting, keys));
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
    },
  } as unknown as Orm<R>;
}
