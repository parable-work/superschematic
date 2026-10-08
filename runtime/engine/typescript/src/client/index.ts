/*
@superschematic/engine/client: a typed client for an engine's HTTP API
(D16), its event stream and the controller pattern over it. It imports
nothing of the engine's server side, of Node.js or of the HTTP runtime,
so it runs in a browser and a worker runtime as well as in a server; a
calling service's credential comes from the HTTP runtime's sources, which
fill its ServiceCredential as they fill a generated SDK's. See
runtime/engine/README.md, "The client".
*/

export {
  BehaviorCalls,
  DEFAULT_NAMESPACE,
  EngineClient,
  EventCalls,
  InstanceCalls,
  NamespaceCalls,
  SchemaCalls,
} from './client.js';
export type { CreateOptions, EngineClientOptions, ListOptions, OperationOutcome, ReadEventsOptions, WriteOptions } from './client.js';
export { EngineProblem, EngineTransportError, EngineVeto, isProblem, isVeto } from './errors.js';
export type { ProblemChange, ProblemDocument, ProblemIssue } from './errors.js';
export { Reconciler, memoryCursor, reconcile } from './reconcile.js';
export type { CursorStore, ReconcileOptions } from './reconcile.js';
export {
  DEFAULT_RECONNECT_INITIAL_MS,
  DEFAULT_RECONNECT_MAX_MS,
  EventSubscription,
  READY_EVENT,
  SseParser,
  StreamEnded,
} from './stream.js';
export type { EventFilters, SseMessage, StreamMessage, SubscribeOptions } from './stream.js';
export { DEFAULT_TIMEOUT_MS, SERVICE_AUTHORIZATION_HEADER } from './transport.js';
export type { CallOptions, EndUserAuth, FetchLike, ForwardedUser, ServiceCredential } from './transport.js';
export type {
  BehaviorDocument,
  BehaviorFieldsJSON,
  BehaviorOperationDocument,
  BehaviorSummary,
  DescribeDocument,
  DescribedBehavior,
  DescribedField,
  DescribedOperation,
  DisplayState,
  DisplayTone,
  EngineEvent,
  EventCause,
  EventKind,
  EventPage,
  Instance,
  InstancePage,
  JSONObject,
  JSONSchema,
  NamespaceRecord,
  OperationChange,
  Preconditions,
  PublishResult,
  SchemaSummary,
  SchemaVersion,
  SearchHit,
  SearchPage,
  SearchParams,
  ToolDefinition,
  ToolGuidance,
  ToolMCPRecord,
  ToolManifest,
  TypeDisplay,
} from './types.js';
