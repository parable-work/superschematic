/*
A behavior's storage: columns on the instances table and tables of its
own, all named by the engine. When a schema that composes a behavior is
first published, the engine records a key for it in engine_behaviors
(its name, lowercased, with each run of other characters made one `_`,
and `_2`, `_3`, ... when a key is taken), and every name the behavior
owns is `bhv_<key>__<its own name>`. A key has no `__` and does not end
in `_`, so one behavior's prefix never starts another's, and the table
keeps each behavior's key for the life of the file.

The behavior's migrations run through the engine's ledger under its name:
each adds the columns it lists (ALTER TABLE engine_instances ADD COLUMN)
and runs its up() with SQL scoped to the behavior (sql.ts). Afterwards
every object sqlite_master gained must be a table or index of its own,
on a table of its own; anything else rolls the migration back.
*/

import { BehaviorError } from '../errors.js';
import type { Row, RunResult, SqlValue } from '../storage/driver.js';
import type { Migration, MigrationSet } from '../storage/migrations.js';
import type { Storage } from '../storage/storage.js';
import type { BehaviorMigration, ColumnSpec, SqlWriter, WritableColumns } from './behavior.js';
import { sqlRefusal, type SqlMode } from './sql.js';

/** The prefix of every name a behavior owns: bhv_<key>__<name>. */
export const STORAGE_PREFIX = 'bhv_';

/** A behavior's own name for a column or table. */
export const LOCAL_NAME = /^[a-z][a-z0-9_]{0,62}$/;

const COLUMN_TYPES: Record<ColumnSpec['type'], string> = {
  integer: 'INTEGER',
  real: 'REAL',
  text: 'TEXT',
  blob: 'BLOB',
  any: 'ANY',
};

/** prefixOf is the prefix of a behavior's names under its key. */
export function prefixOf(key: string): string {
  return `${STORAGE_PREFIX}${key}__`;
}

/** storedKey returns a behavior's key, or undefined when no schema that composes it has been published. */
export function storedKey(storage: Storage, name: string): string | undefined {
  const row = storage.get('SELECT key FROM engine_behaviors WHERE name = ?', [name]);
  return row ? String(row.key) : undefined;
}

/** assignKey returns a behavior's key, recording a new one. Call it inside a transaction. */
export function assignKey(storage: Storage, name: string, now: number): string {
  const known = storedKey(storage, name);
  if (known !== undefined) {
    return known;
  }
  const base =
    name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '_')
      .replace(/^_+|_+$/g, '') || 'b';
  let key = base;
  for (let n = 2; storage.get('SELECT 1 AS taken FROM engine_behaviors WHERE key = ?', [key]); n += 1) {
    key = `${base}_${n}`;
  }
  storage.run('INSERT INTO engine_behaviors (name, key, created_at) VALUES (?, ?, ?)', [name, key, now]);
  return key;
}

/** columnProblems returns what is wrong with a column's spec. */
export function columnProblems(name: string, spec: unknown): string[] {
  const at = `column ${name}`;
  if (!LOCAL_NAME.test(name)) {
    return [`${at}: a column name matches ${LOCAL_NAME.source}`];
  }
  if (typeof spec !== 'object' || spec === null) {
    return [`${at} is { type, notNull?, default? }`];
  }
  const { type, notNull, default: fallback } = spec as { type?: unknown; notNull?: unknown; default?: unknown };
  if (typeof type !== 'string' || !Object.prototype.hasOwnProperty.call(COLUMN_TYPES, type)) {
    return [`${at}: type is one of ${Object.keys(COLUMN_TYPES).join(', ')}`];
  }
  const problems: string[] = [];
  if (notNull !== undefined && typeof notNull !== 'boolean') {
    problems.push(`${at}: notNull is a boolean`);
  }
  if (notNull === true && (fallback === undefined || fallback === null)) {
    problems.push(`${at} is NOT NULL, so it needs a default for the instances that exist`);
  }
  if (fallback !== undefined && fallback !== null) {
    const fits =
      type === 'integer'
        ? Number.isSafeInteger(fallback)
        : type === 'real'
          ? typeof fallback === 'number' && Number.isFinite(fallback)
          : type === 'text'
            ? typeof fallback === 'string'
            : type === 'any'
              ? typeof fallback === 'string' || (typeof fallback === 'number' && Number.isFinite(fallback))
              : false;
    if (!fits) {
      problems.push(`${at}: a default of ${JSON.stringify(fallback)} does not fit type ${type}`);
    }
  }
  return problems;
}

/**
 * migrationSet wraps a behavior's migrations for the engine's ledger. Each
 * adds its columns, then runs its up() on SQL scoped to the behavior.
 */
export function migrationSet(behavior: string, prefix: string, migrations: readonly BehaviorMigration[]): MigrationSet {
  return {
    owner: behavior,
    migrations: migrations.map((migration): Migration => ({
      version: migration.version,
      name: migration.name,
      up(storage) {
        for (const [column, spec] of Object.entries(migration.columns ?? {})) {
          storage.exec(`ALTER TABLE engine_instances ADD COLUMN "${prefix}${column}" ${columnDefinition(spec)}`);
        }
        if (migration.up) {
          const before = new Set(objects(storage).map((object) => object.key));
          const result: unknown = migration.up(new BehaviorSql(storage, behavior, prefix, 'migrate'));
          synchronous(behavior, `migration ${migration.version}`, result);
          for (const object of objects(storage)) {
            if (!before.has(object.key)) {
              checkCreated(behavior, prefix, migration, object);
            }
          }
        }
      },
    })),
  };
}

