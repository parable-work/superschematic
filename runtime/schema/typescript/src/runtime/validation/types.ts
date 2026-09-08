import type { ValidationError, ValidationResult } from '@psgen/scalar-lib/validation';
import type { SchemaInput } from '@psgen/schema-ir';

// The IR document types live in @psgen/schema-ir (types only) so that
// @psgen/scalar-lib can name Parable.Schema without importing this runtime.
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
} from '@psgen/schema-ir';

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
