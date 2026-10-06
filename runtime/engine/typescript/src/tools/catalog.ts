/*
What the engine serves about a namespace's schemas besides their
instances (D16): a describe document per schema, the tools document in
the shape the SDK generators write to tools/schema.json (ir.ToolManifest),
and the calls those tools make. The MCP endpoint (@superschematic/engine/mcp)
and the HTTP routes read them from here; every read and call goes through
the schema registry and the instance store, so the access policy answers
each one.

A tool is one operation: create, get, list, update and delete of every
live schema the namespace reaches (create takes the parameters its
behaviors declare a createParamsSchema for, under behaviors, and create's
data, update's patch and the describe document's instance carry what the
behaviors' validate holds the fields to, as allOf entries their
instanceSchema writes), each operation its behaviors add (a
schema-level one takes its parameters and no instance id), three tools
for writing schemas: list, describe and define a draft, and where a
schema the caller may read composes Search, search, the search across
the namespace's schemas (engine.search). The
update, delete and instance operation tools of a schema one of whose
behaviors declares a preconditionSchema take `preconditions`, each such
behavior's entry by its name, as the HTTP API's Preconditions header
carries them. No tool publishes: a draft goes live only through an HTTP
call the access policy governs, so an MCP client cannot put a schema
live on its own.

Names follow the SDK generators: a tool's name is `<namespace>.<method>`,
the namespace the schema name in kebab case (codegen.ToKebabCase) and the
method the operation's name, as `order.create` or `line-item.addNote`. An
SDK tool's MCP handle is authored with @mcp; the engine derives it from
the same two parts in snake case (codegen.ToSnakeCase), as `order_create`,
and the schema tools are `list_schemas`, `describe_schema` and
`define_schema`. A handle @mcp would refuse (not lowercase snake case, or
longer than 48 characters) or one two tools derive hides both tools, with
the reason; the engine's schema tools keep theirs. A tool the access
policy refuses the caller is hidden too, with that reason, and can still
be called by its handle: the call is refused.
*/

import { checkPrincipal, type Access, type Action, type Principal } from '../access.js';
import type { InstanceSchemaForm, TypeSchema } from '../behaviors/behavior.js';
import { SEARCH_SCHEMAS_PARAMS, searchSchemas } from '../behaviors/core/index.js';
import type { BehaviorOperationDeclaration, OperationScope } from '../behaviors/declaration.js';
import { jsonCopy } from '../behaviors/json.js';
import { synchronous } from '../behaviors/storage.js';
import { BehaviorError, EngineError } from '../errors.js';
import { isPlainObject } from '../instances/patch.js';
import { INSTANCE_ID, type InstanceStore } from '../instances/store.js';
import type { Namespaces } from '../namespaces.js';
import { MAX_PAGE_SIZE } from '../paging.js';
import type { SchemaCatalog, SchemaRecord, SchemaSummary } from '../registry/catalog.js';
import { SCHEMA_NAME } from '../registry/document.js';
import type { ComposedBehavior, SchemaRegistry } from '../registry/registry.js';
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
  /** The JSON Schema of an instance's data: closed, its behaviors' fields read-only. */
  instance: JSONSchemaObject;
  behaviors: DescribedBehavior[];
  /** create, get, list, update, delete, then each behavior's operations in the type's list order. */
  operations: DescribedOperation[];
}

/** A behavior a schema's instance type composes. */
export interface DescribedBehavior {
  name: string;
  description?: string;
  /** The type's config of it, as the schema holds it; {} when it gives none. */
  config: unknown;
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
  description: string;
  writes: boolean;
  params: JSONSchemaObject;
  result: JSONSchemaValue;
  /** Its tool's name in the tools document. */
  tool: string;
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
  guidance: { useWhen: string; doNotUseWhen: string; success: string; errors: Array<{ code: string; description: string; commonCorrection: string }> };
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
}

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
  | 'operation'
  | 'schemaOperation'
  | 'listSchemas'
  | 'describeSchema'
  | 'defineSchema'
  | 'search';

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
}

const TOOL_SCHEMA = 'https://json-schema.org/draft/2020-12/schema';
const EMPTY_GUIDANCE = (): ToolDefinition['guidance'] => ({ useWhen: '', doNotUseWhen: '', success: '', errors: [] });

/** A version's argument schemas: its instance type's fields, and what its behaviors hold them to in each form. */
interface InstanceSchemas {
  readonly input: ArgumentSchema;
  readonly fields: FieldSchemas;
  readonly rules: Readonly<Record<InstanceSchemaForm, readonly unknown[]>>;
}

export class ToolCatalog {
  // The argument schemas of each published version, which never changes.
  private readonly instanceSchemas = new Map<string, InstanceSchemas>();

