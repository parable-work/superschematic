import { describe, expect, test } from 'bun:test';
import { parseIdentityUUID } from 'superscalar/scalars';
import { Hono } from 'hono';
import { HttpProblem, MemoryRateLimitStore, OperationResult, type OperationSpec, type Principal, type RateLimitStore } from './index';
import { errorHandler, honoPath, mountManualOperation, mountOperation, notFoundHandler, parseJsonBody } from './hono';

const getOrder: OperationSpec = {
  name: 'getOrder',
  namespace: 'order',
  method: 'GET',
  path: '/api/orders/{id}',
  pathParams: [{ name: 'id', kind: 'uuid', required: true }],
  queryParams: [{ name: 'includeArchived', kind: 'boolean', required: false }],
  bodyParams: [],
  auth: { public: false, required: true, permissions: ['orders.read'] },
  manual: false,
};

const createOrder: OperationSpec = {
  name: 'createOrder',
  namespace: 'order',
  method: 'POST',
  path: '/api/orders',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  input: {
    parse: value => {
      if (typeof value !== 'object' || value === null || typeof (value as { name?: unknown }).name !== 'string') {
        throw new Error('name required');
      }
      return value;
    },
    required: true,
  },
  auth: { public: false, required: true, permissions: ['orders.write'] },
  bodyLimitBytes: 64,
  manual: false,
};

const health: OperationSpec = {
  name: 'health',
  namespace: 'probe',
  method: 'GET',
  path: '/api/health',
  pathParams: [],
  queryParams: [],
  bodyParams: [],
  auth: { public: true, required: false, permissions: [] },
  manual: false,
};

const stream: OperationSpec = {
  ...createOrder,
  name: 'stream',
  path: '/api/stream',
  input: undefined,
  bodyLimitBytes: undefined,
  manual: true,
};

const principals: Record<string, Principal> = {
  reader: { subject: 'u1', permissions: ['orders.read'] },
  writer: { subject: 'u2', permissions: ['orders'] },
};

function build(extra: (app: Hono) => void = () => {}) {
  const app = new Hono();
  const options = {
    authenticate: async (ctx: { headers: Headers }) => principals[ctx.headers.get('x-user') ?? ''] ?? null,
    onError: (error: unknown) => (error instanceof RangeError ? new HttpProblem(502, 'upstream', { code: 'upstream_error' }) : undefined),
  };
  mountOperation(app, health, async () => ({ status: 'ok' }), options);
  mountOperation(app, getOrder, async (ctx, request) => ({ id: request.path.id, archived: request.query.includeArchived ?? null, who: ctx.principal?.subject }), options);
  mountOperation(app, createOrder, async (_ctx, request) => new OperationResult({ created: request.input }, 201, { location: '/api/orders/new' }), options);
  mountManualOperation(app, stream, (c, ctx) => c.text(`stream for ${ctx.principal?.subject} ${ctx.requestId}`), options);
  extra(app);
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
  return app;
}

describe('parseJsonBody', () => {
  test('parses JSON and resolves undefined for an empty body', async () => {
    expect(await parseJsonBody(new Request('http://x', { method: 'POST', body: '{"a":1}' }))).toEqual({ a: 1 });
    expect(await parseJsonBody(new Request('http://x', { method: 'POST' }))).toBeUndefined();
  });

  test('refuses a non-JSON body with 400', async () => {
    await expect(parseJsonBody(new Request('http://x', { method: 'POST', body: '{nope' }))).rejects.toMatchObject({
      status: 400,
      code: 'bad_request',
    });
  });
});

describe('honoPath', () => {
  test('rewrites brace placeholders to colon captures', () => {
    expect(honoPath('/api/orders/{orderId}/lines/{lineIndex}')).toBe('/api/orders/:orderId/lines/:lineIndex');
  });
});

