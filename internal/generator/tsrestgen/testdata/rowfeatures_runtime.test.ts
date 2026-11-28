/*
fixture-references-api's references and files end to end (#723, D154), run
by TestGeneratedRowFeaturesEndToEnd with API_DIR set to the materialized
package, SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-references-db's Postgres create.sql and
SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-references-db (createOrm), in a schema of the test's own, with an
authenticator that reads `Authorization: Bearer <subject>[:<permission>,...]`,
and a bucket on an R2 binding whose S3 API a stand-in serves here: it
checks each PUT against the V4 signature of the URL the route signed, as R2
does, before the bytes reach the binding the route reads them back
through. Each call goes through the SDK, whose fetch hands the request to
the app.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createHash, createHmac } from 'node:crypto';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { attachmentBlock } from '@superschematic/http-runtime/postgres';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '@superschematic/http-runtime/r2';
import { createOrm } from '@schemas/fixture-references-db-types/orm';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { FixtureReferencesApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

// The callers, each a subject that is a UUID in its wire form: alice and
// bob each own notes, and carol owns none. alice may also upload to notes.
const alice = 'TKbHsHDO6x9RCjbs5c2u4';
const bob = '1JwchoUsfFfo9IGmolouwH';
const carol = '3cKSm7Pv2EM8Wl4z4XvMh1';
const ALICE = `${alice}:notes.upload`;

/**
 * Establishes the subject and the permissions the bearer credential names,
 * `<subject>[:<permission>,...]`; no credential, no caller.
 */
const authenticate = Object.assign(
  async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization');
    if (!header?.startsWith('Bearer ')) return null;
    const [subject, permissions = ''] = header.slice('Bearer '.length).split(':');
    return { subject: subject!, permissions: permissions === '' ? [] : permissions.split(',') };
  },
  { readsAuthorization: true }
);

