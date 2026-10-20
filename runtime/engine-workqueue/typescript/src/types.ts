/*
The work queue's behaviors' configs, records and constants (D16): what a
schema gives Lease, Assignment, Queue, Presence, Blueprint, Budget and
Retries, and what their operations return. The behaviors are Rust
(runtime/engine-workqueue/rust, D58), in the engine's addon.
*/

import type { LinkPin } from '@superschematic/engine';

/** How often the runner expires lapsed leases when the config gives no sweepMs. */
export const DEFAULT_SWEEP_MS = 5000;

/** The most leases one run of the expire schedule expires; the rest wait for the next. */
export const MAX_SWEEP = 1000;

/** Why expire applied an expiry. */
export type ExpiryReason = 'ttl' | 'maxHold' | 'holder';

/** How a lease ended: a release, an abandon, or an expiry for its reason. */
export type LeaseEnd = 'release' | 'abandon' | ExpiryReason;

/** A move of the status a lease's end makes: to transition, from one of from. */
export interface LeaseTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/**
 * A directive the runner sends the holder of an instance's active lease
 * when the target of one of its links moves on: a new revision, or a
 * release.
 */
export interface LeaseDirectOn {
  /** The link of the type's Links whose target's move sends it. */
  readonly link: string;
  /** The link's schema, as the type's Links config gives it. */
  readonly schema: string;
  /** What the link pins, a revision or a release: a move of that kind sends it only to the instances the target moved past. */
  readonly pin?: LinkPin;
  /** The directive's name. */
  readonly name: string;
  /** Its data, beside the revised member the runner adds. */
  readonly data?: Readonly<Record<string, unknown>>;
}

/** Lease's config, parsed: the defaults filled in. */
export interface LeaseConfig {
  readonly ttlMs: number;
  readonly heartbeatMs: number;
  readonly sweepMs: number;
  readonly maxHoldMs?: number;
  readonly maxHoldField?: string;
  readonly onExpiry?: LeaseTransition;
  readonly maxExpiries?: number;
  readonly escalate?: LeaseTransition;
  /** The writing operations other principals may run while a lease is active, as `<Behavior>.<operation>`. */
  readonly exempt: readonly string[];
  /** Whether every write under an active lease presents the current token. */
  readonly requireToken: boolean;
  readonly acquirePermission?: string;
  readonly overridePermission?: string;
  readonly directPermission?: string;
  /** The directives the runner sends the holder when a link's target moves on, one per link. */
  readonly directOn: readonly LeaseDirectOn[];
}

/** Lease's fields, as a read returns them under behaviors.Lease: a field with no value is absent. */
export interface LeaseRecord {
  /** The principal that holds the lease; absent when it is free. */
  readonly holder?: string;
  readonly token: number;
  /** Absent when free. */
  readonly acquiredAt?: number;
  /** When its holder last acquired or renewed it; absent when free. */
  readonly renewedAt?: number;
  /** When the lease stops being active: its expiry time, or its longest hold if that comes first; absent when free. */
  readonly expiresAt?: number;
  /** Whether it is held and neither expired nor past its longest hold. */
  readonly active: boolean;
  /** How many expiries the instance has had, abandons included. */
  readonly expiries: number;
  /** How and when the last lease ended; absent while one is held, and before the first. */
  readonly ended?: { readonly reason: LeaseEnd; readonly at: number };
}

/** A directive, as heartbeat returns it. */
export interface DirectiveRecord {
  /** Its number in its lease, 1, 2, 3, .... */
  readonly id: number;
  readonly name: string;
  readonly data?: Record<string, unknown>;
  /** The key it was sent with, which sends it once per lease. */
  readonly dedupeKey?: string;
  readonly createdAt: number;
  readonly createdBy: string;
}

/** Assignment's config. */
export interface AssignmentConfig {
  /** The permission that assigns another principal, reassigns, and unassigns another principal. */
  readonly permission?: string;
}

/** The most instances one claimNext tries when the config gives no maxCandidates. */
export const DEFAULT_MAX_CANDIDATES = 100;

