/*
@superschematic/engine runs a schema with no generated code (D16). It
takes schema-file documents as data, versions them per namespace, and
keeps them, their instances and an event log in one SQLite file. See
runtime/engine/README.md.
*/

export { Engine, openEngine } from './engine.js';
export type { EngineOptions } from './engine.js';

export { allowAll } from './access.js';
export type { AccessPolicy, AccessRequest, Action, Principal } from './access.js';

export {
  BehaviorError,
  BehaviorVetoError,
  CreateParamsError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  SchemaDocumentError,
} from './errors.js';
export type { EngineErrorCode, SchemaChange, SchemaIssue, ValidationIssue } from './errors.js';

export { DEFAULT_NAMESPACE, NAMESPACE_NAME, Namespaces } from './namespaces.js';
export type { NamespaceOptions } from './namespaces.js';

export { SchemaRegistry } from './registry/registry.js';
export type { ComposedBehavior, DefineOptions, SchemaTarget, ValidateOptions } from './registry/registry.js';
export type { PublishResult, SchemaRecord, SchemaSummary } from './registry/catalog.js';
export { SCHEMA_NAME } from './registry/document.js';
export { SchemaValidator } from './registry/validator.js';

export { INSTANCE_ID, InstanceStore } from './instances/store.js';
export type { CreateOptions, DeleteOptions, InstancePage, InstanceRecord, InstanceTarget, ListOptions, UpdateOptions } from './instances/store.js';
export type { InvokeOptions, InvokeSchemaOptions, OperationOutcome } from './instances/store.js';

export { EventLog } from './events/log.js';
export type { EngineEvent, EventCause, EventKind, EventPage, OperationChange, ReadEventsOptions } from './events/log.js';
export type { EventWatcher } from './events/notifier.js';

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
  CreateInstanceOptions,
  FieldReader,
  FrozenJSON,
  GuardRequest,
  InstanceChange,
  InstanceContext,
  InstanceView,
  Instances,
  OperationContext,
  OperationHandler,
  PublishContext,
  ReactionContext,
  ReadOptions,
  Reference,
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
  TableReader,
  TableWriter,
  WorkContext,
  WritableColumns,
} from './behaviors/behavior.js';
export { BEHAVIOR_NAME, BUILTIN_OPERATIONS, OPERATION_SCOPES } from './behaviors/declaration.js';
export type {
  BehaviorDeclaration,
  BehaviorFieldDeclaration,
  BehaviorOperationDeclaration,
  JSONSchema,
  OperationScope,
} from './behaviors/declaration.js';
export { BehaviorRegistry, MIN_SCHEDULE_MS } from './behaviors/registry.js';
export { MAX_BATCH_READ, MAX_CALL_DEPTH } from './behaviors/execution.js';
export { page, pageRequest } from './behaviors/paging.js';
export type { Page } from './behaviors/paging.js';

// The core's behaviors, which every engine registers (runtime/engine/README.md, "Core behaviors").
export { MAX_ROLLUP_READ, isTerminalState, stateOutcome } from './behaviors/core/index.js';
export type {
  BlockerRecord,
  CommentRecord,
  DependenciesConfig,
  DependentRecord,
  LinkRecord,
  LinkSpec,
  LinksConfig,
  ProposalRecord,
  ProposalState,
  ReactionsConfig,
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
  SearchConfig,
  SearchHit,
  SnippetPart,
  WorkflowConfig,
  WorkflowOutcome,
  WorkflowStates,
  WorkflowTransition,
} from './behaviors/core/index.js';

// Storage.
export { SQLITE_BUSY, SqliteError, isBun, openDriver } from './storage/driver.js';
export type { DriverName, Row, RunResult, SqlDriver, SqlValue } from './storage/driver.js';
export { DEFAULT_BUSY_TIMEOUT_MS, Storage } from './storage/storage.js';
export type { StorageOptions } from './storage/storage.js';
export { appliedMigrations, migrate } from './storage/migrations.js';
export type { AppliedMigration, Migration, MigrationResult, MigrationSet } from './storage/migrations.js';

// Tools: the describe and tools documents and the calls the MCP tools make (runtime/engine/README.md, "Tools").
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
export { BUILTIN_TOOLS, DEFAULT_INVOCATION_POLICY, DEFAULT_TOOL_KEYS, resolveToolOptions } from './tools/options.js';
export type { BuiltinTool, InvocationPolicy, ResolvedToolOptions, ToolKeys, ToolOptions } from './tools/options.js';
export { ANY_JSON_TYPES, typeArguments } from './tools/schema.js';
