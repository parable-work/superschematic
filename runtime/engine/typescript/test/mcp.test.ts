// The MCP endpoint over a listening server, with the official MCP client
// (@modelcontextprotocol/client): initialize, tools/list and tools/call
// through the HTTP runtime's gate and the engine's access policy, a
// refused call as a tool error carrying the problem document, an unknown
// tool as a JSON-RPC error, the 2026-07-28 revision beside the 2025 ones,
// the behavior catalog's tools, service callers, and the core's behaviors'
// operations as tools. The HTTP API and the MCP endpoint are mounted on
// one app. On Node.js the server is
// @hono/node-server, on Bun it is Bun.serve.
import assert from 'node:assert/strict';
import type { Server as NodeServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, test } from 'node:test';

import { serve as serveNode } from '@hono/node-server';
import { Client, ProtocolError, ProtocolErrorCode, StreamableHTTPClientTransport, type CallToolResult } from '@modelcontextprotocol/client';
import type { Authenticator, ServiceAuthenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { defineBehavior, type AccessPolicy, type Engine, type EngineOptions } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { MCP_PATH, engineMcp, type EngineMcpOptions } from '../dist/mcp/index.js';
import { hold, holdDeclaration, openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import {
  alice,
  cleanup,
  documentsDocument,
  notesDocument,
  openTestEngine,
  orderDocument,
  projectsDocument,
  schemaDocument,
  stepsDocument,
  tasksDocument,
} from './helpers.ts';
import { reachBehaviors } from './reach-fixtures.ts';

// The bearer token is the caller's subject; reader may only read.
const authenticate: Authenticator = async (ctx) => {
  const token = ctx.bearerToken;
  if (token === undefined || !['alice', 'reader'].includes(token)) {
    return null;
  }
  return { subject: token, permissions: token === 'reader' ? ['read'] : ['*'] };
};

// alice, whom the fixtures act as, may do everything.
const policy: AccessPolicy = ({ principal, action }) =>
  principal.subject === 'alice' || principal.permissions.includes('*') || principal.permissions.includes(action);

// A behavior whose operation fails in its own code: a defect, not a refusal.
const broken = defineBehavior({
  declaration: {
    name: 'test.Broken',
    operations: [{ name: 'explode', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true }],
  },
  operations: {
    explode() {
      throw new Error('the handler failed with a secret: 42');
    },
  },
});

interface Listening {
  url: string;
  close(): Promise<void>;
}

interface BunRuntime {
  serve(options: { fetch: (request: Request) => Response | Promise<Response>; port: number; hostname: string }): { port: number; stop(force?: boolean): unknown };
}

async function listen(app: Hono): Promise<Listening> {
  const bun = (globalThis as { Bun?: BunRuntime }).Bun;
  if (bun) {
    const server = bun.serve({ fetch: (request) => app.fetch(request), port: 0, hostname: '127.0.0.1' });
    return { url: `http://127.0.0.1:${server.port}`, close: async () => void server.stop(true) };
  }
  return new Promise((resolve) => {
    const server = serveNode({ fetch: app.fetch, port: 0, hostname: '127.0.0.1' }, (info: AddressInfo) => {
      resolve({
        url: `http://127.0.0.1:${info.port}`,
        close: () =>
          new Promise<void>((done) => {
            (server as NodeServer).closeAllConnections();
            server.close(() => done());
          }),
      });
    }) as NodeServer;
  });
}

const servers: Listening[] = [];
const clients: Client[] = [];

afterEach(async () => {
  for (const client of clients.splice(0)) {
    await client.close();
  }
  for (const server of servers.splice(0)) {
    await server.close();
  }
  cleanup();
});

interface Served {
  engine: Engine;
  url: string;
}

const everything = { subject: 'alice', permissions: ['*'] };

// served opens an engine with the test behaviors and an Item that
// composes them, with one instance, and serves it; setup replaces the Item.
async function served(
  engineOptions: Partial<EngineOptions> = {},
  mcpOptions: EngineMcpOptions = {},
  setup: (engine: Engine) => void = (engine) => {
    publishItem(engine, [{ name: 'test.Counter', config: { start: 0, limit: 5 } }, { name: 'test.Flag' }, { name: 'test.Broken' }]);
    engine.instances.create(everything, 'Item', { title: 'Desk' }, { id: 'i1' });
  }
): Promise<Served> {
  const engine = openTestEngine({
    policy,
    metaSchema: openMetaSchema(),
    behaviors: [...testBehaviors, broken],
    namespaces: { names: ['east'] },
    ...engineOptions,
  });
  setup(engine);
  const app = new Hono();
  app.route('/api', engineApp(engine, { authenticate }));
  app.route('/api', engineMcp(engine, { authenticate, ...mcpOptions }));
  const listening = await listen(app);
  servers.push(listening);
  return { engine, url: listening.url };
}

function endpoint(url: string, namespace = 'default'): URL {
  return new URL(`${url}/api${MCP_PATH.replace('{namespace}', namespace)}`);
}

async function connect(
  url: URL,
  token: string | null = 'alice',
  mode: 'legacy' | 'auto' = 'legacy',
  headers: Record<string, string> = {}
): Promise<{ client: Client; transport: StreamableHTTPClientTransport }> {
  const client = new Client({ name: 'engine-test', version: '1.0.0' }, { versionNegotiation: { mode } });
  const transport = new StreamableHTTPClientTransport(url, {
    requestInit: { headers: { ...(token === null ? {} : { authorization: `Bearer ${token}` }), ...headers } },
  });
  await client.connect(transport);
  clients.push(client);
  return { client, transport };
}

/** The problem document a refused call carries, after asserting it is a tool error. */
function problemOf(result: CallToolResult): Record<string, any> {
  assert.equal(result.isError, true, JSON.stringify(result));
  const structured = result.structuredContent as Record<string, any>;
  assert.deepEqual(JSON.parse((result.content[0] as { text: string }).text), structured);
  assert.equal(structured.type, 'about:blank');
  assert.equal(typeof structured.requestId, 'string');
  return structured;
}

describe('initialize', () => {
  test('answers the engine as the server, with tools, on the 2025 revision the client asks for', async () => {
    const { url } = await served();
    const { client, transport } = await connect(endpoint(url));
    assert.deepEqual(client.getServerVersion(), { name: '@superschematic/engine', version: '0.0.0' });
    assert.ok(client.getServerCapabilities()?.tools);
    assert.equal(transport.protocolVersion, '2025-11-25');
  });

  test('negotiates the 2026-07-28 revision with a client that probes for it', async () => {
    const { url } = await served();
    const { client, transport } = await connect(endpoint(url), 'alice', 'auto');
    assert.equal(transport.protocolVersion, '2026-07-28');
    assert.ok((await client.listTools()).tools.some((tool) => tool.name === 'item_increment'));
    const result = (await client.callTool({ name: 'item_increment', arguments: { id: 'i1' } })) as CallToolResult;
    assert.deepEqual(result.structuredContent, { count: 1 });
  });

  test('takes the serverInfo and instructions the deployment gives', async () => {
    const { url } = await served({}, { serverInfo: { name: 'shop', version: '2.0.0' }, instructions: 'Orders and items.' });
    const { client } = await connect(endpoint(url));
    assert.deepEqual(client.getServerVersion(), { name: 'shop', version: '2.0.0' });
    assert.equal(client.getInstructions(), 'Orders and items.');
  });

  test('needs a caller the Authenticator knows, and a namespace the engine has', async () => {
    const { url } = await served();
    await assert.rejects(connect(endpoint(url), null), /401|Unauthorized|unauthorized/);
    await assert.rejects(connect(endpoint(url), 'mallory'), /401|Unauthorized|unauthorized/);
    await assert.rejects(connect(endpoint(url, 'nowhere')), /404|unknown_namespace/);
    const response = await fetch(endpoint(url), { headers: { authorization: 'Bearer alice', accept: 'text/event-stream' } });
    assert.equal(response.status, 405, 'a stateless server opens no stream on GET');
    await response.body?.cancel();
  });
});

describe('tools/list', () => {
  test('lists the visible tools by handle, with their arguments, whether they only read, and their policy', async () => {
    const { url, engine } = await served();
    const { client } = await connect(endpoint(url));
    const { tools } = await client.listTools();
    assert.deepEqual(
      tools.map((tool) => tool.name),
      [
        'list_schemas',
        'describe_schema',
        'define_schema',
        'list_behaviors',
        'describe_behavior',
        'get_value',
        'item_create',
        'item_get',
        'item_list',
        'item_update',
        'item_delete',
        'item_increment',
        'item_history',
        'item_flag',
        'item_unflag',
        'item_explode',
      ]
    );
    assert.ok(!tools.some((tool) => /publish/.test(tool.name)), 'no tool publishes');
    const manifest = engine.tools.manifest(alice);
    for (const tool of tools) {
      const entry = manifest.tools.find((candidate) => !candidate.mcp.hidden && candidate.mcp.handle === tool.name);
      assert.ok(entry, tool.name);
      assert.deepEqual(tool.inputSchema, entry.parameters, `${tool.name} takes its tool's arguments`);
      assert.equal(tool.title, entry.title);
      assert.equal(tool.description, entry.description);
    }
    const byName = new Map(tools.map((tool) => [tool.name, tool]));
    assert.deepEqual(byName.get('item_history')?.annotations, { readOnlyHint: true });
    assert.deepEqual(byName.get('item_increment')?.annotations, { readOnlyHint: false });
    assert.deepEqual(byName.get('item_flag')?._meta, {
      'superschematic/operation-guidance': { useWhen: '', doNotUseWhen: '', success: '', errors: [] },
      invocationPolicy: 'ask',
    });
    assert.equal(byName.get('item_create')?._meta?.invocationPolicy, 'auto');
  });

  test("writes a distribution's policy key and vendor keys", async () => {
    const { url } = await served(
      {
        behaviors: [],
        tools: {
          invocationPolicy: { key: 'confirm', values: ['never', 'always'], default: 'never' },
          invocation: { delete: 'always' },
          keys: { scalar: 'x-acme-scalar', guidance: '', parameters: [{ key: 'x-acme-arguments', value: 1 }] },
        },
      },
      {},
      (engine) => {
        engine.schemas.define(everything, orderDocument(), { namespace: 'east' });
        engine.schemas.publish(everything, 'Order', { namespace: 'east' });
      }
    );
    const { client } = await connect(endpoint(url, 'east'));
    const tools = (await client.listTools()).tools;
    assert.deepEqual(
      tools.map((tool) => [tool.name, tool._meta]),
      [
        ['list_schemas', { confirm: 'never' }],
        ['describe_schema', { confirm: 'never' }],
        ['define_schema', { confirm: 'never' }],
        ['list_behaviors', { confirm: 'never' }],
        ['describe_behavior', { confirm: 'never' }],
        ['get_value', { confirm: 'never' }],
        ['order_create', { confirm: 'never' }],
        ['order_get', { confirm: 'never' }],
        ['order_list', { confirm: 'never' }],
        ['order_update', { confirm: 'never' }],
        ['order_delete', { confirm: 'always' }],
      ]
    );
    for (const tool of tools) {
      assert.equal((tool.inputSchema as Record<string, unknown>)['x-acme-arguments'], 1, tool.name);
    }
    const data = (tools[6].inputSchema.properties as Record<string, any>).data;
    assert.equal(data.properties.quantity['x-acme-scalar'], 'Generic.Int64');
    // The other namespace has no schema.
    const { client: other } = await connect(endpoint(url));
    assert.equal((await other.listTools()).tools.length, 6);
  });

  test('lists only what the access policy lets the caller call', async () => {
    const { url } = await served();
    const { client } = await connect(endpoint(url), 'reader');
    assert.deepEqual(
      (await client.listTools()).tools.map((tool) => tool.name),
      ['list_schemas', 'describe_schema', 'define_schema', 'list_behaviors', 'describe_behavior', 'get_value', 'item_get', 'item_list', 'item_history']
    );
  });
});

describe('schema-level operations', () => {
  test('are tools without an id: tools/list shows their params alone, and tools/call runs them on the schema', async () => {
    const { url, engine } = await served({ behaviors: [...testBehaviors, broken, ...reachBehaviors] }, {}, (engine) => {
      publishItem(engine, [{ name: 'test.Holder' }]);
      for (const id of ['i1', 'i2']) {
        engine.instances.create(everything, 'Item', { title: id }, { id });
      }
      engine.instances.invoke(everything, 'Item', 'i2', 'hold', { schema: 'Item', id: 'i1' });
    });
    const { client } = await connect(endpoint(url));
    const byName = new Map((await client.listTools()).tools.map((tool) => [tool.name, tool]));
    const holders = byName.get('item_holders');
    assert.ok(holders);
    assert.deepEqual([Object.keys(holders.inputSchema.properties ?? {}), holders.inputSchema.required, holders.annotations], [
      ['params'],
      ['params'],
      { readOnlyHint: true },
    ]);
    assert.deepEqual(Object.keys(byName.get('item_hold')?.inputSchema.properties ?? {}), ['expectedSeq', 'id', 'params']);

    const listed = (await client.callTool({ name: 'item_holders', arguments: { params: { schema: 'Item', id: 'i1' } } })) as CallToolResult;
    assert.deepEqual(listed.content, [{ type: 'text', text: '["i2"]' }]);
    const withId = (await client.callTool({ name: 'item_holders', arguments: { id: 'i2', params: { schema: 'Item', id: 'i1' } } })) as CallToolResult;
    assert.equal(problemOf(withId).code, 'invalid_argument');
    const released = (await client.callTool({ name: 'item_release_all', arguments: { params: { schema: 'Item', id: 'i1' } } })) as CallToolResult;
    assert.deepEqual(released.content, [{ type: 'text', text: '1' }]);
    assert.equal(engine.instances.get(alice, 'Item', 'i2')?.data.held, undefined);

    const { client: readOnly } = await connect(endpoint(url), 'reader');
    const names = (await readOnly.listTools()).tools.map((tool) => tool.name);
    assert.ok(names.includes('item_holders') && !names.includes('item_release_all'));
    const refused = (await readOnly.callTool({ name: 'item_release_all', arguments: { params: { schema: 'Item', id: 'i1' } } })) as CallToolResult;
    assert.equal(problemOf(refused).status, 403);
  });
});

describe('tools/call', () => {
  test('runs the operation and returns its result as text and, for an object, structured content', async () => {
    const { url, engine } = await served();
    const { client } = await connect(endpoint(url));
    const call = async (name: string, args: Record<string, unknown>) => (await client.callTool({ name, arguments: args })) as CallToolResult;

    const increment = await call('item_increment', { id: 'i1', params: { by: 2 }, expectedSeq: 1 });
    assert.deepEqual([increment.isError, increment.structuredContent], [undefined, { count: 2 }]);
    assert.deepEqual(JSON.parse((increment.content[0] as { text: string }).text), { count: 2 });
    const history = await call('item_history', { id: 'i1' });
    assert.deepEqual([history.structuredContent, history.content], [undefined, [{ type: 'text', text: '[2]' }]]);

    const created = await call('item_create', { id: 'i2', data: { title: 'Lamp' } });
    assert.deepEqual(created.structuredContent, engine.instances.get(alice, 'Item', 'i2'));
    const updated = await call('item_update', { id: 'i2', patch: { title: 'Table' } });
    assert.equal((updated.structuredContent as { data: { title: string } }).data.title, 'Table');
    assert.deepEqual((await call('item_delete', { id: 'i2' })).content, [{ type: 'text', text: 'null' }]);
    assert.equal(engine.instances.get(alice, 'Item', 'i2'), undefined);

    const draft = await call('define_schema', { document: { kind: 'General', name: 'Note', types: { Note: { name: 'Note', role: 'EmbeddedStruct', fields: [] } } } });
    assert.deepEqual([(draft.structuredContent as { version: unknown }).version, engine.schemas.live(alice, 'Note')], [null, undefined]);
    const summaries = (await call('list_schemas', {})).content[0] as { text: string };
    assert.deepEqual(JSON.parse(summaries.text), engine.schemas.list(alice));
    assert.deepEqual((await call('describe_schema', { name: 'Item' })).structuredContent, engine.tools.describe(alice, 'Item'));
  });

  test('a write the access policy refuses is a tool error with the 403 problem', async () => {
    const { url, engine } = await served();
    const { client } = await connect(endpoint(url), 'reader');
    for (const [name, args] of [
      ['item_create', { data: { title: 'Lamp' } }],
      ['item_increment', { id: 'i1' }],
      ['item_delete', { id: 'i1' }],
    ] as const) {
      const refused = problemOf((await client.callTool({ name, arguments: args })) as CallToolResult);
      assert.deepEqual([refused.status, refused.code, refused.title], [403, 'forbidden', 'Forbidden'], name);
    }
    assert.equal(engine.instances.get(alice, 'Item', 'i1')?.seq, 1, 'nothing was written');
    const history = (await client.callTool({ name: 'item_history', arguments: { id: 'i1' } })) as CallToolResult;
    assert.equal(history.isError, undefined);
  });

  test("every other refusal is a tool error carrying the HTTP API's problem", async () => {
    const { url } = await served();
    const { client } = await connect(endpoint(url));
    const refused = async (name: string, args: Record<string, unknown>) => problemOf((await client.callTool({ name, arguments: args })) as CallToolResult);

    const params = await refused('item_increment', { id: 'i1', params: { by: 0 } });
    assert.deepEqual([params.status, params.code, params.details.issues[0].path], [400, 'invalid_argument', '/by']);
    assert.deepEqual([(await refused('item_get', { id: 'i1', extra: 1 })).code], ['invalid_argument']);
    assert.deepEqual([(await refused('item_get', { id: 'nope' })).status], [404]);
    const veto = await refused('item_increment', { id: 'i1', params: { by: 6 } });
    assert.deepEqual([veto.status, veto.code, veto.details.behavior], [409, 'vetoed', 'test.Counter']);
    assert.deepEqual([(await refused('item_update', { id: 'i1', patch: {}, expectedSeq: 9 })).status], [412]);
    const invalid = await refused('item_create', { data: { title: 7 } });
    assert.deepEqual([invalid.status, invalid.code, invalid.details.issues[0].path], [422, 'invalid_instance', 'title']);
    // A defect in a behavior's code is a 500 whose detail keeps the failure off the wire.
    const defect = await refused('item_explode', { id: 'i1' });
    assert.deepEqual([defect.status, defect.code, defect.detail], [500, 'internal_error', 'An unexpected error occurred']);
    assert.ok(!JSON.stringify(defect).includes('secret'));
  });

  test("a schema's writes take preconditions when a behavior declares them, and a veto's code and details reach the tool error", async () => {
    const { url, engine } = await served({ behaviors: [...testBehaviors, hold] }, {}, (engine) => {
      publishItem(engine, [{ name: 'test.Counter' }, { name: 'test.Hold' }]);
      engine.instances.create(everything, 'Item', { title: 'Desk' }, { id: 'i1' });
    });
    const { client } = await connect(endpoint(url));
    const byName = new Map((await client.listTools()).tools.map((tool) => [tool.name, tool]));
    for (const name of ['item_update', 'item_delete', 'item_increment', 'item_advance']) {
      const properties = byName.get(name)?.inputSchema.properties as Record<string, any>;
      assert.deepEqual(properties.preconditions.properties, { 'test.Hold': holdDeclaration.preconditionSchema }, name);
      assert.equal(properties.preconditions.additionalProperties, false);
    }
    for (const name of ['item_create', 'item_get', 'item_list']) {
      assert.equal((byName.get(name)?.inputSchema.properties as Record<string, unknown>).preconditions, undefined, name);
    }
    const call = async (name: string, args: Record<string, unknown>) => (await client.callTool({ name, arguments: args })) as CallToolResult;
    const fenced = (generation: number) => ({ 'test.Hold': { generation } });

    assert.deepEqual((await call('item_advance', { id: 'i1', preconditions: fenced(0) })).structuredContent, { generation: 1 });
    const stale = problemOf(await call('item_update', { id: 'i1', patch: { title: 'Late' }, preconditions: fenced(0) }));
    assert.deepEqual([stale.status, stale.code, stale.details], [
      409,
      'vetoed',
      { behavior: 'test.Hold', action: 'update', reason: 'generation 0 is stale: the instance is at 1', code: 'stale', details: { generation: 0, current: 1 } },
    ]);
    const invalid = problemOf(await call('item_delete', { id: 'i1', preconditions: { 'test.Nope': {} } }));
    assert.deepEqual([invalid.status, invalid.code, invalid.details.issues], [400, 'invalid_argument', [{ path: '/test.Nope', message: 'Item composes no behavior test.Nope' }]]);
    assert.equal(problemOf(await call('item_increment', { id: 'i1', preconditions: 'test.Hold' })).code, 'invalid_argument');
    assert.equal((await call('item_increment', { id: 'i1', preconditions: fenced(1) })).isError, undefined);
    assert.equal(engine.instances.get(alice, 'Item', 'i1')?.data.count, 1);
  });

  test('list_behaviors and describe_behavior read the behaviors the engine runs, for any caller', async () => {
    const { url, engine } = await served({ policy: ({ principal }) => principal.subject === 'alice' });
    const { client } = await connect(endpoint(url), 'reader');
    const byName = new Map((await client.listTools()).tools.map((tool) => [tool.name, tool]));
    assert.deepEqual(
      [...byName.keys()],
      ['list_schemas', 'describe_schema', 'define_schema', 'list_behaviors', 'describe_behavior', 'get_value'],
      'reader may read no schema, and still lists the engine tools'
    );
    assert.deepEqual(byName.get('list_behaviors')?.annotations, { readOnlyHint: true });
    assert.deepEqual(byName.get('describe_behavior')?.inputSchema.required, ['name']);
    const call = async (name: string, args: Record<string, unknown>) => (await client.callTool({ name, arguments: args })) as CallToolResult;

    const listed = await call('list_behaviors', {});
    assert.deepEqual(JSON.parse((listed.content[0] as { text: string }).text), engine.tools.listBehaviors(everything));
    const described = await call('describe_behavior', { name: 'test.Counter' });
    assert.deepEqual(described.structuredContent, engine.tools.describeBehavior(everything, 'test.Counter'));
    assert.deepEqual((described.structuredContent as { configSchema: unknown }).configSchema, engine.behaviors.declaration('test.Counter')?.configSchema);
    const missing = problemOf(await call('describe_behavior', { name: 'test.Missing' }));
    assert.deepEqual([missing.status, missing.code], [404, 'not_found']);
    assert.equal(problemOf(await call('describe_behavior', {})).code, 'invalid_argument');
  });

  test('a schema with a unique field has a lookup tool, its list takes where, and a repeated value is a tool error with the 409 problem', async () => {
    const { url } = await served({}, {}, (engine) => {
      const document = schemaDocument('Model', [
        { name: 'slug', typeRef: { name: 'string' }, unique: true },
        { name: 'kind', typeRef: { name: 'string' } },
      ]) as { types: { Model: Record<string, unknown> } };
      document.types.Model.behaviors = [{ name: 'Workflow', config: { states: ['todo', 'done'], transitions: [{ from: 'todo', to: 'done' }] } }];
      engine.schemas.define(everything, document);
      engine.schemas.publish(everything, 'Model');
      engine.instances.create(everything, 'Model', { slug: 'org/a', kind: 'x' }, { id: 'a' });
      engine.instances.create(everything, 'Model', { slug: 'org/b', kind: 'y' }, { id: 'b' });
      engine.instances.invoke(everything, 'Model', 'b', 'transition', { to: 'done' });
    });
    const { client } = await connect(endpoint(url), 'reader');
    const { tools } = await client.listTools();
    const lookup = tools.find((tool) => tool.name === 'model_lookup');
    assert.deepEqual([lookup?.annotations?.readOnlyHint, lookup?.inputSchema.required], [true, ['key']]);
    assert.deepEqual((lookup?.inputSchema.properties as Record<string, any>).key.required, ['slug']);
    assert.match(String((lookup?._meta as Record<string, any>)['superschematic/operation-guidance'].useWhen), /Use when you know the slug of the Model to read/);
    const where = (tools.find((tool) => tool.name === 'model_list')?.inputSchema.properties as Record<string, any>).where;
    assert.deepEqual(Object.keys(where.properties), ['slug', 'kind', 'status']);
    const call = async (name: string, args: Record<string, unknown>) => (await client.callTool({ name, arguments: args })) as CallToolResult;
    assert.equal((((await call('model_lookup', { key: { slug: 'org/b' } })).structuredContent) as { id: string }).id, 'b');
    assert.equal(problemOf(await call('model_lookup', { key: { slug: 'org/c' } })).code, 'not_found');
    assert.equal(problemOf(await call('model_lookup', { key: 'org/b' })).code, 'invalid_argument');
    const listed = (await call('model_list', { where: { status: 'done' } })).structuredContent as { items: Array<{ id: string }> };
    assert.deepEqual(listed.items.map((item) => item.id), ['b']);
    const kinds = (await call('model_list', { where: { kind: ['y', 'x'] } })).structuredContent as { items: Array<{ id: string }> };
    assert.deepEqual(kinds.items.map((item) => item.id), ['a', 'b']);
    const { client: writer } = await connect(endpoint(url));
    const repeated = problemOf((await writer.callTool({ name: 'model_create', arguments: { data: { slug: 'org/a' } } })) as CallToolResult);
    assert.deepEqual([repeated.status, repeated.code, repeated.details], [409, 'conflict', { fields: ['slug'] }]);
  });

  test('an unknown tool, or one the caller may not read, is a JSON-RPC invalid-params error', async () => {
    const { url } = await served({ policy: ({ principal, action, schema }) => policy({ principal, action, namespace: 'default', schema }) && !(principal.subject === 'reader' && schema === 'Item') });
    const { client } = await connect(endpoint(url), 'reader');
    for (const name of ['publish_schema', 'item_get']) {
      await assert.rejects(client.callTool({ name, arguments: { id: 'i1' } }), (error: unknown) => {
        assert.ok(error instanceof ProtocolError, String(error));
        assert.equal(error.code, ProtocolErrorCode.InvalidParams);
        assert.match(error.message, new RegExp(`namespace default has no tool "${name}"`));
        return true;
      });
    }
  });
});

describe('service callers', () => {
  // A service credential is the calling deployable's name.
  const authenticateService: ServiceAuthenticator = async (ctx) => {
    const header = ctx.headers.get('service-authorization');
    return header === null ? null : { deployable: header.replace(/^Bearer /u, ''), serves: [], subject: 'sa' };
  };
  // The worker deployable may do anything standing in for an end user;
  // acting for one, it may do what that end user may.
  const withServices: AccessPolicy = (request) =>
    policy(request) || (request.principal.service?.standsIn === true && request.principal.service.deployable === 'worker');
  const service = (deployable: string) => ({ 'service-authorization': `Bearer ${deployable}` });

  test('a service with no end user stands in for one; acting for one, it acts as that end user', async () => {
    const { url, engine } = await served({ policy: withServices }, { authenticateService });
    const { client: worker } = await connect(endpoint(url), null, 'legacy', service('worker'));
    assert.ok((await worker.listTools()).tools.some((tool) => tool.name === 'item_increment'));
    const incremented = (await worker.callTool({ name: 'item_increment', arguments: { id: 'i1' } })) as CallToolResult;
    assert.deepEqual(incremented.structuredContent, { count: 1 });

    const { client: forReader } = await connect(endpoint(url), 'reader', 'legacy', service('worker'));
    assert.ok(!(await forReader.listTools()).tools.some((tool) => tool.name === 'item_increment'));
    const refused = problemOf((await forReader.callTool({ name: 'item_increment', arguments: { id: 'i1' } })) as CallToolResult);
    assert.deepEqual([refused.status, refused.code], [403, 'forbidden']);
    assert.match(refused.detail, /^reader may not/);

    const { client: crawler } = await connect(endpoint(url), null, 'legacy', service('crawler'));
    assert.ok(!(await crawler.listTools()).tools.some((tool) => tool.name.startsWith('item_')));
    assert.deepEqual(
      engine.events.read(alice, { schema: 'Item', instanceId: 'i1', kinds: ['operation'] }).events.map((event) => [event.actor, event.service]),
      [['service:worker', 'worker']]
    );
    // Without the service authenticator the header names nobody.
    const { url: plain } = await served({ policy: withServices });
    await assert.rejects(connect(endpoint(plain), null, 'legacy', service('worker')), /401|Unauthorized|unauthorized/);
  });
});

describe("the core's behaviors", () => {
  // An engine with the core meta-schema (the loader's default) and only its
  // own behaviors, and documents, which composes all three, with one
  // instance. The bearer token is the caller: alice may do everything but
  // holds no permission a behavior's config names, reader may only read,
  // and pat holds documents.publish, which the transition from review to
  // published needs.
  const permissions: Record<string, string[]> = { alice: ['*'], reader: ['read'], pat: ['read', 'write', 'documents.publish'] };
  const people: Authenticator = async (ctx) => {
    const token = ctx.bearerToken;
    return token !== undefined && Object.hasOwn(permissions, token) ? { subject: token, permissions: permissions[token] } : null;
  };
  const withDocument = () =>
    served({ metaSchema: undefined, behaviors: [] }, { authenticate: people }, (engine) => {
      engine.schemas.define(everything, documentsDocument());
      engine.schemas.publish(everything, 'documents');
      engine.instances.create(everything, 'documents', { title: 'Launch plan' }, { id: 'doc-1' });
    });
  const call = async (client: Client, name: string, args: Record<string, unknown>) => (await client.callTool({ name, arguments: args })) as CallToolResult;

  test('tools/list lists their operations with the default policy, the list operations read-only', async () => {
    const { url } = await withDocument();
    const { client } = await connect(endpoint(url));
    assert.deepEqual(
      (await client.listTools()).tools
        .filter((tool) => tool.name.startsWith('documents_'))
        .map((tool) => [tool.name, tool.annotations?.readOnlyHint, tool._meta?.invocationPolicy]),
      [
        ['documents_create', false, 'auto'],
        ['documents_get', true, 'auto'],
        ['documents_list', true, 'auto'],
        ['documents_update', false, 'auto'],
        ['documents_delete', false, 'auto'],
        ['documents_transition', false, 'auto'],
        ['documents_comment', false, 'auto'],
        ['documents_list_comments', true, 'auto'],
        ['documents_list_revisions', true, 'auto'],
        ['documents_propose', false, 'auto'],
        ['documents_approve', false, 'auto'],
        ['documents_reject', false, 'auto'],
        ['documents_list_proposals', true, 'auto'],
      ]
    );
    // A reader is listed only the tools that read.
    const { client: reader } = await connect(endpoint(url), 'reader');
    assert.deepEqual(
      (await reader.listTools()).tools.map((tool) => tool.name).filter((name) => name.startsWith('documents_')),
      ['documents_get', 'documents_list', 'documents_list_comments', 'documents_list_revisions', 'documents_list_proposals']
    );
  });

  test('tools/call runs them, listComments for a reader, and a transition whose permission the caller lacks is a tool error with the 403 problem', async () => {
    const { url, engine } = await withDocument();
    const { client } = await connect(endpoint(url));
    const commented = await call(client, 'documents_comment', { id: 'doc-1', params: { body: 'First pass is up.' }, expectedSeq: 1 });
    assert.deepEqual([commented.isError, (commented.structuredContent as { id: number }).id], [undefined, 1]);
    const { client: reader } = await connect(endpoint(url), 'reader');
    const page = await call(reader, 'documents_list_comments', { id: 'doc-1', params: { limit: 10 } });
    assert.deepEqual(
      [page.isError, (page.structuredContent as { items: Array<{ body: string }>; next: unknown }).items.map((item) => item.body), (page.structuredContent as { next: unknown }).next],
      [undefined, ['First pass is up.'], null]
    );
    assert.equal(problemOf(await call(reader, 'documents_comment', { id: 'doc-1', params: { body: 'Me too.' } })).status, 403);

    const moved = await call(client, 'documents_transition', { id: 'doc-1', params: { to: 'review' }, expectedSeq: 2 });
    assert.deepEqual(moved.structuredContent, { from: 'draft', to: 'review' });
    const refused = problemOf(await call(client, 'documents_transition', { id: 'doc-1', params: { to: 'published' } }));
    assert.deepEqual([refused.status, refused.code, refused.title], [403, 'forbidden', 'Forbidden']);
    assert.match(refused.detail, /the transition needs permission documents\.publish/);
    assert.equal(engine.instances.get(alice, 'documents', 'doc-1')?.seq, 3, 'nothing was written');
    const { client: pat } = await connect(endpoint(url), 'pat');
    const published = await call(pat, 'documents_transition', { id: 'doc-1', params: { to: 'published' }, expectedSeq: 3 });
    assert.deepEqual(published.structuredContent, { from: 'review', to: 'published' });
    assert.equal(engine.instances.get(alice, 'documents', 'doc-1')?.data.status, 'published');
  });

  test("Constants and Variants add no tool: create's data and update's patch show each variant, and a result in the wrong shape and a changed kind are tool errors with the 422 problem", async () => {
    const { url, engine } = await withDocument();
    engine.schemas.define(everything, stepsDocument());
    engine.schemas.publish(everything, 'Step');
    const { client } = await connect(endpoint(url));
    const tools = (await client.listTools()).tools.filter((tool) => tool.name.startsWith('step_'));
    assert.deepEqual(
      tools.map((tool) => tool.name),
      ['step_create', 'step_get', 'step_list', 'step_update', 'step_delete']
    );
    const described = engine.tools.describe(alice, 'Step');
    const data = (tools[0].inputSchema.properties as Record<string, Record<string, unknown>>).data;
    assert.deepEqual(data.allOf, described.instance.allOf);
    const patch = (tools[3].inputSchema.properties as Record<string, Record<string, unknown>>).patch;
    assert.equal((patch.allOf as unknown[]).length, 3);

    const wrong = problemOf(await call(client, 'step_create', { data: { title: 'Look', kind: 'review', result: { passed: true } } }));
    assert.deepEqual(
      [wrong.status, wrong.code, wrong.details.issues.map((issue: { path: string; rule: string }) => [issue.path, issue.rule])],
      [
        422,
        'invalid_instance',
        [
          ['result.passed', 'unknown'],
          ['result.approved', 'required'],
        ],
      ]
    );
    const created = await call(client, 'step_create', { id: 's1', data: { title: 'Look', kind: 'review', result: { approved: true } } });
    assert.equal(created.isError, undefined);
    const renamed = problemOf(await call(client, 'step_update', { id: 's1', patch: { kind: 'verify', result: null } }));
    assert.deepEqual(
      [renamed.status, renamed.code, renamed.details.issues],
      [422, 'invalid_instance', [{ path: 'kind', rule: 'constant', message: 'kind is a constant of Step: its create sets it and nothing changes it after' }]]
    );
  });

  test('Search is a tool with no id that only reads: a reader calls it, and a query FTS5 cannot parse is a tool error with the 400 problem', async () => {
    const { url, engine } = await withDocument();
    engine.schemas.define(everything, notesDocument());
    engine.schemas.publish(everything, 'notes');
    engine.instances.create(everything, 'notes', { title: 'Walnut desk', body: 'Solid wood.' }, { id: 'n1' });
    engine.instances.create(everything, 'notes', { title: 'Oak chair', body: 'Goes with the walnut desk.' }, { id: 'n2' });
    const { client } = await connect(endpoint(url));
    const search = (await client.listTools()).tools.find((tool) => tool.name === 'notes_search');
    assert.deepEqual(
      [search?.annotations?.readOnlyHint, Object.keys(search?.inputSchema.properties ?? {}), search?.inputSchema.required],
      [true, ['params'], ['params']]
    );
    const { client: reader } = await connect(endpoint(url), 'reader');
    const found = await call(reader, 'notes_search', { params: { query: 'walnut' } });
    assert.deepEqual(
      (found.structuredContent as { items: Array<{ id: string; rank: number; field: string }> }).items.map((hit) => [hit.id, hit.rank, hit.field]),
      [
        ['n1', 1, 'title'],
        ['n2', 2, 'body'],
      ]
    );
    const refused = problemOf(await call(reader, 'notes_search', { params: { query: 'title: walnut', syntax: 'fts5' } }));
    assert.deepEqual([refused.status, refused.code, refused.details.issues[0].path], [400, 'invalid_argument', '/query']);
  });

  test('Dependencies and Links are tools: create takes their parameters, listLinked takes no id; a gated transition and a required target\'s delete are tool errors with the 409 problem', async () => {
    const { url, engine } = await withDocument();
    for (const document of [tasksDocument(), projectsDocument()]) {
      engine.schemas.define(everything, document);
      engine.schemas.publish(everything, document.name as string);
    }
    engine.instances.create(everything, 'projects', { title: 'Launch' }, { id: 'launch' });
    engine.instances.create(everything, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
    const { client } = await connect(endpoint(url));
    const tools = (await client.listTools()).tools.filter((tool) => tool.name.startsWith('tasks_'));
    assert.deepEqual(
      tools.map((tool) => [tool.name, tool.annotations?.readOnlyHint, Object.keys(tool.inputSchema.properties ?? {}).join(' ')]),
      [
        ['tasks_create', false, 'behaviors data id'],
        ['tasks_get', true, 'id valueRefs'],
        ['tasks_list', true, 'cursor limit valueRefs where'],
        ['tasks_update', false, 'expectedSeq id patch'],
        ['tasks_delete', false, 'expectedSeq id'],
        ['tasks_transition', false, 'expectedSeq id params'],
        ['tasks_add_blocker', false, 'expectedSeq id params'],
        ['tasks_remove_blocker', false, 'expectedSeq id params'],
        ['tasks_list_blockers', true, 'expectedSeq id params'],
        ['tasks_list_dependents', true, 'expectedSeq id params'],
        ['tasks_link', false, 'expectedSeq id params'],
        ['tasks_unlink', false, 'expectedSeq id params'],
        ['tasks_list_linked', true, 'params'],
      ]
    );
    const behaviors = (tools[0].inputSchema.properties as Record<string, { properties: Record<string, unknown> }>).behaviors;
    assert.deepEqual(Object.keys(behaviors.properties), ['Dependencies', 'Links']);
    // The create parameters the config takes: a property per link, the required one required.
    const links = behaviors.properties.Links as { properties: Record<string, { properties: Record<string, unknown> }>; required: string[]; additionalProperties: boolean };
    assert.deepEqual([Object.keys(links.properties), links.required, links.additionalProperties], [['parent', 'project', 'spec'], ['project'], false]);
    assert.deepEqual(Object.keys(links.properties.spec.properties), ['id', 'revision'], 'a pinned link takes a revision');
    assert.deepEqual(Object.keys(links.properties.project.properties), ['id']);
    // Each tool's _meta carries its guidance under the guidance key, from the config.
    const guidance = (name: string) =>
      (tools.find((tool) => tool.name === name)?._meta as Record<string, { useWhen: string; doNotUseWhen: string; errors: Array<{ code: string }> }>)[
        'superschematic/operation-guidance'
      ];
    assert.match(guidance('tasks_transition').useWhen, /from todo to doing or dropped; from doing to done or dropped/);
    assert.deepEqual(
      guidance('tasks_transition').errors.map((error) => error.code),
      ['transition_not_allowed', 'already_in_state', 'terminal_state', 'no_status', 'blocked']
    );
    assert.match(guidance('tasks_create').useWhen, /project is required/);
    assert.deepEqual(guidance('tasks_unlink').errors.map((error) => error.code), ['required_link']);
    // A create gives the required project, and build's parent and blocker, in one event.
    const missing = problemOf(await call(client, 'tasks_create', { id: 'build', data: { title: 'Build' } }));
    assert.deepEqual([missing.status, missing.code, missing.details.issues], [400, 'invalid_argument', [{ path: '/behaviors/Links', message: 'link project is required, so a create of tasks gives it' }]]);
    const created = await call(client, 'tasks_create', {
      id: 'build',
      data: { title: 'Build' },
      behaviors: { Links: { project: 'launch', parent: 'plan' }, Dependencies: { blockers: [{ id: 'plan' }] } },
    });
    assert.deepEqual((created.structuredContent as { seq: number; data: unknown }).data, {
      title: 'Build',
      status: 'todo',
      blocked: true,
      links: { parent: { schema: 'tasks', id: 'plan' }, project: { schema: 'projects', id: 'launch' } },
    });
    assert.deepEqual((await call(client, 'tasks_add_blocker', { id: 'build', params: { schema: 'documents', id: 'doc-1' } })).structuredContent, {
      schema: 'documents',
      id: 'doc-1',
      status: 'draft',
      open: true,
    });
    await call(client, 'tasks_link', { id: 'build', params: { name: 'spec', id: 'doc-1' } });
    await call(client, 'tasks_transition', { id: 'build', params: { to: 'doing' } });
    const gated = problemOf(await call(client, 'tasks_transition', { id: 'build', params: { to: 'done' } }));
    assert.deepEqual([gated.status, gated.code, gated.details.behavior], [409, 'vetoed', 'Dependencies']);
    const { client: reader } = await connect(endpoint(url), 'reader');
    const linked = await call(reader, 'tasks_list_linked', { params: { name: 'spec', id: 'doc-1' } });
    assert.deepEqual(linked.structuredContent, { items: [{ id: 'build', revision: 1, stale: false }], next: null });
    const required = problemOf(await call(client, 'projects_delete', { id: 'launch' }));
    assert.deepEqual([required.status, required.code, required.details.behavior], [409, 'vetoed', 'Links']);
    assert.ok(engine.instances.get(alice, 'projects', 'launch'));
  });

  test('Rollups adds a field and no tool: get carries the rollups, computed at the read, and a gated transition is a tool error with the 409 problem', async () => {
    const { url, engine } = await withDocument();
    for (const document of [tasksDocument(), projectsDocument()]) {
      engine.schemas.define(everything, document);
      engine.schemas.publish(everything, document.name as string);
    }
    engine.instances.create(everything, 'projects', { title: 'Launch' }, { id: 'launch' });
    engine.instances.create(everything, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: { Links: { project: 'launch' } } });
    const { client } = await connect(endpoint(url));
    assert.deepEqual(
      (await client.listTools()).tools.filter((tool) => tool.name.startsWith('projects_')).map((tool) => tool.name),
      ['projects_create', 'projects_get', 'projects_list', 'projects_update', 'projects_delete', 'projects_transition']
    );
    const { client: reader } = await connect(endpoint(url), 'reader');
    const read = await call(reader, 'projects_get', { id: 'launch' });
    assert.deepEqual((read.structuredContent as { data: Record<string, unknown> }).data.rollups, { tasks: 1, tasksByStatus: { todo: 1 }, tasksFinished: false });
    const gated = problemOf(await call(client, 'projects_transition', { id: 'launch', params: { to: 'done' } }));
    assert.deepEqual(
      [gated.status, gated.code, gated.details.behavior, gated.details.code, gated.details.details],
      [409, 'vetoed', 'Rollups', 'not_held', { rollup: 'tasksFinished', to: 'done', over: false, linked: 1, counted: 0 }]
    );
    await call(client, 'tasks_transition', { id: 'plan', params: { to: 'dropped' } });
    const again = await call(reader, 'projects_get', { id: 'launch' });
    assert.deepEqual((again.structuredContent as { data: Record<string, unknown> }).data.rollups, { tasks: 1, tasksByStatus: { dropped: 1 }, tasksFinished: true });
    assert.deepEqual((await call(client, 'projects_transition', { id: 'launch', params: { to: 'done' } })).structuredContent, { from: 'active', to: 'done' });
  });
});
