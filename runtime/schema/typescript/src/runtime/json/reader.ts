import type {
  ArgumentDef,
  DefinitionKind,
  EnumDef,
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
  TypeDef,
  TypeRef,
  UnionDef,
} from "../validation/types";
import { BUILTIN_SCALARS } from "../builtin-scalars.generated";

type JsonObject = Record<string, unknown>;
const builtinScalars = BUILTIN_SCALARS;

const scalarConstraintKeys = new Set([
  "minLength",
  "maxLength",
  "pattern",
  "format",
  "minimum",
  "maximum",
  "x-example",
  "x-reservedWords",
  "x-caseInsensitive",
  "x-reservedWordsCaseInsensitive",
  "x-reservedWordsMatchPartial",
  "x-typeMapping",
  "x-fileUpload",
  "x-imageConstraints",
  "x-hasCustomNormalize",
  "x-hasCustomValidate",
  "x-hasCustomParse",
  "x-scalar",
]);

function isObject(value: unknown): value is JsonObject {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function asObject(value: unknown, path: string): JsonObject {
  if (!isObject(value)) {
    throw new Error(`runtime schema parse error: expected object at ${path}`);
  }
  return value;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function asBoolean(value: unknown): boolean {
  return typeof value === "boolean" ? value : false;
}

function asNumberOrNull(value: unknown): number | null {
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function asIntOrNull(value: unknown): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return null;
  }
  return Math.trunc(value);
}

function asPositiveInt(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return 0;
  }
  const asInt = Math.trunc(value);
  return asInt > 0 ? asInt : 0;
}

function asStringArray(value: unknown, path: string): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const items: string[] = [];
  for (const item of value) {
    if (typeof item !== "string") {
      throw new Error(
        `runtime schema parse error: expected string array at ${path}`,
      );
    }
    items.push(item);
  }
  return items;
}

function asTruthy(value: unknown): boolean {
  if (typeof value === "boolean") {
    return value;
  }
  if (isObject(value)) {
    return true;
  }
  return value !== null && value !== undefined;
}

function formatDefaultValue(value: unknown): string {
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return String(value);
}

function mapJsonTypeToPrimitive(typeName: string): string {
  switch (typeName) {
    case "string":
      return "String";
    case "integer":
      return "Int";
    case "number":
      return "Float";
    case "boolean":
      return "Boolean";
    case "object":
      return "JSON";
    default:
      return "String";
  }
}

function resolveRefName(ref: string): string {
  const parts = ref.split("/");
  const scalarsIdx = parts.indexOf("scalars");
  if (scalarsIdx !== -1 && scalarsIdx < parts.length - 1) {
    return parts
      .slice(scalarsIdx + 1)
      .join("_")
      .replace(/\./g, "_");
  }
  return parts[parts.length - 1] ?? "";
}

function parseRequiredSet(definition: JsonObject): Set<string> {
  const required = definition.required;
  const values = asStringArray(required, "definition.required");
  return new Set(values);
}

function parseIndexes(definition: JsonObject): IndexDef[] {
  const indexesRaw = definition["x-indexes"];
  if (!Array.isArray(indexesRaw)) {
    return [];
  }
  const indexes: IndexDef[] = [];
  for (const indexRaw of indexesRaw) {
    if (!isObject(indexRaw)) {
      continue;
    }
    indexes.push({
      keys: asStringArray(indexRaw.keys, "definition.x-indexes[].keys"),
      unique: asBoolean(indexRaw.unique),
    });
  }
  return indexes;
}

function parseMiddlewareConfig(raw: JsonObject): MiddlewareConfig | null {
  const rateLimitRaw = raw["x-rateLimit"];
  const bodyLimitRaw = raw["x-bodyLimit"];
  const timeoutRaw = raw["x-timeout"];
  const encrypted = asBoolean(raw["x-encrypted"]);
  if (
    !isObject(rateLimitRaw) &&
    !isObject(bodyLimitRaw) &&
    !isObject(timeoutRaw) &&
    !encrypted
  ) {
    return null;
  }

  const rateLimit = isObject(rateLimitRaw)
    ? asNumberOrNull(rateLimitRaw.requestsPerMinute)
    : null;
  const bodyLimit = isObject(bodyLimitRaw)
    ? asNumberOrNull(bodyLimitRaw.megabytes)
    : null;
  const timeout = isObject(timeoutRaw)
    ? asNumberOrNull(timeoutRaw.seconds)
    : null;

  return {
    rateLimit: rateLimit === null ? null : Math.trunc(rateLimit),
    bodyLimit: bodyLimit === null ? null : Math.trunc(bodyLimit),
    timeout: timeout === null ? null : Math.trunc(timeout),
    encrypted,
  };
}

