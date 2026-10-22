/*
The database of a TypeScript API whose DB service lists sqlite first (D64,
section 18.6 of docs/stack-model.md): SqlDatabase, the runtime's copy of
D1's interface, which the generated Deps.db holds. A D1 binding provides it
on Workers, and openSqlite (./sqlite) provides it over bun:sqlite under
stack dev, so an implementation runs on both unchanged.

The interface is D1's: prepare(sql).bind(...values) and then first, all,
run or raw; batch, which runs statements in one transaction, atomically;
and exec, which runs the statements of a script, one per line, unbound. A
statement's placeholders are ? and ?N, which D1 and SQLite both read. D1
has no interactive transaction: batch is the only way to make several
statements atomic, and an implementation that needs one writes it so.

Values convert as D1 converts them, on both. bind takes a string, a number,
a boolean (stored as 1 or 0), null, or bytes (an ArrayBuffer or a view of
one), and throws D1's error, D1_TYPE_ERROR: Type 'undefined' not supported
for value 'undefined', for undefined or any other value: an optional field
binds as `value ?? null`. A row reads back a BLOB as an Array of its bytes,
not a Uint8Array (new Uint8Array(row.image) makes one), a boolean as 1 or
0, and an INTEGER as a number.

This is the package's ./d1 entry. It imports nothing from node: or a
driver, so a Worker bundles it. It exports the enqueue of the queue client
on SQLite (queue-sqlite.ts, D53, amended): enqueueStatement, whose
statement a Worker adds to the batch whose writes caused the message. It
exports no claims: no worker claims on D1 (PlatformSpec.NoQueueClaims), and
a Worker is no process that claims until it is stopped.
*/

export type { QueueDefinition, QueueField, QueueStatements } from './queue-core.js';
export { enqueueSqlite, enqueueStatement } from './queue-sqlite.js';

/**
 * A value a statement binds: what D1 and SQLite store. A boolean binds as 1
 * or 0, and bytes as a BLOB, which a row reads back as an Array of numbers.
 * bind throws D1_TYPE_ERROR for undefined.
 */
export type SqlValue = string | number | boolean | null | ArrayBuffer | Uint8Array;

/** What a statement's run reports beside its rows, as D1 reports it. */
export interface SqlMeta {
  /** How long the statement took, in milliseconds. */
  readonly duration: number;
  /** The rows the statement inserted, updated or deleted. */
  readonly changes: number;
  /** The rowid of the last row inserted on the connection. */
  readonly last_row_id: number;
  /** Whether the statement changed the database. */
  readonly changed_db: boolean;
  /** Further members D1 reports: rows_read, rows_written, size_after, served_by. */
  readonly [member: string]: unknown;
}

/** The rows a statement answered, and what its run reports. */
export interface SqlResult<T = Record<string, unknown>> {
  readonly results: T[];
  readonly success: true;
  readonly meta: SqlMeta;
}

/** What exec reports: the statements it ran, and how long they took. */
export interface SqlExecResult {
  readonly count: number;
  readonly duration: number;
}

/** A prepared statement: bind returns the statement with its values bound. */
export interface SqlPreparedStatement {
  /** The statement with values bound to its placeholders, in order: ? takes the next, ?N the Nth. Throws D1_TYPE_ERROR for undefined. */
  bind(...values: SqlValue[]): SqlPreparedStatement;
  /** The first row, or null when there is none. */
  first<T = Record<string, unknown>>(): Promise<T | null>;
  /** The named column of the first row, or null when there is no row. Rejects for a column the row lacks. */
  first<T = unknown>(column: string): Promise<T | null>;
  /** Every row, with what the run reports. */
  all<T = Record<string, unknown>>(): Promise<SqlResult<T>>;
  /** Runs the statement: its rows, which a statement without RETURNING has none of, and what the run reports. */
  run<T = Record<string, unknown>>(): Promise<SqlResult<T>>;
  /** Every row as an array of its columns' values. */
  raw<T = unknown[]>(options?: { readonly columnNames?: false }): Promise<T[]>;
  /** The column names, then every row as an array of its columns' values. */
  raw<T = unknown[]>(options: { readonly columnNames: true }): Promise<[string[], ...T[]]>;
}

/** D1's interface: what Deps.db is for a database whose DB service lists sqlite first. */
export interface SqlDatabase {
  /** A statement over sql, whose placeholders bind later. */
  prepare(sql: string): SqlPreparedStatement;
  /** Runs statements in order in one transaction: every one, or none when one fails. */
  batch<T = Record<string, unknown>>(statements: readonly SqlPreparedStatement[]): Promise<SqlResult<T>[]>;
  /** Runs a script's statements, one per line, with no values bound. */
  exec(sql: string): Promise<SqlExecResult>;
}

/** How long a ping waits for its answer, in milliseconds: what /readyz allows a database. */
export const PING_TIMEOUT_MS = 2000;

/**
 * Resolves when db answers SELECT 1 within timeoutMs, and rejects
 * otherwise: what a server's /readyz asks of each SqlDatabase, as it asks
 * each pg Pool (./postgres's ping).
 */
export async function ping(db: SqlDatabase, timeoutMs: number = PING_TIMEOUT_MS): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`the database did not answer within ${timeoutMs} ms`)), timeoutMs);
  });
  try {
    const one = await Promise.race([db.prepare('SELECT 1 AS one').first<number>('one'), timeout]);
    if (one !== 1) throw new Error(`the database answered SELECT 1 with ${JSON.stringify(one)}`);
  } finally {
    clearTimeout(timer);
  }
}

/** Whether value has the methods of a SqlDatabase: what a D1 binding is. */
export function isSqlDatabase(value: unknown): value is SqlDatabase {
  if (typeof value !== 'object' || value === null) return false;
  const db = value as Partial<Record<keyof SqlDatabase, unknown>>;
  return typeof db.prepare === 'function' && typeof db.batch === 'function' && typeof db.exec === 'function';
}
