"""The Postgres storage adapter of the version-graph engine (D19), the Python
counterpart of the Go module's package ``postgres``. It builds its
statements at run time from a graph's descriptor (version 2), which names
the graph's tables, each kind's role columns and every column's value class,
and it returns every row as a canonical row (``canonical``).

The graph's own tables have the columns the loader gives them (D17, D19): a
ref's root_id, parent_ref_id, base_commit_id, head_commit_id, name,
sealed_at, audit and soft-delete columns and _version; a commit's root_id,
ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence,
created_at and created_by; a patch's commit_id, entity_kind, entity_key,
entity_id, entity_version and operation; a snapshot entry's commit_id,
entity_kind, entity_key, entity_id and entity_version; and a release
pointer's root_id, commit_id, audit columns and _version. A history table
keys its images on the kind's id and version columns and holds each in data.

The adapter reaches Postgres through ``Client``, which returns every column
as the text Postgres writes for it, so no driver's type adaptation touches a
date, a time, an interval, a numeric or a bigint. ``psycopg_client`` binds
psycopg 3 (the ``postgres`` extra); this module imports nothing from psycopg
until it is called, so the package loads without it.
"""

import json
import threading
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import timedelta
from typing import Any, Callable, Dict, Iterator, List, Mapping, Optional, Protocol, Sequence, TypeVar, Union

from .canonical import canonical_of, canonical_row, postgres_input, uuid_canonical, uuid_hyphenated
from .errors import NameTakenError, NotFoundError, VersionConflictError
from .exactjson import loads, write_string
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
    "DEFAULT_HISTORY_ACTOR_SETTING",
    "Arg",
    "QueryResult",
    "Conn",
    "Client",
    "PostgresAdapter",
    "psycopg_client",
]

T = TypeVar("T")

DEFAULT_HISTORY_ACTOR_SETTING = "superschematic.history_actor_id"
"""The transaction-local setting a versioned table's history trigger reads a
hard delete's actor from, unless the schema's history_actor_setting naming
key names another."""

# The audit columns the adapter writes on every row when a kind has them.
_CREATED_AT = "created_at"
_CREATED_BY = "created_by"
_UPDATED_AT = "updated_at"
_UPDATED_BY = "updated_by"

# The most columns a Postgres table can have. No row the adapter writes has
# more members.
_MAX_COLUMNS = 1600

Arg = Union[None, str, int, bool, Sequence[str], Sequence[int]]
"""A statement argument: a string, an integer, a boolean, a list of strings
or of integers, or None. Each statement casts its arguments to their
columns' types."""


@dataclass
class QueryResult:
    """The rows a statement returned, each column as the text Postgres writes
    for it (None for SQL NULL), and how many rows it affected."""

    rows: List[List[Optional[str]]] = field(default_factory=list)
    rowcount: int = 0


class Conn(Protocol):
    """Runs statements inside one transaction."""

    def query(self, sql: str, args: Sequence[Arg] = ()) -> QueryResult:
        """Runs a statement with positional arguments ($1, $2, ...) and
        returns its rows, every column as text."""
        ...


class Client(Protocol):
    """Runs the adapter's statements. The adapter asks it for one
    transaction per engine operation. psycopg_client binds psycopg; another
    driver implements the protocol itself. An error it raises for a unique
    violation carries the SQLSTATE as ``sqlstate`` ("23505"), as psycopg's
    errors do."""

    def transact(self, fn: Callable[[Conn], T]) -> T:
        """Runs fn in one transaction. It commits when fn returns and rolls
        back when it raises, raising fn's error."""
        ...


@dataclass(frozen=True)
class _Kind:
    """One member kind's tables and columns."""

    name: str
    table: str
    history_table: str
    key: str
    id: str
    ref: str
    root: str
    tombstone: str
    version: str
    columns: Mapping[str, str]


def _quote(name: str) -> str:
    """Quotes an identifier."""
    return '"' + name.replace('"', '""') + '"'


def _literal(s: str) -> str:
    """Quotes a string as a SQL literal."""
    return "'" + s.replace("'", "''") + "'"


