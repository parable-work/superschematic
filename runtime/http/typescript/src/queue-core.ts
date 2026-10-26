/*
The part of a queue client no database's driver is in (D53, section 8.8 of
docs/stack-model.md): a queue's definition, as a DB service's TypeScript
types package exports it in its ./queues entry, the claims a worker makes
of it, the lost claim's error, and a duration's text, which the Postgres
client (queue.ts, ./postgres) and the SQLite client (queue-sqlite.ts,
./sqlite and ./d1) both read and write. It imports nothing but uuid.ts,
which imports nothing, so a Worker bundles it.
*/

import { uuidToDb } from './uuid.js';

/** A field of a queue's message and the column it writes, as a types package's ./queues entry declares it. */
export interface QueueField {
  /** The message's member. */
  readonly name: string;
  /** Its column, quoted for SQL where it must be. */
  readonly column: string;
  /** The column holds the value as JSON, which an enqueue writes with JSON.stringify. */
  readonly json?: boolean;
  /**
   * The kind of JSON value a JSON column holds for the member's type: an object for a @jsonField type or a map, a
   * list for a list of them. A claim refuses a row whose JSON is of another kind, as Go's and Rust's types do.
   * None for a JSON scalar, which holds any value.
   */
  readonly shape?: 'object' | 'list';
  /** The column holds a list as a Postgres array. */
  readonly list?: boolean;
  /** The schema's default of an enum, which a message that leaves the member unset writes. */
  readonly default?: string;
  /** The column is a POINT, which a location, {lat, lon}, writes as (lon,lat). */
  readonly point?: boolean;
  /** The column is an INTERVAL, which a duration's text writes as its whole microseconds. */
  readonly interval?: boolean;
  /** The column, or each element of its list, holds a UUID, its lowercase hyphenated text on SQLite. */
  readonly uuid?: boolean;
  /** The column, or each element of its list, holds a timestamp, RFC 3339 in UTC on SQLite. */
  readonly timestamp?: boolean;
  /** The column, or each element of its list, holds a boolean, 1 or 0 in a column on SQLite. */
  readonly boolean?: boolean;
}

/**
 * A queue's statements on Postgres: insert begins an enqueue; expire marks
 * dead each message whose last attempt's claim expired ($1 the retries);
 * claim claims up to $1 due messages for $2 seconds; complete, fail, extend
 * and release change message $1 while token $2 claims it; prune deletes up
 * to $2 messages done $1 seconds ago or more. Its statements on SQLite
 * take the same parameters, numbered ?1, ?2 (D53, amended).
 */
export interface QueueStatements {
  readonly insert: string;
  /**
   * Ends an enqueue on a queue that keeps one ready message per key (D80): what it does when a ready message
   * holds the message's key, which replaces that message or keeps it. Other queues have none.
   */
  readonly conflict?: string;
  readonly expire: string;
  readonly claim: string;
  readonly complete: string;
  readonly fail: string;
  readonly extend: string;
  readonly release: string;
  readonly prune: string;
  /** On SQLite alone: whether a message is due, which a claim reads before it takes the write lock. */
  readonly due?: string;
}

/** A queue of a database, whose messages are M, as its types package's ./queues entry exports it. */
export interface QueueDefinition<M> {
  /** The @queue class. */
  readonly name: string;
  /** Its table, quoted for SQL where it must be. */
  readonly table: string;
  /** How many times a message whose handler fails is handled again before it is dead. */
  readonly retries: number;
  /** How long a message that failed once waits before it is due again; each failure after doubles it. */
  readonly backoffSeconds: number;
  /** The longest a failed message waits, however many times it failed. */
  readonly maxBackoffSeconds: number;
  /** How long a claim holds a message unless its worker extends it. */
  readonly leaseSeconds: number;
  /** How long a done message stays before a prune deletes it; 0 deletes it as it completes. */
  readonly retainSeconds: number;
  /** The message's fields, in the order a claim returns their columns. */
  readonly fields: readonly QueueField[];
  readonly statements: QueueStatements;
  /** Its statements on SQLite, for a database whose DB service lists the sqlite dialect. */
  readonly sqliteStatements?: QueueStatements;
  /** The type of the queue's messages, which no definition sets. */
  readonly message?: M;
}

