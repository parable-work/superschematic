//! The runner's errors. A refusal's message is the Go runner's, word for
//! word, wherever the Go runner refuses the same thing.

use std::error::Error as StdError;
use std::fmt;
use std::path::PathBuf;

use rusqlite::ErrorCode;

/// An error a step, a hook or a wrapped failure carries as its cause.
pub type BoxError = Box<dyn StdError + Send + Sync + 'static>;

/// What stopped the runner.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// A refusal: the runner checked the plan, the model, the carried chain
    /// or the file's state and ran nothing. It is the Go runner's
    /// `ErrRefused`.
    Refused(String),
    /// A plan or model document that does not read: it is not JSON, or a
    /// member has the wrong type.
    Document(String),
    /// A step that failed. The plan stays in progress, and the next apply
    /// of the same plan resumes at the step.
    Step(StepError),
    /// The database failed outside a step's statements.
    Database {
        /// What the runner was doing.
        doing: String,
        /// SQLite's error.
        source: rusqlite::Error,
    },
    /// A foreign key a step with `foreignKeysOff` left violated: what
    /// `PRAGMA foreign_key_check` returned before the commit.
    ForeignKeys(String),
    /// Another runner took the plan out of progress while this one ran it.
    NotInProgress {
        /// The plan's hash.
        plan: String,
        /// The DB service the plan migrates.
        service: String,
    },
    /// The [`Runner::after_step`](crate::Runner::after_step) hook stopped
    /// the run.
    Stopped(BoxError),
    /// [`start`](crate::start) found a file no carried plan starts from
    /// (D116): the file is a newer build's, or another app's. Nothing was
    /// changed.
    UnknownModel {
        /// The database file.
        file: PathBuf,
        /// The DB service the carried plans migrate.
        service: String,
        /// The hash of the model the file records as applied; empty when it
        /// records none.
        applied: String,
        /// The plan the file records as in progress, when it is none of the
        /// carried plans.
        in_progress: Option<String>,
        /// The hash of the model the carried plans end at.
        expected: String,
    },
    /// [`start`](crate::start) stopped at a step that failed (D116). The
    /// steps before it stay applied, and the next start goes on from it.
    ChainStep {
        /// The database file.
        file: PathBuf,
        /// The hash of the plan the step belongs to.
        plan: String,
        /// The step's 1-based index in the plan.
        index: usize,
        /// The step's operation.
        op: String,
        /// The object the step changes.
        subject: String,
        /// The step's failure.
        source: Box<StepError>,
    },
    /// A file operation of [`start`](crate::start)'s copy failed.
    Io {
        /// The operation, and the file it was on.
        doing: String,
        /// The system's error.
        source: std::io::Error,
    },
}

impl Error {
    /// Reports whether the error is a refusal: the runner checked and ran
    /// nothing. An unknown model is one.
    pub fn is_refusal(&self) -> bool {
        matches!(self, Error::Refused(_) | Error::UnknownModel { .. })
    }

    /// Reports whether the error, or an error it wraps, is SQLite's
    /// `SQLITE_BUSY`: a wait for another connection's lock that timed out.
    /// The runner retries a step that fails with one.
    pub fn is_busy(&self) -> bool {
        busy(self)
    }

    /// The error with `context` before its message, when it is a refusal or
    /// a document that does not read; any other error as it is.
    pub(crate) fn within(self, context: &str) -> Error {
        match self {
            Error::Refused(message) => Error::Refused(format!("{context}: {message}")),
            Error::Document(message) => Error::Document(format!("{context}: {message}")),
            other => other,
        }
    }

    /// A database failure while `doing`.
    pub(crate) fn database(doing: impl Into<String>) -> impl FnOnce(rusqlite::Error) -> Error {
        let doing = doing.into();
        move |source| Error::Database { doing, source }
    }
}

