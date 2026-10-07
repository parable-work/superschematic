/*
The event log: one append-only table, engine_events. Each change appends
one event in the transaction that makes it: an instance's create, update
and delete, a writing behavior operation on it, a schema's publish, and
the define of a draft, which carries the draft's hash and not its
document, so a reviewer watching the log learns a draft waits.
An event has a global cursor, which orders the whole log, and an
instance's events also a per-instance sequence, 1, 2, 3, ... across its
life, a re-create after a delete included. A delete appends an event and
removes nothing before it.

The log grows until retention prunes it (retention.ts), a namespace's
oldest events first: a namespace's floor is the last cursor retention
pruned of its events, and a read of the namespace from a cursor before
it, or before the last publish it pruned of the shared namespace the
namespace reads, is cursor_expired (CursorExpiredError), since the events
after the cursor are no longer all there. A read from the start, with no
cursor or 0, starts at the floor instead: it asked for what the log
holds, not for a place in it. The head stays the last cursor the log
gave, whatever retention pruned.

Reading is paged from a cursor, within one namespace and optionally one
schema and instance. A namespace that looks schema names up in a shared
namespace also reads the shared namespace's publish events, since the
schemas it reaches change with them; they keep the shared namespace as
theirs. The principal needs `read` on each event's schema in the
namespace it reads, a define's included: the draft route already shows
the draft to a reader. Without a schema filter, events of schemas it may
not read are skipped, so a page can hold fewer events than its limit
while more follow.

A read can also keep only some kinds, only the operations of some
behaviors, or leave out operations by name (a lease's heartbeats, say).
Those filters run on the page a read scans, as the access check does, so
a page still scans at most its limit and its next cursor is past every
event it scanned, kept or not: a reader that resumes from it neither
sees a dropped event again nor scans it again. A read from `head` starts
at the log's last event and returns none, only that cursor.

Each commit that appends events notifies the engine's watchers after it
commits (notifier.ts), which is how a stream and the runner learn the
log grew.

An event the runner's work wrote (runner/runner.ts) records its cause:
the behavior whose reaction or schedule wrote it, the event it reacted
to or the schedule that ran, and its depth, one more than its cause's.
A caller's change has none. An event a service's call wrote (D37)
records the service's deployable beside the actor, the end user it acted
for or, standing in for one, its own subject.

An instance event keeps a large member of its change in the value store
(values/store.ts): a create's instance, an update's patch, an operation's
params and patch each hold a ref in place of a member whose JSON is over
the threshold, and the event lists the pointers to them (valueRefs). A
read returns the event as the log keeps it, refs and all; a caller reads
a value by its hash (values/values.ts), and before() in a reaction puts
the values back.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import { BEHAVIOR_NAME } from '../behaviors/declaration.js';
import { CursorExpiredError, EngineError } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import { checkSchemaName } from '../registry/document.js';
import type { Row, SqlValue } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { refsOf, refsText, valuesOf } from '../values/store.js';
import { notifierOf, type EventNotifier, type EventWatcher } from './notifier.js';

export type EventKind = 'create' | 'update' | 'delete' | 'operation' | 'publish' | 'define';

/** Every event kind, in the order the log's reference lists them. */
export const EVENT_KINDS: readonly EventKind[] = ['create', 'update', 'delete', 'operation', 'publish', 'define'];

/** Why the runner's work wrote an event: a reaction to an event, or a schedule. */
export interface EventCause {
  /** The behavior whose reaction or schedule wrote it. */
  behavior: string;
  /** The cursor of the event its reaction handled; absent for a schedule's. */
  event?: number;
  /** The schedule that ran; absent for a reaction's. */
  schedule?: string;
  /** One more than its cause's depth: 1 for a reaction to a caller's change and for a schedule's write. */
  depth: number;
}