/**
 * The error complete, fail, extend and release return when the token no
 * longer claims the message: its claim expired, and another worker may
 * hold it.
 */
export class ClaimLostError extends Error {
  constructor(message = 'the claim on the message is lost: it expired, and another worker may hold the message') {
    super(message);
    this.name = 'ClaimLostError';
  }
}

/** A claimed message: its ID, the token of the claim, which attempt this is, from 1, and the message. */
export interface QueueClaim<M> {
  readonly id: string;
  readonly token: string;
  readonly attempt: number;
  readonly message: M;
}

/** The claims of a queue, which a worker makes: postgresQueue's over a pool, sqliteQueue's over a SqlDatabase. */
export interface Queue<M> {
  /** The @queue class. */
  readonly name: string;
  /** How long a claim holds a message unless extended: the queue's lease. */
  readonly leaseSeconds: number;
  /** Claims up to n due messages for leaseSeconds, the queue's lease unless given. */
  claim(n: number, leaseSeconds?: number): Promise<QueueClaim<M>[]>;
  /** Marks the message id, which token claims, done. */
  complete(id: string, token: string): Promise<void>;
  /**
   * Records cause on the message id, which token claims, and makes it due
   * again after the backoff, doubled for each attempt before and capped at
   * the maximum backoff, or dead when its retries are spent. Resolves to
   * whether the message is dead.
   */
  fail(id: string, token: string, cause: string): Promise<boolean>;
  /** Holds the message id, which token claims, for leaseSeconds from now, the queue's lease unless given. */
  extend(id: string, token: string, leaseSeconds?: number): Promise<void>;
  /** Gives the message id, which token claims, back, due at once, its attempt not counted. */
  release(id: string, token: string): Promise<void>;
  /**
   * Deletes up to limit messages done for the queue's retention or longer,
   * the oldest first, and resolves to how many it deleted. It never deletes
   * a dead message.
   */
  prune(limit: number): Promise<number>;
}

/** The claims of a queue over a pg pool, which postgresQueue makes. */
export type PostgresQueue<M> = Queue<M>;

/** What an enqueue ends with after its VALUES: a space and the queue's conflict clause, or nothing (D80). */
export function conflictOf(statements: QueueStatements): string {
  return statements.conflict ? ` ${statements.conflict}` : '';
}

/** What a message's member is, unset: absent or null. An enum with a default is also unset when empty, as in Go. */
export function unset(value: unknown, field: QueueField): boolean {
  return value === undefined || value === null || (field.default !== undefined && value === '');
}

/** The nanoseconds in each unit of a duration's text, the scalar core's units. */
const durationUnits: Readonly<Record<string, bigint>> = {
  ns: 1n,
  us: 1_000n,
  'µs': 1_000n,
  'μs': 1_000n,
  ms: 1_000_000n,
  s: 1_000_000_000n,
  m: 60_000_000_000n,
  h: 3_600_000_000_000n,
};

