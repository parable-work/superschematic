// A behavior's own columns across its schema (D16, amended): the read-only
// relation sql.instances() names, over the instances of the call's schema
// in the call's namespace, with their metadata, their own fields and the
// behavior's columns, each statement that names it asking read; and the
// indexes a migration lists on those columns.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  EngineError,
  RELATION_COLUMNS,
  allowAll,
  defineBehavior,
  openEngine,
  type AccessPolicy,
  type AccessRequest,
  type BehaviorMigration,
  type Engine,
  type EngineOptions,
  type Principal,
  type SqlReader,
  type SqlValue,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, freshPath, openTestEngine, schemaDocument, thrown, track } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };

const RELATION = 'bhv_test_queue___instances';

const sqlParams = {
  type: 'object',
  additionalProperties: false,
  required: ['sql'],
  properties: { sql: { type: 'string' }, values: { type: 'array' } },
} as const;

// fill puts the relation's name and the tags table's in a statement the
// test writes with {instances} and {tags}.
function fill(sql: SqlReader, text: string): string {
  return text.replaceAll('{instances}', sql.instances()).replaceAll('{tags}', sql.table('tags'));
}

const queueMigrations: BehaviorMigration[] = [
  {
    version: 1,
    name: 'rank',
    columns: { rank: { type: 'integer', notNull: true, default: 0 }, holder: { type: 'text' } },
    up(sql) {
      sql.run(`CREATE TABLE ${sql.table('tags')} (namespace TEXT NOT NULL, schema TEXT NOT NULL, id TEXT NOT NULL, tag TEXT NOT NULL) STRICT`);
    },
  },
  { version: 2, name: 'indexes', indexes: { by_rank: ['rank'], by_holder: ['holder', 'rank'] } },
];

// test.Queue ranks instances, tags them in a table of its own, and runs
// the SQL a test gives it over its columns across the schema.
function queue(migrations: BehaviorMigration[] = queueMigrations) {
  return defineBehavior({
    declaration: {
      name: 'test.Queue',
      description: 'Ranks instances and reads the ranks across the schema.',
      fields: [{ name: 'ahead', description: 'How many instances of the schema rank above this one.' }],
      operations: [
        {
          name: 'rank',
          paramsSchema: {
            type: 'object',
            additionalProperties: false,
            required: ['rank'],
            properties: { rank: { type: 'integer' }, holder: { type: ['string', 'null'] } },
          },
          resultSchema: true,
          writes: true,
        },
        {
          name: 'tag',
          paramsSchema: { type: 'object', additionalProperties: false, required: ['tag'], properties: { tag: { type: 'string' } } },
          resultSchema: true,
          writes: true,
        },
        { name: 'relation', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: { type: 'string' } },
        { name: 'read', paramsSchema: sqlParams, resultSchema: true },
        { name: 'write', paramsSchema: sqlParams, resultSchema: true, writes: true },
        { name: 'select', scope: 'schema', paramsSchema: sqlParams, resultSchema: true },
      ],
    },
    migrations,
    operations: {
      rank(context, params) {
        context.columns.set({ rank: params.rank as number, ...(params.holder === undefined ? {} : { holder: params.holder as string | null }) });
        return null;
      },
      tag(context, params) {
        context.sql.run(`INSERT INTO ${context.sql.table('tags')} VALUES (?, ?, ?, ?)`, [context.namespace, context.schema, context.id, params.tag as string]);
        return null;
      },
      relation: (context) => context.sql.instances(),
      read: (context, params) => context.sql.all(fill(context.sql, params.sql as string), (params.values ?? []) as SqlValue[]),
      write: (context, params) => context.sql.run(fill(context.sql, params.sql as string), (params.values ?? []) as SqlValue[]).changes,
    },
    schemaOperations: {
      select: (context, params) => context.sql.all(fill(context.sql, params.sql as string), (params.values ?? []) as SqlValue[]),
    },
    fields: {
      ahead: (view) =>
        view.sql.get(`SELECT COUNT(*) AS n FROM {instances} WHERE rank > (SELECT rank FROM {instances} WHERE id = ?)`.replaceAll('{instances}', view.sql.instances()), [
          view.id,
        ])?.n,
    },
  });
}

