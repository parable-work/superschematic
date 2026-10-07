/*
@superschematic/engine runs a schema with no generated code (D16). It
takes schema-file documents as data, versions them per namespace, and
keeps them, their instances and an event log in one SQLite file. See
runtime/engine/README.md.
*/

export { Engine, openEngine } from './engine.js';
export type { EngineOptions } from './engine.js';

export { SERVICE_SUBJECT_PREFIX, allowAll, servicePrincipal, standsIn } from './access.js';
export type {
  AccessPolicy,
  AccessRequest,
  Action,
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
  SchemaDocumentError,
  UniqueConflictError,
} from './errors.js';
export type { EngineErrorCode, SchemaChange, SchemaIssue, ValidationIssue, Veto } from './errors.js';

export { DEFAULT_NAMESPACE, NAMESPACE_NAME, Namespaces } from './namespaces.js';
export type { NamespaceOptions, NamespaceRecord } from './namespaces.js';

export { SchemaRegistry } from './registry/registry.js';
export type { ComposedBehavior, DefineOptions, SchemaTarget, ValidateOptions } from './registry/registry.js';
export type { PublishResult, SchemaRecord, SchemaSummary } from './registry/catalog.js';
export { SCHEMA_NAME } from './registry/document.js';
export { SchemaValidator } from './registry/validator.js';

export { INSTANCE_ID, InstanceStore } from './instances/store.js';
export type { CreateOptions, DeleteOptions, GetOptions, InstancePage, InstanceRecord, InstanceTarget, ListOptions, UpdateOptions } from './instances/store.js';
export type { InvokeOptions, InvokeSchemaOptions, OperationOutcome } from './instances/store.js';
export { FILTER_SCAN_ROWS, MAX_FILTER_RANGES, MAX_FILTER_VALUES } from './instances/filters.js';

export { EVENT_KINDS, EventLog } from './events/log.js';
export type { DefineChange, EngineEvent, EventCause, EventKind, EventPage, OperationChange, ReadEventsOptions } from './events/log.js';
export type { EventWatcher } from './events/notifier.js';
// Event log retention (runtime/engine/README.md, "Retention").
export { DEFAULT_RETENTION_BATCH_SIZE, DEFAULT_RETENTION_EVERY_MS, MAX_RETENTION_BATCH_SIZE } from './events/retention.js';
export type { RetentionOptions } from './events/retention.js';

// The value store (runtime/engine/README.md, "The value store").
export {
  DEFAULT_VALUE_CACHE_BYTES,
  DEFAULT_VALUE_THRESHOLD,
  MIN_VALUE_THRESHOLD,
  SqliteValueDriver,
  VALUE_HASH,
  VALUE_REF_KEY,
} from './values/store.js';
export type { StoredValue, ValueDriver, ValueOptions, ValueRef } from './values/store.js';
export { EngineValues } from './values/values.js';
export type { ValueTarget } from './values/values.js';
export { canonicalJSON } from './values/canonical.js';

// The runner of reactions and schedules (runtime/engine/README.md, "The runner").
export {
  DEFAULT_BATCH_SIZE,
  DEFAULT_MAX_ATTEMPTS,
  DEFAULT_MAX_DEPTH,
  DEFAULT_RETRY_INITIAL_MS,
  DEFAULT_RETRY_MAX_MS,
  Runner,
} from './runner/runner.js';
export type {
  PruneResult,
  RetentionStatus,
  RunnerOptions,
  RunnerPass,
  RunnerStatus,
  ScheduleStatus,
  SubscriptionKey,
  SubscriptionState,
  SubscriptionStatus,
} from './runner/runner.js';

export { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE } from './paging.js';

export { ENGINE_OWNER, engineMigrations } from './migrations.js';

