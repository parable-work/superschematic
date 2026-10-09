// A behavior's storage is its own: its SQL reaches its own tables only, a
// read cannot write, and its migrations create only tables and indexes of
// its own, a virtual table only with fts5.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorError, defineBehavior, type BehaviorMigration, type Engine, type SqlValue } from '../dist/index.js';
import { counter, openBehaviorEngine, publishItem, tablesOf } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, fieldsOf, thrown } from './helpers.ts';

afterEach(cleanup);

const sqlParams = {
  type: 'object',
  additionalProperties: false,
  required: ['sql'],
  properties: { sql: { type: 'string' }, values: { type: 'array' } },
} as const;

// A behavior that runs the SQL it is given, in a read and in a write.
function sneaky(migrations: BehaviorMigration[] = []) {
  return defineBehavior({
    declaration: {
      name: 'test.Sneaky',
      operations: [
        { name: 'read', paramsSchema: sqlParams, resultSchema: true },
        { name: 'write', paramsSchema: sqlParams, resultSchema: true, writes: true },
      ],
    },
    migrations: [
      {
        version: 1,
        name: 'notes',
        up(sql) {
          sql.run(`CREATE TABLE ${sql.table('notes')} (id TEXT PRIMARY KEY, body TEXT) STRICT`);
          sql.run(`CREATE VIRTUAL TABLE ${sql.table('search')} USING fts5(body)`);
        },
      },
      ...migrations,
    ],
    operations: {
      read: (context, params) => context.sql.all(params.sql as string, (params.values ?? []) as SqlValue[]),
      write: (context, params) => context.sql.run(params.sql as string, (params.values ?? []) as SqlValue[]).changes,
    },
  });
}

function sqlError(engine: Engine, operation: 'read' | 'write', sql: string): string {
  return thrown(() => engine.instances.invoke(alice, 'Item', 'i1', operation, { sql }), BehaviorError).message;
}

