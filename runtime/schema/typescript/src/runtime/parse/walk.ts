/**
 * Per-field parse walker. Walks each field of a TypeDef, applies defaults when
 * absent, coerces primitives, runs normalize and parse hooks, and recurses into
 * nested types / arrays. Strict mode rejects unknown fields with
 * {validator: "unknown_field"} keyed at the field path.
 */

import {
  addFieldError,
  addNestedErrors,
  hasErrors,
  newValidationErrors,
  setFieldErrors,
  type ValidationErrors,
} from '@psgen/scalar-lib/validation';
import type { FieldDef, ScalarDef, Schema, TypeDef, TypeRef } from '../validation/types';
import { coerceBool, coerceFloat, coerceInt } from './coerce';
import { applyDefault } from './defaults';
import type { ScalarNormalizeRegistry, ScalarParseRegistry } from './registry';

const BUILTIN_SCALARS = new Set(['String', 'Int', 'Float', 'Boolean', 'ID']);

type RefKind = 'scalar' | 'enum' | 'type' | 'input' | 'builtin';

export interface WalkContext {
  schema: Schema;
  parseRegistry: ScalarParseRegistry;
  normalizeRegistry: ScalarNormalizeRegistry;
  strict: boolean;
}

export function walkTypeDef(
  ctx: WalkContext,
  td: TypeDef,
  raw: Record<string, unknown>
): { data: Record<string, unknown>; errors: ValidationErrors } {
  const result: Record<string, unknown> = {};
  const errors = newValidationErrors();
  const known = new Set<string>();
  for (const field of td.fields) {
    const key = fieldKey(field);
    known.add(key);
    walkField(ctx, field, key, raw, result, errors);
  }
  if (ctx.strict) {
    for (const k of Object.keys(raw)) {
      if (known.has(k)) continue;
      addFieldError(errors, k, 'unknown_field', 'unknown field');
    }
  }
  return { data: result, errors };
}

function walkField(
  ctx: WalkContext,
  field: FieldDef,
  key: string,
  raw: Record<string, unknown>,
  result: Record<string, unknown>,
  errors: ValidationErrors
): void {
  const present = Object.prototype.hasOwnProperty.call(raw, key);
  const value = present ? raw[key] : undefined;

  if (!present) {
    if (field.defaultValue === null || field.defaultValue === undefined) return;
    if (field.typeRef.isArray) return;
    const kind = resolveRefKind(ctx.schema, field.typeRef);
    if (kind === 'type' || kind === 'input') return;
    const scalar = kind === 'scalar' ? scalarDefFor(ctx.schema, field.typeRef.name) : null;
    const [v, ok] = applyDefault(field, scalar);
    if (!ok) {
      addFieldError(errors, key, 'default', 'default value is not parseable for declared type');
      return;
    }
    result[key] = v;
    return;
  }

  if (value === null) {
    result[key] = null;
    return;
  }

  const kind = resolveRefKind(ctx.schema, field.typeRef);
  if (field.typeRef.isArray) {
    walkArrayField(ctx, field, key, kind, value, result, errors);
    return;
  }
  walkSingleField(ctx, field, key, kind, value, result, errors);
}

function walkSingleField(
  ctx: WalkContext,
  field: FieldDef,
  key: string,
  kind: RefKind,
  value: unknown,
  result: Record<string, unknown>,
  errors: ValidationErrors
): void {
  switch (kind) {
    case 'scalar': {
      const scalar = scalarDefFor(ctx.schema, field.typeRef.name);
      if (!scalar) {
        result[key] = value;
        return;
      }
      const [out, ok] = applyScalar(ctx, scalar, value);
      if (!ok) {
        addFieldError(errors, key, 'type', typeMismatchMessage(scalar));
        result[key] = value;
        return;
      }
      const finalValue = applyScalarParse(ctx, scalar, out, key, errors);
      result[key] = finalValue;
      return;
    }
    case 'enum':
      result[key] = value;
      return;
    case 'builtin': {
      const [out, ok] = applyBuiltin(field.typeRef.name, value, ctx.strict);
      if (!ok) {
        addFieldError(errors, key, 'type', `expected ${field.typeRef.name} value`);
        result[key] = value;
        return;
      }
      result[key] = out;
      return;
    }
    case 'type': {
      const td = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
      if (!td || !isObject(value)) {
        addFieldError(errors, key, 'type', 'expected object value');
        result[key] = value;
        return;
      }
      const nested = walkTypeDef(ctx, td, value);
      if (hasErrors(nested.errors)) {
        addNestedErrors(errors, key, nested.errors);
      }
      result[key] = nested.data;
      return;
    }
    case 'input': {
      const td = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
      if (!td || !isObject(value)) {
        addFieldError(errors, key, 'type', 'expected object value');
        result[key] = value;
        return;
      }
      const nested = walkTypeDef(ctx, td, value);
      if (hasErrors(nested.errors)) {
        addNestedErrors(errors, key, nested.errors);
      }
      result[key] = nested.data;
      return;
    }
  }
}

