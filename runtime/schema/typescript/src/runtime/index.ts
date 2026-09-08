export { parseSchema, parseSchemaJson } from './json/reader';
export { parseSchemaIR, parseSchemaIRJson } from './ir/reader';
export { BUILTIN_SCALARS } from './builtin-scalars.generated';
export { writeSchemaJson, writeScalarsJson } from './json/writer';
export {
  createDefaultScalarValidatorRegistry,
  createScalarValidatorRegistry,
  defaultScalarValidators,
  ScalarValidatorRegistry,
} from './validation/registry';
export {
  isObject,
  isSchema,
  resolveSchema,
  validateSchemaInput,
  validateSchemaType,
} from './validation/validate';

// Legacy validate entry points return `true | ValidationErrors`. Prefer the
// facade `validateType` / `validateInput` (return `ValidationErrors` directly,
// empty on success) for new code so it composes with `loadType`, `parseType`.
import {
  validateInput as legacyValidateInput,
  validateType as legacyValidateType,
} from './validation/validate';

/** @deprecated Use `validateType` (returns ValidationErrors). */
export const validateTypeLegacy = legacyValidateType;

/** @deprecated Use `validateInput` (returns ValidationErrors). */
export const validateInputLegacy = legacyValidateInput;

// Facade: load/parse/validate/marshal/mask/merge surface.
export {
  Runtime,
  loadInput,
  loadInputYaml,
  loadType,
  loadTypeYaml,
} from './facade';
export type {
  FacadeMarshalOptions,
  FacadeParseOptions,
  FacadeValidateOptions,
  LoadOptions,
  LoadResult,
  RuntimeOptions,
} from './facade';

export {
  parseInput,
  parseInputJson,
  parseInputMap,
  parseType,
  parseTypeJson,
  parseTypeMap,
  ScalarNormalizeRegistry,
  ScalarParseRegistry,
  createDefaultScalarNormalizeRegistry,
  createDefaultScalarParseRegistry,
} from './parse';
export type {
  ParseOptions,
  ParseResult,
  ScalarNormalizeFunc,
  ScalarParseFunc,
} from './parse';

export {
  inputToMap,
  marshalInput,
  marshalType,
  typeToMap,
} from './serialize';
export type {
  MarshalResult,
  SerializeOptions,
  SerializeResult,
} from './serialize';

export { maskInput, maskType } from './mask';
export { mergeInput, mergeType } from './merge';

export {
  missingValidators,
  validateInputErrors as validateInput,
  validateTypeErrors as validateType,
} from './validation/facade-validate';

export { loadYamlObject, dumpYaml } from './yaml';

import { parseSchemaJson } from './json/reader';
import { writeSchemaJson, writeScalarsJson } from './json/writer';
import type { ScalarDef, Schema } from './validation/types';

export type SchemaFormat = 'json' | 'yaml';

export interface FormatHooks {
  parseYaml?: (schemaText: string) => Schema;
  writeYaml?: (schema: Schema) => string;
  writeScalarsYaml?: (scalars: Record<string, ScalarDef>) => string;
}

function parseSchemaForFormat(
  schemaText: string,
  format: SchemaFormat,
  hooks?: FormatHooks
): Schema {
  switch (format) {
    case 'json':
      return parseSchemaJson(schemaText);
    case 'yaml':
      if (!hooks?.parseYaml) {
        throw new Error(
          'runtime schema parse error: yaml format is not implemented; provide hooks.parseYaml'
        );
      }
      return hooks.parseYaml(schemaText);
    default:
      throw new Error(`runtime schema parse error: unsupported format "${format}"`);
  }
}

function writeSchemaForFormat(
  schema: Schema,
  format: SchemaFormat,
  hooks?: FormatHooks
): string {
  switch (format) {
    case 'json':
      return writeSchemaJson(schema);
    case 'yaml':
      if (!hooks?.writeYaml) {
        throw new Error(
          'runtime schema write error: yaml format is not implemented; provide hooks.writeYaml'
        );
      }
      return hooks.writeYaml(schema);
    default:
      throw new Error(`runtime schema write error: unsupported format "${format}"`);
  }
}

export function parseSchemaByFormat(
  schemaText: string,
  format: SchemaFormat,
  hooks?: FormatHooks
): Schema {
  return parseSchemaForFormat(schemaText, format, hooks);
}

export function writeSchemaByFormat(
  schema: Schema,
  format: SchemaFormat,
  hooks?: FormatHooks
): string {
  return writeSchemaForFormat(schema, format, hooks);
}

export function writeScalarsByFormat(
  scalars: Record<string, ScalarDef>,
  format: SchemaFormat,
  hooks?: FormatHooks
): string {
  switch (format) {
    case 'json':
      return writeScalarsJson(scalars);
    case 'yaml':
      if (!hooks?.writeScalarsYaml) {
        throw new Error(
          'runtime scalar write error: yaml format is not implemented; provide hooks.writeScalarsYaml'
        );
      }
      return hooks.writeScalarsYaml(scalars);
    default:
      throw new Error(`runtime scalar write error: unsupported format "${format}"`);
  }
}

export function convertSchema(
  schemaText: string,
  sourceFormat: SchemaFormat,
  targetFormat: SchemaFormat,
  hooks?: FormatHooks
): string {
  const schema = parseSchemaByFormat(schemaText, sourceFormat, hooks);
  return writeSchemaForFormat(schema, targetFormat, hooks);
}

export type {
  ArgumentDef,
  DefinitionKind,
  EnumDef,
  EnumValueDef,
  FieldDef,
  FileUploadConfig,
  ImageConstraints,
  Import,
  IndexDef,
  MiddlewareConfig,
  OperationSet,
  RelationDef,
  ScalarDef,
  ScalarRegistry,
  ScalarValidateFn,
  Schema,
  SchemaInput,
  TypeDef,
  TypeKind,
  TypeRef,
  UnionDef,
  ValidationFunction,
  ValidationOptions,
} from './validation/types';
