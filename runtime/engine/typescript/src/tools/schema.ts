/*
The JSON Schema of a schema's fields, as the SDK generators write it for
tool arguments (internal/generator/toolsutil and the api generator's
scalar table, apigen/scalars_jsonschema.go), so a model sees the same
argument schema whether a schema is generated or run by the engine:

- a builtin primitive is `string`, `number` or `boolean`, with the
  generators' descriptions;
- a scalar takes its type from its `json_schema` type mapping, or from
  its primitive (an integer when its name or a mapping says so), its
  format from its declaration or from its name and mappings (an email, a
  date-time, a date or a URI), its description, pattern, lengths and
  range, and names itself under the scalar key (`x-superschematic-scalar`
  by default). `Generic.JSON`'s mapping is `any`: every JSON type but
  null (D14, amended);
- an enum is a string listing its serialized values;
- a type of the document is a closed object of its fields, sorted
  `required`; a type already being expanded is a plain reference, so
  recursive types end;
- a list is `items`, a list of lists `items` of `items`, with the field's
  value rules on the innermost items and its list bounds on the outer
  list;
- a field that is not required is nullable: its type also allows null,
  and an enum also lists null.

The engine keys a property by the field's JSON key (its jsonTag, or its
name), the key its instances hold. An instance's object may carry allOf:
the JSON Schemas its behaviors' validate holds its own fields to
(instanceSchema), as they write them. The Go generators write none, so
the parity vectors hold none. A builtin scalar the document does not
declare is taken from the schema runtime's catalog, as the Go loader fills
in a declared one. The GraphQL primitive names the schema runtime also
reads (String, ID, Int, Float, Boolean), which the Go loader refuses, are
the builtin primitive they name; Int is an integer.

runtime/engine/testdata/tool_parameters_parity.json, which a Go test
writes from toolsutil, holds this to the Go output, digests included.

A Property renders two ways: as tools/schema.json writes it (keys sorted,
`any` as the list of every JSON type but null, a nullable type with
"null" last), and as the Go encoder writes it for inputSchemaDigest
(struct field order, the scalar key first, the type of a nullable or an
`any` property and a nullable property's enum last, HTML characters
escaped).
*/

import { createHash } from 'node:crypto';

import { BUILTIN_SCALARS } from '@superschematic/schema-runtime';
import type { Document, FieldDef, ScalarDef as DocumentScalar, TypeDef } from '@superschematic/schema-ir/schema-file';

import { arrayDepth, jsonKey, refKind, scalarKey } from '../registry/document.js';
import { DEFAULT_TOOL_KEYS, type ToolKeys } from './options.js';

/** A JSON Schema: an object, or true or false. */
export type JSONSchemaValue = boolean | { readonly [key: string]: unknown };

/**
 * One property of an argument schema: toolsutil.JSONSchemaProperty. `raw`
 * holds a JSON Schema the engine did not derive (a behavior's parameters),
 * written as it is.
 */
export interface Property {
  readonly raw?: JSONSchemaValue;
  /** The scalar's schema name, written under the scalar key. */
  readonly canonicalScalar?: string;
  readonly nullable?: boolean;
  /** false for a closed object. */
  readonly additionalProperties?: false;
  /** A JSON Schema type; `any` for every JSON type. */
  readonly type?: string;
  readonly format?: string;
  readonly description?: string;
  readonly pattern?: string;
  readonly enum?: readonly string[];
  readonly minLength?: number;
  readonly maxLength?: number;
  readonly minimum?: number;
  readonly maximum?: number;
  readonly minItems?: number;
  readonly maxItems?: number;
  readonly items?: Property;
  readonly properties?: ReadonlyMap<string, Property>;
  readonly required?: readonly string[];
  /**
   * JSON Schemas an object also satisfies, written as they are: what a
   * behavior's validate holds an instance's own fields to beyond their
   * types (its instanceSchema). The Go generators write none.
   */
  readonly allOf?: readonly unknown[];
}

/** An argument schema: toolsutil.JSONSchemaObject, a closed object. */
export interface ArgumentSchema {
  /** Written first: ToolKeys.parameters. */
  readonly vendor: ReadonlyArray<{ readonly key: string; readonly value: unknown }>;
  readonly properties: ReadonlyMap<string, Property>;
  /** Sorted. */
  readonly required: readonly string[];
}

