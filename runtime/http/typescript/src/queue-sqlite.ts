import type { SqlDatabase, SqlPreparedStatement, SqlValue } from './d1.js';
import {
  canonicalUUID,
  ClaimLostError,
  conflictOf,
  errorMessage,
  UNREADABLE,
  formatDuration,
  parseDuration,
  unset,
  type Queue,
  type QueueClaim,
  type QueueDefinition,
  type QueueField,
  type QueueStatements,
} from './queue-core.js';

/*
The queue client of a TypeScript API on SQLite (D53, amended; section 8.8
of docs/stack-model.md): the twin of queue.ts for a database whose DB
service lists the sqlite dialect, over a SqlDatabase, the interface D1 and
openSqlite share. It runs the statements on SQLite a queue's definition
carries, which the Go ORM's queues_sqlite.go and the Rust client run too,
so a message one language enqueues another claims alike. It imports
nothing but types, so the ./sqlite entry exports it over bun:sqlite, and the
./d1 entry its enqueue alone, since no worker claims on D1 (D53, amended).

A SqlDatabase has no interactive transaction, so enqueueStatement returns
the statement that writes a message, which the caller adds to the batch
whose writes caused it: the batch commits them together, or none of them.
sqliteQueue claims, completes, fails, extends, releases and prunes messages
for a worker. SQLite has one writer at a time, so each is one statement,
which holds the write lock from its first read: a claim is one UPDATE ...
RETURNING of the due messages, oldest first, and two workers that claim at
once take different messages. The lease, the attempts, the backoff, the
dead messages and the retention are Postgres's. Times are the database's
clock, in RFC 3339 in UTC to the millisecond. A claim first reads whether a
message is due, so an idle worker's poll takes no write lock; and a row it
took but cannot read, say a JSON column holding no JSON, it fails alone,
recording UNREADABLE and why, and hands the rest to their handlers.

A field crosses as the definition marks it. A member a message leaves
unset takes its column's default, and an enum with a schema default writes
that default. A JSON value is its JSON text; a list a JSON array; a UUID its
lowercase hyphenated text, from its base62 or hyphenated form as Go and Rust
parse it; a timestamp, a Date or its text, RFC 3339 in UTC,
read back as a Date, as the Postgres client reads it; a boolean 1 or 0, read
back as true or false (true or false in a list); a duration its canonical
text. SQLite has no storage for a location, so no definition on SQLite holds
one.
*/

/** The statements on SQLite of queue, which a database that lists the sqlite dialect carries. */
function sqliteStatementsOf<M>(queue: QueueDefinition<M>): QueueStatements {
  if (queue.sqliteStatements === undefined) {
    throw new Error(
      `queue ${queue.name} carries no statements on SQLite: its DB service lists no sqlite dialect, or its message holds a field SQLite's queue client cannot hold; see its types package's queues.ts`
    );
  }
  return queue.sqliteStatements;
}

/**
 * The statement that writes message into queue on db, which the caller adds
 * to the batch whose writes caused the message, so it commits with them, or
 * runs alone. Each member the message sets writes its column; one it leaves
 * unset takes the column's default, and an enum with a schema default
 * writes that default, as the Go ORM writes them.
 */
export function enqueueStatement<M>(db: SqlDatabase, queue: QueueDefinition<M>, message: M): SqlPreparedStatement {
  const statements = sqliteStatementsOf(queue);
  const columns: string[] = [];
  const values: SqlValue[] = [];
  const members = message as unknown as Readonly<Record<string, unknown>>;
  for (const field of queue.fields) {
    const value = members[field.name];
    if (unset(value, field)) {
      if (field.default === undefined) continue;
      columns.push(field.column);
      values.push(field.default);
      continue;
    }
    columns.push(field.column);
    try {
      values.push(sqliteColumnValue(value, field));
    } catch (error) {
      throw new Error(`enqueue on ${queue.table}: field ${field.name}: ${errorMessage(error)}`, { cause: error });
    }
  }
  const text =
    columns.length === 0
      ? `${statements.insert} DEFAULT VALUES`
      : `${statements.insert} (${columns.join(', ')}) VALUES (${columns.map((_, i) => `?${i + 1}`).join(', ')})${conflictOf(statements)}`;
  return db.prepare(text).bind(...values);
}

