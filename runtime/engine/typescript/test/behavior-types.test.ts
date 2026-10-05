// What a behavior learns of the schema's other types, and checks values
// against (D32, What D16 gains). parseConfig reads them through
// ConfigTarget.types, with each field's JSON key, type, kind, list depth
// and whether it is optional; a type it reads counts as reachable from the
// instance type for the version, so the document checks cover its fields
// and a new version keeps them as the compatibility rule keeps the
// instance type's. A context's validate(type, value) holds a value to such
// a type with the version's validator, as a nested value of the type is
// held in an instance.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorConfigError,
  BehaviorError,
  IncompatibleChangeError,
  SchemaDocumentError,
  defineBehavior,
  type ConfigType,
  type ConfigTypes,
  type Engine,
  type FrozenJSON,
  type ValidationIssue,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, clone, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

/** What each parseConfig of test.Kinds was given: whether other schemas were in reach, and the type names. */
const targets: Array<{ schemas: boolean; names: readonly string[] }> = [];
/** The ConfigTarget.types test.Kinds last read through, kept past its parseConfig. */
let kept: ConfigTypes | undefined;
/** What test.Kinds' afterConfigChange checked with the version being published. */
const published: Array<readonly ValidationIssue[]> = [];
/** Types test.Kinds' parseConfig also reads while other schemas are in reach, whatever its config says. */
const extra = { withSchemas: [] as string[] };

afterEach(() => {
  targets.length = 0;
  published.length = 0;
  kept = undefined;
  extra.withSchemas = [];
  cleanup();
});

interface KindsConfig {
  /** The types parseConfig reads, as Branches reads its kinds' types. */
  readonly kinds: Readonly<Record<string, ConfigType>>;
  /** A value afterConfigChange checks against a type, when the config gives one. */
  readonly probe?: { readonly type: string; readonly value: unknown };
}

const checkParams = {
  type: 'object',
  additionalProperties: false,
  required: ['type', 'value'],
  properties: { type: { type: 'string' }, value: {} },
} as const;

// test.Kinds reads the types its config names through ConfigTarget.types
// and keeps each one's fields in its parsed config; its operations check
// a value against a type with validate, and read again through the
// ConfigTarget.types its parseConfig kept.
const kinds = defineBehavior<KindsConfig>({
  declaration: {
    name: 'test.Kinds',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      properties: {
        types: { type: 'array', items: { type: 'string' } },
        withSchemas: { type: 'array', items: { type: 'string' } },
        withoutSchemas: { type: 'array', items: { type: 'string' } },
        probe: { type: 'object', additionalProperties: false, required: ['type', 'value'], properties: { type: { type: 'string' }, value: {} } },
      },
    },
    operations: [
      { name: 'kinds', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true },
      { name: 'check', paramsSchema: checkParams, resultSchema: true },
      { name: 'checkSchema', scope: 'schema', paramsSchema: checkParams, resultSchema: true },
      { name: 'readLate', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true },
    ],
  },
  parseConfig(config, target) {
    const raw = config as { types?: string[]; withSchemas?: string[]; withoutSchemas?: string[]; probe?: { type: string; value: unknown } };
    targets.push({ schemas: target.schemas !== undefined, names: target.types.names });
    kept = target.types;
    const read: Record<string, ConfigType> = {};
    const reading = [
      ...(raw.types ?? []),
      ...((target.schemas === undefined ? raw.withoutSchemas : raw.withSchemas) ?? []),
      ...(target.schemas === undefined ? [] : extra.withSchemas),
    ];
    for (const name of reading) {
      const type = target.types.get(name);
      if (type === undefined) {
        throw new BehaviorConfigError(`${name} is not a type of ${target.schema} besides ${target.type} (its types: ${target.types.names.join(', ')})`);
      }
      read[name] = type;
    }
    return raw.probe === undefined ? { kinds: read } : { kinds: read, probe: raw.probe };
  },
  configChange: () => undefined,
  afterConfigChange(context) {
    const probe = context.config?.probe;
    if (probe !== undefined) {
      published.push(context.validate(probe.type, probe.value));
    }
  },
  operations: {
    kinds: (context) => context.config.kinds,
    check: (context, params) => context.validate(params.type as string, params.value),
    readLate: () => (kept as ConfigTypes).get('Recipe'),
  },
  schemaOperations: {
    checkSchema: (context, params) => context.validate(params.type as string, params.value),
  },
});

type Fields = Array<Record<string, unknown> & { name: string; typeRef: Record<string, unknown> }>;
interface Doc {
  types: Record<string, { name: string; behaviors?: unknown[]; fields: Fields }>;
}

