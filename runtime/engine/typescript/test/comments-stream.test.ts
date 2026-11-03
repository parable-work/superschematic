// Comments' streamed replies over HTTP (D77, step 2): an author begins a
// message, appends to it and finishes it through the client, and a reader
// with the client's `signals: true` subscription follows it as "What a
// reader does with draft" in comments-contract.md says: it opens the
// stream, reads the message, then appends each draft signal at the length
// it holds (in code points, `Array.from(text).length`), ignores one below
// it, and takes the record from the finish event. A reader sends typing
// and viewing through client.signals.send; draft is refused to a caller.
// On SQLite, and with ENGINE_TEST_PG_URL set on Postgres through
// openEngineAsync.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { EngineClient, EngineProblem, type SignalMessage, type StreamMessage } from '../dist/client/index.js';
import type { Engine, EngineSignal } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { alice, backends, cleanup, schemaDocument } from './helpers.ts';

afterEach(cleanup);

// The bearer token is the caller's subject.
const authenticate: Authenticator = async (ctx) => (ctx.bearerToken ? { subject: ctx.bearerToken, permissions: [] } : null);

type Message = { id: number; state: string; body: string; createdAt: number; postedAt?: number };

// A client of the engine's app served in process, as `subject`.
function clientOf(engine: Engine, subject: string): EngineClient {
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

// A reader's hold on one composing message: its draft and the draft's
// length in code points.
class Follower {
  draft: string | undefined;
  held = 0;
  body: string | undefined;
  reads = 0;

  hold(record: Message): void {
    if (record.state === 'composing') {
      this.draft = record.body;
      this.held = Array.from(record.body).length;
    } else {
      this.draft = undefined;
      this.body = record.body;
    }
  }

  async heard(message: StreamMessage | SignalMessage, read: (id: number) => Promise<Message>): Promise<void> {
    if (message.type === 'signal') {
      const { name, data } = message.signal as EngineSignal & { data: { message: number; offset: number; text: string } };
      if (name !== 'draft' || this.draft === undefined) {
        return;
      }
      if (data.offset === this.held) {
        this.draft += data.text;
        this.held += Array.from(data.text).length;
      } else if (data.offset > this.held) {
        this.reads += 1;
        this.hold(await read(data.message));
      }
      return;
    }
    if (message.type === 'event') {
      const change = message.event.change as { behavior?: string; operation?: string; result?: Message };
      if (change.behavior === 'Comments' && (change.operation === 'finish' || change.operation === 'interrupt') && change.result) {
        this.hold(change.result);
      }
    }
  }
}

for (const backend of backends) {
  async function open(): Promise<Engine> {
    const engine = await backend.open();
    const document = schemaDocument('Review', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
      types: { Review: Record<string, unknown> };
    };
    document.types.Review.behaviors = [{ name: 'Comments' }];
    await engine.schemas.define(alice, document);
    await engine.schemas.publish(alice, 'Review');
    await engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });
    return engine;
  }

  describe(backend.name === 'sqlite' ? 'Comments streamed over HTTP' : `Comments streamed over HTTP (${backend.name})`, () => {
    test('a reader with signals follows a streamed reply from begin to finish, and typing reaches it', async () => {
      const engine = await open();
      const author = clientOf(engine, 'bob');
      const reader = clientOf(engine, 'carol');
      const invoke = <R>(client: EngineClient, operation: string, params: Record<string, unknown> = {}) =>
        client.instances.invoke<R>('Review', 'r1', operation, params as never);

      const top = await invoke<Message>(author, 'post', { body: 'What do we ship?' });
      const begun = await invoke<Message>(author, 'begin', { thread: top.id });
      assert.equal(begun.state, 'composing');

      // The reader opens the stream with signals before it reads, and
      // before the author appends: on Postgres the listener hands a signal
      // to the streams after its commit returns, so a stream opened just
      // after an append may hear that append's draft or may not.
      const subscription = reader.events.subscribe({ after: 'head', signals: true });
      const messages = subscription[Symbol.asyncIterator]();
      assert.equal((await next(messages)).type, 'ready');
      await invoke(author, 'appendDraft', { message: begun.id, text: 'We ship ' });
      const appended = await invoke<{ message: number; offset: number; length: number }>(author, 'appendDraft', { message: begun.id, text: 'the naïve ' });
      assert.deepEqual(appended, { message: begun.id, offset: 8, length: 18 });
      const follower = new Follower();
      const read = (id: number) => invoke<Message>(reader, 'getMessage', { message: id });
      follower.hold(await read(begun.id));
      assert.deepEqual([follower.draft, follower.held], ['We ship the naïve ', 18]);

      // A typing signal from the reader, through the client.
      assert.equal(await reader.signals.send({ schema: 'Review', instanceId: 'r1', behavior: 'Comments', name: 'typing', data: { thread: top.id } }), undefined);
      for (const text of ['🐉 build', '', ' today.']) {
        await invoke(author, 'appendDraft', { message: begun.id, text });
      }
      // "We ship " and "the naïve " came before the read, which holds
      // them, and are ignored; typing, then the two drafts that append.
      const seen: string[] = [];
      for (let n = 0; n < 5; n += 1) {
        const message = await next(messages);
        if (message.type === 'signal') {
          seen.push(`${message.signal.name} ${message.signal.from} ${JSON.stringify(message.signal.data)}`);
        }
        await follower.heard(message, read);
      }
      assert.deepEqual(seen, [
        `draft bob {"message":${begun.id},"offset":0,"text":"We ship "}`,
        `draft bob {"message":${begun.id},"offset":8,"text":"the naïve "}`,
        `typing carol {"thread":${top.id}}`,
        `draft bob {"message":${begun.id},"offset":18,"text":"🐉 build"}`,
        `draft bob {"message":${begun.id},"offset":25,"text":" today."}`,
      ]);
      assert.equal(follower.draft, 'We ship the naïve 🐉 build today.');
      assert.equal(follower.reads, 0);

      const finished = await invoke<Message>(author, 'finish', { message: begun.id });
      const event = await next(messages);
      assert.equal(event.type, 'event');
      await follower.heard(event, read);
      assert.equal(follower.draft, undefined);
      assert.equal(follower.body, finished.body);
      assert.equal(finished.body, 'We ship the naïve 🐉 build today.');
      assert.deepEqual([finished.state, finished.postedAt !== undefined], ['posted', true]);

      // A caller may not send draft: only appendDraft does.
      const refused = await reader.signals
        .send({ schema: 'Review', instanceId: 'r1', behavior: 'Comments', name: 'draft', data: { message: begun.id, offset: 0, text: 'forged' } })
        .then(
          () => assert.fail('expected 403'),
          (error: unknown) => error
        );
      assert.ok(refused instanceof EngineProblem, String(refused));
      assert.deepEqual([refused.status, refused.code], [403, 'forbidden']);
      await reader.signals.send({ schema: 'Review', instanceId: 'r1', behavior: 'Comments', name: 'viewing' });
      const viewing = await next(messages);
      assert.ok(viewing.type === 'signal' && viewing.signal.name === 'viewing' && viewing.signal.from === 'carol', JSON.stringify(viewing));
      subscription.close();
    });
  });
}
