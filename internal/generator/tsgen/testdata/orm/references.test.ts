// fixture-references-db end to end (D154): tickets with a conversation,
// references and files; notes with an audience, references to tickets and
// notes, and files of a few media types. TestReferencesTablesEndToEnd
// generates the package, type-checks it, applies the generated create.sql
// in a schema of its own on Postgres and runs these cases through the
// generated repositories' orm.references and orm.attachments, over a
// bucket on an R2 binding whose S3 API a stand-in serves here: it checks
// each PUT against the V4 signature of the URL the API signed, as R2 does,
// before the bytes reach the binding the API reads them back through.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { createHash, createHmac } from 'node:crypto';
import { NotFoundError, RowFeatureError, attachmentBlock } from '@superschematic/http-runtime/postgres';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '@superschematic/http-runtime/r2';
import { createOrm, type FixtureReferencesDbOrm } from './orm';
import { alice, bob, database, dsn, type TestDatabase } from './harness';

const carol = '3cKSm7Pv2EM8Wl4z4XvMh1';

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
const bucketName = 'records-files';

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

describe.skipIf(dsn === '')('references and attachments on Postgres', () => {
  let db: TestDatabase;
  let orm: FixtureReferencesDbOrm;
  let r2: ReturnType<typeof memoryR2>;
  const bucket = () => openR2Bucket(r2, { name: bucketName, binding: 'RECORDS_FILES', r2: credential });

  beforeAll(async () => {
    db = await database('ts_orm_references');
    orm = createOrm(db.pool, alice);
    r2 = memoryR2();
  });

  afterAll(async () => {
    await db?.drop();
  });

  const ticket = async (title: string) => (await orm.ticket.createOne({ title } as never)).id as string;
  const note = async (title: string, owner: string, audience: 'everyone' | 'private' = 'everyone') =>
    (await orm.asSystem().note.createOne({ title, owner, audience, createdAt: new Date() } as never)).id as string;

  /** Uploads bytes to a row as a browser would: begin, a PUT to the signed URL, complete. */
  const upload = async (table: string, key: string, name: string, mediaType: string, bytes: Uint8Array, declared = hex(bytes)) => {
    const { attachment, upload } = await orm.attachments.begin({ to: { table, key }, name, mediaType, size: bytes.length, sha256: declared }, { bucket: bucket() });
    expect(attachment.state).toBe('pending');
    expect(await s3Put(r2, upload.url, upload.headers, bytes)).toBe(200);
    return attachment;
  };

  test("a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks", async () => {
    const thread = await ticket('Quarterly plan');
    const other = await note('Planning notes', alice);
    const bytes = text('The plan, in brief.');
    const pending = await upload('Ticket', thread, 'plan.txt', 'text/plain', bytes);
    // A signature the stand-in did not make is refused, as R2 refuses it.
    const begun = await orm.attachments.begin({ to: { table: 'Ticket', key: thread }, name: 'x.txt', mediaType: 'text/plain', size: 1, sha256: hex(text('x')) }, { bucket: bucket() });
    expect(await s3Put(r2, begun.upload.url, { 'content-type': 'text/html' }, text('x'))).toBe(403);
    const file = await orm.attachments.complete({ table: 'Ticket', id: pending.id }, { bucket: bucket() });
    expect([file.state, file.size, file.contentHash, file.extraction]).toEqual(['ready', bytes.length, hex(bytes), 'pending']);
    // Posted on the ticket's thread as an attachment block.
    const posted = await orm.withBehaviorCaller({ subject: alice, permissions: [] }).ticket.comments.post(thread, { body: 'The plan.', blocks: [attachmentBlock(file)] } as never);
    expect((posted as any).blocks[0]).toEqual({ kind: 'attachment', uri: `attachment:${file.id}`, name: 'plan.txt', mediaType: 'text/plain', bytes: bytes.length, sha256: hex(bytes) });
    // Filed onto the note: the same object, no second upload.
    const objects = r2.objects.size;
    const filed = await orm.attachments.file({ table: 'Ticket', id: file.id, to: { table: 'Note', key: other } });
    expect([filed.table, filed.key, filed.objectKey, filed.filedFrom]).toEqual(['Note', other, file.objectKey, `Ticket:${thread}:${file.id}`]);
    expect(r2.objects.size).toBe(objects);
    expect((await orm.attachments.list({ table: 'Note', key: other })).map(f => f.id)).toEqual([filed.id]);
    expect((await orm.attachments.list({ table: 'Ticket', key: thread })).map(f => f.id)).toEqual([file.id]);
    // The note lists the ticket in its backlinks.
    const backlinks = await orm.references.referencedBy({ table: 'Note', key: other });
    expect(backlinks).toEqual({ items: [{ table: 'Ticket', key: thread, via: 'reference', role: 'filed' }] });
    // A download is a signed GET, and asks the file's preview on its first view.
    const viewed = await orm.attachments.download({ table: 'Note', id: filed.id }, { bucket: bucket() });
    expect(viewed.url).toContain(`/${bucketName}/${file.objectKey}?`);
    expect(viewed.attachment.preview).toBe('pending');
    // A processor's work lands on every file that names the object.
    expect(await orm.attachments.pending('extraction')).toContainEqual({ objectKey: file.objectKey, mediaType: 'text/plain', contentHash: hex(bytes) });
    expect(await orm.attachments.processed({ objectKey: file.objectKey, extraction: { state: 'done', text: 'The plan, in brief.' } })).toBe(2);
  });

  test("a ref block in a row's thread is a reference of the row, which the target lists", async () => {
    const from = await ticket('Login fails');
    const to = await ticket('Session store');
    await orm.withBehaviorCaller({ subject: alice, permissions: [] }).ticket.comments.post(from, { body: 'See the session store.', blocks: [{ kind: 'ref', schema: 'Ticket', id: to }] } as never);
    const backlinks = await orm.references.referencedBy({ table: 'Ticket', key: to });
    expect(backlinks.items).toEqual([{ table: 'Ticket', key: from, via: 'comment', message: 1 }]);
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

    const asCarol = createOrm(db.pool, carol);
    // Carol reads the open note's references: the private note is left out.
    expect((await asCarol.references.of({ table: 'Note', key: open })).map(r => r.to)).toEqual([{ table: 'Ticket', key: shared }]);
    // The ticket's backlinks hold the open note alone, and no count.
    expect(await asCarol.references.referencedBy({ table: 'Ticket', key: shared })).toEqual({ items: [{ table: 'Note', key: open, via: 'reference', role: '' }] });
    // The private note is not there for her, and neither is a way to point at it.
    await expect(asCarol.references.referencedBy({ table: 'Note', key: hidden })).rejects.toBeInstanceOf(NotFoundError);
    await expect(asCarol.references.add({ from: { table: 'Note', key: open }, to: { table: 'Note', key: hidden } })).rejects.toBeInstanceOf(NotFoundError);
    // Bob owns the private note: he sees the open note, and how many he cannot.
    const asBob = createOrm(db.pool, bob);
    expect(await asBob.references.referencedBy({ table: 'Note', key: hidden })).toEqual({ items: [{ table: 'Note', key: open, via: 'reference', role: 'see also' }], hidden: 1 });
    // Alice sees her own two notes point at the ticket.
    expect((await orm.references.referencedBy({ table: 'Ticket', key: shared })).items.map(b => b.key).sort()).toEqual([open, quiet].sort());
    // A note points at tickets and notes alone.
    const other = await ticket('Elsewhere');
    await expect(orm.references.add({ from: { table: 'Ticket', key: other }, to: { table: 'Ticket', key: shared } })).resolves.toMatchObject({ role: '' });
  });

  test("a target's delete clears the references to it and tells each row that held one", async () => {
    const target = await ticket('Short-lived');
    const holder = await note('Pointing note', alice);
    await orm.references.add({ from: { table: 'Note', key: holder }, to: { table: 'Ticket', key: target }, role: 'blocks' });
    await orm.ticket.deleteOne(target as never);
    expect(await orm.references.of({ table: 'Note', key: holder })).toEqual([]);
    const cleared = await db.pool.query(`SELECT holder_table, holder_key, target_table, role FROM reference_cleared_queue WHERE target_key = $1`, [
      (await db.pool.query('SELECT $1::uuid::text AS k', [hyphen(target)])).rows[0].k,
    ]);
    expect(cleared.rows).toEqual([{ holder_table: 'Note', holder_key: hyphen(holder), target_table: 'Ticket', role: 'blocks' }]);
  });

  test("an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it", async () => {
    const row = await ticket('Bounds');
    // Bytes that are not the ones declared are refused, and the file goes.
    const wrong = await upload('Ticket', row, 'a.txt', 'text/plain', text('other bytes'), hex(text('declared bytes')).slice(0, 64));
    const refused = await orm.attachments.complete({ table: 'Ticket', id: wrong.id }, { bucket: bucket() }).catch(e => e);
    expect(refused).toBeInstanceOf(RowFeatureError);
    expect(refused.code).toBe('mismatch');
    await expect(orm.attachments.get({ table: 'Ticket', id: wrong.id })).rejects.toBeInstanceOf(NotFoundError);
    expect((await orm.attachments.release({ objectKey: wrong.objectKey }, { bucket: bucket() })).deleted).toBe(true);
    expect(r2.objects.has(wrong.objectKey)).toBe(false);
    // A file past a ticket's 1 MiB, and a note's media type, are refused.
    const big = await orm.attachments
      .begin({ to: { table: 'Ticket', key: row }, name: 'big.bin', mediaType: 'application/octet-stream', size: 2 * 1024 * 1024, sha256: hex(text('')) }, { bucket: bucket() })
      .catch(e => e);
    expect(big.code).toBe('too_large');
    const owned = await note('Typed files', alice);
    const zip = await orm.attachments
      .begin({ to: { table: 'Note', key: owned }, name: 'a.zip', mediaType: 'application/zip', size: 10, sha256: hex(text('')) }, { bucket: bucket() })
      .catch(e => e);
    expect(zip.code).toBe('media_type');
    // Four files of 1 MiB fill a ticket's 4 MiB: a fifth is refused.
    for (let i = 0; i < 4; i++) {
      await orm.attachments.begin({ to: { table: 'Ticket', key: row }, name: `p${i}.bin`, mediaType: 'application/octet-stream', size: 1024 * 1024, sha256: hex(text(`${i}`)) }, { bucket: bucket() });
    }
    const full = await orm.attachments
      .begin({ to: { table: 'Ticket', key: row }, name: 'p5.bin', mediaType: 'application/octet-stream', size: 1, sha256: hex(text('5')) }, { bucket: bucket() })
      .catch(e => e);
    expect(full.code).toBe('row_full');
    // A file filed onto a note keeps its object until the last file goes.
    const owner = await ticket('Owner');
    const bytes = text('shared bytes');
    const file = await orm.attachments.complete({ table: 'Ticket', id: (await upload('Ticket', owner, 'shared.txt', 'text/plain', bytes)).id }, { bucket: bucket() });
    const moved = await orm.attachments.file({ table: 'Ticket', id: file.id, to: { table: 'Note', key: owned }, move: true });
    await expect(orm.attachments.get({ table: 'Ticket', id: file.id })).rejects.toBeInstanceOf(NotFoundError);
    expect((await orm.attachments.release({ objectKey: file.objectKey }, { bucket: bucket() })).deleted).toBe(false);
    await orm.attachments.detach({ table: 'Note', id: moved.id });
    const released = await db.pool.query(`SELECT reason FROM attachment_released_queue WHERE object_key = $1 ORDER BY created_at`, [file.objectKey]);
    expect(released.rows.map(r => r.reason)).toEqual(['expired', 'detached', 'detached']);
    expect((await orm.attachments.release({ objectKey: file.objectKey }, { bucket: bucket() })).deleted).toBe(true);
    expect(r2.objects.has(file.objectKey)).toBe(false);
  });
});

/** A UUID's hyphenated text, as the database writes it. */
function hyphen(base62: string): string {
  const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';
  let n = 0n;
  for (const c of base62) n = n * 62n + BigInt(BASE62.indexOf(c));
  const h = n.toString(16).padStart(32, '0');
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}
