import type { SqlValue } from './d1.js';
import { canonicalDecimal, columnKey, type RowField, type ValueCodec } from './pgcodec.js';
import { decodeJSONText, formatDuration, parseDuration } from './queue-core.js';
import { utcTimestamp } from './queue-sqlite.js';
import { uuidFromDb, uuidToDb } from './uuid.js';

/*
The SQLite value codec (D116): how a value crosses between the TypeScript
types and a column of the SQLite DDL the build writes (sqlmigrate), for the
repositories on a SQLite file or a D1 binding (./orm-sqlite). It reads the
same per-field descriptors the Postgres codec (./pgcodec) reads, and gives
a row the members Postgres's gives, so a repository answers alike on
either: a UUID in its base62 wire form, a timestamp a Date, a DATE its
YYYY-MM-DD text, a decimal a Generic.Decimal's canonical text, a JSON
column the value it holds.

SQLite keeps some field types differently, and the Rust repositories on a
file write them so too:
  - a UUID is TEXT, lowercase and hyphenated, as Postgres reads one;
  - a boolean is INTEGER 1 or 0;
  - a timestamp is TEXT in RFC 3339 in UTC to the millisecond, the form a
    column's default writes (strftime('%Y-%m-%dT%H:%M:%fZ')), so two order
    as text; a longer fraction is cut to the millisecond;
  - a DATE is TEXT, YYYY-MM-DD;
  - an INTERVAL is TEXT, its duration's canonical text (1h30m0s);
  - a decimal is bound as its text, which the column's NUMERIC affinity
    stores as the number it writes, and is read back in canonical form:
    NUMERIC(12,2)'s 1.50 is 1.5 on either;
  - a JSON value, a list and a list's elements are TEXT holding JSON: a
    list's UUIDs hyphenated, its timestamps in UTC, its booleans true and
    false; null is SQL NULL but in a column that may not be null, where it
    is the JSON null, as on Postgres.
SQLite has no storage for a POINT or a vector: the build refuses a table
that holds one on SQLite, and so does each function here.
*/

/** A timestamp in UTC with exactly three digits of fraction, the form a SQLite column's default writes. */
function millis(utc: string): string {
  const body = utc.endsWith('Z') ? utc.slice(0, -1) : utc;
  const dot = body.indexOf('.');
  const whole = dot < 0 ? body : body.slice(0, dot);
  const fraction = dot < 0 ? '' : body.slice(dot + 1);
  return `${whole}.${fraction.slice(0, 3).padEnd(3, '0')}Z`;
}

/** A timestamp, a Date or its RFC 3339 text, as a SQLite column holds it. */
export function sqliteTimestamp(value: unknown): string {
  if (value instanceof Date) {
    if (Number.isNaN(value.getTime())) throw new Error('the Date is invalid');
    return value.toISOString();
  }
  if (typeof value === 'string') return millis(utcTimestamp(value));
  throw new Error(`${JSON.stringify(value)} is no timestamp, a Date or its RFC 3339 text`);
}

/** A scalar value, or an element of a list, as SQLite holds it: what encodeScalar binds, before a list's JSON. */
function encodeElement(value: unknown, codec: ValueCodec): unknown {
  if (codec.point) throw new Error('SQLite has no storage for a location');
  if (codec.vector) throw new Error('SQLite has no storage for a vector');
  if (codec.uuid) return uuidToDb(value);
  if (codec.timestamp) return sqliteTimestamp(value);
  if (codec.date) return value instanceof Date ? value.toISOString().slice(0, 10) : String(value);
  if (codec.interval) {
    if (typeof value !== 'string') throw new Error(`${JSON.stringify(value)} is no duration's text`);
    return formatDuration(parseDuration(value));
  }
  if (codec.numeric) {
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'bigint') return String(value);
    throw new Error(`${JSON.stringify(value)} is no decimal`);
  }
  if (typeof value === 'bigint') {
    if (value > BigInt(Number.MAX_SAFE_INTEGER) || value < BigInt(Number.MIN_SAFE_INTEGER)) {
      throw new Error(`${value} is out of the range a SQLite binding holds exactly, ±(2^53 - 1)`);
    }
    return Number(value);
  }
  if (codec.boolean && typeof value !== 'boolean') throw new Error(`${JSON.stringify(value)} is no boolean`);
  return value;
}

