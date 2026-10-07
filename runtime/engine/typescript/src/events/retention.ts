/*
Event log retention (D16, amended: the log keeps what retention and the
subscriptions need). Without it the log grows for good, and with it every
large value an event holds in the value store. An engine opened with
`retention` prunes the log on the runner, as one more piece of timed
work: by age (maxAgeMs, the event's `at` against the engine's clock),
by count (maxEvents, a namespace's events past its newest maxEvents), or
both, an event going when either lets it go. A namespace's count is kept
in memory, from one count of its events at the first pass and the events
appended since at each after, so a pass reads what was appended, not the
whole log (D16, amended: retention and the value store have bounds).

Retention prunes each namespace's events on their own, oldest first, in
batches of batchSize, each in its own transaction, and never past a
namespace's hold: the cursor of the least advanced subscription there
that has not handled the events after it, which the runner computes
(runner.ts). So pruning never takes an event a subscription still has to
handle, and one namespace's stuck subscription holds only that
namespace's events. A subscription that does not advance, halted, in an
archived namespace or whose behavior the engine no longer runs, holds
like the others, unless maxHoldMs bounds it: then it holds no event older
than maxHoldMs, and once retention prunes past it, it halts with
cursor_expired when it next runs.

A namespace's floor (engine_log_floors) is the last cursor retention
pruned of its events: every event of the namespace at or before it is
gone, and a read from a cursor before it is cursor_expired (log.ts). The
floor moves first in a batch's transaction, and the trigger that keeps
the log append-only lets a delete through only at or before it.

A pruned event takes its value holders with it, so a value the store
kept only for events goes with the last of them (values/store.ts).
Before it goes, each instance event folds into its instance's base
(engine_event_bases): the instance as the log had it after the event,
its behaviors' fields included, or none after a delete, and the event's
sequence. A reaction's before() folds the instance from the base and the
events still there, and a create after a delete takes the sequence after
the base's, so the sequences an instance's events carry never repeat.
The base holds the values of what it keeps, as an event did.

Pruning changes nothing an operation returns and appends no event: what
a read of an instance, a schema or a behavior returns stays as it was,
and a read of the log from a cursor answers either every event after it
or cursor_expired, never a page with a gap, while one from the start, no
cursor or 0, reads what is kept.
*/

import type { FrozenJSON } from '../behaviors/behavior.js';
import { mergePatch } from '../instances/patch.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { refsOf, refsText, valuesOf, type ValueHolder } from '../values/store.js';
import { EVENT_COLUMNS, toEvent, type EngineEvent, type OperationChange } from './log.js';

/** EngineOptions.retention: how long the log keeps events, and how the runner prunes it. */
export interface RetentionOptions {
  /** Events older than this many milliseconds, by their `at`, are pruned. */
  maxAgeMs?: number;
  /** A namespace's events past its newest maxEvents are pruned, so each namespace keeps about this many. */
  maxEvents?: number;
  /**
   * The longest a subscription that does not advance holds retention: one
   * halted, in an archived namespace, or whose behavior the engine no
   * longer runs keeps no event older than this many milliseconds that age
   * or count lets go. Absent, it holds until it advances.
   */
  maxHoldMs?: number;
  /** How often the started runner prunes, in milliseconds; 60000 by default, at least 1000. */
  everyMs?: number;
  /** Events pruned in one transaction, after which the started runner yields; 1000 by default, at most 10000. */
  batchSize?: number;
}

export const DEFAULT_RETENTION_EVERY_MS = 60_000;
export const DEFAULT_RETENTION_BATCH_SIZE = 1_000;
export const MAX_RETENTION_BATCH_SIZE = 10_000;
const MIN_RETENTION_EVERY_MS = 1_000;

/** Retention options with their defaults filled in. */
export interface ResolvedRetention {
  readonly maxAgeMs: number | undefined;
  readonly maxEvents: number | undefined;
  readonly maxHoldMs: number | undefined;
  readonly everyMs: number;
  readonly batchSize: number;
}

/**
 * checkRetentionOptions fills in the defaults and throws TypeError for
 * options no retention could run with: neither a maximum age nor a
 * maximum count, or a number out of its range. Undefined is no retention.
 */
