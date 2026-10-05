"""The shared SQLite vectors (runtime/versiongraph/testdata/sqlite, whose
README gives each file's shape) through the Python adapter: its layout's
statements are layout.json's, a database the TypeScript adapter wrote
(typescript.sql) reads back through the adapter and the engine as
typescript.json, byte for byte, and through each graph's adapter nothing of
the other graph reads. The Python counterpart of the TypeScript package's
test/sqlite-vectors-cases.ts. None needs a database server."""

import json
import sqlite3
from typing import Any, Callable, Dict, Iterator, List, Optional, Union

import pytest
from support import DESCRIPTOR, TESTDATA

from superschematic_versiongraph.engine import Engine, TreeResult
from superschematic_versiongraph.errors import error_code
from superschematic_versiongraph.exactjson import loads, write_string
from superschematic_versiongraph.sqlite import SqliteAdapter, default_table_name, sqlite_client, sqlite_layout
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
