---
title: Write an extension
description: Add a kind, decorator, document, generator, auth provider or command without editing the core, walking through examples/acme-schematic.
sidebar:
  order: 1
---

An extension is a Go package that implements `registry.Extension` and,
optionally, `cli.CommandProvider`. You pass it to `cli.New`. The core
binary (`cmd/superschematic`) passes none. Everything project-specific
registers here: kinds, decorators, documents, generators, build-all
hooks, auth providers, checks, OpenAPI hooks and extra commands.

`examples/acme-schematic` is the acceptance test of this model. It adds one
of each surface without editing a file under the core, and
`scripts/check_second_decorator.sh` proves that adding one more decorator
stays that way. This page walks those surfaces. When this page and the
example disagree, the example is the authority.

The design behind these surfaces, including what `Finalize` checks, how
the data forms validate extension slots, and the known gaps, is
[docs/extension-model.md](https://github.com/parable-work/superschematic/blob/main/docs/extension-model.md)
in the repository.

## The binary

```go
root := cli.New(cli.Config{
    Name:  "acme-schematic",
    Short: "Generate code from the acme schemas",
}, ext.Extension{})
```

That is the whole of `cmd/acme-schematic/main.go`. Every command assembles
a fresh registry from the naming file it resolves, with every extension
passed here.

## The extension type

```go
type Extension struct{}

func (Extension) Name() string { return "acme" }

func (Extension) Register(r *registry.Registry) error {
    cfg, err := decodeConfig(r.ExtensionConfig(Name))
    // one register* call per surface
    return r.RegisterAuthProvider(auth.Provider{})
}
```

`Name()` is the key of the extension's slot everywhere: `extensions.acme`
on every IR node, `[extension.acme]` in `superschematic.toml`, and the
`Extension` field of every spec it registers. `Register` runs once per
registry assembly, before `Finalize` checks the result (a generator naming
a kind nobody registered, two decorators with the same name, a document on
an unknown kind are assembly errors).

`r.ExtensionConfig("acme")` returns the `[extension.acme]` table of the
naming file undecoded. The core does not know the keys; the extension
validates them. A typo in the toml fails the build instead of being
ignored.

Import the public packages only: `registry`, `cli` and `ir`, plus
`loader` and `schemadeps` when a command needs them. Nothing under
`internal/` is reachable from outside the core module.

## A kind

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
- `Pipeline` names the generators that run, in order. `types` is the core
  type generator; `catalog` is registered by the extension.
- `AllowedReferences` says which other kinds this kind may import types
  from.

A schema declares the kind with `kind: "Catalog"` in `schema.config.ts`.
`SchemaKind` is a closed enum of the core three; `defineConfig` also takes
any string, and the registry validates it. The core-only binary rejects
the file with `unknown kind "Catalog"`.

`KindSpec.Verify` runs after load. `KindSpec.NoSentinel` skips the
`service.generated.ts` file when the kind is not something other services
import. See [platform](/superschematic/guides/platform/) for both.

## A generator

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
  the generator on. `OutputSchema` is the JSON Schema of that block.
  `registry.DecodeOutput` reads it in `Enabled`.
- `Dirs` lists every directory `Generate` writes to, so `build` can clean
  stale output and `build-all` can compute what changed.
- `Generate` gets the loaded `ir.Schema`, the config, the naming and the
  output root in one `GenerateContext`, writes its files and calls
  `c.Done(key, dir)`.

A generator on kinds the extension did not define lists those kinds and
omits `OutputKey`. It then runs after every listed kind's own pipeline and
cannot be switched off from a schema config. acme's `acmeManifest` is that
shape.

## A decorator

The TypeScript half is a no-op at run time so an author gets completion
and tsc rejects a bad argument. The Go half registers the meaning:

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
  Registering a decorator from it makes that package an authoring package.
- `Target` is the node it may sit on (type, field, operation set,
  operation). `Kinds` restricts it to schema kinds.
- `Args` is the JSON Schema of the argument list. Both frontends validate
  a use against it before `Apply` runs.
- `Apply` writes into the node's open `Extensions` slot under the
  extension's name. `ir.UpdateExtension` / `ir.GetExtension` are the
  codec.

## A document

A document is a sidecar file the loader reads from the service directory
and stores on the schema. Deployment values, a region table, anything a
different team edits:

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

- `File` is what the loader looks for next to `schema.config.*`. `Kinds`
  says which schemas may carry it.
- `Loader` turns the file into the JSON stored under
  `Schema.Documents["catalog.config"]`. The string slice is extra files
  the document depends on, for `build-all`'s change detection.
- `Generate` receives the raw document alongside the `GenerateContext`.

The [deploy](/superschematic/guides/deploy/) extension is a document with
no new kind.

## A build-all hook

A hook is for output that needs every service at once, such as values
merged across services into one file. `build-all` runs the hooks in
registration order once every service's output is in place, and names
the hook in any error it returns:

```go
r.RegisterBuildAllHook(registry.BuildAllHook{
    Name:      "acmeInventory",
    Extension: Name,
    Run: func(ctx context.Context, bc registry.BuildAllContext) error {
        for _, service := range bc.Services {
            // service.Name, .Kind, .Dir, .OutputDirs
        }
        return nil
    },
})
```

Hooks run on every `build-all`, including one where every service was
up to date or restored from the cache and nothing was built. `SchemaFor`
returns the IR only of services this process loaded, so a hook that
must see every service reads its files from `Services[i].OutputDirs`:
the directories the service's documents and generators write, which
the cache stores and restores. acme's `acmeInventory` merges every
service's manifest that way. `build` of one service runs no hooks.

## An auth provider

The `api` generator renders middleware and routes through named template
snippets. `auth_provider` in the naming file picks which provider supplies
them. The core ships `session`. acme ships `apikey`.

`registry.AuthProvider` is:

| Method | Role |
| --- | --- |
| `Name()` | the value `auth_provider` selects |
| `Analyze(api, upstream)` | which stores the `authDb` schema can back |
| `Endpoint(field, set, info)` | per-route scope (acme does nothing) |
| `Templates()` | one `{{ define }}` per hook the core templates call |
| `Funcs()` | extra template functions |
| `Files(out)` | extra files to write (optional) |
| `OpenAPIParameters(out)` | parameters on authenticated routes |

`Analyze` runs against the schema `authDb` names. The snippets branch on
the model so the generated code compiles against the ORM it is given.
`registry.AuthSnippetFunc(provider)` in a provider test catches a missing
snippet at `go test`. The api generator checks the set is complete when it
parses the templates.

## Policy on what the core writes

Some features are core mechanism with a distribution-specific rule on top:
a core decorator writes a typed IR field, and the extension decides which
values it accepts, or how an emitted document names its vendor keys. The
core has no option for such a rule; the extension registers it.

A check runs on every loaded schema of the kinds it lists, core kinds
included, after the core checks and the kind's own `Verify`, in every
frontend:

```go
r.RegisterCheck(registry.CheckSpec{
    Name:      "permissions",
    Extension: Name,
    Kinds:     []string{"API"}, // nil: every kind
    Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
        for _, set := range schema.OperationSets {
            for _, op := range set.Operations {
                if len(op.Permissions) == 0 && !op.Public {
                    rep.Errorf("", "%s.%s needs @requirePermission", set.Name, op.Name)
                }
            }
        }
    },
})
```

An OpenAPI hook edits the document the `api` generator builds, before it is
written to `openapi.json` and embedded in `openapi.go`. It gets the document
as decoded JSON (`map[string]any`, `[]any`, `json.Number`). Hooks run in
registration order:

```go
r.RegisterOpenAPIHook(registry.OpenAPIHook{
    Name:      "owner",
    Extension: Name,
    Edit: func(schema *ir.Schema, doc map[string]any) error {
        doc["x-acme-owner"] = schema.Name
        return nil
    },
})
```

acme uses both on the core documentation decorators (`ext/docs.go`):
checks that accept only its own `@docs` audiences and `@icon` names, and a
hook that moves each operation's `@docs` record from
`registry.OpenAPIDocsKey` to `x-acme-docs`. See
[Documentation decorators](/superschematic/reference/documentation/). A
check also carries the rule of which APIs must classify every operation
for MCP: acme's `acmeToolsClassified` (`ext/mcp.go`) requires `@mcp` on
each operation of `shop-api`. See [MCP tools](/superschematic/reference/mcp-tools/).

## A command

`cli.New` returns `build`, `build-all`, `json-schema` and `format`. An
extension that implements `cli.CommandProvider` contributes more:

```go
func (Extension) Commands() []*cobra.Command {
    return []*cobra.Command{describeCommand(), fieldsCommand()}
}
```

acme's `describe [<schemas-root>]` assembles the registry the way `build`
does and prints every kind, document, output key and auth provider. Run it
when a schema is rejected: it shows what the binary knows.

A command that works on built output, such as one that pins consumers to
generated packages, reads the dependency graph `build-all` wrote with the
public `schemadeps` package: `Read` and `Closure` for a consumer's
packages, `Package.Service` for the service that produced each one,
`SyncCopy` to check or refresh the committed copy that `[deps] copy`
names, and `WriteFileAtomic` for the files it rewrites.
`Example_pinCommand` in `schemadeps/example_test.go` is such a command in
miniature.

## TypeScript declarations outside a schema

The schema frontend walks `.schema.ts` files into the IR. A command or
tool that needs other TypeScript types checked, such as a value contract
declared in a `.d.ts` file, uses `loader.NewDeclarationProgram`: the same
compiler, bundled lib files and module resolution, over files it passes
in memory.

```go
program, err := loader.NewDeclarationProgram(loader.DeclarationInput{
    Files: map[string]string{
        "label.d.ts":                    source,
        "node_modules/money/index.d.ts": moneyTypes, // import "money"
    },
    Roots: []string{"label.d.ts"},
    Lib:   []string{"ES2023"}, // the default
})
if err != nil {
    return err
}
defer program.Close()
if diags := program.Diagnostics(); len(diags) > 0 {
    return diags // file:line:col: message, names as in Files
}
checker := program.Checker()
file := program.SourceFile("label.d.ts")
```

- Nothing is read from disk but the compiler's lib files. A relative
  import resolves between the files, and a bare import resolves under
  `node_modules/` in them.
- The compiler options are fixed: strict, `skipLibCheck` off, no emit,
  bundler module resolution, no automatic `@types`. No `tsconfig.json`
  is read.
- `Diagnostics(names...)` reports only the named files. Leave out a file
  whose declarations you trust; they still resolve.
- `Checker()` and `SourceFile()` return the pinned compiler's types
  (`github.com/microsoft/typescript-go/shim/checker` and `.../ast`); import
  the shim to name a node kind. `ErrorAt(node, ...)` makes a located error
  in the same form as the diagnostics.
- A program is for one goroutine. `Close` releases the checker.

acme's `fields <file.d.ts> <type>` type-checks a declaration file and
prints the checked type of each field (`ext/fields.go`).

## Run the example

From the repository root, after `make setup`:

```
examples/acme-schematic/scripts/smoke.sh
examples/acme-schematic/scripts/check_second_decorator.sh
```

Both are the `acme` job in `.github/workflows/ci.yml`. The README in
`examples/acme-schematic` walks each file.
