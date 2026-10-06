// What a schedule gains (D32, What D16 gains): an everyMs function that
// returns null turns it off on a schema until a publish gives it an
// interval, and a run writes its behavior's own tables in its own
// transaction, as the runner's principal. A reaction still writes none.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import type { Engine, ScheduleStatus, SqlWriter } from '../dist/index.js';
import { alice, cleanup, drivers } from './helpers.ts';
import {
  ledger,
  ledgerDocument,
  openRunnerEngine,
  pacer,
  pacerDocument,
  probe,
  publish,
  resetProbe,
  settle,
  testClock,
} from './runner-fixtures.ts';

afterEach(() => {
  resetProbe();
  cleanup();
});

function schedules(engine: Engine): Record<string, Omit<ScheduleStatus, 'behavior' | 'schedule' | 'namespace' | 'schema'>> {
  return Object.fromEntries(
    engine.runner.status().schedules.map(({ behavior: _behavior, schedule: _schedule, namespace: _namespace, schema, ...rest }) => [schema, rest])
  );
}

// notesOf reads test.Ledger's own table, as the engine stores it.
function notesOf(engine: Engine): string[] {
  return engine.storage.all('SELECT note FROM bhv_test_ledger__notes ORDER BY row').map((row) => String(row.note));
}

const off = { state: 'off', everyMs: null, previous: null, next: null, failures: 0, error: null };

