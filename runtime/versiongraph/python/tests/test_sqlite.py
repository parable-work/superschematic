"""The SQLite adapter's own rules, which the scenarios do not reach or reach
only in passing: the version fences of refs and release pointers, a taken
name and a discarded ref's name free again, the history it writes, STRICT
tables and foreign keys, two graphs in one file, the name function, the
clock, its ids, the write lock a second connection waits on, savepoints and
the caller's transaction, a number's digits end to end, the canonical
vectors as a round trip, and the sqlite3 binding: what it refuses and what
it returns. The Python counterpart of the TypeScript package's
test/sqlite-cases.ts. None needs a database server."""

import json
import re
import sqlite3
import sys
import threading
import time
from datetime import timedelta
from pathlib import Path
from typing import Any, Callable, Dict, Iterator, List, Optional

import pytest
from support import DESCRIPTOR, TESTDATA

import superschematic_versiongraph as vg
from superschematic_versiongraph.canonical import (
    CanonicalError,
    canonical_row,
    canonical_value,
    uuid_canonical,
    uuid_hyphenated,
)
from superschematic_versiongraph.engine import CommitOptions, Engine, KindEdits, SweepOptions
from superschematic_versiongraph.errors import NameTakenError, NotFoundError, VersionConflictError, error_code
from superschematic_versiongraph.exactjson import dumps, loads
from superschematic_versiongraph.sqlite import (
    MIN_SQLITE_VERSION,
    SQLITE_TABLES,
    Client,
    SqliteAdapter,
    default_table_name,
    is_unique_violation,
    micros_to_date_time,
    sqlite_client,
    sqlite_layout,
)
from superschematic_versiongraph.storage import NewCommit, NewRef, Patch, Pin, RefUpdate, ReleaseWrite, RowWrite, Tx

GRAPH = "recipe"
COOK = "Cook"
BREAD = "Bread"
DAY = 86_400_000_000
MAX_SAFE = (1 << 53) - 1

# Each kind's columns' value classes, as the fixture declares them.
COLUMNS: Dict[str, Dict[str, str]] = {k["kind"]: k["columns"] for k in json.loads(DESCRIPTOR)["kinds"]}

# SQLite's extended result codes the tests read, and the words SQLite gives
# each, which a Python before 3.11, whose errors carry no code, is checked by.
SQLITE_BUSY = (5, "database is locked")
SQLITE_CONSTRAINT_UNIQUE = (2067, "UNIQUE constraint failed")
SQLITE_CONSTRAINT_FOREIGNKEY = (787, "FOREIGN KEY constraint failed")
SQLITE_CONSTRAINT_DATATYPE = (3091, "cannot store")


def assert_sqlite_error(error: BaseException, code: Any, what: str = "") -> None:
    """Asserts error is SQLite's error of code: by its extended result code
    from Python 3.11, and by the words SQLite gives it before."""
    assert isinstance(error, sqlite3.Error), f"{what}: {error!r}"
    if sys.version_info >= (3, 11):
        assert error.sqlite_errorcode == code[0], f"{what}: {error!r}"
    else:
        assert code[1] in str(error), f"{what}: {error!r}"


def assert_canonical_row(kind: str, row: str) -> None:
    """Asserts a row the adapter returned is a canonical row of its kind: the
    canonical rules leave it as it is, members sorted."""
    assert canonical_row(COLUMNS[kind], row) == row, f"a {kind} row is canonical: {row}"


def assert_canonical_id(id: Any, what: str) -> None:
    """Asserts id is base62 of a version-4 UUID."""
    assert isinstance(id, str), f"{what} is text"
    assert uuid_canonical(id) == id, f"{what} {id} is in its canonical form"
    assert re.fullmatch(
        r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}", uuid_hyphenated(id)
    ), f"{what} {id} is a version-4 UUID"


def member(row: str, name: str) -> Optional[str]:
    """A JSON object's member, as its JSON text."""
    value = loads(row)
    return dumps(value[name]) if name in value else None


def step_row(key: Optional[str], instruction: str, **extra: Any) -> str:
    """A step row of the fixture, as an upsert's JSON text."""
    return json.dumps({"entity_key": key, "position": 1, "instruction": instruction, "timings": {}, **extra})


def history(connection: sqlite3.Connection, table: str, id: str) -> List[Any]:
    return connection.execute(
        f"SELECT _version, operation, data, recorded_at FROM {table} WHERE id = ?1 ORDER BY _version", [id]
    ).fetchall()


def count(connection: sqlite3.Connection, sql: str, *args: Any) -> int:
    return connection.execute(sql, args).fetchone()[0]


class Setup:
    """A database with the layout, and the adapter's storage over it."""

    def __init__(self, connection: sqlite3.Connection, client: Client, adapter: SqliteAdapter) -> None:
        self.connection = connection
        self.client = client
        self.adapter = adapter
        self.storage = adapter.storage(client)
        self.engine = Engine(DESCRIPTOR, self.storage, schema_epoch=1, snapshot_every=3)


@pytest.fixture
def open_db() -> Iterator[Callable[..., sqlite3.Connection]]:
    """Opens connections in autocommit, each closed when the test ends."""
    opened: List[sqlite3.Connection] = []

    def connect(path: str = ":memory:", **kwargs: Any) -> sqlite3.Connection:
        kwargs.setdefault("isolation_level", None)
        connection = sqlite3.connect(path, **kwargs)
        opened.append(connection)
        return connection

    yield connect
    for connection in opened:
        connection.close()


@pytest.fixture
def setup(open_db: Callable[..., sqlite3.Connection]) -> Callable[..., Setup]:
    """Makes a database with the layout, in memory or at a path, and the
    adapter over it, with the adapter's options."""

    def make(path: str = ":memory:", **options: Any) -> Setup:
        connection = open_db(path)
        client = sqlite_client(connection)
        adapter = SqliteAdapter(DESCRIPTOR, graph=options.pop("graph", GRAPH), **options)
        adapter.create_tables(client)
        return Setup(connection, client, adapter)

    return make


def ref_and_commit(tx: Tx, root: str = BREAD, name: str = "main") -> Any:
    """A ref and a tagged commit on it, written through the adapter."""
    ref = tx.create_ref(NewRef(root, None, None, name, COOK))
    commit = tx.insert_commit(NewCommit(root, ref.id, None, "", 1, "0" * 64, tx.next_sequence(root), COOK)).id
    return ref, commit


def test_a_refs_version_fences_its_update_and_its_discard(setup: Callable[..., Setup]) -> None:
    """update_ref and discard_ref at another version than the ref's are
    version_conflict, and a refused discard leaves the transaction usable."""
    s = setup()
    ref = s.storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))
    assert ref.version == 1
    moved = s.storage.transact(lambda tx: tx.update_ref(RefUpdate(ref.id, 1, None, None, False, COOK)))
    assert moved.version == 2
    with pytest.raises(VersionConflictError):
        s.storage.transact(lambda tx: tx.update_ref(RefUpdate(ref.id, 1, None, None, True, COOK)))
    assert s.storage.transact(lambda tx: tx.read_ref(ref.id)).sealed is False

    def refused_then_write(tx: Tx) -> Any:
        with pytest.raises(VersionConflictError):
            tx.discard_ref(ref.id, 1, COOK)
        # The transaction goes on after the refused discard, and commits.
        return tx.create_ref(NewRef(BREAD, ref.id, None, "draft", COOK))

    draft = s.storage.transact(refused_then_write)
    assert s.storage.transact(lambda tx: tx.read_ref(draft.id)).name == "draft"
    s.storage.transact(lambda tx: tx.discard_ref(ref.id, 2, COOK))
    discarded = s.storage.transact(lambda tx: tx.read_ref(ref.id))
    assert (discarded.discarded, discarded.version) == (True, 3)
    with pytest.raises(VersionConflictError):
        s.storage.transact(lambda tx: tx.discard_ref(ref.id, 3, COOK))
    with pytest.raises(NotFoundError):
        s.storage.transact(lambda tx: tx.read_ref("Missing"))


def test_a_release_pointers_version_fences_its_first_write_and_every_move(setup: Callable[..., Setup]) -> None:
    s = setup()
    _, commit = s.storage.transact(ref_and_commit)
    first = s.storage.transact(lambda tx: tx.write_release(ReleaseWrite(BREAD, commit, 0, COOK)))
    assert first.version == 1
    for version in (0, 2):
        with pytest.raises(VersionConflictError):
            s.storage.transact(lambda tx: tx.write_release(ReleaseWrite(BREAD, commit, version, COOK)))
    moved = s.storage.transact(lambda tx: tx.write_release(ReleaseWrite(BREAD, commit, 1, "Baker")))
    assert (moved.id, moved.version) == (first.id, 2)
    assert s.storage.transact(lambda tx: tx.read_release(BREAD)).version == 2
    with pytest.raises(NotFoundError):
        s.storage.transact(lambda tx: tx.read_release("Soup"))
    # The pointer's history is the release log: each write's image at its
    # version.
    log = history(s.connection, '"graph_release_history"', first.id)
    assert [(row[0], row[1], member(row[2], "updated_by")) for row in log] == [
        (1, "INSERT", '"Cook"'),
        (2, "UPDATE", '"Baker"'),
    ]


def test_a_roots_live_ref_names_are_distinct_and_a_discarded_refs_name_is_free_again(
    setup: Callable[..., Setup],
) -> None:
    s = setup()
    main = s.engine.create_primary(COOK, BREAD, "main")
    with pytest.raises(NameTakenError) as taken:
        s.engine.create_primary(COOK, BREAD, "main")
    assert error_code(taken.value) == "name_taken"
    draft = s.engine.branch(COOK, main.id, "draft")
    with pytest.raises(NameTakenError):
        s.engine.branch(COOK, main.id, "draft")
    # Another root takes the name.
    s.engine.create_primary(COOK, "Soup", "main")
    s.engine.discard(COOK, draft.id, draft.version)
    assert s.engine.branch(COOK, main.id, "draft").version == 1


