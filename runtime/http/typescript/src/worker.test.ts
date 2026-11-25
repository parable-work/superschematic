import { describe, expect, test } from 'bun:test';
import { createLogger } from './logger';
import {
  DEFAULT_DRAIN_IDLE_MS,
  PRUNE_BATCH,
  runWorker,
  workerConcurrency,
  workerDrain,
  workerDrainSeconds,
  WORKER_CONCURRENCY_VARIABLE,
  WORKER_DRAIN_SECONDS_VARIABLE,
  WORKER_IDLE_SECONDS_VARIABLE,
  WORKER_MODE_VARIABLE,
  WORKER_SLOTS_VARIABLE,
  type WorkerMessage,
  type WorkerQueue,
} from './worker';

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

  test('it prunes as it starts, a batch at a time while a batch is whole, then every period, and logs each total and each failure', async () => {
    const answers: (number | Error)[] = [PRUNE_BATCH, PRUNE_BATCH, 7, new Error('the database is down'), 3];
    const limits: number[] = [];
    const { lines, logger } = logs();
    const stop = new AbortController();
    const stopped = runWorker(
      {
        ...new FakeQueue(60).queue(),
        prune: async limit => {
          limits.push(limit);
          const answer = answers.shift() ?? 0;
          if (answer instanceof Error) throw answer;
          return answer;
        },
      },
      { signal: stop.signal, idleMs: 5, pruneEveryMs: 20, logger }
    );
    await waitFor('four prunes', () => limits.length >= 6);
    stop.abort();
    await stopped;
    expect(new Set(limits)).toEqual(new Set([PRUNE_BATCH]));
    const pruned = lines.filter(line => line.msg === 'done messages pruned');
    expect(pruned.map(line => [line.pruned, line.queue])).toEqual([
      [2 * PRUNE_BATCH + 7, 'Fake'],
      [3, 'Fake'],
    ]);
    const failed = lines.filter(line => line.msg === 'pruning the done messages failed');
    expect(failed.map(line => [line.level, line.pruned])).toEqual([['warn', 0]]);
  });

  test('a stopping worker waits for no prune that runs', async () => {
    let pruning = false;
    const { lines, logger } = logs();
    const stop = new AbortController();
    const stopped = runWorker(
      {
        ...new FakeQueue(60).queue(),
        prune: () => {
          pruning = true;
          return new Promise<number>(() => {});
        },
      },
      { signal: stop.signal, idleMs: 5, logger }
    );
    await waitFor('the prune started', () => pruning);
    const began = Date.now();
    stop.abort();
    await stopped;
    expect(Date.now() - began).toBeLessThan(1_000);
    expect(lines.some(line => line.msg === 'pruning the done messages failed')).toBe(false);
  });

  test('a queue without a lease does not run', async () => {
    await expect(runWorker({ ...new FakeQueue(0).queue() }, { signal: new AbortController().signal })).rejects.toThrow('a lease of 0s');
  });
});

/** The locks of one queue, as a database keeps them: slot takes one of as many as it is asked for, or none when all are held. */
class Slots {
  held = 0;
  asked: number[] = [];

  slot = async (slots: number): Promise<(() => Promise<void>) | undefined> => {
    this.asked.push(slots);
    if (this.held >= slots) return undefined;
    this.held++;
    return async () => {
      this.held--;
    };
  };
}

