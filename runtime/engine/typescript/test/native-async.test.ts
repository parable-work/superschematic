// openEngineAsync (D58): the engine's API with every call a promise, on the
// Rust engine through its addon: on SQLite always, and on Postgres when
// ENGINE_TEST_PG_URL names one (a schema of the test's own, dropped after).
// Its options are data; a synchronous call on Postgres is refused; it
// hears commits through events.watch; and engineApp serves it, an event
// stream included.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { afterEach, describe, test } from 'node:test';

import { Hono } from 'hono';

import { allowAll, openEngine, openEngineAsync, type AsyncEngine } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, freshPath, openTestEngine, orderDocument, schemaDocument } from './helpers.ts';

const opened: AsyncEngine[] = [];

afterEach(async (context) => {
  for (const engine of opened.splice(0)) {
    engine.close();
  }
  cleanup(context);
});

interface Backend {
  name: string;
  options: () => Record<string, unknown>;
}

const backends: Backend[] = [{ name: 'sqlite', options: () => ({ path: freshPath() }) }];
const postgresUrl = process.env.ENGINE_TEST_PG_URL;
if (postgresUrl) {
  backends.push({ name: 'postgres', options: () => ({ postgres: { url: postgresUrl, schema: `engine_async_${randomUUID().replaceAll('-', '').slice(0, 12)}` } }) });
}

async function open(backend: Backend): Promise<AsyncEngine> {
  const engine = await openEngineAsync({ ...backend.options(), policy: allowAll } as never);
  opened.push(engine);
  return engine;
}

for (const backend of backends) {
  describe(`openEngineAsync (${backend.name})`, () => {
    test('every call is a promise of what the synchronous engine returns, and a watcher hears each commit', async () => {
      const engine = await open(backend);
      const heard: number[] = [];
      engine.events.watch({ committed: (cursor) => heard.push(cursor) });
      await engine.schemas.define(alice, orderDocument());
      assert.deepEqual(await engine.schemas.publish(alice, 'Order'), { namespace: 'default', name: 'Order', version: 1, published: true });
      const created = (await engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' })) as { id: string; seq: number };
      assert.deepEqual([created.id, created.seq], ['o1', 1]);
      const read = (await engine.instances.get(alice, 'Order', 'o1')) as { data: unknown };
      assert.deepEqual(read.data, { title: 'Desk' });
      assert.equal(((await engine.instances.list(alice, 'Order')) as { items: unknown[] }).items.length, 1);
      await assert.rejects(engine.instances.create(alice, 'Order', { title: 3 }), (error: Error & { code?: string }) => error.code === 'invalid_instance');
      const head = await engine.events.head();
      for (let turn = 0; turn < 200 && !heard.includes(head); turn += 1) {
        await new Promise((resolve) => setTimeout(resolve, 10));
      }
      assert.ok(heard.includes(head), `heard ${JSON.stringify(heard)}, head ${head}`);
      assert.equal(await engine.namespaces.resolve(undefined), 'default');
    });

    test("behaviors.invoke calls a namespace operation (D77), and refuses one of another scope or an unknown behavior", async () => {
      // test.Inbox (the test addon's) keeps notes in a table of its own,
      // read and cleared across the schemas that compose it.
      const inboxed = await openEngineAsync({ ...backend.options(), policy: allowAll, metaSchema: openMetaSchema(), behaviors: ['test.Inbox'] } as never);
      opened.push(inboxed);
      for (const name of ['Note', 'Task']) {
        const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as { types: Record<string, Record<string, unknown>> };
        document.types[name]!.behaviors = [{ name: 'test.Inbox', config: { label: name.toLowerCase() } }];
        await inboxed.schemas.define(alice, document);
        await inboxed.schemas.publish(alice, name);
        await inboxed.instances.create(alice, name, { title: name }, { id: `${name}-1` });
        await inboxed.instances.invoke(alice, name, `${name}-1`, 'add', { body: `on ${name}` });
      }
      assert.deepEqual(await inboxed.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'notes' }), {
        items: [
          ['Note', 'Note-1', 'on Note', 0],
          ['Task', 'Task-1', 'on Task', 0],
        ],
      });
      assert.deepEqual(await inboxed.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'clear', params: {} }), { changed: 2 });
      assert.deepEqual(await inboxed.behaviors.invoke(alice, { behavior: 'test.Inbox', operation: 'labels' }), [
        ['Note', 'note'],
        ['Task', 'task'],
      ]);
      const engine = await open(backend);
      await assert.rejects(
        engine.behaviors.invoke(alice, { behavior: 'Comments', operation: 'post' }),
        (error: Error & { code?: string }) => error.code === 'not_found' && /Comments's post is an instance operation/.test(error.message)
      );
      await assert.rejects(
        engine.behaviors.invoke(alice, { behavior: 'test.Nothing', operation: 'inbox', namespace: 'default' }),
        (error: Error & { code?: string }) => error.code === 'not_found' && error.message === 'this engine runs no behavior test.Nothing'
      );
    });

    test('its options are data: a function written in TypeScript is refused', async () => {
      await assert.rejects(openEngineAsync({ ...backend.options(), policy: allowAll, ids: () => 'x' } as never), TypeError);
      await assert.rejects(openEngineAsync({ ...backend.options(), policy: () => true } as never), TypeError);
    });

    test('engineApp serves it off the JavaScript thread, an event stream included', async () => {
      const engine = await open(backend);
      await engine.schemas.define(alice, orderDocument());
      await engine.schemas.publish(alice, 'Order');
      const app = new Hono();
      app.route('/api', engineApp(engine, { authenticate: async () => alice, stream: { heartbeatMs: 50 } }));
      const created = await app.request('/api/namespaces/default/schemas/Order/instances', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ id: 'o1', data: { title: 'Desk' } }),
      });
      assert.equal(created.status, 201);
      assert.equal(created.headers.get('location'), '/api/namespaces/default/schemas/Order/instances/o1');
      const got = await app.request('/api/namespaces/default/schemas/Order/instances/o1');
      assert.equal(got.status, 200);
      assert.equal(got.headers.get('etag'), '"1"');
      const streamed = await app.request('/api/namespaces/default/events?after=0', { headers: { accept: 'text/event-stream' } });
      assert.equal(streamed.status, 200);
      const reader = (streamed.body as ReadableStream<Uint8Array>).getReader();
      let text = '';
      while (!text.includes('event: ready')) {
        const { value, done } = await reader.read();
        if (done) {
          break;
        }
        text += new TextDecoder().decode(value);
      }
      assert.match(text, /^: open\n\n/u);
      assert.match(text, /event: ready/u);
      await reader.cancel();
    });
  });
}

