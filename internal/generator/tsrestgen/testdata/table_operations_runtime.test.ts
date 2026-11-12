/*
fixture-tracker-api's table operations end to end (D106), run by
TestGeneratedTableOperationsEndToEnd with API_DIR set to the materialized
package, CREATE_SQL to fixture-tracker-db's Postgres create.sql and
SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to the database. The generated
router serves the API over the generated repositories of
fixture-tracker-db (createOrm), in a schema of the test's own, with an
authenticator that reads `Authorization: Bearer <subject>[:<permission>,...]`
and establishes that subject holding those permissions. Each call is a
request to the app.

Every operation but Teams.summarizeTeam is marked @tableOperation, so the
router serves it with the runtime's handler of its kind and the
implementation has summarizeTeam alone. The cases are D106's parity
cases, which the Go server runs too: each kind; a row under its parents;
the table's defaults and audit stamps; an update's absent, null and empty
bodies; a unique key and a foreign key refused; a soft and a hard delete;
a list's pages, sorts, search and filters; the routes' guards; the
hand-written operation beside them; and a row rule (D108), which keeps
each caller to the rows that name them.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { createOrm } from '@schemas/fixture-tracker-db-types/orm';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_RULES_TEST_DATABASE_URL;
if (!apiDir || !createSQL || !dsn) throw new Error('API_DIR, CREATE_SQL and SUPERSCHEMATIC_RULES_TEST_DATABASE_URL are required');
const { buildRouter, tableOperations } = await import(`${apiDir}/router.ts`);

/** A UUID's wire form, which a row and a view hold. */
const uuid = (n: number) => parseIdentityUUID(`00000000-0000-4000-8000-${n.toString(16).padStart(12, '0')}`)!;

// The callers, each a user's id: sam may do everything, kim may read and
// write issues, lee holds no permission.
const SAM_ID = uuid(0xa1);
const KIM_ID = uuid(0xa2);
const LEE_ID = uuid(0xa3);
const SAM = `${SAM_ID}:teams.write,members.write,issues.read,issues.write`;
const KIM = `${KIM_ID}:issues.read,issues.write`;
const LEE = `${LEE_ID}`;

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

