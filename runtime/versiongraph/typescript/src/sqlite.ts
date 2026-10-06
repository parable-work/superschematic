// The SQLite storage adapter of the version-graph engine (D32): SyncStorage
// and SyncTx over one fixed layout of tables, the same for every graph, so
// a SyncEngine runs a graph in a SQLite file. postgres.ts is its Postgres
// counterpart, over the tables sqlgen generates per graph.
//
// The layout is nine STRICT tables: ref, ref_history, commit, patch,
// snapshot_entry, release, release_history, member and member_history, each
// named by a function the caller gives (defaultTableName puts graph_ before
// each), as is every index. Every row carries its graph's name, so one file
// holds several graphs, and every statement is scoped to the adapter's
// graph. An id is TEXT holding a UUID in its canonical form (base62); a
// version, a sequence and a tombstone are INTEGER; the times of refs,
// commits, release pointers and history images are INTEGER microseconds
// since the Unix epoch, returned as canonical date-times. A member row holds
// its kind and its role columns as columns and every other column of the
// descriptor as one canonical JSON object, data. Foreign keys check every
// edge inside the layout; there is no root table, so nothing checks a root
// but the adapter, which refuses a write whose ref or commit is another
// graph's or another root's.
//
// SQLite has no triggers a behavior may declare and none that can assign
// NEW, so the adapter does what Postgres's triggers do, in the statements of
// the transaction that changes a row: it sets _version (1 on an insert, the
// old version plus 1 on an update), writes the row's image at its new
// version on an insert or an update, and on a delete writes the row's image
// at the old version plus 1 with the kind's history actor column set to the
// delete's actor. An image leaves out the kind's history-excluded columns,
// and reads back as it was stored, while a live row reads with every column
// its kind declares. Refs and release pointers keep history too. Every value
// it writes is canonicalized by its class first (canonical.ts), so a read
// returns what is stored and needs no rules of its own.
//
// The adapter reaches SQLite through SqliteClient, a small synchronous
// interface of the shape of D16's SqlDriver: run, get and all with
// positional parameters, and exec for transaction control. nodeSqlite and
// bunSqlite bind an already-open node:sqlite DatabaseSync or bun:sqlite
// Database; they use only the methods they call, so this module imports no
// SQLite module and loads without one. createTables and storage refuse a
// SQLite older than 3.37.0, the first with STRICT tables, and one that
// cannot run json_each and json_extract (minSqliteVersion).

import { canonicalOf, uuidCanonical } from "./canonical.js";
import type { Descriptor } from "./contract.js";
import { compareCodePoints, isJsonObject, parseJson, stringifyJson, writeJsonString } from "./json.js";
import {
  NameTakenError,
  NotFoundError,
  VersionConflictError,
  type Commit,
  type CommitNode,
  type NewCommit,
  type NewRef,
  type Patch,
  type Pin,
  type Ref,
  type RefUpdate,
  type Release,
  type ReleaseWrite,
  type RowWrite,
  type SnapshotEntry,
  type SyncStorage,
  type SyncTx,
} from "./storage.js";

/** A value bound to a placeholder or read from a column. */
export type SqliteValue = string | number | bigint | Uint8Array | null;

/** A row a query returned, keyed by column name. */
export type SqliteRow = Record<string, SqliteValue>;

/** What a statement that writes reports: how many rows it changed. */
export interface SqliteRunResult {
  changes: number | bigint;
}

/**
 * Runs the adapter's statements on one SQLite connection, synchronously.
 * run, get and all take positional parameters for the statement's numbered
 * placeholders (?1, ?2, ...), one value for each. get returns undefined for
 * no row, and a row is a plain object. A failure SQLite reports is thrown
 * as an error whose `code` is SQLite's extended result code, a number (2067
 * is SQLITE_CONSTRAINT_UNIQUE), as SqliteError and D16's driver carry it.
 * exec runs a statement with no parameters and returns nothing; the adapter
 * calls it only for transaction control and connection settings, and never
 * in the caller's transaction, so a client without it serves there.
 */
export interface SqliteClient {
  run(sql: string, params?: readonly SqliteValue[]): SqliteRunResult;
  get(sql: string, params?: readonly SqliteValue[]): SqliteRow | undefined;
  all(sql: string, params?: readonly SqliteValue[]): SqliteRow[];
  exec?(sql: string): void;
}

/** A failure SQLite reported, with its extended result code. */
export class SqliteError extends Error {
  /** SQLite's extended result code: 5 is SQLITE_BUSY, 2067 SQLITE_CONSTRAINT_UNIQUE. */
  readonly code: number;

  constructor(message: string, code: number, options?: { cause?: unknown }) {
    super(message, options);
    this.name = "SqliteError";
    this.code = code;
  }
}

/** SQLITE_BUSY: another connection holds the lock past the busy timeout. */
export const SQLITE_BUSY = 5;

/** SQLITE_CONSTRAINT_UNIQUE: a unique index refused a row. */
export const SQLITE_CONSTRAINT_UNIQUE = 2067;

/**
 * The oldest SQLite the adapter runs on: 3.37.0, the first with the STRICT
 * tables its layout declares. Its statements need nothing later: RETURNING
 * came in 3.35.0, and json_each and json_extract, which its reads take lists
 * through, are built in from 3.38.0 and in 3.37 builds with JSON1.
 */
export const minSqliteVersion = "3.37.0";

/** A statement only a SQLite with json_each and json_extract runs, giving 1. */
const jsonProbe = "SELECT json_extract(p.value, '$[0]') AS one FROM json_each('[[1]]') AS p";

/** A SQLite version's numbers, major, minor and patch; undefined for text that is not one. */
function versionParts(version: string): [number, number, number] | undefined {
  const m = /^([0-9]+)\.([0-9]+)\.([0-9]+)/.exec(version);
  return m === null ? undefined : [Number(m[1]), Number(m[2]), Number(m[3])];
}

/** The clients on a connection of the adapter's own whose SQLite passed checkSqlite. */
const checkedClients = new WeakSet<SqliteClient>();

/**
 * The layouts, by their tables' names, whose SQLite passed checkSqlite in a
 * caller's transaction. A host such as D16's Branches binds a new adapter
 * over a new client for each call, all on one connection, so a check kept
 * by client would run on every call. What the check reads is the SQLite
 * library's, so this assumes one SQLite library serves every connection
 * that uses a layout name in this process, as D16's engine does. A second
 * library without the JSON functions under the same names would pass
 * unchecked and fail at its first json_each statement, with SQLite's own
 * error.
 */
const checkedLayouts = new Set<string>();

/**
 * Refuses a SQLite the adapter cannot run on: one older than
 * minSqliteVersion, and one that cannot run json_each and json_extract. On a
 * connection of its own the adapter reads the version with sqlite_version().
 * In the caller's transaction it checks the JSON functions only: D16 refuses
 * a behavior's statement that names sqlite_version, and D16's engine, whose
 * own tables are STRICT, already needs 3.37.0. A check that passes is kept,
 * by client on a connection of the adapter's own and by layout in a
 * caller's transaction, so it runs once; one that fails is not kept.
 */
function checkSqlite(config: AdapterConfig, client: SqliteClient): void {
  const layout = config.callerTransaction ? Object.values(config.tables).join("\u0000") : undefined;
  if (layout !== undefined ? checkedLayouts.has(layout) : checkedClients.has(client)) {
    return;
  }
  let version: string | undefined;
  if (!config.callerTransaction) {
    const reported = client.get("SELECT sqlite_version() AS version")?.["version"];
    const parts = typeof reported === "string" ? versionParts(reported) : undefined;
    if (parts === undefined) {
      throw new Error(`sqlite: SQLite reports its version as ${String(reported)}, not major.minor.patch`);
    }
    version = reported as string;
    const least = versionParts(minSqliteVersion)!;
    const older = parts[0] !== least[0] ? parts[0] < least[0] : parts[1] !== least[1] ? parts[1] < least[1] : parts[2] < least[2];
    if (older) {
      throw new Error(`sqlite: SQLite ${version} is older than ${minSqliteVersion}, the first with the STRICT tables the adapter's layout declares`);
    }
  }
  const of = version === undefined ? "this SQLite" : `SQLite ${version}`;
  let one: SqliteValue | undefined;
  try {
    one = client.get(jsonProbe)?.["one"];
  } catch (err) {
    throw new Error(
      `sqlite: ${of} cannot run json_each and json_extract, which the adapter's statements use (built in from 3.38.0, and in 3.37 with JSON1): ${err instanceof Error ? err.message : String(err)}`,
      { cause: err },
    );
  }
  if (Number(one) !== 1) {
    throw new Error(`sqlite: ${of} gives ${String(one)} for json_extract over json_each, not 1, so the adapter's statements cannot run on it`);
  }
  if (layout !== undefined) {
    checkedLayouts.add(layout);
  } else {
    checkedClients.add(client);
  }
}

