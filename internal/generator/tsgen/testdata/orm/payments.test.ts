// fixture-payments-db's tables end to end (D70, D103's step 4): orders that
// take payments over their own total and refunds of one.
// TestPaymentsTablesEndToEnd generates the package, type-checks it,
// applies the generated DDL and runs these cases on each dialect: on
// bun:sqlite, the SQLite create.sql D27 plans and the HTTP runtime's
// adapter over the layout orm.ts embeds (fixturePaymentsDbRules); on
// Postgres, the generated create.sql and the generated repository itself
// (createOrm). So the generator's layout, its DDL, the uses' owned columns,
// sub-tables and shared table, and the adapter are held to one another: an
// order placed waits until the adapter reports its payment captured, an
// event applies once, and a refund approved asks the order its relation
// points at for its amount, which the adapter's outcome on the order
// settles on both rows.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixturePaymentsDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const adapter: CoreCaller = { subject: 'adapter', permissions: ['payments.record'] };

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates an order row through the core and resolves to its key. */
  order(title: string, totalCents: number): Promise<string>;
  /** Creates a refund of an order through the core and resolves to its key. */
  refund(orderId: string, amountCents: number, reason: string): Promise<string>;
  /** Runs one of Payments' operations of a row as alice. */
  payments(table: 'Order' | 'Refund', key: string, operation: string, params: Record<string, unknown>): Promise<any>;
  /** Records a provider's event on an order as the adapter. */
  record(key: string, params: Record<string, unknown>): Promise<any>;
  /** The actions waiting for acmepay, as the adapter lists them. */
  actions(): Promise<any[]>;
  /** Moves a row's status as alice. */
  move(table: 'Order' | 'Refund', key: string, to: string): Promise<any>;
  /** A row's Payments state and amounts as the store holds them. */
  summary(table: 'Order' | 'Refund', key: string): Promise<{ state: unknown; captured: unknown; refunded: unknown }>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'payments-db-'));
  const file = join(dir, 'payments.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  const run = async (table: string, as: CoreCaller, key: string, useId: string, operation: string, params: unknown) => {
    const action = { kind: 'operation' as const, key, useId, operation, params };
    return (await execute(host, table, { caller: as, now: Date.now(), action })).result;
  };
  return {
    dialect: 'bun:sqlite',
    order: (title, totalCents) => runCreate(host, 'Order', { title, totalCents }, caller),
    refund: (orderId, amountCents, reason) => runCreate(host, 'Refund', { amountCents, reason, orderId }, caller),
    payments: (table, key, operation, params) => run(table, caller, key, 'Payments', operation, params),
    record: (key, params) => run('Order', adapter, key, 'Payments', 'recordPaymentEvent', params),
    async actions() {
      const action = { kind: 'tableOperation' as const, useId: 'Payments', operation: 'listPaymentActions', params: { provider: 'acmepay' } };
      return ((await execute(host, 'Order', { caller: adapter, now: Date.now(), action })).result as any).items;
    },
    move: (table, key, to) => run(table, caller, key, 'status', 'transition', { to }),
    async summary(table, key) {
      const row = await readRow(host, table, key);
      const owned = row?.owned.Payments;
      return { state: owned?.state ?? null, captured: owned?.captured ?? null, refunded: owned?.refunded ?? null };
    },
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_payments');
  const orm = createOrm(db.pool, alice);
  const adapted = orm.withBehaviorCaller(adapter);
  const repository = (table: 'Order' | 'Refund', of = orm) => (table === 'Order' ? of.order : of.refund) as any;
  return {
    dialect: 'Postgres',
    async order(title, totalCents) {
      return (await orm.order.createOne({ title, totalCents } as never)).id;
    },
    async refund(orderId, amountCents, reason) {
      return (await orm.refund.createOne({ amountCents, reason, orderId } as never)).id;
    },
    payments: (table, key, operation, params) => repository(table).payments[operation](key, params),
    record: (key, params) => adapted.order.payments.recordPaymentEvent(key as never, params as never),
    async actions() {
      return (await adapted.order.payments.listPaymentActions({ provider: 'acmepay' })).items;
    },
    move: (table, key, to) => repository(table).status.transition(key, { to }),
    async summary(table, key) {
      const row = await repository(table).findOne({ id: { eq: key } });
      return { state: row?.paymentsState ?? null, captured: row?.paymentsCaptured ?? null, refunded: row?.paymentsRefunded ?? null };
    },
    close: () => db.drop(),
  };
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

/** A captured event of an attempt's whole amount, as the adapter reports it. */
function captured(eventId: string, attempt: string, amount: number): Record<string, unknown> {
  return { provider: 'acmepay', eventId, kind: 'captured', amount, currency: 'GBP', at: Date.now(), attempt };
}

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`an order placed waits until its payment is captured, and an event applies once, on ${dialect.name}`, async () => {
      const order = await t.order('o1', 2500);
      expect(await t.summary('Order', order)).toEqual({ state: 'unpaid', captured: 0, refunded: 0 });
      const opened = await t.payments('Order', order, 'openPayment', { provider: 'acmepay', idempotencyKey: 'k1' });
      expect(opened).toEqual({ attempt: expect.stringMatching(/^pay_/), provider: 'acmepay', amount: 2500, currency: 'GBP', status: 'open' });
      expect(await t.payments('Order', order, 'openPayment', { provider: 'acmepay', idempotencyKey: 'k1' })).toEqual(opened);
      const waiting = (await t.actions()).filter(item => item.id === order);
      expect(waiting.map(item => [item.kind, item.attempt, item.amount])).toEqual([['create', opened.attempt, 2500]]);
      expect(await t.move('Order', order, 'held')).toEqual({ from: 'cart', to: 'held' });
      await expect(t.move('Order', order, 'placed')).rejects.toThrow(/moves to placed once 2500 is captured/);
      const event = captured(`e-${order}`, opened.attempt, 2500);
      expect(await t.record(order, event)).toMatchObject({ applied: true, state: 'paid' });
      expect(await t.record(order, event)).toMatchObject({ applied: false, state: 'paid' });
      expect((await t.actions()).filter(item => item.id === order)).toEqual([]);
      expect(await t.move('Order', order, 'placed')).toEqual({ from: 'held', to: 'placed' });
      expect(await t.summary('Order', order)).toEqual({ state: 'paid', captured: 2500, refunded: 0 });
    });

    test(`a refund approved asks its order for its amount, and the adapter's outcome settles both rows, on ${dialect.name}`, async () => {
      const order = await t.order('o2', 3000);
      const opened = await t.payments('Order', order, 'openPayment', { provider: 'acmepay', idempotencyKey: 'k1' });
      await t.record(order, captured(`e-${order}-1`, opened.attempt, 3000));
      const refund = await t.refund(order, 1000, 'damaged');
      expect(await t.summary('Refund', refund)).toEqual({ state: 'unpaid', captured: 0, refunded: 0 });
      expect(await t.move('Refund', refund, 'approved')).toEqual({ from: 'requested', to: 'approved' });
      expect(await t.summary('Refund', refund)).toEqual({ state: 'pending', captured: 0, refunded: 0 });
      const asked = (await t.actions()).filter(item => item.id === order);
      expect(asked.map(item => [item.kind, item.attempt, item.amount])).toEqual([['refund', opened.attempt, 1000]]);
      const outcome = { provider: 'acmepay', eventId: `e-${order}-2`, kind: 'refunded', amount: 1000, currency: 'GBP', at: Date.now(), refund: asked[0].refund };
      expect(await t.record(order, outcome)).toMatchObject({ applied: true, state: 'partiallyRefunded' });
      expect(await t.summary('Order', order)).toEqual({ state: 'partiallyRefunded', captured: 3000, refunded: 1000 });
      expect(await t.summary('Refund', refund)).toEqual({ state: 'refunded', captured: 0, refunded: 1000 });
      expect((await t.actions()).filter(item => item.id === order)).toEqual([]);
    });
  });
}
