// The SQLite seam: both adapters behave alike, the connection settings, the
// transactions and the per-owner migration ledger. On Bun every case runs
// against bun:sqlite and node:sqlite.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  SQLITE_BUSY,
  SqliteError,
  Storage,
  appliedMigrations,
  isBun,
  migrate,
  openDriver,
  type Migration,
  type MigrationSet,
} from '../dist/index.js';
import { cleanup, drivers, freshPath, thrown, track } from './helpers.ts';

afterEach(cleanup);

test('auto picks bun:sqlite on Bun and node:sqlite elsewhere', () => {
  const driver = track(openDriver(freshPath()));
  assert.equal(driver.name, isBun() ? 'bun' : 'node');
});

test('an unknown driver name is refused', () => {
  assert.throws(() => openDriver(freshPath(), 'sqlite3' as never), /unknown SQLite driver "sqlite3"/);
});

test('the bun driver needs Bun', { skip: isBun() }, () => {
  assert.throws(() => openDriver(freshPath(), 'bun'), /needs Bun/);
});

for (const driver of drivers) {
  const open = (path = freshPath(), busyTimeoutMs?: number): Storage => track(Storage.open(path, { driver, busyTimeoutMs }));

  describe(`storage (${driver})`, () => {
    test('opens in WAL mode with foreign keys and a busy timeout', () => {
      const storage = open();
      assert.equal(storage.driverName, driver);
      assert.equal(String(storage.get('PRAGMA journal_mode')?.journal_mode).toLowerCase(), 'wal');
      assert.equal(storage.get('PRAGMA foreign_keys')?.foreign_keys, 1);
      assert.equal(storage.get('PRAGMA busy_timeout')?.timeout, 5000);
    });

    test('the busy timeout is an option', () => {
      assert.equal(open(freshPath(), 1234).get('PRAGMA busy_timeout')?.timeout, 1234);
      assert.throws(() => open(freshPath(), -1), /busyTimeoutMs must be a non-negative integer/);
    });

    test('a database that cannot use write-ahead logging is refused', () => {
      assert.throws(() => Storage.open(':memory:', { driver }), /would not use write-ahead logging/);
    });

    test('foreign keys are enforced', () => {
      const storage = open();
      storage.exec(`
        CREATE TABLE catalogs (id TEXT PRIMARY KEY) STRICT;
        CREATE TABLE items (id TEXT PRIMARY KEY, catalog TEXT NOT NULL REFERENCES catalogs (id)) STRICT;
      `);
      const error = thrown(() => storage.run('INSERT INTO items VALUES (?, ?)', ['i1', 'missing']), SqliteError);
      assert.equal(error.code, 787); // SQLITE_CONSTRAINT_FOREIGNKEY
    });

    test('rows are plain objects, and a missing row is undefined', () => {
      const storage = open();
      storage.exec("CREATE TABLE notes (id TEXT PRIMARY KEY, body TEXT) STRICT; INSERT INTO notes VALUES ('n1', 'hello')");
      const row = storage.get('SELECT id, body FROM notes WHERE id = ?', ['n1']);
      assert.deepEqual(row, { id: 'n1', body: 'hello' });
      assert.equal(Object.getPrototypeOf(row), Object.prototype);
      assert.equal(Object.getPrototypeOf(storage.all('SELECT id FROM notes')[0]), Object.prototype);
      assert.equal(storage.get('SELECT id FROM notes WHERE id = ?', ['n2']), undefined);
    });

    test('binds strings, numbers, bigints, bytes and null, and refuses anything else alike', () => {
      const storage = open();
      storage.exec('CREATE TABLE cells (t TEXT, i INTEGER, r REAL, b BLOB) STRICT');
      storage.run('INSERT INTO cells VALUES (?, ?, ?, ?)', ['x', 42n, 1.5, new Uint8Array([1, 2, 3])]);
      storage.run('INSERT INTO cells VALUES (?, ?, ?, ?)', [null, 7, null, null]);
      const rows = storage.all('SELECT t, i, r, b FROM cells ORDER BY rowid');
      assert.equal(rows[0].t, 'x');
      assert.equal(rows[0].i, 42);
      assert.equal(rows[0].r, 1.5);
      assert.deepEqual([...(rows[0].b as Uint8Array)], [1, 2, 3]);
      assert.deepEqual(rows[1], { t: null, i: 7, r: null, b: null });
      for (const value of [true, undefined, Number.NaN, Infinity, {}, [1]]) {
        assert.throws(() => storage.run('INSERT INTO cells (t) VALUES (?)', [value as never]), TypeError);
      }
      assert.equal(storage.get('SELECT COUNT(*) AS n FROM cells')?.n, 2);
    });

    test('SQLite failures carry the extended result code', () => {
      const storage = open();
      storage.exec('CREATE TABLE skus (code TEXT UNIQUE) STRICT');
      storage.run('INSERT INTO skus VALUES (?)', ['A-1']);
      const error = thrown(() => storage.run('INSERT INTO skus VALUES (?)', ['A-1']), SqliteError);
      assert.equal(error.code, 2067); // SQLITE_CONSTRAINT_UNIQUE
      assert.match(error.message, /UNIQUE constraint failed/);
      assert.equal(thrown(() => storage.exec('SELECT * FROM nowhere'), SqliteError).code, 1);
    });

    test('a second writer waits for the busy timeout, then fails with SQLITE_BUSY', () => {
      const path = freshPath();
      const first = open(path);
      const second = open(path, 50);
      first.exec('CREATE TABLE counters (n INTEGER) STRICT');
      first.transaction(() => {
        first.run('INSERT INTO counters VALUES (?)', [1]);
        const error = thrown(() => second.transaction(() => second.run('INSERT INTO counters VALUES (?)', [2])), SqliteError);
        assert.equal(error.code, SQLITE_BUSY);
      });
      assert.deepEqual(second.all('SELECT n FROM counters'), [{ n: 1 }]);
    });

    test('a transaction commits when its function returns and rolls back when it throws', () => {
      const storage = open();
      storage.exec('CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT');
      assert.equal(storage.transaction(() => storage.run('INSERT INTO notes VALUES (?)', ['kept']).changes), 1);
      assert.throws(
        () =>
          storage.transaction(() => {
            storage.run('INSERT INTO notes VALUES (?)', ['dropped']);
            throw new Error('refused');
          }),
        /refused/
      );
      assert.deepEqual(storage.all('SELECT id FROM notes'), [{ id: 'kept' }]);
      assert.equal(storage.inTransaction, false);
    });

    test('a nested transaction is a savepoint that rolls back alone', () => {
      const storage = open();
      storage.exec('CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT');
      storage.transaction(() => {
        storage.run('INSERT INTO notes VALUES (?)', ['outer']);
        assert.throws(() =>
          storage.transaction(() => {
            storage.run('INSERT INTO notes VALUES (?)', ['inner']);
            throw new Error('inner refused');
          })
        );
        storage.transaction(() => storage.run('INSERT INTO notes VALUES (?)', ['second inner']));
        assert.equal(storage.inTransaction, true);
      });
      assert.deepEqual(storage.all('SELECT id FROM notes ORDER BY id'), [{ id: 'outer' }, { id: 'second inner' }]);
    });

    test('afterCommit runs once the outermost transaction commits, and never for what rolls back', () => {
      const storage = open();
      storage.exec('CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT');
      const ran: string[] = [];
      assert.throws(() => storage.afterCommit(() => ran.push('outside')), /needs an open transaction/);
      storage.transaction(() => {
        storage.afterCommit(() => ran.push('outer'));
        storage.transaction(() => storage.afterCommit(() => ran.push('inner kept')));
        assert.throws(() =>
          storage.transaction(() => {
            storage.afterCommit(() => ran.push('inner dropped'));
            throw new Error('inner refused');
          })
        );
        storage.afterCommit(() => {
          throw new Error('a failing hook');
        });
        storage.afterCommit(() => ran.push('after a failing hook'));
        assert.deepEqual(ran, []);
      });
      assert.deepEqual(ran, ['outer', 'inner kept', 'after a failing hook']);
      assert.throws(() =>
        storage.transaction(() => {
          storage.afterCommit(() => ran.push('rolled back'));
          throw new Error('refused');
        })
      );
      storage.transaction(() => storage.run('INSERT INTO notes VALUES (?)', ['n1']));
      assert.deepEqual(ran, ['outer', 'inner kept', 'after a failing hook']);
    });

    test('a transaction whose function returns a promise is rolled back', () => {
      const storage = open();
      storage.exec('CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT');
      assert.throws(
        () =>
          storage.transaction(() => {
            storage.run('INSERT INTO notes VALUES (?)', ['async']);
            return Promise.resolve();
          }),
        /synchronous/
      );
      assert.deepEqual(storage.all('SELECT id FROM notes'), []);
      assert.equal(storage.inTransaction, false);
    });

    test('reopening a file keeps its data', () => {
      const path = freshPath();
      const first = Storage.open(path, { driver });
      first.exec("CREATE TABLE notes (id TEXT PRIMARY KEY) STRICT; INSERT INTO notes VALUES ('n1')");
      first.close();
      assert.deepEqual(open(path).all('SELECT id FROM notes'), [{ id: 'n1' }]);
    });

    test('a closed connection refuses statements', () => {
      const storage = Storage.open(freshPath(), { driver });
      storage.close();
      assert.throws(() => storage.get('SELECT 1'), /closed/);
    });
  });

  describe(`migrations (${driver})`, () => {
    const catalogs: Migration = {
      version: 1,
      name: 'catalogs',
      up: (storage) => storage.exec('CREATE TABLE catalogs (id TEXT PRIMARY KEY) STRICT'),
    };
    const catalogTitles: Migration = {
      version: 2,
      name: 'catalog titles',
      up: (storage) => storage.exec('ALTER TABLE catalogs ADD COLUMN title TEXT'),
    };
    const set = (owner: string, ...migrations: Migration[]): MigrationSet => ({ owner, migrations });

    test('applies every migration to an empty file, then nothing', () => {
      const storage = open();
      assert.deepEqual(migrate(storage, set('catalog', catalogs, catalogTitles), 1000), {
        owner: 'catalog',
        from: 0,
        to: 2,
        applied: [1, 2],
      });
      assert.deepEqual(appliedMigrations(storage, 'catalog'), [
        { owner: 'catalog', version: 1, name: 'catalogs', appliedAt: 1000 },
        { owner: 'catalog', version: 2, name: 'catalog titles', appliedAt: 1000 },
      ]);
      assert.deepEqual(migrate(storage, set('catalog', catalogs, catalogTitles)), {
        owner: 'catalog',
        from: 2,
        to: 2,
        applied: [],
      });
    });

    test('applies only what the file lacks, keeping its rows', () => {
      const storage = open();
      migrate(storage, set('catalog', catalogs));
      storage.run('INSERT INTO catalogs (id) VALUES (?)', ['spring']);
      assert.deepEqual(migrate(storage, set('catalog', catalogs, catalogTitles)).applied, [2]);
      assert.deepEqual(storage.all('SELECT id, title FROM catalogs'), [{ id: 'spring', title: null }]);
    });

    test('owners keep separate records, so another owner can add columns to a table it does not own', () => {
      const storage = open();
      migrate(storage, set('catalog', catalogs));
      const featured = set('catalog.Featured', {
        version: 1,
        name: 'featured flag and picks',
        up: (s) =>
          s.exec(`
            ALTER TABLE catalogs ADD COLUMN featured INTEGER NOT NULL DEFAULT 0;
            CREATE TABLE catalog_featured_picks (catalog TEXT NOT NULL REFERENCES catalogs (id)) STRICT;
          `),
      });
      assert.deepEqual(migrate(storage, featured).applied, [1]);
      assert.deepEqual(migrate(storage, set('catalog', catalogs, catalogTitles)).applied, [2]);
      assert.deepEqual(
        appliedMigrations(storage, 'catalog.Featured').map((row) => row.version),
        [1]
      );
      assert.deepEqual(
        appliedMigrations(storage, 'catalog').map((row) => row.version),
        [1, 2]
      );
    });

    test('a file ahead of the build is refused', () => {
      const storage = open();
      migrate(storage, set('catalog', catalogs, catalogTitles));
      assert.throws(() => migrate(storage, set('catalog', catalogs)), /at migration 2, newer than this build's 1; migrations do not run backwards/);
    });

    test('a migration recorded under another name is refused', () => {
      const storage = open();
      migrate(storage, set('catalog', catalogs));
      const renamed = { ...catalogs, name: 'catalog table' };
      assert.throws(() => migrate(storage, set('catalog', renamed)), /migration 1 is "catalog table" in this build, but the file records migration 1 "catalogs"/);
    });

    test('a failing migration leaves neither its changes nor its record', () => {
      const storage = open();
      const failing: Migration = {
        version: 2,
        name: 'half done',
        up: (s) => {
          s.exec('CREATE TABLE half_done (id TEXT) STRICT');
          throw new Error('migration failed');
        },
      };
      assert.throws(() => migrate(storage, set('catalog', catalogs, failing)), /migration failed/);
      assert.deepEqual(
        appliedMigrations(storage, 'catalog').map((row) => row.version),
        [1]
      );
      assert.equal(storage.get("SELECT name FROM sqlite_master WHERE name = 'half_done'"), undefined);
    });

    test('a set must be numbered 1, 2, 3, ... and have an owner name', () => {
      const storage = open();
      assert.throws(() => migrate(storage, set('catalog', catalogTitles)), /numbered 1, 2, 3/);
      assert.throws(() => migrate(storage, set('catalog', catalogs, catalogs)), /position 2 holds version 1/);
      assert.throws(() => migrate(storage, set('', catalogs)), /migration owner "" must match/);
      assert.throws(() => migrate(storage, set('catalog', { ...catalogs, name: '' })), /needs a name/);
    });
  });
}
