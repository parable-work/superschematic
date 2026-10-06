# Version graph (Python)

`superschematic-versiongraph` (module `superschematic_versiongraph`) is the
version graph for Python (D17 and D19 in `docs/DECISIONS.md`): the core
(`runtime/versiongraph/rust`) as a PyO3 extension module built with
maturin, with typed `compose`, `merge`, `diff`, `content_hash` and
`validate` over its JSON contract (`runtime/versiongraph/README.md`); the
engine, which runs every graph operation over a storage adapter; its
Postgres adapter and its SQLite adapter (D32); and the base of the typed
facade pygen generates per graph. It runs on CPython 3.9 and newer; one abi3 wheel per platform serves
them all.

| Path | Holds |
| --- | --- |
| `src/lib.rs` | The crate `superschematic-versiongraph-python`: the extension module `superschematic_versiongraph._native`, whose `run(operation, input)` takes the JSON input as bytes and returns `(ok, output)` with the core's output or error document. It calls the core natively, with the GIL released. PyO3 is its dependency, not the core's. |
| `superschematic_versiongraph/__init__.py` | `VersionGraph`, the module-level operations, `run` and `VersionGraphError`. |
| `superschematic_versiongraph/contract.py` | The contract's types, as `TypedDict`s and `Literal`s, every member named as the contract names it. |
| `superschematic_versiongraph/engine.py` | `Engine`: create_primary, branch, save, commit, seal, merge, rebase, revert, release, released, materialize, compose, diff, history, discard, sweep and run_sweeper, ported from the Go engine with its rules and error codes. |
| `superschematic_versiongraph/storage.py` | The storage protocol the engine runs over (`Storage`, `Tx`) and the values it takes and returns. |
| `superschematic_versiongraph/errors.py` | The named errors, each with its stable `code`, and `error_code`. |
| `superschematic_versiongraph/canonical.py` | The canonical row rules, ported from the Go module's package `canonical`. |
| `superschematic_versiongraph/exactjson.py` | The JSON reader and writer rows travel through, which keep every number's digits. |
| `superschematic_versiongraph/postgres.py` | `PostgresAdapter`, which builds its statements from the descriptor at run time, its `Client` protocol, and `psycopg_client`, the psycopg 3 binding. |
| `superschematic_versiongraph/sqlite.py` | `SqliteAdapter`, which holds every graph in one fixed layout of tables, `sqlite_layout`, its `Client` protocol, `is_unique_violation`, and `sqlite_client`, the binding over the standard library's `sqlite3`. |
| `superschematic_versiongraph/facade.py` | `VersionGraphFacade`, which each generated `<Name>Graph` extends, and the types it returns. |
| `tests/` | Every core vector through the package, the binding's own tests, every scenario and canonical vector through the engine and each adapter, and the adapters', the sweeper's and the facade's own tests. |

## Use the core

```python
import superschematic_versiongraph as vg

result = vg.compose({"descriptor": descriptor, "base": base, "overlay": overlay})
tree, findings = result["tree"], result["findings"]
merged = vg.merge({"descriptor": descriptor, "base": base, "ours": ours, "theirs": theirs})
changes = vg.diff({"descriptor": descriptor, "from": base, "to": tree})["changes"]
content_hash = vg.content_hash({"descriptor": descriptor, "tree": tree})["contentHash"]

try:
    vg.validate({"descriptor": descriptor, "tree": {"recipe_step": []}})
except vg.VersionGraphError as error:
    print(error.code)  # "unknown_kind"
```

- The operations are synchronous. Inputs and outputs are plain dicts and
  lists, keyed as the contract names them; `contract.py` types them.
- A refused input raises `VersionGraphError` with the contract's `code` and
  the core's `message`.
- `run(operation, json)` takes JSON text or bytes and returns the output
  document as text, for a caller with its own JSON handling. An operation
  the core does not have raises `ValueError`.
- The module-level functions use `json`, which keeps an integer's digits
  but reads a number with a fraction or an exponent as a float. For rows
  whose numbers a double does not hold, build a
  `VersionGraph(loads=..., dumps=...)` with a codec that keeps them, or use
  `run`.

