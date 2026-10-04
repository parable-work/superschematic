// Dependencies, the core's blockers: the blocked field and the Workflow
// gate over one rule, across schemas; a blocker's outcome, which finishes
// it only when satisfiedBy lists it; gates on states a transition leaves;
// the edges addBlocker refuses; the blockers a create gives, held to the
// same checks; the lists; a blocker's delete, which removes its edges as
// the caller; reads as the caller; and its config rules.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  CreateParamsError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  defineBehavior,
  type AccessPolicy,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';
import { reader } from './reach-fixtures.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };

// A task moves from todo to doing and then to done, or is dropped; done
// and dropped are terminal, and done is the gated state.
const taskFlow = {
  states: ['todo', 'doing', 'done', 'dropped'],
  transitions: [
    { from: 'todo', to: 'doing' },
    { from: 'doing', to: 'done' },
    { from: 'todo', to: 'dropped' },
    { from: 'doing', to: 'dropped' },
  ],
};

// A milestone is active until it ships.
const milestoneFlow = { states: ['active', 'shipped'], transitions: [{ from: 'active', to: 'shipped' }] };

// A check runs until it passes, fails or is skipped: a failure and a
// neutral outcome beside the success its config leaves unnamed.
const checkFlow = {
  states: ['running', 'passed', 'failed', 'skipped'],
  transitions: [
    { from: 'running', to: 'passed' },
    { from: 'running', to: 'failed' },
    { from: 'running', to: 'skipped' },
  ],
  outcomes: { failed: 'failure', skipped: 'neutral' },
};

function schema(name: string, behaviors: Array<{ name: string; config?: unknown }>): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

function tasks(dependencies: Record<string, unknown> = { schemas: ['Task', 'Milestone'], gatedStates: ['done'] }, extra: Array<{ name: string; config?: unknown }> = []) {
  return schema('Task', [{ name: 'Workflow', config: taskFlow }, { name: 'Dependencies', config: dependencies }, ...extra]);
}

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

// test.Finisher moves a task to done through Workflow's own operation.
const finisher = defineBehavior({
  declaration: {
    name: 'test.Finisher',
    operations: [{ name: 'finish', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true }],
  },
  operations: {
    finish(context) {
      return context.call('Workflow', 'transition', { to: 'done' });
    },
  },
});

