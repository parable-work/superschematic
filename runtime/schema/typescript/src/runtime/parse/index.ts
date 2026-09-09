/**
 * Runtime parse entrypoints. Public surface:
 *   - parseType / parseInput       (map -> coerced + normalized + default-filled map)
 *   - parseTypeMap / parseInputMap (identical aliases)
 *   - parseTypeJson / parseInputJson (parse JSON bytes/string then dispatch)
 *
 * All entrypoints accept an options object with `strict`, `parseRegistry`,
 * `normalizeRegistry`. Errors are returned via {data, errors} where `errors`
 * is always a ValidationErrors object (empty = success).
 */

import {
  addFieldError,
  newValidationErrors,
  type ValidationErrors,
} from 'superscalar/validation';
import type { Schema, SchemaInput } from '../validation/types';
import { resolveSchema } from '../validation/validate';
import {
  ScalarNormalizeRegistry,
  ScalarParseRegistry,
  createDefaultScalarNormalizeRegistry,
  createDefaultScalarParseRegistry,
} from './registry';
import { walkTypeDef, type WalkContext } from './walk';

export {
  ScalarNormalizeRegistry,
  ScalarParseRegistry,
  createDefaultScalarNormalizeRegistry,
  createDefaultScalarParseRegistry,
} from './registry';
export type { ScalarNormalizeFunc, ScalarParseFunc } from './registry';

export interface ParseOptions {
  strict?: boolean;
  parseRegistry?: ScalarParseRegistry;
  normalizeRegistry?: ScalarNormalizeRegistry;
}

export interface ParseResult {
  data: Record<string, unknown>;
  errors: ValidationErrors;
}

function ctxFromOptions(schema: Schema, options?: ParseOptions): WalkContext {
  return {
    schema,
    parseRegistry: options?.parseRegistry ?? defaultParseRegistry(),
    normalizeRegistry: options?.normalizeRegistry ?? defaultNormalizeRegistry(),
    strict: options?.strict === true,
  };
}

let cachedParseRegistry: ScalarParseRegistry | null = null;
let cachedNormalizeRegistry: ScalarNormalizeRegistry | null = null;

function defaultParseRegistry(): ScalarParseRegistry {
  if (!cachedParseRegistry) cachedParseRegistry = createDefaultScalarParseRegistry();
  return cachedParseRegistry;
}

function defaultNormalizeRegistry(): ScalarNormalizeRegistry {
  if (!cachedNormalizeRegistry) cachedNormalizeRegistry = createDefaultScalarNormalizeRegistry();
  return cachedNormalizeRegistry;
}

function unknownTypeError(name: string, kind: 'type' | 'input'): ParseResult {
  const errs = newValidationErrors();
  addFieldError(errs, '', 'type', `unknown ${kind} "${name}"`);
  return { data: {}, errors: errs };
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function objectFromValue(value: unknown): Record<string, unknown> {
  return isObject(value) ? value : {};
}

export function parseType(
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  options?: ParseOptions
): ParseResult {
  const resolved = resolveSchema(schema);
  const td = resolved.types ? resolved.types[typeName] : undefined;
  if (!td) return unknownTypeError(typeName, 'type');
  return walkTypeDef(ctxFromOptions(resolved, options), td, objectFromValue(data));
}

export function parseInput(
  schema: SchemaInput,
  inputName: string,
  data: unknown,
  options?: ParseOptions
): ParseResult {
  const resolved = resolveSchema(schema);
  const td = resolved.inputs ? resolved.inputs[inputName] : undefined;
  if (!td) return unknownTypeError(inputName, 'input');
  return walkTypeDef(ctxFromOptions(resolved, options), td, objectFromValue(data));
}

export function parseTypeMap(
  schema: SchemaInput,
  typeName: string,
  data: Record<string, unknown>,
  options?: ParseOptions
): ParseResult {
  return parseType(schema, typeName, data, options);
}

export function parseInputMap(
  schema: SchemaInput,
  inputName: string,
  data: Record<string, unknown>,
  options?: ParseOptions
): ParseResult {
  return parseInput(schema, inputName, data, options);
}

function decodeJson(json: string | Uint8Array): { value: unknown; errors: ValidationErrors } {
  const errors = newValidationErrors();
  let text: string;
  if (typeof json === 'string') {
    text = json;
  } else {
    try {
      text = new TextDecoder().decode(json);
    } catch (err) {
      addFieldError(errors, '', 'json', err instanceof Error ? err.message : String(err));
      return { value: null, errors };
    }
  }
  try {
    return { value: JSON.parse(text), errors };
  } catch (err) {
    addFieldError(errors, '', 'json', err instanceof Error ? err.message : String(err));
    return { value: null, errors };
  }
}

export function parseTypeJson(
  schema: SchemaInput,
  typeName: string,
  json: string | Uint8Array,
  options?: ParseOptions
): ParseResult {
  const { value, errors } = decodeJson(json);
  if (Object.keys(errors).length > 0) {
    return { data: {}, errors };
  }
  if (!isObject(value)) {
    const errs = newValidationErrors();
    addFieldError(errs, '', 'json', 'expected JSON object payload');
    return { data: {}, errors: errs };
  }
  return parseType(schema, typeName, value, options);
}

export function parseInputJson(
  schema: SchemaInput,
  inputName: string,
  json: string | Uint8Array,
  options?: ParseOptions
): ParseResult {
  const { value, errors } = decodeJson(json);
  if (Object.keys(errors).length > 0) {
    return { data: {}, errors };
  }
  if (!isObject(value)) {
    const errs = newValidationErrors();
    addFieldError(errs, '', 'json', 'expected JSON object payload');
    return { data: {}, errors: errs };
  }
  return parseInput(schema, inputName, value, options);
}