function parseRelationDef(rawRelation: unknown): RelationDef | null {
  if (!isObject(rawRelation)) {
    return null;
  }
  return {
    type: asString(rawRelation.type),
    field: asString(rawRelation.field),
  };
}

function parseArgumentDef(
  name: string,
  rawArg: unknown,
  path: string,
): ArgumentDef {
  const arg = asObject(rawArg, path);
  const defaultValue =
    "default" in arg && arg.default !== null
      ? formatDefaultValue(arg.default)
      : null;
  return {
    name,
    description: asString(arg.description),
    typeRef: parseTypeRef(arg, path),
    required: asBoolean(arg["x-required"]),
    defaultValue,
    isQuery: asString(arg["x-paramType"]) === "query",
  };
}

function parseArguments(rawField: JsonObject, path: string): ArgumentDef[] {
  const argumentsRaw = rawField["x-arguments"];
  if (!isObject(argumentsRaw)) {
    return [];
  }
  const args: ArgumentDef[] = [];
  for (const [argName, rawArg] of Object.entries(argumentsRaw)) {
    args.push(
      parseArgumentDef(argName, rawArg, `${path}.x-arguments.${argName}`),
    );
  }
  return args;
}

function parseTypeRef(raw: JsonObject, path: string): TypeRef {
  const ref = raw.$ref;
  if (typeof ref === "string" && ref.length > 0) {
    const resolved = resolveRefName(ref);
    if (!resolved) {
      throw new Error(`runtime schema parse error: invalid $ref at ${path}`);
    }
    return { name: resolved, isArray: false, elemNonNull: true };
  }

  const typeName = asString(raw.type);
  if (typeName === "array") {
    const itemsRaw = raw.items;
    if (itemsRaw === undefined) {
      throw new Error(
        `runtime schema parse error: array without items at ${path}`,
      );
    }
    const items = asObject(itemsRaw, `${path}.items`);
    const inner = parseTypeRef(items, `${path}.items`);
    if (inner.isArrayOfArrays) {
      throw new Error(
        `runtime schema parse error: arrays nest at most two levels (T[][]) at ${path}`,
      );
    }
    if (inner.isArray) {
      return {
        name: inner.name,
        isArray: true,
        isArrayOfArrays: true,
        elemNonNull: true,
      };
    }
    return {
      name: inner.name,
      isArray: true,
      elemNonNull: true,
    };
  }

  if (typeName.length > 0) {
    return {
      name: mapJsonTypeToPrimitive(typeName),
      isArray: false,
      elemNonNull: true,
    };
  }

  return { name: "JSON", isArray: false, elemNonNull: true };
}

