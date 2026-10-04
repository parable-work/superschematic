// Lease: acquire, heartbeat and release with the fencing token, presented
// as Lease's precondition, which only the holder of an active lease may
// use; the guard that keeps the lease exclusive and refuses a write that
// presents a stale token, whoever calls, and with requireToken one that
// presents none; expiry, applied once by expire, by an acquire over a
// lapsed lease or by the runner's sweep, with onExpiry and escalate moving
// the status only through Workflow's transition and only from their from
// states, and the reason it gives; an abandon, which counts as an expiry;
// a gone holder's leases, expired at once; maxExpiries and resetExpiries;
// the longest hold; directives tied to the lease they were sent under,
// from a principal or another behavior; the codes its refusals carry; and
// the config rules. Real SQLite, a real engine, a clock the tests move.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import {
  BehaviorVetoError,
  EngineError,
  IncompatibleChangeError,
  OperationParamsError,
  SchemaDocumentError,
  type Engine,
  type EngineOptions,
  type Principal,
} from '@superschematic/engine';

import { gate, openMetaSchema, signal } from './fixtures.ts';
import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';
import { lease } from '../dist/index.js';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const operator: Principal = { subject: 'opal', permissions: ['jobs.override'] };
const sender: Principal = { subject: 'sid', permissions: ['jobs.direct'] };
const runner: Principal = { subject: 'runner', permissions: [] };

const T0 = 1_000_000;

// iso writes an epoch-millisecond time as the refusals do.
const iso = (ms: number) => new Date(ms).toISOString();

const requeue = { onExpiry: { transition: 'queued', from: ['running'] } };

