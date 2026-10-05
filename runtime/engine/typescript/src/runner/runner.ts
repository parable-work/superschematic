/*
The runner (D16, amended): work that runs after a commit, from the event
log, as one principal the deployment names. One runs per engine, in its
process; the deployment starts and stops it.

A subscription is one behavior's reactions on one schema that composes
it, in one namespace. It hears that schema's instance events and those
of the schemas the behavior's watches names, from the publish that made
the schema compose the behavior, and keeps the cursor of the last event
it handled in engine_subscriptions. It handles one event at a time, in
log order: the reaction runs in a savepoint of a batch's transaction,
and the cursor's advance commits with it. A reaction that throws rolls
its savepoint back; the batch commits what came before it and records
the failure. The event runs again after a backoff, and after maxAttempts
failures the subscription halts at it until resume. So a reaction's
database effects happen once per event, whatever fails or crashes
before the commit; an effect outside the database happens at least once.

A schedule is one behavior's named timed work on one schema that
composes it, in one namespace, with the time of its next run in
engine_schedules. A run and that time commit together. A schedule that
came due while the runner was stopped runs once; missed ticks are not
replayed. A failing run is retried with backoff, never later than its
next tick, and a schedule never halts. Its interval is fixed, or a
function of the schema's config that the runner calls when it finds the
schedule on the schema (discover); a function that throws or gives no
valid interval fails the schedule on that schema as a failing run does,
now and each time it comes due, with the backoff alone to space the
retries, until a publish or a registration makes the runner look again.
A function that returns null turns the schedule off on the schema (D32):
the runner runs nothing there and drops what it kept for the schedule, so
a publish whose config gives an interval again finds it as for the first
time, due an interval later. A schedule's run writes its behavior's own
tables in the run's transaction, which rolls back with the run when it
throws.

Both run as schema-level work (behaviors/execution.ts, WorkExecution)
on a chain whose principal is the runner's and whose cause each event
they write records: the behavior, the event or the schedule, and a depth
one more than the cause's. A reaction does not run for an event at the
depth limit: its subscription passes over it and counts it as skipped.

Once started, the runner wakes on the commit notifier, after the commit
returns to its writer, and on a timer for the next retry or schedule. It
works through what is due in batches and yields between them. runDue
runs everything due at once, synchronously.
*/

import type { PermissionMatcher } from '@superschematic/http-runtime';

import { checkPrincipal, type Principal } from '../access.js';
import type { BehaviorReactions, BehaviorSchedule } from '../behaviors/behavior.js';
import type { BoundBehavior } from '../behaviors/composition.js';
import { Chain, WorkExecution, type Reach } from '../behaviors/execution.js';
import { MIN_SCHEDULE_MS, type BehaviorRegistry } from '../behaviors/registry.js';
import { synchronous } from '../behaviors/storage.js';
import { BehaviorError, EngineError } from '../errors.js';
import { EVENT_COLUMNS, toEvent, type EngineEvent, type EventLog } from '../events/log.js';
import type { Namespaces } from '../namespaces.js';
import type { SchemaCatalog, SchemaRecord, VersionRuntime } from '../registry/catalog.js';
import { checkSchemaName } from '../registry/document.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';

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

// The longest a timer waits before the runner looks again.
const MAX_TIMER_MS = 60_000;

/** Names one subscription: a behavior's reactions on a schema in a namespace. */
export interface SubscriptionKey {
  behavior: string;
  namespace: string;
  schema: string;
}

/**
 * active: it handles events as they come; retrying: its next event failed
 * and runs again at retryAt; halted: its next event failed maxAttempts
 * times and waits for resume; inactive: the schema's live version no
 * longer composes the behavior, or its implementation is not registered.
 */
export type SubscriptionState = 'active' | 'retrying' | 'halted' | 'inactive';

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
   * nothing there until a publish gives it an interval; inactive: no live
   * version composes it now.
   */
  state: 'active' | 'retrying' | 'off' | 'inactive';
  /** Its interval on the schema; null for an inactive or off one and one whose everyMs function fails there. */
  everyMs: number | null;
  /** When its last run committed; null before its first, and for an off one. */
  previous: number | null;
  /** When it runs next; Infinity for an off one, which runs nothing (JSON writes it as null). */
  next: number;
  /** Failed runs since its last success. */
  failures: number;
  error: string | null;
}

