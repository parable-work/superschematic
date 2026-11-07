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
//! host's time, a request id of its own, kept for every run, and the
//! caller's preconditions, each use's entry by its use id, as the options
//! give them (D16, D104), and a create that names no key gets one (a
//! UUID's base62 wire form for a UUID key, a UUID's text for a string key).
//! It runs the core and answers its reads through the transaction: a row
//! by its key, its own and owned columns converted to the core's JSON (a
//! timestamp to epoch milliseconds, a UUID to base62), a NULL column
//! absent, and the audit columns the layout names (`createdBy`, which a
//! Workflow transition's `by` may name, D104) among its own fields, which
//! no command writes; whether a table has rows; a type's spec from the
//! layout; a row's rows of a use's sub-table, in key order; the rows whose
//! own field holds a key, and those whose use's owned field holds a value
//! (D104), by the layout's column, in key order; and a table's rows (D104),
//! each own field the read names held to its value by equality, in key
//! order, each with the rows of the sub-tables the read names, one read of
//! each sub-table per page. A soft-deletable table's reads and writes see
//! its live rows alone. An operation of the table as a whole (D104), a
//! command without a row, runs through `run_table_operation`: the core
//! reads the table's rows and writes each row it reaches through a call on
//! that row, each fenced on its version, so one that writes no row applies
//! nothing. A lock is a transaction's
//! advisory lock on Postgres; SQLite takes none, and the unit that writes
//! begins with a check of each read made under one. A refusal is returned
//! whole ([`Error::Refused`]); a change set is applied as one unit in a
//! savepoint, every row fenced on the version it was read at, its
//! sub-table rows written, its events written to their row's table's
//! event queue (the layout's `events`, D104) after them, each with the
//! command's time and kept when the row is deleted, and its deadlines
//! armed and cleared as messages of the table's deadline queue; an event
//! of a table with no event queue refuses the command as internal. A row
//! that moved runs the command again from fresh state, up to
//! [`MAX_ATTEMPTS`] runs, before it is refused as `busy`. A due deadline
//! runs through `run_deadline`, which spends its arming (a worker's claim
//! of its message) in the same unit, and releases a fire a guard vetoes to
//! fall due again a minute on; a write of a row its table's change trigger
//! wrote to the change queue, whoever made it, runs through `run_change`
//! as the core's change command, which spends its message in the same unit
//! (D103). A worker that claimed a message of a table's event queue spends
//! it with `spend_event` in the transaction its handler runs in, both or
//! neither committed (D104), and `read_queued_events` reads a table's
//! queued events back. A raw SQL write of an owned column, a sub-table row
//! or a queue's message is never refused: the core reads what was written,
//! and the change queue hears it.
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
pub use layout::{
    Changes, Column, Convert, Deadlines, Events, Layout, Live, Operation, OwnedField, SubTable,
    Table, Use,
};
pub use superschematic_rules::{
    Action, Caller, ChangeSet, Code, Command, Core, Refusal, Row, SubRow, TypeSpec,
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
    /// The caller's preconditions (D16, D104): each use's entry by its use
    /// id, as the core's command carries them (`{"Lease": {"token": 3}}`),
    /// set on the command over any entry of the same use it carries. The
    /// core checks each against its use's behavior before it reads the row
    /// and hands it to that use's guard; an update, a delete and an
    /// operation present them, and the core refuses any other command that
    /// does (`PreconditionsError`), so none is ignored. Empty for none.
    pub preconditions: Map<String, Value>,
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
    /// nothing and reads none of it but its preconditions, which the core
    /// refuses on a create.
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

/// A due deadline a host runs (D103): its row's key in the core's form (a
/// UUID's base62 wire form), the use, the deadline's name, the time it was
/// armed to fall due at, in epoch milliseconds, and its payload: what its
/// message in the table's deadline queue holds.
#[derive(Clone, Debug, PartialEq)]
pub struct Deadline {
    pub key: String,
    pub use_id: String,
    pub name: String,
    pub at: i64,
    pub payload: Value,
}

/// A worker's claim of a deadline's message: the message's id and the
/// claim's token, as the queue gave them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Claim {
    pub id: String,
    pub token: String,
}