/** Names a table or an index of the layout from its local name ("ref", "member_entity"). */
export type TableName = (name: string) => string;

/** The default name of each table and index: graph_ and its local name. */
export const defaultTableName: TableName = (name) => "graph_" + name;

/** The local names of the layout's tables, in the order the layout creates them. */
export const sqliteTables = [
  "ref",
  "ref_history",
  "commit",
  "patch",
  "snapshot_entry",
  "release",
  "release_history",
  "member",
  "member_history",
] as const;

/** Configure a SqliteAdapter. */
export interface SqliteOptions {
  /** The graph's name, which every row of the graph carries. Required, and not empty. */
  graph: string;
  /** Names each table and index; defaultTableName when absent. */
  tableName?: TableName;
  /**
   * The time a transaction writes, in whole microseconds since the Unix
   * epoch. A transaction reads it once, when it begins. Absent is the system
   * clock, Date.now(), whose precision is a millisecond.
   */
  clock?: () => number;
  /**
   * Runs every transaction in the transaction the caller holds on the
   * connection, issuing no transaction control at all: no BEGIN, no
   * savepoint, no COMMIT and no ROLLBACK. The caller begins, commits and
   * rolls back, and rolls back when a transaction throws. Absent or false,
   * the adapter runs a transaction of its own on the connection.
   */
  callerTransaction?: boolean;
}

/** One member kind's role columns, columns and history. */
interface Kind {
  name: string;
  key: string;
  id: string;
  ref: string;
  root: string;
  tombstone: string;
  version: string;
  columns: Readonly<Record<string, string>>;
  /** Every declared column but the role ones: what data holds. */
  data: string[];
  exclude: ReadonlySet<string>;
  actor: string | undefined;
  retentionDays: number | undefined;
}

/** What the adapter reads of a graph descriptor. */
interface DescriptorDoc {
  version?: number;
  kinds?: {
    kind?: string;
    key?: string;
    id?: string;
    ref?: string;
    root?: string;
    tombstone?: string;
    version?: string;
    history?: { retentionDays?: number; exclude?: string[]; actor?: string };
    columns?: Record<string, string>;
  }[];
}

/** The layout's tables, each quoted, under one name function. */
interface Tables {
  ref: string;
  refHistory: string;
  commit: string;
  patch: string;
  snapshot: string;
  release: string;
  releaseHistory: string;
  member: string;
  memberHistory: string;
}

/** An adapter's statements' parts, built once from its descriptor and options. */
interface AdapterConfig {
  graph: string;
  tableName: TableName;
  tables: Tables;
  clock: () => number;
  callerTransaction: boolean;
  kinds: Map<string, Kind>;
}

// The audit columns the adapter writes on every member row when a kind has
// them.
const createdAtColumn = "created_at";
const createdByColumn = "created_by";
const updatedAtColumn = "updated_at";
const updatedByColumn = "updated_by";

const microsPerDay = 86_400_000_000;

/** Quotes an identifier. */
function quote(name: string): string {
  return '"' + name.replace(/"/g, '""') + '"';
}

/** Names one table or index, refusing a name function that gives no name. */
function nameOf(tableName: TableName, local: string): string {
  const name = tableName(local);
  if (typeof name !== "string" || name === "" || name.includes("\u0000")) {
    throw new TypeError(`sqlite: the table name function named ${JSON.stringify(local)} ${JSON.stringify(name)}`);
  }
  return quote(name);
}

function tablesOf(tableName: TableName): Tables {
  return {
    ref: nameOf(tableName, "ref"),
    refHistory: nameOf(tableName, "ref_history"),
    commit: nameOf(tableName, "commit"),
    patch: nameOf(tableName, "patch"),
    snapshot: nameOf(tableName, "snapshot_entry"),
    release: nameOf(tableName, "release"),
    releaseHistory: nameOf(tableName, "release_history"),
    member: nameOf(tableName, "member"),
    memberHistory: nameOf(tableName, "member_history"),
  };
}

/**
 * The statements that create the layout, each table and index under the name
 * tableName gives it (defaultTableName when absent): one statement each,
 * CREATE TABLE IF NOT EXISTS or CREATE [UNIQUE] INDEX IF NOT EXISTS, with no
 * trigger and no transaction control, for a caller that runs its own
 * migrations. SqliteAdapter.createTables runs them.
 */
export function sqliteLayout(tableName: TableName = defaultTableName): string[] {
  const t = tablesOf(tableName);
  const index = (local: string) => nameOf(tableName, local);
  const history = (table: string) =>
    `CREATE TABLE IF NOT EXISTS ${table} (` +
    `history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, id TEXT NOT NULL, _version INTEGER NOT NULL, ` +
    `operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), data TEXT NOT NULL, recorded_at INTEGER NOT NULL` +
    `) STRICT`;
  return [
    // A ref's head and a commit's ref point at each other. A ref is written
    // before any commit of it, and its head moves to a commit only once the
    // commit is written, so both keys are checked at once, not deferred.
    `CREATE TABLE IF NOT EXISTS ${t.ref} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
      `parent_ref_id TEXT REFERENCES ${t.ref} (id), base_commit_id TEXT REFERENCES ${t.commit} (id), ` +
      `head_commit_id TEXT REFERENCES ${t.commit} (id), name TEXT NOT NULL, sealed_at INTEGER, ` +
      `created_at INTEGER NOT NULL, created_by TEXT NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, ` +
      `deleted_at INTEGER, deleted_by TEXT, _version INTEGER NOT NULL` +
      `) STRICT`,
    // A root's live refs have distinct names: a name already taken is this
    // index's SQLITE_CONSTRAINT_UNIQUE.
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("ref_live_name")} ON ${t.ref} (graph, root_id, name) WHERE deleted_at IS NULL`,
    history(t.refHistory),
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("ref_history_version")} ON ${t.refHistory} (id, _version)`,
    `CREATE TABLE IF NOT EXISTS ${t.commit} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
      `ref_id TEXT NOT NULL REFERENCES ${t.ref} (id), parent_commit_id TEXT REFERENCES ${t.commit} (id), ` +
      `message TEXT, schema_epoch INTEGER NOT NULL, content_hash TEXT NOT NULL, sequence INTEGER, ` +
      `created_at INTEGER NOT NULL, created_by TEXT NOT NULL` +
      `) STRICT`,
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("commit_sequence")} ON ${t.commit} (graph, root_id, sequence)`,
    `CREATE TABLE IF NOT EXISTS ${t.patch} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES ${t.commit} (id), ` +
      `entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL, ` +
      `operation TEXT NOT NULL CHECK (operation IN ('ADD', 'UPDATE', 'DELETE'))` +
      `) STRICT`,
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("patch_entity")} ON ${t.patch} (commit_id, entity_kind, entity_key)`,
    `CREATE INDEX IF NOT EXISTS ${index("patch_pin")} ON ${t.patch} (entity_id, entity_version)`,
    `CREATE TABLE IF NOT EXISTS ${t.snapshot} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES ${t.commit} (id), ` +
      `entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL` +
      `) STRICT`,
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("snapshot_entry_entity")} ON ${t.snapshot} (commit_id, entity_kind, entity_key)`,
    `CREATE INDEX IF NOT EXISTS ${index("snapshot_entry_pin")} ON ${t.snapshot} (entity_id, entity_version)`,
    `CREATE TABLE IF NOT EXISTS ${t.release} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, ` +
      `commit_id TEXT NOT NULL REFERENCES ${t.commit} (id), created_at INTEGER NOT NULL, created_by TEXT NOT NULL, ` +
      `updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, _version INTEGER NOT NULL` +
      `) STRICT`,
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("release_root")} ON ${t.release} (graph, root_id)`,
    history(t.releaseHistory),
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("release_history_version")} ON ${t.releaseHistory} (id, _version)`,
    `CREATE TABLE IF NOT EXISTS ${t.member} (` +
      `id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, entity_key TEXT NOT NULL, ` +
      `ref_id TEXT NOT NULL REFERENCES ${t.ref} (id), root_id TEXT NOT NULL, ` +
      `tombstone INTEGER NOT NULL CHECK (tombstone IN (0, 1)), _version INTEGER NOT NULL, data TEXT NOT NULL` +
      `) STRICT`,
    // Unique on the entity key of a kind on a ref, declared as (graph, kind,
    // ref_id, entity_key) so it also serves a read of a ref's rows of a kind.
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("member_entity")} ON ${t.member} (graph, kind, ref_id, entity_key)`,
    `CREATE TABLE IF NOT EXISTS ${t.memberHistory} (` +
      `history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, ` +
      `_version INTEGER NOT NULL, operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), ` +
      `data TEXT NOT NULL, recorded_at INTEGER NOT NULL` +
      `) STRICT`,
    `CREATE UNIQUE INDEX IF NOT EXISTS ${index("member_history_version")} ON ${t.memberHistory} (id, _version)`,
    `CREATE INDEX IF NOT EXISTS ${index("member_history_recorded")} ON ${t.memberHistory} (graph, kind, recorded_at)`,
  ];
}

