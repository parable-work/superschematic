"""The sweeper's rules the sweep scenario cannot reach, since a scenario runs
one step at a time: run_sweeper's passes are skipped while another
transaction holds the sweep lock and stop when its stop event is set, and a
pass leaves an idle change set a write reached after the pass read it. The
Python counterpart of the Go engine's sweeper_test.go."""

import queue
import threading
from datetime import timedelta
from typing import Any, Callable, List, Optional, Tuple

import pytest
from support import DESCRIPTOR, Scratch, hyphenated, requires_database

from superschematic_versiongraph.engine import Engine, KindEdits, SweepOptions, SweepReport
from superschematic_versiongraph.errors import NotFoundError
from superschematic_versiongraph.postgres import PostgresAdapter, psycopg_client
from superschematic_versiongraph.storage import Tx

ACTOR = "Cook"


class NoStorage:
    """Storage that counts the transactions asked of it and refuses each."""

    def __init__(self) -> None:
        self.transactions = 0

    def transact(self, fn: Callable[[Tx], Any]) -> Any:
        self.transactions += 1
        raise RuntimeError("no storage")


def test_run_sweeper_refuses_an_interval_that_is_not_positive() -> None:
    storage = NoStorage()
    engine = Engine(DESCRIPTOR, storage)
    passes: List[Any] = []
    for interval in (timedelta(0), timedelta(seconds=-1)):
        with pytest.raises(ValueError, match="must be positive"):
            engine.run_sweeper(interval, SweepOptions(ACTOR), lambda report, error: passes.append(error))
    assert (storage.transactions, passes) == (0, [])


def test_run_sweeper_with_its_stop_set_runs_no_pass() -> None:
    # A sweeper whose stop is already set returns before a pass: it opens no
    # transaction and reports nothing.
    storage = NoStorage()
    stop = threading.Event()
    stop.set()
    passes: List[Any] = []
    Engine(DESCRIPTOR, storage).run_sweeper(
        timedelta(hours=1), SweepOptions(ACTOR), lambda report, error: passes.append(error), stop
    )
    assert (storage.transactions, passes) == (0, [])


def test_run_sweeper_reports_a_failed_pass_and_keeps_going() -> None:
    # A pass that fails reaches on_pass as its error and does not stop the
    # sweeper, which stops when its stop is set.
    storage = NoStorage()
    stop = threading.Event()
    errors: List[Optional[BaseException]] = []

    def on_pass(report: Optional[SweepReport], error: Optional[BaseException]) -> None:
        errors.append(error)
        if len(errors) == 3:
            stop.set()

    Engine(DESCRIPTOR, storage).run_sweeper(timedelta(milliseconds=5), SweepOptions(ACTOR), on_pass, stop)
    assert storage.transactions == 3
    assert [str(e) for e in errors] == ["no storage"] * 3


def _open(scratch: Scratch) -> Tuple[PostgresAdapter, Engine]:
    adapter = PostgresAdapter(DESCRIPTOR)
    engine = Engine(
        DESCRIPTOR, adapter.storage(psycopg_client(scratch.connect())), schema_epoch=1, snapshot_every=3
    )
    return adapter, engine


@requires_database
def test_run_sweeper_skips_while_the_lock_is_held(scratch_schema: Callable[[str], Scratch]) -> None:
    # run_sweeper runs while another transaction holds the graph's sweep
    # lock: its passes are skipped until the lock is released, the next pass
    # sweeps, and setting its stop ends it.
    scratch = scratch_schema("vg_sweeper_py")
    adapter, engine = _open(scratch)
    holder = scratch.connect()
    holder.execute("BEGIN")
    assert adapter.storage(psycopg_client(holder)).transact(lambda tx: tx.sweep_lock()) is True

    passes: "queue.Queue[Tuple[Optional[SweepReport], Optional[BaseException]]]" = queue.Queue()
    stop = threading.Event()
    sweeper = threading.Thread(
        target=engine.run_sweeper,
        args=(timedelta(milliseconds=5), SweepOptions(ACTOR), lambda report, error: passes.put((report, error)), stop),
    )
    sweeper.start()
    try:

        def next_pass() -> SweepReport:
            report, error = passes.get(timeout=10)
            assert error is None, error
            assert report is not None
            return report

        for _ in range(3):
            assert next_pass().skipped is True
        holder.execute("ROLLBACK")
        while next_pass().skipped:
            # Passes that started before the rollback may still be skipped.
            pass
    finally:
        stop.set()
        sweeper.join(10)
    assert not sweeper.is_alive()


class RacingTx:
    """A transaction whose idle_drafts, after it reads the idle change sets,
    lets a write land on one of them through another connection."""

    def __init__(self, tx: Tx, write: Callable[[], None]) -> None:
        self._tx = tx
        self._write = write

    def idle_drafts(self, idle: timedelta) -> Any:
        refs = self._tx.idle_drafts(idle)
        self._write()
        return refs

    def __getattr__(self, name: str) -> Any:
        return getattr(self._tx, name)


class RacingStorage:
    def __init__(self, storage: Any, write: Callable[[], None]) -> None:
        self._storage = storage
        self._write = write

    def transact(self, fn: Callable[[Tx], Any]) -> Any:
        return self._storage.transact(lambda tx: fn(RacingTx(tx, self._write)))


@requires_database
def test_a_sweep_skips_an_idle_draft_written_during_the_pass(scratch_schema: Callable[[str], Scratch]) -> None:
    # A write lands on an idle change set after the pass read it as idle and
    # before the pass discards it. The pass leaves that change set live,
    # since it is no longer idle, and the rest of the pass lands: it discards
    # the other idle change set and collects a discarded ref's rows.
    scratch = scratch_schema("vg_sweeper_py")
    adapter, engine = _open(scratch)
    sql = scratch.connect()
    bread = "00000000-0000-0000-0000-00000000012a"
    sql.execute(
        "INSERT INTO recipe (id, title, created_by) VALUES (%s::uuid, 'Bread', %s::uuid)",
        [bread, "00000000-0000-0000-0000-000000000001"],
    )
    main = engine.create_primary(ACTOR, bread, "main")
    idle = engine.branch(ACTOR, main.id, "idle")
    busy = engine.branch(ACTOR, main.id, "busy")
    dropped = engine.branch(ACTOR, main.id, "dropped")
    mix = {"step": KindEdits(upsert=['{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}'])}
    saved = engine.save(ACTOR, dropped.id, dropped.version, mix)
    engine.discard(ACTOR, dropped.id, saved.ref.version)
    sql.execute(
        "UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id = ANY(%s::text[]::uuid[])",
        [[hyphenated(idle.id), hyphenated(busy.id)]],
    )
    sql.execute(
        "UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = %s::uuid", [hyphenated(dropped.id)]
    )

    # The write goes through another connection, in a transaction of its own
    # that commits while the pass's is open.
    writer = engine.with_storage(adapter.storage(psycopg_client(scratch.connect())))
    wrote: List[bool] = []

    def write() -> None:
        ref = writer.storage.transact(lambda tx: tx.read_ref(busy.id))
        writer.save(ACTOR, busy.id, ref.version, mix)
        wrote.append(True)

    racing = engine.with_storage(RacingStorage(engine.storage, write))
    report = racing.sweep(SweepOptions(ACTOR, abandon_after=timedelta(hours=48)))
    assert wrote == [True]
    assert (report.abandoned, report.collected_refs, report.collected_rows.get("step")) == (1, 1, 1)
    with pytest.raises(NotFoundError):
        engine.compose(idle.id)
    assert len(engine.compose(busy.id).tree.get("step", [])) == 1
