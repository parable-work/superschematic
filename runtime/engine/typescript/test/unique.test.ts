// Unique fields (@unique, @key and a unique @index) and lookup: what
// define refuses, the writes a unique index refuses (a create, an update,
// a behavior's update() and two creates in one call), publish adding a
// unique field the stored instances break and dropping one, a namespace's
// own values, lookup by a value with a slash, and the value store, which
// keeps an indexed field inline in the row.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  IncompatibleChangeError,
  SchemaDocumentError,
  UniqueConflictError,
  defineBehavior,
  type AccessPolicy,
  type Engine,
  type EngineOptions,
  type FrozenJSON,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown, type Field } from './helpers.ts';

afterEach(cleanup);

const slugParams = { type: 'object', additionalProperties: false, required: ['slug'], properties: { slug: { type: 'string' } } } as const;

// test.Slugger changes an instance's slug through update(), and creates
// two instances with one slug in one call.
const slugger = defineBehavior({
  declaration: {
    name: 'test.Slugger',
    operations: [
      { name: 'setSlug', paramsSchema: slugParams, resultSchema: true, writes: true },
      { name: 'twins', scope: 'schema', paramsSchema: slugParams, resultSchema: true, writes: true },
      { name: 'twinsCaught', scope: 'schema', paramsSchema: slugParams, resultSchema: true, writes: true },
    ],
  },
  operations: {
    setSlug(context, params) {
      return context.update({ slug: params.slug } as FrozenJSON);
    },
  },
  schemaOperations: {
    twins(context, params) {
      context.instances.create('Model', { slug: params.slug } as FrozenJSON, { id: 'twin-1' });
      context.instances.create('Model', { slug: params.slug } as FrozenJSON, { id: 'twin-2' });
      return null;
    },
    twinsCaught(context, params) {
      context.instances.create('Model', { slug: params.slug } as FrozenJSON, { id: 'twin-1' });
      try {
        context.instances.create('Model', { slug: params.slug } as FrozenJSON, { id: 'twin-2' });
      } catch (error) {
        return (error as EngineError).code;
      }
      return 'created';
    },
  },
});

function modelDocument(fields: Field[] = [], extra: Record<string, unknown> = {}): Record<string, unknown> {
  const document = schemaDocument(
    'Model',
    [
      { name: 'slug', typeRef: { name: 'string' }, unique: true },
      { name: 'title', typeRef: { name: 'string' } },
      ...fields,
    ],
    extra
  ) as { types: { Model: Record<string, unknown> } };
  return document;
}

function withBehaviors(document: Record<string, unknown>, behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  (document as { types: { Model: Record<string, unknown> } }).types.Model.behaviors = behaviors;
  return document;
}

function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
  engine.schemas.define(alice, document, { namespace });
  engine.schemas.publish(alice, String(document.name), { namespace });
}

function open(options: Partial<EngineOptions> = {}): Engine {
  return openTestEngine({ metaSchema: openMetaSchema(), behaviors: [slugger], ...options });
}

function uniqueIndexes(engine: Engine): string[] {
  return engine.storage
    .all("SELECT name FROM sqlite_master WHERE type = 'index' AND name LIKE 'engine_unique_%' ORDER BY name")
    .map((row) => String(row.name));
}