function parseFieldDef(
  fieldName: string,
  rawField: unknown,
  requiredSet: Set<string>,
  path: string,
): FieldDef {
  const field = asObject(rawField, path);
  const autoGenerated = asBoolean(field["x-autoGenerated"]);
  const required = requiredSet.has(fieldName) && !autoGenerated;
  const jsonTag = asString(field["x-jsonTag"]);
  const defaultValue =
    "default" in field && field.default !== null
      ? formatDefaultValue(field.default)
      : null;
  const middleware = parseMiddlewareConfig(field);
  if (field["x-validateScalar"] === true) {
    // Injection happens in the Go legacy parser; a legacy-shaped schema
    // reaching this reader with the flag would parse with NO constraints.
    throw new Error(
      `field "${fieldName}": x-validateScalar requires the wire schema form`
    );
  }
  const title = asString(field.title);
  const purpose = asString(field["x-purpose"]);
  const icon = asString(field["x-icon"]);
  const placeholder = asString(field["x-placeholder"]);
  const fieldDef: FieldDef = {
    name: fieldName,
    description: asString(field.description),
    jsonKey: jsonTag || fieldName,
    required,
    autoGenerated,
    defaultValue,
    validateMin: asNumberOrNull(field["x-validateMin"]),
    validateMax: asNumberOrNull(field["x-validateMax"]),
    validateMinLength: asIntOrNull(field["x-validateMinLength"]),
    validateMaxLength: asIntOrNull(field["x-validateMaxLength"]),
    validateListMin: asIntOrNull(field["x-validateListMin"]),
    validateListMax: asIntOrNull(field["x-validateListMax"]),
    validatePattern: asString(field["x-validatePattern"]),
    arguments: parseArguments(field, path),
    key: asBoolean(field["x-key"]),
    unique: asBoolean(field["x-unique"]),
    searchField: asBoolean(field["x-searchField"]),
    relation: parseRelationDef(field["x-relation"]),
    hasMany: asTruthy(field["x-hasMany"]),
    manyToMany: asTruthy(field["x-manyToMany"]),
    jsonField: asBoolean(field["x-jsonField"]),
    secret: asBoolean(field["x-secret"]),
    uiHidden: asBoolean(field["x-uiHidden"]),
    semanticRole: asString(field["x-semantic-role"]),
    temporalFormat: asString(field["x-temporal-format"]),
    transformDedupKey: asBoolean(field["x-transformDedupKey"]),
    transformOrdering: asBoolean(field["x-transformOrdering"]),
    transformFingerprintInput: asBoolean(field["x-transformFingerprintInput"]),
    transformPartitionDate: asBoolean(field["x-transformPartitionDate"]),
    transformStructural: asBoolean(field["x-transformStructural"]),
    transformPersonEmail: asBoolean(field["x-transformPersonEmail"]),
    transformPersonName: asBoolean(field["x-transformPersonName"]),
    transformAccountId: asBoolean(field["x-transformAccountId"]),
    transformExternalUserId: asBoolean(field["x-transformExternalUserId"]),
    exclude: asBoolean(field["x-exclude"]),
    internalMetadata: asBoolean(field["x-internal-metadata"]),
    auth: asBoolean(field["x-auth"]),
    encrypted: middleware?.encrypted ?? asBoolean(field["x-encrypted"]),
    requireOwnership: asBoolean(field["x-requireOwnership"]),
    permissions: asStringArray(field["x-permissions"], `${path}.x-permissions`),
    restMethod: asString(field["x-restMethod"]),
    paramType: asString(field["x-paramType"]),
    middleware,
    typeRef: parseTypeRef(field, path),
  };
  // Set only when non-empty so round-trip output stays free of noise keys.
  if (title) {
    fieldDef.title = title;
  }
  if (purpose) {
    fieldDef.purpose = purpose;
  }
  if (icon) {
    fieldDef.icon = icon;
  }
  if (placeholder) {
    fieldDef.placeholder = placeholder;
  }
  return fieldDef;
}

function parseTypeDefinition(
  name: string,
  definition: JsonObject,
  kind: DefinitionKind,
  path: string,
): TypeDef {
  if (kind !== "type" && kind !== "input") {
    throw new Error(
      `runtime schema parse error: unsupported kind "${kind}" for ${name}`,
    );
  }
  const propertiesRaw = definition.properties;
  if (!isObject(propertiesRaw)) {
    throw new Error(
      `runtime schema parse error: ${name} must define object properties`,
    );
  }

  const requiredSet = parseRequiredSet(definition);
  const fields: FieldDef[] = [];
  for (const [fieldName, rawField] of Object.entries(propertiesRaw)) {
    fields.push(
      parseFieldDef(
        fieldName,
        rawField,
        requiredSet,
        `${path}.properties.${fieldName}`,
      ),
    );
  }

  return {
    name,
    description: asString(definition.description),
    kind: kind === "input" ? "input" : "object",
    fields,
    indexes: parseIndexes(definition),
    envVars: asBoolean(definition["x-envVars"]),
  };
}

function parseEnumDefinition(
  name: string,
  definition: JsonObject,
  path: string,
): EnumDef {
  const enumValues = asStringArray(definition.enum, `${path}.enum`);
  const enumMetadataRaw = definition["x-enumValues"];
  const enumMetadata = isObject(enumMetadataRaw) ? enumMetadataRaw : {};

  const values = enumValues.map((valueName) => {
    const metadata = enumMetadata[valueName];
    const metadataObj = isObject(metadata) ? metadata : {};
    const serializedAs = asString(metadataObj.serializedAs);
    return {
      name: valueName,
      description: asString(metadataObj.description),
      serializedAs,
    };
  });

  return {
    name,
    owner: asString(definition["x-owner"]),
    description: asString(definition.description),
    values,
  };
}

