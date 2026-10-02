// The package as a deployment uses it: its behaviors register with an
// engine through the plug-in interface, carry the declarations the core
// binary writes, and run the document the core binary builds in the CLI
// smoke (fixture-workqueue-json), whose jobs compose Workflow, Lease,
// Assignment and Queue. Queue is declared by the core but not implemented
// here yet, so the engine refuses the document until it is taken out.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import { SchemaDocumentError, type Principal } from '@superschematic/engine';

import { DEFAULT_TTL_MS, assignment, lease, workQueueBehaviors } from '../dist/index.js';
import { Clock, alice, cleanup, drivers, jobsFixture, openTestEngine, thrown } from './helpers.ts';

afterEach(cleanup);

const worker: Principal = { subject: 'wren', permissions: [] };
const sweeper: Principal = { subject: 'sweeper', permissions: [] };
const lead: Principal = { subject: 'lena', permissions: ['jobs.assign'] };

function declarationFile(name: string): unknown {
  return JSON.parse(readFileSync(new URL(`../src/declarations/${name}.behavior.json`, import.meta.url), 'utf8'));
}

for (const driver of drivers) {
  describe(`the work-queue package (${driver})`, () => {
    test('its behaviors carry the core declarations and register with an engine, beside the core behaviors', () => {
      assert.deepEqual(workQueueBehaviors, [lease, assignment]);
      assert.deepEqual(lease.declaration, declarationFile('Lease'));
      assert.deepEqual(assignment.declaration, declarationFile('Assignment'));
      assert.equal(DEFAULT_TTL_MS, 60000);
      const engine = openTestEngine({ driver });
      assert.deepEqual(engine.behaviors.names(), ['Assignment', 'Comments', 'Dependencies', 'Lease', 'Links', 'Reactions', 'Revisions', 'Rollups', 'Search', 'Workflow']);
    });

    test('it runs the document the core binary builds, once the Queue it does not implement yet is taken out', () => {
      const clock = new Clock(1_000_000);
      const engine = openTestEngine({ driver, clock: clock.now });
      const fixture = jobsFixture() as { name: string; types: { Job: { behaviors: Array<{ name: string }> } } };
      const refused = thrown(() => engine.schemas.define(alice, fixture), SchemaDocumentError);
      assert.match(refused.message, /behavior Queue on type Job: no implementation registered/);

      fixture.types.Job.behaviors = fixture.types.Job.behaviors.filter((behavior) => behavior.name !== 'Queue');
      engine.schemas.define(alice, fixture);
      engine.schemas.publish(alice, 'jobs');
      const described = engine.tools.describe(alice, 'jobs');
      assert.deepEqual(
        described.behaviors.map((behavior) => [behavior.name, behavior.operations]),
        [
          ['Workflow', ['transition']],
          ['Lease', ['acquire', 'heartbeat', 'release', 'expire', 'direct', 'acknowledge', 'resetExpiries']],
          ['Assignment', ['assign', 'unassign']],
        ]
      );
      // The config a client reads its lease length from.
      assert.equal((described.behaviors[1].config as { ttlMs: number }).ttlMs, 30000);

      engine.instances.create(alice, 'jobs', { title: 'Index the archive', topic: 'search', priority: 5, timeLimitMs: 45000 }, { id: 'job-1' });
      engine.instances.invoke(lead, 'jobs', 'job-1', 'assign', { to: 'wren' });
      assert.deepEqual(engine.instances.invoke(worker, 'jobs', 'job-1', 'acquire'), { token: 1, expiresAt: 1_030_000, heartbeatMs: 10000 });
      engine.instances.invoke(worker, 'jobs', 'job-1', 'transition', { to: 'running' });
      clock.advance(20000);
      assert.equal((engine.instances.invoke(worker, 'jobs', 'job-1', 'heartbeat', { token: 1 }) as { expiresAt: number }).expiresAt, 1_045_000);
      // The time limit holds: past it the lease expires and the job goes back to the queue.
      clock.advance(25000);
      assert.deepEqual(engine.instances.invoke(sweeper, 'jobs', 'job-1', 'expire'), { expired: true });
      assert.deepEqual(engine.instances.get(alice, 'jobs', 'job-1')?.data, {
        title: 'Index the archive',
        topic: 'search',
        priority: 5,
        timeLimitMs: 45000,
        status: 'queued',
        lease: { holder: null, token: 2, acquiredAt: null, expiresAt: null, active: false, expiries: 1 },
        assignee: 'wren',
      });
    });
  });
}
