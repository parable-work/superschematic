// The worker (@superschematic/engine-workqueue/worker) against an engine
// served in process: engineApp's fetch, no network. Claims, heartbeats and
// directives; the terminal step through a transition, Retries and an
// abandon that counts toward escalation; a lost lease aborting the handler
// and blocking its fenced writes, by a veto or by silence; bounded
// concurrency; waking on the event stream; and a stop that drains or
// releases what the worker holds.
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { afterEach, describe, test } from 'node:test';

import { EngineClient } from '@superschematic/engine/client';
import { engineApp, type EngineHttpOptions } from '@superschematic/engine/http';
import type { Engine, Principal } from '@superschematic/engine';

import { LeaseLostError, QueueWorker, WorkFailure, WorkerStoppedError, type Job, type QueueWorkerOptions } from '../dist/worker/index.js';
import { alice, cleanup, jobFlow, jobsDocument, openTestEngine, publish } from './helpers.ts';

const workers: QueueWorker[] = [];

afterEach(async () => {
  for (const worker of workers.splice(0)) {
    await worker.stop({ drain: false });
  }
  cleanup();
});

/** The operator may override leases and send directives. */
const operator: Principal = { subject: 'operator', permissions: ['jobs.override'] };

const authenticate: NonNullable<EngineHttpOptions['authenticate']> = async (ctx) =>
  ctx.bearerToken ? { subject: ctx.bearerToken, permissions: ctx.bearerToken === 'operator' ? ['jobs.override'] : [] } : null;

interface Served {
  engine: Engine;
  client: EngineClient;
  /** Every request the client sent: method and path. */
  requests: string[];
  /** Moves the engine's clock ahead of this process's. */
  skew(ms: number): void;
  /** Makes the client's heartbeats fail as a network does, or work again. */
  failHeartbeats(fail: boolean): void;
}

const LEASE = {
  name: 'Lease',
  config: {
    ttlMs: 3000,
    onExpiry: { transition: 'queued', from: ['running'] },
    maxExpiries: 2,
    escalate: { transition: 'failed', from: ['running'] },
    overridePermission: 'jobs.override',
  },
};

function serve(behaviors: ReadonlyArray<{ name: string; config?: unknown }> = [LEASE, { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' }, priorityField: 'priority', match: ['topic'] } }]): Served {
  let skew = 0;
  let failing = false;
  const engine = openTestEngine({ clock: () => Date.now() + skew });
  publish(engine, jobsDocument([{ name: 'Workflow', config: jobFlow }, ...behaviors]));
  const app = engineApp(engine, { authenticate });
  const requests: string[] = [];
  const client = new EngineClient({
    baseUrl: 'http://engine.test',
    auth: { token: 'worker' },
    fetch: async (input, init) => {
      const request = new Request(input, init);
      const path = new URL(request.url).pathname;
      requests.push(`${request.method} ${path}`);
      if (failing && path.endsWith('/operations/heartbeat')) {
        throw new TypeError('network down');
      }
      return app.fetch(request);
    },
  });
  return {
    engine,
    client,
    requests,
    skew: (ms) => {
      skew += ms;
    },
    failHeartbeats: (fail) => {
      failing = fail;
    },
  };
}

function start<T = Record<string, unknown>>(client: EngineClient, options: Partial<QueueWorkerOptions<T>> & Pick<QueueWorkerOptions<T>, 'handle'>): Promise<QueueWorker<T>> {
  const worker = new QueueWorker<T>(client, { schema: 'Job', heartbeatMs: 20, idle: { initialMs: 20, maxMs: 100 }, ...options });
  workers.push(worker as unknown as QueueWorker);
  return worker.start().then(() => worker);
}

function job(engine: Engine, id: string): Record<string, any> {
  return engine.instances.get(alice, 'Job', id)?.data as Record<string, any>;
}

/** The operations of an instance's events, in order. */
function operations(engine: Engine, id: string): string[] {
  return engine.events
    .read(alice, { schema: 'Job', instanceId: id, limit: 500 })
    .events.filter((event) => event.kind === 'operation')
    .map((event) => (event.change as { operation: string }).operation);
}

async function until(condition: () => boolean, what: string, timeoutMs = 8_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) {
      assert.fail(`timed out waiting for ${what}`);
    }
    await sleep(5);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** aborted resolves with the signal's reason once it aborts. */
function aborted(signal: AbortSignal): Promise<unknown> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve(signal.reason);
    } else {
      signal.addEventListener('abort', () => resolve(signal.reason), { once: true });
    }
  });
}

