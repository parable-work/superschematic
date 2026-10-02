// The engine's own migrations, from an empty file and from the file each
// earlier version of the engine left. A new migration adds a seed here for
// the version before it: rows written the way that version wrote them,
// which the engine must still read and build on after migrating.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { afterEach, describe, test } from 'node:test';

import { loadSchemaFile } from '@superschematic/schema-runtime';

import {
  ENGINE_OWNER,
  Storage,
  allowAll,
  appliedMigrations,
  engineMigrations,
  migrate,
  openEngine,
  type Engine,
} from '../dist/index.js';
import { counter, itemDocument, openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { holder } from './reach-fixtures.ts';
import { ledger, ledgerDocument, mark, probe, resetProbe, runnerPrincipal } from './runner-fixtures.ts';
import { alice, cleanup, drivers, freshPath, orderDocument, schemaDocument, track } from './helpers.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

interface Seed {
  /** Writes rows into a file at this version, as that version wrote them. */
  write(storage: Storage): void;
  /** Checks, through the engine, that the rows survived the later migrations. */
  check(engine: Engine): void;
}

const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]);

function canonical(document: Record<string, unknown>): { text: string; hash: string } {
  const text = loadSchemaFile(JSON.stringify(document)).canonical;
  return { text, hash: createHash('sha256').update(text).digest('hex') };
}

