import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountManualOperation, mountOperation, type RouterRuntimeOptions } from './hono';
import type { Authenticator, OperationSpec } from './index';
import { MCP_MAX_BODY_BYTES, McpUnknownToolError, mountMcp, serveMcp, type McpRouteTool, type McpToolSet } from './mcp';

/*
The MCP endpoint of a generated router (D81): the transport's refusals,
which the engine's corpus (mcp-parity.test.ts) does not record, and the
router's endpoint, which authenticates as the router's routes do, lists
the tools whose routes its caller may call, and runs a call through the
route of its operation in process, so the route's checks apply.
*/

const auth = (permissions: string[] = []) => ({ public: false, required: true, permissions });

const specs = {
  getItem: {
    name: 'getItem',
    namespace: 'item',
    method: 'GET',
    path: '/api/items/{id}',
    pathParams: [{ name: 'id', kind: 'string', required: true }],
    queryParams: [{ name: 'tag', kind: 'string', required: false, isArray: true }],
    bodyParams: [],
    auth: auth(),
    manual: false,
  },
  renameItem: {
    name: 'renameItem',
    namespace: 'item',
    method: 'POST',
    path: '/api/items/{id}/rename',
    pathParams: [{ name: 'id', kind: 'string', required: true }],
    queryParams: [],
    bodyParams: [{ name: 'new_name', kind: 'string', required: true, minLength: 1 }],
    auth: auth(),
    rateLimitPerMinute: 2,
    manual: false,
  },
  report: {
    name: 'report',
    namespace: 'item',
    method: 'GET',
    path: '/api/report',
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth: auth(['staff']),
    manual: false,
  },
  exportItems: {
    name: 'exportItems',
    namespace: 'item',
    method: 'GET',
    path: '/api/export',
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth: auth(),
    manual: true,
  },
  ping: {
    name: 'ping',
    namespace: 'item',
    method: 'GET',
    path: '/api/ping',
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth: { public: true, required: false, permissions: [] },
    manual: false,
  },
} as const satisfies Record<string, OperationSpec>;

const tool = (name: string, operation: keyof typeof specs, args: McpRouteTool['arguments'] = [], readOnly = true): McpRouteTool => ({
  name,
  title: name,
  description: `The ${name} tool.`,
  inputSchema: { type: 'object', additionalProperties: false, properties: Object.fromEntries(args.map(arg => [arg.key, {}])) },
  annotations: { readOnlyHint: readOnly },
  _meta: { invocationPolicy: readOnly ? 'auto' : 'ask' },
  operation,
  arguments: args,
});

const tools: McpRouteTool[] = [
  tool('get_item', 'getItem', [
    { key: 'id', in: 'path', name: 'id' },
    { key: 'tag', in: 'query', name: 'tag' },
  ]),
  tool(
    'rename_item',
    'renameItem',
    [
      { key: 'id', in: 'path', name: 'id' },
      { key: 'newName', in: 'body', name: 'new_name' },
    ],
    false
  ),
  tool('report', 'report'),
  tool('export_items', 'exportItems'),
  tool('ping', 'ping'),
];

const authenticate: Authenticator = async ctx => {
  if (ctx.bearerToken === 'ada') return { subject: 'ada', permissions: [] };
  if (ctx.bearerToken === 'root') return { subject: 'root', permissions: ['staff'] };
  return null;
};

function router(options: RouterRuntimeOptions = { authenticate }): Hono {
  const app = new Hono();
  mountOperation(
    app,
    specs.getItem,
    async (ctx, request) => ({ id: request.path.id, tags: request.query.tag ?? [], requestId: ctx.requestId, by: ctx.principal?.subject }),
    options
  );
  mountOperation(app, specs.renameItem, async (_ctx, request) => ({ id: request.path.id, name: request.body.new_name }), options);
  mountOperation(app, specs.report, async () => ({ rows: 3 }), options);
  mountManualOperation(app, specs.exportItems, async () => new Response('id,name\n1,desk\n', { headers: { 'content-type': 'text/csv' } }), options);
  mountOperation(app, specs.ping, async () => 'pong', options);
  mountMcp(app, specs, tools, options, { api: 'items-api', version: '1.0.0', requiresAuth: true });
  return app;
}

const JSONRPC = { accept: 'application/json, text/event-stream', 'content-type': 'application/json' };

let ids = 0;
function rpc(method: string, params?: unknown): Record<string, unknown> {
  return { jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) };
}