for (const driver of drivers) {
  describe(`behavior SQL (${driver})`, () => {
    function opened(): Engine {
      const engine = openBehaviorEngine({ driver, behaviors: [counter, sneaky()] });
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Sneaky' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      return engine;
    }

    test('a behavior reads and writes its own tables, virtual ones included', () => {
      const engine = opened();
      const write = (sql: string, values: SqlValue[] = []) => engine.instances.invoke(alice, 'Item', 'i1', 'write', { sql, values });
      const read = (sql: string, values: SqlValue[] = []) => engine.instances.invoke(alice, 'Item', 'i1', 'read', { sql, values });
      assert.equal(write("INSERT INTO bhv_test_sneaky__notes (id, body) VALUES ('n1', 'a desk'); "), 1);
      // bun:sqlite counts an FTS5 insert's shadow-table writes in changes; the read below is the check.
      write('INSERT INTO "BHV_TEST_SNEAKY__SEARCH" (body) VALUES (?)', ['a walnut desk']);
      assert.deepEqual(read('SELECT body FROM bhv_test_sneaky__notes -- engine_instances is only a comment'), [{ body: 'a desk' }]);
      assert.deepEqual(read('/* engine_events */ SELECT body FROM bhv_test_sneaky__search WHERE bhv_test_sneaky__search MATCH ?', ['walnut']), [
        { body: 'a walnut desk' },
      ]);
      assert.deepEqual(read('WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 3) SELECT i FROM n'), [{ i: 1 }, { i: 2 }, { i: 3 }]);
      // Data that looks like a reserved name goes in as a parameter.
      assert.equal(write('UPDATE bhv_test_sneaky__notes SET body = ?', ['engine_instances']), 1);
    });

    test("SQL naming anything outside the behavior's storage is refused before it runs", () => {
      const engine = opened();
      const outside: Array<[string, string]> = [
        ['SELECT data FROM engine_instances', 'engine_instances'],
        ['UPDATE engine_instances SET bhv_test_counter__count = 99', 'engine_instances'],
        ['SELECT * FROM "ENGINE_EVENTS"', 'ENGINE_EVENTS'],
        ['SELECT * FROM [engine_schemas]', 'engine_schemas'],
        ['SELECT * FROM `engine_behaviors`', 'engine_behaviors'],
        ["SELECT * FROM 'engine_instances'", 'engine_instances'],
        ['SELECT * FROM main.engine_migrations', 'engine_migrations'],
        ['DELETE FROM bhv_test_counter__history', 'bhv_test_counter__history'],
        ['SELECT name FROM sqlite_master', 'sqlite_master'],
        ["SELECT * FROM pragma_table_info('x')", 'pragma_table_info'],
        ["SELECT body FROM bhv_test_sneaky__notes WHERE body = 'engine_like'", 'engine_like'],
      ];
      for (const [sql, name] of outside) {
        assert.equal(
          sqlError(engine, 'write', sql),
          `behavior test.Sneaky: SQL refused: it names ${name}, which is not the behavior's own storage (bhv_test_sneaky__*): ${sql}`,
          sql
        );
      }
      const others: Array<[string, RegExp]> = [
        ['PRAGMA foreign_keys = OFF', /a write runs SELECT, VALUES, WITH, INSERT, UPDATE, DELETE or REPLACE, not PRAGMA/],
        ["ATTACH DATABASE 'other.db' AS other", /not ATTACH/],
        ['BEGIN', /not BEGIN/],
        ['SAVEPOINT mine', /not SAVEPOINT/],
        ['COMMIT', /not COMMIT/],
        ['CREATE TABLE bhv_test_sneaky__more (x TEXT)', /not CREATE/],
        ['DROP TABLE bhv_test_sneaky__notes', /not DROP/],
        [
          "INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n2'); DELETE FROM bhv_test_sneaky__notes",
          /the SQL holds more than one statement; run one at a time/,
        ],
        ["SELECT load_extension('evil')", /it calls load_extension/],
        ['', /the SQL holds no statement/],
      ];
      for (const [sql, message] of others) {
        assert.match(sqlError(engine, 'write', sql), message, sql);
      }
      assert.deepEqual(fieldsOf(engine.instances.get(alice, 'Item', 'i1')), { data: { title: 'Desk' }, behaviors: { 'test.Counter': { count: 0 } } });
    });

    test('a read runs SELECT, VALUES and WITH ... SELECT only', () => {
      const engine = opened();
      for (const [sql, message] of [
        ["INSERT INTO bhv_test_sneaky__notes (id) VALUES ('n9') RETURNING id", /a read runs SELECT, VALUES or WITH \.\.\. SELECT, not INSERT/],
        ['DELETE FROM bhv_test_sneaky__notes RETURNING id', /not DELETE/],
        [
          'WITH gone AS (SELECT id FROM bhv_test_sneaky__notes) DELETE FROM bhv_test_sneaky__notes WHERE id IN (SELECT id FROM gone) RETURNING id',
          /not WITH \.\.\. DELETE/,
        ],
        ["REPLACE INTO bhv_test_sneaky__notes (id) VALUES ('n9') RETURNING id", /not REPLACE/],
      ] as const) {
        assert.match(sqlError(engine, 'read', sql), message, sql);
      }
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'read', { sql: 'VALUES (1)' }), [{ column1: 1 }]);
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'read', { sql: 'SELECT id FROM bhv_test_sneaky__notes' }), []);
    });

    test("a table name is the behavior's own name for it", () => {
      const probe = defineBehavior({
        declaration: { name: 'test.Probe', operations: [{ name: 'name', paramsSchema: sqlParams, resultSchema: true }] },
        operations: { name: (context, params) => context.sql.table(params.sql as string) },
      });
      const engine = openBehaviorEngine({ driver, behaviors: [probe] });
      publishItem(engine, [{ name: 'test.Probe' }]);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'name', { sql: 'notes' }), 'bhv_test_probe__notes');
      for (const name of ['Notes', '../x', 'a b', '']) {
        assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'name', { sql: name }), BehaviorError).message, /table name .* must match/);
      }
    });

    test('a migration creates only tables and indexes of its own', () => {
      const cases: Array<[BehaviorMigration, RegExp]> = [
        [
          {
            version: 2,
            name: 'trigger',
            up: (sql) => void sql.run(`CREATE TRIGGER ${sql.table('t')} AFTER INSERT ON ${sql.table('notes')} BEGIN SELECT 1; END`),
          },
          /SQL refused: the SQL holds more than one statement|a migration may CREATE only tables and indexes of its own, not CREATE TRIGGER/,
        ],
        [{ version: 2, name: 'view', up: (sql) => void sql.run(`CREATE VIEW ${sql.table('v')} AS SELECT 1`) }, /not CREATE VIEW/],
        [{ version: 2, name: 'temp', up: (sql) => void sql.run(`CREATE TEMP TABLE ${sql.table('t')} (x)`) }, /not CREATE TEMP TABLE/],
        [{ version: 2, name: 'engine', up: (sql) => void sql.run('ALTER TABLE engine_instances ADD COLUMN x TEXT') }, /it names engine_instances/],
        [
          { version: 2, name: 'rename', up: (sql) => void sql.run(`ALTER TABLE ${sql.table('notes')} RENAME TO notes`) },
          /migration 2 "rename" created table notes/,
        ],
        [{ version: 2, name: 'pragma', up: (sql) => void sql.run('PRAGMA writable_schema = ON') }, /not PRAGMA/],
        // A virtual table is fts5, under a name of its own: no other module, no other name.
        [
          { version: 2, name: 'rtree', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE ${sql.table('boxes')} USING rtree(id, lo, hi)`) },
          /a migration creates a virtual table only with the fts5 module, not rtree/,
        ],
        [
          { version: 2, name: 'fts4', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE ${sql.table('old')} USING fts4(body)`) },
          /only with the fts5 module, not fts4/,
        ],
        [{ version: 2, name: 'dbstat', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE ${sql.table('stat')} USING dbstat`) }, /not dbstat/],
        [{ version: 2, name: 'no module', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE ${sql.table('bare')}`) }, /under a name of its own/],
        [
          { version: 2, name: 'other name', up: (sql) => void sql.run('CREATE VIRTUAL TABLE notes_index USING fts5(body)') },
          /a migration creates a virtual table only under a name of its own \(bhv_test_sneaky__\*, unqualified\)/,
        ],
        [
          { version: 2, name: 'qualified', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE main.${sql.table('more')} USING fts5(body)`) },
          /only under a name of its own/,
        ],
        [
          { version: 2, name: 'temp', up: (sql) => void sql.run(`CREATE VIRTUAL TABLE temp.${sql.table('more')} USING fts5(body)`) },
          /only under a name of its own/,
        ],
        [
          {
            version: 2,
            name: 'content',
            up: (sql) => void sql.run(`CREATE VIRTUAL TABLE ${sql.table('mirror')} USING fts5(data, content='engine_instances')`),
          },
          /it names engine_instances/,
        ],
        [{ version: 2, name: 'later', up: (async () => undefined) as never }, /migration 2 is synchronous \(D16\): it returned a promise/],
      ];
      for (const [migration, message] of cases) {
        const engine = openBehaviorEngine({ driver, behaviors: [sneaky([migration])] });
        engine.schemas.define(
          alice,
          (() => {
            const document = {
              kind: 'General',
              name: 'Item',
              types: { Item: { name: 'Item', role: 'EmbeddedStruct', fields: [], behaviors: [{ name: 'test.Sneaky' }] } },
            };
            return document;
          })()
        );
        const error = thrown(() => engine.schemas.publish(alice, 'Item'), BehaviorError);
        assert.match(error.message, message, migration.name);
        assert.deepEqual(tablesOf(engine, 'bhv_'), [], migration.name);
        assert.deepEqual(tablesOf(engine, 'notes'), [], migration.name);
      }
    });

    test('a migration creates an fts5 table of its own, IF NOT EXISTS and quoted alike', () => {
      const more: BehaviorMigration = {
        version: 2,
        name: 'more',
        up(sql) {
          sql.run(`CREATE VIRTUAL TABLE IF NOT EXISTS ${sql.table('titles')} USING fts5(title)`);
          sql.run(`CREATE VIRTUAL TABLE "${sql.table('bodies')}" USING FTS5(body, tokenize = 'unicode61 remove_diacritics 2')`);
        },
      };
      const engine = openBehaviorEngine({ driver, behaviors: [counter, sneaky([more])] });
      publishItem(engine, [{ name: 'test.Sneaky' }]);
      assert.deepEqual(
        tablesOf(engine, 'bhv_test_sneaky__').filter((name) => !/_(data|idx|content|docsize|config)$/.test(name)),
        ['bhv_test_sneaky__bodies', 'bhv_test_sneaky__notes', 'bhv_test_sneaky__search', 'bhv_test_sneaky__titles']
      );
    });
  });
}