/** A number of a duration's text and its unit, the longer unit first where one starts another. */
const durationSegment = /^(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/u;

/**
 * A duration's text, as Temporal.Duration holds it (1h30m0s, -1.5s, 90m,
 * 250us), as its nanoseconds: a sign for the whole, then one or more
 * numbers, each with its unit, or a bare 0.
 */
export function parseDuration(text: string): bigint {
  const value = text.trim();
  const sign = value.startsWith('-') ? -1n : 1n;
  let rest = /^[+-]/u.test(value) ? value.slice(1) : value;
  if (rest === '0') return 0n;
  if (rest === '') throw new Error(`${JSON.stringify(text)} is no duration`);
  let nanos = 0n;
  while (rest !== '') {
    const match = durationSegment.exec(rest);
    if (match === null) throw new Error(`${JSON.stringify(text)} is no duration`);
    const [whole = '', fraction = ''] = (match[1] ?? '').split('.');
    const unit = durationUnits[match[2] ?? ''] ?? 0n;
    nanos += BigInt(whole || '0') * unit;
    if (fraction !== '') nanos += (BigInt(fraction) * unit) / 10n ** BigInt(fraction.length);
    rest = rest.slice(match[0].length);
  }
  return sign * nanos;
}

/**
 * Nanoseconds as a duration's text in the scalar core's canonical form:
 * under a second, the largest of ms, us and ns that holds it whole (500ms,
 * 250us); from a second, hours and minutes when there are any, then
 * seconds with their fraction (1h30m0s, 1m30s, 1.5s); 0s for zero.
 */
export function formatDuration(nanos: bigint): string {
  if (nanos === 0n) return '0s';
  const sign = nanos < 0n ? '-' : '';
  let rest = nanos < 0n ? -nanos : nanos;
  const second = 1_000_000_000n;
  if (rest < second) {
    if (rest % 1_000_000n === 0n) return `${sign}${rest / 1_000_000n}ms`;
    if (rest % 1_000n === 0n) return `${sign}${rest / 1_000n}us`;
    return `${sign}${rest}ns`;
  }
  const hours = rest / (3600n * second);
  rest %= 3600n * second;
  const minutes = rest / (60n * second);
  rest %= 60n * second;
  const fraction = rest % second;
  const seconds = `${rest / second}${fraction === 0n ? '' : `.${fraction.toString().padStart(9, '0').replace(/0+$/u, '')}`}s`;
  if (hours > 0n) return `${sign}${hours}h${minutes}m${seconds}`;
  if (minutes > 0n) return `${sign}${minutes}m${seconds}`;
  return `${sign}${seconds}`;
}

/**
 * A JSON column's text as the message's member: the JSON it holds, decoded once, of the kind the field's shape
 * names, or JSON null. A claim reads a JSON column as its text on every database: Postgres's json and jsonb
 * columns arrive unparsed (queue.ts's type parsers), and SQLite's are TEXT. So a value that is itself a JSON
 * string, "1" or "x", reads as that string, and is not parsed a second time.
 */
export function jsonMemberValue(text: unknown, field: QueueField): unknown {
  return decodeJSONText(text, field.shape);
}

/**
 * A JSON column's text decoded once (D79, D80): the JSON value it holds, a JSON string among them, and of the kind
 * shape names when it names one. A queue's claim reads every JSON column so, and the Postgres value codec
 * (pgcodec.ts) a TEXT column that holds JSON; a json or jsonb column a repository reads arrives parsed by pg, the
 * same value.
 */
export function decodeJSONText(text: unknown, shape?: 'object' | 'list'): unknown {
  if (typeof text !== 'string') throw new Error(`the column holds ${JSON.stringify(text)}, no JSON text`);
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new Error(`holds no JSON: ${errorMessage(error)}`, { cause: error });
  }
  if (shape !== undefined && value !== null && jsonKind(value) !== shape) {
    throw new Error(`holds ${article(jsonKind(value))}, where its type is ${article(shape)}`);
  }
  return value;
}

/** The kind of a decoded JSON value: an object, a list, a string, a number or a boolean. */
function jsonKind(value: unknown): string {
  if (Array.isArray(value)) return 'list';
  return typeof value === 'object' ? 'object' : typeof value;
}

/** A kind with its article: an object, a list. */
function article(kind: string): string {
  return /^[aeiou]/u.test(kind) ? `an ${kind}` : `a ${kind}`;
}

/** An error's message. */
export function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

/**
 * What a claim records on a message it took but could not read into its
 * type, before the reason: the claim fails that message alone, as a handler
 * that threw would, and hands the others to their handlers, so one
 * undecodable row neither strands its batch nor burns its neighbours'
 * attempts. Go and Rust record the same words.
 */
export const UNREADABLE = 'the message could not be read';

/**
 * A UUID's lowercase hyphenated text from its hyphenated form or its base62
 * form (1 to 22 characters, case-sensitive), as superscalar's ParseUUID, the
 * Go ORM and the Rust client read one, since a TypeScript type's
 * Identity.UUID may hold either. Throws for any other text. It is
 * uuidToDb (uuid.ts), the one copy every runtime path converts through.
 */
export function canonicalUUID(value: unknown): string {
  return uuidToDb(value);
}
