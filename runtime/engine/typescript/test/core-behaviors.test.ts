// The core's behaviors with no extension linked (D10, D16): an engine with
// only its own behaviors and the core meta-schema runs the documents the
// core binary builds in the CLI smoke: fixture-behaviors-json, whose type
// composes Workflow, Comments and Revisions, fixture-cross-instance-json,
// whose tasks wait on tasks and documents and link to both and to the
// project every create gives, fixture-rollups-json, whose projects roll their tasks up,
// fixture-search-json, whose notes are searched,
// fixture-reactions-json, whose projects start, finish and fail their
// parent, fixture-variants-json, whose steps keep their kind and hold
// a result of the kind's shape, and fixture-branches-json, whose recipes
// are version graphs.
// They register when the engine opens, under names no deployment can take.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { EngineError, defineBehavior, type Principal } from '../dist/index.js';
import {
  alice,
  cleanup,
  documentsDocument,
  drivers,
  notesDocument,
  openTestEngine,
  projectTreeDocument,
  projectsDocument,
  recipesDocument,
  stepsDocument,
  tasksDocument,
} from './helpers.ts';

afterEach(cleanup);

// The General schema-file document `superschematic build --emit-ir` loads
// with no extension linked (make cli-smoke).
const documents = documentsDocument();
const tasks = tasksDocument();
const projects = projectsDocument();
const notes = notesDocument();

const writer: Principal = { subject: 'wes', permissions: [] };
const reviewer: Principal = { subject: 'rae', permissions: ['documents.review'] };
const publisher: Principal = { subject: 'pat', permissions: ['documents'] };

