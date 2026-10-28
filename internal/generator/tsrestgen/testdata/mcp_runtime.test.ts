/*
Runtime check of a generated router's OpenAPI routes and MCP endpoint (D81),
run by TestGeneratedMCPRuntime with API_DIR set to the materialized
fixture-mcp package, built with outputs.api.mcp on. Stub implementations
echo their decoded arguments, so each tools/call shows where the router's
route read each argument; the route's checks refuse what breaks its rules,
and refuse a call of a tool that asks before anyone is asked (D98).
fixture-mcp's routes need no caller, so its endpoint needs none either.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { HttpProblem, type RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

const ORDER = '00000000-0000-4000-8000-000000000001';
// The id as the route decodes a path parameter, the scalar library's
// canonical form; an input's fields keep the form the caller sent.
const ORDER_ID = parseIdentityUUID(ORDER);

// The key the endpoint signs the confirmations of update_order, whose
// policy is ask, with (D93); the router needs one.
const CONFIRMATION_KEY = 'a random key of at least thirty-two bytes';

// The operations the stubs ran, in order.
const ran: string[] = [];

function app(options: Record<string, unknown> = {}) {
  options = { ...options, mcp: { confirmationKey: CONFIRMATION_KEY, ...(options.mcp as object | undefined) } };
  const echo = (name: string) => async (args: Record<string, unknown>) => {
    ran.push(name);
    return { id: ORDER, status: 'open', revision: 1, totalCents: 0, echo: { name, args } };
  };
  const root = new Hono();
  root.route(
    '/',
    buildRouter(
      {
        order: {
          getOrder: echo('getOrder'),
          listOrders: async (args: Record<string, unknown>) => [{ id: ORDER, status: 'open', revision: 1, totalCents: 0, echo: args }],
          exportOrders: async () => 'id\n',
          openReturn: echo('openReturn'),
          updateOrder: echo('updateOrder'),
          deleteOrder: echo('deleteOrder'),
        },
      },
      options
    )
  );
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

describe('the OpenAPI routes', () => {
  test('serve the document with the running version and server, and its page', async () => {
    const document = await app().request('/api/openapi.json');
    expect(document.status).toBe(200);
    expect(document.headers.get('content-type')).toBe('application/json');
    const body = await document.json();
    expect(body.info.version).toBe('1.0.0');
    expect(body.servers).toEqual([{ url: 'http://localhost:8080', description: 'Runtime server' }]);
    expect(Object.keys(body.paths)).toContain('/api/orders/{id}');
    const page = await app().request('/api/docs');
    expect(page.headers.get('content-type')).toBe('text/html; charset=utf-8');
    expect(await page.text()).toContain('<title>fixture-mcp API Documentation</title>');

    const stated = await (await app({ openAPIVersion: '2.4.0', openAPIBaseURL: 'https://orders.example' }).request('/api/openapi.json')).json();
    expect(stated.info.version).toBe('2.4.0');
    expect(stated.servers[0].url).toBe('https://orders.example');
  });

  test('are off with serveOpenAPI false', async () => {
    const off = app({ serveOpenAPI: false });
    expect((await off.request('/api/openapi.json')).status).toBe(404);
    expect((await off.request('/api/docs')).status).toBe(404);
  });
});

let ids = 0;
async function mcp(root: Hono, method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await root.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  expect(response.headers.get('content-type')).toBe('text/event-stream');
  const text = await response.text();
  expect(text.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(text.slice('event: message\ndata: '.length));
}

const call = async (root: Hono, name: string, args: unknown) => (await mcp(root, 'tools/call', { name, arguments: args })).result;

/** A tools/call's result for a 2026-07-28 client whose capabilities elicit forms, with params beside the name and arguments. */
async function elicitingCall(root: Hono, name: string, args: unknown, params: Record<string, unknown> = {}): Promise<Record<string, any>> {
  const _meta = {
    'io.modelcontextprotocol/protocolVersion': '2026-07-28',
    'io.modelcontextprotocol/clientInfo': { name: 't', version: '1' },
    'io.modelcontextprotocol/clientCapabilities': { elicitation: { form: {} } },
  };
  const response = await root.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', 'mcp-protocol-version': '2026-07-28' },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method: 'tools/call', params: { name, arguments: args, ...params, _meta } }),
  });
  expect(response.status).toBe(200);
  return (await response.json()).result;
}