const seeds: Record<number, Seed> = {
  0: {
    write() {},
    check(engine) {
      assert.deepEqual(engine.schemas.list(alice), []);
    },
  },
  // Version 1 stored schemas without actors, and had no instances or events.
  1: {
    write(storage) {
      const order = canonical(orderDocument());
      const note = canonical(noteDocument);
      storage.run(
        'INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, ?)',
        ['default', 'Order', 1, order.text, order.hash, 100, 200]
      );
      storage.run(
        'INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, published_at) VALUES (?, ?, ?, ?, ?, ?, NULL)',
        ['default', 'Note', 0, note.text, note.hash, 300]
      );
    },
    check(engine) {
      const order = engine.schemas.live(alice, 'Order');
      assert.equal(order?.version, 1);
      assert.equal(order?.definedAt, 100);
      assert.equal(order?.definedBy, null);
      assert.equal(order?.publishedBy, null);
      assert.equal(engine.schemas.draft(alice, 'Note')?.definedBy, null);
      assert.deepEqual(engine.events.read(alice).events, []);
      const created = engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(created.version, 1);
      assert.equal(engine.schemas.publish(alice, 'Note').version, 1);
      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.kind, event.schema]),
        [
          ['create', 'Order'],
          ['publish', 'Note'],
        ]
      );
    },
  },
  // Version 2 added actors, instances and the event log, with no operation
  // events and no behavior storage.
  2: {
    write(storage) {
      const order = canonical(orderDocument());
      storage.run(
        `INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 1, order.text, order.hash, 100, 'alice', 200, 'alice']
      );
      storage.run(
        `INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 'o1', 'default', 1, 1, '{"title":"Desk"}', 300, 'alice', 300, 'alice']
      );
      storage.run(
        'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)',
        ['publish', 'default', 'Order', null, null, 1, 'alice', 200, order.text]
      );
      storage.run(
        'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)',
        ['create', 'default', 'Order', 'o1', 1, 1, 'alice', 300, '{"title":"Desk"}']
      );
    },
    check(engine) {
      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.cursor, event.kind, event.seq]),
        [
          [1, 'publish', null],
          [2, 'create', 1],
        ]
      );
      const updated = engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' }, { expectedSeq: 1 });
      assert.deepEqual([updated.seq, updated.data], [2, { title: 'Lamp' }]);
      checkOperationEvents(engine, 3);
    },
  },
  // Version 3 indexed the publish events alone; its rows are version 2's.
  3: {
    write(storage) {
      const order = canonical(orderDocument());
      storage.run(
        `INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 1, order.text, order.hash, 100, 'alice', 200, 'alice']
      );
      storage.run(
        `INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 'o1', 'default', 1, 2, '{"title":"Lamp"}', 300, 'alice', 400, 'alice']
      );
      const insert = 'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)';
      storage.run(insert, ['publish', 'default', 'Order', null, null, 1, 'alice', 200, order.text]);
      storage.run(insert, ['create', 'default', 'Order', 'o1', 1, 1, 'alice', 300, '{"title":"Desk"}']);
      storage.run(insert, ['update', 'default', 'Order', 'o1', 2, 1, 'alice', 400, '{"title":"Lamp"}']);
    },
    check(engine) {
      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.cursor, event.kind, event.seq]),
        [
          [1, 'publish', null],
          [2, 'create', 1],
          [3, 'update', 2],
        ]
      );
      assert.deepEqual(engine.events.read(alice, { schema: 'Order', instanceId: 'o1', after: 2 }).events.map((event) => event.change), [
        { title: 'Lamp' },
      ]);
      const updated = engine.instances.update(alice, 'Order', 'o1', { title: 'Chair' }, { expectedSeq: 2 });
      assert.deepEqual([updated.seq, updated.data], [3, { title: 'Chair' }]);
      checkOperationEvents(engine, 4);
    },
  },
  // Version 4 added operation events and behavior storage, with no
  // references between instances.
  4: {
    write(storage) {
      const order = canonical(orderDocument());
      storage.run(
        `INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 1, order.text, order.hash, 100, 'alice', 200, 'alice']
      );
      storage.run(
        `INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 'o1', 'default', 1, 1, '{"title":"Desk"}', 300, 'alice', 300, 'alice']
      );
      const insert = 'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)';
      storage.run(insert, ['publish', 'default', 'Order', null, null, 1, 'alice', 200, order.text]);
      storage.run(insert, ['create', 'default', 'Order', 'o1', 1, 1, 'alice', 300, '{"title":"Desk"}']);
    },
    check(engine) {
      // A behavior on a migrated file records a reference to an instance
      // the seed wrote, and the delete of that instance runs its hook.
      engine.schemas.define(alice, itemDocument([{ name: 'test.Holder' }]));
      engine.schemas.publish(alice, 'Item');
      engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i1' });
      engine.instances.invoke(alice, 'Item', 'i1', 'hold', { schema: 'Order', id: 'o1' });
      assert.deepEqual(engine.instances.get(alice, 'Item', 'i1')?.data.held, ['Order/o1/']);
      assert.equal(engine.instances.delete(alice, 'Order', 'o1'), true);
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.held, undefined);
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'notes'), ['delete Order o1']);
    },
  },
  // Version 5 added references between instances. Its events record no
  // cause, and it has no subscriptions or schedules.
  5: {
    write(storage) {
      const order = canonical(orderDocument());
      storage.run(
        `INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by, published_at, published_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 1, order.text, order.hash, 100, 'alice', 200, 'alice']
      );
      storage.run(
        `INSERT INTO engine_instances (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        ['default', 'Order', 'o1', 'default', 1, 1, '{"title":"Desk"}', 300, 'alice', 300, 'alice']
      );
      const insert = 'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)';
      storage.run(insert, ['publish', 'default', 'Order', null, null, 1, 'alice', 200, order.text]);
      storage.run(insert, ['create', 'default', 'Order', 'o1', 1, 1, 'alice', 300, '{"title":"Desk"}']);
    },
    check(engine) {
      // The seed's events read with no cause. A reaction on the migrated
      // file hears the events after its schema's publish, not the seed's,
      // and the event it writes records its cause.
      assert.deepEqual(
        engine.events.read(alice).events.map((event) => [event.kind, event.cause]),
        [
          ['publish', undefined],
          ['create', undefined],
        ]
      );
      engine.schemas.define(alice, ledgerDocument('Ledgered'));
      engine.schemas.publish(alice, 'Ledgered');
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          mark(context, event);
        }
      };
      engine.instances.create(alice, 'Ledgered', { title: 'Lamp' }, { id: 'l1' });
      assert.equal(engine.runner.runDue().handled, 2);
      const [created, marked] = engine.events.read(alice, { schema: 'Ledgered', instanceId: 'l1' }).events;
      assert.deepEqual(marked.cause, { behavior: 'test.Ledger', event: created.cursor, depth: 1 });
      assert.deepEqual(
        engine.runner.status().subscriptions.map((status) => [status.schema, status.cursor]),
        [['Ledgered', marked.cursor]]
      );
    },
  },
};