/** excluded_until of an instance only a change lets back in. */
export const EXCLUDED = Number.MAX_SAFE_INTEGER;

/** Queue's config, parsed: the defaults filled in, and what it reads of the type's other behaviors. */
export interface QueueConfig {
  readonly claim: { readonly from: readonly string[]; readonly to: string };
  readonly priorityField?: string;
  readonly match: readonly string[];
  readonly maxCandidates: number;
  /** The pinned links of the type's Links config, to a revision or a release, whose staleness keeps an instance out. */
  readonly excludeStale: readonly string[];
  /** Whether the type composes Dependencies, whose blocked field a claim waits on. */
  readonly dependencies: boolean;
  /** Whether the type composes Budget, whose reserve a claim calls and whose checkReserve says whether it fits. */
  readonly budget: boolean;
  /** Whether the type composes Retries, whose exhausted instance a claim is refused. */
  readonly retries: boolean;
  /** Whether the type composes Assignment, whose assignee assignedOnly reads. */
  readonly assignment: boolean;
  /** The type's Lease maxExpiries: an instance with that many is no candidate. */
  readonly maxExpiries?: number;
}

/** A claim, as claim returns it and claimNext's claimed holds it. */
export interface ClaimRecord {
  readonly id: string;
  readonly token: number;
  readonly expiresAt: number;
  readonly heartbeatMs: number;
}

/** A move of the status a miss or a beat makes: to transition, from one of from. */
export interface PresenceTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** Presence's config, parsed: the defaults filled in. */
export interface PresenceConfig {
  readonly ttlMs: number;
  readonly principalField: string;
  readonly onMissed?: PresenceTransition;
  readonly onBeat?: PresenceTransition;
  /** The schemas whose leases the principal holds a miss expires. */
  readonly releaseLeases: readonly string[];
  readonly sweepMs: number;
}

/** Presence's fields, as a read returns them under behaviors.Presence: a field with no value is absent. */
export interface PresenceRecord {
  /** When the instance is missed unless beaten; absent for one that has never had one. */
  readonly deadline?: number;
  /** Absent before the first beat. */
  readonly lastBeatAt?: number;
  /** Whether a miss was applied and no beat came after it. */
  readonly missed: boolean;
  /** The instances whose lease the last miss expired, by schema; absent before a miss. */
  readonly released?: Readonly<Record<string, readonly string[]>>;
}

/** The most steps a map holds: the most children one stamp creates. */
export const MAX_STEPS = 500;

/** When a step is stamped: this instance's field equals a value, or is a list that includes one. */
export type BlueprintWhen = { readonly field: string; readonly equals: unknown } | { readonly field: string; readonly includes: unknown };

/** One step of a map of steps, parsed. */
export interface BlueprintStep {
  readonly key: string;
  /** The keys of the steps whose children block this one's. */
  readonly after: readonly string[];
  readonly when?: BlueprintWhen;
  /** The child's fields beside its key and the copied ones. */
  readonly data: Readonly<Record<string, unknown>>;
}

/** Blueprint's config, parsed. */
export interface BlueprintConfig {
  readonly schema: string;
  readonly parentLink: string;
  readonly keyField: string;
  /** The inline steps, in the config's order; absent with from. */
  readonly steps?: readonly BlueprintStep[];
  readonly from?: { readonly link: string; readonly field: string };
  readonly copyFields: readonly string[];
  readonly copyLinks: readonly string[];
  /** This type's own fields, and the ones that hold lists, which a step's when may name. */
  readonly fields: readonly string[];
  readonly listFields: readonly string[];
}

/** Blueprint's fields, as a read returns them under behaviors.Blueprint: children is absent until the instance is stamped. */
export interface BlueprintRecord {
  readonly children?: ReadonlyArray<{ readonly key: string; readonly id: string }>;
}

