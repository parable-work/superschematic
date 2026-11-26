import type { SqlDatabase, SqlValue } from './d1.js';
import { ormOn, type Clauses, type JsonClauses, type Orm, type OrmDefinition, type OrmDialect, type OrmOptions, type OrmPool, type OrmQueryable, type OrmStatement, type TableDefinition, type TableStatements, type TransactionOptions } from './orm.js';
import { sqliteRulesStore } from './rules.js';
import { sqliteDecodeRow, sqliteDecodeValue, sqliteEncodeParam } from './sqlitecodec.js';
import { inTurn, takeTurn } from './sqlite-turns.js';

// What a repository on SQLite throws, and the core's binding a generated
// orm.ts of a DB service that lists no postgres passes its core through.
export { AccessRefusedError, MergeError, NoUserError, NotFoundError, PageKeyError, TooManyRowsError } from './orm.js';
export { withRulesCore } from './rules.js';

/*
The generated repositories on SQLite and D1 (D116, D79 amended): the
repositories ormOf runs on Postgres, over a SqlDatabase, the interface a D1
binding, openSqlite (./sqlite, bun:sqlite) and openNodeSqlite
(./node-sqlite, node:sqlite) share. A DB service that lists the sqlite
dialect carries each table's statements on SQLite beside Postgres's in its
types package's ./orm entry, which sqlutil writes from the one table model
both come from, as it writes them for the Rust repositories; this module
reads them in Postgres's place (onSQLite) and runs them with SQLite's value
codec (./sqlitecodec), so a repository answers on SQLite what it answers on
Postgres: TestTypeScriptRepositoryResultsOnSQLiteAndPostgres holds them to
it, call for call.

sqliteOrmOf opens the repositories over a file. A transaction is SQLite's:
BEGIN IMMEDIATE, which takes the write lock as it begins, COMMIT, and a
nested one a savepoint. The connection holds one transaction at a time, so
the repositories over one database take turns: a transaction holds the
database until it ends, and a statement outside one waits for it, whether
it is the ORM's or another ORM's over the same database. SQLite runs every
transaction serializable, so an isolation is held whatever it names; it
has no read-only transaction, which is refused.

d1OrmOf opens them over a D1 binding, which has no interactive
transaction: a batch is its one atomic unit. So on D1:
  - each write of one statement runs alone, and is atomic;
  - a write of several statements that are all known before the first runs
    goes as one batch: createMany's insert of each row, an upsert's read of
    whether a row holds its key with the upsert itself, and the change set
    of a table that composes behaviors, which the behaviors' adapter applies
    in one batch fenced on each row's version (D103);
  - a write whose later statement needs an earlier one's answer is refused
    by name: an upsertOne that sets an Encrypted<T> field on the row it
    updates, and runEvent, whose handler's writes and the event's spend
    would commit apart;
  - transaction(fn) is refused: an implementation that needs writes of its
    own to commit together batches them on the binding, Deps.db.
A read of several statements, findMany's count and its rows, runs them one
after the other, each seeing what was committed when it ran.

What SQLite has no form of is refused on both. The build refuses a table
SQLite holds no repository of, naming why (sqlutil.SQLiteTableRefusal: a
BYTEA column, a name SQLite reserves; and a table whose rows keep to an
audience, D156), and its repository throws that at its first statement; the
SQLite DDL refuses for the whole service a @versioned table, a projection,
a vector and a column SQLite has no storage for, POINT among them. At run
time upsertMany (D89), mergeInto (D147) and declared queries (D86) are not
built on SQLite and are refused.
*/

/** A statement's rows and how many rows it changed, or for a read how many it read, as pg counts them. */
async function runOn(db: SqlDatabase, text: string, values: readonly unknown[]): Promise<{ rows: Record<string, unknown>[]; rowCount: number }> {
  const { results, meta } = await db
    .prepare(text)
    .bind(...(values as SqlValue[]))
    .all<Record<string, unknown>>();
  return { rows: results, rowCount: meta.changes || results.length };
}

