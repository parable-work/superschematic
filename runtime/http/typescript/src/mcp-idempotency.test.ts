import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { mountOperation, type RouterRuntimeOptions } from './hono';
import { memoryIdempotencyStore, type Authenticator, type OperationSpec } from './index';
import { MCP_IDEMPOTENCY_ARGUMENT, mountMcp, type McpRouteTool } from './mcp';

/*
The MCP endpoint and @idempotent operations (D140): a tool call has no
headers, so the tool of such an operation takes the call's key as an
argument, idempotencyKey, which the endpoint sends to the route as the
Idempotency-Key a request carries. The route keeps the key as it keeps a
request's.
*/

const auth = { public: false, required: true, permissions: [] };

const specs = {
  placeOrder: {
    name: 'placeOrder',
    namespace: 'orders',
    method: 'POST',
    path: '/api/orders',
    pathParams: [],
    queryParams: [],
    bodyParams: [{ name: 'sku', kind: 'string', required: true }],
    auth,
    idempotent: { ttlSeconds: 3600 },
    manual: false,
  },
  cancelOrder: {
    name: 'cancelOrder',
    namespace: 'orders',
    method: 'DELETE',
    path: '/api/orders/{number}',
    pathParams: [{ name: 'number', kind: 'string', required: true }],
    queryParams: [],
    bodyParams: [],
    auth,
    idempotent: { ttlSeconds: 3600, required: true },
    manual: false,
  },
  listOrders: {
    name: 'listOrders',
    namespace: 'orders',
    method: 'GET',
    path: '/api/orders',
    pathParams: [],
    queryParams: [],
    bodyParams: [],
    auth,
    manual: false,
  },
  // An operation whose own argument has the name keeps it.
  tagOrder: {
    name: 'tagOrder',
    namespace: 'orders',
    method: 'POST',
    path: '/api/orders/tag',
    pathParams: [],
    queryParams: [],
    bodyParams: [{ name: 'idempotencyKey', kind: 'string', required: true }],
    auth,
    idempotent: { ttlSeconds: 3600 },
    manual: false,
  },
} as const satisfies Record<string, OperationSpec>;

const tool = (name: string, operation: keyof typeof specs, args: McpRouteTool['arguments'], required: string[] = []): McpRouteTool => ({
  name,
  inputSchema: { type: 'object', additionalProperties: false, properties: Object.fromEntries(args.map(arg => [arg.key, { type: 'string' }])), required },
  operation,
  arguments: args,
});

const tools: McpRouteTool[] = [
  tool('place_order', 'placeOrder', [{ key: 'sku', in: 'body', name: 'sku' }], ['sku']),
  tool('cancel_order', 'cancelOrder', [{ key: 'number', in: 'path', name: 'number' }], ['number']),
  tool('list_orders', 'listOrders', []),
  tool('tag_order', 'tagOrder', [{ key: 'idempotencyKey', in: 'body', name: 'idempotencyKey' }], ['idempotencyKey']),
];

const authenticate: Authenticator = async ctx => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: [] } : null);

function router(): { app: Hono; runs: string[] } {
  const app = new Hono();
  const runs: string[] = [];
  const options: RouterRuntimeOptions = { authenticate, idempotencyStore: memoryIdempotencyStore() };
  mountOperation(
    app,
    specs.placeOrder,
    async (ctx, request) => {
      runs.push(`place ${request.body.sku} by ${ctx.principal?.subject}`);
      return { number: runs.length, sku: request.body.sku };
    },
    options
  );
  mountOperation(
    app,
    specs.cancelOrder,
    async (_ctx, request) => {
      runs.push(`cancel ${request.path.number}`);
      return { number: request.path.number, cancelled: true };
    },
    options
  );
  mountOperation(app, specs.listOrders, async () => [], options);
  mountOperation(
    app,
    specs.tagOrder,
    async (_ctx, request) => {
      runs.push(`tag ${request.body.idempotencyKey}`);
      return { tag: request.body.idempotencyKey };
    },
    options
  );
  mountMcp(app, specs, tools, options, { api: 'orders-api', version: '1.0.0', requiresAuth: true });
  return { app, runs };
}

let ids = 0;
async function rpc(app: Hono, token: string, method: string, params?: unknown, headers: Record<string, string> = {}): Promise<Record<string, any>> {
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: `Bearer ${token}`, ...headers },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  const frame = (await response.text()).split('\n\n').find(part => part.trim() !== '') ?? '';
  return JSON.parse(frame.slice('event: message\ndata: '.length));
}

