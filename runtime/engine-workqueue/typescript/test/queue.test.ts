// Queue: claim takes the lease and moves the status in one transaction,
// after checks it reads from the instance itself; claimNext picks
// candidates from Queue's own copies, in priority order, filtered by the
// caller's assignment and the match values, and skips the ones whose
// claim is refused; the copies follow the instance and its blockers; and
// the config rules. Real SQLite, a real engine, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type AnyBehaviorImplementation,
  type Engine,
  type EngineOptions,
  type Principal,
} from '@superschematic/engine';

import { lease, queue } from '../dist/index.js';
import { budgetStandIn, gate, openMetaSchema, reserved } from './fixtures.ts';
import { Clock, alice, cleanup, drivers, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const operator: Principal = { subject: 'opal', permissions: ['jobs.override'] };
const lead: Principal = { subject: 'lena', permissions: ['jobs.assign'] };
const runner: Principal = { subject: 'runner', permissions: [] };

const T0 = 1_000_000;

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
  const read = (engine: Engine, id: string) => engine.instances.get(alice, 'Job', id)?.data as Record<string, unknown>;
  const leaseOf = (engine: Engine, id: string) => read(engine, id).lease as { holder: string | null; token: number; expiries: number; active: boolean };
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Queue: claim (${driver})`, () => {
    test('claim takes the lease and moves the status in one call, and returns the token and the heartbeat interval', () => {
      const { engine } = world();
      create(engine, 'j1');
      assert.deepEqual(claim(engine, worker, 'j1'), { id: 'j1', token: 1, expiresAt: T0 + 60000, heartbeatMs: 20000 });
      assert.equal(read(engine, 'j1').status, 'running');
      assert.equal(leaseOf(engine, 'j1').holder, 'wren');
      // One event records both.
      const last = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.deepEqual([last?.actor, (last?.change as { operation: string }).operation], ['wren', 'claim']);
      assert.equal((last?.change as { patch: { status?: string } }).patch.status, 'running');
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
      assert.equal(veto(() => claim(engine, worker, 'j1')).reason, 'it is failed, and it is claimed from queued');
      engine.instances.invoke(alice, 'Job', 'j2', 'addBlocker', { id: 'j3' });
      assert.equal(veto(() => claim(engine, worker, 'j2')).reason, 'a blocker holds it up');
      assert.deepEqual([leaseOf(engine, 'j2').token, read(engine, 'j2').status], [0, 'queued']);
    });

    test('a lease on a type that composes Queue is taken only by claiming it', () => {
      const { engine } = world();
      create(engine, 'j1');
      const refused = veto(() => engine.instances.invoke(worker, 'Job', 'j1', 'acquire'));
      assert.deepEqual([refused.behavior, refused.reason], ['Queue', "its lease is taken by claiming it (Queue's claim), which checks that it can be claimed"]);
    });

    test('a refusal after the lease is taken undoes the lease: a vetoed transition leaves none', () => {
      const { engine } = world({ behaviors: [lease, queue, gate], extra: [{ name: 'test.Gate' }] });
      create(engine, 'held');
      const refused = veto(() => claim(engine, worker, 'held'));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], ['test.Gate', 'transition', 'it is held']);
      assert.deepEqual([leaseOf(engine, 'held').holder, leaseOf(engine, 'held').token, read(engine, 'held').status], [null, 0, 'queued']);
    });

    test('claim expires a lapsed lease first, and checks the status its expiry leaves', () => {
      const { engine, clock } = world();
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      clock.advance(60000);
      assert.deepEqual(claim(engine, other, 'j1'), { id: 'j1', token: 3, expiresAt: T0 + 120000, heartbeatMs: 20000 });
      assert.deepEqual([read(engine, 'j1').status, leaseOf(engine, 'j1').holder, leaseOf(engine, 'j1').expiries], ['running', 'otto', 1]);
    });

    test("with Budget on the type, claim reserves after the lease and before the transition, and a reservation that does not fit refuses it", () => {
      reserved.length = 0;
      const { engine } = world({ behaviors: [lease, queue, budgetStandIn], extra: [{ name: 'Budget' }] });
      create(engine, 'j1');
      create(engine, 'broke');
      claim(engine, worker, 'j1');
      assert.deepEqual(reserved, [{ id: 'j1', params: {}, holder: 'wren', status: 'queued' }]);
      assert.equal(veto(() => claim(engine, worker, 'broke')).reason, 'the reservation does not fit');
      assert.deepEqual([leaseOf(engine, 'broke').holder, leaseOf(engine, 'broke').token, read(engine, 'broke').status], [null, 0, 'queued']);
      // claimNext skips it as a claim it cannot make.
      assert.equal(next(engine, other), null);
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
      assert.equal(veto(() => claim(engine, other, 'later')).behavior, 'Assignment');
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
      assert.equal(engine.instances.get(alice, 'Step', 's1')?.data.status, 'done');
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
      assert.deepEqual(['s1', 's2'].map((id) => [read(engine, id).status, leaseOf(engine, id).holder]), [['failed', null], ['failed', null]]);
      publish(engine, jobs({ queue: { ...claimable, maxCandidates: 4 } }));
      assert.equal(next(engine, worker), 'good');
      assert.equal(read(engine, 'good').status, 'running');
    });

    test('an instance at maxExpiries is no candidate until its expiries are reset', () => {
      const { engine, clock } = world({ lease: { ...requeue, maxExpiries: 1, overridePermission: 'jobs.override' } });
      create(engine, 'j1');
      claim(engine, worker, 'j1');
      clock.advance(60000);
      engine.instances.invoke(other, 'Job', 'j1', 'expire');
      assert.deepEqual([read(engine, 'j1').status, leaseOf(engine, 'j1').expiries], ['queued', 1]);
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
      assert.deepEqual([read(engine, 'j1').status, leaseOf(engine, 'j1').holder], ['queued', null]);
      assert.equal(next(engine, other), 'j1');
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
