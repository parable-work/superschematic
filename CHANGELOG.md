# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every package and module in this repository shares one version
(`versions.env`). A change to the IR, to the naming file's keys, to a public
Go package (`registry`, `loader`, `cli`, `ir`, `runtime/*`) or to the shape
of a generated artifact is always listed here with the bump it requires.

## [Unreleased]

### Added

- Core import: the schema loaders
  (TypeScript, JSON, YAML), the IR, the registry with its extension surfaces
  (kinds, decorators, documents, generators, auth providers, commands), the
  generators (SQL, Go ORM, Go API with the `session` auth provider,
  TypeScript, Go, Python and Rust types, TypeScript, Go and Rust SDKs, env
  config, JSON Schema), the `superschematic` CLI (`build`, `build-all`,
  `json-schema`, `format`), the authoring packages `@superschematic/{schema,
  db, api, schema-config}`, the schema runtimes for Go, TypeScript and
  Python, the http runtime for Go and Rust, and the `extensions/deploy` and
  `extensions/platform` reference extensions.
- Repository bootstrap: license, contribution guide, code of conduct,
  security policy, CI (`ci.yml` with `ci-pass` as the one required check),
  the extraction scrub and the DCO check.
- `examples/acme-schematic`: a downstream extension that adds a kind, a
  decorator, a document, a generator, an auth provider and a command without
  editing the core, with a smoke script and a check that adding a second
  decorator changes nothing outside the example. Runs as the `acme` CI job.
- Release pipeline: `release.yml` verifies the tag against `versions.env`,
  runs the full CI, builds the CLI for linux and darwin on x64 and arm64,
  creates the GitHub release with provenance attestations and an SBOM, and
  publishes the npm packages, the PyPI distribution and the crate through
  trusted publishing behind `RELEASE_PUBLISH_ENABLED`. `release-pr.yml` and
  `scripts/bump_version.py` set the one shared version; `go-module-tag.yml`
  cuts `ir/vX.Y.Z`, `runtime/schema/go/vX.Y.Z` and `runtime/http/go/vX.Y.Z`
  next to `vX.Y.Z`. `scorecard.yml` runs OpenSSF Scorecard.
- Docs site (Starlight) under `docs/`: quickstarts for Go, TypeScript,
  Python and Rust, the extension guide, the deploy and platform extension
  guides, the `superschematic.toml` reference and the CLI reference.
- Go ORM: every generated `<Type>Update` has `ApplyTo(*types.<Type>)`, which
  writes each set and `SetNull` field onto a stored row (SetNull wins; a nil
  receiver or row is a no-op), so a handler can check the post-update row
  before calling `UpdateOne`. Minor.
- `@denyUnknownFields` (from `@superschematic/schema`) on a type makes the
  generated Rust struct `#[serde(deny_unknown_fields)]`, so a payload key the
  type does not declare fails to decode instead of being dropped. The IR
  `TypeDef` gains `denyUnknownFields`; the schema-file JSON Schema accepts
  it. Opt-in per type. Minor.
- OpenAPI: an array field with `listMin` or `listMax` carries `minItems` or
  `maxItems`. Patch.
- `superschematic build --with-deps <service-dir>` also builds every
  service the target transitively depends on (declared dependencies plus
  `authDb`), dependencies first, with `build-all`'s discovery, ordering and
  schema catalog; siblings outside the closure are not built. Minor.
- `build-all` records on every package of the dependency graph the service
  whose build produced it (`service` in `.deps.json`, from each service's
  output directories), and fails when a package directory under the output
  root belongs to no discovered service, naming the directory. `--deps-copy`
  or `[deps] copy` in `superschematic.toml` also writes the graph, byte for
  byte, to a path outside the output root that a repository can commit.
  `schemadeps.SyncCopy` checks or refreshes that copy from a command that
  runs after the build, such as an extension's pin command. `[deps]` is not
  part of the build cache key. Minor.
- `registry.BuildAllContext.Services`: every discovered service in build
  order as a `registry.BuildAllService` (name, kind, service directory and
  the output directories the build cache stores and restores), so a
  build-all hook can read a service this run restored or found up to date
  and did not load. Minor.
