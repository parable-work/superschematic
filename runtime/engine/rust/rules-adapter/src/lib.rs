//! The behaviors' adapter for a hand-written Rust server (D103): the host
//! loop of the pure core (`superschematic-rules`) inside the caller's own
//! transaction, over a build-time project's tables, onto which the layout
//! document `internal/generator/behaviorlayout` writes ([`Layout`]) maps
//! the core's logical names. It holds no behavior's name: a server builds
//! its [`Core`] with the behaviors its project composes
//! (`superschematic_rules_behaviors::core()`, or its own) and runs each
//! command through the `postgres` module on a tokio-postgres transaction
//! (the `postgres` feature) or the `sqlite` module on a rusqlite
//! connection (the `sqlite` feature), as the HTTP runtime's `src/rules.ts`
//! runs TypeScript's. The core's README
//! (`runtime/engine/rust/rules/README.md`, "The host loop", "The adapter"
//! and "The layout document") is the contract; this crate reaches the
//! core's crates directly, with no C ABI between them.
//!
//! For each command the adapter builds the command with the caller, the
//! host's time and a request id of its own, kept for every run, and a
//! create that names no key gets one (a UUID's base62 wire form for a UUID
//! key, a UUID's text for a string key). It runs the core and answers its
//! reads through the transaction: a row by its key, its own and owned
//! columns converted to the core's JSON (a timestamp to epoch
//! milliseconds, a UUID to base62), a NULL column absent; whether a table
//! has rows; a type's spec from the layout. A soft-deletable table's reads
//! and writes see its live rows alone. A lock is a transaction's advisory
//! lock on Postgres; SQLite takes none, and the unit that writes begins
//! with a check of each read made under one. A refusal is returned whole
//! ([`Error::Refused`]); a change set is applied as one unit in a
//! savepoint, every row fenced on the version it was read at, and a row
//! that moved runs the command again from fresh state, up to
//! [`MAX_ATTEMPTS`] runs, before it is refused as `busy`. A raw SQL write
//! of an owned column is never refused: the core reads what it wrote.
//!
//! ```ignore
//! use superschematic_rules_adapter::{postgres, Caller, Host, Layout};
//!
//! let core = superschematic_rules_behaviors::core();
//! let layout = Layout::from_json(ORDERS_LAYOUT)?;      // the generator's document
//! let tx = client.transaction().await?;
//! let host = Host::new(&core, &layout);
//! let result = postgres::run_operation(
//!     &host, &tx, "Order", &key, "status", "transition",
//!     json!({ "to": "paid" }), Caller::new("sam", ["orders.ship"]),
//! ).await?;
//! tx.commit().await?;
//! ```

#[cfg(any(feature = "postgres", feature = "sqlite", test))]
mod convert;
mod host;
mod layout;
#[cfg(any(feature = "postgres", feature = "sqlite", test))]
mod run;

#[cfg(feature = "postgres")]
pub mod postgres;
#[cfg(feature = "sqlite")]
pub mod sqlite;

#[cfg(test)]
mod tests;

use std::collections::BTreeMap;

use serde_json::{Map, Value};

pub use host::Host;
pub use layout::{Column, Convert, Layout, Live, Operation, OwnedField, Table, Use};
pub use superschematic_rules::{
    Action, Caller, ChangeSet, Code, Command, Core, Refusal, Row, TypeSpec,
};

/// How many times a command whose row moved runs before it is refused as
/// busy (D103).
pub const MAX_ATTEMPTS: usize = 8;

/// How many runs of one attempt the adapter makes before it refuses a run
/// still asking as internal: the core's own bound.
pub const MAX_ROUNDS: usize = superschematic_rules::MAX_ROUNDS;

