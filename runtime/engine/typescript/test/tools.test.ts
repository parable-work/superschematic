// The describe document, the tools document and the calls the tools make:
// names and MCP handles, hidden tools and why, invocation policies (the
// default, configured ones, a behavior operation's and a distribution's
// policy), the options' checks, the core's behaviors' operations as tools,
// what each tool call does and refuses, that no tool publishes, and the
// behavior catalog.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  UnknownToolError,
  defineBehavior,
  openEngine,
  type AccessPolicy,
  type DescribedOperation,
  type Engine,
  type EngineOptions,
  type Principal,
  type ToolDefinition,
  type ToolFilter,
  type ToolSummary,
} from '../dist/index.js';
import { counterDeclaration, flagDeclaration, hold, holdDeclaration, openBehaviorEngine, openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, documentsDocument, freshPath, openTestEngine, orderDocument, schemaDocument, stepsDocument, thrown, track } from './helpers.ts';
import { reachBehaviors } from './reach-fixtures.ts';

afterEach(cleanup);

const reader: Principal = { subject: 'reader', permissions: ['read'] };

// alice may do everything; reader may read.
const policy: AccessPolicy = ({ principal, action }) => principal.subject === 'alice' || principal.permissions.includes(action);

function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
  engine.schemas.define(alice, document, { namespace });
  engine.schemas.publish(alice, document.name as string, { namespace });
}

function tool(engine: Engine, name: string, principal: Principal = alice): ToolDefinition {
  const found = engine.tools.manifest(principal).tools.find((candidate) => candidate.name === name);
  assert.ok(found, `no tool ${name}`);
  return found;
}

function operationOf(engine: Engine, schema: string, name: string): DescribedOperation {
  const found = engine.tools.describe(alice, schema).operations.find((operation) => operation.name === name);
  assert.ok(found, `no operation ${name}`);
  return found;
}

