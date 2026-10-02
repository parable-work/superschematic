// What a publish runs of a behavior: afterConfigChange, when the version
// adds the behavior, changes its config or removes it, once for each
// namespace whose instances the schema serves, inside the publish's
// transaction; and what parseConfig is told about the type's fields.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorError, defineBehavior, type Engine, type PublishContext } from '../dist/index.js';
import { itemDocument, openBehaviorEngine, tablesOf } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, thrown } from './helpers.ts';

afterEach(cleanup);

interface Call {
  namespace: string;
  schema: string;
  version: number;
  before: unknown;
  config: unknown;
  visited: string[];
}

// recorder records each afterConfigChange and writes what it visits to its
// own table; hook, when given, runs after that.
function recorder(calls: Call[], hook?: (context: PublishContext<{ tag: string }>) => unknown) {
  return defineBehavior<{ tag: string }>({
    declaration: {
      name: 'test.Recorder',
      configSchema: { type: 'object', additionalProperties: false, properties: { tag: { type: 'string' } } },
    },
    parseConfig: (config) => ({ tag: (config as { tag?: string }).tag ?? 'none' }),
    configChange: () => undefined,
    migrations: [
      {
        version: 1,
        name: 'seen',
        up(sql) {
          sql.run(`CREATE TABLE ${sql.table('seen')} (namespace TEXT NOT NULL, id TEXT NOT NULL, title TEXT, tag TEXT) STRICT`);
        },
      },
    ],
    afterConfigChange(context) {
      const visited: string[] = [];
      context.eachInstance((instance) => {
        assert.ok(Object.isFrozen(instance) && Object.isFrozen(instance.data));
        visited.push(instance.id);
        context.sql.run(`INSERT INTO ${context.sql.table('seen')} (namespace, id, title, tag) VALUES (?, ?, ?, ?)`, [
          context.namespace,
          instance.id,
          String(instance.data.title),
          context.config?.tag ?? null,
        ]);
      });
      calls.push({ namespace: context.namespace, schema: context.schema, version: context.version, before: context.before, config: context.config, visited });
      return hook?.(context);
    },
  });
}

function publish(engine: Engine, behaviors: Parameters<typeof itemDocument>[0], namespace?: string, description?: string): number {
  const document = itemDocument(behaviors);
  if (description !== undefined) {
    (document as { description?: string }).description = description;
  }
  engine.schemas.define(alice, document, { namespace });
  return engine.schemas.publish(alice, 'Item', { namespace }).version;
}

function seen(engine: Engine): Array<[string, string, string]> {
  return engine.storage
    .all('SELECT namespace, id, tag FROM bhv_test_recorder__seen ORDER BY rowid')
    .map((row) => [String(row.namespace), String(row.id), String(row.tag)]);
}

