// Assignment: a principal takes an unassigned instance for itself or
// gives it up; assigning, reassigning and unassigning anyone else need the
// config's permission; and while an instance is assigned, only its
// assignee takes its lease. Real SQLite, a real engine.
import assert from 'node:assert/strict';
import { afterEach, describe, test } from 'node:test';

import { BehaviorVetoError, EngineError, IncompatibleChangeError, type Engine, type Principal } from '@superschematic/engine';

import { Clock, alice, cleanup, drivers, fenced, jobFlow, jobsDocument, openTestEngine, publish, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const lead: Principal = { subject: 'lena', permissions: ['jobs.assign'] };

const T0 = 1_000_000;

for (const driver of drivers) {
  // world opens an engine whose Job composes Workflow, Lease and
  // Assignment with the config given, with job j1 unassigned.
  function world(config?: Record<string, unknown>) {
    const clock = new Clock(T0);
    const engine = openTestEngine({ driver, clock: clock.now });
    publish(
      engine,
      jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Lease' }, config === undefined ? { name: 'Assignment' } : { name: 'Assignment', config }])
    );
    engine.instances.create(alice, 'Job', { title: 'Build' }, { id: 'j1' });
    return { engine, clock };
  }

  const invoke = (engine: Engine, who: Principal, operation: string, params: Record<string, unknown> = {}) =>
    engine.instances.invoke(who, 'Job', 'j1', operation, params);
  const assigneeOf = (engine: Engine) => engine.instances.get(alice, 'Job', 'j1')?.data.assignee;
  const veto = (fn: () => unknown) => thrown(fn, BehaviorVetoError);

  describe(`Assignment (${driver})`, () => {
    test('a principal assigns an unassigned instance to itself and unassigns itself; the events record who acted', () => {
      const { engine } = world();
      assert.equal(assigneeOf(engine), undefined);
      assert.deepEqual(invoke(engine, worker, 'assign', { to: 'wren' }), { assignee: 'wren', assignedAt: T0, assignedBy: 'wren' });
      assert.equal(assigneeOf(engine), 'wren');
      assert.equal(veto(() => invoke(engine, worker, 'assign', { to: 'wren' })).reason, 'it is already assigned to the caller');
      assert.deepEqual(invoke(engine, worker, 'unassign'), { assignee: 'wren' });
      assert.equal(assigneeOf(engine), undefined);
      assert.equal(veto(() => invoke(engine, worker, 'unassign')).reason, 'it is not assigned');
      const events = engine.events.read(alice, { schema: 'Job', instanceId: 'j1' }).events.slice(1);
      assert.deepEqual(
        events.map((event) => [event.actor, (event.change as { operation: string; patch: unknown }).operation, (event.change as { patch: unknown }).patch]),
        [
          ['wren', 'assign', { assignee: 'wren' }],
          ['wren', 'unassign', { assignee: null }],
        ]
      );
    });

    test('without a permission in the config, no one assigns another principal, takes an assigned instance or unassigns another', () => {
      const { engine } = world();
      assert.equal(veto(() => invoke(engine, worker, 'assign', { to: 'otto' })).reason, 'assigning another principal needs a permission, and its config names none');
      invoke(engine, worker, 'assign', { to: 'wren' });
      assert.equal(
        veto(() => invoke(engine, other, 'assign', { to: 'otto' })).reason,
        'reassigning an instance assigned to another principal needs a permission, and its config names none'
      );
      assert.equal(veto(() => invoke(engine, other, 'unassign')).reason, 'unassigning another principal needs a permission, and its config names none');
      assert.equal(assigneeOf(engine), 'wren');
    });

    test("with the config's permission a principal assigns anyone, reassigns and unassigns; without it, it is forbidden", () => {
      const { engine } = world({ permission: 'jobs.assign' });
      const refused = thrown(() => invoke(engine, worker, 'assign', { to: 'otto' }), EngineError);
      assert.deepEqual([refused.code, refused.message], ['forbidden', 'wren may not assign Job j1: assigning another principal needs permission jobs.assign']);
      assert.deepEqual(invoke(engine, lead, 'assign', { to: 'otto' }), { assignee: 'otto', assignedAt: T0, assignedBy: 'lena' });
      assert.equal(thrown(() => invoke(engine, worker, 'assign', { to: 'wren' }), EngineError).code, 'forbidden');
      assert.equal(thrown(() => invoke(engine, worker, 'unassign'), EngineError).code, 'forbidden');
      assert.equal(veto(() => invoke(engine, lead, 'assign', { to: 'otto' })).reason, 'it is already assigned to that principal');
      invoke(engine, lead, 'assign', { to: 'wren' });
      assert.equal(assigneeOf(engine), 'wren');
      assert.deepEqual(invoke(engine, lead, 'unassign'), { assignee: 'wren' });
      assert.equal(assigneeOf(engine), undefined);
    });

    test('while assigned, only the assignee takes the lease', () => {
      const { engine } = world({ permission: 'jobs.assign' });
      invoke(engine, lead, 'assign', { to: 'wren' });
      const refused = veto(() => invoke(engine, other, 'acquire'));
      assert.deepEqual([refused.behavior, refused.action, refused.reason], ['Assignment', 'acquire', 'it is assigned to another principal, who alone may take it']);
      assert.equal((invoke(engine, worker, 'acquire') as { token: number }).token, 1);
      engine.instances.invoke(worker, 'Job', 'j1', 'release', {}, fenced(1));
      invoke(engine, worker, 'unassign');
      assert.equal((invoke(engine, other, 'acquire') as { token: number }).token, 3);
    });

    test("while another principal holds the lease, Lease's guard refuses an assignment like any other write", () => {
      const { engine } = world({ permission: 'jobs.assign' });
      invoke(engine, worker, 'acquire');
      assert.equal(veto(() => invoke(engine, lead, 'assign', { to: 'otto' })).behavior, 'Lease');
      assert.deepEqual(invoke(engine, worker, 'assign', { to: 'wren' }), { assignee: 'wren', assignedAt: T0, assignedBy: 'wren' });
    });

    test('its permission may change; it can be added to a schema that has instances and not removed from one', () => {
      const engine = openTestEngine({ driver });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }]));
      engine.instances.create(alice, 'Job', { title: 'Before' }, { id: 'j1' });
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Assignment' }]));
      assert.equal(assigneeOf(engine), undefined);
      publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, { name: 'Assignment', config: { permission: 'jobs.assign' } }]));
      assert.match(
        thrown(() => engine.schemas.define(alice, jobsDocument([{ name: 'Workflow', config: jobFlow }])), IncompatibleChangeError).message,
        /behavior Assignment cannot be removed from type Job, which has instances: the assignments its instances hold would stay behind/
      );
    });
  });
}