describe('the describe document', () => {
  test('describes a schema without behaviors: its instance, its five operations and their tools', () => {
    const engine = openTestEngine();
    publish(engine, orderDocument());
    const described = engine.tools.describe(alice, 'Order');
    assert.deepEqual(
      [described.namespace, described.name, described.schemaNamespace, described.version, described.instanceType, described.behaviors],
      ['default', 'Order', 'default', 1, 'Order', []]
    );
    assert.equal(described.hash, engine.schemas.live(alice, 'Order')?.hash);
    assert.deepEqual(described.instance.required, ['title']);
    assert.equal(described.instance.additionalProperties, false);
    assert.deepEqual(Object.keys(described.instance.properties as object), ['lines', 'quantity', 'status', 'title']);
    assert.deepEqual((described.instance.properties as Record<string, unknown>).status, {
      description: 'A OrderStatus value',
      enum: ['open', 'shipped', null],
      type: ['string', 'null'],
    });

    assert.deepEqual(
      described.operations.map((operation) => [operation.name, operation.writes, operation.invocationPolicy, operation.tool, operation.behavior]),
      [
        ['create', true, 'auto', 'order.create', undefined],
        ['get', false, 'auto', 'order.get', undefined],
        ['list', false, 'auto', 'order.list', undefined],
        ['update', true, 'auto', 'order.update', undefined],
        ['delete', true, 'auto', 'order.delete', undefined],
      ]
    );
    const params = Object.fromEntries(
      described.operations.map((operation) => [
        operation.name,
        [Object.keys(operation.params.properties as object), operation.params.required ?? []],
      ])
    );
    assert.deepEqual(params, {
      create: [['data', 'id'], ['data']],
      get: [['id', 'valueRefs'], ['id']],
      list: [['cursor', 'limit', 'valueRefs', 'where'], []],
      update: [['expectedSeq', 'id', 'patch'], ['id', 'patch']],
      delete: [['expectedSeq', 'id'], ['id']],
    });
    // where takes the own fields that hold a string, a number or a boolean,
    // each a value or a list of them, never null; lines is a list.
    const where = (operationOf(engine, 'Order', 'list').params.properties as Record<string, any>).where;
    assert.deepEqual(Object.keys(where.properties), ['title', 'quantity', 'status']);
    assert.deepEqual(where.properties.status.anyOf[0], { description: 'A OrderStatus value', enum: ['open', 'shipped'], type: 'string' });
    assert.deepEqual(where.properties.status.anyOf[1], { type: 'array', items: where.properties.status.anyOf[0], minItems: 1, maxItems: 100 });
    // A patch takes any of the fields, nested objects' included.
    const patch = (operationOf(engine, 'Order', 'update').params.properties as Record<string, any>).patch;
    assert.equal(patch.required, undefined);
    assert.equal(patch.properties.lines.items.required[0], 'sku', "a list is replaced whole, so its items keep what they require");
    // create, get and update return the instance, whose data is the instance schema.
    const result = operationOf(engine, 'Order', 'get').result as Record<string, any>;
    assert.deepEqual(result.properties.data, described.instance);
    assert.deepEqual(operationOf(engine, 'Order', 'delete').result, { type: 'null' });
  });

  test("describes a schema with behaviors: their fields read-only in the instance, their config, and their operations", () => {
    const engine = openBehaviorEngine();
    publishItem(engine, [{ name: 'test.Counter', config: { start: 2, limit: 9 } }, { name: 'test.Flag' }]);
    const described = engine.tools.describe(alice, 'Item');
    assert.deepEqual(described.behaviors, [
      {
        name: 'test.Counter',
        description: counterDeclaration.description,
        config: { start: 2, limit: 9 },
        fields: [{ name: 'count', description: 'The count.' }],
        operations: ['increment', 'history'],
        vetoes: [],
      },
      {
        name: 'test.Flag',
        description: flagDeclaration.description,
        config: {},
        fields: [
          { name: 'flagged', description: 'Whether the instance is flagged.' },
          { name: 'flagReason', description: 'Why; absent when it is not flagged.' },
        ],
        operations: ['flag', 'unflag'],
        vetoes: [],
      },
    ]);
    const properties = described.instance.properties as Record<string, unknown>;
    assert.deepEqual(properties.count, { description: 'The count.', readOnly: true });
    assert.deepEqual(properties.flagged, { description: 'Whether the instance is flagged.', readOnly: true });
    assert.deepEqual(described.instance.required, ['title']);
    // create's data is the type's own fields: a behavior's are not given.
    const data = (operationOf(engine, 'Item', 'create').params.properties as Record<string, any>).data;
    assert.deepEqual(Object.keys(data.properties), ['title']);

    assert.deepEqual(
      described.operations.slice(5).map((operation) => [operation.name, operation.behavior, operation.writes, operation.invocationPolicy, operation.tool]),
      [
        ['increment', 'test.Counter', true, 'auto', 'item.increment'],
        ['history', 'test.Counter', false, 'auto', 'item.history'],
        ['flag', 'test.Flag', true, 'ask', 'item.flag'],
        ['unflag', 'test.Flag', true, 'auto', 'item.unflag'],
      ]
    );
    const increment = operationOf(engine, 'Item', 'increment');
    assert.deepEqual((increment.params.properties as Record<string, unknown>).params, counterDeclaration.operations?.[0].paramsSchema);
    assert.deepEqual(increment.params.required, ['id']);
    assert.deepEqual(increment.result, counterDeclaration.operations?.[0].resultSchema);
    // flag's parameters require a reason, so the tool requires params.
    assert.deepEqual(operationOf(engine, 'Item', 'flag').params.required, ['id', 'params']);
  });

  test("shows a schema's create parameters, preconditions, veto codes and what its behaviors' validate holds the fields to, side by side", () => {
    // Step composes Constants and Variants (instanceSchema), Links (create
    // parameters, codes) and test.Hold (a precondition, codes).
    const engine = openBehaviorEngine({ behaviors: [...testBehaviors, hold] });
    const document = stepsDocument() as { types: { Step: { behaviors: unknown[] } } };
    document.types.Step.behaviors.push({ name: 'Links', config: { links: { parent: { schema: 'Step' } } } }, { name: 'test.Hold' });
    publish(engine, document);
    const described = engine.tools.describe(alice, 'Step');
    const linksDeclaration = engine.schemas.behaviors(alice, 'Step').find((bound) => bound.name === 'Links')?.declaration;
    assert.deepEqual(
      described.behaviors.map((behavior) => [behavior.name, behavior.vetoes.map((veto) => veto.code)]),
      [
        ['Constants', []],
        ['Variants', []],
        ['Links', ['no_revision', 'required_link', 'required_target']],
        ['test.Hold', ['stale', 'required', 'refused']],
      ]
    );
    // Variants' if/then per kind with a value, and one for every other kind.
    const rules = described.instance.allOf as unknown[];
    assert.equal(rules.length, 3);
    const argumentsOf = (name: string) => operationOf(engine, 'Step', name).params.properties as Record<string, any>;
    const create = argumentsOf('create');
    assert.deepEqual(create.data.allOf, rules);
    // Links' create parameters as its config takes them: parent alone, optional, with no revision.
    assert.notDeepEqual(create.behaviors.properties.Links, linksDeclaration?.createParamsSchema);
    assert.deepEqual(
      [Object.keys(create.behaviors.properties.Links.properties), create.behaviors.properties.Links.required, create.behaviors.properties.Links.additionalProperties],
      [['parent'], undefined, false]
    );
    assert.equal(create.preconditions, undefined, 'a create has nothing to fence');
    const update = argumentsOf('update');
    assert.equal((update.patch.allOf as unknown[]).length, 3);
    assert.notDeepEqual(update.patch.allOf, rules, 'the patch form');
    for (const name of ['update', 'delete', 'advance', 'link']) {
      const properties = argumentsOf(name);
      assert.deepEqual(properties.preconditions.properties, { 'test.Hold': holdDeclaration.preconditionSchema }, name);
      assert.equal(properties.behaviors, undefined, name);
    }
    // The tools document carries the same arguments.
    const createTool = tool(engine, 'step.create').parameters.properties as Record<string, any>;
    const updateTool = tool(engine, 'step.update').parameters.properties as Record<string, any>;
    assert.deepEqual([createTool.data.allOf, createTool.behaviors, createTool.preconditions], [create.data.allOf, create.behaviors, undefined]);
    assert.deepEqual([updateTool.patch.allOf, updateTool.preconditions], [update.patch.allOf, update.preconditions]);
  });

  test('describes a behavior operation with its scope; a schema-level one takes params and no id, and says where it is served', () => {
    const engine = openTestEngine({ policy, metaSchema: openMetaSchema(), behaviors: [...testBehaviors, ...reachBehaviors] });
    publishItem(engine, [{ name: 'test.Holder' }]);
    const holders = operationOf(engine, 'Item', 'holders');
    assert.deepEqual([holders.behavior, holders.scope, holders.writes, holders.tool], ['test.Holder', 'schema', false, 'item.holders']);
    assert.deepEqual(Object.keys(holders.params.properties as object), ['params']);
    assert.deepEqual(holders.params.required, ['params']);
    assert.deepEqual(operationOf(engine, 'Item', 'hold').scope, 'instance');
    assert.equal(operationOf(engine, 'Item', 'create').scope, undefined);
    assert.equal(operationOf(engine, 'Item', 'scribble').params.required, undefined, 'no parameter is required, so params is not');

    const read = tool(engine, 'item.holders');
    assert.deepEqual(
      [read.httpMethod, read.httpPath, read.mcp.hidden ? undefined : read.mcp.handle, read.replay?.mode, Object.keys(read.parameters.properties as object)],
      ['POST', '/namespaces/default/schemas/Item/operations/holders', 'item_holders', 'read_only', ['params']]
    );
    const write = tool(engine, 'item.releaseAll');
    assert.deepEqual([write.httpPath, write.replay, write.mcp.hidden ? undefined : write.mcp.handle], ['/namespaces/default/schemas/Item/operations/releaseAll', null, 'item_release_all']);
    assert.equal(tool(engine, 'item.hold').httpPath, '/namespaces/default/schemas/Item/instances/{id}/operations/hold');
    // The policy hides it from a caller it refuses, naming the operation.
    const hidden = tool(engine, 'item.releaseAll', reader).mcp;
    assert.deepEqual(hidden, { hidden: true, hiddenReason: 'the access policy refuses reader write on Item (releaseAll)' });
  });

  test('describes a schema the namespace reaches in the shared one, and refuses what it cannot describe', () => {
    const engine = openTestEngine({ policy, namespaces: { names: ['east', 'common'], shared: 'common' } });
    publish(engine, schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]), 'common');
    const note = engine.tools.describe(alice, 'Note', { namespace: 'east' });
    assert.deepEqual([note.namespace, note.schemaNamespace], ['east', 'common']);
    assert.equal(note.operations[0].tool, 'note.create');

    engine.schemas.define(alice, orderDocument());
    assert.equal(thrown(() => engine.tools.describe(alice, 'Order'), EngineError).code, 'not_found');
    assert.equal(thrown(() => engine.tools.describe({ subject: 'nobody', permissions: [] }, 'Note', { namespace: 'east' }), EngineError).code, 'forbidden');
    assert.equal(thrown(() => engine.tools.describe(alice, 'Note', { namespace: 'nowhere' }), EngineError).code, 'unknown_namespace');
  });
});

