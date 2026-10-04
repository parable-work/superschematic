// Budget: reservations held against a meter's limit, here and up a chain
// of enclosing scopes that Links links point at; usage that is never
// refused, draws the instance's own reservation down and releases exactly
// that part at every scope; settlement when the lease a reservation was
// made under ends (release, expiry, an acquire over a lapsed lease) and
// when the instance is deleted; overruns reported and directed once per
// lease; limits only limitPermission changes; daily meters that a read
// never writes; scope operations that cannot free what an instance still
// holds; and the config rules. Real SQLite, a real engine, a clock the
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
  type Principal,
} from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, openTestEngine, publish, thrown, type BehaviorRef } from './helpers.ts';

afterEach(cleanup);

// No principal holds a permission that sends Lease's directives: Budget's
// overrun directive goes through call(), which Lease's guard lets through.
const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const operator: Principal = { subject: 'opal', permissions: ['budget.limit'] };
const lead: Principal = { subject: 'lena', permissions: ['budget.limit'] };
const sweeper: Principal = { subject: 'sweeper', permissions: [] };

const T0 = 1_000_000;
const DAY = 86_400_000;

const requeue = { onExpiry: { transition: 'queued', from: ['running'] } };
const lease: BehaviorRef = { name: 'Lease', config: requeue };

// budgetDocument is a General schema whose instance type, named like the
// schema, has a title and two integer fields, cap and estimate, a meter's
// limitField and reserveField, and composes the behaviors given.
function budgetDocument(name: string, behaviors: readonly BehaviorRef[]): Record<string, unknown> {
  return {
    kind: 'General',
    name,
    types: {
      [name]: {
        name,
        role: 'EmbeddedStruct',
        behaviors,
        fields: [
          { name: 'title', typeRef: { name: 'string' }, required: true },
          { name: 'cap', typeRef: { name: 'Generic.Int64' } },
          { name: 'estimate', typeRef: { name: 'Generic.Int64' } },
        ],
      },
    },
  };
}

interface Meter {
  used: number;
  reserved: number;
  limit: number | null;
  remaining: number | null;
}

