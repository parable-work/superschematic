/*
fixture-agents-api's leases, retries, presence and budgets end to end (D104),
run by TestGeneratedAgentsEndToEnd with API_DIR set to the materialized
package, SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-agents-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-agents-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
workers JobDeadlines and SessionPresence run in process, their queues as
the API's package generates them (jobDeadlinesQueues, sessionPresenceQueues)
under the runtime's runWorker.

A Job is leased with a fencing token, which every write under the lease
presents as Lease's precondition ({ preconditions: { Lease: { token } } }):
in the body of a behavior operation, and in the options of the
repository's updateOne. A timeout waits a backoff before the job runs
again, and then moves it from waiting back to queued; a lease the worker
stops renewing expires at its deadline; a build failure is retried once per
revision, its keyField. A Session is beaten by its device's principal and lives three
minutes past its last beat, fifteen while its agent is in a turn; a miss
moves it to missing and expires the leases its device holds on jobs. An
Account charges its meters, an hourly one and a rolling day's, and its
workspace's through a to-one relation, takes a provider's observed usage for
callers holding accounts.observe, and moves out when its usage passes a
cap. A deadline's fire is driven as the Timers end-to-end tests drive one:
its message made due with SQL, then the worker's queues until the row
moves, the command running at the later of the clock and the deadline's
time, so the core sees the lease lapsed and the backoff ended.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { uuidToDb } from '@superschematic/http-runtime/postgres';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-agents-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, jobDeadlinesQueues, sessionPresenceQueues } = await import(`${apiDir}/index.ts`);
const { FixtureAgentsApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

// The callers: a worker that leases jobs, another caller, a device that
// beats its sessions, and an observer holding accounts.observe.
const WORKER = subject();
const OTHER = subject();
const DEVICE = subject();
const OBSERVER = subject();

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

const schema = `agents_ts_${process.hrtime.bigint()}`;
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
  return new FixtureAgentsApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => as },
  });
}

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

/** The repositories, each create and update through the core run as subject holding permissions (withBehaviorCaller). */
const as = (who: string, ...permissions: string[]) => orm.withBehaviorCaller({ subject: who, permissions });

/** Lease's precondition, the fencing token a write under the lease presents. */
const token = (n: number) => ({ preconditions: { Lease: { token: n } } });

const millis = (value: unknown): number | null => (value === null || value === undefined ? null : value instanceof Date ? value.getTime() : typeof value === 'number' ? value : Date.parse(String(value)));

/** A row as its table holds it, read through the repository. */
async function job(id: string): Promise<Record<string, any>> {
  return (await orm.job.getOne(id as never)) as unknown as Record<string, any>;
}

let jobs = 0;
/** A job of its own, created through the repository: revision 1, queued. Its key. */
async function newJob(): Promise<string> {
  const row = (await as(WORKER).job.createOne({ title: `Job ${++jobs}`, revision: 1 } as never)) as unknown as Record<string, any>;
  expect(row).toMatchObject({ status: 'queued', leaseToken: 0, leaseExpiries: 0, retriesTotal: 0, retriesExhausted: false, retriesKeyedBy: '1' });
  return row.id as string;
}

/** A job the worker holds the lease of at token 1, moved to running. Its key and the lease's expiry. */
async function runningJob(): Promise<{ id: string; expiresAt: number }> {
  const id = await newJob();
  const lease = await sdk(WORKER).job.lease.acquire(id, {});
  expect(lease).toMatchObject({ token: 1 });
  expect(await sdk(WORKER).job.status.transition(id, { to: 'running', ...token(1) })).toMatchObject({ from: 'queued', to: 'running' });
  return { id, expiresAt: lease.expiresAt };
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

const jobWorker = () => jobDeadlinesQueues(pool, orm, logger as never) as WorkerQueue[];
const sessionWorker = () => sessionPresenceQueues(pool, orm, logger as never) as WorkerQueue[];

let ids = 0;
/** A JSON-RPC call of the MCP endpoint as the caller credential names: its message. */
async function mcp(credential: string, method: string, params?: unknown): Promise<Record<string, any>> {
  const response = await app.request('/api/mcp', {
    method: 'POST',
    headers: { accept: 'application/json, text/event-stream', 'content-type': 'application/json', authorization: `Bearer ${credential}` },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++ids, method, ...(params !== undefined ? { params } : {}) }),
  });
  expect(response.status).toBe(200);
  const text = await response.text();
  expect(text.startsWith('event: message\ndata: ')).toBe(true);
  return JSON.parse(text.slice('event: message\ndata: '.length));
}

