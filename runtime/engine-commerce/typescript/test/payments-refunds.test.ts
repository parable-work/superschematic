// Payments' refund instances: a Refund with Workflow, Approvals' two
// sign-offs, a required link to the order it refunds, Reactions and
// Payments with refundOf and issueOn. Entering approved asks the order's
// refundFor, which records the refund and its action; one approval pays
// one refund, whatever key a caller who reaches refundFor makes up; the
// provider's outcome, reported to the order, comes back through
// settleRefundFor, which takes only what the order recorded; the Refund
// turns refunded and a Reactions rule moves it to issued. A refund schema
// records with its order's permission. Real SQLite, the engine's Rust
// behaviors and its runner, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError, type Engine } from '@superschematic/engine';

import type { PaymentAction, PaymentsRecord } from '../dist/index.js';
import { Clock, alice, cleanup, document, field, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const PROVIDER = 'acmepay';
const RECORD = 'payments.record';
const adapter = principal('adapter', [RECORD]);
const bob = principal('bob', ['refunds.approve']);
const carol = principal('carol', ['refunds.approve']);
const runner = principal('runner');

function refundDocument(record: string): Record<string, unknown> {
  return document(
    'Refund',
    [field('amountCents', 'Generic.Int64', {}), field('reason', 'string')],
    [
      {
        name: 'Workflow',
        config: {
          states: ['requested', 'approved', 'issued', 'rejected', 'failed'],
          initial: 'requested',
          transitions: [
            { from: 'requested', to: 'approved' },
            { from: 'requested', to: 'rejected' },
            { from: 'approved', to: 'issued' },
            { from: 'approved', to: 'failed' },
            { from: 'failed', to: 'approved' },
          ],
          outcomes: { rejected: 'neutral' },
        },
      },
      { name: 'Links', config: { links: { order: { schema: 'Order', required: true } } } },
      { name: 'Approvals', config: { gates: { approved: { count: 2, permission: 'refunds.approve' } } } },
      {
        name: 'Payments',
        config: { refundOf: { link: 'order' }, amount: { field: 'amountCents' }, currency: 'GBP', issueOn: ['approved'], record: { permission: record } },
      },
      {
        name: 'Reactions',
        config: {
          rules: [
            { when: { changes: { field: 'Payments.state', to: 'refunded' } }, then: { transition: 'issued' } },
            { when: { changes: { field: 'Payments.state', from: 'pending', to: 'unpaid' } }, then: { transition: 'failed' } },
          ],
        },
      },
    ]
  );
}

/** The Order schema: a title and a totalCents it owes in GBP. */
function orderDocument(): Record<string, unknown> {
  return document(
    'Order',
    [field('title', 'string'), field('totalCents', 'Generic.Int64')],
    [
      {
        name: 'Workflow',
        config: { states: ['open', 'closed'], initial: 'open', transitions: [{ from: 'open', to: 'closed' }] },
      },
      { name: 'Payments', config: { amount: { field: 'totalCents' }, currency: 'GBP', record: { permission: RECORD } } },
    ]
  );
}

/** An engine with an Order o1 of 3000 GBP, paid, and the Refund schema. */
function shop(clock = new Clock()): Engine {
  const engine = openTestEngine({ clock: clock.now, runner: { principal: runner } });
  publish(engine, orderDocument());
  publish(engine, refundDocument(RECORD));
  engine.runner.runDue();
  engine.instances.create(alice, 'Order', { title: 'o1', totalCents: 3000 }, { id: 'o1' });
  const { attempt } = engine.instances.invoke(alice, 'Order', 'o1', 'openPayment', { provider: PROVIDER, idempotencyKey: 'k' }) as { attempt: string };
  report(engine, 'e-paid', 'captured', 3000, { attempt });
  return engine;
}

function report(engine: Engine, eventId: string, kind: string, amount: number, more: Record<string, unknown>): unknown {
  return engine.instances.invoke(adapter, 'Order', 'o1', 'recordPaymentEvent', { provider: PROVIDER, eventId, kind, amount, currency: 'GBP', at: 1_792_573_200_000, ...more });
}

function approved(engine: Engine, id: string, amount: number): void {
  engine.instances.create(alice, 'Refund', { amountCents: amount, reason: 'damaged' }, { id, behaviors: { Links: { order: 'o1' } } });
  engine.instances.invoke(bob, 'Refund', id, 'signOff', { state: 'approved' });
  engine.instances.invoke(carol, 'Refund', id, 'signOff', { state: 'approved' });
  engine.instances.invoke(alice, 'Refund', id, 'transition', { to: 'approved' });
}

function refunds(engine: Engine): PaymentAction[] {
  return (engine.instances.invokeSchema(adapter, 'Order', 'listPaymentActions', { provider: PROVIDER }) as { items: PaymentAction[] }).items.filter(
    (action) => action.kind === 'refund'
  );
}

function paymentsOf(engine: Engine, schema: string, id: string): PaymentsRecord {
  return engine.instances.get(alice, schema, id)?.behaviors.Payments as unknown as PaymentsRecord;
}

function refundFor(engine: Engine, amount: number, key: string): unknown {
  return engine.instances.invoke(alice, 'Order', 'o1', 'refundFor', { refund: { schema: 'Refund', id: 'rf-1' }, amount, idempotencyKey: key });
}

describe('Payments refunds', () => {
  test('a refund is asked once approved, one approval pays one refund whatever the key, and the provider settles it', () => {
    const engine = shop();
    engine.instances.create(alice, 'Refund', { amountCents: 1000, reason: 'damaged' }, { id: 'rf-1', behaviors: { Links: { order: 'o1' } } });
    // Not approved yet: refundFor is refused.
    assert.equal(thrown(() => refundFor(engine, 1000, 'Refund:rf-1.0'), BehaviorVetoError).vetoCode, 'not_linked');
    engine.instances.invoke(bob, 'Refund', 'rf-1', 'signOff', { state: 'approved' });
    engine.instances.invoke(carol, 'Refund', 'rf-1', 'signOff', { state: 'approved' });
    engine.instances.invoke(alice, 'Refund', 'rf-1', 'transition', { to: 'approved' });
    const asked = refunds(engine);
    assert.deepEqual(
      asked.map((action) => action.amount),
      [1000]
    );
    assert.equal(paymentsOf(engine, 'Refund', 'rf-1').state, 'pending');

    // Made-up keys ask nothing more; the derived one returns the same refund.
    for (const key of ['x1', 'x2', 'Refund:rf-1.1']) {
      assert.equal(thrown(() => refundFor(engine, 1000, key), BehaviorVetoError).vetoCode, 'key_reused', key);
    }
    assert.equal((refundFor(engine, 1000, 'Refund:rf-1.0') as { refund: string }).refund, asked[0]?.refund);
    assert.equal(refunds(engine).length, 1);

    // A settlement the order did not record is refused.
    const settle = (status: string, amount: number) =>
      engine.instances.invoke(adapter, 'Refund', 'rf-1', 'settleRefundFor', { of: { schema: 'Order', id: 'o1' }, refund: asked[0]?.refund, status, amount });
    assert.equal(thrown(() => settle('succeeded', 1000), BehaviorVetoError).vetoCode, 'not_linked');
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Refund', 'rf-1', 'settleRefundFor', { of: { schema: 'Order', id: 'o1' }, refund: asked[0]?.refund, status: 'succeeded', amount: 1000 }), EngineError).code, 'forbidden');

    report(engine, 'e-rf', 'refunded', 1000, { refund: asked[0]?.refund });
    assert.equal(paymentsOf(engine, 'Refund', 'rf-1').state, 'refunded');
    assert.equal(thrown(() => refundFor(engine, 1000, 'x3'), BehaviorVetoError).vetoCode, 'key_reused');
    assert.equal(thrown(() => settle('succeeded', 900), BehaviorVetoError).vetoCode, 'not_linked');
    engine.runner.runDue();
    assert.equal(engine.instances.get(alice, 'Refund', 'rf-1')?.behaviors.Workflow?.status, 'issued');
    const order = paymentsOf(engine, 'Order', 'o1');
    assert.deepEqual([order.refunded, order.state], [1000, 'partiallyRefunded']);
  });

  test('a failed refund turns the Refund failed, and approving it again asks under the next key', () => {
    const clock = new Clock();
    const engine = shop(clock);
    approved(engine, 'rf-1', 1200);
    const first = refunds(engine)[0]?.refund;
    report(engine, 'e-f', 'refundFailed', 1200, { refund: first });
    engine.runner.runDue();
    assert.equal(engine.instances.get(alice, 'Refund', 'rf-1')?.behaviors.Workflow?.status, 'failed');
    // The failed one's key no longer asks; the next is the Refund's to use.
    assert.equal(thrown(() => refundFor(engine, 1200, 'Refund:rf-1.0'), BehaviorVetoError).vetoCode, 'not_linked');
    clock.advance(1000);
    engine.instances.invoke(bob, 'Refund', 'rf-1', 'signOff', { state: 'approved' });
    engine.instances.invoke(carol, 'Refund', 'rf-1', 'signOff', { state: 'approved' });
    engine.instances.invoke(alice, 'Refund', 'rf-1', 'transition', { to: 'approved' });
    const again = refunds(engine);
    assert.equal(again.length, 1);
    assert.notEqual(again[0]?.refund, first);
    assert.equal((refundFor(engine, 1200, 'Refund:rf-1.1') as { refund: string }).refund, again[0]?.refund);
  });

  test("a refund schema records with its order's permission", () => {
    const engine = openTestEngine();
    publish(engine, orderDocument());
    assert.throws(() => publish(engine, refundDocument('refunds.record')), /record\.permission is refunds\.record/);
  });
});