const call = (app: Hono, token: string, name: string, args: unknown, headers: Record<string, string> = {}) =>
  rpc(app, token, 'tools/call', { name, arguments: args }, headers);

describe('the MCP endpoint and @idempotent operations (D140)', () => {
  test("tools/list gives an @idempotent operation's tool the key argument, required where the operation requires it", async () => {
    const { app } = router();
    const listed = (await rpc(app, 'ada', 'tools/list')).result.tools as Array<{ name: string; inputSchema: { properties: Record<string, any>; required: string[] } }>;
    const schema = (name: string) => listed.find(t => t.name === name)!.inputSchema;
    expect(MCP_IDEMPOTENCY_ARGUMENT).toBe('idempotencyKey');
    expect(schema('place_order').properties.idempotencyKey).toMatchObject({ type: 'string', minLength: 1, maxLength: 255 });
    expect(schema('place_order').required).toEqual(['sku']);
    expect(schema('cancel_order').required).toEqual(['number', 'idempotencyKey']);
    expect(schema('list_orders').properties).toEqual({});
    // The operation's own argument of the name is left as it declares it.
    expect(schema('tag_order').properties.idempotencyKey).toEqual({ type: 'string' });
  });

  test('a repeat of a call under its key runs nothing and is answered the first answer', async () => {
    const { app, runs } = router();
    const first = await call(app, 'ada', 'place_order', { sku: 'tea', idempotencyKey: 'order-1' });
    const again = await call(app, 'ada', 'place_order', { sku: 'tea', idempotencyKey: 'order-1' });
    expect(first.result.structuredContent).toEqual({ number: 1, sku: 'tea' });
    expect(again.result).toEqual(first.result);
    expect(runs).toEqual(['place tea by ada']);

    // The key with other arguments is refused, and nothing runs.
    const other = await call(app, 'ada', 'place_order', { sku: 'coffee', idempotencyKey: 'order-1' });
    expect(other.result.isError).toBe(true);
    expect(other.result.structuredContent).toMatchObject({ status: 422, code: 'idempotency_key_reused' });
    // A key is one caller's: another caller's call under it runs.
    expect((await call(app, 'grace', 'place_order', { sku: 'tea', idempotencyKey: 'order-1' })).result.structuredContent).toEqual({ number: 2, sku: 'tea' });
    // Without the argument the call runs each time.
    await call(app, 'ada', 'place_order', { sku: 'tea' });
    await call(app, 'ada', 'place_order', { sku: 'tea' });
    expect(runs).toEqual(['place tea by ada', 'place tea by grace', 'place tea by ada', 'place tea by ada']);
  });

  test('an operation that requires a key refuses a call without the argument', async () => {
    const { app, runs } = router();
    const none = await call(app, 'ada', 'cancel_order', { number: '7' });
    expect(none.result.structuredContent).toMatchObject({ status: 400, code: 'idempotency_key_required' });
    for (let i = 0; i < 2; i++) {
      expect((await call(app, 'ada', 'cancel_order', { number: '7', idempotencyKey: 'cancel-7' })).result.structuredContent).toEqual({ number: '7', cancelled: true });
    }
    expect(runs).toEqual(['cancel 7']);
  });

  test('a key that is no key is a tool error, and the argument is no argument of another tool', async () => {
    const { app, runs } = router();
    for (const key of ['', 'x'.repeat(256), 'line\nbreak', 7]) {
      const refused = await call(app, 'ada', 'place_order', { sku: 'tea', idempotencyKey: key });
      expect(refused.result.structuredContent).toMatchObject({
        status: 400,
        code: 'idempotency_key_invalid',
        detail: 'place_order: idempotencyKey is 1 to 255 printable ASCII characters',
      });
    }
    const extra = await call(app, 'ada', 'list_orders', { idempotencyKey: 'order-1' });
    expect(extra.result.structuredContent.detail).toBe('list_orders takes no arguments, not "idempotencyKey"');
    expect(runs).toEqual([]);
  });

  test("the endpoint request's own Idempotency-Key is not a call's", async () => {
    const { app, runs } = router();
    for (let i = 0; i < 2; i++) {
      await call(app, 'ada', 'place_order', { sku: 'tea' }, { 'idempotency-key': 'transport-1' });
    }
    expect(runs.length).toBe(2);
  });

  test("an argument of the operation's own named idempotencyKey stays its argument", async () => {
    const { app, runs } = router();
    for (let i = 0; i < 2; i++) {
      expect((await call(app, 'ada', 'tag_order', { idempotencyKey: 'blue' })).result.structuredContent).toEqual({ tag: 'blue' });
    }
    expect(runs).toEqual(['tag blue', 'tag blue']);
  });
});
