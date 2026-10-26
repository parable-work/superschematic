// Comments, the core's conversation on an instance (D77, batch 1): post,
// its replies, quotes, blocks and retries; edit and listEdits; deleteMessage
// and purge; reactions and listReactions; pins; resolve and reopen;
// listMessages' four anchors and getMessage; the record's thread members
// and the three volatile fields; the refusals, each with its code and
// details; every write beside the instance, which keeps its seq and
// appends an event of no sequence; the access policy's questions; delete;
// and its rule for a new version. Batch 2, streamed replies: begin,
// appendDraft (its lengths in code points), finish and interrupt, their
// refusals, maxDrafts, the composing and interrupted states, postedAt and
// the composing field, batch 1's operations on a composing message, and
// the runner's schedule interrupting an idle draft. Batch 3, read state:
// stands and markRead, the unread field and readUpTo, follow, the
// deliveries a standing message makes, and the namespace operations
// inbox, claimDeliveries and settleDeliveries through
// engine.behaviors.invoke.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError, IncompatibleChangeError, OperationParamsError, type AccessRequest, type Engine, type Principal } from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown, type TestEngineOptions } from './helpers.ts';
import { testClock } from './runner-fixtures.ts';

afterEach(cleanup);

function reviewSchema(behaviors: Array<{ name: string; config?: unknown }> = [{ name: 'Comments' }]): Record<string, unknown> {
  const document = schemaDocument('Review', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: { Review: Record<string, unknown> };
  };
  if (behaviors.length > 0) {
    document.types.Review.behaviors = behaviors;
  }
  return document;
}

const bob: Principal = { subject: 'bob', permissions: [] };
const carol: Principal = { subject: 'carol', permissions: [] };
const moderator: Principal = { subject: 'mo', permissions: ['comments.moderate', 'comments.pin', 'comments.resolve'] };
const runner: Principal = { subject: 'runner', permissions: [] };

type Message = Record<string, unknown> & { id: number };
type Listing = { items: Message[]; before: number | null; after: number | null; root?: Message };

