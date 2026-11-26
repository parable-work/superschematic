import type { SqlDatabase, SqlExecResult, SqlMeta, SqlPreparedStatement, SqlResult, SqlValue } from './d1.js';

/*
SqlDatabase over an open SQLite connection, of bun:sqlite or of node:sqlite
(D64, section 18.13 of docs/stack-model.md): what a database whose DB
service lists sqlite first is under stack dev, and on any host that keeps
it in a file. openSqlite (./sqlite) opens a file through bun:sqlite and
openNodeSqlite (./node-sqlite) through node:sqlite, and both hand the
connection to sqliteDatabaseOf, so Deps.db has on Bun and on Node.js the
interface a D1 binding gives it on Workers. This module imports neither
driver: it calls the methods both connections have, and reads the one
each names differently (bun's columnNames and inTransaction, node's
columns() and isTransaction).

It keeps D1's behaviors that an implementation could come to rely on:
placeholders ? and ?N, booleans bound as 1 and 0, bind refusing undefined
and any other value D1 refuses with D1's D1_TYPE_ERROR, a BLOB read back as
an Array of its bytes rather than the driver's Uint8Array, foreign keys
enforced, batch in one IMMEDIATE transaction, which takes the write lock
as it begins and rolls back when a statement fails, and in a savepoint
when the connection is in a transaction already, so a batch inside a
transaction its caller began is the caller's, the file in write-ahead
logging, and exec running a script one line per statement. Each run
reports changes, the last row id and its duration, as D1's meta does.

Unlike D1, the connection holds an interactive transaction: a caller may
exec BEGIN and COMMIT on it. The repositories over a file do (./orm-sqlite),
one transaction at a time on the connection.
*/

/** A prepared statement of bun:sqlite or node:sqlite, as sqliteDatabaseOf calls it. */
export interface SqliteDriverStatement {
  all(...params: unknown[]): unknown[];
  get(...params: unknown[]): unknown;
  /** bun:sqlite's: every row as an array of its columns' values. */
  values?(...params: unknown[]): unknown[][];
  /** bun:sqlite's: the columns the statement returns. */
  readonly columnNames?: readonly string[];
  /** node:sqlite's: the columns the statement returns. */
  columns?(): readonly { readonly name: string }[];
}

/** An open connection of bun:sqlite (Database) or node:sqlite (DatabaseSync), as sqliteDatabaseOf calls it. */
export interface SqliteDriver {
  prepare(sql: string): SqliteDriverStatement;
  /** Runs a statement with no values bound. bun:sqlite's Database names it run as well. */
  exec(sql: string): unknown;
  close(): void;
  /** bun:sqlite's: whether a transaction is open. */
  readonly inTransaction?: boolean;
  /** node:sqlite's: whether a transaction is open. */
  readonly isTransaction?: boolean;
}

/** A SqlDatabase over an open SQLite file, which close closes. */
export interface SqliteDatabase extends SqlDatabase {
  /** It runs BEGIN and SAVEPOINT, which D1 does not, so the repositories run their transactions on it (D182). */
  readonly beginsTransactions: true;
  /** The file it opened. */
  readonly path: string;
  /** Closes the connection; the database answers no statement after. */
  close(): void;
}

/** What opening a file sets beside the file. */
export interface OpenSqliteOptions {
  /** How long a statement waits for another connection's lock, in milliseconds; 5000 unless set. */
  readonly busyTimeoutMs?: number;
}

/**
 * Sets what every connection to a sqlite-first database's file runs with:
 * foreign keys on, as D1 has them, the busy timeout, and write-ahead
 * logging, in which a reader never waits for a writer, as a server and a
 * worker on one file do (D53, amended). The journal mode is the file's, so
 * every connection after the first finds it set.
 */
export function configureSqlite(driver: Pick<SqliteDriver, 'exec'>, options: OpenSqliteOptions = {}): void {
  driver.exec('PRAGMA foreign_keys = ON');
  driver.exec(`PRAGMA busy_timeout = ${Math.max(0, Math.trunc(options.busyTimeoutMs ?? 5000))}`);
  driver.exec('PRAGMA journal_mode = WAL');
}

/**
 * A value as the driver binds it, converted as D1's bind converts it: a
 * boolean as 1 or 0, an ArrayBuffer, a view of one or an Array of bytes as
 * those bytes. Throws D1's D1_TYPE_ERROR for any other value, undefined
 * among them, which bun:sqlite would bind as NULL and D1 refuses.
 */
