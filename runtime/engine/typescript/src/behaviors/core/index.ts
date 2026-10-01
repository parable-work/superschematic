/*
The behaviors the core declares (internal/registry/behaviors in the
repository, D16), which every engine registers when it opens: Workflow,
Comments, Revisions, Dependencies, Links, Rollups and Search. Each
implementation imports its declaration's copy from declarations/, which
`superschematic behaviors --out` writes and CI checks, so the engine and
the compiler read one declaration. They reach the engine only through the
plug-in interface an extension's behavior uses.
*/

import type { AnyBehaviorImplementation } from '../behavior.js';
import { comments } from './comments.js';
import { dependencies } from './dependencies.js';
import { links } from './links.js';
import { revisions } from './revisions.js';
import { rollups } from './rollups.js';
import { search } from './search.js';
import { workflow } from './workflow.js';

/** The core's behaviors, in the order the engine registers them. */
export const coreBehaviors: readonly AnyBehaviorImplementation[] = Object.freeze([workflow, comments, revisions, dependencies, links, rollups, search]);

export { isTerminalState } from './workflow.js';
export type { WorkflowConfig, WorkflowStates, WorkflowTransition } from './workflow.js';
export type { CommentRecord } from './comments.js';
export type { ProposalRecord, ProposalState, RevisionRecord, RevisionsConfig } from './revisions.js';
export type { BlockerRecord, DependenciesConfig, DependentRecord } from './dependencies.js';
export type { LinkRecord, LinkSpec, LinksConfig } from './links.js';
export { MAX_ROLLUP_READ } from './rollups.js';
export type { RollupFunction, RollupOver, RollupSpec, RollupsConfig } from './rollups.js';
export type { SearchConfig, SearchHit, SnippetPart } from './search.js';
