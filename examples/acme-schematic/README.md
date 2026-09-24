# acme-schematic

A complete downstream extension of superschematic, small enough to read in
one sitting. It is the acceptance test of the extension model: everything
here is added without editing a file under the core, and
`scripts/check_second_decorator.sh` proves that adding one more decorator
stays that way.

The extension adds one of each registration surface:

| Surface | What acme adds | File |
|---|---|---|
| Kind | `Catalog`, with a `catalog` generator | `ext/kind.go` |
| Decorator | `@shelf` from `@acme/schema`, into the field's `extensions.acme` slot | `ext/decorator.go`, `packages/schema` |
| Document | `catalog.config.yaml` next to a Catalog schema, with a generator | `ext/document.go` |
| Generator on core kinds | `acmeManifest`, appended to DB, API, General and Catalog | `ext/manifest.go` |
| Build-all hook | `acmeInventory`, every service's manifest merged into one file | `ext/inventory.go` |
| Auth provider | `apikey`, an `X-API-Key` header over the generic session runtime | `ext/auth/` |
| Checks and an OpenAPI hook | policy over the core documentation decorators: acme's `@docs` audiences and `@icon` names only, and the `x-acme-docs` vendor key | `ext/docs.go` |
| Check, a tool hook and an invocation policy on `@mcp` | every operation of `shop-api` declares `@mcp`, visible or hidden; the SDK tool documents carry acme's vendor keys and icon variant; `confirm` replaces the core's `invocationPolicy` | `ext/mcp.go`, `packages/schema/src/mcp.ts` |
| Command | `describe` and `fields`, through `cli.CommandProvider` | `ext/command.go`, `ext/fields.go` |
| Binary | `acme-schematic`: `cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})` | `cmd/acme-schematic` |

The schemas root under `schemas/` has one service per kind: `shop-db` (DB),
`shop-api` (API, authenticated with `apikey`), `shop-config` (General) and
`shop-catalog` (Catalog). Its `superschematic.toml` names the generated
packages `example.com/acme/...`, `@acme/...`, `acme_types_...` and `acme-...`.

## Run it

From the repository root, after `make setup`:

```sh
examples/acme-schematic/scripts/smoke.sh
examples/acme-schematic/scripts/check_second_decorator.sh
```

The smoke builds the module and both binaries, runs `build-all` over the
schemas root, and asserts each surface did its job (the list is at the top of
the script). The check applies `scripts/second_decorator.patch`, reruns the
smoke, and fails if any path outside this directory changed. Both are the
`acme` job in `.github/workflows/ci.yml`.

To poke at it by hand:

```sh
export CGO_LDFLAGS="$(scripts/superscalar-dep.sh --print)"
cd examples/acme-schematic
go build -o /tmp/acme-schematic ./cmd/acme-schematic
/tmp/acme-schematic describe schemas
/tmp/acme-schematic fields labels/shelf-label.d.ts ShelfLabel
/tmp/acme-schematic build-all schemas/services
/tmp/acme-schematic build schemas/services/shop-catalog --emit-ir | jq .types.Product
```

## Layout

