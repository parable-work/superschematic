import { parseDuration } from './queue-core.js';

/*
The text forms of Postgres values the clients read and write themselves
(D53, D79): a one-dimensional array literal, a POINT and an INTERVAL. The
queue client (./queue) and the value codec (./pgcodec) share them, and this
module imports no driver, so the repositories on SQLite and D1, which run
over the codec's module, load no pg.
*/

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

