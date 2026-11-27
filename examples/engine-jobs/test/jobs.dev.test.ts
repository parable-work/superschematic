// The job runner end to end on development mode (D117), as
// test/jobs.test.ts runs it on the engine: @superschematic/rules-handler
// serves schemas/dev over a SQLite database in memory (bun:sqlite under
// Bun, node:sqlite under Node.js), on a clock the test moves, and the test
// drives its routes through dev.http as the callers of src/auth.ts, under
// the same policy written as data (src/dev.ts). It holds what the engine
// holds where development mode answers alike: a worker claims the most
// urgent job of its topic and finishes it, a held job is its holder's
// alone, an operator's cancel reaches the holder as a directive and fails
// the job, a worker that stops beating is missed and loses its job to
// another, whose new token fences the old one, and a batch stamps a job per
// step (Blueprint on the core, D149), each claimed in its order, a
// transient failure retried, until the runner's Reactions settle the batch.
// A claim is the declared read of the queued jobs and Lease's acquire
// (D104), and the search is a list's text filter. src/dev.ts lists what
// differs.
import assert from 'node:assert/strict';
import { afterEach, test } from 'node:test';

import type { Dev, SqliteConnection } from '@superschematic/rules-handler';

import { claim, devClient, fail, finish, openJobsDev, type DevClient } from '../src/dev.ts';

/** A SQLite database in memory: bun:sqlite's under Bun, node:sqlite's under Node.js. */
async function memory(): Promise<SqliteConnection & { close(): void }> {
  if ((globalThis as { Bun?: unknown }).Bun !== undefined) {
    const { Database } = await import('bun:sqlite' as string);
    return new Database(':memory:');
  }
  const { DatabaseSync } = await import('node:sqlite' as string);
  return new DatabaseSync(':memory:');
}

const opened: Array<() => void> = [];

afterEach(() => {
  for (const close of opened.splice(0)) {
    close();
  }
});

/** A clock the test moves by hand; the host reads it for leases, deadlines and the runner. */
class Clock {
  ms = 1_000_000;
  readonly now = (): number => this.ms;
}

/** The ISO text a date-time field holds for an instant. */
const iso = (ms: number): string => new Date(ms).toISOString();

/** start serves the schemas on development mode, and gives the operator's and each worker's client. */
async function start() {
  const clock = new Clock();
  const connection = await memory();
  opened.push(() => connection.close());
  const dev: Dev = await openJobsDev(connection, clock.now);
  return {
    dev,
    clock,
    operator: devClient(dev, 'operator'),
    worker1: devClient(dev, 'worker-1'),
    worker2: devClient(dev, 'worker-2'),
  };
}

/** create queues a job as the operator, and resolves to its key, which the host makes. */
async function create(operator: DevClient, data: Record<string, unknown>): Promise<string> {
  const created = await operator.call('POST', '/schemas/Job/instances', { data });
  assert.equal(created.status, 201, JSON.stringify(created.body));
  return created.body.id as string;
}

/** job reads a job's row as the operator. */
async function job(operator: DevClient, id: string): Promise<Record<string, any>> {
  const read = await operator.call('GET', `/schemas/Job/instances/${id}`);
  assert.equal(read.status, 200, JSON.stringify(read.body));
  return read.body as Record<string, any>;
}

/** beat beats the worker's own Worker row. */
async function beat(worker: DevClient): Promise<void> {
  const beaten = await worker.call('POST', `/schemas/Worker/instances/${worker.subject}/operations/beat`, {});
  assert.equal(beaten.status, 200, JSON.stringify(beaten.body));
}

