import {
  parseIdentityUUID,
  parseTemporalDateTime,
  validateIdentityUUID,
  validateTemporalDateTime,
} from 'superscalar/scalars';
import type { ValidationError } from 'superscalar/validation';
import { badRequest } from './problem';

/*
Path, query, and body parameters. The generated router carries one ParamSpec
per declared argument (the schema's scalar kind, Validate<> bounds and, for a
scalar type, the scalar's own constraints); this module turns what a request
carries into the values the implementation signature promises, or refuses the
request with 400 and a field-level detail. Semantic kinds go through the
scalar library (the same parse functions the generated Go router calls):
Identity.UUID returns the library's canonical form, Temporal.DateTime is RFC
3339. Primitive kinds stay local and match the Go Param flags: integer,
float, boolean, string, enum.

Path and query parameters arrive as strings: decodeParam reads them, and a
query list accepts repeated keys and comma-separated values. Every body
parameter arrives as a JSON value, so decodeJsonParam reads it as one: a
number is not accepted for a string nor a string for a number, a list is a
JSON array whose elements are never split or dropped, and an object value
is parsed by the generated strict parser the spec carries.
*/

/**
 * 'object' is an object type of a body parameter (T, T[] or T[][]), parsed
 * by ParamSpec.parse; 'json' is a body parameter of a JSON-valued scalar
 * (Generic.JSON), any JSON value but null. Neither is read from a string.
 */
export type ParamKind = 'string' | 'integer' | 'number' | 'boolean' | 'uuid' | 'datetime' | 'enum' | 'object' | 'json';

export type ParamLocation = 'path' | 'query' | 'body';

export interface ParamSpec {
  /** Wire name (the schema argument name). */
  readonly name: string;
  readonly kind: ParamKind;
  readonly required: boolean;
  readonly isArray?: boolean;
  /** A list of lists (T[][]), always a body parameter; isArray is also set. See decodeListOfLists. */
  readonly isArrayOfArrays?: boolean;
  /** Serialized member values when kind is 'enum'. */
  readonly enumValues?: readonly string[];
  /** Applied when the parameter is absent. */
  readonly defaultValue?: string;
  readonly min?: number;
  readonly max?: number;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly listMin?: number;
  readonly listMax?: number;
  readonly pattern?: string;
  /** Kind 'object': the generated strict parser of the type (parse<T>FromJSON over parse<T>Json). */
  readonly parse?: (value: unknown) => unknown;
  /** The constraints of the parameter's scalar type, checked on every value before the argument's own. */
  readonly scalar?: ScalarConstraints;
}

/**
 * A scalar type's own constraints (its IR lengths, pattern and range), as
 * the schema runtimes check them: lengths count code points, the pattern is
 * a JavaScript regular expression without flags. A value that breaks one is
 * refused with one error named by that rule (`minLength`, `maxLength`,
 * `pattern`, `min`, `max`).
 */
export interface ScalarConstraints {
  /** Canonical scalar name, for the refusal (`Network.Url`). */
  readonly name: string;
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly pattern?: string;
  readonly min?: number;
  readonly max?: number;
}

/** Raw values by wire name: every occurrence of a query key, one path capture, one body field. */
export type ParamSource = (name: string) => readonly string[] | undefined;

/**
 * Refuses the request with 400. `path` names the refused value inside a list
 * of lists (`rows[2]`, `rows[2][5]`); the detail then carries it beside the
 * parameter name.
 */
function refuse(
  location: ParamLocation,
  spec: ParamSpec,
  reason: string,
  errors?: ValidationError[] | null,
  path?: string
): never {
  throw badRequest(`Invalid ${location} parameter ${path ?? spec.name}: ${reason}`, {
    details: {
      location,
      parameter: spec.name,
      ...(path !== undefined ? { path } : {}),
      reason,
      ...(errors && errors.length > 0 ? { errors } : {}),
    },
  });
}

