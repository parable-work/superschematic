// The engine's own migrations, as an engine's file shows them: an engine
// brings a new file up to its migrations, each recorded once in the ledger;
// a reopen keeps what the file holds and runs nothing again; a file newer
// than the build is refused; and the event log's indexes, triggers and
// checks are there. Each earlier version's file migrating, with rows written
// the way that version wrote them, is the Rust engine's test of its own
// migrations (runtime/engine/rust/engine/tests/migrations.rs,
// from_each_earlier_version), and a file the TypeScript engine wrote opening
// and migrating is its SQLite corpus's (runtime/engine/testdata/sqlite).
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { allowAll } from '../dist/index.js';
import { openConnection } from '../dist/native/sqlite.js';
import { alice, appliedMigrations, cleanup, drivers, freshPath, openEngine, orderDocument, track, type SqliteReader } from './helpers.ts';

afterEach(cleanup);

/** The owner the engine records its own migrations under. */
const ENGINE_OWNER = 'engine';

// The indexes and triggers of engine_events that the engine relies on: the
// instance index a one-instance read names (INDEXED BY), the namespace,
// schema and publish ranges, and the append-only triggers.
const EVENT_LOG_OBJECTS = [
  'index engine_events_instance',
  'index engine_events_namespace',
  'index engine_events_publish',
  'index engine_events_schema',
  'trigger engine_events_no_delete',
  'trigger engine_events_no_update',
];

function eventLogObjects(storage: SqliteReader): string[] {
  return storage
    .all("SELECT type, name FROM sqlite_master WHERE tbl_name = 'engine_events' AND type IN ('index', 'trigger') ORDER BY type, name")
    .map((row) => `${String(row.type)} ${String(row.name)}`);
}

for (const driver of drivers) {
  describe(`engine migrations (${driver})`, () => {
    test('a new file is brought up to every migration, each recorded once, and a reopen runs none again', () => {
      const path = freshPath();
      const engine = openEngine({ path, driver, policy: allowAll });
      const ledger = appliedMigrations(engine.storage, ENGINE_OWNER);
      assert.ok(ledger.length >= 11, `the engine has its migrations, got ${ledger.length}`);
      assert.deepEqual(
        ledger.map((row) => row.version),
        ledger.map((_, index) => index + 1)
      );
      for (const row of ledger) {
        assert.ok(row.name.length > 0, `migration ${row.version} has a name`);
      }
      assert.deepEqual(eventLogObjects(engine.storage), EVENT_LOG_OBJECTS);
      engine.close();

      const reopened = track(openEngine({ path, driver, policy: allowAll }));
      assert.deepEqual(appliedMigrations(reopened.storage, ENGINE_OWNER), ledger);
    });

    test("the event log is append-only, and a row's kind, instance and version are checked", () => {
      const engine = track(openEngine({ path: freshPath(), driver, policy: allowAll }));
      engine.schemas.define(alice, orderDocument());
      assert.equal(engine.schemas.publish(alice, 'Order').published, true);
      // The log holds the define's and the publish's events, so its triggers fire.
      assert.throws(() => engine.storage.run('DELETE FROM engine_events'), /engine_events is append-only/);
      assert.throws(() => engine.storage.run("UPDATE engine_events SET actor = 'mallory'"), /engine_events is append-only/);
      // A define has no instance and no version, and only a define lacks a version.
      const insert = 'INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)';
      for (const row of [
        ['define', 'default', 'Order', null, null, 1, 'mallory', 1],
        ['define', 'default', 'Order', 'o9', 1, null, 'mallory', 1],
        ['publish', 'default', 'Order', null, null, null, 'mallory', 1],
        ['create', 'default', 'Order', 'o9', 1, null, 'mallory', 1],
      ]) {
        assert.throws(() => engine.storage.run(insert, row), /CHECK constraint failed/, JSON.stringify(row));
      }
    });

    test('an engine whose file is newer than the build refuses to open it', () => {
      const path = freshPath();
      openEngine({ path, driver, policy: allowAll }).close();
      const connection = openConnection(path);
      const latest = Number(connection.get('SELECT MAX(version) AS latest FROM engine_migrations WHERE owner = ?', [ENGINE_OWNER])?.latest);
      connection.run('INSERT INTO engine_migrations (owner, version, name, applied_at) VALUES (?, ?, ?, ?)', [ENGINE_OWNER, latest + 1, 'from a later build', 1]);
      connection.close();
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
      assert.deepEqual(
        second.events.read(alice).events.map((event) => event.kind),
        ['define', 'publish', 'create']
      );
    });
  });
}
