// Comments' deliveries and inbox over HTTP (D77, step 3), end to end
// through the client's behaviors.invoke: an author mentions an agent and a
// person; an agent dispatcher, holding the type's deliverPermission,
// claims the deliveries of `agent:` subjects, the agent replies in the
// thread as itself, and the dispatcher settles the delivery sent; the
// person's inbox lists the mention and the thread, and each leaves the
// unread inbox once the person marks it read, after which a claim drops
// the person's deliveries as read rather than handing them over. On
// SQLite, and with ENGINE_TEST_PG_URL set on Postgres through
// openEngineAsync.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Authenticator } from '@superschematic/http-runtime';
import { Hono } from 'hono';

import { EngineClient, EngineProblem } from '../dist/client/index.js';
import type { Engine } from '../dist/index.js';
import { engineApp } from '../dist/http/index.js';
import { alice, backends, cleanup, schemaDocument } from './helpers.ts';

afterEach(cleanup);

// The bearer token is the caller's subject, but `ada`'s, which is
// agent:ada; the dispatcher holds the permission Review's config names.
const authenticate: Authenticator = async (ctx) =>
  ctx.bearerToken
    ? { subject: ctx.bearerToken === 'ada' ? 'agent:ada' : ctx.bearerToken, permissions: ctx.bearerToken === 'dispatcher' ? ['comments.dispatch'] : [] }
    : null;

type Message = { id: number; stand?: number };
type Delivery = { id: string; schema: string; instanceId: string; message: number; thread?: number; subject: string; reasons: string[]; author: string; body: string; attempt: number };
type Claim = { token: string | null; expiresAt: number | null; items: Delivery[] };
type Inbox = { items: Array<{ kind: string; message: number; thread?: number; unread: number }>; next: string | null };

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

for (const backend of backends) {
  describe(backend.name === 'sqlite' ? 'Comments deliveries over HTTP' : `Comments deliveries over HTTP (${backend.name})`, () => {
    test('an agent dispatcher claims and settles the mention of an agent, and a mention leaves the inbox once its reader marks it read', async () => {
      let now = 1000;
      const engine = await backend.open({ clock: () => now });
      const document = schemaDocument('Review', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
        types: { Review: Record<string, unknown> };
      };
      document.types.Review.behaviors = [{ name: 'Comments', config: { deliverPermission: 'comments.dispatch' } }];
      await engine.schemas.define(alice, document);
      await engine.schemas.publish(alice, 'Review');
      await engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });

      const author = clientOf(engine, 'alice');
      const dispatcher = clientOf(engine, 'dispatcher');
      const agent = clientOf(engine, 'ada');
      const reader = clientOf(engine, 'bob');
      const invoke = <R>(client: EngineClient, operation: string, params: Record<string, unknown> = {}) =>
        client.instances.invoke<R>('Review', 'r1', operation, params as never);
      const comments = <R>(client: EngineClient, operation: string, params: Record<string, unknown> = {}) =>
        client.behaviors.invoke<R>('Comments', operation, params as never);

      const top = await invoke<Message>(author, 'post', { body: 'Can someone check the numbers?', mentions: ['agent:ada', 'bob'] });
      // The dispatcher claims what agents should hear of, and nothing else.
      now = 2000;
      const claim = await comments<Claim>(dispatcher, 'claimDeliveries', { prefix: 'agent:' });
      assert.equal(claim.expiresAt, 62_000);
      assert.deepEqual(
        claim.items.map((item) => [item.schema, item.instanceId, item.message, item.subject, item.reasons, item.author, item.body, item.attempt]),
        [['Review', 'r1', top.id, 'agent:ada', ['mention'], 'alice', 'Can someone check the numbers?', 1]]
      );
      // The agent answers in the thread, as itself; then the dispatcher
      // settles the delivery.
      now = 3000;
      const reply = await invoke<Message>(agent, 'post', { body: 'The numbers add up.', thread: top.id });
      assert.deepEqual(await comments(dispatcher, 'settleDeliveries', { token: claim.token, ids: [claim.items[0]!.id], outcome: 'sent' }), {
        items: [{ id: claim.items[0]!.id, state: 'sent', attempts: 1 }],
      });
      // The agent's own reply is never delivered to it.
      assert.deepEqual(await comments<Claim>(dispatcher, 'claimDeliveries', { prefix: 'agent:' }), { token: null, expiresAt: null, items: [] });
      // Settling it again is refused: the token is spent.
      const stale = await comments(dispatcher, 'settleDeliveries', { token: claim.token, ids: [claim.items[0]!.id], outcome: 'sent' }).catch((error: unknown) => error);
      assert.ok(stale instanceof EngineProblem);
      assert.deepEqual([stale.status, stale.code], [409, 'vetoed']);
      // A caller without the permission may not claim.
      const refused = await comments(reader, 'claimDeliveries').catch((error: unknown) => error);
      assert.ok(refused instanceof EngineProblem);
      assert.deepEqual([refused.status, refused.code], [403, 'forbidden']);

      // bob was mentioned, so he follows the thread: the agent's reply is
      // in his inbox beside the mention.
      const unread = async () => (await comments<Inbox>(reader, 'inbox', { unread: true })).items.map((item) => [item.kind, item.message, item.unread]);
      assert.deepEqual(await unread(), [
        ['thread', reply.id, 1],
        ['mention', top.id, 1],
      ]);
      assert.deepEqual(await invoke(reader, 'markRead', { upTo: top.id }), { readUpTo: top.stand, unread: 0 });
      assert.deepEqual(await unread(), [['thread', reply.id, 1]]);
      assert.deepEqual(await invoke(reader, 'markRead', { thread: top.id, upTo: reply.id }), { readUpTo: reply.stand, unread: 0 });
      assert.deepEqual(await unread(), []);
      // Done items stay; his markRead made the conversation his too.
      const done = await comments<Inbox>(reader, 'inbox', { unread: false });
      assert.deepEqual(
        done.items.map((item) => [item.kind, item.message, item.unread]),
        [
          ['thread', reply.id, 0],
          ['mention', top.id, 0],
          ['conversation', top.id, 0],
        ]
      );
      // A claim of bob's deliveries drops them: he has read both.
      assert.deepEqual((await comments<Claim>(dispatcher, 'claimDeliveries', { subjects: ['bob'] })).items, []);
    });
  });
}