```
examples/acme-schematic/
  go.mod                      module example.com/acme/schematic; replace => ../..
  cmd/acme-schematic/         the binary
  ext/                        the extension (package ext)
    extension.go              Extension: Name, Register, Commands; [extension.acme] config
    kind.go                   Catalog kind + catalog generator
    decorator.go              @shelf + the field codec
    document.go               catalog.config document + generator
    manifest.go               acmeManifest generator on every kind
    inventory.go              acmeInventory build-all hook
    docs.go                   audience and icon checks, x-acme-docs OpenAPI hook
    mcp.go                    every shop-api operation declares @mcp; acme tool keys and icon variant; the confirm policy
    testdata/services/returns-api  an API the tests load that declares confirm on @mcp
    command.go                describe subcommand
    fields.go                 fields subcommand: a declaration file type-checked with loader.NewDeclarationProgram
    auth/                     apikey auth provider + its snippet templates
  packages/schema/            @acme/schema, the authoring package @shelf is imported from, and the confirm key's type
  schemas/
    superschematic.toml       naming, auth_provider = "apikey", [paths], [deps], [extension.acme]
    deps.json                 the committed copy of the dependency graph ([deps] copy)
    tsconfig.base.json        path aliases for @superschematic/*, @acme/*, superscalar
    services/shop-db          DB: User, Session, ApiKey, Product tables
    services/shop-api         API: ProductQueries, ProductMutations over shop-db, with @docs, @mcp, @icon
    services/shop-config      General: ShopConfig with @envVars and field @docs/@purpose/@icon
    services/shop-catalog     Catalog: Product, Bundle with @shelf; catalog.config.yaml
  labels/                     shelf-label.d.ts and location.d.ts, the declarations `fields` reads
  scripts/smoke.sh            the end-to-end assertions
  scripts/check_second_decorator.sh, second_decorator.patch
```

A real extension depends on `github.com/parable-work/superschematic` at a
tag and drops the `replace` lines in `go.mod`. The `[paths]` table in
`superschematic.toml` exists for the same reason: it points the generated
modules' path dependencies at this checkout so the smoke can compile them.
A deployment that consumes published modules leaves it out.

## The extension type

`registry.Extension` is two methods:

```go
type Extension struct{}

func (Extension) Name() string { return "acme" }

func (Extension) Register(r *registry.Registry) error {
	cfg, err := decodeConfig(r.ExtensionConfig(Name))
	// ... one register* call per surface ...
	return r.RegisterAuthProvider(auth.Provider{})
}
```

`Name()` is the key of the extension's slot everywhere: `extensions.acme` on
every IR node, `[extension.acme]` in `superschematic.toml`, and the
`Extension` field of every spec it registers. `Register` runs once per
registry assembly, before `Finalize` checks the result (a generator naming a
kind nobody registered, two decorators with the same name, a document on an
unknown kind are all assembly errors, reported before any schema loads).

`r.ExtensionConfig("acme")` returns the `[extension.acme]` table of the naming
file undecoded. The core does not know the keys; `decodeConfig` in
`extension.go` owns them and rejects the ones it does not recognize, so a
typo in the toml fails the build instead of being ignored.

Every file under `ext/` imports only the public packages `registry`,
`loader`, `cli` and `ir`, and `fields.go` the pinned TypeScript compiler's
shim for the node kinds it matches. Nothing under `internal/` is reachable
from outside the core module, which is what makes "no core edits"
checkable.

## A kind

`ext/kind.go` registers `Catalog`:

```go
r.RegisterKind(registry.KindSpec{
	Name:              "Catalog",
	Extension:         Name,
	StructRole:        ir.RoleEmbeddedStruct,
	Pipeline:          []string{"types", "catalog"},
	AllowedReferences: map[string]bool{"General": true},
})
```

- `StructRole` is what a plain `abstract class` in a schema of this kind
  becomes in the IR. DB schemas make tables; API schemas make projections;
  a Catalog makes embedded structs.
- `Pipeline` names the generators that run for the kind, in order. `types`
  is the core type generator, so a Catalog schema gets TypeScript, Go,
  Python or Rust types like any other kind; `catalog` is registered below.
- `AllowedReferences` says which other kinds a Catalog schema may import
  types from. Tables and API projections are out; a shared General type
  (`Money`, say) is in.

A schema declares the kind with `kind: "Catalog"` in `schema.config.ts`.
`SchemaKind` is a closed enum of the core three; `defineConfig` also takes
any string, and the registry validates it. The core-only binary rejects the
file with `unknown kind "Catalog" (registered kinds: API, DB, General)`,
which is one of the smoke's assertions.

## A generator

The `catalog` generator is a `registry.GeneratorSpec`:

