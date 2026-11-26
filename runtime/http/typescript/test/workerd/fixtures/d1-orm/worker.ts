// The D1 repositories fixture (D182, D186): the generated repositories on
// SQLite of three databases, each its orm-sqlite.ts (<prefix>-orm.ts) and its
// sqlite/create.sql (<prefix>-sql.ts), which TestTheWorkerdD1FixtureIsCurrent
// writes here, run over the runtime's built ./d1 entry inside workerd:
// fixture-references-db's (records), fixture-recurrence-db's (filings) and
// fixture-search-db's (search). The behaviors' core is the wasm32 module the
// rules package ships, which the Worker passes createOrm (rules).
//
// D1 is SQLite behind a binding, which workerd does not serve on its own, so
// a stand-in here plays it over a Durable Object's SQLite storage, workerd's
// own SQLite, which runs no BEGIN or SAVEPOINT, as D1 runs none: prepare,
// bind and all, run and first, each statement run alone in the object and
// answered with its rows and its changes, as D1's meta counts them; and
// batch, whose statements run in one of the object's transactions, all or
// none, as D1 runs a batch. The test handler applies each create.sql there,
// triggers and all, and then, as the repositories run them:
//   - a private note is read by its owner and by no one else, in a read, a
//     list and a count, and a named one by whom it names: the audience's
//     triggers resolve it, and each read keeps to it through json_each;
//   - a reference to a private note is left out for another reader, and its
//     owner hears how many rows they cannot see point at it;
//   - a note's delete clears the references to it and enqueues a cleared
//     message, through the note's trigger;
//   - a file's upload is begun, its bytes put, completed, listed, read,
//     viewed, processed and detached, which enqueues its release;
//   - a ticket, which composes Comments, is created and its thread posted
//     to through the core, each change set one batch;
//   - a filing's recurrence is set, which plans its occurrences and arms its
//     deadlines in one batch; the remind deadline a worker claimed fires,
//     its batch spending the claim with the occurrence's reminder and the
//     events it emits, and a claim another holds spends and writes nothing;
//     the calendar merges the occurrences and a contract's end date;
//   - rows of five tables are indexed by the queue's worker, whose index
//     rows' words workerd's SQLite keeps in FTS5, and a search ranks them
//     with each caller's audience checked inside the query: a private row
//     never shows to another, a delete and an audience change hold at once,
//     a row's conversation is found with it, and scope keeps to its filter.
import { DurableObject } from 'cloudflare:workers';
import {
  NotFoundError,
  indexSearch,
  readCalendar,
  searchTables,
  uuidFromDb,
  uuidToDb,
  type CoreCaller,
  type SqlDatabase,
  type SqlPreparedStatement,
  type SqlResult,
  type SqlValue,
} from '../../../../dist/d1.js';
import { openR2Bucket, type R2BucketBinding, type R2ObjectLike, type R2PutValue } from '../../../../dist/r2.js';
import { initSync } from '../../../../../../rules/typescript/dist/index.js';
import rulesWasm from './superschematic_rules_wasm.wasm';
import { createOrm } from './records-orm.ts';
import { createSQL } from './records-sql.ts';
import { createOrm as createFilings } from './filings-orm.ts';
import { createSQL as filingsSQL } from './filings-sql.ts';
import { createOrm as createSearch } from './search-orm.ts';
import { createSQL as searchSQL } from './search-sql.ts';

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

  /** A batch, as D1 runs one: every statement in one transaction, rolled back whole when one throws. */
  batch(statements: { sql: string; values: SqlValue[] }[]): { rows: Row[]; changes: number }[] {
    return this.ctx.storage.transactionSync(() => statements.map(({ sql, values }) => this.query(sql, values)));
  }
}

/** A value as D1 binds it: a boolean as 1 or 0, bytes as an ArrayBuffer. */
function d1Value(value: SqlValue): SqlValue {
  if (typeof value === 'boolean') return value ? 1 : 0;
  if (value instanceof Uint8Array) return value.slice().buffer;
  return value;
}

/** A statement of the stand-in, which its batch reads the text and values of. */
type StandInStatement = SqlPreparedStatement & { readonly sql: string; readonly values: SqlValue[] };

/** A result as D1 answers it. */
function resultOf<T>({ rows, changes }: { rows: Row[]; changes: number }): SqlResult<T> {
  return { results: rows as T[], success: true, meta: { duration: 0, changes, last_row_id: 0, changed_db: changes > 0 } };
}

