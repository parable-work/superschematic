// The Postgres storage adapter of the version-graph engine (D19), the
// TypeScript counterpart of the Go module's package postgres. It builds its
// statements at run time from a graph's descriptor (version 2), which names
// the graph's tables, each kind's role columns and every column's value
// class, and it returns every row as a canonical row (canonical.ts).
//
// The graph's own tables have the columns the loader gives them (D17, D19):
// a ref's root_id, parent_ref_id, base_commit_id, head_commit_id, name,
// sealed_at, audit and soft-delete columns and _version; a commit's root_id,
// ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence,
// created_at and created_by; a patch's commit_id, entity_kind, entity_key,
// entity_id, entity_version and operation; a snapshot entry's commit_id,
// entity_kind, entity_key, entity_id and entity_version; and a release
// pointer's root_id, commit_id, audit columns and _version. A history table
// keys its images on the kind's id and version columns and holds each in
// data.
//
// The adapter reaches Postgres through Client, which returns every column as
// the text Postgres writes for it, so no driver's type parsing touches a
// date, a time, an interval, a numeric or a bigint. pgPool and pgClient bind
// the npm package pg; they use only the methods they call, so this module
// imports nothing from pg and loads without it.

import { canonicalOf, canonicalRow, durationNanos, uuidCanonical, uuidHyphenated } from "./canonical.js";
import type { Descriptor } from "./contract.js";
import { compareCodePoints, isJsonObject, parseJson, writeJsonString, type JsonValue } from "./json.js";
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
  type Storage,
  type Tx,
} from "./storage.js";

/**
 * The transaction-local setting a versioned table's history trigger reads a
 * hard delete's actor from, unless the schema's history_actor_setting
 * naming key names another.
 */
export const DefaultHistoryActorSetting = "superschematic.history_actor_id";

// The audit columns the adapter writes on every row when a kind has them.
const createdAtColumn = "created_at";
const createdByColumn = "created_by";
const updatedAtColumn = "updated_at";
const updatedByColumn = "updated_by";

/**
 * A statement argument: a string, a number (an integer), a boolean, a list
 * of strings or of numbers, or null. Each statement casts its arguments to
 * their columns' types.
 */
export type Arg = string | number | boolean | null | readonly string[] | readonly number[];

/** The rows a statement returned, each column as the text Postgres writes for it (null for SQL NULL), and how many rows it affected. */
export interface QueryResult {
  rows: (string | null)[][];
  rowCount: number;
}

/** Runs statements inside one transaction. */
export interface Conn {
  /**
   * Runs a statement with positional arguments ($1, $2, ...) and returns
   * its rows, every column as text.
   */
  query(sql: string, args?: readonly Arg[]): Promise<QueryResult>;
}

/**
 * Runs the adapter's statements. The adapter asks it for one transaction
 * per engine operation. pgPool and pgClient bind pg; another driver
 * implements the interface itself. An error it rejects with for a unique
 * violation carries the SQLSTATE as `code` ("23505"), as pg's errors do.
 */
export interface Client {
  /** Runs fn in one transaction. It commits when fn resolves and rolls back when it rejects, rejecting with fn's error. */
  transact<T>(fn: (conn: Conn) => Promise<T>): Promise<T>;
}

/** Configure an adapter. */
export interface PostgresOptions {
  /**
   * The setting the history triggers read a hard delete's actor from: the
   * schema's history_actor_setting naming key. Absent or "" is
   * DefaultHistoryActorSetting.
   */
  historyActorSetting?: string;
}

/** One member kind's tables and columns. */
interface Kind {
  name: string;
  table: string;
  historyTable: string;
  key: string;
  id: string;
  ref: string;
  root: string;
  tombstone: string;
  version: string;
  columns: Record<string, string>;
}

/** What the adapter reads of a graph descriptor. */
interface DescriptorDoc {
  version?: number;
  root?: { table?: string; key?: string };
  refTable?: string;
  commitTable?: string;
  patchTable?: string;
  releaseTable?: string;
  snapshotTable?: string;
  kinds?: {
    kind?: string;
    table?: string;
    historyTable?: string;
    key?: string;
    id?: string;
    ref?: string;
    root?: string;
    tombstone?: string;
    version?: string;
    columns?: Record<string, string>;
  }[];
}

/** An adapter's statements' parts, built once from its descriptor. */
interface AdapterConfig {
  actorSetting: string;
  sweepKey: string;
  refTable: string;
  commitTable: string;
  patchTable: string;
  releaseTable: string;
  snapshotTable: string;
  rootTable: string;
  rootKey: string;
  kinds: Map<string, Kind>;
}

const configs = new WeakMap<PostgresAdapter, AdapterConfig>();

