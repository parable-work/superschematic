// The engine's own migrations, from an empty file and from the file each
// earlier version of the engine left. A new migration adds a seed here for
// the version before it: rows written the way that version wrote them,
// which the engine must still read and build on after migrating.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { ENGINE_OWNER, Storage, appliedMigrations, engineMigrations, migrate, openEngine } from '../dist/index.js';
import { cleanup, drivers, freshPath, orderDocument, track } from './helpers.ts';

afterEach(cleanup);

interface Seed {
  /** Writes rows into a file at this version, as that version wrote them. */
  write(storage: Storage): void;
  /** Checks, through the engine, that the rows survived the later migrations. */
  check(engine: ReturnType<typeof openEngine>): void;
}

const seeds: Record<number, Seed> = {
  0: {
    write() {},
    check(engine) {
      assert.deepEqual(engine.schemas.list(), []);
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

        const engine = track(openEngine({ path, driver }));
        assert.deepEqual(
          appliedMigrations(engine.storage, ENGINE_OWNER).map((row) => row.version),
          engineMigrations.migrations.map((migration) => migration.version)
        );
        seed.check(engine);
        engine.schemas.define(orderDocument());
        assert.equal(engine.schemas.publish('Order').published, true);
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
      assert.throws(() => openEngine({ path, driver }), /migrations do not run backwards/);
    });

    test('reopening an engine keeps its schemas', () => {
      const path = freshPath();
      const first = openEngine({ path, driver });
      first.schemas.define(orderDocument());
      first.schemas.publish('Order');
      first.close();
      const second = track(openEngine({ path, driver }));
      assert.equal(second.schemas.live('Order')?.version, 1);
    });
  });
}
