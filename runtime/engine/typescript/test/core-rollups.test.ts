// Rollups, the core's values derived from linked instances: every
// function over the instances that point at one through a Links link,
// latest's order by creation,
// computed at each read with no event on the instance read, the bound of
// 500 linked instances, reads as the caller, the Workflow gate, all and
// any limited to outcomes, the config rules at define and publish, a
// rollup over its own schema, a link that no longer points at the
// schema, and configChange.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  MAX_ROLLUP_READ,
  SchemaDocumentError,
  type AccessPolicy,
  type AccessRequest,
  type Engine,
  type EngineOptions,
  type Principal,
} from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';

afterEach(cleanup);

const bob: Principal = { subject: 'bob', permissions: [] };

// A task moves from todo to doing and then to done, or is dropped; done
// and dropped are terminal.
const taskFlow = {
  states: ['todo', 'doing', 'done', 'dropped'],
  transitions: [
    { from: 'todo', to: 'doing' },
    { from: 'doing', to: 'done' },
    { from: 'todo', to: 'dropped' },
    { from: 'doing', to: 'dropped' },
  ],
};

// A project is active until it is done or dropped.
const projectFlow = { states: ['active', 'done', 'dropped'], transitions: [{ from: 'active', to: 'done' }, { from: 'active', to: 'dropped' }] };

type Behaviors = Array<{ name: string; config?: unknown }>;

// A Task has a title, an estimate (a number), hours (an integer), a kind
// (an enum), urgent (a boolean), labels (a list) and notes (any JSON).
function tasks(behaviors: Behaviors = [{ name: 'Workflow', config: taskFlow }, { name: 'Links', config: { links: { project: { schema: 'Project' } } } }]) {
  const document = schemaDocument(
    'Task',
    [
      { name: 'title', typeRef: { name: 'string' }, required: true },
      { name: 'estimate', typeRef: { name: 'number' } },
      { name: 'hours', typeRef: { name: 'Generic.Int64' } },
      { name: 'kind', typeRef: { name: 'Kind' } },
      { name: 'urgent', typeRef: { name: 'boolean' } },
      { name: 'labels', typeRef: { name: 'string', isArray: true } },
      { name: 'notes', typeRef: { name: 'Generic.JSON' } },
    ],
    {
      enums: {
        Kind: {
          name: 'Kind',
          values: [
            { name: 'BUG', serializedAs: 'bug' },
            { name: 'FEATURE', serializedAs: 'feature' },
          ],
        },
      },
    }
  ) as { types: Record<string, Record<string, unknown>> };
  document.types.Task.behaviors = behaviors;
  return document;
}

const rollup = (fn: string, extra: Record<string, unknown> = {}) => ({ schema: 'Task', link: 'project', function: fn, ...extra });

const every = {
  tasks: rollup('count'),
  byStatus: rollup('countBy', { field: 'Workflow.status' }),
  byKind: rollup('countBy', { field: 'kind' }),
  byUrgency: rollup('countBy', { field: 'urgent' }),
  estimate: rollup('sum', { field: 'estimate' }),
  smallest: rollup('min', { field: 'estimate' }),
  longest: rollup('max', { field: 'hours' }),
  finished: rollup('all', { gatedStates: ['done'] }),
  started: rollup('any'),
};

function schema(name: string, behaviors: Behaviors): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

function projects(rollups: Record<string, unknown> = every, workflow = true): Record<string, unknown> {
  return schema('Project', [...(workflow ? [{ name: 'Workflow', config: projectFlow }] : []), { name: 'Rollups', config: { rollups } }]);
}

function publish(engine: Engine, document: Record<string, unknown>, principal: Principal = alice): void {
  engine.schemas.define(principal, document);
  engine.schemas.publish(principal, document.name as string);
}

