// fixture-queue-db's keyed queues under the repositories on Postgres (D79,
// D80): enqueue(tx, queue, message) in a transaction of the ORM ends with
// the queue's conflict, so a queue that keeps one waiting message per key
// keeps one, skip the first and replace the latest; the trigger a line's
// write fires in that transaction enqueues under the same rule, against
// the code's messages; and a transaction that throws enqueues nothing.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { enqueue, uuidToDb } from '@superschematic/http-runtime/postgres';
import { createOrm, type FixtureQueueDbOrm } from './orm';
import { orderChangedQueue, stockCheckedQueue } from './queues';
import type { GenericInt64, Priority } from './types';
import { database, dsn, type TestDatabase } from './harness';

const low = 'low' as Priority;
const high = 'high' as Priority;
const n = (v: number) => v as GenericInt64;

let db: TestDatabase;
let orm: FixtureQueueDbOrm;

beforeAll(async () => {
  db = await database('ts_orm_queue_key');
  orm = createOrm(db.pool);
});

afterAll(async () => {
  await db?.drop();
});

/** The waiting messages of a queue's table whose key column holds value, oldest first. */
async function waiting(table: string, key: string, value: string): Promise<Record<string, unknown>[]> {
  const { rows } = await db.pool.query(`SELECT * FROM ${table} WHERE state = 'ready' AND ${key} = $1 ORDER BY created_at, id`, [value]);
  return rows;
}

describe.skipIf(dsn === '')('keyed queues in a transaction', () => {
  test('an enqueue in a transaction of the ORM keeps one waiting message per key: skip the first, replace the latest', async () => {
    await orm.transaction(async tx => {
      await enqueue(tx, stockCheckedQueue, { sku: 'SKU-1', quantity: n(1) });
      await enqueue(tx, stockCheckedQueue, { sku: 'SKU-1', quantity: n(2) });
    });
    await orm.transaction(tx => enqueue(tx, stockCheckedQueue, { sku: 'SKU-1', quantity: n(3) }));
    expect((await waiting('stock_checked_queue', 'sku', 'SKU-1')).map(r => Number(r.quantity))).toEqual([1]);

    const order = await orm.order.createOne({ placedAt: new Date() });
    await orm.transaction(async tx => {
      for (const priority of [low, high, low]) await enqueue(tx, orderChangedQueue, { orderId: order.id, priority });
    });
    await orm.transaction(tx => enqueue(tx, orderChangedQueue, { orderId: order.id, priority: high }));
    const replaced = await waiting('order_changed_queue', 'order_id', uuidToDb(order.id));
    expect(replaced.map(r => [r.priority, r.attempts])).toEqual([['high', 0]]);
  });

  test("a line the repositories write enqueues its order's change through the trigger, under the queue's key with the code's", async () => {
    const product = await orm.product.createOne({ name: 'Widget' });
    const order = await orm.order.createOne({ placedAt: new Date() });
    const line = await orm.transaction(async tx => {
      const line = await tx.orderLine.createOne({ orderId: order.id, productId: product.id, quantity: n(1) });
      await tx.orderLine.updateOne(line.id, { quantity: n(2) });
      // A change to no watched field enqueues nothing.
      await tx.orderLine.updateOne(line.id, { note: 'a gift' });
      // The code's message replaces the trigger's: one the new message leaves unset takes its default.
      await enqueue(tx, orderChangedQueue, { orderId: order.id, priority: high });
      return line;
    });
    const inTransaction = await waiting('order_changed_queue', 'order_id', uuidToDb(order.id));
    expect(inTransaction.map(r => [r.line_id, r.priority])).toEqual([[null, 'high']]);
    // A later write replaces it with the trigger's message, the line's.
    await orm.orderLine.updateOne(line.id, { quantity: n(3) });
    const later = await waiting('order_changed_queue', 'order_id', uuidToDb(order.id));
    expect(later.map(r => [r.line_id, r.priority])).toEqual([[uuidToDb(line.id), 'low']]);
  });

  test('a transaction that throws enqueues nothing, by its code or by its trigger', async () => {
    const product = await orm.product.createOne({ name: 'Gadget' });
    const order = await orm.order.createOne({ placedAt: new Date() });
    await expect(
      orm.transaction(async tx => {
        await tx.orderLine.createOne({ orderId: order.id, productId: product.id, quantity: n(1) });
        await enqueue(tx, stockCheckedQueue, { sku: 'SKU-rolled-back', quantity: n(1) });
        throw new Error('the import failed');
      })
    ).rejects.toThrow('the import failed');
    expect(await waiting('order_changed_queue', 'order_id', uuidToDb(order.id))).toEqual([]);
    expect(await waiting('stock_checked_queue', 'sku', 'SKU-rolled-back')).toEqual([]);
    // The key is free: the next transaction's message waits.
    await orm.transaction(tx => enqueue(tx, stockCheckedQueue, { sku: 'SKU-rolled-back', quantity: n(2) }));
    expect((await waiting('stock_checked_queue', 'sku', 'SKU-rolled-back')).map(r => Number(r.quantity))).toEqual([2]);
  });
});
