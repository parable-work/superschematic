"""The Postgres adapter's own rules, which the scenarios do not reach: what
its constructor refuses, a row with more members than a table has columns,
what write_released writes into a kind's released table and what it
refuses, a prune that keeps pinned images, a sweep lock that
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

from superschematic_versiongraph.canonical import uuid_canonical
from superschematic_versiongraph.errors import NameTakenError, VersionConflictError
from superschematic_versiongraph.postgres import Arg, Conn, PostgresAdapter, QueryResult, psycopg_client
from superschematic_versiongraph.storage import NewCommit, NewRef, Patch, RefUpdate, ReleasedWrite, RowWrite, Tx


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


def test_write_released_refuses_a_kind_without_a_released_table() -> None:
    """A kind whose descriptor names no released table is refused, and so is
    a row that is not a JSON object, before any statement runs; a write that
    names no row and replaces none runs no statement."""
    store = PostgresAdapter(DESCRIPTOR).storage(_NoStatements())
    with pytest.raises(ValueError, match='postgres: kind "step" keeps no released table'):
        store.transact(lambda tx: tx.write_released("step", ReleasedWrite("1", "1", 1, True)))
    with pytest.raises(ValueError, match='postgres: unknown kind "stew"'):
        store.transact(lambda tx: tx.write_released("stew", ReleasedWrite("1", "1", 1, True)))
    with pytest.raises(ValueError, match="postgres: a released part row is a JSON object"):
        store.transact(lambda tx: tx.write_released("part", ReleasedWrite("1", "1", 1, False, ["[]"])))
    with pytest.raises(ValueError, match="postgres: released part column parent_key: "):
        row = '{"entity_key": "Mix", "parent_key": "not a uuid", "rank": "V", "heading": "Mix"}'
        store.transact(lambda tx: tx.write_released("part", ReleasedWrite("1", "1", 1, False, [row])))
    store.transact(lambda tx: tx.write_released("part", ReleasedWrite("1", "1", 1)))


ROOT = "00000000-0000-0000-0000-000000000001"
INSERT_ROOT = (
    f"INSERT INTO recipe (id, title, created_by) VALUES ('{ROOT}', 'Bread', '00000000-0000-0000-0000-000000000002')"
)
ACTOR = "2"


def _main(tx: Tx, name: str = "main") -> Any:
    return tx.create_ref(NewRef("1", None, None, name, ACTOR))


def _part(key: str, heading: str, rank: str = "V", **extra: Any) -> str:
    """A part row of the fixture, as a release hands it to write_released."""
    return json.dumps({"entity_key": key, "parent_key": None, "rank": rank, "heading": heading, **extra})


@requires_database
def test_write_released_replaces_upserts_and_deletes_a_roots_rows(scratch_schema: Callable[[str], Scratch]) -> None:
    # write_released writes a release's rows of one kind of one root into
    # the kind's released table: with replace it clears the root's rows
    # first, it removes the entities delete names, and it writes each upsert
    # by its entity key with the release's commit and sequence, each content
    # column as the kind's own table takes it. A row it does not name stays
    # as an earlier release wrote it, and so do another root's rows.
    scratch = scratch_schema("vg_adapter_py")
    connection = scratch.connect()
    connection.execute(INSERT_ROOT)
    connection.execute(INSERT_ROOT.replace(ROOT, ROOT[:-1] + "3").replace("Bread", "Soup"))
    storage = PostgresAdapter(DESCRIPTOR).storage(psycopg_client(connection))

    def commits(tx: Tx) -> Any:
        bread = tx.create_ref(NewRef("1", None, None, "main", ACTOR))
        soup = tx.create_ref(NewRef("3", None, None, "main", ACTOR))
        first = tx.insert_commit(NewCommit(bread.root, bread.id, None, "", 0, "h", 1, ACTOR))
        second = tx.insert_commit(NewCommit(bread.root, bread.id, first.id, "", 0, "h", 2, ACTOR))
        other = tx.insert_commit(NewCommit(soup.root, soup.id, None, "", 0, "h", 1, ACTOR))
        return bread, first.id, second.id, other.id

    bread, first, second, other = storage.transact(commits)

    def write(root: str, commit: str, sequence: int, replace: bool, upsert: List[str] = [], delete: List[str] = []) -> None:
        storage.transact(lambda tx: tx.write_released("part", ReleasedWrite(root, commit, sequence, replace, upsert, delete)))

    def released() -> List[Any]:
        rows = connection.execute(
            'SELECT recipe_id::text, entity_key::text, heading, rank, parent_key::text, "sequence", commit_id::text '
            "FROM part_released ORDER BY recipe_id, heading"
        ).fetchall()
        return [
            (uuid_canonical(r[0]), uuid_canonical(r[1]), r[2], r[3], r[4] and uuid_canonical(r[4]), r[5], uuid_canonical(r[6]))
            for r in rows
        ]

    # A first release: the whole tree. A row arrives as the engine read it,
    # role and excluded columns among its members.
    whole = json.dumps(
        {
            "_version": 3,
            "deleted_on_ref": False,
            "entity_key": "Knead",
            "heading": "Knead",
            "id": "Row",
            "parent_key": "Mix",
            "rank": "V",
            "recipe_id": "1",
            "ref_id": bread.id,
        }
    )
    write("1", first, 1, True, [_part("Mix", "Mix", "F"), whole, _part("Bake", "Bake", "k")])
    write("3", other, 1, True, [_part("Mix", "Make the stock")])
    soup_rows = [("3", "Mix", "Make the stock", "V", None, 1, other)]
    assert released() == [
        ("1", "Bake", "Bake", "k", None, 1, first),
        ("1", "Knead", "Knead", "V", "Mix", 1, first),
        ("1", "Mix", "Mix", "F", None, 1, first),
    ] + soup_rows

    # A later release: a changed row and an added one take its commit and
    # sequence, a deleted one goes, and the row it does not name stays.
    write("1", second, 2, False, [_part("Mix", "Mix well", "F"), _part("Rest", "Rest", "c")], ["Bake", "Missing"])
    assert released() == [
        ("1", "Knead", "Knead", "V", "Mix", 1, first),
        ("1", "Mix", "Mix well", "F", None, 2, second),
        ("1", "Rest", "Rest", "c", None, 2, second),
    ] + soup_rows
    # A row keeps the id the table gave it when a release writes it again.
    ids = connection.execute("SELECT count(DISTINCT id), count(*) FROM part_released").fetchall()
    assert ids == [(4, 4)]

    # A replace writes the rows it is given over whatever the table holds
    # of the root, and with none leaves the root no row.
    write("1", first, 1, True, [_part("Rest", "Rest", "c")])
    assert released() == [("1", "Rest", "Rest", "c", None, 1, first)] + soup_rows
    write("1", first, 1, True)
    assert released() == soup_rows

    # A kind that keeps no released table is refused, and writes nothing.
    with pytest.raises(ValueError, match='postgres: kind "step" keeps no released table'):
        write_step = ReleasedWrite("1", first, 1, True, ['{"entity_key": "Mix", "position": 1}'])
        storage.transact(lambda tx: tx.write_released("step", write_step))
    assert released() == soup_rows


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
