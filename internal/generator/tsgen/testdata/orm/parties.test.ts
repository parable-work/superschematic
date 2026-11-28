// fixture-parties-db end to end (D157): contacts that are parties, the
// jobs a grant lets one reach and the reports it writes on them, the
// conversations and their messages, which hold files and point at rows,
// the run requests queue, and the notifier a user's reply runs through.
// TestPartiesEndToEnd generates the package, type-checks it, applies the
// generated create.sql in a schema of its own on Postgres and runs these
// cases through the inbound path over the generated repositories, with the
// runtime-providers example's SMS and mail relays (D159) as the stub
// adapters, each over a stand-in service the test answers.
// TestPartiesEndToEndOnSQLite runs the same cases with ORM_DIALECT sqlite
// (D189): over the ./orm/sqlite entry's repositories on a file the
// package's sqlite/create.sql made, the run requests claimed over it.
import { afterEach, describe, expect, test } from 'bun:test';
import { createHash, createHmac } from 'node:crypto';
import {
  InboundError,
  createInbound,
  createNotifier,
  issueGrant,
  revokeGrant,
  type Inbound,
  type Notifier,
  type RunRequest,
  type SendDecision,
} from '@superschematic/http-runtime/postgres';
import type { Principal } from '@superschematic/http-runtime';
import { providersFrom, SecretValue } from '@superschematic/http-runtime/providers';
import type { FixturePartiesDbOrm } from './orm';
import { noticeWorkQueue, runRequestQueue } from './queues';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';
import { smsRelayA } from './sms-relay-a';
import { mailRelay } from './mail-relay';

const RELAY_TOKEN = 'relay-token';
const INBOX = 'inbox@example.test';
const PHOTO = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4, 5, 6, 7, 8]);
const PHOTO_SHA = createHash('sha256').update(PHOTO).digest('hex');

/** The SMS and mail relays as stand-ins: what each was sent, and the photo a text came with. */
function relays() {
  const sms: { to: string; text: string; id: string }[] = [];
  const mail: { to: string; subject: string; text: string; id: string; replyTo?: string; headers?: Record<string, string> }[] = [];
  let n = 0;
  const fetch = (async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = new URL(String(input));
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (url.host === 'sms.test') {
      if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
      if (url.pathname === '/media/1') return new Response(PHOTO, { headers: { 'content-type': 'image/jpeg' } });
      const id = `sms-${++n}`;
      sms.push({ to: body.to, text: body.text, id });
      return Response.json({ id, status: 'accepted' });
    }
    if (url.host === 'mail.test') {
      if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
      const id = `mail-${++n}`;
      mail.push({ to: body.to, subject: body.subject, text: body.text, id, replyTo: body.replyTo, headers: body.headers });
      return Response.json({ id });
    }
    return new Response('no such service', { status: 404 });
  }) as typeof globalThis.fetch;
  return { sms, mail, fetch };
}

/** A callback from a relay, signed as it signs one. */
function signed(body: unknown): { headers: Record<string, string>; body: string } {
  const text = JSON.stringify(body);
  return { headers: { 'x-relay-signature': createHmac('sha256', RELAY_TOKEN).update(text).digest('hex') }, body: text };
}

/** A bucket over a map, as a test needs one: put, get and a signed URL that names the object. */
function memoryBucket() {
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  const bytesOf = async (body: unknown): Promise<Uint8Array> =>
    typeof body === 'string' ? new TextEncoder().encode(body) : body instanceof Uint8Array ? body : new Uint8Array(await new Response(body as ReadableStream).arrayBuffer());
  const bucket = {
    name: 'memory/files',
    async put(name: string, body: unknown, options?: { contentType?: string }) {
      const bytes = await bytesOf(body);
      objects.set(name, { bytes, ...(options?.contentType ? { contentType: options.contentType } : {}) });
      return { name, size: bytes.length };
    },
    async get(name: string) {
      const object = objects.get(name);
      if (!object) throw new Error(`no object ${name}`);
      return { object: { name, size: object.bytes.length }, body: new Response(object.bytes).body! };
    },
    async delete(name: string) {
      objects.delete(name);
    },
    async list() {
      return { objects: [...objects.keys()].sort().map(name => ({ name, size: objects.get(name)!.bytes.length })) };
    },
    async signedUrl(name: string, options: { method: string }) {
      return `https://files.test/${name}?method=${options.method}`;
    },
  };
  return { objects, bucket };
}