```go
r.RegisterGenerator(registry.GeneratorSpec{
	Name:         "catalog",
	Extension:    Name,
	Kinds:        []string{"Catalog"},
	OutputKey:    "catalog",
	OutputSchema: CatalogOutputSchema,
	Dirs:         func(c registry.GenerateContext) []string { ... },
	Enabled:      func(c registry.GenerateContext) (bool, string) { ... },
	Generate:     generateCatalog,
})
```

- `OutputKey` is the key under `outputs:` in `schema.config` that switches
  the generator on; `OutputSchema` is the JSON Schema of that block, checked
  when the config loads. `registry.DecodeOutput(c.Outputs, "catalog", &o)`
  reads it back in `Enabled`. The core's `outputs` type knows only the core
  keys, so the schema config carries a `@ts-expect-error` on the line; the
  registry, not tsc, is the authority.
- `Dirs` lists every directory `Generate` writes to, so `build` can clean
  stale output and `build-all` can compute what changed.
- `Generate` gets the loaded `ir.Schema`, the config, the naming and the
  output root in one `GenerateContext`, writes its files and calls
  `c.Done(key, dir)` so the build log and `Result.Outputs` list them.

`generateCatalog` walks every type's fields, reads the `@shelf` payload
through the codec (next section), and writes `catalog.json`.

The `acmeManifest` generator in `ext/manifest.go` is the other shape: a
generator on kinds the extension did not define. It lists `Kinds: []string{"DB", "API", "General", "Catalog"}` and has no `OutputKey`, so it runs after
every listed kind's own pipeline and cannot be switched off from a schema
config. It writes `manifest.json` under `dist/acme/manifest/<service>/` with
the region from `[extension.acme]`.

## A decorator

`@shelf` lives in two places. The TypeScript half, `packages/schema/src/index.ts`,
is a no-op at run time; it exists so an author gets completion and tsc
rejects a bad argument first:

```ts
export function shelf(_args: ShelfArgs): PropertyDecorator {
  return () => {};
}
```

The Go half registers the decorator and the meaning of its argument:

```go
r.RegisterDecorator(registry.DecoratorSpec{
	Name:      "shelf",
	Extension: Name,
	Packages:  []string{"@acme/schema"},
	Target:    registry.TargetField,
	Kinds:     []string{"Catalog"},
	Args:      ShelfArgs,
	Apply: func(n registry.Node, args []any, _ registry.Site) error {
		var s Shelf
		if err := registry.DecodeArgs(args, &s); err != nil {
			return err
		}
		return ir.UpdateExtension(n.Field, Name, func(f *fieldExt) { f.Shelf = &s })
	},
})
```

- `Packages` names the npm package the decorator is imported from.
  Registering a decorator from `@acme/schema` makes it an authoring package:
  the TypeScript frontend resolves symbols from it, and a schema importing
  any other package fails to load.
- `Target` is the node the decorator may sit on (type, field, operation
  set, operation); `Kinds` restricts it to schema kinds. A `@shelf` on a DB
  table field is a load error that names the decorator.
- `Args` is the JSON Schema of the argument list. Both frontends validate a
  use against it before `Apply` runs: the TypeScript reader from the AST,
  the JSON/YAML reader from the `extensions.acme.shelf` value it finds in
  the file. `Apply` only sees shapes that decode.
- `Apply` writes into the field's open `Extensions` slot under the
  extension's name. `ir.UpdateExtension` and `ir.GetExtension` are the codec:
  the slot holds `json.RawMessage`, and `fieldExt` is the Go struct the
  extension reads and writes it through. Every acme field decorator is a
  member of `fieldExt`, so adding one is a new member, a new `DecoratorSpec`
  and a new export from `@acme/schema`. That is exactly what
  `scripts/second_decorator.patch` does.

In the IR the payload appears as
`{"name": "sku", ..., "extensions": {"acme": {"shelf": {"aisle": 3, "bay": "B"}}}}`,
in the TypeScript form and the data form alike. `ShelfOf(fd)` is the read
side the generators use.

