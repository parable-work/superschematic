// Reactions, the core's rules that move Workflow statuses after a change
// commits: a task that starts moves its project to active, a project whose
// tasks are all finished is done, as the runner's principal, through
// Workflow's transition, whose events record the cause. allTerminal and
// anyTerminal read the outcomes of the linking instances' terminal
// states, so a run completes when its steps all pass and fails when one
// fails, in whatever order the events and the rules come. A rule leaves a
// target it cannot move; a misconfigured one halts its subscription.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  SchemaDocumentError,
  type Engine,
  type EngineEvent,
  type EngineOptions,
  type OperationChange,
  type SubscriptionStatus,
} from '../dist/index.js';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';
import { runnerPrincipal, testClock } from './runner-fixtures.ts';

afterEach(cleanup);

type BehaviorRef = { name: string; config?: unknown };

function schema(name: string, behaviors: BehaviorRef[]): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

const projectFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['planned', 'active', 'done', 'cancelled'],
    transitions: [
      { from: 'planned', to: 'active' },
      { from: 'active', to: 'done' },
      { from: 'planned', to: 'cancelled' },
      { from: 'active', to: 'cancelled' },
    ],
  },
};

const taskFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['todo', 'doing', 'done', 'dropped'],
    transitions: [
      { from: 'todo', to: 'doing' },
      { from: 'doing', to: 'done' },
      { from: 'todo', to: 'dropped' },
      { from: 'doing', to: 'dropped' },
    ],
  },
};

// A step of a run is done when it passes or fails, and failed is a
// failure; a run is open until it completes or fails, and may be retried.
const stepFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['todo', 'doing', 'passed', 'failed'],
    transitions: [
      { from: 'todo', to: 'doing' },
      { from: 'doing', to: 'passed' },
      { from: 'doing', to: 'failed' },
    ],
    outcomes: { failed: 'failure' },
  },
};

const runFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['open', 'completed', 'failed'],
    transitions: [
      { from: 'open', to: 'completed' },
      { from: 'open', to: 'failed' },
      { from: 'failed', to: 'open' },
    ],
  },
};

const closeWhenTasksFinish = { when: { allTerminal: { schema: 'Task', link: 'project' } }, then: { transition: 'done' } };
const completeWhenStepsPass = { when: { allTerminal: { schema: 'Step', link: 'run', outcomes: ['success'] } }, then: { transition: 'completed' } };
const failWhenAStepFails = { when: { anyTerminal: { schema: 'Step', link: 'run', outcomes: ['failure'] } }, then: { transition: 'failed' } };
const startProjectWithTask = { when: { enters: 'doing' }, then: { link: 'project', transition: 'active' } };

function projects(extra: BehaviorRef[] = [], rules: unknown[] = [closeWhenTasksFinish]): Record<string, unknown> {
  return schema('Project', [projectFlow, ...extra, { name: 'Reactions', config: { rules } }]);
}

function tasks(rules: unknown[] = [startProjectWithTask]): Record<string, unknown> {
  return schema('Task', [taskFlow, { name: 'Links', config: { links: { project: { schema: 'Project' } } } }, { name: 'Reactions', config: { rules } }]);
}

function runs(rules: unknown[] = [completeWhenStepsPass, failWhenAStepFails]): Record<string, unknown> {
  return schema('Run', [runFlow, { name: 'Reactions', config: { rules } }]);
}

function steps(rules?: unknown[]): Record<string, unknown> {
  return schema('Step', [stepFlow, { name: 'Links', config: { links: { run: { schema: 'Run' } } } }, ...(rules === undefined ? [] : [{ name: 'Reactions', config: { rules } }])]);
}

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

function status(engine: Engine, schema: string, id: string): unknown {
  return engine.instances.get(alice, schema, id)?.behaviors.Workflow.status;
}

function move(engine: Engine, schema: string, id: string, to: string): EngineEvent {
  engine.instances.invoke(alice, schema, id, 'transition', { to });
  return last(engine, schema, id);
}

