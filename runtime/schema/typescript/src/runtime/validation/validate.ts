import {
  addFieldError,
  addNestedErrors,
  hasErrors,
  newValidationErrors,
  setFieldErrors,
  type ValidationError,
  type ValidationErrors,
  type ValidationResult,
} from 'superscalar/validation';
import { createDefaultScalarValidatorRegistry } from './registry';
import { parseSchema, parseSchemaJson } from '../json/reader';
import { isAnyJSONScalar } from './types';
import type { FieldDef, SchemaInput, ScalarDef, Schema, TypeDef, ValidationOptions } from './types';

const builtinScalars = new Set(['String', 'Int', 'Float', 'Boolean', 'ID']);
let defaultScalarRegistry: ReturnType<typeof createDefaultScalarValidatorRegistry> | null = null;

function getDefaultScalarRegistry(): ReturnType<typeof createDefaultScalarValidatorRegistry> {
  if (!defaultScalarRegistry) {
    defaultScalarRegistry = createDefaultScalarValidatorRegistry();
  }
  return defaultScalarRegistry;
}

export function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function isSchema(value: unknown): value is Schema {
  if (!isObject(value)) {
    return false;
  }

  // Wire-deserialized schemas may omit empty maps entirely, so accept a
  // missing map but reject any present map that is not an object. Requiring
  // at least one map keeps raw JSON-schema documents (definitions/$schema
  // shaped) routed to parseSchema instead.
  const maps = [value.scalars, value.enums, value.types, value.inputs];
  let hasMap = false;
  for (const map of maps) {
    if (map === undefined) {
      continue;
    }
    if (!isObject(map)) {
      return false;
    }
    hasMap = true;
  }
  return hasMap;
}

export function resolveSchema(schema: SchemaInput): Schema {
  if (typeof schema === 'string') {
    return parseSchemaJson(schema);
  }

  if (isSchema(schema)) {
    return schema;
  }

  return parseSchema(schema);
}

function toObject(value: unknown): Record<string, unknown> {
  return isObject(value) ? value : {};
}

function toNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value;
  }
  return null;
}

function resolveRefKind(
  schema: Schema,
  typeName: string
): 'scalar' | 'enum' | 'type' | 'input' | 'builtin' {
  if (schema.scalars && schema.scalars[typeName]) {
    return 'scalar';
  }
  if (schema.enums && schema.enums[typeName]) {
    return 'enum';
  }
  if (schema.types && schema.types[typeName]) {
    return 'type';
  }
  if (schema.inputs && schema.inputs[typeName]) {
    return 'input';
  }
  return builtinScalars.has(typeName) ? 'builtin' : 'builtin';
}

function validateEnumValue(schema: Schema, enumName: string, value: unknown): ValidationError[] {
  if (typeof value !== 'string') {
    return [{ validator: 'type', message: `${enumName} must be a string.` }];
  }

  const enumDef = schema.enums ? schema.enums[enumName] : undefined;
  if (!enumDef) {
    return [];
  }

  for (const enumValue of enumDef.values) {
    const candidate = enumValue.serializedAs || enumValue.name;
    if (candidate === value) {
      return [];
    }
  }

  return [{ validator: 'enum', message: `${enumDef.name} must be one of the allowed values.` }];
}

function validateStringConstraints(
  scalar: ScalarDef,
  value: unknown,
  applyRequired: boolean,
  options?: ValidationOptions
): ValidationError[] {
  // A value of another JSON type is "type", required or not, and its
  // length and format are not checked.
  if (typeof value !== 'string') {
    return [{ validator: 'type', message: `${scalar.name} must be a string.` }];
  }

  if (value === '' && applyRequired) {
    return [{ validator: 'required', message: `${scalar.name} is required.` }];
  }

  const errors: ValidationError[] = [];

  // Code points, not UTF-16 units: Go counts runes, Python counts len().
  const length = [...value].length;

  if (scalar.minLength > 0 && length < scalar.minLength) {
    errors.push({
      validator: 'minLength',
      message: `${scalar.name} must be at least ${scalar.minLength} characters.`,
    });
  }

  if (scalar.maxLength > 0 && length > scalar.maxLength) {
    errors.push({
      validator: 'maxLength',
      message: `${scalar.name} must be at most ${scalar.maxLength} characters.`,
    });
  }

  if (scalar.pattern) {
    try {
      const pattern = new RegExp(scalar.pattern);
      if (!pattern.test(value)) {
        errors.push({
          validator: 'pattern',
          message: `${scalar.name} has an invalid format.`,
        });
      }
    } catch {
      errors.push({
        validator: 'pattern',
        message: `${scalar.name} has an invalid format.`,
      });
    }
  }

  if (scalar.reservedWords.length > 0) {
    const isReserved = scalar.caseInsensitive
      ? scalar.reservedWords.some(
          reservedWord => reservedWord.toLowerCase() === value.toLowerCase()
        )
      : scalar.reservedWords.includes(value);
    if (isReserved) {
      errors.push({
        validator: 'reservedWord',
        message: `${scalar.name} contains a reserved word.`,
      });
    }
  }

  // The scalar core checks the same pattern and lengths again, so it runs
  // only when the constraints above pass: one failing value, one error.
  const registry = options?.scalarRegistry ?? getDefaultScalarRegistry();
  if (errors.length === 0 && (scalar.hasCustomValidate || registry.has(scalar.name))) {
    const scalarFn = registry.get(scalar.name);
    if (scalarFn) {
      const customErrors = scalarFn(value);
      if (customErrors && customErrors.length > 0) {
        errors.push(...customErrors);
      }
    }
  }

  return errors;
}

