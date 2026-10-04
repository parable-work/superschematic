# Version graph (TypeScript)

`@superschematic/versiongraph` is the version graph for TypeScript: the
version-graph core (`runtime/versiongraph/rust`) built for
`wasm32-unknown-unknown`, with typed `compose`, `merge`, `diff`,
`contentHash` and `validate` over its JSON contract
(`runtime/versiongraph/README.md`); the engine, which runs every graph
operation over a storage adapter; the engine's Postgres adapter; and the
base of the facade tsgen generates per graph (D19 in `docs/DECISIONS.md`).

| Entry | Holds |
| --- | --- |
| `@superschematic/versiongraph` | The core: `init` and `initSync`, the operations, `VersionGraphError` and the contract's types. Runs in the browser, bun and Node, has no dependencies, and drives the module through its C ABI exports (`vg_alloc`, `vg_<op>`, `vg_free`, `vg_dealloc`) with no generated glue. |
| `@superschematic/versiongraph/engine` | `Engine` and `SyncEngine`, the storage interfaces (`Storage` and `Tx`, `SyncStorage` and `SyncTx`), the named errors and `errorCode`, the canonical rules (`canonicalValue`, `canonicalRow`) and the exact JSON codec they read with (`parseJson`, `stringifyJson`, `JsonNumber`). |
| `@superschematic/versiongraph/postgres` | `PostgresAdapter`, its `Client` interface, and `pgPool` and `pgClient`, which bind the npm package `pg`. |
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
(version 2) and reaches Postgres through `Client`, whose `query` returns
every column as the text Postgres writes: no driver's type parsing touches
a date, a time, an interval, a numeric or a bigint, and the canonical rules
turn each row into its canonical form. `pgPool(pool)` runs each
transaction on a connection of a `pg.Pool`; `pgClient(client)` runs them
one at a time on one connection, and with `{ savepoint: true }` as
savepoints of a transaction the caller holds. `pg` is an optional peer
dependency: the bindings call the methods they need on what they are
given, so no entry imports it, and the core loads without it.

## Development

```
cd runtime/versiongraph/typescript
bun install --frozen-lockfile
bun run test     # cargo build for wasm32, tsc into dist/, the tests, the Node check
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL=postgres://... bun test test   # with the Postgres tests
make versiongraph-scenarios-ts   # from the repository root: the Postgres tests, failing without the variable
```

The build needs cargo with the `wasm32-unknown-unknown` target
(`rustup target add wasm32-unknown-unknown`). `bun run test` builds the
package, type-checks the tests against the built `dist/`, runs every vector
in `runtime/versiongraph/testdata/vectors` through the package API
(`test/vectors.test.ts`), checks each way of loading the module
(`test/load.test.ts`), runs every canonical vector
(`test/canonical.test.ts`), checks `initSync`'s sources and `SyncEngine`'s
driver (`test/sync.test.ts`), and loads every entry under Node with the
`pg` driver refused (`test/node.mjs`). With the variable set it also runs
every scenario in `runtime/versiongraph/testdata/scenarios` through the
engine and the adapter (`test/scenarios.test.ts`), replaying each operation
through a `SyncEngine` that must make the recorded storage calls, in the
recorded order, and return the `Engine`'s result or error
(`test/replay.ts`); checks each canonical vector's rendering against
Postgres; and runs the adapter's (`test/adapter.test.ts`), the sweeper's
(`test/sweeper.test.ts`) and the facade's (`test/facade.test.ts`) own
tests. Without it they skip.

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).
