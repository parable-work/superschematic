import { describe, expect, test } from 'bun:test';
import { createHmac } from 'node:crypto';
import { parseIdentityUUID } from 'superscalar/scalars';
import { Hono } from 'hono';
import {
  HttpProblem,
  MemoryRateLimitStore,
  OperationResult,
  clientIpKey,
  type OperationSpec,
  type Principal,
  type RateLimitOptions,
  type RateLimitStore,
} from './index';
import { errorHandler, honoPath, mountManualOperation, mountOperation, notFoundHandler, parseJsonBody, type WebhookVerifier } from './hono';

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

  test('decodes each path parameter exactly once, and refuses a path whose percent-encoding does not decode', async () => {
    const app = new Hono();
    const getItem: OperationSpec = { ...health, name: 'getItem', path: '/api/items/{id}', pathParams: [{ name: 'id', kind: 'string', required: true }] };
    mountOperation(app, getItem, async (_ctx, request) => ({ id: request.path.id }));
    app.onError(errorHandler());
    const idOf = async (segment: string) => {
      const response = await app.request(`/api/items/${segment}`);
      expect(response.status).toBe(200);
      return (await response.json()).data.id;
    };
    // A value with a literal % (or an escape sequence as text) is sent
    // encoded once and must arrive as it was sent.
    for (const id of ['%', 'a%25b', '100%', 'x%41y', '%E9', 'a/b', 'caf\u00e9', 'a+b c']) {
      expect(await idOf(encodeURIComponent(id))).toBe(id);
    }
    // Encodings other than encodeURIComponent's decode to the same value.
    expect(await idOf('%41')).toBe('A');
    expect(await idOf('caf%c3%a9')).toBe('caf\u00e9');
    expect(await idOf('a%2fb')).toBe('a/b');
    for (const segment of ['%', '100%', '%ZZ', 'a%2', '%E9', '%C3%28']) {
      const response = await app.request(`/api/items/${segment}`);
      expect(response.status).toBe(400);
      expect(await response.json()).toMatchObject({ status: 400, code: 'bad_request', details: { location: 'path' } });
    }
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

  test('an Authorization header of another scheme reaches authenticate', async () => {
    const id = '00000000-0000-4000-8000-000000000001';
    const app = new Hono();
    const seen: Array<string | null> = [];
    mountOperation(app, getOrder, async ctx => ({ who: ctx.principal?.subject, token: ctx.bearerToken ?? null }), {
      authenticate: async ctx => {
        const header = ctx.headers.get('authorization');
        seen.push(header);
        return header === 'ApiKey k-reader' ? principals.reader : null;
      },
    });
    app.onError(errorHandler());
    const keyed = await app.request(`/api/orders/${id}`, { headers: { authorization: 'ApiKey k-reader' } });
    expect(keyed.status).toBe(200);
    expect((await keyed.json()).data).toEqual({ who: 'u1', token: null });
    expect((await app.request(`/api/orders/${id}`, { headers: { authorization: 'Basic dXNlcjpwdw==' } })).status).toBe(401);
    expect(seen).toEqual(['ApiKey k-reader', 'Basic dXNlcjpwdw==']);
    // A Bearer header is still parsed, case-insensitively, and a malformed one refused.
    expect((await app.request(`/api/orders/${id}`, { headers: { authorization: 'bearer not a token' } })).status).toBe(400);
    expect(seen).toHaveLength(2);
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

  test('every body parameter is decoded from its JSON value: lists of lists, lists and single values', async () => {
    const replaceRows: OperationSpec = {
      ...health,
      name: 'replaceRows',
      method: 'PUT',
      path: '/api/rows',
      bodyParams: [
        { name: 'rows', kind: 'enum', required: true, isArray: true, isArrayOfArrays: true, enumValues: ['light', 'dark'] },
        { name: 'note', kind: 'string', required: false },
        { name: 'tags', kind: 'string', required: false, isArray: true },
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
    const tagged = await put({ rows: [], tags: ['a,b', ''] });
    expect(tagged.status).toBe(200);
    expect((await tagged.json()).data).toEqual({ rows: [], tags: ['a,b', ''] });
    const numericNote = await put({ rows: [], note: 5 });
    expect(numericNote.status).toBe(400);
    expect(await numericNote.json()).toMatchObject({ details: { location: 'body', parameter: 'note', errors: [{ validator: 'type', message: 'expected a string' }] } });
    const nullTag = await put({ rows: [], tags: ['a', null] });
    expect(nullTag.status).toBe(400);
    expect(await nullTag.json()).toMatchObject({ details: { location: 'body', parameter: 'tags', path: 'tags[1]', errors: [{ validator: 'required', message: 'required field' }] } });
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

  test('hands the hook path parameters decoded once, and refuses a path that does not decode', async () => {
    const app = new Hono();
    const fetchItem: OperationSpec = { ...stream, path: '/api/items/{id}', pathParams: [{ name: 'id', kind: 'string', required: true }], auth: { public: true, required: false, permissions: [] } };
    mountManualOperation(app, fetchItem, (c, ctx) => c.text(ctx.pathParams.id ?? ''));
    const decoded = await app.request(`/api/items/${encodeURIComponent('a%41%')}`, { method: 'POST' });
    expect(await decoded.text()).toBe('a%41%');
    const malformed = await app.request('/api/items/a%ZZ', { method: 'POST' });
    expect(malformed.status).toBe(400);
    expect(await malformed.json()).toMatchObject({ status: 400, code: 'bad_request', details: { location: 'path' } });
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

  function limitedApp(rateLimit?: RateLimitOptions) {
    const app = new Hono();
    const options = { rateLimit };
    mountOperation(app, limited, async () => ({ ok: true }), options);
    mountManualOperation(app, { ...limited, name: 'limitedManual', path: '/api/limited-manual', manual: true }, c => c.text('manual'), options);
    return app;
  }

  test('refuses the request past the per-minute budget with 429 and Retry-After; X-Forwarded-For chooses no bucket', async () => {
    const app = limitedApp();
    const from = (ip: string) => app.request('/api/limited', { headers: { 'x-forwarded-for': `${ip}, 10.0.0.1` } });
    expect((await from('203.0.113.7')).status).toBe(200);
    expect((await from('203.0.113.7')).status).toBe(200);
    const refused = await from('203.0.113.7');
    expect(refused.status).toBe(429);
    expect(refused.headers.get('content-type')).toBe('application/problem+json');
    expect(refused.headers.get('retry-after')).toMatch(/^[1-9]\d*$/u);
    expect(await refused.json()).toMatchObject({ status: 429, code: 'too_many_requests', title: 'Too Many Requests', details: { retryAfterSeconds: expect.any(Number) } });
    // The key is the peer address, which app.request does not report: a
    // client that names another hop still draws from the same bucket.
    expect((await from('203.0.113.8')).status).toBe(429);
  });

  test('the key is the transport peer address by default', async () => {
    const app = limitedApp();
    const from = (peer: string) => app.request('/api/limited', { headers: { 'x-forwarded-for': '203.0.113.7' } }, { incoming: { socket: { remoteAddress: peer } } });
    expect((await from('198.51.100.1')).status).toBe(200);
    expect((await from('198.51.100.1')).status).toBe(200);
    expect((await from('198.51.100.1')).status).toBe(429);
    expect((await from('198.51.100.2')).status).toBe(200);
  });

  test('behind a proxy the service trusts, clientIpKey keys by the client IP it reports', async () => {
    const app = limitedApp({ keyOf: clientIpKey });
    const from = (ip: string) => app.request('/api/limited', { headers: { 'x-forwarded-for': `${ip}, 10.0.0.1` } });
    expect((await from('203.0.113.7')).status).toBe(200);
    expect((await from('203.0.113.7')).status).toBe(200);
    expect((await from('203.0.113.7')).status).toBe(429);
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

describe('the order of the refusals', () => {
  // The Go router's order: the rate limit, then the body limit, then the
  // permission gate, so neither of the first two costs an authentication.
  test('the rate limit comes before the body limit, and both before the gate', async () => {
    const app = new Hono();
    const calls: string[] = [];
    const spec: OperationSpec = { ...createOrder, name: 'ordered', path: '/api/ordered', rateLimitPerMinute: 2 };
    mountOperation(app, spec, async () => ({ ok: true }), {
      authenticate: async ctx => {
        calls.push('authenticate');
        return principals[ctx.headers.get('x-user') ?? ''] ?? null;
      },
    });
    const send = (body: string, user?: string) =>
      app.request('/api/ordered', { method: 'POST', headers: { 'content-type': 'application/json', ...(user ? { 'x-user': user } : {}) }, body });
    const oversize = JSON.stringify({ name: 'x'.repeat(100) });
    expect((await send(oversize)).status).toBe(413);
    expect(calls).toEqual([]);
    expect((await send(JSON.stringify({ name: 'ok' }))).status).toBe(401);
    expect(calls).toEqual(['authenticate']);
    const limited = await send(oversize, 'writer');
    expect(limited.status).toBe(429);
    expect(calls).toEqual(['authenticate']);
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

  // The timeout wraps the handler, as the Go router's does: the gate runs
  // before it, so a slow authenticate is not cut.
  test('covers the handler and a manual route handler, not the gate', async () => {
    const app = new Hono();
    const gated = { ...slow, name: 'gated', path: '/api/gated', auth: { public: false, required: true, permissions: [] } };
    mountOperation(app, gated, async ctx => ({ caller: ctx.principal?.subject }), {
      authenticate: async () => {
        await new Promise(resolve => setTimeout(resolve, 200));
        return { subject: 'late', permissions: [] };
      },
    });
    mountManualOperation(app, { ...slow, name: 'slowManual', path: '/api/slow-manual', manual: true }, async c => {
      await new Promise(resolve => setTimeout(resolve, 200));
      return c.text('late');
    });
    const gatedResponse = await app.request('/api/gated');
    expect(gatedResponse.status).toBe(200);
    expect((await gatedResponse.json()).data).toEqual({ caller: 'late' });
    expect((await app.request('/api/slow-manual')).status).toBe(504);
  });
});

describe('@hmacVerified', () => {
  const secret = 'whsec_test';
  const signed: OperationSpec = { ...createOrder, name: 'receiveEvent', path: '/api/webhooks/shop', rateLimitPerMinute: 1, webhookProvider: 'shop' };
  const signedManual: OperationSpec = { ...stream, name: 'receiveRawEvent', path: '/api/webhooks/shop/raw', webhookProvider: 'shop' };

  function signatureOf(body: string): string {
    return createHmac('sha256', secret).update(body).digest('hex');
  }

  // verifyShop reads the body with c.req.text(), as a provider's SDK does,
  // and refuses a request whose signature header is not the body's HMAC.
  const verifyShop: WebhookVerifier = async (c, next) => {
    const body = await c.req.text();
    if (c.req.header('x-shop-signature') !== signatureOf(body)) {
      throw new HttpProblem(401, 'The webhook signature does not match', { code: 'invalid_signature' });
    }
    await next();
  };

  function webhookApp(verifier: WebhookVerifier = verifyShop) {
    const app = new Hono();
    const options = { authenticate: async (ctx: { headers: Headers }) => principals[ctx.headers.get('x-user') ?? ''] ?? null };
    mountOperation(app, signed, async (_ctx, request) => ({ received: request.input }), options, { webhookVerifier: verifier });
    mountManualOperation(app, signedManual, async c => c.text(`raw ${await c.req.text()}`), options, { webhookVerifier: verifier });
    return app;
  }

  function post(app: Hono, path: string, body: string, headers: Record<string, string> = {}) {
    return app.request(path, { method: 'POST', headers: { 'content-type': 'application/json', 'x-real-ip': '198.51.100.9', ...headers }, body });
  }

  test('an operation that names a provider is not mounted without a verifier', () => {
    const app = new Hono();
    expect(() => mountOperation(app, signed, async () => null)).toThrow("receiveEvent is @hmacVerified({ provider: 'shop' }) and was mounted without a webhook verifier");
    expect(() => mountManualOperation(app, signedManual, undefined)).toThrow("receiveRawEvent is @hmacVerified({ provider: 'shop' }) and was mounted without a webhook verifier");
  });

  test('a signed request reaches the implementation with the body the verifier read', async () => {
    const body = JSON.stringify({ name: 'paid' });
    const response = await post(webhookApp(), '/api/webhooks/shop', body, { 'x-shop-signature': signatureOf(body), 'x-user': 'writer' });
    expect(response.status).toBe(200);
    expect((await response.json()).data).toEqual({ received: { name: 'paid' } });
  });

  test('the verifier runs before the rate limit, the body limit and the gate', async () => {
    const app = webhookApp();
    const body = JSON.stringify({ name: 'paid' });
    const large = JSON.stringify({ name: 'x'.repeat(100) });
    const unsigned = async (sent: string, headers: Record<string, string> = {}) => {
      const response = await post(app, '/api/webhooks/shop', sent, { 'x-shop-signature': 'forged', ...headers });
      expect(response.status).toBe(401);
      expect(await response.json()).toMatchObject({ status: 401, code: 'invalid_signature' });
    };
    // Past the 64-byte body limit and without a caller: the signature is refused first.
    await unsigned(large);
    await unsigned(body);
    // A refused signature took no rate-limit token.
    expect((await post(app, '/api/webhooks/shop', body, { 'x-shop-signature': signatureOf(body), 'x-user': 'writer' })).status).toBe(200);
    expect((await post(app, '/api/webhooks/shop', body, { 'x-shop-signature': signatureOf(body), 'x-user': 'writer' })).status).toBe(429);
    await unsigned(body, { 'x-user': 'writer' });

    // Then the Go router's order: the rate limit, the body limit, the gate.
    // A request over the body limit has taken the minute's token.
    const fresh = webhookApp();
    expect((await post(fresh, '/api/webhooks/shop', large, { 'x-shop-signature': signatureOf(large), 'x-user': 'writer' })).status).toBe(413);
    expect((await post(fresh, '/api/webhooks/shop', body, { 'x-shop-signature': signatureOf(body), 'x-user': 'writer' })).status).toBe(429);
    const anonymous = await post(webhookApp(), '/api/webhooks/shop', body, { 'x-shop-signature': signatureOf(body) });
    expect(anonymous.status).toBe(401);
    expect(await anonymous.json()).toMatchObject({ code: 'unauthorized' });
  });

  test('a manual route is verified before the service handler, which can still read the body', async () => {
    const app = webhookApp();
    const body = '{"raw":true}';
    const accepted = await post(app, '/api/webhooks/shop/raw', body, { 'x-shop-signature': signatureOf(body), 'x-user': 'writer' });
    expect(accepted.status).toBe(200);
    expect(await accepted.text()).toBe(`raw ${body}`);
    const refused = await post(app, '/api/webhooks/shop/raw', body, { 'x-shop-signature': 'forged', 'x-user': 'writer' });
    expect(refused.status).toBe(401);
    expect(await refused.json()).toMatchObject({ code: 'invalid_signature' });
  });

  test("a verifier's own response is the route's answer", async () => {
    let called = false;
    const app = webhookApp(async c => {
      called = true;
      return c.text('go away', 403);
    });
    const response = await post(app, '/api/webhooks/shop', '{"name":"paid"}', { 'x-user': 'writer' });
    expect(called).toBe(true);
    expect(response.status).toBe(403);
    expect(await response.text()).toBe('go away');
  });
});
