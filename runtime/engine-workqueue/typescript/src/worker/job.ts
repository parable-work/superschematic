/*
One claimed instance while a worker holds its lease: the handler's view
of it (Job), and the fenced handle its writes go through (LeasedInstance).

Every write of the handle presents the lease's token as Lease's
precondition, so once another lease replaces this one the engine refuses
it (`token_stale`). The handle also stops writing on its own once the job
knows the lease is gone: a write after that throws LeaseLostError and is
not sent, so a worker that a partition cut off from the engine does not
write when the partition heals.
*/

import { isProblem, isVeto, type CallOptions, type EngineClient, type Instance, type JSONObject, type OperationOutcome, type Preconditions } from '@superschematic/engine/client';

import { LeaseLostError, WorkerStoppedError, type LeaseLossReason } from './errors.js';

/** A message to the holder of the lease, as a heartbeat returns it. */
export interface Directive {
  /** Its number in its lease, 1, 2, 3, .... */
  readonly id: number;
  readonly name: string;
  readonly data?: Readonly<Record<string, unknown>>;
  readonly dedupeKey?: string;
  readonly createdAt: number;
  readonly createdBy: string;
}

/** What Queue's claim returns. */
export interface Claim {
  readonly id: string;
  readonly token: number;
  readonly expiresAt: number;
  readonly heartbeatMs: number;
}

/** The Lease vetoes that say the lease is no longer the caller's. */
export const LOST_CODES: readonly LeaseLossReason[] = ['lapsed', 'token_stale', 'not_holder', 'not_leased'];

/** A fenced write's options: the token is added to its preconditions. */
export interface FencedWriteOptions {
  readonly expectedSeq?: number;
  /** Other behaviors' entries; Lease's is the job's. */
  readonly preconditions?: Preconditions;
  readonly signal?: AbortSignal;
}

/** The claimed instance, read freely and written with the lease's token. */
export class LeasedInstance<T = JSONObject> {
  constructor(private readonly job: Job<T>) {}

  get schema(): string {
    return this.job.schema;
  }

  get id(): string {
    return this.job.id;
  }

  /** get reads the instance; a read needs no token. */
  get(options: { readonly signal?: AbortSignal } = {}): Promise<Instance<T>> {
    return this.job.client.instances.get<T>(this.job.schema, this.job.id, this.call(options));
  }

  /** update applies a merge patch, fenced. */
  update(patch: JSONObject, options: FencedWriteOptions = {}): Promise<Instance<T>> {
    return this.write(() => this.job.client.instances.update<T>(this.job.schema, this.job.id, patch, this.fenced(options)));
  }

  /** delete removes the instance, fenced. */
  delete(options: FencedWriteOptions = {}): Promise<void> {
    return this.write(() => this.job.client.instances.delete(this.job.schema, this.job.id, this.fenced(options)));
  }

  /** invoke calls a behavior operation on the instance, fenced, and returns its result. */
  async invoke<R = unknown>(operation: string, params: JSONObject = {}, options: FencedWriteOptions = {}): Promise<R> {
    return (await this.operate<R>(operation, params, options)).result;
  }

  /** operate calls a behavior operation on the instance, fenced, and returns its result and the instance's sequence. */
  operate<R = unknown>(operation: string, params: JSONObject = {}, options: FencedWriteOptions = {}): Promise<OperationOutcome<R>> {
    return this.write(() => this.job.client.instances.operate<R>(this.job.schema, this.job.id, operation, params, this.fenced(options)));
  }

  private call(options: { readonly signal?: AbortSignal }): CallOptions {
    return { namespace: this.job.namespace, ...(options.signal !== undefined ? { signal: options.signal } : {}) };
  }

  private fenced(options: FencedWriteOptions): CallOptions & { expectedSeq?: number; preconditions: Preconditions } {
    if (options.preconditions !== undefined && 'Lease' in options.preconditions) {
      throw new TypeError(`a write of the leased ${this.job.schema} ${this.job.id} presents the job's token; it takes no Lease precondition of its own`);
    }
    return {
      ...this.call(options),
      ...(options.expectedSeq !== undefined ? { expectedSeq: options.expectedSeq } : {}),
      preconditions: { ...(options.preconditions ?? {}), Lease: { token: this.job.token } },
    };
  }

  /** heartbeat renews the lease, acknowledging directives; the worker sends it. */
  heartbeat(acknowledge: readonly number[], signal: AbortSignal): Promise<{ expiresAt: number; directives?: Directive[] }> {
    return this.write(
      () => this.job.client.instances.invoke(this.job.schema, this.job.id, 'heartbeat', acknowledge.length > 0 ? { acknowledge: [...acknowledge] } : {}, this.fenced({ signal })),
      true
    );
  }

