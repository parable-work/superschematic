import { describe, expect, test } from 'bun:test';
import { Database } from 'bun:sqlite';
import { readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Hono } from 'hono';
import {
  HttpProblem,
  IdempotencyCode,
  idempotencyLookup,
  idempotencyRequestHash,
  isIdempotencyKey,
  memoryIdempotencyStore,
  postgresIdempotencyStore,
  sqliteIdempotencyStore,
  type IdempotencyBegin,
  type IdempotencyStore,
  type IdempotencyTable,
  type OperationSpec,
} from './index';
import { mountOperation } from './hono';
import { openSqlite } from './sqlite';

/*
Idempotency keys (D140): each vector of
runtime/http/testdata/idempotency_parity.json answered as the Go runtime's
idempotency package answers it, the scenarios on the in-memory store, on
SQLite, and on Postgres when SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL
names one; and an @idempotent route through the Hono adapter.
*/

const names: IdempotencyTable = {
  table: 'idempotency_key',
  columns: {
    lookup: 'lookup',
    operation: 'operation',
    principal: 'principal',
    key: 'key',
    requestHash: 'request_hash',
    state: 'state',
    status: 'status',
    body: 'body',
    startedAt: 'started_at',
    expiresAt: 'expires_at',
  },
};

/** The reserved table as sqlgen writes it for each dialect: the times and the status are numbers. */
const ddl = (table: string, number: string): string =>
  `CREATE TABLE "${table}" ("lookup" TEXT NOT NULL UNIQUE, "operation" TEXT NOT NULL, "principal" TEXT NOT NULL, "key" TEXT NOT NULL, ` +
  `"request_hash" TEXT NOT NULL, "state" TEXT NOT NULL, "status" ${number}, "body" TEXT, "started_at" ${number} NOT NULL, "expires_at" ${number} NOT NULL)`;

let files = 0;
const stores: Record<string, () => Promise<IdempotencyStore>> = {
  memory: async () => memoryIdempotencyStore(),
  sqlite: async () => {
    const path = join(tmpdir(), `idempotency-${process.pid}-${Date.now()}-${files++}.db`);
    const setup = new Database(path);
    setup.exec(ddl(names.table, 'REAL'));
    setup.close();
    return sqliteIdempotencyStore(await openSqlite(path), names);
  },
};

const postgresUrl = process.env.SUPERSCHEMATIC_HTTP_RUNTIME_TEST_DATABASE_URL;
if (postgresUrl) {
  const { Pool } = await import('pg');
  const pool = new Pool({ connectionString: postgresUrl, max: 4 });
  let tables = 0;
  stores.postgres = async () => {
    const table = `idempotency_key_ts_${process.pid}_${tables++}`;
    await pool.query(ddl(table, 'DOUBLE PRECISION'));
    return postgresIdempotencyStore(pool, { ...names, table });
  };
}

interface Step {
  call: 'begin' | 'finish' | 'abandon' | 'sweep';
  at: number;
  lookup: string;
  requestHash: string;
  ttlMs: number;
  inFlightMs: number;
  startedAt: number;
  status: number;
  body: string;
  limit: number;
  want: IdempotencyBegin | number;
}

interface Parity {
  keys: { valid: string[]; invalid: string[] };
  lookups: { name: string; operation: string; principal: string; key: string; lookup: string }[];
  hashes: { name: string; method: string; path: string; body: string; hash: string }[];
  scenarios: { name: string; steps: Step[] }[];
}

const parity = JSON.parse(readFileSync(new URL('../../testdata/idempotency_parity.json', import.meta.url), 'utf8')) as Parity;

