import type { SqlDatabase } from './d1.js';
import { ormDialect } from './orm.js';
import { inSqliteTurn } from './orm-sqlite.js';
import {
  canonicalUUID,
  conflictOf,
  delayOf,
  errorMessage,
  unset,
  type EnqueueOptions,
  type Queue,
  type QueueDefinition,
  type QueueField,
} from './queue-core.js';
import { sqliteEnqueueOf, sqliteQueue } from './queue-sqlite.js';
import { wakeAfterEnqueue } from './queue-wake.js';
import { intervalText, pointText } from './pgtext.js';
import type { WorkerMessage, WorkerQueue } from './worker.js';

/*
A queue's enqueue in the caller's transaction and a worker's claims, in the
dialect of the database they run on (D53, D80, D189). It imports no driver,
so the ./d1 and ./sqlite entries export it beside ./postgres, and the
platform's features that enqueue in a transaction and drain a queue (the
notifier, D153; the inbound path, D157; the agent dispatcher, D158) run on
Postgres, SQLite and D1 alike.

enqueue writes a message in the transaction client runs, the caller's. On
an ORM or a transaction that sqliteOrm opened (ormDialect 'sqlite'), it
writes the queue's statement on SQLite (queue-sqlite.ts's sqliteEnqueueOf)
on the transaction's connection: on openSqlite's file it commits with the
transaction, and on D1, which holds no transaction open, it is committed as
it runs, as each of the transaction's statements is (D182). On anything
else it writes the statement on Postgres on a pg client: a pool, a
connection, or an ORM over one, whose query takes a pg query config. Either
way the enqueue then sends the queue's wake (D145, D151), a hint that never
fails it.

claimsOver is the claims of a queue a worker makes over what it is given:
a SqlDatabase (a D1 binding, or openSqlite's connection), whose claims are
sqliteQueue's, each taking the connection's turn with the repositories'; a queue's claims already made; or a pg pool, whose claims
are postgresQueue's, which ./postgres's queue client serves it once it is
loaded (servePostgresClaims), since it alone imports pg. workerQueueOf
makes a worker's queue of the claims, each message handed to a handler.
*/

/** The query config the queue client passes pg. */
export interface QueueQueryConfig {
  readonly text: string;
  readonly values: unknown[];
  readonly rowMode?: 'array';
  readonly types?: { getTypeParser(oid: number, format?: 'text' | 'binary'): unknown };
}

/**
 * What the queue client needs of a pg Pool, PoolClient or Client, or of an
 * ORM or a transaction: an enqueue takes the client of the caller's
 * transaction, and the claims a pool.
 */
export interface QueueQueryable {
  query(config: QueueQueryConfig): Promise<{ rows: unknown[]; rowCount: number | null }>;
}

/**
 * What a queue's claims need of a pg Pool: its queries, and, for a worker
 * in drain mode, a connection of its own to hold its slot's lock on (D145),
 * which a Pool's connect gives. connect is typed loosely so that every pg
 * Pool, and a test's stand-in with a query alone, is one; slot checks what
 * it resolves to.
 */
export interface QueuePool extends QueueQueryable {
  connect?(): Promise<unknown>;
}

/**
 * What a worker's queue is claimed over: a pg pool (postgresQueue's
 * claims), a SqlDatabase (sqliteQueue's), or a queue's claims already made.
 */
export type QueueSource = QueuePool | SqlDatabase | Queue<unknown>;

/**
 * Writes message into queue in the transaction client runs, the caller's:
 * the message is due once the transaction commits, and never if it rolls
 * back (on D1, which commits each statement as it runs, once the statement
 * has run). Each member the message sets writes its column, a JSON column
 * its JSON text; one it leaves unset takes the column's default, and an
 * enum with a schema default writes that default, as the Go ORM writes
 * them. With options.delaySeconds the message is due that long after the
 * database's clock, and no worker claims it before (D80). The statement is
 * SQLite's on an ORM or a transaction of sqliteOrm's, and Postgres's on
 * anything else.
 */
export async function enqueue<M>(
  client: QueueQueryable,
  queue: QueueDefinition<M>,
  message: M,
  options?: EnqueueOptions,
): Promise<void> {
  const { text, values, delay } = ormDialect(client) === 'sqlite' ? sqliteEnqueueOf(queue, message, options) : postgresEnqueueOf(queue, message, options);
  try {
    await client.query({ text, values });
  } catch (error) {
    throw new Error(`enqueue on ${queue.table}: ${errorMessage(error)}`, { cause: error });
  }
  // Where the queue's worker runs in drain mode and its platform gives a
  // wake, the enqueue starts it (D145), and where it drains at a Cron
  // Trigger's firings, a message due before the next firing starts a run
  // that waits for it (D151): sent once the statement has run, awaited by
  // nothing, and never a failure of the enqueue. The due time is this
  // process's clock plus the delay, near the database's.
  wakeAfterEnqueue(queue.name, delay > 0 ? Date.now() + delay * 1000 : undefined);
}

