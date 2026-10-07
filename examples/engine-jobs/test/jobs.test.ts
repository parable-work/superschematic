// The jobs server end to end, over a real listening server
// (@hono/node-server on Node.js and on Bun), driven by the example's
// worker, a QueueWorker on the engine's client: a worker claims the most
// urgent job and finishes it, a held job is its holder's alone, an
// operator's cancel reaches the worker as a directive and fails the job, a
// worker cut off from the engine is missed and loses its job to another
// and its stale token is refused, and a batch's steps run in order, a
// transient failure retried, until the runner settles the batch. Each test
// opens its own server on a clock it moves, and runs the runner's sweeps
// and reactions itself (runner.runDue()), so nothing waits on the engine's
// time; the workers' heartbeats and beats run on short timers of this
// process's clock.
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, test } from 'node:test';

import { EngineClient, isProblem, isVeto, type EngineEvent, type EngineProblem, type FetchLike } from '@superschematic/engine/client';
import { LeaseLostError } from '@superschematic/engine-workqueue/worker';

import { jobsApp, listen, openJobs } from '../src/server.ts';
import { JobFailure, jobWorker, type Handler, type JobWorkerOptions } from '../src/worker.ts';

const opened: Array<() => Promise<void>> = [];

afterEach(async () => {
  // The workers stop before the server closes.
  for (const close of opened.splice(0).reverse()) {
    await close();
  }
});

/** A clock the test moves by hand; the engine reads it for leases, deadlines and sweeps. */
class Clock {
  ms = 1_000_000;
  readonly now = (): number => this.ms;
}

/** Heartbeats, beats and the idle backoff on short timers, so a test sees them without waiting. */
const quick: JobWorkerOptions = { heartbeatMs: 20, beatMs: 20, idle: { initialMs: 20, maxMs: 100 } };

/** A worker's way to the engine: cut it, and its requests fail as a dropped network's do. */
interface Network {
  up: boolean;
  /** Requests sent and not yet answered. */
  inflight: number;
}

/** start opens a fresh server on a moved clock, with a client for each caller and a way to start workers. */
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
  const baseUrl = `${server.url}/api`;
  /** client calls as the principal the token names, over the network given. */
  const client = (token: string, network: Network = { up: true, inflight: 0 }) => {
    const send: FetchLike = async (input, init) => {
      if (!network.up) {
        throw new TypeError('network down');
      }
      network.inflight++;
      try {
        return await fetch(input, init);
      } finally {
        network.inflight--;
      }
    };
    return new EngineClient({ baseUrl, auth: { token }, fetch: send });
  };
  return {
    engine,
    clock,
    client,
    operator: client('operator-token'),
    /** worker starts the worker of a subject, over a network of its own, and stops it after the test. */
    worker: async (subject: string, handler: Handler, options: JobWorkerOptions = {}) => {
      const network: Network = { up: true, inflight: 0 };
      const worker = jobWorker(client(`${subject}-token`, network), subject, handler, { ...quick, ...options });
      await worker.start();
      opened.push(() => worker.stop({ drain: false }));
      return { worker, network };
    },
  };
}

