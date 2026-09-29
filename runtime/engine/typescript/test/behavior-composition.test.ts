// Composition: what define and publish check about a type's behaviors,
// the storage a publish creates, a version whose behavior the engine lacks,
// and the compatibility rule for behavior configs.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  IncompatibleChangeError,
  SchemaDocumentError,
  allowAll,
  appliedMigrations,
  defineBehavior,
  openEngine,
  type BehaviorDeclaration,
  type Engine,
} from '../dist/index.js';
import {
  columnsOf,
  counter,
  counterDeclaration,
  flag,
  itemDocument,
  openBehaviorEngine,
  openMetaSchema,
  publishItem,
  tablesOf,
  tally,
} from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, freshPath, schemaDocument, thrown, track } from './helpers.ts';

afterEach(cleanup);

// A behavior whose field and operation collide with the counter's.
const shadow = defineBehavior({
  declaration: {
    name: 'test.Shadow',
    fields: [{ name: 'count' }],
    operations: [{ name: 'history', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true }],
  },
  fields: { count: () => 1 },
  operations: { history: () => [] },
});

function issuesOf(fn: () => unknown): Array<{ path: string; message: string }> {
  return thrown(fn, SchemaDocumentError).issues;
}

for (const driver of drivers) {
  describe(`behavior composition (${driver})`, () => {
    test('define and publish refuse what the compiler loader refuses, at the behavior', () => {
      const engine = openBehaviorEngine({ driver, behaviors: [counter, flag, tally, shadow] });
      const cases: Array<[Parameters<typeof itemDocument>[0], string, string]> = [
        [[{ name: 'test.Counter' }, { name: 'test.Counter' }], '/types/Item/behaviors/1', 'type Item lists behavior test.Counter twice'],
        [[{ name: 'test.Missing' }], '/types/Item/behaviors/0', 'behavior test.Missing on type Item: no implementation registered'],
        [[{ name: 'test.Counter', config: { start: -1 } }], '/types/Item/behaviors/0/config', 'type Item: behavior test.Counter config: /start must be >= 0'],
        [
          [{ name: 'test.Counter', config: { start: 5, limit: 2 } }],
          '/types/Item/behaviors/0/config',
          'type Item: behavior test.Counter config: start 5 is past limit 2',
        ],
        [[{ name: 'test.Flag', config: { strict: true } }], '/types/Item/behaviors/0/config', 'type Item: behavior test.Flag takes no config'],
        [[{ name: 'test.Tally' }], '/types/Item/behaviors/0', 'type Item: behavior test.Tally requires behavior test.Counter, which the type does not list'],
        [
          [{ name: 'test.Counter' }, { name: 'test.Tally' }, { name: 'test.Flag' }],
          '/types/Item/behaviors/2',
          'type Item: behavior test.Flag conflicts with behavior test.Tally, which the type also lists',
        ],
        [
          [{ name: 'test.Counter' }, { name: 'test.Shadow' }],
          '/types/Item/behaviors/1',
          'type Item: behaviors test.Counter and test.Shadow both add field count',
        ],
      ];
      for (const [behaviors, path, message] of cases) {
        const issues = issuesOf(() => engine.schemas.define(alice, itemDocument(behaviors)));
        assert.deepEqual(issues[0], { path, message }, message);
      }
      assert.deepEqual(
        issuesOf(() => engine.schemas.define(alice, itemDocument([{ name: 'test.Counter' }, { name: 'test.Shadow' }]))).map((issue) => issue.message),
        [
          'type Item: behaviors test.Counter and test.Shadow both add field count',
          'type Item: behaviors test.Counter and test.Shadow both add operation history',
        ]
      );
      assert.deepEqual(engine.schemas.list(alice), []);
    });

    test('a behavior field may not take the name or the JSON key of a field of the type', () => {
      const engine = openBehaviorEngine({ driver });
      for (const field of [
        { name: 'count', typeRef: { name: 'number' } },
        { name: 'total', jsonTag: 'count', typeRef: { name: 'number' } },
      ]) {
        assert.deepEqual(
          issuesOf(() => engine.schemas.define(alice, itemDocument([{ name: 'test.Counter' }], [field]))),
          [{ path: '/types/Item/behaviors/0', message: 'type Item: behavior test.Counter adds field count, which the type declares' }]
        );
      }
    });

    test('a behavior composes on the instance type only', () => {
      const engine = openBehaviorEngine({ driver });
      const document = schemaDocument('Item', [{ name: 'part', typeRef: { name: 'Part' } }], {
        types: { Part: { name: 'Part', role: 'EmbeddedStruct', fields: [], behaviors: [{ name: 'test.Counter' }] } },
      });
      assert.deepEqual(
        issuesOf(() => engine.schemas.define(alice, document)),
        [
          {
            path: '/types/Part/behaviors/0',
            message: 'type Part: behavior test.Counter composes on the instance type, Item; Part is a nested type, which has no instances',
          },
        ]
      );
    });

    test('publish checks the draft again, against the implementations registered then', () => {
      const path = freshPath();
      const first = openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [counter] });
      first.schemas.define(alice, itemDocument([{ name: 'test.Counter' }]));
      first.close();
      const second = track(openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema() }));
      assert.deepEqual(
        issuesOf(() => second.schemas.publish(alice, 'Item')),
        [{ path: '/types/Item/behaviors/0', message: 'behavior test.Counter on type Item: no implementation registered' }]
      );
      assert.equal(second.schemas.live(alice, 'Item'), undefined);
    });

    test('storage is created when a schema that composes the behavior is published, and not before', () => {
      const engine = openBehaviorEngine({ driver, clock: () => 7 });
      engine.schemas.define(alice, itemDocument([{ name: 'test.Counter' }, { name: 'test.Flag' }]));
      assert.deepEqual(
        columnsOf(engine).filter((column) => column.startsWith('bhv_')),
        []
      );
      assert.deepEqual(tablesOf(engine, 'bhv_'), []);

      engine.schemas.publish(alice, 'Item');
      assert.deepEqual(
        columnsOf(engine).filter((column) => column.startsWith('bhv_')),
        ['bhv_test_counter__count', 'bhv_test_flag__flagged', 'bhv_test_flag__reason']
      );
      assert.deepEqual(tablesOf(engine, 'bhv_'), ['bhv_test_counter__history', 'bhv_test_counter__history_by_instance']);
      assert.deepEqual(engine.storage.all('SELECT name, key, created_at FROM engine_behaviors ORDER BY name'), [
        { name: 'test.Counter', key: 'test_counter', created_at: 7 },
        { name: 'test.Flag', key: 'test_flag', created_at: 7 },
      ]);
      assert.deepEqual(
        appliedMigrations(engine.storage, 'test.Counter').map((row) => [row.version, row.name]),
        [
          [1, 'count'],
          [2, 'history'],
        ]
      );
      // The tally is registered but no published schema composes it.
      assert.deepEqual(appliedMigrations(engine.storage, 'test.Tally'), []);
    });

    test('schemas.behaviors lists what a version composes, with configs and declarations', () => {
      const engine = openBehaviorEngine({ driver });
      publishItem(engine, [{ name: 'test.Counter', config: { limit: 4 } }, { name: 'test.Flag' }]);
      assert.deepEqual(engine.schemas.behaviors(alice, 'Item'), [
        { name: 'test.Counter', config: { limit: 4 }, declaration: counterDeclaration },
        { name: 'test.Flag', config: {}, declaration: engine.behaviors.declaration('test.Flag') },
      ]);
      assert.equal(thrown(() => engine.schemas.behaviors(alice, 'Item', { version: 2 }), EngineError).code, 'not_found');
    });

    test('a failed publish leaves no storage behind', () => {
      const broken = defineBehavior({
        ...counter,
        declaration: { ...counterDeclaration, name: 'test.Broken' } as BehaviorDeclaration,
        migrations: [...(counter.migrations ?? []), { version: 3, name: 'escape', up: (sql) => void sql.run('CREATE TABLE loose (id TEXT)') }],
      });
      const engine = openBehaviorEngine({ driver, behaviors: [broken] });
      engine.schemas.define(alice, itemDocument([{ name: 'test.Broken' }]));
      assert.throws(
        () => engine.schemas.publish(alice, 'Item'),
        /behavior test\.Broken: migration 3 "escape" created table loose; a behavior creates only tables and indexes named bhv_test_broken__\*/
      );
      assert.deepEqual(
        columnsOf(engine).filter((column) => column.startsWith('bhv_')),
        []
      );
      assert.deepEqual(tablesOf(engine, 'bhv_'), []);
      assert.deepEqual(tablesOf(engine, 'loose'), []);
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM engine_behaviors')?.n, 0);
      assert.equal(engine.schemas.live(alice, 'Item'), undefined);
      assert.equal(engine.schemas.draft(alice, 'Item')?.name, 'Item');
    });

    test('names that lower-case alike get distinct keys', () => {
      const twin = (name: string) =>
        defineBehavior({
          declaration: { name, fields: [{ name: `f${name.length}` }] },
          fields: { [`f${name.length}`]: () => 1 },
          migrations: [{ version: 1, name: 'c', columns: { c: { type: 'integer' } } }],
        });
      const engine = openBehaviorEngine({ driver, behaviors: [twin('test.Twin'), twin('test_.Twin')] });
      engine.schemas.define(
        alice,
        schemaDocument('A', [], { types: { A: { name: 'A', role: 'EmbeddedStruct', fields: [], behaviors: [{ name: 'test.Twin' }] } } })
      );
      engine.schemas.publish(alice, 'A');
      engine.schemas.define(
        alice,
        schemaDocument('B', [], { types: { B: { name: 'B', role: 'EmbeddedStruct', fields: [], behaviors: [{ name: 'test_.Twin' }] } } })
      );
      engine.schemas.publish(alice, 'B');
      assert.deepEqual(engine.storage.all('SELECT name, key FROM engine_behaviors ORDER BY key'), [
        { name: 'test.Twin', key: 'test_twin' },
        { name: 'test_.Twin', key: 'test_twin_2' },
      ]);
    });

    test('an engine without the implementation refuses a live version that composes it, until one registers', () => {
      const path = freshPath();
      const first = openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [counter] });
      publishItem(first, [{ name: 'test.Counter', config: { start: 2 } }]);
      first.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      first.close();

      const second = track(openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema() }));
      const calls = [
        () => second.instances.get(alice, 'Item', 'i1'),
        () => second.instances.list(alice, 'Item'),
        () => second.instances.create(alice, 'Item', { title: 'Lamp' }),
        () => second.instances.update(alice, 'Item', 'i1', { title: 'Lamp' }),
        () => second.instances.delete(alice, 'Item', 'i1'),
        () => second.instances.invoke(alice, 'Item', 'i1', 'increment'),
        () => second.schemas.validate(alice, 'Item', { title: 'Lamp' }),
      ];
      for (const call of calls) {
        const error = thrown(call, EngineError);
        assert.equal(error.code, 'unavailable');
        assert.match(
          error.message,
          /schema Item version 1 in namespace default composes behaviors this engine cannot run: behavior test\.Counter on type Item: no implementation registered/
        );
      }
      second.behaviors.register(counter);
      assert.deepEqual(second.instances.get(alice, 'Item', 'i1')?.data, { title: 'Desk', count: 2 });
    });

    test("registering brings existing storage up to the implementation's last migration", () => {
      const path = freshPath();
      const early = defineBehavior({ ...counter, migrations: counter.migrations?.slice(0, 1) });
      const first = openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [early] });
      publishItem(first, [{ name: 'test.Counter' }]);
      assert.deepEqual(
        appliedMigrations(first.storage, 'test.Counter').map((row) => row.version),
        [1]
      );
      first.close();

      const second = track(openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [counter] }));
      assert.deepEqual(
        appliedMigrations(second.storage, 'test.Counter').map((row) => row.version),
        [1, 2]
      );
      assert.deepEqual(tablesOf(second, 'bhv_test_counter__history'), ['bhv_test_counter__history', 'bhv_test_counter__history_by_instance']);
      second.close();

      assert.throws(
        () => openEngine({ path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [early] }),
        /test\.Counter storage is at migration 2, newer than this build's 1; migrations do not run backwards/
      );
    });
  });

  describe(`behavior configs across versions (${driver})`, () => {
    function published(behaviors: Parameters<typeof itemDocument>[0], instance = true): Engine {
      const engine = openBehaviorEngine({ driver });
      publishItem(engine, behaviors);
      if (instance) {
        engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      }
      return engine;
    }

    function changesOf(engine: Engine, behaviors: Parameters<typeof itemDocument>[0]): string[] {
      return thrown(() => engine.schemas.define(alice, itemDocument(behaviors)), IncompatibleChangeError).changes.map((change) => change.message);
    }

    test('without a configChange, only an identical config is a compatible version', () => {
      const engine = published([{ name: 'test.Counter' }, { name: 'test.Flag' }]);
      const document = itemDocument([{ name: 'test.Counter' }, { name: 'test.Flag' }]);
      (document as { description?: string }).description = 'the same behaviors';
      engine.schemas.define(alice, document);
      assert.equal(engine.schemas.publish(alice, 'Item').version, 2);
    });

    test('an implementation decides which config changes a version may make', () => {
      const engine = published([{ name: 'test.Counter', config: { limit: 5 } }]);
      engine.schemas.define(alice, itemDocument([{ name: 'test.Counter', config: { limit: 9 } }]));
      assert.equal(engine.schemas.publish(alice, 'Item').version, 2);
      assert.deepEqual(changesOf(engine, [{ name: 'test.Counter', config: { limit: 3 } }]), [
        'behavior test.Counter on type Item cannot change its config from {"limit":9} to {"limit":3}: a limit can only rise or go',
      ]);
      assert.deepEqual(changesOf(engine, [{ name: 'test.Counter', config: { limit: 9, start: 1 } }]), [
        'behavior test.Counter on type Item cannot change its config from {"limit":9} to {"limit":9,"start":1}: start cannot change',
      ]);
      const error = thrown(() => engine.schemas.define(alice, itemDocument([{ name: 'test.Counter', config: { limit: 3 } }])), IncompatibleChangeError);
      assert.equal(error.code, 'incompatible_change');
      assert.equal(error.changes[0].path, 'Item.behaviors.test.Counter');
    });

    test('a behavior is added to or removed from a schema with instances only when it opts in', () => {
      const engine = published([{ name: 'test.Flag' }]);
      assert.deepEqual(changesOf(engine, [{ name: 'test.Counter' }]), [
        'behavior test.Flag cannot be removed from type Item, which has instances: it cannot be removed from a schema that has instances',
      ]);
      // The counter opts in to being added: the instance that exists counts from 0.
      engine.schemas.define(alice, itemDocument([{ name: 'test.Flag' }, { name: 'test.Counter' }]));
      assert.equal(engine.schemas.publish(alice, 'Item').version, 2);
      assert.deepEqual(engine.instances.get(alice, 'Item', 'i1')?.data, { title: 'Desk', flagged: false, count: 0 });
      // ... and refuses to be removed.
      assert.deepEqual(changesOf(engine, [{ name: 'test.Flag' }]), [
        'behavior test.Counter cannot be removed from type Item, which has instances: removing it loses every count',
      ]);
      const counted = published([{ name: 'test.Counter' }]);
      assert.deepEqual(changesOf(counted, [{ name: 'test.Counter' }, { name: 'test.Tally' }]), [
        'behavior test.Tally cannot be added to type Item, which has instances: it cannot be added to a schema that has instances',
      ]);
    });

    test('with no instances, behaviors come and go freely', () => {
      const engine = published([{ name: 'test.Flag' }], false);
      engine.schemas.define(alice, itemDocument([{ name: 'test.Counter' }, { name: 'test.Tally' }]));
      assert.equal(engine.schemas.publish(alice, 'Item').version, 2);
      engine.schemas.define(alice, itemDocument([]));
      assert.equal(engine.schemas.publish(alice, 'Item').version, 3);
    });

    test('instances in any namespace count, for a schema the shared namespace holds', () => {
      const engine = openBehaviorEngine({ driver, namespaces: { names: ['shared', 'east'], shared: 'shared' } });
      engine.schemas.define(alice, itemDocument([{ name: 'test.Flag' }]), { namespace: 'shared' });
      engine.schemas.publish(alice, 'Item', { namespace: 'shared' });
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { namespace: 'east' });
      assert.deepEqual(
        thrown(() => engine.schemas.define(alice, itemDocument([]), { namespace: 'shared' }), IncompatibleChangeError).changes.map((change) => change.path),
        ['Item.behaviors.test.Flag']
      );
    });
  });
}

