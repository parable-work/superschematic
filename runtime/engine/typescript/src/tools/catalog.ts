/*
What the engine serves about a namespace's schemas besides their
instances (D16): a describe document per schema, which carries the
instance type's display and its fields' titles and icons (D48), the
tools document in the shape the SDK generators write to
tools/schema.json (ir.ToolManifest), and the calls those tools make. The
MCP endpoint (@superschematic/engine/mcp) and the HTTP routes read them
from here; every read and call goes through the schema registry and the
instance store, so the access policy answers each one.

A tool is one operation: create, get, list, update and delete of every
live schema the namespace reaches, and lookup of one whose instance type
has a unique field (create takes the parameters its behaviors declare a
createParamsSchema for, under behaviors, and create's data, update's
patch and the describe document's instance carry what the behaviors'
validate holds the fields to, as allOf entries their instanceSchema
writes; get, list and lookup take valueRefs, for the refs of the fields
the value store holds in place of their values; list takes where, the
values of the fields it filters on, and lookup key, the values of one
unique index's fields), each operation
its behaviors add (a schema-level one takes its parameters and no
instance id), three tools for writing schemas: list, describe and define
a draft, two that list and describe the behaviors a schema may compose
(behaviors.ts), get_value, which reads a value of the value store by its
hash (engine.values), where a schema the caller may read composes
Search, search, the search across the namespace's schemas
(engine.search), and four that list, create, archive and unarchive
namespaces (engine.namespaces), which the policy's manage answers. The
update, delete and instance operation tools of a schema one of whose
behaviors declares a preconditionSchema take `preconditions`, each such
behavior's entry by its name, as the HTTP API's Preconditions header
carries them. No tool publishes: a draft goes live only through an HTTP
call the access policy governs, so an MCP client cannot put a schema
live on its own.

Each tool carries guidance, as an SDK tool carries its @docs guidance:
the engine's own for its tools and for the operations every schema has,
with what the schema's behaviors say about each operation under their
configs (guidance.ts). The describe document carries it per operation,
with each behavior's summary, and a create's behaviors argument shows
the create parameters each behavior takes under its config when its
implementation narrows them.

Names follow the SDK generators: a tool's name is `<namespace>.<method>`,
the namespace the schema name in kebab case (codegen.ToKebabCase) and the
method the operation's name, as `order.create` or `line-item.addNote`. An
SDK tool's MCP handle is authored with @mcp; the engine derives it from
the same two parts in snake case (codegen.ToSnakeCase), as `order_create`,
and the engine's own tools are `list_schemas`, `describe_schema`,
`define_schema`, `list_behaviors`, `describe_behavior`, `get_value`,
`search`, `list_namespaces`, `create_namespace`, `archive_namespace` and
`unarchive_namespace`. A handle @mcp would refuse (not lowercase snake
case, or longer than 48 characters) or one two tools derive hides both
tools, with the reason; the engine's schema tools keep theirs. A tool the access
policy refuses the caller is hidden too, with that reason, and can still
be called by its handle: the call is refused. So is every tool that
writes in an archived namespace, the namespace tools aside, since the
namespace refuses the write. The engine tools that name their schema or
their namespace only when called, define_schema and the namespace tools,
are asked about with a listing question (access.ts,
ListingAccessRequest): a principal the policy would refuse them does not
see them either.

A mount, the MCP endpoint or the tools route, can narrow what a caller
is offered further with a ToolFilter, asked per principal and tool: a
tool it leaves out is hidden from that caller with its reason, and a
call of it is a tool the namespace does not have (UnknownToolError), so
an agent's session limited to one schema's operations reaches no other.
*/

import type { TypeDisplay } from '@superschematic/schema-ir/schema-file';

import { checkPrincipal, type Access, type NamespaceOperation, type Principal, type SchemaAction } from '../access.js';
import type { InstanceSchemaForm, TypeSchema } from '../behaviors/behavior.js';
import { SEARCH_SCHEMAS_PARAMS, searchSchemas } from '../behaviors/core/index.js';
import { BEHAVIOR_NAME, type BehaviorDeclaration, type BehaviorOperationDeclaration, type OperationScope } from '../behaviors/declaration.js';
import type { BehaviorRegistry } from '../behaviors/registry.js';
import { jsonCopy } from '../behaviors/json.js';
import { synchronous } from '../behaviors/storage.js';
import { BehaviorError, EngineError } from '../errors.js';
import { isPlainObject } from '../instances/patch.js';
import { MAX_FILTER_VALUES, type Filterable } from '../instances/filters.js';
import type { OwnIndex } from '../instances/indexes.js';
import { INSTANCE_ID, type InstanceStore } from '../instances/store.js';
import { NAMESPACE_NAME, type Namespaces } from '../namespaces.js';
import { MAX_PAGE_SIZE } from '../paging.js';
import type { SchemaCatalog, SchemaRecord, SchemaSummary } from '../registry/catalog.js';
import { describedDisplay } from '../registry/display.js';
import { SCHEMA_NAME, jsonKey } from '../registry/document.js';
import type { ComposedBehavior, SchemaRegistry } from '../registry/registry.js';
import { VALUE_HASH } from '../values/store.js';
import type { EngineValues } from '../values/values.js';
import { behaviorDocument, behaviorSummary, type BehaviorDocument, type BehaviorSummary } from './behaviors.js';
import { engineGuidance, versionGuidance, type EngineTool, type ToolGuidance, type VersionGuidance } from './guidance.js';
import type { BuiltinTool, ResolvedToolOptions } from './options.js';
import {
  ANY_JSON_TYPES,
  FieldSchemas,
  argumentsDigest,
  renderArguments,
  renderProperty,
  type ArgumentSchema,
  type JSONSchemaValue,
  type Property,
} from './schema.js';

/** The longest MCP handle @mcp accepts (ir.MCPHandleMaxLength). */
export const MCP_HANDLE_MAX_LENGTH = 48;
/** An MCP handle @mcp accepts: lowercase snake case starting with a letter. */
export const MCP_HANDLE = /^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$/;

/** A JSON Schema. */
export type JSONSchemaObject = Record<string, unknown>;

/** A schema's describe document. */
export interface DescribeDocument {
  /** The namespace the call named. */
  namespace: string;
  name: string;
  /** The namespace that holds the schema: the call's own, or the shared one. */
  schemaNamespace: string;
  /** The live version. */
  version: number;
  hash: string;
  instanceType: string;
  /** The instance type's description, else the document's; absent without either. */
  description?: string;
  /**
   * The instance type's display (@display, D48): what a UI calls an
   * instance, its title and summary fields by their keys in an instance's
   * data, and the labels of its Workflow's states and transitions. Absent
   * when the type declares none.
   */
  display?: TypeDisplay;
  /** The instance type's own fields, in declaration order, each with its title and icon where declared. */
  fields: DescribedField[];
  /** The JSON Schema of an instance's data: closed, its behaviors' fields read-only. */
  instance: JSONSchemaObject;
  behaviors: DescribedBehavior[];
  /** create, get, list, update, delete, lookup when the type has a unique field, then each behavior's operations in the type's list order. */
  operations: DescribedOperation[];
}

/** One of the instance type's own fields, as a UI labels it. */
export interface DescribedField {
  /** Its key in an instance's data. */
  name: string;
  /** Its label, from @docs({ title }). */
  title?: string;
  /** The glyph a UI shows for it, from @icon. */
  icon?: string;
}

/** A behavior a schema's instance type composes. */
export interface DescribedBehavior {
  name: string;
  description?: string;
  /** The type's config of it, as the schema holds it; {} when it gives none. */
  config: unknown;
  /** What it does on the type under the config, as its guidance says; absent for a behavior that gives none. */
  summary?: string;
  fields: Array<{ name: string; description?: string }>;
  operations: string[];
  /** The codes its vetoes carry, as its declaration lists them. */
  vetoes: Array<{ code: string; description?: string }>;
}

/**
 * One operation: its parameters (its tool's arguments) and result, whether
 * it writes, its tool, and its invocation policy under the policy's key.
 */
