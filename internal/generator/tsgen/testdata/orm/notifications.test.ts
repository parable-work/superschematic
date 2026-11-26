// fixture-notifications-db end to end (D153): tickets that raise events,
// some named to a few readers, the notifier's tables, its dispatch queue
// and the channel identities replies resolve through.
// TestNotificationsEndToEnd generates the package, type-checks it, applies
// the generated create.sql in a schema of its own on Postgres and runs
// these cases through the notifier over the generated repositories, with
// the runtime-providers example's SMS and mail relays (D159) as the
// channels, each over a stand-in service the test answers.
import { afterEach, describe, expect, test } from 'bun:test';
import { createHmac } from 'node:crypto';
import { createNotifier, passwordReset, type Notifier, type ReplyAction } from '@superschematic/http-runtime/postgres';
import { providersFrom, SecretValue } from '@superschematic/http-runtime/providers';
import { IdentityService, hashPassword, postgresIdentityStore, verifyPassword } from '@superschematic/http-runtime/identity';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm, type FixtureNotificationsDbOrm } from './orm';
import { noticeWorkQueue, referenceClearedQueue } from './queues';
import { identityDescriptor } from './identity';
import { postgresQueue } from '@superschematic/http-runtime/postgres';
import { database, dsn, type TestDatabase } from './harness';
import { smsRelayA } from './sms-relay-a';
import { mailRelay } from './mail-relay';

const RELAY_TOKEN = 'relay-token';

/** The SMS and mail relays as stand-ins: what each was sent, and switches that make them fail. */
function relays() {
  const sms: { to: string; text: string; id: string }[] = [];
  const mail: { to: string; subject: string; text: string; id: string }[] = [];
  const state = { smsDown: false, failTo: new Set<string>() };
  let n = 0;
  const fetch = (async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input));
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (url.host === 'sms.test') {
      if (url.pathname === '/v1/health') return new Response('{}', { status: state.smsDown ? 503 : 200 });
      if (state.failTo.has(body.to)) return new Response('{}', { status: 500 });
      const id = `sms-${++n}`;
      sms.push({ to: body.to, text: body.text, id });
      return Response.json({ id, status: 'accepted' });
    }
    if (url.host === 'mail.test') {
      if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
      const id = `mail-${++n}`;
      mail.push({ to: body.to, subject: body.subject, text: body.text, id });
      return Response.json({ id });
    }
    return new Response('no such service', { status: 404 });
  }) as typeof globalThis.fetch;
  return { sms, mail, state, fetch };
}

/** A callback from the SMS relay, signed as it signs one. */
function signed(body: unknown): { headers: Record<string, string>; body: string } {
  const text = JSON.stringify(body);
  return { headers: { 'x-relay-signature': createHmac('sha256', RELAY_TOKEN).update(text).digest('hex') }, body: text };
}

const PHONE = { ann: '+15550100001', ben: '+15550100002', cal: '+15550100003' };
const EMAIL = { ann: 'ann@example.test', ben: 'ben@example.test', cal: 'cal@example.test' };

interface World {
  db: TestDatabase;
  orm: FixtureNotificationsDbOrm;
  notifier: Notifier;
  queue: WorkerQueue;
  relay: ReturnType<typeof relays>;
  ann: string;
  ben: string;
  cal: string;
  ticket: string;
}

const markDone: ReplyAction = {
  permission: 'tickets.work',
  describe: 'Marked done',
  async run(ctx) {
    const key = String(ctx.notice.subjectKey);
    const before = await ctx.tx.ticket.getOne(key);
    await ctx.tx.ticket.updateOne(key, { status: 'done' });
    return { status: before.status };
  },
  async undo(ctx, data) {
    await ctx.tx.ticket.updateOne(String(ctx.notice.subjectKey), { status: (data as { status: string }).status });
  },
};

