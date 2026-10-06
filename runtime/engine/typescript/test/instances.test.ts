// Instances: create, get, list, update and delete on a schema's instance
// type, keyed by namespace, schema and id, validated by the live version.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, IncompatibleChangeError, InstanceValidationError, allowAll, openEngine, type Engine, type EngineOptions, type Principal } from '../dist/index.js';
import { alice, cleanup, clone, drivers, freshPath, openTestEngine, orderDocument, schemaDocument, thrown, track } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

const shipmentDocument = schemaDocument(
  'Shipment',
  [
    { name: 'carrier', typeRef: { name: 'string' }, required: true },
    { name: 'address', typeRef: { name: 'Address' } },
    { name: 'parcels', typeRef: { name: 'number', isArray: true } },
  ],
  {
    types: {
      Address: {
        name: 'Address',
        role: 'EmbeddedStruct',
        fields: [
          { name: 'street', typeRef: { name: 'string' } },
          { name: 'city', typeRef: { name: 'string' }, required: true },
        ],
      },
    },
  }
);

function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
  const record = engine.schemas.define(alice, document, { namespace });
  engine.schemas.publish(alice, record.name, { namespace });
}

function withOrders(options: Partial<EngineOptions> = {}): Engine {
  const engine = openTestEngine(options);
  publish(engine, orderDocument());
  return engine;
}

function eventCount(engine: Engine): number {
  return Number(engine.storage.get('SELECT COUNT(*) AS n FROM engine_events')?.n);
}

