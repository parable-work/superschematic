// The JSON the handler's module reads and writes
// (runtime/engine/rust/rules-handler-wasm/src/lib.rs states each document,
// and runtime/engine/rust/rules-handler/src/store.rs and handler.rs each
// type's serde form). Every name here is the handler's: a request, the
// reads it asks of the development layout, a store's answers, and the
// writes of the unit a store applies whole.

/** Any JSON value. */
export type Json = null | boolean | number | string | Json[] | { [member: string]: Json };

/** A JSON object. */
export type JsonObject = { [member: string]: Json };

/**
 * A schema document in the JSON data form `superschematic format
 * --to=json` writes, or a type itself: `name`, `fields` and `behaviors`.
 */
export type Document = Record<string, unknown>;

/** A JSON object a request carries: a row's fields, a patch, each use's parameters or preconditions by use id. */
export type Fields = Record<string, unknown>;

/**
 * Who makes a request: a subject and the permissions it holds; whether it
 * is a service's own principal standing in for no end user; and the
 * subject it acts for (D77), which Provenance records.
 */
export interface Caller {
  subject: string;
  permissions?: string[];
  standsIn?: boolean;
  actingFor?: string;
  /** What the caller is (D156): user, party, agent or extension; absent reads as user. */
  kind?: string;
  /** The kind of the subject it acts for; absent reads as user. */
  actingForKind?: string;
  /** The Agent whose run the caller is (D158, D200). */
  agent?: string;
  /** How the host verified the caller for this call (D200), which an Approvals requirement's proof reads. */
  assurance?: { signedInAt?: number; userVerified?: boolean };
  /** The channel kind the call came over (D157, #606), which Comments records as a message's via; absent for a call over the API. */
  channel?: string;
}

/** A refused request, shaped as the engine's errors are (D58). */
export interface Refusal {
  /** What a client branches on: not_found, forbidden, conflict, busy, ... */
  code: string;
  /** The HTTP status the code answers with. */
  status: number;
  /** The engine's class for it: EngineError, BehaviorVetoError, StoredRowsError, Error, ... */
  class: string;
  message: string;
  details?: JsonObject;
}

/** One term of a list's sort: a field, and whether it descends. */
export interface Sort {
  field: string;
  desc?: boolean;
}

/** A write of a row its table's change queue holds (D103): what it did, when, and the row before and after. */
export interface RowChange {
  write: 'create' | 'update' | 'delete' | string;
  at: number;
  before?: { own?: JsonObject; owned?: Record<string, JsonObject> };
  after?: { own?: JsonObject; owned?: Record<string, JsonObject> };
}

/**
 * What a request asks (the handler's Request). A type's name is `type`; a
 * use's id is `use`, empty for the one use whose behavior declares the
 * operation.
 */
export type HandlerRequest =
  | { kind: 'define'; document: Document }
  | { kind: 'publish'; type: string; discard?: boolean }
  | { kind: 'describe'; type: string }
  | { kind: 'get'; type: string; id: string }
  | { kind: 'create'; type: string; data?: Fields; params?: Fields }
  | { kind: 'update'; type: string; id: string; patch: Fields; preconditions?: Fields }
  | { kind: 'delete'; type: string; id: string; preconditions?: Fields }
  | { kind: 'list'; type: string; filter?: unknown; sort?: Sort[]; limit?: number; cursor?: string }
  | { kind: 'operation'; type: string; id: string; use: string; operation: string; params?: unknown; preconditions?: Fields }
  | { kind: 'tableOperation'; type: string; use: string; operation: string; params?: unknown }
  | { kind: 'deadline'; type: string; id: string; use: string; name: string; at: number; payload?: unknown }
  | { kind: 'change'; type: string; id: string; change: unknown; seq?: number }
  | { kind: 'types' }
  | { kind: 'tools' };

/** What a request runs with beside itself: its caller, the time in epoch milliseconds, the host's id for it, and a fresh version 4 UUID, hyphenated. */
export interface Context {
  caller: Caller;
  now: number;
  request: string;
  id: string;
}

/** What the handler asks a store to read. */
export type StoreRead =
  | { kind: 'live' }
  | { kind: 'draft'; type: string }
  | { kind: 'row'; type: string; id: string }
  | { kind: 'rows'; type: string }
  | { kind: 'subRows'; type: string; id: string; useId: string; table: string }
  | { kind: 'tableSubRows'; type: string; useId: string; table: string }
  | { kind: 'sharedRow'; type: string; useId: string; table: string; key: string }
  | { kind: 'armed'; type: string; id: string; useId: string; name: string };

/** A published version of a type: its number, from 1, and its schema document. */
export interface Version {
  type: string;
  version: number;
  document: unknown;
}

/** A type's version defined and not published yet. */
export interface Draft {
  type: string;
  document: unknown;
}

/** One row as a store keeps it: its id, its version, its own fields and its uses' owned fields by use id. */
export interface StoredRow {
  id: string;
  version: number;
  data: unknown;
  owned: unknown;
}

/** A sub-table row: its key and its values, a null among them left out. */
export interface SubRow {
  key: string;
  values: unknown;
}

/** A row of a use's shared table, with its version. */
export interface SharedRow {
  key: string;
  values: unknown;
  version: number;
}