async function world(options: { now?: () => Date; expand?: (input: Record<string, unknown>) => { occurrences: { dueAt: number }[] } } = {}): Promise<World> {
  const db = await database('notifications');
  const orm = createOrm(db.pool);
  const sys = orm.asSystem();
  const relay = relays();
  const providers = providersFrom({
    slots: { SMS: 'channel', MAIL: 'channel' },
    config: {
      SMS: 'sms-relay-a',
      SMS_URL: 'https://sms.test',
      SMS_TOKEN: new SecretValue(RELAY_TOKEN),
      MAIL: 'mail-relay',
      MAIL_URL: 'https://mail.test',
      MAIL_TOKEN: new SecretValue(RELAY_TOKEN),
      MAIL_FROM: 'notices@example.test',
    },
    definitions: [smsRelayA({ fetch: relay.fetch }), mailRelay({ fetch: relay.fetch })],
  });
  const notifier = createNotifier({
    orm,
    dispatch: noticeWorkQueue,
    providers,
    replies: { 'ticket.overdue': { done: markDone }, 'ticket.assigned': { done: markDone } },
    ...(options.now ? { now: options.now } : {}),
    ...(options.expand ? { expand: options.expand } : {}),
  });
  const ann = String((await sys.account.createOne({ email: EMAIL.ann })).id);
  const ben = String((await sys.account.createOne({ email: EMAIL.ben })).id);
  const cal = String((await sys.account.createOne({ email: EMAIL.cal })).id);
  const worker = await sys.role.createOne({ name: 'worker', permissions: ['tickets.work'] });
  for (const user of [ann, ben]) await sys.userRoleGrant.createOne({ userId: user, roleId: worker.id, grantedAt: new Date() });
  // Ann owns the ticket; Ben is named on it; Cal watches it and may not read it.
  const ticket = String((await sys.ticket.createOne({ title: 'Boiler pressure low', status: 'open', owner: ann, watchers: [ben, cal], audience: 'named', audienceNamed: [ben] })).id);
  for (const [who, phone, email] of [[ann, PHONE.ann, EMAIL.ann], [ben, PHONE.ben, EMAIL.ben], [cal, PHONE.cal, EMAIL.cal]] as const) {
    await sys.channelIdentity.createOne({ kind: 'phone', address: phone, principal: who, trust: 'verified', verifiedAt: new Date() });
    await sys.channelIdentity.createOne({ kind: 'email', address: email, principal: who, trust: 'verified', verifiedAt: new Date() });
  }
  await sys.noticePreference.createOne({ principal: null, eventKind: '*', urgency: '*', channels: ['sms', 'email'] });
  await sys.channelTrust.createOne({ channelKind: 'sms', level: 'low', permissions: [] });
  await sys.channelTrust.createOne({ channelKind: 'email', level: 'standard', permissions: null });
  await sys.noticeRoute.createOne({
    name: 'Overdue tickets page their people',
    eventKind: 'ticket.overdue',
    recipients: [{ path: 'owner' }, { path: 'watchers' }],
    escalation: [{ afterSeconds: 600, channels: ['email'] }],
  });
  return { db, orm, notifier, queue: notifier.workerQueue(db.pool), relay, ann, ben, cal, ticket };
}

/** Runs every message of the dispatch queue that is due, until none is. */
async function drain(queue: WorkerQueue): Promise<number> {
  let handled = 0;
  for (let round = 0; round < 50; round++) {
    const claims = await queue.claim(20, 30);
    if (claims.length === 0) return handled;
    for (const claim of claims) {
      await claim.handle(new AbortController().signal);
      await queue.complete(claim.id, claim.token);
      handled++;
    }
  }
  throw new Error('the dispatch queue did not drain');
}

/** Makes the queue's waiting messages of step due now: time passing. */
async function advance(w: World, step: string): Promise<void> {
  await w.db.pool.query(`UPDATE notice_work_queue SET due_at = now() WHERE step = $1 AND state = 'ready'`, [step]);
}