describe('mountOperation', () => {
  test('public route answers the success envelope with the request id', async () => {
    const response = await build().request('/api/health', { headers: { 'x-request-id': 'probe-1' } });
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toBe('application/json');
    expect(await response.json()).toEqual({ data: { status: 'ok' }, meta: { requestId: 'probe-1' } });
  });

  test('gate: 401 without a principal, 403 without the permission, 200 with it', async () => {
    const id = '00000000-0000-4000-8000-000000000001';
    const anonymous = await build().request(`/api/orders/${id}`);
    expect(anonymous.status).toBe(401);
    expect(await anonymous.json()).toMatchObject({ status: 401, code: 'unauthorized', title: 'Unauthorized', type: 'about:blank' });
    const reader = await build().request(`/api/orders/${id}?includeArchived=true`, { headers: { 'x-user': 'reader' } });
    expect(reader.status).toBe(200);
    expect((await reader.json()).data).toEqual({ id: parseIdentityUUID(id), archived: true, who: 'u1' });
    const forbidden = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'reader' }, body: '{"name":"x"}' });
    expect(forbidden.status).toBe(403);
    expect(await forbidden.json()).toMatchObject({ status: 403, code: 'forbidden' });
  });

  test('a permissionMatcher replaces the default coverage rule', async () => {
    const app = new Hono();
    const rootCoversAll = (held: readonly string[], required: readonly string[]) =>
      required.length === 0 || held.includes('root') || required.some(need => held.includes(need));
    const options = {
      authenticate: async (ctx: { headers: Headers }) => (ctx.headers.get('x-user') === 'root' ? { subject: 'r', permissions: ['root'] } : null),
      permissionMatcher: rootCoversAll,
    };
    mountOperation(app, createOrder, async (_ctx, request) => request.input, options);
    const allowed = await app.request('/api/orders', { method: 'POST', headers: { 'x-user': 'root' }, body: '{"name":"x"}' });
    expect(allowed.status).toBe(200);
    const defaults = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'reader' }, body: '{"name":"x"}' });
    expect(defaults.status).toBe(403);
  });

  test('decodes path and query parameters and refuses malformed ones with 400', async () => {
    const bad = await build().request('/api/orders/not-a-uuid', { headers: { 'x-user': 'reader' } });
    expect(bad.status).toBe(400);
    expect(await bad.json()).toMatchObject({ status: 400, code: 'bad_request', details: { location: 'path', parameter: 'id' } });
    const badQuery = await build().request('/api/orders/00000000-0000-4000-8000-000000000001?includeArchived=maybe', { headers: { 'x-user': 'reader' } });
    expect(badQuery.status).toBe(400);
    expect(await badQuery.json()).toMatchObject({ details: { location: 'query', parameter: 'includeArchived' } });
  });

  test('parses the body through the strict parser and honours the body cap', async () => {
    const created = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'writer' }, body: '{"name":"x"}' });
    expect(created.status).toBe(201);
    expect(created.headers.get('location')).toBe('/api/orders/new');
    expect((await created.json()).data).toEqual({ created: { name: 'x' } });
    const invalid = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'writer' }, body: '{"nope":1}' });
    expect(invalid.status).toBe(400);
    expect(await invalid.json()).toMatchObject({ status: 400, code: 'bad_request' });
    const missing = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'writer' } });
    expect(missing.status).toBe(400);
    const oversize = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'writer' }, body: JSON.stringify({ name: 'x'.repeat(100) }) });
    expect(oversize.status).toBe(413);
    expect(await oversize.json()).toMatchObject({ status: 413, code: 'payload_too_large' });
    const invalidJson = await build().request('/api/orders', { method: 'POST', headers: { 'x-user': 'writer' }, body: '{nope' });
    expect(invalidJson.status).toBe(400);
    expect(await invalidJson.json()).toMatchObject({ status: 400, code: 'bad_request' });
  });

  test('hono/bearer-auth extracts a well-formed token and refuses a malformed Authorization header', async () => {
    const id = '00000000-0000-4000-8000-000000000001';
    const app = new Hono();
    mountOperation(app, getOrder, async ctx => ({ token: ctx.bearerToken ?? null }), {
      authenticate: async ctx => (ctx.bearerToken === 'good.token.value' ? principals.reader : null),
    });
    app.onError(errorHandler());
    const ok = await app.request(`/api/orders/${id}`, { headers: { authorization: 'Bearer good.token.value' } });
    expect(ok.status).toBe(200);
    expect((await ok.json()).data).toEqual({ token: 'good.token.value' });
    const malformed = await app.request(`/api/orders/${id}`, { headers: { authorization: 'Bearer not a token' } });
    expect(malformed.status).toBe(400);
    expect(await malformed.json()).toMatchObject({ status: 400, type: 'about:blank' });
  });

  test('maps thrown problems, mapped failures and unknown failures', async () => {
    const app = build(a => {
      const boom: OperationSpec = { ...health, name: 'boom', path: '/api/boom' };
      mountOperation(a, boom, async () => {
        throw new HttpProblem(409, 'taken', { code: 'WA-X-001', details: { k: 1 }, extensions: { outcome: 'stored' } });
      });
      mountOperation(a, { ...boom, name: 'upstream', path: '/api/upstream' }, async () => {
        throw new RangeError('upstream');
      }, { onError: error => (error instanceof RangeError ? new HttpProblem(502, 'upstream', { code: 'upstream_error' }) : undefined) });
      mountOperation(a, { ...boom, name: 'crash', path: '/api/crash' }, async () => {
        throw new Error('secret internals');
      });
    });
    const conflict = await app.request('/api/boom');
    expect(conflict.status).toBe(409);
    expect(await conflict.json()).toMatchObject({ status: 409, code: 'WA-X-001', details: { k: 1 }, outcome: 'stored', title: 'Conflict' });
    const upstream = await app.request('/api/upstream');
    expect(upstream.status).toBe(502);
    expect(await upstream.json()).toMatchObject({ code: 'upstream_error' });
    const crash = await app.request('/api/crash');
    expect(crash.status).toBe(500);
    const body = await crash.json();
    expect(body).toMatchObject({ status: 500, code: 'internal_error' });
    expect(JSON.stringify(body)).not.toContain('secret internals');
  });

  test('a list-of-lists body parameter is decoded from its JSON value, other body parameters as before', async () => {
    const replaceRows: OperationSpec = {
      ...health,
      name: 'replaceRows',
      method: 'PUT',
      path: '/api/rows',
      bodyParams: [
        { name: 'rows', kind: 'enum', required: true, isArray: true, isArrayOfArrays: true, enumValues: ['light', 'dark'] },
        { name: 'note', kind: 'string', required: false },
      ],
    };
    const app = build(a => {
      mountOperation(a, replaceRows, async (_ctx, request) => request.body);
    });
    const put = (body: unknown) => app.request('/api/rows', { method: 'PUT', body: JSON.stringify(body) });
    const saved = await put({ rows: [['light'], [], ['dark', 'light']], note: 'n' });
    expect(saved.status).toBe(200);
    expect((await saved.json()).data).toEqual({ rows: [['light'], [], ['dark', 'light']], note: 'n' });
    const nullRow = await put({ rows: [['light'], null] });
    expect(nullRow.status).toBe(400);
    expect(await nullRow.json()).toMatchObject({
      status: 400,
      code: 'bad_request',
      detail: 'Invalid body parameter rows[1]: required field',
      details: { location: 'body', parameter: 'rows', path: 'rows[1]', errors: [{ validator: 'required', message: 'required field' }] },
    });
    const badElement = await put({ rows: [['light', 'dim']] });
    expect(badElement.status).toBe(400);
    expect(await badElement.json()).toMatchObject({ details: { parameter: 'rows', path: 'rows[0][1]' } });
    const missing = await put({ note: 'n' });
    expect(missing.status).toBe(400);
    expect(await missing.json()).toMatchObject({ details: { location: 'body', parameter: 'rows', reason: 'required' } });
  });

  test('an object-typed body parameter is decoded from its JSON value and reaches the handler as objects', async () => {
    const parse = (value: unknown) => {
      if (typeof (value as { x?: unknown }).x !== 'number') throw new Error('parsePoint json validation failed');
      return value;
    };
    const saveOutline: OperationSpec = {
      ...health,
      name: 'saveOutline',
      method: 'PUT',
      path: '/api/outline',
      bodyParams: [
        { name: 'points', kind: 'object', required: true, isArray: true, parse },
        { name: 'origin', kind: 'object', required: false, parse },
        { name: 'note', kind: 'string', required: false },
      ],
    };
    const app = build(a => {
      mountOperation(a, saveOutline, async (_ctx, request) => ({ body: request.body, pointType: typeof (request.body.points as unknown[])[0] }));
    });
    const put = (body: unknown) => app.request('/api/outline', { method: 'PUT', body: JSON.stringify(body) });
    const saved = await put({ points: [{ x: 1 }, { x: 2 }], origin: { x: 0 }, note: 'n' });
    expect(saved.status).toBe(200);
    expect((await saved.json()).data).toEqual({ body: { points: [{ x: 1 }, { x: 2 }], origin: { x: 0 }, note: 'n' }, pointType: 'object' });
    const nullPoint = await put({ points: [{ x: 1 }, null] });
    expect(nullPoint.status).toBe(400);
    expect(await nullPoint.json()).toMatchObject({
      detail: 'Invalid body parameter points[1]: required field',
      details: { location: 'body', parameter: 'points', path: 'points[1]', errors: [{ validator: 'required', message: 'required field' }] },
    });
    const badOrigin = await put({ points: [], origin: 'o' });
    expect(badOrigin.status).toBe(400);
    expect(await badOrigin.json()).toMatchObject({ details: { location: 'body', parameter: 'origin', reason: 'expected an object' } });
  });

  test('a list-of-lists result is sent with every nullish list as []', async () => {
    const rowsOf: OperationSpec = { ...health, name: 'rowsOf', path: '/api/rows/{kind}', outputIsArrayOfArrays: true };
    const results: Record<string, unknown> = {
      ragged: [['a'], null, undefined, [], ['b', 'c']],
      absent: undefined,
      wrapped: new OperationResult([null, ['x']], 202),
    };
    const app = build(a => {
      mountOperation(a, rowsOf, async ctx => results[ctx.pathParams.kind ?? '']);
    });
    const ragged = await app.request('/api/rows/ragged');
    expect((await ragged.json()).data).toEqual([['a'], [], [], [], ['b', 'c']]);
    const absent = await app.request('/api/rows/absent');
    expect((await absent.json()).data).toEqual([]);
    const wrapped = await app.request('/api/rows/wrapped');
    expect(wrapped.status).toBe(202);
    expect((await wrapped.json()).data).toEqual([[], ['x']]);
    // Without the flag a result is sent as returned.
    const plain = build(a => {
      mountOperation(a, { ...health, name: 'plain', path: '/api/plain' }, async () => [null]);
    });
    expect((await (await plain.request('/api/plain')).json()).data).toEqual([null]);
  });

  test('a handler may answer with a finished Response', async () => {
    const app = build(a => {
      mountOperation(a, { ...health, name: 'raw', path: '/api/raw' }, async () => new Response('plain', { status: 202 }));
    });
    const response = await app.request('/api/raw');
    expect(response.status).toBe(202);
    expect(await response.text()).toBe('plain');
  });
});

