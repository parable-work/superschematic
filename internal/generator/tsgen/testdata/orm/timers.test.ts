// fixture-timers-db's tables end to end (D103): a ticket that implements
// Timers<Ticket, { ... }>, whose first answer is due 1.5 seconds after its
// create (respond, which escalates its status, a Workflow, unless it leaves
// open first), whose wait on its customer closes it after an hour (idle),
// and whose reminder falls due a day before its visit (remind).
// TestTimersTablesEndToEnd generates the package, type-checks it, applies
// the generated DDL and runs these cases on each dialect: on bun:sqlite,
// the SQLite create.sql D27 plans and the HTTP runtime's adapter over the
// layout orm.ts embeds (fixtureTimersDbRules), since the generated
// repositories run on Postgres alone; on Postgres, the generated
// create.sql and the generated repository itself (createOrm). So the
// generator's layout, its DDL, its sub-table and its deadline queue and the
// adapter are held to one another: a create writes the deadline's sub-table
// row and arms it as a message of the queue, due when it falls due; a
// status that leaves the state cancels it; a due deadline runs through
// runDeadline, with a worker's claim, which its unit completes, or without
// one; a claim a re-arm or a clear took is lost; a row deleted past the
// repository leaves a message the run completes as gone; deleting the row
// removes its deadlines; and a raw SQL write of visitAt is heard: the
// table's trigger writes a message of its change queue, in each dialect's
// JSON, which runChange runs with a worker's claim, re-arming remind (D103).
// No case waits for a deadline: runDeadline runs one at the time it was
// armed to fall due at when the clock is behind it.
import { Database } from 'bun:sqlite';
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { initSync } from '@superschematic/rules';
import {
  execute,
  readRow,
  readSubRows,
  runCreate,
  runChange,
  runDeadline,
  runDelete,
  sqliteRulesStore,
  type ChangeRun,
  type CoreCaller,
  type CoreSubRow,
  type DeadlineClaim,
  type DeadlineRun,
  type RulesHost,
} from '@superschematic/http-runtime/d1';
import { openSqlite, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { createOrm, fixtureTimersDbRules as layout } from './orm';
import { alice, database, dsn } from './harness';

const ticket = layout.types.Ticket!;
const deadlines = ticket.deadlines!;
const changes = ticket.changes!;
const subTable = ticket.uses.find(use => use.id === 'Timers')!.tables!.find(sub => sub.name === 'deadlines')!;

/** A message of a ticket's deadlines as a case reads it, every state: its name, the time it was armed at, its state and its due time in epoch milliseconds. */
interface Message {
  readonly id: string;
  readonly name: string;
  readonly at: number;
  readonly state: string;
  readonly dueAt: number;
}

const millis = (value: unknown): number => (value instanceof Date ? value.getTime() : typeof value === 'number' ? value : Date.parse(String(value)));

/** A message of a ticket's change queue as its trigger wrote it. */
interface ChangeMessage {
  readonly id: string;
  readonly rowKey: string;
  readonly write: string;
  readonly at: number | string;
  readonly before: unknown;
  readonly after: unknown;
}

/** What a case runs through: on Postgres the generated repository, on bun:sqlite the adapter over the generated layout. */
interface Tables {
  readonly dialect: string;
  /** Creates a ticket and resolves to its key and the time its create ran at, about. */
  create(own: Record<string, unknown>): Promise<string>;
  /** The ticket's status, or null when no live row holds it. */
  status(key: string): Promise<string | null>;
  operate(key: string, use: string, operation: string, params: unknown, caller?: CoreCaller): Promise<unknown>;
  delete(key: string): Promise<void>;
  runDeadline(key: string, message: Message, claim?: DeadlineClaim): Promise<DeadlineRun>;
  /** The ticket's deadlines' sub-table rows, as the core reads them. */
  subRows(key: string): Promise<CoreSubRow[]>;
  /** The ticket's deadline messages, every state. */
  messages(key: string): Promise<Message[]>;
  /** Claims the ticket's ready message of a deadline as a worker's claim does: claimed, under a new token. */
  claim(key: string, name: string): Promise<DeadlineClaim>;
  /** Runs a message of the change queue, as a worker's handler does. */
  runChange(message: ChangeMessage, claim?: DeadlineClaim): Promise<ChangeRun>;
  /** A write past the repository and the adapter, its placeholders $1, $2, ... */
  raw(text: string, values: readonly unknown[]): Promise<number>;
  /** A read past the repository and the adapter, its placeholders $1, $2, ... */
  query(text: string, values: readonly unknown[]): Promise<Record<string, unknown>[]>;
  close(): Promise<void>;
}

/** The ticket's change messages, every state, from a query of a dialect. */
async function changesOf(query: (text: string, values: readonly unknown[]) => Promise<Record<string, unknown>[]>, rowKey: string): Promise<ChangeMessage[]> {
  const rows = await query(
    `SELECT id, "${changes.rowKey}" AS row_key, "${changes.write}" AS write, "${changes.at}" AS at, "${changes.before}" AS before, "${changes.after}" AS after FROM "${changes.table}" WHERE "${changes.rowKey}" = $1`,
    [rowKey]
  );
  return rows.map(row => ({ id: String(row.id), rowKey: String(row.row_key), write: String(row.write), at: row.at as number | string, before: row.before, after: row.after }));
}

/** The ticket's messages, from a query of a dialect. */
async function messagesOf(query: (text: string, values: readonly unknown[]) => Promise<Record<string, unknown>[]>, rowKey: string): Promise<Message[]> {
  const rows = await query(`SELECT id, "${deadlines.name}" AS name, "${deadlines.at}" AS at, state, due_at FROM "${deadlines.table}" WHERE "${deadlines.rowKey}" = $1 ORDER BY "${deadlines.name}"`, [rowKey]);
  return rows.map(row => ({ id: String(row.id), name: String(row.name), at: Number(row.at), state: String(row.state), dueAt: millis(row.due_at) }));
}

/** The tables on bun:sqlite: the generated SQLite DDL in a file of the test's own, and the adapter over the generated layout, as alice. */
async function sqliteTables(): Promise<Tables> {
  const dir = mkdtempSync(join(tmpdir(), 'timers-db-'));
  const file = join(dir, 'timers.db');
  const ddl = new Database(file, { create: true });
  ddl.exec(readFileSync(new URL('./create.sqlite.sql', import.meta.url), 'utf8'));
  ddl.close();
  const db: SqliteDatabase = openSqlite(file);
  const sql = (text: string) => text.replace(/\$(\d+)/g, '?$1');
  const query = async (text: string, values: readonly unknown[]) => (await db.prepare(sql(text)).bind(...(values as never[])).all()).results;
  const host: RulesHost = { core: initSync(), layout, store: sqliteRulesStore(db) };
  const caller: CoreCaller = { subject: alice, permissions: [] };
  /** The hyphenated form of a key the core names, as the queue's row_key holds it. */
  const rowKey = (key: string) => uuidText(key);
  return {
    dialect: 'bun:sqlite',
    create: own => runCreate(host, 'Ticket', own, caller),
    async status(key) {
      const row = await readRow(host, 'Ticket', key);
      return row === null ? null : String(row.owned?.status?.status ?? null);
    },
    async operate(key, use, operation, params, as) {
      return (await execute(host, 'Ticket', { caller: as ?? caller, now: Date.now(), action: { kind: 'operation', key, useId: use, operation, params } })).result;
    },
    delete: key => runDelete(host, 'Ticket', key, caller),
    runDeadline: (key, message, claim) => runDeadline(host, 'Ticket', { key, useId: 'Timers', name: message.name, at: message.at, payload: null }, caller, { claim }),
    runChange: (message, claim) => runChange(host, 'Ticket', { key: message.rowKey, write: message.write, at: message.at, before: message.before, after: message.after }, caller, { claim }),
    subRows: key => readSubRows(host, 'Ticket', key, 'Timers', 'deadlines'),
    messages: key => messagesOf(query, rowKey(key)),
    async claim(key, name) {
      const token = crypto.randomUUID();
      const [row] = await query(`SELECT id FROM "${deadlines.table}" WHERE "${deadlines.rowKey}" = $1 AND "${deadlines.name}" = $2 AND state = 'ready'`, [rowKey(key), name]);
      await query(`UPDATE "${deadlines.table}" SET state = 'claimed', claim_token = $1, attempts = attempts + 1 WHERE id = $2`, [token, row!.id]);
      return { id: String(row!.id), token };
    },
    async raw(text, values) {
      await db.prepare(sql(text)).bind(...(values as never[])).run();
      // The rows the statement itself changed, which SQLite's changes()
      // counts without its triggers' (the change queue's insert, D103).
      const [row] = (await db.prepare('SELECT changes() AS n').all()).results;
      return Number(row!.n);
    },
    query,
    async close() {
      db.close();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

/** The tables on Postgres: the generated create.sql in a schema of the test's own, and the generated repository, acting as alice. */
async function postgresTables(): Promise<Tables> {
  const db = await database('ts_orm_timers');
  const orm = createOrm(db.pool, alice);
  const query = async (text: string, values: readonly unknown[]) => (await db.pool.query(text, [...values])).rows;
  return {
    dialect: 'Postgres',
    async create(own) {
      return (await orm.ticket.createOne(own as never)).id;
    },
    async status(key) {
      return (await orm.ticket.findOne({ id: { eq: key } }))?.status ?? null;
    },
    operate: (key, use, operation, params, caller) => (orm.ticket as any)[use][operation](key, params, caller ? { caller } : undefined),
    delete: key => orm.ticket.deleteOne(key),
    runDeadline: (key, message, claim) => orm.ticket.runDeadline({ rowKey: key, useId: 'Timers', name: message.name, at: message.at, payload: null }, { claim }),
    runChange: (message, claim) => orm.ticket.runChange({ rowKey: message.rowKey as never, write: message.write, at: message.at, before: message.before, after: message.after }, { claim }),
    async subRows(key) {
      // No repository reads a sub-table (D103): SQL does.
      const rows = await query(`SELECT * FROM "${subTable.table}" WHERE "${subTable.row}" = $1 ORDER BY "${subTable.key.column}"`, [uuidText(key)]);
      return rows.map(row => {
        const values: Record<string, unknown> = {};
        for (const field of subTable.fields) {
          const value = row[field.column];
          if (value === null || value === undefined) continue;
          values[field.name] = field.convert === 'epochMillis' ? millis(value) : value;
        }
        return { key: String(row[subTable.key.column]), values };
      });
    },
    messages: key => messagesOf(query, uuidText(key)),
    async claim(key, name) {
      const token = crypto.randomUUID();
      const [row] = await query(`SELECT id FROM "${deadlines.table}" WHERE "${deadlines.rowKey}" = $1 AND "${deadlines.name}" = $2 AND state = 'ready'`, [uuidText(key), name]);
      await query(`UPDATE "${deadlines.table}" SET state = 'claimed', claim_token = $1, attempts = attempts + 1 WHERE id = $2`, [token, row!.id]);
      return { id: String(row!.id), token };
    },
    async raw(text, values) {
      return (await db.pool.query(text, [...values])).rowCount ?? 0;
    },
    query,
    close: () => db.drop(),
  };
}

const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/** A UUID's hyphenated text from its base62 wire form, as its column holds it. */
function uuidText(key: string): string {
  let n = 0n;
  for (const c of key) n = n * 62n + BigInt(BASE62.indexOf(c));
  const hex = n.toString(16).padStart(32, '0');
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

const dialects: { readonly name: string; readonly skip: boolean; readonly open: () => Promise<Tables> }[] = [
  { name: 'bun:sqlite', skip: false, open: sqliteTables },
  { name: 'Postgres', skip: dsn === '', open: postgresTables },
];

/** The live messages of a ticket: ready or claimed. */
const live = (messages: Message[]) => messages.filter(message => message.state === 'ready' || message.state === 'claimed');

const DAY = 86_400_000;

for (const dialect of dialects) {
  describe.skipIf(dialect.skip)(dialect.name, () => {
    let t: Tables;

    beforeAll(async () => {
      t = await dialect.open();
    });

    afterAll(async () => {
      await t?.close();
    });

    test(`a create arms the deadline: its sub-table row and its queue message with its due time, on ${dialect.name}`, async () => {
      const before = Date.now();
      const key = await t.create({ title: 'Leak' });
      const after = Date.now();
      const rows = await t.subRows(key);
      expect(rows.map(row => row.key)).toEqual(['respond']);
      const respond = rows[0]!.values!;
      expect(respond).toMatchObject({ fired: false });
      expect(respond.baseAt as number).toBeGreaterThanOrEqual(before);
      expect(respond.baseAt as number).toBeLessThanOrEqual(after);
      expect(respond.dueAt).toBe((respond.baseAt as number) + 1500);
      expect(await t.messages(key)).toEqual([{ id: expect.any(String), name: 'respond', at: respond.dueAt as number, state: 'ready', dueAt: respond.dueAt as number }]);
    });

    test(`a status that leaves the state cancels the deadline by until, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Drip' });
      expect(await t.operate(key, 'status', 'transition', { to: 'waiting' })).toEqual({ from: 'open', to: 'waiting' });
      const rows = Object.fromEntries((await t.subRows(key)).map(row => [row.key, row.values ?? {}]));
      expect(rows.respond!.dueAt).toBeUndefined();
      const idle = rows.idle!;
      expect(idle.dueAt).toBe((idle.baseAt as number) + 3_600_000);
      expect(live(await t.messages(key)).map(message => [message.name, message.at])).toEqual([['idle', idle.dueAt as number]]);
    });

    test(`a due deadline runs through runDeadline without a claim, moves the status and records the outcome, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Flood' });
      const [message] = await t.messages(key);
      const run = await t.runDeadline(key, message!);
      expect(run.ran).toBe(true);
      expect(await t.status(key)).toBe('escalated');
      const [respond] = await t.subRows(key);
      expect(respond!.values).toMatchObject({ fired: true, outcome: 'moved' });
      expect(live(await t.messages(key))).toEqual([]);
    });

    test(`a deadline a worker claimed runs, and its unit completes the message, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Mould' });
      const [message] = await t.messages(key);
      const claim = await t.claim(key, 'respond');
      expect((await t.runDeadline(key, message!, claim)).ran).toBe(true);
      expect(await t.status(key)).toBe('escalated');
      expect(await t.messages(key)).toEqual([]);
    });

    test(`a claim a status change cancelled is lost: the deadline never fires on its old arming, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Crack' });
      const [message] = await t.messages(key);
      const claim = await t.claim(key, 'respond');
      await t.operate(key, 'status', 'transition', { to: 'waiting' });
      expect(await t.runDeadline(key, message!, claim)).toEqual({ ran: false, why: 'claim lost' });
      expect(await t.status(key)).toBe('waiting');
      const rows = Object.fromEntries((await t.subRows(key)).map(row => [row.key, row.values ?? {}]));
      expect(rows.respond).toMatchObject({ fired: false });
      expect(live(await t.messages(key)).map(m => m.name)).toEqual(['idle']);
    });

    test(`a row deleted past the repository leaves a message the run completes as gone, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Stain' });
      const [message] = await t.messages(key);
      const claim = await t.claim(key, 'respond');
      await t.raw(`DELETE FROM "${subTable.table}" WHERE "${subTable.row}" = $1`, [uuidText(key)]);
      expect(await t.raw(`DELETE FROM ticket WHERE id = $1`, [uuidText(key)])).toBe(1);
      const run = await t.runDeadline(key, message!, claim);
      expect(run).toMatchObject({ ran: false, why: 'gone', refusal: { code: 'not_found' } });
      expect(await t.messages(key)).toEqual([]);
    });

    test(`a raw SQL write of a field start is heard: its trigger's message, run through runChange with a claim, re-arms the deadline, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Boiler' });
      const visit = Date.now() + 2 * DAY;
      const at = t.dialect === 'Postgres' ? new Date(visit) : new Date(visit).toISOString();
      expect(await t.raw(`UPDATE ticket SET visit_at = $2 WHERE id = $1`, [uuidText(key), at])).toBe(1);
      const written = await changesOf(t.query, uuidText(key));
      expect(written.map(message => message.write).sort()).toEqual(['insert', 'update']);
      const insert = written.find(message => message.write === 'insert')!;
      const update = written.find(message => message.write === 'update')!;
      // The raw write's, claimed as a worker claims it: remind is armed a day
      // before the visit, and the unit completes the message.
      const token = crypto.randomUUID();
      expect(await t.raw(`UPDATE "${changes.table}" SET state = 'claimed', claim_token = $1, attempts = attempts + 1 WHERE id = $2`, [token, update.id])).toBe(1);
      expect((await t.runChange(update, { id: update.id, token })).ran).toBe(true);
      const rows = Object.fromEntries((await t.subRows(key)).map(row => [row.key, row.values ?? {}]));
      expect(rows.remind).toMatchObject({ dueAt: visit - DAY, fired: false });
      const armed = [
        ['remind', visit - DAY],
        ['respond', rows.respond!.dueAt as number],
      ];
      expect(live(await t.messages(key)).map(message => [message.name, message.at])).toEqual(armed);
      // The create's, the core's own write heard after the row moved on, is
      // run on the row as it is now, which the reconcile has already met.
      expect((await t.runChange(insert)).ran).toBe(true);
      expect(Object.fromEntries((await t.subRows(key)).map(row => [row.key, row.values ?? {}]))).toEqual(rows);
      expect(live(await t.messages(key)).map(message => [message.name, message.at])).toEqual(armed);
      expect((await changesOf(t.query, uuidText(key))).map(message => message.id)).not.toContain(update.id);
    });

    test(`deleting the row removes its deadlines, on ${dialect.name}`, async () => {
      const key = await t.create({ title: 'Rot' });
      await t.operate(key, 'status', 'transition', { to: 'waiting' });
      await t.delete(key);
      expect(await t.subRows(key)).toEqual([]);
      expect(await t.messages(key)).toEqual([]);
    });
  });
}