def test_history_versions_images_a_deletes_actor_and_the_columns_history_leaves_out(
    setup: Callable[..., Setup],
) -> None:
    s = setup()
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    first = s.engine.save(
        COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix", scratch="note to self")])}
    )
    second = s.engine.save("Baker", draft.id, first.ref.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix well")])})
    row = second.saved["step"][0]
    assert member(row, "_version") == "2"
    # A column the update leaves out keeps its value on the live row.
    assert member(row, "scratch") == '"note to self"'
    id = loads(row)["id"]
    s.engine.save("Janitor", draft.id, second.ref.version, {"step": KindEdits(unset=["Mix"])})
    images = history(s.connection, '"graph_member_history"', id)
    assert [(image[0], image[1]) for image in images] == [(1, "INSERT"), (2, "UPDATE"), (3, "DELETE")]
    for image in images:
        assert_canonical_row("step", image[2])
        assert member(image[2], "scratch") is None, "an image leaves scratch out"
        assert member(image[2], "_version") == str(image[0])
    # An update's image is the row as stored, less scratch.
    updated = loads(row)
    del updated["scratch"]
    assert images[1][2] == dumps(updated)
    # The delete's image is the row at its version plus 1, naming the
    # delete's actor in the kind's actor column, updated_by.
    deleted = loads(row)
    del deleted["scratch"]
    deleted["_version"] = loads("3")
    deleted["updated_by"] = "Janitor"
    assert images[2][2] == dumps(deleted)
    assert s.storage.transact(lambda tx: tx.rows("step", draft.id)) == []
    # A kind with no actor column keeps the row's values in its delete's
    # image.
    version = s.storage.transact(lambda tx: tx.read_ref(draft.id)).version
    whisk = s.engine.save(COOK, draft.id, version, {"utensil": KindEdits(upsert=['{"entity_key": "Whisk", "name": "whisk"}'])})
    utensil = whisk.saved["utensil"][0]
    s.engine.save("Janitor", draft.id, whisk.ref.version, {"utensil": KindEdits(unset=["Whisk"])})
    gone = history(s.connection, '"graph_member_history"', loads(utensil)["id"])
    kept = loads(utensil)
    kept["_version"] = loads("2")
    assert gone[1][2] == dumps(kept)
    # A ref's history: its insert and each update at its version, the
    # discard's naming its actor.
    s.engine.discard("Janitor", draft.id, s.storage.transact(lambda tx: tx.read_ref(draft.id)).version)
    ref_images = history(s.connection, '"graph_ref_history"', draft.id)
    assert [(image[0], image[1]) for image in ref_images] == [
        (version, "INSERT" if version == 1 else "UPDATE") for version in range(1, 8)
    ]
    last = ref_images[-1][2]
    assert member(last, "deleted_by") == '"Janitor"'
    assert member(last, "_version") == "7"
    assert re.fullmatch(r'"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z"', member(last, "deleted_at") or "")


def test_prune_keeps_each_rows_newest_and_every_pinned_image_at_most_a_batch_and_nothing_without_retention(
    setup: Callable[..., Setup],
) -> None:
    now = [1_800_000_000_000_000]
    s = setup(clock=lambda: now[0])
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    for instruction in ("Knead", "Knead well", "Knead hard"):
        draft = s.engine.save(
            COOK,
            draft.id,
            draft.version,
            {
                "step": KindEdits(upsert=[step_row("Knead", instruction)]),
                "utensil": KindEdits(upsert=[json.dumps({"entity_key": "Whisk", "name": instruction})]),
            },
        ).ref
    # The commit pins version 3 of each; versions 1 and 2 are unpinned.
    draft = s.engine.commit(COOK, draft.id, draft.version).ref
    s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Knead", "Knead softly")])})

    def prune(kind: str, days: int, batch: int) -> int:
        return s.storage.transact(lambda tx: tx.prune(kind, days, batch))

    def versions(kind: str) -> List[int]:
        rows = s.connection.execute('SELECT _version FROM "graph_member_history" WHERE kind = ?1 ORDER BY _version', [kind])
        return [row[0] for row in rows]

    # Within the step kind's 365 days, nothing goes.
    now[0] += 364 * DAY
    assert prune("step", 0, 0) == 0
    # An argument other than 0 is the retention, in days.
    assert prune("step", 400, 0) == 0
    now[0] += 2 * DAY
    assert prune("step", 400, 0) == 0
    # Past the declared 365 days: versions 1 and 2, a batch at a time.
    assert prune("step", 0, 1) == 1
    assert versions("step") == [2, 3, 4]
    assert prune("step", 0, 0) == 1
    assert versions("step") == [3, 4], "the pinned version 3 and the newest, version 4, stay"
    assert prune("step", 1, 0) == 0
    # utensil declares no retention, so nothing of it goes, whatever the
    # argument.
    assert prune("utensil", 0, 0) == 0
    assert prune("utensil", 1, 0) == 0
    assert versions("utensil") == [1, 2, 3]
    with pytest.raises(ValueError, match="a batch is a whole number"):
        prune("step", 0, -1)


def test_prune_keeps_an_image_exactly_its_retention_old_and_takes_the_oldest_first(setup: Callable[..., Setup]) -> None:
    start = 1_800_000_000_000_000
    now = [start]
    s = setup(clock=lambda: now[0])
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    for instruction in ("Mix", "Mix well"):
        draft = s.engine.save(
            COOK,
            draft.id,
            draft.version,
            {"step": KindEdits(upsert=[step_row(key, instruction) for key in ("Mix", "Rest", "Bake")])},
        ).ref

    def prune(batch: int) -> int:
        return s.storage.transact(lambda tx: tx.prune("step", 0, batch))

    def left() -> List[str]:
        rows = s.connection.execute("SELECT id FROM \"graph_member_history\" WHERE kind = 'step' AND _version = 1")
        return sorted(row[0] for row in rows)

    # Each row's version 1 is prunable once it is past 365 days old, and not
    # at exactly 365 days.
    now[0] = start + 365 * DAY
    assert prune(0) == 0
    # Make the row with the greatest id the oldest by a microsecond more
    # than the next, so oldest first and id order disagree.
    ids = left()
    for id, by in ((ids[2], 2), (ids[1], 1)):
        s.connection.execute(
            'UPDATE "graph_member_history" SET recorded_at = recorded_at - ?2 WHERE id = ?1 AND _version = 1', [id, by]
        )
    assert prune(1) == 1
    assert left() == [ids[0], ids[1]], "the oldest went first"
    assert prune(0) == 1
    assert left() == [ids[0]], "an image exactly 365 days old stays"
    now[0] += 1
    assert prune(0) == 1
    assert left() == []


def test_the_adapter_writes_a_rows_role_and_audit_columns_and_keeps_or_nulls_what_it_lacks(
    setup: Callable[..., Setup],
) -> None:
    """The adapter writes a row's ref, root, tombstone, actor and time, never
    its id or version; a column an update lacks keeps its value, and one an
    insert lacks is null."""
    s = setup(clock=lambda: 1_800_000_000_000_000)
    ref, _ = s.storage.transact(ref_and_commit)

    def write(row: str, tombstone: bool = False, actor: str = COOK) -> str:
        return s.storage.transact(lambda tx: tx.upsert_row("step", RowWrite(ref.id, BREAD, row, tombstone, actor)))

    inserted = write(
        json.dumps(
            {
                "entity_key": "Mix",
                "id": "Elsewhere",
                "_version": 7,
                "ref_id": "Other",
                "recipe_id": "Soup",
                "deleted_on_ref": True,
                "created_at": "2000-01-01T00:00:00Z",
                "created_by": "Somebody",
                "position": 1,
                "instruction": "Mix",
                "timings": {},
            }
        )
    )
    assert_canonical_row("step", inserted)
    assert member(inserted, "id") != '"Elsewhere"'
    assert member(inserted, "_version") == "1"
    assert member(inserted, "ref_id") == json.dumps(ref.id)
    assert member(inserted, "recipe_id") == '"Bread"'
    assert member(inserted, "deleted_on_ref") == "false"
    assert member(inserted, "created_at") == '"2027-01-15T08:00:00Z"'
    assert member(inserted, "created_by") == '"Cook"'
    assert member(inserted, "updated_by") == '"Cook"'
    # A column the insert lacks is stored null, not left out, as the row
    # reads.
    assert member(inserted, "scratch") == "null"
    [stored] = s.connection.execute('SELECT data FROM "graph_member" WHERE id = ?1', [loads(inserted)["id"]]).fetchone()
    assert member(stored, "scratch") == "null"
    updated = write(json.dumps({"entity_key": "Mix", "instruction": "Stir", "created_by": "Somebody"}), True, "Baker")
    assert_canonical_row("step", updated)
    assert s.storage.transact(lambda tx: tx.rows("step", ref.id)) == [updated]
    assert member(updated, "id") == member(inserted, "id")
    assert member(updated, "_version") == "2"
    assert member(updated, "deleted_on_ref") == "true"
    # A column the update lacks keeps its value, and the creation audit
    # stays.
    assert member(updated, "position") == "1"
    assert member(updated, "created_by") == '"Cook"'
    assert member(updated, "updated_by") == '"Baker"'
    # A row without an entity key is a new entity.
    fresh = write(step_row(None, "Rest"))
    assert member(fresh, "entity_key") != member(inserted, "entity_key")
    assert_canonical_id(loads(fresh)["entity_key"], "a generated entity key")
    # A column the descriptor does not declare is refused.
    with pytest.raises(ValueError, match="does not declare"):
        write(json.dumps({"entity_key": "Mix", "flavour": "salt"}))


def test_after_a_kind_gains_a_column_its_old_rows_read_it_as_null_and_its_old_images_read_as_stored(
    setup: Callable[..., Setup],
) -> None:
    s = setup()
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    saved = s.engine.save(
        COOK,
        draft.id,
        draft.version,
        {
            "step": KindEdits(upsert=[step_row("Mix", "Mix")]),
            "utensil": KindEdits(upsert=['{"entity_key": "Whisk", "name": "whisk"}']),
        },
    )
    committed = s.engine.commit(COOK, draft.id, saved.ref.version)
    # The schema's next version: utensil gains color, and step gains memo,
    # which its history leaves out.
    d = json.loads(DESCRIPTOR)
    kinds = {k["kind"]: k for k in d["kinds"]}
    kinds["utensil"]["columns"]["color"] = "string"
    kinds["step"]["columns"]["memo"] = "string"
    kinds["step"]["excluded"].append("memo")
    kinds["step"]["history"]["exclude"].append("memo")
    later = json.dumps(d)
    storage = SqliteAdapter(later, graph=GRAPH).storage(s.client)
    engine = Engine(later, storage, schema_epoch=1, snapshot_every=3)
    [whisk] = storage.transact(lambda tx: tx.rows("utensil", draft.id))
    [mix] = storage.transact(lambda tx: tx.rows("step", draft.id))
    assert member(whisk, "color") == "null"
    assert member(mix, "memo") == "null"
    assert canonical_row(kinds["utensil"]["columns"], whisk) == whisk
    # An image reads as it was stored, without the gained column, as a
    # Postgres history image does.
    [whisk_image] = storage.transact(lambda tx: tx.images("utensil", [Pin(loads(whisk)["id"], 1)]))
    [mix_image] = storage.transact(lambda tx: tx.images("step", [Pin(loads(mix)["id"], 1)]))
    stored_image = s.connection.execute(
        'SELECT data FROM "graph_member_history" WHERE id = ?1 AND _version = 1', [loads(whisk)["id"]]
    ).fetchone()[0]
    assert whisk_image == stored_image
    assert member(whisk_image, "color") is None
    assert canonical_row(kinds["utensil"]["columns"], whisk_image) == whisk_image
    assert member(mix_image, "memo") is None
    assert member(mix_image, "scratch") is None
    # The commit's tree lacks color, and the core reads it as null: it hashes
    # as the draft's live rows, which hold it null, and as a tree whose row
    # holds it null.
    assert committed.commit is not None
    tree = engine.materialize(committed.commit.id)
    [read] = tree.tree["utensil"]
    assert member(read, "color") is None
    assert tree.content_hash == engine.compose(draft.id).content_hash
    with_null = loads(read)
    with_null["color"] = None
    request = (
        '{"descriptor":' + later + ',"tree":{"step":[' + ",".join(tree.tree["step"]) + '],"utensil":[' + dumps(with_null) + "]}}"
    )
    assert tree.content_hash == json.loads(vg.run("content_hash", request))["contentHash"]
    # An update of the old row stores the gained column, null, and its image
    # carries it.
    updated = engine.save(
        COOK, draft.id, committed.ref.version, {"utensil": KindEdits(upsert=['{"entity_key": "Whisk", "name": "big whisk"}'])}
    )
    assert member(updated.saved["utensil"][0], "color") == "null"
    stored = s.connection.execute('SELECT data FROM "graph_member" WHERE id = ?1', [loads(whisk)["id"]]).fetchone()[0]
    assert member(stored, "color") == "null"
    [updated_image] = storage.transact(lambda tx: tx.images("utensil", [Pin(loads(whisk)["id"], 2)]))
    assert member(updated_image, "color") == "null"
    assert member(updated_image, "name") == '"big whisk"'


def test_a_number_keeps_its_digits_from_the_row_written_to_every_read(setup: Callable[..., Setup]) -> None:
    """A canonical row travels as JSON text read by an exact reader: a number
    no double holds, an integer wider than 64 bits and a json member holding
    either keep every digit in the stored row, the row returned, the live
    rows read back, a history image and the committed tree."""
    s = setup()
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    score = "0.1000000000000000055511151231257827021181583404541015625"
    servings = "123456789012345678901234567890"
    remarks = '{"weight":12345678901234567890.000000000000000001}'
    row = (
        '{"entity_key":"Taste","score":' + score + ',"servings":' + servings + ',"remarks":' + remarks
        + ',"bites":[[' + servings + "]]}"
    )
    saved = s.engine.save(COOK, draft.id, draft.version, {"tasting": KindEdits(upsert=[row])})
    committed = s.engine.commit(COOK, draft.id, saved.ref.version)
    assert committed.commit is not None
    [live] = s.storage.transact(lambda tx: tx.rows("tasting", draft.id))
    [stored] = s.connection.execute('SELECT data FROM "graph_member" WHERE kind = \'tasting\'').fetchone()
    [image] = s.storage.transact(lambda tx: tx.images("tasting", [Pin(loads(live)["id"], 1)]))
    [tree] = s.engine.materialize(committed.commit.id).tree["tasting"]
    for what, text in (
        ("the row returned", saved.saved["tasting"][0]),
        ("the stored row", stored),
        ("the live row", live),
        ("the image", image),
        ("the committed tree", tree),
    ):
        assert member(text, "score") == score, what
        assert member(text, "servings") == servings, what
        assert member(text, "remarks") == '{"weight":12345678901234567890.000000000000000001}', what
        assert member(text, "bites") == "[[" + servings + "]]", what


def test_a_unique_index_backs_each_key_the_adapters_own_reads_keep_unique(setup: Callable[..., Setup]) -> None:
    s = setup()
    ref, commit = s.storage.transact(ref_and_commit)
    n = [0]

    def fresh() -> str:
        n[0] += 1
        return f"Row{n[0]}"

    twice = [
        (
            "a commit's entity in its patches",
            lambda: 'INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, '
            f"operation) VALUES ('{fresh()}', 'recipe', '{commit}', 'step', 'Mix', 'B', 1, 'ADD')",
        ),
        (
            "a commit's entity in its snapshot",
            lambda: 'INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, '
            f"entity_version) VALUES ('{fresh()}', 'recipe', '{commit}', 'step', 'Mix', 'B', 1)",
        ),
        (
            "an entity of a kind on a ref",
            lambda: 'INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) '
            f"VALUES ('{fresh()}', 'recipe', 'step', 'Mix', '{ref.id}', 'Bread', 0, 1, '{{}}')",
        ),
        (
            "a member's image at a version",
            lambda: 'INSERT INTO "graph_member_history" (history_id, graph, kind, id, _version, operation, data, '
            f"recorded_at) VALUES ('{fresh()}', 'recipe', 'step', 'Same', 1, 'INSERT', '{{}}', 0)",
        ),
        (
            "a ref's image at a version",
            lambda: 'INSERT INTO "graph_ref_history" (history_id, graph, id, _version, operation, data, recorded_at) '
            f"VALUES ('{fresh()}', 'recipe', 'Same', 1, 'INSERT', '{{}}', 0)",
        ),
        (
            "a pointer's image at a version",
            lambda: 'INSERT INTO "graph_release_history" (history_id, graph, id, _version, operation, data, recorded_at) '
            f"VALUES ('{fresh()}', 'recipe', 'Same', 1, 'INSERT', '{{}}', 0)",
        ),
        (
            "a root's release pointer",
            lambda: 'INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, '
            f"updated_by, _version) VALUES ('{fresh()}', 'recipe', 'Soup', '{commit}', 0, 'Cook', 0, 'Cook', 1)",
        ),
        (
            "a root's sequence",
            lambda: 'INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, sequence, '
            f"created_at, created_by) VALUES ('{fresh()}', 'recipe', 'Bread', '{ref.id}', 1, 'h', 7, 0, 'Cook')",
        ),
        (
            "a root's live ref name",
            lambda: 'INSERT INTO "graph_ref" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, '
            f"_version) VALUES ('{fresh()}', 'recipe', 'Soup', 'main', 0, 'Cook', 0, 'Cook', 1)",
        ),
    ]
    for what, insert in twice:
        s.connection.execute(insert())
        with pytest.raises(sqlite3.Error) as refused:
            s.connection.execute(insert())
        assert_sqlite_error(refused.value, SQLITE_CONSTRAINT_UNIQUE, what)
        assert is_unique_violation(refused.value), what


def test_every_table_is_strict(setup: Callable[..., Setup]) -> None:
    """A value of the wrong type is refused, not stored."""
    s = setup()
    with pytest.raises(sqlite3.Error) as refused:
        s.connection.execute(
            'INSERT INTO "graph_ref" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, _version) '
            "VALUES ('A', 'recipe', 'Bread', 'main', 'today', 'Cook', 0, 'Cook', 1)"
        )
    assert_sqlite_error(refused.value, SQLITE_CONSTRAINT_DATATYPE)
    for table in SQLITE_TABLES:
        [sql] = s.connection.execute(
            "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?1", [default_table_name(table)]
        ).fetchone()
        assert sql.endswith(") STRICT"), f"{table} is STRICT"


def test_foreign_keys_check_each_of_the_layouts_edges(setup: Callable[..., Setup]) -> None:
    """Each edge as an insert of a row that names its target, which holds a
    valid row of every other column: the target the edge names is taken, and
    a missing one is refused."""
    s = setup()
    ref, commit = s.storage.transact(ref_and_commit)
    n = [0]

    def fresh() -> str:
        n[0] += 1
        return f"Row{n[0]}"

    edges = [
        (
            "a ref's parent",
            ref.id,
            lambda t: 'INSERT INTO "graph_ref" (id, graph, root_id, parent_ref_id, name, created_at, created_by, '
            f"updated_at, updated_by, _version) VALUES ('{fresh()}', 'recipe', 'Bread', '{t}', '{fresh()}', 0, 'Cook', 0, "
            "'Cook', 1)",
        ),
        (
            "a ref's base",
            commit,
            lambda t: 'INSERT INTO "graph_ref" (id, graph, root_id, base_commit_id, name, created_at, created_by, '
            f"updated_at, updated_by, _version) VALUES ('{fresh()}', 'recipe', 'Bread', '{t}', '{fresh()}', 0, 'Cook', 0, "
            "'Cook', 1)",
        ),
        (
            "a ref's head",
            commit,
            lambda t: 'INSERT INTO "graph_ref" (id, graph, root_id, head_commit_id, name, created_at, created_by, '
            f"updated_at, updated_by, _version) VALUES ('{fresh()}', 'recipe', 'Bread', '{t}', '{fresh()}', 0, 'Cook', 0, "
            "'Cook', 1)",
        ),
        (
            "a commit's ref",
            ref.id,
            lambda t: 'INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, created_at, '
            f"created_by) VALUES ('{fresh()}', 'recipe', 'Bread', '{t}', 1, 'h', 0, 'Cook')",
        ),
        (
            "a commit's parent",
            commit,
            lambda t: 'INSERT INTO "graph_commit" (id, graph, root_id, ref_id, parent_commit_id, schema_epoch, '
            f"content_hash, created_at, created_by) VALUES ('{fresh()}', 'recipe', 'Bread', '{ref.id}', '{t}', 1, 'h', 0, "
            "'Cook')",
        ),
        (
            "a patch's commit",
            commit,
            lambda t: 'INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, '
            f"entity_version, operation) VALUES ('{fresh()}', 'recipe', '{t}', 'step', '{fresh()}', 'B', 1, 'ADD')",
        ),
        (
            "a snapshot entry's commit",
            commit,
            lambda t: 'INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, '
            f"entity_version) VALUES ('{fresh()}', 'recipe', '{t}', 'step', '{fresh()}', 'B', 1)",
        ),
        (
            "a release pointer's commit",
            commit,
            lambda t: 'INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, '
            f"updated_by, _version) VALUES ('{fresh()}', 'recipe', '{fresh()}', '{t}', 0, 'Cook', 0, 'Cook', 1)",
        ),
        (
            "a member's ref",
            ref.id,
            lambda t: 'INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, '
            f"data) VALUES ('{fresh()}', 'recipe', 'step', '{fresh()}', '{t}', 'Bread', 0, 1, '{{}}')",
        ),
    ]
    for edge, target, insert in edges:
        s.connection.execute(insert(target))
        with pytest.raises(sqlite3.Error) as refused:
            s.connection.execute(insert("Missing"))
        assert_sqlite_error(refused.value, SQLITE_CONSTRAINT_FOREIGNKEY, edge)


def test_the_adapter_refuses_a_write_whose_ref_or_commit_is_another_graphs_or_another_roots(
    setup: Callable[..., Setup],
) -> None:
    s = setup()
    other = SqliteAdapter(DESCRIPTOR, graph="menu").storage(s.client)
    mine_ref, mine_commit = s.storage.transact(ref_and_commit)
    soup = s.storage.transact(lambda tx: ref_and_commit(tx, "Soup"))
    theirs = other.transact(ref_and_commit)

    def refuses(what: str, pattern: str, fn: Callable[[Tx], Any]) -> None:
        with pytest.raises(Exception, match=pattern):
            s.storage.transact(fn)

    def new_commit(ref: str, parent: Optional[str]) -> NewCommit:
        return NewCommit(BREAD, ref, parent, "", 1, "0" * 64, None, COOK)

    patch = Patch("", "step", "Mix", "Row", 1, "ADD")
    from superschematic_versiongraph.storage import SnapshotEntry

    entry = SnapshotEntry("step", "Mix", "Row", 1)
    for whose, (ref, commit) in (("another graph's", (theirs[0].id, theirs[1])), ("another root's", (soup[0].id, soup[1]))):
        refuses(f"create_ref with {whose} parent", "is not a ref of root", lambda tx: tx.create_ref(NewRef(BREAD, ref, None, "x", COOK)))
        refuses(f"create_ref with {whose} base", "is not a commit of root", lambda tx: tx.create_ref(NewRef(BREAD, None, commit, "x", COOK)))
        refuses(
            f"update_ref to {whose} head",
            "is not a commit of root",
            lambda tx: tx.update_ref(RefUpdate(mine_ref.id, 1, commit, None, False, COOK)),
        )
        refuses(
            f"update_ref to {whose} base",
            "is not a commit of root",
            lambda tx: tx.update_ref(RefUpdate(mine_ref.id, 1, None, commit, False, COOK)),
        )
        refuses(f"insert_commit on {whose} ref", "is not a ref of root", lambda tx: tx.insert_commit(new_commit(ref, None)))
        refuses(
            f"insert_commit after {whose} commit",
            "is not a commit of root",
            lambda tx: tx.insert_commit(new_commit(mine_ref.id, commit)),
        )
        refuses(
            f"write_release of {whose} commit",
            "is not a commit of root",
            lambda tx: tx.write_release(ReleaseWrite(BREAD, commit, 0, COOK)),
        )
        refuses(
            f"upsert_row on {whose} ref",
            "is not a ref of root",
            lambda tx: tx.upsert_row("step", RowWrite(ref, BREAD, step_row("Mix", "Mix"), False, COOK)),
        )
    refuses("insert_patches of another graph's commit", "is not a commit of graph", lambda tx: tx.insert_patches(theirs[1], [patch]))
    refuses(
        "insert_snapshot of another graph's commit", "is not a commit of graph", lambda tx: tx.insert_snapshot(theirs[1], [entry])
    )

    # The graph's own refs and commits of the root are taken.
    def own(tx: Tx) -> None:
        tx.create_ref(NewRef(BREAD, mine_ref.id, mine_commit, "draft", COOK))
        tx.update_ref(RefUpdate(mine_ref.id, 1, mine_commit, mine_commit, False, COOK))
        tx.insert_commit(new_commit(mine_ref.id, mine_commit))
        tx.write_release(ReleaseWrite(BREAD, mine_commit, 0, COOK))
        tx.upsert_row("step", RowWrite(mine_ref.id, BREAD, step_row("Mix", "Mix"), False, COOK))
        tx.insert_patches(mine_commit, [patch])
        tx.insert_snapshot(mine_commit, [entry])

    s.storage.transact(own)
    with pytest.raises(ValueError, match='unknown kind "flavour"'):
        s.storage.transact(lambda tx: tx.rows("flavour", mine_ref.id))


def test_two_graphs_in_one_file_keep_apart(setup: Callable[..., Setup]) -> None:
    """Each graph reads, names, sequences, releases, prunes and sweeps only
    its own."""
    now = [1_800_000_000_000_000]

    def clock() -> int:
        return now[0]

    a = setup(clock=clock)
    # Graph menu keeps a day of step history, where recipe keeps 365.
    d = json.loads(DESCRIPTOR)
    next(k for k in d["kinds"] if k["kind"] == "step")["history"]["retentionDays"] = 1
    menu = json.dumps(d)
    b = SqliteAdapter(menu, graph="menu", clock=clock).storage(a.client)
    b_engine = Engine(menu, b, schema_epoch=1, snapshot_every=3)
    main = a.engine.create_primary(COOK, BREAD, "main")
    # The same root and name in the other graph is not taken.
    other = b_engine.create_primary(COOK, BREAD, "main")
    with pytest.raises(NotFoundError):
        b_engine.compose(main.id)

    def work(engine: Engine, primary: Any) -> Any:
        """Saves a step three times on a change set, commits it tagged, and
        merges it into the primary line, tagged."""
        draft = engine.branch(COOK, primary.id, "draft")
        for instruction in ("Mix", "Mix well", "Mix hard"):
            draft = engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", instruction)])}).ref
        committed = engine.commit(COOK, draft.id, draft.version, CommitOptions(tag=True))
        merged = engine.merge(COOK, draft.id, primary.id, primary.version, [], CommitOptions(tag=True))
        return committed.ref, committed.commit, merged.commit

    a_draft, a_commit, a_merged = work(a.engine, main)
    assert b.transact(lambda tx: tx.rows("step", a_draft.id)) == []
    assert b.transact(lambda tx: tx.commits()) == []
    with pytest.raises(NotFoundError):
        b_engine.materialize(a_commit.id)
    with pytest.raises(NotFoundError):
        b_engine.release(COOK, BREAD, a_commit.id, 0)
    with pytest.raises(NotFoundError):
        b_engine.history(a_draft.id)
    with pytest.raises(NotFoundError):
        b_engine.branch(COOK, a_draft.id, "x")
    assert b.transact(lambda tx: tx.next_sequence(BREAD)) == 1
    # Both graphs tag the same root, each numbering its own tags from 1.
    b_draft, b_commit, b_merged = work(b_engine, other)
    assert [a_commit.sequence, a_merged.sequence, b_commit.sequence, b_merged.sequence] == [1, 2, 1, 2]
    # A first pointer in each graph, then a move of menu's while recipe's is
    # at the same version, then a move of recipe's.
    a.engine.release(COOK, BREAD, a_commit.id, 0)
    with pytest.raises(NotFoundError):
        b_engine.released(BREAD)
    b_engine.release(COOK, BREAD, b_commit.id, 0)
    b_engine.release(COOK, BREAD, b_merged.id, 1)
    a.engine.release(COOK, BREAD, a_merged.id, 1)
    released = [a.engine.released(BREAD).release, b_engine.released(BREAD).release]
    assert [(r.commit, r.version) for r in released] == [(a_merged.id, 2), (b_merged.id, 2)]
    # A write through one graph's ref from the other is refused, and a
    # removal of its rows removes none.
    with pytest.raises(ValueError, match="is not a ref of root"):
        b.transact(lambda tx: tx.upsert_row("step", RowWrite(a_draft.id, BREAD, step_row("Mix", "Mix"), False, COOK)))
    assert b.transact(lambda tx: tx.remove_ref_rows("step", a_draft.id, COOK)) == 0
    assert b.transact(lambda tx: tx.remove_row("step", a_draft.id, "Mix", COOK)) is False
    assert len(a.storage.transact(lambda tx: tx.rows("step", a_draft.id))) == 1

    # Thirty days on, menu's sweep prunes its own step images past its day,
    # and none of recipe's, which keeps 365.
    def images(graph: str) -> int:
        return count(a.connection, 'SELECT count(*) FROM "graph_member_history" WHERE graph = ?1', graph)

    kept = images("recipe")
    now[0] += 30 * DAY
    assert b_engine.sweep(SweepOptions(COOK)).pruned == {"step": 2}
    assert images("recipe") == kept
    assert a.engine.sweep(SweepOptions(COOK)).pruned == {}
    assert b_engine.compose(other.id).content_hash == a.engine.compose(main.id).content_hash

    # Each graph discards its draft, and a week and a day on, past the grace,
    # menu's sweep collects its own draft's rows and leaves recipe's, which
    # recipe's own sweep collects.
    def rows_of(graph: str, ref: str) -> int:
        return count(a.connection, 'SELECT count(*) FROM "graph_member" WHERE graph = ?1 AND ref_id = ?2', graph, ref)

    # Each graph's sweep reads only its own idle change sets.
    for storage, draft in ((a.storage, a_draft), (b, b_draft)):
        assert [r.id for r in storage.transact(lambda tx: tx.idle_drafts(timedelta(0)))] == [draft.id]
    a.engine.discard(COOK, a_draft.id, a_draft.version)
    b_engine.discard(COOK, b_draft.id, b_draft.version)
    assert [rows_of("recipe", a_draft.id), rows_of("menu", b_draft.id)] == [1, 1]
    now[0] += 8 * DAY
    # And only its own discarded refs.
    for storage, draft in ((a.storage, a_draft), (b, b_draft)):
        assert [r.id for r in storage.transact(lambda tx: tx.discarded_refs(timedelta(days=7)))] == [draft.id]
    swept = b_engine.sweep(SweepOptions(COOK))
    assert (swept.collected_refs, swept.collected_rows) == (1, {"step": 1})
    assert [rows_of("recipe", a_draft.id), rows_of("menu", b_draft.id)] == [1, 0]
    own = a.engine.sweep(SweepOptions(COOK))
    assert (own.collected_refs, own.collected_rows) == (1, {"step": 1})
    assert rows_of("recipe", a_draft.id) == 0


def test_the_name_function_names_every_table_and_index(setup: Callable[..., Setup]) -> None:
    def table_name(name: str) -> str:
        return f"vg_{name}"

    s = setup(table_name=table_name)
    objects = s.connection.execute(
        "SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY name"
    ).fetchall()
    assert [o[1] for o in objects if o[0] == "table"] == sorted(table_name(t) for t in SQLITE_TABLES)
    for kind, name, table in objects:
        assert re.fullmatch(r"vg_[a-z_]+", name), f"{kind} {name} is named by the function"
        assert table.startswith("vg_")
    assert [o for o in objects if o[0] == "index"]
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix")])})
    assert count(s.connection, 'SELECT count(*) FROM "vg_member"') == 1
    # The layout's statements name each object with the function, and the
    # default's start with graph_.
    for statement in sqlite_layout():
        assert re.match(r'CREATE (UNIQUE )?(TABLE|INDEX) IF NOT EXISTS "graph_[a-z_]+" ', statement), statement
    with pytest.raises(TypeError, match="the table name function named"):
        sqlite_layout(lambda name: "")


def test_a_transaction_reads_the_clock_once_and_every_write_in_it_has_its_time(setup: Callable[..., Setup]) -> None:
    calls = [0]
    start = 1_800_000_000_000_000

    def clock() -> int:
        calls[0] += 1
        return start + calls[0] * 1_000_001

    def at(n: int) -> int:
        return start + n * 1_000_001

    s = setup(clock=clock)
    calls[0] = 0
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    saved = s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix"), step_row("Rest", "Rest")])})
    assert calls[0] == 3, "one read of the clock per transaction"
    for row in saved.saved["step"]:
        assert member(row, "created_at") == '"2027-01-15T08:00:03.000003Z"'
        assert member(row, "updated_at") == '"2027-01-15T08:00:03.000003Z"'
    recorded = s.connection.execute('SELECT DISTINCT recorded_at FROM "graph_member_history"').fetchall()
    assert [r[0] for r in recorded] == [at(3)]
    assert s.connection.execute('SELECT created_at, updated_at FROM "graph_ref" WHERE id = ?1', [draft.id]).fetchone() == (
        at(2),
        at(3),
    )
    committed = s.engine.commit(COOK, draft.id, saved.ref.version)
    assert calls[0] == 4
    assert committed.commit is not None and committed.commit.created_at == "2027-01-15T08:00:04.000004Z"

    # A transaction inside another is a savepoint at the outer one's time.
    def outer(tx: Tx) -> Any:
        tx.create_ref(NewRef("Soup", None, None, "outer", COOK))
        return s.storage.transact(lambda nested: nested.create_ref(NewRef("Soup", None, None, "inner", COOK)))

    inner = s.storage.transact(outer)
    assert calls[0] == 5
    assert count(s.connection, 'SELECT created_at FROM "graph_ref" WHERE id = ?1', inner.id) == at(5)
    for bad in (1.5, True, "now"):
        storage = SqliteAdapter(DESCRIPTOR, graph=GRAPH, clock=lambda bad=bad: bad).storage(s.client)  # type: ignore[misc]
        with pytest.raises(TypeError, match="not a whole number of microseconds"):
            storage.transact(lambda tx: tx.read_ref(main.id))


def test_a_transaction_reads_the_clock_once_the_write_lock_is_held(
    setup: Callable[..., Setup], open_db: Callable[..., sqlite3.Connection], tmp_path: Path
) -> None:
    path = str(tmp_path / "clock.sqlite")
    second = open_db(path, timeout=0)
    # Whether another connection is kept from the write lock when the clock
    # is read.
    held: List[bool] = []

    def clock() -> int:
        try:
            second.execute("BEGIN IMMEDIATE")
            second.execute("ROLLBACK")
            held.append(False)
        except sqlite3.Error as error:
            assert_sqlite_error(error, SQLITE_BUSY)
            held.append(True)
        return 1_800_000_000_000_000

    s = setup(path, clock=clock)
    held.clear()
    s.storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))
    s.engine.create_primary(COOK, "Soup", "main")
    assert held == [True, True]