function bindable(value: unknown): unknown {
  switch (typeof value) {
    case 'number':
    case 'string':
      return value;
    case 'boolean':
      return value ? 1 : 0;
    case 'object':
      if (value === null) return null;
      if (value instanceof ArrayBuffer) return new Uint8Array(value);
      if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
      if (Array.isArray(value) && value.every(b => typeof b === 'number' && Number.isInteger(b) && b >= 0 && b < 256)) return new Uint8Array(value);
  }
  throw new Error(`D1_TYPE_ERROR: Type '${typeof value}' not supported for value '${String(value)}'`);
}

/** A column's value as D1 answers it: a BLOB, which the driver reads as a Uint8Array, as an Array of its bytes. */
function readable(value: unknown): unknown {
  return value instanceof Uint8Array ? Array.from(value) : value;
}

/** A row's columns as D1 answers them, a plain object (node:sqlite's rows have no prototype). */
function readableRow(row: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [column, value] of Object.entries(row)) out[column] = readable(value);
  return out;
}

/** The columns a statement returns, by either driver's name for them. */
function columnNamesOf(statement: SqliteDriverStatement): readonly string[] {
  if (statement.columnNames !== undefined) return statement.columnNames;
  if (typeof statement.columns === 'function') return statement.columns().map(column => column.name);
  return [];
}

class SqliteStatement implements SqlPreparedStatement {
  constructor(
    readonly owner: SqliteConnection,
    readonly sql: string,
    /** The values bound, as the driver binds them. */
    readonly values: readonly unknown[] = []
  ) {}

  /** Throws, as D1's bind does, for a value D1 refuses. */
  bind(...values: SqlValue[]): SqlPreparedStatement {
    return new SqliteStatement(this.owner, this.sql, values.map(bindable));
  }

  first<T = Record<string, unknown>>(): Promise<T | null>;
  first<T = unknown>(column: string): Promise<T | null>;
  async first<T>(column?: string): Promise<T | null> {
    const row = this.owner.statement(this.sql).get(...this.params()) as Record<string, unknown> | null | undefined;
    if (row === null || row === undefined) return null;
    if (column === undefined) return readableRow(row) as T;
    if (!(column in row)) {
      throw new Error(`the row of ${JSON.stringify(this.sql)} has no column ${column}; it has ${Object.keys(row).join(', ')}`);
    }
    return readable(row[column]) as T;
  }

  async all<T = Record<string, unknown>>(): Promise<SqlResult<T>> {
    return this.owner.execute<T>(this);
  }

  async run<T = Record<string, unknown>>(): Promise<SqlResult<T>> {
    return this.owner.execute<T>(this);
  }

  raw<T = unknown[]>(options?: { readonly columnNames?: false }): Promise<T[]>;
  raw<T = unknown[]>(options: { readonly columnNames: true }): Promise<[string[], ...T[]]>;
  async raw<T = unknown[]>(options?: { readonly columnNames?: boolean }): Promise<T[] | [string[], ...T[]]> {
    const statement = this.owner.statement(this.sql);
    // bun:sqlite reads each row as an array; node:sqlite's row object holds
    // its columns in order.
    const arrays = typeof statement.values === 'function' ? statement.values(...this.params()) : statement.all(...this.params()).map(row => Object.values(row as Record<string, unknown>));
    const rows = arrays.map(row => row.map(readable)) as T[];
    return options?.columnNames ? [[...columnNamesOf(statement)], ...rows] : rows;
  }

  params(): unknown[] {
    return [...this.values];
  }
}

class SqliteConnection implements SqliteDatabase {
  // It runs BEGIN and SAVEPOINT, so the repositories run their transactions
  // on it (sqliteOrmPool, D182).
  readonly beginsTransactions = true as const;
  private readonly statements = new Map<string, SqliteDriverStatement>();
  private closed = false;
  /** How many batches are open in savepoints, which names each one. */
  private depth = 0;

  constructor(
    private readonly driver: SqliteDriver,
    readonly path: string
  ) {}

  /** The prepared statement of sql, prepared once. */
  statement(sql: string): SqliteDriverStatement {
    if (this.closed) throw new Error(`the SQLite database ${this.path} is closed`);
    let statement = this.statements.get(sql);
    if (statement === undefined) {
      statement = this.driver.prepare(sql);
      this.statements.set(sql, statement);
    }
    return statement;
  }