// test.Other keeps a column of its own on the same instances.
const other = defineBehavior({
  declaration: { name: 'test.Other', description: 'Keeps a secret on each instance.' },
  migrations: [{ version: 1, name: 'secret', columns: { secret: { type: 'text' } } }],
  initialize(context) {
    context.columns.set({ secret: 'hidden' });
  },
});

function itemDocument(name = 'Item'): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = [{ name: 'test.Queue' }, { name: 'test.Other' }];
  return document;
}

/** A policy that records every question and lets alice do anything; bob gets what rules allow. */
function recording(rules: (request: AccessRequest) => boolean = () => true): { policy: AccessPolicy; asked: AccessRequest[] } {
  const asked: AccessRequest[] = [];
  const policy: AccessPolicy = (request) => {
    asked.push(request);
    return request.principal.subject === 'alice' || rules(request);
  };
  return { policy, asked };
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [queue(), other], ...options });
  }

  function publish(engine: Engine, name = 'Item', namespace?: string): void {
    const target = namespace === undefined ? {} : { namespace };
    engine.schemas.define(alice, itemDocument(name), target);
    engine.schemas.publish(alice, name, target);
  }

  // Item i1 ranks 3 with holder w1, i2 ranks 1, i3 ranks 2 with holder w2.
  function ranked(options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine);
    for (const [id, title, rank, holder] of [
      ['i1', 'Desk', 3, 'w1'],
      ['i2', 'Lamp', 1, null],
      ['i3', 'Rug', 2, 'w2'],
    ] as const) {
      engine.instances.create(alice, 'Item', { title }, { id });
      engine.instances.invoke(alice, 'Item', id, 'rank', { rank, holder });
    }
    return engine;
  }

  function select(engine: Engine, sql: string, values: SqlValue[] = [], principal: Principal = alice, namespace?: string): unknown {
    return engine.instances.invokeSchema(principal, 'Item', 'select', { sql, values }, namespace === undefined ? {} : { namespace });
  }

  describe(`a behavior's columns across its schema (${driver})`, () => {
    test("the relation holds the call's schema's instances with their metadata, own fields and the behavior's columns, under its names", () => {
      const engine = ranked();
      assert.equal(engine.instances.invoke(alice, 'Item', 'i1', 'relation'), RELATION);
      const rows = select(engine, 'SELECT * FROM {instances} ORDER BY id') as Array<Record<string, unknown>>;
      assert.deepEqual(
        rows.map((row) => Object.keys(row)),
        rows.map(() => [...RELATION_COLUMNS, 'rank', 'holder'])
      );
      const desk = engine.instances.get(alice, 'Item', 'i1');
      assert.deepEqual(rows[0], {
        id: 'i1',
        seq: desk?.seq,
        version: 1,
        created_at: desk?.createdAt,
        created_by: 'alice',
        updated_at: desk?.updatedAt,
        updated_by: 'alice',
        data: '{"title":"Desk"}',
        rank: 3,
        holder: 'w1',
      });
      assert.deepEqual(
        rows.map((row) => [row.id, row.rank, row.holder]),
        [
          ['i1', 3, 'w1'],
          ['i2', 1, null],
          ['i3', 2, 'w2'],
        ]
      );
      // A view reads it too: a field reader counts the instances ranked above.
      assert.deepEqual(engine.instances.get(alice, 'Item', 'i2')?.data, { title: 'Lamp', ahead: 2 });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i3', 'read', { sql: "SELECT json_extract(data, '$.title') AS title FROM {instances} WHERE rank >= ? ORDER BY rank", values: [2] }), [
        { title: 'Rug' },
        { title: 'Desk' },
      ]);
    });

    test("another behavior's columns are not in it, under its name or its storage's, and the checker still refuses what is not the behavior's", () => {
      const engine = ranked();
      assert.match(thrown(() => select(engine, 'SELECT secret FROM {instances}'), Error).message, /no such column: secret/);
      assert.match(thrown(() => select(engine, 'SELECT bhv_test_queue__rank FROM {instances}'), Error).message, /no such column: bhv_test_queue__rank/);
      for (const [sql, name] of [
        ['SELECT bhv_test_other__secret FROM {instances}', 'bhv_test_other__secret'],
        ['SELECT i.id FROM {instances} i JOIN engine_instances e ON e.id = i.id', 'engine_instances'],
        ["SELECT id FROM {instances} WHERE id IN (SELECT id FROM 'ENGINE_INSTANCES')", 'ENGINE_INSTANCES'],
        ['SELECT id FROM {instances} UNION SELECT name FROM sqlite_master', 'sqlite_master'],
      ] as const) {
        assert.equal(
          thrown(() => select(engine, sql), BehaviorError).message,
          `behavior test.Queue: SQL refused: it names ${name}, which is not the behavior's own storage (bhv_test_queue__*): ${sql.replaceAll('{instances}', RELATION)}`,
          sql
        );
      }
    });

    test('it shows the namespace its call runs in, and the schema; a schema of the shared namespace shows each namespace its own', () => {
      const engine = open({ namespaces: { names: ['east', 'west', 'library'], shared: 'library' } });
      publish(engine, 'Item', 'library');
      publish(engine, 'Thing', 'east');
      engine.instances.create(alice, 'Item', { title: 'East desk' }, { id: 'i1', namespace: 'east' });
      engine.instances.create(alice, 'Item', { title: 'East lamp' }, { id: 'i2', namespace: 'east' });
      engine.instances.create(alice, 'Item', { title: 'West desk' }, { id: 'i1', namespace: 'west' });
      engine.instances.create(alice, 'Thing', { title: 'A thing' }, { id: 't1', namespace: 'east' });
      const titles = (schema: string, namespace: string) =>
        (
          engine.instances.invokeSchema(alice, schema, 'select', { sql: "SELECT id, json_extract(data, '$.title') AS title FROM {instances} ORDER BY id" }, { namespace }) as Array<
            Record<string, unknown>
          >
        ).map((row) => `${String(row.id)} ${String(row.title)}`);
      assert.deepEqual(titles('Item', 'east'), ['i1 East desk', 'i2 East lamp']);
      assert.deepEqual(titles('Item', 'west'), ['i1 West desk']);
      assert.deepEqual(titles('Item', 'library'), []);
      assert.deepEqual(titles('Thing', 'east'), ['t1 A thing']);
    });

    test("it joins the behavior's own tables, merges into WITH and WITH RECURSIVE, and the statement's parameters bind as written", () => {
      const engine = ranked();
      for (const [id, tag] of [
        ['i1', 'red'],
        ['i2', 'blue'],
        ['i3', 'red'],
      ] as const) {
        engine.instances.invoke(alice, 'Item', id, 'tag', { tag });
      }
      assert.deepEqual(
        select(
          engine,
          'SELECT i.id, t.tag FROM {instances} i JOIN {tags} t ON t.namespace = ? AND t.schema = ? AND t.id = i.id WHERE t.tag = ? AND i.rank > ? ORDER BY i.rank',
          ['default', 'Item', 'red', 1]
        ),
        [
          { id: 'i3', tag: 'red' },
          { id: 'i1', tag: 'red' },
        ]
      );
      assert.deepEqual(select(engine, 'WITH top AS (SELECT id, rank FROM {instances} WHERE rank >= ?) SELECT id FROM top ORDER BY rank DESC', [2]), [
        { id: 'i1' },
        { id: 'i3' },
      ]);
      assert.deepEqual(
        select(
          engine,
          '/* ranks one to n */ with recursive n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT n.i, x.id FROM n JOIN "BHV_TEST_QUEUE___INSTANCES" x ON x.rank = n.i ORDER BY n.i',
          [2]
        ),
        [
          { i: 1, id: 'i2' },
          { i: 2, id: 'i3' },
        ]
      );
      // Numbered parameters, and the relation named twice.
      assert.deepEqual(select(engine, 'SELECT a.id FROM {instances} a JOIN {instances} b ON b.rank = a.rank - ?2 WHERE a.id <> ?1 ORDER BY a.id', ['i3', 1]), [{ id: 'i1' }]);
      assert.deepEqual(select(engine, 'VALUES (?)', [7]), [{ column1: 7 }]);
      // A write reads it: its own table takes a row per instance it selects.
      assert.equal(
        engine.instances.invoke(alice, 'Item', 'i1', 'write', {
          sql: "INSERT INTO {tags} (namespace, schema, id, tag) SELECT ?, ?, id, 'held' FROM {instances} WHERE holder IS NOT NULL AND rank > ?",
          values: ['default', 'Item', 2],
        }),
        1
      );
      assert.deepEqual(select(engine, "SELECT id FROM {tags} WHERE tag = 'held'"), [{ id: 'i1' }]);
    });

    test('each statement that names it asks read on the schema, as the caller, once; a refusal is forbidden', () => {
      const { policy, asked } = recording(() => true);
      const engine = ranked({ policy });
      asked.length = 0;
      select(engine, 'SELECT a.id FROM {instances} a JOIN {instances} b ON a.id = b.id', [], bob);
      assert.deepEqual(asked, [
        { principal: bob, action: 'read', namespace: 'default', schema: 'Item', operation: 'select' },
        { principal: bob, action: 'read', namespace: 'default', schema: 'Item' },
      ]);
      asked.length = 0;
      select(engine, 'SELECT COUNT(*) AS n FROM {tags}', [], bob);
      assert.deepEqual(asked, [{ principal: bob, action: 'read', namespace: 'default', schema: 'Item', operation: 'select' }], 'a statement on its own tables asks nothing more');

      // bob may call the operation, and not read the schema.
      const refusing = recording((request) => request.operation !== undefined);
      const closed = ranked({ policy: refusing.policy });
      const refused = thrown(() => select(closed, 'SELECT id FROM {instances}', [], bob), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not read Item in namespace default']);
      assert.deepEqual(refusing.asked.at(-1), { principal: bob, action: 'read', namespace: 'default', schema: 'Item' });
      assert.deepEqual(select(closed, 'SELECT COUNT(*) AS n FROM {tags}', [], bob), [{ n: 0 }]);
    });

    test('nothing writes through it', () => {
      const engine = ranked();
      for (const sql of [
        'UPDATE {instances} SET rank = 99',
        "DELETE FROM {instances} WHERE id = 'i1'",
        "INSERT INTO {instances} (id, rank) VALUES ('x', 1)",
        "REPLACE INTO {instances} (id, rank) VALUES ('i1', 99)",
        'WITH t AS (SELECT 1) UPDATE {instances} SET holder = NULL',
      ]) {
        assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'write', { sql }), Error).message, new RegExp(`no such table: ${RELATION}`), sql);
      }
      assert.match(
        thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'read', { sql: 'UPDATE {instances} SET rank = 99' }), BehaviorError).message,
        /a read runs SELECT, VALUES or WITH \.\.\. SELECT, not UPDATE/
      );
      assert.deepEqual(
        (select(engine, 'SELECT id, rank, holder FROM {instances} ORDER BY id') as unknown[]).length,
        3
      );
      assert.deepEqual(select(engine, 'SELECT rank FROM {instances} WHERE id = ?', ['i1']), [{ rank: 3 }]);
    });

    test("a migration and afterConfigChange have no relation, and a migration may not name what the engine keeps for the behavior", () => {
      for (const [migration, message] of [
        [
          { version: 3, name: 'reads', up: (sql: unknown) => void (sql as SqlReader).instances() },
          /sql\.instances\(\) reads as a principal, and a migration and afterConfigChange act for none/,
        ],
        [
          { version: 3, name: 'shadow', up: (sql: { run(sql: string): unknown }) => void sql.run(`CREATE TABLE ${RELATION} (id TEXT)`) },
          /it names bhv_test_queue___instances, which is under bhv_test_queue___, the names the engine keeps/,
        ],
        [
          { version: 3, name: 'drop', up: (sql: { run(sql: string): unknown }) => void sql.run('DROP INDEX bhv_test_queue___index_by_rank') },
          /it names bhv_test_queue___index_by_rank, which is under bhv_test_queue___/,
        ],
      ] as const) {
        const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [queue([...queueMigrations, migration as BehaviorMigration]), other] });
        engine.schemas.define(alice, itemDocument());
        assert.match(thrown(() => engine.schemas.publish(alice, 'Item'), BehaviorError).message, message, migration.name);
        assert.equal(engine.schemas.live(alice, 'Item'), undefined);
      }
      const publishing = defineBehavior({
        ...queue(),
        afterConfigChange(context) {
          (context.sql as unknown as SqlReader).instances();
        },
      });
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [publishing, other] });
      engine.schemas.define(alice, itemDocument());
      assert.match(thrown(() => engine.schemas.publish(alice, 'Item'), BehaviorError).message, /sql\.instances\(\) reads as a principal/);
    });

    test("a column named like one of the relation's makes the relation a BehaviorError, and nothing else", () => {
      const clash = defineBehavior({
        declaration: {
          name: 'test.Clash',
          operations: [{ name: 'select', scope: 'schema', paramsSchema: sqlParams, resultSchema: true }],
        },
        migrations: [
          { version: 1, name: 'version', columns: { version: { type: 'integer' } } },
          { version: 2, name: 'notes', up: (sql) => void sql.run(`CREATE TABLE ${sql.table('notes')} (id TEXT) STRICT`) },
        ],
        schemaOperations: {
          select: (context, params) => context.sql.all((params.sql as string).replaceAll('{notes}', context.sql.table('notes'))),
        },
      });
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [clash] });
      const document = schemaDocument('Item', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
        types: Record<string, Record<string, unknown>>;
      };
      document.types.Item.behaviors = [{ name: 'test.Clash' }];
      engine.schemas.define(alice, document);
      engine.schemas.publish(alice, 'Item');
      assert.deepEqual(engine.instances.invokeSchema(alice, 'Item', 'select', { sql: 'SELECT COUNT(*) AS n FROM {notes}' }), [{ n: 0 }]);
      assert.match(
        thrown(() => engine.instances.invokeSchema(alice, 'Item', 'select', { sql: 'SELECT COUNT(*) AS n FROM bhv_test_clash___instances' }), BehaviorError).message,
        /behavior test\.Clash: sql\.instances\(\): its column version shares a name with the relation's own columns/
      );
    });
  });

  describe(`indexes on a behavior's columns (${driver})`, () => {
    test("a migration's index leads with the namespace and schema, and a filter on its columns reads it", () => {
      const engine = ranked();
      const indexes = engine.storage
        .all("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'engine_instances' AND name LIKE 'bhv%' ORDER BY name")
        .map((row) => String(row.name));
      assert.deepEqual(indexes, ['bhv_test_queue___index_by_holder', 'bhv_test_queue___index_by_rank']);
      assert.deepEqual(
        engine.storage.all("SELECT name FROM pragma_index_info('bhv_test_queue___index_by_holder') ORDER BY seqno").map((row) => String(row.name)),
        ['namespace', 'schema', 'bhv_test_queue__holder', 'bhv_test_queue__rank']
      );

      // The plan of the statement the engine runs for the behavior.
      const plan = (sql: string, values: SqlValue[]): string => {
        const statements: Array<[string, readonly SqlValue[]]> = [];
        const all = engine.storage.all.bind(engine.storage);
        engine.storage.all = (text: string, params: readonly SqlValue[] = []) => {
          statements.push([text, params]);
          return all(text, params);
        };
        try {
          select(engine, sql, values);
        } finally {
          engine.storage.all = all;
        }
        const ran = statements.find(([text]) => text.includes('engine_instances'));
        assert.ok(ran, 'the behavior statement ran with the relation');
        return all(`EXPLAIN QUERY PLAN ${ran[0]}`, ran[1])
          .map((row) => String(row.detail))
          .join('; ');
      };
      assert.match(plan('SELECT id FROM {instances} WHERE holder = ? AND rank > ? ORDER BY rank', ['w1', 0]), /SEARCH engine_instances USING INDEX bhv_test_queue___index_by_holder \(namespace=\? AND schema=\? AND bhv_test_queue__holder=\? AND bhv_test_queue__rank>\?\)/);
      assert.match(plan('SELECT id FROM {instances} WHERE rank > ?', [1]), /USING (COVERING )?INDEX bhv_test_queue___index_by_rank \(namespace=\? AND schema=\? AND bhv_test_queue__rank>\?\)/);
      assert.doesNotMatch(plan('SELECT a.id FROM {instances} a JOIN {instances} b ON b.rank = a.rank WHERE a.rank > ?', [1]), /MATERIALIZE/);
    });

    test('registration refuses an index that is not over columns of its own, added by then', () => {
      const v1 = queueMigrations[0];
      const cases: Array<[BehaviorMigration[], RegExp]> = [
        [[v1, { version: 2, name: 'x', indexes: { by_size: ['size'] } }], /migration 2 index by_size names size, which no migration up to this one adds; an index lists the behavior's own columns by its own names \(rank, holder\)/],
        [[v1, { version: 2, name: 'x', indexes: { by_secret: ['bhv_test_other__secret'] } }], /index by_secret names bhv_test_other__secret, which no migration up to this one adds/],
        [[v1, { version: 2, name: 'x', indexes: { by_data: ['rank', 'data'] } }], /index by_data names data, which no migration up to this one adds/],
        [
          [
            { ...v1, indexes: { by_later: ['later'] } },
            { version: 2, name: 'later', columns: { later: { type: 'integer' } } },
          ],
          /migration 1 index by_later names later, which no migration up to this one adds/,
        ],
        [[v1, { version: 2, name: 'x', indexes: { by_none: [] } }], /migration 2 index by_none is a non-empty list of the behavior's column names/],
        [[v1, { version: 2, name: 'x', indexes: { ByRank: ['rank'] } }], /migration 2 index ByRank: an index name matches/],
        [[v1, { version: 2, name: 'x', indexes: { by_rank: ['rank', 'rank'] } }], /index by_rank lists a column twice/],
        [
          [
            { ...v1, indexes: { by_rank: ['rank'] } },
            { version: 2, name: 'x', indexes: { by_rank: ['holder'] } },
          ],
          /migration 2 adds index by_rank, which an earlier migration added/,
        ],
        [[v1, { version: 2, name: 'x', indexes: [['rank']] as never }], /migration 2 indexes is an object of column lists by index name/],
      ];
      for (const [migrations, problem] of cases) {
        assert.match(thrown(() => open({ behaviors: [queue(migrations), other] }), TypeError).message, problem);
      }
    });

    test('an index a later migration lists is created when the implementation registers on a file that holds its storage', () => {
      const path = freshPath();
      const options = { path, driver, policy: allowAll, metaSchema: openMetaSchema() };
      const first = track(openEngine({ ...options, behaviors: [queue([queueMigrations[0]]), other] }));
      first.schemas.define(alice, itemDocument());
      first.schemas.publish(alice, 'Item');
      const indexes = (engine: Engine) =>
        engine.storage.all("SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'bhv_test_queue___index%' ORDER BY name").map((row) => String(row.name));
      assert.deepEqual(indexes(first), []);
      first.close();
      const second = track(openEngine({ ...options, behaviors: [queue(), other] }));
      assert.deepEqual(indexes(second), ['bhv_test_queue___index_by_holder', 'bhv_test_queue___index_by_rank']);
    });
  });
}