function walkArrayField(
  ctx: WalkContext,
  field: FieldDef,
  key: string,
  kind: RefKind,
  value: unknown,
  result: Record<string, unknown>,
  errors: ValidationErrors
): void {
  if (!Array.isArray(value)) {
    addFieldError(errors, key, 'type', 'expected array value');
    result[key] = value;
    return;
  }
  const out: unknown[] = new Array(value.length);
  for (let i = 0; i < value.length; i += 1) {
    const elem = value[i];
    const elemKey = `${key}[${i}]`;
    if (elem === null || elem === undefined) {
      out[i] = null;
      continue;
    }
    switch (kind) {
      case 'scalar': {
        const scalar = scalarDefFor(ctx.schema, field.typeRef.name);
        if (!scalar) {
          out[i] = elem;
          continue;
        }
        const [v, ok] = applyScalar(ctx, scalar, elem);
        if (!ok) {
          addFieldError(errors, elemKey, 'type', typeMismatchMessage(scalar));
          out[i] = elem;
          continue;
        }
        out[i] = applyScalarParse(ctx, scalar, v, elemKey, errors);
        continue;
      }
      case 'enum':
        out[i] = elem;
        continue;
      case 'builtin': {
        const [v, ok] = applyBuiltin(field.typeRef.name, elem, ctx.strict);
        if (!ok) {
          addFieldError(errors, elemKey, 'type', `expected ${field.typeRef.name} value`);
          out[i] = elem;
          continue;
        }
        out[i] = v;
        continue;
      }
      case 'type': {
        const td = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
        if (!td || !isObject(elem)) {
          addFieldError(errors, elemKey, 'type', 'expected object value');
          out[i] = elem;
          continue;
        }
        const nested = walkTypeDef(ctx, td, elem);
        if (hasErrors(nested.errors)) {
          addNestedErrors(errors, elemKey, nested.errors);
        }
        out[i] = nested.data;
        continue;
      }
      case 'input': {
        const td = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
        if (!td || !isObject(elem)) {
          addFieldError(errors, elemKey, 'type', 'expected object value');
          out[i] = elem;
          continue;
        }
        const nested = walkTypeDef(ctx, td, elem);
        if (hasErrors(nested.errors)) {
          addNestedErrors(errors, elemKey, nested.errors);
        }
        out[i] = nested.data;
        continue;
      }
    }
  }
  result[key] = out;
}

function applyScalar(
  ctx: WalkContext,
  scalar: ScalarDef,
  value: unknown
): [unknown, boolean] {
  switch (scalar.primitive) {
    case 'Int':
      return coerceInt(value, ctx.strict);
    case 'Float':
      return coerceFloat(value, ctx.strict);
    case 'Boolean':
      return coerceBool(value, ctx.strict);
    case 'String':
    case '': {
      if (typeof value !== 'string') return [value, false];
      let s = value;
      if (scalar.hasCustomNormalize) {
        const fn = ctx.normalizeRegistry.get(scalar.name);
        if (fn) s = fn(s);
      }
      return [s, true];
    }
    default:
      return [value, true];
  }
}

function applyScalarParse(
  ctx: WalkContext,
  scalar: ScalarDef,
  value: unknown,
  key: string,
  errors: ValidationErrors
): unknown {
  if (!scalar.hasCustomParse) return value;
  const fn = ctx.parseRegistry.get(scalar.name);
  if (!fn) return value;
  const input = typeof value === 'string' ? value : String(value);
  const [parsed, errs] = fn(input);
  if (errs.length > 0) {
    setFieldErrors(errors, key, errs);
    return value;
  }
  if (scalar.primitive === 'Int') {
    const [coerced, ok] = coerceInt(parsed, false);
    return ok ? coerced : value;
  }
  return parsed;
}

function applyBuiltin(name: string, value: unknown, strict: boolean): [unknown, boolean] {
  switch (name) {
    case 'Int':
      return coerceInt(value, strict);
    case 'Float':
      return coerceFloat(value, strict);
    case 'Boolean':
      return coerceBool(value, strict);
    case 'String':
    case 'ID':
      return typeof value === 'string' ? [value, true] : [value, false];
    default:
      return [value, true];
  }
}

function typeMismatchMessage(scalar: ScalarDef): string {
  switch (scalar.primitive) {
    case 'Int':
      return 'expected integer value';
    case 'Float':
      return 'expected numeric value';
    case 'Boolean':
      return 'expected boolean value';
    case 'String':
    case '':
      return 'expected string value';
    default:
      return 'type mismatch';
  }
}

function resolveRefKind(schema: Schema, ref: TypeRef): RefKind {
  if (schema.scalars && schema.scalars[ref.name]) return 'scalar';
  if (schema.enums && schema.enums[ref.name]) return 'enum';
  if (schema.types && schema.types[ref.name]) return 'type';
  if (schema.inputs && schema.inputs[ref.name]) return 'input';
  if (BUILTIN_SCALARS.has(ref.name)) return 'builtin';
  return 'builtin';
}

function scalarDefFor(schema: Schema, name: string): ScalarDef | null {
  if (!schema.scalars) return null;
  return schema.scalars[name] || null;
}

export function fieldKey(field: FieldDef): string {
  return field.jsonKey || field.name;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
