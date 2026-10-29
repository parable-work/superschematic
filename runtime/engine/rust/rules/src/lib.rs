//! The pure core of the behaviors (D103). A behavior adds fields,
//! sub-tables, operations and rules to a table that stays a table; its
//! rules run here, written once in Rust and embedded by every language
//! through its binding. The core does no I/O: it opens no connection and no
//! file, reads no clock and makes no id. It takes a type described as data
//! ([`TypeSpec`]), a command with its caller, its time and the host's id
//! for the request ([`Command`]), and the answers the host has read for it
//! so far ([`Answers`]), and returns one [`Step`]: the reads it needs, the
//! lock keys it needs before the reads a rule across rows decides on, or
//! its outcome, a [`Refusal`] or a [`ChangeSet`] the host applies in the
//! caller's transaction.
//!
//! A run is a pure function of its inputs, so the host resumes it by
//! running it again with more answers:
//!
//! ```
//! use superschematic_rules::{
//!     Action, Answer, Answers, Caller, Command, Core, Outcome, Step, TypeSpec,
//! };
//!
//! let core = Core::new(); // .with(Workflow), ... the project's behaviors
//! let spec = TypeSpec::new("Order");
//! let command = Command::new(
//!     Caller::new("alice", ["orders"]),
//!     1_000_000,
//!     Action::Delete { key: "o1".into() },
//! );
//! let mut answers = Answers::new();
//! let outcome = loop {
//!     match core.run(&spec, &command, &answers) {
//!         Step::Read(requests) => {
//!             for request in requests {
//!                 // The host reads through the caller's transaction.
//!                 answers.answer(request, Answer::Row(None));
//!             }
//!         }
//!         Step::Lock(keys) => {
//!             for key in keys {
//!                 // The host takes the lock; the core asks again for the
//!                 // reads answered before it.
//!                 answers.hold(key);
//!             }
//!         }
//!         Step::Done(outcome) => break outcome,
//!     }
//! };
//! // A row the host does not have is not found.
//! assert_eq!(outcome.refusal().map(|r| r.status), Some(404));
//! ```
//!
//! Names are logical: a type by its name, a row by its type and key, a
//! behavior's owned fields by use id and field name, its sub-table rows by
//! their row, use id, sub-table name and key. Physical tables and columns
//! are the host's layout and never reach the core; [`Declaration`] states
//! the rule a layout maps owned fields to columns by.
//!
//! A behavior implements [`Behavior`]: a [`Declaration`], a config parsed
//! into a Rust type, and the hooks it needs, which read through a
//! [`View`] or a [`Cx`] and stop the run with a [`Stop`] when they need a
//! read, a lock, or refuse.

mod behavior;
mod changes;
mod command;
mod context;
mod core;
mod erased;
mod patch;
mod permissions;
mod reads;
mod refusal;
mod run;
mod spec;

pub use behavior::{
    Behavior, Change, ConfigError, ConfigTarget, Declaration, GuardRequest, Hook, OperationDecl,
    Stop, ValidationRequest,
};
pub use changes::{
    ChangeSet, DeadlineChange, OutboxEvent, RowRef, RowWrite, SubRowChange, SubRowWrite,
};
pub use command::{Action, Caller, Command};
pub use context::{ConfigCx, Cx, View};
pub use core::{Core, Outcome, Source, Step, MAX_ROUNDS};
pub use patch::merge_patch;
pub use permissions::{covers, has_any_permission, DottedPermissions, PermissionMatcher};
pub use reads::{Answer, Answered, Answers, LockKey, ReadRequest, Row};
pub use refusal::{
    Code, ConfigIssue, ConfigIssueKind, Refusal, SchemaChange, SchemaIssue, ValidationIssue, Veto,
};
pub use run::MAX_CALL_DEPTH;
pub use spec::{Anchor, TypeSpec, UseSpec};
