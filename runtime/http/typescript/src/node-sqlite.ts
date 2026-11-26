import { DatabaseSync } from 'node:sqlite';
import { configureSqlite, sqliteDatabaseOf, type OpenSqliteOptions, type SqliteDatabase, type SqliteDriver } from './sqlite-database.js';

/*
SqlDatabase over a SQLite file through node:sqlite (D64): what openSqlite
(./sqlite) gives over bun:sqlite, on Node.js 24 or later, and on Bun,
which has node:sqlite too. Both are sqliteDatabaseOf over the connection
(./sqlite-database), so the queue client on SQLite, the behaviors' adapter
and the repositories (./orm-sqlite) run on either alike. This is the
package's ./node-sqlite entry.
*/

export { configureSqlite, sqliteDatabaseOf, sqliteFile } from './sqlite-database.js';
export type { OpenSqliteOptions, SqliteDatabase, SqliteDriver, SqliteDriverStatement } from './sqlite-database.js';
export { ClaimLostError } from './queue-core.js';
export type { EnqueueOptions, Queue, QueueClaim, QueueDefinition, QueueField, QueueStatements } from './queue-core.js';
export { enqueueSqlite, enqueueStatement, sqliteQueue } from './queue-sqlite.js';

/**
 * Opens the SQLite file at path through node:sqlite, creating it when
 * missing, as a SqlDatabase. Foreign keys are on, as D1 has them.
 */
export function openNodeSqlite(path: string, options: OpenSqliteOptions = {}): SqliteDatabase {
  const db = new DatabaseSync(path);
  const driver: SqliteDriver = {
    prepare: sql => db.prepare(sql),
    exec: sql => db.exec(sql),
    close: () => db.close(),
    get isTransaction() {
      return db.isTransaction;
    },
  };
  configureSqlite(driver, options);
  return sqliteDatabaseOf(driver, path);
}
