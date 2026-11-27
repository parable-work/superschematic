# Development mode's handler (TypeScript)

`@superschematic/rules-handler` is development mode's request handler
(D117, #590; D103's step 6, #587) for a server written by hand in
TypeScript. It is `superschematic-rules-handler` built for
`wasm32-unknown-unknown` from `runtime/engine/rust/rules-handler-wasm`,
driven over its JSON C ABI with no generated glue, as `@superschematic/rules`
drives the core's. The guide is
`docs/src/content/docs/guides/development-mode.md`, "From TypeScript".

```ts
import { devCallers } from '@superschematic/rules-handler';
import { openDev } from '@superschematic/rules-handler/sqlite';

const dev = await openDev('dev.db', { authenticate: devCallers({ subject: 'developer' }) });
const me = { subject: 'alice', permissions: [] };
await dev.request(me, { kind: 'define', document: order });
await dev.request(me, { kind: 'publish', type: 'Order' });
const row = await dev.request(me, { kind: 'create', type: 'Order', data: { title: 'Desk' } });

Bun.serve({ fetch: request => dev.http(request) });
setInterval(() => dev.runDue(), 1000);
```

- The entry imports nothing, so it runs in Bun, Node, Workers and a
  browser. `@superschematic/rules-handler/sqlite` opens the store on
  `bun:sqlite`, a file or `:memory:`, and runs on Bun alone.
- `SqliteStore` is the development layout over any synchronous SQLite
  connection whose statements bind positional parameters: `bun:sqlite`'s
  `Database`, or `node:sqlite`'s `DatabaseSync`. Its tables are the ones
  `superschematic-rules-dev`'s `SqliteStore` creates, so a file either host
  wrote, the other reads.
- `serve` and `runDue` are the host loop over a `Store`, as the Rust
  handler's are. `Dev` is the host: a clock, a runner, a policy, the
  routes (`dev.http`), a typed `dev.request`, the event stream, and the
  tools (`dev.tools`, `dev.callTool`, `dev.mcpTools`).
- A store's answer carries a column's JSON text into the module as it is,
  so a number wider than a double reaches the handler unrounded. A unit's
  values come back through `JSON.parse`; pass `codec` for a lossless one.

## Building and testing

```
bun install
bun run build   # tsc, then scripts/build-wasm.mjs
bun run test    # build, typecheck the tests, bun test test, node test/node.mjs
```

`scripts/build-wasm.mjs` builds `superschematic-rules-handler-wasm` with the
engine workspace's `dist` profile, with the toolchain `tools.env` names and
the `wasm32-unknown-unknown` target, and copies the module into `dist/`.

- `test/transcript.test.ts` replays every transcript rules-dev's replay
  names (`rules-dev/tests/transcript.rs`, with its floors) over bun:sqlite,
  in memory and in a file.
- `test/session.test.ts` runs rules-dev's store session and holds each
  answer to `runtime/engine/testdata/rules-dev/session.json`, which
  `rules-handler-wasm/tests/session.rs` records from the Rust host.
- `test/crosshost.test.ts` builds `superschematic-dev` when cargo is on
  `PATH` (or takes `SUPERSCHEMATIC_DEV_BIN`) and reads SQLite files across
  the two hosts, both ways.
- `test/http.test.ts`, `test/store.test.ts`, `test/load.test.ts` and
  `test/example.test.ts` hold the routes, the event stream, the tools, the
  store's fences, the loader and `examples/serve.ts`; `test/node.mjs` runs
  the entry under Node over `node:sqlite`.
