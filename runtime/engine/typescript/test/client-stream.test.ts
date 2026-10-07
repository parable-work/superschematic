// The client's event stream and reconciler against an engine served in
// process: the server-sent event parser, replay, the ready event, live
// events, the cursor id-only messages move, resuming by Last-Event-ID
// after a dropped connection, a refusal ending a subscription, and the
// reconciler's handler, retries and stored cursor.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { SseParser, memoryCursor, reconcile, type EngineEvent, type StreamMessage } from '../dist/client/index.js';
import { refused, serve, until } from './client-fixtures.ts';
import { cleanup } from './helpers.ts';

afterEach(cleanup);

describe('the server-sent event parser', () => {
  test('reads messages as an EventSource does', () => {
    const parser = new SseParser();
    assert.deepEqual(parser.push('\uFEFF: open\n\nid: 1\ndata: {"a":1}\n\n'), [{ type: 'message', data: '{"a":1}', lastEventId: '1' }]);
    // CRLF and CR end lines too, and a message's lines join with LF.
    assert.deepEqual(parser.push('id: 2\r\ndata: a\r\ndata: b\r\n\r\nid: 3\rdata:c\r\r'), [
      { type: 'message', data: 'a\nb', lastEventId: '2' },
      { type: 'message', data: 'c', lastEventId: '3' },
    ]);
    // A message split across chunks, a CR that ends one chunk and the LF that starts the next.
    assert.deepEqual(parser.push('event: re'), []);
    assert.deepEqual(parser.push('ady\nid: 4\ndata: {"cursor":4}\r'), []);
    assert.deepEqual(parser.push('\n\r\n'), [{ type: 'ready', data: '{"cursor":4}', lastEventId: '4' }]);
    // A message with only an id moves the last event id and carries no data.
    assert.deepEqual(parser.push('id: 9\n\n: keepalive\n\n'), [{ type: '', data: '', lastEventId: '9' }]);
    // An event with no id keeps the last one; an id with a NUL is ignored.
    assert.deepEqual(parser.push('id: 1\u00000\ndata: x\n\n'), [{ type: 'message', data: 'x', lastEventId: '9' }]);
  });
});

