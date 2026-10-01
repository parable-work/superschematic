// The MCP endpoint over a listening server, with the official MCP client
// (@modelcontextprotocol/client): initialize, tools/list and tools/call
// through the HTTP runtime's gate and the engine's access policy, a
// refused call as a tool error carrying the problem document, an unknown
// tool as a JSON-RPC error, and the 2026-07-28 revision beside the 2025
// ones. The HTTP API and the MCP endpoint are mounted on one app. On
// Node.js the server is @hono/node-server, on Bun it is Bun.serve.
import assert from 'node:assert/strict';
import type { Server as NodeServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, test } from 'node:test';

import { serve as serveNode } from '@hono/node-server';
import { Client, ProtocolError, ProtocolErrorCode, StreamableHTTPClientTransport, type CallToolResult } from '@modelcontextprotocol/client';
import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { defineBehavior, type AccessPolicy, type Engine, type EngineOptions } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { MCP_PATH, engineMcp, type EngineMcpOptions } from '../dist/mcp/index.js';
import { openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, openTestEngine, orderDocument } from './helpers.ts';

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

async function connect(url: URL, token: string | null = 'alice', mode: 'legacy' | 'auto' = 'legacy'): Promise<{ client: Client; transport: StreamableHTTPClientTransport }> {
  const client = new Client({ name: 'engine-test', version: '1.0.0' }, { versionNegotiation: { mode } });
  const transport = new StreamableHTTPClientTransport(url, token === null ? {} : { requestInit: { headers: { authorization: `Bearer ${token}` } } });
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
    const data = (tools[3].inputSchema.properties as Record<string, any>).data;
    assert.equal(data.properties.quantity['x-acme-scalar'], 'Generic.Int64');
    // The other namespace has no schema.
    const { client: other } = await connect(endpoint(url));
    assert.equal((await other.listTools()).tools.length, 3);
  });

  test('lists only what the access policy lets the caller call', async () => {
    const { url } = await served();
    const { client } = await connect(endpoint(url), 'reader');
    assert.deepEqual(
      (await client.listTools()).tools.map((tool) => tool.name),
      ['list_schemas', 'describe_schema', 'define_schema', 'item_get', 'item_list', 'item_history']
    );
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