const PHONE = { dee: '+15550100011', eve: '+15550100012', ann: '+15550100001' };

interface World {
  db: FeatureDatabase;
  orm: FixturePartiesDbOrm;
  notifier: Notifier;
  inbound: Inbound;
  relay: ReturnType<typeof relays>;
  files: ReturnType<typeof memoryBucket>;
  /** A contact with a grant on the job. */
  dee: string;
  job: string;
  grant: string;
  ann: string;
  /** Someone of the staff, who may message parties and read triage. */
  staff: Principal;
}

async function world(options: { approve?: (body: string) => SendDecision } = {}): Promise<World> {
  const db = await featureDatabase('parties');
  const orm = (await ormEntry<FixturePartiesDbOrm>()).createOrm(db.source);
  const sys = orm.asSystem();
  const relay = relays();
  const files = memoryBucket();
  const providers = providersFrom({
    slots: { SMS: 'channel', MAIL: 'channel' },
    config: {
      SMS: 'sms-relay-a',
      SMS_URL: 'https://sms.test',
      SMS_TOKEN: new SecretValue(RELAY_TOKEN),
      MAIL: 'mail-relay',
      MAIL_URL: 'https://mail.test',
      MAIL_TOKEN: new SecretValue(RELAY_TOKEN),
      MAIL_FROM: INBOX,
    },
    definitions: [smsRelayA({ fetch: relay.fetch }), mailRelay({ fetch: relay.fetch })],
  });
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue, providers });
  const inbound = createInbound({
    orm,
    notifier,
    runs: runRequestQueue,
    providers,
    bucket: files.bucket as never,
    inboundAddress: INBOX,
    rateLimit: { messages: 3, seconds: 3600 },
    ...(options.approve ? { approve: request => options.approve!(request.body) } : {}),
  });
  await sys.role.createOne({ name: 'contractor', permissions: ['reports.write'], reaches: ['Report:job'] });
  const ann = String((await sys.account.createOne({ email: 'ann@example.test' })).id);
  const dee = String((await sys.contact.createOne({ name: 'Dee' })).id);
  const job = String((await sys.job.createOne({ title: 'Fix the gate' })).id);
  for (const [kind, address] of [['phone', PHONE.dee], ['email', 'dee@example.test']] as const) {
    await sys.channelIdentity.createOne({ kind, address, principalKind: 'party', principalTable: 'Contact', principal: dee, trust: 'verified', verifiedAt: new Date() });
  }
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.ann, principal: ann, trust: 'verified', verifiedAt: new Date() });
  const { grant } = await issueGrant(sys, { holder: dee, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: job });
  const staff: Principal = { subject: ann, kind: 'user', permissions: ['parties.message', 'parties.triage'] };
  return { db, orm, notifier, inbound, relay, files, dee, job, grant: String(grant.id), ann, staff };
}

/** The stub handler of a run request: as the party, through the grant the request names, a report on the granted job, the message filed onto both. */
function fileOnGrantedRecord(w: World) {
  return async (request: RunRequest): Promise<void> => {
    const party = await w.inbound.principalOf(request.party, { grant: request.grant });
    const grant = party.grants?.[0];
    // The grant ended since the message came: the run reaches nothing.
    if (!grant) return;
    const message = (await w.inbound.message(request.message))!;
    const report = await w.orm.as(party).report.createOne({ jobId: grant.targetId, body: String(message.body) });
    await w.inbound.file({ message: request.message, to: { table: 'Report', key: String(report.id) }, as: party });
    await w.inbound.file({ message: request.message, to: { table: grant.targetType, key: grant.targetId }, as: party });
  };
}

/** Runs every run request that is waiting through the stub handler; resolves to how many. */
async function drainRuns(w: World): Promise<number> {
  const queue = w.inbound.runWorker(w.db.source, fileOnGrantedRecord(w));
  let handled = 0;
  for (let round = 0; round < 20; round++) {
    const claims = await queue.claim(10, 30);
    if (claims.length === 0) return handled;
    for (const claim of claims) {
      await claim.handle(new AbortController().signal);
      await queue.complete(claim.id, claim.token);
      handled++;
    }
  }
  throw new Error('the run requests did not drain');
}

