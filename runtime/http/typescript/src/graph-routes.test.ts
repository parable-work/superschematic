import { describe, expect, test } from 'bun:test';
import {
  GRAPH_PRIMARY_NAME,
  graphArrange,
  graphBranch,
  graphCommit,
  graphCompose,
  graphDiff,
  graphDiscard,
  graphEntityHistory,
  graphHistory,
  graphMaterialize,
  graphMerge,
  graphRebase,
  graphRelease,
  graphReleased,
  graphRevert,
  graphSave,
  graphSeal,
  type GraphFacade,
  type GraphOperation,
  type GraphOperationName,
  type GraphRequest,
  type GraphSet,
} from './graph-routes';
import { HttpProblem, type RequestContext } from './index';

// The handlers of a generated router's graph operations (D138), over a
// fake of the repositories and of a graph's facade: the root, the ref and
// the commit each read before the facade is called, the row rule on the
// operations that write a change set, the facade's typed values to and
// from the API's rows, and each refusal answered as the problem of its
// code.

const ROOT = '11111111-1111-4111-8111-111111111111';
const OTHER_ROOT = '22222222-2222-4222-8222-222222222222';
const ANN = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa';
const BOB = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb';

const recipeGraph = {
  graph: 'Recipe',
  root: 'Recipe',
  repository: 'recipe',
  key: 'id',
  refs: 'recipeRef',
  commits: 'recipeCommit',
  kinds: [
    { kind: 'step', member: 'step', fields: ['position', 'instruction'] },
    { kind: 'line_item', member: 'lineItem', fields: ['quantity', 'productId'], relations: [{ member: 'productId', field: 'product', key: 'sku' }] },
  ],
} as const satisfies GraphSet;

/** The operation name of the graph, under the row rule when owned. */
function operation(name: GraphOperationName, owned = false): GraphOperation {
  return { graph: recipeGraph, operation: name, ...(owned ? { by: ['createdBy'], overridePermission: 'recipes.admin' } : {}) };
}

type Row = Record<string, unknown>;
type Filter = Record<string, any>;

/** Whether a row meets a filter of eq, isNull and anyOf members. */
function matches(row: Row, filter: Filter): boolean {
  return Object.entries(filter).every(([member, condition]) => {
    if (member === 'anyOf') return (condition as Filter[]).some(each => matches(row, each));
    if ('isNull' in condition) return (row[member] === null) === condition.isNull;
    return row[member] === condition.eq;
  });
}

/** Repositories over rows, each a table of live rows, with the filters each was asked. */
function repositories(tables: Record<string, Row[]>) {
  const asked: { table: string; filter: Filter }[] = [];
  const orm: Record<string, { findOne(filter: Filter): Promise<Row | null> }> = {};
  for (const [table, rows] of Object.entries(tables)) {
    orm[table] = {
      findOne: async filter => {
        asked.push({ table, filter });
        return rows.find(row => matches(row, filter)) ?? null;
      },
    };
  }
  return { orm, asked };
}

/** A facade that records each call, by actor, and answers what answers gives the method. */
function facade(answers: Partial<Record<keyof GraphFacade, (...args: any[]) => unknown>> = {}) {
  const calls: { actor: string | null; method: string; args: unknown[] }[] = [];
  const as = (actor: string | null): GraphFacade =>
    new Proxy({} as GraphFacade, {
      get: (_target, method: string) => {
        if (method === 'withActor') return (next: string) => as(next);
        return async (...args: unknown[]) => {
          calls.push({ actor, method, args });
          return answers[method as keyof GraphFacade]?.(...args);
        };
      },
    });
  return { graph: as(null), calls };
}

function context(subject?: string, permissions: string[] = []): RequestContext {
  return { principal: subject === undefined ? null : { subject, permissions }, serviceCaller: null } as RequestContext;
}

function request(path: Record<string, unknown>, input?: unknown, query: Record<string, unknown> = {}): GraphRequest {
  return { path: { id: ROOT, ...path }, query, ...(input !== undefined ? { input } : {}) };
}

