// The event stream over a listening server: replay from a cursor in pages,
// events as they commit, resuming from Last-Event-ID, heartbeats, and
// teardown when the client leaves or the engine closes. On Node.js the
// server is @hono/node-server, on Bun it is Bun.serve.
import assert from 'node:assert/strict';
import type { Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { afterEach, describe, test } from 'node:test';

import { serve as serveNode } from '@hono/node-server';
import type { Authenticator } from '@superschematic/http-runtime';
import type { Hono } from 'hono';

import { type AccessPolicy, type Engine, type EngineEvent, type EngineOptions } from '../dist/index.js';
import { engineApp, type EngineHttpOptions } from '../dist/http/index.js';
import { openMetaSchema, publishItem, testBehaviors } from './behavior-fixtures.ts';
import { alice, cleanup, openTestEngine, orderDocument, schemaDocument } from './helpers.ts';

// The bearer token is the caller's subject.
const authenticate: Authenticator = async (ctx) => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: [] } : null);

interface Listening {
  url: string;
  close(): Promise<void>;
}

interface BunServer {
  port: number;
  stop(force?: boolean): unknown;
}

interface BunRuntime {
  serve(options: { fetch: (request: Request) => Response | Promise<Response>; port: number; hostname: string }): BunServer;
}

async function listen(app: Hono): Promise<Listening> {
  const bun = (globalThis as { Bun?: BunRuntime }).Bun;
  if (bun) {
    const server = bun.serve({ fetch: (request) => app.fetch(request), port: 0, hostname: '127.0.0.1' });
    return {
      url: `http://127.0.0.1:${server.port}`,
      close: async () => {
        server.stop(true);
      },
    };
  }
  return new Promise((resolve) => {
    const server = serveNode({ fetch: app.fetch, port: 0, hostname: '127.0.0.1' }, (info: AddressInfo) => {
      resolve({
        url: `http://127.0.0.1:${info.port}`,
        close: () =>
          new Promise<void>((done) => {
            (server as Server).closeAllConnections();
            server.close(() => done());
          }),
      });
    }) as Server;
  });
}

interface Frame {
  id?: string;
  data?: string;
  comment?: string;
}

interface Client {
  response: Response;
  frames: Frame[];
  /** Resolves when the server ends the body. */
  ended: Promise<void>;
  /** Waits for a frame that matches, returning every frame so far. */
  until(match: (frame: Frame, frames: Frame[]) => boolean, timeoutMs?: number): Promise<Frame[]>;
  /** The events received, parsed. */
  events(): EngineEvent[];
  disconnect(): void;
}

const clients: Client[] = [];
const servers: Listening[] = [];

afterEach(async () => {
  for (const client of clients.splice(0)) {
    client.disconnect();
  }
  for (const server of servers.splice(0)) {
    await server.close();
  }
  cleanup();
});

/** open connects to the stream and reads its frames as they arrive. */
async function open(url: string, headers: Record<string, string> = {}): Promise<Client> {
  const abort = new AbortController();
  const response = await fetch(url, {
    headers: { accept: 'text/event-stream', authorization: 'Bearer alice', ...headers },
    signal: abort.signal,
  });
  const frames: Frame[] = [];
  let notify = () => {};
  let buffer = '';
  const decoder = new TextDecoder();
  const ended = (async () => {
    if (!response.body || response.headers.get('content-type') !== 'text/event-stream') {
      return;
    }
    const reader = response.body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) {
          break;
        }
        buffer += decoder.decode(value, { stream: true });
        let end = buffer.indexOf('\n\n');
        while (end >= 0) {
          frames.push(parse(buffer.slice(0, end)));
          buffer = buffer.slice(end + 2);
          end = buffer.indexOf('\n\n');
        }
        notify();
      }
    } catch {
      // Aborted by disconnect.
    } finally {
      notify();
    }
  })();
  const client: Client = {
    response,
    frames,
    ended,
    async until(match, timeoutMs = 5000) {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        if (frames.some((frame) => match(frame, frames))) {
          return frames;
        }
        const left = deadline - Date.now();
        if (left <= 0) {
          assert.fail(`no matching frame within ${timeoutMs} ms; got ${JSON.stringify(frames)}`);
        }
        await new Promise<void>((resolve) => {
          const timer = setTimeout(resolve, Math.min(left, 100));
          notify = () => {
            clearTimeout(timer);
            resolve();
          };
        });
      }
    },
    events() {
      return frames.filter((frame) => frame.data !== undefined).map((frame) => JSON.parse(frame.data as string) as EngineEvent);
    },
    disconnect() {
      abort.abort();
    },
  };
  clients.push(client);
  return client;
}

