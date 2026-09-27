/**
 * The strict schema-file loader: the TypeScript twin of the Go data-form
 * reader (internal/loader/jsonreader over schemafile.DecodeWith). It does
 * what that reader does and no more. It dispatches a JSON payload to its
 * file form, validates it against that form's definition in a meta-schema,
 * decodes it as Go decodes it into the IR types, puts a single definition
 * into a document, drops empty extension entries, and fills in the
 * registry's invocation policy default. The canonical form of the result is
 * byte for byte what `ir.CanonicalJSON` writes for the document Go decodes;
 * runtime/schema/testdata/schema_file_parity.json holds the vectors.
 */

import Ajv2020 from 'ajv/dist/2020.js';
import type { ErrorObject, ValidateFunction } from 'ajv/dist/2020.js';
import type { Document } from '@superschematic/schema-ir/schema-file';
import coreMetaSchema from '@superschematic/schema-ir/schema-file.json';

import {
  JSONNumber,
  formatFloat64,
  isJSONObject,
  parseJSON,
  toPlain,
  writeCanonical,
  type JSONNode,
  type JSONObject,
} from './json';
import { readMetaSchema, type FileForm, type MetaSchemaRules, type SchemaNode } from './metaschema';

/** One reason a payload failed to load, at a JSON pointer into it. */
export interface SchemaFileIssue {
  path: string;
  message: string;
}

/** A payload the loader refuses. */
export class SchemaFileError extends Error {
  readonly source: string;
  readonly issues: SchemaFileIssue[];

  constructor(source: string, issues: SchemaFileIssue[]) {
    super(`${source}: ${issues.map((issue) => (issue.path ? `${issue.path}: ${issue.message}` : issue.message)).join('; ')}`);
    this.name = 'SchemaFileError';
    this.source = source;
    this.issues = issues;
  }
}

export interface SchemaFileLoaderOptions {
  /**
   * The meta-schema to load against: the `superschematic json-schema`
   * output of the binary whose registry the schemas are written for, parsed
   * or as JSON text. The default is the core registry's,
   * `@superschematic/schema-ir/schema-file.json`.
   */
  metaSchema?: Record<string, unknown> | string;
}

/** A loaded schema file. */
export interface LoadedSchemaFile {
  /**
   * The document, with plain JavaScript values: an integer past 2^53 is
   * rounded here, and `canonical` holds it exactly.
   */
  document: Document;
  /** The document as Go's ir.CanonicalJSON writes the document Go decodes. */
  canonical: string;
}

const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;
const integerLiteral = /^-?(0|[1-9][0-9]*)$/;

/** Keys whose values Go stores as raw JSON, not decoded into IR types. */
const RAW_SLOTS = new Set(['extensions', 'documents']);

/** A value Go's decoder refuses after the JSON Schema accepted it. */
class DecodeFailure {
  constructor(readonly issue: SchemaFileIssue) {}
}

/**
 * SchemaFileLoader loads schema files against one meta-schema, compiled
 * once.
 */
export class SchemaFileLoader {
  private readonly rules: MetaSchemaRules;
  private readonly validators = new Map<string, ValidateFunction>();

  constructor(options: SchemaFileLoaderOptions = {}) {
    let metaSchema = options.metaSchema ?? (coreMetaSchema as Record<string, unknown>);
    if (typeof metaSchema === 'string') {
      metaSchema = JSON.parse(metaSchema) as Record<string, unknown>;
    }
    this.rules = readMetaSchema(metaSchema);
    // The Go validator treats format as an annotation and ignores unknown
    // keywords; ajv's strict mode would refuse a decorator's schema that
    // the Go reader accepts.
    const ajv = new Ajv2020({ strict: false, validateFormats: false, allErrors: true });
    ajv.addSchema(metaSchema, this.rules.id);
    const forms = [this.rules.document, this.rules.byDiscriminator.form, ...this.rules.byKind.values()];
    for (const form of forms) {
      const validate = ajv.getSchema(`${this.rules.id}#/$defs/${form.def}`);
      if (!validate) {
        throw new Error(`schema-file meta-schema: cannot compile $defs/${form.def}`);
      }
      this.validators.set(form.def, validate);
    }
  }

  /**
   * load reads one schema file from its JSON text. source names the payload
   * in errors, as a file path or a label. It throws SchemaFileError for a
   * payload the Go reader refuses.
   */
  load(text: string, source = '<input>'): LoadedSchemaFile {
    let tree: JSONNode;
    try {
      tree = parseJSON(text);
    } catch (err) {
      if (err instanceof SyntaxError) {
        throw new SchemaFileError(source, [{ path: '', message: err.message }]);
      }
      throw err;
    }
    // Go reads the payload into a map of float64 values first, so a number
    // past float64 fails anywhere, extension data included.
    const tooLarge = findNonFinite(tree, '');
    if (tooLarge !== undefined) {
      throw new SchemaFileError(source, [{ path: tooLarge, message: 'number is out of the float64 range' }]);
    }
    if (!isJSONObject(tree)) {
      throw new SchemaFileError(source, [{ path: '', message: 'a schema file is a JSON object' }]);
    }

    const form = this.dispatch(tree, source);
    const validate = this.validators.get(form.def) as ValidateFunction;
    if (!validate(toPlain(tree))) {
      throw new SchemaFileError(source, (validate.errors ?? []).map(toIssue));
    }

    let decoded: JSONObject;
    try {
      decoded = this.decodeDef(tree, form.def, '') as JSONObject;
    } catch (err) {
      if (err instanceof DecodeFailure) {
        throw new SchemaFileError(source, [err.issue]);
      }
      throw err;
    }
    const document = intoDocument(decoded, form);
    dropEmptyExtensionEntries(document);
    this.fillInvocationPolicy(document);
    return { document: toPlain(document) as Document, canonical: writeCanonical(document) };
  }

