// The ./server entry (D58): what a generated TypeScript server opens and
// mounts, on the Rust engine through its addon. openEngineInBackground opens
// the engine off the JavaScript thread, retrying until its database
// answers, and runs a setup on it before it hands it out; engineApp serves
// it from the start, 503 unavailable until it opens; deploy defines and
// publishes a server's documents once; and a mount's done signal ends its
// event streams. On SQLite always, and on Postgres when ENGINE_TEST_PG_URL
// names one (a schema of the test's own). It skips when the addon is not
// built.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { afterEach, describe, test } from 'node:test';

import { Hono } from 'hono';

import { loadAddon } from '../dist/native/addon.js';
import {
  EngineNotOpenError,
  engineApp,
  engineRouteSpecs,
  openEngineInBackground,
  type EngineBackground,
} from '../dist/server/index.js';
import { alice, cleanup, freshPath, orderDocument } from './helpers.ts';

const addon = (() => {
  try {
    loadAddon();
    return false;
  } catch (error) {
    return `the Rust engine's addon is not built (scripts/engine-napi.sh): ${error instanceof Error ? error.message : String(error)}`;
  }
})();

const opened: EngineBackground[] = [];

afterEach((context) => {
  for (const background of opened.splice(0)) {
    background.close();
  }
  cleanup(context);
});

/** A Postgres no server listens on, which refuses each connection at once. */
const unreachable = { postgres: { url: 'postgres://engine@127.0.0.1:9/engine?connect_timeout=1&sslmode=disable' } };

interface Backend {
  name: string;
  options: () => Record<string, unknown>;
}

const backends: Backend[] = [{ name: 'sqlite', options: () => ({ path: freshPath() }) }];
const postgresUrl = process.env.ENGINE_TEST_PG_URL;
if (postgresUrl) {
  backends.push({ name: 'postgres', options: () => ({ postgres: { url: postgresUrl, schema: `engine_server_${randomUUID().replaceAll('-', '').slice(0, 12)}` } }) });
}

function background(options: Record<string, unknown>, rest: Parameters<typeof openEngineInBackground>[1] = {}): EngineBackground {
  const opening = openEngineInBackground({ policy: 'allowAll', ...options } as never, rest);
  opened.push(opening);
  return opening;
}

async function until(check: () => boolean, what: string): Promise<void> {
  for (let turn = 0; turn < 500; turn += 1) {
    if (check()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail(`timed out waiting for ${what}`);
}

describe('openEngineInBackground', { skip: addon }, () => {
  test('until the engine opens, its routes answer 503 unavailable before any authentication, and it retries with a doubling wait', async () => {
    const failures: number[] = [];
    const opening = background(unreachable, { retryMinMs: 20, retryMaxMs: 80, onError: (_error, retryMs) => failures.push(retryMs) });
    const app = new Hono().route('/engine/desk', engineApp(opening, { authenticate: async () => null }));
    const refused = await app.request('/engine/desk/namespaces/default/schemas', { headers: { 'x-request-id': 'r-1' } });
    assert.equal(refused.status, 503);
    assert.equal(refused.headers.get('content-type'), 'application/problem+json');
    assert.deepEqual(await refused.json(), {
      type: 'about:blank',
      title: 'Service Unavailable',
      status: 503,
      detail: 'The engine is not available',
      code: 'unavailable',
      requestId: 'r-1',
    });
    await until(() => failures.length >= 4, 'four failed attempts');
    assert.deepEqual(failures.slice(0, 4), [20, 40, 80, 80]);
    assert.equal(opening.current, undefined);
    assert.throws(
      () => opening.engine(),
      (error: Error) => error instanceof EngineNotOpenError && /the engine's connection to the database failed/u.test(error.message)
    );
    await assert.rejects(opening.ready(), EngineNotOpenError);
    const waiting = new AbortController();
    const waited = opening.wait(waiting.signal);
    waiting.abort(new Error('the job stopped'));
    await assert.rejects(waited, /the job stopped/u);
    opening.close();
    assert.equal(opening.closed, true);
    await assert.rejects(opening.wait(), /the engine is closed/u);
  });

  for (const backend of backends) {
    test(`on ${backend.name}, it opens, runs its setup, deploys a document once, and engineApp serves it`, async () => {
      const deployed: unknown[] = [];
      const opening = background(backend.options(), {
        setup: async (engine) => {
          deployed.push(await engine.deploy([orderDocument()], { namespace: 'default' }));
        },
      });
      const engine = await opening.wait();
      assert.equal(opening.current, engine);
      await opening.ready();
      assert.deepEqual(deployed, [[{ name: 'Order', namespace: 'default', version: 1, defined: true, published: true }]]);
      assert.deepEqual(await engine.deploy([orderDocument()]), [{ name: 'Order', namespace: 'default', version: 1, defined: false, published: false }]);
      const health = await engine.health();
      assert.deepEqual([health.ready, health.closed, health.poisoned, health.dialect], [true, false, false, backend.name]);
      const app = new Hono().route('/engine/desk', engineApp(opening, { authenticate: async () => alice }));
      const created = await app.request('/engine/desk/namespaces/default/schemas/Order/instances', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ id: 'o1', data: { title: 'Desk' } }),
      });
      assert.equal(created.status, 201);
      const record = await engine.instances.get(alice, 'Order', 'o1');
      assert.deepEqual(record?.data, { title: 'Desk' });
    });
  }

  test('a setup that fails closes the engine, and the next attempt opens it again', async () => {
    let attempts = 0;
    const failures: unknown[] = [];
    const opening = background(
      { path: freshPath() },
      {
        retryMinMs: 10,
        setup: async () => {
          attempts += 1;
          if (attempts === 1) {
            throw new Error('deploy: refused');
          }
        },
        onError: (error) => failures.push(error),
      }
    );
    await opening.wait();
    assert.equal(attempts, 2);
    assert.deepEqual(
      failures.map((error) => (error as Error).message),
      ['deploy: refused']
    );
  });

  test("a mount's done signal ends every event stream, so a host's shutdown waits for none", async () => {
    const opening = background({ path: freshPath() });
    await opening.wait();
    const done = new AbortController();
    const app = new Hono().route('/engine/desk', engineApp(opening, { authenticate: async () => alice, done: done.signal, stream: { heartbeatMs: 50 } }));
    const streamed = await app.request('/engine/desk/namespaces/default/events?after=0', { headers: { accept: 'text/event-stream' } });
    assert.equal(streamed.status, 200);
    const reader = (streamed.body as ReadableStream<Uint8Array>).getReader();
    let text = '';
    while (!text.includes('event: ready')) {
      const { value, done: ended } = await reader.read();
      assert.equal(ended, false);
      text += new TextDecoder().decode(value);
    }
    done.abort();
    for (;;) {
      const { done: ended } = await reader.read();
      if (ended) {
        break;
      }
    }
  });

  test("engineRouteSpecs are the engine's routes under a mount, for a host's CORS", () => {
    const specs = engineRouteSpecs('/engine/desk-db/');
    assert.ok(specs.some((spec) => spec.method === 'POST' && spec.path === '/engine/desk-db/namespaces/{namespace}/schemas/{name}/instances'));
    assert.ok(specs.some((spec) => spec.method === 'POST' && spec.path === '/engine/desk-db/namespaces/{namespace}/mcp'));
    assert.deepEqual([...new Set(specs.map((spec) => spec.method))].sort(), ['DELETE', 'GET', 'PATCH', 'POST']);
  });
});