function recording(rules: (request: AccessRequest) => boolean): AccessPolicy {
  return (request) => request.principal.subject === 'alice' || rules(request);
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  // Tasks t1, t2 and t3 in todo, and milestone m1, active.
  function world(options: Partial<EngineOptions> = {}, dependencies?: Record<string, unknown>): Engine {
    const engine = open(options);
    publish(engine, schema('Milestone', [{ name: 'Workflow', config: milestoneFlow }]));
    publish(engine, schema('Note', []));
    publish(engine, tasks(dependencies));
    for (const id of ['t1', 't2', 't3']) {
      engine.instances.create(alice, 'Task', { title: id }, { id });
    }
    engine.instances.create(alice, 'Milestone', { title: 'Launch' }, { id: 'm1' });
    engine.instances.create(alice, 'Note', { title: 'Aside' }, { id: 'n1' });
    return engine;
  }

  const move = (engine: Engine, id: string, to: string, principal: Principal = alice) => engine.instances.invoke(principal, 'Task', id, 'transition', { to });

  describe(`Dependencies (${driver})`, () => {
    test('an instance is blocked while a blocker is not in a terminal state of its Workflow, and addBlocker says so', () => {
      const engine = world();
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' }), { schema: 'Task', id: 't2', status: 'todo', open: true });
      assert.deepEqual(engine.instances.get(alice, 'Task', 't1')?.data, { title: 't1', status: 'todo', blocked: true });
      assert.deepEqual(engine.events.read(alice, { schema: 'Task', instanceId: 't1' }).events.at(-1)?.change, {
        behavior: 'Dependencies',
        operation: 'addBlocker',
        params: { id: 't2' },
        patch: { blocked: true },
      });
      move(engine, 't2', 'doing');
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, true);
      move(engine, 't2', 'done');
      // Read when read: the blocker's change shows at t1's next read, with no event on t1.
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.seq, 2);
      const dropped = world();
      dropped.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      move(dropped, 't2', 'dropped');
      assert.equal(dropped.instances.get(alice, 'Task', 't1')?.data.blocked, false, 'every terminal state of the blocker counts, not only the gated one');
    });

    test('the guard holds a transition into a gated state while blocked, whoever asks; other transitions pass', () => {
      const engine = open({ metaSchema: openMetaSchema(), behaviors: [finisher, reader] });
      publish(engine, tasks({ gatedStates: ['done'] }, [{ name: 'test.Finisher' }, { name: 'test.Reader' }]));
      for (const id of ['t1', 't2', 't3']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
      }
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      move(engine, 't1', 'doing');
      const caller = thrown(() => move(engine, 't1', 'done'), BehaviorVetoError);
      assert.deepEqual(
        [caller.behavior, caller.action, caller.reason],
        ['Dependencies', 'transition', 'Task t1 cannot move to done while it is blocked by Task t2 (todo)']
      );
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'finish'), BehaviorVetoError).behavior, 'Dependencies');
      const invoked = thrown(
        () => engine.instances.invoke(alice, 'Task', 't3', 'poke', { schema: 'Task', id: 't1', operation: 'transition', params: { to: 'done' } }),
        BehaviorVetoError
      );
      assert.equal(invoked.behavior, 'Dependencies');
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.status, 'doing');
      // Dropping is terminal but not gated here.
      assert.deepEqual(move(engine, 't1', 'dropped'), { from: 'doing', to: 'dropped' });
      engine.instances.create(alice, 'Task', { title: 't4' }, { id: 't4' });
      engine.instances.invoke(alice, 'Task', 't4', 'addBlocker', { id: 't2' });
      move(engine, 't4', 'doing');
      move(engine, 't2', 'doing');
      move(engine, 't2', 'done');
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't4', 'finish'), { from: 'doing', to: 'done' });
    });

    test('a blocker of another schema is read through its own Workflow config, by the field and the guard alike', () => {
      const engine = world();
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' }), {
        schema: 'Milestone',
        id: 'm1',
        status: 'active',
        open: true,
      });
      move(engine, 't1', 'doing');
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, true);
      assert.equal(thrown(() => move(engine, 't1', 'done'), BehaviorVetoError).reason, 'Task t1 cannot move to done while it is blocked by Milestone m1 (active)');
      engine.instances.invoke(alice, 'Milestone', 'm1', 'transition', { to: 'shipped' });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.deepEqual(move(engine, 't1', 'done'), { from: 'doing', to: 'done' });
    });

    test('addBlocker refuses itself, a schema it does not list, one without Workflow, a missing instance, a repeat and a cycle', () => {
      const engine = world({}, { schemas: ['Task', 'Note'] });
      const params = (fn: () => unknown) => thrown(fn, OperationParamsError).issues;
      const add = (id: string, blocker: Record<string, unknown>) => engine.instances.invoke(alice, 'Task', id, 'addBlocker', blocker);
      assert.deepEqual(params(() => add('t1', { id: 't1' })), [{ path: '/id', message: 'Task t1 cannot block itself' }]);
      assert.deepEqual(params(() => add('t1', { schema: 'Milestone', id: 'm1' })), [
        { path: '/schema', message: 'a blocker of Task is an instance of Task, Note, not Milestone' },
      ]);
      assert.deepEqual(params(() => add('t1', { schema: 'Note', id: 'n1' })), [{ path: '/schema', message: 'Note does not compose Workflow, so its instances cannot block' }]);
      assert.deepEqual(params(() => add('t1', { id: 't9' })), [{ path: '/id', message: 'Task t9 does not exist' }]);
      assert.equal(thrown(() => add('t1', { id: 'bad id' }), OperationParamsError).issues[0].path, '/id');
      add('t1', { id: 't2' });
      assert.equal(thrown(() => add('t1', { id: 't2' }), BehaviorVetoError).reason, 'Task t2 already blocks it');
      add('t2', { id: 't3' });
      const cycle = thrown(() => add('t3', { id: 't1' }), BehaviorVetoError);
      assert.equal(cycle.reason, 'Task t1 is blocked by Task t3, directly or through others: the edge would close a cycle');
      assert.equal(thrown(() => add('t2', { id: 't1' }), BehaviorVetoError).reason, 'Task t1 is blocked by Task t2, directly or through others: the edge would close a cycle');
    });

    test('an instance in a gated state no transition leaves takes no open blocker, and one that is finished', () => {
      const engine = world();
      move(engine, 't1', 'doing');
      move(engine, 't1', 'done');
      const refused = thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' }), BehaviorVetoError);
      assert.equal(refused.reason, 'it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Task t2 (todo)');
      move(engine, 't3', 'dropped');
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't3' }), { schema: 'Task', id: 't3', status: 'dropped', open: false });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
    });

    test('a blocker is finished only in a terminal state whose outcome satisfiedBy lists, success by default', () => {
      const engine = open();
      publish(engine, schema('Check', [{ name: 'Workflow', config: checkFlow }]));
      publish(engine, schema('Milestone', [{ name: 'Workflow', config: milestoneFlow }]));
      publish(engine, tasks({ schemas: ['Task', 'Check', 'Milestone'], gatedStates: ['done'] }));
      for (const id of ['t1', 't2']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
      }
      for (const id of ['c1', 'c2', 'c3']) {
        engine.instances.create(alice, 'Check', { title: id }, { id });
      }
      engine.instances.create(alice, 'Milestone', { title: 'Launch' }, { id: 'm1' });
      const check = (id: string, to: string) => engine.instances.invoke(alice, 'Check', id, 'transition', { to });
      const blocked = (id: string) => engine.instances.get(alice, 'Task', id)?.data.blocked;

      // A failed check does not let the step after it through.
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Check', id: 'c1' });
      move(engine, 't1', 'doing');
      check('c1', 'failed');
      assert.equal(blocked('t1'), true);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'listBlockers', {}), { items: [{ schema: 'Check', id: 'c1', status: 'failed', open: true }], next: null });
      assert.equal(thrown(() => move(engine, 't1', 'done'), BehaviorVetoError).reason, 'Task t1 cannot move to done while it is blocked by Check c1 (failed)');
      // So does a skipped one, whose outcome is neutral; a passed one and a shipped milestone, whose config names no outcome, are successes.
      engine.instances.invoke(alice, 'Task', 't1', 'removeBlocker', { schema: 'Check', id: 'c1' });
      assert.equal(blocked('t1'), false, 'removing the failed blocker is how the work goes on without it');
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Check', id: 'c2' });
      check('c2', 'skipped');
      assert.equal(blocked('t1'), true);
      engine.instances.invoke(alice, 'Task', 't1', 'removeBlocker', { schema: 'Check', id: 'c2' });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Check', id: 'c3' });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      check('c3', 'passed');
      engine.instances.invoke(alice, 'Milestone', 'm1', 'transition', { to: 'shipped' });
      assert.equal(blocked('t1'), false);
      assert.deepEqual(move(engine, 't1', 'done'), { from: 'doing', to: 'done' });
      // An instance in a terminal gated state takes no blocker that failed: it is not finished.
      assert.equal(
        thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Check', id: 'c1' }), BehaviorVetoError).reason,
        'it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Check c1 (failed)'
      );

      // A dependent whose satisfiedBy lists neutral goes on past a skipped check, and still not past a failed one.
      publish(engine, tasks({ schemas: ['Task', 'Check', 'Milestone'], gatedStates: ['done'], satisfiedBy: ['success', 'neutral'] }));
      engine.instances.invoke(alice, 'Task', 't2', 'addBlocker', { schema: 'Check', id: 'c2' });
      assert.equal(blocked('t2'), false);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't2', 'addBlocker', { schema: 'Check', id: 'c1' }), { schema: 'Check', id: 'c1', status: 'failed', open: true });
      assert.equal(blocked('t2'), true);
    });

    test('a gate on a state a transition leaves holds the start of work; a blocker found during the work holds its end', () => {
      const engine = world({}, { schemas: ['Task', 'Milestone'], gatedStates: ['doing', 'done'] });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      const refused = thrown(() => move(engine, 't1', 'doing'), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.reason], ['Dependencies', 'Task t1 cannot move to doing while it is blocked by Task t2 (todo)']);
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.status, 'todo');
      move(engine, 't2', 'dropped');
      assert.deepEqual(move(engine, 't1', 'doing'), { from: 'todo', to: 'doing' }, 'dropped is terminal and a success: the blocker is finished');
      // An instance in doing, a gated state a transition leaves, takes an open blocker, which holds its move to done.
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' }), {
        schema: 'Milestone',
        id: 'm1',
        status: 'active',
        open: true,
      });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, true);
      assert.equal(thrown(() => move(engine, 't1', 'done'), BehaviorVetoError).reason, 'Task t1 cannot move to done while it is blocked by Milestone m1 (active)');
      engine.instances.invoke(alice, 'Milestone', 'm1', 'transition', { to: 'shipped' });
      assert.deepEqual(move(engine, 't1', 'done'), { from: 'doing', to: 'done' });
    });

    test('removeBlocker removes an edge; one that is not an edge is refused', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'removeBlocker', { id: 't2' }), { schema: 'Task', id: 't2' });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.deepEqual(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'removeBlocker', { id: 't2' }), OperationParamsError).issues, [
        { path: '/id', message: 'Task t2 does not block Task t1' },
      ]);
      // With no edge, the blocker can go without asking t1 anything.
      assert.equal(engine.instances.delete(alice, 'Task', 't2'), true);
    });

    test('listBlockers pages the blockers with their states; listDependents pages the dependents the caller may read', () => {
      const engine = open({ policy: recording(({ schema }) => schema !== 'Epic') });
      publish(engine, schema('Milestone', [{ name: 'Workflow', config: milestoneFlow }]));
      publish(engine, tasks());
      publish(engine, schema('Epic', [{ name: 'Workflow', config: taskFlow }, { name: 'Dependencies', config: { schemas: ['Milestone'] } }]));
      for (const id of ['t1', 't2', 't3']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
      }
      engine.instances.create(alice, 'Milestone', { title: 'Launch' }, { id: 'm1' });
      engine.instances.create(alice, 'Epic', { title: 'Big' }, { id: 'e1' });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      engine.instances.invoke(alice, 'Epic', 'e1', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      engine.instances.invoke(alice, 'Task', 't3', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      move(engine, 't2', 'dropped');

      const first = engine.instances.invoke(alice, 'Task', 't1', 'listBlockers', { limit: 1 }) as { items: unknown[]; next: string };
      assert.deepEqual(first.items, [{ schema: 'Milestone', id: 'm1', status: 'active', open: true }]);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'listBlockers', { cursor: first.next }), {
        items: [{ schema: 'Task', id: 't2', status: 'dropped', open: false }],
        next: null,
      });
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Task', 't1', 'listBlockers', { cursor: 'nope' }), OperationParamsError).code, 'invalid_argument');

      // Milestone does not compose Dependencies, so a task lists what it blocks.
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't2', 'listDependents', {}), { items: [{ schema: 'Task', id: 't1' }], next: null });
      engine.instances.invoke(alice, 'Task', 't2', 'addBlocker', { id: 't3' });
      engine.instances.invoke(alice, 'Epic', 'e1', 'removeBlocker', { schema: 'Milestone', id: 'm1' });
      engine.instances.create(alice, 'Epic', { title: 'Small' }, { id: 'e2' });
      publish(engine, schema('Epic', [{ name: 'Workflow', config: taskFlow }, { name: 'Dependencies', config: { schemas: ['Milestone', 'Task'] } }]));
      engine.instances.invoke(alice, 'Epic', 'e2', 'addBlocker', { schema: 'Task', id: 't3' });
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't3', 'listDependents', {}), {
        items: [
          { schema: 'Task', id: 't2' },
          { schema: 'Epic', id: 'e2' },
        ],
        next: null,
      });
      // bob may not read Epic, so its dependents drop out of his pages.
      assert.deepEqual(engine.instances.invoke(bob, 'Task', 't3', 'listDependents', { limit: 2 }), { items: [{ schema: 'Task', id: 't2' }], next: null });
    });

    test('deleting a blocker removes its edges through removeBlocker, as the caller, with an event on each dependent', () => {
      const engine = world({ policy: recording(({ schema, action }) => schema === 'Milestone' || action === 'read') });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      engine.instances.invoke(alice, 'Task', 't2', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      // bob may write milestones, not tasks, so he cannot clear their edges.
      const refused = thrown(() => engine.instances.delete(bob, 'Milestone', 'm1'), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not call removeBlocker (write) on Task in namespace default']);
      assert.ok(engine.instances.get(alice, 'Milestone', 'm1'));
      const after = engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;
      assert.equal(engine.instances.delete(alice, 'Milestone', 'm1'), true);
      assert.deepEqual(
        engine.events.read(alice, { after }).events.map((event) => [event.kind, event.schema, event.instanceId, (event.change as { patch?: unknown } | null)?.patch]),
        [
          ['delete', 'Milestone', 'm1', undefined],
          ['operation', 'Task', 't1', { blocked: false }],
          ['operation', 'Task', 't2', { blocked: false }],
        ]
      );
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'listBlockers', {}), { items: [], next: null });
    });

    test('deleting a dependent deletes its edges', () => {
      const engine = world();
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      engine.instances.delete(alice, 'Task', 't1');
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't2', 'listDependents', {}), { items: [], next: null });
      engine.instances.create(alice, 'Task', { title: 'again' }, { id: 't1' });
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false);
      assert.equal(engine.instances.delete(alice, 'Task', 't2'), true);
    });

    test("blockers are read as the caller: without read on a blocker's schema, blocked cannot be read", () => {
      const engine = world({ policy: recording(({ schema }) => schema === 'Task') });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { schema: 'Milestone', id: 'm1' });
      assert.equal(thrown(() => engine.instances.get(bob, 'Task', 't1'), EngineError).code, 'forbidden');
      assert.equal(engine.instances.get(bob, 'Task', 't2')?.data.blocked, false);
      assert.equal(thrown(() => engine.instances.invoke(bob, 'Task', 't2', 'addBlocker', { schema: 'Milestone', id: 'm1' }), EngineError).code, 'forbidden');
    });

    test('its config: gated states are states of the type Workflow, every terminal one by default; blockers of the own schema by default', () => {
      const engine = open();
      const refusal = (config: Record<string, unknown>) => thrown(() => engine.schemas.define(alice, tasks(config)), SchemaDocumentError);
      assert.match(refusal({ gatedStates: ['lost'] }).message, /behavior Dependencies config: gated state "lost" is not a state of the type's Workflow \(todo, doing, done, dropped\)/);
      // The core meta-schema holds the config to the declaration's configSchema first.
      assert.ok(refusal({ gatedStates: [] }).issues.some((issue) => issue.path === '/types/Task/behaviors/1/config/gatedStates'));
      assert.ok(refusal({ schemas: ['task list'] }).issues.some((issue) => issue.path === '/types/Task/behaviors/1/config/schemas/0'));
      assert.ok(refusal({ satisfiedBy: [] }).issues.some((issue) => issue.path === '/types/Task/behaviors/1/config/satisfiedBy'));
      assert.ok(refusal({ satisfiedBy: ['done'] }).issues.some((issue) => issue.path === '/types/Task/behaviors/1/config/satisfiedBy/0'));
      assert.ok(refusal({ satisfiedBy: ['success', 'success'] }).issues.some((issue) => issue.path === '/types/Task/behaviors/1/config/satisfiedBy'));
      assert.match(
        thrown(() => engine.schemas.define(alice, schema('Task', [{ name: 'Dependencies' }])), SchemaDocumentError).message,
        /behavior Dependencies requires behavior Workflow, which the type does not list/
      );
      publish(engine, tasks({}));
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      engine.instances.create(alice, 'Task', { title: 't2' }, { id: 't2' });
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      move(engine, 't1', 'doing');
      // Every terminal state is gated by default: dropped too.
      assert.equal(thrown(() => move(engine, 't1', 'dropped'), BehaviorVetoError).behavior, 'Dependencies');
    });

    test('configChange: schemas, gated states and satisfiedBy may change; it may be added to a schema with instances, not removed', () => {
      const engine = open();
      publish(engine, schema('Milestone', [{ name: 'Workflow', config: milestoneFlow }]));
      publish(engine, schema('Task', [{ name: 'Workflow', config: taskFlow }]));
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      engine.instances.create(alice, 'Task', { title: 't2' }, { id: 't2' });
      publish(engine, tasks({ gatedStates: ['done'] }));
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, false, 'an instance that exists starts with no blocker');
      engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
      publish(engine, tasks({ schemas: ['Task', 'Milestone'] }));
      move(engine, 't1', 'doing');
      assert.equal(thrown(() => move(engine, 't1', 'dropped'), BehaviorVetoError).behavior, 'Dependencies', 'dropped is gated now');
      publish(engine, tasks({ schemas: ['Task', 'Milestone'], gatedStates: ['doing', 'done'], satisfiedBy: ['success', 'neutral'] }));
      assert.deepEqual(move(engine, 't1', 'dropped'), { from: 'doing', to: 'dropped' }, 'dropped is not gated now');
      engine.schemas.define(alice, tasks({ schemas: ['Milestone'] }));
      assert.equal(engine.schemas.publish(alice, 'Task').published, true);
      assert.equal(engine.instances.get(alice, 'Task', 't1')?.data.blocked, true, 'an edge made before stays');
      const removed = thrown(() => engine.schemas.define(alice, schema('Task', [{ name: 'Workflow', config: taskFlow }])), IncompatibleChangeError);
      assert.deepEqual(removed.changes, [
        {
          path: 'Task.behaviors.Dependencies',
          message: 'behavior Dependencies cannot be removed from type Task, which has instances: the edges and references its instances hold would stay behind',
        },
      ]);
    });

    test("a create gives blockers, of its own schema or one the config lists: it is blocked from its create's event", () => {
      const engine = world();
      const created = engine.instances.create(
        alice,
        'Task',
        { title: 't4' },
        { id: 't4', behaviors: { Dependencies: { blockers: [{ id: 't1' }, { schema: 'Milestone', id: 'm1' }] } } }
      );
      assert.deepEqual(created.data, { title: 't4', status: 'todo', blocked: true });
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Task', instanceId: 't4' }).events.map((event) => [event.kind, event.change]),
        [['create', { title: 't4', status: 'todo', blocked: true }]]
      );
      assert.deepEqual(
        (engine.instances.invoke(alice, 'Task', 't4', 'listBlockers', {}) as { items: unknown[] }).items,
        [
          { schema: 'Task', id: 't1', status: 'todo', open: true },
          { schema: 'Milestone', id: 'm1', status: 'active', open: true },
        ]
      );
      move(engine, 't4', 'doing');
      assert.equal(thrown(() => move(engine, 't4', 'done'), BehaviorVetoError).behavior, 'Dependencies');
      // The edges are references, as addBlocker's are: a blocker's delete removes its edge.
      assert.equal(engine.instances.delete(alice, 'Milestone', 'm1'), true);
      assert.deepEqual(engine.events.read(alice, { schema: 'Task', instanceId: 't4' }).events.at(-1)?.change, {
        behavior: 'Dependencies',
        operation: 'removeBlocker',
        params: { schema: 'Milestone', id: 'm1' },
        patch: {},
      });
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'listDependents', {}), { items: [{ schema: 'Task', id: 't4' }], next: null });
    });

    test("a create's blockers are held to addBlocker's checks, at pointers into its parameters; a refused one leaves nothing", () => {
      const engine = world();
      const create = (blockers: unknown) => () => engine.instances.create(alice, 'Task', { title: 't4' }, { id: 't4', behaviors: { Dependencies: { blockers } } });
      const issues = (blockers: unknown) => thrown(create(blockers), CreateParamsError).issues;
      assert.deepEqual(issues([{ id: 't1' }, { schema: 'Note', id: 'n1' }]), [
        { path: '/behaviors/Dependencies/blockers/1/schema', message: 'a blocker of Task is an instance of Task, Milestone, not Note' },
      ]);
      assert.deepEqual(issues([{ id: 't9' }]), [{ path: '/behaviors/Dependencies/blockers/0/id', message: 'Task t9 does not exist' }]);
      // An instance cannot block itself: the edge would be a cycle.
      assert.deepEqual(issues([{ id: 't4' }]), [{ path: '/behaviors/Dependencies/blockers/0/id', message: 'Task t4 cannot block itself' }]);
      const twice = thrown(create([{ id: 't1' }, { schema: 'Task', id: 't1' }]), BehaviorVetoError);
      assert.deepEqual([twice.behavior, twice.action, twice.reason], ['Dependencies', 'create', 'Task t1 already blocks it']);
      // Its createParamsSchema holds the shape.
      assert.deepEqual(issues('t1'), [{ path: '/behaviors/Dependencies/blockers', message: 'must be array' }]);
      assert.deepEqual(issues([{ id: 't1', open: true }]), [{ path: '/behaviors/Dependencies/blockers/0', message: 'must NOT have additional properties: open' }]);
      assert.deepEqual(
        thrown(() => engine.instances.create(alice, 'Task', { title: 't4' }, { behaviors: { Dependencies: { blocker: [] } } }), CreateParamsError).issues,
        [{ path: '/behaviors/Dependencies', message: 'must NOT have additional properties: blocker' }]
      );
      assert.equal(engine.instances.get(alice, 'Task', 't4'), undefined);
      assert.deepEqual(engine.instances.invoke(alice, 'Task', 't1', 'listDependents', {}), { items: [], next: null });
    });

    test("a create in a gated state no transition leaves takes no blocker that is not finished, by satisfiedBy, wherever the type lists Workflow", () => {
      const engine = world();
      publish(engine, schema('Check', [{ name: 'Workflow', config: checkFlow }]));
      engine.instances.create(alice, 'Check', { title: 'c1' }, { id: 'c1' });
      engine.instances.invoke(alice, 'Check', 'c1', 'transition', { to: 'failed' });
      // Gate starts done, a terminal state and so gated; it lists Dependencies before Workflow.
      const gateFlow = { states: ['todo', 'done'], initial: 'done', transitions: [{ from: 'todo', to: 'done' }] };
      const gate = (dependencies: Record<string, unknown>) =>
        publish(engine, schema('Gate', [{ name: 'Dependencies', config: dependencies }, { name: 'Workflow', config: gateFlow }]));
      const create = (blocker: Record<string, unknown>) => engine.instances.create(alice, 'Gate', { title: 'g1' }, { behaviors: { Dependencies: { blockers: [blocker] } } });
      gate({ schemas: ['Task', 'Check'] });
      assert.equal(
        thrown(() => create({ schema: 'Task', id: 't1' }), BehaviorVetoError).reason,
        'it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Task t1 (todo)'
      );
      // A failed check is terminal, and still not finished: satisfiedBy lists success alone.
      assert.equal(
        thrown(() => create({ schema: 'Check', id: 'c1' }), BehaviorVetoError).reason,
        'it is done, a gated state no transition leaves, so it takes no blocker that is not finished: Check c1 (failed)'
      );
      move(engine, 't1', 'doing');
      move(engine, 't1', 'done');
      assert.deepEqual(create({ schema: 'Task', id: 't1' }).data, { title: 'g1', blocked: false, status: 'done' });
      gate({ schemas: ['Task', 'Check'], satisfiedBy: ['success', 'failure'] });
      assert.deepEqual(create({ schema: 'Check', id: 'c1' }).data, { title: 'g1', blocked: false, status: 'done' });
    });

    test("a create in a gated state a transition leaves takes an open blocker, which holds its first move into a gated state", () => {
      const engine = world({}, { schemas: ['Task', 'Milestone'], gatedStates: ['todo', 'doing', 'done'] });
      const created = engine.instances.create(alice, 'Task', { title: 't4' }, { id: 't4', behaviors: { Dependencies: { blockers: [{ id: 't1' }] } } });
      assert.deepEqual(created.data, { title: 't4', status: 'todo', blocked: true });
      assert.equal(thrown(() => move(engine, 't4', 'doing'), BehaviorVetoError).reason, 'Task t4 cannot move to doing while it is blocked by Task t1 (todo)');
      move(engine, 't1', 'dropped');
      assert.deepEqual(move(engine, 't4', 'doing'), { from: 'todo', to: 'doing' });
    });
  });
}
