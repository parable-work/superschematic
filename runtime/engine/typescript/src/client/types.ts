/*
What the engine's HTTP API sends and takes, as JSON. The client declares
these shapes itself rather than importing the server's: the server's
modules bring node:sqlite, the schema runtime and the HTTP runtime into a
consumer's compile, and the client runs where none of them does, a
browser or a worker runtime. test/client-types.test.ts holds each shape
to the server's type, so a change to one fails the build until the other
follows.
*/

/** A JSON object. */
export type JSONObject = Record<string, unknown>;

/** A JSON Schema, as the describe document and the behavior catalog carry them. */
export type JSONSchema = Record<string, unknown> | boolean;

/** An instance's behavior fields: by behavior name, an object of the fields the behavior declares that have a value. */
export type BehaviorFieldsJSON = Record<string, JSONObject>;

/**
 * A stored instance, as a read returns it. `data` holds its own fields;
 * `behaviors` its behaviors' fields, under each behavior's name, so the two
 * never collide.
 */
export interface Instance<T = JSONObject, B = BehaviorFieldsJSON> {
  namespace: string;
  schema: string;
  id: string;
  /** The namespace that holds the schema: the instance's own, or the shared one. */
  schemaNamespace: string;
  /** The schema version it was last written with. */
  version: number;
  /** The sequence of its last event, its entity tag. */
  seq: number;
  data: T;
  behaviors: B;
  createdAt: number;
  createdBy: string;
  updatedAt: number;
  updatedBy: string;
  /**
   * For a read that asked for valueRefs: the JSON pointers into data of
   * the own fields that hold a ref, `{ "$value": <hash>, "bytes": <n> }`,
   * in place of a value the value store holds. Absent when none does.
   */
  valueRefs?: string[];
}

/** A page of instances in creation order; `next` is null after the last. */
export interface InstancePage<T = JSONObject, B = BehaviorFieldsJSON> {
  items: Array<Instance<T, B>>;
  next: string | null;
}

/** A schema version as the API returns it: the stored record without its canonical text, which `hash` identifies. */
export interface SchemaVersion {
  namespace: string;
  name: string;
  /** The published version, or null for the draft. */
  version: number | null;
  instanceType: string;
  document: JSONObject;
  hash: string;
  definedAt: number;
  definedBy: string | null;
  publishedAt: number | null;
  publishedBy: string | null;
}

/** A namespace, as `GET /namespaces` lists it (runtime/engine/README.md, "Namespaces"). */
export interface NamespaceRecord {
  name: string;
  /** configured: the engine's options name it; created: a create made it while the engine ran. */
  origin: 'configured' | 'created';
  /** Whether it is the shared namespace, which every other one looks schema names up in after itself. */
  shared: boolean;
  /** active, or archived: read as it was, refusing every write until it is unarchived. */
  state: 'active' | 'archived';
  /** When a create made it, and who; null for a configured one. */
  createdAt: number | null;
  createdBy: string | null;
  /** When it was archived, and who; null unless it is archived. */
  archivedAt: number | null;
  archivedBy: string | null;
}

/** A schema name a namespace reaches. */
export interface SchemaSummary {
  namespace: string;
  name: string;
  /** The live version, or null when only a draft exists. */
  liveVersion: number | null;
  hasDraft: boolean;
}

/** What a publish did. */
export interface PublishResult {
  namespace: string;
  name: string;
  /** The live version after the call. */
  version: number;
  /** False when the draft matched the live version and nothing was minted. */
  published: boolean;
}

export type EventKind = 'create' | 'update' | 'delete' | 'operation' | 'publish' | 'define';

/** Why the runner's work wrote an event: a reaction to an event, or a schedule. */
export interface EventCause {
  behavior: string;
  event?: number;
  schedule?: string;
  depth: number;
}

/** One entry of the event log (runtime/engine/README.md, "The event log"). */
export interface EngineEvent {
  /** The global cursor: every later event has a higher one. */
  cursor: number;
  kind: EventKind;
  namespace: string;
  schema: string;
  /** Null for a publish and a define. */
  instanceId: string | null;
  /** Null for a publish and a define. */
  seq: number | null;
  /** Null for a define. */
  version: number | null;
  actor: string;
  /** The deployable of the service whose call made the change (D37). */
  service?: string;
  at: number;
  /**
   * The instance for a create, `{ data, behaviors }`; a merge patch of that
   * for an update; an OperationChange; null for a delete; the document for
   * a publish; `{ hash }` for a define.
   */
  change: unknown;
  cause?: EventCause;
  /**
   * The JSON pointers into change of the members the log keeps in the
   * value store: each holds a ref, `{ "$value": <hash>, "bytes": <n> }`,
   * in place of a value whose JSON is longer than the engine's threshold.
   * Absent when change holds none.
   */
  valueRefs?: string[];
}

/** The change of an operation event. */
export interface OperationChange {
  behavior: string;
  operation: string;
  params: JSONObject;
  /** A merge patch of the instance's `{ data, behaviors }`: a part it does not change is absent. */
  patch: JSONObject;
}

/** A page of the event log: read on from `next`; `more` is false once the page reached the head. */
export interface EventPage {
  events: EngineEvent[];
  next: number;
  more: boolean;
}