describe('the event stream', () => {
  test('replays from a cursor, sends ready once caught up, then each event as it commits', async () => {
    const served = serve();
    const client = served.client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    const subscription = client.events.subscribe({ after: 0, schema: 'Task' });
    const messages = subscription[Symbol.asyncIterator]();
    const replayed: StreamMessage[] = [];
    for (let index = 0; index < 4; index++) {
      replayed.push((await messages.next()).value as StreamMessage);
    }
    assert.deepEqual(
      replayed.map((message) => (message.type === 'event' ? [message.event.cursor, message.event.kind] : ['ready', message.cursor])),
      [
        [1, 'define'],
        [2, 'publish'],
        [3, 'create'],
        ['ready', 3],
      ]
    );
    assert.equal(subscription.cursor, 3);
    const next = messages.next();
    await client.instances.create('Task', { title: 'Build' }, { id: 'build' });
    const live = (await next).value as StreamMessage;
    assert.ok(live.type === 'event' && live.event.instanceId === 'build' && live.event.cursor === 4);
    subscription.close();
    assert.equal((await messages.next()).done, true);
    assert.equal(subscription.closed, true);
  });

  test('from the head, ready comes at once, then only what commits', async () => {
    const served = serve();
    const client = served.client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    const controller = new AbortController();
    const received: StreamMessage[] = [];
    const reading = (async () => {
      for await (const message of client.events.subscribe({ after: 'head', signal: controller.signal })) {
        received.push(message);
        if (message.type === 'ready') {
          await client.instances.invoke('Task', 'plan', 'increment');
        }
        if (message.type === 'event') {
          controller.abort();
        }
      }
    })();
    await reading;
    assert.deepEqual(
      received.map((message) => (message.type === 'event' ? message.event.cursor : `ready ${message.cursor}`)),
      ['ready 3', 4]
    );
  });

  test('moves its cursor past the events its filters drop, without handing them out', async () => {
    const served = serve({ stream: { heartbeatMs: 20 } });
    const client = served.client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    await client.instances.invoke('Task', 'plan', 'increment');
    const subscription = client.events.subscribe({ after: 0, exclude: ['increment'] });
    const messages = subscription[Symbol.asyncIterator]();
    const kinds: string[] = [];
    for (;;) {
      const message = (await messages.next()).value as StreamMessage;
      if (message.type === 'ready') {
        assert.equal(message.cursor, 4);
        break;
      }
      kinds.push(message.event.kind);
    }
    assert.deepEqual(kinds, ['define', 'publish', 'create']);
    assert.equal(subscription.cursor, 4);
    // A dropped event while it waits: the next heartbeat carries its cursor.
    const next = messages.next();
    await client.instances.invoke('Task', 'plan', 'increment');
    await until(() => subscription.cursor === 5, 'the cursor past the dropped increment');
    await client.instances.invoke('Task', 'plan', 'transition', { to: 'doing' });
    const kept = (await next).value as StreamMessage;
    assert.ok(kept.type === 'event' && kept.event.cursor === 6);
    subscription.close();
  });

  test('resumes by Last-Event-ID after a dropped connection, neither repeating nor skipping an event', async () => {
    const served = serve();
    let dropped = false;
    // The first stream breaks right after its ready, as a lost connection does.
    const fetch = async (input: string, init: RequestInit): Promise<Response> => {
      const response = await served.fetch(input, init);
      if (dropped || new Headers(init.headers).get('accept') !== 'text/event-stream' || response.body === null) {
        return response;
      }
      dropped = true;
      const source = response.body.getReader();
      let breakNext = false;
      const body = new ReadableStream<Uint8Array>({
        async pull(controller) {
          if (breakNext) {
            await source.cancel();
            controller.error(new TypeError('network connection lost'));
            return;
          }
          const { done, value } = await source.read();
          if (done) {
            controller.close();
            return;
          }
          controller.enqueue(value);
          breakNext = new TextDecoder().decode(value).includes('event: ready');
        },
      });
      return new Response(body, { status: response.status, headers: response.headers });
    };
    const client = served.client({ fetch });
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    const reconnects: unknown[] = [];
    const events: EngineEvent[] = [];
    const readies: number[] = [];
    const subscription = client.events.subscribe({
      after: 0,
      reconnect: { initialMs: 30, maxMs: 30 },
      onReconnect: (error) => reconnects.push(error),
    });
    const reading = (async () => {
      for await (const message of subscription) {
        if (message.type === 'ready') {
          readies.push(message.cursor);
          if (readies.length === 1) {
            // Committed while the connection is down.
            await client.instances.create('Task', { title: 'Build' }, { id: 'build' });
          } else {
            subscription.close();
          }
        } else {
          events.push(message.event);
        }
      }
    })();
    await reading;
    assert.deepEqual(
      events.map((event) => [event.cursor, event.kind, event.instanceId]),
      [
        [1, 'define', null],
        [2, 'publish', null],
        [3, 'create', 'plan'],
        [4, 'create', 'build'],
      ]
    );
    assert.deepEqual(readies, [3, 4]);
    assert.equal(reconnects.length, 1);
    const streams = served.requests.filter((request) => request.headers.get('accept') === 'text/event-stream');
    assert.deepEqual(
      streams.map((request) => [request.url, request.headers.get('last-event-id')]),
      [
        ['/api/namespaces/default/events?after=0', null],
        ['/api/namespaces/default/events?after=0', '3'],
      ]
    );
  });

  test('a refusal ends the subscription with its problem, and is not retried', async () => {
    const served = serve();
    const outsider = served.client({ auth: { token: 'outsider' } });
    const subscription = outsider.events.subscribe({ after: 'head', schema: 'Task', reconnect: { initialMs: 1, maxMs: 1 } });
    await refused(
      (async () => {
        for await (const _message of subscription) {
          assert.fail('no message reaches a refused caller');
        }
      })(),
      403,
      'forbidden'
    );
    assert.equal(served.requests.length, 1);
    assert.equal(subscription.closed, true);
  });

  test('from the start it replays what retention kept; a resume from a cursor retention passed ends it with cursor_expired', async () => {
    const served = serve({}, { retention: { maxEvents: 1 } });
    const client = served.client();
    await client.instances.create('Task', { title: 'Plan' }, { id: 'plan' });
    served.engine.runner.prune();
    const head = served.engine.events.head();
    const fromStart = client.events.subscribe({ schema: 'Task' });
    const messages = fromStart[Symbol.asyncIterator]();
    const first = (await messages.next()).value as StreamMessage;
    assert.ok(first.type === 'event' && first.event.cursor === head);
    assert.deepEqual((await messages.next()).value, { type: 'ready', cursor: head });
    fromStart.close();

    const resumed = client.events.subscribe({ after: 1, reconnect: { initialMs: 1, maxMs: 1 } });
    const expired = await refused(
      (async () => {
        for await (const _message of resumed) {
          assert.fail('no message comes from before the floor');
        }
      })(),
      410,
      'cursor_expired'
    );
    assert.deepEqual([expired.floor, expired.head], [served.engine.events.floor(), head]);
    assert.equal(resumed.closed, true);
  });
});