/**
 * The JSON types an `any` property takes, as tools/schema.json lists them:
 * every one but null (D14, amended). A nullable one also takes null.
 */
const JSON_VALUE_TYPES: readonly string[] = ['object', 'array', 'string', 'number', 'boolean'];

/** Every JSON type, null included: a result schema that names no type. */
export const ANY_JSON_TYPES: readonly string[] = [...JSON_VALUE_TYPES, 'null'];

// typeValue is a property's JSON Schema type: `any` as every JSON type but
// null, and a nullable type with null after it.
function typeValue(property: Property): string | string[] {
  const nullable = property.nullable === true && property.type !== 'null';
  if (property.type === 'any') {
    return nullable ? [...ANY_JSON_TYPES] : [...JSON_VALUE_TYPES];
  }
  return nullable ? [property.type ?? '', 'null'] : (property.type ?? '');
}

// A scalar's JSON Schema facts: apigen.ScalarJSONSchemaInfo.
interface ScalarInfo {
  canonicalName: string;
  type: string;
  format: string;
  description: string;
  pattern: string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
}

// The builtin primitives, by the names the schema runtime reads: the IR's
// and the GraphQL names.
const PRIMITIVE_INFO: ReadonlyMap<string, ScalarInfo> = (() => {
  const string: ScalarInfo = { canonicalName: '', type: 'string', format: '', description: 'A string value', pattern: '' };
  const number: ScalarInfo = { canonicalName: '', type: 'number', format: '', description: 'A numeric value', pattern: '' };
  const boolean: ScalarInfo = { canonicalName: '', type: 'boolean', format: '', description: 'A boolean value (true or false)', pattern: '' };
  const integer: ScalarInfo = { canonicalName: '', type: 'integer', format: '', description: 'An integer value', pattern: '' };
  return new Map([
    ['string', string],
    ['number', number],
    ['boolean', boolean],
    ['String', string],
    ['ID', string],
    ['Float', number],
    ['Boolean', boolean],
    ['Int', integer],
  ]);
})();

/**
 * FieldSchemas builds the argument schemas of one document's types. It
 * caches each scalar's facts; build one per document.
 */
export class FieldSchemas {
  private readonly scalars = new Map<string, ScalarInfo | undefined>();

  constructor(private readonly document: Document) {}

  /**
   * object is the closed object schema of a type's fields, keyed by their
   * JSON keys: what the SDK generators write as the arguments of an
   * operation whose input is that type.
   */
  object(typeName: string, vendor: ArgumentSchema['vendor'] = []): ArgumentSchema {
    const { properties, required } = this.fields(typeName, new Set());
    return { vendor, properties, required };
  }

  /**
   * type is the schema of a value of one of the document's types: a
   * closed object of its fields, as a field of that type is written,
   * not nullable.
   */
  type(typeName: string): Property {
    const { properties, required } = this.fields(typeName, new Set([typeName]));
    return { type: 'object', description: `${typeName} object`, additionalProperties: false, properties, required };
  }

  /**
   * scalarType is the JSON type of a value of a scalar the document
   * declares or the catalog holds, as a field of it is written: its
   * json_schema type mapping, or the one its primitive gives, `any` for
   * every JSON type; undefined for a name that is no scalar.
   */
  scalarType(typeName: string): string | undefined {
    return refKind(this.document, typeName) === 'scalar' ? this.scalar(typeName)?.type : undefined;
  }

  /** field is the schema of one field's value; toolsutil.FieldToJSONSchemaProperty. */
  field(field: FieldDef, stack: ReadonlySet<string>): Property {
    const typeName = field.typeRef.name;
    let value = this.typeProperty(typeName);
    if (this.scalar(typeName) === undefined && refKind(this.document, typeName) === 'type' && !stack.has(typeName)) {
      const nested = new Set(stack);
      nested.add(typeName);
      const { properties, required } = this.fields(typeName, nested);
      value = { type: 'object', description: `${typeName} object`, additionalProperties: false, properties, required };
    }
    const depth = arrayDepth(field.typeRef);
    if (depth > 0) {
      // The field's value rules go on the innermost items, its list bounds
      // on the outer list.
      value = withRules(value, field, false);
      for (let level = 1; level <= depth; level += 1) {
        value = { type: 'array', description: `Array of ${level > 1 ? 'arrays of ' : ''}${typeName} values`, items: value };
      }
      value = { ...value, minItems: field.validateListMin, maxItems: field.validateListMax };
    } else {
      value = withRules(value, field, true);
    }
    return { ...value, nullable: field.required !== true };
  }