  /** Runs a statement, its rows and what the run reports. */
  execute<T>(statement: SqliteStatement): SqlResult<T> {
    const started = performance.now();
    const before = this.totalChanges();
    const prepared = this.statement(statement.sql);
    // A statement with no columns answers no rows; all runs it either way.
    const results = prepared.all(...statement.params()).map(row => readableRow(row as Record<string, unknown>) as T);
    // The rows the statement itself changed, as D1 counts them: changes(),
    // which leaves out a trigger's writes, once total_changes() says the
    // statement wrote at all (changes() still holds the last write's count
    // after a read).
    const changes = this.totalChanges() === before ? 0 : Number((this.statement('SELECT changes() AS n').get() as { n: number | bigint }).n);
    const { id } = this.statement('SELECT last_insert_rowid() AS id').get() as { id: number | bigint };
    const meta: SqlMeta = { duration: performance.now() - started, changes, last_row_id: Number(id), changed_db: changes > 0 };
    return { results, success: true, meta };
  }

  private totalChanges(): number {
    return Number((this.statement('SELECT total_changes() AS n').get() as { n: number | bigint }).n);
  }

  /** Whether the connection is in a transaction, by either driver's name for it. */
  private inTransaction(): boolean {
    return Boolean(this.driver.inTransaction ?? this.driver.isTransaction);
  }

  prepare(sql: string): SqlPreparedStatement {
    return new SqliteStatement(this, sql);
  }

  async batch<T = Record<string, unknown>>(statements: readonly SqlPreparedStatement[]): Promise<SqlResult<T>[]> {
    const own = statements.map((statement, i) => {
      if (!(statement instanceof SqliteStatement) || statement.owner !== this) {
        throw new Error(`batch: statement ${i} was not prepared on the SQLite database ${this.path}`);
      }
      return statement;
    });
    if (this.closed) throw new Error(`the SQLite database ${this.path} is closed`);
    // IMMEDIATE: the batch takes the write lock as it begins, waiting for
    // it within the busy timeout, so a batch that reads before it writes
    // never fails to upgrade its lock while another connection writes. In
    // a transaction its caller began, the batch is a savepoint of it.
    const nested = this.inTransaction();
    const savepoint = `batch_${++this.depth}`;
    this.driver.exec(nested ? `SAVEPOINT ${savepoint}` : 'BEGIN IMMEDIATE');
    try {
      const results = own.map(statement => this.execute<T>(statement));
      this.driver.exec(nested ? `RELEASE ${savepoint}` : 'COMMIT');
      return results;
    } catch (error) {
      try {
        if (nested) {
          this.driver.exec(`ROLLBACK TO ${savepoint}`);
          this.driver.exec(`RELEASE ${savepoint}`);
        } else if (this.inTransaction()) {
          this.driver.exec('ROLLBACK');
        }
      } catch {
        // The error that failed the batch is the one thrown.
      }
      throw error;
    } finally {
      this.depth--;
    }
  }

  async exec(sql: string): Promise<SqlExecResult> {
    if (this.closed) throw new Error(`the SQLite database ${this.path} is closed`);
    const started = performance.now();
    let count = 0;
    for (const line of sql.split('\n')) {
      if (line.trim() === '') continue;
      this.driver.exec(line);
      count++;
    }
    return { count, duration: performance.now() - started };
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.statements.clear();
    this.driver.close();
  }
}

/**
 * A SqlDatabase over driver, an open bun:sqlite Database or node:sqlite
 * DatabaseSync, which close closes; path names its file in messages. The
 * caller sets the connection's pragmas (configureSqlite), as openSqlite and
 * openNodeSqlite do.
 */
export function sqliteDatabaseOf(driver: SqliteDriver, path: string): SqliteDatabase {
  return new SqliteConnection(driver, path);
}

/** A sqlite: URL: sqlite:PATH, or sqlite://PATH. */
const SQLITE_URL = /^sqlite:(?:\/\/)?([^?#]+)$/iu;

/**
 * The file a sqlite: URL names: sqlite:PATH and sqlite://PATH are PATH
 * (sqlite:///tmp/shop.sqlite is /tmp/shop.sqlite), as the migration runner
 * reads them. The local target derives a sqlite-first database's
 * connection as such a URL, to a file under the environment's state
 * directory (D64, section 18.13), and the generated entrypoint opens the
 * file it names with openSqlite. Throws for any other URL, naming its
 * scheme alone, since a connection string may hold a password.
 */
export function sqliteFile(url: string): string {
  const match = SQLITE_URL.exec(url);
  if (match === null) {
    const scheme = /^([a-z][a-z0-9+.-]*):/iu.exec(url)?.[1];
    const what = scheme === undefined ? 'no URL' : scheme.toLowerCase() === 'sqlite' ? 'a sqlite: URL with a query or no path' : `a ${scheme}: URL`;
    throw new Error(`the database's URL is ${what}; a sqlite-first database's is sqlite:<path>`);
  }
  return match[1]!;
}