describe("a job's lease and retries on fixture-agents-api", () => {
  test("a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease", async () => {
    const id = await newJob();
    const before = Date.now();
    const lease = await sdk(WORKER).job.lease.acquire(id, {});
    expect(lease).toMatchObject({ token: 1, heartbeatMs: 20000 });
    expect(lease.expiresAt).toBeGreaterThanOrEqual(before + 60000);
    expect(lease.expiresAt).toBeLessThanOrEqual(Date.now() + 60000);
    const beat = await sdk(WORKER).job.lease.heartbeat(id, token(1));
    expect(beat.directives).toEqual([]);
    expect(beat.expiresAt).toBeGreaterThanOrEqual(lease.expiresAt);
    expect(await job(id)).toMatchObject({ leaseHolder: WORKER, leaseToken: 1 });

    // The MCP tool takes the preconditions as any input member.
    const { tools } = (await mcp(WORKER, 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'job_lease_heartbeat');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties)).toContain('preconditions');
    const called = (await mcp(WORKER, 'tools/call', { name: 'job_lease_heartbeat', arguments: { id, ...token(1) } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toMatchObject({ directives: [] });
  });

  test("under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved", async () => {
    const id = await newJob();
    await sdk(WORKER).job.lease.acquire(id, {});
    const before = await job(id);
    expect(await refusal(sdk(WORKER).job.status.transition(id, { to: 'running' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'token_required' }),
    });
    expect((await job(id))._version).toBe(before._version);
    expect(await sdk(WORKER).job.status.transition(id, { to: 'running', ...token(1) })).toMatchObject({ from: 'queued', to: 'running' });
    const running = await job(id);
    expect(await refused(as(WORKER).job.updateOne(id as never, { title: 'Mine' } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'token_required' }),
    });
    expect((await job(id))._version).toBe(running._version);
    expect(await as(WORKER).job.updateOne(id as never, { title: 'Mine' } as never, token(1) as never)).toMatchObject({ title: 'Mine' });
  });

  test('a token a later lease replaced is refused as token_stale', async () => {
    const id = await newJob();
    await sdk(WORKER).job.lease.acquire(id, {});
    expect(await sdk(WORKER).job.lease.release(id, token(1))).toEqual({});
    expect(await sdk(OTHER).job.lease.acquire(id, {})).toMatchObject({ token: 3 });
    const before = await job(id);
    expect(await refusal(sdk(WORKER).job.lease.heartbeat(id, token(1)))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'token_stale', vetoDetails: { token: 1, current: 3 } }),
    });
    expect(await refused(as(WORKER).job.updateOne(id as never, { title: 'Late' } as never, token(1) as never))).toMatchObject({
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'token_stale', vetoDetails: { token: 1, current: 3 } }),
    });
    expect((await job(id))._version).toBe(before._version);
  });

  test("another caller's acquire of a job whose lease is active is refused as held_by_another", async () => {
    const id = await newJob();
    const lease = await sdk(OTHER).job.lease.acquire(id, {});
    const before = await job(id);
    expect(await refusal(sdk(WORKER).job.lease.acquire(id, {}))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'held_by_another', vetoDetails: { expiresAt: lease.expiresAt } }),
    });
    expect((await job(id))._version).toBe(before._version);
  });

  test('a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease\'s acquire are refused as backing_off', async () => {
    const { id } = await runningJob();
    const before = Date.now();
    const attempt = await sdk(WORKER).job.retries.recordAttempt(id, { failure: 'timeout', ...token(1) } as never);
    expect(attempt).toMatchObject({ failure: 'timeout', exhausted: false });
    expect(attempt.retryAt).toBeGreaterThanOrEqual(before + 60000);
    expect(attempt.retryAt).toBeLessThanOrEqual(Date.now() + 60000);
    // The release moves the job back to queued (onExpiry).
    expect(await sdk(WORKER).job.lease.release(id, token(1))).toEqual({});
    const released = await job(id);
    expect(released).toMatchObject({ status: 'queued', leaseHolder: null });
    expect(await refusal(sdk(OTHER).job.status.transition(id, { to: 'running' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'backing_off', vetoDetails: { retryAt: attempt.retryAt } }),
    });
    expect(await refusal(sdk(OTHER).job.lease.acquire(id, {}))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'backing_off', vetoDetails: { retryAt: attempt.retryAt } }),
    });
    expect((await job(id))._version).toBe(released._version);
  });

  test('when the backoff ends its then, run by the worker, moves the waiting job back to queued', async () => {
    const { id } = await runningJob();
    const attempt = await sdk(WORKER).job.retries.recordAttempt(id, { failure: 'timeout', ...token(1) } as never);
    expect(await sdk(WORKER).job.status.transition(id, { to: 'waiting', ...token(1) })).toMatchObject({ from: 'running', to: 'waiting' });
    expect(await sdk(WORKER).job.lease.release(id, token(1))).toEqual({});
    expect((await job(id)).status).toBe('waiting');
    await makeDue('job_deadline_queue', id, 'retry');
    await workUntil(jobWorker, async () => (await job(id)).status === 'queued');
    const moved = await job(id);
    expect(moved.status).toBe('queued');
    expect(millis(moved.retriesRetryAt)).toBe(attempt.retryAt);
  });

  test('a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances', async () => {
    const { id, expiresAt } = await runningJob();
    await makeDue('job_deadline_queue', id, 'expire');
    await workUntil(jobWorker, async () => (await job(id)).status === 'queued');
    expect(await job(id)).toMatchObject({ status: 'queued', leaseHolder: null, leaseToken: 2, leaseExpiries: 1, leaseEnded: { reason: 'ttl', at: expiresAt } });
  });

  test('a second build failure of one revision exhausts the job, and a new revision starts its counts again', async () => {
    const { id } = await runningJob();
    const first = await sdk(WORKER).job.retries.recordAttempt(id, { failure: 'build', ...token(1) } as never);
    expect(first).toMatchObject({ exhausted: false, classAttempts: { build: 1 } });
    const second = await sdk(WORKER).job.retries.recordAttempt(id, { failure: 'build', ...token(1) } as never);
    expect(second).toMatchObject({ exhausted: true });
    expect((await job(id)).status).toBe('failed');
    expect(await sdk(WORKER).job.lease.release(id, token(1))).toEqual({});
    const failed = await job(id);
    expect(await refusal(sdk(OTHER).job.status.transition(id, { to: 'queued' }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'exhausted' }),
    });
    expect((await job(id))._version).toBe(failed._version);
    expect(await as(OTHER).job.updateOne(id as never, { revision: 2 } as never)).toMatchObject({ revision: 2, retriesExhausted: false, retriesTotal: 0, retriesKeyedBy: '2' });
    expect(await sdk(OTHER).job.status.transition(id, { to: 'queued' })).toMatchObject({ from: 'failed', to: 'queued' });
  });

  test('a precondition for a use whose behavior takes none is refused as invalid_argument', async () => {
    const id = await newJob();
    const before = await job(id);
    expect(await refused(as(WORKER).job.updateOne(id as never, { title: 'Fenced' } as never, { preconditions: { Retries: { token: 1 } } } as never))).toEqual({
      status: 400,
      code: 'invalid_argument',
      class: 'PreconditionsError',
      message: 'preconditions of Job: /Retries: behavior Retries takes no precondition',
      details: { issues: [{ path: '/Retries', message: 'behavior Retries takes no precondition' }] },
    });
    // Over HTTP a token below 0 is the route's input refusal, 400, before
    // the core runs; the SDK refuses it before it sends it.
    const response = await app.request(`/api/job/${id}/lease/heartbeat`, {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${WORKER}` },
      body: JSON.stringify({ preconditions: { Lease: { token: -1 } } }),
    });
    expect(response.status).toBe(400);
    await expect(sdk(WORKER).job.lease.heartbeat(id, { preconditions: { Lease: { token: -1 } } })).rejects.toThrow('Validation failed');
    expect((await job(id))._version).toBe(before._version);
  });
});

/** A session's presence columns and its live deadline messages, by SQL: no repository reads the queue. */
async function presence(id: string): Promise<{ deadline: number | null; lastBeatAt: number | null; missed: boolean | null; released: unknown; status: string; enteredAt: number | null; version: number; messages: { name: string; at: number }[] }> {
  const { rows } = await pool.query(`SELECT presence_deadline, presence_last_beat_at, presence_missed, presence_released, status, status_entered_at, _version FROM "session" WHERE id = $1`, [uuidToDb(id)]);
  const row = rows[0];
  const { rows: messages } = await pool.query(`SELECT name, at FROM session_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed') ORDER BY name`, [uuidToDb(id)]);
  return {
    deadline: millis(row.presence_deadline),
    lastBeatAt: millis(row.presence_last_beat_at),
    missed: row.presence_missed,
    released: row.presence_released,
    status: row.status,
    enteredAt: millis(row.status_entered_at),
    version: Number(row._version),
    messages: messages.map(message => ({ name: String(message.name), at: Number(message.at) })),
  };
}

/** A session of its own a device opens, DEVICE unless another is named, through the repository. Its key. */
async function newSession(device = DEVICE): Promise<string> {
  const row = (await as(device).session.createOne({ device, agent: 'planner' } as never)) as unknown as Record<string, any>;
  expect(row).toMatchObject({ status: 'idle' });
  return row.id as string;
}

describe("a session's presence on fixture-agents-api", () => {
  let missed: { id: string; device: string };

  test("a session's create arms its miss ttlMs on: its deadline and its queue message", async () => {
    const before = Date.now();
    const id = await newSession();
    const after = Date.now();
    const row = await presence(id);
    expect(row.deadline!).toBeGreaterThanOrEqual(before + 180000);
    expect(row.deadline!).toBeLessThanOrEqual(after + 180000);
    expect(row.missed).toBe(false);
    expect(row.messages).toEqual([{ name: 'miss', at: row.deadline! }]);
  });

  test("only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on", async () => {
    const id = await newSession();
    const created = await presence(id);
    expect(await refusal(sdk(OTHER).session.presence.beat(id, {}))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'not_principal' }),
    });
    expect((await presence(id)).version).toBe(created.version);
    const before = Date.now();
    const beat = await sdk(DEVICE).session.presence.beat(id, {});
    expect(beat.deadline).toBeGreaterThanOrEqual(before + 180000);
    expect(beat.deadline).toBeLessThanOrEqual(Date.now() + 180000);
    const row = await presence(id);
    expect(row.deadline).toBe(beat.deadline);
    expect(row.lastBeatAt).not.toBeNull();
    expect(row.messages).toEqual([{ name: 'miss', at: beat.deadline }]);
  });

  test("a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)", async () => {
    const id = await newSession();
    expect(await sdk(DEVICE).session.status.transition(id, { to: 'turn' })).toMatchObject({ from: 'idle', to: 'turn' });
    const row = await presence(id);
    expect(row.status).toBe('turn');
    expect(row.deadline).toBe(row.enteredAt! + 900000);
    expect(row.messages).toEqual([{ name: 'miss', at: row.enteredAt! + 900000 }]);
  });

  test('a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)', async () => {
    // A device of its own: a miss spares the leases of a device present in
    // another of its sessions, as DEVICE is in the cases before.
    const device = subject();
    const id = await newSession(device);
    const jobId = await newJob();
    expect(await sdk(device).job.lease.acquire(jobId, {})).toMatchObject({ token: 1 });
    await sdk(device).session.presence.beat(id, {});
    await makeDue('session_deadline_queue', id, 'miss');
    await workUntil(sessionWorker, async () => (await presence(id)).status === 'missing');
    const row = await presence(id);
    expect(row.status).toBe('missing');
    expect(row.missed).toBe(true);
    expect(row.released).toEqual({ Job: [jobId] });
    expect(await job(jobId)).toMatchObject({ leaseHolder: null, leaseToken: 2 });
    missed = { id, device };
  });

  test('a beat recovers a missed session: missed clears and onBeat moves it back to idle', async () => {
    await sdk(missed.device).session.presence.beat(missed.id, {});
    expect(await presence(missed.id)).toMatchObject({ missed: false, status: 'idle' });
  });

  test("an update that changes a session's device is 409 principal_fixed", async () => {
    const id = await newSession();
    const before = await presence(id);
    expect(await refused(as(DEVICE).session.updateOne(id as never, { device: OTHER } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'principal_fixed' }),
    });
    expect((await presence(id)).version).toBe(before.version);
  });
});

