"""The Postgres adapter's own rules, which the scenarios do not reach: what
its constructor refuses, a row with more members than a table has columns,
a prune that keeps pinned images, a sweep lock that
one transaction holds at a time, a ref lock another transaction waits for,
the version fences of update_ref and discard_ref, and the psycopg client's
savepoint over a transaction the caller holds, its one transaction at a time
over a connection and its pool. The Python counterpart of the Go module's
postgres_test.go."""

import json
import threading
import time
from typing import Any, Callable, Dict, List, Sequence, TypeVar

import pytest
from support import DESCRIPTOR, DSN, Scratch, requires_database

from superschematic_versiongraph.errors import NameTakenError, VersionConflictError
from superschematic_versiongraph.postgres import Arg, Conn, PostgresAdapter, QueryResult, psycopg_client
from superschematic_versiongraph.storage import NewCommit, NewRef, Patch, RefUpdate, RowWrite, Tx


def _without(path: List[str]) -> Callable[[Dict[str, Any]], None]:
    def edit(d: Dict[str, Any]) -> None:
        target: Any = d
        for step in path[:-1]:
            target = target[int(step)] if isinstance(target, list) else target[step]
        del target[path[-1]]

    return edit


def _set(member: str, value: Any) -> Callable[[Dict[str, Any]], None]:
    def edit(d: Dict[str, Any]) -> None:
        d[member] = value

    return edit


REFUSALS = [
    ("the fixture's descriptor", lambda d: None, ""),
    ("an empty refTable", _set("refTable", ""), "refTable is empty"),
    ("an empty commitTable", _set("commitTable", ""), "commitTable is empty"),
    ("an empty patchTable", _set("patchTable", ""), "patchTable is empty"),
    ("an empty releaseTable", _set("releaseTable", ""), "releaseTable is empty"),
    ("an empty snapshotTable", _set("snapshotTable", ""), "snapshotTable is empty"),
    ("a kind without a root column", _without(["kinds", "0", "root"]), "has no root"),
    (
        "a role column missing from the kind's columns",
        _without(["kinds", "0", "columns", "_version"]),
        'version column "_version" is not in its columns',
    ),
    ("a descriptor of version 2", _set("version", 2), "reads version 3"),
]


@pytest.mark.parametrize("name,edit,refuse", REFUSALS, ids=[r[0] for r in REFUSALS])
def test_new_adapter(name: str, edit: Callable[[Dict[str, Any]], None], refuse: str) -> None:
    d = json.loads(DESCRIPTOR)
    edit(d)
    if refuse == "":
        PostgresAdapter(json.dumps(d))
        PostgresAdapter(d)
        return
    with pytest.raises(ValueError, match=refuse):
        PostgresAdapter(json.dumps(d))


T = TypeVar("T")


class _NoStatements:
    """A client whose transactions fail the test on any statement."""

    def transact(self, fn: Callable[[Conn], T]) -> T:
        return fn(self)

    def query(self, sql: str, args: Sequence[Arg] = ()) -> QueryResult:
        pytest.fail(f"ran {sql}")


def test_upsert_row_refuses_more_columns_than_a_table() -> None:
    """A row with more members than a Postgres table has columns is refused
    before any statement runs."""
    members = {f"c{i}": i for i in range(1601)}
    store = PostgresAdapter(DESCRIPTOR).storage(_NoStatements())
    with pytest.raises(ValueError, match="at most 1600"):
        store.transact(lambda tx: tx.upsert_row("step", RowWrite("1", "1", json.dumps(members), False, "2")))


ROOT = "00000000-0000-0000-0000-000000000001"
INSERT_ROOT = (
    f"INSERT INTO recipe (id, title, created_by) VALUES ('{ROOT}', 'Bread', '00000000-0000-0000-0000-000000000002')"
)
ACTOR = "2"


