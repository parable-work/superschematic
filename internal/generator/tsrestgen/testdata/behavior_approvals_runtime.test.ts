/*
fixture-approvals-api's Constants, StateRules and Approvals end to end
(D104), run by TestGeneratedApprovalsEndToEnd with API_DIR set to the
materialized package, SDK_DIR to the API's generated TypeScript SDK,
CREATE_SQL to fixture-approvals-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-approvals-db (createOrm), in a schema of the test's own, with an
authenticator that reads `Authorization: Bearer <subject>[:<permission>,...]`
and establishes that subject holding those permissions. The subjects are
fresh UUIDs in their base62 form, as the identity runtime's principals are:
alice creates each task, through the repositories acting as her, which
stamp its createdBy; bob owns it (owner), who moves it; carol reviews it
(reviewer) and holds tasks.review, the gates' permission; dave holds
tasks.admin, Constants' permission.

The behavior operations, a transition and a sign-off and the list of
sign-offs, go through the SDK, whose fetch hands the request to the app,
under each use (sdk.task.status.transition, sdk.task.approvals.signOff).
The creates and updates go through the repositories, each update as the
behavior caller withBehaviorCaller names, since an authored route passes
its caller's subject alone to the core (D87) and Constants' permission is
the caller's to hold. The cases are D104's end-to-end cases, which the Go
and Rust servers run too, in one wording: a oneWay field moves only up
unless the caller holds tasks.admin; StateRules freezes the plan while the
task is planned except for its creator (by: ["createdBy"]); a move into a
gated state needs its sign-offs; the pinned gate on done takes a sign-off
only against the row's version; a change of the proof consumes the done
gate's sign-offs (resetOn: ["proof"]) and keeps the proving gate's
(resetOn: []); and the owner, a constant, never changes. The MCP endpoint
lists task_approvals_sign_off, its version optional.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { createOrm } from '@schemas/fixture-approvals-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { FixtureApprovalsApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

// The subjects, and the callers' credentials.
const ALICE = subject();
const BOB = subject();
const CAROL = subject();
const DAVE = subject();
const REVIEWER = `${CAROL}:tasks.review`;

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

const schema = `approvals_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  // reassignTask sets who reviews a task and who owns it; no case calls it.
  const implementations = {
    taskAdmin: {
      reassignTask: async (args: { id: string; input: { reviewer?: string; owner?: string } }) => orm.task.updateOne(args.id as never, args.input as never),
    },
  };
  app = new Hono();
  app.route('/', buildRouter(implementations, { authenticate, orm }));
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
  return new FixtureApprovalsApiSDK({
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

/** The core's refusal a write through the repositories is refused with: its status, code, message and details. */
async function refused(write: Promise<unknown>): Promise<{ status: number; code: string; message: string; details?: any }> {
  try {
    await write;
  } catch (error) {
    const refusal = (error as { refusal?: { status: number; code: string; message: string; details?: unknown } }).refusal;
    if (refusal === undefined) throw error;
    return { status: refusal.status, code: refusal.code, message: refusal.message, details: refusal.details };
  }
  throw new Error('the write was not refused');
}

/** The repositories, each update and create through the core run as subject holding permissions (withBehaviorCaller). */
const as = (who: string, ...permissions: string[]) => orm.withBehaviorCaller({ subject: who, permissions });

/** A task alice creates, through the repositories acting as her, which stamp its createdBy: bob owns it and carol reviews it. Its key. */
async function create(): Promise<string> {
  const task = await orm.withUser(ALICE).task.createOne({ title: 'Launch', owner: BOB, reviewer: CAROL, plan: 'First plan', urgent: false, attempts: 0 } as never);
  expect(task).toMatchObject({ status: 'planned', createdBy: ALICE, urgent: false, attempts: 0 });
  return task.id as string;
}

/** A task bob has moved to working and given a proof. Its key. */
async function working(): Promise<string> {
  const id = await create();
  expect(await sdk(BOB).task.status.transition(id, { to: 'working' })).toEqual({ from: 'planned', to: 'working' });
  expect(await as(BOB).task.updateOne(id as never, { proof: 'Screenshots' })).toMatchObject({ proof: 'Screenshots' });
  return id;
}

