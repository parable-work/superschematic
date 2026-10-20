import type { Logger } from './logger.js';

/*
The claim loop of a stack's TypeScript worker (D53, section 8.8 of
docs/stack-model.md), the twin of the Go runtime's worker.Run: the process
that claims the messages of a queue of a database and handles each with an
API's Deps. The worker's generated main.ts builds a WorkerQueue from the
claims of the runtime's postgresQueue, each message with the handler of the
API's Workers, and calls runWorker. This is the package's ./worker entry,
which imports nothing: the claims are the queue's functions, so the loop is
the same for every queue.

runWorker claims up to its concurrency of messages at a time and runs each
handler at once, extending the message's claim every third of the lease
while the handler runs, so a handler longer than the lease keeps its
message. A handler that resolves completes its message; one that rejects,
or throws, fails it, which the queue retries after its backoff or marks
dead. Each outcome is logged. After a claim that found fewer messages than
it had slots for, it waits a second before claiming again, and after one
that failed, longer each time, up to thirty seconds.

When its signal aborts (SIGTERM), it claims nothing more and gives the
running handlers the grace to finish. Then it aborts the signal each
handler was given, releases their messages, which are due again at once
for another worker, and resolves. A handler that settles after its
message was released changes nothing: JavaScript cannot stop it, but the
process exits after runWorker resolves.

Beside the claims, runWorker prunes the queue (D68): when it starts and
every five minutes after, it deletes the done messages older than the
queue's retention, PRUNE_BATCH at a time until a batch is short, and logs
how many. Every instance of every worker of the queue prunes, which is
safe: each batch skips the rows another holds, and a row deleted once is
gone. It never deletes a dead message.
*/

/** The environment variable a platform sets to the number of messages a worker handles at a time. */
export const WORKER_CONCURRENCY_VARIABLE = 'WORKER_CONCURRENCY';

/** How long a stopping worker lets its handlers finish: under the ten seconds Cloud Run gives a container after SIGTERM. */
export const DEFAULT_GRACE_MS = 8_000;

/** How long a worker waits to claim again after a claim found fewer messages than it had slots for. */
export const DEFAULT_IDLE_MS = 1_000;

/** How often a worker prunes its queue's done messages, after the prune it starts with. */
export const DEFAULT_PRUNE_EVERY_MS = 300_000;

/**
 * How many done messages one prune statement deletes at most, so no
 * statement holds its locks for long; a prune runs another while one
 * deletes a whole batch.
 */
export const PRUNE_BATCH = 1_000;

/** The longest wait after a claim that failed, which doubles from the idle wait. */
const MAX_BACKOFF_MS = 30_000;

/** How long each complete, fail, extend, release and prune batch may take, so a stopping worker still records them. */
const OP_TIMEOUT_MS = 5_000;

/** The longest error a failed message records. */
const MAX_CAUSE = 4_000;

/** A message a worker claimed: its ID, the claim's token, the attempt this is, from 1, and the handler of it. */
export interface WorkerMessage {
  readonly id: string;
  readonly token: string;
  readonly attempt: number;
  /** Runs the worker's handler on the message; signal aborts when the worker stops and its grace is over. */
  handle(signal: AbortSignal): Promise<void>;
}

/** The queue a worker claims from, as postgresQueue claims. */
export interface WorkerQueue {
  /** Names the queue in the log. */
  readonly name: string;
  /** How long a claim holds a message; runWorker extends it every third of it while the message's handler runs. */
  readonly leaseSeconds: number;
  claim(n: number, leaseSeconds: number): Promise<readonly WorkerMessage[]>;
  extend(id: string, token: string, leaseSeconds: number): Promise<void>;
  complete(id: string, token: string): Promise<void>;
  /** Records cause on a claimed message and resolves to whether it is dead. */
  fail(id: string, token: string, cause: string): Promise<boolean>;
  release(id: string, token: string): Promise<void>;
  /** Deletes up to limit messages done for the queue's retention and resolves to how many; without it the worker prunes nothing. */
  prune?(limit: number): Promise<number>;
}

export interface WorkerOptions {
  /** Stops the worker: it claims no more, and lets the running handlers finish within the grace. */
  readonly signal: AbortSignal;
  /** How many messages are handled at a time; one unless set. */
  readonly concurrency?: number;
  /** How long a stopping worker lets its handlers finish; DEFAULT_GRACE_MS unless set. */
  readonly graceMs?: number;
  /** How long to wait after a claim that found too few messages; DEFAULT_IDLE_MS unless set. */
  readonly idleMs?: number;
  /** How often to prune the queue after the prune the worker starts with; DEFAULT_PRUNE_EVERY_MS unless set. */
  readonly pruneEveryMs?: number;
  /** Logs each message's outcome; nothing is logged without one. */
  readonly logger?: Logger;
}

