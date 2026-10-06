"""The shared SQLite vectors (runtime/versiongraph/testdata/sqlite, whose
README gives each file's shape) through the Python adapter: its layout's
statements are layout.json's, a database the TypeScript adapter wrote
(typescript.sql) reads back through the adapter and the engine as
typescript.json, byte for byte, through each graph's adapter nothing of the
other graph reads, and the TypeScript script's writes, replayed through this
adapter with the same clock and ids, write typescript.sql byte for byte. The
Python counterpart of the TypeScript package's test/sqlite-vectors-cases.ts.
None needs a database server."""

import calendar
import json
import re
import sqlite3
import uuid
from typing import Any, Callable, Dict, Iterator, List, Optional, Union

import pytest
from support import DESCRIPTOR, TESTDATA

from superschematic_versiongraph import sqlite as sqlite_module
from superschematic_versiongraph.canonical import uuid_canonical
from superschematic_versiongraph.engine import CommitOptions, Engine, KindEdits, MergeResult, TreeResult
from superschematic_versiongraph.errors import error_code
from superschematic_versiongraph.exactjson import loads, write_string
from superschematic_versiongraph.sqlite import (
    SQLITE_TABLES,
    SqliteAdapter,
    default_table_name,
    sqlite_client,
    sqlite_layout,
)
from superschematic_versiongraph.storage import Commit, Pin, Ref, Release, Tx

VECTORS = TESTDATA / "sqlite"
LAYOUT = VECTORS / "layout.json"
SQL = VECTORS / "typescript.sql"
READS = VECTORS / "typescript.json"

# The engine the vectors are read through: the scenarios' schema epoch and
# snapshot interval.
SCHEMA_EPOCH = 1
SNAPSHOT_EVERY = 3

# The descriptor's kinds, in its order: each one's name and entity key
# column.
KINDS = [(k["kind"], k["key"]) for k in json.loads(DESCRIPTOR)["kinds"]]


def quote(name: str) -> str:
    return '"' + name.replace('"', '""') + '"'


def load_database(connection: sqlite3.Connection, sql: str) -> None:
    """Loads typescript.sql, one statement per line, with foreign keys off,
    since a ref and its head commit name each other; then every key holds."""
    connection.execute("PRAGMA foreign_keys = OFF")
    for statement in sql.split("\n"):
        if statement != "":
            connection.execute(statement)
    assert connection.execute("PRAGMA foreign_key_check").fetchall() == [], "every foreign key of the loaded file holds"


@pytest.fixture
def loaded() -> Iterator[sqlite3.Connection]:
    """An in-memory database holding typescript.sql, in autocommit."""
    connection = sqlite3.connect(":memory:", isolation_level=None)
    try:
        load_database(connection, SQL.read_text(encoding="utf-8"))
        yield connection
    finally:
        connection.close()


class Raw:
    """JSON text written into the document as it is: a row, kept exact."""

    def __init__(self, text: str) -> None:
        self.text = text


Out = Union[None, bool, int, str, Raw, List[Any], Dict[str, Any]]


def pretty(value: Out, indent: str = "") -> str:
    """Writes a value as typescript.json is written: two spaces of
    indentation, each Raw on one line as it is."""
    if isinstance(value, Raw):
        return value.text
    if value is None:
        return "null"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, str):
        return write_string(value)
    inner = indent + "  "
    if isinstance(value, list):
        if not value:
            return "[]"
        return "[\n" + ",\n".join(inner + pretty(v, inner) for v in value) + "\n" + indent + "]"
    if not value:
        return "{}"
    return "{\n" + ",\n".join(inner + write_string(n) + ": " + pretty(v, inner) for n, v in value.items()) + "\n" + indent + "}"


def ref_out(r: Ref) -> Out:
    return {
        "id": r.id,
        "root": r.root,
        "parent": r.parent,
        "base": r.base,
        "head": r.head,
        "name": r.name,
        "sealed": r.sealed,
        "discarded": r.discarded,
        "version": r.version,
    }


