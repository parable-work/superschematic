"""The Python engine of the version graph (D17, D19): every graph operation,
written once over a storage adapter (``storage``) and the core's Python
binding. It ports the Go engine (runtime/versiongraph/go/engine) operation
for operation, with the same rules and the same error codes.

The engine reads and writes canonical rows only: JSON objects keyed by
column name whose values are each column's canonical JSON
(runtime/versiongraph/README.md), carried as JSON text so a number keeps its
digits. An adapter normalizes what its database returns; a typed facade,
such as the one pygen writes per graph, turns typed values into canonical
rows and back. Every id the engine takes or returns is a UUID; it reads the
canonical form (base62) and the hyphenated one, and returns the canonical
form.

The operations are synchronous. Each runs in one transaction of the adapter.
Every write takes an actor, recorded in the audit columns, and every write
through a ref takes the ref's expected version and fails with
VersionConflictError when the ref has moved on.

A primary line (a ref with no parent) takes writes only from merge: work
happens on a change set, which rebase catches up with its parent's head and
merge brings back. A root's release pointer names one tagged commit, which
release moves and released reads. A commit is snapshotted, its full pin set
stored, when it is tagged, released, or snapshot_every commits past the
nearest snapshot on its chain, and materialize stops at the nearest
snapshot. sweep and run_sweeper are the graph's maintenance.
"""

import json
import threading
import time
from dataclasses import dataclass, field, replace
from datetime import timedelta
from typing import Any, Callable, Dict, List, Mapping, Optional, Sequence, Set, Tuple, Union

from . import run as _core_run
from .canonical import CanonicalError, uuid_canonical
from .contract import Finding
from .errors import (
    EntityNotFoundError,
    HistoryMissingError,
    InvalidTreeError,
    MergeIntoItselfError,
    NoActorError,
    NoParentError,
    NothingToCommitError,
    NotTaggedError,
    PrimaryMergeOnlyError,
    RefSealedError,
    RootMismatchError,
    SchemaEpochError,
    WalkCeilingError,
)
from .exactjson import dumps, loads, write_string
from .storage import (
    Commit,
    CommitNode,
    NewCommit,
    NewRef,
    NotFoundError,
    Patch,
    Pin,
    Ref,
    RefUpdate,
    Release,
    ReleaseWrite,
    RowWrite,
    SnapshotEntry,
    Storage,
    Tx,
    VersionConflictError,
)

__all__ = [
    "DEFAULT_WALK_CEILING",
    "DEFAULT_SNAPSHOT_EVERY",
    "DEFAULT_DISCARD_GRACE",
    "Tree",
    "KindEdits",
    "Edits",
    "SaveResult",
    "CommitOptions",
    "CommitResult",
    "Conflict",
    "Resolution",
    "MergeResult",
    "TreeResult",
    "ReleasedResult",
    "Change",
    "SweepOptions",
    "SweepReport",
    "Engine",
]

DEFAULT_WALK_CEILING = 4096
"""How many commits materialize reads, walking a commit's parents, before it
stops with WalkCeilingError."""

DEFAULT_SNAPSHOT_EVERY = 64
"""How many commits past the nearest snapshot on its chain a commit is
snapshotted at, unless the engine's snapshot_every says otherwise."""

DEFAULT_DISCARD_GRACE = timedelta(days=7)
"""How long after a ref is discarded sweep keeps its member rows."""

Tree = Dict[str, List[str]]
"""A tree of canonical rows (JSON text) by kind. A kind with no rows is
absent."""


@dataclass
class KindEdits:
    """One kind's edits of a ref. upsert writes each row (a canonical row as
    JSON text) as the ref's row of its entity, found by its entity key; a row
    without one is a new entity, whose key the database generates. delete
    writes a row that deletes each entity, by entity key, on the ref. unset
    removes the ref's own row of each entity, so the ref reads the entity
    through its base again."""

    upsert: Sequence[str] = ()
    delete: Sequence[str] = ()
    unset: Sequence[str] = ()


Edits = Mapping[str, KindEdits]
"""A save's edits by kind. Save applies every kind's upserts, then every
kind's deletes, then every kind's unsets, each in descriptor order."""


@dataclass
class SaveResult:
    """A saved ref at its new version and the rows save upserted, as stored,
    in edit order per kind."""

    ref: Ref
    saved: Tree


@dataclass(frozen=True)
class CommitOptions:
    """A commit's message and whether it is tagged: a tagged commit takes the
    root's next sequence."""

    message: str = ""
    tag: bool = False


@dataclass
class CommitResult:
    """A ref at its new version and the commit written, None when there was
    nothing to commit."""

    ref: Ref
    commit: Optional[Commit]


@dataclass(frozen=True)
class Conflict:
    """One unit both sides of a merge changed differently, or an edit against
    a delete (path ""). base, ours and theirs are the unit's canonical values
    as JSON text, None where the unit is absent; the authors are each side's
    author column, as JSON text."""

    kind: str
    entity_key: str
    path: str
    base: Optional[str] = None
    ours: Optional[str] = None
    theirs: Optional[str] = None
    ours_author: Optional[str] = None
    theirs_author: Optional[str] = None


@dataclass(frozen=True)
class Resolution:
    """Settles the conflict at path of one entity: take the unit from one
    side ("base", "ours" or "theirs"), or give its value as JSON text
    ("null" is a value). Exactly one of take and value is set."""

    kind: str
    entity_key: str
    path: str
    take: Optional[str] = None
    value: Optional[str] = None


@dataclass
class MergeResult:
    """The target of a merge, or the change set a rebase moved, and the
    commit written. When conflicts is not empty nothing was written and ref
    is the ref as it was."""

    ref: Ref
    commit: Optional[Commit]
    conflicts: List[Conflict]


