/*
The tools (D11, D16): a schema's describe document, a namespace's tools
document and the MCP tools it lists, the behavior catalog, and the
guidance each carries, as engine.tools returns them.
*/

import type { Principal } from '../access.js';
import type { JSONSchema, OperationScope } from './declarations.js';

/** The longest MCP handle @mcp accepts (ir.MCPHandleMaxLength). */
export const MCP_HANDLE_MAX_LENGTH = 48;

/** An MCP handle @mcp accepts: lowercase snake case starting with a letter. */
export const MCP_HANDLE = /^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$/;

/** How a UI shows a state of a Workflow (@display, D48): its label, its form while active and its tone. */
export interface DisplayState {
  activeForm?: string;
  label?: string;
  tone?: 'muted' | 'active' | 'success' | 'warning' | 'danger';
}

/** A type's display (@display, D48), as the schema-file document holds it. */
export interface TypeDisplay {
  createLabel?: string;
  /** The glyph a UI shows beside the type's name, by a name an app's icon set draws (D48, amended). */
  icon?: string;
  noun?: string;
  plural?: string;
  states?: { [key: string]: DisplayState };
  summaryFields?: string[];
  titleField?: string;
  transitions?: { [key: string]: { [key: string]: string } };
}

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
  /** The JSON Schema of an instance's data: its own fields, closed. */
  instance: JSONSchemaObject;
  /**
   * The JSON Schema of an instance's behaviors: by behavior name, an
   * object of each field the behavior declares, read-only, for every
   * behavior on the type that declares one; a volatile field says
   * `volatile: true`, and a per-reader one `perReader: true` besides.
   */
  instanceBehaviors: JSONSchemaObject;
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
  /** Its fields, as its declaration lists them; volatile, true or absent, for one that can move without an event of its own instance; perReader, true or absent, for a volatile one whose value is the caller's, which no event records. */
  fields: Array<{ name: string; description?: string; volatile?: true; perReader?: true }>;
  /** Its operations on the type: an instance's and the schema's. */
  operations: string[];
  /** Its namespace operations (D77), which run on no schema and the tools document lists by the behavior's name; absent when it has none. */
  namespaceOperations?: string[];
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
  scope?: Exclude<OperationScope, 'namespace'>;
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

/** A behavior as list_behaviors and GET /behaviors list it. */
export interface BehaviorSummary {
  name: string;
  description?: string;
  /** Behaviors a type that composes it must also compose. */
  requires: string[];
  /** Behaviors a type that composes it may not compose. */
  conflicts: string[];
  /** The fields it adds to an instance, by name. */
  fields: string[];
  /** The operations it adds, by name. */
  operations: string[];
}

/** A behavior's declaration, as describe_behavior and GET /behaviors/{name} return it. */
export interface BehaviorDocument {
  name: string;
  description?: string;
  /** The JSON Schema of the config a type gives it; absent, it takes none. */
  configSchema?: JSONSchema;
  /** The JSON Schema of the parameters a create gives it, under the create's `behaviors`; absent, it takes none. */
  createParamsSchema?: JSONSchema;
  /** The JSON Schema of its entry in a write's preconditions; absent, it takes none. */
  preconditionSchema?: JSONSchema;
  requires: string[];
  conflicts: string[];
  /** Its fields; volatile, true or absent, for one that can move without an event of its own instance; perReader, true or absent, for a volatile one whose value is the caller's, which no event records. */
  fields: Array<{ name: string; description?: string; volatile?: true; perReader?: true }>;
  operations: BehaviorOperationDocument[];
  /** Its signals (D77), present when it declares any. */
  signals?: Array<{ name: string; description?: string; access: 'read' | 'write' | 'operation'; paramsSchema: JSONSchema }>;
  /** The codes its vetoes carry. */
  vetoes: Array<{ code: string; description?: string }>;
}

/** One operation of a behavior's document. */
export interface BehaviorOperationDocument {
  name: string;
  description?: string;
  /** What it runs on: an instance, the schema as a whole, or the namespace (D77). */
  scope: OperationScope;
  writes: boolean;
  /** True for an operation beside the instance (D77); absent otherwise. */
  beside?: true;
  /** False for a beside operation that appends no event; absent otherwise. */
  log?: false;
  /** `read` for a beside operation the policy is asked read for (D77); absent otherwise. */
  asks?: 'read';
  /** The invocation policy, its declaration's or the policy's default, under the policy's key (after writes, as the describe document has it). */
  [policyKey: string]: unknown;
  /** Its parameters: an object schema with `additionalProperties: false`. */
  paramsSchema: JSONSchema;
  resultSchema: JSONSchema;
}

/** A tool's guidance, as the tools document writes it: ir.ToolOperationGuidance. */
export interface ToolGuidance {
  useWhen: string;
  doNotUseWhen: string;
  success: string;
  errors: Array<{ code: string; description: string; commonCorrection: string }>;
}

/** A JSON Schema: an object, or true or false. */
export type JSONSchemaValue = boolean | { readonly [key: string]: unknown };

/**
 * The JSON types an `any` property takes, as tools/schema.json lists them:
 * every one but null (D14, amended). A nullable one also takes null.
 */
export const JSON_VALUE_TYPES: readonly string[] = ['object', 'array', 'string', 'number', 'boolean'];

/** Every JSON type, null included: a result schema that names no type. */
export const ANY_JSON_TYPES: readonly string[] = [...JSON_VALUE_TYPES, 'null'];
