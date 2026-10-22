/*
@superschematic/engine runs a schema with no generated code (D16): it
takes schema-file documents as data, versions them per namespace, and
keeps them, their instances and an event log in a SQLite file or a
Postgres schema. The engine is the Rust library every server links (D58),
which this package loads as its Node-API addon: openEngine opens it on a
SQLite file with every call synchronous, and openEngineAsync with every
call a promise, for a server on Postgres. See runtime/engine/README.md.
*/

// The engine, synchronous, on SQLite.
export { Engine, openEngine } from './native/engine.js';
export type { EngineRoute, EngineStorage, SchemaValidator } from './native/engine.js';
// The engine with every call a promise, for a server on Postgres (or SQLite);
// its options are data.
export { AsyncEngine, openEngineAsync } from './native/async.js';
export type { DeployedSchema, DeployOptions, EngineHealth } from './native/async.js';
export type { EngineOptions, PostgresOptions } from './native/options.js';
export { UnsupportedError } from './native/errors.js';
export type { UnsupportedGap } from './native/errors.js';
export type { SqliteConnection } from './native/sqlite.js';

export { SERVICE_SUBJECT_PREFIX, allowAll, permissionPolicy, servicePrincipal, standsIn } from './access.js';
export type {
  AccessPolicy,
  AccessRequest,
  Action,
  ListingAccessRequest,
  NamespaceAccessRequest,
  NamespaceOperation,
  Principal,
  PrincipalService,
  SchemaAccessRequest,
  SchemaAction,
} from './access.js';

export {
  BehaviorError,
  BehaviorVetoError,
  CreateParamsError,
  CursorExpiredError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  PreconditionsError,
  SQLITE_BUSY,
  SQLITE_CONSTRAINT_UNIQUE,
  SchemaDocumentError,
  SqliteError,
  UniqueConflictError,
  UnknownToolError,
  ValueTooLargeError,
} from './errors.js';
export type { EngineErrorCode, SchemaChange, SchemaIssue, ValidationIssue, Veto } from './errors.js';

export { DEFAULT_NAMESPACE, NAMESPACE_NAME } from './types/namespaces.js';
export type { NamespaceOptions, NamespaceRecord } from './types/namespaces.js';

export { SCHEMA_NAME } from './types/schemas.js';
export type {
  ComposedBehavior,
  DefineOptions,
  PublishResult,
  SchemaCapabilities,
  SchemaFileDocument,
  SchemaRecord,
  SchemaSummary,
  SchemaTarget,
  ValidateOptions,
} from './types/schemas.js';

export { FILTER_SCAN_ROWS, INSTANCE_ID, MAX_FILTER_RANGES, MAX_FILTER_VALUES } from './types/instances.js';
export type {
  CreateOptions,
  DeleteOptions,
  GetOptions,
  InstancePage,
  InstanceRecord,
  InstanceTarget,
  InvokeOptions,
  InvokeSchemaOptions,
  ListOptions,
  OperationOutcome,
  UpdateOptions,
} from './types/instances.js';
// An instance's behavior fields, under each behavior's name, and their qualified names (D16, amended: a behavior's fields sit under its name).
export { BRANCHES_RELEASE, LINKS_TARGETS, REVISIONS_REVISION, WORKFLOW_STATUS, behaviorField, fieldPath } from './types/fields.js';
export type { BehaviorFields, InstanceFields } from './types/fields.js';

export { EVENT_KINDS } from './types/events.js';
export type { DefineChange, EngineEvent, EventCause, EventKind, EventPage, EventWatcher, OperationChange, ReadEventsOptions } from './types/events.js';
// Event log retention (runtime/engine/README.md, "Retention").
export { DEFAULT_RETENTION_BATCH_SIZE, DEFAULT_RETENTION_EVERY_MS, MAX_RETENTION_BATCH_SIZE } from './types/events.js';
export type { RetentionOptions } from './types/events.js';

// The value store (runtime/engine/README.md, "The value store").
export {
  DEFAULT_VALUE_CACHE_BYTES,
  DEFAULT_VALUE_MAX_BYTES,
  DEFAULT_VALUE_THRESHOLD,
  MIN_VALUE_THRESHOLD,
  VALUE_HASH,
  VALUE_REF_KEY,
} from './types/values.js';
export type { StoredValue, ValueOptions, ValueRef, ValueTarget } from './types/values.js';
export { canonicalJSON } from './canonical.js';