function validateIntConstraints(scalar: ScalarDef, value: unknown): ValidationError[] {
  const parsed = toNumber(value);
  if (parsed === null || !Number.isInteger(parsed)) {
    return [{ validator: 'type', message: `${scalar.name} must be a number.` }];
  }

  const errors: ValidationError[] = [];
  if (typeof scalar.minimum === 'number' && parsed < scalar.minimum) {
    errors.push({
      validator: 'min',
      message: `${scalar.name} must be at least ${scalar.minimum}.`,
    });
  }
  if (typeof scalar.maximum === 'number' && parsed > scalar.maximum) {
    errors.push({
      validator: 'max',
      message: `${scalar.name} must be at most ${scalar.maximum}.`,
    });
  }
  return errors;
}

function validateFloatConstraints(scalar: ScalarDef, value: unknown): ValidationError[] {
  const parsed = toNumber(value);
  if (parsed === null) {
    return [{ validator: 'type', message: `${scalar.name} must be a number.` }];
  }

  const errors: ValidationError[] = [];
  if (typeof scalar.minimum === 'number' && parsed < scalar.minimum) {
    errors.push({
      validator: 'min',
      message: `${scalar.name} must be at least ${scalar.minimum}.`,
    });
  }
  if (typeof scalar.maximum === 'number' && parsed > scalar.maximum) {
    errors.push({
      validator: 'max',
      message: `${scalar.name} must be at most ${scalar.maximum}.`,
    });
  }
  return errors;
}

function validateScalarValue(
  scalar: ScalarDef,
  value: unknown,
  required: boolean,
  options?: ValidationOptions
): ValidationError[] {
  if (isAnyJSONScalar(scalar)) {
    return validateAnyJSONScalarValue(scalar, value, required);
  }
  switch (scalar.primitive) {
    case 'Int':
      return validateIntConstraints(scalar, value);
    case 'Float':
      return validateFloatConstraints(scalar, value);
    case 'Type':
      return validateTypeScalarValue(scalar, value, required, options);
    case 'Bytes':
      return validateBytesScalarValue(scalar, value, required);
    case 'String':
    default:
      return validateStringConstraints(scalar, value, required, options);
  }
}

/**
 * Any-JSON scalars (isAnyJSONScalar): an object, an array, a string (JSON
 * text or not), a number and a boolean are all values, so no type, length or
 * pattern check applies, and the string-typed scalar validator registry is
 * not consulted. Null or undefined is a missing value. A value no JSON
 * document can carry (NaN, undefined inside an object, a function, a class
 * instance, a cycle) is "type".
 */
function validateAnyJSONScalarValue(
  scalar: ScalarDef,
  value: unknown,
  required: boolean
): ValidationError[] {
  if (value === undefined || value === null) {
    return required ? [{ validator: 'required', message: `${scalar.name} is required.` }] : [];
  }
  if (!isJSONValue(value, new Set())) {
    return [{ validator: 'type', message: `${scalar.name} must be a JSON value.` }];
  }
  return [];
}

function isJSONValue(value: unknown, ancestors: Set<object>): boolean {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') {
    return true;
  }
  if (typeof value === 'number') {
    return Number.isFinite(value);
  }
  if (typeof value !== 'object' || ancestors.has(value)) {
    return false;
  }
  if (!Array.isArray(value)) {
    const prototype = Object.getPrototypeOf(value);
    if (prototype !== Object.prototype && prototype !== null) {
      return false;
    }
  }
  ancestors.add(value);
  const items: unknown[] = Array.isArray(value) ? value : Object.values(value);
  const valid = items.every(item => isJSONValue(item, ancestors));
  ancestors.delete(value);
  return valid;
}

