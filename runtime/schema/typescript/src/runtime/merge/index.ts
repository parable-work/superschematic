/**
 * Schema-aware merge for secret fields. When a config form comes back with a
 * secret field zeroed (because the client never received the raw value), those
 * zero values are replaced with the existing stored value before writing.
 * Non-secret fields always take the new value.
 */

import type { FieldDef, Schema, SchemaInput, TypeDef, TypeRef } from '../validation/types';
import { resolveSchema } from '../validation/validate';
import { deepCopyMap, deepCopyValue } from '../mask';

const BUILTIN_SCALARS = new Set(['String', 'Int', 'Float', 'Boolean', 'ID']);

type RefKind = 'scalar' | 'enum' | 'type' | 'input' | 'builtin';

export function mergeType(
  schema: SchemaInput,
  typeName: string,
  newData: Record<string, unknown> | null | undefined,
  existingData: Record<string, unknown> | null | undefined
): Record<string, unknown> | null {
  if (newData === null || newData === undefined) return null;
  if (existingData === null || existingData === undefined) return deepCopyMap(newData);
  const resolved = resolveSchema(schema);
  const td = resolved.types ? resolved.types[typeName] : undefined;
  if (!td) return deepCopyMap(newData);
  return mergeTypeDef(resolved, td, newData, existingData);
}

export function mergeInput(
  schema: SchemaInput,
  inputName: string,
  newData: Record<string, unknown> | null | undefined,
  existingData: Record<string, unknown> | null | undefined
): Record<string, unknown> | null {
  if (newData === null || newData === undefined) return null;
  if (existingData === null || existingData === undefined) return deepCopyMap(newData);
  const resolved = resolveSchema(schema);
  const td = resolved.inputs ? resolved.inputs[inputName] : undefined;
  if (!td) return deepCopyMap(newData);
  return mergeTypeDef(resolved, td, newData, existingData);
}

function mergeTypeDef(
  schema: Schema,
  td: TypeDef,
  newData: Record<string, unknown>,
  existingData: Record<string, unknown>
): Record<string, unknown> {
  const merged: Record<string, unknown> = deepCopyMap(newData);
  for (const field of td.fields) {
    const key = fieldKey(field);
    const newExists = Object.prototype.hasOwnProperty.call(newData, key);
    const existingExists = Object.prototype.hasOwnProperty.call(existingData, key);
    const newVal = newData[key];
    const existingVal = existingData[key];

    if (field.secret) {
      if ((!newExists || isZeroValue(newVal)) && existingExists) {
        merged[key] = deepCopyValue(existingVal);
      }
      continue;
    }

    if (!newExists || newVal === null || newVal === undefined) continue;
    if (existingVal === null || existingVal === undefined) continue;

    const kind = resolveRefKind(schema, field.typeRef);
    if (field.typeRef.isArray) {
      if (Array.isArray(newVal) && Array.isArray(existingVal)) {
        merged[key] = field.typeRef.isArrayOfArrays
          ? mergeNestedArrayField(schema, field, kind, newVal, existingVal)
          : mergeArrayField(schema, field, kind, newVal, existingVal);
      }
      continue;
    }

    if (kind === 'type') {
      const nestedTd = schema.types ? schema.types[field.typeRef.name] : undefined;
      if (nestedTd && isObject(newVal) && isObject(existingVal)) {
        merged[key] = mergeTypeDef(schema, nestedTd, newVal, existingVal);
      }
    } else if (kind === 'input') {
      const nestedTd = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
      if (nestedTd && isObject(newVal) && isObject(existingVal)) {
        merged[key] = mergeTypeDef(schema, nestedTd, newVal, existingVal);
      }
    }
  }
  return merged;
}

function mergeArrayField(
  schema: Schema,
  field: FieldDef,
  kind: RefKind,
  newArr: unknown[],
  existingArr: unknown[]
): unknown[] {
  const merged: unknown[] = new Array(newArr.length);
  for (let i = 0; i < newArr.length; i += 1) {
    const newElem = newArr[i];
    if (newElem === null || newElem === undefined) {
      merged[i] = null;
      continue;
    }
    const existingElem = i < existingArr.length ? existingArr[i] : undefined;
    if (kind === 'type') {
      const td = schema.types ? schema.types[field.typeRef.name] : undefined;
      if (td && isObject(newElem) && isObject(existingElem)) {
        merged[i] = mergeTypeDef(schema, td, newElem, existingElem);
        continue;
      }
    } else if (kind === 'input') {
      const td = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
      if (td && isObject(newElem) && isObject(existingElem)) {
        merged[i] = mergeTypeDef(schema, td, newElem, existingElem);
        continue;
      }
    }
    merged[i] = deepCopyValue(newElem);
  }
  return merged;
}

/**
 * T[][]: merges each new inner list with the existing inner list at the same
 * position, element by element, so secrets inside nested objects survive.
 */
function mergeNestedArrayField(
  schema: Schema,
  field: FieldDef,
  kind: RefKind,
  newArr: unknown[],
  existingArr: unknown[]
): unknown[] {
  return newArr.map((newInner, i) => {
    const existingInner = i < existingArr.length ? existingArr[i] : undefined;
    if (Array.isArray(newInner) && Array.isArray(existingInner)) {
      return mergeArrayField(schema, field, kind, newInner, existingInner);
    }
    return deepCopyValue(newInner);
  });
}

function isZeroValue(value: unknown): boolean {
  if (value === null || value === undefined) return true;
  if (typeof value === 'string') return value === '';
  if (typeof value === 'number') return value === 0;
  if (typeof value === 'boolean') return value === false;
  if (Array.isArray(value)) return value.length === 0;
  if (isObject(value)) return Object.keys(value).length === 0;
  return false;
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