## Use the engine

```python
import psycopg
from superschematic_versiongraph.engine import Engine, KindEdits
from superschematic_versiongraph.postgres import PostgresAdapter, psycopg_client

connection = psycopg.connect(url, autocommit=True)
adapter = PostgresAdapter(descriptor)
engine = Engine(descriptor, adapter.storage(psycopg_client(connection)), schema_epoch=1, snapshot_every=32)
main = engine.create_primary(actor, root, "main")
draft = engine.branch(actor, main.id, "draft")
saved = engine.save(actor, draft.id, draft.version, {"step": KindEdits(upsert=[row_json])})
committed = engine.commit(actor, draft.id, saved.ref.version)
```

- The engine is synchronous and takes the Go engine's arguments in the same
  order, in snake case, with the actor first on every write. Every id is a
  UUID, read in its canonical form (base62) or hyphenated, and returned
  canonical.
- A canonical row is JSON text: a tree is `Dict[str, List[str]]`, and a
  conflict's values, a change's row and a resolution's `value` are JSON
  text, so a wide integer or numeric keeps its digits.
- The named errors are exceptions with the stable `code` every language's
  engine shares; `error_code(err)` returns it, or the core's code.
- `run_sweeper(interval, options, on_pass, stop)` runs a sweep now and then
  every `interval` (a `timedelta`) until the `threading.Event` `stop` is
  set, and returns. A pass under way finishes first.
- psycopg 3 is the `postgres` extra (`superschematic-versiongraph[postgres]`).
  No module imports it until `psycopg_client` is called. Its client runs
  each statement through a raw cursor and reads every column as the text
  Postgres wrote, so psycopg's type adaptation touches no value. Over a
  connection it runs one transaction at a time, as a savepoint when the
  caller holds a transaction on it; over a `psycopg_pool.ConnectionPool`
  each transaction takes a connection of its own.

The generated facade (`<module>/versiongraph_<name>.py` in a Python types
package whose schema declares a graph) wraps the engine with typed trees
and edits; "Use the engine from Python" in the version graphs reference
shows it.

## Use the engine on SQLite

```python
import sqlite3
from superschematic_versiongraph.engine import Engine
from superschematic_versiongraph.sqlite import SqliteAdapter, sqlite_client

connection = sqlite3.connect("recipes.sqlite", isolation_level=None)
client = sqlite_client(connection)
adapter = SqliteAdapter(descriptor, graph="recipe")
adapter.create_tables(client)
engine = Engine(descriptor, adapter.storage(client), schema_epoch=1, snapshot_every=32)
```

- The SQLite adapter is the TypeScript package's
  (`runtime/versiongraph/README.md`, "SQLite"), statement for statement and
  stored form for stored form, so a file one writes the other reads: nine
  `STRICT` tables, the same for every graph, each row carrying its graph's
  name, history written in the statements that change a row, and every
  value stored canonical. It reads only the descriptor's kinds.
- `SqliteAdapter(descriptor, graph=..., table_name=..., clock=...)`:
  `table_name` names each table and index from its local name
  (`default_table_name`, `graph_` before it, when absent), and `clock`
  gives a transaction's time in whole microseconds since the Unix epoch
  (the system clock, `time.time_ns() // 1000`, when absent), inside
  +/-(2^53-1). A transaction reads it once, when it begins: on a
  connection of its own once `BEGIN IMMEDIATE` holds the write lock, and
  inside a transaction the caller holds once its savepoint is set; one
  begun inside another takes the outer one's time. A stored integer outside
  +/-(2^53-1), which the TypeScript adapter does not read, is refused.
  `sqlite_layout(table_name)` lists the layout's statements for a caller
  that runs its own migrations, and `create_tables` runs them, reading the
  clock once as every transaction does.