for (const driver of drivers) {
  // published opens an engine whose Review composes Comments with config,
  // with r1 and r2 created by alice.
  function published(config: Record<string, unknown> = {}, options: Partial<TestEngineOptions> = {}): Engine {
    const engine = openTestEngine({ driver, ...options });
    engine.schemas.define(alice, reviewSchema([{ name: 'Comments', config }]));
    engine.schemas.publish(alice, 'Review');
    engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });
    engine.instances.create(alice, 'Review', { title: 'Q4 plan' }, { id: 'r2' });
    return engine;
  }

  function on(engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}, id = 'r1'): unknown {
    return engine.instances.invoke(who, 'Review', id, operation, params);
  }

  function vetoed(fn: () => unknown): [string | undefined, unknown] {
    const error = thrown(fn, BehaviorVetoError);
    assert.equal(error.behavior, 'Comments');
    return [error.vetoCode, error.vetoDetails];
  }

  function ids(page: unknown): number[] {
    return (page as Listing).items.map((item) => item.id);
  }

  function fields(engine: Engine, id = 'r1'): unknown {
    return engine.instances.get(alice, 'Review', id)?.behaviors.Comments;
  }

  describe(`Comments (${driver})`, () => {
    test('post numbers messages per instance, a reply names its thread, and the fields count them', () => {
      let now = 1000;
      const engine = published({}, { clock: () => now });
      assert.deepEqual(fields(engine), { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
      assert.deepEqual(on(engine, alice, 'post', { body: 'Looks right.' }), {
        id: 1,
        author: 'alice',
        state: 'posted',
        body: 'Looks right.',
        blocks: [],
        mentions: [],
        reactions: {},
        createdAt: 1000,
        postedAt: 1000,
        stand: 1,
        edits: 0,
        pinned: false,
        replies: 0,
        participants: ['alice'],
      });
      now = 2000;
      assert.deepEqual(on(engine, bob, 'post', { body: 'Agreed.', thread: 1, mentions: ['alice', 'role:leads'] }), {
        id: 2,
        thread: 1,
        author: 'bob',
        state: 'posted',
        body: 'Agreed.',
        blocks: [],
        mentions: ['alice', 'role:leads'],
        reactions: {},
        createdAt: 2000,
        postedAt: 2000,
        stand: 2,
        edits: 0,
        pinned: false,
      });
      now = 3000;
      const through: Principal = { subject: 'carol', permissions: [], service: { deployable: 'inbox', serves: [], subject: 'svc-inbox', standsIn: false } };
      const quoting = on(engine, through, 'post', { body: 'Quoting bob.', thread: 1, quotes: 2 }) as Message;
      assert.deepEqual([quoting.id, quoting.quotes, quoting.via], [3, 2, 'inbox']);
      assert.equal((on(engine, alice, 'post', { body: 'Second thread.' }) as Message).id, 4);
      assert.equal((on(engine, alice, 'post', { body: 'First on r2.' }, 'r2') as Message).id, 1);
      const top = on(engine, alice, 'getMessage', { message: 1 }) as Message;
      assert.deepEqual([top.replies, top.lastReplyAt, top.participants], [2, 3000, ['carol', 'bob', 'alice']]);
      assert.deepEqual(fields(engine), { messages: 4, lastMessageAt: 3000, threads: { open: 2, resolved: 0 }, composing: [], unread: 0 });
      assert.deepEqual(
        engine.instances.list(alice, 'Review').items.map((item) => item.behaviors.Comments.messages),
        [4, 1]
      );
    });

    test('post checks in order, and a retry with its clientId returns the message and posts nothing', () => {
      const engine = published({ maxBodyLength: 10 });
      engine.instances.create(alice, 'Review', { title: 'Linked' }, { id: 'r3' });
      const first = on(engine, alice, 'post', { body: 'Hi.', clientId: 'c-1' });
      const head = engine.events.head();
      assert.deepEqual(on(engine, alice, 'post', { body: 'Different, and too long.', clientId: 'c-1' }), first);
      assert.equal(engine.events.head(), head);
      assert.equal((on(engine, bob, 'post', { body: 'Hi.', clientId: 'c-1' }) as Message).id, 2);
      const issues = (params: Record<string, unknown>) => thrown(() => on(engine, alice, 'post', params), OperationParamsError).issues;
      assert.deepEqual(issues({ body: 'Eleven code' }), [{ path: '/body', message: 'must NOT have more than 10 characters' }]);
      assert.deepEqual(issues({ body: 'x', quotes: 1 }), [{ path: '/quotes', message: 'needs thread' }]);
      assert.deepEqual(issues({ body: 'x', blocks: [{ kind: 'action', title: 'Big', status: 'failed', data: { log: 'x'.repeat(8200) } }] }), [
        { path: '/blocks/0/data', message: 'is more than 8192 bytes of JSON' },
      ]);
      for (const params of [{ body: '' }, { body: '  \n ' }, {}, { body: 'x', replyTo: 1 }, { body: 'x', thread: 0 }, { body: 'x', mentions: ['a', 'a'] }]) {
        thrown(() => on(engine, alice, 'post', params), OperationParamsError);
      }
      on(engine, bob, 'post', { body: 'Reply.', thread: 1 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', thread: 9 })), ['no_message', { message: 9 }]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', thread: 3 })), ['not_a_thread', { message: 3, thread: 1 }]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', thread: 1, quotes: 9 })), ['no_message', { message: 9 }]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', thread: 1, quotes: 2 })), ['not_in_thread', { message: 2, thread: 1 }]);
      on(engine, bob, 'deleteMessage', { message: 3 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', thread: 1, quotes: 3 })), ['deleted', { message: 3 }]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', blocks: [{ kind: 'ref', schema: 'Review', id: 'r9' }] })), [
        'ref_not_found',
        { block: 0, schema: 'Review', id: 'r9' },
      ]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'post', { body: 'x', blocks: [{ kind: 'ref', schema: 'Nothing', id: 'x' }] })), [
        'ref_not_found',
        { block: 0, schema: 'Nothing', id: 'x' },
      ]);
      const blocks = [
        { kind: 'ref', schema: 'Review', id: 'r3' },
        { kind: 'action', title: 'Deploy', status: 'succeeded', data: { url: 'https://example.com', n: 1 } },
      ];
      assert.deepEqual((on(engine, alice, 'post', { body: 'See r3.', blocks }) as Message).blocks, blocks);
      // Posting in a deleted top-level message's thread is allowed.
      on(engine, alice, 'deleteMessage', { message: 1 });
      on(engine, carol, 'post', { body: 'Late.', thread: 1 });
      assert.equal((fields(engine) as { messages: number }).messages, 3);
    });

    test('the config limits the block kinds, and a ref block names an instance the caller reads', () => {
      const engine = published({ blocks: ['ref'] });
      assert.deepEqual(
        vetoed(() =>
          on(engine, bob, 'post', {
            body: 'Act.',
            blocks: [
              { kind: 'ref', schema: 'Review', id: 'r2' },
              { kind: 'action', title: 'Run', status: 'running' },
            ],
          })
        ),
        ['block_not_allowed', { block: 1, kind: 'action', allowed: ['ref'] }]
      );
      on(engine, alice, 'post', { body: 'See r2.', blocks: [{ kind: 'ref', schema: 'Review', id: 'r2' }] });
      // An edit that keeps a ref the message holds does not read it again.
      engine.instances.delete(alice, 'Review', 'r2');
      assert.equal((on(engine, alice, 'edit', { message: 1, body: 'See r2, gone.', blocks: [{ kind: 'ref', schema: 'Review', id: 'r2' }] }) as Message).edits, 1);
    });

    test('edit replaces what it gives, and listEdits reads what each edit replaced', () => {
      let now = 1000;
      const engine = published({ editWindowMs: 60000, moderatePermission: 'comments.moderate' }, { clock: () => now });
      on(engine, alice, 'post', { body: 'Draft.', mentions: ['bob'] });
      assert.deepEqual(thrown(() => on(engine, alice, 'edit', { message: 1 }), OperationParamsError).issues, [
        { path: '', message: 'names none of body, blocks and mentions' },
      ]);
      now = 2000;
      const edited = on(engine, alice, 'edit', { message: 1, body: 'Final.' }) as Message;
      assert.deepEqual([edited.body, edited.mentions, edited.editedAt, edited.edits], ['Final.', ['bob'], 2000, 1]);
      now = 3000;
      on(engine, alice, 'edit', { message: 1, mentions: [] });
      assert.deepEqual(on(engine, alice, 'listEdits', { message: 1 }), {
        items: [
          { edit: 1, body: 'Draft.', blocks: [], mentions: ['bob'], by: 'alice', at: 2000 },
          { edit: 2, body: 'Final.', blocks: [], mentions: ['bob'], by: 'alice', at: 3000 },
        ],
        next: null,
      });
      const page = on(engine, alice, 'listEdits', { message: 1, limit: 1 }) as { items: Array<{ edit: number }>; next: string };
      assert.equal((on(engine, alice, 'listEdits', { message: 1, limit: 1, cursor: page.next }) as typeof page).items[0].edit, 2);
      assert.deepEqual(vetoed(() => on(engine, bob, 'edit', { message: 1, body: 'Mine now.' })), ['not_author', { message: 1, author: 'alice' }]);
      now = 100_000;
      assert.deepEqual(vetoed(() => on(engine, alice, 'edit', { message: 1, body: 'Late.' })), [
        'edit_window_passed',
        { message: 1, postedAt: 1000, editWindowMs: 60000 },
      ]);
      assert.equal((on(engine, moderator, 'edit', { message: 1, body: 'Moderated.' }) as Message).body, 'Moderated.');
      assert.deepEqual(vetoed(() => on(engine, alice, 'edit', { message: 7, body: 'x' })), ['no_message', { message: 7 }]);
      on(engine, alice, 'deleteMessage', { message: 1 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'edit', { message: 1, body: 'x' })), ['deleted', { message: 1 }]);
      assert.deepEqual(on(engine, alice, 'listEdits', { message: 1 }), { items: [], next: null });
    });

    test('deleteMessage keeps its place in the thread, and purge removes a thread for good', () => {
      let now = 1000;
      const engine = published({ moderatePermission: 'comments.moderate' }, { clock: () => now });
      on(engine, alice, 'post', { body: 'Top.', clientId: 't' });
      on(engine, bob, 'post', { body: 'Reply.', thread: 1 });
      on(engine, carol, 'post', { body: 'Quoting.', thread: 1, quotes: 2 });
      on(engine, bob, 'react', { message: 1, emoji: ':+1:' });
      on(engine, alice, 'pin', { message: 1 });
      now = 5000;
      on(engine, bob, 'post', { body: 'Other thread.' });
      assert.deepEqual(vetoed(() => on(engine, bob, 'deleteMessage', { message: 1 })), ['not_author', { message: 1, author: 'alice' }]);
      assert.deepEqual(on(engine, alice, 'deleteMessage', { message: 1 }), {
        id: 1,
        author: 'alice',
        state: 'deleted',
        body: '',
        blocks: [],
        mentions: [],
        reactions: {},
        createdAt: 1000,
        postedAt: 1000,
        stand: 1,
        edits: 0,
        pinned: false,
        replies: 2,
        lastReplyAt: 1000,
        participants: ['carol', 'bob'],
      });
      assert.equal((on(engine, alice, 'post', { body: 'Top.', clientId: 't' }) as Message).state, 'deleted');
      assert.deepEqual(vetoed(() => on(engine, alice, 'deleteMessage', { message: 1 })), ['deleted', { message: 1 }]);
      assert.deepEqual(fields(engine), { messages: 3, lastMessageAt: 5000, threads: { open: 1, resolved: 0 }, composing: [], unread: 1 });
      on(engine, moderator, 'deleteMessage', { message: 4 });
      assert.deepEqual(fields(engine), { messages: 2, lastMessageAt: 1000, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
      const refused = thrown(() => on(engine, bob, 'purge', { message: 1 }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not purge on Review r1: it needs permission comments.moderate']);
      assert.deepEqual(vetoed(() => on(engine, moderator, 'purge', { message: 9 })), ['no_message', { message: 9 }]);
      assert.deepEqual(on(engine, moderator, 'purge', { message: 1 }), { message: 1, purged: [1, 2, 3] });
      assert.deepEqual(fields(engine), { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'getMessage', { message: 2 })), ['no_message', { message: 2 }]);
      // Ids are not reused, and a purged message's clientId is free again.
      assert.equal((on(engine, alice, 'post', { body: 'Again.', clientId: 't' }) as Message).id, 5);
    });

    test('purge needs a moderatePermission in the config', () => {
      const engine = published();
      on(engine, alice, 'post', { body: 'Mine.' });
      assert.deepEqual(vetoed(() => on(engine, moderator, 'purge', { message: 1 })), ['not_configured', { permission: 'moderatePermission' }]);
    });

    test('reactions count by emoji, in the order each was first given, and listReactions reads who gave one', () => {
      let now = 1000;
      const engine = published({}, { clock: () => now });
      on(engine, alice, 'post', { body: 'Ship it?' });
      now = 2000;
      on(engine, bob, 'react', { message: 1, emoji: '👍' });
      now = 3000;
      on(engine, carol, 'react', { message: 1, emoji: ':tada:' });
      assert.deepEqual((on(engine, alice, 'react', { message: 1, emoji: '👍' }) as Message).reactions, {
        '👍': { count: 2, mine: true },
        ':tada:': { count: 1, mine: false },
      });
      assert.deepEqual(vetoed(() => on(engine, bob, 'react', { message: 1, emoji: '👍' })), ['already_reacted', { message: 1, emoji: '👍' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'unreact', { message: 1, emoji: ':tada:' })), ['not_reacted', { message: 1, emoji: ':tada:' }]);
      assert.deepEqual(on(engine, alice, 'listReactions', { message: 1, emoji: '👍' }), {
        items: [
          { subject: 'bob', at: 2000 },
          { subject: 'alice', at: 3000 },
        ],
        next: null,
      });
      on(engine, bob, 'unreact', { message: 1, emoji: '👍' });
      on(engine, alice, 'unreact', { message: 1, emoji: '👍' });
      on(engine, bob, 'react', { message: 1, emoji: '👍' });
      assert.deepEqual(Object.keys((on(engine, alice, 'getMessage', { message: 1 }) as Message).reactions as object), [':tada:', '👍']);
      for (let n = 0; n < 18; n += 1) {
        on(engine, alice, 'react', { message: 1, emoji: `e${n}` });
      }
      assert.deepEqual(vetoed(() => on(engine, alice, 'react', { message: 1, emoji: 'e18' })), ['too_many_reactions', { message: 1, max: 20 }]);
      on(engine, carol, 'react', { message: 1, emoji: 'e0' });
      on(engine, alice, 'deleteMessage', { message: 1 });
      assert.deepEqual(vetoed(() => on(engine, bob, 'react', { message: 1, emoji: '👍' })), ['deleted', { message: 1 }]);
    });

    test('pin and unpin, and the permission the config names', () => {
      const engine = published({ pinPermission: 'comments.pin' });
      on(engine, alice, 'post', { body: 'Top.' });
      on(engine, bob, 'post', { body: 'Reply.', thread: 1 });
      const refused = thrown(() => on(engine, alice, 'pin', { message: 2 }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'alice may not pin on Review r1: it needs permission comments.pin']);
      assert.equal((on(engine, moderator, 'pin', { message: 2 }) as Message).pinned, true);
      assert.deepEqual(vetoed(() => on(engine, moderator, 'pin', { message: 2 })), ['already_pinned', { message: 2 }]);
      assert.equal((on(engine, moderator, 'unpin', { message: 2 }) as Message).pinned, false);
      assert.deepEqual(vetoed(() => on(engine, moderator, 'unpin', { message: 2 })), ['not_pinned', { message: 2 }]);
    });

    test('resolve and reopen a thread, and the threads field counts them', () => {
      let now = 1000;
      const engine = published({ resolvePermission: 'comments.resolve' }, { clock: () => now });
      on(engine, alice, 'post', { body: 'Fix the header.' });
      on(engine, bob, 'post', { body: 'Done.', thread: 1 });
      on(engine, bob, 'post', { body: 'And the footer.' });
      now = 4000;
      assert.deepEqual((on(engine, alice, 'resolve', { thread: 1 }) as Message).resolved, { by: 'alice', at: 4000 });
      assert.deepEqual((fields(engine) as { threads: unknown }).threads, { open: 1, resolved: 1 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'resolve', { thread: 1 })), ['already_resolved', { thread: 1, by: 'alice', at: 4000 }]);
      on(engine, carol, 'post', { body: 'Thanks.', thread: 1 });
      assert.deepEqual((on(engine, alice, 'getMessage', { message: 1 }) as Message).resolved, { by: 'alice', at: 4000 });
      assert.deepEqual(vetoed(() => on(engine, alice, 'resolve', { thread: 2 })), ['not_a_thread', { message: 2, thread: 1 }]);
      assert.deepEqual(vetoed(() => on(engine, alice, 'resolve', { thread: 3 })), ['not_author', { message: 3, author: 'bob' }]);
      on(engine, moderator, 'resolve', { thread: 3 });
      assert.equal((on(engine, alice, 'reopen', { thread: 1 }) as Message).resolved, undefined);
      assert.deepEqual(vetoed(() => on(engine, alice, 'reopen', { thread: 1 })), ['not_resolved', { thread: 1 }]);
      assert.deepEqual((fields(engine) as { threads: unknown }).threads, { open: 1, resolved: 1 });
    });

    test('listMessages pages the top-level messages or a thread by its four anchors', () => {
      const engine = published();
      for (let n = 1; n <= 10; n += 1) {
        const { id } = on(engine, alice, 'post', { body: `m${n}` }) as Message;
        if (n === 3) {
          for (let r = 0; r < 4; r += 1) {
            on(engine, bob, 'post', { body: `r${r}`, thread: id });
          }
        }
      }
      const list = (params: Record<string, unknown>) => on(engine, alice, 'listMessages', params) as Listing;
      assert.deepEqual(ids(list({})), [1, 2, 3, 8, 9, 10, 11, 12, 13, 14]);
      const latest = list({ limit: 3 });
      assert.deepEqual([ids(latest), latest.before, latest.after], [[12, 13, 14], 12, null]);
      const older = list({ limit: 3, before: 12 });
      assert.deepEqual([ids(older), older.before, older.after], [[9, 10, 11], 9, 11]);
      const oldest = list({ limit: 3, before: 2 });
      assert.deepEqual([ids(oldest), oldest.before, oldest.after], [[1], null, 1]);
      const newer = list({ limit: 3, after: 3 });
      assert.deepEqual([ids(newer), newer.before, newer.after], [[8, 9, 10], 8, 10]);
      assert.deepEqual(list({ after: 99 }), { items: [], before: null, after: null, readUpTo: 14 });
      const around = list({ limit: 4, around: 9 });
      assert.deepEqual([ids(around), around.before, around.after], [[8, 9, 10, 11], 8, 11]);
      assert.deepEqual(ids(list({ limit: 5, around: 1 })), [1, 2, 3]);
      const thread = list({ thread: 3, limit: 2 });
      assert.deepEqual([ids(thread), thread.before, thread.after, thread.root?.id, thread.root?.replies], [[6, 7], 6, null, 3, 4]);
      assert.deepEqual(thrown(() => list({ around: 2, before: 3 }), OperationParamsError).issues, [{ path: '/before', message: 'must not be given with around' }]);
      assert.deepEqual(thrown(() => list({ before: 2, after: 3 }), OperationParamsError).issues, [{ path: '/after', message: 'must not be given with before' }]);
      assert.deepEqual(vetoed(() => list({ thread: 4 })), ['not_a_thread', { message: 4, thread: 3 }]);
      assert.deepEqual(vetoed(() => list({ around: 4 })), ['no_message', { message: 4 }]);
      assert.deepEqual(on(engine, alice, 'listMessages', {}, 'r2'), { items: [], before: null, after: null });
    });

    test('every write is beside the instance: its seq does not move and its event has none', () => {
      const engine = published({ moderatePermission: 'comments.moderate' });
      const before = engine.instances.get(alice, 'Review', 'r1');
      const posted = on(engine, bob, 'post', { body: 'Hello.' });
      on(engine, alice, 'post', { body: 'Hi.', thread: 1 });
      on(engine, bob, 'edit', { message: 1, body: 'Hello!' });
      on(engine, alice, 'react', { message: 1, emoji: '👋' });
      on(engine, alice, 'unreact', { message: 1, emoji: '👋' });
      on(engine, alice, 'pin', { message: 1 });
      on(engine, alice, 'unpin', { message: 1 });
      on(engine, bob, 'resolve', { thread: 1 });
      on(engine, bob, 'reopen', { thread: 1 });
      on(engine, alice, 'deleteMessage', { message: 2 });
      on(engine, moderator, 'purge', { message: 2 });
      on(engine, alice, 'listMessages');
      on(engine, alice, 'getMessage', { message: 1 });
      const after = engine.instances.get(alice, 'Review', 'r1');
      assert.deepEqual([after?.seq, after?.updatedAt, after?.updatedBy], [before?.seq, before?.updatedAt, before?.updatedBy]);
      const events = engine.events.read(alice, { schema: 'Review', instanceId: 'r1' }).events;
      assert.deepEqual(
        events.slice(1).map((event) => [event.seq, event.beside, (event.change as { operation: string }).operation]),
        ['post', 'post', 'edit', 'react', 'unreact', 'pin', 'unpin', 'resolve', 'reopen', 'deleteMessage', 'purge'].map((operation) => [null, true, operation])
      );
      assert.deepEqual(events[1].change, { behavior: 'Comments', operation: 'post', params: { body: 'Hello.' }, result: posted });
      assert.equal(engine.events.read(alice, { schema: 'Review', instanceId: 'r1', beside: false }).events.length, 1);
      // The tag read before a post is still good after it.
      on(engine, alice, 'post', { body: 'Tagged.' });
      engine.instances.invoke(alice, 'Review', 'r1', 'post', { body: 'Tagged again.' }, { expectedSeq: 1 });
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.seq, 1);
    });

    test('the policy is asked for write to post and for read to list, with the operation named', () => {
      const asked: AccessRequest[] = [];
      const engine = published(
        {},
        {
          policy: (request) => {
            asked.push(request);
            return request.principal.subject === 'alice' || request.action === 'read';
          },
        }
      );
      asked.length = 0;
      on(engine, alice, 'post', { body: 'Mine.' });
      on(engine, bob, 'listMessages');
      assert.equal(thrown(() => on(engine, bob, 'react', { message: 1, emoji: 'x' }), Error).message, 'bob may not call react (write) on Review in namespace default');
      assert.deepEqual(
        asked.map(({ principal, action, operation }) => [principal.subject, action, operation]),
        [
          ['alice', 'write', 'post'],
          ['bob', 'read', 'listMessages'],
          ['bob', 'write', 'react'],
        ]
      );
    });

    test('deleting the instance deletes its conversation, and a re-created one numbers its messages on from it', () => {
      const engine = published();
      on(engine, alice, 'post', { body: 'Hello.' });
      on(engine, bob, 'post', { body: 'Hi.', thread: 1 });
      on(engine, bob, 'react', { message: 1, emoji: 'x' });
      assert.equal(engine.instances.delete(alice, 'Review', 'r1'), true);
      engine.instances.create(alice, 'Review', { title: 'Q3 plan, again' }, { id: 'r1' });
      assert.deepEqual(fields(engine), { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
      assert.deepEqual(on(engine, alice, 'listMessages'), { items: [], before: null, after: null });
      // The instance's events span both lives, so no id is used twice.
      assert.equal((on(engine, alice, 'post', { body: 'Fresh.' }) as Message).id, 3);
    });

    test('Comments joins a schema that has instances, which start with no messages, and does not leave one', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, reviewSchema([]));
      engine.schemas.publish(alice, 'Review');
      engine.instances.create(alice, 'Review', { title: 'Q3 plan' }, { id: 'r1' });
      engine.schemas.define(alice, reviewSchema());
      assert.equal(engine.schemas.publish(alice, 'Review').version, 2);
      const joined = engine.instances.get(alice, 'Review', 'r1');
      assert.deepEqual([joined?.data, joined?.behaviors], [{ title: 'Q3 plan' }, { Comments: { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 } }]);
      engine.instances.invoke(alice, 'Review', 'r1', 'post', { body: 'Late, but here.' });

      const removed = thrown(() => engine.schemas.define(alice, reviewSchema([])), IncompatibleChangeError);
      assert.match(removed.changes[0].message, /the messages its instances have would stay behind with nothing to delete them/);
    });

    test('begin holds a place as composing, appendDraft builds its draft in code points, and finish posts it', () => {
      let now = 1000;
      const engine = published({}, { clock: () => now });
      on(engine, alice, 'post', { body: 'Before.' });
      now = 2000;
      assert.deepEqual(on(engine, bob, 'begin'), {
        id: 2,
        author: 'bob',
        state: 'composing',
        body: '',
        blocks: [],
        mentions: [],
        reactions: {},
        createdAt: 2000,
        edits: 0,
        pinned: false,
        replies: 0,
        participants: [],
      });
      assert.deepEqual(fields(engine), { messages: 1, lastMessageAt: 1000, threads: { open: 1, resolved: 0 }, composing: ['bob'], unread: 0 });
      assert.deepEqual(on(engine, bob, 'appendDraft', { message: 2, text: 'Héllo, ' }), { message: 2, offset: 0, length: 7 });
      // Code points, not UTF-16 units: the emoji is one.
      assert.deepEqual(on(engine, bob, 'appendDraft', { message: 2, text: 'wörld 🙂' }), { message: 2, offset: 7, length: 14 });
      assert.deepEqual(on(engine, bob, 'appendDraft', { message: 2, text: '' }), { message: 2, offset: 14, length: 14 });
      assert.equal((on(engine, carol, 'getMessage', { message: 2 }) as Message).body, 'Héllo, wörld 🙂');
      assert.deepEqual(
        (on(engine, carol, 'listMessages') as Listing).items.map((item) => [item.id, item.state]),
        [
          [1, 'posted'],
          [2, 'composing'],
        ]
      );
      now = 3000;
      const reply = (on(engine, carol, 'begin', { thread: 1, quotes: 1, clientId: 'c-1' }) as Message).id;
      on(engine, carol, 'appendDraft', { message: reply, text: 'Agreed.' });
      assert.deepEqual(fields(engine), { messages: 1, lastMessageAt: 1000, threads: { open: 1, resolved: 0 }, composing: ['bob', 'carol'], unread: 0 });
      now = 4000;
      assert.deepEqual(on(engine, bob, 'finish', { message: 2, mentions: ['alice'] }), {
        id: 2,
        author: 'bob',
        state: 'posted',
        body: 'Héllo, wörld 🙂',
        blocks: [],
        mentions: ['alice'],
        reactions: {},
        createdAt: 2000,
        postedAt: 4000,
        stand: 2,
        edits: 0,
        pinned: false,
        replies: 0,
        participants: ['bob'],
      });
      now = 5000;
      const finished = on(engine, carol, 'finish', { message: reply, body: 'Agreed, with a note.' }) as Message;
      assert.deepEqual([finished.thread, finished.quotes, finished.body, finished.createdAt, finished.postedAt], [1, 1, 'Agreed, with a note.', 3000, 5000]);
      const top = on(engine, alice, 'getMessage', { message: 1 }) as Message;
      assert.deepEqual([top.replies, top.lastReplyAt, top.participants], [1, 5000, ['carol', 'alice']]);
      assert.deepEqual(fields(engine), { messages: 3, lastMessageAt: 5000, threads: { open: 2, resolved: 0 }, composing: [], unread: 1 });
      // begin, finish and interrupt are logged beside the instance;
      // appendDraft appends nothing.
      const events = engine.events.read(alice, { schema: 'Review', instanceId: 'r1', beside: true }).events;
      assert.deepEqual(
        events.map((event) => [event.seq, (event.change as { operation: string }).operation, event.actor]),
        [
          [null, 'post', 'alice'],
          [null, 'begin', 'bob'],
          [null, 'begin', 'carol'],
          [null, 'finish', 'bob'],
          [null, 'finish', 'carol'],
        ]
      );
      assert.equal(((events[3].change as { result: Message }).result as Message).body, 'Héllo, wörld 🙂');
      assert.equal(engine.instances.get(alice, 'Review', 'r1')?.seq, 1);
    });

    test('begin, appendDraft and finish check in order, maxDrafts holds, and a retry returns the message', () => {
      const engine = published({ maxBodyLength: 10, maxDrafts: 2, blocks: ['action'] });
      on(engine, alice, 'post', { body: 'Top.' });
      on(engine, alice, 'post', { body: 'Reply.', thread: 1 });
      const open = (on(engine, carol, 'begin') as Message).id;
      const replying = (on(engine, carol, 'begin', { thread: 1 }) as Message).id;
      assert.deepEqual([open, replying], [3, 4]);
      assert.deepEqual(thrown(() => on(engine, bob, 'begin', { quotes: 1 }), OperationParamsError).issues, [{ path: '/quotes', message: 'needs thread' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'begin', { thread: 9 })), ['no_message', { message: 9 }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'begin', { thread: 2 })), ['not_a_thread', { message: 2, thread: 1 }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'begin', { thread: open })), ['composing', { message: open }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'begin', { thread: 1, quotes: replying })), ['composing', { message: replying }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'post', { body: 'x', thread: open })), ['composing', { message: open }]);
      assert.deepEqual(vetoed(() => on(engine, carol, 'begin')), ['too_many_drafts', { composing: 2, max: 2 }]);
      // A retry returns the message as it is now, past maxDrafts too.
      const first = on(engine, bob, 'begin', { clientId: 'b-1' }) as Message;
      on(engine, bob, 'begin');
      assert.equal((on(engine, bob, 'begin', { clientId: 'b-1' }) as Message).id, first.id);
      assert.equal((on(engine, bob, 'post', { body: 'Other.', clientId: 'b-1' }) as Message).state, 'composing');

      const issues = (operation: string, params: Record<string, unknown>) => thrown(() => on(engine, bob, operation, params), OperationParamsError).issues;
      assert.deepEqual(issues('appendDraft', { message: first.id, text: '"'.repeat(2000) }), [{ path: '/text', message: 'is more than 4000 bytes as a JSON string' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'appendDraft', { message: 99, text: 'x' })), ['no_message', { message: 99 }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'appendDraft', { message: 1, text: 'x' })), ['not_composing', { message: 1, state: 'posted' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'appendDraft', { message: open, text: 'x' })), ['not_author', { message: open, author: 'carol' }]);
      on(engine, bob, 'appendDraft', { message: first.id, text: '🙂🙂🙂🙂🙂' });
      assert.deepEqual(vetoed(() => on(engine, bob, 'appendDraft', { message: first.id, text: '123456' })), [
        'draft_too_long',
        { message: first.id, length: 5, append: 6, maxBodyLength: 10 },
      ]);
      assert.deepEqual(issues('finish', { message: first.id, body: 'Eleven code' }), [{ path: '/body', message: 'must NOT have more than 10 characters' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'finish', { message: open })), ['not_author', { message: open, author: 'carol' }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'finish', { message: first.id, blocks: [{ kind: 'ref', schema: 'Review', id: 'r2' }] })), [
        'block_not_allowed',
        { block: 0, kind: 'ref', allowed: ['action'] },
      ]);
      const blank = (on(engine, alice, 'begin') as Message).id;
      on(engine, alice, 'appendDraft', { message: blank, text: ' \n\t' });
      assert.deepEqual(thrown(() => on(engine, alice, 'finish', { message: blank }), OperationParamsError).issues, [
        { path: '/body', message: 'is required: the draft has no text' },
      ]);
      assert.equal((on(engine, bob, 'finish', { message: first.id }) as Message).body, '🙂🙂🙂🙂🙂');
      // A retry of a finish that committed meets not_composing.
      assert.deepEqual(vetoed(() => on(engine, bob, 'finish', { message: first.id })), ['not_composing', { message: first.id, state: 'posted' }]);
    });

    test('interrupt keeps what was written; its author or a moderator stops it at any time, anyone once it is idle', () => {
      let now = 1000;
      const engine = published({ composeTimeoutMs: 5000, moderatePermission: 'comments.moderate', editWindowMs: 60000 }, { clock: () => now });
      on(engine, alice, 'post', { body: 'Top.' });
      const reply = (on(engine, bob, 'begin', { thread: 1 }) as Message).id;
      on(engine, bob, 'appendDraft', { message: reply, text: 'Half a th' });
      now = 3000;
      assert.deepEqual(vetoed(() => on(engine, carol, 'interrupt', { message: reply })), [
        'not_idle',
        { message: reply, author: 'bob', lastAppendAt: 1000, composeTimeoutMs: 5000 },
      ]);
      assert.deepEqual(on(engine, bob, 'interrupt', { message: reply }), {
        id: 2,
        thread: 1,
        author: 'bob',
        state: 'interrupted',
        body: 'Half a th',
        blocks: [],
        mentions: [],
        reactions: {},
        createdAt: 1000,
        postedAt: 3000,
        stand: 2,
        edits: 0,
        pinned: false,
      });
      assert.deepEqual(fields(engine), { messages: 2, lastMessageAt: 3000, threads: { open: 1, resolved: 0 }, composing: [], unread: 0 });
      // An interrupted message stands: a moderator's edit leaves it so,
      // its author's posts it, and its edit window counts from postedAt.
      assert.equal((on(engine, moderator, 'edit', { message: reply, mentions: ['bob'] }) as Message).state, 'interrupted');
      now = 63_000;
      const edited = on(engine, bob, 'edit', { message: reply, body: 'Half a thought.' }) as Message;
      assert.deepEqual([edited.state, edited.postedAt, edited.edits], ['posted', 3000, 2]);
      now = 63_001;
      assert.deepEqual(vetoed(() => on(engine, bob, 'edit', { message: reply, body: 'Late.' })), [
        'edit_window_passed',
        { message: reply, postedAt: 3000, editWindowMs: 60000 },
      ]);
      // An empty draft interrupted is deleted, and never stood.
      const empty = (on(engine, carol, 'begin') as Message).id;
      const deleted = on(engine, moderator, 'interrupt', { message: empty }) as Message;
      assert.deepEqual([deleted.state, deleted.body, deleted.postedAt], ['deleted', '', undefined]);
      // Idle, anyone the policy lets write may stop it.
      const idle = (on(engine, carol, 'begin') as Message).id;
      on(engine, carol, 'appendDraft', { message: idle, text: 'Gone quiet' });
      now = 66_000;
      // An empty append keeps it from going idle.
      assert.deepEqual(on(engine, carol, 'appendDraft', { message: idle, text: '' }), { message: idle, offset: 10, length: 10 });
      now = 70_999;
      assert.equal(vetoed(() => on(engine, bob, 'interrupt', { message: idle }))[0], 'not_idle');
      now = 71_000;
      assert.equal((on(engine, bob, 'interrupt', { message: idle }) as Message).state, 'interrupted');
      assert.deepEqual(vetoed(() => on(engine, bob, 'interrupt', { message: idle })), ['not_composing', { message: idle, state: 'interrupted' }]);
    });

    test("batch 1's operations refuse a composing message, and deleteMessage and purge drop its draft", () => {
      const engine = published({ moderatePermission: 'comments.moderate' });
      const top = (on(engine, bob, 'begin') as Message).id;
      on(engine, bob, 'appendDraft', { message: top, text: 'Draft.' });
      for (const [operation, params] of [
        ['edit', { message: top, body: 'x' }],
        ['react', { message: top, emoji: 'x' }],
        ['unreact', { message: top, emoji: 'x' }],
        ['pin', { message: top }],
        ['resolve', { thread: top }],
        ['reopen', { thread: top }],
      ] as const) {
        assert.deepEqual(vetoed(() => on(engine, bob, operation, params)), ['composing', { message: top }], operation);
      }
      assert.deepEqual(vetoed(() => on(engine, bob, 'unpin', { message: top })), ['not_pinned', { message: top }]);
      assert.deepEqual((on(engine, bob, 'listMessages', { thread: top }) as Listing).items, []);
      const dropped = on(engine, bob, 'deleteMessage', { message: top }) as Message;
      assert.deepEqual([dropped.state, dropped.body, dropped.postedAt], ['deleted', '', undefined]);
      assert.deepEqual(fields(engine), { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
      const thread = (on(engine, alice, 'post', { body: 'Thread.' }) as Message).id;
      const streaming = (on(engine, carol, 'begin', { thread }) as Message).id;
      on(engine, carol, 'appendDraft', { message: streaming, text: 'Mid-reply' });
      assert.deepEqual(on(engine, moderator, 'purge', { message: thread }), { message: thread, purged: [thread, streaming] });
      assert.deepEqual(fields(engine), { messages: 0, threads: { open: 0, resolved: 0 }, composing: [], unread: 0 });
    });

    test("the runner's interrupt schedule interrupts each draft its author left idle for composeTimeoutMs", () => {
      const clock = testClock(1000);
      const engine = published({ composeTimeoutMs: 5000 }, { clock, runner: { principal: runner } });
      engine.runner.runDue();
      const written = (on(engine, bob, 'begin') as Message).id;
      on(engine, bob, 'appendDraft', { message: written, text: 'Something' });
      const empty = (on(engine, carol, 'begin') as Message).id;
      const busy = (on(engine, alice, 'begin') as Message).id;
      clock.now = 7000;
      on(engine, alice, 'appendDraft', { message: busy, text: 'Still going' });
      for (let pass = 0; pass < 8 && (on(engine, alice, 'getMessage', { message: written }) as Message).state === 'composing'; pass += 1) {
        engine.runner.runDue();
        clock.now += 1250;
      }
      const states = [written, empty, busy].map((id) => (on(engine, alice, 'getMessage', { message: id }) as Message).state);
      assert.deepEqual(states, ['interrupted', 'deleted', 'composing']);
      assert.deepEqual((fields(engine) as { composing: string[] }).composing, ['alice']);
      const interrupts = engine.events
        .read(alice, { schema: 'Review', instanceId: 'r1', beside: true })
        .events.filter((event) => (event.change as { operation: string }).operation === 'interrupt')
        .map((event) => [event.actor, event.cause, (event.change as { params: unknown }).params]);
      assert.deepEqual(interrupts, [
        ['runner', { behavior: 'Comments', schedule: 'interrupt', depth: 1 }, { message: written }],
        ['runner', { behavior: 'Comments', schedule: 'interrupt', depth: 1 }, { message: empty }],
      ]);
    });

    test('markRead counts stands, so a reply finished after a read is unread; unread is the reader’s and follow counts from the latest stand', () => {
      let now = 1000;
      const engine = published({}, { clock: () => now });
      on(engine, alice, 'post', { body: 'One.' });
      const streamed = (on(engine, bob, 'begin') as Message).id;
      on(engine, carol, 'post', { body: 'Three.' });
      const unread = (who: Principal) => (engine.instances.get(who, 'Review', 'r1')?.behaviors.Comments as { unread: number }).unread;
      assert.deepEqual([unread(alice), unread(bob), unread(carol)], [1, 0, 0]);
      assert.deepEqual(on(engine, alice, 'markRead', { upTo: 3 }), { readUpTo: 2, unread: 0 });
      now = 2000;
      const finished = on(engine, bob, 'finish', { message: streamed, body: 'Two.' }) as Message;
      assert.deepEqual([finished.id, finished.postedAt, finished.stand], [2, 2000, 3]);
      assert.equal(unread(alice), 1);
      assert.deepEqual(on(engine, alice, 'markRead', { upTo: 1 }), { readUpTo: 2, unread: 1 });
      assert.equal((on(engine, alice, 'listMessages') as Listing & { readUpTo?: number }).readUpTo, 2);
      assert.deepEqual(on(engine, alice, 'markRead', { upTo: 99 }), { readUpTo: 3, unread: 0 });
      assert.equal((on(engine, bob, 'listMessages', { thread: 1 }) as Listing & { readUpTo?: number }).readUpTo, undefined);
      // A thread's marker counts its replies; follow counts from the
      // conversation's latest stand.
      const reply = (on(engine, carol, 'post', { body: 'Reply.', thread: 1 }) as Message).id;
      assert.deepEqual(on(engine, bob, 'follow', { thread: 1, on: true }), { thread: 1, following: true });
      assert.deepEqual(on(engine, bob, 'markRead', { thread: 1, upTo: reply }), { readUpTo: 4, unread: 0 });
      assert.deepEqual(on(engine, bob, 'follow', { thread: 1, on: false }), { thread: 1, following: false });
      assert.deepEqual(vetoed(() => on(engine, bob, 'markRead', { thread: reply, upTo: 1 })), ['not_a_thread', { message: reply, thread: 1 }]);
      assert.deepEqual(vetoed(() => on(engine, bob, 'follow', { thread: 9, on: true })), ['no_message', { message: 9 }]);
      // Neither appends an event.
      const operations = engine.events
        .read(alice, { schema: 'Review', instanceId: 'r1', beside: true })
        .events.map((event) => (event.change as { operation: string }).operation);
      assert.deepEqual(operations, ['post', 'begin', 'post', 'finish', 'post']);
    });

    test('a standing message delivers to its mentions and its thread’s followers, which a claim takes and a settle marks, and the inbox lists', () => {
      let now = 1000;
      const engine = published({ deliverPermission: 'comments.notify', retryBackoffMs: 1000 }, { clock: () => now });
      const notifier: Principal = { subject: 'notifier', permissions: ['comments.notify'] };
      const invoke = (who: Principal, operation: string, params: Record<string, unknown> = {}) =>
        engine.behaviors.invoke(who, { behavior: 'Comments', operation, params }) as Record<string, any>;
      on(engine, alice, 'post', { body: 'Look,   carol\nand agent:ada.', mentions: ['carol', 'agent:ada', 'role:leads', 'alice'] });
      const claim = invoke(notifier, 'claimDeliveries', { prefix: 'agent:' });
      assert.deepEqual(
        claim.items.map((item: Record<string, unknown>) => [item.subject, item.message, item.reasons, item.attempt]),
        [['agent:ada', 1, ['mention'], 1]]
      );
      assert.deepEqual(claim.expiresAt, 61_000);
      assert.deepEqual(invoke(notifier, 'settleDeliveries', { token: claim.token, ids: [claim.items[0].id], outcome: 'sent' }), {
        items: [{ id: claim.items[0].id, state: 'sent', attempts: 1 }],
      });
      now = 2000;
      on(engine, bob, 'post', { body: 'A reply.', thread: 1, mentions: ['carol'] });
      // carol and agent:ada follow the thread they were mentioned in; carol
      // hears of the reply once, for both reasons.
      const everyone = invoke(notifier, 'claimDeliveries', { limit: 10 });
      assert.deepEqual(
        everyone.items.map((item: Record<string, unknown>) => [item.subject, item.message, item.reasons]),
        [
          ['carol', 1, ['mention']],
          ['role:leads', 1, ['mention']],
          ['agent:ada', 2, ['reply']],
          ['alice', 2, ['reply']],
          ['carol', 2, ['mention', 'reply']],
        ]
      );
      assert.deepEqual(invoke(notifier, 'settleDeliveries', { token: everyone.token, ids: [everyone.items[0].id], outcome: 'failed', error: 'busy' }).items, [
        { id: everyone.items[0].id, state: 'failed', attempts: 1, nextAttemptAt: 3000 },
      ]);
      assert.deepEqual(vetoed(() => invoke(notifier, 'settleDeliveries', { token: 'stale', ids: [everyone.items[1].id], outcome: 'sent' })), ['token_stale', { token: 'stale' }]);
      assert.equal(thrown(() => invoke(bob, 'claimDeliveries'), EngineError).code, 'forbidden');
      // The inbox: carol's mentions, newest first, and the thread she
      // follows.
      const inbox = invoke(carol, 'inbox');
      assert.deepEqual(
        inbox.items.map((item: Record<string, unknown>) => [item.kind, item.message, item.thread, item.excerpt, item.unread]),
        [
          ['mention', 2, 1, 'A reply.', 1],
          ['thread', 2, 1, 'A reply.', 1],
          ['mention', 1, undefined, 'Look, carol and agent:ada.', 1],
        ]
      );
      assert.equal(inbox.next, null);
      const page = invoke(carol, 'inbox', { limit: 1 });
      assert.deepEqual(invoke(carol, 'inbox', { limit: 2, cursor: page.next }).items.map((item: Record<string, unknown>) => item.kind), ['thread', 'mention']);
      on(engine, carol, 'markRead', { thread: 1, upTo: 2 });
      assert.deepEqual(
        invoke(carol, 'inbox', { unread: true }).items.map((item: Record<string, unknown>) => [item.kind, item.message]),
        [['mention', 1]]
      );
      assert.equal(thrown(() => invoke(carol, 'inbox', { cursor: 'nope' }), OperationParamsError).issues[0]?.path, '/cursor');
    });
  });
}
