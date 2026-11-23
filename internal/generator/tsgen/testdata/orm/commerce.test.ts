// fixture-commerce-db's tables end to end (D70, D103's step 4): an
// inventory Stock counts and checkouts that hold its units through the
// contract Supply. TestCommerceTablesEndToEnd generates the package,
// type-checks it, applies the generated DDL and runs these cases on each
// dialect: on bun:sqlite, the SQLite create.sql D27 plans and the HTTP
// runtime's adapter over the layout orm.ts embeds (fixtureCommerceDbRules);
// on Postgres, the generated create.sql and the generated repository itself
// (createOrm). So the generator's layout, its DDL, the uses' owned columns
// and sub-tables, and the adapter are held to one another: staff receive
// stock, a checkout entering held takes its lines at the price they are
// held at, under the inventory's lock, and two checkouts for the last unit
// do not both hold it; the other's move is unavailable until the first lets
// go, and placing commits the units, a movement of the ledger.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import { execute, readRow, runCreate, sqliteRulesStore, type CoreCaller, type RulesHost } from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureCommerceDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const staff: CoreCaller = { subject: 'sam', permissions: ['stock.adjust'] };

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates an inventory row through the core and resolves to its key. */
  inventory(title: string, priceCents: number): Promise<string>;
  /** Creates a checkout of one line through the core and resolves to its key. */
  checkout(title: string, inventoryId: string, quantity: number): Promise<string>;
  /** Runs one of Stock's operations of an inventory row as staff. */
  stock(key: string, operation: string, params: Record<string, unknown>): Promise<any>;
  /** Moves a checkout's status as alice. */
  move(key: string, to: string): Promise<any>;
  /** An inventory row's counts as the store holds them. */
  counts(key: string): Promise<{ onHand: unknown; held: unknown }>;
  /** A checkout's Holds state and total as the store holds them. */
  holds(key: string): Promise<{ state: unknown; total: unknown }>;
  close(): Promise<void>;
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'commerce-db-'));
  const file = join(dir, 'commerce.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  return {
    dialect: 'bun:sqlite',
    inventory: (title, priceCents) => runCreate(host, 'Inventory', { title, priceCents }, caller),
    checkout: (title, inventoryId, quantity) => runCreate(host, 'Checkout', { title, lines: [{ inventoryId, quantity }] }, caller),
    async stock(key, operation, params) {
      const action = { kind: 'operation' as const, key, useId: 'Stock', operation, params };
      return (await execute(host, 'Inventory', { caller: staff, now: Date.now(), action })).result;
    },
    async move(key, to) {
      const action = { kind: 'operation' as const, key, useId: 'status', operation: 'transition', params: { to } };
      return (await execute(host, 'Checkout', { caller, now: Date.now(), action })).result;
    },
    async counts(key) {
      const row = await readRow(host, 'Inventory', key);
      return { onHand: row?.owned.Stock?.onHand ?? null, held: row?.owned.Stock?.held ?? null };
    },
    async holds(key) {
      const row = await readRow(host, 'Checkout', key);
      return { state: row?.owned.Holds?.state ?? null, total: row?.owned.Holds?.total ?? null };
    },
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_commerce');
  const orm = createOrm(db.pool, alice);
  const staffed = orm.withBehaviorCaller(staff);
  return {
    dialect: 'Postgres',
    async inventory(title, priceCents) {
      return (await orm.inventory.createOne({ title, priceCents } as never)).id;
    },
    async checkout(title, inventoryId, quantity) {
      return (await orm.checkout.createOne({ title, lines: [{ inventoryId, quantity }] } as never)).id;
    },
    stock: (key, operation, params) => (staffed.inventory.stock as any)[operation](key, params),
    move: (key, to) => orm.checkout.status.transition(key as never, { to } as never),
    async counts(key) {
      const row = (await orm.inventory.findOne({ id: { eq: key } } as never)) as any;
      return { onHand: row?.stockOnHand ?? null, held: row?.stockHeld ?? null };
    },
    async holds(key) {
      const row = (await orm.checkout.findOne({ id: { eq: key } } as never)) as any;
      return { state: row?.holdsState ?? null, total: row?.holdsTotal ?? null };
    },
    close: () => db.drop(),
  };
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a checkout entering held takes its lines at their price, and placing commits them, on ${dialect.name}`, async () => {
      const anvil = await t.inventory('anvil', 1250);
      expect(await t.counts(anvil)).toEqual({ onHand: 0, held: 0 });
      expect(await t.stock(anvil, 'receiveStock', { quantity: 3, reason: 'delivery' })).toEqual({ onHand: 3, held: 0, available: 3, movement: 1 });
      const cart = await t.checkout('c1', anvil, 2);
      expect(await t.holds(cart)).toEqual({ state: 'pending', total: { amount: 0, currency: null } });
      expect(await t.move(cart, 'held')).toEqual({ from: 'cart', to: 'held' });
      expect(await t.counts(anvil)).toEqual({ onHand: 3, held: 2 });
      expect(await t.holds(cart)).toEqual({ state: 'held', total: { amount: 2500, currency: 'GBP' } });
      expect(await t.move(cart, 'placed')).toEqual({ from: 'held', to: 'placed' });
      expect(await t.counts(anvil)).toEqual({ onHand: 1, held: 0 });
      const moves = await t.stock(anvil, 'listStockMovements', {});
      expect(moves.items.map((item: any) => [item.seq, item.kind, item.delta, item.onHand])).toEqual([
        [2, 'commit', -2, 1],
        [1, 'receive', 3, 3],
      ]);
    });

    test(`two checkouts for the last unit do not both hold it, on ${dialect.name}`, async () => {
      const tea = await t.inventory('tea', 400);
      await t.stock(tea, 'receiveStock', { quantity: 1, reason: 'delivery' });
      const first = await t.checkout('first', tea, 1);
      const second = await t.checkout('second', tea, 1);
      const moved = await Promise.allSettled([t.move(first, 'held'), t.move(second, 'held')]);
      const held = moved.filter(result => result.status === 'fulfilled');
      expect(held.length).toBe(1);
      const refused = moved.find(result => result.status === 'rejected') as PromiseRejectedResult;
      expect(String(refused.reason?.message ?? refused.reason)).toMatch(/1 asked of Inventory \S+, which has 0 available/);
      expect(await t.counts(tea)).toEqual({ onHand: 1, held: 1 });
      const [winner, loser] = moved[0]!.status === 'fulfilled' ? [first, second] : [second, first];
      expect(await t.move(winner, 'cancelled')).toEqual({ from: 'held', to: 'cancelled' });
      expect(await t.counts(tea)).toEqual({ onHand: 1, held: 0 });
      expect(await t.move(loser, 'held')).toEqual({ from: 'cart', to: 'held' });
      expect(await t.counts(tea)).toEqual({ onHand: 1, held: 1 });
    });
  });
}
