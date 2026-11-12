/*
fixture-workflow-view-api's create end to end (D109), run by
TestGeneratedStartStateCreateEndToEnd with API_DIR set to the materialized
package, CREATE_SQL to fixture-workflow-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-workflow-db (createOrm) and the behaviors' core, in a schema of the
test's own, with an authenticator that reads `Authorization: Bearer
<subject>` and establishes that subject. Each call is a request to the app.

OrderViews.placeOrder is a @tableOperation whose body holds status, the
field of Order's Workflow (draft to paid, paid to shipped): a create that
names the state its row starts in. The router creates the order in the
Workflow's initial state and makes the Workflow's own transition to the
state the body names, in the create's transaction and as its caller, so a
move the Workflow refuses keeps no row.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { createOrm } from '@schemas/fixture-workflow-db-types/orm';

const apiDir = process.env.API_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !createSQL || !dsn) throw new Error('API_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, tableOperations } = await import(`${apiDir}/router.ts`);

/** Establishes the subject the bearer credential names; no credential, no caller. */
const authenticate = Object.assign(
  async (ctx: RequestContext): Promise<Principal | null> => {
    const header = ctx.headers.get('authorization');
    if (!header?.startsWith('Bearer ')) return null;
    return { subject: header.slice('Bearer '.length), permissions: [] };
  },
  { readsAuthorization: true }
);

const schema = `start_state_ts_${process.hrtime.bigint()}`;
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
  // The one operation the implementation writes.
  const implementations = {
    orderViews: {
      getOrderView: async (args: { id: string }) => orm.order.getOne(args.id as never),
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

/** Places an order as sam, or as no one. */
async function place(body: unknown, as: string | null = 'sam'): Promise<{ status: number; body: any }> {
  const response = await app.request('/api/orders', {
    method: 'POST',
    headers: { 'content-type': 'application/json', ...(as !== null ? { authorization: `Bearer ${as}` } : {}) },
    body: JSON.stringify(body),
  });
  return { status: response.status, body: await response.json() };
}

/** The orders of a title, as their table holds them. */
const titled = async (title: string) => (await orm.order.findMany({ title: { eq: title } }, { limit: 10 })).rows;

describe('a create that names the state its row starts in', () => {
  test("a create that names no state leaves the row in the Workflow's initial state", async () => {
    expect(tableOperations.placeOrder).toEqual({ kind: 'create', type: 'Order', repository: 'order', view: ['id', 'title', 'status', 'statusEnteredAt'], body: ['title', 'status'], starts: ['status'] });
    for (const body of [{ title: 'Plain' }, { title: 'Plain', status: null }, { title: 'Plain', status: 'draft' }]) {
      const answer = await place(body);
      expect(answer.status).toBe(200);
      expect(answer.body.data).toMatchObject({ id: expect.any(String), title: 'Plain', status: 'draft' });
    }
    // Each is a create and nothing more: the first version of its row.
    const rows = await titled('Plain');
    expect(rows.map(row => [row.status, row._version])).toEqual([
      ['draft', 1],
      ['draft', 1],
      ['draft', 1],
    ]);
  });

  test("a create that names a state moves the row there by the Workflow's transition, in the create's transaction", async () => {
    const answer = await place({ title: 'Prepaid', status: 'paid' });
    expect(answer.status).toBe(200);
    expect(answer.body.data).toMatchObject({ title: 'Prepaid', status: 'paid', statusEnteredAt: expect.any(String) });
    expect(Object.keys(answer.body.data)).toEqual(['id', 'title', 'status', 'statusEnteredAt']);
    // The row is the one the answer shows: created, then moved, its second version.
    const [row] = await titled('Prepaid');
    expect(row).toMatchObject({ id: answer.body.data.id, status: 'paid', _version: 2 });
    // And it moves on from there as any paid order does.
    expect((await orm.order.status.transition(row!.id, { to: 'shipped' }, { caller: { subject: 'kim', permissions: ['orders.ship'] } })).to).toBe('shipped');
  });

  test("a state the initial one does not lead to is the transition's refusal, and no row is kept", async () => {
    const answer = await place({ title: 'Too far', status: 'shipped' });
    expect(answer.status).toBe(409);
    expect(answer.body).toMatchObject({
      status: 409,
      code: 'vetoed',
      detail: expect.stringContaining('no transition leads from draft to shipped; from draft it can move to paid'),
      details: { vetoCode: 'transition_not_allowed' },
    });
    expect(await titled('Too far')).toEqual([]);
  });

  test('a state the Workflow does not have is refused, and no row is kept', async () => {
    const answer = await place({ title: 'Nowhere', status: 'lost' });
    expect(answer.status).toBeGreaterThanOrEqual(400);
    expect(answer.status).toBeLessThan(500);
    expect(JSON.stringify(answer.body)).toContain('lost');
    expect(await titled('Nowhere')).toEqual([]);
  });

  test("the route's guard holds", async () => {
    expect((await place({ title: 'Anonymous', status: 'paid' }, null)).status).toBe(401);
    expect(await titled('Anonymous')).toEqual([]);
  });
});