export interface DescribedOperation {
  name: string;
  /** The behavior that adds it; absent for the operations every schema has. */
  behavior?: string;
  /** For a behavior's operation, what it runs on: an instance, or the schema as a whole. */
  scope?: OperationScope;
  /** Its tool's title, as the tools document writes it. */
  title: string;
  description: string;
  writes: boolean;
  params: JSONSchemaObject;
  result: JSONSchemaValue;
  /** Its tool's name in the tools document. */
  tool: string;
  /** Its tool's guidance, as the tools document writes it. */
  guidance: ToolGuidance;
  /** The invocation policy, under the policy's key. */
  [policyKey: string]: unknown;
}

/** A visible tool's MCP record, or a hidden one's (ir.OperationMCP as tools/schema.json writes it). */
export type ToolMCPRecord =
  | { hidden: false; name: string; handle: string; description: string; _meta?: Record<string, unknown>; [policyKey: string]: unknown }
  | { hidden: true; hiddenReason: string };

/** One tool of the tools document: ir.ToolManifestTool. */
export interface ToolDefinition {
  name: string;
  operationId: string;
  title: string;
  mcp: ToolMCPRecord;
  capability: string;
  lifecycle: string;
  visibility: string;
  audience: string;
  guidance: ToolGuidance;
  replay: { mode: string; idempotencyKeyPointers: string[]; expectedRevisionPointers: string[] } | null;
  description: string;
  namespace: string;
  methodName: string;
  httpMethod: string;
  httpPath: string;
  requiresAuth: boolean;
  requiredPermissions: string[];
  isScoped: boolean;
  bindingStatus: string;
  inputSchemaDigest: string;
  parameters: JSONSchemaObject;
  returns: { type: string | string[]; description?: string; items?: { type: string | string[]; description?: string } };
}

/** The tools document of a namespace: ir.ToolManifest. */
export interface ToolManifest {
  $schema: string;
  title: string;
  description: string;
  tools: ToolDefinition[];
}

/** Where a call looks: a namespace, `default` when absent. */
export interface ToolTarget {
  namespace?: string;
  /**
   * The mount's narrowing of the tools a caller is offered: a tool it
   * leaves out is hidden from the caller, and calling it is
   * UnknownToolError. Absent, every tool the policy lets the caller see.
   */
  filter?: ToolFilter;
}

/** What a ToolFilter is told of one tool. */
export interface ToolSummary {
  /** Its MCP handle, `order_create`, `define_schema`. */
  readonly handle: string;
  /** Its name in the tools document, `order.create`, `engine.defineSchema`. */
  readonly name: string;
  /** The schema a schema's tool reaches; absent for the engine's own tools. */
  readonly schema?: string;
  /** The operation it runs: `create`, `get`, ..., a behavior's operation, or the engine tool's (`defineSchema`). */
  readonly operation: string;
  /** The behavior whose operation it runs; absent for the others. */
  readonly behavior?: string;
  /** Whether it writes. */
  readonly writes: boolean;
}

/**
 * ToolFilter narrows what one caller is offered: true keeps a tool, and
 * anything else leaves it out of the caller's list and refuses its call.
 * It runs synchronously, for every tool of every listing and call. It
 * narrows only: a tool the access policy hides stays hidden.
 */
export type ToolFilter = (principal: Principal, tool: ToolSummary, namespace: string) => boolean;

/** A tool call named a handle the namespace has no tool for, as the caller sees it. */
export class UnknownToolError extends EngineError {
  readonly tool: string;

  constructor(tool: string, namespace: string) {
    super('not_found', `namespace ${namespace} has no tool ${JSON.stringify(tool)}`);
    this.name = 'UnknownToolError';
    this.tool = tool;
  }
}

type ToolKind =
  | 'create'
  | 'get'
  | 'list'
  | 'update'
  | 'delete'
  | 'lookup'
  | 'operation'
  | 'schemaOperation'
  | 'listSchemas'
  | 'describeSchema'
  | 'defineSchema'
  | 'listBehaviors'
  | 'describeBehavior'
  | 'search'
  | 'getValue'
  | 'listNamespaces'
  | 'createNamespace'
  | 'archiveNamespace'
  | 'unarchiveNamespace';

// The engine's tools that act on namespaces themselves, not in the
// namespace whose tools they are among.
const NAMESPACE_TOOLS: ReadonlySet<ToolKind> = new Set(['listNamespaces', 'createNamespace', 'archiveNamespace', 'unarchiveNamespace']);

// The listing question each engine tool that names its schema or its
// namespace only when called asks the policy (ListingAccessRequest).
const LISTING_QUESTIONS: ReadonlyMap<ToolKind, { action: 'define' | 'manage'; operation?: NamespaceOperation }> = new Map([
  ['defineSchema', { action: 'define' }],
  ['listNamespaces', { action: 'manage', operation: 'list' }],
  ['createNamespace', { action: 'manage', operation: 'create' }],
  ['archiveNamespace', { action: 'manage', operation: 'archive' }],
  ['unarchiveNamespace', { action: 'manage', operation: 'unarchive' }],
]);

// kept asks a mount's filter about one tool; only a literal true keeps it.
function kept(filter: ToolFilter, principal: Principal, namespace: string, tool: ToolSpec): boolean {
  const summary: ToolSummary = Object.freeze({
    handle: tool.handle,
    name: tool.name,
    ...(tool.schema === undefined ? {} : { schema: tool.schema.name }),
    operation: tool.methodName,
    ...(tool.behavior === undefined ? {} : { behavior: tool.behavior.name }),
    writes: tool.writes,
  });
  const answer: unknown = filter(principal, summary, namespace);
  if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
    throw new TypeError('a tool filter is synchronous: it returned a promise');
  }
  return answer === true;
}

// One tool before it is rendered: its names, what it does, and who may see it.
interface ToolSpec {
  kind: ToolKind;
  name: string;
  handle: string;
  namespace: string;
  methodName: string;
  operationId: string;
  title: string;
  description: string;
  writes: boolean;
  policy: string;
  httpMethod: string;
  httpPath: string;
  /** The schema a call reaches, for a schema's tool. */
  schema?: SchemaRecord;
  behavior?: ComposedBehavior;
  operation?: BehaviorOperationDeclaration;
  /** For create, the schema's behaviors that take create parameters, in list order. */
  createParams?: ComposedBehavior[];
  /** Why the tool is hidden; absent for a visible one. */
  hidden?: string;
  /** The preconditions argument of an update, a delete or an instance operation; absent when no behavior declares one. */
  preconditions?: Property;
  /** The where argument of a list; absent when the schema has no field a list filters on. */
  where?: Property;
  /** The key argument of a lookup. */
  key?: Property;
}

const TOOL_SCHEMA = 'https://json-schema.org/draft/2020-12/schema';

/** A version's argument schemas: its instance type's fields, and what its behaviors hold them to in each form. */
interface InstanceSchemas {
  readonly input: ArgumentSchema;
  readonly fields: FieldSchemas;
  readonly rules: Readonly<Record<InstanceSchemaForm, readonly unknown[]>>;
}

export class ToolCatalog {
  // The argument schemas of each published version, which never changes.
  private readonly instanceSchemas = new Map<string, InstanceSchemas>();
  // What each published version's behaviors say about it (guidance.ts).
  private readonly guidance = new Map<string, VersionGuidance>();

  constructor(
    private readonly namespaces: Namespaces,
    private readonly access: Access,
    private readonly schemas: SchemaRegistry,
    private readonly instances: InstanceStore,
    /** The invocation policy, the built-in tools' policies and the vendor keys. */
    readonly options: ResolvedToolOptions,
    /** The versions' runtimes, for what their behaviors hold an instance's fields to; the registry has asked the policy. */
    private readonly catalog: SchemaCatalog,
    /** The behaviors this engine runs, which listBehaviors and describeBehavior serve. */
    private readonly behaviors: BehaviorRegistry,
    /** The value store's reads by hash, which get_value serves. */
    private readonly values: EngineValues
  ) {}

