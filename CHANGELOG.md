# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Every package and module in this repository shares one version
(`versions.env`). A change to the IR, to the naming file's keys, to a public
Go package (`registry`, `loader`, `cli`, `ir`, `runtime/*`) or to the shape
of a generated artifact is always listed here with the bump it requires.

## [Unreleased]

### Fixed

- Verification refuses an `@index` of a DB table that the SQL generator
  cannot build: one with a key that resolves to no column of the table, or
  one with no keys. The SQL generator left such an index out of the DDL
  without an error. A key resolves as the generator always resolved it: to
  the column named by the key in snake_case, or by that name plus `_id`,
  so a to-one relation `author` is indexed as `author` or `authorId`. A
  list relation, the generated `id` of a table without a `@key`, `_version`
  and a `@hasMany` back reference's column are not keys: the generator adds
  none of them before it resolves the keys. The TypeScript compiler did
  not catch these, since `@index<T>` checks the keys against `T` rather
  than the decorated class and `keyof T` includes list relations; the JSON
  and YAML forms accept any string. The error names the type, the index
  and the key, such as
  `Post: @index(["author", "titel"]) key "titel" names no field of Post`.
  The SQL generator fails on the same indexes, as a backstop. A schema
  with such an index now fails to load; output for every other schema is
  unchanged. Patch.
- Verification refuses version graph schemas the generators could not run
  correctly. A `@graphMember` may exclude from history only nullable
  fields (besides its audit fields), since Revert and Merge rebuild rows
  from history images and a required column missing from the image would
  be written as NULL. A member field whose column is `entity_key`,
  `ref_id` or `deleted_on_ref` (such as `refId`, or a `@hasMany` back
  reference from a type named `Ref`) fails, as does an authored index
  whose name equals one the graph generates (`uq_<member>_entity_ref`,
  `uq_<graph>_ref_root_name`, `uq_<graph>_commit_root_sequence`,
  `uq_<graph>_patch_entity`, `idx_<graph>_patch_entity_version`). Output
  for schemas that verified before is unchanged. Patch.
- The Python scalars module (`scalars.py`) no longer carries the source
  tree's name for the scalar library: the fallback comment now names the
  configured scalar Python module as not installed, and the availability
  flag is `_SCALAR_MODULE_AVAILABLE`. The scrub gate fails on that name.
  Patch (generated comment and private identifier change).
- The generated version-graph shell's `Save` leaves the row id of a member
  whose `@key` is a plain `Identity.UUID` to the table's default, as it
  does for an `AutoGenerate<Identity.UUID>` key, instead of minting a
  client-side UUID. Behavior is unchanged: each ref holds its own row of an
  entity, so the caller's id was already never the row's. Such members are
  now exercised end to end. Output for a graph whose members all have
  `AutoGenerate` keys is unchanged. Patch.
- Writing a Go types module no longer removes the whole `versiongraph/`
  directory when the schema declares no graph. It removes each
  `versiongraph/*.json` no graph of the schema writes, keeps every other
  file, and removes the directory only when that leaves it empty. Patch.

### Added

- `@superschematic/versiongraph`, a new npm package in
  `runtime/versiongraph/typescript`: the version-graph core built for
  `wasm32-unknown-unknown`, with an async `init` and typed `compose`,
  `merge`, `diff`, `contentHash` and `validate` over the core's JSON
  contract, in the browser, bun and Node. `init` takes the module as bytes,
  a URL, a `Response` (or a promise of one) or a compiled module, and by
  default loads the `superschematic_versiongraph.wasm` the package ships.
  A refused input throws `VersionGraphError` with the contract's error
  code. The contract's types live in one module and the package's tests
  run every vector through them. The package ships compiled ES modules and
  the wasm file, carries the repository version (`bump_version.py` writes
  it), and is packed and published with the other npm packages. The bun
  test of the raw wasm build (`runtime/versiongraph/wasm`) and the
  `versiongraph-wasm` make target are gone: `make ts` and CI's
  versiongraph job run the vectors through the package instead, once each,
  and `make rust` no longer builds the wasm module. A new vector,
  `merge_atomic_unit_is_one_unit`, covers a unit named `atomic`
  explicitly. Minor (new package).
- The rest of D17's `@versioned` changes. `@versioned({ exclude: [...] })`
  names fields left out of every history image: the capture function
  subtracts their columns from the `INSERT` and `UPDATE` image and from a
  delete's tombstone, and the history readers return their zero value. An
  excluded actor column is left out of the tombstone too, so it records no
  actor and generated hard deletes do not set the history actor setting.
  Verification requires each name to be a field of the type and refuses
  the key, `deletedAt` and relations (the history readers find and filter
  rows by them); a `@graphMember` may exclude only fields with
  `@conflictUnit("excluded")` and the audit fields. `@superschematic/db`
  exports `@optimistic`, a core decorator the registry declares: the
  table gets `_version`, a `BEFORE UPDATE` trigger that bumps it
  (`<table>_bump_version`, `trg_<table>_bump_version`),
  `UpdateOneIfVersion`, `DeleteOneIfVersion` and `ErrVersionConflict`, and
  no history table, capture function, prune function or history readers.
  `@versioned` implies it, so a type carrying both fails verification. The
  Go, TypeScript, Rust and Python types give an `@optimistic` table
  `_version`; `HistoryRecord` is declared only when a table is
  `@versioned`. When a `pruneKeepReferencedBy` table is a DB type
  of the same schema, verification checks that its key column holds the
  versioned key's type and that its version column is a `Generic.Int64`;
  a table outside the schema is still checked syntactically only. A
  `@versioned` or `@optimistic` type that declares a field named `version`
  or `_version` fails verification, since the generated `_version` field
  collides with it in Go and Rust. The IR gains `TypeDef.optimistic` and
  `VersionedConfig.exclude`; the JSON and YAML forms, the schema-file JSON
  Schema and TypeScript types, `format` and `@superschematic/db`'s
  `VersionedOptions` carry them. Output for a schema that uses neither is
  unchanged. Minor.
- The generated version-graph shell (D17). When a schema declares a
  version graph, the Go ORM generator writes `versiongraph_<name>.go` with
  `db.<Name>Graph()`, a typed `<Name>Graph` whose methods each run in one
  transaction and need a user in the context: `CreatePrimary`, `Branch`,
  `Save` (per kind `GraphEdits[T]`: upsert a row by entity key, generating
  the key of a new entity; delete, which writes a tombstone copying the
  entity's effective row; unset, which removes the ref's own row through
  the actor-recording hard delete), `Commit` (`Message`, `Tag`, which takes
  the root's next `sequence` under a root lock), `Seal`, `Merge` (source,
  target, target version, resolutions; conflicts write nothing), `Revert`,
  `Materialize` (walks parent commits up to `DefaultWalkCeiling`, 4096, or
  `WithWalkCeiling(n)`, and refuses a commit from a newer schema epoch),
  `Compose`, `Diff`, `History` and `Discard`. Every write through a ref
  takes its expected `_version` and fails with `ErrVersionConflict` when
  it moved. Rows reach the core as `to_jsonb` of live rows and history
  images of committed ones; a commit's patches pin the winning rows'
  `(id, _version)`. It returns `<Name>Tree` (a slice per kind, the content
  hash, compose findings), `<Name>Conflict`, `<Name>Resolution` and
  `<Name>Change` over `json.RawMessage` values, and the named errors
  `ErrRefSealed`, `ErrNothingToCommit`, `ErrWalkCeiling`, `ErrSchemaEpoch`,
  `ErrEntityNotFound`, `ErrHistoryMissing`, `ErrInvalidTree`
  (`*InvalidTreeError`) and `ErrRootMismatch`. The shared machinery is in
  a generated `versiongraph.go`. The graph descriptor is built from the IR
  (`internal/generator/graphdesc`) and written both as the constant
  `<Name>GraphDescriptor` in the ORM and as `versiongraph/<name>.json` in
  the Go types module. Such an ORM requires the version-graph core's Go
  binding: the naming file gains `versiongraph_go_module` (default
  `github.com/parable-work/superschematic/runtime/versiongraph/go`) and
  `[paths] versiongraph_go`, which writes a `replace` directive, and a
  consumer builds the core's archive and adds it to `CGO_LDFLAGS`. The IR
  gains `FieldDef.distinctNull`, which the expansion sets on
  `<Name>Commit.sequence`, so the Go types give it a pointer and store
  `nil` as `NULL` rather than reading `NULL` back as sequence 0. CI's go
  job builds the core's archive before the Go tests. Schemas without a
  graph generate byte-identical output. Minor.
- The version-graph core (D17), `runtime/versiongraph`: a Rust crate,
  `superschematic-versiongraph` (rlib, staticlib, cdylib; depends on serde,
  serde_json and sha2 only), with no IO, clock or randomness. Its five
  operations take and return JSON: `compose` lays one ref's rows over a
  base tree by entity key (a tombstone removes the entity and its
  descendants; a row whose parent is absent is kept and reported), `merge`
  is a three-way merge per entity and then per conflict unit (`atomic`,
  `keyed`, `jsonSchema`), reporting conflicts by kind, entity key and JSON
  Pointer unit path with the base, ours and theirs values, settling them
  from `resolutions`, and saying for each entity which side won, `diff`
  lists `ADD`, `UPDATE` and `DELETE` changes, `content_hash` is SHA-256
  over canonical JSON of the content columns, and `validate` reports
  duplicate entity keys, singleton, absent parent, parent cycle and
  out-of-range order findings. A graph descriptor names each kind's key,
  id, ref, tombstone, version and author columns, its parent edge, order
  column, singleton rule, conflict units and excluded columns; rows are
  the JSON Postgres `to_jsonb` gives them. `runtime/versiongraph/README.md`
  is the contract. A C ABI (`vg_compose`, `vg_merge`, `vg_diff`,
  `vg_content_hash`, `vg_validate`, `vg_free`, and `vg_alloc`/`vg_dealloc`
  on wasm32) serves a new fifth Go module, `runtime/versiongraph/go`
  (`Compose`, `Merge`, `Diff`, `ContentHash`, `Validate` over
  `json.RawMessage`, cgo over the static archive), and the same exports
  build for `wasm32-unknown-unknown`. `runtime/versiongraph/testdata/vectors`
  holds the vectors the Rust tests, the Go binding and the
  `@superschematic/versiongraph` package's tests all run
  (`UPDATE_VECTORS=1 cargo test` rewrites them).
  `make versiongraph` (`scripts/versiongraph-archive.sh`) builds the archive,
  the Makefile adds its directory to `CGO_LDFLAGS` and the module to its Go
  module list, `make rust` runs the crate's gates, including clippy for
  wasm32, and CI runs them in a new `versiongraph` job. The crate and the
  module are version sites of `scripts/bump_version.py`, and
  `go-module-tag.yml` cuts `runtime/versiongraph/go/vX.Y.Z`. The crate is
  not published to crates.io. Minor.
- Version graph declarations (D17). `@superschematic/db` exports
  `@versionGraph({ name?, schemaEpoch? })` for a graph root,
  `@graphMember({ graph, parent?: { key, of }, order?, singleton? })` for
  an entity kind of the graph and `@conflictUnit('atomic' | 'keyed' |
  'jsonSchema' | 'excluded')` for a member field's merge unit, with the
  `VersionGraphOptions`, `GraphMemberOptions`, `GraphParent`,
  `ConflictUnitStrategy` and `SchemaClass` types; `graph` and `parent.of`
  name classes as values. The core registry declares the three
  decorators. The IR gains `TypeDef.versionGraph`
  (`ir.VersionGraphConfig`), `TypeDef.graphMember`
  (`ir.GraphMemberConfig`, `ir.GraphParent`), `FieldDef.conflictUnit`, the
  `ir.ConflictUnit*` constants and `TypeDef.VersionGraphName`; the JSON and
  YAML forms, the schema-file JSON Schema and TypeScript types, and
  `format` carry them. Verification checks the rules D17 states: a root
  and each member have one UUID `@key`; a member is `@versioned`, has
  exactly one relation to its root and no `deletedAt`, and belongs to one
  graph; `parent.of` is a member of the same graph and `parent.key` a UUID
  field; `order` names a `Generic.Int64` field; `keyed` and `jsonSchema`
  sit only on a member's JSON object field; a graph has a member, a
  PascalCase name and a non-negative epoch, and generates no name the
  schema already defines. The loader then expands each graph into ordinary
  types: `<Name>Ref` (`@versioned`, soft-deletable, name unique per root
  among live refs), `<Name>Commit` (`sequence` unique per root),
  `<Name>Patch` (unique on `(commit, entityKind, entityKey)`, indexed on
  `(entityId, entityVersion)`), the enums `<Name>EntityKind` and
  `<Name>PatchOperation`, and on each member `entityKey` (generated on
  insert), `ref`, `deletedOnRef`, a unique `(entityKey, ref)` index and,
  with `retentionDays`, a prune pin on `<name>_patch(entity_id,
  entity_version)`. Every generated relation is `RESTRICT`, and the actor
  fields take the type the ORM resolves its user id to. The `sql`, `orm`
  and `types` generators emit them as any other tables. What the expansion
  adds carries `origin: "versionGraph"` (`ir.OriginVersionGraph`) on
  `TypeDef`, `FieldDef`, `EnumDef`, `IndexDef` and `PruneReference`; the
  data forms have no `origin` key and refuse one, and `format` leaves the
  expanded definitions out and writes the declarations. Every new key is
  omitted when unset, so the IR and every generated artifact of a schema
  without a graph are unchanged. Minor.