/** The SQL handle a behavior gets: its own tables, in one mode. */
export class BehaviorSql implements SqlWriter {
  constructor(
    private readonly storage: Storage,
    private readonly behavior: string,
    private readonly prefix: string,
    private readonly mode: SqlMode
  ) {}

  table(name: string): string {
    if (typeof name !== 'string' || !LOCAL_NAME.test(name)) {
      throw new BehaviorError(this.behavior, `table name ${JSON.stringify(name)} must match ${LOCAL_NAME.source}`);
    }
    return `${this.prefix}${name}`;
  }

  get(sql: string, params: readonly SqlValue[] = []): Row | undefined {
    this.check(sql);
    return this.storage.get(sql, params);
  }

  all(sql: string, params: readonly SqlValue[] = []): Row[] {
    this.check(sql);
    return this.storage.all(sql, params);
  }

  run(sql: string, params: readonly SqlValue[] = []): RunResult {
    if (this.mode === 'read') {
      throw new BehaviorError(this.behavior, 'a read cannot run a statement that writes; run() is for writes');
    }
    this.check(sql);
    return this.storage.run(sql, params);
  }

  private check(sql: string): void {
    const refusal = sqlRefusal(sql, this.prefix, this.mode);
    if (refusal !== undefined) {
      throw new BehaviorError(this.behavior, `SQL refused: ${refusal}: ${String(sql).trim().slice(0, 200)}`);
    }
  }
}

/** One instance's columns of one behavior, read and written in place. */
export class InstanceColumns implements WritableColumns {
  constructor(
    private readonly storage: Storage,
    private readonly behavior: string,
    private readonly prefix: string,
    private readonly names: readonly string[],
    private readonly instance: readonly [namespace: string, schema: string, id: string],
    /** Why set() refuses, or undefined when it may write. */
    private readonly readOnly: string | undefined
  ) {}

  get(): Record<string, SqlValue> {
    if (this.names.length === 0) {
      return {};
    }
    const row = this.storage.get(
      `SELECT ${this.names.map((name) => `"${this.prefix}${name}" AS "${name}"`).join(', ')}
       FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?`,
      this.instance
    );
    if (!row) {
      throw new BehaviorError(this.behavior, `${this.instance[1]} ${this.instance[2]} does not exist`);
    }
    return row;
  }

  set(values: Readonly<Record<string, SqlValue>>): void {
    if (this.readOnly !== undefined) {
      throw new BehaviorError(this.behavior, this.readOnly);
    }
    if (typeof values !== 'object' || values === null) {
      throw new BehaviorError(this.behavior, 'columns.set takes an object of column values');
    }
    const names = Object.keys(values);
    for (const name of names) {
      if (!this.names.includes(name)) {
        throw new BehaviorError(this.behavior, `it has no column ${name}; its columns are ${this.names.join(', ') || 'none'}`);
      }
    }
    if (names.length === 0) {
      return;
    }
    this.storage.run(
      `UPDATE engine_instances SET ${names.map((name) => `"${this.prefix}${name}" = ?`).join(', ')}
       WHERE namespace = ? AND schema = ? AND id = ?`,
      [...names.map((name) => values[name]), ...this.instance]
    );
  }
}

/** The columns an instance had when it was deleted: readable, not writable. */
export class DeletedColumns implements WritableColumns {
  constructor(
    private readonly behavior: string,
    private readonly values: Record<string, SqlValue>
  ) {}

  get(): Record<string, SqlValue> {
    return { ...this.values };
  }

  set(): void {
    throw new BehaviorError(this.behavior, 'the instance is deleted; its columns cannot change');
  }
}

/** synchronous refuses a promise where D16 requires a synchronous call. */
export function synchronous(behavior: string, what: string, value: unknown): void {
  if (
    (typeof value === 'object' || typeof value === 'function') &&
    value !== null &&
    typeof (value as { then?: unknown }).then === 'function'
  ) {
    // The promise's own outcome no longer matters; keep a rejection from
    // surfacing as unhandled.
    (value as PromiseLike<unknown>).then(undefined, () => undefined);
    throw new BehaviorError(behavior, `${what} is synchronous (D16): it returned a promise`);
  }
}

interface SchemaObject {
  key: string;
  type: string;
  name: string;
  table: string;
}

function objects(storage: Storage): SchemaObject[] {
  return storage.all('SELECT type, name, tbl_name FROM sqlite_master').map((row) => ({
    key: `${String(row.type)}\u0000${String(row.name)}`,
    type: String(row.type),
    name: String(row.name),
    table: String(row.tbl_name),
  }));
}

// checkCreated holds a new object to the behavior's own names: a table, or
// an index (its own, or the one SQLite makes for a UNIQUE or PRIMARY KEY
// constraint) on a table of its own.
function checkCreated(behavior: string, prefix: string, migration: BehaviorMigration, object: SchemaObject): void {
  const own = (name: string): boolean => name.toLowerCase().startsWith(prefix);
  const named = object.type === 'index' && object.name.startsWith('sqlite_autoindex_') ? own(object.table) : own(object.name);
  if ((object.type === 'table' || object.type === 'index') && named && own(object.table)) {
    return;
  }
  throw new BehaviorError(
    behavior,
    `migration ${migration.version} "${migration.name}" created ${object.type} ${object.name}; a behavior creates only tables and indexes named ${prefix}*, with sql.table(name)`
  );
}

function columnDefinition(spec: ColumnSpec): string {
  let definition = COLUMN_TYPES[spec.type];
  if (spec.notNull) {
    definition += ' NOT NULL';
  }
  if (spec.default !== undefined && spec.default !== null) {
    definition += ` DEFAULT ${typeof spec.default === 'number' ? String(spec.default) : `'${spec.default.replace(/'/g, "''")}'`}`;
  }
  return definition;
}
