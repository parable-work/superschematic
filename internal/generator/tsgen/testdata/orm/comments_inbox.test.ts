// fixture-comments-inbox-db end to end (#713, D153, D154, D157): tickets,
// some private to their owner, whose Comments emits its mentions and
// replies for the notifier, and the notifier's tables and dispatch queue.
// TestCommentsInboxEndToEnd generates the package, type-checks it, applies
// the generated create.sql in a schema of its own on Postgres and runs
// these cases through the generated repositories: each event the core
// writes to the ticket's event queue is claimed and handed to the
// notifier in the transaction that spends its claim, as a behavior
// worker's Workers method does, and the notifier's routes reach the
// subjects each event names who may read the ticket.
import { afterEach, describe, expect, test } from 'bun:test';
import { createNotifier, type Notifier } from '@superschematic/http-runtime/postgres';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import type { FixtureCommentsInboxDbOrm } from './orm';
import { noticeWorkQueue, ticketEventQueue } from './queues';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';

interface World {
  db: FeatureDatabase;
  orm: FixtureCommentsInboxDbOrm;
  notifier: Notifier;
  queue: WorkerQueue;
  ann: string;
  ben: string;
  cal: string;
  /** A ticket of Ann's that everyone reads. */
  open: string;
  /** A ticket of Ann's that she alone reads. */
  closed: string;
}

async function world(): Promise<World> {
  const db = await featureDatabase('comments_inbox');
  const orm = (await ormEntry<FixtureCommentsInboxDbOrm>()).createOrm(db.source);
  const sys = orm.asSystem();
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue });
  const ann = String((await sys.account.createOne({ email: 'ann@example.test' })).id);
  const ben = String((await sys.account.createOne({ email: 'ben@example.test' })).id);
  const cal = String((await sys.account.createOne({ email: 'cal@example.test' })).id);
  const open = String((await sys.ticket.createOne({ title: 'Boiler pressure low', owner: ann })).id);
  const closed = String((await sys.ticket.createOne({ title: 'Annual review', owner: ann, audience: 'private' })).id);
  // The app's routes: a mention and a reply each reach the subjects the
  // event names (#713).
  await sys.noticeRoute.createOne({ name: 'Mentions', eventKind: 'comments.mentioned', recipients: [{ payload: 'subjects' }] });
  await sys.noticeRoute.createOne({ name: 'Replies', eventKind: 'comments.replied', urgency: 'digest', recipients: [{ payload: 'subjects' }] });
  return { db, orm, notifier, queue: notifier.workerQueue(db.source), ann, ben, cal, open, closed };
}

/** The repositories acting as a user. */
function as(w: World, user: string) {
  return w.orm.withUser(user);
}

/**
 * Hands every event the tickets' Comments emitted to the notifier, as a
 * behavior worker's Workers method does, each in the transaction that
 * spends its claim, then runs the notifier's dispatch queue until it is
 * empty. Resolves to the kinds handed, in order.
 */
async function deliver(w: World): Promise<string[]> {
  const kinds: string[] = [];
  const events = w.db.queue(ticketEventQueue);
  for (let round = 0; round < 20; round++) {
    const claims = await events.claim(20, 60);
    if (claims.length === 0) break;
    for (const claim of claims) {
      const m = claim.message as any;
      kinds.push(m.kind);
      await w.orm.transaction(tx =>
        tx.ticket.runEvent({ rowKey: m.rowKey, useId: m.useId, kind: m.kind, at: m.at, payload: m.payload }, { claim: { id: claim.id, token: claim.token } }, async () => {
          await w.notifier.fromBehaviorEvent(tx, 'Ticket', 'comments', { kind: m.kind, key: String(m.rowKey), payload: m.payload });
        })
      );
    }
  }
  for (let round = 0; round < 50; round++) {
    const claims = await w.queue.claim(20, 30);
    if (claims.length === 0) return kinds;
    for (const claim of claims) {
      await claim.handle(new AbortController().signal);
      await w.queue.complete(claim.id, claim.token);
    }
  }
  throw new Error('the dispatch queue did not drain');
}

/** The user's notices, oldest first: each one's kind, its ticket and its message. */
async function noticesOf(w: World, user: string): Promise<[string, string, number][]> {
  const notices = await w.notifier.inbox({ kind: 'user', id: user });
  return notices
    .map(n => [String(n.eventKind), String(n.subjectKey), Number((n.payload as any).message)] as [string, string, number])
    .reverse();
}

