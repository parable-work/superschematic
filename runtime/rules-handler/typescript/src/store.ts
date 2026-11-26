// Development mode's store over SQLite (D117): the development layout in a
// SQLite database, the tables and the statements superschematic-rules-dev's
// SqliteStore keeps (runtime/engine/rust/rules-dev/src/lib.rs), so a file
// one host wrote the other reads. It takes a connection of any synchronous
// SQLite binding whose statements bind positional parameters: bun:sqlite's
// Database (./sqlite opens one), node:sqlite's DatabaseSync, or a
// Worker's or a browser's once it is wrapped to this shape.
//
// | Table | What it holds |
// |-------|---------------|
// | dev_schema_versions | each type's published versions: the type (schema), the number, the schema document |
// | dev_schema_drafts | each type's version defined and not published yet |
// | dev_instances | the rows: the type, the id, the version, the own fields as one JSON column (data) and the owned fields as another (owned) |
// | dev_sub_<behavior> | one shared sub-table per behavior that keeps sub-table rows per row, keyed by type, id, use, table and key |
// | dev_shared | the rows of the uses' shared tables, keyed by type, use, table and key, each with its version |
// | dev_deadlines | the armed deadlines: the row, the use, the name, the time armed and when due |
// | dev_events | the event queue (D104), in the order written |
// | dev_changes | the change queue (D103): each heard write, run after the commit |
//
// Each request runs in one transaction begun IMMEDIATE, so it holds the
// database's one writer from its start. A unit applies in a savepoint,
// whole or not at all. A read answers with the JSON text the columns hold,
// spliced into its answer as it is (RawJson).

import type { DueDeadline, Layout, QueuedChange, QueuedEvent, StoreRead, StoreWrite, Unit } from './contract.js';
import { refusalOf, RefusalError, type Store } from './host.js';
import { encode, JSON_CODEC, raw, RawJson, type Codec } from './json.js';

/** The version of the layout this store writes, kept in dev_meta: rules-dev's LAYOUT_VERSION. */
export const LAYOUT_VERSION = '1';

/** A value a statement binds. */
export type SqlValue = string | number | bigint | null | Uint8Array;

/** A prepared statement of a synchronous SQLite binding. */
export interface SqliteStatement {
  /** Every row, each an object by column name. */
  all(...params: SqlValue[]): unknown[];
  /** The first row, or null or undefined for none. */
  get(...params: SqlValue[]): unknown;
  /** Runs a write: how many rows it changed. */
  run(...params: SqlValue[]): { changes: number | bigint };
}

/** A synchronous SQLite connection: bun:sqlite's Database and node:sqlite's DatabaseSync both fit. */
export interface SqliteConnection {
  exec(sql: string): unknown;
  prepare(sql: string): SqliteStatement;
}

const TABLES = `CREATE TABLE IF NOT EXISTS dev_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_schema_versions (
               schema TEXT NOT NULL, version INTEGER NOT NULL, document TEXT NOT NULL,
               PRIMARY KEY (schema, version));
             CREATE TABLE IF NOT EXISTS dev_schema_drafts (
               schema TEXT PRIMARY KEY, document TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_instances (
               schema TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL,
               data TEXT NOT NULL, owned TEXT NOT NULL,
               PRIMARY KEY (schema, id));
             CREATE TABLE IF NOT EXISTS dev_deadlines (
               schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL,
               at INTEGER NOT NULL, due INTEGER NOT NULL, payload TEXT NOT NULL,
               PRIMARY KEY (schema, id, use_id, name));
             CREATE INDEX IF NOT EXISTS dev_deadlines_due ON dev_deadlines (due);
             CREATE TABLE IF NOT EXISTS dev_events (
               seq INTEGER PRIMARY KEY AUTOINCREMENT, schema TEXT NOT NULL, id TEXT NOT NULL,
               use_id TEXT NOT NULL, kind TEXT NOT NULL, at INTEGER NOT NULL, payload TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_changes (
               seq INTEGER PRIMARY KEY AUTOINCREMENT, schema TEXT NOT NULL, id TEXT NOT NULL,
               change TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_shared (
               schema TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL, key TEXT NOT NULL,
               data TEXT NOT NULL, version INTEGER NOT NULL,
               PRIMARY KEY (schema, use_id, tbl, key));`;