/** A schema's describe document (runtime/engine/README.md, "The describe document"). */
export interface DescribeDocument {
  namespace: string;
  name: string;
  schemaNamespace: string;
  version: number;
  hash: string;
  instanceType: string;
  description?: string;
  /** The instance type's display; absent when it declares none. */
  display?: TypeDisplay;
  /** The instance type's own fields, in declaration order. */
  fields: DescribedField[];
  /** The JSON Schema of an instance's data: its own fields. */
  instance: JSONObject;
  /** The JSON Schema of an instance's behaviors: each behavior's fields under its name. */
  instanceBehaviors: JSONObject;
  behaviors: DescribedBehavior[];
  operations: DescribedOperation[];
}

/**
 * How a UI shows a schema's instances, the instance type's @display (D48):
 * titleField and summaryFields name own fields by their keys in an
 * instance's data and behavior fields by their qualified names
 * (`Workflow.status`), and transitions are by the state a transition
 * leaves, then the one it enters.
 */
export interface TypeDisplay {
  noun?: string;
  plural?: string;
  titleField?: string;
  createLabel?: string;
  summaryFields?: string[];
  states?: { [state: string]: DisplayState };
  transitions?: { [from: string]: { [to: string]: string } };
}

/** How a UI shows one Workflow state. */
export interface DisplayState {
  label?: string;
  /** The present-progressive form a UI shows while an instance is in the state. */
  activeForm?: string;
  tone?: DisplayTone;
}

/** What a state means to a reader, which a UI maps onto its own colors. */
export type DisplayTone = 'muted' | 'active' | 'success' | 'warning' | 'danger';

/** One of the instance type's own fields: its key in an instance's data, with its title and icon where declared. */
export interface DescribedField {
  name: string;
  title?: string;
  icon?: string;
}

/** A behavior a schema composes, with its config as the schema holds it. */
export interface DescribedBehavior {
  name: string;
  description?: string;
  config: unknown;
  /** What it does on the type under the config, as its guidance says; absent for a behavior that gives none. */
  summary?: string;
  fields: Array<{ name: string; description?: string }>;
  operations: string[];
  vetoes: Array<{ code: string; description?: string }>;
}

/** One operation of the describe document; the invocation policy sits under the policy's key. */
export interface DescribedOperation {
  name: string;
  behavior?: string;
  scope?: 'instance' | 'schema';
  /** Its tool's title. */
  title: string;
  description: string;
  writes: boolean;
  params: JSONObject;
  result: unknown;
  tool: string;
  /** Its tool's guidance, as the tools document writes it. */
  guidance: ToolGuidance;
  [policyKey: string]: unknown;
}

/** A tool's guidance: when to use it and not, what success is, and each error's fix. */
export interface ToolGuidance {
  useWhen: string;
  doNotUseWhen: string;
  success: string;
  errors: Array<{ code: string; description: string; commonCorrection: string }>;
}

/** The tools document of a namespace, `tools/schema.json`'s shape. */
export interface ToolManifest {
  $schema: string;
  title: string;
  description: string;
  tools: ToolDefinition[];
}

/** A visible tool's MCP record, or a hidden one's; the invocation policy sits under the policy's key. */
export type ToolMCPRecord =
  | { hidden: false; name: string; handle: string; description: string; _meta?: Record<string, unknown>; [policyKey: string]: unknown }
  | { hidden: true; hiddenReason: string };

/** One tool of the tools document. */
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
  parameters: JSONObject;
  returns: { type: string | string[]; description?: string; items?: { type: string | string[]; description?: string } };
}

/** A behavior as `GET /behaviors` lists it. */
export interface BehaviorSummary {
  name: string;
  description?: string;
  requires: string[];
  conflicts: string[];
  fields: string[];
  operations: string[];
}

/** A behavior's declaration, as `GET /behaviors/{name}` returns it. */
export interface BehaviorDocument {
  name: string;
  description?: string;
  configSchema?: JSONSchema;
  createParamsSchema?: JSONSchema;
  preconditionSchema?: JSONSchema;
  requires: string[];
  conflicts: string[];
  fields: Array<{ name: string; description?: string }>;
  operations: BehaviorOperationDocument[];
  vetoes: Array<{ code: string; description?: string }>;
}

/** One operation of a behavior's document; the invocation policy sits under the policy's key. */
export interface BehaviorOperationDocument {
  name: string;
  description?: string;
  scope: 'instance' | 'schema';
  writes: boolean;
  paramsSchema: JSONSchema;
  resultSchema: JSONSchema;
  [policyKey: string]: unknown;
}

/** The parameters of a search across a namespace's schemas. */
export interface SearchParams {
  query?: string;
  syntax?: 'words' | 'fts5';
  vector?: number[];
  model?: string;
  limit?: number;
  cursor?: string;
}

/** One hit of a search across a namespace's schemas. */
export interface SearchHit {
  readonly schema: string;
  readonly id: string;
  /** Its place in the ranking, from 1, across pages. */
  readonly rank: number;
  /** 1 / (60 + its place) over its schema's rankings, which orders the hits. */
  readonly score: number;
  /** The indexed field the snippet is from. */
  readonly field?: string;
  readonly snippet?: ReadonlyArray<{ readonly text: string; readonly match: boolean }>;
  /** With a vector: its place in the full-text ranking, when the query matched it. */
  readonly text?: { readonly rank: number };
  /** With a vector: its place in the vector ranking and its cosine similarity. */
  readonly vector?: { readonly rank: number; readonly similarity: number };
}

/** A page of search hits; `next` is null after the last. */
export interface SearchPage {
  items: SearchHit[];
  next: string | null;
}

/**
 * A write's preconditions, entries by behavior name, such as
 * `{ Lease: { token: 7 } }`: the `Preconditions` header.
 */
export type Preconditions = Readonly<Record<string, Readonly<JSONObject>>>;