  /**
   * listBehaviors returns a summary of each behavior this engine runs, by
   * name. Every caller may read it; the policy is not asked, since it
   * names no schema.
   */
  listBehaviors(principal: Principal): BehaviorSummary[] {
    checkPrincipal(principal);
    return this.behaviors.names().map((name) => behaviorSummary(this.behaviors.declaration(name) as BehaviorDeclaration));
  }

  /**
   * describeBehavior returns the document of a behavior this engine runs:
   * its declaration with every default filled in. It throws not_found for
   * a name it does not run. Every caller may read it.
   */
  describeBehavior(principal: Principal, name: string): BehaviorDocument {
    checkPrincipal(principal);
    if (typeof name !== 'string' || !BEHAVIOR_NAME.test(name)) {
      throw new EngineError('invalid_argument', `${JSON.stringify(name)} is not a behavior name`);
    }
    const declaration = this.behaviors.declaration(name);
    if (declaration === undefined) {
      throw new EngineError('not_found', `this engine runs no behavior ${name}`);
    }
    return behaviorDocument(declaration, this.options.invocationPolicy);
  }

  /**
   * describe returns the describe document of a schema's live version. It
   * asks the policy for read, and throws not_found without a live version
   * and unavailable when the engine cannot run its behaviors.
   */
  describe(principal: Principal, name: string, target: ToolTarget = {}): DescribeDocument {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(target.namespace);
    const record = this.schemas.live(principal, name, { namespace });
    if (!record) {
      throw new EngineError('not_found', `schema ${name} has no live version in namespace ${namespace}`);
    }
    const behaviors = this.schemas.behaviors(principal, name, { namespace });
    const tools = this.schemaTools(namespace, record, behaviors);
    const policyKey = this.options.invocationPolicy.key;
    const type = (record.document.types ?? {})[record.instanceType];
    const description = type?.description || record.document.description;
    const display = type === undefined ? undefined : describedDisplay(type);
    const said = this.guidanceOf(record);
    return {
      namespace,
      name: record.name,
      schemaNamespace: record.namespace,
      version: record.version as number,
      hash: record.hash,
      instanceType: record.instanceType,
      ...(description ? { description } : {}),
      ...(display !== undefined ? { display } : {}),
      fields: (type?.fields ?? []).map((field) => ({
        name: jsonKey(field),
        ...(field.title ? { title: field.title } : {}),
        ...(field.icon ? { icon: field.icon } : {}),
      })),
      instance: this.instanceSchema(record, behaviors),
      behaviors: behaviors.map((bound) => ({
        name: bound.name,
        ...(bound.declaration.description ? { description: bound.declaration.description } : {}),
        config: bound.config,
        ...(said.summaries.has(bound.name) ? { summary: said.summaries.get(bound.name) as string } : {}),
        fields: (bound.declaration.fields ?? []).map((field) => ({
          name: field.name,
          ...(field.description ? { description: field.description } : {}),
        })),
        operations: (bound.declaration.operations ?? []).map((operation) => operation.name),
        vetoes: (bound.declaration.vetoes ?? []).map((veto) => ({
          code: veto.code,
          ...(veto.description ? { description: veto.description } : {}),
        })),
      })),
      operations: tools.map((tool) => ({
        name: tool.methodName,
        ...(tool.behavior ? { behavior: tool.behavior.name, scope: tool.kind === 'schemaOperation' ? 'schema' : 'instance' } : {}),
        title: tool.title,
        description: tool.description,
        writes: tool.writes,
        [policyKey]: tool.policy,
        params: renderArguments(this.arguments(tool), this.options.keys.scalar),
        result: this.resultSchema(tool, behaviors),
        tool: tool.name,
        guidance: this.toolGuidance(tool),
      })),
    };
  }

  /**
   * manifest returns the tools document of a namespace: the schema tools,
   * then each live schema it reaches that the principal may read, by name.
   * A schema whose behaviors this engine cannot run has no tools.
   */
  manifest(principal: Principal, target: ToolTarget = {}): ToolManifest {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(target.namespace);
    const tools = this.toolSet(principal, namespace);
    return {
      $schema: TOOL_SCHEMA,
      title: `Namespace ${namespace} Tool Definitions`,
      description: `MCP tool bindings for the schemas namespace ${namespace} reaches`,
      tools: tools.map((tool) => this.definition(principal, namespace, tool, target.filter)),
    };
  }

  /**
   * call runs a tool by its MCP handle with its arguments and returns what
   * the operation returns. The call goes through the schema registry or
   * the instance store, which ask the policy. A handle the namespace has
   * no visible tool for, among the schemas the principal may read, is an
   * UnknownToolError.
   */
  call(principal: Principal, handle: string, args: unknown, target: ToolTarget = {}): unknown {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(target.namespace);
    const tool =
      typeof handle === 'string'
        ? this.toolSet(principal, namespace).find((candidate) => candidate.handle === handle && candidate.hidden === undefined)
        : undefined;
    if (!tool || (target.filter !== undefined && !kept(target.filter, principal, namespace, tool))) {
      throw new UnknownToolError(String(handle), namespace);
    }
    const input = argumentsOf(tool, args);
    const schema = tool.schema?.name as string;
    switch (tool.kind) {
      case 'listSchemas':
        only(tool, input, []);
        return this.schemas.list(principal, { namespace });
      case 'describeSchema':
        only(tool, input, ['name']);
        return this.describe(principal, requiredString(tool, input, 'name'), { namespace });
      case 'defineSchema': {
        only(tool, input, ['document']);
        const document = input.document;
        if (!isPlainObject(document)) {
          throw new EngineError('invalid_argument', `${tool.handle}: document is a schema-file document, a JSON object`);
        }
        const { canonical: _canonical, ...draft } = this.schemas.define(principal, document, { namespace });
        return draft;
      }
      case 'listBehaviors':
        only(tool, input, []);
        return this.listBehaviors(principal);
      case 'describeBehavior':
        only(tool, input, ['name']);
        return this.describeBehavior(principal, requiredString(tool, input, 'name'));
      case 'create': {
        only(tool, input, ['id', 'data', 'behaviors']);
        const id = optionalString(tool, input, 'id');
        if (!('data' in input)) {
          throw new EngineError('invalid_argument', `${tool.handle}: data, the instance, is required`);
        }
        const behaviors = input.behaviors ?? undefined;
        return this.instances.create(principal, schema, input.data, {
          namespace,
          ...(id !== undefined ? { id } : {}),
          ...(behaviors !== undefined ? { behaviors: behaviors as Record<string, unknown> } : {}),
        });
      }
      case 'get': {
        only(tool, input, ['id', 'valueRefs']);
        const id = requiredString(tool, input, 'id');
        const record = this.instances.get(principal, schema, id, { namespace, ...valueRefsOf(tool, input) });
        if (!record) {
          throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
        }
        return record;
      }
      case 'list': {
        only(tool, input, ['limit', 'cursor', 'valueRefs', ...(tool.where === undefined ? [] : ['where'])]);
        const limit = input.limit ?? undefined;
        if (limit !== undefined && typeof limit !== 'number') {
          throw new EngineError('invalid_argument', `${tool.handle}: limit is an integer`);
        }
        const cursor = optionalString(tool, input, 'cursor');
        const where = input.where ?? undefined;
        if (where !== undefined && !isPlainObject(where)) {
          throw new EngineError('invalid_argument', `${tool.handle}: where is a JSON object of field values, by field`);
        }
        return this.instances.list(principal, schema, {
          namespace,
          ...(limit !== undefined ? { limit } : {}),
          ...(cursor !== undefined ? { cursor } : {}),
          ...(where !== undefined ? { where } : {}),
          ...valueRefsOf(tool, input),
        });
      }
      case 'lookup': {
        only(tool, input, ['key', 'valueRefs']);
        if (!isPlainObject(input.key)) {
          throw new EngineError('invalid_argument', `${tool.handle}: key, the values of one unique index's fields, is a JSON object, and required`);
        }
        const record = this.instances.lookup(principal, schema, input.key, { namespace, ...valueRefsOf(tool, input) });
        if (!record) {
          throw new EngineError('not_found', `no ${schema} in namespace ${namespace} holds ${JSON.stringify(input.key)}`);
        }
        return record;
      }
      case 'update': {
        only(tool, input, ['id', 'patch', 'expectedSeq', ...preconditionsArgument(tool)]);
        const id = requiredString(tool, input, 'id');
        if (!('patch' in input)) {
          throw new EngineError('invalid_argument', `${tool.handle}: patch, a JSON merge patch of the instance, is required`);
        }
        return this.instances.update(principal, schema, id, input.patch, { namespace, ...expectedSeqOf(tool, input), ...preconditionsOf(tool, input) });
      }
      case 'delete': {
        only(tool, input, ['id', 'expectedSeq', ...preconditionsArgument(tool)]);
        const id = requiredString(tool, input, 'id');
        if (!this.instances.delete(principal, schema, id, { namespace, ...expectedSeqOf(tool, input), ...preconditionsOf(tool, input) })) {
          throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
        }
        return null;
      }
      case 'operation': {
        only(tool, input, ['id', 'params', 'expectedSeq', ...preconditionsArgument(tool)]);
        const id = requiredString(tool, input, 'id');
        const params = input.params ?? {};
        return this.instances.invoke(principal, schema, id, tool.methodName, params, {
          namespace,
          ...expectedSeqOf(tool, input),
          ...preconditionsOf(tool, input),
        });
      }
      case 'schemaOperation': {
        only(tool, input, ['params']);
        return this.instances.invokeSchema(principal, schema, tool.methodName, input.params ?? {}, { namespace });
      }
      case 'search':
        return searchSchemas(this.schemas, this.instances, principal, input, namespace);
      case 'getValue':
        only(tool, input, ['hash']);
        return this.values.get(principal, requiredString(tool, input, 'hash'), { namespace });
      case 'listNamespaces':
        only(tool, input, []);
        return this.namespaces.list(principal);
      case 'createNamespace':
        only(tool, input, ['name']);
        return this.namespaces.create(principal, requiredString(tool, input, 'name'));
      case 'archiveNamespace':
        only(tool, input, ['name']);
        return this.namespaces.archive(principal, requiredString(tool, input, 'name'));
      case 'unarchiveNamespace':
        only(tool, input, ['name']);
        return this.namespaces.unarchive(principal, requiredString(tool, input, 'name'));
    }
  }

