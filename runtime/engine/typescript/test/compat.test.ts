// The compatibility rule, case by case: each case publishes the base schema,
// changes a copy, and defines it as the next version.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { IncompatibleChangeError } from '../dist/index.js';
import { alice, cleanup, clone, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

type Fields = Array<Record<string, unknown> & { name: string; typeRef: Record<string, unknown> }>;
interface Doc {
  description?: string;
  types: Record<string, { name: string; fields: Fields }>;
  enums: Record<string, { values: Array<{ name: string; serializedAs?: string }> }>;
  scalars: Record<string, Record<string, unknown>>;
}

function base(): Doc {
  return schemaDocument(
    'Order',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true, validateMinLength: 2, validateMaxLength: 40 },
      { name: 'note', typeRef: { name: 'string' }, validatePattern: '^[a-z ]*$', description: 'For the packer.' },
      { name: 'quantity', typeRef: { name: 'number' }, validateMin: 1, validateMax: 100 },
      { name: 'tags', typeRef: { name: 'string', isArray: true }, validateListMin: 1, validateListMax: 5 },
      { name: 'status', typeRef: { name: 'OrderStatus' } },
      { name: 'lines', typeRef: { name: 'OrderLine', isArray: true } },
      { name: 'code', typeRef: { name: 'Local.Code' } },
      { name: 'placedBy', typeRef: { name: 'string' }, jsonTag: 'placed_by' },
    ],
    {
      enums: {
        OrderStatus: { name: 'OrderStatus', values: [{ name: 'OPEN', serializedAs: 'open' }, { name: 'SHIPPED', serializedAs: 'shipped' }] },
        Colour: { name: 'Colour', values: [{ name: 'RED' }, { name: 'BLUE' }] },
      },
      types: {
        OrderLine: {
          name: 'OrderLine',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'sku', typeRef: { name: 'string' }, required: true },
            { name: 'count', typeRef: { name: 'number' } },
          ],
        },
        Sketch: { name: 'Sketch', role: 'EmbeddedStruct', fields: [{ name: 'colour', typeRef: { name: 'Colour' } }] },
      },
      scalars: {
        'Local.Code': { name: 'Local.Code', languagePrimitive: 'string', pattern: '^[A-Z]+$', minLength: 1, maxLength: 8 },
      },
    }
  ) as unknown as Doc;
}

function field(doc: Doc, typeName: string, name: string): Record<string, unknown> & { typeRef: Record<string, unknown> } {
  const found = doc.types[typeName].fields.find((candidate) => candidate.name === name);
  assert.ok(found, `${typeName}.${name}`);
  return found;
}

function nextVersion(change: (doc: Doc) => void): () => unknown {
  const engine = openTestEngine();
  engine.schemas.define(alice, base() as unknown as Record<string, unknown>);
  engine.schemas.publish(alice, 'Order');
  const next = clone(base());
  change(next);
  return () => {
    engine.schemas.define(alice, next as unknown as Record<string, unknown>);
    return engine.schemas.publish(alice, 'Order');
  };
}

describe('changes every stored instance still satisfies', () => {
  const cases: Array<[string, (doc: Doc) => void]> = [
    ['a new optional field', (doc) => doc.types.Order.fields.push({ name: 'giftWrap', typeRef: { name: 'boolean' } })],
    ['a new optional field on a nested type', (doc) => doc.types.OrderLine.fields.push({ name: 'note', typeRef: { name: 'string' } })],
    ['a new enum value', (doc) => doc.enums.OrderStatus.values.push({ name: 'CANCELLED', serializedAs: 'cancelled' })],
    ['a lower minLength', (doc) => (field(doc, 'Order', 'title').validateMinLength = 1)],
    ['a dropped minLength', (doc) => delete field(doc, 'Order', 'title').validateMinLength],
    ['a higher maxLength', (doc) => (field(doc, 'Order', 'title').validateMaxLength = 80)],
    ['a dropped maxLength', (doc) => delete field(doc, 'Order', 'title').validateMaxLength],
    ['a lower min', (doc) => (field(doc, 'Order', 'quantity').validateMin = 0)],
    ['a higher max', (doc) => (field(doc, 'Order', 'quantity').validateMax = 1000)],
    ['a lower listMin', (doc) => (field(doc, 'Order', 'tags').validateListMin = 0)],
    ['a higher listMax', (doc) => (field(doc, 'Order', 'tags').validateListMax = 10)],
    ['a required field made optional', (doc) => delete field(doc, 'Order', 'title').required],
    ['a dropped pattern', (doc) => delete field(doc, 'Order', 'note').validatePattern],
    [
      'documentation, defaults and display metadata',
      (doc) => {
        doc.description = 'Orders placed in the shop.';
        Object.assign(field(doc, 'Order', 'note'), { description: 'For whoever packs it.', comment: 'free text', title: 'Note', placeholder: 'Leave at the door' });
        field(doc, 'Order', 'quantity').default = '1';
      },
    ],
    ['a change to a type no field reaches', (doc) => (doc.types.Sketch.fields = [])],
    ['a change to an enum no field reaches', (doc) => doc.enums.Colour.values.pop()],
    ['a wider scalar', (doc) => Object.assign(doc.scalars['Local.Code'], { maxLength: 16, minLength: 0 })],
  ];
  for (const [name, change] of cases) {
    test(name, () => {
      assert.deepEqual(nextVersion(change)(), { namespace: 'default', name: 'Order', version: 2, published: true });
    });
  }
});