export interface RunnerStatus {
  running: boolean;
  /** The subject of the runner's principal; null when the engine has none. */
  principal: string | null;
  /** The log's last cursor, 0 for an empty log: a subscription at it has nothing to do. */
  head: number;
  subscriptions: SubscriptionStatus[];
  schedules: ScheduleStatus[];
  /** The runner's own last error, outside any reaction or schedule (a busy file, say); null after a pass that worked. */
  error: string | null;
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
}

interface ResolvedOptions {
  maxDepth: number;
  maxAttempts: number;
  retryInitialMs: number;
  retryMaxMs: number;
  batchSize: number;
}

// One behavior on one schema's live version in one namespace.
interface Unit {
  readonly behavior: string;
  readonly namespace: string;
  readonly schema: string;
  readonly record: SchemaRecord;
  readonly runtime: VersionRuntime;
  readonly bound: BoundBehavior;
}

interface ReactionUnit extends Unit {
  readonly reactions: BehaviorReactions<unknown>;
  /** The cursor it starts after: the publish that made the schema compose the behavior. */
  readonly start: number;
}

interface ScheduleUnit extends Unit {
  readonly name: string;
  readonly spec: BehaviorSchedule<unknown>;
  /** Its interval on the schema, that its everyMs function turns it off there, or why it gives none. */
  readonly every: { readonly everyMs: number } | { readonly off: true } | { readonly error: unknown };
}

interface Discovery {
  readonly key: string;
  readonly reactions: readonly ReactionUnit[];
  readonly schedules: readonly ScheduleUnit[];
}

interface Totals {
  handled: number;
  skipped: number;
  failed: number;
  scheduled: number;
  /** Whether a subscription moved, so more may be due. */
  moved: boolean;
}

const SUBSCRIPTION_COLUMNS =
  'behavior, namespace, schema, cursor, halted, attempts, retry_at, failed_cursor, failed_at, error, skipped, skipped_cursor, skipped_reason';
const SCHEDULE_COLUMNS = 'behavior, schedule, namespace, schema, last_run_at, next_run_at, failures, error';

