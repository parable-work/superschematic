/**
 * Validate entrypoints returning `ValidationErrors` directly (empty object on
 * success, populated on failure) rather than the `true | ValidationErrors` of
 * validate.ts, so the facade can compose load/parse/validate without sentinel
 * branching.
 */

import {
  hasErrors,
  newValidationErrors,
  type ValidationErrors,
} from '@psgen/scalar-lib/validation';
import type { Schema, SchemaInput, ValidationOptions } from './types';
import {
  resolveSchema,
  validateSchemaInput as legacyValidateSchemaInput,
  validateSchemaType as legacyValidateSchemaType,
} from './validate';

function toErrors(result: true | ValidationErrors): ValidationErrors {
  if (result === true) return newValidationErrors();
  return result;
}

/**
 * Validate against a named object type. Returns ValidationErrors (empty on success).
 */
export function validateTypeErrors(
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationErrors {
  const resolved = resolveSchema(schema);
  return toErrors(legacyValidateSchemaType(resolved, typeName, data, options));
}

/**
 * Validate against a named input type. Returns ValidationErrors (empty on
 * success).
 */
export function validateInputErrors(
  schema: SchemaInput,
  inputName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationErrors {
  const resolved = resolveSchema(schema);
  return toErrors(legacyValidateSchemaInput(resolved, inputName, data, options));
}

/**
 * Identify scalars that have HasCustomValidate but no registered validator.
 * Returns canonical scalar names sorted; empty array means full coverage.
 */
export function missingValidators(
  schema: Schema,
  options?: ValidationOptions
): string[] {
  if (!options?.scalarRegistry) return [];
  const missing: string[] = [];
  for (const scalar of Object.values(schema.scalars || {})) {
    if (scalar.hasCustomValidate && !options.scalarRegistry.has(scalar.name)) {
      missing.push(scalar.name);
    }
  }
  return missing.sort((a, b) => a.localeCompare(b));
}

export { hasErrors };
