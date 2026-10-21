import { Database, type Statement } from 'bun:sqlite';
import type { SqlDatabase, SqlExecResult, SqlMeta, SqlPreparedStatement, SqlResult, SqlValue } from './d1.js';

/*
SqlDatabase over a SQLite file through bun:sqlite (D64, section 18.13 of
docs/stack-model.md): under stack dev, a database whose DB service lists
sqlite first is a file the migration runner migrates, and the generated
entrypoint opens it with openSqlite, so Deps.db has on Bun the interface a
D1 binding gives it on Workers. This is the package's ./sqlite entry; it
runs under Bun alone.

It keeps D1's behaviors that an implementation could come to rely on:
placeholders ? and ?N, booleans bound as 1 and 0, bind refusing undefined
and any other value D1 refuses with D1's D1_TYPE_ERROR, a BLOB read back as
an Array of its bytes rather than bun:sqlite's Uint8Array, foreign keys
enforced, batch in one transaction that rolls back when a statement fails,
and exec running a script one line per statement. Each run reports changes,
the last row id and its duration, as D1's meta does.
*/

/** A SqlDatabase over an open SQLite file, which close closes. */
export interface SqliteDatabase extends SqlDatabase {
  /** The file it opened. */
  readonly path: string;
  /** Closes the connection; the database answers no statement after. */
  close(): void;
}

/** What openSqlite sets beside the file. */
export interface OpenSqliteOptions {
  /** How long a statement waits for another connection's lock, in milliseconds; 5000 unless set. */
  readonly busyTimeoutMs?: number;
}

/**
 * A value as bun:sqlite binds it, converted as D1's bind converts it: a
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

/** A column's value as D1 answers it: a BLOB, which bun:sqlite reads as a Uint8Array, as an Array of its bytes. */
function readable(value: unknown): unknown {
  return value instanceof Uint8Array ? Array.from(value) : value;
}

/** A row's columns as D1 answers them. */
function readableRow(row: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [column, value] of Object.entries(row)) out[column] = readable(value);
  return out;
}

class SqliteStatement implements SqlPreparedStatement {
  constructor(
    readonly owner: SqliteConnection,
    readonly sql: string,
    /** The values bound, as bun:sqlite binds them. */
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
    const rows = statement.values(...this.params()).map(row => row.map(readable)) as T[];
    return options?.columnNames ? [[...statement.columnNames], ...rows] : rows;
  }

  params(): unknown[] {
    return [...this.values];
  }
}

class SqliteConnection implements SqliteDatabase {
  private readonly db: Database;
  private readonly statements = new Map<string, Statement>();
  private closed = false;

  constructor(
    readonly path: string,
    options: OpenSqliteOptions
  ) {
    this.db = new Database(path, { create: true });
    this.db.run('PRAGMA foreign_keys = ON');
    this.db.run(`PRAGMA busy_timeout = ${Math.max(0, Math.trunc(options.busyTimeoutMs ?? 5000))}`);
  }

  /** The prepared statement of sql, prepared once. */
  statement(sql: string): Statement {
    if (this.closed) throw new Error(`the SQLite database ${this.path} is closed`);
    let statement = this.statements.get(sql);
    if (statement === undefined) {
      statement = this.db.prepare(sql);
      this.statements.set(sql, statement);
    }
    return statement;
  }

  /** Runs a statement, its rows and what the run reports. */
  execute<T>(statement: SqliteStatement): SqlResult<T> {
    const started = performance.now();
    const before = this.totalChanges();
    const prepared = this.statement(statement.sql);
    let results: T[] = [];
    if (prepared.columnNames.length > 0) {
      results = prepared.all(...statement.params()).map(row => readableRow(row as Record<string, unknown>) as T);
    } else {
      prepared.run(...statement.params());
    }
    const changes = this.totalChanges() - before;
    const { id } = this.statement('SELECT last_insert_rowid() AS id').get() as { id: number | bigint };
    const meta: SqlMeta = { duration: performance.now() - started, changes, last_row_id: Number(id), changed_db: changes > 0 };
    return { results, success: true, meta };
  }

  private totalChanges(): number {
    return Number((this.statement('SELECT total_changes() AS n').get() as { n: number | bigint }).n);
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
    return this.db.transaction(() => own.map(statement => this.execute<T>(statement)))();
  }

  async exec(sql: string): Promise<SqlExecResult> {
    if (this.closed) throw new Error(`the SQLite database ${this.path} is closed`);
    const started = performance.now();
    let count = 0;
    for (const line of sql.split('\n')) {
      if (line.trim() === '') continue;
      this.db.run(line);
      count++;
    }
    return { count, duration: performance.now() - started };
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    this.statements.clear();
    this.db.close();
  }
}

/**
 * Opens the SQLite file at path, creating it when missing, as a
 * SqlDatabase: what a database whose DB service lists sqlite first is under
 * stack dev. Foreign keys are on, as D1 has them.
 */
export function openSqlite(path: string, options: OpenSqliteOptions = {}): SqliteDatabase {
  return new SqliteConnection(path, options);
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
