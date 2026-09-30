// Canonical rows (D19): the rules that turn a row as Postgres renders it, in
// to_jsonb of a live row or in a history image, into the canonical row the
// core compares and hashes. runtime/versiongraph/README.md ("Canonical
// rows") is the contract and runtime/versiongraph/testdata/canonical holds
// its vectors; this module is the TypeScript port of the Go module's
// package canonical, rule for rule.
//
// A canonical row is a JSON object keyed by column name whose values are the
// schema runtime's JSON for each field's type, in one canonical form per
// value class. Rows travel as JSON text, so a number keeps its digits.

import {
  compareCodePoints,
  isJsonObject,
  JsonNumber,
  JsonSyntaxError,
  parseJson,
  writeJsonString,
  type JsonValue,
} from "./json.js";

/** The element classes. A column's class is one of them, a list of one or a list of lists. */
export const ElementClasses = [
  "string",
  "integer",
  "number",
  "boolean",
  "uuid",
  "dateTime",
  "date",
  "time",
  "duration",
  "enum",
  "json",
] as const;

/** A value its class's rule refuses, or a row that does not fit its columns. */
export class CanonicalError extends Error {
  /** The row's column, "" for a single value. */
  readonly column: string;
  /** The value class the rule belongs to. */
  readonly class: string;
  /** What is wrong with the value. */
  readonly detail: string;

  constructor(column: string, valueClass: string, detail: string) {
    super(
      column !== ""
        ? `canonical: column ${column} (${valueClass}): ${detail}`
        : `canonical: ${valueClass}: ${detail}`,
    );
    this.name = "CanonicalError";
    this.column = column;
    this.class = valueClass;
    this.detail = detail;
  }
}

/** A class that is not an element class, a list of one or a list of lists of one. */
export class UnknownClassError extends Error {
  constructor(valueClass: string, column = "") {
    super(
      (column !== "" ? `canonical: column ${column}: ` : "canonical: ") +
        `unknown value class ${JSON.stringify(valueClass)}`,
    );
    this.name = "UnknownClassError";
  }
}

/** A rule turns one decoded element into its canonical JSON text, or throws a RuleError. */
type Rule = (value: JsonValue) => string;

/** Why a rule refused a value; the caller wraps it in a CanonicalError. */
class RuleError extends Error {}

const rules: Record<string, Rule> = {
  string: stringRule,
  integer: integerRule,
  number: numberRule,
  boolean: booleanRule,
  uuid: uuidRule,
  dateTime: dateTimeRule,
  date: dateRule,
  time: timeRule,
  duration: durationRule,
  enum: stringRule,
  json: jsonRule,
};

function parseClass(valueClass: string): { rule: Rule; depth: number } {
  let element = valueClass;
  let depth = 0;
  if (valueClass.endsWith("[][]")) {
    element = valueClass.slice(0, -4);
    depth = 2;
  } else if (valueClass.endsWith("[]")) {
    element = valueClass.slice(0, -2);
    depth = 1;
  }
  const rule = Object.prototype.hasOwnProperty.call(rules, element) ? rules[element] : undefined;
  if (rule === undefined) {
    throw new UnknownClassError(valueClass);
  }
  return { rule, depth };
}

function decode(text: string): JsonValue {
  try {
    return parseJson(text);
  } catch (err) {
    if (err instanceof JsonSyntaxError) {
      throw new RuleError(err.message);
    }
    throw err;
  }
}

/**
 * The canonical JSON text of one value of a class, given as JSON text as
 * Postgres renders it inside to_jsonb. JSON null is null in every class.
 */
export function canonicalValue(valueClass: string, json: string): string {
  const { rule, depth } = parseClass(valueClass);
  try {
    return apply(rule, depth, decode(json));
  } catch (err) {
    if (err instanceof RuleError) {
      throw new CanonicalError("", valueClass, err.message);
    }
    throw err;
  }
}