/** A database of one recipe with a primary line, a change set ann made and a commit, and another recipe's ref and commit. */
function database() {
  return repositories({
    recipe: [{ id: ROOT }, { id: OTHER_ROOT }],
    recipeRef: [
      { id: 'main', rootId: ROOT, parentRefId: null, createdBy: ANN },
      { id: 'draft', rootId: ROOT, parentRefId: 'main', createdBy: ANN },
      { id: 'theirs', rootId: OTHER_ROOT, parentRefId: null, createdBy: ANN },
    ],
    recipeCommit: [
      { id: 'c1', rootId: ROOT },
      { id: 'c9', rootId: OTHER_ROOT },
    ],
  });
}

/** What a handler's promise rejects with. */
async function thrown(run: Promise<unknown>): Promise<unknown> {
  try {
    await run;
  } catch (error) {
    return error;
  }
  throw new Error('the handler resolved');
}

/** The status, code and detail a handler's promise is refused with. */
async function refused(run: Promise<unknown>): Promise<{ status: number; code?: string; detail: string; details?: unknown }> {
  const error = await thrown(run);
  expect(error).toBeInstanceOf(HttpProblem);
  const problem = error as HttpProblem;
  return { status: problem.status, code: problem.code, detail: problem.message, ...(problem.details !== undefined ? { details: problem.details } : {}) };
}

describe('the root', () => {
  test('a key that holds no live row, a soft-deleted one among them, is not found before the facade is called', async () => {
    // The repository's findOne answers no soft-deleted row, so one is the row that is not there.
    const { orm } = repositories({ recipe: [], recipeRef: [{ id: 'draft', rootId: ROOT, parentRefId: 'main', createdBy: ANN }], recipeCommit: [] });
    const { graph, calls } = facade();
    for (const run of [
      graphBranch(orm, graph, operation('branch'), context(ANN), request({}, { name: 'draft' })),
      graphCompose(orm, graph, operation('compose'), context(ANN), request({ ref: 'draft' })),
      graphReleased(orm, graph, operation('released'), context(ANN), request({})),
      graphRelease(orm, graph, operation('release'), context(ANN), request({}, { commit: 'c1', version: 0 })),
      graphEntityHistory(orm, graph, operation('entityHistory'), context(ANN), request({ kind: 'step', key: 'k' })),
    ]) {
      expect(await refused(run)).toEqual({ status: 404, code: 'not_found', detail: 'Recipe not found' });
    }
    expect(calls).toEqual([]);
  });

  test('the root is read by the key the path names', async () => {
    const { orm, asked } = database();
    const { graph } = facade({ released: () => ({ release: { id: 'r' }, tree: {} }) });
    await graphReleased(orm, graph, operation('released'), context(ANN), request({}));
    expect(asked).toEqual([{ table: 'recipe', filter: { id: { eq: ROOT } } }]);
  });
});

describe('a ref and a commit', () => {
  test("a ref that does not exist, and one of another root, is not found", async () => {
    const { orm, asked } = database();
    const { graph, calls } = facade();
    for (const ref of ['nowhere', 'theirs']) {
      for (const run of [
        graphCompose(orm, graph, operation('compose'), context(ANN), request({ ref })),
        graphHistory(orm, graph, operation('history'), context(ANN), request({ ref })),
        graphSave(orm, graph, operation('save', true), context(ANN), request({ ref }, { version: 1, edits: {} })),
        graphMerge(orm, graph, operation('merge'), context(ANN), request({ ref }, { version: 1 })),
        graphRevert(orm, graph, operation('revert'), context(ANN), request({ ref }, { version: 1, toCommit: 'c1' })),
      ]) {
        expect(await refused(run)).toEqual({ status: 404, code: 'not_found', detail: 'ref not found' });
      }
    }
    expect(calls).toEqual([]);
    expect(asked.find(each => each.table === 'recipeRef')?.filter).toEqual({ id: { eq: 'nowhere' }, rootId: { eq: ROOT } });
  });

  test("a ref the body names is held to the root too", async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    expect(await refused(graphBranch(orm, graph, operation('branch'), context(ANN), request({}, { from: 'theirs', name: 'x' })))).toMatchObject({ status: 404, detail: 'ref not found' });
    expect(await refused(graphMerge(orm, graph, operation('merge'), context(ANN), request({ ref: 'draft' }, { into: 'theirs', version: 1 })))).toMatchObject({ status: 404, detail: 'ref not found' });
    expect(calls).toEqual([]);
  });

  test("a commit that does not exist, and one of another root, is not found", async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    for (const run of [
      graphMaterialize(orm, graph, operation('materialize'), context(ANN), request({ commit: 'c9' })),
      graphMaterialize(orm, graph, operation('materialize'), context(ANN), request({ commit: 'nowhere' })),
      graphDiff(orm, graph, operation('diff'), context(ANN), request({}, undefined, { from: 'c9', to: 'c1' })),
      graphDiff(orm, graph, operation('diff'), context(ANN), request({}, undefined, { from: 'c1', to: 'c9' })),
    ]) {
      expect(await refused(run)).toEqual({ status: 404, code: 'not_found', detail: 'commit not found' });
    }
    expect(calls).toEqual([]);
  });
});