/** The start of the UTC hour now is in, in epoch milliseconds. */
const hourStart = (at: number) => at - (at % 3_600_000);

/** A meter's row of an account's or a workspace's meters sub-table, by SQL: no repository reads it. */
async function meter(table: 'account' | 'workspace', id: string, name: string): Promise<{ used: number; periodStart: number | null } | undefined> {
  const { rows } = await pool.query(`SELECT used, period_start FROM ${table}_budget_meters WHERE ${table}_id = $1 AND meter = $2`, [uuidToDb(id), name]);
  const row = rows[0];
  return row === undefined ? undefined : { used: Number(row.used), periodStart: millis(row.period_start) };
}

/** An account of its own, in a workspace of its own, through the repositories. Its key and its workspace's. */
async function newAccount(): Promise<{ id: string; workspace: string }> {
  const workspace = ((await orm.workspace.createOne({ name: 'Acme' } as never)) as unknown as Record<string, any>).id as string;
  const account = (await orm.account.createOne({ name: 'Ops', workspaceId: workspace } as never)) as unknown as Record<string, any>;
  expect(account).toMatchObject({ status: 'open' });
  return { id: account.id as string, workspace };
}

describe("an account's budget on fixture-agents-api", () => {
  test("recordUsage charges the account's hourly meter and its workspace's through the to-one relation", async () => {
    const { id, workspace } = await newAccount();
    const before = Date.now();
    expect(await sdk(OTHER).account.budget.recordUsage(id, { meter: 'questions', amount: 1 } as never)).toEqual({ meter: 'questions', used: 1, released: 0, overruns: [], directed: false });
    expect(await meter('account', id, 'questions')).toEqual({ used: 1, periodStart: hourStart(before) });
    expect(await meter('workspace', workspace, 'questions')).toMatchObject({ used: 1 });
  });

  test("a reservation past the account's limit is 409 over_limit, and leaves the rows as they were", async () => {
    const { id } = await newAccount();
    await sdk(OTHER).account.budget.recordUsage(id, { meter: 'questions', amount: 1 } as never);
    const before = await meter('account', id, 'questions');
    expect(await refusal(sdk(OTHER).account.budget.reserve(id, { meter: 'questions', amount: 40 } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'over_limit', vetoDetails: { meter: 'questions', amount: 40, remaining: 39, limit: 40, scope: { schema: 'Account', id } } }),
    });
    expect(await meter('account', id, 'questions')).toEqual(before);
  });

  test('an hourly meter starts again at the next hour: its next write counts from zero', async () => {
    const { id } = await newAccount();
    await sdk(OTHER).account.budget.recordUsage(id, { meter: 'questions', amount: 3 } as never);
    // A raw SQL repair moves the period an hour back, as an hour passing would.
    expect((await pool.query(`UPDATE account_budget_meters SET period_start = period_start - interval '1 hour' WHERE account_id = $1 AND meter = 'questions'`, [uuidToDb(id)])).rowCount).toBe(1);
    const at = Date.now();
    expect(await sdk(OTHER).account.budget.recordUsage(id, { meter: 'questions', amount: 1 } as never)).toMatchObject({ meter: 'questions', used: 1 });
    expect(await meter('account', id, 'questions')).toEqual({ used: 1, periodStart: hourStart(at) });
  });

  test('a rolling meter counts the last day\'s buckets: usage that left the window no longer counts', async () => {
    const { id } = await newAccount();
    await sdk(OTHER).account.budget.recordUsage(id, { meter: 'tokens', amount: 900000 } as never);
    const { rows } = await pool.query(`SELECT starts_at, used FROM account_budget_buckets WHERE account_id = $1`, [uuidToDb(id)]);
    expect(rows).toHaveLength(1);
    expect(millis(rows[0].starts_at)! % (24 * 60_000)).toBe(0);
    expect(Number(rows[0].used)).toBe(900000);
    expect(await refusal(sdk(OTHER).account.budget.reserve(id, { meter: 'tokens', amount: 200000 } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'over_limit', vetoDetails: expect.objectContaining({ remaining: 100000 }) }),
    });
    expect((await pool.query(`UPDATE account_budget_buckets SET starts_at = starts_at - interval '2 days' WHERE account_id = $1`, [uuidToDb(id)])).rowCount).toBe(1);
    expect(await sdk(OTHER).account.budget.reserve(id, { meter: 'tokens', amount: 200000 } as never)).toEqual({ reserved: { tokens: 200000 } });
  });

  test('observe without observePermission is 403 forbidden, and with it the provider\'s figure replaces what the meter counts until resetsAt', async () => {
    const { id } = await newAccount();
    const resetsAt = Date.now() + 3_600_000;
    const observed = { meter: 'tokens', used: 990000, limit: 1000000, resetsAt };
    expect(await refusal(sdk(OTHER).account.budget.observe(id, observed as never))).toMatchObject({ status: 403, code: 'forbidden' });
    expect(await sdk(`${OBSERVER}:accounts.observe`).account.budget.observe(id, observed as never)).toEqual(observed);
    expect(await refusal(sdk(OTHER).account.budget.reserve(id, { meter: 'tokens', amount: 20000 } as never))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: expect.objectContaining({ vetoCode: 'over_limit', vetoDetails: expect.objectContaining({ remaining: 10000 }) }),
    });
    expect(await sdk(OTHER).account.budget.recordUsage(id, { meter: 'tokens', amount: 5000 } as never)).toMatchObject({ meter: 'tokens', used: 995000 });

    // The MCP tool, listed with its schema, observes as any operation runs.
    const { tools } = (await mcp(`${OBSERVER}:accounts.observe`, 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'account_budget_observe');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['id', 'limit', 'meter', 'resetsAt', 'used']);
    const called = (await mcp(`${OBSERVER}:accounts.observe`, 'tools/call', { name: 'account_budget_observe', arguments: { id, meter: 'tokens', used: 980000 } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toMatchObject({ meter: 'tokens', used: 980000 });
  });

  test('usage that takes the account over its limit moves it out (escalate), and counts all the same', async () => {
    const { id } = await newAccount();
    expect(await sdk(OTHER).account.budget.recordUsage(id, { meter: 'questions', amount: 41 } as never)).toMatchObject({
      meter: 'questions',
      used: 41,
      overruns: [{ schema: 'Account', id, used: 41, limit: 40, escalated: true }],
    });
    expect(((await orm.account.getOne(id as never)) as unknown as Record<string, any>).status).toBe('out');
    expect(await meter('account', id, 'questions')).toMatchObject({ used: 41 });
  });
});
