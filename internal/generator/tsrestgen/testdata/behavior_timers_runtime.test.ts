/*
fixture-timers-api's Timers end to end (D103), run by
TestGeneratedTimersEndToEnd with API_DIR set to the materialized package,
SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-timers-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-timers-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
worker TicketTimers runs in process, its queues as the API's package
generates them (ticketTimersQueues: Ticket's deadline queue, then its change
queue, since Timers hears its writes) under the runtime's runWorker. A
ticket's first answer is due 1.5 seconds after its create (respond, which
escalates it unless it leaves open first); its wait on its customer closes
it after an hour (idle); and a reminder falls due a day before its visit
(remind). A create arms respond: its sub-table row and its queue message,
due when it falls due; the worker runs it when it falls due, moving the
status and recording the outcome; a status that leaves open cancels it; a
deadline snoozed while the worker holds its claim never fires on its old
arming; resetTimer and snoozeTimer need the permission the config names,
which the core checks; fireTimer through the SDK fires a due deadline and
spends its message; deleting the row removes its deadlines; a raw SQL
update of visitAt, a raw SQL delete of a ticket and a repository's bulk
update of visitAt are heard through the change queue, which the worker runs
(D103): remind re-arms at the new time, and a deleted ticket's messages go;
and the MCP endpoint lists and calls ticket_timers_fire_timer. The only real
wait is respond's 1.5 seconds; the idle deadline is made due with SQL, the
escape hatch an operator has.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { TicketTimersDeadline, TicketStatusState } from '@schemas/fixture-timers-api-types';
import { createOrm } from '@schemas/fixture-timers-db-types/orm';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, ticketTimersQueues } = await import(`${apiDir}/index.ts`);
const { FixtureTimersApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

// The callers: sam may write tickets, kim may write them and reset or
// snooze their deadlines.
const SAM = 'sam:tickets.write';
const KIM = 'kim:tickets.write,tickets.timers';

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

/** What the worker's queues logged: each line's message and fields. */
const logged: { message: string; fields?: Record<string, unknown> }[] = [];
const logger = {
  debug() {},
  info(message: string, fields?: Record<string, unknown>) {
    logged.push({ message, fields });
  },
  warn() {},
  error() {},
  child() {
    return logger;
  },
};

const schema = `behavior_timers_ts_${process.hrtime.bigint()}`;
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

/** The SDK as the caller the credential names, its fetch handing each request to the app. */
function sdk(as: string) {
  return new FixtureTimersApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => as },
  });
}

/** The problem a call is refused with: its status, code and detail. */
async function refusal(call: Promise<unknown>): Promise<{ status: number; code?: string; detail: string }> {
  try {
    await call;
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    const refused = error as { statusCode: number; code?: string; message: string };
    return { status: refused.statusCode, code: refused.code, detail: refused.message };
  }
  throw new Error('the call was not refused');
}

const millis = (value: unknown): number | null => (value === null || value === undefined ? null : value instanceof Date ? value.getTime() : Date.parse(String(value)));

/** A ticket's deadlines' sub-table rows, by name: no repository reads them (D103), so SQL does. */
async function deadlineRows(id: string): Promise<Record<string, { dueAt: number | null; fired: boolean | null; outcome: string | null }>> {
  const { rows } = await pool.query(`SELECT d.name, d.due_at, d.fired, d.outcome FROM ticket_timers_deadlines d JOIN ticket t ON t.id = d.ticket_id WHERE t.title = $1`, [titles.get(id)]);
  return Object.fromEntries(rows.map(row => [row.name, { dueAt: millis(row.due_at), fired: row.fired, outcome: row.outcome }]));
}

/** A ticket's live deadline messages, ready or claimed, by name: the time each was armed at and its due time. */
async function liveMessages(id: string): Promise<Record<string, { at: number; dueAt: number }>> {
  const { rows } = await pool.query(
    `SELECT q.name, q.at, q.due_at FROM ticket_deadline_queue q JOIN ticket t ON t.id = q.row_key WHERE t.title = $1 AND q.state IN ('ready', 'claimed')`,
    [titles.get(id)]
  );
  return Object.fromEntries(rows.map(row => [row.name, { at: Number(row.at), dueAt: millis(row.due_at)! }]));
}

