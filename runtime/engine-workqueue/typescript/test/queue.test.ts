// Queue: claim takes the lease and moves the status in one transaction,
// after checks it reads from the instance itself; claimNext picks
// candidates from Queue's own copies, in priority order, filtered by the
// caller's assignment and the match values, and skips the ones whose
// claim is refused, vetoed or forbidden; the copies follow the instance,
// its blockers and its enclosing budget scopes, so work over its budget is
// not tried and does not starve the work behind it; countClaimable counts
// the same candidates; and the config rules. Real SQLite, a real engine, a
// clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type AnyBehaviorImplementation,
  type Engine,
  type EngineOptions,
  type Principal,
} from '@superschematic/engine';

import { lease, queue } from '../dist/index.js';
import { budgetStandIn, gate, lock, openMetaSchema, reserved } from './fixtures.ts';
import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const operator: Principal = { subject: 'opal', permissions: ['jobs.override'] };
const lead: Principal = { subject: 'lena', permissions: ['jobs.assign'] };
const runner: Principal = { subject: 'runner', permissions: [] };

const T0 = 1_000_000;
const DAY = 86_400_000;

const requeue = { onExpiry: { transition: 'queued', from: ['running'] } };
const claimable = { claim: { from: ['queued'], to: 'running' }, priorityField: 'priority', match: ['topic', 'urgent'] };

// A step is a piece of work another schema's jobs can wait on.
const stepFlow = { states: ['todo', 'done'], transitions: [{ from: 'todo', to: 'done' }] };

interface World {
  readonly lease?: Record<string, unknown>;
  readonly queue?: Record<string, unknown>;
  readonly extra?: readonly BehaviorRef[];
  readonly behaviors?: readonly AnyBehaviorImplementation[];
  readonly options?: Partial<EngineOptions>;
}

