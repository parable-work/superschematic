/*
The core's behaviors (internal/registry/behaviors, D16), which every
engine registers when it opens: Workflow, Comments, Revisions,
Dependencies, Links, Rollups, Search, Reactions, Constants, Variants,
Branches, StateRules, Approvals, Numbering, Timers, Derived and
Provenance. Each is the Rust
engine's (runtime/engine/rust/behaviors, D58); here are the configs a
schema gives them, the records their operations return, their constants,
and the readers of a config a client asks about any instance with.
*/

import { createHash } from 'node:crypto';

/** One transition of a Workflow config. */
export interface WorkflowTransition {
  readonly from: string;
  readonly to: string;
  /** The permission a caller needs to make it; absent, anyone who may write the instance can. */
  readonly permission?: string;
}

/** The outcome of a terminal state: how the work it ends went. */
export type WorkflowOutcome = 'success' | 'failure' | 'neutral';

/** Workflow's config, parsed: the initial state filled in, and every terminal state's outcome. */
export interface WorkflowConfig {
  readonly states: readonly string[];
  readonly initial: string;
  readonly transitions: readonly WorkflowTransition[];
  readonly outcomes: Readonly<Record<string, WorkflowOutcome>>;
}

/** What isTerminalState and stateOutcome read of a Workflow config, as a schema holds it or parsed. */
export interface WorkflowStates {
  readonly states: readonly string[];
  readonly transitions: ReadonlyArray<{ readonly from: string }>;
  /** The outcomes of the terminal states it names; a terminal state it does not name is a success. */
  readonly outcomes?: Readonly<Record<string, string>>;
}

/**
 * isTerminalState reports whether a state of a Workflow config is
 * terminal: one of its states that no transition leaves. It takes the
 * config as a schema holds it (engine.schemas.behaviors lists it), so a
 * behavior or a client can ask about any instance's status. A state the
 * config does not list is not terminal.
 */
export function isTerminalState(config: WorkflowStates, state: string): boolean {
  return config.states.includes(state) && !config.transitions.some((transition) => transition.from === state);
}

/**
 * stateOutcome gives the outcome of a terminal state of a Workflow
 * config, as a schema holds it or parsed: the one its outcomes names, or
 * success. A state that is not terminal has none, so it returns undefined.
 */
export function stateOutcome(config: WorkflowStates, state: string): WorkflowOutcome | undefined {
  if (!isTerminalState(config, state)) {
    return undefined;
  }
  const outcomes = config.outcomes;
  const named = outcomes !== undefined && Object.prototype.hasOwnProperty.call(outcomes, state) ? outcomes[state] : undefined;
  return (named ?? 'success') as WorkflowOutcome;
}

/** One comment, as comment and listComments return it. */
export interface CommentRecord {
  readonly id: number;
  readonly replyTo?: number;
  readonly body: string;
  readonly createdBy: string;
  readonly createdAt: number;
}

/** Revisions' config. */
export interface RevisionsConfig {
  readonly review?: { readonly permission: string };
}

export type ProposalState = 'pending' | 'approved' | 'rejected';

/** One revision, as listRevisions returns it. */
export interface RevisionRecord {
  readonly revision: number;
  readonly data: Record<string, unknown>;
  readonly createdBy: string;
  readonly createdAt: number;
  /** The proposal whose approval made it. */
  readonly proposal?: number;
}

/** One instance a proposal cites as evidence, and the revision of it, when it names one. */
export interface EvidenceRecord {
  readonly schema: string;
  readonly id: string;
  readonly revision?: number;
}

/** One proposal, as propose, approve, reject and listProposals return it. */
export interface ProposalRecord {
  readonly id: number;
  readonly patch: Record<string, unknown>;
  readonly note?: string;
  /** What the proposer cited, as propose checked it. */
  readonly evidence?: readonly EvidenceRecord[];
  readonly base?: number;
  readonly state: ProposalState;
  readonly createdBy: string;
  readonly createdAt: number;
  readonly reviewedBy?: string;
  readonly reviewedAt?: number;
  readonly reason?: string;
  readonly revision?: number;
}

