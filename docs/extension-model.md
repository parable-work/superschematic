# Extension model

This is the design of the extension seam as it is built in this repository:
what an extension can register, how a binary assembles the registry, where
extension data lives in the IR, how the loader and the generators consult
the registry, and which tests prove that an extension needs no core edit.

Two pages walk the same surfaces with code, and this document does not
repeat them:

- The [write an extension](https://parable-work.github.io/superschematic/extending/write-an-extension/)
  guide (source: `docs/src/content/docs/extending/write-an-extension.md`).
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
| Behavior | `RegisterBehavior(BehaviorSpec)` | 3.16 |

`Name()` is the key of everything the extension owns: the `Extension` field
of each spec it registers, the `extensions.<name>` slot on IR nodes
(section 4), the `[extension.<name>]` table of the naming file and the
prefix of its behaviors' names (section 3.16). It may not contain a dot.

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
   `internal/cmd/superschematic-core` is `cli.New(cli.Config{})`. The
   installed binary, `cmd/superschematic`, is the same call with the
   official extensions (`docs/stack-model.md`, section 13).
3. Extension data round-trips through the TypeScript, JSON and YAML
   authoring forms and the persisted IR, and the core never knows its shape.
4. Mistakes fail closed and early. A bad registration fails when the
   registry is assembled, before any schema loads. An unknown kind,
   decorator, `extensions` key, `documents` key or behavior fails the load
   and names it.

Limits:

- Extensions are Go packages compiled into the binary. Loading one at run
  time is out of scope.
- The extension API (`registry`, `loader`, `cli`, `schemadeps`,
  `generator.Naming`) is not frozen before the first release; see the
  README, "Status".

## 3. The Extension interface and the Registry

`registry.Registry` holds every kind, decorator, document, generator, auth
provider, build-all hook, check, OpenAPI hook, tool hook, behavior and the
scalar catalog a run knows about. It is
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
| `registry` | Aliases and forwarding functions over `internal/registry`, plus the helpers an extension calls: `Assemble`, `DecodeArgs`, `ArgErrorf`, `ParseOutputs`, `DecodeOutput`, `Generate`, `EnvConfigOf`, `RustAPIOf` and `APIDir` (the Rust API crate the `api` generator writes, as `RustAPI` with its `RustEndpoint`, `RustParam` and `RustInput` records, and where), `HasTable`, `AnalyzeSessionStores`, `AuthSnippetFunc`, `CoreScalars`, `ScalarCatalogOf`, `ScalarCatalogWithUploads`, `ScalarCatalogWithRawBodyChecks`, `DefaultNaming`, `LoadNaming`, `ParseNaming`, `GoPublicIdentifier`. The hook types (`BuildAllService`, `CheckSpec`, `VerifyReporter`, `OpenAPIHook`, `ToolHook`, `ToolSet`, `Tool`, `ToolKeys`, `ToolKeyValue`), the behavior types (`BehaviorSpec`, `BehaviorDeclaration`, `BehaviorField`, `BehaviorOperation`, `Behavior`), the default vendor keys (`OpenAPIDocsKey`, `DefaultToolScalarKey`, `DefaultToolGuidanceKey`) and the stack model's specs (`PlatformSpec`, `ConnectorSpec`, `TargetSpec`, `DNSPlatformSpec`, `ProvisionerSpec`, the `Provisioner` interface and their contexts) are aliased here too |
| `loader` | `LoadService` and `LoadServiceWithConfig` with `WithRegistry`, `WithNaming` and `WithSchemaCatalog`, for extension tests against real fixtures; `NewDeclarationProgram`, a type-checked TypeScript program over in-memory files with the loader's compiler, lib files and module resolution, for an extension that checks declarations the schema frontend does not walk; `SchemaError` and `SchemaErrorList`, its located diagnostics |
| `cli` | `cli.New`, `cli.Config`, `cli.CommandProvider` |
| `ir` | The IR, its own Go module, with the extension codecs (section 4.2) |
| `stack` | The stack model's resolver (`docs/stack-model.md`, section 6.10): `Resolve` over a stack, the facts of its services (`Service`, `Config`, `ConfigField`) and one environment, its failures (`Errors`, with a `Code` per check), and `Marshal`, `Unmarshal`, `Write` and `EnvironmentPath` for `environment.json`. An extension's tests resolve a stack over its platforms with it |
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
`json-schema`, `format` and `behaviors` call it once per invocation.
`generator.CoreRegistry(naming)` runs them with no extension and panics on
error. `generator.Run` falls back to a core-only registry when
`Options.Registry` is nil and returns an error when the naming selects an
auth provider the core does not have.

`Use` is fail-closed. An extension with an empty name or a name that
contains a dot, or a `Register` that returns an error, stops `Use`; the
error is recorded and `Finalize` returns it, so a half-registered extension
cannot be used.

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
| `RegisterPlatform`, `RegisterConnector`, `RegisterTarget`, `RegisterDNSPlatform`, `RegisterProvisioner` | the stack model's specs; `docs/stack-model.md`, section 6.7, lists what each refuses |
| `RegisterBehavior` | a declaration that does not decode or has an unknown key, a malformed name or one that does not belong to the registering extension, a duplicate name, a schema that does not compile, a params or precondition schema that is not an object schema or does not set `"additionalProperties": false`, a create params schema that is not an object schema or whose `additionalProperties` is neither `false` nor a schema, an operation or field name that is malformed or repeats, an operation named like one every schema has, a veto code that is not lowercase snake case or repeats (section 3.16) |

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
- Every behavior's `requires` and `conflicts` name registered behaviors,
  other than itself and not the same one in both, and each of its
  operations' `invocationPolicy` is a value of the policy in force
  (section 3.15), whichever extension registered it.
- Every connector joins registered platforms of the kinds its edge joins.
  Every platform a target names is registered and of the right kind, and
  so is any DNS platform or provisioner it names (`docs/stack-model.md`,
  section 6.7).

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
| `Stack` | `EmbeddedStruct` | not allowed | no | `stack` |

DB may not import `@superschematic/api`, API may not import
`@superschematic/db`, General and Stack may import neither. DB may
reference General types, API may reference DB and General types, General
may not reference DB or API types, and Stack references no other service's
types. A Stack service names the services it deploys by their sentinels,
so it sets `ImportsSiblingSentinels`, and nothing names it, so it sets
`NoSentinel` (`docs/stack-model.md`, section 4.1).

`ir.SchemaKind` is a named string with constants for the four core kinds.
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
frontends have validated them against `Args`. Only the `null` literal
evaluates to nil: `[]` and `{}` are an empty `[]any` and `map[string]any`
from the TypeScript form, as the data forms decode them, so an empty list
reaches `Args` and the IR as `[]`, not `null`. `registry.DecodeArgs` decodes
the argument into a Go struct. An `ArgError` (`registry.ArgErrorf`) points
the TypeScript diagnostic at one argument instead of the decorator.

An argument can name a class as a value, in any decorator, core or
extension: `@pairsWith(Accessory)`, `{ of: Backend }`. The TypeScript
frontend resolves the identifier, through any import alias, to the class it
declares and evaluates it to the class reference `{"class": "Accessory"}`
(`ir.ClassRef`), which holds the declared name, unqualified. The data forms
write the same object, so `Apply` sees the same value from every form and
the slot stores it as written. A class declared in another service's
package is recorded in the schema's `Imports` under that package, as a
field type from it is, so the kind's import rules apply and `schema.config`
must declare the dependency. Three registry names serve an extension:

| Name | Use |
| --- | --- |
| `registry.ClassRefSchema` | the JSON Schema of a class reference, for the place in `Args` that takes a class; a name in a string there fails in both forms |
| `registry.ClassRef` | the Go type, a member of the struct `DecodeArgs` fills or of a codec struct |
| `registry.DecodeClassRef(v)` | reads one class reference, such as a whole argument, into the class's name |

The shape is reserved. In a decorator's value under an `extensions` slot,
an object whose only key is `class`, holding a string, is a class reference
wherever it sits. After the schema is assembled, verify fails the load,
in every form, when the schema neither declares nor imports a type of that
name: `type "Product": @pairsWith names class "Missing", which this schema
neither declares nor imports`. That check is what holds the data forms to a
real class. The TypeScript form has the compiler too, and it refuses an
object literal of that shape: a schema names the class itself. A core
decorator that keeps a class in a typed IR field checks it with a rule of
its own, as `@graphMember`'s `graph` is checked.

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
node's `Extensions[<extension name>]` slot (section 4). A directive that
only one distribution reads is an extension decorator, not a typed field:
D18 in `docs/DECISIONS.md` removed twelve such fields from the core IR on
that rule. A spec with no `Args` takes no argument, as acme's `@feedKey`
does, and the data forms write its value as `true`. A spec with a nil
`Apply` is a marker the frontend interprets itself; only the core registers
those (`trait`, `source`, `envVars`, `versioned`, `versionGraph`,
`graphMember`), and `RegisterDecorator` refuses an extension decorator
without `Apply`. `source` and `graphMember` name classes (`@source(Product)`,
`graph: Recipe`), but each would lose a diagnostic or change the IR as an
`Apply`, so they stay markers. `@source` projects the class's flattened
fields, which `Apply` cannot reach, and records no import for a
cross-service target. `versionGraph`, which names no class, and
`graphMember` are read before the type's fields resolve, as `versioned` is,
so their errors are reported when a field fails; `graphMember` also reports
each bad property at that property's value.
`internal/registry/core_decorators.go` gives the reasons beside each
registration.

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
| `RendersBehaviors` | the generator's output covers what a type's behaviors add; without it, `Run` refuses a schema whose types compose behaviors (section 3.16) |

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
| `sql` | `sql` | Postgres DDL, projection views with their migrations and Arrow schemas, and SQLite DDL when `outputs.sql.dialects` lists `sqlite`; implied by the DB kind, and `outputs.sql` places the view migrations (`migrationsDir`), sets their role (`viewOwner`) and lists the dialects (`dialects`) |
| `orm` | none | the Go ORM; implied by the DB kind |
| `api` | `api` | the Go chi server, the Rust axum crate or the TypeScript Hono package (`outputs.api.language`), and OpenAPI |
| `sdks` | `sdk` | TypeScript, Go, Python and Rust clients, one switch per language |
| `envConfig` | none | the environment loader for a schema with an `@envVars` class |
| `stack` | none | each environment of a Stack schema's stack, resolved, as `environment.json` (`docs/stack-model.md`, section 4.1). It loads the services the stack reaches with `LoadDependency`, and reads their outputs from the configs `Options.LoadDependencyConfig` supplies, which every build sets |

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
with `build`, `build-all`, `json-schema`, `format` and `behaviors`, plus the
commands of every extension that implements `cli.CommandProvider`:

```go
type CommandProvider interface {
    Commands() []*cobra.Command
}
```

`build`, `build-all`, `json-schema`, `format` and `behaviors` each resolve
their naming file first (`--naming`, or `superschematic.toml` at the schemas
root; the defaults for `json-schema` and `behaviors`) and then assemble a
fresh registry with `registry.Assemble(naming, exts...)`. The registry
therefore cannot exist when `cli.New` runs, which is why subcommands hang
off the extension value rather than the registry. An extension command that needs a
registry assembles one the same way; acme's `describe` does.

`cli.Config` names the binary in usage text (`Name`) and can replace the
root descriptions (`Short`, `Long`). A binary is its `main` calling
`cli.New(...).Execute()`. The installed `cmd/superschematic` passes the
official extensions, and the core-only `internal/cmd/superschematic-core`
passes none.

`cli.Config.ToolDigest` replaces the hash of the running executable in
every `build-all` cache key and stamp (section 7.4). `cli.New` applies it
for the whole process through `buildcache.SetToolDigest`; empty keeps the
executable hash. It is a config field rather than an `-ldflags -X` target
because a renamed field fails to compile, while an `-X` flag naming a
renamed variable is silently ignored.

`json-schema` emits the data-form schema for the binary's registry
(section 5). `format` reads the file with the binary's registry, so a file
that uses an extension's kind, decorators, documents or tool invocation
policy key converts between JSON and YAML in a binary that links the
extension. The TypeScript writer cannot render an extension's slots or
documents and fails with the slot's name instead of dropping it.
`behaviors` writes the declaration of each behavior the binary registers
into the npm package that implements it (section 3.16).

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
catalog.

A `ScalarMetadata` row has no upload fields, and superscalar's core set has
no upload scalar. A catalog that implements `UploadCatalog` declares its
file-upload scalars beside the rows: `Upload(canonical)` returns a
`ScalarUpload`, the `ir.FileUploadConfig` (size limit, allowed MIME types,
category) and optional `ir.ImageConstraints`. `registry.ScalarCatalogWithUploads`
wraps a catalog with a map of them and rejects a name the catalog does not
define. The loader copies the metadata onto the scalar's `FileUpload` and
`ImageConstraints` when it hydrates, so a field of the scalar is a
multipart file part in the generated APIs and SDKs. The loader checks
`Validate<T, { uploadMaxBytes }>` after hydration, in every form
(`ir.Schema.ValidateHydrated`): the bound must be positive and the field a
single scalar whose hydrated def carries `FileUpload`. The frontends'
own validation (`ir.Schema.Validate`) runs before hydration and leaves the
bound alone. acme registers a catalog with one upload scalar, `Acme.Photo`
(section 10); `internal/registry/scalars_test.go` and
`internal/loader/upload_max_bytes_test.go` cover the seam.

A catalog that implements `RawBodyCheckCatalog` names a raw-body check for
some of its scalars: `RawBodyCheck(canonical)` returns a
`ScalarRawBodyCheck`, a Go function that a generated route calls on the
raw JSON of its request body before decoding it. A distribution uses one
when decoding a value of the scalar loses something a write must refuse,
such as a key the decoded form has no place for.
`registry.ScalarCatalogWithRawBodyChecks` wraps a catalog with a map of
checks and composes with `ScalarCatalogWithUploads` in either order.

- The check names `ImportPath`, the `PackageName` routes.go imports it
  under, and `Func`, whose signature is
  `func(body []byte, fields ...string) E` with `E` the scalar package's
  `ValidationErrors`. The function reports each error at its path under
  the field it names.
- `ErrorsVar` (default `checkErrors`) names the result variable, and
  `Comment` writes line comments above the call. Both exist so a
  distribution can keep the output of a generator it ported byte for byte.
- For an input type with single-valued top-level fields of the scalar, the
  Go route calls
  `if <ErrorsVar> := <PackageName>.<Func>(body, "<field>", ...); <ErrorsVar>.HasErrors()`
  and answers 400 with the result. It does so in each place it decodes the
  input from JSON: the body of a route without file uploads, and the JSON
  body or the multipart `data` part of a route with them. The call follows
  the refusal of a null body and precedes `json.Unmarshal`. The comment is
  written in the two JSON-body paths only.
- Scalars that share a check share one call. A list or a map of the scalar
  is not named. A scalar without a check gets no call and no import, so the
  output for a catalog without checks is unchanged.
- The wrapper rejects a name the catalog does not define and a check whose
  call would not compile (`apigen.RawBodyCheck.Validate`). apigen also
  rejects two checks that import different packages under one name.
- Only the Go API generator renders the calls; the Rust and TypeScript
  servers do not.
- An auth provider whose `routesImports` snippet needs a check's package
  asks `APIOutput.ImportsRawBodyCheckPackage` and leaves its own import
  out.

D24 records the decision. `internal/generator/apigen/raw_body_check_test.go`
covers the seam, including a generated router that refuses a body before
decoding it.

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
`build-all` and `build --with-deps` accept a `schema.config.ts` that imports
the config package under any name the table maps onto
`@superschematic/schema-config`; acme's configs import
`@acme/schema-config`.
A distribution also sets `metadata_key_prefix` (default `superschematic.`),
the namespace of every metadata key in the Arrow schemas the `sql`
generator writes for projection views; `scalar_jsdoc_tag` (default unset),
the JSDoc tag the TypeScript types write above every scalar-typed field
(`/** @<tag> Contact.Email */`) for a tool that reads the declaration
files; `history_actor_setting` (default
`superschematic.history_actor_id`), the Postgres setting a versioned
table's history trigger reads a delete's actor from; and `[deps] copy`,
the committed path of the dependency graph (section 3.8). The first three
are names in the output, so they are naming keys rather than registrations
(D10, D13).

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
  its enum and `Default` as its default (section 5), and the readers fill
  in `Default`.
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

### 3.16 BehaviorSpec

A behavior is code that adds fields, operations, checks and storage to a
type when an engine runs the schema: a state machine, dependency edges
between instances, a rating. A type composes several. D16 in
`docs/DECISIONS.md` is the design; this section is what the compiler does
with them. It declares, carries and checks behaviors; it runs none.

```go
type BehaviorSpec struct {
    Extension   string
    Package     string
    Declaration json.RawMessage
}
```

`Package` is optional: the npm package whose implementation runs the
behavior in an engine, which the `behaviors` command's `--package` keeps
(below). When set it must be an npm package name.

A behavior is declared once, in a JSON file beside the Go package that
registers it, which embeds it and passes it to `RegisterBehavior`. Its
shape is `BehaviorDeclaration`:

| Key | Meaning |
| --- | --- |
| `name` | bare (`Workflow`) for a core behavior, `<extension>.<Name>` for an extension's |
| `description` | what the behavior adds |
| `configSchema` | the JSON Schema of the config a type gives the behavior; absent, the behavior takes none |
| `createParamsSchema` | the JSON Schema of the parameters a create gives the behavior for the new instance, which an engine passes to its `initialize`: an object schema whose `additionalProperties` is `false` or a schema; absent, a create gives it none |
| `requires`, `conflicts` | behaviors a type that lists this one must also list, or may not |
| `fields` | the fields it adds: `name` and `description` |
| `operations` | the operations it adds: `name` (camelCase), `description`, `paramsSchema` (an object schema with `"additionalProperties": false`), `resultSchema`, `writes`, `scope` (`instance`, the default, or `schema`), and `invocationPolicy`, a value of the registry's policy (section 3.15) or absent for its default |
| `preconditionSchema` | the JSON Schema of the entry a caller sends for the behavior in the preconditions of an update, a delete or an operation, which an engine checks and hands to the behavior's guard: an object schema with `"additionalProperties": false`; absent, the behavior takes none |
| `vetoes` | the codes its refusals carry, each `code` (lowercase snake case, at most 64 characters) and `description`; an engine refuses a veto whose code is not listed |

An operation's `scope` says what a call names: `instance`, one instance
by id, or `schema`, the schema as a whole with no instance, which an
engine serves at `POST /namespaces/{ns}/schemas/{name}/operations/{op}`
and as a tool that takes its parameters and no `id` (D16, amended).
`RegisterBehavior` refuses any other value, and the engine does too.

A field carries only a name and a description. A field's type can depend
on the behavior's config, and the loader needs only the name to refuse a
collision.

`RegisterBehavior` rejects what section 3.2 lists. The name after the dot
follows the bare-name rule (`^[A-Z][A-Za-z0-9]*$`), and the prefix is the
registering extension's `Name()`; inside `Use` the spec's `Extension` must
be the extension whose `Register` is running, so an extension cannot
declare a core name. An operation may not be named `create`, `get`,
`list`, `update` or `delete`, which every schema has (D16), or declare a
scope other than `instance` or `schema`. A `preconditionSchema` is held
to a `paramsSchema`'s rule, and a veto code that is not lowercase snake
case, or is listed twice, is refused (D16, amended). `Finalize`
checks `requires`, `conflicts` and the invocation policy values, since the
policy is fixed only once every extension has registered.
`Registry.Behavior(name)` returns a registered `Behavior`: the declaration,
the registering extension, the package that implements it,
`ConfigRequired()` (its config schema rejects `{}`) and `ValidateConfig`.

An operation's `paramsSchema` sets `"additionalProperties": false`, so its
parameters are exactly the ones it declares. An engine runs a behavior's
guards and the operation's handler on the same validated object, and a
key a guard does not know, such as an alias of a parameter it checks,
could otherwise reach the handler unchecked. `RegisterBehavior` refuses
any other value, or none, as the engine does and in its words: `behavior
<B> operation <op> paramsSchema must set "additionalProperties": false,
so its parameters are exactly the ones it declares`. The value must be
`false` itself: a schema that closes the object another way, with
`unevaluatedProperties: false` or `additionalProperties: {"not": {}}`, is
refused too.

A `createParamsSchema` is an object schema too, but its
`additionalProperties` may be a schema as well as `false`: a create's
parameters may be keyed by names the config gives (`Links` takes its
links by name), so the schema admits keys it does not list, and checks
each one's value. `RegisterBehavior` refuses one that does not compile,
is not an object schema, or leaves further keys unchecked (absent or
`true`), as the engine does and in its words: `behavior <B>
createParamsSchema must set "additionalProperties": false or a schema,
so no create parameter goes unchecked`. Neither the loader nor a schema
document reads it: a create gives the parameters to an engine
(`runtime/engine/README.md`, "Create parameters"), and the `behaviors`
command copies it with the rest of the declaration.

In the IR a type lists its behaviors in `TypeDef.Behaviors`, a list of
`ir.BehaviorRef{Name, Config}` written after `implements` and omitted when
empty. `Config` is canonical JSON (section 4.2); a config of `{}` is
stored as none, and an absent config is checked as `{}`. The data forms
write the same list:

```json
"behaviors": [{ "name": "acme.Rating", "config": { "maxStars": 5 } }]
```

The TypeScript form is the core decorator `@behavior(name, config?)` from
`@superschematic/schema`, on a class of any kind (a `DecoratorSpec` on
`TargetType`), so a General schema needs no other authoring package. Each
use appends one entry, so several on one class keep their source order.
Its `Apply` evaluates the config as any decorator argument (section 3.4),
stores it canonically, and fails at the argument, naming the type and the
behavior, when the behavior is not registered or the config fails its
schema; a behavior used twice fails at the second use. The TypeScript
half types the config per name through `BehaviorConfigs`, an empty
interface an extension's authoring package augments, as it augments
`MCPToolOptions` (section 3.15):

```ts
import "@superschematic/schema";

declare module "@superschematic/schema" {
  interface BehaviorConfigs {
    "acme.Rating": { readonly maxStars: number };
  }
}
```

A behavior that takes no config maps to `undefined`. There is no
fallback for a name the program's augmentations do not declare: as with
an unaugmented `@mcp` key, it does not type-check, so a schema's program
must include the augmentation, through an import of the authoring package
or a `tsconfig.json` `include` entry. The registry still decides: a
declared name the binary does not register fails the load. `format
--to=ts` writes one `@behavior` per entry, in list order.

The loader checks every type's list in every frontend (verify, after the
core checks and before the kind's `Verify`), and each failure names the
type and the behavior: a behavior that is not registered, one listed
twice, a config its schema rejects, a requirement the type does not list,
a conflict it does, a field that collides with the type's own fields or
another behavior's, and two behaviors that add an operation of the same
name. A behavior field collides with a type's own field that has its
name or its JSON key (`jsonTag`), since an instance's JSON holds the two
side by side; either fails as `type <T>: behavior <B> adds field <f>,
which the type declares`, the engine's wording. The data-form readers
check names and configs before JSON Schema validation, with the same
wording (section 5). The strict loader in
`@superschematic/schema-runtime` checks them through the meta-schema
alone, with the same verdicts, and the schema-file TypeScript types in
`@superschematic/schema-ir` type an entry as `BehaviorRef`, with the name
and config left open.

Until a generator renders behaviors, it refuses a type that composes one.
`generator.Run` checks once, before any generator in the pipeline runs:
the first enabled generator without `GeneratorSpec.RendersBehaviors` fails
the run with `generator: <name> does not render behaviors yet: type <T>
composes behavior <B>`. The check covers core and extension generators
alike; document generators, which render their documents, are not asked.
No core generator sets the flag. `build --emit-ir`, `format` and
`json-schema` run no generator and accept the schema.

The core declares the behaviors `@superschematic/engine` implements
(D16), one file each in `internal/registry/behaviors/`, which `New`
registers with no extension: `Workflow`, `Comments`, `Revisions`,
`Dependencies`, `Links`, `Rollups`, `Search`, `Reactions`, `Constants`,
`Variants` and `Branches`. It
declares the work-queue behaviors the optional
`@superschematic/engine-workqueue` package implements the same way:
`Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget` and `Retries`. Each spec names its package
(`registry.EnginePackage` or `registry.WorkQueuePackage`). Every binary
therefore accepts a schema that composes them, the schema-file JSON
Schema lists them, and `BehaviorConfigs` in `@superschematic/schema`
types their configs. None names an invocation policy, since a
distribution's policy need not have the core's values; each operation
takes the policy's default. Every
`paramsSchema` sets `additionalProperties: false`. `Links` and
`Dependencies` declare a `createParamsSchema`: a create's links by name
and its blockers. `Lease` declares a `preconditionSchema`, `{ token }`.
`Workflow`, `Revisions`, `Dependencies`, `Links`, `Branches` and every
work-queue behavior list the codes of their vetoes. `Constants` and `Variants`
declare no field, operation, parameter, precondition or code: what they
refuse is an issue at a field (`invalid_instance`), which the engine's
implementation returns from its `validate` hook, not a veto. A config a
declaration's `configSchema` accepts can still fail in the engine, whose
implementation checks what JSON Schema cannot (a Workflow transition
that names a state the config does not list, a Workflow outcome for a
state a transition leaves, a gated state of `Dependencies` that is not a
state of the type's Workflow, or a rollup whose linked schema has no
such link, say);
`runtime/engine/README.md`, "Core behaviors", has each engine
behavior's config, fields and operations, and
`runtime/engine-workqueue/README.md` each work-queue behavior's. The
engine registers its implementations when it opens, so a schema that
composes them runs with no extension linked (D10), its operations served
over HTTP and as MCP tools like any behavior's. A deployment that runs
the work-queue behaviors registers the package's implementations with the
engine; without them the engine refuses a schema that composes one.

| Behavior | Config | Fields | Operations |
| --- | --- | --- | --- |
| `Workflow` | `states`, `initial`, `transitions` (`from`, `to`, `permission`), `outcomes` (by terminal state: `success`, `failure` or `neutral`); required | `status` | `transition` |
| `Comments` | none | `commentCount` | `comment`, `listComments` |
| `Revisions` | `review` (`permission`), optional | `revision` | `listRevisions`, `propose`, `approve`, `reject`, `listProposals` |
| `Dependencies` | `schemas`, `gatedStates`, `satisfiedBy`, optional; requires `Workflow` | `blocked` | `addBlocker`, `removeBlocker`, `listBlockers`, `listDependents` |
| `Links` | `links` (by name: `schema`, `required`, `pinned`); required | `links` | `link`, `unlink`, and `listLinked`, of scope `schema` |
| `Rollups` | `rollups` (by name: `schema`, `link`, `function`, `field`, `gatedStates`, `outcomes`); required | `rollups` | none |
| `Search` | `fields`, `weights`; required | none | `search`, of scope `schema` |
| `Reactions` | `rules` (each a `when`, `enters`, `allTerminal` or `anyTerminal`, and a `then`, `transition` and `link`); required; requires `Workflow` | none | none |
| `Constants` | `fields`, `permission`; required | none | none |
| `Variants` | `field`, `by`, `types` (by a value of `by`, a type of the document); required | none | none |
| `Branches` | `kinds` (by name: `type`, a type of the document, `parent` (`key`, `of`), `order`, `singleton`, `units`, `retentionDays`), `primary`, `snapshotEvery`, `sweep` (`intervalMs`, `discardGrace`, `pruneBatch`, `abandonAfter`); required | none | `branch`, `save`, `commit`, `seal`, `merge`, `rebase`, `revert`, `releaseCommit`, `discard`, and the read-only `refs`, `releases`, `compose`, `materialize`, `released`, `diff` and `history` |
| `Lease` | `ttlMs`, `heartbeatMs`, `sweepMs`, `maxHoldMs`, `maxHoldField`, `onExpiry` and `escalate` (`transition`, `from`), `maxExpiries`, `exempt`, `requireToken`, `acquirePermission`, `overridePermission`, `directPermission`; optional; a `preconditionSchema`, `{ token }`; `@superschematic/engine-workqueue` | `lease` | `acquire`, `heartbeat`, `release`, `expire`, `direct`, `acknowledge`, `resetExpiries`, and `expireHolder`, of scope `schema` |
| `Assignment` | `permission`, optional; `@superschematic/engine-workqueue` | `assignee` | `assign`, `unassign` |
| `Queue` | `claim` (`from`, `to`), `priorityField`, `match`, `maxCandidates`; required; requires `Workflow` and `Lease`; `@superschematic/engine-workqueue` | none | `claim`, `refresh`, and `claimNext` and the read-only `countClaimable`, of scope `schema` |
| `Presence` | `ttlMs`, `principalField`, `onMissed` and `onBeat` (`transition`, `from`), `releaseLeases`, `sweepMs`; required; `@superschematic/engine-workqueue` | `presence` | `beat`, `miss` |
| `Blueprint` | `schema`, `parentLink`, `keyField`, one of `steps` (by key: `after`, `when`, `data`) and `from` (`link`, `field`), `copyFields`, `copyLinks`; required; `@superschematic/engine-workqueue` | `blueprint` | none |
| `Budget` | `meters` (by name: `limit` or `limitField`, `reserve` and `reserveField`, `scope`, `reset`), `limitPermission`, `onExceeded` (`direct`), `escalate` (`transition`, `from`); required; `@superschematic/engine-workqueue` | `budget` | `reserve`, the read-only `checkReserve`, `recordUsage`, `settle`, `setLimit`, `reserveFor`, `settleFor`, `recordUsageFor` |
| `Retries` | `classes` (by name: `attempts` and `hint`, or `terminal`), `totalAttempts`, `limitsField`, `limitsPermission`, `keepBest` (`minDelta`, `neverRegress`), `stuckAfter`, `resultField`, `exhaustedState`, `from`, `permission`; required; requires `Workflow`; `@superschematic/engine-workqueue` | `retries` | `recordAttempt` |

`Dependencies`, `Links` and `Rollups` reach other instances (D16,
amended): a blocker, a link target or the instances a rollup reads are
instances of the schema a config names, which the loader does not
resolve; the engine looks the name up when an operation runs or a field
is read, in the instance's namespace and then the shared one, and checks
a rollup's schema and link when the schema is defined. `make cli-smoke`
loads `fixture-cross-instance-json`, whose type composes the first two,
and `fixture-rollups-json`, whose type rolls up its tasks, with the core
binary, and their TypeScript twins in the tsreader fixtures load to the
same IR.

`Search` indexes the type's own text fields that its config names, which
the loader does not check against the type: the engine refuses a field
the type does not declare or whose values are not strings when the schema
is defined. `make cli-smoke` loads `fixture-search-json` and its
TypeScript twin to the same IR as well.

`Reactions` declares a config and nothing else: the engine's runner
applies its rules after a change commits (D16, amended), and a rule's
links and states are checked by the engine, which sees the type's
Workflow and Links configs. `make cli-smoke` loads
`fixture-reactions-json`, whose projects start, finish and fail their
parent, and its TypeScript twin loads to the same IR.

`Lease`, `Assignment`, `Queue`, `Budget` and `Retries` name the type's
fields (`maxHoldField`, `priorityField`, `match`, `limitField`,
`reserveField`, `limitsField`, `resultField`), its Workflow states
(`onExpiry`, `escalate`, `claim`, `exhaustedState`, `from`), its Links
links (a meter's `scope`, whose target schema must compose `Budget` with
the meter) and its other behaviors' operations (`exempt`), which the
loader does not check: the work-queue package does, when the schema is
defined. So do `Presence` (`principalField`, its states, and the schemas
of `releaseLeases`) and `Blueprint` (the child schema, its links and
fields, and this type's fields its steps read). `make cli-smoke` loads
`fixture-workqueue-json`, whose jobs compose the first five and whose
workers, batches and steps, one type a file, compose the other two, and
its TypeScript twin loads to the same IR.

acme declares `acme.Rating` and types its config in
`packages/schema/src/behaviors.ts` (section 10);
`internal/registry/registrytest` declares two, `acme.Stock` and
`acme.Audited`, which requires it, with a TypeScript fixture and its JSON
and YAML twins.

The code that runs a behavior is TypeScript: an implementation for
`@superschematic/engine` (`runtime/engine/README.md`, "Behaviors") that
carries the same declaration. Some of its members have no part in the
declaration, since a client never calls them: `reactions`, which the
engine's runner hands committed events after the commit, as the
principal the deployment names for it, and `schedules`, named timed work
with an interval, which a schema's config may turn off and whose runs may
write the behavior's own tables where no operation's result changes
(`runtime/engine/README.md`, "Reactions and schedules" and "The
runner"). The compiler neither sees nor checks them; the engine checks
them when the implementation registers. Nor does it see what
`parseConfig` reads of the schema's other types (`ConfigTarget.types`):
the engine holds each type read there to its document checks and its
compatibility rule, and a behavior's contexts check a value against one
with `validate(type, value)` ("Other types" there).

`superschematic behaviors --out <dir>` copies the declaration there: one
canonical `<name>.behavior.json` per behavior the binary registers
(`--extension <name>` keeps one extension's, `--package <npm name>` the
ones that package implements), and
`--check` fails, naming each file, on a copy that differs, is missing or
has no behavior. It is a command of the binary rather than a tool in the
core module, as `internal/tools/scalarcatalog` is, because an extension's
declarations are registered only in its own binary: acme's copy comes
from `acme-schematic behaviors --extension acme`, which acme's smoke runs
with `--check` (section 10). The core's copies come from
the core binary, each into the package that implements it: `make
behaviors` writes `runtime/engine/typescript/src/behaviors/core/declarations`
with `--package @superschematic/engine` and
`runtime/engine-workqueue/typescript/src/declarations` with `--package
@superschematic/engine-workqueue`, so each package carries only the
declarations it implements, and `make behaviors-check`, part of `make
test` and CI's go job, fails on a stale copy in either. The copy is canonical, not the source bytes:
the declaration's keys in `BehaviorDeclaration`'s order, each JSON
Schema's object keys sorted (`createParamsSchema` and `preconditionSchema`
included), two-space
indents and a final newline, so it changes only when the declaration
does.

## 4. The open IR

### 4.1 Slots

The IR has two open slots, both `map[string]json.RawMessage`:

- `Extensions` on `Schema`, `TypeDef`, `FieldDef` (which also holds
  operations) and `OperationSet`. The key is an extension name. The value
  is one JSON object whose keys are that extension's decorator names.
- `Documents` on `Schema`. The key is a `DocumentSpec.Name`.

Core decorators never write `Extensions`; they write typed fields. The
values are raw JSON rather than interfaces so the persisted IR decodes back
into the same Go types.

Behaviors are the one place an extension's data sits outside its
`extensions.<name>` slot: `TypeDef.Behaviors` is a core field that holds
core and extension behaviors alike, and an extension's are told apart by
the `<extension>.` prefix of their names (section 3.16). That is why an
extension name may not contain a dot. The IR is its own module (D1), so an extension, a
runtime or a tool reads it without linking the compiler.

A field that carries acme's `@shelf({ aisle: 3, bay: "B" })` in the
TypeScript form has this next to its core keys in the IR, and the data forms
write the same object:

```json
"extensions": { "acme": { "shelf": { "aisle": 3, "bay": "B" } } }
```

A marker such as acme's `@feedKey` is `true` in the same object:
`"acme": { "feedKey": true, "shelf": { ... } }`. A class in an argument is
a class reference there (section 3.4): acme's `@crossSell({ with: Product })`
is `"acme": { "crossSell": { "with": { "class": "Product" } } }` on the type.

### 4.2 Codecs

`ir/extensions.go` is the typed access layer:

| Function | Does |
| --- | --- |
| `GetExtension[T](node, ext)` | decode `Extensions[ext]` into `T`; `ok` is false when absent |
| `SetExtension[T](node, ext, v)` | encode `v` canonically and store it; a value that encodes to `{}` deletes the key |
| `UpdateExtension[T](node, ext, fn)` | read, let `fn` modify, store; what most `Apply` functions call |
| `GetDocument[T](schema, name)`, `SetDocument[T]` | the same over `Documents`; `SetDocument` keeps `{}`, because a document's presence runs its generator |
| `CanonicalJSON(raw)` | compact JSON, object keys sorted, arrays and number literals as written |

Every path that stores extension or document data or a behavior's config
runs `CanonicalJSON`: the codecs, the data-form readers
(`CanonicalizeExtensions`, `CanonicalizeDocuments`,
`CanonicalizeBehaviors`) and the document loader. The persisted IR is the
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
   its values as the enum and its default as the default (section 3.15).
6. Close `BehaviorRef`, the entries of a type's `behaviors`, to the
   registered behaviors: `name` is one of their names, and an `if`/`then`
   per behavior holds its `config` to its `configSchema`, the way a
   decorator's value is its `Args`. A behavior without a config schema
   takes the empty closed object, and a config schema that rejects `{}`
   makes `config` required (section 3.16). The core registers three, so
   every registry has some; a registry with none (a test's) gives
   `behaviors` no entry (`maxItems: 0`) and `name` no enum, since ajv
   refuses an empty one.
7. Give every property the Go encoder omits at one value that value as its
   default: an `omitempty` string, bool or number that is not a pointer
   (`""`, `false`, `0`) and an `omitempty` slice or map (`[]`, `{}`). A
   behavior's `config` is raw JSON, which the readers store as absent when
   it is `{}`, so its default is `{}`. A pointer keeps its zero value and
   has no default. A reader in another language drops a property that
   holds this default and so writes a document as the Go reader decodes it
   (`@superschematic/schema-runtime`'s strict loader does).

With only the core registered, `extensions` and `documents` admit no key
and `behaviors` admits the core's three.
The compiled definition is cached per registry, keyed by a weak pointer and
the extension list. `superschematic json-schema` prints it for the binary's
registry; `--naming` selects the naming file, since the command has no
service directory.

Before validation the readers check slot names and behavior entries, to
give an error that names the key or behavior and where it sits. After it
they run every extension decorator through the registry the way the
TypeScript frontend does
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
   initializer, `service({...})` sentinel calls, `as` expressions and
   classes, local or imported, which become class references (section
   3.4). Anything computed is an error.
4. Validate against `Args`, then call `Apply`.

The walker, not the registry, decides the shape of a class: a table, an
embedded struct, a projection, an operation set or a trait. `KindSpec`
supplies the roles and whether operation sets are allowed. After the walk,
verify runs the core checks, the kind's `KindSpec.Verify` and the
registered checks for the kind (section 3.12), the same way for all three
forms.

### 6.3 Schema config kinds

In `@superschematic/schema-config`, a config's `kind` has the type
`SchemaKindName`: a member of the closed `SchemaKind` enum (the four core
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

A `schema.config.ts` may import other services' sentinels for its
handles, and nothing else beyond the config package (D34). `build` decides
before loading whether to write the sibling sentinels first: when the
target's kind sets `ImportsSiblingSentinels`, or when its config imports
anything but the config package. For a TypeScript config it scans the
file for `SchemaKind.<Kind>` or the quoted kind name, and for its import
specifiers, so it does not need a compiler program. `build-all`,
`build --with-deps` and `migrate` write every sentinel before discovery,
reading only each config's `name` and `kind`.

## 7. Dispatch

### 7.1 Run

`generator.Run(schema, cfg, opts)`:

1. Parse the config's `outputs` block against the registry (section 7.2).
2. Resolve `schema.Kind` in the registry; an unknown kind is an error.
   A kind whose pipeline runs the core `orm` generator (the DB kind, or
   an extension kind that lists it) needs `outputs.types.go`, since the
   Go ORM imports the Go types; without it the run fails here.
3. Take `reg.Pipeline(kind)`: the kind's `Pipeline` in order, then every
   other generator whose `Kinds` lists the kind, in registration order.
4. Call each generator's `Enabled`. Collect the `Dirs` of the enabled ones
   and fail, before any runs, if two claim the same directory, if a
   type composes a behavior and an enabled generator does not set
   `RendersBehaviors` (section 3.16), or if `types` is enabled and a
   library it writes imports the types of a dependency whose config does
   not enable that language. The dependencies are the ones
   `codegen.TypeDependencies` finds in `Schema.Imports`: those that
   contribute an enum, a union or an object type rather than only
   scalars. `Options.DependencyConfig` supplies their configs;
   `build-all` and `build --with-deps` set it, and without it (a single
   `build`) the run logs the dependencies it did not check.
5. Run the enabled generators in order. `envConfig` writes the loader of
   an `@envVars` class in the language `outputs.types` picks: Go when
   `go` is on, Rust when only `rust` is. The Go loader imports the Go
   types, so with neither on it writes the class's `values-schema.json`
   alone. The TypeScript types generator writes the class's TypeScript
   loader, `config.ts`, into the types package whenever `typescript` is
   on (D22). A run with none of the three logs that it wrote no loader.
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
target languages, fills the API defaults, rejects an enabled `outputs.api`
and an `outputs.sdk` language whose `outputs.types` language is off (each
imports that types package; `GO`, `RUST` and `TYPESCRIPT` servers need
`go`, `rust` and `typescript`), rejects an unknown key in `outputs.sql`,
and keeps every section raw in `Outputs.Raw`. It does not know the kind,
so the ORM's need for the Go types is checked by `Run` (section 7.1) and
`generator.ExpectedOutputDirs` instead. An extension
generator reads its own section with `registry.DecodeOutput(outputs, key,
&v)`, usually in `Enabled`; the section has already passed its
`OutputSchema`.

### 7.3 build-all

`build-all` writes every service's sentinel (`buildplan.EnsureSentinels`,
section 6.3), then discovers the services under the services root with the
command's registry (`buildplan.DiscoverWith`), so an extension kind in a
sibling config is known at discovery. Each service's expected output
directories come from the `Dirs` of its present documents and of the
enabled generators in its pipeline; the `--cache` layer stores and restores
those. `generator.ExpectedOutputDirs` refuses the configs `Run` refuses
before its pipeline (section 7.1, steps 1 and 2), so such a service fails
discovery and nothing is built. Services build in dependency order, each
with the discovered schema set as the document loaders' `Catalog`. The
dependency graph is written next, and its `[deps] copy` when the naming
file sets one. The hooks run last, whether or not any service was built
(section 3.8).

### 7.4 Build cache key

A service's cache key, which is also its stamp, hashes the service's
schema tree, its `authDb`'s tree, the files its documents imported from
elsewhere under the schemas root, the keys of its dependencies, the
resolved naming without `[deps]`, the `[cache] inputs` files, the schemas
workspace's `package.json` and `bun.lock`, the `go`, `bun`, `rustc` and
`cargo` versions on the `PATH`, and the tool digest.

The tool digest stands for the generator code. By default it is a SHA-256
of the running executable, so a rebuilt binary misses every entry its
predecessor stored. That is safe, but it only shares entries between
identical binaries. `make build` and the release pipeline build with
`-trimpath -buildvcs=false`, so the binary depends on the Go sources and
the link inputs, not on the checkout path or the commit. Rebuilding the
same sources in the same checkout gives the same bytes. Two checkouts
still link different binaries: `CGO_LDFLAGS` names each checkout's own
superscalar archive by absolute path, and the Go build ID hashes the
linker flags. On macOS even an empty build ID (`-ldflags=-buildid=`)
leaves a different `LC_UUID`.

A distribution whose checkouts should share the cache sets
`cli.Config.ToolDigest` (section 3.9) to a digest of everything that
shapes its outputs: its extension sources, any templates or data they
embed, and the superschematic version it links (its `go.sum` lines, or
the commit it pins). The digest replaces the executable hash and nothing
else, so the schema, naming and toolchain parts of the key still apply.
A digest that misses a changed input hands out entries that other code
generated. Embed it at build time, from `//go:embed` of the sources or a
file `go generate` writes, rather than reading files at run time.
`examples/acme-schematic` leaves it unset: it links the core through a
`replace` to this checkout, so a digest of its own files would miss core
edits.

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
  (`Session(id, jti, user, expiresAt)`), whether that table is
  soft-deletable with a nullable `deletedAt` the store reports
  (`SessionSoftDelete`, D33), and the principal store (`User(id, name)`);
  `Extra` holds the provider's own findings.
  `registry.AnalyzeSessionStores` is the core half and `registry.HasTable`
  the probe;
- calls `Endpoint` per operation. The core has already set `RequiresAuth`
  and the required permissions; the provider sets `IsScopedEndpoint` and
  `ScopeParamName` (the SDK generators read them) and its own `Auth` data;
- renders the core templates, which call the provider at fixed hook points
  through `authSnippet`. `apigen.AuthSnippets` lists the eighteen snippets
  (imports, context shims, store adapters, config fields, route setup, the
  per-route permission middleware, `go.mod` lines). A provider defines every
  one, empty when it adds nothing. The per-route permission middleware runs
  after the route's rate and body limits and its service step (D37), and
  before its payload decryptor and timeout, so it reads the caller from the
  request context, never from the body. A route with a service clause runs
  `AuthMiddleware` and `routesProtectedMiddleware` in its own chain, just
  before the permission middleware, instead of on the protected group. The
  generator checks the set when it
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
its provider, which supplies those two functions. D15 in
`docs/DECISIONS.md` records the split.

## 9. What the core registers

| Surface | Core registration |
| --- | --- |
| Kinds | `DB`, `API`, `General`, `Stack` (section 3.3) |
| Decorators | 54 specs over the four targets in `internal/registry/core_decorators.go`, `core_projection.go`, `core_stack.go`, `docs_decorators.go` and `behaviors.go`, declared in `@superschematic/{schema,db,api,schema-config,stack}`. Types (18): `trait`, `source`, `envVars`, `jsonField`, `denyUnknownFields`, `strictJSON`, `versioned`, `optimistic`, `versionGraph`, `graphMember`, `index`, `projection`, `join`, `behavior`, `stack`, `server`, `database`, `environment`. Fields (14): `key`, `unique`, `searchField`, `jsonField`, `uiHidden`, `internalMetadata`, `temporalFormat`, `conflictUnit`, `virtual`, `sourceMustProject`, `docs`, `purpose`, `icon`, `column`. Operation sets (5): `rateLimit`, `bodyLimit`, `timeout`, `requireService`, `allowService`. Operations (17): `rest`, `requirePermission`, `requireOwnership`, `auth`, `encrypted`, `publicRoute`, `webhook`, `hmacVerified`, `manualRouteRegistration`, `rateLimit`, `bodyLimit`, `timeout`, `requireService`, `allowService`, `docs`, `mcp`, `icon` |
| Generators | `types`, `sql`, `orm`, `api`, `sdks`, `envConfig`, and the Stack kind's `stack` and `server` (section 3.6; `docs/stack-model.md`, sections 5.1 and 8.1). For a schema that declares a version graph, `orm` also writes the graph's shell and `types` its descriptor (D17) |
| Auth providers | `session` (section 8.2) |
| Scalar catalog | the superscalar Go package (section 3.10) |
| Tool invocation policy | `invocationPolicy`: `auto` or `ask`, `auto` by default (section 3.15) |
| Behaviors | `Workflow`, `Comments`, `Revisions`, `Dependencies`, `Links`, `Rollups`, `Search`, `Reactions`, `Constants`, `Variants`, `Branches`; the work-queue package's `Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget`, `Retries` (section 3.16) |
| Documents | none |
| Build-all hooks | none |
| Checks, OpenAPI hooks, tool hooks | none |
| Platforms, connectors, targets, DNS platforms, provisioners | none (`docs/stack-model.md`, section 6) |
| Commands | `build`, `build-all`, `json-schema`, `format` |

The core stays provider-neutral (`CONTRIBUTING.md`, "The core stays
provider-neutral"). A surface that only one deployment needs belongs in an
extension. D10 in `docs/DECISIONS.md` applies that rule to features that
have a generic mechanism and distribution-specific names or policy: names
go in the naming file, rules in the extension.

## 10. Worked example and acceptance

`examples/acme-schematic` is the acceptance test of this model: a separate
Go module (`example.com/acme/schematic`, with a `replace` onto this
checkout) that imports only `registry`, `loader`, `cli` and `ir` (plus the
superscalar Go package, for the rows of its scalar catalog) and adds one of
each surface:

| Surface | acme | File |
| --- | --- | --- |
| Kind | `Catalog`, pipeline `types`, `catalog` | `ext/kind.go` |
| Decorators | `@shelf` (an argument) and `@feedKey` (a marker) from `@acme/schema`, on Catalog fields; `@crossSell`, whose argument names a class, on Catalog types | `ext/decorator.go`, `ext/cross_sell.go`, `packages/schema` |
| Scalar catalog | the core scalars plus `Acme.Photo`, a file-upload scalar; `Product.photo` in the Catalog service bounds it with `uploadMaxBytes` | `ext/scalars.go`, `packages/schema` |
| Document | `catalog.config.yaml` on Catalog services, with a generator | `ext/document.go` |
| Generator on core kinds | `acmeManifest`, appended to DB, API, General and Catalog | `ext/manifest.go` |
| Build-all hook | `acmeInventory`, every service's manifest merged into one file | `ext/inventory.go` |
| Auth provider | `apikey` | `ext/auth/` |
| Checks and an OpenAPI hook | `acmeIcons` and `acmeDocsAudience` over the core `@icon` and `@docs`; `acmeDocsKey` moves the `@docs` record to `x-acme-docs` | `ext/docs.go` |
| Check and a tool hook | `acmeToolsClassified` requires `@mcp` on every `shop-api` operation; `acmeTools` writes acme's tool keys and icon variant | `ext/mcp.go` |
| Check on a core kind | `acmeProjectionScope`: every projection view in a DB schema binds the scope setting first | `ext/projection_policy.go` |
| Command | `describe` and `fields`, through `cli.CommandProvider` | `ext/command.go`, `ext/fields.go` |
| Configuration | `[extension.acme] region` and `projection_scope_setting`; `metadata_key_prefix`, `scalar_jsdoc_tag`, `[package_aliases]` (`@acme/schema-config`) and `[deps] copy` | `ext/extension.go`, `schemas/superschematic.toml` |
| Tool invocation policy | `confirm`: `never` or `always`, `never` by default, with its `MCPToolOptions` augmentation | `ext/mcp.go`, `packages/schema/src/mcp.ts` |
| Behavior | `acme.Rating`, which a General data-form service in `ext/testdata/services/shop-ratings` composes, and its TypeScript twin `shop-ratings-ts` with `@behavior`; its `BehaviorConfigs` augmentation; its engine implementation over the declaration's copy | `ext/behavior.go`, `ext/rating.behavior.json`, `packages/schema/src/behaviors.ts`, `packages/behaviors` |
| Binary | `cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})` | `cmd/acme-schematic` |
| A core mechanism in acme's terms | the `Planogram` version graph (D17): `Bay` and `Facing` members declared with the core's `@versionGraph`, `@graphMember` and `@conflictUnit` | `schemas/services/shop-db/src/planogram.schema.ts` |

The acceptance criterion: an extension adds every surface above without
editing a file outside its own module, and adding one more decorator stays
that way. Two scripts check it, and the `acme` job in
`.github/workflows/ci.yml` runs both:

- `scripts/smoke.sh` builds the core binary and the acme binary, runs the
  module's tests, runs `build-all` over the example schemas, and asserts
  each surface did its work: `describe` lists the kind, document,
  provider and checks; `catalog.json`, the document's output and a
  manifest per service exist, and the inventory hook merges them again
  when every service is restored from the cache; the `@shelf` payload,
  the `@feedKey` marker and `@crossSell`'s class reference, which `format`
  writes as YAML and as JSON and which load back to the same IR,
  `Acme.Photo`'s upload metadata with the `uploadMaxBytes` bound on
  `Product.photo`, and the scoped projection view are in the IR, and the view, its migration and
  its Arrow schema are written under acme's metadata key prefix; the
  generated API compiles against `apikey`; the committed graph copy is
  current; `fields` type-checks the label declarations; the `@docs`
  records reach the OpenAPI document under `x-acme-docs` and the tool
  documents carry acme's keys; `acme.Rating` reaches the IR of
  `shop-ratings` and of its TypeScript twin, `json-schema` and `format`
  accept it (to YAML and to TypeScript), and `build` refuses the service,
  naming the `types` generator; the implementation's copy of its
  declaration is current (`behaviors --check`), and an engine with the
  implementation publishes `shop-ratings`' `Product`, rates one and reads
  its rating fields while an engine without it refuses the schema;
  shop-db's `Planogram` graph expands in
  the IR, its descriptor and shell are written and the ORM and the API
  over it compile, and `format` writes the declarations back rather than
  the expansion. It also asserts that the
  core-only binary rejects the Catalog service, the `acme.Rating` behavior
  and the naming file that selects `apikey`,
  that it builds the DB and API services with `session` and the result
  compiles, and that it writes the core keys where acme's hooks write its
  own.
- `scripts/check_second_decorator.sh` applies
  `scripts/second_decorator.patch` (a `@perishable` decorator), reruns the
  smoke, checks the new payload reached the IR, and fails if any path
  outside `examples/acme-schematic/` changed.

Inside the core module, four extensions test the seams against the real
loader, generators and resolver:

- `internal/registry/registrytest` is an in-tree fixture extension with a
  kind, decorators on all four targets, a data-form document, a generator
  and two behaviors. `TestAcmeExtensionEndToEnd` loads a TypeScript fixture and its
  data-form twin, checks both produce the same IR with the decorator values
  in their slots, and runs the generator. Its `@pairsWith` takes a class:
  the tests in `class_ref_test.go` load one of the schema's own classes and
  one imported from another service's package from all three forms, carry
  them through the writers `format` uses, and fail a class the schema
  neither declares nor imports.
- `stack/stacktest` registers a fake target with its platforms,
  connectors, DNS platform and provisioner, through the public packages
  alone, and resolves a stack over the acme-shop services into golden
  `environment.json` files (`docs/stack-model.md`, section 6.7).
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
9. A data form's class reference to an imported name is taken on trust.
   The reader does not load the dependency, so it checks only that the
   schema's imports list the name, and cannot tell a class from an enum or
   a scalar of that name. The TypeScript form resolves the class through
   the compiler.

## 12. References

| Path | What it holds |
| --- | --- |
| `registry/registry.go`, `registry/generate.go` | the public registry package |
| `internal/registry/registry.go` | `Registry`, `Use`, `Finalize`, the `Register*` methods |
| `internal/registry/spec.go` | `KindSpec`, `DecoratorSpec`, `DocumentSpec`, `GeneratorSpec`, the contexts, `BuildAllHook` |
| `internal/registry/core.go`, `core_decorators.go`, `core_projection.go`, `docs_decorators.go` | the core kinds and decorators |
| `internal/registry/scalars.go`, `outputs.go` | the scalar catalog seam, `ParseOutputs` |
| `internal/registry/behaviors.go`, `internal/loader/verify/behaviors.go`, `ir/behaviors.go` | behavior declarations, the loader's checks, `ir.BehaviorRef` |
| `internal/generator/core.go`, `generator.go` | the core generators, `Run` |
| `internal/generator/apigen/auth.go`, `apigen/sessionauth` | the provider interface, the snippet hooks, the core provider |
| `internal/generator/apigen/openapi_hooks.go`, `apigen/tools.go` | `OpenAPIHook`, `ToolHook` and where the `api` generator runs them |
| `internal/loader/documents.go` | document loading |
| `internal/loader/tsreader/walker.go`, `evaluate.go` | decorator origin, dispatch, static evaluation |
| `internal/loader/schemafile/schema.go`, `slots.go` | the data-form JSON Schema composition and slot checks |
| `internal/loader/verify/` | import rules, class references and `KindSpec.Verify` |
| `ir/extensions.go` | the codecs |
| `ir/class_ref.go`, `internal/registry/class_ref.go` | the class reference shape, `ClassRefSchema`, `DecodeClassRef` |
| `ir/mcp_invocation.go`, `internal/generator/apigen/tool_invocation.go` | the IR's invocation policy and its encoding, `ToolInvocationPolicy` |
| `cli/cli.go` | `cli.New`, `CommandProvider` |
| `loader/loader.go` | the public loader package |
| `examples/acme-schematic/` | the worked example and its acceptance scripts |
| `docs/DECISIONS.md` | the decisions this design rests on (D1, D2, D3, D4, D6, D10, D11, D13, D15, D16) |