/** Every message of a ticket's deadlines, any state, by row key. */
async function messageCount(id: string): Promise<number> {
  const { rows } = await pool.query(`SELECT count(*) AS n FROM ticket_deadline_queue q JOIN ticket t ON t.id = q.row_key WHERE t.title = $1`, [titles.get(id)]);
  return Number(rows[0].n);
}

/** Each ticket's title by its key, which the SQL reads find it by. */
const titles = new Map<string, string>();

/** Creates a ticket through the repository. */
async function ticket(title: string): Promise<string> {
  const id = (await orm.ticket.createOne({ title })).id;
  titles.set(id, title);
  return id;
}

/** Runs the worker's generated queues until the returned stop is called and resolves. */
function startWorker(): () => Promise<void> {
  const stop = new AbortController();
  const queues: WorkerQueue[] = ticketTimersQueues(pool, orm, logger as never);
  const running = Promise.all(queues.map(queue => runWorker(queue, { signal: stop.signal, concurrency: 2, idleMs: 100, graceMs: 2000 })));
  return async () => {
    stop.abort();
    await running;
  };
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** Runs the worker's queues until done holds, asking every 100 ms for at most ten seconds, then stops them. */
async function workUntil(done: () => Promise<boolean>): Promise<void> {
  const stop = startWorker();
  try {
    const deadline = Date.now() + 10_000;
    while (!(await done()) && Date.now() < deadline) await sleep(100);
  } finally {
    await stop();
  }
}

const DAY = 86_400_000;

/** Creates a ticket with a visit through the repository. */
async function visit(title: string, at: number): Promise<string> {
  const id = (await orm.ticket.createOne({ title, visitAt: new Date(at) })).id;
  titles.set(id, title);
  return id;
}

/** A ticket's live remind messages: what each was armed at. */
async function remindMessages(id: string): Promise<number[]> {
  const { rows } = await pool.query(
    `SELECT q.at FROM ticket_deadline_queue q JOIN ticket t ON t.id = q.row_key WHERE t.title = $1 AND q.name = 'remind' AND q.state IN ('ready', 'claimed')`,
    [titles.get(id)]
  );
  return rows.map(row => Number(row.at));
}

describe('Timers on fixture-timers-api', () => {
  let waiting: string;

  test('a create arms the deadline: its sub-table row and its queue message with its due time', async () => {
    const before = Date.now();
    const id = await ticket('Leak');
    const rows = await deadlineRows(id);
    expect(Object.keys(rows)).toEqual(['respond']);
    expect(rows.respond).toMatchObject({ fired: false });
    expect(rows.respond!.dueAt!).toBeGreaterThanOrEqual(before + 1500);
    expect(rows.respond!.dueAt!).toBeLessThanOrEqual(Date.now() + 1500);
    expect(await liveMessages(id)).toEqual({ respond: { at: rows.respond!.dueAt!, dueAt: rows.respond!.dueAt! } });
  });

  test('a due deadline the worker runs moves the status and records the outcome', async () => {
    const id = await ticket('Flood');
    const stop = startWorker();
    try {
      const deadline = Date.now() + 10_000;
      while ((await orm.ticket.getOne(id)).status !== 'escalated' && Date.now() < deadline) await sleep(100);
    } finally {
      await stop();
    }
    expect((await orm.ticket.getOne(id)).status).toBe('escalated');
    expect((await deadlineRows(id)).respond).toMatchObject({ fired: true, outcome: 'moved' });
    expect(await liveMessages(id)).toEqual({});
  });

  test('a status that leaves the state cancels the deadline by until', async () => {
    waiting = await ticket('Drip');
    expect(await sdk(SAM).ticket.status.transition(waiting, { to: TicketStatusState.Waiting })).toEqual({ from: 'open', to: 'waiting' });
    const rows = await deadlineRows(waiting);
    expect(rows.respond!.dueAt).toBeNull();
    const entered = millis((await orm.ticket.getOne(waiting)).statusEnteredAt)!;
    expect(rows.idle!.dueAt).toBe(entered + 3_600_000);
    expect(await liveMessages(waiting)).toEqual({ idle: { at: entered + 3_600_000, dueAt: entered + 3_600_000 } });
  });

  test('a re-armed deadline does not fire on its old arming', async () => {
    // With the worker stopped: the idle message made due with SQL, and
    // claimed through the generated queue, as the worker claims it.
    expect((await pool.query(`UPDATE ticket_deadline_queue SET due_at = now() WHERE name = 'idle' AND state = 'ready' AND row_key = (SELECT id FROM ticket WHERE title = 'Drip')`)).rowCount).toBe(1);
    const [queue] = ticketTimersQueues(pool, orm, logger as never) as WorkerQueue[];
    const claimed = await queue!.claim(1, 60);
    expect(claimed).toHaveLength(1);
    const later = Date.now() + 7_200_000;
    expect(await sdk(KIM).ticket.timers.snoozeTimer(waiting, { deadline: TicketTimersDeadline.Idle, dueAt: later })).toEqual({ dueAt: later });
    logged.length = 0;
    await claimed[0]!.handle(new AbortController().signal);
    expect(logged).toEqual([{ message: 'deadline ran nothing', fields: { message: claimed[0]!.id, why: 'claim lost' } }]);
    expect((await orm.ticket.getOne(waiting)).status).toBe('waiting');
    expect((await deadlineRows(waiting)).idle!.dueAt).toBe(later);
    expect(await liveMessages(waiting)).toEqual({ idle: { at: later, dueAt: later } });
  });

  test("resetTimer and snoozeTimer are refused without the config's permission and run with it", async () => {
    expect(await refusal(sdk(SAM).ticket.timers.resetTimer(waiting, { deadline: TicketTimersDeadline.Idle }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `sam may not reset deadline idle of Ticket ${waiting}: it needs permission tickets.timers`,
    });
    expect(await refusal(sdk(SAM).ticket.timers.snoozeTimer(waiting, { deadline: TicketTimersDeadline.Idle, dueAt: Date.now() + 60_000 }))).toMatchObject({
      status: 403,
      code: 'forbidden',
    });
    const reset = await sdk(KIM).ticket.timers.resetTimer(waiting, { deadline: TicketTimersDeadline.Idle });
    expect(typeof reset.dueAt).toBe('number');
    expect((await deadlineRows(waiting)).idle!.dueAt).toBe(reset.dueAt);
    expect(await liveMessages(waiting)).toEqual({ idle: { at: reset.dueAt, dueAt: reset.dueAt } });
    const snoozed = Date.now() + 5_400_000;
    expect(await sdk(KIM).ticket.timers.snoozeTimer(waiting, { deadline: TicketTimersDeadline.Idle, dueAt: snoozed })).toEqual({ dueAt: snoozed });
    expect((await deadlineRows(waiting)).idle!.dueAt).toBe(snoozed);
    expect(await liveMessages(waiting)).toEqual({ idle: { at: snoozed, dueAt: snoozed } });
  });

  test('fireTimer through the SDK fires a due deadline and spends its message', async () => {
    const id = await ticket('Crack');
    await sleep(1600);
    expect(await sdk(SAM).ticket.timers.fireTimer(id, { deadline: TicketTimersDeadline.Respond })).toEqual({ fired: true, outcome: 'moved' });
    expect((await orm.ticket.getOne(id)).status).toBe('escalated');
    expect(await liveMessages(id)).toEqual({});
  });

  test('a raw SQL update of a Timers field start re-arms its deadline', async () => {
    const id = await visit('Boiler', Date.now() + 2 * DAY);
    const moved = Date.now() + 3 * DAY;
    expect((await pool.query(`UPDATE ticket SET visit_at = $2 WHERE title = $1`, ['Boiler', new Date(moved)])).rowCount).toBe(1);
    await workUntil(async () => (await deadlineRows(id)).remind?.dueAt === moved - DAY);
    expect((await deadlineRows(id)).remind).toMatchObject({ dueAt: moved - DAY, fired: false });
    expect(await remindMessages(id)).toEqual([moved - DAY]);
    expect((await liveMessages(id)).remind).toEqual({ at: moved - DAY, dueAt: moved - DAY });
  });

  test('a raw SQL delete of a ticket clears its deadline messages', async () => {
    await visit('Pipe', Date.now() + 2 * DAY);
    const { rows } = await pool.query(`SELECT id FROM ticket WHERE title = $1`, ['Pipe']);
    const key = rows[0].id as string;
    const messages = async () => Number((await pool.query(`SELECT count(*) AS n FROM ticket_deadline_queue WHERE row_key = $1`, [key])).rows[0].n);
    expect(await messages()).toBe(2);
    expect((await pool.query(`DELETE FROM ticket WHERE id = $1`, [key])).rowCount).toBe(1);
    await workUntil(async () => (await messages()) === 0);
    expect(await messages()).toBe(0);
  });

  test("a repository's bulk update of a field start re-arms its deadline", async () => {
    const bolt = await visit('Bolt', Date.now() + 2 * DAY);
    const nut = await visit('Nut', Date.now() + 2 * DAY);
    const moved = Date.now() + 4 * DAY;
    expect(await orm.ticket.updateMany({ title: { in: ['Bolt', 'Nut'] } }, { visitAt: new Date(moved) })).toBe(2);
    const rearmed = async (id: string) => (await deadlineRows(id)).remind?.dueAt === moved - DAY;
    await workUntil(async () => (await rearmed(bolt)) && (await rearmed(nut)));
    for (const id of [bolt, nut]) {
      expect((await deadlineRows(id)).remind).toMatchObject({ dueAt: moved - DAY, fired: false });
      expect(await remindMessages(id)).toEqual([moved - DAY]);
    }
  });

  test('deleting the row removes its deadlines', async () => {
    await orm.ticket.deleteOne(waiting);
    const { rows } = await pool.query(`SELECT count(*) AS n FROM ticket_timers_deadlines WHERE ticket_id NOT IN (SELECT id FROM ticket)`);
    expect(Number(rows[0].n)).toBe(0);
    const { rows: messages } = await pool.query(`SELECT count(*) AS n FROM ticket_deadline_queue WHERE row_key NOT IN (SELECT id FROM ticket)`);
    expect(Number(messages[0].n)).toBe(0);
    expect(await messageCount(waiting)).toBe(0);
  });
});

let ids = 0;
/** A JSON-RPC call of the MCP endpoint as the caller credential names: its message. */
async function mcp(as: string, method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: `Bearer ${as}` },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  const text = await response.text();
  expect(text.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(text.slice('event: message\ndata: '.length));
}

describe('the MCP endpoint of fixture-timers-api', () => {
  test('the MCP tool ticket_timers_fire_timer is listed with its schema, and calling it runs the operation', async () => {
    const { tools } = (await mcp(SAM, 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'ticket_timers_fire_timer');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['deadline', 'id']);
    expect(tool.inputSchema.properties.deadline.enum).toEqual(['idle', 'remind', 'respond']);
    const id = await ticket('Hum');
    const called = (await mcp(SAM, 'tools/call', { name: 'ticket_timers_fire_timer', arguments: { id, deadline: 'respond' } })).result;
    expect(called.isError).toBeFalsy();
    // respond is not due: the fire writes nothing.
    expect(called.structuredContent).toMatchObject({ fired: false });
    expect(await liveMessages(id)).toMatchObject({ respond: expect.any(Object) });
  });
});
