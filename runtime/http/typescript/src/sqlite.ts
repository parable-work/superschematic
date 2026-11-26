import { Database } from 'bun:sqlite';
import { configureSqlite, sqliteDatabaseOf, type OpenSqliteOptions, type SqliteDatabase, type SqliteDriver } from './sqlite-database.js';

// The queue client on SQLite (D53, amended): a worker's claims over the
// file a sqlite-first database is under stack dev, and the statement that
// enqueues a message in a batch.
export { ClaimLostError } from './queue-core.js';
export type { EnqueueOptions, Queue, QueueClaim, QueueDefinition, QueueField, QueueStatements } from './queue-core.js';
export { enqueueSqlite, enqueueStatement, sqliteQueue } from './queue-sqlite.js';
// The generated repositories over the file (D182), as ./d1 exports them, and
// the field keys' reader, which needs node:crypto, as Bun has it: under
// Bun, createOrm(db, { fieldKeys: parseFieldKeys(secret) }) opens a
// database's Encrypted<T> fields.
export { SQLITE_ORM_RUNTIME, sqliteOrm, sqliteOrmPool, type TransactionalSqlDatabase } from './orm-sqlite.js';
export { FieldKeyring, parseFieldKeys } from './fieldcrypt.js';

/*
SqlDatabase over a SQLite file through bun:sqlite (D64, section 18.13 of
docs/stack-model.md): under stack dev, a database whose DB service lists
sqlite first is a file the migration runner migrates, and the generated
entrypoint opens it with openSqlite, so Deps.db has on Bun the interface a
D1 binding gives it on Workers. This is the package's ./sqlite entry; it
runs under Bun alone. ./node-sqlite opens the same through node:sqlite,
and both are sqliteDatabaseOf over the driver (./sqlite-database), which
keeps D1's behaviors: placeholders ? and ?N, booleans bound as 1 and 0,
bind refusing undefined with D1's D1_TYPE_ERROR, a BLOB read back as an
Array of its bytes, foreign keys enforced, batch in one IMMEDIATE
transaction, the file in write-ahead logging, and exec running a script
one line per statement.
*/

export { configureSqlite, sqliteDatabaseOf, sqliteFile } from './sqlite-database.js';
export type { OpenSqliteOptions, SqliteDatabase, SqliteDriver, SqliteDriverStatement } from './sqlite-database.js';

/**
 * Opens the SQLite file at path through bun:sqlite, creating it when
 * missing, as a SqlDatabase: what a database whose DB service lists sqlite
 * first is under stack dev. Foreign keys are on, as D1 has them.
 */
export function openSqlite(path: string, options: OpenSqliteOptions = {}): SqliteDatabase {
  const db = new Database(path, { create: true });
  const driver: SqliteDriver = {
    prepare: sql => db.prepare(sql),
    exec: sql => db.run(sql),
    close: () => db.close(),
    get inTransaction() {
      return db.inTransaction;
    },
  };
  configureSqlite(driver, options);
  return sqliteDatabaseOf(driver, path);
}
