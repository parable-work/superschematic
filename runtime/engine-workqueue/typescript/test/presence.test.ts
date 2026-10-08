// Presence: the first deadline at create, beat by the principal the
// instance stands for and no other, miss applied once and only past the
// deadline, onMissed only from its from states and onBeat back, the
// runner's sweep on the engine clock, the guard on principalField, the
// leases a miss expires on the releaseLeases schemas through Lease's
// expireHolder, sparing the ones renewed since the last beat and recording
// the rest, the codes its refusals carry, and the config rules. Real
// SQLite, a real engine, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type Engine,
  type EngineOptions,
  type Principal,
} from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const wren: Principal = { subject: 'wren', permissions: [] };
const otto: Principal = { subject: 'otto', permissions: [] };

const T0 = 1_000_000;
const TTL = 30000;

/** A worker is idle or busy while it beats, missing once it stops, and stopped for good. */
const workerFlow = {
  states: ['idle', 'busy', 'missing', 'stopped'],
  transitions: [
    { from: 'idle', to: 'busy' },
    { from: 'busy', to: 'idle' },
    { from: 'idle', to: 'missing' },
    { from: 'busy', to: 'missing' },
    { from: 'missing', to: 'idle' },
    { from: 'idle', to: 'stopped' },
    { from: 'busy', to: 'stopped' },
    { from: 'missing', to: 'stopped' },
  ],
};

const presenceConfig = {
  ttlMs: TTL,
  principalField: 'subject',
  onMissed: { transition: 'missing', from: ['idle', 'busy'] },
  onBeat: { transition: 'idle', from: ['missing'] },
};

/** workersDocument is a General schema named Worker: the subject of the principal it stands for, and a name. */
function workersDocument(behaviors: readonly BehaviorRef[]): Record<string, unknown> {
  return {
    kind: 'General',
    name: 'Worker',
    types: {
      Worker: {
        name: 'Worker',
        role: 'EmbeddedStruct',
        behaviors,
        fields: [
          { name: 'subject', typeRef: { name: 'string' }, required: true },
          { name: 'name', typeRef: { name: 'string' } },
          { name: 'slots', typeRef: { name: 'Generic.Int64' } },
        ],
      },
    },
  };
}

