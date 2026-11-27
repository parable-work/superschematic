/*
fixture-recurrence-api's filings end to end (D152), run by
TestGeneratedRecurrenceEndToEnd with API_DIR set to the materialized
package, CREATE_SQL to fixture-recurrence-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-recurrence-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
worker FilingDeadlines runs in process, its queues as the API's package
generates them (filingDeadlinesQueues: Filing's deadlines, changes and
events) under the runtime's runWorker, with a Workers of the test's own that
records each event Recurrence emits as it opens, reminds and misses an
occurrence (D153), which a notifier raises.

A Filing implements Recurrence in Europe/London: each occurrence opens two
weeks before it is due, reminds three days before, and is missed a day
after; its due date moves to the next business day of the holiday
calendar its holidayCalendar names, whose days are rows of Holiday. A
deadline's fire is driven as the Schedules end-to-end tests drive one: its
message made due with SQL, then the worker's queues until the row shows
it, the command running at the later of the clock and the deadline's time.

TestGeneratedRecurrenceEndToEndOnSQLite runs the same cases with
ORM_DIALECT sqlite (D186), CREATE_SQL fixture-recurrence-db's
sqlite/create.sql and SQLITE_FILE a file of its own: the router serves the
API over the repositories of the ./orm/sqlite entry on openSqlite's
connection to the file, whose behaviors run through the core in its
transactions, and the worker's queues are the generated ones' handlers
over the SQLite queue client (sqliteQueue).
*/
import { afterAll, beforeAll, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { readCalendar, uuidToDb, type CoreCaller } from '@superschematic/http-runtime/postgres';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-recurrence-db-types/orm';
import { filingChangeQueue, filingDeadlineQueue, filingEventQueue } from '@schemas/fixture-recurrence-db-types/queues';
import { sqliteQueue, type SqliteDatabase } from '@superschematic/http-runtime/sqlite';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const createSQL = process.env.CREATE_SQL;
const dialect = process.env.ORM_DIALECT ?? 'postgres';
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
const sqliteFile = process.env.SQLITE_FILE;
if (!apiDir || !createSQL || (dialect === 'sqlite' ? !sqliteFile : !dsn)) {
  throw new Error('API_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, or ORM_DIALECT=sqlite and SQLITE_FILE, are required');
}
const { buildRouter, filingDeadlinesQueues } = await import(`${apiDir}/index.ts`);
const onSQLite = dialect === 'sqlite';

const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;
const callers = {
  manager: { subject: subject(), permissions: ['filings.manage', 'filings.work'] },
  worker: { subject: subject(), permissions: ['filings.work'] },
} as const;
type Caller = keyof typeof callers;

const authenticate = Object.assign(
  async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization');
    if (!header?.startsWith('Bearer ')) return null;
    const [who, permissions = ''] = header.slice('Bearer '.length).split(':');
    return { subject: who!, permissions: permissions === '' ? [] : permissions.split(',') };
  },
  { readsAuthorization: true }
);

const logger = { debug() {}, info() {}, warn() {}, error() {}, child() { return logger; } };

const schema = `recurrence_ts_${process.hrtime.bigint()}`;
let admin: pg.Client | undefined;
let pool: pg.Pool | undefined;
let sqlite: SqliteDatabase | undefined;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

beforeAll(async () => {
  if (onSQLite) {
    // The file made from the database's SQLite create.sql, then opened as
    // a server under stack dev opens it.
    const { Database } = await import('bun:sqlite');
    const file = new Database(sqliteFile!, { create: true });
    file.exec(readFileSync(createSQL, 'utf8'));
    file.close();
    const { openSqlite } = await import('@superschematic/http-runtime/sqlite');
    sqlite = openSqlite(sqliteFile!);
    const repositories = await import('@schemas/fixture-recurrence-db-types/orm/sqlite');
    orm = repositories.createOrm(sqlite);
  } else {
    admin = new pg.Client({ connectionString: dsn });
    await admin.connect();
    await admin.query(`CREATE SCHEMA ${schema}`);
    pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 6 });
    await pool.query(readFileSync(createSQL, 'utf8'));
    orm = createOrm(pool);
  }
  app = new Hono();
  app.route('/', buildRouter({}, { authenticate, orm }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
});

