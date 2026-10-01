/*
The event log: one append-only table, engine_events. Each change appends
one event in the transaction that makes it: an instance's create, update
and delete, a writing behavior operation on it, and a schema's publish.
An event has a global cursor, which orders the whole log, and an
instance's events also a per-instance sequence, 1, 2, 3, ... across its
life, a re-create after a delete included. A delete appends an event and
removes nothing before it. There is no retention: the log grows until a
later change adds a policy for it.

Reading is paged from a cursor, within one namespace and optionally one
schema and instance. A namespace that looks schema names up in a shared
namespace also reads the shared namespace's publish events, since the
schemas it reaches change with them; they keep the shared namespace as
theirs. The principal needs `read` on each event's schema in the
namespace it reads; without a schema filter, events of schemas it may not
read are skipped, so a page can hold fewer events than its limit while
more follow.

Each commit that appends events notifies the engine's watchers after it
commits (notifier.ts), which is how a stream learns the log grew.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import { EngineError } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import { checkSchemaName } from '../registry/document.js';
import type { Row, SqlValue } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { notifierOf, type EventNotifier, type EventWatcher } from './notifier.js';

export type EventKind = 'create' | 'update' | 'delete' | 'operation' | 'publish';

/** One entry of the event log. */
export interface EngineEvent {
  /** The global cursor: every later event has a higher one. */
  cursor: number;
  kind: EventKind;
  /** The instance's namespace; for a publish, the namespace that holds the schema. */
  namespace: string;
  schema: string;
  /** Null for a publish. */
  instanceId: string | null;
  /** The instance's sequence, 1 for its first event; null for a publish. */
  seq: number | null;
  /** The schema version the change was made with. */
  version: number;
  /** The subject of the principal that made the change. */
  actor: string;
  /** When, in epoch milliseconds. */
  at: number;
  /**
   * create: the instance, its behaviors' fields included; update: a merge
   * patch of the instance, the caller's patch and any change its
   * behaviors' fields took; operation: an OperationChange; delete: null;
   * publish: the schema document.
   */
  change: unknown;
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

export interface ReadEventsOptions {
  /** `default` when absent. */
  namespace?: string;
  schema?: string;
  /** Only this instance's events; needs schema. */
  instanceId?: string;
  /** Events after this cursor; 0, the start of the log, when absent. */
  after?: number;
  /** How many events to scan, 50 by default and at most 500. */
  limit?: number;
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
  version: number;
  actor: string;
  at: number;
  /** The change as JSON text, or null. */
  change: string | null;
}

/**
 * appendEvent appends one event and returns its cursor; call it inside the
 * change's transaction. The engine's watchers hear of it once that
 * transaction commits, and never if it rolls back.
 */
export function appendEvent(storage: Storage, event: NewEvent): number {
  const result = storage.run(
    `INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [event.kind, event.namespace, event.schema, event.instanceId, event.seq, event.version, event.actor, event.at, event.change]
  );
  const cursor = Number(result.lastInsertRowid);
  const notify = () => notifierOf(storage).committed(cursor);
  if (storage.inTransaction) {
    storage.afterCommit(notify);
  } else {
    notify();
  }
  return cursor;
}

/** nextSeq returns the sequence an instance's next event takes. */
export function nextSeq(storage: Storage, namespace: string, schema: string, instanceId: string): number {
  const row = storage.get(
    'SELECT MAX(seq) AS seq FROM engine_events WHERE namespace = ? AND schema = ? AND instance_id = ?',
    [namespace, schema, instanceId]
  );
  return row?.seq === null || row?.seq === undefined ? 1 : Number(row.seq) + 1;
}

const EVENT_COLUMNS = 'cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change';

export class EventLog {
  private readonly notifier: EventNotifier;

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {
    this.notifier = notifierOf(storage);
  }

  /** read returns the page of events after a cursor that the principal may read. */
  read(principal: Principal, options: ReadEventsOptions = {}): EventPage {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const after = options.after ?? 0;
    if (!Number.isSafeInteger(after) || after < 0) {
      throw new EngineError('invalid_argument', `an event cursor is a non-negative integer, got ${String(after)}`);
    }
    const limit = pageSize(options.limit);
    if (options.schema !== undefined) {
      checkSchemaName(options.schema);
      this.access.require(principal, 'read', namespace, options.schema);
    }
    let rows: Row[];
    if (options.instanceId !== undefined) {
      if (options.schema === undefined) {
        throw new EngineError('invalid_argument', 'reading one instance\'s events needs its schema');
      }
      // One instance's events come from its own index, in sequence order,
      // which is their cursor order; the read scans that instance only.
      rows = this.storage.all(
        `SELECT ${EVENT_COLUMNS} FROM engine_events INDEXED BY engine_events_instance
         WHERE namespace = ? AND schema = ? AND instance_id = ? AND cursor > ? ORDER BY seq LIMIT ?`,
        [namespace, options.schema, options.instanceId, after, limit + 1]
      );
    } else {
      rows = this.namespaceRows(namespace, options.schema, after, limit + 1);
    }
    const scanned = rows.slice(0, limit);
    const readable = new Map<string, boolean>();
    const events: EngineEvent[] = [];
    for (const row of scanned) {
      const schema = String(row.schema);
      let allowed = readable.get(schema);
      if (allowed === undefined) {
        allowed = options.schema !== undefined || this.access.allows(principal, 'read', namespace, schema);
        readable.set(schema, allowed);
      }
      if (allowed) {
        events.push(toEvent(row));
      }
    }
    const last = scanned[scanned.length - 1];
    return { events, next: last ? Number(last.cursor) : after, more: rows.length > limit };
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

function toEvent(row: Row): EngineEvent {
  return {
    cursor: Number(row.cursor),
    kind: String(row.kind) as EventKind,
    namespace: String(row.namespace),
    schema: String(row.schema),
    instanceId: row.instance_id === null ? null : String(row.instance_id),
    seq: row.seq === null ? null : Number(row.seq),
    version: Number(row.version),
    actor: String(row.actor),
    at: Number(row.at),
    change: row.change === null ? null : (JSON.parse(String(row.change)) as unknown),
  };
}
