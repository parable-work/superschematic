/*
fixture-graph-api's graph operations end to end (D138), run by
TestGeneratedGraphOperationsEndToEnd with API_DIR set to the materialized
package, SDK_DIR to the API's generated TypeScript SDK, CREATE_SQL to
fixture-version-graph-db's Postgres create.sql and
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to the database. The
generated router serves the API over the generated repositories of
fixture-version-graph-db (createOrm) and its Recipe graph's generated
facade (RecipeGraph), in a schema of the test's own, with an authenticator
that reads `Authorization: Bearer <subject>[:<permission>,...]` and
establishes that subject holding those permissions. Each call goes through
the SDK, whose fetch hands the request to the app: a recipe's first branch
makes its primary line; a change set is saved, read, arranged, committed,
merged with a tag, released, rebased over a conflict, reverted, sealed and
discarded; another caller's change set is not found, and the override
permission lifts that; and each refusal is the problem of its code.
*/
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import pg from 'pg';
import { Hono } from 'hono';
import type { Principal, RequestContext } from '@superschematic/http-runtime';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';
import { pgPool } from '@superschematic/versiongraph/postgres';
import { createOrm } from '@schemas/fixture-version-graph-db-types/orm';
import { RecipeGraph } from '@schemas/fixture-version-graph-db-types/versiongraph';
import { parseIdentityUUID } from 'superscalar/scalars';

const apiDir = process.env.API_DIR;
const sdkDir = process.env.SDK_DIR;
const createSQL = process.env.CREATE_SQL;
const dsn = process.env.SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL;
if (!apiDir || !sdkDir || !createSQL || !dsn) throw new Error('API_DIR, SDK_DIR, CREATE_SQL and SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL are required');
const { buildRouter } = await import(`${apiDir}/router.ts`);
const { FixtureGraphApiSDK, ApiError } = await import(`${sdkDir}/index.ts`);

// The callers, each a subject that is a UUID: ann and bob may write
// recipes, ada holds the permission that lifts the row rule, pat may
// release, and lee holds no permission. sam may write, and their subject
// is no UUID.
const ann = crypto.randomUUID();
const bob = crypto.randomUUID();
const ada = crypto.randomUUID();
const pat = crypto.randomUUID();
const ANN = `${ann}:recipes.write`;
const BOB = `${bob}:recipes.write`;
const ADA = `${ada}:recipes.write,recipes.admin`;
const PAT = `${pat}:recipes.write,recipes.release`;
const LEE = `${crypto.randomUUID()}`;
const SAM = 'sam@kitchen:recipes.write';

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

const schema = `graph_ops_ts_${process.hrtime.bigint()}`;
let admin: pg.Client;
let pool: pg.Pool;
let orm: ReturnType<typeof createOrm>;
let graph: RecipeGraph;
let app: Hono;

