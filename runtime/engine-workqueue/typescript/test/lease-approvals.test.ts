// Lease beside the core's Approvals (D56) on one type: a sign-off is a
// writing operation of another principal than the holder, so Lease's guard
// refuses it while a lease is active, unless the config exempts signOff and
// withdrawSignOff. Exempted, a reviewer signs a leased job off, the
// holder's change of the job consumes the sign-off as any change does, and
// the holder's move into the gated state waits for it.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, jobFlow, jobsDocument, openTestEngine, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const reviewer: Principal = { subject: 'rae', permissions: ['jobs.review'] };

const approvals = { name: 'Approvals', config: { gates: { done: { count: 1, permission: 'jobs.review' } } } };

for (const driver of drivers) {
  describe(`Lease beside Approvals (${driver})`, () => {
    function running(lease: Record<string, unknown>) {
      const clock = new Clock();
      const engine = openTestEngine({ driver, clock: clock.now });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease', config: lease }, approvals]));
      engine.instances.create(alice, 'Job', { title: 'Index' }, { id: 'j1' });
      const invoke = <T>(who: Principal, operation: string, params: Record<string, unknown> = {}): T =>
        engine.instances.invoke(who, 'Job', 'j1', operation, params) as T;
      invoke(worker, 'acquire');
      invoke(worker, 'transition', { to: 'running' });
      return { engine, invoke };
    }

    test("without exempt, the holder's lease refuses another principal's sign-off", () => {
      const { invoke } = running({});
      const refused = thrown(() => invoke(reviewer, 'signOff', { state: 'done' }), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.action, refused.vetoCode], ['Lease', 'signOff', 'held_by_another']);
    });

    test("with signOff and withdrawSignOff exempt, a reviewer signs a leased job off, the holder's change consumes it, and the move waits for it", () => {
      const { engine, invoke } = running({ exempt: ['Approvals.signOff', 'Approvals.withdrawSignOff'] });
      const gates = () => engine.instances.get(alice, 'Job', 'j1')?.behaviors.Approvals?.gates;
      invoke(reviewer, 'signOff', { state: 'done' });
      invoke(reviewer, 'withdrawSignOff', { state: 'done' });
      invoke(reviewer, 'signOff', { state: 'done' });
      assert.deepEqual(gates(), { done: { signedBy: ['rae'] } });

      // The holder changes the job: what was signed is not what moves.
      engine.instances.update(worker, 'Job', 'j1', { title: 'Index again' });
      assert.deepEqual(gates(), {});
      assert.equal(thrown(() => invoke(worker, 'transition', { to: 'done' }), BehaviorVetoError).vetoCode, 'approvals_missing');
      invoke(reviewer, 'signOff', { state: 'done' });
      assert.deepEqual(invoke(worker, 'transition', { to: 'done' }), { from: 'running', to: 'done' });
      assert.deepEqual([engine.instances.get(alice, 'Job', 'j1')?.behaviors.Workflow?.status, gates()], ['done', {}]);
      // Exempt are the two Approvals operations, not every other principal's write.
      assert.equal(thrown(() => engine.instances.update(reviewer, 'Job', 'j1', { title: 'Mine' }), BehaviorVetoError).vetoCode, 'held_by_another');
    });
  });
}