describe('the tools document', () => {
  test("names each tool as the SDK generators do and derives its MCP handle; no tool publishes", () => {
    const engine = openBehaviorEngine();
    publishItem(engine, [{ name: 'test.Counter' }]);
    publish(engine, schemaDocument('LineItem', [{ name: 'sku', typeRef: { name: 'string' } }]));
    const manifest = engine.tools.manifest(alice);
    assert.deepEqual([manifest.$schema, manifest.title], ['https://json-schema.org/draft/2020-12/schema', 'Namespace default Tool Definitions']);
    assert.deepEqual(
      manifest.tools.map((entry) => [entry.name, entry.mcp.hidden ? undefined : entry.mcp.handle, entry.namespace, entry.methodName]),
      [
        ['engine.listSchemas', 'list_schemas', 'engine', 'listSchemas'],
        ['engine.describeSchema', 'describe_schema', 'engine', 'describeSchema'],
        ['engine.defineSchema', 'define_schema', 'engine', 'defineSchema'],
        ['engine.listBehaviors', 'list_behaviors', 'engine', 'listBehaviors'],
        ['engine.describeBehavior', 'describe_behavior', 'engine', 'describeBehavior'],
        ['engine.getValue', 'get_value', 'engine', 'getValue'],
        ['engine.listNamespaces', 'list_namespaces', 'engine', 'listNamespaces'],
        ['engine.createNamespace', 'create_namespace', 'engine', 'createNamespace'],
        ['engine.archiveNamespace', 'archive_namespace', 'engine', 'archiveNamespace'],
        ['engine.unarchiveNamespace', 'unarchive_namespace', 'engine', 'unarchiveNamespace'],
        ['item.create', 'item_create', 'item', 'create'],
        ['item.get', 'item_get', 'item', 'get'],
        ['item.list', 'item_list', 'item', 'list'],
        ['item.update', 'item_update', 'item', 'update'],
        ['item.delete', 'item_delete', 'item', 'delete'],
        ['item.increment', 'item_increment', 'item', 'increment'],
        ['item.history', 'item_history', 'item', 'history'],
        ['line-item.create', 'line_item_create', 'line-item', 'create'],
        ['line-item.get', 'line_item_get', 'line-item', 'get'],
        ['line-item.list', 'line_item_list', 'line-item', 'list'],
        ['line-item.update', 'line_item_update', 'line-item', 'update'],
        ['line-item.delete', 'line_item_delete', 'line-item', 'delete'],
      ]
    );
    for (const entry of manifest.tools) {
      assert.ok(!/publish/i.test(`${entry.name} ${entry.mcp.hidden ? '' : entry.mcp.handle} ${entry.httpPath}`), `${entry.name} does not publish`);
    }
    assert.ok(thrown(() => engine.tools.call(alice, 'publish_schema', { name: 'Item' }), UnknownToolError));

    const increment = tool(engine, 'item.increment');
    assert.deepEqual(increment.mcp, {
      hidden: false,
      name: 'Item: increment',
      handle: 'item_increment',
      description: 'Adds to the count.',
      invocationPolicy: 'auto',
      _meta: { 'superschematic/operation-guidance': { useWhen: '', doNotUseWhen: '', success: '', errors: [] } },
    });
    assert.deepEqual(
      [increment.httpMethod, increment.httpPath, increment.requiresAuth, increment.replay, increment.bindingStatus],
      ['POST', '/namespaces/default/schemas/Item/instances/{id}/operations/increment', true, null, 'ready']
    );
    assert.deepEqual(increment.returns, { type: 'object', description: 'The result of increment' });
    assert.deepEqual(tool(engine, 'item.history').replay, { mode: 'read_only', idempotencyKeyPointers: [], expectedRevisionPointers: [] });
    assert.deepEqual(tool(engine, 'item.history').returns, { type: 'array', description: 'The result of history', items: { type: 'integer' } });
    assert.match(increment.inputSchemaDigest, /^sha256:[0-9a-f]{64}$/);
    assert.notEqual(increment.inputSchemaDigest, tool(engine, 'item.history').inputSchemaDigest);
  });

  test('hides a tool whose handle is not one @mcp takes, or that another tool derives too, with the reason', () => {
    const lister = defineBehavior({
      declaration: {
        name: 'test.Lister',
        operations: [{ name: 'schemas', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true }],
      },
      operations: { schemas: () => [] },
    });
    const engine = openTestEngine({ metaSchema: openMetaSchema(), behaviors: [lister] });
    const listed = schemaDocument('List', [{ name: 'title', typeRef: { name: 'string' } }]) as { types: { List: Record<string, unknown> } };
    listed.types.List.behaviors = [{ name: 'test.Lister' }];
    for (const document of [
      schemaDocument('LineItem', [{ name: 'sku', typeRef: { name: 'string' } }]),
      schemaDocument('Line_Item', [{ name: 'sku', typeRef: { name: 'string' } }]),
      schemaDocument('Tail-', [{ name: 'sku', typeRef: { name: 'string' } }]),
      schemaDocument(`A${'b'.repeat(45)}`, [{ name: 'sku', typeRef: { name: 'string' } }]),
      listed,
    ]) {
      publish(engine, document);
    }
    const hidden = (name: string) => {
      const entry = tool(engine, name);
      assert.equal(entry.mcp.hidden, true, name);
      return (entry.mcp as { hiddenReason: string }).hiddenReason;
    };
    assert.equal(hidden('line-item.create'), 'its MCP handle line_item_create is also the handle of line_item.create');
    assert.equal(hidden('line_item.create'), 'its MCP handle line_item_create is also the handle of line-item.create');
    assert.equal(hidden('tail-.get'), 'its MCP handle tail__get is not lowercase snake case starting with a letter');
    assert.equal(hidden(`a${'b'.repeat(45)}.delete`), `its MCP handle a${'b'.repeat(45)}_delete is longer than 48 characters`);
    assert.equal(hidden('list.schemas'), "its MCP handle list_schemas is the handle of the engine's tool engine.listSchemas");
    assert.equal(tool(engine, 'list.create').mcp.hidden, false);
    // The engine's own tool keeps its handle, and a hidden tool has none to call.
    assert.deepEqual(engine.tools.call(alice, 'list_schemas', {}), engine.schemas.list(alice));
    assert.ok(thrown(() => engine.tools.call(alice, 'line_item_create', { data: {} }), UnknownToolError));
    assert.ok(thrown(() => engine.tools.call(alice, 'tail__get', { id: 'x' }), UnknownToolError));
  });

  test('hides a tool the access policy refuses the caller, and lists no tool of a schema it may not read', () => {
    const engine = openBehaviorEngine({ policy: (request) => policy(request) && !(request.principal.subject === 'reader' && request.schema === 'Secret') });
    publishItem(engine, [{ name: 'test.Counter' }]);
    publish(engine, schemaDocument('Secret', [{ name: 'code', typeRef: { name: 'string' } }]));
    const tools = engine.tools.manifest(reader).tools;
    assert.deepEqual(
      tools.filter((entry) => !entry.mcp.hidden).map((entry) => entry.name),
      ['engine.listSchemas', 'engine.describeSchema', 'engine.listBehaviors', 'engine.describeBehavior', 'engine.getValue', 'item.get', 'item.list', 'item.history']
    );
    assert.equal(
      (tool(engine, 'item.increment', reader).mcp as { hiddenReason: string }).hiddenReason,
      'the access policy refuses reader write on Item (increment)'
    );
    // define_schema and the namespace tools ask the listing question their
    // calls would answer: the policy refuses reader define and manage.
    assert.equal((tool(engine, 'engine.defineSchema', reader).mcp as { hiddenReason: string }).hiddenReason, 'the access policy refuses reader define in namespace default');
    assert.equal(
      (tool(engine, 'engine.archiveNamespace', reader).mcp as { hiddenReason: string }).hiddenReason,
      'the access policy refuses reader manage (archive) of namespaces'
    );
    // A hidden engine tool can still be called; the call asks its own question.
    assert.equal(thrown(() => engine.tools.call(reader, 'define_schema', { document: schemaDocument('Other', []) }), EngineError).code, 'forbidden');
    assert.equal((tool(engine, 'item.create', reader).mcp as { hiddenReason: string }).hiddenReason, 'the access policy refuses reader write on Item');
    assert.ok(!tools.some((entry) => entry.name.startsWith('secret.')));
    assert.ok(tool(engine, 'secret.create', alice));
  });

  test("a mount's filter narrows a caller's tools: hidden with its reason, and its call is a tool the namespace does not have", () => {
    const engine = openBehaviorEngine({ policy });
    publishItem(engine, [{ name: 'test.Counter' }]);
    publish(engine, schemaDocument('Other', [{ name: 'code', typeRef: { name: 'string' } }]));
    const seen: ToolSummary[] = [];
    // An agent's session: Item's operations alone, and nothing that writes but increment.
    const filter: ToolFilter = (principal, summary) => {
      seen.push(summary);
      return principal.subject !== 'agent' || (summary.schema === 'Item' && (!summary.writes || summary.operation === 'increment'));
    };
    const agent: Principal = { subject: 'agent', permissions: ['read', 'write'] };
    const visible = engine.tools
      .manifest(agent, { filter })
      .tools.filter((entry) => !entry.mcp.hidden)
      .map((entry) => entry.name);
    assert.deepEqual(visible, ['item.get', 'item.list', 'item.increment', 'item.history']);
    assert.equal(
      (engine.tools.manifest(agent, { filter }).tools.find((entry) => entry.name === 'other.get')?.mcp as { hiddenReason: string }).hiddenReason,
      "this mount's tool filter leaves it out of agent's tools"
    );
    assert.deepEqual(
      seen.find((summary) => summary.name === 'item.increment'),
      { handle: 'item_increment', name: 'item.increment', schema: 'Item', operation: 'increment', behavior: 'test.Counter', writes: true }
    );
    assert.deepEqual(seen.find((summary) => summary.name === 'engine.listSchemas'), { handle: 'list_schemas', name: 'engine.listSchemas', operation: 'listSchemas', writes: false });
    engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i1' });
    assert.deepEqual(engine.tools.call(agent, 'item_increment', { id: 'i1' }, { filter }), { count: 1 });
    assert.ok(thrown(() => engine.tools.call(agent, 'item_create', { data: { title: 'Desk' } }, { filter }), UnknownToolError));
    assert.ok(thrown(() => engine.tools.call(agent, 'list_schemas', {}, { filter }), UnknownToolError));
    // Another caller through the same filter keeps every tool, and with no filter the agent does too.
    assert.ok(engine.tools.manifest(alice, { filter }).tools.every((entry) => !entry.mcp.hidden));
    assert.equal((engine.tools.call(agent, 'list_schemas', {}) as unknown[]).length, 2);
  });

  test('lists no tool of a schema whose behaviors the engine cannot run', () => {
    const path = freshPath();
    const first = openTestEngine({ path, metaSchema: openMetaSchema(), behaviors: testBehaviors });
    publishItem(first, [{ name: 'test.Counter' }]);
    first.close();
    const engine = openTestEngine({ path, metaSchema: openMetaSchema() });
    publish(engine, orderDocument());
    assert.deepEqual(
      engine.tools.manifest(alice).tools.map((entry) => entry.namespace),
      [...Array.from({ length: 10 }, () => 'engine'), 'order', 'order', 'order', 'order', 'order']
    );
  });
});