describe('changes a stored instance may not satisfy', () => {
  const cases: Array<[string, (doc: Doc) => void, string[]]> = [
    ['a removed field', (doc) => (doc.types.Order.fields = doc.types.Order.fields.filter((f) => f.name !== 'note')), ['field Order.note is removed']],
    ['a renamed field', (doc) => (field(doc, 'Order', 'note').name = 'remark'), ['field Order.note is removed']],
    [
      'a new JSON key',
      (doc) => delete field(doc, 'Order', 'placedBy').jsonTag,
      ['field Order.placedBy changes its JSON key from placed_by to placedBy'],
    ],
    ['a changed type', (doc) => (field(doc, 'Order', 'quantity').typeRef = { name: 'string' }), ['field Order.quantity changes type from number to string']],
    ['a single value made a list', (doc) => (field(doc, 'Order', 'title').typeRef = { name: 'string', isArray: true }), ['field Order.title changes type from string to string[]']],
    [
      'a list made a list of lists',
      (doc) => (field(doc, 'Order', 'tags').typeRef = { name: 'string', isArray: true, isArrayOfArrays: true }),
      ['field Order.tags changes type from string[] to string[][]'],
    ],
    [
      'an enum field retyped, with its enum removed',
      (doc) => {
        field(doc, 'Order', 'status').typeRef = { name: 'string' };
        delete (doc.enums as Partial<Doc['enums']>).OrderStatus;
      },
      ['field Order.status changes type from OrderStatus to string'],
    ],
    ['an optional field made required', (doc) => (field(doc, 'Order', 'note').required = true), ['field Order.note becomes required']],
    [
      'a new required field',
      (doc) => doc.types.Order.fields.push({ name: 'currency', typeRef: { name: 'string' }, required: true }),
      ['field Order.currency is added as required'],
    ],
    ['a higher minLength', (doc) => (field(doc, 'Order', 'title').validateMinLength = 3), ['field Order.title raises minLength from 2 to 3']],
    ['a new minLength', (doc) => (field(doc, 'Order', 'note').validateMinLength = 1), ['field Order.note gains minLength 1']],
    ['a lower maxLength', (doc) => (field(doc, 'Order', 'title').validateMaxLength = 20), ['field Order.title lowers maxLength from 40 to 20']],
    ['a new maxLength', (doc) => (field(doc, 'Order', 'note').validateMaxLength = 10), ['field Order.note gains maxLength 10']],
    ['a higher min', (doc) => (field(doc, 'Order', 'quantity').validateMin = 2), ['field Order.quantity raises min from 1 to 2']],
    ['a lower max', (doc) => (field(doc, 'Order', 'quantity').validateMax = 50), ['field Order.quantity lowers max from 100 to 50']],
    ['a new min', (doc) => (field(doc, 'OrderLine', 'count').validateMin = 1), ['field OrderLine.count gains min 1']],
    ['a higher listMin', (doc) => (field(doc, 'Order', 'tags').validateListMin = 2), ['field Order.tags raises listMin from 1 to 2']],
    ['a lower listMax', (doc) => (field(doc, 'Order', 'tags').validateListMax = 3), ['field Order.tags lowers listMax from 5 to 3']],
    ['a changed pattern', (doc) => (field(doc, 'Order', 'note').validatePattern = '^[a-z]*$'), ['field Order.note changes its pattern']],
    ['a new pattern', (doc) => (field(doc, 'Order', 'title').validatePattern = '^[A-Z]'), ['field Order.title gains a pattern']],
    ['a removed enum value', (doc) => doc.enums.OrderStatus.values.pop(), ['enum OrderStatus drops value shipped']],
    [
      'a renamed enum value',
      (doc) => (doc.enums.OrderStatus.values[0].serializedAs = 'opened'),
      ['enum OrderStatus drops value open'],
    ],
    ['a removed nested field', (doc) => doc.types.OrderLine.fields.pop(), ['field OrderLine.count is removed']],
    ['a nested field made required', (doc) => (field(doc, 'OrderLine', 'count').required = true), ['field OrderLine.count becomes required']],
    ['a scalar with a changed pattern', (doc) => (doc.scalars['Local.Code'].pattern = '^[A-Z]{2,}$'), ['scalar Local.Code changes its pattern']],
    ['a scalar with a higher minLength', (doc) => (doc.scalars['Local.Code'].minLength = 2), ['scalar Local.Code raises minLength from 1 to 2']],
    ['a scalar with a lower maxLength', (doc) => (doc.scalars['Local.Code'].maxLength = 4), ['scalar Local.Code lowers maxLength from 8 to 4']],
    ['a scalar with reserved words', (doc) => (doc.scalars['Local.Code'].reservedWords = ['NONE']), ['scalar Local.Code reserves NONE']],
    [
      'a scalar with another primitive',
      (doc) => Object.assign(doc.scalars['Local.Code'], { languagePrimitive: 'number', pattern: undefined, minLength: undefined, maxLength: undefined }),
      [
        'scalar Local.Code changes primitive from String to Float',
      ],
    ],
    [
      'several changes, named together',
      (doc) => {
        doc.types.Order.fields = doc.types.Order.fields.filter((f) => f.name !== 'note');
        field(doc, 'Order', 'title').validateMinLength = 5;
        doc.enums.OrderStatus.values.shift();
      },
      ['field Order.title raises minLength from 2 to 5', 'field Order.note is removed', 'enum OrderStatus drops value open'],
    ],
  ];
  for (const [name, change, messages] of cases) {
    test(name, () => {
      const error = thrown(nextVersion(change), IncompatibleChangeError);
      assert.equal(error.code, 'incompatible_change');
      assert.deepEqual(
        error.changes.map((c) => c.message),
        messages
      );
      for (const message of messages) {
        assert.ok(error.message.includes(message), error.message);
      }
      assert.match(error.message, /publish any other change under a new schema name$/);
    });
  }
});