for (const driver of drivers) {
  describe(`a schedule a schema turns off (${driver})`, () => {
    test('runs nothing on that schema and shows as off, runs on the others, and runs again an interval after a publish gives it one', () => {
      const clock = testClock(0);
      const engine = openRunnerEngine({ driver, clock, behaviors: [ledger, pacer] });
      probe.every = (config) => config.everyMs ?? null;
      const runs: string[] = [];
      probe.tick = (context) => {
        runs.push(`${context.schema} ${context.now}`);
      };
      publish(engine, pacerDocument('On', { everyMs: 5_000 }));
      publish(engine, pacerDocument('Off', {}));

      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 0 });
      assert.deepEqual(schedules(engine), {
        Off: off,
        On: { state: 'active', everyMs: 5_000, previous: null, next: 5_000, failures: 0, error: null },
      });
      clock.now = 50_000;
      assert.deepEqual(engine.runner.runDue(), { handled: 0, skipped: 0, failed: 0, scheduled: 1 });
      assert.deepEqual(runs, ['On 50000']);
      assert.deepEqual(schedules(engine).Off, off);

      // A publish gives Off an interval: the runner asks again and finds it
      // as for the first time, due an interval later.
      publish(engine, pacerDocument('Off', { everyMs: 2_000 }));
      assert.deepEqual(engine.runner.runDue().scheduled, 0);
      assert.deepEqual(schedules(engine).Off, { state: 'active', everyMs: 2_000, previous: null, next: 52_000, failures: 0, error: null });
      clock.now = 52_000;
      engine.runner.runDue();
      assert.deepEqual(runs, ['On 50000', 'Off 52000']);

      // A publish turns On off: it runs no more, and what the runner kept
      // for it goes, so turning it on again starts it afresh.
      publish(engine, pacerDocument('On', {}));
      clock.now = 100_000;
      engine.runner.runDue();
      assert.deepEqual(runs, ['On 50000', 'Off 52000', 'Off 100000']);
      assert.deepEqual(schedules(engine).On, off);
      assert.equal(engine.storage.all("SELECT 1 FROM engine_schedules WHERE schema = 'On'").length, 0);
      publish(engine, pacerDocument('On', { everyMs: 5_000 }));
      engine.runner.runDue();
      assert.deepEqual(schedules(engine).On, { state: 'active', everyMs: 5_000, previous: null, next: 105_000, failures: 0, error: null });
    });

    test('a started runner whose only schedule is off sets no timer and reads no clock once its pass is done; one that is on sets one', async () => {
      const real = globalThis.setTimeout;
      const timers: number[] = [];
      globalThis.setTimeout = ((handler: () => void, delay?: number) => {
        timers.push(Number(delay ?? 0));
        return real(handler, delay);
      }) as typeof setTimeout;
      try {
        for (const [config, armed] of [
          [{}, []],
          [{ everyMs: 60_000 }, [60_000]],
        ] as const) {
          let reads = 0;
          const clock = testClock(0);
          const counted = () => {
            reads += 1;
            return clock();
          };
          const engine = openRunnerEngine({ driver, clock: counted, behaviors: [ledger, pacer] });
          probe.every = (pacing) => pacing.everyMs ?? null;
          publish(engine, pacerDocument('P', config));
          timers.length = 0;
          engine.runner.start();
          await settle();
          const after = reads;
          await new Promise<void>((resolve) => real(resolve, 50));
          await settle();
          assert.deepEqual([timers, reads - after], [armed, 0], `the runner with ${JSON.stringify(config)}`);
          engine.runner.stop();
        }
      } finally {
        globalThis.setTimeout = real;
      }
    });

    test('only null turns it off: a function that returns nothing still fails the schedule there', () => {
      const clock = testClock(0);
      const engine = openRunnerEngine({ driver, clock, behaviors: [ledger, pacer] });
      probe.every = (config) => (config.everyMs === undefined ? undefined : config.everyMs);
      publish(engine, pacerDocument('Blank', {}));
      assert.equal(engine.runner.runDue().failed, 1);
      const [status] = engine.runner.status().schedules;
      assert.deepEqual([status.state, status.everyMs], ['retrying', null]);
      assert.match(status.error ?? '', /everyMs\(config\) returns an integer of at least 1000, got undefined/);
    });
  });

  describe(`a schedule's writes (${driver})`, () => {
    test("a run writes its behavior's own tables as the runner's principal, and its writes roll back with the run when it throws", () => {
      const clock = testClock(0);
      const engine = openRunnerEngine({ driver, clock });
      publish(engine, ledgerDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      let fail = false;
      probe.sweep = (context) => {
        context.sql.run(`INSERT INTO ${context.sql.table('notes')} (namespace, schema, id, note) VALUES (?, ?, ?, ?)`, [
          context.namespace,
          context.schema,
          'o1',
          `swept by ${context.principal.subject} at ${context.now}`,
        ]);
        if (fail) {
          throw new Error('the sweep failed after its write');
        }
      };
      engine.runner.runDue();
      clock.now = 60_000;
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(notesOf(engine), ['swept by runner at 60000']);
      // The write is the behavior's alone: no event records it.
      assert.deepEqual(
        engine.events.read(alice, { schema: 'Order' }).events.map((event) => event.kind),
        ['define', 'publish', 'create']
      );

      fail = true;
      clock.now = 120_000;
      assert.equal(engine.runner.runDue().failed, 1);
      assert.deepEqual(notesOf(engine), ['swept by runner at 60000']);
      const [status] = engine.runner.status().schedules;
      assert.deepEqual([status.state, status.failures, status.error], ['retrying', 1, 'Error: the sweep failed after its write']);
    });

    test('the relation over the instances stays read-only in a run', () => {
      const clock = testClock(0);
      const engine = openRunnerEngine({ driver, clock });
      publish(engine, ledgerDocument('Order'));
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      const seen: unknown[] = [];
      probe.sweep = (context) => {
        seen.push(context.sql.all(`SELECT id FROM ${context.sql.instances()}`));
        context.sql.run(`UPDATE ${context.sql.instances()} SET data = '{}'`);
      };
      engine.runner.runDue();
      clock.now = 60_000;
      assert.equal(engine.runner.runDue().failed, 1);
      assert.deepEqual(seen, [[{ id: 'o1' }]]);
      assert.match(engine.runner.status().schedules[0].error ?? '', /bhv_test_ledger___instances/);
      assert.deepEqual(engine.instances.get(alice, 'Order', 'o1')?.data.title, 'Desk');
    });
  });

  describe(`a reaction's SQL (${driver})`, () => {
    test('still writes nothing: a write is a failure of the reaction, and leaves nothing', () => {
      const engine = openRunnerEngine({ driver });
      publish(engine, ledgerDocument('Order'));
      probe.react = (context, event) => {
        (context.sql as unknown as SqlWriter).run(`INSERT INTO ${context.sql.table('notes')} (namespace, schema, id, note) VALUES (?, ?, ?, ?)`, [
          context.namespace,
          context.schema,
          event.instanceId as string,
          'reacted',
        ]);
      };
      engine.instances.create(alice, 'Order', { title: 'Desk' }, { id: 'o1' });
      assert.equal(engine.runner.runDue().failed, 1);
      assert.deepEqual(notesOf(engine), []);
      const [subscription] = engine.runner.status().subscriptions;
      assert.equal(subscription.state, 'retrying');
      assert.match(subscription.failure?.error ?? '', /behavior test\.Ledger: a read cannot run a statement that writes/);
    });
  });
}
