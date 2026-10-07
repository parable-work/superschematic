/*
A worker runs a handler over the instances it claims from a schema that
composes Queue and Lease, through the engine's HTTP API (the client of
@superschematic/engine/client). It is the loop every process that works
a queue otherwise writes by hand, with the edge cases each copy got wrong
settled once:

- It claims with `claimNext` (its `match`, `assignedOnly` and `ttlMs`),
  up to `concurrency` at once. An empty queue is watched, not polled: the
  schema's event stream wakes it, and between events it looks again
  after a backoff that doubles, with `countClaimable`, a read that does
  not take the engine's write lock, claiming only when it counts work.
- Each job heartbeats at the claim's interval, presenting the token as
  Lease's precondition, and acknowledges in each heartbeat the directives
  its listeners heard, so an acknowledgement costs no write of its own.
- The lease is lost when Lease vetoes a heartbeat or a fenced write
  (`lapsed`, `token_stale`, `not_holder`, `not_leased`), when the
  instance is gone, or when no heartbeat has succeeded for the lease's
  length since the last one that did was sent. The job's signal aborts,
  its fenced writes stop, and the worker writes nothing more for it: no
  attempt, no release.
- The handler's outcome is the terminal step: `{ transition }` records a
  successful attempt when the type composes Retries, moves the status
  through Workflow and releases; `{ release: true }` or nothing hands it
  back; a WorkFailure records its class and hands it back or abandons it;
  any other error abandons it, counting toward Lease's `maxExpiries`.
- With `presence`, it stands for a Presence instance as the client's
  principal: it beats it once before it claims, then on a timer of its
  own, at most half the Presence config's `ttlMs` apart, whatever its
  handlers are doing. While the engine refuses a beat, or none has
  succeeded for the `ttlMs`, it claims nothing; the jobs it holds go on
  under their own heartbeats, since a miss spares a lease renewed after
  the last beat and expires the rest, which their heartbeats then find.
- A stop claims nothing more, waits for the jobs it holds up to a
  timeout, then aborts the rest and releases their leases, which counts
  nothing, rather than letting them lapse. It beats until then.
*/

import { isProblem, type CallOptions, type EngineClient, type EventSubscription, type JSONObject } from '@superschematic/engine/client';

import { DEFAULT_TTL_MS } from '../defaults.js';
import { PresenceLostError, WorkFailure, type AttemptReport, type PresenceLossReason } from './errors.js';
import { Job, type Claim } from './job.js';

/** A `match` value: one value, or a list one of whose values an instance holds. */
export type MatchValue = string | number | boolean | ReadonlyArray<string | number | boolean>;

/**
 * What the handler returns: the work is done, and `transition` is the
 * Workflow state it moves to, with `attempt` reported to Retries as a
 * success; or it is handed back as it is (`release`, or nothing), which
 * records no attempt. A failure is a WorkFailure the handler throws.
 */
export type WorkResult = { readonly transition: string; readonly attempt?: AttemptReport } | { readonly release: true };

/** The Presence instance a worker stands for, in the worker's namespace. */
export interface PresenceOptions {
  /** The schema that composes Presence. */
  readonly schema: string;
  /** The instance whose `principalField` holds the client's principal. */
  readonly id: string;
  /** Milliseconds between beats: a third of the Presence config's `ttlMs` by default, and at most half of it. */
  readonly beatMs?: number;
}