for (const driver of drivers) {
  describe(`unique fields (${driver})`, () => {
    test('define refuses @unique, @key and @index on a field with no one value, or on another type', () => {
      const engine = open({ driver });
      const refused = (document: Record<string, unknown>, pattern: RegExp): void => {
        const error = thrown(() => engine.schemas.define(alice, document), SchemaDocumentError);
        assert.match(error.message, pattern);
      };
      refused(
        schemaDocument('Model', [{ name: 'tags', typeRef: { name: 'string', isArray: true }, unique: true }]),
        /\/types\/Model\/fields\/0\/unique: field Model.tags is @unique, and a unique field holds a string, a number or a boolean/
      );
      refused(
        schemaDocument('Model', [{ name: 'body', typeRef: { name: 'Generic.JSON' }, key: true }]),
        /field Model.body is @key, and a unique field holds a string, a number or a boolean/
      );
      refused(
        schemaDocument('Model', [{ name: 'address', typeRef: { name: 'Address' }, unique: true }], {
          types: { Address: { name: 'Address', role: 'EmbeddedStruct', fields: [{ name: 'city', typeRef: { name: 'string' }, unique: true }] } },
        }),
        /field Address.city is @unique, and the engine enforces @unique on the instance type's own fields \(Model\) only/
      );
      const indexed = (indexes: unknown[]): Record<string, unknown> => {
        const document = modelDocument([{ name: 'tags', typeRef: { name: 'string', isArray: true } }]) as { types: { Model: Record<string, unknown> } };
        document.types.Model.indexes = indexes;
        return document as unknown as Record<string, unknown>;
      };
      refused(indexed([{ keys: ['slug', 'owner'], unique: true }]), /\/types\/Model\/indexes\/0\/keys\/1: the index names owner, which is not a field of Model/);
      refused(indexed([{ keys: ['tags'] }]), /field Model.tags holds no string, number or boolean, and an index compares one value per field/);
      refused(indexed([{ keys: [] }]), /an index lists at least one field/);
      refused(
        schemaDocument('Model', [{ name: 'slug', typeRef: { name: 'string' }, unique: true, jsonTag: 'a"b' }]),
        /holds a quote, a backslash or a control character/
      );
      // A field of the instance type that holds a string, a number, a
      // boolean or an enum's value takes them all.
      engine.schemas.define(
        alice,
        schemaDocument(
          'Model',
          [
            { name: 'slug', typeRef: { name: 'string' }, unique: true },
            { name: 'rank', typeRef: { name: 'Generic.Int64' }, key: true },
            { name: 'live', typeRef: { name: 'boolean' }, unique: true },
            { name: 'kind', typeRef: { name: 'Kind' }, unique: true },
          ],
          { enums: { Kind: { name: 'Kind', values: [{ name: 'A' }, { name: 'B' }] } } }
        )
      );
    });

    test('a create or an update that repeats a unique value is a conflict; absent and null are no value', () => {
      const engine = open({ driver });
      publish(engine, modelDocument());
      engine.instances.create(alice, 'Model', { slug: 'openai/gpt-5', title: 'A' }, { id: 'a' });
      const error = thrown(() => engine.instances.create(alice, 'Model', { slug: 'openai/gpt-5', title: 'B' }, { id: 'b' }), UniqueConflictError);
      assert.equal(error.code, 'conflict');
      assert.deepEqual(error.fields, ['slug']);
      assert.match(error.message, /another instance holds slug "openai\/gpt-5", and it is unique/);
      assert.equal(engine.instances.get(alice, 'Model', 'b'), undefined);
      // Any number of instances lack it, absent or null.
      engine.instances.create(alice, 'Model', { title: 'C' }, { id: 'c' });
      engine.instances.create(alice, 'Model', { title: 'D' }, { id: 'd' });
      engine.instances.create(alice, 'Model', { slug: null, title: 'E' } as Record<string, unknown>, { id: 'e' });
      // An update to a value another holds is refused and writes nothing;
      // an update that keeps its own value, or removes it, is not.
      const before = engine.events.head();
      thrown(() => engine.instances.update(alice, 'Model', 'c', { slug: 'openai/gpt-5' }), UniqueConflictError);
      assert.equal(engine.events.head(), before);
      assert.equal(engine.instances.get(alice, 'Model', 'c')?.data.slug, undefined);
      engine.instances.update(alice, 'Model', 'a', { slug: 'openai/gpt-5', title: 'A2' });
      engine.instances.update(alice, 'Model', 'a', { slug: null });
      engine.instances.update(alice, 'Model', 'c', { slug: 'openai/gpt-5' });
      // A delete frees the value.
      engine.instances.delete(alice, 'Model', 'c');
      engine.instances.create(alice, 'Model', { slug: 'openai/gpt-5' }, { id: 'f' });
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: 'openai/gpt-5' })?.id, 'f');
    });

    test("a behavior's update() and two creates in one call meet the index too", () => {
      const engine = open({ driver });
      publish(engine, withBehaviors(modelDocument(), [{ name: 'test.Slugger' }]));
      engine.instances.create(alice, 'Model', { slug: 'taken' }, { id: 'a' });
      engine.instances.create(alice, 'Model', { slug: 'free' }, { id: 'b' });
      const seq = engine.instances.get(alice, 'Model', 'b')?.seq;
      thrown(() => engine.instances.invoke(alice, 'Model', 'b', 'setSlug', { slug: 'taken' }), UniqueConflictError);
      assert.deepEqual([engine.instances.get(alice, 'Model', 'b')?.data.slug, engine.instances.get(alice, 'Model', 'b')?.seq], ['free', seq]);
      // The second create of one call is refused, and takes the call back.
      const error = thrown(() => engine.instances.invokeSchema(alice, 'Model', 'twins', { slug: 'twin' }), UniqueConflictError);
      assert.deepEqual(error.fields, ['slug']);
      assert.equal(engine.instances.get(alice, 'Model', 'twin-1'), undefined);
      // Caught, it takes back only its own savepoint.
      assert.equal(engine.instances.invokeSchema(alice, 'Model', 'twinsCaught', { slug: 'twin' }), 'conflict');
      assert.deepEqual(
        engine.instances.list(alice, 'Model', { where: { slug: 'twin' } }).items.map((item) => item.id),
        ['twin-1']
      );
    });

    test('a unique index is per namespace, and a shared schema holds each namespace to its own', () => {
      const engine = open({ driver, namespaces: { names: ['east', 'west', 'common'], shared: 'common' } });
      publish(engine, modelDocument(), 'common');
      engine.instances.create(alice, 'Model', { slug: 'same' }, { namespace: 'east', id: 'e1' });
      engine.instances.create(alice, 'Model', { slug: 'same' }, { namespace: 'west', id: 'w1' });
      thrown(() => engine.instances.create(alice, 'Model', { slug: 'same' }, { namespace: 'east', id: 'e2' }), UniqueConflictError);
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: 'same' }, { namespace: 'west' })?.id, 'w1');
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: 'same' }, { namespace: 'east' })?.id, 'e1');
    });

    test('@key is unique, and a unique @index makes its fields unique together', () => {
      const engine = open({ driver });
      const document = schemaDocument('Model', [
        { name: 'code', typeRef: { name: 'string' }, key: true },
        { name: 'source', typeRef: { name: 'string' } },
        { name: 'externalId', typeRef: { name: 'Generic.Int64' } },
      ]) as { types: { Model: Record<string, unknown> } };
      document.types.Model.indexes = [{ keys: ['source', 'externalId'], unique: true }, { keys: ['source'] }];
      publish(engine, document as unknown as Record<string, unknown>);
      engine.instances.create(alice, 'Model', { code: 'x', source: 'crm', externalId: 1 }, { id: 'a' });
      engine.instances.create(alice, 'Model', { code: 'y', source: 'crm', externalId: 2 }, { id: 'b' });
      engine.instances.create(alice, 'Model', { code: 'z', source: 'erp', externalId: 1 }, { id: 'c' });
      // One key missing leaves the instance out of the index.
      engine.instances.create(alice, 'Model', { code: 'w', source: 'crm' }, { id: 'd' });
      engine.instances.create(alice, 'Model', { code: 'v', source: 'crm' }, { id: 'e' });
      assert.deepEqual(thrown(() => engine.instances.create(alice, 'Model', { code: 'x' }), UniqueConflictError).fields, ['code']);
      const error = thrown(() => engine.instances.create(alice, 'Model', { code: 'u', source: 'crm', externalId: 2 }), UniqueConflictError);
      assert.deepEqual(error.fields, ['source', 'externalId']);
      assert.match(error.message, /another instance holds source "crm" and externalId 2, and together they are unique/);
      assert.equal(engine.instances.lookup(alice, 'Model', { externalId: 1, source: 'erp' })?.id, 'c');
      assert.equal(engine.instances.lookup(alice, 'Model', { code: 'y' })?.id, 'b');
      assert.match(
        thrown(() => engine.instances.lookup(alice, 'Model', { source: 'crm' }), EngineError).message,
        /a lookup key names the fields of one unique index of Model \(code; source and externalId\), not source/
      );
      // Two unique indexes and a plain one, which the unique one's keys do not repeat.
      assert.equal(uniqueIndexes(engine).length, 2);
      assert.equal(engine.storage.all("SELECT name FROM sqlite_master WHERE name LIKE 'engine_index_%'").length, 1);
    });

    test('the describe document carries lookup, its key a closed object per unique index, and guidance that names the unique fields', () => {
      const engine = open({ driver });
      const document = modelDocument([
        { name: 'source', typeRef: { name: 'string' } },
        { name: 'externalId', typeRef: { name: 'Generic.Int64' } },
      ]) as { types: { Model: Record<string, unknown> } };
      document.types.Model.indexes = [{ keys: ['source', 'externalId'], unique: true }];
      publish(engine, document as unknown as Record<string, unknown>);
      const described = engine.tools.describe(alice, 'Model');
      assert.deepEqual(
        described.operations.map((operation) => [operation.name, operation.tool, operation.writes]),
        [
          ['create', 'model.create', true],
          ['get', 'model.get', false],
          ['list', 'model.list', false],
          ['update', 'model.update', true],
          ['delete', 'model.delete', true],
          ['lookup', 'model.lookup', false],
        ]
      );
      const lookup = described.operations[5];
      const key = (lookup.params.properties as Record<string, any>).key;
      assert.deepEqual(lookup.params.required, ['key']);
      assert.deepEqual(
        key.oneOf.map((branch: { required: string[]; additionalProperties: boolean }) => [branch.required, branch.additionalProperties]),
        [
          [['slug'], false],
          [['source', 'externalId'], false],
        ]
      );
      assert.equal(key.oneOf[1].properties.externalId.type, 'integer');
      assert.equal(
        lookup.guidance.useWhen,
        'Use when you know the slug or source and externalId of the Model to read: key names the fields of one unique index and the value, as { "slug": ... }; a value may hold a slash.'
      );
      assert.match(described.operations[0].guidance.useWhen, /slug is unique; source and externalId together are unique: a value another Model holds is refused \(conflict\)\./);
      assert.match(described.operations[0].guidance.doNotUseWhen, /Do not create a Model whose slug or source and externalId another holds; call lookup to find it\./);
      assert.match(described.operations[2].guidance.useWhen, /where keeps the ones whose fields hold the values it gives, a list of values meaning any of them: slug, title, source or externalId\./);
      // A schema without a unique field has no lookup.
      publish(engine, schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]));
      assert.deepEqual(engine.tools.describe(alice, 'Note').operations.map((operation) => operation.name), ['create', 'get', 'list', 'update', 'delete']);
      assert.equal(engine.tools.manifest(alice).tools.some((tool) => tool.name === 'note.lookup'), false);
    });

    test('lookup reads the instance a value names, a slash and all, as a reader of the schema', () => {
      const deny: AccessPolicy = ({ principal, action }) => principal.subject === 'alice' || action !== 'read';
      const engine = open({ driver, policy: deny });
      publish(engine, modelDocument([{ name: 'rank', typeRef: { name: 'number' } }]));
      engine.instances.create(alice, 'Model', { slug: 'models/openai/gpt-5', title: 'GPT', rank: 1 }, { id: 'a' });
      const found = engine.instances.lookup(alice, 'Model', { slug: 'models/openai/gpt-5' });
      assert.deepEqual([found?.id, found?.data], ['a', { slug: 'models/openai/gpt-5', title: 'GPT', rank: 1 }]);
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: 'models/openai' }), undefined);
      const bob = { subject: 'bob', permissions: [] };
      assert.equal(thrown(() => engine.instances.lookup(bob, 'Model', { slug: 'models/openai/gpt-5' }), EngineError).code, 'forbidden');
      for (const [key, pattern] of [
        [{ title: 'GPT' }, /names the fields of one unique index of Model \(slug\), not title/],
        [{ slug: 1 }, /key.slug is a string, not 1/],
        [{ slug: null }, /key.slug is a string, not null/],
        [{ slug: 'a', title: 'b' }, /not slug, title/],
        ['a', /a lookup key is a JSON object/],
      ] as const) {
        const error = thrown(() => engine.instances.lookup(alice, 'Model', key), EngineError);
        assert.equal(error.code, 'invalid_argument');
        assert.match(error.message, pattern);
      }
      publish(engine, schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]));
      assert.match(thrown(() => engine.instances.lookup(alice, 'Note', { body: 'x' }), EngineError).message, /Note has no unique field/);
    });

    test('publish refuses a unique field the stored instances break, and creates its index once they do not', () => {
      const engine = open({ driver });
      const plain = schemaDocument('Model', [
        { name: 'slug', typeRef: { name: 'string' } },
        { name: 'title', typeRef: { name: 'string' } },
      ]);
      publish(engine, plain);
      engine.instances.create(alice, 'Model', { slug: 'dup', title: 'A' }, { id: 'a' });
      engine.instances.create(alice, 'Model', { slug: 'dup', title: 'B' }, { id: 'b' });
      engine.instances.create(alice, 'Model', { title: 'C' }, { id: 'c' });
      // define refuses it while the instances share a value.
      const refused = thrown(() => engine.schemas.define(alice, modelDocument()), IncompatibleChangeError);
      assert.deepEqual(refused.changes, [{ path: 'Model.slug', message: 'field Model.slug becomes unique, and 2 instances in namespace default hold slug "dup"' }]);
      assert.match(refused.message, /Make the values distinct first/);
      // A draft defined before they did is refused at publish, which
      // leaves the live version and the file as they were.
      engine.instances.update(alice, 'Model', 'b', { slug: 'other' });
      engine.schemas.define(alice, modelDocument());
      engine.instances.update(alice, 'Model', 'b', { slug: 'dup' });
      thrown(() => engine.schemas.publish(alice, 'Model'), IncompatibleChangeError);
      assert.equal(engine.schemas.live(alice, 'Model')?.version, 1);
      assert.deepEqual(uniqueIndexes(engine), []);
      engine.instances.update(alice, 'Model', 'b', { slug: 'other' });
      assert.equal(engine.schemas.publish(alice, 'Model').version, 2);
      assert.equal(uniqueIndexes(engine).length, 1);
      thrown(() => engine.instances.update(alice, 'Model', 'b', { slug: 'dup' }), UniqueConflictError);
      // A version without the field's @unique drops the index.
      publish(engine, plain);
      assert.deepEqual(uniqueIndexes(engine), []);
      engine.instances.update(alice, 'Model', 'b', { slug: 'dup' });
      assert.equal(engine.instances.list(alice, 'Model', { where: { slug: 'dup' } }).items.length, 2);
      assert.match(thrown(() => engine.instances.lookup(alice, 'Model', { slug: 'dup' }), EngineError).message, /no unique field/);
    });

    test('an indexed field stays inline in the row whatever its length; a row that held one by hash takes it back at publish', () => {
      const engine = open({ driver, values: { thresholdBytes: 1024 } });
      const long = (seed: string): string => `${seed}/${'x'.repeat(2000)}`;
      publish(engine, schemaDocument('Model', [
        { name: 'slug', typeRef: { name: 'string' } },
        { name: 'body', typeRef: { name: 'string' } },
      ]));
      engine.instances.create(alice, 'Model', { slug: long('a'), body: long('body') }, { id: 'a' });
      const row = (): { data: Record<string, unknown>; refs: unknown } => {
        const stored = engine.storage.get("SELECT data, value_refs FROM engine_instances WHERE id = 'a'");
        return { data: JSON.parse(String(stored?.data)) as Record<string, unknown>, refs: stored?.value_refs };
      };
      assert.equal(row().refs, '["/slug","/body"]');
      // A filter on a field the row holds by hash matches it by its hash.
      assert.deepEqual(engine.instances.list(alice, 'Model', { where: { slug: long('a') } }).items.map((item) => item.id), ['a']);
      const holders = (): number => Number(engine.storage.get("SELECT COUNT(*) AS n FROM engine_payload_holders WHERE holder = 'instance'")?.n);
      assert.equal(holders(), 2);
      publish(engine, modelDocument([{ name: 'body', typeRef: { name: 'string' } }]));
      assert.deepEqual([row().refs, row().data.slug], ['["/body"]', long('a')]);
      assert.equal(holders(), 1);
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: long('a') })?.id, 'a');
      // A create keeps it inline; its event stores it by hash.
      const created = engine.instances.create(alice, 'Model', { slug: long('b'), body: long('b') }, { id: 'b' });
      assert.equal(engine.storage.get("SELECT value_refs FROM engine_instances WHERE id = 'b'")?.value_refs, '["/body"]');
      assert.deepEqual(engine.events.read(alice, { schema: 'Model', instanceId: 'b' }).events[0].valueRefs, ['/slug', '/body']);
      assert.equal(engine.instances.lookup(alice, 'Model', { slug: long('b') })?.id, created.id);
      thrown(() => engine.instances.create(alice, 'Model', { slug: long('b') }), UniqueConflictError);
    });
  });
}