const configs = new WeakMap<SqliteAdapter, AdapterConfig>();

/** The system clock in microseconds. */
function systemClock(): number {
  return Date.now() * 1000;
}

/** A graph's statements over the layout, built from its descriptor. Safe to share; bind a client with storage. */
export class SqliteAdapter {
  /**
   * Reads a graph's descriptor (version 3, as JSON text or an object). The
   * adapter reads only its kinds: each kind's role columns (the root among
   * them), its columns' value classes and its history; the tables the
   * descriptor names are the Postgres adapter's.
   */
  constructor(descriptor: string | Descriptor, options: SqliteOptions) {
    const d = (typeof descriptor === "string" ? JSON.parse(descriptor) : descriptor) as unknown as DescriptorDoc;
    if (d.version !== 3) {
      throw new Error(`sqlite: descriptor version ${String(d.version ?? 0)}; this adapter reads version 3`);
    }
    if (typeof options?.graph !== "string" || options.graph === "") {
      throw new TypeError("sqlite: an adapter needs its graph's name (options.graph)");
    }
    if (options.clock !== undefined && typeof options.clock !== "function") {
      throw new TypeError("sqlite: options.clock is a function that returns microseconds since the Unix epoch");
    }
    const tableName = options.tableName ?? defaultTableName;
    const config: AdapterConfig = {
      graph: options.graph,
      tableName,
      tables: tablesOf(tableName),
      clock: options.clock ?? systemClock,
      callerTransaction: options.callerTransaction === true,
      kinds: new Map(),
    };
    for (const k of d.kinds ?? []) {
      const columns = k.columns ?? {};
      const has = (column: string) => Object.prototype.hasOwnProperty.call(columns, column);
      const roles: [string, string | undefined][] = [
        ["key", k.key],
        ["id", k.id],
        ["ref", k.ref],
        ["root", k.root],
        ["tombstone", k.tombstone],
        ["version", k.version],
      ];
      for (const [role, column] of roles) {
        if (column === undefined || column === "") {
          throw new Error(`sqlite: kind ${JSON.stringify(k.kind)} has no ${role}`);
        }
        if (!has(column)) {
          throw new Error(`sqlite: kind ${JSON.stringify(k.kind)} ${role} column ${JSON.stringify(column)} is not in its columns`);
        }
      }
      if (columns[k.tombstone!] !== "boolean" || columns[k.version!] !== "integer") {
        throw new Error(`sqlite: kind ${JSON.stringify(k.kind)}: a tombstone is a boolean column and a version an integer one`);
      }
      const history = k.history;
      if (history === undefined || history === null || !Array.isArray(history.exclude)) {
        throw new Error(`sqlite: kind ${JSON.stringify(k.kind)} has no history`);
      }
      const roleColumns = new Set(roles.map(([, column]) => column!));
      const exclude = new Set(history.exclude);
      config.kinds.set(k.kind!, {
        name: k.kind!,
        key: k.key!,
        id: k.id!,
        ref: k.ref!,
        root: k.root!,
        tombstone: k.tombstone!,
        version: k.version!,
        columns,
        data: Object.keys(columns)
          .filter((column) => !roleColumns.has(column))
          .sort(compareCodePoints),
        exclude,
        actor: history.actor,
        retentionDays: history.retentionDays,
      });
    }
    configs.set(this, config);
  }

  /**
   * Creates the layout's tables and indexes where they are missing
   * (sqliteLayout), in one transaction: its own on the connection, or the
   * caller's when the adapter runs in the caller's transaction. It first
   * refuses a SQLite the adapter cannot run on (minSqliteVersion).
   */
  createTables(client: SqliteClient): void {
    const config = configs.get(this)!;
    checkSqlite(config, client);
    transact(config, client, () => {
      for (const statement of sqliteLayout(config.tableName)) {
        client.run(statement);
      }
    });
  }

  /**
   * Binds the adapter to a client. On a connection of its own it turns the
   * connection's foreign keys on first, which SQLite ignores inside a
   * transaction, so bind it outside one; in the caller's transaction the
   * caller's connection has them on, as D16's does. Either way it refuses a
   * SQLite the adapter cannot run on (minSqliteVersion).
   */
  storage(client: SqliteClient): SyncStorage {
    const config = configs.get(this)!;
    if (!config.callerTransaction) {
      if (typeof client.exec !== "function") {
        throw new TypeError("sqlite: a client the adapter runs its own transactions on has exec");
      }
      client.exec("PRAGMA foreign_keys = ON");
      const on = client.get("PRAGMA foreign_keys");
      if (on === undefined || Number(on["foreign_keys"]) !== 1) {
        throw new Error("sqlite: the connection's foreign keys would not turn on; bind the adapter outside a transaction");
      }
    }
    checkSqlite(config, client);
    return {
      transact: <T>(fn: (tx: SyncTx) => T): T => transact(config, client, (time) => fn(new SqliteTx(config, client, time))),
    };
  }
}

/** A connection's transaction under way: how deep its savepoints go, and the time it began at. */
interface Open {
  depth: number;
  time: number;
}

const open = new WeakMap<object, Open>();

/** Reads the clock once, refusing a time that is not a whole number of microseconds. */
function readClock(config: AdapterConfig): number {
  const time = config.clock();
  if (typeof time !== "number" || !Number.isSafeInteger(time)) {
    throw new TypeError(`sqlite: the clock returned ${String(time)}, not a whole number of microseconds`);
  }
  return time;
}

function exec(client: SqliteClient, sql: string): void {
  if (typeof client.exec !== "function") {
    throw new TypeError("sqlite: a client the adapter runs its own transactions on has exec");
  }
  client.exec(sql);
}

/** Whether a value is a promise or another thenable. */
function isThenable(value: unknown): boolean {
  return (
    ((typeof value === "object" && value !== null) || typeof value === "function") &&
    typeof (value as { then?: unknown }).then === "function"
  );
}

/** Refuses a transaction's function that returned a promise, which a synchronous transaction cannot wait for. */
function refusePromise(value: unknown): void {
  if (isThenable(value)) {
    // The promise's own outcome no longer matters; keep its rejection from
    // surfacing as unhandled.
    (value as PromiseLike<unknown>).then(undefined, () => undefined);
    throw new TypeError("sqlite: a transaction is synchronous: its function returned a promise");
  }
}

/**
 * Runs fn in one transaction of the connection. In the caller's transaction
 * it reads the clock, runs fn at that time and issues nothing else. On a
 * connection of its own, the outermost transaction begins with BEGIN
 * IMMEDIATE, which takes the file's write lock at once, and then reads the
 * clock; a transaction begun inside it on the same connection is a
 * savepoint, which takes the outer one's time, as every statement of a
 * Postgres transaction has the one now().
 */
