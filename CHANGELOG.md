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
  method, and every projection column, join and row rule. No generator
  renders it yet: each one fails with "<generator> does not support
  arrays of arrays yet", and `apigen.Param` and `apigen.EndpointInfo`
  carry `IsArrayOfArrays` / `OutputIsArrayOfArrays` for the SDK
  generators. Minor.
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

### Changed

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
  dicts with string keys, without cycles) and a required direct
  `Generic.JSON` field accepts `None` as the JSON `null` value in
  `validate_all` instead of reporting it missing. A set, a tuple, NaN or an
  arbitrary object, which JSON cannot carry, now fails validation. Minor.
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

### Fixed

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

[Unreleased]: https://github.com/parable-work/superschematic/commits/main