def test_a_second_connections_begin_immediate_waits_for_the_write_lock_and_then_fails_busy(
    setup: Callable[..., Setup], open_db: Callable[..., sqlite3.Connection], tmp_path: Path
) -> None:
    path = str(tmp_path / "lock.sqlite")
    a = setup(path)
    second = open_db(path, timeout=0.3)
    b = SqliteAdapter(DESCRIPTOR, graph=GRAPH).storage(sqlite_client(second))
    ran = [False]
    outcome: Dict[str, Any] = {}

    def other(tx: Tx) -> Any:
        ran[0] = True
        return tx.read_ref("Missing")

    def hold(tx: Tx) -> None:
        tx.create_ref(NewRef(BREAD, None, None, "main", COOK))
        started = time.monotonic()
        try:
            b.transact(other)
        except sqlite3.Error as error:
            outcome["busy"] = error
        outcome["waited"] = time.monotonic() - started

    a.storage.transact(hold)
    assert "busy" in outcome, "the second connection's transaction failed"
    assert_sqlite_error(outcome["busy"], SQLITE_BUSY)
    assert outcome["waited"] >= 0.25, f"waited {outcome['waited']}s for the lock"
    # The transaction begins by taking the write lock, so even one that
    # would only read never starts.
    assert ran[0] is False
    # Once the first transaction commits, the second connection writes, and
    # reads what the first wrote.
    b.transact(lambda tx: tx.create_ref(NewRef("Soup", None, None, "main", COOK)))
    assert count(second, 'SELECT count(*) FROM "graph_ref"') == 2