test('a refused version leaves the live version and the draft as they were', () => {
  const engine = openTestEngine();
  engine.schemas.define(alice, base() as unknown as Record<string, unknown>);
  engine.schemas.publish(alice, 'Order');
  const compatible = clone(base());
  compatible.description = 'kept';
  engine.schemas.define(alice, compatible as unknown as Record<string, unknown>);
  const incompatible = clone(base());
  field(incompatible, 'Order', 'note').required = true;
  thrown(() => engine.schemas.define(alice, incompatible as unknown as Record<string, unknown>), IncompatibleChangeError);
  assert.equal(engine.schemas.live(alice, 'Order')?.version, 1);
  assert.equal(engine.schemas.draft(alice, 'Order')?.document.description, 'kept');
});

test('a new instance type is refused', () => {
  const engine = openTestEngine();
  const catalog = (typeName: string) => ({
    kind: 'General',
    name: 'Catalog',
    types: { [typeName]: { name: typeName, role: 'EmbeddedStruct', fields: [{ name: 'title', typeRef: { name: 'string' } }] } },
  });
  engine.schemas.define(alice, catalog('Item'));
  engine.schemas.publish(alice, 'Catalog');
  const error = thrown(() => engine.schemas.define(alice, catalog('Product')), IncompatibleChangeError);
  assert.deepEqual(error.changes, [{ path: 'Item', message: 'the instance type changes from Item to Product' }]);
});