/**
 * The canonical row of a row as to_jsonb renders it, whose columns have the
 * classes in columns: a JSON object with its members sorted by column name.
 * A column the row has and columns lacks is refused; one columns has and the
 * row lacks stays absent. The rules read the schema runtime's forms as they
 * read Postgres's (a base62 UUID, a date-time with any offset, an "HH:MM"
 * time, a duration string), so a typed facade turns a row of the schema
 * runtime's JSON into a canonical row with it too.
 */
export function canonicalRow(columns: Readonly<Record<string, string>>, json: string): string {
  let decoded: JsonValue;
  try {
    decoded = parseJson(json);
  } catch (err) {
    if (err instanceof JsonSyntaxError) {
      throw new Error(`canonical: row: ${err.message}`);
    }
    throw err;
  }
  return canonicalRowValue(columns, decoded);
}

/** canonicalRow of a row already parsed. */
export function canonicalRowValue(columns: Readonly<Record<string, string>>, row: JsonValue): string {
  if (!isJsonObject(row)) {
    throw new Error("canonical: a row is a JSON object");
  }
  const names = [...row.keys()].sort(compareCodePoints);
  const out: string[] = [];
  for (const name of names) {
    const valueClass = Object.prototype.hasOwnProperty.call(columns, name) ? columns[name] : undefined;
    if (valueClass === undefined) {
      throw new CanonicalError(name, "", "the row has a column its descriptor does not declare");
    }
    let parsed: { rule: Rule; depth: number };
    try {
      parsed = parseClass(valueClass);
    } catch (err) {
      if (err instanceof UnknownClassError) {
        throw new UnknownClassError(valueClass, name);
      }
      throw err;
    }
    let value: string;
    try {
      value = apply(parsed.rule, parsed.depth, row.get(name)!);
    } catch (err) {
      if (err instanceof RuleError) {
        throw new CanonicalError(name, valueClass, err.message);
      }
      throw err;
    }
    out.push(writeJsonString(name) + ":" + value);
  }
  return "{" + out.join(",") + "}";
}

/** The canonical JSON text of an already parsed value of a class. */
export function canonicalOf(valueClass: string, value: JsonValue): string {
  const { rule, depth } = parseClass(valueClass);
  try {
    return apply(rule, depth, value);
  } catch (err) {
    if (err instanceof RuleError) {
      throw new CanonicalError("", valueClass, err.message);
    }
    throw err;
  }
}

function apply(rule: Rule, depth: number, value: JsonValue): string {
  if (value === null) {
    return "null";
  }
  if (depth === 0) {
    return rule(value);
  }
  if (!Array.isArray(value)) {
    throw new RuleError(`${describe(value)} is not a list`);
  }
  const out: string[] = [];
  value.forEach((element, i) => {
    if (element === null) {
      throw new RuleError(`element ${i} is null, and a list element is never null`);
    }
    try {
      out.push(apply(rule, depth - 1, element));
    } catch (err) {
      if (err instanceof RuleError) {
        throw new RuleError(`element ${i}: ${err.message}`);
      }
      throw err;
    }
  });
  return "[" + out.join(",") + "]";
}

function describe(value: JsonValue): string {
  if (typeof value === "string") {
    return JSON.stringify(value);
  }
  if (value instanceof JsonNumber) {
    return "the number " + value.text;
  }
  if (typeof value === "boolean") {
    return String(value);
  }
  if (Array.isArray(value)) {
    return "a list";
  }
  if (value instanceof Map) {
    return "an object";
  }
  return String(value);
}

function stringOf(value: JsonValue): string {
  if (typeof value !== "string") {
    throw new RuleError(`${describe(value)} is not a string`);
  }
  return value;
}

function stringRule(value: JsonValue): string {
  return writeJsonString(stringOf(value));
}

function booleanRule(value: JsonValue): string {
  if (typeof value !== "boolean") {
    throw new RuleError(`${describe(value)} is not a boolean`);
  }
  return value ? "true" : "false";
}

/**
 * Writes any JSON value canonically: object members sorted by key, no
 * whitespace, strings escaped as writeJsonString does, and every number in
 * the number class's form.
 */
