//! The Rust engine of the version graph (D17, D19): every graph operation,
//! written once over a storage adapter and the core, which it calls
//! natively.
//!
//! The engine reads and writes canonical rows only: JSON objects keyed by
//! column name, whose values are each column's canonical JSON
//! (`runtime/versiongraph/README.md`). A storage adapter normalizes what its
//! database returns ([`canonical`]); a typed facade, such as the one the
//! Rust types generator writes per graph, turns typed values into canonical
//! rows and back.
//!
//! A primary line (a ref with no parent) takes writes only from
//! [`Engine::merge`]: work happens on a change set, which
//! [`Engine::rebase`] catches up with its parent's head and a merge brings
//! back. A root's release pointer names one tagged commit, which
//! [`Engine::release`] moves and [`Engine::released`] reads. A commit is
//! snapshotted, its full pin set stored, when it is tagged, released, or
//! [`Options::snapshot_every`] commits past the nearest snapshot on its
//! chain, and a read stops at the nearest snapshot. [`Engine::sweep`] and
//! [`Engine::run_sweeper`] are the graph's maintenance.
//!
//! The engine's operations are `async`: each runs in one transaction of its
//! [`Storage`]. [`postgres`] is the Postgres adapter, with a default client
//! over tokio-postgres (the `tokio-postgres` feature, on by default), and
//! [`sqlite`] the SQLite adapter (D32), which keeps every graph in one fixed
//! layout of tables, with a default client over rusqlite (the `rusqlite`
//! feature, off by default).
//!
//! ```no_run
//! # async fn example() -> Result<(), superschematic_versiongraph_engine::Error> {
//! use std::sync::Arc;
//! use superschematic_versiongraph_engine::{postgres, Engine, Error, Options};
//!
//! let descriptor = std::fs::read_to_string("versiongraph/recipe.json").map_err(Error::storage)?;
//! let (client, connection) =
//!     tokio_postgres::connect("postgres://localhost/app", tokio_postgres::NoTls)
//!         .await
//!         .map_err(Error::storage)?;
//! tokio::spawn(connection);
//! let adapter = Arc::new(postgres::Adapter::new(&descriptor, postgres::Options::default())?);
//! let storage = Arc::new(adapter.storage(postgres::TokioPostgres::new(client)));
//! let engine = Engine::new(&descriptor, storage, Options { schema_epoch: 1, ..Options::default() })?;
//! let main = engine.create_primary("Cook", "Bread", "main").await?;
//! # Ok(())
//! # }
//! ```

pub mod canonical;
mod core;
mod engine;
mod error;
pub mod facade;
pub mod postgres;
pub mod sqlite;
pub mod storage;
mod sweep;

pub use crate::core::{Change, Conflict, Finding, Resolution, Take, Tree};
pub use crate::engine::{
    CommitOptions, CommitResult, Edits, Engine, KindEdits, MergeResult, Options, ReleasedResult,
    SaveResult, TreeResult, DEFAULT_SNAPSHOT_EVERY, DEFAULT_WALK_CEILING,
};
pub use crate::error::Error;
pub use crate::storage::{Commit, Ref, Release, Storage};
pub use crate::sweep::{SweepOptions, SweepReport, DEFAULT_DISCARD_GRACE};
