"""The SQLite storage adapter of the version-graph engine (D32): ``Storage``
and ``Tx`` over one fixed layout of tables, the same for every graph, so the
engine runs a graph in a SQLite file. The Python counterpart of the
TypeScript package's ``@superschematic/versiongraph/sqlite``, statement for
statement, so a file one writes the other reads; ``postgres`` is its Postgres
counterpart, over the tables sqlgen generates per graph.

The layout is nine STRICT tables: ref, ref_history, commit, patch,
snapshot_entry, release, release_history, member and member_history, each
named by a function the caller gives (``default_table_name`` puts graph_
before each), as is every index. Every row carries its graph's name, so one
file holds several graphs, and every statement is scoped to the adapter's
graph. An id is TEXT holding a UUID in its canonical form (base62); a
version, a sequence and a tombstone are INTEGER; the times of refs, commits,
release pointers and history images are INTEGER microseconds since the Unix
epoch, returned as canonical date-times. A member row holds its kind and its
role columns as columns and every other column of the descriptor as one
canonical JSON object, data. Foreign keys check every edge inside the
layout; there is no root table, so nothing checks a root but the adapter,
which refuses a write whose ref or commit is another graph's or another
root's.

SQLite has no trigger that can assign NEW, so the adapter does what
Postgres's triggers do, in the statements of the transaction that changes a
row: it sets _version (1 on an insert, the old version plus 1 on an update),
writes the row's image at its new version on an insert or an update, and on
a delete writes the row's image at the old version plus 1 with the kind's
history actor column set to the delete's actor. An image leaves out the
kind's history-excluded columns, and reads back as it was stored, while a
live row reads with every column its kind declares. Refs and release
pointers keep history too. Every value it writes is canonicalized by its
class first (``canonical``), so a read returns what is stored and needs no
rules of its own. A row travels as JSON text read by ``exactjson``, so a
number keeps its digits from the row it was given to the row it returns.

The adapter reaches SQLite through ``Client``, as the Postgres adapter
reaches Postgres: ``transact`` runs one transaction, and its ``Conn`` runs
statements with numbered placeholders (?1, ?2, ...). ``sqlite_client`` binds
the standard library's ``sqlite3.Connection``, so the adapter needs nothing
beyond the standard library.
"""

import inspect
import json
import sqlite3
import threading
import time as _time
import uuid
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import timedelta
from typing import Any, Callable, Dict, Iterator, List, Mapping, Optional, Protocol, Sequence, Tuple, TypeVar, Union

from .canonical import _civil_from_days, canonical_of, uuid_canonical
from .errors import NameTakenError, NotFoundError, VersionConflictError
from .exactjson import dumps, loads, write_string
from .storage import (
    Commit,
    CommitNode,
    NewCommit,
    NewRef,
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
)

__all__ = [
    "MIN_SQLITE_VERSION",
    "SQLITE_BUSY",
    "SQLITE_CONSTRAINT_UNIQUE",
    "SQLITE_TABLES",
    "Value",
    "Row",
    "QueryResult",
    "Conn",
    "Client",
    "TableName",
    "default_table_name",
    "sqlite_layout",
    "SqliteAdapter",
    "micros_to_date_time",
    "is_unique_violation",
    "sqlite_client",
]

T = TypeVar("T")

SQLITE_BUSY = 5
"""SQLITE_BUSY: another connection holds the lock past the busy timeout."""

SQLITE_CONSTRAINT_UNIQUE = 2067
"""SQLITE_CONSTRAINT_UNIQUE: a unique index refused a row."""

MIN_SQLITE_VERSION = (3, 37, 0)
"""The oldest SQLite the adapter runs on: 3.37.0, the first with STRICT
tables, which the layout declares. Its statements also need RETURNING
(3.35.0), and the JSON functions json_each and json_extract, which every
build has from 3.38.0 and nearly every one before; ``sqlite_client`` checks
both when it binds a connection."""

Value = Union[None, int, float, str, bytes]
"""A value bound to a placeholder or read from a column."""

Row = Dict[str, Value]
"""A row a statement returned, keyed by column name."""

TableName = Callable[[str], str]
"""Names a table or an index of the layout from its local name ("ref",
"member_entity")."""


def default_table_name(name: str) -> str:
    """The default name of each table and index: graph_ and its local name."""
    return "graph_" + name


SQLITE_TABLES: Tuple[str, ...] = (
    "ref",
    "ref_history",
    "commit",
    "patch",
    "snapshot_entry",
    "release",
    "release_history",
    "member",
    "member_history",
)
"""The local names of the layout's tables, in the order the layout creates
them."""


@dataclass
class QueryResult:
    """The rows a statement returned, each keyed by column name, and how many
    rows a statement without RETURNING changed. The adapter reads rowcount
    only of such a statement, since the sqlite3 module of an older Python
    (3.9 among them) miscounts the rows of one with RETURNING."""

    rows: List[Row] = field(default_factory=list)
    rowcount: int = 0


class Conn(Protocol):
    """Runs statements inside one transaction."""

    def query(self, sql: str, args: Sequence[Value] = ()) -> QueryResult:
        """Runs a statement with positional arguments for its numbered
        placeholders (?1, ?2, ...), one for each, and returns its rows."""
        ...


class Client(Protocol):
    """Runs the adapter's statements on one SQLite connection.
    ``sqlite_client`` binds a ``sqlite3.Connection``; another driver
    implements the protocol itself, with the connection's foreign keys on.
    An error it raises when a unique index refuses a row is one
    ``is_unique_violation`` recognises."""

    def transact(self, fn: Callable[[Conn], T]) -> T:
        """Runs fn in one transaction: BEGIN IMMEDIATE, which takes the
        file's write lock at once, or a savepoint when a transaction is open
        on the connection, the client's own or its caller's. It commits (or
        releases the savepoint) when fn returns and rolls back when it
        raises, raising fn's error. It runs one transaction at a time."""
        ...


@dataclass(frozen=True)
class _Kind:
    """One member kind's role columns, columns and history."""

    name: str
    key: str
    id: str
    ref: str
    root: str
    tombstone: str
    version: str
    columns: Mapping[str, str]
    #: Every declared column but the role ones: what data holds.
    data: Tuple[str, ...]
    exclude: frozenset
    actor: Optional[str]
    retention_days: Optional[int]


@dataclass(frozen=True)
class _Tables:
    """The layout's tables, each quoted, under one name function."""

    ref: str
    ref_history: str
    commit: str
    patch: str
    snapshot: str
    release: str
    release_history: str
    member: str
    member_history: str


@dataclass(frozen=True)
class _Config:
    """An adapter's statements' parts, built once from its descriptor and
    options."""

    graph: str
    table_name: TableName
    tables: _Tables
    clock: Callable[[], int]
    kinds: Mapping[str, _Kind]


# The audit columns the adapter writes on every member row when a kind has
# them.
_CREATED_AT = "created_at"
_CREATED_BY = "created_by"
_UPDATED_AT = "updated_at"
_UPDATED_BY = "updated_by"

_MICROS_PER_DAY = 86_400_000_000


def _quote(name: str) -> str:
    """Quotes an identifier."""
    return '"' + name.replace('"', '""') + '"'


