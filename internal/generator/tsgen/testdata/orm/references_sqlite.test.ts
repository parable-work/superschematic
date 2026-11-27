// fixture-references-db end to end on SQLite (D182): the cases of
// references.test.ts, through the repositories of the ./orm/sqlite entry
// on openSqlite's connection to a file the package's sqlite/create.sql made.
// TestReferencesTablesEndToEndOnSQLite writes the file's create.sql beside
// this test and runs it; it needs no database server. The tickets keep a
// conversation (Comments), whose behaviors the SQLite repositories run
// through the core (D186): a ticket is created and its thread's message
// posted as on Postgres. Every read and every write of references and files
// goes through orm.references and orm.attachments, over a bucket on an R2
// binding whose S3 API a stand-in serves, as on Postgres.
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { Database } from 'bun:sqlite';
import { createHash, createHmac } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { NotFoundError, RowFeatureError, uuidFromDb, uuidToDb } from '@superschematic/http-runtime/d1';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '@superschematic/http-runtime/r2';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import type { FixtureReferencesDbOrm } from './orm';
import { createOrm } from './orm-sqlite';
import { alice, bob } from './harness';

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

/** R2's S3 API as a stand-in: a PUT to a presigned URL, checked against its V4 signature, then stored through the binding. */
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
  const [, bucket, ...object] = u.pathname.split('/');
  if (bucket !== bucketName) return 404;
  await binding.put(decodeURIComponent(object.join('/')), body, { httpMetadata: { contentType: sent['content-type'] } });
  return 200;
}

const hex = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex');
const text = (s: string) => new TextEncoder().encode(s);