/** D1's interface over the object. */
function d1Over(object: DurableObjectStub<Database>): SqlDatabase {
  const statement = (sql: string, values: SqlValue[] = []): StandInStatement => {
    const all = async <T>(): Promise<SqlResult<T>> => resultOf<T>(await object.query(sql, values));
    return {
      sql,
      values,
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
    batch: async <T>(statements: readonly SqlPreparedStatement[]) =>
      (await object.batch(statements.map(s => ({ sql: (s as StandInStatement).sql, values: (s as StandInStatement).values })))).map(result => resultOf<T>(result)),
    exec: async sql => ({ count: await object.script(sql), duration: 0 }),
  };
}

const DAY = 86_400_000;

/** A wall date-time at 09:00 of the first weekday at least some days from now. */
function weekday(daysFromNow: number): string {
  let at = new Date(Date.now() + daysFromNow * DAY);
  while (at.getUTCDay() === 0 || at.getUTCDay() === 6) at = new Date(at.getTime() + DAY);
  return `${at.toISOString().slice(0, 10)}T09:00`;
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
    // The behaviors' core, as a deploy's Worker loads its module.
    const rules = initSync(rulesWasm);
    const database = (name: string) => d1Over(env.DATABASE.get(env.DATABASE.idFromName(name)));
    await records(database('records'), rules);
    await filings(database('filings'), rules);
    await search(database('search'), rules);
  },
};

/** fixture-references-db's references, files and a conversation on D1 (D182, D186). */
async function records(db: SqlDatabase, rules: ReturnType<typeof initSync>): Promise<void> {
  {
    await db.exec(createSQL);
    const orm = createOrm(db, { user: ALICE, rules });
    const system = orm.asSystem();
    const note = async (title: string, owner: string, audience: string, audienceNamed?: string[]) =>
      (await system.note.createOne({ title, owner, audience, ...(audienceNamed ? { audienceNamed } : {}), createdAt: new Date() } as never)).id as string;

    // The audience, resolved by its triggers and kept to by each read.
    const hidden = await note("Bob's private note", BOB, 'private');
    const shared = await note('Shared with Carol', BOB, 'named', [CAROL]);
    const open = await note("Alice's open note", ALICE, 'everyone');
    const asBob = createOrm(db, { user: BOB, rules });
    const asCarol = createOrm(db, { user: CAROL, rules });
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

    // A ticket composes Comments: its create and its thread's post run
    // through the core, each change set one batch (D186).
    const ticket = (await orm.ticket.createOne({ title: 'Written on D1' } as never)).id as string;
    check((await orm.ticket.getOne(ticket as never)).title === 'Written on D1', "a behaviors' table is written through the core");
    await orm.withBehaviorCaller({ subject: ALICE, permissions: [] }).ticket.comments.post(ticket as never, { body: 'Posted on D1.' } as never);
    const posted = await system.query<{ body: string; state: string }>('SELECT body, state FROM ticket_comments_messages WHERE ticket_id = ?1', [uuidToDb(ticket)]);
    check(posted.rows.length === 1 && posted.rows[0]!.body === 'Posted on D1.' && posted.rows[0]!.state === 'posted', `a post lands as one message: ${JSON.stringify(posted.rows)}`);
    const raw = crypto.randomUUID();
    await orm.exec('INSERT INTO ticket (id, title) VALUES (?1, ?2)', [raw, 'Written by hand']);
    check((await orm.ticket.getOne(uuidFromDb(raw) as never)).title === 'Written by hand', "a raw write of a behaviors' table is read");
  }
}

