//! What the storage refuses or a database reports.
//!
//! The variants a caller branches on are the ones the engine maps to its
//! own codes (`runtime/engine/typescript/src/errors.ts`): [`Error::Busy`]
//! is the lock wait the TypeScript engine sees as SQLite's `SQLITE_BUSY`,
//! and [`Error::Unique`] the refusal it reads as `SQLITE_CONSTRAINT_UNIQUE`
//! to raise a unique conflict. Both databases report them alike: a
//! Postgres lock timeout or deadlock is busy, and its unique violation is
//! unique, the index named as SQLite's message names it.

use std::error::Error as StdError;
use std::fmt;

/// The code a database gave a failure.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DbCode {
    /// SQLite's extended result code (5 is `SQLITE_BUSY`, 2067
    /// `SQLITE_CONSTRAINT_UNIQUE`).
    Sqlite(i32),
    /// Postgres's SQLSTATE (`55P03` lock_not_available, `23505`
    /// unique_violation).
    Postgres(String),
}

impl fmt::Display for DbCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            DbCode::Sqlite(code) => write!(f, "SQLite result code {code}"),
            DbCode::Postgres(state) => write!(f, "SQLSTATE {state}"),
        }
    }
}

/// `SQLITE_BUSY`: another connection holds the lock past the busy timeout.
pub const SQLITE_BUSY: i32 = 5;

/// `SQLITE_CONSTRAINT_UNIQUE`: a write would repeat the values of a UNIQUE
/// index.
pub const SQLITE_CONSTRAINT_UNIQUE: i32 = 2067;

/// `SQLITE_CONSTRAINT_PRIMARYKEY`: a write would repeat a primary key,
/// which Postgres reports as a unique violation, and so does the storage.
pub const SQLITE_CONSTRAINT_PRIMARYKEY: i32 = 1555;

/// What the storage refuses or a database reports.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// Another writer holds the lock past the busy timeout: SQLite's
    /// `SQLITE_BUSY`; Postgres's lock timeout, deadlock or serialization
    /// failure; or no pooled connection came free in time (no code).
    Busy {
        code: Option<DbCode>,
        message: String,
    },
    /// A write would repeat the values of a unique index, unique
    /// constraint or primary key. `index` is the index or constraint the
    /// database names: SQLite names an index (`UNIQUE constraint failed:
    /// index 'name'`) but not a column's constraint; Postgres names both.
    Unique {
        index: Option<String>,
        code: DbCode,
        message: String,
    },
    /// Any other failure the database reported.
    Database { code: DbCode, message: String },
    /// The connection to the database failed or closed.
    Connection { message: String },
    /// A behavior's SQL refused before it ran, or a behavior's use of its
    /// storage the engine does not allow: the TypeScript engine's
    /// BehaviorError, whose message reads `behavior <name>: <message>`.
    Behavior { behavior: String, message: String },
    /// A statement after one that failed in the same transaction, before
    /// a savepoint around the failure rolled back. Postgres refuses every
    /// statement of a transaction after an error, and the storage holds
    /// SQLite to the same rule, so a caller handles a failure it expects
    /// with [`crate::Tx::try_run`] and its kin.
    Aborted { cause: String },
    /// The migration ledger refuses: a database ahead of the build, a
    /// migration recorded under another name, a set numbered wrong.
    Migration { message: String },
    /// A value that cannot be bound or read: a parameter Postgres types in
    /// a way the value cannot take, a column type the storage does not
    /// read, a row read as the wrong type.
    Value { message: String },
    /// The storage used in a way it does not allow: options out of range,
    /// a second transaction on one thread, a write in a read.
    Usage { message: String },
    /// An error a caller's code returned inside a migration or a callback
    /// the storage runs (the engine's own, from migration 10 or an access
    /// check).
    External(Box<dyn StdError + Send + Sync>),
}

/// A storage result.
pub type Result<T, E = Error> = std::result::Result<T, E>;

impl Error {
    /// Whether another writer held the lock past the busy timeout.
    pub fn is_busy(&self) -> bool {
        matches!(self, Error::Busy { .. })
    }

    /// Whether a write would repeat a unique index's values.
    pub fn is_unique(&self) -> bool {
        matches!(self, Error::Unique { .. })
    }

    /// The code the database gave the failure, if it gave one.
    pub fn code(&self) -> Option<&DbCode> {
        match self {
            Error::Busy { code, .. } => code.as_ref(),
            Error::Unique { code, .. } | Error::Database { code, .. } => Some(code),
            _ => None,
        }
    }

    /// SQLite's extended result code, when SQLite gave the failure.
    pub fn sqlite_code(&self) -> Option<i32> {
        match self.code()? {
            DbCode::Sqlite(code) => Some(*code),
            DbCode::Postgres(_) => None,
        }
    }

    /// Postgres's SQLSTATE, when Postgres gave the failure.
    pub fn sqlstate(&self) -> Option<&str> {
        match self.code()? {
            DbCode::Postgres(state) => Some(state),
            DbCode::Sqlite(_) => None,
        }
    }

    /// An error of a caller's own, carried through the storage.
    pub fn external(error: impl Into<Box<dyn StdError + Send + Sync>>) -> Self {
        Error::External(error.into())
    }

    pub(crate) fn usage(message: impl Into<String>) -> Self {
        Error::Usage {
            message: message.into(),
        }
    }

    pub(crate) fn behavior(behavior: &str, message: impl Into<String>) -> Self {
        Error::Behavior {
            behavior: behavior.to_owned(),
            message: message.into(),
        }
    }

    pub(crate) fn migration(message: impl Into<String>) -> Self {
        Error::Migration {
            message: message.into(),
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Busy { message, .. }
            | Error::Unique { message, .. }
            | Error::Database { message, .. }
            | Error::Connection { message }
            | Error::Migration { message }
            | Error::Value { message }
            | Error::Usage { message } => f.write_str(message),
            Error::Behavior { behavior, message } => write!(f, "behavior {behavior}: {message}"),
            Error::Aborted { cause } => write!(
                f,
                "the transaction stopped at an earlier failure ({cause}); a statement whose failure the caller handles runs with try_run, try_get or try_all, or in a savepoint that rolls back"
            ),
            Error::External(error) => error.fmt(f),
        }
    }
}

impl StdError for Error {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        match self {
            Error::External(error) => Some(error.as_ref()),
            _ => None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn codes_read_by_database() {
        let busy = Error::Busy {
            code: Some(DbCode::Sqlite(SQLITE_BUSY)),
            message: "database is locked".into(),
        };
        assert!(busy.is_busy());
        assert_eq!(busy.sqlite_code(), Some(5));
        assert_eq!(busy.sqlstate(), None);
        let unique = Error::Unique {
            index: Some("t_a_key".into()),
            code: DbCode::Postgres("23505".into()),
            message: "duplicate key".into(),
        };
        assert!(unique.is_unique());
        assert_eq!(unique.sqlstate(), Some("23505"));
        let refused = Error::Behavior {
            behavior: "test.Sneaky".into(),
            message: "SQL refused".into(),
        };
        assert_eq!(refused.to_string(), "behavior test.Sneaky: SQL refused");
    }
}
