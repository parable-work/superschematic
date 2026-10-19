/*
The engine a generated TypeScript server opens while it serves (D58), the
twin of the Go binding's engine.Background: the server starts while its
database is not up yet, as its pools do, and reports the engine
unavailable, in its readiness and on the engine's routes, until the engine
opens and its setup passes.

openEngineInBackground opens the Rust engine with openEngineAsync, off the
JavaScript thread, and tries again after each failure, the open's or the
setup's, waiting a second, doubling to thirty, until it opens or the
background closes. A setup is what runs on the engine before it is handed
out: a server deploys its database's engine document, a runner starts the
engine's runner; a setup that fails closes the engine, and the next attempt
opens it again.

It imports nothing of the TypeScript engine, so a server that loads it
loads the addon's facade alone (the package's ./server entry).
*/

import { AsyncNativeEngine } from '../native/async.js';
import type { NativeEngineOptions } from '../native/options.js';

/** The first wait between a background's attempts, which doubles up to DEFAULT_RETRY_MAX_MS, unless its options set them. */
export const DEFAULT_RETRY_MIN_MS = 1_000;
export const DEFAULT_RETRY_MAX_MS = 30_000;

/** What a background engine answers until it opens: its database has not answered yet, or its setup has not finished; cause is the last attempt's failure. */
export class EngineNotOpenError extends Error {
  constructor(cause?: unknown) {
    super(cause === undefined ? 'the engine is not open yet' : `the engine is not open yet: ${messageOf(cause)}`, cause === undefined ? undefined : { cause });
    this.name = 'EngineNotOpenError';
  }
}

/** How a background opens its engine. */
export interface BackgroundOptions {
  /**
   * Runs on the engine once it opens, before the background hands it out:
   * a server's deploy, a runner's runner.start. A rejection closes the
   * engine, and the next attempt opens it again. signal aborts when the
   * background closes.
   */
  readonly setup?: (engine: AsyncNativeEngine, signal: AbortSignal) => Promise<void> | void;
  /** The wait between attempts, which doubles from retryMinMs to retryMaxMs: DEFAULT_RETRY_MIN_MS and DEFAULT_RETRY_MAX_MS unless set. */
  readonly retryMinMs?: number;
  readonly retryMaxMs?: number;
  /** Told of each failed attempt and how long the background waits before the next. */
  readonly onError?: (error: unknown, retryMs: number) => void;
  /** Told of the open engine, once its setup passed. */
  readonly onOpen?: (engine: AsyncNativeEngine) => void;
}

/**
 * An engine a server opens while it serves (openEngineInBackground). Its
 * engine is the open engine once it opens; until then engine() throws
 * EngineNotOpenError, with why the last attempt failed.
 */
export class EngineBackground {
  #engine: AsyncNativeEngine | undefined;
  #error: unknown;
  #closed = false;
  readonly #stop = new AbortController();
  readonly #opened: Promise<void>;
  #open!: () => void;

  /** @internal Use openEngineInBackground. */
  constructor(options: NativeEngineOptions, background: BackgroundOptions = {}) {
    this.#opened = new Promise<void>((resolve) => {
      this.#open = resolve;
    });
    void this.#run(options, background);
  }

  /** The open engine, or undefined until it opens and once the background closed. */
  get current(): AsyncNativeEngine | undefined {
    return this.#closed ? undefined : this.#engine;
  }

  /** Whether the background closed. */
  get closed(): boolean {
    return this.#closed;
  }

  /** The open engine, or a throw: EngineNotOpenError until it opens, an Error once the background closed. */
  engine(): AsyncNativeEngine {
    if (this.#closed) {
      throw new Error('the engine is closed');
    }
    if (this.#engine === undefined) {
      throw new EngineNotOpenError(this.#error);
    }
    return this.#engine;
  }

  /**
   * wait resolves with the engine once it opens, or rejects when signal
   * aborts first, or the background closes: what a job, a worker or an
   * implementation awaits before it calls the engine.
   */
  async wait(signal?: AbortSignal): Promise<AsyncNativeEngine> {
    if (this.#engine !== undefined || this.#closed) {
      return this.engine();
    }
    signal?.throwIfAborted();
    await new Promise<void>((resolve, reject) => {
      const aborted = (): void => {
        cleanup();
        reject(signal?.reason instanceof Error ? signal.reason : new EngineNotOpenError(this.#error));
      };
      const stopped = (): void => {
        cleanup();
        resolve();
      };
      const cleanup = (): void => {
        signal?.removeEventListener('abort', aborted);
        this.#stop.signal.removeEventListener('abort', stopped);
      };
      signal?.addEventListener('abort', aborted, { once: true });
      this.#stop.signal.addEventListener('abort', stopped, { once: true });
      void this.#opened.then(stopped);
    });
    return this.engine();
  }

  /**
   * ready resolves while the engine is open and ready, and rejects with
   * why not: not open yet, closed, poisoned, or its database does not
   * answer. It is what a server's readiness checks.
   */
  async ready(): Promise<void> {
    const health = await this.engine().health();
    if (health.ready) {
      return;
    }
    if (health.closed) {
      throw new Error('the engine is closed');
    }
    if (health.poisoned) {
      throw new Error('a panic inside a transaction poisoned the engine; restart it');
    }
    throw new Error(`the database does not answer: ${health.error ?? 'unknown'}`);
  }

  /**
   * close stops trying to open the engine and closes it if it is open. An
   * attempt in flight is not waited for: what it opens closes when it
   * returns. Closing a closed background does nothing.
   */
  close(): void {
    if (this.#closed) {
      return;
    }
    this.#closed = true;
    this.#stop.abort(new Error('the engine is closed'));
    const engine = this.#engine;
    this.#engine = undefined;
    engine?.close();
  }

  async #run(options: NativeEngineOptions, background: BackgroundOptions): Promise<void> {
    const min = background.retryMinMs !== undefined && background.retryMinMs > 0 ? background.retryMinMs : DEFAULT_RETRY_MIN_MS;
    const max = Math.max(background.retryMaxMs ?? DEFAULT_RETRY_MAX_MS, min);
    let wait = min;
    for (;;) {
      let engine: AsyncNativeEngine | undefined;
      try {
        engine = await AsyncNativeEngine.openAsync(options);
        if (this.#closed) {
          engine.close();
          return;
        }
        await background.setup?.(engine, this.#stop.signal);
        if (this.#closed) {
          engine.close();
          return;
        }
        this.#engine = engine;
        this.#error = undefined;
        this.#open();
        // The engine is open: what onOpen throws is not the open's failure.
        try {
          background.onOpen?.(engine);
        } catch {
          // A listener's failure leaves the engine open.
        }
        return;
      } catch (error) {
        engine?.close();
        this.#error = error;
        if (this.#closed) {
          return;
        }
        background.onError?.(error, wait);
        await sleep(wait, this.#stop.signal);
        if (this.#closed) {
          return;
        }
        wait = Math.min(wait * 2, max);
      }
    }
  }
}

/**
 * openEngineInBackground opens the Rust engine on options, which are data
 * alone, off the JavaScript thread, trying again after each failure, the
 * open's or the setup's, until it opens or the background closes.
 */
export function openEngineInBackground(options: NativeEngineOptions, background: BackgroundOptions = {}): EngineBackground {
  return new EngineBackground(options, background);
}

// sleep resolves after ms, or at once when signal aborts. Its timer holds
// the process open while the engine is not open yet, so a job or a worker
// that waits for it does not exit with the wait pending.
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const done = (): void => {
      clearTimeout(timer);
      signal.removeEventListener('abort', done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener('abort', done, { once: true });
  });
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