describe('mountManualOperation', () => {
  test('runs the gate and hands the Hono context to the hook', async () => {
    const anonymous = await build().request('/api/stream', { method: 'POST' });
    expect(anonymous.status).toBe(401);
    const response = await build().request('/api/stream', { method: 'POST', headers: { 'x-user': 'writer', 'x-request-id': 'm-1' } });
    expect(response.status).toBe(200);
    expect(await response.text()).toBe('stream for u2 m-1');
  });

  test('answers 501 when no hook is supplied and honours a path alias', async () => {
    const app = new Hono();
    mountManualOperation(app, { ...stream, auth: { public: true, required: false, permissions: [] } }, undefined);
    mountManualOperation(app, { ...stream, auth: { public: true, required: false, permissions: [] } }, c => c.text('alias'), {}, { path: '/stream' });
    const missing = await app.request('/api/stream', { method: 'POST' });
    expect(missing.status).toBe(501);
    expect(await missing.json()).toMatchObject({ status: 501, code: 'not_implemented' });
    const alias = await app.request('/stream', { method: 'POST' });
    expect(await alias.text()).toBe('alias');
  });
});

describe('application root handlers', () => {
  test('notFound and onError answer the problem envelope', async () => {
    const app = build(a => {
      a.get('/api/plain-throw', () => {
        throw new HttpProblem(418, 'teapot', { code: 'teapot' });
      });
      a.get('/api/plain-crash', () => {
        throw new Error('nope');
      });
    });
    const missing = await app.request('/api/nothing');
    expect(missing.status).toBe(404);
    expect(missing.headers.get('content-type')).toBe('application/problem+json');
    expect(await missing.json()).toMatchObject({ status: 404, code: 'not_found', type: 'about:blank' });
    const teapot = await app.request('/api/plain-throw');
    expect(teapot.status).toBe(418);
    expect(await teapot.json()).toMatchObject({ code: 'teapot' });
    const crash = await app.request('/api/plain-crash');
    expect(crash.status).toBe(500);
    expect(await crash.json()).toMatchObject({ code: 'internal_error' });
  });
});

