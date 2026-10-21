import pg from 'pg';
import {
  ClaimLostError,
  errorMessage,
  formatDuration,
  parseDuration,
  unset,
  type PostgresQueue,
  type QueueClaim,
  type QueueDefinition,
  type QueueField,
} from './queue-core.js';

export { ClaimLostError, formatDuration, parseDuration } from './queue-core.js';
export type { PostgresQueue, Queue, QueueClaim, QueueDefinition, QueueField, QueueStatements } from './queue-core.js';

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

/** The query config the queue client passes pg. */
export interface QueueQueryConfig {
  readonly text: string;
  readonly values: unknown[];
  readonly rowMode?: 'array';
  readonly types?: { getTypeParser(oid: number, format?: 'text' | 'binary'): unknown };
}

/**
 * What the queue client needs of a pg Pool, PoolClient or Client: an
 * enqueue takes the client of the caller's transaction, and the claims a
 * pool.
 */
export interface QueueQueryable {
  query(config: QueueQueryConfig): Promise<{ rows: unknown[]; rowCount: number | null }>;
}

/**
 * Writes message into queue in the transaction client runs, the caller's:
 * the message is due once the transaction commits, and never if it rolls
 * back. Each member the message sets writes its column, a JSON column its
 * JSON text; one it leaves unset takes the column's default, and an enum
 * with a schema default writes that default, as the Go ORM writes them.
 */
export async function enqueue<M>(client: QueueQueryable, queue: QueueDefinition<M>, message: M): Promise<void> {
  const columns: string[] = [];
  const values: unknown[] = [];
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
      values.push(columnValue(value, field));
    } catch (error) {
      throw new Error(`enqueue on ${queue.table}: field ${field.name}: ${errorMessage(error)}`, { cause: error });
    }
  }
  const text =
    columns.length === 0
      ? `${queue.statements.insert} DEFAULT VALUES`
      : `${queue.statements.insert} (${columns.join(', ')}) VALUES (${columns.map((_, i) => `$${i + 1}`).join(', ')})`;
  try {
    await client.query({ text, values });
  } catch (error) {
    throw new Error(`enqueue on ${queue.table}: ${errorMessage(error)}`, { cause: error });
  }
}

/** What a member a message sets writes to its column: its JSON text, a location's point, a duration's interval, or itself. */
function columnValue(value: unknown, field: QueueField): unknown {
  if (field.json) return JSON.stringify(value);
  if (field.point) return pointText(value);
  if (field.interval) return intervalText(value);
  return value;
}

