// Derived, the core's own fields a model outside the engine fills (D73):
// a deriver pulls the stale fields and settles values by hash, which land
// as own fields attributed to Derived (D16, amended); a write by anyone
// else overrides a field until rederive; failures and refused values count
// attempts and append no event; and the config's permission.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const deriver: Principal = { subject: 'deriver', permissions: ['tickets.derive'] };

const derived = {
  fields: {
    category: { from: ['subject', 'body'], instruction: 'Classify the request.', model: 'standard' },
    summary: { from: ['body'], instruction: 'Summarize it in one sentence.', model: 'simple' },
  },
  permission: 'tickets.derive',
  maxAttempts: 2,
};

function ticketSchema(): Record<string, unknown> {
  const document = schemaDocument('Ticket', [
    { name: 'subject', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
    { name: 'category', typeRef: { name: 'Category' } },
    { name: 'summary', typeRef: { name: 'string' } },
  ]) as { types: { Ticket: Record<string, unknown> }; enums?: unknown };
  document.enums = { Category: { name: 'Category', values: [{ name: 'billing' }, { name: 'bug' }, { name: 'other' }] } };
  document.types.Ticket.behaviors = [{ name: 'Derived', config: derived }];
  return document;
}

type Item = { id: string; field: string; sourceHash: string; attempt: number; inputs: Record<string, unknown>; output: unknown };

for (const driver of drivers) {
  function opened(): Engine {
    const engine = openTestEngine({ driver });
    engine.schemas.define(alice, ticketSchema());
    engine.schemas.publish(alice, 'Ticket');
    engine.instances.create(alice, 'Ticket', { subject: 'Card declined', body: 'Refused at checkout.' }, { id: 't1' });
    return engine;
  }

  const stale = (engine: Engine) => (engine.instances.invokeSchema(deriver, 'Ticket', 'staleDerivations', {}) as { items: Item[] }).items;
  const hashOf = (items: Item[], field: string) => items.find((item) => item.field === field)?.sourceHash;
  const settle = (engine: Engine, items: unknown[]) => engine.instances.invokeSchema(deriver, 'Ticket', 'settleDerivations', { items });
  const derivedOf = (engine: Engine) => engine.instances.get(alice, 'Ticket', 't1')?.behaviors.Derived as { fields: Record<string, Record<string, unknown>>; stale: string[] };

  describe(`Derived (${driver})`, () => {
    test('a deriver pulls the stale fields and settles values that land as own fields, attributed to Derived', () => {
      const engine = opened();
      const items = stale(engine);
      assert.deepEqual(items.map((item) => [item.field, item.attempt]), [['category', 1], ['summary', 1]]);
      assert.deepEqual(items[0]?.inputs, { subject: 'Card declined', body: 'Refused at checkout.' });
      assert.deepEqual(
        settle(engine, [
          { id: 't1', field: 'category', sourceHash: hashOf(items, 'category'), value: 'billing', confidence: 0.9 },
          { id: 't1', field: 'summary', sourceHash: hashOf(items, 'summary'), value: 'A card is refused.' },
        ]),
        { settled: 2, failed: 0, skipped: [] }
      );
      const instance = engine.instances.get(alice, 'Ticket', 't1');
      assert.equal(instance?.data.category, 'billing');
      assert.equal(instance?.seq, 2);
      assert.equal(derivedOf(engine).fields.category?.confidence, 0.9);
      assert.deepEqual(derivedOf(engine).stale, []);
      const change = engine.events.read(alice, { schema: 'Ticket', instanceId: 't1' }).events.at(-1)?.change as Record<string, unknown>;
      assert.deepEqual(change.attribution, { category: { behavior: 'Derived', confidence: 0.9 }, summary: { behavior: 'Derived' } });
    });

    test("a caller's write overrides the field until rederive, and a settle of an old hash is skipped", () => {
      const engine = opened();
      const items = stale(engine);
      engine.instances.update(alice, 'Ticket', 't1', { category: 'other', body: 'Refused twice.' });
      assert.equal((derivedOf(engine).fields.category?.overridden as { by: string }).by, 'alice');
      assert.deepEqual(derivedOf(engine).stale, ['summary']);
      const skipped = settle(engine, [
        { id: 't1', field: 'category', sourceHash: hashOf(items, 'category'), value: 'bug' },
        { id: 't1', field: 'summary', sourceHash: hashOf(items, 'summary'), value: 'Old.' },
      ]) as { skipped: Array<{ reason: string }> };
      assert.deepEqual(skipped.skipped.map((skip) => skip.reason), ['overridden', 'moved']);
      const veto = thrown(
        () => engine.instances.invoke(deriver, 'Ticket', 't1', 'applyDerived', { fields: { category: { sourceHash: hashOf(items, 'category'), value: 'bug' } } }),
        BehaviorVetoError
      );
      assert.equal(veto.vetoCode, 'overridden');
      assert.deepEqual(engine.instances.invoke(alice, 'Ticket', 't1', 'rederive', { fields: ['category'] }), { stale: ['category', 'summary'] });
      assert.equal(stale(engine).length, 2);
    });

    test('a refused value and a failure count attempts, append no event, and run out into failedDerivations', () => {
      const engine = opened();
      const category = hashOf(stale(engine), 'category');
      const invalid = settle(engine, [{ id: 't1', field: 'category', sourceHash: category, value: 'payments' }]) as { skipped: Array<{ reason: string }> };
      assert.equal(invalid.skipped[0]?.reason, 'invalid');
      settle(engine, [{ id: 't1', field: 'category', sourceHash: category, failure: { class: 'timeout', message: 'too slow' } }]);
      assert.equal(engine.instances.get(alice, 'Ticket', 't1')?.seq, 1);
      assert.deepEqual(stale(engine).map((item) => item.field), ['summary']);
      assert.deepEqual((engine.instances.invokeSchema(deriver, 'Ticket', 'failedDerivations', {}) as { items: unknown[] }).items, [
        { id: 't1', field: 'category', attempts: 2, failure: { class: 'timeout', message: 'too slow' } },
      ]);
    });

    test("a deriver needs the config's permission", () => {
      const engine = opened();
      for (const operation of ['staleDerivations', 'failedDerivations']) {
        assert.equal(thrown(() => engine.instances.invokeSchema(alice, 'Ticket', operation, {}), EngineError).code, 'forbidden');
      }
      assert.equal(thrown(() => engine.instances.invokeSchema(alice, 'Ticket', 'settleDerivations', { items: [] }), EngineError).code, 'forbidden');
    });
  });
}