def commit_out(c: Commit) -> Out:
    return {
        "id": c.id,
        "root": c.root,
        "ref": c.ref,
        "parent": c.parent,
        "message": c.message,
        "schemaEpoch": c.schema_epoch,
        "contentHash": c.content_hash,
        "sequence": c.sequence,
        "createdAt": c.created_at,
        "createdBy": c.created_by,
        "snapshot": c.snapshot,
    }


def release_out(r: Release) -> Out:
    return {"id": r.id, "root": r.root, "commit": r.commit, "version": r.version}


def finding_out(f: Any) -> Out:
    out: Dict[str, Any] = {"code": f["code"], "kind": f["kind"]}
    if "entityKey" in f:
        out["entityKey"] = f["entityKey"]
    out["message"] = f["message"]
    return out


def tree_result_out(t: TreeResult) -> Dict[str, Any]:
    """A tree, its kinds by name and each kind's rows in the engine's order,
    with its content hash and findings."""
    return {
        "tree": {kind: [Raw(row) for row in t.tree[kind]] for kind in sorted(t.tree)},
        "contentHash": t.content_hash,
        "findings": [finding_out(f) for f in t.findings],
    }


def attempt(fn: Callable[[], Out]) -> Out:
    """An engine read's value, or {"error": code} for an engine error."""
    try:
        return fn()
    except Exception as error:
        code = error_code(error)
        if code == "":
            raise
        return {"error": code}


def key_of(row: str, column: str) -> str:
    key = loads(row)[column]
    assert isinstance(key, str), f"a row's {column} is a string"
    return key


def ids_of(connection: sqlite3.Connection, local: str, graph: str, column: str = "id") -> List[str]:
    """The ids of a table's rows in a graph, in code point order."""
    rows = connection.execute(f"SELECT {column} FROM {quote(default_table_name(local))} WHERE graph = ?1", [graph])
    return sorted({row[0] for row in rows})


def read_graph(connection: sqlite3.Connection, graph: str) -> Out:
    """One graph's reads, through an adapter opened with its name."""
    storage = SqliteAdapter(DESCRIPTOR, graph=graph).storage(sqlite_client(connection))
    engine = Engine(DESCRIPTOR, storage, schema_epoch=SCHEMA_EPOCH, snapshot_every=SNAPSHOT_EVERY)

    def read(fn: Callable[[Tx], Any]) -> Any:
        return storage.transact(fn)

    refs: List[Out] = []
    for id in ids_of(connection, "ref", graph):
        rows: Dict[str, Any] = {}
        for kind, key in KINDS:
            found = read(lambda tx: tx.rows(kind, id))
            rows[kind] = [Raw(row) for row in sorted(found, key=lambda row: key_of(row, key))]
        refs.append(
            {
                "id": id,
                "readRef": ref_out(read(lambda tx: tx.read_ref(id))),
                "rows": rows,
                "compose": attempt(lambda: tree_result_out(engine.compose(id))),
                "history": attempt(lambda: [commit_out(c) for c in engine.history(id)]),
            }
        )

    commits: List[Out] = []
    for id in ids_of(connection, "commit", graph):
        patches = sorted(read(lambda tx: tx.patches([id])), key=lambda p: (p.kind, p.entity_key))
        entries = sorted(read(lambda tx: tx.snapshot(id)), key=lambda e: (e.kind, e.entity_key))
        commits.append(
            {
                "id": id,
                "readCommit": commit_out(read(lambda tx: tx.read_commit(id))),
                "materialize": attempt(lambda: tree_result_out(engine.materialize(id))),
                "patches": [
                    {
                        "commit": p.commit,
                        "kind": p.kind,
                        "entityKey": p.entity_key,
                        "entityId": p.entity_id,
                        "entityVersion": p.entity_version,
                        "operation": p.operation,
                    }
                    for p in patches
                ],
                "snapshot": [
                    {"kind": e.kind, "entityKey": e.entity_key, "entityId": e.entity_id, "entityVersion": e.entity_version}
                    for e in entries
                ],
            }
        )

    def released(root: str) -> Out:
        r = engine.released(root)
        return {"release": release_out(r.release), **tree_result_out(r.tree)}

    roots: List[Out] = [
        {"root": root, "released": attempt(lambda: released(root))} for root in ids_of(connection, "ref", graph, "root_id")
    ]

    pins = sorted(
        connection.execute(
            f"SELECT kind, id, _version FROM {quote(default_table_name('member_history'))} WHERE graph = ?1", [graph]
        ).fetchall()
    )
    images: List[Out] = []
    for kind, id, version in pins:
        found = read(lambda tx: tx.images(kind, [Pin(id, version)]))
        assert len(found) == 1, f"history holds {kind} {id} at {version}"
        images.append({"kind": kind, "id": id, "version": version, "image": Raw(found[0])})

    return {"graph": graph, "refs": refs, "commits": commits, "roots": roots, "images": images}