export class Runner {
  private readonly options: ResolvedOptions;
  private readonly principal: Principal | undefined;
  private started = false;
  private closed = false;
  private passing = false;
  private unwatch: (() => void) | undefined;
  private soon: ReturnType<typeof setImmediate> | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private discovery: Discovery | undefined;
  // When the next retry or schedule comes due, as the last pass saw it.
  private due: number | undefined;
  private lastError: string | null = null;

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly catalog: SchemaCatalog,
    private readonly behaviors: BehaviorRegistry,
    private readonly reach: Reach,
    private readonly events: EventLog,
    private readonly clock: () => number,
    private readonly permissions: PermissionMatcher,
    options: RunnerOptions | undefined
  ) {
    this.options = resolveOptions(options);
    this.principal = options?.principal;
  }

  /** check refuses runner options no runner could run with; openEngine calls it before the file opens. */
  static check(options: RunnerOptions | undefined): void {
    resolveOptions(options);
  }

  /** Whether the runner is started. */
  get running(): boolean {
    return this.started;
  }

  /**
   * start runs what is due, then whatever the commits after it make due,
   * until stop. It needs the engine's runner principal. Starting a
   * started runner does nothing.
   */
  start(): void {
    this.ready('start');
    if (this.started) {
      return;
    }
    this.started = true;
    this.unwatch = this.events.watch({ committed: () => this.wake(), closed: () => this.stop() });
    this.wake();
  }

  /** stop stops the runner: no pass starts after it, and start resumes from the saved cursors. */
  stop(): void {
    this.started = false;
    this.unwatch?.();
    this.unwatch = undefined;
    if (this.soon !== undefined) {
      clearImmediate(this.soon);
      this.soon = undefined;
    }
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }

  /** close stops the runner for good; Engine.close calls it. */
  close(): void {
    this.stop();
    this.closed = true;
  }

  /**
   * runDue runs every reaction and schedule that is due now, the reactions
   * to what they write included, and returns what it did. It needs the
   * runner principal and no open transaction, and runs whether or not the
   * runner is started.
   */
  runDue(): RunnerPass {
    this.ready('runDue');
    const total: RunnerPass = { handled: 0, skipped: 0, failed: 0, scheduled: 0 };
    for (;;) {
      const pass = this.pass();
      total.handled += pass.handled;
      total.skipped += pass.skipped;
      total.failed += pass.failed;
      total.scheduled += pass.scheduled;
      if (!pass.moved) {
        return total;
      }
    }
  }

  /** status lists every subscription and schedule, with their cursors, retries and failures. */
  status(): RunnerStatus {
    const discovery = this.discover();
    const subscriptions = new Map<string, SubscriptionStatus>();
    for (const row of this.storage.all(`SELECT ${SUBSCRIPTION_COLUMNS} FROM engine_subscriptions`)) {
      const status = subscriptionStatus(row);
      subscriptions.set(subscriptionId(status), { ...status, state: 'inactive', retryAt: null });
    }
    for (const unit of discovery.reactions) {
      const id = subscriptionId(unit);
      const row = this.subscriptionRow(unit);
      subscriptions.set(id, row ? { ...subscriptionStatus(row), cursor: Math.max(Number(row.cursor), unit.start) } : fresh(unit));
    }
    const schedules = new Map<string, ScheduleStatus>();
    for (const row of this.storage.all(`SELECT ${SCHEDULE_COLUMNS} FROM engine_schedules`)) {
      const status = scheduleStatus(row, null);
      schedules.set(scheduleId(status), { ...status, state: 'inactive' });
    }
    for (const unit of discovery.schedules) {
      const id = scheduleId({ ...unit, schedule: unit.name });
      if ('off' in unit.every) {
        schedules.set(id, offStatus(unit));
        continue;
      }
      const row = this.scheduleRow(unit);
      if (row) {
        schedules.set(id, scheduleStatus(row, 'everyMs' in unit.every ? unit.every.everyMs : null));
      }
    }
    const head = this.storage.get('SELECT MAX(cursor) AS head FROM engine_events');
    return {
      running: this.started,
      principal: this.principal?.subject ?? null,
      head: head?.head === null || head?.head === undefined ? 0 : Number(head.head),
      subscriptions: [...subscriptions.values()].sort(byKey),
      schedules: [...schedules.values()].sort((a, b) => compare(scheduleId(a), scheduleId(b))),
      error: this.lastError,
    };
  }

  /**
   * resume clears a subscription's failure and runs it again from its
   * cursor. With skip, it first passes over the event that failed and
   * records the skip. A subscription with no failed event cannot skip.
   */
  resume(key: SubscriptionKey, options: { skip?: boolean } = {}): SubscriptionStatus {
    if (this.closed) {
      throw new Error('the engine is closed');
    }
    const row = this.storage.get(`SELECT ${SUBSCRIPTION_COLUMNS} FROM engine_subscriptions WHERE behavior = ? AND namespace = ? AND schema = ?`, [
      key.behavior,
      key.namespace,
      key.schema,
    ]);
    if (!row) {
      throw new EngineError('not_found', `no subscription of ${key.behavior} on ${key.schema} in namespace ${key.namespace} has run`);
    }
    const skip = options.skip === true;
    if (skip && (row.failed_cursor === null || Number(row.attempts) === 0)) {
      throw new EngineError(
        'invalid_argument',
        `the subscription of ${key.behavior} on ${key.schema} in namespace ${key.namespace} has no failed event to skip`
      );
    }
    this.storage.transaction(() => {
      const params = [key.behavior, key.namespace, key.schema];
      if (skip) {
        this.storage.run(
          `UPDATE engine_subscriptions SET cursor = failed_cursor, skipped = skipped + 1, skipped_cursor = failed_cursor, skipped_reason = 'resume'
           WHERE behavior = ? AND namespace = ? AND schema = ?`,
          params
        );
      }
      this.storage.run(
        `UPDATE engine_subscriptions SET halted = 0, attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
         WHERE behavior = ? AND namespace = ? AND schema = ?`,
        params
      );
    });
    this.wake();
    const status = this.status().subscriptions.find((candidate) => subscriptionId(candidate) === subscriptionId(key));
    return status as SubscriptionStatus;
  }

  private ready(what: string): void {
    if (this.closed) {
      throw new Error('the engine is closed');
    }
    if (this.principal === undefined) {
      throw new TypeError(`runner.${what} needs a principal: open the engine with runner: { principal }`);
    }
  }

  private wake(): void {
    if (!this.started || this.soon !== undefined) {
      return;
    }
    this.soon = setImmediate(() => {
      this.soon = undefined;
      this.tick();
    });
  }

  private tick(): void {
    if (!this.started || this.passing) {
      return;
    }
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    let moved: boolean;
    try {
      moved = this.pass().moved;
    } catch (error) {
      // The runner's own failure, not a reaction's: a busy file, say. It
      // tries again after the first retry delay.
      this.lastError = describe(error);
      this.arm(this.clock() + this.options.retryInitialMs);
      return;
    }
    if (moved) {
      this.wake();
    } else {
      this.arm(this.due);
    }
  }

  private arm(at: number | undefined): void {
    if (at === undefined || !this.started) {
      return;
    }
    const delay = Math.min(Math.max(0, at - this.clock()), MAX_TIMER_MS);
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.tick();
    }, delay);
  }

  // pass runs one batch of every due subscription and every due schedule.
  private pass(): Totals {
    if (this.passing) {
      throw new Error('the runner is already running: runDue cannot run inside a reaction or a schedule');
    }
    if (this.storage.inTransaction) {
      throw new Error('the runner runs outside any transaction');
    }
    this.passing = true;
    try {
      const totals: Totals = { handled: 0, skipped: 0, failed: 0, scheduled: 0, moved: false };
      this.due = undefined;
      const discovery = this.discover();
      for (const unit of discovery.reactions) {
        this.react(unit, totals);
      }
      for (const unit of discovery.schedules) {
        this.schedule(unit, totals);
      }
      this.lastError = null;
      return totals;
    } finally {
      this.passing = false;
    }
  }

  // react runs one batch of a subscription's events, if it is due.
  private react(unit: ReactionUnit, totals: Totals): void {
    const now = this.clock();
    const before = this.subscriptionRow(unit);
    // A row from before the schema last came to compose the behavior
    // starts over at the new start (ensureSubscription), whatever it held.
    const current = before !== undefined && Number(before.cursor) >= unit.start ? before : undefined;
    if (current && Number(current.halted) === 1) {
      return;
    }
    if (current && current.retry_at !== null && Number(current.retry_at) > now) {
      this.dueAt(Number(current.retry_at));
      return;
    }
    const cursor = current ? Number(current.cursor) : unit.start;
    let watched: string[];
    try {
      watched = this.watched(unit);
    } catch (error) {
      this.storage.transaction(() => {
        this.ensureSubscription(unit);
        this.fail(unit, null, Number(current?.attempts ?? 0) + 1, now, error);
      });
      totals.failed += 1;
      return;
    }
    const rows = this.storage.all(
      `SELECT ${EVENT_COLUMNS} FROM engine_events
       WHERE namespace = ? AND schema IN (${watched.map(() => '?').join(', ')}) AND instance_id IS NOT NULL AND cursor > ?
       ORDER BY cursor LIMIT ?`,
      [unit.namespace, ...watched, cursor, this.options.batchSize]
    );
    if (rows.length === 0) {
      // A failure with no event, of watches, is over once watches works.
      if (current && Number(current.attempts) > 0 && current.failed_cursor === null) {
        this.storage.transaction(() =>
          this.storage.run(
            `UPDATE engine_subscriptions SET attempts = 0, retry_at = NULL, failed_at = NULL, error = NULL
             WHERE behavior = ? AND namespace = ? AND schema = ?`,
            [unit.behavior, unit.namespace, unit.schema]
          )
        );
      }
      return;
    }
    this.storage.transaction(() => {
      const row = this.ensureSubscription(unit);
      let at = Number(row.cursor);
      let attempts = Number(row.attempts);
      let handled = 0;
      let skipped = 0;
      let lastSkip: number | undefined;
      let failure: { cursor: number; error: unknown } | undefined;
      for (const eventRow of rows) {
        const event = toEvent(eventRow);
        if (event.cursor <= at) {
          continue;
        }
        const depth = event.cause?.depth ?? 0;
        if (depth >= this.options.maxDepth) {
          skipped += 1;
          lastSkip = event.cursor;
          at = event.cursor;
          continue;
        }
        try {
          this.storage.transaction(() => this.handle(unit, event, depth));
        } catch (error) {
          failure = { cursor: event.cursor, error };
          break;
        }
        handled += 1;
        attempts = 0;
        at = event.cursor;
      }
      const key = [unit.behavior, unit.namespace, unit.schema];
      this.storage.run(
        `UPDATE engine_subscriptions SET cursor = ?, skipped = skipped + ?${lastSkip === undefined ? '' : ", skipped_cursor = ?, skipped_reason = 'depth'"}
         WHERE behavior = ? AND namespace = ? AND schema = ?`,
        [at, skipped, ...(lastSkip === undefined ? [] : [lastSkip]), ...key]
      );
      if (failure) {
        this.fail(unit, failure.cursor, attempts + 1, now, failure.error);
      } else if (handled > 0) {
        this.storage.run(
          `UPDATE engine_subscriptions SET attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
           WHERE behavior = ? AND namespace = ? AND schema = ?`,
          key
        );
      }
      totals.handled += handled;
      totals.skipped += skipped;
      totals.failed += failure ? 1 : 0;
      totals.moved ||= handled + skipped > 0;
    });
  }

  // handle runs a subscription's reaction to one event, on a chain whose
  // cause is the event, one deeper than it.
  private handle(unit: ReactionUnit, event: EngineEvent, depth: number): void {
    const chain = new Chain(this.principal as Principal, unit.namespace, this.clock(), this.permissions, {
      behavior: unit.behavior,
      event: event.cursor,
      depth: depth + 1,
    });
    this.work(unit, chain).react(unit.bound, unit.reactions, event);
  }

  // fail records a failed attempt at an event: a retry after the backoff,
  // or a halt after maxAttempts. Call it inside a transaction.
  private fail(unit: Unit, cursor: number | null, attempts: number, now: number, error: unknown): void {
    const halted = attempts >= this.options.maxAttempts;
    const retryAt = halted ? null : now + this.backoff(attempts);
    if (retryAt !== null) {
      this.dueAt(retryAt);
    }
    this.storage.run(
      `UPDATE engine_subscriptions SET halted = ?, attempts = ?, retry_at = ?, failed_cursor = ?, failed_at = ?, error = ?
       WHERE behavior = ? AND namespace = ? AND schema = ?`,
      [halted ? 1 : 0, attempts, retryAt, cursor, now, describe(error), unit.behavior, unit.namespace, unit.schema]
    );
  }

  // schedule runs a schedule once if it is due. A schedule found for the
  // first time is due an interval later. One that is off on the schema
  // runs nothing and keeps no row, so it is found anew once it is on.
  private schedule(unit: ScheduleUnit, totals: Totals): void {
    const now = this.clock();
    const row = this.scheduleRow(unit);
    const key = [unit.behavior, unit.name, unit.namespace, unit.schema];
    if ('off' in unit.every) {
      if (row) {
        this.storage.transaction(() =>
          this.storage.run('DELETE FROM engine_schedules WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?', key)
        );
      }
      return;
    }
    if (!('everyMs' in unit.every)) {
      this.failInterval(unit, row, now, unit.every.error, totals);
      return;
    }
    const everyMs = unit.every.everyMs;
    if (!row) {
      this.storage.transaction(() =>
        this.storage.run(
          `INSERT INTO engine_schedules (behavior, schedule, namespace, schema, next_run_at) VALUES (?, ?, ?, ?, ?)
           ON CONFLICT (behavior, schedule, namespace, schema) DO NOTHING`,
          [...key, now + everyMs]
        )
      );
      this.dueAt(now + everyMs);
      return;
    }
    if (Number(row.next_run_at) > now) {
      this.dueAt(Number(row.next_run_at));
      return;
    }
    const previous = row.last_run_at === null ? undefined : Number(row.last_run_at);
    this.storage.transaction(() => {
      try {
        this.storage.transaction(() => {
          const chain = new Chain(this.principal as Principal, unit.namespace, now, this.permissions, {
            behavior: unit.behavior,
            schedule: unit.name,
            depth: 1,
          });
          this.work(unit, chain).schedule(unit.bound, unit.name, unit.spec, previous);
        });
      } catch (error) {
        const failures = Number(row.failures) + 1;
        const next = now + Math.min(this.backoff(failures), everyMs);
        this.storage.run(
          `UPDATE engine_schedules SET failures = ?, error = ?, next_run_at = ?
           WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?`,
          [failures, describe(error), next, ...key]
        );
        this.dueAt(next);
        totals.failed += 1;
        return;
      }
      this.storage.run(
        `UPDATE engine_schedules SET last_run_at = ?, next_run_at = ?, failures = 0, error = NULL
         WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?`,
        [now, now + everyMs, ...key]
      );
      this.dueAt(now + everyMs);
      totals.scheduled += 1;
    });
  }

  // failInterval records a schedule whose everyMs function gives no
  // interval on the schema as a failed run, when the runner first finds
  // it and each time it comes due after: the failure and the error show
  // in status, and it is due again after the backoff. Nothing runs and
  // the schedule never halts.
  private failInterval(unit: ScheduleUnit, row: Row | undefined, now: number, error: unknown, totals: Totals): void {
    if (row && Number(row.next_run_at) > now) {
      this.dueAt(Number(row.next_run_at));
      return;
    }
    const failures = Number(row?.failures ?? 0) + 1;
    const next = now + this.backoff(failures);
    this.storage.transaction(() =>
      this.storage.run(
        `INSERT INTO engine_schedules (behavior, schedule, namespace, schema, next_run_at, failures, error) VALUES (?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (behavior, schedule, namespace, schema) DO UPDATE SET next_run_at = excluded.next_run_at, failures = excluded.failures, error = excluded.error`,
        [unit.behavior, unit.name, unit.namespace, unit.schema, next, failures, describe(error)]
      )
    );
    this.dueAt(next);
    totals.failed += 1;
  }

  private work(unit: Unit, chain: Chain): WorkExecution {
    return new WorkExecution(this.storage, unit.runtime, chain, this.reach, unit.schema, unit.record.version as number);
  }

  // watched lists the schemas a subscription hears: its own and the ones
  // its behavior's watches names for the schema's config.
  private watched(unit: ReactionUnit): string[] {
    const watches = unit.reactions.watches;
    if (watches === undefined) {
      return [unit.schema];
    }
    const named: unknown = watches.call(unit.reactions, unit.bound.config, unit.schema);
    if (!Array.isArray(named) || named.some((schema) => typeof schema !== 'string')) {
      throw new BehaviorError(unit.behavior, 'reactions.watches returns a list of schema names');
    }
    for (const schema of named as string[]) {
      checkSchemaName(schema);
    }
    return [...new Set([unit.schema, ...(named as string[])])];
  }

  private backoff(attempts: number): number {
    return Math.min(this.options.retryInitialMs * 2 ** Math.min(attempts - 1, 30), this.options.retryMaxMs);
  }

  private dueAt(at: number): void {
    this.due = this.due === undefined ? at : Math.min(this.due, at);
  }

  private subscriptionRow(key: SubscriptionKey): Row | undefined {
    return this.storage.get(`SELECT ${SUBSCRIPTION_COLUMNS} FROM engine_subscriptions WHERE behavior = ? AND namespace = ? AND schema = ?`, [
      key.behavior,
      key.namespace,
      key.schema,
    ]);
  }

  private scheduleRow(unit: ScheduleUnit): Row | undefined {
    return this.storage.get(`SELECT ${SCHEDULE_COLUMNS} FROM engine_schedules WHERE behavior = ? AND schedule = ? AND namespace = ? AND schema = ?`, [
      unit.behavior,
      unit.name,
      unit.namespace,
      unit.schema,
    ]);
  }

  // ensureSubscription returns a subscription's row, creating it at its
  // start. A row from before the schema stopped composing the behavior
  // and composed it again moves to the new start, with its failure
  // cleared: the events between belong to no subscription. Call it inside
  // a transaction.
  private ensureSubscription(unit: ReactionUnit): Row {
    const params = [unit.behavior, unit.namespace, unit.schema];
    this.storage.run(
      `INSERT INTO engine_subscriptions (behavior, namespace, schema, cursor) VALUES (?, ?, ?, ?)
       ON CONFLICT (behavior, namespace, schema) DO NOTHING`,
      [...params, unit.start]
    );
    this.storage.run(
      `UPDATE engine_subscriptions SET cursor = ?, halted = 0, attempts = 0, retry_at = NULL, failed_cursor = NULL, failed_at = NULL, error = NULL
       WHERE behavior = ? AND namespace = ? AND schema = ? AND cursor < ?`,
      [unit.start, ...params, unit.start]
    );
    return this.subscriptionRow(unit) as Row;
  }

  // discover finds every behavior with reactions or schedules on every
  // schema's live version in every namespace. It looks again when a
  // schema is published or a behavior registered.
  private discover(): Discovery {
    const head = this.storage.get("SELECT MAX(cursor) AS head FROM engine_events WHERE kind = 'publish'");
    const key = `${String(head?.head ?? 0)}\u0000${this.behaviors.names().join(',')}`;
    if (this.discovery?.key === key) {
      return this.discovery;
    }
    const reactions: ReactionUnit[] = [];
    const schedules: ScheduleUnit[] = [];
    for (const namespace of this.namespaces.names) {
      for (const summary of this.catalog.list(namespace)) {
        if (summary.liveVersion === null) {
          continue;
        }
        const record = this.catalog.find(summary.name, namespace, 'live');
        if (!record) {
          continue;
        }
        let runtime: VersionRuntime;
        try {
          runtime = this.catalog.runtimeOf(record);
        } catch (error) {
          // A version whose behaviors this engine cannot run has no work;
          // its subscriptions show as inactive until one registers.
          if (error instanceof EngineError && error.code === 'unavailable') {
            continue;
          }
          throw error;
        }
        for (const bound of runtime.composition.behaviors) {
          const implementation = bound.behavior.implementation;
          const unit = { behavior: bound.behavior.name, namespace, schema: record.name, record, runtime, bound };
          if (implementation.reactions !== undefined) {
            reactions.push({ ...unit, reactions: implementation.reactions, start: this.startOf(record, bound.behavior.name) });
          }
          for (const [name, spec] of Object.entries(implementation.schedules ?? {})) {
            schedules.push({ ...unit, name, spec, every: intervalOf(bound, name, spec) });
          }
        }
      }
    }
    reactions.sort(byKey);
    schedules.sort((a, b) => compare(scheduleId({ ...a, schedule: a.name }), scheduleId({ ...b, schedule: b.name })));
    this.discovery = { key, reactions, schedules };
    return this.discovery;
  }

  // startOf is the cursor a subscription starts after: the publish of the
  // earliest version of the run of versions, up to the live one, that
  // compose the behavior.
  private startOf(live: SchemaRecord, behavior: string): number {
    let first = live.version as number;
    for (let version = first - 1; version >= 1; version -= 1) {
      const older = this.catalog.find(live.name, live.namespace, version);
      if (!older || !composes(older, behavior)) {
        break;
      }
      first = version;
    }
    const row = this.storage.get("SELECT cursor FROM engine_events WHERE kind = 'publish' AND namespace = ? AND schema = ? AND version = ?", [
      live.namespace,
      live.name,
      first,
    ]);
    return row ? Number(row.cursor) : 0;
  }
}

