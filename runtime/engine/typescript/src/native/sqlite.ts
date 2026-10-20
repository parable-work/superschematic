/*
A connection of its own on the engine's SQLite file (engine.storage's
connection(), D58), for a store that runs its transactions statement by
statement (BEGIN IMMEDIATE, then COMMIT), as the HTTP runtime's identity
store does, which the engine's own connection, holding the engine's
transactions, cannot take. It is the runtime's built-in SQLite, neither of
them a native addon: node:sqlite (DatabaseSync) on Node.js and bun:sqlite
on Bun, opened as the engine opens the file: write-ahead logging, foreign
keys on and a busy timeout.

- A row is a plain object, and a missing row undefined (bun:sqlite returns
  null).
- A parameter is a string, a finite number, a bigint, a Uint8Array or
  null, checked before the statement runs.
- A failure SQLite reports is a SqliteError with its extended result code.
*/

import { createRequire } from 'node:module';

import { SqliteError } from '../errors.js';
import { DEFAULT_BUSY_TIMEOUT_MS, type Row, type RunResult, type SqlValue } from '../types/storage.js';

/** A SQLite connection: statements with `?` parameters, run synchronously. */
export interface SqliteConnection {
  /** Runs one or more statements with no parameters: DDL, pragmas, transaction control. */
  exec(sql: string): void;
  run(sql: string, params?: readonly SqlValue[]): RunResult;
  get(sql: string, params?: readonly SqlValue[]): Row | undefined;
  all(sql: string, params?: readonly SqlValue[]): Row[];
  close(): void;
}

interface RawStatement {
  run(...params: unknown[]): { changes: number | bigint; lastInsertRowid: number | bigint };
  get(...params: unknown[]): unknown;
  all(...params: unknown[]): unknown[];
}

interface RawDatabase {
  exec(sql: string): unknown;
  prepare(sql: string): RawStatement;
  close(): void;
}

type RawDatabaseConstructor = new (path: string) => RawDatabase;

const requireBuiltin = createRequire(import.meta.url);

function isBun(): boolean {
  return typeof (globalThis as { Bun?: unknown }).Bun !== 'undefined';
}

function database(): RawDatabaseConstructor {
  if (isBun()) {
    return (requireBuiltin('bun:sqlite') as { Database: RawDatabaseConstructor }).Database;
  }
  const module = requireBuiltin('node:sqlite') as { DatabaseSync?: RawDatabaseConstructor };
  if (typeof module.DatabaseSync !== 'function') {
    throw new Error('node:sqlite has no DatabaseSync in this runtime (Node.js 24 or later)');
  }
  return module.DatabaseSync;
}

/**
 * openConnection opens a connection on the SQLite file at path (creating it
 * if absent) as the engine opens it: write-ahead logging, foreign keys and
 * busyTimeoutMs (5000 by default).
 */
export function openConnection(path: string, busyTimeoutMs: number = DEFAULT_BUSY_TIMEOUT_MS): SqliteConnection {
  if (!Number.isInteger(busyTimeoutMs) || busyTimeoutMs < 0) {
    throw new TypeError(`busyTimeoutMs must be a non-negative integer, got ${String(busyTimeoutMs)}`);
  }
  const Database = database();
  let db: RawDatabase;
  try {
    db = new Database(path);
  } catch (error) {
    throw sqliteError(error);
  }
  const connection = new Connection(db);
  try {
    connection.exec(`PRAGMA busy_timeout = ${busyTimeoutMs}`);
    const mode = connection.get('PRAGMA journal_mode = WAL');
    if (String(mode?.journal_mode).toLowerCase() !== 'wal') {
      throw new Error(`${path}: SQLite would not use write-ahead logging (journal mode ${String(mode?.journal_mode)})`);
    }
    connection.exec('PRAGMA foreign_keys = ON');
  } catch (error) {
    connection.close();
    throw error;
  }
  return connection;
}

class Connection implements SqliteConnection {
  private readonly statements = new Map<string, RawStatement>();
  private closed = false;

  constructor(private readonly db: RawDatabase) {}

  exec(sql: string): void {
    this.open();
    try {
      this.db.exec(sql);
    } catch (error) {
      throw sqliteError(error);
    }
  }

  run(sql: string, params: readonly SqlValue[] = []): RunResult {
    const statement = this.prepare(sql);
    checkParams(params);
    try {
      const result = statement.run(...params);
      return { changes: Number(result.changes), lastInsertRowid: result.lastInsertRowid };
    } catch (error) {
      throw sqliteError(error);
    }
  }

  get(sql: string, params: readonly SqlValue[] = []): Row | undefined {
    const statement = this.prepare(sql);
    checkParams(params);
    let row: unknown;
    try {
      row = statement.get(...params);
    } catch (error) {
      throw sqliteError(error);
    }
    return row === null || row === undefined ? undefined : { ...(row as Row) };
  }

  all(sql: string, params: readonly SqlValue[] = []): Row[] {
    const statement = this.prepare(sql);
    checkParams(params);
    try {
      return statement.all(...params).map((row) => ({ ...(row as Row) }));
    } catch (error) {
      throw sqliteError(error);
    }
  }

  close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    this.statements.clear();
    try {
      this.db.close();
    } catch (error) {
      throw sqliteError(error);
    }
  }

  private open(): void {
    if (this.closed) {
      throw new Error('the SQLite connection is closed');
    }
  }

  // A statement is prepared once per SQL text and reused.
  private prepare(sql: string): RawStatement {
    this.open();
    const cached = this.statements.get(sql);
    if (cached !== undefined) {
      return cached;
    }
    let statement: RawStatement;
    try {
      statement = this.db.prepare(sql);
    } catch (error) {
      throw sqliteError(error);
    }
    this.statements.set(sql, statement);
    return statement;
  }
}

function checkParams(params: readonly SqlValue[]): void {
  for (let index = 0; index < params.length; index += 1) {
    const value: unknown = params[index];
    if (
      value === null ||
      typeof value === 'string' ||
      typeof value === 'bigint' ||
      value instanceof Uint8Array ||
      (typeof value === 'number' && Number.isFinite(value))
    ) {
      continue;
    }
    const kind = typeof value === 'number' ? String(value) : value === undefined ? 'undefined' : typeof value;
    throw new TypeError(`SQL parameter ${index + 1} is ${kind}: bind a string, a finite number, a bigint, a Uint8Array or null`);
  }
}

// node:sqlite throws an Error with code ERR_SQLITE_ERROR and the extended
// result code in errcode; bun:sqlite throws a SQLiteError with it in errno.
function sqliteError(error: unknown): unknown {
  if (!(error instanceof Error) || error instanceof SqliteError) {
    return error;
  }
  const fields = error as Error & { code?: unknown; errcode?: unknown; errno?: unknown };
  if (fields.code === 'ERR_SQLITE_ERROR' && typeof fields.errcode === 'number') {
    return new SqliteError(error.message, fields.errcode, { cause: error });
  }
  if (error.name === 'SQLiteError' && typeof fields.errno === 'number') {
    return new SqliteError(error.message, fields.errno, { cause: error });
  }
  return error;
}