/** The run requests waiting, read and put back. */
async function pendingRuns(w: World): Promise<RunRequest[]> {
  const queue = w.db.queue(runRequestQueue);
  const claims = await queue.claim(50);
  for (const claim of claims) await queue.release(claim.id, claim.token);
  return claims.map(c => c.message as unknown as RunRequest);
}

function text(from: string, body: string, extra: Record<string, unknown> = {}) {
  return signed({ messages: [{ id: `in-${crypto.randomUUID()}`, from, text: body, at: new Date().toISOString(), ...extra }] });
}

function mail(from: string, body: string, extra: Record<string, unknown> = {}) {
  return signed({
    mail: [{ id: `mail-in-${crypto.randomUUID()}`, from, to: [INBOX], subject: 'The gate', text: body, at: new Date().toISOString(), auth: { spf: 'pass', dkim: 'pass', dmarc: 'pass' }, ...extra }],
  });
}

async function reportsOf(w: World) {
  return (await w.orm.asSystem().report.findMany({ jobId: { eq: w.job } })).rows;
}

describe.skipIf(!available)('parties', () => {
  let w: World;
  afterEach(async () => {
    await w?.db.drop();
  });

  test('a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached', async () => {
    w = await world();
    const [outcome] = await w.inbound.inbound('SMS', text('+1 (555) 010-0011', 'Gate fixed, photo attached', { media: [{ url: 'https://sms.test/media/1', type: 'image/jpeg', name: 'gate.jpg' }] }));
    expect(outcome!.outcome).toBe('run');
    expect(outcome!.party).toEqual({ table: 'Contact', key: w.dee });
    expect(outcome!.grant).toBe(w.grant);
    // The run request, as the agent dispatcher (D158) takes it.
    const [request] = await pendingRuns(w);
    expect(request).toEqual({ trigger: 'inbound', party: { table: 'Contact', key: w.dee }, message: outcome!.message!, grant: w.grant });
    const message = (await w.inbound.message(outcome!.message!))!;
    expect(message).toMatchObject({ direction: 'inbound', via: 'sms', fromAddress: PHONE.dee, disposition: 'run', partyTable: 'Contact', trust: 'verified' });
    expect(message.triage ?? null).toBeNull();
    // The photo is the message's, in the bucket once.
    const sys = w.orm.asSystem();
    const [photo] = await sys.attachments.list({ table: 'Message', key: outcome!.message! });
    expect(photo).toMatchObject({ name: 'gate.jpg', mediaType: 'image/jpeg', size: PHOTO.length, contentHash: PHOTO_SHA });
    expect(w.files.objects.size).toBe(1);
    expect(await drainRuns(w)).toBe(1);
    // A report on the granted job, written as the party, with the photo.
    const reports = await reportsOf(w);
    expect(reports.length).toBe(1);
    expect(reports[0]!.body).toBe('Gate fixed, photo attached');
    const [filed] = await sys.attachments.list({ table: 'Report', key: String(reports[0]!.id) });
    expect(filed).toMatchObject({ name: 'gate.jpg', contentHash: PHOTO_SHA, objectKey: photo!.objectKey });
    expect(w.files.objects.size).toBe(1);
    // A run request run again, after its handler failed, files nothing twice.
    const filer = await w.inbound.principalOf({ table: 'Contact', key: w.dee });
    await w.inbound.file({ message: outcome!.message!, to: { table: 'Report', key: String(reports[0]!.id) }, as: filer });
    expect((await sys.attachments.list({ table: 'Report', key: String(reports[0]!.id) })).length).toBe(1);
    // The message is on each row it references: the job, the report and the party.
    for (const row of [{ table: 'Job', key: w.job }, { table: 'Report', key: String(reports[0]!.id) }, { table: 'Contact', key: w.dee }]) {
      const backlinks = await sys.references.referencedBy(row);
      expect(backlinks.items.some(b => b.table === 'Message' && b.key === outcome!.message)).toBe(true);
    }
    // The party reaches the job's reports through its grant, and no other job's.
    const other = String((await sys.job.createOne({ title: 'Paint the fence' })).id);
    const party = await w.inbound.principalOf({ table: 'Contact', key: w.dee });
    await expect(w.orm.as(party).report.createOne({ jobId: other, body: 'not mine' })).rejects.toThrow();
  });

  test('a text from an unknown number lands in triage', async () => {
    w = await world();
    const [outcome] = await w.inbound.inbound('SMS', text('+15550109999', 'Gate fixed', { media: [{ url: 'https://sms.test/media/1', type: 'image/jpeg' }] }));
    expect(outcome!.outcome).toBe('unknown');
    expect(outcome!.party).toBeUndefined();
    expect(await pendingRuns(w)).toEqual([]);
    const message = (await w.inbound.message(outcome!.message!))!;
    expect(message).toMatchObject({ disposition: 'unknown', triage: 'open', fromAddress: '+15550109999' });
    expect(message.party ?? null).toBeNull();
    // Its photo is filed to its conversation, held with it.
    expect((await w.orm.asSystem().attachments.list({ table: 'Message', key: outcome!.message! })).length).toBe(1);
    const held = await w.inbound.triage(w.staff);
    expect(held.map(m => m.id)).toEqual([outcome!.message!]);
    // Reading triage is a permission.
    await expect(w.inbound.triage({ subject: w.ann, kind: 'user', permissions: [] })).rejects.toThrow(InboundError);
    await w.inbound.settle(w.staff, outcome!.message!);
    expect(await w.inbound.triage(w.staff)).toEqual([]);
    expect(await reportsOf(w)).toEqual([]);
  });

  test('the same text from a party whose grant has ended reaches nothing, until its identity is verified again', async () => {
    w = await world();
    // A run request made before the grant ended runs as nothing after.
    await w.inbound.inbound('SMS', text(PHONE.dee, 'Gate fixed'));
    await revokeGrant(w.orm, w.grant);
    expect(await drainRuns(w)).toBe(1);
    expect(await reportsOf(w)).toEqual([]);
    // The number may be someone else's now: held as no one's.
    const [outcome] = await w.inbound.inbound('SMS', text(PHONE.dee, 'Gate fixed'));
    expect(outcome!.outcome).toBe('lapsed');
    expect(outcome!.party).toBeUndefined();
    expect(await pendingRuns(w)).toEqual([]);
    const message = (await w.inbound.message(outcome!.message!))!;
    expect(message).toMatchObject({ disposition: 'lapsed', triage: 'open' });
    expect(message.party ?? null).toBeNull();
    expect((await w.inbound.timeline(w.staff, { table: 'Contact', key: w.dee })).some(m => m.id === outcome!.message)).toBe(false);
    expect((await w.orm.asSystem().references.referencedBy({ table: 'Contact', key: w.dee })).items.some(b => b.key === outcome!.message)).toBe(false);
    // Nothing is sent to it either.
    expect((await w.inbound.send({ as: w.staff, party: { table: 'Contact', key: w.dee }, channel: 'sms', body: 'Are you there?' })).detail).toContain('lapsed');
    // Verified again by the code sent to it, the party is itself again, with no grant.
    const [identity] = (await w.orm.asSystem().channelIdentity.findMany({ kind: { eq: 'phone' }, address: { eq: PHONE.dee } })).rows;
    await w.notifier.startVerification(String(identity!.id));
    const code = /(\d{6})/u.exec(w.relay.sms.at(-1)!.text)![1]!;
    const [verified] = await w.inbound.inbound('SMS', text(PHONE.dee, code));
    expect(verified!.outcome).toBe('verified');
    const [again] = await w.inbound.inbound('SMS', text(PHONE.dee, 'Gate fixed'));
    expect(again!.outcome).toBe('timeline');
    expect(await pendingRuns(w)).toEqual([]);
  });

  test('email failing DMARC is treated as unknown, and an authenticated email threads to its party', async () => {
    w = await world();
    const [spoofed] = await w.inbound.inbound('MAIL', mail('Dee@Example.test', 'Gate fixed', { auth: { spf: 'pass', dkim: 'pass', dmarc: 'fail' } }));
    expect(spoofed!.outcome).toBe('unknown');
    expect(spoofed!.detail).toContain('dmarc=fail');
    expect(await pendingRuns(w)).toEqual([]);
    // No authentication reported is none passed.
    const [bare] = await w.inbound.inbound('MAIL', mail('dee@example.test', 'Gate fixed', { auth: undefined }));
    expect(bare!.outcome).toBe('unknown');
    // Mail to another address is not the deployment's.
    const [elsewhere] = await w.inbound.inbound('MAIL', mail('dee@example.test', 'Gate fixed', { to: ['other@example.test'] }));
    expect(elsewhere!.outcome).toBe('ignored');
    // The staff write; the party's reply threads to the same conversation.
    const sent = await w.inbound.send({ as: w.staff, party: { table: 'Contact', key: w.dee }, channel: 'email', subject: 'The gate', body: 'Is the gate fixed?', about: [{ table: 'Job', key: w.job }] });
    expect(sent.outcome).toBe('sent');
    const out = w.relay.mail.at(-1)!;
    expect(out.to).toBe('dee@example.test');
    expect(out.replyTo).toBe(INBOX);
    const messageId = out.headers!['Message-ID']!;
    expect(messageId).toMatch(/^<.+@example\.test>$/u);
    const [answer] = await w.inbound.inbound('MAIL', mail('dee@example.test', 'Yes, photo attached', { messageId: '<r1@mail.example>', inReplyTo: messageId, references: [messageId], attachments: [{ name: 'gate.jpg', type: 'image/jpeg', content: Buffer.from(PHOTO).toString('base64') }] }));
    expect(answer!.outcome).toBe('run');
    const outbound = (await w.inbound.message(sent.message!))!;
    expect(answer!.conversation).toBe(String(outbound.conversation));
    expect((await w.inbound.message(answer!.message!))!.inReplyTo).toBe(messageId);
    expect(await drainRuns(w)).toBe(1);
    const [report] = await reportsOf(w);
    expect((await w.orm.asSystem().attachments.list({ table: 'Report', key: String(report!.id) }))[0]!.contentHash).toBe(PHOTO_SHA);
    // The outbound message is on the job it was about.
    expect((await w.orm.asSystem().references.referencedBy({ table: 'Job', key: w.job })).items.some(b => b.key === sent.message)).toBe(true);
  });

  test("a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply", async () => {
    w = await world();
    const sys = w.orm.asSystem();
    const eve = String((await sys.contact.createOne({ name: 'Eve' })).id);
    await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.eve, principalKind: 'party', principalTable: 'Contact', principal: eve, trust: 'entered', verifiedAt: new Date() });
    const [outcome] = await w.inbound.inbound('SMS', text(PHONE.eve, 'Can I quote for the fence?'));
    expect(outcome!.outcome).toBe('timeline');
    expect(outcome!.party).toEqual({ table: 'Contact', key: eve });
    const message = (await w.inbound.message(outcome!.message!))!;
    expect(message).toMatchObject({ disposition: 'timeline', triage: 'open', trust: 'entered' });
    expect((await w.inbound.timeline(w.staff, { table: 'Contact', key: eve })).map(m => m.id)).toEqual([outcome!.message!]);
    expect((await w.inbound.triage(w.staff)).map(m => m.id)).toEqual([outcome!.message!]);
    expect((await sys.references.referencedBy({ table: 'Contact', key: eve })).items.map(b => b.key)).toEqual([outcome!.message!]);
    expect(await pendingRuns(w)).toEqual([]);
    // A user's text is a reply, which the notifier runs as the user.
    const [reply] = await w.inbound.inbound('SMS', text(PHONE.ann, 'ack'));
    expect(reply!.outcome).toBe('reply');
    expect(reply!.reply!.principal).toEqual({ kind: 'user', id: w.ann });
    expect(reply!.message).toBeUndefined();
  });

  test('two parties sharing an address are told apart by the thread, and held in triage otherwise', async () => {
    w = await world();
    const sys = w.orm.asSystem();
    const fay = String((await sys.contact.createOne({ name: 'Fay' })).id);
    const gus = String((await sys.contact.createOne({ name: 'Gus' })).id);
    const fence = String((await sys.job.createOne({ title: 'Paint the fence' })).id);
    for (const party of [fay, gus]) {
      await sys.channelIdentity.createOne({ kind: 'email', address: 'office@example.test', principalKind: 'party', principalTable: 'Contact', principal: party, trust: 'verified', verifiedAt: new Date() });
    }
    await issueGrant(sys, { holder: fay, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: w.job });
    const { grant: gusGrant } = await issueGrant(sys, { holder: gus, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: fence });
    const [unthreaded] = await w.inbound.inbound('MAIL', mail('office@example.test', 'Done'));
    expect(unthreaded!.outcome).toBe('unknown');
    expect(unthreaded!.detail).toContain('shared by 2 parties');
    const sent = await w.inbound.send({ as: w.staff, party: { table: 'Contact', key: gus }, channel: 'email', subject: 'The fence', body: 'How is the fence?' });
    const messageId = w.relay.mail.at(-1)!.headers!['Message-ID']!;
    const [threaded] = await w.inbound.inbound('MAIL', mail('office@example.test', 'Fence painted', { inReplyTo: messageId }));
    expect(threaded!.outcome).toBe('run');
    expect(threaded!.party).toEqual({ table: 'Contact', key: gus });
    expect(threaded!.grant).toBe(String(gusGrant.id));
    expect(threaded!.conversation).toBe(String((await w.inbound.message(sent.message!))!.conversation));
    expect(await drainRuns(w)).toBe(1);
    expect((await sys.report.findMany({ jobId: { eq: fence } })).rows.map(r => r.body)).toEqual(['Fence painted']);
    expect(await reportsOf(w)).toEqual([]);
  });

  test('sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP', async () => {
    w = await world({ approve: body => (body.startsWith('Quote') ? 'ask' : body.startsWith('Refund') ? 'deny' : 'allow') });
    const party = { table: 'Contact', key: w.dee };
    await expect(w.inbound.send({ as: { subject: w.ann, kind: 'user', permissions: [] }, party, channel: 'sms', body: 'Hello' })).rejects.toThrow(InboundError);
    // An agent run sends for the person it acts for.
    const agent: Principal = { subject: crypto.randomUUID(), kind: 'agent', permissions: ['parties.message'], actingFor: { kind: 'user', subject: w.ann } };
    const first = await w.inbound.send({ as: agent, party, channel: 'sms', body: 'The gate is booked for Monday.' });
    expect(first.outcome).toBe('sent');
    expect(w.relay.sms.map(m => m.to)).toEqual([PHONE.dee]);
    expect(await w.inbound.message(first.message!)).toMatchObject({ direction: 'outbound', state: 'sent', author: agent.subject, authorKind: 'agent', forSubject: w.ann, via: 'sms' });
    // Asked: held until approved.
    const asked = await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'Quote: 400 for the gate.' });
    expect(asked.outcome).toBe('awaitingApproval');
    expect(w.relay.sms.length).toBe(1);
    expect((await w.inbound.release(asked.message!)).outcome).toBe('sent');
    expect(w.relay.sms.length).toBe(2);
    const denied = await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'Refund issued.' });
    expect(denied).toMatchObject({ outcome: 'refused', detail: 'the approval policy refuses it' });
    expect((await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'See you Monday.' })).outcome).toBe('sent');
    const limited = await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'One more thing.' });
    expect(limited.outcome).toBe('refused');
    expect(limited.detail).toContain('the most it may be');
    expect(w.relay.sms.length).toBe(3);
    // STOP is the adapter's to read and the core's to keep.
    const [stop] = await w.inbound.inbound('SMS', text(PHONE.dee, 'STOP'));
    expect(stop!.outcome).toBe('optOut');
    const [identity] = (await w.orm.asSystem().channelIdentity.findMany({ kind: { eq: 'phone' }, address: { eq: PHONE.dee } })).rows;
    expect(identity!.optedOutAt).toBeTruthy();
    expect(await pendingRuns(w)).toEqual([]);
    expect((await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'Still there?' })).detail).toBe('the address opted out');
    const [start] = await w.inbound.inbound('SMS', text(PHONE.dee, 'start'));
    expect(start!.outcome).toBe('optOut');
    expect((await w.inbound.send({ as: w.staff, party, channel: 'sms', body: 'Welcome back.' })).detail).toContain('the most it may be');
    expect(w.relay.sms.length).toBe(3);
    expect(JSON.stringify(w.relay)).not.toContain(RELAY_TOKEN);
  });
});
