/**
 * Schema-aware secret masking. Walks the schema and replaces values of fields
 * marked `@secret` with their zero-equivalent (empty string / 0 / false / [] /
 * {}) before the data leaves the server.
 *
 * - Non-secret fields are deep-copied as-is.
 * - Nested types / inputs / arrays are recursed.
 * - Required secrets get a typed zero value; optional secrets get null.
 * - Unknown fields in `data` are preserved on the output.
 */

import type { FieldDef, Schema, SchemaInput, TypeDef, TypeRef } from '../validation/types';
import { resolveSchema } from '../validation/validate';

const BUILTIN_SCALARS = new Set(['String', 'Int', 'Float', 'Boolean', 'ID']);

type RefKind = 'scalar' | 'enum' | 'type' | 'input' | 'builtin';

export function maskType(
  schema: SchemaInput,
  typeName: string,
  data: Record<string, unknown> | null | undefined
): Record<string, unknown> | null {
  if (data === null || data === undefined) return null;
  const resolved = resolveSchema(schema);
  const td = resolved.types ? resolved.types[typeName] : undefined;
  if (!td) return deepCopyMap(data);
  return maskTypeDef(resolved, td, data);
}

export function maskInput(
  schema: SchemaInput,
  inputName: string,
  data: Record<string, unknown> | null | undefined
): Record<string, unknown> | null {
  if (data === null || data === undefined) return null;
  const resolved = resolveSchema(schema);
  const td = resolved.inputs ? resolved.inputs[inputName] : undefined;
  if (!td) return deepCopyMap(data);
  return maskTypeDef(resolved, td, data);
}

function maskTypeDef(
  schema: Schema,
  td: TypeDef,
  data: Record<string, unknown>
): Record<string, unknown> {
  const masked: Record<string, unknown> = deepCopyMap(data);
  for (const field of td.fields) {
    const key = fieldKey(field);
    if (!Object.prototype.hasOwnProperty.call(data, key)) continue;
    masked[key] = maskField(schema, field, data[key]);
  }
  return masked;
}

function maskField(schema: Schema, field: FieldDef, value: unknown): unknown {
  if (value === null || value === undefined) return null;

  if (field.secret) {
    return zeroSecretValue(schema, field);
  }

  const kind = resolveRefKind(schema, field.typeRef);
  if (field.typeRef.isArray) {
    if (!Array.isArray(value)) return deepCopyValue(value);
    return maskArrayField(schema, field, kind, value);
  }

  switch (kind) {
    case 'type': {
      const td = schema.types ? schema.types[field.typeRef.name] : undefined;
      if (!td || !isObject(value)) return deepCopyValue(value);
      return maskTypeDef(schema, td, value);
    }
    case 'input': {
      const td = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
      if (!td || !isObject(value)) return deepCopyValue(value);
      return maskTypeDef(schema, td, value);
    }
    default:
      return deepCopyValue(value);
  }
}

function maskArrayField(
  schema: Schema,
  field: FieldDef,
  kind: RefKind,
  arr: unknown[]
): unknown[] {
  const out: unknown[] = new Array(arr.length);
  for (let i = 0; i < arr.length; i += 1) {
    const elem = arr[i];
    if (elem === null || elem === undefined) {
      out[i] = null;
      continue;
    }
    if (kind === 'type') {
      const td = schema.types ? schema.types[field.typeRef.name] : undefined;
      if (td && isObject(elem)) {
        out[i] = maskTypeDef(schema, td, elem);
        continue;
      }
    } else if (kind === 'input') {
      const td = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
      if (td && isObject(elem)) {
        out[i] = maskTypeDef(schema, td, elem);
        continue;
      }
    }
    out[i] = deepCopyValue(elem);
  }
  return out;
}

function zeroSecretValue(schema: Schema, field: FieldDef): unknown {
  if (!field.required) return null;
  if (field.typeRef.isArray) return [];
  const kind = resolveRefKind(schema, field.typeRef);
  switch (kind) {
    case 'type':
    case 'input':
      return {};
    case 'enum':
      return '';
    case 'scalar': {
      const scalar = schema.scalars ? schema.scalars[field.typeRef.name] : undefined;
      return scalar ? zeroForPrimitive(scalar.primitive) : null;
    }
    case 'builtin':
      return zeroForPrimitive(field.typeRef.name);
    default:
      return null;
  }
}

function zeroForPrimitive(name: string): unknown {
  switch (name) {
    case 'String':
    case 'ID':
      return '';
    case 'Int':
      return 0;
    case 'Float':
      return 0;
    case 'Boolean':
      return false;
    default:
      return null;
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

function fieldKey(field: FieldDef): string {
  return field.jsonKey || field.name;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function deepCopyMap(input: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(input)) {
    out[k] = deepCopyValue(v);
  }
  return out;
}

export function deepCopyValue(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(deepCopyValue);
  if (isObject(v)) return deepCopyMap(v);
  return v;
}
