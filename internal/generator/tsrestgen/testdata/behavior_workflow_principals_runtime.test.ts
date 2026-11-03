/*
fixture-workflow-principals-api's Workflows end to end (D104), run by
TestGeneratedWorkflowPrincipalsEndToEnd with API_DIR set to the
materialized package, SDK_DIR to the API's generated TypeScript SDK,
CREATE_SQL to fixture-workflow-principals-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-workflow-principals-db (createOrm), in a schema of the test's own,
with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]` and establishes that
subject holding those permissions. The subjects are fresh UUIDs in their
base62 form, as the identity runtime's principals are: alice asks each
question, through the repositories acting as her, which stamp its
createdBy; bob is the one it is asked of (askedOf); carol is anyone else;
mod holds questions.moderate, the status Workflow's overridePermission.

Each call goes through the SDK, whose fetch hands the request to the app,
under each field (sdk.question.status.transition). The cases are D104's
end-to-end cases, which the Go and Rust servers run too, in one wording: a
transition's by lets the subject a field holds make it and refuses anyone
else as not_principal; createdBy is a field by may name; overridePermission
moves a caller past by; repeat: "unchanged" answers a move to the state the
row is in as no change and writes nothing, while the review Workflow, which
has no repeat, refuses it as already_in_state; and a from that is not the
row's state is stale, so a retry that finds its move made is stale with the
state it moved to.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { QuestionReviewState, QuestionStatusState } from '@schemas/fixture-workflow-principals-api-types';
import { createOrm } from '@schemas/fixture-workflow-principals-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { FixtureWorkflowPrincipalsApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

/** A fresh subject: a UUID in its base62 wire form. */
const subject = (): string => parseIdentityUUID(crypto.randomUUID())!;

// The subjects, and the callers' credentials.
const ALICE = subject();
const BOB = subject();
const CAROL = subject();
const MOD = subject();
const MODERATOR = `${MOD}:questions.moderate`;

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

const schema = `workflow_principals_ts_${process.hrtime.bigint()}`;
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
  // reassignQuestion sets who a question is asked of; no case calls it.
  const implementations = {
    questionAdmin: {
      reassignQuestion: async (args: { id: string; input: { askedOf: string } }) => orm.question.updateOne(args.id as never, { askedOf: args.input.askedOf } as never),
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
  return new FixtureWorkflowPrincipalsApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => as },
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

/** A question alice asks bob, through the repositories acting as her, which stamp its createdBy: its key. */
async function ask(): Promise<string> {
  const question = await orm.withUser(ALICE).question.createOne({ prompt: 'Which table?', askedOf: BOB } as never);
  expect(question).toMatchObject({ status: 'open', review: 'pending' });
  return question.id as string;
}

/** The question as its table holds it. */
async function stored(q: string): Promise<Record<string, unknown>> {
  return (await orm.question.getOne(q as never)) as unknown as Record<string, unknown>;
}

/** The veto message of the core for a move of question q: its reason after the core's prefix. */
const vetoed = (q: string, reason: string) => `behavior Workflow vetoes transition of Question ${q}: ${reason}`;

describe('who may move a question of fixture-workflow-principals-api', () => {
  test('the subject the question is asked of answers it', async () => {
    const q = await ask();
    const moved = await sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered });
    expect(moved).toEqual({ from: 'open', to: 'answered' });
    expect(moved.unchanged).toBeUndefined();
    expect(await stored(q)).toMatchObject({ status: 'answered' });
  });

  test('another caller is refused as not_principal', async () => {
    const q = await ask();
    const before = await stored(q);
    expect(await refusal(sdk(CAROL).question.status.transition(q, { to: QuestionStatusState.Answered }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: vetoed(q, `${CAROL} may not move Question ${q}'s status from open to answered: the transition is made by the subject askedOf holds`),
      details: expect.objectContaining({ vetoCode: 'not_principal', vetoDetails: { from: 'open', to: 'answered', by: ['askedOf'] } }),
    });
    expect(await stored(q)).toMatchObject({ status: 'open', _version: before._version });
  });

  test("the question's creator withdraws it, by createdBy", async () => {
    const q = await ask();
    expect(await refusal(sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Withdrawn }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: { vetoCode: 'not_principal', vetoDetails: { from: 'open', to: 'withdrawn', by: ['createdBy'] } },
    });
    expect(await sdk(ALICE).question.status.transition(q, { to: QuestionStatusState.Withdrawn })).toEqual({ from: 'open', to: 'withdrawn' });
    expect(await stored(q)).toMatchObject({ status: 'withdrawn' });
  });

  test('a caller holding the override permission answers it', async () => {
    const q = await ask();
    expect(await sdk(MODERATOR).question.status.transition(q, { to: QuestionStatusState.Answered })).toEqual({ from: 'open', to: 'answered' });
    expect(await stored(q)).toMatchObject({ status: 'answered' });
  });

  test("answering again answers unchanged and leaves the row's version", async () => {
    const q = await ask();
    expect(await sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered })).toEqual({ from: 'open', to: 'answered' });
    const answered = await stored(q);
    const again = await sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered });
    expect(again).toEqual({ from: 'answered', to: 'answered', unchanged: true });
    // Nothing was written: the version and the time the status was entered
    // are where the first answer left them.
    expect(await stored(q)).toMatchObject({ status: 'answered', _version: answered._version, statusEnteredAt: answered.statusEnteredAt });
  });

  test('a repeat on the Workflow without repeat is refused as already_in_state', async () => {
    const q = await ask();
    const before = await stored(q);
    expect(await refusal(sdk(BOB).question.review.transition(q, { to: QuestionReviewState.Pending }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: vetoed(q, `Question ${q}'s review is already pending`),
      details: expect.objectContaining({ vetoCode: 'already_in_state', vetoDetails: { from: 'pending', to: 'pending' } }),
    });
    expect(await stored(q)).toMatchObject({ review: 'pending', _version: before._version });
  });

  test("a from that is not the row's state is refused as stale", async () => {
    const q = await ask();
    expect(await refusal(sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered, from: QuestionStatusState.Answered }))).toEqual({
      status: 409,
      code: 'vetoed',
      detail: vetoed(q, `Question ${q}'s status is open, not answered`),
      details: expect.objectContaining({ vetoCode: 'stale', vetoDetails: { from: 'answered', status: 'open' } }),
    });
    expect(await stored(q)).toMatchObject({ status: 'open' });
  });

  test("a from that is the row's state moves it, and its retry is stale with the state it moved to", async () => {
    const q = await ask();
    expect(await sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered, from: QuestionStatusState.Open })).toEqual({ from: 'open', to: 'answered' });
    // The retry landed: the row is in the state it asked for.
    expect(await refusal(sdk(BOB).question.status.transition(q, { to: QuestionStatusState.Answered, from: QuestionStatusState.Open }))).toMatchObject({
      status: 409,
      code: 'vetoed',
      details: { vetoCode: 'stale', vetoDetails: { from: 'open', status: 'answered' } },
    });
    expect(await stored(q)).toMatchObject({ status: 'answered' });
  });
});
