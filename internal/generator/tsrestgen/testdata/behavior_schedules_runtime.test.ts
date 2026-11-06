/*
fixture-schedules-api's wake-ups end to end (D104), run by
TestGeneratedSchedulesEndToEnd with API_DIR set to the materialized
package, SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-schedules-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-schedules-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
worker ProblemWakeUps runs in process, its queues as the API's package
generates them (problemWakeUpsQueues: Problem's deadlines, changes and
events) under the runtime's runWorker, with a Workers of the test's own.

A Problem implements Schedules: wake-ups after a delay, at an interval or
on a cron line in America/New_York, a minute to thirty days apart, at most
ten live a problem, one live name each, set by a caller holding
problems.schedule; each run is an event of the table's event queue, which
the worker hands to the project's Workers method
problemWakeUpsSchedulesRun. The test's method records each event it gets
as its key, name, run and attempt; when the test has set it to fail the
next events, it rejects each with the error "the agent was not told";
otherwise it reports the run through the event's transaction as the
worker, reportRun({ name, run, attempt, outcome }), the outcome the test set for
the schedule's name, else told, and a row gone completes the event. A
missed run is tried again five minutes on, and a problem that is done or
dropped finishes its wake-ups. A deadline's fire is driven as the Seats
end-to-end tests drive one: its message made due with SQL, then the
worker's queues until the row's schedule shows the run, the command
running at the later of the clock and the deadline's time. The subjects
are fresh UUIDs in their base62 form, as the identity runtime's principals
are.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { NotFoundError, uuidFromDb, uuidToDb } from '@superschematic/http-runtime/postgres';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-schedules-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, problemWakeUpsQueues } = await import(`${apiDir}/index.ts`);
const { FixtureSchedulesApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

/** The callers: agent holding problems.schedule, and guest holding none. */
const callers = {
  agent: { subject: subject(), permissions: ['problems.schedule'] },
  guest: { subject: subject(), permissions: [] },
} as const;
type Caller = keyof typeof callers;

const GUEST = callers.guest.subject;

/** The bearer credential of a caller, `<subject>[:<permission>,...]`. */
const credential = (who: Caller): string => {
  const { subject, permissions } = callers[who];
  return permissions.length === 0 ? subject : `${subject}:${permissions.join(',')}`;
};

/**
 * Establishes the subject and the permissions the bearer credential names,
 * `<subject>[:<permission>,...]`; no credential, no caller. It reads the
 * header itself, since hono's bearer parser takes no colon.
 */
const authenticate = Object.assign(
  async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization');
    if (!header?.startsWith('Bearer ')) return null;
    const [who, permissions = ''] = header.slice('Bearer '.length).split(':');
    return { subject: who!, permissions: permissions === '' ? [] : permissions.split(',') };
  },
  { readsAuthorization: true }
);

const logger = {
  debug() {},
  info() {},
  warn() {},
  error() {},
  child() {
    return logger;
  },
};

/** The payload of a run event, as Schedules' declaration types it. */
interface RunPayload {
  readonly name: string;
  readonly run: number;
  readonly attempt: number;
  readonly kind: string;
  readonly spec: string;
  readonly timeZone?: string;
  readonly setAt: number;
  readonly dueAt: number;
  readonly firedAt: number;
  readonly nextAt?: number;
}

/** An event the test's handler got: the problem's key in its base62 form, the schedule, the run and the attempt, and the event whole. */
interface Handled {
  readonly key: string;
  readonly name: string;
  readonly run: number;
  readonly attempt: number;
  readonly at: number;
  readonly payload: RunPayload;
}

/** Every event the handler got, in the order it got them. */
const handled: Handled[] = [];
/** How many of the next events the handler fails. */
let failing = 0;
/** The outcome the handler reports for a schedule's runs, by name; told when none. */
const outcomes = new Map<string, string>();

/**
 * The project's Workers, as the test writes them: the handler of the event
 * run of Problem's Schedules, which records the event, fails it while the
 * test says so, and otherwise reports the run through the event's
 * transaction, as the worker; a row gone completes the event.
 */