# A ref's graph columns, as text.
_REF_COLUMNS = (
    "id::text, root_id::text, COALESCE(parent_ref_id::text, ''), COALESCE(base_commit_id::text, ''), "
    "COALESCE(head_commit_id::text, ''), \"name\", sealed_at IS NOT NULL, deleted_at IS NOT NULL, _version"
)

# A commit's columns, followed by whether it has a snapshot.
_COMMIT_COLUMNS = (
    "id::text, root_id::text, ref_id::text, COALESCE(parent_commit_id::text, ''), COALESCE(message, ''), "
    'schema_epoch, content_hash, COALESCE("sequence"::text, \'\'), to_jsonb(created_at)::text, created_by::text'
)

# A release pointer's columns.
_RELEASE_COLUMNS = "id::text, root_id::text, commit_id::text, _version"


class PostgresAdapter:
    """A graph's statements, built from its descriptor. Safe to share; bind
    a Client with storage()."""

    def __init__(
        self,
        descriptor: Union[str, Mapping[str, Any]],
        *,
        history_actor_setting: str = "",
    ) -> None:
        """Reads a graph's descriptor (JSON text or a mapping). The core
        checks the rest of it; the adapter needs its tables, each kind's role
        columns, the root column among them, and each kind's columns.
        history_actor_setting is the setting the history triggers read a
        hard delete's actor from: the schema's history_actor_setting naming
        key ("" is DEFAULT_HISTORY_ACTOR_SETTING)."""
        d = json.loads(descriptor) if isinstance(descriptor, str) else descriptor
        if d.get("version") != 2:
            raise ValueError(f"postgres: descriptor version {d.get('version', 0)}; this adapter reads version 2")
        root = d.get("root") or {}
        for member, value in (
            ("root table", root.get("table")),
            ("root key", root.get("key")),
            ("refTable", d.get("refTable")),
            ("commitTable", d.get("commitTable")),
            ("patchTable", d.get("patchTable")),
            ("releaseTable", d.get("releaseTable")),
            ("snapshotTable", d.get("snapshotTable")),
        ):
            if not value:
                raise ValueError(f"postgres: the descriptor's {member} is empty")
        self.actor_setting = history_actor_setting or DEFAULT_HISTORY_ACTOR_SETTING
        # The key every language's adapter takes the sweep lock under, so
        # their sweepers exclude each other.
        self.sweep_key = "superschematic.versiongraph.sweep:" + d["refTable"]
        self.ref_table = _quote(d["refTable"])
        self.commit_table = _quote(d["commitTable"])
        self.patch_table = _quote(d["patchTable"])
        self.release_table = _quote(d["releaseTable"])
        self.snapshot_table = _quote(d["snapshotTable"])
        self.root_table = _quote(root["table"])
        self.root_key = _quote(root["key"])
        self.kinds: Dict[str, _Kind] = {}
        for k in d.get("kinds") or []:
            columns = k.get("columns") or {}
            for role in ("table", "historyTable", "key", "id", "ref", "root", "tombstone", "version"):
                column = k.get(role)
                if not column:
                    raise ValueError(f"postgres: kind {write_string(str(k.get('kind')))} has no {role}")
                if role not in ("table", "historyTable") and column not in columns:
                    raise ValueError(
                        f"postgres: kind {write_string(str(k.get('kind')))} {role} column "
                        f"{write_string(column)} is not in its columns"
                    )
            self.kinds[k["kind"]] = _Kind(
                k["kind"],
                k["table"],
                k["historyTable"],
                k["key"],
                k["id"],
                k["ref"],
                k["root"],
                k["tombstone"],
                k["version"],
                dict(columns),
            )

    def storage(self, client: Client) -> Storage:
        """Binds the adapter to a client."""
        return _PostgresStorage(self, client)


class _PostgresStorage:
    def __init__(self, adapter: PostgresAdapter, client: Client) -> None:
        self._adapter = adapter
        self._client = client

    def transact(self, fn: Callable[[Tx], T]) -> T:
        return self._client.transact(lambda conn: fn(_PostgresTx(self._adapter, conn)))


def _text(value: Optional[str]) -> str:
    if value is None:
        raise ValueError("postgres: a column the adapter reads is NULL")
    return value


def _bool(value: Optional[str]) -> bool:
    t = _text(value)
    if t in ("t", "true"):
        return True
    if t in ("f", "false"):
        return False
    raise ValueError(f"postgres: {write_string(t)} is not a boolean")