describe('@rateLimit', () => {
  const limited: OperationSpec = { ...health, name: 'limited', path: '/api/limited', rateLimitPerMinute: 2 };

  function limitedApp(rateLimit?: { store?: RateLimitStore; keyOf?: (ctx: { principal: Principal | null }) => string; now?: () => number }) {
    const app = new Hono();
    const options = { rateLimit };
    mountOperation(app, limited, async () => ({ ok: true }), options);
    mountManualOperation(app, { ...limited, name: 'limitedManual', path: '/api/limited-manual', manual: true }, c => c.text('manual'), options);
    return app;
  }

  test('refuses the request past the per-minute budget with 429 and Retry-After, per client IP', async () => {
    const app = limitedApp();
    const from = (ip: string) => app.request('/api/limited', { headers: { 'x-forwarded-for': `${ip}, 10.0.0.1` } });
    expect((await from('203.0.113.7')).status).toBe(200);
    expect((await from('203.0.113.7')).status).toBe(200);
    const refused = await from('203.0.113.7');
    expect(refused.status).toBe(429);
    expect(refused.headers.get('content-type')).toBe('application/problem+json');
    expect(refused.headers.get('retry-after')).toMatch(/^[1-9]\d*$/u);
    expect(await refused.json()).toMatchObject({ status: 429, code: 'too_many_requests', title: 'Too Many Requests', details: { retryAfterSeconds: expect.any(Number) } });
    expect((await from('203.0.113.8')).status).toBe(200);
  });

  test('buckets are per operation: a manual route has its own budget, and the limit runs before the gate', async () => {
    const app = limitedApp();
    const headers = { 'x-real-ip': '198.51.100.4' };
    expect((await app.request('/api/limited', { headers })).status).toBe(200);
    expect((await app.request('/api/limited', { headers })).status).toBe(200);
    expect((await app.request('/api/limited', { headers })).status).toBe(429);
    expect((await app.request('/api/limited-manual', { headers })).status).toBe(200);
    expect((await app.request('/api/limited-manual', { headers })).status).toBe(200);
    expect((await app.request('/api/limited-manual', { headers })).status).toBe(429);
  });

  test('refills at the declared rate and honours a pluggable store and key', async () => {
    let now = 0;
    const store = new MemoryRateLimitStore();
    const keys: string[] = [];
    const recording: RateLimitStore = {
      take(key, limit, at) {
        keys.push(key);
        return store.take(key, limit, at);
      },
    };
    const app = limitedApp({ store: recording, keyOf: () => 'everyone', now: () => now });
    expect((await app.request('/api/limited')).status).toBe(200);
    expect((await app.request('/api/limited')).status).toBe(200);
    const refused = await app.request('/api/limited');
    expect(refused.status).toBe(429);
    expect(refused.headers.get('retry-after')).toBe('30');
    now += 30_000;
    expect((await app.request('/api/limited')).status).toBe(200);
    expect((await app.request('/api/limited')).status).toBe(429);
    expect(new Set(keys)).toEqual(new Set(['limited:everyone']));
  });

  test('a mount override of 0 disables the schema limit', async () => {
    const app = new Hono();
    mountOperation(app, limited, async () => ({ ok: true }), {}, { rateLimitPerMinute: 0 });
    for (let i = 0; i < 5; i += 1) expect((await app.request('/api/limited')).status).toBe(200);
  });
});