function transact<T>(config: AdapterConfig, client: SqliteClient, fn: (time: number) => T): T {
  if (config.callerTransaction) {
    const out = fn(readClock(config));
    refusePromise(out);
    return out;
  }
  const outer = open.get(client);
  if (outer === undefined) {
    exec(client, "BEGIN IMMEDIATE");
    let out: T;
    try {
      // Read once the write lock is held, so times order as the writes do.
      const time = readClock(config);
      open.set(client, { depth: 1, time });
      out = fn(time);
      refusePromise(out);
    } catch (err) {
      open.delete(client);
      try {
        exec(client, "ROLLBACK");
      } catch {
        // The error that ended the transaction is the one to report.
      }
      throw err;
    }
    open.delete(client);
    try {
      exec(client, "COMMIT");
    } catch (err) {
      try {
        exec(client, "ROLLBACK");
      } catch {
        // The commit's error is the one to report.
      }
      throw err;
    }
    return out;
  }
  const savepoint = `superschematic_versiongraph_${outer.depth}`;
  exec(client, `SAVEPOINT ${savepoint}`);
  outer.depth++;
  let out: T;
  try {
    out = fn(outer.time);
    refusePromise(out);
  } catch (err) {
    outer.depth--;
    try {
      exec(client, `ROLLBACK TO ${savepoint}`);
      exec(client, `RELEASE ${savepoint}`);
    } catch {
      // The error that ended the savepoint is the one to report.
    }
    throw err;
  }
  outer.depth--;
  exec(client, `RELEASE ${savepoint}`);
  return out;
}

/** A new id: a version-4 UUID in its canonical form. */
function newID(): string {
  return uuidCanonical(globalThis.crypto.randomUUID());
}

/**
 * A time in microseconds since the Unix epoch as a canonical date-time: UTC
 * with Z, its fraction of a second without trailing zeros and left out when
 * zero. A year outside 0000-9999 is refused.
 */
export function microsToDateTime(micros: number): string {
  if (!Number.isSafeInteger(micros)) {
    throw new RangeError(`sqlite: ${String(micros)} is not a whole number of microseconds`);
  }
  const rest = ((micros % 1000) + 1000) % 1000;
  const iso = new Date((micros - rest) / 1000).toISOString();
  if (!/^[0-9]{4}-/.test(iso)) {
    throw new RangeError(`sqlite: ${micros} microseconds falls outside the years 0000-9999`);
  }
  const fraction = (iso.slice(20, 23) + String(rest).padStart(3, "0")).replace(/0+$/, "");
  return iso.slice(0, 19) + (fraction !== "" ? "." + fraction : "") + "Z";
}

function text(value: SqliteValue | undefined, column: string): string {
  if (typeof value !== "string") {
    throw new Error(`sqlite: column ${column} is ${value === null || value === undefined ? "NULL" : typeof value}, not text`);
  }
  return value;
}

function optionalText(value: SqliteValue | undefined, column: string): string | null {
  return value === null || value === undefined ? null : text(value, column);
}

function int(value: SqliteValue | undefined, column: string): number {
  const n = typeof value === "bigint" ? Number(value) : value;
  if (typeof n !== "number" || !Number.isSafeInteger(n)) {
    throw new Error(`sqlite: column ${column} is ${String(value)}, not an integer a number holds exactly`);
  }
  return n;
}

function optionalInt(value: SqliteValue | undefined, column: string): number | null {
  return value === null || value === undefined ? null : int(value, column);
}

function changes(result: SqliteRunResult): number {
  return Number(result.changes);
}

/** Whether err is SQLite's SQLITE_CONSTRAINT_UNIQUE. */
function isUniqueViolation(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { code?: unknown }).code === SQLITE_CONSTRAINT_UNIQUE;
}

/**
 * Prefixes an error's message with what the adapter was doing, keeping the
 * error itself (its class and its code) so callers can still tell it apart.
 */
function withContext(what: string, err: unknown): unknown {
  if (err instanceof NotFoundError || err instanceof NameTakenError) {
    return err;
  }
  if (err instanceof Error && !err.message.startsWith("sqlite: ")) {
    err.message = `sqlite: ${what}: ${err.message}`;
  }
  return err;
}

/** Writes a JSON object of members, each already JSON text, sorted by name. */
function writeObject(members: ReadonlyMap<string, string>): string {
  return (
    "{" +
    [...members.keys()]
      .sort(compareCodePoints)
      .map((name) => writeJsonString(name) + ":" + members.get(name)!)
      .join(",") +
    "}"
  );
}

/** Reads a JSON object column, each member as its JSON text. */
function readObject(json: string, column: string): Map<string, string> {
  const parsed = parseJson(json);
  if (!isJsonObject(parsed)) {
    throw new Error(`sqlite: column ${column} does not hold a JSON object`);
  }
  const out = new Map<string, string>();
  for (const [name, value] of parsed) {
    out.set(name, stringifyJson(value));
  }
  return out;
}

/** The JSON text of an optional string. */
function jsonText(value: string | null): string {
  return value === null ? "null" : writeJsonString(value);
}

/** The JSON text of an optional time, as a canonical date-time. */
function jsonTime(micros: number | null): string {
  return micros === null ? "null" : writeJsonString(microsToDateTime(micros));
}

// The columns each table's reads select, in the layout's names.
const refColumns =
  "id, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, created_at, created_by, " +
  "updated_at, updated_by, deleted_at, deleted_by, _version";
const commitColumns =
  "c.id, c.root_id, c.ref_id, c.parent_commit_id, c.message, c.schema_epoch, c.content_hash, c.sequence, c.created_at, c.created_by";
const releaseColumns = "id, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version";
const memberColumns = "id, entity_key, ref_id, root_id, tombstone, _version, data";

function scanRef(row: SqliteRow): Ref {
  return {
    id: text(row["id"], "id"),
    root: text(row["root_id"], "root_id"),
    parent: optionalText(row["parent_ref_id"], "parent_ref_id"),
    base: optionalText(row["base_commit_id"], "base_commit_id"),
    head: optionalText(row["head_commit_id"], "head_commit_id"),
    name: text(row["name"], "name"),
    sealed: optionalInt(row["sealed_at"], "sealed_at") !== null,
    discarded: optionalInt(row["deleted_at"], "deleted_at") !== null,
    version: int(row["_version"], "_version"),
  };
}

/** A ref's history image: each of its columns, an id in its canonical form and a time as a canonical date-time. */
function refImage(row: SqliteRow): string {
  const image = new Map<string, string>();
  for (const column of ["id", "root_id", "parent_ref_id", "base_commit_id", "head_commit_id", "name", "created_by", "updated_by", "deleted_by"]) {
    image.set(column, jsonText(optionalText(row[column], column)));
  }
  for (const column of ["sealed_at", "created_at", "updated_at", "deleted_at"]) {
    image.set(column, jsonTime(optionalInt(row[column], column)));
  }
  image.set("_version", String(int(row["_version"], "_version")));
  return writeObject(image);
}

/** A release pointer's history image, as refImage writes a ref's. */
function releaseImage(row: SqliteRow): string {
  const image = new Map<string, string>();
  for (const column of ["id", "root_id", "commit_id", "created_by", "updated_by"]) {
    image.set(column, jsonText(text(row[column], column)));
  }
  for (const column of ["created_at", "updated_at"]) {
    image.set(column, jsonTime(int(row[column], column)));
  }
  image.set("_version", String(int(row["_version"], "_version")));
  return writeObject(image);
}

function scanCommit(row: SqliteRow): Commit {
  return {
    id: text(row["id"], "id"),
    root: text(row["root_id"], "root_id"),
    ref: text(row["ref_id"], "ref_id"),
    parent: optionalText(row["parent_commit_id"], "parent_commit_id"),
    message: optionalText(row["message"], "message") ?? "",
    schemaEpoch: int(row["schema_epoch"], "schema_epoch"),
    contentHash: text(row["content_hash"], "content_hash"),
    sequence: optionalInt(row["sequence"], "sequence"),
    createdAt: microsToDateTime(int(row["created_at"], "created_at")),
    createdBy: text(row["created_by"], "created_by"),
    snapshot: int(row["snapshot"], "snapshot") === 1,
  };
}

function scanRelease(row: SqliteRow): Release {
  return {
    id: text(row["id"], "id"),
    root: text(row["root_id"], "root_id"),
    commit: text(row["commit_id"], "commit_id"),
    version: int(row["_version"], "_version"),
  };
}