  // toolSet lists the namespace's tools: the schema tools, then each live
  // schema's, with every handle checked and each tool's visibility to the
  // principal decided.
  private toolSet(principal: Principal, namespace: string): ToolSpec[] {
    const schemaTools = this.schemas.list(principal, { namespace }).flatMap((summary) => this.liveTools(principal, namespace, summary));
    // The search across schemas is there when a schema it would search is.
    const searches = schemaTools.some((tool) => tool.kind === 'schemaOperation' && tool.behavior?.name === 'Search' && tool.methodName === 'search');
    const engineTools = this.engineTools(namespace).filter((tool) => tool.kind !== 'search' || searches);
    const all = [...engineTools, ...schemaTools];
    const reserved = new Map(engineTools.map((tool) => [tool.handle, tool.name]));
    const byHandle = new Map<string, ToolSpec[]>();
    for (const tool of schemaTools) {
      byHandle.set(tool.handle, [...(byHandle.get(tool.handle) ?? []), tool]);
    }
    for (const tool of schemaTools) {
      const sharing = (byHandle.get(tool.handle) ?? []).filter((other) => other !== tool);
      if (reserved.has(tool.handle)) {
        tool.hidden = `its MCP handle ${tool.handle} is the handle of the engine's tool ${String(reserved.get(tool.handle))}`;
      } else if (!MCP_HANDLE.test(tool.handle)) {
        tool.hidden = `its MCP handle ${tool.handle} is not lowercase snake case starting with a letter`;
      } else if (tool.handle.length > MCP_HANDLE_MAX_LENGTH) {
        tool.hidden = `its MCP handle ${tool.handle} is longer than ${MCP_HANDLE_MAX_LENGTH} characters`;
      } else if (sharing.length > 0) {
        tool.hidden = `its MCP handle ${tool.handle} is also the handle of ${sharing.map((other) => other.name).join(', ')}`;
      }
    }
    return all;
  }

  // liveTools lists the tools of a schema the namespace reaches: none
  // without a live version, or when the engine cannot run its behaviors.
  private liveTools(principal: Principal, namespace: string, summary: SchemaSummary): ToolSpec[] {
    if (summary.liveVersion === null) {
      return [];
    }
    const record = this.schemas.live(principal, summary.name, { namespace });
    if (!record) {
      return [];
    }
    try {
      return this.schemaTools(namespace, record, this.schemas.behaviors(principal, summary.name, { namespace }));
    } catch (error) {
      if (error instanceof EngineError && error.code === 'unavailable') {
        return [];
      }
      throw error;
    }
  }

  private engineTools(namespace: string): ToolSpec[] {
    const invocation = this.options.invocation;
    const base = `/namespaces/${encodeURIComponent(namespace)}/schemas`;
    const tool = (kind: BuiltinTool & ToolKind, handle: string, title: string, description: string, writes: boolean, httpMethod: string, httpPath: string): ToolSpec => ({
      kind,
      name: `engine.${kind}`,
      handle,
      namespace: 'engine',
      methodName: kind,
      operationId: `engine.${kind}`,
      title,
      description,
      writes,
      policy: invocation[kind],
      httpMethod,
      httpPath,
    });
    return [
      tool(
        'listSchemas',
        'list_schemas',
        'List schemas',
        'Lists the schemas this namespace reaches, its own and the shared namespace\'s, with each one\'s live version and whether it has a draft.',
        false,
        'GET',
        base
      ),
      tool(
        'describeSchema',
        'describe_schema',
        'Describe a schema',
        "Returns a schema's describe document: how a UI shows an instance (its display, and each field's title and icon), the JSON Schema of an instance, the behaviors its type composes with their config, and every operation with its parameters, its result and its tool.",
        false,
        'GET',
        `${base}/{name}/describe`
      ),
      tool(
        'defineSchema',
        'define_schema',
        'Define a schema draft',
        'Stores a schema-file document (the form superschematic format --to=json writes) as the draft of its name, replacing the draft before it. It does not publish: a draft goes live only through the HTTP publish route.',
        true,
        'POST',
        base
      ),
      tool(
        'listBehaviors',
        'list_behaviors',
        'List behaviors',
        "Lists the behaviors this engine runs, which a schema's types may compose: each one's name and description, the behaviors it requires and conflicts with, and the fields and operations it adds.",
        false,
        'GET',
        '/behaviors'
      ),
      tool(
        'describeBehavior',
        'describe_behavior',
        'Describe a behavior',
        "Returns a behavior's declaration: the JSON Schema of the config a type gives it, of the parameters a create gives it and of the entry a write's preconditions carry for it, the behaviors it requires and conflicts with, its fields, its operations with their scope, parameters, result and invocation policy, and the codes its vetoes carry.",
        false,
        'GET',
        '/behaviors/{name}'
      ),
      tool(
        'getValue',
        'get_value',
        'Get a stored value',
        "Returns a value the engine stores by its hash, { hash, bytes, value }: a field whose JSON is longer than the engine's threshold, which an event (and an instance read with valueRefs) carries as a ref, { \"$value\": <hash>, \"bytes\": <n> }. The caller must be able to read a schema of this namespace whose instances, events or behaviors hold the value.",
        false,
        'GET',
        `/namespaces/${encodeURIComponent(namespace)}/values/{hash}`
      ),
      tool(
        'listNamespaces',
        'list_namespaces',
        'List namespaces',
        "Lists the namespaces the caller may see: each one's name, whether the engine's options configure it or a create made it, whether it is the shared namespace, and whether it is archived.",
        false,
        'GET',
        '/namespaces'
      ),
      tool(
        'createNamespace',
        'create_namespace',
        'Create a namespace',
        'Creates a namespace, which holds schemas and instances of its own and looks schema names up in the shared namespace after itself. Its name is lowercase letters, digits and hyphens, starting with a letter.',
        true,
        'POST',
        '/namespaces'
      ),
      tool(
        'archiveNamespace',
        'archive_namespace',
        'Archive a namespace',
        'Archives a namespace a create made: its schemas, instances and events stay readable, and it refuses every write, and runs no reaction or schedule, until it is unarchived.',
        true,
        'POST',
        '/namespaces/{name}/archive'
      ),
      tool(
        'unarchiveNamespace',
        'unarchive_namespace',
        'Unarchive a namespace',
        'Lets an archived namespace be written again; its reactions and schedules pick up where they stopped.',
        true,
        'POST',
        '/namespaces/{name}/unarchive'
      ),
      tool(
        'search',
        'search',
        'Search every schema',
        "Searches the instances of every schema this namespace reaches that composes Search and that the caller may read, and returns one ranking: by query, by vector (with its model), or both. Each hit names its schema and says how it matched. Before creating an instance, search for its text to find near-duplicates.",
        false,
        'POST',
        `/namespaces/${encodeURIComponent(namespace)}/search`
      ),
    ];
  }

