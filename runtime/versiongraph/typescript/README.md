# Version graph (TypeScript)

`@superschematic/versiongraph` is the version graph for TypeScript: the
version-graph core (`runtime/versiongraph/rust`) built for
`wasm32-unknown-unknown`, with typed `compose`, `merge`, `diff`,
`contentHash` and `validate` over its JSON contract
(`runtime/versiongraph/README.md`); the engine, which runs every graph
operation over a storage adapter; the engine's Postgres and SQLite
adapters; and the base of the facade tsgen generates per graph (D19 and
D32 in `docs/DECISIONS.md`).

| Entry | Holds |
| --- | --- |
| `@superschematic/versiongraph` | The core: `init` and `initSync`, the operations, `VersionGraphError` and the contract's types. Runs in the browser, bun and Node, has no dependencies, and drives the module through its C ABI exports (`vg_alloc`, `vg_<op>`, `vg_free`, `vg_dealloc`) with no generated glue. |
| `@superschematic/versiongraph/engine` | `Engine` and `SyncEngine`, the storage interfaces (`Storage` and `Tx`, `SyncStorage` and `SyncTx`), the named errors and `errorCode`, the canonical rules (`canonicalValue`, `canonicalRow`) and the exact JSON codec they read with (`parseJson`, `stringifyJson`, `JsonNumber`). |
| `@superschematic/versiongraph/postgres` | `PostgresAdapter`, its `Client` interface, and `pgPool` and `pgClient`, which bind the npm package `pg`. |
| `@superschematic/versiongraph/sqlite` | `SqliteAdapter`, its `SqliteClient` interface and `SqliteError`, `sqliteLayout` and `sqliteTables`, and `nodeSqlite` and `bunSqlite`, which bind `node:sqlite` and `bun:sqlite`. |
| `@superschematic/versiongraph/facade` | `VersionGraphFacade`, which each generated `<Name>Graph` extends, and the types it returns. |

## The core

```ts
import { init, VersionGraphError } from "@superschematic/versiongraph";

const graph = await init();
const { tree, findings } = graph.compose({ descriptor, base, overlay });
```

- `init(source?, options?)` compiles and instantiates the module. `source`
  is its bytes, a URL (a `file:` URL is read from disk under bun and Node;
  any other is fetched), a `Response` or a promise of one, or a compiled
  `WebAssembly.Module`. Without it, `init` loads
  `superschematic_versiongraph.wasm` from next to `index.js`
  (`new URL(..., import.meta.url)`), which a bundler that understands that
  pattern copies into the build.
  The file is also exported as
  `@superschematic/versiongraph/superschematic_versiongraph.wasm`.
- `initSync(source?, options?)` compiles and instantiates the module
  without awaiting, for a caller that cannot await, such as a behavior of
  D16's engine. `source` is the module's bytes (an `ArrayBuffer` or an
  `ArrayBufferView`) or a compiled `WebAssembly.Module`. Without it,
  `initSync` reads `superschematic_versiongraph.wasm` from next to
  `index.js` with the `node:fs` that `process.getBuiltinModule` returns
  under bun and Node; anywhere else it throws, and the caller passes the
  module or calls `init`. It takes `init`'s options, and returns the same
  `VersionGraph`. A browser may refuse to compile a large module
  synchronously on its main thread.
- The operations are synchronous. A refused input throws a
  `VersionGraphError` with the contract's `code` and the core's `message`.
- `run(operation, json)` takes and returns JSON text, for a caller with its
  own JSON handling. `options.parse` and `options.stringify` replace the
  JSON codec of the typed operations, for rows whose numbers do not fit a
  double.
- `src/contract.ts` holds the contract's types, one module, every member
  named as the contract names it.

## The engine and the Postgres adapter

```ts
import pg from "pg";
import { Engine } from "@superschematic/versiongraph/engine";
import { PostgresAdapter, pgPool } from "@superschematic/versiongraph/postgres";

const adapter = new PostgresAdapter(descriptor, { historyActorSetting: "superschematic.history_actor_id" });
const engine = await Engine.create(descriptor, adapter.storage(pgPool(new pg.Pool())), { schemaEpoch: 1, snapshotEvery: 64 });
const main = await engine.createPrimary(actor, root, "main");
```

