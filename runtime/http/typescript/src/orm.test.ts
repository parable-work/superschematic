import { describe, expect, test } from 'bun:test';
import { fenceBumps, ormOf, type Clauses, type FenceDefinition, type OrmDefinition, type OrmPool, type OrmQueryable, type OrmTx } from './orm';
import type { RowField } from './pgcodec';

// The statements an ORM's transaction runs around its function (D89),
// over a pool that records each statement and answers with rows of its
// own, so no database is needed: the Postgres tests of the generated
// packages run the same against one.

/** Every clause, as a format the fill reads; no table uses them here. */
const clauses = new Proxy({}, { get: () => '%s' }) as Clauses;

const definition: OrmDefinition = { clauses, defaultLimit: 100, historyActorCTE: '', clearHistoryActor: '', tables: {} };

/** A pool that records each statement, answering each with rows. */
function recording(rows: Record<string, unknown>[] = []): { pool: OrmPool; statements: string[]; released: (Error | boolean | undefined)[] } {
  const statements: string[] = [];
  const released: (Error | boolean | undefined)[] = [];
  const queryable: OrmQueryable = {
    async query(config) {
      statements.push(config.text);
      return { rows, rowCount: rows.length };
    },
  };
  return {
    statements,
    released,
    pool: {
      ...queryable,
      async connect() {
        return { ...queryable, release: (err?: Error | boolean) => void released.push(err) };
      },
    },
  };
}

describe('an ORM transaction', () => {
  test('begins at its isolation level, read only or not, and commits', async () => {
    const { pool, statements, released } = recording();
    const orm = ormOf<Record<string, never>>(pool, definition);
    await orm.transaction(async () => undefined);
    await orm.transaction(async () => undefined, { isolation: 'repeatable read', readOnly: true });
    await orm.transaction(async () => undefined, { isolation: 'serializable' });
    await orm.transaction(async () => undefined, { readOnly: true });
    expect(statements).toEqual([
      'BEGIN',
      'COMMIT',
      'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY',
      'COMMIT',
      'BEGIN ISOLATION LEVEL SERIALIZABLE',
      'COMMIT',
      'BEGIN READ ONLY',
      'COMMIT',
    ]);
    expect(released).toEqual([false, false, false, false]);
    await expect(orm.transaction(async () => undefined, { isolation: 'snapshot' as never })).rejects.toThrow(/isolation is read committed, repeatable read, serializable/);
  });

  test('nests a savepoint per depth, released when its function returns and rolled back to when it throws', async () => {
    const { pool, statements } = recording();
    const orm = ormOf<Record<string, never>>(pool, definition);
    const out = await orm.transaction(async tx => {
      await tx.transaction(async inner => {
        await inner.exec('first');
        await inner.transaction(async innermost => innermost.exec('second'));
      });
      await expect(tx.transaction(async () => Promise.reject(new Error('fails')))).rejects.toThrow('fails');
      return tx.exec('after');
    });
    expect(out).toBe(0);
    expect(statements).toEqual([
      'BEGIN',
      'SAVEPOINT sp_1',
      'first',
      'SAVEPOINT sp_2',
      'second',
      'RELEASE SAVEPOINT sp_2',
      'RELEASE SAVEPOINT sp_1',
      'SAVEPOINT sp_1',
      'ROLLBACK TO SAVEPOINT sp_1',
      'RELEASE SAVEPOINT sp_1',
      'after',
      'COMMIT',
    ]);
  });

  test('a throw rolls it back and returns its connection to the pool', async () => {
    const { pool, statements, released } = recording();
    const orm = ormOf<Record<string, never>>(pool, definition);
    await expect(orm.transaction(async () => Promise.reject(new Error('no')))).rejects.toThrow('no');
    expect(statements).toEqual(['BEGIN', 'ROLLBACK']);
    expect(released).toEqual([false]);
  });
});

describe("a raw query's rows", () => {
  test('read through fields as decodeRow reads them, on the ORM and in a transaction, and as pg gives them without', async () => {
    const id = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
    const { pool } = recording([{ id, amount: '12', extra: true }]);
    const orm = ormOf<Record<string, never>>(pool, definition);
    const fields: RowField[] = [
      { name: 'id', column: 'id', uuid: true },
      { name: 'amount', column: 'amount', int64: true },
    ];
    const read = await orm.query<{ id: string; amount: number }>('SELECT id, amount, extra FROM t', [], { decode: fields });
    expect(read.rows).toEqual([{ id: 'TKbHsHDO6x9RCjbs5c2u4', amount: 12 }]);
    const raw = await orm.query<{ id: string }>('SELECT id FROM t');
    expect(raw.rows[0]!.id).toBe(id);
    const inTx = await orm.transaction((tx: OrmTx<Record<string, never>>) => tx.query<{ id: string }>({ text: 'SELECT id FROM t', values: [] }, undefined, { decode: fields }));
    expect(inTx.rows[0]!.id).toBe('TKbHsHDO6x9RCjbs5c2u4');
    expect(await orm.exec('UPDATE t SET x = 1')).toBe(1);
  });
});

describe("a table's fences", () => {
  // The fences of fixture-fenced-db's tables, as its ./orm entry holds them
  // (D119); the Postgres test of the generated package runs the
  // repositories over them.
  const plan = 'plan_version = delivery.plan_version + 1';
  const run = 'run_version = delivery.run_version + 1';
  const delivery: FenceDefinition[] = [
    { column: 'plan_version', covers: ['address', 'courier_id'], bump: plan },
    { column: 'run_version', covers: ['attempts', 'proof'], bump: run },
  ];
  const parcel: FenceDefinition[] = [{ column: 'version', bump: 'version = parcel.version + 1' }];
  const depot: FenceDefinition[] = [{ column: 'revision', covers: ['"name"', 'deleted_at'], bump: 'revision = depot.revision + 1' }];

  test('an update advances each fence that covers a column it sets, once, and none it sets itself', () => {
    const cases: [FenceDefinition[], string[], string[]][] = [
      [delivery, ['address = $1', 'updated_at = $2'], [plan]],
      [delivery, ['courier_id = NULL'], [plan]],
      [delivery, ['proof = EXCLUDED.proof', 'address = EXCLUDED.address'], [plan, run]],
      [delivery, ['attempts = $1', 'proof = $2'], [run]],
      [delivery, ['note = $1', 'updated_at = $2'], []],
      [delivery, ['address = $1', 'plan_version = $2', 'attempts = $3'], [run]],
      [delivery, ['plan_version = $1'], []],
      [delivery, [], []],
      [parcel, ['note = NULL'], ['version = parcel.version + 1']],
      [parcel, ['note = $1', 'version = $2'], []],
      [parcel, [], []],
      [depot, ['capacity = $1'], []],
      [depot, ['"name" = $1'], ['revision = depot.revision + 1']],
      // A column is read whole: name_note is no write of name.
      [depot, ['"name_note" = $1', 'name = $2'], []],
    ];
    for (const [fences, sets, want] of cases) expect(fenceBumps(fences, sets)).toEqual(want);
  });
});