function last(engine: Engine, schema: string, id: string): EngineEvent {
  const events = engine.events.read(alice, { schema, instanceId: id }).events;
  return events[events.length - 1];
}

function subscriptions(engine: Engine): SubscriptionStatus[] {
  return engine.runner.status().subscriptions;
}

for (const driver of drivers) {
  function open(options: Partial<EngineOptions> = {}): Engine {
    return openTestEngine({ driver, runner: { principal: runnerPrincipal }, ...options });
  }

  // Project p1; tasks t1 and t2, linked to it.
  function world(options: Partial<EngineOptions> = {}, extra: BehaviorRef[] = []): Engine {
    const engine = open(options);
    publish(engine, projects(extra));
    publish(engine, tasks());
    engine.instances.create(alice, 'Project', { title: 'Launch' }, { id: 'p1' });
    for (const id of ['t1', 't2']) {
      engine.instances.create(alice, 'Task', { title: id }, { id });
      engine.instances.invoke(alice, 'Task', id, 'link', { name: 'project', id: 'p1' });
    }
    return engine;
  }

  describe(`Reactions (${driver})`, () => {
    test('a task that starts moves its project to active, as the runner, and the move records its cause', () => {
      const engine = world();
      const started = move(engine, 'Task', 't1', 'doing');
      assert.equal(status(engine, 'Project', 'p1'), 'planned');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'active');
      const moved = last(engine, 'Project', 'p1');
      assert.deepEqual(
        [moved.kind, moved.actor, moved.change, moved.cause],
        [
          'operation',
          'runner',
          { behavior: 'Workflow', operation: 'transition', params: { to: 'active' }, patch: { behaviors: { Workflow: { status: 'active' } } } },
          { behavior: 'Reactions', event: started.cursor, depth: 1 },
        ]
      );

      // The second task finds the project active already: nothing to do, and no failure.
      move(engine, 'Task', 't2', 'doing');
      engine.runner.runDue();
      assert.equal(last(engine, 'Project', 'p1').cursor, moved.cursor);
      assert.deepEqual(
        subscriptions(engine).map((subscription) => [subscription.schema, subscription.state, subscription.failure]),
        [
          ['Project', 'active', null],
          ['Task', 'active', null],
        ]
      );
    });

    test('a project whose tasks are all in a terminal state is done', () => {
      const engine = world();
      move(engine, 'Task', 't1', 'doing');
      move(engine, 'Task', 't2', 'doing');
      move(engine, 'Task', 't1', 'done');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'active');
      const dropped = move(engine, 'Task', 't2', 'dropped');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'done');
      assert.deepEqual(last(engine, 'Project', 'p1').cause, { behavior: 'Reactions', event: dropped.cursor, depth: 1 });
    });

    test('a task deleted or linked elsewhere settles the project it linked to before', () => {
      const engine = world();
      move(engine, 'Task', 't1', 'doing');
      move(engine, 'Task', 't1', 'done');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'active');
      engine.instances.delete(alice, 'Task', 't2');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'done');

      engine.instances.create(alice, 'Project', { title: 'Docs' }, { id: 'p2' });
      engine.instances.create(alice, 'Project', { title: 'Site' }, { id: 'p3' });
      for (const id of ['t3', 't4']) {
        engine.instances.create(alice, 'Task', { title: id }, { id });
        engine.instances.invoke(alice, 'Task', id, 'link', { name: 'project', id: 'p2' });
      }
      move(engine, 'Task', 't3', 'doing');
      move(engine, 'Task', 't3', 'done');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p2'), 'active');
      engine.instances.invoke(alice, 'Task', 't4', 'link', { name: 'project', id: 'p3' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Project', 'p2'), status(engine, 'Project', 'p3')], ['done', 'planned']);
    });

    test('a project with no tasks left, or whose guards refuse, stays as it is', () => {
      const engine = world({}, [{ name: 'Dependencies' }]);
      move(engine, 'Task', 't1', 'doing');
      engine.runner.runDue();
      engine.instances.invoke(alice, 'Task', 't1', 'unlink', { name: 'project' });
      engine.instances.invoke(alice, 'Task', 't2', 'unlink', { name: 'project' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'active');

      // p2 waits on p3, an open blocker: Dependencies vetoes its move to done.
      engine.instances.create(alice, 'Project', { title: 'Docs' }, { id: 'p2' });
      engine.instances.create(alice, 'Project', { title: 'Site' }, { id: 'p3' });
      engine.instances.invoke(alice, 'Project', 'p2', 'addBlocker', { id: 'p3' });
      engine.instances.invoke(alice, 'Task', 't1', 'link', { name: 'project', id: 'p2' });
      move(engine, 'Project', 'p2', 'active');
      move(engine, 'Task', 't1', 'done');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p2'), 'active');
      assert.ok(subscriptions(engine).every((subscription) => subscription.state === 'active' && subscription.failure === null));
    });

    test('allTerminal with outcomes fires only when every linking instance is in a terminal state whose outcome it lists', () => {
      const engine = open();
      publish(engine, runs([completeWhenStepsPass]));
      publish(engine, steps());
      for (const [run, ids] of [['r1', ['s1', 's2']], ['r2', ['s3']]] as const) {
        engine.instances.create(alice, 'Run', { title: run }, { id: run });
        for (const id of ids) {
          engine.instances.create(alice, 'Step', { title: id }, { id });
          engine.instances.invoke(alice, 'Step', id, 'link', { name: 'run', id: run });
          move(engine, 'Step', id, 'doing');
        }
      }
      engine.runner.runDue();
      move(engine, 'Step', 's1', 'passed');
      move(engine, 'Step', 's2', 'failed');
      const passed = move(engine, 'Step', 's3', 'passed');
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Run', 'r1'), status(engine, 'Run', 'r2')], ['open', 'completed']);
      assert.deepEqual(last(engine, 'Run', 'r2').cause, { behavior: 'Reactions', event: passed.cursor, depth: 1 });
    });

    test('anyTerminal fires when a linking instance enters a terminal state with a listed outcome, or is linked here in one, and on nothing else', () => {
      const engine = open();
      publish(engine, runs([failWhenAStepFails]));
      publish(engine, steps());
      for (const id of ['r1', 'r2', 'r3']) {
        engine.instances.create(alice, 'Run', { title: id }, { id });
      }
      for (const id of ['s1', 's2', 's3']) {
        engine.instances.create(alice, 'Step', { title: id }, { id });
        move(engine, 'Step', id, 'doing');
      }
      for (const id of ['s1', 's2']) {
        engine.instances.invoke(alice, 'Step', id, 'link', { name: 'run', id: 'r1' });
      }
      move(engine, 'Step', 's1', 'passed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open', 'a success is not a listed outcome');
      const failed = move(engine, 'Step', 's2', 'failed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'failed');
      assert.deepEqual(last(engine, 'Run', 'r1').cause, { behavior: 'Reactions', event: failed.cursor, depth: 1 });

      // A retried run is not failed again by a change of the failed step that moves neither its status nor its link.
      move(engine, 'Run', 'r1', 'open');
      engine.instances.update(alice, 'Step', 's2', { title: 'flaky' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open');

      // A failed step linked to a run, or moved to another, fails that run.
      move(engine, 'Step', 's3', 'failed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r3'), 'open', 'a step that links nowhere has no run to fail');
      engine.instances.invoke(alice, 'Step', 's3', 'link', { name: 'run', id: 'r3' });
      engine.instances.invoke(alice, 'Step', 's2', 'link', { name: 'run', id: 'r2' });
      engine.runner.runDue();
      assert.deepEqual(['r1', 'r2', 'r3'].map((id) => status(engine, 'Run', id)), ['open', 'failed', 'failed']);
      engine.instances.invoke(alice, 'Step', 's2', 'unlink', { name: 'run' });
      move(engine, 'Run', 'r2', 'open');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r2'), 'open', 'an unlink fails nothing');
      assert.ok(subscriptions(engine).every((subscription) => subscription.state === 'active' && subscription.failure === null));
    });

    describe('allTerminal with [success] and anyTerminal with [failure] never complete a run with a failed step, whatever order the events and rules come in', () => {
      // A rule on the step that fails its run too, which runs in the steps' own subscription.
      const failTheRun = { when: { enters: 'failed' }, then: { link: 'run', transition: 'failed' } };
      // One test per combination, each with its own engine: as one test, the
      // sixteen took ~5 s on a GitHub-hosted runner, bun's default timeout. A
      // { timeout } option is no fix: node:test, and bun's, enforce it with a
      // timer, which cannot fire while a synchronous test runs, so it would
      // leave this one with no limit.
      for (const rules of [
        [completeWhenStepsPass, failWhenAStepFails],
        [failWhenAStepFails, completeWhenStepsPass],
      ]) {
        for (const stepRules of [undefined, [failTheRun]]) {
          for (const lastStep of ['passed', 'failed']) {
            for (const eager of [true, false]) {
              const name = [
                `${Object.keys(rules[0].when)[0]} first`,
                stepRules === undefined ? 'no step rule' : 'a step rule fails the run too',
                `the ${lastStep} step moves last`,
                eager ? 'the runner runs between moves' : 'the runner runs once',
              ].join(', ');
              test(name, () => {
                const engine = open();
                publish(engine, runs(rules));
                publish(engine, steps(stepRules));
                engine.instances.create(alice, 'Run', { title: 'r1' }, { id: 'r1' });
                for (const id of ['s1', 's2']) {
                  engine.instances.create(alice, 'Step', { title: id }, { id });
                  engine.instances.invoke(alice, 'Step', id, 'link', { name: 'run', id: 'r1' });
                  move(engine, 'Step', id, 'doing');
                }
                const settle = () => (eager ? engine.runner.runDue() : undefined);
                settle();
                move(engine, 'Step', 's1', lastStep === 'passed' ? 'failed' : 'passed');
                settle();
                move(engine, 'Step', 's2', lastStep);
                engine.runner.runDue();
                const moves = engine.events
                  .read(alice, { schema: 'Run', instanceId: 'r1' })
                  .events.filter((event) => event.kind === 'operation')
                  .map((event) => (event.change as OperationChange).params);
                assert.deepEqual(moves, [{ to: 'failed' }]);
              });
            }
          }
        }
      }
    });

    test('rules on the instance itself chain, and one whose state the instance has left does nothing', () => {
      const engine = open();
      publish(
        engine,
        schema('Order', [
          {
            name: 'Workflow',
            config: {
              states: ['placed', 'paid', 'packed', 'shipped'],
              transitions: [
                { from: 'placed', to: 'paid' },
                { from: 'paid', to: 'packed' },
                { from: 'packed', to: 'shipped' },
              ],
            },
          },
          {
            name: 'Reactions',
            config: {
              rules: [
                { when: { enters: 'paid' }, then: { transition: 'packed' } },
                { when: { enters: 'packed' }, then: { transition: 'shipped' } },
              ],
            },
          },
        ])
      );
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const paid = move(engine, 'Order', 'o1', 'paid');
      engine.runner.runDue();
      assert.equal(status(engine, 'Order', 'o1'), 'shipped');
      const events = engine.events.read(alice, { schema: 'Order', instanceId: 'o1', after: paid.cursor }).events;
      assert.deepEqual(
        events.map((event) => [(event.change as OperationChange).params, event.cause?.depth]),
        [
          [{ to: 'packed' }, 1],
          [{ to: 'shipped' }, 2],
        ]
      );
      assert.equal(events[1].cause?.event, events[0].cursor);

      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      move(engine, 'Order', 'o2', 'paid');
      const packed = move(engine, 'Order', 'o2', 'packed');
      move(engine, 'Order', 'o2', 'shipped');
      engine.runner.runDue();
      assert.equal(last(engine, 'Order', 'o2').cursor, packed.cursor + 1);
      assert.equal(subscriptions(engine)[0].failure, null);
    });

    test('a move the runner principal may not make halts the subscription', () => {
      const clock = testClock();
      const engine = open({ clock, runner: { principal: runnerPrincipal, maxAttempts: 2 } });
      const guarded = clone(projectFlow) as { config: { transitions: Array<{ from: string; to: string; permission?: string }> } };
      guarded.config.transitions[1].permission = 'projects.close';
      publish(engine, schema('Project', [guarded as BehaviorRef, { name: 'Reactions', config: { rules: [closeWhenTasksFinish] } }]));
      publish(engine, tasks());
      engine.instances.create(alice, 'Project', { title: 'Launch' }, { id: 'p1' });
      engine.instances.create(alice, 'Task', { title: 't1' }, { id: 't1' });
      engine.instances.invoke(alice, 'Task', 't1', 'link', { name: 'project', id: 'p1' });
      move(engine, 'Task', 't1', 'doing');
      engine.runner.runDue();
      move(engine, 'Task', 't1', 'done');
      engine.runner.runDue();
      clock.now += 1_000;
      engine.runner.runDue();
      const project = subscriptions(engine).find((subscription) => subscription.schema === 'Project') as SubscriptionStatus;
      assert.equal(project.state, 'halted');
      assert.equal(
        project.failure?.error,
        'forbidden: runner may not move Project p1 from active to done: the transition needs permission projects.close'
      );
      assert.equal(status(engine, 'Project', 'p1'), 'active');
    });

    test('a linked schema that does not link here halts the subscription with what is wrong', () => {
      for (const when of [{ allTerminal: { schema: 'Note', link: 'project' } }, { anyTerminal: { schema: 'Note', link: 'project', outcomes: ['failure'] } }]) {
        const engine = open({ runner: { principal: runnerPrincipal, maxAttempts: 1 } });
        publish(engine, schema('Note', [taskFlow]));
        publish(engine, projects([], [{ when, then: { transition: 'done' } }]));
        engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
        engine.runner.runDue();
        const project = subscriptions(engine).find((subscription) => subscription.schema === 'Project') as SubscriptionStatus;
        assert.deepEqual(
          [project.state, project.failure?.error],
          ['halted', `BehaviorError: behavior Reactions: ${Object.keys(when)[0]} names link project of Note, which has no such link to Project`]
        );
      }
    });

    test("parseConfig refuses rules the type's own configs show are wrong", () => {
      const engine = open();
      const linksTo = (target: string): BehaviorRef => ({ name: 'Links', config: { links: { project: { schema: target } } } });
      const refused = (behaviors: BehaviorRef[]): string =>
        thrown(() => engine.schemas.define(alice, schema('Task', behaviors)), SchemaDocumentError)
          .issues.map((issue) => issue.message)
          .join('; ');
      const reactions = (...rules: unknown[]): BehaviorRef => ({ name: 'Reactions', config: { rules } });
      const prefix = 'type Task: behavior Reactions config: ';
      for (const [behaviors, message] of [
        [[taskFlow, reactions({ when: { enters: 'review' }, then: { transition: 'done' } })], 'rule 1: when.enters "review" is not a state of the type\'s Workflow (todo, doing, done, dropped)'],
        [[taskFlow, reactions({ when: { enters: 'doing' }, then: { transition: 'shipped' } })], 'rule 1: then.transition "shipped" is not a state of the type\'s Workflow (todo, doing, done, dropped)'],
        [[taskFlow, reactions({ when: { enters: 'doing' }, then: { link: 'project', transition: 'active' } })], 'rule 1: then.link project needs Links on the type, which does not list it'],
        [[taskFlow, linksTo('Project'), reactions({ when: { enters: 'doing' }, then: { link: 'parent', transition: 'active' } })], "rule 1: then.link parent is not a link of the type's Links (project)"],
        [[taskFlow, reactions({ when: { enters: 'todo' }, then: { transition: 'done' } })], "rule 1: no transition of the type's Workflow leads from todo to done, so the rule could never move the instance"],
        [[taskFlow, linksTo('Task'), reactions({ when: { allTerminal: { schema: 'Task', link: 'project' } }, then: { transition: 'todo' } })], "rule 1: no transition of the type's Workflow leads to todo, so the rule could never move the instance"],
        [[taskFlow, linksTo('Project'), reactions({ when: { allTerminal: { schema: 'Task', link: 'project' } }, then: { transition: 'done' } })], 'rule 1: when.allTerminal names link project of Task, which has no such link to Task'],
        [
          [taskFlow, linksTo('Project'), reactions({ when: { anyTerminal: { schema: 'Task', link: 'project', outcomes: ['failure'] } }, then: { transition: 'dropped' } })],
          'rule 1: when.anyTerminal names link project of Task, which has no such link to Task',
        ],
        [
          [taskFlow, linksTo('Task'), reactions({ when: { anyTerminal: { schema: 'Task', link: 'project', outcomes: ['failure'] } }, then: { transition: 'todo' } })],
          "rule 1: no transition of the type's Workflow leads to todo, so the rule could never move the instance",
        ],
      ] as Array<[BehaviorRef[], string]>) {
        assert.equal(refused(behaviors), prefix + message);
      }

      const loop: BehaviorRef = {
        name: 'Workflow',
        config: { states: ['open', 'held'], transitions: [{ from: 'open', to: 'held' }, { from: 'held', to: 'open' }] },
      };
      assert.equal(
        refused([loop, reactions({ when: { enters: 'open' }, then: { transition: 'held' } }, { when: { enters: 'held' }, then: { transition: 'open' } })]),
        `${prefix}its rules on the instance itself move it round the states open, held, open for ever`
      );
      assert.equal(refused([reactions({ when: { enters: 'open' }, then: { transition: 'held' } })]), 'type Task: behavior Reactions requires behavior Workflow, which the type does not list');
      // The meta-schema holds a rule to one when, as the configSchema does.
      assert.match(
        refused([taskFlow, reactions({ when: { enters: 'doing', allTerminal: { schema: 'Task', link: 'project' } }, then: { transition: 'done' } })]),
        /must NOT have more than 1 properties/
      );
      // anyTerminal names its outcomes, and an outcome is one of three.
      assert.match(refused([taskFlow, reactions({ when: { anyTerminal: { schema: 'Project', link: 'project' } }, then: { transition: 'dropped' } })]), /must have required property 'outcomes'/);
      assert.match(refused([taskFlow, reactions({ when: { allTerminal: { schema: 'Project', link: 'project', outcomes: ['lost'] } }, then: { transition: 'done' } })]), /must be one of "success", "failure", "neutral"/);
      // A rule on a task that fails its parent task when a subtask fails passes: the link is the type's own.
      engine.schemas.define(
        alice,
        schema('Task', [taskFlow, linksTo('Task'), reactions({ when: { anyTerminal: { schema: 'Task', link: 'project', outcomes: ['failure'] } }, then: { transition: 'dropped' } })])
      );
    });

    test('configChange: rules may change, and Reactions may be added to and removed from a schema with instances', () => {
      const engine = open();
      publish(engine, schema('Project', [projectFlow]));
      engine.instances.create(alice, 'Project', { title: 'Launch' }, { id: 'p1' });
      publish(engine, projects([], [{ when: { enters: 'active' }, then: { transition: 'done' } }]));
      publish(engine, projects([], [{ when: { enters: 'planned' }, then: { transition: 'active' } }]));
      publish(engine, schema('Project', [projectFlow]));
      assert.equal(engine.schemas.live(alice, 'Project')?.version, 4);
    });
  });
}

function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
