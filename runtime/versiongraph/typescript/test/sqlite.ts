// What the SQLite tests share: a database opened through each binding, and a
// client that holds every statement to D16's rules for a behavior's SQL. It
// imports no SQLite module, so it loads under bun and Node alike; each test
// file hands it the module it opens databases with. Under Node it loads with
// Node's type stripping, so it uses no syntax that needs compiling.
import { sqlRefusal, type SqlMode } from "../../../engine/typescript/src/behaviors/sql.ts";
import {
  bunSqlite,
  nodeSqlite,
  type BunSqliteDatabase,
  type NodeSqliteDatabase,
  type SqliteClient,
  type SqliteRow,
  type SqliteRunResult,
  type SqliteValue,
} from "../dist/sqlite.js";

/** An open database: its client and how to close it. */
export interface Database {
  client: SqliteClient;
  close(): void;
}

/** A binding the tests open databases with: its name and how to open a file (":memory:" for none). */
export interface Binding {
  name: string;
  open(path: string): Database;
}

/** node:sqlite's DatabaseSync, as the tests construct it. */
export type NodeDatabaseClass = new (path: string) => NodeSqliteDatabase & { close(): void };

/** bun:sqlite's Database, as the tests construct it. */
export type BunDatabaseClass = new (path: string) => BunSqliteDatabase & { close(): void };

/** node:sqlite through nodeSqlite. */
export function nodeBinding(DatabaseSync: NodeDatabaseClass): Binding {
  return {
    name: "node:sqlite",
    open(path) {
      const db = new DatabaseSync(path);
      return { client: nodeSqlite(db), close: () => db.close() };
    },
  };
}

/** bun:sqlite through bunSqlite. */
export function bunBinding(Database: BunDatabaseClass): Binding {
  return {
    name: "bun:sqlite",
    open(path) {
      const db = new Database(path);
      return { client: bunSqlite(db), close: () => db.close() };
    },
  };
}

/** The prefix of the behavior a D16 engine would run the adapter as. */
export const behaviorPrefix = "bhv_branches__";

/** Names the layout's tables and indexes as a behavior's sql.table(name) does. */
export const behaviorTable = (name: string): string => behaviorPrefix + name;

/** A statement a checked client ran, and how. */
export interface Ran {
  method: "run" | "get" | "all";
  sql: string;
}

/**
 * A client over another that refuses, before it runs, every statement D16's
 * engine would refuse a behavior whose prefix is behaviorPrefix in mode, and
 * every exec, which a behavior's sql does not have, as D16's BehaviorSql
 * does. It records each statement it runs in ran.
 */
export function checkedClient(inner: SqliteClient, mode: SqlMode, ran: Ran[] = []): SqliteClient & { ran: Ran[] } {
  const check = (method: Ran["method"], sql: string): void => {
    const refusal = sqlRefusal(sql, behaviorPrefix, mode);
    if (refusal !== undefined) {
      throw new Error(`D16 refuses ${sql}: ${refusal}`);
    }
    if (method === "run" && mode === "read") {
      throw new Error(`D16 refuses run() in a read: ${sql}`);
    }
    ran.push({ method, sql });
  };
  return {
    ran,
    run(sql: string, params?: readonly SqliteValue[]): SqliteRunResult {
      check("run", sql);
      return inner.run(sql, params);
    },
    get(sql: string, params?: readonly SqliteValue[]): SqliteRow | undefined {
      check("get", sql);
      return inner.get(sql, params);
    },
    all(sql: string, params?: readonly SqliteValue[]): SqliteRow[] {
      check("all", sql);
      return inner.all(sql, params);
    },
  };
}

/**
 * Runs fn in a transaction the caller holds on the connection, as D16's
 * engine runs a behavior's operation: BEGIN IMMEDIATE, then COMMIT when fn
 * returns and ROLLBACK when it throws.
 */
export function inCallerTransaction<T>(client: SqliteClient, fn: () => T): T {
  client.exec!("BEGIN IMMEDIATE");
  let out: T;
  try {
    out = fn();
  } catch (err) {
    client.exec!("ROLLBACK");
    throw err;
  }
  client.exec!("COMMIT");
  return out;
}
