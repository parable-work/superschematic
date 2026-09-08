/**
 * Top-level runtime facade. Composes the parse / validate / serialize / mask /
 * merge submodules into one end-to-end API:
 *
 *   load*   = parse + validate (most common entrypoint)
 *   parse*  = parse only (for staged pipelines)
 *   validate* = validate already-parsed data
 *   marshal* = serialize map -> JSON
 *   typeToMap / inputToMap = sanitized map output
 *   mask* / merge* = secret handling
 *
 * Strict mode and registries are passed via an options object on each call.
 * Construct a `Runtime` once per schema to reuse compiled state across many calls.
 */

import {
  hasErrors,
  mergeValidationErrors,
  newValidationErrors,
  type ValidationErrors,
} from 'superscalar/validation';
import {
  ScalarNormalizeRegistry,
  ScalarParseRegistry,
  createDefaultScalarNormalizeRegistry,
  createDefaultScalarParseRegistry,
  parseInput,
  parseInputJson,
  parseInputMap,
  parseType,
  parseTypeJson,
  parseTypeMap,
  type ParseOptions,
  type ParseResult,
} from './parse';
import {
  inputToMap,
  marshalInput,
  marshalType,
  typeToMap,
  type MarshalResult,
  type SerializeOptions,
  type SerializeResult,
} from './serialize';
import { maskInput, maskType } from './mask';
import { mergeInput, mergeType } from './merge';
import {
  missingValidators,
  validateInputErrors,
  validateTypeErrors,
} from './validation/facade-validate';
import {
  ScalarValidatorRegistry,
  createDefaultScalarValidatorRegistry,
} from './validation/registry';
import type { Schema, SchemaInput, ValidationOptions } from './validation/types';
import { resolveSchema } from './validation/validate';
import { loadYamlObject } from './yaml';

export interface RuntimeOptions {
  strict?: boolean;
  maskSecrets?: boolean;
  parseRegistry?: ScalarParseRegistry;
  normalizeRegistry?: ScalarNormalizeRegistry;
  validateRegistry?: ScalarValidatorRegistry;
}

export interface LoadOptions extends RuntimeOptions {}
export interface FacadeParseOptions extends RuntimeOptions {}
export interface FacadeMarshalOptions extends RuntimeOptions {}
export interface FacadeValidateOptions {
  validateRegistry?: ScalarValidatorRegistry;
}

export interface LoadResult {
  data: Record<string, unknown>;
  errors: ValidationErrors;
}

function parseOptionsFrom(opts?: RuntimeOptions): ParseOptions {
  return {
    strict: opts?.strict,
    parseRegistry: opts?.parseRegistry,
    normalizeRegistry: opts?.normalizeRegistry,
  };
}

function serializeOptionsFrom(opts?: RuntimeOptions): SerializeOptions {
  return {
    strict: opts?.strict,
    maskSecrets: opts?.maskSecrets,
  };
}

function validationOptionsFrom(opts?: RuntimeOptions): ValidationOptions {
  return {
    scalarRegistry: opts?.validateRegistry,
  };
}

function withErrors(parseResult: ParseResult, validateErrs: ValidationErrors): LoadResult {
  if (hasErrors(parseResult.errors)) {
    if (hasErrors(validateErrs)) {
      return {
        data: parseResult.data,
        errors: mergeValidationErrors(parseResult.errors, validateErrs),
      };
    }
    return parseResult;
  }
  return { data: parseResult.data, errors: validateErrs };
}

/** Reusable runtime that caches default registries across many calls. */
export class Runtime {
  readonly schema: Schema;
  private readonly options: RuntimeOptions;
  private readonly parseRegistry: ScalarParseRegistry;
  private readonly normalizeRegistry: ScalarNormalizeRegistry;
  private readonly validateRegistry: ScalarValidatorRegistry;

