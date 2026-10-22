// Stock: counts received, adjusted and counted by a caller with the
// config's permission, each a movement paged newest first; a claim's
// holds, commits, releases and returns through Holds; unavailable,
// over_max and below_held; the unit price a line keeps; Stock.low as a
// field and a list filter; and the delete refused while it holds. Real
// SQLite, the engine's Rust behaviors, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError } from '@superschematic/engine';

import type { StockRecord } from '../dist/index.js';
import { Clock, alice, checkout, checkoutHolds, cleanup, inventory, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const stocker = principal('sam', ['stock.adjust']);
const config = { permission: 'stock.adjust', price: { field: 'priceCents', currency: 'GBP' }, lowAt: { field: 'reorderAt' }, maxPerHold: 5 };

function opened(stock: Record<string, unknown> = config) {
  const clock = new Clock();
  const engine = openTestEngine({ clock: clock.now });
  publish(engine, inventory(stock));
  publish(engine, checkout(checkoutHolds));
  const invoke = (who: typeof alice, schema: string, id: string, operation: string, params: Record<string, unknown> = {}) =>
    engine.instances.invoke(who, schema, id, operation, params);
  const stockOf = (id: string) => engine.instances.get(alice, 'Inventory', id)?.behaviors.Stock as unknown as StockRecord;
  const receive = (id: string, quantity: number) => invoke(stocker, 'Inventory', id, 'receiveStock', { quantity, reason: 'delivery' });
  return { engine, clock, invoke, stockOf, receive };
}

describe('Stock', () => {
  test('counts are received, adjusted and counted with the permission, each a movement paged newest first', () => {
    const { engine, invoke, stockOf } = opened();
    engine.instances.create(alice, 'Inventory', { title: 'Anvil', priceCents: 1250, reorderAt: 2 }, { id: 'anvil' });
    assert.deepEqual(stockOf('anvil'), { onHand: 0, held: 0, available: 0, low: true });
    const forbidden = thrown(() => invoke(alice, 'Inventory', 'anvil', 'receiveStock', { quantity: 10, reason: 'delivery' }), EngineError);
    assert.equal(forbidden.code, 'forbidden');
    assert.deepEqual(invoke(stocker, 'Inventory', 'anvil', 'receiveStock', { quantity: 10, reason: 'delivery', reference: 'DN-1' }), {
      onHand: 10,
      held: 0,
      available: 10,
      movement: 1,
    });
    assert.deepEqual(invoke(stocker, 'Inventory', 'anvil', 'adjustStock', { delta: -3, reason: 'damaged' }), { onHand: 7, held: 0, available: 7, movement: 2 });
    assert.deepEqual(invoke(stocker, 'Inventory', 'anvil', 'adjustStock', { delta: 0, reason: 'nothing' }), { onHand: 7, held: 0, available: 7, movement: null });
    assert.deepEqual(invoke(stocker, 'Inventory', 'anvil', 'countStock', { onHand: 5, reason: 'stock take' }), { onHand: 5, held: 0, available: 5, movement: 3 });
    const below = thrown(() => invoke(stocker, 'Inventory', 'anvil', 'adjustStock', { delta: -6, reason: 'lost' }), BehaviorVetoError);
    assert.deepEqual([below.vetoCode, below.vetoDetails], ['below_held', { onHand: -1, held: 0 }]);

    const first = invoke(alice, 'Inventory', 'anvil', 'listStockMovements', { limit: 2 }) as { items: Array<Record<string, unknown>>; next: string | null };
    assert.deepEqual(
      first.items.map((item) => [item.seq, item.kind, item.delta, item.onHand]),
      [
        [3, 'count', -2, 5],
        [2, 'adjust', -3, 7],
      ]
    );
    const rest = invoke(alice, 'Inventory', 'anvil', 'listStockMovements', { limit: 2, cursor: first.next }) as { items: Array<Record<string, unknown>>; next: string | null };
    assert.deepEqual([rest.items[0].kind, rest.items[0].reference, rest.next], ['receive', 'DN-1', null]);
  });

  test('a checkout holds, commits and returns units, keeping the price it held them at', () => {
    const { engine, invoke, stockOf, receive } = opened();
    engine.instances.create(alice, 'Inventory', { title: 'Anvil', priceCents: 1250, reorderAt: 1 }, { id: 'anvil' });
    receive('anvil', 3);
    engine.instances.create(alice, 'Checkout', { title: 'c1', lines: [{ inventoryId: 'anvil', quantity: 2 }] }, { id: 'c1' });
    engine.instances.create(alice, 'Checkout', { title: 'c2', lines: [{ inventoryId: 'anvil', quantity: 2 }] }, { id: 'c2' });
    invoke(alice, 'Checkout', 'c1', 'transition', { to: 'held' });
    assert.deepEqual(stockOf('anvil'), { onHand: 3, held: 2, available: 1, low: true });
    const refused = thrown(() => invoke(alice, 'Checkout', 'c2', 'transition', { to: 'held' }), BehaviorVetoError);
    assert.deepEqual([refused.behavior, refused.vetoCode], ['Holds', 'unavailable']);

    engine.instances.update(alice, 'Inventory', 'anvil', { priceCents: 9999 });
    invoke(alice, 'Checkout', 'c1', 'transition', { to: 'placed' });
    assert.deepEqual(stockOf('anvil'), { onHand: 1, held: 0, available: 1, low: true });
    assert.deepEqual(engine.instances.get(alice, 'Checkout', 'c1')?.behaviors.Holds?.total, { amount: 2500, currency: 'GBP' });
    invoke(alice, 'Checkout', 'c1', 'transition', { to: 'returned' });
    assert.equal(stockOf('anvil').onHand, 3);
    const movements = invoke(alice, 'Inventory', 'anvil', 'listStockMovements', {}) as { items: Array<Record<string, unknown>> };
    assert.deepEqual(
      movements.items.map((item) => [item.kind, item.delta, item.holder]),
      [
        ['return', 2, { schema: 'Checkout', id: 'c1' }],
        ['commit', -2, { schema: 'Checkout', id: 'c1' }],
        ['receive', 3, undefined],
      ]
    );
  });

  test('a hold past maxPerHold is over_max, and a direct call of the contract is not_requested', () => {
    const { engine, invoke, receive } = opened();
    engine.instances.create(alice, 'Inventory', { title: 'Rope', priceCents: 300 }, { id: 'rope' });
    receive('rope', 10);
    engine.instances.create(alice, 'Checkout', { title: 'c1', lines: [{ inventoryId: 'rope', quantity: 6 }] }, { id: 'c1' });
    const refused = thrown(() => invoke(alice, 'Checkout', 'c1', 'transition', { to: 'held' }), BehaviorVetoError);
    assert.equal(refused.vetoCode, 'unavailable');
    assert.equal((refused.vetoDetails?.items as Array<Record<string, unknown>>)[0].code, 'over_max');
    const direct = thrown(
      () => invoke(alice, 'Inventory', 'rope', 'holdFor', { holder: { schema: 'Checkout', id: 'c1' }, hold: 'lines', item: 0, quantity: 6 }),
      BehaviorVetoError
    );
    assert.equal(direct.vetoCode, 'not_requested');
  });

  test('Stock.low is a list filter, and a supply that holds is not deleted', () => {
    const { engine, invoke, receive } = opened({ permission: 'stock.adjust', lowAt: 2 });
    for (const [id, quantity] of [
      ['anvil', 1],
      ['rope', 5],
      ['vice', 2],
    ] as const) {
      engine.instances.create(alice, 'Inventory', { title: id }, { id });
      receive(id, quantity);
    }
    const low = () => engine.instances.list(alice, 'Inventory', { where: { 'Stock.low': true } }).items.map((record) => record.id);
    assert.deepEqual(low(), ['anvil', 'vice']);
    engine.instances.create(alice, 'Checkout', { title: 'c1', lines: [{ inventoryId: 'rope', quantity: 3 }] }, { id: 'c1' });
    invoke(alice, 'Checkout', 'c1', 'transition', { to: 'held' });
    assert.deepEqual(low(), ['anvil', 'rope', 'vice']);
    const held = thrown(() => engine.instances.delete(alice, 'Inventory', 'rope'), BehaviorVetoError);
    assert.deepEqual([held.behavior, held.vetoCode], ['Stock', 'held']);
    invoke(alice, 'Checkout', 'c1', 'transition', { to: 'cancelled' });
    assert.equal(engine.instances.delete(alice, 'Inventory', 'rope'), true);
  });
});