describe.skipIf(!available)('comments in the inbox', () => {
  let w: World | undefined;
  afterEach(async () => {
    await w?.db.drop();
    w = undefined;
  });

  test("a mention reaches the mentioned principal's inbox, and a principal who cannot read the row is not notified", async () => {
    w = await world();
    const posted = await as(w, w.ann).ticket.comments.post(w.open, { body: 'Can you look at the gauge?', mentions: [w.ben, w.cal, w.ann] });
    // Ben may not read the closed ticket: mentioned there, he hears nothing.
    await as(w, w.ann).ticket.comments.post(w.closed, { body: 'Notes for the review', mentions: [w.ben] });
    expect(await deliver(w)).toEqual(['mentioned', 'mentioned']);
    expect(await noticesOf(w, w.ben)).toEqual([['comments.mentioned', w.open, (posted as any).id]]);
    expect(await noticesOf(w, w.cal)).toEqual([['comments.mentioned', w.open, (posted as any).id]]);
    // The author is never among a message's subjects.
    expect(await noticesOf(w, w.ann)).toEqual([]);
    const [notice] = await w.notifier.inbox({ kind: 'user', id: w.ben });
    expect(notice!.subjectTable).toBe('Ticket');
    expect(notice!.payload).toMatchObject({ author: w.ann, subjects: [w.ben, w.cal], excerpt: 'Can you look at the gauge?' });
    // Nothing records that Ben was skipped on the closed ticket.
    expect((await w.orm.asSystem().notice.findMany({ subjectKey: { eq: w.closed } })).rows).toEqual([]);
  });

  test("a reply reaches the thread's followers, a follower it mentions once, and never its author", async () => {
    w = await world();
    const top = (await as(w, w.ben).ticket.comments.post(w.open, { body: 'The pump hums.' })) as any;
    const second = (await as(w, w.ann).ticket.comments.post(w.open, { body: 'Since when?', thread: top.id })) as any;
    const third = (await as(w, w.cal).ticket.comments.post(w.open, { body: 'Since Monday.', thread: top.id, mentions: [w.ann] })) as any;
    expect(await deliver(w)).toEqual(['replied', 'mentioned', 'replied']);
    // Ben started the thread: both replies reach him.
    expect(await noticesOf(w, w.ben)).toEqual([
      ['comments.replied', w.open, second.id],
      ['comments.replied', w.open, third.id],
    ]);
    // Ann follows the thread she replied in, and Cal's reply mentions her: one notice, as mentioned.
    expect(await noticesOf(w, w.ann)).toEqual([['comments.mentioned', w.open, third.id]]);
    expect(await noticesOf(w, w.cal)).toEqual([]);
    const replied = (await w.notifier.inbox({ kind: 'user', id: w.ben }))[0]!;
    expect(replied.urgency).toBe('digest');
    expect(replied.payload).toMatchObject({ thread: top.id, author: w.cal, subjects: [w.ben] });
  });

  test("markRead clears a reader's unread, and follow turns a thread's replies on and off", async () => {
    w = await world();
    const cal = as(w, w.cal).ticket.comments;
    const first = (await as(w, w.ann).ticket.comments.post(w.open, { body: 'Gauge reads 0.8 bar.' })) as any;
    // No marker yet: nothing counts as unread until Cal first reads.
    expect(await cal.view(w.open)).toEqual({ unread: 0 });
    expect(await cal.markRead(w.open, { upTo: first.id })).toEqual({ readUpTo: 1, unread: 0 });
    const second = (await as(w, w.ann).ticket.comments.post(w.open, { body: 'Refilled to 1.4.' })) as any;
    const third = (await as(w, w.ann).ticket.comments.post(w.open, { body: 'Holding.' })) as any;
    expect(await cal.view(w.open)).toEqual({ unread: 2 });
    // Ann wrote them: none is unread to her.
    expect(await as(w, w.ann).ticket.comments.view(w.open)).toEqual({ unread: 0 });
    expect(await cal.markRead(w.open, { upTo: second.id })).toEqual({ readUpTo: 2, unread: 1 });
    expect(await cal.markRead(w.open, { upTo: third.id })).toEqual({ readUpTo: 3, unread: 0 });
    expect(await cal.view(w.open)).toEqual({ unread: 0 });
    // The view reads as its caller: Ben may not read the closed ticket.
    await expect(as(w, w.ben).ticket.comments.view(w.closed)).rejects.toThrow();

    expect(await cal.follow(w.open, { thread: first.id, on: true })).toEqual({ thread: first.id, following: true });
    const reply = (await as(w, w.ben).ticket.comments.post(w.open, { body: 'Valve was shut.', thread: first.id })) as any;
    expect(await cal.follow(w.open, { thread: first.id, on: false })).toEqual({ thread: first.id, following: false });
    const later = (await as(w, w.ben).ticket.comments.post(w.open, { body: 'Opened it.', thread: first.id })) as any;
    await deliver(w);
    expect(await noticesOf(w, w.cal)).toEqual([['comments.replied', w.open, reply.id]]);
    expect(await noticesOf(w, w.ann)).toEqual([
      ['comments.replied', w.open, reply.id],
      ['comments.replied', w.open, later.id],
    ]);
  });

  test('a message posted for a sender over a channel records its via and its for', async () => {
    w = await world();
    // An agent run writes in Ben's name a text Ben sent over SMS.
    const agent = w.orm.withUser(w.ann).withBehaviorCaller({ subject: 'run-1', kind: 'agent', actingFor: w.ben, channel: 'sms', permissions: [] });
    const posted = (await agent.ticket.comments.post(w.open, { body: 'Running late, there by 4.', mentions: [w.cal, w.ben] })) as any;
    expect(posted).toMatchObject({ author: 'run-1', for: w.ben, via: 'sms' });
    const read = (await as(w, w.cal).ticket.comments.getMessage(w.open, { message: posted.id })) as any;
    expect(read).toMatchObject({ author: 'run-1', for: w.ben, via: 'sms' });
    const stored = await w.db.raw('SELECT author, for_subject, via FROM ticket_comments_messages');
    expect(stored.rows).toEqual([{ author: 'run-1', for_subject: w.ben, via: 'sms' }]);
    // A call over the API names no channel.
    const plain = (await as(w, w.ann).ticket.comments.post(w.open, { body: 'Thanks.' })) as any;
    expect(plain.via).toBeUndefined();
    expect(plain.for).toBeUndefined();
    // Ben, whom it was written for, hears nothing of it; Cal does, with its via and for.
    await deliver(w);
    expect(await noticesOf(w, w.ben)).toEqual([]);
    const [notice] = await w.notifier.inbox({ kind: 'user', id: w.cal });
    expect(notice!.payload).toMatchObject({ author: 'run-1', forSubject: w.ben, via: 'sms', subjects: [w.cal] });
  });
});