/** Quotes an identifier. */
function quote(name: string): string {
  return '"' + name.replace(/"/g, '""') + '"';
}

/** Quotes a string as a SQL literal. */
function literal(s: string): string {
  return "'" + s.replace(/'/g, "''") + "'";
}

/** A graph's statements, built from its descriptor. Safe to share; bind a Client with storage. */
export class PostgresAdapter {
  /**
   * Reads a graph's descriptor (JSON text or an object). The core checks
   * the rest of it; the adapter needs its tables, each kind's role columns,
   * the root column among them, and each kind's columns.
   */
  constructor(descriptor: string | Descriptor, options: PostgresOptions = {}) {
    const d = (typeof descriptor === "string" ? JSON.parse(descriptor) : descriptor) as unknown as DescriptorDoc;
    if (d.version !== 2) {
      throw new Error(`postgres: descriptor version ${String(d.version ?? 0)}; this adapter reads version 2`);
    }
    const tables: [string, string | undefined][] = [
      ["root table", d.root?.table],
      ["root key", d.root?.key],
      ["refTable", d.refTable],
      ["commitTable", d.commitTable],
      ["patchTable", d.patchTable],
      ["releaseTable", d.releaseTable],
      ["snapshotTable", d.snapshotTable],
    ];
    for (const [member, value] of tables) {
      if (value === undefined || value === "") {
        throw new Error(`postgres: the descriptor's ${member} is empty`);
      }
    }
    const config: AdapterConfig = {
      actorSetting: options.historyActorSetting || DefaultHistoryActorSetting,
      // The key every language's adapter takes the sweep lock under, so
      // their sweepers exclude each other.
      sweepKey: "superschematic.versiongraph.sweep:" + d.refTable!,
      refTable: quote(d.refTable!),
      commitTable: quote(d.commitTable!),
      patchTable: quote(d.patchTable!),
      releaseTable: quote(d.releaseTable!),
      snapshotTable: quote(d.snapshotTable!),
      rootTable: quote(d.root!.table!),
      rootKey: quote(d.root!.key!),
      kinds: new Map(),
    };
    for (const k of d.kinds ?? []) {
      const columns = k.columns ?? {};
      const roles: [string, string | undefined][] = [
        ["table", k.table],
        ["historyTable", k.historyTable],
        ["key", k.key],
        ["id", k.id],
        ["ref", k.ref],
        ["root", k.root],
        ["tombstone", k.tombstone],
        ["version", k.version],
      ];
      for (const [role, column] of roles) {
        if (column === undefined || column === "") {
          throw new Error(`postgres: kind ${JSON.stringify(k.kind)} has no ${role}`);
        }
        if (role !== "table" && role !== "historyTable" && !Object.prototype.hasOwnProperty.call(columns, column)) {
          throw new Error(`postgres: kind ${JSON.stringify(k.kind)} ${role} column ${JSON.stringify(column)} is not in its columns`);
        }
      }
      config.kinds.set(k.kind!, {
        name: k.kind!,
        table: k.table!,
        historyTable: k.historyTable!,
        key: k.key!,
        id: k.id!,
        ref: k.ref!,
        root: k.root!,
        tombstone: k.tombstone!,
        version: k.version!,
        columns,
      });
    }
    configs.set(this, config);
  }

  /** Binds the adapter to a client. */
  storage(client: Client): Storage {
    const config = configs.get(this)!;
    return {
      transact: <T>(fn: (tx: Tx) => Promise<T>): Promise<T> => client.transact((conn) => fn(new PostgresTx(config, conn))),
    };
  }
}

/** A ref's graph columns, as text. */
const refColumns =
  `id::text, root_id::text, COALESCE(parent_ref_id::text, ''), COALESCE(base_commit_id::text, ''), ` +
  `COALESCE(head_commit_id::text, ''), "name", sealed_at IS NOT NULL, deleted_at IS NOT NULL, _version`;

/** A commit's columns, followed by whether it has a snapshot. */
const commitColumns =
  `id::text, root_id::text, ref_id::text, COALESCE(parent_commit_id::text, ''), COALESCE(message, ''), ` +
  `schema_epoch, content_hash, COALESCE("sequence"::text, ''), to_jsonb(created_at)::text, created_by::text`;

/** A release pointer's columns. */
const releaseColumns = `id::text, root_id::text, commit_id::text, _version`;

/** Whether err is Postgres's unique_violation. */
function isUniqueViolation(err: unknown): boolean {
  return typeof err === "object" && err !== null && (err as { code?: unknown }).code === "23505";
}

function text(value: string | null | undefined): string {
  if (value === null || value === undefined) {
    throw new Error("postgres: a column the adapter reads is NULL");
  }
  return value;
}

function bool(value: string | null | undefined): boolean {
  const t = text(value);
  if (t === "t" || t === "true") {
    return true;
  }
  if (t === "f" || t === "false") {
    return false;
  }
  throw new Error(`postgres: ${JSON.stringify(t)} is not a boolean`);
}

function int(value: string | null | undefined): number {
  const t = text(value);
  if (!/^-?[0-9]+$/.test(t)) {
    throw new Error(`postgres: ${JSON.stringify(t)} is not an integer`);
  }
  const n = Number(t);
  if (!Number.isSafeInteger(n)) {
    throw new Error(`postgres: ${t} is past the integers a number holds exactly`);
  }
  return n;
}

/** The canonical form of a UUID Postgres rendered as text, null for "". */
function optionalID(value: string | null | undefined): string | null {
  const t = text(value);
  return t === "" ? null : uuidCanonical(t);
}

function requiredID(value: string | null | undefined): string {
  return uuidCanonical(text(value));
}

/** The hyphenated text a statement casts to uuid, "" for null. */
function uuidArg(id: string | null): string {
  return id === null || id === "" ? "" : uuidHyphenated(id);
}

function scanRef(row: (string | null)[]): Ref {
  return {
    id: requiredID(row[0]),
    root: requiredID(row[1]),
    parent: optionalID(row[2]),
    base: optionalID(row[3]),
    head: optionalID(row[4]),
    name: text(row[5]),
    sealed: bool(row[6]),
    discarded: bool(row[7]),
    version: int(row[8]),
  };
}

function scanCommit(row: (string | null)[]): Commit {
  const sequence = text(row[7]);
  const createdAt = JSON.parse(canonicalOf("dateTime", parseJson(text(row[8])))) as string;
  return {
    id: requiredID(row[0]),
    root: requiredID(row[1]),
    ref: requiredID(row[2]),
    parent: optionalID(row[3]),
    message: text(row[4]),
    schemaEpoch: int(row[5]),
    contentHash: text(row[6]),
    sequence: sequence === "" ? null : int(sequence),
    createdAt,
    createdBy: requiredID(row[9]),
    snapshot: bool(row[10]),
  };
}

function scanRelease(row: (string | null)[]): Release {
  return { id: requiredID(row[0]), root: requiredID(row[1]), commit: requiredID(row[2]), version: int(row[3]) };
}

/**
 * Turns a canonical value of a class into the JSON jsonb_populate_record
 * reads into the column: a UUID, or a list of them, hyphenated, and a
 * duration, or a list of them, as interval text. A json column and a list of
 * lists are JSONB and keep the canonical JSON, which is the schema
 * runtime's; every other class is already what Postgres reads.
 */
function inputValue(valueClass: string, value: JsonValue): string {
  const normalized = canonicalOf(valueClass, value);
  if (valueClass.endsWith("[][]")) {
    return normalized;
  }
  const depth = valueClass.endsWith("[]") ? 1 : 0;
  const element = depth === 1 ? valueClass.slice(0, -2) : valueClass;
  let convert: (s: string) => string;
  if (element === "uuid") {
    convert = uuidHyphenated;
  } else if (element === "duration") {
    convert = intervalText;
  } else {
    return normalized;
  }
  if (normalized === "null") {
    return normalized;
  }
  if (depth === 0) {
    return writeJsonString(convert(JSON.parse(normalized) as string));
  }
  return "[" + (JSON.parse(normalized) as string[]).map((s) => writeJsonString(convert(s))).join(",") + "]";
}

/** Writes a canonical duration as interval text Postgres reads exactly: [-]H:MM:SS with the fraction of a second. */
function intervalText(duration: string): string {
  let nanos = durationNanos(duration);
  let sign = "";
  if (nanos < 0n) {
    sign = "-";
    nanos = -nanos;
  }
  const seconds = nanos / 1_000_000_000n;
  const fraction = nanos % 1_000_000_000n;
  const two = (n: bigint) => String(n).padStart(2, "0");
  let out = `${sign}${two(seconds / 3600n)}:${two((seconds / 60n) % 60n)}:${two(seconds % 60n)}`;
  if (fraction > 0n) {
    out += ("." + String(fraction).padStart(9, "0")).replace(/0+$/, "");
  }
  return out;
}

/** One transaction's view of the graph, over a Conn. */
class PostgresTx implements Tx {
  readonly #a: AdapterConfig;
  readonly #conn: Conn;

  constructor(config: AdapterConfig, conn: Conn) {
    this.#a = config;
    this.#conn = conn;
  }

  #kind(name: string): Kind {
    const k = this.#a.kinds.get(name);
    if (k === undefined) {
      throw new Error(`postgres: unknown kind ${JSON.stringify(name)}`);
    }
    return k;
  }

  async #query(sql: string, args: readonly Arg[] = []): Promise<QueryResult> {
    return this.#conn.query(sql, args);
  }

  /** Runs a statement whose one column is a row's JSON as text, and returns each as the canonical row of kind. */
  async #rows(k: Kind, sql: string, args: readonly Arg[]): Promise<string[]> {
    const result = await this.#query(sql, args);
    return result.rows.map((row) => {
      try {
        return canonicalRow(k.columns, text(row[0]));
      } catch (err) {
        if (err instanceof Error) {
          err.message = `${k.name} row: ${err.message}`;
        }
        throw err;
      }
    });
  }

  async #ref(sql: string, args: readonly Arg[]): Promise<Ref | undefined> {
    const result = await this.#query(sql, args);
    return result.rows.length > 0 ? scanRef(result.rows[0]!) : undefined;
  }

  async createRef(ref: NewRef): Promise<Ref> {
    const sql =
      `INSERT INTO ${this.#a.refTable} (root_id, parent_ref_id, base_commit_id, "name", created_by, updated_by) ` +
      `VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5::uuid, $5::uuid) RETURNING ${refColumns}`;
    let out: Ref | undefined;
    try {
      out = await this.#ref(sql, [uuidArg(ref.root), uuidArg(ref.parent), uuidArg(ref.base), ref.name, uuidArg(ref.actor)]);
    } catch (err) {
      if (isUniqueViolation(err)) {
        throw new NameTakenError(`the root already has a live ref of that name: ${JSON.stringify(ref.name)}`);
      }
      throw withContext("create ref", err);
    }
    return out!;
  }

  readRef(id: string): Promise<Ref> {
    return this.#readRef(id, "");
  }

  lockRef(id: string): Promise<Ref> {
    return this.#readRef(id, " FOR UPDATE");
  }

  async #readRef(id: string, lock: string): Promise<Ref> {
    const arg = uuidArg(id);
    let ref: Ref | undefined;
    try {
      ref = await this.#ref(`SELECT ${refColumns} FROM ${this.#a.refTable} WHERE id = $1::uuid${lock}`, [arg]);
    } catch (err) {
      throw withContext("read ref", err);
    }
    if (ref === undefined) {
      throw new NotFoundError();
    }
    return ref;
  }

  async updateRef(update: RefUpdate): Promise<Ref> {
    const sql =
      `UPDATE ${this.#a.refTable} SET head_commit_id = COALESCE(NULLIF($3, '')::uuid, head_commit_id), ` +
      `base_commit_id = COALESCE(NULLIF($6, '')::uuid, base_commit_id), ` +
      `sealed_at = CASE WHEN $4::boolean THEN now() ELSE sealed_at END, updated_at = now(), updated_by = $5::uuid ` +
      `WHERE id = $1::uuid AND _version = $2 RETURNING ${refColumns}`;
    let ref: Ref | undefined;
    try {
      ref = await this.#ref(sql, [
        uuidArg(update.id),
        update.version,
        uuidArg(update.head),
        update.seal,
        uuidArg(update.actor),
        uuidArg(update.base),
      ]);
    } catch (err) {
      throw withContext("update ref", err);
    }
    if (ref === undefined) {
      throw new VersionConflictError();
    }
    return ref;
  }

  async discardRef(id: string, version: number, actor: string): Promise<void> {
    let result: QueryResult;
    try {
      result = await this.#query(
        `UPDATE ${this.#a.refTable} SET deleted_at = now(), deleted_by = $3::uuid ` +
          `WHERE id = $1::uuid AND _version = $2 AND deleted_at IS NULL`,
        [uuidArg(id), version, uuidArg(actor)],
      );
    } catch (err) {
      throw withContext("discard ref", err);
    }
    if (result.rowCount === 0) {
      throw new VersionConflictError();
    }
  }

  async rows(kindName: string, ref: string): Promise<string[]> {
    const k = this.#kind(kindName);
    try {
      return await this.#rows(k, `SELECT to_jsonb(t)::text FROM ${quote(k.table)} AS t WHERE t.${quote(k.ref)} = $1::uuid`, [
        uuidArg(ref),
      ]);
    } catch (err) {
      throw withContext(`read ${k.name} rows`, err);
    }
  }

  async upsertRow(kindName: string, write: RowWrite): Promise<string> {
    const k = this.#kind(kindName);
    const parsed = parseJson(write.row);
    if (!isJsonObject(parsed)) {
      throw new Error(`postgres: a ${k.name} row is a JSON object`);
    }
    const members = new Map(parsed);
    members.delete(k.id);
    members.delete(k.version);
    if (members.get(k.key) === null) {
      members.delete(k.key);
    }
    members.set(k.tombstone, write.tombstone);
    members.set(k.ref, write.ref);
    members.set(k.root, write.root);
    for (const column of [createdByColumn, updatedByColumn]) {
      if (Object.prototype.hasOwnProperty.call(k.columns, column)) {
        members.set(column, write.actor);
      }
    }
    const input = new Map<string, string>();
    const columns: string[] = [];
    for (const [column, value] of members) {
      const valueClass = Object.prototype.hasOwnProperty.call(k.columns, column) ? k.columns[column] : undefined;
      if (valueClass === undefined) {
        throw new Error(`postgres: the ${k.name} row has column ${JSON.stringify(column)}, which its descriptor does not declare`);
      }
      try {
        input.set(column, inputValue(valueClass, value));
      } catch (err) {
        throw withContext(`${k.name} column ${column}`, err);
      }
      columns.push(column);
    }
    for (const column of [createdAtColumn, updatedAtColumn]) {
      if (Object.prototype.hasOwnProperty.call(k.columns, column) && !input.has(column)) {
        columns.push(column);
      }
    }
    columns.sort(compareCodePoints);
    const inputJSON =
      "{" +
      [...input.keys()]
        .sort(compareCodePoints)
        .map((column) => writeJsonString(column) + ":" + input.get(column)!)
        .join(",") +
      "}";

    const names: string[] = [];
    const values: string[] = [];
    const updates: string[] = [];
    for (const column of columns) {
      const q = quote(column);
      names.push(q);
      const valueClass = k.columns[column]!;
      if (column === createdAtColumn || column === updatedAtColumn) {
        values.push("now()");
      } else if (valueClass === "json" || valueClass.endsWith("[][]")) {
        // A JSONB column takes the member itself, so a JSON null is the
        // value null, which a required column holds, rather than SQL NULL,
        // which jsonb_populate_record reads it as.
        values.push(`($1::jsonb -> ${literal(column)})`);
      } else {
        values.push("r." + q);
      }
      if (column !== k.key && column !== k.ref && column !== k.root && column !== createdAtColumn && column !== createdByColumn) {
        // The conflict key, the root and the creation audit stay as the
        // row was first written.
        updates.push(`${q} = EXCLUDED.${q}`);
      }
    }
    const table = quote(k.table);
    const sql =
      `INSERT INTO ${table} AS t (${names.join(", ")}) ` +
      `SELECT ${values.join(", ")} FROM jsonb_populate_record(NULL::${table}, $1::jsonb) AS r ` +
      `ON CONFLICT (${quote(k.key)}, ${quote(k.ref)}) DO UPDATE SET ${updates.join(", ")} ` +
      `RETURNING to_jsonb(t)::text`;
    let rows: string[];
    try {
      rows = await this.#rows(k, sql, [inputJSON]);
    } catch (err) {
      throw withContext(`write ${k.name} row`, err);
    }
    if (rows.length !== 1) {
      throw new Error(`postgres: write ${k.name} row: ${rows.length} rows written`);
    }
    return rows[0]!;
  }

  async removeRow(kindName: string, ref: string, entityKey: string, actor: string): Promise<boolean> {
    const k = this.#kind(kindName);
    const sql =
      `WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) ` +
      `DELETE FROM ${quote(k.table)} USING history_actor WHERE ${quote(k.key)} = $3::uuid AND ${quote(k.ref)} = $4::uuid`;
    let result: QueryResult;
    try {
      result = await this.#query(sql, [this.#a.actorSetting, uuidArg(actor), uuidArg(entityKey), uuidArg(ref)]);
    } catch (err) {
      throw withContext(`remove the ${k.name} row`, err);
    }
    await this.#clearActor();
    return result.rowCount > 0;
  }

  async #clearActor(): Promise<void> {
    try {
      await this.#query(`SELECT set_config($1, '', true)`, [this.#a.actorSetting]);
    } catch (err) {
      throw withContext("clear the history actor", err);
    }
  }

  async images(kindName: string, pins: readonly Pin[]): Promise<string[]> {
    const k = this.#kind(kindName);
    if (pins.length === 0) {
      return [];
    }
    const sql =
      `SELECT h.data::text FROM ${quote(k.historyTable)} AS h ` +
      `JOIN unnest($1::text[]::uuid[], $2::bigint[]) AS p(id, version) ON h.${quote(k.id)} = p.id AND h.${quote(k.version)} = p.version`;
    try {
      return await this.#rows(
        k,
        sql,
        [pins.map((pin) => uuidHyphenated(pin.id)), pins.map((pin) => pin.version)],
      );
    } catch (err) {
      throw withContext(`read ${k.name} history`, err);
    }
  }

  /** The SQL that tells whether the commit whose id is idSQL has a snapshot. */
  #hasSnapshot(idSQL: string): string {
    return `EXISTS (SELECT 1 FROM ${this.#a.snapshotTable} AS s WHERE s.commit_id = ${idSQL})`;
  }

  async #commits(sql: string, args: readonly Arg[]): Promise<Commit[]> {
    const result = await this.#query(sql, args);
    return result.rows.map(scanCommit);
  }

  async readCommit(id: string): Promise<Commit> {
    let commits: Commit[];
    try {
      commits = await this.#commits(
        `SELECT ${commitColumns}, ${this.#hasSnapshot("c.id")} FROM ${this.#a.commitTable} AS c WHERE id = $1::uuid`,
        [uuidArg(id)],
      );
    } catch (err) {
      throw withContext("read commit", err);
    }
    if (commits.length === 0) {
      throw new NotFoundError();
    }
    return commits[0]!;
  }

  async insertCommit(commit: NewCommit): Promise<Commit> {
    const sql =
      `INSERT INTO ${this.#a.commitTable} (root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, "sequence", created_by) ` +
      `VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7::bigint, $8::uuid) RETURNING ${commitColumns}, false`;
    try {
      const commits = await this.#commits(sql, [
        uuidArg(commit.root),
        uuidArg(commit.ref),
        uuidArg(commit.parent),
        commit.message,
        commit.schemaEpoch,
        commit.contentHash,
        commit.sequence,
        uuidArg(commit.actor),
      ]);
      return commits[0]!;
    } catch (err) {
      throw withContext("write commit", err);
    }
  }

  async insertPatches(commit: string, patches: readonly Patch[]): Promise<void> {
    const payload =
      "[" +
      patches
        .map(
          (p) =>
            `{"kind":${writeJsonString(p.kind)},"key":${writeJsonString(uuidHyphenated(p.entityKey))},` +
            `"id":${writeJsonString(uuidHyphenated(p.entityId))},"version":${p.entityVersion},"op":${writeJsonString(p.operation)}}`,
        )
        .join(",") +
      "]";
    const sql =
      `INSERT INTO ${this.#a.patchTable} (commit_id, entity_kind, entity_key, entity_id, entity_version, operation) ` +
      `SELECT $1::uuid, p.kind, p.key::uuid, p.id::uuid, p.version, p.op ` +
      `FROM jsonb_to_recordset($2::jsonb) AS p(kind text, key text, id text, version bigint, op text)`;
    try {
      await this.#query(sql, [uuidArg(commit), payload]);
    } catch (err) {
      throw withContext("write patches", err);
    }
  }

  async walk(commit: string, limit: number): Promise<Commit[]> {
    // The walk carries each commit's snapshot flag, and goes no further than
    // the first commit that has one.
    const sql =
      `WITH RECURSIVE chain AS (` +
      `SELECT c.*, 1 AS depth, ${this.#hasSnapshot("c.id")} AS snapshotted FROM ${this.#a.commitTable} AS c WHERE c.id = $1::uuid ` +
      `UNION ALL SELECT c.*, chain.depth + 1, ${this.#hasSnapshot("c.id")} FROM ${this.#a.commitTable} AS c ` +
      `JOIN chain ON c.id = chain.parent_commit_id WHERE chain.depth < $2 AND NOT chain.snapshotted` +
      `) SELECT ${commitColumns}, snapshotted FROM chain ORDER BY depth`;
    try {
      return await this.#commits(sql, [uuidArg(commit), limit]);
    } catch (err) {
      throw withContext("walk commits", err);
    }
  }

  async refCommits(ref: string, head: string, limit: number): Promise<Commit[]> {
    const sql =
      `WITH RECURSIVE chain AS (` +
      `SELECT c.*, 1 AS depth FROM ${this.#a.commitTable} AS c WHERE c.id = $1::uuid AND c.ref_id = $2::uuid ` +
      `UNION ALL SELECT c.*, chain.depth + 1 FROM ${this.#a.commitTable} AS c ` +
      `JOIN chain ON c.id = chain.parent_commit_id WHERE c.ref_id = $2::uuid AND chain.depth < $3` +
      `) SELECT ${commitColumns}, ${this.#hasSnapshot("chain.id")} FROM chain ORDER BY depth`;
    try {
      return await this.#commits(sql, [uuidArg(head), uuidArg(ref), limit]);
    } catch (err) {
      throw withContext("list commits", err);
    }
  }

  async patches(commits: readonly string[]): Promise<Patch[]> {
    if (commits.length === 0) {
      return [];
    }
    const sql =
      `SELECT commit_id::text, entity_kind, entity_key::text, entity_id::text, entity_version, operation ` +
      `FROM ${this.#a.patchTable} WHERE commit_id = ANY($1::text[]::uuid[])`;
    try {
      const result = await this.#query(sql, [commits.map(uuidHyphenated)]);
      return result.rows.map((row) => ({
        commit: requiredID(row[0]),
        kind: text(row[1]),
        entityKey: requiredID(row[2]),
        entityId: requiredID(row[3]),
        entityVersion: int(row[4]),
        operation: text(row[5]) as Patch["operation"],
      }));
    } catch (err) {
      throw withContext("read patches", err);
    }
  }

  async nextSequence(root: string): Promise<number> {
    const arg = uuidArg(root);
    // FOR NO KEY UPDATE conflicts with itself and not with the key-share
    // locks that inserting a row that references the root takes.
    try {
      await this.#query(`SELECT 1 FROM ${this.#a.rootTable} WHERE ${this.#a.rootKey} = $1::uuid FOR NO KEY UPDATE`, [arg]);
    } catch (err) {
      throw withContext("lock the root", err);
    }
    try {
      const result = await this.#query(
        `SELECT COALESCE(MAX("sequence"), 0) + 1 FROM ${this.#a.commitTable} WHERE root_id = $1::uuid`,
        [arg],
      );
      return int(result.rows[0]![0]);
    } catch (err) {
      throw withContext("read the next sequence", err);
    }
  }

  async prune(kindName: string, retentionDays: number, batchSize: number): Promise<number> {
    const k = this.#kind(kindName);
    // A kind declared without retentionDays has no prune function, and keeps
    // its history.
    const fn = quote(k.table + "_prune_history");
    let exists: boolean;
    try {
      const result = await this.#query(`SELECT to_regproc($1) IS NOT NULL`, [fn]);
      exists = bool(result.rows[0]![0]);
    } catch (err) {
      throw withContext(`find the ${k.name} prune function`, err);
    }
    if (!exists) {
      return 0;
    }
    // The prune function's retention_days defaults to the kind's declared
    // retention, which a retentionDays of 0 keeps.
    let sql = `SELECT ${fn}(max_rows => NULLIF($2, 0)::integer, retention_days => $1::integer)`;
    let args: Arg[] = [retentionDays, batchSize];
    if (retentionDays === 0) {
      sql = `SELECT ${fn}(max_rows => NULLIF($1, 0)::integer)`;
      args = [batchSize];
    }
    try {
      const result = await this.#query(sql, args);
      return int(result.rows[0]![0]);
    } catch (err) {
      throw withContext(`prune ${k.name} history`, err);
    }
  }

  async sweepLock(): Promise<boolean> {
    try {
      const result = await this.#query(`SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, [this.#a.sweepKey]);
      return bool(result.rows[0]![0]);
    } catch (err) {
      throw withContext("take the sweep lock", err);
    }
  }

  async snapshot(commit: string): Promise<SnapshotEntry[]> {
    const sql = `SELECT entity_kind, entity_key::text, entity_id::text, entity_version FROM ${this.#a.snapshotTable} WHERE commit_id = $1::uuid`;
    try {
      const result = await this.#query(sql, [uuidArg(commit)]);
      return result.rows.map((row) => ({
        kind: text(row[0]),
        entityKey: requiredID(row[1]),
        entityId: requiredID(row[2]),
        entityVersion: int(row[3]),
      }));
    } catch (err) {
      throw withContext("read the snapshot", err);
    }
  }

  async insertSnapshot(commit: string, entries: readonly SnapshotEntry[]): Promise<void> {
    if (entries.length === 0) {
      return;
    }
    const payload =
      "[" +
      entries
        .map(
          (e) =>
            `{"kind":${writeJsonString(e.kind)},"key":${writeJsonString(uuidHyphenated(e.entityKey))},` +
            `"id":${writeJsonString(uuidHyphenated(e.entityId))},"version":${e.entityVersion}}`,
        )
        .join(",") +
      "]";
    const sql =
      `INSERT INTO ${this.#a.snapshotTable} (commit_id, entity_kind, entity_key, entity_id, entity_version) ` +
      `SELECT $1::uuid, e.kind, e.key::uuid, e.id::uuid, e.version ` +
      `FROM jsonb_to_recordset($2::jsonb) AS e(kind text, key text, id text, version bigint)`;
    try {
      await this.#query(sql, [uuidArg(commit), payload]);
    } catch (err) {
      throw withContext("write the snapshot", err);
    }
  }

  async commits(): Promise<CommitNode[]> {
    const sql =
      `SELECT c.id::text, COALESCE(c.parent_commit_id::text, ''), c."sequence" IS NOT NULL, ${this.#hasSnapshot("c.id")} ` +
      `FROM ${this.#a.commitTable} AS c`;
    try {
      const result = await this.#query(sql, []);
      return result.rows.map((row) => ({
        id: requiredID(row[0]),
        parent: optionalID(row[1]),
        tagged: bool(row[2]),
        snapshot: bool(row[3]),
      }));
    } catch (err) {
      throw withContext("read the commits", err);
    }
  }

  async readRelease(root: string): Promise<Release> {
    let result: QueryResult;
    try {
      result = await this.#query(`SELECT ${releaseColumns} FROM ${this.#a.releaseTable} WHERE root_id = $1::uuid`, [
        uuidArg(root),
      ]);
    } catch (err) {
      throw withContext("read the release", err);
    }
    if (result.rows.length === 0) {
      throw new NotFoundError();
    }
    return scanRelease(result.rows[0]!);
  }

  async writeRelease(write: ReleaseWrite): Promise<Release> {
    let sql: string;
    let args: Arg[];
    if (write.version === 0) {
      // A root's first pointer. Another writer's first pointer takes the
      // root's slot, as a move at a stale version would.
      sql =
        `INSERT INTO ${this.#a.releaseTable} (root_id, commit_id, created_by, updated_by) ` +
        `VALUES ($1::uuid, $2::uuid, $3::uuid, $3::uuid) ON CONFLICT (root_id) DO NOTHING RETURNING ${releaseColumns}`;
      args = [uuidArg(write.root), uuidArg(write.commit), uuidArg(write.actor)];
    } else {
      sql =
        `UPDATE ${this.#a.releaseTable} SET commit_id = $2::uuid, updated_at = now(), updated_by = $3::uuid ` +
        `WHERE root_id = $1::uuid AND _version = $4 RETURNING ${releaseColumns}`;
      args = [uuidArg(write.root), uuidArg(write.commit), uuidArg(write.actor), write.version];
    }
    let result: QueryResult;
    try {
      result = await this.#query(sql, args);
    } catch (err) {
      throw withContext("write the release", err);
    }
    if (result.rows.length === 0) {
      throw new VersionConflictError();
    }
    return scanRelease(result.rows[0]!);
  }

  discardedRefs(graceMs: number): Promise<Ref[]> {
    return this.#refs(
      `SELECT ${refColumns} FROM ${this.#a.refTable} ` +
        `WHERE deleted_at IS NOT NULL AND deleted_at < now() - $1::bigint * interval '1 microsecond' ORDER BY deleted_at, id`,
      [micros(graceMs)],
    );
  }

  idleDrafts(idleMs: number): Promise<Ref[]> {
    return this.#refs(
      `SELECT ${refColumns} FROM ${this.#a.refTable} ` +
        `WHERE deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < now() - $1::bigint * interval '1 microsecond' ORDER BY updated_at, id`,
      [micros(idleMs)],
    );
  }

  async #refs(sql: string, args: readonly Arg[]): Promise<Ref[]> {
    try {
      const result = await this.#query(sql, args);
      return result.rows.map(scanRef);
    } catch (err) {
      throw withContext("read refs", err);
    }
  }

  async removeRefRows(kindName: string, ref: string, actor: string): Promise<number> {
    const k = this.#kind(kindName);
    const sql =
      `WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) ` +
      `DELETE FROM ${quote(k.table)} USING history_actor WHERE ${quote(k.ref)} = $3::uuid`;
    let result: QueryResult;
    try {
      result = await this.#query(sql, [this.#a.actorSetting, uuidArg(actor), uuidArg(ref)]);
    } catch (err) {
      throw withContext(`remove the ${k.name} rows of a ref`, err);
    }
    await this.#clearActor();
    return result.rowCount;
  }
}

