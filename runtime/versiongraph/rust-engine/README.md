# superschematic-versiongraph-engine

The Rust engine of the version graph (D17, D19 and D32 in
`docs/DECISIONS.md`): every graph operation, written once over a storage
adapter, with a Postgres adapter and a SQLite adapter. It calls the core
(`../rust`, crate `superschematic-versiongraph`) natively through a path
dependency. `../README.md` is the contract it keeps: the descriptor,
canonical rows, the engine's rules and error codes, and the scenario
format.

```
src/engine.rs      Engine: create_primary, branch, save, commit, seal, merge, rebase,
                   revert, release, released, materialize, compose, diff, history, discard
src/sweep.rs       Engine::sweep and Engine::run_sweeper
src/storage.rs     the Storage and Tx traits an adapter implements
src/postgres/      the Postgres adapter, its Client seam and the tokio-postgres binding
src/sqlite/        the SQLite adapter, its fixed layout, its Client seam and the rusqlite binding
src/canonical/     the canonical row rules for what Postgres returns
src/core.rs        the core's operations over trees of canonical rows
src/error.rs       Error and its stable codes
```

## Use

```rust
use std::sync::Arc;
use superschematic_versiongraph_engine::{postgres, CommitOptions, Engine, Options};

let (client, connection) = tokio_postgres::connect(url, tokio_postgres::NoTls).await?;
tokio::spawn(connection);
let adapter = Arc::new(postgres::Adapter::new(descriptor, postgres::Options::default())?);
let storage = Arc::new(adapter.storage(postgres::TokioPostgres::new(client)));
let engine = Engine::new(descriptor, storage, Options { schema_epoch: 1, snapshot_every: 64, ..Options::default() })?;

let main = engine.create_primary(actor, root, "main").await?;
let draft = engine.branch(actor, &main.id, "first draft").await?;
let saved = engine.save(actor, &draft.id, draft.version, &edits).await?;
let committed = engine.commit(actor, &draft.id, saved.ref_.version, &CommitOptions::default()).await?;
let merged = engine.merge(actor, &draft.id, &main.id, main.version, &[], &CommitOptions { message: "v1".into(), tag: true }).await?;
```

Every operation is `async` and runs in one transaction of its `Storage`.
Rows are canonical rows (`serde_json::Value` objects keyed by column name),
and every id is a UUID string, taken in its canonical form (base62) or
hyphenated and returned canonical. Every write takes an actor and every
write through a ref the ref's expected version. The engine's errors are
`Error`; `Error::code()` is the stable code every language's engine shares
(`version_conflict`, `primary_merge_only`, ...), or the core's code for an
input it refused.

The generated Rust types crate of a schema that declares a graph carries a
typed facade over this crate (`<Name>Graph` in `src/versiongraph_<name>.rs`),
which turns typed edits into canonical rows and canonical rows into typed
trees.

## The Postgres adapter

`postgres::Adapter::new(descriptor, options)` reads a version 3 descriptor
and builds its statements from it at run time. It reaches Postgres through
`postgres::Client`, a small seam of two traits (begin a transaction; query,
execute, commit, roll back) over `postgres::SqlValue` arguments and columns.
`postgres::TokioPostgres` binds one tokio-postgres connection and is on by
default (the `tokio-postgres` feature); operations on one binding take
turns. A service that runs operations side by side, or inside a transaction
it holds, implements `Client` over its own pool or transaction.

Each row is read as `to_jsonb` text and turned into its canonical row by
`canonical::postgres_row`, the rules the Go module's package `canonical`
implements.

## The SQLite adapter

`sqlite::Adapter::new(descriptor, options)` reads a version 3 descriptor and
keeps every graph in one fixed layout of nine `STRICT` tables (D32,
`../README.md`, "SQLite"), the same for every graph, under the names
`options.table_name` gives (`graph_ref` and so on by default), with every
row carrying `options.graph`. It is the TypeScript adapter
(`../typescript/src/sqlite.ts`) statement for statement, and stores what it
does, so a file one writes the other reads. It writes history itself, as
Postgres's triggers do: `_version`, an image of every insert and update,
and a delete's image naming its actor. A transaction reads its time once,
from `options.clock` (microseconds since the Unix epoch; the system clock
by default), after it has taken the file's write lock; `create_tables`
reads it too, as the TypeScript adapter's does, and a row's id is drawn
before a key the row lacks, so a seeded run writes the TypeScript adapter's
file byte for byte (`src/sqlite/write_parity.rs`, a test of the crate).

```rust
use std::sync::Arc;
use superschematic_versiongraph_engine::{sqlite, Engine, Options};

let client = sqlite::Rusqlite::new(rusqlite::Connection::open("recipes.sqlite")?);
let adapter = Arc::new(sqlite::Adapter::new(descriptor, sqlite::Options { graph: "recipe".into(), ..Default::default() })?);
adapter.create_tables(&client).await?;
let storage = Arc::new(adapter.storage(client).await?);
let engine = Engine::new(descriptor, storage, Options { schema_epoch: 1, ..Options::default() })?;
```

It reaches SQLite through `sqlite::Client`, a seam of two traits as the
Postgres one is: a client begins a transaction and runs a connection
setting (`exec`), and a transaction runs statements with numbered `?1`
placeholders over `sqlite::SqlValue`, commits and rolls back. A client's
error carries SQLite's extended result code (`sqlite::result_code` reads it
from the engine's error). `sqlite::Rusqlite` binds one rusqlite connection,
with the SQLite rusqlite bundles, behind the `rusqlite` feature, which is
off by default so a Postgres user does not compile SQLite. On a connection
in autocommit mode it begins each transaction with `BEGIN IMMEDIATE`;
inside a transaction the caller holds on the connection
(`Rusqlite::connection`) it begins a savepoint, so the operation commits or
rolls back with the caller's transaction. A transaction an operation
dropped is rolled back at once. rusqlite's calls are synchronous, so each
statement blocks the executor while SQLite runs it. `storage` and
`create_tables` refuse a SQLite older than 3.37.0, and one whose `json_each`
and `json_extract` do not work (SQLite builds them in from 3.38.0, and 3.37
has them in builds with JSON1), and `storage` turns the connection's
foreign keys on and refuses one where they stay off. A time, from the clock
when a transaction begins or stored and read back, and every integer read
back, must lie within 2^53 - 1 either side of zero, as the TypeScript
adapter holds them, so a file one adapter writes the other reads. Its tests
hold it to the SQLite vectors (`../testdata/sqlite`): its layout under the
default names is `layout.json`, and the database the TypeScript adapter
wrote, `typescript.sql`, reads back through it and the engine as
`typescript.json`.

## Test

```
cargo test --features rusqlite               # the rules, the vectors, the SQLite adapter's tests, the SQLite
                                             # vectors and every scenario on SQLite, and every Postgres test
                                             # that has a database
make versiongraph-scenarios-rust             # from the repository root: every scenario and the SQLite adapter's
                                             # tests on SQLite, then every scenario, canonical vector rendering,
                                             # adapter and sweeper test against Postgres
```

The SQLite tests need no server and run with or without a database. The
Postgres tests read `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` and skip
without it; `make versiongraph-scenarios-rust` runs the SQLite pass first
and then fails without it. Each Postgres test creates a schema of its own,
applies `../testdata/fixture/create.sql` and drops the schema after.
