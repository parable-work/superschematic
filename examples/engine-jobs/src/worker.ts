// A worker process over the engine's HTTP API: the work-queue package's
// QueueWorker on the engine's typed client. It beats the worker instance
// its principal stands for, claims the most urgent job of its topic,
// renews the lease while the handler works, presents the lease's token on
// every write to the job, hands the handler an operator's cancel, and
// records the attempt, finishes the job and releases it. Once the lease is
// gone it stops the handler and writes nothing more.
import type { EngineClient } from '@superschematic/engine/client';
import { QueueWorker, WorkFailure, type Job, type QueueWorkerOptions, type WorkResult } from '@superschematic/engine-workqueue/worker';

/** A job's own fields, as a read returns them in data; its behaviors' fields are apart, in behaviors. */
export interface JobFields {
  title: string;
  topic?: string;
  step?: string;
  [field: string]: unknown;
}

/** What a handler gets: the job, a signal, and where to report what it used. */
export interface Work {
  id: string;
  job: JobFields;
  /** Aborts when the lease is lost, when the worker stops without waiting, and when an operator cancels the job. */
  signal: AbortSignal;
  /** recordUsage charges CPU seconds to the job's budget. */
  recordUsage(cpuSeconds: number): Promise<void>;
}

/** A handler does the work and returns its result, or throws a JobFailure to fail the attempt. */
export type Handler = (work: Work) => Promise<string>;

/** A failed attempt, by the failure class it counts against (Retries in jobs.schema.json). */
export class JobFailure extends WorkFailure {
  constructor(failure: 'transient' | 'invalid', message: string) {
    super(message, { failure });
  }
}

/** The worker's options a deployment tunes, beside the topic it claims. */
export interface JobWorkerOptions extends Pick<QueueWorkerOptions<JobFields>, 'concurrency' | 'heartbeatMs' | 'idle' | 'onError'> {
  /** The topic it claims; any topic when absent. */
  topic?: string;
  /** Milliseconds between beats of its worker instance; a third of the workers' ttlMs by default. */
  beatMs?: number;
}

/**
 * jobWorker is the worker of one process. Its client calls as the
 * principal its token names, whose worker instance, registered by the
 * server with the subject as its id, the worker beats; start() begins.
 */
export function jobWorker(client: EngineClient, subject: string, handler: Handler, options: JobWorkerOptions = {}): QueueWorker<JobFields> {
  const { topic, beatMs, ...tuning } = options;
  return new QueueWorker<JobFields>(client, {
    ...tuning,
    schema: 'jobs',
    ...(topic !== undefined ? { match: { topic } } : {}),
    presence: { schema: 'workers', id: subject, ...(beatMs !== undefined ? { beatMs } : {}) },
    handle: (job) => work(job, handler),
  });
}

// work runs a claimed job through the handler. A result is a success:
// Retries keeps it in the job's result field, and the job moves to done.
// A JobFailure records an attempt of its class and hands the job back;
// Retries fails the job once its caps run out, at once for `invalid`. Any
// other error gives the job up, an expiry toward Lease's maxExpiries. A
// cancel directive aborts the handler's signal, and the job fails for good
// (the terminal class `cancelled`).
async function work(job: Job<JobFields>, handler: Handler): Promise<WorkResult> {
  const cancel = new AbortController();
  job.onDirective((directive) => {
    if (directive.name === 'cancel') {
      cancel.abort();
    }
  });
  const { data } = await job.instance.get({ signal: job.signal });
  let result: string | undefined;
  try {
    result = await handler({
      id: job.id,
      job: data,
      signal: AbortSignal.any([job.signal, cancel.signal]),
      recordUsage: async (cpuSeconds) => {
        await job.instance.invoke('recordUsage', { meter: 'cpuSeconds', amount: cpuSeconds });
      },
    });
  } catch (error) {
    if (!cancel.signal.aborted) {
      throw error;
    }
  }
  if (cancel.signal.aborted) {
    throw new WorkFailure('cancelled by an operator', { failure: 'cancelled' });
  }
  return { transition: 'done', attempt: { result } };
}