function subTableDdl(table: string): string {
  return `CREATE TABLE IF NOT EXISTS "${table}" (
                   schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL,
                   key TEXT NOT NULL, data TEXT NOT NULL,
                   PRIMARY KEY (schema, id, use_id, tbl, key));`;
}

// A store failure as the request's refusal: busy while another writer holds
// the database past the busy timeout, internal for any other.
function failed(error: unknown): RefusalError {
  if (error instanceof RefusalError) {
    return error;
  }
  const message = error instanceof Error ? error.message : String(error);
  const code = (error as { code?: unknown; errcode?: unknown } | null)?.code;
  const errcode = (error as { errcode?: unknown } | null)?.errcode;
  if (code === 'SQLITE_BUSY' || errcode === 5 || /database is locked|SQLITE_BUSY/i.test(message)) {
    return new RefusalError(refusalOf('busy', `the development store is busy with another writer: ${message}`));
  }
  return new RefusalError(refusalOf('internal', `the development store failed: ${message}`));
}

// Why a write stopped short: a fence that did not hold.
class Moved extends Error {}

/** Code point order, as Rust and SQLite's BINARY collation order text. */
function byCodePoint(a: string, b: string): number {
  const left = [...a];
  const right = [...b];
  for (let i = 0; i < Math.min(left.length, right.length); i++) {
    const x = left[i]!.codePointAt(0)!;
    const y = right[i]!.codePointAt(0)!;
    if (x !== y) return x - y;
  }
  return left.length - right.length;
}

type Row = Record<string, unknown>;

function count(changes: number | bigint): number {
  return typeof changes === 'bigint' ? Number(changes) : changes;
}

/** The development layout in a SQLite database. */
export class SqliteStore implements Store {
  /** The connection, for a test's or an operator's own SQL. */
  readonly connection: SqliteConnection;
  readonly #codec: Codec;
  /** The behaviors whose shared sub-tables the store holds, by name: each one's table. */
  readonly #subTables = new Map<string, string>();
  readonly #statements = new Map<string, SqliteStatement>();
  #open = false;

  /**
   * The store over a connection, its tables created when they are not
   * there, for the behaviors the handler's layout declares
   * (HandlerModule.layout).
   */
  constructor(connection: SqliteConnection, layout: Layout, codec: Codec = JSON_CODEC) {
    this.connection = connection;
    this.#codec = codec;
    try {
      connection.exec('PRAGMA busy_timeout = 5000');
      connection.exec(TABLES);
      this.#run("INSERT INTO dev_meta (key, value) VALUES ('layout', ?) ON CONFLICT (key) DO NOTHING", LAYOUT_VERSION);
      for (const behavior of [...layout.behaviors].sort((a, b) => byCodePoint(a.name, b.name))) {
        if (!behavior.rowTables) continue;
        const table = `dev_sub_${behavior.storageKey}`;
        connection.exec(subTableDdl(table));
        this.#subTables.set(behavior.name, table);
      }
    } catch (error) {
      throw failed(error);
    }
  }

  /** Closes the connection, when it can be closed. */
  close(): void {
    this.#statements.clear();
    (this.connection as Partial<{ close(): void }>).close?.();
  }

