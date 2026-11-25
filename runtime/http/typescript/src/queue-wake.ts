/*
The wake a queue client sends after an enqueue (D145, D151, section 8.8 of
docs/stack-model.md). A worker in drain mode runs as a job that empties its
queue and exits, so a queue that is empty runs nothing, and an enqueue has
to start it; and a worker on Cloudflare Workers drains at each firing of a
Cron Trigger, a minute apart, so a message due before the next firing waits
for it unless something starts a drain sooner. A platform that can start
one gives each deployable that may enqueue to the queue its wakes in
QUEUE_WAKES, a JSON object keyed by the queue's `@queue` class, each member
the list of the wakes of the workers that drain it. A wake is one of two
kinds, and names no provider:

- an HTTP POST, with a bearer token when the wake says where one comes
  from: on gcp the Cloud Run job's run method and the metadata server's
  token (D145);
- a binding: an object of the deployable's own, which the entrypoint hands
  the waker (installQueueWakes in ./workers), whose create starts a run of
  the worker that waits until the message is due and then drains. On
  Cloudflare it is the worker's Workflow (D151), and the run's id names the
  second it is due in, so two wakes for one second start one run.

An enqueue says when its message is due (dueAt, epoch milliseconds by this
process's clock; now when it does not say). A message due past a wake's
horizon sends none, since what runs anyway finds it first: an HTTP wake's
horizon is the drain worker's, DRAIN_HORIZON_MS, within which a worker that
found its queue idle waits for the message; a binding's is
BINDING_WAKE_HORIZON_MS, the minute between two firings of the worker's
Cron Trigger.

A wake is a hint, never part of the write. It is sent after the enqueue's
statement, not awaited, and a wake that fails is logged and nothing else:
the worker's tick or its next firing finds the message. An HTTP wake goes
at most once per queue in WAKE_INTERVAL_MS per process; a binding's at
most once in that interval for due times a wake already sent covers, those
from its due time to BINDING_WAKE_COVER_MS after it, which the run's drain
waits for. settled resolves once every wake sent has been answered, which
an entrypoint awaits before its process or its invocation ends. It imports
nothing, so a Worker bundles it, where no QUEUE_WAKES is set unless the
platform binds one and it does nothing without.
*/

/** The environment variable that holds the wakes, as a platform sets it. */
export const QUEUE_WAKES_VARIABLE = 'QUEUE_WAKES';

/** The least time between two HTTP wakes of one queue from one process. */
export const WAKE_INTERVAL_MS = 5_000;

/**
 * How far ahead an HTTP wake looks: a drain worker that found its queue
 * idle waits for a message due within this long (the worker's
 * DRAIN_HORIZON_MS), and its tick finds one due later.
 */
export const HTTP_WAKE_HORIZON_MS = 300_000;

/**
 * How far ahead a binding's wake looks (D151): a message due later than
 * this is found by a firing of the worker's Cron Trigger, a minute apart,
 * which then runs it within that minute, so only one due before the next
 * firing needs a wake.
 */
export const BINDING_WAKE_HORIZON_MS = 60_000;

/**
 * How long after a wake's due time its run's drain waits for messages that
 * fall due (D151), so a wake for a due time this close after one already
 * sent within WAKE_INTERVAL_MS is not sent.
 */
export const BINDING_WAKE_COVER_MS = 10_000;

/** How long a wake's requests may take, the token's and the wake's each. */
const WAKE_TIMEOUT_MS = 10_000;

/**
 * How a queue's worker is started: a POST to url, with a bearer token when
 * token says where one comes from; or, with binding, the create of the
 * deployable's own object of that name (D151).
 */
export interface QueueWake {
  readonly url?: string;
  /** A GET of url with headers answers `{ access_token, expires_in }`. */
  readonly token?: { readonly url: string; readonly headers?: Readonly<Record<string, string>> };
  /** The name of the binding that starts a run of the worker: a Workflow's on Cloudflare. */
  readonly binding?: string;
}

/** What a binding wake's object does: start a run named id with params, which rejects when a run of that id exists. */
export interface QueueWakeBinding {
  create(options: { readonly id: string; readonly params: { readonly dueAt: number } }): Promise<unknown>;
}

/** What a wake needs of its host, which a test replaces. */
export interface QueueWakeHost {
  fetch(url: string, init: { method: string; headers: Record<string, string>; body?: string; signal?: AbortSignal }): Promise<{ ok: boolean; status: number; json(): Promise<unknown> }>;
  now(): number;
  /** Receives a wake that failed; the enqueue has succeeded whatever it says. */
  warn(message: string, fields: Readonly<Record<string, unknown>>): void;
  /** The object a binding wake names, or undefined when the host holds none, as a process outside a Worker. */
  binding?(name: string): QueueWakeBinding | undefined;
}

