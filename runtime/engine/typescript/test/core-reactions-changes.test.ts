// Reactions' changes (D56): a rule fires when a create, an update or an
// operation of the instance moves a field's value, an own field or a
// behavior's by qualified name with a member path after it, to to and
// from from when they are given. A create moves a field from absent and a
// delete moves nothing. A value the event keeps in the value store is
// compared once loaded, a rule on the instance itself acts only while the
// field still holds to, and changes rules on Workflow.status chain, to the
// runner's depth limit, and join enters rules' cycle check. parseConfig
// refuses an unknown field, a volatile one and Workflow.enteredAt.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { SchemaDocumentError, type Engine, type EngineEvent, type OperationChange } from '../dist/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown, type TestEngineOptions } from './helpers.ts';
import { runnerPrincipal } from './runner-fixtures.ts';

afterEach(cleanup);

type BehaviorRef = { name: string; config?: unknown };

// A ticket is triaged, escalated or closed; an escalated one goes back to
// triage. Its priority and body are its own.
const ticketFlow: BehaviorRef = {
  name: 'Workflow',
  config: {
    states: ['new', 'triaged', 'escalated', 'closed'],
    transitions: [
      { from: 'new', to: 'triaged' },
      { from: 'new', to: 'escalated' },
      { from: 'triaged', to: 'escalated' },
      { from: 'escalated', to: 'triaged' },
      { from: 'triaged', to: 'closed' },
      { from: 'escalated', to: 'closed' },
    ],
  },
};

const projectFlow: BehaviorRef = {
  name: 'Workflow',
  config: { states: ['planned', 'active', 'done'], transitions: [{ from: 'planned', to: 'active' }, { from: 'active', to: 'done' }] },
};

const linksToProject: BehaviorRef = { name: 'Links', config: { links: { project: { schema: 'Project' } } } };

function ticketSchema(behaviors: BehaviorRef[]): Record<string, unknown> {
  const document = schemaDocument('Ticket', [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'priority', typeRef: { name: 'string' } },
    { name: 'body', typeRef: { name: 'string' } },
  ]) as { types: Record<string, Record<string, unknown>> };
  document.types.Ticket.behaviors = behaviors;
  return document;
}

function tickets(rules: unknown[], extra: BehaviorRef[] = []): Record<string, unknown> {
  return ticketSchema([ticketFlow, ...extra, { name: 'Reactions', config: { rules } }]);
}

function projects(): Record<string, unknown> {
  const document = schemaDocument('Project', [{ name: 'title', typeRef: { name: 'string' }, required: true }]) as {
    types: Record<string, Record<string, unknown>>;
  };
  document.types.Project.behaviors = [projectFlow];
  return document;
}

