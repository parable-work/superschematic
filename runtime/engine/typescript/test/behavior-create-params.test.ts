// Behaviors take parameters at create (D16, amended): a declaration's
// createParamsSchema; the engine's checks of a create's parameters, at
// JSON pointers under /behaviors; the create guard, asked after the
// insert and before any initialize, whose veto, like one initialize
// throws, carries a code its declaration lists; each initialize with its
// own parameters; a behavior's instances.create with parameters; and the
// create route, tool and describe document, which carry them beside the
// preconditions and veto codes.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';

import {
  BehaviorError,
  BehaviorVetoError,
  CreateParamsError,
  defineBehavior,
  type BehaviorDeclaration,
  type Engine,
  type FrozenJSON,
  type GuardRequest,
} from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { hold, holdDeclaration, openBehaviorEngine, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, thrown } from './helpers.ts';

/** What the test behaviors saw, in order. */
const seen: Array<Record<string, unknown>> = [];

afterEach(() => {
  seen.length = 0;
  cleanup();
});

const tagsDeclaration: BehaviorDeclaration = {
  name: 'test.Tags',
  description: 'Tags a create gives, which it keeps.',
  configSchema: { type: 'object', additionalProperties: false, properties: { refuse: { type: 'string' }, code: { type: 'string' } } },
  createParamsSchema: {
    type: 'object',
    additionalProperties: false,
    properties: { tags: { type: 'array', maxItems: 3, items: { type: 'string', minLength: 1 } } },
  },
  fields: [{ name: 'tags', description: 'The tags its create gave.' }],
  vetoes: [{ code: 'refused_tag', description: 'A create gives a tag its config refuses.' }],
};

// test.Tags records what its guard is asked at a create and what its
// initialize gets, and vetoes a create that gives the tag its config
// refuses, with the code its config names (refused_tag by default); its
// initialize vetoes the tag late the same way.
const tags = defineBehavior<{ refuse?: string; code?: string }>({
  declaration: tagsDeclaration,
  migrations: [{ version: 1, name: 'tags', columns: { tags: { type: 'text' } } }],
  guard(view, request) {
    if (request.kind !== 'create') {
      return undefined;
    }
    seen.push({ step: 'guard', columns: view.columns.get(), data: view.data, request, frozen: Object.isFrozen(request.behaviors) });
    const given = (request.behaviors['test.Tags']?.tags ?? []) as readonly string[];
    const refuse = view.config.refuse;
    return refuse !== undefined && given.includes(refuse)
      ? { reason: `tag ${refuse} is refused`, code: view.config.code ?? 'refused_tag', details: { tag: refuse } }
      : undefined;
  },
  initialize(context, params) {
    seen.push({ step: 'initialize', params });
    if (((params.tags ?? []) as readonly string[]).includes('late')) {
      throw new BehaviorVetoError('test.Tags', 'create', context.schema, context.id, { reason: 'tag late is refused', code: context.config.code ?? 'refused_tag' });
    }
    context.columns.set({ tags: JSON.stringify(params.tags ?? []) });
  },
  afterChange(context, change) {
    if (change.kind === 'create') {
      seen.push({ step: 'afterChange', columns: context.columns.get() });
    }
  },
  fields: {
    tags: (view) => {
      const stored = view.columns.get().tags;
      return stored === null ? undefined : (JSON.parse(String(stored)) as unknown);
    },
  },
});

// test.Reason needs a reason at every create: its createParamsSchema
// refuses {}.
const reason = defineBehavior({
  declaration: {
    name: 'test.Reason',
    createParamsSchema: { type: 'object', additionalProperties: false, required: ['reason'], properties: { reason: { type: 'string' } } },
  },
  initialize(_context, params) {
    seen.push({ step: 'reason', params });
  },
});

