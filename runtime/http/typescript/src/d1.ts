/*
The database of a TypeScript API whose DB service lists sqlite first (D64,
section 18.6 of docs/stack-model.md): SqlDatabase, the runtime's copy of
D1's interface, which the generated Deps.db holds. A D1 binding provides it
on Workers, and openSqlite (./sqlite) provides it over bun:sqlite under
stack dev, so an implementation runs on both unchanged.

The interface is D1's: prepare(sql).bind(...values) and then first, all,
run or raw; batch, which runs statements in one transaction, atomically;
and exec, which runs the statements of a script, one per line, unbound. A
statement's placeholders are ? and ?N, which D1 and SQLite both read. D1
has no interactive transaction: batch is the only way to make several
statements atomic, and an implementation that needs one writes it so.

Values convert as D1 converts them, on both. bind takes a string, a number,
a boolean (stored as 1 or 0), null, or bytes (an ArrayBuffer or a view of
one), and throws D1's error, D1_TYPE_ERROR: Type 'undefined' not supported
for value 'undefined', for undefined or any other value: an optional field
binds as `value ?? null`. A row reads back a BLOB as an Array of its bytes,
not a Uint8Array (new Uint8Array(row.image) makes one), a boolean as 1 or
0, and an INTEGER as a number.

This is the package's ./d1 entry. It imports nothing from node: or a
driver, so a Worker bundles it. It exports the enqueue of the queue client
on SQLite (queue-sqlite.ts, D53, amended): enqueueStatement, whose
statement a Worker adds to the batch whose writes caused the message, and
enqueue, which writes a message in an ORM's transaction in the ORM's
dialect (queue-orm.ts, D189). claimsOver and workerQueueOf make a queue's
claims over a SqlDatabase, sqliteQueue's statements, each in the
connection's turn: a generated worker's drain.ts claims with them over its
D1 binding at each firing of its Cron Trigger and at each wake of its
Workflow, as it claims on Postgres through Hyperdrive (D151, #702).

It exports the behaviors' adapter (rules.ts, D103), which imports nothing
from node: or a driver either: a Worker, or a server over openSqlite, runs a
command of a table that composes behaviors on sqliteRulesStore, over the
layout its database's generated orm.ts embeds.

It exports the notifier (D153), the inbound path and parties (D157) and
the agent dispatcher (D158), which run over the repositories and enqueue
in their transactions, on D1 as on Postgres (D189).

It exports the generated repositories over a SqlDatabase (orm-sqlite.ts,
D182), sqliteOrm, which a types package's ./orm/sqlite entry calls, with
references and attachments (D154), the audience and grants each read
keeps to (D156), and the behaviors' writes, operations, deadlines and
changes (D186), whose change set D1 applies in one batch; withRulesCore,
which the entry's createOrm passes the rules package's core through, or a
Worker's own: createOrm(db, { rules: initSync(wasmModule) }). They import node:async_hooks alone of node:, which a
Worker has under the nodejs_als flag, for the caller of the operation being
run.
*/