@dataclass
class TreeResult:
    """A tree in the core's order (rows by their order column, then by entity
    key), its content hash, and, for a composed ref, the problems compose
    found."""

    tree: Tree
    content_hash: str
    findings: List[Finding]


@dataclass
class ReleasedResult:
    """A root's release pointer and the tree of the commit it names."""

    release: Release
    tree: TreeResult


@dataclass(frozen=True)
class Change:
    """One entity two trees differ on. row is the later tree's row (JSON
    text): its tombstone for a DELETE when it has one, else None."""

    kind: str
    entity_key: str
    operation: str
    row: Optional[str] = None


@dataclass(frozen=True)
class SweepOptions:
    """Configure a maintenance pass. actor is who the pass writes as: the
    discards it makes and the deletes of discarded refs' rows record it.
    discard_grace is how long after a ref is discarded its member rows are
    kept (zero is DEFAULT_DISCARD_GRACE); a positive abandon_after discards
    every live change set with no write for that long; prune_batch caps how
    many history images of each kind the pass prunes (0 is no cap)."""

    actor: str
    discard_grace: timedelta = timedelta(0)
    abandon_after: timedelta = timedelta(0)
    prune_batch: int = 0


@dataclass
class SweepReport:
    """What one pass did. Each count is keyed by kind and holds only kinds
    with a nonzero count."""

    #: True when another pass held the graph's sweep lock, and this one did
    #: nothing.
    skipped: bool = False
    #: How many idle change sets the pass discarded.
    abandoned: int = 0
    #: How many discarded refs had member rows the pass deleted.
    collected_refs: int = 0
    #: How many rows of each kind the pass deleted.
    collected_rows: Dict[str, int] = field(default_factory=dict)
    #: How many history images of each kind the pass pruned.
    pruned: Dict[str, int] = field(default_factory=dict)
    #: How many missing snapshots the pass wrote.
    snapshots: int = 0


@dataclass(frozen=True)
class _KindRoles:
    """The columns of a kind's rows the engine reads."""

    name: str
    key: str
    id: str
    tombstone: str
    version: str


@dataclass(frozen=True)
class _RowRoles:
    """What the engine reads of one row."""

    key: str
    id: str
    version: int
    tombstone: bool


# One entity of a tree: its kind and entity key.
_Entity = Tuple[str, str]
# A commit's tree as the row versions it holds, by entity.
_PinSet = Dict[_Entity, SnapshotEntry]
# Rows of a tree by kind, then by entity key.
_Index = Dict[str, Dict[str, str]]


def _id(what: str, value: str) -> str:
    """Normalizes a UUID argument to its canonical form."""
    try:
        return uuid_canonical(value)
    except CanonicalError as error:
        raise CanonicalError("", "uuid", f"engine: {what}: {error.detail}") from None


def _actor_id(actor: str) -> str:
    """Normalizes a write's actor; an empty one is NoActorError."""
    if actor == "":
        raise NoActorError()
    return _id("actor", actor)