def read_vectors(connection: sqlite3.Connection) -> str:
    """What a database reads back as, through an adapter opened with each
    graph's name, as typescript.json holds it."""
    graphs = sorted({row[0] for row in connection.execute(f"SELECT graph FROM {quote(default_table_name('ref'))}")})
    return pretty({"graphs": [read_graph(connection, graph) for graph in graphs]}) + "\n"


def test_the_layout_is_layout_json() -> None:
    """sqlite_layout() under the default names is layout.json's statements,
    string for string, in order."""
    assert sqlite_layout() == json.loads(LAYOUT.read_text(encoding="utf-8"))["statements"]


def test_typescript_sql_begins_with_the_layout() -> None:
    """The file the TypeScript adapter wrote begins with this adapter's
    layout, so the database it loads is the one this adapter creates."""
    lines = SQL.read_text(encoding="utf-8").split("\n")
    layout = sqlite_layout()
    assert lines[: len(layout)] == [statement + ";" for statement in layout]
    assert all(line == "" or line.startswith("INSERT INTO ") for line in lines[len(layout) :])


def test_typescript_sql_reads_as_typescript_json(loaded: sqlite3.Connection) -> None:
    """The database the TypeScript adapter wrote reads back through this
    adapter and the engine, with an adapter opened with each graph's name,
    as typescript.json, byte for byte: every row as its exact canonical
    text."""
    got = read_vectors(loaded)
    want = READS.read_text(encoding="utf-8")
    if got != want:
        # The first line that differs, to say where.
        for i, (a, b) in enumerate(zip(got.split("\n"), want.split("\n"))):
            assert a == b, f"line {i + 1} of typescript.json"
    assert got == want


def test_through_each_graphs_adapter_another_graphs_refs_commits_and_images_read_as_none(
    loaded: sqlite3.Connection,
) -> None:
    file = json.loads(READS.read_text(encoding="utf-8"))
    assert len(file["graphs"]) >= 2, "the file holds two graphs"

    def code_of(fn: Callable[[], Any]) -> Optional[str]:
        try:
            fn()
        except Exception as error:
            return error_code(error)
        return None

    client = sqlite_client(loaded)
    for g in file["graphs"]:
        storage = SqliteAdapter(DESCRIPTOR, graph=g["graph"]).storage(client)
        engine = Engine(DESCRIPTOR, storage, schema_epoch=SCHEMA_EPOCH, snapshot_every=SNAPSHOT_EVERY)
        for other in (o for o in file["graphs"] if o["graph"] != g["graph"]):
            where = f"{g['graph']} reads {other['graph']}'s"

            def reads(tx: Tx) -> None:
                for ref in other["refs"]:
                    assert code_of(lambda: tx.read_ref(ref["id"])) == "not_found", f"{where} ref {ref['id']}"
                    for kind, _ in KINDS:
                        assert tx.rows(kind, ref["id"]) == [], f"{where} {kind} rows"
                for commit in other["commits"]:
                    assert code_of(lambda: tx.read_commit(commit["id"])) == "not_found", f"{where} commit {commit['id']}"
                    assert tx.patches([commit["id"]]) == [], f"{where} patches"
                    assert tx.snapshot(commit["id"]) == [], f"{where} snapshot"
                for pin in other["images"]:
                    assert tx.images(pin["kind"], [Pin(pin["id"], pin["version"])]) == [], f"{where} image"

            storage.transact(reads)
            for ref in other["refs"]:
                assert code_of(lambda: engine.compose(ref["id"])) == "not_found", f"{where} ref {ref['id']}"
            for commit in other["commits"]:
                assert code_of(lambda: engine.materialize(commit["id"])) == "not_found", f"{where} commit {commit['id']}"


