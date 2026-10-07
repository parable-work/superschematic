/*
What a worker's handler throws to say how its attempt failed, what a
job's signal aborts with, and what the worker reports when its presence
is gone.
*/

/** What an attempt reports to Retries beside its failure class: `recordAttempt`'s other parameters. */
export interface AttemptReport {
  readonly score?: number;
  readonly result?: unknown;
  readonly signature?: string;
  readonly predicates?: Readonly<Record<string, boolean>>;
  readonly detail?: Readonly<Record<string, unknown>>;
}

export interface WorkFailureOptions {
  /** The Retries failure class the attempt is recorded under, when the type composes Retries. */
  readonly failure?: string;
  /** Give the work up as failed: `release({ abandon: true })`, which counts as an expiry toward Lease's `maxExpiries`. */
  readonly abandon?: boolean;
  /** The rest of the attempt for Retries. */
  readonly attempt?: AttemptReport;
  readonly cause?: unknown;
}

/**
 * A handler throws WorkFailure to say how its attempt failed: the Retries
 * class it is recorded under, and whether the work is given up
 * (`abandon`) or handed back to be claimed again. Any other error a
 * handler throws is given up: an error nobody classified would otherwise
 * come back forever.
 */
export class WorkFailure extends Error {
  readonly failure: string | undefined;
  readonly abandon: boolean;
  readonly attempt: AttemptReport | undefined;

  constructor(message: string, options: WorkFailureOptions = {}) {
    super(message, options.cause !== undefined ? { cause: options.cause } : undefined);
    this.name = 'WorkFailure';
    this.failure = options.failure;
    this.abandon = options.abandon ?? false;
    this.attempt = options.attempt;
  }
}

/**
 * Why a job's lease is gone:
 * - `lapsed`, `token_stale`, `not_holder`, `not_leased`: Lease's veto of a
 *   heartbeat or a fenced write;
 * - `not_found`: the instance was deleted;
 * - `unrenewed`: no heartbeat succeeded within the lease's length;
 * - `released`: the worker gave the lease back, at the end of the job or
 *   at a stop.
 */
export type LeaseLossReason = 'lapsed' | 'token_stale' | 'not_holder' | 'not_leased' | 'not_found' | 'unrenewed' | 'released';

/** A job's lease is gone: its signal aborts with this, and a fenced write throws it without being sent. */
export class LeaseLostError extends Error {
  readonly reason: LeaseLossReason;
  readonly schema: string;
  readonly id: string;
  readonly token: number;

  constructor(reason: LeaseLossReason, job: { readonly schema: string; readonly id: string; readonly token: number }, cause?: unknown) {
    super(`the lease on ${job.schema} ${job.id} (token ${job.token}) is gone: ${reason}`, cause !== undefined ? { cause } : undefined);
    this.name = 'LeaseLostError';
    this.reason = reason;
    this.schema = job.schema;
    this.id = job.id;
    this.token = job.token;
  }
}

/** The worker is stopping and did not wait for the job: its signal aborts with this, and the worker releases the lease. */
export class WorkerStoppedError extends Error {
  constructor() {
    super('the worker is stopping');
    this.name = 'WorkerStoppedError';
  }
}

/**
 * Why a worker's presence is gone:
 * - `refused`: the engine answered a beat with a refusal, a 4xx but 408
 *   and 429: Presence's veto (`not_principal`, `no_principal`), a
 *   `not_found` instance, a `forbidden` caller;
 * - `lapsed`: no beat succeeded for the Presence config's `ttlMs` since
 *   the last one that did was sent, by the worker's clock.
 */
export type PresenceLossReason = 'refused' | 'lapsed';

/** The worker's presence is gone: it claims nothing until a beat succeeds, and its onError hears this once per loss. */
export class PresenceLostError extends Error {
  readonly reason: PresenceLossReason;
  readonly schema: string;
  readonly id: string;

  constructor(reason: PresenceLossReason, presence: { readonly schema: string; readonly id: string }, cause?: unknown) {
    super(`the presence ${presence.schema} ${presence.id} is gone: ${reason}; the worker claims nothing until a beat succeeds`, cause !== undefined ? { cause } : undefined);
    this.name = 'PresenceLostError';
    this.reason = reason;
    this.schema = presence.schema;
    this.id = presence.id;
  }
}