for (const driver of drivers) {
  describe(`the core's behaviors with no extension (${driver})`, () => {
    test("an engine registers the core's behaviors when it opens, and no one else can take their names", () => {
      const engine = openTestEngine({ driver });
      assert.deepEqual(engine.behaviors.names(), ['Branches', 'Comments', 'Constants', 'Dependencies', 'Links', 'Reactions', 'Revisions', 'Rollups', 'Search', 'Variants', 'Workflow']);
      assert.deepEqual(engine.behaviors.declaration('Workflow')?.fields, [{ name: 'status', description: 'The state the instance is in.' }]);
      const impostor = defineBehavior({ declaration: { name: 'Workflow' } });
      assert.throws(() => engine.behaviors.register(impostor), /behavior Workflow is already registered with this engine/);
      assert.throws(() => openTestEngine({ driver, behaviors: [impostor] }), /already registered/);
    });

    test('the core also declares the work-queue behaviors, which this engine runs only once a deployment registers their package', () => {
      const engine = openTestEngine({ driver });
      const document = JSON.parse(JSON.stringify(notes)) as { types: { Note: Record<string, unknown> } };
      document.types.Note.behaviors = [{ name: 'Lease', config: { ttlMs: 30000 } }];
      // The core meta-schema admits Lease, so the loader passes it; the
      // engine has no implementation of it.
      const refused = thrown(() => engine.schemas.define(alice, document));
      assert.equal(refused.code, 'invalid_schema');
      assert.match(refused.message, /behavior Lease on type Note: no implementation registered/);
    });

    test('it runs the document the core binary builds, with all three composed on one type', () => {
      let now = 1000;
      const engine = openTestEngine({ driver, clock: () => (now += 1) });
      engine.schemas.define(alice, documents);
      assert.deepEqual(engine.schemas.publish(alice, 'documents'), { namespace: 'default', name: 'documents', version: 1, published: true });
      assert.deepEqual(
        engine.schemas.behaviors(alice, 'documents').map(({ name }) => name),
        ['Workflow', 'Comments', 'Revisions']
      );

      const created = engine.instances.create(writer, 'documents', { title: 'Launch plan' }, { id: 'doc-1' });
      assert.deepEqual(created.data, { title: 'Launch plan' });
      assert.deepEqual(created.behaviors, { Workflow: { status: 'draft' }, Comments: { commentCount: 0 }, Revisions: { revision: 1, pendingProposals: 0 } });

      engine.instances.invoke(writer, 'documents', 'doc-1', 'comment', { body: 'First pass is up.' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'review' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'propose', { patch: { body: 'The plan, in full.' }, note: 'Adds the body.' });
      assert.equal(thrown(() => engine.instances.invoke(writer, 'documents', 'doc-1', 'approve', { proposal: 1 })).code, 'forbidden');
      engine.instances.invoke(reviewer, 'documents', 'doc-1', 'approve', { proposal: 1 });
      engine.instances.invoke(reviewer, 'documents', 'doc-1', 'comment', { body: 'Approved the body.', replyTo: 1 });
      assert.equal(thrown(() => engine.instances.invoke(reviewer, 'documents', 'doc-1', 'transition', { to: 'published' })).code, 'forbidden');
      engine.instances.invoke(publisher, 'documents', 'doc-1', 'transition', { to: 'published' });

      const read = engine.instances.get(alice, 'documents', 'doc-1');
      assert.deepEqual(read?.data, { title: 'Launch plan', body: 'The plan, in full.' });
      assert.deepEqual(read?.behaviors, { Workflow: { status: 'published' }, Comments: { commentCount: 2 }, Revisions: { revision: 2, pendingProposals: 0 } });
      assert.equal(read?.seq, 7);

      const events = engine.events.read(alice, { schema: 'documents', instanceId: 'doc-1' }).events;
      assert.deepEqual(
        events.map((event) => [event.kind, event.actor, event.kind === 'operation' ? (event.change as { patch: unknown }).patch : event.change]),
        [
          [
            'create',
            'wes',
            { data: { title: 'Launch plan' }, behaviors: { Workflow: { status: 'draft' }, Comments: { commentCount: 0 }, Revisions: { revision: 1, pendingProposals: 0 } } },
          ],
          ['operation', 'wes', { behaviors: { Comments: { commentCount: 1 } } }],
          ['operation', 'wes', { behaviors: { Workflow: { status: 'review' } } }],
          ['operation', 'wes', { behaviors: { Revisions: { pendingProposals: 1 } } }],
          ['operation', 'rae', { data: { body: 'The plan, in full.' }, behaviors: { Revisions: { revision: 2, pendingProposals: 0 } } }],
          ['operation', 'rae', { behaviors: { Comments: { commentCount: 2 } } }],
          ['operation', 'pat', { behaviors: { Workflow: { status: 'published' } } }],
        ]
      );
      // The log replays, a merge patch at a time, to the instance a read returns.
      let replayed: unknown = {};
      for (const event of events) {
        replayed = mergePatch(replayed, event.kind === 'create' ? event.change : (event.change as { patch: unknown }).patch);
      }
      assert.deepEqual(replayed, { data: read?.data, behaviors: read?.behaviors });

      const revisions = engine.instances.invoke(alice, 'documents', 'doc-1', 'listRevisions') as { items: Array<{ revision: number; data: unknown; proposal?: number }> };
      assert.deepEqual(
        revisions.items.map(({ revision, data, proposal }) => ({ revision, data, proposal })),
        [
          { revision: 1, data: { title: 'Launch plan' }, proposal: undefined },
          { revision: 2, data: { title: 'Launch plan', body: 'The plan, in full.' }, proposal: 1 },
        ]
      );
      const comments = engine.instances.invoke(alice, 'documents', 'doc-1', 'listComments') as { items: Array<{ replyTo?: number; createdBy: string }> };
      assert.deepEqual(comments.items.map(({ replyTo, createdBy }) => [replyTo, createdBy]), [
        [undefined, 'wes'],
        [1, 'rae'],
      ]);

      // A new version adds an optional field and a state; the instance keeps its history.
      const next = JSON.parse(JSON.stringify(documents)) as {
        types: { Document: { fields: unknown[]; behaviors: Array<{ config?: { states: string[]; transitions: unknown[] } }> } };
      };
      next.types.Document.fields.push({ name: 'summary', typeRef: { name: 'string' } });
      const flow = next.types.Document.behaviors[0].config as { states: string[]; transitions: unknown[] };
      flow.states.push('retracted');
      flow.transitions.push({ from: 'published', to: 'retracted' });
      engine.schemas.define(alice, next);
      assert.equal(engine.schemas.publish(alice, 'documents').version, 2);
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'retracted' });
      engine.instances.update(writer, 'documents', 'doc-1', { summary: 'Withdrawn.' });
      const retracted = engine.instances.get(alice, 'documents', 'doc-1');
      assert.deepEqual(retracted?.data, { title: 'Launch plan', body: 'The plan, in full.', summary: 'Withdrawn.' });
      assert.deepEqual(retracted?.behaviors, { Workflow: { status: 'retracted' }, Comments: { commentCount: 2 }, Revisions: { revision: 3, pendingProposals: 0 } });
    });

    test('it runs the notes document: its title and body are searched, the title weighing more, and its embedder pulls their text', () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, notes);
      engine.schemas.publish(alice, 'notes');
      const vectors = { dimensions: 384, model: 'minilm-l6', permission: 'notes.embed' };
      assert.deepEqual(engine.schemas.behaviors(alice, 'notes'), [
        { name: 'Search', config: { fields: ['title', 'body'], weights: { title: 3 }, vectors }, declaration: engine.behaviors.declaration('Search') },
      ]);
      engine.instances.create(writer, 'notes', { title: 'Standup', body: 'The release slips a week.' }, { id: 'n1' });
      engine.instances.create(writer, 'notes', { title: 'Release plan', body: 'Dates and owners.' }, { id: 'n2' });
      engine.instances.create(writer, 'notes', { title: 'Lunch', body: 'Tacos.' }, { id: 'n3' });
      const found = engine.instances.invokeSchema(writer, 'notes', 'search', { query: 'release' }) as { items: Array<{ id: string; field: string }> };
      assert.deepEqual(
        found.items.map((hit) => [hit.id, hit.field]),
        [
          ['n2', 'title'],
          ['n1', 'body'],
        ]
      );
      const pulled = engine.instances.invokeSchema(writer, 'notes', 'staleEmbeddings', { limit: 1 }) as { model: string; items: Array<{ id: string; text: string }> };
      assert.deepEqual([pulled.model, pulled.items.map((item) => [item.id, item.text])], ['minilm-l6', [['n1', 'Standup\n\nThe release slips a week.']]]);
      engine.instances.delete(writer, 'notes', 'n2');
      assert.deepEqual(engine.instances.invokeSchema(writer, 'notes', 'search', { query: 'release plan' }), { items: [], next: null });
    });

    test('it runs the tasks document beside the documents one: blockers of both schemas, and links to both and to the project its create gives', () => {
      const engine = openTestEngine({ driver });
      for (const document of [documents, tasks, projects]) {
        engine.schemas.define(alice, document);
        engine.schemas.publish(alice, document.name as string);
      }
      assert.deepEqual(
        engine.schemas.behaviors(alice, 'tasks').map(({ name }) => name),
        ['Workflow', 'Dependencies', 'Links']
      );
      engine.instances.create(writer, 'documents', { title: 'Design' }, { id: 'doc-1' });
      engine.instances.create(writer, 'projects', { title: 'Launch' }, { id: 'launch' });
      // A task's project is required: its create gives it.
      assert.equal(thrown(() => engine.instances.create(writer, 'tasks', { title: 'Stray' })).code, 'invalid_argument');
      const project = { Links: { project: 'launch' } };
      engine.instances.create(writer, 'tasks', { title: 'Plan' }, { id: 'plan', behaviors: project });
      const created = engine.instances.create(writer, 'tasks', { title: 'Build' }, { id: 'build', behaviors: project });
      assert.deepEqual([created.data, created.behaviors], [
        { title: 'Build' },
        { Workflow: { status: 'todo' }, Dependencies: { blocked: false }, Links: { targets: { project: { schema: 'projects', id: 'launch' } } } },
      ]);

      // build waits on plan and on the design, implements the design at its
      // first revision, and belongs to plan.
      engine.instances.invoke(writer, 'tasks', 'build', 'addBlocker', { id: 'plan' });
      engine.instances.invoke(writer, 'tasks', 'build', 'addBlocker', { schema: 'documents', id: 'doc-1' });
      engine.instances.invoke(writer, 'tasks', 'build', 'link', { name: 'spec', id: 'doc-1' });
      engine.instances.invoke(writer, 'tasks', 'build', 'link', { name: 'parent', id: 'plan' });
      engine.instances.invoke(writer, 'tasks', 'build', 'transition', { to: 'doing' });
      assert.deepEqual(engine.instances.get(alice, 'tasks', 'build')?.behaviors, {
        Workflow: { status: 'doing' },
        Dependencies: { blocked: true },
        Links: {
          targets: {
            parent: { schema: 'tasks', id: 'plan' },
            project: { schema: 'projects', id: 'launch' },
            spec: { schema: 'documents', id: 'doc-1', revision: 1, latest: 1, stale: false },
          },
        },
      });
      // A create gives the same edges and links in one event: test waits on plan.
      const test = engine.instances.create(writer, 'tasks', { title: 'Test' }, {
        id: 'test',
        behaviors: { Links: { project: 'launch', parent: 'plan' }, Dependencies: { blockers: [{ id: 'plan' }] } },
      });
      assert.deepEqual([test.seq, test.behaviors.Dependencies.blocked, Object.keys(test.behaviors.Links.targets as object)], [1, true, ['parent', 'project']]);
      engine.instances.delete(writer, 'tasks', 'test');
      assert.equal(thrown(() => engine.instances.invoke(writer, 'tasks', 'build', 'transition', { to: 'done' })).code, 'vetoed');

      // The design is revised, then archived, a terminal state; plan is done.
      engine.instances.update(writer, 'documents', 'doc-1', { body: 'Revised.' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'review' });
      engine.instances.invoke(publisher, 'documents', 'doc-1', 'transition', { to: 'published' });
      engine.instances.invoke(writer, 'documents', 'doc-1', 'transition', { to: 'archived' });
      engine.instances.invoke(writer, 'tasks', 'plan', 'transition', { to: 'doing' });
      engine.instances.invoke(writer, 'tasks', 'plan', 'transition', { to: 'done' });
      const build = engine.instances.get(alice, 'tasks', 'build');
      assert.deepEqual(
        [build?.behaviors.Dependencies.blocked, (build?.behaviors.Links.targets as { spec: unknown }).spec],
        [false, { schema: 'documents', id: 'doc-1', revision: 1, latest: 2, stale: true }]
      );
      assert.deepEqual(engine.instances.invokeSchema(alice, 'tasks', 'listLinked', { name: 'spec', id: 'doc-1', stale: true }), {
        items: [{ id: 'build', revision: 1, latest: 2, stale: true }],
        next: null,
      });
      assert.deepEqual(engine.instances.invoke(writer, 'tasks', 'build', 'transition', { to: 'done' }), { from: 'doing', to: 'done' });

      // launch is the tasks' required project, so it stays; the design can
      // go, which removes build's edge to it and clears its spec link.
      assert.equal(thrown(() => engine.instances.delete(writer, 'projects', 'launch')).code, 'vetoed');
      const after = engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;
      assert.equal(engine.instances.delete(writer, 'documents', 'doc-1'), true);
      assert.deepEqual(
        engine.events.read(alice, { after }).events.map((event) => [event.kind, event.schema, event.instanceId, (event.change as { operation?: string } | null)?.operation]),
        [
          ['delete', 'documents', 'doc-1', undefined],
          ['operation', 'tasks', 'build', 'removeBlocker'],
          ['operation', 'tasks', 'build', 'unlink'],
        ]
      );
      const done = engine.instances.get(alice, 'tasks', 'build');
      assert.deepEqual([done?.data, done?.behaviors], [
        { title: 'Build' },
        {
          Workflow: { status: 'done' },
          Dependencies: { blocked: false },
          Links: { targets: { parent: { schema: 'tasks', id: 'plan' }, project: { schema: 'projects', id: 'launch' } } },
        },
      ]);
    });

    test('it runs the projects document beside the tasks one: a project rolls up the tasks that point at it, and waits for them to finish', () => {
      const engine = openTestEngine({ driver });
      for (const document of [documents, tasks, projects]) {
        engine.schemas.define(alice, document);
        engine.schemas.publish(alice, document.name as string);
      }
      assert.deepEqual(
        engine.schemas.behaviors(alice, 'projects').map(({ name }) => name),
        ['Workflow', 'Rollups']
      );
      const created = engine.instances.create(writer, 'projects', { title: 'Launch' }, { id: 'launch' });
      assert.deepEqual([created.data, created.behaviors], [
        { title: 'Launch' },
        { Workflow: { status: 'active' }, Rollups: { values: { tasks: 0, tasksByStatus: {}, tasksFinished: true } } },
      ]);
      for (const id of ['plan', 'build']) {
        engine.instances.create(writer, 'tasks', { title: id }, { id, behaviors: { Links: { project: 'launch' } } });
      }
      engine.instances.invoke(writer, 'tasks', 'plan', 'transition', { to: 'doing' });
      assert.deepEqual(engine.instances.get(alice, 'projects', 'launch')?.behaviors.Rollups.values, {
        tasks: 2,
        tasksByStatus: { doing: 1, todo: 1 },
        tasksFinished: false,
      });
      assert.equal(thrown(() => engine.instances.invoke(writer, 'projects', 'launch', 'transition', { to: 'done' })).code, 'vetoed');
      engine.instances.invoke(writer, 'tasks', 'plan', 'transition', { to: 'done' });
      engine.instances.invoke(writer, 'tasks', 'build', 'transition', { to: 'dropped' });
      const read = engine.instances.get(alice, 'projects', 'launch');
      assert.deepEqual([read?.seq, read?.behaviors.Rollups.values], [1, { tasks: 2, tasksByStatus: { done: 1, dropped: 1 }, tasksFinished: true }]);
      assert.deepEqual(engine.instances.invoke(writer, 'projects', 'launch', 'transition', { to: 'done' }), { from: 'active', to: 'done' });
    });

    test('it runs the project tree document: a project starts its parent, the last to finish finishes it, and one that fails fails it, after the commit', () => {
      const runner: Principal = { subject: 'runner', permissions: [] };
      const engine = openTestEngine({ driver, runner: { principal: runner } });
      engine.schemas.define(alice, projectTreeDocument());
      engine.schemas.publish(alice, 'projects');
      for (const id of ['launch', 'design', 'build']) {
        engine.instances.create(writer, 'projects', { title: id }, { id });
      }
      for (const id of ['design', 'build']) {
        engine.instances.invoke(writer, 'projects', id, 'link', { name: 'parent', id: 'launch' });
      }
      const statuses = () => ['launch', 'design', 'build'].map((id) => engine.instances.get(alice, 'projects', id)?.behaviors.Workflow.status);

      engine.instances.invoke(writer, 'projects', 'design', 'transition', { to: 'doing' });
      assert.deepEqual(statuses(), ['todo', 'doing', 'todo']);
      engine.runner.runDue();
      assert.deepEqual(statuses(), ['doing', 'doing', 'todo']);
      engine.instances.invoke(writer, 'projects', 'design', 'transition', { to: 'done' });
      engine.instances.invoke(writer, 'projects', 'build', 'transition', { to: 'doing' });
      engine.instances.invoke(writer, 'projects', 'build', 'transition', { to: 'done' });
      engine.runner.runDue();
      assert.deepEqual(statuses(), ['done', 'done', 'done']);
      const launch = engine.events.read(alice, { schema: 'projects', instanceId: 'launch' }).events;
      assert.deepEqual(
        launch.slice(1).map((event) => [event.actor, (event.change as { params: unknown }).params, event.cause?.behavior, event.cause?.depth]),
        [
          ['runner', { to: 'doing' }, 'Reactions', 1],
          ['runner', { to: 'done' }, 'Reactions', 1],
        ]
      );

      // A tree with a failed project fails its parent, and is never done.
      for (const id of ['release', 'docs', 'site']) {
        engine.instances.create(writer, 'projects', { title: id }, { id });
      }
      for (const id of ['docs', 'site']) {
        engine.instances.invoke(writer, 'projects', id, 'link', { name: 'parent', id: 'release' });
        engine.instances.invoke(writer, 'projects', id, 'transition', { to: 'doing' });
      }
      engine.instances.invoke(writer, 'projects', 'docs', 'transition', { to: 'failed' });
      engine.instances.invoke(writer, 'projects', 'site', 'transition', { to: 'done' });
      engine.runner.runDue();
      assert.deepEqual(
        ['release', 'docs', 'site'].map((id) => engine.instances.get(alice, 'projects', id)?.behaviors.Workflow.status),
        ['failed', 'failed', 'done']
      );
    });

    test("it runs the steps document: a step's kind stays what its create gave it, and its result has the kind's shape", () => {
      const engine = openTestEngine({ driver });
      engine.schemas.define(alice, stepsDocument());
      engine.schemas.publish(alice, 'Step');
      const check = engine.instances.create(writer, 'Step', { title: 'Lint', kind: 'verify', result: { passed: true, checks: [{ name: 'eslint', ok: true }] } });
      assert.equal(check.seq, 1);
      assert.deepEqual(
        thrown(() => engine.instances.update(writer, 'Step', check.id, { kind: 'note', result: null })).message,
        'Step in namespace default (version 1): kind: kind is a constant of Step: its create sets it and nothing changes it after'
      );
      assert.equal(thrown(() => engine.instances.update(writer, 'Step', check.id, { result: { passed: 'yes' } })).code, 'invalid_instance');
      assert.equal(thrown(() => engine.instances.create(writer, 'Step', { title: 'Idea', kind: 'note', result: { passed: true } })).code, 'invalid_instance');
      assert.deepEqual(engine.instances.update(writer, 'Step', check.id, { result: { passed: false } }).data, {
        title: 'Lint',
        kind: 'verify',
        result: { passed: false, checks: [{ name: 'eslint', ok: true }] },
      });
    });

    test('it runs the recipes document: a recipe is a graph root whose drafts merge into its primary line, and a tagged commit is released', () => {
      const engine = openTestEngine({ driver, runner: { principal: { subject: 'runner', permissions: [] } } });
      engine.schemas.define(alice, recipesDocument());
      engine.schemas.publish(alice, 'Recipe');
      engine.instances.create(writer, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const invoke = <T>(operation: string, params: Record<string, unknown> = {}): T =>
        engine.instances.invoke(writer, 'Recipe', 'soup', operation, params) as T;
      type Ref = { id: string; version: number; parent: string | null };
      const [main] = invoke<{ items: Ref[] }>('refs').items;
      const draft = invoke<Ref>('branch', { fromRef: main.id, name: 'first' });
      const saved = invoke<{ ref: Ref; saved: { step: Array<{ entity_key: string }> } }>('save', {
        ref: draft.id,
        version: draft.version,
        edits: { step: { upsert: [{ instruction: 'Boil', position: 1 }] }, cover: { upsert: [{ photoUrl: 'soup.jpg' }] } },
      });
      invoke('save', {
        ref: draft.id,
        version: saved.ref.version,
        edits: { ingredient: { upsert: [{ stepKey: saved.saved.step[0].entity_key, quantity: '1 l' }] } },
      });
      const committed = invoke<{ ref: Ref }>('commit', { ref: draft.id, version: saved.ref.version + 1 });
      const merged = invoke<{ commit: { id: string; sequence: number } }>('merge', { source: draft.id, target: main.id, targetVersion: main.version, tag: true });
      assert.equal(merged.commit.sequence, 1);
      assert.deepEqual(invoke('releaseCommit', { commit: merged.commit.id, version: 0 }), { commit: merged.commit.id, version: 1 });
      const released = invoke<{ tree: Record<string, Array<Record<string, unknown>>> }>('released');
      assert.deepEqual(
        Object.fromEntries(Object.entries(released.tree).map(([kind, rows]) => [kind, rows.length])),
        { cover: 1, ingredient: 1, step: 1 }
      );
      assert.equal(committed.ref.parent, main.id);
      // The sweep is on for the schema, at the interval its config gives.
      engine.runner.runDue();
      assert.deepEqual(
        engine.runner.status().schedules.filter((schedule) => schedule.behavior === 'Branches').map(({ state, everyMs }) => [state, everyMs]),
        [['active', 3600000]]
      );
    });
  });
}

// mergePatch applies a JSON merge patch (RFC 7386) to a copy of target.
function mergePatch(target: unknown, patch: unknown): unknown {
  if (typeof patch !== 'object' || patch === null || Array.isArray(patch)) {
    return patch;
  }
  const out: Record<string, unknown> = typeof target === 'object' && target !== null && !Array.isArray(target) ? { ...(target as Record<string, unknown>) } : {};
  for (const [key, value] of Object.entries(patch)) {
    if (value === null) {
      delete out[key];
    } else {
      out[key] = mergePatch(out[key], value);
    }
  }
  return out;
}

// thrown runs fn and returns the EngineError it throws.
function thrown(fn: () => unknown): EngineError {
  try {
    fn();
  } catch (error) {
    assert.ok(error instanceof EngineError, `expected an EngineError, got ${String(error)}`);
    return error;
  }
  assert.fail('expected an EngineError');
}