export function checkRetentionOptions(options: RetentionOptions | undefined): ResolvedRetention | undefined {
  if (options === undefined) {
    return undefined;
  }
  if (typeof options !== 'object' || options === null) {
    throw new TypeError('retention is { maxAgeMs?, maxEvents?, maxHoldMs?, everyMs?, batchSize? }');
  }
  const maxAgeMs = integer('maxAgeMs', options.maxAgeMs, undefined, 1, Number.MAX_SAFE_INTEGER);
  const maxEvents = integer('maxEvents', options.maxEvents, undefined, 1, Number.MAX_SAFE_INTEGER);
  if (maxAgeMs === undefined && maxEvents === undefined) {
    throw new TypeError('retention keeps events by age (maxAgeMs), by count (maxEvents) or both: give at least one');
  }
  return {
    maxAgeMs,
    maxEvents,
    maxHoldMs: integer('maxHoldMs', options.maxHoldMs, undefined, 1, Number.MAX_SAFE_INTEGER),
    everyMs: integer('everyMs', options.everyMs, DEFAULT_RETENTION_EVERY_MS, MIN_RETENTION_EVERY_MS, Number.MAX_SAFE_INTEGER) as number,
    batchSize: integer('batchSize', options.batchSize, DEFAULT_RETENTION_BATCH_SIZE, 1, MAX_RETENTION_BATCH_SIZE) as number,
  };
}

function integer(name: string, value: number | undefined, fallback: number | undefined, min: number, max: number): number | undefined {
  if (value === undefined) {
    return fallback;
  }
  if (!Number.isSafeInteger(value) || value < min || value > max) {
    throw new TypeError(`retention.${name} is an integer from ${min} to ${max}, got ${String(value)}`);
  }
  return value;
}

/** How far retention has pruned one namespace's events. */
export interface LogFloor {
  namespace: string;
  /** The last cursor pruned of the namespace's events: a read from before it is cursor_expired. */
  floor: number;
  /** The last publish event pruned of the namespace's, which the namespaces that look names up there read. */
  publishFloor: number;
  /** Events pruned of the namespace so far. */
  pruned: number;
}

/** floorsOf lists how far retention has pruned each namespace's events, by namespace. */
export function floorsOf(storage: Storage): LogFloor[] {
  return storage.all('SELECT namespace, floor, publish_floor, pruned FROM engine_log_floors ORDER BY namespace').map((row) => ({
    namespace: String(row.namespace),
    floor: Number(row.floor),
    publishFloor: Number(row.publish_floor),
    pruned: Number(row.pruned),
  }));
}

/**
 * What holds a namespace's events, as the runner finds it: the least
 * advanced cursor of its subscriptions that advance (active, retrying, not
 * yet run), and of the ones that do not (halted, archived, unregistered).
 * Retention prunes none after the first, and none after the second younger
 * than maxHoldMs, or none at all without maxHoldMs.
 */
export interface NamespaceHold {
  readonly advancing?: number;
  readonly stuck?: number;
}

export class Retention {
  // Each namespace's count of kept events, through the last cursor counted.
  private readonly counts = new Map<string, { kept: number; through: number }>();

  constructor(
    private readonly storage: Storage,
    private readonly clock: () => number,
    readonly options: ResolvedRetention
  ) {}

  /**
   * namespacesWithEvents lists the namespaces the log holds events of, by
   * name, with one indexed seek each: a namespace no longer configured
   * keeps its events until retention prunes them too.
   */
  namespacesWithEvents(): string[] {
    const names: string[] = [];
    let after = '';
    for (;;) {
      const row = this.storage.get('SELECT namespace FROM engine_events WHERE namespace > ? ORDER BY namespace LIMIT 1', [after]);
      if (!row) {
        return names;
      }
      after = String(row.namespace);
      names.push(after);
    }
  }