async function post(app: Hono, body: unknown, headers: Record<string, string> = {}): Promise<Response> {
  return app.request('/api/mcp', { method: 'POST', headers: { ...JSONRPC, ...headers }, body: typeof body === 'string' ? body : JSON.stringify(body) });
}

/** The JSON-RPC messages of an event-stream answer. */
async function messages(response: Response): Promise<Array<Record<string, any>>> {
  expect(response.headers.get('content-type')).toBe('text/event-stream');
  const text = await response.text();
  return text
    .split('\n\n')
    .filter(frame => frame.trim() !== '')
    .map(frame => {
      expect(frame.startsWith('event: message\ndata: ')).toBe(true);
      return JSON.parse(frame.slice('event: message\ndata: '.length));
    });
}

async function call(app: Hono, token: string, name: string, args?: unknown, headers: Record<string, string> = {}): Promise<Record<string, any>> {
  const [reply] = await messages(await post(app, rpc('tools/call', { name, ...(args !== undefined ? { arguments: args } : {}) }), { authorization: `Bearer ${token}`, ...headers }));
  return reply;
}

describe('the router endpoint', () => {
  test('authenticates as the routes do, and GET and DELETE answer 405 behind the gate', async () => {
    const app = router();
    const refused = await post(app, rpc('initialize', { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 't', version: '1' } }));
    expect(refused.status).toBe(401);
    expect(refused.headers.get('content-type')).toBe('application/problem+json');
    expect((await refused.json()).code).toBe('unauthorized');
    expect((await app.request('/api/mcp', { headers: { authorization: 'Bearer nobody' } })).status).toBe(401);
    for (const method of ['GET', 'DELETE']) {
      const answer = await app.request('/api/mcp', { method, headers: { authorization: 'Bearer ada' } });
      expect(answer.status).toBe(405);
      expect(await answer.json()).toEqual({ jsonrpc: '2.0', error: { code: -32000, message: 'Method not allowed.' }, id: null });
    }
  });

  test('initialize names the API and its version, unless the options name a server', async () => {
    const init = rpc('initialize', { protocolVersion: '2099-01-01', capabilities: {}, clientInfo: { name: 't', version: '1' } });
    const [reply] = await messages(await post(router(), init, { authorization: 'Bearer ada' }));
    expect(reply.result).toEqual({ protocolVersion: '2025-11-25', capabilities: { tools: { listChanged: false } }, serverInfo: { name: 'items-api', version: '1.0.0' } });

    const app = new Hono();
    mountMcp(app, specs, [], { authenticate }, { api: 'items-api', version: '1.0.0', requiresAuth: true }, { serverInfo: { name: 'items', version: '2.0.0' }, instructions: 'Items.' });
    const [named] = await messages(await post(app, init, { authorization: 'Bearer ada' }));
    expect(named.result.serverInfo).toEqual({ name: 'items', version: '2.0.0' });
    expect(named.result.instructions).toBe('Items.');
  });

  test('tools/list holds the tools whose routes the caller may call', async () => {
    const app = router();
    const list = async (token: string) => (await messages(await post(app, rpc('tools/list'), { authorization: `Bearer ${token}` })))[0].result.tools;
    const ada = await list('ada');
    expect(ada.map((t: { name: string }) => t.name)).toEqual(['get_item', 'rename_item', 'export_items', 'ping']);
    expect(ada[1]).toEqual({
      name: 'rename_item',
      title: 'rename_item',
      description: 'The rename_item tool.',
      inputSchema: { type: 'object', additionalProperties: false, properties: { id: {}, newName: {} } },
      annotations: { readOnlyHint: false },
      _meta: { invocationPolicy: 'ask' },
    });
    expect((await list('root')).map((t: { name: string }) => t.name)).toContain('report');
  });

  test('tools/call runs the route with the arguments where it reads them, as the caller, under the request id', async () => {
    const reply = await call(router(), 'ada', 'get_item', { id: 'a b/c', tag: ['x', 'y'] }, { 'x-request-id': 'req-7' });
    const data = { id: 'a b/c', tags: ['x', 'y'], requestId: 'req-7', by: 'ada' };
    expect(reply.result).toEqual({ content: [{ type: 'text', text: JSON.stringify(data) }], structuredContent: data });

    const renamed = await call(router(), 'ada', 'rename_item', { id: 'i1', newName: 'Desk' });
    expect(renamed.result.structuredContent).toEqual({ id: 'i1', name: 'Desk' });
  });

  test("a refusal of the route is a tool error carrying the route's problem", async () => {
    const app = router();
    const invalid = await call(app, 'ada', 'rename_item', { id: 'i1', newName: '' }, { 'x-request-id': 'req-8' });
    expect(invalid.result.isError).toBe(true);
    expect(invalid.result.structuredContent).toMatchObject({ status: 400, code: 'bad_request', requestId: 'req-8' });
    expect(JSON.parse(invalid.result.content[0].text)).toEqual(invalid.result.structuredContent);
    // The route's @rateLimit (two a minute) counts the call: the third is refused.
    await call(app, 'ada', 'rename_item', { id: 'i1', newName: 'Desk' });
    const limited = await call(app, 'ada', 'rename_item', { id: 'i1', newName: 'Desk' });
    expect(limited.result.structuredContent).toMatchObject({ status: 429, code: 'too_many_requests' });
  });

  test('arguments the tool does not take, and a missing path argument, are tool errors', async () => {
    const app = router();
    const extra = await call(app, 'ada', 'get_item', { id: 'i1', extra: 1 });
    expect(extra.result.structuredContent).toMatchObject({ status: 400, detail: 'get_item takes id, tag, not "extra"' });
    const none = await call(app, 'ada', 'ping', { x: 1 });
    expect(none.result.structuredContent.detail).toBe('ping takes no arguments, not "x"');
    const missing = await call(app, 'ada', 'get_item', {});
    expect(missing.result.structuredContent.detail).toBe('get_item: id is required');
  });

  test("a tool the caller is not offered, or the API has not got, is the JSON-RPC invalid-params error", async () => {
    const app = router();
    for (const name of ['report', 'nowhere']) {
      const reply = await call(app, 'ada', name);
      expect(reply.error).toEqual({ code: -32602, message: `items-api has no tool "${name}"` });
    }
    expect((await call(app, 'root', 'report')).result.structuredContent).toEqual({ rows: 3 });
  });

  test('a result that is no object is text alone, and a body that is no JSON its text', async () => {
    const app = router();
    expect((await call(app, 'ada', 'ping')).result).toEqual({ content: [{ type: 'text', text: '"pong"' }] });
    expect((await call(app, 'ada', 'export_items')).result).toEqual({ content: [{ type: 'text', text: 'id,name\n1,desk\n' }] });
  });

  test('refuses a tool that names an operation the router has not got', () => {
    expect(() => mountMcp(new Hono(), specs, [tool('lost', 'nowhere' as never)], {}, { api: 'items-api', version: '1.0.0', requiresAuth: false })).toThrow(
      'mountMcp: tool lost names operation nowhere, which the router does not have'
    );
  });

  test("a call carries the endpoint request's headers, so an authenticator of its own header authenticates it too", async () => {
    const byKey: Authenticator = async ctx => (ctx.headers.get('x-api-key') === 'k1' ? { subject: 'service-key', permissions: [] } : null);
    const app = router({ authenticate: byKey });
    const [reply] = await messages(await post(app, rpc('tools/call', { name: 'get_item', arguments: { id: 'i1' } }), { 'x-api-key': 'k1' }));
    expect(reply.result.structuredContent.by).toBe('service-key');
    expect((await post(app, rpc('tools/list'))).status).toBe(401);
  });

  test('an API whose routes need no caller serves its endpoint to anyone', async () => {
    const app = new Hono();
    mountOperation(app, specs.ping, async () => 'pong');
    mountMcp(app, specs, [tool('ping', 'ping')], {}, { api: 'items-api', version: '1.0.0', requiresAuth: false });
    const [reply] = await messages(await post(app, rpc('tools/call', { name: 'ping' })));
    expect(reply.result.content[0].text).toBe('"pong"');
  });
});