- `sqlite_client(connection)` binds an open `sqlite3.Connection`, which
  must be opened with `isolation_level=None` (or `autocommit=True` from
  Python 3.12) so the module never begins a transaction on its own: the
  client issues `BEGIN IMMEDIATE`, `SAVEPOINT`, `RELEASE`, `COMMIT` and
  `ROLLBACK` itself. It refuses a SQLite older than 3.37.0
  (`MIN_SQLITE_VERSION`, the first with `STRICT` tables) or one without
  the JSON functions, and turns the connection's foreign keys on, which
  SQLite ignores inside a transaction.
- Each transaction begins with `BEGIN IMMEDIATE`, which waits for the
  file's write lock as long as the connection's `timeout` says and then
  fails with SQLite's busy error. One begun inside another, or while the
  caller holds a transaction on the connection, is a savepoint of it, so
  the graph's writes commit or roll back with the caller's. Over one
  connection the client runs one transaction at a time; bind one client
  per connection.
- A name already taken is `name_taken`, from the unique index on live
  refs' names. `is_unique_violation` reads that refusal: by the error's
  `sqlite_errorcode` (2067) from Python 3.11, and before it, when the
  sqlite3 module's errors carry no code, by SQLite's words for it.
- Rows travel as JSON text read by `exactjson`, so a number keeps its
  digits from the row written to every row read; no value goes through a
  `float`.

## Development

```
cd runtime/versiongraph/python
uv run pytest -q                            # builds the extension with maturin, then the tests
uv run --python 3.9 --isolated pytest -q    # the same on the 3.9 floor
make python                                 # from the repository root: both, with cargo fmt and clippy
make versiongraph-scenarios-python          # from the repository root: the SQLite pass, then the Postgres tests
```

uv builds the package with maturin into `.venv` and rebuilds it when the
binding's or the core's Rust sources change (`[tool.uv] cache-keys`). The
build needs cargo. `tests/test_vectors.py` runs every vector in
`runtime/versiongraph/testdata/vectors` as JSON text through `run` and
through the typed operations with an exact codec, comparing each output
with the vector's `expect`, member order and number digits included, and
through the module-level operations when `json` reads the vector without
loss. It also checks every vector's input and output against the types
in `contract.py` and fails when a member or literal there appears in no
vector. `tests/test_binding.py` checks the errors, that many calls do not
grow the process, and that the core runs with the GIL released.
`tests/test_engine.py` checks the engine's rules that need no database,
and `tests/test_scenarios.py` reads every scenario file and checks the
scenario format's rules without one. The SQLite tests need no database
server and run everywhere the others do, on Python 3.9 too:
`tests/test_scenarios.py` runs every scenario through the engine and the
SQLite adapter, in an in-memory database, and `tests/test_sqlite.py` holds
the adapter's own rules: the version fences, a taken name, the history it
writes, `STRICT` tables and foreign keys, two graphs in one file, the name
function, the clock, its ids, a second connection's wait for the write
lock, savepoints and the caller's transaction, a number's digits end to
end, every canonical vector as a round trip, and what the binding
refuses. `tests/test_sqlite_vectors.py` holds the adapter to the shared
SQLite vectors (`runtime/versiongraph/testdata/sqlite`): its layout is
`layout.json`'s, the database the TypeScript adapter wrote reads back as
`typescript.json` byte for byte, and one graph reads none of another's.

The Postgres tests need `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` and
skip without it; `make versiongraph-scenarios-python` runs the SQLite pass
first, then fails without it.
`tests/test_scenarios.py` runs every scenario in
`runtime/versiongraph/testdata/scenarios` through the engine and the
adapter, each in a schema of its own holding the fixture's DDL.
`tests/test_canonical.py` runs every canonical vector through the rules and
checks each rendering against Postgres. `tests/test_adapter.py`,
`tests/test_sweeper.py` and `tests/test_facade.py` hold the adapter's, the
sweeper's and the facade's own rules: a ref lock another transaction waits
for, the version fences, the sweep lock, a prune that keeps pinned images,
the psycopg client's savepoint and pool, a pass skipped while the lock is
held, an idle draft written during a pass, and a relation written as its
target's key.

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).
