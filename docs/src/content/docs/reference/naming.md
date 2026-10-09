---
title: Naming file
description: Every key of superschematic.toml and its default.
sidebar:
  order: 1
---

`superschematic.toml` sits at the schemas root (the parent of `services/`).
`build`, `build-all` and `migrate plan` load it from there, or from
`--naming`; `migrate plan` loads the previous version's from that
version's own schemas root when it has one. `format` walks up from the
file you pass and loads the first one it finds. `json-schema` and
`behaviors` have no service directory; they use the built-in defaults
unless you pass `--naming`.

A missing file is `Default()`. A key left out keeps its default. An
unknown top-level key is an error, except keys under `[extension.<name>]`,
which the named extension validates.

The file at the repository root is the defaults written out so a
deployment can copy it. Structural suffixes (`-types`, `-sdk`, `-api`, the
`types/go` and `sdk/go` subpaths) are not configurable; they describe the
artifact kind.

## Top-level keys

### `go_module_root`

Default: `example.com/schemas`

Prefix of every generated Go module path:
`<root>/types/go/<name>`, `<root>/orm/<name>`, `<root>/api/<name>`,
`<root>/sdk/go/<name>`.

### `npm_scope`

Default: `@schemas`

npm scope of generated TypeScript packages and of the service authoring
packages schemas import from each other: `<scope>/<name>-types`,
`<scope>/<name>-sdk`, `<scope>/<name>-api` (a TypeScript API server),
`<scope>/<name>`.

### `python_types_module_prefix`

Default: `schemas_types_`

Prefix of generated Python type modules: `<prefix><stem>`, where stem is
the schema name with `-` folded to `_`.

### `python_sdk_module_prefix`

Default: `schemas_`

Prefix of generated Python SDK modules. Combined with
`python_sdk_module_suffix`: `<prefix><stem><suffix>`.

### `python_sdk_module_suffix`

Default: `_sdk`

Suffix of generated Python SDK modules.

### `rust_crate_prefix`

Default: `schemas-`

Prefix of generated crates: `<prefix><name>-types`, `<prefix><name>-sdk`,
`<prefix><name>-api`.

### `scalar_go_module`

Default: `github.com/parable-work/superscalar/go`

Go module path of the scalar library generated Go code imports.

### `scalar_npm_package`

Default: `superscalar`

npm package of the scalar library. Always added to `authoring_packages`
if you omit it there.

### `scalar_pypi_dist`

Default: `superscalar`

PyPI distribution name of the scalar library.

### `scalar_python_module`

Default: `superscalar`

Python import name of the scalar library.

### `scalar_rust_crate`

Default: `superscalar`

Cargo crate name of the scalar library. Generated Rust spells it with
`-` folded to `_`.

### `scalar_rust_registry`

Default: unset, which reads the scalar crate's builtin registry,
`<scalar_rust_crate>::Registry::builtin()` with the crate name folded as
above.

The Rust expression a generated Rust validator reads the scalar registry
from, of type `&'static Registry` of the scalar crate. A scalar crate that
adds its own scalars to superscalar's names the function that returns its
assembled registry, for example `acme_scalars::registry()`. Without it a
value of one of those scalars is refused as an unknown scalar.

### `schema_ir_go_module`

Default: `github.com/parable-work/superschematic/ir`

Go module path of the schema IR.

### `schema_runtime_go_module`

Default: `github.com/parable-work/superschematic/runtime/schema/go`

Go module path of the schema runtime.

### `schema_runtime_rust_crate`

Default: `superschematic-schema-runtime`

Cargo crate name of the Rust schema runtime: the error map, JSON type
checks and compiled patterns the validators of a generated Rust types
crate call. Generated code imports it as the identifier form of this name
(`superschematic_schema_runtime`).

### `versiongraph_go_module`

Default: `github.com/parable-work/superschematic/runtime/versiongraph/go`