function decodeScalar(location: ParamLocation, spec: ParamSpec, raw: string, path?: string): unknown {
  switch (spec.kind) {
    case 'integer': {
      if (!/^[+-]?\d+$/u.test(raw.trim())) refuse(location, spec, 'expected an integer', null, path);
      const value = Number(raw);
      if (!Number.isSafeInteger(value)) refuse(location, spec, 'integer out of range', null, path);
      return checkNumber(location, spec, value, path);
    }
    case 'number': {
      const value = Number(raw.trim());
      if (raw.trim() === '' || !Number.isFinite(value)) refuse(location, spec, 'expected a number', null, path);
      return checkNumber(location, spec, value, path);
    }
    case 'boolean': {
      const value = raw.trim().toLowerCase();
      if (value === 'true' || value === '1') return true;
      if (value === 'false' || value === '0') return false;
      return refuse(location, spec, 'expected true or false', null, path);
    }
    case 'uuid': {
      const value = raw.trim();
      const parsed = parseIdentityUUID(value);
      if (parsed === null) {
        const [, errors] = validateIdentityUUID(value);
        return refuse(location, spec, 'expected a UUID', errors, path);
      }
      return parsed;
    }
    case 'datetime': {
      const value = raw.trim();
      const parsed = parseTemporalDateTime(value);
      if (parsed === null) {
        const [, errors] = validateTemporalDateTime(value);
        return refuse(location, spec, 'expected an RFC 3339 timestamp', errors, path);
      }
      return parsed;
    }
    case 'enum': {
      if (!spec.enumValues?.includes(raw)) {
        refuse(location, spec, `expected one of ${(spec.enumValues ?? []).join(', ')}`, null, path);
      }
      return raw;
    }
    case 'object':
      return refuse(location, spec, 'expected an object', null, path);
    case 'string':
    default:
      return checkString(location, spec, raw, path);
  }
}

/**
 * Refuses one value with one error named by the rule it breaks (`required`,
 * `type`, `min`, `maxLength`, `pattern`, ...), reported at `path` when it
 * sits inside a list.
 */
function refuseAt(location: ParamLocation, spec: ParamSpec, path: string | undefined, validator: string, message: string): never {
  return refuse(location, spec, message, [{ validator, message }], path);
}

/** A scalar's range, then the argument's own bounds. */
function checkNumber(location: ParamLocation, spec: ParamSpec, value: number, path?: string): number {
  const scalar = spec.scalar;
  if (scalar?.min !== undefined && value < scalar.min) refuseAt(location, spec, path, 'min', `must be at least ${scalar.min}`);
  if (scalar?.max !== undefined && value > scalar.max) refuseAt(location, spec, path, 'max', `must be at most ${scalar.max}`);
  if (spec.min !== undefined && value < spec.min) refuseAt(location, spec, path, 'min', `must be at least ${spec.min}`);
  if (spec.max !== undefined && value > spec.max) refuseAt(location, spec, path, 'max', `must be at most ${spec.max}`);
  return value;
}

/** Whether a scalar's pattern accepts a value; a pattern JavaScript cannot compile accepts nothing, as in the schema runtime. */
function matchesScalarPattern(pattern: string, value: string): boolean {
  try {
    return new RegExp(pattern).test(value);
  } catch {
    return false;
  }
}

/** A scalar's lengths and pattern, then the argument's own constraints. */
function checkString(location: ParamLocation, spec: ParamSpec, value: string, path?: string): string {
  const scalar = spec.scalar;
  if (scalar) {
    // Code points, not UTF-16 units, as the schema runtimes count.
    const length = [...value].length;
    if (scalar.minLength !== undefined && length < scalar.minLength) {
      refuseAt(location, spec, path, 'minLength', `must be at least ${scalar.minLength} characters`);
    }
    if (scalar.maxLength !== undefined && length > scalar.maxLength) {
      refuseAt(location, spec, path, 'maxLength', `must be at most ${scalar.maxLength} characters`);
    }
    if (scalar.pattern && !matchesScalarPattern(scalar.pattern, value)) {
      refuseAt(location, spec, path, 'pattern', `is not a valid ${scalar.name}`);
    }
  }
  if (spec.minLength !== undefined && value.length < spec.minLength) {
    refuseAt(location, spec, path, 'minLength', `must be at least ${spec.minLength} characters`);
  }
  if (spec.maxLength !== undefined && value.length > spec.maxLength) {
    refuseAt(location, spec, path, 'maxLength', `must be at most ${spec.maxLength} characters`);
  }
  if (spec.pattern && !new RegExp(spec.pattern, 'u').test(value)) {
    refuseAt(location, spec, path, 'pattern', 'does not match the required pattern');
  }
  return value;
}

