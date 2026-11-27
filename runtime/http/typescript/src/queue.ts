import pg from 'pg';
import {
  ClaimLostError,
  errorMessage,
  UNREADABLE,
  formatDuration,
  jsonMemberValue,
  messageUUID,
  parseDuration,
  type PostgresQueue,
  type QueueClaim,
  type QueueDefinition,
} from './queue-core.js';
import { servePostgresClaims, type QueuePool, type QueueQueryable } from './queue-orm.js';
import { parseArray, parseInterval, parsePoint } from './pgtext.js';

export { ClaimLostError, formatDuration, parseDuration } from './queue-core.js';
export { intervalText, parseArray, parseInterval, parsePoint, pointText } from './pgtext.js';
export { parseQueueWakes, QUEUE_WAKES_VARIABLE, queueWaker, queueWakesSettled, setProcessQueueWaker, WAKE_INTERVAL_MS, wakeRunId } from './queue-wake.js';
export type { QueueWake, QueueWakeBinding, QueueWakeHost, QueueWaker } from './queue-wake.js';
export type { EnqueueOptions, PostgresQueue, Queue, QueueClaim, QueueDefinition, QueueField, QueueStatements } from './queue-core.js';
// The enqueue in the caller's transaction, in its database's dialect, and
// a worker's claims over what it is given (D189), which import no driver.
export { claimsOver, enqueue, workerQueueOf } from './queue-orm.js';
export type { QueuePool, QueueQueryable, QueueQueryConfig, QueueSource } from './queue-orm.js';

/*
The queue client of a TypeScript API (D53, section 8.8 of
docs/stack-model.md), the TypeScript twin of the Go ORM's queues.go. There
is no TypeScript ORM, so a DB service's TypeScript types package carries
each of its queues as data, in its ./queues entry: the table, the
arguments, the column each field of the message writes, and the statements
on Postgres, which the generator writes from the one source the Go ORM's
come from. This module runs them over pg, and is part of the package's
./postgres entry, so the main entry never imports pg.

enqueue writes a message in the caller's transaction, so it commits with
the writes that caused it, or not at all. postgresQueue claims, completes,
fails, extends, releases and prunes messages for a worker: a claim takes up
to n messages that are ready and due, or claimed past their claim's expiry,
with FOR UPDATE SKIP LOCKED, gives each a new claim token and one more
attempt, and first marks dead each message whose last attempt's claim
expired. Each change but the claim and the prune refuses a token that no
longer claims the message with ClaimLostError. A failed message waits the
backoff, doubled for each attempt before it and capped at the maximum
backoff (D68), and a prune deletes a batch of the messages done for the
queue's retention, never a dead one. Delivery is at least once, and times
are the database's clock.

A claimed row's values reach the message as the TypeScript types hold
them: a timestamp a Date, a date its YYYY-MM-DD text, a 64-bit integer a
number, a JSON column the value it holds, a list an array, a POINT a
location, {lat, lon}, and an INTERVAL a duration's text in the scalar
core's canonical form (1h30m0s, 1.5s, 250us).

A location crosses as Postgres's (x,y), x the longitude and y the
latitude, and a duration as its whole microseconds, written
[-]H:MM:SS.ffffff, so the sign holds for the whole duration and a
nanosecond remainder is truncated toward zero, as the Go ORM's
time.Duration crosses through pgx. An interval with days or months reads
as their hours, a day 24 and a month 30 days, as pgx reads it.
*/

/** A connection a pool gave: release(true) ends it, and with it the session and its locks. */
interface QueueSession extends QueueQueryable {
  release(destroy?: boolean): void;
}

/**
 * Whether error is the unique violation of a queue's index of claimed or of ready messages by key (D80): a claim
 * on a queue that holds one claim per key meets it when another worker's claim of the key commits first, and a
 * fail or a release on a queue that keeps one ready message per key when a concurrent enqueue of the key does.
 * The claim then takes nothing, and the fail or the release runs once more, when the ready message it sees takes
 * its place, as the Go ORM's and the Rust client's do.
 */
