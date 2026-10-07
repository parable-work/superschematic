/*
@superschematic/engine-workqueue/worker: a worker that runs a handler
over the instances it claims from a work queue, through an engine's HTTP
API. It imports only @superschematic/engine/client, not the engine or the
behaviors, so a worker process loads no SQLite and runs wherever the
client does. See runtime/engine-workqueue/README.md, "The worker".
*/

export { LeaseLostError, PresenceLostError, WorkFailure, WorkerStoppedError } from './errors.js';
export type { AttemptReport, LeaseLossReason, PresenceLossReason, WorkFailureOptions } from './errors.js';
export { Job, LOST_CODES, LeasedInstance } from './job.js';
export type { Claim, Directive, FencedWriteOptions, JobState } from './job.js';
export { DEFAULT_DRAIN_TIMEOUT_MS, DEFAULT_IDLE_INITIAL_MS, DEFAULT_IDLE_MAX_MS, QueueWorker } from './worker.js';
export type { MatchValue, PresenceOptions, QueueWorkerOptions, StopOptions, WorkResult } from './worker.js';
