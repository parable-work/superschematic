/*
The event log (D16): every change an engine makes is an event, in one
global cursor order, which engine.events reads by page, watches and
streams, and which retention (RetentionOptions) prunes by age or count.
*/

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
  /**
   * The instance's sequence, 1 for its first event; null for a publish, a
   * define and a beside operation's event.
   */
  seq: number | null;
  /**
   * True for a beside operation's event (D77): an operation event of an
   * instance with no sequence, whose change is a BesideChange and which
   * replay skips. Absent otherwise.
   */
  beside?: true;
  /** The schema version the change was made with, or the one a publish made live; null for a define, whose draft has none. */
  version: number | null;
  /** The subject of the principal that made the change. */
  actor: string;
  /** The deployable of the service whose call made the change (D37); absent for a call no service made. */
  service?: string;
  /**
   * The subject the principal that made the change acted for (D77), beside
   * actor; absent when it acted for no one, as the runner's work never does.
   */
  for?: string;
  /** When, in epoch milliseconds. */
  at: number;
  /**
   * create: the instance as a read returns it, { data, behaviors }; update:
   * a merge patch of that, the caller's patch under data and any change
   * its behaviors' fields took under behaviors; operation: an
   * OperationChange; delete: null; publish: the schema document; define: a
   * DefineChange.
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
   * A merge patch of the instance as a read returns it, { data, behaviors },
   * from before the call to after it: under data its own fields an update()
   * in the operation changed, under behaviors its behaviors' fields; a part
   * the call did not change is absent.
   */
  patch: Record<string, unknown>;
  /**
   * Where the values of the own fields the operation changed came from, by
   * field: the behavior whose update() wrote each, and the confidence and
   * source it gave. Absent when the operation attributed none (D16, amended).
   */
  attribution?: Record<string, { behavior: string; confidence?: number; source?: string }>;
}

/**
 * The change of a beside operation's event (D77): what was called and what
 * it returned, in place of a patch, since it changes nothing a read of the
 * instance returns.
 */
export interface BesideChange {
  /** The behavior whose operation the caller called. */
  behavior: string;
  operation: string;
  /** The parameters, as its guard and handler got them. */
  params: Record<string, unknown>;
  /** The operation's result. */
  result: unknown;
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
  /**
   * `false` drops beside operations' events (D77), for a consumer that
   * rebuilds instances; `true` keeps only them. Absent, both.
   */
  beside?: boolean;
}

/** One page of the log. */
export interface EventPage {
  events: EngineEvent[];
  /** The cursor to read on from. */
  next: number;
  /** Whether the log held more events past this page when it was read. */
  more: boolean;
}

/** What EventLog.watch calls: after each commit that appended events, and once when the engine closes. */
export interface EventWatcher {
  /** The log grew: called after the commit, once for each event it appended, with that event's cursor. */
  committed(cursor: number): void;
  /** The engine closed; nothing follows. */
  closed?(): void;
}

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