# -- The script, replayed -------------------------------------------------------
#
# writeDatabase in typescript/test/sqlite-vectors.ts, step for step: the same
# clock, the same seeded ids, the same rows, through this adapter and the
# engine. What it writes, dumped as dumpDatabase dumps it, is typescript.sql.

# The time of the script's first transaction, 2026-10-05T09:00:00Z, and how
# far its clock moves at each read.
FIRST_TIME = calendar.timegm((2026, 10, 5, 9, 0, 0)) * 1_000_000
CLOCK_STEP = 1_250_005

# The seed of the script's ids.
ID_SEED = 0x5EEDD32A

MASK64 = (1 << 64) - 1


def seeded_ids(seed: int) -> Callable[[], str]:
    """New ids as the script makes them: version-4 UUIDs from a splitmix64
    generator seeded with seed, in their canonical form."""
    state = [seed]

    def next64() -> int:
        state[0] = (state[0] + 0x9E3779B97F4A7C15) & MASK64
        z = state[0]
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def new_id() -> str:
        n = (next64() << 64) | next64()
        n = (n & ~(0xF << 76)) | (0x4 << 76)  # version 4
        n = (n & ~(0x3 << 62)) | (0x2 << 62)  # variant 10
        return uuid_canonical(str(uuid.UUID(int=n)))

    return new_id


def fixed_clock() -> Callable[[], int]:
    """A clock that reads FIRST_TIME, then moves CLOCK_STEP at each read."""
    now = [FIRST_TIME]

    def clock() -> int:
        time = now[0]
        now[0] += CLOCK_STEP
        return time

    return clock


def row(**columns: Any) -> str:
    return json.dumps(columns, separators=(",", ":"), ensure_ascii=False)


# The tastings: a value of every class the descriptor names, as JSON text,
# so the integer wider than a double keeps its digits.
FIRST_TASTING = (
    '{"entity_key":"First","taster":"Ann","salty":true,"score":4.5,"servings":9007199254740993,'
    '"tasted_on":"2026-09-01","tasted_at":"2026-09-01T10:00:00.12Z","served_at":"18:30:00","rested":"1h30m0s",'
    '"verdict":"again","remarks":{"crust":[1,2.50],"crumb":"open"},"tags":["sour","a \\"quoted\\" tag","crème brûlée 🍞"],'
    '"helpers":["Bob","Cy"],"bites":[[1,2],[3]]}'
)
SECOND_TASTING = (
    '{"entity_key":"00000000-0000-0000-0000-000000000002","taster":"00000000-0000-0000-0000-00000000000a","salty":false,'
    '"score":1e21,"servings":-3,"tasted_on":"2026-02-28","tasted_at":"2026-09-01T12:30:00+02:30","served_at":"2:30 pm",'
    '"rested":"-1m30.5s","verdict":"never","remarks":null,"tags":[],"helpers":["00000000-0000-0000-0000-00000000003d"],"bites":[]}'
)

COOK, ANN, BREAD = "Cook", "Ann", "Bread"


def merged(result: MergeResult) -> Any:
    assert result.conflicts == [], "the script's merges have no conflicts"
    assert result.commit is not None, "the script's merges write a commit"
    return result.ref, result.commit


