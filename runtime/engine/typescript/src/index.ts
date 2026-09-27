/*
@superschematic/engine runs a schema with no generated code (D16). It
takes schema-file documents as data, versions them per namespace, and
keeps them in one SQLite file. See runtime/engine/README.md.
*/

export { Engine, openEngine } from './engine.js';
export type { EngineOptions } from './engine.js';

export { EngineError, IncompatibleChangeError, SchemaDocumentError } from './errors.js';
export type { EngineErrorCode, SchemaChange, SchemaIssue } from './errors.js';

export { DEFAULT_NAMESPACE, NAMESPACE_NAME, Namespaces } from './namespaces.js';
export type { NamespaceOptions } from './namespaces.js';

export { SchemaRegistry } from './registry/registry.js';
export type {
  DefineOptions,
  PublishResult,
  SchemaRecord,
  SchemaSummary,
  SchemaTarget,
  ValidateOptions,
} from './registry/registry.js';
export { SCHEMA_NAME } from './registry/document.js';
export { SchemaValidator } from './registry/validator.js';
export type { ValidationIssue } from './registry/validator.js';

export { ENGINE_OWNER, engineMigrations } from './migrations.js';

// Storage, for the behaviors that will own tables and columns of their own.
export { SQLITE_BUSY, SqliteError, isBun, openDriver } from './storage/driver.js';
export type { DriverName, Row, RunResult, SqlDriver, SqlValue } from './storage/driver.js';
export { DEFAULT_BUSY_TIMEOUT_MS, Storage } from './storage/storage.js';
export type { StorageOptions } from './storage/storage.js';
export { appliedMigrations, migrate } from './storage/migrations.js';
export type { AppliedMigration, Migration, MigrationResult, MigrationSet } from './storage/migrations.js';