/**
 * Object-valued scalars (primitive: "Type"). No scalar in the core catalog
 * has it: Generic_JSON is any JSON value (validateAnyJSONScalarValue), and
 * the other object-shaped scalars carry the String primitive.
 * Only the primitive shape (object, non-array, non-null) is checked here. The
 * string-typed scalar validator registry is deliberately not consulted --
 * ScalarValidateFn is `(value: string) => ...`, meaningless for object scalars.
 */
function validateTypeScalarValue(
  scalar: ScalarDef,
  value: unknown,
  required: boolean,
  _options?: ValidationOptions
): ValidationError[] {
  if (value === undefined || value === null) {
    return required ? [{ validator: 'required', message: `${scalar.name} is required.` }] : [];
  }

  if (typeof value !== 'object' || Array.isArray(value)) {
    return [{ validator: 'type', message: `${scalar.name} must be an object.` }];
  }

  return [];
}

/**
 * Bytes-valued scalars (primitive: "Bytes"). Runtime values may be Uint8Array,
 * ArrayBuffer, or base64 strings depending on transport; accept any of those
 * shapes here, transport adapters canonicalise before deeper validation.
 */
function validateBytesScalarValue(
  scalar: ScalarDef,
  value: unknown,
  required: boolean
): ValidationError[] {
  if (value === undefined || value === null) {
    return required ? [{ validator: 'required', message: `${scalar.name} is required.` }] : [];
  }

  if (typeof value === 'string' || value instanceof Uint8Array || value instanceof ArrayBuffer) {
    return [];
  }

  return [{ validator: 'type', message: `${scalar.name} must be bytes.` }];
}

function validateFieldLevelStringConstraints(field: FieldDef, value: string): ValidationError[] {
  const fieldErrors: ValidationError[] = [];
  // Code points, not UTF-16 units: Go counts runes, Python counts len().
  const length = [...value].length;
  if (field.validateMinLength !== null && length < field.validateMinLength) {
    fieldErrors.push({
      validator: 'minLength',
      message: `${field.name} must be at least ${field.validateMinLength} characters.`,
    });
  }
  if (field.validateMaxLength !== null && length > field.validateMaxLength) {
    fieldErrors.push({
      validator: 'maxLength',
      message: `${field.name} must be at most ${field.validateMaxLength} characters.`,
    });
  }
  if (field.validatePattern) {
    try {
      const pattern = new RegExp(field.validatePattern);
      if (!pattern.test(value)) {
        fieldErrors.push({
          validator: 'pattern',
          message: `${field.name} has an invalid format.`,
        });
      }
    } catch {
      fieldErrors.push({
        validator: 'pattern',
        message: `${field.name} has an invalid format.`,
      });
    }
  }
  return fieldErrors;
}

function validateFieldLevelNumericConstraints(field: FieldDef, value: number): ValidationError[] {
  const fieldErrors: ValidationError[] = [];
  if (field.validateMin !== null && value < field.validateMin) {
    fieldErrors.push({
      validator: 'min',
      message: `${field.name} must be at least ${field.validateMin}.`,
    });
  }
  if (field.validateMax !== null && value > field.validateMax) {
    fieldErrors.push({
      validator: 'max',
      message: `${field.name} must be at most ${field.validateMax}.`,
    });
  }
  return fieldErrors;
}

/**
 * The "type" error for a builtin field value of the wrong JSON type, or null.
 * The IR's string, number and boolean are checked, and so are the GraphQL
 * names the JSON Schema reader gives them; any other name is not. The
 * field's own constraints are checked only on a value that passes.
 */
function builtinTypeError(field: FieldDef, value: unknown): ValidationError | null {
  switch (field.typeRef.name) {
    case 'string':
    case 'String':
    case 'ID':
      return typeof value === 'string' ? null : { validator: 'type', message: 'expected a string' };
    case 'number':
    case 'Float':
      return toNumber(value) !== null ? null : { validator: 'type', message: 'expected a number' };
    case 'Int': {
      const parsed = toNumber(value);
      return parsed !== null && Number.isInteger(parsed)
        ? null
        : { validator: 'type', message: 'expected an integer' };
    }
    case 'boolean':
    case 'Boolean':
      return typeof value === 'boolean' ? null : { validator: 'type', message: 'expected a boolean' };
    default:
      return null;
  }
}

