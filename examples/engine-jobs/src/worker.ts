// A worker over the engine's HTTP API. It beats its worker instance,
// claims the best queued job, works it while a timer renews the lease, and
// reports the attempt, finishes the job and hands the lease back. Every
// write to a claimed job presents the lease's token in the Preconditions
// header, so once the lease has gone to another worker, this one's writes
// are refused and it stops.
import { setTimeout as sleep } from 'node:timers/promises';

/** What claimNext returns: the job, the lease's token and how often to renew it. */
export interface Claim {
  id: string;
  token: number;
  expiresAt: number;
  heartbeatMs: number;
}

/** A job's fields as a read returns them, its behaviors' included. */
export interface Job {
  title: string;
  topic?: string;
  step?: string;
  [field: string]: unknown;
}

/** What a handler gets: the job, and where to report what it used. */
export interface Work {
  id: string;
  job: Job;
  /** recordUsage charges CPU seconds to the job's budget. */
  recordUsage(cpuSeconds: number): Promise<void>;
}

/** A handler does the work and returns its result, or throws to fail the attempt. */
export type Handler = (work: Work) => Promise<string>;

/** A failed attempt, by the failure class it counts against (Retries in jobs.schema.json). */
export class JobFailure extends Error {
  readonly failure: 'transient' | 'invalid';

  constructor(failure: 'transient' | 'invalid', message: string) {
    super(message);
    this.failure = failure;
  }
}

/** A refusal: the problem document the HTTP API answered with. */
export class Refused extends Error {
  readonly status: number;
  readonly code: string;
  /** The problem's details: for a veto, { behavior, action, reason, code?, details? }. */
  readonly details: any;

  constructor(status: number, code: string, details: unknown) {
    super(`${status} ${code}`);
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

/** How a worked job ended for this worker. */
export type Outcome = 'done' | 'retry' | 'failed' | 'lost';

/** A caller of the namespace's routes, as the principal its bearer token names. */
export class Client {
  /** The namespace's routes, such as http://127.0.0.1:8788/api/namespaces/default. */
  readonly api: string;
  readonly #token: string;

  constructor(api: string, token: string) {
    this.api = api;
    this.#token = token;
  }

  /** call sends a request and returns the envelope's data; a refusal throws Refused. */
  async call(method: string, route: string, body?: unknown, leaseToken?: number): Promise<any> {
    const response = await fetch(`${this.api}${route}`, {
      method,
      headers: {
        authorization: `Bearer ${this.#token}`,
        ...(body === undefined ? {} : { 'content-type': 'application/json' }),
        // What the caller assumes of the lease: Lease's precondition.
        ...(leaseToken === undefined ? {} : { preconditions: JSON.stringify({ Lease: { token: leaseToken } }) }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const json = (await response.json()) as { data?: unknown; code?: string; details?: unknown };
    if (!response.ok) {
      throw new Refused(response.status, json.code ?? 'unknown', json.details);
    }
    return json.data;
  }
}

/** A worker process, which runs as one principal and beats that principal's worker instance. */
export class JobWorker extends Client {
  /** The principal the token names, which is also its worker instance's id. */
  readonly subject: string;

  constructor(api: string, subject: string, token: string) {
    super(api, token);
    this.subject = subject;
  }

  /** beat says this worker is alive: Presence's beat on its worker instance. */
  beat(): Promise<{ deadline: number }> {
    return this.call('POST', `/schemas/workers/instances/${this.subject}/operations/beat`, {});
  }

  /** claimNext claims the best queued job of the topic, or of any topic, or returns null. */
  async claimNext(topic?: string): Promise<Claim | null> {
    const { claimed } = await this.call('POST', '/schemas/jobs/operations/claimNext', topic === undefined ? {} : { match: { topic } });
    return claimed;
  }

  /** operate calls an operation on a claimed job, presenting the lease's token. */
  operate(claim: Claim, operation: string, params: unknown = {}): Promise<any> {
    return this.call('POST', `/schemas/jobs/instances/${claim.id}/operations/${operation}`, params, claim.token);
  }

  /**
   * heartbeat renews the lease. Its result lists the directives sent to
   * the holder, which this worker does not act on.
   */
  heartbeat(claim: Claim): Promise<{ expiresAt: number }> {
    return this.operate(claim, 'heartbeat');
  }

  /**
   * work runs a claimed job through the handler. A success records the
   * result and moves the job to done; a failure records an attempt of its
   * class, and Retries moves the job to failed once its caps run out. The
   * release then puts a job that is still running back in the queue
   * (Lease's onExpiry). A Lease veto at any step means the lease is gone,
   * expired or taken by another worker: the job is no longer this
   * worker's, and it writes nothing more.
   */
  async work(claim: Claim, handler: Handler): Promise<Outcome> {
    const renew = setInterval(() => {
      // Renewing the lease and beating the worker on one timer keeps both
      // inside Presence's ttlMs. A failure shows at the next write.
      this.beat().catch(() => {});
      this.heartbeat(claim).catch(() => {});
    }, claim.heartbeatMs);
    try {
      const job: Job = (await this.call('GET', `/schemas/jobs/instances/${claim.id}`)).data;
      let outcome: Outcome;
      try {
        const result = await handler({ id: claim.id, job, recordUsage: (amount) => this.operate(claim, 'recordUsage', { meter: 'cpuSeconds', amount }) });
        await this.operate(claim, 'recordAttempt', { result });
        await this.operate(claim, 'transition', { to: 'done' });
        outcome = 'done';
      } catch (error) {
        if (leaseGone(error)) {
          throw error;
        }
        const failure = error instanceof JobFailure ? error.failure : 'transient';
        const attempt = await this.operate(claim, 'recordAttempt', { failure, detail: { message: (error as Error).message } });
        outcome = attempt.exhausted ? 'failed' : 'retry';
      }
      await this.operate(claim, 'release');
      return outcome;
    } catch (error) {
      if (leaseGone(error)) {
        return 'lost';
      }
      throw error;
    } finally {
      clearInterval(renew);
    }
  }

  /** runOnce claims a job and works it; null when no job is waiting. */
  async runOnce(handler: Handler, topic?: string): Promise<{ id: string; outcome: Outcome } | null> {
    const claim = await this.claimNext(topic);
    return claim === null ? null : { id: claim.id, outcome: await this.work(claim, handler) };
  }

  /** run beats and works jobs until the signal aborts, waiting idleMs whenever no job is waiting. */
  async run(handler: Handler, options: { topic?: string; idleMs?: number; signal: AbortSignal }): Promise<void> {
    while (!options.signal.aborted) {
      await this.beat();
      if ((await this.runOnce(handler, options.topic)) === null) {
        await sleep(options.idleMs ?? 1000, undefined, { signal: options.signal }).catch(() => {});
      }
    }
  }
}

/** leaseGone says whether a refusal is Lease's: the token is stale, the lease lapsed, or another holds it. */
export function leaseGone(error: unknown): boolean {
  return error instanceof Refused && error.code === 'vetoed' && error.details?.behavior === 'Lease';
}
