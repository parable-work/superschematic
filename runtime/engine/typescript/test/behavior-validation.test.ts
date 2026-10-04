// A behavior's validate (D16, amended): it judges the instance's own
// fields a write would store once the live version accepts them, and its
// issues refuse the write as invalid_instance, as the live version's do,
// before any guard. It runs on every write of the fields: a caller's
// create and update, a behavior's instances.create, an operation's
// update(), and validateUpdate(), which reports without writing. Its
// checkType holds a value to a type its checkedTypes names, strictly.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorError,
  InstanceValidationError,
  SchemaDocumentError,
  defineBehavior,
  type Engine,
  type FrozenJSON,
  type Principal,
  type ValidationIssue,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

/** What the test behaviors saw, in order. */
const seen: Array<Record<string, unknown>> = [];

afterEach(() => {
  seen.length = 0;
  cleanup();
});

interface ShapeConfig {
  ban?: string;
  permission?: string;
  type?: string;
}

// test.Shape refuses a title that holds the word its config bans, unless
// the caller holds its permission, and holds extra to the type its config
// names. It records each request and its context's members.
const shape = defineBehavior<ShapeConfig>({
  declaration: {
    name: 'test.Shape',
    configSchema: {
      type: 'object',
      additionalProperties: false,
      properties: { ban: { type: 'string' }, permission: { type: 'string' }, type: { type: 'string' } },
    },
  },
  checkedTypes: (config) => (config.type === undefined ? [] : [config.type]),
  validate(context, request) {
    seen.push({ step: 'validate', request, frozen: Object.isFrozen(request) && Object.isFrozen(context), members: Object.keys(context).sort() });
    const data = request.kind === 'create' ? request.data : request.after;
    const issues: ValidationIssue[] = [];
    const { ban, permission, type } = context.config;
    if (ban !== undefined && String(data.title).includes(ban) && !(permission !== undefined && context.can(permission))) {
      issues.push({ path: 'title', rule: 'banned', message: `title holds ${ban}` });
    }
    if (type !== undefined && data.extra !== undefined) {
      issues.push(...context.checkType(type, data.extra, 'extra'));
    }
    return issues;
  },
  guard(_view, request) {
    seen.push({ step: 'guard', kind: request.kind });
  },
});

// test.Second refuses the title "two", after test.Shape in list order.
const second = defineBehavior({
  declaration: { name: 'test.Second' },
  validate(_context, request) {
    const data = request.kind === 'create' ? request.data : request.after;
    return String(data.title).includes('two') ? [{ path: 'title', rule: 'second', message: 'title holds two' }] : undefined;
  },
});

const patchParams = { type: 'object', additionalProperties: false, required: ['patch'], properties: { patch: { type: 'object' } } } as const;

// test.Writer writes the instance's fields through update(), reports what
// validateUpdate() says, and creates an Item.
const writer = defineBehavior({
  declaration: {
    name: 'test.Writer',
    operations: [
      { name: 'write', paramsSchema: patchParams, resultSchema: true, writes: true },
      { name: 'check', paramsSchema: patchParams, resultSchema: true },
      {
        name: 'make',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['id', 'title'], properties: { id: { type: 'string' }, title: { type: 'string' } } },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  operations: {
    write(context, params) {
      return context.update(params.patch as FrozenJSON);
    },
    check(context, params) {
      return { issues: context.validateUpdate(params.patch as FrozenJSON), title: context.data.title };
    },
    make(context, params) {
      try {
        return context.instances.create('Item', { title: String(params.title) }, { id: String(params.id) }).id;
      } catch (error) {
        if (error instanceof InstanceValidationError) {
          return { refused: error.issues };
        }
        throw error;
      }
    },
  },
});

// test.Defect's validate breaks the contract as its config says.
const defect = defineBehavior<{ mode: string }>({
  declaration: {
    name: 'test.Defect',
    configSchema: { type: 'object', additionalProperties: false, required: ['mode'], properties: { mode: { type: 'string' } } },
  },
  configChange: () => undefined,
  checkedTypes: () => ['Inner'],
  validate(context) {
    switch (context.config.mode) {
      case 'reason':
        return 'no' as unknown as ValidationIssue[];
      case 'shape':
        return [{ path: 'title', message: 'no rule' }] as unknown as ValidationIssue[];
      case 'promise':
        return Promise.resolve([]) as unknown as ValidationIssue[];
      case 'type':
        return context.checkType('Shape', {}, 'extra');
      default:
        return undefined;
    }
  },
});

/** An Item with a title, an open extra, and the types Shape and Inner, composing the behaviors given. */
function itemDocument(behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const document = schemaDocument(
    'Item',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'extra', typeRef: { name: 'Generic.JSON' } },
    ],
    {
      types: {
        Shape: {
          name: 'Shape',
          role: 'EmbeddedStruct',
          fields: [
            { name: 'size', typeRef: { name: 'number' }, required: true },
            { name: 'tags', typeRef: { name: 'string', isArray: true } },
            { name: 'inner', typeRef: { name: 'Inner' } },
          ],
        },
        Inner: { name: 'Inner', role: 'EmbeddedStruct', fields: [{ name: 'name', typeRef: { name: 'string' }, required: true }] },
      },
    }
  ) as { types: { Item: Record<string, unknown> } };
  document.types.Item.behaviors = behaviors;
  return document;
}