/**
 * Decodes one parameter. `raw` carries every occurrence on the wire; an array
 * parameter accepts repeated keys and comma-separated values (the Go and Rust
 * routers read both), a scalar parameter takes the first occurrence.
 */
export function decodeParam(location: ParamLocation, spec: ParamSpec, raw: readonly string[] | undefined): unknown {
  let values = raw ?? [];
  if (values.length === 0 && spec.defaultValue !== undefined) {
    values = [spec.defaultValue];
  }
  if (spec.isArray) {
    const items = values.flatMap(value => value.split(',')).map(value => value.trim()).filter(value => value !== '');
    if (items.length === 0) {
      if (spec.required) refuse(location, spec, 'required');
      return undefined;
    }
    if (spec.listMin !== undefined && items.length < spec.listMin) refuse(location, spec, `expected at least ${spec.listMin} values`);
    if (spec.listMax !== undefined && items.length > spec.listMax) refuse(location, spec, `expected at most ${spec.listMax} values`);
    return items.map(item => decodeScalar(location, spec, item));
  }
  const first = values[0];
  if (first === undefined || (first === '' && spec.kind !== 'string')) {
    if (spec.required) refuse(location, spec, 'required');
    return undefined;
  }
  return decodeScalar(location, spec, first);
}

/**
 * One non-null JSON value, checked as its kind: an object goes through the
 * spec's parser, a JSON-valued scalar is taken as it is, and every other
 * kind must arrive as its JSON type (`type` otherwise) and then passes the
 * checks of a path or query value of that kind.
 */
function decodeJsonValue(location: ParamLocation, spec: ParamSpec, item: unknown, path?: string): unknown {
  switch (spec.kind) {
    case 'object': {
      if (typeof item !== 'object' || item === null || Array.isArray(item)) refuseAt(location, spec, path, 'type', 'expected an object');
      if (!spec.parse) throw new Error(`ParamSpec ${spec.name} has kind 'object' but no parse function`);
      try {
        return spec.parse(item);
      } catch {
        // The parser's message names generated functions; like the input body,
        // the refusal says what failed and where, not how.
        return refuse(location, spec, 'does not match the declared type', null, path);
      }
    }
    case 'json':
      return item;
    case 'integer':
      if (typeof item !== 'number' || !Number.isInteger(item)) refuseAt(location, spec, path, 'type', 'expected an integer');
      if (!Number.isSafeInteger(item)) refuse(location, spec, 'integer out of range', null, path);
      return checkNumber(location, spec, item, path);
    case 'number':
      if (typeof item !== 'number') refuseAt(location, spec, path, 'type', 'expected a number');
      return checkNumber(location, spec, item, path);
    case 'boolean':
      if (typeof item !== 'boolean') refuseAt(location, spec, path, 'type', 'expected a boolean');
      return item;
    default:
      if (typeof item !== 'string') refuseAt(location, spec, path, 'type', 'expected a string');
      return decodeScalar(location, spec, item, path);
  }
}

/** One list element: never null, then the checks of its kind, reported at `path`. */
function decodeListElement(location: ParamLocation, spec: ParamSpec, path: string, item: unknown): unknown {
  if (item === null || item === undefined) refuseAt(location, spec, path, 'required', 'required field');
  return decodeJsonValue(location, spec, item, path);
}