- Generated Rust and Python types give a `@versioned` table the `_version`
  field and declare a generic `HistoryRecord`, as the Go and TypeScript
  types do (D17). In Rust the field is `version: i64`, renamed to
  `_version` and defaulted to 0 when absent, and
  `HistoryRecord<T> { version, operation, recorded_at, value }` takes
  `recordedAt` as the scalar crate's `DateTime`, so the crate of a
  versioned schema always depends on the scalar crate. In Python the field
  is `version_: int = 0` with alias `_version`, and the pydantic model
  `HistoryRecord[T]` (exported from the package) reads `recordedAt` as a
  `datetime` and writes wire names from `to_json` and `to_json_dict`. Both
  read and write the JSON Go's `HistoryRecord[T]` writes. Schemas without
  `@versioned` generate the same output as before. Minor.
- Naming file: `history_actor_setting` (default
  `superschematic.history_actor_id`) names the transaction-local Postgres
  setting a `@versioned` table's history trigger reads a delete's actor
  from (D17). The value must be dotted identifiers, the form Postgres takes
  for a custom setting; any other value fails the load. `sqlgen.Options`
  gains `HistoryActorSetting`. Minor.
- Go ORM: every `@versioned` repository has `DeleteOneIfVersion(ctx, id,
  expectedVersion)`, on the repository interface and the no-op repository
  too (D17). It deletes the row only when its stored `_version` equals
  `expectedVersion`: a soft delete when the table has `deletedAt`, a hard
  delete otherwise. It and `UpdateOneIfVersion` return the new
  `ErrVersionConflict` when the row exists at another version and
  `ErrNotFound` when it does not; `ErrVersionConflict` wraps `ErrNotFound`,
  so `errors.Is(err, ErrNotFound)` holds as before. A hand-written
  implementation of a versioned repository interface must add the method.
  Minor.

- `@behavior(name, config?)` from `@superschematic/schema`, the TypeScript
  authoring form of a type's behaviors (D16): a core decorator on a class
  of any kind, appending one `behaviors` entry per use in source order,
  with the config stored canonically and checked against the behavior's
  declaration at the argument. The config is typed per name through the
  new `BehaviorConfigs` interface (with `BehaviorName` and
  `BehaviorConfigArg`), which an extension's authoring package augments as
  it augments `MCPToolOptions`; a name no augmentation declares does not
  type-check. `format --to=ts` writes the decorators. The acme example
  types `acme.Rating`'s config and has a TypeScript twin of its ratings
  service. Minor.
- `@superschematic/schema-ir` ships the schema-file data form: the
  `./schema-file` subpath has TypeScript types for the `Document`, the
  single-definition file forms (`SchemaFile` is their union) and every IR
  node type the JSON Schema defines, and `./schema-file.json` is the JSON
  Schema `superschematic json-schema` prints with no extension linked and
  the built-in names. The types leave open what a registry closes: the
  extension slots, the documents, the schema kinds, the MCP invocation
  policy key and the behavior names and configs.
  `internal/tools/schemafiletypes` writes both from the IR structs;
  `make schema-file-types` regenerates them, and
  `make schema-file-types-check` and CI fail when a committed copy
  differs. The package root (`index.d.ts`) is unchanged. Minor.
- The schema-file JSON Schema gives the MCP invocation policy key its
  registered default (`"default": "auto"` for the core's
  `invocationPolicy`), the value the readers fill in for a visible tool
  that omits it. Minor.
- `@superschematic/schema-runtime` has a strict schema-file loader:
  `SchemaFileLoader` (and `loadSchemaFile`) reads a JSON schema file as
  the Go data-form reader does, dispatching its form, validating it
  against a meta-schema, decoding it as Go decodes it into the IR and
  filling in the invocation policy default, and returns the document with
  its canonical form, byte for byte what `ir.CanonicalJSON` writes for the
  document Go decodes. The meta-schema is an option, the `superschematic
  json-schema` output of a deployment's binary; the default is
  `@superschematic/schema-ir/schema-file.json`, which the package now reads
  at run time. `canonicalJSON` writes any JSON text in that form, and a
  refused payload throws `SchemaFileError`. The package depends on `ajv`
  (8.20.0) and needs `JSON.parse` source text access (Node.js 21 or
  later, or Bun). `runtime/schema/testdata/schema_file_parity.json`, which
  `internal/loader/schemafile` writes from the Go reader, holds the vectors
  the TypeScript suite asserts. Minor.
- The schema-file JSON Schema gives every property the Go encoder omits at
  one value that value as its default: an `omitempty` string, bool or
  number that is not a pointer (`""`, `false`, `0`) and an `omitempty`
  list or map (`[]`, `{}`). The readers accept and decode the same
  payloads as before. Minor.
- Behavior declarations in the compiler (D16). A behavior adds fields,
  operations, checks and storage to a type when an engine runs the schema;
  the compiler declares, carries and checks behaviors and runs none.
  `Registry.RegisterBehavior(BehaviorSpec{Extension, Declaration})` parses
  one JSON declaration (`BehaviorDeclaration`: `name`, `description`,
  `configSchema`, `requires`, `conflicts`, `fields`, `operations` with
  `paramsSchema`, `resultSchema`, `writes` and `invocationPolicy`) and
  fails assembly on a malformed one: a core name is bare, an extension's
  is `<extension>.<Name>`, operations are camelCase and not `create`,
  `get`, `list`, `update` or `delete`, and `Finalize` checks `requires`,
  `conflicts` and the invocation policy values. `Registry.Behavior`,
  `Behaviors` and `BehaviorNames` read them back; the public `registry`
  package aliases the types. The IR `TypeDef` gains `behaviors`
  (`ir.BehaviorRef{Name, Config}`, after `implements`, omitted when empty,
  so existing IR is unchanged), with `ir.CanonicalizeBehaviors` and
  `Schema.FindBehavior`. The JSON and YAML forms carry
  `behaviors: [{name, config}]` with the config stored canonically, and the
  schema-file JSON Schema gains `TypeDef.behaviors` and `$defs/BehaviorRef`,
  closed to the registered behaviors with each config held to its schema
  (no entry is valid when none is registered) and `{}` as the config's
  default. The schema-file TypeScript types carry `behaviors` and
  `BehaviorRef`, and the strict loader in `@superschematic/schema-runtime`
  reads them as the Go reader does, storing a config of `{}` as none; the
  parity vectors cover behaviors. The compiler's loader refuses, naming
  the type and the behavior, an unregistered behavior, one listed twice, a
  config its schema rejects, a missing requirement, a conflict, a field
  that collides with the type's own or another behavior's, and two
  behaviors adding the same operation. `generator.Run` refuses a schema
  whose types compose a behavior when an enabled generator does not set
  the new `GeneratorSpec.RendersBehaviors`, naming the generator; no core
  generator sets it, so `build` fails and `build --emit-ir`, `format` and
  `json-schema` accept the schema. The core declares none; the acme
  example declares `acme.Rating`. Minor,
  except that `Registry.Use` now rejects an extension name that contains
  a dot, which separates an extension from its behaviors' names: an
  extension so named must be renamed. Major for that case only.
- Go types: every generated enum has a `Values()` method that returns its
  members in schema declaration order, as a new slice on each call. It is
  the Go counterpart of Rust's `ALL`, and it works through the alias a
  module that imports the enum declares. Minor.
- `runtime/http/go/bodyargs`: decodes the body arguments of an operation
  without an input type from a JSON object body, each from its own JSON
  value, with the list rules and the value rules, recording every failure
  in a `ValidationErrors` at its path. `ReadObject` reads the body;
  `NewArg` with `Required`, `ListMin`, `ListMax`, `MinLength`,
  `MaxLength`, `Pattern`, `Min` and `Max` describes one argument; the
  generic `Value`, `List`, `ListOfLists`, `Map` and `MapOfLists` decode
  it. Generated Go API routes call it. Minor.
- `runtime/http/go/bodyargs`: `QueryList` decodes a list argument of a
  `GET` operation from the query string with the same `Arg` and rules:
  repeated keys and comma-separated items, each read as its kind's JSON
  value and checked as a list element at `name[i]`. Minor.
- `@superschematic/http-runtime`: `ParamSpec.isMap` and `decodeMap`, which
  `decodeJsonParam` calls for a map body parameter. Minor.
- Python SDK: the generated client retries a `GET`, `HEAD` or `OPTIONS`
  request that fails with a `NetworkError`, up to
  `ClientConfig.max_network_retries` times (default 3), sleeping 1, 2, 4
  seconds between attempts. Other methods are not retried, since a write
  may have reached the server before the connection dropped. Set
  `max_network_retries=0` for the old behaviour. Minor.
- `schema.config.json` and `schema.config.yaml` accept an extension
  generator's output key, as `schema.config.ts` already did. The embedded
  config schema checks the core sections and admits any other key
  (`SchemaOutputsDocument` in `@superschematic/schema-config`);
  `ParseOutputs` rejects a key no registered generator claims. Minor.
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
  that depends on a sibling through `workspace:*`, and on the scalar
  library through a `file:` spec outside the tree, then installs from the
  root or from any package. The name comes from `npm_scope`. Minor.
- `@strictJSON` (from `@superschematic/schema`) on a type makes every
  generated decoder of that type reject a key the type does not declare and
  a required field that is absent or null: Go `UnmarshalJSON`, the
  TypeScript validator and `parse<Type>Json`/`Yaml`/`FromJSON`, the Python
  model (`extra='forbid'`) and the Rust struct (`deny_unknown_fields`). It
  applies to the decorated object only; a nested object type opts in on its
  own. The IR `TypeDef` gains `strictJSON`; the schema-file JSON Schema
  accepts it. The TypeScript writer now emits `@strictJSON` and
  `@denyUnknownFields`, which it dropped before. Minor.
- `Validate<T, { uploadMaxBytes: N }>` on a file-upload scalar field sets
  the largest multipart upload, in bytes, the generated Go API accepts for
  that field, in place of the scalar's own limit; the OpenAPI field
  description states it. The IR `FieldDef` gains `validateUploadMaxBytes`;
  schema validation rejects a bound that is not positive or a field that is
  not a single file-upload scalar (a scalar with `fileUpload` metadata), and
  the TypeScript reader rejects a literal that is not an exact integer and
  the key on an operation argument. Minor.
- Rust types: a crate gets `schemas/<Type>.json` for each `@jsonField`
  type: the schema IR with that type as `rootType` and every type, enum,
  scalar and union it reaches, imported ones included. Generation fails if
  a reached type does not resolve. The crate README lists the files.
  Minor.
- Go types: a `@strictJSON` type in a General schema gets
  `<Type>OpenAPISchema() map[string]any`, a fresh copy of its standalone
  OpenAPI schema (`title`, `additionalProperties: false`, referenced types
  under `definitions`). The schema comes from the new
  `apigen.TypeOpenAPISchema`. In every OpenAPI document a `@strictJSON`
  component sets `additionalProperties: false`, and a string-map scalar's
  values are typed from its type mappings. Minor.
- Registry: two extension surfaces for a rule on top of a core mechanism.
  `RegisterCheck(CheckSpec{Name, Extension, Kinds, Verify})` adds a
  verification rule that runs on every loaded schema of the listed kinds,
  core kinds included (nil means every kind), after the core checks and the
  kind's own `Verify`, in every frontend; before, an extension could only
  verify schemas of a kind it registered. `RegisterOpenAPIHook(OpenAPIHook{
  Name, Extension, Edit})` lets an extension edit the OpenAPI document the
  `api` generator builds, as decoded JSON, before it is written; hooks run
  in registration order and a hook error names the hook. With neither
  registered, output is unchanged. The public `registry` package exports
  `CheckSpec` and `OpenAPIHook`. Minor.