for (const driver of drivers) {
  // world opens an engine whose Worker composes Workflow and Presence
  // with the config given, on a clock at T0, with worker w1 for wren.
  function world(config: Record<string, unknown> = presenceConfig, options: Partial<EngineOptions> = {}) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: { subject: 'runner', permissions: [] } }, ...options });
    publish(engine, workersDocument([{ name: 'Workflow', config: workerFlow }, { name: 'Presence', config }]));
    engine.instances.create(alice, 'Worker', { subject: 'wren', name: 'Wren' }, { id: 'w1' });
    return { engine, clock };
  }

  const invoke = (engine: Engine, who: Principal, operation: string, id = 'w1') => engine.instances.invoke(who, 'Worker', id, operation, {});
  const presenceOf = (engine: Engine, id = 'w1') => engine.instances.get(alice, 'Worker', id)?.behaviors.Presence as Record<string, unknown>;
  const statusOf = (engine: Engine, id = 'w1') => engine.instances.get(alice, 'Worker', id)?.behaviors.Workflow?.status;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Presence: beat and miss (${driver})`, () => {
    test("a create sets the first deadline one ttl on, so a worker that never beats is missed; Presence's fields show it", () => {
      const { engine } = world();
      assert.deepEqual(presenceOf(engine), { deadline: T0 + TTL, missed: false });
    });

    test('only the principal the instance stands for can beat it', () => {
      const { engine, clock } = world();
      clock.advance(10000);
      assert.deepEqual(invoke(engine, wren, 'beat'), { deadline: T0 + 10000 + TTL });
      assert.deepEqual(presenceOf(engine), { deadline: T0 + 10000 + TTL, lastBeatAt: T0 + 10000, missed: false });
      // Knowing the subject the instance holds does not make another principal it.
      const refused = veto(() => invoke(engine, otto, 'beat'));
      assert.deepEqual(
        [refused.behavior, refused.action, refused.reason, refused.vetoCode],
        ['Presence', 'beat', 'it stands for another principal, who alone may beat it', 'not_principal']
      );
      assert.equal(presenceOf(engine).lastBeatAt, T0 + 10000);
    });

    test('miss acts once, and only past the deadline: it sets missed and onMissed moves the status', () => {
      const { engine, clock } = world();
      clock.advance(TTL - 1);
      assert.deepEqual(invoke(engine, otto, 'miss'), { missed: false });
      assert.equal(statusOf(engine), 'idle');
      // A miss that finds the deadline ahead changes nothing, and appends no event.
      assert.equal(engine.instances.get(alice, 'Worker', 'w1')?.seq, 1);
      clock.advance(1);
      assert.deepEqual(invoke(engine, otto, 'miss'), { missed: true, released: {} });
      assert.deepEqual(presenceOf(engine), { deadline: T0 + TTL, missed: true, released: {} });
      assert.equal(statusOf(engine), 'missing');
      const seq = engine.instances.get(alice, 'Worker', 'w1')?.seq;
      assert.deepEqual(invoke(engine, otto, 'miss'), { missed: false });
      assert.equal(engine.instances.get(alice, 'Worker', 'w1')?.seq, seq);
    });

    test('onMissed moves the status only from its from states: a stopped worker stays stopped', () => {
      const { engine, clock } = world();
      engine.instances.invoke(wren, 'Worker', 'w1', 'transition', { to: 'stopped' });
      clock.advance(TTL);
      assert.deepEqual(invoke(engine, otto, 'miss'), { missed: true, released: {} });
      assert.equal(statusOf(engine), 'stopped');
      assert.equal(presenceOf(engine).missed, true);
    });

    test('a beat recovers a missed worker: missed clears, the deadline moves, and onBeat moves the status back only from its from states', () => {
      const { engine, clock } = world();
      clock.advance(TTL);
      invoke(engine, otto, 'miss');
      clock.advance(5000);
      assert.deepEqual(invoke(engine, wren, 'beat'), { deadline: T0 + TTL + 5000 + TTL });
      assert.deepEqual(presenceOf(engine), { deadline: T0 + TTL + 5000 + TTL, lastBeatAt: T0 + TTL + 5000, missed: false, released: {} });
      assert.equal(statusOf(engine), 'idle');
      // From busy, which onBeat does not list, a beat leaves the status.
      engine.instances.invoke(wren, 'Worker', 'w1', 'transition', { to: 'busy' });
      invoke(engine, wren, 'beat');
      assert.equal(statusOf(engine), 'busy');
    });

    test('without onMissed and onBeat, a miss and a beat leave the status, and Workflow is not needed', () => {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, workersDocument([{ name: 'Presence', config: { ttlMs: 1000, principalField: 'subject' } }]));
      engine.instances.create(alice, 'Worker', { subject: 'wren' }, { id: 'w1' });
      clock.advance(1000);
      assert.deepEqual(invoke(engine, otto, 'miss'), { missed: true, released: {} });
      assert.equal(engine.instances.get(alice, 'Worker', 'w1')?.behaviors.Workflow, undefined);
      assert.deepEqual(invoke(engine, wren, 'beat'), { deadline: T0 + 2000 });
    });
  });

  describe(`Presence: the sweep (${driver})`, () => {
    test("the runner's schedule misses each worker past its deadline once, on the engine clock, every sweepMs", () => {
      const { engine, clock } = world({ ...presenceConfig, sweepMs: 10000 });
      engine.instances.create(alice, 'Worker', { subject: 'otto' }, { id: 'w2' });
      // The runner finds the schedule and runs it an interval later.
      assert.equal(engine.runner.runDue().scheduled, 0);
      clock.advance(10000);
      engine.instances.invoke(otto, 'Worker', 'w2', 'beat', {});
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.equal(presenceOf(engine).missed, false);

      clock.advance(20000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.equal(statusOf(engine), 'missing');
      assert.equal(presenceOf(engine).missed, true);
      assert.equal(statusOf(engine, 'w2'), 'idle');
      const seq = engine.instances.get(alice, 'Worker', 'w1')?.seq;
      const [missEvent] = engine.events.read(alice, { schema: 'Worker', limit: 500 }).events.filter((event) => event.kind === 'operation' && event.instanceId === 'w1');
      assert.equal(missEvent.actor, 'runner');
      assert.deepEqual(missEvent.cause, { behavior: 'Presence', schedule: 'miss', depth: 1 });

      // A missed worker is not missed again; the other one is, at its own deadline.
      clock.advance(10000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.equal(engine.instances.get(alice, 'Worker', 'w1')?.seq, seq);
      assert.equal(statusOf(engine, 'w2'), 'missing');
    });

    test('a miss that fails fails the run, which the runner retries and shows, and leaves the worker as it was', () => {
      const flow = {
        ...workerFlow,
        transitions: workerFlow.transitions.map((move) => (move.to === 'missing' ? { ...move, permission: 'workers.mark' } : move)),
      };
      const clock = new Clock(T0);
      const options = { driver, clock: clock.now };
      const engine = openTestEngine({ ...options, runner: { principal: { subject: 'runner', permissions: [] } } });
      publish(engine, workersDocument([{ name: 'Workflow', config: flow }, { name: 'Presence', config: presenceConfig }]));
      engine.instances.create(alice, 'Worker', { subject: 'wren' }, { id: 'w1' });
      engine.runner.runDue();
      clock.advance(TTL);
      assert.equal(engine.runner.runDue().failed, 1);
      const [status] = engine.runner.status().schedules;
      assert.equal(status.schedule, 'miss');
      assert.match(String(status.error), /runner may not move Worker w1 from idle to missing: the transition needs permission workers.mark/);
      assert.deepEqual(presenceOf(engine), { deadline: T0 + TTL, missed: false });
      assert.equal(statusOf(engine), 'idle');
    });
  });

  describe(`Presence: the guard (${driver})`, () => {
    test('an update that changes principalField is refused, whoever asks; the rest of an update goes through', () => {
      const { engine } = world();
      const refused = veto(() => engine.instances.update(alice, 'Worker', 'w1', { subject: 'otto' }));
      assert.deepEqual([refused.behavior, refused.action, refused.vetoCode], ['Presence', 'update', 'principal_fixed']);
      assert.equal(refused.reason, 'subject holds the principal the instance stands for, so it cannot change once set');
      engine.instances.update(alice, 'Worker', 'w1', { name: 'Wren the second', subject: 'wren' });
      assert.equal(engine.instances.get(alice, 'Worker', 'w1')?.data.name, 'Wren the second');
      assert.equal(veto(() => invoke(engine, otto, 'beat')).reason, 'it stands for another principal, who alone may beat it');
    });

    test('an instance whose principalField holds no principal cannot be beaten (no_principal)', () => {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, workersDocument([{ name: 'Presence', config: { ttlMs: TTL, principalField: 'subject' } }]));
      engine.instances.create(alice, 'Worker', { subject: '' }, { id: 'w0' });
      const refused = veto(() => engine.instances.invoke(wren, 'Worker', 'w0', 'beat', {}));
      assert.deepEqual([refused.reason, refused.vetoCode], ['its subject holds no principal, so no one may beat it', 'no_principal']);
    });
  });

  describe(`Presence: releaseLeases (${driver})`, () => {
    // fleet opens an engine with Presence on Worker and Lease on Job, whose
    // expiry puts a running job back in the queue: worker w1 stands for
    // wren, who holds the leases of jobs j1 and j2, and otto holds j3's.
    // The leases last 60 s, so none lapses before the worker's 30 s TTL.
    function fleet(runnerPermissions: readonly string[]) {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: { subject: 'runner', permissions: runnerPermissions } } });
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: { onExpiry: { transition: 'queued', from: ['running'] }, overridePermission: 'jobs.override' } },
        ])
      );
      publish(engine, workersDocument([{ name: 'Workflow', config: workerFlow }, { name: 'Presence', config: { ...presenceConfig, releaseLeases: ['Job'] } }]));
      engine.instances.create(alice, 'Worker', { subject: 'wren' }, { id: 'w1' });
      for (const [id, who] of [['j1', wren], ['j2', wren], ['j3', otto]] as const) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
        engine.instances.invoke(who, 'Job', id, 'acquire', {});
        engine.instances.invoke(who, 'Job', id, 'transition', { to: 'running' });
      }
      engine.runner.runDue();
      return { engine, clock };
    }
    // jobs reads each job's status, holder and count of expiries.
    const jobs = (engine: Engine) =>
      ['j1', 'j2', 'j3'].map((id) => {
        const behaviors = engine.instances.get(alice, 'Job', id)?.behaviors as { Workflow: { status: string }; Lease: { holder?: string; expiries: number } };
        return [behaviors.Workflow.status, behaviors.Lease.holder, behaviors.Lease.expiries];
      });

    test("a miss expires every lease the worker's principal holds on the releaseLeases schemas, through Lease's expireHolder, and no other", () => {
      const { engine, clock } = fleet(['jobs.override']);
      clock.advance(TTL);
      assert.equal(engine.runner.runDue().failed, 0);
      assert.equal(statusOf(engine), 'missing');
      assert.deepEqual(jobs(engine), [
        ['queued', undefined, 1],
        ['queued', undefined, 1],
        ['running', 'otto', 0],
      ]);
      // Each expiry is Lease's expire on the job, with its own event, caused by the sweep.
      const expiries = engine.events
        .read(alice, { schema: 'Job', limit: 500 })
        .events.filter((event) => event.kind === 'operation' && event.actor === 'runner');
      assert.deepEqual(
        expiries.map((event) => [event.instanceId, (event.change as { operation: string; params: unknown }).operation, (event.change as { params: unknown }).params, event.cause]),
        [
          ['j1', 'expire', { holder: 'wren', notRenewedAfter: T0 }, { behavior: 'Presence', schedule: 'miss', depth: 1 }],
          ['j2', 'expire', { holder: 'wren', notRenewedAfter: T0 }, { behavior: 'Presence', schedule: 'miss', depth: 1 }],
        ]
      );
    });

    test("a miss spares a lease its holder renewed after the worker's last beat, and records the ones it expired in its event", () => {
      const { engine, clock } = fleet(['jobs.override']);
      clock.advance(5000);
      engine.instances.invoke(wren, 'Worker', 'w1', 'beat', {});
      // The worker's beats stop at T0 + 5000; j2's heartbeats go on.
      clock.advance(15000);
      engine.instances.invoke(wren, 'Job', 'j2', 'heartbeat', {}, fenced(1));
      clock.advance(15000);
      assert.equal(engine.runner.runDue().failed, 0);
      assert.equal(statusOf(engine), 'missing');
      assert.deepEqual(jobs(engine), [
        ['queued', undefined, 1],
        ['running', 'wren', 0],
        ['running', 'otto', 0],
      ]);
      // The miss's event carries what it released; the expiry's, the time it was given.
      assert.deepEqual(presenceOf(engine).released, { Job: ['j1'] });
      const miss = engine.events
        .read(alice, { schema: 'Worker', instanceId: 'w1', limit: 500 })
        .events.find((event) => (event.change as { operation?: string } | null)?.operation === 'miss');
      assert.deepEqual((miss?.change as { patch: { behaviors: { Presence: unknown } } }).patch.behaviors.Presence, { missed: true, released: { Job: ['j1'] } });
      const expiry = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.deepEqual((expiry?.change as { params: unknown }).params, { holder: 'wren', notRenewedAfter: T0 + 5000 });
    });

    test('a miss leaves the leases while another instance that stands for the same principal is present, and expires them when that one is missed too', () => {
      const { engine, clock } = fleet(['jobs.override']);
      engine.instances.create(alice, 'Worker', { subject: 'wren' }, { id: 'w2' });
      clock.advance(TTL);
      engine.instances.invoke(wren, 'Worker', 'w2', 'beat', {});
      // The holders renew their leases, so none lapses before the second worker is missed.
      for (const [id, who] of [['j1', wren], ['j2', wren], ['j3', otto]] as const) {
        engine.instances.invoke(who, 'Job', id, 'heartbeat', {}, fenced(1));
      }
      engine.runner.runDue();
      assert.deepEqual([presenceOf(engine).missed, presenceOf(engine, 'w2').missed], [true, false]);
      assert.deepEqual(jobs(engine), [
        ['running', 'wren', 0],
        ['running', 'wren', 0],
        ['running', 'otto', 0],
      ]);
      clock.advance(TTL);
      engine.runner.runDue();
      assert.equal(presenceOf(engine, 'w2').missed, true);
      assert.deepEqual(jobs(engine), [
        ['queued', undefined, 1],
        ['queued', undefined, 1],
        ['running', 'otto', 0],
      ]);
    });

    test("without Lease's overridePermission the runner's miss fails: nothing is missed or released, and the status shows why", () => {
      const { engine, clock } = fleet([]);
      clock.advance(TTL);
      assert.equal(engine.runner.runDue().failed, 1);
      const miss = engine.runner.status().schedules.find((schedule) => schedule.behavior === 'Presence');
      assert.match(String(miss?.error), /runner may not expire the leases of a holder on Job: it needs permission jobs.override/);
      assert.equal(presenceOf(engine).missed, false);
      assert.deepEqual(jobs(engine), [
        ['running', 'wren', 0],
        ['running', 'wren', 0],
        ['running', 'otto', 0],
      ]);
    });
  });

  describe(`Presence: the config (${driver})`, () => {
    const define = (engine: Engine, config: Record<string, unknown>, behaviors: BehaviorRef[] = [{ name: 'Workflow', config: workerFlow }]) =>
      thrown(() => engine.schemas.define(alice, workersDocument([...behaviors, { name: 'Presence', config }])), SchemaDocumentError).message;

    test('principalField is a string field of the type; onMissed and onBeat need Workflow and its states and transitions', () => {
      const engine = openTestEngine({ driver });
      assert.match(define(engine, { ttlMs: TTL, principalField: 'who' }), /principalField "who" is not a field of Worker \(its fields: subject, name, slots\)/);
      assert.match(define(engine, { ttlMs: TTL, principalField: 'slots' }), /principalField "slots" is not a string field of Worker/);
      assert.match(define(engine, presenceConfig, []), /onMissed moves the status through Workflow, which the type does not list/);
      assert.match(
        define(engine, { ...presenceConfig, onMissed: { transition: 'gone', from: ['idle'] } }),
        /onMissed.transition "gone" is not a state of the type's Workflow/
      );
      assert.match(
        define(engine, { ...presenceConfig, onBeat: { transition: 'idle', from: ['stopped'] } }),
        /onBeat: no transition of the type's Workflow leads from "stopped" to "idle"/
      );
      assert.match(define(engine, { ...presenceConfig, ttlMs: 999 }), /ttlMs/);
    });

    test('releaseLeases names live schemas that compose Lease', () => {
      const engine = openTestEngine({ driver });
      assert.match(define(engine, { ...presenceConfig, releaseLeases: ['Job'] }), /releaseLeases: schema Job has no live version; publish it first/);
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }]));
      assert.match(define(engine, { ...presenceConfig, releaseLeases: ['Job'] }), /releaseLeases: Job does not compose Lease, so it holds no lease to expire/);
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease' }], 'Task'));
      engine.schemas.define(alice, workersDocument([{ name: 'Workflow', config: workerFlow }, { name: 'Presence', config: { ...presenceConfig, releaseLeases: ['Task'] } }]));
    });

    test('a new version keeps principalField, and Presence is not removed from a schema with instances', () => {
      const { engine } = world();
      const next = (config: Record<string, unknown> | undefined) =>
        workersDocument([{ name: 'Workflow', config: workerFlow }, ...(config === undefined ? [] : [{ name: 'Presence', config }])]);
      assert.match(thrown(() => engine.schemas.define(alice, next({ ...presenceConfig, principalField: 'name' })), IncompatibleChangeError).message, /principalField/);
      assert.match(thrown(() => engine.schemas.define(alice, next(undefined)), IncompatibleChangeError).message, /would stay behind/);
      publish(engine, next({ ...presenceConfig, ttlMs: 60000, sweepMs: 1000 }));
      assert.deepEqual(engine.instances.invoke(wren, 'Worker', 'w1', 'beat', {}), { deadline: T0 + 60000 });
      // With no instance, no principal is held where the old config said: principalField may move.
      engine.instances.delete(alice, 'Worker', 'w1');
      publish(engine, next({ ...presenceConfig, principalField: 'name' }));
    });

    test('beat and miss take no parameters', () => {
      const { engine } = world();
      assert.match(thrown(() => engine.instances.invoke(wren, 'Worker', 'w1', 'beat', { subject: 'wren' }), OperationParamsError).message, /operation beat of behavior Presence/);
    });
  });
}