describe('invocation policies', () => {
  test('a built-in operation takes the default unless configured, a behavior operation its declaration', () => {
    const engine = openBehaviorEngine({ tools: { invocation: { delete: 'ask', defineSchema: 'ask' } } });
    publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Flag' }]);
    const policies = Object.fromEntries(
      engine.tools.manifest(alice).tools.map((entry) => [entry.name, entry.mcp.hidden ? undefined : entry.mcp.invocationPolicy])
    );
    assert.deepEqual(policies, {
      'engine.listSchemas': 'auto',
      'engine.describeSchema': 'auto',
      'engine.defineSchema': 'ask',
      'engine.listBehaviors': 'auto',
      'engine.describeBehavior': 'auto',
      'engine.getValue': 'auto',
      'engine.listNamespaces': 'auto',
      'engine.createNamespace': 'auto',
      'engine.archiveNamespace': 'auto',
      'engine.unarchiveNamespace': 'auto',
      'item.create': 'auto',
      'item.get': 'auto',
      'item.list': 'auto',
      'item.update': 'auto',
      'item.delete': 'ask',
      'item.increment': 'auto',
      'item.history': 'auto',
      'item.flag': 'ask',
      'item.unflag': 'auto',
    });
    assert.equal(operationOf(engine, 'Item', 'delete').invocationPolicy, 'ask');
    assert.equal(operationOf(engine, 'Item', 'flag').invocationPolicy, 'ask');
  });

  test("a distribution's policy: its key everywhere, its default, its values", () => {
    const review = { key: 'review', values: ['never', 'on-write', 'always'], default: 'on-write' };
    const flagUnderReview = defineBehavior({
      declaration: { ...flagDeclaration, operations: flagDeclaration.operations?.map((operation) => (operation.name === 'flag' ? { ...operation, invocationPolicy: 'always' } : operation)) },
      migrations: [{ version: 1, name: 'flag', columns: { flagged: { type: 'integer', notNull: true, default: 0 }, reason: { type: 'text' } } }],
      operations: { flag: () => true, unflag: () => false },
      fields: { flagged: () => false, flagReason: () => undefined },
    });
    const engine = openTestEngine({
      metaSchema: openMetaSchema(),
      behaviors: [flagUnderReview],
      tools: { invocationPolicy: review, invocation: { get: 'never', list: 'never' } },
    });
    publishItem(engine, [{ name: 'test.Flag' }]);
    const flagTool = tool(engine, 'item.flag');
    assert.deepEqual(Object.keys(flagTool.mcp), ['hidden', 'name', 'handle', 'description', 'review', '_meta']);
    assert.deepEqual(
      engine.tools.manifest(alice).tools.map((entry) => [entry.name, entry.mcp.hidden ? undefined : entry.mcp.review]),
      [
        ['engine.listSchemas', 'on-write'],
        ['engine.describeSchema', 'on-write'],
        ['engine.defineSchema', 'on-write'],
        ['engine.listBehaviors', 'on-write'],
        ['engine.describeBehavior', 'on-write'],
        ['engine.getValue', 'on-write'],
        ['engine.listNamespaces', 'on-write'],
        ['engine.createNamespace', 'on-write'],
        ['engine.archiveNamespace', 'on-write'],
        ['engine.unarchiveNamespace', 'on-write'],
        ['item.create', 'on-write'],
        ['item.get', 'never'],
        ['item.list', 'never'],
        ['item.update', 'on-write'],
        ['item.delete', 'on-write'],
        ['item.flag', 'always'],
        ['item.unflag', 'on-write'],
      ]
    );
    const described = operationOf(engine, 'Item', 'flag');
    assert.deepEqual([described.review, described.invocationPolicy], ['always', undefined]);

    // Under this policy, test.Flag's own declaration ("ask") does not register.
    const refused = thrown(
      () => track(openEngine({ path: freshPath(), policy: () => true, behaviors: testBehaviors, tools: { invocationPolicy: review } })),
      TypeError
    );
    assert.match(refused.message, /operation flag invocationPolicy "ask" is not one of the review values \(never, on-write, always\)/);
  });

  test("the tool options are checked as the compiler's registry checks them", () => {
    const refusal = (tools: EngineOptions['tools']) => thrown(() => openTestEngine({ tools }), TypeError).message;
    assert.match(refusal({ invocationPolicy: { key: 'handle', values: ['auto'], default: 'auto' } }), /key "handle" is a key @mcp already uses/);
    assert.match(refusal({ invocationPolicy: { key: '9lives', values: ['auto'], default: 'auto' } }), /must be a letter followed by/);
    assert.match(refusal({ invocationPolicy: { key: 'confirm', values: [], default: 'auto' } }), /lists no values/);
    assert.match(refusal({ invocationPolicy: { key: 'confirm', values: ['Never', 'never', 'never'], default: 'always' } }), /"Never" must be lowercase.*never is listed twice.*default "always" is not one of/);
    assert.match(refusal({ invocation: { delete: 'maybe' } }), /the invocation policy of delete, "maybe", is not one of auto, ask/);
    assert.match(refusal({ invocation: { publish: 'ask' } as never }), /invocation names publish, which is not one of create/);
    assert.match(refusal({ keys: { parameters: [{ key: 'type', value: 1 }] } }), /"type" is written by the core/);
    assert.match(refusal({ keys: { scalar: 'x-s', parameters: [{ key: 'x-s', value: 1 }] } }), /"x-s" is the scalar key/);
    assert.match(refusal({ keys: { parameters: [{ key: 'x-a', value: 1 }, { key: 'x-a', value: 2 }] } }), /"x-a" is listed twice/);
    assert.match(refusal({ keys: { parameters: [{ key: 'x-a', value: Number.NaN }] } }), /"x-a" is not JSON/);
  });
});

