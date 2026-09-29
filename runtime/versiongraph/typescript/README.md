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
| `@superschematic/versiongraph` | The core: `init`, the operations, `VersionGraphError` and the contract's types. Runs in the browser, bun and Node, has no dependencies, and drives the module through its C ABI exports (`vg_alloc`, `vg_<op>`, `vg_free`, `vg_dealloc`) with no generated glue. |
| `@superschematic/versiongraph/engine` | `Engine`, the storage interface (`Storage`, `Tx`), the named errors and `errorCode`, the canonical rules (`canonicalValue`, `canonicalRow`) and the exact JSON codec they read with (`parseJson`, `stringifyJson`, `JsonNumber`). |
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
`AbortSignal` aborts.

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
(`test/canonical.test.ts`), and loads every entry under Node with the `pg`
driver refused (`test/node.mjs`). With the variable set it also runs every
scenario in `runtime/versiongraph/testdata/scenarios` through the engine
and the adapter (`test/scenarios.test.ts`), checks each canonical vector's
rendering against Postgres, and runs the adapter's
(`test/adapter.test.ts`) and the sweeper's (`test/sweeper.test.ts`) own
tests; without it they skip.

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).
