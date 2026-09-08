/**
 * Schema-aware serialization. Produces a sanitized object (typeToMap /
 * inputToMap) or canonical JSON (marshalType / marshalInput) honouring schema
 * field order.
 *
 * Semantics:
 *   - Unknown fields are dropped in lenient mode; reported as
 *     {validator: "unknown_field"} when opts.strict.
 *   - Nil/missing arrays become `[]` in JSON output (not `null`).
 *   - opts.maskSecrets composes with the mask package to zero @secret fields
 *     before serializing.
 *   - JSON keys are emitted in TypeDef declaration order so output is
 *     byte-stable across calls.
 */

import {
  addFieldError,
  addNestedErrors,
  hasErrors,
  newValidationErrors,
  type ValidationErrors,
} from 'superscalar/validation';
import type { FieldDef, Schema, SchemaInput, TypeDef, TypeRef } from '../validation/types';
import { resolveSchema } from '../validation/validate';
import { maskInput, maskType } from '../mask';

const BUILTIN_SCALARS = new Set(['String', 'Int', 'Float', 'Boolean', 'ID']);

type RefKind = 'scalar' | 'enum' | 'type' | 'input' | 'builtin';

export interface SerializeOptions {
  strict?: boolean;
  maskSecrets?: boolean;
}

export interface SerializeResult {
  data: Record<string, unknown>;
  errors: ValidationErrors;
}

export interface MarshalResult {
  json: string;
  errors: ValidationErrors;
}

interface Ctx {
  schema: Schema;
  strict: boolean;
  maskSecrets: boolean;
}

function ctxFromOptions(schema: Schema, options?: SerializeOptions): Ctx {
  return {
    schema,
    strict: options?.strict === true,
    maskSecrets: options?.maskSecrets === true,
  };
}

function unknownTypeError(name: string, kind: 'type' | 'input'): SerializeResult {
  const errs = newValidationErrors();
  addFieldError(errs, '', 'type', `unknown ${kind} "${name}"`);
  return { data: {}, errors: errs };
}

export function typeToMap(
  schema: SchemaInput,
  typeName: string,
  data: Record<string, unknown> | null | undefined,
  options?: SerializeOptions
): SerializeResult {
  const resolved = resolveSchema(schema);
  const td = resolved.types ? resolved.types[typeName] : undefined;
  if (!td) return unknownTypeError(typeName, 'type');
  return serialize(ctxFromOptions(resolved, options), td, data ?? {}, 'type', typeName);
}

export function inputToMap(
  schema: SchemaInput,
  inputName: string,
  data: Record<string, unknown> | null | undefined,
  options?: SerializeOptions
): SerializeResult {
  const resolved = resolveSchema(schema);
  const td = resolved.inputs ? resolved.inputs[inputName] : undefined;
  if (!td) return unknownTypeError(inputName, 'input');
  return serialize(ctxFromOptions(resolved, options), td, data ?? {}, 'input', inputName);
}

export function marshalType(
  schema: SchemaInput,
  typeName: string,
  data: Record<string, unknown> | null | undefined,
  options?: SerializeOptions
): MarshalResult {
  const resolved = resolveSchema(schema);
  const td = resolved.types ? resolved.types[typeName] : undefined;
  if (!td) {
    const errs = newValidationErrors();
    addFieldError(errs, '', 'type', `unknown type "${typeName}"`);
    return { json: '', errors: errs };
  }
  return marshal(ctxFromOptions(resolved, options), td, data ?? {}, 'type', typeName);
}

export function marshalInput(
  schema: SchemaInput,
  inputName: string,
  data: Record<string, unknown> | null | undefined,
  options?: SerializeOptions
): MarshalResult {
  const resolved = resolveSchema(schema);
  const td = resolved.inputs ? resolved.inputs[inputName] : undefined;
  if (!td) {
    const errs = newValidationErrors();
    addFieldError(errs, '', 'type', `unknown input "${inputName}"`);
    return { json: '', errors: errs };
  }
  return marshal(ctxFromOptions(resolved, options), td, data ?? {}, 'input', inputName);
}

function serialize(
  ctx: Ctx,
  td: TypeDef,
  data: Record<string, unknown>,
  kind: 'type' | 'input',
  name: string
): SerializeResult {
  const errors = newValidationErrors();
  const src = applyMask(ctx, data, kind, name);
  const out = walkTypeDef(ctx, td, src, errors);
  return { data: out, errors };
}

function marshal(
  ctx: Ctx,
  td: TypeDef,
  data: Record<string, unknown>,
  kind: 'type' | 'input',
  name: string
): MarshalResult {
  const { data: sanitized, errors } = serialize(ctx, td, data, kind, name);
  if (hasErrors(errors)) {
    return { json: '', errors };
  }
  try {
    const json = writeObject(ctx, td, sanitized);
    return { json, errors };
  } catch (err) {
    addFieldError(errors, '', 'json', err instanceof Error ? err.message : String(err));
    return { json: '', errors };
  }
}

function applyMask(
  ctx: Ctx,
  data: Record<string, unknown>,
  kind: 'type' | 'input',
  name: string
): Record<string, unknown> {
  if (!ctx.maskSecrets) return data;
  const masked = kind === 'input' ? maskInput(ctx.schema, name, data) : maskType(ctx.schema, name, data);
  return masked ?? data;
}

