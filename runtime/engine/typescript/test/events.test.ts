// The event log: one event per write, appended in the write's transaction,
// never changed, read in pages from a cursor or from the head, with
// filters by kind, by behavior and by operation name.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, SqliteError, type AccessPolicy, type Engine, type EngineEvent, type Principal } from '../dist/index.js';
import { openBehaviorEngine, publishItem } from './behavior-fixtures.ts';
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
        ['define', 'default', 'Order', null, null, null, 'alice'],
        ['publish', 'default', 'Order', null, null, 1, 'alice'],
        ['create', 'default', 'Order', 'o1', 1, 1, 'alice'],
        ['update', 'default', 'Order', 'o1', 2, 1, 'bob'],
        ['delete', 'default', 'Order', 'o1', 3, 1, 'bob'],
      ]);
      // A define carries the draft's hash, not its document; the publish
      // carries the document it made live.
      assert.deepEqual(
        events.map((event) => event.change),
        [{ hash: defined.hash }, defined.document, { data: { title: 'Desk', quantity: 1 }, behaviors: {} }, { data: { quantity: null, status: 'open' } }, null]
      );
      assert.ok(events.every((event) => !('service' in event)), 'no service made these calls');
      const cursors = events.map((event) => event.cursor);
      assert.deepEqual([...cursors].sort((a, b) => a - b), cursors);
      assert.equal(new Set(cursors).size, 5);
      assert.deepEqual(events.map((event) => event.at), [102, 103, 104, 105, 106]);
      assert.equal(next, cursors[4]);
      assert.equal(more, false);
    });

    test('a define appends an event with the draft hash each time, which a reader of the schema reads', () => {
      // carol may read Order but not define or publish it; dave may define
      // Order and read nothing.
      const policy: AccessPolicy = ({ principal, action }) =>
        principal.subject === 'alice' || (principal.subject === 'carol' && action === 'read') || (principal.subject === 'dave' && action === 'define');
      const engine = openTestEngine({ driver, policy, namespaces: { names: ['default', 'common'], shared: 'common' } });
      const carol: Principal = { subject: 'carol', permissions: [] };
      const dave: Principal = { subject: 'dave', permissions: [] };
      const first = engine.schemas.define(dave, orderDocument());
      const second = engine.schemas.define(dave, { ...orderDocument(), description: 'Orders.' });
      engine.schemas.define(alice, noteDocument, { namespace: 'common' });
      assert.notEqual(first.hash, second.hash);

      const defines = engine.events.read(carol).events;
      assert.deepEqual(
        defines.map((event) => [event.kind, event.schema, event.version, event.actor, event.change]),
        [
          ['define', 'Order', null, 'dave', { hash: first.hash }],
          ['define', 'Order', null, 'dave', { hash: second.hash }],
        ],
        "a draft in the shared namespace changes nothing this namespace reaches until it is published, so its define is the shared namespace's alone"
      );
      assert.equal(engine.schemas.draft(carol, 'Order')?.hash, second.hash, 'the hash names the draft a reader can fetch');
      assert.equal(thrown(() => engine.events.read(dave, { schema: 'Order' }), EngineError).code, 'forbidden');
      assert.deepEqual(engine.events.read(dave).events, [], 'defining is not reading');
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'common' }).events.map((event) => [event.kind, event.schema]),
        [['define', 'Note']]
      );

      // A refused define appends nothing.
      assert.throws(() => engine.schemas.define(dave, { kind: 'General', name: 'Order', types: {} }));
      assert.throws(() => engine.schemas.define(carol, orderDocument()));
      assert.equal(engine.events.read(carol).events.length, 2);
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
      assert.equal(engine.events.read(alice).events.length, before);
      // A draft that matches the live version is still a define; the
      // publish that then mints nothing appends nothing.
      engine.schemas.define(alice, orderDocument());
      assert.equal(engine.events.read(alice).events.length, before + 1);
      assert.equal(engine.schemas.publish(alice, 'Order').published, false);
      assert.equal(engine.events.read(alice).events.length, before + 1);
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
      assert.deepEqual(first.events.map((event) => event.kind), ['define', 'publish']);
      assert.equal(first.more, true);
      const second = engine.events.read(alice, { after: first.next, limit: 2 });
      assert.deepEqual(second.events.map((event) => event.instanceId), ['o1', 'o2']);
      assert.equal(second.more, true);
      const third = engine.events.read(alice, { after: second.next, limit: 2 });
      assert.deepEqual(third.events.map((event) => event.instanceId), ['o3', 'o4']);
      assert.equal(third.more, false);
      const end = engine.events.read(alice, { after: third.next });
      assert.deepEqual(end, { events: [], next: third.next, more: false });
      engine.instances.create(alice, 'Order', { title: 'o5' }, { id: 'o5' });
      assert.deepEqual(
        engine.events.read(alice, { after: third.next }).events.map((event) => event.instanceId),
        ['o5']
      );
    });

    test('reads keep to a namespace and the shared publishes it reaches, and filter by schema and instance', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'common'], shared: 'common' } });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.schemas.define(alice, noteDocument, { namespace: 'common' });
      engine.schemas.publish(alice, 'Note', { namespace: 'common' });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      engine.instances.create(alice, 'Note', { body: 'hello' }, { id: 'n1' });
      engine.instances.create(alice, 'Note', { body: 'east' }, { id: 'n1', namespace: 'east' });

      // A publish is logged in the namespace that holds the schema, and
      // every namespace that looks names up there reads it too.
      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.kind, event.namespace, event.schema, event.instanceId]),
        [
          ['define', 'default', 'Order', null],
          ['publish', 'default', 'Order', null],
          ['publish', 'common', 'Note', null],
          ['create', 'default', 'Order', 'o1'],
          ['create', 'default', 'Order', 'o2'],
          ['create', 'default', 'Note', 'n1'],
        ]
      );
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'common' }).events.map((event) => [event.kind, event.schema]),
        [
          ['define', 'Note'],
          ['publish', 'Note'],
        ]
      );
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'east' }).events.map((event) => [event.kind, event.namespace, event.instanceId]),
        [
          ['publish', 'common', null],
          ['create', 'east', 'n1'],
        ]
      );
      assert.deepEqual(
        engine.events.read(alice, { namespace: 'east', schema: 'Note' }).events.map((event) => [event.kind, event.namespace]),
        [
          ['publish', 'common'],
          ['create', 'east'],
        ]
      );
      assert.deepEqual(engine.events.read(alice, { namespace: 'east', schema: 'Order' }).events, []);
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Order', instanceId: 'o2' }).events.map((event) => event.instanceId),
        ['o2']
      );
    });

    test('a read pages through a namespace and the shared publishes in cursor order', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'common'], shared: 'common' } });
      engine.schemas.define(alice, noteDocument, { namespace: 'common' });
      engine.schemas.publish(alice, 'Note', { namespace: 'common' });
      // The shared namespace's own instances are not the other namespaces' events.
      engine.instances.create(alice, 'Note', { body: 'shared' }, { id: 'c1', namespace: 'common' });
      engine.instances.create(alice, 'Note', { body: 'one' }, { id: 'n1', namespace: 'east' });
      engine.schemas.define(alice, { ...noteDocument, description: 'second' }, { namespace: 'common' });
      engine.schemas.publish(alice, 'Note', { namespace: 'common' });
      engine.instances.create(alice, 'Note', { body: 'two' }, { id: 'n2', namespace: 'east' });
      engine.instances.create(alice, 'Note', { body: 'three' }, { id: 'n3', namespace: 'east' });

      const seen: unknown[] = [];
      let after = 0;
      for (let page = 0; page < 10; page += 1) {
        const read = engine.events.read(alice, { namespace: 'east', after, limit: 2 });
        assert.ok(read.events.length <= 2);
        seen.push(...read.events.map((event) => [event.kind, event.instanceId ?? event.version]));
        after = read.next;
        if (!read.more) {
          break;
        }
      }
      assert.deepEqual(seen, [
        ['publish', 1],
        ['create', 'n1'],
        ['publish', 2],
        ['create', 'n2'],
        ['create', 'n3'],
      ]);
    });

    test('watchers hear of each commit that appended events, after it commits', () => {
      const engine = openTestEngine({ driver });
      const heard: number[] = [];
      let closed = 0;
      const stop = engine.events.watch({ committed: (cursor) => heard.push(cursor), closed: () => (closed += 1) });
      assert.equal(engine.events.watching, 1);

      engine.schemas.define(alice, orderDocument());
      assert.equal(heard.length, 1, 'a draft appends its define event');
      engine.schemas.publish(alice, 'Order');
      const created = engine.storage.transaction(() => {
        const record = engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
        assert.equal(heard.length, 2, 'nothing is heard before the commit');
        return record;
      });
      assert.throws(() =>
        engine.storage.transaction(() => {
          engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
          throw new Error('rolled back');
        })
      );
      assert.throws(() => engine.instances.create(alice, 'Order', { title: 3 }));
      const cursors = engine.events.read(alice).events.map((event) => event.cursor);
      assert.deepEqual(heard, cursors);
      assert.equal(created.seq, 1);

      stop();
      engine.instances.delete(alice, 'Order', 'o1');
      assert.equal(heard.length, 3);
      assert.equal(engine.events.watching, 0);

      engine.events.watch({ committed: () => undefined, closed: () => (closed += 1) });
      engine.close();
      assert.equal(closed, 1, 'the watcher still registered hears the close; the removed one does not');
      assert.throws(() => engine.events.watch({ committed: () => undefined }), /the engine is closed/);
    });

    test('a watcher that throws does not fail the write or silence the others', () => {
      const engine = openTestEngine({ driver });
      const heard: number[] = [];
      engine.events.watch({
        committed: () => {
          throw new Error('a broken watcher');
        },
      });
      engine.events.watch({ committed: (cursor) => heard.push(cursor) });
      engine.schemas.define(alice, orderDocument());
      assert.equal(engine.schemas.publish(alice, 'Order').published, true);
      assert.equal(heard.length, 2);
    });

    test('a read checks its arguments', () => {
      const engine = openTestEngine({ driver });
      for (const options of [
        { after: -1 },
        { after: 1.5 },
        { after: 'tail' as 'head' },
        { limit: 0 },
        { limit: 501 },
        { instanceId: 'o1' },
        { after: 'head' as const, instanceId: 'o1' },
        { schema: 'not a name' },
        { kinds: [] },
        { kinds: ['created' as 'create'] },
        { behaviors: [] },
        { behaviors: ['not a name'] },
        { exclude: ['Heartbeat'] },
        { exclude: 'heartbeat' as unknown as string[] },
      ]) {
        assert.equal(thrown(() => engine.events.read(alice, options), EngineError).code, 'invalid_argument', JSON.stringify(options));
      }
      assert.equal(thrown(() => engine.events.read(alice, { namespace: 'east' }), EngineError).code, 'unknown_namespace');
      assert.deepEqual(engine.events.read(alice, { exclude: [] }), { events: [], next: 0, more: false }, 'an empty exclude drops nothing');
    });

    test('a read from the head returns no events and the cursor of the last one', () => {
      const engine = openTestEngine({ driver, policy: ({ schema }) => schema === 'Order' });
      assert.deepEqual(engine.events.read(alice, { after: 'head' }), { events: [], next: 0, more: false });
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const head = engine.events.head();
      assert.equal(head, engine.events.read(alice).next);
      // The head is the whole log's, whatever the filters keep.
      assert.deepEqual(engine.events.read(alice, { after: 'head', schema: 'Order', kinds: ['delete'] }), { events: [], next: head, more: false });
      // It still asks what any read asks.
      assert.equal(thrown(() => engine.events.read(alice, { after: 'head', schema: 'Note' }), EngineError).code, 'forbidden');

      engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
      assert.deepEqual(
        engine.events.read(alice, { after: head }).events.map((event) => [event.kind, event.instanceId]),
        [['update', 'o1']]
      );
    });

    test('filters keep kinds and behaviors, drop operations by name, and the cursor moves past what they drop', () => {
      const engine = openBehaviorEngine({ driver });
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Tally' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      for (let beat = 0; beat < 3; beat += 1) {
        engine.instances.invoke(alice, 'Item', 'i1', 'increment');
      }
      engine.instances.invoke(alice, 'Item', 'i1', 'bump');
      engine.instances.update(alice, 'Item', 'i1', { title: 'Lamp' });
      const all = engine.events.read(alice).events;
      const label = (event: EngineEvent) => (event.kind === 'operation' ? (event.change as { operation: string }).operation : event.kind);
      assert.deepEqual(all.map(label), ['define', 'publish', 'create', 'increment', 'increment', 'increment', 'bump', 'update']);

      const read = (options: Parameters<Engine['events']['read']>[1]) => engine.events.read(alice, options).events.map(label);
      assert.deepEqual(read({ kinds: ['operation'] }), ['increment', 'increment', 'increment', 'bump']);
      assert.deepEqual(read({ kinds: ['define', 'publish'] }), ['define', 'publish']);
      assert.deepEqual(read({ behaviors: ['test.Tally'] }), ['bump'], 'bump calls increment under its own one event');
      assert.deepEqual(read({ exclude: ['increment'] }), ['define', 'publish', 'create', 'bump', 'update']);
      assert.deepEqual(read({ behaviors: ['test.Counter'], exclude: ['increment'] }), []);
      assert.deepEqual(read({ kinds: ['create', 'update'], behaviors: ['test.Counter'] }), [], 'a behavior filter keeps operations only');
      assert.deepEqual(read({ schema: 'Item', instanceId: 'i1', exclude: ['increment', 'bump'] }), ['create', 'update']);

      // Pages scan their limit whatever the filters keep, and each next is
      // past every event it scanned: a page of dropped events is empty,
      // and reading on from it neither repeats nor skips one.
      const pages: Array<{ kept: string[]; next: number; more: boolean }> = [];
      let after = 0;
      for (;;) {
        const page = engine.events.read(alice, { after, limit: 2, exclude: ['increment'] });
        pages.push({ kept: page.events.map(label), next: page.next, more: page.more });
        after = page.next;
        if (!page.more) {
          break;
        }
      }
      const cursors = all.map((event) => event.cursor);
      assert.deepEqual(pages, [
        { kept: ['define', 'publish'], next: cursors[1], more: true },
        { kept: ['create'], next: cursors[3], more: true },
        { kept: [], next: cursors[5], more: true },
        { kept: ['bump', 'update'], next: cursors[7], more: false },
      ]);
    });
  });
}
