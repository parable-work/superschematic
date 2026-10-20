/*
@superschematic/engine-workqueue: claimable work for @superschematic/engine
(D16). The core declares the work queue's behaviors, Lease, Assignment,
Queue, Presence, Blueprint, Budget and Retries (internal/registry/behaviors
in the repository), so every binary's meta-schema admits a schema that
composes them, but an engine runs them only once a deployment names them:
pass workQueueBehaviors, or the names of the ones it uses, as the engine's
behaviors option. They are Rust (runtime/engine-workqueue/rust, D58),
which the engine's addon is built with and registers by name. This
package holds their names, their configs' and records' types and their
constants, and ./worker, the loop that claims and runs work over the
engine's HTTP API. See runtime/engine-workqueue/README.md.
*/

export { DEFAULT_TTL_MS } from './defaults.js';
export { DEFAULT_MAX_CANDIDATES, DEFAULT_SWEEP_MS, EXCLUDED, MAX_STEPS, MAX_SWEEP } from './types.js';
export type {
  AssignmentConfig,
  AttemptRecord,
  BlueprintConfig,
  BlueprintRecord,
  BlueprintStep,
  BlueprintWhen,
  BudgetConfig,
  BudgetMeter,
  BudgetTransition,
  ClaimRecord,
  DirectiveRecord,
  ExpiryReason,
  LeaseConfig,
  LeaseDirectOn,
  LeaseEnd,
  LeaseRecord,
  LeaseTransition,
  MeterRecord,
  Overrun,
  PresenceConfig,
  PresenceRecord,
  PresenceTransition,
  QueueConfig,
  ReserveCheck,
  RetriesConfig,
  RetriesRecord,
  RetryClass,
  ScopeRead,
  ScopeValue,
} from './types.js';

/** The name of each behavior this package ships. */
export type WorkQueueBehavior = 'Lease' | 'Assignment' | 'Queue' | 'Presence' | 'Blueprint' | 'Budget' | 'Retries';

/**
 * Every behavior this package ships, by name, in the order a deployment
 * registers them: the engine's behaviors option takes them as they are
 * (`openEngine({ ..., behaviors: workQueueBehaviors })`).
 */
export const workQueueBehaviors: readonly WorkQueueBehavior[] = Object.freeze(['Lease', 'Assignment', 'Queue', 'Presence', 'Blueprint', 'Budget', 'Retries']);