  /**
   * batch prunes the oldest events of one namespace that retention lets
   * go, at most batchSize of them, none its hold keeps, in one
   * transaction, and returns how many it pruned: fewer than batchSize when
   * it pruned all it may for now. Call it outside a transaction.
   */
  batch(namespace: string, hold: NamespaceHold = {}): number {
    const { maxAgeMs, maxEvents, maxHoldMs, batchSize } = this.options;
    const floor = this.floorOf(namespace);
    // Without maxHoldMs a subscription that does not advance holds as one
    // that does.
    const firm = maxHoldMs === undefined ? lowest(hold.advancing, hold.stuck) : hold.advancing;
    const limit = firm ?? Number.MAX_SAFE_INTEGER;
    if (limit <= floor) {
      return 0;
    }
    // How many of its oldest events the count lets go.
    const over = maxEvents === undefined ? 0 : this.kept(namespace, floor) - maxEvents;
    if (maxAgeMs === undefined && over <= 0) {
      return 0;
    }
    const rows = this.storage.all(
      `SELECT ${EVENT_COLUMNS} FROM engine_events WHERE namespace = ? AND cursor > ? AND cursor <= ? ORDER BY cursor LIMIT ?`,
      [namespace, floor, limit, maxAgeMs === undefined ? Math.min(batchSize, over) : batchSize]
    );
    const pruned = this.gone(rows, over, maxHoldMs === undefined ? undefined : hold.stuck);
    if (pruned.length === 0) {
      return 0;
    }
    this.storage.transaction(() => this.prune(namespace, floor, pruned));
    const count = this.counts.get(namespace);
    if (count !== undefined) {
      count.kept -= pruned.length;
    }
    return pruned.length;
  }

  // gone keeps the run of rows, oldest first, that retention lets go: each
  // older than maxAgeMs or among the namespace's over oldest, and, past a
  // stuck subscription's cursor, no younger than maxHoldMs. It stops at the
  // first that stays, so the floor never passes an event kept.
  private gone(rows: readonly Row[], over: number, stuck: number | undefined): Row[] {
    const { maxAgeMs, maxHoldMs } = this.options;
    const now = this.clock();
    const gone: Row[] = [];
    for (const [index, row] of rows.entries()) {
      const at = Number(row.at);
      const letGo = (maxAgeMs !== undefined && at < now - maxAgeMs) || index < over;
      const held = stuck !== undefined && maxHoldMs !== undefined && Number(row.cursor) > stuck && at >= now - maxHoldMs;
      if (!letGo || held) {
        break;
      }
      gone.push(row);
    }
    return gone;
  }

  // kept is how many events of a namespace the log holds after its floor:
  // the count it kept, plus the events appended since it last counted.
  // The first count reads every event the namespace keeps; each after,
  // only the new ones.
  private kept(namespace: string, floor: number): number {
    const known = this.counts.get(namespace);
    const since = known?.through ?? floor;
    const row = this.storage.get('SELECT COUNT(*) AS count, MAX(cursor) AS last FROM engine_events WHERE namespace = ? AND cursor > ?', [namespace, since]);
    const appended = Number(row?.count ?? 0);
    const count = { kept: (known?.kept ?? 0) + appended, through: appended > 0 ? Number(row?.last) : since };
    this.counts.set(namespace, count);
    return count.kept;
  }

  // prune removes a namespace's oldest rows, in cursor order, inside a
  // transaction: each instance event folds into its instance's base, the
  // events' value holders go, the floor moves past them, and then they do.
  private prune(namespace: string, floor: number, rows: readonly Row[]): void {
    const events = rows.map(toEvent);
    const byInstance = new Map<string, EngineEvent[]>();
    for (const event of events) {
      if (event.instanceId === null) {
        continue;
      }
      const key = `${event.schema}\u0000${event.instanceId}`;
      byInstance.set(key, [...(byInstance.get(key) ?? []), event]);
    }
    for (const instanceEvents of byInstance.values()) {
      this.fold(namespace, instanceEvents);
    }
    const values = valuesOf(this.storage);
    for (const event of events) {
      if (event.valueRefs !== undefined) {
        values.release({ namespace, schema: event.schema, holder: 'event', id: event.instanceId ?? '', key: String(event.cursor) });
      }
    }
    const last = events[events.length - 1].cursor;
    const published = events.filter((event) => event.kind === 'publish').map((event) => event.cursor);
    this.storage.run(
      `INSERT INTO engine_log_floors (namespace, floor, publish_floor, pruned) VALUES (?, ?, ?, ?)
       ON CONFLICT (namespace) DO UPDATE SET floor = excluded.floor,
         publish_floor = MAX(engine_log_floors.publish_floor, excluded.publish_floor), pruned = engine_log_floors.pruned + excluded.pruned`,
      [namespace, last, published.length > 0 ? Math.max(...published) : 0, events.length]
    );
    this.storage.run('DELETE FROM engine_events WHERE namespace = ? AND cursor > ? AND cursor <= ?', [namespace, floor, last]);
  }

