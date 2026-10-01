/*
The behaviors the core declares (internal/registry/behaviors in the
repository, D16), which every engine registers when it opens: Workflow,
Comments and Revisions. Each implementation imports its declaration's
copy from declarations/, which `superschematic behaviors --out` writes and
CI checks, so the engine and the compiler read one declaration. They
reach the engine only through the plug-in interface an extension's
behavior uses.
*/

import type { AnyBehaviorImplementation } from '../behavior.js';
import { comments } from './comments.js';
import { revisions } from './revisions.js';
import { workflow } from './workflow.js';

/** The core's behaviors, in the order the engine registers them. */
export const coreBehaviors: readonly AnyBehaviorImplementation[] = Object.freeze([workflow, comments, revisions]);

export { isTerminalState } from './workflow.js';
export type { WorkflowConfig, WorkflowStates, WorkflowTransition } from './workflow.js';
export type { CommentRecord } from './comments.js';
export type { ProposalRecord, ProposalState, RevisionRecord, RevisionsConfig } from './revisions.js';