describe('reconcile', () => {
  test('runs the handler per event from the head, and a new run resumes from the stored cursor', async () => {
    const served = serve();
    const client = served.client();
    await client.instances.create('Task', { title: 'Before' }, { id: 'before' });
    const store = memoryCursor();
    const seen: string[] = [];
    let ready = 0;
    const options = {
      schema: 'Task',
      kinds: ['create'] as const,
      cursor: store,
      handle: (event: EngineEvent) => {
        seen.push(event.instanceId as string);
      },
      onReady: () => {
        ready++;
      },
    };
    const first = reconcile(client, options);
    await until(() => ready === 1, 'the first run to catch up');
    await client.instances.create('Task', { title: 'A' }, { id: 'a' });
    await client.instances.create('Task', { title: 'B' }, { id: 'b' });
    await until(() => seen.length === 2, 'a and b');
    await first.stop();
    assert.deepEqual(seen, ['a', 'b']);
    assert.equal(store.value, first.cursor);

    // Committed while no reconciler runs.
    await client.instances.create('Task', { title: 'C' }, { id: 'c' });
    await client.instances.invoke('Task', 'c', 'increment');
    const second = reconcile(client, options);
    await until(() => seen.length === 3, 'c');
    await client.instances.create('Task', { title: 'D' }, { id: 'd' });
    await until(() => seen.length === 4, 'd');
    await second.stop();
    assert.deepEqual(seen, ['a', 'b', 'c', 'd']);
    const last = served.engine.events.head();
    assert.equal(store.value, last);
  });

  test('retries a failing handler on the same event, in order, and never skips it', async () => {
    const served = serve();
    const client = served.client();
    const seen: string[] = [];
    const failures: Array<[string, number]> = [];
    let failed = false;
    const reconciler = reconcile(client, {
      schema: 'Task',
      kinds: ['create'],
      start: 0,
      retry: { initialMs: 5, maxMs: 5 },
      handle: (event) => {
        if (event.instanceId === 'b' && !failed) {
          failed = true;
          throw new Error('the downstream is down');
        }
        seen.push(event.instanceId as string);
      },
      onError: (error, event, attempt) => {
        failures.push([event?.instanceId ?? '', attempt]);
        assert.match(String(error), /downstream/u);
      },
    });
    for (const id of ['a', 'b', 'c']) {
      await client.instances.create('Task', { title: id }, { id });
    }
    await until(() => seen.length === 3, 'every create');
    await reconciler.stop();
    assert.deepEqual(seen, ['a', 'b', 'c']);
    assert.deepEqual(failures, [['b', 1]]);
  });

  test('a ready saves the cursor past events the filters dropped, so a restart does not scan them again', async () => {
    const served = serve();
    const client = served.client();
    for (const id of ['a', 'b']) {
      await client.instances.create('Task', { title: id }, { id });
    }
    const store = memoryCursor();
    let ready = 0;
    const reconciler = reconcile(client, { kinds: ['delete'], start: 0, cursor: store, handle: () => assert.fail('no delete'), onReady: () => ready++ });
    await until(() => ready === 1, 'the replay to catch up');
    await reconciler.stop();
    assert.equal(store.value, served.engine.events.head());
  });

  test('a refused stream ends the reconciler, and stop reports it', async () => {
    const served = serve();
    const reconciler = reconcile(served.client({ auth: { token: 'outsider' } }), { schema: 'Task', handle: () => undefined });
    await refused(reconciler.done, 403, 'forbidden');
    await refused(reconciler.stop(), 403, 'forbidden');
  });
});
