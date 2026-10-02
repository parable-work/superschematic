# superschematic

A schema compiler. You write one schema per service in TypeScript, JSON or
YAML; superschematic generates the SQL DDL, Go ORM, a REST server (Go, Rust
or TypeScript), OpenAPI document, TypeScript, Python and Rust types, and TypeScript, Go, Python and
Rust SDKs for it. Scalar types (email, UUID, URL, cron, and about forty more)
come from [superscalar](https://github.com/parable-work/superscalar), which
validates them the same way in every language.

```
schema source (.schema.ts | .schema.json | .schema.yaml)
  -> superschematic build
     -> sql/         Postgres DDL
     -> orm/         Go repositories
     -> api/         Go chi router, middleware, OpenAPI; or a Rust axum crate;
                     or a TypeScript Hono router package
     -> types/       Go, TypeScript, Python, Rust
     -> sdk/         TypeScript, Go, Python, Rust clients
```

## Extension model

The core knows three schema kinds (DB, API, General), one auth
provider (`session`) and the generic scalar set. Everything project-specific
lives in an extension: a Go package that registers kinds, decorators,
generators, auth providers, build hooks and scalars with the registry, and a
naming file (`superschematic.toml`) that gives the generated packages their
coordinates. A binary is `cli.New(cli.Config{Name: ...}, ext...)`; the
`cmd/superschematic` binary links no extension. `extensions/deploy` and
`extensions/platform` are worked examples. The design is written up in
`docs/extension-model.md`; the decisions behind it are in
`docs/DECISIONS.md`. The Starlight site under `docs/` (quickstarts, the
extension guide, the naming-file and CLI references) deploys to GitHub
Pages on a release tag once the repository is public.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/superschematic/` | The binary with no extension linked |
| `cli/` | `cli.New(Config, ...Extension)`: build, build-all, format, json-schema, behaviors |
| `registry/`, `loader/`, `schemadeps/` | Public packages an extension imports |
| `internal/` | Loader, generators, writers, build plan and cache |
| `ir/` | The schema IR (own Go module; the runtimes import it) |
| `ir/typescript/` | `@superschematic/schema-ir`: the runtime document's types, and the schema-file data form's types and JSON Schema |
| `runtime/schema/{go,typescript,python}/` | Schema runtime the generated code links |
| `runtime/http/{go,rust,typescript}/` | HTTP runtime the generated servers link; the engine's HTTP API is built on the TypeScript one |
| `runtime/versiongraph/{rust,go,typescript}/` | Version-graph core: compose, merge, diff, hash and validate trees of versioned rows; a Rust crate with a Go binding (its own Go module) and `@superschematic/versiongraph`, the TypeScript package over its wasm build for the browser, bun and Node. `runtime/versiongraph/README.md` is its JSON contract and `testdata/vectors` its executable form. A generated ORM whose schema declares a graph imports the Go binding |
| `runtime/engine/typescript/` | `@superschematic/engine`: runs a schema with no generated code (D16); its storage, schema registry, instances, event log, access policy, HTTP API with the event stream (`@superschematic/engine/http`), behavior plug-in interface, runner of reactions and schedules, describe and tools documents, MCP endpoint (`@superschematic/engine/mcp`) and the core's behaviors (`Workflow`, `Comments`, `Revisions`, and `Dependencies`, `Links` and `Rollups`, which reach other instances, `Search`, full-text search without vectors, and `Reactions`, which the runner runs) are built. `runtime/engine/testdata/` holds the Go vectors its tool argument schemas are checked against |
| `runtime/engine-workqueue/typescript/` | `@superschematic/engine-workqueue`: claimable work for the engine (D16), behaviors a deployment registers with it. `Lease`, leases with fencing tokens, heartbeats, expiry on the runner and directives, `Assignment`, and `Queue`, the claim and `claimNext`, are built |
| `packages/` | `@superschematic/{api,db,schema,schema-config}`: the TypeScript authoring packages |
| `extensions/` | Example extensions |
| `superschematic.toml` | The default naming file, written out |

Five Go modules: the root (compiler), `ir`, `runtime/schema/go`,
`runtime/http/go` and `runtime/versiongraph/go`. Generated code imports the
runtimes and the IR, never the compiler.

## Build

Pins: `tools.env` (Go 1.26.4, Node, Bun, Python, uv, Rust) and
`superscalar.pin` (the superscalar commit). superscalar has no release yet, so
its Go binding is built from source: `scripts/superscalar-dep.sh` checks the
pinned commit out under `third_party/superscalar`, builds the static archive
and the TypeScript binding, and prints the `CGO_LDFLAGS` value Go needs.

```
make setup          # superscalar checkout + build, bun install, uv sync
make build          # go build every module; bin/superschematic
make test           # go test, catalog drift, TypeScript, Python, Rust, CLI smoke
make lint           # go vet, gofmt, golangci-lint, scrub
```

Or by hand:

```
export GOTOOLCHAIN=go1.26.4
eval "$(scripts/superscalar-dep.sh --export)"
go build -trimpath -buildvcs=false -o bin/superschematic ./cmd/superschematic
bin/superschematic build path/to/schemas/services/my-service
```

`superschematic build <service-dir>` reads `<schemas-root>/superschematic.toml`
for names and writes to `<schemas-root>/dist`. `examples/acme-shop` is the
docs site's tutorial project: four services built with the core binary,
and a Go and a TypeScript app that serve and call what they generate;
`examples/acme-shop/scripts/check.sh` builds and tests it. `examples/acme-schematic` is a
complete downstream example: a schemas root with one service per kind and an
extension that adds a kind, a decorator, a document, a generator, an auth
provider and a command without editing the core. Its README walks through
each surface; `examples/acme-schematic/scripts/smoke.sh` runs it.
`examples/engine-notes` is the engine guide's project: a notes server on
`@superschematic/engine` whose schema composes `Workflow`, `Comments` and
`Revisions`, served over HTTP, the event stream and MCP;
`examples/engine-notes/scripts/check.sh` runs its end-to-end test on
Node.js and Bun.

## Status

Pre-release. The API surface an extension depends on (`registry`, `loader`,
`cli`, `schemadeps`, `generator.Naming`) is not yet frozen. The first tag is
`v0.1.0-alpha.1`; until it is cut the Go modules are consumed at a commit and
nothing is published to npm, PyPI or crates.io. `CONTRIBUTING.md`, "Releases",
has the procedure.

## Contributing

See `CONTRIBUTING.md`. Commits need a DCO sign-off (`git commit -s`).
superschematic is maintained by Parable Work, Inc. and licensed under
Apache-2.0 (`LICENSE`).
