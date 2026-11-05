/*
fixture-seats-api's seats and reconciliation end to end (D104), run by
TestGeneratedSeatsEndToEnd with API_DIR set to the materialized package,
SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-seats-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-seats-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
worker SandboxDeadlines runs in process, its queues as the API's package
generates them (sandboxDeadlinesQueues) under the runtime's runWorker.

A Problem implements Seats: one accountable person, whom the next to take
the seat displaces; any number of responsible agents, the problem's own
agent among them through its field; and at most five handles, each released
by the agent that claimed it or by a caller holding problems.release. Only a
subject seated as responsible starts and submits a problem, only the
accountable one closes it or sends it back, a caller holding
problems.override moves past both, and in review only the accountable one
edits its title, which StateRules freezes. A Sandbox's status follows the
state its provider reports through its observed field, a Reconcile: each
observation moves it along the fewest transitions its Workflow allows, each
as the observer, so a stop needs sandboxes.stop of the observer, and one it
may not make records drift; five minutes without an observation, its stale
deadline moves it to unreachable. The deadline's fire is driven as the
Agents end-to-end tests drive one: its message made due with SQL, then the
worker's queues until the row moves, the command running at the later of
the clock and the deadline's time. The subjects are fresh UUIDs in their
base62 form, as the identity runtime's principals are.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { uuidToDb } from '@superschematic/http-runtime/postgres';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-seats-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, sandboxDeadlinesQueues } = await import(`${apiDir}/index.ts`);
const { FixtureSeatsApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

/** The callers, each a fresh subject holding the permissions D104's cases give it. */
const callers = {
  person: { subject: subject(), permissions: ['problems.own', 'problems.assign'] },
  person2: { subject: subject(), permissions: ['problems.own'] },
  agent: { subject: subject(), permissions: ['problems.work'] },
  agent2: { subject: subject(), permissions: ['problems.work'] },
  nobody: { subject: subject(), permissions: [] },
  releaser: { subject: subject(), permissions: ['problems.release'] },
  moderator: { subject: subject(), permissions: ['problems.override'] },
  poller: { subject: subject(), permissions: ['sandboxes.observe'] },
  operator: { subject: subject(), permissions: ['sandboxes.observe', 'sandboxes.stop'] },
} as const;
type Caller = keyof typeof callers;

const PERSON = callers.person.subject;
const PERSON2 = callers.person2.subject;
const AGENT = callers.agent.subject;
const AGENT2 = callers.agent2.subject;
const NOBODY = callers.nobody.subject;

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

const schema = `seats_ts_${process.hrtime.bigint()}`;
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

/** The SDK as the caller a credential names, its fetch handing each request to the app. */
function sdkWith(token: string) {
  return new FixtureSeatsApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => token },
  });
}

/** The SDK as one of the callers. */
const sdk = (who: Caller) => sdkWith(credential(who));

/** The problem a call through the SDK is refused with: its status, code, detail and details. */
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

/** The core's refusal a write through the repositories is refused with: its status, code, class, message and details. */
async function refused(write: Promise<unknown>): Promise<{ status: number; code: string; class: string; message: string; details?: any }> {
  try {
    await write;
  } catch (error) {
    const refusal = (error as { refusal?: { status: number; code: string; class: string; message: string; details?: unknown } }).refusal;
    if (refusal === undefined) throw error;
    return { status: refusal.status, code: refusal.code, class: refusal.class, message: refusal.message, details: refusal.details };
  }
  throw new Error('the write was not refused');
}

/** The repositories, each create, update and operation through the core run as a caller (withBehaviorCaller). */
const as = (who: Caller) => orm.withBehaviorCaller({ subject: callers[who].subject, permissions: [...callers[who].permissions] });

const millis = (value: unknown): number | null => (value === null || value === undefined ? null : value instanceof Date ? value.getTime() : typeof value === 'number' ? value : Date.parse(String(value)));

/** A problem as its table holds it, read through the repository. */
async function problem(id: string): Promise<Record<string, any>> {
  return (await orm.problem.getOne(id as never)) as unknown as Record<string, any>;
}

let problems = 0;
/** A problem of its own, created through the repository as the person, its agent the agent's subject unless another is named: open. Its key. */
async function newProblem(agent: string = AGENT): Promise<string> {
  const row = (await as('person').problem.createOne({ title: `Problem ${++problems}`, agent } as never)) as unknown as Record<string, any>;
  expect(row).toMatchObject({ status: 'open', agent });
  return row.id as string;
}