function parseFileUploadConfig(
  definition: JsonObject,
): FileUploadConfig | null {
  const fileUploadRaw = definition["x-fileUpload"];
  if (!isObject(fileUploadRaw)) {
    return null;
  }
  return {
    maxSize: asPositiveInt(fileUploadRaw.maxSize),
    allowedTypes: asStringArray(
      fileUploadRaw.allowedTypes,
      "scalar.x-fileUpload.allowedTypes",
    ),
    category: asString(fileUploadRaw.category),
  };
}

function parseImageConstraints(
  definition: JsonObject,
): ImageConstraints | null {
  const imageConstraintsRaw = definition["x-imageConstraints"];
  if (!isObject(imageConstraintsRaw)) {
    return null;
  }
  return {
    maxWidth: asPositiveInt(imageConstraintsRaw.maxWidth),
    maxHeight: asPositiveInt(imageConstraintsRaw.maxHeight),
    minAspectRatio: asNumberOrNull(imageConstraintsRaw.minAspectRatio),
    maxAspectRatio: asNumberOrNull(imageConstraintsRaw.maxAspectRatio),
    requireTransparency: asBoolean(imageConstraintsRaw.requireTransparency),
  };
}

function parseScalarDefinition(
  name: string,
  definition: JsonObject,
): ScalarDef {
  const typeMappingsRaw = definition["x-typeMapping"];
  const typeMappings = isObject(typeMappingsRaw) ? typeMappingsRaw : {};
  const parsedTypeMappings: Record<string, string> = {};
  for (const [lang, mappedType] of Object.entries(typeMappings)) {
    if (typeof mappedType === "string" && mappedType.length > 0) {
      parsedTypeMappings[lang] = mappedType;
    }
  }

  const pattern = asString(definition.pattern);
  if (pattern) {
    try {
      new RegExp(pattern);
    } catch (error) {
      throw new Error(
        `runtime schema parse error: invalid regex pattern for scalar ${name}: ${String(error)}`,
      );
    }
  }

  let example = asString(definition["x-example"]);
  if (
    !example &&
    Array.isArray(definition.examples) &&
    definition.examples.length > 0
  ) {
    example = formatDefaultValue(definition.examples[0]);
  }

  return {
    name,
    description: asString(definition.description),
    primitive: mapJsonTypeToPrimitive(asString(definition.type)),
    minLength: asPositiveInt(definition.minLength),
    maxLength: asPositiveInt(definition.maxLength),
    pattern,
    format: asString(definition.format),
    reservedWords: asStringArray(
      definition["x-reservedWords"],
      `scalar ${name}.x-reservedWords`,
    ),
    caseInsensitive: asBoolean(definition["x-caseInsensitive"]),
    reservedWordsCaseInsensitive: asBoolean(
      definition["x-reservedWordsCaseInsensitive"],
    ),
    reservedWordsMatchPartial: asBoolean(
      definition["x-reservedWordsMatchPartial"],
    ),
    minimum: asNumberOrNull(definition.minimum),
    maximum: asNumberOrNull(definition.maximum),
    example,
    fileUpload: parseFileUploadConfig(definition),
    imageConstraints: parseImageConstraints(definition),
    hasCustomNormalize: asBoolean(definition["x-hasCustomNormalize"]),
    hasCustomValidate: asBoolean(definition["x-hasCustomValidate"]),
    hasCustomParse: asBoolean(definition["x-hasCustomParse"]),
    typeMappings: parsedTypeMappings,
  };
}

function parseImports(root: JsonObject): Import[] {
  const importsRaw = root["x-imports"];
  if (!Array.isArray(importsRaw)) {
    return [];
  }

  const imports: Import[] = [];
  for (const importRaw of importsRaw) {
    if (!isObject(importRaw)) {
      continue;
    }

    const from = asString(importRaw.from);
    if (!from) {
      continue;
    }

    const typesRaw = importRaw.types;
    let types: string[] = [];
    if (typeof typesRaw === "string") {
      types = [typesRaw];
    } else if (Array.isArray(typesRaw)) {
      types = typesRaw.filter(
        (typeName): typeName is string => typeof typeName === "string",
      );
    }
    imports.push({ from, types });
  }
  return imports;
}