  private fields(typeName: string, stack: ReadonlySet<string>): { properties: Map<string, Property>; required: string[] } {
    const type = (this.document.types ?? {})[typeName] as TypeDef;
    const properties = new Map<string, Property>();
    const required: string[] = [];
    for (const field of type.fields ?? []) {
      const key = jsonKey(field);
      properties.set(key, this.field(field, stack));
      if (field.required === true) {
        required.push(key);
      }
    }
    return { properties, required: required.sort(compareStrings) };
  }

  // typeProperty is toolsutil.typeToJSONSchemaProperty: a scalar's facts, an
  // enum's values, or a string described by the type's name.
  private typeProperty(typeName: string): Property {
    const info = this.scalar(typeName);
    if (info) {
      return omitEmpty({
        canonicalScalar: info.canonicalName,
        type: info.type,
        format: info.format,
        description: info.description,
        pattern: info.pattern,
        minLength: info.minLength,
        maxLength: info.maxLength,
        minimum: info.minimum,
        maximum: info.maximum,
      });
    }
    const property: Property = { type: 'string', description: `A ${typeName} value` };
    const values = refKind(this.document, typeName) === 'enum' ? (this.document.enums ?? {})[typeName].values : [];
    return values.length > 0 ? { ...property, enum: values.map((value) => value.serializedAs || value.name) } : property;
  }

  private scalar(typeName: string): ScalarInfo | undefined {
    if (!this.scalars.has(typeName)) {
      this.scalars.set(typeName, this.scalarInfo(typeName));
    }
    return this.scalars.get(typeName);
  }

  // scalarInfo is the apigen scalar table's row for a type name: a builtin
  // primitive, or a scalar the document declares or the catalog holds.
  private scalarInfo(typeName: string): ScalarInfo | undefined {
    const primitive = PRIMITIVE_INFO.get(typeName);
    if (primitive) {
      return primitive;
    }
    if (refKind(this.document, typeName) !== 'scalar') {
      return undefined;
    }
    const definition = scalarDefinition(this.document, typeName);
    const traits = scalarTraits(typeName, definition);
    let type = definition.typeMappings.json_schema ?? '';
    if (type === '') {
      switch (definition.languagePrimitive) {
        case 'number':
          type = traits.integer ? 'integer' : 'number';
          break;
        case 'boolean':
          type = 'boolean';
          break;
        default:
          type = 'string';
      }
    }
    let format = definition.format;
    if (format === '') {
      format = traits.email ? 'email' : traits.dateTime ? 'date-time' : traits.date ? 'date' : traits.url ? 'uri' : '';
    }
    const description = definition.description.trim() !== '' ? definition.description : definition.comment;
    return {
      canonicalName: typeName,
      type,
      format,
      description: description === '' ? `A ${typeName} value` : description,
      pattern: definition.pattern,
      minLength: definition.minLength > 0 ? definition.minLength : undefined,
      maxLength: definition.maxLength > 0 ? definition.maxLength : undefined,
      minimum: definition.minimum ?? undefined,
      maximum: definition.maximum ?? undefined,
    };
  }
}

// A scalar as the Go loader leaves it: a catalog scalar's row wins over
// what the document says about it, and the document's type mappings stay
// beside the catalog's (loader.hydrateScalarsFromRegistry).
interface ScalarDefinition {
  description: string;
  comment: string;
  languagePrimitive: string;
  minLength: number;
  maxLength: number;
  minimum: number | null;
  maximum: number | null;
  pattern: string;
  format: string;
  typeMappings: Record<string, string>;
}

