// The schema registry: what a schema document must be, define and publish,
// the reads, and the validator of a version.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import { loadSchemaFile } from '@superschematic/schema-runtime';

import { EngineError, SchemaDocumentError, type Engine } from '../dist/index.js';
import { objectScalarIssue } from '../dist/registry/document.js';
import { alice, cleanup, clone, drivers, openTestEngine, orderDocument, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

// The meta-schema of a binary that registers two behaviors, acme.Audited
// and acme.Stock, which the Go data-form reader's parity corpus uses.
const behaviorMetaSchema = readFileSync(
  new URL('../../../schema/testdata/schema_file_parity.meta-schema.json', import.meta.url),
  'utf8'
);

function issuesOf(fn: () => unknown): Array<{ path: string; message: string }> {
  return thrown(fn, SchemaDocumentError).issues;
}

for (const driver of drivers) {
  const open = (options: Parameters<typeof openTestEngine>[0] = {}): Engine => openTestEngine({ driver, ...options });

  describe(`define and publish (${driver})`, () => {
    test('define stores a draft; publish makes it version 1', () => {
      let now = 1000;
      const engine = open({ clock: () => now });
      const draft = engine.schemas.define(alice, orderDocument());
      assert.equal(draft.version, null);
      assert.equal(draft.namespace, 'default');
      assert.equal(draft.instanceType, 'Order');
      assert.equal(draft.definedAt, 1000);
      assert.equal(draft.publishedAt, null);
      assert.equal(engine.schemas.live(alice, 'Order'), undefined);

      now = 2000;
      assert.deepEqual(engine.schemas.publish(alice, 'Order'), { namespace: 'default', name: 'Order', version: 1, published: true });
      const live = engine.schemas.live(alice, 'Order');
      assert.equal(live?.version, 1);
      assert.equal(live?.definedAt, 1000);
      assert.equal(live?.publishedAt, 2000);
      assert.equal(engine.schemas.draft(alice, 'Order'), undefined);
    });

    test('the stored document is the loader canonical form, with its SHA-256', () => {
      const engine = open();
      const text = JSON.stringify(orderDocument(), null, 2);
      const record = engine.schemas.define(alice, text);
      const { canonical, document } = loadSchemaFile(text);
      assert.equal(record.canonical, canonical);
      assert.deepEqual(record.document, document);
      assert.equal(record.hash, createHash('sha256').update(canonical).digest('hex'));
      assert.equal(engine.schemas.define(alice, orderDocument()).canonical, canonical);
    });

    test('a name has one draft; the next define replaces it', () => {
      const engine = open();
      engine.schemas.define(alice, orderDocument());
      const second = clone(orderDocument()) as { description?: string };
      second.description = 'Orders placed in the shop.';
      engine.schemas.define(alice, second);
      assert.equal(engine.schemas.draft(alice, 'Order')?.document.description, 'Orders placed in the shop.');
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM engine_schemas')?.n, 1);
    });

    test('each publish mints the next version and every version stays readable', () => {
      const engine = open();
      for (const description of ['one', 'two', 'three']) {
        engine.schemas.define(alice, { ...orderDocument(), description });
        engine.schemas.publish(alice, 'Order');
      }
      assert.equal(engine.schemas.live(alice, 'Order')?.version, 3);
      assert.deepEqual(
        [1, 2, 3].map((version) => engine.schemas.version(alice, 'Order', version)?.document.description),
        ['one', 'two', 'three']
      );
      assert.equal(engine.schemas.version(alice, 'Order', 4), undefined);
    });

    test('publishing a draft identical to the live version mints nothing and drops the draft', () => {
      const engine = open();
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.schemas.define(alice, JSON.stringify(orderDocument(), null, 4));
      assert.deepEqual(engine.schemas.publish(alice, 'Order'), { namespace: 'default', name: 'Order', version: 1, published: false });
      assert.equal(engine.schemas.draft(alice, 'Order'), undefined);
      assert.equal(engine.schemas.live(alice, 'Order')?.version, 1);
    });

    test('publish without a draft is not_found', () => {
      const engine = open();
      assert.equal(thrown(() => engine.schemas.publish(alice, 'Order'), EngineError).code, 'not_found');
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      assert.match(thrown(() => engine.schemas.publish(alice, 'Order'), EngineError).message, /schema Order has no draft in namespace default/);
    });

    test('list reports each name with its live version and whether it has a draft', () => {
      const engine = open();
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      engine.schemas.define(alice, { ...orderDocument(), description: 'next' });
      engine.schemas.define(alice, schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' } }]));
      assert.deepEqual(engine.schemas.list(alice), [
        { namespace: 'default', name: 'Note', liveVersion: null, hasDraft: true },
        { namespace: 'default', name: 'Order', liveVersion: 1, hasDraft: true },
      ]);
    });

    test('reads check their arguments', () => {
      const engine = open();
      for (const version of [0, -1, 1.5]) {
        assert.equal(thrown(() => engine.schemas.version(alice, 'Order', version), EngineError).code, 'invalid_argument');
      }
      assert.equal(thrown(() => engine.schemas.live(alice, 'not a name'), EngineError).code, 'invalid_argument');
    });
  });

  describe(`schema documents (${driver})`, () => {
    test('the instance type is the type named like the schema, even among several', () => {
      const engine = open();
      assert.equal(engine.schemas.define(alice, orderDocument()).instanceType, 'Order');
    });

    test('the instance type is the only type when none is named like the schema', () => {
      const engine = open();
      const document = { kind: 'General', name: 'Catalog', types: { Item: { name: 'Item', role: 'EmbeddedStruct' } } };
      assert.equal(engine.schemas.define(alice, document).instanceType, 'Item');
    });

    test('a schema without an instance type is refused, naming the types it has', () => {
      const engine = open();
      const document = {
        kind: 'General',
        name: 'Catalog',
        types: { Item: { name: 'Item', role: 'EmbeddedStruct' }, Shelf: { name: 'Shelf', role: 'EmbeddedStruct' } },
      };
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, document)), [
        { path: '/types', message: 'no instance type: declare a type named Catalog or exactly one type (found: Item, Shelf)' },
      ]);
      assert.deepEqual(engine.schemas.list(alice), []);
    });

    test('only a General document with a name is a schema', () => {
      const engine = open();
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, { ...orderDocument(), kind: 'DB' })), [
        { path: '/kind', message: 'a schema is a document of kind General, not DB' },
      ]);
      const enumFile = { kind: 'Enum', name: 'OrderStatus', values: [{ name: 'OPEN' }] };
      assert.deepEqual(
        issuesOf(() => engine.schemas.define(alice, enumFile)).map((issue) => issue.message),
        ['a schema is a document of kind General; this file has no kind', 'a schema needs a name']
      );
      const { name: _, ...nameless } = orderDocument();
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, nameless)), [{ path: '/name', message: 'a schema needs a name' }]);
      const spaced = schemaDocument('Order Line', [{ name: 'sku', typeRef: { name: 'string' } }]);
      assert.match(issuesOf(() => engine.schemas.define(alice, spaced))[0].message, /schema name "Order Line" must match/);
    });

    test('imports and operation sets are refused', () => {
      const engine = open();
      const imports = { ...orderDocument(), imports: [{ from: '../shared', package: 'shared', types: ['Money'] }] };
      assert.deepEqual(
        issuesOf(() => engine.schemas.define(alice, imports)).map((issue) => issue.path),
        ['/imports']
      );
      const operations = {
        ...orderDocument(),
        operationSets: [{ name: 'OrderOps', operations: [{ name: 'getOrder', typeRef: { name: 'Order' } }] }],
      };
      assert.deepEqual(
        issuesOf(() => engine.schemas.define(alice, operations)).map((issue) => issue.path),
        ['/operationSets']
      );
    });

    test('field types the schema runtime cannot validate are refused', () => {
      const engine = open();
      const document = schemaDocument(
        'Order',
        [
          { name: 'payment', typeRef: { name: 'Payment' } },
          { name: 'labels', typeRef: { name: 'string', isMap: true } },
          { name: 'owner', typeRef: { name: 'Customer' } },
        ],
        {
          unions: { Payment: { name: 'Payment', types: ['Card', 'Cash'] } },
          types: {
            Card: { name: 'Card', role: 'EmbeddedStruct' },
            Cash: { name: 'Cash', role: 'EmbeddedStruct' },
          },
        }
      );
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, document)), [
        {
          path: '/types/Order/fields/0/typeRef',
          message: 'field Order.payment has the union type Payment, which the schema runtime does not validate',
        },
        { path: '/types/Order/fields/1/typeRef', message: 'field Order.labels is a map, which the schema runtime does not validate' },
        {
          path: '/types/Order/fields/2/typeRef',
          message: 'field Order.owner has type Customer, which is not a primitive, a scalar, or an enum or type of this schema',
        },
      ]);
    });

    test('a document the loader refuses is refused with its issues, and nothing is written', () => {
      const engine = open();
      const unknownKey = clone(orderDocument()) as { types: { Order: Record<string, unknown> } };
      unknownKey.types.Order.colour = 'red';
      const issues = issuesOf(() => engine.schemas.define(alice, unknownKey));
      assert.equal(issues[0].path, '/types/Order');
      assert.equal(issues[0].message, 'unknown key "colour"');
      assert.equal(thrown(() => engine.schemas.define(alice, '{"kind": "General",'), SchemaDocumentError).code, 'invalid_schema');
      assert.match(thrown(() => engine.schemas.define(alice, '{}', { source: 'orders.schema.json' }), SchemaDocumentError).message, /^orders\.schema\.json: /);
      assert.deepEqual(engine.schemas.list(alice), []);
    });

    test('a behavior the loader accepts is refused without an implementation, and on a nested type', () => {
      const engine = open({ metaSchema: behaviorMetaSchema });
      const document = clone(orderDocument()) as { types: { Order: Record<string, unknown>; OrderLine: Record<string, unknown> } };
      document.types.Order.behaviors = [{ name: 'acme.Audited' }];
      document.types.OrderLine.behaviors = [{ name: 'acme.Stock', config: { aisles: 3 } }];
      const error = thrown(() => engine.schemas.define(alice, document), SchemaDocumentError);
      assert.deepEqual(error.issues, [
        {
          path: '/types/OrderLine/behaviors/0',
          message: 'type OrderLine: behavior acme.Stock composes on the instance type, Order; OrderLine is a nested type, which has no instances',
        },
        { path: '/types/Order/behaviors/0', message: 'behavior acme.Audited on type Order: no implementation registered' },
      ]);
      assert.deepEqual(engine.schemas.list(alice), []);
      // Without the behavior, the deployment's meta-schema loads the document.
      assert.equal(engine.schemas.define(alice, orderDocument()).name, 'Order');
    });

    test('an object scalar the document declares says which JSON it holds, or define and publish refuse it', () => {
      const engine = open();
      const blob = (name: string, typeMappings?: Record<string, string>, rules: Record<string, unknown> = {}) =>
        schemaDocument(name, [{ name: 'payload', typeRef: { name: 'Acme.Blob' } }], {
          scalars: { 'Acme.Blob': { name: 'Acme.Blob', languagePrimitive: 'object', ...rules, ...(typeMappings ? { typeMappings } : {}) } },
        });
      const refused = [
        {
          path: '/scalars/Acme.Blob',
          message:
            'scalar Acme.Blob has language primitive object but no json_schema type mapping of object, array or any to say which JSON it holds: ' +
            'add typeMappings: { json_schema: object } (or array or any; JSONSchemaType in a catalog row), ' +
            "use the catalog's Generic.JSON for free-form JSON, or model a value with known fields as a nested object type",
        },
      ];
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, blob('Reading'))), refused);
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, blob('Reading', { json_schema: 'string', sql: 'JSONB' }))), refused);
      // A length is a rule on a string, so with one the runtime does not
      // hold the value to a JSON object.
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, blob('Reading', { json_schema: 'object' }, { maxLength: 10 }))), [
        {
          path: '/scalars/Acme.Blob',
          message:
            'scalar Acme.Blob has language primitive object and json_schema type mapping object, but its maxLength 10 is a rule on a string, ' +
            'so the validators check its values as strings: drop the maxLength',
        },
      ]);
      assert.deepEqual(engine.schemas.list(alice), []);

      // A draft an engine stored before the rule is refused at publish,
      // which loads it again.
      const { canonical } = loadSchemaFile(JSON.stringify(blob('Reading')));
      engine.storage.run(
        'INSERT INTO engine_schemas (namespace, name, version, document, hash, defined_at, defined_by) VALUES (?, ?, 0, ?, ?, ?, ?)',
        ['default', 'Reading', canonical, createHash('sha256').update(canonical).digest('hex'), 1, 'alice']
      );
      assert.deepEqual(issuesOf(() => engine.schemas.publish(alice, 'Reading')), refused);
      assert.equal(engine.schemas.live(alice, 'Reading'), undefined);

      // With the mapping, the schema runtime holds the value to that JSON.
      for (const [name, jsonType, value, wrong] of [
        ['ObjectBlob', 'object', { a: [1, 'b'] }, [1]],
        ['ArrayBlob', 'array', [1, 'b'], { a: 1 }],
        ['AnyBlob', 'any', 'text', undefined],
      ] as const) {
        engine.schemas.define(alice, blob(name, { json_schema: jsonType }));
        assert.equal(engine.schemas.publish(alice, name).version, 1, name);
        assert.deepEqual(engine.schemas.validate(alice, name, { payload: value }), [], name);
        if (wrong !== undefined) {
          assert.deepEqual(
            engine.schemas.validate(alice, name, { payload: wrong }).map((issue) => [issue.path, issue.rule]),
            [['payload', 'type']],
            name
          );
        }
      }
    });

    test("an object scalar's refusal is the Go loader's, word for word", () => {
      // ir/testdata/object_scalar_errors.json: the cases the Go IR's
      // ScalarDef.ObjectJSONError tests read too.
      const { cases } = JSON.parse(readFileSync(new URL('../../../../ir/testdata/object_scalar_errors.json', import.meta.url), 'utf8')) as {
        cases: Array<{ case: string; scalar: { name: string } & Record<string, unknown>; error: string | null }>;
      };
      assert.ok(cases.length > 0);
      for (const { case: name, scalar, error } of cases) {
        assert.equal(objectScalarIssue(scalar.name, scalar as never), error ?? undefined, name);
      }
    });

    test('an extension JSON scalar, as format --to=json writes it, is defined and published', () => {
      // cli/testdata/format/ext-json-scalar.schema.json is what format
      // --to=json writes of a TypeScript service whose extension catalog
      // gives Ext.Doc the Object primitive and the json_schema mapping
      // object; the Go test that writes it keeps it current. The engine
      // knows only the builtin catalog, so it reads Ext.Doc as written.
      const engine = open();
      const text = readFileSync(new URL('../../../../cli/testdata/format/ext-json-scalar.schema.json', import.meta.url), 'utf8');
      engine.schemas.define(alice, text);
      assert.equal(engine.schemas.publish(alice, 'ext-json-scalar').version, 1);
      assert.deepEqual(engine.schemas.validate(alice, 'ext-json-scalar', { payload: { a: 1 }, meta: [1, 'b'] }), []);
      assert.deepEqual(
        engine.schemas.validate(alice, 'ext-json-scalar', { payload: [1], meta: 7 }).map((issue) => [issue.path, issue.rule]),
        [['payload', 'type']]
      );
    });

    test('with the core meta-schema, the loader itself refuses a behavior the core does not declare', () => {
      const engine = open();
      const document = clone(orderDocument()) as { types: { Order: Record<string, unknown> } };
      document.types.Order.behaviors = [{ name: 'acme.Audited' }];
      assert.deepEqual(issuesOf(() => engine.schemas.define(alice, document)), [
        {
          path: '/types/Order/behaviors/0/name',
          message: 'must be one of "Assignment", "Blueprint", "Branches", "Budget", "Comments", "Constants", "Dependencies", "Lease", "Links", "Presence", "Queue", "Reactions", "Retries", "Revisions", "Rollups", "Search", "Variants", "Workflow"',
        },
      ]);
    });
  });

  describe(`validators (${driver})`, () => {
    function published(): Engine {
      const engine = open();
      engine.schemas.define(alice, orderDocument());
      engine.schemas.publish(alice, 'Order');
      return engine;
    }

    test('a valid instance has no issues', () => {
      const engine = published();
      const order = { title: 'Desk', quantity: 2, status: 'open', lines: [{ sku: 'D-1', count: 1 }] };
      assert.deepEqual(engine.schemas.validate(alice, 'Order', order), []);
    });

    test('field rules come from the schema runtime, with nested paths', () => {
      const engine = published();
      const issues = engine.schemas.validate(alice, 'Order', {
        title: 7,
        quantity: 'two',
        status: 'lost',
        lines: [{ count: 0 }, null, { sku: 'SKU-TOO-LONG-1' }],
      });
      assert.deepEqual(
        issues.map((issue) => [issue.path, issue.rule]),
        [
          ['title', 'type'],
          ['quantity', 'type'],
          ['status', 'enum'],
          ['lines[0].sku', 'required'],
          ['lines[0].count', 'min'],
          ['lines[1]', 'required'],
          ['lines[2].sku', 'maxLength'],
        ]
      );
    });

    test('an undeclared key is refused at every level', () => {
      const engine = published();
      assert.deepEqual(engine.schemas.validate(alice, 'Order', { title: 'Desk', colour: 'red', lines: [{ sku: 'D-1', weight: 3 }] }), [
        { path: 'colour', rule: 'unknown', message: 'Order has no field colour' },
        { path: 'lines[0].weight', rule: 'unknown', message: 'OrderLine has no field weight' },
      ]);
    });

    test('a value of the wrong shape is one issue, the schema runtime type rule', () => {
      const engine = published();
      engine.schemas.define(
        alice,
        schemaDocument(
          'Shipment',
          [
            { name: 'tags', typeRef: { name: 'string', isArray: true }, required: true },
            { name: 'address', typeRef: { name: 'Address' } },
          ],
          { types: { Address: { name: 'Address', role: 'EmbeddedStruct', fields: [{ name: 'city', typeRef: { name: 'string' } }] } } }
        )
      );
      engine.schemas.publish(alice, 'Shipment');
      const issues = (name: string, value: Record<string, unknown>) =>
        engine.schemas.validate(alice, name, value).map((issue) => [issue.path, issue.rule, issue.message]);
      // An optional list given a non-list.
      assert.deepEqual(issues('Order', { title: 'Desk', lines: { sku: 'D-1' } }), [['lines', 'type', 'expected an array']]);
      // A required list given a non-list.
      assert.deepEqual(issues('Shipment', { tags: 'fragile' }), [['tags', 'type', 'expected an array']]);
      // An object-typed field given a string.
      assert.deepEqual(issues('Shipment', { tags: [], address: 'Main St' }), [['address', 'type', 'expected an object']]);
      // A list element of an object type given a number.
      assert.deepEqual(issues('Order', { title: 'Desk', lines: [7] }), [['lines[0]', 'type', 'expected an object']]);
    });

    test('an instance is a JSON object of JSON values', () => {
      const engine = published();
      assert.deepEqual(engine.schemas.validate(alice, 'Order', ['Desk']), [{ path: '', rule: 'type', message: 'an instance is a JSON object' }]);
      assert.deepEqual(
        engine.schemas.validate(alice, 'Order', { title: 'Desk', quantity: Number.NaN, lines: [{ sku: new Date(0) }] }).map((issue) => issue.path),
        ['quantity', 'lines[0].sku']
      );
      // A member whose value is undefined is absent.
      assert.deepEqual(engine.schemas.validate(alice, 'Order', { title: 'Desk', quantity: undefined }), []);
    });

    test('a value validates against the live version or a given one', () => {
      const engine = published();
      const next = clone(orderDocument()) as { types: { Order: { fields: Array<Record<string, unknown>> } } };
      next.types.Order.fields.push({ name: 'note', typeRef: { name: 'string' } });
      engine.schemas.define(alice, next);
      engine.schemas.publish(alice, 'Order');
      assert.deepEqual(engine.schemas.validate(alice, 'Order', { title: 'Desk', note: 'fragile' }), []);
      assert.deepEqual(
        engine.schemas.validate(alice, 'Order', { title: 'Desk', note: 'fragile' }, { version: 1 }).map((issue) => issue.rule),
        ['unknown']
      );
    });

    test('the validator of a version is built once', () => {
      const engine = published();
      const validator = engine.schemas.validator(alice, 'Order');
      assert.equal(engine.schemas.validator(alice, 'Order', { version: 1 }), validator);
      assert.equal(validator.model.instanceType, 'Order');
      engine.schemas.define(alice, { ...orderDocument(), description: 'next' });
      engine.schemas.publish(alice, 'Order');
      assert.notEqual(engine.schemas.validator(alice, 'Order'), validator);
      assert.equal(engine.schemas.validator(alice, 'Order', { version: 1 }), validator);
    });

    test('a catalog scalar the document declares, as format --to=json writes it, validates as the catalog has it', () => {
      const engine = open();
      const declared = schemaDocument(
        'Reading',
        [
          { name: 'meta', typeRef: { name: 'Generic.JSON' } },
          { name: 'count', typeRef: { name: 'Generic.Int64' } },
        ],
        {
          scalars: {
            'Generic.JSON': { name: 'Generic.JSON', languagePrimitive: 'object', typeMappings: { sql: 'JSONB' } },
            'Generic.Int64': { name: 'Generic.Int64', languagePrimitive: 'number' },
          },
        }
      );
      engine.schemas.define(alice, declared);
      engine.schemas.publish(alice, 'Reading');
      assert.deepEqual(engine.schemas.validate(alice, 'Reading', { meta: { a: [1, 'b'] }, count: 2 }), []);
      assert.deepEqual(
        engine.schemas.validate(alice, 'Reading', { count: 1.5 }).map((issue) => issue.path),
        ['count']
      );
      // A version that stops declaring them changes no scalar.
      engine.schemas.define(alice, { ...declared, scalars: {} });
      assert.equal(engine.schemas.publish(alice, 'Reading').version, 2);
    });

    test('validating a schema with no live version is not_found', () => {
      const engine = open();
      engine.schemas.define(alice, orderDocument());
      assert.equal(thrown(() => engine.schemas.validate(alice, 'Order', {}), EngineError).code, 'not_found');
      assert.equal(thrown(() => engine.schemas.validate(alice, 'Note', {}), EngineError).code, 'not_found');
    });
  });
}
