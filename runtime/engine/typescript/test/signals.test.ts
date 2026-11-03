// Signals (D77): a message about an instance that the engine delivers to
// the event streams opened with signals and keeps nowhere. A caller sends
// one through engine.signals.send, the route POST
// .../instances/{id}/signals/{behavior}/{name} (204) or the client's
// signals.send; a behavior's operation through its context's signal(),
// after its call commits. The client's subscription with signals: true
// hands each out as a `signal` message, which moves no cursor; one
// without hands out none. On SQLite, and with ENGINE_TEST_PG_URL set on
// Postgres through openEngineAsync, with the test addon's test.Chatter.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { EngineClient, EngineProblem, type SignalMessage, type StreamMessage } from '../dist/client/index.js';
import { BehaviorError, EngineError, type BehaviorDeclaration, type Engine, type EngineSignal } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, backends, cleanup, openTestEngine, thrown, type TestEngineOptions } from './helpers.ts';

afterEach(cleanup);

// The bearer token is the caller's subject.
const authenticate: Authenticator = async (ctx) => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: [] } : null);

const typing = { schema: 'Item', instanceId: 'a1', behavior: 'test.Chatter', name: 'typing' } as const;

// A client of the engine's app served in process, as `subject`.
function clientOf(engine: Engine, subject = 'bob'): EngineClient {
  const app = new Hono();
  app.route('/api', engineApp(engine, { authenticate }));
  return new EngineClient({
    baseUrl: 'http://engine.test/api',
    fetch: async (input: string, init: RequestInit) => app.fetch(new Request(input, init)),
    auth: { token: subject },
  });
}

// next takes the subscription's next message, failing after a while.
async function next(messages: AsyncIterator<StreamMessage | SignalMessage>, timeoutMs = 10_000): Promise<StreamMessage | SignalMessage> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('no message in time')), timeoutMs);
  });
  try {
    const result = await Promise.race([messages.next(), timeout]);
    assert.ok(!result.done, 'the subscription ended');
    return result.value;
  } finally {
    clearTimeout(timer);
  }
}

for (const backend of backends) {
  async function open(options: Partial<TestEngineOptions> = {}): Promise<Engine> {
    const engine = await backend.open({ metaSchema: openMetaSchema(), behaviors: ['test.Chatter'], ...options });
    await engine.schemas.define(alice, {
      kind: 'General',
      name: 'Item',
      types: {
        Item: {
          name: 'Item',
          role: 'EmbeddedStruct',
          fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }],
          behaviors: [{ name: 'test.Chatter' }],
        },
      },
    });
    await engine.schemas.publish(alice, 'Item');
    await engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a1' });
    return engine;
  }

  describe(backend.name === 'sqlite' ? 'signals' : `signals (${backend.name})`, () => {
    test("a subscription with signals receives a caller's and an operation's, and they move no cursor", async () => {
      const engine = await open();
      const client = clientOf(engine);
      const subscription = client.events.subscribe({ after: 'head', signals: true });
      const messages = subscription[Symbol.asyncIterator]();
      const ready = await next(messages);
      assert.equal(ready.type, 'ready');
      const cursor = subscription.cursor;

      // Through the client: 204, and the signal comes as a message of its own.
      assert.equal(await client.signals.send({ ...typing, data: { thread: 2 } }), undefined);
      const sent = await next(messages);
      assert.equal(sent.type, 'signal');
      const signal = (sent as { signal: EngineSignal }).signal;
      assert.deepEqual({ ...signal, at: 0 }, {
        namespace: 'default',
        schema: 'Item',
        instanceId: 'a1',
        behavior: 'test.Chatter',
        name: 'typing',
        data: { thread: 2 },
        from: 'bob',
        at: 0,
      });
      assert.equal(typeof signal.at, 'number');
      assert.equal(subscription.cursor, cursor, 'a signal moves no cursor');

      // Through the engine, and from an operation after its commit, its
      // beside event beside it.
      await engine.signals.send(alice, { ...typing, name: 'nudge', data: [1] });
      await engine.instances.invoke(alice, 'Item', 'a1', 'draft', { text: 'hello' });
      const seen: string[] = [];
      while (seen.length < 3) {
        const message = await next(messages);
        seen.push(message.type === 'signal' ? `${message.signal.name} ${JSON.stringify(message.signal.data)} ${message.signal.from}` : message.type);
      }
      assert.deepEqual(seen.sort(), ['draft {"text":"hello"} alice', 'event', 'nudge [1] alice']);
      assert.equal(subscription.cursor, await engine.events.head());
      subscription.close();
    });

    test('a subscription without signals hands out none, and a send stores nothing', async () => {
      const engine = await open();
      const client = clientOf(engine);
      const subscription = client.events.subscribe({ after: 'head' });
      const messages = subscription[Symbol.asyncIterator]();
      assert.equal((await next(messages)).type, 'ready');
      const head = await engine.events.head();
      await client.signals.send(typing);
      assert.equal(await engine.events.head(), head);
      await engine.instances.create(alice, 'Item', { title: 'Lamp' }, { id: 'a2' });
      const message = await next(messages);
      assert.ok(message.type === 'event' && message.event.instanceId === 'a2', 'the event, and no signal before it');
      subscription.close();
    });

    test('a send is refused what it cannot send, and past its rate 429', async () => {
      const engine = await open({ signals: { perSecond: 2 } });
      const client = clientOf(engine);
      const refuse = async (call: Promise<unknown>, status: number, code: string) => {
        const error = await call.then(
          () => assert.fail(`expected ${status} ${code}`),
          (failure: unknown) => failure
        );
        assert.ok(error instanceof EngineProblem, String(error));
        assert.deepEqual([error.status, error.code], [status, code], error.message);
        return error;
      };
      await refuse(client.signals.send({ ...typing, name: 'draft', data: { text: 'x' } }), 403, 'forbidden');
      await refuse(client.signals.send({ ...typing, name: 'whisper' }), 404, 'not_found');
      await refuse(client.signals.send({ ...typing, instanceId: 'zz' }), 404, 'not_found');
      await refuse(client.signals.send({ ...typing, data: 'not an object' }), 400, 'invalid_argument');
      await refuse(client.signals.send({ ...typing, name: 'nudge', data: 'x'.repeat(4095) }), 400, 'invalid_argument');
      await client.signals.send(typing);
      await client.signals.send(typing);
      const limited = await refuse(client.signals.send(typing), 429, 'rate_limited');
      assert.equal(limited.retryAfter, 1);
      // The engine's own refusal, and another subject's own budget.
      const refused = await (async () => engine.signals.send({ subject: 'bob', permissions: [] }, typing))().then(
        () => assert.fail('expected rate_limited'),
        (error: unknown) => error
      );
      assert.ok(refused instanceof EngineError && refused.code === 'rate_limited', String(refused));
      await engine.signals.send(alice, typing);
    });
  });
}