// Behaviors: the implementation interface and the registry (runtime/engine/README.md, "Behaviors").
export { BehaviorConfigError, RELATION_COLUMNS, defineBehavior } from './behaviors/behavior.js';
export type {
  AnyBehaviorImplementation,
  BehaviorFilter,
  BehaviorGuidance,
  BehaviorImplementation,
  BehaviorMigration,
  BehaviorReactions,
  BehaviorSchedule,
  BehaviorScope,
  ColumnSpec,
  Columns,
  ConfigSchema,
  ConfigSchemas,
  ConfigTarget,
  ConfigType,
  ConfigTypeField,
  ConfigTypes,
  CreateInstanceOptions,
  DescribeTarget,
  DescribedTypeOperation,
  FieldReader,
  FrozenJSON,
  GuardAnswer,
  GuardRequest,
  GuidanceError,
  InstanceChange,
  InstanceContext,
  InstanceSchemaForm,
  InstanceView,
  Instances,
  InstancesInvokeOptions,
  OperationContext,
  OperationGuidance,
  OperationHandler,
  PublishContext,
  ReactionContext,
  ReadOptions,
  Reference,
  ReferenceHears,
  ReferenceContext,
  ReferenceReader,
  References,
  ScheduleContext,
  SchemaContext,
  SchemaOperationHandler,
  Schemas,
  SqlReader,
  SqlWriter,
  StoredInstance,
  StowedObject,
  TableReader,
  TableWriter,
  TypeCheck,
  TypeSchema,
  ValidationContext,
  ValidationRequest,
  ValueReader,
  ValueWriter,
  WorkContext,
  WritableColumns,
} from './behaviors/behavior.js';
export { BEHAVIOR_NAME, BUILTIN_OPERATIONS, OPERATION_SCOPES, VETO_CODE } from './behaviors/declaration.js';
export type {
  BehaviorDeclaration,
  BehaviorFieldDeclaration,
  BehaviorOperationDeclaration,
  BehaviorVetoDeclaration,
  JSONSchema,
  OperationScope,
} from './behaviors/declaration.js';
export { BehaviorRegistry, MIN_SCHEDULE_MS } from './behaviors/registry.js';
export { MAX_BATCH_READ, MAX_CALL_DEPTH } from './behaviors/execution.js';
export { page, pageRequest } from './behaviors/paging.js';
export type { Page } from './behaviors/paging.js';

// The core's behaviors, which every engine registers (runtime/engine/README.md, "Core behaviors").
export {
  ACTOR_NAMESPACE,
  DEFAULT_PRIMARY,
  MAX_ROLLUP_READ,
  ROLE_COLUMNS,
  ROOT_NAMESPACE,
  RRF_K,
  RRF_WINDOW,
  SEARCH_SCHEMAS_PARAMS,
  SIMILAR_TERMS,
  isTerminalState,
  stateOutcome,
  uuidV5,
} from './behaviors/core/index.js';
export type {
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
  LinkRecord,
  LinkSpec,
  LinksConfig,
  ProposalRecord,
  ProposalState,
  ReactionsConfig,
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
  SnippetPart,
  VariantsConfig,
  WorkflowConfig,
  WorkflowOutcome,
  WorkflowStates,
  WorkflowTransition,
} from './behaviors/core/index.js';

// Storage.
export { SQLITE_BUSY, SQLITE_CONSTRAINT_UNIQUE, SqliteError, isBun, openDriver } from './storage/driver.js';
export type { DriverName, Row, RunResult, SqlDriver, SqlValue } from './storage/driver.js';
export { DEFAULT_BUSY_TIMEOUT_MS, Storage } from './storage/storage.js';
export type { StorageOptions } from './storage/storage.js';
export { appliedMigrations, migrate } from './storage/migrations.js';
export type { AppliedMigration, Migration, MigrationResult, MigrationSet } from './storage/migrations.js';

// Tools: the describe and tools documents, the behavior catalog and the calls the MCP tools make (runtime/engine/README.md, "Tools").
export { MCP_HANDLE, MCP_HANDLE_MAX_LENGTH, ToolCatalog, UnknownToolError, kebabCase, snakeCase } from './tools/catalog.js';
export type {
  DescribeDocument,
  DescribedBehavior,
  DescribedOperation,
  JSONSchemaObject,
  ToolDefinition,
  ToolMCPRecord,
  ToolManifest,
  ToolTarget,
} from './tools/catalog.js';
export type { BehaviorDocument, BehaviorOperationDocument, BehaviorSummary } from './tools/behaviors.js';
export type { ToolGuidance } from './tools/guidance.js';
export { BUILTIN_TOOLS, DEFAULT_INVOCATION_POLICY, DEFAULT_TOOL_KEYS, resolveToolOptions } from './tools/options.js';
export type { BuiltinTool, InvocationPolicy, ResolvedToolOptions, ToolKeys, ToolOptions } from './tools/options.js';
export { ANY_JSON_TYPES, typeArguments } from './tools/schema.js';