function hasFieldLevelConstraints(field: FieldDef): boolean {
  return (
    field.validateMin !== null ||
    field.validateMax !== null ||
    field.validateMinLength !== null ||
    field.validateMaxLength !== null ||
    field.validatePattern !== ''
  );
}

function applyFieldLevelConstraints(field: FieldDef, value: unknown): ValidationError[] {
  if (typeof value === 'string') {
    return validateFieldLevelStringConstraints(field, value);
  }
  const numeric = toNumber(value);
  if (numeric !== null) {
    return validateFieldLevelNumericConstraints(field, numeric);
  }
  return [];
}

function validateSingleField(
  schema: Schema,
  field: FieldDef,
  key: string,
  value: unknown,
  errors: ValidationErrors,
  options?: ValidationOptions
): void {
  const kind = resolveRefKind(schema, field.typeRef.name);

  if (kind === 'scalar') {
    const scalar = schema.scalars ? schema.scalars[field.typeRef.name] : undefined;
    if (!scalar) {
      return;
    }
    const fieldErrors = validateScalarValue(scalar, value, field.required, options);
    if (hasFieldLevelConstraints(field)) {
      fieldErrors.push(...applyFieldLevelConstraints(field, value));
    }
    setFieldErrors(errors, key, fieldErrors);
    return;
  }

  if (kind === 'enum') {
    const fieldErrors = validateEnumValue(schema, field.typeRef.name, value);
    setFieldErrors(errors, key, fieldErrors);
    return;
  }

  if (kind === 'type') {
    const nested = schema.types ? schema.types[field.typeRef.name] : undefined;
    if (nested && isObject(value)) {
      const nestedErrors = validateTypeDef(schema, nested, value, options);
      addNestedErrors(errors, key, nestedErrors);
    }
    return;
  }

  if (kind === 'input') {
    const nested = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
    if (nested && isObject(value)) {
      const nestedErrors = validateTypeDef(schema, nested, value, options);
      addNestedErrors(errors, key, nestedErrors);
    }
    return;
  }

  if (kind === 'builtin') {
    const typeError = builtinTypeError(field, value);
    if (typeError) {
      setFieldErrors(errors, key, [typeError]);
      return;
    }
    if (
      field.required &&
      (field.typeRef.name === 'String' || field.typeRef.name === 'ID') &&
      typeof value === 'string' &&
      value === ''
    ) {
      addFieldError(errors, key, 'required', `${field.typeRef.name} is required.`);
      return;
    }
    if (hasFieldLevelConstraints(field)) {
      const fieldErrors = applyFieldLevelConstraints(field, value);
      setFieldErrors(errors, key, fieldErrors);
    }
  }
}

function validateArrayField(
  schema: Schema,
  field: FieldDef,
  key: string,
  value: unknown,
  errors: ValidationErrors,
  options?: ValidationOptions
): void {
  if (!Array.isArray(value)) {
    return;
  }

  const kind = resolveRefKind(schema, field.typeRef.name);
  if (!field.typeRef.isArrayOfArrays) {
    validateArrayElements(schema, field, kind, key, value, errors, options);
    return;
  }

  // T[][]: every inner list is an array, never null, and may be empty. A
  // null inner list is "required" and any other value "type", at key[i].
  for (let index = 0; index < value.length; index += 1) {
    const inner = value[index];
    const innerKey = `${key}[${index}]`;
    if (inner === null || inner === undefined) {
      addFieldError(errors, innerKey, 'required', 'required field');
      continue;
    }
    if (!Array.isArray(inner)) {
      addFieldError(errors, innerKey, 'type', 'expected an array');
      continue;
    }
    validateArrayElements(schema, field, kind, innerKey, inner, errors, options);
  }
}

/**
 * Validates the elements of one list of a T[] or T[][] field, reporting each
 * at `${key}[${index}]`. For T[][] the key already names the inner list, so
 * elements report at field[i][j]. A list element is never null, in a
 * required list and an optional one alike, and the field's own constraints
 * (length, pattern, bounds) apply to each element.
 */
