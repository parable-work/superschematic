/**
 * What the schema-file loader reads from a meta-schema: the output of
 * `superschematic json-schema`, the JSON Schema a binary's registry builds
 * for schema files. Everything a registry decides comes from it: the
 * schema kinds, the single-definition file forms and their discriminators,
 * the document's collection keys, and the invocation policy's key and
 * default. A deployment's meta-schema may add kinds, extension slots,
 * documents and another policy, and the loader reads it the same way.
 */

/** A JSON Schema node, as the meta-schema writes them. */
export interface SchemaNode {
  $ref?: string;
  type?: string;
  enum?: unknown[];
  const?: unknown;
  default?: unknown;
  items?: SchemaNode;
  properties?: Record<string, SchemaNode>;
  required?: string[];
  additionalProperties?: SchemaNode | boolean;
}

/**
 * A file form: the $defs entry a payload validates against and where its
 * definition goes in the document. The document form has no collection.
 */
export interface FileForm {
  def: string;
  collection?: { key: string; list: boolean };
}

/** The invocation policy of the registry that wrote the meta-schema. */
export interface InvocationPolicy {
  def: string;
  key: string;
  default: string;
}

/** The dispatch rules and definitions one meta-schema declares. */
export interface MetaSchemaRules {
  id: string;
  defs: Record<string, SchemaNode>;
  document: FileForm;
  schemaKinds: Set<string>;
  /** Single-definition forms keyed by the value of their kind discriminator. */
  byKind: Map<string, FileForm>;
  /** The form without a kind discriminator and the key that marks it. */
  byDiscriminator: { key: string; form: FileForm };
  /** Keys that make a payload without a discriminator a document. */
  collectionKeys: string[];
  policy: InvocationPolicy;
}

const DOCUMENT = 'Document';
const MCP_RECORD = 'OperationMCP';
const DEFS_PREFIX = '#/$defs/';

function fail(message: string): never {
  throw new Error(`schema-file meta-schema: ${message}`);
}

function refName(node: SchemaNode | boolean | undefined): string | undefined {
  if (typeof node !== 'object' || typeof node.$ref !== 'string' || !node.$ref.startsWith(DEFS_PREFIX)) {
    return undefined;
  }
  return node.$ref.slice(DEFS_PREFIX.length);
}

function sameJSON(a: unknown, b: unknown): boolean {
  return JSON.stringify(sortKeys(a)) === JSON.stringify(sortKeys(b));
}

function sortKeys(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map(sortKeys);
  }
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, sortKeys((value as Record<string, unknown>)[key])])
    );
  }
  return value;
}

/**
 * readMetaSchema derives the loader's rules from a meta-schema. It throws
 * when the meta-schema lacks a part the rules need, such as the invocation
 * policy's default, which meta-schemas from before that default was
 * declared lack.
 */
export function readMetaSchema(metaSchema: Record<string, unknown>): MetaSchemaRules {
  const defs = metaSchema.$defs as Record<string, SchemaNode> | undefined;
  if (defs === null || typeof defs !== 'object') {
    fail('no $defs');
  }
  const documentDef = defs[DOCUMENT];
  const documentProps = documentDef?.properties;
  if (!documentProps) {
    fail(`no $defs/${DOCUMENT} with properties`);
  }
  const variants = metaSchema.oneOf;
  if (!Array.isArray(variants)) {
    fail('no root oneOf');
  }

  // A collection is a Document property holding definitions: a map keyed
  // by definition name, or a list.
  const collectionOf = (def: string): { key: string; list: boolean } => {
    const matches = Object.entries(documentProps)
      .filter(([, prop]) => refName(prop.additionalProperties) === def || refName(prop.items) === def)
      .map(([key, prop]) => ({ key, list: prop.type === 'array' }));
    if (matches.length !== 1) {
      fail(`expected one ${DOCUMENT} property holding $defs/${def}, found ${matches.length}`);
    }
    return matches[0];
  };

  // A single-definition form with a kind discriminator is a copy of a
  // definition with a required const "kind"; find the definition it copies.
  const baseOf = (fileDef: string): string => {
    const file = defs[fileDef];
    const props = { ...file.properties };
    delete props.kind;
    const required = (file.required ?? []).filter((key) => key !== 'kind');
    const matches = Object.keys(defs).filter(
      (name) =>
        name !== fileDef &&
        sameJSON(defs[name].properties ?? {}, props) &&
        sameJSON(defs[name].required ?? [], required)
    );
    if (matches.length !== 1) {
      fail(`expected one definition $defs/${fileDef} copies, found ${matches.length}`);
    }
    return matches[0];
  };

  const byKind = new Map<string, FileForm>();
  const withoutKind: string[] = [];
  for (const variant of variants as SchemaNode[]) {
    const name = refName(variant);
    if (name === undefined || !defs[name]) {
      fail('a root oneOf entry is not a $ref to $defs');
    }
    if (name === DOCUMENT) {
      continue;
    }
    const kind = defs[name].properties?.kind?.const;
    if (typeof kind === 'string') {
      byKind.set(kind, { def: name, collection: collectionOf(baseOf(name)) });
    } else {
      withoutKind.push(name);
    }
  }
  if (withoutKind.length !== 1) {
    fail(`expected one single-definition form without a kind discriminator, found ${withoutKind.length}`);
  }
  // That form is marked by the one key it requires that a document does
  // not have.
  const typeForm = withoutKind[0];
  const markers = (defs[typeForm].required ?? []).filter((key) => !(key in documentProps));
  if (markers.length !== 1) {
    fail(`expected one key marking $defs/${typeForm}, found ${markers.length}`);
  }

  const schemaKinds = documentProps.kind?.enum;
  if (!Array.isArray(schemaKinds)) {
    fail(`$defs/${DOCUMENT}/properties/kind has no enum`);
  }

  // The invocation policy is the mcp record's one property whose default
  // is one of its values; the other defaults are the zero values Go omits.
  const mcpProps = defs[MCP_RECORD]?.properties ?? {};
  const policies = Object.entries(mcpProps).filter(
    ([, prop]) => Array.isArray(prop.enum) && typeof prop.default === 'string' && prop.enum.includes(prop.default)
  );
  if (policies.length !== 1) {
    fail(`expected one invocation policy with a default on $defs/${MCP_RECORD}, found ${policies.length}`);
  }
  const [policyKey, policySchema] = policies[0];

  return {
    id: typeof metaSchema.$id === 'string' ? metaSchema.$id : 'schema-file.json',
    defs,
    document: { def: DOCUMENT },
    schemaKinds: new Set(schemaKinds.filter((kind): kind is string => typeof kind === 'string')),
    byKind,
    byDiscriminator: { key: markers[0], form: { def: typeForm, collection: collectionOf(typeForm) } },
    collectionKeys: Object.keys(documentProps).filter(
      (key) => documentProps[key].type === 'object' || documentProps[key].type === 'array'
    ),
    policy: { def: MCP_RECORD, key: policyKey, default: policySchema.default as string },
  };
}