The engine ports the Go engine (`runtime/versiongraph/go/engine`)
operation for operation: `createPrimary`, `branch`, `save`, `commit`,
`seal`, `merge`, `rebase`, `revert`, `release`, `released`, `materialize`,
`compose`, `diff`, `history`, `discard`, `sweep` and `runSweeper`, with the
walk ceiling, the snapshots, the schema-epoch check and the named errors
and their stable codes. A canonical row is JSON text, so a number keeps its
digits; durations are milliseconds; `runSweeper` stops when its
`AbortSignal` aborts, runs no pass when it has already aborted, and lets a
pass under way finish.

The operations are written once, as generator functions that yield each
storage call, and two drivers run them (D32). `Engine` awaits each call
over `Storage` and `Tx`, whose methods return promises. `SyncEngine` makes
each call over `SyncStorage` and `SyncTx`, which have `Tx`'s methods
returning their values (`transact<T>(fn: (tx: SyncTx) => T): T`), for a
database whose driver blocks, as SQLite's does in bun and Node. Either
driver sends a call's value back into the operation and throws a failed
call's error into it, so an operation that catches a storage error, as a
sweep catches a moved ref's `VersionConflictError`, does the same under
both.

```ts
import { initSync } from "@superschematic/versiongraph";
import { SyncEngine } from "@superschematic/versiongraph/engine";

const engine = new SyncEngine(initSync(), descriptor, storage, { schemaEpoch: 1, snapshotEvery: 64 });
const main = engine.createPrimary(actor, root, "main");
```

`SyncEngine` has `Engine`'s operations, arguments and errors, each
returning its value or throwing. An actor or an id it refuses throws before
a transaction begins, where `Engine` rejects; the entity keys of a save's
deletes and unsets and of a merge's or a rebase's resolutions are checked
inside the transaction, under both. A `SyncTx` method that returns a
promise, or a `transact` that does, ends the operation with a `TypeError`
naming it, and `SyncTx`'s methods with no value return `undefined`, not
`void`, so tsc refuses an async one (a class declares them `: undefined`).
`SyncEngine` is built over a core already instantiated, so it has no
`create`, and it has no `runSweeper`, since a loop that waits between
passes would block its thread; its host schedules `sweep`. The facade stays
asynchronous, over `Engine`.

The adapter builds its statements at run time from the descriptor
(version 3) and reaches Postgres through `Client`, whose `query` returns
every column as the text Postgres writes: no driver's type parsing touches
a date, a time, an interval, a numeric or a bigint, and the canonical rules
turn each row into its canonical form. `pgPool(pool)` runs each
transaction on a connection of a `pg.Pool`; `pgClient(client)` runs them
one at a time on one connection, and with `{ savepoint: true }` as
savepoints of a transaction the caller holds. `pg` is an optional peer
dependency: the bindings call the methods they need on what they are
given, so no entry imports it, and the core loads without it.

## The SQLite adapter

```ts
import { DatabaseSync } from "node:sqlite";
import { initSync } from "@superschematic/versiongraph";
import { SyncEngine } from "@superschematic/versiongraph/engine";
import { SqliteAdapter, nodeSqlite } from "@superschematic/versiongraph/sqlite";

const client = nodeSqlite(new DatabaseSync("recipes.sqlite"));
const adapter = new SqliteAdapter(descriptor, { graph: "recipe" });
adapter.createTables(client);
const engine = new SyncEngine(initSync(), descriptor, adapter.storage(client), { schemaEpoch: 1, snapshotEvery: 64 });
const main = engine.createPrimary(actor, root, "main");
```

