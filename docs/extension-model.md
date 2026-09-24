# Extension model

This is the design of the extension seam as it is built in this repository:
what an extension can register, how a binary assembles the registry, where
extension data lives in the IR, how the loader and the generators consult
the registry, and which tests prove that an extension needs no core edit.

Two pages walk the same surfaces with code, and this document does not
repeat them:

- The [write an extension](https://parable-work.github.io/superschematic/guides/write-an-extension/)
  guide (source: `docs/src/content/docs/guides/write-an-extension.md`).
- `examples/acme-schematic/README.md`, which walks every file of the
  example extension.

Source comments cite this document by section number ("docs/extension-model.md
section 8.2"). Keep the numbers stable: add a section at the end of its group
rather than renumbering. When this document and the code disagree, the code
is right and this document is the defect. Section 11 lists the places where
the code does less than the model implies.

## 1. What an extension is

An extension is a Go value that implements `registry.Extension`:

```go
type Extension interface {
    Name() string
    Register(r *Registry) error
}
```

A binary links extensions at compile time by passing them to `cli.New`
(section 3.9). There are no plugins, no extension processes and no
configuration that turns a linked extension off: every extension a binary
links is active for every command it runs.

`Register` adds specs to the registry. The surfaces:

| Surface | Registered with | Section |
| --- | --- | --- |
| Schema kind | `RegisterKind(KindSpec)` | 3.3 |
| Decorator | `RegisterDecorator(DecoratorSpec)` | 3.4 |
| Sidecar document | `RegisterDocument(DocumentSpec)` | 3.5 |
| Generator | `RegisterGenerator(GeneratorSpec)` | 3.6 |
| Auth provider | `RegisterAuthProvider(AuthProvider)` | 3.7, 8 |
| Build-all hook | `RegisterBuildAllHook(BuildAllHook)` | 3.8 |
| Subcommand | `cli.CommandProvider` on the extension value | 3.9 |
| Scalar catalog | `RegisterScalars(owner, ScalarCatalog)` | 3.10 |
| Configuration | `[extension.<name>]` in `superschematic.toml` | 3.11 |
| Check | `RegisterCheck(CheckSpec)` | 3.12 |
| OpenAPI hook | `RegisterOpenAPIHook(OpenAPIHook)` | 3.13 |
| Tool hook | `RegisterToolHook(ToolHook)` | 3.14 |
| MCP tool invocation policy | `RegisterToolInvocationPolicy(ToolInvocationPolicy)` | 3.15 |

`Name()` is the key of everything the extension owns: the `Extension` field
of each spec it registers, the `extensions.<name>` slot on IR nodes
(section 4) and the `[extension.<name>]` table of the naming file.

An extension cannot:

- add an authoring form, a class shape or a type wrapper. The TypeScript
  walker, the JSON and YAML readers and the wrapper table belong to the core
  (section 6.2);
- replace a core kind, decorator, generator or provider. Every `Register*`
  call rejects a duplicate key;
- change the `KindSpec` of a kind it did not register: its `Verify`, its
  pipeline or its import rules. A generator it registers can still run on
  that kind (section 3.6), and a check it registers can still verify it
  (section 3.12);
- change the content of a core template, except through the auth snippet
  hook points (section 8.1). The OpenAPI and tool hooks (sections 3.13
  and 3.14) edit documents the core has built, not its templates.

## 2. Goals and limits

Goals:

1. A downstream module adds a kind, a decorator, a document, a generator,
   an auth provider and a command without editing a file in this
   repository.
2. The core-only binary is the same program with no extensions:
   `cmd/superschematic` is `cli.New(cli.Config{})`.
3. Extension data round-trips through the TypeScript, JSON and YAML
   authoring forms and the persisted IR, and the core never knows its shape.
4. Mistakes fail closed and early. A bad registration fails when the
   registry is assembled, before any schema loads. An unknown kind,
   decorator, `extensions` key or `documents` key fails the load and names
   the key.

Limits:

- Extensions are Go packages compiled into the binary. Loading one at run
  time is out of scope.
- The extension API (`registry`, `loader`, `cli`, `schemadeps`,
  `generator.Naming`) is not frozen before the first release; see the
  README, "Status".

## 3. The Extension interface and the Registry

`registry.Registry` holds every kind, decorator, document, generator, auth
provider, build-all hook, check, OpenAPI hook, tool hook and the scalar
catalog a run knows about. It is
built per command (or per test) and threaded through the loader
(`loader.WithRegistry`) and the generators (`Options.Registry`). There is no
package-level registry.

### 3.1 Package placement

The registry types live in `internal/registry`. Both the loader and the
generators consult it, so it sits below both in the import graph. It
imports the IR, the superscalar Go package, `internal/loader/schemaconfig`,
`internal/profile` and the generator leaf packages `apigen` (with
`apigen/sessionauth`), `envgen`, `codegen` and `naming`. It must never
import the loader root, the generator root, `tsreader`, `schemafile` or
`buildplan`, because those import it.

Two consequences:

- `AuthProvider` and `AuthModel` are declared in `apigen`, which consumes
  them, and aliased in `internal/registry`. `apigen` cannot import the
  registry, because the registry imports `apigen` for `APIOutput`.
  `OpenAPIHook` and `ToolHook` are declared there for the same reason:
  the `api` generator runs them.
- Core registration is split by owner. `internal/registry` registers the
  core kinds, the core decorators and the `session` auth provider in `New`.
  The core generators are registered by `generator.RegisterCore` in
  `internal/generator/core.go`, because their closures call generator
  internals.

An extension in another module cannot import `internal/`. It imports the
public packages at the module root:

| Package | What it is |
| --- | --- |
| `registry` | Aliases and forwarding functions over `internal/registry`, plus the helpers an extension calls: `Assemble`, `DecodeArgs`, `ArgErrorf`, `ParseOutputs`, `DecodeOutput`, `Generate`, `EnvConfigOf`, `HasTable`, `AnalyzeSessionStores`, `AuthSnippetFunc`, `CoreScalars`, `ScalarCatalogOf`, `DefaultNaming`, `LoadNaming`, `ParseNaming`, `GoPublicIdentifier`. The hook types (`BuildAllService`, `CheckSpec`, `VerifyReporter`, `OpenAPIHook`, `ToolHook`, `ToolSet`, `Tool`, `ToolKeys`, `ToolKeyValue`) and the default vendor keys (`OpenAPIDocsKey`, `DefaultToolScalarKey`, `DefaultToolGuidanceKey`) are aliased here too |
| `loader` | `LoadService` and `LoadServiceWithConfig` with `WithRegistry`, `WithNaming` and `WithSchemaCatalog`, for extension tests against real fixtures; `NewDeclarationProgram`, a type-checked TypeScript program over in-memory files with the loader's compiler, lib files and module resolution, for an extension that checks declarations the schema frontend does not walk; `SchemaError` and `SchemaErrorList`, its located diagnostics |
| `cli` | `cli.New`, `cli.Config`, `cli.CommandProvider` |
| `ir` | The IR, its own Go module, with the extension codecs (section 4.2) |
| `schemadeps` | The dependency graph of the generated packages, which `build-all` writes to `<dist>/.deps.json` and, with `[deps] copy`, to a path a repository commits. Every package names the service that produced it. `Read`, `Closure`, `WriteFileAtomic` and `SyncCopy` (check or refresh the committed copy) are what an extension's command over the graph needs (section 3.8) |

`registry` and `loader` are alias packages rather than the implementation;
D2 in `docs/DECISIONS.md` records why. Every identifier in them is an alias
or a one-line forward, so the two layers cannot drift.

### 3.2 Assembly

A registry is assembled in four steps:

```go
reg := registry.New(naming)       // core kinds and decorators, session
err := registry.RegisterCore(reg) // core generators
err = reg.Use(exts...)            // each extension's Register, in order
err = reg.Finalize()              // cross-reference checks; now fixed
```

`registry.Assemble(naming, exts...)` runs all four; `build`, `build-all`,
`json-schema` and `format` call it once per invocation.
`generator.CoreRegistry(naming)` runs them with no extension and panics on
error. `generator.Run` falls back to a core-only registry when
`Options.Registry` is nil and returns an error when the naming selects an
auth provider the core does not have.

`Use` is fail-closed. An extension with an empty name, or a `Register` that
returns an error, stops `Use`; the error is recorded and `Finalize` returns
it, so a half-registered extension cannot be used.

Each `Register*` method checks its own spec:

| Method | Rejects |
| --- | --- |
| `RegisterKind` | an empty name, a duplicate name |
| `RegisterDecorator` | an empty name or target, no `Packages`, an extension decorator without `Apply`, a duplicate `(Name, Target)`, an `Args` schema that does not compile |
| `RegisterDocument` | an empty name, a duplicate name |
| `RegisterGenerator` | an empty name, no `Generate`, a duplicate name, an `OutputSchema` without an `OutputKey` or one that does not compile |
| `RegisterAuthProvider` | a nil provider, an empty name, a duplicate name |
| `RegisterBuildAllHook` | an empty name, no `Run`, a duplicate name |
| `RegisterCheck` | an empty name, no `Verify`, a duplicate name |
| `RegisterOpenAPIHook` | an empty name, no `Edit`, a duplicate name |
| `RegisterToolHook` | an empty name, no `Edit`, a duplicate name |
| `RegisterScalars` | an empty owner, a nil catalog, a second catalog |
| `RegisterToolInvocationPolicy` | no `Extension`, a key, value list or default that fails `Validate`, a second policy |

Every one of them fails after `Finalize`.

`Finalize` checks what needs the whole registry:

- `auth_provider` in the naming file names a registered provider. The
  error lists the registered ones.
- Every `KindSpec.Pipeline` entry names a registered generator, and that
  generator's `Kinds`, when set, include the kind.
- Every `Kinds` list on a generator, decorator, document or check names
  registered kinds. A misspelt or missing kind fails assembly instead of
  leaving the spec inert for it.
- No two generators claim the same `OutputKey`.

`Registry.Extensions()` is the set of names passed to `Use` or set on a
spec. It closes the data-form `extensions` objects (section 5).

### 3.3 KindSpec

A kind is the `kind` of a schema config and `ir.Schema.Kind`. The spec
decides what a schema of that kind may author and which generators run.

| Field | Read by | Meaning |
| --- | --- | --- |
| `Name` | everything | the kind string |
| `Extension` | `Extensions()` | the registering extension; empty for core |
| `StructRole` | TypeScript walker | the `ir.Role` of a plain class: `DBTable` for DB, `EmbeddedStruct` elsewhere |
| `SourceProjectionRole` | TypeScript walker | the role of a `@source(...)` class; zero forbids `@source` |
| `AllowsOperationSets` | TypeScript walker | whether classes with methods are allowed; only API sets it |
| `ForbiddenPackages` | verify | authoring packages the kind may not import, keyed by declaring package |
| `AllowedReferences`, `DeniedReferences` | verify | cross-kind type references: an allowlist, a denylist, or neither |
| `Pipeline` | `generator.Run` | generator names, in run order; empty is valid |
| `NoSentinel` | `build`, `build-all`, sentinel sweep | the kind groups other services; no `src/service.generated.ts` is written for it |
| `ImportsSiblingSentinels` | `build`, verify | schema files import other services' sentinels as values; `build` writes every sibling's sentinel first, and verify applies no reference rule to those imports |
| `Verify` | verify | extra checks on the assembled schema, after the core checks, in every frontend |

The core kinds:

| Kind | Struct role | `@source` role | Operation sets | Pipeline |
| --- | --- | --- | --- | --- |
| `DB` | `DBTable` | not allowed | no | `sql`, `orm`, `types` |
| `API` | `EmbeddedStruct` | `APIView` | yes | `types`, `api`, `sdks` |
| `General` | `EmbeddedStruct` | `EmbeddedStruct` | no | `types`, `envConfig` |

DB may not import `@superschematic/api`, API may not import
`@superschematic/db`, General may import neither. DB may reference General
types, API may reference DB and General types, General may not reference
DB or API types.

`ir.SchemaKind` is a named string with constants for the three core kinds.
Any registered name is a valid value.

### 3.4 DecoratorSpec

A decorator spec is keyed by `(Name, Target)`. The same name may be
registered for several targets; the core does this for `jsonField`,
`docs`, `icon` and the middleware trio.

| Field | Meaning |
| --- | --- |
| `Name` | the name without `@` |
| `Extension` | the registering extension; empty for core |
| `Packages` | the npm packages that declare the decorator; each joins the authoring set (section 6.1) |
| `Target` | `TargetType` (class), `TargetField` (property), `TargetOperationSet` (class with methods) or `TargetOperation` (method) |
| `Kinds` | kinds the decorator is allowed in; nil means any |
| `Args` | the JSON Schema of the single argument; nil means no argument, written as `true` in the data forms |
| `Apply` | writes the decorator into the IR node |

`Apply(node, args, site)` receives the statically evaluated arguments
(strings, `float64`, bools, nil, `[]any`, `map[string]any`), after both
frontends have validated them against `Args`. `registry.DecodeArgs` decodes
the argument into a Go struct. An `ArgError` (`registry.ArgErrorf`) points
the TypeScript diagnostic at one argument instead of the decorator.

A core spec can also take class type arguments. `DecoratorSpec.TypeArgs()`
is their number; `@projection<Source>` and `@join<Table>` take one each.
The TypeScript frontend resolves each type argument to the name of the
schema class it references and passes the names to `Apply` ahead of the
value arguments, and an `ArgError` index counts them first. Only the core
sets it: the data forms write core decorators as typed IR fields and have
no place for a type argument in an extension slot.

`Node` holds the schema and the target holder: `Type`, `Field` or
`OperationSet`. Operations are `FieldDef`s with an HTTP method, so
`TargetOperation` sets `Field`. `Apply` should read only the target: the
data-form readers also fill the enclosing type or operation set, the
TypeScript walker does not.

Core decorators write typed IR fields. Extension decorators write the
node's `Extensions[<extension name>]` slot (section 4). A spec with a nil
`Apply` is a marker the frontend interprets itself; only the core registers
those (`trait`, `source`, `envVars`, `versioned`), and `RegisterDecorator`
refuses an extension decorator without `Apply`.

A decorator has a TypeScript half: a function the extension's npm package
exports that does nothing at run time, so the author gets completion and
`tsc` checks the argument type. The Go spec is the authority.

### 3.5 DocumentSpec

A document is an input that does not belong in a schema file: a sidecar
next to `schema.config.*` that the loader reads and stores on the schema.
Deployment values are the usual case; `extensions/deploy` is a
document-only extension.

| Field | Meaning |
| --- | --- |
| `Name` | the key under `Schema.Documents` and under `documents` in a data-form schema file |
| `Extension` | the registering extension |
| `File` | the sidecar path relative to the service directory |
| `Kinds` | kinds that may carry the document; nil means any |
| `Loader` | reads the sidecar and returns its JSON plus the files it depends on |
| `Schema` | the JSON Schema of the loaded document |
| `Dirs` | the directories `Generate` writes under the output root |
| `Generate` | emits the document's outputs; nil for a document that only a build-all hook reads |

Loading (`internal/loader/documents.go`), for every registered spec with a
`Loader`:

- A spec with a `File` runs only when that file exists. Absence is not an
  error. A spec with no `File` runs once per service.
- A sidecar in a service whose kind is not in `Kinds` is a load error.
- A sidecar for a document the data-form schema files already define under
  `documents.<name>` is a load error.
- The loaded JSON is stored in canonical form (section 4.2). The imports
  the loader returns join `Schema.AuthoringImports`, which the build cache
  tracks for invalidation.

`LoadContext` gives the loader the service path, the schema loaded so far,
its config, the registry, the discovered schema set (`Catalog`, only under
`build-all`), `DecodeData` (read a JSON or YAML file and validate it against
a JSON Schema) and `RunModule` (evaluate a TypeScript module with the bun
harness and return its default export as JSON).

A spec without a `Loader` is data-form only: the document can appear under
`documents.<name>` in a JSON or YAML schema file and nowhere else.

Document generators are driven by presence, not by the kind's pipeline
(section 7.1).

### 3.6 GeneratorSpec

| Field | Meaning |
| --- | --- |
| `Name` | the identifier a `KindSpec.Pipeline` names |
| `Extension` | the registering extension |
| `Kinds` | kinds whose pipelines may name the generator; it is appended to those it is not named in. nil means any kind and appended to none |
| `OutputKey` | the `outputs.<key>` of `schema.config` the generator claims |
| `OutputSchema` | the JSON Schema of `outputs.<key>`, compiled at registration. `ParseOutputs` validates the section against it before any generator runs, whichever form the config is in. nil leaves the section to the generator |
| `Dirs` | the directories the generator writes for this run |
| `Enabled` | whether it runs; a non-empty reason is recorded in `Result.Skipped`. nil means enabled |
| `Generate` | writes the outputs |

A generator reaches a kind in one of two ways. The kind's `Pipeline` names
it, and it runs at that position. Or the generator lists the kind in
`Kinds` and the kind does not name it, and it runs after the kind's own
pipeline, in registration order. The second way is how an extension adds
output to the core kinds without touching them: acme's `acmeManifest`
lists `DB`, `API`, `General` and `Catalog` and has no `OutputKey`, so it
runs on every build and a schema config cannot turn it off.

`GenerateContext` is the per-run state every generator in a pipeline
shares: the schema, its config, the parsed outputs, the options, the
registry, memoized `LoadDependency`, `APIOutput` and `EnvConfig` closures,
and the `Result`. `Done(key, dir)` and `Skip(key)` record outputs.
`InstallTargetDir` resolves a directory relative to the repository root for
a generator that installs files outside the output root.

The core generators:

| Name | Output key | Writes |
| --- | --- | --- |
| `types` | `types` | Go, TypeScript, Python and Rust types, one switch per language |
| `sql` | `sql` | Postgres DDL, projection views with their migrations and Arrow schemas; implied by the DB kind, and `outputs.sql` places the view migrations (`migrationsDir`) and sets their role (`viewOwner`) |
| `orm` | none | the Go ORM; implied by the DB kind |
| `api` | `api` | the Go chi server, the Rust axum crate or the TypeScript Hono package (`outputs.api.language`), and OpenAPI |
| `sdks` | `sdk` | TypeScript, Go, Python and Rust clients, one switch per language |
| `envConfig` | none | the environment loader for a schema with an `@envVars` class |

### 3.7 AuthProvider

An auth provider supplies the authentication half of a generated API. The
core registers `session`; an extension registers others with
`RegisterAuthProvider`, and `auth_provider` in the naming file selects the
one the `api` generator renders with. Section 8 is the design.

### 3.8 BuildAllHook

```go
type BuildAllHook struct {
    Name      string
    Extension string
    Run       func(ctx context.Context, bc BuildAllContext) error
}
```

`build-all` runs the hooks once, in registration order, after every
service's output is in place and the dependency graph (`.deps.json`) is
written. They run on every `build-all`, including one where every service
was up to date or restored from the build cache and nothing was built. The
first error stops the run and is wrapped as `build-all hook <name>: ...`
so the failing hook is named. `BuildAllContext` carries the service names
in build order, `Services` (every discovered service in build order as a
`BuildAllService`: name, kind, service directory and the output
directories the build cache stores and restores), `SchemaFor` (the loaded
IR of a service this process loaded, so not one it restored or found up to
date), the repository root, the output root, the naming and the log.
`build` of a single service runs no hooks.

A hook is for output that needs every service at once, such as values
merged across services into one chart. It reads what it merges from
`Services[i].OutputDirs`, which a cached run fills as well as a full one.
The core registers none. acme's `acmeInventory` merges every service's
manifest (section 10); `cli/build_all_hooks_test.go` covers ordering, error
attribution and a hook across built, up-to-date and cache-restored runs.

A command that works on the dependency graph after the build, such as a
pin command, reads the committed copy `[deps] copy` names and keeps it
current with `schemadeps.SyncCopy`. The core ships none;
`Example_pinCommand` in `schemadeps/example_test.go` is one in miniature.

### 3.9 The CLI

`cli.New(cli.Config, ...registry.Extension)` returns the root cobra command
with `build`, `build-all`, `json-schema` and `format`, plus the commands of
every extension that implements `cli.CommandProvider`:

```go
type CommandProvider interface {
    Commands() []*cobra.Command
}
```

`build`, `build-all`, `json-schema` and `format` each resolve their
naming file first (`--naming`, or `superschematic.toml` at the schemas
root; the defaults for `json-schema`) and then assemble a fresh registry
with `registry.Assemble(naming, exts...)`. The registry therefore cannot exist
when `cli.New` runs, which is why subcommands hang off the extension value
rather than the registry. An extension command that needs a
registry assembles one the same way; acme's `describe` does.

`cli.Config` names the binary in usage text (`Name`) and can replace the
root descriptions (`Short`, `Long`). A binary is its `main` calling
`cli.New(...).Execute()`; `cmd/superschematic` passes no extension.

`json-schema` emits the data-form schema for the binary's registry
(section 5). `format` reads the file with the binary's registry, so a file
that uses an extension's kind, decorators, documents or tool invocation
policy key converts between JSON and YAML in a binary that links the
extension. The TypeScript writer cannot render an extension's slots or
documents and fails with the slot's name instead of dropping it.

### 3.10 Scalar catalog

The loader hydrates every scalar a schema references from the registry's
`ScalarCatalog`: one superscalar `ScalarMetadata` row per canonical name.
With no registration, `Scalars()` returns `CoreScalars()`, the catalog of
the superscalar Go package the core links (D3, D4).
A row's `Symbol`, `TypeScriptType`, `PythonType`, `RustType`, `SQLType` and
`JSONSchemaType` become the scalar's `go`, `typescript`, `python`, `rust`,
`sql` and `json_schema` type mappings, which the generators read. A
`RustType` that declares a type (`struct Location { ... }`) instead of
naming one is skipped, and rustgen maps that scalar itself.

`RegisterScalars(owner, catalog)` replaces that catalog. It exists for a
distribution that assembles its own scalar package over the generic set and
needs the loader to accept its extra names and the generators to emit their
symbols. One catalog per registry; a second registration is an error that
names both owners. `registry.ScalarCatalogOf` wraps a metadata map as a
catalog. acme does not register one; `internal/registry/scalars_test.go`
covers the seam.

### 3.11 Extension configuration

`superschematic.toml` carries the core's naming keys
(`docs/src/content/docs/reference/naming.md`) and one table per extension:

```toml
[extension.acme]
region = "eu"
```

The naming parser keeps every `[extension.<name>]` table undecoded and
rejects any other unknown top-level key. `Registry.ExtensionConfig(name)`
returns the table, or nil when the file has none. The extension owns the
keys and should reject the ones it does not know, so a typo fails the build;
acme's `decodeConfig` does. A table for an extension the binary does not
link is never read.

Three core keys exist for extensions: `auth_provider` selects a provider,
`authoring_packages` lists npm packages whose exports the TypeScript
frontend accepts, and `[package_aliases]` maps a distribution's
republished package names onto the core packages that declare the symbols.
A distribution also sets `metadata_key_prefix` (default `superschematic.`),
the namespace of every metadata key in the Arrow schemas the `sql`
generator writes for projection views, and `[deps] copy`, the committed
path of the dependency graph (section 3.8).

### 3.12 CheckSpec

```go
type CheckSpec struct {
    Name      string
    Extension string
    Kinds     []string
    Verify    func(schema *ir.Schema, r VerifyReporter)
}
```

A check is a verification rule over schemas of any kind, the core kinds
included. It is how an extension enforces its policy on what the core
decorators write, which `KindSpec.Verify` cannot do for a kind the
extension did not register. `Kinds` limits the kinds it runs on; nil means
every kind. Verify (`internal/loader/verify`) runs the core checks, then
the kind's own `KindSpec.Verify`, then every check for the kind in
registration order, once per load and in every frontend. An error the check
reports fails the load.

The core registers none. acme registers four (section 10): the `@icon` set,
the `@docs` audiences, `@mcp` on every `shop-api` operation, and a first
row rule on every projection view. `internal/registry/checks_test.go`
covers registration and ordering.

### 3.13 OpenAPIHook

```go
type OpenAPIHook struct {
    Name      string
    Extension string
    Edit      func(schema *ir.Schema, doc map[string]any) error
}
```

The `api` generator builds the OpenAPI document, then runs every hook in
registration order before it writes the file. A hook gets the API schema
and the document as decoded JSON (objects `map[string]any`, arrays `[]any`,
numbers `json.Number`, so no literal changes on the way back out) and edits
it in place. An error fails the generator and names the hook. With no hook
the document is written as built.

The core registers none. The core writes an operation's `@docs` record
under `registry.OpenAPIDocsKey` (`x-superschematic-docs`); acme's
`acmeDocsKey` moves it to `x-acme-docs`.

### 3.14 ToolHook

```go
type ToolHook struct {
    Name      string
    Extension string
    Edit      func(schema *ir.Schema, tools *ToolSet) error
}
```

A tool hook edits what the SDK generators publish about an API's MCP tools.
The `ToolSet` holds the vendor keys of the tool documents (`ToolKeys`: the
key a scalar argument names its scalar under, the `_meta` key of a visible
tool's `@docs` guidance, and extra keys written at the root of every
argument schema) and one `Tool` per operation, whose `MCP` field is a copy
of the resolved `@mcp` record the hook may edit. Hooks run in registration
order in the `api` generator, before the tool collision checks; the SDK
generators publish what they leave. An error fails the generator and names
the hook. With no hook the keys are `DefaultToolScalarKey`
(`x-superschematic-scalar`), `DefaultToolGuidanceKey` and no extra keys.
A hook may change a visible tool's invocation policy, but only to another
value of the registry's policy (section 3.15); the generator checks every
tool again after the hooks.

The core registers none. acme's `acmeTools` writes its own keys and fills
in each tool icon's family and style.

### 3.15 Tool invocation policy

A visible MCP tool's `@mcp` record carries an invocation policy: whether a
client runs the tool when a model calls it or asks the person first. The
policy is a core field whose key, values and default a distribution may
spell its own way, so a registry holds one `ToolInvocationPolicy`
(declared in `apigen`, aliased in `registry`):

| Field | Meaning |
| --- | --- |
| `Extension` | the registering extension; empty only for the core's |
| `Key` | the key inside `@mcp({...})`, in the data forms' `mcp` record, in the IR and in every tool document |
| `Values` | the allowed values, in the order `tools/index.ts` types them |
| `Default` | the value a visible tool gets when `@mcp` omits `Key` |

The core's is `invocationPolicy`, `auto` or `ask`, `auto` by default.
`RegisterToolInvocationPolicy` replaces it. A second registration is an
error that names both extensions, so two extensions that disagree fail
assembly. `Registry.ToolInvocationPolicy()` returns the one in force.

The policy is read in four places, all through the registry:

- The core `@mcp` decorator's `Apply` accepts `Key` with a value from
  `Values`, fills in `Default` for a visible tool, and names the build's
  key when an author uses the core key under another policy.
- The data-form JSON Schema adds `Key` to `OperationMCP` with `Values` as
  its enum (section 5), and the readers fill in `Default`.
- The `api` generator resolves every visible tool against it again, after
  the tool hooks, for IR that did not come through the loader.
- The SDK generators write `Key` at fixed positions and type it in
  `tools/index.ts` as the union of `Values`.

In the IR the policy is `OperationMCP.Invocation`, an `ir.MCPInvocation`
holding the key and the value, which `OperationMCP`'s JSON and YAML
encoders write after `hiddenReason`. The IR needs no registry to encode or
decode it; the loader holds it to the registry's policy.

The TypeScript half is `MCPToolOptions` in `@superschematic/api`: the
options a visible tool's `@mcp` takes besides `handle` and `_meta`. An
extension's authoring package adds its key by module augmentation. The
TypeScript frontend type-checks schema files, so a schema whose program
does not include the augmentation fails the load at the key.

## 4. The open IR

### 4.1 Slots

The IR has two open slots, both `map[string]json.RawMessage`:

- `Extensions` on `Schema`, `TypeDef`, `FieldDef` (which also holds
  operations) and `OperationSet`. The key is an extension name. The value
  is one JSON object whose keys are that extension's decorator names.
- `Documents` on `Schema`. The key is a `DocumentSpec.Name`.

Core decorators never write `Extensions`; they write typed fields. The
values are raw JSON rather than interfaces so the persisted IR decodes back
into the same Go types. The IR is its own module (D1), so an extension, a
runtime or a tool reads it without linking the compiler.

A field that carries acme's `@shelf({ aisle: 3, bay: "B" })` in the
TypeScript form has this next to its core keys in the IR, and the data forms
write the same object:

```json
"extensions": { "acme": { "shelf": { "aisle": 3, "bay": "B" } } }
```

### 4.2 Codecs

`ir/extensions.go` is the typed access layer:

| Function | Does |
| --- | --- |
| `GetExtension[T](node, ext)` | decode `Extensions[ext]` into `T`; `ok` is false when absent |
| `SetExtension[T](node, ext, v)` | encode `v` canonically and store it; a value that encodes to `{}` deletes the key |
| `UpdateExtension[T](node, ext, fn)` | read, let `fn` modify, store; what most `Apply` functions call |
| `GetDocument[T](schema, name)`, `SetDocument[T]` | the same over `Documents`; `SetDocument` keeps `{}`, because a document's presence runs its generator |
| `CanonicalJSON(raw)` | compact JSON, object keys sorted, arrays and number literals as written |

Every path that stores extension or document data runs `CanonicalJSON`: the
codecs, the data-form readers (`CanonicalizeExtensions`,
`CanonicalizeDocuments`) and the document loader. The persisted IR is the
same whether a value came from TypeScript, JSON or YAML.

An extension defines one Go struct per node type it writes (acme's
`fieldExt`) and adds a member per decorator. Adding a decorator is a new
member, a new `DecoratorSpec` and a new TypeScript export.

### 4.3 Persisted form

Both slots are `omitempty`, and `NewSchema` does not allocate them. A schema
that uses no extension decorator and carries no document marshals without
either key, so linking an extension does not change `--emit-ir` output or
generated code for schemas that do not use it. `ir/extensions_test.go`
pins the empty-map case.

A schema loaded by a binary that links an extension may carry that
extension's data. A binary without the extension rejects a data-form file
that carries it (section 5) and does not run generators for a document it
has no spec for (section 7.1).

## 5. JSON Schema composition for the data forms

The JSON and YAML readers validate each file against the schema-file JSON
Schema, which `schemafile.DefinitionFor(reg)` builds per registry:

1. Reflect the IR structs with `invopop/jsonschema`.
2. Set the `enum` of `Document.kind` to `reg.Kinds()`.
3. Close every `extensions` property (on the document root, `TypeDef`,
   `FieldDef` and `OperationSet`) to the registered extension names. On
   `TypeDef`, `FieldDef` and `OperationSet`, each extension's object is
   closed to its decorators for that node's targets, and each decorator's
   value is its `Args` schema, or `{"const": true}` when it takes no
   argument. No decorator targets the schema root, so there each
   extension's value is an open object the extension owns.
4. Close `documents` on the document to the registered document names, each
   with its `DocumentSpec.Schema`.
5. Add the registry's tool invocation policy key to `OperationMCP`, with
   its values as the enum (section 3.15).

With only the core registered, `extensions` and `documents` admit no key.
The compiled definition is cached per registry, keyed by a weak pointer and
the extension list. `superschematic json-schema` prints it for the binary's
registry; `--naming` selects the naming file, since the command has no
service directory.

After validation the readers check slot names again to give an error that
names the key and its location, then run every extension decorator through
the registry the way the TypeScript frontend does
(`internal/loader/schemafile/slots.go`): the decorator must belong to the
extension whose slot holds it, the kind must allow it, the argument is
validated against `Args`, and `Apply` runs. A decorator's `Apply` sees the
same input from every form.

`schema.config.json` and `schema.config.yaml` are validated against a
separate schema, generated from the `@superschematic/schema-config` types
and embedded in `internal/loader/schemaconfig`. Its `kind` admits any
string (`SchemaKindName`, section 6.3), and the loader then checks the kind
against the registry with an error that lists the registered kinds. Its
`outputs` object checks the core sections and admits any other key
(`SchemaOutputsDocument`): `ParseOutputs` rejects a key no registered
generator claims and validates each section against its generator's
`OutputSchema` (section 3.6).
`superschematic json-schema --config` prints the embedded schema as is.

## 6. The frontend

### 6.1 Authoring packages

The TypeScript frontend resolves every decorator and type wrapper to its
declaring package, through re-exports, and accepts it only from an
authoring package. `Registry.IsAuthoringPackage` is true for:

- every entry of `authoring_packages` in the naming file (the default is
  in the naming reference);
- every specifier `[package_aliases]` maps onto a declaring package;
- every package a registered decorator lists in `Packages`.

The third rule means an extension that registers a decorator from
`@acme/schema` makes that package an authoring package without a naming-file
entry. A decorator from any other package fails with
`decorator @x does not come from ...`.

Verify applies the kind's import rules to authoring imports: an import is
rejected when the kind's `ForbiddenPackages` lists the declaring package,
or when every decorator the package declares is restricted to other kinds
(`Registry.PackageAllowsKind`). The second rule keeps an extension's
authoring package out of the core kinds without the core naming it.

### 6.2 Dispatch

For each decorator on a node the walker calls `applyDecorator`
(`internal/loader/tsreader/walker.go`):

1. Look up `(name, target)`. A missing spec, or one whose `Packages` do not
   include the identifier's declaring package, is
   `decorator @x is not valid on <target>`. Two packages can therefore
   declare decorators with the same name.
2. Check `Kinds`. A decorator outside them fails with
   `@x is only allowed in A or B schemas (this service is kind C)`.
3. Resolve the spec's class type arguments, if it takes any (section
   3.4), to class names. Evaluate the arguments statically: literals,
   object and array literals, enum members, `const` variables with an
   initializer, `service({...})` sentinel calls and `as` expressions.
   Anything computed is an error.
4. Validate against `Args`, then call `Apply`.

The walker, not the registry, decides the shape of a class: a table, an
embedded struct, a projection, an operation set or a trait. `KindSpec`
supplies the roles and whether operation sets are allowed. After the walk,
verify runs the core checks, the kind's `KindSpec.Verify` and the
registered checks for the kind (section 3.12), the same way for all three
forms.

### 6.3 Schema config kinds

In `@superschematic/schema-config`, a config's `kind` has the type
`SchemaKindName`: a member of the closed `SchemaKind` enum (the three core
kinds) or any other string (`ExtensionKind`). An extension kind is written
as a string literal:

```ts
export default defineConfig({
  name: "shop-catalog",
  kind: "Catalog",
  outputs: {
    // @ts-expect-error catalog is the acme generator's output key
    catalog: { enabled: true }
  }
});
```

The frontend reads the config statically and treats `kind` as a plain
string; `ValidateShapeWith` checks it against the registry. The static
evaluator reads through `as` expressions, so a config that casts an
extension kind (`kind: "Catalog" as SchemaKind`) also loads.

The `outputs` type in the same package lists only the core keys, so an
extension's output key needs the `@ts-expect-error` shown above; the
registry, not `tsc`, decides which keys are valid (section 7.2). The data
forms need no such escape: `schema.config.json` and `schema.config.yaml`
carry the key as it is (section 5).

`build` decides before loading whether a target's kind sets
`ImportsSiblingSentinels`. For a TypeScript config it scans the file for
`SchemaKind.<Kind>` or the quoted kind name, so it does not need a compiler
program.

## 7. Dispatch

### 7.1 Run

`generator.Run(schema, cfg, opts)`:

1. Parse the config's `outputs` block against the registry (section 7.2).
2. Resolve `schema.Kind` in the registry; an unknown kind is an error.
3. Take `reg.Pipeline(kind)`: the kind's `Pipeline` in order, then every
   other generator whose `Kinds` lists the kind, in registration order.
4. Call each generator's `Enabled`. Collect the `Dirs` of the enabled ones
   and fail, before any runs, if two claim the same directory.
5. Run the enabled generators in order.
6. Run the document generators: for every registered `DocumentSpec` with a
   `Generate` whose name is present in `Schema.Documents`, in name order.
   They run whatever the kind's pipeline and the `outputs` switches say. A
   document with no registered spec is left alone.
7. Sort `Result.Skipped` and return.

`registry.Generate` is the public entry to `Run`, for extension tests.

### 7.2 Outputs

`ParseOutputs(raw, reg)` accepts only keys that some generator claims as
its `OutputKey`. The error lists the core keys in registration order
(`types`, `sql`, `api`, `sdk`), then the extension keys sorted. It
validates each section against the `OutputSchema` of the generator that
claims its key, decodes the core sections into typed fields, checks their
target languages, rejects an unknown key in `outputs.sql`, fills the API
defaults, and keeps every section raw in `Outputs.Raw`. An extension
generator reads its own section with `registry.DecodeOutput(outputs, key,
&v)`, usually in `Enabled`; the section has already passed its
`OutputSchema`.

### 7.3 build-all

`build-all` discovers the services under the services root with the
command's registry (`buildplan.DiscoverWith`), so an extension kind in a
sibling config is known at discovery. Each service's expected output
directories come from the `Dirs` of its present documents and of the
enabled generators in its pipeline; the `--cache` layer stores and restores
those. Services build in dependency order, each with the discovered schema
set as the document loaders' `Catalog`. The dependency graph is written
next, and its `[deps] copy` when the naming file sets one. The hooks run
last, whether or not any service was built (section 3.8).

## 8. Auth providers

### 8.1 Interface

```go
type AuthProvider interface {
    Name() string
    Analyze(api, upstream *ir.Schema) (AuthModel, error)
    Endpoint(op *ir.FieldDef, set *ir.OperationSet, ep *EndpointInfo) error
    Templates() embed.FS
    Funcs() template.FuncMap
    Files(output *APIOutput) []codegen.ConditionalFile
    OpenAPIParameters(output *APIOutput) []map[string]any
}
```

The `api` generator takes the provider `auth_provider` selects
(`Registry.SelectedAuthProvider`) and:

- calls `Analyze` once with the API schema and the upstream DB schema its
  config's `authDb` names (nil when the API is not public). `AuthModel`
  reports whether the upstream can back the session store
  (`Session(id, jti, user, expiresAt)`) and the principal store
  (`User(id, name)`); `Extra` holds the provider's own findings.
  `registry.AnalyzeSessionStores` is the core half and `registry.HasTable`
  the probe;
- calls `Endpoint` per operation. The core has already set `RequiresAuth`
  and the required permissions; the provider sets `IsScopedEndpoint` and
  `ScopeParamName` (the SDK generators read them) and its own `Auth` data;
- renders the core templates, which call the provider at fixed hook points
  through `authSnippet`. `apigen.AuthSnippets` lists the eighteen snippets
  (imports, context shims, store adapters, config fields, route setup, the
  per-route permission middleware, `go.mod` lines). A provider defines every
  one, empty when it adds nothing. The generator checks the set when it
  parses the templates, before it writes a file, and
  `registry.AuthSnippetFunc(provider)` runs the same check in a provider's
  own test;
- renders the provider's whole files (`Files`) next to the core files and
  adds `OpenAPIParameters` to every operation of the OpenAPI document.

The snippet hooks are the only way a provider changes a core template.

### 8.2 The core session provider

`internal/generator/apigen/sessionauth` registers as `session`, the default
`auth_provider`. It models bearer sessions over the upstream `Session`
table, an optional principal from the `User` table, and plain-string
permissions. It has no tenancy or organization scope: no endpoint is scoped
and no scope parameter is hoisted.

Its generated code depends only on the generic runtime:
`runtime/http/go/session` (the context helpers, `RequireAuth`,
`RequirePermissions`, `RequirePermissionsWith`, `Guard`, the `Store`,
`PrincipalStore` and `RoleStore` interfaces) and the other provider-neutral
runtime packages (D6). Permissions are dotted paths; a granted permission
covers a required one when they are equal or the required one is nested
under it, and there is no root permission.
`TestSessionProviderAPIDependsOnGenericRuntimeOnly` in
`internal/generator/apigen/compile_test.go` generates a fixture API, builds
and vets it, and fails if its dependency closure lacks the generic
`session` package, reaches a runtime package D6 left out, or its source
names a deployment-specific auth identifier.

A deployment with its own identity model, tenancy or permission vocabulary
does not add it here. It registers its own provider and ships the runtime
packages that provider's snippets import, pulling them in through the
`moduleRequires` and `moduleReplaces` snippets.

### 8.3 Writing a provider

A provider is an ordinary Go type in the extension module, registered in
`Register`. It cannot import `sessionauth`, which is internal, so it
reimplements the snippets it needs; `registry.AnalyzeSessionStores` and
`registry.HasTable` cover the model probe. acme's `apikey`
(`examples/acme-schematic/ext/auth`) is the worked example: it reads an
`X-API-Key` header, resolves it to a principal through a key store, puts
the principal on the context with the session runtime's helpers, and so
reuses `RequireAuth` and `RequirePermissions` unchanged. The key store
adapter is generated only when the upstream DB has an `ApiKey(id, secret,
user)` table, so the generated module always compiles against the ORM it
is given.

### 8.4 The TypeScript server

An API schema with `outputs.api.language` set to `TYPESCRIPT` gets a Hono
router package instead of the Go module. It reads the same `APIOutput`, so
the provider's `Endpoint` hook and the registered OpenAPI and tool hooks
have run, but it renders no auth snippet: its templates have no hook
points. Each route's requirement goes into the generated operation table
(`@publicRoute`, an authenticated caller, the `@requirePermission` list),
and `@superschematic/http-runtime` applies it at request time with what the
service passes to `buildRouter`: an `Authenticator` that establishes the
caller, and optionally a `PermissionMatcher` that replaces the default
dotted-path coverage rule. A deployment's identity model, token format and
service-to-service verification live in its own TypeScript package next to
its provider, which supplies those two functions. D12 in
`docs/DECISIONS.md` records the split.

## 9. What the core registers

| Surface | Core registration |
| --- | --- |
| Kinds | `DB`, `API`, `General` (section 3.3) |
| Decorators | 41 specs over the four targets in `internal/registry/core_decorators.go`, `core_projection.go` and `docs_decorators.go`, declared in `@superschematic/{schema,db,api,schema-config}` |
| Generators | `types`, `sql`, `orm`, `api`, `sdks`, `envConfig` (section 3.6) |
| Auth providers | `session` (section 8.2) |
| Scalar catalog | the superscalar Go package (section 3.10) |
| Tool invocation policy | `invocationPolicy`: `auto` or `ask`, `auto` by default (section 3.15) |
| Documents | none |
| Build-all hooks | none |
| Checks, OpenAPI hooks, tool hooks | none |
| Commands | `build`, `build-all`, `json-schema`, `format` |

The core stays provider-neutral (`CONTRIBUTING.md`, "The core stays
provider-neutral"). A surface that only one deployment needs belongs in an
extension. D10 in `docs/DECISIONS.md` applies that rule to features that
have a generic mechanism and distribution-specific names or policy: names
go in the naming file, rules in the extension.

## 10. Worked example and acceptance

`examples/acme-schematic` is the acceptance test of this model: a separate
Go module (`example.com/acme/schematic`, with a `replace` onto this
checkout) that imports only `registry`, `cli` and `ir` and adds one of each
surface:

| Surface | acme | File |
| --- | --- | --- |
| Kind | `Catalog`, pipeline `types`, `catalog` | `ext/kind.go` |
| Decorator | `@shelf` from `@acme/schema`, on Catalog fields | `ext/decorator.go`, `packages/schema` |
| Document | `catalog.config.yaml` on Catalog services, with a generator | `ext/document.go` |
| Generator on core kinds | `acmeManifest`, appended to DB, API, General and Catalog | `ext/manifest.go` |
| Build-all hook | `acmeInventory`, every service's manifest merged into one file | `ext/inventory.go` |
| Auth provider | `apikey` | `ext/auth/` |
| Checks and an OpenAPI hook | `acmeIcons` and `acmeDocsAudience` over the core `@icon` and `@docs`; `acmeDocsKey` moves the `@docs` record to `x-acme-docs` | `ext/docs.go` |
| Check and a tool hook | `acmeToolsClassified` requires `@mcp` on every `shop-api` operation; `acmeTools` writes acme's tool keys and icon variant | `ext/mcp.go` |
| Check on a core kind | `acmeProjectionScope`: every projection view in a DB schema binds the scope setting first | `ext/projection_policy.go` |
| Command | `describe` and `fields`, through `cli.CommandProvider` | `ext/command.go`, `ext/fields.go` |
| Configuration | `[extension.acme] region` and `projection_scope_setting`; `metadata_key_prefix` and `[deps] copy` | `ext/extension.go`, `schemas/superschematic.toml` |
| Tool invocation policy | `confirm`: `never` or `always`, `never` by default, with its `MCPToolOptions` augmentation | `ext/mcp.go`, `packages/schema/src/mcp.ts` |
| Binary | `cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})` | `cmd/acme-schematic` |

It does not register a scalar catalog; the test in section 3.10 covers
that.

The acceptance criterion: an extension adds every surface above without
editing a file outside its own module, and adding one more decorator stays
that way. Two scripts check it, and the `acme` job in
`.github/workflows/ci.yml` runs both:

- `scripts/smoke.sh` builds the core binary and the acme binary, runs the
  module's tests, runs `build-all` over the example schemas, and asserts
  each surface did its work: `describe` lists the kind, document,
  provider and checks; `catalog.json`, the document's output and a
  manifest per service exist, and the inventory hook merges them again
  when every service is restored from the cache; the `@shelf` payload and
  the scoped projection view are in the IR, and the view, its migration and
  its Arrow schema are written under acme's metadata key prefix; the
  generated API compiles against `apikey`; the committed graph copy is
  current; `fields` type-checks the label declarations; the `@docs`
  records reach the OpenAPI document under `x-acme-docs` and the tool
  documents carry acme's keys. It also asserts that the core-only binary
  rejects the Catalog service and the naming file that selects `apikey`,
  that it builds the DB and API services with `session` and the result
  compiles, and that it writes the core keys where acme's hooks write its
  own.
- `scripts/check_second_decorator.sh` applies
  `scripts/second_decorator.patch` (a `@perishable` decorator), reruns the
  smoke, checks the new payload reached the IR, and fails if any path
  outside `examples/acme-schematic/` changed.

Inside the core module, three extensions test the seams against the real
loader and generators:

- `internal/registry/registrytest` is an in-tree fixture extension with a
  kind, decorators on all four targets, a data-form document and a
  generator. `TestAcmeExtensionEndToEnd` loads a TypeScript fixture and its
  data-form twin, checks both produce the same IR with the decorator values
  in their slots, and runs the generator.
- `extensions/deploy` registers a document and nothing else. Its test loads
  `testdata/services/example` and compares the generated values files byte
  for byte. See the [deploy guide](https://parable-work.github.io/superschematic/guides/deploy/).
- `extensions/platform` registers a kind with `NoSentinel`, a `Verify` rule,
  a type decorator and a generator. Its tests load a TypeScript fixture, a
  YAML twin and a fixture that must fail verify. See the
  [platform guide](https://parable-work.github.io/superschematic/guides/platform/).

## 11. Known gaps

These are places where the code does less than the model implies. Each is
a candidate for a change with its own test.

1. Closed. `GeneratorSpec.OutputSchema` was not read, so an extension
   generator had to validate its own section. It is compiled at
   registration, and `ParseOutputs` validates each claimed section against
   it before any generator runs (section 3.6).
2. Closed. A data-form config (`schema.config.json` or `.yaml`) could not
   carry an extension output key, because the embedded config schema
   closed `outputs` to the core keys. The config schema now checks the core
   sections and admits any other key; `ParseOutputs` rejects a key no
   generator claims and validates the section against its `OutputSchema`
   (section 5). The YAML twin of the in-tree fixture extension
   (`internal/registry/registrytest/testdata/shop-yaml`) switches its
   generator on this way.
3. Closed. `Finalize` did not check that the `Kinds` of a generator,
   decorator, document or check name registered kinds, so a spec that
   listed an unregistered kind was inert for it. `Finalize` now fails
   (section 3.2).
4. Closed. An extension could not attach a `Verify` rule to a kind it did
   not register, so a policy check over core-kind schemas had no seam.
   `RegisterCheck` (section 3.12) is that seam.
5. Closed. `format` read schema files with the core registry only, so a
   file that used an extension kind, decorator, document or tool
   invocation policy key did not convert. It reads with the binary's
   registry (section 3.9).
6. The TypeScript writer cannot render an extension's decorators or
   documents. `format --to=ts` fails on a file with extension data and
   names the slot; JSON and YAML carry it.
7. The config schema is not composed per registry the way the schema-file
   schema is (section 5). `json-schema --config` admits any extension
   output key and does not carry its `OutputSchema`, so an editor that
   validates against it does not catch a misspelt extension key or a bad
   section. The build does.
8. D10 puts a distribution's names in the naming file, but the prefix of
   the core's vendor-extension keys has no naming key. A distribution
   renames the OpenAPI and tool document keys (`x-superschematic-docs`,
   `x-superschematic-scalar` and the tool `_meta` keys) with
   `RegisterOpenAPIHook` and `RegisterToolHook` (sections 3.13 and 3.14);
   the `x-superschematic` key of `values-schema.json` has no seam.

## 12. References

| Path | What it holds |
| --- | --- |
| `registry/registry.go`, `registry/generate.go` | the public registry package |
| `internal/registry/registry.go` | `Registry`, `Use`, `Finalize`, the `Register*` methods |
| `internal/registry/spec.go` | `KindSpec`, `DecoratorSpec`, `DocumentSpec`, `GeneratorSpec`, the contexts, `BuildAllHook` |
| `internal/registry/core.go`, `core_decorators.go`, `core_projection.go`, `docs_decorators.go` | the core kinds and decorators |
| `internal/registry/scalars.go`, `outputs.go` | the scalar catalog seam, `ParseOutputs` |
| `internal/generator/core.go`, `generator.go` | the core generators, `Run` |
| `internal/generator/apigen/auth.go`, `apigen/sessionauth` | the provider interface, the snippet hooks, the core provider |
| `internal/generator/apigen/openapi_hooks.go`, `apigen/tools.go` | `OpenAPIHook`, `ToolHook` and where the `api` generator runs them |
| `internal/loader/documents.go` | document loading |
| `internal/loader/tsreader/walker.go`, `evaluate.go` | decorator origin, dispatch, static evaluation |
| `internal/loader/schemafile/schema.go`, `slots.go` | the data-form JSON Schema composition and slot checks |
| `internal/loader/verify/` | import rules and `KindSpec.Verify` |
| `ir/extensions.go` | the codecs |
| `ir/mcp_invocation.go`, `internal/generator/apigen/tool_invocation.go` | the IR's invocation policy and its encoding, `ToolInvocationPolicy` |
| `cli/cli.go` | `cli.New`, `CommandProvider` |
| `loader/loader.go` | the public loader package |
| `examples/acme-schematic/` | the worked example and its acceptance scripts |
| `docs/DECISIONS.md` | the decisions this design rests on (D1, D2, D3, D4, D6, D10, D11) |
