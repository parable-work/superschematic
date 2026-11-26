// The D1 repositories fixture (D182): fixture-references-db's generated
// repositories on SQLite (records-orm.ts, its orm-sqlite.ts) and its
// sqlite/create.sql (records-sql.ts), which TestTheWorkerdD1FixtureIsCurrent
// writes here, run over the runtime's built ./d1 entry inside workerd.
//
// D1 is SQLite behind a binding, which workerd does not serve on its own, so
// a stand-in here plays it over a Durable Object's SQLite storage, workerd's
// own SQLite, which runs no BEGIN or SAVEPOINT, as D1 runs none: prepare,
// bind and all, run and first, each statement run alone in the object and
// answered with its rows and its changes, as D1's meta counts them. The test
// handler applies create.sql there, triggers and all, and then, as the
// repositories run them:
//   - a private note is read by its owner and by no one else, in a read, a
//     list and a count, and a named one by whom it names: the audience's
//     triggers resolve it, and each read keeps to it through json_each;
//   - a reference to a private note is left out for another reader, and its
//     owner hears how many rows they cannot see point at it;
//   - a note's delete clears the references to it and enqueues a cleared
//     message, through the note's trigger;
//   - a file's upload is begun, its bytes put, completed, listed, read,
//     viewed, processed and detached, which enqueues its release;
//   - a write of a table that composes behaviors is refused.
import { DurableObject } from 'cloudflare:workers';
import { NotFoundError, uuidFromDb, uuidToDb, type SqlDatabase, type SqlPreparedStatement, type SqlResult, type SqlValue } from '../../../../dist/d1.js';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '../../../../dist/r2.js';
import { createOrm } from './records-orm.ts';
import { createSQL } from './records-sql.ts';

type Row = Record<string, unknown>;

/** The SQLite D1 runs on, as a Durable Object holds it: each statement alone, and its changes. */
export class Database extends DurableObject {
  query(sql: string, values: SqlValue[]): { rows: Row[]; changes: number } {
    const rows = this.ctx.storage.sql.exec(sql, ...(values as never[])).toArray() as Row[];
    const changes = Number((this.ctx.storage.sql.exec('SELECT changes() AS n').one() as { n: number }).n);
    return { rows, changes: /^\s*(INSERT|UPDATE|DELETE)\b/iu.test(sql) ? changes : 0 };
  }

  script(sql: string): number {
    return this.ctx.storage.sql.exec(sql).rowsWritten;
  }
}

/** A value as D1 binds it: a boolean as 1 or 0, bytes as an ArrayBuffer. */
function d1Value(value: SqlValue): SqlValue {
  if (typeof value === 'boolean') return value ? 1 : 0;
  if (value instanceof Uint8Array) return value.slice().buffer;
  return value;
}

/** D1's interface over the object. */
function d1Over(object: DurableObjectStub<Database>): SqlDatabase {
  const statement = (sql: string, values: SqlValue[] = []): SqlPreparedStatement => {
    const all = async <T>(): Promise<SqlResult<T>> => {
      const { rows, changes } = await object.query(sql, values);
      return { results: rows as T[], success: true, meta: { duration: 0, changes, last_row_id: 0, changed_db: changes > 0 } };
    };
    return {
      bind: (...bound: SqlValue[]) => statement(sql, bound.map(d1Value)),
      all,
      run: all,
      first: (async (column?: string) => {
        const row = (await all<Row>()).results[0];
        if (row === undefined) return null;
        return column === undefined ? row : row[column];
      }) as SqlPreparedStatement['first'],
      raw: (async () => (await all<Row>()).results.map(row => Object.values(row))) as SqlPreparedStatement['raw'],
    };
  };
  return {
    prepare: sql => statement(sql),
    batch: async statements => Promise.all(statements.map(s => s.all())),
    exec: async sql => ({ count: await object.script(sql), duration: 0 }),
  };
}