/** One entry of the event log. */
export interface EngineEvent {
  /** The global cursor: every later event has a higher one. */
  cursor: number;
  kind: EventKind;
  /** The instance's namespace; for a publish or a define, the namespace that holds the schema. */
  namespace: string;
  schema: string;
  /** Null for a publish and a define. */
  instanceId: string | null;
  /** The instance's sequence, 1 for its first event; null for a publish and a define. */
  seq: number | null;
  /** The schema version the change was made with, or the one a publish made live; null for a define, whose draft has none. */
  version: number | null;
  /** The subject of the principal that made the change. */
  actor: string;
  /** The deployable of the service whose call made the change (D37); absent for a call no service made. */
  service?: string;
  /** When, in epoch milliseconds. */
  at: number;
  /**
   * create: the instance, its behaviors' fields included; update: a merge
   * patch of the instance, the caller's patch and any change its
   * behaviors' fields took; operation: an OperationChange; delete: null;
   * publish: the schema document; define: a DefineChange.
   */
  change: unknown;
  /** What caused it, for an event a reaction or a schedule wrote; absent for a caller's change. */
  cause?: EventCause;
  /**
   * The JSON pointers into change of the members the log keeps in the
   * value store: each holds a ref, `{ "$value": <hash>, "bytes": <n> }`,
   * in place of a value whose JSON is longer than the engine's threshold.
   * Absent when change holds none.
   */
  valueRefs?: string[];
}

/** The change of an operation event: what was called, and what it did to the instance. */
export interface OperationChange {
  /** The behavior whose operation the caller called. */
  behavior: string;
  operation: string;
  /** The parameters, as its guards and handler got them. */
  params: Record<string, unknown>;
  /**
   * A merge patch of the instance as a read returns it, from before the
   * call to after it: its own fields an update() in the operation changed,
   * and its behaviors' fields.
   */
  patch: Record<string, unknown>;
}

/** The change of a define event: the draft's hash, not its document, which the draft route serves. */
export interface DefineChange {
  /** The SHA-256 of the draft's canonical JSON, hex, as its record's hash. */
  hash: string;
}

export interface ReadEventsOptions {
  /** `default` when absent. */
  namespace?: string;
  schema?: string;
  /** Only this instance's events; needs schema. */
  instanceId?: string;
  /**
   * Events after this cursor; the start of the log when absent or 0,
   * which no event has: after retention, the oldest event it kept, never
   * cursor_expired. `head`, the log's last event, for an empty page whose
   * next is that cursor. Any other cursor before the namespace's floor,
   * where retention has pruned events after it, is cursor_expired.
   */
  after?: number | 'head';
  /** How many events to scan, 50 by default and at most 500. */
  limit?: number;
  /** Only events of these kinds; at least one. */
  kinds?: readonly EventKind[];
  /** Only operation events of these behaviors, by name; at least one. */
  behaviors?: readonly string[];
  /** No operation events of operations by these names (`heartbeat`). */
  exclude?: readonly string[];
}

/** One page of the log. */
export interface EventPage {
  events: EngineEvent[];
  /** The cursor to read on from. */
  next: number;
  /** Whether the log held more events past this page when it was read. */
  more: boolean;
}

/** What appendEvent writes. */
export interface NewEvent {
  kind: EventKind;
  namespace: string;
  schema: string;
  instanceId: string | null;
  seq: number | null;
  /** Null for a define. */
  version: number | null;
  actor: string;
  /** The deployable of the calling service, when a service made the call. */
  service?: string;
  at: number;
  /** The change as JSON text, or null. */
  change: string | null;
  /** For an event the runner's work writes, what caused it. */
  cause?: EventCause;
  /**
   * For a change the value store stowed: the pointers to its refs, and the
   * hashes they name, which the event holds from its append on.
   */
  values?: { readonly refs: readonly string[]; readonly hashes: ReadonlySet<string> };
}

/**
 * appendEvent appends one event and returns its cursor; call it inside the
 * change's transaction. The engine's watchers hear of it once that
 * transaction commits, and never if it rolls back.
 */