test('a worker claims the most urgent job of its topic, works it and finishes it', async () => {
  const { operator, worker1 } = await start();
  const nightly = await create(operator, { title: 'Rebuild the nightly index', topic: 'search', priority: 1 });
  const catalog = await create(operator, { title: 'Reindex the catalog', topic: 'search', priority: 9 });
  await create(operator, { title: 'Send the digest', topic: 'mail', priority: 20 });
  // A worker does the work, and queues none.
  const forbidden = await worker1.call('POST', '/schemas/Job/instances', { data: { title: 'Mine' } });
  assert.equal(forbidden.status, 403);

  await beat(worker1);
  const claimed = await claim(worker1, 'search');
  assert.deepEqual([claimed?.id, claimed?.token], [catalog, 1]);
  await finish(worker1, claimed!, 'Reindexed 1200 products', 45);

  const done = await job(operator, catalog);
  assert.deepEqual(
    [done.title, done.topic, done.priority, done.workflowStatus, done.leaseActive, done.leaseToken, done.leaseEnded],
    ['Reindex the catalog', 'search', 9, 'done', false, 2, { reason: 'release', at: 1_000_000 }]
  );
  // A free lease has no holder, and a job no batch stamped no batch.
  assert.equal(done.leaseHolder, undefined);
  assert.equal(done.batchId, undefined);
  assert.deepEqual(done.budgetMeters, { cpuSeconds: { used: 45, reserved: 0, limit: 600, remaining: 555 } });
  assert.deepEqual(
    [done.retriesTotal, done.retriesClassAttempts, done.retriesExhausted, done.dependenciesBlocked],
    [0, { cancelled: 0, invalid: 0, transient: 0 }, false, false]
  );
  assert.equal((await job(operator, nightly)).workflowStatus, 'queued');
  // The worker beat its Worker row before it claimed.
  const presence = await operator.call('GET', '/schemas/Worker/instances/worker-1');
  assert.deepEqual([presence.body.workflowStatus, presence.body.presenceDeadline], ['active', iso(1_015_000)]);

  // The engine's Search indexed a job's title and result; development mode
  // filters a field's text, matched anywhere, case aside, with no weights
  // and no ranking (searchText waits for D62's search fields).
  const hits = await operator.call('GET', '/schemas/Job/instances?filter[title][contains]=CATALOG');
  assert.deepEqual(
    hits.body.items.map((hit: { id: string }) => hit.id),
    [catalog]
  );
});

test('a held job belongs to its holder alone', async () => {
  const { operator, worker1, worker2 } = await start();
  const digest = await create(operator, { title: 'Send the digest', topic: 'mail' });
  const held = await claim(worker1, 'mail');
  assert.deepEqual([held?.id, held?.token], [digest, 1]);

  // The read passes over a running job, and an acquire of it is refused.
  assert.equal(await claim(worker2, 'mail'), null);
  const acquired = await worker2.job(digest, 'acquire');
  assert.deepEqual([acquired.status, acquired.body.details?.vetoCode], [409, 'held_by_another']);

  // The token fences one principal's processes from each other; who holds
  // the lease fences principals. Knowing the token gets worker-2 nowhere.
  const finished = await worker2.job(digest, 'transition', { to: 'done' }, held!.token);
  assert.deepEqual([finished.status, finished.body.details?.behavior, finished.body.details?.vetoCode], [409, 'Lease', 'held_by_another']);

  await finish(worker1, held!, 'Sent', 1);
  assert.equal((await job(operator, digest)).workflowStatus, 'done');
});

test('an operator cancels a running job: the holder hears the directive and fails the job', async () => {
  const { operator, worker1 } = await start();
  const crawl = await create(operator, { title: 'Crawl the archive', topic: 'search' });
  const held = await claim(worker1, 'search');
  assert.equal(held?.id, crawl);

  // A directive goes to the lease's holder; its next heartbeat delivers it.
  const directed = await operator.job(crawl, 'direct', { name: 'cancel' });
  assert.equal(directed.status, 200, JSON.stringify(directed.body));
  const heartbeat = await worker1.job(crawl, 'heartbeat', {}, held!.token);
  assert.deepEqual(
    heartbeat.body.directives.map((directive: { name: string; createdBy: string }) => [directive.name, directive.createdBy]),
    [['cancel', 'operator']]
  );
  // The cancelled attempt is terminal: the job fails, and the release
  // that follows ends the lease.
  const recorded = await fail(worker1, held!, 'cancelled', 'Cancelled by an operator.');
  assert.deepEqual([recorded.body.exhausted, recorded.body.classAttempts.cancelled], [true, 1]);
  const failed = await job(operator, crawl);
  assert.deepEqual(
    [failed.workflowStatus, failed.leaseHolder, failed.leaseEnded?.reason, failed.retriesExhausted],
    ['failed', undefined, 'release', true]
  );
});

