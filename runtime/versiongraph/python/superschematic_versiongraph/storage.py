"""The seam between the version-graph engine and the database that holds a
graph (D19): the operations a storage adapter implements, and the values
they take and return. The Python counterpart of the Go module's package
``storage``.

The engine reaches storage only through this protocol, and asks for one
transaction per operation. Every row an adapter returns, live or from
history, is a canonical row (runtime/versiongraph/README.md, "Canonical
rows") as JSON text, so its numbers keep their digits. Every row the engine
hands an adapter is canonical too, and every id, of a ref, a commit, a root,
a row or an actor, is a UUID in its canonical form (base62).

The module imports nothing but the errors, so an adapter can implement it
without loading the core.
"""

from dataclasses import dataclass
from datetime import timedelta
from typing import Callable, List, Optional, Protocol, Sequence, TypeVar

from .errors import NameTakenError, NotFoundError, VersionConflictError

__all__ = [
    "NotFoundError",
    "VersionConflictError",
    "NameTakenError",
    "Storage",
    "Tx",
    "Ref",
    "NewRef",
    "RefUpdate",
    "RowWrite",
    "Pin",
    "Commit",
    "NewCommit",
    "Patch",
    "SnapshotEntry",
    "CommitNode",
    "Release",
    "ReleaseWrite",
]

T = TypeVar("T")


@dataclass(frozen=True)
class Ref:
    """A ref's graph columns. An absent reference is None."""

    id: str
    root: str
    parent: Optional[str]
    base: Optional[str]
    head: Optional[str]
    name: str
    sealed: bool
    #: True once the ref is soft-deleted.
    discarded: bool
    version: int


@dataclass(frozen=True)
class NewRef:
    """A ref to write: a primary line when parent is None, else a change set
    of parent whose base is base (None for none)."""

    root: str
    parent: Optional[str]
    base: Optional[str]
    name: str
    actor: str


@dataclass(frozen=True)
class RefUpdate:
    """Changes a ref at version: moves the head to head and the base to base
    (each unless it is None), seals the ref when seal is set, and bumps its
    version in any case."""

    id: str
    version: int
    head: Optional[str]
    base: Optional[str]
    seal: bool
    actor: str


@dataclass(frozen=True)
class RowWrite:
    """A row to write onto a ref. The adapter writes ref, root and tombstone
    into the row's ref, root and tombstone columns, and actor and the time
    into its audit columns, whatever the row says; it never writes the row's
    id or version columns. A column the row lacks keeps its stored value on
    an update and its default on an insert; a row without an entity key is a
    new entity, whose key the database generates."""

    ref: str
    root: str
    #: The canonical row, as JSON text.
    row: str
    tombstone: bool
    actor: str


@dataclass(frozen=True)
class Pin:
    """One row version: the row's id and its version."""

    id: str
    version: int


@dataclass
class Commit:
    """A commit."""

    id: str
    root: str
    ref: str
    #: None for a commit with no parent.
    parent: Optional[str]
    #: "" for none.
    message: str
    schema_epoch: int
    content_hash: str
    #: None for an untagged commit.
    sequence: Optional[int]
    #: The commit's time as a canonical dateTime.
    created_at: str
    created_by: str
    #: True when the commit has a snapshot.
    snapshot: bool


@dataclass(frozen=True)
class NewCommit:
    """A commit to write."""

    root: str
    ref: str
    parent: Optional[str]
    message: str
    schema_epoch: int
    content_hash: str
    sequence: Optional[int]
    actor: str


@dataclass(frozen=True)
class Patch:
    """One entity a commit changed, pinned to the row version it sealed.
    operation is ADD, UPDATE or DELETE; commit is set on a patch read back
    and "" on one to write."""

    commit: str
    kind: str
    entity_key: str
    entity_id: str
    entity_version: int
    operation: str


@dataclass(frozen=True)
class SnapshotEntry:
    """One entity of a snapshotted commit's tree, pinned to the row version
    the tree holds."""

    kind: str
    entity_key: str
    entity_id: str
    entity_version: int


@dataclass(frozen=True)
class CommitNode:
    """One commit of the graph as a sweep reads it: its parent (None for
    none), and whether it is tagged and has a snapshot."""

    id: str
    parent: Optional[str]
    tagged: bool
    snapshot: bool


@dataclass(frozen=True)
class Release:
    """A root's release pointer: the commit it names, fenced by its
    version."""

    id: str
    root: str
    commit: str
    version: int


@dataclass(frozen=True)
class ReleaseWrite:
    """Points a root's release at commit, fenced by version (0 for the
    root's first pointer)."""

    root: str
    commit: str
    version: int
    actor: str


