// The event log: one event per write, appended in the write's transaction,
// never changed, read in pages from a cursor.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, SqliteError, type Engine, type EngineEvent, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, orderDocument, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };
const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]);

function shape(event: EngineEvent): unknown[] {
  return [event.kind, event.namespace, event.schema, event.instanceId, event.seq, event.version, event.actor];
}

for (const driver of drivers) {
  describe(`event log (${driver})`, () => {
    test('each write appends one event with its namespace, schema, instance, sequence, version, actor, time and change', () => {
      let now = 100;
      const engine = openTestEngine({ driver, clock: () => (now += 1) });
      const defined = engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 1 }, { id: 'o1' });
      engine.instances.update(bob, 'Order', 'o1', { quantity: null, status: 'open' });
      engine.instances.delete(bob, 'Order', 'o1');

      const { events, next, more } = engine.events.read(alice);
      assert.deepEqual(events.map(shape), [
        ['publish', 'default', 'Order', null, null, 1, 'alice'],
        ['create', 'default', 'Order', 'o1', 1, 1, 'alice'],
        ['update', 'default', 'Order', 'o1', 2, 1, 'bob'],
        ['delete', 'default', 'Order', 'o1', 3, 1, 'bob'],
      ]);
      assert.deepEqual(
        events.map((event) => event.change),
        [defined.document, { title: 'Desk', quantity: 1 }, { quantity: null, status: 'open' }, null]
      );
      const cursors = events.map((event) => event.cursor);
      assert.deepEqual([...cursors].sort((a, b) => a - b), cursors);
      assert.equal(new Set(cursors).size, 4);
      assert.deepEqual(events.map((event) => event.at), [103, 104, 105, 106]);
      assert.equal(next, cursors[3]);
      assert.equal(more, false);
    });

    test('a delete keeps the events before it, and a re-created id continues its sequence', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
      engine.instances.delete(alice, 'Order', 'o1');
      engine.instances.create(alice, 'Order', { title: 'Chair' }, { id: 'o1' });
      const history = engine.events.read(alice, { schema: 'Order', instanceId: 'o1' }).events;
      assert.deepEqual(
        history.map((event) => [event.kind, event.seq]),
        [
          ['create', 1],
          ['update', 2],
          ['delete', 3],
          ['create', 4],
        ]
      );
      const later = engine.events.read(alice, { schema: 'Order', instanceId: 'o1', after: history[1].cursor, limit: 1 });
      assert.deepEqual(later.events.map((event) => event.seq), [3]);
      assert.deepEqual([later.next, later.more], [history[2].cursor, true]);
    });

    test('a refused write appends no event', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const before = engine.events.read(alice).events.length;
      assert.throws(() => engine.instances.create(alice, 'Order', { title: 3 }));
      assert.throws(() => engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' }));
      assert.throws(() => engine.instances.update(alice, 'Order', 'o1', { title: null }));
      engine.schemas.define(alice, orderDocument());
      assert.equal(engine.schemas.publish(alice, 'Order').published, false);
      assert.equal(engine.events.read(alice).events.length, before);
    });

    test('the log is append-only', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      for (const sql of ["UPDATE engine_events SET actor = 'mallory'", 'DELETE FROM engine_events']) {
        assert.match(thrown(() => engine.storage.run(sql), SqliteError).message, /engine_events is append-only/);
      }
      assert.equal(engine.events.read(alice).events[0].actor, 'alice');
    });

    test('reading is paged from a cursor', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      for (const id of ['o1', 'o2', 'o3', 'o4']) {
        engine.instances.create(alice, 'Order', { title: id }, { id });
      }
      const first = engine.events.read(alice, { limit: 2 });
      assert.deepEqual(first.events.map((event) => event.instanceId), [null, 'o1']);
      assert.equal(first.more, true);
      const second = engine.events.read(alice, { after: first.next, limit: 2 });
      assert.deepEqual(second.events.map((event) => event.instanceId), ['o2', 'o3']);
      const third = engine.events.read(alice, { after: second.next, limit: 2 });
      assert.deepEqual(third.events.map((event) => event.instanceId), ['o4']);
      assert.equal(third.more, false);
      const end = engine.events.read(alice, { after: third.next });
      assert.deepEqual(end, { events: [], next: third.next, more: false });
      engine.instances.create(alice, 'Order', { title: 'o5' }, { id: 'o5' });
      assert.deepEqual(
        engine.events.read(alice, { after: third.next }).events.map((event) => event.instanceId),
        ['o5']
      );
    });

    test('reads keep to a namespace, and filter by schema and instance', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'common'], shared: 'common' } });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.schemas.define(alice, noteDocument, { namespace: 'common' });
      engine.schemas.publish(alice, 'Note', { namespace: 'common' });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      engine.instances.create(alice, 'Note', { body: 'hello' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { body: 'east' }, { id: 'n1', namespace: 'east' });

      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.kind, event.schema, event.instanceId]),
        [
          ['publish', 'Order', null],
          ['create', 'Order', 'o1'],
          ['create', 'Order', 'o2'],
          ['create', 'Note', 'n1'],
        ]
      );
      // A publish is logged in the namespace that holds the schema.
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'common' }).events.map((event) => [event.kind, event.schema]),
        [['publish', 'Note']]
      );
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'east' }).events.map((event) => [event.kind, event.namespace, event.instanceId]),
        [['create', 'east', 'n1']]
      );
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Order', instanceId: 'o2' }).events.map((event) => event.instanceId),
        ['o2']
      );
    });

    test('a read checks its arguments', () => {
      const engine = openTestEngine({ driver });
      for (const options of [{ after: -1 }, { after: 1.5 }, { limit: 0 }, { limit: 501 }, { instanceId: 'o1' }, { schema: 'not a name' }]) {
        assert.equal(thrown(() => engine.events.read(alice, options), EngineError).code, 'invalid_argument');
      }
      assert.equal(thrown(() => engine.events.read(alice, { namespace: 'east' }), EngineError).code, 'unknown_namespace');
    });
  });
}