describe('references and attachments on SQLite', () => {
  let dir: string;
  let db: SqliteDatabase;
  let orm: FixtureReferencesDbOrm;
  let r2: ReturnType<typeof memoryR2>;
  const bucket = () => openR2Bucket(r2, { name: bucketName, binding: 'RECORDS_FILES', r2: credential });

  beforeAll(() => {
    dir = mkdtempSync(join(tmpdir(), 'ts-orm-references-sqlite-'));
    const file = join(dir, 'references.sqlite');
    const raw = new Database(file, { create: true });
    raw.exec(readFileSync(new URL('./sqlite/create.sql', import.meta.url), 'utf8'));
    raw.close();
    db = openSqlite(file);
    orm = createOrm(db, alice);
    r2 = memoryR2();
  });

  afterAll(() => {
    db?.close();
    rmSync(dir, { recursive: true, force: true });
  });

  // A ticket composes Comments: its create runs through the core (D186).
  const ticket = async (title: string) => (await orm.ticket.createOne({ title } as never)).id as string;
  const note = async (title: string, owner: string, audience: 'everyone' | 'private' = 'everyone') =>
    (await orm.asSystem().note.createOne({ title, owner, audience, createdAt: new Date() } as never)).id as string;

  /** Uploads bytes to a row as a browser would: begin, a PUT to the signed URL. */
  const upload = async (table: string, key: string, name: string, mediaType: string, bytes: Uint8Array, declared = hex(bytes)) => {
    const { attachment, upload } = await orm.attachments.begin({ to: { table, key }, name, mediaType, size: bytes.length, sha256: declared }, { bucket: bucket() });
    expect(attachment.state).toBe('pending');
    expect(await s3Put(r2, upload.url, upload.headers, bytes)).toBe(200);
    return attachment;
  };

  test("a file of one row is filed onto a second row, which lists the first in its backlinks", async () => {
    const thread = await ticket('Quarterly plan');
    const other = await note('Planning notes', alice);
    const bytes = text('The plan, in brief.');
    const pending = await upload('Ticket', thread, 'plan.txt', 'text/plain', bytes);
    expect(pending.expiresAt).toBeInstanceOf(Date);
    const file = await orm.attachments.complete({ table: 'Ticket', id: pending.id }, { bucket: bucket() });
    expect([file.state, file.size, file.contentHash, file.extraction]).toEqual(['ready', bytes.length, hex(bytes), 'pending']);
    expect(file.uploadedAt).toBeInstanceOf(Date);
    const objects = r2.objects.size;
    const filed = await orm.attachments.file({ table: 'Ticket', id: file.id, to: { table: 'Note', key: other } });
    expect([filed.table, filed.key, filed.objectKey, filed.filedFrom]).toEqual(['Note', other, file.objectKey, `Ticket:${thread}:${file.id}`]);
    expect(r2.objects.size).toBe(objects);
    expect((await orm.attachments.list({ table: 'Note', key: other })).map(f => f.id)).toEqual([filed.id]);
    expect((await orm.attachments.get({ table: 'Ticket', id: file.id })).name).toBe('plan.txt');
    const backlinks = await orm.references.referencedBy({ table: 'Note', key: other });
    expect(backlinks).toEqual({ items: [{ table: 'Ticket', key: thread, via: 'reference', role: 'filed' }] });
    const viewed = await orm.attachments.download({ table: 'Note', id: filed.id }, { bucket: bucket() });
    expect(viewed.attachment.preview).toBe('pending');
    expect(await orm.attachments.pending('extraction')).toContainEqual({ objectKey: file.objectKey, mediaType: 'text/plain', contentHash: hex(bytes) });
    expect(await orm.attachments.processed({ objectKey: file.objectKey, extraction: { state: 'done', text: 'The plan, in brief.' } })).toBe(2);
    expect(await orm.attachments.pending('extraction')).not.toContainEqual(expect.objectContaining({ objectKey: file.objectKey }));
  });

  test("a ref block in a row's thread is a reference of the row, which the target lists", async () => {
    const from = await ticket('Login fails');
    const to = await ticket('Session store');
    // The thread's message, posted through the core (D186).
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

    const asCarol = createOrm(db, carol);
    expect((await asCarol.references.of({ table: 'Note', key: open })).map(r => r.to)).toEqual([{ table: 'Ticket', key: shared }]);
    expect(await asCarol.references.referencedBy({ table: 'Ticket', key: shared })).toEqual({ items: [{ table: 'Note', key: open, via: 'reference', role: '' }] });
    await expect(asCarol.references.referencedBy({ table: 'Note', key: hidden })).rejects.toBeInstanceOf(NotFoundError);
    await expect(asCarol.references.add({ from: { table: 'Note', key: open }, to: { table: 'Note', key: hidden } })).rejects.toBeInstanceOf(NotFoundError);
    const asBob = createOrm(db, bob);
    expect(await asBob.references.referencedBy({ table: 'Note', key: hidden })).toEqual({ items: [{ table: 'Note', key: open, via: 'reference', role: 'see also' }], hidden: 1 });
    expect((await orm.references.referencedBy({ table: 'Ticket', key: shared })).items.map(b => b.key).sort()).toEqual([open, quiet].sort());
    const other = await ticket('Elsewhere');
    const added = await orm.references.add({ from: { table: 'Ticket', key: other }, to: { table: 'Ticket', key: shared } });
    expect(added).toMatchObject({ role: '' });
    expect(added.addedAt).toBeInstanceOf(Date);
    // Adding it again answers the one there.
    expect((await orm.references.add({ from: { table: 'Ticket', key: other }, to: { table: 'Ticket', key: shared } })).id).toBe(added.id);
    expect(await orm.references.remove({ from: { table: 'Ticket', key: other }, to: { table: 'Ticket', key: shared } })).toBe(1);
  });

  test("a target's delete clears the references to it and tells each row that held one", async () => {
    const target = await note('Short-lived', alice);
    const holder = await note('Pointing note', alice);
    await orm.references.add({ from: { table: 'Note', key: holder }, to: { table: 'Note', key: target }, role: 'blocks' });
    await orm.note.deleteOne(target as never);
    expect(await orm.references.of({ table: 'Note', key: holder })).toEqual([]);
    const cleared = await orm.query<Record<string, unknown>>('SELECT holder_table, holder_key, target_table, role FROM reference_cleared_queue WHERE target_key = ?1', [uuidToDb(target)]);
    expect(cleared.rows).toEqual([{ holder_table: 'Note', holder_key: uuidToDb(holder), target_table: 'Note', role: 'blocks' }]);
    // A raw delete clears them as well: the trigger's, whatever deletes.
    const gone = await ticket('Gone by hand');
    await orm.references.add({ from: { table: 'Note', key: holder }, to: { table: 'Ticket', key: gone } });
    await orm.exec('DELETE FROM ticket WHERE id = ?1', [uuidToDb(gone)]);
    expect(await orm.references.of({ table: 'Note', key: holder })).toEqual([]);
  });

  test("an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it", async () => {
    const row = await ticket('Bounds');
    const wrong = await upload('Ticket', row, 'a.txt', 'text/plain', text('other bytes'), hex(text('declared bytes')).slice(0, 64));
    const refused = await orm.attachments.complete({ table: 'Ticket', id: wrong.id }, { bucket: bucket() }).catch(e => e);
    expect(refused).toBeInstanceOf(RowFeatureError);
    expect(refused.code).toBe('mismatch');
    await expect(orm.attachments.get({ table: 'Ticket', id: wrong.id })).rejects.toBeInstanceOf(NotFoundError);
    expect((await orm.attachments.release({ objectKey: wrong.objectKey }, { bucket: bucket() })).deleted).toBe(true);
    expect(r2.objects.has(wrong.objectKey)).toBe(false);
    const big = await orm.attachments
      .begin({ to: { table: 'Ticket', key: row }, name: 'big.bin', mediaType: 'application/octet-stream', size: 2 * 1024 * 1024, sha256: hex(text('')) }, { bucket: bucket() })
      .catch(e => e);
    expect(big.code).toBe('too_large');
    const owned = await note('Typed files', alice);
    const zip = await orm.attachments
      .begin({ to: { table: 'Note', key: owned }, name: 'a.zip', mediaType: 'application/zip', size: 10, sha256: hex(text('')) }, { bucket: bucket() })
      .catch(e => e);
    expect(zip.code).toBe('media_type');
    for (let i = 0; i < 4; i++) {
      await orm.attachments.begin({ to: { table: 'Ticket', key: row }, name: `p${i}.bin`, mediaType: 'application/octet-stream', size: 1024 * 1024, sha256: hex(text(`${i}`)) }, { bucket: bucket() });
    }
    const full = await orm.attachments
      .begin({ to: { table: 'Ticket', key: row }, name: 'p5.bin', mediaType: 'application/octet-stream', size: 1, sha256: hex(text('5')) }, { bucket: bucket() })
      .catch(e => e);
    expect(full.code).toBe('row_full');
    const owner = await ticket('Owner');
    const bytes = text('shared bytes');
    const file = await orm.attachments.complete({ table: 'Ticket', id: (await upload('Ticket', owner, 'shared.txt', 'text/plain', bytes)).id }, { bucket: bucket() });
    const moved = await orm.attachments.file({ table: 'Ticket', id: file.id, to: { table: 'Note', key: owned }, move: true });
    await expect(orm.attachments.get({ table: 'Ticket', id: file.id })).rejects.toBeInstanceOf(NotFoundError);
    expect((await orm.attachments.release({ objectKey: file.objectKey }, { bucket: bucket() })).deleted).toBe(false);
    await orm.attachments.detach({ table: 'Note', id: moved.id });
    const released = await orm.query<{ reason: string }>('SELECT reason FROM attachment_released_queue WHERE object_key = ?1 ORDER BY created_at, rowid', [file.objectKey]);
    expect(released.rows.map(r => r.reason)).toEqual(['expired', 'detached', 'detached']);
    expect((await orm.attachments.release({ objectKey: file.objectKey }, { bucket: bucket() })).deleted).toBe(true);
    expect(r2.objects.has(file.objectKey)).toBe(false);
  });

  test("a table that composes behaviors is written through the core, and a raw write of its rows is read", async () => {
    const id = await ticket('Written on SQLite');
    expect((await orm.ticket.getOne(id as never)).title).toBe('Written on SQLite');
    await orm.ticket.updateOne(id as never, { title: 'Renamed on SQLite' } as never);
    expect((await orm.ticket.getOne(id as never)).title).toBe('Renamed on SQLite');
    const raw = crypto.randomUUID();
    await orm.exec('INSERT INTO ticket (id, title) VALUES (?1, ?2)', [raw, 'Written by hand']);
    expect((await orm.ticket.getOne(uuidFromDb(raw) as never)).title).toBe('Written by hand');
    await orm.ticket.deleteOne(id as never);
    await expect(orm.ticket.getOne(id as never)).rejects.toBeInstanceOf(NotFoundError);
  });
});