export interface QueueWorkerOptions<T = JSONObject> {
  /** The schema to claim from: it composes Queue and Lease. */
  readonly schema: string;
  /** The namespace; the client's by default. */
  readonly namespace?: string;
  /** Values of the Queue config's `match` fields an instance must hold. */
  readonly match?: Readonly<Record<string, MatchValue>>;
  /** Only instances assigned to the caller. */
  readonly assignedOnly?: boolean;
  /** The lease's length each claim asks for, at most the Lease config's `ttlMs`; the config's when absent. */
  readonly ttlMs?: number;
  /** How many instances it works at once; 1 by default. */
  readonly concurrency?: number;
  /** Milliseconds between heartbeats, at most the claim's `heartbeatMs`, which is the default. */
  readonly heartbeatMs?: number;
  /** The wait between looks at an empty queue: from `initialMs` (250), doubling to `maxMs` (10000). */
  readonly idle?: { readonly initialMs?: number; readonly maxMs?: number };
  /** Whether the schema's event stream wakes an idle worker; true by default. Off, it only looks after each wait. */
  readonly wakeOnEvents?: boolean;
  /** The Presence instance the worker stands for: it beats it while it runs, and claims only while its presence holds. */
  readonly presence?: PresenceOptions;
  /** Runs once per claimed instance. */
  readonly handle: (job: Job<T>) => WorkResult | void | Promise<WorkResult | void>;
  /** Hears what fails outside a handler's result: a claim, a heartbeat, a beat, a lost presence, a terminal step, a listener, an unclassified error. */
  readonly onError?: (error: unknown, job: Job<T> | undefined) => void;
}

export interface StopOptions {
  /** Wait for the jobs it holds before releasing them; true by default. */
  readonly drain?: boolean;
  /** How long a drain waits before it aborts and releases the rest; 30000 by default. */
  readonly timeoutMs?: number;
}

export const DEFAULT_IDLE_INITIAL_MS = 250;
export const DEFAULT_IDLE_MAX_MS = 10_000;
export const DEFAULT_DRAIN_TIMEOUT_MS = 30_000;

type Wake = 'event' | 'timer' | 'capacity' | 'presence' | 'stop';

interface Running<T> {
  readonly job: Job<T>;
  /** Settles once the job is over: released, lost, or given up at a stop. */
  readonly done: Promise<void>;
  stopHeartbeats(): void;
}

/** A worker over one schema's queue. */
export class QueueWorker<T = JSONObject> {
  private readonly namespace: string;
  private readonly concurrency: number;
  private readonly idleInitialMs: number;
  private readonly idleMaxMs: number;
  private readonly running = new Set<Running<T>>();
  private started = false;
  private stopping = false;
  private stopped: Promise<void> | undefined;
  private loop: Promise<void> | undefined;
  private wakeLoop: ((reason: Wake) => void) | undefined;
  private woken = false;
  private subscription: EventSubscription | undefined;
  private leaseMs = DEFAULT_TTL_MS;
  private retries = false;
  // The presence: whether it holds, absent until the first beat when the
  // worker has one; its length and interval; the timers of the next beat
  // and of its lapse.
  private presentNow: boolean;
  private presenceMs = 0;
  private beatMs = 0;
  private beatTimer: ReturnType<typeof setTimeout> | undefined;
  private presenceDeadline: ReturnType<typeof setTimeout> | undefined;
  private beatsOver = false;
  private readonly beating = new AbortController();

  constructor(
    readonly client: EngineClient,
    private readonly options: QueueWorkerOptions<T>
  ) {
    if (typeof options.schema !== 'string' || options.schema === '') {
      throw new TypeError('QueueWorker: schema names the schema to claim from');
    }
    if (typeof options.handle !== 'function') {
      throw new TypeError('QueueWorker: handle is a function of a job');
    }
    const concurrency = options.concurrency ?? 1;
    if (!Number.isInteger(concurrency) || concurrency < 1) {
      throw new TypeError(`QueueWorker: concurrency is a positive integer, got ${String(options.concurrency)}`);
    }
    if (options.presence !== undefined) {
      for (const name of ['schema', 'id'] as const) {
        if (typeof options.presence[name] !== 'string' || options.presence[name] === '') {
          throw new TypeError(`QueueWorker: presence.${name} names the Presence instance the worker stands for`);
        }
      }
    }
    for (const [name, value] of [
      ['ttlMs', options.ttlMs],
      ['heartbeatMs', options.heartbeatMs],
      ['idle.initialMs', options.idle?.initialMs],
      ['idle.maxMs', options.idle?.maxMs],
      ['presence.beatMs', options.presence?.beatMs],
    ] as const) {
      if (value !== undefined && (!Number.isInteger(value) || value < 1)) {
        throw new TypeError(`QueueWorker: ${name} is a positive integer of milliseconds, got ${String(value)}`);
      }
    }
    this.namespace = options.namespace ?? client.namespace;
    this.presentNow = options.presence === undefined;
    this.concurrency = concurrency;
    this.idleInitialMs = options.idle?.initialMs ?? DEFAULT_IDLE_INITIAL_MS;
    this.idleMaxMs = Math.max(this.idleInitialMs, options.idle?.maxMs ?? DEFAULT_IDLE_MAX_MS);
  }