/** fixture-recurrence-db's Recurrence and calendar on D1 (D152, D186). */
async function filings(db: SqlDatabase, rules: ReturnType<typeof initSync>): Promise<void> {
  await db.exec(filingsSQL);
  const orm = createFilings(db, { rules });
  const system = orm.asSystem();
  const manager: CoreCaller = { subject: ALICE, permissions: ['filings.manage', 'filings.work'] };
  const start = weekday(40);
  // The first due day is a holiday of the east calendar: it moves.
  await orm.holiday.createOne({ calendar: 'east', date: start.slice(0, 10), name: 'Closed' } as never);
  const filing = (await orm.filing.createOne({ title: 'Quarterly return', holidayCalendar: 'east' } as never)).id as string;
  const set = (await orm.filing.recurrence.setRecurrence(filing as never, { anchor: 'calendar', rule: 'FREQ=MONTHLY', start } as never, { caller: manager })) as { recurrence: { revision: number } };
  check(set.recurrence.revision === 1, `setRecurrence sets the first revision: ${JSON.stringify(set)}`);
  const occurrences = async () =>
    (await system.query<Row>('SELECT key, state, shifted, reminded_at FROM filing_recurrence_occurrences WHERE filing_id = ?1 ORDER BY due_at, key', [uuidToDb(filing)])).rows;
  const planned = await occurrences();
  check(planned.length >= 12 && planned[0]!.key === start && planned[0]!.shifted === 'holiday', `the plan's occurrences land in its batch, moved off the holiday: ${JSON.stringify(planned.slice(0, 2))}`);
  check(planned.every(row => row.state === 'upcoming'), 'every occurrence is upcoming');
  const armed = (await system.query<Row>("SELECT id, row_key, use_id, name, at, payload FROM filing_deadline_queue WHERE row_key = ?1 AND state = 'ready'", [uuidToDb(filing)])).rows;
  const remind = armed.find(row => row.name === 'remind');
  check(remind !== undefined && armed.some(row => row.name === 'open'), `the open and remind deadlines are armed in the same batch: ${JSON.stringify(armed.map(row => row.name))}`);

  // A worker's claim of the remind deadline, as its queue claims one.
  const token = crypto.randomUUID();
  await system.exec("UPDATE filing_deadline_queue SET state = 'claimed', claim_token = ?2, attempts = attempts + 1 WHERE id = ?1", [remind!.id, token]);
  const due = { rowKey: remind!.row_key, useId: String(remind!.use_id), name: 'remind', at: Number(remind!.at), payload: typeof remind!.payload === 'string' ? JSON.parse(remind!.payload) : null };
  // A claim another token holds spends nothing, and its batch writes nothing.
  const lost = await orm.filing.runDeadline(due, { claim: { id: String(remind!.id), token: crypto.randomUUID() }, caller: manager });
  check(!lost.ran && lost.why === 'claim lost', `a lost claim runs nothing: ${JSON.stringify(lost)}`);
  check((await occurrences())[0]!.reminded_at === null, 'a lost claim writes no reminder');
  const fired = await orm.filing.runDeadline(due, { claim: { id: String(remind!.id), token }, caller: manager });
  check(fired.ran, `the claimed deadline runs: ${JSON.stringify(fired)}`);
  const reminded = (await occurrences())[0]!;
  check(reminded.reminded_at !== null && reminded.state === 'open', `its batch records the reminder and opens the occurrence: ${JSON.stringify(reminded)}`);
  const spent = await system.query<Row>('SELECT id FROM filing_deadline_queue WHERE id = ?1', [remind!.id]);
  check(spent.rows.length === 0, 'the same batch spends the claim');
  const events = await system.query<{ kind: string }>('SELECT kind FROM filing_event_queue WHERE row_key = ?1 ORDER BY created_at, id', [uuidToDb(filing)]);
  check(events.rows.map(row => row.kind).sort().join() === 'opened,reminded', `the fire emits the opening and the reminder: ${JSON.stringify(events.rows)}`);

  // The calendar, as the manager reads it: the filing's occurrences and a
  // contract's end date, in start order.
  const ends = weekday(20).slice(0, 10);
  const contract = (await orm.contract.createOne({ title: 'Lease', endsOn: ends } as never)).id as string;
  const read = await readCalendar(orm, { subject: ALICE, permissions: ['filings.manage', 'filings.work'] }, { from: new Date(), to: new Date(Date.now() + 45 * DAY) });
  check(read.entries.some(entry => entry.source === 'contract' && entry.key === contract && entry.start === ends && entry.allDay === true), `the calendar shows the contract's end: ${JSON.stringify(read.entries)}`);
  const deadline = read.entries.find(entry => entry.source === 'filing' && entry.key === filing);
  check(deadline?.occurrence?.key === start && deadline.kind === 'deadline' && deadline.title === 'Quarterly return', `the calendar shows the filing's occurrence: ${JSON.stringify(deadline)}`);
  const starts = read.entries.map(entry => entry.start);
  check(JSON.stringify([...starts].sort()) === JSON.stringify(starts), 'the calendar is in start order');
}

