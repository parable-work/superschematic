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

### Changed

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
- The superscalar pin moves to `79a8e6a` (`superscalar.pin` and every
  `go.mod`), and the TypeScript and Python scalar catalogs are regenerated
  from it. Generated output changes where a scalar changed:
  - Four new scalars are available to schemas: `AgentSkill.Name`,
    `Git.PathPattern`, `Ordering.Rank` and `Version.SemVer`.
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
  - A required `Generic.JSON` treats JSON `null` as present and only
    `undefined` as missing, as Go and Python already did.

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

### Fixed

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
