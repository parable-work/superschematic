import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { loadBucket, loadDatabase, loadService } from './stackconfig';
import {
  WorkersBindingError,
  binding,
  loadSecrets,
  perIsolate,
  requestIP,
  serviceFetch,
  stackEnvOf,
  stackEnvWithSecrets,
  type WorkersEnv,
} from './workers';

/*
The Workers entry's half of the runtime (D64, section 18.5): a Worker's env
read as the config loaders read a process's, its bindings taken by the
names the config gives, its secrets awaited once per isolate, the client's
address from CF-Connecting-IP, and values built once per isolate.
*/

const d1 = { prepare() {}, batch() {}, exec() {} };
const r2 = { put() {}, get() {}, head() {}, delete() {}, list() {} };

function env(extra: Record<string, unknown> = {}): WorkersEnv {
  return {
    SHOP_DB_DATABASE_D1_BINDING: 'SHOP_DB_DATABASE',
    SHOP_DB_DATABASE_D1_DATABASE: '1c3e5a7b-9d2f-4b6a-8c0e-2f4a6b8c0d1e',
    SHOP_DB_DATABASE: d1,
    SHOP_MEDIA_BUCKET_NAME: 'shop-stack-shop-media',
    SHOP_MEDIA_BUCKET_BINDING: 'SHOP_MEDIA_BUCKET',
    SHOP_MEDIA_BUCKET: r2,
    SHOP_API_SERVICE_URL: 'https://shop-api',
    SHOP_API_SERVICE_BINDING_NAME: 'SHOP_API_SERVICE',
    SHOP_API_SERVICE_BINDING_WORKER: 'shop-api',
    SHOP_API_SERVICE: { fetch: async (input: RequestInfo | URL) => new Response(`bound ${new Request(input).url}`) },
    ...extra,
  };
}

describe('stackEnvOf', () => {
  test('keeps the string bindings the config loaders read', () => {
    const vars = stackEnvOf(env({ RETRIES: '3', FLAG: true, NOTHING: undefined }));
    expect(vars.RETRIES).toBe('3');
    expect('SHOP_DB_DATABASE' in vars).toBe(false);
    expect('FLAG' in vars).toBe(false);
    expect(loadDatabase('SHOP_DB_DATABASE', vars)).toEqual({ d1: { binding: 'SHOP_DB_DATABASE', database: '1c3e5a7b-9d2f-4b6a-8c0e-2f4a6b8c0d1e' } });
    expect(loadBucket('SHOP_MEDIA_BUCKET', vars)).toEqual({ name: 'shop-stack-shop-media', binding: 'SHOP_MEDIA_BUCKET' });
    expect(loadService('SHOP_API_SERVICE', vars)).toEqual({ url: 'https://shop-api', binding: { name: 'SHOP_API_SERVICE', worker: 'shop-api' } });
  });
});

describe('binding', () => {
  test('takes each binding by the name its config gives', async () => {
    const e = env();
    expect(binding(e, 'SHOP_DB_DATABASE', 'd1')).toBe(d1 as never);
    expect(binding(e, 'SHOP_MEDIA_BUCKET', 'r2')).toBe(r2 as never);
    const call = serviceFetch(binding(e, 'SHOP_API_SERVICE', 'service'));
    expect(await (await call('https://shop-api/api/products')).text()).toBe('bound https://shop-api/api/products');
  });

  test('refuses one that is missing or of another kind, naming it', () => {
    expect(() => binding(env(), 'SHOP_DB_DATABASE', 'r2')).toThrow("the Worker's binding SHOP_DB_DATABASE is not an R2 bucket");
    expect(() => binding(env(), 'SHOP_DB_DATABASE_D1_BINDING', 'd1')).toThrow('is not a D1 database');
    expect(() => binding(env(), 'ORDERS_DB', 'd1')).toThrow('the Worker has no binding ORDERS_DB, which its config names as a D1 database');
    expect(() => binding(env(), 'SHOP_API_SERVICE', 'secret')).toThrow(WorkersBindingError);
  });
});

describe('loadSecrets', () => {
  test('awaits each secret once per isolate', async () => {
    let reads = 0;
    const e = env({ STRIPE_KEY: { get: async () => `sk_${++reads}` } });
    expect(await loadSecrets(e, ['STRIPE_KEY'])).toEqual({ STRIPE_KEY: 'sk_1' });
    expect(await loadSecrets(e, ['STRIPE_KEY'])).toEqual({ STRIPE_KEY: 'sk_1' });
    const vars = await stackEnvWithSecrets(e, ['STRIPE_KEY']);
    expect(vars.STRIPE_KEY).toBe('sk_1');
    expect(vars.SHOP_API_SERVICE_URL).toBe('https://shop-api');
    expect(reads).toBe(1);
    // Another isolate's env reads its own.
    expect(await loadSecrets(env({ STRIPE_KEY: { get: async () => 'other' } }), ['STRIPE_KEY'])).toEqual({ STRIPE_KEY: 'other' });
  });

  test('reads a secret again after its get fails', async () => {
    let reads = 0;
    const e = env({
      STRIPE_KEY: {
        get: async () => {
          if (++reads === 1) throw new Error('the store is unavailable');
          return 'sk_live';
        },
      },
    });
    await expect(loadSecrets(e, ['STRIPE_KEY'])).rejects.toThrow('the Secrets Store secret STRIPE_KEY: the store is unavailable');
    expect(await loadSecrets(e, ['STRIPE_KEY'])).toEqual({ STRIPE_KEY: 'sk_live' });
    await expect(loadSecrets(e, ['MISSING'])).rejects.toThrow('the Worker has no binding MISSING');
  });
});

describe('requestIP', () => {
  test("reads the client's address from CF-Connecting-IP, which the Hono adapter takes as Bun's", async () => {
    expect(requestIP(new Request('https://w', { headers: { 'CF-Connecting-IP': ' 203.0.113.7 ' } }))).toEqual({ address: '203.0.113.7' });
    expect(requestIP(new Request('https://w'))).toBeNull();
    const app = new Hono<{ Bindings: { requestIP: typeof requestIP } }>();
    app.get('/ip', c => c.text(c.env.requestIP(c.req.raw)?.address ?? 'none'));
    const response = await app.fetch(new Request('https://w/ip', { headers: { 'CF-Connecting-IP': '2001:db8::1' } }), { requestIP });
    expect(await response.text()).toBe('2001:db8::1');
  });
});

describe('perIsolate', () => {
  test('builds once, shares a build in flight, and builds again after a failure', async () => {
    let builds = 0;
    const deps = perIsolate(async () => {
      builds++;
      if (builds === 1) throw new Error('the secret store is unavailable');
      return { builds };
    });
    await expect(deps()).rejects.toThrow('the secret store is unavailable');
    const [a, b] = await Promise.all([deps(), deps()]);
    expect(a).toBe(b);
    expect(await deps()).toBe(a);
    expect(builds).toBe(2);
  });
});
