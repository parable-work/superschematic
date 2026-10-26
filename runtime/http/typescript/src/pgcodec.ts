import { decodeJSONText, formatDuration } from './queue-core.js';
import { intervalText, parseArray, parseInterval, parsePoint, pointText } from './queue.js';
import { uuidFromDb, uuidToDb } from './uuid.js';
import { parseGenericDecimalStrict } from 'superscalar/scalars';

/*
The Postgres value codec (D79): how a value crosses between the TypeScript
types and a Postgres column, for the raw SQL an implementation keeps and
for the generated repositories (./postgres's ormOf), which read and write
every row through it. It is driven by the per-field descriptors a DB's
types package carries: a queue's fields in ./queues, and a table's columns
in ./orm, each naming the member, its column and what the column holds.

Identity.UUID has three places (D79): the wire and the TypeScript types
hold its base62 form, the scalar's canonical one, and Postgres holds the
lowercase hyphenated text a UUID column reads and writes. encodeParam
writes a UUID hyphenated from either form, and decodeRow reads it back as
base62, so a row an implementation reads is in the form a response
returns, and ctx.principal.subject, base62, matches its user row through
uuidToDb.

A 64-bit integer is a number, as the types hold Generic.Int64 and every
integer scalar, refused past 2^53 - 1 rather than rounded; a NUMERIC is its
exact text, and in a row a Generic.Decimal's canonical text, which
superscalar writes (D84): Postgres writes a NUMERIC(5,2)'s 1.5 as 1.50,
which the wire form is not; a vector(n) a list of numbers, which crosses as
pgvector's text, [0.5,-1]; a DATE its YYYY-MM-DD text, Temporal.Date's; a TIMESTAMPTZ a
Date; a JSON column the value it holds; a POINT the location {lat, lon}, x
the longitude and y the latitude; an INTERVAL its duration's canonical text
(1h30m0s), a day 24 hours and a month 30 days, as the queue client and
Go's time.Duration read one.

connectPostgres opens its pools with postgresTypeParsers, per pool and
never through pg's global types, so a raw query's BIGINT, NUMERIC and DATE
come back as the types hold them; decodeRow reads pg's own forms too, for a
pool opened without them.

A JSON column is read and bound by its column's type, never by the
JavaScript type of the value (D79). pg parses a JSON or JSONB column with
its own parsers and with postgresTypeParsers alike, so the value arrives as
the JSON value it holds, a string among them, and decodeRow takes it as it
is: parsing it again would read the JSON string "1" as the number 1, and
fail on "x". Only a TEXT column that holds JSON (jsonText), as SQLite keeps
it, arrives as its text, which decodeRow parses; on Postgres every JSON
column is JSONB (D83). encodeParam writes every
JSON value as its JSON text, a string as its quoted text and a list as a
JSON array, never as pg's array literal; null is SQL NULL, but in a column that
may not be null (required), where it is the JSON null.
*/

/** A field of a row and the column it reads: a table's column in ./orm, or a queue's field in ./queues. */
export interface RowField {
  /** The row's member. */
  readonly name: string;
  /** Its column, quoted for SQL where it must be, as a statement names it. */
  readonly column: string;
  /** The column is JSON or JSONB, which pg reads already parsed: the JSON value it holds, a string, a number, a boolean, null, a list or an object. */
  readonly json?: boolean;
  /** The column is TEXT that holds a JSON value, as SQLite keeps one, which decodeRow parses. */
  readonly jsonText?: boolean;
  /** The column is NOT NULL: a JSON column's null is the JSON null, where a nullable column's is SQL NULL. */
  readonly required?: boolean;
  /** The column holds a list as a Postgres array. */
  readonly list?: boolean;
  /** The column, or each element of its list, is a UUID: hyphenated in the database, base62 in the row. */
  readonly uuid?: boolean;
  /** The column, or each element, is a timestamp, a Date in the row. */
  readonly timestamp?: boolean;
  /** The column, or each element, is a DATE, its YYYY-MM-DD text in the row. */
  readonly date?: boolean;
  /** The column, or each element, is a BIGINT, a number in the row, refused past 2^53 - 1. */
  readonly int64?: boolean;
  /** The column, or each element, is a NUMERIC, a Generic.Decimal's canonical text in the row (D84): 1.50 reads as 1.5. */
  readonly numeric?: boolean;
  /** The column is pgvector's vector(n), a list of numbers in the row, written as its text, [0.5,-1] (D84). */
  readonly vector?: boolean;
  /** The column is a POINT, a location {lat, lon} in the row. */
  readonly point?: boolean;
  /** The column is an INTERVAL, its duration's canonical text in the row. */
  readonly interval?: boolean;
  /** The column, or each element, is a boolean. */
  readonly boolean?: boolean;
}

