---
title: Write an extension
description: Add a kind, decorator, document, generator, auth provider, behavior, deploy target or command without editing the core, walking through examples/acme-schematic.
sidebar:
  order: 1
---

An extension is a Go package that implements `registry.Extension` and,
optionally, `cli.CommandProvider`. You pass it to `cli.New`. The installed
binary (`cmd/superschematic`) passes the official ones (the gcp target, the
Cloudflare DNS platform and target, the `embedded` target and the Pulumi
provisioner), and the core alone
(`internal/cmd/superschematic-core`) passes none. Everything project-specific
registers here: kinds, decorators, scalar catalogs, documents, generators
and their package directories, build-all hooks, auth providers and
rate-limit keys, checks, OpenAPI hooks, tool hooks, behaviors, runtime
features, deploy targets and extra commands.

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

A generator that writes one package per service, in a directory named
after the service, registers that directory, so `build-all`'s dependency
graph (`dist/.deps.json`, and the naming file's `[deps] copy`) lists its
packages beside the core's:

```go
r.RegisterPackageDir(registry.PackageDir{
    Extension: Name,
    Language:  registry.LangGo, // go.mod; LangRust, LangPython, LangTypeScript
    Dir:       "cache/go",      // cache/go/<service>/go.mod
    Kind:      "cache",
})
```

The graph reads each package's manifest as it reads a core package's of
that language, and gives it the ID `<service>-<Kind>` and the kind `Kind`.
`Kind` is a lower-case word other than the core's `types`, `sdk`, `api`
and `orm`, and `Dir` is a clean relative path; a directory is registered
once. The generator's `Dirs` must include each service's directory there,
since a package no service's output directories hold fails the build.

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
the [decision log](https://github.com/parable-work/superschematic/blob/e8e685a5149a8897001153fb6cca1100ac90d0c0/docs/DECISIONS.md)). A flag is a spec without `Args`: a schema writes it
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
service's name or kind rebuilds it. D41 in the [decision log](https://github.com/parable-work/superschematic/blob/e8e685a5149a8897001153fb6cca1100ac90d0c0/docs/DECISIONS.md) has the
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
  APIs and SDKs, as a field the core's `FileUpload<C>` declares is
  ([Files](/superschematic/guides/api-routes/#files)), which needs no
  scalar of its own. The upload's `ir.FileUploadConfig` holds its limit,
  its media types and its filename extensions (`AllowedExtensions`).
  `Validate<Acme.Photo, { uploadMaxBytes: 2097152 }>` lowers
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
  of `docs/extension-model.md` and D24 in the [decision log](https://github.com/parable-work/superschematic/blob/e8e685a5149a8897001153fb6cca1100ac90d0c0/docs/DECISIONS.md) have the
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

The core registers one hook itself, `tasks-install`, which installs each
Tasks service's task file ([Tasks](/superschematic/guides/tasks/#the-task-file)).
It runs before an extension's.

## Task resolvers and sections

A Tasks service declares the work a commit needs
([Tasks](/superschematic/guides/tasks/)). An extension adds to it in
two places.

**An input resolver** turns one kind of input into tracked files of a
commit. The core has `path` and `schema`, and the official
`extensions/resolvers` has a Go module, a Rust crate, a JS package and
an image, which are the worked examples. A resolver for another
toolchain is yours:

```go
r.RegisterTaskResolver(registry.TaskResolverSpec{
    Kind:      "bazel-target",
    Extension: Name,
    Spec:      json.RawMessage(`{"type":"object","required":["label"],"additionalProperties":false,"properties":{"label":{"type":"string"}}}`),
    Resolve: func(req registry.TaskResolveRequest) (registry.TaskResolution, error) {
        // req.Tree is the commit: Entries, Entry and Read.
        // registry.TaskEntriesUnder and registry.MatchTaskPaths read it
        // by directory and by pattern.
        // Return the tracked files the input covers in Paths, and in
        // Generated each path a manifest names that the commit lacks.
        // Values holds what a tracked file says, where its bytes say
        // more than a build reads: a name and the bytes that stand for
        // it, in place of the file in Paths.
    },
})
```

`Spec` is the JSON Schema of the input's `spec`, which the build and the
plan both hold an input to. `Resolve` is pure: one tree and one spec
give one answer. A resolver feeds the keys a forge reuses work by, and
it runs on a commit somebody else may have written, before anything is
generated. So it reads tracked files only, through `req.Tree`; it runs
no build tool and starts no process; it uses no network; and it executes
nothing the tree holds. `Read` refuses a file over 1 MiB.

A manifest often names a path a build writes, such as a workspace member
under the output root. Report it in `Generated` with the manifest that
names it, and do not read it. The plan then requires the task to use an
output that covers the path, and fails with the edge to add when it does
not.

Type the spec in your authoring package, so a schema's editor checks it:

```ts
declare module "@superschematic/tasks" {
  interface InputKinds {
    "bazel-target": { readonly label: string };
  }
}
```

**A task section** is a part of the task file that your forge owns. The
core checks each part against its schema and copies it, and reads
nothing in it:

```go
r.RegisterTaskSection(registry.TaskSectionSpec{
    Name:      "acme",
    Extension: Name,
    File:      taskSectionFile, // the JSON Schema of the part @tasks holds
    Task:      taskSectionTask, // the JSON Schema of the part @task holds
    Verify: func(req registry.TaskSectionRequest, r registry.VerifyReporter) {
        // req.File, req.Tasks by task name, and req.Names, every task.
    },
})
```

`Verify` holds what a schema cannot say. acme's checks that every task
names a tier, that the tier is one `@tasks` declares, and that no task's
timeout is longer than its tier's budget (`ext/tasks.go`). A forge with
no extension registers its section as data instead, in the naming file's
[`[tasks.sections.<name>]`](/superschematic/reference/naming/#taskssectionsname),
and checks those rules when it reads the file.

Augment `TaskSections` and `FileSections` of `@superschematic/tasks` to
type the two parts, as `InputKinds` above.

A section is also how a CI renderer reads what is its CI's. The core's
`github` renderer registers the `github` section and writes a workflow
from the task file, a job per task
([Run the tasks on GitHub Actions](/superschematic/guides/tasks/#run-the-tasks-on-github-actions)).
A renderer for another CI gives its `CIRendererSpec` a `RenderTasks`,
registers a section for its triggers and placement, and is named under
`outputs.tasks.ci`.

acme's test `TestAForgeReusesWorkByKeyAcrossCommits`
(`ext/tasks_test.go`) is a reader in miniature: it decodes the committed
file with the public `tasks` package, runs `tasks plan` across commits,
and reuses each pass by its key.

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
| `Files(out)` | extra files to write (optional); one with a core file's name, such as `context.go`, replaces it |
| `OpenAPIParameters(out)` | parameters on authenticated routes |

`Analyze` runs against the schema `authDb` names. The snippets branch on
the model so the generated code compiles against the ORM it is given.
`registry.AuthSnippetFunc(provider)` in a provider test catches a missing
snippet at `go test`. The api generator checks the set is complete when it
parses the templates.

Three snippets add middlewares to each mounted route, and take the
route's `EndpointInfo`: `routeMiddlewaresFirst` runs before every other
step, `routePermissions` runs after the rate limit, body limit and service
step, and `routeMiddlewaresAfterDecrypt` runs after the payload decryptor
and before the timeout. `routesConfigFields` adds the `Config` fields they
read, in every API, public or not, and `routesDeclarations` adds the
functions, types and values of `routes.go` they call.
`routesConfigPrepare` opens `RegisterRoutes`, before it validates
`cfg`: statements that fill a core field from the provider's fields.
`routeUploadBody` and `routeQueryList` replace the core's reading of an
upload route's body and of one list in the query string when they are
not empty: the first declares `input` and `files`, the second the list's
variable. `callerImports` and `callerPrincipal` give `caller.go` its
imports and the principal `WithCaller` binds to the ORM's context, so a
provider that does not build on the core's session runtime still
compiles; an empty principal leaves the context as it is.

An extension's own word for what its Go routers count a `@rateLimit` per,
such as an organization, registers with `RegisterRateLimitKey` (D131): a
`registry.RateLimitKey` with its `Name` and `GoPrincipal`, a Go
expression of type `func(*http.Request) string`
([A rate limit per principal](/superschematic/guides/api-routes/#a-rate-limit-per-principal)).

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
it in `openapi.go`, the Rust server in `src/openapi.rs`, the TypeScript
package in `openapi.ts`). It gets the document
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

`tools.Keys` holds every vendor key the documents spell, and
`DigestInternalTypes`, which makes the input schema digest hash a
`Generic.JSON` property's internal type, for a distribution that stores
digests another compiler wrote (D135)
([Rules an extension adds](/superschematic/reference/mcp-tools/#rules-an-extension-adds)).

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
table (D103), or to a General schema's type (D16). The compiler declares,
carries and checks behaviors; the behaviors' core runs them, on a table
and in development mode (D117). An extension ships the declaration,
registered with the compiler, and the behavior's rules: stated in the
declarative vocabulary, or a Rust crate that implements its hooks over
the core's API, which the project's rules crate adds to the core
([The project's rules core](#the-projects-rules-core)).

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
  A config never renames a field. A field the core computes at a read
  from other instances, the clock or the config says `"volatile": true`,
  since no event of its instance records its moves. One whose value is
  the caller's, not the instance's (an unread count), also says
  `"perReader": true`, so no event records it as the instance's; only a
  volatile field may.
- `operations` are camelCase and may not be `create`, `get`, `list`,
  `update` or `delete`, which every schema has. `paramsSchema` is an
  object schema that sets `"additionalProperties": false`, so an
  operation's parameters are exactly the ones it declares; the registry
  refuses one that does not.
  `invocationPolicy` is a value of the registry's tool invocation policy;
  left out, the policy's default applies. `scope` is `instance`, the
  default, `schema`, an operation on the schema as a whole, or
  `namespace`, one on the behavior's own tables across every schema of a
  namespace that composes it, called by the behavior's name
  ([development mode's routes](/superschematic/guides/development-mode/#routes)).
  An instance operation that writes only its
  caller's own state may say `"beside": true, "log": false, "asks":
  "read"`, and the policy is asked `read` for it.
- `signals` are the messages about an instance the core hands to the
  event streams open on it and keeps nowhere (D77): a typing indicator,
  a draft's next chunk. Each is `{ name, description, paramsSchema,
  access }`, its name camelCase and unique among the behavior's signals,
  its `paramsSchema` the JSON Schema of its data, and its `access` who
  may send it: `read` or `write`, a caller the policy lets read or write
  the schema, or `operation`, the behavior's own operations alone,
  through their context's `signal(name, data)`.
- `vetoes` lists the codes the behavior's refusals carry, each
  `{ code, description }` in lowercase snake case, so a client can
  branch on `details.code` of a 409 `vetoed`. The core refuses a veto
  with a code the declaration does not list.
- `preconditionSchema`, a closed object schema, declares what a caller
  may assert to the behavior with a write: the entry it sends under the
  behavior's name in the preconditions of an update, a delete or an
  operation, which the core checks and hands to the behavior's guard.
  Lease's is `{ token }`.
- `rules`, optional, states the behavior's rules in the declarative
  vocabulary the behaviors' core interprets (D103's step 5), so a table
  that composes it runs it with no Rust: conditions, field rules, guards,
  an operation's effects, reactions and fields derived from a sub-table
  ([declarative behaviors](/superschematic/reference/declarative-behaviors/)).
  acme's file states them beside `tables` (a `ratings` sub-table keyed by
  the shopper) and fields with a `type`, which a table's layout reads.

Assembly fails on a malformed declaration, and `Finalize` on a
`requires` or `conflicts` name nobody registered or an invocation policy
value the registry's policy does not have.

A General schema's type composes behaviors with `@behavior` from
`@superschematic/schema` (D16). Several on one class apply in source
order, and a type lists a behavior once:

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

A DB type is a table, which implements a behavior or carries it on a
field instead (D103): the load refuses `@behavior` on it and names that
form.

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
under the name in PascalCase (`acme.Rating` is `AcmeRating`). A General
schema with such a type builds. A table that implements the behavior, or
carries it on a field, stays a table: `sql` writes the fields it owns as
columns, and the behaviors' core runs it
([The project's rules core](#the-projects-rules-core)). An API's own type
cannot compose one. A generator of yours whose output covers what a
behavior adds to a table, or holds nothing of the table at all, as acme's
manifest lists type names, sets `GeneratorSpec.RendersFieldBehaviors`;
without it, a build that runs it on a schema one of whose tables
composes a behavior, or that imports such a table, fails and names it.
acme's `shop-ratings` and `shop-ratings-ts` services
(`ext/testdata/services`) compose `acme.Rating`.

## A behavior declared by a class

A behavior can also be declared by an abstract class in the schema
language (D103). The build derives from the class the declaration the
behaviors' core reads, the same JSON document as above. The class
declares and never implements: a method body would run in one language.
The core's own behaviors are classes of this form, in
`internal/registry/behaviors/<name>.behavior.ts`, and `make behaviors`
writes each one's `<name>.behavior.json` from it.

### The class

```ts
import { declaresBehavior, subTable, type Integer, type JsonSchema, type Names } from "@superschematic/db";

interface RatingConfig {
  /** The most stars a rating gives. */
  maxStars: JsonSchema<Integer, { minimum: 1; maximum: 10 }>;
  /** The permission a caller needs to clear the ratings. */
  clearPermission?: JsonSchema<string, { minLength: 1 }>;
  /** The field whose Workflow a rating needs to be open. */
  status?: Names<string, "workflow">;
}

/** The codes Rating's refusals carry. */
enum RatingVeto {
  /** More stars than the config's maxStars. */
  too_many_stars,
}

/** Shoppers rate a row from one star to the most the config gives. */
@declaresBehavior({
  name: "notes.Rating",
  vetoes: RatingVeto,
  crate: { crate: "notes-rules", path: "crates/rules", register: "notes_rules::Rating" },
})
export abstract class Rating<T, C extends RatingConfig> {
  /** How many ratings the row has. */
  ratingCount: number;

  /**
   * Rates the row as the caller: a second rating replaces the first.
   * @writes
   */
  abstract rate(params: { stars: Integer }): { count: Integer };

  /**
   * Clears the row's ratings.
   * @writes
   * @checks clearPermission
   */
  abstract clear(): { count: Integer };

  /** One row per rater. */
  @subTable({ key: "rater" })
  static ratings = class {
    /** Who rated. */
    rater: string;
    /** How many stars. */
    stars: number;
  };
}
```

Each part of the class is one member of the declaration:

- The class's JSDoc is its `description`. The options of
  `@declaresBehavior` give its `name` (the class's own when absent),
  `requires`, `conflicts`, `armsDeadlines`, `hearsChanges`, the contracts
  it `asks`, its `vetoes` (an enum: each member is a code, and its JSDoc
  is the code's description), and its `crate` or its `rules`.
- The last type parameter's constraint is the config. A first one, when
  there are two, is the table the behavior sits on.
- A property is a field, its type the column's: `string`, `number`,
  `boolean`, or a superscalar scalar such as `Temporal.DateTime`.
  `@valueField` marks the field a field anchor's value is, and `?` on it
  makes it optional. `@fromCreate`, `@computed`, `@volatile` and
  `@perReader` mark the rest.
- A static property whose value is a class expression, marked
  `@subTable({ key, shared? })`, is a sub-table. It is static so that a
  sub-table may share its name with a field, as Timers' `deadlines` do.
  `RowKey` types a field that holds the key of a row of the table.
- An abstract method is an operation. Its parameter's type is its
  `paramsSchema`, its return type its `resultSchema`, and its JSDoc's
  tags say the rest: `@writes`, `@scope schema`, `@beside`, `@log false`,
  `@asks read`, `@invocation <value>`, `@checks <paths>` and
  `@returns <description>`. A method tagged `@event` is an event, one
  tagged `@signal <access>` a signal. The methods `create` and
  `precondition` are the create's parameters and a caller's precondition.
- `implements Capacity` or `implements Supply` names a contract the
  behavior implements. A JSDoc comment before the name is its
  description.

The reader writes each JSON Schema from the types as written. An
interface or a type literal is an object with `additionalProperties`
false, whose members marked `?` are left out of `required`. `X[]` is an
array, a tuple an array of exactly its elements, `Record<K, V>` an object
of `V`, a union of literals an `enum`, `T | null` the type of `T` with
`"null"` beside it, and another union a `oneOf`. `Integer`, `Const<V>`,
`OneOf<U>` and `Open<T>` say what TypeScript's types do not, and
`JsonSchema<T, K>` adds the keywords the literal type `K` holds. A
superscalar scalar (`Temporal.Period`) is its JSON type with the bounds
and the pattern its definition declares, and its name under
`x-superschematic-scalar`, the keyword a tool's schema names a scalar by.

### What the declaration states

Two members of a declaration say what the build used to read from code
it held for each behavior:

- An operation's `permissions`, written with `@checks`, are the config
  paths whose permissions it checks: `transitions[].permission` is each
  transition's, `gates.*.permission` each gate's. The permission catalog
  lists under an operation only the permissions a table's config holds at
  those paths. A declaration that states none for any operation lists
  every permission its config names under each of its operations.
- `references` are the config's members that name a part of the table,
  each with its path and its kind. A class marks them in its config's
  types. `Names<T, "field">` names an own field, and
  `Names<T, { field: "Temporal.DateTime"; reason }>` one that holds that
  scalar. `Names<T, "workflow">` names a Workflow use, and
  `Names<T, "state">` a state of the Workflow it reads. `Hears<T>` is an
  object whose `schema` and `link` name a table and its relation to this
  one, whose writes the behavior hears. The build holds a table's config to
  them before a behavior's own checks run, and finds from them the tables
  whose writes a table hears.

### A class that states its rules

A behavior whose rules the declarative vocabulary states needs no crate
(D103's step 5). Its class gives them in `@declaresBehavior`'s `rules`,
written as the JSON the declaration's `rules` member holds, and names no
`crate`: a behavior's rules are its crate's or the vocabulary's, not both.
acme.Rating is one, in `examples/acme-schematic/ext/rating.behavior.ts`:

```ts
@declaresBehavior({
  name: "acme.Rating",
  rules: {
    operations: {
      rate: {
        params: { stars: { lte: { config: "maxStars" } } },
        effects: [{ upsert: { table: "ratings", key: { caller: true }, values: { stars: { param: "stars" }, ratedAt: { now: true } } } }],
        result: { ratingCount: { field: "ratingCount" }, ratingAverage: { field: "ratingAverage" } },
      },
    },
    derived: {
      ratingCount: { count: { table: "ratings" } },
      ratingAverage: { average: { table: "ratings", field: "stars", empty: 0 } },
    },
    configChange: { oneWay: { maxStars: "up" } },
  },
})
export abstract class Rating<C extends RatingConfig> {
  // its fields, its ratings sub-table and its rate and ratingSummary
}
```

The derived declaration keeps the rules' object members in sorted order,
as it keeps a JSON Schema's. `make behaviors` writes the extension's
`rating.behavior.json` from the class, which the extension embeds, and
`make behaviors-check` fails when the two differ. The
[declarative behaviors](/superschematic/reference/declarative-behaviors/)
reference lists the vocabulary.

### A project's own behaviors

A project names the directory of its behaviors in its `superschematic.toml`
([`behaviors`](/superschematic/reference/naming/#behaviors)). The
directory holds the classes of its `*.behavior.ts` files, beside a
`tsconfig.json` that resolves `@superschematic/db`. It may also hold a
behavior's declaration as data, a `*.behavior.json` or `*.behavior.yaml`
file, with a `crate` member beside the declaration's, or with `rules` for a
declarative behavior. Every command
registers them before it reads a schema. A project's behavior is named
`<prefix>.<Name>`, as an extension's is.

A table implements a class's behavior through the class:

```ts
import { Identity } from "superscalar";
import { key, type Behavior } from "@superschematic/db";
import { Rating } from "../../../behaviors/rating.behavior";

export abstract class Movie implements Behavior<typeof Rating, Movie, { maxStars: 5 }> {
  @key id: Identity.UUID;
  title: string;
}
```

`Behavior<typeof B, T, C>` resolves to the empty object type, so
TypeScript imposes none of the class's members on the table.
`BehaviorField<typeof B, C>` carries one on a field. A JSON or YAML
schema names a project's behavior as it names one of the core's:
`{ "name": "notes.Flag", "implements": true }`.

### The project's rules core

A behavior whose rules need code names the Rust crate that implements its
hooks over the core's API (`crate`, `registry.BehaviorSpec.Rules` in Go):
a value that implements `superschematic_rules::Behavior`. The
registration gives its Cargo package name, its directory relative to your
project (the parent of the schemas root, which `[paths]` is relative to
as well), and the Rust expression the project's rules crate adds to the
core:

```go
r.RegisterBehavior(registry.BehaviorSpec{
	Extension: Name,
	Rules: registry.BehaviorRust{
		Crate:    "acme-rules",
		Path:     "crates/rules",
		Register: "acme_rules::Flagged",
	},
	Declaration: flaggedDeclaration,
})
```

Copy the declaration into the crate with your binary's `behaviors`
command, and run it with `--check` in CI so the copy cannot fall behind:

```sh
acme-schematic behaviors --extension acme --out crates/rules/declarations
acme-schematic behaviors --extension acme --out crates/rules/declarations --check
```

A table may then implement the behavior, and the build of its DB schema
writes the project's rules crate into `rules/rust/<schema>`. The crate is the core's
behaviors with each composed behavior's crate added, and it exports the
core's ABI over them (`superschematic_rules_wasm::export_rules_abi`). Its
tests find every composed behavior registered and declared.

The crate's archive and module stand in the stock ones' place:

- A Go server links the crate's static archive. Stage it with
  `scripts/rules-archive.sh --crate <crate> --out <dir>` and put `-L<dir>`
  first in `CGO_LDFLAGS`, before the binding's own directory.
- A TypeScript host loads the crate's `wasm32-unknown-unknown` build:
  pass it to `@superschematic/rules`' `init(source)`.

A schema whose tables compose the core's behaviors alone gets no rules
crate: the stock core runs them, as it runs a declarative behavior, whose
declaration the layout hands it. A composed behavior whose crate is not
in the project leaves no core that can run the schema, and the build's log
says why.

A Rust server the build writes for an API over the schema runs on the
same core. Its `src/behaviors.rs` builds the project's rules crate's
`core()`, which its `Cargo.toml` depends on by path, or the stock core
when the schema needs no crate. When the layout declares declarative
behaviors, it adds them to that core.

## A deploy target

A stack deploys a schema tree through a target, and the target's resources
through a provisioner (D30 in the [decision log](https://github.com/parable-work/superschematic/blob/e8e685a5149a8897001153fb6cca1100ac90d0c0/docs/DECISIONS.md); section 6 of
`docs/stack-model.md` is the design). The official extensions are the
examples: `extensions/gcp` registers the `gcp` target with its platforms,
connectors and Cloud DNS platform, `extensions/cloudflare` the
`cloudflare` target with its platforms and connectors and a DNS platform,
`extensions/embedded` the `embedded` target, which builds an environment
as one binary, and `extensions/pulumi` the provisioner. Six registrations
make up the surface, each spec carrying the extension's `Name()`:

| Registration | Adds |
| --- | --- |
| `RegisterPlatform(registry.PlatformSpec)` | one deployable kind (`server`, `database`, `job`, `worker`, `site`, `bucket`, `memoryStore`, `deriver` or `app`) on one runtime, such as Cloud Run or Cloud SQL: the server languages or SQL dialects it runs, the JSON Schema of a deployable's settings there, its name and address in an environment, and a pure `Lower` to its resources |
| `RegisterConnector(registry.ConnectorSpec)` | one edge kind (`sql`, `http`, `site`, `bucket` or `memoryStore`) between two platforms: a pure `Connect` that returns the edge's resources, such as an IAM grant, and the value of the config field the edge derives |
| `RegisterTarget(registry.TargetSpec)` | a platform per deployable kind, the JSON Schema of an environment's values, the default DNS platform, the provisioner, the properties schema of each resource type its platforms emit, and policy rules over the resolved resource graph |
| `RegisterDNSPlatform(registry.DNSPlatformSpec)` | a DNS provider for an environment's domain records, which need not be the target's provider: its values schema, a pure `Lower` from records to resources, the schemas of the resource types it brings, and the credentials its provider reads |
| `RegisterProvisioner(registry.ProvisionerSpec)` | a tool that applies a resolved environment: `Render` writes the tool's program where a person can read it, and `Plan`, `Apply`, `Destroy` and `Outputs` run it; `Tools` lists the command-line tools it runs, which a generated CI job installs |
| `RegisterCIRenderer(registry.CIRendererSpec)` | a CI system a workflow is written for: the directory it installs into, a pure `Render` from a stack, its resolved environments with their CI identities, its options from `outputs.ci` and the release, to files, and a pure `RenderTasks` from a Tasks service's task file to files (`outputs.tasks.ci`). A renderer gives one or both. The core registers `github` |

Resolution checks every resource against the schema of its type, so a
target checks in the provider schemas it emits from a pinned provider
version, as the gcp target does with pulumi-gcp's. A target that deploys
also fills the deploy seams on its `TargetSpec`: `State`, `Secrets`,
`Bootstrap`, `Migrations`, `Builder`, `CI`, which says how a generated
CI job signs in to an environment, `Jobs`, which runs a deployed job
on demand, `Queues`, `Sites`, `Bundles` and `PreDestroy`. The core
registers one target of its own, `local`, for `stack dev`.
[Stack targets](/superschematic/extending/stack-targets/) walks each
registration, its seams and its offline tests.

## A runtime provider

What a running app calls, a channel adapter, a credential provider, a
sandbox, a model or a file processor, is not registered with the binary.
A runtime provider is a package of its own that declares the interface it
implements in its `package.json`, under `superschematic.provider`; an
API's config lists the packages, and each environment names one per slot
(D159). The interfaces are TypeScript only for now. What an extension
registers is a feature that needs one: `RegisterRuntimeFeature` with a
`registry.RuntimeFeature`, its `Name`, the `Interface` it needs and a
`Uses` over the API's schema and its databases, so the stack refuses an
environment that runs such an API with no provider of that interface.
[Runtime providers](/superschematic/guides/runtime-providers/) has both.

## A command

`cli.New` returns the core's commands: `check`, `build`, `build-all`,
`migrate`, `queries`, `json-schema`, `format`, `behaviors`, `stack`,
`identity`, `env`, `catalog`, `init`, `new` and `tasks`
([CLI](/superschematic/reference/cli/)). An extension that implements
`cli.CommandProvider` contributes more:

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
  (`github.com/microsoft/typescript-go/shim/checker` and `.../ast`). Import
  the shim to name a node kind. `ErrorAt(node, ...)` makes a located error
  in the same form as the diagnostics.
- The pinned compiler is Microsoft's unmodified TypeScript 7.0.2
  (`github.com/microsoft/TypeScript/tsc`). The shim module
  (`github.com/parable-work/typescript-shim`) exposes its packages. Go
  does not pass a `replace` on to a module that requires yours, so your
  extension's `go.mod` repeats the two `replace` lines of the root
  `go.mod`, as `examples/acme-schematic/go.mod` does.
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
