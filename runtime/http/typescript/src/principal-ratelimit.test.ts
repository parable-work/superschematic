import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { type OperationSpec, type PrincipalRateLimitStore } from './index';
import { mountOperation } from './hono';

// A route whose @rateLimit counts per principal (D131): two requests a
// minute for each caller the permission gate admits.
const getOrder: OperationSpec = {
  name: 'getOrder',
  namespace: 'order',
  method: 'GET',
  path: '/api/orders/{id}',
  pathParams: [{ name: 'id', kind: 'string', required: true }],
  queryParams: [],
  bodyParams: [],
  auth: { public: false, required: true, permissions: ['orders.read'] },
  rateLimitPerMinute: 2,
  rateLimitKey: 'principal',
  manual: false,
};

/** Counts per (bucket, principal); refuses past the limit with a 30.5 s retry, or fails. */
class Counts implements PrincipalRateLimitStore {
  readonly counts = new Map<string, number>();
  fail = false;

  async take(bucket: string, principal: string, limit: number, windowMs: number) {
    if (this.fail) throw new Error('store down');
    expect(windowMs).toBe(60_000);
    const key = `${bucket} ${principal}`;
    const count = (this.counts.get(key) ?? 0) + 1;
    this.counts.set(key, count);
    return { allowed: count <= limit, retryAfterSeconds: 30.5 };
  }
}

function limitedApp(store: Counts) {
  const app = new Hono();
  mountOperation(app, getOrder, async (_ctx, request) => ({ id: request.path.id }), {
    principalRateLimitStore: store,
    // x-user names the caller; x-user "nobody" is a caller with no subject.
    authenticate: async ctx => {
      const user = ctx.headers.get('x-user');
      if (!user) return null;
      return { subject: user === 'nobody' ? '' : user, permissions: ['orders.read'] };
    },
  });
  return app;
}

describe('a principal @rateLimit (D131)', () => {
  test('counts each caller the permission gate admits, after it', async () => {
    const store = new Counts();
    const app = limitedApp(store);
    const as = (user?: string) => app.request('/api/orders/o1', { headers: user ? { 'x-user': user } : {} });
    expect((await as('ana')).status).toBe(200);
    expect((await as('ana')).status).toBe(200);
    const refused = await as('ana');
    expect(refused.status).toBe(429);
    expect(refused.headers.get('retry-after')).toBe('31');
    expect(await refused.json()).toMatchObject({ code: 'too_many_requests', detail: 'This principal exceeded 2 requests per minute' });
    expect((await as('ben')).status).toBe(200);

    expect((await as()).status).toBe(401);
    expect([...store.counts.keys()].sort()).toEqual(['GET /api/orders/{id} ana', 'GET /api/orders/{id} ben']);

    const nobody = await as('nobody');
    expect(nobody.status).toBe(400);
    expect(await nobody.json()).toMatchObject({ code: 'rate_limit_principal_missing' });

    store.fail = true;
    const down = await as('ben');
    expect(down.status).toBe(503);
    expect(await down.json()).toMatchObject({ code: 'rate_limit_unavailable' });
  });

  test('mounting the route without a store throws', () => {
    expect(() => mountOperation(new Hono(), getOrder, async () => ({}))).toThrow('principalRateLimitStore is required');
  });
});
