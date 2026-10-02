// The runner (D16, amended): reactions run after the commit, from the log,
// as the runner's principal, one subscription's events in log order, with a
// cursor that commits with the reaction's writes, retries, a halt, a depth
// limit, and schedules on the engine's clock.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, test } from 'node:test';

import {
  EngineError,
  allowAll,
  openEngine,
  type AccessRequest,
  type Engine,
  type EngineEvent,
  type FrozenJSON,
  type SubscriptionStatus,
} from '../dist/index.js';
import { openMetaSchema } from './behavior-fixtures.ts';
import { alice, cleanup, drivers, freshPath, thrown, track } from './helpers.ts';
import {
  ledger,
  ledgerDocument,
  mark,
  openRunnerEngine,
  plainDocument,
  probe,
  publish,
  resetProbe,
  runnerPrincipal,
  settle,
  testClock,
  waitFor,
} from './runner-fixtures.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

function notes(engine: Engine, schema: string, id: string): unknown {
  return engine.instances.get(alice, schema, id)?.data.notes;
}

function history(engine: Engine, schema: string, id: string): EngineEvent[] {
  return engine.events.read(alice, { schema, instanceId: id }).events;
}

function subscription(engine: Engine, schema: string): SubscriptionStatus {
  const found = engine.runner.status().subscriptions.find((candidate) => candidate.schema === schema);
  assert.ok(found, `no subscription on ${schema}`);
  return found;
}

// markCallers is a reaction that writes a note for each caller's change
// and ignores what the runner's work wrote.
function markCallers(): void {
  probe.react = (context, event) => {
    if (event.cause === undefined) {
      mark(context, event);
    }
  };
}