/** The claims of queue over pool, for a worker. */
export function postgresQueue<M>(pool: QueueQueryable, queue: QueueDefinition<M>): PostgresQueue<M> {
  const update = async (what: string, statement: string, id: string, token: string, extra: unknown[] = []): Promise<void> => {
    let result: { rowCount: number | null };
    try {
      result = await pool.query({ text: statement, values: [id, token, ...extra] });
    } catch (error) {
      throw new Error(`${what} message ${id} on ${queue.table}: ${errorMessage(error)}`, { cause: error });
    }
    if (!result.rowCount) throw new ClaimLostError(`${what} message ${id} on ${queue.table}: ${new ClaimLostError().message}`);
  };
  return {
    name: queue.name,
    leaseSeconds: queue.leaseSeconds,
    async claim(n, leaseSeconds = queue.leaseSeconds) {
      if (n <= 0) return [];
      let rows: unknown[];
      try {
        await pool.query({ text: queue.statements.expire, values: [queue.retries] });
        ({ rows } = await pool.query({ text: queue.statements.claim, values: [n, leaseSeconds], rowMode: 'array', types: queueTypes }));
      } catch (error) {
        throw new Error(`claim on ${queue.table}: ${errorMessage(error)}`, { cause: error });
      }
      return rows.map(row => claimOf(queue, row as readonly unknown[]));
    },
    complete: (id, token) => update('complete', queue.statements.complete, id, token),
    async fail(id, token, cause) {
      let rows: unknown[];
      try {
        ({ rows } = await pool.query({
          text: queue.statements.fail,
          values: [id, token, queue.retries, queue.backoffSeconds, cause, queue.maxBackoffSeconds],
        }));
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
  };
}

/** A claimed row, [id, token, attempt, ...the message's columns], as its claim. */
function claimOf<M>(queue: QueueDefinition<M>, row: readonly unknown[]): QueueClaim<M> {
  const [id, token, attempt] = row;
  const message: Record<string, unknown> = {};
  queue.fields.forEach((field, i) => {
    let value = row[3 + i];
    if (value === null || value === undefined) return;
    if (field.json && typeof value === 'string') {
      try {
        value = JSON.parse(value);
      } catch (error) {
        throw new Error(`message ${String(id)} of ${queue.table}: field ${field.name} holds no JSON: ${errorMessage(error)}`, { cause: error });
      }
    } else if (field.list && typeof value === 'string') {
      // A list of a type pg does not know, such as citext[], arrives as its text.
      value = parseArray(value);
    }
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
 * make {x, y}, and an INTERVAL the canonical text of its duration,
 * Temporal.Duration's, where pg would make an object of its parts.
 */
const overrides: Readonly<Record<number, (value: string) => unknown>> = {
  20: value => Number(value),
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

/**
 * Parses a one-dimensional Postgres array literal, `{a,"b c",NULL}`, into
 * its elements as text, NULL as null.
 */
export function parseArray(text: string): (string | null)[] {
  if (!text.startsWith('{') || !text.endsWith('}')) throw new Error(`${JSON.stringify(text)} is no Postgres array`);
  const out: (string | null)[] = [];
  const body = text.slice(1, -1);
  if (body === '') return out;
  let i = 0;
  while (i <= body.length) {
    if (body[i] === '"') {
      let element = '';
      i++;
      while (i < body.length && body[i] !== '"') {
        if (body[i] === '\\') i++;
        element += body[i] ?? '';
        i++;
      }
      out.push(element);
      i += 2; // the closing quote and the comma
      continue;
    }
    let end = body.indexOf(',', i);
    if (end < 0) end = body.length;
    const element = body.slice(i, end);
    out.push(element === 'NULL' ? null : element);
    i = end + 1;
  }
  return out;
}

/**
 * A location, {lat, lon}, as the text of a Postgres POINT, (x,y): x is the
 * longitude and y the latitude, each as JavaScript writes the number, so it
 * reads back as the same number.
 */
export function pointText(value: unknown): string {
  const { lat, lon } = (value ?? {}) as { lat?: unknown; lon?: unknown };
  if (typeof lat !== 'number' || typeof lon !== 'number' || !Number.isFinite(lat) || !Number.isFinite(lon)) {
    throw new Error(`${JSON.stringify(value)} is no location, {lat, lon}`);
  }
  return `(${lon},${lat})`;
}

/** A POINT's text, (x,y), as the location it holds, {lat: y, lon: x}. */
export function parsePoint(text: string): { lat: number; lon: number } {
  const match = /^\(([^,()]+),([^,()]+)\)$/u.exec(text);
  const lon = Number(match?.[1]);
  const lat = Number(match?.[2]);
  if (match === null || Number.isNaN(lon) || Number.isNaN(lat)) throw new Error(`${JSON.stringify(text)} is no POINT`);
  return { lat, lon };
}

/**
 * A duration's text as the text of a Postgres INTERVAL of its whole
 * microseconds, [-]H:MM:SS.ffffff, the remainder truncated toward zero.
 * Postgres gives the sign of this form to the whole time, where it would
 * give the sign of -1h2m to its hours alone.
 */
export function intervalText(value: unknown): string {
  if (typeof value !== 'string') throw new Error(`${JSON.stringify(value)} is no duration`);
  const micros = parseDuration(value) / 1_000n;
  const sign = micros < 0n ? '-' : '';
  const whole = micros < 0n ? -micros : micros;
  const seconds = whole / 1_000_000n;
  const fraction = whole % 1_000_000n;
  const two = (n: bigint): string => n.toString().padStart(2, '0');
  const tail = fraction === 0n ? '' : `.${fraction.toString().padStart(6, '0')}`;
  return `${sign}${seconds / 3600n}:${two((seconds / 60n) % 60n)}:${two(seconds % 60n)}${tail}`;
}

/** The nanoseconds in each unit an interval's text counts, a month 30 days and a year 12 months, as pgx counts them. */
const intervalUnits: Readonly<Record<string, bigint>> = {
  day: 86_400_000_000_000n,
  days: 86_400_000_000_000n,
  mon: 2_592_000_000_000_000n,
  mons: 2_592_000_000_000_000n,
  year: 31_104_000_000_000_000n,
  years: 31_104_000_000_000_000n,
};

/** The time of an interval's text: a sign, hours, minutes, seconds and up to six places of a second. */
const intervalTime = /^([+-]?)(\d+):(\d{2}):(\d{2})(?:\.(\d{1,6}))?$/u;

/**
 * An INTERVAL's text, as Postgres writes it in IntervalStyle postgres, its
 * default (01:30:00, -00:00:01.5, 1 day 02:00:00, -1 days +02:00:00,
 * 1 mon), as its nanoseconds: a day is 24 hours and a month 30 days.
 */
export function parseInterval(text: string): bigint {
  const words = text.trim().split(/\s+/u);
  let nanos = 0n;
  for (let i = 0; i < words.length; i++) {
    const word = words[i] ?? '';
    const time = intervalTime.exec(word);
    if (time !== null) {
      const [, sign, hours = '0', minutes = '0', seconds = '0', fraction = ''] = time;
      const micros = ((BigInt(hours) * 60n + BigInt(minutes)) * 60n + BigInt(seconds)) * 1_000_000n + BigInt(fraction.padEnd(6, '0') || '0');
      nanos += (sign === '-' ? -micros : micros) * 1_000n;
      continue;
    }
    const unit = intervalUnits[words[i + 1] ?? ''];
    if (!/^[+-]?\d+$/u.test(word) || unit === undefined) {
      throw new Error(`${JSON.stringify(text)} is no interval in IntervalStyle postgres`);
    }
    nanos += BigInt(word) * unit;
    i++;
  }
  return nanos;
}

