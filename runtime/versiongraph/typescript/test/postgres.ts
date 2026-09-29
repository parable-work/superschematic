// What the Postgres tests share: the database they run against, a schema of
// their own per test that holds the fixture's DDL, and the fixture.
import { readFileSync } from "node:fs";
import pg from "pg";

/** The variable that names the Postgres the tests run against. */
export const databaseVariable = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL";

export const dsn = process.env[databaseVariable] ?? "";

const fixtureDir = new URL("../../testdata/fixture/", import.meta.url);

/** The scenarios' graph descriptor, fixture-version-graph-db's Recipe graph, as JSON text. */
export const descriptor = readFileSync(new URL("recipe.json", fixtureDir), "utf8");

/** The fixture's Postgres DDL. */
export const createSQL = readFileSync(new URL("create.sql", fixtureDir), "utf8");

/** Returns every column as the text Postgres writes, unparsed. */
export const rawTypes = { getTypeParser: () => (value: string) => value };

let counter = 0;

/** A schema of its own holding the fixture's DDL, and a pool whose connections use it. */
export interface Scratch {
  schema: string;
  pool: pg.Pool;
  close(): Promise<void>;
}

/** Creates a schema, applies the fixture's DDL in it, and returns a pool over it. */
export async function scratchSchema(prefix: string): Promise<Scratch> {
  const admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  // The fixture's DDL creates pgcrypto if it is missing. An extension's name
  // is unique in the database, so create it once in public, where every
  // schema's search path finds it, before tests running in parallel each
  // try to create it in their own schema.
  try {
    await admin.query("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public");
  } catch (err) {
    if ((err as { code?: string }).code !== "23505") {
      throw err;
    }
  }
  const schema = `${prefix}_${process.pid}_${Date.now()}_${counter++}`;
  await admin.query(`CREATE SCHEMA ${schema}`);
  const pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public` });
  await pool.query(createSQL);
  return {
    schema,
    pool,
    async close() {
      await pool.end();
      await admin.query(`DROP SCHEMA ${schema} CASCADE`);
      await admin.end();
    },
  };
}
