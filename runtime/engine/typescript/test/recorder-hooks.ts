// The tests recorder.test.ts records, with ENGINE_RECORD set, to hold the
// recorder to the scenarios README's rule for hooks: test.Ledger's
// reactions, which run a closure the test puts in runner-fixtures.ts's
// probe, with one set and run, with none set, and with one set that no
// runner pass runs; and test.Gate's guard, which reads a value hook the
// test closes and opens again. Not a test file of the suite: its name does
// not end in .test.ts, so neither Node.js's runner nor Bun's runs it alone.
import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import { BehaviorVetoError, type Engine } from '../dist/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, openTestEngine } from './helpers.ts';
import { testHooks } from './recorder.ts';
import { ledgerDocument, mark, openRunnerEngine, probe, publish, resetProbe, testClock } from './runner-fixtures.ts';

/** Whether test.Gate refuses updates; the test closes it. */
const gate = testHooks('gate', { closed: false });

// test.Gate refuses every update while the test has closed it.
const gated = defineBehavior({
  declaration: { name: 'test.Gate' },
  guard(_view, request) {
    return request.kind === 'update' && gate.closed ? 'the gate is closed' : undefined;
  },
});

afterEach((t) => {
  resetProbe();
  gate.closed = false;
  cleanup(t);
});

function ledgerWithOrder(): Engine {
  const engine = openRunnerEngine({ clock: testClock() });
  publish(engine, ledgerDocument('Order'));
  // The first pass finds the reactions' subscription.
  engine.runner.runDue();
  return engine;
}

test('a reaction runs a closure the test set', () => {
  const engine = ledgerWithOrder();
  probe.react = (context, event) => {
    if (event.cause === undefined) {
      mark(context, event);
    }
  };
  engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
  assert.equal(engine.runner.runDue().handled, 2);
  assert.equal((engine.instances.get(alice, 'Order', 'o1')?.behaviors['test.Ledger']?.notes as string[]).length, 1);
});

test('a reaction with no closure set', () => {
  const engine = ledgerWithOrder();
  engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
  assert.equal(engine.runner.runDue().handled, 1);
  assert.equal(engine.instances.get(alice, 'Order', 'o1')?.behaviors['test.Ledger']?.notes, undefined);
});

test('a closure the test set that no runner pass runs', () => {
  const engine = ledgerWithOrder();
  probe.react = (context, event) => mark(context, event);
  engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
});

test('a guard that reads a value hook the test closes and opens again', () => {
  const engine = openTestEngine({ clock: () => 1000, metaSchema: openMetaSchema(), behaviors: [gated] });
  publishItem(engine, [{ name: 'test.Gate' }]);
  engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i1' });
  engine.instances.update(alice, 'Item', 'i1', { title: 'Desk' });
  gate.closed = true;
  assert.throws(() => engine.instances.update(alice, 'Item', 'i1', { title: 'Chair' }), BehaviorVetoError);
  gate.closed = false;
  engine.instances.update(alice, 'Item', 'i1', { title: 'Chair' });
});