  constructor(
    private readonly namespaces: Namespaces,
    private readonly access: Access,
    private readonly schemas: SchemaRegistry,
    private readonly instances: InstanceStore,
    /** The invocation policy, the built-in tools' policies and the vendor keys. */
    readonly options: ResolvedToolOptions,
    /** The versions' runtimes, for what their behaviors hold an instance's fields to; the registry has asked the policy. */
    private readonly catalog: SchemaCatalog
  ) {}

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
    return {
      namespace,
      name: record.name,
      schemaNamespace: record.namespace,
      version: record.version as number,
      hash: record.hash,
      instanceType: record.instanceType,
      ...(description ? { description } : {}),
      instance: this.instanceSchema(record, behaviors),
      behaviors: behaviors.map((bound) => ({
        name: bound.name,
        ...(bound.declaration.description ? { description: bound.declaration.description } : {}),
        config: bound.config,
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
        description: tool.description,
        writes: tool.writes,
        [policyKey]: tool.policy,
        params: renderArguments(this.arguments(tool), this.options.keys.scalar),
        result: this.resultSchema(tool, behaviors),
        tool: tool.name,
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
      tools: tools.map((tool) => this.definition(principal, namespace, tool)),
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
    if (!tool) {
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
        only(tool, input, ['id']);
        const id = requiredString(tool, input, 'id');
        const record = this.instances.get(principal, schema, id, { namespace });
        if (!record) {
          throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
        }
        return record;
      }
      case 'list': {
        only(tool, input, ['limit', 'cursor']);
        const limit = input.limit ?? undefined;
        if (limit !== undefined && typeof limit !== 'number') {
          throw new EngineError('invalid_argument', `${tool.handle}: limit is an integer`);
        }
        const cursor = optionalString(tool, input, 'cursor');
        return this.instances.list(principal, schema, { namespace, ...(limit !== undefined ? { limit } : {}), ...(cursor !== undefined ? { cursor } : {}) });
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
        "Returns a schema's describe document: the JSON Schema of an instance, the behaviors its type composes with their config, and every operation with its parameters, its result and its tool.",
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
  // delete, then its behaviors' operations.
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
        description: `Lists ${name} instances in creation order, a page at a time: pass a page's next as cursor for the page after it.`,
        writes: false,
        policy: invocation.list,
        httpMethod: 'GET',
        httpPath: instances,
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
  // policy refuses the principal is hidden with that reason.
  private definition(principal: Principal, namespace: string, tool: ToolSpec): ToolDefinition {
    const keys = this.options.keys;
    const guidance = EMPTY_GUIDANCE();
    const refusal = tool.hidden === undefined ? this.refusal(principal, namespace, tool) : undefined;
    const hidden = tool.hidden ?? refusal;
    const mcp: ToolMCPRecord =
      hidden !== undefined
        ? { hidden: true, hiddenReason: hidden }
        : {
            hidden: false,
            name: tool.title,
            handle: tool.handle,
            description: tool.description,
            [this.options.invocationPolicy.key]: tool.policy,
            ...(keys.guidance !== '' ? { _meta: { [keys.guidance]: EMPTY_GUIDANCE() } } : {}),
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
  // tool asks the action its call asks. The schema tools name no schema
  // until they are called, so the policy answers each call.
  private refusal(principal: Principal, namespace: string, tool: ToolSpec): string | undefined {
    if (!tool.schema) {
      return undefined;
    }
    const action: Action = tool.writes ? 'write' : 'read';
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
      case 'search':
        return schema(
          Object.entries(SEARCH_SCHEMAS_PARAMS.properties as Record<string, JSONSchemaValue>).map(([name, raw]): [string, Property] => [name, { raw }]),
          []
        );
      case 'create': {
        const properties: Array<[string, Property]> = [
          ['id', { ...id, description: 'The instance id; the engine makes one when it is absent' }],
          ['data', this.dataProperty(tool.schema as SchemaRecord)],
        ];
        const takers = tool.createParams ?? [];
        if (takers.length > 0) {
          properties.push(['behaviors', { raw: createParamsOf(takers) }]);
        }
        return schema(properties, ['data']);
      }
      case 'get':
        return schema([['id', id]], ['id']);
      case 'list':
        return schema(
          [
            ['limit', { type: 'integer', description: 'How many instances a page holds, 50 when absent', minimum: 1, maximum: MAX_PAGE_SIZE }],
            ['cursor', { type: 'string', description: "The previous page's next" }],
          ],
          []
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

  private fieldsOf(record: SchemaRecord): InstanceSchemas {
    const key = `${record.namespace}\u0000${record.name}\u0000${String(record.version)}\u0000${record.hash}`;
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
      case 'search':
        return { type: 'object', description: 'A page of hits across the schemas, best first' };
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
 * createParamsSchema of each behavior that declares one, as its
 * declaration holds it.
 */
function createParamsOf(behaviors: readonly ComposedBehavior[]): JSONSchemaObject {
  const properties: Record<string, unknown> = {};
  for (const behavior of behaviors) {
    properties[behavior.name] = behavior.declaration.createParamsSchema;
  }
  return {
    type: 'object',
    description: "Each behavior's create parameters, by behavior name; they hold from the create on, in its transaction",
    additionalProperties: false,
    properties,
  };
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