function keyHeld(error: unknown): boolean {
  return (error as { code?: unknown } | null)?.code === '23505';
}

/** Runs query once more when it meets a key another write holds (keyHeld). */
async function onceMoreOnKeyHeld<T>(query: () => Promise<T>): Promise<T> {
  try {
    return await query();
  } catch (error) {
    if (!keyHeld(error)) throw error;
    return query();
  }
}

/** The claims of queue over pool, for a worker. */
export function postgresQueue<M>(pool: QueuePool, queue: QueueDefinition<M>): PostgresQueue<M> {
  const update = async (what: string, statement: string, id: string, token: string, extra: unknown[] = []): Promise<void> => {
    let result: { rowCount: number | null };
    try {
      result = await onceMoreOnKeyHeld(() => pool.query({ text: statement, values: [id, token, ...extra] }));
    } catch (error) {
      throw new Error(`${what} message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
    }
    if (!result.rowCount) throw new ClaimLostError(`${what} message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
  };
  const claims: PostgresQueue<M> = {
    name: queue.name,
    leaseSeconds: queue.leaseSeconds,
    async claim(n, leaseSeconds = queue.leaseSeconds) {
      if (n <= 0) return [];
      let rows: unknown[];
      try {
        await pool.query({ text: queue.statements.expire, values: [queue.retries] });
        ({ rows } = await pool.query({ text: queue.statements.claim, values: [n, leaseSeconds], rowMode: 'array', types: queueTypes }));
      } catch (error) {
        if (keyHeld(error)) return [];
        throw new Error(`claim on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      // Each row is claimed already: one that does not read fails alone.
      const out: QueueClaim<M>[] = [];
      for (const row of rows as (readonly unknown[])[]) {
        try {
          out.push(claimOf(queue, row));
        } catch (error) {
          await claims.fail(String(row[0]), String(row[1]), `${UNREADABLE}: ${errorMessage(error)}`).catch(() => undefined);
        }
      }
      return out;
    },
    complete: (id, token) => update('complete', queue.statements.complete, id, token),
    async fail(id, token, cause) {
      let rows: unknown[];
      try {
        ({ rows } = await onceMoreOnKeyHeld(() =>
          pool.query({
            text: queue.statements.fail,
            values: [id, token, queue.retries, queue.backoffSeconds, cause, queue.maxBackoffSeconds],
          })
        ));
      } catch (error) {
        throw new Error(`fail message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      const row = rows[0] as { state?: unknown } | undefined;
      if (row === undefined) throw new ClaimLostError(`fail message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
      return row.state === 'dead';
    },
    extend: (id, token, leaseSeconds = queue.leaseSeconds) => update('extend', queue.statements.extend, id, token, [leaseSeconds]),
    release: (id, token) => update('release', queue.statements.release, id, token),
    async prune(limit) {
      if (limit <= 0) return 0;
      try {
        const { rowCount } = await pool.query({ text: queue.statements.prune, values: [queue.retainSeconds, limit] });
        return rowCount ?? 0;
      } catch (error) {
        throw new Error(`prune ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
    },
    async slot(slots) {
      const statement = queue.statements.slot;
      if (statement === undefined) {
        throw new Error(`take a slot of ${queue.table}: the queue's definition holds no slot statement; build its DB service again to write one`);
      }
      const none = `take a slot of ${queue.table}: the queue's client gives no connection of its own to hold the slot's lock on; give postgresQueue a pool`;
      if (pool.connect === undefined) throw new Error(none);
      // The lock is the session's, so the slot keeps one connection, and
      // giving it back ends that connection: a worker that dies gives its
      // slot back as its connection ends.
      const connected = (await pool.connect()) as Partial<QueueSession> | null | undefined;
      if (typeof connected?.query !== 'function' || typeof connected.release !== 'function') throw new Error(none);
      const session = connected as QueueSession;
      try {
        for (let slot = 1; slot <= slots; slot++) {
          const { rows } = await session.query({ text: statement, values: [slot], rowMode: 'array' });
          const held = (rows[0] as unknown[] | undefined)?.[0];
          if (held === true || held === 't') {
            return () => {
              session.release(true);
              return Promise.resolve();
            };
          }
        }
      } catch (error) {
        session.release(true);
        throw new Error(`take a slot of ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      session.release();
      return undefined;
    },
    async nextDue() {
      // A definition written before the statement reads no due time, so a
      // worker in drain mode stops once idle, as it did.
      const statement = queue.statements.nextDue;
      if (statement === undefined) return undefined;
      try {
        const { rows } = await pool.query({ text: statement, values: [], rowMode: 'array' });
        const seconds = (rows[0] as unknown[] | undefined)?.[0];
        if (seconds === null || seconds === undefined) return undefined;
        const n = Number(seconds);
        return Number.isFinite(n) ? n : undefined;
      } catch (error) {
        throw new Error(`read when the next message of ${queue.table} is due: ${errorMessage(error)}`, { cause: error });
      }
    },
  };
  return claims;
}

/** A claimed row, [id, token, attempt, ...the message's columns], as its claim. */
function claimOf<M>(queue: QueueDefinition<M>, row: readonly unknown[]): QueueClaim<M> {
  const [id, token, attempt] = row;
  const message: Record<string, unknown> = {};
  queue.fields.forEach((field, i) => {
    let value = row[3 + i];
    if (value === null || value === undefined) return;
    if (field.json) {
      // By the column's type, not the value's: queueTypes leaves a json or jsonb column's text unparsed, so its
      // JSON is decoded here once, a JSON string among it.
      try {
        value = jsonMemberValue(value, field);
      } catch (error) {
        throw new Error(`message ${String(id)} of ${queue.table}: field ${field.name}: ${errorMessage(error)}`, { cause: error });
      }
    } else if (field.list && typeof value === 'string') {
      // A list of a type pg does not know, such as citext[], arrives as its text.
      value = parseArray(value);
    }
    // A UUID column's hyphenated text is handed on as base62, the form the
    // TypeScript types hold a UUID in everywhere else.
    if (field.uuid && !field.json) value = messageUUID(value, field.list);
    message[field.name] = value;
  });
  return { id: String(id), token: String(token), attempt: Number(attempt), message: message as M };
}

/**
 * The type parsers of a claim: pg's, but for the types whose TypeScript
 * type is not what pg makes of them. A 64-bit integer is a number, as the
 * TypeScript types hold Generic.Int64 and every integer scalar, a date
 * its YYYY-MM-DD text, Temporal.Date's, where pg would make a Date at local
 * midnight, a POINT the location {lat, lon}, Geo.Location's, where pg would
 * make {x, y}, an INTERVAL the canonical text of its duration,
 * Temporal.Duration's, where pg would make an object of its parts, and a
 * json or jsonb column its text, which claimOf decodes once by the field.
 */
const overrides: Readonly<Record<number, (value: string) => unknown>> = {
  20: value => Number(value),
  // pg would decode json and jsonb itself, and a JSON string, "1", would then be decoded twice.
  114: value => value,
  3802: value => value,
  600: value => parsePoint(value),
  1016: value => parseArray(value).map(element => (element === null ? null : Number(element))),
  1082: value => value,
  1182: value => parseArray(value),
  1186: value => formatDuration(parseInterval(value)),
};

const queueTypes = {
  getTypeParser(oid: number, format?: 'text' | 'binary'): unknown {
    const override = (format ?? 'text') === 'text' ? overrides[oid] : undefined;
    return override ?? pg.types.getTypeParser(oid, format);
  },
};

// A worker's claims over a pg pool are this client's (queue-orm.ts's
// claimsOver), which the notifier, the inbound path and the agent
// dispatcher make without importing pg themselves.
servePostgresClaims(postgresQueue);