/** Dependencies' config, parsed: the defaults filled in. */
export interface DependenciesConfig {
  /** The schemas a blocker may be an instance of. */
  readonly schemas: readonly string[];
  /** The states of the type's Workflow a transition into waits for every blocker to finish. */
  readonly gatedStates: readonly string[];
  /** The outcomes of a blocker's terminal state that finish it. */
  readonly satisfiedBy: readonly WorkflowOutcome[];
  /** The gated states no transition of the type's Workflow leaves: an instance in one takes no open blocker. */
  readonly terminalGatedStates: readonly string[];
}

/** A blocker, as addBlocker and listBlockers return it. */
export interface BlockerRecord {
  readonly schema: string;
  readonly id: string;
  /** Its Workflow status; absent when it has none. */
  readonly status?: string;
  /** Whether it is not finished: its status is not a terminal state of its schema's Workflow whose outcome satisfiedBy lists. */
  readonly open: boolean;
}

/** An instance another blocks, as listDependents returns it. */
export interface DependentRecord {
  readonly schema: string;
  readonly id: string;
}

/** What a pinned link pins: a revision of Revisions, or a release of Branches. */
export type LinkPin = 'revision' | 'release';

/**
 * linkPin reads what a link of a Links config, as a schema holds it, pins:
 * pinned true or "revision" pins a revision, "release" a release; any
 * other value pins nothing.
 */
export function linkPin(link: unknown): LinkPin | undefined {
  const pinned = (link as { pinned?: unknown } | undefined)?.pinned;
  if (pinned === true || pinned === 'revision') {
    return 'revision';
  }
  return pinned === 'release' ? 'release' : undefined;
}

/**
 * linkMany reads whether a link of a Links config, as a schema holds it,
 * holds a list of targets: many true. What reads one target of a link
 * refuses one that does.
 */
export function linkMany(link: unknown): boolean {
  return (link as { many?: unknown } | undefined)?.many === true;
}

/** One link of a Links config. */
export interface LinkSpec {
  /** The schema of the instance the link points at. */
  readonly schema: string;
  /** Every create gives it; it is moved, never unlinked, and its target's delete is refused; a many-valued one keeps at least one target. */
  readonly required: boolean;
  /** What it pins of the target, reporting whether the target moved past it; absent when it pins nothing. */
  readonly pin?: LinkPin;
  /** Whether it holds a list of targets, in the order they were linked, rather than one. */
  readonly many: boolean;
  /** For a many-valued link, the most targets it holds. */
  readonly max?: number;
}

/** Links' config, parsed. */
export interface LinksConfig {
  readonly links: Readonly<Record<string, LinkSpec>>;
}

/** One link, or one target of a many-valued link, as the links field holds it. */
export interface LinkRecord {
  readonly schema: string;
  readonly id: string;
  /** The target's revision a link that pins revisions records. */
  readonly revision?: number;
  /** The target's release a link that pins releases records. */
  readonly release?: number;
  /** The target's latest revision or release, of the kind the link pins. */
  readonly latest?: number;
  /** Whether the target has moved past the pinned one. */
  readonly stale?: boolean;
}

/** The functions a rollup computes. */
export type RollupFunction = 'count' | 'countBy' | 'sum' | 'min' | 'max' | 'latest' | 'all' | 'any';

