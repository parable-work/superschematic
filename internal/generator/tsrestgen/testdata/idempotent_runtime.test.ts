/*
@idempotent operations end to end (D140), run by
TestGeneratedIdempotentRoutesEndToEnd with API_DIR set to the materialized
fixture-idempotent-api package, SDK_DIR to its generated TypeScript SDK and
IDEMPOTENCY_DDL to the SQLite DDL of the table fixture-idempotent-db
declares for the keys. The generated router serves
idempotent_implementation.ts with its keys in a SQLite file made from that
DDL, under the names router.ts exports: a repeat under a key is answered
what the first request was, and the implementation runs once.
*/
import { describe, expect, test } from 'bun:test';
import { Database } from 'bun:sqlite';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { Hono } from 'hono';
import { sqliteIdempotencyStore } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { openSqlite } from '@superschematic/http-runtime/sqlite';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const ddl = process.env.IDEMPOTENCY_DDL;
if (!apiDir || !sdkDir || !ddl) throw new Error('API_DIR, SDK_DIR and IDEMPOTENCY_DDL are required');
const { buildRouter, idempotencyTable, operationSpecs } = await import(`${apiDir}/router.ts`);
const { counts, orders, placed } = await import(`${apiDir}/idempotent_implementation.ts`);
const { FixtureIdempotentApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

const path = join(tmpdir(), `idempotent-routes-${process.pid}-${Date.now()}.db`);
const setup = new Database(path);
setup.exec(ddl);
setup.close();
const keys = new Database(path, { readonly: true });

const app = new Hono();
app.route('/', buildRouter({ orders }, { idempotencyStore: sqliteIdempotencyStore(await openSqlite(path), idempotencyTable) }));
app.notFound(notFoundHandler());
app.onError(errorHandler());

const place = (sku: string, key?: string) =>
  app.request('/api/orders', {
    method: 'POST',
    body: JSON.stringify({ sku, quantity: 2 }),
    headers: { 'content-type': 'application/json', ...(key ? { 'Idempotency-Key': key } : {}) },
  });
const cancel = (number: number, key?: string) => app.request(`/api/orders/${number}`, { method: 'DELETE', headers: key ? { 'Idempotency-Key': key } : {} });

describe('generated fixture-idempotent-api router', () => {
  test('the operation table and the table of its keys', () => {
    expect(operationSpecs.placeOrder.idempotent).toEqual({ ttlSeconds: 3600 });
    expect(operationSpecs.cancelOrder.idempotent).toEqual({ ttlSeconds: 86400, required: true });
    expect(operationSpecs.listOrders.idempotent).toBeUndefined();
    expect(idempotencyTable.table).toBe('idempotency_key');
    expect(() => buildRouter({ orders })).toThrow(/idempotencyStore/);
  });

  test('a key runs the operation once, and a repeat is answered what the first request was', async () => {
    const first = await place('tea', 'order-1');
    expect([first.status, first.headers.get('idempotent-replay')]).toEqual([200, null]);
    const body = await first.text();
    expect(JSON.parse(body).data).toEqual({ number: 1, sku: 'tea', quantity: 2, cancelled: false });

    const again = await place('tea', 'order-1');
    expect([again.status, again.headers.get('idempotent-replay'), await again.text()]).toEqual([200, 'true', body]);
    expect([counts.runs, placed.length]).toEqual([1, 1]);

    // The key with another order is refused, and nothing runs.
    const other = await place('coffee', 'order-1');
    expect([other.status, (await other.json()).code]).toEqual([422, 'idempotency_key_reused']);
    // Another key runs again, and so does a request with none.
    expect((await place('tea', 'order-2')).headers.get('idempotent-replay')).toBeNull();
    await place('tea');
    await place('tea');
    expect([counts.runs, placed.length]).toEqual([4, 4]);
  });

  test('a 500 keeps no answer, and a refusal under 500 is kept as any answer', async () => {
    for (let attempt = 0; attempt < 2; attempt++) {
      const failed = await place('broken', 'order-3');
      expect([failed.status, failed.headers.get('idempotent-replay')]).toEqual([500, null]);
    }
    expect(counts.runs).toBe(6);
    for (const replayed of [null, 'true']) {
      const refused = await place('gone', 'order-4');
      expect([refused.status, refused.headers.get('idempotent-replay'), refused.headers.get('content-type'), (await refused.json()).code]).toEqual([409, replayed, 'application/problem+json', 'conflict']);
    }
    expect(counts.runs).toBe(7);
  });

  test('an operation that requires a key refuses a request without one, and a key is one operation\'s', async () => {
    const none = await cancel(1);
    expect([none.status, (await none.json()).code]).toEqual([400, 'idempotency_key_required']);
    expect(counts.runs).toBe(7);
    for (const replayed of [null, 'true']) {
      const cancelled = await cancel(1, 'cancel-1');
      expect([cancelled.status, cancelled.headers.get('idempotent-replay'), (await cancelled.json()).data.cancelled]).toEqual([200, replayed, true]);
    }
    // The same key on another order is another request.
    expect((await cancel(2, 'cancel-1')).status).toBe(422);
    // placeOrder's key is cancelOrder's own key too.
    const own = await cancel(2, 'order-1');
    expect([own.status, own.headers.get('idempotent-replay')]).toEqual([200, null]);
    expect(counts.runs).toBe(9);
  });

  test('the rows are in the generated table, done, with their answers', () => {
    const rows = keys
      .query(`SELECT "${idempotencyTable.columns.operation}" AS operation, "${idempotencyTable.columns.state}" AS state, "${idempotencyTable.columns.status}" AS status FROM "${idempotencyTable.table}"`)
      .all() as { operation: string; state: string; status: number }[];
    expect(rows.map(row => `${row.operation} ${row.state} ${row.status}`).sort()).toEqual([
      'orders.cancelOrder done 200',
      'orders.cancelOrder done 200',
      'orders.placeOrder done 200',
      'orders.placeOrder done 200',
      'orders.placeOrder done 409',
    ]);
  });

  test('the SDK sends a key of its own for each call, and the one its caller names', async () => {
    const seen: (string | null)[] = [];
    const sdk = new FixtureIdempotentApiSDK({
      baseUrl: 'http://api.example.com',
      fetch: (input: RequestInfo | URL, init?: RequestInit) => {
        seen.push(new Headers(init?.headers).get('idempotency-key'));
        return app.request(String(input), init);
      },
    });
    const before = placed.length;
    // Two calls, two keys the SDK made: each a UUID, and each ran.
    await sdk.orders.placeOrder({ sku: 'mate', quantity: 1 });
    await sdk.orders.placeOrder({ sku: 'mate', quantity: 1 });
    expect(seen[0]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(seen[1]).not.toBe(seen[0]);
    expect(placed.length).toBe(before + 2);

    // A key the caller names: the second call is answered the first one's order.
    const once = await sdk.orders.placeOrder({ sku: 'chai', quantity: 3 }, { idempotencyKey: 'sdk-order-1' });
    const twice = await sdk.orders.placeOrder({ sku: 'chai', quantity: 3 }, { idempotencyKey: 'sdk-order-1' });
    expect(twice).toEqual(once);
    expect(seen.slice(2)).toEqual(['sdk-order-1', 'sdk-order-1']);
    expect(placed.length).toBe(before + 3);
    // The key with another order is the SDK's error.
    let reused: unknown;
    try {
      await sdk.orders.placeOrder({ sku: 'chai', quantity: 4 }, { idempotencyKey: 'sdk-order-1' });
    } catch (error) {
      reused = error;
    }
    expect(reused).toBeInstanceOf(ApiError);
    expect([(reused as { statusCode: number }).statusCode, (reused as { code: string }).code]).toEqual([422, 'idempotency_key_reused']);

    // cancelOrder requires a key, which the SDK always sends.
    expect((await sdk.orders.cancelOrder(once.number)).cancelled).toBe(true);
    // An operation that is not @idempotent is sent no key.
    await sdk.orders.listOrders();
    expect(seen.at(-1)).toBeNull();
  });
});