def test_a_transaction_that_raises_rolls_back_and_one_inside_another_is_a_savepoint_that_rolls_back_alone(
    setup: Callable[..., Setup],
) -> None:
    s = setup()

    def create(tx: Tx, name: str) -> Any:
        return tx.create_ref(NewRef(BREAD, None, None, name, COOK))

    def gone(tx: Tx) -> None:
        create(tx, "gone")
        raise RuntimeError("rolled back")

    with pytest.raises(RuntimeError, match="rolled back"):
        s.storage.transact(gone)

    def nested(tx: Tx) -> None:
        create(tx, "inner")
        create(tx, "kept")

    def outer(tx: Tx) -> None:
        create(tx, "kept")
        with pytest.raises(NameTakenError):
            s.storage.transact(nested)
        create(tx, "after")

    s.storage.transact(outer)

    def names() -> List[str]:
        return [row[0] for row in s.connection.execute('SELECT name FROM "graph_ref" ORDER BY name')]

    assert names() == ["after", "kept"]
    assert s.connection.in_transaction is False

    # A transaction is synchronous: one whose function returns an awaitable
    # is refused, and rolls back.
    async def later() -> None:
        return None

    def awaits(tx: Tx) -> Any:
        create(tx, "awaited")
        return later()

    with pytest.raises(TypeError, match="its function returned an awaitable"):
        s.storage.transact(awaits)
    assert names() == ["after", "kept"]


