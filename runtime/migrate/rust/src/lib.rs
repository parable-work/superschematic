//! Applies a migration plan to a SQLite file, in the process that serves
//! the file (D27 and D116 in `docs/DECISIONS.md`). The compiler writes
//! plans (`superschematic migrate plan`); this crate reads them, checks
//! them and runs their steps, and it never computes one.
//! `runtime/migrate/README.md` is the contract: the plan document, its
//! canonical JSON and hashes, the state tables and what apply, status and
//! adopt do.
//!
//! It is the second runner. The first is the Go module beside it
//! (`runtime/migrate/go`), whose binary a deploy or a job runs. This one a
//! binary links, so an install that is one file migrates its own database
//! files as it starts. Both keep the same two state tables with the same
//! values, so either goes on from a file the other migrated, and the tests
//! hold this crate to the Go runner step for step. It differs in what it
//! leaves out: it runs `sqlite` plans alone, over rusqlite.
//!
//! - [`canonical_json`], [`hash`] and [`plan_hash`]: canonical JSON and the
//!   hashes over it, byte for byte the Go runner's.
//! - [`read_plan`] and [`read_model`]: a plan and a model, checked before
//!   anything runs.
//! - [`Runner`]: `apply`, `status` and `adopt` over one connection.
//! - [`start`]: what a binary that carries its plans does to each file
//!   before it serves a request: take it forward plan by plan, copy it
//!   first when a contract step will run, adopt a file built by hand, and
//!   refuse a file no carried plan starts from.

mod canonical;
mod error;
mod json;
mod plan;
mod runner;
mod start;
mod state;

pub use canonical::{canonical_json, hash, plan_hash};
pub use error::{BoxError, Error, StepError};
pub use json::JsonError;
pub use plan::{read_model, read_plan, Dialect, Hazard, Model, Phase, Plan, Step, PLAN_VERSION};
pub use runner::{Applied, Runner, Status, DEFAULT_BUSY_TIMEOUT, DEFAULT_WAITS};
pub use start::{start, Adoption, AppliedPlan, StartOptions, Started};
pub use state::{LoggedStep, PLAN_PHASE_EXPAND, PLAN_PHASE_EXPANDED};