def _int(value: Optional[str]) -> int:
    t = _text(value)
    try:
        return int(t, 10)
    except ValueError:
        raise ValueError(f"postgres: {write_string(t)} is not an integer") from None


def _optional_id(value: Optional[str]) -> Optional[str]:
    """The canonical form of a UUID Postgres rendered as text, None for ""."""
    t = _text(value)
    return None if t == "" else uuid_canonical(t)


def _required_id(value: Optional[str]) -> str:
    return uuid_canonical(_text(value))


def _uuid_arg(id: Optional[str]) -> str:
    """The hyphenated text a statement casts to uuid, "" for None."""
    return "" if not id else uuid_hyphenated(id)


def _scan_ref(row: List[Optional[str]]) -> Ref:
    return Ref(
        id=_required_id(row[0]),
        root=_required_id(row[1]),
        parent=_optional_id(row[2]),
        base=_optional_id(row[3]),
        head=_optional_id(row[4]),
        name=_text(row[5]),
        sealed=_bool(row[6]),
        discarded=_bool(row[7]),
        version=_int(row[8]),
    )


def _scan_commit(row: List[Optional[str]]) -> Commit:
    sequence = _text(row[7])
    created_at = loads(canonical_of("dateTime", loads(_text(row[8]))))
    return Commit(
        id=_required_id(row[0]),
        root=_required_id(row[1]),
        ref=_required_id(row[2]),
        parent=_optional_id(row[3]),
        message=_text(row[4]),
        schema_epoch=_int(row[5]),
        content_hash=_text(row[6]),
        sequence=None if sequence == "" else _int(sequence),
        created_at=created_at,
        created_by=_required_id(row[9]),
        snapshot=_bool(row[10]),
    )


def _scan_release(row: List[Optional[str]]) -> Release:
    return Release(_required_id(row[0]), _required_id(row[1]), _required_id(row[2]), _int(row[3]))


def _micros(d: timedelta) -> int:
    """A duration as whole microseconds, as a statement multiplies an
    interval of one microsecond by it."""
    return d // timedelta(microseconds=1)


@contextmanager
def _context(what: str) -> Iterator[None]:
    """Prefixes an error's message with what the adapter was doing, keeping
    the error itself (its class, and a driver's sqlstate) so callers can
    still tell it apart."""
    try:
        yield
    except (NotFoundError, NameTakenError):
        raise
    except Exception as error:
        if error.args and isinstance(error.args[0], str) and not error.args[0].startswith("postgres: "):
            error.args = (f"postgres: {what}: {error.args[0]}",) + error.args[1:]
        raise


