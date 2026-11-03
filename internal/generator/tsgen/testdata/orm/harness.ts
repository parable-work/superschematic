// What each repository test shares (D79): a schema of its own on the test
// database with the package's create.sql applied, a pool on it, and a pool
// that records the statements it runs. TestRepositoriesOnPostgres copies it
// into each generated package beside the test.
import { readFileSync } from 'node:fs';
import pg from 'pg';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';

export const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL ?? '';

/** A database's schema of its own, with a pool whose search_path is it, then public, which holds the extensions. */
export interface TestDatabase {
  readonly schema: string;
  readonly pool: pg.Pool;
  /** Ends the pool and drops the schema. */
  drop(): Promise<void>;
}

let schemas = 0;

/** A schema of its own, prefix and a counter, create.sql applied. */
export async function database(prefix: string): Promise<TestDatabase> {
  const schema = `${prefix}_${process.pid}_${Date.now()}_${++schemas}`;
  const admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(new URL('./create.sql', import.meta.url), 'utf8'));
  return {
    schema,
    pool,
    async drop() {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    },
  };
}

/** A pool over pool that records the text of each statement it and its connections run, BEGIN and COMMIT among them. */
export function recording(pool: pg.Pool): { pool: OrmPool; statements: string[] } {
  const statements: string[] = [];
  const record = (q: OrmQueryable): OrmQueryable => ({
    query(config) {
      statements.push(config.text);
      return q.query(config as never) as never;
    },
  });
  return {
    statements,
    pool: {
      ...record(pool as unknown as OrmQueryable),
      async connect() {
        const client = await pool.connect();
        return { ...record(client as unknown as OrmQueryable), release: (err?: Error | boolean) => client.release(err) };
      },
    },
  };
}

/** A user's id, base62 as Identity.UUID holds it. */
export const alice = 'TKbHsHDO6x9RCjbs5c2u4';
export const aliceHyphenated = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
export const bob = '1JwchoUsfFfo9IGmolouwH';