/** One rollup of a Rollups config. */
export interface RollupSpec {
  /** The schema whose instances point at this one. */
  readonly schema: string;
  /** The link of that schema's Links config they point through. */
  readonly link: string;
  readonly function: RollupFunction;
  /** countBy's, sum's, min's, max's and latest's field of the linked instances. */
  readonly field?: string;
  /** For all and any, the states of the type's Workflow a transition into waits for the rollup to hold; none when absent. */
  readonly gatedStates: readonly string[];
  /** For all and any, the outcomes a linked instance's terminal state must have to count; every outcome when absent. */
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** Rollups' config, parsed. */
export interface RollupsConfig {
  readonly rollups: Readonly<Record<string, RollupSpec>>;
}

/** A rollup's entry in the rollups field when its link holds more than MAX_ROLLUP_READ instances. */
export interface RollupOver {
  readonly over: true;
}

/** The most linked instances one rollup reads per computation: one listLinked page, one getMany. */
export const MAX_ROLLUP_READ = 500;

/** Search's vectors, as its config gives them. */
export interface SearchVectors {
  /** How many numbers a vector holds. */
  readonly dimensions: number;
  /** The label of the model the vectors come from, which the engine only compares. */
  readonly model: string;
  /** The permission an embedder needs to settle vectors. */
  readonly permission: string;
}

/** Search's config, parsed. */
export interface SearchConfig {
  /** The indexed fields, by JSON key, in the config's order: the index's columns. */
  readonly fields: readonly string[];
  /** Each indexed field's weight, in the order of fields; 1 for one the config does not weigh. */
  readonly weights: readonly number[];
  /** The vectors it keeps; absent when it keeps none. */
  readonly vectors?: SearchVectors;
}

/** One part of a snippet: text, and whether it is a match. */
export interface SnippetPart {
  readonly text: string;
  readonly match: boolean;
}

/** One instance a search returns. */
export interface SearchHit {
  readonly id: string;
  /** The instance's place in the ranking, from 1, across pages. */
  readonly rank: number;
  /**
   * With a vector: the fused score, 1 / (RRF_K + its place) summed over
   * the rankings it is in, which orders the hits.
   */
  readonly score?: number;
  /** The indexed field the snippet is from. */
  readonly field?: string;
  readonly snippet?: readonly SnippetPart[];
  /** With a vector: its place in the full-text ranking, when the query matched it. */
  readonly text?: { readonly rank: number };
  /** With a vector: its place in the vector ranking and its vector's cosine similarity to the one searched for. */
  readonly vector?: { readonly rank: number; readonly similarity: number };
}

/** One hit of a search across a namespace's schemas (searchSchemas). */
export interface SchemaSearchHit extends SearchHit {
  /** The schema the instance is of. */
  readonly schema: string;
  readonly score: number;
}

/** Reciprocal rank fusion's constant: a hit scores 1 / (RRF_K + its place) in each ranking it is in. */
export const RRF_K = 60;

/**
 * How far into each ranking fusion reads: a search with both a query and a
 * vector, similar, and a search across schemas fuse the first RRF_WINDOW
 * of each ranking they fuse.
 */
export const RRF_WINDOW = 200;

/** The words of an instance's text that similar searches for, at most: its longest. */
export const SIMILAR_TERMS = 32;

/** The instances an allTerminal or anyTerminal rule hears: those of schema that link here through link. */
export interface ReactionsTerminal {
  readonly schema: string;
  readonly link: string;
  /** The outcomes a terminal state must have to count; every outcome when absent, which only allTerminal allows. */
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** A move of a field a changes rule hears: to to and from from, when they are given. */
export interface ReactionsChanges {
  /** An own field's JSON key, or a behavior's field by qualified name, with a member path after it. */
  readonly field: string;
  readonly to?: unknown;
  readonly from?: unknown;
}

/** A field a changes rule names, as parseConfig resolved it. */
export interface ReactionsField {
  /** The behavior whose field it is; absent for an own field. */
  readonly behavior?: string;
  /** The own field's JSON key, or the name the behavior's declaration gives the field. */
  readonly field: string;
  /** The members below the field's value the rule reads, outermost first; [] for the value itself. */
  readonly members: readonly string[];
}

/** What sets a rule off. */
export type ReactionsWhen =
  | { readonly enters: string }
  | { readonly changes: ReactionsChanges }
  | { readonly allTerminal: ReactionsTerminal }
  | { readonly anyTerminal: ReactionsTerminal & { readonly outcomes: readonly WorkflowOutcome[] } }
  | { readonly holds: string }
  | { readonly revised: { readonly link: string } };

/** What a rule does: move the instance, or the one its link points to, to a state. */
export interface ReactionsThen {
  readonly transition: string;
  readonly link?: string;
}

export interface ReactionsRule {
  readonly when: ReactionsWhen;
  readonly then: ReactionsThen;
}

/** An all or any rollup of the type's Rollups config a holds rule names, as the config gives it. */
export interface ReactionsRollup {
  readonly schema: string;
  readonly link: string;
  readonly function: 'all' | 'any';
  readonly outcomes?: readonly WorkflowOutcome[];
}

/** A link of the type's Links config a revised rule names, as the config gives it. */
export interface ReactionsRevisedLink {
  readonly schema: string;
  /** What the link pins, a revision or a release; absent when it pins nothing. */
  readonly pin?: LinkPin;
}

/** Reactions' config, parsed: the rules, and what the type's other configs say of the fields, rollups and links they name. */
export interface ReactionsConfig {
  readonly rules: readonly ReactionsRule[];
  /** Each field a changes rule names, by the name the rule gives it. */
  readonly fields: Readonly<Record<string, ReactionsField>>;
  /** Each rollup a holds rule names, by name. */
  readonly rollups: Readonly<Record<string, ReactionsRollup>>;
  /** Each link a revised rule names, by name. */
  readonly revised: Readonly<Record<string, ReactionsRevisedLink>>;
}

/** Constants' config, parsed. */
export interface ConstantsConfig {
  /** The type's own top-level fields, by JSON key, that keep the value their create gives them. */
  readonly fields: readonly string[];
  /** The permission a caller needs to change them after the create. */
  readonly permission?: string;
}

/** Variants' config, parsed. */
export interface VariantsConfig {
  /** The type's own field the variants type, by JSON key. */
  readonly field: string;
  /** The type's own string or enum field whose value picks the variant, by JSON key. */
  readonly by: string;
  /** By a value of by, the type of the schema document field holds while by holds it. */
  readonly types: Readonly<Record<string, string>>;
  /** Whether field is required, so that an instance always holds a value of it. */
  readonly required: boolean;
}

/** The namespace an actor is the version-5 UUID of a principal's subject in. */
export const ACTOR_NAMESPACE = 'e5df4b2e-719d-4507-8d3a-9474ef4afe81';

/** The namespace a root's id is the version-5 UUID of its instance's id in. */
export const ROOT_NAMESPACE = 'cc634206-0f43-4e6c-a60f-65184f26131e';

/** The name of each instance's primary line when the config gives none. */
export const DEFAULT_PRIMARY = 'main';

/** A kind's role and audit columns, by name, with their value classes: a field of its type may not take one of these keys. */
export const ROLE_COLUMNS: Readonly<Record<string, string>> = Object.freeze({
  id: 'uuid',
  entity_key: 'uuid',
  ref_id: 'uuid',
  root_id: 'uuid',
  deleted_on_ref: 'boolean',
  _version: 'integer',
  created_at: 'dateTime',
  created_by: 'uuid',
  updated_at: 'dateTime',
  updated_by: 'uuid',
});

/** A field's conflict unit, as @conflictUnit sets it. */
export type BranchesUnit = 'atomic' | 'keyed' | 'jsonSchema' | 'excluded';

/** One kind of the graph, as the config gives it, with what parseConfig read of its type. */
export interface BranchesKind {
  /** The type of the schema document whose fields are the kind's content. */
  readonly type: string;
  readonly parent?: { readonly key: string; readonly of: string };
  readonly order?: string;
  readonly singleton: boolean;
  /** The units the config sets, by field; a field it does not name is atomic. */
  readonly units: Readonly<Record<string, BranchesUnit>>;
  readonly retentionDays?: number;
  /** The JSON keys of the type's fields, in the document's order: what a saved row holds beside entity_key. */
  readonly fields: readonly string[];
  /** Each field's value class, by JSON key. */
  readonly classes: Readonly<Record<string, string>>;
}

/** When the sweep runs on a schema, and what it keeps. */
export interface BranchesSweep {
  readonly intervalMs: number;
  readonly discardGrace?: number;
  readonly pruneBatch?: number;
  readonly abandonAfter?: number;
}

/** Branches' config, as parseConfig returns it. */
export interface BranchesConfig {
  readonly kinds: Readonly<Record<string, BranchesKind>>;
  readonly primary: string;
  readonly snapshotEvery: number;
  readonly sweep?: BranchesSweep;
  /** The graph's descriptor, version 3, as JSON text. */
  readonly descriptor: string;
}

/** A ref, as the operations return it. */
export interface BranchRef {
  readonly id: string;
  readonly name: string;
  readonly parent: string | null;
  readonly base: string | null;
  readonly head: string | null;
  readonly sealed: boolean;
  readonly discarded: boolean;
  readonly version: number;
  readonly createdAt: string;
  readonly createdBy: string;
  readonly updatedAt: string;
  readonly updatedBy: string;
}

/** A commit, as the operations return it. */
export interface BranchCommit {
  readonly id: string;
  readonly ref: string;
  readonly parent: string | null;
  readonly message: string;
  readonly sequence: number | null;
  readonly contentHash: string;
  readonly createdAt: string;
  readonly createdBy: string;
  readonly snapshot: boolean;
}

/** One version of the release pointer, as releases returns it. */
export interface BranchRelease {
  readonly version: number;
  readonly commit: string;
  readonly releasedAt: string;
  readonly releasedBy: string;
}

// The digits of the version graph's canonical UUID form, base62.
const BASE62 = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz';

/**
 * The version-5 UUID (RFC 9562) of name in namespace, in the version
 * graph's canonical form, base62: how Branches names an actor (in
 * ACTOR_NAMESPACE) and a root (in ROOT_NAMESPACE).
 */
export function uuidV5(namespace: string, name: string): string {
  const bytes = createHash('sha1').update(Buffer.from(namespace.replace(/-/g, ''), 'hex')).update(name, 'utf8').digest().subarray(0, 16);
  bytes[6] = (bytes[6] & 0x0f) | 0x50;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  let n = BigInt(`0x${bytes.toString('hex')}`);
  let out = '';
  while (n > 0n) {
    out = BASE62[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out === '' ? '0' : out;
}

/** One state's rules, parsed. */
export interface StateRule {
  /** The own fields an instance must hold to enter the state, and keeps while it is in it. */
  readonly required: readonly string[];
  /** The own fields that do not change while the instance is in the state: every one of the type's when frozenAll. */
  readonly frozen: readonly string[];
  /** Whether the config freezes every own field ("all"). */
  readonly frozenAll: boolean;
}

/** StateRules' config, parsed. */
export interface StateRulesConfig {
  /** The rules, by a state of the type's Workflow, in the order its config lists the states. */
  readonly states: Readonly<Record<string, StateRule>>;
  /** The permission a caller needs to change a frozen field. */
  readonly permission?: string;
  /** The state a new instance starts in, the type's Workflow's initial state; absent when its config is not one Workflow takes. */
  readonly initial?: string;
}

/** One gate of an Approvals config, parsed: excludeCreator filled in. */
export interface ApprovalsGate {
  /** How many distinct principals must sign off before a transition into the state. */
  readonly count: number;
  /** The permission a caller needs to sign off. */
  readonly permission: string;
  /** The states a transition from is gated; every state when absent. */
  readonly from?: readonly string[];
  /** Whether the principal who created the instance is refused a sign-off. */
  readonly excludeCreator: boolean;
}

/** Approvals' config, parsed: the defaults filled in. */
export interface ApprovalsConfig {
  /** The gates, by the state of the type's Workflow each gates. */
  readonly gates: Readonly<Record<string, ApprovalsGate>>;
  /** Whether a change of the instance's own fields consumes every gate's sign-offs. */
  readonly resetOnChange: boolean;
}

/** One sign-off, as withdrawSignOff and listSignOffs return it. */
export interface SignOffRecord {
  readonly state: string;
  /** The subject of the principal who signed. */
  readonly subject: string;
  readonly note?: string;
  /** When it was signed, in epoch milliseconds. */
  readonly at: number;
  /** When it was consumed; absent while it counts. */
  readonly consumedAt?: number;
}

/** One derived field of a Derived config, parsed. */
export interface DerivedField {
  /** The own top-level field the value is written to. */
  readonly name: string;
  /** The own top-level fields the value is derived from. */
  readonly from: readonly string[];
  readonly instruction: string;
  /** The label of the model, which the engine only compares. */
  readonly model: string;
  /** The states of the type's Workflow it is derived in; every state when absent. */
  readonly states?: readonly string[];
}

/** Derived's config, parsed: maxAttempts filled in. */
export interface DerivedConfig {
  /** The derived fields, in the config's order. */
  readonly fields: readonly DerivedField[];
  /** The permission a deriver needs. */
  readonly permission: string;
  /** How many failed attempts a field takes before it waits. */
  readonly maxAttempts: number;
}

/** Provenance's review, parsed. */
export interface ProvenanceReview {
  /** The behaviors whose writes enter review. */
  readonly sources: readonly string[];
  /** The confidence at or above which a write is accepted with no reviewer. */
  readonly below?: number;
  /** The permission a reviewer needs. */
  readonly permission: string;
}

/** One field's record, as Provenance.fields holds it. */
export interface ProvenanceRecord {
  /** caller, or the name of the behavior whose operation wrote it. */
  readonly source: string;
  /** The subject of the principal who wrote it. */
  readonly by: string;
  /** When, in epoch milliseconds. */
  readonly at: number;
  readonly confidence?: number;
  readonly review?: {
    readonly state: 'proposed' | 'accepted' | 'rejected' | 'edited';
    readonly by?: string;
    readonly at?: number;
  };
}

/** Numbering's config, parsed. */
export interface NumberingConfig {
  /** How a number prints: {seq} or {seq:N} once, {yyyy} and {yy} as often as wanted, and text. */
  readonly format: string;
  /** The link of the type's Links config whose target each count runs per; one count for the schema when absent. */
  readonly scope?: { readonly link: string };
  /** yearly starts each count again at 1 every UTC year. */
  readonly reset?: 'yearly';
}

/** An instance's number, as assignNumber returns it and the fields hold it. */
export interface NumberingNumber {
  readonly seq: number;
  readonly label: string;
}

/** What a deadline starts from: the create, each entry of the status into a state, or an own date-time field. */
export type TimersStart = { readonly create: true } | { readonly enters: string } | { readonly field: string };

/** The move a fire makes: to transition, while the status is one of from. */
export interface TimersThen {
  readonly transition: string;
  readonly from: readonly string[];
}

/** What cancels a pending deadline: the status out of the state it starts in, or in one of the states given. */
export type TimersUntil = { readonly leaves: string } | { readonly enters: readonly string[] };

/** One deadline of a Timers config, parsed: afterMs and then's from filled in. */
export interface TimersDeadline {
  readonly start: TimersStart;
  /** How long after its start it is due, in milliseconds; negative, before it, for a field start. */
  readonly afterMs: number;
  readonly then?: TimersThen;
  readonly until?: TimersUntil;
}

/** Timers' config, parsed: the defaults filled in. */
export interface TimersConfig {
  readonly deadlines: Readonly<Record<string, TimersDeadline>>;
  readonly sweepMs: number;
  /** The permission a caller needs to reset or snooze a deadline; nobody may when absent. */
  readonly permission?: string;
}

/** What a fire's move met: the move made, the status not one of from, a guard's veto, or a permission the caller lacks. */
export type TimersOutcome = 'moved' | 'skipped' | 'vetoed' | 'forbidden';

/** One started deadline, as the Timers.deadlines field holds it. */
export interface TimersDeadlineState {
  /** When it is due, in epoch milliseconds. */
  readonly dueAt: number;
  readonly fired: boolean;
  /** When it fired; absent before. */
  readonly firedAt?: number;
  /** What its then met when it fired; absent before, and without then. */
  readonly outcome?: TimersOutcome;
}
