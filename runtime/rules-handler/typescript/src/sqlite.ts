// @superschematic/rules-handler/sqlite: development mode on bun:sqlite
// (D117), a SQLite file or a database in memory (":memory:") that goes with
// its process, with the tables superschematic-rules-dev's SqliteStore
// creates, so a file one host wrote the other reads. Bun alone runs this
// entry; the package's entry imports nothing and takes any store.

import { Database } from 'bun:sqlite';
import { Dev, type DevOptions } from './dev.js';
import type { Codec } from './json.js';
import { JSON_CODEC } from './json.js';
import { SqliteStore, type SqliteConnection } from './store.js';
import type { HandlerModule } from './wasm.js';

/** A SQLite connection that can be closed. */
export type ClosableConnection = SqliteConnection & { close(): void };

/** A bun:sqlite database: a file, created when it is not there, or ":memory:". */
export function openSqlite(path = ':memory:'): ClosableConnection {
  return new Database(path, { create: true });
}

/** The development layout in a SQLite file or in ":memory:", over bun:sqlite. */
export function sqliteStore(path: string, handler: HandlerModule, codec: Codec = JSON_CODEC): SqliteStore {
  return new SqliteStore(openSqlite(path), handler.layout(), codec);
}

/**
 * The development host over a SQLite file, or a database in memory when
 * path is ":memory:" or absent, running the behaviors ported onto the core.
 */
export async function openDev(path = ':memory:', options: DevOptions = {}): Promise<Dev> {
  return Dev.open((layout, codec) => new SqliteStore(openSqlite(path), layout, codec), options);
}
