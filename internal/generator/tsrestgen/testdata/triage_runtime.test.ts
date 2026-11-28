/*
fixture-triage-api's triage end to end (#746, #606), run by
TestGeneratedTriageEndToEnd with API_DIR set to the materialized package,
CREATE_SQL to fixture-parties-db's Postgres create.sql and
SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL to the database. The generated
router serves the API over the inbound path the runtime builds over
fixture-parties-db's generated repositories (createInbound, as the
generated runtime.ts builds Deps.inbound), in a schema of the test's own,
with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`, and a bucket on an R2
binding over a map, which holds a held message's files. Messages reach the
inbound path through receive, as an adapter's route hands them down it.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { createInbound, createNotifier, issueGrant, type Inbound } from '@superschematic/http-runtime/postgres';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '@superschematic/http-runtime/r2';
import { createOrm } from '@schemas/fixture-parties-db-types/orm';
import { noticeWorkQueue, runRequestQueue } from '@schemas/fixture-parties-db-types/queues';

const apiDir = process.env.API_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL;
if (!apiDir || !createSQL || !dsn) throw new Error('API_DIR, CREATE_SQL and SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

/**
 * Establishes the subject and the permissions the bearer credential names,
 * `<subject>[:<permission>,...]`; no credential, no caller.
 */
const authenticate = Object.assign(
  async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization');
    if (!header?.startsWith('Bearer ')) return null;
    const [subject, permissions = ''] = header.slice('Bearer '.length).split(':');
    return { subject: subject!, kind: 'user', permissions: permissions === '' ? [] : permissions.split(',') };
  },
  { readsAuthorization: true }
);

/** An r2_bucket binding over a map, as R2 holds objects. */
function memoryR2(): R2BucketBinding {
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  const meta = (key: string): R2ObjectLike => {
    const object = objects.get(key)!;
    return { key, size: object.bytes.length, uploaded: new Date(), ...(object.contentType ? { httpMetadata: { contentType: object.contentType } } : {}) };
  };
  return {
    async put(key: string, value: R2PutValue, options?: { httpMetadata?: { contentType?: string } }) {
      const bytes = typeof value === 'string' ? new TextEncoder().encode(value) : new Uint8Array(value instanceof ArrayBuffer ? value : (value as ArrayBufferView).buffer);
      objects.set(key, { bytes, ...(options?.httpMetadata?.contentType ? { contentType: options.httpMetadata.contentType } : {}) });
      return meta(key);
    },
    async get(key: string) {
      return objects.has(key) ? { ...meta(key), body: new Response(objects.get(key)!.bytes).body! } : null;
    },
    async head(key: string) {
      return objects.has(key) ? meta(key) : null;
    },
    async delete(keys: string | string[]) {
      for (const key of [keys].flat()) objects.delete(key);
    },
    async list() {
      return { objects: [...objects.keys()].sort().map(meta), truncated: false };
    },
  };
}

const credential = { account: '0123456789abcdef0123456789abcdef', accessKeyId: '8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d', token: 'r2-token-value' };

const schema = `triage_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let inbound: Inbound;
let app: Hono;
let staff: string;
let clerk: string;
let fay: string;
let grant: string;

/** The phone numbers the texts come from. */
const PHONE = { stranger: '+15550100042', other: '+15550100043', fay: '+15550100041', staff: '+15550100001' };

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  const bucket = openR2Bucket(memoryR2(), { name: 'message-files', binding: 'MESSAGE_FILES', r2: credential });
  const notifier = createNotifier({ orm, dispatch: noticeWorkQueue });
  inbound = createInbound({ orm, notifier, runs: runRequestQueue, bucket });
  const sys = orm.asSystem();
  await sys.role.createOne({ name: 'contractor', permissions: ['reports.write'], reaches: ['Report:job'] });
  staff = String((await sys.account.createOne({ email: 'staff@example.test' })).id);
  clerk = String((await sys.account.createOne({ email: 'clerk@example.test' })).id);
  // A user's address the user has not verified: a text from it is held as
  // an unknown sender's, and it is no party's to be settled onto.
  await sys.channelIdentity.createOne({ kind: 'phone', address: PHONE.staff, principal: staff, trust: 'unverified' });
  fay = String((await sys.contact.createOne({ name: 'Fay' })).id);
  const job = String((await sys.job.createOne({ title: 'Fix the gate' })).id);
  grant = String((await issueGrant(sys, { holder: fay, holderKind: 'party', role: 'contractor', targetType: 'Job', targetId: job })).grant.id);
  app = new Hono();
  app.route('/', buildRouter({ contact: { listContacts: async () => [] } }, { authenticate, inbound }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
});

afterAll(async () => {
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

/** The staff member, who holds the triage permission, and a clerk, who does not. */
const STAFF = () => `${staff}:parties.triage`;
const CLERK = () => `${clerk}:contacts.read`;

/** A call of the route as the caller the credential names, or none: its status and its envelope's data or its problem. */
async function call(method: string, path: string, as?: string, body?: unknown): Promise<{ status: number; data: any }> {
  const response = await app.request(`http://api.example.com${path}`, {
    method,
    headers: { ...(as ? { authorization: `Bearer ${as}` } : {}), ...(body !== undefined ? { 'content-type': 'application/json' } : {}) },
    ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
  });
  const json: any = await response.json();
  return { status: response.status, data: response.ok ? json.data : json };
}

