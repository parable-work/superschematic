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

export { EngineError, IncompatibleChangeError, InstanceValidationError, SchemaDocumentError } from './errors.js';
export type { EngineErrorCode, SchemaChange, SchemaIssue, ValidationIssue } from './errors.js';

export { DEFAULT_NAMESPACE, NAMESPACE_NAME, Namespaces } from './namespaces.js';
export type { NamespaceOptions } from './namespaces.js';

export { SchemaRegistry } from './registry/registry.js';
export type { DefineOptions, SchemaTarget, ValidateOptions } from './registry/registry.js';
export type { PublishResult, SchemaRecord, SchemaSummary } from './registry/catalog.js';
export { SCHEMA_NAME } from './registry/document.js';
export { SchemaValidator } from './registry/validator.js';

export { INSTANCE_ID, InstanceStore } from './instances/store.js';
export type { CreateOptions, DeleteOptions, InstancePage, InstanceRecord, InstanceTarget, ListOptions, UpdateOptions } from './instances/store.js';

export { EventLog } from './events/log.js';
export type { EngineEvent, EventKind, EventPage, ReadEventsOptions } from './events/log.js';
export type { EventWatcher } from './events/notifier.js';

export { DEFAULT_PAGE_SIZE, MAX_PAGE_SIZE } from './paging.js';

export { ENGINE_OWNER, engineMigrations } from './migrations.js';

// Storage, for the behaviors that will own tables and columns of their own.
export { SQLITE_BUSY, SqliteError, isBun, openDriver } from './storage/driver.js';
export type { DriverName, Row, RunResult, SqlDriver, SqlValue } from './storage/driver.js';
export { DEFAULT_BUSY_TIMEOUT_MS, Storage } from './storage/storage.js';
export type { StorageOptions } from './storage/storage.js';
export { appliedMigrations, migrate } from './storage/migrations.js';
export type { AppliedMigration, Migration, MigrationResult, MigrationSet } from './storage/migrations.js';
