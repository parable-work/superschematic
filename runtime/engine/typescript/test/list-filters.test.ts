// A list's where: equality on the instance type's own fields, by key, and
// on a field a behavior lets a list filter on, or a member of one, by its
// qualified name (Workflow.status), a list of values, null for no value,
// paging with the cursor in creation order, a page read through an index,
// a page that scans a bounded number of instances when none serves it,
// and what where refuses.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, FILTER_SCAN_ROWS, defineBehavior, type BehaviorDeclaration, type Engine, type ListOptions } from '../dist/index.js';
import { counter, openMetaSchema, publishItem } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const WORKFLOW = {
  name: 'Workflow',
  config: {
    states: ['todo', 'doing', 'done'],
    transitions: [
      { from: 'todo', to: 'doing' },
      { from: 'doing', to: 'done' },
    ],
  },
};

// test.Tag gives an instance an owner, or none, in a column the tag
// field's owner member reads, which a list filters on as
// test.Tag.tag.owner through an index on the column.
const tagDeclaration: BehaviorDeclaration = {
  name: 'test.Tag',
  description: 'Tags an instance with an owner, or none.',
  fields: [{ name: 'tag', description: 'The tag, { owner }.' }],
  operations: [
    {
      name: 'own',
      description: 'Sets the owner, or clears it with null.',
      paramsSchema: { type: 'object', additionalProperties: false, required: ['owner'], properties: { owner: { type: ['string', 'null'] } } },
      resultSchema: { type: 'object', additionalProperties: false },
      writes: true,
    },
  ],
};

const tag = defineBehavior({
  declaration: tagDeclaration,
  migrations: [{ version: 1, name: 'tag', columns: { owner: { type: 'text' } }, indexes: { owner: ['owner'] } }],
  filters: { 'tag.owner': { column: 'owner', type: 'string', description: 'Who owns it; null for no one.' } },
  operations: {
    own(context, params) {
      context.columns.set({ owner: (params.owner as string | null) ?? null });
      return {};
    },
  },
  fields: { tag: (view) => ({ owner: view.columns.get().owner ?? null }) },
});

function jobDocument(): Record<string, unknown> {
  const document = schemaDocument(
    'Job',
    [
      { name: 'slug', typeRef: { name: 'string' }, unique: true },
      { name: 'kind', typeRef: { name: 'string' } },
      { name: 'rank', typeRef: { name: 'Generic.Int64' } },
      { name: 'urgent', typeRef: { name: 'boolean' } },
      { name: 'tags', typeRef: { name: 'string', isArray: true } },
      { name: 'owner', typeRef: { name: 'Owner' } },
    ],
    { types: { Owner: { name: 'Owner', role: 'EmbeddedStruct', fields: [{ name: 'name', typeRef: { name: 'string' } }] } } }
  ) as { types: { Job: Record<string, unknown> } };
  document.types.Job.behaviors = [WORKFLOW];
  return document as unknown as Record<string, unknown>;
}

function open(driver: (typeof drivers)[number]): Engine {
  const engine = openTestEngine({ driver });
  engine.schemas.define(alice, jobDocument());
  engine.schemas.publish(alice, 'Job');
  return engine;
}

// every reads every page of a filter from the start and returns the ids
// in order, with how many items each page held.
function every(engine: Engine, options: ListOptions, schema = 'Job'): { ids: string[]; pages: number[] } {
  const ids: string[] = [];
  const pages: number[] = [];
  let cursor: string | undefined;
  do {
    const page = engine.instances.list(alice, schema, { ...options, ...(cursor === undefined ? {} : { cursor }) });
    ids.push(...page.items.map((item) => item.id));
    pages.push(page.items.length);
    cursor = page.next ?? undefined;
  } while (cursor !== undefined);
  return { ids, pages };
}

// many creates count jobs in one transaction, each from make.
function many(engine: Engine, count: number, make: (index: number) => Record<string, unknown>, status?: (index: number) => string | undefined): void {
  engine.storage.transaction(() => {
    for (let index = 0; index < count; index += 1) {
      const id = `j${String(index).padStart(5, '0')}`;
      engine.instances.create(alice, 'Job', make(index), { id });
      const to = status?.(index);
      if (to === 'doing' || to === 'done') {
        engine.instances.invoke(alice, 'Job', id, 'transition', { to: 'doing' });
      }
      if (to === 'done') {
        engine.instances.invoke(alice, 'Job', id, 'transition', { to: 'done' });
      }
    }
  });
}