  // schemaTools lists a live schema's tools: create, get, list, update,
  // delete, lookup when its instance type has a unique field, then its
  // behaviors' operations.
  private schemaTools(namespace: string, record: SchemaRecord, behaviors: ComposedBehavior[]): ToolSpec[] {
    const name = record.name;
    const kebab = kebabCase(name);
    const invocation = this.options.invocation;
    const schemaPath = `/namespaces/${encodeURIComponent(namespace)}/schemas/${encodeURIComponent(name)}`;
    const instances = `${schemaPath}/instances`;
    const spec = (kind: ToolKind, methodName: string, parts: Omit<ToolSpec, 'kind' | 'name' | 'handle' | 'namespace' | 'methodName' | 'operationId' | 'schema'>): ToolSpec => ({
      kind,
      name: `${kebab}.${methodName}`,
      handle: `${snakeCase(name)}_${snakeCase(methodName)}`,
      namespace: kebab,
      methodName,
      operationId: `${name}.${methodName}`,
      schema: record,
      ...parts,
    });
    const preconditions = preconditionsProperty(behaviors);
    const fenced = preconditions === undefined ? {} : { preconditions };
    const createParams = behaviors.filter((behavior) => behavior.declaration.createParamsSchema !== undefined);
    const runtime = this.catalog.runtimeOf(record);
    const where = this.whereProperty(record, behaviors);
    const unique = runtime.indexes.filter((index) => index.unique);
    const tools: ToolSpec[] = [
      spec('create', 'create', {
        title: `Create ${name}`,
        description:
          createParams.length === 0
            ? `Creates a ${name}: validates the instance, data, against the schema's live version and stores it under id, or under a new id when none is given.`
            : `Creates a ${name}: validates the instance, data, against the schema's live version and stores it under id, or under a new id when none is given. behaviors gives its behaviors their create parameters (${createParams.map((behavior) => behavior.name).join(', ')}), which hold from the create on.`,
        writes: true,
        policy: invocation.create,
        httpMethod: 'POST',
        httpPath: instances,
        createParams,
      }),
      spec('get', 'get', {
        title: `Get ${name}`,
        description: `Returns the ${name} with the id, its behaviors' fields included.`,
        writes: false,
        policy: invocation.get,
        httpMethod: 'GET',
        httpPath: `${instances}/{id}`,
      }),
      spec('list', 'list', {
        title: `List ${name}`,
        description:
          where === undefined
            ? `Lists ${name} instances in creation order, a page at a time: pass a page's next as cursor for the page after it.`
            : `Lists ${name} instances in creation order, a page at a time: pass a page's next as cursor for the page after it. where keeps the instances whose fields hold the values it gives, a list meaning any of them; a filtered page can hold fewer than limit while next is not null.`,
        writes: false,
        policy: invocation.list,
        httpMethod: 'GET',
        httpPath: instances,
        ...(where === undefined ? {} : { where }),
      }),
      spec('update', 'update', {
        title: `Update ${name}`,
        description: `Applies a JSON merge patch (RFC 7386) to the ${name} with the id: a member replaces the instance's, a nested object merges and null removes a member. With expectedSeq the update is refused unless the instance is still at that sequence.`,
        writes: true,
        policy: invocation.update,
        httpMethod: 'PATCH',
        httpPath: `${instances}/{id}`,
        ...fenced,
      }),
      spec('delete', 'delete', {
        title: `Delete ${name}`,
        description: `Deletes the ${name} with the id. With expectedSeq the delete is refused unless the instance is still at that sequence.`,
        writes: true,
        policy: invocation.delete,
        httpMethod: 'DELETE',
        httpPath: `${instances}/{id}`,
        ...fenced,
      }),
    ];
    if (unique.length > 0) {
      tools.push(
        spec('lookup', 'lookup', {
          title: `Look up ${name}`,
          description: `Returns the ${name} whose unique fields hold the values key gives (${unique.map((index) => index.keys.join(' and ')).join('; ')}), its behaviors' fields included.`,
          writes: false,
          policy: invocation.lookup,
          httpMethod: 'GET',
          httpPath: `${schemaPath}/lookup`,
          key: this.keyProperty(record, unique),
        })
      );
    }
    for (const behavior of behaviors) {
      for (const operation of behavior.declaration.operations ?? []) {
        const schemaLevel = operation.scope === 'schema';
        tools.push(
          spec(schemaLevel ? 'schemaOperation' : 'operation', operation.name, {
            title: `${name}: ${operation.name}`,
            description: operation.description || `Operation ${operation.name} of behavior ${behavior.name}.`,
            writes: operation.writes === true,
            policy: operation.invocationPolicy ?? this.options.invocationPolicy.default,
            httpMethod: 'POST',
            httpPath: `${schemaLevel ? schemaPath : `${instances}/{id}`}/operations/${encodeURIComponent(operation.name)}`,
            behavior,
            operation,
            ...(schemaLevel ? {} : fenced),
          })
        );
      }
    }
    return tools;
  }

  // definition renders a tool as tools/schema.json writes it. A tool the
  // policy refuses the principal, or the mount's filter leaves out, is
  // hidden with that reason.
  private definition(principal: Principal, namespace: string, tool: ToolSpec, filter: ToolFilter | undefined): ToolDefinition {
    const keys = this.options.keys;
    const guidance = this.toolGuidance(tool);
    const refusal = tool.hidden === undefined ? this.refusal(principal, namespace, tool) : undefined;
    const filtered =
      tool.hidden === undefined && refusal === undefined && filter !== undefined && !kept(filter, principal, namespace, tool)
        ? `this mount's tool filter leaves it out of ${principal.subject}'s tools`
        : undefined;
    const hidden = tool.hidden ?? refusal ?? filtered;
    const mcp: ToolMCPRecord =
      hidden !== undefined
        ? { hidden: true, hiddenReason: hidden }
        : {
            hidden: false,
            name: tool.title,
            handle: tool.handle,
            description: tool.description,
            [this.options.invocationPolicy.key]: tool.policy,
            ...(keys.guidance !== '' ? { _meta: { [keys.guidance]: this.toolGuidance(tool) } } : {}),
          };
    const args = this.arguments(tool);
    return {
      name: tool.name,
      operationId: tool.operationId,
      title: tool.title,
      mcp,
      capability: '',
      lifecycle: '',
      visibility: '',
      audience: '',
      guidance,
      replay: tool.writes ? null : { mode: 'read_only', idempotencyKeyPointers: [], expectedRevisionPointers: [] },
      description: tool.description,
      namespace: tool.namespace,
      methodName: tool.methodName,
      httpMethod: tool.httpMethod,
      httpPath: tool.httpPath,
      requiresAuth: true,
      requiredPermissions: [],
      isScoped: false,
      bindingStatus: 'ready',
      inputSchemaDigest: argumentsDigest(args, keys.scalar),
      parameters: renderArguments(args, keys.scalar),
      returns: this.returns(tool),
    };
  }