for (const driver of drivers) {
  describe(`afterConfigChange (${driver})`, () => {
    test('runs for a first version, a changed config and a removal, with both configs parsed, and not for an unchanged one', () => {
      const calls: Call[] = [];
      const engine = openBehaviorEngine({ driver, behaviors: [recorder(calls)], namespaces: { names: ['east'] } });
      const RECORDER = { name: 'test.Recorder', config: { tag: 'a' } };
      assert.equal(publish(engine, [RECORDER]), 1);
      assert.deepEqual(calls, [{ namespace: 'default', schema: 'Item', version: 1, before: undefined, config: { tag: 'a' }, visited: [] }]);

      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
      engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i2' });
      assert.equal(publish(engine, [RECORDER], undefined, 'the same config'), 2);
      assert.equal(calls.length, 1, 'an unchanged config runs nothing');

      assert.equal(publish(engine, [{ name: 'test.Recorder', config: { tag: 'b' } }]), 3);
      assert.equal(publish(engine, []), 4);
      assert.equal(publish(engine, [{ name: 'test.Recorder' }]), 5);
      assert.deepEqual(calls.slice(1), [
        { namespace: 'default', schema: 'Item', version: 3, before: { tag: 'a' }, config: { tag: 'b' }, visited: ['i1', 'i2'] },
        { namespace: 'default', schema: 'Item', version: 4, before: { tag: 'b' }, config: undefined, visited: ['i1', 'i2'] },
        { namespace: 'default', schema: 'Item', version: 5, before: undefined, config: { tag: 'none' }, visited: ['i1', 'i2'] },
      ]);

      // east's own Item is its own: a publish there covers east alone.
      publish(engine, [RECORDER], 'east');
      assert.deepEqual(calls.at(-1), { namespace: 'east', schema: 'Item', version: 1, before: undefined, config: { tag: 'a' }, visited: [] });
    });

    test('covers every namespace for a schema of the shared namespace, each with its own instances, in creation order', () => {
      const calls: Call[] = [];
      const engine = openBehaviorEngine({ driver, behaviors: [recorder(calls)], namespaces: { names: ['shared', 'east'], shared: 'shared' } });
      publish(engine, [], 'shared');
      const ids = Array.from({ length: 501 }, (_, n) => `e${String(n).padStart(3, '0')}`);
      for (const id of [...ids].reverse()) {
        engine.instances.create(alice, 'Item', { title: id }, { id, namespace: 'east' });
      }
      engine.instances.create(alice, 'Item', { title: 'Shared' }, { id: 's1', namespace: 'shared' });
      publish(engine, [{ name: 'test.Recorder', config: { tag: 'x' } }], 'shared');
      assert.deepEqual(
        calls.map((call) => [call.namespace, call.version, call.visited.length]),
        [
          ['default', 2, 0],
          ['shared', 2, 1],
          ['east', 2, 501],
        ]
      );
      assert.deepEqual(calls[2].visited, [...ids].reverse(), 'every instance once, 500 at a time, in creation order');
      assert.equal(seen(engine).length, 502);
    });

    test("its SQL reaches the behavior's own tables only, and a throw refuses the publish and takes its writes back", () => {
      for (const [hook, message, type] of [
        [(context: PublishContext<{ tag: string }>) => context.sql.run('DELETE FROM engine_instances'), /SQL refused: it names engine_instances/, BehaviorError],
        [() => Promise.resolve(), /afterConfigChange is synchronous \(D16\): it returned a promise/, BehaviorError],
        [
          () => {
            throw new Error('the rebuild failed');
          },
          /the rebuild failed/,
          Error,
        ],
      ] as const) {
        const calls: Call[] = [];
        const engine = openBehaviorEngine({ driver, behaviors: [recorder(calls, hook)] });
        publish(engine, []);
        engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
        engine.schemas.define(alice, itemDocument([{ name: 'test.Recorder' }]));
        assert.match(thrown(() => engine.schemas.publish(alice, 'Item'), type).message, message);
        assert.equal(calls.length, 1, 'the hook ran');
        assert.equal(engine.schemas.live(alice, 'Item')?.version, 1, 'no version was minted');
        assert.deepEqual(tablesOf(engine, 'bhv_test_recorder__'), [], 'its storage was rolled back with what it wrote');
        assert.deepEqual(engine.instances.get(alice, 'Item', 'i1')?.data, { title: 'Desk' });
      }
    });

    test('an implementation whose afterConfigChange is not a function does not register', () => {
      const engine = openBehaviorEngine({ driver, behaviors: [] });
      assert.throws(
        () => engine.behaviors.register({ declaration: { name: 'test.Odd' }, afterConfigChange: 'rebuild' } as never),
        /behavior test\.Odd cannot register: afterConfigChange is a function/
      );
    });
  });

  describe(`parseConfig's target (${driver})`, () => {
    test("gives the JSON Schema of each of the type's own fields, by JSON key", () => {
      const targets: unknown[] = [];
      const probe = defineBehavior({
        declaration: { name: 'test.Probe' },
        parseConfig(config, target) {
          targets.push(target.fieldSchemas);
          return config;
        },
      });
      const engine = openBehaviorEngine({ driver, behaviors: [probe] });
      engine.schemas.define(
        alice,
        itemDocument(
          [{ name: 'test.Probe' }],
          [
            { name: 'note', typeRef: { name: 'string' }, jsonTag: 'memo' },
            { name: 'email', typeRef: { name: 'Contact.Email' } },
            { name: 'count', typeRef: { name: 'Generic.Int64' } },
            { name: 'tags', typeRef: { name: 'string', isArray: true } },
          ]
        )
      );
      const schemas = targets.at(-1) as Record<string, { type: unknown; format?: string; items?: unknown }>;
      assert.deepEqual(Object.keys(schemas), ['title', 'memo', 'email', 'count', 'tags']);
      assert.equal(schemas.title.type, 'string');
      assert.deepEqual(schemas.memo.type, ['string', 'null']);
      assert.deepEqual([schemas.email.type, schemas.email.format], [['string', 'null'], 'email']);
      assert.deepEqual(schemas.count.type, ['integer', 'null']);
      assert.deepEqual(schemas.tags.type, ['array', 'null']);
      assert.ok(Object.isFrozen(schemas.title));
    });
  });
}
