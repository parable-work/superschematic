// The package as a deployment uses it: its behaviors register with an
// engine through the plug-in interface, carry the declarations the core
// binary writes, and run the document the core binary builds in the CLI
// smoke (fixture-workqueue-json), whose jobs compose Workflow, Lease,
// Assignment, Queue, Budget and Retries: workers claim them in order, each
// claim reserving its budget, hold and renew their leases and record their
// attempts, and the runner puts a job whose time ran out back in the
// queue, settling what it reserved.
// The fixture's other files hold one type each, which run as schemas of
// their own: a worker with Presence, which releases its principal's leases
// on jobs, and a batch whose Blueprint stamps steps.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import type { Principal } from '@superschematic/engine';

import { DEFAULT_TTL_MS, assignment, blueprint, budget, lease, presence, queue, retries, workQueueBehaviors } from '../dist/index.js';
import { Clock, alice, cleanup, drivers, fenced, jobsFixture, openTestEngine } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const other: Principal = { subject: 'otto', permissions: [] };
const lead: Principal = { subject: 'lena', permissions: ['jobs.assign'] };
const runner: Principal = { subject: 'runner', permissions: [] };

/** fixtureType reads one type's file of fixture-workqueue-json as a schema of its own. */
function fixtureType(name: string, file: string): Record<string, unknown> {
  const type = JSON.parse(
    readFileSync(new URL(`../../../../internal/loader/testdata/services/fixture-workqueue-json/src/${file}.schema.json`, import.meta.url), 'utf8')
  ) as { name: string };
  return { kind: 'General', name, types: { [type.name]: type } };
}

function declarationFile(name: string): unknown {
  return JSON.parse(readFileSync(new URL(`../src/declarations/${name}.behavior.json`, import.meta.url), 'utf8'));
}

