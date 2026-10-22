---
title: Write an extension
description: Add a kind, decorator, document, generator, auth provider, behavior, deploy target or command without editing the core, walking through examples/acme-schematic.
sidebar:
  order: 1
---

An extension is a Go package that implements `registry.Extension` and,
optionally, `cli.CommandProvider`. You pass it to `cli.New`. The installed
binary (`cmd/superschematic`) passes the official ones (the gcp target, the
Cloudflare DNS platform and target, and the Pulumi provisioner), and the core alone
(`internal/cmd/superschematic-core`) passes none. Everything project-specific
registers here: kinds, decorators, scalar catalogs, documents, generators,
build-all hooks, auth providers, checks, OpenAPI hooks, tool hooks,
behaviors, deploy targets and extra commands.

`examples/acme-schematic` is the acceptance test of this model. It adds one
of each surface but the deploy ones, which the official extensions show,
without editing a file under the core, and
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

### Share the build cache across checkouts

`build-all --cache` keys every entry on a hash of the running binary, so
a rebuilt binary never reuses what the previous one stored. Binaries
built in two checkouts usually differ, even with
`-trimpath -buildvcs=false`: when `CGO_LDFLAGS` names each checkout's own
superscalar archive, as `scripts/superscalar-dep.sh --print` does, the Go
build ID hashes that path, and on macOS the `LC_UUID` differs even with
an empty build ID. Two worktrees of one repository then never share cache
hits.

Set `cli.Config.ToolDigest` to share them. The digest replaces the binary
hash in every cache key and stamp; the schema, naming and toolchain parts
of the key still apply. It must change whenever anything that shapes the
generated output changes: your extension's sources, the templates and
data they embed, and the superschematic version you link. Embed it at
build time. One way is a file at your module root, since `//go:embed`
reaches only files at or below its package's directory:

```go
// Package schematic sits at the module root, next to go.mod.
package schematic

import (
    "crypto/sha256"
    "embed"
    "encoding/hex"
    "fmt"
    "io/fs"
)

//go:embed go.mod go.sum ext
var sources embed.FS

// SourceDigest hashes the path and contents of every embedded file.
func SourceDigest() string {
    h := sha256.New()
    err := fs.WalkDir(sources, ".", func(path string, d fs.DirEntry, err error) error {
        if err != nil || d.IsDir() {
            return err
        }
        data, err := sources.ReadFile(path)
        if err != nil {
            return err
        }
        _, _ = fmt.Fprintf(h, "%s %d\n", path, len(data))
        _, _ = h.Write(data)
        return nil
    })
    if err != nil {
        panic(err)
    }
    return hex.EncodeToString(h.Sum(nil))
}
```

```go
root := cli.New(cli.Config{
    Name:       "acme-schematic",
    ToolDigest: schematic.SourceDigest(),
}, ext.Extension{})
```

`go.sum` pins superschematic by content hash when you require a version.
A `replace` to a local directory has no `go.sum` line, so the digest must
cover that directory some other way, or stay unset. A digest that misses
a changed input hands out entries other code generated. Unset is always
correct: it keeps the binary hash. acme leaves it unset for that reason,
since it links the core through a `replace` to this repository.

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
on every IR node, `[extension.acme]` in `superschematic.toml`, the
`Extension` field of every spec it registers, and the `acme.` prefix of
its behaviors' names. It may not contain a dot. `Register` runs once per
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
`SchemaKind` is a closed enum of the core four; `defineConfig` also takes
any string, and the registry validates it. The core-only binary rejects
the file with `unknown kind "Catalog"`.

`KindSpec.Verify` runs after load. `KindSpec.NoSentinel` skips the
`service.generated.ts` file when the kind is not something other services
import. The core's Stack kind registers through the same `KindSpec` and
sets both (`internal/registry/core_stack.go`): its `Verify` checks that
every class declares exactly one of `@stack`, `@server`, `@database` and
`@environment`, and no service imports a stack, so it gets no sentinel.

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
  the generator on. `OutputSchema` is the JSON Schema of that block; the
  registry validates the block against it before any generator runs, in
  every config form. `registry.DecodeOutput` reads it in `Enabled`.
- `Dirs` lists every directory `Generate` writes to, so `build` can clean
  stale output and `build-all` can compute what changed.
- `Generate` gets the loaded `ir.Schema`, the config, the naming and the
  output root in one `GenerateContext`, writes its files and calls
  `c.Done(key, dir)`.