`SqliteAdapter` is `SyncStorage` and `SyncTx` over one fixed layout of nine
`STRICT` tables (`ref`, `ref_history`, `commit`, `patch`,
`snapshot_entry`, `release`, `release_history`, `member` and
`member_history`), the same for every graph (D32). Every row carries the
graph's name, `options.graph`, so one file holds several graphs, and
`options.tableName` names each table and index from its local name
(`graph_` before it by default), so a D16 behavior can pass its
`sql.table`. It reads from the descriptor only its kinds' role columns,
value classes and `history`, and returns each row with every column its
kind declares, `null` where the stored row lacks one. `createTables` creates the layout where it is
missing, and `sqliteLayout(tableName)` returns its statements, one each,
with no trigger and no transaction control, for a caller that runs its own
migrations. `runtime/versiongraph/README.md` ("SQLite") holds the layout
and its rules.

The adapter does what Postgres's history triggers do, in the statements of
the transaction that changes a row: it sets `_version`, writes each
insert's and update's image at its new version and a delete's at the old
version plus 1, naming the delete's actor, and leaves the kind's excluded
columns out. A transaction reads its time once, from `options.clock`
(microseconds since the Unix epoch; the system clock by default), and the
adapter generates every id, a version-4 UUID in its canonical form. On a
connection of its own it turns foreign keys on when it is bound, begins
each transaction with `BEGIN IMMEDIATE`, and runs one begun inside another
as a savepoint; with `options.callerTransaction` it runs in the
transaction its caller holds and issues no transaction control, as a D16
behavior's `sql` requires. A taken name is the live-name index's
`SQLITE_CONSTRAINT_UNIQUE`, and `lockRef`, `nextSequence` and `sweepLock`
lean on SQLite's one writer.

`SqliteClient` is the shape of D16's `SqlDriver`: `run`, `get` and `all`
with positional parameters for numbered placeholders (`?1`), plain rows,
`undefined` for no row, and an error whose `code` is SQLite's extended
result code; `exec`, which only a connection of the adapter's own needs, is
for transaction control. `nodeSqlite(db)` and `bunSqlite(db)` bind an open
`DatabaseSync` and an open `bun:sqlite` `Database`. They call only the
methods they need on what they are given, so no entry imports a SQLite
module.

## Development

```
cd runtime/versiongraph/typescript
bun install --frozen-lockfile
bun run test     # cargo build for wasm32, tsc into dist/, the tests, the Node check
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL=postgres://... bun test test   # with the Postgres tests
make versiongraph-scenarios-ts   # from the repository root: the SQLite pass, then the Postgres tests, failing without the variable
```

The build needs cargo with the `wasm32-unknown-unknown` target
(`rustup target add wasm32-unknown-unknown`). `bun run test` builds the
package, type-checks the tests against the built `dist/`, runs every vector
in `runtime/versiongraph/testdata/vectors` through the package API
(`test/vectors.test.ts`), checks each way of loading the module
(`test/load.test.ts`), runs every canonical vector
(`test/canonical.test.ts`), reads every scenario file and checks the
scenario format's rules (`test/scenarios.test.ts`), runs every scenario on
SQLite through `SyncEngine` and the SQLite adapter, in memory, once through
`bun:sqlite` and once through `node:sqlite` in a transaction the runner
holds with every statement held to D16's rules for a behavior's SQL
(`test/scenarios.test.ts`), runs the SQLite adapter's own tests through
both bindings (`test/sqlite.test.ts`, over the cases in
`test/sqlite-cases.ts`), checks `initSync`'s sources and `SyncEngine`'s
driver (`test/sync.test.ts`), and loads every entry under Node with the
`pg` driver and the SQLite modules refused, then runs the SQLite adapter's
tests through `node:sqlite` (`test/node.mjs`). With the variable set it
also runs every scenario in
`runtime/versiongraph/testdata/scenarios` through the engine and the
adapter (`test/scenarios.test.ts`), replaying each operation through a
`SyncEngine` that must make the recorded storage calls, in the recorded
order, and return the `Engine`'s result or error (`test/replay.ts`);
checks each canonical vector's rendering against Postgres; and runs the
adapter's (`test/adapter.test.ts`), the sweeper's (`test/sweeper.test.ts`)
and the facade's (`test/facade.test.ts`) own tests. Without it they skip.

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).