  // refusal is why the policy hides a tool from the principal: a schema
  // tool asks the action its call asks. define_schema and the namespace
  // tools name their schema or namespace only when called, so they ask a
  // listing question (ListingAccessRequest): define, or manage with what
  // the tool does. The other engine tools only read, and answer with what
  // the policy lets the caller read, so they are in every list. In an
  // archived namespace a tool that writes there is hidden: the namespace
  // refuses the write.
  private refusal(principal: Principal, namespace: string, tool: ToolSpec): string | undefined {
    if (tool.writes && !NAMESPACE_TOOLS.has(tool.kind) && this.namespaces.archived(namespace)) {
      return `namespace ${namespace} is archived: it refuses every write until it is unarchived`;
    }
    const listing = LISTING_QUESTIONS.get(tool.kind);
    if (listing !== undefined) {
      return this.access.allowsListing(principal, listing.action, namespace, listing.operation)
        ? undefined
        : `the access policy refuses ${principal.subject} ${listing.operation === undefined ? `${listing.action} in namespace ${namespace}` : `${listing.action} (${listing.operation}) of namespaces`}`;
    }
    if (!tool.schema) {
      return undefined;
    }
    const action: SchemaAction = tool.writes ? 'write' : 'read';
    const operation = tool.kind === 'operation' || tool.kind === 'schemaOperation' ? tool.methodName : undefined;
    return this.access.allows(principal, action, namespace, tool.schema.name, operation)
      ? undefined
      : `the access policy refuses ${principal.subject} ${action} on ${tool.schema.name}${operation ? ` (${operation})` : ''}`;
  }

  // arguments is a tool's argument schema, with the vendor keys at its root.
  private arguments(tool: ToolSpec): ArgumentSchema {
    const vendor = this.options.keys.parameters;
    const id: Property = { type: 'string', description: 'The instance id', pattern: INSTANCE_ID.source };
    const expectedSeq: Property = {
      type: 'integer',
      description: "The sequence the caller last read (an instance's seq): the call is refused unless the instance is still at it",
      minimum: 0,
    };
    const valueRefs: Property = {
      type: 'boolean',
      description: 'Return each field the value store holds as its ref, { "$value": <hash>, "bytes": <n> }, listed in valueRefs, rather than its value, which get_value reads',
    };
    const schema = (properties: Array<[string, Property]>, required: string[]): ArgumentSchema => ({
      vendor,
      properties: new Map(properties),
      required: [...required].sort(),
    });
    switch (tool.kind) {
      case 'listSchemas':
        return schema([], []);
      case 'describeSchema':
        return schema([['name', { type: 'string', description: 'The schema name', pattern: SCHEMA_NAME.source }]], ['name']);
      case 'defineSchema':
        return schema([['document', { raw: { type: 'object', description: 'The schema-file document: kind General, a name, and the types; superschematic format --to=json writes it' } }]], ['document']);
      case 'listBehaviors':
        return schema([], []);
      case 'describeBehavior':
        return schema([['name', { type: 'string', description: 'The behavior name', pattern: BEHAVIOR_NAME.source }]], ['name']);
      case 'search':
        return schema(
          Object.entries(SEARCH_SCHEMAS_PARAMS.properties as Record<string, JSONSchemaValue>).map(([name, raw]): [string, Property] => [name, { raw }]),
          []
        );
      case 'getValue':
        return schema(
          [['hash', { type: 'string', description: "The value's hash: the SHA-256 of its canonical JSON, as a ref's $value holds it", pattern: VALUE_HASH.source }]],
          ['hash']
        );
      case 'listNamespaces':
        return schema([], []);
      case 'createNamespace':
      case 'archiveNamespace':
      case 'unarchiveNamespace':
        return schema([['name', { type: 'string', description: 'The namespace name', pattern: NAMESPACE_NAME.source }]], ['name']);
      case 'create': {
        const properties: Array<[string, Property]> = [
          ['id', { ...id, description: 'The instance id; the engine makes one when it is absent' }],
          ['data', this.dataProperty(tool.schema as SchemaRecord)],
        ];
        const takers = tool.createParams ?? [];
        if (takers.length > 0) {
          properties.push(['behaviors', { raw: createParamsOf(takers, this.guidanceOf(tool.schema as SchemaRecord).createParams) }]);
        }
        return schema(properties, ['data']);
      }
      case 'get':
        return schema(
          [
            ['id', id],
            ['valueRefs', valueRefs],
          ],
          ['id']
        );
      case 'list':
        return schema(
          [
            ['limit', { type: 'integer', description: 'How many instances a page holds, 50 when absent', minimum: 1, maximum: MAX_PAGE_SIZE }],
            ['cursor', { type: 'string', description: "The previous page's next" }],
            ...(tool.where === undefined ? [] : [['where', tool.where] as [string, Property]]),
            ['valueRefs', valueRefs],
          ],
          []
        );
      case 'lookup':
        return schema(
          [
            ['key', tool.key as Property],
            ['valueRefs', valueRefs],
          ],
          ['key']
        );
      case 'update': {
        const record = tool.schema as SchemaRecord;
        const patch = patchOf(this.dataProperty(record), `A JSON merge patch of the ${record.instanceType}`);
        return schema(
          [
            ['id', id],
            ['patch', withRules(patch, this.fieldsOf(record).rules.patch)],
            ['expectedSeq', expectedSeq],
            ...preconditionsEntry(tool),
          ],
          ['id', 'patch']
        );
      }
      case 'delete':
        return schema(
          [
            ['id', id],
            ['expectedSeq', expectedSeq],
            ...preconditionsEntry(tool),
          ],
          ['id']
        );
      case 'operation': {
        const params = (tool.operation as BehaviorOperationDeclaration).paramsSchema;
        const required = paramsRequired(params);
        return schema(
          [
            ['id', id],
            ['params', { raw: params }],
            ['expectedSeq', expectedSeq],
            ...preconditionsEntry(tool),
          ],
          required ? ['id', 'params'] : ['id']
        );
      }
      case 'schemaOperation': {
        const params = (tool.operation as BehaviorOperationDeclaration).paramsSchema;
        const required = paramsRequired(params);
        return schema([['params', { raw: params }]], required ? ['params'] : []);
      }
    }
  }

  // dataProperty is the instance's own fields: the schema a create takes,
  // with what the behaviors hold them to.
  private dataProperty(record: SchemaRecord): Property {
    const { input, rules } = this.fieldsOf(record);
    return withRules(
      {
        type: 'object',
        description: `${record.instanceType} object`,
        additionalProperties: false,
        properties: input.properties,
        required: input.required,
      },
      rules.instance
    );
  }

  // whereProperty is a list's where argument: a member per field the
  // version filters on, a value of its type or a list of them; undefined
  // when it filters on none.
  private whereProperty(record: SchemaRecord, behaviors: ComposedBehavior[]): Property | undefined {
    const filters = this.catalog.runtimeOf(record).filters;
    if (filters.size === 0) {
      return undefined;
    }
    const properties: Record<string, unknown> = {};
    for (const filterable of filters.values()) {
      const one = this.filterValueSchema(record, behaviors, filterable.key, filterable.behavior, filterable.type);
      properties[filterable.key] = {
        anyOf: [one, { type: 'array', items: one, minItems: 1, maxItems: MAX_FILTER_VALUES }],
      };
    }
    return {
      raw: {
        type: 'object',
        description: `The values the instances hold, by field: a value, or a list of 1 to ${MAX_FILTER_VALUES} meaning any of them; every member must hold`,
        additionalProperties: false,
        properties,
      },
    };
  }

  // keyProperty is a lookup's key argument: the values of one unique
  // index's fields, each required.
  private keyProperty(record: SchemaRecord, unique: readonly OwnIndex[]): Property {
    const filters = this.catalog.runtimeOf(record).filters;
    const branches = unique.map((index) => ({
      type: 'object',
      additionalProperties: false,
      properties: Object.fromEntries(
        index.keys.map((key) => [key, this.filterValueSchema(record, [], key, undefined, (filters.get(key) as Filterable).type)])
      ),
      required: [...index.keys],
    }));
    const description = 'The values of the fields of one unique index of the instance type, by field';
    return { raw: branches.length === 1 ? { ...branches[0], description } : { type: 'object', description, oneOf: branches } };
  }

