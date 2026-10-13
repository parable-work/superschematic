import { describe, expect, test } from 'bun:test';
import { createLogger } from './logger';
import { runWorker, workerConcurrency, WORKER_CONCURRENCY_VARIABLE, type WorkerMessage, type WorkerQueue } from './worker';

/** A queue in memory: messages to claim, and what the loop did with each, as the Go loop's tests keep it. */
class FakeQueue {
  ready: WorkerMessage[] = [];
  maxBatch = 0;
  completed: string[] = [];
  failed = new Map<string, string>();
  released: string[] = [];
  extended = new Map<string, number>();
  claimErrors = 0;

  constructor(readonly leaseSeconds: number) {}

  add(id: string, handle: (signal: AbortSignal) => Promise<void>): void {
    this.ready.push({ id, token: `token-${id}`, attempt: 1, handle });
  }

  queue(): WorkerQueue {
    return {
      name: 'Fake',
      leaseSeconds: this.leaseSeconds,
      claim: async n => {
        if (this.claimErrors > 0) {
          this.claimErrors--;
          throw new Error('the database is down');
        }
        this.maxBatch = Math.max(this.maxBatch, n);
        return this.ready.splice(0, n);
      },
      extend: async id => {
        this.extended.set(id, (this.extended.get(id) ?? 0) + 1);
      },
      complete: async id => {
        this.completed.push(id);
      },
      fail: async (id, _token, cause) => {
        this.failed.set(id, cause);
        return cause.includes('fatal');
      },
      release: async id => {
        this.released.push(id);
      },
    };
  }
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** Polls cond until it holds or a few seconds pass. */
async function waitFor(what: string, cond: () => boolean): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (!cond()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await sleep(5);
  }
}

function logs() {
  const lines: Record<string, unknown>[] = [];
  return { lines, logger: createLogger({}, { write: line => lines.push(JSON.parse(line)) }) };
}

describe('runWorker', () => {
  test('a handler that resolves completes its message, one that rejects or throws fails it, and each outcome is logged', async () => {
    const f = new FakeQueue(60);
    f.add('ok', async () => {});
    f.add('retry', async () => {
      throw new Error('try later');
    });
    f.add('dead', async () => {
      throw new Error('fatal: no such order');
    });
    f.add('throws', () => {
      throw new Error('boom');
    });
    const { lines, logger } = logs();
    const stop = new AbortController();
    const stopped = runWorker(f.queue(), { signal: stop.signal, concurrency: 2, idleMs: 10, logger });
    await waitFor("every message's outcome", () => f.completed.length + f.failed.size === 4);
    stop.abort();
    await stopped;
    expect(f.completed).toEqual(['ok']);
    expect(Object.fromEntries(f.failed)).toEqual({ retry: 'try later', dead: 'fatal: no such order', throws: 'boom' });
    expect(f.maxBatch).toBeLessThanOrEqual(2);
    for (const [message, msg] of Object.entries({
      ok: 'message handled',
      retry: 'message failed and will be retried',
      dead: 'message failed and is dead: its retries are spent',
      throws: 'message failed and will be retried',
    })) {
      expect(lines.some(line => line.msg === msg && line.message === message && line.queue === 'Fake')).toBe(true);
    }
  });

  test('no more handlers run at once than the concurrency, and a slot that frees is claimed for', async () => {
    const f = new FakeQueue(60);
    let running = 0;
    let most = 0;
    for (let i = 0; i < 9; i++) {
      f.add(String(i), async () => {
        running++;
        most = Math.max(most, running);
        await sleep(20);
        running--;
      });
    }
    const stop = new AbortController();
    const stopped = runWorker(f.queue(), { signal: stop.signal, concurrency: 3, idleMs: 5 });
    await waitFor('nine messages handled', () => f.completed.length === 9);
    stop.abort();
    await stopped;
    expect(most).toBe(3);
  });

  test("a handler longer than a third of the lease has its claim extended", async () => {
    const f = new FakeQueue(0.09);
    f.add('long', () => sleep(200).then(() => undefined));
    const stop = new AbortController();
    const stopped = runWorker(f.queue(), { signal: stop.signal, idleMs: 5 });
    await waitFor('the long message handled', () => f.completed.length === 1);
    stop.abort();
    await stopped;
    expect(f.extended.get('long') ?? 0).toBeGreaterThanOrEqual(2);
  });

  test('once its signal aborts it claims no more, lets a handler that finishes within the grace complete, and aborts and releases the one that does not', async () => {
    const f = new FakeQueue(60);
    const started: string[] = [];
    f.add('quick', async () => {
      started.push('quick');
      await sleep(30);
    });
    let aborted = false;
    f.add('stuck', signal => {
      started.push('stuck');
      return new Promise((_, reject) => {
        signal.addEventListener('abort', () => {
          aborted = true;
          reject(signal.reason);
        });
      });
    });
    const stop = new AbortController();
    const stopped = runWorker(f.queue(), { signal: stop.signal, concurrency: 2, graceMs: 150, idleMs: 5 });
    await waitFor('both handlers started', () => started.length === 2);
    f.add('late', async () => {});
    const began = Date.now();
    stop.abort();
    await stopped;
    const took = Date.now() - began;
    expect(took).toBeGreaterThanOrEqual(140);
    expect(took).toBeLessThan(2_000);
    await waitFor("the stuck handler's signal aborted", () => aborted);
    await sleep(20);
    expect(f.completed).toEqual(['quick']);
    expect(f.released).toEqual(['stuck']);
    expect(f.failed.size).toBe(0);
    expect(f.ready.map(m => m.id)).toEqual(['late']);
  });

  test('a claim that fails is logged and tried again, after a longer wait each time', async () => {
    const f = new FakeQueue(60);
    f.claimErrors = 2;
    f.add('after', async () => {});
    const { lines, logger } = logs();
    const stop = new AbortController();
    const stopped = runWorker(f.queue(), { signal: stop.signal, idleMs: 5, logger });
    await waitFor('the message handled after the database came back', () => f.completed.length === 1);
    stop.abort();
    await stopped;
    const failed = lines.filter(line => line.msg === 'claim failed');
    expect(failed.map(line => line.retryIn)).toEqual([0.005, 0.01]);
  });

  test('a queue without a lease does not run', async () => {
    await expect(runWorker({ ...new FakeQueue(0).queue() }, { signal: new AbortController().signal })).rejects.toThrow('a lease of 0s');
  });
});

describe('workerConcurrency', () => {
  test("reads the environment's concurrency, or the default, and refuses one under one", () => {
    expect(workerConcurrency(4, {})).toBe(4);
    expect(workerConcurrency(4, { [WORKER_CONCURRENCY_VARIABLE]: '' })).toBe(4);
    expect(workerConcurrency(4, { [WORKER_CONCURRENCY_VARIABLE]: '7' })).toBe(7);
    expect(() => workerConcurrency(4, { [WORKER_CONCURRENCY_VARIABLE]: '0' })).toThrow('WORKER_CONCURRENCY is "0"; it is a whole number, one or more');
    expect(() => workerConcurrency(4, { [WORKER_CONCURRENCY_VARIABLE]: '2.5' })).toThrow('a whole number');
  });
});
