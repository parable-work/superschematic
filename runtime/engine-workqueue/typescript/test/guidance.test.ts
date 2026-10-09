// The work-queue behaviors' guidance (runtime/engine/README.md,
// "Guidance"): what each says about the type under its config, and the
// refusals each adds to the operations it guards, which the describe
// document, the tools document and MCP _meta carry.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import type { Engine, ToolGuidance } from '@superschematic/engine';

import { alice, cleanup, jobFlow, jobsDocument, jobsFixture, openTestEngine, publish } from './helpers.ts';

afterEach(cleanup);

function guidanceOf(engine: Engine, schema: string, operation: string): ToolGuidance {
  const found = engine.tools.describe(alice, schema).operations.find((candidate) => candidate.name === operation);
  assert.ok(found, `${schema} has ${operation}`);
  return found.guidance;
}

function summaryOf(engine: Engine, schema: string, behavior: string): string {
  const found = engine.tools.describe(alice, schema).behaviors.find((candidate) => candidate.name === behavior);
  assert.ok(found?.summary, `${schema} ${behavior} has a summary`);
  return found.summary;
}

const codes = (guidance: ToolGuidance) => guidance.errors.map((error) => `${error.description.split(':')[0]}.${error.code}`);

/** fixtureType reads one type's file of fixture-workqueue-json as a schema of its own. */
function fixtureType(name: string, file: string): Record<string, unknown> {
  const type = JSON.parse(
    readFileSync(new URL(`../../../../internal/loader/testdata/services/fixture-workqueue-json/src/${file}.schema.json`, import.meta.url), 'utf8')
  ) as { name: string };
  return { kind: 'General', name, types: { [type.name]: type } };
}

