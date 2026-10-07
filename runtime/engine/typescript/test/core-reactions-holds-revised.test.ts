// Reactions' holds and revised. holds fires when a change of a linked
// instance makes an all or any rollup of the instance hold: on the edge
// from not holding to holding, judged with the other linked instances as
// they are when the rule runs, and up a tree of the type's own schema to
// the runner's depth limit. revised fires when the instance a link points
// to gains a revision, or a release when its schema composes Branches,
// on each instance whose link points there, of a link pinned to what the
// target gained only the ones the target has moved past. parseConfig
// holds both to the type's
// Rollups and Links, and a link to a schema with neither Revisions nor
// Branches halts the subscription.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { SchemaDocumentError, type Engine, type EngineEvent, type EngineOptions, type SubscriptionStatus } from '../dist/index.js';
import { Calls, recipeDocument, type Commit } from './branches-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown } from './helpers.ts';
import { runnerPrincipal } from './runner-fixtures.ts';

afterEach(cleanup);

type BehaviorRef = { name: string; config?: unknown };

function schema(name: string, behaviors: BehaviorRef[]): Record<string, unknown> {
  const document = schemaDocument(name, [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types[name].behaviors = behaviors;
  return document;
}

// A step passes or fails, failed a failure; a run is open until it
// completes or fails, and a failed run may be opened again.
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
      { from: 'completed', to: 'open' },
    ],
  },
};

const runRollups: BehaviorRef = {
  name: 'Rollups',
  config: {
    rollups: {
      stepsPassed: { schema: 'Step', link: 'run', function: 'all', outcomes: ['success'] },
      stepFailed: { schema: 'Step', link: 'run', function: 'any', outcomes: ['failure'] },
      steps: { schema: 'Step', link: 'run', function: 'count' },
    },
  },
};

const completeWhenPassed = { when: { holds: 'stepsPassed' }, then: { transition: 'completed' } };
const failWhenOneFailed = { when: { holds: 'stepFailed' }, then: { transition: 'failed' } };

// Work is done against a spec, and goes back for review when it changes.
const workFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['todo', 'review', 'done'],
    transitions: [
      { from: 'todo', to: 'review' },
      { from: 'review', to: 'todo' },
      { from: 'todo', to: 'done' },
      { from: 'done', to: 'review' },
    ],
  },
};

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

function status(engine: Engine, schema: string, id: string): unknown {
  return engine.instances.get(alice, schema, id)?.data.status;
}