export type { EnqueueOptions, QueueDefinition, QueueField, QueueStatements } from './queue-core.js';
export { enqueueSqlite, enqueueStatement } from './queue-sqlite.js';
// The generated repositories over a SqlDatabase (D182): a types package's
// ./orm/sqlite entry's createOrm calls sqliteOrm, and the predicates and
// members a read across tables applies on SQLite as on Postgres.
export { SQLITE_ORM_RUNTIME, sqliteOrm, sqliteOrmPool, type TransactionalSqlDatabase } from './orm-sqlite.js';
export { AccessRefusedError, MergeError, NoUserError, NotFoundError, PageKeyError, TooManyRowsError, audienceConditions, ormDialect, ormOfRuntime } from './orm.js';
// Grants (D156), the calendar (D152) and search across tables (D155) over
// the repositories, on SQLite and D1 as on Postgres (D186): none of them
// imports a driver.
export { GRANT_TOKEN_PREFIX, accessLogOf, activeGrants, grantAuthenticator, grantEnded, grantTokenHash, issueGrant, reachesOf, revokeGrant } from './access.js';
export type { AccessLog, AccessLogEntry, ActiveGrantsFilter, GrantAuthenticatorOptions, GrantRow, IssueGrantInput } from './access.js';
export { teamAuthenticator, teamsOf, type TeamAuthenticatorOptions } from './teams.js';
export {
  CALENDAR_FEED,
  CALENDAR_FEED_DAYS,
  calendarFeedOf,
  calendarIcs,
  calendarMcpTool,
  calendarReference,
  calendarRoutes,
  calendarSourcesOf,
  callCalendarTool,
  isCalendarFeed,
  issueCalendarFeed,
  readCalendar,
  revokeCalendarFeed,
  rotateCalendarFeed,
} from './calendar.js';
export type * from './calendar.js';
export {
  MAX_INDEXED_TEXT,
  MAX_SEARCH_WORDS,
  RRF_K,
  RRF_WINDOW,
  callSearchTool,
  indexSearch,
  reindexSearch,
  searchEmbeddingsOf,
  searchMcpTool,
  searchRoutes,
  searchSourcesOf,
  searchTables,
  searchWords,
} from './search-tables.js';
// The query syntax of search across tables (#740).
export { MAX_QUERY_TERMS, ftsQuery, postgresQuery, searchPlan, type SearchPlan, type SearchTerm } from './search-query.js';
export type * from './search-tables.js';
// The platform's features that enqueue in a transaction and drain a queue,
// on SQLite and D1 as on Postgres (D189): the notifier (D153), the inbound
// path and parties (D157) and the agent dispatcher (D158), over the
// repositories, with the enqueue in the ORM's dialect and a worker's
// claims over a SqlDatabase.
export { claimsOver, enqueue, workerQueueOf } from './queue-orm.js';
export type { QueueQueryable, QueueSource } from './queue-orm.js';
export type { WorkerMessage, WorkerQueue } from './worker.js';
// Events, notifications and channels (D153): the notifier that raises a
// table's events in the caller's transaction, routes them by rows, holds
// each recipient to the record's audience, delivers through the channel
// adapters an environment configures, and runs a reply as its sender; and
// password reset over the email channel (D81).
export {
  CODE_ATTEMPTS,
  CODE_TTL_SECONDS,
  DEFAULT_DELIVERY_RETENTION_DAYS,
  DEFAULT_DELIVERY_TIMEOUT_SECONDS,
  HEALTH_TTL_MS,
  INAPP,
  NotifierError,
  createNotifier,
  normaliseAddress,
  passwordReset,
  quietDelaySeconds,
  sameId,
  type AddIdentityInput,
  type ChannelIdentityRow,
  type EscalationStep,
  type NoticeRow,
  type NoticeWork,
  type Notifier,
  type NotifierOptions,
  type PasswordResetOptions,
  type RaiseInput,
  type RaisedEvent,
  type RecipientRef,
  type RecipientSpec,
  type RenderedNotice,
  type ReplyAction,
  type ReplyContext,
  type ReplyInput,
  type ReplyOutcome,
  type Urgency,
} from './notify.js';
// The notifier's routes (D153, D157): each channel slot's relay callbacks and
// the in-app inbox, which a generated server mounts.
export { notifierRoutes, slotSegment, type NotifierRoutesOptions } from './notify-routes.js';
// The notifier's upkeep on a schedule (D153, #702): every adapter's health
// asked on its own, and the deliveries pruned, by the dispatch worker.
export { HEALTH_CHECK_EVERY_MS, notifierUpkeep, type NotifierUpkeepOptions } from './notify-upkeep.js';
// Agent orchestration (D158): the dispatcher of agent runs, their tokens,
// autonomy, budgets and stop switches, over the @agents tables.
export {
  AGENT_PERMISSIONS,
  AgentsError,
  DEFAULT_TRANSCRIPT_RETENTION_DAYS,
  createAgents,
  defaultAutonomy,
  effectiveAutonomy,
  narrowReaches,
  paramsHash,
  trustOf,
  type AgentRunRequest,
  type AgentSandboxTool,
  type AgentTool,
  type AgentToolContext,
  type AgentTrigger,
  type Agents,
  type AgentsOptions,
  type AskThread,
  type Autonomy,
  type GateOutcome,
  type OperationClass,
  type PrincipalRef,
  type RunTrust,
  type TranscriptEntry,
  type TranscriptStore,
} from './agents.js';
// Outside parties and inbound messages (D157): the principal a party is,
// the one path every inbound message takes, conversations whose messages
// are rows, and sending to a party as a permission.
export {
  InboundError,
  createInbound,
  emailAuthenticated,
  type Inbound,
  type InboundDisposition,
  type InboundOptions,
  type InboundOutcome,
  type SettleOnto,
  type MessageRow,
  type ReceivedMessage,
  type RunRequest,
  type SendDecision,
  type SendInput,
  type SendOutcome,
} from './inbound.js';
export { identityLapsed, partyPrincipal, partyTablesOf, type IdentityStanding, type PartyPrincipalOptions, type PartyRef } from './parties.js';
export type * from './orm.js';
export { RowFeatureError, attachmentBlock, previewObjectOf } from './rowfeatures.js';
export type * from './rowfeatures.js';
// The file processor's wiring (D154, D159, #702): what attachments have
// pending, handed to the environment's files slots, the text and preview
// recorded with processed.
export { DEFAULT_PROCESS_LIMIT, DEFAULT_PROCESS_MAX_BYTES, processFiles, type FileProcessingOptions, type FileProcessingRun } from './file-processing.js';
export { FieldEncryptionError, type FieldEncryptionReason } from './fieldcrypt-error.js';
export { uuidFromDb, uuidToDb } from './uuid.js';
export type { FieldKeyring, FieldKeys } from './fieldcrypt.js';
export {
  MAX_ATTEMPTS,
  RulesRefusal,
  busyRefusal,
  execute,
  lazyCore,
  prepareCreate,
  readArmedDeadlines,
  readLinked,
  readQueuedEvents,
  readRow,
  readRows,
  readSubRows,
  runChange,
  runCreate,
  runDeadline,
  runDelete,
  runOperation,
  runTableOperation,
  runUpdate,
  spendEvent,
  sqliteRulesStore,
  VETOED_RETRY_SECONDS,
  withRulesCore,
} from './rules.js';
export type {
  ArmedDeadline,
  ChangeOptions,
  ChangeRun,
  CoreCaller,
  CoreChangeSet,
  CoreCommand,
  CoreDeadline,
  CoreFieldSpec,
  CoreKeyedRow,
  CoreLinkedRows,
  CoreOwned,
  CoreRefusal,
  CoreRelationSpec,
  CoreRow,
  CoreRowChange,
  CoreRowImage,
  CoreSubRow,
  CoreTableRef,
  CoreTypeSpec,
  CoreWriteKind,
  CreateOptions,
  DeadlineClaim,
  DeadlineOptions,
  DeadlineRun,
  EventRun,
  ExecuteOptions,
  QueuedEvent,
  RulesChanges,
  RulesColumn,
  RulesCore,
  RulesDeadlines,
  RulesEvents,
  RulesHost,
  RulesLayout,
  RulesLive,
  RulesOperation,
  RulesOwnedField,
  RulesResult,
  RulesStore,
  RulesSubTable,
  RulesTable,
  RulesUse,
  SqliteRulesStoreOptions,
  TableChange,
} from './rules.js';