  // fold folds one instance's pruned events, in sequence order, into its
  // base: the instance as the log had it after the last of them, as
  // before() folds it.
  private fold(namespace: string, events: readonly EngineEvent[]): void {
    const { schema, instanceId } = events[0] as EngineEvent & { instanceId: string };
    const values = valuesOf(this.storage);
    const params = [namespace, schema, instanceId];
    const base = this.storage.get('SELECT data, value_refs FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?', params);
    let data: unknown = base === undefined || base.data === null ? undefined : values.fill(JSON.parse(String(base.data)) as unknown, refsOf(base.value_refs));
    for (const event of events) {
      data = foldEvent(data, event.kind, values.fill(event.change, event.valueRefs));
    }
    const holder: ValueHolder = { namespace, schema, holder: 'base', id: instanceId, key: '' };
    const seq = events[events.length - 1].seq as number;
    if (data === undefined) {
      values.hold(holder, new Set());
      this.storage.run(
        `INSERT INTO engine_event_bases (namespace, schema, instance_id, seq, data, value_refs) VALUES (?, ?, ?, ?, NULL, NULL)
         ON CONFLICT (namespace, schema, instance_id) DO UPDATE SET seq = excluded.seq, data = NULL, value_refs = NULL`,
        [...params, seq]
      );
      return;
    }
    const stowed = values.stow(data as Record<string, unknown>);
    values.hold(holder, stowed.hashes);
    this.storage.run(
      `INSERT INTO engine_event_bases (namespace, schema, instance_id, seq, data, value_refs) VALUES (?, ?, ?, ?, ?, ?)
       ON CONFLICT (namespace, schema, instance_id) DO UPDATE SET seq = excluded.seq, data = excluded.data, value_refs = excluded.value_refs`,
      [...params, seq, JSON.stringify(stowed.value), refsText(stowed.refs)]
    );
  }

  private floorOf(namespace: string): number {
    const row = this.storage.get('SELECT floor FROM engine_log_floors WHERE namespace = ?', [namespace]);
    return row === undefined ? 0 : Number(row.floor);
  }
}

// lowest is the lesser of two cursors either of which may be absent.
function lowest(a: number | undefined, b: number | undefined): number | undefined {
  return a === undefined ? b : b === undefined ? a : Math.min(a, b);
}

/**
 * foldEvent applies one instance event's change, its values put back, to
 * the instance as the log had it before: a create gives the instance, an
 * update merges its patch, an operation its patch, and a delete leaves
 * none. The log records each change as a merge patch of what a read
 * returns, so folding an instance's events from its create gives it.
 */
export function foldEvent(data: unknown, kind: string, change: unknown): unknown {
  if (kind === 'create') {
    return change;
  }
  if (kind === 'update') {
    return mergePatch(data ?? {}, change);
  }
  if (kind === 'operation') {
    return mergePatch(data ?? {}, (change as OperationChange).patch);
  }
  return undefined;
}

/**
 * instanceBase returns what retention kept of an instance's pruned events:
 * the sequence of the last of them and the instance as the log had it
 * after it (undefined after a delete), its values put back; undefined
 * when retention has pruned none of its events.
 */
export function instanceBase(storage: Storage, namespace: string, schema: string, instanceId: string): { seq: number; data: FrozenJSON | undefined } | undefined {
  const row = storage.get('SELECT seq, data, value_refs FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?', [
    namespace,
    schema,
    instanceId,
  ]);
  if (row === undefined) {
    return undefined;
  }
  return {
    seq: Number(row.seq),
    data: row.data === null ? undefined : (valuesOf(storage).fill(JSON.parse(String(row.data)) as unknown, refsOf(row.value_refs)) as FrozenJSON),
  };
}