  /**
   * dispatch picks the file form, in the Go reader's order: a kind naming
   * a single-definition form or a schema kind, then the key that marks the
   * form without a kind, then any document collection key.
   */
  private dispatch(payload: JSONObject, source: string): FileForm {
    const { rules } = this;
    if (payload.has('kind')) {
      const kind = payload.get('kind');
      if (typeof kind !== 'string') {
        throw new SchemaFileError(source, [{ path: '/kind', message: 'the "kind" key must be a string' }]);
      }
      const single = rules.byKind.get(kind);
      if (single) {
        return single;
      }
      if (rules.schemaKinds.has(kind)) {
        return rules.document;
      }
      throw new SchemaFileError(source, [
        {
          path: '/kind',
          message: `unknown kind "${kind}": expected a definition kind (${[...rules.byKind.keys()].join(', ')}) or a schema kind (${[...rules.schemaKinds].join(', ')})`,
        },
      ]);
    }
    if (payload.has(rules.byDiscriminator.key)) {
      return rules.byDiscriminator.form;
    }
    if (rules.collectionKeys.some((key) => payload.has(key))) {
      return rules.document;
    }
    throw new SchemaFileError(source, [
      {
        path: '',
        message: `cannot determine schema file shape: expected a "kind" or "${rules.byDiscriminator.key}" discriminator or a document collection key (${rules.collectionKeys.join(', ')})`,
      },
    ]);
  }

  /**
   * decodeDef decodes an object against a $defs entry as Go's encoding/json
   * decodes it into the IR struct and the struct encodes back: a property
   * that holds its default, the value the Go encoder omits, is dropped,
   * except the invocation policy, which Go keeps once set.
   */
  private decodeDef(node: JSONNode, def: string, path: string): JSONNode {
    const props = this.rules.defs[def]?.properties ?? {};
    const out: JSONObject = new Map();
    for (const [key, child] of node as JSONObject) {
      const prop = props[key];
      if (prop === undefined) {
        throw new Error(`schema-file loader: $defs/${def} validated a key it does not declare: ${key}`);
      }
      const at = `${path}/${escapePointer(key)}`;
      const value = RAW_SLOTS.has(key) ? child : this.decode(child, prop, at);
      const isPolicy = def === this.rules.policy.def && key === this.rules.policy.key;
      if (!isPolicy && prop.default !== undefined && equalsDefault(value, prop.default)) {
        continue;
      }
      out.set(key, value);
    }
    return out;
  }

  private decode(node: JSONNode, schema: SchemaNode, path: string): JSONNode {
    if (schema.$ref !== undefined) {
      return this.decodeDef(node, schema.$ref.slice('#/$defs/'.length), path);
    }
    switch (schema.type) {
      case 'object': {
        if (schema.properties) {
          throw new Error(`schema-file meta-schema: an inline object with properties at ${path}`);
        }
        const out: JSONObject = new Map();
        const values = schema.additionalProperties;
        for (const [key, child] of node as JSONObject) {
          const at = `${path}/${escapePointer(key)}`;
          out.set(key, typeof values === 'object' ? this.decode(child, values, at) : decodeAny(child));
        }
        return out;
      }
      case 'array':
        return (node as JSONNode[]).map((item, i) => this.decode(item, schema.items as SchemaNode, `${path}/${i}`));
      case 'integer':
        return decodeInteger(node as JSONNumber, path);
      case 'number':
        return new JSONNumber(formatFloat64(Number((node as JSONNumber).literal)));
      default:
        return node;
    }
  }

  /**
   * fillInvocationPolicy gives every visible tool of an operation set that
   * omits the policy the registry's default, as the Go reader does. An mcp
   * record elsewhere, or a hidden one, is left as it is.
   */
  private fillInvocationPolicy(document: JSONObject): void {
    const { key, default: value } = this.rules.policy;
    for (const set of children(document.get('operationSets'))) {
      for (const op of children(isJSONObject(set) ? set.get('operations') : undefined)) {
        const mcp = isJSONObject(op) ? op.get('mcp') : undefined;
        if (isJSONObject(mcp) && mcp.get('hidden') === false && !mcp.has(key)) {
          mcp.set(key, value);
        }
      }
    }
  }
}

/**
 * decodeAny decodes a value Go holds as `any`: a number becomes a float64,
 * written back in Go's float form.
 */