function scalarDefinition(document: Document, name: string): ScalarDefinition {
  const own = Object.prototype.hasOwnProperty.call(document.scalars ?? {}, name)
    ? ((document.scalars as Record<string, DocumentScalar>)[name] as DocumentScalar)
    : undefined;
  const key = scalarKey(name);
  if (Object.prototype.hasOwnProperty.call(BUILTIN_SCALARS, key)) {
    const row = BUILTIN_SCALARS[key];
    return {
      description: row.description,
      comment: own?.comment ?? '',
      languagePrimitive: languagePrimitiveOf(row.primitive),
      minLength: row.minLength,
      maxLength: row.maxLength,
      minimum: row.minimum,
      maximum: row.maximum,
      pattern: row.pattern,
      format: row.format,
      typeMappings: { ...(own?.typeMappings ?? {}), ...row.typeMappings },
    };
  }
  const declared = own as DocumentScalar;
  return {
    description: declared.description ?? '',
    comment: declared.comment ?? '',
    languagePrimitive: declared.languagePrimitive,
    minLength: declared.minLength ?? 0,
    maxLength: declared.maxLength ?? 0,
    minimum: declared.minimum ?? null,
    maximum: declared.maximum ?? null,
    pattern: declared.pattern ?? '',
    format: declared.format ?? '',
    typeMappings: { ...(declared.typeMappings ?? {}) },
  };
}

// languagePrimitiveOf is ir.CatalogLanguagePrimitive: the language primitive
// the Go loader gives a scalar it fills in from a catalog row.
function languagePrimitiveOf(primitive: string): string {
  switch (primitive.trim().toLowerCase()) {
    case 'string':
    case 'str':
      return 'string';
    case 'number':
    case 'float':
    case 'float64':
    case 'int':
    case 'int32':
    case 'int64':
    case 'integer':
      return 'number';
    case 'bool':
    case 'boolean':
      return 'boolean';
    default:
      return 'object';
  }
}

interface Traits {
  integer: boolean;
  email: boolean;
  dateTime: boolean;
  date: boolean;
  url: boolean;
}

// scalarTraits is the part of codegen.BuildScalarTraits the scalar table
// reads: from the name's last segment, the type mappings and the format.
function scalarTraits(name: string, definition: ScalarDefinition): Traits {
  const traits: Traits = { integer: false, email: false, dateTime: false, date: false, url: false };
  const segments = name.trim().split('.');
  switch (segments[segments.length - 1].toLowerCase()) {
    case 'datetime':
      traits.dateTime = true;
      break;
    case 'date':
      traits.date = true;
      break;
    case 'int':
    case 'int32':
    case 'int64':
    case 'integer':
      traits.integer = true;
      break;
    case 'email':
      traits.email = true;
      break;
    case 'url':
    case 'uri':
      traits.url = true;
      break;
  }
  for (const mapped of Object.values(definition.typeMappings)) {
    const normalized = mapped.trim().toLowerCase();
    if (['int', 'int32', 'int64', 'integer'].includes(normalized)) {
      traits.integer = true;
    }
    if (['time.time', 'datetime', 'datetime.datetime', 'jsdate'].includes(normalized) || normalized.includes('time.time')) {
      traits.dateTime = true;
    }
  }
  switch (definition.format.trim().toLowerCase()) {
    case 'date-time':
      traits.dateTime = true;
      break;
    case 'date':
      traits.date = true;
      break;
    case 'email':
      traits.email = true;
      break;
    case 'uri':
    case 'url':
      traits.url = true;
      break;
  }
  return traits;
}

// withRules is toolsutil.applyFieldValidation: a field's own lengths,
// pattern and range replace its scalar's, and a range is truncated to an
// integer as the Go generator stores it. Its list bounds apply only where
// the field is not a list.
function withRules(property: Property, field: FieldDef, listBounds: boolean): Property {
  return omitEmpty({
    ...property,
    minLength: field.validateMinLength ?? property.minLength,
    maxLength: field.validateMaxLength ?? property.maxLength,
    minItems: listBounds ? (field.validateListMin ?? property.minItems) : property.minItems,
    maxItems: listBounds ? (field.validateListMax ?? property.maxItems) : property.maxItems,
    pattern: field.validatePattern || property.pattern,
    minimum: field.validateMin !== undefined ? Math.trunc(field.validateMin) : property.minimum,
    maximum: field.validateMax !== undefined ? Math.trunc(field.validateMax) : property.maximum,
  });
}

// omitEmpty drops undefined members and empty strings, which the Go
// encoder omits.
function omitEmpty(property: Property): Property {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(property)) {
    if (value !== undefined && value !== '') {
      out[key] = value;
    }
  }
  return out as Property;
}

/**
 * renderProperty writes a property as tools/schema.json does
 * (toolsutil.JSONSchemaPropertyLiteral): keys sorted, the scalar name
 * under scalarKey when it is not empty, `any` as every JSON type but null,
 * and a nullable type, enum or list with null.
 */
