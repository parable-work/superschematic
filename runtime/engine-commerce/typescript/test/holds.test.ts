// Holds: a checkout takes every line and its gift wrap as it enters held,
// all or none, keeps the prices and their total, commits as it is placed,
// releases as it is cancelled and returns as it is returned;
// holds_missing, held_fields, committed, no_supply and not_taking; an
// update in held that holds nothing takes what it now asks; listHolds;
// and a delete lets go. Real SQLite, the engine's Rust behaviors, a clock
// the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError } from '@superschematic/engine';

import type { HoldsRecord, StockRecord } from '../dist/index.js';
import { Clock, alice, checkout, checkoutHolds, cleanup, inventory, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const stocker = principal('sam', ['stock.adjust']);

function opened() {
  const clock = new Clock();
  const engine = openTestEngine({ clock: clock.now });
  publish(engine, inventory({ permission: 'stock.adjust', price: { field: 'priceCents', currency: 'GBP' } }));
  publish(engine, checkout(checkoutHolds));
  for (const [id, units, price] of [
    ['anvil', 3, 1250],
    ['rope', 5, 300],
    ['wrap', 2, 150],
  ] as const) {
    engine.instances.create(alice, 'Inventory', { title: id, priceCents: price }, { id });
    engine.instances.invoke(stocker, 'Inventory', id, 'receiveStock', { quantity: units, reason: 'delivery' });
  }
  const move = (id: string, to: string) => engine.instances.invoke(alice, 'Checkout', id, 'transition', { to });
  const holdsOf = (id: string) => engine.instances.get(alice, 'Checkout', id)?.behaviors.Holds as unknown as HoldsRecord;
  const stockOf = (id: string) => engine.instances.get(alice, 'Inventory', id)?.behaviors.Stock as unknown as StockRecord;
  const states = (id: string) => holdsOf(id).items.map((item) => item.state);
  const cart = (id: string, lines: Array<[string, number]>, wrap?: number) =>
    engine.instances.create(
      alice,
      'Checkout',
      { title: id, lines: lines.map(([inventoryId, quantity]) => ({ inventoryId, quantity })), ...(wrap ? { wrapCount: wrap } : {}) },
      { id, ...(wrap ? { behaviors: { Links: { wrap: 'wrap' } } } : {}) }
    );
  return { engine, clock, move, holdsOf, stockOf, states, cart };
}

describe('Holds', () => {
  test('a checkout takes every line and its wrap as it enters held, commits as it is placed and returns as it is returned', () => {
    const { move, holdsOf, stockOf, states, cart } = opened();
    const created = cart('c1', [
      ['anvil', 2],
      ['rope', 1],
    ], 1);
    assert.deepEqual([created.behaviors.Holds?.state, created.behaviors.Holds?.total], ['pending', undefined]);
    move('c1', 'held');
    assert.deepEqual(holdsOf('c1'), {
      items: [
        { hold: 'lines', item: 0, schema: 'Inventory', id: 'anvil', quantity: 2, state: 'held', unitPrice: 1250, currency: 'GBP' },
        { hold: 'lines', item: 1, schema: 'Inventory', id: 'rope', quantity: 1, state: 'held', unitPrice: 300, currency: 'GBP' },
        { hold: 'wrap', item: 0, schema: 'Inventory', id: 'wrap', quantity: 1, state: 'held', unitPrice: 150, currency: 'GBP' },
      ],
      state: 'held',
      total: { amount: 2950, currency: 'GBP' },
    });
    assert.deepEqual(stockOf('anvil'), { onHand: 3, held: 2, available: 1, low: false });
    move('c1', 'placed');
    assert.deepEqual(states('c1'), ['committed', 'committed', 'committed']);
    assert.equal(stockOf('anvil').onHand, 1);
    move('c1', 'returned');
    assert.deepEqual(states('c1'), ['returned', 'returned', 'returned']);
    assert.equal(stockOf('anvil').onHand, 3);
  });

  test('a claim holds all of its items or none, and the veto lists each that does not fit', () => {
    const { engine, move, stockOf, states, cart } = opened();
    cart('c1', [['anvil', 2]]);
    move('c1', 'held');
    cart('c2', [
      ['rope', 1],
      ['anvil', 2],
      ['rope', 9],
    ]);
    const refused = thrown(() => move('c2', 'held'), BehaviorVetoError);
    assert.deepEqual([refused.behavior, refused.vetoCode, refused.reason], ['Holds', 'unavailable', '2 items do not fit their supply']);
    assert.deepEqual(
      (refused.vetoDetails?.items as Array<Record<string, unknown>>).map((item) => [item.hold, item.item, item.available]),
      [
        ['lines', 1, 1],
        ['lines', 2, 5],
      ]
    );
    assert.deepEqual(states('c2'), ['pending', 'pending', 'pending']);
    assert.equal(stockOf('rope').held, 0);
    const listed = engine.instances.invoke(alice, 'Checkout', 'c2', 'listHolds', {}) as { items: Array<{ check?: { fits: boolean } }> };
    assert.deepEqual(
      listed.items.map((item) => item.check?.fits),
      [true, false, false]
    );
    move('c1', 'cancelled');
    assert.deepEqual(states('c1'), ['released']);
    engine.instances.update(alice, 'Checkout', 'c2', { lines: [{ inventoryId: 'rope', quantity: 1 }, { inventoryId: 'anvil', quantity: 2 }, { inventoryId: 'rope', quantity: 4 }] });
    move('c2', 'held');
    assert.equal(stockOf('rope').available, 0);
  });

  test('a claim commits only what it holds, and what it holds does not change', () => {
    const { engine, move, cart } = opened();
    cart('c1', [['anvil', 1]], 1);
    const missing = thrown(() => move('c1', 'placed'), BehaviorVetoError);
    assert.deepEqual([missing.vetoCode, missing.vetoDetails], [
      'holds_missing',
      {
        items: [
          { hold: 'lines', item: 0, state: 'pending' },
          { hold: 'wrap', item: 0, state: 'pending' },
        ],
      },
    ]);
    move('c1', 'held');
    const fields = thrown(() => engine.instances.update(alice, 'Checkout', 'c1', { lines: [{ inventoryId: 'anvil', quantity: 2 }] }), BehaviorVetoError);
    assert.deepEqual([fields.vetoCode, fields.vetoDetails], ['held_fields', { fields: ['lines'] }]);
    assert.equal(thrown(() => engine.instances.update(alice, 'Checkout', 'c1', { wrapCount: 2 }), BehaviorVetoError).vetoCode, 'held_fields');
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Checkout', 'c1', 'unlink', { name: 'wrap' }), BehaviorVetoError).vetoCode, 'held_fields');
    engine.instances.update(alice, 'Checkout', 'c1', { title: 'renamed' });
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Checkout', 'c1', 'retakeHolds', {}), BehaviorVetoError).vetoCode, 'not_taking');
    cart('c2', [['nothing', 1]]);
    assert.equal(thrown(() => move('c2', 'held'), BehaviorVetoError).vetoCode, 'no_supply');
  });

  test('an update in held that holds nothing takes what it now asks, and a delete lets go', () => {
    const { engine, move, holdsOf, stockOf, states, cart } = opened();
    cart('c1', []);
    move('c1', 'held');
    assert.equal(holdsOf('c1').state, 'none');
    assert.equal(thrown(() => engine.instances.update(alice, 'Checkout', 'c1', { lines: [{ inventoryId: 'anvil', quantity: 4 }] }), BehaviorVetoError).vetoCode, 'unavailable');
    engine.instances.update(alice, 'Checkout', 'c1', { lines: [{ inventoryId: 'anvil', quantity: 2 }] });
    assert.deepEqual(states('c1'), ['held']);
    assert.equal(stockOf('anvil').held, 2);
    assert.equal(engine.instances.delete(alice, 'Checkout', 'c1'), true);
    assert.equal(stockOf('anvil').held, 0);

    cart('c2', [['anvil', 2]]);
    move('c2', 'held');
    move('c2', 'placed');
    assert.equal(stockOf('anvil').onHand, 1);
    assert.equal(engine.instances.delete(alice, 'Checkout', 'c2'), true);
    assert.equal(stockOf('anvil').onHand, 3);
  });
});
