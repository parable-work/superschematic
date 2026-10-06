// The jobs server end to end, over a real listening server
// (@hono/node-server on Node.js and on Bun), driven by the example's
// worker: a worker claims the most urgent job and finishes it, a held job
// is its holder's alone, a worker that stops beating loses its job to
// another and its stale token is refused, and a batch's steps run in order,
// a transient failure retried, until the runner settles the batch. Each
// test opens its own server on a clock it moves, and runs the runner's
// sweeps and reactions itself (runner.runDue()), so nothing waits on time.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import type { EngineEvent } from '@superschematic/engine';

import { jobsApp, listen, openJobs } from '../src/server.ts';
import { Client, JobFailure, JobWorker, Refused, type Handler } from '../src/worker.ts';

const opened: Array<() => Promise<void>> = [];

afterEach(async () => {
  for (const close of opened.splice(0)) {
    await close();
  }
});

/** A clock the test moves by hand; the engine reads it for leases, deadlines and sweeps. */
class Clock {
  ms = 1_000_000;
  readonly now = (): number => this.ms;
}

/** start opens a fresh server on a moved clock, with a client for the operator and each worker. */
async function start() {
  const directory = mkdtempSync(join(tmpdir(), 'engine-jobs-'));
  const clock = new Clock();
  const engine = openJobs(join(directory, 'jobs.db'), { clock: clock.now });
  // The runner finds each sweep at its first pass and runs it an interval later.
  engine.runner.runDue();
  const server = await listen(jobsApp(engine));
  opened.push(async () => {
    await server.close();
    engine.close();
    rmSync(directory, { recursive: true, force: true });
  });
  const api = `${server.url}/api/namespaces/default`;
  return {
    engine,
    clock,
    operator: new Client(api, 'operator-token'),
    worker1: new JobWorker(api, 'worker-1', 'worker-1-token'),
    worker2: new JobWorker(api, 'worker-2', 'worker-2-token'),
  };
}

/** refusal awaits a call that must be refused, and returns the refusal. */
async function refusal(call: Promise<unknown>): Promise<Refused> {
  const error = await call.then(
    () => assert.fail('expected a refusal'),
    (error: unknown) => error
  );
  assert.ok(error instanceof Refused, String(error));
  return error;
}

test('a worker claims the most urgent job, works it and finishes it', async () => {
  const { operator, worker1 } = await start();
  await operator.call('POST', '/schemas/jobs/instances', { id: 'nightly', data: { title: 'Rebuild the nightly index', topic: 'search', priority: 1 } });
  await operator.call('POST', '/schemas/jobs/instances', { id: 'catalog', data: { title: 'Reindex the catalog', topic: 'search', priority: 9 } });
  // A worker does the work, and queues none.
  const forbidden = await refusal(worker1.call('POST', '/schemas/jobs/instances', { data: { title: 'Mine' } }));
  assert.equal(forbidden.status, 403);

  const stop = new AbortController();
  const worked: string[] = [];
  await worker1.run(
    async ({ id, recordUsage }) => {
      worked.push(id);
      await recordUsage(45);
      // One job is enough: the loop ends once this one is finished.
      stop.abort();
      return 'Reindexed 1200 products';
    },
    { topic: 'search', signal: stop.signal }
  );
  assert.deepEqual(worked, ['catalog']);

  const catalog = await operator.call('GET', '/schemas/jobs/instances/catalog');
  assert.deepEqual(catalog.data, {
    title: 'Reindex the catalog',
    topic: 'search',
    priority: 9,
    result: 'Reindexed 1200 products',
    status: 'done',
    lease: { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: { reason: 'release', at: 1_000_000 } },
    budget: { cpuSeconds: { used: 45, reserved: 0, limit: 600, remaining: 555 } },
    retries: { total: 0, classAttempts: { invalid: 0, transient: 0 }, bestScore: null, exhausted: false, stuck: false },
    blocked: false,
  });
  assert.equal((await operator.call('GET', '/schemas/jobs/instances/nightly')).data.status, 'queued');
  const worker = await operator.call('GET', '/schemas/workers/instances/worker-1');
  assert.equal(worker.data.status, 'active');
  assert.equal(worker.data.presence.deadline, 1_015_000);

  // Search indexes a job's title and the result its worker reported.
  const hits = await operator.call('POST', '/schemas/jobs/operations/search', { query: 'products' });
  assert.deepEqual(hits.items.map((hit: { id: string; field: string }) => [hit.id, hit.field]), [['catalog', 'result']]);
});

test('a held job belongs to its holder alone', async () => {
  const { operator, worker1, worker2 } = await start();
  await operator.call('POST', '/schemas/jobs/instances', { id: 'digest', data: { title: 'Send the digest', topic: 'mail' } });
  const claim = await worker1.claimNext();
  assert.deepEqual(claim, { id: 'digest', token: 1, expiresAt: 1_030_000, heartbeatMs: 10000 });

  // claimNext passes over a held job, and a claim of it is refused.
  assert.equal(await worker2.claimNext(), null);
  const claimed = await refusal(worker2.call('POST', '/schemas/jobs/instances/digest/operations/claim', {}));
  assert.equal(claimed.status, 409);
  assert.equal(claimed.details.code, 'held_by_another');

  // The token fences one principal's processes from each other; who holds
  // the lease fences principals. Knowing the token gets worker-2 nowhere.
  const finished = await refusal(worker2.operate(claim!, 'transition', { to: 'done' }));
  assert.deepEqual([finished.code, finished.details.behavior, finished.details.code], ['vetoed', 'Lease', 'held_by_another']);

  assert.equal(await worker1.work(claim!, async () => 'Sent'), 'done');
});