export function renderProperty(property: Property, scalarKeyName: string): unknown {
  if (property.raw !== undefined) {
    return property.raw;
  }
  const out: Record<string, unknown> = {};
  const nullable = property.nullable === true && property.type !== 'any' && property.type !== 'null';
  if (property.additionalProperties !== undefined) {
    out.additionalProperties = property.additionalProperties;
  }
  if (property.description !== undefined) out.description = property.description;
  if (property.enum !== undefined && property.enum.length > 0) out.enum = nullable ? [...property.enum, null] : [...property.enum];
  if (property.format !== undefined) out.format = property.format;
  if (property.items !== undefined) out.items = renderProperty(property.items, scalarKeyName);
  if (property.maxItems !== undefined) out.maxItems = property.maxItems;
  if (property.maxLength !== undefined) out.maxLength = property.maxLength;
  if (property.maximum !== undefined) out.maximum = property.maximum;
  if (property.minItems !== undefined) out.minItems = property.minItems;
  if (property.minLength !== undefined) out.minLength = property.minLength;
  if (property.minimum !== undefined) out.minimum = property.minimum;
  if (property.pattern !== undefined) out.pattern = property.pattern;
  if (property.properties !== undefined && property.properties.size > 0) out.properties = renderProperties(property.properties, scalarKeyName);
  if (property.required !== undefined && property.required.length > 0) out.required = [...property.required];
  if (property.allOf !== undefined && property.allOf.length > 0) out.allOf = [...property.allOf];
  out.type = typeValue(property);
  if (scalarKeyName !== '' && property.canonicalScalar !== undefined) {
    out[scalarKeyName] = property.canonicalScalar;
  }
  // The members above are in order but for the scalar key, which sorts
  // wherever its name falls; what they hold is sorted already.
  return sortMembers(out);
}

/** renderProperties writes properties by key, sorted. */
export function renderProperties(properties: ReadonlyMap<string, Property>, scalarKeyName: string): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of [...properties.keys()].sort(compareStrings)) {
    out[key] = renderProperty(properties.get(key) as Property, scalarKeyName);
  }
  return out;
}

/**
 * renderArguments writes an argument schema as tools/schema.json does:
 * type, additionalProperties, the vendor keys, properties, and required
 * when it lists a key.
 */
export function renderArguments(schema: ArgumentSchema, scalarKeyName: string): Record<string, unknown> {
  const out: Record<string, unknown> = { type: 'object', additionalProperties: false };
  for (const { key, value } of schema.vendor) {
    out[key] = value;
  }
  out.properties = renderProperties(schema.properties, scalarKeyName);
  if (schema.required.length > 0) {
    out.required = [...schema.required];
  }
  return out;
}

/**
 * typeArguments is the tool argument schema the SDK generators write for
 * an operation whose input is one of a document's types, keyed by its
 * fields' JSON keys, with the digest they write beside it
 * (tools/schema.json's parameters and inputSchemaDigest).
 */
export function typeArguments(
  document: Document,
  typeName: string,
  keys: ToolKeys = DEFAULT_TOOL_KEYS
): { parameters: Record<string, unknown>; inputSchemaDigest: string } {
  if (refKind(document, typeName) !== 'type') {
    throw new TypeError(`${typeName} is not a type of the document`);
  }
  const schema = new FieldSchemas(document).object(typeName, keys.parameters);
  return { parameters: renderArguments(schema, keys.scalar), inputSchemaDigest: argumentsDigest(schema, keys.scalar) };
}

/** argumentsDigest is inputSchemaDigest: sha256: and the hex SHA-256 of the Go encoding. */
export function argumentsDigest(schema: ArgumentSchema, scalarKeyName: string): string {
  return `sha256:${createHash('sha256').update(encodeArguments(schema, scalarKeyName), 'utf8').digest('hex')}`;
}

/**
 * encodeArguments writes an argument schema as the Go encoder writes a
 * toolsutil.JSONSchemaObject: the text inputSchemaDigest hashes.
 */
export function encodeArguments(schema: ArgumentSchema, scalarKeyName: string): string {
  const members = schema.vendor.map(({ key, value }) => `${goString(key)}:${goJSON(value)}`);
  members.push('"additionalProperties":false', '"type":"object"');
  members.push(`"properties":${encodeProperties(schema.properties, scalarKeyName)}`);
  if (schema.required.length > 0) {
    members.push(`"required":${goJSON(schema.required)}`);
  }
  return `{${members.join(',')}}`;
}