test('a worker that stops beating is missed and loses its job to another, and its stale token is refused', async () => {
  const { dev, clock, operator, worker1, worker2 } = await start();
  const crawl = await create(operator, { title: 'Crawl the archive', topic: 'search' });
  await beat(worker1);
  const first = await claim(worker1, 'search');
  assert.deepEqual([first?.id, first?.token], [crawl, 1]);

  // worker-1 stops beating and renews nothing. Past its deadline, 15
  // seconds after its last beat, the runner misses it, and the miss expires
  // the leases its principal holds on jobs, sooner than the lease's own 30
  // seconds would. worker-2 beats on.
  clock.ms += 16_000;
  await beat(worker2);
  dev.runDue();
  const missing = await operator.call('GET', '/schemas/Worker/instances/worker-1');
  assert.deepEqual([missing.body.workflowStatus, missing.body.presenceReleased], ['missing', { Job: [crawl] }]);
  const queued = await job(operator, crawl);
  assert.deepEqual(
    [queued.workflowStatus, queued.leaseToken, queued.leaseActive, queued.leaseExpiries, queued.leaseEnded],
    ['queued', 2, false, 1, { reason: 'holder', at: 1_016_000 }]
  );

  // worker-2 claims it again, under a new token.
  const second = await claim(worker2, 'search');
  assert.deepEqual([second?.id, second?.token], [crawl, 3]);

  // worker-1 comes back and reports with its old token: Lease refuses it.
  const late = await worker1.job(crawl, 'recordUsage', { meter: 'cpuSeconds', amount: 30 }, first!.token);
  assert.deepEqual([late.status, late.body.details?.vetoCode, late.body.details?.vetoDetails], [409, 'token_stale', { token: 1, current: 3 }]);

  // The job stays worker-2's, and worker-1 wrote nothing to it.
  await finish(worker2, second!, 'Crawled', 5);
  const done = await job(operator, crawl);
  assert.deepEqual([done.workflowStatus, done.budgetMeters.cpuSeconds.used], ['done', 5]);

  // A beat brings worker-1 back.
  await beat(worker1);
  assert.equal((await operator.call('GET', '/schemas/Worker/instances/worker-1')).body.workflowStatus, 'active');
});

test('a batch stamps a job per step, runs them in order, retries a transient failure, and settles when they finish', async () => {
  const { dev, operator, worker1, worker2 } = await start();
  const created = await operator.call('POST', '/schemas/Batch/instances', { data: { title: 'Nightly crawl', topic: 'search' } });
  assert.equal(created.status, 201, JSON.stringify(created.body));
  const batch = async (id: string) => (await operator.call('GET', `/schemas/Batch/instances/${id}`)).body as Record<string, any>;
  const nightly = created.body.id as string;

  // The batch's create stamped a job per step, in the template's order,
  // each copying the batch's topic and blocked by the one before. The batch
  // keeps none of it: a read lists the jobs that point at it.
  const children = (await batch(nightly)).blueprintChildren as Array<{ key: string; id: string }>;
  assert.deepEqual(
    children.map((child) => child.key),
    ['fetch', 'index', 'report']
  );
  const stamped = await Promise.all(children.map((child) => job(operator, child.id)));
  assert.deepEqual(
    stamped.map((row) => [row.step, row.title, row.topic, row.batchId, row.workflowStatus, row.dependenciesBlocked]),
    [
      ['fetch', 'Fetch the sources', 'search', nightly, 'queued', false],
      ['index', 'Index what was fetched', 'search', nightly, 'queued', true],
      ['report', 'Report what was indexed', 'search', nightly, 'queued', true],
    ]
  );
  // A stamped step stays the step it was stamped as.
  const renamed = await operator.call('PATCH', `/schemas/Job/instances/${children[1]!.id}`, { step: 'fetch' });
  assert.deepEqual([renamed.status, renamed.body.code], [422, 'invalid_instance']);

  // The worker claims them in order. index fails once, and goes back in
  // the queue.
  const steps: string[] = [];
  let failures = 1;
  for (;;) {
    const claimed = await claim(worker1, 'search');
    if (claimed === null) {
      break;
    }
    const step = claimed.job.step as string;
    steps.push(step);
    if (step === 'index' && failures-- > 0) {
      const recorded = await fail(worker1, claimed, 'transient', 'The source timed out.');
      assert.equal(recorded.body.hint, 'Try again; the source may be back.');
      continue;
    }
    await finish(worker1, claimed, `${step} ok`, 10);
  }
  assert.deepEqual(steps, ['fetch', 'index', 'index', 'report']);

  // The rollup counts the steps at each read; the runner's Reactions
  // settle the batch once every step succeeded.
  let read = await batch(nightly);
  assert.deepEqual([read.workflowStatus, read.rollupsValues], ['running', { steps: { done: 3 } }]);
  dev.runDue();
  read = await batch(nightly);
  assert.equal(read.workflowStatus, 'done');
  const indexed = await job(operator, children[1]!.id);
  assert.deepEqual(indexed.retriesClassAttempts, { cancelled: 0, invalid: 0, transient: 1 });

  // A step that fails for good fails its batch, and the steps after it are
  // never claimed: their gate holds them while it is not done.
  const broken = (await operator.call('POST', '/schemas/Batch/instances', { data: { title: 'Broken crawl', topic: 'mail' } })).body.id as string;
  const first = await claim(worker2, 'mail');
  assert.equal(first?.job.step, 'fetch');
  const recorded = await fail(worker2, first!, 'invalid', 'No such source.');
  assert.equal(recorded.body.exhausted, true);
  assert.equal(await claim(worker2, 'mail'), null);
  dev.runDue();
  read = await batch(broken);
  assert.deepEqual([read.workflowStatus, read.rollupsValues], ['failed', { steps: { failed: 1, queued: 2 } }]);
});