- `@docs` (from `@superschematic/api`) on an operation declares its
  reader-facing documentation: `title`, `description`, `capability` (a
  dotted lowercase identifier), `lifecycle`, `visibility`, and optionally
  `audience`, `mappingStatus` (default `mapped`), `replacement` and
  `sunset`. The IR `FieldDef` gains `docs` (`ir.OperationDocs`,
  `ir.ValidateOperationDocs`); the loader checks it in every authoring form
  and rejects it on a data field, and the schema-file JSON Schema accepts it
  on operations. The audience is an open string: a distribution restricts
  it with a registered check. In the OpenAPI document the operation's
  `summary` becomes the title, its `description` the `@docs` description
  (over the comment), `deprecated` is set for a deprecated or retired
  operation, and the record is written under `x-superschematic-docs`
  (`registry.OpenAPIDocsKey`), which an OpenAPI hook can rename. Operations
  without `@docs` are unchanged. The TypeScript writer emits the decorator.
  The acme example restricts audiences and writes `x-acme-docs`. Minor.
- `@docs` guidance: `useWhen`, `doNotUseWhen` and `success` (non-blank
  texts) and `errors`, a non-empty list of `{ code, description,
  commonCorrection }` with codes unique ignoring case. `ir.OperationDocs`
  gains the four fields and `ir.OperationDocsError`; the OpenAPI
  `x-superschematic-docs` record carries them when set; the TypeScript
  writer emits them. Minor.
- Field presentation decorators from `@superschematic/schema`:
  `@docs({ title })` sets the field's `title` (a new authoring form for the
  existing IR field), `@purpose(markdown)` the new `FieldDef.purpose` and
  `@icon(name)` the new `FieldDef.icon`. Each is non-empty, at most once per
  field, and changes no generated type or wire format; a blank value fails
  the load in every authoring form. The core accepts any icon name; an
  icon set is a registered check (the acme example has one). The
  TypeScript writer emits them (`docs as schemaDocs`, `purpose`,
  `icon as schemaIcon`). The TypeScript schema runtime reads and writes
  `x-purpose` and `x-icon` in the legacy JSON Schema form and reads
  `purpose` and `icon` from the IR; the Python runtime reads `x-purpose`
  and `x-icon` into `FieldDef.purpose` and `icon`; the TypeScript IR
  declarations gain both. Minor.
- `@mcp` (from `@superschematic/api`) on an operation classifies it for
  MCP: `{ handle, _meta? }` publishes a visible tool, `{ hidden: true,
  reason }` records why the operation is not one. A handle is lowercase
  snake_case, at most 48 characters; a visible tool must also declare
  `@docs`. `@icon(name)` from `@superschematic/api` names an operation's
  tool icon (any non-blank name). The IR `FieldDef` gains `mcp`
  (`ir.OperationMCP`, `ir.MCPIcon`, `ir.ValidateOperationMCP`,
  `ir.ValidateOperationIcon`) and the operation's `icon` uses the existing
  field; the loader checks both in every authoring form and rejects `mcp`
  on a data field, and the schema-file JSON Schema accepts them. The `api`
  generator resolves each record (`apigen.EndpointInfo.MCP`): a visible
  tool's name and description come from `@docs`, its icon from `@icon`.
  It fails the build when two visible tools of one API share a handle or
  have display names that differ only by case. The TypeScript writer emits
  `@mcp` and `@icon` (as `apiIcon`). Which APIs must classify their
  operations, and which icon names exist, is a registered check; the acme
  example requires `@mcp` on every `shop-api` operation. Minor.
- `@docs` replay keys: `replayMode` (`read_only`, `idempotent`,
  `compare_and_swap`) and the RFC 6901 pointer lists
  `idempotencyKeyPointers` and `expectedRevisionPointers`, which address the
  operation's generated tool arguments. `idempotent` needs idempotency keys
  and no revision, `compare_and_swap` needs a revision, `read_only` and no
  mode take no pointers, and a list names a pointer once.
  `ir.OperationDocs` gains the three fields (`ir.DocsReplayMode`) between
  `sunset` and `useWhen`; the OpenAPI `x-superschematic-docs` record
  carries `replay: { mode, idempotencyKeyPointers,
  expectedRevisionPointers }` when a mode is set; the TypeScript writer
  emits them. Minor.
- MCP tool documents: the TypeScript, Go and Rust SDKs write
  `tools/mcp-audit.json`, one record per operation (handle, title,
  description, icon, hidden and reason, permissions, `@docs` facts,
  guidance, replay contract, input schema digest, binding status), and
  `tools/schema.json` gains, per operation, `operationId`, `title`, the
  resolved `mcp` record, `capability`, `lifecycle`, `visibility`,
  `audience`, `guidance`, `replay`, `requiredPermissions`,
  `bindingStatus` (`unsupported_multipart` for a file upload) and
  `inputSchemaDigest` (the SHA-256 of the encoded argument schema). The
  TypeScript `tools/index.ts` carries the same values. A visible tool's
  `_meta` also carries its `@docs` guidance. When an operation declares
  replay pointers, tool generation fails unless each resolves to a
  required argument, a string for an idempotency key and a number for a
  revision. Minor.
- Tool argument schemas are complete: query parameters are arguments,
  nested object types expand to closed objects, unions render as `oneOf`
  with the discriminator pinned, a map is an object whose
  `additionalProperties` is the value schema, `Validate<>` bounds carry
  over, a body field that is not required is nullable, the root is
  `additionalProperties: false`, and a scalar property names its scalar
  under `x-superschematic-scalar`. `apigen.APIOutput` gains `TypeFields`,
  `TypeUnions` and `ToolKeys`, `apigen.Param` gains `IsMap`, and
  `apigen.ScalarJSONSchemaInfo` gains `CanonicalName`. In `tools/index.ts`
  a map argument is typed `Record<string, T>`. Minor.
- `ir.ToolManifest` and `ir.ToolBindingManifest` (with
  `ir.ToolManifestTool`, `ir.ToolParameterSchema`, `ir.ToolSchemaProperty`,
  `ir.ToolSchemaType`, `ir.ToolSchemaAdditionalProperties` and the binding
  types): the Go wire types of `tools/schema.json` and
  `tools/mcp-binding.json`. A test decodes the rendered documents with
  unknown fields refused and checks the round trip. The TypeScript and Go
  SDK generators use `ir.ToolBinding` for the binding records. Minor.
- Registry: `RegisterToolHook(ToolHook{Name, Extension, Edit})` lets an
  extension edit what the SDK generators publish about an API's MCP tools:
  the vendor keys of the tool documents (`ToolKeys`: the scalar key, the
  `_meta` guidance key, and extra keys written at the root of every
  argument schema) and each operation's resolved `@mcp` record (an icon's
  family and style, `_meta` entries). Hooks run in registration order in
  the `api` generator, before the collision checks; a hook error names the
  hook. The public `registry` package exports `ToolHook`, `ToolSet`,
  `Tool`, `ToolKeys`, `ToolKeyValue`, `DefaultToolScalarKey` and
  `DefaultToolGuidanceKey`. The acme example writes its own keys and icon
  variant with one. Minor.
- MCP invocation policy: a visible `@mcp` tool says whether a client runs
  it when a model calls it or asks the person first, with
  `invocationPolicy: "auto" | "ask"`; a tool that omits it gets `"auto"`
  when it loads, from any authoring form. The IR carries it as
  `OperationMCP.Invocation` (`ir.MCPInvocation`, the key with the value),
  written right after `hiddenReason` in the `mcp` record; the data forms'
  JSON Schema lists the key with its values. `tools/schema.json` writes it
  after `description` in the `mcp` object, `tools/mcp-audit.json` after
  `hiddenReason` (empty for a hidden or unclassified operation), and
  `tools/index.ts` as a member typed with the values and as a literal,
  both after `description`, in the TypeScript, Go and Rust SDKs.
  `Registry.RegisterToolInvocationPolicy(ToolInvocationPolicy{Extension,
  Key, Values, Default})` replaces the core's key, values and default; a
  registry holds one, and a second registration fails assembly naming both
  extensions. `@superschematic/api` exports `MCPToolOptions` for an
  extension's authoring package to add its key by module augmentation,
  and `MCPInvocationPolicy`. The public `registry` package exports
  `ToolInvocationPolicy`, `DefaultToolInvocationPolicy`,
  `DefaultToolInvocationKey`, `ToolInvocationAuto` and `ToolInvocationAsk`.
  The acme example registers `confirm: "never" | "always"`, `"never"` by
  default. Minor: IR, generated tool documents, public Go API and the
  `@superschematic/api` types all gain the field; no key is removed.
- Projection views: `@projection<Source>({ pool, name, migration, where?,
  collapse? })` and `@join<Table>(alias, on, kind?)` on a class and
  `@column("alias.field" | { function, args })` on its fields, from
  `@superschematic/db`, declare a read-only relation over tables of the same
  DB schema. `where` takes setting bindings (optional ones too), `anyOf`
  alternatives, `isNull`/`notNull`/`equals` literal rules and function
  rules, each with an optional `when` guard; `collapse` keeps one row per
  key. The IR gains the `Projection` role, `TypeDef.projection`,
  `FieldDef.projectedFrom` and `FieldDef.projectedFunction`
  (`ir/projection.go`) and `Schema.Projections()`; the schema-file JSON
  Schema accepts them and the TypeScript writer emits them. Verification
  checks every reference, column type, rule shape and name for every
  frontend; it requires no particular rule, which a deployment adds with
  `RegisterCheck`. The type, ORM and table DDL generators skip projections. A
  core `DecoratorSpec` can now take class type arguments, which the
  TypeScript frontend resolves to class names and passes to `Apply` ahead of
  the value arguments (`DecoratorSpec.TypeArgs`). Minor.
- `examples/acme-schematic` declares a projection view in `shop-db` and
  registers `acmeProjectionScope`, a check on the DB kind that requires
  every view's first `where` rule to bind the setting
  `[extension.acme] projection_scope_setting` names. `describe` lists the
  registered checks. Patch.
- The sql generator writes projection views: each view in `create.sql`
  (after the tables, in `CREATE SCHEMA IF NOT EXISTS "pool"`) and
  `drop.sql` (dropped first), a re-runnable migration pair
  `<stamp>_<pool>_<name>_projection.{up,down}.sql`, and
  `projections/<pool>.<name>.arrow.json` (the view's Arrow schema in arrow-rs
  serde form) and `.docs.json`. Views are `security_barrier`; a required
  setting binding makes an unset setting raise, an optional one matches no
  row. A new `outputs.sql` block (`@superschematic/schema-config`
  `SqlOutputConfig`, and the data-form config schema) takes
  `migrationsDir`, relative to the service directory, and `viewOwner`, the
  role the up migration creates the view as with `SET ROLE`; the `sql`
  generator now claims the `sql` output key, so the unknown-key error lists
  `types, sql, api, sdk`, and an unknown key in `outputs.sql` fails the
  build. The generator refuses unsafe names and unmappable column types
  even when the IR skipped verification. Minor.
- Naming file: `metadata_key_prefix` (default `superschematic.`) prefixes
  every key of the projection Arrow schemas' metadata
  (`<prefix>scalar.canonical_name`, `<prefix>projection.settings`, ...).
  Minor.
- TypeScript API server: `outputs.api.language: "TYPESCRIPT"` makes the
  `api` generator write `<out>/api/<service>` as the npm package
  `<npm_scope>/<service>-api` instead of the Go module. It holds
  `interfaces.ts` (one implementation interface per operation namespace),
  `router.ts` (`buildRouter` returns a Hono app; `operationSpecs` is the
  operation table), `openapi.json`, a README, and `values-schema.json` for
  an `@envVars` class. The router decodes path and query parameters through
  the scalar library, parses bodies with the generated `parse<Input>Json`
  decoders, and applies `@publicRoute`, `@auth`, `@requirePermission`,
  `@bodyLimit`, `@rateLimit`, `@timeout` and `@manualRouteRegistration`. An
  operation that uploads files without `@manualRouteRegistration` fails the
  build. `schema-config` gains `ApiLanguage`, and an unknown API language is
  now an error instead of the Go server. `apigen.EndpointInfo` gains
  `PublicRoute`. The dependency graph collects the package as
  `<service>-api`. Minor.