describe('runWorker in drain mode (D145)', () => {
  const never = new AbortController().signal;

  test('it handles what its queue holds, stays up while a handler holds a message, and resolves once none was ready or held for its idle time', async () => {
    const idleMs = 80;
    const f = new FakeQueue(60);
    let let_go: () => void = () => {};
    const gate = new Promise<void>(resolve => {
      let_go = resolve;
    });
    f.add('held', () => gate);
    f.add('ok', async () => {});
    const { lines, logger } = logs();
    let stopped = false;
    const done = runWorker(f.queue(), { signal: never, concurrency: 2, idleMs: 5, logger, drain: { idleMs } }).then(() => {
      stopped = true;
    });
    await waitFor('the message that finishes', () => f.completed.length === 1);
    await sleep(3 * idleMs);
    expect(stopped).toBe(false);
    const released = Date.now();
    let_go();
    await done;
    expect(Date.now() - released).toBeGreaterThanOrEqual(idleMs - 5);
    expect(f.completed.sort()).toEqual(['held', 'ok']);
    expect(f.released).toEqual([]);
    expect(lines.filter(line => line.msg === 'drained: no message was ready or held')).toHaveLength(1);
    expect(lines.at(-1)?.msg).toBe('worker stopped');
  });

  test('a message that arrives within the idle time is handled, and the idle time starts again', async () => {
    const idleMs = 400;
    const f = new FakeQueue(60);
    const started = Date.now();
    const done = runWorker(f.queue(), { signal: never, idleMs: 5, drain: { idleMs } });
    for (const [i, id] of ['a', 'b', 'c'].entries()) {
      await sleep(idleMs / 4);
      f.add(id, async () => {});
      await waitFor(`message ${id}`, () => f.completed.length === i + 1);
    }
    await done;
    expect(Date.now() - started).toBeGreaterThanOrEqual((3 * idleMs) / 4 + idleMs - 5);
  });

  test('it takes one of its slots before it claims and gives it back, and one that finds every slot taken claims nothing and resolves at once', async () => {
    const locks = new Slots();
    const first = new FakeQueue(60);
    let let_go: () => void = () => {};
    first.add(
      'm',
      () =>
        new Promise<void>(resolve => {
          let_go = resolve;
        })
    );
    const firstDone = runWorker({ ...first.queue(), slot: locks.slot }, { signal: never, idleMs: 5, drain: { idleMs: 40, slots: 1 } });
    await waitFor("the first worker's claim", () => first.ready.length === 0);

    // Its idle time is long, so only an exit at once resolves in time.
    const second = new FakeQueue(60);
    second.add('n', async () => {});
    const { lines, logger } = logs();
    const began = Date.now();
    await runWorker({ ...second.queue(), slot: locks.slot }, { signal: never, idleMs: 5, logger, drain: { idleMs: 60_000, slots: 1 } });
    expect(Date.now() - began).toBeLessThan(10_000);
    expect(second.ready).toHaveLength(1);
    expect(lines.map(line => line.msg)).toEqual(['every slot of the queue is taken: the workers that hold them drain it']);

    // With two slots, a second worker runs beside the first.
    const third = new FakeQueue(60);
    third.add('o', async () => {});
    const thirdDone = runWorker({ ...third.queue(), slot: locks.slot }, { signal: never, idleMs: 5, drain: { idleMs: 40, slots: 2 } });
    await waitFor("the third worker's message", () => third.completed.length === 1);
    expect(locks.held).toBe(2);
    let_go();
    await Promise.all([firstDone, thirdDone]);
    expect(locks.held).toBe(0);
    expect(locks.asked).toEqual([1, 1, 2]);
  });

  test('a slot that cannot be asked for rejects, and a worker that is no drain takes none', async () => {
    const broken = {
      ...new FakeQueue(60).queue(),
      slot: async () => {
        throw new Error('the database is down');
      },
    };
    await expect(runWorker(broken, { signal: never, idleMs: 5, drain: { idleMs: 40 } })).rejects.toThrow('the database is down');

    const locks = new Slots();
    const stop = new AbortController();
    const done = runWorker({ ...new FakeQueue(60).queue(), slot: locks.slot }, { signal: stop.signal, idleMs: 5 });
    await sleep(30);
    stop.abort();
    await done;
    expect(locks.asked).toEqual([]);
  });

  test('an idle worker waits for a message due within its horizon, a delayed one or a retry, and handles it, and stops once the next is due past it', async () => {
    const f = new FakeQueue(60);
    // The queue holds a message due 150 ms from now: a claim takes it once
    // it is due, and nextDue reads how long until then, as the database
    // reads a ready message that waits.
    const dueAt = Date.now() + 150;
    let later = false;
    f.add('delayed', async () => {
      // A message due in an hour waits past the horizon of a minute.
      later = true;
    });
    const fake = f.queue();
    const queue: WorkerQueue = {
      ...fake,
      claim: async (n, leaseSeconds) => (Date.now() < dueAt ? [] : fake.claim(n, leaseSeconds)),
      nextDue: async () => {
        if (later) return 3600;
        return Date.now() < dueAt ? (dueAt - Date.now()) / 1000 : undefined;
      },
    };
    const { lines, logger } = logs();
    const started = Date.now();
    await runWorker(queue, { signal: never, idleMs: 5, logger, drain: { idleMs: 20 }, drainHorizonMs: 60_000 });
    expect(f.completed).toEqual(['delayed']);
    expect(Date.now() - started).toBeGreaterThanOrEqual(145);
    expect(lines.some(line => line.msg === 'idle, and waiting for a message due soon')).toBe(true);
    expect(lines.some(line => line.msg === "the next message is due past the drain's horizon: a wake or the tick runs it")).toBe(true);
    expect(lines.filter(line => line.msg === 'drained: no message was ready or held')).toHaveLength(1);
  });

  test('a worker whose next message is due past its horizon, or whose due time cannot be read, stops once idle', async () => {
    for (const nextDue of [async () => 600, async () => Promise.reject(new Error('the database is down'))]) {
      const started = Date.now();
      await runWorker({ ...new FakeQueue(60).queue(), nextDue }, { signal: never, idleMs: 5, drain: { idleMs: 20 }, drainHorizonMs: 60_000 });
      expect(Date.now() - started).toBeLessThan(5_000);
    }
  });

  test('a worker whose every claim fails for its idle time rejects and gives its slot back, and one whose claims recover keeps draining', async () => {
    const locks = new Slots();
    const failing = new FakeQueue(60);
    failing.claimErrors = Number.POSITIVE_INFINITY;
    const { lines, logger } = logs();
    await expect(runWorker({ ...failing.queue(), slot: locks.slot }, { signal: never, idleMs: 5, logger, drain: { idleMs: 60 } })).rejects.toThrow(
      'worker: every claim of queue Fake failed for 0.06s, so the worker in drain mode stops: the database is down'
    );
    expect(locks.held).toBe(0);
    expect(lines.some(line => line.msg === 'every claim failed for the idle time: the worker stops with an error and gives its slot back')).toBe(true);

    // Two failed claims, within the idle time, then the queue answers.
    const recovering = new FakeQueue(60);
    recovering.claimErrors = 2;
    recovering.add('m', async () => {});
    await runWorker(recovering.queue(), { signal: never, idleMs: 5, drain: { idleMs: 200 } });
    expect(recovering.completed).toEqual(['m']);

    // A worker in a pool keeps trying until it is stopped.
    const pooled = new FakeQueue(60);
    pooled.claimErrors = Number.POSITIVE_INFINITY;
    const stop = new AbortController();
    const done = runWorker(pooled.queue(), { signal: stop.signal, idleMs: 5 });
    await sleep(100);
    stop.abort();
    await done;
  });

  test('a signal stops a draining worker as it stops any, and its slot is given back', async () => {
    const locks = new Slots();
    const stop = new AbortController();
    const done = runWorker({ ...new FakeQueue(60).queue(), slot: locks.slot }, { signal: stop.signal, idleMs: 5, drain: { idleMs: 60_000 } });
    await waitFor('the slot', () => locks.held === 1);
    stop.abort();
    await done;
    expect(locks.held).toBe(0);
  });
});