// A Kitchen whose instance type reaches Meta through a field, beside types
// no field reaches: a Recipe with a field of every kind and its Steps, a
// Loose type with a map field, an Odd one with a field of a type the
// document lacks, a Wrapper around a Loose, and a Spare.
function kitchen(config: Record<string, unknown> = {}): Doc {
  const document = schemaDocument(
    'Kitchen',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'meta', typeRef: { name: 'Meta' } },
    ],
    {
      enums: { Unit: { name: 'Unit', values: [{ name: 'GRAM', serializedAs: 'g' }, { name: 'CUP', serializedAs: 'cup' }] } },
      types: {
        Meta: { name: 'Meta', role: 'EmbeddedStruct', fields: [{ name: 'source', typeRef: { name: 'string' }, required: true }] },
        Recipe: {
          name: 'Recipe',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'title', typeRef: { name: 'string' }, required: true },
            { name: 'servings', typeRef: { name: 'Int' }, jsonTag: 'serves' },
            { name: 'contact', typeRef: { name: 'Contact.Email' } },
            { name: 'unit', typeRef: { name: 'Unit' } },
            { name: 'steps', typeRef: { name: 'Step', isArray: true } },
            { name: 'grid', typeRef: { name: 'number', isArray: true, isArrayOfArrays: true } },
          ],
        },
        Step: {
          name: 'Step',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'name', typeRef: { name: 'string' }, required: true },
            { name: 'minutes', typeRef: { name: 'number' }, validateMin: 1 },
          ],
        },
        Loose: { name: 'Loose', role: 'EmbeddedStruct', fields: [{ name: 'extras', typeRef: { name: 'string', isMap: true } }] },
        Odd: { name: 'Odd', role: 'EmbeddedStruct', fields: [{ name: 'part', typeRef: { name: 'Nope' } }] },
        Wrapper: { name: 'Wrapper', role: 'EmbeddedStruct', fields: [{ name: 'inner', typeRef: { name: 'Loose' } }] },
        Spare: { name: 'Spare', role: 'EmbeddedStruct', fields: [{ name: 'note', typeRef: { name: 'string' } }] },
      },
    }
  ) as unknown as Doc;
  document.types.Kitchen.behaviors = [{ name: 'test.Kinds', config }];
  return document;
}

function setKinds(document: Doc, config: Record<string, unknown>): Doc {
  document.types.Kitchen.behaviors = [{ name: 'test.Kinds', config }];
  return document;
}

function publish(engine: Engine, document: Doc): void {
  engine.schemas.define(alice, document as unknown as Record<string, unknown>);
  engine.schemas.publish(alice, 'Kitchen');
}

function define(engine: Engine, document: Doc): unknown {
  return engine.schemas.define(alice, document as unknown as Record<string, unknown>);
}

