# Decisions

Short records of the choices that shape this repository. One entry per
decision, newest last. A change that reverses one adds a new entry and links
back rather than editing the old one.

## D1. Four Go modules

The compiler (`github.com/parable-work/superschematic`, the repository root),
the IR (`.../ir`), the schema runtime (`.../runtime/schema/go`) and the HTTP
runtime (`.../runtime/http/go`) are separate Go modules.

Generated code imports the runtimes and the IR at run time. Keeping them out
of the root module means a service that links a generated ORM or router does
not pull the compiler, its TypeScript parser (`typescript-go`) or cobra into
its build. The IR could have folded into the root module, but the schema
runtime imports it (`ircheck`, `parse`, `validate` read `ir.Schema`), so
folding it would have made the runtime depend on the compiler. The root
module and the runtimes reach the sibling modules through `replace`
directives until the modules are tagged.

The `ptr` helpers are a package of the schema runtime module, not a module of
their own.

## D2. Public packages, no alias layer

An extension imports `registry`, `loader`, `cli` and `schemadeps` from the
repository root. `registry` and `loader` are thin alias packages over
`internal/registry` and `internal/loader`, which is how the source tree
exposed them; the alias layer came across unchanged rather than moving the
internal packages out, because the extension-facing names
(`registry.Registry`, `registry.KindSpec`, `loader.Load`, `cli.New`,
`generator.Naming`, `apigen.AuthProvider`) already match and a move would
have touched every import in the tree for no change in API. Making the real
packages public is a later, mechanical change.

## D3. Scalars come from superscalar, at a pinned commit

The core depends on `github.com/parable-work/superscalar/go` and nothing
else for scalar validation, coercion and metadata. superscalar has no release
yet: its Go binding is cgo against a static archive its release pipeline
would publish, and its npm and PyPI packages are not published. Until they
are:

- `superscalar.pin` names the commit; every `go.mod` requires the same commit
  as a pseudo-version, and `internal/testpaths` has a test that fails when
  they disagree.
- `scripts/superscalar-dep.sh` clones that commit under `third_party/`
  (gitignored), builds the Go archive and the TypeScript binding from source,
  and prints the `CGO_LDFLAGS` value. CI runs it in every job that links
  scalars and caches the checkout on the pin.
- The TypeScript runtime resolves `superscalar` to the checkout through
  `tsconfig` paths and a symlink script; the Python runtime resolves it
  through a `[tool.uv.sources]` path entry that maturin builds.

When superscalar releases, the script's build step becomes its
`fetch_release_archive.sh`, and the TypeScript and Python paths become
published package versions. Nothing else changes.

## D4. The core's scalar set is the generic one

The source tree extended the scalar set with project-specific scalars
(permission strings, a schema-identifier scalar, file and image uploads with
storage constraints) and the compiler named their Go and TypeScript symbols
in templates. Those scalars are not in superscalar, so:

- The generated Go `scalars.go` no longer emits a legacy alias block; it
  emits one alias per scalar the service uses, with the symbol taken from
  the scalar registry's metadata (`Symbol`), not from a literal.
- The TypeScript generator dropped the scalar-lib hook symbol and the
  `Permission` type alias.
- The schema runtimes dropped their platform facades; the TypeScript and
  Python built-in catalogs are written from the superscalar Go metadata by
  `internal/tools/scalarcatalog` and checked in CI.