/// What a command takes beside itself.
#[derive(Clone, Debug, Default)]
pub struct ExecuteOptions {
    /// The own columns of the command's own row as the store binds them, by
    /// column: a server's own encoding of its input (its audit stamps among
    /// them), which a create's insert and an update's set write in place of
    /// the change set's own fields, in the map's order. `None`: the change
    /// set's own fields, converted by the layout.
    pub own_columns: Option<Map<String, Value>>,
    /// The columns a delete of the command's own row sets in place of
    /// deleting it, by column (a soft-deletable table's `deletedAt` and
    /// `deletedBy`), fenced on the version as the delete would be.
    pub soft_delete: Option<Map<String, Value>>,
}

// A value of own_columns or soft_delete binds as its JSON scalar: a string
// as text, a number, a boolean, null; an object or an array as its JSON
// text.

/// What a create through the core takes beside its own fields.
#[derive(Clone, Debug, Default)]
pub struct CreateOptions {
    /// The key to write, in its wire form; `None` for one the adapter makes.
    pub id: Option<String>,
    /// Each use's create parameters, by use id.
    pub params: Map<String, Value>,
    /// The rest of what the command takes; a prepared create applies
    /// nothing and reads none of it.
    pub execute: ExecuteOptions,
}

/// What an applied command came to.
#[derive(Clone, Debug)]
pub struct Executed {
    /// The change set applied.
    pub changes: ChangeSet,
    /// What the command returns: an operation's result, null for the rest.
    pub result: Value,
    /// The key of the command's row: a create's, the adapter's when the
    /// create named none; `None` for a define or publish check.
    pub key: Option<String>,
    /// How many times the command ran: 1 unless a row moved.
    pub attempts: usize,
}

/// A row's owned fields, by use id and then declared name, as the core
/// gives them.
pub type Owned = BTreeMap<String, Map<String, Value>>;

/// Why a command did not apply.
#[derive(Debug)]
pub enum Error {
    /// The core's refusal, or the adapter's own (busy, conflict, internal,
    /// invalid_argument, an own write of an owned field), whole: its code,
    /// status, class, message and details.
    Refused(Box<Refusal>),
    /// A failure of the database.
    Database(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// The refusal, when the command was refused.
    pub fn refusal(&self) -> Option<&Refusal> {
        match self {
            Error::Refused(refusal) => Some(refusal),
            Error::Database(_) => None,
        }
    }

    /// Whether the command was refused as `not_found`: a row, a use or an
    /// operation that does not exist.
    pub fn is_not_found(&self) -> bool {
        self.refusal()
            .is_some_and(|refusal| refusal.code == Code::NotFound)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Refused(refusal) => write!(f, "{}: {}", refusal.code, refusal.message),
            Error::Database(error) => write!(f, "rules: the database failed: {error}"),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Refused(refusal) => Some(refusal.as_ref()),
            Error::Database(error) => Some(error.as_ref()),
        }
    }
}

impl From<Refusal> for Error {
    fn from(refusal: Refusal) -> Self {
        Error::Refused(Box::new(refusal))
    }
}

#[cfg(feature = "postgres")]
impl From<tokio_postgres::Error> for Error {
    fn from(error: tokio_postgres::Error) -> Self {
        Error::Database(Box::new(error))
    }
}

#[cfg(feature = "sqlite")]
impl From<rusqlite::Error> for Error {
    fn from(error: rusqlite::Error) -> Self {
        Error::Database(Box::new(error))
    }
}

/// The refusal of a command whose row kept moving: `busy`, 503, of the
/// class `EngineError` (D58), in the reference host's words, so every
/// adapter refuses it alike: the operation's name (a deadline's, else the
/// command's kind), the type, and the row's key when the command names one.
pub fn busy_refusal(type_name: &str, command: &Command, attempts: usize) -> Refusal {
    let key = command
        .action
        .key()
        .filter(|key| !key.is_empty())
        .map(|key| format!(" {key}"))
        .unwrap_or_default();
    Refusal::new(
        Code::Busy,
        format!(
            "{} of {type_name}{key}: the row kept moving while the command ran; it ran {attempts} times and nothing was written, so it may be tried again",
            command.action.name()
        ),
    )
}