describe('the transport', () => {
  const none: McpToolSet = {
    list: () => [],
    call: async name => {
      throw new McpUnknownToolError(`no tool ${name}`);
    },
  };
  const server = { serverInfo: { name: 'items-api', version: '1.0.0' } };
  const send = (body: string, headers: Record<string, string> = JSONRPC) =>
    serveMcp(new Request('http://api.test/api/mcp', { method: 'POST', headers, body }), none, server);
  const refusal = async (response: Response) => ({ status: response.status, body: await response.json() });

  test('refuses what is not a JSON-RPC exchange over JSON, as the SDK does', async () => {
    expect(await refusal(await send('{}', { accept: JSONRPC.accept, 'content-type': 'text/plain' }))).toEqual({
      status: 415,
      body: { jsonrpc: '2.0', error: { code: -32000, message: 'Unsupported Media Type: Content-Type must be application/json' }, id: null },
    });
    expect(await refusal(await send(JSON.stringify(rpc('ping')), { accept: 'application/json', 'content-type': 'application/json' }))).toEqual({
      status: 406,
      body: { jsonrpc: '2.0', error: { code: -32000, message: 'Not Acceptable: Client must accept both application/json and text/event-stream' }, id: null },
    });
    expect(await refusal(await send('{nope'))).toEqual({ status: 400, body: { jsonrpc: '2.0', error: { code: -32700, message: 'Parse error: Invalid JSON' }, id: null } });
    expect(await refusal(await send('[]'))).toEqual({ status: 400, body: { jsonrpc: '2.0', error: { code: -32600, message: 'Bad Request: empty JSON-RPC batch' }, id: null } });
    expect(await refusal(await send('{"id":1}'))).toEqual({
      status: 400,
      body: { jsonrpc: '2.0', error: { code: -32600, message: 'Bad Request: the request body is not a valid JSON-RPC message' }, id: null },
    });
    const init = rpc('initialize', { protocolVersion: '2025-11-25' });
    expect(await refusal(await send(JSON.stringify([init, rpc('ping')])))).toEqual({
      status: 400,
      body: { jsonrpc: '2.0', error: { code: -32600, message: 'Invalid Request: Only one initialization request is allowed' }, id: null },
    });
    expect((await refusal(await send(JSON.stringify(rpc('ping')), { ...JSONRPC, 'mcp-protocol-version': '2024-01-01' }))).body.error.message).toBe(
      'Bad Request: Unsupported protocol version: 2024-01-01 (supported versions: 2025-11-25, 2025-06-18, 2025-03-26, 2024-11-05, 2024-10-07)'
    );
    const big = await serveMcp(
      new Request('http://api.test/api/mcp', { method: 'POST', headers: { ...JSONRPC, 'content-length': String(MCP_MAX_BODY_BYTES + 1) }, body: '{}' }),
      none,
      server
    );
    expect(big.status).toBe(413);
  });

  test('answers a batch with one event per request, a notification with 202, and an unknown method with its error', async () => {
    const batch = await send(JSON.stringify([rpc('ping'), { jsonrpc: '2.0', method: 'notifications/initialized' }, rpc('resources/list')]));
    const [ping, unknown] = await messages(batch);
    expect(ping).toEqual({ result: {}, jsonrpc: '2.0', id: ids - 1 });
    expect(unknown).toEqual({ jsonrpc: '2.0', id: ids, error: { code: -32601, message: 'Method not found' } });
    const notified = await send(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }));
    expect(notified.status).toBe(202);
    expect(await notified.text()).toBe('');
    const [invalid] = await messages(await send(JSON.stringify(rpc('tools/call', { arguments: {} }))));
    expect(invalid.error).toEqual({ code: -32602, message: 'Invalid params for tools/call: name must be a string' });
  });

  test('a 2026-07-28 request is one JSON body, and its envelope and headers must agree', async () => {
    const meta = {
      'io.modelcontextprotocol/protocolVersion': '2026-07-28',
      'io.modelcontextprotocol/clientInfo': { name: 't', version: '1' },
      'io.modelcontextprotocol/clientCapabilities': {},
    };
    const modern = { ...JSONRPC, 'mcp-protocol-version': '2026-07-28' };
    const listed = await send(JSON.stringify(rpc('tools/list', { _meta: meta })), modern);
    expect(listed.headers.get('content-type')).toBe('application/json');
    expect((await listed.json()).result).toEqual({
      tools: [],
      resultType: 'complete',
      ttlMs: 0,
      cacheScope: 'private',
      _meta: { 'io.modelcontextprotocol/serverInfo': { name: 'items-api', version: '1.0.0' } },
    });
    const absent = await refusal(await send(JSON.stringify(rpc('tools/list', { _meta: meta }))));
    expect(absent.status).toBe(400);
    expect(absent.body.error.code).toBe(-32020);
    const missing = await refusal(await send(JSON.stringify(rpc('tools/list')), modern));
    expect(missing.body.error).toEqual({
      code: -32602,
      message:
        'Invalid params: the MCP-Protocol-Version header names protocol revision 2026-07-28, but the request is missing the required per-request envelope key(s): _meta',
      data: { envelope: { missing: ['_meta'] } },
    });
    const wrongMethod = await refusal(await send(JSON.stringify(rpc('tools/list', { _meta: meta })), { ...modern, 'mcp-method': 'tools/call' }));
    expect(wrongMethod.body.error.code).toBe(-32020);
    const unknown = await refusal(await send(JSON.stringify(rpc('initialize', { _meta: meta })), modern));
    expect(unknown).toMatchObject({ status: 404, body: { error: { code: -32601 } } });
  });
});