- `loader.NewDeclarationProgram`: a type-checked TypeScript program over
  in-memory files, built with the compiler, bundled lib files and module
  resolution the schema loader uses, for an extension or tool that
  evaluates types the schema frontend does not walk. `DeclarationProgram`
  has `Checker`, `SourceFile`, `Diagnostics` (optionally limited to named
  files, located as `file:line:col` with the caller's file names), `ErrorAt`
  and `Close`. The compiler options are fixed (strict, no `skipLibCheck`),
  no `tsconfig.json` is read, and nothing is read from disk but the
  compiler's lib files. `loader.SchemaError` and `loader.SchemaErrorList`
  alias the located diagnostic types. The checker and AST types are the
  pinned compiler's shim types. Minor.
- `runtime/http/go`: `AppError.WithDetails(details)` returns a copy that
  carries structured, client-safe details, and `apperror.Respond` writes them
  as the problem response's `details` member through the new
  `response.ErrorWithDetails`. An `AppError` without details renders as
  before. Minor.
- TypeScript types: every build writes `<out>/types/typescript/package.json`,
  a private Bun workspace root named `<npm_scope>/types-workspace` whose
  workspaces are the generated types packages next to it. A types package
  that depends on a sibling through `file:../<schema>`, and on the scalar
  library through a `file:` spec outside the tree, then installs from the
  root or from any package. The name comes from `npm_scope`. Minor.

### Changed

- Generated Go API modules require `github.com/go-chi/chi/v5` v5.3.2
  (was v5.3.1), matching `runtime/http/go`. Patch.
- Generated Go ORM modules require `github.com/jackc/pgx/v5` v5.11.0 (was
  v5.10.0). Patch.
- Generated Python types packages declare `*.schema.json` as package data
  next to `py.typed`, so a JSON Schema document a build step writes into the
  package directory ships in the wheel and sdist. Patch.
- Go types: a required array means present, not non-empty, as it already
  did in TypeScript and Rust. `Validate` reports `required` for a nil list
  only; an explicit `[]` is valid unless the field declares `listMin` of 1 or
  more, which reports `listMin`. Decoding (`UnmarshalJSON`, `FromMap`,
  `FromMapStrict`) keeps an absent or null list nil so the required check
  can see it; encoding still writes `[]` for a nil list. A schema that relied
  on required implying non-empty adds `listMin: 1`. Minor.
- `build-all` orders a service after its `authDb`, in sequential and
  `--parallel` builds, even when the config does not also list it under
  `dependencies`. The generated API module imports the authDb's packages,
  so it is a build-order edge. Patch.
- `build-all` runs the registered `BuildAllHook`s on a run where every
  service was up to date or restored from the cache and nothing was built.
  Before, such a run skipped them, so a hook's merged output depended on
  which services happened to rebuild. Minor.
- `schemadeps.CollectFromDist(distRoot, producers)` and
  `schemadeps.EmitFromDist(distRoot, producers, copyPath)` take the map from
  output directory to producing service and, for `EmitFromDist`, the copy
  path. Callers of the old one-argument forms pass `nil` and `""` for the
  old behavior. Minor.
- `build-all` writes `<schemas-root>/dist/.build-stamps/<service>` for every
  service it builds, with or without `--cache`; before, only cached builds
  wrote stamps, so a step that keys on the stamp saw none after a plain
  build. `build --with-deps` still writes none. Patch.

### Fixed

- `session` auth provider: the generated session and principal stores passed
  a `*string` where the ORM's `UUIDFilter` takes the scalar UUID, so any
  upstream DB with a `Session` or `User` table failed to compile the generated
  API. The stores now parse the id with `scalars.ParseUUID`. Found by the acme
  example.
- TypeScript types: a per-type validator checked a field typed with an enum
  from a dependency package for presence only, so a strict parser accepted
  any string there. It now imports that package's enum validators from its
  `validators/enums` subpath and validates the value. Patch.
- Go types: `Parse<Scalar>` for a custom-parse scalar with a JSON-shaped Go
  type (a map) called a superscalar function by its leaf name and converted
  the returned string to the map type, which does not compile. It now calls
  `Parse<Symbol>` and decodes the canonical JSON into the alias. No scalar
  in the pinned superscalar catalog takes this path yet; `Generic.StringMap`
  does once superscalar marks it custom-parse. Patch.
- Go types: `Parse<Scalar>` for `Finance.Money`, `Generic.Int64`,
  `Identity.UserID` and the `Temporal` integer durations (`Milliseconds`,
  `Seconds`, `Minutes`, `Hours`, `Days`) called a superscalar function named
  after the scalar's last identity segment (`ParseSeconds`, `ParseUserID`).
  The superscalar Go binding exports no such function, so a types module
  using one of these scalars did not compile. An integer scalar now calls
  `Parse<Symbol>` and parses the canonical string; `Identity.UserID` calls
  `ParseUUID`, as `Identity.UUID` already did. A test builds a module that
  uses every custom-parse scalar in the catalog. Patch.
- Go types: the length and pattern checks on a `Temporal.Duration` field
  converted the int64 duration with `string(value)`, which yields one rune,
  so every non-zero duration failed its pattern check and `go vet` rejected
  the module. They now format the value with `String()`. Patch.
- Python types: generated enums accept their serialized value when a model
  is validated with `strict=True`, in direct, list and map fields. Unknown
  values and unrelated coercions still fail. Patch.

[Unreleased]: https://github.com/parable-work/superschematic/commits/main