## A document

Some inputs do not belong in a schema file: deployment values, a region
table, anything a different team edits. A document is a sidecar file the
loader reads from the service directory and stores on the schema:

```go
r.RegisterDocument(registry.DocumentSpec{
	Name:      "catalog.config",
	Extension: Name,
	File:      "catalog.config.yaml",
	Kinds:     []string{"Catalog"},
	Schema:    DocumentSchema,
	Loader: func(_ context.Context, lc registry.LoadContext) (json.RawMessage, []string, error) {
		doc, err := lc.DecodeData("catalog.config.yaml", DocumentSchema)
		return doc, nil, err
	},
	Dirs:     func(c registry.GenerateContext) []string { ... },
	Generate: generateCatalogConfig,
})
```

- `File` is what the loader looks for next to `schema.config.*`; `Kinds`
  says which schemas may carry it. The file next to a DB schema is a load
  error.
- `Loader` turns the file into the JSON stored under
  `Schema.Documents["catalog.config"]`. `lc.DecodeData` reads YAML or JSON
  and validates it against `Schema`. The string slice is the list of extra
  files the document depends on, for `build-all`'s change detection.
- `Generate` receives the raw document alongside the `GenerateContext`. The
  acme generator checks that no `@shelf` names an aisle past the document's
  `aisles` count, which neither a schema nor a JSON Schema could express on
  its own, then writes `config.json`.

The `--emit-ir` output carries the document verbatim under `documents`.

## A build-all hook

Some output needs every service at once. `ext/inventory.go` registers a
hook that `build-all` runs once every service's output is in place:

```go
r.RegisterBuildAllHook(registry.BuildAllHook{
	Name:      "acmeInventory",
	Extension: Name,
	Run: func(_ context.Context, bc registry.BuildAllContext) error {
		for _, service := range bc.Services {
			// read manifest.json from ManifestDir(bc.OutputRoot, service.Name),
			// one of service.OutputDirs
		}
		// write dist/acme/inventory.json
	},
})
```

The hook runs on every `build-all`, including one where every service was
up to date or restored from the build cache and nothing was built. Then
`bc.SchemaFor` has no IR for any service, so the hook reads each manifest
from the service's `OutputDirs`, the directories the cache stores and
restores. A manifest missing there fails the hook instead of leaving a
service out of the inventory. The smoke deletes `dist`, restores all four
services from the cache, and checks the inventory is the same.

## An auth provider

The `api` generator renders the generated middleware and routes through a
set of named template snippets, and the naming file's `auth_provider` picks
which provider supplies them. The core ships `session`. `ext/auth` ships
`apikey`, which the acme `superschematic.toml` selects.

`registry.AuthProvider` is:

| Method | What acme does |
|---|---|
| `Name()` | `"apikey"` |
| `Analyze(api, upstream)` | `registry.AnalyzeSessionStores(upstream)` for the core `User` probe, plus `registry.HasTable(upstream, "ApiKey", "id", "secret", "user")` into `AuthModel.Extra` |
| `Endpoint(field, set, info)` | nothing; API keys scope no endpoint |
| `Templates()` | the embedded `templates/auth_snippets.tmpl` |
| `Funcs()` | nil |
| `Files(out)` | none; every addition is a snippet |
| `OpenAPIParameters(out)` | the `X-API-Key` header parameter on every authenticated route |

`Analyze` runs against the schema `authDb` names in `schema.config`. It
reports which stores the upstream DB can back, and the snippets branch on
the model, so the generated code always compiles against the ORM it is
given. `Config.APIKeys KeyStore` is always required; with an `ApiKey` table
the module also generates `NewKeyStore(db)` over the ORM, without one the
caller supplies its own.

