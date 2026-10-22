// Payments: an order owes its totalCents in GBP; openPayment records an
// attempt and the action that creates it at the provider, once per key;
// the adapter lists the actions waiting for it and reports what the
// provider did through recordPaymentEvent, which applies each event once
// and moves an attempt only forward; require holds placed until it is
// paid; a new attempt voids one that failed, a capture that pays what is
// due voids the others, and what a late capture brings past what is due
// is overpaid and asked back; refunds draw on what is captured. Real
// SQLite, the engine's Rust behaviors, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError, type Engine } from '@superschematic/engine';

import type { PaymentAction, PaymentsRecord } from '../dist/index.js';
import { Clock, alice, checkoutFlow, cleanup, document, field, openTestEngine, principal, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const PROVIDER = 'acmepay';
const adapter = principal('adapter', ['payments.record']);
const refunder = principal('rita', ['payments.refund']);

/** An engine with Order: a title and a totalCents, its Workflow a checkout's, owing totalCents in GBP. */
function opened(): Engine {
  const clock = new Clock();
  const engine = openTestEngine({ clock: clock.now });
  publish(
    engine,
    document(
      'Order',
      [field('title', 'string'), field('totalCents', 'Generic.Int64')],
      [
        { name: 'Workflow', config: checkoutFlow },
        {
          name: 'Payments',
          config: {
            amount: { field: 'totalCents' },
            currency: 'GBP',
            require: { placed: 'paid' },
            voidOn: ['expired', 'cancelled'],
            record: { permission: 'payments.record' },
            refunds: { permission: 'payments.refund' },
          },
        },
      ]
    )
  );
  return engine;
}

function order(engine: Engine, id: string, total: number): void {
  engine.instances.create(alice, 'Order', { title: id, totalCents: total }, { id });
}

function pay(engine: Engine, id: string, key: string): { attempt: string; amount: number; currency: string; status: string } {
  return engine.instances.invoke(alice, 'Order', id, 'openPayment', { provider: PROVIDER, idempotencyKey: key, returnUrl: 'https://shop.test/back' }) as {
    attempt: string;
    amount: number;
    currency: string;
    status: string;
  };
}

function report(engine: Engine, id: string, eventId: string, kind: string, amount: number, more: Record<string, unknown>): { applied: boolean; state: string } {
  return engine.instances.invoke(adapter, 'Order', id, 'recordPaymentEvent', { provider: PROVIDER, eventId, kind, amount, currency: 'GBP', at: 1_792_573_200_000, ...more }) as {
    applied: boolean;
    state: string;
  };
}

function paymentsOf(engine: Engine, id: string): PaymentsRecord {
  return engine.instances.get(alice, 'Order', id)?.behaviors.Payments as unknown as PaymentsRecord;
}

function waiting(engine: Engine): PaymentAction[] {
  return (engine.instances.invokeSchema(adapter, 'Order', 'listPaymentActions', { provider: PROVIDER }) as { items: PaymentAction[] }).items;
}

function vetoOf(fn: () => unknown): string | undefined {
  return thrown(fn, BehaviorVetoError).vetoCode;
}

describe('Payments', () => {
  test('an attempt opens once per key, its create action waits for the adapter, and a provider event applies once', () => {
    const engine = opened();
    order(engine, 'o1', 2500);
    assert.deepEqual(paymentsOf(engine, 'o1'), { due: 2500, authorized: 0, captured: 0, refunded: 0, outstanding: 2500, overpaid: 0, currency: 'GBP', state: 'unpaid', attempts: [] });
    const opened1 = pay(engine, 'o1', 'k1');
    assert.deepEqual([opened1.amount, opened1.currency, opened1.status], [2500, 'GBP', 'open']);
    assert.deepEqual(pay(engine, 'o1', 'k1'), opened1);
    assert.equal(vetoOf(() => engine.instances.invoke(alice, 'Order', 'o1', 'openPayment', { provider: 'other', idempotencyKey: 'k1' })), 'key_reused');
    assert.equal(vetoOf(() => pay(engine, 'o1', 'k2')), 'nothing_due');
    assert.deepEqual(
      waiting(engine).map((action) => [action.kind, action.attempt, action.amount, action.returnUrl]),
      [['create', opened1.attempt, 2500, 'https://shop.test/back']]
    );
    assert.equal(thrown(() => engine.instances.invokeSchema(alice, 'Order', 'listPaymentActions', { provider: PROVIDER }), EngineError).code, 'forbidden');

    // Placed waits for the money.
    assert.equal(vetoOf(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'placed' })), 'unpaid');
    assert.equal(report(engine, 'o1', 'e1', 'processing', 2500, { attempt: opened1.attempt, providerRef: 'pi_1' }).applied, true);
    assert.deepEqual(waiting(engine), []);
    // No move while the payment is processing.
    assert.equal(vetoOf(() => engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'cancelled' })), 'payment_in_progress');
    const captured = report(engine, 'o1', 'e2', 'captured', 2500, { providerRef: 'pi_1', brand: 'visa', last4: '4242' });
    assert.deepEqual(captured, { ...captured, applied: true, state: 'paid' });
    // A repeat changes nothing; the same id with other content is key_reused; a late event moves nothing back.
    assert.equal(report(engine, 'o1', 'e2', 'captured', 2500, { providerRef: 'pi_1' }).applied, false);
    assert.equal(vetoOf(() => report(engine, 'o1', 'e2', 'captured', 100, { providerRef: 'pi_1' })), 'key_reused');
    assert.equal(report(engine, 'o1', 'e3', 'failed', 2500, { attempt: opened1.attempt }).applied, false);
    assert.equal(vetoOf(() => report(engine, 'o1', 'e4', 'captured', 1, { attempt: opened1.attempt })), 'over_capture');
    assert.equal(vetoOf(() => report(engine, 'o1', 'e5', 'captured', 1, { attempt: opened1.attempt, currency: 'EUR' })), 'currency_mismatch');
    const fields = paymentsOf(engine, 'o1');
    assert.deepEqual([fields.captured, fields.outstanding, fields.state, fields.attempts[0]?.last4], [2500, 0, 'paid', '4242']);
    engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'held' });
    engine.instances.invoke(alice, 'Order', 'o1', 'transition', { to: 'placed' });
  });

  test('a new attempt voids one that failed, and what a late capture brings past what is due is overpaid and asked back', () => {
    const engine = opened();
    order(engine, 'o1', 2500);
    const first = pay(engine, 'o1', 'k1').attempt;
    report(engine, 'o1', 'e1', 'failed', 2500, { attempt: first, providerRef: 'pi_1' });
    const second = pay(engine, 'o1', 'k2').attempt;
    assert.deepEqual(
      waiting(engine).map((action) => [action.kind, action.attempt, action.amount]),
      [
        ['create', second, 2500],
        ['void', first, 2500],
      ]
    );
    report(engine, 'o1', 'e2', 'processing', 2500, { attempt: second, providerRef: 'pi_2' });
    // The provider finishes the first payment anyway: it pays what is due.
    assert.equal(report(engine, 'o1', 'e3', 'captured', 2500, { attempt: first }).state, 'paid');
    assert.equal(paymentsOf(engine, 'o1').overpaid, 0);
    // The second captures too: the excess is overpaid, asked back, and the order stays paid.
    assert.equal(report(engine, 'o1', 'e4', 'captured', 2500, { attempt: second }).state, 'paid');
    const refunds = waiting(engine).filter((action) => action.kind === 'refund');
    assert.deepEqual(
      refunds.map((action) => action.amount),
      [2500]
    );
    let fields = paymentsOf(engine, 'o1');
    assert.deepEqual([fields.captured, fields.overpaid, fields.outstanding, fields.state], [5000, 2500, 0, 'paid']);
    report(engine, 'o1', 'e5', 'refunded', 2500, { refund: refunds[0]?.refund });
    fields = paymentsOf(engine, 'o1');
    assert.deepEqual([fields.refunded, fields.overpaid, fields.state], [2500, 2500, 'paid']);
    assert.equal(
      thrown(() => engine.instances.invoke(refunder, 'Order', 'o1', 'refundPayment', { amount: 1, reason: 'x', idempotencyKey: 'overpaid:e9' }), EngineError).code,
      'invalid_argument'
    );
  });

  test('a capture that pays what is due voids the other open attempts', () => {
    const engine = opened();
    order(engine, 'o1', 1000);
    const first = pay(engine, 'o1', 'k1').attempt;
    report(engine, 'o1', 'e1', 'failed', 1000, { attempt: first });
    const second = pay(engine, 'o1', 'k2').attempt;
    report(engine, 'o1', 'e2', 'captured', 1000, { attempt: first });
    assert.deepEqual(
      waiting(engine)
        .filter((action) => action.kind === 'void')
        .map((action) => action.attempt),
      [second]
    );
    report(engine, 'o1', 'e3', 'cancelled', 1000, { attempt: second });
    assert.deepEqual(waiting(engine), []);
    assert.deepEqual([paymentsOf(engine, 'o1').state, paymentsOf(engine, 'o1').overpaid], ['paid', 0]);
  });

  test('a refund draws on what is captured, needs its permission, and is once per key', () => {
    const engine = opened();
    order(engine, 'o1', 3000);
    const attempt = pay(engine, 'o1', 'k1').attempt;
    report(engine, 'o1', 'e1', 'captured', 3000, { attempt });
    assert.equal(thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'refundPayment', { amount: 1000, reason: 'damaged', idempotencyKey: 'r1' }), EngineError).code, 'forbidden');
    const refund = engine.instances.invoke(refunder, 'Order', 'o1', 'refundPayment', { amount: 1000, reason: 'damaged', idempotencyKey: 'r1' }) as { refund: string; status: string };
    assert.equal(refund.status, 'requested');
    assert.deepEqual(engine.instances.invoke(refunder, 'Order', 'o1', 'refundPayment', { amount: 1000, reason: 'damaged', idempotencyKey: 'r1' }), refund);
    const refused = thrown(() => engine.instances.invoke(refunder, 'Order', 'o1', 'refundPayment', { amount: 2500, reason: 'all', idempotencyKey: 'r2' }), BehaviorVetoError);
    assert.deepEqual([refused.vetoCode, refused.vetoDetails?.refundable], ['exceeds_refundable', 2000]);
    report(engine, 'o1', 'e2', 'refunded', 1000, { refund: refund.refund });
    assert.deepEqual([paymentsOf(engine, 'o1').refunded, paymentsOf(engine, 'o1').state], [1000, 'partiallyRefunded']);
  });
});