class _PostgresTx:
    """One transaction's view of the graph, over a Conn."""

    def __init__(self, adapter: PostgresAdapter, conn: Conn) -> None:
        self._a = adapter
        self._conn = conn

    def _kind(self, name: str) -> _Kind:
        k = self._a.kinds.get(name)
        if k is None:
            raise ValueError(f"postgres: unknown kind {write_string(name)}")
        return k

    def _rows(self, k: _Kind, sql: str, args: Sequence[Arg]) -> List[str]:
        """Runs a statement whose one column is a row's JSON as text, and
        returns each as the canonical row of kind."""
        out: List[str] = []
        for row in self._conn.query(sql, args).rows:
            try:
                out.append(canonical_row(k.columns, _text(row[0])))
            except ValueError as error:
                error.args = (f"{k.name} row: {error}",)
                raise
        return out

    def _ref(self, sql: str, args: Sequence[Arg]) -> Optional[Ref]:
        result = self._conn.query(sql, args)
        return _scan_ref(result.rows[0]) if result.rows else None

    def create_ref(self, ref: NewRef) -> Ref:
        sql = (
            f'INSERT INTO {self._a.ref_table} (root_id, parent_ref_id, base_commit_id, "name", created_by, updated_by) '
            "VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5::uuid, $5::uuid) "
            f"RETURNING {_REF_COLUMNS}"
        )
        try:
            with _context("create ref"):
                out = self._ref(
                    sql, [_uuid_arg(ref.root), _uuid_arg(ref.parent), _uuid_arg(ref.base), ref.name, _uuid_arg(ref.actor)]
                )
        except Exception as error:
            if getattr(error, "sqlstate", None) == "23505":
                raise NameTakenError(
                    f"the root already has a live ref of that name: {write_string(ref.name)}"
                ) from None
            raise
        assert out is not None
        return out

    def read_ref(self, id: str) -> Ref:
        return self._read_ref(id, "")

    def lock_ref(self, id: str) -> Ref:
        return self._read_ref(id, " FOR UPDATE")

    def _read_ref(self, id: str, lock: str) -> Ref:
        arg = _uuid_arg(id)
        with _context("read ref"):
            ref = self._ref(f"SELECT {_REF_COLUMNS} FROM {self._a.ref_table} WHERE id = $1::uuid{lock}", [arg])
        if ref is None:
            raise NotFoundError()
        return ref

    def update_ref(self, update: RefUpdate) -> Ref:
        sql = (
            f"UPDATE {self._a.ref_table} SET head_commit_id = COALESCE(NULLIF($3, '')::uuid, head_commit_id), "
            "base_commit_id = COALESCE(NULLIF($6, '')::uuid, base_commit_id), "
            "sealed_at = CASE WHEN $4::boolean THEN now() ELSE sealed_at END, updated_at = now(), updated_by = $5::uuid "
            f"WHERE id = $1::uuid AND _version = $2 RETURNING {_REF_COLUMNS}"
        )
        with _context("update ref"):
            ref = self._ref(
                sql,
                [
                    _uuid_arg(update.id),
                    update.version,
                    _uuid_arg(update.head),
                    update.seal,
                    _uuid_arg(update.actor),
                    _uuid_arg(update.base),
                ],
            )
        if ref is None:
            raise VersionConflictError()
        return ref

    def discard_ref(self, id: str, version: int, actor: str) -> None:
        with _context("discard ref"):
            result = self._conn.query(
                f"UPDATE {self._a.ref_table} SET deleted_at = now(), deleted_by = $3::uuid "
                "WHERE id = $1::uuid AND _version = $2 AND deleted_at IS NULL",
                [_uuid_arg(id), version, _uuid_arg(actor)],
            )
        if result.rowcount == 0:
            raise VersionConflictError()

    def rows(self, kind: str, ref: str) -> List[str]:
        k = self._kind(kind)
        with _context(f"read {k.name} rows"):
            return self._rows(
                k, f"SELECT to_jsonb(t)::text FROM {_quote(k.table)} AS t WHERE t.{_quote(k.ref)} = $1::uuid", [_uuid_arg(ref)]
            )

    def upsert_row(self, kind: str, write: RowWrite) -> str:
        k = self._kind(kind)
        parsed = loads(write.row)
        if not isinstance(parsed, dict):
            raise ValueError(f"postgres: a {k.name} row is a JSON object")
        members: Dict[str, Any] = dict(parsed)
        members.pop(k.id, None)
        members.pop(k.version, None)
        if k.key in members and members[k.key] is None:
            del members[k.key]
        members[k.tombstone] = write.tombstone
        members[k.ref] = write.ref
        members[k.root] = write.root
        for column in (_CREATED_BY, _UPDATED_BY):
            if column in k.columns:
                members[column] = write.actor
        if len(members) > _MAX_COLUMNS:
            raise ValueError(
                f"postgres: the {k.name} row has {len(members)} columns; a Postgres table has at most {_MAX_COLUMNS}"
            )
        values_in: Dict[str, str] = {}
        for column, value in members.items():
            value_class = k.columns.get(column)
            if value_class is None:
                raise ValueError(
                    f"postgres: the {k.name} row has column {write_string(column)}, which its descriptor does not declare"
                )
            with _context(f"{k.name} column {column}"):
                values_in[column] = postgres_input(value_class, value)
        columns = list(values_in)
        for column in (_CREATED_AT, _UPDATED_AT):
            if column in k.columns and column not in values_in:
                columns.append(column)
        columns.sort()
        input_json = "{" + ",".join(write_string(c) + ":" + values_in[c] for c in sorted(values_in)) + "}"

        names: List[str] = []
        values: List[str] = []
        updates: List[str] = []
        for column in columns:
            q = _quote(column)
            names.append(q)
            value_class = k.columns[column]
            if column in (_CREATED_AT, _UPDATED_AT):
                values.append("now()")
            elif value_class == "json" or value_class.endswith("[][]"):
                # A JSONB column takes the member itself, so a JSON null is
                # the value null, which a required column holds, rather than
                # SQL NULL, which jsonb_populate_record reads it as.
                values.append(f"($1::jsonb -> {_literal(column)})")
            else:
                values.append("r." + q)
            if column not in (k.key, k.ref, k.root, _CREATED_AT, _CREATED_BY):
                # The conflict key, the root and the creation audit stay as
                # the row was first written.
                updates.append(f"{q} = EXCLUDED.{q}")
        table = _quote(k.table)
        sql = (
            f"INSERT INTO {table} AS t ({', '.join(names)}) "
            f"SELECT {', '.join(values)} FROM jsonb_populate_record(NULL::{table}, $1::jsonb) AS r "
            f"ON CONFLICT ({_quote(k.key)}, {_quote(k.ref)}) DO UPDATE SET {', '.join(updates)} "
            "RETURNING to_jsonb(t)::text"
        )
        with _context(f"write {k.name} row"):
            rows = self._rows(k, sql, [input_json])
        if len(rows) != 1:
            raise ValueError(f"postgres: write {k.name} row: {len(rows)} rows written")
        return rows[0]

    def remove_row(self, kind: str, ref: str, entity_key: str, actor: str) -> bool:
        k = self._kind(kind)
        sql = (
            "WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) "
            f"DELETE FROM {_quote(k.table)} USING history_actor "
            f"WHERE {_quote(k.key)} = $3::uuid AND {_quote(k.ref)} = $4::uuid"
        )
        with _context(f"remove the {k.name} row"):
            result = self._conn.query(
                sql, [self._a.actor_setting, _uuid_arg(actor), _uuid_arg(entity_key), _uuid_arg(ref)]
            )
        self._clear_actor()
        return result.rowcount > 0

    def _clear_actor(self) -> None:
        with _context("clear the history actor"):
            self._conn.query("SELECT set_config($1, '', true)", [self._a.actor_setting])

    def images(self, kind: str, pins: Sequence[Pin]) -> List[str]:
        k = self._kind(kind)
        if not pins:
            return []
        sql = (
            f"SELECT h.data::text FROM {_quote(k.history_table)} AS h "
            "JOIN unnest($1::text[]::uuid[], $2::bigint[]) AS p(id, version) "
            f"ON h.{_quote(k.id)} = p.id AND h.{_quote(k.version)} = p.version"
        )
        with _context(f"read {k.name} history"):
            return self._rows(k, sql, [[uuid_hyphenated(p.id) for p in pins], [p.version for p in pins]])

    def _has_snapshot(self, id_sql: str) -> str:
        """The SQL that tells whether the commit whose id is id_sql has a
        snapshot."""
        return f"EXISTS (SELECT 1 FROM {self._a.snapshot_table} AS s WHERE s.commit_id = {id_sql})"

    def _commits(self, sql: str, args: Sequence[Arg]) -> List[Commit]:
        return [_scan_commit(row) for row in self._conn.query(sql, args).rows]

    def read_commit(self, id: str) -> Commit:
        with _context("read commit"):
            commits = self._commits(
                f"SELECT {_COMMIT_COLUMNS}, {self._has_snapshot('c.id')} FROM {self._a.commit_table} AS c WHERE id = $1::uuid",
                [_uuid_arg(id)],
            )
        if not commits:
            raise NotFoundError()
        return commits[0]

    def insert_commit(self, commit: NewCommit) -> Commit:
        sql = (
            f"INSERT INTO {self._a.commit_table} "
            '(root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, "sequence", created_by) '
            "VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7::bigint, $8::uuid) "
            f"RETURNING {_COMMIT_COLUMNS}, false"
        )
        with _context("write commit"):
            return self._commits(
                sql,
                [
                    _uuid_arg(commit.root),
                    _uuid_arg(commit.ref),
                    _uuid_arg(commit.parent),
                    commit.message,
                    commit.schema_epoch,
                    commit.content_hash,
                    commit.sequence,
                    _uuid_arg(commit.actor),
                ],
            )[0]

    def insert_patches(self, commit: str, patches: Sequence[Patch]) -> None:
        payload = (
            "["
            + ",".join(
                '{"kind":%s,"key":%s,"id":%s,"version":%d,"op":%s}'
                % (
                    write_string(p.kind),
                    write_string(uuid_hyphenated(p.entity_key)),
                    write_string(uuid_hyphenated(p.entity_id)),
                    p.entity_version,
                    write_string(p.operation),
                )
                for p in patches
            )
            + "]"
        )
        sql = (
            f"INSERT INTO {self._a.patch_table} (commit_id, entity_kind, entity_key, entity_id, entity_version, operation) "
            "SELECT $1::uuid, p.kind, p.key::uuid, p.id::uuid, p.version, p.op "
            "FROM jsonb_to_recordset($2::jsonb) AS p(kind text, key text, id text, version bigint, op text)"
        )
        with _context("write patches"):
            self._conn.query(sql, [_uuid_arg(commit), payload])

    def walk(self, commit: str, limit: int) -> List[Commit]:
        # The walk carries each commit's snapshot flag, and goes no further
        # than the first commit that has one.
        sql = (
            "WITH RECURSIVE chain AS ("
            f"SELECT c.*, 1 AS depth, {self._has_snapshot('c.id')} AS snapshotted FROM {self._a.commit_table} AS c "
            "WHERE c.id = $1::uuid "
            f"UNION ALL SELECT c.*, chain.depth + 1, {self._has_snapshot('c.id')} FROM {self._a.commit_table} AS c "
            "JOIN chain ON c.id = chain.parent_commit_id WHERE chain.depth < $2 AND NOT chain.snapshotted"
            f") SELECT {_COMMIT_COLUMNS}, snapshotted FROM chain ORDER BY depth"
        )
        with _context("walk commits"):
            return self._commits(sql, [_uuid_arg(commit), limit])

    def ref_commits(self, ref: str, head: str, limit: int) -> List[Commit]:
        sql = (
            "WITH RECURSIVE chain AS ("
            f"SELECT c.*, 1 AS depth FROM {self._a.commit_table} AS c WHERE c.id = $1::uuid AND c.ref_id = $2::uuid "
            f"UNION ALL SELECT c.*, chain.depth + 1 FROM {self._a.commit_table} AS c "
            "JOIN chain ON c.id = chain.parent_commit_id WHERE c.ref_id = $2::uuid AND chain.depth < $3"
            f") SELECT {_COMMIT_COLUMNS}, {self._has_snapshot('chain.id')} FROM chain ORDER BY depth"
        )
        with _context("list commits"):
            return self._commits(sql, [_uuid_arg(head), _uuid_arg(ref), limit])

    def patches(self, commits: Sequence[str]) -> List[Patch]:
        if not commits:
            return []
        sql = (
            "SELECT commit_id::text, entity_kind, entity_key::text, entity_id::text, entity_version, operation "
            f"FROM {self._a.patch_table} WHERE commit_id = ANY($1::text[]::uuid[])"
        )
        with _context("read patches"):
            result = self._conn.query(sql, [[uuid_hyphenated(c) for c in commits]])
            return [
                Patch(
                    _required_id(row[0]), _text(row[1]), _required_id(row[2]), _required_id(row[3]), _int(row[4]), _text(row[5])
                )
                for row in result.rows
            ]

    def next_sequence(self, root: str) -> int:
        arg = _uuid_arg(root)
        # FOR NO KEY UPDATE conflicts with itself and not with the key-share
        # locks that inserting a row that references the root takes.
        with _context("lock the root"):
            self._conn.query(
                f"SELECT 1 FROM {self._a.root_table} WHERE {self._a.root_key} = $1::uuid FOR NO KEY UPDATE", [arg]
            )
        with _context("read the next sequence"):
            result = self._conn.query(
                f'SELECT COALESCE(MAX("sequence"), 0) + 1 FROM {self._a.commit_table} WHERE root_id = $1::uuid', [arg]
            )
            return _int(result.rows[0][0])

    def prune(self, kind: str, retention_days: int, batch_size: int) -> int:
        k = self._kind(kind)
        # A kind declared without retentionDays has no prune function, and
        # keeps its history.
        function = _quote(k.table + "_prune_history")
        with _context(f"find the {k.name} prune function"):
            exists = _bool(self._conn.query("SELECT to_regproc($1) IS NOT NULL", [function]).rows[0][0])
        if not exists:
            return 0
        # The prune function's retention_days defaults to the kind's declared
        # retention, which a retention_days of 0 keeps.
        sql = f"SELECT {function}(max_rows => NULLIF($2, 0)::integer, retention_days => $1::integer)"
        args: List[Arg] = [retention_days, batch_size]
        if retention_days == 0:
            sql = f"SELECT {function}(max_rows => NULLIF($1, 0)::integer)"
            args = [batch_size]
        with _context(f"prune {k.name} history"):
            return _int(self._conn.query(sql, args).rows[0][0])

    def sweep_lock(self) -> bool:
        with _context("take the sweep lock"):
            result = self._conn.query("SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))", [self._a.sweep_key])
            return _bool(result.rows[0][0])

    def snapshot(self, commit: str) -> List[SnapshotEntry]:
        sql = (
            "SELECT entity_kind, entity_key::text, entity_id::text, entity_version "
            f"FROM {self._a.snapshot_table} WHERE commit_id = $1::uuid"
        )
        with _context("read the snapshot"):
            return [
                SnapshotEntry(_text(row[0]), _required_id(row[1]), _required_id(row[2]), _int(row[3]))
                for row in self._conn.query(sql, [_uuid_arg(commit)]).rows
            ]

    def insert_snapshot(self, commit: str, entries: Sequence[SnapshotEntry]) -> None:
        if not entries:
            return
        payload = (
            "["
            + ",".join(
                '{"kind":%s,"key":%s,"id":%s,"version":%d}'
                % (
                    write_string(e.kind),
                    write_string(uuid_hyphenated(e.entity_key)),
                    write_string(uuid_hyphenated(e.entity_id)),
                    e.entity_version,
                )
                for e in entries
            )
            + "]"
        )
        sql = (
            f"INSERT INTO {self._a.snapshot_table} (commit_id, entity_kind, entity_key, entity_id, entity_version) "
            "SELECT $1::uuid, e.kind, e.key::uuid, e.id::uuid, e.version "
            "FROM jsonb_to_recordset($2::jsonb) AS e(kind text, key text, id text, version bigint)"
        )
        with _context("write the snapshot"):
            self._conn.query(sql, [_uuid_arg(commit), payload])

    def commits(self) -> List[CommitNode]:
        sql = (
            "SELECT c.id::text, COALESCE(c.parent_commit_id::text, ''), c.\"sequence\" IS NOT NULL, "
            f"{self._has_snapshot('c.id')} FROM {self._a.commit_table} AS c"
        )
        with _context("read the commits"):
            return [
                CommitNode(_required_id(row[0]), _optional_id(row[1]), _bool(row[2]), _bool(row[3]))
                for row in self._conn.query(sql, []).rows
            ]

    def read_release(self, root: str) -> Release:
        with _context("read the release"):
            result = self._conn.query(
                f"SELECT {_RELEASE_COLUMNS} FROM {self._a.release_table} WHERE root_id = $1::uuid", [_uuid_arg(root)]
            )
        if not result.rows:
            raise NotFoundError()
        return _scan_release(result.rows[0])

    def write_release(self, write: ReleaseWrite) -> Release:
        args: List[Arg] = [_uuid_arg(write.root), _uuid_arg(write.commit), _uuid_arg(write.actor)]
        if write.version == 0:
            # A root's first pointer. Another writer's first pointer takes the
            # root's slot, as a move at a stale version would.
            sql = (
                f"INSERT INTO {self._a.release_table} (root_id, commit_id, created_by, updated_by) "
                "VALUES ($1::uuid, $2::uuid, $3::uuid, $3::uuid) ON CONFLICT (root_id) DO NOTHING "
                f"RETURNING {_RELEASE_COLUMNS}"
            )
        else:
            sql = (
                f"UPDATE {self._a.release_table} SET commit_id = $2::uuid, updated_at = now(), updated_by = $3::uuid "
                f"WHERE root_id = $1::uuid AND _version = $4 RETURNING {_RELEASE_COLUMNS}"
            )
            args.append(write.version)
        with _context("write the release"):
            result = self._conn.query(sql, args)
        if not result.rows:
            raise VersionConflictError()
        return _scan_release(result.rows[0])

    def discarded_refs(self, grace: timedelta) -> List[Ref]:
        return self._refs(
            f"SELECT {_REF_COLUMNS} FROM {self._a.ref_table} "
            "WHERE deleted_at IS NOT NULL AND deleted_at < now() - $1::bigint * interval '1 microsecond' "
            "ORDER BY deleted_at, id",
            [_micros(grace)],
        )

    def idle_drafts(self, idle: timedelta) -> List[Ref]:
        return self._refs(
            f"SELECT {_REF_COLUMNS} FROM {self._a.ref_table} "
            "WHERE deleted_at IS NULL AND parent_ref_id IS NOT NULL "
            "AND updated_at < now() - $1::bigint * interval '1 microsecond' ORDER BY updated_at, id",
            [_micros(idle)],
        )

    def _refs(self, sql: str, args: Sequence[Arg]) -> List[Ref]:
        with _context("read refs"):
            return [_scan_ref(row) for row in self._conn.query(sql, args).rows]

    def remove_ref_rows(self, kind: str, ref: str, actor: str) -> int:
        k = self._kind(kind)
        sql = (
            "WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) "
            f"DELETE FROM {_quote(k.table)} USING history_actor WHERE {_quote(k.ref)} = $3::uuid"
        )
        with _context(f"remove the {k.name} rows of a ref"):
            result = self._conn.query(sql, [self._a.actor_setting, _uuid_arg(actor), _uuid_arg(ref)])
        self._clear_actor()
        return result.rowcount