def test_a_nested_transaction_that_returns_an_awaitable_is_refused_and_rolls_back_alone(setup: Callable[..., Setup]) -> None:
    s = setup()

    async def later() -> None:
        return None

    def awaits(tx: Tx) -> Any:
        tx.create_ref(NewRef(BREAD, None, None, "awaited", COOK))
        return later()

    def outer(tx: Tx) -> None:
        tx.create_ref(NewRef(BREAD, None, None, "outer", COOK))
        with pytest.raises(TypeError, match="its function returned an awaitable"):
            s.storage.transact(awaits)

    s.storage.transact(outer)
    assert [row[0] for row in s.connection.execute('SELECT name FROM "graph_ref" ORDER BY name')] == ["outer"]


def test_a_failed_savepoint_is_rolled_back_to_and_released(setup: Callable[..., Setup]) -> None:
    """A transaction begun inside another that raises rolls back to its
    savepoint and releases it, so the outer transaction holds no savepoint
    of it afterwards and goes on to commit."""
    s = setup()

    def failing(tx: Tx) -> None:
        tx.create_ref(NewRef(BREAD, None, None, "inner", COOK))
        raise RuntimeError("inner fails")

    def outer(tx: Tx) -> None:
        tx.create_ref(NewRef(BREAD, None, None, "outer", COOK))
        with pytest.raises(RuntimeError, match="inner fails"):
            s.storage.transact(failing)
        with pytest.raises(sqlite3.OperationalError, match="no such savepoint"):
            s.connection.execute("RELEASE superschematic_versiongraph_0")

    s.storage.transact(outer)
    assert [row[0] for row in s.connection.execute('SELECT name FROM "graph_ref" ORDER BY name')] == ["outer"]
    assert s.connection.in_transaction is False


