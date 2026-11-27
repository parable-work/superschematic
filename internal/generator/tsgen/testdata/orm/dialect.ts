// What a test of a platform feature that runs on Postgres and on SQLite
// shares (D189): the notifier's, the inbound path's and the agent
// dispatcher's end-to-end tests run their cases on either, as ORM_DIALECT
// says. On Postgres the database is a schema of its own with the
// package's create.sql applied (harness.ts); on SQLite it is a file of its
// own that the package's sqlite/create.sql made, opened as openSqlite opens
// a server's file under stack dev, the ORM the ./orm/sqlite entry's. A
// statement the test runs itself is written in Postgres's words and run in
// the dialect's: $n as ?n, and now() as SQLite's clock.
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type pg from 'pg';
import { postgresQueue, type Queue, type QueueDefinition } from '@superschematic/http-runtime/postgres';
import { database, dsn } from './harness';

/** Whether the test runs on SQLite. */
export const onSQLite = process.env.ORM_DIALECT === 'sqlite';

/** Whether the test has a database to run on: a file always, Postgres with its URL. */
export const available = onSQLite || dsn !== '';

/** A test's database in its dialect. */
export interface FeatureDatabase {
  /** What the ORM and a worker's queue run over: the pool, or the file's SqlDatabase. */
  readonly source: any;
  /** The pool, on Postgres. */
  readonly pool?: pg.Pool;
  /** The file's path, on SQLite. */
  readonly file?: string;
  /** Runs a statement written in Postgres's words: its rows, and how many rows it changed. */
  raw(text: string, values?: readonly unknown[]): Promise<{ rows: Record<string, any>[]; rowCount: number }>;
  /** A queue's claims over the database. */
  queue<M>(definition: QueueDefinition<M>): Queue<M>;
  /** The identity store over the database, as identity's postgres or sqlite store opens it. */
  identityStore(descriptor: unknown): Promise<any>;
  /** Closes the connections and removes the database. */
  drop(): Promise<void>;
}

/** A statement's text in SQLite's words: Postgres's $n as ?n, and its clock. */
export function sqliteText(text: string): string {
  return text.replace(/\$(\d+)/gu, '?$1').replaceAll('now()', "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')");
}

/** A database of its own in the test's dialect, prefix naming it. */
export async function featureDatabase(prefix: string): Promise<FeatureDatabase> {
  if (!onSQLite) {
    const db = await database(prefix);
    return {
      source: db.pool,
      pool: db.pool,
      async raw(text, values = []) {
        const result = await db.pool.query(text, [...values]);
        return { rows: result.rows, rowCount: result.rowCount ?? 0 };
      },
      queue: definition => postgresQueue(db.pool, definition),
      async identityStore(descriptor) {
        const { postgresIdentityStore } = await import('@superschematic/http-runtime/identity');
        return postgresIdentityStore(db.pool, descriptor);
      },
      drop: () => db.drop(),
    };
  }
  const { Database } = await import('bun:sqlite');
  const { openSqlite, sqliteQueue } = await import('@superschematic/http-runtime/sqlite');
  const dir = mkdtempSync(join(tmpdir(), `${prefix}-`));
  const file = join(dir, `${prefix}.sqlite`);
  const made = new Database(file, { create: true });
  made.exec(readFileSync(new URL('./sqlite/create.sql', import.meta.url), 'utf8'));
  made.close();
  const sqlite = openSqlite(file);
  const others: { close(): void }[] = [];
  return {
    source: sqlite,
    file,
    async raw(text, values = []) {
      const result = await sqlite.prepare(sqliteText(text)).bind(...(values as never[])).all<Record<string, any>>();
      return { rows: result.results, rowCount: result.meta.changes };
    },
    queue: definition => sqliteQueue(sqlite, definition),
    async identityStore(descriptor) {
      const { bunSqlite, sqliteIdentityStore } = await import('@superschematic/http-runtime/identity');
      const connection = new Database(file);
      connection.exec('PRAGMA busy_timeout = 5000');
      others.push(connection);
      return sqliteIdentityStore(bunSqlite(connection as never), descriptor);
    },
    async drop() {
      for (const other of others) other.close();
      sqlite.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The package's createOrm in the test's dialect: the ./orm entry's over a pool, the ./orm/sqlite entry's over a file. */
export async function ormEntry<T>(): Promise<{ createOrm(source: any, options?: any): T }> {
  return (onSQLite ? await import('./orm-sqlite' as string) : await import('./orm' as string)) as { createOrm(source: any, options?: any): T };
}