/** A member row as the layout holds it: its role columns, and its other columns as JSON text. */
interface Member {
  id: string;
  key: string;
  ref: string;
  root: string;
  tombstone: boolean;
  version: number;
  data: Map<string, string>;
}

function scanMember(row: SqliteRow): Member {
  return {
    id: text(row["id"], "id"),
    key: text(row["entity_key"], "entity_key"),
    ref: text(row["ref_id"], "ref_id"),
    root: text(row["root_id"], "root_id"),
    tombstone: int(row["tombstone"], "tombstone") === 1,
    version: int(row["_version"], "_version"),
    data: readObject(text(row["data"], "data"), "data"),
  };
}

/**
 * A member's canonical row's members: its role columns under the
 * descriptor's names, and every other column the kind declares, null where
 * the stored row lacks it, as a Postgres row has a column added after it
 * was written. A stored column the kind no longer declares is kept as
 * stored.
 */
function memberMembers(k: Kind, m: Member): Map<string, string> {
  const members = new Map(m.data);
  for (const column of k.data) {
    if (!members.has(column)) {
      members.set(column, "null");
    }
  }
  members.set(k.id, writeJsonString(m.id));
  members.set(k.key, writeJsonString(m.key));
  members.set(k.ref, writeJsonString(m.ref));
  members.set(k.root, writeJsonString(m.root));
  members.set(k.tombstone, m.tombstone ? "true" : "false");
  members.set(k.version, String(m.version));
  return members;
}

/** One transaction's view of the graph, over a client, at the transaction's time. */
class SqliteTx implements SyncTx {
  readonly #a: AdapterConfig;
  readonly #client: SqliteClient;
  readonly #time: number;

  constructor(config: AdapterConfig, client: SqliteClient, time: number) {
    this.#a = config;
    this.#client = client;
    this.#time = time;
  }