- A generator that renders field types reads a field's list depth from
  `TypeRef.ArrayDepth()` (0, 1 or 2), not from `IsArray` alone, or it
  renders a list of lists as a flat list. See
  [Arrays of arrays](/superschematic/reference/arrays-of-arrays/#reading-the-ir).

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
  operation). `Kinds` restricts it to schema kinds. A schema whose kind
  no decorator of a package allows cannot import that package: verify
  rejects the import, so a kind's own authoring package stays out of the
  others without their naming it.
- `Args` is the JSON Schema of the argument list. Both frontends validate
  a use against it before `Apply` runs.
- `Apply` writes into the node's open `Extensions` slot under the
  extension's name. `ir.UpdateExtension` / `ir.GetExtension` are the
  codec.

A per-field directive that only your distribution reads is a decorator
like this one; the core IR has no field for it (D18 in
`docs/DECISIONS.md`). A flag is a spec without `Args`: a schema writes it
bare (acme's `@feedKey`) and the data forms write `"feedKey": true` under
`extensions.acme`. `format` carries the slot from TypeScript to JSON and
YAML and between the two; the TypeScript writer cannot render extension
data yet.

### A class as an argument

An argument can name a class as a value: the class itself, never its name
in a string. Acme's `@crossSell` offers a type beside another class's
listing:

```ts
export type SchemaClass = abstract new (...args: never[]) => unknown;

export function crossSell(_args: { readonly with: SchemaClass }): ClassDecorator {
  return () => {};
}

@crossSell({ with: Product })
export abstract class Bundle {
  code: Identity.Slug;
}
```

The loader reads a class in any decorator's argument, so this takes no
core edit. `Apply` gets the class reference `{"class": "Product"}`, which
holds the class's declared name, and the data forms write the same object:

```yaml
extensions:
  acme:
    crossSell: { with: { class: Product } }
```

The Go half uses two registry names:

```go
var CrossSellArgs = json.RawMessage(`{
	"type": "object",
	"required": ["with"],
	"additionalProperties": false,
	"properties": {"with": ` + string(registry.ClassRefSchema) + `}
}`)

type CrossSell struct {
	With registry.ClassRef `json:"with"`
}
```

- `registry.ClassRefSchema` is the JSON Schema of a class reference. Use it
  where your `Args` takes a class, so a string there fails in both forms.
- `registry.ClassRef` is the Go type, for the struct `registry.DecodeArgs`
  fills and for your codec.
- A third, `registry.DecodeClassRef(v)`, reads one class reference, such as
  a whole argument (`@pairsWith(Accessory)`), into the class's name.

A class from another service's package is recorded in the schema's
imports under that package, as a field type from it is, so the schema
config must declare that service as a dependency. A class the schema
neither declares nor imports fails the load, in every form, and the error
names it. An object whose only key is `class` is always read as a class
reference, so don't give other data that shape.

### A service as an argument

An argument can take another service's handle: its sentinel, imported
from the service's package as a config imports one, or
`service({ name, kind })`. `Apply` gets `{"name": ..., "kind": ...}`, and
the data forms write the same object. The handle references the service:
the IR lists it under `references`, and `build-all --cache` rebuilds your
schema when that service, or one its config reaches, changes. It does not
order the build. The data forms state the list themselves, as they state
`imports`:

```yaml
references:
  - { name: shop-db, kind: DB }
```

A handle that only names a service, such as a list of callers, is an
identity. Declare where your argument holds them with `Identities`, paths
of object keys from the argument (`"from"`, `"settings.of"`):

```go
r.RegisterDecorator(registry.DecoratorSpec{
    Name: "admits", Extension: "acme", Packages: []string{"@acme/schematic"},
    Target: registry.TargetType, Args: AdmitsArgs,
    Identities: []string{"from"},
    Apply: applyAdmits,
})
```

An identity adds no edge: an edit to the named service leaves your schema
cached, and two services may name each other. Only a change to the
service's name or kind rebuilds it. D41 in `docs/DECISIONS.md` has the
reasons.

## A scalar catalog

The loader hydrates every scalar a schema names from the registry's scalar
catalog, superscalar's core set unless an extension registers another with
`RegisterScalars`. A distribution whose scalar package adds names registers
its rows. A superscalar row has no upload fields, so a file-upload scalar is
declared beside it:

```go
catalog, err := registry.ScalarCatalogWithUploads(registry.ScalarCatalogOf(rows), map[string]registry.ScalarUpload{
    "Acme.Photo": {FileUpload: ir.FileUploadConfig{
        MaxSize:      8 << 20,
        AllowedTypes: []string{"image/jpeg", "image/png", "image/webp"},
        Category:     "image",
    }},
})
if err != nil {
    return err
}
return r.RegisterScalars(Name, catalog)
```

- `rows` holds every scalar the schemas may use: acme copies the core rows
  from `registry.CoreScalars()` and adds its own.
- A row whose `Primitive` the loader reads as the object language primitive
  (`Object`, `JSON`, `Type`, `jsonb`, or a spelling it does not know) needs
  a `JSONSchemaType` of `object`, `array` or `any`, which becomes the
  scalar's `json_schema` mapping and says what JSON it holds, and with
  `object` or `array`, no pattern and no length, which are rules on a
  string. `RegisterScalars` refuses a catalog with a row that does not,
  and names the row; for an unknown spelling, it names the primitive and
  the spellings it may have meant
  ([JSON-valued scalars](/superschematic/reference/json-scalars/#a-scalar-of-your-own-that-holds-json)).
- A field of an upload scalar is a multipart file part in the generated
  APIs and SDKs. `Validate<Acme.Photo, { uploadMaxBytes: 2097152 }>` lowers
  the limit for one field; the loader checks it after hydration, so it
  needs the upload declared here. An upload row's `Primitive` is `String`:
  `RegisterScalars` refuses an upload scalar whose primitive reads as
  object.
- The TypeScript brand (`string & { readonly __brand: "Acme.Photo" }`) can
  live in the extension's authoring package, as acme's does. Name that
  package for the brand's namespace, so `format --to=ts` imports the
  namespace from it and not from superscalar:

  ```go
  named, err := registry.ScalarCatalogWithNpmPackages(catalog, map[string]string{"Acme": "@acme/schema"})
  if err != nil {
      return err
  }
  return r.RegisterScalars(Name, named)
  ```

  The key is a namespace, the part of a scalar's name before its first
  dot. Without it, `format --to=ts` refuses a schema with an `Acme`
  scalar, since an import of `Acme` from superscalar would not compile.
  The wrapper composes with the other two in any order.

A scalar whose decode loses something a write must refuse, such as a
repeated key in a JSON object, can name a raw-body check: a Go function the
generated Go routes call on the raw JSON body before they decode it.

```go
catalog, err := registry.ScalarCatalogWithRawBodyChecks(catalog, map[string]registry.ScalarRawBodyCheck{
    "Generic.JSON": {
        ImportPath:  "example.com/checks/jsonkeys",
        PackageName: "jsonkeys",
        Func:        "DuplicateKeyErrors",
    },
})
```

- The function has the signature
  `func(body []byte, fields ...string) ValidationErrors` and reports each
  error at its path under the field it names.
- A route whose input type has single-valued top-level fields of the scalar
  calls it with their wire names and answers 400 when it reports errors. It
  runs in every place the route decodes the input from JSON, including the
  `data` part of a multipart request.
- It composes with `ScalarCatalogWithUploads` in either order. Section 3.10
  of `docs/extension-model.md` and D24 in `docs/DECISIONS.md` have the
  details.

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

A document with `Kinds` left nil may sit next to any schema, so an
extension can be a document and nothing else. `Schema` checks the file's
shape at load time; a check that needs the schema too, such as every key
naming a field of the `@envVars` class, belongs in `Generate`, which
receives both. For deployment values, a [stack](/superschematic/guides/stacks/)
already sets each server's `@envVars` from its environments' settings
and derives its connection strings, so a document need not restate them.

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

An OpenAPI hook edits the document the `api` generator builds, before
every server's build writes it to `openapi.json` (and the Go server embeds
it in `openapi.go`, the Rust server in `src/openapi.rs`). It gets the document
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
each operation of `shop-api`.

A tool hook edits what the SDK generators publish about an API's MCP
tools: the vendor keys of the tool documents and each operation's
resolved `@mcp` record. Hooks run in registration order, once per API
build, and every SDK language reads what they leave:

```go
r.RegisterToolHook(registry.ToolHook{
    Name:      "tools",
    Extension: Name,
    Edit: func(schema *ir.Schema, tools *registry.ToolSet) error {
        tools.Keys.Scalar = "x-acme-scalar"
        tools.Keys.Parameters = append(tools.Keys.Parameters, registry.ToolKeyValue{Key: "x-acme-arguments", Value: 1})
        for _, tool := range tools.Tools {
            if tool.MCP != nil && tool.MCP.Icon != nil {
                tool.MCP.Icon.Family = "acme"
            }
        }
        return nil
    },
})
```

A visible tool's invocation policy, whether an MCP client runs it when a
model calls it or asks the person first, is a core field whose key,
values and default an extension can replace. The core's is
`invocationPolicy`, `"auto"` or `"ask"`, `"auto"` by default. An extension
registers its own; a registry holds one, and two extensions that each
register one fail assembly:

```go
r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{
    Extension: Name,
    Key:       "confirm",
    Values:    []string{"never", "always"},
    Default:   "never",
})
```

`@mcp({ handle, confirm: "always" })` and the data forms' `mcp` record then
take `confirm` with one of those values, a visible tool without it gets
`"never"`, and the IR and every tool document write `confirm` where the
core writes `invocationPolicy`. The core key becomes an unknown key.

The TypeScript loader type-checks schema files, so the key must also
type-check. `@superschematic/api` exports `MCPToolOptions`, the options a
visible tool's `@mcp` takes besides its handle and `_meta`, for module
augmentation. The extension's authoring package adds its key:

```ts
import "@superschematic/api";

declare module "@superschematic/api" {
  interface MCPToolOptions {
    readonly confirm?: "never" | "always";
  }
}
```

A program sees the augmentation when it includes that file: through an
import of the authoring package, or a `tsconfig.json` `include` entry.
acme keeps it in `packages/schema/src/mcp.ts`; an API schema cannot import
`@acme/schema`, whose decorators are for Catalog schemas, so an API
service lists the file in its `tsconfig.json`
(`ext/testdata/services/returns-api`).

See [MCP tools](/superschematic/reference/mcp-tools/).

acme also checks the core projection views (`ext/projection_policy.go`): a
check on the DB kind refuses a view whose first `where` rule is not a
required, unconditional binding of the setting `projection_scope_setting`
names in `[extension.acme]`. A policy's settings belong in the extension's
own table, not in the core's naming keys. See
[Projection views](/superschematic/reference/projections/).

## A behavior

A behavior is code that adds fields, operations, checks and storage to a
type when an engine runs the schema. The compiler declares, carries and
checks behaviors; the engine, the Rust library every server links (D58),
runs them. An extension ships both halves: the declaration, registered
with the compiler, and a Rust crate that implements it, which the
project's engine compiles in (below).

An extension declares a behavior in a JSON file beside its Go package,
embeds it and registers it:

```go
//go:embed rating.behavior.json
var ratingDeclaration json.RawMessage

r.RegisterBehavior(registry.BehaviorSpec{Extension: Name, Declaration: ratingDeclaration})
```

```json
{
  "name": "acme.Rating",
  "description": "Shoppers rate a catalog item from one star to the configured maximum.",
  "configSchema": {
    "type": "object",
    "required": ["maxStars"],
    "additionalProperties": false,
    "properties": { "maxStars": { "type": "integer", "minimum": 3, "maximum": 10 } }
  },
  "fields": [
    { "name": "ratingCount", "description": "How many ratings the item has." },
    { "name": "ratingAverage", "description": "The mean rating; 0 before the first." }
  ],
  "operations": [
    {
      "name": "rate",
      "description": "Records one shopper's rating of the item.",
      "paramsSchema": {
        "type": "object",
        "required": ["stars"],
        "additionalProperties": false,
        "properties": { "stars": { "type": "integer", "minimum": 1 } }
      },
      "resultSchema": { "type": "object", "properties": { "ratingCount": { "type": "integer" }, "ratingAverage": { "type": "number" } } },
      "writes": true,
      "invocationPolicy": "always"
    }
  ]
}
```

- `name` is `<extension>.<Name>`, with the extension's own `Name()` as
  the prefix. Only the core declares bare names.
- `description` says what the behavior adds; the describe document
  shows it.
- `configSchema` is the JSON Schema of what a type passes; leave it out
  for a behavior that takes no config.
- `createParamsSchema`, optional, is the JSON Schema of the parameters a
  create gives the behavior for the new instance, which its
  `initialize` gets: an object schema whose `additionalProperties` is
  `false` or a schema, so every key is checked.
- `requires` and `conflicts` name other behaviors a type must, or may not,
  list with this one.
- `fields` carry a name and a description, each unique in the
  declaration. An instance holds them under the behavior's name,
  `behaviors["acme.Rating"].ratingCount`, apart from the type's own
  fields and every other behavior's, so none collides, and a filter or a
  display names one by its qualified name, `acme.Rating.ratingCount`.
  A config never renames a field. A field the engine computes at a read
  from other instances, the clock or the config says `"volatile": true`,
  since no event of its instance records its moves.
- `operations` are camelCase and may not be `create`, `get`, `list`,
  `update` or `delete`, which every schema has. `paramsSchema` is an
  object schema that sets `"additionalProperties": false`, so an
  operation's parameters are exactly the ones it declares; the registry
  and the engine both refuse one that does not.
  `invocationPolicy` is a value of the registry's tool invocation policy;
  left out, the policy's default applies.
- `vetoes` lists the codes the behavior's refusals carry, each
  `{ code, description }` in lowercase snake case, so a client can
  branch on `details.code` of a 409 `vetoed`. The engine refuses a veto
  with a code the declaration does not list.
- `preconditionSchema`, a closed object schema, declares what a caller
  may assert to the behavior with a write: the entry it sends under the
  behavior's name in the preconditions of an update, a delete or an
  operation, which the engine checks and hands to the behavior's guard.
  Lease's is `{ token }`.

Assembly fails on a malformed declaration, and `Finalize` on a
`requires` or `conflicts` name nobody registered or an invocation policy
value the registry's policy does not have.

A type composes behaviors with `@behavior` from `@superschematic/schema`,
which any kind may import. Several on one class apply in source order,
and a type lists a behavior once:

```ts
import { behavior } from "@superschematic/schema";
import { Identity } from "superscalar";

@behavior("acme.Rating", { maxStars: 5 })
export abstract class Product {
  sku: Identity.Slug;
}
```

The data forms write the same list:

```json
"behaviors": [{ "name": "acme.Rating", "config": { "maxStars": 5 } }]
```

The config type comes from `BehaviorConfigs`, which your authoring package
augments, as it augments `MCPToolOptions` for an invocation policy key:

```ts
import "@superschematic/schema";

declare module "@superschematic/schema" {
  interface BehaviorConfigs {
    "acme.Rating": { readonly maxStars: number };
    "acme.Flagged": undefined; // takes no config
  }
}
```

A name no augmentation declares does not type-check. A General schema
cannot import an authoring package whose decorators are for other kinds,
so the service lists the file in its `tsconfig.json` `include`, as acme's
`shop-ratings-ts` does with `packages/schema/src/behaviors.ts`.

The loader checks each entry against its declaration and names the type
and the behavior when it fails; `json-schema` limits the names to the
registered behaviors and holds each config to its schema. The core's
`types` generator renders behaviors: its `<Type>Behaviors` holds your
behavior's fields under its exact name, typed as any JSON, since a
declaration names a field without a schema, and in Go, Rust and Python
under the name in PascalCase (`acme.Rating` is `AcmeRating`). The DB and
General pipelines build such a schema: on a DB type a behavior makes an
engine type, which `sql` and `orm` leave out and the `enginedoc`
generator writes into the engine document, with your behavior and the
crate its `BehaviorSpec`'s `Rust` names
([Engine types](/superschematic/guides/engine-behaviors/#engine-types)).
An API's own type cannot compose one, and the API generators refuse an
API that uses an engine type, naming themselves. A generator of yours
whose output covers what behaviors add, or holds nothing a type's
behaviors add, as acme's manifest lists type names, sets
`GeneratorSpec.RendersBehaviors`; without it, a build of such a schema
that runs it fails and names it. acme's `shop-ratings` and
`shop-ratings-ts` services (`ext/testdata/services`) compose
`acme.Rating`.

### The implementation

The engine runs a Rust type that implements its `Behavior` trait over the
same declaration. The crate lives in your project, as a Cargo workspace of
its own with its own lock file, and depends on `superschematic-engine`
without its database features, which the project's engine picks. Copy
the declaration into the crate with your binary's `behaviors` command, and
run it with `--check` in CI so the copy cannot fall behind:

```sh
acme-schematic behaviors --extension acme --out crates/behaviors/declarations
acme-schematic behaviors --extension acme --out crates/behaviors/declarations --check
```

The implementation embeds that copy, parses a type's config into the
value its hooks get, and gives the engine a hook for each point of an
instance's life, with a handler and a reader for exactly the operations
and fields the declaration names. acme's `Rating`, in the `acme-behaviors`
crate (`crates/behaviors/src/lib.rs`), whose `RatingConfig`, `Totals`,
`totals`, `summary` and `average` are its own:

```rust
use serde_json::Value;
use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec};
use superschematic_engine::json::number_value;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{Behavior, ConfigTarget, EngineError, Error, Hooks, Result};

pub struct Rating;

impl Behavior for Rating {
    type Config = RatingConfig;

    fn declaration(&self) -> &str {
        include_str!("../declarations/acme.Rating.behavior.json")
    }

    fn parse_config(&self, raw: &Value, _target: &ConfigTarget<'_>) -> Result<RatingConfig> {
        match raw.get("maxStars") {
            Some(Value::Number(max)) => Ok(RatingConfig { max_stars: number_value(max) as i64 }),
            _ => Err(Error::config("maxStars, the highest rating a shopper can give, is required")),
        }
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            config_change: Some(|_, before, after, _| { /* maxStars may rise, not fall */ }),
            migrations: vec![BehaviorMigration::new(1, "rating totals")
                .column("count", ColumnSpec::integer().not_null().default(0))
                .column("stars", ColumnSpec::integer().not_null().default(0))],
            operations: vec![
                ("rate", |_, cx, params| {
                    let stars = match params.get("stars") {
                        Some(Value::Number(stars)) => number_value(stars),
                        _ => 0.0,
                    };
                    if stars > cx.config().max_stars as f64 {
                        return Err(EngineError::invalid_argument(/* ... */).into());
                    }
                    let before = totals(&cx.columns().get()?)?;
                    let after = Totals { count: before.count + 1, stars: before.stars + stars as i64 };
                    cx.columns().set(&[
                        ("count", SqlValue::Integer(after.count)),
                        ("stars", SqlValue::Integer(after.stars)),
                    ])?;
                    Ok(summary(&after))
                }),
                ("ratingSummary", |_, cx, _| Ok(summary(&totals(&cx.columns().get()?)?))),
            ],
            fields: vec![
                ("ratingCount", |_, view| Ok(Some(Value::from(totals(&view.columns().get()?)?.count)))),
                ("ratingAverage", |_, view| Ok(Some(average(&totals(&view.columns().get()?)?)))),
            ],
            ..Hooks::default()
        }
    }
}
```

Every hook is optional and a plain function, runs inside the call's
transaction, and writes only the behavior's own storage: columns the
engine adds to its instances table and names for the behavior
(`cx.columns()`), and tables of its own through `cx.sql().table(name)`.
Its SQL also reads its own columns across every instance of the schema,
with their own fields, through a read-only relation the engine names
(`cx.sql().instances()`), asking the access policy for read as the
caller. A refusal is an `EngineError`, such as `invalid_argument`, or a
veto. A guard may veto an update, a delete or any behavior's operation on
the type with a `Veto`, a reason or `{ reason, code, details }` whose code
the declaration lists, and reads the caller's precondition for its
behavior in `request.precondition()`; another behavior changes this one's
state only by calling its operations, so those guards always run. Where a
config names a permission, the behavior asks `cx.can(permission)`, which
the permission matcher the deployment gives the engine answers. An
operation can change the instance's own fields with `cx.update(patch)`,
which runs the checks and guards of an update. A `validate` hook judges
the fields a create or an update would store and returns issues at their
paths, which refuse the write as `invalid_instance`, as the live
version's own do, before any guard is asked; it never sees a
precondition, which is the guard's to judge. `checked_types` names the
document's types it checks values against, which the compatibility rule
then holds, and `instance_schema` shows clients what it holds the fields
to, under `allOf` in the describe document and the create and update
tools. A behavior that keeps values of the schema's other types in its
own tables reads those types in `parse_config`
(`target.types().get(name)`), which holds them to the schema's checks and
its compatibility rule as a type a field reaches is held, and checks a
value against one with `validate(type, value)` in its contexts.
`guidance` tells an MCP client what the config means: a summary, and for
each operation of the type, the behavior's own and the ones its guard
holds, when to call it and the codes of the vetoes it can meet, which the
engine merges into each tool's guidance, as an SDK tool carries its
`@docs`. `create_params_schema` shows the create parameters the config
takes, narrower than the declaration's. The engine's README ("Behaviors",
and "Guidance" under "Tools") has the whole interface: `parse_config`,
and the hooks `config_change`, `after_config_change`, `migrations`,
`initialize`, `validate`, `checked_types`, `instance_schema`, `guidance`,
`create_params_schema`, `guard`, `operations`, `schema_operations`,
`fields`, `filters`, `after_change`, `guard_reference`,
`after_reference_change`, `reactions` and `schedules`. `config_change`'s
last argument says whether the schema has instances (`instances()`), so a
change only stored ones could break is refused only while there are some.

A behavior written in TypeScript, with `defineBehavior` from
`@superschematic/engine/testing`, runs only on the engine's test addon
(`scripts/engine-napi.sh --js-behaviors`), through its bridge; the
shipped addon and a project's refuse it. It is for an engine's tests and
fixtures, not an extension's implementation.

### The project's engine

Name the crate in the behavior's registration: its Cargo package name,
its directory relative to your project (the parent of the schemas root,
which `[paths]` is relative to as well), and the Rust expression that
registers it:

```go
r.RegisterBehavior(registry.BehaviorSpec{
	Extension: Name,
	Rust: registry.BehaviorRust{
		Crate:    "acme-behaviors",
		Path:     "crates/behaviors",
		Register: "acme_behaviors::Rating",
	},
	Declaration: ratingDeclaration,
})
```

For a DB or General schema whose types compose the behavior, the build's
`projectengine` generator writes the project's engine crate,
`engine/rust/<service>`, named `<rust_crate_prefix><service>-engine`: it
depends on the engine's crates at the `[paths]` keys `engine_rust`,
`engine_behaviors_rust`, `engine_workqueue_rust` and
`engine_commerce_rust`, and on yours at its path, and its `behaviors()`
registers the core's behaviors, the work queue's and the commerce ones
when the schema uses one, then yours; `tools()` carries your
binary's invocation policy and vendor keys, under which an operation that
names your policy's value registers. Each server language takes the
engine from that crate its own way
([The project's engine crate](/superschematic/guides/engine-behaviors/#the-projects-engine-crate)):

- A Rust server links the library and opens the engine with its
  `behaviors()` and `tools()`.
- A Go server links the workspace's `ffi/` member, the engine's C ABI
  over the same `behaviors()`, as a static archive in place of the
  core's (`scripts/engine-archive.sh --crate <crate>/ffi --out <dir>`).
- A TypeScript server loads the workspace's `napi/` member, the engine's
  Node-API addon over them, in place of the core's addon: build it with
  `scripts/engine-napi.sh --crate <crate>/napi --out <dir>`, which prints
  the file it wrote, and name that file with the engine option `addon`
  or, for every engine the process opens, `SUPERSCHEMATIC_ENGINE_ADDON`.

A generated server does this itself: a stack's Dockerfile builds the
member its language takes in its engine stage, and `stack dev` builds it
before it builds or starts the server, so the commands above are for a
host you write by hand.

The host runs your behavior beside the core's: a Rust host passes the
set of both, as the engine crate's `behaviors()` is, and a TypeScript
host names yours in `behaviors`, which the addon registers after the
core's. It passes the meta-schema its binary writes,
which declares the behavior, and the tool invocation policy and vendor
keys its binary registers, which the engine writes its MCP tools with (an
operation's `invocationPolicy` must be one of the policy's values). In
TypeScript, with `addon` the file `scripts/engine-napi.sh` printed and
`tools` the engine document's:

```ts
import { openEngine } from "@superschematic/engine";

const engine = openEngine({ path: "shop.db", policy, addon, metaSchema: acmeJSONSchema, behaviors: ["acme.Rating"], tools });
engine.schemas.define(me, productSchema);   // composes acme.Rating
engine.schemas.publish(me, "Product");       // creates the rating columns
engine.instances.invoke(me, "Product", id, "rate", { stars: 4 });
```

and in Rust, over the core's set and the crate's:

```rust
use superschematic_engine::{Engine, EngineOptions, MetaSchema, Policy};

let mut options = EngineOptions::sqlite("shop.db");
options.policy = Some(Policy::allow_all());
options.meta_schema = Some(MetaSchema::Text(std::fs::read_to_string("acme-schema-file.json")?));
options.behaviors = superschematic_engine_behaviors::core().extend(acme_behaviors::behaviors());
options.tools = acme_behaviors::tools();
let engine = Engine::open(options)?;
```

An engine without the behavior refuses a schema that composes it. The
engine registers the core's own behaviors, `Workflow`, `Comments`,
`Revisions`, `Dependencies`, `Links`, `Rollups`, `Search`, `Reactions`,
`Constants`, `Variants`, `Branches`, `Timers`, `StateRules`, `Numbering`
and `Approvals`, when it opens, and the core's addon also carries the
[work-queue behaviors](/superschematic/guides/work-queues/), which run
once a host names them (`workQueueBehaviors` from
`@superschematic/engine-workqueue`). No core operation names an
invocation policy, so their tools take the default of the policy you
pass. A behavior whose registration names no crate, or a crate the
project does not hold, gets no project engine: the build's log says no
engine can run the schema's behaviors, and an engine refuses the schema.

acme's `crates/behaviors` is the whole example: its tests
(`tests/rating.rs`) run the engine scenario on SQLite and Postgres, and
the smoke builds and tests the engine crate the acme binary writes for
`shop-ratings`, its archive with it, and loads its addon in Node.js,
where an engine on it publishes `shop-ratings`' `Product`, creates one
and rates it. The [engine guide](/superschematic/guides/engine/) serves
three of the core's behaviors in `examples/engine-notes`.

## A deploy target

A stack deploys a schema tree through a target, and the target's resources
through a provisioner (D30 in `docs/DECISIONS.md`; section 6 of
`docs/stack-model.md` is the design). The official extensions are the
examples: `extensions/gcp` registers the `gcp` target with its platforms,
connectors and Cloud DNS platform, `extensions/cloudflare` the
`cloudflare` target with its platforms and connectors and a DNS platform,
and `extensions/pulumi` the provisioner. Six registrations make up the
surface, each spec carrying the extension's `Name()`:

| Registration | Adds |
| --- | --- |
| `RegisterPlatform(registry.PlatformSpec)` | one deployable kind (`server` or `database`) on one runtime, such as Cloud Run or Cloud SQL: the server languages or SQL dialects it runs, the JSON Schema of a deployable's settings there, its name and address in an environment, and a pure `Lower` to its resources |
| `RegisterConnector(registry.ConnectorSpec)` | one edge kind (`sql` or `http`) between two platforms: a pure `Connect` that returns the edge's resources, such as an IAM grant, and the value of the config field the edge derives |
| `RegisterTarget(registry.TargetSpec)` | a platform per deployable kind, the JSON Schema of an environment's values, the default DNS platform, the provisioner, the properties schema of each resource type its platforms emit, and policy rules over the resolved resource graph |
| `RegisterDNSPlatform(registry.DNSPlatformSpec)` | a DNS provider for an environment's domain records, which need not be the target's provider: its values schema, a pure `Lower` from records to resources, the schemas of the resource types it brings, and the credentials its provider reads |
| `RegisterProvisioner(registry.ProvisionerSpec)` | a tool that applies a resolved environment: `Render` writes the tool's program where a person can read it, and `Plan`, `Apply`, `Destroy` and `Outputs` run it; `Tools` lists the command-line tools it runs, which a generated CI job installs |
| `RegisterCIRenderer(registry.CIRendererSpec)` | a CI system a stack's workflow is written for: the directory it installs into and a pure `Render` from the stack, its resolved environments with their CI identities, its options from `outputs.ci` and the release, to files. The core registers `github` |

Resolution checks every resource against the schema of its type, so a
target checks in the provider schemas it emits from a pinned provider
version, as the gcp target does with pulumi-gcp's. A target that deploys
also fills seven seams on its `TargetSpec`, `State`, `Secrets`,
`Bootstrap`, `Migrations`, `Builder`, `CI`, which says how a generated
CI job signs in to an environment, and `Jobs`, which runs a deployed job
on demand, and the core registers one
target of its own, `local`, for `stack dev`.
[Stack targets](/superschematic/extending/stack-targets/) walks each
registration, its seams and its offline tests.

## A command

`cli.New` returns `build`, `build-all`, `migrate`, `json-schema`, `format`
and `behaviors`. An extension that implements `cli.CommandProvider`
contributes more:

```go
func (Extension) Commands() []*cobra.Command {
    return []*cobra.Command{describeCommand(), fieldsCommand()}
}
```

acme's `describe [<schemas-root>]` assembles the registry the way `build`
does and prints every kind, document, output key, auth provider and check,
the tool invocation policy and every behavior. Run it when a schema is rejected: it shows
what the binary knows.

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