function encodeProperties(properties: ReadonlyMap<string, Property>, scalarKeyName: string): string {
  return `{${[...properties.keys()]
    .sort(compareStrings)
    .map((key) => `${goString(key)}:${encodeProperty(properties.get(key) as Property, scalarKeyName)}`)
    .join(',')}}`;
}

// encodeProperty is toolsutil.JSONSchemaProperty.MarshalJSON. A nullable
// property is a struct that embeds the plain one and redeclares type and
// enum, and an `any` one a struct that redeclares type, which Go writes
// after every embedded member.
function encodeProperty(property: Property, scalarKeyName: string): string {
  if (property.raw !== undefined) {
    return goJSON(property.raw);
  }
  const members: string[] = [];
  if (scalarKeyName !== '' && property.canonicalScalar !== undefined) {
    members.push(`${goString(scalarKeyName)}:${goString(property.canonicalScalar)}`);
  }
  const any = property.type === 'any';
  const nullable = property.nullable === true && !any && property.type !== 'null';
  if (property.additionalProperties !== undefined) members.push('"additionalProperties":false');
  if (!nullable && !any) members.push(`"type":${goString(property.type ?? '')}`);
  if (property.format !== undefined) members.push(`"format":${goString(property.format)}`);
  if (property.description !== undefined) members.push(`"description":${goString(property.description)}`);
  if (property.pattern !== undefined) members.push(`"pattern":${goString(property.pattern)}`);
  const values = property.enum !== undefined && property.enum.length > 0 ? property.enum : undefined;
  if (!nullable && values) members.push(`"enum":${goJSON(values)}`);
  for (const key of ['minLength', 'maxLength', 'minimum', 'maximum', 'minItems', 'maxItems'] as const) {
    if (property[key] !== undefined) members.push(`"${key}":${goJSON(property[key])}`);
  }
  if (property.items !== undefined) members.push(`"items":${encodeProperty(property.items, scalarKeyName)}`);
  if (property.properties !== undefined && property.properties.size > 0) {
    members.push(`"properties":${encodeProperties(property.properties, scalarKeyName)}`);
  }
  if (property.required !== undefined && property.required.length > 0) members.push(`"required":${goJSON(property.required)}`);
  if (nullable || any) members.push(`"type":${goJSON(typeValue(property))}`);
  if (nullable && values) members.push(`"enum":${goJSON([...values, null])}`);
  if (property.allOf !== undefined && property.allOf.length > 0) members.push(`"allOf":${goJSON(property.allOf)}`);
  return `{${members.join(',')}}`;
}

/**
 * goJSON writes a JSON value as Go's encoding/json does: object keys
 * sorted, and <, >, &, U+2028 and U+2029 escaped.
 */
export function goJSON(value: unknown): string {
  if (value === null || typeof value === 'number' || typeof value === 'boolean') {
    return JSON.stringify(value);
  }
  if (typeof value === 'string') {
    return goString(value);
  }
  if (Array.isArray(value)) {
    return `[${value.map((element) => goJSON(element)).join(',')}]`;
  }
  if (typeof value === 'object') {
    const object = value as Record<string, unknown>;
    return `{${Object.keys(object)
      .filter((key) => object[key] !== undefined)
      .sort(compareStrings)
      .map((key) => `${goString(key)}:${goJSON(object[key])}`)
      .join(',')}}`;
  }
  return 'null';
}

function goString(value: string): string {
  return JSON.stringify(value).replace(/[<>&\u2028\u2029]/g, (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, '0')}`);
}

function sortMembers(object: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of Object.keys(object).sort(compareStrings)) {
    out[key] = object[key];
  }
  return out;
}

/** sortKeys copies a JSON value with every object's keys sorted. */
export function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortKeys);
  }
  if (typeof value === 'object' && value !== null) {
    const object = value as Record<string, unknown>;
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(object).sort(compareStrings)) {
      out[key] = sortKeys(object[key]);
    }
    return out;
  }
  return value;
}

// compareStrings orders by UTF-16 code units, which is byte order for the
// ASCII keys a schema's fields and JSON Schema's keywords use.
function compareStrings(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}
