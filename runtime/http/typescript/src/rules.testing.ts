/*
What the behaviors' adapter's tests share (D103): the core, the
@superschematic/rules package of this checkout (runtime/rules/typescript),
whose source Bun runs and whose module is the one that package's
scripts/build-wasm.mjs copies into its dist/, built here first whenever
cargo is on PATH, so the tests never run a module older than the core's
crates; a database of each dialect, bun:sqlite through openSqlite and
Postgres through pg in a schema of the test's own; and a transaction of
each, as RulesStore.

The package is test code's only: the HTTP runtime never imports
@superschematic/rules (the generated orm.ts passes its core in).
*/
import { execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import pg from 'pg';
import { initSync, type RulesCore as PackageCore } from '../../../rules/typescript/src/index.ts';
import { postgresRulesStore, sqliteRulesStore, type RulesCore, type RulesStore } from './rules';
import { openSqlite, type SqliteDatabase } from './sqlite';

/** The variable that names the Postgres the adapter's tests run against; they are skipped without it. */
export const RULES_DATABASE_VARIABLE = 'SUPERSCHEMATIC_RULES_TEST_DATABASE_URL';
export const rulesDsn = process.env[RULES_DATABASE_VARIABLE] ?? '';

const packageRoot = fileURLToPath(new URL('../../../rules/typescript/', import.meta.url));
const wasmFile = join(packageRoot, 'dist', 'superschematic_rules_wasm.wasm');

const onPath = (tool: string): boolean => (process.env.PATH ?? '').split(delimiter).some(dir => dir !== '' && existsSync(join(dir, tool)));

/**
 * The module's bytes: dist/'s, built first whenever cargo is on PATH (a
 * build of crates that have not changed is cargo's to skip, and one that
 * gives the module dist/ holds leaves it as it is); undefined, with a line
 * saying how to build it, when it cannot be had, so the tests that need
 * the core skip rather than fail on a checkout without Rust.
 */
function wasmBytes(): Uint8Array | undefined {
  if (onPath('cargo')) {
    execFileSync('node', [join(packageRoot, 'scripts', 'build-wasm.mjs')], { stdio: 'inherit' });
  }
  if (!existsSync(wasmFile)) {
    console.warn(`rules: ${wasmFile} is missing and cargo is not on PATH; build it with \`bun run build\` in runtime/rules/typescript to run the behaviors' adapter tests`);
    return undefined;
  }
  return readFileSync(wasmFile);
}

const bytes = wasmBytes();

/** The core every behavior ported so far runs on, as the package gives it, or undefined when its module cannot be had. */
export const packageCore: PackageCore | undefined = bytes === undefined ? undefined : initSync(bytes);

/** The core as the adapter takes it: the package's RulesCore fits the adapter's. */
export const rulesCore: RulesCore | undefined = packageCore;

/** The core, which a test that runs only with one asks for. */
export function core(): PackageCore {
  if (packageCore === undefined) throw new Error('rules: the core is not loaded');
  return packageCore;
}

/** A database of a dialect the tests run on: DDL, a transaction as a store, and raw SQL past the adapter. */
export interface TestDatabase {
  readonly dialect: 'sqlite' | 'postgres';
  /** Runs DDL or a raw write, its placeholders the dialect's. */
  exec(text: string, values?: readonly unknown[]): Promise<number>;
  /** The rows a query reads. */
  rows(text: string, values?: readonly unknown[]): Promise<Record<string, unknown>[]>;
  /**
   * Runs fn with a store over a transaction: on Postgres a connection
   * between BEGIN and COMMIT (ROLLBACK when fn throws), on SQLite the
   * database itself, whose batch is the unit.
   */
  transaction<T>(fn: (store: RulesStore) => Promise<T>): Promise<T>;
  /** A write by another connection, committed at once: what moves a row under a command. */
  concurrently(text: string, values?: readonly unknown[]): Promise<void>;
  close(): Promise<void>;
}

/** A SQLite file of the test's own, through openSqlite, as a sqlite-first database is under stack dev. */
export function sqliteDatabase(): TestDatabase {
  const dir = mkdtempSync(join(tmpdir(), 'rules-sqlite-'));
  const db: SqliteDatabase = openSqlite(join(dir, 'rules.db'));
  const other: SqliteDatabase = openSqlite(join(dir, 'rules.db'));
  const exec = async (text: string, values: readonly unknown[] = []) =>
    (await db.prepare(text).bind(...(values as never[])).run()).meta.changes;
  return {
    dialect: 'sqlite',
    exec,
    async rows(text, values = []) {
      return (await db.prepare(text).bind(...(values as never[])).all()).results;
    },
    async transaction(fn) {
      return fn(sqliteRulesStore(db));
    },
    async concurrently(text, values = []) {
      await other.prepare(text).bind(...(values as never[])).run();
    },
    async close() {
      db.close();
      other.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** A schema of the test's own on the Postgres RULES_DATABASE_VARIABLE names, dropped on close. */
export async function postgresDatabase(): Promise<TestDatabase> {
  const schema = `rules_${process.pid}_${Date.now()}_${Math.floor(Math.random() * 1e6)}`;
  const admin = new pg.Pool({ connectionString: rulesDsn, max: 1 });
  await admin.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString: rulesDsn, max: 4, options: `-c search_path=${schema}` });
  return {
    dialect: 'postgres',
    async exec(text, values = []) {
      return (await pool.query(text, [...values])).rowCount ?? 0;
    },
    async rows(text, values = []) {
      return (await pool.query(text, [...values])).rows;
    },
    async transaction(fn) {
      const client = await pool.connect();
      try {
        await client.query('BEGIN');
        try {
          const out = await fn(postgresRulesStore(client as never));
          await client.query('COMMIT');
          return out;
        } catch (error) {
          await client.query('ROLLBACK');
          throw error;
        }
      } finally {
        client.release();
      }
    },
    async concurrently(text, values = []) {
      await pool.query(text, [...values]);
    },
    async close() {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    },
  };
}

/** The column types of a test table's columns in a dialect: text, a timestamp, a version, a key. */
export function columnTypes(dialect: 'sqlite' | 'postgres'): { text: string; timestamp: string; version: string; key: string; number: string; integer: string; boolean: string; json: string } {
  return dialect === 'postgres'
    ? { text: 'TEXT', timestamp: 'TIMESTAMPTZ', version: 'BIGINT DEFAULT 1 NOT NULL', key: 'TEXT PRIMARY KEY', number: 'DOUBLE PRECISION', integer: 'BIGINT', boolean: 'BOOLEAN', json: 'JSONB' }
    : { text: 'TEXT', timestamp: 'TEXT', version: 'INTEGER DEFAULT 1 NOT NULL', key: 'TEXT PRIMARY KEY', number: 'REAL', integer: 'INTEGER', boolean: 'INTEGER', json: 'TEXT' };
}

/** The @optimistic trigger sqlgen writes on Postgres, which bumps _version on every update of a table. */
export function bumpVersion(table: string): string[] {
  return [
    `CREATE OR REPLACE FUNCTION ${table}_bump_version() RETURNS trigger AS $$
BEGIN
  NEW._version := OLD._version + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`,
    `CREATE TRIGGER trg_${table}_bump_version BEFORE UPDATE ON "${table}" FOR EACH ROW EXECUTE FUNCTION ${table}_bump_version()`,
  ];
}