export function appendEvent(storage: Storage, event: NewEvent): number {
  const cause = event.cause;
  const result = storage.run(
    `INSERT INTO engine_events
       (kind, namespace, schema, instance_id, seq, version, actor, service, at, change, cause_behavior, cause_event, cause_schedule, depth, value_refs)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [
      event.kind,
      event.namespace,
      event.schema,
      event.instanceId,
      event.seq,
      event.version,
      event.actor,
      event.service ?? null,
      event.at,
      event.change,
      cause?.behavior ?? null,
      cause?.event ?? null,
      cause?.schedule ?? null,
      cause?.depth ?? 0,
      refsText(event.values?.refs ?? []),
    ]
  );
  const cursor = Number(result.lastInsertRowid);
  if (event.values !== undefined && event.values.hashes.size > 0) {
    valuesOf(storage).hold(
      { namespace: event.namespace, schema: event.schema, holder: 'event', id: event.instanceId ?? '', key: String(cursor) },
      event.values.hashes
    );
  }
  const notify = () => notifierOf(storage).committed(cursor);
  if (storage.inTransaction) {
    storage.afterCommit(notify);
  } else {
    notify();
  }
  return cursor;
}

/** actorOf is who an event records as making a change: the principal's subject and, for a service's call, its deployable. */
export function actorOf(principal: Principal): { actor: string; service?: string } {
  return principal.service === undefined ? { actor: principal.subject } : { actor: principal.subject, service: principal.service.deployable };
}

/**
 * nextSeq returns the sequence an instance's next event takes: one past
 * its last event's, which retention may have pruned and kept in the
 * instance's base (retention.ts).
 */
export function nextSeq(storage: Storage, namespace: string, schema: string, instanceId: string): number {
  const params = [namespace, schema, instanceId];
  const row = storage.get('SELECT MAX(seq) AS seq FROM engine_events WHERE namespace = ? AND schema = ? AND instance_id = ?', params);
  const base = storage.get('SELECT seq FROM engine_event_bases WHERE namespace = ? AND schema = ? AND instance_id = ?', params);
  return Math.max(numberOr0(row?.seq), numberOr0(base?.seq)) + 1;
}

/** The cursor a log's head is: its last event's, or the last one retention pruned, 0 for a log that never held one. */
export function logHead(storage: Storage): number {
  const last = storage.get('SELECT MAX(cursor) AS head FROM engine_events');
  const pruned = storage.get('SELECT MAX(floor) AS head FROM engine_log_floors');
  return Math.max(numberOr0(last?.head), numberOr0(pruned?.head));
}

/**
 * logFloor returns the earliest cursor a read of a namespace may start
 * from: the last cursor retention pruned of its events and, for a read
 * that takes them, of the shared namespace's publishes, which it reads.
 * 0 when retention has pruned none.
 */
export function logFloor(storage: Storage, namespaces: Namespaces, namespace: string, withShared = true): number {
  const own = storage.get('SELECT floor FROM engine_log_floors WHERE namespace = ?', [namespace]);
  const shared = withShared ? namespaces.lookup(namespace)[1] : undefined;
  const published = shared === undefined ? undefined : storage.get('SELECT publish_floor FROM engine_log_floors WHERE namespace = ?', [shared]);
  return Math.max(numberOr0(own?.floor), numberOr0(published?.publish_floor));
}

function numberOr0(value: unknown): number {
  return value === null || value === undefined ? 0 : Number(value);
}

/** The engine_events columns toEvent reads. */
export const EVENT_COLUMNS =
  'cursor, kind, namespace, schema, instance_id, seq, version, actor, service, at, change, cause_behavior, cause_event, cause_schedule, depth, value_refs';

export class EventLog {
  private readonly notifier: EventNotifier;

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {
    this.notifier = notifierOf(storage);
  }

  /**
   * read returns the page of events after a cursor that the principal may
   * read and the filters keep. From `head` it returns no events, and the
   * log's last cursor as next. With no cursor, or 0, it starts at the
   * namespace's floor, the oldest event retention kept; from a cursor
   * before the floor it throws CursorExpiredError (cursor_expired).
   */
  read(principal: Principal, options: ReadEventsOptions = {}): EventPage {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const fromHead = options.after === 'head';
    const after = fromHead ? 0 : (options.after ?? 0);
    if (typeof after !== 'number' || !Number.isSafeInteger(after) || after < 0) {
      throw new EngineError('invalid_argument', `an event cursor is a non-negative integer or head, got ${String(after)}`);
    }
    const limit = pageSize(options.limit);
    const keeps = eventFilter(options);
    if (options.schema !== undefined) {
      checkSchemaName(options.schema);
      this.access.require(principal, 'read', namespace, options.schema);
    }
    if (options.instanceId !== undefined && options.schema === undefined) {
      throw new EngineError('invalid_argument', 'reading one instance\'s events needs its schema');
    }
    if (fromHead) {
      return { events: [], next: this.head(), more: false };
    }
    // A read from the start, with no cursor or 0, which no event has,
    // starts at the floor. One from a cursor before it would miss what
    // retention pruned. One instance's events leave the shared namespace's
    // publishes out.
    const floor = logFloor(this.storage, this.namespaces, namespace, options.instanceId === undefined);
    if (after > 0 && after < floor) {
      throw new CursorExpiredError(namespace, after, floor, this.head());
    }
    const from = Math.max(after, floor);
    let rows: Row[];
    if (options.instanceId !== undefined) {
      // One instance's events come from its own index, in sequence order,
      // which is their cursor order; the read scans that instance only.
      rows = this.storage.all(
        `SELECT ${EVENT_COLUMNS} FROM engine_events INDEXED BY engine_events_instance
         WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor > ? ORDER BY seq LIMIT ?`,
        [namespace, options.schema as string, options.instanceId, from, limit + 1]
      );
    } else {
      rows = this.namespaceRows(namespace, options.schema, from, limit + 1);
    }
    const scanned = rows.slice(0, limit);
    const readable = new Map<string, boolean>();
    const events: EngineEvent[] = [];
    for (const row of scanned) {
      const event = keeps(row);
      if (event === undefined) {
        continue;
      }
      const schema = event.schema;
      let allowed = readable.get(schema);
      if (allowed === undefined) {
        allowed = options.schema !== undefined || this.access.allows(principal, 'read', namespace, schema);
        readable.set(schema, allowed);
      }
      if (allowed) {
        events.push(event);
      }
    }
    // Next is past every event scanned, kept or not.
    const last = scanned[scanned.length - 1];
    return { events, next: last ? Number(last.cursor) : from, more: rows.length > limit };
  }

  /**
   * head returns the cursor of the log's last event, 0 for a log that
   * never held one. Retention never moves it back: when it has pruned
   * every event, the head is the last cursor it pruned.
   */
  head(): number {
    return logHead(this.storage);
  }

  /**
   * floor returns the earliest cursor a read of a namespace may start
   * from: 0 until retention prunes the namespace's events or the shared
   * namespace's publishes it reads, then the last cursor it pruned of
   * them. A read from before it is cursor_expired.
   */
  floor(namespace?: string): number {
    return logFloor(this.storage, this.namespaces, this.namespaces.resolve(namespace));
  }

  /**
   * watch registers a watcher the engine calls after each commit that
   * appended events, and once when it closes. It returns the function
   * that removes the watcher.
   */
  watch(watcher: EventWatcher): () => void {
    return this.notifier.watch(watcher);
  }

  /** The watchers registered, for tests and diagnostics. */
  get watching(): number {
    return this.notifier.size;
  }

  /** close tells every watcher the engine is closing; Engine.close calls it. */
  close(): void {
    this.notifier.close();
  }

  // The events of a namespace after a cursor, optionally of one schema,
  // with the shared namespace's publish events when the namespace looks
  // names up there. Each arm is one indexed range read of at most `count`
  // rows, and the merge keeps the first `count` by cursor.
  private namespaceRows(namespace: string, schema: string | undefined, after: number, count: number): Row[] {
    const own = `SELECT ${EVENT_COLUMNS} FROM engine_events
      WHERE namespace = ?${schema === undefined ? '' : ' AND schema = ?'} AND cursor > ? ORDER BY cursor LIMIT ?`;
    const ownParams: SqlValue[] = schema === undefined ? [namespace, after, count] : [namespace, schema, after, count];
    const shared = this.namespaces.lookup(namespace)[1];
    if (shared === undefined) {
      return this.storage.all(own, ownParams);
    }
    const published = `SELECT ${EVENT_COLUMNS} FROM engine_events
      WHERE kind = 'publish' AND namespace = ?${schema === undefined ? '' : ' AND schema = ?'} AND cursor > ? ORDER BY cursor LIMIT ?`;
    const publishedParams: SqlValue[] = schema === undefined ? [shared, after, count] : [shared, schema, after, count];
    return this.storage.all(
      `SELECT ${EVENT_COLUMNS} FROM (${own}) UNION ALL SELECT ${EVENT_COLUMNS} FROM (${published}) ORDER BY cursor LIMIT ?`,
      [...ownParams, ...publishedParams, count]
    );
  }
}

const OPERATION_NAME = /^[a-z][A-Za-z0-9]*$/;

/**
 * eventFilter checks a read's kinds, behaviors and exclude and returns
 * what keeps a scanned row: its event, or undefined for one a filter
 * drops. Kinds are compared before the change is parsed.
 */
function eventFilter(options: ReadEventsOptions): (row: Row) => EngineEvent | undefined {
  const kinds = filterList(options.kinds, 'kinds', 'event kinds', (kind) => EVENT_KINDS.includes(kind as EventKind), `one of ${EVENT_KINDS.join(', ')}`);
  const behaviors = filterList(options.behaviors, 'behaviors', 'behavior names', (name) => BEHAVIOR_NAME.test(name), 'a behavior name');
  const exclude = filterList(options.exclude, 'exclude', 'operation names', (name) => OPERATION_NAME.test(name), 'an operation name, camelCase', true);
  return (row) => {
    const kind = String(row.kind) as EventKind;
    if (kinds !== undefined && !kinds.has(kind)) {
      return undefined;
    }
    if (kind !== 'operation' && behaviors !== undefined) {
      return undefined;
    }
    const event = toEvent(row);
    if (kind === 'operation') {
      const change = event.change as OperationChange;
      if ((behaviors !== undefined && !behaviors.has(change.behavior)) || exclude?.has(change.operation)) {
        return undefined;
      }
    }
    return event;
  };
}

// filterList checks one of a read's filters: absent, or a list of names
// each the predicate takes; an empty list keeps nothing, so only exclude,
// which drops nothing then, may be empty.
function filterList(
  value: readonly string[] | undefined,
  key: string,
  what: string,
  valid: (name: string) => boolean,
  rule: string,
  emptyAllowed = false
): Set<string> | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!Array.isArray(value)) {
    throw new EngineError('invalid_argument', `${key} is a list of ${what}`);
  }
  if (value.length === 0 && !emptyAllowed) {
    throw new EngineError('invalid_argument', `${key} lists no ${what}, so it would keep no event; leave it out to keep every one`);
  }
  for (const name of value) {
    if (typeof name !== 'string' || !valid(name)) {
      throw new EngineError('invalid_argument', `${key}: ${JSON.stringify(name)} is not ${rule}`);
    }
  }
  return value.length === 0 ? undefined : new Set(value);
}

/** toEvent reads an engine_events row of EVENT_COLUMNS. */
export function toEvent(row: Row): EngineEvent {
  const event: EngineEvent = {
    cursor: Number(row.cursor),
    kind: String(row.kind) as EventKind,
    namespace: String(row.namespace),
    schema: String(row.schema),
    instanceId: row.instance_id === null ? null : String(row.instance_id),
    seq: row.seq === null ? null : Number(row.seq),
    version: row.version === null ? null : Number(row.version),
    actor: String(row.actor),
    ...(row.service === null || row.service === undefined ? {} : { service: String(row.service) }),
    at: Number(row.at),
    change: row.change === null ? null : (JSON.parse(String(row.change)) as unknown),
  };
  if (row.cause_behavior !== null && row.cause_behavior !== undefined) {
    event.cause = {
      behavior: String(row.cause_behavior),
      ...(row.cause_event === null ? {} : { event: Number(row.cause_event) }),
      ...(row.cause_schedule === null ? {} : { schedule: String(row.cause_schedule) }),
      depth: Number(row.depth),
    };
  }
  const refs = refsOf(row.value_refs);
  if (refs !== undefined) {
    event.valueRefs = refs;
  }
  return event;
}