for (const driver of drivers) {
  describe(`instances (${driver})`, () => {
    test('create validates an instance, stores it with the live version and reads it back', () => {
      const engine = withOrders({ driver, clock: () => 5000 });
      const created = engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 2 });
      assert.match(created.id, UUID);
      assert.deepEqual(created, {
        namespace: 'default',
        schema: 'Order',
        id: created.id,
        schemaNamespace: 'default',
        version: 1,
        seq: 1,
        data: { title: 'Desk', quantity: 2 },
        createdAt: 5000,
        createdBy: 'alice',
        updatedAt: 5000,
        updatedBy: 'alice',
      });
      assert.deepEqual(engine.instances.get(alice, 'Order', created.id), created);
    });

    test('ids come from the id generator, or the caller', () => {
      let next = 0;
      const engine = withOrders({ driver, ids: () => `order-${(next += 1)}` });
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Desk' }).id, 'order-1');
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Lamp' }).id, 'order-2');
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Chair' }, { id: 'chair' }).id, 'chair');
      assert.equal(engine.instances.get(alice, 'Order', 'chair')?.data.title, 'Chair');
    });

    test('an id is checked, whoever made it', () => {
      const engine = withOrders({ driver, ids: () => 'not an id' });
      for (const id of ['', 'has space', '-leading', 'x'.repeat(257)]) {
        assert.equal(thrown(() => engine.instances.create(alice, 'Order', { title: 'Desk' }, { id }), EngineError).code, 'invalid_argument');
      }
      assert.match(thrown(() => engine.instances.create(alice, 'Order', { title: 'Desk' }), EngineError).message, /instance id "not an id"/);
    });

    test('a duplicate id in a namespace is a conflict and writes nothing', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'fixed' });
      const error = thrown(() => engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'fixed' }), EngineError);
      assert.equal(error.code, 'conflict');
      assert.equal(engine.instances.get(alice, 'Order', 'fixed')?.data.title, 'Desk');
      assert.equal(eventCount(engine), 3); // the define, the publish and the first create
    });

    test('an id in another namespace or schema is another instance', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east'] } });
      publish(engine, orderDocument());
      publish(engine, orderDocument(), 'east');
      publish(engine, shipmentDocument);
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const east = engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o1', namespace: 'east' });
      assert.equal(east.seq, 1);
      engine.instances.create(alice, 'Shipment', { carrier: 'Post' }, { id: 'o1' });
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.data.title, 'Desk');
      assert.equal(engine.instances.get(alice, 'Order', 'o1', { namespace: 'east' })?.data.title, 'Lamp');
      engine.instances.delete(alice, 'Order', 'o1');
      assert.equal(engine.instances.get(alice, 'Order', 'o1'), undefined);
      assert.equal(engine.instances.get(alice, 'Order', 'o1', { namespace: 'east' })?.data.title, 'Lamp');
    });

    test('get of a missing id is undefined', () => {
      const engine = withOrders({ driver });
      assert.equal(engine.instances.get(alice, 'Order', 'missing'), undefined);
    });

    test('a schema without a live version is not_found for every operation', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, orderDocument());
      for (const schema of ['Order', 'Note']) {
        const calls = [
          () => engine.instances.create(alice, schema, { title: 'Desk' }),
          () => engine.instances.get(alice, schema, 'o1'),
          () => engine.instances.list(alice, schema),
          () => engine.instances.update(alice, schema, 'o1', { title: 'Lamp' }),
          () => engine.instances.delete(alice, schema, 'o1'),
        ];
        for (const call of calls) {
          const error = thrown(call, EngineError);
          assert.equal(error.code, 'not_found');
          assert.match(error.message, new RegExp(`schema ${schema} has no live version in namespace default`));
        }
      }
    });

    test('a refused instance names each issue and writes nothing', () => {
      const engine = withOrders({ driver });
      const error = thrown(() => engine.instances.create(alice, 'Order', { quantity: 'two', colour: 'red' }), InstanceValidationError);
      assert.equal(error.code, 'invalid_instance');
      assert.deepEqual(
        error.issues.map((issue) => [issue.path, issue.rule]),
        [
          ['colour', 'unknown'],
          ['title', 'required'],
          ['quantity', 'type'],
        ]
      );
      assert.deepEqual(engine.instances.list(alice, 'Order').items, []);
      assert.equal(eventCount(engine), 2);
    });

    test('update merges a patch, validates the result and records who and when', () => {
      let now = 1000;
      const engine = withOrders({ driver, clock: () => now });
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 1 }, { id: 'o1' });
      now = 2000;
      const updated = engine.instances.update(bob, 'Order', 'o1', { quantity: 3, status: 'shipped' });
      assert.deepEqual(updated.data, { title: 'Desk', quantity: 3, status: 'shipped' });
      assert.equal(updated.seq, 2);
      assert.deepEqual(
        [updated.createdAt, updated.createdBy, updated.updatedAt, updated.updatedBy],
        [1000, 'alice', 2000, 'bob']
      );
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1'), updated);
    });

    test('null in a patch removes an optional field', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 3 }, { id: 'o1' });
      assert.deepEqual(engine.instances.update(alice, 'Order', 'o1', { quantity: null }).data, { title: 'Desk' });
    });

    test('null on a required field is refused and writes nothing', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const error = thrown(() => engine.instances.update(alice, 'Order', 'o1', { title: null }), InstanceValidationError);
      assert.deepEqual(error.issues.map((issue) => issue.path), ['title']);
      const stored = engine.instances.get(alice, 'Order', 'o1');
      assert.deepEqual([stored?.data, stored?.seq], [{ title: 'Desk' }, 1]);
    });

    test('a nested object merges and a list is replaced', () => {
      const engine = openTestEngine({ driver });
      publish(engine, shipmentDocument);
      engine.instances.create(alice, 'Shipment', { carrier: 'Post', address: { street: '1 Main St', city: 'Oslo' }, parcels: [1, 2] }, { id: 's1' });
      const updated = engine.instances.update(alice, 'Shipment', 's1', { address: { city: 'Bergen' }, parcels: [3] });
      assert.deepEqual(updated.data, { carrier: 'Post', address: { street: '1 Main St', city: 'Bergen' }, parcels: [3] });
      const cleared = engine.instances.update(alice, 'Shipment', 's1', { address: { street: null } });
      assert.deepEqual(cleared.data.address, { city: 'Bergen' });
    });

    test('a patch that changes nothing writes nothing', () => {
      let now = 1000;
      const engine = withOrders({ driver, clock: () => now });
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 3 }, { id: 'o1' });
      now = 2000;
      const same = engine.instances.update(bob, 'Order', 'o1', { quantity: 3, status: undefined });
      assert.deepEqual([same.seq, same.updatedAt, same.updatedBy], [1, 1000, 'alice']);
      assert.equal(eventCount(engine), 3);
    });

    test('a patch is a JSON object, and the instance must exist', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      for (const patch of [['title'], 'Lamp', null]) {
        assert.equal(thrown(() => engine.instances.update(alice, 'Order', 'o1', patch), EngineError).code, 'invalid_argument');
      }
      const missing = thrown(() => engine.instances.update(alice, 'Order', 'o2', { title: 'Lamp' }), EngineError);
      assert.equal(missing.code, 'not_found');
      assert.match(missing.message, /Order o2 does not exist in namespace default/);
    });

    test('delete removes the instance; a second delete finds nothing', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(engine.instances.delete(alice, 'Order', 'o1'), true);
      assert.equal(engine.instances.get(alice, 'Order', 'o1'), undefined);
      assert.equal(engine.instances.delete(alice, 'Order', 'o1'), false);
      assert.equal(eventCount(engine), 4);
    });

    test('update and delete with an expected sequence write only while the instance is at it', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, { expectedSeq: 1 }).seq, 2);

      const stale = thrown(() => engine.instances.update(alice, 'Order', 'o1', { title: 'Chair' }, { expectedSeq: 1 }), EngineError);
      assert.equal(stale.code, 'seq_mismatch');
      assert.match(stale.message, /Order o1 is no longer at sequence 1/);
      // A precondition is checked before the patch: an invalid patch at a
      // stale sequence is still seq_mismatch.
      assert.equal(thrown(() => engine.instances.update(alice, 'Order', 'o1', { title: null }, { expectedSeq: 1 }), EngineError).code, 'seq_mismatch');
      assert.equal(thrown(() => engine.instances.delete(alice, 'Order', 'o1', { expectedSeq: 1 }), EngineError).code, 'seq_mismatch');
      // 0 matches no instance: sequences start at 1.
      assert.equal(thrown(() => engine.instances.delete(alice, 'Order', 'o1', { expectedSeq: 0 }), EngineError).code, 'seq_mismatch');
      assert.deepEqual([engine.instances.get(alice, 'Order', 'o1')?.data, eventCount(engine)], [{ title: 'Lamp' }, 4]);

      // A patch that changes nothing still needs the sequence.
      assert.equal(engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, { expectedSeq: 2 }).seq, 2);
      assert.equal(engine.instances.delete(alice, 'Order', 'o1', { expectedSeq: 2 }), true);
      // A missing instance is not_found for an update and false for a delete, whatever the sequence.
      assert.equal(thrown(() => engine.instances.update(alice, 'Order', 'o1', { title: 'Desk' }, { expectedSeq: 3 }), EngineError).code, 'not_found');
      assert.equal(engine.instances.delete(alice, 'Order', 'o1', { expectedSeq: 3 }), false);
      for (const expectedSeq of [-1, 1.5, Number.NaN]) {
        assert.equal(thrown(() => engine.instances.update(alice, 'Order', 'o1', {}, { expectedSeq }), EngineError).code, 'invalid_argument');
        assert.equal(thrown(() => engine.instances.delete(alice, 'Order', 'o1', { expectedSeq }), EngineError).code, 'invalid_argument');
      }
    });

    test('an expected sequence is checked against the file inside the write, not against what the caller read', () => {
      const path = freshPath();
      const first = track(openEngine({ path, driver, policy: allowAll }));
      publish(first, orderDocument());
      first.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const read = first.instances.get(alice, 'Order', 'o1');
      // A second connection writes between the read and the update.
      const second = track(openEngine({ path, driver, policy: allowAll }));
      second.instances.update(bob, 'Order', 'o1', { title: 'Lamp' });
      const error = thrown(() => first.instances.update(alice, 'Order', 'o1', { title: 'Chair' }, { expectedSeq: read?.seq }), EngineError);
      assert.equal(error.code, 'seq_mismatch');
      assert.deepEqual(first.instances.get(alice, 'Order', 'o1')?.data, { title: 'Lamp' });
    });

    test('an id created again after a delete continues its sequence', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.delete(alice, 'Order', 'o1');
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o1' }).seq, 3);
    });

    test('an instance keeps the version it was written with until it is written again', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 2 }, { id: 'o1' });
      const next = clone(orderDocument()) as { types: { Order: { fields: Array<Record<string, unknown>> } } };
      next.types.Order.fields.push({ name: 'note', typeRef: { name: 'string' } });
      publish(engine, next);
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.version, 1);
      assert.equal(engine.instances.update(alice, 'Order', 'o1', { note: 'fragile' }).version, 2);
      assert.equal(engine.instances.create(alice, 'Order', { title: 'Lamp' }).version, 2);
    });

    test('a version that drops a field stored instances hold is refused, and they still read', () => {
      const engine = withOrders({ driver });
      engine.instances.create(alice, 'Order', { title: 'Desk', quantity: 7 }, { id: 'o1' });
      const dropped = clone(orderDocument()) as { types: { Order: { fields: Array<{ name: string }> } } };
      dropped.types.Order.fields = dropped.types.Order.fields.filter((field) => field.name !== 'quantity');
      const error = thrown(() => engine.schemas.define(alice, dropped), IncompatibleChangeError);
      assert.deepEqual(error.changes.map((change) => change.message), ['field Order.quantity is removed']);
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1')?.data, { title: 'Desk', quantity: 7 });
    });

    test('an instance of a shared schema lives in the namespace that creates it', () => {
      const engine = openTestEngine({ driver, namespaces: { names: ['east', 'west', 'common'], shared: 'common' } });
      publish(engine, shipmentDocument, 'common');
      const created = engine.instances.create(alice, 'Shipment', { carrier: 'Post' }, { id: 's1', namespace: 'east' });
      assert.deepEqual([created.namespace, created.schemaNamespace, created.version], ['east', 'common', 1]);
      assert.equal(engine.instances.get(alice, 'Shipment', 's1', { namespace: 'west' }), undefined);
      assert.deepEqual(engine.instances.list(alice, 'Shipment', { namespace: 'common' }).items, []);
      assert.deepEqual(
        engine.instances.list(alice, 'Shipment', { namespace: 'east' }).items.map((item) => item.id),
        ['s1']
      );
    });

    describe('list', () => {
      function ids(engine: Engine, options: { limit?: number; cursor?: string; namespace?: string } = {}) {
        const page = engine.instances.list(alice, 'Order', options);
        return { ids: page.items.map((item) => item.id), next: page.next };
      }

      test('pages through instances in creation order', () => {
        const engine = withOrders({ driver });
        for (const id of ['g', 'a', 'f', 'b', 'e', 'c', 'd']) {
          engine.instances.create(alice, 'Order', { title: id }, { id });
        }
        const first = ids(engine, { limit: 3 });
        assert.deepEqual(first.ids, ['g', 'a', 'f']);
        const second = ids(engine, { limit: 3, cursor: first.next ?? undefined });
        assert.deepEqual(second.ids, ['b', 'e', 'c']);
        const third = ids(engine, { limit: 3, cursor: second.next ?? undefined });
        assert.deepEqual(third, { ids: ['d'], next: null });
      });

      test('a page holds 50 by default and at most 500', () => {
        const engine = withOrders({ driver });
        engine.storage.transaction(() => {
          for (let index = 0; index < 51; index += 1) {
            engine.instances.create(alice, 'Order', { title: `order ${index}` }, { id: `o${index}` });
          }
        });
        const page = ids(engine);
        assert.equal(page.ids.length, 50);
        assert.deepEqual(ids(engine, { cursor: page.next ?? undefined }), { ids: ['o50'], next: null });
        assert.equal(ids(engine, { limit: 500 }).ids.length, 51);
        for (const limit of [0, 501, 1.5]) {
          assert.equal(thrown(() => ids(engine, { limit }), EngineError).code, 'invalid_argument');
        }
      });

      test('an instance created or deleted while a client pages moves no other instance', () => {
        const engine = withOrders({ driver });
        for (const id of ['a', 'b', 'c', 'd', 'e']) {
          engine.instances.create(alice, 'Order', { title: id }, { id });
        }
        const first = ids(engine, { limit: 2 });
        assert.deepEqual(first.ids, ['a', 'b']);
        engine.instances.delete(alice, 'Order', 'a');
        engine.instances.delete(alice, 'Order', 'c');
        engine.instances.create(alice, 'Order', { title: 'f' }, { id: 'f' });
        engine.instances.create(alice, 'Order', { title: 'again' }, { id: 'a' });
        const second = ids(engine, { limit: 2, cursor: first.next ?? undefined });
        assert.deepEqual(second.ids, ['d', 'e']);
        assert.deepEqual(ids(engine, { limit: 2, cursor: second.next ?? undefined }), { ids: ['f', 'a'], next: null });
      });

      test('a list stays inside its namespace and schema', () => {
        const engine = openTestEngine({ driver, namespaces: { names: ['east'] } });
        publish(engine, orderDocument());
        publish(engine, orderDocument(), 'east');
        publish(engine, shipmentDocument);
        engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'here' });
        engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'there', namespace: 'east' });
        engine.instances.create(alice, 'Shipment', { carrier: 'Post' }, { id: 'elsewhere' });
        assert.deepEqual(ids(engine).ids, ['here']);
        assert.deepEqual(ids(engine, { namespace: 'east' }).ids, ['there']);
      });

      test('a cursor is one the engine returned', () => {
        const engine = withOrders({ driver });
        for (const cursor of ['', 'garbage', Buffer.from('after:-1').toString('base64url')]) {
          assert.equal(thrown(() => ids(engine, { cursor }), EngineError).code, 'invalid_argument');
        }
      });
    });
  });
}