describe('the row rule', () => {
  const owned: [GraphOperationName, (...args: Parameters<typeof graphSave>) => Promise<unknown>, unknown][] = [
    ['save', graphSave, { version: 1, edits: {} }],
    ['arrange', graphArrange, { version: 1, kind: 'step', keys: [] }],
    ['commit', graphCommit, { version: 1 }],
    ['seal', graphSeal, { version: 1 }],
    ['rebase', graphRebase, { version: 1 }],
    ['discard', graphDiscard, { version: 1 }],
  ];

  test("an operation that writes a change set reaches the caller's own, and another caller's is not found", async () => {
    for (const [name, handler, body] of owned) {
      const { orm, asked } = database();
      const { graph, calls } = facade({ save: () => ({ ref: {}, saved: {} }), arrange: () => ({ ref: {}, saved: {} }) });
      expect(await refused(handler(orm, graph, operation(name, true), context(BOB, ['recipes.write']), request({ ref: 'draft' }, body)))).toEqual({
        status: 404,
        code: 'not_found',
        detail: 'ref not found',
      });
      expect(calls).toEqual([]);
      expect(asked.at(-1)?.filter).toEqual({ id: { eq: 'draft' }, rootId: { eq: ROOT }, anyOf: [{ createdBy: { eq: BOB } }] });

      await handler(orm, graph, operation(name, true), context(ANN), request({ ref: 'draft' }, body));
      expect(calls.map(call => [call.actor, call.method])).toEqual([[ANN, name]]);
    }
  });

  test('a holder of the override permission reaches every change set, and one of a permission above it too', async () => {
    for (const permissions of [['recipes.admin'], ['recipes']]) {
      const { orm, asked } = database();
      const { graph, calls } = facade();
      await graphCommit(orm, graph, operation('commit', true), context(BOB, permissions), request({ ref: 'draft' }, { version: 3 }));
      expect(calls).toEqual([{ actor: BOB, method: 'commit', args: ['draft', 3, {}] }]);
      expect(asked.at(-1)?.filter).toEqual({ id: { eq: 'draft' }, rootId: { eq: ROOT } });
    }
  });

  test('a request with no caller, or one whose subject is no UUID, reaches none', async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    for (const ctx of [context(), context('sam@kitchen', ['recipes.write'])]) {
      expect(await refused(graphSave(orm, graph, operation('save', true), ctx, request({ ref: 'draft' }, { version: 1, edits: {} })))).toMatchObject({ status: 404 });
    }
    expect(calls).toEqual([]);
  });

  test('an operation with no rule, and one that reads, reaches every ref of the root', async () => {
    const { orm } = database();
    const { graph, calls } = facade({ compose: () => ({ contentHash: 'h', findings: [] }) });
    await graphSave(orm, graph, operation('save'), context(BOB), request({ ref: 'draft' }, { version: 1, edits: {} })).catch(() => undefined);
    await graphCompose(orm, graph, { ...operation('compose'), by: ['createdBy'] }, context(BOB), request({ ref: 'draft' }));
    expect(calls.map(call => call.method)).toEqual(['save', 'compose']);
    // A revert writes the change set, so the rule holds it too.
    expect(
      await refused(graphRevert(orm, graph, { ...operation('revert'), by: ['createdBy'] }, context(BOB), request({ ref: 'draft' }, { version: 1, toCommit: 'c1' }))),
    ).toMatchObject({ status: 404 });
    expect(calls.map(call => call.method)).toEqual(['save', 'compose']);
  });
});