describe('MemoryRateLimitStore', () => {
  test('token bucket: capacity is the per-minute limit, refill is continuous, idle buckets are swept past maxKeys', () => {
    const store = new MemoryRateLimitStore(2);
    expect(store.take('a', 60, 0)).toEqual({ allowed: true, retryAfterSeconds: 0 });
    for (let i = 0; i < 59; i += 1) expect(store.take('a', 60, 0).allowed).toBe(true);
    expect(store.take('a', 60, 0)).toEqual({ allowed: false, retryAfterSeconds: 1 });
    expect(store.take('a', 60, 999).allowed).toBe(false);
    expect(store.take('a', 60, 1000).allowed).toBe(true);
    expect(store.take('b', 60, 1000).allowed).toBe(true);
    expect(store.size).toBe(2);
    expect(store.take('c', 60, 61_000).allowed).toBe(true);
    expect(store.size).toBe(1);
    expect(store.take('d', 0, 0)).toEqual({ allowed: false, retryAfterSeconds: 60 });
  });
});

describe('@timeout', () => {
  const slow: OperationSpec = { ...health, name: 'slow', path: '/api/slow', timeoutSeconds: 0.05 };

  test('answers 504 gateway_timeout when the implementation outlives the deadline, and aborts ctx.signal', async () => {
    const app = new Hono();
    let aborted = false;
    mountOperation(app, slow, async ctx => {
      await new Promise<void>(resolve => {
        ctx.signal.addEventListener('abort', () => {
          aborted = true;
          resolve();
        });
        setTimeout(resolve, 1000);
      });
      return { late: true };
    });
    const response = await app.request('/api/slow', { headers: { 'x-request-id': 't-1' } });
    expect(response.status).toBe(504);
    expect(await response.json()).toMatchObject({ status: 504, code: 'gateway_timeout', title: 'Gateway Timeout', requestId: 't-1' });
    expect(aborted).toBe(true);
  });

  test('a fast implementation answers normally, and a mount override of 0 disables the deadline', async () => {
    const app = new Hono();
    mountOperation(app, slow, async () => ({ fast: true }));
    mountOperation(app, { ...slow, name: 'unbounded', path: '/api/unbounded' }, async () => {
      await new Promise(resolve => setTimeout(resolve, 80));
      return { done: true };
    }, {}, { timeoutSeconds: 0 });
    expect((await app.request('/api/slow')).status).toBe(200);
    const unbounded = await app.request('/api/unbounded');
    expect(unbounded.status).toBe(200);
    expect((await unbounded.json()).data).toEqual({ done: true });
  });

  test('covers the gate and a manual route handler too', async () => {
    const app = new Hono();
    const gated = { ...slow, name: 'gated', path: '/api/gated', auth: { public: false, required: true, permissions: [] } };
    mountOperation(app, gated, async () => ({ ok: true }), {
      authenticate: async () => {
        await new Promise(resolve => setTimeout(resolve, 200));
        return { subject: 'late', permissions: [] };
      },
    });
    mountManualOperation(app, { ...slow, name: 'slowManual', path: '/api/slow-manual', manual: true }, async c => {
      await new Promise(resolve => setTimeout(resolve, 200));
      return c.text('late');
    });
    expect((await app.request('/api/gated')).status).toBe(504);
    expect((await app.request('/api/slow-manual')).status).toBe(504);
  });
});