/** until waits for a condition, which may read the engine over HTTP. */
async function until(condition: () => boolean | Promise<boolean>, what: string, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!(await condition())) {
    if (Date.now() > deadline) {
      assert.fail(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

/** refusal awaits a call that must be refused, and returns the refusal. */
async function refusal(call: Promise<unknown>): Promise<EngineProblem> {
  const error = await call.then(
    () => assert.fail('expected a refusal'),
    (error: unknown) => error
  );
  assert.ok(isProblem(error), String(error));
  return error;
}

/** A read's data, untyped, for the assertions. */
type Data = Record<string, any>;

test('a worker claims the most urgent job, works it and finishes it', async () => {
  const { operator, client, worker } = await start();
  await operator.instances.create('jobs', { title: 'Rebuild the nightly index', topic: 'search', priority: 1 }, { id: 'nightly' });
  await operator.instances.create('jobs', { title: 'Reindex the catalog', topic: 'search', priority: 9 }, { id: 'catalog' });
  // A worker does the work, and queues none.
  const forbidden = await refusal(client('worker-1-token').instances.create('jobs', { title: 'Mine' }));
  assert.equal(forbidden.status, 403);

  let first!: () => void;
  const one = new Promise<void>((resolve) => (first = resolve));
  const worked: string[] = [];
  const { worker: worker1 } = await worker(
    'worker-1',
    async ({ id, recordUsage }) => {
      worked.push(id);
      await recordUsage(45);
      first();
      return 'Reindexed 1200 products';
    },
    { topic: 'search' }
  );
  // One job is enough: the stop claims nothing more, and waits for this one to finish.
  await one;
  await worker1.stop();
  assert.deepEqual(worked, ['catalog']);

  const catalog = await operator.instances.get<Data>('jobs', 'catalog');
  assert.deepEqual(catalog.data, {
    title: 'Reindex the catalog',
    topic: 'search',
    priority: 9,
    result: 'Reindexed 1200 products',
    status: 'done',
    lease: { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 0, ended: { reason: 'release', at: 1_000_000 } },
    budget: { cpuSeconds: { used: 45, reserved: 0, limit: 600, remaining: 555 } },
    retries: { total: 0, classAttempts: { cancelled: 0, invalid: 0, transient: 0 }, bestScore: null, exhausted: false, stuck: false },
    blocked: false,
  });
  assert.equal((await operator.instances.get<Data>('jobs', 'nightly')).data.status, 'queued');
  // The worker beat its worker instance before it claimed.
  const presence = await operator.instances.get<Data>('workers', 'worker-1');
  assert.equal(presence.data.status, 'active');
  assert.equal(presence.data.presence.deadline, 1_015_000);

  // Search indexes a job's title and the result its worker reported.
  const hits = await operator.instances.invokeSchema<{ items: Array<{ id: string; field: string }> }>('jobs', 'search', { query: 'products' });
  assert.deepEqual(
    hits.items.map((hit) => [hit.id, hit.field]),
    [['catalog', 'result']]
  );
});

test('a held job belongs to its holder alone', async () => {
  const { operator, client, worker } = await start();
  await operator.instances.create('jobs', { title: 'Send the digest', topic: 'mail' }, { id: 'digest' });
  let open = false;
  const { worker: worker1 } = await worker('worker-1', async () => {
    await until(() => open, 'the test to let the job finish');
    return 'Sent';
  });
  await until(() => worker1.active === 1, 'the claim');
  const [held] = worker1.jobs;
  assert.deepEqual(held?.claim, { id: 'digest', token: 1, expiresAt: 1_030_000, heartbeatMs: 10000 });

  // claimNext passes over a held job, and a claim of it is refused.
  const worker2 = client('worker-2-token');
  assert.deepEqual(await worker2.instances.invokeSchema('jobs', 'claimNext'), { claimed: null });
  const claimed = await refusal(worker2.instances.invoke('jobs', 'digest', 'claim'));
  assert.ok(isVeto(claimed, undefined, 'held_by_another') && claimed.status === 409, String(claimed));

  // The token fences one principal's processes from each other; who holds
  // the lease fences principals. Knowing the token gets worker-2 nowhere.
  const finished = await refusal(worker2.instances.invoke('jobs', 'digest', 'transition', { to: 'done' }, { preconditions: { Lease: { token: held!.token } } }));
  assert.ok(isVeto(finished, 'Lease', 'held_by_another'), String(finished));

  open = true;
  await until(async () => (await operator.instances.get<Data>('jobs', 'digest')).data.status === 'done', 'the job done');
});

test('an operator cancels a running job: the worker hears the directive, stops the handler and fails the job', async () => {
  const { operator, worker } = await start();
  await operator.instances.create('jobs', { title: 'Crawl the archive', topic: 'search' }, { id: 'crawl' });
  let stopped: unknown;
  const { worker: worker1 } = await worker('worker-1', async ({ signal }) => {
    // A long crawl, which stops when its signal aborts.
    await new Promise((_, reject) => signal.addEventListener('abort', () => reject((stopped = signal.reason)), { once: true }));
    return 'Crawled';
  });
  await until(() => worker1.active === 1, 'the claim');

  // A directive goes to the lease's holder; its next heartbeat delivers it.
  await operator.instances.invoke('jobs', 'crawl', 'direct', { name: 'cancel' });
  await until(async () => (await operator.instances.get<Data>('jobs', 'crawl')).data.status === 'failed', 'the cancel');
  assert.ok(stopped !== undefined && !(stopped instanceof LeaseLostError), String(stopped));
  const crawl = (await operator.instances.get<Data>('jobs', 'crawl')).data;
  assert.deepEqual(
    [crawl.retries.classAttempts.cancelled, crawl.retries.exhausted, crawl.lease.holder, crawl.lease.ended.reason, crawl.result],
    [1, true, null, 'release', undefined]
  );
});

test('a worker cut off from the engine is missed and loses its job to another, and its stale token is refused', async () => {
  const { engine, clock, client, operator, worker } = await start();
  await operator.instances.create('jobs', { title: 'Crawl the archive', topic: 'search' }, { id: 'crawl' });
  let lost: unknown;
  let late: unknown;
  const first = await worker(
    'worker-1',
    async ({ signal, recordUsage }) => {
      await new Promise((resolve) => signal.addEventListener('abort', resolve, { once: true }));
      lost = signal.reason;
      // Its work ends there: what it reports now is refused without being sent.
      late = await recordUsage(30).catch((error: unknown) => error);
      return 'Crawled late';
    },
    { topic: 'search' }
  );
  await until(() => first.worker.active === 1, 'the claim');

  // worker-1 is cut off holding the job: it beats no more and renews
  // nothing. Past its worker's deadline, 15 seconds after its last beat, the
  // runner's sweep misses it, and the miss expires the leases its principal
  // holds on jobs, sooner than the lease's own 30 seconds would. worker-2
  // beats on.
  first.network.up = false;
  await until(() => first.network.inflight === 0, 'what worker-1 sent to be answered');
  clock.ms += 16_000;
  await client('worker-2-token').instances.invoke('workers', 'worker-2', 'beat');
  engine.runner.runDue();
  const missing = await operator.instances.get<Data>('workers', 'worker-1');
  assert.equal(missing.data.status, 'missing');
  assert.deepEqual(missing.data.presence.released, { jobs: ['crawl'] });
  const crawl = await operator.instances.get<Data>('jobs', 'crawl');
  assert.equal(crawl.data.status, 'queued');
  assert.deepEqual(crawl.data.lease, { holder: null, token: 2, acquiredAt: null, renewedAt: null, expiresAt: null, active: false, expiries: 1, ended: { reason: 'holder', at: 1_016_000 } });

  // worker-2 claims it again, under a new token.
  let open = false;
  const second = await worker(
    'worker-2',
    async () => {
      await until(() => open, 'the test to let the job finish');
      return 'Crawled';
    },
    { topic: 'search' }
  );
  await until(() => second.worker.active === 1, 'the claim by worker-2');
  assert.equal(second.worker.jobs[0]?.token, 3);

  // worker-1 comes back and renews with its old token: Lease refuses it,
  // whoever presents it, and the worker stops the handler.
  first.network.up = true;
  await until(() => late !== undefined, 'worker-1 to lose the job');
  assert.ok(lost instanceof LeaseLostError && lost.reason === 'token_stale', String(lost));
  assert.ok(isVeto(lost.cause, 'Lease', 'token_stale'));
  assert.deepEqual(lost.cause.vetoDetails, { token: 1, current: 3 });
  assert.ok(late instanceof LeaseLostError, String(late));

  // The job stays worker-2's, and worker-1 wrote nothing to it.
  open = true;
  await until(async () => (await operator.instances.get<Data>('jobs', 'crawl')).data.status === 'done', 'worker-2 to finish');
  const done = (await operator.instances.get<Data>('jobs', 'crawl')).data;
  assert.deepEqual([done.result, done.budget.cpuSeconds.used], ['Crawled', 0]);

  // A beat brings worker-1 back.
  await until(async () => (await operator.instances.get<Data>('workers', 'worker-1')).data.status === 'active', 'worker-1 to beat again');
});

test('a batch runs its steps in order, retries a transient failure, and settles when they finish', async () => {
  const { engine, operator, worker } = await start();
  await operator.instances.create('batches', { title: 'Nightly crawl', topic: 'search' }, { id: 'nightly' });
  const batch = async (id: string) => (await operator.instances.get<Data>('batches', id)).data;

  // The batch's create stamped a job per step, each blocked by the one
  // before, so the worker claims them in order. index fails once, and goes
  // back in the queue.
  let failures = 1;
  const steps: string[] = [];
  const handler: Handler = async ({ job }) => {
    steps.push(job.step!);
    if (job.step === 'index' && failures-- > 0) {
      throw new JobFailure('transient', 'The source timed out.');
    }
    return `${job.step} ok`;
  };
  const first = await worker('worker-1', handler, { topic: 'search' });
  await until(async () => (await batch('nightly')).rollups.steps.done === 3, 'every step done');
  await first.worker.stop();
  assert.deepEqual(steps, ['fetch', 'index', 'index', 'report']);

  // The rollup counts the steps at each read; the runner's Reactions
  // settle the batch once every step succeeded.
  let nightly = await batch('nightly');
  assert.deepEqual([nightly.status, nightly.rollups], ['running', { steps: { done: 3 } }]);
  engine.runner.runDue();
  nightly = await batch('nightly');
  assert.equal(nightly.status, 'done');
  const index = nightly.blueprint.children.find((child: { key: string }) => child.key === 'index');
  const indexed = await operator.instances.get<Data>('jobs', index.id);
  assert.deepEqual(indexed.data.retries.classAttempts, { cancelled: 0, invalid: 0, transient: 1 });

  // A step that fails for good fails its batch, and the steps after it are never claimed.
  await operator.instances.create('batches', { title: 'Broken crawl', topic: 'mail' }, { id: 'broken' });
  const failed: string[] = [];
  const second = await worker(
    'worker-2',
    async ({ job }) => {
      failed.push(job.step!);
      throw new JobFailure('invalid', 'No such source.');
    },
    { topic: 'mail' }
  );
  await until(async () => (await batch('broken')).rollups.steps.failed === 1, 'the first step failed');
  await new Promise((resolve) => setTimeout(resolve, 100));
  await second.worker.stop();
  assert.deepEqual(failed, ['fetch']);
  engine.runner.runDue();
  const broken = await batch('broken');
  assert.deepEqual([broken.status, broken.rollups], ['failed', { steps: { failed: 1, queued: 2 } }]);

  // The runner moved the batch, as its own principal, and the event says why.
  const { events } = await operator.events.read({ after: 0, schema: 'batches', behaviors: ['Workflow'] });
  assert.deepEqual(
    events.map((event: EngineEvent) => [event.instanceId, event.actor, event.cause?.behavior, (event.change as { patch: unknown }).patch]),
    [
      ['nightly', 'runner', 'Reactions', { status: 'done' }],
      ['broken', 'runner', 'Reactions', { status: 'failed' }],
    ]
  );
});
