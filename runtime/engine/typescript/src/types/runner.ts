/*
The runner (runtime/engine/README.md, "The runner"): it runs the
behaviors' reactions after the commit and their schedules, as the
principal the options name, and prunes the event log under a retention;
what engine.runner's status, runDue, prune and resume report.
*/

import type { Principal } from '../access.js';

/** How the deployment runs the runner. */
export interface RunnerOptions {
  /**
   * The principal reactions and schedules act as. The access policy is
   * asked as it at every read and invoke, and the events they write record
   * its subject as their actor. There is no default.
   */
  principal: Principal;
  /** A reaction runs for an event below this depth; 8 by default. */
  maxDepth?: number;
  /** Failed attempts at one event before its subscription halts; 5 by default. */
  maxAttempts?: number;
  /** The delay before the first retry, in milliseconds, doubling for each after; 1000 by default. */
  retryInitialMs?: number;
  /** The longest delay between retries, in milliseconds; 60000 by default. */
  retryMaxMs?: number;
  /** Events a subscription handles in one transaction before the runner yields; 100 by default, at most 500. */
  batchSize?: number;
}

export const DEFAULT_MAX_DEPTH = 8;

export const DEFAULT_MAX_ATTEMPTS = 5;

export const DEFAULT_RETRY_INITIAL_MS = 1_000;

export const DEFAULT_RETRY_MAX_MS = 60_000;

export const DEFAULT_BATCH_SIZE = 100;

/** Names one subscription: a behavior's reactions on a schema in a namespace. */
export interface SubscriptionKey {
  behavior: string;
  namespace: string;
  schema: string;
}

/**
 * active: it handles events as they come; retrying: its next event failed
 * and runs again at retryAt; halted: its next event failed maxAttempts
 * times, or retention pruned past its cursor, and it waits for resume;
 * off: the behavior's watches turns its reactions off for the live
 * version's config, so it handles nothing until a publish turns them on;
 * archived: its namespace is archived, and it runs nothing until the
 * namespace is unarchived; inactive: the schema's live version no longer
 * composes the behavior, or its implementation is not registered. An off
 * subscription is listed only once it has run.
 */
export type SubscriptionState = 'active' | 'retrying' | 'halted' | 'off' | 'archived' | 'inactive';

export interface SubscriptionStatus extends SubscriptionKey {
  state: SubscriptionState;
  /** The cursor of the last event it handled or passed over. */
  cursor: number;
  /** Failed attempts at the event after cursor since its last success. */
  attempts: number;
  /** When it tries again, for a retrying subscription; null otherwise. */
  retryAt: number | null;
  /** Its last failure: the event, when, and the error; null after a success. */
  failure: { cursor: number | null; at: number; error: string } | null;
  /** Events it passed over: at the depth limit, or skipped by resume. */
  skipped: number;
  lastSkip: { cursor: number; reason: 'depth' | 'resume' } | null;
}

export interface ScheduleStatus {
  behavior: string;
  schedule: string;
  namespace: string;
  schema: string;
  /**
   * retrying: its last run failed, or its interval could not be had; off:
   * its everyMs function returns null for the schema's config, so it runs
   * nothing there until a publish gives it an interval; archived: its
   * namespace is archived, and it runs nothing until it is unarchived;
   * inactive: no live version composes it now.
   */
  state: 'active' | 'retrying' | 'off' | 'archived' | 'inactive';
  /** Its interval on the schema; null for an inactive or off one and one whose everyMs function fails there. */
  everyMs: number | null;
  /** When its last run committed; null before its first, and for an off one. */
  previous: number | null;
  /** When it runs next; null for an off one, which runs nothing. */
  next: number | null;
  /** Failed runs since its last success. */
  failures: number;
  error: string | null;
}

export interface RunnerStatus {
  running: boolean;
  /** The subject of the runner's principal; null when the engine has none. */
  principal: string | null;
  /** The log's last cursor, 0 for a log that never held one: a subscription at it has nothing to do. */
  head: number;
  subscriptions: SubscriptionStatus[];
  schedules: ScheduleStatus[];
  /** The event log's retention; absent for an engine opened without it. */
  retention?: RetentionStatus;
  /** The runner's own last error, outside any reaction or schedule (a busy file, say); null after a pass that worked. */
  error: string | null;
}

/** What retention keeps and how far it has pruned. */
export interface RetentionStatus {
  maxAgeMs: number | null;
  maxEvents: number | null;
  /** The longest a subscription that does not advance holds the log; null for no bound. */
  maxHoldMs: number | null;
  everyMs: number;
  /** When the started runner last finished pruning; null before it first does. */
  previous: number | null;
  /** When it prunes next: at its first pass, then everyMs after it last finished. */
  next: number | null;
  /**
   * Each namespace retention has pruned or a subscription holds, by name:
   * its floor, the events pruned of it so far, and the least advanced
   * subscription there, past whose cursor nothing of the namespace is
   * pruned (null when none holds it), with how far that holds the log.
   */
  namespaces: RetentionNamespaceStatus[];
}

/** How far retention has pruned one namespace, and what holds it. */
export interface RetentionNamespaceStatus {
  namespace: string;
  /** The last cursor pruned of its events. */
  floor: number;
  /** Events pruned of it so far. */
  pruned: number;
  /** The cursor of the least advanced subscription, past which nothing is pruned; null when none holds it. */
  heldAt: number | null;
  heldBy: SubscriptionKey | null;
  /**
   * That subscription's state: active or retrying, which advance, or
   * halted, archived or inactive (its behavior not registered), which do
   * not; null when none holds it.
   */
  heldState: SubscriptionState | null;
  /** When the oldest event the hold keeps was appended: how far back the hold reaches; null when it keeps none. */
  heldSince: number | null;
  /**
   * For a subscription that does not advance, under maxHoldMs: the time
   * its hold of that event lasts to (heldSince + maxHoldMs), after which
   * retention prunes past it and it halts with cursor_expired when it next
   * runs; null otherwise.
   */
  heldUntil: number | null;
}

/** What one runDue did. */
export interface RunnerPass {
  /** Events a reaction handled, committed. */
  handled: number;
  /** Events passed over at the depth limit. */
  skipped: number;
  /** Failed attempts, of reactions and schedules. */
  failed: number;
  /** Schedule runs, committed. */
  scheduled: number;
  /** Events retention pruned; absent for an engine opened without retention. */
  pruned?: number;
}

/** What one prune did. */
export interface PruneResult {
  /** Events pruned. */
  pruned: number;
}