- `runtime/http/typescript` (`@superschematic/http-runtime`): the runtime
  the TypeScript router is built on. It holds the request context, the
  success and RFC 9457 problem envelopes, parameter decoding, the 401/403
  permission gate with a pluggable `PermissionMatcher` (default:
  dotted-path coverage, no root permission, as in `runtime/http/go/session`),
  a token-bucket rate limiter with a pluggable store, and the Hono adapter.
  The service supplies an `Authenticator`; identity and token verification
  stay out of the runtime (D15). It ships TypeScript sources and is packed
  with the other npm packages on release. The naming key
  `http_runtime_npm_package` (default `@superschematic/http-runtime`) names
  it in generated code, and `Naming.NpmAPIPackage` names the generated
  package. Minor.
- `examples/acme-schematic`: `shop-storefront`, an API service on the
  TypeScript server, and `storefront/`, the app that implements it with an
  `X-API-Key` authenticator. The smoke type-checks both and drives the
  generated router over HTTP.
- Arrays of arrays, the IR and loader half (D12): a field, a request
  input field, a body argument or a response can be a list of lists of a
  scalar, enum, object type or union, one level of nesting only. The IR
  `TypeRef` gains `isArrayOfArrays` (after `isArray`, omitted when false,
  so existing IR is unchanged), `TypeRef.ArrayDepth()` and
  `Schema.FindArrayOfArrays()`; `Schema.Validate` requires `isArray` with
  it and refuses it with `isMap`. The TypeScript reader accepts `T[][]`,
  `Array<Array<T>>`, `Array<T[]>`, `Array<T>[]` and their `readonly` forms
  (and `Array<T>` / `ReadonlyArray<T>` for a single list); it refuses a
  third level, a map value that is a list of lists, a nullable inner list
  and list bounds on the inner lists. The data forms write
  `typeRef: { name, isArray: true, isArrayOfArrays: true }` and the
  schema-file JSON Schema accepts it; the TypeScript writer emits `T[][]`;
  a platform default takes a list of lists. Verification refuses it in env
  config fields, relations, indexed fields and `@index` keys, query and
  path parameters, arguments of GET operations and operations without a
  method, and every projection column, join and row rule.
  `apigen.Param` and `apigen.EndpointInfo` carry `IsArrayOfArrays` /
  `OutputIsArrayOfArrays` for the SDK generators. Minor.
- Naming file: `scalar_jsdoc_tag` names a JSDoc tag that the TypeScript
  types write above every scalar-typed field in `types/types.ts`, followed
  by the scalar's canonical name (`/** @scalar Contact.Email */`), after
  the field's doc line. `tsc` keeps it in the declaration files, where a
  tool can read each field's scalar. The key is unset by default, and then
  no tag line is written, so existing output does not change. A value that
  is not an identifier fails the load (D13). `examples/acme-schematic`
  sets `acmeScalar`. Minor.
- Arrays of arrays in Python (D12): pygen renders `T[][]` as
  `List[List[T]]` with element errors at `field[i][j]`, and the Python
  schema runtime reads, parses, validates, serializes, masks and merges
  lists of lists (`TypeRef.is_array_of_arrays`). Minor.
- Arrays of arrays in the Go API and the tool schemas: the api generator
  and `apigen.TypeOpenAPISchema` render `T[][]` as items of items, with
  element constraints on the inner items and `minItems`/`maxItems` on the
  outer array; the Go routes decode a `T[][]` body argument as `[][]T`,
  refuse a null inner list at `name[i]`, validate each element at
  `name[i][j]` and send a nil inner list of a `T[][]` response as `[]`.
  `toolsutil` renders `T[][]` input fields, body arguments
  (`ToolScalarArg.IsArray`, `IsArrayOfArrays`) and responses
  (`BuildReturnSchemaAtDepth`). A list argument is no longer taken as a
  path parameter or as the operation's input type, and a `T[]` body
  argument is an array in the OpenAPI request body. Minor.
- Arrays of arrays in SQL and the Go ORM: a `T[][]` column is `JSONB`,
  never a native array, and the ORM writes it through its JSON codec with
  nil inner lists stored as `[]`. Minor.
- Arrays of arrays in Go types and the Go schema runtime: typegen renders
  `T[][]` as `[][]T` (`InputField[[][]T]` for an optional input field) and
  no longer refuses it; `Validate` reports a null inner list as required at
  `field[i]`, checks each inner element at `field[i][j]` and applies list
  bounds to the outer list; union elements decode through the union wrapper
  at `[i][j]`, and `MaskSecrets` and `To<Type>` copy each inner list. The
  runtime's parse, validate, mask, merge and serialize walk inner lists with
  the same paths. A types module with enums and types but no scalars no
  longer declares `ValidationError` twice. Minor.
- Arrays of arrays in TypeScript: tsgen emits `T[][]`, and its validators
  and the TypeScript schema runtime check every innermost element at
  `field[i][j]`, apply list bounds to the outer list and reject a null inner
  list at `field[i]` (`required`) and any other non-list one (`type`).
  Minor.
- Rust types render a list of lists as `Vec<Vec<T>>` (`Option<Vec<Vec<T>>>`
  when optional) with the serde attributes of `Vec<T>`; a null inner list
  fails to decode. Minor.
- Arrays of arrays in the TypeScript API server. Before, the generator
  rendered a `T[][]` body argument or response as `T[]`; it now renders
  `T[][]`. The runtime decodes a list-of-lists body argument from its JSON
  value. List bounds apply to the outer list. A null inner list is refused
  at `name[i]` (`required`, "required field"), and so is a non-list one
  (`type`, "expected an array"). Each element is checked at `name[i][j]`,
  an object element through the generated `parse<T>Json`, and the 400
  `details` carry the `path`. A `T[][]` response sends a nullish inner list
  as `[]`. `ParamSpec` gains `isArrayOfArrays` and `parse`, `ParamKind`
  gains `object`, `OperationSpec` gains `outputIsArrayOfArrays`, and the
  runtime exports `decodeListOfLists`. A list of lists of a union fails
  the build with "tsrestgen does not support arrays of arrays yet". Minor.
- Arrays of arrays in the SDKs and the Rust API: a `T[][]` body argument
  or response is `[][]T` in the Go SDK, `T[][]` in the TypeScript SDK,
  `list[list[T]]` in the Python SDK and `Vec<Vec<T>>` in the Rust SDK. The
  Go, TypeScript and Python SDKs refuse a null inner list at `name[i]` and
  an element that fails its type's validation at `name[i][j]` before
  sending; the TypeScript and Python SDKs parse a list-of-lists response of
  an object type row by row. The SDK tool documents carry the list shape of
  body arguments (`T[]` and `T[][]` arguments were rendered as `T`) and
  nest the return schema's items. The Rust API crate passes lists of lists
  through its `serde_json::Value` handlers. Minor.
- Arrays of arrays on the docs site: a reference page for `T[][]` (the
  TypeScript and data forms, what each generator writes, where a list of
  lists is accepted and refused with each error, the list rules and the
  error paths), and a note in the extension guide that a generator reads
  `TypeRef.ArrayDepth()`. `examples/acme-schematic` declares a list of
  lists as a DB column, in API types and as a tool argument, and its smoke
  follows it into every output. No generated output changes.
- `registry.ScalarCatalogWithUploads`, `registry.UploadCatalog` and
  `registry.ScalarUpload`: a scalar catalog declares which of its scalars
  are file uploads, with each one's `ir.FileUploadConfig` and optional
  `ir.ImageConstraints`, and the loader hydrates them onto the scalar.
  superscalar's `ScalarMetadata` row has no upload fields, so no catalog
  could declare an upload scalar before. `ir.Schema.ValidateHydrated` runs
  the IR checks that read hydrated scalar metadata. `examples/acme-schematic`
  registers `Acme.Photo` and bounds it with `uploadMaxBytes` in its Catalog
  service. Minor.
- `cli.Config.ToolDigest`: a distribution that links extensions sets the
  tool component of every `build-all` cache key and stamp, in place of a
  hash of the running executable. Two builds with the same digest share
  cache entries even when their executables differ, as builds from two
  checkouts do. The digest must change whenever the distribution's sources
  or the superschematic version it links change. Unset keeps the executable
  hash. Minor.

### Changed

- Go SDK: a list query parameter (`QueryParam<T[]>`) is a `[]T` field of
  the query struct, as the Go route takes it: `[]types.OrderStatus` for an
  enum list, the scalar's type for a UUID, timestamp or other string
  scalar (`[]types.IdentityUUID`), and `[]int64`, `[]float64`, `[]bool` or
  `[]string` otherwise. It was one value (`*string` for an enum or UUID
  list), so the caller joined the items and nothing checked them. The list
  is sent as one comma-separated value (`?statuses=open,paid`) by the new
  `runtime.AddQueryList`, and an empty list is left out, since the route
  refuses a present empty value. Before the request, a required list with
  no item is `required` at `name`, `listMin` and `listMax` bound a
  non-empty list, and each item is checked at `name[i]` with the
  argument's `min`, `max`, `minLength`, `maxLength` and `pattern`, then
  its own validation (an enum member, a non-zero UUID). The route splits
  the value on commas and trims each item, so an empty item is `required`
  and one with a comma or surrounding space is `pattern`. Code that fills
  the query struct changes with the field type. A scalar query parameter,
  and an SDK without a list query parameter, are unchanged. Minor.
- Go ORM: a `Generic.JSON[]` or `Generic.JSON[][]` column refuses a null
  element, as every other list column and every other implementation do
  (D12, amended): `GetOne`, `FindOne`, `FindMany`, `GetManyByIDs`, the
  `RETURNING` reads and the history decoder fail with
  `metadataList[0]: null element` for a stored `[null, true]`, which read
  as the `null` and `true` tokens. That holds for a JSONB column and for a
  native `JSONB[]` one (a `Generic.JSON[]` without `@jsonField`), whose SQL
  NULL element read as a nil value. `CreateOne`, `CreateMany`, `UpdateOne`
  and `UpdateMany` refuse a nil element or the JSON null token before they
  write; they stored it. A row that holds such an element must be
  rewritten before it reads. Minor.
- TypeScript API server: an encrypted operation (in an `Encrypted`
  operation set, declared `@encrypted`, or with an `EncryptedField<T>`
  result) fails the build unless it is `@manualRouteRegistration`, with the
  operation named: `tsrestgen: operation tenant.createTenant is encrypted
  ...; the TypeScript router has no decryption step, declare it
  @manualRouteRegistration and decrypt the payload in the service's
  handler`. The runtime has no decryption step, so the router mounted such
  an operation as an ordinary JSON route and handed the ciphertext to the
  body parser. A schema that selects the TypeScript server and declares an
  encrypted operation marks it `@manualRouteRegistration` and decrypts in
  its `manualRoutes` handler, as it already does for a file upload. The Go
  server is unchanged. Minor.
- Go API: a list argument of a `GET` operation is decoded by
  `bodyargs.QueryList`. Each item is read as its type's JSON value and
  checked as a list element at `name[i]`, with the scalar's and the
  argument's rules: `?scores=1,x` is `type` at `scores[1]` ("expected a
  number"), `?ranks=0` is `min` at `ranks[0]` for `Ordering.Rank`, and a
  malformed UUID or timestamp is `pattern`. Before, the route cast each
  item's string to the element's Go type, so a `GET` operation with a
  list of numbers, integers, booleans, UUIDs or timestamps generated a
  `routes.go` that did not compile; an element's failure was reported at
  `name`; and the argument's `minLength`, `maxLength`, `min` and `max` did
  not apply to an element. A required list with no item (absent,
  `?labels=` or `?labels=,`) is a `required` field error at `name`, where
  it was a plain 400 message. Minor.
- Go API: a query parameter or `GET` argument that breaks a length or a
  list bound is named `minLength`, `maxLength`, `listMin` or `listMax`, as
  in every other validator (D14); it was `min_length`, `max_length`,
  `list_min` or `list_max`. A client that matched the old names matches
  the new ones. Minor.
- Go SDK: a map body argument (`Record<string, T>`, `Record<string, T[]>`)
  is a `map[string]T` (`map[string][]T`) field of the input struct, as the
  Go route takes it; it was typed as its value type, so the SDK sent one
  value where the route expects a JSON object. An optional map is left
  out when nil. Before the request, a nil required map is `required`, a
  nil list value is `required` at `name[key]`, and each value or list
  element runs its own validation at `name[key]` or `name[key][i]`. Code
  that fills the input struct changes with the field type. Minor.
- TypeScript API server: a map body argument (`Record<string, T>`,
  `Record<string, T[]>`) is typed `Record<string, T>` (`Record<string,
  T[]>`) in the implementation's arguments and decoded with the Go
  routes' map rules: a JSON object (`type` otherwise, "expected an
  object"), `{}` satisfies a required map, a value is never null
  (`required` at `name[key]`) and passes the checks of a list element at
  `name[key]`, a list value's elements are checked at `name[key][i]`, and
  list bounds do not bound a map. Before, the argument was typed and
  decoded as its value type, so a JSON object was refused and one bare
  value accepted. An implementation of such an operation changes its
  argument type. Minor.