describe("the core's behaviors", () => {
  // An engine with the core meta-schema and only its own behaviors, and
  // documents, which composes all three. No core operation names an
  // invocation policy, so each takes the default of the policy the engine
  // is given.
  const tools = (engine: Engine) => engine.tools.manifest(alice).tools.filter((entry) => entry.namespace === 'documents');

  test('their operations are tools with the default policy, and the list operations only read', () => {
    const engine = openTestEngine({ policy });
    publish(engine, documentsDocument());
    assert.deepEqual(
      tools(engine).map((entry) => [entry.name, entry.mcp.hidden ? entry.mcp.hiddenReason : entry.mcp.handle, entry.replay?.mode ?? 'writes']),
      [
        ['documents.create', 'documents_create', 'writes'],
        ['documents.get', 'documents_get', 'read_only'],
        ['documents.list', 'documents_list', 'read_only'],
        ['documents.update', 'documents_update', 'writes'],
        ['documents.delete', 'documents_delete', 'writes'],
        ['documents.transition', 'documents_transition', 'writes'],
        ['documents.comment', 'documents_comment', 'writes'],
        ['documents.listComments', 'documents_list_comments', 'read_only'],
        ['documents.listRevisions', 'documents_list_revisions', 'read_only'],
        ['documents.propose', 'documents_propose', 'writes'],
        ['documents.approve', 'documents_approve', 'writes'],
        ['documents.reject', 'documents_reject', 'writes'],
        ['documents.listProposals', 'documents_list_proposals', 'read_only'],
      ]
    );
    for (const entry of tools(engine)) {
      assert.equal(entry.mcp.hidden ? undefined : entry.mcp.invocationPolicy, 'auto', entry.name);
    }
    const transition = tool(engine, 'documents.transition');
    assert.equal(transition.httpPath, '/namespaces/default/schemas/documents/instances/{id}/operations/transition');
    assert.deepEqual((transition.parameters.properties as Record<string, unknown>).params, {
      additionalProperties: false,
      properties: { to: { description: 'The state to move to.', minLength: 1, type: 'string' } },
      required: ['to'],
      type: 'object',
    });

    // The describe document names each operation's behavior and policy, and
    // holds the behaviors' fields read-only in the instance.
    const described = engine.tools.describe(alice, 'documents');
    assert.deepEqual(
      described.operations.filter((operation) => operation.behavior !== undefined).map((operation) => [operation.behavior, operation.name, operation.writes, operation.invocationPolicy]),
      [
        ['Workflow', 'transition', true, 'auto'],
        ['Comments', 'comment', true, 'auto'],
        ['Comments', 'listComments', false, 'auto'],
        ['Revisions', 'listRevisions', false, 'auto'],
        ['Revisions', 'propose', true, 'auto'],
        ['Revisions', 'approve', true, 'auto'],
        ['Revisions', 'reject', true, 'auto'],
        ['Revisions', 'listProposals', false, 'auto'],
      ]
    );
    const properties = described.instance.properties as Record<string, { readOnly?: boolean }>;
    assert.deepEqual(
      ['status', 'commentCount', 'revision', 'title'].map((field) => properties[field]?.readOnly === true),
      [true, true, true, false]
    );
  });

  test("under a distribution's policy they take its default, and the engine still opens", () => {
    const confirm = { key: 'confirm', values: ['never', 'always'], default: 'always' };
    const engine = openTestEngine({ policy, tools: { invocationPolicy: confirm, invocation: { get: 'never', list: 'never' } } });
    publish(engine, documentsDocument());
    assert.deepEqual(
      tools(engine).map((entry) => [entry.name, entry.mcp.hidden ? undefined : entry.mcp.confirm]),
      [
        ['documents.create', 'always'],
        ['documents.get', 'never'],
        ['documents.list', 'never'],
        ['documents.update', 'always'],
        ['documents.delete', 'always'],
        ['documents.transition', 'always'],
        ['documents.comment', 'always'],
        ['documents.listComments', 'always'],
        ['documents.listRevisions', 'always'],
        ['documents.propose', 'always'],
        ['documents.approve', 'always'],
        ['documents.reject', 'always'],
        ['documents.listProposals', 'always'],
      ]
    );
  });
});