  /** How many jobs it holds. */
  get active(): number {
    return this.running.size;
  }

  /** The jobs it holds. */
  get jobs(): Array<Job<T>> {
    return [...this.running].map((running) => running.job);
  }

  /** Whether its presence holds, so it claims; true for a worker with no `presence`. */
  get present(): boolean {
    return this.presentNow;
  }

  /**
   * start reads the schema's describe document, which must compose Queue
   * and Lease, and starts claiming. With `presence`, it reads that
   * schema's too, which must compose Presence, and beats the instance
   * first: a refused or failed beat rejects the start. It resolves once
   * the worker runs.
   */
  async start(): Promise<void> {
    if (this.started) {
      throw new Error('QueueWorker: start runs once');
    }
    this.started = true;
    const described = await this.client.schemas.describe(this.options.schema, this.call());
    const names = described.behaviors.map((behavior) => behavior.name);
    const missing = ['Lease', 'Queue'].filter((name) => !names.includes(name));
    if (missing.length > 0) {
      throw new TypeError(`QueueWorker: ${this.options.schema} does not compose ${missing.join(' or ')}, so it has no claimable work`);
    }
    this.retries = names.includes('Retries');
    const lease = described.behaviors.find((behavior) => behavior.name === 'Lease')?.config as { ttlMs?: number } | undefined;
    this.leaseMs = this.options.ttlMs ?? lease?.ttlMs ?? DEFAULT_TTL_MS;
    const presence = this.options.presence;
    if (presence !== undefined) {
      const stands = await this.client.schemas.describe(presence.schema, this.call());
      const config = stands.behaviors.find((behavior) => behavior.name === 'Presence')?.config as { ttlMs?: number } | undefined;
      if (config?.ttlMs === undefined) {
        throw new TypeError(`QueueWorker: ${presence.schema} does not compose Presence, so it has no instance to beat`);
      }
      this.presenceMs = config.ttlMs;
      this.beatMs = Math.max(1, Math.min(presence.beatMs ?? Math.floor(this.presenceMs / 3), Math.floor(this.presenceMs / 2)));
      await this.beat();
      this.beatTimer = setTimeout(() => void this.beats(), this.beatMs);
    }
    if (this.options.wakeOnEvents !== false) {
      this.watch();
    }
    this.loop = this.claimLoop();
  }

  /**
   * stop claims nothing more, waits for the jobs it holds (drain), up to
   * timeoutMs, then aborts the rest with a WorkerStoppedError and releases
   * their leases. It resolves once nothing is held.
   */
  stop(options: StopOptions = {}): Promise<void> {
    this.stopped ??= this.shutdown(options);
    return this.stopped;
  }

  private call(): CallOptions {
    return { namespace: this.namespace };
  }

  private report(error: unknown, job?: Job<T>): void {
    try {
      this.options.onError?.(error, job);
    } catch {
      // A listener's own failure is not the worker's.
    }
  }

  private wake(reason: Wake): void {
    this.woken = true;
    this.wakeLoop?.(reason);
  }