describe('the caller of a write', () => {
  test('a write runs as the caller, and a read as no one', async () => {
    const { orm } = database();
    const { graph, calls } = facade({ released: () => ({ release: {}, tree: {} }) });
    await graphRelease(orm, graph, operation('release'), context(ANN), request({}, { commit: 'c1', version: 0 }));
    await graphReleased(orm, graph, operation('released'), context(ANN), request({}));
    await graphHistory(orm, graph, operation('history'), context(), request({ ref: 'draft' }));
    expect(calls).toEqual([
      { actor: ANN, method: 'release', args: [ROOT, 'c1', 0] },
      { actor: null, method: 'released', args: [ROOT] },
      { actor: null, method: 'history', args: ['draft'] },
    ]);
  });

  test('a write with no caller, or one whose subject is no UUID, is 403 no_actor', async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    for (const ctx of [context(), context('sam@kitchen', ['recipes.write'])]) {
      expect(await refused(graphRelease(orm, graph, operation('release'), ctx, request({}, { commit: 'c1', version: 0 })))).toMatchObject({ status: 403, code: 'no_actor' });
      expect(await refused(graphBranch(orm, graph, operation('branch'), ctx, request({}, { name: 'x' })))).toMatchObject({ status: 403, code: 'no_actor' });
    }
    expect(calls).toEqual([]);
  });
});

describe('branch', () => {
  test("a branch with no from branches the root's primary line", async () => {
    const { orm } = database();
    const { graph, calls } = facade({ branch: () => ({ id: 'new' }) });
    expect(await graphBranch(orm, graph, operation('branch'), context(BOB), request({}, { name: 'feature', from: null }))).toEqual({ id: 'new' });
    expect(calls).toEqual([{ actor: BOB, method: 'branch', args: ['main', 'feature'] }]);
  });

  test('a branch of the ref the body names', async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    await graphBranch(orm, graph, operation('branch'), context(BOB), request({}, { from: 'draft', name: 'feature' }));
    expect(calls).toEqual([{ actor: BOB, method: 'branch', args: ['draft', 'feature'] }]);
  });

  test('a root with no primary line gets one named main first, as the caller', async () => {
    const refs: Row[] = [];
    const { orm } = repositories({ recipe: [{ id: ROOT }], recipeRef: refs, recipeCommit: [] });
    const { graph, calls } = facade({
      createPrimary: (root: string, name: string) => {
        refs.push({ id: 'made', rootId: root, parentRefId: null, name });
        return { id: 'made' };
      },
    });
    await graphBranch(orm, graph, operation('branch'), context(ANN), request({}, { name: 'draft' }));
    expect(GRAPH_PRIMARY_NAME).toBe('main');
    expect(calls).toEqual([
      { actor: ANN, method: 'createPrimary', args: [ROOT, 'main'] },
      { actor: ANN, method: 'branch', args: ['made', 'draft'] },
    ]);
  });

  test('when another request made the primary line meanwhile, that one is branched', async () => {
    const refs: Row[] = [];
    const { orm } = repositories({ recipe: [{ id: ROOT }], recipeRef: refs, recipeCommit: [] });
    const { graph, calls } = facade({
      createPrimary: () => {
        refs.push({ id: 'raced', rootId: ROOT, parentRefId: null, name: 'main' });
        throw Object.assign(new Error('the root already has a live ref of that name'), { code: 'name_taken', name: 'NameTakenError' });
      },
    });
    await graphBranch(orm, graph, operation('branch'), context(ANN), request({}, { name: 'draft' }));
    expect(calls.at(-1)).toEqual({ actor: ANN, method: 'branch', args: ['raced', 'draft'] });
  });

  test('any other failure to make the primary line is the refusal it is', async () => {
    const { orm } = repositories({ recipe: [{ id: ROOT }], recipeRef: [], recipeCommit: [] });
    const { graph } = facade({
      createPrimary: () => {
        throw Object.assign(new Error('the commit is from a newer schema epoch'), { code: 'schema_epoch' });
      },
    });
    expect(await refused(graphBranch(orm, graph, operation('branch'), context(ANN), request({}, { name: 'draft' })))).toMatchObject({ status: 409, code: 'schema_epoch' });
  });
});