/** What the repositories run a statement on over a SqlDatabase: the database, and whether a statement takes a turn on it (./sqlite-turns), as a file's outside a transaction does, and none on D1 or in a transaction, which holds its turn. */
class SqlQueryable implements OrmQueryable {
  constructor(
    readonly db: SqlDatabase,
    private readonly turns = false
  ) {}

  async query(config: { text: string; values: unknown[]; types?: unknown }): Promise<{ rows: unknown[]; rowCount: number | null }> {
    return this.inTurn(() => runOn(this.db, config.text, config.values ?? []));
  }

  /** Runs fn in a turn of its own, or at once where the queryable takes none. */
  async inTurn<T>(fn: () => Promise<T>): Promise<T> {
    return this.turns ? inTurn(this.db, fn) : fn();
  }
}

/** The SqlQueryable a repository runs on, which every queryable this module hands it is. */
function sqlQueryable(queryable: OrmQueryable): SqlQueryable {
  if (!(queryable instanceof SqlQueryable)) throw new Error('orm: the SQLite repositories run over a SqlDatabase, through sqliteOrmOf or d1OrmOf');
  return queryable;
}

/** Runs statements as one batch: all of them or none. */
async function batchOn(queryable: OrmQueryable, statements: readonly OrmStatement[]): Promise<{ rows: Record<string, unknown>[]; rowCount: number }[]> {
  const q = sqlQueryable(queryable);
  return q.inTurn(async () => {
    const results = await q.db.batch<Record<string, unknown>>(statements.map(statement => q.db.prepare(statement.text).bind(...(statement.values as SqlValue[]))));
    return results.map(({ results: rows, meta }) => ({ rows, rowCount: meta.changes || rows.length }));
  });
}

/** The statement that begins a transaction on SQLite: IMMEDIATE, which takes the write lock as it begins. */
function sqliteBegin(options: TransactionOptions = {}): string {
  if (options.readOnly) {
    throw new Error('orm: a transaction on SQLite cannot begin read only, which SQLite has no form of; begin it without readOnly');
  }
  // SQLite runs every transaction serializable, which holds any isolation
  // a transaction names.
  return 'BEGIN IMMEDIATE';
}

/** The dialect of the repositories on a SQLite file. */
const SQLITE_DIALECT: OrmDialect = {
  name: 'sqlite',
  encodeParam: sqliteEncodeParam,
  decodeRow: sqliteDecodeRow,
  decodeValue: sqliteDecodeValue,
  begin: sqliteBegin,
  rulesStore: queryable => sqliteRulesStore(sqlQueryable(queryable).db),
  batch: batchOn,
};

/** Why D1 opens no transaction, which transaction(fn) and a write that needs one throw. */
export const D1_NO_TRANSACTIONS =
  'D1 has no interactive transaction: each repository write is atomic by itself, one of several statements as one batch, and writes of your own that must commit together go as one batch on the binding, Deps.db (D116)';

/** The dialect of the repositories on a D1 binding. */
const D1_DIALECT: OrmDialect = {
  ...SQLITE_DIALECT,
  begin: () => {
    throw new Error(`orm: ${D1_NO_TRANSACTIONS}`);
  },
  noTransactions: D1_NO_TRANSACTIONS,
};

/** A table's statements that throw why SQLite holds no repository of it, at the first one its repository reads. */
function refusedStatements(table: TableDefinition, why: string): TableStatements {
  return new Proxy({} as TableStatements, {
    get(_, member) {
      if (typeof member === 'symbol') return undefined;
      throw new Error(`orm: ${table.name} has no repository on SQLite: ${why}`);
    },
  });
}