  // The schema's events wake an idle worker: a create, a release, an
  // expiry, a blocker finishing. Heartbeats are left out. A refused stream
  // leaves the worker looking on its backoff alone.
  private watch(): void {
    const subscription = this.client.events.subscribe({
      ...this.call(),
      schema: this.options.schema,
      exclude: ['heartbeat'],
      after: 'head',
      reconnect: { initialMs: this.idleInitialMs, maxMs: this.idleMaxMs },
    });
    this.subscription = subscription;
    void (async () => {
      try {
        for await (const message of subscription) {
          if (message.type === 'event') {
            this.wake('event');
          }
        }
      } catch (error) {
        if (!this.stopping) {
          this.report(error);
        }
      }
    })();
  }

  private async claimLoop(): Promise<void> {
    let backoff = this.idleInitialMs;
    let look = true;
    while (!this.stopping) {
      // At capacity, or absent, it waits for a job to end, its presence
      // to come back, or the stop.
      if (this.running.size >= this.concurrency || !this.presentNow) {
        await this.idle(undefined);
        continue;
      }
      if (!look) {
        const reason = await this.idle(backoff);
        if (this.stopping) {
          return;
        }
        if (reason === 'capacity' || reason === 'presence' || !this.presentNow) {
          continue;
        }
        if (reason === 'timer') {
          backoff = Math.min(this.idleMaxMs, backoff * 2);
        }
        try {
          const { count } = await this.client.instances.invokeSchema<{ count: number }>(this.options.schema, 'countClaimable', this.claimParams(false), this.call());
          look = count > 0;
        } catch (error) {
          this.report(error);
        }
        continue;
      }
      this.woken = false;
      const sentAt = Date.now();
      let claimed: Claim | null;
      try {
        ({ claimed } = await this.client.instances.invokeSchema<{ claimed: Claim | null }>(this.options.schema, 'claimNext', this.claimParams(true), this.call()));
      } catch (error) {
        this.report(error);
        look = false;
        continue;
      }
      if (this.stopping) {
        // A claim the stop raced: hand it back at once.
        if (claimed !== null) {
          await this.release(new Job<T>(this.client, this.namespace, this.options.schema, claimed, (error) => this.report(error)), false);
        }
        return;
      }
      if (claimed === null) {
        look = false;
        continue;
      }
      backoff = this.idleInitialMs;
      this.run(claimed, sentAt);
    }
  }

  private claimParams(claim: boolean): JSONObject {
    return {
      ...(this.options.match !== undefined ? { match: this.options.match as JSONObject } : {}),
      ...(this.options.assignedOnly !== undefined ? { assignedOnly: this.options.assignedOnly } : {}),
      ...(claim && this.options.ttlMs !== undefined ? { ttlMs: this.options.ttlMs } : {}),
    };
  }

  // idle waits for a wake: an event, capacity freed, the stop, or the
  // timer when one is given. A wake that came while the worker was busy
  // ends the wait at once.
  private idle(ms: number | undefined): Promise<Wake> {
    if (this.stopping) {
      return Promise.resolve('stop');
    }
    if (this.woken && ms !== undefined) {
      this.woken = false;
      return Promise.resolve('event');
    }
    return new Promise((resolve) => {
      const timer = ms === undefined ? undefined : setTimeout(() => finish('timer'), ms);
      const finish = (reason: Wake) => {
        clearTimeout(timer);
        this.wakeLoop = undefined;
        // An event heard while at capacity or absent is kept for the next
        // wait, and so freed capacity and a presence back look at once.
        if (reason === 'event' || reason === 'timer') {
          this.woken = false;
        }
        resolve(reason);
      };
      this.wakeLoop = (reason) => {
        // At capacity or absent, only freed capacity, the presence back
        // or the stop ends the wait.
        if (ms === undefined && reason === 'event') {
          return;
        }
        finish(reason);
      };
    });
  }

  private run(claim: Claim, sentAt: number): void {
    const job = new Job<T>(this.client, this.namespace, this.options.schema, claim, (error) => this.report(error, job));
    const beats = this.heartbeats(job, sentAt);
    const running: Running<T> = {
      job,
      stopHeartbeats: beats,
      done: this.work(job, () => beats()).finally(() => {
        beats();
        this.running.delete(running);
        this.wake('capacity');
      }),
    };
    this.running.add(running);
  }