  #statement(sql: string): SqliteStatement {
    let statement = this.#statements.get(sql);
    if (statement === undefined) {
      statement = this.connection.prepare(sql);
      this.#statements.set(sql, statement);
    }
    return statement;
  }

  #all(sql: string, ...params: SqlValue[]): Row[] {
    return this.#statement(sql).all(...params) as Row[];
  }

  #get(sql: string, ...params: SqlValue[]): Row | undefined {
    return (this.#statement(sql).get(...params) as Row | null | undefined) ?? undefined;
  }

  #run(sql: string, ...params: SqlValue[]): number {
    return count(this.#statement(sql).run(...params).changes);
  }

  // one runs a write and holds it to change one row.
  #one(sql: string, ...params: SqlValue[]): void {
    if (this.#run(sql, ...params) !== 1) {
      throw new Moved();
    }
  }

  #text(value: unknown): string {
    return encode(value, this.#codec);
  }

  #subTable(behavior: string): string {
    const table = this.#subTables.get(behavior);
    if (table === undefined) {
      throw new RefusalError(refusalOf('internal', `the development store keeps no sub-table of ${behavior}, which declares none`));
    }
    return table;
  }

  #rowJson(id: string, row: Row): string {
    return `{"id":${JSON.stringify(id)},"version":${Number(row.version)},"data":${String(row.data)},"owned":${String(row.owned)}}`;
  }

  #row(type: string, id: string): string | undefined {
    const row = this.#get('SELECT version, data, owned FROM dev_instances WHERE schema = ? AND id = ?', type, id);
    return row === undefined ? undefined : this.#rowJson(id, row);
  }

  #rows(type: string): string[] {
    return this.#all('SELECT id, version, data, owned FROM dev_instances WHERE schema = ? ORDER BY id', type).map(row => this.#rowJson(String(row.id), row));
  }

  #subRowsText(type: string, id: string, useId: string, table: string): { key: string; text: string }[] {
    const out: { key: string; text: string }[] = [];
    for (const subTable of this.#subTables.values()) {
      for (const row of this.#all(`SELECT key, data FROM "${subTable}" WHERE schema = ? AND id = ? AND use_id = ? AND tbl = ? ORDER BY key`, type, id, useId, table)) {
        const key = String(row.key);
        out.push({ key, text: `{"key":${JSON.stringify(key)},"values":${String(row.data)}}` });
      }
    }
    return out.sort((a, b) => byCodePoint(a.key, b.key));
  }

  #tableSubRowsText(type: string, useId: string, table: string): string[] {
    const out: { id: string; key: string; text: string }[] = [];
    for (const subTable of this.#subTables.values()) {
      for (const row of this.#all(`SELECT id, key, data FROM "${subTable}" WHERE schema = ? AND use_id = ? AND tbl = ? ORDER BY id, key`, type, useId, table)) {
        const id = String(row.id);
        const key = String(row.key);
        out.push({ id, key, text: `{"id":${JSON.stringify(id)},"row":{"key":${JSON.stringify(key)},"values":${String(row.data)}}}` });
      }
    }
    return out.sort((a, b) => byCodePoint(a.id, b.id) || byCodePoint(a.key, b.key)).map(entry => entry.text);
  }

  // The values of a shared table's row with its version, a null among them
  // left out, as SharedRow::new reads them.
  #shared(type: string, useId: string, table: string, key: string): { values: Record<string, unknown>; version: number } | undefined {
    const row = this.#get('SELECT data, version FROM dev_shared WHERE schema = ? AND use_id = ? AND tbl = ? AND key = ?', type, useId, table, key);
    if (row === undefined) return undefined;
    const values = this.#codec.parse(String(row.data)) as Record<string, unknown>;
    for (const [name, value] of Object.entries(values)) {
      if (value === null) delete values[name];
    }
    return { values, version: Number(row.version) };
  }

  /** A row's rows of one use's sub-table, in key order. */
  subRows(type: string, id: string, useId: string, table: string): { key: string; values: Record<string, unknown> }[] {
    return this.#subRowsText(type, id, useId, table).map(entry => this.#codec.parse(entry.text) as { key: string; values: Record<string, unknown> });
  }

  /** The row of a key of a use's shared table, with its version. */
  sharedRow(type: string, useId: string, table: string, key: string): { key: string; values: Record<string, unknown>; version: number } | undefined {
    const held = this.#shared(type, useId, table, key);
    return held === undefined ? undefined : { key, ...held };
  }

  /** A row's armed deadlines: each its use, name, time and payload, by use and name. */
  armed(type: string, id: string): { useId: string; name: string; at: number; payload: unknown }[] {
    return this.#all('SELECT use_id, name, at, payload FROM dev_deadlines WHERE schema = ? AND id = ? ORDER BY use_id, name', type, id).map(row => ({
      useId: String(row.use_id),
      name: String(row.name),
      at: Number(row.at),
      payload: this.#codec.parse(String(row.payload)),
    }));
  }

  due(now: number): DueDeadline[] {
    try {
      return this.#all('SELECT schema, id, use_id, name, at, payload FROM dev_deadlines WHERE due <= ? ORDER BY due, schema, id, use_id, name', now).map(row => ({
        type: String(row.schema),
        id: String(row.id),
        useId: String(row.use_id),
        name: String(row.name),
        at: Number(row.at),
        payload: raw(String(row.payload)),
      }));
    } catch (error) {
      throw failed(error);
    }
  }

  queued(): QueuedChange[] {
    try {
      return this.#all('SELECT seq, schema, id, change FROM dev_changes ORDER BY seq').map(row => ({
        seq: Number(row.seq),
        type: String(row.schema),
        id: String(row.id),
        change: raw(String(row.change)),
      }));
    } catch (error) {
      throw failed(error);
    }
  }

  events(after: number): QueuedEvent[] {
    try {
      return this.#all('SELECT seq, schema, id, use_id, kind, at, payload FROM dev_events WHERE seq > ? ORDER BY seq', after).map(row => ({
        seq: Number(row.seq),
        type: String(row.schema),
        id: String(row.id),
        useId: String(row.use_id),
        kind: String(row.kind),
        at: Number(row.at),
        payload: raw(String(row.payload)),
      }));
    } catch (error) {
      throw failed(error);
    }
  }

  begin(): void {
    try {
      this.connection.exec('BEGIN IMMEDIATE');
      this.#open = true;
    } catch (error) {
      throw failed(error);
    }
  }

  commit(): void {
    try {
      this.connection.exec('COMMIT');
      this.#open = false;
    } catch (error) {
      throw failed(error);
    }
  }

  rollback(): void {
    if (!this.#open) return;
    this.#open = false;
    try {
      this.connection.exec('ROLLBACK');
    } catch (error) {
      // SQLite rolls a transaction back itself after some failures.
      if (!/no transaction is active/i.test(error instanceof Error ? error.message : String(error))) {
        throw failed(error);
      }
    }
  }

  read(read: StoreRead): RawJson {
    try {
      return raw(this.#read(read));
    } catch (error) {
      throw failed(error);
    }
  }

  #read(read: StoreRead): string {
    switch (read.kind) {
      case 'live': {
        const versions = this.#all(
          `SELECT schema, version, document FROM dev_schema_versions AS v
                         WHERE version = (SELECT MAX(version) FROM dev_schema_versions WHERE schema = v.schema)
                         ORDER BY schema`,
        ).map(row => `{"type":${JSON.stringify(String(row.schema))},"version":${Number(row.version)},"document":${String(row.document)}}`);
        return `{"live":[${versions.join(',')}]}`;
      }
      case 'draft': {
        const row = this.#get('SELECT document FROM dev_schema_drafts WHERE schema = ?', read.type);
        return row === undefined ? '{"draft":null}' : `{"draft":{"type":${JSON.stringify(read.type)},"document":${String(row.document)}}}`;
      }
      case 'row':
        return `{"row":${this.#row(read.type, read.id) ?? 'null'}}`;
      case 'rows':
        return `{"rows":[${this.#rows(read.type).join(',')}]}`;
      case 'subRows':
        return `{"subRows":[${this.#subRowsText(read.type, read.id, read.useId, read.table)
          .map(entry => entry.text)
          .join(',')}]}`;
      case 'tableSubRows':
        return `{"tableSubRows":[${this.#tableSubRowsText(read.type, read.useId, read.table).join(',')}]}`;
      case 'sharedRow': {
        const held = this.#shared(read.type, read.useId, read.table, read.key);
        return held === undefined ? '{"sharedRow":null}' : `{"sharedRow":${this.#text({ key: read.key, values: held.values, version: held.version })}}`;
      }
      case 'armed': {
        const row = this.#get('SELECT at, payload FROM dev_deadlines WHERE schema = ? AND id = ? AND use_id = ? AND name = ?', read.type, read.id, read.useId, read.name);
        return row === undefined ? '{"armed":null}' : `{"armed":{"at":${Number(row.at)},"payload":${String(row.payload)}}}`;
      }
      default:
        throw new RefusalError(refusalOf('internal', `the development store answers no read of this kind: ${JSON.stringify(read)}`));
    }
  }

  apply(unit: Unit): boolean {
    try {
      this.connection.exec('SAVEPOINT dev_unit');
    } catch (error) {
      throw failed(error);
    }
    try {
      for (const write of unit.writes) {
        this.#apply(write);
      }
    } catch (error) {
      try {
        this.connection.exec('ROLLBACK TO dev_unit; RELEASE dev_unit');
      } catch (undo) {
        throw failed(undo);
      }
      if (error instanceof Moved) {
        return false;
      }
      throw failed(error);
    }
    try {
      this.connection.exec('RELEASE dev_unit');
    } catch (error) {
      throw failed(error);
    }
    return true;
  }

  #dropRow(type: string, id: string): void {
    for (const subTable of this.#subTables.values()) {
      this.#run(`DELETE FROM "${subTable}" WHERE schema = ? AND id = ?`, type, id);
    }
    this.#run('DELETE FROM dev_deadlines WHERE schema = ? AND id = ?', type, id);
  }

  #apply(write: StoreWrite): void {
    switch (write.write) {
      case 'putDraft':
        this.#run(
          `INSERT INTO dev_schema_drafts (schema, document) VALUES (?, ?)
                     ON CONFLICT (schema) DO UPDATE SET document = excluded.document`,
          write.type,
          this.#text(write.document),
        );
        return;
      case 'putVersion': {
        const live = Number(this.#get('SELECT COALESCE(MAX(version), 0) AS live FROM dev_schema_versions WHERE schema = ?', write.type)?.live ?? 0);
        if (live !== write.version - 1) {
          throw new Moved();
        }
        this.#run('INSERT INTO dev_schema_versions (schema, version, document) VALUES (?, ?, ?)', write.type, write.version, this.#text(write.document));
        this.#run('DELETE FROM dev_schema_drafts WHERE schema = ?', write.type);
        return;
      }
      case 'insert': {
        if (this.#get('SELECT 1 AS held FROM dev_instances WHERE schema = ? AND id = ?', write.type, write.id) !== undefined) {
          throw new RefusalError(refusalOf('conflict', `${write.type} ${write.id} already exists`));
        }
        this.#run('INSERT INTO dev_instances (schema, id, version, data, owned) VALUES (?, ?, 1, ?, ?)', write.type, write.id, this.#text(write.data), this.#text(write.owned));
        return;
      }
      case 'update':
        this.#one(
          `UPDATE dev_instances SET data = ?, owned = ?, version = version + 1
                 WHERE schema = ? AND id = ? AND version = ?`,
          this.#text(write.data),
          this.#text(write.owned),
          write.type,
          write.id,
          write.expected,
        );
        return;
      case 'rewrite':
        this.#one('UPDATE dev_instances SET data = ?, owned = ? WHERE schema = ? AND id = ?', this.#text(write.data), this.#text(write.owned), write.type, write.id);
        return;
      case 'delete':
        if (write.expected !== undefined && write.expected !== null) {
          this.#one('DELETE FROM dev_instances WHERE schema = ? AND id = ? AND version = ?', write.type, write.id, write.expected);
        } else {
          this.#one('DELETE FROM dev_instances WHERE schema = ? AND id = ?', write.type, write.id);
        }
        this.#dropRow(write.type, write.id);
        return;
      case 'gone':
        if (this.#row(write.type, write.id) === undefined) {
          this.#dropRow(write.type, write.id);
        }
        return;
      case 'subRow': {
        const subTable = this.#subTable(write.behavior);
        const change = write.change;
        switch (change.kind) {
          case 'insert': {
            const values: Record<string, unknown> = {};
            for (const [name, value] of Object.entries(change.values)) {
              if (value !== null) values[name] = value;
            }
            this.#one(
              `INSERT INTO "${subTable}" (schema, id, use_id, tbl, key, data)
                                 VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
              write.type,
              write.id,
              write.useId,
              write.table,
              write.key,
              this.#text(values),
            );
            return;
          }
          case 'update': {
            const held = this.#get(`SELECT data FROM "${subTable}" WHERE schema = ? AND id = ? AND use_id = ? AND tbl = ? AND key = ?`, write.type, write.id, write.useId, write.table, write.key);
            if (held === undefined) {
              throw new Moved();
            }
            const data = this.#codec.parse(String(held.data)) as Record<string, unknown>;
            for (const [name, value] of Object.entries(change.values)) {
              if (value === null) delete data[name];
              else data[name] = value;
            }
            this.#one(
              `UPDATE "${subTable}" SET data = ? WHERE schema = ? AND id = ? AND use_id = ? AND tbl = ? AND key = ?`,
              this.#text(data),
              write.type,
              write.id,
              write.useId,
              write.table,
              write.key,
            );
            return;
          }
          case 'delete':
            this.#one(`DELETE FROM "${subTable}" WHERE schema = ? AND id = ? AND use_id = ? AND tbl = ? AND key = ?`, write.type, write.id, write.useId, write.table, write.key);
            return;
          default:
            throw new RefusalError(refusalOf('internal', `the development store takes no sub-table change of this kind: ${JSON.stringify(change)}`));
        }
      }
      case 'sharedRow': {
        const change = write.change;
        switch (change.kind) {
          case 'insert':
            this.#one(
              `INSERT INTO dev_shared (schema, use_id, tbl, key, data, version)
                     VALUES (?, ?, ?, ?, ?, 1) ON CONFLICT DO NOTHING`,
              write.type,
              write.useId,
              write.table,
              write.key,
              this.#text(change.values),
            );
            return;
          case 'update': {
            const held = this.#shared(write.type, write.useId, write.table, write.key);
            if (held === undefined) {
              throw new Moved();
            }
            const data = held.values;
            for (const [name, value] of Object.entries(change.values)) {
              if (value === null) delete data[name];
              else data[name] = value;
            }
            this.#one(
              `UPDATE dev_shared SET data = ?, version = version + 1
                         WHERE schema = ? AND use_id = ? AND tbl = ? AND key = ? AND version = ?`,
              this.#text(data),
              write.type,
              write.useId,
              write.table,
              write.key,
              change.expectedVersion,
            );
            return;
          }
          // A delete held to its version (D147): one a writer moved or
          // deleted first deletes nothing, and the command runs again.
          case 'delete':
            this.#one(
              'DELETE FROM dev_shared WHERE schema = ? AND use_id = ? AND tbl = ? AND key = ? AND version = ?',
              write.type,
              write.useId,
              write.table,
              write.key,
              change.expectedVersion,
            );
            return;
          default:
            throw new RefusalError(refusalOf('internal', `the development store takes no shared-table change of this kind: ${JSON.stringify(change)}`));
        }
      }
      case 'dropUse': {
        const subTable = this.#subTables.get(write.behavior);
        if (subTable !== undefined) {
          this.#run(`DELETE FROM "${subTable}" WHERE schema = ? AND use_id = ?`, write.type, write.useId);
        }
        this.#run('DELETE FROM dev_shared WHERE schema = ? AND use_id = ?', write.type, write.useId);
        return;
      }
      case 'arm':
        this.#run(
          `INSERT INTO dev_deadlines (schema, id, use_id, name, at, due, payload)
                     VALUES (?, ?, ?, ?, ?, ?, ?)
                     ON CONFLICT (schema, id, use_id, name)
                     DO UPDATE SET at = excluded.at, due = excluded.due, payload = excluded.payload`,
          write.type,
          write.id,
          write.useId,
          write.name,
          write.at,
          write.at,
          this.#text(write.payload ?? null),
        );
        return;
      case 'clear':
        this.#run('DELETE FROM dev_deadlines WHERE schema = ? AND id = ? AND use_id = ? AND name = ?', write.type, write.id, write.useId, write.name);
        return;
      case 'clearUse':
        this.#run('DELETE FROM dev_deadlines WHERE schema = ? AND use_id = ?', write.type, write.useId);
        return;
      case 'spend':
        this.#one('DELETE FROM dev_deadlines WHERE schema = ? AND id = ? AND use_id = ? AND name = ? AND at = ?', write.type, write.id, write.useId, write.name, write.at);
        return;
      case 'release':
        this.#one('UPDATE dev_deadlines SET due = ? WHERE schema = ? AND id = ? AND use_id = ? AND name = ?', write.due, write.type, write.id, write.useId, write.name);
        return;
      case 'event':
        this.#run('INSERT INTO dev_events (schema, id, use_id, kind, at, payload) VALUES (?, ?, ?, ?, ?, ?)', write.type, write.id, write.useId, write.kind, write.at, this.#text(write.payload ?? null));
        return;
      case 'change':
        this.#run('INSERT INTO dev_changes (schema, id, change) VALUES (?, ?, ?)', write.type, write.id, this.#text(write.change));
        return;
      case 'dequeue':
        this.#one('DELETE FROM dev_changes WHERE seq = ?', write.seq);
        return;
      default:
        throw new RefusalError(refusalOf('internal', `the development store takes no write of this kind: ${JSON.stringify(write)}`));
    }
  }
}