/// What running a due deadline came to (D103).
#[derive(Debug)]
pub enum DeadlineRun {
    /// The deadline ran, its change set applied with its arming spent.
    Ran(Box<Executed>),
    /// Nothing ran: the claim's message was gone before the unit could
    /// spend it, the deadline armed or cleared since the claim, or claimed
    /// again after its lease.
    ClaimLost,
    /// Nothing ran: the row or the use is gone (the core's `not_found`,
    /// carried here), and the arming was spent.
    Gone(Box<Refusal>),
    /// Nothing ran: a guard vetoed the fire (the core's `vetoed`, carried
    /// here), and the claimed message was released, due again in
    /// [`VETOED_RETRY_SECONDS`], counting no attempt. Its sub-table row
    /// stays pending, so the fire comes back for as long as the guard holds
    /// it.
    Vetoed(Box<Refusal>),
}

/// How long a fire a guard vetoed waits before its message falls due again
/// (D103): a minute, the claim's attempt given back, so a guard that holds
/// a fire never sends its message dead.
pub const VETOED_RETRY_SECONDS: i64 = 60;

impl DeadlineRun {
    /// Whether the deadline ran.
    pub fn ran(&self) -> bool {
        matches!(self, DeadlineRun::Ran(_))
    }

    /// Why nothing ran, as every adapter words it: `claim lost`, `gone` or
    /// `vetoed`; empty for a deadline that ran.
    pub fn why(&self) -> &'static str {
        match self {
            DeadlineRun::Ran(_) => "",
            DeadlineRun::ClaimLost => "claim lost",
            DeadlineRun::Gone(_) => "gone",
            DeadlineRun::Vetoed(_) => "vetoed",
        }
    }
}

/// A message of a table's change queue as its trigger wrote it (D103): the
/// row's key in its wire form or its column's (a UUID's base62 or
/// hyphenated text), the write (`insert`, `update` or `delete`), its time
/// in epoch milliseconds by the database's clock, and the row's columns
/// before and after it, each a JSON object by column (on SQLite its text)
/// or null.
#[derive(Clone, Debug, PartialEq)]
pub struct TableChange {
    pub key: String,
    pub write: String,
    pub at: i64,
    pub before: Value,
    pub after: Value,
}

/// What running a change came to (D103).
#[derive(Debug)]
pub enum ChangeRun {
    /// The change ran, its change set applied with its message spent.
    Ran(Box<Executed>),
    /// Nothing ran: the claim's message was gone, or claimed again after
    /// its lease, before the unit could spend it.
    ClaimLost,
}

impl ChangeRun {
    /// Whether the change ran.
    pub fn ran(&self) -> bool {
        matches!(self, ChangeRun::Ran(_))
    }

    /// Why nothing ran, as every adapter words it: `claim lost`; empty for
    /// a change that ran.
    pub fn why(&self) -> &'static str {
        match self {
            ChangeRun::Ran(_) => "",
            ChangeRun::ClaimLost => "claim lost",
        }
    }
}

/// What spending an event's claim came to (D104): its message of the
/// table's event queue deleted by its id and token, or nothing spent
/// because the claim no longer held it.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum EventRun {
    /// The claimed message was spent, in the caller's transaction.
    Spent,
    /// Nothing was spent: the claim's message was gone, or claimed again
    /// after its lease, before the statement could spend it.
    ClaimLost,
}

impl EventRun {
    /// Whether the claimed message was spent, so its handler runs.
    pub fn ran(&self) -> bool {
        matches!(self, EventRun::Spent)
    }

    /// Why nothing was spent, as every adapter words it: `claim lost`;
    /// empty for a message that was spent.
    pub fn why(&self) -> &'static str {
        match self {
            EventRun::Spent => "",
            EventRun::ClaimLost => "claim lost",
        }
    }
}

/// A message of a table's event queue as an adapter reads it back (D104):
/// the row's key in the core's form (a UUID's base62 wire form), the use
/// that emitted it, its kind, the time of the command that emitted it, in
/// epoch milliseconds, and its payload, parsed (null for none).
#[derive(Clone, Debug, PartialEq)]
pub struct QueuedEvent {
    pub key: String,
    pub use_id: String,
    pub kind: String,
    pub at: i64,
    pub payload: Value,
}

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
