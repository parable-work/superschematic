import { SqlIdentityStore, driveAsync, postgresPlaceholders, type SqlRow, type SqlRunner, type SqlStatement, type SqlWork } from './sqlstore.js';

/*
The identity store's Postgres binding over the npm package pg. It uses only
the methods it calls, typed here, so this module imports nothing from pg
and loads without it. The tables are not qualified with a schema: the
store finds them on the connection's search_path. A statement outside a
transaction runs on the pool; a transaction takes a connection of its own
and returns it when it ends.
*/

/** The query config pg takes. */
export interface PgQueryConfig {
  text: string;
  values: unknown[];
}

/** What the store needs of a pg Client or PoolClient. */
export interface PgQueryable {
  query(config: PgQueryConfig): Promise<{ rows: Record<string, unknown>[]; rowCount: number | null }>;
}

/** What the store needs of a pg Pool. */
export interface PgPool extends PgQueryable {
  connect(): Promise<PgQueryable & { release(err?: Error | boolean): void }>;
}

async function execute(queryable: PgQueryable, statement: SqlStatement): Promise<SqlRow[] | number> {
  const result = await queryable.query({ text: postgresPlaceholders(statement.sql), values: [...statement.params] });
  return statement.kind === 'rows' ? result.rows : (result.rowCount ?? 0);
}

/** The SqlRunner over a pg Pool. */
export function postgresRunner(pool: PgPool): SqlRunner {
  return {
    dialect: 'postgres',
    async run<T>(work: () => SqlWork<T>, transaction: boolean): Promise<T> {
      if (!transaction) return driveAsync(work(), statement => execute(pool, statement));
      const client = await pool.connect();
      // A connection whose transaction could not be ended is not returned
      // to the pool.
      let broken: Error | undefined;
      try {
        await client.query({ text: 'BEGIN', values: [] });
        let out: T;
        try {
          out = await driveAsync(work(), statement => execute(client, statement));
        } catch (error) {
          try {
            await client.query({ text: 'ROLLBACK', values: [] });
          } catch (rollbackError) {
            broken = rollbackError instanceof Error ? rollbackError : new Error(String(rollbackError));
          }
          throw error;
        }
        try {
          await client.query({ text: 'COMMIT', values: [] });
        } catch (error) {
          broken = error instanceof Error ? error : new Error(String(error));
          throw error;
        }
        return out;
      } finally {
        client.release(broken ?? false);
      }
    },
  };
}

/** The identity store over a pg Pool whose connections find the schema's tables on their search_path. */
export function postgresIdentityStore(pool: PgPool, descriptor: string | unknown): SqlIdentityStore {
  return new SqlIdentityStore(postgresRunner(pool), descriptor);
}
