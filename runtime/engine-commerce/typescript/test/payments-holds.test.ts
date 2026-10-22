// Payments over a claim's Holds.total: a checkout owes what its lines were
// held at, which it cannot pay while they are pending; a total that cannot
// be read (priced lines in two currencies, or a sum past what an integer
// holds) fails closed, refusing openPayment and the move require holds
// with total_unknown; a claim whose lines carry no price owes nothing and
// is placed with no payment; and a config's currency must be the total's.
// Real SQLite, the engine's Rust behaviors, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Engine } from '@superschematic/engine';

import type { PaymentsRecord } from '../dist/index.js';
import { Clock, alice, checkout, checkoutHolds, cleanup, document, field, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const PROVIDER = 'acmepay';
const adapter = principal('adapter', ['payments.record']);
const stocker = principal('sam', ['stock.adjust']);

/** Payments of the claim's Holds.total, which must be paid to enter placed; `more` adds to it. */
function payments(more: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    name: 'Payments',
    config: { amount: { holds: true }, require: { placed: 'paid' }, voidOn: ['expired', 'cancelled'], record: { permission: 'payments.record' }, ...more },
  };
}

/**
 * An engine with Inventory, which Stock counts at its priceCents in its own
 * currency, or at no price, and the Checkout schema with Payments; items in
 * GBP and EUR, one at the largest price an integer field holds, and one with
 * no price.
 */
function opened(more: Record<string, unknown> = {}): Engine {
  const engine = openTestEngine({ clock: new Clock().now });
  publish(
    engine,
    document(
      'Inventory',
      [field('title', 'string'), field('priceCents', 'Generic.Int64'), field('currency', 'string')],
      [{ name: 'Stock', config: { permission: 'stock.adjust', price: { field: 'priceCents', currency: { field: 'currency' } } } }]
    )
  );
  publish(engine, checkout(checkoutHolds, [payments(more)]));
  for (const [id, price, currency, units] of [
    ['anvil', 1250, 'GBP', 10],
    ['rope', 300, 'GBP', 10],
    ['lamp', 900, 'EUR', 10],
    ['gold', Number.MAX_SAFE_INTEGER, 'GBP', 2000],
    ['leaflet', undefined, 'GBP', 10],
  ] as const) {
    engine.instances.create(alice, 'Inventory', { title: id, currency, ...(price === undefined ? {} : { priceCents: price }) }, { id });
    engine.instances.invoke(stocker, 'Inventory', id, 'receiveStock', { quantity: units, reason: 'delivery' });
  }
  return engine;
}

function cart(engine: Engine, id: string, lines: Array<[string, number]>): void {
  engine.instances.create(alice, 'Checkout', { title: id, lines: lines.map(([inventoryId, quantity]) => ({ inventoryId, quantity })) }, { id });
}

function move(engine: Engine, id: string, to: string): unknown {
  return engine.instances.invoke(alice, 'Checkout', id, 'transition', { to });
}

function pay(engine: Engine, id: string, key: string): { attempt: string; amount: number; currency: string } {
  return engine.instances.invoke(alice, 'Checkout', id, 'openPayment', { provider: PROVIDER, idempotencyKey: key }) as { attempt: string; amount: number; currency: string };
}

function paymentsOf(engine: Engine, id: string): PaymentsRecord {
  return engine.instances.get(alice, 'Checkout', id)?.behaviors.Payments as unknown as PaymentsRecord;
}

function vetoed(fn: () => unknown): [string | undefined, string | undefined] {
  const error = thrown(fn, BehaviorVetoError);
  return [error.behavior, error.vetoCode];
}

describe('Payments over Holds.total', () => {
  test('a checkout owes what its lines were held at, and nothing is known while they are pending', () => {
    const engine = opened();
    cart(engine, 'c1', [
      ['anvil', 2],
      ['rope', 1],
    ]);
    assert.deepEqual(vetoed(() => pay(engine, 'c1', 'k0')), ['Payments', 'total_unknown']);
    move(engine, 'c1', 'held');
    assert.deepEqual([paymentsOf(engine, 'c1').due, paymentsOf(engine, 'c1').currency], [2800, 'GBP']);
    const opened1 = pay(engine, 'c1', 'k1');
    assert.deepEqual([opened1.amount, opened1.currency], [2800, 'GBP']);
    assert.deepEqual(vetoed(() => move(engine, 'c1', 'placed')), ['Payments', 'unpaid']);
    engine.instances.invoke(adapter, 'Checkout', 'c1', 'recordPaymentEvent', {
      provider: PROVIDER,
      eventId: 'e1',
      kind: 'captured',
      amount: 2800,
      currency: 'GBP',
      at: 1_792_573_200_000,
      attempt: opened1.attempt,
    });
    move(engine, 'c1', 'placed');
    assert.equal(paymentsOf(engine, 'c1').state, 'paid');
  });

  test('a total that cannot be read fails closed, and a claim with nothing priced owes nothing', () => {
    const engine = opened();
    cart(engine, 'mixed', [
      ['anvil', 1],
      ['lamp', 1],
    ]);
    cart(engine, 'huge', [['gold', 1025]]);
    for (const id of ['mixed', 'huge']) {
      move(engine, id, 'held');
      assert.equal(paymentsOf(engine, id).due, 0, id);
      assert.deepEqual(vetoed(() => pay(engine, id, 'k')), ['Payments', 'total_unknown'], id);
      const refused = thrown(() => move(engine, id, 'placed'), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.vetoCode, refused.vetoDetails?.requires], ['Payments', 'total_unknown', 'paid'], id);
      move(engine, id, 'cancelled');
    }

    cart(engine, 'free', [['leaflet', 2]]);
    move(engine, 'free', 'held');
    assert.equal(paymentsOf(engine, 'free').due, 0);
    assert.deepEqual(vetoed(() => pay(engine, 'free', 'k')), ['Payments', 'nothing_due']);
    move(engine, 'free', 'placed');
    assert.equal(engine.instances.get(alice, 'Checkout', 'free')?.behaviors.Workflow?.status, 'placed');
  });

  test("a config's currency must be the total's", () => {
    const engine = opened({ currency: 'EUR' });
    cart(engine, 'c1', [['rope', 1]]);
    move(engine, 'c1', 'held');
    assert.deepEqual(vetoed(() => pay(engine, 'c1', 'k')), ['Payments', 'currency_mismatch']);
  });
});