  private async write<R>(send: () => Promise<R>, heartbeat = false): Promise<R> {
    this.job.checkHeld();
    try {
      return await send();
    } catch (error) {
      this.job.noticeRefusal(error, heartbeat);
      throw error;
    }
  }
}

/** The lifecycle of a job, as the worker drives it. */
export type JobState = 'working' | 'finishing' | 'lost' | 'released';

/** One claimed instance, as the handler sees it. */
export class Job<T = JSONObject> {
  /** The claimed instance, fenced. */
  readonly instance: LeasedInstance<T>;
  private readonly controller = new AbortController();
  private readonly listeners = new Set<(directive: Directive) => void | Promise<void>>();
  private readonly pending: Directive[] = [];
  private readonly seen = new Set<number>();
  private readonly heard = new Set<number>();
  private delivering: Promise<void> = Promise.resolve();
  private lostWith: LeaseLostError | undefined;
  /** Where the worker has the job; the handler reads `signal` instead. */
  state: JobState = 'working';

  constructor(
    /** The client, for anything other than the claimed instance; its calls carry no token. */
    readonly client: EngineClient,
    readonly namespace: string,
    readonly schema: string,
    readonly claim: Claim,
    private readonly report: (error: unknown) => void
  ) {
    this.instance = new LeasedInstance<T>(this);
  }

  get id(): string {
    return this.claim.id;
  }

  /** The lease's fencing token, which every fenced write presents. */
  get token(): number {
    return this.claim.token;
  }

  /**
   * Aborts when the lease is lost, with a LeaseLostError, and when the
   * worker stops without waiting for the job, with a WorkerStoppedError.
   * The handler passes it to what it waits on, and stops.
   */
  get signal(): AbortSignal {
    return this.controller.signal;
  }

  /** The loss, once the lease is gone. */
  get lost(): LeaseLostError | undefined {
    return this.lostWith;
  }

  /**
   * onDirective hears each directive sent to the lease's holder, once, in
   * the order they were sent; one sent before a listener was added waits
   * for it. A directive is acknowledged in the heartbeat after its
   * listeners have returned. It returns the function that removes the
   * listener.
   */
  onDirective(listener: (directive: Directive) => void | Promise<void>): () => void {
    this.listeners.add(listener);
    this.flush();
    return () => {
      this.listeners.delete(listener);
    };
  }

  /** checkHeld throws once the lease is gone, so a fenced write is not sent. */
  checkHeld(): void {
    if (this.lostWith !== undefined) {
      throw this.lostWith;
    }
  }

  /**
   * noticeRefusal reads a refused write: Lease's veto of a lost lease
   * loses the job. A `not_found` loses it only for the worker's heartbeat,
   * an operation Lease always has; a handler's write can be refused that
   * way for an operation the type does not have.
   */
  noticeRefusal(error: unknown, heartbeat = false): void {
    if (isVeto(error, 'Lease', LOST_CODES)) {
      this.lose(error.vetoCode as LeaseLossReason, error);
    } else if (heartbeat && isProblem(error, 'not_found')) {
      this.lose('not_found', error);
    }
  }

  /** lose marks the lease gone and aborts the handler's signal; the first reason stands. */
  lose(reason: LeaseLossReason, cause?: unknown): void {
    if (this.lostWith !== undefined) {
      return;
    }
    this.lostWith = new LeaseLostError(reason, this, cause);
    if (this.state === 'working' || this.state === 'finishing') {
      this.state = reason === 'released' ? 'released' : 'lost';
    }
    if (!this.controller.signal.aborted) {
      this.controller.abort(this.lostWith);
    }
  }

  /** stopped aborts the handler's signal for a stop that does not wait for it. */
  stopped(): void {
    if (!this.controller.signal.aborted) {
      this.controller.abort(new WorkerStoppedError());
    }
  }

  /** deliver takes a heartbeat's directives: each new one goes to the listeners, or waits for one. */
  deliver(directives: readonly Directive[]): void {
    for (const directive of directives) {
      if (!this.seen.has(directive.id)) {
        this.seen.add(directive.id);
        this.pending.push(directive);
      }
    }
    this.flush();
  }

  /** The directives heard and not yet acknowledged, for the next heartbeat. */
  get unacknowledged(): number[] {
    return [...this.heard];
  }

  /** acknowledged drops the directives a heartbeat acknowledged. */
  acknowledged(ids: readonly number[]): void {
    for (const id of ids) {
      this.heard.delete(id);
    }
  }

  // One delivery at a time, in order, so a slow listener holds back the
  // directives after its own rather than racing them.
  private flush(): void {
    this.delivering = this.delivering.then(async () => {
      while (this.listeners.size > 0 && this.pending.length > 0) {
        const directive = this.pending.shift() as Directive;
        for (const listener of [...this.listeners]) {
          try {
            await listener(directive);
          } catch (error) {
            this.report(error);
          }
        }
        this.heard.add(directive.id);
      }
    });
  }
}
