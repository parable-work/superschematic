# superschematic

A schema compiler. You write one schema per service in TypeScript, JSON or
YAML; superschematic generates the SQL DDL, Go ORM, Go REST server, OpenAPI
document, TypeScript, Python and Rust types, and TypeScript, Go, Python and
Rust SDKs for it. Scalar types (email, UUID, URL, cron, and about forty more)
come from [superscalar](https://github.com/parable-work/superscalar), which
validates them the same way in every language.

```
schema source (.schema.ts | .schema.json | .schema.yaml)
  -> superschematic build
     -> sql/         Postgres DDL
     -> orm/         Go repositories
     -> api/         Go chi router, middleware, OpenAPI, or a Rust axum crate
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
`docs/DECISIONS.md`.

## Layout

| Path | What it is |
| --- | --- |
| `cmd/superschematic/` | The binary with no extension linked |
| `cli/` | `cli.New(Config, ...Extension)`: build, build-all, format, json-schema |
| `registry/`, `loader/`, `schemadeps/` | Public packages an extension imports |
| `internal/` | Loader, generators, writers, build plan and cache |
| `ir/` | The schema IR (own Go module; the runtimes import it) |
| `runtime/schema/{go,typescript,python}/` | Schema runtime the generated code links |
| `runtime/http/{go,rust}/` | HTTP runtime the generated servers link |
| `packages/` | `@superschematic/{api,db,schema,schema-config}`: the TypeScript authoring packages |
| `extensions/` | Example extensions |
| `superschematic.toml` | The default naming file, written out |

Four Go modules: the root (compiler), `ir`, `runtime/schema/go` and
`runtime/http/go`. Generated code imports the runtimes and the IR, never the
compiler.

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
go build -o bin/superschematic ./cmd/superschematic
bin/superschematic build path/to/schemas/services/my-service
```

`superschematic build <service-dir>` reads `<schemas-root>/superschematic.toml`
for names and writes to `<schemas-root>/dist`. The fixture corpus under
`internal/loader/tsreader/testdata/services` is a usable set of examples until
`examples/acme-schematic` lands.

## Status

Pre-release. The API surface an extension depends on (`registry`, `loader`,
`cli`, `schemadeps`, `generator.Naming`) is not yet frozen. There is no
release pipeline yet; the Go modules are consumed at a commit.

## Contributing

See `CONTRIBUTING.md`. Commits need a DCO sign-off (`git commit -s`).
superschematic is maintained by Parable Work, Inc. and licensed under
Apache-2.0 (`LICENSE`).