- OpenAPI: a body argument carries its constraints as a field of an input
  type does. The scalar's own `format`, `pattern`, lengths and range and
  the argument's `minLength`, `maxLength`, `pattern`, `minimum` and
  `maximum` sit on each value (the items of a list, the values of a map),
  and `listMin` and `listMax` are `minItems` and `maxItems` on a list.
  They were left out. Patch.
- IR, breaking: `ir.FieldDef` drops ten per-field directives it carried
  from the source tree, which only one distribution reads:
  `transformDedupKey`, `transformOrdering`, `transformFingerprintInput`,
  `transformPartitionDate`, `transformStructural`,
  `transformPersonEmail`, `transformPersonName`, `transformAccountId`,
  `transformExternalUserId` and `transformForeignKey`, with its type
  `ir.TransformForeignKeyDef`. No core decorator set them and no generator
  read them. They also leave the `FieldDef` types of
  `@superschematic/schema-ir` (the package root and `./schema-file`), the
  schema-file JSON Schema (`./schema-file.json` and `superschematic
  json-schema`), and `@superschematic/schema-runtime`, whose IR reader
  read them and whose JSON Schema reader and writer read and wrote them as
  `x-transform*` keys. A JSON or YAML schema file whose field carries one
  now fails validation (`additional properties 'transformDedupKey' not
  allowed`). The replacement is an extension decorator: a `DecoratorSpec`
  on `TargetField` whose `Apply` writes the field's
  `extensions.<extension>` slot, which every form carries as
  `"extensions": {"<extension>": {"<directive>": <value>}}` (D18); acme's
  `@feedKey` shows it. `temporalFormat` stays in the core. Major.
- SQL and Go ORM: the history of every `@versioned` table (D17). Every
  versioned table's `create.sql` and `drop.sql` change: a database built
  from an older `create.sql` needs the capture function replaced and its
  triggers recreated. A `BEFORE UPDATE` trigger
  (`trg_<table>_bump_version`) sets `NEW._version = OLD._version + 1`, and
  `trg_<table>_capture_history_write` is now `AFTER INSERT OR UPDATE` and
  records the row as stored, so an `INSERT ... ON CONFLICT DO UPDATE`
  records one `UPDATE` with the stored values; it recorded the proposed
  insert as well. A delete tombstone's image is the pre-delete row with
  `_version` set to the tombstone's version; its `deleted_by` (else
  `updated_by`) is the actor in the `history_actor_setting` setting when
  set, else the row's own. The ORM's hard deletes on a versioned table with
  such a column (`DeleteOne`, `DeleteMany` and `DeleteOneIfVersion` without
  `deletedAt`, `HardDeleteOne` with it) set the setting to the context user
  for the statement and clear it after, inside a transaction. `GetVersion`
  no longer returns a tombstone's image, and `GetAsOf` and the
  `ListAsOfBy<Relation>ID` readers treat a key whose latest history row at
  the time is a `DELETE`, or a soft-deleted image, as absent: `GetAsOf`
  returned the pre-delete image as if the row were live. Major.

- `@superschematic/schema-ir` carries the repository's one version, which
  `scripts/bump_version.py` writes, in place of a fixed `0.1.0`, and the
  release packs and publishes it with the other npm packages. Its
  `description` names the new subpaths. Patch.
- Go API: a map body argument (`Record<string, T>`, or `Record<string,
  T[]>`) is decoded as a map. The route read it as one `T` (or `[]T`), so
  `{"toneByName": {"a": "warm"}}` failed as "Invalid request body" while
  `{"toneByName": "warm"}` was accepted, and the implementation took a
  `T`. The implementation now takes `map[string]T` (`map[string][]T`), the
  body must hold a JSON object there (`type`, "expected an object"), and
  each value is checked as a list element at `name[key]` (a list value's
  elements at `name[key][i]`): never null (`required`), then the scalar's
  and the argument's rules and the type's own `Validate`. A
  `Record<string, T>` argument whose `T` is an object type was taken for
  the operation's input type, so the body had to be one `T` and the
  implementation took `input *types.T`; it is a body argument like any
  other map. `openapi.json` describes a map body argument as an object
  with `additionalProperties`, where it wrote the value's schema alone. A
  map argument of a `GET` operation, and a map path or query parameter,
  fail the build with the argument named: the query string has no
  encoding for a map. An implementation of an operation with a map body
  argument changes its signature. Minor.
- Go API: a list argument of a `GET` operation reads every occurrence of
  its query key, each split on commas, as a `QueryParam<T[]>` and the
  TypeScript server already did: `?labels=a,b&labels=c` is `["a", "b",
  "c"]`, where it was `["a", "b"]`. Patch.
- Go API: a route reads each body argument (an argument of an operation
  that is not `GET` and has no input type, other than a path or query
  parameter) from its own JSON value through `runtime/http/go/bodyargs`,
  and answers 400 with field errors at the argument's path, in the
  `errors` object an input type's field errors use. Before, the body
  decoded into an anonymous struct, and an argument was checked only when
  its Go type had a `Validate` method, which a list and a builtin do not.
  A required argument that was absent or null was accepted as nil or its
  zero value; it is `required`, and `[]` still satisfies a required list.
  A null list element became its type's zero value; it is `required` at
  `name[i]` (or `name[i][j]`). A value or element of the wrong JSON type
  failed the request as "Invalid request body"; it is `type` at its path
  ("expected a string", "expected an array", ...). `listMin` and
  `listMax`, and the scalar's own lengths, pattern and range, were not
  checked for a list or a builtin, and the argument's own constraints for
  no body argument; they apply to every value and element, one error per
  value named by its rule (D14). An enum element outside the enum is
  `enum`, an object element's field errors nest under `name[i]`, and a
  string its type's decoder refuses (a malformed UUID) is `pattern`. A
  `Generic.JSON` argument accepted null when required and a null element,
  and an optional one could not be left out (its zero value failed with
  `parse`); it is any JSON value but null, and an optional one that is
  absent or null reaches the implementation empty. The body must be one
  JSON object: `null`, an array or a scalar is 400 "Invalid request body",
  as an empty or malformed body already was. Keys match exactly, where
  `encoding/json` matched a struct field case-insensitively
  (`{"Labels": []}` set `labels`). `routes.go` imports
  `runtime/http/go/bodyargs` and no longer carries `validateListElement`.
  A client that left out a required argument, sent null elements, or sent
  numbers or booleans as strings sends the declared JSON values. Minor.
- `runtime/http/go`: OpenTelemetry `otel`, `otel/sdk`, `otel/trace` and
  `otel/metric` 1.45.0 to 1.46.0. A module that requires the runtime
  resolves 1.46.0 or later. Patch.
- Go API: a route no longer emits the response check after the
  implementation call. It asserted `Validate() interface{ HasErrors() bool }`,
  which no generated type satisfies (their `Validate` returns
  `ValidationErrors`), so it never ran and no response was ever checked.
  Responses are sent as before, and a nil inner list of an array-of-arrays
  response is still sent as `[]`. Patch.
- Go schema runtime: a scalar value is checked against the scalar's IR
  constraints (lengths, pattern, reserved words, range) before the
  registered validator, and a failure is named by them (`minLength`,
  `maxLength`, `pattern`, `min`, `max`); only a value they accept reaches
  the registry. `validate.NewDispatchRegistry`, and so `DefaultRegistry`,
  tags what the scalar core rejects `pattern` instead of `scalar`, with the
  core's message (D14). A too-short `Identity.Name` was `scalar` and is
  `minLength`; code that matches the validator name `scalar` must match
  `pattern` or the constraint's name. Major.
- Go types: `Validate` checks a scalar field once, through a generated
  `validate<Scalar>Value`: a failure of the scalar's own length, pattern
  or range is named by that rule, and the scalar's `Validate` (the scalar
  core) decides only for a value they accept. A malformed URL was
  `pattern` twice and is `pattern` once; a too-long one was `length` and
  `maxLength` and is `maxLength`. A field's own constraints
  (`validateMaxLength`, `validatePattern`, ...) are still checked inline.
  Minor.
- Go types: `UnmarshalJSON` of every generated type refuses a null element
  of a `T[]` field and a null innermost element of a `T[][]` field,
  required or optional, of every element type (`Generic.JSON` and unions
  included), with `decode <Type>: <field>[i]: null element` (or
  `<field>[i][j]`). `json.Unmarshal` decoded it to the element type's zero
  value, which `Validate` could not tell from a real `""`, `0`, `false` or
  empty object, so the generated Go validator was the one validator that
  let a null element through (D12). A null list and a null inner list
  decode as before, and no Go type changes. A payload without a `null`
  token costs one byte search more to decode; one with a null anywhere
  costs one pass over the object's bytes. Behavior change for data a Go
  program receives or has stored: a Go API route answers `400` to a request
  body whose input type holds a null list element, instead of passing a
  zero value to the implementation; an SDK response that holds one fails to
  decode; `FromJSON`, `FromMap` and `FromYAML` fail on one; and the Go ORM
  fails to read a JSON column of an object type whose list holds a null
  element another writer stored (`failed to decode JSON field <column>:
  decode <Type>: <field>[i]: null element`). Such stored values stop
  decoding until the nulls are removed. Not checked: a map whose values are
  lists, and a list or list-of-lists column the ORM reads, which still
  decode a null element to its zero value. Minor.
- TypeScript types: `validate<Type>` rejects a null list element of every
  `T[]` and `T[][]` field as `required` ("required field") at `field[i]`
  or `field[i][j]`, in an optional list too, as the schema runtimes and
  the Python types do (D12). Before, an optional list and a list of
  strings, numbers or objects accepted it. Minor.
- TypeScript types: `validate<Type>` of a type that is not `@strictJSON`
  validates each nested object (a field, a list or list-of-lists element,
  a map value, or the type itself) with `validate<Nested>` and reports its
  errors under the field's path (`points[1].shade`). Before, only a
  `@strictJSON` type validated nested objects, so a bad value inside one
  passed. Minor.

- IR: a type's `strictJSON` key is written after `jsonField` instead of
  after `denyUnknownFields`, the position the source tree's IR uses, so a
  persisted schema from either compares byte for byte. The key is written
  only when set. Patch.
- `ParseOutputs` validates each `outputs.<key>` section against the
  `OutputSchema` of the generator that claims the key, before any generator
  runs; `RegisterGenerator` compiles the schema and rejects one that does
  not compile or has no `OutputKey`. Before, `OutputSchema` was never read.
  A config whose section fails its schema, such as an undeclared key in an
  extension's section, now fails the build. Minor.
- `Registry.Finalize` fails when the `Kinds` list of a generator,
  decorator, document or check names a kind no one registered. Before, such
  a spec was silently inert for that kind. Minor.
- `format` reads the file with the binary's registry, so a file that uses
  a linked extension's kind, decorators or documents converts between JSON
  and YAML. The TypeScript writer fails with the slot's name on extension
  data or documents it cannot render; before, a type's or operation set's
  extension slots were dropped. Minor.
- OpenAPI: the placeholders the generator writes for `info.version` and
  the server URL are `__OPENAPI_VERSION__` and `__OPENAPI_BASE_URL__`. The
  generated Go server replaces both values at startup, so only a reader
  that substitutes the old tokens in the raw `openapi.json` needs to
  change. Patch.
- `tools/openai.json` and `tools/anthropic.json` list only the operations
  with a visible `@mcp`: publishing an operation to a model is opt-in.
  Before, they listed every operation. `tools/schema.json`,
  `tools/mcp-binding.json` and `tools/index.ts` still list every
  operation. An API that relied on the old lists declares `@mcp` on the
  operations it publishes. Major.
- The TypeScript SDK takes a method's JSDoc description from `@docs` when
  the operation has one, as the tool documents do. Patch.
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
- Python types: a `Generic.JSON` scalar validates that its value stays in
  the JSON domain (strings, finite numbers, booleans, null, lists, and
  dicts with string keys, without cycles), and `validate_all` checks a
  required direct `Generic.JSON` field against it. A set, a tuple, NaN or
  an arbitrary object, which JSON cannot carry, now fails validation.
  Minor.
- Go types: an optional field of an API input type (an `InputField[T]`
  wrapper) is tagged `omitzero` instead of `omitempty`, and `InputField`
  gains `IsZero`, so encoding leaves out only a field whose key was absent.
  Before, an unset wrapper encoded as `null`, so re-encoding a decoded
  input turned an omitted key into an explicit null. A present null, false,
  zero, `""` or empty collection is still written. Minor.