/** A problem's occupants, its Seats' sub-table rows, by SQL in their numbers' order: no repository reads them. */
async function occupants(id: string): Promise<{ seat: string; subject: string; seatedBy: string }[]> {
  const { rows } = await pool.query(`SELECT seat, subject, seated_by FROM problem_seats_occupants WHERE problem_id = $1 ORDER BY number`, [uuidToDb(id)]);
  return rows.map(row => ({ seat: String(row.seat), subject: String(row.subject), seatedBy: String(row.seated_by) }));
}

/** A sandbox as its table holds it, read through the repository. */
async function sandbox(id: string): Promise<Record<string, any>> {
  return (await orm.sandbox.getOne(id as never)) as unknown as Record<string, any>;
}

let sandboxes = 0;
/** A sandbox of its own, created through the repository: pending, and never observed. Its key. */
async function newSandbox(): Promise<string> {
  const row = (await orm.sandbox.createOne({ name: `Sandbox ${++sandboxes}` } as never)) as unknown as Record<string, any>;
  expect(row).toMatchObject({ status: 'pending', observed: null, observedObservedAt: null, observedDrift: null, observedReconciledAt: null });
  return row.id as string;
}

/** A sandbox's live deadline messages, by SQL: no repository reads the queue. */
async function deadlines(id: string): Promise<{ use: string; name: string; at: number; dueAt: number | null }[]> {
  const { rows } = await pool.query(`SELECT use_id, name, at, due_at FROM sandbox_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed') ORDER BY name`, [uuidToDb(id)]);
  return rows.map(row => ({ use: String(row.use_id), name: String(row.name), at: Number(row.at), dueAt: millis(row.due_at) }));
}

