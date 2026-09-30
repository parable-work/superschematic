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
export type { EngineEvent, EventKind, EventPage, OperationChange, ReadEventsOptions } from './events/log.js';
export type { EventWatcher } from './events/notifier.js';

export { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE } from './paging.js';

export { ENGINE_OWNER, engineMigrations } from './migrations.js';

// Behaviors: the implementation interface and the registry (runtime/engine/README.md, "Behaviors").
export { BehaviorConfigError, defineBehavior } from './behaviors/behavior.js';
export type {
  AnyBehaviorImplementation,
  BehaviorImplementation,
  BehaviorMigration,
  BehaviorScope,
  ColumnSpec,
  Columns,
  ConfigTarget,
  FieldReader,
  FrozenJSON,
  GuardRequest,
  InstanceChange,
  InstanceContext,
  InstanceView,
  Instances,
  OperationContext,
  OperationHandler,
  ReadOptions,
  Reference,
  ReferenceContext,
  ReferenceReader,
  References,
  SchemaContext,
  SchemaOperationHandler,
  Schemas,
  SqlReader,
  SqlWriter,
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
export { BehaviorRegistry } from './behaviors/registry.js';
export { MAX_BATCH_READ, MAX_CALL_DEPTH } from './behaviors/execution.js';
export { page, pageRequest } from './behaviors/paging.js';
export type { Page } from './behaviors/paging.js';

// The core's behaviors, which every engine registers (runtime/engine/README.md, "Core behaviors").
export { isTerminalState } from './behaviors/core/index.js';
export type {
  CommentRecord,
  ProposalRecord,
  ProposalState,
  RevisionRecord,
  RevisionsConfig,
  WorkflowConfig,
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