/**
 * A value a statement binds: what D1 and SQLite store. A boolean binds as 1
 * or 0, and bytes as a BLOB, which a row reads back as an Array of numbers.
 * bind throws D1_TYPE_ERROR for undefined.
 */
export type SqlValue = string | number | boolean | null | ArrayBuffer | Uint8Array;

/** What a statement's run reports beside its rows, as D1 reports it. */
export interface SqlMeta {
  /** How long the statement took, in milliseconds. */
  readonly duration: number;
  /** The rows the statement inserted, updated or deleted. */
  readonly changes: number;
  /** The rowid of the last row inserted on the connection. */
  readonly last_row_id: number;
  /** Whether the statement changed the database. */
  readonly changed_db: boolean;
  /** Further members D1 reports: rows_read, rows_written, size_after, served_by. */
  readonly [member: string]: unknown;
}

/** The rows a statement answered, and what its run reports. */
export interface SqlResult<T = Record<string, unknown>> {
  readonly results: T[];
  readonly success: true;
  readonly meta: SqlMeta;
}

/** What exec reports: the statements it ran, and how long they took. */
export interface SqlExecResult {
  readonly count: number;
  readonly duration: number;
}

/** A prepared statement: bind returns the statement with its values bound. */
export interface SqlPreparedStatement {
  /** The statement with values bound to its placeholders, in order: ? takes the next, ?N the Nth. Throws D1_TYPE_ERROR for undefined. */
  bind(...values: SqlValue[]): SqlPreparedStatement;
  /** The first row, or null when there is none. */
  first<T = Record<string, unknown>>(): Promise<T | null>;
  /** The named column of the first row, or null when there is no row. Rejects for a column the row lacks. */
  first<T = unknown>(column: string): Promise<T | null>;
  /** Every row, with what the run reports. */
  all<T = Record<string, unknown>>(): Promise<SqlResult<T>>;
  /** Runs the statement: its rows, which a statement without RETURNING has none of, and what the run reports. */
  run<T = Record<string, unknown>>(): Promise<SqlResult<T>>;
  /** Every row as an array of its columns' values. */
  raw<T = unknown[]>(options?: { readonly columnNames?: false }): Promise<T[]>;
  /** The column names, then every row as an array of its columns' values. */
  raw<T = unknown[]>(options: { readonly columnNames: true }): Promise<[string[], ...T[]]>;
}

