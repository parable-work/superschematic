// Timers, named deadlines on an instance (D56): a deadline that starts at
// the create, at each entry of the status into a state, or at a date-time
// field, before it with a negative afterMs; the runner's fire schedule,
// which fires each due deadline once through fireTimer, a keyset past the
// ones whose fire is refused, per schema and namespace; then's move, from
// the state the deadline starts in by default, recorded as moved,
// skipped, vetoed or forbidden; until, which cancels a pending deadline;
// re-entry, a move away and back within one call included; a snooze and a
// reset, which a later write keeps, and their permission; the
// Timers.deadlines field, its events and a Reactions rule that hears a
// fire; its config, its rule for a new version, deletes and its guidance.
// Lease beside it is in the work-queue package's lease-timers test.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  SchemaDocumentError,
  behaviorField,
  type AccessPolicy,
  type Engine,
  type EngineEvent,
  type OperationChange,
  type Principal,
  type ToolGuidance,
} from '../dist/index.js';
import { defineBehavior } from '../dist/testing/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, openTestEngine, schemaDocument, thrown, type TestEngineOptions } from './helpers.ts';
import { testClock, type TestClock } from './runner-fixtures.ts';

afterEach(cleanup);

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** When every test's clock starts. */
const T0 = Date.UTC(2026, 9, 9, 9);

const runner: Principal = { subject: 'runner', permissions: [] };
const lead: Principal = { subject: 'lee', permissions: ['tickets.timers', 'tickets.escalate'] };

// A ticket is open, waits on its customer, is escalated, resolved, or
// closed; most moves go back and forth.
const flow = {
  states: ['open', 'waiting', 'escalated', 'resolved', 'closed'],
  transitions: [
    { from: 'open', to: 'waiting' },
    { from: 'waiting', to: 'open' },
    { from: 'open', to: 'escalated' },
    { from: 'waiting', to: 'escalated' },
    { from: 'escalated', to: 'open' },
    { from: 'open', to: 'resolved' },
    { from: 'waiting', to: 'resolved' },
    { from: 'escalated', to: 'resolved' },
    { from: 'resolved', to: 'open' },
    { from: 'open', to: 'closed' },
    { from: 'resolved', to: 'closed' },
  ],
};

type Behavior = { name: string; config?: unknown };

/** A Ticket schema whose Workflow is flow, or workflow, then Timers with its config (none for null), then extra. */
function ticketSchema(timers: unknown, extra: Behavior[] = [], workflow: unknown = flow, name = 'Ticket'): Record<string, unknown> {
  const document = schemaDocument(name, [
    { name: 'title', typeRef: { name: 'string' }, required: true },
    { name: 'body', typeRef: { name: 'string' } },
    { name: 'visitAt', typeRef: { name: 'Temporal.DateTime' } },
    { name: 'visitDay', typeRef: { name: 'Temporal.Date' } },
  ]) as { types: Record<string, Record<string, unknown>> };
  document.types[name].behaviors = [
    ...(workflow === null ? [] : [{ name: 'Workflow', config: workflow }]),
    ...(timers === null ? [] : [{ name: 'Timers', config: timers }]),
    ...extra,
  ];
  return document;
}

// test.Bounce moves the status to via and back to back in one call, as
// one operation that calls transition twice.
const bounce = defineBehavior({
  declaration: {
    name: 'test.Bounce',
    operations: [
      {
        name: 'bounce',
        paramsSchema: { type: 'object', additionalProperties: false, required: ['via', 'back'], properties: { via: { type: 'string' }, back: { type: 'string' } } },
        resultSchema: true,
        writes: true,
      },
    ],
  },
  operations: {
    bounce(context, params) {
      context.call('Workflow', 'transition', { to: params.via });
      context.call('Workflow', 'transition', { to: params.back });
      return behaviorField(context.instances.get(context.schema, context.id, { fields: ['Workflow.enteredAt'] }), 'Workflow', 'enteredAt');
    },
  },
});

// test.Hold vetoes a fire of an instance whose title starts with stuck,
// as a lease another principal holds does.
const hold = defineBehavior({
  declaration: { name: 'test.Hold', vetoes: [{ code: 'held', description: 'A fire of an instance the test holds.' }] },
  guard(view, request) {
    if (request.kind === 'operation' && request.behavior === 'Timers' && request.operation === 'fireTimer' && String(view.data.title).startsWith('stuck')) {
      return { reason: 'the test holds it', code: 'held' };
    }
    return undefined;
  },
});

type Deadline = { dueAt: number; fired: boolean; firedAt?: number; outcome?: string };

const at = (ms: number): string => new Date(ms).toISOString();

