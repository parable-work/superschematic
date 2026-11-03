/*
fixture-workflow-api's behavior operations end to end (D103), run by
TestGeneratedBehaviorOperationsEndToEnd with API_DIR set to the
materialized package, SDK_DIR to the API's generated TypeScript SDK,
CREATE_SQL to fixture-workflow-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-workflow-db (createOrm), in a schema of the test's own, with an
authenticator that reads `Authorization: Bearer <subject>[:<permission>,...]`
and establishes that subject holding those permissions. Each call goes
through the SDK, whose fetch hands the request to the app, under each
field (sdk.order.status.transition): a transition moves the field and
answers its result; a move the config has no transition for, a transition
whose permission the caller lacks and a key no row holds are refused with
the core's status, code and message; a caller without the route's
permission is refused by the API's auth before the core runs; a table's
two Workflows move apart; and the MCP endpoint lists the operation's tool
and runs a call of it through its route.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { OrderStatusState, ShipmentPaymentStatusState, ShipmentStatusState } from '@schemas/fixture-workflow-api-types';
import { createOrm } from '@schemas/fixture-workflow-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { FixtureWorkflowApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

// The callers: sam may write orders, kim may write and ship them, lee holds
// no permission.
const SAM = 'sam:orders.write';
const KIM = 'kim:orders.write,orders.ship';
const LEE = 'lee';

/**
 * Establishes the subject and the permissions the bearer credential names,
 * `<subject>[:<permission>,...]`; no credential, no caller. It reads the
 * header itself, since hono's bearer parser takes no colon.
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

const schema = `behavior_ops_ts_${process.hrtime.bigint()}`;
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
  // getOrder answers the row as its table holds it, the fields its status
  // Workflow owns among them.
  const implementations = {
    order: {
      getOrder: async (args: { id: string }) => orm.order.getOne(args.id as never),
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

/** The SDK as the caller the credential names, or none, its fetch handing each request to the app. */
function sdk(as?: string) {
  return new FixtureWorkflowApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    ...(as !== undefined ? { auth: { getToken: async () => as } } : {}),
  });
}

/** The problem a call is refused with: its status, code, detail and details. */
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

describe('the behavior operations of fixture-workflow-api', () => {
  let order: string;
  let shipment: string;

  beforeAll(async () => {
    order = (await orm.order.createOne({ title: 'o' })).id;
    shipment = (await orm.shipment.createOne({ reference: 'S-1' })).id;
  });

  test('a transition moves the field and returns the result', async () => {
    expect(await sdk(SAM).order.status.transition(order, { to: OrderStatusState.Paid })).toEqual({ from: 'draft', to: 'paid' });
    expect(await orm.order.getOne(order)).toMatchObject({ status: 'paid', _version: 2 });
  });

  test("a move the config has no transition for is refused with the core's status, code and message", async () => {
    expect(await refusal(sdk(SAM).order.status.transition(order, { to: OrderStatusState.Draft }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      detail: `behavior Workflow vetoes transition of Order ${order}: no transition leads from paid to draft; from paid it can move to shipped`,
      details: { vetoCode: 'transition_not_allowed' },
    });
    expect(await orm.order.getOne(order)).toMatchObject({ status: 'paid', _version: 2 });
  });

  test("a caller without the route's permission is refused by the API's auth", async () => {
    const lee = await refusal(sdk(LEE).order.status.transition(order, { to: OrderStatusState.Shipped }));
    expect(lee.status).toBe(403);
    expect(lee.detail).not.toContain('may not move');
    expect((await refusal(sdk().order.status.transition(order, { to: OrderStatusState.Shipped }))).status).toBe(401);
    expect(await orm.order.getOne(order)).toMatchObject({ status: 'paid', _version: 2 });
  });

  test('a caller without the permission the config names is refused by the core, and one who holds it moves the row', async () => {
    expect(await refusal(sdk(SAM).order.status.transition(order, { to: OrderStatusState.Shipped }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `sam may not move Order ${order}'s status from paid to shipped: the transition needs permission orders.ship`,
    });
    expect(await sdk(KIM).order.status.transition(order, { to: OrderStatusState.Shipped })).toEqual({ from: 'paid', to: 'shipped' });
    // The output type carries the field the Workflow owns.
    expect(await sdk(KIM).order.getOrder(order)).toMatchObject({ id: order, status: 'shipped' });
  });

  test('a key no row holds is not found', async () => {
    const key = parseIdentityUUID(crypto.randomUUID())!;
    expect(await refusal(sdk(SAM).order.status.transition(key, { to: OrderStatusState.Paid }))).toMatchObject({
      status: 404,
      code: 'not_found',
      detail: `Order ${key} does not exist`,
    });
  });

  test("a table's two Workflows move apart", async () => {
    expect(await sdk(LEE).shipment.status.transition(shipment, { to: ShipmentStatusState.Packed })).toEqual({ from: 'pending', to: 'packed' });
    expect(await sdk(LEE).shipment.paymentStatus.transition(shipment, { to: ShipmentPaymentStatusState.Paid })).toEqual({ from: 'unpaid', to: 'paid' });
    expect(await orm.shipment.getOne(shipment)).toMatchObject({ status: 'packed', paymentStatus: 'paid' });
    expect(await refusal(sdk(LEE).shipment.paymentStatus.transition(shipment, { to: ShipmentPaymentStatusState.Refunded }))).toMatchObject({
      status: 403,
      code: 'forbidden',
      detail: `lee may not move Shipment ${shipment}'s paymentStatus from paid to refunded: the transition needs permission payments.refund`,
    });
    expect(await orm.shipment.getOne(shipment)).toMatchObject({ status: 'packed', paymentStatus: 'paid' });
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

describe('the MCP endpoint of fixture-workflow-api', () => {
  test('the MCP tool is listed with its schema, and calling it runs the operation', async () => {
    const { tools } = (await mcp(KIM, 'tools/list')).result;
    const tool = tools.find((t: { name: string }) => t.name === 'order_status_transition');
    expect(tool).toBeDefined();
    expect(Object.keys(tool.inputSchema.properties).sort()).toEqual(['id', 'to']);
    expect(tool.inputSchema.properties.to.enum).toEqual(['draft', 'paid', 'shipped']);
    expect([...tool.inputSchema.required].sort()).toEqual(['id', 'to']);
    expect(tool.annotations).toEqual({ readOnlyHint: false });

    const order = (await orm.order.createOne({ title: 'by tool' })).id;
    const called = (await mcp(KIM, 'tools/call', { name: 'order_status_transition', arguments: { id: order, to: 'paid' } })).result;
    expect(called.isError).toBeFalsy();
    expect(called.structuredContent).toEqual({ from: 'draft', to: 'paid' });
    expect(await orm.order.getOne(order)).toMatchObject({ status: 'paid', _version: 2 });
  });
});