// A clock function is the engine's time for every call, a request served
// over HTTP included: the Rust engine's facade reads it once per request,
// as for a call, and sets the engine's manual clock to it first.
describe('a clock function', () => {
  test('is read for each request engineApp serves, synchronous and asynchronous', async () => {
    let now = 1_000_000;
    const clock = () => now;
    const engines: Array<{ name: string; open: () => Promise<Hono> }> = [
      {
        name: 'openEngine',
        open: async () => {
          const engine = openTestEngine({ clock });
          engine.schemas.define(alice, orderDocument());
          engine.schemas.publish(alice, 'Order');
          return new Hono().route('/api', engineApp(engine, { authenticate: async () => alice }));
        },
      },
      {
        name: 'openEngineAsync',
        open: async () => {
          const engine = await openEngineAsync({ path: freshPath(), policy: allowAll, clock });
          opened.push(engine);
          await engine.schemas.define(alice, orderDocument());
          await engine.schemas.publish(alice, 'Order');
          return new Hono().route('/api', engineApp(engine, { authenticate: async () => alice }));
        },
      },
    ];
    for (const { name, open: openOne } of engines) {
      now = 1_000_000;
      const app = await openOne();
      now = 2_000_000;
      const created = await app.request('/api/namespaces/default/schemas/Order/instances', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ id: 'o1', data: { title: 'Desk' } }),
      });
      assert.equal(created.status, 201, name);
      now = 3_000_000;
      const updated = await app.request('/api/namespaces/default/schemas/Order/instances/o1', {
        method: 'PATCH',
        headers: { 'content-type': 'application/merge-patch+json', 'if-match': '"1"' },
        body: JSON.stringify({ title: 'Lamp' }),
      });
      assert.equal(updated.status, 200, name);
      const { data } = (await updated.json()) as { data: { createdAt: number; updatedAt: number } };
      assert.deepEqual([data.createdAt, data.updatedAt], [2_000_000, 3_000_000], name);
    }
  });
});

describe('a synchronous engine on Postgres', { skip: !postgresUrl && 'ENGINE_TEST_PG_URL names no Postgres' }, () => {
  test('refuses a synchronous call: it would hold the JavaScript thread while it waits on the database', () => {
    const engine = openEngine({ postgres: { url: postgresUrl as string, schema: `engine_sync_${randomUUID().replaceAll('-', '').slice(0, 12)}` }, policy: allowAll });
    try {
      assert.throws(() => engine.schemas.list(alice), (error: Error & { code?: string }) => error.code === 'sync_on_async_storage');
    } finally {
      engine.close();
    }
  });
});
