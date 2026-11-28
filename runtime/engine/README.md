# runtime/engine

The engine (D16, D58) ran a schema with no generated code. It kept a
schema, its instances and an event log in tables of its own, and each
language reached it through a binding: a Go module over its C ABI, a
TypeScript package over its Node-API addon, and the crate itself. D103's
step 7 removed it, with its bindings, the work queue's and commerce's
packages, the addon packages and the engine archive a release shipped
(D103, #587). A schema now runs with no generated code in development
mode or runtime mode (D117, #590), on the behaviors' core, and a DB type
is a table whose behaviors the same core runs.

The directory keeps its name for now. It holds:

- `rust/`, the Cargo workspace of the behaviors' core and the hosts that
  run it:
  - `rules` (`superschematic-rules`): the pure core. A command and the
    state it asks for go in, and a refusal or a change set comes out,
    with no I/O. [`rust/rules/README.md`](rust/rules/README.md) is the
    contract.
  - `rules-behaviors`: the behaviors ported onto the core.
  - `rules-testkit`: the core's reference host, an adapter over memory,
    and the replayer that holds every host to the recorded transcripts.
  - `rules-adapter`: the core's host loop in a hand-written Rust server's
    own transaction, on SQLite and Postgres
    ([`rust/rules-adapter/README.md`](rust/rules-adapter/README.md)).
  - `rules-handler`: development mode's request handler, a request and
    its caller in, a response and a change set out.
  - `rules-dev`: development mode's store, the handler over SQLite.
  - `rules-runtime`: runtime mode, the handler over each type's own
    table, and a publish that is a migration.
  - `rules-wasm`: the core behind a JSON C ABI, the WebAssembly module
    `@superschematic/rules` loads and the static archive
    `runtime/rules/go` links (`scripts/rules-archive.sh`).
  - `rules-handler-wasm`: the handler behind the same kind of ABI, the
    module `@superschematic/rules-handler` loads.
  - `pg` (`superschematic-engine-pg`): the one Postgres layer the hosts
    share, its errors' classes and a connection string's TLS.
- `testdata/rules/`: the scenarios and the transcripts every host of the
  core replays, in Rust, Go and TypeScript.
- `testdata/linkcheck/`: the Go program `scripts/link-check.sh` builds
  over superscalar's, the version graph's and the behaviors' core's
  archives, to show they link into one binary.

The core's other hosts live beside this directory: the Go binding and
adapter in [`runtime/rules/go`](../rules/go/README.md), the TypeScript
package in [`runtime/rules/typescript`](../rules/typescript/README.md),
and development mode's TypeScript handler in
[`runtime/rules-handler/typescript`](../rules-handler/typescript/README.md).
[Development mode](../../docs/src/content/docs/guides/development-mode.md)
and [runtime mode](../../docs/src/content/docs/guides/runtime-mode.md)
describe what the handler serves.
