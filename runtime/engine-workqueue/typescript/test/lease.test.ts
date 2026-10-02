// Lease: acquire, heartbeat and release with the fencing token, which only
// the holder of an active lease may use; the guard that keeps the lease
// exclusive; expiry, applied once by expire or by an acquire over a lapsed
// lease, with onExpiry and escalate moving the status only through
// Workflow's transition and only from their from states; maxExpiries and
// resetExpiries; the longest hold; directives tied to the lease they were
// sent under; and the config rules. Real SQLite, a real engine, a clock the
// tests move.
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

import { Clock, alice, cleanup, drivers, jobFlow, jobsDocument, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const operator: Principal = { subject: 'opal', permissions: ['jobs.override'] };
const sender: Principal = { subject: 'sid', permissions: ['jobs.direct'] };

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

  const invoke = (engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}, id = 'j1') =>
    engine.instances.invoke(who, 'Job', id, operation, params);
  const leaseOf = (engine: Engine, id = 'j1') => engine.instances.get(alice, 'Job', id)?.data.lease as Record<string, unknown>;
  const statusOf = (engine: Engine, id = 'j1') => engine.instances.get(alice, 'Job', id)?.data.status;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Lease: acquire, heartbeat and release (${driver})`, () => {
    test('acquire takes the lease with token 1 and the default length and heartbeat; the lease field shows it', () => {
      const { engine } = world();
      assert.deepEqual(leaseOf(engine), { holder: null, token: 0, acquiredAt: null, expiresAt: null, active: false, expiries: 0 });
      assert.deepEqual(invoke(engine, worker, 'acquire'), { token: 1, expiresAt: T0 + 60000, heartbeatMs: 20000 });
      assert.deepEqual(leaseOf(engine), { holder: 'wren', token: 1, acquiredAt: T0, expiresAt: T0 + 60000, active: true, expiries: 0 });
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
      invoke(engine, worker, 'release', { token: 1 });
      assert.equal((invoke(engine, worker, 'acquire') as { token: number }).token, 3);
      clock.advance(30000);
      const seq = engine.instances.get(alice, 'Job', 'j1')?.seq as number;
      assert.deepEqual(invoke(engine, worker, 'heartbeat', { token: 3 }), { expiresAt: T0 + 90000, directives: [] });
      // Each heartbeat is a writing operation, with its event.
      assert.equal(engine.instances.get(alice, 'Job', 'j1')?.seq, seq + 1);
      // A process of the holder still on the lease it released is stale.
      assert.equal(veto(() => invoke(engine, worker, 'heartbeat', { token: 1 })).reason, 'token 1 is stale: the lease is at token 3');
      // The current token does not make another principal the holder.
      assert.equal(veto(() => invoke(engine, other, 'heartbeat', { token: 3 })).reason, 'another principal holds its lease');
      // Past its expiry time, though no one has applied the expiry yet, the lease cannot be renewed.
      clock.advance(60000);
      assert.match(veto(() => invoke(engine, worker, 'heartbeat', { token: 3 })).reason, new RegExp(`^its lease expired at ${iso(T0 + 90000)}`));
      assert.deepEqual(leaseOf(engine), { holder: 'wren', token: 3, acquiredAt: T0 + 0, expiresAt: T0 + 90000, active: false, expiries: 0 });
    });

    test('release ends the lease and advances the token, so the next acquire is two tokens on', () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      assert.deepEqual(invoke(engine, worker, 'release', { token: 1 }), {});
      assert.deepEqual(leaseOf(engine), { holder: null, token: 2, acquiredAt: null, expiresAt: null, active: false, expiries: 0 });
      assert.equal((invoke(engine, other, 'acquire') as { token: number }).token, 3);
    });

    test('only the holder releases, with its current token; a principal with overridePermission releases any lease', () => {
      const { engine } = world();
      invoke(engine, worker, 'acquire');
      assert.equal(veto(() => invoke(engine, other, 'release', { token: 1 })).reason, 'another principal holds its lease, and only the holder may release it');
      assert.deepEqual(thrown(() => invoke(engine, worker, 'release'), OperationParamsError).issues, [
        { path: '/token', message: 'the holder releases its lease with its token' },
      ]);
      assert.equal(veto(() => invoke(engine, worker, 'release', { token: 2 })).reason, 'token 2 is stale: the lease is at token 1');
      assert.equal(leaseOf(engine).holder, 'wren');

      const overridden = world({ overridePermission: 'jobs.override' }).engine;
      invoke(overridden, worker, 'acquire');
      const refused = thrown(() => invoke(overridden, other, 'release', { token: 1 }), EngineError);
      assert.equal(refused.code, 'forbidden');
      assert.equal(refused.message, "otto may not release another principal's lease of Job j1: it needs permission jobs.override");
      assert.deepEqual(invoke(overridden, operator, 'release'), {});
      assert.deepEqual(leaseOf(overridden), { holder: null, token: 2, acquiredAt: null, expiresAt: null, active: false, expiries: 0 });
    });

    test('acquire takes a lease shorter than the config, with a heartbeat scaled to it, and refuses a longer one', () => {
      const { engine, clock } = world();
      assert.deepEqual(invoke(engine, worker, 'acquire', { ttlMs: 30000 }), { token: 1, expiresAt: T0 + 30000, heartbeatMs: 10000 });
      clock.advance(20000);
      // Each heartbeat renews it by the length it was acquired with.
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }) as { expiresAt: number }).expiresAt, T0 + 50000);
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
      assert.equal(veto(() => invoke(engine, worker, 'release', { token: 1 })).reason, `its lease expired at ${iso(T0 + 60000)}; expire applies the expiry`);
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
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true });
      assert.deepEqual(leaseOf(engine), { holder: null, token: 2, acquiredAt: null, expiresAt: null, active: false, expiries: 1 });
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
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true });
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries, leaseOf(engine).holder], ['done', 0, null]);
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'j2'), { expired: true });
      assert.deepEqual([statusOf(engine, 'j2'), leaseOf(engine, 'j2').expiries], ['queued', 1]);
    });

    test('acquire over a lapsed lease applies its expiry first, then leases to the new principal', () => {
      const { engine, clock } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      clock.advance(60000);
      assert.deepEqual(invoke(engine, other, 'acquire'), { token: 3, expiresAt: T0 + 120000, heartbeatMs: 20000 });
      assert.deepEqual(leaseOf(engine), { holder: 'otto', token: 3, acquiredAt: T0 + 60000, expiresAt: T0 + 120000, active: true, expiries: 1 });
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
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true });
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
      assert.deepEqual(invoke(strict, { subject: 'sweeper', permissions: ['jobs.requeue'] }, 'expire'), { expired: true });
      assert.deepEqual([leaseOf(strict).holder, leaseOf(strict).expiries, statusOf(strict)], [null, 1, 'queued']);
    });

    test('a release moves unfinished work back with onExpiry and counts no expiry; finished work stays', () => {
      const { engine } = world(requeue);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'release', { token: 1 });
      assert.deepEqual([statusOf(engine), leaseOf(engine).expiries], ['queued', 0]);
      invoke(engine, worker, 'acquire');
      invoke(engine, worker, 'transition', { to: 'running' });
      invoke(engine, worker, 'transition', { to: 'done' });
      invoke(engine, worker, 'release', { token: 3 });
      assert.equal(statusOf(engine), 'done');
    });
  });

  describe(`Lease: the longest hold (${driver})`, () => {
    const limited = { ttlMs: 10000, heartbeatMs: 3000, maxHoldMs: 25000, maxHoldField: 'timeLimitMs' };

    test('heartbeats renew a lease up to its longest hold and no further, and it lapses there', () => {
      const { engine, clock } = world(limited);
      invoke(engine, worker, 'acquire');
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }) as { expiresAt: number }).expiresAt, T0 + 18000);
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }) as { expiresAt: number }).expiresAt, T0 + 25000);
      clock.advance(8000);
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }) as { expiresAt: number }).expiresAt, T0 + 25000);
      clock.advance(1000);
      assert.match(veto(() => invoke(engine, worker, 'heartbeat', { token: 1 })).reason, /^its lease expired at/);
      assert.deepEqual(invoke(engine, other, 'expire'), { expired: true });
    });

    test("the instance's maxHoldField overrides maxHoldMs; a value that is not a positive integer does not", () => {
      const { engine, clock } = world(limited);
      engine.instances.create(alice, 'Job', { title: 'Short', timeLimitMs: 12000 }, { id: 'short' });
      engine.instances.create(alice, 'Job', { title: 'Unset', timeLimitMs: 0 }, { id: 'zero' });
      assert.equal((invoke(engine, worker, 'acquire', {}, 'short') as { expiresAt: number }).expiresAt, T0 + 10000);
      invoke(engine, worker, 'acquire', {}, 'zero');
      clock.advance(9000);
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }, 'short') as { expiresAt: number }).expiresAt, T0 + 12000);
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }, 'zero') as { expiresAt: number }).expiresAt, T0 + 19000);
      clock.advance(3000);
      assert.equal(leaseOf(engine, 'short').active, false);
      assert.equal(leaseOf(engine, 'zero').active, true);
      // A lease past its longest hold is never held longer by a short acquire.
      assert.deepEqual(invoke(engine, other, 'expire', {}, 'short'), { expired: true });
    });

    test('the field that limits the hold cannot change while a lease is held, so a holder cannot extend its own', () => {
      const { engine } = world(limited);
      invoke(engine, worker, 'acquire');
      assert.equal(
        veto(() => engine.instances.update(worker, 'Job', 'j1', { timeLimitMs: 999999 })).reason,
        'timeLimitMs limits how long its lease may be held, so it cannot change while the lease is held'
      );
      engine.instances.update(worker, 'Job', 'j1', { title: 'Allowed' });
      invoke(engine, worker, 'release', { token: 1 });
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
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'cancel', data: { reason: 'superseded' } }), { id: 1 });
      clock.advance(10);
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'pause' }), { id: 2 });
      const both = [
        { id: 1, name: 'cancel', data: { reason: 'superseded' }, createdAt: T0, createdBy: 'sid' },
        { id: 2, name: 'pause', createdAt: T0 + 10, createdBy: 'sid' },
      ];
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { token: 1 }) as { directives: unknown }).directives, both);
      // Delivery is at least once: they come again until acknowledged.
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { token: 1 }) as { directives: unknown }).directives, both);
      assert.deepEqual(invoke(engine, worker, 'acknowledge', { token: 1, ids: [1] }), {});
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { token: 1 }) as { directives: unknown }).directives, [both[1]]);
      // Acknowledging one again changes nothing.
      assert.deepEqual(invoke(engine, worker, 'acknowledge', { token: 1, ids: [1] }), {});
    });

    test('only the holder acknowledges, with its current token, the directives sent under it', () => {
      const { engine } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      assert.equal(veto(() => invoke(engine, other, 'acknowledge', { token: 1, ids: [1] })).reason, 'another principal holds its lease');
      assert.equal(veto(() => invoke(engine, worker, 'acknowledge', { token: 2, ids: [1] })).reason, 'token 2 is stale: the lease is at token 1');
      assert.deepEqual(thrown(() => invoke(engine, worker, 'acknowledge', { token: 1, ids: [1, 3] }), OperationParamsError).issues, [
        { path: '/ids', message: 'no directive 3 was sent under token 1' },
      ]);
      // Nothing was acknowledged by the refused call.
      assert.equal((invoke(engine, worker, 'heartbeat', { token: 1 }) as { directives: unknown[] }).directives.length, 1);
    });

    test('the directives of a lease that ends never reach the next holder', () => {
      const { engine, clock } = world(directed);
      invoke(engine, worker, 'acquire');
      invoke(engine, sender, 'direct', { name: 'cancel' });
      invoke(engine, worker, 'release', { token: 1 });
      invoke(engine, other, 'acquire');
      assert.deepEqual((invoke(engine, other, 'heartbeat', { token: 3 }) as { directives: unknown }).directives, []);
      // Nor across an expiry; a directive's number counts within its lease.
      invoke(engine, sender, 'direct', { name: 'pause' });
      clock.advance(60000);
      invoke(engine, worker, 'acquire');
      assert.deepEqual((invoke(engine, worker, 'heartbeat', { token: 5 }) as { directives: unknown }).directives, []);
      assert.deepEqual(invoke(engine, sender, 'direct', { name: 'pause' }), { id: 1 });
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
      assert.deepEqual(invoke(overrideOnly, operator, 'direct', { name: 'cancel' }), { id: 1 });

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

  describe(`Lease: its config (${driver})`, () => {
    function refusal(engine: Engine, config: Record<string, unknown>, behaviors?: BehaviorRef[]): string {
      const document = jobsDocument(behaviors ?? [{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config }, { name: 'Comments' }]);
      return thrown(() => engine.schemas.define(alice, document), SchemaDocumentError).message;
    }

    test('parseConfig holds the config to the type: its heartbeat, its fields, its Workflow states and its behaviors', () => {
      const engine = openTestEngine({ driver });
      assert.match(refusal(engine, { heartbeatMs: 60000 }), /behavior Lease config: heartbeatMs \(60000\) is not less than ttlMs \(60000\): a lease would expire between heartbeats/);
      assert.match(refusal(engine, { ttlMs: 3000, heartbeatMs: 5000 }), /heartbeatMs \(5000\) is not less than ttlMs \(3000\)/);
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
      assert.match(refusal(engine, { maxHoldField: 'deadline' }), /maxHoldField "deadline" is not a field of Job \(its fields: title, priority, timeLimitMs\)/);
      assert.match(refusal(engine, { maxHoldField: 'title' }), /maxHoldField "title" is not an integer field of Job/);
      // The core meta-schema holds the shape first.
      assert.ok(
        thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Lease', config: { ttlMs: 500 } }])), SchemaDocumentError).issues.some(
          (issue) => issue.path === '/types/Job/behaviors/0/config/ttlMs'
        )
      );
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ...requeue, exempt: ['Comments.comment'], maxHoldField: 'timeLimitMs' } }, { name: 'Comments' }]));
    });

    test('it can be added to a schema that has instances, which start free, and cannot be removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }]));
      engine.instances.create(alice, 'Job', { title: 'Before' }, { id: 'j1' });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 30000 } }]));
      assert.deepEqual(leaseOf(engine), { holder: null, token: 0, acquiredAt: null, expiresAt: null, active: false, expiries: 0 });
      // Its config may change.
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 20000 } }]));
      const refused = thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }])), IncompatibleChangeError);
      assert.match(refused.message, /behavior Lease cannot be removed from type Job, which has instances: the leases and directives its instances hold would stay behind/);
    });
  });
}
