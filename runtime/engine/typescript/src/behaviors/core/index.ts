/*
The behaviors the core declares (internal/registry/behaviors in the
repository, D16), which every engine registers when it opens: Workflow,
Comments, Revisions, Dependencies, Links, Rollups, Search, Reactions,
Constants and Variants.
Each implementation imports its declaration's copy from declarations/,
which `superschematic behaviors --out` writes and CI checks, so the
engine and the compiler read one declaration. They reach the engine only
through the plug-in interface an extension's behavior uses.
*/

import type { AnyBehaviorImplementation } from '../behavior.js';
import { comments } from './comments.js';
import { constants } from './constants.js';
import { dependencies } from './dependencies.js';
import { links } from './links.js';
import { reactions } from './reactions.js';
import { revisions } from './revisions.js';
import { rollups } from './rollups.js';
import { search } from './search.js';
import { variants } from './variants.js';
import { workflow } from './workflow.js';

/** The core's behaviors, in the order the engine registers them. */
export const coreBehaviors: readonly AnyBehaviorImplementation[] = Object.freeze([
  workflow,
  comments,
  revisions,
  dependencies,
  links,
  rollups,
  search,
  reactions,
  constants,
  variants,
]);

export { isTerminalState, stateOutcome } from './workflow.js';
export type { WorkflowConfig, WorkflowOutcome, WorkflowStates, WorkflowTransition } from './workflow.js';
export type { CommentRecord } from './comments.js';
export type { ProposalRecord, ProposalState, RevisionRecord, RevisionsConfig } from './revisions.js';
export type { BlockerRecord, DependenciesConfig, DependentRecord } from './dependencies.js';
export type { LinkRecord, LinkSpec, LinksConfig } from './links.js';
export { MAX_ROLLUP_READ } from './rollups.js';
export type { RollupFunction, RollupOver, RollupSpec, RollupsConfig } from './rollups.js';
export type { SearchConfig, SearchHit, SnippetPart } from './search.js';
export type { ReactionsConfig, ReactionsRule, ReactionsTerminal, ReactionsThen, ReactionsWhen } from './reactions.js';
export type { ConstantsConfig } from './constants.js';
export type { VariantsConfig } from './variants.js';
