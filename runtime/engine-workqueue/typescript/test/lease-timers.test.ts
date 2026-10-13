// Lease beside the core's Timers (D56) on one type: the runner's fire is a
// writing operation of another principal than the holder, so Lease's guard
// refuses it while a lease is active, and the sweep leaves the deadline
// for its next run, unless the config exempts fireTimer. Exempted, the
// runner fires a leased job's deadline, and its move, a call() of
// Workflow's transition, passes the guard as every behavior's call does.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, jobFlow, jobsDocument, openTestEngine, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const runner: Principal = { subject: 'runner', permissions: [] };

const MINUTE = 60_000;

// A running job fails ten minutes after it starts, unless it has moved on.
const timers = { name: 'Timers', config: { deadlines: { timeout: { start: { enters: 'running' }, afterMs: 10 * MINUTE, then: { transition: 'failed' } } } } };

for (const driver of drivers) {
  describe(`Lease beside Timers (${driver})`, () => {
    function running(lease: Record<string, unknown>) {
      const clock = new Clock();
      const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: runner } });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: { ttlMs: 60 * MINUTE, ...lease } }, timers]));
      engine.runner.runDue();
      engine.instances.create(alice, 'Job', { title: 'Index' }, { id: 'j1' });
      engine.instances.invoke(worker, 'Job', 'j1', 'acquire');
      engine.instances.invoke(worker, 'Job', 'j1', 'transition', { to: 'running' });
      const read = () => engine.instances.get(alice, 'Job', 'j1')?.behaviors;
      const timeout = () => (read()?.Timers.deadlines as { timeout: { fired: boolean; outcome?: string } }).timeout;
      return { engine, clock, read, timeout };
    }

    test("without exempt, the holder's lease refuses the runner's fire, and the sweep leaves the deadline for its next run", () => {
      const { engine, clock, read, timeout } = running({});
      clock.advance(11 * MINUTE);
      const pass = engine.runner.runDue();
      assert.deepEqual([pass.failed, read()?.Workflow.status, timeout().fired], [0, 'running', false]);
      const refused = thrown(() => engine.instances.invoke(runner, 'Job', 'j1', 'fireTimer', { deadline: 'timeout' }), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.action, refused.vetoCode], ['Lease', 'fireTimer', 'held_by_another']);
      // The holder may fire it itself.
      assert.deepEqual(engine.instances.invoke(worker, 'Job', 'j1', 'fireTimer', { deadline: 'timeout' }), { fired: true, outcome: 'moved' });
    });

    test('with fireTimer exempt, the runner fires a leased job and its move passes the guard', () => {
      const { engine, clock, read, timeout } = running({ exempt: ['Timers.fireTimer'] });
      clock.advance(11 * MINUTE);
      engine.runner.runDue();
      assert.deepEqual([read()?.Workflow.status, timeout().outcome], ['failed', 'moved']);
      const fire = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.at(-1);
      assert.deepEqual([fire?.actor, fire?.cause?.schedule], ['runner', 'fire']);
      // Exempt is the fire alone, not every other principal's write.
      assert.equal(thrown(() => engine.instances.update(runner, 'Job', 'j1', { title: 'Mine' }), BehaviorVetoError).vetoCode, 'held_by_another');
    });
  });
}