  // filterValueSchema is the JSON Schema of one value of a field a filter
  // or a key names: an own field's, not null, or a behavior field's type
  // with its declared description.
  private filterValueSchema(record: SchemaRecord, behaviors: ComposedBehavior[], key: string, behavior: string | undefined, type: string): unknown {
    if (behavior === undefined) {
      const property = this.fieldsOf(record).input.properties.get(key);
      if (property !== undefined) {
        return renderProperty({ ...property, nullable: false }, this.options.keys.scalar);
      }
    }
    const declared = behaviors.find((candidate) => candidate.name === behavior)?.declaration.fields?.find((field) => field.name === key);
    return { type, ...(declared?.description ? { description: declared.description } : {}) };
  }

  // instanceSchema is an instance's data as reads return it: its own
  // fields, then its behaviors' fields, read-only, with what the behaviors
  // hold the own fields to.
  private instanceSchema(record: SchemaRecord, behaviors: ComposedBehavior[]): JSONSchemaObject {
    const { input, rules } = this.fieldsOf(record);
    const properties = new Map(input.properties);
    for (const behavior of behaviors) {
      for (const field of behavior.declaration.fields ?? []) {
        properties.set(field.name, { raw: { ...(field.description ? { description: field.description } : {}), readOnly: true } });
      }
    }
    const schema = renderArguments({ vendor: [], properties, required: input.required }, this.options.keys.scalar);
    return rules.instance.length > 0 ? { ...schema, allOf: [...rules.instance] } : schema;
  }

  // toolGuidance is a tool's guidance: an engine tool's own, or what the
  // engine and the schema's behaviors say about the operation.
  private toolGuidance(tool: ToolSpec): ToolGuidance {
    if (!tool.schema) {
      return engineGuidance(tool.kind as EngineTool);
    }
    const said = this.guidanceOf(tool.schema).operations.get(tool.methodName);
    if (!said) {
      throw new Error(`no guidance for ${tool.name}, an operation of the version`);
    }
    return jsonCopyOf(said);
  }

  // guidanceOf is what a version's behaviors say about it, computed once.
  private guidanceOf(record: SchemaRecord): VersionGuidance {
    const key = versionKey(record);
    let cached = this.guidance.get(key);
    if (!cached) {
      const runtime = this.catalog.runtimeOf(record);
      cached = versionGuidance(record.name, runtime.composition, {
        unique: runtime.indexes.filter((index) => index.unique).map((index) => index.keys),
        filters: [...runtime.filters.keys()],
      });
      this.guidance.set(key, cached);
    }
    return cached;
  }

  private fieldsOf(record: SchemaRecord): InstanceSchemas {
    const key = versionKey(record);
    let cached = this.instanceSchemas.get(key);
    if (!cached) {
      const fields = new FieldSchemas(record.document);
      cached = {
        input: fields.object(record.instanceType),
        fields,
        rules: { instance: this.instanceRules(record, fields, 'instance'), patch: this.instanceRules(record, fields, 'patch') },
      };
      this.instanceSchemas.set(key, cached);
    }
    return cached;
  }

  // instanceRules is what a version's behaviors hold an instance's own
  // fields to, in a form: each behavior's instanceSchema, in list order,
  // which renders the types its checkedTypes names as this document
  // renders a nested type.
  private instanceRules(record: SchemaRecord, fields: FieldSchemas, form: InstanceSchemaForm): unknown[] {
    const rules: unknown[] = [];
    for (const bound of this.catalog.runtimeOf(record).composition.behaviors) {
      const instanceSchema = bound.behavior.implementation.instanceSchema;
      if (!instanceSchema) {
        continue;
      }
      const name = bound.behavior.name;
      const typeSchema: TypeSchema = (type, options) => {
        if (typeof type !== 'string' || !bound.checked.includes(type)) {
          throw new BehaviorError(
            name,
            `instanceSchema: ${String(type)} is not a type its checkedTypes names (${bound.checked.length > 0 ? bound.checked.join(', ') : 'none'})`
          );
        }
        const value = fields.type(type);
        const shaped = form === 'patch' ? patchOf(value) : value;
        return renderProperty({ ...shaped, nullable: options?.nullable === true }, this.options.keys.scalar);
      };
      const answer: unknown = instanceSchema.call(bound.behavior.implementation, bound.config, form, typeSchema);
      synchronous(name, 'instanceSchema', answer);
      const copied = jsonCopy(answer);
      if (!('value' in copied) || !Array.isArray(copied.value)) {
        throw new BehaviorError(name, 'instanceSchema returns a list of JSON Schemas');
      }
      rules.push(...(copied.value as unknown[]));
    }
    return rules;
  }

  private resultSchema(tool: ToolSpec, behaviors: ComposedBehavior[]): JSONSchemaValue {
    const record = tool.schema as SchemaRecord;
    switch (tool.kind) {
      case 'create':
      case 'get':
      case 'update':
      case 'lookup':
        return instanceRecordSchema(this.instanceSchema(record, behaviors));
      case 'list':
        return {
          type: 'object',
          additionalProperties: false,
          properties: {
            items: { type: 'array', items: instanceRecordSchema(this.instanceSchema(record, behaviors)) },
            next: { type: ['string', 'null'], description: "The next page's cursor; null after the last page" },
          },
          required: ['items', 'next'],
        };
      case 'delete':
        return { type: 'null' };
      case 'operation':
      case 'schemaOperation':
        return (tool.operation as BehaviorOperationDeclaration).resultSchema;
      default:
        return true;
    }
  }

  // returns is a tool's return shape as tools/schema.json writes it
  // (ir.ToolReturnSchema).
  private returns(tool: ToolSpec): ToolDefinition['returns'] {
    const name = tool.schema?.name;
    switch (tool.kind) {
      case 'create':
      case 'get':
      case 'update':
      case 'lookup':
        return { type: 'object', description: `${name} instance` };
      case 'list':
        return { type: 'object', description: `A page of ${name} instances` };
      case 'delete':
        return { type: 'null', description: 'Nothing: the instance is deleted' };
      case 'listSchemas':
        return { type: 'array', description: 'Array of schema summaries', items: { type: 'object', description: 'A schema summary' } };
      case 'describeSchema':
        return { type: 'object', description: 'The describe document' };
      case 'defineSchema':
        return { type: 'object', description: 'The draft' };
      case 'listBehaviors':
        return { type: 'array', description: 'Array of behavior summaries', items: { type: 'object', description: 'A behavior summary' } };
      case 'describeBehavior':
        return { type: 'object', description: "The behavior's declaration" };
      case 'search':
        return { type: 'object', description: 'A page of hits across the schemas, best first' };
      case 'getValue':
        return { type: 'object', description: 'The value, with its hash and its canonical JSON\'s length in bytes' };
      case 'listNamespaces':
        return { type: 'array', description: 'Array of namespaces', items: { type: 'object', description: 'A namespace' } };
      case 'createNamespace':
      case 'archiveNamespace':
      case 'unarchiveNamespace':
        return { type: 'object', description: 'The namespace' };
      case 'operation':
      case 'schemaOperation': {
        const result = (tool.operation as BehaviorOperationDeclaration).resultSchema;
        const type = typeOf(result);
        const description = isPlainObject(result) && typeof result.description === 'string' ? result.description : `The result of ${tool.methodName}`;
        const items = isPlainObject(result) && type === 'array' && result.items !== undefined ? { type: typeOf(result.items) } : undefined;
        return { type, description, ...(items ? { items } : {}) };
      }
    }
  }
}

/**
 * createParamsOf is a create's behaviors argument: by behavior name, the
 * create parameters of each behavior that declares a createParamsSchema:
 * what its implementation narrows them to under its config, else the
 * declaration's.
 */
function createParamsOf(behaviors: readonly ComposedBehavior[], narrowed: ReadonlyMap<string, unknown>): JSONSchemaObject {
  const properties: Record<string, unknown> = {};
  for (const behavior of behaviors) {
    properties[behavior.name] = narrowed.get(behavior.name) ?? behavior.declaration.createParamsSchema;
  }
  return {
    type: 'object',
    description: "Each behavior's create parameters, by behavior name; they hold from the create on, in its transaction",
    additionalProperties: false,
    properties,
  };
}