// A behavior written in TypeScript sends its signals from its operations'
// context, on the test addon's bridge (SQLite).
const echoDeclaration: BehaviorDeclaration = {
  name: 'test.Echo',
  operations: [
    {
      name: 'echo',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['texts'], properties: { texts: { type: 'array', items: { type: 'string' } } } },
      resultSchema: true,
      writes: true,
      beside: true,
    },
    { name: 'garble', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true, beside: true },
  ],
  signals: [{ name: 'said', access: 'operation', paramsSchema: { type: 'object', additionalProperties: false, required: ['text'], properties: { text: { type: 'string' } } } }],
};

const echo = defineBehavior({
  declaration: echoDeclaration,
  operations: {
    echo(context, params) {
      for (const text of (params as { texts: string[] }).texts) {
        context.signal('said', { text });
      }
      return null;
    },
    garble(context) {
      context.signal('said', { text: 7 });
      return null;
    },
  },
});

describe('signals from a behavior written in TypeScript', () => {
  test("an operation's signals come after its commit, in order, and a bad one refuses the call", async () => {
    const engine = openTestEngine({ metaSchema: openMetaSchema(), behaviors: [echo] });
    publishItem(engine, [{ name: 'test.Echo' }]);
    engine.instances.create(alice, 'Item', { title: 'Desk' }, { id: 'a1' });
    const subscription = clientOf(engine).events.subscribe({ after: 'head', signals: true, kinds: ['create'] });
    const messages = subscription[Symbol.asyncIterator]();
    assert.equal((await next(messages)).type, 'ready');
    engine.instances.invoke(alice, 'Item', 'a1', 'echo', { texts: ['one', 'two', 'three'] });
    const said: string[] = [];
    while (said.length < 3) {
      const message = await next(messages);
      if (message.type === 'signal') {
        said.push((message.signal.data as { text: string }).text);
      }
    }
    assert.deepEqual(said, ['one', 'two', 'three']);
    const refused = thrown(() => engine.instances.invoke(alice, 'Item', 'a1', 'garble'), BehaviorError);
    assert.match(refused.message, /\/text/);
    assert.equal(thrown(() => engine.signals.send(alice, { schema: 'Item', instanceId: 'a1', behavior: 'test.Echo', name: 'said', data: { text: 'x' } }), EngineError).code, 'forbidden');
    subscription.close();
  });
});