  constructor(schema: SchemaInput, options?: RuntimeOptions) {
    this.schema = resolveSchema(schema);
    this.options = options ?? {};
    this.parseRegistry = options?.parseRegistry ?? createDefaultScalarParseRegistry();
    this.normalizeRegistry = options?.normalizeRegistry ?? createDefaultScalarNormalizeRegistry();
    this.validateRegistry = options?.validateRegistry ?? createDefaultScalarValidatorRegistry();
  }

  private resolveOptions(call?: RuntimeOptions): RuntimeOptions {
    return {
      strict: call?.strict ?? this.options.strict,
      maskSecrets: call?.maskSecrets ?? this.options.maskSecrets,
      parseRegistry: call?.parseRegistry ?? this.parseRegistry,
      normalizeRegistry: call?.normalizeRegistry ?? this.normalizeRegistry,
      validateRegistry: call?.validateRegistry ?? this.validateRegistry,
    };
  }

  loadType(typeName: string, data: unknown, opts?: LoadOptions): LoadResult {
    return loadType(this.schema, typeName, data, this.resolveOptions(opts));
  }

  loadInput(inputName: string, data: unknown, opts?: LoadOptions): LoadResult {
    return loadInput(this.schema, inputName, data, this.resolveOptions(opts));
  }

  loadTypeYaml(typeName: string, yamlSource: string | Uint8Array, opts?: LoadOptions): LoadResult {
    return loadTypeYaml(this.schema, typeName, yamlSource, this.resolveOptions(opts));
  }

  loadInputYaml(inputName: string, yamlSource: string | Uint8Array, opts?: LoadOptions): LoadResult {
    return loadInputYaml(this.schema, inputName, yamlSource, this.resolveOptions(opts));
  }

  parseType(typeName: string, data: unknown, opts?: FacadeParseOptions): ParseResult {
    return parseType(this.schema, typeName, data, parseOptionsFrom(this.resolveOptions(opts)));
  }

  parseInput(inputName: string, data: unknown, opts?: FacadeParseOptions): ParseResult {
    return parseInput(this.schema, inputName, data, parseOptionsFrom(this.resolveOptions(opts)));
  }

  parseTypeMap(typeName: string, data: Record<string, unknown>, opts?: FacadeParseOptions): ParseResult {
    return parseTypeMap(this.schema, typeName, data, parseOptionsFrom(this.resolveOptions(opts)));
  }

  parseInputMap(inputName: string, data: Record<string, unknown>, opts?: FacadeParseOptions): ParseResult {
    return parseInputMap(this.schema, inputName, data, parseOptionsFrom(this.resolveOptions(opts)));
  }

  validateType(typeName: string, data: unknown, opts?: FacadeValidateOptions): ValidationErrors {
    return validateTypeErrors(this.schema, typeName, data, {
      scalarRegistry: opts?.validateRegistry ?? this.validateRegistry,
    });
  }

  validateInput(inputName: string, data: unknown, opts?: FacadeValidateOptions): ValidationErrors {
    return validateInputErrors(this.schema, inputName, data, {
      scalarRegistry: opts?.validateRegistry ?? this.validateRegistry,
    });
  }

  marshalType(typeName: string, data: Record<string, unknown>, opts?: FacadeMarshalOptions): MarshalResult {
    return marshalType(this.schema, typeName, data, serializeOptionsFrom(this.resolveOptions(opts)));
  }

  marshalInput(inputName: string, data: Record<string, unknown>, opts?: FacadeMarshalOptions): MarshalResult {
    return marshalInput(this.schema, inputName, data, serializeOptionsFrom(this.resolveOptions(opts)));
  }

  typeToMap(typeName: string, data: Record<string, unknown>, opts?: FacadeMarshalOptions): SerializeResult {
    return typeToMap(this.schema, typeName, data, serializeOptionsFrom(this.resolveOptions(opts)));
  }