afterAll(async () => {
  sqlite?.close();
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

/** Calls an operation of a filing's Recurrence as a caller: its status and its answer, the body's data (or the refusal whole). */
async function call(who: Caller, id: string, operation: string, body: unknown): Promise<{ status: number; body: any }> {
  const { subject, permissions } = callers[who];
  const response = await app.request(`/api/filing/${id}/recurrence/${operation}`, {
    method: 'POST',
    headers: { authorization: `Bearer ${subject}:${permissions.join(',')}`, 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  const json = (await response.json()) as { data?: unknown };
  return { status: response.status, body: json.data ?? json };
}

const DAY = 86_400_000;

/** A wall date-time at 09:00 of the first weekday at least some days from now, as setRecurrence takes it: no weekend moves it. */
function londonDay(daysFromNow: number): string {
  let at = new Date(Date.now() + daysFromNow * DAY);
  while (at.getUTCDay() === 0 || at.getUTCDay() === 6) at = new Date(at.getTime() + DAY);
  return `${at.toISOString().slice(0, 10)}T09:00`;
}

let filings = 0;
async function newFiling(own: Record<string, unknown> = {}): Promise<string> {
  const row = (await orm.filing.createOne({ title: `Filing ${++filings}`, ...own } as never)) as unknown as Record<string, any>;
  return row.id as string;
}

/** A statement's text in the dialect: Postgres's $n as SQLite's ?n, and its clock. */
const sql = (text: string) => (onSQLite ? text.replace(/\$(\d+)/gu, '?$1').replaceAll('now()', "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')") : text);

/** Runs a statement on the test's own connection, the pool's or the file's: its rows and how many it changed. */
async function raw(text: string, values: unknown[] = []): Promise<{ rows: Record<string, any>[]; rowCount: number }> {
  if (onSQLite) {
    const result = await sqlite!.prepare(sql(text)).bind(...(values as never[])).all<Record<string, any>>();
    return { rows: result.results, rowCount: result.meta.changes };
  }
  const result = await pool!.query(text, values);
  return { rows: result.rows, rowCount: result.rowCount ?? 0 };
}

/** A time column as epoch milliseconds: pg reads a Date, SQLite its RFC 3339 text. */
const millis = (value: unknown) => (value === null || value === undefined ? value : value instanceof Date ? value.getTime() : typeof value === 'string' ? Date.parse(value) : Number(value));
const TIMES = ['due_at', 'opens_at', 'remind_at', 'overdue_at', 'reminded_at'];

/** A filing's occurrences by SQL, in due order, each time as epoch milliseconds. */
async function occurrences(id: string): Promise<Record<string, any>[]> {
  const { rows } = await raw(
    `SELECT key, due, due_at, opens_at, remind_at, overdue_at, state, revision, shifted, reminded_at, completed_by_table, completed_by_key, completed_by_principal FROM filing_recurrence_occurrences WHERE filing_id = $1 ORDER BY due_at, key`,
    [uuidToDb(id)]
  );
  return rows.map(row => ({ ...row, ...Object.fromEntries(TIMES.map(column => [column, millis(row[column])])) }));
}

/** A filing's live deadline messages, by name. */
async function deadlines(id: string): Promise<Map<string, number>> {
  const { rows } = await raw(`SELECT name, at FROM filing_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed')`, [uuidToDb(id)]);
  return new Map(rows.map(row => [String(row.name), Number(row.at)]));
}

async function makeDue(id: string, name: string): Promise<void> {
  const { rowCount } = await raw(`UPDATE filing_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = $2 AND state IN ('ready', 'claimed')`, [uuidToDb(id), name]);
  expect(rowCount).toBe(1);
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));
/** What Recurrence emitted, as the project's Workers methods heard it (D153): the kind and the occurrence's key. */
const heard: { kind: string; key: string; row: string }[] = [];
const hear = (kind: string) => async (event: { key: string; payload: { key: string } }) => {
  heard.push({ kind, key: event.payload.key, row: event.key });
};
const workers = {
  filingDeadlinesRecurrenceOpened: hear('opened'),
  filingDeadlinesRecurrenceReminded: hear('reminded'),
  filingDeadlinesRecurrenceMissed: hear('missed'),
};
const worker = (): WorkerQueue[] => (onSQLite ? sqliteQueues() : (filingDeadlinesQueues(pool, orm, workers, logger as never) as WorkerQueue[]));

/** The caller the generated FilingDeadlines' commands run as. */
const workerCaller: CoreCaller = { subject: 'fixture-recurrence-api/FilingDeadlines', permissions: ['filings.work', 'filings.manage'] };

/**
 * FilingDeadlines' queues on SQLite: the generated filingDeadlinesQueues'
 * handlers over the SQLite queue client, as a server under stack dev
 * claims a sqlite-first database's messages (sqliteQueue): each deadline
 * and change run through the repository in one transaction with its
 * message's completion, and each event handed to the Workers method of
 * its kind in the transaction that spends its claim.
 */
function sqliteQueues(): WorkerQueue[] {
  const queue = (definition: unknown, handle: (claim: { id: string; token: string; message: any }) => Promise<void>): WorkerQueue => {
    const q = sqliteQueue(sqlite!, definition as never);
    return {
      name: q.name,
      leaseSeconds: q.leaseSeconds,
      claim: async (n: number, leaseSeconds?: number) =>
        (await q.claim(n, leaseSeconds)).map(claim => ({ id: claim.id, token: claim.token, attempt: claim.attempt, handle: () => handle(claim as never) })),
      extend: (id: string, token: string, leaseSeconds?: number) => q.extend(id, token, leaseSeconds),
      // The handler completes its message in the transaction that runs it.
      complete: async () => {},
      fail: (id: string, token: string, cause: unknown) => q.fail(id, token, cause),
      release: (id: string, token: string) => q.release(id, token),
      prune: (limit?: number) => q.prune(limit),
      nextDue: () => q.nextDue(),
    } as unknown as WorkerQueue;
  };
  const filing = (tx: unknown) => (tx as { filing: any }).filing;
  const method = (kind: string) => (workers as Record<string, (event: { key: string; payload: { key: string } }) => Promise<void>>)[`filingDeadlinesRecurrence${kind[0]!.toUpperCase()}${kind.slice(1)}`]!;
  return [
    queue(filingDeadlineQueue, async claim => {
      await orm.transaction(tx => filing(tx).runDeadline(claim.message, { claim: { id: claim.id, token: claim.token }, caller: workerCaller }));
    }),
    queue(filingChangeQueue, async claim => {
      await orm.transaction(tx => filing(tx).runChange(claim.message, { claim: { id: claim.id, token: claim.token }, caller: workerCaller }));
    }),
    queue(filingEventQueue, async claim => {
      await orm
        .withBehaviorCaller(workerCaller)
        .transaction(tx => filing(tx).runEvent(claim.message, { claim: { id: claim.id, token: claim.token } }, () => method(claim.message.kind)({ key: claim.message.rowKey, payload: claim.message.payload })));
    }),
  ];
}

async function workUntil(done: () => Promise<boolean>): Promise<void> {
  const stop = new AbortController();
  const running = Promise.all(worker().map(queue => runWorker(queue, { signal: stop.signal, concurrency: 2, idleMs: 100, graceMs: 2000 })));
  try {
    const deadline = Date.now() + 15_000;
    while (!(await done()) && Date.now() < deadline) await sleep(100);
  } finally {
    stop.abort();
    await running;
  }
  expect(await done()).toBe(true);
}

test('setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines', async () => {
  const start = londonDay(40);
  // The first due day is a holiday of the east calendar: it moves to the
  // next business day.
  await orm.holiday.createOne({ calendar: 'east', date: start.slice(0, 10), name: 'Closed' } as never);
  const id = await newFiling({ holidayCalendar: 'east' });
  const set = await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=MONTHLY', start });
  expect(set.status).toBe(200);
  expect(set.body.recurrence.revision).toBe(1);
  const rows = await occurrences(id);
  expect(rows.length).toBeGreaterThanOrEqual(12);
  expect(rows[0].key).toBe(start);
  expect(rows[0].shifted).toBe('holiday');
  // A monthly rule's later occurrences fall on any day: some move off a
  // weekend, and each has its key.
  expect(rows[0].due).not.toBe(start);
  expect(rows.every(row => row.state === 'upcoming')).toBe(true);
  const armed = await deadlines(id);
  expect(armed.get('open')).toBe(Number(rows[0].opens_at));
  expect(armed.get('remind')).toBe(Number(rows[0].remind_at));
  expect(armed.has('overdue')).toBe(false);
  const listed = await call('worker', id, 'list-occurrences', { states: ['upcoming'] });
  expect(listed.body.items[0].key).toBe(start);
});

test('a rule change leaves the open occurrence untouched and replans the ones that have not opened', async () => {
  const id = await newFiling();
  // Due in ten days: its two-week window is open at once.
  expect((await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=MONTHLY', start: londonDay(10) })).status).toBe(200);
  const before = await occurrences(id);
  expect(before[0].state).toBe('open');
  const changed = await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=WEEKLY', start: londonDay(20) });
  expect(changed.status).toBe(200);
  expect(changed.body.kept).toEqual([before[0].key]);
  const after = await occurrences(id);
  expect(after[0]).toEqual(before[0]);
  expect(after.slice(1).every(row => Number(row.revision) === 2)).toBe(true);
  // The rest are the weekly rule's: a week apart, give or take a change of
  // the clocks.
  const gaps = after.slice(2).map((row, i) => Number(row.due_at) - Number(after[i + 1]!.due_at));
  expect(gaps.length).toBeGreaterThan(40);
  expect(gaps.every(gap => Math.abs(gap - 7 * DAY) <= 3_600_000)).toBe(true);
});

test('the remind deadline fires on the worker, records the reminder and emits the opening and the reminder', async () => {
  const id = await newFiling();
  expect((await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=YEARLY', start: londonDay(30) })).status).toBe(200);
  const first = (await occurrences(id))[0]!;
  expect(first.reminded_at).toBeNull();
  await makeDue(id, 'remind');
  await workUntil(async () => (await occurrences(id))[0]!.reminded_at !== null);
  const fired = (await occurrences(id))[0]!;
  expect(Number(fired.reminded_at)).toBeGreaterThanOrEqual(Number(first.remind_at));
  // Three days before its due, its two-week window has opened too.
  expect(fired.state).toBe('open');
  // The fire emitted both moves, which the worker handed to the project's
  // Workers methods (D153).
  await workUntil(async () => heard.some(e => e.row === id && e.kind === 'reminded'));
  expect(heard.filter(e => e.row === id).map(e => [e.kind, e.key]).sort()).toEqual([
    ['opened', fired.key],
    ['reminded', fired.key],
  ]);
});

test('complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it', async () => {
  const id = await newFiling();
  expect((await call('manager', id, 'set-recurrence', { anchor: 'afterDone', every: 'P90D', start: londonDay(5) })).status).toBe(200);
  const done = await call('worker', id, 'complete', { completedBy: { table: 'Payment', key: 'p-17' } });
  expect(done.status).toBe(200);
  expect(done.body).toMatchObject({ state: 'done', completedBy: { table: 'Payment', key: 'p-17' }, completedByPrincipal: callers.worker.subject });
  const rows = await occurrences(id);
  expect(rows.map(row => row.state)).toEqual(['done', 'upcoming']);
  expect(rows[0]).toMatchObject({ completed_by_table: 'Payment', completed_by_key: 'p-17' });
});

test('without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400', async () => {
  const id = await newFiling();
  expect((await call('worker', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=YEARLY', start: londonDay(5) })).status).toBe(403);
  const bad = await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=SOMETIMES', start: londonDay(5) });
  expect(bad.status).toBe(400);
});

test('the calendar merges a contract end date and a filing occurrences, each dated as its source says', async () => {
  const ends = londonDay(30).slice(0, 10);
  const contract = (await orm.contract.createOne({ title: 'Lease', endsOn: ends } as never)) as unknown as Record<string, any>;
  const id = await newFiling({ title: 'Quarterly return' });
  expect((await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=MONTHLY', start: londonDay(10) })).status).toBe(200);
  const read = await readCalendar(orm, { subject: callers.manager.subject, permissions: [...callers.manager.permissions] }, { from: new Date(), to: new Date(Date.now() + 45 * DAY) });
  const renewal = read.entries.find(entry => entry.source === 'contract' && entry.key === contract.id);
  expect(renewal).toMatchObject({ kind: 'renewal', title: 'Lease', start: ends, allDay: true });
  const deadlines = read.entries.filter(entry => entry.source === 'filing' && entry.key === id);
  expect(deadlines.length).toBeGreaterThanOrEqual(1);
  expect(deadlines[0]).toMatchObject({ kind: 'deadline', color: 'filing', title: 'Quarterly return', occurrence: { key: londonDay(10), state: 'open' } });
  const starts = read.entries.map(entry => entry.start);
  expect([...starts].sort()).toEqual(starts);
});

test('a raw SQL write of a filing holiday calendar is heard by the worker, which plans again', async () => {
  const start = londonDay(60);
  await orm.holiday.createOne({ calendar: 'west', date: start.slice(0, 10), name: 'Closed' } as never);
  const id = await newFiling();
  expect((await call('manager', id, 'set-recurrence', { anchor: 'calendar', rule: 'FREQ=MONTHLY', start })).status).toBe(200);
  expect((await occurrences(id))[0]!.shifted).toBeNull();
  await raw(`UPDATE filing SET holiday_calendar = 'west' WHERE id = $1`, [uuidToDb(id)]);
  await workUntil(async () => (await occurrences(id))[0]!.shifted === 'holiday');
});