- OpenAPI: a component property carries its scalar's `format`, `pattern`,
  `minLength`/`maxLength` and `minimum`/`maximum`, and the field's
  `Validate<>` bounds; on an array they go on `items` and on a map on the
  map values, while `listMin`/`listMax` stay on the array. A scalar's
  `json_schema` type mapping now decides its OpenAPI type for every value
  (before, only `object` overrode the language primitive), and the value
  `any` renders as an empty schema that accepts any JSON value. Minor.
- Array query parameters (`QueryParam<T[]>`) work end to end. The Go API
  handler parses `?name=a,b` (and repeated keys) into a slice, parses and
  validates each item with the element type's parser and validator, applies
  `listMin`/`listMax` to the item count, rejects an empty item, and passes a
  nil slice for an absent optional parameter; the implementation interface
  takes `[]T` instead of a scalar. OpenAPI describes the parameter as an
  array with `style: form`, `explode: false` and `minItems`/`maxItems`. The
  TypeScript SDK types it `T[]`, and the Rust SDK takes `Vec<T>` and sends
  one comma-separated value. Before, the handler parsed the parameter as a
  single scalar. The Rust SDK also drops a zero `listMin` check, which
  compared an unsigned length with zero. Minor.
- The superscalar pin moves to `f1440d9` (`superscalar.pin` and every
  `go.mod`), and the TypeScript and Python scalar catalogs are regenerated
  from it. Generated output changes where a scalar changed:
  - Four new scalars are available to schemas: `AgentSkill.Name`,
    `Git.PathPattern`, `Ordering.Rank` and `Version.SemVer`.
    `Git.PathPattern` reads escapes by the parity of each backslash run:
    `/a\\[b]` (an escaped backslash, then a class) is accepted, and
    `/a\\ ` (a bare trailing space) is rejected.
  - `Generic.JSON` is any JSON value. Its description changes in every
    generated scalar comment and readme; its `json_schema` type mapping is
    `any` (was `object`), which the projection Arrow metadata key
    `scalar.json_schema_type` reports; and it validates through superscalar,
    so the generated TypeScript validator and the Python validator also call
    the library's `validateGenericJSON` / `validate_generic_json`.
  - `Generic.StringMap` is custom-parse. A Go types module that uses it
    emits `ParseGenericStringMap`, and the Go, TypeScript and Python schema
    runtimes parse it to canonical JSON text. superscalar's TypeScript
    `parseGenericStringMap` now returns the decoded map, so the TypeScript
    runtime's default parse registry sends a custom-parse scalar whose
    `json_schema` type is `object` through the core, as it already did for
    `Temporal.DateTime`; stringifying the map gave `[object Object]`.

  Minor.
- superscalar at `f1440d9` identifies a scalar by its canonical name only:
  numeric scalar ids are gone from its C ABI and every binding. The schema
  runtimes call it by name. The Go runtime lists the linked scalars from
  `VALID_SCALARS` (was the keys of `ScalarIDByCanonical`). The TypeScript
  runtime's default parse registry passes the canonical name to the
  superscalar backend (was the id from `scalarIdByCanonical`). The Python
  default validate registry (`_generated_default_registry.py`, regenerated)
  checks each name against `VALID_SCALARS` and passes it to
  `_native.validate` (was the id from `SCALAR_ID_BY_CANONICAL`). Parsing,
  validation and generated code are unchanged. The runtimes no longer work
  with a superscalar that takes ids; the pin and every `go.mod` require one
  that takes names. Patch.
- TypeScript and Python types: a custom-parse scalar whose `json_schema`
  type is `object` (`Generic.StringMap`) takes its TypeScript and Python
  types from the scalar catalog, `Record<string, string>` and
  `Dict[str, str]` (were `string` and `Any`). The TypeScript
  `validate<Symbol>` runs superscalar's parser instead of string checks,
  and `parse<Symbol>` takes the map or its JSON text and returns the map.
  The Python field parses through `parse_generic_string_map` and holds a
  dict; a map with a non-string value now fails validation. OpenAPI types
  such a map's values through `additionalProperties`. Before, a TypeScript
  types package that used `Generic.StringMap` did not compile against the
  pinned superscalar, whose parser returns a map. Minor.
- Go SDK: a path or query parameter typed with an integer, float or
  boolean scalar (`Generic.Int64`, `Generic.Probability`) is `int64`,
  `float64` or `bool`, as a bare `number` or `boolean` already was; before,
  every named scalar was a `string`. A tool call's JSON arguments decode
  into the query struct, so a numeric argument failed to decode into the
  string field. A caller that passed such a parameter as a string passes
  the number. Minor.
- The loader takes each catalog scalar's TypeScript, Python and Rust types
  (`TypeScriptType`, `PythonType` and `RustType` on its superscalar metadata
  row) as the scalar's `typescript`, `python` and `rust` type mappings.
  Before, only the Go, SQL and JSON Schema mappings came from the catalog,
  and the other generators inferred a type from the primitive. A Rust type
  that declares a struct rather than naming a type is skipped. For the core
  catalog, generated output changes only for `Generic.JSON` in TypeScript:
  - A field's type is `GenericJSON`, which is superscalar's `JSONValue`
    (any JSON value), where it was `Record<string, any>`.
  - `types/scalars.ts` re-exports `JSONValue`.
  - The scalar validator skips string checks.

  rustgen's special case for `Generic.StringMap` is gone, because its
  `HashMap` now comes from the catalog. An extension that registers a
  scalar catalog (`RegisterScalars`) sets these fields to choose each
  language's type. Minor.
- Rust types: a `Generic.JSON` field (direct, optional, list, map or map of
  lists) decodes through the scalar crate's lossless adapter,
  `<scalar_rust_crate>::scalars::json_scalar::serde::deserialize`, where
  the crate name comes from the naming file. The field keeps every
  number's digits, and an object whose key spells one of serde_json's
  private marker names stays an object. A union that reaches
  `Generic.JSON`, directly, through an imported member or through a
  recursive type, reads its input through the adapter too; an untagged one
  then tries each member in order. A crate with such a field or union
  depends on the scalar crate and `serde_json`. Minor.
- Schema runtimes (Go, TypeScript, Python): one set of list rules for
  `T[]` and for the outer list of `T[][]` (D12), which changes `T[]`
  validation too. The Go and Python runtimes accept an explicit `[]` for a
  required list (present, not non-empty; `listMin` declares non-emptiness).
  The Go runtime enforces `listMin` and `listMax` ("must contain at least N
  items"). The TypeScript and Python runtimes apply a field's `minLength`,
  `maxLength`, `pattern`, `min` and `max` to each element, as the Go
  runtime did. All three report a null list element as `required` at
  `field[i]` whether or not the schema sets the legacy `elemNonNull`;
  before, the Go runtime and the TypeScript IR reader accepted it. The
  Go, TypeScript and Python runtimes and the TypeScript and Python
  validators report a null inner list as `required` ("required field") and
  a non-list inner value as `type` ("expected an array") at `field[i]`.
  Every runtime suite now asserts the parity matrix of the generated
  validators, `runtime/schema/testdata/validation_parity.json`. Minor.
- Python types: `validate_all` reports a `None` list element at
  `field[i]` (`required`), and a `None` innermost element at
  `field[i][j]`, for `T[]` and `T[][]`; element rules skip it. An optional
  list with a `None` entry no longer also reports `field: invalid` from the
  whole-value check. Minor.
- Verification refuses a DB table column that is an array of arrays of a
  table type (`Table[][]`) with one error naming the field, instead of
  failing later in the sql and orm generators, which keep their check.
  Patch.
- Go types and the Go SDK: an optional list field (`T[]` or `T[][]`, not a
  map) of an output type, and an optional list argument of a Go SDK
  request, is tagged `omitzero` instead of `omitempty`. A nil list is
  still left out, and an explicit `[]` is now written, so decoding and
  re-encoding a payload keeps an empty list present, as the TypeScript,
  Python and Rust types already do. Before, `[]` was dropped on encode and
  came back as absent. Minor.
- IR: `Schema.Validate` no longer checks `Validate<T, { uploadMaxBytes }>`;
  `Schema.ValidateHydrated` does, and the loader runs it once the scalars
  are hydrated, in every form. A caller that relied on `Validate` for the
  check calls `ValidateHydrated` on a hydrated schema. Minor.
- `make build` and the release pipeline build the `superschematic` binary
  with `-trimpath -buildvcs=false`. The binary no longer carries checkout
  paths or the revision, time and dirty bit Go stamps, so a commit or an
  edit that changes no Go source leaves the binary, and the `build-all`
  cache entries keyed on it, as they were. `go version -m` on a release
  binary no longer shows `vcs.*` lines; the tarball's `BUILD_COMMIT` names
  the commit. Patch.
- TypeScript API server: every body argument is decoded from its JSON
  value, as an object-typed or list-of-lists one already was (D12). A list
  of a string, number, boolean, enum or scalar type (`string[]`,
  `number[]`, `E[]`, `Network.Url[]`) went through the query-string
  decoder: it split each element on commas and dropped empty strings,
  turned a null element into `"null"` and an object into
  `"[object Object]"`, accepted `"5"` for a number, and refused `[]` for a
  required list. Now a list is its JSON array, a null element is refused
  at `name[i]` (`required`), an element of the wrong JSON type as `type`,
  and `[]` satisfies a required list. A single value must arrive as its
  JSON type too: `"5"` is no longer a number, `5` no longer a string and
  `"true"` no longer a boolean. A `Generic.JSON` argument (`ParamKind`
  `json`) is any JSON value but null, as the schema runtimes decide for
  null, and reaches the implementation as that value; before, it was
  stringified. A scalar-typed parameter, in the path, the query or the
  body, carries the scalar's lengths, pattern and range (`ParamSpec.scalar`,
  type `ScalarConstraints`), which the runtime checks on every value before
  the argument's own; a constraint refusal names its rule in
  `details.errors` (`pattern`, `minLength`, `maxLength`, `min`, `max`;
  D14). A list in the query string is still read from repeated keys and
  comma-separated values. A client that sent numbers or booleans as
  strings, or several values in one comma-separated string, sends JSON
  values of the declared type. Minor.

### Fixed

- Go API: a public API whose `authDb` declares a version graph did not
  resolve its dependencies. The upstream ORM imports the version-graph
  core's Go binding, and the replace directive in the ORM's `go.mod` does
  not apply to a module that imports the ORM, so `go mod tidy` looked the
  binding up at its placeholder version. The API's `go.mod` now carries
  the binding's replace (`[paths] versiongraph_go`) when its upstream
  schema declares a graph. Output for any other API is unchanged. Patch.
- Go API: a `GET` operation with an optional number, integer, boolean or
  UUID argument, or a timestamp argument, generated a `routes.go` that did
  not compile: the route parsed an optional one into a pointer while the
  implementation takes the value, and cast a timestamp's string to its Go
  type. An optional one that is absent reaches the implementation as its
  zero value, and a timestamp is parsed as a `QueryParam` one is; a
  malformed one answers 400. Patch.
- A struct that holds itself through a cycle of relations to other
  structs compiles in Go and Rust. The Rust types box a field whose struct
  holds the owner back inline, as they boxed a direct self-reference, and
  the ORM's field selections (`<Type>Fields`) take a pointer for such a
  relation's nested selection, as for a self-relation. Output for a schema
  without such a cycle is unchanged. Patch.