  #kind(name: string): Kind {
    const k = this.#a.kinds.get(name);
    if (k === undefined) {
      throw new Error(`sqlite: unknown kind ${JSON.stringify(name)}`);
    }
    return k;
  }

  #run(sql: string, params: readonly SqliteValue[]): number {
    return changes(this.#client.run(sql, params));
  }

  #get(sql: string, params: readonly SqliteValue[]): SqliteRow | undefined {
    return this.#client.get(sql, params);
  }

  #all(sql: string, params: readonly SqliteValue[]): SqliteRow[] {
    return this.#client.all(sql, params);
  }

  /** The canonical text of a value of a column's class, given as a string, decoded: an id, a key, an actor. */
  #roleValue(k: Kind, column: string, value: string): string {
    const canonical = JSON.parse(canonicalOf(k.columns[column]!, value)) as unknown;
    if (typeof canonical !== "string") {
      throw new Error(`sqlite: the ${k.name} column ${column} holds a string`);
    }
    return canonical;
  }

  /** Refuses a write whose ref is not one of this graph's refs of root. */
  #requireRef(id: string, root: string, what: string): void {
    const found = this.#get(`SELECT 1 AS found FROM ${this.#a.tables.ref} WHERE graph = ?1 AND id = ?2 AND root_id = ?3`, [
      this.#a.graph,
      id,
      root,
    ]);
    if (found === undefined) {
      throw new Error(`sqlite: ${what}: ref ${id} is not a ref of root ${root} in graph ${this.#a.graph}`);
    }
  }

  /** Refuses a write whose commit is not one of this graph's commits of root. */
  #requireCommit(id: string, root: string, what: string): void {
    const found = this.#get(`SELECT 1 AS found FROM ${this.#a.tables.commit} WHERE graph = ?1 AND id = ?2 AND root_id = ?3`, [
      this.#a.graph,
      id,
      root,
    ]);
    if (found === undefined) {
      throw new Error(`sqlite: ${what}: commit ${id} is not a commit of root ${root} in graph ${this.#a.graph}`);
    }
  }

  /** Writes a ref's or a release pointer's history image. */
  #history(table: string, id: string, version: number, operation: "INSERT" | "UPDATE" | "DELETE", data: string): void {
    this.#run(
      `INSERT INTO ${table} (history_id, graph, id, _version, operation, data, recorded_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
      [newID(), this.#a.graph, id, version, operation, data, this.#time],
    );
  }

  /**
   * Writes a member's history image: its canonical row's members less the
   * kind's history-excluded columns.
   */
  #memberHistory(k: Kind, id: string, version: number, operation: "INSERT" | "UPDATE" | "DELETE", members: Map<string, string>): void {
    const image = new Map(members);
    for (const column of k.exclude) {
      image.delete(column);
    }
    this.#run(
      `INSERT INTO ${this.#a.tables.memberHistory} (history_id, graph, kind, id, _version, operation, data, recorded_at) ` +
        `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
      [newID(), this.#a.graph, k.name, id, version, operation, writeObject(image), this.#time],
    );
  }

  createRef(ref: NewRef): Ref {
    const t = this.#a.tables;
    try {
      if (ref.parent !== null) {
        this.#requireRef(ref.parent, ref.root, "the parent");
      }
      if (ref.base !== null) {
        this.#requireCommit(ref.base, ref.root, "the base");
      }
      const id = newID();
      let row: SqliteRow | undefined;
      try {
        row = this.#get(
          `INSERT INTO ${t.ref} (id, graph, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, ` +
            `created_at, created_by, updated_at, updated_by, deleted_at, deleted_by, _version) ` +
            `VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, NULL, ?7, ?8, ?7, ?8, NULL, NULL, 1) RETURNING ${refColumns}`,
          [id, this.#a.graph, ref.root, ref.parent, ref.base, ref.name, this.#time, ref.actor],
        );
      } catch (err) {
        if (isUniqueViolation(err)) {
          throw new NameTakenError(`the root already has a live ref of that name: ${JSON.stringify(ref.name)}`);
        }
        throw err;
      }
      this.#history(t.refHistory, id, 1, "INSERT", refImage(row!));
      return scanRef(row!);
    } catch (err) {
      throw withContext("create ref", err);
    }
  }

  readRef(id: string): Ref {
    let row: SqliteRow | undefined;
    try {
      row = this.#get(`SELECT ${refColumns} FROM ${this.#a.tables.ref} WHERE graph = ?1 AND id = ?2`, [this.#a.graph, id]);
    } catch (err) {
      throw withContext("read ref", err);
    }
    if (row === undefined) {
      throw new NotFoundError();
    }
    return scanRef(row);
  }

  /** readRef: the one writer the file's write lock lets in orders every write, so a ref needs no lock of its own. */
  lockRef(id: string): Ref {
    return this.readRef(id);
  }

  updateRef(update: RefUpdate): Ref {
    const t = this.#a.tables;
    let row: SqliteRow | undefined;
    try {
      if (update.head !== null || update.base !== null) {
        const current = this.#get(`SELECT root_id FROM ${t.ref} WHERE graph = ?1 AND id = ?2`, [this.#a.graph, update.id]);
        if (current === undefined) {
          throw new VersionConflictError();
        }
        const root = text(current["root_id"], "root_id");
        if (update.head !== null) {
          this.#requireCommit(update.head, root, "the head");
        }
        if (update.base !== null) {
          this.#requireCommit(update.base, root, "the base");
        }
      }
      row = this.#get(
        `UPDATE ${t.ref} SET head_commit_id = COALESCE(?3, head_commit_id), base_commit_id = COALESCE(?4, base_commit_id), ` +
          `sealed_at = CASE WHEN ?5 = 1 THEN ?6 ELSE sealed_at END, updated_at = ?6, updated_by = ?7, _version = _version + 1 ` +
          `WHERE graph = ?1 AND id = ?2 AND _version = ?8 RETURNING ${refColumns}`,
        [this.#a.graph, update.id, update.head, update.base, update.seal ? 1 : 0, this.#time, update.actor, update.version],
      );
      if (row === undefined) {
        throw new VersionConflictError();
      }
      this.#history(t.refHistory, update.id, int(row["_version"], "_version"), "UPDATE", refImage(row));
    } catch (err) {
      throw withContext("update ref", err);
    }
    return scanRef(row);
  }

  discardRef(id: string, version: number, actor: string): undefined {
    const t = this.#a.tables;
    try {
      const row = this.#get(
        `UPDATE ${t.ref} SET deleted_at = ?3, deleted_by = ?4, _version = _version + 1 ` +
          `WHERE graph = ?1 AND id = ?2 AND _version = ?5 AND deleted_at IS NULL RETURNING ${refColumns}`,
        [this.#a.graph, id, this.#time, actor, version],
      );
      if (row === undefined) {
        throw new VersionConflictError();
      }
      this.#history(t.refHistory, id, int(row["_version"], "_version"), "UPDATE", refImage(row));
    } catch (err) {
      throw withContext("discard ref", err);
    }
    return undefined;
  }

  #members(sql: string, params: readonly SqliteValue[]): Member[] {
    return this.#all(sql, params).map(scanMember);
  }

  rows(kindName: string, ref: string): string[] {
    const k = this.#kind(kindName);
    try {
      return this.#members(`SELECT ${memberColumns} FROM ${this.#a.tables.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3`, [
        this.#a.graph,
        k.name,
        ref,
      ]).map((m) => writeObject(memberMembers(k, m)));
    } catch (err) {
      throw withContext(`read ${k.name} rows`, err);
    }
  }

  upsertRow(kindName: string, write: RowWrite): string {
    const k = this.#kind(kindName);
    try {
      return this.#upsert(k, write);
    } catch (err) {
      throw withContext(`write ${k.name} row`, err);
    }
  }

  #upsert(k: Kind, write: RowWrite): string {
    const parsed = parseJson(write.row);
    if (!isJsonObject(parsed)) {
      throw new Error(`a ${k.name} row is a JSON object`);
    }
    // The row's other columns, each canonical. The adapter writes the ref,
    // the root and the tombstone, and never the id or the version.
    const given = new Map<string, string>();
    for (const [column, value] of parsed) {
      if (!Object.prototype.hasOwnProperty.call(k.columns, column)) {
        throw new Error(`the ${k.name} row has column ${JSON.stringify(column)}, which its descriptor does not declare`);
      }
      if (column === k.id || column === k.version || column === k.ref || column === k.root || column === k.tombstone || column === k.key) {
        continue;
      }
      try {
        given.set(column, canonicalOf(k.columns[column]!, value));
      } catch (err) {
        throw withContext(`column ${column}`, err);
      }
    }
    const keyValue = parsed.get(k.key);
    let key: string | undefined;
    if (keyValue !== undefined && keyValue !== null) {
      const canonical = JSON.parse(canonicalOf(k.columns[k.key]!, keyValue)) as unknown;
      if (typeof canonical !== "string") {
        throw new Error(`the ${k.name} row's entity key is a string`);
      }
      key = canonical;
    }
    const ref = this.#roleValue(k, k.ref, write.ref);
    const root = this.#roleValue(k, k.root, write.root);
    this.#requireRef(ref, root, "the row's ref");
    const time = microsToDateTime(this.#time);
    const audit = (column: string, value: string) => {
      if (Object.prototype.hasOwnProperty.call(k.columns, column)) {
        given.set(column, canonicalOf(k.columns[column]!, value));
      }
    };
    const t = this.#a.tables;
    const existing =
      key === undefined
        ? undefined
        : this.#members(`SELECT ${memberColumns} FROM ${t.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4`, [
            this.#a.graph,
            k.name,
            ref,
            key,
          ])[0];
    if (existing !== undefined) {
      // A column the row lacks keeps its stored value; the entity key, the
      // root and the creation audit stay as the row was first written.
      given.delete(createdAtColumn);
      given.delete(createdByColumn);
      audit(updatedAtColumn, time);
      audit(updatedByColumn, write.actor);
      const data = new Map([...existing.data, ...given]);
      // A column the kind gained after the row was written is null, as a
      // Postgres row has it.
      for (const column of k.data) {
        if (!data.has(column)) {
          data.set(column, "null");
        }
      }
      const stored: Member = {
        ...existing,
        tombstone: write.tombstone,
        version: existing.version + 1,
        data,
      };
      const changed = this.#run(
        `UPDATE ${t.member} SET tombstone = ?3, _version = ?4, data = ?5 WHERE graph = ?1 AND id = ?2 AND _version = ?6`,
        [this.#a.graph, stored.id, stored.tombstone ? 1 : 0, stored.version, writeObject(stored.data), existing.version],
      );
      if (changed !== 1) {
        throw new Error(`${changed} rows written`);
      }
      const members = memberMembers(k, stored);
      this.#memberHistory(k, stored.id, stored.version, "UPDATE", members);
      return writeObject(members);
    }
    // A column the row lacks holds its default, null; the audit columns
    // hold the write's actor and time.
    for (const column of [createdAtColumn, updatedAtColumn]) {
      audit(column, time);
    }
    for (const column of [createdByColumn, updatedByColumn]) {
      audit(column, write.actor);
    }
    const data = new Map<string, string>();
    for (const column of k.data) {
      data.set(column, given.get(column) ?? "null");
    }
    const stored: Member = {
      id: this.#roleValue(k, k.id, newID()),
      key: key ?? this.#roleValue(k, k.key, newID()),
      ref,
      root,
      tombstone: write.tombstone,
      version: 1,
      data,
    };
    this.#run(
      `INSERT INTO ${t.member} (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) ` +
        `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, ?8)`,
      [stored.id, this.#a.graph, k.name, stored.key, stored.ref, stored.root, stored.tombstone ? 1 : 0, writeObject(data)],
    );
    const members = memberMembers(k, stored);
    this.#memberHistory(k, stored.id, 1, "INSERT", members);
    return writeObject(members);
  }

  /**
   * Hard-deletes a member row and writes its DELETE image: the row at its
   * version plus 1, with the kind's history actor column, when it has one,
   * set to actor.
   */
  #remove(k: Kind, m: Member, actor: string): void {
    const changed = this.#run(`DELETE FROM ${this.#a.tables.member} WHERE graph = ?1 AND id = ?2`, [this.#a.graph, m.id]);
    if (changed !== 1) {
      throw new Error(`${changed} rows deleted`);
    }
    const members = memberMembers(k, m);
    members.set(k.version, String(m.version + 1));
    if (k.actor !== undefined) {
      members.set(k.actor, canonicalOf(k.columns[k.actor]!, actor));
    }
    this.#memberHistory(k, m.id, m.version + 1, "DELETE", members);
  }

  removeRow(kindName: string, ref: string, entityKey: string, actor: string): boolean {
    const k = this.#kind(kindName);
    try {
      const key = this.#roleValue(k, k.key, entityKey);
      const found = this.#members(
        `SELECT ${memberColumns} FROM ${this.#a.tables.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4`,
        [this.#a.graph, k.name, ref, key],
      );
      for (const m of found) {
        this.#remove(k, m, actor);
      }
      return found.length > 0;
    } catch (err) {
      throw withContext(`remove the ${k.name} row`, err);
    }
  }

  /**
   * Each pinned image as it was stored, as a Postgres history image reads:
   * one taken before its kind gained a column lacks it, and the core reads
   * a content column a row lacks as null.
   */
  images(kindName: string, pins: readonly Pin[]): string[] {
    const k = this.#kind(kindName);
    if (pins.length === 0) {
      return [];
    }
    const pinList = "[" + pins.map((pin) => `[${writeJsonString(pin.id)},${String(int(pin.version, "version"))}]`).join(",") + "]";
    try {
      return this.#all(
        `SELECT h.data FROM json_each(?3) AS p JOIN ${this.#a.tables.memberHistory} AS h ` +
          `ON h.id = json_extract(p.value, '$[0]') AND h._version = json_extract(p.value, '$[1]') ` +
          `WHERE h.graph = ?1 AND h.kind = ?2`,
        [this.#a.graph, k.name, pinList],
      ).map((row) => text(row["data"], "data"));
    } catch (err) {
      throw withContext(`read ${k.name} history`, err);
    }
  }

  /** The SQL that tells whether the commit whose id is idSQL has a snapshot. */
  #hasSnapshot(idSQL: string): string {
    return `EXISTS (SELECT 1 FROM ${this.#a.tables.snapshot} AS s WHERE s.commit_id = ${idSQL})`;
  }

  readCommit(id: string): Commit {
    let row: SqliteRow | undefined;
    try {
      row = this.#get(
        `SELECT ${commitColumns}, ${this.#hasSnapshot("c.id")} AS snapshot FROM ${this.#a.tables.commit} AS c WHERE c.graph = ?1 AND c.id = ?2`,
        [this.#a.graph, id],
      );
    } catch (err) {
      throw withContext("read commit", err);
    }
    if (row === undefined) {
      throw new NotFoundError();
    }
    return scanCommit(row);
  }

  insertCommit(commit: NewCommit): Commit {
    try {
      this.#requireRef(commit.ref, commit.root, "the commit's ref");
      if (commit.parent !== null) {
        this.#requireCommit(commit.parent, commit.root, "the commit's parent");
      }
      const id = newID();
      this.#run(
        `INSERT INTO ${this.#a.tables.commit} (id, graph, root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence, created_at, created_by) ` +
          `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)`,
        [
          id,
          this.#a.graph,
          commit.root,
          commit.ref,
          commit.parent,
          commit.message === "" ? null : commit.message,
          commit.schemaEpoch,
          commit.contentHash,
          commit.sequence,
          this.#time,
          commit.actor,
        ],
      );
      return {
        id,
        root: commit.root,
        ref: commit.ref,
        parent: commit.parent,
        message: commit.message,
        schemaEpoch: commit.schemaEpoch,
        contentHash: commit.contentHash,
        sequence: commit.sequence,
        createdAt: microsToDateTime(this.#time),
        createdBy: commit.actor,
        snapshot: false,
      };
    } catch (err) {
      throw withContext("write commit", err);
    }
  }

  /** Refuses a commit of another graph, whatever its root, before writing its patches or its snapshot. */
  #requireOwnCommit(id: string, what: string): void {
    const found = this.#get(`SELECT 1 AS found FROM ${this.#a.tables.commit} WHERE graph = ?1 AND id = ?2`, [this.#a.graph, id]);
    if (found === undefined) {
      throw new Error(`${what}: commit ${id} is not a commit of graph ${this.#a.graph}`);
    }
  }

  /** An entity a patch or a snapshot entry pins: its kind's name and its key and id in their canonical forms. */
  #pinned(kindName: string, entityKey: string, entityId: string): { kind: Kind; key: string; id: string } {
    const k = this.#kind(kindName);
    return { kind: k, key: this.#roleValue(k, k.key, entityKey), id: this.#roleValue(k, k.id, entityId) };
  }

  insertPatches(commit: string, patches: readonly Patch[]): undefined {
    try {
      this.#requireOwnCommit(commit, "the patches' commit");
      for (const p of patches) {
        const { kind, key, id } = this.#pinned(p.kind, p.entityKey, p.entityId);
        this.#run(
          `INSERT INTO ${this.#a.tables.patch} (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) ` +
            `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)`,
          [newID(), this.#a.graph, commit, kind.name, key, id, p.entityVersion, p.operation],
        );
      }
    } catch (err) {
      throw withContext("write patches", err);
    }
    return undefined;
  }

  #commits(sql: string, params: readonly SqliteValue[]): Commit[] {
    return this.#all(sql, params).map(scanCommit);
  }

  walk(commit: string, limit: number): Commit[] {
    // The walk carries each commit's snapshot flag, and goes no further than
    // the first commit that has one.
    const t = this.#a.tables;
    const sql =
      `WITH RECURSIVE chain (id, parent, depth, snapshotted) AS (` +
      `SELECT c.id, c.parent_commit_id, 1, ${this.#hasSnapshot("c.id")} FROM ${t.commit} AS c WHERE c.graph = ?1 AND c.id = ?2 ` +
      `UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1, ${this.#hasSnapshot("c.id")} FROM ${t.commit} AS c ` +
      `JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND chain.depth < ?3 AND NOT chain.snapshotted` +
      `) SELECT ${commitColumns}, chain.snapshotted AS snapshot FROM chain JOIN ${t.commit} AS c ON c.id = chain.id ORDER BY chain.depth`;
    try {
      return this.#commits(sql, [this.#a.graph, commit, limit]);
    } catch (err) {
      throw withContext("walk commits", err);
    }
  }

  refCommits(ref: string, head: string, limit: number): Commit[] {
    const t = this.#a.tables;
    const sql =
      `WITH RECURSIVE chain (id, parent, depth) AS (` +
      `SELECT c.id, c.parent_commit_id, 1 FROM ${t.commit} AS c WHERE c.graph = ?1 AND c.id = ?2 AND c.ref_id = ?3 ` +
      `UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1 FROM ${t.commit} AS c ` +
      `JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND c.ref_id = ?3 AND chain.depth < ?4` +
      `) SELECT ${commitColumns}, ${this.#hasSnapshot("c.id")} AS snapshot FROM chain JOIN ${t.commit} AS c ON c.id = chain.id ORDER BY chain.depth`;
    try {
      return this.#commits(sql, [this.#a.graph, head, ref, limit]);
    } catch (err) {
      throw withContext("list commits", err);
    }
  }

  patches(commits: readonly string[]): Patch[] {
    if (commits.length === 0) {
      return [];
    }
    try {
      return this.#all(
        `SELECT commit_id, entity_kind, entity_key, entity_id, entity_version, operation FROM ${this.#a.tables.patch} ` +
          `WHERE graph = ?1 AND commit_id IN (SELECT value FROM json_each(?2))`,
        [this.#a.graph, "[" + commits.map(writeJsonString).join(",") + "]"],
      ).map((row) => ({
        commit: text(row["commit_id"], "commit_id"),
        kind: text(row["entity_kind"], "entity_kind"),
        entityKey: text(row["entity_key"], "entity_key"),
        entityId: text(row["entity_id"], "entity_id"),
        entityVersion: int(row["entity_version"], "entity_version"),
        operation: text(row["operation"], "operation") as Patch["operation"],
      }));
    } catch (err) {
      throw withContext("read patches", err);
    }
  }

  /** The root's highest sequence plus one: the file's one writer orders every tagger, so the root needs no lock of its own. */
  nextSequence(root: string): number {
    try {
      const row = this.#get(`SELECT COALESCE(MAX(sequence), 0) + 1 AS next FROM ${this.#a.tables.commit} WHERE graph = ?1 AND root_id = ?2`, [
        this.#a.graph,
        root,
      ]);
      return int(row?.["next"], "next");
    } catch (err) {
      throw withContext("read the next sequence", err);
    }
  }

  prune(kindName: string, retentionDays: number, batchSize: number): number {
    const k = this.#kind(kindName);
    // A kind declared without retentionDays keeps its history.
    if (k.retentionDays === undefined) {
      return 0;
    }
    if (!Number.isSafeInteger(batchSize) || batchSize < 0) {
      throw new RangeError(`sqlite: prune ${k.name} history: a batch is a whole number of images, 0 for no limit, not ${String(batchSize)}`);
    }
    const days = retentionDays !== 0 ? retentionDays : k.retentionDays;
    const t = this.#a.tables;
    // Older than the retention, not the newest image of its row, and pinned
    // by no patch and no snapshot; the oldest first.
    const sql =
      `DELETE FROM ${t.memberHistory} WHERE history_id IN (` +
      `SELECT h.history_id FROM ${t.memberHistory} AS h WHERE h.graph = ?1 AND h.kind = ?2 AND h.recorded_at < ?3 ` +
      `AND EXISTS (SELECT 1 FROM ${t.memberHistory} AS newer WHERE newer.id = h.id AND newer._version > h._version) ` +
      `AND NOT EXISTS (SELECT 1 FROM ${t.patch} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) ` +
      `AND NOT EXISTS (SELECT 1 FROM ${t.snapshot} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) ` +
      `ORDER BY h.recorded_at, h.id, h._version LIMIT ?4)`;
    try {
      return this.#run(sql, [this.#a.graph, k.name, this.#time - days * microsPerDay, batchSize === 0 ? -1 : batchSize]);
    } catch (err) {
      throw withContext(`prune ${k.name} history`, err);
    }
  }

  /** True: the one writer the file's write lock lets in is the only sweeper there can be. */
  sweepLock(): boolean {
    return true;
  }

  snapshot(commit: string): SnapshotEntry[] {
    try {
      return this.#all(
        `SELECT entity_kind, entity_key, entity_id, entity_version FROM ${this.#a.tables.snapshot} WHERE graph = ?1 AND commit_id = ?2`,
        [this.#a.graph, commit],
      ).map((row) => ({
        kind: text(row["entity_kind"], "entity_kind"),
        entityKey: text(row["entity_key"], "entity_key"),
        entityId: text(row["entity_id"], "entity_id"),
        entityVersion: int(row["entity_version"], "entity_version"),
      }));
    } catch (err) {
      throw withContext("read the snapshot", err);
    }
  }

  insertSnapshot(commit: string, entries: readonly SnapshotEntry[]): undefined {
    if (entries.length === 0) {
      return undefined;
    }
    try {
      this.#requireOwnCommit(commit, "the snapshot's commit");
      for (const e of entries) {
        const { kind, key, id } = this.#pinned(e.kind, e.entityKey, e.entityId);
        this.#run(
          `INSERT INTO ${this.#a.tables.snapshot} (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) ` +
            `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)`,
          [newID(), this.#a.graph, commit, kind.name, key, id, e.entityVersion],
        );
      }
    } catch (err) {
      throw withContext("write the snapshot", err);
    }
    return undefined;
  }

  commits(): CommitNode[] {
    try {
      return this.#all(
        `SELECT c.id, c.parent_commit_id, c.sequence IS NOT NULL AS tagged, ${this.#hasSnapshot("c.id")} AS snapshot ` +
          `FROM ${this.#a.tables.commit} AS c WHERE c.graph = ?1`,
        [this.#a.graph],
      ).map((row) => ({
        id: text(row["id"], "id"),
        parent: optionalText(row["parent_commit_id"], "parent_commit_id"),
        tagged: int(row["tagged"], "tagged") === 1,
        snapshot: int(row["snapshot"], "snapshot") === 1,
      }));
    } catch (err) {
      throw withContext("read the commits", err);
    }
  }

  readRelease(root: string): Release {
    let row: SqliteRow | undefined;
    try {
      row = this.#get(`SELECT ${releaseColumns} FROM ${this.#a.tables.release} WHERE graph = ?1 AND root_id = ?2`, [this.#a.graph, root]);
    } catch (err) {
      throw withContext("read the release", err);
    }
    if (row === undefined) {
      throw new NotFoundError();
    }
    return scanRelease(row);
  }

  writeRelease(write: ReleaseWrite): Release {
    const t = this.#a.tables;
    let row: SqliteRow | undefined;
    try {
      this.#requireCommit(write.commit, write.root, "the release's commit");
      if (write.version === 0) {
        // A root's first pointer. Another first pointer of the root holds
        // its slot, as a move at a stale version would.
        row = this.#get(
          `INSERT INTO ${t.release} (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) ` +
            `VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?5, ?6, 1) ON CONFLICT (graph, root_id) DO NOTHING RETURNING ${releaseColumns}`,
          [newID(), this.#a.graph, write.root, write.commit, this.#time, write.actor],
        );
      } else {
        row = this.#get(
          `UPDATE ${t.release} SET commit_id = ?3, updated_at = ?4, updated_by = ?5, _version = _version + 1 ` +
            `WHERE graph = ?1 AND root_id = ?2 AND _version = ?6 RETURNING ${releaseColumns}`,
          [this.#a.graph, write.root, write.commit, this.#time, write.actor, write.version],
        );
      }
      if (row === undefined) {
        throw new VersionConflictError();
      }
      const release = scanRelease(row);
      this.#history(t.releaseHistory, release.id, release.version, write.version === 0 ? "INSERT" : "UPDATE", releaseImage(row));
      return release;
    } catch (err) {
      throw withContext("write the release", err);
    }
  }

  discardedRefs(graceMs: number): Ref[] {
    return this.#refs(
      `SELECT ${refColumns} FROM ${this.#a.tables.ref} WHERE graph = ?1 AND deleted_at IS NOT NULL AND deleted_at < ?2 ORDER BY deleted_at, id`,
      [this.#a.graph, this.#time - micros(graceMs)],
    );
  }

  idleDrafts(idleMs: number): Ref[] {
    return this.#refs(
      `SELECT ${refColumns} FROM ${this.#a.tables.ref} ` +
        `WHERE graph = ?1 AND deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < ?2 ORDER BY updated_at, id`,
      [this.#a.graph, this.#time - micros(idleMs)],
    );
  }

  #refs(sql: string, params: readonly SqliteValue[]): Ref[] {
    try {
      return this.#all(sql, params).map(scanRef);
    } catch (err) {
      throw withContext("read refs", err);
    }
  }

  removeRefRows(kindName: string, ref: string, actor: string): number {
    const k = this.#kind(kindName);
    try {
      const found = this.#members(`SELECT ${memberColumns} FROM ${this.#a.tables.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3`, [
        this.#a.graph,
        k.name,
        ref,
      ]);
      for (const m of found) {
        this.#remove(k, m, actor);
      }
      return found.length;
    } catch (err) {
      throw withContext(`remove the ${k.name} rows of a ref`, err);
    }
  }
}

