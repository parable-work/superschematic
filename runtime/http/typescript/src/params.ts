import {
  parseIdentityUUID,
  parseTemporalDateTime,
  validateIdentityUUID,
  validateTemporalDateTime,
} from 'superscalar/scalars';
import type { ValidationError } from 'superscalar/validation';
import { badRequest } from './problem';

/*
Path, query, and scalar body parameters. The generated router carries one
ParamSpec per declared argument (the schema's scalar kind and Validate<>
bounds); this module turns the raw strings a request carries into the scalar
values the implementation signature promises, or refuses the request with 400
and a field-level detail. Semantic kinds go through the scalar library (the
same parse functions the generated Go router calls): Identity.UUID returns
the library's canonical form, Temporal.DateTime is RFC 3339. Primitive kinds
stay local and match the Go Param flags: integer, float, boolean, string,
enum.

A list of lists (T[][]) only travels in a JSON body, so decodeListOfLists
reads it from JSON values instead of strings; its element may also be an
object type, parsed by the generated strict parser the spec carries.
*/

/** 'object' is an object element type of a list of lists, parsed by ParamSpec.parse; a string never is one. */
export type ParamKind = 'string' | 'integer' | 'number' | 'boolean' | 'uuid' | 'datetime' | 'enum' | 'object';

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
  /** Kind 'object': the generated strict parser of the element type (parse<T>FromJSON over parse<T>Json). */
  readonly parse?: (value: unknown) => unknown;
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

function checkNumber(location: ParamLocation, spec: ParamSpec, value: number, path?: string): number {
  if (spec.min !== undefined && value < spec.min) refuse(location, spec, `must be at least ${spec.min}`, null, path);
  if (spec.max !== undefined && value > spec.max) refuse(location, spec, `must be at most ${spec.max}`, null, path);
  return value;
}

function checkString(location: ParamLocation, spec: ParamSpec, value: string, path?: string): string {
  if (spec.minLength !== undefined && value.length < spec.minLength) {
    refuse(location, spec, `must be at least ${spec.minLength} characters`, null, path);
  }
  if (spec.maxLength !== undefined && value.length > spec.maxLength) {
    refuse(location, spec, `must be at most ${spec.maxLength} characters`, null, path);
  }
  if (spec.pattern && !new RegExp(spec.pattern, 'u').test(value)) {
    refuse(location, spec, 'does not match the required pattern', null, path);
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

/** Refuses a value inside a list of lists with one list-rule error, reported at `path`. */
function refuseAt(location: ParamLocation, spec: ParamSpec, path: string, validator: string, message: string): never {
  return refuse(location, spec, message, [{ validator, message }], path);
}

/** The JSON type each element kind arrives as, and the refusal when it does not. */
function expectedJsonType(kind: ParamKind): { type: 'string' | 'number' | 'boolean'; message: string } {
  switch (kind) {
    case 'integer':
      return { type: 'number', message: 'expected an integer' };
    case 'number':
      return { type: 'number', message: 'expected a number' };
    case 'boolean':
      return { type: 'boolean', message: 'expected a boolean' };
    default:
      return { type: 'string', message: 'expected a string' };
  }
}

/** One innermost element of a list of lists: never null, then the same checks as an element of T[]. */
function decodeListElement(location: ParamLocation, spec: ParamSpec, path: string, item: unknown): unknown {
  if (item === null || item === undefined) refuseAt(location, spec, path, 'required', 'required field');
  if (spec.kind === 'object') {
    if (typeof item !== 'object' || Array.isArray(item)) refuseAt(location, spec, path, 'type', 'expected an object');
    if (!spec.parse) throw new Error(`ParamSpec ${spec.name} has kind 'object' but no parse function`);
    try {
      return spec.parse(item);
    } catch {
      // The parser's message names generated functions; like the input body,
      // the refusal says what failed and where, not how.
      return refuse(location, spec, 'does not match the declared type', null, path);
    }
  }
  const expected = expectedJsonType(spec.kind);
  if (typeof item !== expected.type) refuseAt(location, spec, path, 'type', expected.message);
  return decodeScalar(location, spec, String(item), path);
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
  if (value === undefined || value === null) {
    if (spec.required) refuse(location, spec, 'required');
    return undefined;
  }
  if (!Array.isArray(value)) {
    return refuse(location, spec, 'expected an array', [{ validator: 'type', message: 'expected an array' }]);
  }
  if (spec.listMin !== undefined && value.length < spec.listMin) refuse(location, spec, `expected at least ${spec.listMin} values`);
  if (spec.listMax !== undefined && value.length > spec.listMax) refuse(location, spec, `expected at most ${spec.listMax} values`);
  return value.map((row: unknown, i) => {
    const rowPath = `${spec.name}[${i}]`;
    if (row === null || row === undefined) refuseAt(location, spec, rowPath, 'required', 'required field');
    if (!Array.isArray(row)) refuseAt(location, spec, rowPath, 'type', 'expected an array');
    return row.map((item: unknown, j) => decodeListElement(location, spec, `${rowPath}[${j}]`, item));
  });
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