/** An r2_bucket binding over a map, as R2 holds objects. */
function memoryR2(): R2BucketBinding {
  const objects = new Map<string, Uint8Array>();
  const meta = (key: string): R2ObjectLike => ({ key, size: objects.get(key)!.length, uploaded: new Date() });
  return {
    async put(key: string, value: R2PutValue) {
      objects.set(key, typeof value === 'string' ? new TextEncoder().encode(value) : new Uint8Array(value instanceof ArrayBuffer ? value : (value as ArrayBufferView).buffer));
      return meta(key);
    },
    async get(key: string) {
      return objects.has(key) ? { ...meta(key), body: new Response(objects.get(key)!).body! } : null;
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

function check(condition: unknown, what: string): void {
  if (!condition) throw new Error(`d1-orm: ${what}`);
}

async function refuses(work: Promise<unknown>, what: string, test: (error: unknown) => boolean): Promise<void> {
  try {
    await work;
  } catch (error) {
    check(test(error), `${what}: ${String(error)}`);
    return;
  }
  throw new Error(`d1-orm: ${what}: it was not refused`);
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)), b => b.toString(16).padStart(2, '0')).join('');
}

// The users, in the base62 wire form a principal's subject holds.
const ALICE = 'TKbHsHDO6x9RCjbs5c2u4';
const BOB = '1JwchoUsfFfo9IGmolouwH';
const CAROL = '3cKSm7Pv2EM8Wl4z4XvMh1';