function move(engine: Engine, schema: string, id: string, ...to: string[]): EngineEvent {
  for (const state of to) {
    engine.instances.invoke(alice, schema, id, 'transition', { to: state });
  }
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

  // Step links to its Run; Run r1 has steps s1 and s2.
  function runs(rules: unknown[] = [completeWhenPassed, failWhenOneFailed], options: Partial<EngineOptions> = {}): Engine {
    const engine = open(options);
    publish(engine, schema('Step', [stepFlow, { name: 'Links', config: { links: { run: { schema: 'Run' } } } }]));
    publish(engine, schema('Run', [runFlow, runRollups, { name: 'Reactions', config: { rules } }]));
    engine.instances.create(alice, 'Run', { title: 'Nightly' }, { id: 'r1' });
    for (const id of ['s1', 's2']) {
      engine.instances.create(alice, 'Step', { title: id }, { id, behaviors: { Links: { run: 'r1' } } });
    }
    return engine;
  }

  describe(`Reactions: holds (${driver})`, () => {
    test('a change that makes an all rollup hold sets the rule off, once: a later change while it holds does not', () => {
      const engine = runs();
      move(engine, 'Step', 's1', 'doing', 'passed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open');
      const passed = move(engine, 'Step', 's2', 'doing', 'passed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'completed');
      const completed = last(engine, 'Run', 'r1');
      assert.deepEqual(completed.cause, { behavior: 'Reactions', event: passed.cursor, depth: 1 });
      // Opened again by hand, it stays open: a step's update moves no step
      // into the rollup, so the rollup, holding already, has no edge.
      move(engine, 'Run', 'r1', 'open');
      engine.instances.update(alice, 'Step', 's1', { title: 'Build again' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open');
    });

    test('an any rollup fires when it comes to hold, not again while it holds, and again once it has stopped', () => {
      const engine = runs();
      move(engine, 'Step', 's1', 'doing', 'failed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'failed');
      // Retried, the run stays open when s2 fails too: s1's failure still holds the rollup.
      move(engine, 'Run', 'r1', 'open');
      move(engine, 'Step', 's2', 'doing', 'failed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open');
      // Both failed steps leave the run: unlinking the second ends the
      // rollup's hold, and fires nothing; stepsPassed over no step sets no
      // rule off either.
      engine.instances.invoke(alice, 'Step', 's1', 'unlink', { name: 'run' });
      engine.instances.invoke(alice, 'Step', 's2', 'unlink', { name: 'run' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'open');
      // A new step that fails makes it hold again.
      engine.instances.create(alice, 'Step', { title: 's3' }, { id: 's3', behaviors: { Links: { run: 'r1' } } });
      move(engine, 'Step', 's3', 'doing', 'failed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'failed');
    });

    test('a step linked in a state it counts in, or linked away, moves the rollup as its link does', () => {
      const engine = runs();
      engine.instances.create(alice, 'Run', { title: 'Weekly' }, { id: 'r2' });
      move(engine, 'Step', 's1', 'doing', 'passed');
      move(engine, 'Step', 's2', 'doing');
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Run', 'r1'), status(engine, 'Run', 'r2')], ['open', 'open']);
      // s2, still doing, moves to r2: r1's rollup holds now, r2's does not.
      engine.instances.invoke(alice, 'Step', 's2', 'link', { name: 'run', id: 'r2' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Run', 'r1'), status(engine, 'Run', 'r2')], ['completed', 'open']);
      // A passed step linked to r2 changes nothing there while s2 is doing.
      engine.instances.invoke(alice, 'Step', 's1', 'link', { name: 'run', id: 'r2' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r2'), 'open');
      move(engine, 'Step', 's2', 'passed');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r2'), 'completed');
    });

    test('an all rollup sets the rule off over at least one instance: the delete of the last step that kept it out fires when a passed step is left, not when none is', () => {
      const engine = runs();
      move(engine, 'Step', 's1', 'doing', 'passed');
      move(engine, 'Step', 's2', 'doing');
      engine.runner.runDue();
      engine.instances.delete(alice, 'Step', 's2');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r1'), 'completed');
      // r2's only step goes: Rollups' all holds over none, as its gate has
      // it, but no rule fires on that.
      engine.instances.create(alice, 'Run', { title: 'Weekly' }, { id: 'r2' });
      engine.instances.create(alice, 'Step', { title: 's3' }, { id: 's3', behaviors: { Links: { run: 'r2' } } });
      move(engine, 'Step', 's3', 'doing');
      engine.instances.delete(alice, 'Step', 's3');
      engine.runner.runDue();
      assert.equal(status(engine, 'Run', 'r2'), 'open');
      assert.equal((engine.instances.get(alice, 'Run', 'r2')?.data.rollups as Record<string, unknown>).stepsPassed, true);
    });

    test('up a tree of its own schema, each parent completes as its last subtask does, to the depth limit', () => {
      const taskFlow: BehaviorRef = {
        name: 'Workflow',
        config: { states: ['todo', 'doing', 'done'], transitions: [{ from: 'todo', to: 'doing' }, { from: 'doing', to: 'done' }] },
      };
      const tasks = schema('Task', [
        taskFlow,
        { name: 'Links', config: { links: { parent: { schema: 'Task' } } } },
        { name: 'Rollups', config: { rollups: { subtasksDone: { schema: 'Task', link: 'parent', function: 'all' } } } },
        { name: 'Reactions', config: { rules: [{ when: { holds: 'subtasksDone' }, then: { transition: 'done' } }] } },
      ]);
      for (const [maxDepth, moved] of [
        [8, ['done', 'done', 'done']],
        [1, ['doing', 'done', 'done']],
      ] as Array<[number, string[]]>) {
        const engine = open({ runner: { principal: runnerPrincipal, maxDepth } });
        publish(engine, tasks);
        engine.instances.create(alice, 'Task', { title: 'Launch' }, { id: 't1' });
        engine.instances.create(alice, 'Task', { title: 'Build' }, { id: 't2', behaviors: { Links: { parent: 't1' } } });
        engine.instances.create(alice, 'Task', { title: 'Test' }, { id: 't3', behaviors: { Links: { parent: 't2' } } });
        for (const id of ['t1', 't2', 't3']) {
          move(engine, 'Task', id, 'doing');
        }
        move(engine, 'Task', 't3', 'done');
        engine.runner.runDue();
        assert.deepEqual(['t1', 't2', 't3'].map((id) => status(engine, 'Task', id)), moved, `maxDepth ${maxDepth}`);
        if (maxDepth === 1) {
          assert.deepEqual(subscriptions(engine)[0].lastSkip, { cursor: last(engine, 'Task', 't2').cursor, reason: 'depth' });
        }
      }
    });

    test("parseConfig holds a holds rule to an all or any rollup of the type's Rollups", () => {
      const engine = open();
      publish(engine, schema('Step', [stepFlow, { name: 'Links', config: { links: { run: { schema: 'Run' } } } }]));
      const refused = (behaviors: BehaviorRef[]): string =>
        thrown(() => engine.schemas.define(alice, schema('Run', behaviors)), SchemaDocumentError)
          .issues.map((issue) => issue.message)
          .join('; ');
      const reactions = (...rules: unknown[]): BehaviorRef => ({ name: 'Reactions', config: { rules } });
      const prefix = 'type Run: behavior Reactions config: ';
      assert.equal(refused([runFlow, reactions(completeWhenPassed)]), `${prefix}rule 1: when.holds names rollup stepsPassed of Rollups, which the type does not list`);
      assert.equal(
        refused([runFlow, runRollups, reactions({ when: { holds: 'stepsDone' }, then: { transition: 'completed' } })]),
        `${prefix}rule 1: when.holds names rollup stepsDone, which is not a rollup of the type's Rollups (stepsPassed, stepFailed, steps)`
      );
      assert.equal(
        refused([runFlow, runRollups, reactions({ when: { holds: 'steps' }, then: { transition: 'completed' } })]),
        `${prefix}rule 1: when.holds names rollup steps, a count rollup; holds takes an all or any rollup, which holds or does not`
      );
      const noWayBack: BehaviorRef = { name: 'Workflow', config: { states: ['open', 'completed'], transitions: [{ from: 'completed', to: 'open' }] } };
      assert.equal(
        refused([noWayBack, runRollups, reactions(completeWhenPassed)]),
        `${prefix}rule 1: no transition of the type's Workflow leads to completed, so the rule could never move the instance`
      );
      assert.match(refused([runFlow, runRollups, reactions({ when: { holds: 'Steps' }, then: { transition: 'completed' } })]), /must match pattern/);
    });
  });

  // Spec composes Revisions; Work pins its spec link and links a design
  // without a pin; w1 and w2 pin sp1 at its first revision.
  function specs(rules?: unknown[]): Engine {
    const engine = open();
    publish(engine, schema('Spec', [{ name: 'Revisions' }, { name: 'Comments' }]));
    publish(
      engine,
      schema('Work', [
        workFlow,
        { name: 'Links', config: { links: { spec: { schema: 'Spec', pinned: true }, design: { schema: 'Spec' } } } },
        {
          name: 'Reactions',
          config: {
            rules: rules ?? [
              { when: { revised: { link: 'spec' } }, then: { transition: 'review' } },
              { when: { revised: { link: 'design' } }, then: { transition: 'review' } },
            ],
          },
        },
      ])
    );
    engine.instances.create(alice, 'Spec', { title: 'v1' }, { id: 'sp1' });
    engine.instances.create(alice, 'Spec', { title: 'Look' }, { id: 'sp2' });
    for (const id of ['w1', 'w2']) {
      engine.instances.create(alice, 'Work', { title: id }, { id, behaviors: { Links: { spec: 'sp1' } } });
    }
    engine.instances.create(alice, 'Work', { title: 'w3' }, { id: 'w3', behaviors: { Links: { design: 'sp2' } } });
    return engine;
  }

  describe(`Reactions: revised (${driver})`, () => {
    test("a spec's new revision moves the work pinned to an earlier one, as the runner, and nothing else moves it", () => {
      const engine = specs();
      // A comment is no revision.
      engine.instances.invoke(alice, 'Spec', 'sp1', 'comment', { body: 'Looks right' });
      engine.runner.runDue();
      assert.deepEqual(['w1', 'w2'].map((id) => status(engine, 'Work', id)), ['todo', 'todo']);
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v2' });
      const revised = last(engine, 'Spec', 'sp1');
      engine.runner.runDue();
      assert.deepEqual(['w1', 'w2', 'w3'].map((id) => status(engine, 'Work', id)), ['review', 'review', 'todo']);
      assert.deepEqual([last(engine, 'Work', 'w1').actor, last(engine, 'Work', 'w1').cause], ['runner', { behavior: 'Reactions', event: revised.cursor, depth: 1 }]);
    });

    test('of a pinned link, only the work the target has moved past moves: one pinned to the new revision since is left alone', () => {
      const engine = specs();
      engine.instances.update(alice, 'Spec', 'sp1', { title: 'v2' });
      // Before the runner hears it, w1 is pinned to revision 2.
      engine.instances.invoke(alice, 'Work', 'w1', 'link', { name: 'spec', id: 'sp1' });
      engine.runner.runDue();
      assert.deepEqual(['w1', 'w2'].map((id) => status(engine, 'Work', id)), ['todo', 'review']);
    });

    test('of a link with no pin, every instance that points at the target moves at each revision', () => {
      const engine = specs();
      engine.instances.create(alice, 'Work', { title: 'w4' }, { id: 'w4', behaviors: { Links: { design: 'sp2' } } });
      move(engine, 'Work', 'w4', 'done');
      engine.instances.update(alice, 'Spec', 'sp2', { title: 'New look' });
      engine.runner.runDue();
      assert.deepEqual(['w3', 'w4'].map((id) => status(engine, 'Work', id)), ['review', 'review']);
    });

    test("a release of a schema that composes Branches moves the work linked to it; its other operations do not", () => {
      const engine = open();
      publish(engine, recipeDocument() as unknown as Record<string, unknown>);
      publish(
        engine,
        schema('Work', [
          workFlow,
          { name: 'Links', config: { links: { recipe: { schema: 'Recipe' } } } },
          { name: 'Reactions', config: { rules: [{ when: { revised: { link: 'recipe' } }, then: { transition: 'review' } }] } },
        ])
      );
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      engine.instances.create(alice, 'Work', { title: 'Cook' }, { id: 'w1', behaviors: { Links: { recipe: 'soup' } } });
      const soup = new Calls(engine, 'soup');
      const first: Commit = soup.change('first', { cover: { upsert: [{ photoUrl: 'soup.jpg' }] } });
      engine.instances.update(alice, 'Recipe', 'soup', { title: 'Tomato soup' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Work', 'w1'), 'todo');
      soup.invoke('releaseCommit', { commit: first.id, version: 0 });
      engine.runner.runDue();
      assert.equal(status(engine, 'Work', 'w1'), 'review');
    });

    test('of a link pinned to a release, a release moves only the work pinned to an earlier one; a revision of the target moves every one', () => {
      const engine = open();
      const recipe = recipeDocument();
      recipe.types.Recipe.behaviors?.push({ name: 'Revisions' });
      publish(engine, recipe as unknown as Record<string, unknown>);
      publish(
        engine,
        schema('Work', [
          workFlow,
          { name: 'Links', config: { links: { recipe: { schema: 'Recipe', pinned: 'release' } } } },
          { name: 'Reactions', config: { rules: [{ when: { revised: { link: 'recipe' } }, then: { transition: 'review' } }] } },
        ])
      );
      engine.instances.create(alice, 'Recipe', { title: 'Soup' }, { id: 'soup' });
      const soup = new Calls(engine, 'soup');
      const first: Commit = soup.change('first', { cover: { upsert: [{ photoUrl: 'soup.jpg' }] } });
      soup.invoke('releaseCommit', { commit: first.id, version: 0 });
      for (const id of ['w1', 'w2']) {
        engine.instances.create(alice, 'Work', { title: id }, { id, behaviors: { Links: { recipe: 'soup' } } });
      }
      engine.runner.runDue();
      const second: Commit = soup.change('second', { step: { upsert: [{ instruction: 'Boil', position: 1 }] } });
      soup.invoke('releaseCommit', { commit: second.id, version: 1 });
      // Before the runner hears it, w1 is pinned to release 2.
      engine.instances.invoke(alice, 'Work', 'w1', 'link', { name: 'recipe', id: 'soup' });
      engine.runner.runDue();
      assert.deepEqual(['w1', 'w2'].map((id) => status(engine, 'Work', id)), ['todo', 'review']);
      // A revision is not what the link pins: it moves every instance that points there.
      engine.instances.update(alice, 'Recipe', 'soup', { title: 'Tomato soup' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Work', 'w1'), 'review');
    });

    test('a link to a schema with neither Revisions nor Branches halts the subscription with what is wrong', () => {
      const engine = open({ runner: { principal: runnerPrincipal, maxAttempts: 1 } });
      publish(engine, schemaDocument('Note', [{ name: 'title', typeRef: { name: 'string' }, required: true }]));
      publish(
        engine,
        schema('Work', [
          workFlow,
          { name: 'Links', config: { links: { note: { schema: 'Note' } } } },
          { name: 'Reactions', config: { rules: [{ when: { revised: { link: 'note' } }, then: { transition: 'review' } }] } },
        ])
      );
      engine.instances.create(alice, 'Note', { title: 'n1' }, { id: 'n1' });
      engine.instances.update(alice, 'Note', 'n1', { title: 'n1 again' });
      engine.runner.runDue();
      const work = subscriptions(engine).find((subscription) => subscription.schema === 'Work') as SubscriptionStatus;
      assert.deepEqual(
        [work.state, work.failure?.error],
        ['halted', 'BehaviorError: behavior Reactions: revised names link note to Note, which composes neither Revisions nor Branches, so it gains no revision or release']
      );
    });

    test("parseConfig holds a revised rule to a link of the type's Links", () => {
      const engine = open();
      publish(engine, schema('Spec', [{ name: 'Revisions' }]));
      const refused = (behaviors: BehaviorRef[]): string =>
        thrown(() => engine.schemas.define(alice, schema('Work', behaviors)), SchemaDocumentError)
          .issues.map((issue) => issue.message)
          .join('; ');
      const rule = { name: 'Reactions', config: { rules: [{ when: { revised: { link: 'spec' } }, then: { transition: 'review' } }] } };
      const prefix = 'type Work: behavior Reactions config: ';
      assert.equal(refused([workFlow, rule]), `${prefix}rule 1: when.revised names link spec of Links, which the type does not list`);
      assert.equal(
        refused([workFlow, { name: 'Links', config: { links: { design: { schema: 'Spec' } } } }, rule]),
        `${prefix}rule 1: when.revised names link spec, which is not a link of the type's Links (design)`
      );
      assert.match(refused([workFlow, { name: 'Reactions', config: { rules: [{ when: { revised: {} }, then: { transition: 'review' } }] } }]), /must have required property 'link'/);
    });
  });
}