/** A duration in milliseconds as whole microseconds. */
function micros(ms: number): number {
  return Math.trunc(ms * 1000);
}

// The bindings. Each wraps an already-open database of its module, typed by
// the methods it calls, prepares each statement once and reuses it, returns
// plain rows (node:sqlite's have no prototype) and undefined for no row
// (bun:sqlite's get returns null), and throws a SqliteError carrying SQLite's
// extended result code (node:sqlite puts it in errcode, bun:sqlite in
// errno).

/** A prepared statement of node:sqlite or bun:sqlite, as the bindings call it. */
export interface SqliteModuleStatement {
  run(...params: SqliteValue[]): { changes: number | bigint };
  get(...params: SqliteValue[]): unknown;
  all(...params: SqliteValue[]): unknown[];
}

/** What nodeSqlite needs of a node:sqlite DatabaseSync. */
export interface NodeSqliteDatabase {
  prepare(sql: string): SqliteModuleStatement;
  exec(sql: string): void;
}

/** What bunSqlite needs of a bun:sqlite Database. */
export interface BunSqliteDatabase {
  prepare(sql: string): SqliteModuleStatement;
  exec(sql: string): unknown;
}

/** A client over an open node:sqlite DatabaseSync. */
export function nodeSqlite(db: NodeSqliteDatabase): SqliteClient {
  return moduleClient(db, (err) => {
    const fields = err as { code?: unknown; errcode?: unknown };
    return fields.code === "ERR_SQLITE_ERROR" && typeof fields.errcode === "number" ? fields.errcode : undefined;
  });
}

