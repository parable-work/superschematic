// A writing operation whose handler says it changed nothing
// (OperationContext.unchanged) writes nothing: no event, no seq, no
// updatedAt, no afterChange, as an update whose patch changes nothing.
// The engine holds the claim to the rows the call wrote.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorError, defineBehavior, type Engine, type FrozenJSON, type InstanceChange } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);

const onParams = { type: 'object', additionalProperties: false, required: ['on'], properties: { on: { type: 'boolean' } } } as const;
const titleParams = { type: 'object', additionalProperties: false, required: ['title'], properties: { title: { type: 'string' } } } as const;
const noParams = { type: 'object', additionalProperties: false } as const;

// What afterChange saw, per test.
const changes: InstanceChange[] = [];

// test.Toggle holds a switch. set says it changed nothing when the switch
// is already where it is asked to be; the others say so when they wrote.
const toggle = defineBehavior({
  declaration: {
    name: 'test.Toggle',
    fields: [{ name: 'on' }],
    operations: [
      { name: 'set', paramsSchema: onParams, resultSchema: true, writes: true },
      { name: 'setAndClaim', paramsSchema: onParams, resultSchema: true, writes: true },
      { name: 'retitleQuietly', paramsSchema: titleParams, resultSchema: true, writes: true },
      { name: 'relay', paramsSchema: onParams, resultSchema: true, writes: true },
      { name: 'quietRelay', paramsSchema: onParams, resultSchema: true, writes: true },
      { name: 'peek', paramsSchema: noParams, resultSchema: true },
    ],
  },
  migrations: [{ version: 1, name: 'on', columns: { on: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    set(context, params) {
      const on = params.on === true;
      if ((Number(context.columns.get().on) === 1) === on) {
        context.unchanged();
        return { on, changed: false };
      }
      context.columns.set({ on: on ? 1 : 0 });
      return { on, changed: true };
    },
    setAndClaim(context, params) {
      context.columns.set({ on: params.on === true ? 1 : 0 });
      context.unchanged();
      return {};
    },
    retitleQuietly(context, params) {
      context.update({ title: params.title });
      context.unchanged();
      return context.data.title as FrozenJSON;
    },
    relay(context, params) {
      return context.call('test.Toggle', 'set', { on: params.on }) as FrozenJSON;
    },
    quietRelay(context, params) {
      const result = context.call('test.Toggle', 'set', { on: params.on }) as FrozenJSON;
      context.unchanged();
      return result;
    },
    peek(context) {
      context.unchanged();
      return Number(context.columns.get().on) === 1;
    },
  },
  fields: { on: (view) => Number(view.columns.get().on) === 1 },
  afterChange(_context, change) {
    changes.push(change);
  },
});

for (const driver of drivers) {
  describe(`an operation that changes nothing (${driver})`, () => {
    let now = 1_000;
    function open(): Engine {
      changes.length = 0;
      now = 1_000;
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [toggle], clock: () => now });
      publishItem(engine, [{ name: 'test.Toggle' }]);
      engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'a' });
      changes.length = 0;
      return engine;
    }

    function events(engine: Engine): number {
      return engine.events.read(alice, { schema: 'Item', instanceId: 'a' }).events.length;
    }

    test('appends no event and keeps seq, updatedAt and updatedBy, and no afterChange runs', () => {
      const engine = open();
      now = 2_000;
      assert.deepEqual(engine.instances.operate(alice, 'Item', 'a', 'set', { on: true }), { result: { on: true, changed: true }, seq: 2 });
      assert.equal(changes.length, 1);
      now = 3_000;
      const bob = { subject: 'bob', permissions: [] };
      assert.deepEqual(engine.instances.operate(bob, 'Item', 'a', 'set', { on: true }), { result: { on: true, changed: false }, seq: 2 });
      const instance = engine.instances.get(alice, 'Item', 'a');
      assert.equal(instance?.seq, 2);
      assert.equal(instance?.updatedAt, 2_000);
      assert.equal(instance?.updatedBy, 'alice');
      assert.equal(events(engine), 2);
      assert.equal(changes.length, 1);
    });

    test('a stale expectedSeq is still refused, before the handler', () => {
      const engine = open();
      engine.instances.invoke(alice, 'Item', 'a', 'set', { on: true });
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'set', { on: true }, { expectedSeq: 1 }), Error).message.includes('sequence'), true);
    });

    test('a call that wrote and says it changed nothing is a BehaviorError, and rolls back', () => {
      const engine = open();
      const error = thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'setAndClaim', { on: true }), BehaviorError);
      assert.match(error.message, /said it changed nothing/);
      assert.equal(engine.instances.get(alice, 'Item', 'a')?.behaviors['test.Toggle']?.on, false);
      assert.equal(events(engine), 1);
    });

    test("an update() that changed the own fields counts as a write; one that changed nothing does not", () => {
      const engine = open();
      thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'retitleQuietly', { title: 'Desk lamp' }), BehaviorError);
      assert.equal(engine.instances.get(alice, 'Item', 'a')?.data.title, 'Lamp');
      assert.equal(engine.instances.invoke(alice, 'Item', 'a', 'retitleQuietly', { title: 'Lamp' }), 'Lamp');
      assert.equal(events(engine), 1);
    });

    test('a called operation that changed nothing leaves its caller to say so', () => {
      const engine = open();
      // relay does not say it changed nothing, so its event is appended.
      engine.instances.invoke(alice, 'Item', 'a', 'relay', { on: false });
      assert.equal(events(engine), 2);
      assert.equal(engine.instances.get(alice, 'Item', 'a')?.seq, 2);
      // quietRelay says so, and the call it made wrote nothing.
      assert.deepEqual(engine.instances.operate(alice, 'Item', 'a', 'quietRelay', { on: false }), { result: { on: false, changed: false }, seq: 2 });
      assert.equal(events(engine), 2);
      // Where the call it made wrote, the claim is refused.
      thrown(() => engine.instances.invoke(alice, 'Item', 'a', 'quietRelay', { on: true }), BehaviorError);
      assert.equal(engine.instances.get(alice, 'Item', 'a')?.behaviors['test.Toggle']?.on, false);
    });

    test('a read-only operation that says so reads as before', () => {
      const engine = open();
      assert.deepEqual(engine.instances.operate(alice, 'Item', 'a', 'peek', {}), { result: false, seq: 1 });
    });

    test('over HTTP the ETag stays where it was', async () => {
      const engine = open();
      const app = engineApp(engine, { authenticate: async () => alice });
      const post = (on: boolean) =>
        app.request('/namespaces/default/schemas/Item/instances/a/operations/set', {
          method: 'POST',
          headers: { 'content-type': 'application/json', authorization: 'Bearer t' },
          body: JSON.stringify({ on }),
        });
      assert.equal((await post(true)).headers.get('etag'), '"2"');
      assert.equal((await post(true)).headers.get('etag'), '"2"');
    });
  });
}