describe('the MCP endpoint', () => {
  test('initialize names the API and its served version', async () => {
    const init = { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 't', version: '1' } };
    expect((await mcp(app(), 'initialize', init)).result.serverInfo).toEqual({ name: 'fixture-mcp', version: '1.0.0' });
    expect((await mcp(app({ openAPIVersion: '2.4.0' }), 'initialize', init)).result.serverInfo).toEqual({ name: 'fixture-mcp', version: '2.4.0' });
    const named = await mcp(app({ mcp: { serverInfo: { name: 'orders', version: '9' }, instructions: 'Orders.' } }), 'initialize', init);
    expect(named.result).toMatchObject({ serverInfo: { name: 'orders', version: '9' }, instructions: 'Orders.' });
  });

  test('tools/list holds the visible tools, with their policy, and no hidden or unclassified operation', async () => {
    const { tools } = (await mcp(app(), 'tools/list')).result;
    expect(tools.map((tool: { name: string }) => tool.name)).toEqual(['list_orders', 'get_order', 'update_order', 'open_return']);
    const byName = Object.fromEntries(tools.map((tool: { name: string }) => [tool.name, tool]));
    expect(byName.get_order).toMatchObject({ title: 'Get an order', description: 'Returns one order by its identifier.', annotations: { readOnlyHint: true } });
    expect(byName.get_order._meta).toMatchObject({ ui: { resourceUri: 'ui://orders/detail' }, invocationPolicy: 'auto' });
    expect(byName.get_order._meta['superschematic/operation-guidance'].useWhen).toBe('Use when you have an order identifier.');
    expect(byName.update_order).toMatchObject({ annotations: { readOnlyHint: false }, _meta: { invocationPolicy: 'ask' } });
    expect(byName.update_order.inputSchema.properties.confirmationToken).toMatchObject({ type: 'string' });
    expect(byName.get_order.inputSchema.properties.confirmationToken).toBeUndefined();
    expect(byName.list_orders.inputSchema.required).toEqual(['statuses']);
  });

  test('a hidden operation is no tool to call', async () => {
    const reply = await mcp(app(), 'tools/call', { name: 'delete_order', arguments: { id: ORDER } });
    expect(reply.error).toEqual({ code: -32602, message: 'fixture-mcp has no tool "delete_order"' });
  });

  test('tools/call runs the route, which reads each argument where the operation takes it', async () => {
    const got = await call(app(), 'get_order', { id: ORDER });
    expect(got.structuredContent.echo).toEqual({ name: 'getOrder', args: { id: ORDER_ID } });
    expect(JSON.parse(got.content[0].text)).toEqual(got.structuredContent);

    const listed = await call(app(), 'list_orders', { statuses: ['open', 'shipped'], limit: 5 });
    expect(listed.structuredContent).toBeUndefined();
    expect(JSON.parse(listed.content[0].text)[0].echo).toEqual({ statuses: ['open', 'shipped'], limit: 5 });

    const pickup = { line1: '1 Main St', city: 'Springfield', postalCode: null };
    const opened = await call(app(), 'open_return', { requestId: ORDER, orderId: ORDER, reason: 'Too small', pickup, labels: { size: 'S' } });
    expect(opened.structuredContent.echo.args.input).toEqual({ requestId: ORDER, orderId: ORDER, reason: 'Too small', pickup, labels: { size: 'S' } });

    // update_order asks: its first call runs nothing and answers a token,
    // and the same call with the token runs, once (D93).
    const root = app();
    const asked = await call(root, 'update_order', { id: ORDER, revision: 2, note: null });
    expect(asked).toMatchObject({ isError: true, structuredContent: { status: 428, code: 'confirmation_required', details: { tool: 'update_order' } } });
    expect(asked.structuredContent.details.summary).toBe(`Update an order (update_order) with {"id":"${ORDER}","note":null,"revision":2}`);
    const confirmationToken = asked.structuredContent.details.confirmationToken;
    const updated = await call(root, 'update_order', { id: ORDER, revision: 2, note: null, confirmationToken });
    expect(updated.structuredContent.echo.args).toEqual({ id: ORDER_ID, input: { revision: 2, note: null } });
    const again = await call(root, 'update_order', { id: ORDER, revision: 2, note: null, confirmationToken });
    expect(again.structuredContent).toMatchObject({ code: 'confirmation_required', details: { refused: 'used' } });
  });

  test('update_order is asked about in band for a client that elicits, and runs on accept (D93)', async () => {
    const root = app();
    ran.length = 0;
    const asked = await elicitingCall(root, 'update_order', { id: ORDER, revision: 2, note: null });
    expect(asked).toMatchObject({ resultType: 'input_required', inputRequests: { confirm: { method: 'elicitation/create' } } });
    expect(ran).toEqual([]);
    const answer = { inputResponses: { confirm: { action: 'accept' } }, requestState: asked.requestState };
    const updated = await elicitingCall(root, 'update_order', { id: ORDER, revision: 2, note: null }, answer);
    expect(updated.structuredContent.echo.args).toEqual({ id: ORDER_ID, input: { revision: 2, note: null } });
    expect(ran).toEqual(['updateOrder']);
  });

  test('a call of update_order its route would refuse is refused at once, and no one is asked (D98)', async () => {
    const root = app({ bodyLimits: { updateOrder: 64 } });
    ran.length = 0;
    const takesInput = await call(root, 'update_order', { id: ORDER, input: { revision: 2, note: null } });
    expect(takesInput).toMatchObject({ isError: true, structuredContent: { status: 400, code: 'bad_request', detail: 'update_order takes id, revision, note, not "input"' } });
    expect(takesInput.structuredContent.details).toBeUndefined();
    const misShaped = [
      { id: ORDER, revision: 'two', note: null },
      { id: ORDER, note: 'no revision' },
      { id: 'not an id!', revision: 2, note: null },
    ];
    for (const args of misShaped) {
      const refused = await call(root, 'update_order', args);
      expect(refused).toMatchObject({ isError: true, structuredContent: { status: 400, code: 'bad_request' } });
      expect(refused.structuredContent.details?.confirmationToken).toBeUndefined();
    }
    const tooLong = await call(root, 'update_order', { id: ORDER, revision: 2, note: 'x'.repeat(100) });
    expect(tooLong.structuredContent).toMatchObject({ status: 413, code: 'payload_too_large' });
    // A client that elicits is not asked either.
    const elicited = await elicitingCall(root, 'update_order', { id: ORDER, input: { revision: 2, note: null } });
    expect(elicited).toMatchObject({ resultType: 'complete', isError: true, structuredContent: { status: 400, code: 'bad_request' } });
    expect(elicited.requestState).toBeUndefined();

    // A token sent with a mis-shaped call is not spent: it runs its own call after.
    const confirmationToken = (await call(root, 'update_order', { id: ORDER, revision: 2, note: null })).structuredContent.details.confirmationToken;
    const withToken = await call(root, 'update_order', { id: ORDER, revision: 'two', note: null, confirmationToken });
    expect(withToken.structuredContent).toMatchObject({ status: 400, code: 'bad_request' });
    expect(ran).toEqual([]);
    const updated = await call(root, 'update_order', { id: ORDER, revision: 2, note: null, confirmationToken });
    expect(updated.structuredContent.echo.args).toEqual({ id: ORDER_ID, input: { revision: 2, note: null } });
    expect(ran).toEqual(['updateOrder']);
  });

  test("a caller update_order's route refuses is refused at once, and no one is asked (D98)", async () => {
    // A service credential that may read orders and not change them, as a
    // credential's scopes would say: the service step admits it to the
    // endpoint's own route and every other, and refuses it on
    // update_order's alone.
    const readOnly = async (ctx: RequestContext) => {
      if (ctx.operation.name === 'updateOrder') throw new HttpProblem(403, 'This credential may read orders, not change them', { code: 'service_forbidden' });
      return null;
    };
    const root = app({ authenticateService: readOnly });
    ran.length = 0;
    const refused = await call(root, 'update_order', { id: ORDER, revision: 2, note: null });
    expect(refused).toMatchObject({ isError: true, structuredContent: { status: 403, code: 'service_forbidden', detail: 'This credential may read orders, not change them' } });
    expect(refused.structuredContent.details).toBeUndefined();
    const elicited = await elicitingCall(root, 'update_order', { id: ORDER, revision: 2, note: null });
    expect(elicited).toMatchObject({ resultType: 'complete', isError: true, structuredContent: { status: 403 } });
    expect(ran).toEqual([]);
    expect((await call(root, 'get_order', { id: ORDER })).structuredContent.echo.name).toBe('getOrder');
  });

  test('a tool that asks needs the confirmation key', () => {
    expect(() => buildRouter({ order: {} }, {})).toThrow('mountMcp: tool update_order runs only once the person confirms it, so options.mcp.confirmationKey is required');
  });

  test("the route's rules refuse a call as they refuse a request, as a tool error", async () => {
    const empty = await call(app(), 'list_orders', { statuses: [] });
    expect(empty.isError).toBe(true);
    expect(empty.structuredContent).toMatchObject({ status: 400, code: 'bad_request' });
    const short = await call(app(), 'open_return', { requestId: ORDER, orderId: ORDER, reason: '', pickup: { line1: 'a', city: 'b', postalCode: null }, labels: {} });
    expect(short.structuredContent).toMatchObject({ status: 400 });
    const notAnId = await call(app(), 'get_order', { id: 'not an id!' });
    expect(notAnId.structuredContent).toMatchObject({ status: 400 });
  });
});
