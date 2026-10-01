//! The engine's named errors and their stable codes.

use std::fmt;

use crate::canonical;
use crate::core::Finding;

/// What an engine operation, or the storage adapter under it, failed with.
///
/// Each named variant has a stable code that every language's engine shares
/// and the scenario files name ([`Error::code`]). An input the core refuses
/// is [`Error::Core`] and keeps the core's code.
#[derive(Debug)]
#[non_exhaustive]
pub enum Error {
    /// A ref or commit that does not exist, or a discarded ref.
    NotFound,
    /// A write through a ref, or a release pointer, at another version than
    /// the one given. [`Error::is_not_found`] holds for it too, as Go's
    /// `ErrVersionConflict` wraps `ErrNotFound`.
    VersionConflict,
    /// A new ref whose root already has a live ref of that name.
    NameTaken(String),
    /// A write with no actor to record.
    NoActor,
    /// A write through a sealed ref.
    RefSealed,
    /// A commit of a ref that composes to its last commit's tree (or its
    /// base's).
    NothingToCommit,
    /// A commit walk past the engine's ceiling.
    WalkCeiling(String),
    /// A commit from a newer schema epoch than the engine's.
    SchemaEpoch(String),
    /// A delete of an entity the ref does not hold, or an unset of an
    /// override it does not have.
    EntityNotFound(String),
    /// A row version a commit pins that history no longer holds.
    HistoryMissing(String),
    /// A commit whose tree the core's validate finds wrong, with what it
    /// found.
    InvalidTree(Vec<Finding>),
    /// Two refs, or a ref and a commit, of different roots.
    RootMismatch,
    /// A merge whose source is its target.
    MergeIntoItself,
    /// A save, commit, seal or revert on a primary line, which takes writes
    /// only from a merge.
    PrimaryMergeOnly,
    /// A release of a commit that is not tagged.
    NotTagged,
    /// A rebase of a primary line, which has no parent to rebase onto.
    NoParent,
    /// An input the core refused; its code is the core's.
    Core(superschematic_versiongraph::Error),
    /// A value its class's canonical rule refuses.
    Canonical(canonical::Error),
    /// An argument or a stored value the engine cannot read: an unknown
    /// kind, a row without its role columns, an interval that is not
    /// positive.
    Invalid(String),
    /// The storage adapter or its database failed.
    Storage(Box<dyn std::error::Error + Send + Sync>),
}

impl Error {
    /// The stable code of the error, which every language's engine shares:
    /// one of the named errors' codes (`"version_conflict"`,
    /// `"ref_sealed"`, ...), or the core's code for an input it refused
    /// (`"unmatched_resolution"`, ...). `None` for any other error.
    pub fn code(&self) -> Option<&str> {
        Some(match self {
            Error::NotFound => "not_found",
            Error::VersionConflict => "version_conflict",
            Error::NameTaken(_) => "name_taken",
            Error::NoActor => "no_actor",
            Error::RefSealed => "ref_sealed",
            Error::NothingToCommit => "nothing_to_commit",
            Error::WalkCeiling(_) => "walk_ceiling",
            Error::SchemaEpoch(_) => "schema_epoch",
            Error::EntityNotFound(_) => "entity_not_found",
            Error::HistoryMissing(_) => "history_missing",
            Error::InvalidTree(_) => "invalid_tree",
            Error::RootMismatch => "root_mismatch",
            Error::MergeIntoItself => "merge_into_itself",
            Error::PrimaryMergeOnly => "primary_merge_only",
            Error::NotTagged => "not_tagged",
            Error::NoParent => "no_parent",
            Error::Core(error) => error.code,
            Error::Canonical(_) | Error::Invalid(_) | Error::Storage(_) => return None,
        })
    }

    /// Whether the error is [`Error::NotFound`] or [`Error::VersionConflict`]:
    /// the ref or pointer is not there at the version the caller named.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Error::NotFound | Error::VersionConflict)
    }

    /// A storage error from any error value.
    pub fn storage(error: impl Into<Box<dyn std::error::Error + Send + Sync>>) -> Self {
        Error::Storage(error.into())
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::NotFound => f.write_str("not found"),
            Error::VersionConflict => f.write_str("not found at the expected version"),
            Error::NameTaken(name) => {
                write!(f, "the root already has a live ref of that name: {name:?}")
            }
            Error::NoActor => f.write_str("the write has no actor"),
            Error::RefSealed => f.write_str("the ref is sealed"),
            Error::NothingToCommit => f.write_str("nothing to commit"),
            Error::WalkCeiling(detail) => {
                write!(f, "the commit walk passed its ceiling: {detail}")
            }
            Error::SchemaEpoch(detail) => {
                write!(f, "the commit is from a newer schema epoch: {detail}")
            }
            Error::EntityNotFound(detail) => write!(f, "entity not found on the ref: {detail}"),
            Error::HistoryMissing(detail) => write!(
                f,
                "a row version a commit names is missing from history: {detail}"
            ),
            Error::InvalidTree(findings) => {
                f.write_str("the tree breaks the graph's rules: ")?;
                for (i, finding) in findings.iter().enumerate() {
                    if i > 0 {
                        f.write_str("; ")?;
                    }
                    write!(f, "{}: {}", finding.kind, finding.message)?;
                }
                Ok(())
            }
            Error::RootMismatch => {
                f.write_str("the ref and the commit or ref it names have different roots")
            }
            Error::MergeIntoItself => f.write_str("a ref cannot be merged into itself"),
            Error::PrimaryMergeOnly => f.write_str("a primary line takes writes only from a merge"),
            Error::NotTagged => f.write_str("the commit is not tagged"),
            Error::NoParent => f.write_str("a primary line has no parent to rebase onto"),
            Error::Core(error) => write!(f, "versiongraph: {error}"),
            Error::Canonical(error) => error.fmt(f),
            Error::Invalid(message) => write!(f, "engine: {message}"),
            Error::Storage(error) => error.fmt(f),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Core(error) => Some(error),
            Error::Canonical(error) => Some(error),
            Error::Storage(error) => Some(error.as_ref()),
            _ => None,
        }
    }
}

impl From<canonical::Error> for Error {
    fn from(error: canonical::Error) -> Self {
        Error::Canonical(error)
    }
}

impl From<superschematic_versiongraph::Error> for Error {
    fn from(error: superschematic_versiongraph::Error) -> Self {
        Error::Core(error)
    }
}