# -- psycopg 3 ----------------------------------------------------------------


class _PsycopgConn:
    """A Conn over one psycopg connection: each statement runs through a raw
    cursor, which takes Postgres's own $1 placeholders, and its rows are read
    from the libpq result as the text Postgres wrote, so no loader parses a
    value."""

    def __init__(self, connection: Any) -> None:
        import psycopg

        self._connection = connection
        self._cursor = psycopg.RawCursor(connection)
        self._encoding = connection.info.encoding

    def query(self, sql: str, args: Sequence[Arg] = ()) -> QueryResult:
        cursor = self._cursor
        cursor.execute(sql, [list(a) if isinstance(a, (list, tuple)) else a for a in args])
        result = cursor.pgresult
        rows: List[List[Optional[str]]] = []
        if result is not None:
            for i in range(result.ntuples):
                row: List[Optional[str]] = []
                for j in range(result.nfields):
                    value = result.get_value(i, j)
                    row.append(None if value is None else bytes(value).decode(self._encoding))
                rows.append(row)
        return QueryResult(rows, cursor.rowcount)

    def close(self) -> None:
        self._cursor.close()


class _PsycopgClient:
    def __init__(self, source: Any) -> None:
        # A pool hands each transaction a connection of its own; one
        # connection runs its transactions one at a time.
        self._pool = source if hasattr(source, "connection") and not hasattr(source, "cursor") else None
        self._connection = None if self._pool is not None else source
        self._lock = threading.Lock()

    def transact(self, fn: Callable[[Conn], T]) -> T:
        if self._pool is not None:
            with self._pool.connection() as connection:
                return self._run(connection, fn)
        with self._lock:
            return self._run(self._connection, fn)

    @staticmethod
    def _run(connection: Any, fn: Callable[[Conn], T]) -> T:
        conn = _PsycopgConn(connection)
        try:
            # A transaction of its own, or a savepoint of the one the caller
            # holds on the connection.
            with connection.transaction():
                return fn(conn)
        finally:
            conn.close()


def psycopg_client(source: Any) -> Client:
    """The default Client: psycopg 3 over a connection (``psycopg.Connection``)
    or a pool (``psycopg_pool.ConnectionPool``). Each transaction runs in
    ``connection.transaction()``: a transaction of its own, or a savepoint
    when the caller already holds a transaction on the connection, so the
    adapter's writes commit or roll back with the caller's. Over one
    connection the transactions run one at a time; over a pool each takes a
    connection of its own. It needs the ``postgres`` extra
    (``superschematic-versiongraph[postgres]``)."""
    return _PsycopgClient(source)