def _name_of(table_name: TableName, local: str) -> str:
    """Names one table or index, refusing a name function that gives no
    name."""
    name = table_name(local)
    if not isinstance(name, str) or name == "" or "\x00" in name:
        shown = write_string(name) if isinstance(name, str) else repr(name)
        raise TypeError(f"sqlite: the table name function named {write_string(local)} {shown}")
    return _quote(name)


def _tables_of(table_name: TableName) -> _Tables:
    return _Tables(
        ref=_name_of(table_name, "ref"),
        ref_history=_name_of(table_name, "ref_history"),
        commit=_name_of(table_name, "commit"),
        patch=_name_of(table_name, "patch"),
        snapshot=_name_of(table_name, "snapshot_entry"),
        release=_name_of(table_name, "release"),
        release_history=_name_of(table_name, "release_history"),
        member=_name_of(table_name, "member"),
        member_history=_name_of(table_name, "member_history"),
    )


def sqlite_layout(table_name: TableName = default_table_name) -> List[str]:
    """The statements that create the layout, each table and index under the
    name table_name gives it: one statement each, CREATE TABLE IF NOT EXISTS
    or CREATE [UNIQUE] INDEX IF NOT EXISTS, with no trigger and no
    transaction control, for a caller that runs its own migrations.
    ``SqliteAdapter.create_tables`` runs them."""
    t = _tables_of(table_name)

    def index(local: str) -> str:
        return _name_of(table_name, local)

    def history(table: str) -> str:
        return (
            f"CREATE TABLE IF NOT EXISTS {table} ("
            "history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, id TEXT NOT NULL, _version INTEGER NOT NULL, "
            "operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), data TEXT NOT NULL, "
            "recorded_at INTEGER NOT NULL"
            ") STRICT"
        )

    return [
        # A ref's head and a commit's ref point at each other. A ref is
        # written before any commit of it, and its head moves to a commit
        # only once the commit is written, so both keys are checked at once,
        # not deferred.
        f"CREATE TABLE IF NOT EXISTS {t.ref} ("
        "id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, "
        f"parent_ref_id TEXT REFERENCES {t.ref} (id), base_commit_id TEXT REFERENCES {t.commit} (id), "
        f"head_commit_id TEXT REFERENCES {t.commit} (id), name TEXT NOT NULL, sealed_at INTEGER, "
        "created_at INTEGER NOT NULL, created_by TEXT NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, "
        "deleted_at INTEGER, deleted_by TEXT, _version INTEGER NOT NULL"
        ") STRICT",
        # A root's live refs have distinct names: a name already taken is
        # this index's SQLITE_CONSTRAINT_UNIQUE.
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('ref_live_name')} ON {t.ref} (graph, root_id, name) "
        "WHERE deleted_at IS NULL",
        history(t.ref_history),
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('ref_history_version')} ON {t.ref_history} (id, _version)",
        f"CREATE TABLE IF NOT EXISTS {t.commit} ("
        "id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, "
        f"ref_id TEXT NOT NULL REFERENCES {t.ref} (id), parent_commit_id TEXT REFERENCES {t.commit} (id), "
        "message TEXT, schema_epoch INTEGER NOT NULL, content_hash TEXT NOT NULL, sequence INTEGER, "
        "created_at INTEGER NOT NULL, created_by TEXT NOT NULL"
        ") STRICT",
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('commit_sequence')} ON {t.commit} (graph, root_id, sequence)",
        f"CREATE TABLE IF NOT EXISTS {t.patch} ("
        f"id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES {t.commit} (id), "
        "entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL, "
        "operation TEXT NOT NULL CHECK (operation IN ('ADD', 'UPDATE', 'DELETE'))"
        ") STRICT",
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('patch_entity')} ON {t.patch} (commit_id, entity_kind, entity_key)",
        f"CREATE INDEX IF NOT EXISTS {index('patch_pin')} ON {t.patch} (entity_id, entity_version)",
        f"CREATE TABLE IF NOT EXISTS {t.snapshot} ("
        f"id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES {t.commit} (id), "
        "entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL"
        ") STRICT",
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('snapshot_entry_entity')} ON {t.snapshot} "
        "(commit_id, entity_kind, entity_key)",
        f"CREATE INDEX IF NOT EXISTS {index('snapshot_entry_pin')} ON {t.snapshot} (entity_id, entity_version)",
        f"CREATE TABLE IF NOT EXISTS {t.release} ("
        "id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, "
        f"commit_id TEXT NOT NULL REFERENCES {t.commit} (id), created_at INTEGER NOT NULL, created_by TEXT NOT NULL, "
        "updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, _version INTEGER NOT NULL"
        ") STRICT",
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('release_root')} ON {t.release} (graph, root_id)",
        history(t.release_history),
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('release_history_version')} ON {t.release_history} (id, _version)",
        f"CREATE TABLE IF NOT EXISTS {t.member} ("
        "id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, entity_key TEXT NOT NULL, "
        f"ref_id TEXT NOT NULL REFERENCES {t.ref} (id), root_id TEXT NOT NULL, "
        "tombstone INTEGER NOT NULL CHECK (tombstone IN (0, 1)), _version INTEGER NOT NULL, data TEXT NOT NULL"
        ") STRICT",
        # Unique on the entity key of a kind on a ref, declared as (graph,
        # kind, ref_id, entity_key) so it also serves a read of a ref's rows
        # of a kind.
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('member_entity')} ON {t.member} (graph, kind, ref_id, entity_key)",
        f"CREATE TABLE IF NOT EXISTS {t.member_history} ("
        "history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, "
        "_version INTEGER NOT NULL, operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), "
        "data TEXT NOT NULL, recorded_at INTEGER NOT NULL"
        ") STRICT",
        f"CREATE UNIQUE INDEX IF NOT EXISTS {index('member_history_version')} ON {t.member_history} (id, _version)",
        f"CREATE INDEX IF NOT EXISTS {index('member_history_recorded')} ON {t.member_history} (graph, kind, recorded_at)",
    ]


def _system_clock() -> int:
    """The system clock in whole microseconds since the Unix epoch."""
    return _time.time_ns() // 1000