/** fixture-search-db's search across tables on D1 (D155, D186). */
async function search(db: SqlDatabase, rules: ReturnType<typeof initSync>): Promise<void> {
  await db.exec(searchSQL);
  const orm = createSearch(db, { user: ALICE, rules });
  const system = orm.asSystem();
  const user = (subject: string) => ({ subject, permissions: [] });
  // What the queue's worker runs: every ready message, indexed.
  const drain = async (): Promise<number> => {
    let n = 0;
    for (;;) {
      const { rows } = await system.query<{ source_table: string; source_key: string }>(
        "DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key"
      );
      if (rows.length === 0) return n;
      for (const row of rows) await indexSearch(orm, { sourceTable: row.source_table, sourceKey: row.source_key });
      n += rows.length;
    }
  };
  const found = async (subject: string, q: string, extra: object = {}) => (await searchTables(orm, user(subject), { q, limit: 100, ...extra })).items.map(hit => `${hit.table}:${hit.key}`);
  const asAlice = orm.as(user(ALICE));
  const privateFolder = (await asAlice.folder.createOne({ name: 'Walnut private', owner: ALICE, audience: 'private', createdAt: new Date() } as never)).id as string;
  const sharedFolder = (await asAlice.folder.createOne({ name: 'Walnut shared', owner: ALICE, createdAt: new Date() } as never)).id as string;
  const privateDoc = (await asAlice.document.createOne({ folderId: privateFolder, title: 'Walnut plan', text: 'The plan in brief.', createdAt: new Date() } as never)).id as string;
  const sharedDoc = (await asAlice.document.createOne({ folderId: sharedFolder, title: 'Memo', text: 'A walnut desk for the hall.', createdAt: new Date() } as never)).id as string;
  const contract = (await system.contract.createOne({ name: 'Walnut supply', summary: 'Timber for the year.' } as never)).id as string;
  const unit = (await system.unit.createOne({ name: 'Annex', notes: 'Walnut floors.', contractId: contract } as never)).id as string;
  const noteP1 = (await system.note.createOne({ project: 'p1', title: 'Walnut idea', text: 'Oil it twice.' } as never)).id as string;
  const noteP2 = (await system.note.createOne({ project: 'p2', title: 'Pine idea', text: 'A walnut stain on pine.' } as never)).id as string;
  check((await drain()) === 8, 'every write enqueued its row');
  const words = await system.query<{ words: number; rows: number }>('SELECT (SELECT count(*) FROM search_index_fts) AS words, (SELECT count(*) FROM search_index) AS rows');
  check(words.rows[0]!.words === 8 && words.rows[0]!.rows === 8, `workerd's SQLite keeps each index row's words in FTS5: ${JSON.stringify(words.rows)}`);

  const alices = await found(ALICE, 'walnut');
  const bobs = await found(BOB, 'walnut');
  check(alices.includes(`Folder:${privateFolder}`) && alices.includes(`Document:${privateDoc}`), `the owner finds her private rows: ${JSON.stringify(alices)}`);
  check(!bobs.includes(`Folder:${privateFolder}`) && !bobs.includes(`Document:${privateDoc}`) && bobs.includes(`Document:${sharedDoc}`), `another never finds them: ${JSON.stringify(bobs)}`);
  check(alices.length - bobs.length === 2 && new Set(bobs.map(key => key.split(':')[0])).size >= 4, 'the ranking spans the tables each may read');
  const page = await searchTables(orm, user(BOB), { q: 'walnut', limit: 2 });
  check(page.total === bobs.length && page.more && page.items.map(hit => `${hit.table}:${hit.key}`).join() === bobs.slice(0, 2).join(), 'a page keeps the total and the order');
  check(JSON.stringify(await found(BOB, 'walnut stain')) === JSON.stringify([`Note:${noteP2}`]), 'two words: a row holds both');
  check(JSON.stringify(await found(BOB, 'idea', { scope: 'p1' })) === JSON.stringify([`Note:${noteP1}`]), 'scope keeps to its filter');

  // A conversation is found with its row, once the queue drains.
  await orm.withBehaviorCaller({ subject: ALICE, permissions: [] }).unit.comments.post(unit as never, { body: 'The sycamore by the gate needs a trim.' } as never);
  check((await drain()) === 1, "a post enqueues its row");
  const sycamore = (await searchTables(orm, user(BOB), { q: 'sycamore' })).items;
  check(sycamore.length === 1 && sycamore[0]!.key === unit && sycamore[0]!.matched === 'comments', `a row's conversation is found with it: ${JSON.stringify(sycamore)}`);

  // An audience change and a delete hold at once, with no drain.
  await asAlice.folder.updateOne(sharedFolder as never, { audience: 'private' } as never);
  const hidden = await found(BOB, 'walnut');
  check(!hidden.includes(`Folder:${sharedFolder}`) && !hidden.includes(`Document:${sharedDoc}`), `a folder made private hides it and its documents at once: ${JSON.stringify(hidden)}`);
  await system.note.deleteOne(noteP2 as never);
  check(!(await found(BOB, 'walnut')).includes(`Note:${noteP2}`), 'a soft delete takes the index row at once');
}