for (const driver of drivers) {
  describe(`list filters (${driver})`, () => {
    test('where keeps the instances whose fields hold its values, a list meaning any, every member together', () => {
      const engine = open(driver);
      many(
        engine,
        8,
        (index) => ({ slug: `org/job-${index}`, kind: index % 2 === 0 ? 'build' : 'test', rank: index % 3, urgent: index < 2 }),
        (index) => (index % 4 === 1 ? 'doing' : index % 4 === 3 ? 'done' : undefined)
      );
      const ids = (where: Record<string, unknown>): string[] => every(engine, { where }).ids;
      assert.deepEqual(ids({ kind: 'build' }), ['j00000', 'j00002', 'j00004', 'j00006']);
      assert.deepEqual(ids({ kind: ['test'], rank: 0 }), ['j00003']);
      assert.deepEqual(ids({ rank: [2, 0] }), ['j00000', 'j00002', 'j00003', 'j00005', 'j00006']);
      assert.deepEqual(ids({ urgent: true }), ['j00000', 'j00001']);
      assert.deepEqual(ids({ urgent: false, kind: 'build' }), ['j00002', 'j00004', 'j00006']);
      assert.deepEqual(ids({ 'Workflow.status': 'doing' }), ['j00001', 'j00005']);
      assert.deepEqual(ids({ 'Workflow.status': ['done', 'todo'] }), ['j00000', 'j00002', 'j00003', 'j00004', 'j00006', 'j00007']);
      assert.deepEqual(ids({ 'Workflow.status': 'todo', kind: 'build' }), ['j00000', 'j00002', 'j00004', 'j00006']);
      assert.deepEqual(ids({ slug: ['org/job-6', 'org/job-1', 'org/missing'] }), ['j00001', 'j00006']);
      assert.deepEqual(ids({ slug: 'org/job-1', 'Workflow.status': 'todo' }), []);
      assert.deepEqual(ids({ kind: 'deploy' }), []);
      // An empty where, or none, lists every instance.
      assert.equal(every(engine, { where: {} }).ids.length, 8);
      // valueRefs and the rest of a read work as an unfiltered list's.
      const page = engine.instances.list(alice, 'Job', { where: { 'Workflow.status': 'doing' }, valueRefs: true });
      assert.deepEqual([page.items[0].data, page.items[0].behaviors], [{ slug: 'org/job-1', kind: 'test', rank: 1, urgent: true }, { Workflow: { status: 'doing' } }]);
    });

    test('pages keep creation order across the cursor, a list of values merged', () => {
      const engine = open(driver);
      many(engine, 30, (index) => ({ slug: `s${index}`, kind: ['a', 'b', 'c'][index % 3] }), (index) => ['todo', 'doing', 'done'][index % 3]);
      const kinds = every(engine, { where: { kind: ['c', 'a'] }, limit: 4 });
      const expected = Array.from({ length: 30 }, (_, index) => index)
        .filter((index) => index % 3 !== 1)
        .map((index) => `j${String(index).padStart(5, '0')}`);
      assert.deepEqual(kinds.ids, expected);
      assert.deepEqual(kinds.pages, [4, 4, 4, 4, 4]);
      // Through Workflow's index, one range read per state.
      const states = every(engine, { where: { 'Workflow.status': ['done', 'todo'] }, limit: 4 });
      assert.deepEqual(states.ids, expected);
      assert.deepEqual(states.pages, [4, 4, 4, 4, 4]);
      // An instance created behind the cursor moves nothing between pages.
      const first = engine.instances.list(alice, 'Job', { where: { 'Workflow.status': 'todo' }, limit: 2 });
      engine.instances.create(alice, 'Job', { slug: 'late' }, { id: 'late' });
      const rest = every(engine, { where: { 'Workflow.status': 'todo' }, cursor: first.next as string });
      assert.deepEqual([...first.items.map((item) => item.id), ...rest.ids].slice(-2), ['j00027', 'late']);
    });

    test("a field an index serves reads only the instances that hold its values; another field's page scans a bounded number", () => {
      const engine = open(driver);
      const total = FILTER_SCAN_ROWS * 2 + 500;
      const rare = new Set([10, FILTER_SCAN_ROWS + 500, FILTER_SCAN_ROWS * 2 + 400]);
      many(
        engine,
        total,
        (index) => ({ slug: `s${index}`, kind: rare.has(index) ? 'rare' : 'common' }),
        (index) => (rare.has(index) ? 'doing' : undefined)
      );
      const ids = [...rare].map((index) => `j${String(index).padStart(5, '0')}`);
      // Workflow's index on its status reads the three in one page.
      assert.deepEqual(every(engine, { where: { 'Workflow.status': 'doing' } }), { ids, pages: [3] });
      // So does the unique index on slug, a combination at a time.
      assert.deepEqual(every(engine, { where: { slug: [...rare].map((index) => `s${index}`) } }), { ids, pages: [3] });
      // No index serves kind: each page reads FILTER_SCAN_ROWS instances
      // past its cursor, and keeps the ones that match.
      assert.deepEqual(every(engine, { where: { kind: 'rare' } }), { ids, pages: [1, 1, 1] });
      assert.deepEqual(every(engine, { where: { kind: 'none' } }), { ids: [], pages: [0, 0, 0] });
      // A member no index serves beside one an index does is tested on the
      // rows the index reads.
      assert.deepEqual(every(engine, { where: { 'Workflow.status': 'doing', kind: 'rare', slug: `s${FILTER_SCAN_ROWS + 500}` } }).ids, [ids[1]]);
    });

    test("a behavior's filter with no index of its own reads as an own field's does", () => {
      const engine = openTestEngine({
        driver,
        metaSchema: openMetaSchema(),
        behaviors: [{ ...counter, filters: { count: { column: 'count', type: 'integer' } } }],
      });
      publishItem(engine, [{ name: 'test.Counter' }]);
      for (const [id, by] of [['a', 2], ['b', 1], ['c', 2]] as const) {
        engine.instances.create(alice, 'Item', { title: id }, { id });
        engine.instances.invoke(alice, 'Item', id, 'increment', { by });
      }
      const ids = (where: Record<string, unknown>): string[] => engine.instances.list(alice, 'Item', { where }).items.map((item) => item.id);
      assert.deepEqual(ids({ 'test.Counter.count': 2 }), ['a', 'c']);
      assert.deepEqual(ids({ 'test.Counter.count': [1, 3], title: ['a', 'b'] }), ['b']);
      assert.match(thrown(() => ids({ 'test.Counter.count': 'two' }), EngineError).message, /where.test.Counter.count is an integer/);
    });

    test("null keeps the instances whose field holds no value, an own field's or a behavior's member's", () => {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [tag] });
      publishItem(engine, [{ name: 'test.Tag' }], [{ name: 'kind', typeRef: { name: 'string' } }]);
      const owners: Array<[string, string | undefined, string | null]> = [
        ['a', 'build', 'ann'],
        ['b', undefined, null],
        ['c', 'test', null],
        ['d', undefined, 'bob'],
        ['e', 'build', 'ann'],
      ];
      for (const [id, kind, owner] of owners) {
        engine.instances.create(alice, 'Item', { title: id, ...(kind === undefined ? {} : { kind }) }, { id });
        if (owner !== null) {
          engine.instances.invoke(alice, 'Item', id, 'own', { owner });
        }
      }
      // A cleared owner is none again.
      engine.instances.invoke(alice, 'Item', 'e', 'own', { owner: null });
      const ids = (where: Record<string, unknown>): string[] => engine.instances.list(alice, 'Item', { where }).items.map((item) => item.id);
      assert.deepEqual(ids({ kind: null }), ['b', 'd']);
      assert.deepEqual(ids({ kind: [null, 'test'] }), ['b', 'c', 'd']);
      assert.deepEqual(ids({ 'test.Tag.tag.owner': 'ann' }), ['a']);
      assert.deepEqual(ids({ 'test.Tag.tag.owner': null }), ['b', 'c', 'e']);
      assert.deepEqual(ids({ 'test.Tag.tag.owner': [null, 'bob'], kind: null }), ['b', 'd']);
      // What a read shows agrees with what the filter kept.
      for (const item of engine.instances.list(alice, 'Item', { where: { 'test.Tag.tag.owner': null } }).items) {
        assert.deepEqual(item.behaviors['test.Tag'], { tag: { owner: null } });
      }
      assert.match(thrown(() => ids({ 'test.Tag.tag.owner': 3 }), EngineError).message, /where.test.Tag.tag.owner is a string or null, or a list of them, not 3/);
      // The member's name alone, or the field's, names nothing a list filters on.
      assert.match(
        thrown(() => ids({ 'tag.owner': 'ann' }), EngineError).message,
        /where names tag.owner, which is not a field Item filters on; it filters on title, kind, test.Tag.tag.owner/
      );
      assert.match(thrown(() => ids({ 'test.Tag.tag': 'ann' }), EngineError).message, /where names test.Tag.tag, which is not a field Item filters on/);
    });

    test("a behavior's index serves null as one more range; an own index, partial on a value, serves none", () => {
      const engine = openTestEngine({ driver, metaSchema: openMetaSchema(), behaviors: [tag] });
      publishItem(engine, [{ name: 'test.Tag' }], [{ name: 'slug', typeRef: { name: 'string' }, unique: true }]);
      const total = FILTER_SCAN_ROWS * 2 + 500;
      const bare = new Set([10, FILTER_SCAN_ROWS + 500, FILTER_SCAN_ROWS * 2 + 400]);
      engine.storage.transaction(() => {
        for (let index = 0; index < total; index += 1) {
          const id = `i${String(index).padStart(5, '0')}`;
          engine.instances.create(alice, 'Item', { title: id, ...(bare.has(index) ? {} : { slug: id }) }, { id });
          if (!bare.has(index)) {
            engine.instances.invoke(alice, 'Item', id, 'own', { owner: 'many' });
          }
        }
      });
      const ids = [...bare].map((index) => `i${String(index).padStart(5, '0')}`);
      assert.deepEqual(every(engine, { where: { 'test.Tag.tag.owner': null } }, 'Item'), { ids, pages: [3] });
      assert.deepEqual(every(engine, { where: { slug: null } }, 'Item'), { ids, pages: [1, 1, 1] });
      // With a value beside null, the unique index still serves no member.
      assert.deepEqual(every(engine, { where: { slug: [null, 'i00000'] } }, 'Item').ids, ['i00000', ...ids]);
    });

    test('where refuses a field it does not filter on, a value of another type, and an empty or long list', () => {
      const engine = open(driver);
      const refused = (where: unknown, pattern: RegExp): void => {
        const error = thrown(() => engine.instances.list(alice, 'Job', { where: where as Record<string, unknown> }), EngineError);
        assert.equal(error.code, 'invalid_argument');
        assert.match(error.message, pattern);
      };
      refused({ tags: 'a' }, /where names tags, which is not a field Job filters on; it filters on slug, kind, rank, urgent, Workflow.status/);
      // Workflow's status goes by its qualified name: a bare key names an own field, and Job has no own status.
      refused({ status: 'todo' }, /where names status, which is not a field Job filters on/);
      refused({ owner: { name: 'x' } }, /where names owner/);
      refused({ title: 'x' }, /where names title/);
      refused({ kind: 1 }, /where.kind is a string or null, or a list of them, not 1/);
      refused({ rank: 1.5 }, /where.rank is an integer or null, or a list of them, not 1.5/);
      refused({ urgent: 'yes' }, /where.urgent is a boolean/);
      refused({ kind: [] }, /where.kind lists 1 to 100 values, got 0/);
      refused({ kind: Array.from({ length: 101 }, (_, index) => `k${index}`) }, /lists 1 to 100 values, got 101/);
      refused({ 'Workflow.status': ['todo', 3] }, /where.Workflow.status is a string/);
      refused(['kind'], /where is a JSON object/);
    });
  });
}
