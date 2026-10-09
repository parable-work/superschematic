import { describe, expect, test } from 'bun:test';
import { Duplex } from 'node:stream';
import type { Pool } from 'pg';
import { CLOUD_SQL_CONNECTOR_PACKAGE, connectPostgres, ping, type CloudSqlConnectorModule } from './postgres';

/** The variable that names a Postgres the URL tests run against; they are skipped without it. */
const databaseVariable = 'SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL';
const dsn = process.env[databaseVariable] ?? '';

const cloudSql = { instance: 'acme:us-central1:shop', database: 'shop_db', user: 'shop-api@acme.iam' };

/** A connector module that records what the pools ask of it. */
function fakeConnector(fail?: Error) {
  const record = { built: 0, closed: 0, asked: [] as unknown[] };
  const stream = () => new Duplex({ read() {}, write(_chunk, _encoding, done) { done(); } });
  const module: CloudSqlConnectorModule = {
    Connector: class {
      constructor() {
        record.built++;
      }
      async getOptions(options: unknown) {
        record.asked.push(options);
        if (fail) throw fail;
        return { stream };
      }
      close() {
        record.closed++;
      }
    },
    IpAddressTypes: { PUBLIC: 'PUBLIC' },
    AuthTypes: { IAM: 'IAM' },
  };
  return { record, stream, load: async () => module };
}

function optionsOf(pool: Pool): Record<string, unknown> {
  return (pool as unknown as { options: Record<string, unknown> }).options;
}

describe('connectPostgres', () => {
  test.skipIf(dsn === '')('opens a pool over a connection string that ping answers', async () => {
    const pool = await connectPostgres({ url: dsn }, { pool: { max: 2 } });
    try {
      await ping(pool, 2000);
      const { rows } = await pool.query<{ answer: number }>('SELECT 41 + 1 AS answer');
      expect(rows).toEqual([{ answer: 42 }]);
      expect(optionsOf(pool).max).toBe(2);
    } finally {
      await pool.end();
    }
  });

  test('a pool connects when first used, so a database that is not up fails the ping, not the open', async () => {
    const pool = await connectPostgres({ url: 'postgres://nobody@127.0.0.1:9/none' });
    try {
      await expect(ping(pool, 2000)).rejects.toThrow();
    } finally {
      await pool.end();
    }
  });

  test('listens for a failing idle connection, with console.error by default', async () => {
    const seen: Error[] = [];
    const pool = await connectPostgres({ url: 'postgres://127.0.0.1:9/none' }, { onIdleError: error => seen.push(error) });
    expect(pool.listenerCount('error')).toBe(1);
    pool.emit('error', new Error('terminating connection'));
    expect(seen.map(error => error.message)).toEqual(['terminating connection']);
    await pool.end();
    const quiet = await connectPostgres({ url: 'postgres://127.0.0.1:9/none' });
    expect(quiet.listenerCount('error')).toBe(1);
    await quiet.end();
  });

  test('a Cloud SQL configuration dials through the connector with IAM authentication on the public IP, without TLS of pg', async () => {
    const connector = fakeConnector();
    const pool = await connectPostgres({ cloudSql }, { loadConnector: connector.load, pool: { max: 3 } });
    expect(connector.record.asked).toEqual([{ instanceConnectionName: 'acme:us-central1:shop', ipType: 'PUBLIC', authType: 'IAM' }]);
    const options = optionsOf(pool);
    expect(options).toMatchObject({ user: 'shop-api@acme.iam', database: 'shop_db', ssl: false, max: 3 });
    expect(options.stream).toBe(connector.stream);
    expect(options.password).toBeUndefined();
    await pool.end();
  });

  test('every Cloud SQL pool shares one connector, which ending the last pool closes', async () => {
    const connector = fakeConnector();
    const first = await connectPostgres({ cloudSql }, { loadConnector: connector.load });
    const second = await connectPostgres({ cloudSql: { ...cloudSql, database: 'orders_db' } }, { loadConnector: connector.load });
    expect(connector.record.built).toBe(1);
    await first.end();
    expect(connector.record.closed).toBe(0);
    await new Promise<void>(resolve => second.end(resolve));
    await Bun.sleep(0);
    expect(connector.record.closed).toBe(1);
    // The next pool builds a connector again.
    const third = await connectPostgres({ cloudSql }, { loadConnector: connector.load });
    expect(connector.record.built).toBe(2);
    await third.end();
  });

  test('refuses a Cloud SQL configuration clearly when the connector is not installed', async () => {
    const missing = async (): Promise<CloudSqlConnectorModule> => {
      throw new Error(`Cannot find package '${CLOUD_SQL_CONNECTOR_PACKAGE}'`);
    };
    await expect(connectPostgres({ cloudSql }, { loadConnector: missing })).rejects.toThrow(
      `Cloud SQL instance acme:us-central1:shop: a Cloud SQL connection needs ${CLOUD_SQL_CONNECTOR_PACKAGE}, which could not be loaded; add it to the server's dependencies`
    );
    // A later pool tries to load it again.
    const connector = fakeConnector();
    const pool = await connectPostgres({ cloudSql }, { loadConnector: connector.load });
    expect(connector.record.built).toBe(1);
    await pool.end();
  });

  test('loads the installed connector by default', async () => {
    const module = (await import(CLOUD_SQL_CONNECTOR_PACKAGE)) as CloudSqlConnectorModule;
    expect(module.IpAddressTypes.PUBLIC).toBe('PUBLIC');
    expect(module.AuthTypes.IAM).toBe('IAM');
  });

  test('a connector that cannot read the instance rejects, naming it, and is closed', async () => {
    const connector = fakeConnector(new Error('instance not found'));
    await expect(connectPostgres({ cloudSql }, { loadConnector: connector.load })).rejects.toThrow('Cloud SQL instance acme:us-central1:shop: instance not found');
    await Bun.sleep(0);
    expect(connector.record.closed).toBe(1);
  });
});

describe('ping', () => {
  test('rejects when the database does not answer in time', async () => {
    const silent = { query: () => new Promise<never>(() => {}) } as unknown as Pool;
    await expect(ping(silent, 20)).rejects.toThrow('the database did not answer within 20 ms');
  });

  test('resolves when SELECT 1 answers', async () => {
    const asked: string[] = [];
    const answering = { query: async (sql: string) => asked.push(sql) } as unknown as Pool;
    await ping(answering, 1000);
    expect(asked).toEqual(['SELECT 1']);
  });
});
