# superschematic-versiongraph-engine

The Rust engine of the version graph (D17, D19 and D32 in
`docs/DECISIONS.md`): every graph operation, written once over a storage
adapter, with a Postgres adapter and a SQLite adapter, whose fixed layout
also runs on Postgres without a trigger and inside a synchronous
transaction a host holds (D58). It calls the core
(`../rust`, crate `superschematic-versiongraph`) natively through a path
dependency. `../README.md` is the contract it keeps: the descriptor,
canonical rows, the engine's rules and error codes, and the scenario
format.

```
src/engine.rs      Engine: create_primary, branch, save, commit, seal, merge, rebase,
                   revert, release, released, materialize, compose, diff, history, discard;
                   EngineOn, the same over a storage it borrows; Engine::run_on and ready
src/sweep.rs       Engine::sweep and Engine::run_sweeper
src/storage.rs     the Storage and Tx traits an adapter implements
src/postgres/      the Postgres adapter, its Client seam and the tokio-postgres binding
src/fixed/         the fixed layout in either dialect (FixedLayout, Dialect), its Client seam,
                   and HostTx and HostClient, a host's synchronous transaction as its client
src/sqlite/        the SQLite adapter: the fixed layout on SQLite under its names, the version
                   check, SQLite's result codes and the rusqlite binding
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

`engine.on(&storage)` runs the same operations over a storage the caller
lends, which need not be `'static`: it may borrow a transaction the
caller holds. `engine.run_on(&storage, |e| e.save(...))` runs one of them
synchronously, with no executor: `ready` polls the operation's future
once, and refuses one that is not ready. An operation awaits nothing but
its storage, so over a storage whose futures are always ready (the fixed
layout over a host's transaction, below) it runs to its end on the
calling thread. `Engine::unbound` builds an engine with no storage of its
own, for a host that lends one to every operation. The operation's future
borrows its arguments, so bind them before the closure.

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

`sqlite::Adapter` is `fixed::FixedLayout`, and `sqlite::SqliteStorage`
`fixed::FixedStorage`: the fixed layout on SQLite, under the names it has
had since D32. `sqlite::Adapter::new(descriptor, options)` reads a version 3 descriptor and
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

## The fixed layout on Postgres

`fixed::FixedLayout::with_dialect(fixed::Dialect::Postgres, descriptor,
options)` keeps a graph in the same nine tables on Postgres, writing
history itself, so it needs no trigger, which a D16 behavior may not
create (D58). `fixed::layout(Dialect::Postgres, name)` gives the
statements that create them. The statements are SQLite's but where the
two databases word them differently:

| What | SQLite | Postgres |
|---|---|---|
| The layout's columns | `INTEGER`, `TEXT`, `STRICT` tables | `BIGINT`, `TEXT COLLATE "C"` (so text compares and orders by bytes, as SQLite's does), no `STRICT` |
| The ref's keys to its base and head commits | declared with the ref table | `ALTER TABLE ... DROP CONSTRAINT IF EXISTS` and `ADD CONSTRAINT` after the commit table (`ref_base_commit`, `ref_head_commit`), since a key needs its table |
| A list argument: the pins an image read takes, the commits a patch read takes | `json_each(?3)` and `json_extract(p.value, '$[0]')` | `jsonb_array_elements(CAST(CAST(?3 AS TEXT) AS JSONB))` and `p.value ->> 0` |
| Whether a commit is snapshotted or tagged, as a read returns it | the condition, an integer | `CASE WHEN ... THEN 1 ELSE 0 END` |
| A parameter compared with an integer literal or a depth (`seal`, a walk's limit) | `?5` | `CAST(?5 AS BIGINT)` |
| A new ref whose name is taken | `ref_live_name` refuses it (`SQLITE_CONSTRAINT_UNIQUE`) | `ON CONFLICT (graph, root_id, name) WHERE deleted_at IS NULL DO NOTHING`, and no row is `name_taken`: an error would abort the transaction |
| A prune with no batch | `LIMIT -1` | `LIMIT NULL` |
| Binding a client | the version and JSON checks, foreign keys on | nothing |

Every parameter of the Postgres statements is `TEXT` or `BIGINT`, by its
column or a cast, and every column it reads is text or an integer, so a
client binds `SqlValue`s as text, `int8` and typed nulls. Both dialects
keep one writer per graph: `lock_ref` reads a ref as `read_ref` does,
`next_sequence` is the root's highest sequence plus 1 and `sweep_lock` is
true, so on Postgres the caller serializes a graph's writers, as D58's
engine does with a lock on its namespace.

## Inside a host's transaction

A host that holds a synchronous transaction, as D58's engine holds the
call a behavior runs in, implements `fixed::HostTx` over it: `query` and
`execute` take a statement whose placeholders are `?`, each taking the
next argument (a host on Postgres numbers them `$1`, `$2`, ...), and
return rows of `SqlValue`s; `begin`, `commit` and `rollback` bound each
engine operation, and issue nothing by default, as the TypeScript
adapter's `callerTransaction` does, so the host rolls back what a failed
operation wrote. A host that goes on after an operation fails runs each in
a savepoint instead: on Postgres a failed statement leaves the
transaction refusing the rest. `fixed::HostClient::new(&mut host)` is
then the layout's client: it rewrites each statement's numbered
placeholders to `?` with the arguments to match and runs it at once, so
its futures are ready at their first poll. It reaches the host's
transaction, which need be neither `Send` nor `Sync`, only on the thread
that bound it. `FixedLayout::bind` binds it without the SQLite checks,
whose `PRAGMA` and `sqlite_version` a behavior's transaction refuses, so
a host binds a storage per call:

```rust
use std::sync::Arc;
use superschematic_versiongraph_engine::fixed::{Dialect, FixedLayout, HostClient};
use superschematic_versiongraph_engine::{Engine, Options};

let adapter = Arc::new(FixedLayout::with_dialect(Dialect::Postgres, descriptor, options)?);
let engine = Engine::unbound(descriptor, Options { schema_epoch: 0, ..Options::default() })?;
// In each call, over the host's transaction:
let storage = adapter.bind(HostClient::new(&mut host));
let main = engine.run_on(&storage, |e| e.create_primary(actor, root, "main"))?;
```

## Test

```
cargo test --features rusqlite               # the rules, the vectors, the SQLite adapter's tests, the SQLite
                                             # vectors and every scenario on SQLite, also inside a host's
                                             # transaction, and every Postgres test that has a database
make versiongraph-scenarios-rust             # from the repository root: every scenario and the SQLite adapter's
                                             # tests on SQLite, then every scenario, canonical vector rendering,
                                             # adapter and sweeper test against Postgres, and every scenario on
                                             # the fixed layout on Postgres inside a host's transaction
```

The SQLite tests need no server and run with or without a database. The
Postgres tests read `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` and skip
without it; `make versiongraph-scenarios-rust` runs the SQLite pass first
and then fails without it. Each Postgres test creates a schema of its own,
applies `../testdata/fixture/create.sql` and drops the schema after. The
sweeper's tests run side by side and hold and take the graph's sweep lock,
an advisory lock, which Postgres keys to the database rather than the
schema, so each creates its schema in a database of its own and drops the
database after; the variable's role must be able to create databases.

Inside a host's transaction (`tests/host.rs`, and in `tests/scenarios.rs`
every scenario again), the host is a test's own `HostTx` over plain
rusqlite or over the synchronous `postgres` crate (`tests/support/hosts.rs`),
which runs each step in a transaction of the host's and each operation in
a savepoint, and each operation runs through `Engine::run_on` with no
executor. The `postgres` client blocks on a runtime of its own, so those
tests run on none. On Postgres each scenario runs in a schema of its own
that holds the fixed layout. The runner runs the steps for `sqlite`, the
fixed layout's backend, and an sql step's `sqlite` statement, on Postgres
with its placeholders numbered `$1` and SQLite's `json_extract` and
`CASE ... WHEN 1 THEN 'true'` in Postgres's words. The Postgres test host
binds only text, `int8` and nulls and reads only text and integers, so
the layout's Postgres statements are held to that.