/** One meter of a Budget config, parsed. */
export interface BudgetMeter {
  readonly limit?: number;
  readonly limitField?: string;
  readonly reserve?: number;
  readonly reserveField?: string;
  /** The Links link whose target is the enclosing scope. */
  readonly scope?: string;
  /** Whether used starts again at 0 at each UTC day. */
  readonly daily: boolean;
}

/** Budget's config, parsed. */
export interface BudgetConfig {
  readonly meters: Readonly<Record<string, BudgetMeter>>;
  readonly limitPermission?: string;
  readonly onExceeded?: { readonly direct: string };
  /** The move of the status usage that leaves the instance over a limit makes. */
  readonly escalate?: BudgetTransition;
  /** Whether the type composes Lease, whose active lease a reservation lives within. */
  readonly leased: boolean;
}

/** A move of the status: to transition, from one of from. */
export interface BudgetTransition {
  readonly transition: string;
  readonly from: readonly string[];
}

/** One meter, as the meters field holds it. */
export interface MeterRecord {
  readonly used: number;
  /** The instance's own reservation and what it holds for the instances inside it. */
  readonly reserved: number;
  /** null when the meter has no limit. */
  readonly limit: number | null;
  /** limit minus used and reserved, below 0 once usage passed the limit; null without a limit. */
  readonly remaining: number | null;
}

/** An instance of a chain whose usage of a meter is over its limit. */
export interface Overrun {
  readonly schema: string;
  readonly id: string;
  readonly used: number;
  readonly limit: number;
  /** Whether its escalate moved its status. */
  readonly escalated: boolean;
}

/** A value of a scope a checkReserve answer turns on, as a reference hears it: a JSON pointer into the scope as a read returns it, and the number it crosses. */
export interface ScopeValue {
  readonly path: string;
  readonly crosses?: number;
}

/** An enclosing scope a checkReserve answer read, and the values of it the answer turns on. */
export interface ScopeRead {
  readonly schema: string;
  readonly id: string;
  readonly hears: readonly ScopeValue[];
}

/** What checkReserve answers. */
export interface ReserveCheck {
  readonly fits: boolean;
  /** When it does not fit, the start of the next UTC day if that alone makes it fit; null otherwise. */
  readonly until: number | null;
  /** The enclosing scopes the answer read, innermost first. */
  readonly scopes: readonly ScopeRead[];
}

/** A failure class: a cap of failures with an optional hint for the next attempt, or terminal. */
export type RetryClass = { readonly attempts: number; readonly hint?: string } | 'terminal';

/** Retries' config, parsed: the defaults filled in. */
export interface RetriesConfig {
  readonly classes: Readonly<Record<string, RetryClass>>;
  readonly totalAttempts: number;
  readonly limitsField?: string;
  readonly keepBest?: { readonly minDelta: number; readonly neverRegress: readonly string[] };
  readonly stuckAfter?: number;
  readonly resultField?: string;
  readonly exhaustedState: string;
  /** The states exhaustion moves the status from. */
  readonly from: readonly string[];
  readonly permission?: string;
  /** The permission that changes limitsField once the instance exists. */
  readonly limitsPermission?: string;
  /** Whether the type composes Lease, whose holder does not change limitsField. */
  readonly leased: boolean;
}

/** Retries' fields, as a read returns them under behaviors.Retries: a field with no value is absent. */
export interface RetriesRecord {
  /** Failures counted. */
  readonly total: number;
  /** Failures counted, by class: every class of the config. */
  readonly classAttempts: Readonly<Record<string, number>>;
  /** The score of the kept result; absent when none is kept with a score. */
  readonly bestScore?: number;
  readonly exhausted: boolean;
  /** Whether a repeated signature exhausted it. */
  readonly stuck: boolean;
}

/** What recordAttempt returns. */
export interface AttemptRecord {
  readonly failure: string | null;
  readonly score: number | null;
  readonly kept: boolean;
  readonly total: number;
  readonly classAttempts: Readonly<Record<string, number>>;
  readonly exhausted: boolean;
  readonly stuck: boolean;
  /** The failure class's hint; null for a success and a class without one. */
  readonly hint: string | null;
}
