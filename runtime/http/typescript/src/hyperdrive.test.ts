import { describe, expect, test } from 'bun:test';
import { HyperdriveScopeError, hyperdrivePool, withHyperdrive } from './hyperdrive.js';
import { ping } from './postgres.js';
import { binding, WorkersBindingError } from './workers.js';

/*
The Hyperdrive pool (D92): each scope withHyperdrive runs opens a pool of
its own on first use, which its queries, connections and transactions
share and which it ends when it ends; a query outside every scope is
refused. Against a Postgres when SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL
names one, as a Hyperdrive binding's connection string names Hyperdrive's
pool; the Workers path, over workerd's own hyperdrive binding, is
servergen's TestTypeScriptHyperdriveUnderWorkerd's.
*/

const dsn = process.env.SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL ?? '';

describe('hyperdrivePool', () => {
  test('refuses a binding with no connection string', () => {
    expect(() => hyperdrivePool({ connectionString: '' })).toThrow('the binding holds no connection string');
  });

  test('refuses a query and a connect outside a scope, naming why', () => {
    const pool = hyperdrivePool({ connectionString: 'postgresql://user:password@127.0.0.1:9/db' });
    expect(() => pool.query('SELECT 1')).toThrow(HyperdriveScopeError);
    expect(() => pool.query('SELECT 1')).toThrow("a Hyperdrive pool's query ran outside a request or a firing");
    expect(() => pool.connect()).toThrow("a Hyperdrive pool's connect ran outside a request or a firing");
  });

  test('takes a Worker binding of the hyperdrive kind, and refuses another kind', () => {
    const env = { DB: { connectionString: 'postgresql://u:p@h.hyperdrive.local:5432/d' }, OTHER: { fetch() {} } };
    expect(binding(env, 'DB', 'hyperdrive').connectionString).toContain('hyperdrive.local');
    expect(() => binding(env, 'OTHER', 'hyperdrive')).toThrow(WorkersBindingError);
    expect(() => binding(env, 'OTHER', 'hyperdrive')).toThrow("the Worker's binding OTHER is not a Hyperdrive config");
  });

  test.skipIf(dsn === '')('opens a pool per scope, which its queries and transactions share and which it ends', async () => {
    const pool = hyperdrivePool({ connectionString: dsn });
    const backends: number[] = [];
    let ending: Promise<unknown> | undefined;
    await withHyperdrive(
      async () => {
        await ping(pool, 2000);
        const first = await pool.query('SELECT pg_backend_pid() AS pid, 9007199254740991::bigint AS big');
        expect(first.rows[0].big).toBe(9007199254740991);
        backends.push(first.rows[0].pid as number);
        const client = await pool.connect();
        try {
          await client.query('BEGIN');
          await client.query('CREATE TEMP TABLE hyperdrive_scope (n int) ON COMMIT DROP');
          await client.query('INSERT INTO hyperdrive_scope VALUES (1)');
          expect((await client.query('SELECT count(*)::int AS n FROM hyperdrive_scope')).rows[0].n).toBe(1);
          await client.query('COMMIT');
        } finally {
          client.release();
        }
        backends.push((await pool.query('SELECT pg_backend_pid() AS pid')).rows[0].pid as number);
      },
      promise => {
        ending = promise;
      }
    );
    // One connection served the scope, and the scope's waitUntil took its ending.
    expect(new Set(backends).size).toBe(1);
    expect(ending).toBeDefined();
    await ending;
    await withHyperdrive(async () => {
      const again = await pool.query('SELECT pg_backend_pid() AS pid');
      // The first scope ended its pool, so the next scope connected anew.
      expect(again.rows[0].pid).not.toBe(backends[0]);
    });
    expect(() => pool.query('SELECT 1')).toThrow(HyperdriveScopeError);
  });

  test.skipIf(dsn === '')('gives concurrent scopes pools of their own', async () => {
    const pool = hyperdrivePool({ connectionString: dsn });
    const pids = await Promise.all(
      [0, 1].map(() =>
        withHyperdrive(async () => {
          const client = await pool.connect();
          try {
            await client.query('SELECT pg_sleep(0.05)');
            return (await client.query('SELECT pg_backend_pid() AS pid')).rows[0].pid as number;
          } finally {
            client.release();
          }
        })
      )
    );
    expect(pids[0]).not.toBe(pids[1]);
  });
});