/** What a value of a field is, for encodeParam: a RowField without its member and column. */
export type ValueCodec = Omit<RowField, 'name' | 'column'>;

/** The OIDs of the types postgresTypeParsers reads. */
export const PG_TYPES = {
  int8: 20,
  int8Array: 1016,
  numeric: 1700,
  numericArray: 1231,
  date: 1082,
  dateArray: 1182,
} as const;

/**
 * A BIGINT's text as a number. A value past Number.MAX_SAFE_INTEGER either
 * way is refused, since a number would hold it rounded; cast such a column
 * to text or NUMERIC in the query.
 */
export function parseInt8(text: string): number {
  const n = Number(text);
  if (!Number.isSafeInteger(n)) {
    throw new RangeError(`the BIGINT ${text} is past 2^53 - 1, which a number holds exactly; cast it to text or numeric in the query`);
  }
  return n;
}

function parseInt8Array(text: string): (number | null)[] {
  return parseArray(text).map(element => (element === null ? null : parseInt8(element)));
}

/**
 * A NUMERIC's text as a raw query reads it: a finite one as a
 * Generic.Decimal's canonical text (D84), 1.50 as 1.5, the same value
 * exactly; NaN and the infinities as Postgres writes them.
 */
export function numericText(text: string): string {
  return /^[+-]?[0-9]/u.test(text) ? canonicalDecimal(text) : text;
}

function parseNumericArray(text: string): (string | null)[] {
  return parseArray(text).map(element => (element === null ? null : numericText(element)));
}

/**
 * The type parsers connectPostgres gives each pool, by OID, beside pg's
 * own: BIGINT a number, refused past 2^53 - 1, NUMERIC its exact text in
 * canonical form (numericText), and DATE its YYYY-MM-DD text, where pg
 * would make a Date at local midnight; and a list of each alike.
 */
export const postgresTypeParsers: Readonly<Record<number, (value: string) => unknown>> = {
  [PG_TYPES.int8]: parseInt8,
  [PG_TYPES.int8Array]: parseInt8Array,
  [PG_TYPES.numeric]: numericText,
  [PG_TYPES.numericArray]: parseNumericArray,
  [PG_TYPES.date]: value => value,
  [PG_TYPES.dateArray]: parseArray,
};

/** What pg takes as a pool's or a query's types. */
export interface TypeParsers {
  getTypeParser(oid: number, format?: 'text' | 'binary'): unknown;
}

/**
 * The types of a pool or a query: overrides by OID, text format alone,
 * then fallback, pg's own (pg.types). Nothing global changes.
 */
export function withTypeParsers(fallback: TypeParsers, overrides: Readonly<Record<number, (value: string) => unknown>> = postgresTypeParsers): TypeParsers {
  return {
    getTypeParser(oid: number, format?: 'text' | 'binary'): unknown {
      const override = (format ?? 'text') === 'text' ? overrides[oid] : undefined;
      return override ?? fallback.getTypeParser(oid, format);
    },
  };
}

/** A column's name as a row returned by pg names it: its SQL name without the quotes. */
export function columnKey(column: string): string {
  return column.startsWith('"') && column.endsWith('"') ? column.slice(1, -1).replaceAll('""', '"') : column;
}

/**
 * The row's members, read from a row pg returned, keyed by column: each
 * field's column decoded to the form the TypeScript types hold, an SQL
 * NULL as null. A field whose column the row does not hold is left out, so
 * a query may select some columns alone. Throws, naming the field, for a
 * value that is not of its column's kind.
 */