function parseUnionDefinitions(root: JsonObject): Record<string, UnionDef> {
  const unionsRaw = root["x-unions"];
  if (!isObject(unionsRaw)) {
    return {};
  }

  const unions: Record<string, UnionDef> = {};
  for (const [name, unionRaw] of Object.entries(unionsRaw)) {
    if (!isObject(unionRaw)) {
      continue;
    }

    const types: string[] = [];
    const anyOfRaw = unionRaw.anyOf;
    if (Array.isArray(anyOfRaw)) {
      for (const memberRaw of anyOfRaw) {
        if (!isObject(memberRaw)) {
          continue;
        }
        const ref = asString(memberRaw.$ref);
        if (!ref) {
          continue;
        }
        const resolved = resolveRefName(ref);
        if (resolved) {
          types.push(resolved);
        }
      }
    }

    unions[name] = {
      name,
      description: asString(unionRaw.description),
      types,
    };
  }

  return unions;
}

function parseOperationSet(
  raw: unknown,
  path: string,
): OperationSet | undefined {
  if (!isObject(raw)) {
    return undefined;
  }
  const operations: FieldDef[] = [];
  const requiredSet = parseRequiredSet(raw);
  const propertiesRaw = raw.properties;
  if (isObject(propertiesRaw)) {
    for (const [operationName, operationRaw] of Object.entries(propertiesRaw)) {
      operations.push(
        parseFieldDef(
          operationName,
          operationRaw,
          requiredSet,
          `${path}.properties.${operationName}`,
        ),
      );
    }
  }

  const middleware = parseMiddlewareConfig(raw);

  return {
    operations,
    middleware,
    encrypted: middleware?.encrypted ?? asBoolean(raw["x-encrypted"]),
  };
}

function looksLikeScalarDefinition(definition: JsonObject): boolean {
  if (typeof definition["x-scalar"] === "string") {
    return true;
  }

  if (isObject(definition["x-typeMapping"])) {
    return true;
  }

  for (const key of scalarConstraintKeys) {
    if (key in definition) {
      return true;
    }
  }

  const typeName = asString(definition.type);
  return (
    typeName === "string" ||
    typeName === "integer" ||
    typeName === "number" ||
    typeName === "boolean"
  );
}

export function parseSchema(schema: unknown): Schema {
  const root = asObject(schema, "root");
  const definitionsRaw = root.definitions;
  const definitions = isObject(definitionsRaw) ? definitionsRaw : {};

  const scalars: Record<string, ScalarDef> = {};
  const enums: Record<string, EnumDef> = {};
  const types: Record<string, TypeDef> = {};
  const inputs: Record<string, TypeDef> = {};

  for (const [name, rawDefinition] of Object.entries(definitions)) {
    const definition = asObject(rawDefinition, `definitions.${name}`);
    const kind = asString(definition["x-kind"]);

    if (kind === "type") {
      types[name] = parseTypeDefinition(
        name,
        definition,
        "type",
        `definitions.${name}`,
      );
      continue;
    }

    if (kind === "input") {
      inputs[name] = parseTypeDefinition(
        name,
        definition,
        "input",
        `definitions.${name}`,
      );
      continue;
    }

    if (kind === "enum") {
      enums[name] = parseEnumDefinition(
        name,
        definition,
        `definitions.${name}`,
      );
      continue;
    }

    if (kind.length > 0) {
      throw new Error(
        `runtime schema parse error: unsupported x-kind "${kind}" for definition ${name}`,
      );
    }

    if (looksLikeScalarDefinition(definition)) {
      scalars[name] = parseScalarDefinition(name, definition);
    }
  }

  const parsed: Schema = {
    name: asString(root.title) || undefined,
    kind: asString(root["x-kind"]) || undefined,
    description: asString(root.description) || undefined,
    rootType: asString(root["x-rootType"]) || undefined,
    imports: parseImports(root),
    scalars: {
      ...builtinScalars,
      ...scalars,
    },
    enums,
    types,
    inputs,
    unions: parseUnionDefinitions(root),
    queries: parseOperationSet(root["x-queries"], "x-queries"),
    mutations: parseOperationSet(root["x-mutations"], "x-mutations"),
  };

  return parsed;
}

export function parseSchemaJson(schemaJson: string): Schema {
  let parsed: unknown;
  try {
    parsed = JSON.parse(schemaJson) as unknown;
  } catch (error) {
    throw new Error(
      `runtime schema parse error: invalid JSON: ${String(error)}`,
    );
  }
  return parseSchema(parsed);
}
