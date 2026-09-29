/*
Storage owns one SQLite connection to one file. Opening it sets the busy
timeout, write-ahead logging and foreign keys, which SQLite ignores inside
a transaction, so they are set first. Writes run in transactions that
begin with BEGIN IMMEDIATE, which takes the write lock at once rather than
at the first write, so two writers queue on the busy timeout instead of
failing midway. A transaction called inside another becomes a savepoint:
it rolls back alone and leaves the outer one running.

A transaction is synchronous. Behavior code runs inside the write
transaction and cannot await (D16), so a function that returns a promise
rolls the transaction back and throws.

One process writes the file. SQLite serializes writers from several
processes, but the engine keeps per-process state, and nothing here
coordinates it across processes.
*/

import { openDriver, type DriverName, type Row, type RunResult, type SqlDriver, type SqlValue } from './driver.js';

export interface StorageOptions {
  /** The SQLite binding; `auto` (the default) picks bun:sqlite on Bun, node:sqlite elsewhere. */
  driver?: DriverName | 'auto';
  /** How long a statement waits for another connection's lock, in milliseconds (default 5000). */
  busyTimeoutMs?: number;
}

export const DEFAULT_BUSY_TIMEOUT_MS = 5000;

export class Storage {
  private depth = 0;

  private constructor(
    /** The database file. */
    readonly path: string,
    private readonly driver: SqlDriver
  ) {}

  /** open opens (creating if absent) the SQLite file at path. */
  static open(path: string, options: StorageOptions = {}): Storage {
    const busyTimeoutMs = options.busyTimeoutMs ?? DEFAULT_BUSY_TIMEOUT_MS;
    if (!Number.isInteger(busyTimeoutMs) || busyTimeoutMs < 0) {
      throw new TypeError(`busyTimeoutMs must be a non-negative integer, got ${String(busyTimeoutMs)}`);
    }
    const storage = new Storage(path, openDriver(path, options.driver ?? 'auto'));
    try {
      storage.exec(`PRAGMA busy_timeout = ${busyTimeoutMs}`);
      const mode = storage.get('PRAGMA journal_mode = WAL');
      if (String(mode?.journal_mode).toLowerCase() !== 'wal') {
        throw new Error(`${path}: SQLite would not use write-ahead logging (journal mode ${String(mode?.journal_mode)})`);
      }
      storage.exec('PRAGMA foreign_keys = ON');
    } catch (error) {
      storage.close();
      throw error;
    }
    return storage;
  }

  /** The SQLite binding in use. */
  get driverName(): DriverName {
    return this.driver.name;
  }

  /** Whether a transaction is open on this connection. */
  get inTransaction(): boolean {
    return this.depth > 0;
  }

  exec(sql: string): void {
    this.driver.exec(sql);
  }

  run(sql: string, params: readonly SqlValue[] = []): RunResult {
    return this.driver.run(sql, params);
  }

  get(sql: string, params: readonly SqlValue[] = []): Row | undefined {
    return this.driver.get(sql, params);
  }

  all(sql: string, params: readonly SqlValue[] = []): Row[] {
    return this.driver.all(sql, params);
  }

  /**
   * transaction runs fn in a transaction, commits when it returns and rolls
   * back when it throws. Called inside another transaction, it runs in a
   * savepoint.
   */
  transaction<T>(fn: () => T): T {
    const savepoint = `engine_savepoint_${this.depth}`;
    this.driver.exec(this.depth === 0 ? 'BEGIN IMMEDIATE' : `SAVEPOINT ${savepoint}`);
    this.depth += 1;
    let result: T;
    try {
      result = fn();
      if (isThenable(result)) {
        // The promise's own outcome no longer matters; keep a rejection
        // from surfacing as unhandled.
        (result as PromiseLike<unknown>).then(undefined, () => undefined);
        throw new TypeError('a storage transaction is synchronous: its function returned a promise');
      }
    } catch (error) {
      this.depth -= 1;
      this.rollback(savepoint);
      throw error;
    }
    this.depth -= 1;
    if (this.depth > 0) {
      this.driver.exec(`RELEASE ${savepoint}`);
      return result;
    }
    try {
      this.driver.exec('COMMIT');
    } catch (error) {
      this.rollback(savepoint);
      throw error;
    }
    return result;
  }

  close(): void {
    this.driver.close();
  }

  // A failed statement can end the transaction on its own (SQLite rolls
  // back on some errors), so a failing rollback does not hide the error
  // that caused it.
  private rollback(savepoint: string): void {
    try {
      if (this.depth === 0) {
        this.driver.exec('ROLLBACK');
      } else {
        this.driver.exec(`ROLLBACK TO ${savepoint}; RELEASE ${savepoint}`);
      }
    } catch {
      // The caller rethrows the original error.
    }
  }
}

function isThenable(value: unknown): boolean {
  return (
    (typeof value === 'object' || typeof value === 'function') &&
    value !== null &&
    typeof (value as { then?: unknown }).then === 'function'
  );
}
