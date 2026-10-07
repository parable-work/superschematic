/*
The controller pattern over the event stream: watch a namespace's log
from where the controller left off, filtered, and act on each event in
log order. Every controller wrote the same loop, and each copy got
something wrong: it replayed the whole log at each start, polled, lost
its place on a reconnect, or skipped an event its action failed on.

reconcile subscribes after the cursor its store holds, or from the head
on a first run, and runs the handler once per event, one at a time. The
cursor is saved after the handler returns, so an event is handled at
least once: a crash between the handler and the save runs it again on
the next start. A handler that throws is retried on the same event with
a backoff, and never skipped; a stop waits for the handler in progress.
A ready also saves the stream's cursor, which may be past events the
filters dropped, so a restart does not scan them again. A stored cursor
retention has pruned past ends the reconciler with the 410
`cursor_expired` problem: it does not skip what it never handled.
*/

import type { EngineClient } from './client.js';
import { DEFAULT_RECONNECT_INITIAL_MS, DEFAULT_RECONNECT_MAX_MS, type EventFilters, type EventSubscription } from './stream.js';
import type { ForwardedUser } from './transport.js';
import type { EngineEvent } from './types.js';

/** Where a reconciler keeps its cursor between runs: a file, a row, an instance of the engine's own. */
export interface CursorStore {
  /** The cursor of the last event handled, or undefined on a first run. */
  load(): number | undefined | Promise<number | undefined>;
  save(cursor: number): void | Promise<void>;
}

export interface ReconcileOptions extends EventFilters {
  /** Runs once per event, in log order; the cursor moves past the event once it returns. */
  readonly handle: (event: EngineEvent) => void | Promise<void>;
  /** The namespace; the client's by default. */
  readonly namespace?: string;
  /** Forward this end user rather than the client's own credentials. */
  readonly forward?: ForwardedUser;
  /** Where the cursor is kept; in memory by default, so a new process starts at `start`. */
  readonly cursor?: CursorStore;
  /** Where a run with no stored cursor starts: `head` by default, only what commits from now on; 0 replays the log. */
  readonly start?: number | 'head';
  /** The backoff of a failing handler and of reconnects. */
  readonly retry?: { readonly initialMs?: number; readonly maxMs?: number };
  /** Hears each failure: a handler's with its event and attempt, a dropped connection's with no event. */
  readonly onError?: (error: unknown, event: EngineEvent | undefined, attempt: number) => void;
  /** Hears each connection's ready, with the cursor it caught up at. */
  readonly onReady?: (cursor: number) => void;
}

/** memoryCursor is a cursor store in memory, which a new process does not see. */
export function memoryCursor(initial?: number): CursorStore & { readonly value: number | undefined } {
  let value = initial;
  return {
    get value() {
      return value;
    },
    load: () => value,
    save: (cursor) => {
      value = cursor;
    },
  };
}

/** A running reconciler. */
export class Reconciler {
  /** Settles when the reconciler ends: resolves after stop, rejects when the stream is refused or the store cannot load. */
  readonly done: Promise<void>;
  private readonly controller = new AbortController();
  private subscription: EventSubscription | undefined;
  private saved: number | undefined;

  constructor(
    private readonly client: EngineClient,
    private readonly options: ReconcileOptions
  ) {
    if (typeof options.handle !== 'function') {
      throw new TypeError('reconcile: handle is a function of an event');
    }
    this.done = this.run();
    // A caller that only stops it still sees a refusal through stop().
    this.done.catch(() => undefined);
  }

  /** The cursor last saved. */
  get cursor(): number | undefined {
    return this.saved;
  }

  /** stop ends the reconciler once the handler in progress returns, and resolves when it has. */
  async stop(): Promise<void> {
    this.controller.abort();
    this.subscription?.close();
    await this.done;
  }

  private async run(): Promise<void> {
    const options = this.options;
    const store = options.cursor ?? memoryCursor();
    const initialMs = options.retry?.initialMs ?? DEFAULT_RECONNECT_INITIAL_MS;
    const maxMs = options.retry?.maxMs ?? DEFAULT_RECONNECT_MAX_MS;
    const stored = await store.load();
    if (this.stopped) {
      return;
    }
    this.saved = stored;
    const subscription = this.client.events.subscribe({
      schema: options.schema,
      instanceId: options.instanceId,
      kinds: options.kinds,
      behaviors: options.behaviors,
      exclude: options.exclude,
      ...(options.namespace !== undefined ? { namespace: options.namespace } : {}),
      ...(options.forward !== undefined ? { forward: options.forward } : {}),
      after: stored ?? options.start ?? 'head',
      reconnect: { initialMs, maxMs },
      onReconnect: (error, attempt) => options.onError?.(error, undefined, attempt),
    });
    this.subscription = subscription;
    if (this.stopped) {
      subscription.close();
      return;
    }
    for await (const message of subscription) {
      if (message.type === 'ready') {
        await this.save(store, subscription.cursor ?? message.cursor);
        options.onReady?.(message.cursor);
        continue;
      }
      const event = message.event;
      for (let attempt = 1; ; attempt++) {
        try {
          await options.handle(event);
          break;
        } catch (error) {
          options.onError?.(error, event, attempt);
          await this.sleep(Math.min(maxMs, initialMs * 2 ** (attempt - 1)));
          if (this.stopped) {
            return;
          }
        }
      }
      await this.save(store, event.cursor);
    }
  }

  private get stopped(): boolean {
    return this.controller.signal.aborted;
  }

  // A failed save is reported and the run goes on: the event is handled
  // again after a restart, as at least once allows.
  private async save(store: CursorStore, cursor: number): Promise<void> {
    if (this.saved !== undefined && cursor <= this.saved) {
      return;
    }
    try {
      await store.save(cursor);
      this.saved = cursor;
    } catch (error) {
      this.options.onError?.(error, undefined, 1);
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => {
      const signal = this.controller.signal;
      if (signal.aborted) {
        resolve();
        return;
      }
      const done = () => {
        clearTimeout(timer);
        signal.removeEventListener('abort', done);
        resolve();
      };
      const timer = setTimeout(done, ms);
      signal.addEventListener('abort', done, { once: true });
    });
  }
}

/**
 * reconcile runs a handler over a namespace's events, filtered, in log
 * order, from the cursor its store holds: the controller pattern.
 */
export function reconcile(client: EngineClient, options: ReconcileOptions): Reconciler {
  return new Reconciler(client, options);
}