def test_the_default_clock_is_the_system_clock_in_microseconds(open_db: Callable[..., sqlite3.Connection]) -> None:
    connection = open_db()
    client = sqlite_client(connection)
    adapter = SqliteAdapter(DESCRIPTOR, graph=GRAPH)
    adapter.create_tables(client)
    before = time.time_ns() // 1000
    ref = adapter.storage(client).transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))
    after = time.time_ns() // 1000
    stored = count(connection, 'SELECT created_at FROM "graph_ref" WHERE id = ?1', ref.id)
    assert before - 2_000_000 <= stored <= after + 2_000_000, (before, stored, after)


def test_a_name_with_a_double_quote_is_quoted_by_doubling_it(setup: Callable[..., Setup]) -> None:
    """The name function may give a name holding a double quote: every
    statement quotes it with the quote doubled, so the layout and every
    write and read name the table it gives."""

    def table_name(name: str) -> str:
        return f'vg"{name}'

    s = setup(table_name=table_name)
    names = [row[0] for row in s.connection.execute("SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")]
    assert names == sorted(table_name(t) for t in SQLITE_TABLES)
    assert all('"vg""' in statement for statement in sqlite_layout(table_name))
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix")])})
    assert count(s.connection, 'SELECT count(*) FROM "vg""member"') == 1
    assert len(s.engine.compose(draft.id).tree["step"]) == 1


def test_an_error_that_escapes_the_adapter_names_what_it_was_doing(setup: Callable[..., Setup]) -> None:
    """An error the adapter does not turn into an engine error keeps its
    class and gains "sqlite: <what>: " before its message, once: a value
    its column's class refuses names the column, and a refusal of SQLite's
    names the write."""
    s = setup()
    ref, commit = s.storage.transact(ref_and_commit)
    with pytest.raises(CanonicalError) as refused:
        s.storage.transact(
            lambda tx: tx.upsert_row("step", RowWrite(ref.id, BREAD, '{"entity_key":"Mix","position":"one"}', False, COOK))
        )
    assert str(refused.value).startswith('sqlite: column position: canonical: integer: "one"'), str(refused.value)
    assert str(refused.value).count("sqlite: ") == 1
    with pytest.raises(sqlite3.IntegrityError) as foreign:
        s.storage.transact(lambda tx: tx.insert_patches(commit, [Patch("", "step", "Mix", "Row", 1, "MOVE")]))
    assert str(foreign.value).startswith("sqlite: write patches: CHECK constraint failed"), str(foreign.value)
    # The engine's own errors keep their messages.
    with pytest.raises(NotFoundError) as missing:
        s.storage.transact(lambda tx: tx.read_ref("Missing"))
    assert not str(missing.value).startswith("sqlite: ")


def test_in_the_callers_transaction_the_adapters_is_a_savepoint_and_the_callers_rollback_undoes_its_writes(
    setup: Callable[..., Setup],
) -> None:
    """Bound to a connection whose transaction the caller holds, each of the
    adapter's transactions is a savepoint of it, reading the clock once: a
    failed one rolls back to its savepoint and leaves the caller's
    transaction usable, and the caller's rollback undoes every write."""
    calls = [0]

    def clock() -> int:
        calls[0] += 1
        return 1_800_000_000_000_000 + calls[0]

    s = setup(clock=clock)
    s.connection.execute("BEGIN IMMEDIATE")
    calls[0] = 0
    main = s.engine.create_primary(COOK, BREAD, "main")
    with pytest.raises(NameTakenError):
        s.engine.create_primary(COOK, BREAD, "main")
    assert calls[0] == 2

    def written_then_raised(tx: Tx) -> None:
        tx.create_ref(NewRef("Soup", None, None, "main", COOK))
        raise RuntimeError("rolled back to its savepoint")

    with pytest.raises(RuntimeError, match="rolled back to its savepoint"):
        s.storage.transact(written_then_raised)
    assert count(s.connection, "SELECT count(*) FROM \"graph_ref\" WHERE root_id = 'Soup'") == 0
    draft = s.engine.branch(COOK, main.id, "draft")
    s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row("Mix", "Mix")])})
    assert s.connection.in_transaction is True
    assert count(s.connection, 'SELECT count(*) FROM "graph_ref"') == 2
    assert count(s.connection, 'SELECT count(*) FROM "graph_member"') == 1
    s.connection.execute("ROLLBACK")
    assert count(s.connection, 'SELECT count(*) FROM "graph_ref"') == 0
    assert count(s.connection, 'SELECT count(*) FROM "graph_member_history"') == 0


def test_the_client_runs_its_transactions_one_at_a_time(open_db: Callable[..., sqlite3.Connection]) -> None:
    """The client's transactions over one connection run one at a time: two
    asked for together, from two threads, do not interleave their
    statements."""
    connection = open_db(check_same_thread=False)
    client = sqlite_client(connection)
    adapter = SqliteAdapter(DESCRIPTOR, graph=GRAPH)
    adapter.create_tables(client)
    storage = adapter.storage(client)
    order: List[str] = []
    started = threading.Event()
    failed: List[BaseException] = []

    def slow(tx: Tx) -> None:
        order.append("first begins")
        tx.create_ref(NewRef(BREAD, None, None, "first", COOK))
        started.set()
        time.sleep(0.05)
        order.append("first ends")

    def fast(tx: Tx) -> None:
        order.append("second begins")
        tx.create_ref(NewRef(BREAD, None, None, "second", COOK))
        order.append("second ends")

    def first() -> None:
        try:
            storage.transact(slow)
        except BaseException as error:  # noqa: BLE001 - reported below
            failed.append(error)
            started.set()

    thread = threading.Thread(target=first)
    thread.start()
    assert started.wait(10)
    storage.transact(fast)
    thread.join()
    assert failed == []
    assert order == ["first begins", "first ends", "second begins", "second ends"]


def test_a_commits_time_is_a_canonical_date_time(setup: Callable[..., Setup]) -> None:
    """No trailing zeros in its fraction, and no fraction when it is zero."""
    now = [1_800_000_000_120_000]
    s = setup(clock=lambda: now[0])
    ref = s.storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))

    def commit() -> Any:
        return s.storage.transact(lambda tx: tx.insert_commit(NewCommit(BREAD, ref.id, None, "", 1, "0" * 64, None, COOK)))

    assert commit().created_at == "2027-01-15T08:00:00.12Z"
    now[0] = 1_800_000_000_000_000
    whole = commit()
    assert whole.created_at == "2027-01-15T08:00:00Z"
    assert s.storage.transact(lambda tx: tx.read_commit(whole.id)).created_at == "2027-01-15T08:00:00Z"
    now[0] = 1_800_000_000_000_001
    assert commit().created_at == "2027-01-15T08:00:00.000001Z"
    assert micros_to_date_time(-1) == "1969-12-31T23:59:59.999999Z"


