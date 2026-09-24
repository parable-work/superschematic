/*
Runtime check of a generated router (run by TestGeneratedRouterRuntime with
API_DIR set to the materialized fixture-api package). Stub implementations echo
their decoded arguments; the assertions cover what the router owns: the two
envelopes, the strict body parser, the auth gate, parameter decoding, and the
manual-route hook.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { HttpProblem, OperationResult } from '@superschematic/http-runtime';
import { parseIdentityUUID } from 'superscalar/scalars';

function canonicalUUID(raw: string): string {
  const parsed = parseIdentityUUID(raw);
  if (parsed === null) throw new Error(`not a UUID: ${raw}`);
  return parsed;
}

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter, operationSpecs } = await import(`${apiDir}/router.ts`);

const principals: Record<string, { subject: string; permissions: string[] }> = {
  reader: { subject: 'reader', permissions: ['tenants.read'] },
  // `tenants` covers tenants.read and tenants.write: a granted permission
  // covers the permissions nested under it.
  writer: { subject: 'writer', permissions: ['tenants'] },
};

const tenant = { id: '00000000-0000-4000-8000-000000000001', name: 'Acme', userCount: 1, internalDebugLabel: 'x' };

function app(
  manual?: (c: { text: (s: string) => Response }, ctx: { requestId: string; principal: unknown }) => Response,
  overrides: { rateLimits?: Record<string, number>; timeouts?: Record<string, number>; slowGet?: boolean; frozenClock?: boolean } = {}
) {
  const root = new Hono();
  const implementations = {
    session: {
      currentTenant: async (_args: unknown, ctx: { principal: { subject: string } | null }) => ({ ...tenant, name: ctx.principal?.subject ?? 'nobody' }),
    },
    tenant: {
      listTenants: async (args: unknown) => new OperationResult([{ ...tenant, name: JSON.stringify(args) }], 200, { 'x-args': JSON.stringify(args) }),
      createTenant: async (args: { input: { name: string } }) => ({ ...tenant, name: args.input.name }),
      getTenant: async (args: { id: string; includeArchived: boolean }, ctx: { signal: AbortSignal }) => {
        if (overrides.slowGet) await new Promise(resolve => ctx.signal.addEventListener('abort', resolve, { once: true }));
        if (args.includeArchived) throw new HttpProblem(410, 'archived', { code: 'gone', details: { id: args.id } });
        return { ...tenant, id: args.id };
      },
      updateSecret: async (args: { id: string; secret: string }) => ({ ...tenant, id: args.id, name: args.secret }),
    },
  };
  root.route(
    '/',
    buildRouter(implementations, {
      authenticate: async (ctx: { headers: Headers }) => principals[ctx.headers.get('x-user') ?? ''] ?? null,
      manualRoutes: manual ? { customHandler: manual } : undefined,
      rateLimits: overrides.rateLimits,
      timeouts: overrides.timeouts,
      // A frozen clock keeps the bucket from refilling mid-test.
      rateLimit: overrides.frozenClock ? { now: () => 0 } : undefined,
    })
  );
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

describe('generated fixture-api router', () => {
  test('operation table names every schema operation', () => {
    expect(Object.keys(operationSpecs).sort()).toEqual(['createTenant', 'currentTenant', 'customHandler', 'getTenant', 'listTenants', 'updateSecret']);
    expect(operationSpecs.customHandler.manual).toBe(true);
    expect(operationSpecs.getTenant.path).toBe('/api/tenants/{id}');
  });

  test('operation table carries @rateLimit and @timeout as the Go router resolves them', () => {
    expect(operationSpecs.getTenant.rateLimitPerMinute).toBe(60);
    expect(operationSpecs.getTenant.timeoutSeconds).toBe(5);
    expect(operationSpecs.listTenants.rateLimitPerMinute).toBe(60);
    expect(operationSpecs.listTenants.timeoutSeconds).toBeUndefined();
    expect(operationSpecs.createTenant.rateLimitPerMinute).toBeUndefined();
    expect(operationSpecs.createTenant.timeoutSeconds).toBeUndefined();
  });

  test('@rateLimit refuses the 61st request in a minute from one client with 429', async () => {
    const router = app(undefined, { frozenClock: true });
    const id = '00000000-0000-4000-8000-000000000001';
    const headers = { 'x-user': 'reader', 'x-forwarded-for': '203.0.113.9' };
    for (let i = 0; i < 60; i += 1) {
      expect((await router.request(`/api/tenants/${id}?includeArchived=false`, { headers })).status).toBe(200);
    }
    const refused = await router.request(`/api/tenants/${id}?includeArchived=false`, { headers });
    expect(refused.status).toBe(429);
    expect(refused.headers.get('retry-after')).toBe('1');
    expect(await refused.json()).toMatchObject({ status: 429, code: 'too_many_requests' });
    const other = await router.request(`/api/tenants/${id}?includeArchived=false`, { headers: { ...headers, 'x-forwarded-for': '203.0.113.10' } });
    expect(other.status).toBe(200);
    const relaxed = app(undefined, { rateLimits: { getTenant: 0 } });
    for (let i = 0; i < 61; i += 1) {
      expect((await relaxed.request(`/api/tenants/${id}?includeArchived=false`, { headers })).status).toBe(200);
    }
  });

  test('@timeout answers 504 when the implementation outlives it, honouring a router override', async () => {
    const id = '00000000-0000-4000-8000-000000000001';
    const slow = app(undefined, { slowGet: true, timeouts: { getTenant: 0.05 } });
    const response = await slow.request(`/api/tenants/${id}?includeArchived=false`, { headers: { 'x-user': 'reader' } });
    expect(response.status).toBe(504);
    expect(await response.json()).toMatchObject({ status: 504, code: 'gateway_timeout', title: 'Gateway Timeout' });
  });

  test('success envelope carries data and meta.requestId', async () => {
    const response = await app().request('/api/auth/me', { headers: { 'x-user': 'reader', 'x-request-id': 'req-9' } });
    expect(response.status).toBe(200);
    expect(response.headers.get('content-type')).toBe('application/json');
    expect(await response.json()).toEqual({ data: { ...tenant, name: 'reader' }, meta: { requestId: 'req-9' } });
  });

  test('auth gate: 401 without a principal, 403 without the permission', async () => {
    const anonymous = await app().request('/api/auth/me');
    expect(anonymous.status).toBe(401);
    expect(anonymous.headers.get('content-type')).toBe('application/problem+json');
    expect(await anonymous.json()).toMatchObject({ type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthorized' });
    const forbidden = await app().request('/api/tenants', { method: 'POST', headers: { 'x-user': 'reader', 'content-type': 'application/json' }, body: '{"name":"Acme","slug":"acme"}' });
    expect(forbidden.status).toBe(403);
    expect(await forbidden.json()).toMatchObject({ status: 403, code: 'forbidden', title: 'Forbidden' });
  });

  test('bodies go through the generated strict parser: 400 on an invalid body, 200 on a valid one', async () => {
    const headers = { 'x-user': 'writer', 'content-type': 'application/json' };
    // CreateTenantInput is not @strictJSON, so the fixture parser refuses a
    // wrong type and a missing required field; unknown-field refusal is the
    // strict contracts' behaviour and is covered by their own decoders.
    const wrongType = await app().request('/api/tenants', { method: 'POST', headers, body: '{"name":1}' });
    expect(wrongType.status).toBe(400);
    expect(await wrongType.json()).toMatchObject({ status: 400, code: 'bad_request' });
    const missingField = await app().request('/api/tenants', { method: 'POST', headers, body: '{"name":"Acme"}' });
    expect(missingField.status).toBe(400);
    const notJson = await app().request('/api/tenants', { method: 'POST', headers, body: '{nope' });
    expect(notJson.status).toBe(400);
    const created = await app().request('/api/tenants', { method: 'POST', headers, body: '{"name":"Acme","slug":"acme"}' });
    expect(created.status).toBe(200);
    expect((await created.json()).data).toMatchObject({ name: 'Acme' });
  });

  test('path and query parameters are decoded to the declared kinds', async () => {
    const id = '00000000-0000-4000-8000-0000000000AB';
    const canonical = canonicalUUID(id);
    const found = await app().request(`/api/tenants/${id}?includeArchived=false`, { headers: { 'x-user': 'reader' } });
    expect(found.status).toBe(200);
    expect((await found.json()).data.id).toBe(canonical);
    const archived = await app().request(`/api/tenants/${id}?includeArchived=true`, { headers: { 'x-user': 'reader' } });
    expect(archived.status).toBe(410);
    expect(await archived.json()).toMatchObject({ status: 410, code: 'gone', details: { id: canonical } });
    const missingQuery = await app().request(`/api/tenants/${id}`, { headers: { 'x-user': 'reader' } });
    expect(missingQuery.status).toBe(400);
    expect(await missingQuery.json()).toMatchObject({ details: { location: 'query', parameter: 'includeArchived' } });
    const badPath = await app().request('/api/tenants/not-a-uuid?includeArchived=true', { headers: { 'x-user': 'reader' } });
    expect(badPath.status).toBe(400);
  });

  test('array query parameters accept repeated keys and comma lists, enums are checked', async () => {
    const a = '00000000-0000-4000-8000-000000000001';
    const b = '00000000-0000-4000-8000-000000000002';
    const list = await app().request(`/api/tenants?ids=${a}&ids=${b}&statuses=active,suspended`, { headers: { 'x-user': 'reader' } });
    expect(list.status).toBe(200);
    expect(JSON.parse(list.headers.get('x-args') ?? '{}')).toEqual({
      ids: [canonicalUUID(a), canonicalUUID(b)],
      statuses: ['active', 'suspended'],
    });
    const badEnum = await app().request(`/api/tenants?ids=${a}&statuses=deleted`, { headers: { 'x-user': 'reader' } });
    expect(badEnum.status).toBe(400);
    const tooMany = await app().request(`/api/tenants?ids=${Array(101).fill(a).join(',')}`, { headers: { 'x-user': 'reader' } });
    expect(tooMany.status).toBe(400);
  });

  test('scalar body arguments of a non-GET operation are read from the JSON object', async () => {
    const id = '00000000-0000-4000-8000-000000000001';
    const response = await app().request(`/api/tenants/${id}`, { method: 'PATCH', headers: { 'x-user': 'writer', 'content-type': 'application/json' }, body: '{"secret":"s3"}' });
    expect(response.status).toBe(200);
    expect((await response.json()).data).toMatchObject({ id: canonicalUUID(id), name: 's3' });
    const missing = await app().request(`/api/tenants/${id}`, { method: 'PATCH', headers: { 'x-user': 'writer', 'content-type': 'application/json' }, body: '{}' });
    expect(missing.status).toBe(400);
  });

  test('manual route hook receives the Hono context; 501 without one', async () => {
    const hooked = await app((c, ctx) => c.text(`manual ${ctx.requestId}`)).request('/api/tenant/custom-handler', { method: 'POST', headers: { 'x-request-id': 'm-1' } });
    expect(hooked.status).toBe(200);
    expect(await hooked.text()).toBe('manual m-1');
    const missing = await app().request('/api/tenant/custom-handler', { method: 'POST' });
    expect(missing.status).toBe(501);
    expect(await missing.json()).toMatchObject({ status: 501, code: 'not_implemented' });
  });

  test('unknown paths answer the problem envelope', async () => {
    const response = await app().request('/api/nothing');
    expect(response.status).toBe(404);
    expect(await response.json()).toMatchObject({ status: 404, code: 'not_found', type: 'about:blank' });
  });
});