describe('idempotency keys (D140)', () => {
  test('a key is 1 to 255 printable ASCII characters', () => {
    for (const key of parity.keys.valid) expect([key, isIdempotencyKey(key)]).toEqual([key, true]);
    for (const key of parity.keys.invalid) expect([key, isIdempotencyKey(key)]).toEqual([key, false]);
  });

  for (const vector of parity.lookups) {
    test(`idempotencyLookup: ${vector.name}`, async () => {
      expect(await idempotencyLookup(vector.operation, vector.principal, vector.key)).toBe(vector.lookup);
    });
  }

  for (const vector of parity.hashes) {
    test(`idempotencyRequestHash: ${vector.name}`, async () => {
      expect(await idempotencyRequestHash(vector.method, vector.path, new TextEncoder().encode(vector.body))).toBe(vector.hash);
    });
  }

  for (const [dialect, open] of Object.entries(stores)) {
    for (const scenario of parity.scenarios) {
      test(`${dialect}: ${scenario.name}`, async () => {
        const store = await open();
        for (const [index, step] of scenario.steps.entries()) {
          switch (step.call) {
            case 'begin':
              expect([
                index,
                await store.begin({
                  lookup: step.lookup,
                  operation: 'orders.createOrder',
                  principal: 'user-1',
                  key: 'k',
                  requestHash: step.requestHash,
                  now: step.at,
                  expiresAt: step.at + step.ttlMs,
                  takeOverBefore: step.at - step.inFlightMs,
                }),
              ]).toEqual([index, step.want as IdempotencyBegin]);
              break;
            case 'finish':
              await store.finish(step.lookup, step.startedAt, step.status, step.body);
              break;
            case 'abandon':
              await store.abandon(step.lookup, step.startedAt);
              break;
            case 'sweep':
              expect([index, await store.sweep(step.at, step.limit)]).toEqual([index, step.want as number]);
              break;
          }
        }
      });
    }

    test(`${dialect}: of many requests under one key at once, one starts`, async () => {
      const store = await open();
      const attempt = { lookup: '9'.repeat(64), operation: 'o', principal: '', key: 'k', requestHash: 'h', now: 1000, expiresAt: 2000, takeOverBefore: 0 };
      const kinds = (await Promise.all(Array.from({ length: 16 }, () => store.begin(attempt)))).map(begin => begin.kind);
      expect(kinds.filter(kind => kind === 'started').length).toBe(1);
      expect(kinds.filter(kind => kind === 'running').length).toBe(15);
    });
  }
});

const createOrder: OperationSpec = {
  name: 'createOrder',
  namespace: 'orders',
  method: 'POST',
  path: '/api/orders',
  pathParams: [],
  queryParams: [],
  bodyParams: [{ name: 'sku', kind: 'string', required: true }],
  idempotent: { ttlSeconds: 24 * 60 * 60 },
  auth: { public: false, required: true, permissions: [] },
  manual: false,
};

