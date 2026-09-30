"""What every generated Python facade of a version graph shares (D19): the
typed operations over the engine and the Postgres adapter, and the
conversions between typed values and canonical rows. pygen writes one
subclass per graph beside the Python types (versiongraph_<name>.py): its
descriptor, its kinds, the columns each typed value's fields map to, and the
tree and edits types.

A typed value becomes a canonical row through the schema runtime's JSON
(``model_dump(mode="json", by_alias=True)`` of a generated pydantic model,
or a mapping keyed by JSON field name) and the canonical rules; a canonical
row becomes a typed value through the type's ``model_validate_json``, so an
instant comes back as a datetime. A value read back has its canonical form:
a UUID in base62, an instant in UTC, a time of day as HH:MM:SS, a duration
in the scalar core's form. A to-one relation is written as its target's key
and read back empty, since the row does not hold its target.
"""

import copy
import json
import threading
from dataclasses import dataclass
from datetime import timedelta
from typing import Any, Callable, Dict, Generic, List, Mapping, Optional, Sequence, Tuple, TypeVar

from .canonical import canonical_row
from .contract import Finding
from .engine import (
    DEFAULT_DISCARD_GRACE,
    DEFAULT_SNAPSHOT_EVERY,
    DEFAULT_WALK_CEILING,
    Change,
    CommitOptions,
    CommitResult,
    Conflict,
    Engine,
    KindEdits,
    MergeResult,
    Resolution,
    SweepOptions,
    SweepReport,
    TreeResult,
)
from . import errors as _errors
from .errors import *  # noqa: F401,F403
from .exactjson import dumps, loads
from .postgres import Client, PostgresAdapter, psycopg_client
from .storage import Commit, Ref, Release

__all__ = [
    "Column",
    "FacadeKind",
    "FacadeGraph",
    "TypedKindEdits",
    "TypedSaveResult",
    "TypedReleased",
    "VersionGraphFacade",
    "Change",
    "Client",
    "Commit",
    "CommitOptions",
    "CommitResult",
    "Conflict",
    "Finding",
    "MergeResult",
    "Ref",
    "Release",
    "Resolution",
    "SweepOptions",
    "SweepReport",
    "DEFAULT_DISCARD_GRACE",
    "DEFAULT_SNAPSHOT_EVERY",
    "DEFAULT_WALK_CEILING",
    "psycopg_client",
]
__all__ += _errors.__all__

T = TypeVar("T")
TreeT = TypeVar("TreeT")
EditsT = TypeVar("EditsT")
FacadeT = TypeVar("FacadeT", bound="VersionGraphFacade[Any, Any]")


@dataclass(frozen=True)
class Column:
    """One column of a kind's canonical rows and the typed field that holds
    it, by the field's JSON name."""

    column: str
    field: str
    #: For a to-one relation, the target's key field: the typed field holds
    #: the target, and the column its key. A read leaves the field empty.
    relation_key: str = ""
    #: True for a column a typed upsert writes: every column but the row id,
    #: the version, the ref, the root, the tombstone and the audit columns,
    #: which the engine and its adapter write.
    write: bool = False


@dataclass(frozen=True)
class FacadeKind:
    """How a facade reads and writes one member kind."""

    #: The kind's name in the descriptor.
    kind: str
    #: The attribute of the graph's tree and edits that holds the kind.
    member: str
    #: Every column of the kind's rows, with the field it holds.
    columns: Tuple[Column, ...]
    #: The schema runtime's JSON, as text, to a typed value: a generated
    #: model's model_validate_json.
    parse: Callable[[str], Any]


@dataclass(frozen=True)
class FacadeGraph:
    """A generated facade's graph: its descriptor, schema epoch, snapshot
    interval, history actor setting, kinds, and the tree type its reads
    return, built with a list per kind member, content_hash and findings."""

    descriptor: str
    schema_epoch: int
    snapshot_every: int
    history_actor_setting: str
    kinds: Tuple[FacadeKind, ...]
    tree: Callable[..., Any]


@dataclass
class TypedKindEdits(Generic[T]):
    """One kind's edits of a ref, applied by a graph's save: every kind's
    upserts, then its deletes, then its unsets. upsert writes each value as
    the ref's override of its entity, found by its entity key; a value
    without one is a new entity, whose key the database generates. delete
    writes a row that deletes each entity on the ref. unset removes the
    ref's own row of each entity, so the ref reads the entity through its
    base again."""

    upsert: Sequence[T] = ()
    delete: Sequence[str] = ()
    unset: Sequence[str] = ()


@dataclass
class TypedSaveResult(Generic[TreeT]):
    """A saved ref at its new version and the rows save upserted, in edit
    order per kind."""

    ref: Ref
    saved: TreeT


@dataclass
class TypedReleased(Generic[TreeT]):
    """A root's release pointer and the tree of the commit it names."""

    release: Release
    tree: TreeT