// intervalOf is a schedule's interval on one schema: its everyMs, or what
// its everyMs function returns for the schema's config, held to the rule
// registration holds a fixed one to, or off when the function returns
// null. A function that throws, returns a promise or returns anything else
// (undefined included) gives the error instead.
function intervalOf(
  bound: BoundBehavior,
  name: string,
  spec: BehaviorSchedule<unknown>
): { everyMs: number } | { off: true } | { error: unknown } {
  const every = spec.everyMs;
  if (typeof every === 'number') {
    return { everyMs: every };
  }
  try {
    const value: unknown = every.call(spec, bound.config);
    synchronous(bound.behavior.name, `schedule ${name} everyMs`, value);
    if (value === null) {
      return { off: true };
    }
    if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < MIN_SCHEDULE_MS) {
      return {
        error: new BehaviorError(
          bound.behavior.name,
          `schedule ${name}: everyMs(config) returns an integer of at least ${MIN_SCHEDULE_MS}, got ${typeof value === 'number' ? String(value) : typeof value}`
        ),
      };
    }
    return { everyMs: value };
  } catch (error) {
    return { error };
  }
}

function composes(record: SchemaRecord, behavior: string): boolean {
  return ((record.document.types ?? {})[record.instanceType]?.behaviors ?? []).some((ref) => ref.name === behavior);
}

