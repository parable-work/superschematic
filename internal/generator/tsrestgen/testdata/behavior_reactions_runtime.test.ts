/*
fixture-reactions-api's Reactions end to end (D103), run by
TestGeneratedReactionsEndToEnd with API_DIR set to the materialized package,
SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-reactions-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated router
serves the API over the generated repositories of fixture-reactions-db
(createOrm), in a schema of the test's own, with an authenticator that reads
`Authorization: Bearer <subject>[:<permission>,...]`; and the behavior
workers ProjectReactions and TaskReactions run in process, their queues as
the API's package generates them (projectReactionsQueues,
taskReactionsQueues, each its table's change queue) under the runtime's
runWorker. A project is active once one of its tasks starts (Task's enters
rule, which moves the project task.project holds), done once every task is
done with success (Project's allTerminal over task.project), and failed once
one fails (anyTerminal); a task whose priority becomes urgent starts (a
changes rule on an own field). Each rule hears a write of a row whoever
makes it, the SDK's or raw SQL's, through the table's change queue, after
the write commits; a then whose target cannot move leaves it as it is and
completes its message. Each case runs the workers until its condition
holds, asking every 100 ms for at most ten seconds.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { runWorker, type WorkerQueue } from '@superschematic/http-runtime/worker';
import { createOrm } from '@schemas/fixture-reactions-db-types/orm';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, projectReactionsQueues, taskReactionsQueues } = await import(`${apiDir}/index.ts`);
const { FixtureReactionsApiSDK } = await import(`${sdkDir}/index.ts`);

// The caller: sam may write projects and tasks.
const SAM = 'sam:projects.write,tasks.write';

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

/** What the workers' queues logged: each line's message and fields. */
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