def test_a_time_outside_the_years_0000_to_9999_or_outside_2_to_the_53_microseconds_is_refused(
    setup: Callable[..., Setup],
) -> None:
    """A time is refused outside the years 0000-9999 and outside
    +/-(2^53-1) microseconds, the integers the TypeScript adapter reads
    exactly: from the clock when a transaction begins, and from a stored
    integer column when it is read."""
    # The years first, so a time past each bound names its own.
    with pytest.raises(ValueError, match="outside the years 0000-9999"):
        micros_to_date_time(-62_167_219_200_000_001)
    with pytest.raises(ValueError, match="outside the years 0000-9999"):
        micros_to_date_time(253_402_300_800_000_000)
    for micros in (MAX_SAFE + 1, -MAX_SAFE - 1):
        with pytest.raises(ValueError, match=re.escape("outside +/-(2^53-1)")):
            micros_to_date_time(micros)
    assert micros_to_date_time(MAX_SAFE) == "2255-06-05T23:47:34.740991Z"
    assert micros_to_date_time(-MAX_SAFE) == "1684-07-28T00:12:25.259009Z"
    # A clock outside the range is refused when a transaction begins, and
    # nothing is written; one at its bounds is taken.
    now = [MAX_SAFE]
    s = setup(clock=lambda: now[0])
    now[0] = MAX_SAFE + 1
    with pytest.raises(ValueError, match=re.escape("the clock returned 9007199254740992 microseconds, outside +/-(2^53-1)")):
        s.storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))
    assert count(s.connection, 'SELECT count(*) FROM "graph_ref"') == 0
    for bound in (MAX_SAFE, -MAX_SAFE):
        now[0] = bound
        ref = s.storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, f"at {bound}", COOK)))
        assert s.storage.transact(lambda tx: tx.read_ref(ref.id)).name == f"at {bound}"
    # A stored integer outside the range is refused on read: a commit's time,
    # a ref's seal and a version.
    now[0] = 1_800_000_000_000_000
    ref, commit = s.storage.transact(ref_and_commit)
    for sql, read, column in (
        ('UPDATE "graph_commit" SET created_at = ?1 WHERE id = ?2', lambda tx: tx.read_commit(commit), "created_at"),
        ('UPDATE "graph_ref" SET sealed_at = ?1 WHERE id = ?2', lambda tx: tx.read_ref(ref.id), "sealed_at"),
        ('UPDATE "graph_ref" SET _version = ?1 WHERE id = ?2', lambda tx: tx.read_ref(ref.id), "_version"),
    ):
        for value in (MAX_SAFE + 1, -MAX_SAFE - 1):
            s.connection.execute(sql, [value, commit if "commit" in sql else ref.id])
            with pytest.raises(ValueError, match=re.escape(f"column {column} is {value}, outside +/-(2^53-1)")):
                s.storage.transact(read)
        s.connection.execute(sql, [MAX_SAFE if column != "_version" else 1, commit if "commit" in sql else ref.id])
        s.storage.transact(read)


def test_every_id_the_adapter_writes_is_a_version_4_uuid_in_its_canonical_form(setup: Callable[..., Setup]) -> None:
    s = setup()
    main = s.engine.create_primary(COOK, BREAD, "main")
    draft = s.engine.branch(COOK, main.id, "draft")
    saved = s.engine.save(COOK, draft.id, draft.version, {"step": KindEdits(upsert=[step_row(None, "Mix")])})
    committed = s.engine.commit(COOK, draft.id, saved.ref.version)
    merged = s.engine.merge(COOK, draft.id, main.id, main.version, [], CommitOptions(tag=True))
    assert merged.commit is not None
    s.engine.release(COOK, BREAD, merged.commit.id, 0)
    ids: List[str] = []
    for table, names in (
        ("ref", ["id"]),
        ("ref_history", ["history_id"]),
        ("commit", ["id"]),
        ("patch", ["id"]),
        ("snapshot_entry", ["id"]),
        ("release", ["id"]),
        ("release_history", ["history_id"]),
        ("member", ["id", "entity_key"]),
        ("member_history", ["history_id"]),
    ):
        rows = s.connection.execute(f'SELECT {", ".join(names)} FROM "graph_{table}"').fetchall()
        assert rows, f"{table} has rows"
        for row in rows:
            for name, value in zip(names, row):
                assert_canonical_id(value, f"{table}.{name}")
                ids.append(value)
    assert len(set(ids)) == len(ids) - 1, "every id is new but the entity key two rows share"
    assert committed.commit is not None
    assert_canonical_id(committed.commit.id, "a commit's id")


def _canonical_cases() -> Any:
    values: List[Dict[str, Any]] = []
    rows: List[Dict[str, Any]] = []
    for path in sorted((TESTDATA / "canonical").glob("*.json")):
        doc = json.loads(path.read_text(encoding="utf-8"))
        values.extend(doc.get("cases", []))
        rows.extend(doc.get("rows", []))
    return values, rows


# Input forms the canonical rules read, for each list class, beside the
# vectors' Postgres forms: each is stored canonical, as canonical_value gives
# it, or refused.
INPUTS = [
    ("string[]", '["a", 1]'),
    ("string[][]", '[["a"], [2]]'),
    ("integer[]", "[-0, 12]"),
    ("integer[][]", "[[-0, 7], []]"),
    ("integer[][]", "[[1.5]]"),
    ("number[]", "[1e2, -0]"),
    ("number[][]", "[[1.50e1]]"),
    ("boolean[]", '["true"]'),
    ("boolean[][]", '[[true], ["true"]]'),
    ("uuid[][]", '[["5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"]]'),
    ("dateTime[]", '["2026-09-01T12:30:00+02:30"]'),
    ("dateTime[][]", '[["2026-09-01T12:30:00.10+02:30"]]'),
    ("date[]", '["2026-02-30"]'),
    ("date[]", '["2026-09-01", "nope"]'),
    ("date[][]", '[["2026-02-29"]]'),
    ("time[]", '["2:30 pm"]'),
    ("duration[]", '["1 day 02:00:00"]'),
    ("enum[]", "[1]"),
    ("enum[][]", '[["again"], [1]]'),
    ("json[]", '[{"b": 1.50, "a": [2.0]}]'),
    ("json[][]", '[[{"b": 1, "a": 0}]]'),
]


def test_every_canonical_vector_reads_back_as_the_canonical_value_written(open_db: Callable[..., sqlite3.Connection]) -> None:
    """Each case's canonical value, written in a row, is stored and reads
    back unchanged, live and from history; its Postgres form and the input
    forms the rules read are stored canonical; a value its class refuses is
    not stored."""
    values, rows = _canonical_cases()
    assert values and rows
    # A kind per case, whose role columns are named apart from its own.
    roles = {"key": "vg_key", "id": "vg_id", "ref": "vg_ref", "root": "vg_root", "tombstone": "vg_tombstone", "version": "vg_version"}
    role_columns = {
        "vg_key": "uuid",
        "vg_id": "uuid",
        "vg_ref": "uuid",
        "vg_root": "uuid",
        "vg_tombstone": "boolean",
        "vg_version": "integer",
    }

    def kind_of(kind: str, columns: Dict[str, str]) -> Dict[str, Any]:
        return {"kind": kind, **roles, "history": {"exclude": []}, "columns": {**role_columns, **columns}}

    kinds = (
        [kind_of(f"value{i}", {"v": c["class"]}) for i, c in enumerate(values)]
        + [kind_of(f"postgres{i}", {"v": c["class"]}) for i, c in enumerate(values)]
        + [kind_of(f"row{i}", c["columns"]) for i, c in enumerate(rows)]
        + [kind_of(f"postgresRow{i}", c["columns"]) for i, c in enumerate(rows)]
        + [kind_of(f"input{i}", {"v": value_class}) for i, (value_class, _) in enumerate(INPUTS)]
    )
    connection = open_db()
    client = sqlite_client(connection)
    adapter = SqliteAdapter(json.dumps({"version": 3, "kinds": kinds}), graph="vectors")
    adapter.create_tables(client)
    storage = adapter.storage(client)
    ref = storage.transact(lambda tx: tx.create_ref(NewRef(BREAD, None, None, "main", COOK)))

    def write(kind: str, row: str) -> str:
        return storage.transact(lambda tx: tx.upsert_row(kind, RowWrite(ref.id, BREAD, row, False, COOK)))

    def stored(kind: str) -> Any:
        return connection.execute('SELECT id, data FROM "graph_member" WHERE kind = ?1', [kind]).fetchone()

    for i, c in enumerate(values):
        kind, name = f"value{i}", f"{c['class']}/{c['name']}"
        if "canonical" not in c:
            with pytest.raises(CanonicalError):
                write(kind, '{"v":' + c["postgres"] + "}")
            assert stored(kind) is None, name
            continue
        written = write(kind, '{"v":' + c["canonical"] + "}")
        id, data = stored(kind)
        assert data == '{"v":' + c["canonical"] + "}", f"{name} is stored canonical"
        write(f"postgres{i}", '{"v":' + c["postgres"] + "}")
        assert stored(f"postgres{i}")[1] == '{"v":' + c["canonical"] + "}", f"{name}'s Postgres form is stored canonical"
        [read] = storage.transact(lambda tx: tx.rows(kind, ref.id))
        [image] = storage.transact(lambda tx: tx.images(kind, [Pin(id, 1)]))
        for text in (written, read, image):
            assert text.startswith('{"v":' + c["canonical"] + ',"vg_id":'), f"{name}: {text}"
    for i, c in enumerate(rows):
        kind = f"row{i}"
        if "canonical" not in c:
            with pytest.raises(Exception):
                write(kind, c["postgres"])
            assert stored(kind) is None, c["name"]
            continue
        write(kind, c["canonical"])
        write(f"postgresRow{i}", c["postgres"])
        # Every declared column, the ones the row lacks as null.
        canonical = loads(c["canonical"])
        expected = ",".join(
            json.dumps(column) + ":" + (dumps(canonical[column]) if column in canonical else "null")
            for column in sorted(c["columns"])
        )
        assert stored(kind)[1] == "{" + expected + "}", c["name"]
        assert stored(f"postgresRow{i}")[1] == "{" + expected + "}", f"{c['name']}: its Postgres form"
    for i, (value_class, given) in enumerate(INPUTS):
        kind = f"input{i}"
        try:
            want: Optional[str] = canonical_value(value_class, given)
        except CanonicalError:
            want = None
        if want is None:
            with pytest.raises(CanonicalError):
                write(kind, '{"v":' + given + "}")
            assert stored(kind) is None, f"{value_class} {given} is refused"
            continue
        assert dumps(loads(given)) != want, f"{value_class} {given} is not already canonical"
        write(kind, '{"v":' + given + "}")
        assert stored(kind)[1] == '{"v":' + want + "}", f"{value_class} {given} is stored canonical"


# -- The sqlite3 binding --------------------------------------------------------


