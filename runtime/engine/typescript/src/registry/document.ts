/*
What a schema is to the engine. A schema is one JSON schema-file document
(the form `superschematic format --to=json` writes) that the strict loader
of @superschematic/schema-runtime reads against the deployment's
meta-schema. On top of the loader, the engine requires:

- `kind: General` and a `name` that fits in a URL path segment;
- no `imports` (a type in another schema is reached through a link
  behavior) and no `operationSets` (the engine serves create, get, list,
  update and delete, and behaviors add their own);
- an instance type: the type named like the schema, or its only type;
- field types the schema runtime validates: a builtin primitive, a scalar,
  an enum or a type of the document, alone, as a list or a list of lists.
  A union or a map is refused, since the runtime checks neither;
- behaviors the engine has implementations for, composed as the compiler's
  loader requires (behaviors/composition.ts), which the caller checks
  through readSchema's compose argument.
*/

import { BUILTIN_SCALARS, SchemaFileError, type LoadedSchemaFile, type SchemaFileLoader } from '@superschematic/schema-runtime';
import type { Document, FieldDef, TypeDef, TypeRef } from '@superschematic/schema-ir/schema-file';

import { EngineError, SchemaDocumentError, type SchemaIssue } from '../errors.js';

/** A schema name: a letter, then letters, digits, `_` and `-`, at most 128 characters. */
export const SCHEMA_NAME = /^[A-Za-z][A-Za-z0-9_-]{0,127}$/;

/** checkSchemaName refuses a schema name the engine would never store. */
export function checkSchemaName(name: string): void {
  if (typeof name !== 'string' || !SCHEMA_NAME.test(name)) {
    throw new EngineError('invalid_argument', `schema name "${String(name)}" must match ${SCHEMA_NAME.source}`);
  }
}

/** A schema document the engine accepted. */
export interface SchemaModel {
  name: string;
  /** The type that holds instances. */
  instanceType: string;
  document: Document;
  /** The document as the loader's canonical JSON: compact, keys sorted. */
  canonical: string;
}

/** What a field's type name refers to. */
export type RefKind = 'primitive' | 'scalar' | 'enum' | 'type' | 'union' | 'unknown';

// The builtin names the schema runtime checks: the IR's primitives and
// the GraphQL names its JSON Schema reader produces.
const PRIMITIVES = new Set(['string', 'number', 'boolean', 'String', 'ID', 'Int', 'Float', 'Boolean']);

/**
 * readSchema loads a schema document from its JSON text and applies the
 * engine's rules, and compose's to its behaviors when it has an instance
 * type. It throws SchemaDocumentError with every issue it finds.
 */
export function readSchema(
  loader: SchemaFileLoader,
  text: string,
  source: string,
  compose: (model: SchemaModel) => SchemaIssue[]
): SchemaModel {
  let loaded: LoadedSchemaFile;
  try {
    loaded = loader.load(text, source);
  } catch (error) {
    if (error instanceof SchemaFileError) {
      throw new SchemaDocumentError(error.source, error.issues);
    }
    throw error;
  }
  const { document, canonical } = loaded;
  const issues: SchemaIssue[] = [];

  if (document.kind !== 'General') {
    issues.push({
      path: '/kind',
      message:
        document.kind === undefined
          ? 'a schema is a document of kind General; this file has no kind'
          : `a schema is a document of kind General, not ${document.kind}`,
    });
  }
  const name = document.name;
  if (name === undefined || name === '') {
    issues.push({ path: '/name', message: 'a schema needs a name' });
  } else if (!SCHEMA_NAME.test(name)) {
    issues.push({ path: '/name', message: `schema name "${name}" must match ${SCHEMA_NAME.source}` });
  }
  if (document.imports && document.imports.length > 0) {
    issues.push({
      path: '/imports',
      message: 'a schema has no imports; reach a type in another schema through a link behavior',
    });
  }
  if (document.operationSets && document.operationSets.length > 0) {
    issues.push({
      path: '/operationSets',
      message: 'a schema declares no operations; the engine serves create, get, list, update and delete, and behaviors add their own',
    });
  }
  const types = document.types ?? {};
  const instanceType = name === undefined ? undefined : instanceTypeOf(document, name);
  if (name !== undefined && instanceType === undefined) {
    const found = Object.keys(types);
    issues.push({
      path: '/types',
      message: `no instance type: declare a type named ${name} or exactly one type (found: ${found.length > 0 ? found.join(', ') : 'none'})`,
    });
  }
  if (instanceType !== undefined) {
    for (const typeName of reachableTypes(document, instanceType)) {
      (types[typeName].fields ?? []).forEach((field, index) => {
        const issue = fieldTypeIssue(document, typeName, field);
        if (issue) {
          issues.push({ path: `${pointer('types', typeName)}/fields/${index}/typeRef`, message: issue });
        }
      });
    }
    if (name !== undefined && SCHEMA_NAME.test(name)) {
      issues.push(...compose({ name, instanceType, document, canonical }));
    }
  }

  if (issues.length > 0) {
    throw new SchemaDocumentError(source, issues);
  }
  return { name: name as string, instanceType: instanceType as string, document, canonical };
}

