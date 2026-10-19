//! The engine's storage (D16, D58): SQLite in one process, Postgres in a
//! stack, behind one synchronous API, a port of the TypeScript engine's
//! `src/storage/`, `src/migrations.ts`, `src/behaviors/storage.ts` and
//! `src/behaviors/sql.ts`.
//!
//! - [`Db`] is a SQLite file ([`Db::open_sqlite`], feature `sqlite`) or a
//!   schema of a Postgres database ([`Db::open_postgres`], feature
//!   `postgres`); both features are on by default, since the bindings every
//!   server links open both, and a build that needs one names it.
//! - [`Db::write`] runs a function in a transaction ([`Tx`]) that excludes
//!   the writers its [`Lock`] names; a nested call runs in
//!   [`Tx::savepoint`]. A failure ends the transaction until a savepoint
//!   around it rolls back, on both databases, so a failure the caller
//!   handles runs in [`Tx::try_run`] and its kin. [`Tx::changes`] counts the
//!   rows written and never goes down.
//! - Statements are SQL text with `?` placeholders ([`Sql`]), numbered for
//!   Postgres ([`postgres_placeholders`]); a statement the databases word
//!   differently is written for each ([`Sql::per`]). Values are SQLite's
//!   five storage classes ([`SqlValue`]) on both, and rows plain data
//!   ([`Row`]).
//! - A table is declared as a [`TableSpec`] each database gets in its own
//!   types. Migrations run through a ledger per owner ([`Db::migrate`]);
//!   [`engine_migrations`] is the engine's own.
//! - A behavior's storage ([`behavior`]) is its prefix, its migrations and
//!   its SQL handle, whose every statement passes the checker
//!   ([`sql_refusal`]).
//! - Full-text search is the engine's own ([`fulltext`]): an index a
//!   behavior's migration creates and its SQL handle matches, FTS5 on
//!   SQLite and a `tsvector` with a GIN index on Postgres.
//! - [`Tx::notify`] wakes waiters on the [`Notifier`] when the write
//!   commits, in every process on Postgres.
//!
//! Nothing here is the engine's logic: the registry, instances, events
//! and behaviors build on it.

pub mod behavior;
mod checker;
mod conn;
mod db;
mod ddl;
mod dialect;
mod engine_migrations;
mod error;
pub mod fulltext;
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
pub use db::{Db, Lease, Lock, Tx, DEFAULT_BUSY_TIMEOUT};
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
    applied_migrations, migrate_in, AppliedMigration, Migration, MigrationResult, MigrationSet,
    MigrationUp, OWNER_NAME,
};
pub use notifier::Notifier;
#[cfg(feature = "postgres")]
pub use postgres::{
    PostgresOptions, DEFAULT_IDLE_IN_TRANSACTION_TIMEOUT, DEFAULT_POOL_MAX, DEFAULT_SCHEMA,
};
#[cfg(feature = "sqlite")]
pub use sqlite::SqliteOptions;
pub use tokens::postgres_placeholders;
pub use value::{Row, RunResult, SqlValue};