def test_the_binding_returns_rows_keyed_by_column_and_raises_sqlites_errors(
    open_db: Callable[..., sqlite3.Connection],
) -> None:
    connection = open_db()
    # A row factory of the caller's does not reach the adapter's rows.
    connection.row_factory = lambda cursor, row: {d[0]: value for d, value in zip(cursor.description, row)}
    client = sqlite_client(connection)
    client.transact(lambda conn: conn.query("CREATE TABLE t (a TEXT NOT NULL UNIQUE, b INTEGER) STRICT"))
    inserted = client.transact(lambda conn: conn.query("INSERT INTO t (a, b) VALUES (?1, ?2)", ["x", 1]))
    assert (inserted.rows, inserted.rowcount) == ([], 1)
    got = client.transact(lambda conn: conn.query("SELECT a, b, NULL AS c, ?1 AS d FROM t WHERE a = ?1", ["x"]))
    assert got.rows == [{"a": "x", "b": 1, "c": None, "d": "x"}]
    assert client.transact(lambda conn: conn.query("SELECT a FROM t WHERE a = ?1", ["y"])).rows == []
    with pytest.raises(sqlite3.IntegrityError) as unique:
        client.transact(lambda conn: conn.query("INSERT INTO t (a, b) VALUES (?1, ?2)", ["x", 2]))
    assert_sqlite_error(unique.value, SQLITE_CONSTRAINT_UNIQUE)
    assert is_unique_violation(unique.value)
    with pytest.raises(sqlite3.OperationalError) as syntax:
        client.transact(lambda conn: conn.query("SELEC 1"))
    assert not is_unique_violation(syntax.value)
    assert connection.in_transaction is False


def test_is_unique_violation_reads_the_code_and_before_python_3_11_the_message() -> None:
    """An error that carries SQLite's extended result code is read by it
    alone; one without, as the sqlite3 module raises before Python 3.11, by
    SQLite's words for the refusal."""
    without_code = sqlite3.IntegrityError("UNIQUE constraint failed: graph_ref.graph, graph_ref.root_id, graph_ref.name")
    assert is_unique_violation(without_code)
    assert not is_unique_violation(sqlite3.IntegrityError("FOREIGN KEY constraint failed"))
    assert not is_unique_violation(sqlite3.OperationalError("UNIQUE constraint failed: t.a"))
    assert not is_unique_violation(ValueError("UNIQUE constraint failed: t.a"))
    for code, unique in ((2067, True), (1555, False), (787, False)):
        error = sqlite3.IntegrityError("UNIQUE constraint failed: t.a")
        error.sqlite_errorcode = code  # type: ignore[attr-defined]
        assert is_unique_violation(error) is unique, code
    # After the prefix the adapter puts on an error that escapes it.
    assert is_unique_violation(sqlite3.IntegrityError("sqlite: write patches: UNIQUE constraint failed: graph_patch.commit_id"))
    assert not is_unique_violation(sqlite3.IntegrityError("sqlite: write patches: FOREIGN KEY constraint failed"))
    assert not is_unique_violation(sqlite3.IntegrityError("sqlite: write patches: CHECK constraint failed: operation"))


def test_a_unique_violation_that_escapes_the_adapter_is_one_with_its_prefix(setup: Callable[..., Setup]) -> None:
    """A unique index's refusal the adapter does not turn into an engine
    error escapes with what the adapter was doing before SQLite's message,
    and is_unique_violation still reads it as one, by the error's code from
    Python 3.11 and by the words after the prefix before."""
    s = setup()
    _, commit = s.storage.transact(ref_and_commit)
    patch = Patch("", "step", "Mix", "Row", 1, "ADD")
    s.storage.transact(lambda tx: tx.insert_patches(commit, [patch]))
    with pytest.raises(sqlite3.IntegrityError) as escaped:
        s.storage.transact(lambda tx: tx.insert_patches(commit, [patch]))
    assert str(escaped.value).startswith("sqlite: write patches: UNIQUE constraint failed: "), str(escaped.value)
    assert is_unique_violation(escaped.value)
    assert_sqlite_error(escaped.value, SQLITE_CONSTRAINT_UNIQUE)


def test_the_binding_refuses_a_sqlite_older_than_the_layout_needs(
    open_db: Callable[..., sqlite3.Connection], monkeypatch: pytest.MonkeyPatch
) -> None:
    floor = ".".join(str(n) for n in MIN_SQLITE_VERSION)
    assert floor == "3.37.0"
    for version in ("3.36.0", "3.31.1", "2.8.17"):
        monkeypatch.setattr(sqlite3, "sqlite_version", version)
        with pytest.raises(RuntimeError, match=re.escape(f"SQLite is {version}; the adapter needs 3.37.0 or newer")):
            sqlite_client(open_db())
    for version in ("3.37.0", "3.37.2", "3.45.1"):
        monkeypatch.setattr(sqlite3, "sqlite_version", version)
        sqlite_client(open_db())


def test_this_pythons_sqlite_runs_the_adapter() -> None:
    """The SQLite library this Python loaded is one the adapter runs on, so
    every test here runs on it rather than being refused."""
    assert tuple(int(n) for n in sqlite3.sqlite_version.split(".")) >= MIN_SQLITE_VERSION, sqlite3.sqlite_version


def test_the_binding_refuses_a_sqlite_without_the_json_functions(open_db: Callable[..., sqlite3.Connection]) -> None:
    connection = open_db()
    SQLITE_FUNCTION = 31

    def no_json(action: int, arg1: Optional[str], arg2: Optional[str], *rest: Any) -> int:
        return sqlite3.SQLITE_DENY if action == SQLITE_FUNCTION and arg2 == "json_extract" else sqlite3.SQLITE_OK

    connection.set_authorizer(no_json)
    with pytest.raises(RuntimeError, match="cannot run json_each and json_extract"):
        sqlite_client(connection)


def test_the_binding_refuses_a_connection_the_sqlite3_module_begins_transactions_on(
    open_db: Callable[..., sqlite3.Connection],
) -> None:
    for isolation_level in ("", "DEFERRED", "IMMEDIATE", "EXCLUSIVE"):
        with pytest.raises(ValueError, match="open it with isolation_level=None"):
            sqlite_client(open_db(isolation_level=isolation_level))


@pytest.mark.skipif(sys.version_info < (3, 12), reason="a connection's autocommit is Python 3.12's")
def test_the_binding_takes_autocommit_on_and_refuses_it_off(open_db: Callable[..., sqlite3.Connection]) -> None:
    """From Python 3.12 autocommit decides a connection's transactions: on,
    the module begins none, and the binding runs on it whatever
    isolation_level says; off, the module holds one open always, and the
    binding refuses it."""
    with pytest.raises(ValueError, match="open it with isolation_level=None"):
        sqlite_client(open_db(isolation_level=None, autocommit=False))
    connection = open_db(isolation_level="DEFERRED", autocommit=True)
    client = sqlite_client(connection)
    adapter = SqliteAdapter(DESCRIPTOR, graph=GRAPH)
    adapter.create_tables(client)
    engine = Engine(DESCRIPTOR, adapter.storage(client), schema_epoch=1, snapshot_every=3)
    engine.create_primary(COOK, BREAD, "main")
    assert connection.in_transaction is False
    assert count(connection, 'SELECT count(*) FROM "graph_ref"') == 1


def test_the_binding_refuses_a_connection_whose_foreign_keys_would_not_turn_on(
    open_db: Callable[..., sqlite3.Connection],
) -> None:
    connection = open_db()
    connection.execute("PRAGMA foreign_keys = OFF")
    # SQLite ignores the pragma inside a transaction.
    connection.execute("BEGIN")
    with pytest.raises(ValueError, match="foreign keys would not turn on"):
        sqlite_client(connection)
    connection.execute("ROLLBACK")
    sqlite_client(connection)
    assert connection.execute("PRAGMA foreign_keys").fetchone()[0] == 1


def _without(member: str, *path: Any) -> Callable[[Dict[str, Any]], None]:
    def edit(d: Dict[str, Any]) -> None:
        target: Any = d
        for step in path:
            target = target[step]
        del target[member]

    return edit


def _set(value: Any, member: str, *path: Any) -> Callable[[Dict[str, Any]], None]:
    def edit(d: Dict[str, Any]) -> None:
        target: Any = d
        for step in path:
            target = target[step]
        target[member] = value

    return edit


REFUSALS = [
    ("the fixture's descriptor", lambda d: None, {}, ""),
    ("a descriptor of version 2", _set(2, "version"), {}, "reads version 3"),
    ("no graph", lambda d: None, {"graph": ""}, "needs its graph's name"),
    ("a kind without a root column", _without("root", "kinds", 0), {}, "has no root"),
    (
        "a role column missing from the kind's columns",
        _without("_version", "kinds", 0, "columns"),
        {},
        'version column "_version" is not in its columns',
    ),
    ("a tombstone that is not boolean", _set("integer", "deleted_on_ref", "kinds", 0, "columns"), {}, "a tombstone is a boolean column"),
    ("a kind without history", _without("history", "kinds", 0), {}, "has no history"),
    ("a clock that is not a function", lambda d: None, {"clock": 5}, "clock is a function"),
]


@pytest.mark.parametrize("name,edit,options,refuse", REFUSALS, ids=[r[0] for r in REFUSALS])
def test_new_adapter(name: str, edit: Callable[[Dict[str, Any]], None], options: Dict[str, Any], refuse: str) -> None:
    d = json.loads(DESCRIPTOR)
    edit(d)
    kwargs = {"graph": GRAPH, **options}
    if refuse == "":
        SqliteAdapter(json.dumps(d), **kwargs)
        SqliteAdapter(d, **kwargs)
        return
    with pytest.raises((ValueError, TypeError), match=re.escape(refuse)):
        SqliteAdapter(json.dumps(d), **kwargs)