/** A text received on the SMS slot, which the inbound path holds when it knows no one by the address. */
async function text(from: string, body: string, attachments: { name: string; mediaType: string; bytes: Uint8Array }[] = []): Promise<string> {
  const outcome = await inbound.receive({ channel: 'sms', slot: 'SMS', from, body, providerMessageId: `in-${crypto.randomUUID()}`, ...(attachments.length ? { attachments } : {}) });
  expect(outcome.outcome).toBe('unknown');
  return outcome.message!;
}

/** Closes every message still held, so each case starts from an empty triage. */
async function emptyTriage(): Promise<void> {
  const held = await inbound.triage({ subject: staff, kind: 'user', permissions: ['parties.triage'] }, { limit: 500 });
  for (const row of held) await inbound.settle({ subject: staff, kind: 'user', permissions: ['parties.triage'] }, String(row.id));
}

describe('the triage routes', () => {
  test('a held message is listed only to a caller who holds the triage permission, a page at a time and by channel', async () => {
    await emptyTriage();
    const first = await text(PHONE.stranger, 'Is the gate fixed?');
    const second = await text(PHONE.other, 'Wrong number?');
    const mail = await inbound.receive({ channel: 'email', slot: 'MAIL', from: 'someone@example.test', subject: 'Hello', body: 'Who is this?', providerMessageId: `in-${crypto.randomUUID()}` });
    expect(mail.outcome).toBe('unknown');

    const all = await call('GET', '/api/triage', STAFF());
    expect(all.status).toBe(200);
    expect(all.data.items.map((m: any) => m.id)).toEqual([mail.message, second, first]);
    expect(all.data.next).toBeNull();
    // Nothing is shown to a caller without the permission, nor to no one.
    expect((await call('GET', '/api/triage', CLERK())).status).toBe(403);
    expect((await call('GET', `/api/triage/${first}`, CLERK())).status).toBe(403);
    expect((await call('GET', '/api/triage')).status).toBe(401);
    // A page at a time, by the cursor the page before answers.
    const page = await call('GET', '/api/triage?limit=2', STAFF());
    expect(page.data.items.map((m: any) => m.id)).toEqual([mail.message, second]);
    expect(typeof page.data.next).toBe('string');
    const rest = await call('GET', `/api/triage?limit=2&cursor=${page.data.next}`, STAFF());
    expect(rest.data.items.map((m: any) => m.id)).toEqual([first]);
    expect(rest.data.next).toBeNull();
    expect((await call('GET', '/api/triage?cursor=bm90LWEtY3Vyc29y', STAFF())).status).toBe(400);
    // By channel.
    const texts = await call('GET', '/api/triage?channel=sms', STAFF());
    expect(texts.data.items.map((m: any) => m.id)).toEqual([second, first]);
    const mails = await call('GET', '/api/triage?channel=email', STAFF());
    expect(mails.data.items).toMatchObject([{ id: mail.message, channel: 'email', fromAddress: 'someone@example.test', subject: 'Hello' }]);
  });

  test('a held message shows its sender, its channel, its body and its files', async () => {
    await emptyTriage();
    const photo = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1, 2, 3, 4]);
    const id = await text(PHONE.stranger, 'Photo of the gate', [{ name: 'gate.png', mediaType: 'image/png', bytes: photo }]);
    const { status, data } = await call('GET', `/api/triage/${id}`, STAFF());
    expect(status).toBe(200);
    expect(data).toMatchObject({ id, channel: 'sms', slot: 'SMS', fromAddress: PHONE.stranger, body: 'Photo of the gate', state: 'open', disposition: 'unknown' });
    expect(data.attachments).toHaveLength(1);
    expect(data.attachments[0]).toMatchObject({ name: 'gate.png', mediaType: 'image/png', size: photo.length });
    // The list shows its files too.
    const listed = await call('GET', '/api/triage', STAFF());
    expect(listed.data.items[0].attachments[0]).toMatchObject({ name: 'gate.png' });
    // A message that was never held is no held message.
    expect((await call('GET', `/api/triage/${crypto.randomUUID()}`, STAFF())).status).toBe(404);
  });

  test("settling a held message onto a party files it as the party's and takes it out of triage", async () => {
    await emptyTriage();
    const id = await text(PHONE.fay, 'Gate fixed');
    expect((await call('POST', `/api/triage/${id}/settle`, CLERK(), { party: fay, trust: 'entered' })).status).toBe(403);
    const settled = await call('POST', `/api/triage/${id}/settle`, STAFF(), { party: fay, trust: 'entered' });
    expect(settled.status).toBe(200);
    expect(settled.data).toEqual({ outcome: 'run', message: id, party: fay, grant });
    expect((await call('GET', '/api/triage', STAFF())).data.items).toEqual([]);
    const message = await call('GET', `/api/triage/${id}`, STAFF());
    expect(message.data).toMatchObject({ state: 'closed', partyTable: 'Contact', party: fay, trust: 'entered', disposition: 'run', closedBy: staff });
    // Filed onto the party: the contact's row lists the message.
    const backlinks = await orm.asSystem().references.referencedBy({ table: 'Contact', key: fay });
    expect(backlinks.items.some(b => b.table === 'Message' && b.key === id)).toBe(true);
    // The address is the contact's now: its next text reaches the contact.
    const next = await inbound.receive({ channel: 'sms', slot: 'SMS', from: PHONE.fay, body: 'Back tomorrow', providerMessageId: `in-${crypto.randomUUID()}` });
    expect(next).toMatchObject({ outcome: 'run', party: { table: 'Contact', key: fay } });
  });

  test('settling onto a party the caller cannot read, or onto an address that is a user\'s, is refused', async () => {
    await emptyTriage();
    const id = await text(PHONE.other, 'Hello?');
    expect((await call('POST', `/api/triage/${id}/settle`, STAFF(), { party: crypto.randomUUID(), trust: 'entered' })).status).toBe(404);
    expect((await call('POST', `/api/triage/${id}/settle`, STAFF(), { party: fay, trust: 'sometimes' })).status).toBe(400);
    const users = await text(PHONE.staff, 'It is me');
    const refused = await call('POST', `/api/triage/${users}/settle`, STAFF(), { party: fay, trust: 'entered' });
    expect(refused.status).toBe(422);
    expect(refused.data.detail).toContain("the address is a user's");
    // Both are still held.
    expect((await call('GET', '/api/triage', STAFF())).data.items.map((m: any) => m.id)).toEqual([users, id]);
  });

  test('dismissing a held message takes it out of triage, and a second dismiss or a settle of it is a conflict', async () => {
    await emptyTriage();
    const id = await text(PHONE.other, 'Buy now');
    expect((await call('POST', `/api/triage/${id}/dismiss`, CLERK())).status).toBe(403);
    const dismissed = await call('POST', `/api/triage/${id}/dismiss`, STAFF());
    expect(dismissed.status).toBe(200);
    expect(dismissed.data).toMatchObject({ id, state: 'closed', closedBy: staff });
    expect((await call('GET', '/api/triage', STAFF())).data.items).toEqual([]);
    expect((await call('POST', `/api/triage/${id}/dismiss`, STAFF())).status).toBe(409);
    expect((await call('POST', `/api/triage/${id}/settle`, STAFF(), { party: fay, trust: 'entered' })).status).toBe(409);
    // It stays on its conversation, with no party.
    expect(await inbound.message(id)).toMatchObject({ triage: 'closed', party: null });
  });
});