The snippet file defines one `{{ define }}` per hook point the core
templates call: `contextImports`, `contextAuth`, `middlewareStdImports`,
`middlewareImports`, `middlewareAliases`, `middlewareAuthz`,
`middlewareStores`, `routesImports`, `routesConfigStores`,
`routesConfigMiddlewares`, `routesConfigValidate`, `routesConfigExample`,
`routesSetupPre`, `routesSetup`, `routesProtectedMiddleware`,
`routePermissions`, `moduleRequires`, `moduleReplaces`. The api generator
checks the set is complete when it parses the templates, before it renders
a file; `registry.AuthSnippetFunc(provider)` in a provider test
(`ext/auth/provider_test.go`) catches a missing one at `go test`. The acme
snippets build on `runtime/http/go/session`
(`Principal`, `PrincipalStore`, `RequirePermissions`, `ErrNotFound`) and add
`APIKeyMiddleware`, which reads the header, resolves it through the key
store to a principal id, loads the principal and puts it on the request
context.

The smoke `go build`s the generated `shop-api` module to prove the result
compiles. It also builds `shop-db` and `shop-api` with the core-only binary
and `auth_provider = "session"` and compiles that too. Writing this example
is how the session provider's stores were found not to compile against a
DB with a `User` table; the fix is in the core with a test, and the smoke
keeps it fixed.

## Policy on core decorators

`@docs` on an operation and `@icon` on a field are core decorators: the
core checks their shape, writes them to the IR, and puts an operation's
record in the OpenAPI document under `x-superschematic-docs`. acme adds its
rules in `ext/docs.go` without a core option; `acmeIcons` is a second check
like the one below, over every type's fields:

```go
r.RegisterCheck(registry.CheckSpec{
	Name:      "acmeDocsAudience",
	Extension: Name,
	Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
		// report a @docs audience that is not "shoppers" or "staff"
	},
})
r.RegisterOpenAPIHook(registry.OpenAPIHook{
	Name:      "acmeDocsKey",
	Extension: Name,
	Edit: func(_ *ir.Schema, doc map[string]any) error {
		// move each operation's registry.OpenAPIDocsKey entry to x-acme-docs
		return nil
	},
})
```

The check runs after the core checks on every schema the binary loads,
core kinds included, in every authoring form. The hook edits the OpenAPI
document the api generator builds before it is written. `shop-api` declares
`@docs` on two operations and `shop-config` declares field `@docs`,
`@purpose` and `@icon`; `ext/docs_test.go` checks the record lands under
`x-acme-docs` and that an audience or icon outside acme's sets fails the
load while the core registry accepts it. The smoke checks the generated
`openapi.json` from both binaries and the field presentation in the IR.

`@mcp` is a core decorator too: an operation opts in as a visible MCP tool
with a handle, or says why it is hidden, and the core publishes only the
visible ones. Which APIs must classify every operation is acme's rule, a
check in `ext/mcp.go` over API schemas:

```go
r.RegisterCheck(registry.CheckSpec{
	Name:      "acmeToolsClassified",
	Extension: Name,
	Kinds:     []string{string(ir.SchemaKindAPI)},
	Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
		// in shop-api, report an operation without @mcp
	},
})
```

`shop-api` classifies its three operations: `getProduct` and
`createProduct` are visible tools with an `@icon` from acme's set,
`listProducts` is hidden with a reason. `acmeIcons` covers operation
icons as well as field icons.

The SDK generators write the tools of `shop-api` to `tools/schema.json`,
`tools/mcp-audit.json` and the provider lists. How those documents spell
their vendor keys, and which variant of acme's icon set a tool's icon is
drawn from, is a tool hook in the same file:

```go
r.RegisterToolHook(registry.ToolHook{
	Name:      "acmeTools",
	Extension: Name,
	Edit: func(_ *ir.Schema, tools *registry.ToolSet) error {
		tools.Keys.Scalar = "x-acme-scalar"
		tools.Keys.Guidance = "acme/operation-guidance"
		tools.Keys.Parameters = append(tools.Keys.Parameters, registry.ToolKeyValue{Key: "x-acme-arguments", Value: 1})
		// set Icon.Family and Icon.Style on every tool icon
		return nil
	},
})
```