// test.Score keeps points and gives them as score, with the level they
// reach: an object a member path reads, which award moves member by
// member.
const score = defineBehavior({
  declaration: {
    name: 'test.Score',
    fields: [{ name: 'score', description: 'The points, and the level they reach.' }],
    operations: [
      {
        name: 'award',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['points'], properties: { points: { type: 'integer', minimum: 1 } } },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  migrations: [{ version: 1, name: 'points', columns: { points: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    award(context, params) {
      const points = Number(context.columns.get().points) + (params.points as number);
      context.columns.set({ points });
      return { points };
    },
  },
  fields: {
    score: (view) => {
      const points = Number(view.columns.get().points);
      return { points, level: points >= 10 ? 'gold' : 'silver' };
    },
  },
});

// Workflow.Tally is an extension's behavior whose name Workflow, the
// core's, prefixes: a field under it resolves to the longer name.
const tally = defineBehavior({
  declaration: {
    name: 'Workflow.Tally',
    fields: [{ name: 'count', description: 'How many bumps.' }],
    operations: [{ name: 'bump', paramsSchema: { type: 'object', additionalProperties: false }, resultSchema: true, writes: true }],
  },
  migrations: [{ version: 1, name: 'count', columns: { count: { type: 'integer', notNull: true, default: 0 } } }],
  operations: {
    bump(context) {
      context.columns.set({ count: Number(context.columns.get().count) + 1 });
      return null;
    },
  },
  fields: { count: (view) => Number(view.columns.get().count) },
});

function publish(engine: Engine, document: Record<string, unknown>): void {
  engine.schemas.define(alice, document);
  engine.schemas.publish(alice, document.name as string);
}

function status(engine: Engine, schema: string, id: string): unknown {
  return engine.instances.get(alice, schema, id)?.behaviors.Workflow.status;
}

function move(engine: Engine, schema: string, id: string, to: string): void {
  engine.instances.invoke(alice, schema, id, 'transition', { to });
}

function events(engine: Engine, schema: string, id: string): EngineEvent[] {
  return engine.events.read(alice, { schema, instanceId: id }).events;
}

// moves lists the Reactions moves of an instance: the state each moved it to, and its cause's depth.
function moves(engine: Engine, schema: string, id: string): Array<[unknown, number | undefined]> {
  return events(engine, schema, id)
    .filter((event) => event.cause?.behavior === 'Reactions')
    .map((event) => [(event.change as OperationChange).params.to, event.cause?.depth]);
}

for (const driver of drivers) {
  function open(options: Partial<TestEngineOptions> = {}): Engine {
    return openTestEngine({ driver, runner: { principal: runnerPrincipal }, ...options });
  }

  describe(`Reactions' changes (${driver})`, () => {
    test('an own field: a create moves it from absent, an update that moves it fires, and one that leaves it fires nothing', () => {
      const engine = open();
      publish(engine, tickets([{ when: { changes: { field: 'priority', to: 'urgent' } }, then: { transition: 'escalated' } }]));
      engine.instances.create(alice, 'Ticket', { title: 'a', priority: 'urgent' }, { id: 'a' });
      engine.instances.create(alice, 'Ticket', { title: 'b', priority: 'low' }, { id: 'b' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Ticket', 'a'), status(engine, 'Ticket', 'b')], ['escalated', 'new']);
      assert.deepEqual(events(engine, 'Ticket', 'a').at(-1)?.cause, { behavior: 'Reactions', event: events(engine, 'Ticket', 'a')[0].cursor, depth: 1 });

      // A patch that leaves priority out, or gives the value it holds, moves nothing.
      engine.instances.update(alice, 'Ticket', 'b', { title: 'b, again' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 'b'), 'new');
      engine.instances.update(alice, 'Ticket', 'b', { priority: 'urgent' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 'b'), 'escalated');
      move(engine, 'Ticket', 'b', 'triaged');
      engine.instances.update(alice, 'Ticket', 'b', { priority: 'urgent', title: 'b, the third' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 'b'), 'triaged');
      assert.deepEqual(moves(engine, 'Ticket', 'b'), [['escalated', 1]]);
    });

    test('from and to: only a move from the one value to the other fires, and a create moves a field from no value', () => {
      const engine = open();
      publish(engine, tickets([{ when: { changes: { field: 'priority', from: 'urgent', to: 'low' } }, then: { transition: 'triaged' } }]));
      engine.instances.create(alice, 'Ticket', { title: 'd', priority: 'low' }, { id: 'd' });
      move(engine, 'Ticket', 'd', 'escalated');
      for (const priority of ['urgent', 'normal', 'urgent']) {
        engine.instances.update(alice, 'Ticket', 'd', { priority });
      }
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 'd'), 'escalated');
      engine.instances.update(alice, 'Ticket', 'd', { priority: 'low' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 'd'), 'triaged');
      // A patch that removes the field moves it to no value, which no to names.
      engine.instances.update(alice, 'Ticket', 'd', { priority: null });
      engine.runner.runDue();
      assert.deepEqual(moves(engine, 'Ticket', 'd'), [['triaged', 1]]);
    });

    test("a behavior's field: Workflow.status as enters hears it, and Comments.commentCount moving from 0, the operation's patch read as a diff", () => {
      const engine = open();
      publish(engine, projects());
      publish(
        engine,
        tickets(
          [
            { when: { changes: { field: 'Workflow.status', to: 'escalated' } }, then: { link: 'project', transition: 'active' } },
            { when: { changes: { field: 'Comments.commentCount', from: 0 } }, then: { transition: 'triaged' } },
          ],
          [{ name: 'Comments' }, linksToProject]
        )
      );
      engine.instances.create(alice, 'Project', { title: 'Launch' }, { id: 'p1' });
      engine.instances.create(alice, 'Ticket', { title: 't1' }, { id: 't1', behaviors: { Links: { project: 'p1' } } });
      // The first comment moves the count from 0 and triages the ticket; a comment's patch holds no status.
      engine.instances.invoke(alice, 'Ticket', 't1', 'comment', { body: 'Seen it.' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Ticket', 't1'), status(engine, 'Project', 'p1')], ['triaged', 'planned']);
      move(engine, 'Ticket', 't1', 'escalated');
      engine.runner.runDue();
      assert.equal(status(engine, 'Project', 'p1'), 'active');
      // A second comment moves the count from 1: the ticket stays escalated.
      engine.instances.invoke(alice, 'Ticket', 't1', 'comment', { body: 'Still there.' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'escalated');
      assert.deepEqual(moves(engine, 'Ticket', 't1'), [['triaged', 1]]);
    });

    test('a delete moves nothing', () => {
      const engine = open();
      publish(engine, projects());
      publish(engine, tickets([{ when: { changes: { field: 'priority' } }, then: { link: 'project', transition: 'active' } }], [linksToProject]));
      for (const id of ['p1', 'p2']) {
        engine.instances.create(alice, 'Project', { title: id }, { id });
      }
      // t2's create sets its priority before it links anywhere, so it moves no project.
      engine.instances.create(alice, 'Ticket', { title: 't1' }, { id: 't1', behaviors: { Links: { project: 'p1' } } });
      engine.instances.create(alice, 'Ticket', { title: 't2', priority: 'low' }, { id: 't2' });
      engine.runner.runDue();
      // A link's patch holds no priority.
      engine.instances.invoke(alice, 'Ticket', 't2', 'link', { name: 'project', id: 'p2' });
      engine.runner.runDue();
      engine.instances.delete(alice, 'Ticket', 't2');
      engine.instances.update(alice, 'Ticket', 't1', { priority: 'high' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Project', 'p1'), status(engine, 'Project', 'p2')], ['active', 'planned']);
    });

    test('a member path below a behavior field whose value is an object, the behavior the longest name that prefixes it', () => {
      const engine = open({ metaSchema: openMetaSchema(), behaviors: [score, tally] });
      publish(
        engine,
        tickets(
          [
            { when: { changes: { field: 'test.Score.score.level', to: 'gold' } }, then: { transition: 'escalated' } },
            { when: { changes: { field: 'Workflow.Tally.count', to: 2 } }, then: { transition: 'triaged' } },
          ],
          [{ name: 'test.Score' }, { name: 'Workflow.Tally' }]
        )
      );
      engine.instances.create(alice, 'Ticket', { title: 't1' }, { id: 't1' });
      // 4 points leave the level silver: the patch moves points alone.
      engine.instances.invoke(alice, 'Ticket', 't1', 'award', { points: 4 });
      assert.deepEqual((events(engine, 'Ticket', 't1').at(-1)?.change as OperationChange).patch, { behaviors: { 'test.Score': { score: { points: 4 } } } });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'new');
      engine.instances.invoke(alice, 'Ticket', 't1', 'award', { points: 6 });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'escalated');
      for (let bump = 0; bump < 2; bump += 1) {
        engine.instances.invoke(alice, 'Ticket', 't1', 'bump');
      }
      engine.runner.runDue();
      assert.deepEqual(moves(engine, 'Ticket', 't1'), [
        ['escalated', 1],
        ['triaged', 1],
      ]);
    });

    test('a value the event keeps in the value store is compared once loaded, never as its ref', () => {
      const engine = open({ values: { thresholdBytes: 1024 } });
      const long = 'x'.repeat(2048);
      publish(
        engine,
        tickets([
          { when: { changes: { field: 'body', to: long } }, then: { transition: 'escalated' } },
          { when: { changes: { field: 'body', from: long } }, then: { transition: 'closed' } },
        ])
      );
      engine.instances.create(alice, 'Ticket', { title: 't1', body: long }, { id: 't1' });
      assert.deepEqual(events(engine, 'Ticket', 't1')[0].valueRefs, ['/data/body']);
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'escalated');
      // The same long body again, beside a new title: the patch's ref is the value the ticket holds.
      move(engine, 'Ticket', 't1', 'triaged');
      engine.instances.update(alice, 'Ticket', 't1', { title: 'Renamed', body: long });
      assert.deepEqual(events(engine, 'Ticket', 't1').at(-1)?.valueRefs, ['/data/body']);
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'triaged');
      engine.instances.update(alice, 'Ticket', 't1', { body: 'Short now.' });
      engine.runner.runDue();
      assert.equal(status(engine, 'Ticket', 't1'), 'closed');
    });

    test('a rule with to on the instance itself acts only while the field still holds it; one through a link does not ask', () => {
      const engine = open();
      publish(engine, projects());
      publish(
        engine,
        tickets(
          [
            { when: { changes: { field: 'priority', to: 'urgent' } }, then: { transition: 'escalated' } },
            { when: { changes: { field: 'priority', to: 'urgent' } }, then: { link: 'project', transition: 'active' } },
          ],
          [linksToProject]
        )
      );
      engine.instances.create(alice, 'Project', { title: 'Launch' }, { id: 'p1' });
      engine.instances.create(alice, 'Ticket', { title: 't1' }, { id: 't1', behaviors: { Links: { project: 'p1' } } });
      engine.instances.update(alice, 'Ticket', 't1', { priority: 'urgent' });
      engine.instances.update(alice, 'Ticket', 't1', { priority: 'low' });
      engine.runner.runDue();
      assert.deepEqual([status(engine, 'Ticket', 't1'), status(engine, 'Project', 'p1')], ['new', 'active']);
    });

    test('rules on Workflow.status chain on the instance itself, each move one deeper, and stop at the depth limit', () => {
      const rules = [
        { when: { changes: { field: 'Workflow.status', to: 'triaged' } }, then: { transition: 'escalated' } },
        { when: { changes: { field: 'Workflow.status', from: 'triaged' } }, then: { transition: 'closed' } },
      ];
      for (const [maxDepth, moved] of [
        [8, ['closed', [['escalated', 1], ['closed', 2]]]],
        [1, ['escalated', [['escalated', 1]]]],
      ] as Array<[number, [string, Array<[string, number]>]]>) {
        const engine = open({ runner: { principal: runnerPrincipal, maxDepth } });
        publish(engine, tickets(rules));
        engine.instances.create(alice, 'Ticket', { title: 't1' }, { id: 't1' });
        move(engine, 'Ticket', 't1', 'triaged');
        engine.runner.runDue();
        assert.deepEqual([status(engine, 'Ticket', 't1'), moves(engine, 'Ticket', 't1')], moved, `maxDepth ${maxDepth}`);
      }
    });

    test('guidance names each changes rule, and the writes that can set one off say the status may move after the commit', () => {
      const engine = open();
      publish(
        engine,
        tickets(
          [
            { when: { changes: { field: 'priority', to: 'urgent' } }, then: { transition: 'escalated' } },
            { when: { changes: { field: 'Comments.commentCount', from: 0 } }, then: { transition: 'triaged' } },
          ],
          [{ name: 'Comments' }]
        )
      );
      const described = engine.tools.describe(alice, 'Ticket');
      assert.equal(
        described.behaviors.find((behavior) => behavior.name === 'Reactions')?.summary,
        'After a change commits, the runner moves statuses by rules, through transition and its guards: when priority changes to "urgent", it moves to escalated; when Comments.commentCount changes from 0, it moves to triaged. A move a guard vetoes, or that no transition allows, is left undone.'
      );
      const success = (name: string) => described.operations.find((operation) => operation.name === name)?.guidance.success ?? '';
      for (const name of ['transition', 'create', 'update', 'comment']) {
        assert.match(success(name), /Reactions' rules on Ticket may move it, or the instances it links to, again after the commit\./, name);
      }
      assert.doesNotMatch(success('listComments'), /Reactions/);
    });

    test("parseConfig refuses a field the type does not have, a volatile one and Workflow.enteredAt, and a changes rule's cycles on the status", () => {
      const engine = open({ metaSchema: openMetaSchema(), behaviors: [tally] });
      const refused = (rules: unknown[], extra: BehaviorRef[] = [linksToProject]): string =>
        thrown(() => engine.schemas.define(alice, tickets(rules, extra)), SchemaDocumentError)
          .issues.map((issue) => issue.message)
          .join('; ');
      const changes = (field: string, more: Record<string, unknown> = {}) => ({ when: { changes: { field, ...more } }, then: { transition: 'escalated' } });
      const prefix = 'type Ticket: behavior Reactions config: rule 1: ';
      for (const [rule, message] of [
        [changes('severity'), 'when.changes names severity, which is not a field of Ticket (title, priority, body)'],
        [changes('Lease.holder'), 'when.changes names Lease.holder, which is not a field of a behavior the type lists (Workflow, Links, Reactions)'],
        [changes('Workflow.state'), 'when.changes names Workflow.state, but Workflow declares no field state (status, enteredAt)'],
        [changes('Reactions.rules'), 'when.changes names Reactions.rules, but Reactions declares no field rules (none)'],
        [
          changes('Links.targets.project'),
          'when.changes names Links.targets.project, a volatile field: it moves at a read, with no event of the instance to hear; a rule hears what moves it with holds, allTerminal, anyTerminal or revised',
        ],
        [changes('Workflow.enteredAt'), 'when.changes names Workflow.enteredAt, which every move of the status moves; a rule on Workflow.status hears those'],
        [changes('Workflow.status', { to: 'lost' }), 'when.changes.to "lost" is not a state of the type\'s Workflow (new, triaged, escalated, closed)'],
        [changes('Workflow.status', { from: 3 }), 'when.changes.from 3 is not a state of the type\'s Workflow (new, triaged, escalated, closed)'],
        [changes('Workflow.status', { to: 'closed' }), "no transition of the type's Workflow leads from closed to escalated, so the rule could never move the instance"],
      ] as Array<[unknown, string]>) {
        assert.equal(refused([rule]), prefix + message);
      }
      // Dependencies' blocked moves with the blockers: volatile too.
      assert.match(refused([changes('Dependencies.blocked')], [{ name: 'Dependencies' }]), /names Dependencies\.blocked, a volatile field/);
      // A changes rule on the status with to moves on from that state; without it, from each state a transition to its state leaves.
      const back = { when: { enters: 'escalated' }, then: { transition: 'triaged' } };
      assert.equal(
        refused([changes('Workflow.status', { to: 'triaged' }), back]),
        'type Ticket: behavior Reactions config: its rules on the instance itself move it round the states triaged, escalated, triaged for ever'
      );
      assert.equal(
        refused([changes('Workflow.status'), back]),
        'type Ticket: behavior Reactions config: its rules on the instance itself move it round the states escalated, triaged, escalated for ever'
      );
      // The configSchema holds changes to a field and its two values.
      assert.match(refused([{ when: { changes: { to: 'urgent' } }, then: { transition: 'escalated' } }]), /must have required property 'field'/);
      assert.match(refused([{ when: { changes: { field: 'priority', into: 'urgent' } }, then: { transition: 'escalated' } }]), /must NOT have additional properties/);
      assert.match(refused([changes('Workflow..status')]), /must match pattern/);
      // These pass: a field of the longer behavior name, a rule through a link, and a rule from a state.
      engine.schemas.define(
        alice,
        tickets(
          [
            changes('Workflow.Tally.count', { to: 1 }),
            { when: { changes: { field: 'Workflow.status', to: 'closed' } }, then: { link: 'project', transition: 'done' } },
            { when: { changes: { field: 'Workflow.status', from: 'new' } }, then: { transition: 'closed' } },
          ],
          [linksToProject, { name: 'Workflow.Tally' }]
        )
      );
    });
  });
}