- TypeScript types and the Go, TypeScript and Python schema runtimes: a
  list field given a value that is not a list is `type` ("expected an
  array"), required or optional; a required one was `required` and an
  optional one passed. An object-typed field, list element or innermost
  element given a value that is not an object is `type` ("expected an
  object"); it passed. An enum given a value that is not a string is `type`
  in the TypeScript validator, where it was `enum`. A string scalar whose
  TypeScript type is not `string` is checked as a string in the TypeScript
  validator: `Temporal.DateTime` takes a string or a valid `Date` and
  refuses `42`, which `String(value)` let through, and a `Geo.Location`
  object is `type`, where it was `pattern`. A `@strictJSON` type's list of
  objects given a value that is not a list is `type`, where it was `array`.
  The runtimes' lenient parse coerces a numeric or boolean string for the
  IR's `number` and `boolean` as it does for `Float` and `Boolean`, so a
  lenient load of `"5"` into a `number` field is `5`; the Python runtime's
  `Float` coercion no longer reads a boolean as `1.0` or `0.0` (D14,
  amended). Minor.
- String lengths (`minLength`, `maxLength`) count Unicode code points in
  every validator: the generated TypeScript validator, the TypeScript SDK
  and the TypeScript API server's argument lengths counted UTF-16 units,
  and the generated Go validator, the Go runtime's scalar lengths,
  `runtime/http/go/bodyargs`, the Go routes' query parameters and `GET`
  arguments, and the Go and Rust SDKs counted UTF-8 bytes. Five emoji now
  satisfy a `maxLength` of 5 everywhere, and an accented `e` (U+00E9)
  counts one. A generated `types.go` with a length rule imports
  `unicode/utf8` (D14, amended). Minor.
- JSON and YAML readers: a schema file in which an object repeats a key
  is refused, with an error that names the key and its JSON pointer, such
  as `repeated object key "fields" at '/types/Item/fields'`. The slot
  check and the JSON Schema validation read the last copy of a repeated
  key, but the decode read every copy into the same map or struct, so an
  earlier copy reached the IR unvalidated: a type with an unknown role in
  a first `types` copy, a field's unknown `httpMethod` in a first `fields`
  copy, or an extension the registry does not link in a first
  `extensions` copy. Keys compare after their escapes are read. A file
  that repeats a key now fails to load. The YAML reader already refused a
  repeated mapping key. The TypeScript `SchemaFileLoader` refuses the same
  files, with the pointer as the issue's path; `JSON.parse` keeps only the
  last copy, so it reads the text for a repeated key.
  `schema_file_parity.json` holds reject vectors for them. Patch.
- IR: `ir.CanonicalJSON` refuses any text but whitespace after the value.
  It checked `Decoder.More`, which reports false before a closing bracket
  or brace, so it accepted `{}]` and `{}}` and wrote `{}`. A JSON sidecar
  document had the same check and now refuses such text too. Patch.
- Go ORM: `CreateOne` and `CreateMany` insert a required enum field's
  declared default when the Go value is `""`, for an enum declared in the
  schema or imported from a dependency. They inserted `''`, which is not a
  member, so a struct literal that left out a field typed
  `Default<OrderStatus, OrderStatus.Pending>` failed the column's `CHECK`.
  A set value is inserted as before. Optional enums, enum lists and
  required enums without a default are unchanged. Patch.
- Go ORM: a list or list-of-lists column it decodes from JSONB (a `T[][]`,
  or a `T[]` it stores as JSON) refuses a null element when it is read, as
  an object column and the generated types' `UnmarshalJSON` already did:
  `GetOne`, `FindMany` and the history decoder fail with
  `failed to decode JSON field labels: labels[0][1]: null element`. The
  column was decoded into the Go list directly, so a null element another
  writer stored read as the element's zero value (`[["a", null]]` as
  `[["a", ""]]`). A null inner list still reads as a nil list (D12,
  amended). Patch.
- Go SDK: an error response's message is read from the RFC 9457 `detail`
  member first, then `error`, then `message`. A generated Go server writes
  its message only in `detail`, which the Go SDK did not read, so an error
  carried the generic text for its status, such as "api request failed".
  The TypeScript, Python and Rust SDKs already read `detail`.
  `APIError.Error()` adds the error code when the response has one:
  `<message> (code: <code>, status: <status>)`. Without a code it is
  `<message> (status: <status>)` as before. Patch.
- Go types: a union field is validated, and a union without an
  `@internalMetadata` discriminator decodes to the member the payload
  describes. `Validate` emitted nothing for a union field, so an absent
  required union passed, and so did a member missing its own required
  fields; a generated Go API route, which validates its input before the
  implementation runs, let both through. Each `<Union>Wrapper` now has
  `Validate`, which validates the member it holds, by value or by pointer.
  A containing type's `Validate` reports an absent required union
  (`required`), a nil union element of a list, a list of lists or a map
  (`required` at its path), and nests the member's errors under the
  field's path, including a union imported from a dependency. A union
  without a discriminator tried each member's `FromMapStrict` in turn, but
  a member's `UnmarshalJSON` ignores keys it does not declare, so the first
  member took every payload. The wrapper now takes the first member whose
  fields include every payload key and whose tags the payload does not
  contradict. A tag is a field that two or more members declare with
  distinct string or enum defaults, such as
  `kind: Default<TriggerKind, TriggerKind.NewMessage>`. When no member
  declares every key, the wrapper tolerates the unknown keys and takes the
  first member whose tags the payload allows. A Go server now answers 400
  to a union input without the member's required fields, which it used to
  accept. Unions with a discriminator decode as before. Minor.
- Rust types: an untagged union (one without an `@internalMetadata`
  discriminator) decodes to the member the payload describes. serde's
  untagged derive tried each member in turn, and a member's derived
  `Deserialize` ignores keys it does not declare, so the first member whose
  required fields were present took the payload: of two members with the
  same fields, told apart by a defaulted `kind`, the second never decoded,
  and a payload with an unknown key went to the first member it fit. The
  union now decodes through a `serde_json::Value` and picks its member by
  the Go types' rule above, which both generators take from one place. A
  payload that is not a JSON object is refused; serde used to read a JSON
  array as a member struct. A crate with an untagged union now depends on
  `serde_json`. Patch.
- TypeScript types and the Go, TypeScript and Python schema runtimes: a
  value of the wrong JSON type in a field typed `string`, `number` or
  `boolean`, required or optional, single, a `T[]` or `T[][]` element or
  (TypeScript types only) a map value, is one `type` error at its path,
  and its length, pattern and range are not checked. So is a non-string
  value of an optional string scalar, and, in the TypeScript validator, a
  string where a number scalar belongs. Before, `parse<Type>Json` accepted
  `{"x": "far"}` for a number `x`, and a missing required string with a
  `maxLength` below 9 was `required` and `maxLength`, because the
  validator measured `String(undefined)` (D14). A list given a value that
  is not a list is `type`, required or optional (D14, amended). Minor.
- SDK tool documents (TypeScript, Go and Rust): an enum argument or field
  was written as a plain string, so a model was never told the allowed
  values. Its schema now carries `enum` with the serialized values at every
  depth: a path, query or body argument, an input field, a list item at
  either depth, a map value and a field of a nested object or union member;
  a nullable one lists `null` too. A body argument of a map type (an
  operation without an input type) lost its map shape and was written as
  its value type; it is now an object whose `additionalProperties` is the
  value schema, a list for a map of lists, and `tools/index.ts` types it
  `Record<string, T>`. The `inputSchemaDigest` of every tool with an enum
  argument or a map body argument changes; other digests do not.
  `JSONSchemaProperty.enum` in `tools/index.ts` is `Array<string | null>`.
  Patch.
- Go, TypeScript and Python schema runtimes and the generated Go,
  TypeScript and Python validators: `Generic.JSON` is any JSON value but
  null (D14, amended). The runtimes checked it as a string, from the
  `String` primitive of its superscalar metadata row: in parse
  (`ParseType`, `LoadType`, `parseType`, `loadType`, `parse_type`,
  `load_type`) and in validation an object, array, number or boolean was
  `type`, and the Go runtime read a string as JSON text, so `"s"` was
  `pattern`. Now a present value of any JSON type passes, with no type,
  length or pattern check, and the parse step keeps it as it is; a value no
  JSON document can carry (NaN, a set, `undefined` inside an object) is
  `type`. The runtimes key the rule off the scalar's `json_schema` type
  mapping `any`, not its name or primitive. A null or missing required
  `Generic.JSON` is `required` in every validator: the generated
  TypeScript (`validate<Symbol>Required`) and Python (`validate_all`)
  validators took a null one for a present JSON value, and the generated
  Go `Validate` checked neither null nor absence. A null optional one is
  absent, a null list element is `required` at its index (the generated
  Go decoder refuses it, D12), and a map value may still be null. Behavior change: a runtime accepts an
  object, array, number or boolean it refused; a Go API route answers
  `400` with `required` to a request body whose input type has a null or
  missing required `Generic.JSON` field; `parse<Type>Json` in TypeScript
  throws on one, and `validate_all` in Python reports it. New in the `ir`
  module: `ScalarDef.IsAnyJSON` and `JSONSchemaAnyType`. Minor.
- Go, TypeScript and Python schema runtimes, the generated Go, TypeScript
  and Python validators and the TypeScript API server: a scalar whose
  `json_schema` type mapping is `object` or `array` (`Generic.StringMap`,
  `Embedding.Vector`) holds that JSON object or array, the value its
  generated types put on the wire (D14, amended). The runtimes checked it
  as a string, from the `String` primitive of its superscalar metadata
  row: in parse and in validation a map or an array was `type`, and only
  its JSON text passed. Now the object or array passes, and so does its
  JSON text, which parse reads into the object or array; any other JSON
  type is `type`; a null or missing required one is `required`, and an
  empty object or array is a value. The runtimes hand the scalar core the
  value's JSON text, so it still checks a map's values and a vector's
  numbers. The rule keys off the type mapping, not the name; a scalar
  whose metadata also has a pattern or a length (`Geo.Location`) keeps
  its string checks. Behavior change: a runtime accepts a map or an array
  it refused, and `ParseType`, `parseType` and `parse_type` return the
  object or array for its JSON text, where they returned the text. The
  generated TypeScript validator reports a value of another JSON type as
  `type` (it said `parse` for a string map and took anything for a
  vector); the generated Go `Validate` reports a missing required
  `Generic.StringMap` as `required`, which it did not check; the
  generated Python `Embedding.Vector` is `list[float]`, read from the
  list or its JSON text, where it was `Any`; and the Python decoder reads
  the JSON text of either scalar without superscalar installed. The
  TypeScript API server takes a body argument of either scalar as its
  object or array (parameter kinds `jsonObject` and `jsonArray` in
  `@superschematic/http-runtime`), as the Go routes do, where it took
  only a string. New in the `ir` module: `ScalarDef.StructuredJSONType`,
  `JSONSchemaObjectType` and `JSONSchemaArrayType`. Minor.
- Go types: a module with an `Embedding.Vector` field builds.
  `ParseEmbeddingVector` converted the scalar core's canonical text to
  `[]float32`, which does not compile; it now decodes the JSON array, as
  `ParseGenericStringMap` decodes its map. Patch.
- OpenAPI: an `Embedding.Vector` field, or one of any scalar whose
  `json_schema` type mapping is `array`, is an `array` whose items come
  from its type mappings (`number` for `Embedding.Vector`); it was a
  `string`. Patch.
- TypeScript schema runtime: `writeSchemaJson` and `writeScalarsJson`
  write a scalar's JSON Schema type from its `json_schema` mapping: no
  `type` for `Generic.JSON`, whose value is any JSON value, and `object` or
  `array` for a JSON object or array scalar. They wrote `string` for all
  three, from the `String` primitive. Patch.
- TypeScript SDK: `invokeTool` in `tools/index.ts` did not type-check or
  call some SDK methods correctly. A tool with two or more required body
  arguments and no input type passed one object where the method requires
  more arguments (TS2555); `invokeTool` now passes the body arguments as
  one object and `undefined` for the remaining required positions. A
  file-upload tool called the method without its `files` argument (TS2554);
  `invokeTool` now throws for a tool whose `bindingStatus` is
  `unsupported_multipart`. An input type was passed as the parameters
  object, whose keys are camelCase, so a snake_case field such as
  `created_by` was missing (TS2345) and the path parameters went into the
  body; the input is now built from its fields under their own names, and
  body arguments no longer carry the path parameters either. An encrypted
  operation with two or more body arguments got its key in the wrong
  position, in `invokeTool` and in `tools/mcp-binding.json`. A `POST`,
  `PUT` or `PATCH` SDK method with two or more body arguments, called with
  them as one object, dropped the query parameters; it now sends them. The
  SDK package's `tsconfig.json` no longer excludes `tools/`, so its build
  type-checks `tools/index.ts` and emits `dist/tools/`. Minor: a type
  error in `tools/index.ts` now fails the SDK build.
- Go ORM: `GetManyByIDs` keyed its result map on a hard-coded `entity.Id`
  and took UUID keys. A table keyed on a UUID field with another name did
  not compile, and a table keyed on a string `id` always returned an empty
  map. The method now takes and keys on the table's own key: the UUID type
  for a UUID key under any field name, the key's type otherwise
  (`[]string` for a string `id`). A caller of a string-keyed table's
  `GetManyByIDs` passes strings. Minor.
- Go API: `routes.go` imported `time` when any operation declared
  `@rateLimit`, `@bodyLimit` or `@timeout`, but only `@rateLimit` and
  `@timeout` on a route `RegisterRoutes` mounts call it. An API whose only
  such directives were `@bodyLimit`, or sat on `@manualRouteRegistration`
  operations, did not compile. Patch.
- Go ORM: a DB schema whose tables use no UUID scalar got an ORM whose id
  lookups, UUID filter and user context were typed `types.UUID`. The Go
  types package declares only the scalars the schema uses, and never that
  name, so the ORM did not compile. Generation now fails with an error that
  names the schema and asks for a key field of a UUID scalar. Patch.
- Go SDK: a body argument or response whose type is a scalar was typed
  with the last segment of the scalar's name (`types.UserID` for
  `Identity.UserID`, `types.JSON` for `Generic.JSON`). The Go types package
  does not declare those names, so the SDK did not compile. It now uses the
  name the types package declares (`types.IdentityUserID`,
  `types.GenericJSON`). Patch.