// test.Maker creates an Item from its operation, with parameters for the
// new instance's behaviors.
const maker = defineBehavior({
  declaration: {
    name: 'test.Maker',
    operations: [
      {
        name: 'make',
        paramsSchema: {
          type: 'object',
          additionalProperties: false,
          required: ['id'],
          properties: { id: { type: 'string' }, tags: { type: 'array' }, broken: { type: 'boolean' }, catch: { type: 'boolean' } },
        },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  operations: {
    make(context, params) {
      const behaviors = params.broken === true ? { 'test.Tags': { tags: [Number.NaN] } } : { 'test.Tags': { tags: params.tags as FrozenJSON } };
      try {
        const made = context.instances.create('Item', { title: String(params.id) }, { id: String(params.id), behaviors });
        return { id: made.id, tags: made.behaviors['test.Tags']?.tags, frozen: Object.isFrozen(made) };
      } catch (error) {
        if (params.catch === true && error instanceof CreateParamsError) {
          return { refused: error.issues };
        }
        throw error;
      }
    },
  },
});

const createBehaviors = [tags, reason, maker, hold];

for (const driver of drivers) {
  function open(): Engine {
    return openBehaviorEngine({ driver, behaviors: [...testBehaviors, ...createBehaviors] });
  }

  describe(`create parameters (${driver})`, () => {
    test("a declaration's createParamsSchema is an object schema that checks every key it admits", () => {
      const engine = open();
      const refusal = (createParamsSchema: unknown) =>
        assert.throws(
          () => engine.behaviors.register(defineBehavior({ declaration: { name: 'test.Open', createParamsSchema } as BehaviorDeclaration })),
          (error: unknown) => error instanceof TypeError && /createParamsSchema must/.test(error.message)
        );
      refusal({ type: 'array' });
      refusal(true);
      refusal({ type: 'object' });
      refusal({ type: 'object', additionalProperties: true });
      assert.throws(
        () => engine.behaviors.register(defineBehavior({ declaration: { name: 'test.Odd', createParamsSchema: { type: 'object', additionalProperties: false, minProperties: 'x' } } })),
        /createParamsSchema does not compile/
      );
      engine.behaviors.register(
        defineBehavior({ declaration: { name: 'test.Keyed', createParamsSchema: { type: 'object', additionalProperties: { type: 'string' } } } })
      );
      assert.deepEqual(engine.behaviors.declaration('test.Keyed')?.createParamsSchema, { type: 'object', additionalProperties: { type: 'string' } });
    });

    test('a create asks every guard after the insert and before any initialize, then hands each initialize its own parameters', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }, { name: 'test.Counter', config: { start: 2 } }, { name: 'test.Flag' }]);
      const created = engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: { 'test.Tags': { tags: ['a', 'b'] } } });
      assert.deepEqual([created.data, created.behaviors], [
        { title: 'Desk' },
        { 'test.Tags': { tags: ['a', 'b'] }, 'test.Counter': { count: 2 }, 'test.Flag': { flagged: false } },
      ]);
      assert.deepEqual(seen, [
        {
          step: 'guard',
          columns: { tags: null },
          data: { title: 'Desk' },
          request: { kind: 'create', data: { title: 'Desk' }, behaviors: { 'test.Tags': { tags: ['a', 'b'] } } },
          frozen: true,
        },
        { step: 'initialize', params: { tags: ['a', 'b'] } },
        { step: 'afterChange', columns: { tags: '["a","b"]' } },
      ]);
      // The create event carries what the parameters set.
      assert.deepEqual(engine.events.read(alice, { schema: 'Item', instanceId: 'i1' }).events.map((event) => [event.kind, event.seq, event.change]), [
        ['create', 1, { data: { title: 'Desk' }, behaviors: { 'test.Tags': { tags: ['a', 'b'] }, 'test.Counter': { count: 2 }, 'test.Flag': { flagged: false } } }],
      ]);
      // A create that gives a behavior nothing hands its initialize {}.
      seen.length = 0;
      assert.deepEqual(engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i2' }).behaviors['test.Tags']?.tags, []);
      assert.deepEqual(
        seen.map((entry) => (entry.step === 'guard' ? (entry.request as GuardRequest & { kind: 'create' }).behaviors : entry.params ?? entry.columns)),
        [{}, {}, { tags: '[]' }]
      );
    });

    test("a guard's veto refuses the create, and leaves nothing of it", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Tags', config: { refuse: 'x' } }]);
      const vetoed = thrown(() => engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: { 'test.Tags': { tags: ['a', 'x'] } } }), BehaviorVetoError);
      assert.deepEqual(
        [vetoed.code, vetoed.behavior, vetoed.action, vetoed.message, vetoed.vetoCode, vetoed.vetoDetails],
        ['vetoed', 'test.Tags', 'create', 'behavior test.Tags vetoes create of Item i1: tag x is refused', 'refused_tag', { tag: 'x' }]
      );
      assert.deepEqual(
        seen.map((entry) => entry.step),
        ['guard']
      );
      assert.equal(engine.instances.get(alice, 'Item', 'i1'), undefined);
      assert.equal(engine.events.read(alice, { schema: 'Item' }).events.filter((event) => event.instanceId !== null).length, 0);
      assert.equal(engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: { 'test.Tags': { tags: ['a'] } } }).seq, 1);
    });

    test("a veto initialize throws refuses the create too; a create's veto whose code the declaration does not list is a BehaviorError", () => {
      const world = (config: Record<string, unknown>) => {
        const engine = open();
        publishItem(engine, [{ name: 'test.Tags', config }]);
        const create = (given: string[]) => () => engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: { 'test.Tags': { tags: given } } });
        return { engine, create };
      };
      const listed = world({ refuse: 'x' });
      const late = thrown(listed.create(['late']), BehaviorVetoError);
      assert.deepEqual([late.behavior, late.action, late.reason, late.vetoCode], ['test.Tags', 'create', 'tag late is refused', 'refused_tag']);
      assert.equal(listed.engine.instances.get(alice, 'Item', 'i1'), undefined);
      const unlisted = world({ refuse: 'x', code: 'unlisted' });
      for (const given of [['x'], ['late']]) {
        assert.match(thrown(unlisted.create(given), BehaviorError).message, /a veto's code "unlisted" is not one its declaration lists \(refused_tag\)/);
      }
      assert.equal(unlisted.engine.instances.list(alice, 'Item').items.length, 0);
    });

    test('the engine refuses parameters for a behavior the type does not compose or that takes none, and ones a createParamsSchema refuses, each at a pointer', () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }, { name: 'test.Counter' }, { name: 'test.Reason' }]);
      const issues = (behaviors: unknown) =>
        thrown(() => engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: behaviors as Record<string, unknown> }), CreateParamsError).issues;
      const reasonGiven = { 'test.Reason': { reason: 'stock' } };
      assert.deepEqual(issues({ ...reasonGiven, 'test.Nope': {} }), [
        { path: '/behaviors/test.Nope', message: 'Item does not compose behavior test.Nope (its behaviors that take create parameters: test.Tags, test.Reason)' },
      ]);
      assert.deepEqual(issues({ ...reasonGiven, 'test.Counter': {} }), [{ path: '/behaviors/test.Counter', message: 'behavior test.Counter takes no create parameters' }]);
      assert.deepEqual(
        issues({ ...reasonGiven, 'test.Tags': { tags: ['', 'b', 'c', 'd'] } }).sort((a, b) => a.path.localeCompare(b.path)),
        [
          { path: '/behaviors/test.Tags/tags', message: 'must NOT have more than 3 items' },
          { path: '/behaviors/test.Tags/tags/0', message: 'must NOT have fewer than 1 characters' },
        ]
      );
      assert.deepEqual(issues({ ...reasonGiven, 'test.Tags': { tags: [Number.NaN] } }), [{ path: '/behaviors/test.Tags/tags/0', message: 'NaN is not a JSON number' }]);
      assert.deepEqual(issues(['test.Tags']), [{ path: '/behaviors', message: "behaviors is the create's parameters by behavior name: a JSON object" }]);
      // A behavior whose createParamsSchema refuses {} needs an entry at every create.
      assert.deepEqual(issues({}), [{ path: '/behaviors/test.Reason', message: "must have required property 'reason'" }]);
      // Every issue at once, across behaviors.
      const all = thrown(() => engine.instances.create(alice, 'Item', { title: 'Desk' }, { behaviors: { 'test.Nope': {}, 'test.Tags': { tags: 'a' } } }), CreateParamsError);
      assert.deepEqual(
        all.issues.map((issue) => issue.path),
        ['/behaviors/test.Nope', '/behaviors/test.Tags/tags', '/behaviors/test.Reason']
      );
      assert.equal(all.code, 'invalid_argument');
      assert.match(all.message, /^create of Item: \/behaviors\/test\.Nope: Item does not compose behavior test\.Nope/);
      assert.equal(engine.instances.list(alice, 'Item').items.length, 0);
      assert.equal(seen.length, 0);
      engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1', behaviors: reasonGiven });
      assert.deepEqual(seen.find((entry) => entry.step === 'reason'), { step: 'reason', params: { reason: 'stock' } });
    });

    test("a behavior's instances.create gives the new instance's behaviors their parameters, held to the same checks", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }, { name: 'test.Maker' }]);
      engine.instances.create(alice, 'Item', { title: 'Maker' }, { id: 'm1' });
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i1', tags: ['made'] }), { id: 'i1', tags: ['made'], frozen: true });
      // A refusal the behavior catches leaves nothing of the create.
      assert.deepEqual(engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i2', tags: [''], catch: true }), {
        refused: [{ path: '/behaviors/test.Tags/tags/0', message: 'must NOT have fewer than 1 characters' }],
      });
      assert.equal(engine.instances.get(alice, 'Item', 'i2'), undefined);
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i2', tags: [''] }), CreateParamsError).code, 'invalid_argument');
      // Parameters that are not JSON are the behavior's defect.
      assert.match(thrown(() => engine.instances.invoke(alice, 'Item', 'm1', 'make', { id: 'i3', broken: true }), BehaviorError).message, /behaviors is not JSON/);
      assert.deepEqual(
        engine.instances.list(alice, 'Item').items.map((item) => item.id),
        ['m1', 'i1']
      );
    });

    test("describe and the tools document carry each behavior's createParamsSchema under create's behaviors argument", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }, { name: 'test.Counter' }, { name: 'test.Reason' }]);
      const described = engine.tools.describe(alice, 'Item');
      const create = described.operations.find((operation) => operation.name === 'create');
      const params = create?.params as { properties: Record<string, unknown>; required: string[] };
      assert.deepEqual(Object.keys(params.properties), ['behaviors', 'data', 'id']);
      assert.deepEqual(params.required, ['data']);
      assert.deepEqual(params.properties.behaviors, {
        type: 'object',
        description: "Each behavior's create parameters, by behavior name; they hold from the create on, in its transaction",
        additionalProperties: false,
        properties: { 'test.Tags': tagsDeclaration.createParamsSchema, 'test.Reason': engine.behaviors.declaration('test.Reason')?.createParamsSchema },
      });
      assert.match(create?.description ?? '', /behaviors gives its behaviors their create parameters \(test\.Tags, test\.Reason\)/);
      const tool = engine.tools.manifest(alice).tools.find((candidate) => candidate.name === 'item.create');
      assert.deepEqual(tool?.parameters, create?.params);
      // A schema whose behaviors take none has no behaviors argument.
      publishItem(engine, [{ name: 'test.Counter' }]);
      const plain = engine.tools.describe(alice, 'Item').operations.find((operation) => operation.name === 'create');
      assert.deepEqual(Object.keys((plain?.params as { properties: object }).properties), ['data', 'id']);
    });

    test("describe and the tools document show a schema's create parameters, preconditions and veto codes together", () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }, { name: 'test.Hold' }, { name: 'test.Counter' }]);
      const described = engine.tools.describe(alice, 'Item');
      assert.deepEqual(
        described.behaviors.map((behavior) => [behavior.name, behavior.vetoes]),
        [
          ['test.Tags', tagsDeclaration.vetoes],
          ['test.Hold', holdDeclaration.vetoes],
          ['test.Counter', []],
        ]
      );
      const paramsOf = (name: string) => described.operations.find((operation) => operation.name === name)?.params as { properties: Record<string, unknown> };
      // create takes the create parameters and no preconditions; the writes
      // of an instance take the preconditions and no create parameters.
      assert.deepEqual(Object.keys(paramsOf('create').properties), ['behaviors', 'data', 'id']);
      assert.deepEqual(Object.keys((paramsOf('create').properties.behaviors as { properties: object }).properties), ['test.Tags']);
      for (const name of ['update', 'delete', 'advance', 'increment']) {
        const properties = paramsOf(name).properties;
        assert.ok(!('behaviors' in properties), name);
        assert.deepEqual((properties.preconditions as { properties: Record<string, unknown> }).properties, { 'test.Hold': holdDeclaration.preconditionSchema }, name);
      }
      const tools = engine.tools.manifest(alice).tools;
      for (const name of ['create', 'update', 'delete', 'advance']) {
        assert.deepEqual(tools.find((tool) => tool.name === `item.${name}`)?.parameters, paramsOf(name), name);
      }
    });

    test('the create route and tool take behaviors beside data: a refusal is 400 with its issues, null is none', async () => {
      const engine = open();
      publishItem(engine, [{ name: 'test.Tags' }]);
      const authenticate: Authenticator = async () => ({ subject: 'alice', permissions: [] });
      const app = engineApp(engine, { authenticate });
      const post = (body: unknown) =>
        app.request('/namespaces/default/schemas/Item/instances', {
          method: 'POST',
          headers: { authorization: 'Bearer alice', 'content-type': 'application/json' },
          body: JSON.stringify(body),
        });
      const created = await post({ id: 'i1', data: { title: 'Desk' }, behaviors: { 'test.Tags': { tags: ['a'] } } });
      assert.equal(created.status, 201);
      const body = ((await created.json()) as { data: { data: unknown; behaviors: unknown } }).data;
      assert.deepEqual([body.data, body.behaviors], [{ title: 'Desk' }, { 'test.Tags': { tags: ['a'] } }]);
      const refused = await post({ data: { title: 'Desk' }, behaviors: { 'test.Tags': { tags: 'a' } } });
      const problem = (await refused.json()) as { code: string; details: unknown };
      assert.deepEqual([refused.status, problem.code, problem.details], [400, 'invalid_argument', { issues: [{ path: '/behaviors/test.Tags/tags', message: 'must be array' }] }]);
      assert.equal((await post({ id: 'i2', data: { title: 'Lamp' }, behaviors: null })).status, 201);
      assert.deepEqual(engine.tools.call(alice, 'item_create', { id: 'i3', data: { title: 'Rug' }, behaviors: { 'test.Tags': { tags: ['b'] } } }), engine.instances.get(alice, 'Item', 'i3'));
      assert.deepEqual(engine.instances.get(alice, 'Item', 'i3')?.behaviors['test.Tags']?.tags, ['b']);
      assert.equal(thrown(() => engine.tools.call(alice, 'item_create', { data: { title: 'Rug' }, behaviors: { 'test.Nope': {} } }), CreateParamsError).code, 'invalid_argument');
    });
  });
}