describe('the work-queue behaviors say what their config means', () => {
  test('Lease gives its lengths, what an expiry does, and fences every write but the exempt ones', () => {
    const engine = openTestEngine();
    publish(engine, jobsFixture());
    const summary = summaryOf(engine, 'jobs', 'Lease');
    assert.match(summary, /for 30 seconds at a time, renewed by heartbeat at least every 10 seconds, and held at most the instance's timeLimitMs milliseconds\./);
    assert.match(summary, /The runner expires a lapsed lease within 5 seconds; an expiry moves status from running to queued\./);
    assert.match(summary, /After 3 expiries it is not leased again, and that expiry moves status from running to failed instead\./);
    assert.match(guidanceOf(engine, 'jobs', 'heartbeat').useWhen, /^Use as the holder at least every 10 seconds, presenting the token/);
    assert.match(guidanceOf(engine, 'jobs', 'acquire').success, /present the token as preconditions \{"Lease": \{"token": n\}\} on each write/);
    // Lease's guard holds the type's writes: Workflow's transition and Lease's own update rule among them.
    assert.deepEqual(codes(guidanceOf(engine, 'jobs', 'update')), ['Lease.held_by_another', 'Lease.token_stale', 'Lease.lapsed', 'Lease.hold_limit_fixed']);
    assert.ok(codes(guidanceOf(engine, 'jobs', 'transition')).includes('Lease.held_by_another'));
    assert.ok(!codes(guidanceOf(engine, 'jobs', 'refresh')).includes('Lease.held_by_another'), "Queue's refresh is let through");
    assert.ok(!codes(guidanceOf(engine, 'jobs', 'countClaimable')).includes('Lease.held_by_another'), 'a read is let through');
    assert.ok(!codes(guidanceOf(engine, 'jobs', 'create')).includes('Lease.held_by_another'), 'a create holds no lease');

    // Another config: exempt operations pass, and requireToken asks every write for the token.
    publish(
      engine,
      jobsDocument([
        { name: 'Workflow', config: jobFlow },
        { name: 'Comments' },
        { name: 'Lease', config: { ttlMs: 120000, heartbeatMs: 20000, exempt: ['Comments.comment'], requireToken: true } },
      ])
    );
    assert.match(summaryOf(engine, 'Job', 'Lease'), /for 2 minutes at a time, renewed by heartbeat at least every 20 seconds\./);
    assert.match(summaryOf(engine, 'Job', 'Lease'), /only its holder writes, but for Comments\.comment;/);
    assert.deepEqual(codes(guidanceOf(engine, 'Job', 'comment')), []);
    assert.deepEqual(codes(guidanceOf(engine, 'Job', 'transition')).filter((code) => code.startsWith('Lease.')), [
      'Lease.held_by_another',
      'Lease.token_stale',
      'Lease.lapsed',
      'Lease.token_required',
    ]);
    assert.match(guidanceOf(engine, 'Job', 'resetExpiries').doNotUseWhen, /names no overridePermission/);
  });

  test('Queue names its claim states, its order, its match fields and what keeps work out; acquire gives way to the claim', () => {
    const engine = openTestEngine();
    publish(engine, jobsFixture());
    const summary = summaryOf(engine, 'jobs', 'Queue');
    assert.match(summary, /^A claim takes an instance in queued: its lease, its budget reservation, and a move to running, in one write\./);
    assert.match(summary, /claimNext claims the first the caller can, highest priority first, then oldest first, matching topic, trying at most 100\./);
    assert.match(summary, /not claimed while it is exhausted \(Retries\), over its budget, here or in an enclosing scope \(Budget\), at 3 lease expiries or assigned to another principal\./);
    assert.match(guidanceOf(engine, 'jobs', 'claimNext').useWhen, /match filters on topic, each a value or a list of values\. assignedOnly takes only work assigned to you\./);
    // A claim meets its own refusals and those of what it runs: the lease, the assignment, the budget, the retries.
    assert.deepEqual(codes(guidanceOf(engine, 'jobs', 'claim')), [
      'Queue.not_claimable',
      'Lease.held_by_another',
      'Lease.token_stale',
      'Lease.lapsed',
      'Assignment.assigned_to_another',
      'Budget.over_limit',
      'Budget.scope_moved',
      'Retries.exhausted',
    ]);
    const acquire = guidanceOf(engine, 'jobs', 'acquire');
    assert.ok(codes(acquire).includes('Queue.claim_required'));
    assert.match(acquire.doNotUseWhen, /Do not use: on Job the lease is taken by claim or claimNext\./);

    publish(
      engine,
      jobsDocument([
        { name: 'Workflow', config: jobFlow },
        { name: 'Dependencies' },
        { name: 'Lease' },
        { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' } } },
      ])
    );
    assert.match(summaryOf(engine, 'Job', 'Queue'), /claimNext claims the first the caller can, oldest first, trying at most 100\. An instance is not claimed while it is blocked\./);
    assert.deepEqual(codes(guidanceOf(engine, 'Job', 'claim')).slice(0, 2), ['Queue.not_claimable', 'Queue.blocked']);
  });

  test('Retries gives its classes and caps, Budget its meters and scopes, Assignment who may assign whom', () => {
    const engine = openTestEngine();
    publish(engine, jobsFixture());
    assert.match(
      summaryOf(engine, 'jobs', 'Retries'),
      /^Failure classes: invalidOutput \(2 attempts\), rejected \(terminal: one failure exhausts\) and timeout \(3 attempts\); at most 4 counted failures in all\./
    );
    assert.match(summaryOf(engine, 'jobs', 'Retries'), /Once exhausted, status moves from queued or running to failed/);
    assert.ok(codes(guidanceOf(engine, 'jobs', 'transition')).includes('Retries.exhausted'));
    assert.match(summaryOf(engine, 'jobs', 'Budget'), /^Meters: cpuSeconds \(limit 3600; reserving 600 at a claim; counted per UTC day\)\./);
    assert.match(guidanceOf(engine, 'jobs', 'setLimit').useWhen, /needs permission jobs\.budget/);
    assert.deepEqual(codes(guidanceOf(engine, 'jobs', 'reserve')).slice(0, 3), ['Budget.over_limit', 'Budget.scope_moved', 'Budget.not_leased']);
    assert.match(summaryOf(engine, 'jobs', 'Assignment'), /need permission jobs\.assign\.$/);
    assert.ok(codes(guidanceOf(engine, 'jobs', 'acquire')).includes('Assignment.assigned_to_another'));

    // A scope link: Budget holds Links' link and unlink of it while a reservation is held through it.
    publish(engine, jobsDocument([{ name: 'Budget', config: { meters: { cpu: { limit: 100 } } } }], 'Pool'));
    publish(
      engine,
      jobsDocument([
        { name: 'Links', config: { links: { pool: { schema: 'Pool' } } } },
        { name: 'Budget', config: { meters: { cpu: { reserveField: 'timeLimitMs', scope: 'pool' } } } },
      ])
    );
    assert.match(summaryOf(engine, 'Job', 'Budget'), /cpu \(no limit; reserving the instance's timeLimitMs at a claim; drawn from the scope its pool link points at\)/);
    assert.deepEqual(codes(guidanceOf(engine, 'Job', 'link')), ['Budget.scope_reserved']);
    assert.match(guidanceOf(engine, 'Job', 'setLimit').doNotUseWhen, /names no limitPermission/);
  });

  test('Presence names who beats an instance and what a miss does; Blueprint what a stamp creates', () => {
    const engine = openTestEngine();
    publish(engine, jobsFixture());
    for (const [name, file] of [
      ['workers', 'worker'],
      ['steps', 'step'],
      ['batches', 'batch'],
    ]) {
      publish(engine, fixtureType(name, file));
    }
    const presence = summaryOf(engine, 'workers', 'Presence');
    assert.match(presence, /stands for the principal subject names, which beats it at least every 30 seconds/);
    assert.match(presence, /A miss moves status from idle or busy to missing\. A miss expires the principal's leases on jobs, but those renewed since its last beat\./);
    assert.deepEqual(codes(guidanceOf(engine, 'workers', 'beat')), ['Presence.not_principal', 'Presence.no_principal']);
    assert.deepEqual(codes(guidanceOf(engine, 'workers', 'update')), ['Presence.principal_fixed']);

    const blueprint = summaryOf(engine, 'batches', 'Blueprint');
    assert.match(blueprint, /^The create of a Batch stamps a child per step, /);
    assert.match(blueprint, /Each child is a steps instance whose step holds its step's key and whose batch links to its parent, copying topic;/);
    const create = guidanceOf(engine, 'batches', 'create');
    assert.match(create.success, /The create stamps the children \(.*\) in its own write; behaviors\.Blueprint\.children lists them\./);
    assert.deepEqual(codes(create), ['Blueprint.no_dependencies', 'Blueprint.not_constant']);
  });

  test("every operation of the work-queue behaviors says when to use it or not, in plain ASCII, and the tools document and _meta carry it", () => {
    const engine = openTestEngine();
    publish(engine, jobsFixture());
    for (const [name, file] of [
      ['workers', 'worker'],
      ['steps', 'step'],
      ['batches', 'batch'],
    ]) {
      publish(engine, fixtureType(name, file));
    }
    const manifest = engine.tools.manifest(alice);
    for (const schema of ['jobs', 'workers', 'steps', 'batches']) {
      const document = engine.tools.describe(alice, schema);
      for (const operation of document.operations) {
        const { useWhen, doNotUseWhen, success, errors } = operation.guidance;
        assert.ok(useWhen !== '' || doNotUseWhen !== '', `${schema}.${operation.name} says when to use it`);
        for (const text of [useWhen, doNotUseWhen, success, ...errors.flatMap((error) => [error.description, error.commonCorrection])]) {
          assert.ok(/^[\x20-\x7e]*$/.test(text), `${schema}.${operation.name}: plain ASCII, ${JSON.stringify(text)}`);
        }
        const entry = manifest.tools.find((tool) => tool.name === operation.tool);
        assert.ok(entry && !entry.mcp.hidden, operation.tool);
        assert.deepEqual([entry.guidance, (entry.mcp._meta as Record<string, unknown>)['superschematic/operation-guidance']], [operation.guidance, operation.guidance]);
      }
    }
  });
});
