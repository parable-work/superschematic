/*
The SQLite driver seam. The engine reaches SQLite only through SqlDriver, a
small synchronous interface. Two adapters implement it over the JavaScript
runtimes' built-in modules, neither of them a native addon: node:sqlite
(DatabaseSync) on Node.js and bun:sqlite on Bun. Bun also ships node:sqlite,
so on Bun either adapter opens a file; on Node.js only node:sqlite does.

The modules differ, and the adapters even that out:

- A row is a plain object. node:sqlite returns rows with a null prototype.
- A missing row is undefined. bun:sqlite returns null.
- A bound value is a string, a finite number, a bigint, a Uint8Array or
  null, checked before the statement runs. bun:sqlite binds a boolean as 1
  and node:sqlite refuses it; both refuse it here.
- A failure SQLite reports is a SqliteError carrying SQLite's extended
  result code (5 is SQLITE_BUSY, 2067 SQLITE_CONSTRAINT_UNIQUE).

An integer column reads as a JavaScript number. Past 2^53, node:sqlite
throws and bun:sqlite rounds; the engine keeps its integers below that.
*/

import { createRequire } from 'node:module';

/** A value bound to a `?` placeholder or read from a column. */
export type SqlValue = string | number | bigint | Uint8Array | null;

/** A row as plain data, keyed by column name. */
export type Row = Record<string, SqlValue>;

/** The outcome of a statement that writes. */
export interface RunResult {
  changes: number;
  lastInsertRowid: number | bigint;
}

/** The SQLite binding an adapter wraps: `node` is node:sqlite, `bun` is bun:sqlite. */
export type DriverName = 'node' | 'bun';

/** The synchronous surface the engine runs SQL through. */
export interface SqlDriver {
  readonly name: DriverName;
  /** Runs one or more statements with no parameters: DDL, pragmas, transaction control. */
  exec(sql: string): void;
  run(sql: string, params?: readonly SqlValue[]): RunResult;
  get(sql: string, params?: readonly SqlValue[]): Row | undefined;
  all(sql: string, params?: readonly SqlValue[]): Row[];
  close(): void;
}

/** A failure SQLite reported, with its extended result code. */
export class SqliteError extends Error {
  readonly code: number;

  constructor(message: string, code: number, options?: { cause?: unknown }) {
    super(message, options);
    this.name = 'SqliteError';
    this.code = code;
  }
}

/** SQLITE_BUSY: another connection holds the lock past the busy timeout. */
export const SQLITE_BUSY = 5;

// The structural subset of DatabaseSync and bun:sqlite's Database the
// adapters call.
interface RawStatement {
  run(...params: unknown[]): { changes: number | bigint; lastInsertRowid: number | bigint };
  get(...params: unknown[]): unknown;
  all(...params: unknown[]): unknown[];
}

interface RawDatabase {
  prepare(sql: string): RawStatement;
  exec(sql: string): unknown;
  close(): void;
}

type RawDatabaseConstructor = new (path: string) => RawDatabase;

const requireBuiltin = createRequire(import.meta.url);

/** Whether this process runs on Bun. */
export function isBun(): boolean {
  return typeof (globalThis as { Bun?: unknown }).Bun !== 'undefined';
}

/**
 * openDriver opens (creating if absent) the SQLite file at path with the
 * named adapter. `auto`, the default, picks bun:sqlite on Bun and
 * node:sqlite elsewhere.
 */
export function openDriver(path: string, choice: DriverName | 'auto' = 'auto'): SqlDriver {
  let name: DriverName;
  if (choice === 'auto') {
    name = isBun() ? 'bun' : 'node';
  } else if (choice === 'node' || choice === 'bun') {
    name = choice;
  } else {
    throw new TypeError(`unknown SQLite driver "${String(choice)}": expected "node", "bun" or "auto"`);
  }
  const Database = loadDatabase(name);
  let db: RawDatabase;
  try {
    db = new Database(path);
  } catch (error) {
    throw asSqliteError(error);
  }
  return new Adapter(name, db);
}

function loadDatabase(name: DriverName): RawDatabaseConstructor {
  if (name === 'bun') {
    if (!isBun()) {
      throw new Error('the "bun" SQLite driver (bun:sqlite) needs Bun; on Node.js use "node" (node:sqlite)');
    }
    return (requireBuiltin('bun:sqlite') as { Database: RawDatabaseConstructor }).Database;
  }
  let module: { DatabaseSync?: RawDatabaseConstructor };
  try {
    module = requireBuiltin('node:sqlite') as { DatabaseSync?: RawDatabaseConstructor };
  } catch (error) {
    throw new Error('the "node" SQLite driver needs node:sqlite (Node.js 24 or later, or Bun)', { cause: error });
  }
  if (typeof module.DatabaseSync !== 'function') {
    throw new Error('node:sqlite has no DatabaseSync in this runtime');
  }
  return module.DatabaseSync;
}

class Adapter implements SqlDriver {
  private readonly statements = new Map<string, RawStatement>();
  private closed = false;

  constructor(
    readonly name: DriverName,
    private readonly db: RawDatabase
  ) {}

  exec(sql: string): void {
    this.open();
    try {
      this.db.exec(sql);
    } catch (error) {
      throw asSqliteError(error);
    }
  }

  run(sql: string, params: readonly SqlValue[] = []): RunResult {
    const statement = this.prepare(sql);
    checkParams(params);
    try {
      const result = statement.run(...params);
      return { changes: Number(result.changes), lastInsertRowid: result.lastInsertRowid };
    } catch (error) {
      throw asSqliteError(error);
    }
  }

  get(sql: string, params: readonly SqlValue[] = []): Row | undefined {
    const statement = this.prepare(sql);
    checkParams(params);
    let row: unknown;
    try {
      row = statement.get(...params);
    } catch (error) {
      throw asSqliteError(error);
    }
    if (row === null || row === undefined) {
      return undefined;
    }
    return { ...(row as Row) };
  }

  all(sql: string, params: readonly SqlValue[] = []): Row[] {
    const statement = this.prepare(sql);
    checkParams(params);
    let rows: unknown[];
    try {
      rows = statement.all(...params);
    } catch (error) {
      throw asSqliteError(error);
    }
    return rows.map((row) => ({ ...(row as Row) }));
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
      throw asSqliteError(error);
    }
  }

  private open(): void {
    if (this.closed) {
      throw new Error('the SQLite connection is closed');
    }
  }

  // Statements are prepared once per SQL text and reused.
  private prepare(sql: string): RawStatement {
    this.open();
    const cached = this.statements.get(sql);
    if (cached) {
      return cached;
    }
    let statement: RawStatement;
    try {
      statement = this.db.prepare(sql);
    } catch (error) {
      throw asSqliteError(error);
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
    throw new TypeError(
      `SQL parameter ${index + 1} is ${kind}: bind a string, a finite number, a bigint, a Uint8Array or null`
    );
  }
}

// node:sqlite throws an Error with code ERR_SQLITE_ERROR and the extended
// result code in errcode; bun:sqlite throws a SQLiteError with it in errno.
function asSqliteError(error: unknown): unknown {
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