const schema = `table_ops_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let app: Hono;

/** What the hand-written operation was called with. */
const summarized: unknown[] = [];

interface Answer {
  readonly status: number;
  readonly body: any;
}

/** A request to the app as the caller the credential names, or none. */
async function call(method: string, path: string, options: { as?: string; body?: unknown } = {}): Promise<Answer> {
  const response = await app.request(path, {
    method,
    headers: {
      ...(options.as !== undefined ? { authorization: `Bearer ${options.as}` } : {}),
      ...(options.body !== undefined ? { 'content-type': 'application/json' } : {}),
    },
    ...(options.body !== undefined ? { body: JSON.stringify(options.body) } : {}),
  });
  return { status: response.status, body: await response.json() };
}

/** The data of a call that succeeds, as sam unless told. */
async function ok(method: string, path: string, body?: unknown, as = SAM): Promise<any> {
  const answer = await call(method, path, { as, body });
  if (answer.status !== 200) throw new Error(`${method} ${path} answered ${answer.status}: ${JSON.stringify(answer.body)}`);
  expect(Object.keys(answer.body)).toEqual(['data', 'meta']);
  return answer.body.data;
}

/** The problem a call is refused with, less its request id. */
async function refused(method: string, path: string, options: { as?: string; body?: unknown } = { as: SAM }): Promise<Record<string, unknown>> {
  const answer = await call(method, path, options);
  expect(answer.status).toBeGreaterThanOrEqual(400);
  const { requestId, ...problem } = answer.body;
  expect(typeof requestId).toBe('string');
  expect(problem.status).toBe(answer.status);
  // No refusal carries a statement's or the driver's words.
  expect(JSON.stringify(problem)).not.toMatch(/violates|constraint|relation "|duplicate key|SELECT|INSERT|UPDATE /u);
  return problem;
}

/** Every item of a list, following each next from path, and the size of each page. */
async function walk(path: string, as = SAM): Promise<{ items: any[]; sizes: number[] }> {
  const items: any[] = [];
  const sizes: number[] = [];
  let next: string | null = null;
  do {
    const page: any = await ok('GET', next === null ? path : `${path}${path.includes('?') ? '&' : '?'}cursor=${next}`, undefined, as);
    expect(Object.keys(page).sort()).toEqual(['items', 'next']);
    items.push(...page.items);
    sizes.push(page.items.length);
    next = page.next;
  } while (next !== null && sizes.length < 50);
  return { items, sizes };
}

const titles = (items: readonly { title: string }[]) => items.map(item => item.title);

const ISSUE_VIEW = ['id', 'teamId', 'title', 'body', 'status', 'priority', 'estimate', 'dueOn', 'assigneeId', 'createdAt', 'createdBy', 'updatedAt'];

// The rows every case reads: two teams, two members, and the issues of
// the first team, which no case changes. A case that writes makes rows of
// its own, in the second team.
let core: any;
let other: any;
let ada: any;
let bob: any;
/** The first team's issues, in the order they were created. */
let issues: any[];

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  // The one operation the implementation writes: a team's open issues,
  // counted through the repositories.
  const implementations = {
    teams: {
      summarizeTeam: async (args: { id: string }) => {
        summarized.push(args);
        const { total } = await orm.issue.findMany({ teamId: { eq: args.id as never }, status: { eq: 'open' as never } }, { limit: 1 });
        return { teamId: args.id, openIssues: total };
      },
    },
  };
  app = new Hono();
  app.route('/', buildRouter(implementations, { authenticate, orm }));
  app.notFound(notFoundHandler());
  app.onError(errorHandler());

  core = await ok('POST', '/api/teams', { key: 'core', name: 'Core' });
  other = await ok('POST', '/api/teams', { key: 'other', name: 'Other' });
  ada = await ok('POST', '/api/members', { name: 'Ada' });
  bob = await ok('POST', '/api/members', { name: 'Bob' });
  issues = [];
  for (const input of [
    { title: 'Crash on save', body: 'The editor crashes when saving', priority: 5, estimate: 3, dueOn: '2026-03-01', assigneeId: ada.id },
    { title: 'Slow search', priority: 2 },
    { title: '100% done', body: 'crash report attached', priority: 9, estimate: 0.5, dueOn: '2026-01-15', assigneeId: bob.id },
    { title: '100 percent', priority: 1, estimate: 8, dueOn: '2026-06-30', assigneeId: ada.id },
    { title: 'snake_case names', body: 'save the snake', priority: 7 },
    { title: 'snakeXcase', body: 'a back\\slash', priority: 4, estimate: 1, dueOn: '2026-03-01', assigneeId: bob.id },
  ]) {
    issues.push(await ok('POST', `/api/teams/${core.id}/issues`, input));
  }
  // Two of them move on, so a filter by status finds each.
  issues[2] = await ok('PATCH', `/api/issues/${issues[2].id}`, { status: 'done' });
  issues[3] = await ok('PATCH', `/api/issues/${issues[3].id}`, { status: 'started' });
});

afterAll(async () => {
  await pool?.end();
  await admin?.query(`DROP SCHEMA IF EXISTS ${schema} CASCADE`);
  await admin?.end();
});

describe('the table operations of fixture-tracker-api', () => {
  test("a create answers the row's view, with the table's defaults and its caller stamped", async () => {
    // A team's view is its three members, the key the database made among them.
    expect(core).toEqual({ id: expect.any(String), key: 'core', name: 'Core' });
    expect(Object.keys(core)).toEqual(['id', 'key', 'name']);
    expect(await orm.team.getOne(core.id)).toEqual(core);

    const created = await ok('POST', `/api/teams/${other.id}/issues`, { title: 'Defaults', priority: null }, KIM);
    expect(Object.keys(created)).toEqual(ISSUE_VIEW);
    expect(created).toEqual({
      id: expect.any(String),
      teamId: other.id,
      title: 'Defaults',
      body: null,
      // The table's defaults, for a member left out and for one sent as null.
      status: 'open',
      priority: 0,
      estimate: null,
      dueOn: null,
      assigneeId: null,
      createdAt: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/u),
      createdBy: KIM_ID,
      updatedAt: null,
    });
    // The repositories stamp who made the row, as its first writer too.
    const row = await orm.issue.getOne(created.id);
    expect(row).toMatchObject({ createdBy: KIM_ID, updatedBy: KIM_ID, updatedAt: null, deletedAt: null, deletedBy: null, priority: 0, status: 'open' });
    expect(row.createdAt.toISOString()).toBe(created.createdAt);

    // Every column type the view holds comes back as the row holds it.
    expect(issues[0]).toMatchObject({
      title: 'Crash on save',
      body: 'The editor crashes when saving',
      priority: 5,
      estimate: 3,
      dueOn: '2026-03-01',
      assigneeId: ada.id,
      createdBy: SAM_ID,
    });
  });

  test('a create under a parent sets the relation from the path, and a parent that does not exist is not found', async () => {
    expect(issues.every(issue => issue.teamId === core.id)).toBe(true);
    const comment = await ok('POST', `/api/issues/${issues[0].id}/comments`, { body: 'Seen it too' });
    expect(comment).toEqual({ id: expect.any(String), issueId: issues[0].id, body: 'Seen it too', createdAt: expect.any(String) });

    expect(await refused('POST', `/api/teams/${uuid(0xdead)}/issues`, { as: SAM, body: { title: 'Nowhere' } })).toEqual({
      type: 'about:blank',
      title: 'Not Found',
      status: 404,
      detail: 'Team not found',
      code: 'not_found',
    });
    expect(await refused('POST', `/api/issues/${uuid(0xdead)}/comments`, { as: SAM, body: { body: 'Nowhere' } })).toMatchObject({
      status: 404,
      code: 'not_found',
      detail: 'Issue not found',
    });
    expect((await orm.issue.findMany({ title: { eq: 'Nowhere' } })).total).toBe(0);
  });

  test('a read answers the view of the row its key and its parents name', async () => {
    expect(await ok('GET', `/api/issues/${issues[0].id}`)).toEqual(issues[0]);
    expect(await ok('GET', `/api/teams/${core.id}`, undefined, LEE)).toEqual(core);
    // The key in its hyphenated form names the same row.
    expect(await ok('GET', '/api/teams/' + (await pool.query('SELECT id FROM team WHERE "key" = $1', ['core'])).rows[0].id)).toEqual(core);
    expect(await refused('GET', `/api/issues/${uuid(0xdead)}`)).toEqual({
      type: 'about:blank',
      title: 'Not Found',
      status: 404,
      detail: 'Issue not found',
      code: 'not_found',
    });
    expect(await refused('GET', `/api/teams/${uuid(0xdead)}`)).toMatchObject({ status: 404, code: 'not_found', detail: 'Team not found' });
  });

  test('a second view of the table shows only its own members, behind its own guard', async () => {
    const answer = await call('GET', `/api/public/issues/${issues[0].id}`);
    expect(answer.status).toBe(200);
    expect(answer.body.data).toEqual({ id: issues[0].id, title: 'Crash on save', status: 'open' });
    expect(Object.keys(answer.body.data)).toEqual(['id', 'title', 'status']);
    expect((await refused('GET', `/api/public/issues/${uuid(0xdead)}`, {})).status).toBe(404);
  });

  test('an update sets the members the body sets: absent leaves, null clears, and null on a column that takes none is a 400 at the member', async () => {
    const made = await ok('POST', `/api/teams/${other.id}/issues`, { title: 'To change', body: 'first', estimate: 2, dueOn: '2026-05-05', assigneeId: ada.id });
    const path = `/api/issues/${made.id}`;

    const changed = await ok('PATCH', path, { title: 'Changed', priority: 3, status: 'started', assigneeId: bob.id }, KIM);
    expect(changed).toEqual({
      ...made,
      title: 'Changed',
      priority: 3,
      status: 'started',
      assigneeId: bob.id,
      updatedAt: expect.any(String),
    });
    // The members the body left out are as they were, and the update is stamped with its caller.
    expect(changed).toMatchObject({ body: 'first', estimate: 2, dueOn: '2026-05-05', createdBy: SAM_ID });
    const row = await orm.issue.getOne(made.id);
    expect(row).toMatchObject({ updatedBy: KIM_ID, createdBy: SAM_ID });
    expect(row.updatedAt!.toISOString()).toBe(changed.updatedAt);

    // null clears a column that may be null.
    const cleared = await ok('PATCH', path, { body: null, estimate: null, dueOn: null, assigneeId: null });
    expect(cleared).toMatchObject({ title: 'Changed', priority: 3, status: 'started', body: null, estimate: null, dueOn: null, assigneeId: null });

    // null on a column that takes none, each a 400 at its member, with nothing written.
    expect(await refused('PATCH', path, { as: SAM, body: { title: null, body: 'not written' } })).toEqual({
      type: 'about:blank',
      title: 'Bad Request',
      status: 400,
      detail: 'Request body does not match the declared input',
      code: 'bad_request',
      details: { location: 'body', reason: 'validation failed' },
      errors: { title: [{ validator: 'required', message: 'required field' }] },
    });
    expect((await refused('PATCH', path, { as: SAM, body: { priority: null } })).errors).toEqual({ priority: [{ validator: 'required', message: 'required field' }] });
    expect((await refused('PATCH', path, { as: SAM, body: { status: null } })).errors).toEqual({ status: [{ validator: 'required', message: 'required field' }] });
    expect(await ok('GET', path)).toEqual(cleared);

    // The body's own rules hold before the handler runs.
    expect((await refused('PATCH', path, { as: SAM, body: { title: '' } })).status).toBe(400);
    expect((await refused('PATCH', path, { as: SAM, body: { teamId: core.id } })).status).toBe(400);
    expect((await refused('PATCH', `/api/issues/${uuid(0xdead)}`, { as: SAM, body: { title: 'x' } })).status).toBe(404);
  });

  test('an update of no member writes nothing', async () => {
    const made = await ok('POST', `/api/teams/${other.id}/issues`, { title: 'Untouched' });
    const before = await orm.issue.getOne(made.id);
    expect(await ok('PATCH', `/api/issues/${made.id}`, {}, KIM)).toEqual(made);
    const after = await orm.issue.getOne(made.id);
    expect(after).toEqual(before);
    // Neither stamp moved: the row's last writer is still who made it.
    expect(after).toMatchObject({ updatedAt: null, updatedBy: SAM_ID });
  });

  test('a unique key another row holds is a conflict', async () => {
    expect(await refused('POST', '/api/teams', { as: SAM, body: { key: 'core', name: 'Core again' } })).toEqual({
      type: 'about:blank',
      title: 'Conflict',
      status: 409,
      detail: 'Team with that key already exists',
      code: 'conflict',
    });
    expect((await orm.team.findMany({ key: { eq: 'core' as never } })).total).toBe(1);
    // The transaction rolled back, and the next write goes through.
    expect((await ok('POST', '/api/teams', { key: 'third', name: 'Third' })).key).toBe('third');
  });

  test("a relation's key that names no row is a 400 at the member", async () => {
    const made = await ok('POST', `/api/teams/${other.id}/issues`, { title: 'Unassigned' });
    const problem = {
      type: 'about:blank',
      title: 'Bad Request',
      status: 400,
      detail: 'Request body does not match the declared input',
      code: 'bad_request',
      details: { location: 'body', reason: 'validation failed' },
      errors: { assigneeId: [{ validator: 'reference', message: 'names no Member' }] },
    };
    expect(await refused('PATCH', `/api/issues/${made.id}`, { as: SAM, body: { assigneeId: uuid(0xdead), title: 'Not written' } })).toEqual(problem);
    expect(await ok('GET', `/api/issues/${made.id}`)).toEqual(made);
    expect(await refused('POST', `/api/teams/${other.id}/issues`, { as: SAM, body: { title: 'Never made', assigneeId: uuid(0xdead) } })).toEqual(problem);
    expect((await orm.issue.findMany({ title: { eq: 'Never made' } })).total).toBe(0);
  });

  test('a soft delete keeps the row, stamped, and every operation then finds none', async () => {
    const made = await ok('POST', `/api/teams/${other.id}/issues`, { title: 'To delete', body: 'kept in the table' });
    const path = `/api/issues/${made.id}`;
    // The view as it was before the delete.
    expect(await ok('DELETE', path, undefined, KIM)).toEqual(made);

    const row = await orm.issue.findOne({ id: { eq: made.id } }, { includeDeleted: true });
    expect(row).toMatchObject({ id: made.id, title: 'To delete', deletedBy: KIM_ID });
    expect(row!.deletedAt).toBeInstanceOf(Date);

    const gone = { type: 'about:blank', title: 'Not Found', status: 404, detail: 'Issue not found', code: 'not_found' };
    expect(await refused('GET', path)).toEqual(gone);
    expect(await refused('PATCH', path, { as: SAM, body: { title: 'Back' } })).toEqual(gone);
    expect(await refused('DELETE', path)).toEqual(gone);
    expect(await refused('GET', `/api/public/issues/${made.id}`, {})).toEqual(gone);
    expect(titles((await walk(`/api/teams/${other.id}/issues?limit=5`)).items)).not.toContain('To delete');
    expect(await orm.issue.findOne({ id: { eq: made.id } }, { includeDeleted: true })).toEqual(row);
  });

  test('a hard delete removes the row, and one under another parent is not found', async () => {
    const [first, second] = [issues[0].id, issues[1].id];
    const comment = await ok('POST', `/api/issues/${first}/comments`, { body: 'To remove' });
    // The comment is the first issue's: the second's path does not reach it.
    expect(await refused('DELETE', `/api/issues/${second}/comments/${comment.id}`)).toEqual({
      type: 'about:blank',
      title: 'Not Found',
      status: 404,
      detail: 'Comment not found',
      code: 'not_found',
    });
    expect((await orm.comment.getOne(comment.id)).body).toBe('To remove');

    expect(await ok('DELETE', `/api/issues/${first}/comments/${comment.id}`)).toEqual(comment);
    expect((await pool.query('SELECT count(*)::int AS n FROM "comment" WHERE body = $1', ['To remove'])).rows[0].n).toBe(0);
    expect((await refused('DELETE', `/api/issues/${first}/comments/${comment.id}`)).status).toBe(404);
    expect(titles((await walk(`/api/issues/${second}/comments`)).items)).toEqual([]);
  });

  test("a list pages by cursor in the repository's order, under its parents", async () => {
    // An unsorted list reads in the repository's own page order, by key.
    const byKey = (await orm.team.findPage({}, { limit: 100 })).rows;
    expect(byKey.length).toBeGreaterThanOrEqual(2);
    const all = await walk('/api/teams?limit=1', LEE);
    expect(all.items).toEqual(byKey);
    expect(all.sizes).toEqual(byKey.map(() => 1));
    expect((await ok('GET', '/api/teams')).items).toEqual(byKey);
    // The cursor is the repository's page key.
    const first = await ok('GET', '/api/teams?limit=1');
    expect(JSON.parse(Buffer.from(first.next, 'base64url').toString())).toEqual({ id: byKey[0]!.id });

    // The first team's issues, newest first, two to a page, and none of the second's.
    const mine = await walk(`/api/teams/${core.id}/issues`);
    expect(mine.items).toEqual([...issues].reverse());
    expect(mine.sizes).toEqual([2, 2, 2]);
    expect((await walk(`/api/teams/${core.id}/issues?limit=5`)).sizes).toEqual([5, 1]);
    const theirs = await walk(`/api/teams/${other.id}/issues?limit=5`);
    expect(theirs.items.every(issue => issue.teamId === other.id)).toBe(true);
    expect(titles(theirs.items)).not.toContain('Crash on save');
    expect((await walk(`/api/teams/${uuid(0xdead)}/issues`)).items).toEqual([]);

    // An issue's comments, under the issue.
    await ok('POST', `/api/issues/${issues[2].id}/comments`, { body: 'One' });
    await ok('POST', `/api/issues/${issues[2].id}/comments`, { body: 'Two' });
    const comments = await walk(`/api/issues/${issues[2].id}/comments?limit=1`);
    expect(comments.items.map(comment => comment.body).sort()).toEqual(['One', 'Two']);
    expect(comments.items.every(comment => comment.issueId === issues[2].id)).toBe(true);

    // A cursor that names no row of its order, and one that is no page key.
    const forged = (key: unknown) => Buffer.from(JSON.stringify(key)).toString('base64url');
    for (const cursor of [forged({ id: 'not a key' }), forged('green'), forged({ position: 3 })]) {
      expect(await refused('GET', `/api/teams?cursor=${cursor}`)).toMatchObject({
        status: 400,
        code: 'bad_request',
        detail: 'cursor is not one this operation returned',
        details: { location: 'query', parameter: 'cursor' },
      });
    }
    expect((await refused('GET', `/api/teams/${core.id}/issues?limit=6`)).details).toMatchObject({ parameter: 'limit' });
  });

  test('a list sorts by each field either way, each next bound to its sort', async () => {
    const by = (field: 'priority' | 'createdAt', desc: boolean) =>
      [...issues].sort((a, b) => (a[field] < b[field] ? -1 : a[field] > b[field] ? 1 : 0) * (desc ? -1 : 1));
    for (const field of ['priority', 'createdAt'] as const) {
      for (const desc of [false, true]) {
        const sorted = await walk(`/api/teams/${core.id}/issues?sort=${desc ? '-' : ''}${field}`);
        expect(titles(sorted.items)).toEqual(titles(by(field, desc)));
        expect(sorted.sizes).toEqual([2, 2, 2]);
      }
    }
    // Text sorts in the database's order, its collation's, which the
    // repository's own read gives.
    for (const desc of [false, true]) {
      const inOrder = await orm.issue.findMany({ teamId: { eq: core.id } }, { orderBy: [{ field: 'title', desc }], limit: 100 });
      const sorted = await walk(`/api/teams/${core.id}/issues?sort=${desc ? '-' : ''}title`);
      expect(titles(sorted.items)).toEqual(titles(inOrder.rows));
      expect(sorted.sizes).toEqual([2, 2, 2]);
    }
    expect(titles(by('priority', false))).toEqual(['100 percent', 'Slow search', 'snakeXcase', 'Crash on save', 'snake_case names', '100% done']);
    // The default sort is the newest first.
    expect((await ok('GET', `/api/teams/${core.id}/issues`)).items).toEqual(by('createdAt', true).slice(0, 2));

    const first = await ok('GET', `/api/teams/${core.id}/issues?sort=-priority`);
    expect(JSON.parse(Buffer.from(first.next, 'base64url').toString())).toEqual({ sort: '-priority', key: { id: by('priority', true)[1].id } });
    expect(await refused('GET', `/api/teams/${core.id}/issues?sort=priority&cursor=${first.next}`)).toMatchObject({
      status: 400,
      detail: 'cursor is not one this sort returned',
    });
    expect((await refused('GET', `/api/teams/${core.id}/issues?sort=status`)).details).toMatchObject({ parameter: 'sort' });
  });

  test('a list sorts by a field that may be null, its nulls last either way, and pages through them', async () => {
    // The order a walk must read, worked out here and not by the
    // database: the rows that hold a value by it, ties by id, then the
    // rows that hold none by id, both in the sort's direction. Postgres
    // orders a uuid by its bytes, as its hyphenated text orders.
    const ids = new Map<string, string>();
    for (const row of (await pool.query('SELECT id::text AS id, title FROM issue')).rows) ids.set(row.title, row.id);
    const byId = (a: any, b: any) => (ids.get(a.title)! < ids.get(b.title)! ? -1 : 1);
    const nullsLast = (field: 'dueOn' | 'estimate', desc: boolean) => {
      const way = desc ? -1 : 1;
      const held = issues.filter(issue => issue[field] !== null);
      const none = issues.filter(issue => issue[field] === null);
      held.sort((a, b) => (a[field] < b[field] ? -1 : a[field] > b[field] ? 1 : byId(a, b)) * way);
      none.sort((a, b) => byId(a, b) * way);
      return [...held, ...none];
    };
    // Two issues are due on one day, two have no due date and two no
    // estimate, so a page ends on a value, on a tie, on the last value
    // before the nulls, on a null, and on the last row.
    expect(issues.filter(issue => issue.dueOn === null).length).toBe(2);
    expect(issues.filter(issue => issue.dueOn === '2026-03-01').length).toBe(2);
    for (const field of ['dueOn', 'estimate'] as const) {
      for (const desc of [false, true]) {
        const want = titles(nullsLast(field, desc));
        for (const [limit, sizes] of [[1, [1, 1, 1, 1, 1, 1]], [2, [2, 2, 2]], [4, [4, 2]], [5, [5, 1]]] as const) {
          const sorted = await walk(`/api/teams/${core.id}/issues?sort=${desc ? '-' : ''}${field}&limit=${limit}`);
          expect(titles(sorted.items)).toEqual(want);
          expect(sorted.sizes).toEqual([...sizes]);
        }
      }
    }
    expect(titles(nullsLast('dueOn', false)).slice(0, 1)).toEqual(['100% done']);
    expect(titles(nullsLast('dueOn', true)).slice(0, 1)).toEqual(['100 percent']);
    expect(titles(nullsLast('dueOn', true)).slice(4).sort()).toEqual(['Slow search', 'snake_case names']);

    // The cursor of a page that ends on a null is the row's key, as any is.
    const last = nullsLast('dueOn', false);
    const page = await ok('GET', `/api/teams/${core.id}/issues?sort=dueOn&limit=5`);
    expect(JSON.parse(Buffer.from(page.next, 'base64url').toString())).toEqual({ sort: 'dueOn', key: { id: last[4].id } });
    // A filter holds beside the order: the issues with no due date alone,
    // and the ones with one.
    expect(titles((await walk(`/api/teams/${core.id}/issues?sort=-dueOn&limit=1&filter[dueOn][isNull]=true`)).items)).toEqual(titles(nullsLast('dueOn', true).slice(4)));
    expect(titles((await walk(`/api/teams/${core.id}/issues?sort=-dueOn&limit=1&filter[dueOn][isNull]=false`)).items)).toEqual(titles(nullsLast('dueOn', true).slice(0, 4)));
  });

  test('a list searches by its text', async () => {
    const found = async (q: string) => titles((await walk(`/api/teams/${core.id}/issues?limit=5&q=${encodeURIComponent(q)}`)).items).sort();
    // Each word, in the title or the body, in any case.
    expect(await found('crash')).toEqual(['100% done', 'Crash on save']);
    expect(await found('CRASH save')).toEqual(['Crash on save']);
    expect(await found('snake')).toEqual(['snakeXcase', 'snake_case names']);
    expect(await found('snake save')).toEqual(['snake_case names']);
    expect(await found('nothing-holds-this')).toEqual([]);
    // A search of no text searches nothing.
    expect(await found('')).toEqual(titles(issues).sort());
    expect(await found('   ')).toEqual(titles(issues).sort());
    // A word's % and _ match themselves.
    expect(await found('100%')).toEqual(['100% done']);
    expect((await refused('GET', `/api/teams/${core.id}/issues?q=${'x'.repeat(101)}`)).details).toMatchObject({ parameter: 'q' });
  });

  test('a list filters by each kind of field', async () => {
    const kept = async (query: string) => titles((await walk(`/api/teams/${core.id}/issues?limit=5&${query}`)).items).sort();
    const where = (keep: (issue: any) => boolean) => titles(issues.filter(keep)).sort();

    // An enum: eq and in.
    expect(await kept('filter[status]=open')).toEqual(where(issue => issue.status === 'open'));
    expect(await kept('filter[status][in]=done,started')).toEqual(['100 percent', '100% done']);
    // A whole number: eq, in and a range.
    expect(await kept('filter[priority]=5')).toEqual(['Crash on save']);
    expect(await kept('filter[priority][in]=1,9,4')).toEqual(['100 percent', '100% done', 'snakeXcase']);
    expect(await kept('filter[priority][gte]=4&filter[priority][lt]=9')).toEqual(where(issue => issue.priority >= 4 && issue.priority < 9));
    expect(await kept('filter[priority][gt]=5&filter[priority][lte]=9')).toEqual(['100% done', 'snake_case names']);
    // Text: eq, in and contains, in any case.
    expect(await kept('filter[title]=Slow+search')).toEqual(['Slow search']);
    expect(await kept('filter[title][in]=Slow+search,snakeXcase')).toEqual(['Slow search', 'snakeXcase']);
    expect(await kept('filter[title][contains]=SNAKE')).toEqual(['snakeXcase', 'snake_case names']);
    // A number that may be null: a range, and isNull either way.
    expect(await kept('filter[estimate][gt]=0.5&filter[estimate][lte]=3')).toEqual(['Crash on save', 'snakeXcase']);
    expect(await kept('filter[estimate][isNull]=true')).toEqual(where(issue => issue.estimate === null));
    expect(await kept('filter[estimate][isNull]=false')).toEqual(where(issue => issue.estimate !== null));
    // A date: eq, a range and isNull.
    expect(await kept('filter[dueOn]=2026-03-01')).toEqual(['Crash on save', 'snakeXcase']);
    expect(await kept('filter[dueOn][gte]=2026-02-01&filter[dueOn][lt]=2026-07-01')).toEqual(['100 percent', 'Crash on save', 'snakeXcase']);
    expect(await kept('filter[dueOn][isNull]=true')).toEqual(['Slow search', 'snake_case names']);
    // A relation's key: eq, in and isNull.
    expect(await kept(`filter[assigneeId]=${ada.id}`)).toEqual(['100 percent', 'Crash on save']);
    expect(await kept(`filter[assigneeId][in]=${ada.id},${bob.id}`)).toEqual(where(issue => issue.assigneeId !== null));
    expect(await kept('filter[assigneeId][isNull]=true')).toEqual(['Slow search', 'snake_case names']);
    // Who created a row, an audit field: eq and in (D110).
    expect(await kept(`filter[createdBy]=${SAM_ID}`)).toEqual(where(() => true));
    expect(await kept(`filter[createdBy]=${KIM_ID}`)).toEqual([]);
    expect(await kept(`filter[createdBy][in]=${KIM_ID},${SAM_ID}&filter[assigneeId]=${ada.id}`)).toEqual(['100 percent', 'Crash on save']);
    // A timestamp: a range around one issue's.
    const at = issues[2].createdAt;
    expect(await kept(`filter[createdAt][lt]=${at}`)).toEqual(titles(issues.slice(0, 2)).sort());
    expect(await kept(`filter[createdAt][gte]=${at}`)).toEqual(titles(issues.slice(2)).sort());
    // A response's timestamp holds milliseconds and the column
    // microseconds, so the row is found by the millisecond it falls in.
    const after = new Date(new Date(at).getTime() + 1).toISOString();
    expect(await kept(`filter[createdAt][gte]=${at}&filter[createdAt][lt]=${after}`)).toEqual(['100% done']);
    expect(await kept('filter[createdAt][lt]=2000-01-01T00:00:00Z')).toEqual([]);

    // A field, an operator and a value the filter does not take are the route's 400 at filter[field].
    for (const [query, parameter] of [
      ['filter[body]=x', 'filter[body]'],
      ['filter[status][gt]=open', 'filter[status]'],
      ['filter[status]=closed', 'filter[status]'],
      ['filter[priority]=high', 'filter[priority]'],
      ['filter[dueOn]=soon', 'filter[dueOn]'],
      ['filter[createdAt]=yesterday', 'filter[createdAt]'],
      ['filter[assigneeId]=not-a-uuid', 'filter[assigneeId]'],
    ] as const) {
      expect(await refused('GET', `/api/teams/${core.id}/issues?${query}`)).toMatchObject({ status: 400, code: 'bad_request', details: { location: 'query', parameter } });
    }
  });

  test("a list's contains matches % and _ as themselves", async () => {
    const kept = async (text: string) =>
      titles((await walk(`/api/teams/${core.id}/issues?limit=5&filter[title][contains]=${encodeURIComponent(text)}`)).items).sort();
    expect(await kept('100')).toEqual(['100 percent', '100% done']);
    expect(await kept('100%')).toEqual(['100% done']);
    expect(await kept('%')).toEqual(['100% done']);
    expect(await kept('snake')).toEqual(['snakeXcase', 'snake_case names']);
    expect(await kept('snake_case')).toEqual(['snake_case names']);
    expect(await kept('_')).toEqual(['snake_case names']);
    expect(await kept('e_c')).toEqual(['snake_case names']);
    // A backslash matches itself too: no title holds one.
    expect(await kept('\\')).toEqual([]);
  });

  test("a list's filters, search, sort and cursor hold together", async () => {
    const path = `/api/teams/${core.id}/issues?sort=-priority&limit=1&q=crash&filter[status][in]=open,done&filter[estimate][isNull]=false`;
    const found = await walk(path);
    // Both issues hold "crash", each has an estimate, and the higher priority comes first.
    expect(titles(found.items)).toEqual(['100% done', 'Crash on save']);
    expect(found.sizes).toEqual([1, 1]);
    const first = await ok('GET', path);
    expect(first.next).not.toBeNull();
    // A cursor carries no filter: the next page under another is that one's.
    const narrowed = await ok('GET', `/api/teams/${core.id}/issues?sort=-priority&limit=1&filter[status]=done&cursor=${first.next}`);
    expect(narrowed).toEqual({ items: [], next: null });
    // The parents hold beside the filter: the second team's issues are not the first's.
    expect((await walk(`/api/teams/${other.id}/issues?limit=5&q=crash`)).items).toEqual([]);
  });

  test("the routes' guards hold: no caller, no permission, a public route", async () => {
    const unauthorized = { type: 'about:blank', title: 'Unauthorized', status: 401, detail: 'Authentication required', code: 'unauthorized' };
    const forbidden = { type: 'about:blank', title: 'Forbidden', status: 403, detail: 'Insufficient permissions', code: 'forbidden' };
    // Without a caller, every operation but the public one.
    for (const [method, path, body] of [
      ['GET', '/api/teams'],
      ['GET', `/api/teams/${core.id}`],
      ['POST', '/api/teams', { key: 'anon', name: 'Anon' }],
      ['GET', `/api/teams/${core.id}/issues`],
      ['GET', `/api/issues/${issues[0].id}`],
      ['POST', `/api/teams/${core.id}/issues`, { title: 'Anon' }],
      ['PATCH', `/api/issues/${issues[0].id}`, { title: 'Anon' }],
      ['DELETE', `/api/issues/${issues[0].id}`],
      ['GET', `/api/issues/${issues[0].id}/comments`],
    ] as const) {
      expect(await refused(method, path, { body })).toEqual(unauthorized);
    }
    // A caller without the route's permission.
    for (const [method, path, body] of [
      ['POST', '/api/teams', { key: 'lee', name: 'Lee' }],
      ['POST', '/api/members', { name: 'Lee' }],
      ['GET', `/api/teams/${core.id}/issues`],
      ['GET', `/api/issues/${issues[0].id}`],
      ['PATCH', `/api/issues/${issues[0].id}`, { title: 'Lee' }],
      ['DELETE', `/api/issues/${issues[0].id}`],
      ['POST', `/api/issues/${issues[0].id}/comments`, { body: 'Lee' }],
    ] as const) {
      expect(await refused(method, path, { as: LEE, body })).toEqual(forbidden);
    }
    expect(await refused('POST', '/api/teams', { as: KIM, body: { key: 'kim', name: 'Kim' } })).toEqual(forbidden);
    // Nothing a refused caller sent was written.
    expect(await ok('GET', `/api/issues/${issues[0].id}`)).toEqual(issues[0]);
    expect((await orm.team.findMany({ key: { in: ['anon', 'lee', 'kim'] as never } })).total).toBe(0);
    // A route that asks only for a caller takes one with no permission, and the public route no one.
    expect((await ok('GET', '/api/teams', undefined, LEE)).items.length).toBeGreaterThan(0);
    expect((await call('GET', `/api/public/issues/${issues[0].id}`)).status).toBe(200);
  });

  test('a row rule keeps each caller to the rows that name them, and its override permission lifts it', async () => {
    const issue = issues[0].id;
    // A create names the caller as the owner, whatever the body says of it.
    const kims = await ok('POST', `/api/issues/${issue}/notes`, { text: 'Kim only' }, KIM);
    expect(kims).toEqual({ id: expect.any(String), issueId: issue, owner: KIM_ID, sharedWith: null, text: 'Kim only', createdAt: expect.any(String), createdBy: KIM_ID });
    // The body's view holds no owner, so a request that names one is refused.
    expect(await refused('POST', `/api/issues/${issue}/notes`, { as: KIM, body: { text: 'For Sam', owner: SAM_ID } })).toMatchObject({
      status: 400,
      errors: { owner: [{ validator: 'unknown', message: 'unknown field' }] },
    });
    const shared = await ok('POST', `/api/issues/${issue}/notes`, { text: 'Kim, for Lee', sharedWith: LEE_ID }, KIM);
    expect(shared).toMatchObject({ owner: KIM_ID, sharedWith: LEE_ID });
    const lees = await ok('POST', `/api/issues/${issue}/notes`, { text: 'Lee only' }, LEE);
    expect((await orm.note.getOne(shared.id)).owner).toBe(KIM_ID);

    // A list holds the caller's own rows and the ones shared with them,
    // beside whatever its filter says.
    const texts = async (as: string, query = '') => (await walk(`/api/notes${query}`, as)).items.map(note => note.text);
    expect(await texts(KIM)).toEqual(['Kim only', 'Kim, for Lee']);
    expect(await texts(LEE)).toEqual(['Kim, for Lee', 'Lee only']);
    expect(await texts(SAM)).toEqual([]);
    expect(await texts(LEE, `?filter[owner]=${KIM_ID}`)).toEqual(['Kim, for Lee']);
    expect(await texts(SAM, `?filter[owner]=${KIM_ID}`)).toEqual([]);

    // A read of a row that names another is the 404 of one that is not there.
    const notFound = { status: 404, code: 'not_found', title: 'Not Found', detail: 'Note not found' };
    expect(await ok('GET', `/api/notes/${shared.id}`, undefined, LEE)).toEqual(shared);
    expect(await refused('GET', `/api/notes/${kims.id}`, { as: LEE })).toMatchObject(notFound);
    expect(await refused('GET', `/api/notes/${kims.id}`, { as: SAM })).toMatchObject(notFound);
    expect(await refused('GET', `/api/notes/${uuid(0xdead)}`, { as: KIM })).toMatchObject(notFound);

    // An update is the owner's alone: a note shared with Lee is not Lee's to change.
    expect(await refused('PATCH', `/api/notes/${shared.id}`, { as: LEE, body: { text: 'Lee was here' } })).toMatchObject(notFound);
    expect(await ok('PATCH', `/api/notes/${shared.id}`, { text: 'Kim, still for Lee' }, KIM)).toMatchObject({ text: 'Kim, still for Lee', owner: KIM_ID });
    expect((await orm.note.getOne(shared.id)).text).toBe('Kim, still for Lee');

    // The holder of the override permission reaches every row, by a grant that covers it.
    for (const admin of [`${SAM_ID}:notes.admin`, `${SAM_ID}:notes`]) {
      expect(await texts(admin)).toEqual(['Kim only', 'Kim, still for Lee', 'Lee only']);
      expect(await ok('GET', `/api/notes/${kims.id}`, undefined, admin)).toEqual(kims);
    }
    // The update names no override, so it holds for an admin too.
    expect(await refused('PATCH', `/api/notes/${kims.id}`, { as: `${SAM_ID}:notes.admin`, body: { text: 'Sam was here' } })).toMatchObject(notFound);

    // A delete is its writer's, by createdBy, or an admin's.
    expect(await refused('DELETE', `/api/notes/${lees.id}`, { as: KIM })).toMatchObject(notFound);
    expect(await ok('DELETE', `/api/notes/${lees.id}`, undefined, LEE)).toEqual(lees);
    expect(await ok('DELETE', `/api/notes/${kims.id}`, undefined, `${SAM_ID}:notes.admin`)).toEqual(kims);
    expect(await texts(`${SAM_ID}:notes.admin`)).toEqual(['Kim, still for Lee']);

    // A caller whose subject is no id is named by no row, and cannot be named in a new one.
    expect(await texts('billing-worker')).toEqual([]);
    expect(await refused('GET', `/api/notes/${shared.id}`, { as: 'billing-worker' })).toMatchObject(notFound);
    expect(await refused('POST', `/api/issues/${issue}/notes`, { as: 'billing-worker', body: { text: 'x' } })).toMatchObject({ status: 403 });
    // And the routes ask for a caller.
    expect(await refused('GET', '/api/notes', {})).toMatchObject({ status: 401 });
  });

  test('an operation written by hand reaches the implementation', async () => {
    const before = summarized.length;
    expect(await ok('GET', `/api/teams/${core.id}/summary`, undefined, LEE)).toEqual({
      teamId: core.id,
      openIssues: issues.filter(issue => issue.status === 'open').length,
    });
    expect(summarized.slice(before)).toEqual([{ id: core.id }]);
    // It is the one operation that is no table operation.
    expect(Object.keys(tableOperations).sort()).toEqual(
      [
        'createComment',
        'createIssue',
        'createMember',
        'createNote',
        'createTeam',
        'deleteComment',
        'deleteIssue',
        'deleteNote',
        'getIssue',
        'getIssueForGuest',
        'getNote',
        'getTeam',
        'listComments',
        'listIssues',
        'listMembers',
        'listNotes',
        'listTeams',
        'updateIssue',
        'updateNote',
      ].sort()
    );
  });
});
