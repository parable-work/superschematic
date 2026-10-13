//! The engine's storage (D16, D58): SQLite in one process, Postgres in a
//! stack, behind one synchronous API.
//!
//! Statements are SQL text with `?` placeholders ([`Sql`]); a statement
//! the databases word differently is written for each ([`Sql::per`]), and
//! a table is declared as a [`TableSpec`] each database gets in its own
//! types. A behavior's statements pass the checker ([`sql_refusal`])
//! before they run.

pub mod behavior;
mod checker;
mod conn;
mod db;
mod ddl;
mod dialect;
mod engine_migrations;
mod error;
mod ident;
mod migrations;
mod notifier;
#[cfg(feature = "postgres")]
mod postgres;
#[cfg(feature = "sqlite")]
mod sqlite;
mod tokens;
mod value;

pub use checker::{
    sql_refusal, with_relation, SqlMode, POSTGRES_REFUSED_FUNCTIONS, POSTGRES_RESERVED_PREFIX,
    POSTGRES_SCHEMAS, RESERVED_PREFIXES,
};
pub use db::{Db, Lock, Tx, DEFAULT_BUSY_TIMEOUT};
pub use ddl::{is_column_name, ColumnSpec, ColumnType, IndexSpec, TableSpec, MAX_SAFE_INTEGER};
pub use dialect::{Dialect, Sql};
pub use engine_migrations::{engine_migrations, ENGINE_OWNER};
pub use error::{
    DbCode, Error, Result, SQLITE_BUSY, SQLITE_CONSTRAINT_PRIMARYKEY, SQLITE_CONSTRAINT_UNIQUE,
};
pub use ident::{
    catalog_lock_key, identifier, lock_key, migration_lock_key, namespace_lock_key,
    postgres_identifier, quote_identifier, sql_string, POSTGRES_IDENTIFIER_MAX,
};
pub use migrations::{
    applied_migrations, AppliedMigration, Migration, MigrationResult, MigrationSet, MigrationUp,
    OWNER_NAME,
};
pub use notifier::Notifier;
#[cfg(feature = "postgres")]
pub use postgres::{PostgresOptions, DEFAULT_POOL_MAX, DEFAULT_SCHEMA};
#[cfg(feature = "sqlite")]
pub use sqlite::SqliteOptions;
pub use tokens::postgres_placeholders;
pub use value::{Row, RunResult, SqlValue};
