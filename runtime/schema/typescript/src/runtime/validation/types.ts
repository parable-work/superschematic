import type { ValidationError, ValidationResult } from 'superscalar/validation';
import type { ScalarDef, SchemaInput } from '@superschematic/schema-ir';

// The IR document types live in @superschematic/schema-ir (types only) so that
// a scalar package can name the schema document type without importing this
// runtime.
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
  Schema,
  SchemaInput,
  TypeDef,
  TypeKind,
  TypeRef,
  UnionDef,
} from '@superschematic/schema-ir';

export type ScalarValidateFn = (value: string) => ValidationError[] | null;

export interface ValidationOptions {
  scalarRegistry?: ScalarRegistry;
}

export interface ScalarRegistry {
  register(name: string, fn: ScalarValidateFn): void;
  unregister(name: string): boolean;
  get(name: string): ScalarValidateFn | undefined;
  has(name: string): boolean;
  names(): string[];
}

export type ValidationFunction = (
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
) => ValidationResult;

/**
 * Reports whether a scalar's value is any JSON value, which its json_schema
 * type mapping declares as "any" (Generic_JSON in the core catalog). The
 * catalog gives such a scalar the String primitive, but an object, an array,
 * a string, a number and a boolean are all values, and only null stands for
 * a missing one. Parse and validation key the rule off this mapping, not the
 * scalar's name or primitive.
 */
export function isAnyJSONScalar(scalar: ScalarDef): boolean {
  return scalar.typeMappings?.json_schema === 'any';
}