describe('rows', () => {
  test("a save hands the facade each kind's typed values, a relation's key as the object that holds it", async () => {
    const { orm } = database();
    const { graph, calls } = facade({
      save: () => ({
        ref: { id: 'draft', version: 2 },
        saved: {
          step: [{ id: 'row', entityKey: 's1', position: 1, instruction: 'Boil', recipe: { id: ROOT }, ref: { id: 'draft' }, deletedOnRef: false, _version: 1 }],
          lineItem: [{ entityKey: 'l1', quantity: 2, product: { sku: 'P-1' } }],
          contentHash: '',
          findings: [],
        },
      }),
    });
    const result = await graphSave(
      orm,
      graph,
      operation('save', true),
      context(ANN),
      request(
        { ref: 'draft' },
        {
          version: 1,
          edits: {
            step: { upsert: [{ position: 1, instruction: 'Boil', entityKey: null }], delete: null, unset: ['s9'] },
            lineItem: { upsert: [{ entityKey: 'l1', quantity: 2, productId: 'P-1' }, { quantity: 1, productId: null }] },
            sauce: { upsert: [{ name: 'no such kind' }] },
          },
        }
      )
    );
    expect(calls).toEqual([
      {
        actor: ANN,
        method: 'save',
        args: [
          'draft',
          1,
          {
            step: { upsert: [{ position: 1, instruction: 'Boil' }], delete: [], unset: ['s9'] },
            lineItem: { upsert: [{ entityKey: 'l1', quantity: 2, product: { sku: 'P-1' } }, { quantity: 1, product: null }], delete: [], unset: [] },
          },
        ],
      },
    ]);
    // The rows written, as the API's row types list them: no hash and no findings.
    expect(result).toEqual({
      ref: { id: 'draft', version: 2 },
      saved: { step: [{ entityKey: 's1', position: 1, instruction: 'Boil' }], lineItem: [{ entityKey: 'l1', quantity: 2, productId: 'P-1' }] },
    });
  });

  test("a tree holds each kind's rows, its hash and its findings", async () => {
    const { orm } = database();
    const finding = { code: 'missing_parent', kind: 'step', entityKey: 's1', message: 'no parent' };
    const tree = { step: [{ entityKey: 's1', position: 1, instruction: 'Boil', _version: 4 }], contentHash: 'abc', findings: [finding] };
    const { graph } = facade({ compose: () => tree, materialize: () => ({ ...tree, findings: [] }), released: () => ({ release: { id: 'r', version: 1 }, tree }) });
    const rows = { step: [{ entityKey: 's1', position: 1, instruction: 'Boil' }], lineItem: [] };
    expect(await graphCompose(orm, graph, operation('compose'), context(ANN), request({ ref: 'draft' }))).toEqual({ ...rows, contentHash: 'abc', findings: [finding] });
    expect(await graphMaterialize(orm, graph, operation('materialize'), context(ANN), request({ commit: 'c1' }))).toEqual({ ...rows, contentHash: 'abc', findings: [] });
    expect(await graphReleased(orm, graph, operation('released'), context(ANN), request({}))).toEqual({
      release: { id: 'r', version: 1 },
      tree: { ...rows, contentHash: 'abc', findings: [finding] },
    });
  });

  test('an arrange names the kind as the facade does, and no parent as null', async () => {
    const { orm } = database();
    const { graph, calls } = facade({ arrange: () => ({ ref: { version: 2 }, saved: { lineItem: [{ entityKey: 'l2', quantity: 1, product: null }] } }) });
    const result = await graphArrange(orm, graph, operation('arrange', true), context(ANN), request({ ref: 'draft' }, { version: 1, kind: 'line_item', keys: ['l2', 'l1'] }));
    expect(calls).toEqual([{ actor: ANN, method: 'arrange', args: ['draft', 1, 'lineItem', null, ['l2', 'l1']] }]);
    expect(result).toEqual({ ref: { version: 2 }, saved: { step: [], lineItem: [{ entityKey: 'l2', quantity: 1, productId: null }] } });
    expect(await refused(graphArrange(orm, graph, operation('arrange', true), context(ANN), request({ ref: 'draft' }, { version: 1, kind: 'sauce', keys: [] })))).toMatchObject({
      status: 422,
      code: 'unknown_kind',
    });
  });
});