describe('tool calls', () => {
  test('run the operation their handle names, through the engine', () => {
    const engine = openBehaviorEngine({ policy });
    publishItem(engine, [{ name: 'test.Counter' }]);
    const created = engine.tools.call(alice, 'item_create', { id: 'i1', data: { title: 'Desk' } }) as { id: string; seq: number; data: unknown };
    assert.deepEqual([created.id, created.seq, created.data], ['i1', 1, { title: 'Desk', count: 0 }]);
    assert.match((engine.tools.call(alice, 'item_create', { data: { title: 'Lamp' } }) as { id: string }).id, /^[0-9a-f-]{36}$/);
    assert.deepEqual(engine.tools.call(alice, 'item_get', { id: 'i1' }), engine.instances.get(alice, 'Item', 'i1'));
    assert.equal((engine.tools.call(alice, 'item_list', { limit: 1 }) as { items: unknown[] }).items.length, 1);
    assert.deepEqual(engine.tools.call(alice, 'item_increment', { id: 'i1', params: { by: 3 }, expectedSeq: 1 }), { count: 3 });
    assert.deepEqual(engine.tools.call(alice, 'item_history', { id: 'i1' }), [3]);
    const updated = engine.tools.call(alice, 'item_update', { id: 'i1', patch: { title: 'Table' }, expectedSeq: 2 }) as { seq: number; data: unknown };
    assert.deepEqual([updated.seq, updated.data], [3, { title: 'Table', count: 3 }]);
    assert.equal(thrown(() => engine.tools.call(alice, 'item_delete', { id: 'i1', expectedSeq: 2 }), EngineError).code, 'seq_mismatch');
    assert.equal(engine.tools.call(alice, 'item_delete', { id: 'i1', expectedSeq: 3 }), null);
    assert.equal(thrown(() => engine.tools.call(alice, 'item_get', { id: 'i1' }), EngineError).code, 'not_found');
    assert.equal(thrown(() => engine.tools.call(alice, 'item_delete', { id: 'i1' }), EngineError).code, 'not_found');
  });

  test('a schema-level tool runs its operation with params and no instance', () => {
    const engine = openTestEngine({ policy, metaSchema: openMetaSchema(), behaviors: [...testBehaviors, ...reachBehaviors] });
    publishItem(engine, [{ name: 'test.Holder' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'i2' });
    engine.tools.call(alice, 'item_hold', { id: 'i2', params: { schema: 'Item', id: 'i1' } });
    assert.deepEqual(engine.tools.call(reader, 'item_holders', { params: { schema: 'Item', id: 'i1' } }), ['i2']);
    assert.equal(thrown(() => engine.tools.call(alice, 'item_holders', { id: 'i2', params: { schema: 'Item', id: 'i1' } }), EngineError).code, 'invalid_argument');
    assert.equal(thrown(() => engine.tools.call(alice, 'item_holders', {}), EngineError).code, 'invalid_argument');
    assert.equal(thrown(() => engine.tools.call(reader, 'item_release_all', { params: { schema: 'Item', id: 'i1' } }), EngineError).code, 'forbidden');
    assert.equal(engine.tools.call(alice, 'item_release_all', { params: { schema: 'Item', id: 'i1' } }), 1);
    assert.deepEqual(engine.tools.call(alice, 'item_holders', { params: { schema: 'Item', id: 'i1' } }), []);
  });

  test('the schema tools list, describe and define a draft, which stays a draft', () => {
    const engine = openTestEngine({ policy });
    assert.deepEqual(engine.tools.call(alice, 'list_schemas', undefined), []);
    const draft = engine.tools.call(alice, 'define_schema', { document: orderDocument() }) as Record<string, unknown>;
    assert.deepEqual([draft.name, draft.version, 'canonical' in draft], ['Order', null, false]);
    assert.deepEqual(engine.tools.call(alice, 'list_schemas', {}), [{ namespace: 'default', name: 'Order', liveVersion: null, hasDraft: true }]);
    assert.equal(thrown(() => engine.tools.call(alice, 'describe_schema', { name: 'Order' }), EngineError).code, 'not_found');
    engine.schemas.publish(alice, 'Order');
    assert.deepEqual(engine.tools.call(alice, 'describe_schema', { name: 'Order' }), engine.tools.describe(alice, 'Order'));
    // define is asked of the policy with the document's name.
    assert.equal(thrown(() => engine.tools.call(reader, 'define_schema', { document: orderDocument() }), EngineError).code, 'forbidden');
    assert.equal(thrown(() => engine.tools.call(alice, 'define_schema', { document: { kind: 'General' } }), EngineError).code, 'invalid_schema');
  });

  test('refuse arguments of the wrong shape, a write the policy refuses and a tool the namespace does not have', () => {
    const engine = openBehaviorEngine({ policy });
    publishItem(engine, [{ name: 'test.Counter' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    for (const [handle, args] of [
      ['item_get', {}],
      ['item_get', { id: 7 }],
      ['item_get', { id: 'i1', extra: true }],
      ['item_get', ['i1']],
      ['item_create', { id: 'i2' }],
      ['item_create', { id: 3, data: { title: 'Lamp' } }],
      ['item_update', { id: 'i1' }],
      ['item_update', { id: 'i1', patch: {}, expectedSeq: 'one' }],
      ['item_list', { limit: 'ten' }],
      ['item_list', { limit: 0 }],
      ['item_increment', { id: 'i1', params: { by: 'two' } }],
      ['describe_schema', { name: 3 }],
      ['define_schema', { document: 'text' }],
      ['list_schemas', { all: true }],
      ['list_behaviors', { name: 'Workflow' }],
      ['describe_behavior', {}],
      ['describe_behavior', { name: 'not a name' }],
    ] as const) {
      assert.equal(thrown(() => engine.tools.call(alice, handle, args), EngineError).code, 'invalid_argument', `${handle} ${JSON.stringify(args)}`);
    }
    assert.equal(thrown(() => engine.tools.call(alice, 'item_create', { data: { title: 3 } }), EngineError).code, 'invalid_instance');
    assert.equal(thrown(() => engine.tools.call(reader, 'item_increment', { id: 'i1' }), EngineError).code, 'forbidden');
    assert.equal(thrown(() => engine.tools.call(reader, 'item_create', { data: { title: 'Lamp' } }), EngineError).code, 'forbidden');
    assert.deepEqual(engine.tools.call(reader, 'item_history', { id: 'i1' }), []);
    for (const handle of ['item_publish', 'order_create', '', 'Item_get']) {
      const unknown = thrown(() => engine.tools.call(alice, handle, {}), UnknownToolError);
      assert.equal(unknown.code, 'not_found');
    }
    // A schema the caller may not read has no tools to it.
    const nobody = { subject: 'nobody', permissions: [] };
    assert.ok(thrown(() => engine.tools.call(nobody, 'item_get', { id: 'i1' }), UnknownToolError));
    assert.equal(thrown(() => engine.tools.call(alice, 'item_get', { id: 'i1' }, { namespace: 'nowhere' }), EngineError).code, 'unknown_namespace');
  });
});

describe('the behavior catalog', () => {
  test('lists every behavior the engine runs and describes each with its defaults filled in, asking the policy nothing', () => {
    const asked: string[] = [];
    const engine = openTestEngine({
      metaSchema: openMetaSchema(),
      behaviors: testBehaviors,
      policy: (request) => {
        asked.push(request.action);
        return false;
      },
    });
    const nobody = { subject: 'nobody', permissions: [] };
    const summaries = engine.tools.listBehaviors(nobody);
    assert.deepEqual(
      summaries.map((summary) => summary.name),
      engine.behaviors.names()
    );
    assert.deepEqual(
      summaries.find((summary) => summary.name === 'test.Counter'),
      { name: 'test.Counter', description: counterDeclaration.description, requires: [], conflicts: [], fields: ['count'], operations: ['increment', 'history'] }
    );
    assert.deepEqual(engine.tools.describeBehavior(nobody, 'test.Counter'), {
      name: 'test.Counter',
      description: counterDeclaration.description,
      configSchema: counterDeclaration.configSchema,
      requires: [],
      conflicts: [],
      fields: [{ name: 'count', description: 'The count.' }],
      operations: [
        {
          name: 'increment',
          description: 'Adds to the count.',
          scope: 'instance',
          writes: true,
          invocationPolicy: 'auto',
          paramsSchema: counterDeclaration.operations?.[0].paramsSchema,
          resultSchema: counterDeclaration.operations?.[0].resultSchema,
        },
        {
          name: 'history',
          description: 'Lists what each increment added.',
          scope: 'instance',
          writes: false,
          invocationPolicy: 'auto',
          paramsSchema: counterDeclaration.operations?.[1].paramsSchema,
          resultSchema: counterDeclaration.operations?.[1].resultSchema,
        },
      ],
      vetoes: [],
    });
    // Everything a declaration carries reaches the document.
    const described = engine.tools.describeBehavior(nobody, 'Links');
    const links = engine.behaviors.declaration('Links');
    assert.deepEqual(described.createParamsSchema, links?.createParamsSchema);
    assert.deepEqual(
      described.vetoes.map((veto) => veto.code),
      (links?.vetoes ?? []).map((veto) => veto.code)
    );
    assert.deepEqual(engine.tools.call(nobody, 'list_behaviors', {}), summaries);
    assert.deepEqual(engine.tools.call(nobody, 'describe_behavior', { name: 'Workflow' }), engine.tools.describeBehavior(nobody, 'Workflow'));
    assert.equal(thrown(() => engine.tools.describeBehavior(nobody, 'test.Missing'), EngineError).code, 'not_found');
    assert.deepEqual(asked, [], 'the catalog names no schema, so the policy is not asked');
  });

  test("a schema-level operation, a precondition schema and a distribution's policy key reach a behavior's document", () => {
    const review = { key: 'review', values: ['never', 'on-write', 'always'], default: 'on-write' };
    const engine = openTestEngine({ metaSchema: openMetaSchema(), behaviors: [...reachBehaviors, hold], tools: { invocationPolicy: review } });
    const holder = engine.tools.describeBehavior(alice, 'test.Holder');
    const holders = holder.operations.find((operation) => operation.name === 'holders');
    assert.deepEqual([holders?.scope, holders?.writes, holders?.review], ['schema', false, 'on-write']);
    // The policy sits after writes whatever its key, as in the describe document.
    assert.deepEqual(Object.keys(holders ?? {}), ['name', 'description', 'scope', 'writes', 'review', 'paramsSchema', 'resultSchema']);
    assert.deepEqual(engine.tools.describeBehavior(alice, 'test.Hold').preconditionSchema, holdDeclaration.preconditionSchema);
  });
});