/** The outer list is the parameter: absent or null is refused when required, otherwise it is an array within listMin and listMax. */
function outerList(location: ParamLocation, spec: ParamSpec, value: unknown): unknown[] | undefined {
  if (value === undefined || value === null) {
    if (spec.required) refuse(location, spec, 'required');
    return undefined;
  }
  if (!Array.isArray(value)) {
    return refuse(location, spec, 'expected an array', [{ validator: 'type', message: 'expected an array' }]);
  }
  if (spec.listMin !== undefined && value.length < spec.listMin) refuse(location, spec, `expected at least ${spec.listMin} values`);
  if (spec.listMax !== undefined && value.length > spec.listMax) refuse(location, spec, `expected at most ${spec.listMax} values`);
  return value;
}

/**
 * Decodes a list of lists (T[][]) from its JSON body value, with the list
 * rules the schema runtimes and the Go router share:
 *
 * - the outer list is the parameter: absent or null is refused when
 *   required, an empty list is valid, listMin and listMax bound it;
 * - an inner list is never null (`required`, "required field", at name[i])
 *   and must be an array (`type`, "expected an array", at name[i]); an
 *   empty one is valid;
 * - every innermost element is never null and passes the checks of a T[]
 *   element, reported at name[i][j]. Elements arrive as JSON values, so a
 *   number is not accepted for a string, nor a string for a number.
 *
 * The first failure refuses the request; its detail carries `path`.
 */
export function decodeListOfLists(location: ParamLocation, spec: ParamSpec, value: unknown): unknown[][] | undefined {
  return outerList(location, spec, value)?.map((row: unknown, i) => {
    const rowPath = `${spec.name}[${i}]`;
    if (row === null || row === undefined) refuseAt(location, spec, rowPath, 'required', 'required field');
    if (!Array.isArray(row)) refuseAt(location, spec, rowPath, 'type', 'expected an array');
    return row.map((item: unknown, j) => decodeListElement(location, spec, `${rowPath}[${j}]`, item));
  });
}

/**
 * Decodes a body parameter from its JSON value. The Hono adapter reads every
 * body parameter this way; only path and query values are strings.
 *
 * - T[][] is decodeListOfLists;
 * - T[] follows the same list rules one level down: the list is the
 *   parameter (required means present, an empty list is valid, listMin and
 *   listMax bound it), and each element is never null (`required`,
 *   "required field") and passes the checks of its kind, both at name[i];
 * - T is refused when required and absent or null, and otherwise passes
 *   the checks of its kind.
 *
 * A value must arrive as the JSON type of its kind: a string for a string,
 * enum, UUID or timestamp, a number for a number or an integer, a boolean
 * for a boolean (`type`, "expected a string", ...). An object value must be
 * a JSON object (`type`, "expected an object") and pass the spec's parser
 * ("does not match the declared type"). A 'json' value is any JSON value
 * but null. A parameter of any other kind with a default that is absent or
 * null decodes the default as a path or query value would.
 */
export function decodeJsonParam(location: ParamLocation, spec: ParamSpec, value: unknown): unknown {
  if ((value === undefined || value === null) && spec.defaultValue !== undefined && !spec.isArrayOfArrays && spec.kind !== 'object') {
    return decodeParam(location, spec, undefined);
  }
  if (spec.isArrayOfArrays) return decodeListOfLists(location, spec, value);
  if (spec.isArray) {
    return outerList(location, spec, value)?.map((item: unknown, i) => decodeListElement(location, spec, `${spec.name}[${i}]`, item));
  }
  if (value === undefined || value === null) {
    if (spec.required) refuse(location, spec, 'required');
    return undefined;
  }
  return decodeJsonValue(location, spec, value);
}

/** Decodes every spec from a source; the result is keyed by wire name. */
export function decodeParams(
  location: ParamLocation,
  specs: readonly ParamSpec[],
  source: ParamSource
): Record<string, unknown> {
  const decoded: Record<string, unknown> = {};
  for (const spec of specs) {
    const value = decodeParam(location, spec, source(spec.name));
    if (value !== undefined) decoded[spec.name] = value;
  }
  return decoded;
}