/**
 * The definition orm as it runs on SQLite (D116): each table's statements
 * and unique keys on SQLite, which its types package carries beside
 * Postgres's, and the clauses and JSON conditions there, with what SQLite
 * runs none of left out: a table's merge (D147), the vectors' nearest
 * (D84), the declared queries (D86) and the history actor of a @versioned
 * table, which the SQLite DDL refuses. A table SQLite holds no repository
 * of keeps its columns, and its repository throws why at its first
 * statement. Throws for a definition written for a DB service that lists no
 * sqlite dialect.
 */
export function onSQLite(orm: OrmDefinition): OrmDefinition {
  const sqlite = orm.sqlite;
  if (sqlite === undefined) {
    throw new Error("orm: the definition holds no statements on SQLite: its DB service lists no sqlite dialect in outputs.sql.dialects, or its types package was built before the SQLite repositories; list sqlite and build it again (D116)");
  }
  const tables: Record<string, TableDefinition> = {};
  for (const [member, table] of Object.entries(orm.tables)) {
    const { merge: _merge, sqlite: own, historyActor: _historyActor, ...rest } = table;
    if (own === undefined || own.statements === undefined) {
      const why = own?.unsupported ?? 'its types carry no statements of it on SQLite, as of a projection, which the SQLite DDL does not hold';
      tables[member] = { ...rest, statements: refusedStatements(table, why), uniqueKeys: {} };
      continue;
    }
    tables[member] = { ...rest, statements: own.statements, uniqueKeys: own.uniqueKeys ?? {} };
  }
  const { sqlite: _sqlite, nearest: _nearest, queries: _queries, ...rest } = orm;
  const clauses: Clauses = sqlite.clauses;
  const jsonClauses: JsonClauses = sqlite.jsonClauses;
  return { ...rest, clauses, jsonClauses, tables };
}

/** The definitions onSQLite made, by the definition they were made of, so a definition is read once. */
const onSQLiteOf = new WeakMap<OrmDefinition, OrmDefinition>();

function sqliteDefinition(orm: OrmDefinition): OrmDefinition {
  let out = onSQLiteOf.get(orm);
  if (out === undefined) {
    out = onSQLite(orm);
    onSQLiteOf.set(orm, out);
  }
  return out;
}

/** The connection of a file's transaction, which holds the database's turn until it is released. */
class HeldQueryable extends SqlQueryable {
  constructor(
    db: SqlDatabase,
    readonly release: (err?: Error | boolean) => void
  ) {
    super(db);
  }
}

/** A pool over a SqlDatabase of a file: its statements, and a transaction of its own on the one connection, each in its turn. */
class FilePool extends SqlQueryable implements OrmPool {
  constructor(db: SqlDatabase) {
    super(db, true);
  }

  async connect(): Promise<HeldQueryable> {
    const end = await takeTurn(this.db);
    return new HeldQueryable(this.db, () => end());
  }
}

/** A pool over a D1 binding: its statements, and no transaction. */
class D1Pool extends SqlQueryable implements OrmPool {
  constructor(db: SqlDatabase) {
    super(db);
  }

  connect(): Promise<HeldQueryable> {
    return Promise.reject(new Error(`orm: ${D1_NO_TRANSACTIONS}`));
  }
}

/**
 * The repositories of a database's tables over db, a SQLite file opened
 * with openSqlite or openNodeSqlite, as its types package's ./orm entry
 * describes them, acting as the user options names, as ormOf does on
 * Postgres. The generated createSqliteOrm calls it.
 */
export function sqliteOrmOf<R>(db: SqlDatabase, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  return ormOn<R>(new FilePool(db), sqliteDefinition(orm), SQLITE_DIALECT, options);
}

/**
 * The repositories of a database's tables over db, a D1 binding, which has
 * no interactive transaction: transaction(fn) is refused, and a repository's
 * write of several statements goes as one batch. The generated createD1Orm
 * calls it.
 */
export function d1OrmOf<R>(db: SqlDatabase, orm: OrmDefinition, options?: string | null | OrmOptions): Orm<R> {
  return ormOn<R>(new D1Pool(db), sqliteDefinition(orm), D1_DIALECT, options);
}
