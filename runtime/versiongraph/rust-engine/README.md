# superschematic-versiongraph-engine

The Rust engine of the version graph (D17 and D19 in `docs/DECISIONS.md`):
every graph operation, written once over a storage adapter, with a Postgres
adapter. It calls the core (`../rust`, crate `superschematic-versiongraph`)
natively through a path dependency. `../README.md` is the contract it keeps:
the descriptor, canonical rows, the engine's rules and error codes, and the
scenario format.

```
src/engine.rs      Engine: create_primary, branch, save, commit, seal, merge, rebase,
                   revert, release, released, materialize, compose, diff, history, discard
src/sweep.rs       Engine::sweep and Engine::run_sweeper
src/storage.rs     the Storage and Tx traits an adapter implements
src/postgres/      the Postgres adapter, its Client seam and the tokio-postgres binding
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

## Test

```
cargo test                                   # the rules, the vectors, and every Postgres test that has a database
make versiongraph-scenarios-rust             # from the repository root: every scenario, canonical vector
                                             # rendering, adapter and sweeper test against Postgres
```

The Postgres tests read `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` and
skip without it; `make versiongraph-scenarios-rust` fails without it. Each
test creates a schema of its own, applies `../testdata/fixture/create.sql`
and drops the schema after.