/** D1's interface: what Deps.db is for a database whose DB service lists sqlite first. */
export interface SqlDatabase {
  /** A statement over sql, whose placeholders bind later. */
  prepare(sql: string): SqlPreparedStatement;
  /** Runs statements in order in one transaction: every one, or none when one fails. */
  batch<T = Record<string, unknown>>(statements: readonly SqlPreparedStatement[]): Promise<SqlResult<T>[]>;
  /** Runs a script's statements, one per line, with no values bound. */
  exec(sql: string): Promise<SqlExecResult>;
}

/** How long a ping waits for its answer, in milliseconds: what /readyz allows a database. */
export const PING_TIMEOUT_MS = 2000;

/**
 * Resolves when db answers SELECT 1 within timeoutMs, and rejects
 * otherwise: what a server's /readyz asks of each SqlDatabase, as it asks
 * each pg Pool (./postgres's ping).
 */
export async function ping(db: SqlDatabase, timeoutMs: number = PING_TIMEOUT_MS): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`the database did not answer within ${timeoutMs} ms`)), timeoutMs);
  });
  try {
    const one = await Promise.race([db.prepare('SELECT 1 AS one').first<number>('one'), timeout]);
    if (one !== 1) throw new Error(`the database answered SELECT 1 with ${JSON.stringify(one)}`);
  } finally {
    clearTimeout(timer);
  }
}

/** Whether value has the methods of a SqlDatabase: what a D1 binding is. */
export function isSqlDatabase(value: unknown): value is SqlDatabase {
  if (typeof value !== 'object' || value === null) return false;
  const db = value as Partial<Record<keyof SqlDatabase, unknown>>;
  return typeof db.prepare === 'function' && typeof db.batch === 'function' && typeof db.exec === 'function';
}