for (const driver of drivers) {
  describe(`runner (${driver})`, () => {
    test('a reaction runs after the commit, as the runner principal, and the event it writes records its cause', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      const seen: Array<[number, string, string]> = [];
      probe.react = (context, event) => {
        seen.push([event.cursor, context.principal.subject, context.schema]);
        if (event.cause === undefined) {
          mark(context, event);
        }
      };
      const created = engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.deepEqual(seen, []);
      assert.equal(created.data.notes, undefined);

      assert.deepEqual(engine.runner.runDue(), { handled: 2, skipped: 0, failed: 0, scheduled: 0 });
      const [create, marked] = history(engine, 'Order', 'o1');
      assert.deepEqual(
        [create, marked].map((event) => [event.kind, event.actor, event.cause]),
        [
          ['create', 'alice', undefined],
          ['operation', 'runner', { behavior: 'test.Ledger', event: create.cursor, depth: 1 }],
        ]
      );
      assert.deepEqual(notes(engine, 'Order', 'o1'), [`create ${create.cursor}`]);
      assert.deepEqual(seen, [
        [create.cursor, 'runner', 'Order'],
        [marked.cursor, 'runner', 'Order'],
      ]);
      const status = subscription(engine, 'Order');
      assert.deepEqual(
        { ...status },
        {
          behavior: 'test.Ledger',
          namespace: 'default',
          schema: 'Order',
          state: 'active',
          cursor: marked.cursor,
          attempts: 0,
          retryAt: null,
          failure: null,
          skipped: 0,
          lastSkip: null,
        }
      );
      assert.equal(engine.runner.status().head, marked.cursor);
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0 });
    });

    test('started, the runner wakes on a commit once the write has returned, and stops', async () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      markCallers();
      engine.runner.start();
      engine.runner.start();
      assert.equal(engine.runner.running, true);
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(notes(engine, 'Order', 'o1'), undefined);
      await waitFor(() => notes(engine, 'Order', 'o1') !== undefined);
      assert.equal((notes(engine, 'Order', 'o1') as string[]).length, 1);
      engine.runner.stop();
      assert.equal(engine.runner.running, false);
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      await settle();
      assert.equal(notes(engine, 'Order', 'o2'), undefined);
    });

    test('a write that rolls back leaves no event, so nothing reacts', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      markCallers();
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.runner.runDue();
      const cursor = subscription(engine, 'Order').cursor;
      assert.throws(() => engine.instances.create(alice, 'Order', { title: 3 }), EngineError);
      assert.throws(() =>
        engine.storage.transaction(() => {
          engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
          throw new Error('the caller rolls back');
        })
      );
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0 });
      assert.equal(engine.instances.get(alice, 'Order', 'o2'), undefined);
      assert.equal(subscription(engine, 'Order').cursor, cursor);
    });

    test('a reaction that throws after its invoke leaves neither its effect nor a cursor move, and its retry writes once', () => {
      const clock = testClock();
      const engine = openRunnerEngine({ driver, clock });
      publish(engine, ledgerDocument('Order'));
      const start = subscription(engine, 'Order').cursor;
      let crash = true;
      probe.react = (context, event) => {
        if (event.cause !== undefined) {
          return;
        }
        mark(context, event);
        if (crash) {
          throw new Error('crash before the commit');
        }
      };
      const created = engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const cursor = history(engine, 'Order', 'o1')[0].cursor;
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 1, scheduled: 0 });
      assert.equal(notes(engine, 'Order', 'o1'), undefined);
      assert.equal(history(engine, 'Order', 'o1').length, 1);
      assert.equal(engine.instances.get(alice, 'Order', 'o1')?.seq, created.seq);
      const failed = subscription(engine, 'Order');
      assert.deepEqual(
        [failed.state, failed.cursor, failed.attempts, failed.retryAt, failed.failure],
        ['retrying', start, 1, clock.now + 1_000, { cursor, at: clock.now, error: 'Error: crash before the commit' }]
      );

      crash = false;
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0 });
      clock.now += 1_000;
      assert.equal(engine.runner.runDue().handled, 2);
      assert.deepEqual(notes(engine, 'Order', 'o1'), [`create ${cursor}`]);
      assert.equal(history(engine, 'Order', 'o1').filter((event) => event.kind === 'operation').length, 1);
      const recovered = subscription(engine, 'Order');
      assert.deepEqual([recovered.state, recovered.attempts, recovered.retryAt, recovered.failure], ['active', 0, null, null]);
    });

    test('a process that dies between a reaction and its commit leaves nothing, and the next run writes once', () => {
      const path = freshPath();
      const options = { path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [ledger], runner: { principal: runnerPrincipal } };
      const first = openEngine(options);
      publish(first, ledgerDocument('Order'));
      first.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      first.close();

      const child = spawnSync(process.execPath, [fileURLToPath(new URL('./runner-crash.ts', import.meta.url)), path, driver], { encoding: 'utf8' });
      assert.equal(child.status, 7, `the child process ended with ${String(child.status)}: ${child.stderr}`);

      const second = track(openEngine(options));
      assert.equal(notes(second, 'Order', 'o1'), undefined);
      assert.deepEqual(
        history(second, 'Order', 'o1').map((event) => event.kind),
        ['create']
      );
      assert.equal(second.storage.get('SELECT COUNT(*) AS count FROM engine_subscriptions')?.count, 0);
      markCallers();
      assert.equal(second.runner.runDue().handled, 2);
      assert.deepEqual(notes(second, 'Order', 'o1'), [`create ${history(second, 'Order', 'o1')[0].cursor}`]);
    });

    test('a subscription handles its events in log order, and none after one that fails until it succeeds', () => {
      const clock = testClock();
      const engine = openRunnerEngine({ driver, clock });
      publish(engine, ledgerDocument('Order'));
      let refuse: number | undefined;
      probe.react = (context, event) => {
        if (event.cause !== undefined) {
          return;
        }
        if (event.cursor === refuse) {
          throw new Error('not yet');
        }
        mark(context, event);
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      engine.instances.update(alice, 'Order', 'o1', { title: 'Desk, oak' });
      engine.instances.create(alice, 'Order', { title: 'Rug' }, { id: 'o3' });
      const caller = engine.events.read(alice, { schema: 'Order' }).events.filter((event) => event.kind !== 'publish');
      const [created1, created2, updated1, created3] = caller;
      refuse = created2.cursor;

      assert.deepEqual(engine.runner.runDue(), { handled: 1, skipped: 0, failed: 1, scheduled: 0 });
      assert.equal(subscription(engine, 'Order').cursor, created1.cursor);
      assert.deepEqual(notes(engine, 'Order', 'o1'), [`create ${created1.cursor}`]);
      assert.equal(notes(engine, 'Order', 'o2'), undefined);
      assert.equal(notes(engine, 'Order', 'o3'), undefined);

      refuse = undefined;
      clock.now += 1_000;
      engine.runner.runDue();
      assert.deepEqual(notes(engine, 'Order', 'o1'), [`create ${created1.cursor}`, `update ${updated1.cursor}`]);
      assert.deepEqual(notes(engine, 'Order', 'o2'), [`create ${created2.cursor}`]);
      assert.deepEqual(notes(engine, 'Order', 'o3'), [`create ${created3.cursor}`]);
      const caused = engine.events
        .read(alice, { schema: 'Order', after: created3.cursor })
        .events.map((event) => event.cause?.event);
      assert.deepEqual(caused, [created1.cursor, created2.cursor, updated1.cursor, created3.cursor]);
    });

    test('failures retry with backoff, halt after maxAttempts and resume, and skip passes over the event', () => {
      const clock = testClock();
      const engine = openRunnerEngine({
        driver,
        clock,
        runner: { principal: runnerPrincipal, maxAttempts: 3, retryInitialMs: 100, retryMaxMs: 150 },
      });
      publish(engine, ledgerDocument('Order'));
      publish(engine, ledgerDocument('Note'));
      let refuse = true;
      probe.react = (context, event) => {
        if (event.cause !== undefined) {
          return;
        }
        if (context.schema === 'Order' && refuse) {
          throw new EngineError('invalid_argument', 'refused');
        }
        mark(context, event);
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.create(alice, 'Note', { title: 'Call' }, { id: 'n1' });
      const failing = history(engine, 'Order', 'o1')[0].cursor;

      assert.equal(engine.runner.runDue().failed, 1);
      assert.equal((notes(engine, 'Note', 'n1') as string[]).length, 1);
      const retries: unknown[] = [];
      for (const wait of [99, 1, 149, 1]) {
        clock.now += wait;
        engine.runner.runDue();
        const status = subscription(engine, 'Order');
        retries.push([status.state, status.attempts, status.retryAt === null ? null : status.retryAt - clock.now]);
      }
      // 100 ms after the first failure, then min(200, 150) after the second; the third halts.
      assert.deepEqual(retries, [
        ['retrying', 1, 1],
        ['retrying', 2, 150],
        ['retrying', 2, 1],
        ['halted', 3, null],
      ]);
      const halted = subscription(engine, 'Order');
      assert.deepEqual(halted.failure, { cursor: failing, at: clock.now, error: 'invalid_argument: refused' });

      clock.now += 1_000_000;
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      engine.instances.create(alice, 'Note', { title: 'Write' }, { id: 'n2' });
      engine.runner.runDue();
      assert.equal(notes(engine, 'Order', 'o2'), undefined);
      assert.equal((notes(engine, 'Note', 'n2') as string[]).length, 1);
      assert.equal(subscription(engine, 'Order').state, 'halted');

      const resumed = engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' });
      assert.deepEqual([resumed.state, resumed.attempts, resumed.failure], ['active', 0, null]);
      assert.equal(engine.runner.runDue().failed, 1);
      assert.equal(subscription(engine, 'Order').state, 'retrying');

      refuse = false;
      const skipped = engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, { skip: true });
      assert.deepEqual(
        [skipped.state, skipped.cursor, skipped.skipped, skipped.lastSkip],
        ['active', failing, 1, { cursor: failing, reason: 'resume' }]
      );
      engine.runner.runDue();
      assert.equal(notes(engine, 'Order', 'o1'), undefined);
      assert.equal((notes(engine, 'Order', 'o2') as string[]).length, 1);

      assert.equal(
        thrown(() => engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, { skip: true }), EngineError).code,
        'invalid_argument'
      );
      assert.equal(thrown(() => engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Ghost' }), EngineError).code, 'not_found');
    });

    test('the depth limit stops a loop of reactions, and the subscription counts what it passed over', () => {
      const engine = openRunnerEngine({ driver, runner: { principal: runnerPrincipal, maxDepth: 3 } });
      publish(engine, ledgerDocument('Order'));
      probe.react = (context, event) => mark(context, event);
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.deepEqual(engine.runner.runDue(), { handled: 3, skipped: 1, failed: 0, scheduled: 0 });
      const events = history(engine, 'Order', 'o1');
      assert.deepEqual(
        events.map((event) => [event.cause?.event, event.cause?.depth ?? 0]),
        [
          [undefined, 0],
          [events[0].cursor, 1],
          [events[1].cursor, 2],
          [events[2].cursor, 3],
        ]
      );
      const status = subscription(engine, 'Order');
      assert.deepEqual([status.state, status.cursor, status.skipped, status.lastSkip], ['active', events[3].cursor, 1, { cursor: events[3].cursor, reason: 'depth' }]);
      assert.equal((notes(engine, 'Order', 'o1') as string[]).length, 3);
    });

    test('the runner acts as its principal: the policy is asked as it, a refusal halts, and the caller does not limit it', () => {
      const clock = testClock();
      const asked: AccessRequest[] = [];
      let grant = false;
      const policy = (request: AccessRequest): boolean => {
        if (request.principal.subject === 'runner') {
          asked.push(request);
          return request.action === 'read' || grant;
        }
        // alice may not call mark herself.
        return request.operation !== 'mark';
      };
      const engine = openRunnerEngine({ driver, clock, policy, runner: { principal: runnerPrincipal, maxAttempts: 2 } });
      publish(engine, ledgerDocument('Order'));
      markCallers();
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(thrown(() => engine.instances.invoke(alice, 'Order', 'o1', 'mark', { note: 'mine' }), EngineError).code, 'forbidden');

      assert.equal(engine.runner.runDue().failed, 1);
      assert.match(subscription(engine, 'Order').failure?.error ?? '', /^forbidden: runner may not call mark \(write\) on Order in namespace default$/);
      assert.ok(asked.some((request) => request.action === 'write' && request.schema === 'Order' && request.operation === 'mark'));
      clock.now += 1_000;
      engine.runner.runDue();
      assert.equal(subscription(engine, 'Order').state, 'halted');

      grant = true;
      engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' });
      engine.runner.runDue();
      assert.equal((notes(engine, 'Order', 'o1') as string[]).length, 1);
      assert.equal(history(engine, 'Order', 'o1')[1].actor, 'runner');
    });

    test('without a principal the runner neither starts nor runs, and bad options refuse to open', () => {
      const engine = track(openEngine({ path: freshPath(), driver, policy: allowAll }));
      assert.match(thrown(() => engine.runner.start(), TypeError).message, /runner\.start needs a principal/);
      assert.match(thrown(() => engine.runner.runDue(), TypeError).message, /runner\.runDue needs a principal/);
      assert.deepEqual(engine.runner.status(), { running: false, principal: null, head: 0, subscriptions: [], schedules: [], error: null });
      for (const runner of [
        { principal: { subject: '', permissions: [] } },
        { principal: { subject: 'runner' } },
        { principal: runnerPrincipal, maxDepth: 0 },
        { principal: runnerPrincipal, batchSize: 501 },
        { principal: runnerPrincipal, retryInitialMs: 10, retryMaxMs: 5 },
      ]) {
        assert.throws(() => openEngine({ path: freshPath(), driver, policy: allowAll, runner: runner as never }), TypeError, JSON.stringify(runner));
      }
    });

    test('stop and start resume from the saved cursor, and so does a new engine on the file', async () => {
      const path = freshPath();
      const options = { path, driver, policy: allowAll, metaSchema: openMetaSchema(), behaviors: [ledger], runner: { principal: runnerPrincipal } };
      const engine = track(openEngine(options));
      publish(engine, ledgerDocument('Order'));
      markCallers();
      engine.runner.start();
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      await waitFor(() => notes(engine, 'Order', 'o1') !== undefined);
      engine.runner.stop();
      engine.instances.create(alice, 'Order', { title: 'Lamp' }, { id: 'o2' });
      await settle();
      assert.equal(notes(engine, 'Order', 'o2'), undefined);
      engine.runner.start();
      await waitFor(() => notes(engine, 'Order', 'o2') !== undefined);
      engine.instances.create(alice, 'Order', { title: 'Rug' }, { id: 'o3' });
      engine.close();
      assert.equal(engine.runner.running, false);

      const reopened = track(openEngine(options));
      reopened.runner.runDue();
      for (const id of ['o1', 'o2', 'o3']) {
        assert.equal((notes(reopened, 'Order', id) as string[]).length, 1, id);
      }
    });

    test('a schedule runs an interval after the runner finds it, once for any number of missed ticks, and retries without halting', () => {
      const clock = testClock(0);
      const engine = openRunnerEngine({ driver, clock, runner: { principal: runnerPrincipal, retryInitialMs: 50_000, retryMaxMs: 500_000 } });
      publish(engine, ledgerDocument('Order'));
      publish(engine, plainDocument('Note'));
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const runs: Array<[string, number | undefined, number]> = [];
      let fail = false;
      probe.sweep = (context) => {
        runs.push([context.schedule, context.previous, context.now]);
        if (fail) {
          throw new Error('the sweep failed');
        }
        context.instances.invoke('Order', 'o1', 'mark', { note: `sweep at ${context.now}` } as FrozenJSON);
      };

      assert.equal(engine.runner.runDue().scheduled, 0);
      const found = engine.runner.status().schedules;
      assert.deepEqual(found, [
        {
          behavior: 'test.Ledger',
          schedule: 'sweep',
          namespace: 'default',
          schema: 'Order',
          state: 'active',
          everyMs: 60_000,
          previous: null,
          next: 60_000,
          failures: 0,
          error: null,
        },
      ]);
      clock.now = 59_999;
      assert.equal(engine.runner.runDue().scheduled, 0);
      clock.now = 60_000;
      assert.equal(engine.runner.runDue().scheduled, 1);
      clock.now = 60_000 + 10 * 60_000;
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(runs, [
        ['sweep', undefined, 60_000],
        ['sweep', 60_000, 660_000],
      ]);
      const swept = history(engine, 'Order', 'o1').filter((event) => event.kind === 'operation');
      assert.deepEqual(
        swept.map((event) => [event.actor, event.cause]),
        [
          ['runner', { behavior: 'test.Ledger', schedule: 'sweep', depth: 1 }],
          ['runner', { behavior: 'test.Ledger', schedule: 'sweep', depth: 1 }],
        ]
      );

      fail = true;
      const failures: unknown[] = [];
      clock.now = 720_000;
      for (let attempt = 0; attempt < 4; attempt += 1) {
        assert.equal(engine.runner.runDue().failed, 1);
        const [status] = engine.runner.status().schedules;
        failures.push([status.state, status.failures, status.next - clock.now]);
        clock.now = status.next;
      }
      // 50 s after the first failure, then never later than the next tick.
      assert.deepEqual(failures, [
        ['retrying', 1, 50_000],
        ['retrying', 2, 60_000],
        ['retrying', 3, 60_000],
        ['retrying', 4, 60_000],
      ]);
      assert.equal(engine.runner.status().schedules[0].error, 'Error: the sweep failed');
      fail = false;
      assert.equal(engine.runner.runDue().scheduled, 1);
      const [recovered] = engine.runner.status().schedules;
      assert.deepEqual([recovered.state, recovered.failures, recovered.error, recovered.previous], ['active', 0, null, clock.now]);
    });

    test('a subscription starts at the publish that composes the behavior, and hears the schemas watches names', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, plainDocument('Note'));
      publish(engine, plainDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Before' }, { id: 'o1' });
      const seen: string[] = [];
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          seen.push(`${context.schema} hears ${event.kind} ${event.schema} ${event.instanceId}`);
        }
      };
      publish(engine, ledgerDocument('Order', { watch: ['Note'] }));
      engine.instances.create(alice, 'Order', { title: 'After' }, { id: 'o2' });
      engine.instances.create(alice, 'Note', { title: 'Call' }, { id: 'n1' });
      engine.runner.runDue();
      assert.deepEqual(seen, ['Order hears create Order o2', 'Order hears create Note n1']);

      // Removed, its subscription is inactive and hears nothing; composed
      // again, it starts at that publish.
      publish(engine, plainDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Between' }, { id: 'o3' });
      engine.runner.runDue();
      assert.equal(subscription(engine, 'Order').state, 'inactive');
      publish(engine, ledgerDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Again' }, { id: 'o4' });
      engine.runner.runDue();
      assert.deepEqual(seen.slice(2), ['Order hears create Order o4']);
      assert.equal(subscription(engine, 'Order').state, 'active');
    });

    test('a reaction reads an instance as the log had it before an event, and invokes schema-level operations', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      const before: unknown[] = [];
      const counted: unknown[] = [];
      probe.react = (context, event) => {
        if (event.cause === undefined) {
          before.push(context.before(event) ?? null);
          counted.push(context.instances.invokeSchema('Order', 'count'));
        }
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      engine.instances.update(alice, 'Order', 'o1', { title: 'Lamp' });
      engine.instances.invoke(alice, 'Order', 'o1', 'mark', { note: 'kept' });
      engine.instances.delete(alice, 'Order', 'o1');
      engine.runner.runDue();
      assert.deepEqual(before, [null, { title: 'Desk' }, { title: 'Lamp' }, { title: 'Lamp', notes: ['kept'] }]);
      assert.deepEqual(counted, [0, 0, 0, 0]);

      probe.react = (context) => {
        context.instances.invokeSchema('Order', 'mark', { note: 'x' } as FrozenJSON);
      };
      engine.instances.create(alice, 'Order', { title: 'Rug' }, { id: 'o2' });
      engine.runner.runDue();
      assert.match(subscription(engine, 'Order').failure?.error ?? '', /^not_found: Order's mark is an instance operation/);
    });

    test('registration refuses reactions and schedules the runner cannot run', () => {
      for (const [broken, problem] of [
        [{ ...ledger, reactions: { react: 1 } }, /reactions\.react is a function/],
        [{ ...ledger, reactions: { react() {}, watches: [] } }, /reactions\.watches is a function/],
        [{ ...ledger, schedules: { Sweep: { everyMs: 60_000, run() {} } } }, /schedule Sweep: a schedule name is camelCase/],
        [{ ...ledger, schedules: { sweep: { everyMs: 999, run() {} } } }, /schedule sweep: everyMs is an integer of at least 1000, got 999/],
        [{ ...ledger, schedules: { sweep: { everyMs: 1_000 } } }, /schedule sweep: run is a function/],
      ] as const) {
        assert.match(thrown(() => openRunnerEngine({ driver, behaviors: [broken as never] }), TypeError).message, problem);
      }
    });

    test('a watches that throws is a failure of the subscription, with no event, which passes once it works', () => {
      const clock = testClock();
      const engine = openRunnerEngine({ driver, clock });
      publish(engine, ledgerDocument('Order'));
      markCallers();
      probe.watches = () => {
        throw new Error('no schemas today');
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(engine.runner.runDue().failed, 1);
      const failed = subscription(engine, 'Order');
      assert.deepEqual([failed.state, failed.attempts, failed.failure], ['retrying', 1, { cursor: null, at: clock.now, error: 'Error: no schemas today' }]);
      assert.equal(
        thrown(() => engine.runner.resume({ behavior: 'test.Ledger', namespace: 'default', schema: 'Order' }, { skip: true }), EngineError).code,
        'invalid_argument'
      );
      probe.watches = () => ['Order', 3 as unknown as string];
      clock.now += 1_000;
      engine.runner.runDue();
      assert.equal(subscription(engine, 'Order').failure?.error, 'BehaviorError: behavior test.Ledger: reactions.watches returns a list of schema names');

      delete probe.watches;
      clock.now += 2_000;
      engine.runner.runDue();
      assert.equal((notes(engine, 'Order', 'o1') as string[]).length, 1);
      const recovered = subscription(engine, 'Order');
      assert.deepEqual([recovered.state, recovered.attempts, recovered.failure], ['active', 0, null]);
    });

    test('runDue inside a reaction is refused, as a failure of that reaction', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      probe.react = () => {
        engine.runner.runDue();
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(engine.runner.runDue().failed, 1);
      assert.match(subscription(engine, 'Order').failure?.error ?? '', /the runner is already running/);
    });
  });
}