export default {
  async test(_controller: unknown, env: { DATABASE: DurableObjectNamespace<Database> }) {
    const db = d1Over(env.DATABASE.get(env.DATABASE.idFromName('records')));
    await db.exec(createSQL);
    const orm = createOrm(db, ALICE);
    const system = orm.asSystem();
    const note = async (title: string, owner: string, audience: string, audienceNamed?: string[]) =>
      (await system.note.createOne({ title, owner, audience, ...(audienceNamed ? { audienceNamed } : {}), createdAt: new Date() } as never)).id as string;

    // The audience, resolved by its triggers and kept to by each read.
    const hidden = await note("Bob's private note", BOB, 'private');
    const shared = await note('Shared with Carol', BOB, 'named', [CAROL]);
    const open = await note("Alice's open note", ALICE, 'everyone');
    const asBob = createOrm(db, BOB);
    const asCarol = createOrm(db, CAROL);
    check((await asBob.note.getOne(hidden as never)).title === "Bob's private note", 'the owner reads a private note');
    await refuses(asCarol.note.getOne(hidden as never), 'another reads a private note', error => error instanceof NotFoundError);
    check((await asCarol.note.getOne(shared as never)).title === 'Shared with Carol', 'a named reader reads a named note');
    const carolSees = await asCarol.note.findMany({}, {});
    check(carolSees.total === 2 && carolSees.rows.every(row => row.id !== hidden), `another lists only the notes she may see: ${JSON.stringify(carolSees.rows.map(row => row.title))}`);
    const effective = await system.query<{ effective_audience: string }>('SELECT effective_audience FROM note WHERE id = ?1', [uuidToDb(shared)]);
    check(JSON.parse(effective.rows[0]!.effective_audience).length === 2, 'a named note resolves to its owner and whom it names');

    // References, held to what each reader may read.
    await system.references.add({ from: { table: 'Note', key: open }, to: { table: 'Note', key: hidden }, role: 'see also' });
    const quiet = await note("Alice's private note", ALICE, 'private');
    await system.references.add({ from: { table: 'Note', key: quiet }, to: { table: 'Note', key: hidden } });
    const seenByBob = await asBob.references.referencedBy({ table: 'Note', key: hidden });
    check(JSON.stringify(seenByBob) === JSON.stringify({ items: [{ table: 'Note', key: open, via: 'reference', role: 'see also' }], hidden: 1 }), `the owner hears of the rows he cannot see: ${JSON.stringify(seenByBob)}`);
    await refuses(asCarol.references.referencedBy({ table: 'Note', key: hidden }), "another reads a private note's backlinks", error => error instanceof NotFoundError);
    check((await asCarol.references.of({ table: 'Note', key: open })).length === 0, 'a reference to a note another cannot read is left out');

    // A target's delete clears the references to it, and tells the holder.
    const target = await note('Short-lived', ALICE, 'everyone');
    await orm.references.add({ from: { table: 'Note', key: open }, to: { table: 'Note', key: target }, role: 'blocks' });
    await orm.note.deleteOne(target as never);
    check((await orm.references.of({ table: 'Note', key: open })).every(r => r.to.key !== target), "a deleted note's references are cleared");
    const cleared = await system.query<{ holder_key: string; role: string }>('SELECT holder_key, role FROM reference_cleared_queue WHERE target_key = ?1', [uuidToDb(target)]);
    check(cleared.rows.length === 1 && cleared.rows[0]!.holder_key === uuidToDb(open) && cleared.rows[0]!.role === 'blocks', `a cleared message names the holder: ${JSON.stringify(cleared.rows)}`);

    // A file: begun, uploaded, completed and read.
    const r2 = memoryR2();
    const bucket = openR2Bucket(r2, { name: 'records-files', binding: 'RECORDS_FILES', r2: { account: '0123456789abcdef0123456789abcdef', accessKeyId: '8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d', token: 'r2-token-value' } });
    const bytes = new TextEncoder().encode('The plan, in brief.');
    const { attachment, upload } = await orm.attachments.begin({ to: { table: 'Note', key: open }, name: 'plan.txt', mediaType: 'text/plain', size: bytes.length, sha256: await sha256Hex(bytes) }, { bucket });
    check(attachment.state === 'pending' && attachment.expiresAt instanceof Date && upload.method === 'PUT', 'an upload begins');
    await r2.put(attachment.objectKey, bytes);
    const file = await orm.attachments.complete({ table: 'Note', id: attachment.id }, { bucket });
    check(file.state === 'ready' && file.extraction === 'pending' && file.uploadedAt instanceof Date, 'a complete upload makes the file ready');
    check((await orm.attachments.list({ table: 'Note', key: open })).map(f => f.id).join() === file.id, "the note lists its file");
    check((await orm.attachments.get({ table: 'Note', id: file.id })).name === 'plan.txt', 'a file is read by its id');
    check((await orm.attachments.download({ table: 'Note', id: file.id }, { bucket })).attachment.preview === 'pending', "a file's first view asks its preview");
    check((await orm.attachments.pending('extraction')).some(p => p.objectKey === file.objectKey), "the file's text is pending");
    check((await orm.attachments.processed({ objectKey: file.objectKey, extraction: { state: 'done', text: 'The plan, in brief.' } })) === 1, "a processor's text lands on the file");
    await orm.attachments.detach({ table: 'Note', id: file.id });
    const released = await system.query<{ reason: string }>('SELECT reason FROM attachment_released_queue WHERE object_key = ?1 ORDER BY created_at, rowid', [file.objectKey]);
    check(released.rows.map(r => r.reason).join() === 'expired,detached', `a detached file's object is released: ${JSON.stringify(released.rows)}`);
    check((await orm.attachments.release({ objectKey: file.objectKey }, { bucket })).deleted, 'the object goes once no file names it');

    // A ticket composes Comments, whose writes run on Postgres alone so far.
    const ticket = crypto.randomUUID();
    await orm.exec('INSERT INTO ticket (id, title) VALUES (?1, ?2)', [ticket, 'Read on D1']);
    check((await orm.ticket.getOne(uuidFromDb(ticket) as never)).title === 'Read on D1', "a behaviors' table is read");
    await refuses(orm.ticket.createOne({ title: 'Not yet' } as never), "a write of a behaviors' table", error => String(error).includes('on Postgres alone so far'));
  },
};