describe('workerDrain', () => {
  test("reads the platform's variables, with the defaults for those it leaves out, and refuses what it cannot read", () => {
    expect(workerDrain({})).toBeUndefined();
    expect(workerDrain({ [WORKER_MODE_VARIABLE]: '' })).toBeUndefined();
    expect(workerDrain({ [WORKER_MODE_VARIABLE]: 'pool', [WORKER_IDLE_SECONDS_VARIABLE]: '45' })).toBeUndefined();
    expect(workerDrain({ [WORKER_MODE_VARIABLE]: 'drain' })).toEqual({ idleMs: DEFAULT_DRAIN_IDLE_MS, slots: 1 });
    expect(workerDrain({ [WORKER_MODE_VARIABLE]: 'drain', [WORKER_IDLE_SECONDS_VARIABLE]: '45', [WORKER_SLOTS_VARIABLE]: '3' })).toEqual({
      idleMs: 45_000,
      slots: 3,
    });
    expect(DEFAULT_DRAIN_IDLE_MS).toBe(30_000);
    expect(() => workerDrain({ [WORKER_MODE_VARIABLE]: 'burst' })).toThrow('WORKER_MODE is "burst"; it is "pool" or "drain"');
    expect(() => workerDrain({ [WORKER_MODE_VARIABLE]: 'drain', [WORKER_IDLE_SECONDS_VARIABLE]: 'soon' })).toThrow('WORKER_IDLE_SECONDS is "soon"');
    expect(() => workerDrain({ [WORKER_MODE_VARIABLE]: 'drain', [WORKER_SLOTS_VARIABLE]: '0' })).toThrow('WORKER_SLOTS is "0"');
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

describe('workerDrainSeconds', () => {
  test("reads a firing's window, or the default, and refuses one under a second", () => {
    expect(workerDrainSeconds(50, {})).toBe(50);
    expect(workerDrainSeconds(50, { [WORKER_DRAIN_SECONDS_VARIABLE]: '' })).toBe(50);
    expect(workerDrainSeconds(50, { [WORKER_DRAIN_SECONDS_VARIABLE]: '20' })).toBe(20);
    expect(() => workerDrainSeconds(50, { [WORKER_DRAIN_SECONDS_VARIABLE]: '0' })).toThrow('WORKER_DRAIN_SECONDS is "0"; it is a whole number of seconds, one or more');
    expect(() => workerDrainSeconds(50, { [WORKER_DRAIN_SECONDS_VARIABLE]: '1m' })).toThrow('a whole number of seconds');
  });
});