A visible tool's invocation policy says whether a client runs it when a
model calls it or asks the person first. The core spells it
`invocationPolicy`, `"auto"` or `"ask"`. acme spells it its own way, in
the same file:

```go
r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{
	Extension: Name,
	Key:       "confirm",
	Values:    []string{"never", "always"},
	Default:   "never",
})
```

Under acme, `@mcp({ handle: "approve_return", confirm: "always" })` is how
a tool asks first; a tool without `confirm` gets `"never"`, and the IR and
every tool document write `confirm` where the core writes
`invocationPolicy`. The loader type-checks schema files, so the key must
type-check too: `packages/schema/src/mcp.ts` adds it to the core's
`MCPToolOptions` by module augmentation. An API schema cannot import
`@acme/schema`, whose decorators are for Catalog schemas, so an API
service lists that file in its `tsconfig.json`.

`shop-api` does not declare `confirm`: the core-only binary builds
`shop-api` in the smoke, and it rejects the key. Its tools get acme's
default. `ext/testdata/services/returns-api` is the API that declares it,
and the tests load and generate it.

`ext/mcp_test.go` checks that an unclassified `shop-api` operation fails
the load while another API and the core registry accept it, that the
tool documents carry acme's keys and icon variant while the core registry
writes the core's, and that `confirm` reaches the IR and the tool
documents, fails with a value acme does not list, and is unknown to the
core registry. The smoke checks the classification and the policy in the
IR and the tool documents from both binaries.

## A command

`cli.New` returns the root cobra command with `build`, `build-all`,
`json-schema` and `format`. An extension that implements
`cli.CommandProvider` contributes more:

```go
func (Extension) Commands() []*cobra.Command {
	return []*cobra.Command{describeCommand(), fieldsCommand()}
}
```

`describe [<schemas-root>]` assembles the registry the way `build` does
(`registry.LoadNaming` on the root, then `registry.Assemble(names,
Extension{})`) and prints every kind with its pipeline, every document,
every output key, every auth provider and the tool invocation policy. It
is the first thing to run when a schema is rejected: it shows what the
binary knows.

`fields <file.d.ts> <type>` is a command on TypeScript the schema frontend
does not walk. It hands the file, and the other `.d.ts` files next to it,
to `loader.NewDeclarationProgram`, which type-checks them in memory with
the compiler, lib files and module resolution the loader uses and reads
nothing else from disk. It fails on any diagnostic, located as
`file:line:col`, then prints each property of `<type>` with the type the
checker gives it:

```sh
$ acme-schematic fields labels/shelf-label.d.ts ShelfLabel
sku: string
price: number
currency: Currency
location: Location
promo: string | undefined
```

## The binary

```go
root := cli.New(cli.Config{
	Name:  "acme-schematic",
	Short: "Generate code from the acme schemas",
}, ext.Extension{})
```

That is the whole of `cmd/acme-schematic/main.go`. The core assembles a
fresh registry per command from the naming file the command resolves, with
every extension passed here; `cmd/superschematic` is the same call with no
extension.

## Naming

`schemas/superschematic.toml` is read from the schemas root by every
command. The acme file sets `go_module_root`, `npm_scope`,
`python_types_module_prefix`, `python_sdk_module_prefix`,
`python_sdk_module_suffix`, `rust_crate_prefix`, `package_author`,
`auth_provider`, `authoring_packages`, the `[paths]` and `[deps]` tables
and `[extension.acme]`. `[deps] copy` makes `build-all` also write the
dependency graph of the generated packages to `schemas/deps.json`, which is
committed; each package in it names the service that produced it, and the
smoke fails when the committed copy is stale. A key left out keeps the default from the naming file
at the repository root; the docs site's naming reference lists every key.