class SqliteAdapter:
    """A graph's statements over the layout, built from its descriptor. Safe
    to share; bind a Client with storage()."""

    def __init__(
        self,
        descriptor: Union[str, Mapping[str, Any]],
        *,
        graph: str,
        table_name: Optional[TableName] = None,
        clock: Optional[Callable[[], int]] = None,
    ) -> None:
        """Reads a graph's descriptor (version 3, as JSON text or a
        mapping). The adapter reads only its kinds: each kind's role columns
        (the root among them), its columns' value classes and its history;
        the tables the descriptor names are the Postgres adapter's.

        graph is the graph's name, which every row of the graph carries;
        table_name names each table and index (default_table_name when
        None); clock gives the time a transaction writes, in whole
        microseconds since the Unix epoch, and a transaction reads it once,
        when it begins (the system clock when None)."""
        d = json.loads(descriptor) if isinstance(descriptor, str) else descriptor
        if d.get("version") != 3:
            raise ValueError(f"sqlite: descriptor version {d.get('version', 0)}; this adapter reads version 3")
        if not isinstance(graph, str) or graph == "":
            raise TypeError("sqlite: an adapter needs its graph's name (graph)")
        if clock is not None and not callable(clock):
            raise TypeError("sqlite: clock is a function that returns microseconds since the Unix epoch")
        name_function = table_name if table_name is not None else default_table_name
        kinds: Dict[str, _Kind] = {}
        for k in d.get("kinds") or []:
            columns = dict(k.get("columns") or {})
            kind = write_string(str(k.get("kind")))
            roles = [(role, k.get(role)) for role in ("key", "id", "ref", "root", "tombstone", "version")]
            for role, column in roles:
                if not column:
                    raise ValueError(f"sqlite: kind {kind} has no {role}")
                if column not in columns:
                    raise ValueError(f"sqlite: kind {kind} {role} column {write_string(column)} is not in its columns")
            if columns[k["tombstone"]] != "boolean" or columns[k["version"]] != "integer":
                raise ValueError(f"sqlite: kind {kind}: a tombstone is a boolean column and a version an integer one")
            history = k.get("history")
            if not isinstance(history, Mapping) or not isinstance(history.get("exclude"), list):
                raise ValueError(f"sqlite: kind {kind} has no history")
            role_columns = {column for _, column in roles}
            kinds[k["kind"]] = _Kind(
                name=k["kind"],
                key=k["key"],
                id=k["id"],
                ref=k["ref"],
                root=k["root"],
                tombstone=k["tombstone"],
                version=k["version"],
                columns=columns,
                data=tuple(sorted(column for column in columns if column not in role_columns)),
                exclude=frozenset(history["exclude"]),
                actor=history.get("actor"),
                retention_days=history.get("retentionDays"),
            )
        self._config = _Config(
            graph=graph,
            table_name=name_function,
            tables=_tables_of(name_function),
            clock=clock if clock is not None else _system_clock,
            kinds=kinds,
        )

    def create_tables(self, client: Client) -> None:
        """Creates the layout's tables and indexes where they are missing
        (sqlite_layout), in one transaction of the client."""
        statements = sqlite_layout(self._config.table_name)

        def run(conn: Conn) -> None:
            for statement in statements:
                conn.query(statement)

        client.transact(run)

    def storage(self, client: Client) -> Storage:
        """Binds the adapter to a client."""
        return _SqliteStorage(self._config, client)


# The time of each client's outermost transaction under way, by the
# client's id: a transaction begun inside another on the same client takes
# the outer one's time, as every statement of a Postgres transaction has the
# one now(). A client runs one transaction at a time, so only the thread
# inside it reads or writes its entry.
_open: Dict[int, int] = {}


def _read_clock(config: _Config) -> int:
    """Reads the clock once, refusing a time that is not a whole number of
    microseconds."""
    now = config.clock()
    if not isinstance(now, int) or isinstance(now, bool):
        raise TypeError(f"sqlite: the clock returned {now!r}, not a whole number of microseconds")
    return now


def _refuse_awaitable(value: Any) -> None:
    """Refuses a transaction's function that returned an awaitable, which a
    synchronous transaction cannot wait for."""
    if inspect.isawaitable(value):
        close = getattr(value, "close", None)
        if callable(close):
            # A coroutine that never runs; close it so it is not reported as
            # never awaited.
            close()
        raise TypeError("sqlite: a transaction is synchronous: its function returned an awaitable")


class _SqliteStorage:
    def __init__(self, config: _Config, client: Client) -> None:
        self._config = config
        self._client = client

    def transact(self, fn: Callable[[Tx], T]) -> T:
        config, client = self._config, self._client

        def run(conn: Conn) -> T:
            key = id(client)
            outer = _open.get(key)
            if outer is not None:
                out = fn(_SqliteTx(config, conn, outer))
                _refuse_awaitable(out)
                return out
            # Read once the client has begun, so times order as the writes
            # the file's write lock orders do.
            now = _read_clock(config)
            _open[key] = now
            try:
                out = fn(_SqliteTx(config, conn, now))
                _refuse_awaitable(out)
                return out
            finally:
                del _open[key]

        return client.transact(run)


def _new_id() -> str:
    """A new id: a version-4 UUID in its canonical form."""
    return uuid_canonical(str(uuid.uuid4()))