  private async work(job: Job<T>, stopHeartbeats: () => void): Promise<void> {
    let outcome: { ok: true; value: WorkResult | void } | { ok: false; error: unknown };
    try {
      outcome = { ok: true, value: await this.options.handle(job) };
    } catch (error) {
      outcome = { ok: false, error };
    }
    // Lost, or released at a stop: there is nothing left to write.
    if (job.state !== 'working') {
      return;
    }
    job.state = 'finishing';
    if (outcome.ok) {
      const result = outcome.value;
      if (result !== undefined && result !== null && 'transition' in result) {
        if (this.retries && !(await this.step(job, 'Retries', 'recordAttempt', { ...(result.attempt ?? {}) } as JSONObject))) {
          return;
        }
        if (!(await this.step(job, 'Workflow', 'transition', { to: result.transition }))) {
          return;
        }
      }
      stopHeartbeats();
      await this.release(job, false);
      return;
    }
    const error = outcome.error;
    const failure = error instanceof WorkFailure ? error : undefined;
    if (failure === undefined) {
      this.report(error, job);
    }
    if (this.retries && failure?.failure !== undefined) {
      if (!(await this.step(job, 'Retries', 'recordAttempt', { ...(failure.attempt ?? {}), failure: failure.failure } as JSONObject))) {
        return;
      }
    }
    stopHeartbeats();
    await this.release(job, failure === undefined ? true : failure.abandon);
  }

  // A terminal step through the fenced handle; false when the lease is
  // gone, so nothing more is written. A refusal of another kind is
  // reported, and the job still ends with its release.
  private async step(job: Job<T>, behavior: string, operation: string, params: JSONObject): Promise<boolean> {
    try {
      await job.instance.invoke(operation, params);
    } catch (error) {
      if (job.lost !== undefined) {
        return false;
      }
      this.report(new Error(`${behavior}.${operation} of ${job.schema} ${job.id} failed: ${error instanceof Error ? error.message : String(error)}`, { cause: error }), job);
    }
    return job.lost === undefined;
  }

  private async release(job: Job<T>, abandon: boolean): Promise<void> {
    try {
      await job.instance.invoke('release', abandon ? { abandon: true } : {});
    } catch (error) {
      if (job.lost === undefined) {
        this.report(error, job);
      }
    }
    job.lose('released');
  }

  // heartbeats renews the job's lease until the returned function stops
  // it, and loses the job when Lease refuses, the instance is gone, or no
  // renewal succeeds within the lease's length.
  private heartbeats(job: Job<T>, claimSentAt: number): () => void {
    const intervalMs = Math.max(1, Math.min(this.options.heartbeatMs ?? job.claim.heartbeatMs, job.claim.heartbeatMs));
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let deadline: ReturnType<typeof setTimeout> | undefined;
    let over = false;
    const stop = () => {
      over = true;
      clearTimeout(timer);
      clearTimeout(deadline);
      controller.abort();
    };
    // The lease lasts at least its length from when the request that
    // renewed it was sent, by this process's clock, whatever the engine's.
    const renewedFrom = (sentAt: number) => {
      clearTimeout(deadline);
      deadline = setTimeout(() => {
        job.lose('unrenewed');
        stop();
      }, Math.max(0, sentAt + this.leaseMs - Date.now()));
    };
    const beat = async () => {
      if (over || job.lost !== undefined) {
        return;
      }
      const sentAt = Date.now();
      const acknowledge = job.unacknowledged;
      try {
        const { directives } = await job.instance.heartbeat(acknowledge, controller.signal);
        if (over) {
          return;
        }
        renewedFrom(sentAt);
        job.acknowledged(acknowledge);
        job.deliver(directives ?? []);
      } catch (error) {
        if (over || job.lost !== undefined) {
          stop();
          return;
        }
        this.report(error, job);
      }
      if (!over) {
        timer = setTimeout(beat, Math.max(0, sentAt + intervalMs - Date.now()));
      }
    };
    renewedFrom(claimSentAt);
    timer = setTimeout(beat, intervalMs);
    job.signal.addEventListener('abort', () => {
      if (job.lost !== undefined) {
        stop();
      }
    });
    return stop;
  }

