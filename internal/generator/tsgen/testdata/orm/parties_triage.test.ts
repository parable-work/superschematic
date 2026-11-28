// fixture-parties-db end to end (D157, #606): a message held in triage
// settled onto a party, by an address entered by hand or one verified by a
// code, and a messaging bridge's inbound path. TestPartiesTriageEndToEnd
// runs it on Postgres and TestPartiesTriageEndToEndOnSQLite on SQLite, with
// the runtime-providers example's SMS relay and its chat bridge as the
// stub adapters: the bridge's loop reads a stand-in network and posts to
// the notifier's routes, as a generated server mounts them.
import { afterEach, describe, expect, test } from 'bun:test';
import { createHmac } from 'node:crypto';
import { InboundError, createInbound, createNotifier, issueGrant, notifierRoutes, type Inbound, type Notifier, type RunRequest } from '@superschematic/http-runtime/postgres';
import type { Principal } from '@superschematic/http-runtime';
import { providersFrom, SecretValue } from '@superschematic/http-runtime/providers';
import type { FixturePartiesDbOrm } from './orm';
import { noticeWorkQueue, runRequestQueue } from './queues';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';
import { smsRelayA } from './sms-relay-a';
import { chatBridge, runBridge, type RelayedMessage } from './chat-bridge';

const RELAY_TOKEN = 'relay-token';
const RELAY_KEY = 'chat-relay-key-5e1b';
const PHONE = { fay: '+15550100041', stranger: '+15550100042', ann: '+15550100001' };

interface World {
  db: FeatureDatabase;
  orm: FixturePartiesDbOrm;
  notifier: Notifier;
  inbound: Inbound;
  sms: { to: string; text: string }[];
  routes: (request: Request) => Promise<Response | null>;
  fay: string;
  dee: string;
  job: string;
  grant: string;
  ann: string;
  staff: Principal;
}

async function world(): Promise<World> {
  const db = await featureDatabase('triage');
  const orm = (await ormEntry<FixturePartiesDbOrm>()).createOrm(db.source);
  const sys = orm.asSystem();
  const sms: { to: string; text: string }[] = [];
  let n = 0;
  const fetch = (async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input));
    if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    sms.push({ to: body.to, text: body.text });
    return Response.json({ id: `out-${++n}`, status: 'accepted' });
  }) as typeof globalThis.fetch;
  const providers = providersFrom({
    slots: { SMS: 'channel', CHAT: 'channel' },
    config: {
      SMS: 'sms-relay-a',
      SMS_URL: 'https://sms.test',
      SMS_TOKEN: new SecretValue(RELAY_TOKEN),
      CHAT: 'chat-bridge',
      CHAT_URL: 'https://chat.test',
      CHAT_TOKEN: new SecretValue(RELAY_TOKEN),
      CHAT_RELAY_KEY: new SecretValue(RELAY_KEY),
    },
    definitions: [smsRelayA({ fetch }), chatBridge({ fetch })],
  });
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue, providers });
  const inbound = createInbound({ orm, notifier, runs: runRequestQueue, providers });
  const routes = notifierRoutes({ notifier, inbound, slots: ['SMS', 'CHAT'], authenticate: async () => null });
  await sys.role.createOne({ name: 'contractor', permissions: ['reports.write'], reaches: ['Report:job'] });
  const ann = String((await sys.account.createOne({ email: 'ann@example.test' })).id);
  const fay = String((await sys.contact.createOne({ name: 'Fay' })).id);
  const dee = String((await sys.contact.createOne({ name: 'Dee' })).id);
  const job = String((await sys.job.createOne({ title: 'Fix the gate' })).id);
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.ann, principal: ann, trust: 'verified', verifiedAt: new Date() });
  await sys.channelIdentity.createOne({ kind: 'handle', address: '@dee', principalKind: 'party', principalTable: 'Contact', principal: dee, trust: 'verified', verifiedAt: new Date() });
  const { grant } = await issueGrant(sys, { holder: fay, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: job });
  await issueGrant(sys, { holder: dee, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: job });
  const staff: Principal = { subject: ann, kind: 'user', permissions: ['parties.message', 'parties.triage'] };
  return { db, orm, notifier, inbound, sms, routes, fay, dee, job, grant: String(grant.id), ann, staff };
}