// The declarations the Go registry's test extension registers, as their JSON
// files hold them, and the meta-schema a binary with that extension writes:
// the engine runs implementations of the files the compiler embeds.
describe("a binary's meta-schema and its behaviors' declarations", () => {
  const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');
  const stockDeclaration = JSON.parse(read('../../../../internal/registry/registrytest/stock.behavior.json')) as BehaviorDeclaration;
  const auditedDeclaration = JSON.parse(read('../../../../internal/registry/registrytest/audited.behavior.json')) as BehaviorDeclaration;
  const metaSchema = read('../../../schema/testdata/schema_file_parity.meta-schema.json');

  const stock = defineBehavior<{ aisles: number; unit?: string }>({
    declaration: stockDeclaration,
    migrations: [{ version: 1, name: 'on hand', columns: { on_hand: { type: 'integer', notNull: true, default: 0 } } }],
    operations: {
      restock(context, params) {
        const onHand = Number(context.columns.get().on_hand) + (params.quantity as number);
        context.columns.set({ on_hand: onHand });
        return { onHand };
      },
      countStock: (context) => ({ onHand: Number(context.columns.get().on_hand) }),
    },
    fields: { onHand: (context) => context.columns.get().on_hand },
  });
  const audited = defineBehavior({
    declaration: auditedDeclaration,
    migrations: [{ version: 1, name: 'audited at', columns: { audited_at: { type: 'text' } } }],
    operations: {
      audit(context) {
        const auditedAt = new Date(context.now).toISOString();
        context.columns.set({ audited_at: auditedAt });
        return { auditedAt };
      },
    },
    fields: { auditedAt: (context) => context.columns.get().audited_at },
  });

  test('a schema that uses them loads, publishes and runs', () => {
    const engine = track(openEngine({ path: freshPath(), policy: allowAll, metaSchema, behaviors: [stock, audited], clock: () => 0 }));
    const document = clone(schemaDocument('Item', [{ name: 'sku', typeRef: { name: 'string' }, required: true }])) as {
      types: { Item: Record<string, unknown> };
    };
    document.types.Item.behaviors = [{ name: 'acme.Stock', config: { aisles: 2 } }, { name: 'acme.Audited' }];
    engine.schemas.define(alice, document);
    engine.schemas.publish(alice, 'Item');
    engine.instances.create(alice, 'Item', { sku: 'D-1' }, { id: 'i1' });
    assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'restock', { quantity: 4 }), { onHand: 4 });
    assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'audit'), { auditedAt: '1970-01-01T00:00:00.000Z' });
    assert.deepEqual(engine.instances.get(alice, 'Item', 'i1')?.data, { sku: 'D-1', onHand: 4, auditedAt: '1970-01-01T00:00:00.000Z' });
  });

  test('the meta-schema refuses a name the binary does not declare before the engine looks', () => {
    const engine = track(openEngine({ path: freshPath(), policy: allowAll, metaSchema, behaviors: [stock, audited, counter] }));
    const document = clone(schemaDocument('Item', [])) as { types: { Item: Record<string, unknown> } };
    document.types.Item.behaviors = [{ name: 'test.Counter' }];
    assert.equal(thrown(() => engine.schemas.define(alice, document), SchemaDocumentError).issues[0].path, '/types/Item/behaviors/0/name');
  });
});