/** The text and values of the statement that writes message into queue on Postgres, $1 the delay's seconds when it has one. */
function postgresEnqueueOf<M>(queue: QueueDefinition<M>, message: M, options?: EnqueueOptions): { readonly text: string; readonly values: unknown[]; readonly delay: number } {
  const delay = delayOf(queue.table, queue.statements, options);
  const columns: string[] = [];
  const placeholders: string[] = [];
  const values: unknown[] = [];
  if (delay > 0) {
    // due_at goes first: the statement numbers the delay's seconds $1.
    columns.push('due_at');
    placeholders.push(queue.statements.dueIn ?? '');
    values.push(delay);
  }
  const members = message as unknown as Readonly<Record<string, unknown>>;
  for (const field of queue.fields) {
    const value = members[field.name];
    if (unset(value, field)) {
      if (field.default === undefined) continue;
      columns.push(field.column);
      values.push(field.default);
      placeholders.push(`$${values.length}`);
      continue;
    }
    columns.push(field.column);
    try {
      values.push(columnValue(value, field));
    } catch (error) {
      throw new Error(`enqueue on ${queue.table}: field ${field.name}: ${errorMessage(error)}`, { cause: error });
    }
    placeholders.push(`$${values.length}`);
  }
  const text =
    columns.length === 0
      ? `${queue.statements.insert} DEFAULT VALUES`
      : `${queue.statements.insert} (${columns.join(', ')}) VALUES (${placeholders.join(', ')})${conflictOf(queue.statements)}`;
  return { text, values, delay };
}

/** What a member a message sets writes to its column on Postgres: its JSON text, a location's point, a duration's interval, or itself. */
function columnValue(value: unknown, field: QueueField): unknown {
  if (field.json) return JSON.stringify(value);
  if (field.point) return pointText(value);
  if (field.interval) return intervalText(value);
  // A UUID, hyphenated or base62, goes hyphenated, as Go and Rust send it.
  if (field.uuid) return field.list && Array.isArray(value) ? value.map(canonicalUUID) : canonicalUUID(value);
  return value;
}

/** The claims of a queue over a pg pool, which ./postgres's queue client serves. */
type PostgresClaims = <M>(pool: QueuePool, queue: QueueDefinition<M>) => Queue<M>;

let postgresClaims: PostgresClaims | undefined;

/** Serves claimsOver the claims of a queue over a pg pool: ./postgres's queue client (queue.ts) calls it as it loads. */
export function servePostgresClaims(claims: PostgresClaims): void {
  postgresClaims = claims;
}

/** Whether source is a queue's claims already made. */
function isQueue(source: QueueSource): source is Queue<unknown> {
  return typeof (source as Partial<Queue<unknown>>).claim === 'function' && typeof (source as Partial<Queue<unknown>>).complete === 'function';
}

/** Whether source is a SqlDatabase: a D1 binding, or openSqlite's connection. */
function isSqlDatabase(source: QueueSource): source is SqlDatabase {
  return typeof (source as Partial<SqlDatabase>).prepare === 'function' && typeof (source as Partial<SqlDatabase>).batch === 'function';
}

/**
 * The claims of queue a worker makes over source: sqliteQueue's over a
 * SqlDatabase, source itself when it is a queue's claims, and
 * postgresQueue's over a pg pool, which needs ./postgres loaded.
 */
export function claimsOver<M>(source: QueueSource, queue: QueueDefinition<M>): Queue<M> {
  if (isQueue(source)) return source as Queue<M>;
  if (isSqlDatabase(source)) return inTurns(source, sqliteQueue(source, queue));
  if (postgresClaims === undefined) {
    throw new Error(`claim ${queue.table}: the claims over a pg pool are ./postgres's queue client's; import @superschematic/http-runtime/postgres, or pass a SqlDatabase or a queue's claims`);
  }
  return postgresClaims(source, queue);
}

/**
 * claims, each of whose statements takes db's turn with the repositories'
 * (inSqliteTurn): a SqlDatabase is one connection, and a claim made while a
 * handler's transaction is open on it would otherwise run inside it.
 */
function inTurns<M>(db: SqlDatabase, claims: Queue<M>): Queue<M> {
  return {
    name: claims.name,
    leaseSeconds: claims.leaseSeconds,
    claim: (n, leaseSeconds) => inSqliteTurn(db, () => claims.claim(n, leaseSeconds)),
    complete: (id, token) => inSqliteTurn(db, () => claims.complete(id, token)),
    fail: (id, token, cause) => inSqliteTurn(db, () => claims.fail(id, token, cause)),
    extend: (id, token, leaseSeconds) => inSqliteTurn(db, () => claims.extend(id, token, leaseSeconds)),
    release: (id, token) => inSqliteTurn(db, () => claims.release(id, token)),
    prune: limit => inSqliteTurn(db, () => claims.prune(limit)),
  };
}

/**
 * A worker's queue of claims, each claimed message handed to handle, and
 * prune, when given, run beside the queue's own (a feature's retention of
 * its rows, the dispatch queue's deliveries). A queue on SQLite has no
 * slots and no next due time (D145), so neither has the worker's queue.
 */
export function workerQueueOf<M>(claims: Queue<M>, handle: (message: M) => Promise<void>, prune?: (limit: number) => Promise<number>): WorkerQueue {
  return {
    name: claims.name,
    leaseSeconds: claims.leaseSeconds,
    claim: async (n, leaseSeconds) =>
      (await claims.claim(n, leaseSeconds)).map((claim): WorkerMessage => ({ id: claim.id, token: claim.token, attempt: claim.attempt, handle: () => handle(claim.message) })),
    extend: (id, token, leaseSeconds) => claims.extend(id, token, leaseSeconds),
    complete: (id, token) => claims.complete(id, token),
    fail: (id, token, cause) => claims.fail(id, token, cause),
    release: (id, token) => claims.release(id, token),
    prune: prune ? async limit => (await claims.prune(limit)) + (await prune(limit)) : limit => claims.prune(limit),
    ...(claims.slot ? { slot: (slots: number) => claims.slot!(slots) } : {}),
    ...(claims.nextDue ? { nextDue: () => claims.nextDue!() } : {}),
  };
}