/** The task as its table holds it. */
async function stored(id: string): Promise<Record<string, any>> {
  return (await orm.task.getOne(id as never)) as unknown as Record<string, any>;
}

/** The veto message of the core for an action of a behavior on task id: its reason after the core's prefix. */
const vetoed = (behavior: string, action: string, id: string, reason: string) => `behavior ${behavior} vetoes ${action} of Task ${id}: ${reason}`;

describe('the freezes and sign-offs of a task of fixture-approvals-api', () => {
  test('a oneWay field moved back is refused as one_way, and moved forward is saved', async () => {
    const id = await create();
    expect(await as(BOB).task.updateOne(id as never, { urgent: true, attempts: 1 })).toMatchObject({ urgent: true, attempts: 1 });
    const before = await stored(id);
    expect(await refused(as(BOB).task.updateOne(id as never, { urgent: false }))).toEqual({
      status: 409,
      code: 'vetoed',
      message: vetoed('Constants', 'update', id, `Task ${id}'s urgent only turns true: an update may not move it from true to false; only a caller with tasks.admin may move it back`),
      details: expect.objectContaining({ vetoCode: 'one_way', vetoDetails: { field: 'urgent', direction: 'up', current: true, attempted: false } }),
    });
    expect(await stored(id)).toMatchObject({ urgent: true, attempts: 1, _version: before._version });
    expect(await as(BOB).task.updateOne(id as never, { attempts: 3 })).toMatchObject({ urgent: true, attempts: 3 });
    expect(await stored(id)).toMatchObject({ urgent: true, attempts: 3 });
  });

  test('a caller holding tasks.admin moves a oneWay field back', async () => {
    const id = await create();
    await as(BOB).task.updateOne(id as never, { urgent: true, attempts: 2 });
    expect(await refused(as(BOB).task.updateOne(id as never, { attempts: 0 }))).toMatchObject({
      status: 409,
      details: { vetoCode: 'one_way', vetoDetails: { field: 'attempts', direction: 'up', current: 2, attempted: 0 } },
    });
    expect(await as(DAVE, 'tasks.admin').task.updateOne(id as never, { urgent: false, attempts: 0 })).toMatchObject({ urgent: false, attempts: 0 });
    expect(await stored(id)).toMatchObject({ urgent: false, attempts: 0 });
  });

  test('the creator edits the plan while the task is planned', async () => {
    const id = await create();
    expect(await as(ALICE).task.updateOne(id as never, { plan: 'Second plan' })).toMatchObject({ status: 'planned', plan: 'Second plan' });
    expect(await stored(id)).toMatchObject({ plan: 'Second plan' });
  });

  test('another caller editing the plan while the task is planned is refused as frozen_fields', async () => {
    const id = await create();
    const before = await stored(id);
    expect(await refused(as(BOB).task.updateOne(id as never, { plan: "Bob's plan" }))).toEqual({
      status: 409,
      code: 'vetoed',
      message: vetoed('StateRules', 'update', id, `Task ${id} is planned, which freezes plan: it does not change while it is planned; only the subject createdBy holds may change it`),
      details: expect.objectContaining({ vetoCode: 'frozen_fields', vetoDetails: { state: 'planned', fields: ['plan'], by: ['createdBy'] } }),
    });
    expect(await stored(id)).toMatchObject({ plan: 'First plan', _version: before._version });
  });

  test('a move into proving without its sign-offs is refused as approvals_missing', async () => {
    const id = await working();
    const before = await stored(id);
    expect(await refusal(sdk(BOB).task.status.transition(id, { to: 'proving' }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: vetoed('Approvals', 'transition', id, `Task ${id} cannot move to proving with 0 of the 1 sign-offs it needs`),
      details: expect.objectContaining({ vetoCode: 'approvals_missing', vetoDetails: { state: 'proving', required: 1, count: 0 } }),
    });
    expect(await stored(id)).toMatchObject({ status: 'working', _version: before._version });
  });

  test("a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved", async () => {
    const id = await working();
    const version = (await stored(id))._version as number;
    expect(await refusal(sdk(REVIEWER).task.approvals.signOff(id, { state: 'done' }))).toMatchObject({
      status: 400,
      code: 'invalid_argument',
      detail: "operation signOff of behavior Approvals: /version: is required: the gate on done is pinned, so a sign-off names the row's version its signer read",
    });
    expect(await refusal(sdk(REVIEWER).task.approvals.signOff(id, { state: 'done', version: version - 1 }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: vetoed('Approvals', 'signOff', id, `Task ${id} is at version ${version}, not ${version - 1}`),
      details: expect.objectContaining({ vetoCode: 'stale', vetoDetails: { version: version - 1, current: version } }),
    });
    expect((await stored(id))._version).toBe(version);
    expect(await sdk(REVIEWER).task.approvals.signOff(id, { state: 'done', version })).toMatchObject({ state: 'done', subject: CAROL, signedBy: [CAROL], count: 1, at: expect.any(Number) });
    // A sign-off writes the row, so its version moves on.
    expect((await stored(id))._version).toBe(version + 1);
  });

  test("editing the proof consumes the done gate's sign-offs and keeps the proving gate's", async () => {
    const id = await working();
    expect(await sdk(REVIEWER).task.approvals.signOff(id, { state: 'proving' })).toMatchObject({ state: 'proving', signedBy: [CAROL], count: 1 });
    const version = (await stored(id))._version as number;
    expect(await sdk(REVIEWER).task.approvals.signOff(id, { state: 'done', version })).toMatchObject({ state: 'done', signedBy: [CAROL], count: 1 });
    expect(await as(BOB).task.updateOne(id as never, { proof: 'New screenshots' })).toMatchObject({ proof: 'New screenshots' });
    const { items, next } = await sdk(REVIEWER).task.approvals.listSignOffs(id, {});
    expect(next ?? null).toBeNull();
    expect(items).toHaveLength(2);
    expect(items[0]).toMatchObject({ state: 'proving', subject: CAROL, at: expect.any(Number) });
    expect(items[0].consumedAt ?? null).toBeNull();
    expect(items[1]).toMatchObject({ state: 'done', subject: CAROL, at: expect.any(Number), consumedAt: expect.any(Number) });
    expect(await sdk(BOB).task.status.transition(id, { to: 'proving' })).toEqual({ from: 'working', to: 'proving' });
    expect(await stored(id)).toMatchObject({ status: 'proving' });
  });

  test('an update of the owner, a constant, is refused as invalid_instance', async () => {
    const id = await create();
    const before = await stored(id);
    expect(await refused(as(BOB).task.updateOne(id as never, { owner: DAVE }))).toMatchObject({
      status: 422,
      code: 'invalid_instance',
      details: {
        issues: [{ path: 'owner', rule: 'constant', message: 'owner is a constant of Task: its create sets it and nothing changes it after; only a caller with tasks.admin may change it' }],
      },
    });
    expect(await stored(id)).toMatchObject({ owner: BOB, _version: before._version });
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

describe('the MCP endpoint of fixture-approvals-api', () => {
  test('the MCP tool task_approvals_sign_off is listed with its schema, its version optional', async () => {
    const { tools } = (await mcp(REVIEWER, 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'task_approvals_sign_off');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['id', 'note', 'state', 'version']);
    expect([...tool.inputSchema.required].sort()).toEqual(['id', 'state']);
    expect(tool.inputSchema.properties.state.enum).toEqual(['done', 'proving']);
    // The gate on proving is not pinned: a sign-off without version signs.
    const id = await working();
    const called = (await mcp(REVIEWER, 'tools/call', { name: 'task_approvals_sign_off', arguments: { id, state: 'proving' } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toMatchObject({ state: 'proving', subject: CAROL, signedBy: [CAROL], count: 1 });
  });
});
