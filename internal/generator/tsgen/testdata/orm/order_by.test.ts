// fixture-queue-db's Order on Postgres (D79), the counterpart of the Go
// ORM's order_by_postgres_test.go: its table is named order, a Postgres
// keyword, and findMany and findOne order it by a column, the ORDER BY
// naming the table as the FROM clause does, "order".
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createOrm, type FixtureQueueDbOrm } from './orm';
import { database, dsn, type TestDatabase } from './harness';

let db: TestDatabase;
let orm: FixtureQueueDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_order_by');
  orm = createOrm(db.pool);
});

afterAll(async () => {
  await db?.drop();
});

describe.skipIf(dsn === '')('order by', () => {
  test('findMany and findOne order a keyword-named table by a column, each way', async () => {
    // Three orders, created out of the order they were placed in.
    for (const day of [2, 3, 1]) {
      await orm.order.createOne({ placedAt: new Date(Date.UTC(2026, 9, day, 12)) });
    }
    for (const [desc, want] of [
      [false, [1, 2, 3]],
      [true, [3, 2, 1]],
    ] as const) {
      const orderBy = [{ field: 'placedAt' as const, desc }, { field: 'id' as const }];
      const { rows, total } = await orm.order.findMany(null, { orderBy });
      expect(total).toBe(3);
      expect(rows.map(r => r.placedAt.getUTCDate())).toEqual([...want]);
      const first = await orm.order.findOne(null, { orderBy });
      expect(first?.placedAt.getUTCDate()).toBe(want[0]);
    }
    // A page of a keyword-named table, by the column.
    const page = await orm.order.findPage(null, { sort: { field: 'placedAt', desc: true }, limit: 2 });
    expect(page.rows.map(r => r.placedAt.getUTCDate())).toEqual([3, 2]);
    const next = await orm.order.findPage(null, { sort: { field: 'placedAt', desc: true }, limit: 2, after: page.next });
    expect(next.rows.map(r => r.placedAt.getUTCDate())).toEqual([1]);
  });
});