function publishItem(engine: Engine, behaviors: Array<{ name: string; config?: unknown }>): void {
  engine.schemas.define(alice, itemDocument(behaviors));
  engine.schemas.publish(alice, 'Item');
}

const editor: Principal = { subject: 'eve', permissions: ['items.edit'] };

for (const driver of drivers) {
  function open(): Engine {
    return openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [shape, second, writer, defect] });
  }

  describe(`a behavior's validate (${driver})`, () => {
    test('a create the live version accepts and a validate refuses is invalid_instance with every issue, before any guard, and writes nothing', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { ban: 'secret' } }, { name: 'test.Second' }]);
      const refused = thrown(() => engine.instances.create(alice, 'Item', { title: 'a secret two' }, { id: 'i1' }), InstanceValidationError);
      assert.equal(refused.code, 'invalid_instance');
      assert.deepEqual(refused.issues, [
        { path: 'title', rule: 'banned', message: 'title holds secret' },
        { path: 'title', rule: 'second', message: 'title holds two' },
      ]);
      assert.equal(refused.message, 'Item in namespace default (version 1): title: title holds secret; title: title holds two');
      assert.deepEqual(seen, [
        {
          step: 'validate',
          request: { kind: 'create', data: { title: 'a secret two' } },
          frozen: true,
          members: ['behavior', 'can', 'checkType', 'config', 'id', 'namespace', 'now', 'principal', 'schema', 'version'],
        },
      ]);
      assert.equal(engine.instances.get(alice, 'Item', 'i1'), undefined);
      assert.equal(engine.events.read(alice, { schema: 'Item' }).events.filter((event) => event.kind !== 'publish').length, 0);
      seen.length = 0;
      engine.instances.create(alice, 'Item', { title: 'plain' }, { id: 'i1' });
      assert.deepEqual(
        seen.map((entry) => entry.step),
        ['validate', 'guard']
      );
    });

    test("the live version's issues come first: validate is asked only about fields the live version accepts", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { ban: 'secret' } }]);
      const refused = thrown(() => engine.instances.create(alice, 'Item', { title: 7 }), InstanceValidationError);
      assert.deepEqual(
        refused.issues.map((issue) => [issue.path, issue.rule]),
        [['title', 'type']]
      );
      assert.equal(seen.length, 0);
    });

    test('an update asks it with the fields before and after the merge; its issues refuse the update before any guard, unless can() lets the caller through', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { ban: 'secret', permission: 'items.edit' } }]);
      engine.instances.create(alice, 'Item', { title: 'plain', extra: { a: 1 } }, { id: 'i1' });
      seen.length = 0;
      const refused = thrown(() => engine.instances.update(alice, 'Item', 'i1', { title: 'the secret', extra: { b: 2 } }), InstanceValidationError);
      assert.deepEqual(refused.issues, [{ path: 'title', rule: 'banned', message: 'title holds secret' }]);
      assert.deepEqual(
        seen.map((entry) => [entry.step, entry.request]),
        [['validate', { kind: 'update', before: { title: 'plain', extra: { a: 1 } }, after: { title: 'the secret', extra: { a: 1, b: 2 } } }]]
      );
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 1);
      // The holder of the permission may.
      assert.equal(engine.instances.update(editor, 'Item', 'i1', { title: 'the secret' }).data.title, 'the secret');
    });

    test("an operation's update() asks it with the operation's behavior as caller, and validateUpdate() reports the same issues without writing", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { ban: 'secret', type: 'Shape' } }, { name: 'test.Writer' }]);
      engine.instances.create(alice, 'Item', { title: 'plain' }, { id: 'i1' });
      seen.length = 0;
      const patch = { title: 'secret', extra: { size: 'big' } };
      const refused = thrown(() => engine.instances.invoke(alice, 'Item', 'i1', 'write', { patch }), InstanceValidationError);
      assert.deepEqual(
        refused.issues.map((issue) => [issue.path, issue.rule]),
        [
          ['title', 'banned'],
          ['extra.size', 'type'],
        ]
      );
      const issues = refused.issues;
      assert.deepEqual((seen.find((entry) => entry.step === 'validate')?.request as { caller?: string }).caller, 'test.Writer');
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 1);
      seen.length = 0;
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'check', { patch }), { issues, title: 'plain' });
      // It asks no guard, and writes nothing.
      assert.deepEqual(
        seen.map((entry) => entry.step),
        ['guard', 'validate']
      );
      assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 1);
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'check', { patch: { extra: { size: 2 } } }), { issues: [], title: 'plain' });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'i1', 'write', { patch: { extra: { size: 2 } } }), { title: 'plain', extra: { size: 2 } });
    });

    test("a behavior's instances.create asks it too, and a refusal the behavior catches leaves nothing", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { ban: 'secret' } }, { name: 'test.Writer' }]);
      engine.instances.create(alice, 'Item', { title: 'maker' }, { id: 'm1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i1', title: 'my secret' }), {
        refused: [{ path: 'title', rule: 'banned', message: 'title holds secret' }],
      });
      assert.equal(engine.instances.get(alice, 'Item', 'i1'), undefined);
      assert.equal(engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i2', title: 'open' }), 'i2');
    });

    test('checkType holds a value to a type its checkedTypes names, as a field of the type: a JSON object, its fields, and no undeclared key at any depth', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Shape', config: { type: 'Shape' } }]);
      const issues = (extra: unknown) =>
        thrown(() => engine.instances.create(alice, 'Item', { title: 't', extra: extra as FrozenJSON }), InstanceValidationError)
          .issues.map((issue) => [issue.path, issue.rule])
          .sort();
      assert.deepEqual(issues({ size: 'big', tags: 'x', inner: { name: 1, more: true }, other: 1 }), [
        ['extra.inner.more', 'unknown'],
        ['extra.inner.name', 'type'],
        ['extra.other', 'unknown'],
        ['extra.size', 'type'],
        ['extra.tags', 'type'],
      ]);
      assert.deepEqual(issues({ inner: {} }), [
        ['extra.inner.name', 'required'],
        ['extra.size', 'required'],
      ]);
      assert.deepEqual(
        thrown(() => engine.instances.create(alice, 'Item', { title: 't', extra: 'text' }), InstanceValidationError).issues,
        [{ path: 'extra', rule: 'type', message: 'a Shape is a JSON object' }]
      );
      assert.equal(engine.instances.create(alice, 'Item', { title: 't', extra: { size: 2, tags: ['a'], inner: { name: 'n' } } }).data.extra !== undefined, true);
    });

    test('checkedTypes names types of the document besides the instance type, and checkType reaches only those; the rest is a defect', () => {
      const engine = open();
      for (const type of ['Nope', 'Item']) {
        const refused = thrown(() => engine.schemas.define(alice, itemDocument([{ name: 'test.Shape', config: { type } }])), SchemaDocumentError);
        assert.deepEqual(refused.issues, [
          {
            path: '/types/Item/behaviors/0/config',
            message: `type Item: behavior test.Shape checks values against ${type}, which is not a type of the document besides Item`,
          },
        ]);
      }
      const broken = (mode: string): BehaviorError => {
        publishItem(engine, [{ name: 'test.Defect', config: { mode } }]);
        return thrown(() => engine.instances.create(alice, 'Item', { title: 't' }), BehaviorError);
      };
      assert.match(broken('reason').message, /behavior test\.Defect: validate returns a list of issues, each \{ path, rule, message \}/);
      assert.match(broken('shape').message, /validate returns a list of issues/);
      assert.match(broken('promise').message, /behavior test\.Defect: validate is synchronous \(D16\): it returned a promise/);
      assert.match(broken('type').message, /behavior test\.Defect: checkType: Shape is not a type its checkedTypes names \(Inner\)/);
      assert.equal(engine.instances.list(alice, 'Item').items.length, 0);
      for (const member of ['validate', 'checkedTypes', 'instanceSchema']) {
        assert.throws(
          () => engine.behaviors.register(defineBehavior({ declaration: { name: `test.Bad${member}` }, [member]: 'no' } as never)),
          new RegExp(`${member} is a function`)
        );
      }
    });
  });
}