def _main(tx: Tx, name: str = "main") -> Any:
    return tx.create_ref(NewRef("1", None, None, name, ACTOR))


@requires_database
def test_prune_keeps_pinned_images(scratch_schema: Callable[[str], Scratch]) -> None:
    # A prune past every image's retention deletes the superseded image no
    # commit pins and keeps the pinned one, and a kind declared without
    # retentionDays has no prune function and prunes nothing.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))

    def write(tx: Tx) -> None:
        ref = _main(tx)
        pinned = ""
        for instruction in ("Mix", "Mix well", "Mix gently"):
            row = json.dumps({"entity_key": "Mix", "position": 1, "instruction": instruction, "timings": {}})
            stored = tx.upsert_row("step", RowWrite(ref.id, ref.root, row, False, ACTOR))
            if instruction == "Mix well":
                pinned = stored
        tx.upsert_row("cover", RowWrite(ref.id, ref.root, '{"photo_url": "a.jpg"}', False, ACTOR))
        image = json.loads(pinned)
        commit = tx.insert_commit(NewCommit(ref.root, ref.id, None, "", 0, "h", None, ACTOR))
        tx.insert_patches(commit.id, [Patch("", "step", "Mix", image["id"], image["_version"], "ADD")])

    storage.transact(write)
    for table in ("step_history", "cover_history"):
        connection.execute(f"UPDATE {table} SET recorded_at = now() - interval '400 days'")
    assert storage.transact(lambda tx: [tx.prune("step", 365, 0), tx.prune("cover", 365, 0)]) == [1, 0]
    history = connection.execute("SELECT data->>'instruction' FROM step_history ORDER BY _version").fetchall()
    assert [row[0] for row in history] == ["Mix well", "Mix gently"]


@requires_database
def test_the_sweep_lock_is_held_by_one_transaction(scratch_schema: Callable[[str], Scratch]) -> None:
    # While one transaction holds the graph's sweep lock, another does not
    # get it and does not wait; once the first ends, the lock is free.
    scratch = scratch_schema("vg_adapter_py")
    adapter = PostgresAdapter(DESCRIPTOR)
    holder = adapter.storage(psycopg_client(scratch.connect()))
    other = adapter.storage(psycopg_client(scratch.connect()))

    def take() -> bool:
        return other.transact(lambda tx: tx.sweep_lock())

    def hold(tx: Tx) -> None:
        assert tx.sweep_lock() is True
        assert take() is False

    holder.transact(hold)
    assert take() is True


@requires_database
def test_lock_ref_waits_for_the_holder(scratch_schema: Callable[[str], Scratch]) -> None:
    # While one transaction holds a ref's lock, another transaction's
    # lock_ref of it waits (here past its lock_timeout, with
    # lock_not_available) while read_ref does not, and once the first ends
    # the lock is free.
    import psycopg

    scratch = scratch_schema("vg_adapter_py")
    first = scratch.connect()
    first.execute(INSERT_ROOT)
    adapter = PostgresAdapter(DESCRIPTOR)
    holder = adapter.storage(psycopg_client(first))
    ref = holder.transact(_main)
    second = scratch.connect()
    second.execute("SET lock_timeout = '200ms'")
    other = adapter.storage(psycopg_client(second))

    def lock() -> Any:
        return other.transact(lambda tx: tx.lock_ref(ref.id))

    def hold(tx: Tx) -> None:
        tx.lock_ref(ref.id)
        with pytest.raises(psycopg.Error) as waited:
            lock()
        assert waited.value.sqlstate == "55P03"
        assert other.transact(lambda t: t.read_ref(ref.id)).id == ref.id

    holder.transact(hold)
    assert lock().id == ref.id