class Engine:
    """Runs one graph's operations."""

    def __init__(
        self,
        descriptor: Union[str, Mapping[str, Any]],
        storage: Storage,
        *,
        schema_epoch: int = 0,
        walk_ceiling: int = 0,
        snapshot_every: int = 0,
    ) -> None:
        """The engine of the graph descriptor describes (version 3, as JSON
        text or a mapping), over storage. schema_epoch is the graph's: every
        commit records it, and materialize refuses a commit from a newer one.
        walk_ceiling bounds a commit walk and snapshot_every is the graph's
        snapshot interval (@versionGraph({ snapshotEvery })); either at 0 or
        below is its default. The core checks the descriptor."""
        text = descriptor if isinstance(descriptor, str) else json.dumps(descriptor)
        _core_run("validate", '{"descriptor":' + text + ',"tree":{}}')
        parsed = json.loads(text)
        self._descriptor = text
        self._kinds = [
            _KindRoles(k["kind"], k["key"], k["id"], k["tombstone"], k["version"]) for k in parsed["kinds"]
        ]
        self._by_name = {k.name: k for k in self._kinds}
        self._storage = storage
        self._schema_epoch = schema_epoch
        self._walk_ceiling = walk_ceiling if walk_ceiling > 0 else DEFAULT_WALK_CEILING
        self._snapshot_every = snapshot_every if snapshot_every > 0 else DEFAULT_SNAPSHOT_EVERY

    def _copy(self, storage: Storage, walk_ceiling: int) -> "Engine":
        copied = Engine.__new__(Engine)
        copied.__dict__.update(self.__dict__)
        copied._storage = storage
        copied._walk_ceiling = walk_ceiling
        return copied

    @property
    def storage(self) -> Storage:
        """The storage the engine runs over."""
        return self._storage

    def with_storage(self, storage: Storage) -> "Engine":
        """A copy of this engine over storage."""
        return self._copy(storage, self._walk_ceiling)

    def with_walk_ceiling(self, n: int) -> "Engine":
        """A copy of this engine that walks at most n commits to read a
        commit's tree, and fails with WalkCeilingError past them. n <= 0 is
        DEFAULT_WALK_CEILING."""
        return self._copy(self._storage, n if n > 0 else DEFAULT_WALK_CEILING)

    def _kind(self, name: str) -> _KindRoles:
        k = self._by_name.get(name)
        if k is None:
            raise ValueError(f"engine: unknown kind {write_string(name)}")
        return k

    # -- Operations --------------------------------------------------------

    def create_primary(self, actor: str, root: str, name: str) -> Ref:
        """Creates a primary line of root: a ref with no parent."""
        actor = _actor_id(actor)
        root = _id("id", root)
        return self._storage.transact(lambda tx: tx.create_ref(NewRef(root, None, None, name, actor)))

    def branch(self, actor: str, from_ref: str, name: str) -> Ref:
        """Creates a change set of from_ref whose base is from_ref's head."""
        actor = _actor_id(actor)
        from_ref = _id("id", from_ref)

        def run(tx: Tx) -> Ref:
            source = self._read_ref(tx, from_ref, None, False)
            return tx.create_ref(NewRef(source.root, source.id, source.head, name, actor))

        return self._storage.transact(run)

    def save(self, actor: str, ref: str, version: int, edits: Edits) -> SaveResult:
        """Applies edits to a change set at version. It refuses a sealed ref,
        and a primary line with PrimaryMergeOnlyError."""
        actor = _actor_id(actor)
        ref = _id("id", ref)
        for name in edits:
            self._kind(name)

        def run(tx: Tx) -> SaveResult:
            saved: Tree = {}
            r = self._read_draft(tx, ref, version)
            deletes = False
            for k in self._kinds:
                kind_edits = edits.get(k.name)
                if kind_edits is None:
                    continue
                for row in kind_edits.upsert:
                    saved.setdefault(k.name, []).append(self._write_row(tx, k.name, r, row, False, actor))
                deletes = deletes or len(kind_edits.delete) > 0
            if deletes:
                by_key = self._index(self._compose(tx, r)[0])
                for k in self._kinds:
                    kind_edits = edits.get(k.name)
                    for key in kind_edits.delete if kind_edits is not None else ():
                        self._delete_entity(tx, r, by_key, k.name, _id("id", key), actor)
            for k in self._kinds:
                kind_edits = edits.get(k.name)
                for key in kind_edits.unset if kind_edits is not None else ():
                    entity_key = _id("id", key)
                    if not tx.remove_row(k.name, r.id, entity_key, actor):
                        raise EntityNotFoundError(
                            f"entity not found on the ref: no {k.name} override of {entity_key}"
                        )
            moved = tx.update_ref(RefUpdate(r.id, r.version, None, None, False, actor))
            return SaveResult(moved, saved)

        return self._storage.transact(run)

    def commit(self, actor: str, ref: str, version: int, options: CommitOptions = CommitOptions()) -> CommitResult:
        """Composes a change set at version, diffs it against its last commit
        (or its base), writes a commit with a patch per changed entity and
        moves the ref's head. It raises NothingToCommitError when nothing
        changed, InvalidTreeError when the composed tree breaks the graph's
        rules, and PrimaryMergeOnlyError for a primary line, whose commits
        merge writes."""
        return self._commit_ref(actor, ref, version, options, False, False)

    def seal(self, actor: str, ref: str, version: int) -> CommitResult:
        """Commits a change set at version when it has changes, and seals it:
        the ref then refuses writes. A primary line is
        PrimaryMergeOnlyError."""
        return self._commit_ref(actor, ref, version, CommitOptions(), True, True)

    def _commit_ref(
        self, actor: str, ref: str, version: int, options: CommitOptions, allow_empty: bool, seal: bool
    ) -> CommitResult:
        actor = _actor_id(actor)
        ref = _id("id", ref)

        def run(tx: Tx) -> CommitResult:
            r = self._read_draft(tx, ref, version)
            return self._commit_and_move(tx, r, options, allow_empty, seal, actor)

        return self._storage.transact(run)

    def merge(
        self,
        actor: str,
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
        nothing. Merge is the only write a primary line takes."""
        actor = _actor_id(actor)
        source = _id("id", source)
        target = _id("id", target)

        def run(tx: Tx) -> MergeResult:
            t = self._read_ref(tx, target, target_version, True)
            s = self._read_ref(tx, source, None, False)
            if s.root != t.root:
                raise RootMismatchError()
            if s.id == t.id:
                raise MergeIntoItselfError()
            conflicts = self._merge(tx, s, t, resolutions, actor)
            if conflicts:
                return MergeResult(t, None, conflicts)
            moved = self._commit_and_move(tx, t, options, True, False, actor)
            return MergeResult(moved.ref, moved.commit, [])

        return self._storage.transact(run)

    def rebase(self, actor: str, draft: str, version: int, resolutions: Sequence[Resolution] = ()) -> MergeResult:
        """Moves a change set at version onto its parent's head. It merges
        the parent's head into the change set, with the change set's base as
        the merge base and its composed tree, uncommitted work included, as
        ours. With conflicts left after resolutions it returns them and
        writes nothing. Otherwise it writes the change set's rows so it
        composes to the merged tree over the parent's head, makes that head
        its base, and commits on it with its previous head (or, with none,
        its new base) as the parent, so its history keeps its commits. A
        primary line has no parent to rebase onto: NoParentError."""
        actor = _actor_id(actor)
        draft = _id("id", draft)

        def run(tx: Tx) -> MergeResult:
            d = self._read_ref(tx, draft, version, True)
            if d.parent is None:
                raise NoParentError()
            parent = self._read_ref(tx, d.parent, None, False)
            if parent.head == d.base:
                # Already on the parent's head: nothing to merge.
                moved = tx.update_ref(RefUpdate(d.id, d.version, None, None, False, actor))
                return MergeResult(moved, None, [])
            base = self._materialize(tx, d.base)
            theirs = self._materialize(tx, parent.head)
            ours, _, own = self._compose(tx, d)
            merged, conflicts = self._core_merge(base, ours, theirs, resolutions)
            if conflicts:
                return MergeResult(d, None, conflicts)
            rebased = replace(d, base=parent.head)
            self._overlay(tx, rebased, theirs, merged, ours, own, actor)
            written: Optional[Commit] = None
            try:
                written = self._commit(tx, rebased, CommitOptions(), actor)
            except NothingToCommitError:
                pass
            moved = tx.update_ref(
                RefUpdate(d.id, d.version, written.id if written is not None else None, parent.head, False, actor)
            )
            return MergeResult(moved, written, [])

        return self._storage.transact(run)

    def revert(self, actor: str, ref: str, version: int, to_commit: str) -> CommitResult:
        """Writes the rows that make a change set at version compose to the
        tree of to_commit, and commits them. History is never rewritten. A
        primary line is PrimaryMergeOnlyError: revert a change set of it and
        merge that."""
        actor = _actor_id(actor)
        ref = _id("id", ref)
        to_commit = _id("id", to_commit)

        def run(tx: Tx) -> CommitResult:
            r = self._read_draft(tx, ref, version)
            commit = tx.read_commit(to_commit)
            if commit.root != r.root:
                raise RootMismatchError()
            self._revert(tx, r, self._materialize(tx, to_commit), actor)
            return self._commit_and_move(tx, r, CommitOptions(), True, False, actor)

        return self._storage.transact(run)

    def release(self, actor: str, root: str, commit: str, version: int) -> Release:
        """Points root's release at commit, a tagged commit of root, fenced by
        the pointer's version: 0 for the root's first release. It snapshots
        the commit and writes no member rows, so a rollback is a release to
        an earlier tagged commit, and the pointer's history is the release
        log. An untagged commit is NotTaggedError; another root's is
        RootMismatchError."""
        actor = _actor_id(actor)
        root = _id("id", root)
        commit = _id("id", commit)

        def run(tx: Tx) -> Release:
            c = tx.read_commit(commit)
            if c.root != root:
                raise RootMismatchError()
            if c.sequence is None:
                raise NotTaggedError()
            self._ensure_snapshot(tx, c.id, c.snapshot)
            return tx.write_release(ReleaseWrite(root, c.id, version, actor))

        return self._storage.transact(run)

    def released(self, root: str) -> ReleasedResult:
        """Reads root's release pointer and the tree of the commit it names.
        A root never released is NotFoundError."""
        root = _id("id", root)

        def run(tx: Tx) -> ReleasedResult:
            release = tx.read_release(root)
            tree = self._order(self._materialize(tx, release.commit))
            return ReleasedResult(release, self._tree_result(tree, []))

        return self._storage.transact(run)

    def materialize(self, commit: str) -> TreeResult:
        """Reads a commit's tree: the nearest snapshot on its chain with each
        later commit's patches laid over it, the nearest winning and a DELETE
        removing the entity."""
        commit = _id("id", commit)
        return self._storage.transact(
            lambda tx: self._tree_result(self._order(self._materialize(tx, commit)), [])
        )

    def compose(self, ref: str) -> TreeResult:
        """Reads a ref's tree: its base commit's tree with the ref's own rows
        laid over it."""
        ref = _id("id", ref)

        def run(tx: Tx) -> TreeResult:
            r = self._read_ref(tx, ref, None, False)
            tree, findings, _ = self._compose(tx, r)
            return self._tree_result(tree, findings)

        return self._storage.transact(run)

    def diff(self, from_commit: str, to_commit: str) -> List[Change]:
        """Lists the entities the trees of two commits differ on."""
        from_commit = _id("id", from_commit)
        to_commit = _id("id", to_commit)
        return self._storage.transact(
            lambda tx: self._diff(self._materialize(tx, from_commit), self._materialize(tx, to_commit))
        )

    def history(self, ref: str) -> List[Commit]:
        """Lists the commits a ref wrote, newest first."""
        ref = _id("id", ref)

        def run(tx: Tx) -> List[Commit]:
            r = self._read_ref(tx, ref, None, False)
            if r.head is None:
                return []
            return tx.ref_commits(r.id, r.head, self._walk_ceiling)

        return self._storage.transact(run)

    def discard(self, actor: str, ref: str, version: int) -> None:
        """Soft-deletes a ref at version, which frees its name."""
        actor = _actor_id(actor)
        ref = _id("id", ref)

        def run(tx: Tx) -> None:
            self._read_ref(tx, ref, version, False)
            tx.discard_ref(ref, version, actor)

        self._storage.transact(run)

    def sweep(self, options: SweepOptions) -> SweepReport:
        """Runs one maintenance pass in one transaction, under the graph's
        sweep lock; when another pass holds it, sweep does nothing and
        reports skipped. In order, the pass discards the change sets idle
        past abandon_after, leaving one a write reaches after the pass read
        it; deletes the member rows of refs discarded longer ago than
        discard_grace, keeping their ref rows and commits as the audit trail;
        prunes each kind's history past its declared retention, keeping every
        row version a patch or a snapshot pins; and writes each snapshot the
        graph's rules call for and it lacks. Nothing calls sweep unless a
        service does."""
        actor = _actor_id(options.actor)
        grace = options.discard_grace if options.discard_grace != timedelta(0) else DEFAULT_DISCARD_GRACE

        def run(tx: Tx) -> SweepReport:
            report = SweepReport()
            if not tx.sweep_lock():
                report.skipped = True
                return report
            if options.abandon_after > timedelta(0):
                for ref in tx.idle_drafts(options.abandon_after):
                    # A write that reached the ref after idle_drafts read it
                    # moved its version, so the ref is no longer idle: leave
                    # it.
                    try:
                        tx.discard_ref(ref.id, ref.version, actor)
                    except VersionConflictError:
                        continue
                    report.abandoned += 1
            for ref in tx.discarded_refs(grace):
                rows = 0
                for k in self._kinds:
                    n = tx.remove_ref_rows(k.name, ref.id, actor)
                    if n > 0:
                        report.collected_rows[k.name] = report.collected_rows.get(k.name, 0) + n
                        rows += n
                if rows > 0:
                    report.collected_refs += 1
            for k in self._kinds:
                n = tx.prune(k.name, 0, options.prune_batch)
                if n > 0:
                    report.pruned[k.name] = n
            report.snapshots = self._backfill(tx)
            return report

        return self._storage.transact(run)

    def run_sweeper(
        self,
        interval: timedelta,
        options: SweepOptions,
        on_pass: Optional[Callable[[Optional[SweepReport], Optional[BaseException]], None]] = None,
        stop: Optional[threading.Event] = None,
    ) -> None:
        """Runs a sweep pass now and then once every interval until stop is
        set, and then returns. Each pass takes the graph's sweep lock, so one
        replica sweeps at a time and a pass that finds it held is skipped.
        on_pass, when given, receives each pass's report or error; an error
        does not stop the sweeper. A stop already set runs no pass, and a
        pass under way when stop is set finishes before the sweeper returns.
        Without stop it runs until the process ends."""
        seconds = interval.total_seconds()
        if not seconds > 0:
            raise ValueError("engine: a sweeper's interval must be positive")
        stop = stop if stop is not None else threading.Event()
        deadline = time.monotonic()
        while not stop.is_set():
            deadline += seconds
            try:
                report = self.sweep(options)
            except Exception as error:  # noqa: BLE001 - reported to on_pass, never raised
                if on_pass is not None:
                    on_pass(None, error)
            else:
                if on_pass is not None:
                    on_pass(report, None)
            if stop.wait(max(0.0, deadline - time.monotonic())):
                return
            deadline = max(deadline, time.monotonic())

    # -- Reading refs and rows ---------------------------------------------

    def _read_ref(self, tx: Tx, ref_id: str, expected: Optional[int], write: bool) -> Ref:
        """Reads a ref. With expected set it locks the ref's row and refuses
        a ref at another version, and with write set a sealed ref."""
        ref = tx.lock_ref(ref_id) if expected is not None else tx.read_ref(ref_id)
        if ref.discarded:
            raise NotFoundError()
        if expected is not None and ref.version != expected:
            raise VersionConflictError()
        if write and ref.sealed:
            raise RefSealedError()
        return ref

    def _read_draft(self, tx: Tx, ref_id: str, version: int) -> Ref:
        """Reads a ref to write through at version: a live, unsealed change
        set. A primary line takes writes only from merge."""
        ref = self._read_ref(tx, ref_id, version, True)
        if ref.parent is None:
            raise PrimaryMergeOnlyError()
        return ref

    def _read_row(self, kind: _KindRoles, raw: str) -> _RowRoles:
        columns = loads(raw)
        if not isinstance(columns, dict):
            raise ValueError(f"engine: decode {kind.name} row: not an object")

        def text(column: str) -> str:
            if column not in columns:
                raise ValueError(f"engine: {kind.name} row {column}: unexpected end of JSON input")
            value = columns[column]
            if value is None:
                return ""
            if not isinstance(value, str):
                raise ValueError(f"engine: {kind.name} row {column}: not a string")
            return value

        if kind.version not in columns:
            raise ValueError(f"engine: {kind.name} row {kind.version}: unexpected end of JSON input")
        version_value = columns[kind.version]
        version = 0
        if version_value is not None:
            try:
                version = int(version_value.text)
            except (AttributeError, ValueError):
                raise ValueError(f"engine: {kind.name} row {kind.version}: not an integer") from None
        tombstone_value = columns.get(kind.tombstone)
        tombstone = False
        if tombstone_value is not None:
            if not isinstance(tombstone_value, bool):
                raise ValueError(f"engine: {kind.name} row {kind.tombstone}: not a boolean")
            tombstone = tombstone_value
        return _RowRoles(text(kind.key), text(kind.id), version, tombstone)

    def _index(self, tree: Tree) -> _Index:
        """Maps each kind's rows by entity key."""
        by_key: _Index = {}
        for name, rows in tree.items():
            kind = self._kind(name)
            by_key[name] = {self._read_row(kind, raw).key: raw for raw in rows}
        return by_key

    def _own_rows(self, tx: Tx, ref: str) -> Tree:
        """Reads every row a ref holds, tombstones included."""
        tree: Tree = {}
        for k in self._kinds:
            rows = tx.rows(k.name, ref)
            if rows:
                tree[k.name] = list(rows)
        return tree

    # -- Commits, pins and snapshots ---------------------------------------

    def _resolve(self, tx: Tx, commit: Optional[str]) -> Tuple[_PinSet, int]:
        """Reads the pin set of a commit's tree: it walks the commit's
        parents to the nearest snapshot, takes that snapshot's pins, and lays
        each nearer commit's patches over them, the nearest winning and a
        DELETE removing the entity. It also returns the commit's distance
        from the nearest snapshot on its chain: 0 for a snapshotted commit,
        else how many commits separate them, the snapshot excluded, counting
        a chain with no snapshot from before its first commit. An empty
        commit is the empty set at distance 0."""
        pins: _PinSet = {}
        if not commit:
            return pins, 0
        chain = tx.walk(commit, self._walk_ceiling)
        if not chain:
            raise NotFoundError()
        for c in chain:
            if c.schema_epoch > self._schema_epoch:
                raise SchemaEpochError(
                    f"the commit is from a newer schema epoch: commit {c.id} has epoch "
                    f"{c.schema_epoch}, this graph {self._schema_epoch}"
                )
        last = chain[-1]
        distance = len(chain)
        patched = chain
        if last.snapshot:
            for entry in tx.snapshot(last.id):
                pins[(entry.kind, entry.entity_key)] = entry
            distance = len(chain) - 1
            patched = chain[:-1]
        elif last.parent is not None:
            raise WalkCeilingError(f"the commit walk passed its ceiling: {self._walk_ceiling} commits")
        if not patched:
            return pins, distance
        depth = {c.id: i for i, c in enumerate(patched)}
        nearest: Dict[_Entity, Patch] = {}
        for p in tx.patches([c.id for c in patched]):
            at = (p.kind, p.entity_key)
            seen = nearest.get(at)
            if seen is None or depth[p.commit] < depth[seen.commit]:
                nearest[at] = p
        _apply_patches(pins, nearest)
        return pins, distance

    def _images(self, tx: Tx, pins: _PinSet) -> Tree:
        """Reads the history image of every row version a pin set holds."""
        by_kind: Dict[str, List[Pin]] = {}
        for entry in pins.values():
            by_kind.setdefault(entry.kind, []).append(Pin(entry.entity_id, entry.entity_version))
        tree: Tree = {}
        for k in self._kinds:
            want = by_kind.get(k.name, [])
            if not want:
                continue
            images = tx.images(k.name, want)
            if len(images) != len(want):
                raise HistoryMissingError(
                    "a row version a commit names is missing from history: "
                    f"{len(want) - len(images)} of {len(want)} {k.name} rows"
                )
            tree[k.name] = list(images)
        return tree

    def _materialize(self, tx: Tx, commit: Optional[str]) -> Tree:
        """Reads a commit's tree: the row versions its pin set holds, read
        from history. An empty commit is the empty tree."""
        return self._materialize_pins(tx, commit)[0]

    def _materialize_pins(self, tx: Tx, commit: Optional[str]) -> Tuple[Tree, _PinSet, int]:
        """_materialize, with the commit's pin set and its distance from the
        nearest snapshot (see _resolve)."""
        pins, distance = self._resolve(tx, commit)
        return self._images(tx, pins), pins, distance

    def _ensure_snapshot(self, tx: Tx, commit: str, snapshotted: bool) -> bool:
        """Snapshots a commit that has no snapshot yet. Reports whether it
        wrote one: a commit whose tree is empty has no entries to write."""
        if snapshotted:
            return False
        pins, _ = self._resolve(tx, commit)
        if not pins:
            return False
        tx.insert_snapshot(commit, _pin_entries(pins))
        return True

    # -- The core ------------------------------------------------------------

    def _run(self, operation: str, request: str) -> Dict[str, Any]:
        """Runs one core operation on a JSON request and reads its output
        exactly."""
        output = loads(_core_run(operation, request))  # type: ignore[arg-type]
        assert isinstance(output, dict)
        return output

    def _compose(self, tx: Tx, ref: Ref) -> Tuple[Tree, List[Finding], Tree]:
        """core.compose(materialize(ref.base) or empty, the ref's own rows),
        with the core's findings and the own rows."""
        base = self._materialize(tx, ref.base)
        own = self._own_rows(tx, ref.id)
        output = self._run(
            "compose",
            '{"descriptor":' + self._descriptor + ',"base":' + _tree_json(base) + ',"overlay":' + _tree_json(own) + "}",
        )
        return _decode_tree(output["tree"]), _decode_findings(output["findings"]), own

    def _order(self, tree: Tree) -> Tree:
        """tree in the core's order: rows by their order column, then by
        entity key. A materialized tree comes out of history in no order."""
        output = self._run(
            "compose", '{"descriptor":' + self._descriptor + ',"base":' + _tree_json(tree) + ',"overlay":{}}'
        )
        return _decode_tree(output["tree"])

    def _content_hash(self, tree: Tree) -> str:
        output = self._run("content_hash", '{"descriptor":' + self._descriptor + ',"tree":' + _tree_json(tree) + "}")
        return str(output["contentHash"])

    def _tree_result(self, tree: Tree, findings: List[Finding]) -> TreeResult:
        return TreeResult(tree, self._content_hash(tree), findings)

    def _diff(self, from_tree: Tree, to_tree: Tree) -> List[Change]:
        output = self._run(
            "diff",
            '{"descriptor":' + self._descriptor + ',"from":' + _tree_json(from_tree) + ',"to":' + _tree_json(to_tree) + "}",
        )
        changes: List[Change] = []
        for change in output["changes"]:
            row = change.get("row")
            changes.append(
                Change(change["kind"], change["entityKey"], change["operation"], dumps(row) if "row" in change else None)
            )
        return changes

    # -- Writes ------------------------------------------------------------

    def _write_row(self, tx: Tx, kind: str, ref: Ref, row: str, tombstone: bool, actor: str) -> str:
        """Writes a row onto a ref as the ref's row of its entity: live, or
        with tombstone set, the row that deletes the entity on the ref.
        Returns the row as stored."""
        return tx.upsert_row(kind, RowWrite(ref.id, ref.root, row, tombstone, actor))

    def _delete_entity(self, tx: Tx, ref: Ref, composed: _Index, kind: str, key: str, actor: str) -> None:
        """Writes the row that deletes an entity on a ref: a copy of the
        entity's effective row, so every required column holds, with its
        tombstone set."""
        row = composed.get(kind, {}).get(key)
        if row is None:
            raise EntityNotFoundError(f"entity not found on the ref: {kind} {key}")
        self._write_row(tx, kind, ref, row, True, actor)

    def _commit(self, tx: Tx, ref: Ref, options: CommitOptions, actor: str) -> Commit:
        """Composes the ref, diffs it against its last commit (or its base)
        and writes a commit with a patch per changed entity. Returns the new
        commit, or raises NothingToCommitError. The caller moves the ref's
        head."""
        composed, _, own = self._compose(tx, ref)
        validated = self._run("validate", '{"descriptor":' + self._descriptor + ',"tree":' + _tree_json(composed) + "}")
        findings = _decode_findings(validated["findings"])
        if findings:
            raise InvalidTreeError(findings)
        parent = ref.head if ref.head is not None else ref.base
        previous, pins, distance = self._materialize_pins(tx, parent)
        changes = self._diff(previous, composed)
        if not changes:
            raise NothingToCommitError()
        own_by_key = self._index(own)
        previous_by_key = self._index(previous)
        patches: List[Patch] = []
        nearest: Dict[_Entity, Patch] = {}
        for change in changes:
            kind = self._kind(change.kind)
            # An ADD or UPDATE pins the winning row. A DELETE pins the ref's
            # tombstone, or the entity's last committed row when a removed
            # parent took it with it.
            pinned = change.row
            if change.operation == "DELETE":
                pinned = previous_by_key.get(change.kind, {}).get(change.entity_key)
                raw = own_by_key.get(change.kind, {}).get(change.entity_key)
                if raw is not None:
                    try:
                        tombstone = self._read_row(kind, raw).tombstone
                    except ValueError:
                        # As the Go engine: a row it cannot read is not the
                        # tombstone.
                        tombstone = False
                    if tombstone:
                        pinned = raw
            if pinned is None:
                raise ValueError(f"engine: {change.kind} row {kind.key}: unexpected end of JSON input")
            r = self._read_row(kind, pinned)
            patch = Patch("", change.kind, change.entity_key, r.id, r.version, change.operation)
            patches.append(patch)
            nearest[(patch.kind, patch.entity_key)] = patch
        content_hash = self._content_hash(composed)
        sequence = tx.next_sequence(ref.root) if options.tag else None
        written = tx.insert_commit(
            NewCommit(ref.root, ref.id, parent, options.message, self._schema_epoch, content_hash, sequence, actor)
        )
        tx.insert_patches(written.id, patches)
        # A tagged commit is snapshotted, and so is one snapshot_every commits
        # past the nearest snapshot on its chain: its parent's pins with its
        # own patches laid over them.
        if options.tag or distance + 1 >= self._snapshot_every:
            _apply_patches(pins, nearest)
            if pins:
                tx.insert_snapshot(written.id, _pin_entries(pins))
                written.snapshot = True
        return written

    def _commit_and_move(
        self, tx: Tx, ref: Ref, options: CommitOptions, allow_empty: bool, seal: bool, actor: str
    ) -> CommitResult:
        """Commits the ref and moves its head, sealing it when asked. Nothing
        to commit is not an error when allow_empty is set: the ref's version
        still moves and the returned commit is None."""
        written: Optional[Commit] = None
        try:
            written = self._commit(tx, ref, options, actor)
        except NothingToCommitError:
            if not allow_empty:
                raise
        moved = tx.update_ref(
            RefUpdate(ref.id, ref.version, written.id if written is not None else None, None, seal, actor)
        )
        return CommitResult(moved, written)

    def _merge(
        self, tx: Tx, source: Ref, target: Ref, resolutions: Sequence[Resolution], actor: str
    ) -> List[Conflict]:
        """Merges source's head into target against source's base. With no
        conflicts it writes every entity the target does not already hold as
        the merge left it: the merged row, or the row that deletes the
        entity."""
        base = self._materialize(tx, source.base)
        theirs = base
        if source.head is not None:
            theirs = self._materialize(tx, source.head)
        ours = self._compose(tx, target)[0]
        result = self._core_merge_result(base, ours, theirs, resolutions)
        conflicts = _decode_conflicts(result["conflicts"])
        if conflicts:
            return conflicts
        merged_by_key = self._index(_decode_tree(result["merged"]))
        ours_by_key = self._index(ours)
        for outcome in result["entities"]:
            kind, entity_key = outcome["kind"], outcome["entityKey"]
            # A result equal to ours is ours' side, a delete on both sides
            # included, so a delete from another side deletes a live entity
            # of ours.
            if outcome["side"] == "ours":
                continue
            if outcome.get("deleted") is True:
                self._delete_entity(tx, target, ours_by_key, kind, entity_key, actor)
                continue
            row = merged_by_key.get(kind, {}).get(entity_key)
            if row is None:
                raise ValueError(f"engine: the merge left no {kind} row for {entity_key}")
            self._write_row(tx, kind, target, row, False, actor)
        return []

    def _core_merge_result(
        self, base: Tree, ours: Tree, theirs: Tree, resolutions: Sequence[Resolution]
    ) -> Dict[str, Any]:
        """Runs the core's three-way merge of three trees, with the
        resolutions' entity keys in their canonical form."""
        encoded: List[str] = []
        for resolution in resolutions:
            members = [
                '"kind":' + write_string(resolution.kind),
                '"entityKey":' + write_string(_id("resolution entity key", resolution.entity_key)),
                '"path":' + write_string(resolution.path),
            ]
            if resolution.take:
                members.append('"take":' + write_string(resolution.take))
            if resolution.value is not None and resolution.value != "":
                members.append('"value":' + dumps(loads(resolution.value)))
            encoded.append("{" + ",".join(members) + "}")
        request = (
            '{"descriptor":'
            + self._descriptor
            + ',"base":'
            + _tree_json(base)
            + ',"ours":'
            + _tree_json(ours)
            + ',"theirs":'
            + _tree_json(theirs)
        )
        if encoded:
            request += ',"resolutions":[' + ",".join(encoded) + "]"
        return self._run("merge", request + "}")

    def _core_merge(
        self, base: Tree, ours: Tree, theirs: Tree, resolutions: Sequence[Resolution]
    ) -> Tuple[Tree, List[Conflict]]:
        """_core_merge_result's merged tree, or its conflicts."""
        result = self._core_merge_result(base, ours, theirs, resolutions)
        conflicts = _decode_conflicts(result["conflicts"])
        if conflicts:
            return {}, conflicts
        return _decode_tree(result["merged"]), []

    def _overlay(self, tx: Tx, ref: Ref, base: Tree, want: Tree, current: Tree, own: Tree, actor: str) -> None:
        """Writes a ref's own rows so that, over the tree of its base (base),
        it composes to want. current is what the ref composes to now and own
        its rows. Each entity want holds differently from base gets the ref's
        row of it (a tombstone where want lacks it) unless the ref's row
        already gives it; every other row the ref holds is removed, so the
        entity reads through the base."""
        changes = self._diff(base, want)
        differs: Set[_Entity] = {(c.kind, c.entity_key) for c in self._diff(current, want)}
        own_by_key = self._index(own)
        base_by_key = self._index(base)
        kept: Set[_Entity] = set()
        for change in changes:
            at = (change.kind, change.entity_key)
            kept.add(at)
            kind = self._kind(change.kind)
            own_row = own_by_key.get(change.kind, {}).get(change.entity_key)
            tombstone = own_row is not None and self._read_row(kind, own_row).tombstone
            if change.operation == "DELETE":
                if own_row is not None and tombstone:
                    continue
                self._delete_entity(tx, ref, base_by_key, change.kind, change.entity_key, actor)
                continue
            if own_row is not None and not tombstone and at not in differs:
                continue
            assert change.row is not None
            self._write_row(tx, change.kind, ref, change.row, False, actor)
        for k in self._kinds:
            for key in own_by_key.get(k.name, {}):
                if (k.name, key) not in kept:
                    tx.remove_row(k.name, ref.id, key, actor)

    def _revert(self, tx: Tx, ref: Ref, tree: Tree, actor: str) -> None:
        """Writes the rows that make the ref compose to tree: each entity tree
        holds that the ref composes differently is written from tree, and each
        entity only the ref holds is deleted on it."""
        current = self._compose(tx, ref)[0]
        current_by_key = self._index(current)
        for change in self._diff(current, tree):
            if change.operation == "DELETE":
                self._delete_entity(tx, ref, current_by_key, change.kind, change.entity_key, actor)
                continue
            assert change.row is not None
            self._write_row(tx, change.kind, ref, change.row, False, actor)

    def _backfill(self, tx: Tx) -> int:
        """Writes every snapshot the graph's rules call for and it lacks: of a
        tagged commit (release takes only tagged ones, so this covers a
        released commit), and of a commit snapshot_every commits past the
        nearest snapshot on its chain. It visits each commit after its
        parent, so every walk it takes stops at a snapshot at most
        snapshot_every commits away. Returns how many snapshots it wrote."""
        nodes = tx.commits()
        by_id = {n.id: n for n in nodes}
        # Each visited commit's distance from the nearest snapshot on its
        # chain, 0 for a snapshotted commit.
        distance: Dict[str, int] = {}
        written = 0
        for start in nodes:
            # Visit start's unvisited ancestors from the oldest down.
            path: List[CommitNode] = []
            at: Optional[CommitNode] = start
            while at is not None:
                if at.id in distance:
                    break
                path.append(at)
                if at.parent is None:
                    break
                at = by_id.get(at.parent)
            for n in reversed(path):
                if n.snapshot:
                    distance[n.id] = 0
                    continue
                d = (distance.get(n.parent, 0) if n.parent is not None else 0) + 1
                if n.tagged or d >= self._snapshot_every:
                    if self._ensure_snapshot(tx, n.id, False):
                        written += 1
                    d = 0
                distance[n.id] = d
        return written


def _apply_patches(pins: _PinSet, patches: Mapping[_Entity, Patch]) -> None:
    """Lays patches over a pin set: a DELETE removes its entity, and any other
    patch pins its row version."""
    for at, p in patches.items():
        if p.operation == "DELETE":
            pins.pop(at, None)
            continue
        pins[at] = SnapshotEntry(p.kind, p.entity_key, p.entity_id, p.entity_version)


def _pin_entries(pins: _PinSet) -> List[SnapshotEntry]:
    """A pin set as a snapshot's entries, ordered by kind and entity key."""
    return sorted(pins.values(), key=lambda e: (e.kind, e.entity_key))


def _tree_json(tree: Tree) -> str:
    """A tree as the core reads it: its kinds sorted, each a JSON array of
    its rows."""
    kinds = sorted(kind for kind, rows in tree.items() if rows)
    return "{" + ",".join(write_string(kind) + ":[" + ",".join(tree[kind]) + "]" for kind in kinds) + "}"


def _decode_tree(value: Any) -> Tree:
    """A tree the core returned, without its empty kinds."""
    if not isinstance(value, dict):
        raise ValueError("engine: decode tree: not an object")
    tree: Tree = {}
    for kind, rows in value.items():
        if not isinstance(rows, list):
            raise ValueError(f"engine: decode tree: {kind} is not a list")
        if rows:
            tree[kind] = [dumps(row) for row in rows]
    return tree


def _decode_findings(value: Any) -> List[Finding]:
    findings: List[Finding] = []
    for item in value:
        finding: Finding = {"code": item["code"], "kind": item["kind"], "message": item["message"]}
        entity_key = item.get("entityKey")
        if isinstance(entity_key, str) and entity_key != "":
            finding["entityKey"] = entity_key
        findings.append(finding)
    return findings


def _decode_conflicts(value: Any) -> List[Conflict]:
    conflicts: List[Conflict] = []
    for item in value:
        units = {
            member: dumps(item[source])
            for member, source in (
                ("base", "base"),
                ("ours", "ours"),
                ("theirs", "theirs"),
                ("ours_author", "oursAuthor"),
                ("theirs_author", "theirsAuthor"),
            )
            if source in item
        }
        conflicts.append(Conflict(item["kind"], item["entityKey"], item["path"], **units))
    return conflicts