const workers = {
  async problemWakeUpsSchedulesRun(event: { key: string; at: number; payload: RunPayload }, tx: any): Promise<void> {
    const payload = event.payload;
    handled.push({ key: uuidFromDb(event.key), name: payload.name, run: payload.run, attempt: payload.attempt, at: event.at, payload });
    if (failing > 0) {
      failing--;
      throw new Error('the agent was not told');
    }
    try {
      await tx.problem.schedules.reportRun(event.key, { name: payload.name, run: payload.run, attempt: payload.attempt, outcome: outcomes.get(payload.name) ?? 'told' });
    } catch (error) {
      if (error instanceof NotFoundError) return;
      throw error;
    }
  },
};

const schema = `schedules_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 6 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  app = new Hono();
  app.route('/', buildRouter({}, { authenticate, orm }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());
});

afterAll(async () => {
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

/** The SDK as one of the callers, its fetch handing each request to the app. */
function sdk(who: Caller) {
  return new FixtureSchedulesApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => credential(who) },
  });
}

/** The problem a call through the SDK is refused with, whole: its status, code, detail and details. */
async function refusal(call: Promise<unknown>): Promise<{ status: number; code?: string; detail: string; details?: any }> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    const refused = error as { statusCode: number; code?: string; message: string; response?: { details?: unknown } };
    return { status: refused.statusCode, code: refused.code, detail: refused.message, details: refused.response?.details };
  }
  throw new Error('the call was not refused');
}

/** A problem's version, by SQL, as the contract's check reads it. */
async function version(id: string): Promise<number> {
  const { rows } = await pool.query(`SELECT _version FROM problem WHERE id = $1`, [uuidToDb(id)]);
  return Number(rows[0]!._version);
}

let problems = 0;
/** A problem of its own, created through the repository: open. Its key. */
async function newProblem(): Promise<string> {
  const row = (await orm.problem.createOne({ title: `Problem ${++problems}` } as never)) as unknown as Record<string, any>;
  expect(row).toMatchObject({ status: 'open' });
  return row.id as string;
}

/** A schedule of a problem as listSchedules reads it. */
async function listed(id: string, name: string): Promise<Record<string, any> | undefined> {
  return (await sdk('agent').problem.schedules.listSchedules(id, { name })).items[0];
}

/** A problem's live deadline messages, by SQL in their names' order: no repository reads the queue. */
async function deadlines(id: string): Promise<{ use: string; name: string; at: number; dueAt: number }[]> {
  const { rows } = await pool.query(`SELECT use_id, name, at, due_at FROM problem_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed') ORDER BY name`, [uuidToDb(id)]);
  return rows.map(row => ({ use: String(row.use_id), name: String(row.name), at: Number(row.at), dueAt: (row.due_at as Date).getTime() }));
}

/** A problem's event messages, every state, in the order written. */
async function events(id: string): Promise<{ id: string; state: string; attempts: number; dueAt: number; lastError: string | null; kind: string }[]> {
  const { rows } = await pool.query(`SELECT id::text AS id, state, attempts, due_at, last_error, kind FROM problem_event_queue WHERE row_key = $1 ORDER BY created_at, id`, [uuidToDb(id)]);
  return rows.map(row => ({ id: String(row.id), state: String(row.state), attempts: Number(row.attempts), dueAt: (row.due_at as Date).getTime(), lastError: row.last_error === null ? null : String(row.last_error), kind: String(row.kind) }));
}

/** Makes a problem's live deadline message of a schedule due now with SQL: the escape hatch an operator has. */
async function makeDue(id: string, name: string): Promise<void> {
  const { rowCount } = await pool.query(`UPDATE problem_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = $2 AND state IN ('ready', 'claimed')`, [uuidToDb(id), name]);
  expect(rowCount).toBe(1);
}

/** The events the handler got of a problem's schedule. */
const handledOf = (id: string, name: string): Handled[] => handled.filter(event => event.key === id && event.name === name);

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

const worker = () => problemWakeUpsQueues(pool, orm, workers, logger as never) as WorkerQueue[];

/** Runs the worker's queues until done holds, asking every 100 ms for at most fifteen seconds, then stops them. */
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

/** Runs the worker's queues for ms, then stops them. */
async function workFor(ms: number): Promise<void> {
  const stop = new AbortController();
  const running = Promise.all(worker().map(queue => runWorker(queue, { signal: stop.signal, concurrency: 2, idleMs: 100, graceMs: 2000 })));
  await sleep(ms);
  stop.abort();
  await running;
}

