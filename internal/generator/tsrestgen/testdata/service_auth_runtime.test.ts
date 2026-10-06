/*
Runtime check of the generated router for fixture-service-auth-api (run by
TestGeneratedServiceAuthRouter with API_DIR set to the materialized
package). Callers sign Ed25519 tokens with the runtime's signedTokenSource,
and the router verifies them with its serviceAuthenticator, as a local
stack does (D37). The assertions cover each rule of section 9.3 of
docs/stack-model.md as the operation table states it: @requireService
refuses a missing caller with 401 service_unauthorized and an unlisted one
with 403 service_forbidden; @allowService admits a listed caller without
calling authenticate and sends anyone else through the user clause; a set's
clause applies unless the operation replaces it or is @publicRoute; the
service step runs after the rate limit and the body limit and before the
end-user step; and without authenticateService a route with a service
clause answers 401.
*/
import { beforeAll, describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { serviceAuthenticator, signedTokenSource, type ServiceAuthConfig, type ServiceAuthenticator } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

interface Seen {
  service: string | null;
  user: string | null;
}

interface Context {
  serviceCaller: { deployable: string } | null;
  principal: { subject: string } | null;
}

const seen = (ctx: Context): Seen => ({ service: ctx.serviceCaller?.deployable ?? null, user: ctx.principal?.subject ?? null });

// Every implementation answers with what it saw of the callers.
const implementations = {
  ledger: { listReservations: async (_args: unknown, ctx: Context) => [{ id: 'r1', sku: 'sku-1', held: true, ...seen(ctx) }] },
  stock: {
    reserveStock: async (args: { sku: string }, ctx: Context) => ({ id: 'r1', sku: args.sku, held: true, ...seen(ctx) }),
    releaseReservation: async (args: { id: string }, ctx: Context) => ({ id: args.id, sku: 'sku-1', held: false, ...seen(ctx) }),
    reindexStock: async (_args: unknown, ctx: Context) => ({ id: 'run-1', done: true, ...seen(ctx) }),
    getReservation: async (args: { id: string }, ctx: Context) => ({ id: args.id, sku: 'sku-1', held: true, ...seen(ctx) }),
  },
  sync: {
    syncStock: async (_args: unknown, ctx: Context) => ({ id: 'run-2', done: true, ...seen(ctx) }),
    syncMyStock: async (_args: unknown, ctx: Context) => ({ id: 'run-3', done: true, ...seen(ctx) }),
    syncStatus: async (_args: unknown, ctx: Context) => ({ id: 'run-4', done: true, ...seen(ctx) }),
  },
};

// The end users, by bearer token.
const users: Record<string, { subject: string; permissions: string[] }> = {
  'alice-token': { subject: 'alice', permissions: ['stock.reserve', 'stock.write'] },
  'bob-token': { subject: 'bob', permissions: [] },
};

const now = () => 1_767_225_600_000;
const tokens: Record<string, (fresh: boolean) => Promise<string>> = {};
let config: ServiceAuthConfig;

beforeAll(async () => {
  const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair;
  const jwk = await crypto.subtle.exportKey('jwk', pair.privateKey);
  const key = { kty: 'OKP', crv: 'Ed25519', d: jwk.d!, x: jwk.x!, kid: 'edge-1' } as const;
  for (const caller of ['orders', 'billing', 'stranger']) {
    tokens[caller] = signedTokenSource(key, { issuer: 'local', subject: caller, audience: 'fixture-service-auth-api' }, { now });
  }
  config = {
    issuers: [
      {
        issuer: 'local',
        audience: 'fixture-service-auth-api',
        algorithms: ['EdDSA'],
        keys: [{ kty: 'OKP', crv: 'Ed25519', x: key.x, kid: key.kid }],
        maxLifetimeSeconds: 300,
        callers: {
          orders: { deployable: 'orders', serves: ['fixture-service-caller-api'] },
          billing: { deployable: 'billing', serves: ['fixture-billing-api'] },
        },
      },
    ],
  };
});

function app(options: Record<string, unknown> = {}) {
  const authenticated: string[] = [];
  const root = new Hono();
  root.route(
    '/',
    buildRouter(implementations, {
      authenticate: async (ctx: { operation: { name: string }; bearerToken?: string }) => {
        authenticated.push(ctx.operation.name);
        return (ctx.bearerToken && users[ctx.bearerToken]) || null;
      },
      authenticateService: serviceAuthenticator(config, { now }),
      ...options,
    })
  );
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return { root, authenticated };
}

/** The headers of a request from `service` (a caller's name, or a raw Service-Authorization value) forwarding `user`'s token. */
async function credentials(service?: string, user?: string): Promise<Record<string, string>> {
  const headers: Record<string, string> = { 'content-type': 'application/json' };
  if (service) headers['service-authorization'] = tokens[service] ? `Bearer ${await tokens[service]!(false)}` : service;
  if (user) headers.authorization = `Bearer ${user}`;
  return headers;
}

async function call(root: Hono, method: string, path: string, headers: Record<string, string>, body?: string) {
  const response = await root.request(path, { method, headers, ...(body !== undefined ? { body } : {}) });
  return { status: response.status, body: (await response.json()) as { data?: Seen | Seen[]; code?: string; detail?: string } };
}

const reserve = async (root: Hono, service?: string, user?: string) => call(root, 'POST', '/api/stock/reservations', await credentials(service, user), '{"sku":"sku-1"}');
const release = async (root: Hono, service?: string, user?: string) => call(root, 'POST', '/api/stock/reservations/r1/release', await credentials(service, user));
const reindex = async (root: Hono, service?: string, user?: string) => call(root, 'POST', '/api/stock/reindex', await credentials(service, user));

describe('generated fixture-service-auth-api router', () => {
  test('@requireService alone: any listed caller and no end user; a missing caller is 401, an unknown one 403', async () => {
    const { root, authenticated } = app();
    expect(await reindex(root, 'billing')).toMatchObject({ status: 200, body: { data: { service: 'billing', user: null } } });
    expect(await reindex(root, undefined, 'alice-token')).toMatchObject({
      status: 401,
      body: { code: 'service_unauthorized', detail: 'Service credential required' },
    });
    expect(await reindex(root, 'stranger')).toMatchObject({ status: 403, body: { code: 'service_forbidden', detail: 'Service not permitted' } });
    expect(await reindex(root, 'Bearer not.a.token')).toMatchObject({ status: 401, body: { code: 'service_unauthorized', detail: 'Invalid service credential' } });
    expect(authenticated).toEqual([]);
  });

  test('@requireService with a user clause: a listed caller forwarding a user who may', async () => {
    const { root } = app();
    expect(await reserve(root, 'orders', 'alice-token')).toMatchObject({ status: 200, body: { data: { service: 'orders', user: 'alice' } } });
    expect(await reserve(root, 'billing', 'alice-token')).toMatchObject({ status: 403, body: { code: 'service_forbidden' } });
    expect(await reserve(root, undefined, 'alice-token')).toMatchObject({ status: 401, body: { code: 'service_unauthorized' } });
    expect(await reserve(root, 'orders')).toMatchObject({ status: 401, body: { code: 'unauthorized' } });
    expect(await reserve(root, 'orders', 'bob-token')).toMatchObject({ status: 403, body: { code: 'forbidden' } });
  });

  test('@allowService: a listed caller stands in for the user, and authenticate is not called', async () => {
    const { root, authenticated } = app();
    expect(await release(root, 'orders')).toMatchObject({ status: 200, body: { data: { service: 'orders', user: null } } });
    // Not even a malformed Authorization header reaches the end-user step.
    expect(await release(root, 'orders', 'not a token')).toMatchObject({ status: 200, body: { data: { service: 'orders', user: null } } });
    expect(authenticated).toEqual([]);
  });

  test('@allowService: anyone else goes through the user clause', async () => {
    const { root, authenticated } = app();
    expect(await release(root, 'billing')).toMatchObject({ status: 401, body: { code: 'unauthorized' } });
    expect(await release(root, 'billing', 'alice-token')).toMatchObject({ status: 200, body: { data: { service: 'billing', user: 'alice' } } });
    expect(await release(root, undefined, 'alice-token')).toMatchObject({ status: 200, body: { data: { service: null, user: 'alice' } } });
    expect(await release(root, undefined, 'bob-token')).toMatchObject({ status: 403, body: { code: 'forbidden' } });
    expect(await release(root, 'Bearer not.a.token', 'alice-token')).toMatchObject({ status: 401, body: { code: 'service_unauthorized' } });
    expect(authenticated).toEqual(['releaseReservation', 'releaseReservation', 'releaseReservation', 'releaseReservation']);
  });

  test("a set's clause applies unless the operation replaces it or is @publicRoute", async () => {
    const { root } = app();
    const post = async (path: string, service?: string, user?: string) => call(root, 'POST', path, await credentials(service, user));
    expect(await post('/api/sync/stock', 'orders')).toMatchObject({ status: 200, body: { data: { service: 'orders' } } });
    expect(await post('/api/sync/stock', 'billing')).toMatchObject({ status: 403, body: { code: 'service_forbidden' } });
    expect(await post('/api/sync/stock/mine', 'billing')).toMatchObject({ status: 200, body: { data: { service: 'billing', user: null } } });
    expect(await call(root, 'GET', '/api/sync/status', {})).toMatchObject({ status: 200, body: { data: { service: null, user: null } } });
    const ledger = await call(root, 'GET', '/api/ledger/reservations', await credentials('orders'));
    expect(ledger).toMatchObject({ status: 200, body: { data: [{ service: 'orders', user: null }] } });
    expect(await call(root, 'GET', '/api/ledger/reservations', await credentials('billing'))).toMatchObject({ status: 401, body: { code: 'unauthorized' } });
  });

  test('a route without a service clause reports a valid caller and refuses an invalid credential', async () => {
    const { root } = app();
    const get = async (service?: string, user?: string) => call(root, 'GET', '/api/stock/reservations/r1', await credentials(service, user));
    expect(await get('orders', 'bob-token')).toMatchObject({ status: 200, body: { data: { service: 'orders', user: 'bob' } } });
    expect(await get(undefined, 'bob-token')).toMatchObject({ status: 200, body: { data: { service: null, user: 'bob' } } });
    expect(await get('Bearer not.a.token', 'bob-token')).toMatchObject({ status: 401, body: { code: 'service_unauthorized' } });
  });

  test('the service step runs after the rate limit and the body limit, and before the end-user step', async () => {
    const services: string[] = [];
    const verify = serviceAuthenticator(config, { now });
    const counted: ServiceAuthenticator = async ctx => {
      services.push(ctx.operation.name);
      return verify(ctx);
    };
    const { root, authenticated } = app({ authenticateService: counted, rateLimits: { reserveStock: 2 }, bodyLimits: { reserveStock: 16 } });
    const large = JSON.stringify({ sku: 'x'.repeat(32) });
    // Over the body cap: the rate limit takes a token, the body limit refuses, the service step does not run.
    const oversize = await call(root, 'POST', '/api/stock/reservations', { ...(await credentials('orders', 'alice-token')), 'content-length': String(large.length) }, large);
    expect(oversize).toMatchObject({ status: 413, body: { code: 'payload_too_large' } });
    expect(services).toEqual([]);
    // A refused service credential: the end user is not authenticated.
    expect(await reserve(root, 'Bearer not.a.token', 'alice-token')).toMatchObject({ status: 401, body: { code: 'service_unauthorized' } });
    expect(services).toEqual(['reserveStock']);
    expect(authenticated).toEqual([]);
    // Both tokens are gone: the rate limit refuses before the service step.
    expect(await reserve(root, 'orders', 'alice-token')).toMatchObject({ status: 429, body: { code: 'too_many_requests' } });
    expect(services).toEqual(['reserveStock']);
  });

  test('without authenticateService a route with a service clause answers 401, and other routes ignore the header', async () => {
    const { root, authenticated } = app({ authenticateService: undefined });
    for (const answer of [await reindex(root, 'orders'), await reserve(root, 'orders', 'alice-token'), await release(root, 'orders', 'alice-token')]) {
      expect(answer).toMatchObject({ status: 401, body: { code: 'service_unauthorized', detail: 'Service credential required' } });
    }
    expect(authenticated).toEqual([]);
    const get = await call(root, 'GET', '/api/stock/reservations/r1', await credentials('Bearer not.a.token', 'bob-token'));
    expect(get).toMatchObject({ status: 200, body: { data: { service: null, user: 'bob' } } });
    expect(await call(root, 'GET', '/api/sync/status', await credentials('orders'))).toMatchObject({ status: 200 });
  });
});
