/*
The event log: one append-only table, engine_events. Each change appends
one event in the transaction that makes it: an instance's create, update
and delete, and a schema's publish. An event has a global cursor, which
orders the whole log, and an instance's events also a per-instance
sequence, 1, 2, 3, ... across its life, a re-create after a delete
included. A delete appends an event and removes nothing before it. There
is no retention: the log grows until a later change adds a policy for it.

Reading is paged from a cursor, within one namespace and optionally one
schema and instance. The principal needs `read` on each event's schema;
without a schema filter, events of schemas it may not read are skipped,
so a page can hold fewer events than its limit while more follow.
*/

import { checkPrincipal, type Access, type Principal } from '../access.js';
import { EngineError } from '../errors.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import { checkSchemaName } from '../registry/document.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';

export type EventKind = 'create' | 'update' | 'delete' | 'publish';

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
  /** create: the instance; update: the merge patch; delete: null; publish: the schema document. */
  change: unknown;
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

/** appendEvent appends one event and returns its cursor; call it inside the change's transaction. */
export function appendEvent(storage: Storage, event: NewEvent): number {
  const result = storage.run(
    `INSERT INTO engine_events (kind, namespace, schema, instance_id, seq, version, actor, at, change)
     VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
    [event.kind, event.namespace, event.schema, event.instanceId, event.seq, event.version, event.actor, event.at, event.change]
  );
  return Number(result.lastInsertRowid);
}

/** nextSeq returns the sequence an instance's next event takes. */
export function nextSeq(storage: Storage, namespace: string, schema: string, instanceId: string): number {
  const row = storage.get(
    'SELECT MAX(seq) AS seq FROM engine_events WHERE namespace = ? AND schema = ? AND instance_id = ?',
    [namespace, schema, instanceId]
  );
  return row?.seq === null || row?.seq === undefined ? 1 : Number(row.seq) + 1;
}

export class EventLog {
  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly access: Access
  ) {}

  /** read returns the page of events after a cursor that the principal may read. */
  read(principal: Principal, options: ReadEventsOptions = {}): EventPage {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    const after = options.after ?? 0;
    if (!Number.isSafeInteger(after) || after < 0) {
      throw new EngineError('invalid_argument', `an event cursor is a non-negative integer, got ${String(after)}`);
    }
    const limit = pageSize(options.limit);
    const filters = ['namespace = ?', 'cursor > ?'];
    const params: Array<string | number> = [namespace, after];
    if (options.schema !== undefined) {
      checkSchemaName(options.schema);
      this.access.require(principal, 'read', namespace, options.schema);
      filters.push('schema = ?');
      params.push(options.schema);
    }
    let source = 'engine_events';
    let order = 'cursor';
    if (options.instanceId !== undefined) {
      if (options.schema === undefined) {
        throw new EngineError('invalid_argument', 'reading one instance\'s events needs its schema');
      }
      filters.push('instance_id = ?');
      params.push(options.instanceId);
      // One instance's events come from its own index, in sequence order,
      // which is their cursor order; the read scans that instance only.
      source = 'engine_events INDEXED BY engine_events_instance';
      order = 'seq';
    }
    const rows = this.storage.all(
      `SELECT cursor, kind, namespace, schema, instance_id, seq, version, actor, at, change
       FROM ${source} WHERE ${filters.join(' AND ')} ORDER BY ${order} LIMIT ?`,
      [...params, limit + 1]
    );
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