describe('the worker', () => {
  test('claims, heartbeats with the token, completes through the handler\'s transition and releases', async () => {
    const { engine, client } = serve([LEASE, { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' }, priorityField: 'priority', match: ['topic'] } }, { name: 'Retries', config: { classes: { timeout: { attempts: 3 } }, totalAttempts: 4, exhaustedState: 'failed' } }]);
    engine.instances.create(alice, 'Job', { title: 'index', topic: 'search', priority: 1 }, { id: 'low' });
    engine.instances.create(alice, 'Job', { title: 'index first', topic: 'search', priority: 9 }, { id: 'high' });
    engine.instances.create(alice, 'Job', { title: 'mail', topic: 'mail', priority: 99 }, { id: 'other' });
    const order: string[] = [];
    await start(client, {
      match: { topic: 'search' },
      handle: async (job) => {
        const instance = await job.instance.get();
        order.push(`${instance.id} ${String(instance.data.status)} ${String((instance.data.lease as { token: number }).token === job.token)}`);
        await until(() => operations(engine, job.id).filter((name) => name === 'heartbeat').length >= 2, 'two heartbeats');
        await job.instance.update({ title: `${String(instance.data.title)}, done` });
        return { transition: 'done', attempt: { detail: { pages: 3 } } };
      },
    });
    await until(() => job(engine, 'low')?.status === 'done' && job(engine, 'high')?.status === 'done', 'both search jobs done');
    assert.deepEqual(order, ['high running true', 'low running true']);
    assert.equal(job(engine, 'other').status, 'queued');
    const done = job(engine, 'high');
    assert.deepEqual([done.title, done.lease.holder, done.lease.ended.reason, done.lease.expiries, done.retries.total], ['index first, done', null, 'release', 0, 0]);
    const ran = operations(engine, 'high');
    assert.deepEqual([ran[0], ...ran.slice(-3)], ['claim', 'recordAttempt', 'transition', 'release']);
    assert.ok(ran.filter((name) => name === 'heartbeat').length >= 2);
  });

  test('hands each directive to the handler once, and acknowledges it in a heartbeat', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'crawl' }, { id: 'crawl' });
    const heard: string[] = [];
    let claimed: Job | undefined;
    await start(client, {
      handle: async (job) => {
        claimed = job;
        // A directive sent before a listener is added waits for it.
        await until(() => operations(engine, job.id).includes('direct'), 'the directive');
        job.onDirective((directive) => {
          heard.push(`${directive.id} ${directive.name} ${JSON.stringify(directive.data)}`);
        });
        await until(() => engine.events.read(alice, { schema: 'Job', instanceId: job.id, limit: 500 }).events.some(acknowledged), 'the acknowledgement');
        await sleep(60);
        return { transition: 'done' };
      },
    });
    await until(() => claimed !== undefined, 'the claim');
    engine.instances.invoke(operator, 'Job', 'crawl', 'direct', { name: 'slowDown', data: { by: 2 } });
    await until(() => job(engine, 'crawl').status === 'done', 'the job done');
    assert.deepEqual(heard, ['1 slowDown {"by":2}']);
  });

  test('a lapsed lease aborts the handler, and its fenced writes are refused without being sent', async () => {
    const served = serve();
    const { engine, client, requests } = served;
    engine.instances.create(alice, 'Job', { title: 'long' }, { id: 'long' });
    const outcome: unknown[] = [];
    await start(client, {
      handle: async (job) => {
        const reason = await aborted(job.signal);
        outcome.push(reason);
        const sent = requests.length;
        await job.instance.update({ title: 'too late' }).catch((error: unknown) => outcome.push(error));
        await job.instance.invoke('release').catch((error: unknown) => outcome.push(error));
        outcome.push(requests.length - sent);
        return { transition: 'done' };
      },
    });
    await until(() => job(engine, 'long').status === 'running', 'the claim');
    served.skew(10 * 60_000);
    await until(() => outcome.length === 4, 'the handler to stop');
    const [reason, update, release, sent] = outcome as [LeaseLostError, LeaseLostError, LeaseLostError, number];
    assert.ok(reason instanceof LeaseLostError && reason.reason === 'lapsed');
    assert.ok(update instanceof LeaseLostError && update.reason === 'lapsed');
    assert.ok(release instanceof LeaseLostError);
    assert.equal(sent, 0);
    await sleep(60);
    // Nothing more was written for it: no update, no transition, no release.
    const written = engine.events.read(alice, { schema: 'Job', instanceId: 'long', limit: 500 }).events;
    assert.deepEqual(new Set(written.map((event) => event.kind)), new Set(['create', 'operation']));
    assert.ok(operations(engine, 'long').every((name) => name === 'claim' || name === 'heartbeat'));
    assert.deepEqual([job(engine, 'long').title, job(engine, 'long').status], ['long', 'running']);
  });

  test('a lease another replaced is stale: the handler is aborted by the next heartbeat', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'contested' }, { id: 'contested' });
    const reasons: string[] = [];
    let claims = 0;
    await start(client, {
      handle: async (job) => {
        claims++;
        if (claims > 1) {
          return { transition: 'done' };
        }
        const reason = await aborted(job.signal);
        reasons.push((reason as LeaseLostError).reason);
        await assert.rejects(job.instance.update({ title: 'mine' }), (error: unknown) => error instanceof LeaseLostError && error.reason === 'token_stale');
        return undefined;
      },
    });
    await until(() => job(engine, 'contested').status === 'running', 'the claim');
    engine.instances.invoke(operator, 'Job', 'contested', 'expire', { holder: 'worker' });
    await until(() => job(engine, 'contested').status === 'done', 'the job claimed again and done');
    assert.deepEqual(reasons, ['token_stale']);
    assert.equal(job(engine, 'contested').title, 'contested');
  });

  test('a deleted instance loses the lease at the next heartbeat', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'doomed' }, { id: 'doomed' });
    let lost: LeaseLostError | undefined;
    await start(client, {
      handle: async (job) => {
        lost = (await aborted(job.signal)) as LeaseLostError;
        return { transition: 'done' };
      },
    });
    await until(() => job(engine, 'doomed')?.status === 'running', 'the claim');
    engine.instances.delete(operator, 'Job', 'doomed');
    await until(() => lost !== undefined, 'the loss');
    assert.equal(lost?.reason, 'not_found');
  });

  test('heartbeats that fail for the lease\'s length lose it', async () => {
    const served = serve();
    const { engine, client, requests } = served;
    engine.instances.create(alice, 'Job', { title: 'cut off' }, { id: 'cut' });
    const errors: unknown[] = [];
    let lost: LeaseLostError | undefined;
    let blockedSend = -1;
    await start(client, {
      ttlMs: 1000,
      onError: (error) => errors.push(error),
      handle: async (job) => {
        served.failHeartbeats(true);
        lost = (await aborted(job.signal)) as LeaseLostError;
        const sent = requests.length;
        await assert.rejects(job.instance.update({ title: 'healed' }), LeaseLostError);
        blockedSend = requests.length - sent;
        served.failHeartbeats(false);
        return { transition: 'done' };
      },
    });
    await until(() => lost !== undefined, 'the loss', 5_000);
    assert.equal(lost?.reason, 'unrenewed');
    await until(() => blockedSend !== -1, 'the blocked write');
    assert.equal(blockedSend, 0);
    assert.ok(errors.length > 0 && errors.every((error) => /network down/u.test(String(error))));
    assert.equal(job(engine, 'cut').title, 'cut off');
  });

  test('an abandon counts as an expiry, so a job every attempt gives up escalates at maxExpiries', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'poison' }, { id: 'poison' });
    const errors: unknown[] = [];
    let attempts = 0;
    await start(client, {
      onError: (error) => errors.push(error),
      handle: async () => {
        attempts++;
        // An error nobody classified is given up too.
        throw attempts === 1 ? new Error('unexpected input') : new WorkFailure('cannot parse it', { abandon: true });
      },
    });
    await until(() => job(engine, 'poison').status === 'failed', 'the escalation');
    assert.equal(attempts, 2);
    const poison = job(engine, 'poison');
    assert.deepEqual([poison.lease.expiries, poison.lease.ended.reason, poison.lease.holder], [2, 'abandon', null]);
    assert.deepEqual(errors.map(String), ['Error: unexpected input']);
  });

  test('a classified failure records its attempt through Retries and hands the job back', async () => {
    const { engine, client } = serve([LEASE, { name: 'Queue', config: { claim: { from: ['queued'], to: 'running' } } }, { name: 'Retries', config: { classes: { timeout: { attempts: 3 } }, totalAttempts: 4, exhaustedState: 'failed' } }]);
    engine.instances.create(alice, 'Job', { title: 'flaky' }, { id: 'flaky' });
    let attempts = 0;
    await start(client, {
      handle: async () => {
        attempts++;
        if (attempts === 1) {
          throw new WorkFailure('timed out', { failure: 'timeout', attempt: { detail: { after: '30s' } } });
        }
        return { transition: 'done', attempt: { score: 1 } };
      },
    });
    await until(() => job(engine, 'flaky').status === 'done', 'the second attempt');
    const flaky = job(engine, 'flaky');
    assert.deepEqual([flaky.retries.total, flaky.retries.classAttempts.timeout, flaky.lease.expiries], [1, 1, 0]);
    const recorded = engine.events
      .read(alice, { schema: 'Job', instanceId: 'flaky', behaviors: ['Retries'], limit: 500 })
      .events.map((event) => (event.change as { params: unknown }).params);
    assert.deepEqual(recorded, [{ failure: 'timeout', detail: { after: '30s' } }, { score: 1 }]);
  });

  test('works at most its concurrency at once', async () => {
    const { engine, client } = serve();
    for (const id of ['a', 'b', 'c']) {
      engine.instances.create(alice, 'Job', { title: id }, { id });
    }
    let running = 0;
    let most = 0;
    let open = false;
    const worker = await start(client, {
      concurrency: 2,
      handle: async () => {
        running++;
        most = Math.max(most, running);
        await until(() => open, 'the gate');
        running--;
        return { transition: 'done' };
      },
    });
    await until(() => worker.active === 2, 'two claims');
    await sleep(80);
    assert.equal(worker.active, 2);
    assert.equal(job(engine, 'c').status, 'queued');
    open = true;
    await until(() => ['a', 'b', 'c'].every((id) => job(engine, id).status === 'done'), 'every job done');
    assert.equal(most, 2);
  });

  test('an idle worker wakes on the event stream rather than waiting out its backoff, and looks with countClaimable', async () => {
    const { engine, client, requests } = serve();
    const done: string[] = [];
    await start(client, {
      idle: { initialMs: 60_000, maxMs: 60_000 },
      handle: async (job) => {
        done.push(job.id);
        return { transition: 'done' };
      },
    });
    await until(() => requests.includes('POST /namespaces/default/schemas/Job/operations/claimNext'), 'the first look');
    await sleep(50);
    const before = requests.length;
    engine.instances.create(alice, 'Job', { title: 'late' }, { id: 'late' });
    await until(() => done.includes('late'), 'the late job', 3_000);
    const looked = requests.slice(before).filter((request) => request.includes('/operations/'));
    assert.deepEqual(looked.slice(0, 2), [
      'POST /namespaces/default/schemas/Job/operations/countClaimable',
      'POST /namespaces/default/schemas/Job/operations/claimNext',
    ]);
  });

  test('a stop without draining aborts the handler and releases the lease, which counts nothing', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'endless' }, { id: 'endless' });
    let reason: unknown;
    let late: unknown;
    let release: () => void = () => undefined;
    const worker = await start(client, {
      handle: async (job) => {
        reason = await aborted(job.signal);
        await new Promise<void>((resolve) => (release = resolve));
        late = await job.instance.update({ title: 'after the stop' }).catch((error: unknown) => error);
        return { transition: 'done' };
      },
    });
    await until(() => worker.active === 1, 'the claim');
    await worker.stop({ drain: false });
    assert.ok(reason instanceof WorkerStoppedError);
    const endless = job(engine, 'endless');
    assert.deepEqual([endless.status, endless.lease.holder, endless.lease.ended.reason, endless.lease.expiries], ['queued', null, 'release', 0]);
    release();
    await until(() => late !== undefined, 'the handler to finish');
    assert.ok(late instanceof LeaseLostError && late.reason === 'released');
    assert.equal(job(engine, 'endless').title, 'endless');
  });

  test('a draining stop waits for the jobs it holds, and releases what outlasts its timeout', async () => {
    const { engine, client } = serve();
    engine.instances.create(alice, 'Job', { title: 'quick' }, { id: 'quick' });
    engine.instances.create(alice, 'Job', { title: 'stuck' }, { id: 'stuck' });
    const worker = await start(client, {
      concurrency: 2,
      handle: async (job) => {
        if (job.id === 'quick') {
          await sleep(100);
          return { transition: 'done' };
        }
        await new Promise(() => undefined);
        return undefined;
      },
    });
    await until(() => worker.active === 2, 'both claims');
    const started = Date.now();
    await worker.stop({ timeoutMs: 400 });
    assert.ok(Date.now() - started >= 300);
    assert.equal(job(engine, 'quick').status, 'done');
    const stuck = job(engine, 'stuck');
    assert.deepEqual([stuck.status, stuck.lease.holder, stuck.lease.ended.reason], ['queued', null, 'release']);
  });

  test('refuses a schema that has no claimable work', async () => {
    const { client } = serve([LEASE]);
    await assert.rejects(new QueueWorker(client, { schema: 'Job', handle: () => undefined }).start(), /does not compose Queue/u);
    assert.throws(() => new QueueWorker(client, { schema: 'Job', concurrency: 0, handle: () => undefined }), /concurrency/u);
  });

  test('the built worker imports only its own modules and the engine\'s client', () => {
    const directory = new URL('../dist/worker/', import.meta.url);
    for (const file of readdirSync(directory).filter((name) => name.endsWith('.js'))) {
      const source = readFileSync(new URL(file, directory), 'utf8');
      for (const match of source.matchAll(/(?:^|\n)\s*(?:import|export)\b[^'"]*?from\s*['"]([^'"]+)['"]/gu)) {
        assert.match(match[1], /^(?:\.\/[a-z-]+\.js|\.\.\/defaults\.js|@superschematic\/engine\/client)$/u, `${file} imports ${match[1]}`);
      }
    }
  });
});

function acknowledged(event: { kind: string; change: unknown }): boolean {
  const change = event.change as { operation?: string; params?: { acknowledge?: number[] } };
  return event.kind === 'operation' && change.operation === 'heartbeat' && (change.params?.acknowledge ?? []).includes(1);
}