// versionKey names a published version, which never changes, in the
// catalog's caches.
function versionKey(record: SchemaRecord): string {
  return `${record.namespace}\u0000${record.name}\u0000${String(record.version)}\u0000${record.hash}`;
}

// jsonCopyOf copies a cached guidance, so a caller that changes what it
// gets changes nothing the next caller reads.
function jsonCopyOf(guidance: ToolGuidance): ToolGuidance {
  return JSON.parse(JSON.stringify(guidance)) as ToolGuidance;
}

// paramsRequired is whether an operation's parameters must hold a member:
// its schema requires one, or every branch of its anyOf or oneOf does, as
// search's query or vector.
function paramsRequired(params: unknown): boolean {
  if (!isPlainObject(params)) {
    return false;
  }
  if (Array.isArray(params.required) && params.required.length > 0) {
    return true;
  }
  return (['anyOf', 'oneOf'] as const).some((key) => {
    const branches = params[key];
    return Array.isArray(branches) && branches.length > 0 && branches.every(paramsRequired);
  });
}

/** The JSON Schema of an instance as the engine returns it. */
function instanceRecordSchema(data: JSONSchemaObject): JSONSchemaObject {
  return {
    type: 'object',
    additionalProperties: false,
    properties: {
      namespace: { type: 'string', description: 'The namespace that holds the instance' },
      schema: { type: 'string' },
      id: { type: 'string' },
      schemaNamespace: { type: 'string', description: 'The namespace that holds the schema' },
      version: { type: 'integer', description: 'The schema version the instance was last written with' },
      seq: { type: 'integer', description: "The sequence of the instance's last event: its entity tag" },
      data,
      createdAt: { type: 'integer', description: 'Epoch milliseconds' },
      createdBy: { type: 'string' },
      updatedAt: { type: 'integer', description: 'Epoch milliseconds' },
      updatedBy: { type: 'string' },
      valueRefs: {
        type: 'array',
        items: { type: 'string' },
        description: 'For a read with valueRefs: the JSON pointers into data of the fields that hold a ref, { "$value": <hash>, "bytes": <n> }, in place of a value the value store holds',
      },
    },
    required: ['namespace', 'schema', 'id', 'schemaNamespace', 'version', 'seq', 'data', 'createdAt', 'createdBy', 'updatedAt', 'updatedBy'],
  };
}

// withRules adds what the behaviors hold an object to, as allOf, when
// they hold it to anything.
function withRules(property: Property, rules: readonly unknown[]): Property {
  return rules.length > 0 ? { ...property, allOf: rules } : property;
}

// patchOf is the schema of a merge patch of an object: its properties
// with none required, down through nested objects, which a patch merges.
// A list is replaced whole, so its items keep theirs.
function patchOf(property: Property, description?: string): Property {
  if (property.properties === undefined) {
    return property;
  }
  const properties = new Map<string, Property>();
  for (const [key, child] of property.properties) {
    properties.set(key, patchOf(child));
  }
  const { allOf: _allOf, ...rest } = property;
  return { ...rest, ...(description ? { description } : {}), properties, required: [] };
}

// typeOf is a result schema's type as ir.ToolSchemaType holds it: its
// type, or every JSON type when it names none.
function typeOf(schema: unknown): string | string[] {
  if (isPlainObject(schema)) {
    if (typeof schema.type === 'string') {
      return schema.type;
    }
    if (Array.isArray(schema.type) && schema.type.length > 0 && schema.type.every((type) => typeof type === 'string')) {
      return schema.type as string[];
    }
  }
  return [...ANY_JSON_TYPES];
}

// argumentsOf reads a call's arguments: a JSON object, {} when absent.
function argumentsOf(tool: ToolSpec, args: unknown): Record<string, unknown> {
  if (args === undefined || args === null) {
    return {};
  }
  if (!isPlainObject(args)) {
    throw new EngineError('invalid_argument', `${tool.handle}: the arguments are a JSON object`);
  }
  return args;
}

function only(tool: ToolSpec, args: Record<string, unknown>, allowed: string[]): void {
  const unknown = Object.keys(args).filter((key) => !allowed.includes(key) && args[key] !== undefined);
  if (unknown.length > 0) {
    throw new EngineError(
      'invalid_argument',
      `${tool.handle} takes ${allowed.length > 0 ? allowed.join(', ') : 'no arguments'}, not ${unknown.map((key) => JSON.stringify(key)).join(', ')}`
    );
  }
}

function requiredString(tool: ToolSpec, args: Record<string, unknown>, key: string): string {
  const value = args[key];
  if (typeof value !== 'string') {
    throw new EngineError('invalid_argument', `${tool.handle}: ${key} is a string, and required`);
  }
  return value;
}

function optionalString(tool: ToolSpec, args: Record<string, unknown>, key: string): string | undefined {
  const value = args[key];
  if (value === undefined || value === null) {
    return undefined;
  }
  if (typeof value !== 'string') {
    throw new EngineError('invalid_argument', `${tool.handle}: ${key} is a string`);
  }
  return value;
}

// preconditionsProperty is the preconditions argument of a schema's
// writes: an entry for each behavior that declares a preconditionSchema,
// none required; undefined when none does.
function preconditionsProperty(behaviors: ComposedBehavior[]): Property | undefined {
  const properties = new Map<string, Property>();
  for (const behavior of behaviors) {
    if (behavior.declaration.preconditionSchema !== undefined) {
      properties.set(behavior.name, { raw: behavior.declaration.preconditionSchema });
    }
  }
  if (properties.size === 0) {
    return undefined;
  }
  return {
    type: 'object',
    description: "Preconditions by behavior: each entry is checked against its behavior's schema and handed to its guard, which refuses the call when it does not hold",
    additionalProperties: false,
    properties,
    required: [],
  };
}

// preconditionsArgument names the preconditions argument when the tool takes one.
function preconditionsArgument(tool: ToolSpec): string[] {
  return tool.preconditions === undefined ? [] : ['preconditions'];
}

function preconditionsEntry(tool: ToolSpec): Array<[string, Property]> {
  return tool.preconditions === undefined ? [] : [['preconditions', tool.preconditions]];
}

// preconditionsOf reads the preconditions argument, which the engine checks.
function preconditionsOf(tool: ToolSpec, args: Record<string, unknown>): { preconditions?: Record<string, unknown> } {
  const value = args.preconditions;
  if (value === undefined || value === null) {
    return {};
  }
  if (!isPlainObject(value)) {
    throw new EngineError('invalid_argument', `${tool.handle}: preconditions is a JSON object of each behavior's entry by its name`);
  }
  return { preconditions: value };
}

function valueRefsOf(tool: ToolSpec, args: Record<string, unknown>): { valueRefs?: boolean } {
  const value = args.valueRefs;
  if (value === undefined || value === null) {
    return {};
  }
  if (typeof value !== 'boolean') {
    throw new EngineError('invalid_argument', `${tool.handle}: valueRefs is a boolean`);
  }
  return { valueRefs: value };
}

function expectedSeqOf(tool: ToolSpec, args: Record<string, unknown>): { expectedSeq?: number } {
  const value = args.expectedSeq;
  if (value === undefined || value === null) {
    return {};
  }
  if (typeof value !== 'number') {
    throw new EngineError('invalid_argument', `${tool.handle}: expectedSeq is a non-negative integer`);
  }
  return { expectedSeq: value };
}

/** kebabCase is codegen.ToKebabCase: a hyphen at each lower-to-upper boundary, then lowercase. */
export function kebabCase(name: string): string {
  return name.replace(/([a-z0-9])([A-Z])/g, '$1-$2').toLowerCase();
}

/** snakeCase is codegen.ToSnakeCase: hyphens become underscores, an underscore at each lower-to-upper boundary, then lowercase. */
export function snakeCase(name: string): string {
  return name.replace(/-/g, '_').replace(/([a-z0-9])([A-Z])/g, '$1_$2').toLowerCase();
}