class VersionGraphFacade(Generic[TreeT, EditsT]):
    """A version graph's typed operations over the engine and the Postgres
    adapter: refs that hold sparse override rows of each member kind,
    commits that pin the exact row versions a ref sealed, and merges between
    refs. A primary line takes writes only from merge; work happens on
    change sets, which rebase catches up with their parent. Each root's
    release pointer names one tagged commit. Every operation runs in one
    transaction of the client; every write but sweep records the facade's
    actor, and every write through a ref takes the ref's expected version
    and fails with VersionConflictError when the ref has moved on."""

    def __init__(self, graph: FacadeGraph, client: Client, *, actor: str = "", walk_ceiling: int = 0) -> None:
        """The graph over client. actor, a UUID, is who the facade's writes
        are recorded as; a write without one fails with NoActorError.
        walk_ceiling is how many commits a read walks before it fails with
        WalkCeilingError; 0 is DEFAULT_WALK_CEILING."""
        self._graph = graph
        self._client = client
        self._actor = actor
        self._by_kind = {k.kind: k for k in graph.kinds}
        self._columns: Dict[str, Dict[str, str]] = {
            k["kind"]: k["columns"] for k in json.loads(graph.descriptor)["kinds"]
        }
        adapter = PostgresAdapter(graph.descriptor, history_actor_setting=graph.history_actor_setting)
        self._engine = Engine(
            graph.descriptor,
            adapter.storage(client),
            schema_epoch=graph.schema_epoch,
            snapshot_every=graph.snapshot_every,
            walk_ceiling=walk_ceiling,
        )

    @property
    def engine(self) -> Engine:
        """The engine the graph runs on."""
        return self._engine

    def with_actor(self: FacadeT, actor: str) -> FacadeT:
        """A copy of this graph whose writes record actor."""
        copied = copy.copy(self)
        copied._actor = actor
        return copied

    def with_walk_ceiling(self: FacadeT, n: int) -> FacadeT:
        """A copy of this graph that reads a commit's tree by walking at most
        n commits, and fails with WalkCeilingError past them."""
        copied = copy.copy(self)
        copied._engine = self._engine.with_walk_ceiling(n)
        return copied

    def create_primary(self, root: str, name: str) -> Ref:
        """Creates a primary line of root: a ref with no parent."""
        return self._engine.create_primary(self._actor, root, name)

    def branch(self, from_ref: str, name: str) -> Ref:
        """Creates a change set of from_ref whose base is from_ref's head."""
        return self._engine.branch(self._actor, from_ref, name)

    def save(self, ref: str, version: int, edits: EditsT) -> TypedSaveResult[TreeT]:
        """Applies edits to a change set at version: upserts, then deletes,
        then unsets, kind by kind. It refuses a sealed ref, and a primary
        line with PrimaryMergeOnlyError."""
        canonical: Dict[str, KindEdits] = {}
        for kind in self._graph.kinds:
            kind_edits = edits.get(kind.member) if isinstance(edits, Mapping) else getattr(edits, kind.member, None)
            if kind_edits is None:
                continue
            upsert = [self._row(kind, value) for value in kind_edits.upsert]
            if upsert or kind_edits.delete or kind_edits.unset:
                canonical[kind.kind] = KindEdits(upsert, list(kind_edits.delete), list(kind_edits.unset))
        saved = self._engine.save(self._actor, ref, version, canonical)
        return TypedSaveResult(saved.ref, self._tree(TreeResult(saved.saved, "", [])))

    def commit(self, ref: str, version: int, options: CommitOptions = CommitOptions()) -> CommitResult:
        """Composes a change set at version, diffs it against its last commit
        (or its base), writes a commit with a patch per changed entity and
        moves the ref's head. It raises NothingToCommitError when nothing
        changed, InvalidTreeError when the composed tree breaks the graph's
        rules, and PrimaryMergeOnlyError for a primary line."""
        return self._engine.commit(self._actor, ref, version, options)

    def seal(self, ref: str, version: int) -> CommitResult:
        """Commits a change set at version when it has changes, and seals it:
        the ref then refuses writes."""
        return self._engine.seal(self._actor, ref, version)

    def merge(
        self,
        source: str,
        target: str,
        target_version: int,
        resolutions: Sequence[Resolution] = (),
        options: CommitOptions = CommitOptions(),
    ) -> MergeResult:
        """Merges source's head into target at target_version, against
        source's base. Without conflicts it writes the result onto target and
        commits it in the same transaction, with options' message and tag.
        With conflicts left after resolutions it returns them and writes
        nothing. It is the only write a primary line takes."""
        return self._engine.merge(self._actor, source, target, target_version, resolutions, options)

    def rebase(self, draft: str, version: int, resolutions: Sequence[Resolution] = ()) -> MergeResult:
        """Moves a change set at version onto its parent's head: it merges
        the parent's head into the change set against the change set's base,
        with its composed tree, uncommitted work included, as ours. Without
        conflicts it rewrites the change set's rows over the new base, moves
        its base and commits on it after its previous head. With conflicts
        left after resolutions it returns them and writes nothing. A primary
        line is NoParentError."""
        return self._engine.rebase(self._actor, draft, version, resolutions)

    def revert(self, ref: str, version: int, to_commit: str) -> CommitResult:
        """Writes the rows that make a change set at version compose to the
        tree of to_commit, and commits them. History is never rewritten. A
        primary line is PrimaryMergeOnlyError: revert a change set of it and
        merge that."""
        return self._engine.revert(self._actor, ref, version, to_commit)

    def release(self, root: str, commit: str, version: int) -> Release:
        """Points root's release at commit, a tagged commit of root, fenced
        by the pointer's version: 0 for the root's first release. It writes
        no member rows, so a rollback is a release to an earlier tagged
        commit, and the pointer's history is the release log. An untagged
        commit is NotTaggedError."""
        return self._engine.release(self._actor, root, commit, version)

    def released(self, root: str) -> TypedReleased[TreeT]:
        """Reads root's release pointer and the tree of the commit it names:
        the released content. A root never released is NotFoundError."""
        read = self._engine.released(root)
        return TypedReleased(read.release, self._tree(read.tree))

    def materialize(self, commit: str) -> TreeT:
        """Reads a commit's tree: the nearest snapshot on its chain with each
        later commit's patches laid over it, the nearest winning and a DELETE
        removing the entity."""
        return self._tree(self._engine.materialize(commit))

    def compose(self, ref: str) -> TreeT:
        """Reads a ref's tree: its base commit's tree with the ref's own rows
        laid over it."""
        return self._tree(self._engine.compose(ref))

    def diff(self, from_commit: str, to_commit: str) -> List[Change]:
        """Lists the entities the trees of two commits differ on."""
        return self._engine.diff(from_commit, to_commit)

    def history(self, ref: str) -> List[Commit]:
        """Lists the commits a ref wrote, newest first."""
        return self._engine.history(ref)

    def discard(self, ref: str, version: int) -> None:
        """Soft-deletes a ref at version, which frees its name."""
        self._engine.discard(self._actor, ref, version)

    def sweep(self, options: SweepOptions) -> SweepReport:
        """Runs one maintenance pass of the graph in a transaction of its
        own, as options.actor rather than the facade's actor, under the
        graph's sweep lock; while another pass holds the lock it does nothing
        and reports skipped. It discards change sets idle past
        options.abandon_after, deletes the member rows of refs discarded
        longer ago than options.discard_grace, prunes each kind's history
        past its retention while keeping every pinned row version, and
        writes missing snapshots. Nothing calls it unless a service does."""
        return self._engine.sweep(options)

    def run_sweeper(
        self,
        interval: timedelta,
        options: SweepOptions,
        on_pass: Optional[Callable[[Optional[SweepReport], Optional[BaseException]], None]] = None,
        stop: Optional[threading.Event] = None,
    ) -> None:
        """Runs a sweep pass now and then once every interval until stop is
        set. One replica sweeps at a time: a pass that finds the sweep lock
        held is skipped. on_pass, when given, receives each pass's report or
        error; an error does not stop the sweeper."""
        self._engine.run_sweeper(interval, options, on_pass, stop)

    def _row(self, kind: FacadeKind, value: Any) -> str:
        """The canonical row of a typed value of kind, whose every written
        column holds the schema runtime's JSON of its field. The engine
        writes the id, root, ref, tombstone, version and audit columns, and a
        value without an entity key makes a new entity."""
        if hasattr(value, "model_dump"):
            fields = value.model_dump(mode="json", by_alias=True)
        elif isinstance(value, Mapping):
            fields = dict(value)
        else:
            raise TypeError(f"version graph: a {kind.kind} upsert is a model or a mapping, not {type(value).__name__}")
        row: Dict[str, Any] = {}
        for c in kind.columns:
            if not c.write:
                continue
            held = fields.get(c.field)
            if c.relation_key and isinstance(held, Mapping):
                held = held.get(c.relation_key)
            # An unset field is null: an optional field left out, or an
            # entity key left out, which makes a new entity.
            row[c.column] = held
        return canonical_row(self._columns[kind.kind], json.dumps(row, allow_nan=False))

    def _tree(self, result: TreeResult) -> TreeT:
        """Types a tree of canonical rows the engine returned."""
        members: Dict[str, List[Any]] = {k.member: [] for k in self._graph.kinds}
        for kind_name, rows in result.tree.items():
            kind = self._by_kind.get(kind_name)
            if kind is None:
                raise ValueError(f"version graph: the engine returned rows of an unknown kind {kind_name}")
            members[kind.member] = [self._value(kind, row) for row in rows]
        return self._graph.tree(content_hash=result.content_hash, findings=result.findings, **members)  # type: ignore[no-any-return]

    def _value(self, kind: FacadeKind, row: str) -> Any:
        """The typed value of a canonical row of kind: each column the row
        has, but a relation's, into its field."""
        columns = loads(row)
        fields = {c.field: columns[c.column] for c in kind.columns if not c.relation_key and c.column in columns}
        return kind.parse(dumps(fields))
