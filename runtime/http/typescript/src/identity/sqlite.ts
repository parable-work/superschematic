import { SqlIdentityStore, driveSync, type SqlParam, type SqlRow, type SqlRunner, type SqlWork } from './sqlstore.js';

/*
The identity store's SQLite binding. It reaches SQLite through
SqliteClient, a small synchronous interface (run, get, all and exec with
positional ? parameters), the shape of the engine's SqlDriver, which
serves as one as it is. nodeSqlite and bunSqlite bind an already-open
node:sqlite DatabaseSync or bun:sqlite Database; they use only the methods
they call, so this module imports no SQLite module and loads without one.

Every operation runs synchronously, its transaction included (BEGIN
IMMEDIATE to COMMIT), so nothing else on the event loop runs a statement
on the connection while it is open, and an operation never waits on
another. The connection should have foreign_keys on and a busy_timeout;
the store deletes a role's grants itself, so it does not rely on the
cascade.
*/

/** A value bound to a placeholder or read from a column. */
export type SqliteValue = string | number | bigint | Uint8Array | null;

/** Runs statements on one SQLite connection, synchronously. get returns undefined for no row, and a row is a plain object. */
export interface SqliteClient {
  run(sql: string, params?: readonly SqliteValue[]): { changes: number | bigint };
  get(sql: string, params?: readonly SqliteValue[]): Record<string, SqliteValue> | undefined;
  all(sql: string, params?: readonly SqliteValue[]): Record<string, SqliteValue>[];
  exec(sql: string): void;
}

/** A prepared statement of node:sqlite or bun:sqlite, as the bindings call it. */
export interface SqliteModuleStatement {
  run(...params: SqliteValue[]): { changes: number | bigint };
  get(...params: SqliteValue[]): unknown;
  all(...params: SqliteValue[]): unknown[];
}

/** What the bindings need of a node:sqlite DatabaseSync or a bun:sqlite Database. */
export interface SqliteModuleDatabase {
  prepare(sql: string): SqliteModuleStatement;
  exec(sql: string): unknown;
}

/**
 * A client over an open node:sqlite DatabaseSync or bun:sqlite Database. It
 * prepares each statement once, returns plain rows (node:sqlite's have no
 * prototype) and undefined for no row (bun:sqlite's get returns null).
 */
function moduleClient(db: SqliteModuleDatabase): SqliteClient {
  const statements = new Map<string, SqliteModuleStatement>();
  const prepare = (sql: string): SqliteModuleStatement => {
    let statement = statements.get(sql);
    if (statement === undefined) {
      statement = db.prepare(sql);
      statements.set(sql, statement);
    }
    return statement;
  };
  const plain = (row: unknown): Record<string, SqliteValue> => ({ ...(row as Record<string, SqliteValue>) });
  return {
    exec(sql) {
      db.exec(sql);
    },
    run(sql, params = []) {
      return { changes: prepare(sql).run(...params).changes };
    },
    get(sql, params = []) {
      const row = prepare(sql).get(...params);
      return row === null || row === undefined ? undefined : plain(row);
    },
    all(sql, params = []) {
      return prepare(sql).all(...params).map(plain);
    },
  };
}

/** A client over an open node:sqlite DatabaseSync. */
export function nodeSqlite(db: SqliteModuleDatabase): SqliteClient {
  return moduleClient(db);
}

/** A client over an open bun:sqlite Database. */
export function bunSqlite(db: SqliteModuleDatabase): SqliteClient {
  return moduleClient(db);
}

/** The SqlRunner over a SqliteClient: every operation runs synchronously, in one transaction when it asks for one. */
export function sqliteRunner(client: SqliteClient): SqlRunner {
  const execute = (statement: { kind: 'rows' | 'exec'; sql: string; params: readonly SqlParam[] }): SqlRow[] | number => {
    if (statement.kind === 'rows') return client.all(statement.sql, statement.params);
    return Number(client.run(statement.sql, statement.params).changes);
  };
  return {
    dialect: 'sqlite',
    run<T>(work: () => SqlWork<T>, transaction: boolean): Promise<T> {
      try {
        if (!transaction) return Promise.resolve(driveSync(work(), execute));
        client.exec('BEGIN IMMEDIATE');
        try {
          const out = driveSync(work(), execute);
          client.exec('COMMIT');
          return Promise.resolve(out);
        } catch (error) {
          try {
            client.exec('ROLLBACK');
          } catch {
            // The error that ended the transaction is the one to report.
          }
          throw error;
        }
      } catch (error) {
        return Promise.reject(error);
      }
    },
  };
}

/** The identity store over a SQLite connection holding the schema's tables. */
export function sqliteIdentityStore(client: SqliteClient, descriptor: string | unknown): SqlIdentityStore {
  return new SqlIdentityStore(sqliteRunner(client), descriptor);
}
