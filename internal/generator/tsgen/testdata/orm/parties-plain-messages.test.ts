// fixture-parties-plain-messages-db end to end (D157): the inbound path
// over messages that keep references and hold no files, in a database
// whose reports hold files. A party's text is filed onto a report and onto
// the granted job once, with no throw: filing asks no table for files it
// does not hold, so the run request is handled the first time and is not
// run again. TestPartiesPlainMessagesEndToEnd runs it on Postgres, and
// TestPartiesPlainMessagesEndToEndOnSQLite with ORM_DIALECT sqlite.
import { afterEach, describe, expect, test } from 'bun:test';
import { createHmac } from 'node:crypto';
import { createInbound, createNotifier, issueGrant, type Inbound, type RunRequest } from '@superschematic/http-runtime/postgres';
import { providersFrom, SecretValue } from '@superschematic/http-runtime/providers';
import type { FixturePartiesPlainMessagesDbOrm } from './orm';
import { noticeWorkQueue, runRequestQueue } from './queues';
import { available, featureDatabase, ormEntry, type FeatureDatabase } from './dialect';
import { smsRelayA } from './sms-relay-a';

const RELAY_TOKEN = 'relay-token';
const PHONE = '+15550100011';
const PHOTO = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4]);

/** The SMS relay as a stand-in: its health, a text's photo, and what it was sent. */
const relayFetch = (async (input: string | URL | Request): Promise<Response> => {
  const url = new URL(String(input));
  if (url.pathname === '/v1/health') return new Response('{}', { status: 200 });
  if (url.pathname === '/media/1') return new Response(PHOTO, { headers: { 'content-type': 'image/jpeg' } });
  return Response.json({ id: `sms-${crypto.randomUUID()}`, status: 'accepted' });
}) as typeof globalThis.fetch;

/** A bucket that holds nothing: the messages hold no files, so none reaches it. */
const bucket = {
  name: 'memory/files',
  put: async () => {
    throw new Error('no file reaches the bucket');
  },
  get: async (name: string) => {
    throw new Error(`no object ${name}`);
  },
  delete: async () => {},
  list: async () => ({ objects: [] }),
  signedUrl: async (name: string) => `https://files.test/${name}`,
};

/** A text from the relay, signed as it signs one. */
function text(from: string, body: string, extra: Record<string, unknown> = {}) {
  const payload = JSON.stringify({ messages: [{ id: `in-${crypto.randomUUID()}`, from, text: body, at: new Date().toISOString(), ...extra }] });
  return { headers: { 'x-relay-signature': createHmac('sha256', RELAY_TOKEN).update(payload).digest('hex') }, body: payload };
}

interface World {
  db: FeatureDatabase;
  orm: FixturePartiesPlainMessagesDbOrm;
  inbound: Inbound;
  dee: string;
  job: string;
  grant: string;
}

async function world(): Promise<World> {
  const db = await featureDatabase('plainmessages');
  const orm = (await ormEntry<FixturePartiesPlainMessagesDbOrm>()).createOrm(db.source);
  const sys = orm.asSystem();
  const providers = providersFrom({
    slots: { SMS: 'channel' },
    config: { SMS: 'sms-relay-a', SMS_URL: 'https://sms.test', SMS_TOKEN: new SecretValue(RELAY_TOKEN) },
    definitions: [smsRelayA({ fetch: relayFetch })],
  });
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue, providers });
  const inbound = createInbound({ orm, notifier, runs: runRequestQueue, providers, bucket: bucket as never });
  await sys.role.createOne({ name: 'contractor', permissions: ['reports.write'], reaches: ['Report:job'] });
  const dee = String((await sys.contact.createOne({ name: 'Dee' })).id);
  const job = String((await sys.job.createOne({ title: 'Fix the gate' })).id);
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE, principalKind: 'party', principalTable: 'Contact', principal: dee, trust: 'verified', verifiedAt: new Date() });
  const { grant } = await issueGrant(sys, { holder: dee, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: job });
  return { db, orm, inbound, dee, job, grant: String(grant.id) };
}

describe.skipIf(!available)('parties with plain messages', () => {
  let w: World;
  afterEach(async () => {
    await w?.db.drop();
  });

  test('a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files', async () => {
    w = await world();
    const [outcome] = await w.inbound.inbound('SMS', text(PHONE, 'Gate fixed', { media: [{ url: 'https://sms.test/media/1', type: 'image/jpeg', name: 'gate.jpg' }] }));
    expect(outcome!.outcome).toBe('run');
    expect(outcome!.grant).toBe(w.grant);
    // The photo is dropped, and the message says why: its table holds no files.
    const message = (await w.inbound.message(outcome!.message!))!;
    expect(String(message.detail)).toContain('no @attachments');
    // The triage asks the messages table for no files either: it lists none.
    const staff = { subject: crypto.randomUUID(), kind: 'user' as const, permissions: ['parties.triage'] };
    expect(await w.inbound.attachmentsOf(staff, outcome!.message!)).toEqual([]);

    // The run request's handler, as the party through its grant: a report
    // on the granted job, the message filed onto it and onto the job. A
    // handler that throws is a run request that is run again.
    let runs = 0;
    let report = '';
    const handle = async (request: RunRequest): Promise<void> => {
      runs++;
      const party = await w.inbound.principalOf(request.party, { grant: request.grant });
      const grant = party.grants![0]!;
      report = String((await w.orm.as(party).report.createOne({ jobId: grant.targetId, body: 'Gate fixed' })).id);
      await w.inbound.file({ message: request.message, to: { table: 'Report', key: report }, as: party });
      await w.inbound.file({ message: request.message, to: { table: grant.targetType, key: grant.targetId }, as: party });
    };
    const queue = w.inbound.runWorker(w.db.source, handle);
    const claims = await queue.claim(10, 30);
    expect(claims.length).toBe(1);
    await claims[0]!.handle(new AbortController().signal);
    await queue.complete(claims[0]!.id, claims[0]!.token);
    expect(runs).toBe(1);
    // Nothing is left to run again.
    expect((await queue.claim(10, 30)).length).toBe(0);

    // The message is on the report, the job and the party, and the report holds no file of it.
    const sys = w.orm.asSystem();
    for (const row of [{ table: 'Report', key: report }, { table: 'Job', key: w.job }, { table: 'Contact', key: w.dee }]) {
      const backlinks = await sys.references.referencedBy(row);
      expect(backlinks.items.filter(b => b.table === 'Message' && b.key === outcome!.message).length).toBe(1);
    }
    expect(await sys.attachments.list({ table: 'Report', key: report })).toEqual([]);
    // Filing it again changes nothing.
    const party = await w.inbound.principalOf({ table: 'Contact', key: w.dee });
    await w.inbound.file({ message: outcome!.message!, to: { table: 'Report', key: report }, as: party });
    expect((await sys.references.referencedBy({ table: 'Report', key: report })).items.filter(b => b.table === 'Message').length).toBe(1);
  });
});