for (const driver of drivers) {
  const invoke = (engine: Engine, who: Principal, schema: string, id: string, operation: string, params: Record<string, unknown> = {}) =>
    engine.instances.invoke(who, schema, id, operation, params);
  const meterOf = (engine: Engine, schema: string, id: string, meter = 'cpu') =>
    (engine.instances.get(alice, schema, id)?.data.budget as Record<string, Meter>)[meter];
  const seqOf = (engine: Engine, schema: string, id: string) => engine.instances.get(alice, schema, id)?.seq;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);
  const meter = (used: number, reserved: number, limit: number | null): Meter => ({
    used,
    reserved,
    limit,
    remaining: limit === null ? null : limit - used - reserved,
  });

  // single opens an engine whose Step composes Workflow, Lease and Budget
  // with the config given, with step s1 queued.
  function single(config: Record<string, unknown>, behaviors: BehaviorRef[] = [{ name: 'Workflow', config: jobFlow }, lease]) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now });
    publish(engine, budgetDocument('Step', [...behaviors, { name: 'Budget', config }]));
    engine.instances.create(alice, 'Step', { title: 'Build' }, { id: 's1' });
    return { engine, clock };
  }

  // chain opens an engine with three levels: Pool p1, Run r1 whose pool
  // link points at it, and Steps s1 and s2 whose run link points at r1,
  // each with a cpu meter of the config given; a run's and a step's meter
  // draw on their link.
  function chain(meters: { pool?: object; run?: object; step?: object; stepConfig?: object } = {}) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now });
    publish(engine, budgetDocument('Pool', [{ name: 'Budget', config: { meters: { cpu: meters.pool ?? { limit: 100 } }, limitPermission: 'budget.limit' } }]));
    publish(
      engine,
      budgetDocument('Run', [
        { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
        { name: 'Budget', config: { meters: { cpu: { scope: 'pool', ...(meters.run ?? {}) } } } },
      ])
    );
    publish(
      engine,
      budgetDocument('Step', [
        { name: 'Workflow', config: jobFlow },
        lease,
        { name: 'Links', config: { links: { run: { schema: 'Run' } } } },
        { name: 'Budget', config: { meters: { cpu: { scope: 'run', ...(meters.step ?? { reserve: 60 }) } }, ...(meters.stepConfig ?? {}) } },
      ])
    );
    engine.instances.create(alice, 'Pool', { title: 'Shared' }, { id: 'p1' });
    engine.instances.create(alice, 'Run', { title: 'Nightly' }, { id: 'r1' });
    invoke(engine, alice, 'Run', 'r1', 'link', { name: 'pool', id: 'p1' });
    for (const id of ['s1', 's2']) {
      engine.instances.create(alice, 'Step', { title: id }, { id });
      invoke(engine, alice, 'Step', id, 'link', { name: 'run', id: 'r1' });
    }
    return { engine, clock };
  }

  // claim takes a step's lease and reserves what the config gives, as
  // Queue's claim does; it returns the lease's token.
  function claim(engine: Engine, id = 's1', who: Principal = worker): number {
    const { token } = invoke(engine, who, 'Step', id, 'acquire') as { token: number };
    invoke(engine, who, 'Step', id, 'reserve');
    return token;
  }

  describe(`Budget: one instance (${driver})`, () => {
    test('a claim reserves, usage draws the reservation down, and the release settles the rest', () => {
      const { engine } = single({ meters: { cpu: { limit: 80, reserve: 60 } } });
      assert.deepEqual(meterOf(engine, 'Step', 's1'), meter(0, 0, 80));
      invoke(engine, worker, 'Step', 's1', 'acquire');
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'reserve'), { reserved: { cpu: 60 } });
      assert.deepEqual(meterOf(engine, 'Step', 's1'), { used: 0, reserved: 60, limit: 80, remaining: 20 });
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 25 }), {
        meter: 'cpu',
        used: 25,
        released: 25,
        overruns: [],
        directed: false,
      });
      assert.deepEqual(meterOf(engine, 'Step', 's1'), { used: 25, reserved: 35, limit: 80, remaining: 20 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual(meterOf(engine, 'Step', 's1'), { used: 25, reserved: 0, limit: 80, remaining: 55 });
    });

    test('a reservation that does not fit is refused and changes nothing; one with a meter takes an amount', () => {
      const { engine } = single({ meters: { cpu: { limit: 80, reserve: 60 }, calls: {} } });
      claim(engine);
      const seq = seqOf(engine, 'Step', 's1');
      const refused = veto(() => invoke(engine, worker, 'Step', 's1', 'reserve', { meter: 'cpu', amount: 30 }));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], ['Budget', 'reserve', 'meter cpu has 20 of its limit 80 left, not 30']);
      assert.deepEqual(meterOf(engine, 'Step', 's1'), meter(0, 60, 80));
      assert.equal(seqOf(engine, 'Step', 's1'), seq);
      // A meter with no limit takes any amount; reserve with no meter takes only the configured reservations.
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'reserve', { meter: 'calls', amount: 1000 }), { reserved: { calls: 1000 } });
      assert.deepEqual(meterOf(engine, 'Step', 's1', 'calls'), meter(0, 1000, null));
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'reserve', { meter: 'cpu', amount: 20 }), { reserved: { cpu: 20 } });
      assert.deepEqual(thrown(() => invoke(engine, worker, 'Step', 's1', 'reserve', { meter: 'calls' }), OperationParamsError).issues, [
        { path: '/amount', message: 'meter calls has no configured reservation, so reserve takes an amount' },
      ]);
      assert.deepEqual(thrown(() => invoke(engine, worker, 'Step', 's1', 'reserve', { amount: 5 }), OperationParamsError).issues, [
        { path: '/amount', message: 'an amount is reserved of one meter, which meter names' },
      ]);
      assert.deepEqual(thrown(() => invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'gpu', amount: 5 }), OperationParamsError).issues, [
        { path: '/meter', message: 'Step has no meter gpu (its meters: calls, cpu)' },
      ]);
    });

    test('on a type that composes Lease, reserve needs an active lease, and only its holder reserves', () => {
      const { engine, clock } = single({ meters: { cpu: { limit: 80, reserve: 60 } } });
      const reason = 'no lease is active, and on a type that composes Lease a reservation is made under the active lease';
      assert.equal(veto(() => invoke(engine, worker, 'Step', 's1', 'reserve')).reason, reason);
      invoke(engine, worker, 'Step', 's1', 'acquire');
      assert.equal(veto(() => invoke(engine, other, 'Step', 's1', 'reserve')).behavior, 'Lease');
      clock.advance(60000);
      assert.equal(veto(() => invoke(engine, other, 'Step', 's1', 'reserve')).reason, reason);
      // Without Lease on the type, a reservation needs no lease and lasts until it is settled.
      const plain = single({ meters: { cpu: { limit: 80, reserve: 60 } } }, []).engine;
      assert.deepEqual(invoke(plain, other, 'Step', 's1', 'reserve'), { reserved: { cpu: 60 } });
      assert.deepEqual(invoke(plain, other, 'Step', 's1', 'settle'), { released: { cpu: 60 } });
      assert.deepEqual(meterOf(plain, 'Step', 's1'), meter(0, 0, 80));
    });

    test('usage is never refused: past the limit it is recorded and reported, and the holder gets one directive per lease', () => {
      const { engine } = single({ meters: { cpu: { limit: 80, reserve: 60 } }, onExceeded: { direct: 'meterExceeded' } });
      claim(engine);
      const over = { schema: 'Step', id: 's1', used: 100, limit: 80 };
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 100 }), {
        meter: 'cpu',
        used: 100,
        released: 60,
        overruns: [over],
        directed: true,
      });
      assert.deepEqual(meterOf(engine, 'Step', 's1'), { used: 100, reserved: 0, limit: 80, remaining: -20 });
      const directives = (engine.instances.invoke(worker, 'Step', 's1', 'heartbeat', {}, fenced(1)) as { directives: unknown[] }).directives;
      assert.deepEqual(directives, [
        { id: 1, name: 'meterExceeded', data: { meter: 'cpu', used: 100, limit: 80, scope: { schema: 'Step', id: 's1' } }, createdAt: T0, createdBy: 'wren' },
      ]);
      // The worker could not send it itself; Budget's call() needs no permission.
      assert.equal(
        veto(() => invoke(engine, worker, 'Step', 's1', 'direct', { name: 'meterExceeded' })).reason,
        'its config names no permission that sends directives (directPermission or overridePermission)'
      );
      // Once per meter per lease: the next overrun under the same token sends none.
      assert.equal((invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 5 }) as { directed: boolean }).directed, false);
      assert.equal((engine.instances.invoke(worker, 'Step', 's1', 'heartbeat', {}, fenced(1)) as { directives: unknown[] }).directives.length, 1);
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      // A caller without a lease learns of the overrun from the result alone.
      assert.deepEqual(invoke(engine, other, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 1 }), {
        meter: 'cpu',
        used: 106,
        released: 0,
        overruns: [{ ...over, used: 106 }],
        directed: false,
      });
      // A new lease is a new token, which gets its own directive.
      invoke(engine, worker, 'Step', 's1', 'acquire');
      assert.equal((invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 1 }) as { directed: boolean }).directed, true);
      assert.deepEqual((engine.instances.invoke(worker, 'Step', 's1', 'heartbeat', {}, fenced(3)) as { directives: Array<{ data: unknown }> }).directives[0].data, {
        meter: 'cpu',
        used: 107,
        limit: 80,
        scope: { schema: 'Step', id: 's1' },
      });
    });

    test('settle releases the own reservation of one meter or of every meter', () => {
      const { engine } = single({ meters: { cpu: { limit: 80, reserve: 60 }, calls: { reserve: 3 } } });
      claim(engine);
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'settle', { meter: 'calls' }), { released: { calls: 3 } });
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'settle'), { released: { cpu: 60, calls: 0 } });
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Step', 's1', 'calls')], [meter(0, 0, 80), meter(0, 0, null)]);
    });
  });

  describe(`Budget: settlement when a lease ends (${driver})`, () => {
    test('a release settles the reservation here and in every enclosing scope', () => {
      const { engine } = chain();
      claim(engine);
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(0, 60, null), meter(0, 60, 100)]);
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 0, null), meter(0, 0, 100)]);
    });

    test("an expiry settles it, applied by Lease's expire on the engine's clock; an expire that finds the lease active settles nothing", () => {
      const { engine, clock } = chain();
      claim(engine);
      assert.deepEqual(invoke(engine, sweeper, 'Step', 's1', 'expire'), { expired: false });
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 60, 100));
      clock.advance(60000);
      assert.deepEqual(invoke(engine, sweeper, 'Step', 's1', 'expire'), { expired: true, reason: 'ttl' });
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 0, null), meter(0, 0, 100)]);
    });

    test('an acquire over a lapsed lease settles the reservation of the lease it expires', () => {
      const { engine, clock } = chain();
      claim(engine);
      clock.advance(60000);
      assert.equal((invoke(engine, other, 'Step', 's1', 'acquire') as { token: number }).token, 3);
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 0, 100)]);
      // The new holder's own reservation is made under its token, and lasts.
      invoke(engine, other, 'Step', 's1', 'reserve');
      engine.instances.invoke(other, 'Step', 's1', 'heartbeat', {}, fenced(3));
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 60, 100));
    });

    test('deleting an instance settles everything it reserved, up the chain', () => {
      const { engine } = chain({ pool: { limit: 200 } });
      claim(engine);
      claim(engine, 's2');
      assert.equal(engine.instances.delete(worker, 'Step', 's1'), true);
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(0, 60, 200)]);
      assert.equal(engine.storage.get("SELECT COUNT(*) AS n FROM bhv_budget__meters WHERE id = 's1'")?.n, 0);
      assert.equal(engine.storage.get("SELECT COUNT(*) AS n FROM bhv_budget__holds WHERE inner_id = 's1'")?.n, 0);
    });
  });

  describe(`Budget: enclosing scopes (${driver})`, () => {
    test("a scope that cannot fit a reservation refuses it, and the refusal leaves nothing anywhere in the chain", () => {
      const { engine } = chain();
      claim(engine);
      invoke(engine, worker, 'Step', 's2', 'acquire');
      const seqs = () => [seqOf(engine, 'Step', 's2'), seqOf(engine, 'Run', 'r1'), seqOf(engine, 'Pool', 'p1')];
      const before = seqs();
      const refused = veto(() => invoke(engine, worker, 'Step', 's2', 'reserve'));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], ['Budget', 'reserveFor', 'meter cpu has 40 of its limit 100 left, not 60']);
      assert.match(refused.message, /of Pool p1:/);
      assert.deepEqual([meterOf(engine, 'Step', 's2'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 60, null), meter(0, 60, 100)]);
      assert.deepEqual(seqs(), before);
      assert.equal(engine.storage.get("SELECT COUNT(*) AS n FROM bhv_budget__holds WHERE inner_id = 's2'")?.n, 0);
      // Once the first claim's lease ends, the second fits.
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual(invoke(engine, worker, 'Step', 's2', 'reserve'), { reserved: { cpu: 60 } });
    });

    test('a reservation reaches the outermost scope through every level, each with its own check and its own event', () => {
      const { engine } = chain({ run: { limit: 150 } });
      claim(engine);
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(0, 60, 150), meter(0, 60, 100)]);
      for (const [schema, id, holds] of [
        ['Run', 'r1', { schema: 'Step', id: 's1' }],
        ['Pool', 'p1', { schema: 'Run', id: 'r1' }],
      ] as const) {
        const last = engine.events.read(alice, { schema, instanceId: id }).events.at(-1);
        assert.equal(last?.actor, 'wren');
        assert.deepEqual((last?.change as { operation: string; params: unknown }).operation, 'reserveFor');
        assert.deepEqual((last?.change as { params: unknown }).params, { meter: 'cpu', ...holds, amount: 60 });
      }
      // The pool, held through one run, refuses a claim through another of its runs.
      engine.instances.create(alice, 'Run', { title: 'Weekly' }, { id: 'r2' });
      invoke(engine, alice, 'Run', 'r2', 'link', { name: 'pool', id: 'p1' });
      engine.instances.create(alice, 'Step', { title: 's3' }, { id: 's3' });
      invoke(engine, alice, 'Step', 's3', 'link', { name: 'run', id: 'r2' });
      invoke(engine, other, 'Step', 's3', 'acquire');
      assert.match(veto(() => invoke(engine, other, 'Step', 's3', 'reserve')).message, /of Pool p1: meter cpu has 40 of its limit 100 left, not 60$/);
      assert.deepEqual(meterOf(engine, 'Run', 'r2'), meter(0, 0, 150));
      // A run's own limit refuses before the pool is asked.
      const tight = chain({ run: { limit: 50 } }).engine;
      invoke(tight, worker, 'Step', 's1', 'acquire');
      assert.match(veto(() => invoke(tight, worker, 'Step', 's1', 'reserve')).message, /of Run r1: meter cpu has 50 of its limit 50 left, not 60$/);
    });

    test("usage beyond a reservation releases only the reservation's part at each scope, so other instances' reservations stay held", () => {
      const { engine } = chain({ step: { reserve: 30 } });
      claim(engine, 's1');
      claim(engine, 's2', other);
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 60, 100));
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 50 }), {
        meter: 'cpu',
        used: 50,
        released: 30,
        overruns: [],
        directed: false,
      });
      // s2's 30 is still held at the run and the pool.
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(50, 0, null), meter(50, 30, null), meter(50, 30, 100)]);
      assert.equal(veto(() => invoke(engine, other, 'Step', 's2', 'reserve', { meter: 'cpu', amount: 21 })).reason, 'meter cpu has 20 of its limit 100 left, not 21');
    });

    test('usage rolls up and stays when the reservation is settled', () => {
      const { engine } = chain();
      claim(engine);
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 30 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(30, 0, null), meter(30, 0, null), meter(30, 0, 100)]);
    });

    test("each instance's reserveField reserves its own amount, and the scopes add them up", () => {
      const { engine } = chain({ pool: { limit: 200 }, step: { reserveField: 'estimate' } });
      engine.instances.update(alice, 'Step', 's1', { estimate: 30 });
      engine.instances.update(alice, 'Step', 's2', { estimate: 80 });
      claim(engine, 's1');
      claim(engine, 's2', other);
      assert.deepEqual(meterOf(engine, 'Run', 'r1'), meter(0, 110, null));
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 10 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(10, 80, null), meter(10, 80, 200)]);
      // An instance without a positive estimate reserves nothing of the meter.
      engine.instances.create(alice, 'Step', { title: 'Unsized' }, { id: 's3' });
      invoke(engine, worker, 'Step', 's3', 'acquire');
      assert.deepEqual(invoke(engine, worker, 'Step', 's3', 'reserve'), { reserved: {} });
    });

    test("usage that takes a scope over its limit is reported, and directs the holder of the instance's lease", () => {
      const { engine } = chain({ stepConfig: { onExceeded: { direct: 'meterExceeded' } } });
      claim(engine);
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 120 }), {
        meter: 'cpu',
        used: 120,
        released: 60,
        overruns: [{ schema: 'Pool', id: 'p1', used: 120, limit: 100 }],
        directed: true,
      });
      const directives = (engine.instances.invoke(worker, 'Step', 's1', 'heartbeat', {}, fenced(1)) as { directives: Array<{ name: string; data: unknown }> }).directives;
      assert.deepEqual(
        directives.map((one) => [one.name, one.data]),
        [['meterExceeded', { meter: 'cpu', used: 120, limit: 100, scope: { schema: 'Pool', id: 'p1' } }]]
      );
    });

    test('the scope operations cannot free what an instance still holds, nor hold more than it reserved', () => {
      const { engine } = chain();
      claim(engine);
      // Anyone who may write the scope can call them; they release nothing the step still holds.
      assert.deepEqual(invoke(engine, other, 'Run', 'r1', 'settleFor', { meter: 'cpu', schema: 'Step', id: 's1', amount: 60 }), { released: 0 });
      assert.deepEqual(invoke(engine, other, 'Pool', 'p1', 'settleFor', { meter: 'cpu', schema: 'Run', id: 'r1', amount: 60 }), { released: 0 });
      assert.deepEqual(
        invoke(engine, other, 'Pool', 'p1', 'recordUsageFor', { meter: 'cpu', schema: 'Run', id: 'r1', amount: 1, released: 60 }),
        { released: 0, overruns: [] }
      );
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(1, 60, 100)]);
      assert.equal(
        veto(() => invoke(engine, other, 'Pool', 'p1', 'reserveFor', { meter: 'cpu', schema: 'Run', id: 'r1', amount: 10 })).reason,
        'Run r1 has 60 of meter cpu reserved and 60 of it is held here, so 10 more is not its to hold'
      );
      // An instance that does not draw the meter from the scope is no one the scope holds for.
      assert.deepEqual(
        thrown(() => invoke(engine, other, 'Pool', 'p1', 'reserveFor', { meter: 'cpu', schema: 'Step', id: 's1', amount: 10 }), OperationParamsError).issues,
        [{ path: '/id', message: 'Step s1 does not draw meter cpu from Pool p1' }]
      );
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(1, 60, 100));
    });

    test('a scope link does not move while a reservation is held through it', () => {
      const { engine } = chain();
      engine.instances.create(alice, 'Run', { title: 'Other' }, { id: 'r2' });
      claim(engine);
      const reason = 'meter cpu has 60 reserved through link run; settle it before the link changes';
      assert.equal(veto(() => invoke(engine, worker, 'Step', 's1', 'link', { name: 'run', id: 'r2' })).reason, reason);
      assert.equal(veto(() => invoke(engine, worker, 'Step', 's1', 'unlink', { name: 'run' })).reason, reason);
      // A scope holding for an instance is held to it too.
      assert.equal(veto(() => invoke(engine, alice, 'Run', 'r1', 'unlink', { name: 'pool' })).reason, 'meter cpu has 60 reserved through link pool; settle it before the link changes');
      invoke(engine, worker, 'Step', 's1', 'settle');
      invoke(engine, worker, 'Step', 's1', 'link', { name: 'run', id: 'r2' });
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 7 });
      assert.deepEqual([meterOf(engine, 'Run', 'r1').used, meterOf(engine, 'Run', 'r2').used], [0, 7]);
    });
  });

  // queued opens an engine with Pool p1 (cpu limit 100), Runs r1 and r2
  // under it, whose cap is their own limit, and Steps that compose
  // Workflow, Lease, Links, Queue and Budget, with each step of steps
  // ([id, run]) linked to its run, and a runner whose schedules have had
  // their first pass.
  function queued(steps: Array<[string, string]>, options: { stepMeters?: object; caps?: Record<string, number> } = {}) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: sweeper } });
    publish(engine, budgetDocument('Pool', [{ name: 'Budget', config: { meters: { cpu: { limit: 100 } } } }]));
    publish(
      engine,
      budgetDocument('Run', [
        { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
        { name: 'Budget', config: { meters: { cpu: { scope: 'pool', limitField: 'cap' } } } },
      ])
    );
    publish(
      engine,
      budgetDocument('Step', [
        { name: 'Workflow', config: jobFlow },
        lease,
        { name: 'Links', config: { links: { run: { schema: 'Run' } } } },
        { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' } } },
        { name: 'Budget', config: { meters: options.stepMeters ?? { cpu: { scope: 'run', reserve: 60 } } } },
      ])
    );
    engine.instances.create(alice, 'Pool', { title: 'Shared' }, { id: 'p1' });
    for (const run of ['r1', 'r2']) {
      const cap = options.caps?.[run];
      engine.instances.create(alice, 'Run', { title: run, ...(cap === undefined ? {} : { cap }) }, { id: run });
      invoke(engine, alice, 'Run', run, 'link', { name: 'pool', id: 'p1' });
    }
    for (const [id, run] of steps) {
      engine.instances.create(alice, 'Step', { title: id }, { id });
      invoke(engine, alice, 'Step', id, 'link', { name: 'run', id: run });
    }
    engine.runner.runDue();
    return { engine, clock };
  }

  const claimNext = (engine: Engine, who: Principal = worker) =>
    (engine.instances.invokeSchema(who, 'Step', 'claimNext', {}) as { claimed: { id: string; token: number } | null }).claimed;
  const stepOf = (engine: Engine, id: string) => {
    const data = engine.instances.get(alice, 'Step', id)?.data as { status: string; lease: { holder: string | null; token: number } };
    return [data.status, data.lease.holder, data.lease.token];
  };

  describe(`Budget: claimed through Queue (${driver})`, () => {
    test("a claim reserves every meter's claim amount, here and in every scope", () => {
      const { engine } = queued([['s1', 'r1']], { stepMeters: { cpu: { scope: 'run', reserve: 60 }, calls: { reserve: 3 }, gpu: { limit: 10 } } });
      assert.deepEqual(invoke(engine, worker, 'Step', 's1', 'claim'), { id: 's1', token: 1, expiresAt: T0 + 60000, heartbeatMs: 20000 });
      assert.deepEqual(stepOf(engine, 's1'), ['running', 'wren', 1]);
      assert.deepEqual(
        [meterOf(engine, 'Step', 's1'), meterOf(engine, 'Step', 's1', 'calls'), meterOf(engine, 'Step', 's1', 'gpu')],
        [meter(0, 60, null), meter(0, 3, null), meter(0, 0, 10)]
      );
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(0, 60, 100)]);
    });

    test('a reservation that does not fit refuses the claim, which leaves no lease, no reservation and the status as it was', () => {
      const { engine } = queued([
        ['s1', 'r1'],
        ['s2', 'r2'],
      ]);
      invoke(engine, worker, 'Step', 's1', 'claim');
      const seq = seqOf(engine, 'Step', 's2');
      const refused = veto(() => invoke(engine, other, 'Step', 's2', 'claim'));
      assert.deepEqual([refused.action, refused.reason], ['reserveFor', 'meter cpu has 40 of its limit 100 left, not 60']);
      assert.match(refused.message, /of Pool p1:/);
      assert.deepEqual(stepOf(engine, 's2'), ['queued', null, 0]);
      assert.equal(seqOf(engine, 'Step', 's2'), seq);
      assert.deepEqual([meterOf(engine, 'Step', 's2'), meterOf(engine, 'Run', 'r2'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 0, null), meter(0, 60, 100)]);
    });

    test('claimNext passes over a candidate whose budget does not fit and claims the next', () => {
      const { engine } = queued(
        [
          ['s1', 'r1'],
          ['s2', 'r2'],
        ],
        { caps: { r1: 50 } }
      );
      assert.equal(claimNext(engine)?.id, 's2');
      assert.deepEqual([stepOf(engine, 's1'), stepOf(engine, 's2')], [['queued', null, 0], ['running', 'wren', 1]]);
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Run', 'r2')], [meter(0, 0, 50), meter(0, 60, null)]);
      assert.equal(claimNext(engine, other), null);
    });

    test("a release and the runner's expiry settle the claim's reservation, and Queue's copies follow, so it is claimed again", () => {
      const { engine, clock } = queued([['s1', 'r1']]);
      assert.equal(claimNext(engine)?.token, 1);
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 10 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual(stepOf(engine, 's1'), ['queued', null, 2]);
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Pool', 'p1')], [meter(10, 0, null), meter(10, 0, 100)]);

      assert.equal(claimNext(engine)?.token, 3);
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(10, 60, 100));
      clock.advance(60000);
      engine.runner.runDue();
      assert.deepEqual(stepOf(engine, 's1'), ['queued', null, 4]);
      assert.deepEqual([meterOf(engine, 'Step', 's1'), meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(10, 0, null), meter(10, 0, null), meter(10, 0, 100)]);
      assert.deepEqual(claimNext(engine, other), { id: 's1', token: 5, expiresAt: T0 + 120000, heartbeatMs: 20000 });
    });

    test('through three levels, claimNext claims what the pool can fit, and what a finished claim used stays counted', () => {
      const { engine } = queued([
        ['s1', 'r1'],
        ['s2', 'r2'],
        ['s3', 'r1'],
      ]);
      assert.equal(claimNext(engine)?.id, 's1');
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 60, null), meter(0, 60, 100)]);
      // s2 through r2 and s3 through r1 both find the pool short.
      assert.equal(claimNext(engine, other), null);
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 30 });
      invoke(engine, worker, 'Step', 's1', 'transition', { to: 'done' });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(30, 0, 100));
      assert.equal(claimNext(engine, other)?.id, 's2');
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Run', 'r2'), meterOf(engine, 'Pool', 'p1')], [meter(30, 0, null), meter(0, 60, null), meter(30, 60, 100)]);
      assert.equal(claimNext(engine), null);
      assert.deepEqual(stepOf(engine, 's3'), ['queued', null, 0]);
    });
  });

  describe(`Budget: a scope a new version moves (${driver})`, () => {
    test('what is held at the old scope is released there, and a reservation through the new one waits until it is settled', () => {
      const { engine } = chain();
      const step = (scope: string) =>
        budgetDocument('Step', [
          { name: 'Workflow', config: jobFlow },
          lease,
          { name: 'Links', config: { links: { run: { schema: 'Run' }, alt: { schema: 'Run' } } } },
          { name: 'Budget', config: { meters: { cpu: { scope, reserve: 60 } } } },
        ]);
      publish(engine, step('run'));
      engine.instances.create(alice, 'Run', { title: 'Other' }, { id: 'r2' });
      invoke(engine, alice, 'Step', 's1', 'link', { name: 'alt', id: 'r2' });
      claim(engine);
      publish(engine, step('alt'));
      assert.equal(
        veto(() => invoke(engine, worker, 'Step', 's1', 'reserve', { meter: 'cpu', amount: 1 })).reason,
        'meter cpu has 60 reserved through Run r1; settle it before reserving through Run r2'
      );
      // The usage counts at the new scope; the part it draws is released at the old one, which holds it.
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 10 });
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Run', 'r2'), meterOf(engine, 'Pool', 'p1')], [meter(0, 50, null), meter(10, 0, null), meter(0, 50, 100)]);
      invoke(engine, worker, 'Step', 's1', 'settle');
      assert.deepEqual([meterOf(engine, 'Run', 'r1'), meterOf(engine, 'Pool', 'p1')], [meter(0, 0, null), meter(0, 0, 100)]);
      invoke(engine, worker, 'Step', 's1', 'reserve');
      assert.deepEqual(meterOf(engine, 'Run', 'r2'), meter(10, 60, null));
    });
  });

  describe(`Budget: limits (${driver})`, () => {
    test('setLimit needs limitPermission, raises or lowers the limit, never below what is used and reserved, and is refused when the config names none', () => {
      const { engine } = single({ meters: { cpu: { limit: 80, reserve: 60 } }, limitPermission: 'budget.limit' });
      claim(engine);
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 10 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      const refused = thrown(() => invoke(engine, other, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 200 }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'otto may not change a limit of Step s1: it needs permission budget.limit']);
      assert.equal(veto(() => invoke(engine, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 9 })).reason, 'meter cpu has 10 used and reserved, more than the limit 9');
      // It lowers a limit too, down to what is committed.
      assert.deepEqual(invoke(engine, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 10 }), { meter: 'cpu', limit: 10, previous: 80 });
      assert.deepEqual(invoke(engine, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 200 }), { meter: 'cpu', limit: 200, previous: 10 });
      // The next reservation fits the new limit; while it is held, the limit does not drop below it.
      claim(engine, 's1', lead);
      assert.deepEqual(meterOf(engine, 'Step', 's1'), meter(10, 60, 200));
      assert.equal(
        veto(() => invoke(engine, lead, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 69 })).reason,
        'meter cpu has 70 used and reserved, more than the limit 69'
      );
      // While a worker holds the lease, the operator's change goes through when Lease exempts it.
      assert.equal(veto(() => invoke(engine, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 70 })).behavior, 'Lease');
      const exempt = single(
        { meters: { cpu: { limit: 80, reserve: 60 } }, limitPermission: 'budget.limit' },
        [{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { exempt: ['Budget.setLimit'] } }]
      ).engine;
      claim(exempt);
      assert.deepEqual(invoke(exempt, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 60 }), { meter: 'cpu', limit: 60, previous: 80 });
      assert.equal(veto(() => invoke(exempt, worker, 'Step', 's1', 'reserve', { meter: 'cpu', amount: 1 })).reason, 'meter cpu has 0 of its limit 60 left, not 1');

      const unset = single({ meters: { cpu: { limit: 80 } } }).engine;
      assert.equal(
        veto(() => invoke(unset, operator, 'Step', 's1', 'setLimit', { meter: 'cpu', limit: 100 })).reason,
        'its config names no limitPermission, which changing a limit needs'
      );
    });

    test("limitField holds the instance's limit, which only limitPermission changes, setLimit through update() with its checks", () => {
      const { engine } = single({ meters: { cpu: { limitField: 'cap', reserve: 60 } }, limitPermission: 'budget.limit' });
      assert.deepEqual(meterOf(engine, 'Step', 's1'), meter(0, 0, null));
      engine.instances.create(alice, 'Step', { title: 'Capped', cap: 80 }, { id: 's2' });
      assert.deepEqual(meterOf(engine, 'Step', 's2'), meter(0, 0, 80));
      const refused = thrown(() => engine.instances.update(other, 'Step', 's2', { cap: 1000 }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'otto may not change cap, the limit of meter cpu, of Step s2: it needs permission budget.limit']);
      engine.instances.update(operator, 'Step', 's2', { cap: 100 });
      assert.deepEqual(meterOf(engine, 'Step', 's2'), meter(0, 0, 100));
      assert.deepEqual(invoke(engine, operator, 'Step', 's2', 'setLimit', { meter: 'cpu', limit: 120 }), { meter: 'cpu', limit: 120, previous: 100 });
      assert.equal(engine.instances.get(alice, 'Step', 's2')?.data.cap, 120);
      claim(engine, 's2', lead);
      invoke(engine, lead, 'Step', 's2', 'recordUsage', { meter: 'cpu', amount: 10 });
      assert.equal(
        veto(() => engine.instances.update(lead, 'Step', 's2', { cap: 50 })).reason,
        'meter cpu has 60 used and reserved, more than the limit 50'
      );

      const unset = single({ meters: { cpu: { limitField: 'cap' } } }).engine;
      assert.equal(
        veto(() => unset.instances.update(alice, 'Step', 's1', { cap: 10 })).reason,
        'cap holds the limit of meter cpu, which changes only with limitPermission, and the config names none'
      );
      unset.instances.update(alice, 'Step', 's1', { title: 'Renamed' });
    });
  });

  describe(`Budget: daily meters (${driver})`, () => {
    test("a daily meter's usage starts again each UTC day; a read shows it and writes nothing, and the next write starts the day", () => {
      const { engine, clock } = chain({ pool: { limit: 100, reset: 'daily' } });
      const stored = () => engine.storage.get("SELECT used, period_start FROM bhv_budget__meters WHERE schema = 'Pool' AND id = 'p1'");
      claim(engine);
      invoke(engine, worker, 'Step', 's1', 'recordUsage', { meter: 'cpu', amount: 90 });
      engine.instances.invoke(worker, 'Step', 's1', 'release', {}, fenced(1));
      invoke(engine, other, 'Step', 's2', 'acquire');
      assert.equal(veto(() => invoke(engine, other, 'Step', 's2', 'reserve')).reason, 'meter cpu has 10 of its limit 100 left, not 60');
      assert.deepEqual(stored(), { used: 90, period_start: 0 });

      clock.ms = DAY + 5000;
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 0, 100));
      // The read wrote nothing: back on the first day the pool still shows its usage.
      assert.deepEqual(stored(), { used: 90, period_start: 0 });
      clock.ms = DAY - 1;
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(90, 0, 100));

      clock.ms = DAY + 5000;
      claim(engine, 's2', other);
      assert.deepEqual(stored(), { used: 0, period_start: DAY });
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 60, 100));
      // Reservations are kept across a day.
      clock.ms = 2 * DAY;
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 60, 100));
    });
  });

  describe(`Budget: its config (${driver})`, () => {
    function refusal(engine: Engine, name: string, behaviors: BehaviorRef[]): string {
      return thrown(() => engine.schemas.define(alice, budgetDocument(name, behaviors)), SchemaDocumentError).message;
    }

    test('parseConfig holds the config to the type: its integer fields, its Links links, and the schema each scope points at', () => {
      const engine = openTestEngine({ driver });
      const links = (schema: string): BehaviorRef => ({ name: 'Links', config: { links: { pool: { schema } } } });
      const budgetOn = (cpu: object, rest: object = {}): BehaviorRef => ({ name: 'Budget', config: { meters: { cpu }, ...rest } });
      assert.match(refusal(engine, 'Run', [budgetOn({ limitField: 'title' })]), /behavior Budget config: meter cpu: limitField "title" is not an integer field of Run/);
      assert.match(refusal(engine, 'Run', [budgetOn({ reserveField: 'size' })]), /meter cpu: reserveField "size" is not a field of Run \(its fields: title, cap, estimate\)/);
      assert.match(refusal(engine, 'Run', [budgetOn({ scope: 'pool' })]), /meter cpu: scope pool is a link of Links, which the type does not list/);
      assert.match(refusal(engine, 'Run', [links('Pool'), budgetOn({ scope: 'parent' })]), /meter cpu: scope parent is not a link of the type's Links config \(its links: pool\)/);
      assert.match(refusal(engine, 'Run', [links('Pool'), budgetOn({ scope: 'pool' })]), /meter cpu: scope pool: schema Pool, which the link points at, has no live version; publish it first/);
      publish(engine, budgetDocument('Pool', [{ name: 'Comments' }]));
      assert.match(refusal(engine, 'Run', [links('Pool'), budgetOn({ scope: 'pool' })]), /scope pool: Pool, which the link points at, does not compose Budget/);
      publish(engine, budgetDocument('Pool', [{ name: 'Comments' }, { name: 'Budget', config: { meters: { gpu: { limit: 10 } } } }]));
      assert.match(refusal(engine, 'Run', [links('Pool'), budgetOn({ scope: 'pool' })]), /scope pool: Pool, which the link points at, has no meter cpu \(its meters: gpu\)/);
      assert.match(refusal(engine, 'Run', [budgetOn({}, { onExceeded: { direct: 'stop' } })]), /onExceeded sends a directive through Lease's direct, which the type does not list/);
      // The core meta-schema holds the shape first.
      assert.ok(
        thrown(() => engine.schemas.define(alice, budgetDocument('Run', [budgetOn({ limit: 10, limitField: 'cap' })])), SchemaDocumentError).issues.some(
          (issue) => issue.path === '/types/Run/behaviors/0/config/meters/cpu'
        )
      );
      // A scope on the type's own schema: a run nested in a run.
      publish(engine, budgetDocument('Run', [{ name: 'Links', config: { links: { pool: { schema: 'Run' } } } }, budgetOn({ limit: 10, scope: 'pool' })]));
    });

    test('it can be added to a schema that has instances, keeps its meters, and cannot be removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, budgetDocument('Pool', [{ name: 'Comments' }]));
      engine.instances.create(alice, 'Pool', { title: 'Before' }, { id: 'p1' });
      publish(engine, budgetDocument('Pool', [{ name: 'Comments' }, { name: 'Budget', config: { meters: { cpu: { limit: 100 } } } }]));
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 0, 100));
      invoke(engine, alice, 'Pool', 'p1', 'reserve', { meter: 'cpu', amount: 40 });
      // A limit may change, and a meter may be added; a meter may not go.
      publish(engine, budgetDocument('Pool', [{ name: 'Comments' }, { name: 'Budget', config: { meters: { cpu: { limit: 120 }, gpu: {} } } }]));
      assert.deepEqual(meterOf(engine, 'Pool', 'p1'), meter(0, 40, 120));
      const dropped = thrown(
        () => engine.schemas.define(alice, budgetDocument('Pool', [{ name: 'Comments' }, { name: 'Budget', config: { meters: { cpu: { limit: 120 } } } }])),
        IncompatibleChangeError
      );
      assert.match(dropped.message, /meter gpu is gone, and its instances and the scopes they draw on may hold it/);
      const removed = thrown(() => engine.schemas.define(alice, budgetDocument('Pool', [{ name: 'Comments' }])), IncompatibleChangeError);
      assert.match(removed.message, /behavior Budget cannot be removed from type Pool, which has instances: the budgets its instances hold, and what enclosing scopes hold for them, would stay behind/);
    });
  });
}