for (const driver of drivers) {
  // world opens an engine whose Job composes Workflow, Lease with the
  // config given, Comments and the extra behaviors, with job j1 queued.
  function world(config?: Record<string, unknown>, extra: BehaviorRef[] = [], options: Partial<EngineOptions> = {}) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now, ...options });
    publish(
      engine,
      jobsDocument([{ name: 'Workflow', config: jobFlow }, config === undefined ? { name: 'Lease' } : { name: 'Lease', config }, { name: 'Comments' }, ...extra])
    );
    engine.instances.create(alice, 'Job', { title: 'Build' }, { id: 'j1' });
    return { engine, clock };
  }

  // invoke calls an operation of a job, presenting token as Lease's precondition when it is given.
  const invoke = (engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}, id = 'j1', token?: number) =>
    engine.instances.invoke(who, 'Job', id, operation, params, token === undefined ? {} : fenced(token));
  const leaseOf = (engine: Engine, id = 'j1') => engine.instances.get(alice, 'Job', id)?.data.lease as Record<string, unknown>;
  const statusOf = (engine: Engine, id = 'j1') => engine.instances.get(alice, 'Job', id)?.data.status;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Lease: acquire, heartbeat and release (${driver})`, () => {
    test('acquire takes the lease with token 1 and the default length and heartbeat; the lease field shows it', () => {
      const { engine } = world();
      assert.deepEqual(leaseOf(engine), { holder: null, token: 0, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: null });
      assert.deepEqual(invoke(engine, worker, 'acquire'), { token: 1, expiresAt: T0 + 60000, heartbeatMs: 20000 });
      assert.deepEqual(leaseOf(engine), { holder: 'wren', token: 1, acquiredAt: T0, renewedAt: T0, expiresAt: T0 + 60000, active: true, expiries: 0, ended: null });
    });

    test('acquire is refused while the lease is active, to another principal and to the holder alike', () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      const refused = veto(() => invoke(engine, other, 'acquire'));
      assert.equal(refused.behavior, 'Lease');
      assert.equal(refused.reason, `another principal holds its lease, until ${iso(T0 + 60000)}`);
      assert.equal(veto(() => invoke(engine, worker, 'acquire')).reason, `the caller already holds its lease, until ${iso(T0 + 60000)}; heartbeat renews it`);
      assert.equal(leaseOf(engine).token, 1);
    });

    test('a heartbeat renews the lease, and needs the holder, its current token and an active lease', () => {
      const { engine, clock } = world();
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'release', {}, 'j1', 1);
      assert.equal((invoke(engine, worker, 'acquire') as { token: number }).token, 3);
      clock.advance(30000);
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq as number;
      assert.deepEqual(invoke(engine, worker, 'heartbeat', {}, 'j1', 3), { expiresAt: T0 + 90000, directives: [] });
      // Each heartbeat is a writing operation, with its event.
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.seq, seq + 1);
      // A process of the holder still on the lease it released is stale.
      assert.equal(veto(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 1)).reason, 'token 1 is stale: the lease is at token 3');
      // The current token does not make another principal the holder.
      assert.equal(veto(() => invoke(engine, other, 'heartbeat', {}, 'j1', 3)).reason, 'another principal holds its lease');
      // Past its expiry time, though no one has applied the expiry yet, the lease cannot be renewed.
      clock.advance(60000);
      assert.match(veto(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 3)).reason, new RegExp(`^its lease expired at ${iso(T0 + 90000)}`));
      assert.deepEqual(leaseOf(engine), { holder: 'wren', token: 3, acquiredAt: T0 + 0, renewedAt: T0 + 30000, expiresAt: T0 + 90000, active: false, expiries: 0, ended: null });
    });

    test('release ends the lease and advances the token, so the next acquire is two tokens on', () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      assert.deepEqual(invoke(engine, worker, 'release', {}, 'j1', 1), {});
      assert.deepEqual(leaseOf(engine), { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: { reason: 'release', at: T0 } });
      assert.equal((invoke(engine, other, 'acquire') as { token: number }).token, 3);
    });

    test('only the holder releases, with its current token; a principal with overridePermission releases any lease', () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      assert.equal(veto(() => invoke(engine, other, 'release', {}, 'j1', 1)).reason, 'another principal holds its lease, and only the holder may release it');
      const untokened = veto(() => invoke(engine, worker, 'release'));
      assert.deepEqual([untokened.vetoCode, untokened.reason], ['token_required', "the holder releases its lease presenting its token as Lease's precondition"]);
      assert.equal(veto(() => invoke(engine, worker, 'release', {}, 'j1', 2)).reason, 'token 2 is stale: the lease is at token 1');
      assert.equal(leaseOf(engine).holder, 'wren');

      const overridden = world({ overridePermission: 'jobs.override' }).engine;
      invoke(overridden, worker, 'acquire');
      const refused = thrown(() => invoke(overridden, other, 'release', {}, 'j1', 1), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(refused.message, "otto may not release another principal's lease of Job j1: it needs permission jobs.override");
      assert.deepEqual(invoke(overridden, operator, 'release'), {});
      assert.deepEqual(leaseOf(overridden), { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: { reason: 'release', at: T0 } });
    });

    test('acquire takes a lease shorter than the config, with a heartbeat scaled to it, and refuses a longer one', () => {
      const { engine, clock } = world();
      assert.deepEqual(invoke(engine, worker, 'acquire', { ttlMs: 30000 }), { token: 1, expiresAt: T0 + 30000, heartbeatMs: 10000 });
      clock.advance(20000);
      // Each heartbeat renews it by the length it was acquired with.
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { expiresAt: number }).expiresAt, T0 + 50000);
      assert.deepEqual(thrown(() => invoke(engine, other, 'acquire', { ttlMs: 90000 }, 'j1'), OperationParamsError).issues, [
        { path: '/ttlMs', message: "90000 is longer than the config's ttlMs, 60000" },
      ]);
    });

    test('acquire needs acquirePermission when the config names one', () => {
      const { engine } = world({ acquirePermission: 'jobs.work' });
      const refused = thrown(() => invoke(engine, worker, 'acquire'), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(refused.message, 'wren may not acquire the lease of Job j1: it needs permission jobs.work');
      assert.equal((invoke(engine, { subject: 'wren', permissions: ['jobs'] }, 'acquire') as { token: number }).token, 1);
    });
  });

  describe(`Lease: the guard (${driver})`, () => {
    test("while the lease is active, another principal's update, delete and writing operations are refused; the holder's go through", () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      const update = veto(() => engine.instances.update(other, 'Job', 'j1', { title: 'Mine' }));
      assert.deepEqual([update.behavior, update.action, update.reason], ['Lease', 'update', `another principal holds its lease, until ${iso(T0 + 60000)}`]);
      assert.equal(veto(() => engine.instances.delete(other, 'Job', 'j1')).action, 'delete');
      assert.equal(veto(() => invoke(engine, other, 'comment', { body: 'Taking over.' })).action, 'comment');
      assert.equal(veto(() => invoke(engine, other, 'transition', { to: 'running' })).action, 'transition');
      // A read-only operation is no change, and Lease's own operations check their callers themselves.
      assert.deepEqual(invoke(engine, other, 'listComments'), { items: [], next: null });
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: false });

      engine.instances.update(worker, 'Job', 'j1', { title: 'Build the site' });
      invoke(engine, worker, 'comment', { body: 'Started.' });
      invoke(engine, worker, 'transition', { to: 'running' });
      assert.equal(statusOf(engine), 'running');
      // A write does not renew the lease: only a heartbeat does.
      assert.equal(leaseOf(engine).expiresAt, T0 + 60000);
    });

    test('an operation the config exempts, and a principal with overridePermission, pass while another holds the lease', () => {
      const { engine } = world({ exempt: ['Comments.comment'], overridePermission: 'jobs.override' });
      invoke(engine, worker, 'acquire');
      assert.equal((invoke(engine, other, 'comment', { body: 'Is it done yet?' }) as { id: number }).id, 1);
      assert.equal(veto(() => invoke(engine, other, 'transition', { to: 'running' })).behavior, 'Lease');
      engine.instances.update(operator, 'Job', 'j1', { title: 'Renamed by an operator' });
      invoke(engine, operator, 'transition', { to: 'running' });
      assert.deepEqual(engine.instances.get(alice, 'Job', 'j1')?.data.title, 'Renamed by an operator');
      assert.equal(statusOf(engine), 'running');
    });

    test("an expired holder no one has expired yet no longer holds the instance: its writes and its release are refused, and others' go through", () => {
      const { engine, clock } = world();
      invoke(engine, worker, 'acquire');
      clock.advance(60000);
      const reason = `the caller's lease expired at ${iso(T0 + 60000)}, so it no longer holds the instance`;
      assert.equal(veto(() => engine.instances.update(worker, 'Job', 'j1', { title: 'Late' })).reason, reason);
      assert.equal(veto(() => invoke(engine, worker, 'comment', { body: 'Late.' })).reason, reason);
      assert.equal(veto(() => invoke(engine, worker, 'release', {}, 'j1', 1)).reason, `its lease expired at ${iso(T0 + 60000)}; expire applies the expiry`);
      engine.instances.update(other, 'Job', 'j1', { title: 'Picked up' });
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.data.title, 'Picked up');
      assert.equal(leaseOf(engine).holder, 'wren');
    });

    test('with no lease held, the guard refuses nothing', () => {
      const { engine } = world();
      engine.instances.update(other, 'Job', 'j1', { title: 'Free' });
      invoke(engine, other, 'transition', { to: 'running' });
      assert.equal(engine.instances.delete(other, 'Job', 'j1'), true);
    });
  });

  describe(`Lease: expiry (${driver})`, () => {
    test('expire applies an expiry once: the holder goes, the token advances, the expiry counts, and onExpiry moves the status', () => {
      const { engine, clock } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: false });
      clock.advance(60000);
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true, reason: 'ttl' });
      assert.deepEqual(leaseOf(engine), { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 1, ended: { reason: 'ttl', at: T0 + 60000 } });
      assert.equal(statusOf(engine), 'queued');
      // The status moved through Workflow's transition, in expire's event, as the principal that expired it.
      const last = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.equal(last?.actor, 'otto');
      assert.deepEqual((last?.change as { operation: string; patch: { status?: string } }).operation, 'expire');
      assert.equal((last?.change as { patch: { status?: string } }).patch.status, 'queued');
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: false });
    });

    test('onExpiry moves the status only from its from states, and an expiry in a terminal state does not count', () => {
      const { engine, clock } = world(requeue);
      engine.instances.create(alice, 'Job', { title: 'Queued' }, { id: 'j2' });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'transition', { to: 'done' });
      invoke(engine, worker, 'acquire', {}, 'j2');
      clock.advance(60000);
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true, reason: 'ttl' });
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries, leaseOf(engine).holder], ['done', 0, null]);
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'j2'), { expired: true, reason: 'ttl' });
      assert.deepEqual([statusOf(engine, 'j2'), leaseOf(engine, 'j2').expiries], ['queued', 1]);
    });

    test('acquire over a lapsed lease applies its expiry first, then leases to the new principal', () => {
      const { engine, clock } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      clock.advance(60000);
      assert.deepEqual(invoke(engine, other, 'acquire'), { token: 3, expiresAt: T0 + 120000, heartbeatMs: 20000 });
      assert.deepEqual(leaseOf(engine), { holder: 'otto', token: 3, acquiredAt: T0 + 60000, renewedAt: T0 + 60000, expiresAt: T0 + 120000, active: true, expiries: 1, ended: null });
      assert.equal(statusOf(engine), 'queued');
      // The expired holder's writes are now another principal's.
      assert.equal(veto(() => engine.instances.update(worker, 'Job', 'j1', { title: 'Late' })).reason, `another principal holds its lease, until ${iso(T0 + 120000)}`);
    });

    test('at maxExpiries the expiry escalates instead, and the lease cannot be acquired until overridePermission resets the count', () => {
      const { engine, clock } = world({ ...requeue, maxExpiries: 2, escalate: { transition: 'failed', from: ['running'] }, overridePermission: 'jobs.override' });
      const lose = () => {
        invoke(engine, worker, 'acquire');
        invoke(engine, worker, 'transition', { to: 'running' });
        clock.advance(60000);
        return invoke(engine, other, 'expire');
      };
      lose();
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['queued', 1]);
      // The expiry that reaches the cap escalates.
      lose();
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['failed', 2]);
      assert.equal(veto(() => invoke(engine, other, 'acquire')).reason, 'its lease has expired 2 times, the most its config allows; resetExpiries lets it be leased again');
      assert.equal(thrown(() => invoke(engine, other, 'resetExpiries'), EngineError).code, 'forbidden');
      assert.deepEqual(invoke(engine, operator, 'resetExpiries'), { expiries: 2 });
      assert.equal(leaseOf(engine).expiries, 0);
      assert.equal((invoke(engine, worker, 'acquire') as { token: number }).token, 5);

      const plain = world().engine;
      assert.equal(veto(() => invoke(plain, operator, 'resetExpiries')).reason, 'its config names no overridePermission, which resetting its expiries needs');
    });

    test('an acquire whose expiry of a lapsed lease would reach the cap is refused, and the lapsed lease stays to expire', () => {
      const { engine, clock } = world({ ...requeue, maxExpiries: 1, escalate: { transition: 'failed', from: ['running'] } });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      clock.advance(60000);
      assert.equal(veto(() => invoke(engine, other, 'acquire')).reason, 'its lease has expired 1 times, the most its config allows; resetExpiries lets it be leased again');
      assert.deepEqual([leaseOf(engine).holder, leaseOf(engine).expiries, statusOf(engine)], ['wren', 0, 'running']);
      invoke(engine, other, 'expire');
      assert.deepEqual([leaseOf(engine).holder, leaseOf(engine).expiries, statusOf(engine)], [null, 1, 'failed']);
    });

    test('a move a guard vetoes leaves the status, and the lease expires all the same; any other refusal fails the expiry', () => {
      // An open blocker gates every terminal state, failed included, so the escalation is vetoed.
      const { engine, clock } = world({ ...requeue, maxExpiries: 1, escalate: { transition: 'failed', from: ['running'] } }, [{ name: 'Dependencies' }]);
      engine.instances.create(alice, 'Job', { title: 'First' }, { id: 'j0' });
      invoke(engine, alice, 'addBlocker', { id: 'j0' });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      clock.advance(60000);
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true, reason: 'ttl' });
      assert.deepEqual([leaseOf(engine).holder, leaseOf(engine).expiries, statusOf(engine)], [null, 1, 'running']);

      // A transition that needs a permission the expirer lacks is no veto: the expiry fails and leaves the lease lapsed.
      const guarded = { ...jobFlow, transitions: jobFlow.transitions.map((move) => (move.from === 'running' && move.to === 'queued' ? { ...move, permission: 'jobs.requeue' } : move)) };
      const clock2 = new Clock(T0);
      const strict = openTestEngine({ driver, clock: clock2.now });
      publish(strict, jobsDocument([{ name: 'Workflow', config: guarded }, { name: 'Lease', config: requeue }]));
      strict.instances.create(alice, 'Job', { title: 'Build' }, { id: 'j1' });
      invoke(strict, worker, 'acquire');
      invoke(strict, worker, 'transition', { to: 'running' });
      clock2.advance(60000);
      assert.equal(thrown(() => invoke(strict, other, 'expire'), EngineError).code, 'forbidden');
      assert.deepEqual([leaseOf(strict).holder, leaseOf(strict).active, statusOf(strict)], ['wren', false, 'running']);
      assert.deepEqual(invoke(strict, { subject: 'sweeper', permissions: ['jobs.requeue'] }, 'expire'), { expired: true, reason: 'ttl' });
      assert.deepEqual([leaseOf(strict).holder, leaseOf(strict).expiries, statusOf(strict)], [null, 1, 'queued']);
    });

    test('a release moves unfinished work back with onExpiry and counts no expiry; finished work stays', () => {
      const { engine } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'release', {}, 'j1', 1);
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['queued', 0]);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'transition', { to: 'done' });
      invoke(engine, worker, 'release', {}, 'j1', 3);
      assert.equal(statusOf(engine), 'done');
    });
  });

  describe(`Lease: the longest hold (${driver})`, () => {
    const limited = { ttlMs: 10000, heartbeatMs: 3000, maxHoldMs: 25000, maxHoldField: 'timeLimitMs' };

    test('heartbeats renew a lease up to its longest hold and no further, and it lapses there', () => {
      const { engine, clock } = world(limited);
      invoke(engine, worker, 'acquire');
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { expiresAt: number }).expiresAt, T0 + 18000);
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { expiresAt: number }).expiresAt, T0 + 25000);
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { expiresAt: number }).expiresAt, T0 + 25000);
      clock.advance(1000);
      assert.match(veto(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 1)).reason, /^its lease expired at/);
      // It reached its longest hold while its holder still renewed it.
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true, reason: 'maxHold' });
    });

    test("the instance's maxHoldField overrides maxHoldMs; a value that is not a positive integer does not", () => {
      const { engine, clock } = world(limited);
      engine.instances.create(alice, 'Job', { title: 'Short', timeLimitMs: 12000 }, { id: 'short' });
      engine.instances.create(alice, 'Job', { title: 'Unset', timeLimitMs: 0 }, { id: 'zero' });
      assert.equal((invoke(engine, worker, 'acquire', {}, 'short') as { expiresAt: number }).expiresAt, T0 + 10000);
      invoke(engine, worker, 'acquire', {}, 'zero');
      clock.advance(9000);
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'short', 1) as { expiresAt: number }).expiresAt, T0 + 12000);
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'zero', 1) as { expiresAt: number }).expiresAt, T0 + 19000);
      clock.advance(3000);
      assert.equal(leaseOf(engine, 'short').active, false);
      assert.equal(leaseOf(engine, 'zero').active, true);
      // A lease past its longest hold is never held longer by a short acquire.
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'short'), { expired: true, reason: 'maxHold' });
    });

    test('the field that limits the hold cannot change while a lease is held, so a holder cannot extend its own', () => {
      const { engine } = world(limited);
      invoke(engine, worker, 'acquire');
      assert.equal(
        veto(() => engine.instances.update(worker, 'Job', 'j1', { timeLimitMs: 999999 })).reason,
        'timeLimitMs limits how long its lease may be held, so it cannot change while the lease is held'
      );
      engine.instances.update(worker, 'Job', 'j1', { title: 'Allowed' });
      invoke(engine, worker, 'release', {}, 'j1', 1);
      engine.instances.update(worker, 'Job', 'j1', { timeLimitMs: 999999 });
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.data.timeLimitMs, 999999);
    });
  });

  describe(`Lease: directives (${driver})`, () => {
    const directed = { overridePermission: 'jobs.override', directPermission: 'jobs.direct' };

    test('a directive reaches the holder of the active lease on each heartbeat until it acknowledges it', () => {
      const { engine, clock } = world(directed);
      assert.equal(veto(() => invoke(engine, sender, 'direct', { name: 'cancel' })).reason, 'no lease is active, so there is no holder to direct');
      invoke(engine, worker, 'acquire');
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'cancel', data: { reason: 'superseded' } }), { id: 1, created: true });
      clock.advance(10);
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'pause' }), { id: 2, created: true });
      const both = [
        { id: 1, name: 'cancel', data: { reason: 'superseded' }, createdAt: T0, createdBy: 'sid' },
        { id: 2, name: 'pause', createdAt: T0 + 10, createdBy: 'sid' },
      ];
      assert.deepEqual((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { directives: unknown }).directives, both);
      // Delivery is at least once: they come again until acknowledged.
      assert.deepEqual((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { directives: unknown }).directives, both);
      assert.deepEqual(invoke(engine, worker, 'acknowledge', { ids: [1] }, 'j1', 1), {});
      assert.deepEqual((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { directives: unknown }).directives, [both[1]]);
      // Acknowledging one again changes nothing.
      assert.deepEqual(invoke(engine, worker, 'acknowledge', { ids: [1] }, 'j1', 1), {});
    });

    test('a heartbeat acknowledges directives in the same write, and refuses an id not sent under its token', () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      invoke(engine, sender, 'direct', { name: 'pause' });
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq as number;
      assert.deepEqual(thrown(() => invoke(engine, worker, 'heartbeat', { acknowledge: [1, 3] }, 'j1', 1), OperationParamsError).issues, [
        { path: '/acknowledge', message: 'no directive 3 was sent under token 1' },
      ]);
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.seq, seq);
      const beat = invoke(engine, worker, 'heartbeat', { acknowledge: [1] }, 'j1', 1) as { directives: Array<{ id: number }> };
      assert.deepEqual(beat.directives.map((one) => one.id), [2]);
      // One write, one event, for the heartbeat and the acknowledgement.
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.seq, seq + 1);
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { acknowledge: [] }, 'j1', 1) as { directives: unknown[] }).directives.length, 1);
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { acknowledge: [2] }, 'j1', 1) as { directives: unknown[] }).directives, []);
    });

    test('a directive sent with a dedupeKey is sent once per lease, acknowledged or not, and again to the next lease', () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      const once = { name: 'cancel', dedupeKey: 'budget:cpu' };
      assert.deepEqual(invoke(engine, sender, 'direct', once), { id: 1, created: true });
      assert.deepEqual(invoke(engine, sender, 'direct', { ...once, data: { again: true } }), { id: 1, created: false });
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'cancel' }), { id: 2, created: true });
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { acknowledge: [1] }, 'j1', 1) as { directives: unknown }).directives, [
        { id: 2, name: 'cancel', createdAt: T0, createdBy: 'sid' },
      ]);
      assert.deepEqual(invoke(engine, sender, 'direct', once), { id: 1, created: false });
      invoke(engine, worker, 'release', {}, 'j1', 1);
      invoke(engine, worker, 'acquire');
      assert.deepEqual(invoke(engine, sender, 'direct', once), { id: 1, created: true });
      assert.deepEqual((invoke(engine, worker, 'heartbeat', {}, 'j1', 3) as { directives: unknown }).directives, [
        { id: 1, name: 'cancel', dedupeKey: 'budget:cpu', createdAt: T0, createdBy: 'sid' },
      ]);
    });

    test('only the holder acknowledges, with its current token, the directives sent under it', () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      assert.equal(veto(() => invoke(engine, other, 'acknowledge', { ids: [1] }, 'j1', 1)).reason, 'another principal holds its lease');
      assert.equal(veto(() => invoke(engine, worker, 'acknowledge', { ids: [1] }, 'j1', 2)).reason, 'token 2 is stale: the lease is at token 1');
      assert.deepEqual(thrown(() => invoke(engine, worker, 'acknowledge', { ids: [1, 3] }, 'j1', 1), OperationParamsError).issues, [
        { path: '/ids', message: 'no directive 3 was sent under token 1' },
      ]);
      // Nothing was acknowledged by the refused call.
      assert.equal((invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { directives: unknown[] }).directives.length, 1);
    });

    test('the directives of a lease that ends never reach the next holder', () => {
      const { engine, clock } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      invoke(engine, worker, 'release', {}, 'j1', 1);
      invoke(engine, other, 'acquire');
      assert.deepEqual((invoke(engine, other, 'heartbeat', {}, 'j1', 3) as { directives: unknown }).directives, []);
      // Nor across an expiry; a directive's number counts within its lease.
      invoke(engine, sender, 'direct', { name: 'pause' });
      clock.advance(60000);
      invoke(engine, worker, 'acquire');
      assert.deepEqual((invoke(engine, worker, 'heartbeat', {}, 'j1', 5) as { directives: unknown }).directives, []);
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'pause' }), { id: 1, created: true });
    });

    test('direct needs directPermission, or overridePermission when that is absent, and neither configured refuses it', () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      const refused = thrown(() => invoke(engine, other, 'direct', { name: 'cancel' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'otto may not send a directive to the holder of Job j1: it needs permission jobs.direct']);
      // With directPermission set, overridePermission does not send directives.
      assert.equal(thrown(() => invoke(engine, operator, 'direct', { name: 'cancel' }), EngineError).code, 'forbidden');
      // The holder needs the permission too.
      assert.equal(thrown(() => invoke(engine, worker, 'direct', { name: 'cancel' }), EngineError).code, 'forbidden');

      const overrideOnly = world({ overridePermission: 'jobs.override' }).engine;
      invoke(overrideOnly, worker, 'acquire');
      assert.deepEqual(invoke(overrideOnly, operator, 'direct', { name: 'cancel' }), { id: 1, created: true });

      const none = world().engine;
      invoke(none, worker, 'acquire');
      assert.equal(
        veto(() => invoke(none, operator, 'direct', { name: 'cancel' })).reason,
        'its config names no permission that sends directives (directPermission or overridePermission)'
      );
    });

    test("deleting the instance takes its lease and its directives with it", () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM bhv_lease__directives')?.n, 1);
      assert.equal(engine.instances.delete(worker, 'Job', 'j1'), true);
      assert.equal(engine.storage.get('SELECT COUNT(*) AS n FROM bhv_lease__directives')?.n, 0);
    });
  });

  describe(`Lease: the token fences writes (${driver})`, () => {
    // Two processes of one principal: a, whose lease lapsed, and b, which
    // took the instance again. Both act as wren.
    function retaken(config: Record<string, unknown> = {}) {
      const { engine, clock } = world({ ...requeue, ...config });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' }, 'j1', 1);
      clock.advance(60000);
      const { token } = invoke(engine, worker, 'acquire') as { token: number };
      invoke(engine, worker, 'transition', { to: 'running' }, 'j1', token);
      return { engine, clock, token };
    }

    test("a write that presents a token other than the current one is refused, so a process that lost its lease cannot write after a sibling of its principal takes it again", () => {
      const { engine, token } = retaken();
      assert.equal(token, 3);
      const update = veto(() => engine.instances.update(worker, 'Job', 'j1', { title: 'Stale' }, fenced(1)));
      assert.deepEqual([update.behavior, update.action, update.vetoCode, update.vetoDetails], ['Lease', 'update', 'token_stale', { token: 1, current: 3 }]);
      assert.equal(update.reason, 'token 1 is stale: the lease is at token 3');
      assert.equal(veto(() => invoke(engine, worker, 'transition', { to: 'done' }, 'j1', 1)).vetoCode, 'token_stale');
      assert.equal(veto(() => invoke(engine, worker, 'comment', { body: 'Done, I think.' }, 'j1', 1)).vetoCode, 'token_stale');
      assert.equal(veto(() => engine.instances.delete(worker, 'Job', 'j1', fenced(1))).vetoCode, 'token_stale');
      // Whoever calls: an operator with overridePermission who presents a stale token is refused too.
      assert.equal(veto(() => engine.instances.update(operator, 'Job', 'j1', { title: 'Stale' }, fenced(2))).vetoCode, 'token_stale');
      // The process that holds the current token writes.
      invoke(engine, worker, 'transition', { to: 'done' }, 'j1', token);
      assert.equal(statusOf(engine), 'done');
      // Without requireToken, a write that presents no token is the principal's, which holds the lease.
      engine.instances.update(worker, 'Job', 'j1', { title: 'Unfenced' });
      // A read-only operation presents nothing that can be stale.
      assert.deepEqual(invoke(engine, worker, 'listComments', {}, 'j1', 1), { items: [], next: null });
      // The heartbeat, the acknowledgement and the release of the old lease are refused the same way.
      assert.equal(veto(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 1)).vetoCode, 'token_stale');
      assert.equal(veto(() => invoke(engine, worker, 'release', {}, 'j1', 1)).vetoCode, 'token_stale');
    });

    test('a token on a free instance is checked too: a lease that ended leaves its token stale, and the current one is a compare-and-set for acquire', () => {
      const { engine } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'release', {}, 'j1', 1);
      assert.equal(veto(() => engine.instances.update(worker, 'Job', 'j1', { title: 'After' }, fenced(1))).vetoCode, 'token_stale');
      assert.equal(veto(() => invoke(engine, other, 'acquire', {}, 'j1', 1)).vetoCode, 'token_stale');
      assert.equal((invoke(engine, other, 'acquire', {}, 'j1', 2) as { token: number }).token, 3);
    });

    test("with requireToken, a write under an active lease presents the current token, the holder's own included; exempt and read-only operations, an override and Lease's own operations are not held to it", () => {
      const { engine, token } = retaken({ requireToken: true, exempt: ['Comments.comment'], overridePermission: 'jobs.override' });
      const bare = veto(() => engine.instances.update(worker, 'Job', 'j1', { title: 'Bare' }));
      assert.deepEqual([bare.vetoCode, bare.reason], ['token_required', "its config requires every write under the lease to present the lease's token as Lease's precondition"]);
      assert.equal(veto(() => invoke(engine, worker, 'transition', { to: 'done' })).vetoCode, 'token_required');
      assert.equal(veto(() => engine.instances.delete(worker, 'Job', 'j1')).vetoCode, 'token_required');
      // Another principal is refused for holding no lease, before any token.
      assert.equal(veto(() => engine.instances.update(other, 'Job', 'j1', { title: 'Mine' })).vetoCode, 'held_by_another');
      engine.instances.update(worker, 'Job', 'j1', { title: 'Fenced' }, fenced(token));
      invoke(engine, other, 'comment', { body: 'Exempt.' });
      invoke(engine, worker, 'comment', { body: 'Exempt for the holder too.' });
      assert.equal((invoke(engine, worker, 'listComments') as { items: unknown[] }).items.length, 2);
      engine.instances.update(operator, 'Job', 'j1', { title: 'Overridden' });
      assert.deepEqual(invoke(engine, worker, 'heartbeat', {}, 'j1', token), { expiresAt: T0 + 120000, directives: [] });
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: false });
      // Once the lease is released, nothing is held to the token.
      invoke(engine, worker, 'release', {}, 'j1', token);
      engine.instances.update(worker, 'Job', 'j1', { title: 'Free' });
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.data.title, 'Free');
    });

    test("with requireToken, the runner's sweep still expires a lapsed lease", () => {
      const { engine, clock } = world({ ...requeue, requireToken: true, sweepMs: 1000 }, [], { runner: { principal: runner } });
      engine.runner.runDue();
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' }, 'j1', 1);
      clock.advance(60000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual([statusOf(engine), leaseOf(engine).holder, leaseOf(engine).ended], ['queued', null, { reason: 'ttl', at: T0 + 60000 }]);
    });

    test('heartbeat, acknowledge and a release need the holder presenting its token; each refusal carries its code', () => {
      const { engine, clock } = world({ directPermission: 'jobs.direct' });
      const code = (fn: () => unknown) => veto(fn).vetoCode;
      assert.equal(code(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 0)), 'not_leased');
      assert.equal(code(() => invoke(engine, worker, 'release', {}, 'j1', 0)), 'not_leased');
      assert.equal(code(() => invoke(engine, sender, 'direct', { name: 'stop' })), 'not_leased');
      invoke(engine, worker, 'acquire');
      assert.equal(code(() => invoke(engine, worker, 'acquire')), 'held_by_caller');
      assert.equal(code(() => invoke(engine, other, 'acquire')), 'held_by_another');
      assert.equal(code(() => invoke(engine, worker, 'heartbeat')), 'token_required');
      assert.equal(code(() => invoke(engine, worker, 'acknowledge', { ids: [1] })), 'token_required');
      assert.equal(code(() => invoke(engine, other, 'heartbeat', {}, 'j1', 1)), 'not_holder');
      assert.equal(code(() => invoke(engine, other, 'release', {}, 'j1', 1)), 'not_holder');
      clock.advance(60000);
      const lapsed = veto(() => invoke(engine, worker, 'heartbeat', {}, 'j1', 1));
      assert.deepEqual([lapsed.vetoCode, lapsed.vetoDetails], ['lapsed', { expiredAt: T0 + 60000 }]);
      assert.equal(code(() => engine.instances.update(worker, 'Job', 'j1', { title: 'Late' })), 'lapsed');
      assert.equal(code(() => invoke(engine, sender, 'direct', { name: 'stop' })), 'lapsed');
      assert.equal(code(() => invoke(engine, operator, 'resetExpiries')), 'not_configured');
    });
  });

  describe(`Lease: abandon (${driver})`, () => {
    test('a release with abandon counts as an expiry: onExpiry moves the status, and the abandon that reaches maxExpiries escalates', () => {
      const { engine } = world({ ...requeue, maxExpiries: 2, escalate: { transition: 'failed', from: ['running'] } });
      const take = () => {
        const { token } = invoke(engine, worker, 'acquire') as { token: number };
        invoke(engine, worker, 'transition', { to: 'running' }, 'j1', token);
        return token;
      };
      // A plain release hands the work back without counting.
      invoke(engine, worker, 'release', {}, 'j1', take());
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries, leaseOf(engine).ended], ['queued', 0, { reason: 'release', at: T0 }]);
      assert.deepEqual(invoke(engine, worker, 'release', { abandon: true }, 'j1', take()), {});
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries, leaseOf(engine).ended], ['queued', 1, { reason: 'abandon', at: T0 }]);
      const last = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1)?.change as { params: unknown; patch: Record<string, unknown> };
      assert.deepEqual(last.params, { abandon: true });
      assert.deepEqual(last.patch, {
        status: 'queued',
        lease: { holder: null, token: 4, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 1, ended: { reason: 'abandon', at: T0 } },
      });
      // The abandon that reaches the cap escalates, and the lease cannot be taken again.
      invoke(engine, worker, 'release', { abandon: true }, 'j1', take());
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['failed', 2]);
      const refused = veto(() => invoke(engine, worker, 'acquire'));
      assert.deepEqual([refused.vetoCode, refused.vetoDetails], ['max_expiries', { expiries: 2, maxExpiries: 2 }]);
    });

    test('an abandon of finished work does not count, and an operator may abandon a lease it overrides', () => {
      const { engine } = world({ ...requeue, overridePermission: 'jobs.override' });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' }, 'j1', 1);
      invoke(engine, worker, 'transition', { to: 'done' }, 'j1', 1);
      invoke(engine, worker, 'release', { abandon: true }, 'j1', 1);
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['done', 0]);
      engine.instances.create(alice, 'Job', { title: 'Next' }, { id: 'j2' });
      invoke(engine, worker, 'acquire', {}, 'j2');
      invoke(engine, worker, 'transition', { to: 'running' }, 'j2', 1);
      invoke(engine, operator, 'release', { abandon: true }, 'j2');
      assert.deepEqual([statusOf(engine, 'j2'), leaseOf(engine, 'j2').expiries, (leaseOf(engine, 'j2').ended as { reason: string }).reason], ['queued', 1, 'abandon']);
    });
  });

  describe(`Lease: why a lease ended (${driver})`, () => {
    test('expire says why: ttl for a holder that stopped renewing, maxHold for one that reached its longest hold, holder for an active lease expired by name', () => {
      const { engine, clock } = world({ ...requeue, maxHoldMs: 90000, overridePermission: 'jobs.override' });
      for (const id of ['j2', 'j3', 'j4']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
      }
      for (const id of ['j1', 'j2', 'j3', 'j4']) {
        invoke(engine, worker, 'acquire', {}, id);
        invoke(engine, worker, 'transition', { to: 'running' }, id, 1);
      }
      // j2, j3 and j4 are renewed up to their longest hold; j1 is not renewed.
      clock.advance(50000);
      for (const id of ['j2', 'j3', 'j4']) {
        assert.deepEqual(invoke(engine, worker, 'heartbeat', {}, id, 1), { expiresAt: T0 + 90000, directives: [] });
      }
      clock.advance(20000);
      assert.deepEqual(invoke(engine, operator, 'expire', { holder: 'wren' }, 'j3'), { expired: true, reason: 'holder' });
      clock.advance(20000);
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'j1'), { expired: true, reason: 'ttl' });
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'j2'), { expired: true, reason: 'maxHold' });
      // Each expiry's event carries the reason in the lease's ended.
      const ended = (id: string) =>
        (engine.events.read(alice, { schema: 'Job', instanceId: id }).events.at(-1)?.change as { patch: { lease: { ended: unknown } } }).patch.lease.ended;
      assert.deepEqual(['j1', 'j2', 'j3'].map(ended), [
        { reason: 'ttl', at: T0 + 90000 },
        { reason: 'maxHold', at: T0 + 90000 },
        { reason: 'holder', at: T0 + 70000 },
      ]);
      // expireHolder counts by reason: j4, lapsed at its longest hold, is wren's last lease.
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren' }), { expired: 1, reasons: { maxHold: 1 }, ids: ['j4'] });
      // A new acquire clears ended: it describes the last lease that ended, not the one held.
      invoke(engine, other, 'acquire');
      assert.equal(leaseOf(engine).ended, null);
    });

    test('expire with a holder whose lease has lapsed gives the lapse as its reason', () => {
      const { engine, clock } = world({ ...requeue, overridePermission: 'jobs.override' });
      invoke(engine, worker, 'acquire');
      clock.advance(60000);
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren' }), { expired: 1, reasons: { ttl: 1 }, ids: ['j1'] });
    });
  });

  describe(`Lease: expiry on the runner, and a gone holder (${driver})`, () => {
    test("every sweepMs the runner expires the leases past their expiry time or their longest hold, as its principal", () => {
      const { engine, clock } = world({ ...requeue, sweepMs: 2000, maxHoldField: 'timeLimitMs', overridePermission: 'jobs.override' }, [], {
        runner: { principal: runner },
      });
      engine.runner.runDue();
      const [schedule] = engine.runner.status().schedules;
      assert.deepEqual([schedule.behavior, schedule.schedule, schedule.everyMs, schedule.next], ['Lease', 'expire', 2000, T0 + 2000]);
      for (const id of ['j2', 'j3']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
      }
      for (const id of ['j1', 'j2', 'j3']) {
        invoke(engine, worker, 'acquire', {}, id);
        invoke(engine, worker, 'transition', { to: 'running' }, id);
      }
      // j3's limit is cut below its expiry time: past it, the lease has lapsed though expiresAt has not come.
      engine.instances.update(operator, 'Job', 'j3', { timeLimitMs: 30000 });
      clock.advance(30000);
      invoke(engine, worker, 'heartbeat', {}, 'j2', 1);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(
        ['j1', 'j2', 'j3'].map((id) => [statusOf(engine, id), leaseOf(engine, id).holder]),
        [['running', 'wren'], ['running', 'wren'], ['queued', null]]
      );
      clock.advance(30000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(
        ['j1', 'j2', 'j3'].map((id) => [statusOf(engine, id), leaseOf(engine, id).holder, leaseOf(engine, id).expiries]),
        [['queued', null, 1], ['running', 'wren', 0], ['queued', null, 1]]
      );
      const last = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.deepEqual([last?.actor, (last?.change as { operation: string }).operation, last?.cause], [
        'runner',
        'expire',
        { behavior: 'Lease', schedule: 'expire', depth: 1 },
      ]);
    });

    test("a guard's veto of one expiry leaves that lease for the next sweep, and the sweep goes on", () => {
      const clock = new Clock(T0);
      const engine = openTestEngine({ driver, clock: clock.now, behaviors: [lease, gate], metaSchema: openMetaSchema(), runner: { principal: runner } });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: requeue }, { name: 'test.Gate' }]));
      for (const id of ['a', 'stuck', 'z']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
        invoke(engine, worker, 'acquire', {}, id);
      }
      engine.runner.runDue();
      clock.advance(60000);
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 1 });
      assert.deepEqual(['a', 'stuck', 'z'].map((id) => leaseOf(engine, id).holder), [null, 'wren', null]);
    });

    test('expire with a holder expires that holder\'s lease at once, active or not, and needs overridePermission', () => {
      const { engine } = world({ ...requeue, overridePermission: 'jobs.override' });
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      assert.equal(thrown(() => invoke(engine, other, 'expire', { holder: 'wren' }), EngineError).code, 'forbidden');
      assert.deepEqual(invoke(engine, operator, 'expire', { holder: 'otto' }), { expired: false });
      assert.deepEqual(invoke(engine, operator, 'expire', { holder: 'wren' }), { expired: true, reason: 'holder' });
      assert.deepEqual([statusOf(engine), leaseOf(engine).holder, leaseOf(engine).expiries], ['queued', null, 1]);
    });

    test('notRenewedAfter spares an active lease its holder renewed after the time, and expireHolder lists what it expired', () => {
      const { engine, clock } = world({ ...requeue, overridePermission: 'jobs.override' });
      for (const id of ['j2', 'j3']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
        invoke(engine, worker, 'acquire', {}, id);
      }
      invoke(engine, worker, 'acquire', {}, 'j1');
      assert.equal(leaseOf(engine).renewedAt, T0);
      clock.advance(20000);
      invoke(engine, worker, 'heartbeat', {}, 'j2', 1);
      assert.deepEqual([leaseOf(engine, 'j2').renewedAt, leaseOf(engine, 'j2').acquiredAt], [T0 + 20000, T0]);
      // j2 was renewed after T0 + 10000, so its holder's own heartbeats show it alive; j1 and j3 were not.
      clock.advance(1000);
      assert.deepEqual(invoke(engine, operator, 'expire', { holder: 'wren', notRenewedAfter: T0 + 10000 }, 'j2'), { expired: false });
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren', notRenewedAfter: T0 + 10000 }), {
        expired: 2,
        reasons: { holder: 2 },
        ids: ['j1', 'j3'],
      });
      assert.deepEqual(['j1', 'j2', 'j3'].map((id) => leaseOf(engine, id).holder), [null, 'wren', null]);
      // A lapsed lease expires whenever it was renewed.
      clock.advance(60000);
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren', notRenewedAfter: T0 }), {
        expired: 1,
        reasons: { ttl: 1 },
        ids: ['j2'],
      });
      assert.deepEqual(thrown(() => invoke(engine, operator, 'expire', { notRenewedAfter: T0 }), OperationParamsError).issues, [
        { path: '/notRenewedAfter', message: "notRenewedAfter spares a holder's active leases, so it needs holder" },
      ]);
    });

    test("expireHolder expires every lease one holder has on the schema, and needs overridePermission, which the config must name", () => {
      const { engine, clock } = world({ ...requeue, overridePermission: 'jobs.override' });
      for (const id of ['j2', 'j3']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
      }
      invoke(engine, worker, 'acquire', {}, 'j1');
      invoke(engine, worker, 'transition', { to: 'running' }, 'j1');
      invoke(engine, worker, 'acquire', {}, 'j2');
      invoke(engine, other, 'acquire', {}, 'j3');
      clock.advance(1000);
      const refused = thrown(() => engine.instances.invokeSchema(other, 'Job', 'expireHolder', { holder: 'wren' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'otto may not expire the leases of a holder on Job: it needs permission jobs.override']);
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren' }), { expired: 2, reasons: { holder: 2 }, ids: ['j1', 'j2'] });
      assert.deepEqual(
        ['j1', 'j2', 'j3'].map((id) => [statusOf(engine, id), leaseOf(engine, id).holder, leaseOf(engine, id).expiries]),
        [['queued', null, 1], ['queued', null, 1], ['queued', 'otto', 0]]
      );
      assert.deepEqual(engine.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren' }), { expired: 0, reasons: {}, ids: [] });

      const none = world().engine;
      const unnamed = thrown(() => none.instances.invokeSchema(operator, 'Job', 'expireHolder', { holder: 'wren' }), EngineError);
      assert.deepEqual([unnamed.code, unnamed.message], ['forbidden', 'opal may not expire the leases of a holder on Job: its Lease config names no overridePermission']);
    });
  });

  describe(`Lease: directives from another behavior (${driver})`, () => {
    function signalled(config?: Record<string, unknown>) {
      const engine = openTestEngine({ driver, behaviors: [lease, signal], metaSchema: openMetaSchema() });
      publish(
        engine,
        jobsDocument([{ name: 'Workflow', config: jobFlow }, config === undefined ? { name: 'Lease' } : { name: 'Lease', config }, { name: 'test.Signal' }])
      );
      engine.instances.create(alice, 'Job', { title: 'Build' }, { id: 'j1' });
      return engine;
    }

    test("a behavior of the type sends a directive through call(), as its principal, without the permission a principal's own direct needs", () => {
      const engine = signalled({ directPermission: 'jobs.direct' });
      invoke(engine, worker, 'acquire');
      assert.equal(thrown(() => invoke(engine, worker, 'direct', { name: 'stop' }), EngineError).code, 'forbidden');
      assert.deepEqual(invoke(engine, worker, 'signal', { name: 'stop' }), { id: 1, created: true });
      const [directive] = (invoke(engine, worker, 'heartbeat', {}, 'j1', 1) as { directives: Array<{ name: string; createdBy: string }> }).directives;
      assert.deepEqual([directive.name, directive.createdBy], ['stop', 'wren']);
    });

    test('with no permission in the config, a principal cannot send one and a behavior still can', () => {
      const engine = signalled();
      invoke(engine, worker, 'acquire');
      const refused = veto(() => invoke(engine, worker, 'direct', { name: 'stop' }));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], [
        'Lease',
        'direct',
        'its config names no permission that sends directives (directPermission or overridePermission)',
      ]);
      assert.deepEqual(invoke(engine, worker, 'signal', { name: 'stop' }), { id: 1, created: true });
      // There must still be a lease to direct.
      invoke(engine, worker, 'release', {}, 'j1', 1);
      assert.equal(veto(() => invoke(engine, worker, 'signal', { name: 'stop' })).reason, 'no lease is active, so there is no holder to direct');
    });
  });

  describe(`Lease: its config (${driver})`, () => {
    function refusal(engine: Engine, config: Record<string, unknown>, behaviors?: BehaviorRef[]): string {
      const document = jobsDocument(behaviors ?? [{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config }, { name: 'Comments' }]);
      return thrown(() => engine.schemas.define(alice, document), SchemaDocumentError).message;
    }

    test('parseConfig holds the config to the type: its heartbeat, its fields, its Workflow states and its behaviors', () => {
      const engine = openTestEngine({ driver });
      assert.match(refusal(engine, { heartbeatMs: 60000 }), /behavior Lease config: heartbeatMs \(60000\) is more than half of ttlMs \(60000\): one late heartbeat would lose the lease/);
      assert.match(refusal(engine, { ttlMs: 3000, heartbeatMs: 5000 }), /heartbeatMs \(5000\) is more than half of ttlMs \(3000\)/);
      // Half of ttlMs is the most: one heartbeat can be late by a whole interval.
      assert.match(refusal(engine, { ttlMs: 10000, heartbeatMs: 5001 }), /heartbeatMs \(5001\) is more than half of ttlMs \(10000\)/);
      assert.match(refusal(engine, { onExpiry: { transition: 'waiting', from: ['running'] } }), /onExpiry.transition "waiting" is not a state of the type's Workflow \(queued, running, done, failed\)/);
      assert.match(refusal(engine, { onExpiry: { transition: 'queued', from: ['paused'] } }), /onExpiry.from names "paused", which is not a state of the type's Workflow/);
      assert.match(refusal(engine, { onExpiry: { transition: 'queued', from: ['done'] } }), /onExpiry: no transition of the type's Workflow leads from "done" to "queued"/);
      assert.match(refusal(engine, { onExpiry: { transition: 'queued', from: ['queued'] } }), /onExpiry moves the status from "queued" to itself/);
      assert.match(
        refusal(engine, { maxExpiries: 3, escalate: { transition: 'done', from: ['queued'] } }),
        /escalate: no transition of the type's Workflow leads from "queued" to "done"/
      );
      assert.match(refusal(engine, { escalate: { transition: 'failed', from: ['running'] } }), /escalate applies at the expiry that reaches maxExpiries, which the config does not give/);
      assert.match(
        refusal(engine, requeue, [{ name: 'Lease', config: requeue }]),
        /behavior Lease config: onExpiry moves the status through Workflow, which the type does not list/
      );
      assert.match(refusal(engine, { exempt: ['Revisions.approve'] }), /exempt names Revisions.approve, but the type does not list Revisions \(it lists Workflow, Lease, Comments\)/);
      assert.match(refusal(engine, { exempt: ['Lease.release'] }), /exempt names Lease.release: Lease's own operations check their callers themselves/);
      assert.match(refusal(engine, { maxHoldField: 'deadline' }), /maxHoldField "deadline" is not a field of Job \(its fields: title, priority, timeLimitMs, topic, urgent\)/);
      assert.match(refusal(engine, { maxHoldField: 'title' }), /maxHoldField "title" is not an integer field of Job/);
      // The core meta-schema holds the shape first.
      assert.ok(
        thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Lease', config: { ttlMs: 500 } }])), SchemaDocumentError).issues.some(
          (issue) => issue.path === '/types/Job/behaviors/0/config/ttlMs'
        )
      );
      publish(
        engine,
        jobsDocument([
          { name: 'Workflow', config: jobFlow },
          { name: 'Lease', config: { ...requeue, ttlMs: 10000, heartbeatMs: 5000, exempt: ['Comments.comment'], maxHoldField: 'timeLimitMs' } },
          { name: 'Comments' },
        ])
      );
    });

    test('it can be added to a schema that has instances, which start free, and cannot be removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }]));
      engine.instances.create(alice, 'Job', { title: 'Before' }, { id: 'j1' });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 30000 } }]));
      assert.deepEqual(leaseOf(engine), { holder: null, token: 0, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: null });
      // Its config may change.
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 20000 } }]));
      const refused = thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }])), IncompatibleChangeError);
      assert.match(refused.message, /behavior Lease cannot be removed from type Job, which has instances: the leases and directives its instances hold would stay behind/);
    });
  });
}