  // beat beats the presence instance as the client's principal. A success
  // holds the presence for its ttlMs from when the request was sent, by
  // this process's clock, and brings back one that was gone. It throws what
  // failed: start rejects with it, and beats reports it.
  private async beat(): Promise<void> {
    const presence = this.options.presence as PresenceOptions;
    const sentAt = Date.now();
    await this.client.instances.invoke(presence.schema, presence.id, 'beat', {}, { ...this.call(), signal: this.beating.signal });
    if (this.beatsOver) {
      return;
    }
    clearTimeout(this.presenceDeadline);
    this.presenceDeadline = setTimeout(() => this.absent('lapsed'), Math.max(0, sentAt + this.presenceMs - Date.now()));
    if (!this.presentNow) {
      this.presentNow = true;
      // The first beat, in start, comes before the loop, which claims at once.
      if (this.loop !== undefined) {
        this.wake('presence');
      }
    }
  }

  // beats beats on the worker's own timer, beatMs from when each beat was
  // sent, whatever the claim loop and the handlers are doing, until the
  // stop has released what the worker held. A failed beat is reported, a
  // refused one loses the presence at once, and the next is tried at its
  // time.
  private async beats(): Promise<void> {
    const sentAt = Date.now();
    try {
      await this.beat();
    } catch (error) {
      if (!this.beatsOver) {
        this.report(error);
        if (refused(error)) {
          this.absent('refused', error);
        }
      }
    }
    if (!this.beatsOver) {
      this.beatTimer = setTimeout(() => void this.beats(), Math.max(0, sentAt + this.beatMs - Date.now()));
    }
  }

  private absent(reason: PresenceLossReason, cause?: unknown): void {
    clearTimeout(this.presenceDeadline);
    if (!this.presentNow) {
      return;
    }
    this.presentNow = false;
    this.report(new PresenceLostError(reason, this.options.presence as PresenceOptions, cause));
  }

  private async shutdown(options: StopOptions): Promise<void> {
    this.stopping = true;
    this.wake('stop');
    this.subscription?.close();
    await this.loop;
    const drain = options.drain ?? true;
    const timeoutMs = drain ? (options.timeoutMs ?? DEFAULT_DRAIN_TIMEOUT_MS) : 0;
    const all = () => Promise.all([...this.running].map((running) => running.done)).then(() => undefined);
    if (timeoutMs > 0 && this.running.size > 0) {
      let timer: ReturnType<typeof setTimeout> | undefined;
      await Promise.race([all(), new Promise<void>((resolve) => (timer = setTimeout(resolve, timeoutMs)))]);
      clearTimeout(timer);
    }
    // What is still working is aborted and handed back; a job already
    // finishing ends with its own release.
    const releases: Array<Promise<void>> = [];
    for (const running of this.running) {
      const job = running.job;
      if (job.state === 'working') {
        job.stopped();
        job.state = 'finishing';
        running.stopHeartbeats();
        releases.push(this.release(job, false));
      } else {
        releases.push(running.done);
      }
    }
    await Promise.all(releases);
    // Nothing is held: the beats stop, and the instance is missed a ttlMs
    // later, with no lease left to release.
    this.beatsOver = true;
    clearTimeout(this.beatTimer);
    clearTimeout(this.presenceDeadline);
    this.beating.abort();
  }
}

// refused says whether the engine refused a beat, rather than it failing on
// the way: a problem with a 4xx status but 408 and 429, which no retry of
// the same beat gets past.
function refused(error: unknown): boolean {
  return isProblem(error) && error.status >= 400 && error.status < 500 && error.status !== 408 && error.status !== 429;
}