for (const driver of drivers) {
  describe(`the work-queue package (${driver})`, () => {
    test('its behaviors carry the core declarations and register with an engine, beside the core behaviors', () => {
      assert.deepEqual(workQueueBehaviors, [lease, assignment, queue, presence, blueprint, budget, retries]);
      assert.deepEqual(lease.declaration, declarationFile('Lease'));
      assert.deepEqual(assignment.declaration, declarationFile('Assignment'));
      assert.deepEqual(queue.declaration, declarationFile('Queue'));
      assert.deepEqual(presence.declaration, declarationFile('Presence'));
      assert.deepEqual(blueprint.declaration, declarationFile('Blueprint'));
      assert.deepEqual(budget.declaration, declarationFile('Budget'));
      assert.deepEqual(retries.declaration, declarationFile('Retries'));
      assert.equal(DEFAULT_TTL_MS, 60000);
      const engine = openTestEngine({ driver });
      assert.deepEqual(engine.behaviors.names(), [
        'Assignment',
        'Blueprint',
        'Budget',
        'Comments',
        'Constants',
        'Dependencies',
        'Lease',
        'Links',
        'Presence',
        'Queue',
        'Reactions',
        'Retries',
        'Revisions',
        'Rollups',
        'Search',
        'Variants',
        'Workflow',
      ]);
    });

    test('it runs the document the core binary builds: claimed in order, held, and put back when its time runs out', () => {
      const clock = new Clock(1_000_000);
      const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: runner } });
      engine.schemas.define(alice, jobsFixture());
      engine.schemas.publish(alice, 'jobs');
      const described = engine.tools.describe(alice, 'jobs');
      assert.deepEqual(
        described.behaviors.map((behavior) => [behavior.name, behavior.operations]),
        [
          ['Workflow', ['transition']],
          ['Lease', ['acquire', 'heartbeat', 'release', 'expire', 'direct', 'acknowledge', 'expireHolder', 'resetExpiries']],
          ['Assignment', ['assign', 'unassign']],
          ['Queue', ['claim', 'claimNext', 'countClaimable', 'refresh']],
          ['Budget', ['reserve', 'checkReserve', 'recordUsage', 'settle', 'setLimit', 'reserveFor', 'settleFor', 'recordUsageFor']],
          ['Retries', ['recordAttempt']],
        ]
      );
      // The config a client reads its lease length from.
      assert.equal((described.behaviors[1].config as { ttlMs: number }).ttlMs, 30000);
      engine.runner.runDue();

      const job = (id: string, data: Record<string, unknown>) => engine.instances.create(alice, 'jobs', { title: id, ...data }, { id });
      job('index', { topic: 'search', priority: 5, timeLimitMs: 45000 });
      job('reindex', { topic: 'search', priority: 9 });
      job('digest', { topic: 'mail', priority: 7 });
      job('mine', { topic: 'search', priority: 10 });
      engine.instances.invoke(lead, 'jobs', 'mine', 'assign', { to: 'otto' });

      const claimNext = (who: Principal, topic: string) =>
        (engine.instances.invokeSchema(who, 'jobs', 'claimNext', { match: { topic } }) as { claimed: { id: string; token: number } | null }).claimed;
      assert.deepEqual(claimNext(worker, 'search'), { id: 'reindex', token: 1, expiresAt: 1_030_000, heartbeatMs: 10000 });
      assert.equal(claimNext(worker, 'search')?.id, 'index');
      assert.equal(claimNext(worker, 'search'), null);
      assert.equal(claimNext(other, 'search')?.id, 'mine');
      // Each claim reserved the job's claim amount of its daily CPU budget.
      assert.deepEqual(engine.instances.get(alice, 'jobs', 'index')?.data.budget, { cpuSeconds: { used: 0, reserved: 600, limit: 3600, remaining: 3000 } });
      engine.instances.invoke(worker, 'jobs', 'index', 'recordUsage', { meter: 'cpuSeconds', amount: 100 });
      engine.instances.invoke(worker, 'jobs', 'index', 'recordAttempt', { failure: 'timeout' });

      clock.advance(20000);
      assert.equal((engine.instances.invoke(worker, 'jobs', 'reindex', 'heartbeat', {}, fenced(1)) as { expiresAt: number }).expiresAt, 1_050_000);
      assert.equal((engine.instances.invoke(worker, 'jobs', 'index', 'heartbeat', {}, fenced(1)) as { expiresAt: number }).expiresAt, 1_045_000);
      engine.instances.invoke(worker, 'jobs', 'reindex', 'transition', { to: 'done' });
      engine.instances.invoke(worker, 'jobs', 'reindex', 'release', {}, fenced(1));

      // index's time limit passes, and mine's lease, never renewed, has
      // expired: the runner's sweep puts both back in the queue.
      clock.advance(25000);
      assert.equal(engine.runner.runDue().scheduled, 1);
      assert.deepEqual(engine.instances.get(alice, 'jobs', 'index')?.data, {
        title: 'index',
        topic: 'search',
        priority: 5,
        timeLimitMs: 45000,
        status: 'queued',
        lease: { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 1, ended: { reason: 'maxHold', at: 1_045_000 } },
        budget: { cpuSeconds: { used: 100, reserved: 0, limit: 3600, remaining: 3500 } },
        retries: { total: 1, classAttempts: { timeout: 1, invalidOutput: 0, rejected: 0 }, bestScore: null, exhausted: false, stuck: false },
      });
      assert.deepEqual([claimNext(other, 'search')?.id, claimNext(other, 'search')?.id], ['mine', 'index']);
      assert.equal(engine.instances.get(alice, 'jobs', 'reindex')?.data.status, 'done');
    });

    test("it runs the fixture's worker type as a schema of its own: a missed worker's live lease is spared, and goes when it runs out", () => {
      const clock = new Clock(1_000_000);
      const engine = openTestEngine({ driver, clock: clock.now, runner: { principal: { subject: 'runner', permissions: ['jobs.override'] } } });
      engine.schemas.define(alice, jobsFixture());
      engine.schemas.publish(alice, 'jobs');
      engine.schemas.define(alice, fixtureType('workers', 'worker'));
      engine.schemas.publish(alice, 'workers');
      engine.runner.runDue();

      engine.instances.create(alice, 'workers', { subject: 'wren', name: 'Wren' }, { id: 'w1' });
      engine.instances.create(alice, 'jobs', { title: 'index', topic: 'search', priority: 5 }, { id: 'index' });
      const claimNext = (who: Principal) =>
        (engine.instances.invokeSchema(who, 'jobs', 'claimNext', { match: { topic: 'search' } }) as { claimed: { id: string } | null }).claimed?.id;
      clock.advance(10000);
      assert.deepEqual(engine.instances.invoke(worker, 'workers', 'w1', 'beat'), { deadline: 1_040_000 });
      assert.equal(claimNext(worker), 'index');
      clock.advance(20000);
      engine.instances.invoke(worker, 'jobs', 'index', 'heartbeat', {}, fenced(1));

      // wren stops beating its worker but renews its job's lease: the
      // runner misses the worker, and the miss spares the lease, renewed
      // after the worker's last beat, so live work is not lost.
      clock.advance(10000);
      engine.runner.runDue();
      assert.equal(engine.instances.get(alice, 'workers', 'w1')?.data.status, 'missing');
      assert.deepEqual((engine.instances.get(alice, 'workers', 'w1')?.data.presence as { released: unknown }).released, { jobs: [] });
      assert.equal((engine.instances.get(alice, 'jobs', 'index')?.data.lease as { holder: unknown }).holder, 'wren');
      // Its heartbeats stop too: the lease runs out on its own, at
      // 1_060_000, and the runner's sweep puts the job back.
      clock.advance(20000);
      engine.runner.runDue();
      assert.deepEqual(engine.instances.get(alice, 'jobs', 'index')?.data.lease, {
        holder: null,
        token: 2,
        acquiredAt: null,
        renewedAt: null,
        expiresAt: null,
        active: false,
        expiries: 1,
        ended: { reason: 'ttl', at: 1_060_000 },
      });
      assert.equal(engine.instances.get(alice, 'jobs', 'index')?.data.status, 'queued');
      // The expiry settled the claim's reservation.
      assert.deepEqual(engine.instances.get(alice, 'jobs', 'index')?.data.budget, { cpuSeconds: { used: 0, reserved: 0, limit: 3600, remaining: 3600 } });
      // Queue's copies followed the expiry, so claimNext finds the job again.
      assert.equal(claimNext(other), 'index');
    });

    test("it runs the fixture's batch and step types, each as a schema of its own: a batch stamps its steps", () => {
      const engine = openTestEngine({ driver });
      for (const [name, file] of [
        ['steps', 'step'],
        ['batches', 'batch'],
      ]) {
        engine.schemas.define(alice, fixtureType(name, file));
        engine.schemas.publish(alice, name);
      }
      engine.instances.create(alice, 'batches', { title: 'Search the archive', topic: 'search' }, { id: 'b1' });
      engine.instances.create(alice, 'batches', { title: 'Copy the archive', topic: 'copy' }, { id: 'b2' });
      const stamped = (id: string) =>
        (engine.instances.get(alice, 'batches', id)?.data.blueprint as { children: Array<{ key: string; id: string }> }).children.map((child) => {
          const step = engine.instances.get(alice, 'steps', child.id)?.data as { step: string; topic: string; blocked: boolean };
          return [step.step, step.topic, step.blocked];
        });
      assert.deepEqual(stamped('b1'), [
        ['fetch', 'search', false],
        ['check', 'search', true],
        ['index', 'search', true],
      ]);
      assert.deepEqual(stamped('b2'), [
        ['fetch', 'copy', false],
        ['index', 'copy', true],
      ]);
    });
  });
}