function parse(block: string): Frame {
  const frame: Frame = {};
  for (const line of block.split('\n')) {
    if (line.startsWith(':')) {
      frame.comment = line.slice(1).trim();
    } else if (line.startsWith('id: ')) {
      frame.id = line.slice(4);
    } else if (line.startsWith('data: ')) {
      frame.data = line.slice(6);
    }
  }
  return frame;
}

const noteDocument = schemaDocument('Note', [{ name: 'body', typeRef: { name: 'string' }, required: true }]);

async function serve(options: EngineHttpOptions = {}, engineOptions: Partial<EngineOptions> = {}): Promise<{ engine: Engine; base: string }> {
  const engine = openTestEngine(engineOptions);
  const server = await listen(engineApp(engine, { authenticate, ...options }));
  servers.push(server);
  return { engine, base: server.url };
}

function publishOrders(engine: Engine, namespace?: string): void {
  engine.schemas.define(alice, orderDocument(), { namespace });
  engine.schemas.publish(alice, 'Order', { namespace });
}

async function eventually(check: () => boolean, what: string): Promise<void> {
  const deadline = Date.now() + 5000;
  while (!check()) {
    if (Date.now() > deadline) {
      assert.fail(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

describe('event stream', () => {
  test('replays from a cursor in pages, then sends events as they commit', async () => {
    const { engine, base } = await serve({ stream: { pageSize: 2, heartbeatMs: 60_000 } });
    publishOrders(engine);
    for (const id of ['o1', 'o2', 'o3', 'o4', 'o5']) {
      engine.instances.create(alice, 'Order', { title: id }, { id });
    }
    const reads: Array<number | undefined> = [];
    const read = engine.events.read.bind(engine.events);
    engine.events.read = (principal, options) => {
      reads.push(options?.limit);
      return read(principal, options);
    };

    const client = await open(`${base}/namespaces/default/events?after=0`);
    assert.equal(client.response.status, 200);
    assert.equal(client.response.headers.get('cache-control'), 'no-store');
    await client.until((frame) => frame.id !== undefined && client.events().length === 6);
    const logged = read(alice, { limit: 50 }).events;
    assert.deepEqual(client.events(), logged);
    assert.deepEqual(
      client.frames.filter((frame) => frame.id !== undefined).map((frame) => Number(frame.id)),
      logged.map((event) => event.cursor)
    );
    assert.equal(client.frames[0].comment, 'open');
    assert.ok(reads.length >= 3 && reads.every((limit) => limit === 2), `pages of 2: ${JSON.stringify(reads)}`);

    // Live: a write through the API and one through the engine.
    const created = await fetch(`${base}/namespaces/default/schemas/Order/instances`, {
      method: 'POST',
      headers: { authorization: 'Bearer bob', 'content-type': 'application/json' },
      body: JSON.stringify({ id: 'o6', data: { title: 'o6' } }),
    });
    assert.equal(created.status, 201);
    engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
    await client.until(() => client.events().length === 8);
    assert.deepEqual(
      client.events().slice(6).map((event) => [event.kind, event.instanceId, event.actor]),
      [
        ['create', 'o6', 'bob'],
        ['update', 'o1', 'alice'],
      ]
    );
    assert.ok(reads.every((limit) => limit === 2));
  });

  test('resumes after Last-Event-ID, which wins over after', async () => {
    const { engine, base } = await serve();
    publishOrders(engine);
    for (const id of ['o1', 'o2', 'o3']) {
      engine.instances.create(alice, 'Order', { title: id }, { id });
    }
    const cursors = engine.events.read(alice).events.map((event) => event.cursor);
    const first = await open(`${base}/namespaces/default/events?after=${cursors[0]}`);
    await first.until(() => first.events().length === 3);
    const lastId = first.frames.filter((frame) => frame.id !== undefined)[1].id as string;
    first.disconnect();

    const resumed = await open(`${base}/namespaces/default/events?after=0`, { 'last-event-id': lastId });
    await resumed.until(() => resumed.events().length === 1);
    assert.deepEqual(
      resumed.events().map((event) => event.cursor),
      [cursors[3]]
    );
  });

  test('filters by schema and instance', async () => {
    const { engine, base } = await serve();
    publishOrders(engine);
    engine.schemas.define(alice, noteDocument);
    engine.schemas.publish(alice, 'Note');
    engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
    engine.instances.create(alice, 'Note', { body: 'hi' }, { id: 'n1' });
    engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
    const client = await open(`${base}/namespaces/default/events?schema=Order&instanceId=o2`);
    engine.instances.update(alice, 'Order', 'o1', { title: 'Chair' });
    engine.instances.update(alice, 'Order', 'o2', { title: 'Stool' });
    await client.until(() => client.events().length === 2);
    assert.deepEqual(
      client.events().map((event) => [event.kind, event.instanceId, event.seq]),
      [
        ['create', 'o2', 1],
        ['update', 'o2', 2],
      ]
    );
  });

  test('carries operation events, replayed and as they commit, and no read-only operation', async () => {
    const { engine, base } = await serve({}, { metaSchema: openMetaSchema(), behaviors: testBehaviors });
    publishItem(engine, [{ name: 'test.Counter', config: { limit: 3 } }, { name: 'test.Tally' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'i1' });
    engine.instances.invoke(alice, 'Item', 'i1', 'increment');
    const client = await open(`${base}/namespaces/default/events?schema=Item`);
    await client.until(() => client.events().length === 3);
    // A read-only operation appends nothing; bump's two calls run in
    // savepoints under one operation event; tryBump's call is vetoed and
    // rolled back alone, and its own write commits with its event.
    engine.instances.invoke(alice, 'Item', 'i1', 'history');
    engine.instances.invoke(alice, 'Item', 'i1', 'bump', { times: 2 });
    engine.instances.invoke(alice, 'Item', 'i1', 'tryBump');
    await client.until(() => client.events().length === 5);
    assert.deepEqual(
      client.events().map((event) => [event.kind, event.seq, event.kind === 'operation' ? (event.change as { operation: string }).operation : null]),
      [
        ['publish', null, null],
        ['create', 1, null],
        ['operation', 2, 'increment'],
        ['operation', 3, 'bump'],
        ['operation', 4, 'tryBump'],
      ]
    );
    assert.deepEqual(client.events(), engine.events.read(alice, { schema: 'Item' }).events);
  });

  test('sends a comment every heartbeat while it waits', async () => {
    const { engine, base } = await serve({ stream: { heartbeatMs: 30 } });
    publishOrders(engine);
    const client = await open(`${base}/namespaces/default/events`);
    await client.until(() => client.frames.filter((frame) => frame.comment === 'keepalive').length >= 2);
    assert.equal(client.events().length, 1);
    engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
    await client.until(() => client.events().length === 2);
  });

  test('a namespace stream carries its own events and the shared publishes, and none of another namespace', async () => {
    const { engine, base } = await serve({}, { namespaces: { names: ['east', 'west', 'common'], shared: 'common' } });
    publishOrders(engine, 'west');
    const client = await open(`${base}/namespaces/east/events`);
    engine.schemas.define(alice, noteDocument, { namespace: 'common' });
    engine.schemas.publish(alice, 'Note', { namespace: 'common' });
    engine.instances.create(alice, 'Order', { title: 'West' }, { id: 'w1', namespace: 'west' });
    engine.instances.create(alice, 'Note', { body: 'West' }, { id: 'n1', namespace: 'west' });
    engine.instances.create(alice, 'Note', { body: 'East' }, { id: 'n1', namespace: 'east' });
    await client.until(() => client.events().some((event) => event.namespace === 'east'));
    assert.deepEqual(
      client.events().map((event) => [event.kind, event.namespace, event.schema]),
      [
        ['publish', 'common', 'Note'],
        ['create', 'east', 'Note'],
      ]
    );
  });

  test('a refused stream answers a problem document instead', async () => {
    const onlyOrders: AccessPolicy = ({ schema }) => schema === 'Order';
    const { engine, base } = await serve({}, { policy: onlyOrders });
    publishOrders(engine);
    const forbidden = await open(`${base}/namespaces/default/events?schema=Note`);
    assert.equal(forbidden.response.status, 403);
    assert.equal(forbidden.response.headers.get('content-type'), 'application/problem+json');
    assert.equal(((await forbidden.response.json()) as { code: string }).code, 'forbidden');

    const anonymous = await open(`${base}/namespaces/default/events`, { authorization: '' });
    assert.equal(anonymous.response.status, 401);
    await anonymous.response.body?.cancel();
    const unknown = await open(`${base}/namespaces/nowhere/events`);
    assert.equal(unknown.response.status, 404);
    await unknown.response.body?.cancel();
    assert.equal(engine.events.watching, 0);
  });

  test('a client that disconnects stops its stream and its watcher', async () => {
    const { engine, base } = await serve({ stream: { heartbeatMs: 60_000 } });
    publishOrders(engine);
    const streams = await Promise.all([0, 1, 2].map(() => open(`${base}/namespaces/default/events`)));
    await Promise.all(streams.map((client) => client.until(() => client.events().length === 1)));
    assert.equal(engine.events.watching, 3);
    streams[0].disconnect();
    streams[1].disconnect();
    await eventually(() => engine.events.watching === 1, 'two watchers to go');
    engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
    await streams[2].until(() => streams[2].events().length === 2);
  });

  test('closing the engine ends every stream', async () => {
    const { engine, base } = await serve({ stream: { heartbeatMs: 60_000 } });
    publishOrders(engine);
    const client = await open(`${base}/namespaces/default/events`);
    await client.until(() => client.events().length === 1);
    engine.close();
    await client.ended;
    assert.equal(engine.events.watching, 0);
  });
});
