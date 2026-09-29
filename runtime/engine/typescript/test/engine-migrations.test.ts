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
import { alice, cleanup, drivers, freshPath, orderDocument, schemaDocument, track } from './helpers.ts';

afterEach(cleanup);

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
};

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

        const engine = track(openEngine({ path, driver, policy: allowAll }));
        assert.deepEqual(
          appliedMigrations(engine.storage, ENGINE_OWNER).map((row) => row.version),
          engineMigrations.migrations.map((migration) => migration.version)
        );
        seed.check(engine);
        engine.schemas.define(alice, { ...orderDocument(), description: 'after the migration' });
        assert.equal(engine.schemas.publish(alice, 'Order').published, true);
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