/** An r2_bucket binding over a map, as R2 holds objects. */
function memoryR2(): R2BucketBinding & { readonly objects: Map<string, { bytes: Uint8Array; contentType?: string }> } {
  const objects = new Map<string, { bytes: Uint8Array; contentType?: string }>();
  const meta = (key: string): R2ObjectLike => {
    const object = objects.get(key)!;
    return { key, size: object.bytes.length, uploaded: new Date(), ...(object.contentType ? { httpMetadata: { contentType: object.contentType } } : {}) };
  };
  return {
    objects,
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
const bucketName = 'reference-files';

/**
 * R2's S3 API as a stand-in: a PUT to a presigned URL, checked as S3 checks
 * one (AWS Signature Version 4, query-string authentication, unsigned
 * payload), written here from the specification rather than from the
 * runtime's signer, then stored through the binding.
 */
async function s3Put(binding: R2BucketBinding, url: string, headers: Record<string, string>, body: Uint8Array): Promise<number> {
  const u = new URL(url);
  const q = u.searchParams;
  const signedHeaders = q.get('X-Amz-SignedHeaders')!.split(';');
  const sent: Record<string, string> = { host: u.host, ...Object.fromEntries(Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v])) };
  const encode = (s: string) => encodeURIComponent(s).replace(/[!'()*]/g, c => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
  const query = [...q.entries()]
    .filter(([k]) => k !== 'X-Amz-Signature')
    .map(([k, v]) => [encode(k), encode(v)] as const)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([k, v]) => `${k}=${v}`)
    .join('&');
  const canonical = ['PUT', u.pathname, query, signedHeaders.map(h => `${h}:${(sent[h] ?? '').trim()}\n`).join(''), signedHeaders.join(';'), 'UNSIGNED-PAYLOAD'].join('\n');
  const [, scopeRest] = q.get('X-Amz-Credential')!.split(/\/(.*)/s);
  const stamp = q.get('X-Amz-Date')!;
  const toSign = ['AWS4-HMAC-SHA256', stamp, scopeRest, createHash('sha256').update(canonical).digest('hex')].join('\n');
  const secret = createHash('sha256').update(credential.token).digest('hex');
  let key: Buffer = createHmac('sha256', `AWS4${secret}`).update(stamp.slice(0, 8)).digest();
  for (const part of ['auto', 's3', 'aws4_request']) key = createHmac('sha256', key).update(part).digest();
  const signature = createHmac('sha256', key).update(toSign).digest('hex');
  if (signature !== q.get('X-Amz-Signature')) return 403;
  const expires = Date.parse(`${stamp.slice(0, 4)}-${stamp.slice(4, 6)}-${stamp.slice(6, 8)}T${stamp.slice(9, 11)}:${stamp.slice(11, 13)}:${stamp.slice(13, 15)}Z`) + Number(q.get('X-Amz-Expires')) * 1000;
  if (Date.now() > expires) return 403;
  const [, bucket, ...object] = u.pathname.split('/');
  if (bucket !== bucketName) return 404;
  await binding.put(decodeURIComponent(object.join('/')), body, { httpMetadata: { contentType: sent['content-type'] } });
  return 200;
}

const hex = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex');
const text = (s: string) => new TextEncoder().encode(s);

const schema = `row_features_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let r2: ReturnType<typeof memoryR2>;
let app: Hono;

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  r2 = memoryR2();
  const implementations = {
    ticket: {
      getTicket: async (args: { id: string }) => orm.ticket.getOne(args.id as never),
    },
  };
  app = new Hono();
  const attachmentsBucket = openR2Bucket(r2, { name: bucketName, binding: 'REFERENCE_FILES', r2: credential });
  app.route('/', buildRouter(implementations, { authenticate, orm, attachmentsBucket }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
});

afterAll(async () => {
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

/** The SDK as the caller the credential names, or none, its fetch handing each request to the app. */
function sdk(as?: string) {
  return new FixtureReferencesApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    ...(as !== undefined ? { auth: { getToken: async () => as } } : {}),
  });
}

/** The problem a call is refused with: its status and code. */
async function refusal(call: Promise<unknown>): Promise<{ status: number; code?: string }> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    const refused = error as { statusCode: number; code?: string };
    return { status: refused.statusCode, code: refused.code };
  }
  throw new Error('the call was not refused');
}

const ticket = async (title: string) => (await orm.asSystem().ticket.createOne({ title } as never)).id as string;
const note = async (title: string, owner: string, audience: 'everyone' | 'private' = 'everyone') =>
  (await orm.asSystem().note.createOne({ title, owner, audience, createdAt: new Date() } as never)).id as string;

/** Uploads bytes to a ticket as a browser would: a begin through its route, then a PUT to the signed URL. */
const upload = async (as: string, row: string, name: string, bytes: Uint8Array, declared = hex(bytes)) => {
  const begun = await sdk(as).ticket.beginUpload(row, { name, mediaType: 'text/plain', size: bytes.length, sha256: declared });
  expect(begun.attachment.state).toBe('pending');
  expect(begun.upload.method).toBe('PUT');
  expect(await s3Put(r2, begun.upload.url, begun.upload.headers, bytes)).toBe(200);
  return begun.attachment;
};

describe('the references and files of fixture-references-api', () => {
  test('a file uploaded straight to the bucket through the signed URL and posted on one row is filed onto a second row, which lists the first in its backlinks', async () => {
    const thread = await ticket('Quarterly plan');
    const other = await note('Planning notes', alice);
    const bytes = text('The plan, in brief.');
    const pending = await upload(alice, thread, 'plan.txt', bytes);
    // A signature the stand-in did not make is refused, as R2 refuses it.
    const begun = await sdk(alice).ticket.beginUpload(thread, { name: 'x.txt', mediaType: 'text/plain', size: 1, sha256: hex(text('x')) });
    expect(await s3Put(r2, begun.upload.url, { 'content-type': 'text/html' }, text('x'))).toBe(403);
    const file = await sdk(alice).ticket.completeUpload(thread, pending.id);
    expect([file.state, file.size, file.contentHash, file.table, file.key]).toEqual(['ready', bytes.length, hex(bytes), 'Ticket', thread]);
    // Posted on the ticket's thread as an attachment block.
    await orm.withBehaviorCaller({ subject: alice, permissions: [] }).ticket.comments.post(thread, { body: 'The plan.', blocks: [attachmentBlock(file as never)] } as never);
    expect((await sdk(alice).ticket.attachments(thread)).map((f: { id: string }) => f.id)).toEqual([file.id]);
    // Filed onto the note: the same object, no second upload.
    const objects = r2.objects.size;
    const filed = await sdk(alice).ticket.fileAttachment(thread, file.id, { to: { table: 'Note', key: other } });
    expect([filed.table, filed.key, filed.objectKey]).toEqual(['Note', other, file.objectKey]);
    expect(r2.objects.size).toBe(objects);
    expect((await sdk(alice).note.attachments(other)).map((f: { id: string }) => f.id)).toEqual([filed.id]);
    // The note lists the ticket in its backlinks.
    const backlinks = await sdk(alice).note.backlinks(other);
    expect(backlinks.items).toEqual([{ table: 'Ticket', key: thread, via: 'reference', role: 'filed' }]);
    expect(backlinks.hidden ?? null).toBeNull();
    // A download is a signed GET of the object.
    const viewed = await sdk(alice).note.download(other, filed.id);
    expect(viewed.url).toContain(`/${bucketName}/${file.objectKey}?`);
    expect(viewed.attachment.id).toBe(filed.id);
  });

  test('a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count', async () => {
    const shared = await ticket('Shared ticket');
    const hidden = await note("Bob's private note", bob, 'private');
    const quiet = await note("Alice's private note", alice, 'private');
    const open = await note("Alice's open note", alice);
    const system = orm.asSystem();
    await system.references.add({ from: { table: 'Note', key: quiet }, to: { table: 'Note', key: hidden } });
    await system.references.add({ from: { table: 'Note', key: open }, to: { table: 'Note', key: hidden }, role: 'see also' });
    await system.references.add({ from: { table: 'Note', key: open }, to: { table: 'Ticket', key: shared } });
    await system.references.add({ from: { table: 'Note', key: quiet }, to: { table: 'Ticket', key: shared } });

    // Carol reads the open note's references: the private note is left out.
    expect((await sdk(carol).note.references(open)).map((r: { to: unknown }) => r.to)).toEqual([{ table: 'Ticket', key: shared }]);
    // The ticket's backlinks hold the open note alone, and no count.
    const seen = await sdk(carol).ticket.backlinks(shared);
    expect(seen.items).toEqual([{ table: 'Note', key: open, via: 'reference', role: '' }]);
    expect(seen.hidden ?? null).toBeNull();
    // The private note is not there for her, and neither is a way to point at it.
    expect(await refusal(sdk(carol).note.backlinks(hidden))).toMatchObject({ status: 404 });
    expect(await refusal(sdk(carol).note.addReference(open, { to: { table: 'Note', key: hidden } }))).toMatchObject({ status: 404 });
    // Bob owns the private note: he sees the open note, and how many he cannot.
    const owned = await sdk(bob).note.backlinks(hidden);
    expect(owned.items).toEqual([{ table: 'Note', key: open, via: 'reference', role: 'see also' }]);
    expect(owned.hidden).toBe(1);
  });

  test('a reference is added, listed and removed through the routes', async () => {
    const from = await ticket('Login fails');
    const to = await ticket('Session store');
    const added = await sdk(alice).ticket.addReference(from, { to: { table: 'Ticket', key: to }, role: 'blocks' });
    expect([added.from, added.to, added.role]).toEqual([{ table: 'Ticket', key: from }, { table: 'Ticket', key: to }, 'blocks']);
    // Once per target and role: the same add answers the one there.
    expect((await sdk(alice).ticket.addReference(from, { to: { table: 'Ticket', key: to }, role: 'blocks' })).id).toBe(added.id);
    expect((await sdk(alice).ticket.references(from)).map((r: { id: string }) => r.id)).toEqual([added.id]);
    expect((await sdk(alice).ticket.backlinks(to)).items).toEqual([{ table: 'Ticket', key: from, via: 'reference', role: 'blocks' }]);
    // A note points at tickets and notes alone; a ticket at any table.
    expect(await refusal(sdk(alice).ticket.addReference(from, { to: { table: 'Nowhere', key: to } }))).toMatchObject({ status: 422, code: 'not_a_target' });
    expect(await sdk(alice).ticket.removeReference(from, { to: { table: 'Ticket', key: to } })).toEqual({ removed: 1 });
    expect(await sdk(alice).ticket.references(from)).toEqual([]);
  });

  test('an upload whose bytes differ is refused, and a file of another row is not found', async () => {
    const row = await ticket('Bounds');
    const wrong = await upload(alice, row, 'a.txt', text('other bytes'), hex(text('declared bytes')));
    expect(await refusal(sdk(alice).ticket.completeUpload(row, wrong.id))).toEqual({ status: 422, code: 'mismatch' });
    // A file past a ticket's 1 MiB is refused before any URL is signed.
    expect(
      await refusal(sdk(alice).ticket.beginUpload(row, { name: 'big.bin', mediaType: 'application/octet-stream', size: 2 * 1024 * 1024, sha256: hex(text('')) }))
    ).toEqual({ status: 422, code: 'too_large' });
    // Another ticket's file is not this ticket's, by its id or any other way.
    const owner = await ticket('Owner');
    const bytes = text('owned bytes');
    const file = await sdk(alice).ticket.completeUpload(owner, (await upload(alice, owner, 'owned.txt', bytes)).id);
    expect(await refusal(sdk(alice).ticket.download(row, file.id))).toMatchObject({ status: 404 });
    expect(await refusal(sdk(alice).ticket.removeAttachment(row, file.id))).toMatchObject({ status: 404 });
    // Removed through its own row, it is gone.
    expect(await sdk(alice).ticket.removeAttachment(owner, file.id)).toBe(true);
    expect(await sdk(alice).ticket.attachments(owner)).toEqual([]);
  });

  test("a permission the set names for an operation replaces the class's, and a call with no caller is refused", async () => {
    const owned = await note('Typed files', alice);
    const bytes = text('note bytes');
    const input = { name: 'n.txt', mediaType: 'text/plain', size: bytes.length, sha256: hex(bytes) };
    // An upload to a note needs notes.upload, which bob does not hold.
    expect(await refusal(sdk(bob).note.beginUpload(owned, input))).toMatchObject({ status: 403 });
    const begun = await sdk(ALICE).note.beginUpload(owned, input);
    expect(await s3Put(r2, begun.upload.url, begun.upload.headers, bytes)).toBe(200);
    expect((await sdk(alice).note.completeUpload(owned, begun.attachment.id)).state).toBe('ready');
    // Every route needs a caller (@auth).
    expect(await refusal(sdk().note.backlinks(owned))).toMatchObject({ status: 401 });
  });
});

let ids = 0;

/** A JSON-RPC call to the MCP endpoint as the caller the credential names. */
async function mcp(as: string, method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: `Bearer ${as}` },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  const body = await response.text();
  expect(body.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(body.slice('event: message\ndata: '.length));
}

describe('the MCP endpoint of fixture-references-api', () => {
  test('the MCP tools are the reads and the add of a reference', async () => {
    const { tools } = (await mcp(alice, 'tools/list')).result;
    const names = tools.map((tool: { name: string }) => tool.name).filter((name: string) => name.startsWith('ticket_') || name.startsWith('note_'));
    expect(names.sort()).toEqual(
      [
        'note_add_reference',
        'note_attachments',
        'note_backlinks',
        'note_references',
        'ticket_add_reference',
        'ticket_attachments',
        'ticket_backlinks',
        'ticket_references',
      ].sort()
    );
    const from = await ticket('Agent ticket');
    const to = await ticket('Linked ticket');
    const added = (await mcp(alice, 'tools/call', { name: 'ticket_add_reference', arguments: { id: from, to: { table: 'Ticket', key: to } } })).result;
    expect(added.isError).toBeFalsy();
    const backlinks = (await mcp(alice, 'tools/call', { name: 'ticket_backlinks', arguments: { id: to } })).result;
    expect(backlinks.isError).toBeFalsy();
    expect(backlinks.structuredContent).toEqual({ items: [{ table: 'Ticket', key: from, via: 'reference', role: '' }] });
  });
});