/** A client over an open bun:sqlite Database. */
export function bunSqlite(db: BunSqliteDatabase): SqliteClient {
  return moduleClient(db, (err) => {
    const fields = err as { name?: unknown; errno?: unknown };
    return fields.name === "SQLiteError" && typeof fields.errno === "number" ? fields.errno : undefined;
  });
}

function moduleClient(db: NodeSqliteDatabase | BunSqliteDatabase, codeOf: (err: Error) => number | undefined): SqliteClient {
  const statements = new Map<string, SqliteModuleStatement>();
  const normalize = (err: unknown): unknown => {
    if (!(err instanceof Error) || err instanceof SqliteError) {
      return err;
    }
    const code = codeOf(err);
    return code === undefined ? err : new SqliteError(err.message, code, { cause: err });
  };
  const prepare = (sql: string): SqliteModuleStatement => {
    let statement = statements.get(sql);
    if (statement === undefined) {
      try {
        statement = db.prepare(sql);
      } catch (err) {
        throw normalize(err);
      }
      statements.set(sql, statement);
    }
    return statement;
  };
  const plain = (row: unknown): SqliteRow => ({ ...(row as SqliteRow) });
  return {
    exec(sql: string): void {
      try {
        db.exec(sql);
      } catch (err) {
        throw normalize(err);
      }
    },
    run(sql: string, params: readonly SqliteValue[] = []): SqliteRunResult {
      const statement = prepare(sql);
      try {
        return { changes: statement.run(...params).changes };
      } catch (err) {
        throw normalize(err);
      }
    },
    get(sql: string, params: readonly SqliteValue[] = []): SqliteRow | undefined {
      const statement = prepare(sql);
      let row: unknown;
      try {
        row = statement.get(...params);
      } catch (err) {
        throw normalize(err);
      }
      return row === null || row === undefined ? undefined : plain(row);
    },
    all(sql: string, params: readonly SqliteValue[] = []): SqliteRow[] {
      const statement = prepare(sql);
      try {
        return statement.all(...params).map(plain);
      } catch (err) {
        throw normalize(err);
      }
    },
  };
}