for (const driver of drivers) {
  function open(): Engine {
    return openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [kinds] });
  }

  describe(`ConfigTarget.types (${driver})`, () => {
    test("parseConfig reads each other type's fields: JSON key, type, kind, list depth and whether it is optional", () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      engine.instances.create(alice, 'Kitchen', { title: 'Home' }, { id: 'k1' });
      const read = engine.instances.invoke(alice, 'Kitchen', 'k1', 'kinds', {}) as Record<string, ConfigType>;
      assert.deepEqual(read, {
        Recipe: {
          name: 'Recipe',
          fields: [
            { key: 'title', type: 'string', kind: 'primitive', depth: 0, optional: false },
            { key: 'serves', type: 'Int', kind: 'primitive', depth: 0, optional: true },
            { key: 'contact', type: 'Contact.Email', kind: 'scalar', depth: 0, optional: true },
            { key: 'unit', type: 'Unit', kind: 'enum', depth: 0, optional: true },
            { key: 'steps', type: 'Step', kind: 'type', depth: 1, optional: true },
            { key: 'grid', type: 'number', kind: 'primitive', depth: 2, optional: true },
          ],
        },
      });
      // The names are the types besides the instance type, sorted. The
      // operation's config came from a version composed again to run it,
      // with no other schema in reach and the types there all the same.
      assert.deepEqual(targets.at(-1), { schemas: false, names: ['Loose', 'Meta', 'Odd', 'Recipe', 'Spare', 'Step', 'Wrapper'] });
      assert.ok(targets.some((target) => target.schemas));
    });

    test('the instance type, an enum and a name the document lacks are no type to read', () => {
      const engine = open();
      for (const name of ['Kitchen', 'Unit', 'Nope']) {
        const refused = thrown(() => define(engine, kitchen({ types: [name] })), SchemaDocumentError);
        assert.deepEqual(
          refused.issues.map((issue) => issue.path),
          ['/types/Kitchen/behaviors/0/config']
        );
        assert.match(refused.issues[0].message, new RegExp(`behavior test\\.Kinds config: ${name} is not a type of Kitchen besides Kitchen`));
      }
    });

    test('a type parseConfig reads gets the document checks, and so do the types it reaches; a type nothing reads is free', () => {
      const engine = open();
      // Loose's map and Odd's missing type pass while nothing reads them.
      define(engine, kitchen({ types: ['Recipe', 'Spare'] }));
      assert.deepEqual(thrown(() => define(engine, kitchen({ types: ['Loose'] })), SchemaDocumentError).issues, [
        { path: '/types/Loose/fields/0/typeRef', message: 'field Loose.extras is a map, which the schema runtime does not validate' },
      ]);
      assert.deepEqual(thrown(() => define(engine, kitchen({ types: ['Odd'] })), SchemaDocumentError).issues, [
        {
          path: '/types/Odd/fields/0/typeRef',
          message: 'field Odd.part has type Nope, which is not a primitive, a scalar, or an enum or type of this schema',
        },
      ]);
      // A type read reaches Loose through its field.
      assert.deepEqual(
        thrown(() => define(engine, kitchen({ types: ['Wrapper'] })), SchemaDocumentError).issues.map((issue) => issue.path),
        ['/types/Loose/fields/0/typeRef']
      );
    });

    test('what parseConfig reads may not depend on the other schemas: a define or publish whose reads differ without them is refused', () => {
      const engine = open();
      const at = '/types/Kitchen/behaviors/0/config';
      for (const [config, reads] of [
        [{ types: ['Spare'], withSchemas: ['Recipe'] }, "Recipe through ConfigTarget.types only while other schemas"],
        [{ withoutSchemas: ['Recipe', 'Step'] }, 'Recipe, Step through ConfigTarget.types only when no other schemas'],
      ] as const) {
        const refused = thrown(() => define(engine, kitchen(config)), SchemaDocumentError);
        assert.deepEqual(
          refused.issues.map((issue) => issue.path),
          [at]
        );
        assert.match(refused.issues[0].message, new RegExp(`^type Kitchen: behavior test\.Kinds config: parseConfig reads ${reads}`));
      }
      // The same types with and without them stand.
      define(engine, kitchen({ types: ['Recipe'], withSchemas: ['Spare'], withoutSchemas: ['Spare'] }));
      // A publish composes the draft again, with the other schemas in reach
      // and without, and refuses it as a define does.
      define(engine, kitchen({ types: ['Recipe'] }));
      extra.withSchemas = ['Spare'];
      const atPublish = thrown(() => engine.schemas.publish(alice, 'Kitchen'), SchemaDocumentError);
      assert.deepEqual(
        atPublish.issues.map((issue) => issue.path),
        [at]
      );
      assert.match(atPublish.issues[0].message, /parseConfig reads Spare through ConfigTarget\.types only while other schemas/);
      assert.equal(engine.schemas.live(alice, 'Kitchen'), undefined);
      extra.withSchemas = [];
      engine.schemas.publish(alice, 'Kitchen');
    });

    test('a read is recorded only while parseConfig runs: a read after it returns is a BehaviorError', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      engine.instances.create(alice, 'Kitchen', { title: 'Home' }, { id: 'k1' });
      assert.match(
        thrown(() => engine.instances.invoke(alice, 'Kitchen', 'k1', 'readLate', {}), BehaviorError).message,
        /behavior test\.Kinds: ConfigTarget\.types\.get reads a type only while parseConfig runs/
      );
    });
  });

  describe(`the compatibility rule over the types a behavior reads (${driver})`, () => {
    function next(engine: Engine, change: (doc: Doc) => void, config: Record<string, unknown> = { types: ['Recipe'] }): () => unknown {
      const document = setKinds(clone(kitchen()), config);
      change(document);
      return () => define(engine, document);
    }
    const field = (doc: Doc, type: string, name: string) => doc.types[type].fields.find((candidate) => candidate.name === name) as Record<string, unknown>;

    test('a new version keeps the fields of a type the live version read, and of the types it reaches, as it keeps the instance type', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      const required = thrown(
        next(engine, (doc) => doc.types.Recipe.fields.push({ name: 'cuisine', typeRef: { name: 'string' }, required: true })),
        IncompatibleChangeError
      );
      assert.deepEqual(required.changes, [{ path: 'Recipe.cuisine', message: 'field Recipe.cuisine is added as required' }]);
      const narrower = thrown(
        next(engine, (doc) => (field(doc, 'Step', 'minutes').validateMin = 2)),
        IncompatibleChangeError
      );
      assert.deepEqual(narrower.changes, [{ path: 'Step.minutes', message: 'field Step.minutes raises min from 1 to 2' }]);
      // An optional field is a compatible change, as on the instance type.
      next(engine, (doc) => doc.types.Recipe.fields.push({ name: 'cuisine', typeRef: { name: 'string' } }))();
    });

    test('the live version holds the types it read even where the new version reads them no longer', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      const removed = thrown(
        next(engine, (doc) => {
          delete (doc.types as Record<string, unknown>).Recipe;
        }, { types: [] }),
        IncompatibleChangeError
      );
      assert.deepEqual(removed.changes, [{ path: 'Recipe', message: 'type Recipe is removed' }]);
      const changed = thrown(
        next(engine, (doc) => (field(doc, 'Recipe', 'title').typeRef = { name: 'number' }), { types: [] }),
        IncompatibleChangeError
      );
      assert.deepEqual(changed.changes, [{ path: 'Recipe.title', message: 'field Recipe.title changes type from string to number' }]);
    });

    test('a type no live behavior read changes freely, even one the new version starts to read', () => {
      const engine = open();
      publish(engine, kitchen({ types: [] }));
      next(engine, (doc) => doc.types.Recipe.fields.push({ name: 'cuisine', typeRef: { name: 'string' }, required: true }), { types: [] })();
      next(engine, (doc) => (field(doc, 'Recipe', 'title').typeRef = { name: 'number' }), { types: ['Recipe'] })();
      next(engine, (doc) => {
        delete (doc.types as Record<string, unknown>).Spare;
      }, { types: [] })();
    });
  });

  describe(`a context's validate(type, value) (${driver})`, () => {
    const recipe = {
      title: 'Bread',
      serves: 4,
      contact: 'baker@example.com',
      unit: 'g',
      steps: [{ name: 'knead', minutes: 10 }],
      grid: [[1, 2], [3]],
    };
    const rules = (issues: unknown) => (issues as ValidationIssue[]).map((issue) => [issue.path, issue.rule]).sort();

    test('holds a value to a type, as a nested value: each field, its kind and list depth, and no undeclared key at any depth', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      engine.instances.create(alice, 'Kitchen', { title: 'Home' }, { id: 'k1' });
      const check = (type: string, value: unknown) => engine.instances.invoke(alice, 'Kitchen', 'k1', 'check', { type, value } as FrozenJSON);
      assert.deepEqual(check('Recipe', recipe), []);
      assert.deepEqual(
        rules(
          check('Recipe', {
            title: 7,
            serves: 'four',
            contact: 'not an email',
            unit: 'ounce',
            steps: [{ minutes: 0, extra: true }, 'knead'],
            grid: [1],
            other: 1,
          })
        ),
        [
          ['contact', 'pattern'],
          ['grid[0]', 'type'],
          ['other', 'unknown'],
          ['serves', 'type'],
          ['steps[0].extra', 'unknown'],
          ['steps[0].minutes', 'min'],
          ['steps[0].name', 'required'],
          ['steps[1]', 'type'],
          ['title', 'type'],
          ['unit', 'enum'],
        ]
      );
      assert.deepEqual(check('Recipe', 'Bread'), [{ path: '', rule: 'type', message: 'a Recipe is a JSON object' }]);
      assert.deepEqual(rules(check('Step', { name: 'rest', later: { at: 1 } })), [['later', 'unknown']]);
    });

    test('reaches a type its fields reach, one parseConfig read and the types those reach; any other is a BehaviorError', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'] }));
      engine.instances.create(alice, 'Kitchen', { title: 'Home' }, { id: 'k1' });
      const check = (type: string, value: unknown) => engine.instances.invoke(alice, 'Kitchen', 'k1', 'check', { type, value } as FrozenJSON);
      assert.deepEqual(rules(check('Meta', {})), [['source', 'required']]);
      for (const type of ['Spare', 'Kitchen', 'Unit', 'Nope']) {
        assert.match(
          thrown(() => check(type, {}), BehaviorError).message,
          new RegExp(`behavior test\\.Kinds: validate: ${type} is not a type the version checks besides Kitchen.*\\(Meta, Recipe, Step\\)`)
        );
      }
    });

    test('a schema-level operation and afterConfigChange check with it too, the latter with the version being published', () => {
      const engine = open();
      publish(engine, kitchen({ types: ['Recipe'], probe: { type: 'Recipe', value: { title: 1 } } }));
      assert.deepEqual(rules(published[0]), [['title', 'type']]);
      assert.deepEqual(rules(engine.instances.invokeSchema(alice, 'Kitchen', 'checkSchema', { type: 'Step', value: { name: 1 } })), [['name', 'type']]);
      // The next version makes Recipe's cuisine an optional field, which
      // the hook's value may then hold.
      const document = kitchen({ types: ['Recipe'], probe: { type: 'Recipe', value: { title: 'Bread', cuisine: 'rye' } } });
      document.types.Recipe.fields.push({ name: 'cuisine', typeRef: { name: 'string' } });
      publish(engine, document);
      assert.deepEqual(published.at(-1), []);
    });
  });
}