def write_database(connection: sqlite3.Connection) -> None:
    """The script: two graphs of root Bread, recipe and menu, menu's first
    writes over the fixture's descriptor less utensil.name."""
    d = json.loads(DESCRIPTOR)
    utensil = next(k for k in d["kinds"] if k["kind"] == "utensil")
    assert "name" in utensil["columns"], "the fixture's utensil declares name"
    del utensil["columns"]["name"]
    before_gain = json.dumps(d, separators=(",", ":"), ensure_ascii=False)
    options: Dict[str, Any] = {"schema_epoch": SCHEMA_EPOCH, "snapshot_every": SNAPSHOT_EVERY}
    clock = fixed_clock()
    client = sqlite_client(connection)

    recipe = SqliteAdapter(DESCRIPTOR, graph="recipe", clock=clock)
    recipe.create_tables(client)
    g = Engine(DESCRIPTOR, recipe.storage(client), **options)
    main = g.create_primary(COOK, BREAD, "main")
    first = g.branch(COOK, main.id, "first")
    every_kind = {
        "cover": KindEdits(upsert=[row(entity_key="Cover", photo_url="https://example.com/bread.jpg")]),
        "ingredient": KindEdits(
            upsert=[
                row(entity_key="Flour", step_key="Knead", quantity="500 g", substitutes=[{"name": "spelt", "ratio": 1}]),
                row(entity_key="Salt", step_key="Knead", quantity="10 g", substitutes=None),
            ]
        ),
        "note": KindEdits(
            upsert=[
                row(entity_key="Note", body="Proof overnight\tif there's time"),
                row(entity_key="Reply", body="Agreed", reply_to="Note"),
            ]
        ),
        "step": KindEdits(
            upsert=[
                row(entity_key="Knead", position=1, instruction="Knead for ten minutes", timings={"knead": "10m"}, scratch="floury"),
                row(entity_key="Bake", position=2, instruction="Bake at 230 C", timings={"bake": "35m", "preheat": "30m"}),
            ]
        ),
        "tasting": KindEdits(upsert=[FIRST_TASTING, SECOND_TASTING]),
        "utensil": KindEdits(upsert=[row(name="Bowl")]),
    }
    first = g.save(COOK, first.id, first.version, every_kind).ref
    first = g.commit(COOK, first.id, first.version, CommitOptions(message="first draft")).ref
    main, m1 = merged(g.merge(COOK, first.id, main.id, main.version, [], CommitOptions(message="first", tag=True)))
    release = g.release(COOK, BREAD, m1.id, 0)
    g.seal(COOK, first.id, first.version)

    second = g.branch(ANN, main.id, "second")
    second = g.save(
        ANN,
        second.id,
        second.version,
        {
            "ingredient": KindEdits(delete=["Salt"]),
            "step": KindEdits(
                upsert=[
                    row(
                        entity_key="Knead",
                        position=1,
                        instruction="Knead for twelve minutes",
                        timings={"knead": "12m", "rest": "5m"},
                        scratch="sticky",
                    ),
                    row(entity_key="Proof", position=3, instruction="Proof for an hour", timings={"proof": "1h"}),
                ]
            ),
        },
    ).ref
    second = g.commit(ANN, second.id, second.version, CommitOptions(message="second draft")).ref
    # A partial row keeps the columns it lacks; the unset removes the change
    # set's own Proof row, as Cook, which the DELETE image names.
    second = g.save(
        COOK,
        second.id,
        second.version,
        {"step": KindEdits(upsert=[row(entity_key="Knead", instruction="Knead until smooth")], unset=["Proof"])},
    ).ref
    second = g.commit(COOK, second.id, second.version).ref
    main, m2 = merged(g.merge(ANN, second.id, main.id, main.version, [], CommitOptions(message="second", tag=True)))
    g.release(ANN, BREAD, m2.id, release.version)
    # Work the change set does not commit: a partial row on insert stores
    # null for each column it lacks.
    g.save(ANN, second.id, second.version, {"tasting": KindEdits(upsert=[row(entity_key="First", score=5)])})

    scrap = g.branch(COOK, main.id, "scrap")
    scrap = g.save(
        COOK,
        scrap.id,
        scrap.version,
        {"cover": KindEdits(delete=["Cover"]), "utensil": KindEdits(upsert=[row(entity_key="Whisk", name="Whisk")])},
    ).ref
    g.discard(COOK, scrap.id, scrap.version)

    # Before the gain: rows, images and commits without the utensil's name.
    h = Engine(before_gain, SqliteAdapter(before_gain, graph="menu", clock=clock).storage(client), **options)
    lunch = h.create_primary(COOK, BREAD, "main")
    today = h.branch(COOK, lunch.id, "today")
    today = h.save(
        COOK,
        today.id,
        today.version,
        {
            "step": KindEdits(upsert=[row(entity_key="Knead", position=1, instruction="Slice", timings={})]),
            "utensil": KindEdits(upsert=[row(entity_key="Knife")]),
        },
    ).ref
    today = h.commit(COOK, today.id, today.version, CommitOptions(message="today")).ref
    lunch, served = merged(h.merge(COOK, today.id, lunch.id, lunch.version, [], CommitOptions(message="lunch", tag=True)))
    h.release(COOK, BREAD, served.id, 0)

    # After it: the fixture's descriptor, which declares the name.
    i = Engine(DESCRIPTOR, SqliteAdapter(DESCRIPTOR, graph="menu", clock=clock).storage(client), **options)
    dinner = i.branch(ANN, lunch.id, "dinner")
    dinner = i.save(
        ANN,
        dinner.id,
        dinner.version,
        {"utensil": KindEdits(upsert=[row(entity_key="Knife", name="Bread knife"), row(entity_key="Board", name="Bread board")])},
    ).ref
    dinner = i.commit(ANN, dinner.id, dinner.version, CommitOptions(message="dinner")).ref
    merged(i.merge(ANN, dinner.id, lunch.id, lunch.version, [], CommitOptions(message="supper", tag=True)))