async function overdue(w: World): Promise<string> {
  return w.orm.transaction(tx => w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.overdue' }));
}

async function noticesOf(w: World, user: string) {
  return w.notifier.inbox({ kind: 'user', id: user });
}

describe.skipIf(!dsn)('notifications', () => {
  let w: World;
  afterEach(async () => {
    await w?.db.drop();
  });

  test('an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time', async () => {
    w = await world();
    const event = await overdue(w);
    await drain(w.queue);
    expect(w.relay.sms.map(m => m.to).sort()).toEqual([PHONE.ann, PHONE.ben]);
    expect(w.relay.sms[0]!.text).toContain('Boiler pressure low');
    expect(w.relay.sms[0]!.text).toContain('ACK 1');
    const annNotices = await noticesOf(w, w.ann);
    expect(annNotices.length).toBe(1);
    expect(annNotices[0]!.eventId).toBe(event);
    expect(annNotices[0]!.urgency).toBe('urgent');
    expect(annNotices[0]!.payload).toEqual({ title: 'Boiler pressure low' });
    // A kind the table does not declare is refused, and its transaction with it.
    await expect(w.orm.transaction(tx => w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.lost' }))).rejects.toThrow('declares no event ticket.lost');
    // Cal cannot read the ticket: no notice, no delivery, no trace.
    expect(await noticesOf(w, w.cal)).toEqual([]);
    expect((await w.orm.asSystem().delivery.findMany({ recipient: { eq: w.cal } })).rows).toEqual([]);
    expect(w.relay.mail).toEqual([]);
    // Ten minutes on, nobody has acknowledged: email follows.
    await advance(w, 'escalate');
    await drain(w.queue);
    expect(w.relay.mail.map(m => m.to).sort()).toEqual([EMAIL.ann, EMAIL.ben]);
    const deliveries = (await w.orm.asSystem().delivery.findMany({ recipient: { eq: w.ann } }, { orderBy: [{ field: 'queuedAt' }] })).rows;
    expect(deliveries.map(d => [d.channelKind, d.state, d.attempt])).toEqual([
      ['sms', 'sent', 1],
      ['email', 'sent', 2],
    ]);
  });

  test('a reply of ack acknowledges the notice and stops its escalation, and undo takes it back', async () => {
    w = await world();
    await overdue(w);
    await drain(w.queue);
    const acked = await w.notifier.reply({ channel: 'sms', from: '(555) 010-0001'.replace('(', '+1'), body: 'ack' });
    expect(acked.outcome).toBe('done');
    expect(acked.echo).toContain('UNDO 1');
    expect(w.relay.sms.at(-1)!.to).toBe(PHONE.ann);
    const [notice] = await noticesOf(w, w.ann);
    expect(notice!.state).toBe('acknowledged');
    expect(notice!.acknowledgedVia).toBe('sms');
    await advance(w, 'escalate');
    await drain(w.queue);
    // Ann acknowledged: only Ben's email went out.
    expect(w.relay.mail.map(m => m.to)).toEqual([EMAIL.ben]);
    const undone = await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'undo 1' });
    expect(undone.outcome).toBe('done');
    expect((await noticesOf(w, w.ann))[0]!.state).toBe('unread');
  });

  test('a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text', async () => {
    w = await world();
    // Ann has had 141 notices before this one.
    await w.orm.asSystem().notice.createOne({ recipient: w.ann, reference: 141, eventId: crypto.randomUUID(), eventKind: 'ticket.assigned', urgency: 'record', title: 'Earlier', state: 'read' });
    await overdue(w);
    await drain(w.queue);
    expect(w.relay.sms.find(m => m.to === PHONE.ann)!.text).toContain('#142');
    const done = await w.notifier.reply({ channel: 'email', from: 'Ann@Example.test', body: 'done 142' });
    expect(done.outcome).toBe('done');
    expect(done.echo).toContain('Marked done 142');
    expect(done.echo).toContain('UNDO 142');
    expect((await w.orm.asSystem().ticket.getOne(w.ticket)).status).toBe('done');
    expect(w.relay.mail.at(-1)!.to).toBe(EMAIL.ann);
    const [audit] = (await w.orm.asSystem().reply.findMany({ verb: { eq: 'done' } })).rows;
    expect(audit).toMatchObject({ channelKind: 'email', address: EMAIL.ann, interpreter: 'keyword', verb: 'done', reference: 142, outcome: 'done', body: 'done 142' });
    expect(String(audit!.principal)).toBe(w.ann);
    const notice = (await noticesOf(w, w.ann)).find(n => Number(n.reference) === 142)!;
    expect(notice.state).toBe('done');
    // Undo puts the ticket back as it was.
    expect((await w.notifier.reply({ channel: 'email', from: EMAIL.ann, body: 'undo 142' })).outcome).toBe('done');
    expect((await w.orm.asSystem().ticket.getOne(w.ticket)).status).toBe('open');
  });

  test('a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges', async () => {
    w = await world();
    await overdue(w);
    await drain(w.queue);
    const refused = await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'done 1' });
    expect(refused.outcome).toBe('refused');
    expect(refused.detail).toContain('its trust is low');
    expect((await w.orm.asSystem().ticket.getOne(w.ticket)).status).toBe('open');
    const [audit] = (await w.orm.asSystem().reply.findMany({ outcome: { eq: 'refused' } })).rows;
    expect(audit).toMatchObject({ channelKind: 'sms', verb: 'done', outcome: 'refused' });
    // Ben's email, entered by hand, is trusted only to acknowledge.
    await w.orm.asSystem().channelIdentity.updateMany({ principal: { eq: w.ben }, kind: { eq: 'email' } }, { trust: 'entered' });
    const byHand = await w.notifier.reply({ channel: 'email', from: EMAIL.ben, body: 'done 1' });
    expect(byHand.outcome).toBe('refused');
    expect(byHand.detail).toContain('entered by hand');
    expect((await w.notifier.reply({ channel: 'email', from: EMAIL.ben, body: 'ack 1' })).outcome).toBe('done');
  });

  test('a reply names its notice by reference unless exactly one waits on its sender', async () => {
    w = await world();
    await overdue(w);
    await w.orm.transaction(tx => w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.overdue', id: crypto.randomUUID() }));
    await drain(w.queue);
    const ambiguous = await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'ack' });
    expect(ambiguous.outcome).toBe('ambiguous');
    expect(ambiguous.detail).toContain('#2');
    expect((await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'ack 2' })).outcome).toBe('done');
    // One waits now: ack alone takes it.
    const one = await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'OK' });
    expect(one.outcome).toBe('done');
    expect((await noticesOf(w, w.ann)).every(n => n.state === 'acknowledged')).toBe(true);
    expect((await w.notifier.reply({ channel: 'sms', from: PHONE.ann, body: 'ack 9' })).outcome).toBe('refused');
  });

  test('an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing', async () => {
    w = await world();
    const dana = String((await w.orm.asSystem().account.createOne({ email: 'dana@example.test' })).id);
    const identity = await w.notifier.addIdentity({ kind: 'phone', address: '+1 555 010 0009', principal: { kind: 'user', id: dana } });
    expect(identity.trust).toBe('unverified');
    expect(await w.notifier.resolveSender('phone', '+15550100009')).toEqual([]);
    await w.notifier.startVerification(String(identity.id));
    const sent = w.relay.sms.at(-1)!;
    expect(sent.to).toBe('+15550100009');
    const code = /(\d{6})/u.exec(sent.text)![1]!;
    expect((await w.notifier.reply({ channel: 'sms', from: '+15550100009', body: '000000' })).outcome).toBe('unknown');
    const verified = await w.notifier.reply({ channel: 'sms', from: '+15550100009', body: code });
    expect(verified.outcome).toBe('verified');
    const [resolved] = await w.notifier.resolveSender('phone', '+15550100009');
    expect(String(resolved!.principal)).toBe(dana);
    const stranger = await w.notifier.reply({ channel: 'sms', from: '+15550109999', body: 'done 1' });
    expect(stranger.outcome).toBe('unknown');
    expect(stranger.principal).toBeUndefined();
    expect((await w.orm.asSystem().ticket.getOne(w.ticket)).status).toBe('open');
  });

  test('a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others', async () => {
    w = await world();
    w.relay.state.failTo.add(PHONE.ann);
    await overdue(w);
    await drain(w.queue);
    expect(w.relay.mail.map(m => m.to)).toEqual([EMAIL.ann]);
    const annDeliveries = (await w.orm.asSystem().delivery.findMany({ recipient: { eq: w.ann } }, { orderBy: [{ field: 'queuedAt' }] })).rows;
    expect(annDeliveries.map(d => [d.channelKind, d.state])).toEqual([
      ['sms', 'failed'],
      ['email', 'sent'],
    ]);
    // The SMS relay goes down: its health check fails, channel.down is
    // raised to the people its route names, and they hear of it by email.
    await w.orm.asSystem().noticeRoute.createOne({ name: 'Channel alerts', eventKind: 'channel.down', recipients: [{ principal: w.ben }] });
    w.relay.state.smsDown = true;
    const health = await w.notifier.checkHealth();
    expect(health.find(h => h.slot === 'SMS')!.ok).toBe(false);
    await drain(w.queue);
    const alert = (await noticesOf(w, w.ben)).find(n => n.eventKind === 'channel.down')!;
    expect(alert.subjectKey).toBe('SMS');
    expect(w.relay.mail.at(-1)!.to).toBe(EMAIL.ben);
    // A second check while it is still down alerts no one again.
    await w.notifier.checkHealth();
    await drain(w.queue);
    expect((await noticesOf(w, w.ben)).filter(n => n.eventKind === 'channel.down').length).toBe(1);
  });

  test('quiet hours hold a normal notice, digests batch, and record sends nothing', async () => {
    const fixed = new Date('2027-03-10T23:30:00Z');
    w = await world({ now: () => fixed, expand: () => ({ occurrences: [{ dueAt: fixed.getTime() + 8.5 * 3600_000 }] }) });
    const sys = w.orm.asSystem();
    await sys.noticePreference.createOne({ principal: w.ben, eventKind: '*', urgency: 'normal', channels: ['sms'], quietStart: '22:00', quietEnd: '07:00', timeZone: 'UTC' });
    await sys.noticePreference.createOne({ principal: w.ann, eventKind: '*', urgency: 'digest', channels: ['email'], timeZone: 'UTC', digest: 'FREQ=DAILY;BYHOUR=8;BYMINUTE=0' });
    await sys.noticeRoute.createOne({ name: 'Assignments', eventKind: 'ticket.assigned', recipients: [{ principal: w.ben }] });
    await sys.noticeRoute.createOne({ name: 'Summaries', eventKind: 'ticket.summary', recipients: [{ path: 'owner' }] });
    await sys.noticeRoute.createOne({ name: 'History', eventKind: 'reference.cleared', recipients: [{ path: 'owner' }] });
    await w.orm.transaction(async tx => {
      await w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.assigned' });
      await w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.summary' });
      await w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'ticket.summary', id: crypto.randomUUID() });
      await w.notifier.raise(tx, { table: 'Ticket', key: w.ticket, kind: 'reference.cleared' });
    });
    await drain(w.queue);
    expect(w.relay.sms).toEqual([]);
    expect(w.relay.mail).toEqual([]);
    const held = (await sys.delivery.findMany({ recipient: { eq: w.ben } })).rows[0]!;
    expect(held.state).toBe('queued');
    expect(new Date(held.dueAt as Date).toISOString()).toBe('2027-03-11T07:00:00.000Z');
    expect((await noticesOf(w, w.ben))[0]!.payload).toEqual({ title: 'Boiler pressure low', status: 'open' });
    const record = (await noticesOf(w, w.ann)).find(n => n.eventKind === 'reference.cleared')!;
    expect(record.state).toBe('read');
    // One digest is waiting for Ann's two summaries.
    const waiting = await w.db.pool.query(`SELECT step FROM notice_work_queue WHERE state = 'ready'`);
    expect(waiting.rows.map(r => r.step).sort()).toEqual(['deliver', 'digest']);
    await advance(w, 'digest');
    await advance(w, 'deliver');
    await drain(w.queue);
    expect(w.relay.sms.map(m => m.to)).toEqual([PHONE.ben]);
    expect(w.relay.mail.length).toBe(1);
    expect(w.relay.mail[0]!.to).toBe(EMAIL.ann);
    expect(w.relay.mail[0]!.text.split('\n').length).toBe(2);
  });

  test("a delivery's statuses move it on and a cleared reference is recorded on its holder", async () => {
    w = await world();
    await overdue(w);
    await drain(w.queue);
    const sms = w.relay.sms.find(m => m.to === PHONE.ann)!;
    const moved = await w.notifier.statuses('SMS', signed({ events: [{ id: sms.id, status: 'delivered', at: new Date().toISOString() }] }));
    expect(moved).toBe(1);
    const delivery = (await w.orm.asSystem().delivery.findOne({ providerMessageId: { eq: sms.id } }))!;
    expect(delivery.state).toBe('delivered');
    // Replies come in through the relay's own callback.
    const [outcome] = await w.notifier.inbound('SMS', signed({ messages: [{ id: 'in-1', from: PHONE.ann, text: 'ack 1', at: new Date().toISOString() }] }));
    expect(outcome!.outcome).toBe('done');
    expect((await w.orm.asSystem().delivery.findOne({ providerMessageId: { eq: sms.id } }))!.state).toBe('acknowledged');
    // A ticket another refers to is deleted: its holder records it.
    const sys = w.orm.asSystem();
    await sys.noticeRoute.createOne({ name: 'History', eventKind: 'reference.cleared', recipients: [{ path: 'owner' }] });
    const other = String((await sys.ticket.createOne({ title: 'Parts order', status: 'open', owner: w.ann })).id);
    await sys.references.add({ from: { table: 'Ticket', key: w.ticket }, to: { table: 'Ticket', key: other } });
    await sys.ticket.deleteOne(other);
    const cleared = postgresQueue(w.db.pool, referenceClearedQueue);
    const claims = await cleared.claim(10);
    expect(claims.length).toBe(1);
    await w.orm.transaction(tx => w.notifier.cleared(tx, claims[0]!.message as never));
    await cleared.complete(claims[0]!.id, claims[0]!.token);
    await drain(w.queue);
    const record = (await noticesOf(w, w.ann)).find(n => n.eventKind === 'reference.cleared')!;
    expect(record.payload).toMatchObject({ targetTable: 'Ticket', role: '' });
    expect(record.title).toBe('Boiler pressure low');
    // A behavior's event, as its behavior worker's Workers method hands it.
    await sys.noticeRoute.createOne({ name: 'Missed', eventKind: 'recurrence.missed', recipients: [{ path: 'owner' }] });
    await w.orm.transaction(tx => w.notifier.fromBehaviorEvent(tx, 'Ticket', 'recurrence', { kind: 'missed', key: w.ticket, payload: { key: '2027-01-01T09:00' } }));
    await drain(w.queue);
    const missed = (await noticesOf(w, w.ann)).find(n => n.eventKind === 'recurrence.missed')!;
    expect(missed.payload).toEqual({ key: '2027-01-01T09:00' });
  });

  test('password reset sends a code through the email adapter and the code sets the password', async () => {
    w = await world();
    const params = { memoryKiB: 64, iterations: 1, parallelism: 1 };
    const store = postgresIdentityStore(w.db.pool, identityDescriptor);
    const identity = new IdentityService({ store, config: { password: { argon2: params } } });
    await store.createUser({ login: 'dana@example.test', name: '', passwordHash: await hashPassword('first-password-1', params), at: new Date() });
    const reset = passwordReset({ notifier: w.notifier, orm: w.orm, identity });
    await reset.request({ login: 'nobody@example.test' });
    expect(w.relay.mail).toEqual([]);
    await reset.request({ login: 'Dana@example.test' });
    const sent = w.relay.mail.at(-1)!;
    expect(sent.to).toBe('dana@example.test');
    expect(sent.subject).toContain('password reset');
    const code = /(\d{6})/u.exec(sent.text)![1]!;
    expect(await reset.complete({ login: 'dana@example.test', code: '999999', password: 'second-password-2' })).toBe(false);
    expect(await reset.complete({ login: 'dana@example.test', code, password: 'second-password-2' })).toBe(true);
    const login = await store.findLogin('dana@example.test');
    expect((await verifyPassword(login.passwordHash, 'second-password-2', params)).ok).toBe(true);
    // The code is spent, and the address it reached is verified.
    expect(await reset.complete({ login: 'dana@example.test', code, password: 'third-password-3' })).toBe(false);
    expect((await w.notifier.resolveSender('email', 'dana@example.test')).length).toBe(1);
    expect(JSON.stringify(w.relay.mail)).not.toContain(RELAY_TOKEN);
  });
});