const defaultHost: QueueWakeHost = {
  fetch: (url, init) => fetch(url, init),
  now: () => Date.now(),
  warn: (message, fields) => console.warn(JSON.stringify({ level: 'warn', msg: message, ...fields })),
};

/**
 * Reads QUEUE_WAKES: a JSON object that gives each queue the list of its
 * wakes. An unset or empty variable is no wakes. Throws for a value that is
 * no such object.
 */
export function parseQueueWakes(value: string | undefined): ReadonlyMap<string, readonly QueueWake[]> {
  const wakes = new Map<string, readonly QueueWake[]>();
  if (value === undefined || value.trim() === '') return wakes;
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch (error) {
    throw new Error(`${QUEUE_WAKES_VARIABLE} is no JSON: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error(`${QUEUE_WAKES_VARIABLE} is no JSON object of lists of wakes by queue`);
  }
  for (const [queue, list] of Object.entries(parsed as Record<string, unknown>)) {
    if (!Array.isArray(list)) throw new Error(`${QUEUE_WAKES_VARIABLE}: queue ${queue} has no list of wakes`);
    const read: QueueWake[] = [];
    for (const wake of list as unknown[]) {
      const w = wake as { url?: unknown; token?: { url?: unknown; headers?: unknown }; binding?: unknown } | null;
      if (typeof w === 'object' && w !== null && w.url === undefined && w.binding !== undefined) {
        if (typeof w.binding !== 'string' || w.binding === '') throw new Error(`${QUEUE_WAKES_VARIABLE}: a wake of queue ${queue} names a binding that is no name`);
        read.push({ binding: w.binding });
        continue;
      }
      if (typeof w !== 'object' || w === null || typeof w.url !== 'string' || w.url === '') {
        throw new Error(`${QUEUE_WAKES_VARIABLE}: a wake of queue ${queue} has no url and no binding`);
      }
      if (w.token !== undefined && (typeof w.token !== 'object' || w.token === null || typeof w.token.url !== 'string' || w.token.url === '')) {
        throw new Error(`${QUEUE_WAKES_VARIABLE}: a wake of queue ${queue} names a token with no url`);
      }
      const headers: Record<string, string> = {};
      if (w.token?.headers !== undefined && typeof w.token.headers === 'object' && w.token.headers !== null) {
        for (const [name, text] of Object.entries(w.token.headers as Record<string, unknown>)) headers[name] = String(text);
      }
      read.push(w.token === undefined ? { url: w.url } : { url: w.url, token: { url: w.token.url as string, headers } });
    }
    wakes.set(queue, read);
  }
  return wakes;
}

/** Sends the wakes of a set of queues, as the module says. */
export interface QueueWaker {
  /**
   * Says a message was enqueued on the queue, due at dueAt, epoch
   * milliseconds, or at once when undefined. It returns at once; the
   * promise it returns settles when the wakes it sent, if any, have been
   * answered, and never rejects, which a test awaits and an enqueue does
   * not.
   */
  enqueued(queue: string, dueAt?: number): Promise<void>;
  /** Settles once every wake sent so far has been answered; never rejects. */
  settled(): Promise<void>;
}

/** The id of a binding wake's run: the second its due time falls in, so two wakes for one second start one run. */
export function wakeRunId(dueAt: number): string {
  return `due-${Math.ceil(dueAt / 1000)}`;
}

type HttpWake = QueueWake & { readonly url: string };

/** A waker over wakes, on host. */
export function queueWaker(wakes: ReadonlyMap<string, readonly QueueWake[]>, host: QueueWakeHost = defaultHost): QueueWaker {
  const last = new Map<string, number>();
  // The due times each binding was woken for within the last interval, and
  // when each was sent.
  const told = new Map<string, { readonly dueAt: number; readonly sent: number }[]>();
  const tokens = new Map<string, { value: string; until: number }>();
  const inFlight = new Set<Promise<void>>();
  const tokenOf = async (token: NonNullable<QueueWake['token']>): Promise<string> => {
    const held = tokens.get(token.url);
    if (held !== undefined && held.until > host.now()) return held.value;
    const response = await host.fetch(token.url, { method: 'GET', headers: { ...token.headers }, signal: AbortSignal.timeout(WAKE_TIMEOUT_MS) });
    if (!response.ok) throw new Error(`the token request answered ${response.status}`);
    const body = (await response.json()) as { access_token?: unknown; expires_in?: unknown } | null;
    if (typeof body?.access_token !== 'string' || body.access_token === '') throw new Error('the token request answered no access_token');
    // A token is used until a minute before it ends, or for a minute when it does not say.
    const seconds = typeof body.expires_in === 'number' && body.expires_in > 120 ? body.expires_in - 60 : 60;
    tokens.set(token.url, { value: body.access_token, until: host.now() + seconds * 1000 });
    return body.access_token;
  };
  const post = async (queue: string, wake: HttpWake): Promise<void> => {
    try {
      const headers: Record<string, string> = { 'content-type': 'application/json' };
      if (wake.token !== undefined) headers.authorization = `Bearer ${await tokenOf(wake.token)}`;
      const response = await host.fetch(wake.url, { method: 'POST', headers, body: '{}', signal: AbortSignal.timeout(WAKE_TIMEOUT_MS) });
      if (!response.ok) throw new Error(`the wake answered ${response.status}`);
    } catch (error) {
      host.warn("the wake of a queue's worker failed: its tick finds the message", { queue, error: error instanceof Error ? error.message : String(error) });
    }
  };
  const create = async (queue: string, name: string, dueAt: number): Promise<void> => {
    try {
      const binding = host.binding?.(name);
      if (binding === undefined) throw new Error(`the deployable holds no binding ${name}`);
      await binding.create({ id: wakeRunId(dueAt), params: { dueAt } });
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      // A run of that id exists: a wake for that second went out already.
      if (/already exists|already_exists|duplicate/i.test(message)) return;
      host.warn("the wake of a queue's worker failed: its next firing finds the message", { queue, binding: name, error: message });
    }
  };
  // Whether a binding was woken within the interval for a due time whose
  // run's drain covers dueAt.
  const covered = (name: string, dueAt: number, now: number): boolean => {
    const recent = (told.get(name) ?? []).filter(t => now - t.sent < WAKE_INTERVAL_MS);
    told.set(name, recent);
    return recent.some(t => t.dueAt <= dueAt && dueAt <= t.dueAt + BINDING_WAKE_COVER_MS);
  };
  return {
    async enqueued(queue, dueAt) {
      const list = wakes.get(queue);
      if (list === undefined || list.length === 0) return;
      const now = host.now();
      const due = dueAt === undefined || !Number.isFinite(dueAt) || dueAt < now ? now : dueAt;
      const sends: Promise<void>[] = [];
      const urls = list.filter((w): w is HttpWake => w.url !== undefined);
      if (urls.length > 0 && due - now <= HTTP_WAKE_HORIZON_MS) {
        const sent = last.get(queue);
        if (sent === undefined || now - sent >= WAKE_INTERVAL_MS) {
          last.set(queue, now);
          for (const wake of urls) sends.push(post(queue, wake));
        }
      }
      if (due - now <= BINDING_WAKE_HORIZON_MS) {
        for (const wake of list) {
          if (wake.url !== undefined || wake.binding === undefined || covered(wake.binding, due, now)) continue;
          told.get(wake.binding)?.push({ dueAt: due, sent: now });
          sends.push(create(queue, wake.binding, due));
        }
      }
      if (sends.length === 0) return;
      const work = Promise.all(sends).then(() => undefined);
      inFlight.add(work);
      try {
        await work;
      } finally {
        inFlight.delete(work);
      }
    },
    async settled() {
      while (inFlight.size > 0) await Promise.all([...inFlight]);
    },
  };
}

let processWaker: QueueWaker | undefined;

/**
 * Says a message was enqueued on the queue, due at dueAt (epoch
 * milliseconds; at once when undefined), to the process's waker: the one
 * an entrypoint installed (setProcessQueueWaker), or else one that reads
 * QUEUE_WAKES from the process's environment once. A process with none
 * sends nothing. It never throws, and nothing awaits the wake.
 */
export function wakeAfterEnqueue(queue: string, dueAt?: number): void {
  if (processWaker === undefined) {
    try {
      const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env;
      processWaker = queueWaker(parseQueueWakes(env?.[QUEUE_WAKES_VARIABLE]));
    } catch (error) {
      // Said once: the process then sends no wake, and its enqueues go on.
      defaultHost.warn('the wakes of the queues could not be read', { error: error instanceof Error ? error.message : String(error) });
      processWaker = queueWaker(new Map());
    }
  }
  try {
    void processWaker.enqueued(queue, dueAt).catch(() => undefined);
  } catch {
    // A waker that throws fails no enqueue.
  }
}

/**
 * Settles once every wake the process's waker has sent has been answered
 * (D145, D151): a job awaits it before it exits, and a Worker hands it to
 * its invocation's waitUntil, so a wake sent just before the end still
 * goes out. It never rejects.
 */
export function queueWakesSettled(): Promise<void> {
  return processWaker === undefined ? Promise.resolve() : processWaker.settled().catch(() => undefined);
}

/** Replaces the process's waker, for a test or an entrypoint; undefined reads QUEUE_WAKES again. */
export function setProcessQueueWaker(waker: QueueWaker | undefined): void {
  processWaker = waker;
}