function validateArrayElements(
  schema: Schema,
  field: FieldDef,
  kind: ReturnType<typeof resolveRefKind>,
  key: string,
  elements: unknown[],
  errors: ValidationErrors,
  options?: ValidationOptions
): void {
  for (let index = 0; index < elements.length; index += 1) {
    const element = elements[index];
    const elementKey = `${key}[${index}]`;

    if (element === null || element === undefined) {
      addFieldError(errors, elementKey, 'required', `${field.typeRef.name} is required.`);
      continue;
    }

    if (kind === 'scalar') {
      const scalar = schema.scalars ? schema.scalars[field.typeRef.name] : undefined;
      if (!scalar) {
        continue;
      }
      const elementErrors = validateScalarValue(scalar, element, field.required, options);
      if (hasFieldLevelConstraints(field)) {
        elementErrors.push(...applyFieldLevelConstraints(field, element));
      }
      setFieldErrors(errors, elementKey, elementErrors);
      continue;
    }

    if (kind === 'builtin') {
      const typeError = builtinTypeError(field, element);
      if (typeError) {
        setFieldErrors(errors, elementKey, [typeError]);
      } else if (hasFieldLevelConstraints(field)) {
        setFieldErrors(errors, elementKey, applyFieldLevelConstraints(field, element));
      }
      continue;
    }

    if (kind === 'enum') {
      const elementErrors = validateEnumValue(schema, field.typeRef.name, element);
      setFieldErrors(errors, elementKey, elementErrors);
      continue;
    }

    if (kind === 'type') {
      const nested = schema.types ? schema.types[field.typeRef.name] : undefined;
      if (nested && isObject(element)) {
        const nestedErrors = validateTypeDef(schema, nested, element, options);
        addNestedErrors(errors, elementKey, nestedErrors);
      }
      continue;
    }

    if (kind === 'input') {
      const nested = schema.inputs ? schema.inputs[field.typeRef.name] : undefined;
      if (nested && isObject(element)) {
        const nestedErrors = validateTypeDef(schema, nested, element, options);
        addNestedErrors(errors, elementKey, nestedErrors);
      }
    }
  }
}

function validateField(
  schema: Schema,
  field: FieldDef,
  data: Record<string, unknown>,
  errors: ValidationErrors,
  options?: ValidationOptions
): void {
  const key = field.jsonKey || field.name;
  const hasKey = Object.prototype.hasOwnProperty.call(data, key);
  const value = data[key];

  if (field.required) {
    if (!hasKey || value === null || value === undefined) {
      addFieldError(errors, key, 'required', `${field.name} is required.`);
      return;
    }

    if (field.typeRef.isArray && !Array.isArray(value)) {
      addFieldError(errors, key, 'required', `${field.name} is required.`);
      return;
    }
  }

  if (!hasKey || value === null || value === undefined) {
    return;
  }

  if (field.typeRef.isArray) {
    // List bounds apply to the outer list, including for T[][].
    if (Array.isArray(value)) {
      if (field.validateListMin !== null && value.length < field.validateListMin) {
        addFieldError(
          errors,
          key,
          'listMin',
          `${field.name} must have at least ${field.validateListMin} items.`
        );
      }
      if (field.validateListMax !== null && value.length > field.validateListMax) {
        addFieldError(
          errors,
          key,
          'listMax',
          `${field.name} must have at most ${field.validateListMax} items.`
        );
      }
    }
    validateArrayField(schema, field, key, value, errors, options);
    return;
  }

  validateSingleField(schema, field, key, value, errors, options);
}

function validateTypeDef(
  schema: Schema,
  typeDef: TypeDef,
  data: Record<string, unknown>,
  options?: ValidationOptions
): ValidationErrors {
  const errors = newValidationErrors();
  for (const field of typeDef.fields) {
    validateField(schema, field, data, errors, options);
  }
  return errors;
}

function finalizeResult(errors: ValidationErrors): ValidationResult {
  return hasErrors(errors) ? errors : true;
}

export function validateSchemaType(
  schema: Schema,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationResult {
  const typeDef = schema.types ? schema.types[typeName] : undefined;
  if (!typeDef) {
    return true;
  }

  const errors = validateTypeDef(schema, typeDef, toObject(data), options);
  return finalizeResult(errors);
}

export function validateSchemaInput(
  schema: Schema,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationResult {
  const typeDef = schema.inputs ? schema.inputs[typeName] : undefined;
  if (!typeDef) {
    return true;
  }

  const errors = validateTypeDef(schema, typeDef, toObject(data), options);
  return finalizeResult(errors);
}

export function validateType(
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationResult {
  return validateSchemaType(resolveSchema(schema), typeName, data, options);
}

export function validateInput(
  schema: SchemaInput,
  typeName: string,
  data: unknown,
  options?: ValidationOptions
): ValidationResult {
  return validateSchemaInput(resolveSchema(schema), typeName, data, options);
}
