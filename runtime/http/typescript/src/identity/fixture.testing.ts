// What the identity tests share: the fixture's descriptor and DDL
// (runtime/http/testdata/identity, fixture-user-model-db), and the
// databases a store test runs on: SQLite through node:sqlite and through
// bun:sqlite always, and Postgres when SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL
// names one, each test in a schema of its own. tsconfig.json leaves it out
// of the build.
import { Database as BunDatabase } from 'bun:sqlite';
import { readFileSync } from 'node:fs';
import { DatabaseSync } from 'node:sqlite';
import pg from 'pg';
import {
  bunSqlite,
  nodeSqlite,
  postgresIdentityStore,
  sqliteIdentityStore,
  type SqlIdentityStore,
  type SqliteClient,
  type SqliteModuleDatabase,
} from './index';

const fixtureDir = new URL('../../../testdata/identity/', import.meta.url);

/**
 * The fixture's identity descriptor, as JSON text. A fixture written before
 * the descriptor named user.nameScalar and role.keyScalar gets the fixture
 * schema's types for them (its display name is an Identity.Name, its role
 * key an Identity.UUID); one that names them is read as it is.
 */
export const descriptorJSON = (() => {
  const d = JSON.parse(readFileSync(new URL('fixture-user-model-db.json', fixtureDir), 'utf8')) as Record<string, any>;
  d.user.nameScalar ??= 'Identity.Name';
  if (d.role) d.role.keyScalar ??= 'Identity.UUID';
  return JSON.stringify(d, null, 2);
})();
export const sqliteDDL = readFileSync(new URL('sqlite/create.sql', fixtureDir), 'utf8');
export const postgresDDL = readFileSync(new URL('create.sql', fixtureDir), 'utf8');

/** The variable that names the Postgres the tests also run against. */
export const postgresURLEnv = 'SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL';
export const postgresURL = process.env[postgresURLEnv] ?? '';

/** The fixture's descriptor, edited by edit. */
export function descriptorWith(edit: (d: Record<string, any>) => void): string {
  const d = JSON.parse(descriptorJSON) as Record<string, any>;
  edit(d);
  return JSON.stringify(d);
}

/** A database holding the fixture's tables. */
export interface TestDatabase {
  readonly name: 'node:sqlite' | 'bun:sqlite' | 'postgres';
  /** A store over the database, from the fixture's descriptor or another. */
  store(descriptor?: string): SqlIdentityStore;
  /** Runs a statement written with ? placeholders and returns its rows. */
  query(sql: string, params?: string[]): Promise<Record<string, unknown>[]>;
  close(): Promise<void>;
}

/** The ways a test opens a database. */
export interface DatabaseKind {
  readonly name: TestDatabase['name'];
  open(ddl?: string): Promise<TestDatabase>;
}

function sqliteDatabase(name: 'node:sqlite' | 'bun:sqlite', db: SqliteModuleDatabase & { close(): void }, client: SqliteClient, ddl: string): TestDatabase {
  client.exec('PRAGMA foreign_keys = ON');
  client.exec(ddl);
  return {
    name,
    store: descriptor => sqliteIdentityStore(client, descriptor ?? descriptorJSON),
    query: async (sql, params = []) => client.all(sql, params),
    close: async () => db.close(),
  };
}

let schemas = 0;

async function postgresDatabase(ddl: string): Promise<TestDatabase & { pool: pg.Pool }> {
  const admin = new pg.Client({ connectionString: postgresURL });
  await admin.connect();
  // An extension's name is unique in the database, so create the DDL's
  // extensions once in public, where every schema's search path finds them.
  for (const extension of ['pgcrypto', 'citext']) {
    try {
      await admin.query(`CREATE EXTENSION IF NOT EXISTS ${extension} SCHEMA public`);
    } catch (error) {
      if ((error as { code?: string }).code !== '23505') throw error;
    }
  }
  const schema = `identity_ts_${process.pid}_${Date.now()}_${schemas++}`;
  await admin.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString: postgresURL, options: `-c search_path=${schema},public` });
  await pool.query(ddl);
  return {
    name: 'postgres',
    pool,
    store: descriptor => postgresIdentityStore(pool, descriptor ?? descriptorJSON),
    async query(sql, params = []) {
      let n = 0;
      return (await pool.query(sql.replace(/\?/gu, () => `$${++n}`), params)).rows;
    },
    async close() {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    },
  };
}

/** node:sqlite, bun:sqlite and, when postgresURLEnv names one, Postgres. */
export const databaseKinds: DatabaseKind[] = [
  {
    name: 'node:sqlite',
    async open(ddl = sqliteDDL) {
      const db = new DatabaseSync(':memory:');
      return sqliteDatabase('node:sqlite', db, nodeSqlite(db), ddl);
    },
  },
  {
    name: 'bun:sqlite',
    async open(ddl = sqliteDDL) {
      const db = new BunDatabase(':memory:');
      return sqliteDatabase('bun:sqlite', db as unknown as SqliteModuleDatabase & { close(): void }, bunSqlite(db as unknown as SqliteModuleDatabase), ddl);
    },
  },
  ...(postgresURL !== '' ? [{ name: 'postgres' as const, open: (ddl = postgresDDL) => postgresDatabase(ddl) }] : []),
];

/** A fixed instant plus ms milliseconds, as both dialects keep it. */
export function at(ms = 0): Date {
  return new Date(Date.UTC(2026, 9, 8, 12, 0, 0) + ms);
}

export const SECOND = 1000;
export const MINUTE = 60 * SECOND;
export const HOUR = 60 * MINUTE;