# A character that splits a line for some reader: a control character, NEL,
# or a line or paragraph separator.
LINE_BREAK = re.compile("[\u0000-\u001f\u007f\u0085  ]")


def literal(value: Any, where: str) -> str:
    """A value as an SQL literal: text quoted with '' doubled, an integer as
    written, NULL."""
    if value is None:
        return "NULL"
    if isinstance(value, str):
        assert LINE_BREAK.search(value) is None, f"{where} holds no line break"
        return "'" + value.replace("'", "''") + "'"
    if isinstance(value, int) and not isinstance(value, bool):
        return str(value)
    raise AssertionError(f"{where}: the layout holds text, integers and NULL only, not {value!r}")


def dump_database(connection: sqlite3.Connection) -> str:
    """A database as the SQL text typescript.sql holds, as dumpDatabase
    writes it: the layout's statements, then one INSERT per row naming every
    column, the tables in the layout's order and each table's rows by
    primary key."""
    lines = [statement + ";" for statement in sqlite_layout()]
    for local in SQLITE_TABLES:
        name = default_table_name(local)
        columns = [r[0] for r in connection.execute("SELECT name FROM pragma_table_info(?1) ORDER BY cid", [name])]
        key = "history_id" if local.endswith("_history") else "id"
        for values in connection.execute(f"SELECT {', '.join(columns)} FROM {quote(name)} ORDER BY {key}"):
            out = ", ".join(literal(value, f"{name}.{column}") for column, value in zip(columns, values))
            lines.append(f"INSERT INTO {quote(name)} ({', '.join(columns)}) VALUES ({out});")
    return "\n".join(lines) + "\n"


def test_the_script_replayed_through_this_adapter_writes_typescript_sql(monkeypatch: pytest.MonkeyPatch) -> None:
    """The TypeScript script's writes, through this adapter and the engine,
    with the script's clock and its seeded ids, store what the TypeScript
    adapter stored: the database dumps as typescript.sql, byte for byte, and
    reads back as typescript.json. The ids are the adapter's own, made by its
    _new_id, which the test seeds for its run alone."""
    monkeypatch.setattr(sqlite_module, "_new_id", seeded_ids(ID_SEED))
    connection = sqlite3.connect(":memory:", isolation_level=None)
    try:
        write_database(connection)
        got = dump_database(connection)
        want = SQL.read_text(encoding="utf-8")
        if got != want:
            for i, (a, b) in enumerate(zip(got.split("\n"), want.split("\n"))):
                assert a == b, f"line {i + 1} of typescript.sql"
        assert got == want
        assert read_vectors(connection) == READS.read_text(encoding="utf-8")
    finally:
        connection.close()