function walkTypeDef(
  ctx: Ctx,
  td: TypeDef,
  data: Record<string, unknown>,
  errors: ValidationErrors
): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const known = new Set<string>();
  for (const field of td.fields) {
    const key = fieldKey(field);
    known.add(key);
    if (!Object.prototype.hasOwnProperty.call(data, key)) continue;
    out[key] = walkField(ctx, field, data[key], errors, key);
  }
  if (ctx.strict) {
    for (const k of Object.keys(data)) {
      if (known.has(k)) continue;
      addFieldError(errors, k, 'unknown_field', 'unknown field');
    }
  }
  return out;
}

function walkField(
  ctx: Ctx,
  field: FieldDef,
  value: unknown,
  errors: ValidationErrors,
  path: string
): unknown {
  if (value === null || value === undefined) {
    return field.typeRef.isArray ? [] : null;
  }
  const kind = resolveRefKind(ctx.schema, field.typeRef);

  if (field.typeRef.isArray) {
    if (!Array.isArray(value)) return value;
    const out: unknown[] = new Array(value.length);
    for (let i = 0; i < value.length; i += 1) {
      const elemPath = `${path}[${i}]`;
      out[i] = walkElem(ctx, field, kind, value[i], errors, elemPath);
    }
    return out;
  }

  switch (kind) {
    case 'type': {
      const nestedTd = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
      if (!nestedTd || !isObject(value)) return value;
      const nestedErrs = newValidationErrors();
      const result = walkTypeDef(ctx, nestedTd, value, nestedErrs);
      if (hasErrors(nestedErrs)) addNestedErrors(errors, path, nestedErrs);
      return result;
    }
    case 'input': {
      const nestedTd = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
      if (!nestedTd || !isObject(value)) return value;
      const nestedErrs = newValidationErrors();
      const result = walkTypeDef(ctx, nestedTd, value, nestedErrs);
      if (hasErrors(nestedErrs)) addNestedErrors(errors, path, nestedErrs);
      return result;
    }
    default:
      return value;
  }
}

function walkElem(
  ctx: Ctx,
  field: FieldDef,
  kind: RefKind,
  elem: unknown,
  errors: ValidationErrors,
  path: string
): unknown {
  if (elem === null || elem === undefined) return null;
  if (kind === 'type') {
    const td = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
    if (!td || !isObject(elem)) return elem;
    const nestedErrs = newValidationErrors();
    const result = walkTypeDef(ctx, td, elem, nestedErrs);
    if (hasErrors(nestedErrs)) addNestedErrors(errors, path, nestedErrs);
    return result;
  }
  if (kind === 'input') {
    const td = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
    if (!td || !isObject(elem)) return elem;
    const nestedErrs = newValidationErrors();
    const result = walkTypeDef(ctx, td, elem, nestedErrs);
    if (hasErrors(nestedErrs)) addNestedErrors(errors, path, nestedErrs);
    return result;
  }
  return elem;
}

// writeObject emits JSON with keys in TypeDef declaration order, then any
// remaining keys (shouldn't happen in lenient mode since walkTypeDef drops
// unknown fields, but kept as a defensive fallback).
function writeObject(
  ctx: Ctx,
  td: TypeDef,
  sanitized: Record<string, unknown>
): string {
  const parts: string[] = [];
  const emitted = new Set<string>();
  for (const field of td.fields) {
    const key = fieldKey(field);
    if (!Object.prototype.hasOwnProperty.call(sanitized, key)) continue;
    emitted.add(key);
    parts.push(`${JSON.stringify(key)}:${writeFieldValue(ctx, field, sanitized[key])}`);
  }
  for (const [key, value] of Object.entries(sanitized)) {
    if (emitted.has(key)) continue;
    parts.push(`${JSON.stringify(key)}:${JSON.stringify(value)}`);
  }
  return `{${parts.join(',')}}`;
}

function writeFieldValue(ctx: Ctx, field: FieldDef, value: unknown): string {
  if (value === null || value === undefined) {
    return field.typeRef.isArray ? '[]' : 'null';
  }
  const kind = resolveRefKind(ctx.schema, field.typeRef);
  if (field.typeRef.isArray) {
    if (!Array.isArray(value)) return JSON.stringify(value);
    const parts = value.map(elem => writeElemValue(ctx, field, kind, elem));
    return `[${parts.join(',')}]`;
  }
  if (kind === 'type') {
    const nested = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
    if (nested && isObject(value)) return writeObject(ctx, nested, value);
  } else if (kind === 'input') {
    const nested = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
    if (nested && isObject(value)) return writeObject(ctx, nested, value);
  }
  return JSON.stringify(value);
}

function writeElemValue(ctx: Ctx, field: FieldDef, kind: RefKind, elem: unknown): string {
  if (elem === null || elem === undefined) return 'null';
  if (kind === 'type') {
    const td = ctx.schema.types ? ctx.schema.types[field.typeRef.name] : undefined;
    if (td && isObject(elem)) return writeObject(ctx, td, elem);
  } else if (kind === 'input') {
    const td = ctx.schema.inputs ? ctx.schema.inputs[field.typeRef.name] : undefined;
    if (td && isObject(elem)) return writeObject(ctx, td, elem);
  }
  return JSON.stringify(elem);
}

function resolveRefKind(schema: Schema, ref: TypeRef): RefKind {
  if (schema.scalars && schema.scalars[ref.name]) return 'scalar';
  if (schema.enums && schema.enums[ref.name]) return 'enum';
  if (schema.types && schema.types[ref.name]) return 'type';
  if (schema.inputs && schema.inputs[ref.name]) return 'input';
  if (BUILTIN_SCALARS.has(ref.name)) return 'builtin';
  return 'builtin';
}

function fieldKey(field: FieldDef): string {
  return field.jsonKey || field.name;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
