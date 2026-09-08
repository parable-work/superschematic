import type { ValidationError, ValidationResult } from 'superscalar/validation';
import type { SchemaInput } from '@superschematic/schema-ir';

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