function text(from: string, body: string) {
  const payload = JSON.stringify({ messages: [{ id: `in-${crypto.randomUUID()}`, from, text: body, at: new Date().toISOString() }] });
  return { headers: { 'x-relay-signature': createHmac('sha256', RELAY_TOKEN).update(payload).digest('hex') }, body: payload };
}

/** The run requests waiting, read and put back. */
async function pendingRuns(w: World): Promise<RunRequest[]> {
  const queue = w.db.queue(runRequestQueue);
  const claims = await queue.claim(50);
  for (const claim of claims) await queue.release(claim.id, claim.token);
  return claims.map(c => c.message as unknown as RunRequest);
}

/** The app as the bridge reaches it: the notifier's routes. */
function appFetch(w: World): typeof globalThis.fetch {
  return (async (input: string | URL | Request, init?: RequestInit) => (await w.routes(new Request(input, init))) ?? new Response('not found', { status: 404 })) as typeof globalThis.fetch;
}

async function* network(messages: RelayedMessage[]): AsyncGenerator<RelayedMessage> {
  for (const message of messages) yield message;
}

describe.skipIf(!available)('settling triage and a messaging bridge', () => {
  let w: World;
  afterEach(async () => {
    await w?.db.drop();
  });

  test('a held message settled onto a party by an address entered by hand is filed as from the party, at the lower trust', async () => {
    w = await world();
    const [held] = await w.inbound.inbound('SMS', text(PHONE.fay, 'Gate fixed'));
    expect(held).toMatchObject({ outcome: 'unknown' });
    const party = { table: 'Contact', key: w.fay };
    const settled = await w.inbound.settleOnto(w.staff, held!.message!, { party, trust: 'entered' });
    expect(settled).toMatchObject({ outcome: 'run', message: held!.message, party, grant: w.grant });
    // As if it had come from the party: its conversation, its grant, the
    // trust of an address entered by hand, and out of triage.
    const message = (await w.inbound.message(held!.message!))!;
    expect(message).toMatchObject({ partyTable: 'Contact', disposition: 'run', trust: 'entered', triage: 'closed', triageClosedBy: w.ann });
    const conversation = await w.orm.asSystem().conversation.findOne({ id: { eq: message.conversation } });
    expect(conversation).toMatchObject({ partyTable: 'Contact', channelKind: 'sms' });
    expect(await pendingRuns(w)).toEqual([{ trigger: 'inbound', party, message: settled.message!, grant: w.grant }]);
    const backlinks = await w.orm.asSystem().references.referencedBy({ table: 'Contact', key: w.fay });
    expect(backlinks.items.some(b => b.table === 'Message' && b.key === held!.message)).toBe(true);
    // The address is the party's now: its next text reaches the party.
    const identity = (await w.orm.asSystem().channelIdentity.findMany({ address: { eq: PHONE.fay } })).rows[0]!;
    expect(identity).toMatchObject({ kind: 'phone', principalKind: 'party', principalTable: 'Contact', trust: 'entered' });
    const [next] = await w.inbound.inbound('SMS', text(PHONE.fay, 'Back tomorrow'));
    expect(next).toMatchObject({ outcome: 'run', party, grant: w.grant });
    expect((await w.inbound.message(next!.message!))!.trust).toBe('entered');
    // A message settled once is held no more.
    await expect(w.inbound.settleOnto(w.staff, held!.message!, { party, trust: 'entered' })).rejects.toThrow(InboundError);
  });

  test('a held message settled onto a party to be verified waits for the code its address answers, then is filed', async () => {
    w = await world();
    const [held] = await w.inbound.inbound('SMS', text(PHONE.fay, 'Gate fixed'));
    const party = { table: 'Contact', key: w.fay };
    const settling = await w.inbound.settleOnto(w.staff, held!.message!, { party, trust: 'verified' });
    expect(settling).toMatchObject({ outcome: 'verifying', message: held!.message, party });
    expect(await w.inbound.message(held!.message!)).toMatchObject({ disposition: 'verifying', triage: 'open' });
    expect(await pendingRuns(w)).toEqual([]);
    // The code goes to the address, which answers it.
    const sent = w.sms.filter(m => m.to === PHONE.fay).at(-1)!;
    const code = /\d{6}/u.exec(sent.text)![0];
    const [answer] = await w.inbound.inbound('SMS', text(PHONE.fay, code));
    expect(answer).toMatchObject({ outcome: 'verified', party });
    // Then the held message is filed as the party's, at the trust of a verified address.
    expect(await w.inbound.message(held!.message!)).toMatchObject({ partyTable: 'Contact', disposition: 'run', trust: 'verified', triage: 'closed', triageClosedBy: w.ann });
    expect(await pendingRuns(w)).toEqual([{ trigger: 'inbound', party, message: held!.message!, grant: w.grant }]);
  });

  test("settling is held to the triage permission, the party's row and an address that is no user's", async () => {
    w = await world();
    const [held] = await w.inbound.inbound('SMS', text(PHONE.stranger, 'Who is this?'));
    const party = { table: 'Contact', key: w.fay };
    const clerk: Principal = { subject: w.ann, kind: 'user', permissions: ['parties.message'] };
    await expect(w.inbound.settleOnto(clerk, held!.message!, { party, trust: 'entered' })).rejects.toThrow(/parties\.triage/u);
    await expect(w.inbound.settleOnto(w.staff, held!.message!, { party: { table: 'Contact', key: crypto.randomUUID() }, trust: 'entered' })).rejects.toThrow(/no party/u);
    // A user's address is no party's.
    await w.orm.asSystem().channelIdentity.createOne({ kind: 'phone', address: PHONE.stranger, principal: w.ann, trust: 'unverified' });
    await expect(w.inbound.settleOnto(w.staff, held!.message!, { party, trust: 'entered' })).rejects.toThrow(/user's/u);
    expect(await w.inbound.message(held!.message!)).toMatchObject({ disposition: 'unknown', triage: 'open' });
    expect((await w.orm.asSystem().channelIdentity.findMany({ principalKind: { eq: 'party' }, address: { eq: PHONE.stranger } })).rows).toEqual([]);
  });

  test("a messaging bridge relays a party's message and an unknown sender's down the inbound path, with its relay key alone", async () => {
    w = await world();
    const fetch = appFetch(w);
    const relayed = await runBridge({
      messages: network([
        { id: 'chat-1', from: '@dee', text: 'Gate fixed', at: new Date().toISOString() },
        { id: 'chat-2', from: '@stranger', text: 'Is the gate fixed?', at: new Date().toISOString() },
      ]),
      inbound: 'http://app.test/webhooks/chat/inbound',
      key: RELAY_KEY,
      fetch,
    });
    expect(relayed).toBe(2);
    const rows = (await w.orm.asSystem().message.findMany({ via: { eq: 'chat' } }, { orderBy: [{ field: 'at' }] })).rows;
    expect(rows.map(r => [r.fromAddress, r.disposition, r.triage ?? null])).toEqual([
      ['@dee', 'run', null],
      ['@stranger', 'unknown', 'open'],
    ]);
    expect(await pendingRuns(w)).toEqual([{ trigger: 'inbound', party: { table: 'Contact', key: w.dee }, message: expect.any(String), grant: expect.any(String) }]);
    // A key that is not the relay key relays nothing.
    await expect(
      runBridge({ messages: network([{ id: 'chat-3', from: '@dee', text: 'forged', at: new Date().toISOString() }]), inbound: 'http://app.test/webhooks/chat/inbound', key: 'not-the-key', fetch })
    ).rejects.toThrow(/relay key/u);
    expect((await w.orm.asSystem().message.findMany({ via: { eq: 'chat' } })).rows.length).toBe(2);
    // The held chat message settles onto a party like any other.
    const party = { table: 'Contact', key: w.fay };
    expect(await w.inbound.settleOnto(w.staff, String(rows[1]!.id), { party, trust: 'entered' })).toMatchObject({ outcome: 'run', party });
  });
});