describe('an @idempotent route', () => {
  function serve(spec: OperationSpec = createOrder, store: IdempotencyStore = memoryIdempotencyStore()) {
    let runs = 0;
    const app = new Hono();
    mountOperation(
      app,
      spec,
      async (_ctx, request) => {
        runs += 1;
        const sku = request.body.sku as string;
        if (sku === 'fail') throw new Error('boom');
        if (sku === 'refuse') throw new HttpProblem(409, 'Out of stock', { code: 'conflict' });
        return { run: runs, sku };
      },
      { idempotencyStore: store, authenticate: async ctx => ({ subject: ctx.headers.get('x-user') ?? 'u1', permissions: [] }) }
    );
    const post = (sku: string, headers: Record<string, string> = {}, path = '/api/orders') =>
      app.request(path, { method: 'POST', body: JSON.stringify({ sku }), headers: { 'content-type': 'application/json', authorization: 'Bearer t', ...headers } });
    return { post, runs: () => runs };
  }

  test('runs once per key and answers a repeat what the first request was answered', async () => {
    const { post, runs } = serve();
    const first = await post('tea', { 'Idempotency-Key': 'order-7', 'x-request-id': 'req-1' });
    expect([first.status, first.headers.get('idempotent-replay')]).toEqual([200, null]);
    const body = await first.text();
    expect(JSON.parse(body)).toEqual({ data: { run: 1, sku: 'tea' }, meta: { requestId: 'req-1' } });

    const again = await post('tea', { 'Idempotency-Key': 'order-7', 'x-request-id': 'req-2' });
    expect([again.status, again.headers.get('idempotent-replay'), again.headers.get('content-type'), again.headers.get('x-request-id')]).toEqual([200, 'true', 'application/json', 'req-2']);
    // The stored answer, the first request's id in its envelope and all.
    expect(await again.text()).toBe(body);
    expect(runs()).toBe(1);
  });

  test('refuses the key with another request, keeps each principal its own keys, and runs a request without one each time', async () => {
    const { post, runs } = serve();
    await post('tea', { 'Idempotency-Key': 'order-7' });
    const other = await post('coffee', { 'Idempotency-Key': 'order-7' });
    expect([other.status, (await other.json()).code]).toEqual([422, IdempotencyCode.REUSED]);
    expect((await post('tea', { 'Idempotency-Key': 'order-7' }, '/api/orders?dryRun=true')).status).toBe(422);
    expect(runs()).toBe(1);

    const theirs = await post('tea', { 'Idempotency-Key': 'order-7', 'x-user': 'u2' });
    expect([theirs.status, theirs.headers.get('idempotent-replay')]).toEqual([200, null]);
    await post('tea');
    await post('tea');
    expect(runs()).toBe(4);
  });

  test('stores a refusal under 500 and runs a 500 again', async () => {
    const { post, runs } = serve();
    for (const replayed of [null, 'true']) {
      const refused = await post('refuse', { 'Idempotency-Key': 'k1' });
      expect([refused.status, refused.headers.get('idempotent-replay'), refused.headers.get('content-type'), (await refused.json()).code]).toEqual([409, replayed, 'application/problem+json', 'conflict']);
    }
    expect(runs()).toBe(1);
    for (let attempt = 0; attempt < 2; attempt++) {
      const failed = await post('fail', { 'Idempotency-Key': 'k2' });
      expect([failed.status, failed.headers.get('idempotent-replay')]).toEqual([500, null]);
    }
    expect(runs()).toBe(3);
  });

  test('refuses a key no header may hold, and a missing one where the operation requires it', async () => {
    const { post, runs } = serve({ ...createOrder, idempotent: { ttlSeconds: 60, required: true } });
    const none = await post('tea');
    expect([none.status, (await none.json()).code]).toEqual([400, IdempotencyCode.REQUIRED]);
    for (const key of ['k'.repeat(256), 'tab\tbed']) {
      const bad = await post('tea', { 'Idempotency-Key': key });
      expect([key.length, bad.status, (await bad.json()).code]).toEqual([key.length, 400, IdempotencyCode.INVALID]);
    }
    expect(runs()).toBe(0);
    expect((await post('tea', { 'Idempotency-Key': 'k' })).status).toBe(200);
  });

  test('answers a repeat 409 while the first request runs, and never refuses a caller it has not let in', async () => {
    let release: () => void = () => undefined;
    const held = new Promise<void>(resolve => (release = resolve));
    let entered: () => void = () => undefined;
    const running = new Promise<void>(resolve => (entered = resolve));
    const app = new Hono();
    mountOperation(
      app,
      { ...createOrder, timeoutSeconds: 30 },
      async () => {
        entered();
        await held;
        return { ok: true };
      },
      { idempotencyStore: memoryIdempotencyStore(), authenticate: async ctx => (ctx.headers.get('authorization') ? { subject: 'u1', permissions: [] } : null) }
    );
    const post = (headers: Record<string, string>) => app.request('/api/orders', { method: 'POST', body: '{"sku":"tea"}', headers: { 'content-type': 'application/json', ...headers } });
    const first = post({ authorization: 'Bearer t', 'Idempotency-Key': 'slow' });
    await running;
    const waiting = await post({ authorization: 'Bearer t', 'Idempotency-Key': 'slow' });
    expect([waiting.status, (await waiting.json()).code, waiting.headers.get('retry-after')]).toEqual([409, IdempotencyCode.IN_FLIGHT, '30']);
    // The permission gate runs first: no key is read for a caller it refuses.
    expect((await post({ 'Idempotency-Key': 'slow' })).status).toBe(401);
    release();
    expect((await first).status).toBe(200);
    const replay = await post({ authorization: 'Bearer t', 'Idempotency-Key': 'slow' });
    expect([replay.status, replay.headers.get('idempotent-replay')]).toEqual([200, 'true']);
  });

  test('a route declared @idempotent mounts only with a store', () => {
    expect(() => mountOperation(new Hono(), createOrder, async () => null)).toThrow(/idempotencyStore/);
  });
});