function resolveOptions(options: RunnerOptions | undefined): ResolvedOptions {
  if (options !== undefined) {
    if (typeof options !== 'object' || options === null) {
      throw new TypeError('runner is { principal, maxDepth?, maxAttempts?, retryInitialMs?, retryMaxMs?, batchSize? }');
    }
    try {
      checkPrincipal(options.principal);
    } catch {
      throw new TypeError('runner.principal is the principal reactions and schedules act as: { subject, permissions }');
    }
    if (!Array.isArray(options.principal.permissions)) {
      throw new TypeError('runner.principal.permissions is a list of permissions');
    }
  }
  const resolved = {
    maxDepth: integer('maxDepth', options?.maxDepth, DEFAULT_MAX_DEPTH, 1, 1_000),
    maxAttempts: integer('maxAttempts', options?.maxAttempts, DEFAULT_MAX_ATTEMPTS, 1, 1_000),
    retryInitialMs: integer('retryInitialMs', options?.retryInitialMs, DEFAULT_RETRY_INITIAL_MS, 1, Number.MAX_SAFE_INTEGER),
    retryMaxMs: integer('retryMaxMs', options?.retryMaxMs, DEFAULT_RETRY_MAX_MS, 1, Number.MAX_SAFE_INTEGER),
    batchSize: integer('batchSize', options?.batchSize, DEFAULT_BATCH_SIZE, 1, 500),
  };
  if (resolved.retryMaxMs < resolved.retryInitialMs) {
    throw new TypeError(`runner.retryMaxMs (${resolved.retryMaxMs}) is less than runner.retryInitialMs (${resolved.retryInitialMs})`);
  }
  return resolved;
}

