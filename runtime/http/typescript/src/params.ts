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
*/

export type ParamKind = 'string' | 'integer' | 'number' | 'boolean' | 'uuid' | 'datetime' | 'enum';

export type ParamLocation = 'path' | 'query' | 'body';

export interface ParamSpec {
  /** Wire name (the schema argument name). */
  readonly name: string;
  readonly kind: ParamKind;
  readonly required: boolean;
  readonly isArray?: boolean;
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
}

/** Raw values by wire name: every occurrence of a query key, one path capture, one body field. */
export type ParamSource = (name: string) => readonly string[] | undefined;

function refuse(
  location: ParamLocation,
  spec: ParamSpec,
  reason: string,
  errors?: ValidationError[] | null
): never {
  throw badRequest(`Invalid ${location} parameter ${spec.name}: ${reason}`, {
    details: {
      location,
      parameter: spec.name,
      reason,
      ...(errors && errors.length > 0 ? { errors } : {}),
    },
  });
}

function decodeScalar(location: ParamLocation, spec: ParamSpec, raw: string): unknown {
  switch (spec.kind) {
    case 'integer': {
      if (!/^[+-]?\d+$/u.test(raw.trim())) refuse(location, spec, 'expected an integer');
      const value = Number(raw);
      if (!Number.isSafeInteger(value)) refuse(location, spec, 'integer out of range');
      return checkNumber(location, spec, value);
    }
    case 'number': {
      const value = Number(raw.trim());
      if (raw.trim() === '' || !Number.isFinite(value)) refuse(location, spec, 'expected a number');
      return checkNumber(location, spec, value);
    }
    case 'boolean': {
      const value = raw.trim().toLowerCase();
      if (value === 'true' || value === '1') return true;
      if (value === 'false' || value === '0') return false;
      return refuse(location, spec, 'expected true or false');
    }
    case 'uuid': {
      const value = raw.trim();
      const parsed = parseIdentityUUID(value);
      if (parsed === null) {
        const [, errors] = validateIdentityUUID(value);
        return refuse(location, spec, 'expected a UUID', errors);
      }
      return parsed;
    }
    case 'datetime': {
      const value = raw.trim();
      const parsed = parseTemporalDateTime(value);
      if (parsed === null) {
        const [, errors] = validateTemporalDateTime(value);
        return refuse(location, spec, 'expected an RFC 3339 timestamp', errors);
      }
      return parsed;
    }
    case 'enum': {
      if (!spec.enumValues?.includes(raw)) {
        refuse(location, spec, `expected one of ${(spec.enumValues ?? []).join(', ')}`);
      }
      return raw;
    }
    case 'string':
    default:
      return checkString(location, spec, raw);
  }
}

function checkNumber(location: ParamLocation, spec: ParamSpec, value: number): number {
  if (spec.min !== undefined && value < spec.min) refuse(location, spec, `must be at least ${spec.min}`);
  if (spec.max !== undefined && value > spec.max) refuse(location, spec, `must be at most ${spec.max}`);
  return value;
}

function checkString(location: ParamLocation, spec: ParamSpec, value: string): string {
  if (spec.minLength !== undefined && value.length < spec.minLength) {
    refuse(location, spec, `must be at least ${spec.minLength} characters`);
  }
  if (spec.maxLength !== undefined && value.length > spec.maxLength) {
    refuse(location, spec, `must be at most ${spec.maxLength} characters`);
  }
  if (spec.pattern && !new RegExp(spec.pattern, 'u').test(value)) {
    refuse(location, spec, 'does not match the required pattern');
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
