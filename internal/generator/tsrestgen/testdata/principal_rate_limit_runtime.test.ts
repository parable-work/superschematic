/*
Runtime check of the generated router for fixture-service-auth-api with
getReservation limited to two requests a minute per principal and
syncStatus to one per client (run by TestGeneratedPrincipalRateLimitRouter
with API_DIR set to the materialized package, D131). The principal route
counts each caller the permission gate admits, after the gate, under its
method and path; it answers 429 with Retry-After past the limit and 503
when the store fails. The ip route counts per client and never in the
principal store.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import type { PrincipalRateLimitStore } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

const reservation = { id: 'r1', sku: 'sku-1', held: true };
const run = { id: 'run', done: true };
const implementations = {
  ledger: { listReservations: async () => [] },
  stock: {
    reserveStock: async () => reservation,
    releaseReservation: async () => reservation,
    reindexStock: async () => run,
    getReservation: async () => reservation,
  },
  sync: { syncStock: async () => run, syncMyStock: async () => run, syncStatus: async () => run },
};

class Counts implements PrincipalRateLimitStore {
  readonly counts = new Map<string, number>();
  fail = false;
  async take(bucket: string, principal: string, limit: number) {
    if (this.fail) throw new Error('store down');
    const key = `${bucket} ${principal}`;
    const count = (this.counts.get(key) ?? 0) + 1;
    this.counts.set(key, count);
    return { allowed: count <= limit, retryAfterSeconds: 30 };
  }
}

function app(store: Counts) {
  const root = new Hono();
  root.route(
    '/',
    buildRouter(implementations, {
      authenticate: async (ctx: { bearerToken?: string }) => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: [] } : null),
      principalRateLimitStore: store,
    })
  );
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

const get = (root: Hono, path: string, user?: string) => root.request(path, { headers: user ? { authorization: `Bearer ${user}` } : {} });

describe('a principal @rateLimit (D131)', () => {
  test('counts each caller the permission gate admits, after it', async () => {
    const store = new Counts();
    const root = app(store);
    expect((await get(root, '/api/stock/reservations/r1', 'ana')).status).toBe(200);
    expect((await get(root, '/api/stock/reservations/r1', 'ana')).status).toBe(200);
    const refused = await get(root, '/api/stock/reservations/r1', 'ana');
    expect(refused.status).toBe(429);
    expect(refused.headers.get('retry-after')).toBe('30');
    expect(((await refused.json()) as { code: string }).code).toBe('too_many_requests');
    expect((await get(root, '/api/stock/reservations/r1', 'ben')).status).toBe(200);

    expect((await get(root, '/api/stock/reservations/r1')).status).toBe(401);
    expect([...store.counts.keys()].sort()).toEqual(['GET /api/stock/reservations/{id} ana', 'GET /api/stock/reservations/{id} ben']);

    store.fail = true;
    const down = await get(root, '/api/stock/reservations/r1', 'ben');
    expect(down.status).toBe(503);
    expect(((await down.json()) as { code: string }).code).toBe('rate_limit_unavailable');
  });

  test('the ip route counts per client, not in the principal store', async () => {
    const store = new Counts();
    const root = app(store);
    expect((await get(root, '/api/sync/status')).status).toBe(200);
    expect((await get(root, '/api/sync/status')).status).toBe(429);
    expect(store.counts.size).toBe(0);
  });
});
