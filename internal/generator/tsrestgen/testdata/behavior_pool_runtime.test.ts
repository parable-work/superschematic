/*
fixture-pool-api's pool of accounts end to end (D104), run by
TestGeneratedPoolEndToEnd with API_DIR set to the materialized package,
SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-pool-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-pool-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
worker AccountRechecks runs in process, its queues as the API's package
generates them (accountRechecksQueues: Account's deadlines and events)
under the runtime's runWorker, with a Workers of the test's own.

An Account implements Pool, ranked by its rank, out for thirty minutes when
a provider names no time and rechecked every two minutes past its outUntil,
markOut and reportRoom needing accounts.pool; and Budget, its tokens
metered by the hour, a thousand of them, observe needing accounts.observe.
pick is an operation of the table as a whole (D104), a command without a
row at account/pool/pick: each case keeps to accounts of an owner of its
own, a fresh UUID, which pick's match names. Each recheck is an event of
the table's event queue, which the worker hands to the project's Workers
method accountRechecksPoolRecheck. The test's method records each event it
gets as its key and dueAt, and reports nothing for a pair it recorded
already (a handler is idempotent by them); otherwise it asks a fake
provider for the account, room unless the test set another answer, and
reports its check through the event's transaction as the worker,
reportRoom, recording what the report answered. A recheck's fire is driven
as the Schedules end-to-end tests drive one: its message made due with
SQL, then the worker's queues until the row shows the check, the fire
running at the later of the clock and the deadline's time; the check the
handler reports runs at the clock, so a member the handler may open is one
whose outUntil is already past. The subjects and owners are fresh UUIDs in
their base62 form, as the identity runtime's principals are.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { uuidFromDb, uuidToDb } from '@superschematic/http-runtime/postgres';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-pool-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, accountRechecksQueues } = await import(`${apiDir}/index.ts`);
const { FixturePoolApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

/** The callers: ada holding accounts.pool and accounts.observe, and guest holding none. */
const callers = {
  ada: { subject: subject(), permissions: ['accounts.pool', 'accounts.observe'] },
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

/** The payload of a recheck event, as Pool's declaration types it. */
interface RecheckPayload {
  readonly outUntil: number;
  readonly outReason?: string;
  readonly checkedAt?: number;
  readonly dueAt: number;
  readonly firedAt: number;
}

/** What the fake provider answers of an account: room, near its limit, and when its room is back. */
interface Answer {
  readonly room: boolean;
  readonly near?: boolean;
  readonly resetsAt?: number;
}

/** An event the test's handler got: the account's key in its base62 form, the recheck's dueAt, the event whole, and what its report answered. */
interface Handled {
  readonly key: string;
  readonly dueAt: number;
  readonly payload: RecheckPayload;
  report?: Record<string, any>;
}

/** Every event the handler got and reported, in the order it got them. */
const handled: Handled[] = [];
/** The fake provider's answer for an account, by its base62 key; room when none. */
const provider = new Map<string, Answer>();

/**
 * The project's Workers, as the test writes them: the handler of the event
 * recheck of Account's Pool, which passes over a pair of key and dueAt it
 * handled already, asks the fake provider for the account, and reports
 * its check through the event's transaction, as the worker.
 */
const workers = {
  async accountRechecksPoolRecheck(event: { key: string; at: number; payload: RecheckPayload }, tx: any): Promise<void> {
    const key = uuidFromDb(event.key);
    const payload = event.payload;
    if (handled.some(seen => seen.key === key && seen.dueAt === payload.dueAt)) return;
    const seen: Handled = { key, dueAt: payload.dueAt, payload };
    handled.push(seen);
    seen.report = await tx.account.pool.reportRoom(event.key, provider.get(key) ?? { room: true });
  },
};

const schema = `pool_ts_${process.hrtime.bigint()}`;
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
  return new FixturePoolApiSDK({
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

/** An account's version, by SQL, as the contract's check reads it. */
async function version(id: string): Promise<number> {
  const { rows } = await pool.query(`SELECT _version FROM account WHERE id = $1`, [uuidToDb(id)]);
  return Number(rows[0]!._version);
}

/** An account's Pool columns, by SQL. */
async function member(id: string): Promise<{ availability: string | null; outUntil: number | null; outReason: string | null; checkedAt: number | null }> {
  const { rows } = await pool.query(`SELECT pool_availability, pool_out_until, pool_out_reason, pool_checked_at FROM account WHERE id = $1`, [uuidToDb(id)]);
  const row = rows[0]!;
  const millis = (value: unknown) => (value === null ? null : (value as Date).getTime());
  return { availability: row.pool_availability, outUntil: millis(row.pool_out_until), outReason: row.pool_out_reason, checkedAt: millis(row.pool_checked_at) };
}

/** An owner's accounts, each created through the repository, with the key its spec names when it names one: open. Their keys, by name. */
async function accounts(owner: string, ...specs: [name: string, vendor: string, kind: 'account' | 'key', rank: number, key?: string][]): Promise<Record<string, string>> {
  const out: Record<string, string> = {};
  for (const [name, vendor, kind, rank, key] of specs) {
    const row = (await orm.account.createOne({ ...(key === undefined ? {} : { id: key }), owner, name, vendor, kind, rank } as never)) as unknown as Record<string, any>;
    expect(row).toMatchObject({ poolAvailability: 'open' });
    out[name] = uuidFromDb(row.id);
  }
  return out;
}

/** An account's live deadline messages, by SQL in their names' order: no repository reads the queue. */
async function deadlines(id: string): Promise<{ use: string; name: string; at: number; dueAt: number; payload: unknown }[]> {
  const { rows } = await pool.query(`SELECT use_id, name, at, due_at, payload FROM account_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed') ORDER BY name`, [uuidToDb(id)]);
  return rows.map(row => ({ use: String(row.use_id), name: String(row.name), at: Number(row.at), dueAt: (row.due_at as Date).getTime(), payload: row.payload }));
}

/** Makes an account's live recheck message due now with SQL: the escape hatch an operator has. */
async function makeDue(id: string): Promise<void> {
  const { rowCount } = await pool.query(`UPDATE account_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = 'recheck' AND state IN ('ready', 'claimed')`, [uuidToDb(id)]);
  expect(rowCount).toBe(1);
}

/** The events the handler got of an account. */
const handledOf = (id: string): Handled[] => handled.filter(event => event.key === id);

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

const worker = () => accountRechecksQueues(pool, orm, workers, logger as never) as WorkerQueue[];

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
const OUT_FOR = 30 * MINUTE;

/** Puts an account out now, its outUntil the command's time, so its recheck is due at once and a check made after it may open it. Its outUntil. */
async function outNow(id: string, reason = 'rate limited'): Promise<number> {
  const out = await sdk('ada').account.pool.markOut(id, { until: Date.now(), reason });
  expect(out).toMatchObject({ availability: 'out', outReason: reason });
  return out.outUntil;
}

describe("an owner's pool of accounts on fixture-pool-api", () => {
  test("a pick answers the open account lowest by rank among an owner's accounts", async () => {
    const owner = subject();
    // a1 takes the greatest of three keys and k1 the least, so a pick that
    // went by key rather than by rank would answer k1.
    const [least, middle, greatest] = [subject(), subject(), subject()].sort((a, b) => (uuidToDb(a) < uuidToDb(b) ? -1 : 1));
    const { a1 } = await accounts(owner, ['a1', 'anthropic', 'account', 1, greatest], ['a2', 'anthropic', 'account', 2, middle], ['k1', 'anthropic', 'key', 9, least]);
    expect(a1).toBe(greatest);
    expect(await sdk('ada').account.pool.pick({ match: { owner } })).toEqual({ member: { key: a1, availability: 'open', order: 1, preferred: true } });
  });

  test("a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank", async () => {
    const owner = subject();
    const { a1 } = await accounts(owner, ['a1', 'anthropic', 'account', 1], ['o1', 'openai', 'account', 2]);
    // The body as any HTTP client may send it: the SDK's types take no null.
    const response = await app.request('/api/account/pool/pick', {
      method: 'POST',
      headers: { 'content-type': 'application/json', authorization: `Bearer ${credential('ada')}` },
      body: JSON.stringify({ match: { owner, rank: null }, prefer: { vendor: null } }),
    });
    expect(response.status).toBe(200);
    expect(((await response.json()) as { data: unknown }).data).toEqual({ member: { key: a1, availability: 'open', order: 1, preferred: true } });
  });

  test("prefer tries a vendor's open accounts, then its near ones, then the others by rank", async () => {
    const owner = subject();
    const { a1, a2, o1 } = await accounts(owner, ['a1', 'anthropic', 'account', 1], ['a2', 'anthropic', 'account', 2], ['o1', 'openai', 'account', 3]);
    expect(await sdk('ada').account.pool.reportRoom(a2!, { room: true, near: true })).toEqual({ availability: 'near', checkedAt: expect.any(Number) });
    const prefer = { vendor: 'anthropic', kind: 'account' };
    expect(await sdk('ada').account.pool.pick({ match: { owner }, prefer })).toEqual({ member: { key: a1, availability: 'open', order: 1, preferred: true } });
    expect(await sdk('ada').account.pool.pick({ match: { owner }, prefer, exclude: [a1!] })).toEqual({ member: { key: a2, availability: 'near', order: 2, preferred: true } });
    expect(await sdk('ada').account.pool.pick({ match: { owner }, prefer, exclude: [a1!, a2!] })).toEqual({ member: { key: o1, availability: 'open', order: 3, preferred: false } });
  });

  test('markOut puts an account out for thirty minutes and arms its recheck at outUntil', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const before = Date.now();
    const out = await sdk('ada').account.pool.markOut(a1!, { reason: 'rate limited' });
    expect(out).toEqual({ availability: 'out', outUntil: expect.any(Number), outReason: 'rate limited' });
    expect(out.outUntil).toBeGreaterThanOrEqual(before + OUT_FOR);
    expect(out.outUntil).toBeLessThanOrEqual(Date.now() + OUT_FOR);
    expect(await member(a1!)).toEqual({ availability: 'out', outUntil: out.outUntil, outReason: 'rate limited', checkedAt: null });
    expect(await deadlines(a1!)).toEqual([{ use: 'Pool', name: 'recheck', at: out.outUntil, dueAt: out.outUntil, payload: { dueAt: out.outUntil } }]);
  });

  test('markOut keeps the later until, and a repeat answers unchanged', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const t = Date.now();
    expect(await sdk('ada').account.pool.markOut(a1!, { until: t + 3_600_000, reason: 'quota' })).toEqual({ availability: 'out', outUntil: t + 3_600_000, outReason: 'quota' });
    const at = await version(a1!);
    expect(await sdk('ada').account.pool.markOut(a1!, { until: t + 600_000, reason: 'quota' })).toEqual({ availability: 'out', outUntil: t + 3_600_000, outReason: 'quota', unchanged: true });
    expect(await version(a1!)).toBe(at);
    expect(await deadlines(a1!)).toEqual([{ use: 'Pool', name: 'recheck', at: t + 3_600_000, dueAt: t + 3_600_000, payload: { dueAt: t + 3_600_000 } }]);
  });

  test('a check with room before outUntil leaves the account out', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const t = Date.now();
    await sdk('ada').account.pool.markOut(a1!, { until: t + 3_600_000, reason: 'quota' });
    const before = Date.now();
    const checked = await sdk('ada').account.pool.reportRoom(a1!, { room: true });
    expect(checked).toEqual({ availability: 'out', checkedAt: expect.any(Number), outUntil: t + 3_600_000, outReason: 'quota' });
    expect(checked.checkedAt).toBeGreaterThanOrEqual(before);
    expect(await member(a1!)).toEqual({ availability: 'out', outUntil: t + 3_600_000, outReason: 'quota', checkedAt: checked.checkedAt });
  });

  test('with every account out, pick answers no member and the soonest outUntil', async () => {
    const owner = subject();
    const { a1, a2 } = await accounts(owner, ['a1', 'anthropic', 'account', 1], ['a2', 'openai', 'account', 2]);
    const t = Date.now();
    await sdk('ada').account.pool.markOut(a1!, { until: t + 600_000, reason: 'rate limited' });
    await sdk('ada').account.pool.markOut(a2!, { until: t + 1_200_000, reason: 'quota' });
    expect(await sdk('ada').account.pool.pick({ match: { owner } })).toEqual({ member: null, until: t + 600_000 });
  });

  test('a due recheck hands the handler its event once, whose check with room opens the account', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const outUntil = await outNow(a1!);
    expect(await deadlines(a1!)).toEqual([{ use: 'Pool', name: 'recheck', at: outUntil, dueAt: outUntil, payload: { dueAt: outUntil } }]);
    await makeDue(a1!);
    await workUntil(async () => (await member(a1!)).availability === 'open');
    const events = handledOf(a1!);
    expect(events).toHaveLength(1);
    const [event] = events as [Handled];
    expect(event.dueAt).toBe(outUntil);
    expect(event.payload).toEqual({ outUntil, outReason: 'rate limited', dueAt: outUntil, firedAt: expect.any(Number) });
    expect(event.payload.firedAt).toBeGreaterThanOrEqual(outUntil);
    expect(event.report).toEqual({ availability: 'open', checkedAt: expect.any(Number), opened: true });
    expect(await member(a1!)).toEqual({ availability: 'open', outUntil: null, outReason: null, checkedAt: event.report!.checkedAt });
    // The open cleared the recheck the fire armed again.
    expect(await deadlines(a1!)).toEqual([]);
    await workFor(1000);
    expect(handledOf(a1!)).toHaveLength(1);
  });

  test('a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const resetsAt = Date.now() + 3_600_000;
    provider.set(a1!, { room: false, resetsAt });
    await outNow(a1!);
    await makeDue(a1!);
    await workUntil(async () => (await member(a1!)).outUntil === resetsAt);
    const [event] = handledOf(a1!) as [Handled];
    expect(event.report).toEqual({ availability: 'out', checkedAt: expect.any(Number), outUntil: resetsAt, outReason: 'rate limited' });
    expect(await member(a1!)).toEqual({ availability: 'out', outUntil: resetsAt, outReason: 'rate limited', checkedAt: event.report!.checkedAt });
    expect(await deadlines(a1!)).toEqual([{ use: 'Pool', name: 'recheck', at: resetsAt, dueAt: resetsAt, payload: { dueAt: resetsAt } }]);
  });

  test("an account whose Budget meter has no room counts as out until the provider's figure lapses", async () => {
    const owner = subject();
    const { a1, a2 } = await accounts(owner, ['a1', 'anthropic', 'account', 1], ['a2', 'anthropic', 'account', 2]);
    const resetsAt = Date.now() + 2000;
    expect(await sdk('ada').account.budget.observe(a1!, { meter: 'tokens', used: 1000, resetsAt })).toMatchObject({ meter: 'tokens', used: 1000, limit: 1000, resetsAt });
    // a1 is open by its own state, and out by its Budget's Capacity.
    expect((await member(a1!)).availability).toBe('open');
    expect(await sdk('ada').account.pool.pick({ match: { owner } })).toEqual({ member: { key: a2, availability: 'open', order: 2, preferred: true } });
    await sdk('ada').account.pool.markOut(a2!, { reason: 'rate limited' });
    expect(await sdk('ada').account.pool.pick({ match: { owner } })).toEqual({ member: null, until: resetsAt });
    await sleep(Math.max(0, resetsAt - Date.now()) + 100);
    expect(await sdk('ada').account.pool.pick({ match: { owner } })).toEqual({ member: { key: a1, availability: 'open', order: 1, preferred: true } });
  });

  test('markOut and reportRoom without accounts.pool are forbidden', async () => {
    const { a1 } = await accounts(subject(), ['a1', 'anthropic', 'account', 1]);
    const at = await version(a1!);
    expect(await refusal(sdk('guest').account.pool.markOut(a1!, { reason: 'rate limited' }))).toEqual({
      status: 403,
      code: 'forbidden',
      detail: `${GUEST} may not mark Account ${a1} out: it needs permission accounts.pool`,
      details: undefined,
    });
    expect(await refusal(sdk('guest').account.pool.reportRoom(a1!, { room: true }))).toEqual({
      status: 403,
      code: 'forbidden',
      detail: `${GUEST} may not report a check of Account ${a1}: it needs permission accounts.pool`,
      details: undefined,
    });
    expect(await version(a1!)).toBe(at);
    expect(await member(a1!)).toEqual({ availability: 'open', outUntil: null, outReason: null, checkedAt: null });
  });

  test('reportRoom of a key no row holds is not found', async () => {
    const missing = subject();
    expect(await refusal(sdk('ada').account.pool.reportRoom(missing, { room: true }))).toEqual({
      status: 404,
      code: 'not_found',
      detail: `Account ${missing} does not exist`,
      details: undefined,
    });
  });

  test('the MCP tool account_pool_pick lists and runs', async () => {
    const owner = subject();
    const { a1 } = await accounts(owner, ['a1', 'anthropic', 'account', 1]);
    const { tools } = (await mcp(credential('guest'), 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'account_pool_pick');
    expect(tool).toBeDefined();
    // An operation of the table as a whole takes no id.
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['exclude', 'match', 'prefer']);
    const called = (await mcp(credential('guest'), 'tools/call', { name: 'account_pool_pick', arguments: { match: { owner } } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toEqual({ member: { key: a1, availability: 'open', order: 1, preferred: true } });
  });
});
