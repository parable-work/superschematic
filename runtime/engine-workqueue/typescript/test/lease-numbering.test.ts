// Lease beside the core's Numbering (D56) on one type: a create holds no
// lease, so Numbering numbers each new instance whoever holds what, and
// Lease's guard holds assignNumber, a writing operation, to the holder
// unless the config exempts it, as a backfill of leased instances needs.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, type Principal } from '@superschematic/engine';

import { alice, cleanup, drivers, jobsDocument, openTestEngine, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };

const numbering = { name: 'Numbering', config: { format: 'J-{seq:3}' } };

for (const driver of drivers) {
  describe(`Lease beside Numbering (${driver})`, () => {
    test("a create is numbered under another's lease, and a backfill reaches a leased instance when Lease exempts assignNumber", () => {
      const engine = openTestEngine({ driver });
      publish(engine, jobsDocument([{ name: 'Lease' }]));
      for (const id of ['j1', 'j2']) {
        engine.instances.create(alice, 'Job', { title: id }, { id });
        engine.instances.invoke(worker, 'Job', id, 'acquire');
      }
      publish(engine, jobsDocument([{ name: 'Lease' }, numbering]));
      assert.deepEqual(engine.instances.create(worker, 'Job', { title: 'j3' }, { id: 'j3' }).behaviors.Numbering, { seq: 1, label: 'J-001' });

      // The backfill of a leased instance is the holder's alone.
      const refused = thrown(() => engine.instances.invoke(alice, 'Job', 'j1', 'assignNumber'), BehaviorVetoError);
      assert.deepEqual([refused.behavior, refused.vetoCode], ['Lease', 'held_by_another']);
      assert.deepEqual(engine.instances.invoke(worker, 'Job', 'j1', 'assignNumber'), { seq: 2, label: 'J-002' });

      // Exempted, it reaches every instance, leased or not.
      publish(engine, jobsDocument([{ name: 'Lease', config: { exempt: ['Numbering.assignNumber'] } }, numbering]));
      assert.deepEqual(engine.instances.invoke(alice, 'Job', 'j2', 'assignNumber'), { seq: 3, label: 'J-003' });
      assert.equal(engine.instances.get(alice, 'Job', 'j2')?.behaviors.Lease.holder, worker.subject);
    });
  });
}