Go module path of the version-graph core's Go binding. A generated ORM
imports it when its schema declares a version graph. The binding links
the core's static archive through cgo; the
[Go install page](/superschematic/install/go/#requirements) says how to
build it.

### `versiongraph_rust_crate`

Default: `superschematic-versiongraph-engine`

Cargo crate name of the version graph's Rust engine. A generated Rust
types crate depends on it when its schema declares a version graph, and
its typed facade (`src/versiongraph_<name>.rs`) imports it as the
identifier form of this name (`superschematic_versiongraph_engine`).

### `http_runtime_go_module`

Default: `github.com/parable-work/superschematic/runtime/http/go`

Go module path of the HTTP runtime.

### `http_runtime_rust_crate`

Default: `superschematic-http-runtime`

Cargo crate name of the HTTP runtime. Generated Rust API crates import
it as the identifier form of this name (`superschematic_http_runtime`).

### `http_runtime_npm_package`

Default: `@superschematic/http-runtime`

npm package name of the TypeScript HTTP runtime. A generated TypeScript
API package imports its request pipeline from this package and from its
`/hono` entry point, and lists it as a peer dependency.

### `versiongraph_npm_package`

Default: `@superschematic/versiongraph`

npm package name of the TypeScript version-graph runtime: the core's wasm
build, the engine, its Postgres adapter and the facade base. A generated
TypeScript types package whose schema declares a version graph imports
the facade base from its `/facade` entry point and lists the package as a
dependency ([Use the engine from TypeScript](/superschematic/reference/version-graphs/#use-the-engine-from-typescript)).

### `versiongraph_pypi_dist`

Default: `superschematic-versiongraph`

PyPI distribution name of the Python version-graph runtime: the core's
PyO3 binding, the engine, its Postgres adapter and the facade base. A
generated Python types package whose schema declares a version graph lists
it as a dependency ([Use the engine from Python](/superschematic/reference/version-graphs/#use-the-engine-from-python)).

### `versiongraph_python_module`

Default: `superschematic_versiongraph`

Python import name of the Python version-graph runtime. A generated
facade (`<module>/versiongraph_<name>.py`) imports the facade base from
its `facade` module.

### `engine_npm_package`

Default: `@superschematic/engine`

npm package name of the engine. The module `superschematic engine-client`
writes imports the typed client from its `/client` entry point
([CLI](/superschematic/reference/cli/#engine-client---out-filets-schema-file)).

### `ptr_go_module`

Default: `github.com/parable-work/superschematic/runtime/schema/go/ptr`

Go module path of the `ptr` helpers. They are a package of the schema
runtime, not a module of their own, unless you point this elsewhere.

### `schema_language`

Default: `Superschematic`

How generated readmes and the schema-file JSON Schema name the schema
language ("generated from `<schema_language>` definitions").

### `package_author`

Default: `superschematic`

Author field generated package manifests carry (`package.json`,
`pyproject.toml`, `setup.py`).

### `meta_schema_url_prefix`

Default: `superschematic://`

Prefix of the `$id` of the JSON Schemas the tool emits and validates
against (the schema-file document schema).

### `metadata_key_prefix`

Default: `superschematic.`

Prefix of every metadata key in the Arrow schemas the sql generator writes
for [projection views](/superschematic/reference/projections/):
`<prefix>scalar.canonical_name`, `<prefix>enum.values`,
`<prefix>projection.settings` and the rest. Set it to the namespace the
schemas' readers expect; the part after the prefix is fixed.

### `history_actor_setting`

Default: `superschematic.history_actor_id`

The transaction-local Postgres setting the history trigger of a
`@versioned` table reads a delete's actor from. A delete tombstone's image
is the row before the delete, with its actor column set from this setting
when it is set, else left as the row had it. The actor column is
`deleted_by` when the table has one, else `updated_by`; a table with
neither records no actor. The ORM's hard deletes on such a table set the
setting to the context user for their statement and, inside a
transaction, clear it after. A statement of your own sets it with
`SELECT set_config('<setting>', '<user id>', true)`. When
`@versioned({ exclude })` names the actor column, the tombstone records no
actor and the generated hard deletes do not set the setting.

The value is a custom setting name: two or more identifiers (letters,
digits and `_`, not starting with a digit) joined by dots. Any other value
fails the load.

### `scalar_jsdoc_tag`

Default: unset (no tag line)

Name of a JSDoc tag the generated TypeScript types write above every
scalar-typed field, followed by the scalar's canonical name. With
`scalar_jsdoc_tag = "scalar"`, `types/types.ts` reads:

```ts
export interface User {
  /** @scalar Identity.UUID */
  id?: IdentityUUID | null;
  /** Display name shown across the product. */
  /** @scalar Identity.Name */
  name: IdentityName;
  isActive: boolean;
}
```

The tag line sits directly above the field, after the field's doc line
when it has one. Fields of a primitive, enum or object type get none.
`tsc` keeps the comment in the declaration files it emits, so a tool that
reads the `.d.ts` files can find each field's scalar without the IR.

The value is the tag name without the `@`: letters, digits and `_`, not
starting with a digit. Any other value fails the load. Unset, the types
carry no tag line; unlike most keys, an empty value has no default to fall
back to.

### `auth_provider`

Default: `session`

Registered auth provider the api generator renders with. The core
registers `session`. An extension that registers another provider names
it here. A value no linked extension registered is a `Finalize` error.

### `authoring_packages`

Default:

```
[
  "@superschematic/api",
  "@superschematic/db",
  "@superschematic/deploy",
  "@superschematic/schema",
  "@superschematic/schema-config",
  "superscalar",
]
```

npm packages whose exports the TypeScript frontend treats as toolchain.
Decorators and type wrappers must resolve from one of them. An empty
list in the file falls back to this default; the scalar package is
always appended if missing.

### `package_aliases`

Default: unset (every specifier is its own declaring package)

Map from an import specifier a schema may write to the authoring package
that declares the symbols it re-exports. A distribution that republishes
the core packages under its own names writes:

```toml
authoring_packages = ["@acme/api", "@acme/db", "@acme/schema", "@acme/schema-config", "@acme/scalars"]

[package_aliases]
"@acme/api" = "@superschematic/api"
"@acme/db" = "@superschematic/db"
"@acme/schema" = "@superschematic/schema"
"@acme/schema-config" = "@superschematic/schema-config"
"@acme/scalars" = "superscalar"
```

The loader resolves symbols to their declaring package. The writer and
diagnostics use the author's spelling (the alphabetically first alias
that maps to a declaring package). A `schema.config.ts` may import the
config package under any specifier that resolves to
`@superschematic/schema-config`; the loader goes by what a name resolves
to. The generated service sentinels import it under the author's
spelling too.

## `[cache]`

Both keys are optional. The zero value keeps the platform default root
and hashes no extra files.

### `cache.root`

Default: unset (XDG cache directory, under `superschematic/build`)

Build cache directory. `~` expands. `SUPERSCHEMATIC_BUILD_CACHE_DIR`
overrides it, and `--cache-root` overrides both.

### `cache.inputs`

Default: unset (no extra files)

Repo-relative files hashed into every `build-all` cache key alongside the
schema tree, the tool digest and the workspace lockfile: files generation
reads that live outside the schema tree. The repository root is the
parent of the schemas root. An absolute entry is an error that names it,
such as `cache.inputs[0]`.

## `[paths]`

In-tree locations generated modules point path dependencies at (`go.mod`
replace, Cargo `path`, npm `file:`). Every key is optional and
repo-relative. The repository root is the parent of the schemas root. An
absolute value is an error that names the key. An unset key emits no path
dependency, so the generated manifest resolves the published module; set
the key until that module is published. For a Go runtime module, a
release of superschematic pins an unset key's module in every generated
`go.mod` to itself: superschematic's modules at the release's tag and the
scalar library's Go module at the version the release links. A binary
built from a checkout pins nothing, and those `go.mod` files require
versions no module proxy serves.

### `paths.scalar_go`

Default: unset. This repository's own file sets
`third_party/superscalar/go`.

Directory of the scalar library's Go module (`go.mod`). A stack's server
Dockerfiles build superscalar's static archive, and the version graph's,
from the checkout that holds it. Unset, a release of superschematic pins
the module to the version the release links, and its server Dockerfiles
download the archives the release ships
([Stacks](/superschematic/guides/stacks/)); a binary built from a
checkout writes no Dockerfile.

### `paths.scalar_typescript`

Default: unset. This repository's own file sets
`third_party/superscalar/bindings/typescript`.

Directory of the scalar library's npm package (`package.json`). Unset, a
generated TypeScript types package depends on `superscalar` with `*`,
which fails `bun install` with a 404 until superscalar publishes the
package.

### `paths.scalar_rust`

Default: unset. This repository's own file sets
`third_party/superscalar/crates/core`.

Directory of the scalar library's Rust crate (`Cargo.toml`).

### `paths.schema_ir`

Default: unset. This repository's own file sets `ir`.

Directory of the schema IR Go module.

### `paths.schema_runtime_go`

Default: unset. This repository's own file sets `runtime/schema/go`.

Directory of the schema runtime Go module.

### `paths.schema_runtime_rust`

Default: unset. This repository's own file sets `runtime/schema/rust`.

Directory of the Rust schema runtime crate. Unset, a generated Rust types
crate names the crate's version instead of a path.

### `paths.versiongraph_go`

Default: unset. This repository's own file sets `runtime/versiongraph/go`.

Directory of the version-graph core's Go binding module.

### `paths.versiongraph_typescript`

Default: unset. This repository's own file sets
`runtime/versiongraph/typescript`.

Directory of the TypeScript version-graph runtime's npm package
(`package.json`). A generated TypeScript types package whose schema
declares a version graph depends on it with a `file:` spec. Unset, the
dependency is `*`, and `@superschematic/versiongraph` is unpublished until
the first tag, so until then set this key whenever a schema declares a
graph. Without it `bun install` fails with a 404, and since the generated
types packages form one Bun workspace, it fails for every package in it.

### `paths.versiongraph_rust`

Default: unset. This repository's own file sets
`runtime/versiongraph/rust-engine`.

Directory of the version graph's Rust engine crate. Unset, a generated
Rust types crate names the crate's version instead of a path.

### `paths.versiongraph_python`

Default: unset. This repository's own file sets
`runtime/versiongraph/python`.

Directory of the Python version-graph runtime (`pyproject.toml`). A
generated Python types package whose schema declares a version graph
names it as a uv path source (`[tool.uv.sources]`); unset, the dependency
has no source and resolves from the index.

### `paths.http_runtime_go`

Default: unset. This repository's own file sets `runtime/http/go`.

Directory of the HTTP runtime Go module.

### `paths.http_runtime_rust`

Default: unset. This repository's own file sets `runtime/http/rust`.

Directory of the HTTP runtime Rust crate.

### `paths.http_runtime_typescript`

Default: unset. This repository's own file sets
`runtime/http/typescript`.

Directory of the HTTP runtime's npm package (`package.json`). The
generated TypeScript API packages, and the implementations the scaffold
writes, depend on `@superschematic/http-runtime` with `*`; with this key
set, the output root's Bun workspace overrides that with a `file:` path
to the checkout, which the install copies from its `dist/`, so build the
runtime first (`bun install && bun run build` in
`runtime/http/typescript`). Unset, the install fetches the package, which
fails with a 404 until it is published.

### `paths.ptr`

Default: unset.

Directory of the `ptr` Go module when it is a module of its own rather
than a package of the schema runtime.

### `paths.build_context`

Default: unset, the repository root. `examples/acme-shop` sets `../..`.

The build context of a stack's server images: the directory the generated
`Dockerfile` copies from, and the root `stack build` and `stack deploy`
archive for each build
([The commands](/superschematic/guides/stacks/#the-commands)). Every
module a server's `go.mod` replaces must lie under it, or the server gets
no `Dockerfile`. Set it when the runtime modules the other keys name lie
above the repository root, as in an example inside a checkout of
superschematic.

## `[deps]`

### `deps.copy`

Default: unset (the graph is written only to `<output-root>/.deps.json`)

Repo-relative path that `build-all` also writes the dependency graph of
the generated packages to, byte for byte. The repository root is the
parent of the schemas root, as for `[paths]`. An absolute value is an
error. The output root is usually ignored by version control; a copy
outside it can be committed, so CI or a pin tool reads the graph without
building. `--deps-copy` overrides it and may be absolute. The key is not
part of the build cache key: moving the copy rebuilds nothing.

## `[derived_fields]`

How the config fields an API's edges derive in a stack are named: the
API's database connection, the endpoint of each API it `calls`, and the
connection of each bucket it lists in `buckets`
(see [What an API gets from its edges](/superschematic/guides/stacks/#what-an-api-gets-from-its-edges)). In each template `{SERVICE}` is the
DB, called API or Bucket service's name in upper snake case, and the rest
of the template holds upper-case letters, digits and underscores. A
platform sets each field as one environment variable per member of its
value (`SHOP_DB_DATABASE_URL`, `SHOP_API_SERVICE_CREDENTIAL_SOURCE`,
`SHOP_MEDIA_BUCKET_NAME`), and the loader refuses an `@envVars` field
named after one.

### `derived_fields.database`

Default: `{SERVICE}_DATABASE`

The field of an API's database: its `authDb`, or its one DB-kind
dependency.

### `derived_fields.service`

Default: `{SERVICE}_SERVICE`

The field of each API an API `calls`.

### `derived_fields.bucket`

Default: `{SERVICE}_BUCKET`

The field of each bucket an API lists in `buckets`
([Buckets](/superschematic/guides/buckets/)).

## `[implementation_paths]`

Where each API service's implementation lives, per language, as a path
from the repository root (the parent of the schemas root) in which
`{service}` is the service's name. A stack's build writes a missing
implementation there for each API its servers serve, and
`build --scaffold` and `build-all --scaffold` for each Go or TypeScript
API built. A server's generated entrypoint imports the implementation
from there. An absolute path, or one without `{service}`, is an error.

### `implementation_paths.go`

Default: `go/{service}`

The Go package of the implementation, whose `New(deps Deps)
(Implementations, error)` the generated API's `Constructor` types.

### `implementation_paths.typescript`

Default: `typescript/{service}`

The npm package of the implementation, whose `create` the generated API
package's `Constructor` types. The scaffold names it
`<npm_scope>/<service>-implementation`, a name you may change. The
output root's Bun workspace has every directory the template matches,
with `*` for `{service}`, as a member, so keep other packages out of
them.

## `[extension.<name>]`

Undecoded tables handed to the extension whose `Name()` matches
`<name>`. The core rejects no keys here; the extension does. A table for
an extension the binary does not link is ignored at load and unused.

acme reads `[extension.acme]` for a region string. See
[write an extension](/superschematic/extending/write-an-extension/).
