# Version-graph core (TypeScript)

`@superschematic/versiongraph` is the version-graph core
(`runtime/versiongraph/rust`) built for `wasm32-unknown-unknown`, with typed
`compose`, `merge`, `diff`, `contentHash` and `validate` over its JSON
contract (`runtime/versiongraph/README.md`). It runs in the browser, bun and
Node, has no dependencies, and drives the module through its C ABI exports
(`vg_alloc`, `vg_<op>`, `vg_free`, `vg_dealloc`) with no generated glue.

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

The reference page is "Version graphs" in the docs site
(`docs/src/content/docs/reference/version-graphs.md`).

## Development

```
cd runtime/versiongraph/typescript
bun install --frozen-lockfile
bun run test     # cargo build for wasm32, tsc into dist/, the tests, the Node check
```

The build needs cargo with the `wasm32-unknown-unknown` target
(`rustup target add wasm32-unknown-unknown`). `bun run test` builds the
package, type-checks the tests against the built `dist/`, runs every vector
in `runtime/versiongraph/testdata/vectors` through the package API
(`test/vectors.test.ts`), checks each way of loading the module
(`test/load.test.ts`), and loads the package under Node (`test/node.mjs`).