/** A store's answer to one read, of the variant the read asks for. */
export type StoreAnswer =
  | { live: Version[] }
  | { draft: Draft | null }
  | { row: StoredRow | null }
  | { rows: StoredRow[] }
  | { subRows: SubRow[] }
  | { tableSubRows: { id: string; row: SubRow }[] }
  | { sharedRow: SharedRow | null }
  | { armed: { at: number; payload: unknown } | null };

/** What happens to a sub-table row. */
export type SubRowChange = { kind: 'insert'; values: JsonObject } | { kind: 'update'; values: JsonObject } | { kind: 'delete' };

/** What happens to a row of a shared table: a delete, as an update, is held to the version read (D147). */
export type SharedRowChange =
  | { kind: 'insert'; values: JsonObject }
  | { kind: 'update'; expectedVersion: number; values: JsonObject }
  | { kind: 'delete'; expectedVersion: number };

/** One write of a unit. A store applies a unit's writes in order, all of them or none. */
export type StoreWrite =
  | { write: 'putDraft'; type: string; document: unknown }
  | { write: 'putVersion'; type: string; version: number; document: unknown }
  | { write: 'insert'; type: string; id: string; data: unknown; owned: unknown }
  | { write: 'update'; type: string; id: string; expected: number; data: unknown; owned: unknown }
  | { write: 'rewrite'; type: string; id: string; data: unknown; owned: unknown }
  | { write: 'delete'; type: string; id: string; expected?: number }
  | { write: 'gone'; type: string; id: string }
  | { write: 'subRow'; type: string; id: string; behavior: string; useId: string; table: string; key: string; change: SubRowChange }
  | { write: 'sharedRow'; type: string; useId: string; table: string; key: string; change: SharedRowChange }
  | { write: 'dropUse'; type: string; behavior: string; useId: string }
  | { write: 'arm'; type: string; id: string; useId: string; name: string; at: number; payload: unknown }
  | { write: 'clear'; type: string; id: string; useId: string; name: string }
  | { write: 'clearUse'; type: string; useId: string }
  | { write: 'spend'; type: string; id: string; useId: string; name: string; at: number }
  | { write: 'release'; type: string; id: string; useId: string; name: string; due: number }
  | { write: 'event'; type: string; id: string; useId: string; kind: string; at: number; payload: unknown }
  | { write: 'change'; type: string; id: string; change: unknown }
  | { write: 'dequeue'; seq: number };

/** The writes a store applies as one unit, in order, all of them or none. */
export interface Unit {
  writes: StoreWrite[];
}

/** An HTTP response as the handler writes it: its status, its headers (lowercase names) and its body. */
export interface HttpResponseJson {
  status: number;
  headers: [string, string][];
  body: string;
}

/** One step of a run: the reads to answer before the request runs again, or its answer and unit. */
export type Step =
  | { read: StoreRead[] }
  | { done: { response: unknown; unit: Unit; http?: HttpResponseJson } }
  | { done: { refused: Refusal; unit: Unit; http?: HttpResponseJson } };

/** How a routed request's answer is written. */
export type Answer = { kind: 'data' } | { kind: 'row' } | { kind: 'created'; path: string } | { kind: 'schema' };

/** One read of the event queue. */
export interface EventsQuery {
  /** A cursor, or "head" for only what comes next. */
  after: number | 'head';
  schema?: string | null;
  instanceId?: string | null;
  behaviors: string[];
  kinds: string[];
  limit: number;
  /** Whether the client asked for server-sent events. */
  stream: boolean;
}

/** What an HTTP request routes to. */
export type Routed =
  | { handle: { request: HandlerRequest; answer: Answer } }
  | { events: EventsQuery }
  | { mcp: Record<string, never> }
  | { response: HttpResponseJson };

/** An event of the event queue (D104), as a store reads it. */
export interface QueuedEvent {
  seq: number;
  type: string;
  id: string;
  useId: string;
  kind: string;
  at: number;
  payload: unknown;
}

/** A deadline that fell due, as a runner reads it. */
export interface DueDeadline {
  type: string;
  id: string;
  useId: string;
  name: string;
  at: number;
  payload: unknown;
}

/** A message of the change queue, as a runner reads it. */
export interface QueuedChange {
  seq: number;
  type: string;
  id: string;
  change: unknown;
}

/** A behavior as a store's layout reads it: its sub-tables and its storage key. */
export interface LayoutBehavior {
  name: string;
  /** The behavior's name in snake case, which its shared sub-table is named by (dev_sub_<key>). */
  storageKey: string;
  /** Whether it keeps sub-table rows per row, in its shared sub-table. */
  rowTables: boolean;
  tables: { name: string; shared: boolean }[];
  armsDeadlines: boolean;
  hearsChanges: boolean;
}

/** What a store's tables are made from, and the bounds of the host loop. */
export interface Layout {
  namespace: string;
  behaviors: LayoutBehavior[];
  maxAttempts: number;
  maxReadRounds: number;
  maxRuns: number;
  heartbeat: string;
}

/** An access policy rule (D58's form). */
export interface PolicyRule {
  action?: string | string[];
  operation?: string | string[];
  schema?: string | string[];
  namespace?: string | string[];
  subject?: string | string[];
  allow?: boolean;
  require?: string;
}

/** The access policy the handler asks before each request a caller makes: a shorthand or rules. */
export type Policy = 'allowAll' | 'permission' | { rules: PolicyRule[] };