/**
 * A value of a field as a SQLite statement binds it for its column: a
 * UUID hyphenated, from base62 or hyphenated; a JSON value, whatever it is,
 * its JSON text, and null the JSON null in a required JSON column; a list
 * a JSON array of its elements as SQLite holds them; a timestamp its UTC
 * text to the millisecond; a boolean 1 or 0; a decimal its text; undefined,
 * and null elsewhere, SQL NULL. Throws for a location or a vector, which
 * SQLite has no storage for.
 */
export function sqliteEncodeParam(value: unknown, codec: ValueCodec): SqlValue {
  if (value === undefined) return null;
  if (codec.json || codec.jsonText) {
    if (value === null) return codec.required ? 'null' : null;
    return JSON.stringify(value);
  }
  if (value === null) return null;
  if (codec.list) {
    if (!Array.isArray(value)) throw new Error(`${JSON.stringify(value)} is no list`);
    return JSON.stringify(value.map(element => (element === null || element === undefined ? null : encodeElement(element, codec))));
  }
  const element = encodeElement(value, codec);
  if (typeof element === 'boolean') return element ? 1 : 0;
  if (element instanceof Uint8Array || typeof element === 'string' || typeof element === 'number') return element;
  if (element instanceof Date) return element.toISOString();
  throw new Error(`${JSON.stringify(element)} is no value a SQLite column holds`);
}

/** A scalar column's value, or a list's element, as the TypeScript types hold it. */
function decodeElement(value: unknown, codec: ValueCodec): unknown {
  if (codec.uuid) return uuidFromDb(value);
  if (codec.numeric) return canonicalDecimal(String(value));
  if (codec.timestamp) {
    if (value instanceof Date) return value;
    const date = new Date(String(value));
    if (Number.isNaN(date.getTime())) throw new Error(`${JSON.stringify(value)} is no timestamp`);
    return date;
  }
  if (codec.date) return String(value).slice(0, 10);
  if (codec.interval) return formatDuration(parseDuration(String(value)));
  if (codec.boolean) {
    if (typeof value === 'boolean') return value;
    return value === 1 || value === '1' ? true : value === 0 || value === '0' ? false : value;
  }
  if (typeof value === 'bigint') {
    if (value > BigInt(Number.MAX_SAFE_INTEGER) || value < BigInt(Number.MIN_SAFE_INTEGER)) {
      throw new RangeError(`${value} is out of the range a number holds exactly, ±(2^53 - 1)`);
    }
    return Number(value);
  }
  if (codec.int64 && typeof value === 'number' && !Number.isSafeInteger(value)) throw new RangeError(`${value} is no integer a number holds exactly`);
  return value;
}

/** A SQLite column's value as the TypeScript types hold it, an SQL NULL null. */
export function sqliteDecodeValue(value: unknown, codec: ValueCodec): unknown {
  if (value === null || value === undefined) return null;
  // A JSON column is TEXT: its text is decoded once, a JSON string among it.
  if (codec.json || codec.jsonText) return decodeJSONText(value);
  if (codec.point) throw new Error('SQLite has no storage for a location');
  if (codec.vector) throw new Error('SQLite has no storage for a vector');
  if (codec.list) {
    const elements: unknown = typeof value === 'string' ? JSON.parse(value) : value;
    if (!Array.isArray(elements)) throw new Error(`${JSON.stringify(value)} is no list`);
    return elements.map(element => (element === null || element === undefined ? null : decodeElement(element, codec)));
  }
  return decodeElement(value, codec);
}

/**
 * The row's members, read from a row SQLite returned, keyed by column, as
 * decodeRow (./pgcodec) reads Postgres's: each field's column decoded to
 * the form the TypeScript types hold, a field whose column the row does not
 * hold left out. Throws, naming the field, for a value that is not of its
 * column's kind.
 */
export function sqliteDecodeRow<T>(row: Readonly<Record<string, unknown>>, fields: readonly RowField[]): T {
  const out: Record<string, unknown> = {};
  for (const field of fields) {
    const key = columnKey(field.column);
    if (!(key in row)) continue;
    try {
      out[field.name] = sqliteDecodeValue(row[key], field);
    } catch (error) {
      throw new Error(`column ${key}, field ${field.name}: ${error instanceof Error ? error.message : String(error)}`, { cause: error });
    }
  }
  return out as T;
}