class Tx(Protocol):
    """One transaction's view of a graph: the operations the engine builds
    every graph operation from."""

    def create_ref(self, ref: NewRef) -> Ref:
        """Writes a ref and returns it. A root that already has a live ref of
        the name is NameTakenError."""
        ...

    def read_ref(self, id: str) -> Ref:
        """Reads a ref, a discarded one included. A ref that does not exist
        is NotFoundError."""
        ...

    def lock_ref(self, id: str) -> Ref:
        """Reads a ref as read_ref does and locks it until the transaction
        ends."""
        ...

    def update_ref(self, update: RefUpdate) -> Ref:
        """Moves a ref's head or base, seals it, or only bumps its version,
        fenced by the version it expects. Returns the ref as written, or
        raises VersionConflictError."""
        ...

    def discard_ref(self, id: str, version: int, actor: str) -> None:
        """Soft-deletes a live ref at the version it expects, or raises
        VersionConflictError and leaves the transaction usable."""
        ...

    def rows(self, kind: str, ref: str) -> List[str]:
        """Reads every row a ref holds of one kind, tombstones included, in
        no particular order."""
        ...

    def upsert_row(self, kind: str, write: RowWrite) -> str:
        """Writes a row as the ref's row of its entity: an update of the
        ref's row of the entity when it has one, else a new row. Returns the
        row as stored."""
        ...

    def remove_row(self, kind: str, ref: str, entity_key: str, actor: str) -> bool:
        """Hard-deletes the ref's row of an entity, recording actor as the
        delete's actor in history. Reports whether there was one."""
        ...

    def images(self, kind: str, pins: Sequence[Pin]) -> List[str]:
        """Reads the history images of row versions. A pin whose image
        history no longer holds is left out."""
        ...

    def read_commit(self, id: str) -> Commit:
        """Reads a commit, or raises NotFoundError."""
        ...

    def insert_commit(self, commit: NewCommit) -> Commit:
        """Writes a commit and returns it."""
        ...

    def insert_patches(self, commit: str, patches: Sequence[Patch]) -> None:
        """Writes a commit's patches."""
        ...

    def walk(self, commit: str, limit: int) -> List[Commit]:
        """Reads a commit and its parents, nearest first, at most limit of
        them, and stops after the first that has a snapshot. A commit that
        does not exist reads as no commits."""
        ...

    def ref_commits(self, ref: str, head: str, limit: int) -> List[Commit]:
        """Reads head and the parents of it that ref wrote, nearest first, at
        most limit of them."""
        ...

    def patches(self, commits: Sequence[str]) -> List[Patch]:
        """Reads every patch of the commits."""
        ...

    def next_sequence(self, root: str) -> int:
        """Locks the root against other taggers until the transaction ends,
        and returns the root's next published sequence."""
        ...

    def snapshot(self, commit: str) -> List[SnapshotEntry]:
        """Reads a commit's snapshot: its full pin set, in no particular
        order. A commit without one reads as no entries."""
        ...

    def insert_snapshot(self, commit: str, entries: Sequence[SnapshotEntry]) -> None:
        """Writes a commit's snapshot."""
        ...

    def commits(self) -> List[CommitNode]:
        """Reads every commit of the graph, in no particular order, with
        whether each is tagged and snapshotted."""
        ...

    def read_release(self, root: str) -> Release:
        """Reads a root's release pointer, or raises NotFoundError when the
        root has none."""
        ...

    def write_release(self, write: ReleaseWrite) -> Release:
        """Points a root's release at a commit, fenced by the pointer's
        version: version 0 writes the root's first pointer, and any other
        moves the pointer at that version. A pointer at another version, or
        one that already exists when version is 0, is VersionConflictError.
        Returns the pointer as written."""
        ...

    def prune(self, kind: str, retention_days: int, batch_size: int) -> int:
        """Deletes the history images of one kind older than retention_days
        (0 for the kind's declared retention), keeping every image a patch or
        a snapshot pins, at most batch_size of them (0 for no limit). Returns
        how many it deleted."""
        ...

    def discarded_refs(self, grace: timedelta) -> List[Ref]:
        """Reads the refs discarded longer ago than grace."""
        ...

    def idle_drafts(self, idle: timedelta) -> List[Ref]:
        """Reads the live change sets whose last write is older than idle."""
        ...

    def remove_ref_rows(self, kind: str, ref: str, actor: str) -> int:
        """Hard-deletes every row a ref holds of one kind, recording actor as
        each delete's actor in history. Returns how many it deleted."""
        ...

    def sweep_lock(self) -> bool:
        """Takes the graph's sweep lock until the transaction ends. Reports
        False, without waiting, when another transaction holds it."""
        ...


class Storage(Protocol):
    """Holds one version graph. An adapter builds it from the graph's
    descriptor."""

    def transact(self, fn: Callable[[Tx], T]) -> T:
        """Runs fn in one transaction. It commits when fn returns and rolls
        back when it raises, raising fn's error."""
        ...