describe('the other operations', () => {
  test("a commit's, a seal's, a revert's and a discard's arguments", async () => {
    const { orm } = database();
    const { graph, calls } = facade();
    await graphCommit(orm, graph, operation('commit'), context(ANN), request({ ref: 'draft' }, { version: 2, message: 'm', tag: true }));
    await graphCommit(orm, graph, operation('commit'), context(ANN), request({ ref: 'draft' }, { version: 2, message: null, tag: null }));
    await graphSeal(orm, graph, operation('seal'), context(ANN), request({ ref: 'draft' }, { version: 3 }));
    await graphRevert(orm, graph, operation('revert'), context(ANN), request({ ref: 'draft' }, { version: 4, toCommit: 'c1' }));
    expect(await graphDiscard(orm, graph, operation('discard'), context(ANN), request({ ref: 'draft' }, { version: 5 }))).toBe(true);
    expect(calls.map(call => [call.method, call.args])).toEqual([
      ['commit', ['draft', 2, { message: 'm', tag: true }]],
      ['commit', ['draft', 2, {}]],
      ['seal', ['draft', 3]],
      ['revert', ['draft', 4, 'c1']],
      ['discard', ['draft', 5]],
    ]);
  });

  test("a merge is into the source's parent unless the body names a ref, and its conflicts are answered", async () => {
    const { orm } = database();
    const conflicts = [{ kind: 'step', entityKey: 's1', path: 'instruction', base: '"a"', ours: '"b"', theirs: '"c"' }];
    const { graph, calls } = facade({ merge: () => ({ ref: { id: 'main', version: 7 }, commit: null, conflicts }) });
    const resolutions = [
      { kind: 'step', entityKey: 's1', path: 'instruction', take: 'ours', value: null },
      { kind: 'step', entityKey: 's2', path: 'instruction', take: null, value: '"x"' },
    ];
    expect(await graphMerge(orm, graph, operation('merge'), context(BOB), request({ ref: 'draft' }, { version: 7, resolutions, message: 'm' }))).toEqual({
      ref: { id: 'main', version: 7 },
      commit: null,
      conflicts,
    });
    await graphMerge(orm, graph, operation('merge'), context(BOB), request({ ref: 'draft' }, { into: 'main', version: 7, tag: true }));
    expect(calls).toEqual([
      {
        actor: BOB,
        method: 'merge',
        args: [
          'draft',
          'main',
          7,
          [
            { kind: 'step', entityKey: 's1', path: 'instruction', take: 'ours' },
            { kind: 'step', entityKey: 's2', path: 'instruction', value: '"x"' },
          ],
          { message: 'm' },
        ],
      },
      { actor: BOB, method: 'merge', args: ['draft', 'main', 7, [], { tag: true }] },
    ]);
    // A primary line has no parent to merge into.
    expect(await refused(graphMerge(orm, graph, operation('merge'), context(BOB), request({ ref: 'main' }, { version: 1 })))).toMatchObject({ status: 409, code: 'no_parent' });
  });

  test("a rebase's resolutions, a diff's commits and an entity's history", async () => {
    const { orm } = database();
    const { graph, calls } = facade({ diff: () => [{ kind: 'step', entityKey: 's1', operation: 'ADD' }], entityHistory: () => [] });
    await graphRebase(orm, graph, operation('rebase'), context(ANN), request({ ref: 'draft' }, { version: 2 }));
    expect(await graphDiff(orm, graph, operation('diff'), context(ANN), request({}, undefined, { from: 'c1', to: 'c1' }))).toEqual([{ kind: 'step', entityKey: 's1', operation: 'ADD' }]);
    expect(await graphEntityHistory(orm, graph, operation('entityHistory'), context(ANN), request({ kind: 'line_item', key: 'l1' }))).toEqual([]);
    expect(calls.map(call => [call.method, call.args])).toEqual([
      ['rebase', ['draft', 2, []]],
      ['diff', ['c1', 'c1']],
      ['entityHistory', [ROOT, 'line_item', 'l1']],
    ]);
    expect(await refused(graphEntityHistory(orm, graph, operation('entityHistory'), context(ANN), request({ kind: 'lineItem', key: 'l1' })))).toEqual({
      status: 404,
      code: 'not_found',
      detail: 'the Recipe graph has no kind lineItem',
    });
  });
});

