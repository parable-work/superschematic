/*
The wake a queue client sends after an enqueue (D145, section 8.8 of
docs/stack-model.md). A worker in drain mode runs as a job that empties its
queue and exits, so a queue that is empty runs nothing, and an enqueue has
to start it. A platform that can start one gives each deployable that may
enqueue to the queue its wakes in QUEUE_WAKES, a JSON object keyed by the
queue's `@queue` class, each member the list of the wakes of the workers
that drain it: an HTTP POST each, with a bearer token when the wake says
where one comes from. It names no provider: on gcp it is the Cloud Run
job's run method and the metadata server's token.

A wake is a hint, never part of the write. It is sent after the enqueue's
statement, not awaited, at most once per queue in WAKE_INTERVAL_MS per
process, and a wake that fails is logged and nothing else: the worker's
tick finds the message. It imports nothing, so a Worker bundles it, where
no QUEUE_WAKES is set and it does nothing.
*/

/** The environment variable that holds the wakes, as a platform sets it. */
export const QUEUE_WAKES_VARIABLE = 'QUEUE_WAKES';

/** The least time between two wakes of one queue from one process. */
export const WAKE_INTERVAL_MS = 5_000;

/** How long a wake's requests may take, the token's and the wake's each. */
const WAKE_TIMEOUT_MS = 10_000;

/** How a queue's worker is started: a POST to url, with a bearer token when token says where one comes from. */
export interface QueueWake {
  readonly url: string;
  /** A GET of url with headers answers `{ access_token, expires_in }`. */
  readonly token?: { readonly url: string; readonly headers?: Readonly<Record<string, string>> };
}

/** What a wake needs of its host, which a test replaces. */
export interface QueueWakeHost {
  fetch(url: string, init: { method: string; headers: Record<string, string>; body?: string; signal?: AbortSignal }): Promise<{ ok: boolean; status: number; json(): Promise<unknown> }>;
  now(): number;
  /** Receives a wake that failed; the enqueue has succeeded whatever it says. */
  warn(message: string, fields: Readonly<Record<string, unknown>>): void;
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
      const w = wake as { url?: unknown; token?: { url?: unknown; headers?: unknown } } | null;
      if (typeof w !== 'object' || w === null || typeof w.url !== 'string' || w.url === '') {
        throw new Error(`${QUEUE_WAKES_VARIABLE}: a wake of queue ${queue} has no url`);
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

/** Sends the wakes of a set of queues, each queue's at most once in WAKE_INTERVAL_MS. */
export interface QueueWaker {
  /**
   * Says a message was enqueued on the queue. It returns at once; the
   * promise it returns settles when the wakes it sent, if any, have been
   * answered, and never rejects, which a test awaits and an enqueue does
   * not.
   */
  enqueued(queue: string): Promise<void>;
}

/** A waker over wakes, on host. */
export function queueWaker(wakes: ReadonlyMap<string, readonly QueueWake[]>, host: QueueWakeHost = defaultHost): QueueWaker {
  const last = new Map<string, number>();
  const tokens = new Map<string, { value: string; until: number }>();
  const tokenOf = async (wake: QueueWake): Promise<string | undefined> => {
    if (wake.token === undefined) return undefined;
    const held = tokens.get(wake.token.url);
    if (held !== undefined && held.until > host.now()) return held.value;
    const response = await host.fetch(wake.token.url, { method: 'GET', headers: { ...wake.token.headers }, signal: AbortSignal.timeout(WAKE_TIMEOUT_MS) });
    if (!response.ok) throw new Error(`the token request answered ${response.status}`);
    const body = (await response.json()) as { access_token?: unknown; expires_in?: unknown } | null;
    if (typeof body?.access_token !== 'string' || body.access_token === '') throw new Error('the token request answered no access_token');
    // A token is used until a minute before it ends, or for a minute when it does not say.
    const seconds = typeof body.expires_in === 'number' && body.expires_in > 120 ? body.expires_in - 60 : 60;
    tokens.set(wake.token.url, { value: body.access_token, until: host.now() + seconds * 1000 });
    return body.access_token;
  };
  const send = async (queue: string, wake: QueueWake): Promise<void> => {
    try {
      const token = await tokenOf(wake);
      const headers: Record<string, string> = { 'content-type': 'application/json' };
      if (token !== undefined) headers.authorization = `Bearer ${token}`;
      const response = await host.fetch(wake.url, { method: 'POST', headers, body: '{}', signal: AbortSignal.timeout(WAKE_TIMEOUT_MS) });
      if (!response.ok) throw new Error(`the wake answered ${response.status}`);
    } catch (error) {
      host.warn("the wake of a queue's worker failed: its tick finds the message", { queue, error: error instanceof Error ? error.message : String(error) });
    }
  };
  return {
    async enqueued(queue) {
      const list = wakes.get(queue);
      if (list === undefined || list.length === 0) return;
      const now = host.now();
      const sent = last.get(queue);
      if (sent !== undefined && now - sent < WAKE_INTERVAL_MS) return;
      last.set(queue, now);
      await Promise.all(list.map(wake => send(queue, wake)));
    },
  };
}

let processWaker: QueueWaker | undefined;

/**
 * Says a message was enqueued on the queue, to the process's waker, which
 * reads QUEUE_WAKES once. A process with none, or a runtime with no
 * process, sends nothing. It never throws, and nothing awaits the wake.
 */
export function wakeAfterEnqueue(queue: string): void {
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
    void processWaker.enqueued(queue).catch(() => undefined);
  } catch {
    // A waker that throws fails no enqueue.
  }
}

/** Replaces the process's waker, for a test; undefined reads QUEUE_WAKES again. */
export function setProcessQueueWaker(waker: QueueWaker | undefined): void {
  processWaker = waker;
}