/** Writes message into queue on db, in a statement of its own: enqueueStatement's statement, run alone. */
export async function enqueueSqlite<M>(db: SqlDatabase, queue: QueueDefinition<M>, message: M): Promise<void> {
  const statement = enqueueStatement(db, queue, message);
  try {
    await statement.run();
  } catch (error) {
    throw new Error(`enqueue on ${queue.table}: ${errorMessage(error)}`, { cause: error });
  }
}

/** The claims of queue over db, for a worker, as postgresQueue's over a pool. */
export function sqliteQueue<M>(db: SqlDatabase, queue: QueueDefinition<M>): Queue<M> {
  const statements = sqliteStatementsOf(queue);
  const update = async (what: string, statement: string, id: string, token: string, extra: SqlValue[] = []): Promise<void> => {
    let changes: number;
    try {
      ({
        meta: { changes },
      } = await db
        .prepare(statement)
        .bind(id, token, ...extra)
        .run());
    } catch (error) {
      throw new Error(`${what} message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
    }
    if (!changes) throw new ClaimLostError(`${what} message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
  };
  const claims: Queue<M> = {
    name: queue.name,
    leaseSeconds: queue.leaseSeconds,
    async claim(n, leaseSeconds = queue.leaseSeconds) {
      if (n <= 0) return [];
      let rows: unknown[][];
      try {
        // A read first: an idle poll takes no write lock.
        if (statements.due !== undefined && (await db.prepare(statements.due).first()) === null) return [];
        await db.prepare(statements.expire).bind(queue.retries).run();
        rows = await db.prepare(statements.claim).bind(n, leaseSeconds).raw();
      } catch (error) {
        throw new Error(`claim on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      // Each row is claimed already: one that does not read fails alone.
      const out: QueueClaim<M>[] = [];
      for (const row of rows) {
        try {
          out.push(sqliteClaimOf(queue, row));
        } catch (error) {
          await claims.fail(String(row[0]), String(row[1]), `${UNREADABLE}: ${errorMessage(error)}`).catch(() => undefined);
        }
      }
      return out;
    },
    complete: (id, token) => update('complete', statements.complete, id, token),
    async fail(id, token, cause) {
      let rows: { state?: unknown }[];
      try {
        ({ results: rows } = await db
          .prepare(statements.fail)
          .bind(id, token, queue.retries, queue.backoffSeconds, cause, queue.maxBackoffSeconds)
          .all<{ state?: unknown }>());
      } catch (error) {
        throw new Error(`fail message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      const row = rows[0];
      if (row === undefined) throw new ClaimLostError(`fail message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
      return row.state === 'dead';
    },
    extend: (id, token, leaseSeconds = queue.leaseSeconds) => update('extend', statements.extend, id, token, [leaseSeconds]),
    release: (id, token) => update('release', statements.release, id, token),
    async prune(limit) {
      if (limit <= 0) return 0;
      try {
        const { meta } = await db.prepare(statements.prune).bind(queue.retainSeconds, limit).run();
        return meta.changes;
      } catch (error) {
        throw new Error(`prune ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
    },
  };
  return claims;
}

/** What a member a message sets writes to its column on SQLite. */
export function sqliteColumnValue(value: unknown, field: QueueField): SqlValue {
  if (field.json) return JSON.stringify(value);
  if (field.list) {
    if (!Array.isArray(value)) throw new Error(`${JSON.stringify(value)} is no list`);
    return JSON.stringify(value.map((element, i) => sqliteElement(element, field, `element ${i}: `)));
  }
  if (field.point) throw new Error('SQLite has no storage for a location');
  const element = sqliteElement(value, field, '');
  if (typeof element === 'boolean') return element ? 1 : 0;
  if (typeof element === 'bigint') {
    // A number holds an integer exactly up to 2^53; a SqlDatabase binds no bigint.
    if (element > BigInt(Number.MAX_SAFE_INTEGER) || element < BigInt(Number.MIN_SAFE_INTEGER)) {
      throw new Error(`${element} is out of the range a SQLite binding holds exactly, ±(2^53 - 1)`);
    }
    return Number(element);
  }
  if (element instanceof Uint8Array || typeof element === 'string' || typeof element === 'number' || element === null) return element;
  throw new Error(`${JSON.stringify(element)} is no value a SQLite column holds`);
}

/** A value, or an element of a list, as SQLite holds it: a UUID's lowercase text, a timestamp in UTC, a duration's canonical text, or itself. */
function sqliteElement(value: unknown, field: QueueField, where: string): unknown {
  try {
    if (field.uuid) return canonicalUUID(value);
    if (field.timestamp && (typeof value === 'string' || value instanceof Date)) return utcTimestamp(value);
    if (field.interval && typeof value === 'string') return formatDuration(parseDuration(value));
    if (field.boolean && typeof value !== 'boolean') throw new Error(`${JSON.stringify(value)} is no boolean`);
  } catch (error) {
    throw new Error(`${where}${errorMessage(error)}`, { cause: error });
  }
  return value;
}

/** RFC 3339 text: a date, a time with its fraction of a second, and its offset. */
const RFC3339 = /^(\d{4}-\d{2}-\d{2})[Tt ](\d{2}:\d{2}:\d{2})(\.\d+)?([Zz]|[+-]\d{2}:?\d{2})$/u;

/**
 * A timestamp, a Date or its RFC 3339 text, as RFC 3339 in UTC: a Date's
 * ISO text, or the text moved to UTC with its fraction kept, so a
 * timestamp written to the microsecond reads back to the microsecond where
 * the reader's type holds it.
 */
export function utcTimestamp(value: string | Date): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new Error('the Date is invalid');
    return value.toISOString();
  }
  const match = RFC3339.exec(value);
  if (match === null) throw new Error(`${JSON.stringify(value)} is no RFC 3339 timestamp`);
  const [, date, time, fraction = '', offset = 'Z'] = match;
  // An offset without its colon (+0200) is RFC 3339's with one.
  const zone = offset.toUpperCase() === 'Z' ? 'Z' : `${offset.slice(0, 3)}:${offset.slice(-2)}`;
  const whole = Date.parse(`${date}T${time}${zone}`);
  if (Number.isNaN(whole)) throw new Error(`${JSON.stringify(value)} is no RFC 3339 timestamp`);
  return `${new Date(whole).toISOString().slice(0, 19)}${fraction}Z`;
}

/** A claimed row, [id, token, attempt, ...the message's columns], as its claim. */
function sqliteClaimOf<M>(queue: QueueDefinition<M>, row: readonly unknown[]): QueueClaim<M> {
  const [id, token, attempt] = row;
  const message: Record<string, unknown> = {};
  queue.fields.forEach((field, i) => {
    const column = row[3 + i];
    if (column === null || column === undefined) return;
    try {
      message[field.name] = sqliteMemberValue(column, field);
    } catch (error) {
      throw new Error(`message ${String(id)} of ${queue.table}: field ${field.name}: ${errorMessage(error)}`, { cause: error });
    }
  });
  return { id: String(id), token: String(token), attempt: Number(attempt), message: message as M };
}

/** A column's value as the message's member: the JSON a JSON column or a list holds, a boolean, a timestamp's Date, a duration's canonical text, or the value. */
export function sqliteMemberValue(column: unknown, field: QueueField): unknown {
  if (field.json || field.list) {
    if (typeof column !== 'string') throw new Error(`the column holds ${JSON.stringify(column)}, no JSON text`);
    const value: unknown = JSON.parse(column);
    if (field.list && field.timestamp && Array.isArray(value)) {
      return value.map(element => (typeof element === 'string' ? new Date(element) : element));
    }
    return value;
  }
  if (field.boolean) return column !== 0 && column !== '0' && column !== false;
  if (field.timestamp && typeof column === 'string') return new Date(column);
  if (field.interval && typeof column === 'string') return formatDuration(parseDuration(column));
  if (typeof column === 'bigint') {
    if (column > BigInt(Number.MAX_SAFE_INTEGER) || column < BigInt(Number.MIN_SAFE_INTEGER)) {
      throw new Error(`${column} is out of the range a number holds exactly, ±(2^53 - 1)`);
    }
    return Number(column);
  }
  return column;
}