/** Sets n wake-ups on a problem, w0 to w(n-1), each every ten minutes. */
async function fill(id: string, n: number): Promise<string[]> {
  const names = Array.from({ length: n }, (_, i) => `w${i}`);
  for (const name of names) expect(await sdk('agent').problem.schedules.setSchedule(id, { name, every: 600_000 })).toMatchObject({ name, state: 'active' });
  return names;
}

/** A time's wall clock in a zone: its weekday, hour and minute. */
function wallClock(ms: number, timeZone: string): { weekday: string; hour: string; minute: string } {
  const parts = new Intl.DateTimeFormat('en-US', { timeZone, weekday: 'short', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).formatToParts(new Date(ms));
  const part = (type: string) => parts.find(p => p.type === type)!.value;
  return { weekday: part('weekday'), hour: part('hour'), minute: part('minute') };
}

let ids = 0;
/** A JSON-RPC call of the MCP endpoint as the caller credential names: its message. */
async function mcp(token: string, method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: `Bearer ${token}` },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  const text = await response.text();
  expect(text.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(text.slice('event: message\ndata: '.length));
}

const MINUTE = 60_000;
const DAY = 24 * 60 * MINUTE;

describe("a problem's wake-ups on fixture-schedules-api", () => {
  test("setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt", async () => {
    const id = await newProblem();
    const before = Date.now();
    const nudge = await sdk('agent').problem.schedules.setSchedule(id, { name: 'nudge', after: 300_000 });
    const hourly = await sdk('agent').problem.schedules.setSchedule(id, { name: 'hourly', every: 3_600_000 });
    const morning = await sdk('agent').problem.schedules.setSchedule(id, { name: 'morning', cron: '0 9 * * MON-FRI' });
    const after = Date.now();
    expect(nudge).toEqual({ name: 'nudge', kind: 'after', spec: '300000', state: 'active', setAt: expect.any(Number), nextAt: nudge.setAt + 300_000, runs: 0 });
    expect(hourly).toEqual({ name: 'hourly', kind: 'every', spec: '3600000', state: 'active', setAt: expect.any(Number), nextAt: hourly.setAt + 3_600_000, runs: 0 });
    expect(morning).toEqual({ name: 'morning', kind: 'cron', spec: '0 9 * * MON-FRI', timeZone: 'America/New_York', state: 'active', setAt: expect.any(Number), nextAt: expect.any(Number), runs: 0 });
    for (const set of [nudge, hourly, morning]) {
      expect(set.setAt).toBeGreaterThanOrEqual(before);
      expect(set.setAt).toBeLessThanOrEqual(after);
    }
    // The cron line's next run is the first weekday 09:00 in New York
    // after it was set: within three days and a little more.
    expect(morning.nextAt).toBeGreaterThan(morning.setAt);
    expect(morning.nextAt - morning.setAt).toBeLessThanOrEqual(3 * DAY + 9 * 60 * MINUTE);
    const wall = wallClock(morning.nextAt, 'America/New_York');
    expect({ hour: wall.hour, minute: wall.minute }).toEqual({ hour: '09', minute: '00' });
    expect(['Mon', 'Tue', 'Wed', 'Thu', 'Fri']).toContain(wall.weekday);
    expect(morning.nextAt % MINUTE).toBe(0);
    expect(await deadlines(id)).toEqual([
      { use: 'Schedules', name: 'hourly', at: hourly.nextAt, dueAt: hourly.nextAt },
      { use: 'Schedules', name: 'morning', at: morning.nextAt, dueAt: morning.nextAt },
      { use: 'Schedules', name: 'nudge', at: nudge.nextAt, dueAt: nudge.nextAt },
    ]);
    // A set is no run: nothing is queued for the handler.
    expect(await events(id)).toEqual([]);
  });

  test("a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was", async () => {
    const id = await newProblem();
    // The MCP tool, listed with its schema, sets a wake-up as any operation runs.
    const { tools } = (await mcp(credential('agent'), 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'problem_schedules_set_schedule');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['after', 'cron', 'every', 'id', 'name', 'timeZone']);
    const called = (await mcp(credential('agent'), 'tools/call', { name: 'problem_schedules_set_schedule', arguments: { id, name: 'hourly', every: 3_600_000 } })).result;
    expect(called.isError).toBeFalsy();
    const set = called.structuredContent;
    expect(set).toMatchObject({ name: 'hourly', kind: 'every', spec: '3600000', state: 'active', runs: 0 });
    const before = await version(id);
    expect(await sdk('agent').problem.schedules.setSchedule(id, { name: 'hourly', every: 3_600_000 })).toEqual({ ...set, unchanged: true });
    expect(await version(id)).toBe(before);
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'hourly', at: set.nextAt, dueAt: set.nextAt }]);
  });

  test("setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was", async () => {
    const id = await newProblem();
    const at = await version(id);
    expect(await refusal(sdk('guest').problem.schedules.setSchedule(id, { name: 'nudge', after: 300_000 }))).toEqual({
      status: 403,
      code: 'forbidden',
      detail: `${GUEST} may not set a schedule of Problem ${id}: it needs permission problems.schedule`,
      details: undefined,
    });
    expect(await version(id)).toBe(at);
    const reason = '1000 is outside minMs..maxMs (60000..2592000000): a delay lies between them';
    expect(await refusal(sdk('agent').problem.schedules.setSchedule(id, { name: 'nudge', after: 1000 }))).toEqual({
      status: 400,
      code: 'invalid_argument',
      detail: `operation setSchedule of behavior Schedules: /after: ${reason}`,
      details: { issues: [{ path: '/after', message: reason }] },
    });
    expect(await version(id)).toBe(at);
    const names = await fill(id, 10);
    const full = await version(id);
    const limit = `it holds 10 live schedules (${[...names].sort().join(', ')}), and its max is 10: cancel one to set another`;
    expect(await refusal(sdk('agent').problem.schedules.setSchedule(id, { name: 'eleventh', every: 600_000 }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: `behavior Schedules vetoes setSchedule of Problem ${id}: ${limit}`,
      details: { behavior: 'Schedules', action: 'setSchedule', reason: limit, vetoCode: 'schedule_limit', vetoDetails: { max: 10, live: [...names].sort() } },
    });
    expect(await version(id)).toBe(full);
    expect((await sdk('agent').problem.schedules.listSchedules(id, {})).items.map((item: { name: string }) => item.name)).toEqual([...names].sort());
  });

  test("a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told", async () => {
    const id = await newProblem();
    const set = await sdk('agent').problem.schedules.setSchedule(id, { name: 'nudge', after: 300_000 });
    await makeDue(id, 'nudge');
    await workUntil(async () => (await listed(id, 'nudge'))?.lastOutcome === 'told');
    // The fire ran at its deadline's time, the later of it and the clock:
    // the run was due then, fired then, and the event carries that time.
    expect(handledOf(id, 'nudge')).toEqual([
      { key: id, name: 'nudge', run: 1, attempt: 1, at: set.nextAt, payload: { name: 'nudge', run: 1, attempt: 1, kind: 'after', spec: '300000', setAt: set.setAt, dueAt: set.nextAt, firedAt: set.nextAt } },
    ]);
    // An after runs once: it finished with its run, and the handler's
    // report recorded told.
    expect(await listed(id, 'nudge')).toEqual({
      name: 'nudge',
      kind: 'after',
      spec: '300000',
      state: 'finished',
      setAt: set.setAt,
      runs: 1,
      attempt: 1,
      lastAt: set.nextAt,
      lastOutcome: 'told',
      finishedAt: set.nextAt,
      endedBy: 'run',
    });
    expect(await deadlines(id)).toEqual([]);
    expect(await events(id)).toEqual([]);
  });

  test("the handler runs once per event: a completed event's message is gone and is not handed to it again", async () => {
    const id = await newProblem();
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'once', after: MINUTE });
    await makeDue(id, 'once');
    await workUntil(async () => (await listed(id, 'once'))?.lastOutcome === 'told');
    expect(handledOf(id, 'once').map(event => [event.run, event.attempt])).toEqual([[1, 1]]);
    expect(await events(id)).toEqual([]);
    await workFor(1000);
    expect(handledOf(id, 'once')).toHaveLength(1);
  });

  test("a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again", async () => {
    const id = await newProblem();
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'flaky', after: MINUTE });
    failing = 1;
    await makeDue(id, 'flaky');
    await workUntil(async () => (await events(id)).some(event => event.attempts === 1 && event.state === 'ready'));
    expect(failing).toBe(0);
    expect(handledOf(id, 'flaky').map(event => [event.run, event.attempt])).toEqual([[1, 1]]);
    const [failed, ...more] = await events(id);
    expect(more).toEqual([]);
    expect(failed).toMatchObject({ state: 'ready', attempts: 1, kind: 'run' });
    expect(failed!.dueAt - Date.now()).toBeGreaterThan(20_000);
    expect(failed!.lastError).toContain('the agent was not told');
    // The fire committed: the run is recorded, its outcome is not.
    const fired = await listed(id, 'flaky');
    expect(fired).toMatchObject({ state: 'finished', runs: 1, attempt: 1 });
    expect(fired!.lastOutcome).toBeUndefined();
    const { rowCount } = await pool.query(`UPDATE problem_event_queue SET due_at = now() WHERE id = $1`, [failed!.id]);
    expect(rowCount).toBe(1);
    await workUntil(async () => (await listed(id, 'flaky'))?.lastOutcome === 'told');
    // The same event, run 1's first attempt, handed again.
    expect(handledOf(id, 'flaky').map(event => [event.run, event.attempt])).toEqual([
      [1, 1],
      [1, 1],
    ]);
    expect(await events(id)).toEqual([]);
  });

  test('an interval overdue by many runs fires once, and its next run is the first of its grid after the fire', async () => {
    const id = await newProblem();
    const set = await sdk('agent').problem.schedules.setSchedule(id, { name: 'tick', every: MINUTE });
    // Ten minutes back, by SQL: the schedule set then, its next run nine
    // minutes ago, and its deadline due now.
    await pool.query(`UPDATE problem_schedules_schedules SET set_at = set_at - interval '10 minutes', next_at = next_at - interval '10 minutes' WHERE problem_id = $1 AND name = $2`, [uuidToDb(id), 'tick']);
    const { rowCount } = await pool.query(`UPDATE problem_deadline_queue SET at = at - 600000, due_at = now() WHERE row_key = $1 AND name = $2 AND state = 'ready'`, [uuidToDb(id), 'tick']);
    expect(rowCount).toBe(1);
    const anchor = set.setAt - 10 * MINUTE;
    const before = Date.now();
    await workUntil(async () => (await listed(id, 'tick'))?.lastOutcome === 'told');
    const after = Date.now();
    const runs = handledOf(id, 'tick');
    expect(runs).toHaveLength(1);
    const [{ payload }] = runs as [Handled];
    expect(payload).toMatchObject({ name: 'tick', run: 1, attempt: 1, kind: 'every', spec: '60000', dueAt: anchor + MINUTE });
    expect(payload.firedAt).toBeGreaterThanOrEqual(before);
    expect(payload.firedAt).toBeLessThanOrEqual(after);
    // Its next run is on its grid, the first point after the fire.
    expect((payload.nextAt! - anchor) % MINUTE).toBe(0);
    expect(payload.nextAt!).toBeGreaterThan(payload.firedAt);
    expect(payload.nextAt! - payload.firedAt).toBeLessThanOrEqual(MINUTE);
    expect(await listed(id, 'tick')).toMatchObject({ state: 'active', runs: 1, nextAt: payload.nextAt, lastAt: payload.firedAt });
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'tick', at: payload.nextAt!, dueAt: payload.nextAt! }]);
  });

  test('a missed run is tried again five minutes on, as attempt 2 of the same run', async () => {
    const id = await newProblem();
    const set = await sdk('agent').problem.schedules.setSchedule(id, { name: 'retry', every: 3_600_000 });
    outcomes.set('retry', 'missed');
    await makeDue(id, 'retry');
    const reported = Date.now();
    await workUntil(async () => (await listed(id, 'retry'))?.lastOutcome === 'missed');
    const missed = await listed(id, 'retry');
    expect(missed).toMatchObject({ state: 'active', runs: 1, attempt: 1, lastAt: set.nextAt, nextAt: set.nextAt + 3_600_000, lastOutcome: 'missed' });
    // The retry is five minutes after the report, before the next run.
    expect(missed!.retryAt).toBeGreaterThanOrEqual(reported + 300_000);
    expect(missed!.retryAt).toBeLessThanOrEqual(Date.now() + 300_000);
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'retry', at: missed!.retryAt, dueAt: missed!.retryAt }]);
    outcomes.set('retry', 'told');
    await makeDue(id, 'retry');
    await workUntil(async () => (await listed(id, 'retry'))?.lastOutcome === 'told');
    expect(handledOf(id, 'retry').map(event => event.payload)).toEqual([
      { name: 'retry', run: 1, attempt: 1, kind: 'every', spec: '3600000', setAt: set.setAt, dueAt: set.nextAt, firedAt: set.nextAt, nextAt: set.nextAt + 3_600_000 },
      { name: 'retry', run: 1, attempt: 2, kind: 'every', spec: '3600000', setAt: set.setAt, dueAt: missed!.retryAt, firedAt: missed!.retryAt, nextAt: set.nextAt + 3_600_000 },
    ]);
    const told = await listed(id, 'retry');
    expect(told).toMatchObject({ state: 'active', runs: 1, attempt: 2, lastAt: missed!.retryAt, lastOutcome: 'told' });
    expect(told!.retryAt).toBeUndefined();
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'retry', at: set.nextAt + 3_600_000, dueAt: set.nextAt + 3_600_000 }]);
  });

  test('a report of an older run answers unchanged, and one of a run not had is 409 no_run', async () => {
    const id = await newProblem();
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'tick', every: 600_000 });
    await makeDue(id, 'tick');
    await workUntil(async () => (await listed(id, 'tick'))?.lastOutcome === 'told');
    await makeDue(id, 'tick');
    await workUntil(async () => {
      const tick = await listed(id, 'tick');
      return tick?.runs === 2 && tick.lastOutcome === 'told';
    });
    const at = await version(id);
    expect(await sdk('agent').problem.schedules.reportRun(id, { name: 'tick', run: 1, outcome: 'held' })).toEqual({ name: 'tick', run: 1, outcome: 'held', unchanged: true });
    expect(await version(id)).toBe(at);
    const reason = 'its schedule "tick" has had 2 runs, and no run 3 to report';
    expect(await refusal(sdk('agent').problem.schedules.reportRun(id, { name: 'tick', run: 3, outcome: 'told' }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: `behavior Schedules vetoes reportRun of Problem ${id}: ${reason}`,
      details: { behavior: 'Schedules', action: 'reportRun', reason, vetoCode: 'no_run', vetoDetails: { name: 'tick', run: 3, runs: 2 } },
    });
    expect(await version(id)).toBe(at);
    expect(await listed(id, 'tick')).toMatchObject({ runs: 2, lastOutcome: 'told' });
  });

  test('pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once', async () => {
    const id = await newProblem();
    const set = await sdk('agent').problem.schedules.setSchedule(id, { name: 'tick', every: 600_000 });
    expect(await sdk('agent').problem.schedules.pauseSchedule(id, { name: 'tick' })).toEqual({ ...set, state: 'paused' });
    expect(await deadlines(id)).toEqual([]);
    // While paused, its next run passes, by SQL.
    await pool.query(`UPDATE problem_schedules_schedules SET next_at = now() - interval '1 hour' WHERE problem_id = $1 AND name = $2`, [uuidToDb(id), 'tick']);
    const resumed = await sdk('agent').problem.schedules.resumeSchedule(id, { name: 'tick' });
    expect(resumed).toMatchObject({ name: 'tick', state: 'active', setAt: set.setAt, runs: 0 });
    expect(resumed.nextAt).toBeLessThan(Date.now() - 50 * MINUTE);
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'tick', at: resumed.nextAt, dueAt: resumed.nextAt }]);
    const before = Date.now();
    await workUntil(async () => (await listed(id, 'tick'))?.lastOutcome === 'told');
    const runs = handledOf(id, 'tick');
    expect(runs).toHaveLength(1);
    const [{ payload }] = runs as [Handled];
    expect(payload).toMatchObject({ name: 'tick', run: 1, attempt: 1, kind: 'every', spec: '600000', dueAt: resumed.nextAt, nextAt: set.setAt + 600_000 });
    expect(payload.firedAt).toBeGreaterThanOrEqual(before);
    expect(await deadlines(id)).toEqual([{ use: 'Schedules', name: 'tick', at: set.setAt + 600_000, dueAt: set.setAt + 600_000 }]);
  });

  test('cancelSchedule finishes a wake-up and frees its place under max', async () => {
    const id = await newProblem();
    const names = await fill(id, 10);
    const before = Date.now();
    const cancelled = await sdk('agent').problem.schedules.cancelSchedule(id, { name: 'w0' });
    expect(cancelled).toEqual({ name: 'w0', kind: 'every', spec: '600000', state: 'finished', setAt: expect.any(Number), runs: 0, finishedAt: expect.any(Number), endedBy: 'cancel' });
    expect(cancelled.finishedAt).toBeGreaterThanOrEqual(before);
    expect((await deadlines(id)).map(deadline => deadline.name)).toEqual(names.filter(name => name !== 'w0').sort());
    expect(await sdk('agent').problem.schedules.setSchedule(id, { name: 'w10', every: 600_000 })).toMatchObject({ name: 'w10', state: 'active' });
    const items = (await sdk('agent').problem.schedules.listSchedules(id, {})).items as { name: string; state: string }[];
    expect(items.filter(item => item.state === 'active')).toHaveLength(10);
    expect(items.find(item => item.name === 'w0')).toMatchObject({ state: 'finished', endedBy: 'cancel' });
  });

  test('a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended', async () => {
    const id = await newProblem();
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'tick', every: 600_000 });
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'nudge', after: 300_000 });
    expect(await sdk('agent').problem.status.transition(id, { to: 'working' })).toEqual({ from: 'open', to: 'working' });
    expect(await deadlines(id)).toHaveLength(2);
    const before = Date.now();
    expect(await sdk('agent').problem.status.transition(id, { to: 'done' })).toEqual({ from: 'working', to: 'done' });
    for (const name of ['nudge', 'tick']) {
      const schedule = await listed(id, name);
      expect(schedule).toMatchObject({ name, state: 'finished', endedBy: 'endOn' });
      expect(schedule!.nextAt).toBeUndefined();
      expect(schedule!.finishedAt).toBeGreaterThanOrEqual(before);
    }
    expect(await deadlines(id)).toEqual([]);
    const at = await version(id);
    const reason = 'its status is done, which ends its schedules (endOn): it takes no live schedule';
    expect(await refusal(sdk('agent').problem.schedules.setSchedule(id, { name: 'more', after: 300_000 }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: `behavior Schedules vetoes setSchedule of Problem ${id}: ${reason}`,
      details: { behavior: 'Schedules', action: 'setSchedule', reason, vetoCode: 'schedules_ended', vetoDetails: { status: 'done' } },
    });
    expect(await version(id)).toBe(at);
  });

  test("a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups", async () => {
    const id = await newProblem();
    await sdk('agent').problem.schedules.setSchedule(id, { name: 'tick', every: 600_000 });
    // Past the core, as an operator's repair: the table's change trigger
    // hears it, and the worker runs Schedules' endOn after the commit.
    const { rowCount } = await pool.query(`UPDATE problem SET status = 'dropped' WHERE id = $1`, [uuidToDb(id)]);
    expect(rowCount).toBe(1);
    await workUntil(async () => (await listed(id, 'tick'))?.state === 'finished');
    expect(await listed(id, 'tick')).toMatchObject({ name: 'tick', state: 'finished', endedBy: 'endOn', runs: 0 });
    expect(await deadlines(id)).toEqual([]);
    expect(handledOf(id, 'tick')).toEqual([]);
  });

  test("listSchedules reads a problem's wake-ups in name order", async () => {
    const id = await newProblem();
    for (const name of ['b', 'a', 'c']) await sdk('agent').problem.schedules.setSchedule(id, { name, every: 600_000 });
    const items = (await sdk('agent').problem.schedules.listSchedules(id, {})).items as { name: string }[];
    expect(items.map(item => item.name)).toEqual(['a', 'b', 'c']);
    expect(await sdk('agent').problem.schedules.listSchedules(id, { name: 'b' })).toEqual({ items: [items[1]] });
    expect(await sdk('agent').problem.schedules.listSchedules(id, { name: 'z' })).toEqual({ items: [] });
    expect(await refusal(sdk('guest').problem.schedules.listSchedules(id, {}))).toEqual({
      status: 403,
      code: 'forbidden',
      detail: `${GUEST} may not list the schedules of Problem ${id}: it needs permission problems.schedule`,
      details: undefined,
    });
  });
});