/**
 * The concurrency the environment gives a worker in WORKER_CONCURRENCY, or
 * fallback when it sets none. Throws for a value that is not a whole
 * number, one or more.
 */
export function workerConcurrency(fallback: number, env: Readonly<Record<string, string | undefined>> = processEnv()): number {
  const value = env[WORKER_CONCURRENCY_VARIABLE];
  if (value === undefined || value === '') return fallback;
  const n = Number(value);
  if (!/^\d+$/.test(value.trim()) || !Number.isSafeInteger(n) || n < 1) {
    throw new Error(`${WORKER_CONCURRENCY_VARIABLE} is ${JSON.stringify(value)}; it is a whole number, one or more`);
  }
  return n;
}

function processEnv(): Readonly<Record<string, string | undefined>> {
  return (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env ?? {};
}

/** One message whose handler runs. */
interface Handling {
  readonly message: WorkerMessage;
  readonly started: number;
  released: boolean;
  /** Settles once the handler settled and its outcome is recorded. */
  done?: Promise<void>;
}

const silent: Logger = {
  debug() {},
  info() {},
  warn() {},
  error() {},
  child() {
    return silent;
  },
};

/**
 * Claims and handles queue's messages until options.signal aborts, then
 * stops as the module says: it claims no more, lets the running handlers
 * finish within the grace, and releases the messages of those that do not.
 * It resolves once it stopped, and rejects only for a queue it cannot run.
 */
export async function runWorker(queue: WorkerQueue, options: WorkerOptions): Promise<void> {
  if (!(queue.leaseSeconds > 0)) throw new Error(`worker: queue ${queue.name} has a lease of ${queue.leaseSeconds}s; it is positive`);
  const concurrency = Math.max(1, Math.floor(options.concurrency ?? 1));
  const graceMs = options.graceMs !== undefined && options.graceMs > 0 ? options.graceMs : DEFAULT_GRACE_MS;
  const idleMs = options.idleMs !== undefined && options.idleMs > 0 ? options.idleMs : DEFAULT_IDLE_MS;
  const pruneEveryMs = options.pruneEveryMs !== undefined && options.pruneEveryMs > 0 ? options.pruneEveryMs : DEFAULT_PRUNE_EVERY_MS;
  const logger = (options.logger ?? silent).child({ queue: queue.name });
  const { signal } = options;
  // The handlers' signal outlives the worker's by the grace.
  const handlers = new AbortController();
  const running = new Map<string, Handling>();
  let wake: (() => void) | undefined;
  const freed = (): void => {
    wake?.();
    wake = undefined;
  };

  const start = (message: WorkerMessage): void => {
    const h: Handling = { message, started: Date.now(), released: false };
    running.set(message.id, h);
    h.done = (async () => {
      let extending: Promise<void> = Promise.resolve();
      const timer = setInterval(
        () => {
          extending = extending.then(() =>
            withTimeout(queue.extend(message.id, message.token, queue.leaseSeconds)).catch((error: unknown) => {
              logger.warn('extend the claim failed', { message: message.id, attempt: message.attempt, error });
            })
          );
        },
        (queue.leaseSeconds * 1000) / 3
      );
      let error: unknown;
      let failed = false;
      try {
        await message.handle(handlers.signal);
      } catch (err) {
        failed = true;
        error = err;
      }
      clearInterval(timer);
      await extending;
      await finish(h, failed, error);
      freed();
    })();
  };

  const finish = async (h: Handling, failed: boolean, error: unknown): Promise<void> => {
    running.delete(h.message.id);
    const { message } = h;
    const fields = { message: message.id, attempt: message.attempt, took: (Date.now() - h.started) / 1000 };
    if (h.released) {
      logger.warn('message handler returned after its message was released', failed ? { ...fields, error } : fields);
      return;
    }
    if (!failed) {
      try {
        await withTimeout(queue.complete(message.id, message.token));
        logger.info('message handled', fields);
      } catch (completeError) {
        logger.error('message handled, but completing it failed', { ...fields, error: completeError });
      }
      return;
    }
    const cause = causeOf(error).slice(0, MAX_CAUSE);
    try {
      const dead = await withTimeout(queue.fail(message.id, message.token, cause));
      if (dead) logger.error('message failed and is dead: its retries are spent', { ...fields, error: cause });
      else logger.warn('message failed and will be retried', { ...fields, error: cause });
    } catch (recordError) {
      logger.error('message failed, and recording the failure failed', { ...fields, error: cause, recordError });
    }
  };

  // Deletes the done messages past the retention, a batch at a time until a
  // batch is short, and logs how many. A failed batch ends the prune, which
  // the next tries again. A stopping worker waits for no batch: one that
  // runs settles unheard.
  const stopped = new Promise<'stopped'>(resolve => {
    if (signal.aborted) resolve('stopped');
    else signal.addEventListener('abort', () => resolve('stopped'), { once: true });
  });
  const prune = async (deleteBatch: (limit: number) => Promise<number>): Promise<void> => {
    let pruned = 0;
    try {
      while (!signal.aborted) {
        const batch = withTimeout(deleteBatch(PRUNE_BATCH));
        batch.catch(() => {});
        const n = await Promise.race([batch, stopped]);
        if (n === 'stopped') break;
        pruned += n;
        if (n < PRUNE_BATCH) break;
      }
    } catch (error) {
      if (!signal.aborted) logger.warn('pruning the done messages failed', { pruned, error });
    }
    if (pruned > 0) logger.info('done messages pruned', { pruned });
  };
  // Prunes at once, then every pruneEveryMs, until the signal aborts.
  const pruning = (async () => {
    const deleteBatch = queue.prune?.bind(queue);
    if (deleteBatch === undefined) return;
    do {
      await prune(deleteBatch);
    } while (await sleep(pruneEveryMs, signal));
  })();

  // Claims for the free slots until the signal aborts.
  let wait = idleMs;
  while (!signal.aborted) {
    const free = concurrency - running.size;
    if (free <= 0) {
      // Waits for a handler to settle, or for the signal.
      await new Promise<void>(resolve => {
        const abort = (): void => resolve();
        wake = () => {
          signal.removeEventListener('abort', abort);
          resolve();
        };
        signal.addEventListener('abort', abort, { once: true });
      });
      continue;
    }
    let messages: readonly WorkerMessage[];
    try {
      messages = await queue.claim(free, queue.leaseSeconds);
    } catch (error) {
      if (signal.aborted) break;
      logger.error('claim failed', { retryIn: wait / 1000, error });
      if (!(await sleep(wait, signal))) break;
      wait = Math.min(wait * 2, MAX_BACKOFF_MS);
      continue;
    }
    wait = idleMs;
    for (const message of messages) start(message);
    if (messages.length < free && !(await sleep(idleMs, signal))) break;
  }

  // Stops: waits up to the grace for the running handlers, then aborts the
  // rest and releases their messages. The prune stops with the signal.
  if (running.size > 0) logger.info('stopping: letting the running handlers finish', { running: running.size, grace: graceMs / 1000 });
  const all = Promise.all([...running.values()].map(h => h.done));
  let timer: ReturnType<typeof setTimeout> | undefined;
  const over = new Promise<boolean>(resolve => {
    timer = setTimeout(() => resolve(true), graceMs);
  });
  const late = await Promise.race([all.then(() => false), over]);
  clearTimeout(timer);
  if (!late) {
    await pruning;
    logger.info('worker stopped');
    return;
  }
  const unfinished = [...running.values()];
  for (const h of unfinished) h.released = true;
  handlers.abort(new Error('the worker stopped, and its grace is over'));
  let released = 0;
  for (const { message } of unfinished) {
    try {
      await withTimeout(queue.release(message.id, message.token));
      released++;
    } catch (error) {
      logger.error('release failed: the message is due again when its claim expires', { message: message.id, error });
    }
  }
  await pruning;
  logger.info('worker stopped: the grace is over, and the unfinished messages are released', { released, unfinished: unfinished.length });
}

/** What a failed handler's message records: its error's message, or what it threw. */
function causeOf(error: unknown): string {
  if (error instanceof Error) return error.message || error.name;
  if (typeof error === 'string') return error;
  try {
    return JSON.stringify(error) ?? String(error);
  } catch {
    return String(error);
  }
}

/** Waits ms, or until signal aborts, and resolves to whether it is still live. */
function sleep(ms: number, signal: AbortSignal): Promise<boolean> {
  return new Promise(resolve => {
    if (signal.aborted) {
      resolve(false);
      return;
    }
    const abort = (): void => {
      clearTimeout(timer);
      resolve(false);
    };
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort);
      resolve(true);
    }, ms);
    signal.addEventListener('abort', abort, { once: true });
  });
}

/** Rejects when work takes longer than OP_TIMEOUT_MS. */
function withTimeout<T>(work: Promise<T>): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`no answer within ${OP_TIMEOUT_MS} ms`)), OP_TIMEOUT_MS);
  });
  return Promise.race([work, timeout]).finally(() => clearTimeout(timer));
}