@requires_database
def test_update_ref_fences_its_version(scratch_schema: Callable[[str], Scratch]) -> None:
    # update_ref at the ref's version moves it to the next version, and
    # update_ref at the version it had before raises VersionConflictError and
    # changes nothing.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))

    def run(tx: Tx) -> None:
        ref = _main(tx)
        moved = tx.update_ref(RefUpdate(ref.id, ref.version, None, None, False, ACTOR))
        assert moved.version == ref.version + 1
        with pytest.raises(VersionConflictError):
            tx.update_ref(RefUpdate(ref.id, ref.version, None, None, True, ACTOR))
        now = tx.read_ref(ref.id)
        assert (now.version, now.sealed) == (moved.version, False)

    storage.transact(run)


@requires_database
def test_discard_ref_refuses_a_discarded_ref(scratch_schema: Callable[[str], Scratch]) -> None:
    # discard_ref of a ref already discarded, even at its current version,
    # raises VersionConflictError and keeps who discarded it first.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))

    def run(tx: Tx) -> None:
        ref = _main(tx)
        tx.discard_ref(ref.id, ref.version, "2")
        discarded = tx.read_ref(ref.id)
        assert discarded.discarded is True
        with pytest.raises(VersionConflictError):
            tx.discard_ref(ref.id, discarded.version, "3")

    storage.transact(run)
    by = connection.execute("SELECT deleted_by::text FROM recipe_ref").fetchall()
    assert [row[0] for row in by] == ["00000000-0000-0000-0000-000000000002"]


@requires_database
def test_the_client_runs_as_a_savepoint_of_the_callers_transaction(scratch_schema: Callable[[str], Scratch]) -> None:
    # Bound to a connection whose transaction the caller holds, the adapter's
    # transaction is a savepoint inside it, so what it writes rolls back with
    # the caller's transaction; a failed one rolls back to its savepoint and
    # leaves the caller's transaction usable.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    connection.execute("BEGIN")
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))
    assert storage.transact(_main).name == "main"
    with pytest.raises(NameTakenError):
        storage.transact(_main)
    assert connection.execute("SELECT count(*) FROM recipe_ref").fetchone()[0] == 1
    connection.execute("ROLLBACK")
    assert connection.execute("SELECT count(*) FROM recipe_ref").fetchone()[0] == 0


@requires_database
def test_the_client_runs_its_transactions_one_at_a_time(scratch_schema: Callable[[str], Scratch]) -> None:
    # The client's transactions over one connection run one at a time: two
    # asked for together, from two threads, do not interleave their
    # statements.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))
    order: List[str] = []
    started = threading.Event()

    def slow(tx: Tx) -> None:
        order.append("first begins")
        _main(tx, "first")
        started.set()
        time.sleep(0.05)
        order.append("first ends")

    def fast(tx: Tx) -> None:
        order.append("second begins")
        _main(tx, "second")
        order.append("second ends")

    thread = threading.Thread(target=lambda: storage.transact(slow))
    thread.start()
    assert started.wait(10)
    storage.transact(fast)
    thread.join()
    assert order == ["first begins", "first ends", "second begins", "second ends"]


@requires_database
def test_the_client_over_a_pool_takes_a_connection_per_transaction(scratch_schema: Callable[[str], Scratch]) -> None:
    # Over a pool each transaction runs on a connection of its own: one
    # transaction holds the sweep lock while another, on another connection
    # of the pool, is refused it, and a failed transaction rolls back
    # without harming the next.
    from psycopg_pool import ConnectionPool

    scratch = scratch_schema("vg_adapter_py")
    scratch.connect().execute(INSERT_ROOT)
    with ConnectionPool(DSN, min_size=2, open=False, kwargs={"options": f"-c search_path={scratch.schema},public"}) as pool:
        storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(pool))

        def hold(tx: Tx) -> None:
            assert tx.sweep_lock() is True
            assert storage.transact(lambda other: other.sweep_lock()) is False

        storage.transact(hold)
        storage.transact(_main)
        with pytest.raises(NameTakenError):
            storage.transact(_main)
        assert storage.transact(lambda tx: _main(tx, "second")).name == "second"