test('a worker that stops beating loses its job to another, and its stale token is refused', async () => {
  const { engine, clock, operator, worker1, worker2 } = await start();
  await operator.call('POST', '/schemas/jobs/instances', { id: 'crawl', data: { title: 'Crawl the archive', topic: 'search' } });
  await worker1.beat();
  const claim = (await worker1.claimNext('search'))!;

  // worker-1 dies holding the job: it beats no more and renews nothing.
  // Past its worker's deadline, 15 seconds after its last beat, the
  // runner's sweep misses it, and the miss expires the leases its principal
  // holds on jobs, sooner than the lease's own 30 seconds would. worker-2
  // beats on.
  clock.ms += 16_000;
  await worker2.beat();
  engine.runner.runDue();
  const missing = await operator.call('GET', '/schemas/workers/instances/worker-1');
  assert.equal(missing.data.status, 'missing');
  assert.deepEqual(missing.data.presence.released, { jobs: ['crawl'] });
  const crawl = await operator.call('GET', '/schemas/jobs/instances/crawl');
  assert.equal(crawl.data.status, 'queued');
  assert.deepEqual(crawl.data.lease, { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 1, ended: { reason: 'holder', at: 1_016_000 } });

  // worker-2 claims it again, under a new token.
  const taken = (await worker2.claimNext('search'))!;
  assert.equal(taken.token, 3);

  // worker-1 comes back and renews with its old token: Lease refuses it,
  // whoever presents it.
  const stale = await refusal(worker1.heartbeat(claim));
  assert.deepEqual(
    [stale.status, stale.code, stale.details.behavior, stale.details.code, stale.details.details],
    [409, 'vetoed', 'Lease', 'token_stale', { token: 1, current: 3 }]
  );
  // Its work ends there: the result it reports is refused, and the job stays worker-2's.
  assert.equal(await worker1.work(claim, async () => 'Crawled late'), 'lost');
  assert.equal(await worker2.work(taken, async () => 'Crawled'), 'done');
  assert.equal((await operator.call('GET', '/schemas/jobs/instances/crawl')).data.result, 'Crawled');

  // A beat brings worker-1 back.
  await worker1.beat();
  assert.equal((await operator.call('GET', '/schemas/workers/instances/worker-1')).data.status, 'active');
});

test('a batch runs its steps in order, retries a transient failure, and settles when they finish', async () => {
  const { engine, operator, worker1, worker2 } = await start();
  await operator.call('POST', '/schemas/batches/instances', { id: 'nightly', data: { title: 'Nightly crawl', topic: 'search' } });

  // The batch's create stamped a job per step, each blocked by the one
  // before, so the worker claims them in order. index fails once.
  let failures = 1;
  const steps: string[] = [];
  const handler: Handler = async ({ job }) => {
    steps.push(job.step!);
    if (job.step === 'index' && failures-- > 0) {
      throw new JobFailure('transient', 'The source timed out.');
    }
    return `${job.step} ok`;
  };
  const outcomes: string[] = [];
  for (let next; (next = await worker1.runOnce(handler, 'search')) !== null; ) {
    outcomes.push(next.outcome);
  }
  assert.deepEqual(steps, ['fetch', 'index', 'index', 'report']);
  assert.deepEqual(outcomes, ['done', 'retry', 'done', 'done']);

  // The rollup counts the steps at each read; the runner's Reactions
  // settle the batch once every step succeeded.
  let batch = await operator.call('GET', '/schemas/batches/instances/nightly');
  assert.deepEqual([batch.data.status, batch.data.rollups], ['running', { steps: { done: 3 } }]);
  engine.runner.runDue();
  batch = await operator.call('GET', '/schemas/batches/instances/nightly');
  assert.equal(batch.data.status, 'done');
  const index = batch.data.blueprint.children.find((child: { key: string }) => child.key === 'index');
  const indexed = await operator.call('GET', `/schemas/jobs/instances/${index.id}`);
  assert.deepEqual(indexed.data.retries.classAttempts, { invalid: 0, transient: 1 });

  // A step that fails for good fails its batch, and the steps after it are never claimed.
  await operator.call('POST', '/schemas/batches/instances', { id: 'broken', data: { title: 'Broken crawl', topic: 'mail' } });
  const first = await worker2.runOnce(async () => {
    throw new JobFailure('invalid', 'No such source.');
  }, 'mail');
  assert.equal(first?.outcome, 'failed');
  assert.equal(await worker2.runOnce(handler, 'mail'), null);
  engine.runner.runDue();
  batch = await operator.call('GET', '/schemas/batches/instances/broken');
  assert.deepEqual([batch.data.status, batch.data.rollups], ['failed', { steps: { failed: 1, queued: 2 } }]);

  // The runner moved the batch, as its own principal, and the event says why.
  const events: EngineEvent[] = (await operator.call('GET', '/events?after=0&schema=batches&behavior=Workflow')).events;
  assert.deepEqual(
    events.map((event) => [event.instanceId, event.actor, event.cause?.behavior, (event.change as { patch: unknown }).patch]),
    [
      ['nightly', 'runner', 'Reactions', { status: 'done' }],
      ['broken', 'runner', 'Reactions', { status: 'failed' }],
    ]
  );
});