- JSON and YAML readers: the keys that mark a multi-definition schema
  file without a `kind` or `role` included six that the document form does
  not have. A file whose only top-level key was one of them was read as a
  document and failed on an unknown key; it now fails with the "cannot
  determine schema file shape" error. A test keeps the list equal to the
  document's collection fields. Patch.
- Go API, `session` auth provider: when the upstream DB has a `Session`
  table, the provider's `middlewareStdImports` snippet imported `"time"`,
  which `middleware.go` already imports. go/format drops the duplicate, so
  a normal build compiled, but a `--skip-format` build wrote `"time"` twice
  and `middleware.go` did not compile. The snippet no longer imports it.
  Patch.
- Build cache: the authoring-import depfile of a service with sidecar
  documents went to `<repo>/schemas/dist/.authoring-imports/` on a single
  `build` or `build --with-deps` whose schemas root had another name,
  because only `build-all` set the schemas directory. Each command also
  derived the location from the output root, so with `--out` the depfile
  went beside the output (and a build failed when the directory above
  `--out` did not exist), where the input hash never read it. Every
  command now sets the schemas root it resolved, and the depfile goes
  under `<schemas-root>/dist/` whatever `--out` is. Patch.
- Python schema runtime: `parse_schema` named the schema from the root
  `title` only, so a document that carries its identifier in `name` and a
  display title in `title` got the display title as its name. It now reads
  `name` first and falls back to `title`. Patch.
- Rust API server: an operation set with a multi-word name
  (`PoolSearchMutations`, namespace `pool-search`) put the kebab-case
  namespace into its handler names (`handle_pool-search_...`), and the
  crate did not compile. The router now snake-cases the namespace in
  handler names, as the implementations struct already did. A single-word
  namespace renders the same bytes as before. Patch.
- TypeScript SDK: `tools/index.ts` typed a tool parameter from its JSON
  Schema, so an enum was `string`, an object `Record<string, unknown>` or an
  inline shape, a union `Record<string, unknown>` and a date-time or JSON
  scalar field `string` or `unknown`, and `invokeTool` did not type-check
  against an SDK method taking an input type with such a field. A
  parameter now has the type the SDK method takes: the generated enum,
  object type or union, imported from the types package, and a scalar
  field of the input type as `<Input>['<field>']`. A caller that passed a
  string where an enum is taken passes the enum member. Minor.
- TypeScript types: a types package names a sibling types package with
  `workspace:*` instead of `file:../<schema>`. With the `file:` spec, the
  second `bun install` in the types workspace (Bun 1.4.0), or the first
  after a package gained such a spec (Bun 1.4.2), read the scalar
  library's `file:` path from the wrong directory and failed. The
  TypeScript gates in `go test` now fail instead of skipping under
  `SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1`, and the CI `go` job installs
  `packages/` so the schema-config JSON Schema drift test runs. Minor.
- TypeScript and Python schema runtimes: a scalar value the IR constraints
  reject is no longer also handed to the registered scalar validator, which
  checks the same pattern again; a malformed URL was `pattern` twice in the
  TypeScript runtime and is `pattern` once (D14). Patch.
- TypeScript types: `validate<Scalar>Required` reports a non-string value
  of a string scalar as `type` ("expected string value"), as the schema
  runtimes do, instead of formatting it and reporting the pattern it
  fails. A scalar with a custom validator in the scalar core runs it only
  when its own pattern and length checks pass, so a malformed email is
  `pattern` once. Patch.
- Python types: `validate_all` reports a malformed optional scalar value
  once, as `pattern`. The type check with the scalar's pydantic type runs
  after the field's rules and reports `invalid` only for a failure they
  did not find. Patch.

- Go API: a field is a multipart upload only when its scalar carries
  `fileUpload` metadata. Before, four scalar names (`Artifact.File`,
  `Asset.File`, `Asset.Image`, `Asset.LogoImage`) were treated as uploads
  without it, with a 100 MiB limit and no allowed types. A catalog that
  registers those names declares `fileUpload` on them
  (`registry.ScalarCatalogWithUploads`). Minor.
- `build-all` writes the TypeScript types workspace manifest
  (`types/typescript/package.json`) on a run where every service was
  restored from the cache or up to date. Before, only a service's types
  build wrote it, so a fully cached run could leave it missing. Patch.
- `tools/mcp-binding.json` listed a method's query object before its
  input, while the generated methods take path, input, query, options; a
  consumer that followed the positions passed them in the wrong order. It
  now lists them in the method's order. Patch.
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
  `Parse<Symbol>` and decodes the canonical JSON into the alias.
  `Generic.StringMap` takes this path. Patch.
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
- Go types: union fields decode in every shape. A map or map-of-lists of a
  union decodes each value through the union's wrapper (before, the
  generated `UnmarshalJSON` did not compile); an optional union field is the
  nilable union interface instead of a pointer to it; an optional input
  union keeps absent, null and a value apart in its `InputField`; and a
  field typed with a union from a dependency is treated as a union. A
  module that re-exports an imported union also re-exports its
  `<Union>Wrapper`. Minor.
- Go ORM: a JSONB column typed with a closed union (local or imported from
  a dependency), alone, nullable, in a list or in a map, decodes through
  the union's `<Union>Wrapper` in every read path and in the history
  decoder; before, the generated code decoded into an interface, which
  fails. A `Generic.JSON` column keeps the JSON `null` token as a value: a
  required one reads `null` as `null`, and a nullable one reads a JSONB
  `null` as a pointer to `null` and only SQL NULL as nil. A selected-field
  read now returns a JSON decode error instead of dropping it. Minor.
- Go ORM and Go types: the generated `go.mod` replaces every declared schema
  dependency's types module, not only the ones this module imports
  directly. Go does not inherit `replace` lines from a dependency's
  `go.mod`, so a module that reached a sibling only through another
  generated module did not resolve it. Patch.
- Go ORM: an optional map column did not compile. `NewXSnapshotUpdate`
  compared the map with a zero value of its element type, `ApplyTo`
  assigned that zero value on `SetNull`, and a map of a scalar or enum was
  treated as a pointer and assigned without a dereference. An optional map
  is now nil-checked like a list, and its `<Type>Update` field holds the map
  type the types module emits: `map[string]*T` for a non-union value (was
  `map[string]T`). A table whose only optional string field is a map no
  longer imports `database/sql` without using it. Patch.
- Go types: an optional map or map of lists of a union on an output type
  was `map[string]*Choice` (`map[string][]*Choice`), while its generated
  `UnmarshalJSON` builds `map[string]Choice`, so the module did not
  compile. It is now `map[string]Choice` (`map[string][]Choice`), as the
  required map and the optional input map already were. Patch.
- Rust SDK: an array query parameter was validated as its comma-joined
  wire text, so the pattern and length checks saw `a,b`, `min` and `max`
  tried to parse `1,5` as one number, and the list count split items that
  contain a comma. The generated server checks each item, so the SDK
  rejected requests the server accepts. The SDK now checks each item and
  counts the list it was given, in the JSON and the multipart methods.
  Patch.
- Go types: a map field of a scalar or enum (`Record<string, Identity.UUID>`),
  and a `minLength`, `maxLength`, `pattern`, `min` or `max` rule on a map
  field, did not compile: `Validate` called the scalar's methods on the map
  itself. `Validate` now checks each entry and reports its errors under
  `name[key]`; a nil required map reports `required`, an optional map skips
  a null entry, and an input map is checked when present and not null.
  `MaskSecrets` on an optional map of a generated type, which did not
  compile either, keeps a null entry null. Patch.
- TypeScript types: `parse<Type>FromJSON` for a type with an optional map
  of a nested type returned a null or absent entry as is, typed `unknown`,
  so the package did not type-check against the map's `T | null` values.
  It now maps such an entry to `null`. Patch.
- Python types: a discriminated union whose discriminator is not already
  snake_case (`eventKind`) named it as written in
  `Field(discriminator=...)`. Pydantic resolves that name against the
  members' Python field names (`event_kind`), so importing the package
  failed. The union now names the Python field. Patch.
- Rust types: a discriminated union failed to decode a member with a
  floating-point field (`invalid type: map, expected f64`) whenever
  serde_json's `arbitrary_precision` feature was on anywhere in the build.
  Cargo unifies features, so any crate in the graph could turn it on. The
  union now decodes through a `serde_json::Value` and picks the member by
  its tag; a missing or unknown tag is an error that names the union. A
  crate with a discriminated union depends on `serde_json`. Patch.
- TypeScript loader: a class that extends a class from another service
  failed to load (`references unknown type`) when an inherited field
  referenced a type or enum declared in that service. The loader flattens
  the base's fields into the class but did not record the types they
  reference as imports. It now records them, so the generated code
  imports them from the base's package. Patch.
- TypeScript SDK: a file-upload endpoint with query parameters had no way
  to pass them; the convenience and raw methods now take
  `params?: { ... }` before `signal` and send them. A DELETE endpoint that
  declares an input dropped it, and the Go API handler, which decodes the
  body of every method with an input, answered "Invalid request body"; the
  SDK now sends the input as the DELETE body. The Go, Python and Rust SDKs
  already did both. Patch.
- `superschematic format --to=ts` (the TypeScript writer): a schema that
  referenced a catalog scalar with bounds (`Generic.Int64`,
  `Temporal.Seconds`, `Ordering.Rank`) or with a hydrated TypeScript,
  Python or Rust type failed with "declares metadata beyond its language
  primitive". The writer now strips every bound and type mapping that
  equals the catalog's before it checks a scalar reference. Patch.
- Go types: an optional map of a generated type (`map[string]*T`) on an
  input type did not compile: `MaskSecrets` and `Validate` called the
  type's methods on the map itself. They now mask and validate each entry,
  keep a null entry null and report an entry's errors under `name[key]`.
  On an output type, `Validate` called `Validate` on a null entry, which
  panics when the type checks any field; it now skips the entry. An input
  whose map values are the input twin of a paired type
  (`Record<string, TInput>` on the input of a `@jsonField` type) rendered
  `To<Type>` as `Tomap[string]T()` and the module failed to format; it now
  converts each entry and keeps a null entry null. Patch.
- Go types: encoding a value (`MarshalJSON`) set every nil list it
  reached to `[]`, including a field tagged `json:"-"`, which is not on the
  wire, so encoding changed a value's in-memory metadata. It now skips
  fields tagged `json:"-"` and unexported fields. Patch.
- `build-all` and `build --with-deps`: a `schema.config.ts` that imported
  the config package under a specifier `[package_aliases]` maps onto
  `@superschematic/schema-config` failed discovery with "schema.config.ts
  may import only @superschematic/schema-config", so a distribution that
  republishes the authoring packages under its own names could build none
  of its services with either command. The check resolves each import
  through the alias table, and its error names every accepted specifier.
  Patch.
- `Validate<T, { uploadMaxBytes }>` on a file-upload scalar failed to load
  from TypeScript with "uploadMaxBytes requires a file-upload scalar". The
  TypeScript frontend checked the bound before the loader hydrated the
  scalar, when a brand carries only its name, so no upload scalar could
  pass. The check runs after hydration in every form, against the upload
  metadata the registered catalog declares. Patch.
- TypeScript API server: a body argument that is a list of an object type
  (`points: Point[]`) was typed `string[]`, and the runtime turned each
  element into a string, so the implementation received
  `"[object Object]"`. It is now typed `Point[]`, and each element goes
  through the generated `parse<T>Json` with the list rules: a null element
  is refused at `name[i]` (`required`), a non-object one (`type`), and one
  the parser refuses ("does not match the declared type"). A single
  object-typed body argument that is not the input (a DB table type) is
  parsed the same way. A union-typed body argument, alone or in a list,
  now fails the build ("tsrestgen cannot decode body argument"). The
  runtime exports `decodeJsonParam`. Minor.

[Unreleased]: https://github.com/parable-work/superschematic/commits/main