// checkOperationEvents publishes a schema with a behavior on a migrated file,
// runs one of its writing operations and reads the events that follow the
// update the seed's check made at cursor `update`: the log's cursors carry
// on from the seed's, and the rebuilt kind CHECK admits an operation.
function checkOperationEvents(engine: Engine, update: number): void {
  publishItem(engine, [{ name: 'test.Counter' }]);
  engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i1' });
  engine.instances.invoke(alice, 'Item', 'i1', 'increment');
  assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 2);
  assert.deepEqual(
    engine.events.read(alice, { after: update - 1 }).events.map((event) => [event.cursor, event.kind]),
    [
      [update, 'update'],
      [update + 1, 'publish'],
      [update + 2, 'create'],
      [update + 3, 'operation'],
    ]
  );
}

// The indexes and triggers of engine_events that the code relies on: the
// instance index a one-instance read names (INDEXED BY), the namespace,
// schema and publish ranges, and the append-only triggers. The last
// migration rebuilds the table, which drops its indexes and triggers.
const EVENT_LOG_OBJECTS = [
  'index engine_events_instance',
  'index engine_events_namespace',
  'index engine_events_publish',
  'index engine_events_schema',
  'trigger engine_events_no_delete',
  'trigger engine_events_no_update',
];

function eventLogObjects(storage: Storage): string[] {
  return storage
    .all("SELECT type, name FROM sqlite_master WHERE tbl_name = 'engine_events' AND type IN ('index', 'trigger') ORDER BY type, name")
    .map((row) => `${String(row.type)} ${String(row.name)}`);
}

const latest = engineMigrations.migrations.length;

for (const driver of drivers) {
  describe(`engine migrations (${driver})`, () => {
    for (let from = 0; from < latest; from += 1) {
      test(`from version ${from}`, () => {
        const seed = seeds[from];
        assert.ok(seed, `add a seed for engine storage version ${from}`);
        const path = freshPath();
        const storage = Storage.open(path, { driver });
        migrate(storage, { owner: ENGINE_OWNER, migrations: engineMigrations.migrations.slice(0, from) });
        seed.write(storage);
        storage.close();

        const engine = track(
          openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [counter, holder, ledger], runner: { principal: runnerPrincipal } })
        );
        assert.deepEqual(
          appliedMigrations(engine.storage, ENGINE_OWNER).map((row) => row.version),
          engineMigrations.migrations.map((migration) => migration.version)
        );
        seed.check(engine);
        assert.deepEqual(eventLogObjects(engine.storage), EVENT_LOG_OBJECTS);
        engine.schemas.define(alice, { ...orderDocument(), description: 'after the migration' });
        assert.equal(engine.schemas.publish(alice, 'Order').published, true);
        // The log now holds an event whatever the seed wrote, so its triggers fire.
        assert.throws(() => engine.storage.run('DELETE FROM engine_events'), /engine_events is append-only/);
        assert.throws(() => engine.storage.run("UPDATE engine_events SET actor = 'mallory'"), /engine_events is append-only/);
      });
    }

    test('an engine whose file is newer than the build refuses to open it', () => {
      const path = freshPath();
      const storage = Storage.open(path, { driver });
      migrate(storage, {
        owner: ENGINE_OWNER,
        migrations: [...engineMigrations.migrations, { version: latest + 1, name: 'from a later build', up() {} }],
      });
      storage.close();
      assert.throws(() => openEngine({ path, driver, policy: allowAll }), /migrations do not run backwards/);
    });

    test('reopening an engine keeps its schemas, instances and events', () => {
      const path = freshPath();
      const first = openEngine({ path, driver, policy: allowAll });
      first.schemas.define(alice, orderDocument());
      first.schemas.publish(alice, 'Order');
      first.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      first.close();
      const second = track(openEngine({ path, driver, policy: allowAll }));
      assert.equal(second.schemas.live(alice, 'Order')?.version, 1);
      assert.equal(second.instances.get(alice, 'Order', 'o1')?.data.title, 'Desk');
      assert.equal(second.events.read(alice).events.length, 2);
    });
  });
}