for (const driver of drivers) {
  // opened opens an engine on a test clock at T0 with the runner, and the
  // Ticket schema, and lets the runner find the fire schedule, which runs
  // an interval later.
  function opened(timers: unknown, extra: Behavior[] = [], options: Partial<TestEngineOptions> & { workflow?: unknown } = {}) {
    const { workflow, ...rest } = options;
    const clock = testClock(T0);
    const engine = openTestEngine({ driver, clock, runner: { principal: runner }, metaSchema: openMetaSchema(), behaviors: [bounce, hold], ...rest });
    publish(engine, ticketSchema(timers, extra, workflow));
    engine.runner.runDue();
    return { engine, clock, ...calls(engine, clock) };
  }

  function publish(engine: Engine, document: Record<string, unknown>, namespace?: string): void {
    engine.schemas.define(alice, document, { namespace });
    engine.schemas.publish(alice, String(document.name), { namespace });
  }

  function calls(engine: Engine, clock: TestClock) {
    return {
      create: (id: string, data: Record<string, unknown> = {}) => engine.instances.create(alice, 'Ticket', { title: id, ...data }, { id }),
      invoke: <T = unknown>(id: string, operation: string, params: Record<string, unknown> = {}, who: Principal = alice): T =>
        engine.instances.invoke(who, 'Ticket', id, operation, params) as T,
      move: (id: string, to: string) => engine.instances.invoke(alice, 'Ticket', id, 'transition', { to }),
      deadlines: (id: string) => engine.instances.get(alice, 'Ticket', id)?.behaviors.Timers?.deadlines as Record<string, Deadline> | undefined,
      status: (id: string) => engine.instances.get(alice, 'Ticket', id)?.behaviors.Workflow?.status,
      seq: (id: string) => engine.instances.get(alice, 'Ticket', id)?.seq,
      events: (id: string): EngineEvent[] => engine.events.read(alice, { schema: 'Ticket', instanceId: id }).events,
      // sweep moves the clock on by ms, at least the sweep's interval, and runs what is due.
      sweep: (ms: number) => {
        clock.now += ms;
        return engine.runner.runDue();
      },
    };
  }

  describe(`Timers (${driver})`, () => {
    test("a deadline that starts at an entry arms as the status enters its state, and the runner's sweep fires it once it is due, moving the status on; Timers.deadlines and each event show it", () => {
      const { engine, clock, create, invoke, deadlines, status, seq, events, sweep } = opened({
        deadlines: { respond: { start: { enters: 'open' }, afterMs: 4 * HOUR, then: { transition: 'escalated' } } },
      });
      const created = create('t1');
      assert.deepEqual(created.behaviors.Timers, { deadlines: { respond: { dueAt: T0 + 4 * HOUR, fired: false } } });
      assert.deepEqual((events('t1')[0].change as { behaviors: Record<string, unknown> }).behaviors.Timers, created.behaviors.Timers);

      // Not due: the sweep and a caller's fireTimer change nothing.
      assert.equal(sweep(4 * HOUR - MINUTE).scheduled, 1);
      assert.equal(status('t1'), 'open');
      assert.deepEqual(invoke('t1', 'fireTimer', { deadline: 'respond' }), { fired: false });
      assert.deepEqual([seq('t1'), events('t1').length], [1, 1]);

      sweep(MINUTE);
      const firedAt = clock.now;
      assert.equal(status('t1'), 'escalated');
      assert.deepEqual(deadlines('t1'), { respond: { dueAt: T0 + 4 * HOUR, fired: true, firedAt, outcome: 'moved' } });
      const fire = events('t1').at(-1) as EngineEvent;
      const change = fire.change as OperationChange;
      assert.deepEqual(
        [fire.actor, fire.cause, change.behavior, change.operation, change.params],
        ['runner', { behavior: 'Timers', schedule: 'fire', depth: 1 }, 'Timers', 'fireTimer', { deadline: 'respond' }]
      );
      assert.deepEqual(change.patch, {
        behaviors: { Workflow: { status: 'escalated', enteredAt: firedAt }, Timers: { deadlines: { respond: { fired: true, firedAt, outcome: 'moved' } } } },
      });

      // It fired once: a caller's fireTimer and the next sweeps change nothing.
      assert.deepEqual(invoke('t1', 'fireTimer', { deadline: 'respond' }), { fired: false });
      sweep(HOUR);
      assert.equal(events('t1').length, 2);
      assert.deepEqual(
        engine.runner
          .status()
          .schedules.filter((schedule) => schedule.behavior === 'Timers')
          .map(({ schedule, schema, state, everyMs }) => [schedule, schema, state, everyMs]),
        [['fire', 'Ticket', 'active', 5000]]
      );
      const unknown = thrown(() => invoke('t1', 'fireTimer', { deadline: 'answer' }), BehaviorVetoError);
      assert.deepEqual([unknown.behavior, unknown.vetoCode, unknown.reason], ['Timers', 'no_deadline', 'answer is not a deadline of Ticket\'s Timers (its deadlines: respond)']);
    });

    test('a caller who may write fires a due deadline itself, before the sweep; the sweep then leaves it', () => {
      const { create, invoke, deadlines, status, events, sweep, clock } = opened({
        deadlines: { respond: { start: { enters: 'open' }, afterMs: HOUR, then: { transition: 'escalated' } } },
        sweepMs: 60_000,
      });
      create('t1');
      clock.now += HOUR;
      assert.deepEqual(invoke('t1', 'fireTimer', { deadline: 'respond' }), { fired: true, outcome: 'moved' });
      assert.deepEqual([status('t1'), events('t1').at(-1)?.actor], ['escalated', 'alice']);
      sweep(MINUTE);
      assert.deepEqual([events('t1').length, deadlines('t1')?.respond.firedAt], [2, T0 + HOUR]);
    });

    test('a deadline that starts at the create is due afterMs after it and moves from the initial state alone, recording skipped for an instance that moved on; one without then only fires', () => {
      const { create, move, deadlines, status, sweep } = opened({
        deadlines: {
          close: { start: { create: true }, afterMs: 30 * MINUTE, then: { transition: 'closed' } },
          ping: { start: { create: true }, afterMs: 10 * MINUTE },
        },
      });
      create('t1');
      create('t2');
      move('t2', 'waiting');
      assert.deepEqual(deadlines('t2'), { close: { dueAt: T0 + 30 * MINUTE, fired: false }, ping: { dueAt: T0 + 10 * MINUTE, fired: false } });

      sweep(10 * MINUTE);
      for (const id of ['t1', 't2']) {
        assert.deepEqual(deadlines(id)?.ping, { dueAt: T0 + 10 * MINUTE, fired: true, firedAt: T0 + 10 * MINUTE });
      }
      sweep(20 * MINUTE);
      assert.deepEqual([status('t1'), deadlines('t1')?.close.outcome], ['closed', 'moved']);
      // t2 is waiting, which close does not move from.
      assert.deepEqual([status('t2'), deadlines('t2')?.close], ['waiting', { dueAt: T0 + 30 * MINUTE, fired: true, firedAt: T0 + 30 * MINUTE, outcome: 'skipped' }]);
    });

    test('a deadline that starts at a date-time field is due afterMs after the time it holds, or before it; a new value moves it and clears fired, and clearing the field cancels it', () => {
      const { engine, create, deadlines, status, events, sweep } = opened({
        deadlines: {
          remind: { start: { field: 'visitAt' }, afterMs: -DAY },
          visit: { start: { field: 'visitAt' }, then: { transition: 'waiting', from: ['open'] } },
        },
      });
      const visit = T0 + 3 * DAY;
      create('t1', { visitAt: at(visit) });
      create('t2');
      assert.deepEqual(deadlines('t1'), { remind: { dueAt: visit - DAY, fired: false }, visit: { dueAt: visit, fired: false } });
      assert.deepEqual(deadlines('t2'), {});
      // A value in another offset is the same time.
      engine.instances.update(alice, 'Ticket', 't2', { visitAt: '2026-10-12T11:00:00+02:00' });
      assert.deepEqual(deadlines('t2')?.visit, { dueAt: visit, fired: false });

      sweep(2 * DAY);
      assert.deepEqual(deadlines('t1')?.remind, { dueAt: visit - DAY, fired: true, firedAt: T0 + 2 * DAY });
      // A write that leaves the field as it is keeps the fire.
      engine.instances.update(alice, 'Ticket', 't1', { body: 'Bring the part.' });
      assert.equal(deadlines('t1')?.remind.fired, true);
      // A new time starts both again, fired cleared.
      engine.instances.update(alice, 'Ticket', 't1', { visitAt: at(visit + DAY) });
      assert.deepEqual(deadlines('t1'), { remind: { dueAt: visit, fired: false }, visit: { dueAt: visit + DAY, fired: false } });
      assert.deepEqual((events('t1').at(-1)?.change as { behaviors: unknown }).behaviors, {
        Timers: { deadlines: { remind: { dueAt: visit, fired: false, firedAt: null }, visit: { dueAt: visit + DAY } } },
      });
      // Clearing it cancels them.
      engine.instances.update(alice, 'Ticket', 't1', { visitAt: null });
      assert.deepEqual(deadlines('t1'), {});

      sweep(DAY);
      assert.deepEqual([status('t2'), deadlines('t2')?.visit.outcome], ['waiting', 'moved']);
      assert.deepEqual(events('t1').filter((event) => event.cause?.schedule === 'fire').length, 1);
    });

    test('each entry starts an enters deadline again, a move away and back within one call included, and until cancels it while it is pending', () => {
      const { clock, create, invoke, move, deadlines, events, sweep } = opened(
        {
          deadlines: {
            respond: { start: { enters: 'open' }, afterMs: 4 * HOUR, then: { transition: 'escalated' }, until: { leaves: 'open' } },
            idle: { start: { enters: 'open' }, afterMs: 2 * HOUR },
          },
        },
        [{ name: 'test.Bounce' }]
      );
      create('t1');
      clock.now = T0 + HOUR;
      move('t1', 'waiting');
      // respond is cancelled on leaving open; idle, with no until, waits.
      assert.deepEqual(deadlines('t1'), { idle: { dueAt: T0 + 2 * HOUR, fired: false } });
      clock.now = T0 + 2 * HOUR;
      move('t1', 'open');
      assert.deepEqual(deadlines('t1'), { respond: { dueAt: T0 + 6 * HOUR, fired: false }, idle: { dueAt: T0 + 4 * HOUR, fired: false } });

      // Away and back in one operation: one event, a new enteredAt, and
      // both start again.
      clock.now = T0 + 3 * HOUR;
      const seen = events('t1').length;
      assert.equal(invoke('t1', 'bounce', { via: 'waiting', back: 'open' }), T0 + 3 * HOUR);
      assert.equal(events('t1').length, seen + 1);
      assert.deepEqual(deadlines('t1'), { respond: { dueAt: T0 + 7 * HOUR, fired: false }, idle: { dueAt: T0 + 5 * HOUR, fired: false } });

      sweep(2 * HOUR);
      assert.deepEqual(deadlines('t1')?.idle, { dueAt: T0 + 5 * HOUR, fired: true, firedAt: T0 + 5 * HOUR });
      // A fired deadline is not cancelled; an entry starts it again.
      clock.now = T0 + 6 * HOUR;
      move('t1', 'waiting');
      assert.deepEqual(deadlines('t1'), { idle: { dueAt: T0 + 5 * HOUR, fired: true, firedAt: T0 + 5 * HOUR } });
      clock.now += MINUTE;
      move('t1', 'open');
      assert.deepEqual(deadlines('t1'), {
        respond: { dueAt: T0 + 10 * HOUR + MINUTE, fired: false },
        idle: { dueAt: T0 + 8 * HOUR + MINUTE, fired: false },
      });
    });

    test('until enters cancels a deadline that starts at the create, and a return to an earlier state does not start it again', () => {
      const { create, move, deadlines, events, sweep } = opened({
        deadlines: { answer: { start: { create: true }, afterMs: HOUR, until: { enters: ['resolved', 'closed'] } } },
      });
      create('t1');
      move('t1', 'resolved');
      assert.deepEqual(deadlines('t1'), {});
      move('t1', 'open');
      assert.deepEqual(deadlines('t1'), {});
      sweep(2 * HOUR);
      assert.equal(events('t1').length, 3);
    });

    test('a deadline whose until holds when it comes due, after a version added the until, is cancelled rather than fired', () => {
      const { engine, create, move, deadlines, events, sweep } = opened({ deadlines: { answer: { start: { create: true }, afterMs: HOUR } } });
      create('t1');
      move('t1', 'resolved');
      publish(engine, ticketSchema({ deadlines: { answer: { start: { create: true }, afterMs: HOUR, until: { enters: ['resolved'] } } } }));
      assert.deepEqual(deadlines('t1'), { answer: { dueAt: T0 + HOUR, fired: false } });
      sweep(2 * HOUR);
      assert.deepEqual(deadlines('t1'), {});
      const change = events('t1').at(-1)?.change as OperationChange;
      assert.deepEqual([change.operation, change.patch], ['fireTimer', { behaviors: { Timers: { deadlines: { answer: null } } } }]);
    });

    test('a create arms its deadlines whatever the order the type lists its behaviors in', () => {
      const clock = testClock(T0);
      const engine = openTestEngine({ driver, clock });
      const document = ticketSchema({ deadlines: { respond: { start: { enters: 'open' }, afterMs: HOUR }, ping: { start: { create: true } } } }) as {
        types: { Ticket: { behaviors: Behavior[] } };
      };
      document.types.Ticket.behaviors.reverse();
      publish(engine, document as unknown as Record<string, unknown>);
      const created = engine.instances.create(alice, 'Ticket', { title: 'First' }, { id: 't1' });
      assert.deepEqual(created.behaviors.Timers, { deadlines: { ping: { dueAt: T0, fired: false }, respond: { dueAt: T0 + HOUR, fired: false } } });
    });

    test('a snooze outlives a later write until what the deadline starts from moves, and resetTimer starts it again from that; both need the permission', () => {
      const { engine, clock, create, invoke, move, deadlines, status, seq, sweep } = opened({
        deadlines: {
          respond: { start: { enters: 'open' }, afterMs: 4 * HOUR, then: { transition: 'escalated' } },
          chase: { start: { enters: 'waiting' }, afterMs: DAY },
        },
        permission: 'tickets.timers',
      });
      create('t1');
      const forbidden = thrown(() => invoke('t1', 'snoozeTimer', { deadline: 'respond', dueAt: T0 + 8 * HOUR }), EngineError);
      assert.deepEqual([forbidden.code, forbidden.message], ['forbidden', 'alice may not snooze deadline respond of Ticket t1: it needs permission tickets.timers']);
      assert.equal(thrown(() => invoke('t1', 'resetTimer', { deadline: 'respond' }), EngineError).code, 'forbidden');

      assert.deepEqual(invoke('t1', 'snoozeTimer', { deadline: 'respond', dueAt: T0 + 8 * HOUR }, lead), { dueAt: T0 + 8 * HOUR });
      clock.now = T0 + HOUR;
      // A write that leaves the status where it was keeps the snooze.
      engine.instances.update(alice, 'Ticket', 't1', { body: 'Still broken.' });
      assert.deepEqual(deadlines('t1'), { respond: { dueAt: T0 + 8 * HOUR, fired: false } });
      sweep(4 * HOUR);
      assert.equal(status('t1'), 'open');
      // The same dueAt again changes nothing.
      const before = seq('t1');
      invoke('t1', 'snoozeTimer', { deadline: 'respond', dueAt: T0 + 8 * HOUR }, lead);
      assert.equal(seq('t1'), before);

      // A reset starts it from the entry into open again: due already.
      assert.deepEqual(invoke('t1', 'resetTimer', { deadline: 'respond' }, lead), { dueAt: T0 + 4 * HOUR });
      sweep(MINUTE);
      assert.deepEqual([status('t1'), deadlines('t1')?.respond.outcome], ['escalated', 'moved']);
      // A fired one snoozed fires again then; out of open, its move is skipped.
      invoke('t1', 'snoozeTimer', { deadline: 'respond', dueAt: clock.now + HOUR }, lead);
      assert.deepEqual(deadlines('t1')?.respond, { dueAt: clock.now + HOUR, fired: false });
      sweep(HOUR);
      assert.deepEqual([status('t1'), deadlines('t1')?.respond.outcome], ['escalated', 'skipped']);

      // Out of open, respond has nothing to start from now; chase has never started.
      const reset = thrown(() => invoke('t1', 'resetTimer', { deadline: 'respond' }, lead), BehaviorVetoError);
      assert.deepEqual(
        [reset.vetoCode, reset.reason],
        ['not_armed', 'deadline respond starts when the status enters open, and Ticket t1 is escalated; it starts at the next entry']
      );
      const snooze = thrown(() => invoke('t1', 'snoozeTimer', { deadline: 'chase', dueAt: T0 }, lead), BehaviorVetoError);
      assert.deepEqual(
        [snooze.vetoCode, snooze.reason],
        ['not_armed', 'deadline chase of Ticket t1 has not started or was cancelled, so it has no due time to move']
      );
      assert.equal(thrown(() => invoke('t1', 'snoozeTimer', { deadline: 'answer', dueAt: T0 }, lead), BehaviorVetoError).vetoCode, 'no_deadline');
      // Back in open, the entry starts respond again and the snooze is gone.
      move('t1', 'open');
      assert.deepEqual(deadlines('t1')?.respond, { dueAt: clock.now + 4 * HOUR, fired: false });

      // With no permission in the config, nobody resets or snoozes.
      const closed = opened({ deadlines: { respond: { start: { enters: 'open' }, afterMs: HOUR } } });
      closed.create('t1');
      const refused = thrown(() => closed.invoke('t1', 'snoozeTimer', { deadline: 'respond', dueAt: T0 }, lead), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', "lee may not snooze deadline respond of Ticket t1: Ticket's Timers config names no permission, so nobody may"]);
    });

    test('a fire whose move a guard vetoes, or whose transition needs a permission the runner lacks, is fired all the same and records which; no sweep tries it again', () => {
      const respond = { deadlines: { respond: { start: { enters: 'open' }, afterMs: HOUR, then: { transition: 'escalated' } } } };
      // StateRules keeps a ticket out of escalated until it has a body.
      const vetoing = opened(respond, [{ name: 'StateRules', config: { states: { escalated: { required: ['body'] } } } }]);
      vetoing.create('t1');
      vetoing.sweep(HOUR);
      assert.deepEqual([vetoing.status('t1'), vetoing.deadlines('t1')?.respond.outcome], ['open', 'vetoed']);
      vetoing.sweep(HOUR);
      assert.equal(vetoing.events('t1').length, 2);

      const guarded = {
        ...flow,
        transitions: flow.transitions.map((move) => (move.from === 'open' && move.to === 'escalated' ? { ...move, permission: 'tickets.escalate' } : move)),
      };
      const forbidding = opened(respond, [], { workflow: guarded });
      forbidding.create('t1');
      forbidding.create('t2');
      forbidding.clock.now += HOUR;
      // A caller with the transition's permission fires t2 itself.
      assert.deepEqual(forbidding.invoke('t2', 'fireTimer', { deadline: 'respond' }, lead), { fired: true, outcome: 'moved' });
      forbidding.sweep(MINUTE);
      assert.deepEqual([forbidding.status('t1'), forbidding.deadlines('t1')?.respond.outcome], ['open', 'forbidden']);
      assert.deepEqual([forbidding.status('t2'), forbidding.events('t2').length], ['escalated', 2]);
    });

    test("a runner the policy refuses write on the schema fails the sweep, which its status shows, and fires nothing", () => {
      // The runner may read the schema, which the sweep reads, and not write it.
      const policy: AccessPolicy = ({ principal, action }) => !(principal.subject === runner.subject && action === 'write');
      const { engine, create, deadlines, sweep } = opened(
        { deadlines: { respond: { start: { enters: 'open' }, afterMs: HOUR, then: { transition: 'escalated' } } } },
        [],
        { policy }
      );
      create('t1');
      const pass = sweep(HOUR);
      assert.equal(pass.failed, 1);
      const status = engine.runner.status().schedules.find((schedule) => schedule.behavior === 'Timers');
      assert.equal(status?.state, 'retrying');
      assert.match(String(status?.error), /forbidden|may not/);
      assert.equal(deadlines('t1')?.respond.fired, false);
    });

    test('the sweep pages by due time past the deadlines whose fire is refused, so they hold none behind them back', () => {
      const { clock, create, deadlines, events, sweep, engine } = opened({ deadlines: { ping: { start: { create: true }, afterMs: HOUR } } }, [{ name: 'test.Hold' }]);
      // A full page of held deadlines due first, then three that fire.
      for (let index = 0; index < 100; index += 1) {
        engine.instances.create(alice, 'Ticket', { title: 'stuck' }, { id: `s${String(index).padStart(3, '0')}` });
      }
      clock.now += MINUTE;
      for (const id of ['f1', 'f2', 'f3']) {
        create(id);
      }
      for (let run = 0; run < 2; run += 1) {
        const pass = sweep(2 * HOUR);
        assert.deepEqual([pass.scheduled, pass.failed], [1, 0]);
        for (const id of ['f1', 'f2', 'f3']) {
          assert.equal(deadlines(id)?.ping.fired, true);
          assert.equal(events(id).length, 2);
        }
        assert.deepEqual([deadlines('s000')?.ping.fired, deadlines('s099')?.ping.fired, events('s050').length], [false, false, 1]);
      }
      assert.equal(engine.runner.status().schedules.find((schedule) => schedule.behavior === 'Timers')?.state, 'active');
    });

    test('the sweep runs per schema and per namespace, each firing its own deadlines', () => {
      const clock = testClock(T0);
      const engine = openTestEngine({ driver, clock, runner: { principal: runner }, namespaces: { names: ['east', 'west'] } });
      const timers = { deadlines: { ping: { start: { create: true }, afterMs: HOUR } } };
      for (const namespace of ['east', 'west']) {
        publish(engine, ticketSchema(timers), namespace);
        publish(engine, ticketSchema(timers, [], flow, 'Task'), namespace);
      }
      engine.runner.runDue();
      const create = (namespace: string, schema: string) => engine.instances.create(alice, schema, { title: 'One' }, { id: 'x1', namespace });
      const fired = (namespace: string, schema: string) =>
        (engine.instances.get(alice, schema, 'x1', { namespace })?.behaviors.Timers?.deadlines as Record<string, Deadline>).ping.fired;
      create('east', 'Ticket');
      create('east', 'Task');
      clock.now = T0 + 2 * HOUR;
      create('west', 'Ticket');
      create('west', 'Task');
      assert.equal(engine.runner.runDue().scheduled, 4);
      assert.deepEqual(
        [fired('east', 'Ticket'), fired('east', 'Task'), fired('west', 'Ticket'), fired('west', 'Task')],
        [true, true, false, false]
      );
      clock.now = T0 + 4 * HOUR;
      engine.runner.runDue();
      assert.deepEqual([fired('west', 'Ticket'), fired('west', 'Task')], [true, true]);
      assert.deepEqual(
        engine.runner
          .status()
          .schedules.filter((schedule) => schedule.behavior === 'Timers')
          .map(({ namespace, schema }) => `${namespace} ${schema}`)
          .sort(),
        ['east Task', 'east Ticket', 'west Task', 'west Ticket']
      );
      const west = engine.events.read(alice, { schema: 'Ticket', instanceId: 'x1', namespace: 'west' }).events;
      assert.deepEqual(west.map((event) => (event.kind === 'operation' ? (event.change as OperationChange).operation : event.kind)), ['create', 'fireTimer']);
    });

    test('a Reactions rule hears a deadline fire through its changes trigger', () => {
      const { clock, create, deadlines, status, events, sweep, engine } = opened({ deadlines: { nudge: { start: { enters: 'open' }, afterMs: HOUR } } }, [
        { name: 'Reactions', config: { rules: [{ when: { changes: { field: 'Timers.deadlines.nudge.fired', to: true } }, then: { transition: 'escalated' } }] } },
      ]);
      create('t1');
      clock.now += 30 * MINUTE;
      // t2's nudge is armed by its create, which moves fired from absent to false: no rule fires.
      create('t2');
      sweep(30 * MINUTE);
      engine.runner.runDue();
      assert.deepEqual([status('t1'), deadlines('t1')?.nudge.fired], ['escalated', true]);
      const moved = events('t1').at(-1) as EngineEvent;
      assert.deepEqual([moved.cause?.behavior, (moved.change as OperationChange).params], ['Reactions', { to: 'escalated' }]);
      assert.deepEqual([status('t2'), deadlines('t2')?.nudge.fired, events('t2').length], ['open', false, 1]);
    });

    test('a new version: a new afterMs moves the pending deadlines from their start, a snoozed or a fired one kept; a removed deadline goes, and a changed start, which arms again at the next write; a new field deadline arms every instance at its publish; Timers joins and leaves a schema with instances', () => {
      const clock = testClock(T0);
      const engine = openTestEngine({ driver, clock, runner: { principal: runner } });
      const { create, invoke, deadlines } = calls(engine, clock);
      const version = (timers: unknown) => publish(engine, ticketSchema(timers));
      version(null);
      create('t1');
      create('t2', { visitAt: at(T0 + DAY) });

      // Joining: the field deadline arms at the publish; the enters one at
      // each instance's next write.
      clock.now = T0 + HOUR;
      version({
        deadlines: {
          respond: { start: { enters: 'open' }, afterMs: 4 * HOUR },
          remind: { start: { field: 'visitAt' }, afterMs: -HOUR },
        },
        permission: 'tickets.timers',
      });
      assert.deepEqual([deadlines('t1'), deadlines('t2')], [{}, { remind: { dueAt: T0 + 23 * HOUR, fired: false } }]);
      assert.equal(engine.events.read(alice, { schema: 'Ticket', instanceId: 't2' }).events.length, 1);
      engine.instances.update(alice, 'Ticket', 't1', { body: 'First write.' });
      assert.deepEqual(deadlines('t1'), { respond: { dueAt: T0 + 4 * HOUR, fired: false } });
      create('t3');
      invoke('t3', 'snoozeTimer', { deadline: 'respond', dueAt: T0 + 9 * HOUR }, lead);

      // A new afterMs: t1's pending respond moves; t3's snooze stays.
      version({
        deadlines: {
          respond: { start: { enters: 'open' }, afterMs: 6 * HOUR },
          remind: { start: { field: 'visitAt' }, afterMs: -HOUR },
        },
        permission: 'tickets.timers',
      });
      assert.deepEqual([deadlines('t1')?.respond.dueAt, deadlines('t3')?.respond.dueAt], [T0 + 6 * HOUR, T0 + 9 * HOUR]);

      // A changed start drops its rows, which the next write arms again;
      // a removed deadline goes.
      version({ deadlines: { respond: { start: { create: true }, afterMs: 2 * HOUR } }, permission: 'tickets.timers' });
      assert.deepEqual([deadlines('t1'), deadlines('t2'), deadlines('t3')], [{}, {}, {}]);
      engine.instances.update(alice, 'Ticket', 't3', { body: 'Next write.' });
      assert.deepEqual(deadlines('t3'), { respond: { dueAt: T0 + HOUR + 2 * HOUR, fired: false } });

      // Leaving drops every deadline; joining again arms the field ones.
      version(null);
      assert.equal(engine.instances.get(alice, 'Ticket', 't3')?.behaviors.Timers, undefined);
      version({ deadlines: { remind: { start: { field: 'visitAt' } } } });
      assert.deepEqual([deadlines('t2'), deadlines('t3')], [{ remind: { dueAt: T0 + DAY, fired: false } }, {}]);
    });

    test('deleting the instance deletes its deadlines', () => {
      const { create, deadlines, sweep, engine } = opened({ deadlines: { ping: { start: { create: true }, afterMs: HOUR } } });
      create('t1');
      engine.instances.delete(alice, 'Ticket', 't1');
      sweep(2 * HOUR);
      create('t1');
      assert.deepEqual(deadlines('t1'), { ping: { dueAt: T0 + 2 * HOUR + HOUR, fired: false } });
    });

    test('its config: each start, then and until is held to the type, Workflow is required for a state, and the shape to its configSchema', () => {
      const engine = openTestEngine({ driver });
      const refusal = (timers: unknown, workflow: unknown = flow) => thrown(() => engine.schemas.define(alice, ticketSchema(timers, [], workflow)), SchemaDocumentError).message;
      const one = (deadline: unknown) => ({ deadlines: { sla: deadline } });
      const cases: Array<[unknown, RegExp, unknown?]> = [
        [one({ start: { enters: 'open' } }), /deadlines\.sla\.start\.enters reads the status of Workflow, which the type does not list/, null],
        [one({ start: { create: true }, then: { transition: 'closed' } }), /deadlines\.sla\.then reads the status of Workflow, which the type does not list/, null],
        [one({ start: { enters: 'gone' } }), /deadlines\.sla\.start\.enters "gone" is not a state of the type's Workflow \(open, waiting, escalated, resolved, closed\)/],
        [one({ start: { field: 'when' } }), /deadlines\.sla\.start\.field "when" is not a field of Ticket \(its fields: title, body, visitAt, visitDay\)/],
        [one({ start: { field: 'visitDay' } }), /deadlines\.sla\.start\.field "visitDay" is not a date-time field of Ticket/],
        [one({ start: { enters: 'open' }, afterMs: -1 }), /deadlines\.sla\.afterMs is negative, which only a field start takes/],
        [one({ start: { enters: 'open' }, then: { transition: 'gone' } }), /deadlines\.sla\.then\.transition "gone" is not a state/],
        [one({ start: { enters: 'waiting' }, then: { transition: 'closed' } }), /deadlines\.sla\.then: no transition of the type's Workflow leads from "waiting" to "closed"/],
        [one({ start: { enters: 'open' }, then: { transition: 'open' } }), /deadlines\.sla\.then moves the status from "open" to itself/],
        [one({ start: { field: 'visitAt' }, then: { transition: 'closed' } }), /deadlines\.sla\.then names no from: a deadline that starts at a field is in no state of its own/],
        [one({ start: { field: 'visitAt' }, until: { leaves: 'open' } }), /deadlines\.sla\.until\.leaves: a deadline that starts at a field is in no state of its own to leave/],
        [one({ start: { enters: 'waiting' }, until: { leaves: 'open' } }), /deadlines\.sla\.until\.leaves "open" is not the state the deadline starts in, "waiting"/],
        [one({ start: { create: true }, until: { leaves: 'waiting' } }), /deadlines\.sla\.until\.leaves "waiting" is not the state the deadline starts in, "open"/],
        [one({ start: { create: true }, until: { enters: ['closed', 'open'] } }), /deadlines\.sla\.until\.enters names "open", the state the deadline starts in/],
        [one({ start: { field: 'visitAt' }, until: { enters: ['gone'] } }), /deadlines\.sla\.until\.enters "gone" is not a state/],
      ];
      for (const [timers, message, workflow] of cases) {
        assert.match(refusal(timers, workflow), message);
      }
      // A field deadline needs no Workflow, and its then names from.
      engine.schemas.define(alice, ticketSchema({ deadlines: { sla: { start: { field: 'visitAt' }, afterMs: -HOUR } } }, [], null));
      engine.schemas.define(alice, ticketSchema(one({ start: { field: 'visitAt' }, then: { transition: 'closed', from: ['open', 'resolved'] }, until: { enters: ['closed'] } })));
      // The core meta-schema holds the config to the declaration's configSchema first.
      const paths = (timers: unknown) =>
        thrown(() => engine.schemas.define(alice, ticketSchema(timers)), SchemaDocumentError).issues.map((issue) => issue.path);
      assert.ok(paths({ deadlines: {} }).includes('/types/Ticket/behaviors/1/config/deadlines'));
      assert.ok(paths(one({ start: { create: true, enters: 'open' } })).some((path) => path.startsWith('/types/Ticket/behaviors/1/config/deadlines/sla/start')));
      assert.ok(paths({ ...one({ start: { create: true } }), sweepMs: 999 }).includes('/types/Ticket/behaviors/1/config/sweepMs'));
      assert.ok(paths(one({ start: { create: true }, afterMs: 1.5 })).includes('/types/Ticket/behaviors/1/config/deadlines/sla/afterMs'));
    });

    test('its guidance says when each deadline is due, what moves and cancels it, who resets and snoozes one; create, transition and update hear it', () => {
      const { engine } = opened({
        deadlines: {
          respond: { start: { enters: 'open' }, afterMs: 4 * HOUR, then: { transition: 'escalated' }, until: { leaves: 'open' } },
          close: { start: { create: true }, afterMs: 30 * DAY, then: { transition: 'closed' }, until: { enters: ['resolved'] } },
          remind: { start: { field: 'visitAt' }, afterMs: -DAY },
        },
        sweepMs: 60_000,
        permission: 'tickets.timers',
      });
      const described = engine.tools.describe(alice, 'Ticket');
      const guidance = (operation: string): ToolGuidance => described.operations.find((candidate) => candidate.name === operation)?.guidance as ToolGuidance;
      const codes = (operation: string) => guidance(operation).errors.map((error) => error.code);
      const summary = described.behaviors.find((behavior) => behavior.name === 'Timers')?.summary as string;
      // The deadlines in the order the stored config holds them, by name.
      assert.match(
        summary,
        /^Deadline close is due 30 days after the create; when it fires, it moves the status to closed from open; it is cancelled when the status enters resolved\. /
      );
      assert.match(summary, / Deadline remind is due 1 day before the time visitAt holds\. /);
      assert.match(summary, / Deadline respond is due 4 hours after the status enters open; when it fires, it moves the status to escalated from open; it is cancelled when the status leaves open\. /);
      assert.match(summary, /one not fired is overdue once its dueAt has passed\. The runner fires each due deadline within 1 minute/);
      assert.match(summary, /A caller with permission tickets\.timers may reset or snooze a deadline\.$/);
      assert.deepEqual([codes('fireTimer'), codes('resetTimer'), codes('snoozeTimer')], [['no_deadline'], ['no_deadline', 'not_armed'], ['no_deadline', 'not_armed']]);
      assert.match(guidance('snoozeTimer').useWhen, /It needs permission tickets\.timers, else the call is forbidden\./);
      assert.match(guidance('transition').success, /Entering resolved cancels deadline close\. Entering open starts deadline respond, due 4 hours later\. Leaving open cancels deadline respond\./);
      assert.match(guidance('update').success, /Changing visitAt moves deadline remind, which fires again when it comes due; clearing visitAt cancels it\./);
      assert.match(guidance('create').success, /A new Ticket starts deadline close, due 30 days later\. A visitAt the create gives starts deadline remind\./);

      const closed = opened({ deadlines: { ping: { start: { field: 'visitAt' } } } }).engine.tools.describe(alice, 'Ticket');
      assert.match(closed.behaviors.find((behavior) => behavior.name === 'Timers')?.summary as string, /Nobody resets or snoozes a deadline: the config names no permission\.$/);
      assert.match(closed.operations.find((operation) => operation.name === 'resetTimer')?.guidance.doNotUseWhen as string, /the config names no permission/);
      assert.equal(closed.operations.find((operation) => operation.name === 'transition')?.guidance.success.includes('deadline'), false);
    });
  });
}
