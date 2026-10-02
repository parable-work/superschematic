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
import { lease } from './lease.js';

export { assignment } from './assignment.js';
export type { AssignmentConfig } from './assignment.js';
export { DEFAULT_TTL_MS, lease } from './lease.js';
export type { DirectiveRecord, LeaseConfig, LeaseRecord, LeaseTransition } from './lease.js';

/** Every behavior this package implements, in the order a deployment registers them. */
export const workQueueBehaviors: readonly AnyBehaviorImplementation[] = Object.freeze([lease, assignment]);