/** Makes a row's live deadline message named name, in the table's deadline queue, due now with SQL: the escape hatch an operator has. */
async function makeDue(queue: string, id: string, name: string): Promise<void> {
  const { rowCount } = await pool.query(`UPDATE ${queue} SET due_at = now() WHERE row_key = $1 AND name = $2 AND state = 'ready'`, [uuidToDb(id), name]);
  expect(rowCount).toBe(1);
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** Runs the queues build makes until done holds, asking every 100 ms for at most ten seconds, then stops them. */
async function workUntil(build: () => WorkerQueue[], done: () => Promise<boolean>): Promise<void> {
  const stop = new AbortController();
  const running = Promise.all(build().map(queue => runWorker(queue, { signal: stop.signal, concurrency: 2, idleMs: 100, graceMs: 2000 })));
  try {
    const deadline = Date.now() + 10_000;
    while (!(await done()) && Date.now() < deadline) await sleep(100);
  } finally {
    stop.abort();
    await running;
  }
}

const sandboxWorker = () => sandboxDeadlinesQueues(pool, orm, logger as never) as WorkerQueue[];

/** How long after an observation's at a sandbox goes stale (staleAfterMs). */
const STALE_AFTER_MS = 300_000;

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

describe("a problem's seats on fixture-seats-api", () => {
  test('a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)', async () => {
    const id = await newProblem();
    const before = Date.now();
    expect(await sdk('person').problem.seats.takeSeat(id, { seat: 'accountable' })).toEqual({ seat: 'accountable', subject: PERSON, displaced: [] });
    expect(await sdk('person2').problem.seats.takeSeat(id, { seat: 'accountable' })).toEqual({ seat: 'accountable', subject: PERSON2, displaced: [PERSON] });
    const listed = await sdk('person').problem.seats.listSeats(id, { seat: 'accountable' });
    expect(listed).toEqual({ items: [{ seat: 'accountable', subject: PERSON2, seatedAt: expect.any(Number), seatedBy: PERSON2, fromField: false }] });
    expect(listed.items[0].seatedAt).toBeGreaterThanOrEqual(before);
    expect(listed.items[0].seatedAt).toBeLessThanOrEqual(Date.now());
    expect(await occupants(id)).toEqual([{ seat: 'accountable', subject: PERSON2, seatedBy: PERSON2 }]);
  });

  test("a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was", async () => {
    const id = await newProblem();
    // The MCP tool, listed with its schema, takes a seat as any operation runs.
    const { tools } = (await mcp(credential('person'), 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'problem_seats_take_seat');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['id', 'seat', 'subject']);
    const called = (await mcp(credential('person'), 'tools/call', { name: 'problem_seats_take_seat', arguments: { id, seat: 'accountable' } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toEqual({ seat: 'accountable', subject: PERSON, displaced: [] });
    const before = await problem(id);
    expect(await sdk('person').problem.seats.takeSeat(id, { seat: 'accountable' })).toEqual({ seat: 'accountable', subject: PERSON, displaced: [], unchanged: true });
    expect((await problem(id))._version).toBe(before._version);
    expect(await occupants(id)).toEqual([{ seat: 'accountable', subject: PERSON, seatedBy: PERSON }]);
  });

  test("a take by a caller without the seat's take permission is 403 forbidden", async () => {
    const id = await newProblem();
    const before = await problem(id);
    expect(await refusal(sdk('agent').problem.seats.takeSeat(id, { seat: 'accountable' }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `${AGENT} may not take a seat in Problem ${id}'s accountable: it needs permission problems.own`,
    });
    expect((await problem(id))._version).toBe(before._version);
    expect(await occupants(id)).toEqual([]);
  });

  test("the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field", async () => {
    const id = await newProblem();
    expect(await sdk('agent2').problem.seats.takeSeat(id, { seat: 'responsible' })).toEqual({ seat: 'responsible', subject: AGENT2, displaced: [] });
    expect((await sdk('nobody').problem.seats.listSeats(id, { seat: 'responsible' })).items).toEqual([
      { seat: 'responsible', subject: AGENT, seatedAt: null, seatedBy: null, fromField: true },
      { seat: 'responsible', subject: AGENT2, seatedAt: expect.any(Number), seatedBy: AGENT2, fromField: false },
    ]);
    // The field's subject has no row of its own.
    expect(await occupants(id)).toEqual([{ seat: 'responsible', subject: AGENT2, seatedBy: AGENT2 }]);
    const before = await problem(id);
    expect(await refusal(sdk('agent').problem.seats.leaveSeat(id, { seat: 'responsible' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'held_by_field', vetoDetails: { seat: 'responsible', subject: AGENT, field: 'agent' } }),
    });
    expect((await problem(id))._version).toBe(before._version);
  });

  test('a sixth handle is 409 seat_full', async () => {
    const id = await newProblem();
    // Any caller claims a handle: the seat names no take.
    const takers = Array.from({ length: 6 }, () => subject());
    for (const taker of takers.slice(0, 5)) {
      expect(await sdkWith(taker).problem.seats.takeSeat(id, { seat: 'handles' })).toEqual({ seat: 'handles', subject: taker, displaced: [] });
    }
    const before = await problem(id);
    expect(await refusal(sdkWith(takers[5]!).problem.seats.takeSeat(id, { seat: 'handles' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'seat_full', vetoDetails: { seat: 'handles', max: 5, seated: takers.slice(0, 5) } }),
    });
    expect((await problem(id))._version).toBe(before._version);
    expect((await occupants(id)).map(row => row.subject)).toEqual(takers.slice(0, 5));
  });

  test("a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden", async () => {
    const id = await newProblem();
    expect(await sdk('agent').problem.seats.takeSeat(id, { seat: 'handles' })).toEqual({ seat: 'handles', subject: AGENT, displaced: [] });
    const before = await problem(id);
    expect(await refusal(sdk('nobody').problem.seats.leaveSeat(id, { seat: 'handles', subject: AGENT }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `${NOBODY} may not unseat ${AGENT} from Problem ${id}'s handles: unseating another needs permission problems.release`,
    });
    expect((await problem(id))._version).toBe(before._version);
    expect(await sdk('agent').problem.seats.leaveSeat(id, { seat: 'handles' })).toEqual({ seat: 'handles', subject: AGENT });
    expect(await sdk('agent2').problem.seats.takeSeat(id, { seat: 'handles' })).toEqual({ seat: 'handles', subject: AGENT2, displaced: [] });
    expect(await sdk('releaser').problem.seats.leaveSeat(id, { seat: 'handles', subject: AGENT2 })).toEqual({ seat: 'handles', subject: AGENT2 });
    expect(await occupants(id)).toEqual([]);
  });

  test('a leave of a seat the caller does not hold answers unchanged', async () => {
    const id = await newProblem();
    const before = await problem(id);
    expect(await sdk('nobody').problem.seats.leaveSeat(id, { seat: 'handles' })).toEqual({ seat: 'handles', subject: NOBODY, unchanged: true });
    expect((await problem(id))._version).toBe(before._version);
  });
});

describe("a problem's moves and edits by seat on fixture-seats-api", () => {
  test('a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat', async () => {
    const id = await newProblem();
    const before = await problem(id);
    expect(await refusal(sdk('nobody').problem.status.transition(id, { to: 'working' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({
        vetoCode: 'not_principal',
        reason: `${NOBODY} may not move Problem ${id}'s status from open to working: the transition is made by a subject seated in responsible`,
        vetoDetails: { from: 'open', to: 'working', by: ['seat:responsible'] },
      }),
    });
    expect((await problem(id))._version).toBe(before._version);
    // The agent is seated as responsible through the problem's agent field.
    expect(await sdk('agent').problem.status.transition(id, { to: 'working' })).toEqual({ from: 'open', to: 'working' });
    expect((await problem(id)).status).toBe('working');
  });

  test('a caller holding overridePermission moves past by', async () => {
    const id = await newProblem();
    expect(await sdk('moderator').problem.status.transition(id, { to: 'working' })).toEqual({ from: 'open', to: 'working' });
    expect((await problem(id)).status).toBe('working');
  });

  test("in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable", async () => {
    const id = await newProblem();
    expect(await sdk('agent').problem.status.transition(id, { to: 'working' })).toEqual({ from: 'open', to: 'working' });
    expect(await sdk('agent').problem.status.transition(id, { to: 'review' })).toEqual({ from: 'working', to: 'review' });
    expect(await sdk('person').problem.seats.takeSeat(id, { seat: 'accountable' })).toMatchObject({ seat: 'accountable', subject: PERSON });
    expect(await as('person').problem.updateOne(id as never, { title: 'Reviewed' } as never)).toMatchObject({ title: 'Reviewed', status: 'review' });
    const before = await problem(id);
    expect(await refused(as('agent2').problem.updateOne(id as never, { title: 'Mine' } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'frozen_fields', vetoDetails: { state: 'review', fields: ['title'], by: ['seat:accountable'] } }),
    });
    expect(await problem(id)).toMatchObject({ title: 'Reviewed', _version: before._version });
  });
});

describe("a sandbox's reconciliation on fixture-seats-api", () => {
  test('observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt', async () => {
    const id = await newSandbox();
    const at = Date.now();
    expect(await sdk('poller').sandbox.observed.observe(id, { state: 'running', at })).toEqual({
      observed: 'running',
      observedAt: at,
      status: 'running',
      moves: [
        { from: 'pending', to: 'starting' },
        { from: 'starting', to: 'running' },
      ],
    });
    const row = await sandbox(id);
    expect(row).toMatchObject({ status: 'running', observed: 'running', observedDrift: null });
    expect(millis(row.observedObservedAt)).toBe(at);
    expect(millis(row.observedReconciledAt)).toBeGreaterThanOrEqual(at);
    expect(await deadlines(id)).toEqual([{ use: 'observed', name: 'stale', at: at + STALE_AFTER_MS, dueAt: at + STALE_AFTER_MS }]);
  });

  test("an observation no newer than observedAt answers unchanged and leaves the row's _version where it was", async () => {
    const id = await newSandbox();
    const at = Date.now();
    // The MCP tool, listed with its schema, observes as any operation runs.
    const { tools } = (await mcp(credential('poller'), 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'sandbox_observed_observe');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['at', 'id', 'state']);
    const called = (await mcp(credential('poller'), 'tools/call', { name: 'sandbox_observed_observe', arguments: { id, state: 'running', at } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toMatchObject({ observed: 'running', observedAt: at, status: 'running' });
    const before = await sandbox(id);
    const unchanged = { observed: 'running', observedAt: at, status: 'running', moves: [], unchanged: true };
    expect(await sdk('poller').sandbox.observed.observe(id, { state: 'running', at })).toEqual(unchanged);
    expect(await sdk('poller').sandbox.observed.observe(id, { state: 'stopped', at: at - 1000 })).toEqual(unchanged);
    expect((await sandbox(id))._version).toBe(before._version);
  });

  test('a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift', async () => {
    const id = await newSandbox();
    const now = Date.now();
    await sdk('poller').sandbox.observed.observe(id, { state: 'running', at: now - 2000 });
    const drift = { status: 'running', observed: 'stopped', code: 'forbidden' };
    // running moves to stopped through stopping, and the move to stopping
    // needs sandboxes.stop, which the poller lacks.
    expect(await sdk('poller').sandbox.observed.observe(id, { state: 'stopped', at: now - 1000 })).toEqual({ observed: 'stopped', observedAt: now - 1000, status: 'running', moves: [], drift });
    expect(await sandbox(id)).toMatchObject({ status: 'running', observed: 'stopped', observedDrift: drift });
    expect(await sdk('operator').sandbox.observed.observe(id, { state: 'stopped', at: now })).toEqual({
      observed: 'stopped',
      observedAt: now,
      status: 'stopped',
      moves: [
        { from: 'running', to: 'stopping' },
        { from: 'stopping', to: 'stopped' },
      ],
    });
    expect(await sandbox(id)).toMatchObject({ status: 'stopped', observed: 'stopped', observedDrift: null });
  });

  test('observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument', async () => {
    const id = await newSandbox();
    const before = await sandbox(id);
    expect(await refusal(sdk('nobody').sandbox.observed.observe(id, { state: 'running', at: Date.now() }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `${NOBODY} may not observe Sandbox ${id}'s observed: observe needs permission sandboxes.observe`,
    });
    // The input types state by the Workflow's states, so the SDK refuses
    // hibernating before it sends it and the route before the core runs;
    // the repository hands it to the core, which refuses it in Reconcile's
    // words.
    await expect(sdk('poller').sandbox.observed.observe(id, { state: 'hibernating', at: Date.now() } as never)).rejects.toThrow('Validation failed');
    const response = await app.request(`/api/sandbox/${id}/observed/observe`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${credential('poller')}` },
      body: JSON.stringify({ state: 'hibernating', at: Date.now() }),
    });
    expect(response.status).toBe(400);
    expect(await response.json()).toMatchObject({ status: 400, code: 'bad_request', details: { location: 'body' } });
    expect(await refused(as('poller').sandbox.observed.observe(id as never, { state: 'hibernating', at: Date.now() } as never))).toMatchObject({
      status: 400,
      code: 'invalid_argument',
      class: 'OperationParamsError',
      message:
        'operation observe of behavior Reconcile on field observed: /state: "hibernating" is not a state of Sandbox.status (pending, starting, running, stopping, stopped, failed, unreachable)',
    });
    expect((await sandbox(id))._version).toBe(before._version);
  });

  test('the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable', async () => {
    const id = await newSandbox();
    const at = Date.now();
    await sdk('poller').sandbox.observed.observe(id, { state: 'running', at });
    await makeDue('sandbox_deadline_queue', id, 'stale');
    await workUntil(sandboxWorker, async () => (await sandbox(id)).status === 'unreachable');
    const row = await sandbox(id);
    expect(row).toMatchObject({ status: 'unreachable', observed: 'unreachable', observedDrift: null });
    expect(millis(row.observedObservedAt)).toBe(at);
    // The fire moved the row at the deadline's time, and arms no other.
    expect(millis(row.statusEnteredAt)).toBe(at + STALE_AFTER_MS);
    expect(await deadlines(id)).toEqual([]);
  });

  test('a newer observation moves the sandbox back from unreachable', async () => {
    const id = await newSandbox();
    const first = Date.now() - 1000;
    await sdk('poller').sandbox.observed.observe(id, { state: 'running', at: first });
    await makeDue('sandbox_deadline_queue', id, 'stale');
    await workUntil(sandboxWorker, async () => (await sandbox(id)).status === 'unreachable');
    const at = Date.now();
    expect(await sdk('poller').sandbox.observed.observe(id, { state: 'running', at })).toEqual({
      observed: 'running',
      observedAt: at,
      status: 'running',
      moves: [{ from: 'unreachable', to: 'running' }],
    });
    expect(await sandbox(id)).toMatchObject({ status: 'running', observed: 'running', observedDrift: null });
    expect(await deadlines(id)).toEqual([{ use: 'observed', name: 'stale', at: at + STALE_AFTER_MS, dueAt: at + STALE_AFTER_MS }]);
  });
});