const schema = `behavior_reactions_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 8 });
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
  return new FixtureReactionsApiSDK({
    baseUrl: 'http://api.example.com',
    fetch: (input: RequestInfo | URL, init?: RequestInit) => app.request(String(input), init),
    auth: { getToken: async () => as },
  });
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** Runs both workers' generated queues until done holds, asking every 100 ms for at most ten seconds, then stops them. */
async function workUntil(done: () => Promise<boolean>): Promise<void> {
  const stop = new AbortController();
  const queues: WorkerQueue[] = [...projectReactionsQueues(pool, orm, logger as never), ...taskReactionsQueues(pool, orm, logger as never)];
  const running = Promise.all(queues.map(queue => runWorker(queue, { signal: stop.signal, concurrency: 2, idleMs: 100, graceMs: 2000 })));
  try {
    const deadline = Date.now() + 10_000;
    while (!(await done()) && Date.now() < deadline) await sleep(100);
  } finally {
    stop.abort();
    await running;
  }
}

/** Whether no message of the change queues waits or is held: every write so far has been heard. */
async function drained(): Promise<boolean> {
  const { rows } = await pool.query(
    `SELECT (SELECT count(*) FROM project_change_queue WHERE state IN ('ready', 'claimed')) + (SELECT count(*) FROM task_change_queue WHERE state IN ('ready', 'claimed')) AS n`
  );
  return Number(rows[0].n) === 0;
}

/** A project's status as its repository reads it. */
const projectStatus = async (id: string): Promise<string> => (await orm.project.getOne(id)).status;

/** A project through the repository, planned, or moved to active through its own repository. */
async function project(name: string, active = false): Promise<string> {
  const id = (await orm.project.createOne({ name })).id;
  if (active) await orm.project.status.transition(id, { to: 'active' });
  return id;
}

/** A task of a project through the repository, todo. */
async function task(title: string, projectId: string): Promise<string> {
  return (await orm.task.createOne({ title, projectId })).id;
}

/** A task's key as its column holds it, which raw SQL binds. */
async function column(title: string): Promise<string> {
  const { rows } = await pool.query(`SELECT id FROM task WHERE title = $1`, [title]);
  return rows[0].id;
}

describe('Reactions on fixture-reactions-api', () => {
  test('a task that starts moves its project to active', async () => {
    const p = await project('Launch');
    const t = await task('Draft', p);
    expect(await projectStatus(p)).toBe('planned');
    expect(await sdk(SAM).task.status.transition(t, { to: 'doing' })).toEqual({ from: 'todo', to: 'doing' });
    await workUntil(async () => (await projectStatus(p)) === 'active');
    expect(await projectStatus(p)).toBe('active');
  });

  test('a project whose tasks are all done moves to done', async () => {
    const p = await project('Ship', true);
    const first = await task('Build', p);
    const second = await task('Test', p);
    for (const t of [first, second]) await orm.task.status.transition(t, { to: 'doing' });
    await sdk(SAM).task.status.transition(first, { to: 'done' });
    // Every write so far heard: one task done, the other doing.
    await workUntil(drained);
    expect(await projectStatus(p)).toBe('active');
    await sdk(SAM).task.status.transition(second, { to: 'done' });
    await workUntil(async () => (await projectStatus(p)) === 'done');
    expect(await projectStatus(p)).toBe('done');
  });

  test('a task that is done, reopened and done again closes its project each time, and starting it again takes the project up', async () => {
    // Done is a state a transition leaves, which finished names (D113): it
    // counts as finished, so the project closes though each task may be
    // reopened.
    const p = await project('Refit', true);
    const first = await task('Strip', p);
    const second = await task('Coat', p);
    for (const t of [first, second]) {
      await orm.task.status.transition(t, { to: 'doing' });
      await orm.task.status.transition(t, { to: 'done' });
    }
    await workUntil(async () => (await projectStatus(p)) === 'done');
    expect(await projectStatus(p)).toBe('done');
    // A reopened task moves nothing by itself: no rule hears a task that
    // leaves done, and the project stays done with a task to do.
    expect(await sdk(SAM).task.status.transition(second, { to: 'todo' })).toEqual({ from: 'done', to: 'todo' });
    await workUntil(drained);
    expect(await projectStatus(p)).toBe('done');
    // The task's own rule takes the project up when the task starts again,
    // along the transition the project lists from done.
    await sdk(SAM).task.status.transition(second, { to: 'doing' });
    await workUntil(async () => (await projectStatus(p)) === 'active');
    expect(await projectStatus(p)).toBe('active');
    // Done again, every task is done again, and the project closes again.
    await sdk(SAM).task.status.transition(second, { to: 'done' });
    await workUntil(async () => (await projectStatus(p)) === 'done');
    expect(await projectStatus(p)).toBe('done');
    expect((await orm.task.getOne(first)).status).toBe('done');
  });

  test('a task that fails moves its project to failed', async () => {
    const p = await project('Dig', true);
    const t = await task('Drill', p);
    await orm.task.status.transition(t, { to: 'doing' });
    await sdk(SAM).task.status.transition(t, { to: 'failed' });
    await workUntil(async () => (await projectStatus(p)) === 'failed');
    expect(await projectStatus(p)).toBe('failed');
  });

  test("a raw SQL write of a task's status is heard and moves its project", async () => {
    const p = await project('Paint');
    await task('Prime', p);
    expect((await pool.query(`UPDATE task SET status = 'doing', status_entered_at = now() WHERE id = $1`, [await column('Prime')])).rowCount).toBe(1);
    await workUntil(async () => (await projectStatus(p)) === 'active');
    expect(await projectStatus(p)).toBe('active');
  });

  test("a raw SQL write of a task's priority to urgent starts it, which moves its project", async () => {
    const p = await project('Rush');
    const t = await task('Hurry', p);
    await workUntil(drained);
    expect((await pool.query(`UPDATE task SET priority = 'urgent' WHERE id = $1`, [await column('Hurry')])).rowCount).toBe(1);
    await workUntil(async () => (await projectStatus(p)) === 'active');
    // The changes rule starts the task, whose start, heard, moves its project.
    expect((await orm.task.getOne(t)).status).toBe('doing');
    expect(await projectStatus(p)).toBe('active');
  });

  test('a then whose target has no transition to its state leaves the target as it is and completes the message', async () => {
    const p = await project('Plant', true);
    const t = await task('Seed', p);
    await orm.task.status.transition(t, { to: 'doing' });
    await orm.task.status.transition(t, { to: 'done' });
    await workUntil(drained);
    logged.length = 0;
    const key = await column('Seed');
    expect((await pool.query(`UPDATE task SET priority = 'urgent' WHERE id = $1`, [key])).rowCount).toBe(1);
    const messages = async () => Number((await pool.query(`SELECT count(*) AS n FROM task_change_queue WHERE row_key = $1`, [key])).rows[0].n);
    expect(await messages()).toBe(1);
    await workUntil(async () => (await messages()) === 0);
    // No transition leads from done to doing: the task stays done, and its
    // message completed rather than failed, none ready, claimed or dead.
    expect((await orm.task.getOne(t)).status).toBe('done');
    expect(await messages()).toBe(0);
    expect(logged).toEqual([]);
  });
});