What remains of this (the D6 bucket in the source tree's sweep): the Go
types generator's `scalars.tmpl` still names `scalars.ValidationError`,
`scalars.ValidationErrors`, `scalars.NewValidationErrors` and `scalars.NewUUID`
as literals, and the Go API generator's `fileupload.tmpl` names
`scalars.FileUploadData`. The first four exist in superscalar and are the
scalar package's error and id contract rather than a scalar, so they stay
literals for now. `FileUploadData` does not exist in superscalar; the template is
only rendered when a scalar carries `FileUpload` metadata, which no generic
scalar does, so the core never emits it, but an extension that registers an
upload scalar would get a reference to a symbol its scalar package must
provide under that exact name. The fix is for the upload scalar's registry
entry to carry the data type's symbol and for the template to render it.

## D5. Defaults are superschematic's own

`naming.Default()` names `example.com/schemas` as the Go module root,
`@schemas` as the npm scope, `schemas_types_` / `schemas_` as the Python
prefixes, `schemas-` as the crate prefix, superscalar as the scalar package
in all four languages, this repository's modules as the runtimes, and
`session` as the auth provider. `superschematic.toml` at the repository root
writes those defaults out. The build cache reads
`SUPERSCHEMATIC_BUILD_CACHE_DIR` and falls back to the XDG cache directory
under `superschematic/build`.

A distribution that republishes the authoring packages under its own scope
lists them in `authoring_packages` and maps each to the declaring package in
`[package_aliases]`. The alias map has three consumers: the TypeScript writer
picks the preferred specifier when it emits an import, the registry's
forbidden-package check resolves an import to its declaring package before
looking it up, and the resolver's "type from package" diagnostic names the
specifier the author wrote. `sentinel.Content` emits the `schema-config`
specifier through the same map.

## D6. The HTTP runtime carries only provider-neutral packages

`runtime/http/go` holds `apperror`, `filterparse`, `middleware` (request
logging, recovery, AES-GCM payload decryption behind a caller-supplied
`PayloadDecryptor`), `requestctx`, `response`, `routing` and `session`. The
source tree's `authz` and `authmw` packages, and the typed identity fields
`requestctx` carried for them, encode one deployment's tenancy model and did
not come across; they belong to the extension that registers that auth
provider. The `session` provider the core registers knows nothing about
tenants: an endpoint's hoisted path parameter is `IsScopedEndpoint` /
`ScopeParamName` on `EndpointInfo`, and the generated SDK's tooling index
reports it as `isScoped`.

`runtime/http/rust` is a standalone crate with its own dependency versions;
generated Rust API crates import it as `superschematic_http_runtime`, the
identifier form of `Naming.HTTPRuntimeRustCrate`.

## D7. Meta-schema ids and package metadata come from Naming

The JSON Schema for schema files and for `schema.config.json` carry `$id`s
under `Naming.MetaSchemaURLPrefix` (default `superschematic://`), the
TypeScript runtime's `writeSchemaJson` takes the `$schema` URL as an option
with the same default, and generated readmes and package manifests read the
schema language name and package author from `Naming`.

## D8. What a downstream naming file can and cannot reproduce

Recorded by the R15 review of the bootstrap import. A distribution that
consumed the source tree's generator wants its generated output to stay
byte-identical after it switches to this core plus its own extension. The
review rendered three DB/General services from that tree and the API fixture
with both binaries under one extended naming file, normalized the header
timestamps, and diffed. Every difference falls into one of three buckets.

Bucket (a): reproduced by the naming file. Bucket (b): reproduced after a
code change made during the review. Bucket (c): unconditional; the consumer
accepts the change and updates its readers.

| # | Change | Bucket | Key or reason |
|---|--------|--------|---------------|
| 1 | Generated-header tool name | (c) | A literal in 38 templates and 61 generator files; `cli.Config.Name` only names the binary in usage text. The header says which program wrote the file, and that is this one. Threading a display name through every template so a fork can sign its output with another name is config in the wrong place. |
| 2 | `x-psgen` -> `x-superschematic` in `values-schema.json` | (c) | A JSON struct tag (`envgen/values_schema.go`); the vendor-extension key names the tool that owns the schema. Readers of the old key update. |
| 3 | Legacy alias blocks removed (`type UUID = scalars.UUID` and the Go `scalars.go` alias table; TS `Permission`) | (c) | The blocks re-exported one scalar library's whole symbol table under generated package names. The generated code never used them; downstream callers that wrote `dbtypes.UUID` migrate to the scalar package directly. A `[legacy_aliases]` table would keep a per-distribution list of symbols alive in the core. |
| 4 | `isTenantScoped` -> `isScoped` (tools index, MCP binding), Rust SDK `tenant_scoped` -> `with_header`, `IsScopedEndpoint` in templates | (c) | The identifiers are the core's own vocabulary for a hoisted path parameter; tenancy is what the extraction removed. The emitted scope parameter name itself (`tenantId`) still comes from the auth provider, so the SDK method signatures are unchanged when the provider sets it. |
| 5 | Python runtime import, Rust http runtime crate | (a) | `http_runtime_rust_crate` plus `[paths] http_runtime_rust` render the crate name and `use` ident; the Python schema runtime is never imported from generated packages, so its rename does not reach dist. |
| 6 | Meta-schema `$id` / `$schema` URL | (a) | `meta_schema_url_prefix`. The ids appear only in the tool's own JSON meta-schemas, not in generated service output. |
| 7 | Package author, readme text, schema language | (a) for author and the Python readmes (`package_author`, `schema_language`); (c) for the Go types readme | The Go types readme frames the value as "the {language} schema" while the Python readmes frame it as "{language} definitions"; the source tree wrote the noun lowercase in one ("X schema") and as part of the proper name in the other ("X Schema definitions"), and one key cannot reproduce both casings. Two keys to preserve a casing inconsistency is the wrong tool. |
| 8 | Scalar symbols from the registry | (a) | Symbols come from the assembled `ScalarDef.Symbol`, so an extension that registers its scalars renders the same identifiers. Rendering confirmed identical for every core scalar; the distribution-specific scalars need the extension present. |

Prose scrubbed from templates is also unconditional and lands in generated
comments and readmes: `scalar-lib` -> `the scalar library`, ticket and
decision-record ids removed from `create.tmpl`, `response.tmpl`,
`routes.tmpl`, `client.tmpl` and `runtime.tmpl`, `A tenant with this name` ->
`An account with this name` in `errors.tmpl`, the `PayloadDecryptor` and
`utils.tmpl` chain comments, and "local package ... monorepo" -> "workspace
package" in the SDK readme. The `scripts/scrub-check.sh` gate is what keeps
those out; the consumer regenerates once and reviews the comment diff.

Key names the review added to the source tree's naming file to reach this
result: `schema_language`, `package_author`, `meta_schema_url_prefix`,
`[paths] scalar_go / scalar_typescript / scalar_rust / schema_ir /
schema_runtime_go / http_runtime_go / http_runtime_rust / ptr`, and a
`[package_aliases]` table mapping the old authoring specifiers onto
`@superschematic/*`. `paths.scalar_lib` no longer exists.

## D9. Docs build on every pull request

superscalar builds its Starlight site only in `release.yml` (`docs-deploy`
on a `v*` tag). This repository also runs `npm ci && npm run build` in
`docs/` as the `docs` job of `ci.yml`, listed in `ci-pass`. A broken
internal link or a missing sidebar page fails the pull request instead of
waiting for the first tag. The release job still deploys to GitHub Pages
once the repository is public; the CI job does not deploy.