/** modelOf rebuilds the model of a document the engine already accepted, from its canonical JSON. */
export function modelOf(canonical: string): SchemaModel {
  const document = JSON.parse(canonical) as Document;
  const name = document.name as string;
  const instanceType = instanceTypeOf(document, name);
  if (instanceType === undefined) {
    throw new Error(`stored schema ${name} has no instance type`);
  }
  return { name, instanceType, document, canonical };
}

/** instanceTypeOf returns the type named like the schema, or its only type. */
export function instanceTypeOf(document: Document, name: string): string | undefined {
  const types = document.types ?? {};
  if (Object.prototype.hasOwnProperty.call(types, name)) {
    return name;
  }
  const names = Object.keys(types);
  return names.length === 1 ? names[0] : undefined;
}

/** refKind says what a field's type name refers to in a document. */
export function refKind(document: Document, name: string): RefKind {
  if (PRIMITIVES.has(name)) {
    return 'primitive';
  }
  if (hasOwn(document.enums, name)) {
    return 'enum';
  }
  if (hasOwn(document.types, name)) {
    return 'type';
  }
  if (hasOwn(document.unions, name)) {
    return 'union';
  }
  if (hasOwn(document.scalars, name) || hasOwn(BUILTIN_SCALARS, scalarKey(name))) {
    return 'scalar';
  }
  return 'unknown';
}

/** scalarKey is the name the schema runtime keys a scalar by: dots become underscores. */
export function scalarKey(name: string): string {
  return name.replace(/\./g, '_');
}

/** arrayDepth is 0 for a single value, 1 for T[], 2 for T[][]. */
export function arrayDepth(typeRef: TypeRef): number {
  if (!typeRef.isArray) {
    return 0;
  }
  return typeRef.isArrayOfArrays ? 2 : 1;
}

/** jsonKey is the key a field's value has in an instance. */
export function jsonKey(field: FieldDef): string {
  return field.jsonTag || field.name;
}

/** reachableTypes lists the types an instance can hold, from the instance type through its fields. */
export function reachableTypes(document: Document, root: string): string[] {
  const types = document.types ?? {};
  const seen: string[] = [];
  const queue = [root];
  while (queue.length > 0) {
    const typeName = queue.shift() as string;
    if (seen.includes(typeName) || !hasOwn(types, typeName)) {
      continue;
    }
    seen.push(typeName);
    for (const field of (types[typeName] as TypeDef).fields ?? []) {
      if (refKind(document, field.typeRef.name) === 'type') {
        queue.push(field.typeRef.name);
      }
    }
  }
  return seen;
}

/** pointer writes a JSON pointer from its tokens. */
export function pointer(...tokens: string[]): string {
  return tokens.map((token) => `/${token.replace(/~/g, '~0').replace(/\//g, '~1')}`).join('');
}

function fieldTypeIssue(document: Document, typeName: string, field: FieldDef): string | undefined {
  const label = `field ${typeName}.${field.name}`;
  if (field.typeRef.isMap) {
    return `${label} is a map, which the schema runtime does not validate`;
  }
  switch (refKind(document, field.typeRef.name)) {
    case 'union':
      return `${label} has the union type ${field.typeRef.name}, which the schema runtime does not validate`;
    case 'unknown':
      return `${label} has type ${field.typeRef.name}, which is not a primitive, a scalar, or an enum or type of this schema`;
    default:
      return undefined;
  }
}

function hasOwn(record: object | undefined, key: string): boolean {
  return record !== undefined && Object.prototype.hasOwnProperty.call(record, key);
}