function integer(name: string, value: number | undefined, fallback: number, min: number, max: number): number {
  if (value === undefined) {
    return fallback;
  }
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new TypeError(`runner.${name} is an integer from ${min} to ${max}, got ${String(value)}`);
  }
  return value;
}

// describe names an error as the status shows it: an EngineError by its
// code, any other by its name.
function describe(error: unknown): string {
  if (error instanceof EngineError) {
    return `${error.code}: ${error.message}`;
  }
  if (error instanceof Error) {
    return `${error.name}: ${error.message}`;
  }
  return String(error);
}

function subscriptionStatus(row: Row): SubscriptionStatus {
  const halted = Number(row.halted) === 1;
  const attempts = Number(row.attempts);
  return {
    behavior: String(row.behavior),
    namespace: String(row.namespace),
    schema: String(row.schema),
    state: halted ? 'halted' : attempts > 0 ? 'retrying' : 'active',
    cursor: Number(row.cursor),
    attempts,
    retryAt: row.retry_at === null ? null : Number(row.retry_at),
    failure:
      row.error === null
        ? null
        : { cursor: row.failed_cursor === null ? null : Number(row.failed_cursor), at: Number(row.failed_at), error: String(row.error) },
    skipped: Number(row.skipped),
    lastSkip:
      row.skipped_cursor === null ? null : { cursor: Number(row.skipped_cursor), reason: String(row.skipped_reason) as 'depth' | 'resume' },
  };
}