describe('refusals', () => {
  /** What a commit is refused with when the facade throws error. */
  async function commitThrowing(error: unknown) {
    const { orm } = database();
    const { graph } = facade({
      commit: () => {
        throw error;
      },
    });
    return thrown(graphCommit(orm, graph, operation('commit'), context(ANN), request({ ref: 'draft' }, { version: 1 })));
  }

  test('each refusal of the engine and the core is the problem of its code', async () => {
    const statuses: Record<string, number> = {
      not_found: 404,
      version_conflict: 409,
      ref_sealed: 409,
      primary_merge_only: 409,
      schema_epoch: 409,
      name_taken: 409,
      nothing_to_commit: 409,
      not_tagged: 409,
      no_parent: 409,
      merge_into_itself: 409,
      root_mismatch: 409,
      entity_not_found: 422,
      invalid_row: 422,
      invalid_arrangement: 422,
      order_not_a_key: 422,
      unmatched_resolution: 422,
      invalid_resolution: 422,
      duplicate_entity_key: 422,
      no_order_key: 422,
      no_actor: 403,
    };
    for (const [code, status] of Object.entries(statuses)) {
      const cause = Object.assign(new Error(`the engine says ${code}`), { code });
      const problem = (await commitThrowing(cause)) as HttpProblem;
      expect(problem).toBeInstanceOf(HttpProblem);
      expect({ status: problem.status, code: problem.code, detail: problem.message, details: problem.details }).toEqual({
        status,
        code,
        detail: `the engine says ${code}`,
        details: undefined,
      });
      expect(problem.cause).toBe(cause);
    }
  });

  test("an invalid tree carries the core's findings", async () => {
    const findings = [{ code: 'missing_parent', kind: 'step', entityKey: 's1', message: 'step s1 names a parent that is absent' }];
    const problem = (await commitThrowing(Object.assign(new Error("the tree breaks the graph's rules"), { code: 'invalid_tree', findings }))) as HttpProblem;
    expect({ status: problem.status, code: problem.code, details: problem.details }).toEqual({ status: 422, code: 'invalid_tree', details: { findings } });
  });

  test('a refusal an error is caused by, and a value a canonical rule refuses', async () => {
    const wrapped = new Error('save failed', { cause: Object.assign(new Error('the ref is sealed'), { code: 'ref_sealed' }) });
    expect(((await commitThrowing(wrapped)) as HttpProblem).code).toBe('ref_sealed');
    const canonical = Object.assign(new Error('canonical: column position (integer): not an integer'), { name: 'CanonicalError', column: 'position', class: 'integer', detail: 'not an integer' });
    const problem = (await commitThrowing(canonical)) as HttpProblem;
    expect({ status: problem.status, code: problem.code, detail: problem.message }).toEqual({
      status: 422,
      code: 'invalid_row',
      detail: 'canonical: column position (integer): not an integer',
    });
  });

  test("any other error is rethrown as it is, the server's own to answer", async () => {
    for (const error of [
      new Error('connection refused'),
      Object.assign(new Error('duplicate key value violates unique constraint'), { code: '23505' }),
      Object.assign(new Error('the commit walk passed its ceiling'), { code: 'walk_ceiling' }),
      Object.assign(new Error('a row version a commit names is missing from history'), { code: 'history_missing' }),
      Object.assign(new Error('core: bad descriptor'), { code: 'invalid_descriptor' }),
      Object.assign(new Error('core panicked'), { code: 'internal' }),
    ]) {
      const caught = await commitThrowing(error);
      expect(caught).toBe(error);
      expect(caught).not.toBeInstanceOf(HttpProblem);
    }
  });

  test('repositories that hold no table of the graph are the generator\'s error', async () => {
    const { graph } = facade();
    const caught = (await thrown(graphReleased({}, graph, operation('released'), context(ANN), request({})))) as Error;
    expect(caught).not.toBeInstanceOf(HttpProblem);
    expect(caught.message).toBe("graph operation: the repositories hold no recipe, a table of the Recipe graph; rebuild the database's types");
  });
});
