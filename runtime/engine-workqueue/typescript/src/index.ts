/*
@superschematic/engine-workqueue: the implementations of the core's
work-queue behaviors for @superschematic/engine (D16). The core declares
them (internal/registry/behaviors in the repository), so every binary's
meta-schema admits a schema that composes them, but the engine runs them
only once a deployment registers them: pass workQueueBehaviors, or the
ones it uses, as the engine's behaviors option. Each implementation
imports its declaration's copy from declarations/, which `superschematic
behaviors --package @superschematic/engine-workqueue` writes and CI
checks. They reach the engine only through the plug-in interface an
extension's behavior uses. See runtime/engine-workqueue/README.md.
*/

import type { AnyBehaviorImplementation } from '@superschematic/engine';

import { assignment } from './assignment.js';
import { blueprint } from './blueprint.js';
import { budget } from './budget.js';
import { lease } from './lease.js';
import { presence } from './presence.js';
import { queue } from './queue.js';
import { retries } from './retries.js';

export { assignment } from './assignment.js';
export type { AssignmentConfig } from './assignment.js';
export { budget } from './budget.js';
export type { BudgetConfig, BudgetMeter, MeterRecord, Overrun } from './budget.js';
export { DEFAULT_SWEEP_MS, DEFAULT_TTL_MS, MAX_SWEEP, lease } from './lease.js';
export type { DirectiveRecord, LeaseConfig, LeaseRecord, LeaseTransition } from './lease.js';
export { presence } from './presence.js';
export type { PresenceConfig, PresenceRecord, PresenceTransition } from './presence.js';
export { DEFAULT_MAX_CANDIDATES, queue } from './queue.js';
export type { ClaimRecord, QueueConfig } from './queue.js';
export { retries } from './retries.js';
export type { AttemptRecord, RetriesConfig, RetriesRecord, RetryClass } from './retries.js';
export { MAX_STEPS, blueprint } from './blueprint.js';
export type { BlueprintConfig, BlueprintRecord, BlueprintStep, BlueprintWhen } from './blueprint.js';

/** Every behavior this package implements, in the order a deployment registers them. */
export const workQueueBehaviors: readonly AnyBehaviorImplementation[] = Object.freeze([lease, assignment, queue, presence, blueprint, budget, retries]);