/** A duration in milliseconds as whole microseconds, as a statement multiplies an interval of one microsecond by it. */
function micros(ms: number): number {
  return Math.trunc(ms * 1000);
}

/**
 * Prefixes an error's message with what the adapter was doing, keeping the
 * error itself (its class and its code) so callers can still tell it apart.
 */
function withContext(what: string, err: unknown): unknown {
  if (err instanceof NotFoundError || err instanceof NameTakenError) {
    return err;
  }
  if (err instanceof Error && !err.message.startsWith("postgres: ")) {
    err.message = `postgres: ${what}: ${err.message}`;
  }
  return err;
}

/** The query config pg takes: every column comes back as the text Postgres writes, unparsed. */
interface PgQueryConfig {
  text: string;
  values: unknown[];
  rowMode: "array";
  types: { getTypeParser: (oid: number, format?: string) => (value: string) => unknown };
}

/** What pgClient needs of a pg Client or PoolClient. */
export interface PgQueryable {
  query(config: PgQueryConfig): Promise<{ rows: unknown[][]; rowCount: number | null }>;
}

/** What pgPool needs of a pg Pool. */
export interface PgPool {
  connect(): Promise<PgQueryable & { release(err?: Error | boolean): void }>;
}

const rawText: PgQueryConfig["types"] = { getTypeParser: () => (value: string) => value };