function fresh(unit: ReactionUnit): SubscriptionStatus {
  return {
    behavior: unit.behavior,
    namespace: unit.namespace,
    schema: unit.schema,
    state: 'active',
    cursor: unit.start,
    attempts: 0,
    retryAt: null,
    failure: null,
    skipped: 0,
    lastSkip: null,
  };
}

function scheduleStatus(row: Row, everyMs: number | null): ScheduleStatus {
  const failures = Number(row.failures);
  return {
    behavior: String(row.behavior),
    schedule: String(row.schedule),
    namespace: String(row.namespace),
    schema: String(row.schema),
    state: failures > 0 ? 'retrying' : 'active',
    everyMs,
    previous: row.last_run_at === null ? null : Number(row.last_run_at),
    next: Number(row.next_run_at),
    failures,
    error: row.error === null ? null : String(row.error),
  };
}

// offStatus is the status of a schedule its everyMs function turns off on
// the schema: nothing kept, nothing due.
function offStatus(unit: ScheduleUnit): ScheduleStatus {
  return {
    behavior: unit.behavior,
    schedule: unit.name,
    namespace: unit.namespace,
    schema: unit.schema,
    state: 'off',
    everyMs: null,
    previous: null,
    next: Number.POSITIVE_INFINITY,
    failures: 0,
    error: null,
  };
}

function subscriptionId(key: SubscriptionKey): string {
  return `${key.behavior}\u0000${key.namespace}\u0000${key.schema}`;
}

function scheduleId(key: { behavior: string; schedule: string; namespace: string; schema: string }): string {
  return `${key.behavior}\u0000${key.schedule}\u0000${key.namespace}\u0000${key.schema}`;
}

function byKey(a: SubscriptionKey, b: SubscriptionKey): number {
  return compare(subscriptionId(a), subscriptionId(b));
}

function compare(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}