def micros_to_date_time(micros: int) -> str:
    """A time in microseconds since the Unix epoch as a canonical date-time:
    UTC with Z, its fraction of a second without trailing zeros and left out
    when zero. A year outside 0000-9999 is refused."""
    if not isinstance(micros, int) or isinstance(micros, bool):
        raise TypeError(f"sqlite: {micros!r} is not a whole number of microseconds")
    days, of_day = divmod(micros, _MICROS_PER_DAY)
    year, month, day = _civil_from_days(days)
    if not 0 <= year <= 9999:
        raise ValueError(f"sqlite: {micros} microseconds falls outside the years 0000-9999")
    seconds, fraction = divmod(of_day, 1_000_000)
    out = "%04d-%02d-%02dT%02d:%02d:%02d" % (year, month, day, seconds // 3600, seconds // 60 % 60, seconds % 60)
    if fraction:
        out += ("." + "%06d" % fraction).rstrip("0")
    return out + "Z"


def _describe(value: Any) -> str:
    return "NULL" if value is None else type(value).__name__


def _text(value: Value, column: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"sqlite: column {column} is {_describe(value)}, not text")
    return value


def _optional_text(value: Value, column: str) -> Optional[str]:
    return None if value is None else _text(value, column)


def _int(value: Any, column: str) -> int:
    if not isinstance(value, int) or isinstance(value, bool):
        raise ValueError(f"sqlite: column {column} is {value!r}, not an integer")
    return value


def _optional_int(value: Value, column: str) -> Optional[int]:
    return None if value is None else _int(value, column)


def is_unique_violation(error: BaseException) -> bool:
    """Whether error is SQLite's SQLITE_CONSTRAINT_UNIQUE (2067): a unique
    index refused a row. From Python 3.11 the sqlite3 module's errors carry
    SQLite's extended result code as ``sqlite_errorcode``, and an error that
    carries one is read by it alone. Before 3.11 they carry no code, and an
    ``sqlite3.IntegrityError`` whose message begins "UNIQUE constraint
    failed", as SQLite words the refusal, is one; that message cannot tell a
    unique index from a primary key (SQLITE_CONSTRAINT_PRIMARYKEY), which the
    adapter's inserts never repeat, since each takes a new random id."""
    code = getattr(error, "sqlite_errorcode", None)
    if isinstance(code, int) and not isinstance(code, bool):
        return code == SQLITE_CONSTRAINT_UNIQUE
    return isinstance(error, sqlite3.IntegrityError) and str(error).startswith("UNIQUE constraint failed")


@contextmanager
def _context(what: str) -> Iterator[None]:
    """Prefixes an error's message with what the adapter was doing, keeping
    the error itself (its class, and a driver's sqlite_errorcode) so callers
    can still tell it apart."""
    try:
        yield
    except (NotFoundError, NameTakenError):
        raise
    except Exception as error:
        if error.args and isinstance(error.args[0], str) and not error.args[0].startswith("sqlite: "):
            error.args = (f"sqlite: {what}: {error.args[0]}",) + error.args[1:]
        raise


def _write_object(members: Mapping[str, str]) -> str:
    """Writes a JSON object of members, each already JSON text, sorted by
    name."""
    return "{" + ",".join(write_string(name) + ":" + members[name] for name in sorted(members)) + "}"


def _read_object(text: str, column: str) -> Dict[str, str]:
    """Reads a JSON object column, each member as its JSON text."""
    parsed = loads(text)
    if not isinstance(parsed, dict):
        raise ValueError(f"sqlite: column {column} does not hold a JSON object")
    return {name: dumps(value) for name, value in parsed.items()}


def _json_text(value: Optional[str]) -> str:
    """The JSON text of an optional string."""
    return "null" if value is None else write_string(value)


def _json_time(micros: Optional[int]) -> str:
    """The JSON text of an optional time, as a canonical date-time."""
    return "null" if micros is None else write_string(micros_to_date_time(micros))


# The columns each table's reads select, in the layout's names.
_REF_COLUMNS = (
    "id, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, created_at, created_by, "
    "updated_at, updated_by, deleted_at, deleted_by, _version"
)
_COMMIT_COLUMNS = (
    "c.id, c.root_id, c.ref_id, c.parent_commit_id, c.message, c.schema_epoch, c.content_hash, c.sequence, "
    "c.created_at, c.created_by"
)
_RELEASE_COLUMNS = "id, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version"
_MEMBER_COLUMNS = "id, entity_key, ref_id, root_id, tombstone, _version, data"


def _scan_ref(row: Row) -> Ref:
    return Ref(
        id=_text(row["id"], "id"),
        root=_text(row["root_id"], "root_id"),
        parent=_optional_text(row["parent_ref_id"], "parent_ref_id"),
        base=_optional_text(row["base_commit_id"], "base_commit_id"),
        head=_optional_text(row["head_commit_id"], "head_commit_id"),
        name=_text(row["name"], "name"),
        sealed=_optional_int(row["sealed_at"], "sealed_at") is not None,
        discarded=_optional_int(row["deleted_at"], "deleted_at") is not None,
        version=_int(row["_version"], "_version"),
    )


def _ref_image(row: Row) -> str:
    """A ref's history image: each of its columns, an id in its canonical
    form and a time as a canonical date-time."""
    image: Dict[str, str] = {}
    for column in (
        "id",
        "root_id",
        "parent_ref_id",
        "base_commit_id",
        "head_commit_id",
        "name",
        "created_by",
        "updated_by",
        "deleted_by",
    ):
        image[column] = _json_text(_optional_text(row[column], column))
    for column in ("sealed_at", "created_at", "updated_at", "deleted_at"):
        image[column] = _json_time(_optional_int(row[column], column))
    image["_version"] = str(_int(row["_version"], "_version"))
    return _write_object(image)


def _release_image(row: Row) -> str:
    """A release pointer's history image, as _ref_image writes a ref's."""
    image: Dict[str, str] = {}
    for column in ("id", "root_id", "commit_id", "created_by", "updated_by"):
        image[column] = _json_text(_text(row[column], column))
    for column in ("created_at", "updated_at"):
        image[column] = _json_time(_int(row[column], column))
    image["_version"] = str(_int(row["_version"], "_version"))
    return _write_object(image)


def _scan_commit(row: Row) -> Commit:
    return Commit(
        id=_text(row["id"], "id"),
        root=_text(row["root_id"], "root_id"),
        ref=_text(row["ref_id"], "ref_id"),
        parent=_optional_text(row["parent_commit_id"], "parent_commit_id"),
        message=_optional_text(row["message"], "message") or "",
        schema_epoch=_int(row["schema_epoch"], "schema_epoch"),
        content_hash=_text(row["content_hash"], "content_hash"),
        sequence=_optional_int(row["sequence"], "sequence"),
        created_at=micros_to_date_time(_int(row["created_at"], "created_at")),
        created_by=_text(row["created_by"], "created_by"),
        snapshot=_int(row["snapshot"], "snapshot") == 1,
    )


def _scan_release(row: Row) -> Release:
    return Release(
        id=_text(row["id"], "id"),
        root=_text(row["root_id"], "root_id"),
        commit=_text(row["commit_id"], "commit_id"),
        version=_int(row["_version"], "_version"),
    )


@dataclass(frozen=True)
class _Member:
    """A member row as the layout holds it: its role columns, and its other
    columns as JSON text."""

    id: str
    key: str
    ref: str
    root: str
    tombstone: bool
    version: int
    data: Mapping[str, str]


def _scan_member(row: Row) -> _Member:
    return _Member(
        id=_text(row["id"], "id"),
        key=_text(row["entity_key"], "entity_key"),
        ref=_text(row["ref_id"], "ref_id"),
        root=_text(row["root_id"], "root_id"),
        tombstone=_int(row["tombstone"], "tombstone") == 1,
        version=_int(row["_version"], "_version"),
        data=_read_object(_text(row["data"], "data"), "data"),
    )


def _member_members(k: _Kind, m: _Member) -> Dict[str, str]:
    """A member's canonical row's members: its role columns under the
    descriptor's names, and every other column the kind declares, null where
    the stored row lacks it, as a Postgres row has a column added after it
    was written. A stored column the kind no longer declares is kept as
    stored."""
    members = dict(m.data)
    for column in k.data:
        if column not in members:
            members[column] = "null"
    members[k.id] = write_string(m.id)
    members[k.key] = write_string(m.key)
    members[k.ref] = write_string(m.ref)
    members[k.root] = write_string(m.root)
    members[k.tombstone] = "true" if m.tombstone else "false"
    members[k.version] = str(m.version)
    return members


def _micros(d: timedelta) -> int:
    """A duration as whole microseconds."""
    return d // timedelta(microseconds=1)


class _SqliteTx:
    """One transaction's view of the graph, over a Conn, at the
    transaction's time."""

    def __init__(self, config: _Config, conn: Conn, now: int) -> None:
        self._a = config
        self._conn = conn
        self._time = now

    def _kind(self, name: str) -> _Kind:
        k = self._a.kinds.get(name)
        if k is None:
            raise ValueError(f"sqlite: unknown kind {write_string(name)}")
        return k

    def _run(self, sql: str, args: Sequence[Value]) -> int:
        """Runs a statement without RETURNING and returns how many rows it
        changed."""
        return self._conn.query(sql, args).rowcount

    def _get(self, sql: str, args: Sequence[Value]) -> Optional[Row]:
        rows = self._conn.query(sql, args).rows
        return rows[0] if rows else None

    def _all(self, sql: str, args: Sequence[Value]) -> List[Row]:
        return self._conn.query(sql, args).rows

    def _role_value(self, k: _Kind, column: str, value: str) -> str:
        """The canonical text of a value of a column's class, given as a
        string, decoded: an id, a key, an actor."""
        canonical = loads(canonical_of(k.columns[column], value))
        if not isinstance(canonical, str):
            raise ValueError(f"sqlite: the {k.name} column {column} holds a string")
        return canonical

    def _require_ref(self, id: str, root: str, what: str) -> None:
        """Refuses a write whose ref is not one of this graph's refs of
        root."""
        found = self._get(
            f"SELECT 1 AS found FROM {self._a.tables.ref} WHERE graph = ?1 AND id = ?2 AND root_id = ?3",
            [self._a.graph, id, root],
        )
        if found is None:
            raise ValueError(f"sqlite: {what}: ref {id} is not a ref of root {root} in graph {self._a.graph}")

    def _require_commit(self, id: str, root: str, what: str) -> None:
        """Refuses a write whose commit is not one of this graph's commits of
        root."""
        found = self._get(
            f"SELECT 1 AS found FROM {self._a.tables.commit} WHERE graph = ?1 AND id = ?2 AND root_id = ?3",
            [self._a.graph, id, root],
        )
        if found is None:
            raise ValueError(f"sqlite: {what}: commit {id} is not a commit of root {root} in graph {self._a.graph}")

    def _history(self, table: str, id: str, version: int, operation: str, data: str) -> None:
        """Writes a ref's or a release pointer's history image."""
        self._run(
            f"INSERT INTO {table} (history_id, graph, id, _version, operation, data, recorded_at) "
            "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            [_new_id(), self._a.graph, id, version, operation, data, self._time],
        )

    def _member_history(self, k: _Kind, id: str, version: int, operation: str, members: Mapping[str, str]) -> None:
        """Writes a member's history image: its canonical row's members less
        the kind's history-excluded columns."""
        image = {name: value for name, value in members.items() if name not in k.exclude}
        self._run(
            f"INSERT INTO {self._a.tables.member_history} "
            "(history_id, graph, kind, id, _version, operation, data, recorded_at) "
            "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            [_new_id(), self._a.graph, k.name, id, version, operation, _write_object(image), self._time],
        )

    def create_ref(self, ref: NewRef) -> Ref:
        t = self._a.tables
        with _context("create ref"):
            if ref.parent is not None:
                self._require_ref(ref.parent, ref.root, "the parent")
            if ref.base is not None:
                self._require_commit(ref.base, ref.root, "the base")
            id = _new_id()
            try:
                row = self._get(
                    f"INSERT INTO {t.ref} (id, graph, root_id, parent_ref_id, base_commit_id, head_commit_id, name, "
                    "sealed_at, created_at, created_by, updated_at, updated_by, deleted_at, deleted_by, _version) "
                    f"VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, NULL, ?7, ?8, ?7, ?8, NULL, NULL, 1) RETURNING {_REF_COLUMNS}",
                    [id, self._a.graph, ref.root, ref.parent, ref.base, ref.name, self._time, ref.actor],
                )
            except Exception as error:
                if is_unique_violation(error):
                    raise NameTakenError(
                        f"the root already has a live ref of that name: {write_string(ref.name)}"
                    ) from None
                raise
            assert row is not None
            self._history(t.ref_history, id, 1, "INSERT", _ref_image(row))
            return _scan_ref(row)

    def read_ref(self, id: str) -> Ref:
        with _context("read ref"):
            row = self._get(f"SELECT {_REF_COLUMNS} FROM {self._a.tables.ref} WHERE graph = ?1 AND id = ?2", [self._a.graph, id])
        if row is None:
            raise NotFoundError()
        return _scan_ref(row)

    def lock_ref(self, id: str) -> Ref:
        """read_ref: the one writer the file's write lock lets in orders
        every write, so a ref needs no lock of its own."""
        return self.read_ref(id)

    def update_ref(self, update: RefUpdate) -> Ref:
        t = self._a.tables
        with _context("update ref"):
            if update.head is not None or update.base is not None:
                current = self._get(f"SELECT root_id FROM {t.ref} WHERE graph = ?1 AND id = ?2", [self._a.graph, update.id])
                if current is None:
                    raise VersionConflictError()
                root = _text(current["root_id"], "root_id")
                if update.head is not None:
                    self._require_commit(update.head, root, "the head")
                if update.base is not None:
                    self._require_commit(update.base, root, "the base")
            row = self._get(
                f"UPDATE {t.ref} SET head_commit_id = COALESCE(?3, head_commit_id), "
                "base_commit_id = COALESCE(?4, base_commit_id), "
                "sealed_at = CASE WHEN ?5 = 1 THEN ?6 ELSE sealed_at END, updated_at = ?6, updated_by = ?7, "
                f"_version = _version + 1 WHERE graph = ?1 AND id = ?2 AND _version = ?8 RETURNING {_REF_COLUMNS}",
                [
                    self._a.graph,
                    update.id,
                    update.head,
                    update.base,
                    1 if update.seal else 0,
                    self._time,
                    update.actor,
                    update.version,
                ],
            )
            if row is None:
                raise VersionConflictError()
            self._history(t.ref_history, update.id, _int(row["_version"], "_version"), "UPDATE", _ref_image(row))
        return _scan_ref(row)

    def discard_ref(self, id: str, version: int, actor: str) -> None:
        t = self._a.tables
        with _context("discard ref"):
            row = self._get(
                f"UPDATE {t.ref} SET deleted_at = ?3, deleted_by = ?4, _version = _version + 1 "
                f"WHERE graph = ?1 AND id = ?2 AND _version = ?5 AND deleted_at IS NULL RETURNING {_REF_COLUMNS}",
                [self._a.graph, id, self._time, actor, version],
            )
            if row is None:
                raise VersionConflictError()
            self._history(t.ref_history, id, _int(row["_version"], "_version"), "UPDATE", _ref_image(row))

    def _members(self, sql: str, args: Sequence[Value]) -> List[_Member]:
        return [_scan_member(row) for row in self._all(sql, args)]

    def rows(self, kind: str, ref: str) -> List[str]:
        k = self._kind(kind)
        with _context(f"read {k.name} rows"):
            found = self._members(
                f"SELECT {_MEMBER_COLUMNS} FROM {self._a.tables.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3",
                [self._a.graph, k.name, ref],
            )
            return [_write_object(_member_members(k, m)) for m in found]

    def upsert_row(self, kind: str, write: RowWrite) -> str:
        k = self._kind(kind)
        with _context(f"write {k.name} row"):
            return self._upsert(k, write)

    def _upsert(self, k: _Kind, write: RowWrite) -> str:
        parsed = loads(write.row)
        if not isinstance(parsed, dict):
            raise ValueError(f"a {k.name} row is a JSON object")
        # The row's other columns, each canonical. The adapter writes the
        # ref, the root and the tombstone, and never the id or the version.
        given: Dict[str, str] = {}
        for column, value in parsed.items():
            if column not in k.columns:
                raise ValueError(
                    f"the {k.name} row has column {write_string(column)}, which its descriptor does not declare"
                )
            if column in (k.id, k.version, k.ref, k.root, k.tombstone, k.key):
                continue
            with _context(f"column {column}"):
                given[column] = canonical_of(k.columns[column], value)
        key: Optional[str] = None
        key_value = parsed.get(k.key)
        if key_value is not None:
            canonical = loads(canonical_of(k.columns[k.key], key_value))
            if not isinstance(canonical, str):
                raise ValueError(f"the {k.name} row's entity key is a string")
            key = canonical
        ref = self._role_value(k, k.ref, write.ref)
        root = self._role_value(k, k.root, write.root)
        self._require_ref(ref, root, "the row's ref")
        now = micros_to_date_time(self._time)

        def audit(column: str, value: str) -> None:
            if column in k.columns:
                given[column] = canonical_of(k.columns[column], value)

        t = self._a.tables
        existing: Optional[_Member] = None
        if key is not None:
            found = self._members(
                f"SELECT {_MEMBER_COLUMNS} FROM {t.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4",
                [self._a.graph, k.name, ref, key],
            )
            existing = found[0] if found else None
        if existing is not None:
            # A column the row lacks keeps its stored value; the entity key,
            # the root and the creation audit stay as the row was first
            # written.
            given.pop(_CREATED_AT, None)
            given.pop(_CREATED_BY, None)
            audit(_UPDATED_AT, now)
            audit(_UPDATED_BY, write.actor)
            data = dict(existing.data)
            data.update(given)
            # A column the kind gained after the row was written is null, as
            # a Postgres row has it.
            for column in k.data:
                if column not in data:
                    data[column] = "null"
            stored = _Member(
                id=existing.id,
                key=existing.key,
                ref=existing.ref,
                root=existing.root,
                tombstone=write.tombstone,
                version=existing.version + 1,
                data=data,
            )
            changed = self._run(
                f"UPDATE {t.member} SET tombstone = ?3, _version = ?4, data = ?5 WHERE graph = ?1 AND id = ?2 AND _version = ?6",
                [
                    self._a.graph,
                    stored.id,
                    1 if stored.tombstone else 0,
                    stored.version,
                    _write_object(stored.data),
                    existing.version,
                ],
            )
            if changed != 1:
                raise ValueError(f"{changed} rows written")
            members = _member_members(k, stored)
            self._member_history(k, stored.id, stored.version, "UPDATE", members)
            return _write_object(members)
        # A column the row lacks holds its default, null; the audit columns
        # hold the write's actor and time.
        for column in (_CREATED_AT, _UPDATED_AT):
            audit(column, now)
        for column in (_CREATED_BY, _UPDATED_BY):
            audit(column, write.actor)
        data = {column: given.get(column, "null") for column in k.data}
        stored = _Member(
            id=self._role_value(k, k.id, _new_id()),
            key=key if key is not None else self._role_value(k, k.key, _new_id()),
            ref=ref,
            root=root,
            tombstone=write.tombstone,
            version=1,
            data=data,
        )
        self._run(
            f"INSERT INTO {t.member} (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) "
            "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, ?8)",
            [
                stored.id,
                self._a.graph,
                k.name,
                stored.key,
                stored.ref,
                stored.root,
                1 if stored.tombstone else 0,
                _write_object(data),
            ],
        )
        members = _member_members(k, stored)
        self._member_history(k, stored.id, 1, "INSERT", members)
        return _write_object(members)

    def _remove(self, k: _Kind, m: _Member, actor: str) -> None:
        """Hard-deletes a member row and writes its DELETE image: the row at
        its version plus 1, with the kind's history actor column, when it has
        one, set to actor."""
        changed = self._run(f"DELETE FROM {self._a.tables.member} WHERE graph = ?1 AND id = ?2", [self._a.graph, m.id])
        if changed != 1:
            raise ValueError(f"{changed} rows deleted")
        members = _member_members(k, m)
        members[k.version] = str(m.version + 1)
        if k.actor is not None:
            members[k.actor] = canonical_of(k.columns[k.actor], actor)
        self._member_history(k, m.id, m.version + 1, "DELETE", members)

    def remove_row(self, kind: str, ref: str, entity_key: str, actor: str) -> bool:
        k = self._kind(kind)
        with _context(f"remove the {k.name} row"):
            key = self._role_value(k, k.key, entity_key)
            found = self._members(
                f"SELECT {_MEMBER_COLUMNS} FROM {self._a.tables.member} "
                "WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4",
                [self._a.graph, k.name, ref, key],
            )
            for m in found:
                self._remove(k, m, actor)
            return len(found) > 0

    def images(self, kind: str, pins: Sequence[Pin]) -> List[str]:
        """Each pinned image as it was stored, as a Postgres history image
        reads: one taken before its kind gained a column lacks it, and the
        core reads a content column a row lacks as null."""
        k = self._kind(kind)
        if not pins:
            return []
        pin_list = "[" + ",".join(f"[{write_string(p.id)},{_int(p.version, 'version')}]" for p in pins) + "]"
        with _context(f"read {k.name} history"):
            return [
                _text(row["data"], "data")
                for row in self._all(
                    f"SELECT h.data FROM json_each(?3) AS p JOIN {self._a.tables.member_history} AS h "
                    "ON h.id = json_extract(p.value, '$[0]') AND h._version = json_extract(p.value, '$[1]') "
                    "WHERE h.graph = ?1 AND h.kind = ?2",
                    [self._a.graph, k.name, pin_list],
                )
            ]

    def _has_snapshot(self, id_sql: str) -> str:
        """The SQL that tells whether the commit whose id is id_sql has a
        snapshot."""
        return f"EXISTS (SELECT 1 FROM {self._a.tables.snapshot} AS s WHERE s.commit_id = {id_sql})"

    def read_commit(self, id: str) -> Commit:
        with _context("read commit"):
            row = self._get(
                f"SELECT {_COMMIT_COLUMNS}, {self._has_snapshot('c.id')} AS snapshot FROM {self._a.tables.commit} AS c "
                "WHERE c.graph = ?1 AND c.id = ?2",
                [self._a.graph, id],
            )
        if row is None:
            raise NotFoundError()
        return _scan_commit(row)

    def insert_commit(self, commit: NewCommit) -> Commit:
        with _context("write commit"):
            self._require_ref(commit.ref, commit.root, "the commit's ref")
            if commit.parent is not None:
                self._require_commit(commit.parent, commit.root, "the commit's parent")
            id = _new_id()
            self._run(
                f"INSERT INTO {self._a.tables.commit} (id, graph, root_id, ref_id, parent_commit_id, message, "
                "schema_epoch, content_hash, sequence, created_at, created_by) "
                "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)",
                [
                    id,
                    self._a.graph,
                    commit.root,
                    commit.ref,
                    commit.parent,
                    None if commit.message == "" else commit.message,
                    commit.schema_epoch,
                    commit.content_hash,
                    commit.sequence,
                    self._time,
                    commit.actor,
                ],
            )
            return Commit(
                id=id,
                root=commit.root,
                ref=commit.ref,
                parent=commit.parent,
                message=commit.message,
                schema_epoch=commit.schema_epoch,
                content_hash=commit.content_hash,
                sequence=commit.sequence,
                created_at=micros_to_date_time(self._time),
                created_by=commit.actor,
                snapshot=False,
            )

    def _require_own_commit(self, id: str, what: str) -> None:
        """Refuses a commit of another graph, whatever its root, before
        writing its patches or its snapshot."""
        found = self._get(f"SELECT 1 AS found FROM {self._a.tables.commit} WHERE graph = ?1 AND id = ?2", [self._a.graph, id])
        if found is None:
            raise ValueError(f"{what}: commit {id} is not a commit of graph {self._a.graph}")

    def _pinned(self, kind: str, entity_key: str, entity_id: str) -> Tuple[_Kind, str, str]:
        """An entity a patch or a snapshot entry pins: its kind, and its key
        and id in their canonical forms."""
        k = self._kind(kind)
        return k, self._role_value(k, k.key, entity_key), self._role_value(k, k.id, entity_id)

    def insert_patches(self, commit: str, patches: Sequence[Patch]) -> None:
        with _context("write patches"):
            self._require_own_commit(commit, "the patches' commit")
            for p in patches:
                k, key, id = self._pinned(p.kind, p.entity_key, p.entity_id)
                self._run(
                    f"INSERT INTO {self._a.tables.patch} "
                    "(id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) "
                    "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
                    [_new_id(), self._a.graph, commit, k.name, key, id, p.entity_version, p.operation],
                )

    def _commits(self, sql: str, args: Sequence[Value]) -> List[Commit]:
        return [_scan_commit(row) for row in self._all(sql, args)]

    def walk(self, commit: str, limit: int) -> List[Commit]:
        # The walk carries each commit's snapshot flag, and goes no further
        # than the first commit that has one.
        t = self._a.tables
        sql = (
            "WITH RECURSIVE chain (id, parent, depth, snapshotted) AS ("
            f"SELECT c.id, c.parent_commit_id, 1, {self._has_snapshot('c.id')} FROM {t.commit} AS c "
            "WHERE c.graph = ?1 AND c.id = ?2 "
            f"UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1, {self._has_snapshot('c.id')} FROM {t.commit} AS c "
            "JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND chain.depth < ?3 AND NOT chain.snapshotted"
            f") SELECT {_COMMIT_COLUMNS}, chain.snapshotted AS snapshot FROM chain JOIN {t.commit} AS c ON c.id = chain.id "
            "ORDER BY chain.depth"
        )
        with _context("walk commits"):
            return self._commits(sql, [self._a.graph, commit, limit])

    def ref_commits(self, ref: str, head: str, limit: int) -> List[Commit]:
        t = self._a.tables
        sql = (
            "WITH RECURSIVE chain (id, parent, depth) AS ("
            f"SELECT c.id, c.parent_commit_id, 1 FROM {t.commit} AS c WHERE c.graph = ?1 AND c.id = ?2 AND c.ref_id = ?3 "
            f"UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1 FROM {t.commit} AS c "
            "JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND c.ref_id = ?3 AND chain.depth < ?4"
            f") SELECT {_COMMIT_COLUMNS}, {self._has_snapshot('c.id')} AS snapshot FROM chain "
            f"JOIN {t.commit} AS c ON c.id = chain.id ORDER BY chain.depth"
        )
        with _context("list commits"):
            return self._commits(sql, [self._a.graph, head, ref, limit])

    def patches(self, commits: Sequence[str]) -> List[Patch]:
        if not commits:
            return []
        with _context("read patches"):
            return [
                Patch(
                    commit=_text(row["commit_id"], "commit_id"),
                    kind=_text(row["entity_kind"], "entity_kind"),
                    entity_key=_text(row["entity_key"], "entity_key"),
                    entity_id=_text(row["entity_id"], "entity_id"),
                    entity_version=_int(row["entity_version"], "entity_version"),
                    operation=_text(row["operation"], "operation"),
                )
                for row in self._all(
                    "SELECT commit_id, entity_kind, entity_key, entity_id, entity_version, operation "
                    f"FROM {self._a.tables.patch} WHERE graph = ?1 AND commit_id IN (SELECT value FROM json_each(?2))",
                    [self._a.graph, "[" + ",".join(write_string(c) for c in commits) + "]"],
                )
            ]

    def next_sequence(self, root: str) -> int:
        """The root's highest sequence plus one: the file's one writer orders
        every tagger, so the root needs no lock of its own."""
        with _context("read the next sequence"):
            row = self._get(
                f"SELECT COALESCE(MAX(sequence), 0) + 1 AS next FROM {self._a.tables.commit} WHERE graph = ?1 AND root_id = ?2",
                [self._a.graph, root],
            )
            return _int(None if row is None else row["next"], "next")

    def prune(self, kind: str, retention_days: int, batch_size: int) -> int:
        k = self._kind(kind)
        # A kind declared without retentionDays keeps its history.
        if k.retention_days is None:
            return 0
        if not isinstance(batch_size, int) or isinstance(batch_size, bool) or batch_size < 0:
            raise ValueError(
                f"sqlite: prune {k.name} history: a batch is a whole number of images, 0 for no limit, not {batch_size!r}"
            )
        days = retention_days if retention_days != 0 else k.retention_days
        t = self._a.tables
        # Older than the retention, not the newest image of its row, and
        # pinned by no patch and no snapshot; the oldest first.
        sql = (
            f"DELETE FROM {t.member_history} WHERE history_id IN ("
            f"SELECT h.history_id FROM {t.member_history} AS h WHERE h.graph = ?1 AND h.kind = ?2 AND h.recorded_at < ?3 "
            f"AND EXISTS (SELECT 1 FROM {t.member_history} AS newer WHERE newer.id = h.id AND newer._version > h._version) "
            f"AND NOT EXISTS (SELECT 1 FROM {t.patch} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id "
            "AND pin.entity_version = h._version) "
            f"AND NOT EXISTS (SELECT 1 FROM {t.snapshot} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id "
            "AND pin.entity_version = h._version) "
            "ORDER BY h.recorded_at, h.id, h._version LIMIT ?4)"
        )
        with _context(f"prune {k.name} history"):
            return self._run(
                sql, [self._a.graph, k.name, self._time - days * _MICROS_PER_DAY, -1 if batch_size == 0 else batch_size]
            )

    def sweep_lock(self) -> bool:
        """True: the one writer the file's write lock lets in is the only
        sweeper there can be."""
        return True

    def snapshot(self, commit: str) -> List[SnapshotEntry]:
        with _context("read the snapshot"):
            return [
                SnapshotEntry(
                    kind=_text(row["entity_kind"], "entity_kind"),
                    entity_key=_text(row["entity_key"], "entity_key"),
                    entity_id=_text(row["entity_id"], "entity_id"),
                    entity_version=_int(row["entity_version"], "entity_version"),
                )
                for row in self._all(
                    "SELECT entity_kind, entity_key, entity_id, entity_version "
                    f"FROM {self._a.tables.snapshot} WHERE graph = ?1 AND commit_id = ?2",
                    [self._a.graph, commit],
                )
            ]

    def insert_snapshot(self, commit: str, entries: Sequence[SnapshotEntry]) -> None:
        if not entries:
            return
        with _context("write the snapshot"):
            self._require_own_commit(commit, "the snapshot's commit")
            for e in entries:
                k, key, id = self._pinned(e.kind, e.entity_key, e.entity_id)
                self._run(
                    f"INSERT INTO {self._a.tables.snapshot} "
                    "(id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) "
                    "VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
                    [_new_id(), self._a.graph, commit, k.name, key, id, e.entity_version],
                )

    def commits(self) -> List[CommitNode]:
        with _context("read the commits"):
            return [
                CommitNode(
                    id=_text(row["id"], "id"),
                    parent=_optional_text(row["parent_commit_id"], "parent_commit_id"),
                    tagged=_int(row["tagged"], "tagged") == 1,
                    snapshot=_int(row["snapshot"], "snapshot") == 1,
                )
                for row in self._all(
                    f"SELECT c.id, c.parent_commit_id, c.sequence IS NOT NULL AS tagged, {self._has_snapshot('c.id')} "
                    f"AS snapshot FROM {self._a.tables.commit} AS c WHERE c.graph = ?1",
                    [self._a.graph],
                )
            ]

    def read_release(self, root: str) -> Release:
        with _context("read the release"):
            row = self._get(
                f"SELECT {_RELEASE_COLUMNS} FROM {self._a.tables.release} WHERE graph = ?1 AND root_id = ?2",
                [self._a.graph, root],
            )
        if row is None:
            raise NotFoundError()
        return _scan_release(row)

    def write_release(self, write: ReleaseWrite) -> Release:
        t = self._a.tables
        with _context("write the release"):
            self._require_commit(write.commit, write.root, "the release's commit")
            if write.version == 0:
                # A root's first pointer. Another first pointer of the root
                # holds its slot, as a move at a stale version would.
                row = self._get(
                    f"INSERT INTO {t.release} (id, graph, root_id, commit_id, created_at, created_by, updated_at, "
                    "updated_by, _version) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?5, ?6, 1) "
                    f"ON CONFLICT (graph, root_id) DO NOTHING RETURNING {_RELEASE_COLUMNS}",
                    [_new_id(), self._a.graph, write.root, write.commit, self._time, write.actor],
                )
            else:
                row = self._get(
                    f"UPDATE {t.release} SET commit_id = ?3, updated_at = ?4, updated_by = ?5, _version = _version + 1 "
                    f"WHERE graph = ?1 AND root_id = ?2 AND _version = ?6 RETURNING {_RELEASE_COLUMNS}",
                    [self._a.graph, write.root, write.commit, self._time, write.actor, write.version],
                )
            if row is None:
                raise VersionConflictError()
            release = _scan_release(row)
            self._history(
                t.release_history,
                release.id,
                release.version,
                "INSERT" if write.version == 0 else "UPDATE",
                _release_image(row),
            )
            return release

    def discarded_refs(self, grace: timedelta) -> List[Ref]:
        return self._refs(
            f"SELECT {_REF_COLUMNS} FROM {self._a.tables.ref} "
            "WHERE graph = ?1 AND deleted_at IS NOT NULL AND deleted_at < ?2 ORDER BY deleted_at, id",
            [self._a.graph, self._time - _micros(grace)],
        )

    def idle_drafts(self, idle: timedelta) -> List[Ref]:
        return self._refs(
            f"SELECT {_REF_COLUMNS} FROM {self._a.tables.ref} "
            "WHERE graph = ?1 AND deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < ?2 "
            "ORDER BY updated_at, id",
            [self._a.graph, self._time - _micros(idle)],
        )

    def _refs(self, sql: str, args: Sequence[Value]) -> List[Ref]:
        with _context("read refs"):
            return [_scan_ref(row) for row in self._all(sql, args)]

    def remove_ref_rows(self, kind: str, ref: str, actor: str) -> int:
        k = self._kind(kind)
        with _context(f"remove the {k.name} rows of a ref"):
            found = self._members(
                f"SELECT {_MEMBER_COLUMNS} FROM {self._a.tables.member} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3",
                [self._a.graph, k.name, ref],
            )
            for m in found:
                self._remove(k, m, actor)
            return len(found)


# -- sqlite3 --------------------------------------------------------------------


def _version_tuple(version: str) -> Tuple[int, ...]:
    return tuple(int(part) for part in version.split(".")[:3])


class _Sqlite3Conn:
    """A Conn over one sqlite3 connection: each statement runs through a
    cursor of its own, whose rows are plain tuples whatever row factory the
    connection has, read into rows keyed by column name."""

    def __init__(self, connection: sqlite3.Connection) -> None:
        self._connection = connection

    def query(self, sql: str, args: Sequence[Value] = ()) -> QueryResult:
        cursor = self._connection.cursor()
        try:
            cursor.row_factory = None
            cursor.execute(sql, tuple(args))
            rows = cursor.fetchall()
            names = [d[0] for d in cursor.description] if cursor.description else []
            return QueryResult([dict(zip(names, row)) for row in rows], cursor.rowcount)
        finally:
            cursor.close()


class _Sqlite3Client:
    def __init__(self, connection: sqlite3.Connection) -> None:
        version = sqlite3.sqlite_version
        if _version_tuple(version) < MIN_SQLITE_VERSION:
            raise RuntimeError(
                f"sqlite: this Python's SQLite is {version}; the adapter needs "
                f"{'.'.join(str(n) for n in MIN_SQLITE_VERSION)} or newer, the first with the STRICT tables its "
                "layout declares (sqlite3.sqlite_version is the library Python was built with)"
            )
        # From Python 3.12 a connection's autocommit, unless it is
        # LEGACY_TRANSACTION_CONTROL (-1), decides its transactions, and
        # isolation_level does only when it is.
        autocommit = getattr(connection, "autocommit", -1)
        if autocommit is False or (autocommit is not True and connection.isolation_level is not None):
            raise ValueError(
                "sqlite: the sqlite3 module begins transactions on this connection on its own; open it with "
                "isolation_level=None (or autocommit=True from Python 3.12), and the client issues BEGIN IMMEDIATE, "
                "SAVEPOINT, RELEASE, COMMIT and ROLLBACK itself"
            )
        conn = _Sqlite3Conn(connection)
        # SQLite ignores the pragma inside a transaction, so a connection
        # bound inside one keeps whatever its caller set.
        conn.query("PRAGMA foreign_keys = ON")
        if conn.query("PRAGMA foreign_keys").rows != [{"foreign_keys": 1}]:
            raise ValueError("sqlite: the connection's foreign keys would not turn on; bind it outside a transaction")
        try:
            conn.query("SELECT json_extract('[1]', '$[0]') AS one, (SELECT count(*) FROM json_each('[1]')) AS n")
        except sqlite3.Error as error:
            raise RuntimeError(
                f"sqlite: this Python's SQLite ({version}) cannot run json_each and json_extract, which the adapter's "
                f"statements use: {error}"
            ) from None
        self._connection = connection
        self._conn = conn
        # Reentrant, so a transaction begun inside another on the same thread
        # runs as its savepoint, while another thread's waits for both.
        self._lock = threading.RLock()
        self._depth = 0

    def transact(self, fn: Callable[[Conn], T]) -> T:
        with self._lock:
            connection = self._connection
            if not connection.in_transaction:
                connection.execute("BEGIN IMMEDIATE")
                try:
                    out = fn(self._conn)
                except BaseException:
                    _quietly(connection, "ROLLBACK")
                    raise
                try:
                    connection.execute("COMMIT")
                except BaseException:
                    _quietly(connection, "ROLLBACK")
                    raise
                return out
            savepoint = f"superschematic_versiongraph_{self._depth}"
            connection.execute(f"SAVEPOINT {savepoint}")
            self._depth += 1
            try:
                out = fn(self._conn)
            except BaseException:
                self._depth -= 1
                _quietly(connection, f"ROLLBACK TO {savepoint}", f"RELEASE {savepoint}")
                raise
            self._depth -= 1
            connection.execute(f"RELEASE {savepoint}")
            return out


def _quietly(connection: sqlite3.Connection, *statements: str) -> None:
    """Runs statements that end a failed transaction, keeping the failure
    that ended it as the error to report."""
    try:
        for statement in statements:
            connection.execute(statement)
    except sqlite3.Error:
        pass


def sqlite_client(connection: sqlite3.Connection) -> Client:
    """The default Client: the standard library's sqlite3 over an open
    connection, which must be opened with ``isolation_level=None`` (or
    ``autocommit=True`` from Python 3.12) so the sqlite3 module never begins
    a transaction on its own. It refuses a SQLite older than
    MIN_SQLITE_VERSION, or one without the JSON functions, and turns the
    connection's foreign keys on, which SQLite ignores inside a transaction,
    so bind it outside one unless the caller's has them on.

    Each transaction begins with BEGIN IMMEDIATE, which takes the file's
    write lock at once and waits for it as long as the connection's timeout
    (its busy timeout) says, then fails with SQLITE_BUSY; one begun inside
    another, or while the caller holds a transaction on the connection, is a
    savepoint of it, so the adapter's writes commit or roll back with the
    caller's. Over one connection the client runs one transaction at a time,
    and a transaction begun inside another on the same thread runs as its
    savepoint. Bind one client per connection."""
    return _Sqlite3Client(connection)