beforeAll(async () => {
  admin = new pg.Client({ connectionString: dsn });
  await admin.connect();
  await admin.query(`CREATE SCHEMA ${schema}`);
  pool = new pg.Pool({ connectionString: dsn, options: `-c search_path=${schema},public`, max: 4 });
  await pool.query(readFileSync(createSQL, 'utf8'));
  orm = createOrm(pool);
  // The facade a project's entrypoint builds: the graph over a client of
  // the database, which each write then runs through as its caller.
  graph = new RecipeGraph(pgPool(pool));
  const implementations = {
    recipe: {
      getRecipe: async (args: { id: string }) => orm.recipe.getOne(args.id as never),
    },
  };
  app = new Hono();
  app.route('/', buildRouter(implementations, { authenticate, orm, graphs: { recipe: graph } }));
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
  return new FixtureGraphApiSDK({
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

/** A recipe row, created as ann. */
async function newRecipe(title: string): Promise<string> {
  return (await createOrm(pool, ann).recipe.createOne({ title })).id;
}

/** A ref's version as its row holds it. */
async function versionOf(ref: string): Promise<number> {
  return (await orm.recipeRef.getOne(ref as never))._version;
}

/** The primary line of a recipe. */
async function primaryOf(recipe: string) {
  const row = await orm.recipeRef.findOne({ rootId: { eq: recipe as never }, parentRefId: { isNull: true } });
  if (row === null) throw new Error('the recipe has no primary line');
  return row;
}

describe('the graph operations of fixture-graph-api', () => {
  let recipe: string;
  let main: string;
  let draft: { id: string; version: number };
  let parts: string[];
  let firstCommit: string;
  let tagged: string;

  beforeAll(async () => {
    recipe = await newRecipe('Soup');
  });

  test('branch creates the primary line of a root that has none, and a change set of it', async () => {
    const created = await sdk(ANN).recipe.branch(recipe, { name: 'draft' });
    expect(created).toMatchObject({ root: recipe, name: 'draft', sealed: false, discarded: false });
    const primary = await primaryOf(recipe);
    expect(primary.name).toBe('main');
    expect(parseIdentityUUID(primary.createdBy)).toBe(parseIdentityUUID(ann));
    expect(created.parent).toBe(primary.id);
    main = primary.id;
    draft = { id: created.id, version: created.version };

    // A branch of a ref the body names, by another caller, and no second
    // primary line.
    const other = await sdk(BOB).recipe.branch(recipe, { from: main, name: 'other' });
    expect(other.parent).toBe(main);
    expect((await primaryOf(recipe)).id).toBe(main);
    expect(await refusal(sdk(ANN).recipe.branch(recipe, { name: 'draft' }))).toMatchObject({ status: 409, code: 'name_taken' });
  });

  test('save writes rows with order keys, and the tree reads them back', async () => {
    const ranks = await graph.orderKeys(null, null, 3);
    const saved = await sdk(ANN).recipe.save(recipe, draft.id, {
      version: draft.version,
      edits: {
        part: {
          upsert: [
            { rank: ranks[0], heading: 'Intro' },
            { rank: ranks[1], heading: 'Method' },
            { rank: ranks[2], heading: 'Notes' },
          ],
        },
        step: { upsert: [{ position: 1, instruction: 'Boil the stock', timings: { boil: 10 } }] },
      },
    });
    expect(saved.ref.version).toBeGreaterThan(draft.version);
    draft.version = saved.ref.version;
    expect(saved.saved.part.map((part: { heading: string }) => part.heading)).toEqual(['Intro', 'Method', 'Notes']);
    expect(saved.saved.step).toHaveLength(1);
    parts = saved.saved.part.map((part: { entityKey: string }) => part.entityKey);

    const tree = await sdk(ANN).recipe.compose(recipe, draft.id);
    expect(tree.part.map((part: { heading: string }) => part.heading)).toEqual(['Intro', 'Method', 'Notes']);
    // A row holds its entity key and its content, and nothing else of it.
    expect(Object.keys(tree.part[0]).sort()).toEqual(['entityKey', 'heading', 'parentKey', 'rank']);
    expect(tree.step[0]).toMatchObject({ position: 1, instruction: 'Boil the stock', timings: { boil: 10 } });
    expect(tree.cover).toEqual([]);
    expect(tree.contentHash).not.toBe('');
    expect(tree.findings).toEqual([]);
  });

  test("arrange puts a parent's children in the order given", async () => {
    const arranged = await sdk(ANN).recipe.arrange(recipe, draft.id, { version: draft.version, kind: 'part', keys: [parts[2], parts[0], parts[1]] });
    draft.version = arranged.ref.version;
    expect(arranged.saved.part.length).toBeGreaterThan(0);
    const tree = await sdk(ANN).recipe.compose(recipe, draft.id);
    expect(tree.part.map((part: { heading: string }) => part.heading)).toEqual(['Notes', 'Intro', 'Method']);
  });

  test('commit writes a commit, and a stale version is a version conflict', async () => {
    expect(await refusal(sdk(ANN).recipe.commit(recipe, draft.id, { version: draft.version - 1, message: 'stale' }))).toMatchObject({
      status: 409,
      code: 'version_conflict',
    });
    const committed = await sdk(ANN).recipe.commit(recipe, draft.id, { version: draft.version, message: 'three parts' });
    expect(committed.commit).toMatchObject({ root: recipe, ref: draft.id, message: 'three parts' });
    expect(committed.commit.sequence ?? null).toBeNull();
    draft.version = committed.ref.version;
    firstCommit = committed.commit.id;
    expect(await refusal(sdk(ANN).recipe.commit(recipe, draft.id, { version: draft.version }))).toMatchObject({ status: 409, code: 'nothing_to_commit' });
  });

  test("a change set is its creator's: another caller is not found, and the override permission lifts the rule", async () => {
    const edits = { cover: { upsert: [{ photoUrl: 'https://example.com/soup.jpg' }] } };
    expect(await refusal(sdk(BOB).recipe.save(recipe, draft.id, { version: draft.version, edits }))).toMatchObject({
      status: 404,
      code: 'not_found',
      detail: 'ref not found',
    });
    expect(await refusal(sdk(BOB).recipe.commit(recipe, draft.id, { version: draft.version }))).toMatchObject({ status: 404, code: 'not_found' });
    expect(await refusal(sdk(BOB).recipe.discard(recipe, draft.id, { version: draft.version }))).toMatchObject({ status: 404, code: 'not_found' });
    // A merge writes its target: a merge into another caller's change set
    // is not found as a save to it is, whatever the source.
    expect(await refusal(sdk(BOB).recipe.merge(recipe, main, { into: draft.id, version: draft.version }))).toMatchObject({ status: 404, code: 'not_found' });
    // A primary line is never discarded, by the caller who made it either.
    expect(await refusal(sdk(ANN).recipe.discard(recipe, main, { version: await versionOf(main) }))).toMatchObject({ status: 409, code: 'primary_merge_only' });
    // A read is not a write of the change set.
    expect((await sdk(BOB).recipe.compose(recipe, draft.id)).part).toHaveLength(3);
    expect(await versionOf(draft.id)).toBe(draft.version);

    const saved = await sdk(ADA).recipe.save(recipe, draft.id, { version: draft.version, edits });
    expect(saved.saved.cover).toHaveLength(1);
    const committed = await sdk(ADA).recipe.commit(recipe, draft.id, { version: saved.ref.version, message: 'a cover' });
    draft.version = committed.ref.version;
    expect(parseIdentityUUID(committed.commit.createdBy)).toBe(parseIdentityUUID(ada));
  });

  test('merge with a tag commits onto the primary line, and history, diff and materialize read it', async () => {
    const merged = await sdk(ANN).recipe.merge(recipe, draft.id, { version: await versionOf(main), tag: true, message: 'first edition' });
    expect(merged.conflicts).toEqual([]);
    expect(merged.ref.id).toBe(main);
    expect(merged.commit).toMatchObject({ ref: main, message: 'first edition', sequence: 1 });
    tagged = merged.commit.id;

    const history = await sdk(ANN).recipe.history(recipe, main);
    expect(history.map((commit: { id: string }) => commit.id)).toEqual([tagged]);
    expect((await sdk(ANN).recipe.history(recipe, draft.id)).length).toBe(2);

    const tree = await sdk(ANN).recipe.materialize(recipe, tagged);
    expect(tree.part.map((part: { heading: string }) => part.heading)).toEqual(['Notes', 'Intro', 'Method']);
    expect(tree.cover).toHaveLength(1);

    const changes = await sdk(ANN).recipe.diff(recipe, { from: firstCommit, to: tagged });
    expect(changes).toHaveLength(1);
    expect(changes[0]).toMatchObject({ kind: 'cover', operation: 'ADD' });
    expect(JSON.parse(changes[0].row)).toMatchObject({ photo_url: 'https://example.com/soup.jpg' });
  });

  test('release needs its own permission, a second release at the same version conflicts, and released reads the tree', async () => {
    expect(await refusal(sdk(PAT).recipe.released(recipe))).toMatchObject({ status: 404, code: 'not_found' });
    // recipes.write does not release: the operation's permissions replace the class's.
    expect((await refusal(sdk(ANN).recipe.release(recipe, { commit: tagged, version: 0 }))).status).toBe(403);
    expect(await refusal(sdk(PAT).recipe.release(recipe, { commit: firstCommit, version: 0 }))).toMatchObject({ status: 409, code: 'not_tagged' });
    const release = await sdk(PAT).recipe.release(recipe, { commit: tagged, version: 0 });
    expect(release).toMatchObject({ root: recipe, commit: tagged });
    expect(await refusal(sdk(PAT).recipe.release(recipe, { commit: tagged, version: 0 }))).toMatchObject({ status: 409, code: 'version_conflict' });

    const released = await sdk(ANN).recipe.released(recipe);
    expect(released.release).toMatchObject({ commit: tagged, version: release.version });
    expect(released.tree.part).toHaveLength(3);
    // The released table of parts holds the tree's rows (D139).
    expect((await orm.partReleased.findMany({ recipeId: { eq: recipe as never } })).rows).toHaveLength(3);
  });

  test('entity history lists the commits that changed an entity', async () => {
    const history = await sdk(ANN).recipe.entityHistory(recipe, 'part', parts[0]);
    expect(history).toHaveLength(1);
    expect(history[0]).toMatchObject({ operation: 'ADD', commit: { id: tagged, sequence: 1 } });
    expect(await refusal(sdk(ANN).recipe.entityHistory(recipe, 'sauce', parts[0]))).toMatchObject({ status: 404, code: 'not_found' });
  });

  let rebased: { id: string; version: number };

  test('a rebase that conflicts answers the conflicts and writes nothing, and a resolution settles it', async () => {
    const first = await sdk(ANN).recipe.branch(recipe, { name: 'rename-intro' });
    const second = await sdk(ANN).recipe.branch(recipe, { name: 'rename-intro-too' });
    const intro = (await sdk(ANN).recipe.compose(recipe, first.id)).part.find((part: { entityKey: string }) => part.entityKey === parts[0]);
    expect(intro.heading).toBe('Intro');

    // The first change set renames the part and reaches the primary line.
    const one = await sdk(ANN).recipe.save(recipe, first.id, { version: first.version, edits: { part: { upsert: [{ ...intro, heading: 'Introduction' }] } } });
    const oneCommitted = await sdk(ANN).recipe.commit(recipe, first.id, { version: one.ref.version });
    expect(oneCommitted.commit).toBeDefined();
    expect((await sdk(ANN).recipe.merge(recipe, first.id, { version: await versionOf(main) })).conflicts).toEqual([]);

    // The second renames it another way, and its rebase finds the conflict.
    const two = await sdk(ANN).recipe.save(recipe, second.id, { version: second.version, edits: { part: { upsert: [{ ...intro, heading: 'Preface' }] } } });
    const conflicted = await sdk(ANN).recipe.rebase(recipe, second.id, { version: two.ref.version });
    expect(conflicted.conflicts).toHaveLength(1);
    const conflict = conflicted.conflicts[0];
    expect(conflict).toMatchObject({ kind: 'part', entityKey: parts[0] });
    expect(JSON.parse(conflict.ours)).toBe('Preface');
    expect(JSON.parse(conflict.theirs)).toBe('Introduction');
    expect(JSON.parse(conflict.base)).toBe('Intro');
    expect(conflicted.commit ?? null).toBeNull();
    // Nothing was written: the ref is at the version it was.
    expect(await versionOf(second.id)).toBe(two.ref.version);

    // A resolution that names no conflict of the merge is refused.
    expect(
      await refusal(
        sdk(ANN).recipe.rebase(recipe, second.id, {
          version: two.ref.version,
          resolutions: [{ kind: 'part', entityKey: parts[1], path: conflict.path, take: 'theirs' }],
        })
      )
    ).toMatchObject({ status: 422, code: 'unmatched_resolution' });

    const settled = await sdk(ANN).recipe.rebase(recipe, second.id, {
      version: two.ref.version,
      resolutions: [{ kind: 'part', entityKey: parts[0], path: conflict.path, take: 'ours' }],
    });
    expect(settled.conflicts).toEqual([]);
    expect(settled.commit).toBeDefined();
    rebased = { id: second.id, version: settled.ref.version };
    const tree = await sdk(ANN).recipe.compose(recipe, second.id);
    expect(tree.part.find((part: { entityKey: string }) => part.entityKey === parts[0]).heading).toBe('Preface');

  });

  test("revert composes a change set to an earlier commit's tree", async () => {
    const reverted = await sdk(ANN).recipe.revert(recipe, rebased.id, { version: rebased.version, toCommit: tagged });
    expect(reverted.commit).toBeDefined();
    rebased.version = reverted.ref.version;
    const tree = await sdk(ANN).recipe.compose(recipe, rebased.id);
    expect(tree.part.find((part: { entityKey: string }) => part.entityKey === parts[0]).heading).toBe('Intro');
  });

  test('a sealed ref refuses a save, and discard frees the name', async () => {
    const sealed = await sdk(ANN).recipe.seal(recipe, rebased.id, { version: rebased.version });
    expect(sealed.ref.sealed).toBe(true);
    expect(
      await refusal(sdk(ANN).recipe.save(recipe, rebased.id, { version: sealed.ref.version, edits: { note: { upsert: [{ body: 'late' }] } } }))
    ).toMatchObject({ status: 409, code: 'ref_sealed' });

    const scratch = await sdk(ANN).recipe.branch(recipe, { name: 'scratch' });
    expect(await sdk(ANN).recipe.discard(recipe, scratch.id, { version: scratch.version })).toBe(true);
    expect(await refusal(sdk(ANN).recipe.compose(recipe, scratch.id))).toMatchObject({ status: 404, code: 'not_found' });
    expect((await sdk(ANN).recipe.branch(recipe, { name: 'scratch' })).id).not.toBe(scratch.id);
  });

  test('an input the core refuses is unprocessable', async () => {
    const ref = await sdk(ANN).recipe.branch(recipe, { name: 'reorder' });
    expect(await refusal(sdk(ANN).recipe.arrange(recipe, ref.id, { version: ref.version, kind: 'part', keys: [parts[0]] }))).toMatchObject({
      status: 422,
      code: 'invalid_arrangement',
    });
    expect(await refusal(sdk(ANN).recipe.arrange(recipe, ref.id, { version: ref.version, kind: 'step', keys: [] }))).toMatchObject({
      status: 422,
      code: 'order_not_a_key',
    });
    const unknown = parseIdentityUUID(crypto.randomUUID())!;
    expect(await refusal(sdk(ANN).recipe.save(recipe, ref.id, { version: ref.version, edits: { part: { delete: [unknown] } } }))).toMatchObject({
      status: 422,
      code: 'entity_not_found',
    });
    expect(await versionOf(ref.id)).toBe(ref.version);
  });

  test("a root, a ref or a commit that is not this root's is not found", async () => {
    const nowhere = parseIdentityUUID(crypto.randomUUID())!;
    for (const call of [
      () => sdk(ANN).recipe.branch(nowhere, { name: 'draft' }),
      () => sdk(ANN).recipe.compose(nowhere, draft.id),
      () => sdk(ANN).recipe.released(nowhere),
      () => sdk(ANN).recipe.materialize(nowhere, tagged),
    ]) {
      expect(await refusal(call())).toMatchObject({ status: 404, code: 'not_found', detail: 'Recipe not found' });
    }

    const stew = await newRecipe('Stew');
    const theirs = await sdk(ANN).recipe.branch(stew, { name: 'draft' });
    for (const call of [
      () => sdk(ANN).recipe.compose(recipe, theirs.id),
      () => sdk(ANN).recipe.history(recipe, theirs.id),
      () => sdk(ANN).recipe.save(recipe, theirs.id, { version: theirs.version, edits: {} }),
      () => sdk(ANN).recipe.branch(recipe, { from: theirs.id, name: 'crossed' }),
      () => sdk(ANN).recipe.merge(recipe, draft.id, { into: theirs.id, version: theirs.version }),
      () => sdk(ANN).recipe.compose(recipe, nowhere),
    ]) {
      expect(await refusal(call())).toMatchObject({ status: 404, code: 'not_found', detail: 'ref not found' });
    }
    // A commit of another root is not found where the path or the body
    // names it, so a caller learns nothing of another root's commits.
    for (const call of [
      () => sdk(ANN).recipe.materialize(stew, tagged),
      () => sdk(ANN).recipe.diff(stew, { from: tagged, to: tagged }),
      () => sdk(ANN).recipe.diff(recipe, { from: tagged, to: nowhere }),
      () => sdk(PAT).recipe.release(stew, { commit: tagged, version: 0 }),
      () => sdk(ANN).recipe.revert(stew, theirs.id, { version: theirs.version, toCommit: tagged }),
    ]) {
      expect(await refusal(call())).toMatchObject({ status: 404, code: 'not_found', detail: 'commit not found' });
    }
  });

  test("a caller without the route's permission is refused by the API's auth, and a write needs a caller who is a UUID", async () => {
    expect((await refusal(sdk(LEE).recipe.branch(recipe, { name: 'mine' }))).status).toBe(403);
    expect((await refusal(sdk(LEE).recipe.compose(recipe, draft.id))).status).toBe(403);
    expect((await refusal(sdk().recipe.compose(recipe, draft.id))).status).toBe(401);
    expect(await refusal(sdk(SAM).recipe.branch(recipe, { name: 'mine' }))).toMatchObject({ status: 403, code: 'no_actor' });
    expect((await sdk(SAM).recipe.compose(recipe, draft.id)).part).toHaveLength(3);
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

describe('the MCP endpoint of fixture-graph-api', () => {
  test('the MCP tools are listed, and calling one runs the operation', async () => {
    // A caller is listed the tools their permissions reach: pat may release.
    const { tools } = (await mcp(PAT, 'tools/list')).result;
    const annsTools = (await mcp(ANN, 'tools/list')).result.tools.map((tool: { name: string }) => tool.name);
    expect(annsTools).toContain('recipe_save');
    expect(annsTools).not.toContain('recipe_release');
    const names = tools.map((tool: { name: string }) => tool.name).filter((name: string) => name.startsWith('recipe_'));
    expect(names.sort()).toEqual(
      [
        'recipe_arrange',
        'recipe_branch',
        'recipe_commit',
        'recipe_compose',
        'recipe_diff',
        'recipe_discard',
        'recipe_entity_history',
        'recipe_history',
        'recipe_materialize',
        'recipe_merge',
        'recipe_rebase',
        'recipe_release',
        'recipe_released',
        'recipe_revert',
        'recipe_save',
        'recipe_seal',
      ].sort()
    );
    const save = tools.find((tool: { name: string }) => tool.name === 'recipe_save');
    expect(Object.keys(save.inputSchema.properties).sort()).toEqual(['edits', 'id', 'ref', 'version']);

    const recipe = await newRecipe('Bread');
    const branched = (await mcp(ANN, 'tools/call', { name: 'recipe_branch', arguments: { id: recipe, name: 'draft' } })).result;
    expect(branched.isError).toBeFalsy();
    expect(branched.structuredContent).toMatchObject({ root: recipe, name: 'draft' });
    const composed = (await mcp(ANN, 'tools/call', { name: 'recipe_compose', arguments: { id: recipe, ref: branched.structuredContent.id } })).result;
    expect(composed.isError).toBeFalsy();
    expect(composed.structuredContent).toMatchObject({ part: [], findings: [] });
  });
});
