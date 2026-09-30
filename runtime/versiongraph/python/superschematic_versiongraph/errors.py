"""The engine's named errors and their stable codes, which every language's
engine shares and the scenario files name (runtime/versiongraph/README.md,
"Engine and storage"). Each is an ``EngineError`` whose ``code`` is the
code; ``error_code`` reads the code of any error, the core's included.
"""

from typing import TYPE_CHECKING, List

if TYPE_CHECKING:
    from .contract import Finding

__all__ = [
    "EngineError",
    "NotFoundError",
    "VersionConflictError",
    "NameTakenError",
    "NoActorError",
    "RefSealedError",
    "NothingToCommitError",
    "WalkCeilingError",
    "SchemaEpochError",
    "EntityNotFoundError",
    "HistoryMissingError",
    "InvalidTreeError",
    "RootMismatchError",
    "MergeIntoItselfError",
    "PrimaryMergeOnlyError",
    "NotTaggedError",
    "NoParentError",
    "error_code",
]


class EngineError(Exception):
    """An error the engine names. ``code`` is stable across languages."""

    code = ""
    default_message = ""

    def __init__(self, message: str = "") -> None:
        super().__init__(message or self.default_message)


class NotFoundError(EngineError):
    """A ref or a commit that does not exist, or a discarded ref."""

    code = "not_found"
    default_message = "not found"


class VersionConflictError(NotFoundError):
    """A fenced write when the row is at another version. It is a
    NotFoundError too, as the Go engine's error of that name wraps
    ErrNotFound."""

    code = "version_conflict"
    default_message = "not found at the expected version"


class NameTakenError(EngineError):
    """A root already has a live ref of that name."""

    code = "name_taken"
    default_message = "the root already has a live ref of that name"


class NoActorError(EngineError):
    """A write with no actor to record."""

    code = "no_actor"
    default_message = "the write has no actor"


class RefSealedError(EngineError):
    """A write through a sealed ref."""

    code = "ref_sealed"
    default_message = "the ref is sealed"


class NothingToCommitError(EngineError):
    """A commit of a ref that composes to the tree of its last commit (or of
    its base)."""

    code = "nothing_to_commit"
    default_message = "nothing to commit"


class WalkCeilingError(EngineError):
    """Reading a commit's tree would walk more parent commits than the
    engine's walk ceiling."""

    code = "walk_ceiling"
    default_message = "the commit walk passed its ceiling"


class SchemaEpochError(EngineError):
    """A commit written at a newer schema epoch than the engine's."""

    code = "schema_epoch"
    default_message = "the commit is from a newer schema epoch"


class EntityNotFoundError(EngineError):
    """An edit that deletes an entity the ref does not hold, or unsets an
    override the ref does not have."""

    code = "entity_not_found"
    default_message = "entity not found on the ref"


class HistoryMissingError(EngineError):
    """A row version a commit names is no longer in its table's history."""

    code = "history_missing"
    default_message = "a row version a commit names is missing from history"


class InvalidTreeError(EngineError):
    """A commit whose tree breaks the graph's rules: ``findings`` are what
    the core's validate found."""

    code = "invalid_tree"
    default_message = "the tree breaks the graph's rules"

    def __init__(self, findings: "List[Finding]") -> None:
        detail = "; ".join(f"{f['kind']}: {f['message']}" for f in findings)
        super().__init__(f"{self.default_message}: {detail}")
        self.findings = findings


class RootMismatchError(EngineError):
    """Two refs, or a ref and a commit, of one operation belong to different
    roots."""

    code = "root_mismatch"
    default_message = "the ref and the commit or ref it names have different roots"


class MergeIntoItselfError(EngineError):
    """A merge whose source is its target."""

    code = "merge_into_itself"
    default_message = "a ref cannot be merged into itself"


class PrimaryMergeOnlyError(EngineError):
    """A save, commit, seal or revert on a primary line, which takes writes
    only from a merge."""

    code = "primary_merge_only"
    default_message = "a primary line takes writes only from a merge"


class NotTaggedError(EngineError):
    """A release of a commit that is not tagged."""

    code = "not_tagged"
    default_message = "the commit is not tagged"


class NoParentError(EngineError):
    """A rebase of a primary line, which has no parent to rebase onto."""

    code = "no_parent"
    default_message = "a primary line has no parent to rebase onto"


def error_code(error: BaseException) -> str:
    """The stable code of an error: an engine error's, the core's for an
    input it refused (``VersionGraphError``), else ""."""
    if isinstance(error, EngineError):
        return error.code
    from . import VersionGraphError

    if isinstance(error, VersionGraphError):
        return error.code
    return ""