for (const driver of drivers) {
  // world opens an engine whose Job composes Workflow, Lease, Queue and
  // the extra behaviors, with the configs given.
  function world(spec: World = {}) {
    const clock = new Clock(T0);
    const engine = openTestEngine({
      driver,
      clock: clock.now,
      ...(spec.behaviors === undefined ? {} : { behaviors: spec.behaviors, metaSchema: openMetaSchema() }),
      ...spec.options,
    });
    publish(engine, jobs(spec));
    return { engine, clock };
  }

  function jobs(spec: World): Record<string, unknown> {
    return jobsDocument([
      { name: 'Workflow', config: jobFlow },
      { name: 'Lease', config: spec.lease ?? requeue },
      { name: 'Queue', config: spec.queue ?? claimable },
      ...(spec.extra ?? []),
    ]);
  }

  const create = (engine: Engine, id: string, data: Record<string, unknown> = {}) =>
    engine.instances.create(alice, 'Job', { title: id, ...data }, { id });
  const claim = (engine: Engine, who: Principal, id: string, params: Record<string, unknown> = {}) =>
    engine.instances.invoke(who, 'Job', id, 'claim', params) as { id: string; token: number; expiresAt: number; heartbeatMs: number };
  const claimNext = (engine: Engine, who: Principal, params: Record<string, unknown> = {}) =>
    (engine.instances.invokeSchema(who, 'Job', 'claimNext', params) as { claimed: { id: string } | null }).claimed;
  const next = (engine: Engine, who: Principal, params: Record<string, unknown> = {}) => claimNext(engine, who, params)?.id ?? null;
  const statusOf = (engine: Engine, id: string) => engine.instances.get(alice, 'Job', id)?.behaviors.Workflow?.status;
  const leaseOf = (engine: Engine, id: string) =>
    engine.instances.get(alice, 'Job', id)?.behaviors.Lease as { holder?: string; token: number; expiries: number; active: boolean };
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Queue: claim (${driver})`, () => {
    test('claim takes the lease and moves the status in one call, and returns the token and the heartbeat interval', () => {
      const { engine } = world();
      create(engine, 'j1');
      assert.deepEqual(claim(engine, worker, 'j1'), { id: 'j1', token: 1, expiresAt: T0 + 60000, heartbeatMs: 20000 });
      assert.equal(statusOf(engine, 'j1'), 'running');
      assert.equal(leaseOf(engine, 'j1').holder, 'wren');
      // One event records both.
      const last = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.deepEqual([last?.actor, (last?.change as { operation: string }).operation], ['wren', 'claim']);
      assert.equal((last?.change as { patch: { behaviors: { Workflow: { status?: string } } } }).patch.behaviors.Workflow.status, 'running');
      // A claimed instance is another principal's lease.
      assert.equal(veto(() => claim(engine, other, 'j1')).behavior, 'Lease');
      // The lease's length is Lease's acquire's.
      create(engine, 'j2');
      assert.deepEqual(claim(engine, worker, 'j2', { ttlMs: 30000 }), { id: 'j2', token: 1, expiresAt: T0 + 30000, heartbeatMs: 10000 });
    });

    test('claim checks the instance itself: a state it is not claimed from, and a blocker, refuse it and change nothing', () => {
      const { engine } = world({ extra: [{ name: 'Dependencies' }] });
      create(engine, 'j1');
      create(engine, 'j2');
      create(engine, 'j3');
      engine.instances.invoke(alice, 'Job', 'j1', 'transition', { to: 'failed' });
      const failed = veto(() => claim(engine, worker, 'j1'));
      assert.deepEqual([failed.reason, failed.vetoCode, failed.vetoDetails], ['it is failed, and it is claimed from queued', 'not_claimable', { status: 'failed', from: ['queued'] }]);
      engine.instances.invoke(alice, 'Job', 'j2', 'addBlocker', { id: 'j3' });
      const blocked = veto(() => claim(engine, worker, 'j2'));
      assert.deepEqual([blocked.behavior, blocked.reason, blocked.vetoCode], ['Queue', 'a blocker holds it up', 'blocked']);
      assert.deepEqual([leaseOf(engine, 'j2').token, statusOf(engine, 'j2')], [0, 'queued']);
    });

    test('a lease on a type that composes Queue is taken only by claiming it', () => {
      const { engine } = world();
      create(engine, 'j1');
      const refused = veto(() => engine.instances.invoke(worker, 'Job', 'j1', 'acquire'));
      assert.deepEqual(
        [refused.behavior, refused.reason, refused.vetoCode],
        ['Queue', "its lease is taken by claiming it (Queue's claim), which checks that it can be claimed", 'claim_required']
      );
    });

    test('a refusal after the lease is taken undoes the lease: a vetoed transition leaves none', () => {
      const { engine } = world({ behaviors: [lease, queue, gate], extra: [{ name: 'test.Gate' }] });
      create(engine, 'held');
      const refused = veto(() => claim(engine, worker, 'held'));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], ['test.Gate', 'transition', 'it is held']);
      assert.deepEqual([leaseOf(engine, 'held').holder, leaseOf(engine, 'held').token, statusOf(engine, 'held')], [undefined, 0, 'queued']);
    });

    test('claim expires a lapsed lease first, and checks the status its expiry leaves', () => {
      const { engine, clock } = world();
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      clock.advance(60000);
      assert.deepEqual(claim(engine, other, 'j1'), { id: 'j1', token: 3, expiresAt: T0 + 120000, heartbeatMs: 20000 });
      assert.deepEqual([statusOf(engine, 'j1'), leaseOf(engine, 'j1').holder, leaseOf(engine, 'j1').expiries], ['running', 'otto', 1]);
    });

    test("with Budget on the type, claim reserves after the lease and before the transition, and a reservation that does not fit refuses it", () => {
      reserved.length = 0;
      const { engine } = world({ behaviors: [lease, queue, budgetStandIn], extra: [{ name: 'Budget' }] });
      create(engine, 'j1');
      create(engine, 'broke');
      claim(engine, worker, 'j1');
      assert.deepEqual(reserved, [{ id: 'j1', params: {}, holder: 'wren', status: 'queued' }]);
      assert.equal(veto(() => claim(engine, worker, 'broke')).reason, 'the reservation does not fit');
      assert.deepEqual([leaseOf(engine, 'broke').holder, leaseOf(engine, 'broke').token, statusOf(engine, 'broke')], [undefined, 0, 'queued']);
      // claimNext does not try it, since Budget's checkReserve says it does not fit.
      reserved.length = 0;
      assert.equal(next(engine, other), null);
      assert.deepEqual(reserved, []);
    });
  });

  describe(`Queue: claimNext (${driver})`, () => {
    test('claimNext claims highest priority first, an instance without one last, then oldest, then by id; then none', () => {
      const { engine, clock } = world();
      create(engine, 'low', { priority: 1 });
      clock.advance(1);
      create(engine, 'high', { priority: 9 });
      clock.advance(1);
      create(engine, 'none');
      clock.advance(1);
      create(engine, 'tie-c', { priority: 9 });
      create(engine, 'tie-b', { priority: 9 });
      const order = [1, 2, 3, 4, 5, 6].map(() => next(engine, worker));
      assert.deepEqual(order, ['high', 'tie-b', 'tie-c', 'low', 'none', null]);
      assert.deepEqual(claimNext(engine, worker), null);
    });

    test('a change of priority reorders the queue', () => {
      const { engine } = world();
      create(engine, 'a', { priority: 1 });
      create(engine, 'b', { priority: 2 });
      engine.instances.update(alice, 'Job', 'a', { priority: 5 });
      assert.equal(next(engine, worker), 'a');
      engine.instances.update(alice, 'Job', 'b', { priority: null });
      create(engine, 'c');
      assert.equal(next(engine, worker), 'b');
    });

    test('match takes a list of values, any of which an instance holds: one priority order across them', () => {
      const { engine } = world();
      create(engine, 'search', { topic: 'search', priority: 5 });
      create(engine, 'mail', { topic: 'mail', priority: 9 });
      create(engine, 'misc', { topic: 'misc', priority: 10 });
      create(engine, 'urgent', { topic: 'mail', urgent: true, priority: 1 });
      const both = { match: { topic: ['search', 'mail'] } };
      assert.equal(next(engine, worker, both), 'mail');
      assert.equal(next(engine, worker, { match: { topic: ['search', 'mail'], urgent: [true] } }), 'urgent');
      assert.equal(next(engine, worker, both), 'search');
      assert.equal(next(engine, worker, both), null);
      assert.equal(thrown(() => claimNext(engine, worker, { match: { topic: [] } }), OperationParamsError).code, 'invalid_argument');
    });

    test('claimNext passes ttlMs to the claim', () => {
      const { engine } = world();
      create(engine, 'j1');
      assert.deepEqual(claimNext(engine, worker, { ttlMs: 30000 }), { id: 'j1', token: 1, expiresAt: T0 + 30000, heartbeatMs: 10000 });
    });

    test('a claim forbidden to the caller is skipped like a veto; when every one it tried is, claimNext says why', () => {
      const { engine } = world({ behaviors: [lease, queue, lock], extra: [{ name: 'test.Lock' }] });
      create(engine, 'locked', { priority: 9 });
      create(engine, 'open', { priority: 1 });
      assert.equal(next(engine, worker), 'open');
      const refused = thrown(() => claimNext(engine, worker), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'wren may not claim Job locked: it needs permission jobs.unlock']);
      assert.equal(next(engine, { subject: 'kim', permissions: ['jobs.unlock'] }), 'locked');
    });

    test("match filters on the config's fields by equality, and refuses a field the config does not name", () => {
      const { engine } = world();
      create(engine, 'search', { topic: 'search', priority: 9 });
      create(engine, 'mail', { topic: 'mail' });
      create(engine, 'urgent-mail', { topic: 'mail', urgent: true });
      assert.equal(next(engine, worker, { match: { topic: 'mail', urgent: true } }), 'urgent-mail');
      assert.equal(next(engine, worker, { match: { topic: 'mail' } }), 'mail');
      assert.equal(next(engine, worker, { match: { topic: 'mail' } }), null);
      assert.equal(next(engine, worker, { match: { topic: 'search' } }), 'search');
      assert.deepEqual(thrown(() => claimNext(engine, worker, { match: { title: 'x' } }), OperationParamsError).issues, [
        { path: '/match/title', message: 'claimNext on Job matches topic, urgent, not title' },
      ]);
    });

    test('an instance assigned to a principal is a candidate for that principal only', () => {
      const { engine } = world({ extra: [{ name: 'Assignment', config: { permission: 'jobs.assign' } }] });
      create(engine, 'mine', { priority: 9 });
      create(engine, 'open', { priority: 1 });
      engine.instances.invoke(lead, 'Job', 'mine', 'assign', { to: 'wren' });
      assert.equal(next(engine, other), 'open');
      assert.equal(next(engine, other), null);
      assert.equal(next(engine, worker), 'mine');
      // The claim itself is refused to anyone but the assignee, whatever the copies say.
      create(engine, 'later');
      engine.instances.invoke(lead, 'Job', 'later', 'assign', { to: 'wren' });
      const refused = veto(() => claim(engine, other, 'later'));
      assert.deepEqual([refused.behavior, refused.vetoCode], ['Assignment', 'assigned_to_another']);
    });

    test('assignedOnly takes only the work assigned to the caller, and needs Assignment on the type', () => {
      const { engine } = world({ extra: [{ name: 'Assignment', config: { permission: 'jobs.assign' } }] });
      create(engine, 'open', { priority: 9 });
      create(engine, 'mine', { priority: 1 });
      engine.instances.invoke(lead, 'Job', 'mine', 'assign', { to: 'wren' });
      const count = (who: Principal, params: Record<string, unknown> = {}) =>
        (engine.instances.invokeSchema(who, 'Job', 'countClaimable', params) as { count: number }).count;
      assert.deepEqual([count(worker), count(worker, { assignedOnly: true }), count(other, { assignedOnly: true })], [2, 1, 0]);
      assert.equal(next(engine, worker, { assignedOnly: true }), 'mine');
      assert.equal(next(engine, worker, { assignedOnly: true }), null);
      assert.equal(next(engine, worker), 'open');

      const plain = world().engine;
      assert.deepEqual(thrown(() => claimNext(plain, worker, { assignedOnly: true }), OperationParamsError).issues, [
        { path: '/assignedOnly', message: 'Job does not compose Assignment, so none of its instances is assigned' },
      ]);
    });

    test('countClaimable counts what claimNext would try, without maxCandidates, and changes nothing', () => {
      const { engine } = world({ queue: { ...claimable, maxCandidates: 1 }, extra: [{ name: 'Dependencies' }, { name: 'Assignment', config: { permission: 'jobs.assign' } }] });
      for (const [id, topic] of [['a', 'search'], ['b', 'search'], ['c', 'mail'], ['blocked', 'search'], ['theirs', 'search'], ['failed', 'search']] as const) {
        create(engine, id, { topic });
      }
      engine.instances.invoke(alice, 'Job', 'blocked', 'addBlocker', { id: 'a' });
      engine.instances.invoke(lead, 'Job', 'theirs', 'assign', { to: 'otto' });
      engine.instances.invoke(alice, 'Job', 'failed', 'transition', { to: 'failed' });
      const count = (params: Record<string, unknown> = {}) => (engine.instances.invokeSchema(worker, 'Job', 'countClaimable', params) as { count: number }).count;
      const cursor = engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor;
      assert.deepEqual([count(), count({ match: { topic: 'search' } }), count({ match: { topic: ['search', 'mail'] } })], [3, 2, 3]);
      assert.equal(engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor, cursor);
      assert.deepEqual(thrown(() => count({ match: { title: 'a' } }), OperationParamsError).issues, [
        { path: '/match/title', message: 'countClaimable on Job matches topic, urgent, not title' },
      ]);
      assert.equal(next(engine, worker), 'a');
      assert.equal(count(), 2);
    });

    test('an instance waits while a blocker in another schema is open, and is claimed once the blocker is done', () => {
      const { engine } = world({ extra: [{ name: 'Dependencies', config: { schemas: ['Job', 'Step'] } }] });
      publish(engine, { kind: 'General', name: 'Step', types: { Step: { name: 'Step', role: 'EmbeddedStruct', behaviors: [{ name: 'Workflow', config: stepFlow }], fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } } });
      engine.instances.create(alice, 'Step', { title: 'Approve' }, { id: 's1' });
      create(engine, 'j1');
      engine.instances.invoke(alice, 'Job', 'j1', 'addBlocker', { schema: 'Step', id: 's1' });
      assert.equal(next(engine, worker), null);
      // The step's change, by another principal, refreshes the job's copies through its reference.
      engine.instances.invoke(other, 'Step', 's1', 'transition', { to: 'done' });
      assert.equal(next(engine, worker), 'j1');
      const refreshed = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.find(
        (event) => (event.change as { operation?: string } | null)?.operation === 'refresh'
      );
      assert.equal(refreshed?.actor, 'otto');
    });

    test('an instance a create gives its blockers is no candidate from its create, and hears them as addBlocker would', () => {
      const { engine } = world({ extra: [{ name: 'Dependencies', config: { schemas: ['Job', 'Step'] } }] });
      publish(engine, { kind: 'General', name: 'Step', types: { Step: { name: 'Step', role: 'EmbeddedStruct', behaviors: [{ name: 'Workflow', config: stepFlow }], fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } } });
      engine.instances.create(alice, 'Step', { title: 'Approve' }, { id: 's1' });
      engine.instances.create(alice, 'Job', { title: 'j1' }, { id: 'j1', behaviors: { Dependencies: { blockers: [{ schema: 'Step', id: 's1' }] } } });
      const copies = engine.storage.get(`SELECT bhv_queue__status AS status, bhv_queue__blocked AS blocked FROM engine_instances WHERE schema = 'Job' AND id = 'j1'`);
      assert.deepEqual({ ...copies }, { status: 'queued', blocked: 1 });
      assert.equal(next(engine, worker), null);
      assert.equal((engine.instances.invokeSchema(worker, 'Job', 'countClaimable', {}) as { count: number }).count, 0);
      // Its create event is its only one: the copies were set in it.
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.map((event) => event.kind),
        ['create']
      );
      engine.instances.invoke(other, 'Step', 's1', 'transition', { to: 'done' });
      assert.equal(next(engine, worker), 'j1');
    });

    test('a refresh that finds the copies right changes nothing and appends no event; one that finds them wrong writes', () => {
      const { engine } = world({ extra: [{ name: 'Dependencies', config: { schemas: ['Job', 'Step'] } }] });
      publish(engine, { kind: 'General', name: 'Step', types: { Step: { name: 'Step', role: 'EmbeddedStruct', behaviors: [{ name: 'Workflow', config: stepFlow }], fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } } });
      engine.instances.create(alice, 'Step', { title: 'Approve' }, { id: 's1' });
      engine.instances.create(alice, 'Job', { title: 'j1' }, { id: 'j1', behaviors: { Dependencies: { blockers: [{ schema: 'Step', id: 's1' }] } } });
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq;
      assert.deepEqual(engine.instances.operate(alice, 'Job', 'j1', 'refresh', {}), { result: {}, seq });
      assert.deepEqual(engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.map((event) => event.kind), ['create']);
      // A copy out of step, as one a version before the copies kept would be, is set right by a refresh with its event.
      engine.storage.run(`UPDATE engine_instances SET bhv_queue__blocked = 0 WHERE schema = 'Job' AND id = 'j1'`);
      assert.deepEqual(engine.instances.operate(alice, 'Job', 'j1', 'refresh', {}), { result: {}, seq: (seq as number) + 1 });
      assert.equal(engine.storage.get(`SELECT bhv_queue__blocked AS blocked FROM engine_instances WHERE schema = 'Job' AND id = 'j1'`)?.blocked, 1);
    });

    test("a blocker's change refreshes a dependent another principal holds the lease of: Lease's guard lets refresh through", () => {
      const { engine } = world({ extra: [{ name: 'Dependencies', config: { schemas: ['Job', 'Step'] } }] });
      publish(engine, { kind: 'General', name: 'Step', types: { Step: { name: 'Step', role: 'EmbeddedStruct', behaviors: [{ name: 'Workflow', config: stepFlow }], fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }] } } });
      engine.instances.create(alice, 'Step', { title: 'Review' }, { id: 's1' });
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      engine.instances.invoke(worker, 'Job', 'j1', 'addBlocker', { schema: 'Step', id: 's1' });
      // otto may not change j1 itself while wren holds it...
      assert.equal(veto(() => engine.instances.update(other, 'Job', 'j1', { title: 'Mine' })).behavior, 'Lease');
      // ...but finishing the step, which refreshes j1 as otto, goes through.
      engine.instances.invoke(other, 'Step', 's1', 'transition', { to: 'done' });
      assert.equal(engine.instances.get(alice, 'Step', 's1')?.behaviors.Workflow?.status, 'done');
    });

    test('a stale copy costs a skipped candidate, never a wrong claim; maxCandidates caps the tries', () => {
      const { engine } = world({ queue: { ...claimable, maxCandidates: 2 } });
      for (const [id, priority] of [['s1', 9], ['s2', 8], ['s3', 7], ['good', 1]] as const) {
        create(engine, id, { priority });
      }
      // The three first are failed, but their copies are made to say queued,
      // as copies a change failed to reach would.
      for (const id of ['s1', 's2', 's3']) {
        engine.instances.invoke(alice, 'Job', id, 'transition', { to: 'failed' });
        engine.storage.run(`UPDATE engine_instances SET bhv_queue__status = 'queued' WHERE schema = 'Job' AND id = ?`, [id]);
      }
      // Two tries, both stale: nothing claimed, nothing changed.
      assert.equal(next(engine, worker), null);
      assert.deepEqual(['s1', 's2'].map((id) => [statusOf(engine, id), leaseOf(engine, id).holder]), [['failed', undefined], ['failed', undefined]]);
      publish(engine, jobs({ queue: { ...claimable, maxCandidates: 4 } }));
      assert.equal(next(engine, worker), 'good');
      assert.equal(statusOf(engine, 'good'), 'running');
    });

    test('an instance at maxExpiries is no candidate until its expiries are reset', () => {
      const { engine, clock } = world({ lease: { ...requeue, maxExpiries: 1, overridePermission: 'jobs.override' } });
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      clock.advance(60000);
      engine.instances.invoke(other, 'Job', 'j1', 'expire');
      assert.deepEqual([statusOf(engine, 'j1'), leaseOf(engine, 'j1').expiries], ['queued', 1]);
      assert.equal(next(engine, other), null);
      assert.match(veto(() => claim(engine, other, 'j1')).reason, /expired 1 times/);
      engine.instances.invoke(operator, 'Job', 'j1', 'resetExpiries');
      assert.equal(next(engine, other), 'j1');
    });

    test("the runner's lease sweep puts a claim whose worker went quiet back in the queue", () => {
      const { engine, clock } = world({ lease: { ...requeue, sweepMs: 2000 }, options: { runner: { principal: runner } } });
      engine.runner.runDue();
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      assert.equal(next(engine, other), null);
      clock.advance(60000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual([statusOf(engine, 'j1'), leaseOf(engine, 'j1').holder], ['queued', undefined]);
      assert.equal(next(engine, other), 'j1');
    });
  });

  describe(`Queue: work over its budget is not tried (${driver})`, () => {
    // pooled opens an engine with Pool, whose cpu meter has a limit of 20,
    // pools p1 and p2, and Job, whose claim reserves 10 cpu of the pool
    // its pool link points at, with claimNext trying 2 candidates at most.
    function pooled() {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, {
        kind: 'General',
        name: 'Pool',
        types: {
          Pool: {
            name: 'Pool',
            role: 'EmbeddedStruct',
            behaviors: [{ name: 'Budget', config: { meters: { cpu: { limit: 20 } }, limitPermission: 'pools.limit' } }],
            fields: [{ name: 'title', typeRef: { name: 'string' }, required: true }],
          },
        },
      });
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
          { name: 'Queue', config: { ...claimable, maxCandidates: 2 } },
          { name: 'Budget', config: { meters: { cpu: { scope: 'pool', reserve: 10 } } } },
        ])
      );
      for (const id of ['p1', 'p2']) {
        engine.instances.create(alice, 'Pool', { title: id }, { id });
      }
      return { engine, clock };
    }

    const job = (engine: Engine, id: string, pool: string, priority: number) => {
      create(engine, id, { priority });
      engine.instances.invoke(alice, 'Job', id, 'link', { name: 'pool', id: pool });
    };
    const count = (engine: Engine) => (engine.instances.invokeSchema(worker, 'Job', 'countClaimable', {}) as { count: number }).count;
    const refreshes = (engine: Engine, after: number) =>
      engine.events
        .read(alice, { schema: 'Job', after, limit: 500 })
        .events.filter((event) => (event.change as { operation?: string } | null)?.operation === 'refresh')
        .map((event) => [event.instanceId, event.actor]);
    const cursorOf = (engine: Engine) => engine.events.read(alice, { limit: 500 }).events.at(-1)?.cursor ?? 0;

    test('more over-budget candidates than maxCandidates ahead of a claimable one do not starve it', () => {
      const { engine } = pooled();
      for (const [id, priority] of [['a1', 9], ['a2', 8], ['a3', 7], ['a4', 6], ['a5', 5]] as const) {
        job(engine, id, 'p1', priority);
      }
      job(engine, 'b1', 'p2', 1);
      assert.equal(count(engine), 6);
      // Two claims take p1's whole limit; the second's reservation leaves
      // a3, a4 and a5, more than maxCandidates, out of the candidates.
      assert.equal(next(engine, worker), 'a1');
      const before = cursorOf(engine);
      const second = claimNext(engine, worker) as { id: string; token: number };
      assert.equal(second.id, 'a2');
      assert.deepEqual(refreshes(engine, before), [
        ['a3', 'wren'],
        ['a4', 'wren'],
        ['a5', 'wren'],
      ]);
      assert.equal(count(engine), 1);
      // Copies that let them in, as before they kept the budget, make
      // claimNext try a3 and a4 alone, both refused, and claim nothing.
      const exclusion = (value: number) =>
        engine.storage.run(`UPDATE engine_instances SET bhv_queue__excluded_until = ? WHERE schema = 'Job' AND id IN ('a3', 'a4', 'a5')`, [value]);
      exclusion(0);
      assert.equal(next(engine, worker), null);
      exclusion(Number.MAX_SAFE_INTEGER);
      // So claimNext reaches b1 behind them.
      assert.equal(next(engine, worker), 'b1');
      assert.equal(next(engine, worker), null);

      // a2's release settles its reservation in p1, which lets a3, a4 and
      // a5 back in; a2's own copies follow its release.
      const released = cursorOf(engine);
      engine.instances.invoke(worker, 'Job', 'a2', 'release', {}, fenced(second.token));
      assert.deepEqual(refreshes(engine, released), [
        ['a3', 'wren'],
        ['a4', 'wren'],
        ['a5', 'wren'],
      ]);
      assert.equal(count(engine), 4);
      assert.equal(next(engine, worker), 'a2');
      assert.equal(count(engine), 0);
      // A change of p1 that moves no one's fit refreshes no one.
      const quiet = cursorOf(engine);
      engine.instances.invoke(worker, 'Job', 'a1', 'recordUsage', { meter: 'cpu', amount: 1 }, fenced(1));
      assert.deepEqual(refreshes(engine, quiet), []);
    });

    test("a scope's new limit lets its waiting work in, refreshed as the principal that set it", () => {
      const { engine } = pooled();
      for (const [id, priority] of [['a1', 9], ['a2', 8], ['a3', 7]] as const) {
        job(engine, id, 'p1', priority);
      }
      engine.instances.invoke({ subject: 'pam', permissions: ['pools.limit'] }, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 5 });
      assert.equal(count(engine), 0);
      assert.equal(next(engine, worker), null);
      const cursor = cursorOf(engine);
      engine.instances.invoke({ subject: 'pam', permissions: ['pools.limit'] }, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 30 });
      assert.deepEqual(refreshes(engine, cursor), [
        ['a1', 'pam'],
        ['a2', 'pam'],
        ['a3', 'pam'],
      ]);
      assert.deepEqual([next(engine, worker), next(engine, worker), next(engine, worker), next(engine, worker)], ['a1', 'a2', 'a3', null]);
    });

    test('an instance a create links to a scope without room is excluded from its create, and hears the scope', () => {
      const { engine } = pooled();
      const pam: Principal = { subject: 'pam', permissions: ['pools.limit'] };
      engine.instances.invoke(pam, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 5 });
      engine.instances.create(alice, 'Job', { title: 'a1', priority: 1 }, { id: 'a1', behaviors: { Links: { pool: 'p1' } } });
      engine.instances.create(alice, 'Job', { title: 'b1', priority: 1 }, { id: 'b1', behaviors: { Links: { pool: 'p2' } } });
      const excluded = (id: string) =>
        Number(engine.storage.get(`SELECT bhv_queue__excluded_until AS until FROM engine_instances WHERE schema = 'Job' AND id = ?`, [id])?.until);
      assert.deepEqual([excluded('a1'), excluded('b1')], [Number.MAX_SAFE_INTEGER, 0]);
      assert.equal(count(engine), 1);
      assert.deepEqual([next(engine, worker), next(engine, worker)], ['b1', null]);
      // p1's new limit refreshes a1, which heard p1 from its create.
      const cursor = cursorOf(engine);
      engine.instances.invoke(pam, 'Pool', 'p1', 'setLimit', { meter: 'cpu', limit: 30 });
      assert.deepEqual(refreshes(engine, cursor), [['a1', 'pam']]);
      assert.equal(next(engine, worker), 'a1');
    });

    test("an instance over its own daily limit waits for the next day, which lets it in with no change", () => {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: requeue },
          { name: 'Queue', config: { ...claimable, maxCandidates: 1 } },
          { name: 'Budget', config: { meters: { cpu: { limit: 25, reserve: 10, reset: 'daily' } } } },
        ])
      );
      create(engine, 'spent', { priority: 9 });
      create(engine, 'fresh', { priority: 1 });
      // 20 used leaves 5 of 25, less than a claim's 10.
      engine.instances.invoke(alice, 'Job', 'spent', 'recordUsage', { meter: 'cpu', amount: 20 });
      assert.equal(count(engine), 1);
      assert.equal(next(engine, worker), 'fresh');
      assert.equal(next(engine, worker), null);
      const seq = engine.instances.get(alice, 'Job', 'spent')?.seq;
      clock.ms = DAY;
      assert.equal(count(engine), 1);
      assert.equal(next(engine, worker), 'spent');
      assert.equal(engine.instances.get(alice, 'Job', 'spent')?.seq, (seq as number) + 1);
    });
  });

  describe(`Queue: its config (${driver})`, () => {
    function refusal(engine: Engine, config: Record<string, unknown>): string {
      return thrown(() => engine.schemas.define(alice, jobs({ queue: config })), SchemaDocumentError).message;
    }

    test("parseConfig holds the claim to the type's Workflow and the fields to the type's", () => {
      const engine = openTestEngine({ driver });
      assert.match(refusal(engine, { claim: { from: ['queued'], to: 'working' } }), /behavior Queue config: claim.to "working" is not a state of the type's Workflow \(queued, running, done, failed\)/);
      assert.match(refusal(engine, { claim: { from: ['waiting'], to: 'running' } }), /claim.from names "waiting", which is not a state of the type's Workflow/);
      assert.match(refusal(engine, { claim: { from: ['running'], to: 'running' } }), /claim.from names "running", the state a claim moves the instance to/);
      assert.match(refusal(engine, { claim: { from: ['done'], to: 'running' } }), /claim: no transition of the type's Workflow leads from "done" to "running"/);
      assert.match(refusal(engine, { ...claimable, priorityField: 'title' }), /priorityField names "title", which is not an integer field of Job/);
      assert.match(refusal(engine, { ...claimable, priorityField: 'rank' }), /priorityField names "rank", which is not a field of Job/);
      assert.match(refusal(engine, { ...claimable, match: ['owner'] }), /match names "owner", which is not a field of Job/);
      assert.match(
        thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Queue', config: claimable }])), SchemaDocumentError).message,
        /behavior Queue requires behavior Lease, which the type does not list/
      );
    });

    test('it goes on a schema before it has instances, its config may change, and it is not removed from one that has them', () => {
      const engine = openTestEngine({ driver });
      const plain = jsonsWithout();
      publish(engine, plain);
      engine.instances.create(alice, 'Job', { title: 'Before' }, { id: 'j1' });
      assert.match(thrown(() => engine.schemas.define(alice, jobs({})), IncompatibleChangeError).message, /compose Queue before a schema has instances/);

      const fresh = openTestEngine({ driver });
      publish(fresh, jobs({}));
      fresh.instances.create(alice, 'Job', { title: 'One' }, { id: 'j1' });
      publish(fresh, jobs({ queue: { claim: { from: ['queued'], to: 'running' }, maxCandidates: 5 } }));
      assert.match(thrown(() => fresh.schemas.define(alice, jsonsWithout()), IncompatibleChangeError).message, /the claim facts and blocker references its instances hold would stay behind/);
    });

    // jsonsWithout is the Job schema with Workflow and Lease and no Queue.
    function jsonsWithout(): Record<string, unknown> {
      return jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: requeue }]);
    }
  });
}