function jsonRule(value: JsonValue): string {
  if (value === null) {
    return "null";
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  if (typeof value === "string") {
    return writeJsonString(value);
  }
  if (value instanceof JsonNumber) {
    return numberRule(value);
  }
  if (Array.isArray(value)) {
    return "[" + value.map(jsonRule).join(",") + "]";
  }
  const names = [...value.keys()].sort(compareCodePoints);
  return "{" + names.map((name) => writeJsonString(name) + ":" + jsonRule(value.get(name)!)).join(",") + "}";
}

const integerPattern = /^-?(?:0|[1-9][0-9]*)$/;
const numberPattern = /^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$/;

/**
 * Keeps an integer's digits exactly, however wide: an optional minus and no
 * leading zeros, with -0 written 0. A fraction or an exponent is refused.
 */
function integerRule(value: JsonValue): string {
  if (!(value instanceof JsonNumber) || !integerPattern.test(value.text)) {
    throw new RuleError(`${describe(value)} is not an integer`);
  }
  return value.text === "-0" ? "0" : value.text;
}

/**
 * Writes a number's exact decimal value in the layout ECMAScript's
 * Number::toString uses: plain digits while the decimal point falls within
 * 21 digits of the first and no more than 6 zeros follow it, else one digit,
 * a fraction and an exponent with its sign (1e+21, 1.5e-7). Trailing zeros
 * go (1.50 is 1.5), and -0 is 0; the digits are never rounded.
 */
function numberRule(value: JsonValue): string {
  if (!(value instanceof JsonNumber)) {
    throw new RuleError(`${describe(value)} is not a number`);
  }
  const m = numberPattern.exec(value.text);
  if (m === null) {
    throw new RuleError(`${JSON.stringify(value.text)} is not a JSON number`);
  }
  const negative = m[1] === "-";
  const whole = m[2]!;
  const fraction = m[3] ?? "";
  const exponentText = m[4] ?? "";
  let exponent = 0;
  if (exponentText !== "") {
    // strconv.ParseInt(text, 10, 32): an optional sign, then digits, in
    // the range of a 32-bit integer.
    const e = BigInt(exponentText);
    if (e < -(2n ** 31n) || e > 2n ** 31n - 1n) {
      throw new RuleError(`the exponent of ${value.text} is out of range`);
    }
    exponent = Number(e);
  }
  // The value is 0.digits * 10^point.
  let digits = whole + fraction;
  let point = whole.length + exponent;
  const trimmed = digits.replace(/^0+/, "");
  point -= digits.length - trimmed.length;
  digits = trimmed.replace(/0+$/, "");
  if (digits === "") {
    return "0";
  }
  const sign = negative ? "-" : "";
  const k = digits.length;
  if (k <= point && point <= 21) {
    return sign + digits + "0".repeat(point - k);
  }
  if (0 < point && point <= 21) {
    return sign + digits.slice(0, point) + "." + digits.slice(point);
  }
  if (-6 < point && point <= 0) {
    return sign + "0." + "0".repeat(-point) + digits;
  }
  let e = point - 1;
  let exponentSign = "+";
  if (e < 0) {
    exponentSign = "-";
    e = -e;
  }
  let mantissa = digits.slice(0, 1);
  if (k > 1) {
    mantissa += "." + digits.slice(1);
  }
  return sign + mantissa + "e" + exponentSign + String(e);
}

const base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const hyphenatedUUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const base62UUID = /^[0-9A-Za-z]{1,22}$/;

/**
 * Writes a UUID in the scalar core's canonical form, base62 of its 128 bits
 * (the nil UUID is "0"). It reads the hyphenated form Postgres renders, in
 * either case, and the base62 form.
 */
function uuidRule(value: JsonValue): string {
  return writeJsonString(canonicalUUID(stringOf(value)));
}

/** The canonical form (base62) of a UUID given hyphenated or in base62. */
function canonicalUUID(s: string): string {
  let n: bigint;
  if (hyphenatedUUID.test(s)) {
    n = BigInt("0x" + s.replace(/-/g, ""));
  } else if (base62UUID.test(s)) {
    n = 0n;
    for (const c of s) {
      n = n * 62n + BigInt(base62Alphabet.indexOf(c));
    }
    if (n >= 1n << 128n) {
      throw new RuleError(`${JSON.stringify(s)} is wider than a UUID`);
    }
  } else {
    throw new RuleError(`${JSON.stringify(s)} is not a UUID`);
  }
  if (n === 0n) {
    return "0";
  }
  let out = "";
  while (n > 0n) {
    out = base62Alphabet[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out;
}

/**
 * The canonical form (base62) of a UUID given hyphenated or in base62.
 * Throws a CanonicalError for anything else.
 */
export function uuidCanonical(s: string): string {
  try {
    return canonicalUUID(s);
  } catch (err) {
    if (err instanceof RuleError) {
      throw new CanonicalError("", "uuid", err.message);
    }
    throw err;
  }
}

/** The hyphenated form Postgres reads of a UUID given in base62 or hyphenated. */
export function uuidHyphenated(s: string): string {
  let n = 0n;
  for (const c of uuidCanonical(s)) {
    n = n * 62n + BigInt(base62Alphabet.indexOf(c));
  }
  const hex = n.toString(16).padStart(32, "0");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20, 32)}`;
}

const dateTimePattern =
  /^([0-9]{4})-([0-9]{2})-([0-9]{2})[T ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}(?::[0-9]{2}(?::[0-9]{2})?)?)$/;
const datePattern = /^([0-9]{4})-([0-9]{2})-([0-9]{2})$/;
const timePattern = /^([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?$/;
// Go's \s, which the rule was written with, is [\t\n\f\r ].
const clockPattern = /^([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?[\t\n\f\r ]?([AaPp])[Mm]$/;

function isLeap(year: number): boolean {
  return (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
}

function daysIn(year: number, month: number): number {
  return [31, isLeap(year) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1]!;
}

/** Whether a calendar date exists in the proleptic Gregorian calendar. */
function validDate(year: number, month: number, day: number): boolean {
  return month >= 1 && month <= 12 && day >= 1 && day <= daysIn(year, month);
}

// Days since 1970-01-01 of a proleptic Gregorian date, and back.
function daysFromCivil(y: number, m: number, d: number): number {
  y -= m <= 2 ? 1 : 0;
  const era = Math.floor(y / 400);
  const yoe = y - era * 400;
  const doy = Math.floor((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5) + d - 1;
  const doe = yoe * 365 + Math.floor(yoe / 4) - Math.floor(yoe / 100) + doy;
  return era * 146097 + doe - 719468;
}

function civilFromDays(z: number): [number, number, number] {
  z += 719468;
  const era = Math.floor(z / 146097);
  const doe = z - era * 146097;
  const yoe = Math.floor((doe - Math.floor(doe / 1460) + Math.floor(doe / 36524) - Math.floor(doe / 146096)) / 365);
  const y = yoe + era * 400;
  const doy = doe - (365 * yoe + Math.floor(yoe / 4) - Math.floor(yoe / 100));
  const mp = Math.floor((5 * doy + 2) / 153);
  const d = doy - Math.floor((153 * mp + 2) / 5) + 1;
  const m = mp + (mp < 10 ? 3 : -9);
  return [m <= 2 ? y + 1 : y, m, d];
}

function pad(n: number, width: number): string {
  return String(n).padStart(width, "0");
}

/**
 * Writes an instant as RFC 3339 in UTC with a Z, its fraction of a second
 * without trailing zeros and left out when zero: Go's RFC3339Nano of the UTC
 * time. It reads the form Postgres renders, whose offset follows the
 * session's time zone and may carry seconds (+00:17:30), and any RFC 3339
 * time with an offset. A year outside 0000-9999, BC, infinity, a time
 * without an offset and an offset of a day or more are refused.
 */
function dateTimeRule(value: JsonValue): string {
  const s = stringOf(value);
  const m = dateTimePattern.exec(s);
  if (m === null) {
    throw new RuleError(`${JSON.stringify(s)} is not a date-time with an offset`);
  }
  const offset = offsetSeconds(s, m[8]!);
  const [year, month, day, hour, minute, second] = [m[1], m[2], m[3], m[4], m[5], m[6]].map(Number) as [
    number,
    number,
    number,
    number,
    number,
    number,
  ];
  const nanos = (m[7] ?? "").padEnd(9, "0");
  if (!validDate(year, month, day) || hour > 23 || minute > 59 || second > 59) {
    throw new RuleError(`${JSON.stringify(s)} is not a valid date-time`);
  }
  const seconds = daysFromCivil(year, month, day) * 86400 + hour * 3600 + minute * 60 + second - offset;
  const days = Math.floor(seconds / 86400);
  const rest = seconds - days * 86400;
  const [utcYear, utcMonth, utcDay] = civilFromDays(days);
  if (utcYear < 0 || utcYear > 9999) {
    throw new RuleError(`${JSON.stringify(s)} falls outside the years 0000-9999`);
  }
  const fraction = nanos.replace(/0+$/, "");
  return (
    '"' +
    `${pad(utcYear, 4)}-${pad(utcMonth, 2)}-${pad(utcDay, 2)}T` +
    `${pad(Math.floor(rest / 3600), 2)}:${pad(Math.floor(rest / 60) % 60, 2)}:${pad(rest % 60, 2)}` +
    (fraction !== "" ? "." + fraction : "") +
    'Z"'
  );
}

function offsetSeconds(s: string, text: string): number {
  if (text === "Z") {
    return 0;
  }
  const sign = text[0] === "-" ? -1 : 1;
  const parts = text.slice(1).split(":").map(Number);
  const seconds = parts[0]! * 3600 + (parts[1] ?? 0) * 60 + (parts[2] ?? 0);
  if (seconds >= 24 * 3600) {
    throw new RuleError(`${JSON.stringify(s)}: offset ${text} is a day or more`);
  }
  return sign * seconds;
}

/**
 * Writes a calendar date as YYYY-MM-DD, the form Postgres renders. BC,
 * infinity and a date that does not exist are refused.
 */
function dateRule(value: JsonValue): string {
  const s = stringOf(value);
  const m = datePattern.exec(s);
  if (m === null) {
    throw new RuleError(`${JSON.stringify(s)} is not a date`);
  }
  if (!validDate(Number(m[1]), Number(m[2]), Number(m[3]))) {
    throw new RuleError(`${JSON.stringify(s)} is not a valid date`);
  }
  return writeJsonString(s);
}

/**
 * Writes a time of day as HH:MM:SS on a 24-hour clock, its fraction of a
 * second without trailing zeros and left out when zero, the form Postgres
 * renders. It also reads the other forms the scalar accepts: HH:MM, and a
 * 12-hour clock (2:30 pm, 12:05:09AM), each as Postgres reads it into a
 * time. A fraction a time column holds is kept. 24:00:00 is refused.
 */
function timeRule(value: JsonValue): string {
  const s = stringOf(value);
  let hour: number;
  let minute: number;
  let second = 0;
  let fraction = "";
  let m = timePattern.exec(s);
  if (m !== null) {
    hour = Number(m[1]);
    minute = Number(m[2]);
    fraction = m[4] ?? "";
    if (m[3] !== undefined) {
      second = Number(m[3]);
    }
  } else if ((m = clockPattern.exec(s)) !== null && Number(m[1]) >= 1 && Number(m[1]) <= 12) {
    hour = Number(m[1]) % 12;
    minute = Number(m[2]);
    if (m[3] !== undefined) {
      second = Number(m[3]);
    }
    if (m[4] === "p" || m[4] === "P") {
      hour += 12;
    }
  } else {
    throw new RuleError(`${JSON.stringify(s)} is not a time of day`);
  }
  if (hour > 23 || minute > 59 || second > 59) {
    throw new RuleError(`${JSON.stringify(s)} is not a time of day`);
  }
  fraction = fraction.replace(/0+$/, "");
  return `"${pad(hour, 2)}:${pad(minute, 2)}:${pad(second, 2)}${fraction !== "" ? "." + fraction : ""}"`;
}

/**
 * Writes a duration in the scalar core's canonical form: under a second,
 * the largest of ms, us and ns that holds it whole (500ms, 1500us); from a
 * second, hours and minutes when present, then seconds with their fraction
 * (1h30m0s, 1m30s, 1.5s); 0s for zero. It reads the interval text Postgres
 * renders with IntervalStyle postgres, the default (01:30:00,
 * 1 day 02:00:00, -1 days +02:00:00), where a day is 24 hours, and a
 * duration string such as Go's (1h30m0s, 1.5ms). Months and years, which
 * have no fixed length, are refused.
 */
function durationRule(value: JsonValue): string {
  const s = stringOf(value);
  let nanos: bigint;
  try {
    nanos = intervalNanos(s);
  } catch (err) {
    const parsed = parseDuration(s);
    if (parsed === undefined) {
      throw err;
    }
    nanos = parsed;
  }
  return '"' + formatDuration(nanos) + '"';
}

/** A duration's nanoseconds, from interval text or a duration string. */
export function durationNanos(s: string): bigint {
  try {
    return intervalNanos(s);
  } catch (err) {
    const parsed = parseDuration(s);
    if (parsed === undefined) {
      throw new CanonicalError("", "duration", (err as Error).message);
    }
    return parsed;
  }
}

const intervalTime = /^([+-]?)([0-9]+):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?$/;
const intervalCount = /^[+-]?[0-9]+$/;
// Go's strings.Fields splits on unicode.IsSpace.
const goSpace = /[\t\n\v\f\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]+/;
const secondNanos = 1_000_000_000n;
const minInt64 = -(2n ** 63n);
const maxInt64 = 2n ** 63n - 1n;

/** Reads Postgres interval text in its postgres style. */
function intervalNanos(s: string): bigint {
  const fields = s.split(goSpace).filter((field) => field !== "");
  if (fields.length === 0) {
    throw new RuleError(`${JSON.stringify(s)} is not a duration`);
  }
  let total = 0n;
  for (let i = 0; i < fields.length; i++) {
    const field = fields[i]!;
    const m = intervalTime.exec(field);
    if (m !== null) {
      let part = (BigInt(m[2]!) * 3600n + BigInt(m[3]!) * 60n + BigInt(m[4]!)) * secondNanos;
      part += BigInt((m[5] ?? "").padEnd(9, "0"));
      if (m[1] === "-") {
        part = -part;
      }
      total += part;
      continue;
    }
    if (!intervalCount.test(field) || i + 1 === fields.length) {
      throw new RuleError(`${JSON.stringify(s)} is not a Postgres interval`);
    }
    const count = BigInt(field.replace(/^\+/, ""));
    i++;
    switch (fields[i]) {
      case "day":
      case "days":
        total += count * 24n * 3600n * secondNanos;
        break;
      case "mon":
      case "mons":
      case "year":
      case "years":
        throw new RuleError(`${JSON.stringify(s)} has months or years, which have no fixed length`);
      default:
        throw new RuleError(`${JSON.stringify(s)} is not a Postgres interval`);
    }
  }
  if (total < minInt64 || total > maxInt64) {
    throw new RuleError(`${JSON.stringify(s)} is too long a duration`);
  }
  return total;
}

const unitNanos: Record<string, bigint> = {
  ns: 1n,
  us: 1000n,
  "\u00b5s": 1000n,
  "\u03bcs": 1000n,
  ms: 1_000_000n,
  s: secondNanos,
  m: 60n * secondNanos,
  h: 3600n * secondNanos,
};

const limit = 1n << 63n;

/**
 * Go's time.ParseDuration: a signed sequence of decimal numbers, each with
 * an optional fraction and a unit (ns, us, \u00b5s or \u03bcs, ms, s, m, h). Undefined for
 * a string it refuses.
 */
function parseDuration(text: string): bigint | undefined {
  let s = text;
  let neg = false;
  if (s !== "" && (s[0] === "-" || s[0] === "+")) {
    neg = s[0] === "-";
    s = s.slice(1);
  }
  if (s === "0") {
    return 0n;
  }
  if (s === "") {
    return undefined;
  }
  const isDigit = (c: string | undefined) => c !== undefined && c >= "0" && c <= "9";
  let d = 0n;
  while (s !== "") {
    if (!(s[0] === "." || isDigit(s[0]))) {
      return undefined;
    }
    // [0-9]*
    let i = 0;
    let v = 0n;
    for (; i < s.length && isDigit(s[i]); i++) {
      if (v > limit / 10n) {
        return undefined;
      }
      v = v * 10n + BigInt(s.charCodeAt(i) - 48);
      if (v > limit) {
        return undefined;
      }
    }
    const pre = i > 0;
    s = s.slice(i);
    // (\.[0-9]*)?
    let post = false;
    let f = 0n;
    let scale = 1;
    if (s !== "" && s[0] === ".") {
      s = s.slice(1);
      let j = 0;
      let overflow = false;
      for (; j < s.length && isDigit(s[j]); j++) {
        if (overflow) {
          continue;
        }
        if (f > (limit - 1n) / 10n) {
          overflow = true;
          continue;
        }
        const y = f * 10n + BigInt(s.charCodeAt(j) - 48);
        if (y > limit) {
          overflow = true;
          continue;
        }
        f = y;
        scale *= 10;
      }
      post = j > 0;
      s = s.slice(j);
    }
    if (!pre && !post) {
      return undefined;
    }
    // The unit: everything up to the next number.
    let u = 0;
    for (; u < s.length; u++) {
      if (s[u] === "." || isDigit(s[u])) {
        break;
      }
    }
    if (u === 0) {
      return undefined;
    }
    const unitText = s.slice(0, u);
    s = s.slice(u);
    const unit = Object.prototype.hasOwnProperty.call(unitNanos, unitText) ? unitNanos[unitText] : undefined;
    if (unit === undefined) {
      return undefined;
    }
    if (v > limit / unit) {
      return undefined;
    }
    v *= unit;
    if (f > 0n) {
      // As Go does, in float64: v >= 0 && (f*unit/scale) <= 3.6e+12.
      v += BigInt(Math.trunc(Number(f) * (Number(unit) / scale)));
      if (v > limit) {
        return undefined;
      }
    }
    d += v;
    if (d > limit) {
      return undefined;
    }
  }
  if (neg) {
    return -d;
  }
  if (d > limit - 1n) {
    return undefined;
  }
  return d;
}

/** The scalar core's canonical duration text of a number of nanoseconds. */
export function formatDuration(nanos: bigint): string {
  if (nanos === 0n) {
    return "0s";
  }
  let sign = "";
  let remaining = nanos;
  if (nanos < 0n) {
    sign = "-";
    remaining = -nanos;
  }
  const microsecond = 1000n;
  const millisecond = 1_000_000n;
  const minute = 60n * secondNanos;
  const hour = 60n * minute;
  if (remaining < secondNanos) {
    if (remaining % millisecond === 0n) {
      return sign + String(remaining / millisecond) + "ms";
    }
    if (remaining % microsecond === 0n) {
      return sign + String(remaining / microsecond) + "us";
    }
    return sign + String(remaining) + "ns";
  }
  const hours = remaining / hour;
  remaining %= hour;
  const minutes = remaining / minute;
  remaining %= minute;
  let seconds = String(remaining / secondNanos);
  const fraction = remaining % secondNanos;
  if (fraction !== 0n) {
    seconds += "." + String(fraction).padStart(9, "0").replace(/0+$/, "");
  }
  let out = sign;
  if (hours > 0n) {
    out += String(hours) + "h";
  }
  if (hours > 0n || minutes > 0n) {
    out += String(minutes) + "m";
  }
  return out + seconds + "s";
}