function pgConn(queryable: PgQueryable): Conn {
  return {
    async query(sql: string, args: readonly Arg[] = []): Promise<QueryResult> {
      const result = await queryable.query({
        text: sql,
        values: args.map((arg) => (Array.isArray(arg) ? [...arg] : arg)),
        rowMode: "array",
        types: rawText,
      });
      return {
        rows: result.rows.map((row) => row.map((value) => (value === null || value === undefined ? null : String(value)))),
        rowCount: result.rowCount ?? 0,
      };
    },
  };
}

async function run(queryable: PgQueryable, sql: string): Promise<void> {
  await queryable.query({ text: sql, values: [], rowMode: "array", types: rawText });
}

/**
 * The default Client over a pg Pool: each transaction runs on a connection
 * of its own, taken from the pool and returned when it ends.
 */
export function pgPool(pool: PgPool): Client {
  return {
    async transact<T>(fn: (conn: Conn) => Promise<T>): Promise<T> {
      const client = await pool.connect();
      // A connection whose transaction could not be ended is not returned
      // to the pool.
      let broken: Error | undefined;
      try {
        await run(client, "BEGIN");
        let out: T;
        try {
          out = await fn(pgConn(client));
        } catch (err) {
          try {
            await run(client, "ROLLBACK");
          } catch (rollbackErr) {
            broken = rollbackErr instanceof Error ? rollbackErr : new Error(String(rollbackErr));
          }
          throw err;
        }
        try {
          await run(client, "COMMIT");
        } catch (err) {
          broken = err instanceof Error ? err : new Error(String(err));
          throw err;
        }
        return out;
      } finally {
        client.release(broken ?? false);
      }
    },
  };
}