/// Reports whether `err`, or an error it wraps, is SQLite's `SQLITE_BUSY`
/// or one of its extended codes.
fn busy(err: &(dyn StdError + 'static)) -> bool {
    let mut next = Some(err);
    while let Some(err) = next {
        if let Some(sqlite) = err.downcast_ref::<rusqlite::Error>() {
            if sqlite.sqlite_error_code() == Some(ErrorCode::DatabaseBusy) {
                return true;
            }
        }
        next = err.source();
    }
    false
}

/// Builds a refusal from a format string.
macro_rules! refuse {
    ($($arg:tt)*) => {
        $crate::Error::Refused(format!($($arg)*))
    };
}
pub(crate) use refuse;

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Refused(message) | Error::Document(message) => f.write_str(message),
            Error::Step(step) => step.fmt(f),
            Error::Database { doing, source } => write!(f, "{doing}: {source}"),
            Error::ForeignKeys(violations) => write!(f, "foreign_key_check: {violations}"),
            Error::NotInProgress { plan, service } => {
                write!(
                    f,
                    "plan {plan} is no longer in progress for service {service}"
                )
            }
            Error::Stopped(cause) => cause.fmt(f),
            Error::UnknownModel {
                file,
                service,
                applied,
                in_progress,
                expected,
            } => {
                write!(f, "{}: service {service} ", file.display())?;
                if applied.is_empty() {
                    f.write_str("has no applied model")?;
                } else {
                    write!(f, "is at model {applied}")?;
                }
                if let Some(plan) = in_progress {
                    write!(
                        f,
                        " with plan {plan} in progress, which is none of the carried plans"
                    )?;
                }
                write!(
                    f,
                    "; no carried plan starts from it, and the carried plans end at model {expected}: \
                     the file is a newer build's, or another app's. Nothing was changed"
                )
            }
            Error::ChainStep {
                file,
                plan,
                index,
                op,
                subject,
                source,
            } => {
                write!(
                    f,
                    "{}: plan {plan}: step {index} ({op} {subject}) failed: {}",
                    file.display(),
                    source.cause
                )?;
                if let Some(statement) = &source.statement {
                    write!(f, "\nstatement:\n{statement}")?;
                }
                Ok(())
            }
            Error::Io { doing, source } => write!(f, "{doing}: {source}"),
        }
    }
}

impl StdError for Error {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        match self {
            Error::Step(step) => Some(step),
            Error::Database { source, .. } => Some(source),
            Error::Stopped(cause) => Some(cause.as_ref()),
            Error::ChainStep { source, .. } => Some(source),
            Error::Io { source, .. } => Some(source),
            Error::Refused(_)
            | Error::Document(_)
            | Error::ForeignKeys(_)
            | Error::NotInProgress { .. }
            | Error::UnknownModel { .. } => None,
        }
    }
}

/// A step that failed. The plan stays in progress, and the next apply of
/// the same plan resumes at the step.
#[derive(Debug)]
pub struct StepError {
    /// The step's 1-based index in the plan.
    pub index: usize,
    /// The object the step changes.
    pub subject: String,
    /// The statement that failed; `None` when the failure was not a
    /// statement's, such as a foreign key check.
    pub statement: Option<String>,
    /// Why the step failed.
    pub cause: BoxError,
}

impl StepError {
    /// Reports whether the step failed on SQLite's `SQLITE_BUSY`: a wait
    /// for another connection's lock that timed out.
    pub fn is_busy(&self) -> bool {
        busy(self)
    }
}

impl fmt::Display for StepError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "step {} ({}) failed: {}",
            self.index, self.subject, self.cause
        )?;
        if let Some(statement) = &self.statement {
            write!(f, "\nstatement:\n{statement}")?;
        }
        Ok(())
    }
}

impl StdError for StepError {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        Some(self.cause.as_ref())
    }
}

/// A step's cause once the runner has retried it after every lock timeout
/// its schedule allows.
#[derive(Debug)]
pub(crate) struct GaveUp {
    pub(crate) cause: BoxError,
    pub(crate) retries: usize,
}

impl fmt::Display for GaveUp {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}; gave up after {} retries", self.cause, self.retries)
    }
}

impl StdError for GaveUp {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        Some(self.cause.as_ref())
    }
}