function decodeAny(node: JSONNode): JSONNode {
  if (node instanceof JSONNumber) {
    return new JSONNumber(formatFloat64(Number(node.literal)));
  }
  if (Array.isArray(node)) {
    return node.map(decodeAny);
  }
  if (isJSONObject(node)) {
    return new Map([...node].map(([key, value]) => [key, decodeAny(value)]));
  }
  return node;
}

/**
 * decodeInteger decodes into a Go int or int64: an integer literal with no
 * fraction or exponent, within int64. -0 reads as 0.
 */
function decodeInteger(node: JSONNumber, path: string): JSONNumber {
  const literal = node.literal;
  if (integerLiteral.test(literal)) {
    const value = BigInt(literal);
    if (value >= INT64_MIN && value <= INT64_MAX) {
      return new JSONNumber(value.toString());
    }
  }
  throw new DecodeFailure({ path, message: `cannot decode number ${literal} into an integer field` });
}

function equalsDefault(value: JSONNode, fallback: unknown): boolean {
  if (value instanceof JSONNumber) {
    return typeof fallback === 'number' && Number(value.literal) === fallback;
  }
  if (Array.isArray(value)) {
    return Array.isArray(fallback) && fallback.length === 0 && value.length === 0;
  }
  if (isJSONObject(value)) {
    return fallback !== null && typeof fallback === 'object' && !Array.isArray(fallback) && Object.keys(fallback).length === 0 && value.size === 0;
  }
  return value === fallback;
}

/**
 * intoDocument puts a single definition into a document, under its name
 * in a map collection or as the one entry of a list, without the kind
 * discriminator, as the Go reader does.
 */
function intoDocument(decoded: JSONObject, form: FileForm): JSONObject {
  if (!form.collection) {
    return decoded;
  }
  decoded.delete('kind');
  const { key, list } = form.collection;
  if (list) {
    return new Map([[key, [decoded]]]);
  }
  const name = decoded.get('name');
  return new Map([[key, new Map([[typeof name === 'string' ? name : '', decoded]])]]);
}

/**
 * dropEmptyExtensionEntries drops each extension entry that is an empty
 * object, and then an empty extensions object, on the holders the Go
 * reader canonicalizes: the document, its types and their fields, its
 * operation sets and their operations.
 */
function dropEmptyExtensionEntries(document: JSONObject): void {
  const holders: JSONNode[] = [document];
  const types = document.get('types');
  for (const type of isJSONObject(types) ? types.values() : []) {
    holders.push(type, ...children(isJSONObject(type) ? type.get('fields') : undefined));
  }
  for (const set of children(document.get('operationSets'))) {
    holders.push(set, ...children(isJSONObject(set) ? set.get('operations') : undefined));
  }
  for (const holder of holders) {
    if (!isJSONObject(holder)) {
      continue;
    }
    const extensions = holder.get('extensions');
    if (!isJSONObject(extensions)) {
      continue;
    }
    for (const [name, value] of extensions) {
      if (isJSONObject(value) && value.size === 0) {
        extensions.delete(name);
      }
    }
    if (extensions.size === 0) {
      holder.delete('extensions');
    }
  }
}

function children(node: JSONNode | undefined): JSONNode[] {
  return Array.isArray(node) ? node : [];
}

function findNonFinite(node: JSONNode, path: string): string | undefined {
  if (node instanceof JSONNumber) {
    return Number.isFinite(Number(node.literal)) ? undefined : path;
  }
  const entries: [string, JSONNode][] = Array.isArray(node)
    ? node.map((item, i) => [String(i), item])
    : isJSONObject(node)
      ? [...node]
      : [];
  for (const [key, child] of entries) {
    const found = findNonFinite(child, `${path}/${escapePointer(key)}`);
    if (found !== undefined) {
      return found;
    }
  }
  return undefined;
}

function escapePointer(key: string): string {
  return key.replace(/~/g, '~0').replace(/\//g, '~1');
}

function toIssue(error: ErrorObject): SchemaFileIssue {
  let message = error.message ?? 'is invalid';
  if (error.keyword === 'additionalProperties') {
    message = `unknown key "${(error.params as { additionalProperty: string }).additionalProperty}"`;
  } else if (error.keyword === 'enum') {
    message = `must be one of ${(error.params as { allowedValues: unknown[] }).allowedValues.map((value) => JSON.stringify(value)).join(', ')}`;
  }
  return { path: error.instancePath, message };
}

let defaultLoader: SchemaFileLoader | undefined;

/**
 * loadSchemaFile loads one schema file against the core registry's
 * meta-schema, or against options.metaSchema. A loader built once serves
 * many files; construct a SchemaFileLoader to keep one for a deployment's
 * meta-schema.
 */
export function loadSchemaFile(
  text: string,
  options: SchemaFileLoaderOptions & { source?: string } = {}
): LoadedSchemaFile {
  let loader: SchemaFileLoader;
  if (options.metaSchema !== undefined) {
    loader = new SchemaFileLoader({ metaSchema: options.metaSchema });
  } else {
    defaultLoader ??= new SchemaFileLoader();
    loader = defaultLoader;
  }
  return loader.load(text, options.source);
}