export function decodeRow<T>(row: Readonly<Record<string, unknown>>, fields: readonly RowField[]): T {
  const out: Record<string, unknown> = {};
  for (const field of fields) {
    const key = columnKey(field.column);
    if (!(key in row)) continue;
    try {
      out[field.name] = decodeValue(row[key], field);
    } catch (error) {
      throw new Error(`column ${key}, field ${field.name}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
    }
  }
  return out as T;
}

/** A column's value as the TypeScript types hold it, an SQL NULL null. */
export function decodeValue(value: unknown, codec: ValueCodec): unknown {
  if (value === null || value === undefined) return null;
  // pg parses a JSON or JSONB column itself: its value is the JSON value,
  // which a string may be. Only a TEXT column holds JSON as its text.
  if (codec.json) return value;
  // A TEXT column's JSON is decoded once, as a queue's claim decodes it.
  if (codec.jsonText) return decodeJSONText(value);
  if (codec.point) return decodePoint(value);
  if (codec.interval) return decodeInterval(value);
  if (codec.vector) return decodeVector(value);
  if (codec.list) {
    // A list of a type pg does not know, such as citext[], arrives as its text.
    const elements = typeof value === 'string' ? parseArray(value) : value;
    if (!Array.isArray(elements)) throw new Error(`${JSON.stringify(value)} is no list`);
    return elements.map(element => (element === null || element === undefined ? null : decodeScalar(element, codec)));
  }
  return decodeScalar(value, codec);
}

function decodeScalar(value: unknown, codec: ValueCodec): unknown {
  if (codec.uuid) return uuidFromDb(value);
  if (codec.int64) {
    if (typeof value === 'number') {
      if (!Number.isSafeInteger(value)) throw new RangeError(`${value} is no integer a number holds exactly`);
      return value;
    }
    return parseInt8(String(value));
  }
  if (codec.numeric) return canonicalDecimal(typeof value === 'string' ? value : String(value));
  if (codec.date) return value instanceof Date ? localDate(value) : String(value).slice(0, 10);
  if (codec.timestamp) return value instanceof Date ? value : timestampOf(String(value));
  if (codec.boolean) return value === 1 || value === '1' || value === 't' ? true : value === 0 || value === '0' || value === 'f' ? false : value;
  return value;
}

/**
 * A NUMERIC's text as a Generic.Decimal's canonical form, superscalar's
 * (D84): no + or leading zero, no trailing zero after the point, and 0 for
 * zero. NaN and the infinities, which a NUMERIC without a precision can
 * hold, are no decimal and are refused.
 */
export function canonicalDecimal(text: string): string {
  try {
    return parseGenericDecimalStrict(text);
  } catch {
    throw new RangeError(`the NUMERIC ${text} is no Generic.Decimal, which holds a finite decimal alone`);
  }
}

/** A vector(n)'s text, [0.5,-1], or the list pg made of it, as its numbers. */
function decodeVector(value: unknown): number[] {
  if (Array.isArray(value)) return value.map(Number);
  const text = String(value).trim();
  if (!text.startsWith('[') || !text.endsWith(']')) throw new Error(`${JSON.stringify(value)} is no vector`);
  const inner = text.slice(1, -1).trim();
  if (inner === '') return [];
  return inner.split(',').map(part => {
    const n = Number(part.trim());
    if (!Number.isFinite(n)) throw new Error(`${JSON.stringify(value)} is no vector`);
    return n;
  });
}

/** A list of numbers in pgvector's text form, [0.5,-1], which pg sends for a vector(n) column. */
export function vectorText(value: unknown): string {
  if (!Array.isArray(value)) throw new Error(`${JSON.stringify(value)} is no vector, a list of numbers`);
  return `[${value
    .map(n => {
      if (typeof n !== 'number' || !Number.isFinite(n)) throw new Error(`${JSON.stringify(value)} is no vector, a list of finite numbers`);
      return String(n);
    })
    .join(',')}]`;
}

/** A Date pg made of a DATE, at local midnight, as its YYYY-MM-DD text. */
function localDate(value: Date): string {
  const two = (n: number): string => String(n).padStart(2, '0');
  return `${String(value.getFullYear()).padStart(4, '0')}-${two(value.getMonth() + 1)}-${two(value.getDate())}`;
}

/** A timestamp's text, as Postgres or SQLite writes it, as a Date. */
function timestampOf(text: string): Date {
  const m = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})(\.\d+)?(Z|[+-]\d{2}(?::?\d{2})?)?$/u.exec(text);
  if (!m) throw new Error(`${JSON.stringify(text)} is no timestamp`);
  const fraction = m[3] ? m[3].slice(0, 4).padEnd(4, '0') : '';
  let zone = m[4] ?? 'Z';
  if (/^[+-]\d{2}$/u.test(zone)) zone += ':00';
  else if (/^[+-]\d{4}$/u.test(zone)) zone = `${zone.slice(0, 3)}:${zone.slice(3)}`;
  const date = new Date(`${m[1]}T${m[2]}${fraction}${zone}`);
  if (Number.isNaN(date.getTime())) throw new Error(`${JSON.stringify(text)} is no timestamp`);
  return date;
}

/** A POINT, as its text or as pg's {x, y}, as the location {lat: y, lon: x}. */
function decodePoint(value: unknown): { lat: number; lon: number } {
  if (typeof value === 'string') return parsePoint(value);
  const { x, y, lat, lon } = value as { x?: unknown; y?: unknown; lat?: unknown; lon?: unknown };
  if (typeof x === 'number' && typeof y === 'number') return { lat: y, lon: x };
  if (typeof lat === 'number' && typeof lon === 'number') return { lat, lon };
  throw new Error(`${JSON.stringify(value)} is no POINT`);
}

/** The microseconds in each part of pg's parsed interval, a month 30 days and a year 12 months. */
const INTERVAL_PARTS: ReadonlyArray<readonly [string, bigint]> = [
  ['years', 31_104_000_000_000n],
  ['months', 2_592_000_000_000n],
  ['days', 86_400_000_000n],
  ['hours', 3_600_000_000n],
  ['minutes', 60_000_000n],
  ['seconds', 1_000_000n],
];

/** An INTERVAL, as its text or as pg's parsed object, as its duration's canonical text. */
function decodeInterval(value: unknown): string {
  if (typeof value === 'string') return formatDuration(parseInterval(value));
  if (typeof value !== 'object' || value === null) throw new Error(`${JSON.stringify(value)} is no INTERVAL`);
  const parts = value as Record<string, unknown>;
  let micros = 0n;
  for (const [part, unit] of INTERVAL_PARTS) {
    const n = parts[part];
    if (typeof n === 'number') micros += BigInt(Math.trunc(n)) * unit;
  }
  if (typeof parts.milliseconds === 'number') micros += BigInt(Math.round(parts.milliseconds * 1000));
  return formatDuration(micros * 1_000n);
}

/**
 * A value of a field as a statement binds it for its column: a UUID
 * hyphenated, from base62 or hyphenated; a JSON value, whatever it is, its
 * JSON text, and null the JSON null in a required JSON column; a location
 * (lon,lat); a duration its whole microseconds; a list each element alike;
 * undefined, and null elsewhere, SQL NULL; any other as it is, which pg
 * writes for its column (a Date, a number, text, a boolean).
 */
export function encodeParam(value: unknown, codec: ValueCodec): unknown {
  if (value === undefined) return null;
  if (codec.json || codec.jsonText) {
    if (value === null) return codec.required ? 'null' : null;
    return JSON.stringify(value);
  }
  if (value === null) return null;
  if (codec.point) return pointText(value);
  if (codec.interval) return intervalText(value);
  if (codec.vector) return vectorText(value);
  if (codec.list && Array.isArray(value)) return value.map(element => (element === null || element === undefined ? null : encodeScalar(element, codec)));
  return encodeScalar(value, codec);
}

function encodeScalar(value: unknown, codec: ValueCodec): unknown {
  if (codec.uuid) return uuidToDb(value);
  if (codec.int64 && typeof value === 'bigint') {
    if (value > BigInt(Number.MAX_SAFE_INTEGER) || value < BigInt(Number.MIN_SAFE_INTEGER)) return value.toString();
    return Number(value);
  }
  return value;
}
