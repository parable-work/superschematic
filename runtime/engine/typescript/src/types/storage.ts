/*
The values of the engine's SQLite file, as engine.storage's raw SQL binds
and reads them (D58): the deployment's reach into the file, for its tests'
looks at the tables and an identity store's DDL.
*/

/** A value bound to a `?` placeholder or read from a column. */
export type SqlValue = string | number | bigint | Uint8Array | null;

/** A row as plain data, keyed by column name. */
export type Row = Record<string, SqlValue>;

/** The outcome of a statement that writes. */
export interface RunResult {
  changes: number;
  lastInsertRowid: number | bigint;
}

/** How long a write waits for another connection's lock before the engine refuses it busy, in milliseconds (EngineOptions.busyTimeoutMs). */
export const DEFAULT_BUSY_TIMEOUT_MS = 5000;