  inputToMap(inputName: string, data: Record<string, unknown>, opts?: FacadeMarshalOptions): SerializeResult {
    return inputToMap(this.schema, inputName, data, serializeOptionsFrom(this.resolveOptions(opts)));
  }

  maskType(typeName: string, data: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
    return maskType(this.schema, typeName, data);
  }

  maskInput(inputName: string, data: Record<string, unknown> | null | undefined): Record<string, unknown> | null {
    return maskInput(this.schema, inputName, data);
  }

  mergeType(
    typeName: string,
    newData: Record<string, unknown> | null | undefined,
    existingData: Record<string, unknown> | null | undefined
  ): Record<string, unknown> | null {
    return mergeType(this.schema, typeName, newData, existingData);
  }

  mergeInput(
    inputName: string,
    newData: Record<string, unknown> | null | undefined,
    existingData: Record<string, unknown> | null | undefined
  ): Record<string, unknown> | null {
    return mergeInput(this.schema, inputName, newData, existingData);
  }

  missingValidators(): string[] {
    return missingValidators(this.schema, { scalarRegistry: this.validateRegistry });
  }
}

// ---------- Module-level convenience functions ----------

export function loadType(
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  opts?: LoadOptions
): LoadResult {
  const resolved = resolveSchema(schema);
  const parsed = typeof data === 'string' || data instanceof Uint8Array
    ? parseTypeJson(resolved, typeName, data, parseOptionsFrom(opts))
    : parseType(resolved, typeName, data, parseOptionsFrom(opts));
  if (hasErrors(parsed.errors)) {
    return { data: parsed.data, errors: parsed.errors };
  }
  const valErrs = validateTypeErrors(resolved, typeName, parsed.data, validationOptionsFrom(opts));
  return withErrors(parsed, valErrs);
}

export function loadInput(
  schema: SchemaInput,
  inputName: string,
  data: unknown,
  opts?: LoadOptions
): LoadResult {
  const resolved = resolveSchema(schema);
  const parsed = typeof data === 'string' || data instanceof Uint8Array
    ? parseInputJson(resolved, inputName, data, parseOptionsFrom(opts))
    : parseInput(resolved, inputName, data, parseOptionsFrom(opts));
  if (hasErrors(parsed.errors)) {
    return { data: parsed.data, errors: parsed.errors };
  }
  const valErrs = validateInputErrors(resolved, inputName, parsed.data, validationOptionsFrom(opts));
  return withErrors(parsed, valErrs);
}

export function loadTypeYaml(
  schema: SchemaInput,
  typeName: string,
  yamlSource: string | Uint8Array,
  opts?: LoadOptions
): LoadResult {
  const resolved = resolveSchema(schema);
  const { value, errors } = loadYamlObject(yamlSource);
  if (hasErrors(errors)) return { data: {}, errors };
  const parsed = parseType(resolved, typeName, value ?? {}, parseOptionsFrom(opts));
  if (hasErrors(parsed.errors)) {
    return { data: parsed.data, errors: parsed.errors };
  }
  const valErrs = validateTypeErrors(resolved, typeName, parsed.data, validationOptionsFrom(opts));
  return withErrors(parsed, valErrs);
}

export function loadInputYaml(
  schema: SchemaInput,
  inputName: string,
  yamlSource: string | Uint8Array,
  opts?: LoadOptions
): LoadResult {
  const resolved = resolveSchema(schema);
  const { value, errors } = loadYamlObject(yamlSource);
  if (hasErrors(errors)) return { data: {}, errors };
  const parsed = parseInput(resolved, inputName, value ?? {}, parseOptionsFrom(opts));
  if (hasErrors(parsed.errors)) {
    return { data: parsed.data, errors: parsed.errors };
  }
  const valErrs = validateInputErrors(resolved, inputName, parsed.data, validationOptionsFrom(opts));
  return withErrors(parsed, valErrs);
}

export { newValidationErrors, hasErrors };