function recording(rules: (request: AccessRequest) => boolean): { policy: AccessPolicy; asked: AccessRequest[] } {
  const asked: AccessRequest[] = [];
  return {
    asked,
    policy: (request) => {
      asked.push(request);
      return request.principal.subject === 'alice' || rules(request);
    },
  };
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, ...options });
  }

  const task = (engine: Engine, id: string, data: Record<string, unknown> = {}, project?: string) => {
    engine.instances.create(alice, 'Task', { title: id, ...data }, { id });
    if (project !== undefined) {
      engine.instances.invoke(alice, 'Task', id, 'link', { name: 'project', id: project });
    }
  };
  const move = (engine: Engine, schemaName: string, id: string, to: string, principal: Principal = alice) =>
    engine.instances.invoke(principal, schemaName, id, 'transition', { to });
  const rollupsOf = (engine: Engine, id: string, principal: Principal = alice) => engine.instances.get(principal, 'Project', id)?.behaviors.Rollups.values;

  // Projects p1, p2 and p3; tasks t1 to t3 point at p1, t4 at p2 and t5 at none.
  function world(options: Partial<EngineOptions> = {}, rollups: Record<string, unknown> = every): Engine {
    const engine = open(options);
    publish(engine, tasks());
    publish(engine, projects(rollups));
    for (const id of ['p1', 'p2', 'p3']) {
      engine.instances.create(alice, 'Project', { title: id }, { id });
    }
    task(engine, 't1', { estimate: 3, hours: 2, kind: 'bug', urgent: true, labels: ['a'] }, 'p1');
    task(engine, 't2', { estimate: 5.5, kind: 'feature', urgent: false }, 'p1');
    task(engine, 't3', { notes: { size: 8 } }, 'p1');
    task(engine, 't4', { estimate: 8, hours: 10, kind: 'bug' }, 'p2');
    task(engine, 't5', { estimate: 100, hours: 100 });
    return engine;
  }

  describe(`Rollups (${driver})`, () => {
    test('each function over the instances that point at the instance through the link, and only those', () => {
      const engine = world();
      assert.deepEqual(rollupsOf(engine, 'p1'), {
        byKind: { bug: 1, feature: 1 },
        byStatus: { todo: 3 },
        byUrgency: { false: 1, true: 1 },
        estimate: 8.5,
        finished: false,
        longest: 2,
        smallest: 3,
        started: false,
        tasks: 3,
      });
      move(engine, 'Task', 't1', 'doing');
      move(engine, 'Task', 't1', 'done');
      move(engine, 'Task', 't2', 'dropped');
      assert.deepEqual(rollupsOf(engine, 'p1'), {
        byKind: { bug: 1, feature: 1 },
        byStatus: { done: 1, dropped: 1, todo: 1 },
        byUrgency: { false: 1, true: 1 },
        estimate: 8.5,
        finished: false,
        longest: 2,
        smallest: 3,
        started: true,
        tasks: 3,
      });
      move(engine, 'Task', 't3', 'dropped');
      assert.equal((rollupsOf(engine, 'p1') as { finished: boolean }).finished, true, 'every terminal state counts, not only done');
      assert.deepEqual(rollupsOf(engine, 'p2'), {
        byKind: { bug: 1 },
        byStatus: { todo: 1 },
        byUrgency: {},
        estimate: 8,
        finished: false,
        longest: 10,
        smallest: 8,
        started: false,
        tasks: 1,
      });
      // None point at p3: all of none holds, any of none does not, and min and max have no value.
      assert.deepEqual(rollupsOf(engine, 'p3'), { byKind: {}, byStatus: {}, byUrgency: {}, estimate: 0, finished: true, started: false, tasks: 0 });
      const p3 = engine.instances.get(alice, 'Project', 'p3');
      assert.deepEqual([p3?.data, p3?.behaviors], [
        { title: 'p3' },
        {
          Workflow: { status: 'active' },
          Rollups: { values: { byKind: {}, byStatus: {}, byUrgency: {}, estimate: 0, finished: true, started: false, tasks: 0 } },
        },
      ]);
    });

    test('latest is a field\'s value on the linked instance created last, the greater id on a tie, and absent when that one holds none', () => {
      let now = 100;
      const engine = open({ clock: () => now });
      publish(engine, tasks());
      publish(
        engine,
        projects({ lastKind: rollup('latest', { field: 'kind' }), lastNotes: rollup('latest', { field: 'notes' }), lastStatus: rollup('latest', { field: 'Workflow.status' }) })
      );
      engine.instances.create(alice, 'Project', { title: 'p1' }, { id: 'p1' });
      assert.deepEqual(rollupsOf(engine, 'p1'), {}, 'over no instance it has no value');
      task(engine, 't2', { kind: 'bug', notes: { size: 1 } }, 'p1');
      task(engine, 't1', { kind: 'feature' }, 'p1');
      // t1 and t2 were created at once: the greater id is the latest.
      assert.deepEqual(rollupsOf(engine, 'p1'), { lastKind: 'bug', lastNotes: { size: 1 }, lastStatus: 'todo' });
      now = 200;
      task(engine, 't0', { notes: [1, 2] }, 'p1');
      assert.deepEqual(rollupsOf(engine, 'p1'), { lastNotes: [1, 2], lastStatus: 'todo' }, 'the latest holds no kind, so lastKind has no value');
      move(engine, 'Task', 't0', 'doing');
      assert.equal((rollupsOf(engine, 'p1') as { lastStatus: string }).lastStatus, 'doing');
      // Creation orders, not linking: an older instance linked now is not the latest.
      now = 50;
      task(engine, 'old', { kind: 'feature' });
      now = 300;
      engine.instances.invoke(alice, 'Task', 'old', 'link', { name: 'project', id: 'p1' });
      assert.deepEqual(rollupsOf(engine, 'p1'), { lastNotes: [1, 2], lastStatus: 'doing' });
      // The latest gone, the one created before it is.
      engine.instances.delete(alice, 'Task', 't0');
      assert.deepEqual(rollupsOf(engine, 'p1'), { lastKind: 'bug', lastNotes: { size: 1 }, lastStatus: 'todo' });
    });

    test("a bare status names the linked type's own field, Workflow.status its Workflow's: a rollup reads each apart", () => {
      const engine = open();
      const document = tasks() as { types: { Task: { fields: unknown[] } } };
      document.types.Task.fields.push({ name: 'status', typeRef: { name: 'string' } });
      publish(engine, document);
      publish(
        engine,
        projects({
          byOwn: rollup('countBy', { field: 'status' }),
          byWorkflow: rollup('countBy', { field: 'Workflow.status' }),
          lastOwn: rollup('latest', { field: 'status' }),
          lastWorkflow: rollup('latest', { field: 'Workflow.status' }),
        })
      );
      engine.instances.create(alice, 'Project', { title: 'p1' }, { id: 'p1' });
      task(engine, 't1', { status: 'billed' }, 'p1');
      task(engine, 't2', { status: 'billed' }, 'p1');
      move(engine, 'Task', 't2', 'doing');
      assert.deepEqual(rollupsOf(engine, 'p1'), {
        byOwn: { billed: 2 },
        byWorkflow: { doing: 1, todo: 1 },
        lastOwn: 'billed',
        lastWorkflow: 'doing',
      });
    });

    test("a linked instance's change shows at the next read, with no event on the instance read", () => {
      const engine = world();
      const before = engine.instances.get(alice, 'Project', 'p1');
      const events = engine.events.read(alice, { schema: 'Project', instanceId: 'p1' }).events.length;
      move(engine, 'Task', 't3', 'doing');
      engine.instances.update(alice, 'Task', 't2', { estimate: 1.5 });
      engine.instances.invoke(alice, 'Task', 't5', 'link', { name: 'project', id: 'p1' });
      engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'project' });
      engine.instances.invoke(alice, 'Task', 't4', 'link', { name: 'project', id: 'p1' });
      const after = engine.instances.get(alice, 'Project', 'p1');
      assert.deepEqual(
        [after?.seq, after?.behaviors.Rollups.values],
        [
          before?.seq,
          {
            byKind: { bug: 1, feature: 1 },
            byStatus: { doing: 1, todo: 3 },
            byUrgency: { false: 1 },
            estimate: 109.5,
            finished: false,
            longest: 100,
            smallest: 1.5,
            started: false,
            tasks: 4,
          },
        ]
      );
      assert.equal((rollupsOf(engine, 'p2') as { tasks: number }).tasks, 0, 't4 moved to p1');
      engine.instances.delete(alice, 'Task', 't5');
      assert.equal((rollupsOf(engine, 'p1') as { tasks: number }).tasks, 3);
      assert.equal(engine.events.read(alice, { schema: 'Project', instanceId: 'p1' }).events.length, events, 'no event on the project');
      // The project's own change carries no rollup in its event: they are the same before and after it.
      engine.instances.update(alice, 'Project', 'p1', { title: 'Renamed' });
      assert.deepEqual(engine.events.read(alice, { schema: 'Project', instanceId: 'p1' }).events.at(-1)?.change, { data: { title: 'Renamed' } });
    });

    test('a transition into a gated state waits for its rollup to hold, whoever asks; other states do not', () => {
      const engine = world();
      move(engine, 'Task', 't1', 'doing');
      const vetoed = thrown(() => move(engine, 'Project', 'p1', 'done'), BehaviorVetoError);
      assert.deepEqual(
        [vetoed.behavior, vetoed.action, vetoed.reason, vetoed.vetoCode, vetoed.vetoDetails],
        [
          'Rollups',
          'transition',
          'Project p1 cannot move to done until rollup finished holds: 3 of the 3 instances of Task that point at it through project are not in a terminal state',
          'not_held',
          { rollup: 'finished', to: 'done', over: false, linked: 3, counted: 0 },
        ]
      );
      assert.equal(engine.instances.get(alice, 'Project', 'p1')?.behaviors.Workflow.status, 'active');
      move(engine, 'Task', 't1', 'done');
      move(engine, 'Task', 't2', 'dropped');
      assert.match(thrown(() => move(engine, 'Project', 'p1', 'done'), BehaviorVetoError).reason, /1 of the 3 instances of Task/);
      assert.deepEqual(move(engine, 'Project', 'p2', 'dropped'), { from: 'active', to: 'dropped' }, 'dropped is not gated');
      move(engine, 'Task', 't3', 'dropped');
      assert.deepEqual(move(engine, 'Project', 'p1', 'done'), { from: 'active', to: 'done' });
      assert.deepEqual(move(engine, 'Project', 'p3', 'done'), { from: 'active', to: 'done' }, 'a project with no task can finish');
    });

    test('an any rollup gates too: it holds once one linked instance is in a terminal state', () => {
      const engine = world({}, { started: rollup('any', { gatedStates: ['done', 'dropped'] }) });
      const vetoed = thrown(() => move(engine, 'Project', 'p1', 'dropped'), BehaviorVetoError);
      assert.equal(vetoed.reason, 'Project p1 cannot move to dropped until rollup started holds: none of the 3 instances of Task that point at it through project is in a terminal state');
      assert.deepEqual([vetoed.vetoCode, vetoed.vetoDetails], ['not_held', { rollup: 'started', to: 'dropped', over: false, linked: 3, counted: 0 }]);
      assert.match(thrown(() => move(engine, 'Project', 'p3', 'done'), BehaviorVetoError).reason, /until rollup started holds: no instance of Task points at it through project$/);
      move(engine, 'Task', 't2', 'dropped');
      assert.deepEqual(move(engine, 'Project', 'p1', 'dropped'), { from: 'active', to: 'dropped' });
    });

    test('all and any with outcomes count only the linked instances in a terminal state whose outcome they list', () => {
      const engine = open();
      publish(engine, tasks([{ name: 'Workflow', config: { ...taskFlow, outcomes: { dropped: 'failure' } } }, { name: 'Links', config: { links: { project: { schema: 'Project' } } } }]));
      publish(
        engine,
        projects({
          finished: rollup('all'),
          succeeded: rollup('all', { outcomes: ['success'], gatedStates: ['done'] }),
          failed: rollup('any', { outcomes: ['failure'], gatedStates: ['dropped'] }),
        })
      );
      for (const id of ['p1', 'p2']) {
        engine.instances.create(alice, 'Project', { title: id }, { id });
      }
      task(engine, 't1', {}, 'p1');
      task(engine, 't2', {}, 'p1');
      assert.deepEqual(rollupsOf(engine, 'p1'), { failed: false, finished: false, succeeded: false });
      move(engine, 'Task', 't1', 'doing');
      move(engine, 'Task', 't1', 'done');
      move(engine, 'Task', 't2', 'dropped');
      assert.deepEqual(rollupsOf(engine, 'p1'), { failed: true, finished: true, succeeded: false }, 'without outcomes, every terminal state counts');
      assert.equal(
        thrown(() => move(engine, 'Project', 'p1', 'done'), BehaviorVetoError).reason,
        'Project p1 cannot move to done until rollup succeeded holds: 1 of the 2 instances of Task that point at it through project are not in a terminal state whose outcome is success'
      );
      assert.deepEqual(move(engine, 'Project', 'p1', 'dropped'), { from: 'active', to: 'dropped' });
      // All of none holds and any of none does not, outcomes or not.
      assert.deepEqual(rollupsOf(engine, 'p2'), { failed: false, finished: true, succeeded: true });
      task(engine, 't3', {}, 'p2');
      move(engine, 'Task', 't3', 'doing');
      move(engine, 'Task', 't3', 'done');
      assert.equal(
        thrown(() => move(engine, 'Project', 'p2', 'dropped'), BehaviorVetoError).reason,
        'Project p2 cannot move to dropped until rollup failed holds: none of the 1 instances of Task that point at it through project is in a terminal state whose outcome is failure'
      );
      assert.deepEqual(move(engine, 'Project', 'p2', 'done'), { from: 'active', to: 'done' });
    });

    test(`a rollup reads at most ${MAX_ROLLUP_READ} linked instances: past that it is {over: true}, and its gate does not hold`, () => {
      const engine = open();
      publish(engine, tasks());
      publish(engine, schema('Board', [{ name: 'Links', config: { links: { project: { schema: 'Project' } } } }]));
      publish(
        engine,
        projects({ tasks: rollup('count'), estimate: rollup('sum', { field: 'estimate' }), finished: rollup('all', { gatedStates: ['done'] }), boards: { schema: 'Board', link: 'project', function: 'count' } })
      );
      engine.instances.create(alice, 'Project', { title: 'Big' }, { id: 'p1' });
      engine.instances.create(alice, 'Board', { title: 'Wall' }, { id: 'b1' });
      engine.instances.invoke(alice, 'Board', 'b1', 'link', { name: 'project', id: 'p1' });
      for (let index = 1; index <= MAX_ROLLUP_READ; index += 1) {
        task(engine, `t${index}`, { estimate: 1 }, 'p1');
        move(engine, 'Task', `t${index}`, 'dropped');
      }
      assert.deepEqual(rollupsOf(engine, 'p1'), { boards: 1, estimate: 500, finished: true, tasks: 500 });
      task(engine, 'last', { estimate: 1 }, 'p1');
      assert.deepEqual(rollupsOf(engine, 'p1'), { boards: 1, estimate: { over: true }, finished: { over: true }, tasks: { over: true } });
      const vetoed = thrown(() => move(engine, 'Project', 'p1', 'done'), BehaviorVetoError);
      assert.equal(vetoed.reason, 'Project p1 cannot move to done: rollup finished has no value, since more than 500 instances of Task that point at it through project exist');
      assert.deepEqual([vetoed.vetoCode, vetoed.vetoDetails], ['not_held', { rollup: 'finished', to: 'done', over: true }]);
      engine.instances.invoke(alice, 'Task', 'last', 'unlink', { name: 'project' });
      assert.deepEqual(move(engine, 'Project', 'p1', 'done'), { from: 'active', to: 'done' });
    });

    test('it reads as the caller: without read on the linked schema, the instance cannot be read or moved', () => {
      const { policy, asked } = recording(({ schema }) => schema === 'Project');
      const engine = world({ policy });
      asked.length = 0;
      rollupsOf(engine, 'p1');
      assert.deepEqual(
        asked.map(({ action, schema, operation }) => [action, schema, operation]),
        [
          ['read', 'Project', undefined],
          ['read', 'Task', undefined],
          ['read', 'Task', 'listLinked'],
          ['read', 'Task', undefined],
          ['read', 'Task', undefined],
        ],
        "the project, then the tasks' Links config, listLinked, the tasks and their Workflow config"
      );
      assert.equal(thrown(() => engine.instances.get(bob, 'Project', 'p1'), EngineError).code, 'forbidden');
      assert.equal(thrown(() => engine.instances.list(bob, 'Project'), EngineError).code, 'forbidden');
      const refused = thrown(() => move(engine, 'Project', 'p3', 'done', bob), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'bob may not read Task in namespace default']);
      assert.equal(engine.instances.get(alice, 'Project', 'p3')?.behaviors.Workflow.status, 'active');
      // Defining a rollup asks read on the schema it names, as the caller who defines it.
      const definer = thrown(() => engine.schemas.define(bob, projects({ tasks: rollup('count') })), EngineError);
      assert.deepEqual([definer.code, definer.message], ['forbidden', 'bob may not read Task in namespace default']);
    });

    test('its config, at define: gated states of the type Workflow, and a linked schema that composes the link to this one with fitting fields', () => {
      const engine = open();
      publish(engine, schema('Note', [{ name: 'Workflow', config: taskFlow }]));
      publish(engine, schema('Plain', [{ name: 'Links', config: { links: { project: { schema: 'Project' } } } }]));
      publish(engine, tasks([{ name: 'Workflow', config: taskFlow }, { name: 'Links', config: { links: { project: { schema: 'Project' }, owner: { schema: 'Person' } } } }]));
      const refusal = (rollups: Record<string, unknown>, workflow = true) =>
        thrown(() => engine.schemas.define(alice, projects(rollups, workflow)), SchemaDocumentError).message.replace(/^schema: \/types\/Project\/behaviors\/\d\/config: type Project: behavior Rollups config: /, '');
      assert.equal(refusal({ finished: rollup('all', { gatedStates: ['shipped'] }) }), 'rollup finished: gated state "shipped" is not a state of the type\'s Workflow (active, done, dropped)');
      assert.equal(refusal({ finished: rollup('all', { gatedStates: ['done'] }) }, false), "rollup finished gates states of the type's Workflow, which the type does not compose");
      assert.equal(refusal({ tasks: { schema: 'Ghost', link: 'project', function: 'count' } }), 'rollup tasks: schema Ghost has no live version; publish it first');
      assert.equal(refusal({ tasks: { schema: 'Note', link: 'project', function: 'count' } }), 'rollup tasks: Note does not compose Links, so none of its instances points at Project');
      assert.equal(refusal({ tasks: rollup('count', { link: 'parent' }) }), 'rollup tasks: Task has no link parent (its links: owner, project)');
      assert.equal(refusal({ tasks: rollup('count', { link: 'owner' }) }), "rollup tasks: Task's link owner points at Person, not Project");
      assert.equal(
        refusal({ byTitle: rollup('countBy', { field: 'missing' }) }),
        'rollup byTitle: Task has no field missing; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow'
      );
      // Workflow's status goes by its qualified name: a bare status names an own field, and Task has none.
      assert.equal(
        refusal({ byStatus: rollup('countBy', { field: 'status' }) }),
        'rollup byStatus: Task has no field status; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow'
      );
      assert.equal(refusal({ byHours: rollup('countBy', { field: 'hours' }) }), "rollup byHours: countBy takes a string, enum or boolean field, and Task's hours holds an integer");
      assert.equal(refusal({ byLabel: rollup('countBy', { field: 'labels' }) }), "rollup byLabel: countBy takes a string, enum or boolean field, and Task's labels holds a list");
      assert.equal(
        refusal({ byStatus: { schema: 'Plain', link: 'project', function: 'countBy', field: 'Workflow.status' } }),
        'rollup byStatus: Plain has no field Workflow.status; countBy takes a string, enum or boolean field of its type, or Workflow.status when it composes Workflow'
      );
      assert.equal(refusal({ total: rollup('sum', { field: 'title' }) }), "rollup total: sum takes a number or integer field, and Task's title holds a string");
      assert.equal(refusal({ least: rollup('min', { field: 'notes' }) }), "rollup least: min takes a number or integer field, and Task's notes holds any JSON value");
      assert.equal(refusal({ most: rollup('max', { field: 'Workflow.status' }) }), 'rollup most: Task has no field Workflow.status; max takes a number or integer field of its type');
      assert.equal(
        refusal({ finished: { schema: 'Plain', link: 'project', function: 'all' } }),
        "rollup finished: all reads the terminal states of Plain's Workflow, which it does not compose"
      );
      assert.equal(
        refusal({ last: rollup('latest', { field: 'missing' }) }),
        'rollup last: Task has no field missing; latest takes a field of its type, or Workflow.status when it composes Workflow'
      );
      assert.equal(
        refusal({ last: { schema: 'Plain', link: 'project', function: 'latest', field: 'Workflow.status' } }),
        'rollup last: Plain has no field Workflow.status; latest takes a field of its type, or Workflow.status when it composes Workflow'
      );
      // The core meta-schema holds the config to the declaration's configSchema first.
      const shape = (rollups: unknown) => thrown(() => engine.schemas.define(alice, projects(rollups as Record<string, unknown>)), SchemaDocumentError).issues;
      assert.ok(shape({}).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups'));
      assert.ok(shape({ Tasks: rollup('count') }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups'));
      assert.ok(shape({ total: rollup('sum') }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/total'));
      assert.ok(shape({ last: rollup('latest') }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/last'));
      assert.ok(shape({ last: rollup('latest', { field: 'kind', gatedStates: ['done'] }) }).some((issue) => issue.path.startsWith('/types/Project/behaviors/1/config/rollups/last')));
      assert.ok(shape({ tasks: rollup('count', { field: 'title' }) }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/tasks'));
      assert.ok(shape({ tasks: rollup('count', { gatedStates: ['done'] }) }).some((issue) => issue.path.startsWith('/types/Project/behaviors/1/config/rollups/tasks')));
      assert.ok(shape({ tasks: rollup('count', { outcomes: ['success'] }) }).some((issue) => issue.path.startsWith('/types/Project/behaviors/1/config/rollups/tasks')));
      assert.ok(shape({ finished: rollup('all', { outcomes: [] }) }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/finished/outcomes'));
      assert.ok(shape({ finished: rollup('all', { outcomes: ['done'] }) }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/finished/outcomes/0'));
      assert.ok(shape({ mean: rollup('average', { field: 'estimate' }) }).some((issue) => issue.path === '/types/Project/behaviors/1/config/rollups/mean/function'));
      // What passes: every function, on every kind of field it takes.
      publish(engine, projects());
      assert.deepEqual(engine.schemas.behaviors(alice, 'Project').map(({ name }) => name), ['Workflow', 'Rollups']);
    });

    test('publish checks the linked schema again, as it is then', () => {
      const engine = open();
      publish(engine, tasks());
      engine.schemas.define(alice, projects({ tasks: rollup('count') }));
      // Task has no instances, so a new version may drop Links.
      publish(engine, tasks([{ name: 'Workflow', config: taskFlow }]));
      const refused = thrown(() => engine.schemas.publish(alice, 'Project'), SchemaDocumentError);
      assert.match(refused.message, /^default\/Project draft: \/types\/Project\/behaviors\/1\/config: .*rollup tasks: Task does not compose Links/);
      assert.equal(engine.schemas.live(alice, 'Project'), undefined);
    });

    test('a rollup over its own schema reads the version being defined for its own name', () => {
      const engine = open();
      const nested = tasks([
        { name: 'Workflow', config: taskFlow },
        { name: 'Links', config: { links: { parent: { schema: 'Task' } } } },
        { name: 'Rollups', config: { rollups: { subtasks: { schema: 'Task', link: 'parent', function: 'count' }, done: { schema: 'Task', link: 'parent', function: 'all', gatedStates: ['done'] } } } },
      ]);
      publish(engine, nested);
      for (const id of ['t1', 't2', 't3']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
      }
      engine.instances.invoke(alice, 'Task', 't2', 'link', { name: 'parent', id: 't1' });
      engine.instances.invoke(alice, 'Task', 't3', 'link', { name: 'parent', id: 't1' });
      assert.deepEqual(engine.instances.get(alice, 'Task', 't1')?.behaviors.Rollups.values, { done: false, subtasks: 2 });
      assert.deepEqual(engine.instances.get(alice, 'Task', 't2')?.behaviors.Rollups.values, { done: true, subtasks: 0 });
      move(engine, 'Task', 't1', 'doing');
      assert.equal(thrown(() => move(engine, 'Task', 't1', 'done'), BehaviorVetoError).behavior, 'Rollups');
      move(engine, 'Task', 't2', 'dropped');
      move(engine, 'Task', 't3', 'dropped');
      assert.deepEqual(move(engine, 'Task', 't1', 'done'), { from: 'doing', to: 'done' });
    });

    test("a link that no longer points at this schema holds none of the linked schema's instances", () => {
      const engine = open();
      publish(engine, schema('Other', []));
      publish(engine, tasks());
      publish(engine, projects({ tasks: rollup('count'), estimate: rollup('sum', { field: 'estimate' }) }));
      engine.instances.create(alice, 'Project', { title: 'p1' }, { id: 'p1' });
      // With no instance, Task may drop Links, then name project again for another schema.
      publish(engine, tasks([{ name: 'Workflow', config: taskFlow }]));
      assert.deepEqual(rollupsOf(engine, 'p1'), { estimate: 0, tasks: 0 });
      publish(engine, tasks([{ name: 'Workflow', config: taskFlow }, { name: 'Links', config: { links: { project: { schema: 'Other' } } } }]));
      engine.instances.create(alice, 'Other', { title: 'same id' }, { id: 'p1' });
      task(engine, 't1', { estimate: 4 }, 'p1');
      assert.deepEqual(engine.instances.get(alice, 'Task', 't1')?.behaviors.Links.targets, { project: { schema: 'Other', id: 'p1' } });
      assert.deepEqual(rollupsOf(engine, 'p1'), { estimate: 0, tasks: 0 }, "a task that points at Other p1 is not Project p1's");
    });

    test('configChange: nothing is stored, so rollups change freely and Rollups comes and goes on a schema with instances', () => {
      const engine = open();
      publish(engine, tasks());
      publish(engine, schema('Project', [{ name: 'Workflow', config: projectFlow }]));
      engine.instances.create(alice, 'Project', { title: 'p1' }, { id: 'p1' });
      task(engine, 't1', { estimate: 2 }, 'p1');
      publish(engine, projects({ tasks: rollup('count') }));
      assert.deepEqual(rollupsOf(engine, 'p1'), { tasks: 1 });
      publish(engine, projects({ estimate: rollup('sum', { field: 'estimate' }), finished: rollup('all', { gatedStates: ['done'] }) }));
      assert.deepEqual(rollupsOf(engine, 'p1'), { estimate: 2, finished: false });
      assert.equal(thrown(() => move(engine, 'Project', 'p1', 'done'), BehaviorVetoError).behavior, 'Rollups');
      publish(engine, schema('Project', [{ name: 'Workflow', config: projectFlow }]));
      const p1 = engine.instances.get(alice, 'Project', 'p1');
      assert.deepEqual([p1?.data, p1?.behaviors], [{ title: 'p1' }, { Workflow: { status: 'active' } }]);
      assert.deepEqual(move(engine, 'Project', 'p1', 'done'), { from: 'active', to: 'done' });
    });
  });
}