// The runner of reactions and schedules (runtime/engine/README.md, "The runner").
export { DEFAULT_BATCH_SIZE, DEFAULT_MAX_ATTEMPTS, DEFAULT_MAX_DEPTH, DEFAULT_RETRY_INITIAL_MS, DEFAULT_RETRY_MAX_MS } from './types/runner.js';
export type {
  PruneResult,
  RetentionNamespaceStatus,
  RetentionStatus,
  RunnerOptions,
  RunnerPass,
  RunnerStatus,
  ScheduleStatus,
  SubscriptionKey,
  SubscriptionState,
  SubscriptionStatus,
} from './types/runner.js';

export { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE } from './types/paging.js';
export type { Page } from './types/paging.js';

// The engine's SQLite file, as engine.storage reaches it.
export { DEFAULT_BUSY_TIMEOUT_MS } from './types/storage.js';
export type { Row, RunResult, SqlValue } from './types/storage.js';

// Behaviors' declarations (runtime/engine/README.md, "Behaviors"); a
// behavior written in TypeScript, for tests, is @superschematic/engine/testing's.
export { BEHAVIOR_NAME, BUILTIN_OPERATIONS, OPERATION_SCOPES, VETO_CODE } from './types/declarations.js';
export type {
  BehaviorDeclaration,
  BehaviorFieldDeclaration,
  BehaviorOperationDeclaration,
  BehaviorVetoDeclaration,
  JSONSchema,
  OperationScope,
} from './types/declarations.js';

// The core's behaviors, which every engine registers (runtime/engine/README.md, "Core behaviors").
export {
  ACTOR_NAMESPACE,
  DEFAULT_PRIMARY,
  MAX_ROLLUP_READ,
  ROLE_COLUMNS,
  ROOT_NAMESPACE,
  RRF_K,
  RRF_WINDOW,
  SIMILAR_TERMS,
  isTerminalState,
  linkMany,
  linkPin,
  stateOutcome,
  uuidV5,
} from './types/core.js';
export type {
  ApprovalsConfig,
  ApprovalsGate,
  BlockerRecord,
  BranchCommit,
  BranchRef,
  BranchRelease,
  BranchesConfig,
  BranchesKind,
  BranchesSweep,
  BranchesUnit,
  CommentRecord,
  ConstantsConfig,
  DependenciesConfig,
  DependentRecord,
  EvidenceRecord,
  LinkPin,
  LinkRecord,
  LinkSpec,
  LinksConfig,
  NumberingConfig,
  NumberingNumber,
  ProposalRecord,
  ProposalState,
  ReactionsChanges,
  ReactionsConfig,
  ReactionsField,
  ReactionsRevisedLink,
  ReactionsRollup,
  ReactionsRule,
  ReactionsTerminal,
  ReactionsThen,
  ReactionsWhen,
  RevisionRecord,
  RevisionsConfig,
  RollupFunction,
  RollupOver,
  RollupSpec,
  RollupsConfig,
  SchemaSearchHit,
  SearchConfig,
  SearchHit,
  SearchVectors,
  SignOffRecord,
  SnippetPart,
  StateRule,
  StateRulesConfig,
  TimersConfig,
  TimersDeadline,
  TimersDeadlineState,
  TimersOutcome,
  TimersStart,
  TimersThen,
  TimersUntil,
  VariantsConfig,
  WorkflowConfig,
  WorkflowOutcome,
  WorkflowStates,
  WorkflowTransition,
} from './types/core.js';

// Tools: the describe and tools documents, the behavior catalog and the calls the MCP tools make (runtime/engine/README.md, "Tools").
export { ANY_JSON_TYPES, MCP_HANDLE, MCP_HANDLE_MAX_LENGTH } from './types/tools.js';
export type {
  BehaviorDocument,
  BehaviorOperationDocument,
  BehaviorSummary,
  DescribeDocument,
  DescribedBehavior,
  DescribedField,
  DescribedOperation,
  DisplayState,
  JSONSchemaObject,
  ToolDefinition,
  ToolFilter,
  ToolGuidance,
  ToolMCPRecord,
  ToolManifest,
  ToolSummary,
  ToolTarget,
  TypeDisplay,
} from './types/tools.js';
export { BUILTIN_TOOLS, DEFAULT_INVOCATION_POLICY, DEFAULT_TOOL_KEYS, resolveToolOptions } from './native/tool-options.js';
export type { BehaviorOperation, BuiltinTool, InvocationPolicy, ResolvedToolOptions, ToolKeys, ToolOptions } from './native/tool-options.js';