/** Configure pgClient. */
export interface PgClientOptions {
  /**
   * Runs each transaction as a savepoint, inside a transaction the caller
   * already holds on the connection, rather than as a transaction of its
   * own.
   */
  savepoint?: boolean;
}

/**
 * A Client over one pg connection (a Client or a PoolClient). Its
 * transactions run one at a time, in the order they were asked for. With
 * options.savepoint each is a savepoint of the transaction the caller holds.
 */
export function pgClient(client: PgQueryable, options: PgClientOptions = {}): Client {
  let queue: Promise<unknown> = Promise.resolve();
  const [begin, commit, rollback] = options.savepoint
    ? ["SAVEPOINT superschematic_versiongraph", "RELEASE SAVEPOINT superschematic_versiongraph", "ROLLBACK TO SAVEPOINT superschematic_versiongraph"]
    : ["BEGIN", "COMMIT", "ROLLBACK"];
  return {
    transact<T>(fn: (conn: Conn) => Promise<T>): Promise<T> {
      const next = queue.then(async () => {
        await run(client, begin);
        let out: T;
        try {
          out = await fn(pgConn(client));
        } catch (err) {
          try {
            await run(client, rollback);
            if (options.savepoint) {
              await run(client, commit);
            }
          } catch {
            // The error that ended the transaction is the one to report.
          }
          throw err;
        }
        await run(client, commit);
        return out;
      });
      queue = next.catch(() => undefined);
      return next;
    },
  };
}
