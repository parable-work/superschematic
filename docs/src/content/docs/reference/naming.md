---
title: Naming file
description: Every key of superschematic.toml and its default.
sidebar:
  order: 1
---

`superschematic.toml` sits at the schemas root (the parent of `services/`).
`build` and `build-all` load it from there, or from `--naming`. `format`
walks up from the file you pass and loads the first one it finds.
`json-schema` has no service directory; it uses the built-in defaults
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
`<scope>/<name>-sdk`, `<scope>/<name>`.

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

### `schema_ir_go_module`

Default: `github.com/parable-work/superschematic/ir`

Go module path of the schema IR.

### `schema_runtime_go_module`

Default: `github.com/parable-work/superschematic/runtime/schema/go`

Go module path of the schema runtime.

### `http_runtime_go_module`

Default: `github.com/parable-work/superschematic/runtime/http/go`

Go module path of the HTTP runtime.

### `http_runtime_rust_crate`

Default: `superschematic-http-runtime`

Cargo crate name of the HTTP runtime. Generated Rust API crates import
it as the identifier form of this name (`superschematic_http_runtime`).

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
that maps to a declaring package).

## `[cache]`

Both keys are optional. The zero value keeps the platform default root
and hashes no extra files.

### `cache.root`

Default: unset (XDG cache directory, under `superschematic/build`)

Build cache directory. `~` expands. `SUPERSCHEMATIC_BUILD_CACHE_DIR` and
`--cache-root` override it, in that order.

### `cache.inputs`

Default: unset (no extra files)

Repo-relative files hashed into every `build-all` cache key alongside the
schema tree, the tool tree and the workspace lockfile: files generation
reads that live outside the schema tree.

## `[paths]`

In-tree locations generated modules point path dependencies at (`go.mod`
replace, Cargo `path`, npm `file:`). Every key is optional and
repo-relative. The repository root is the parent of the schemas root. An
unset key emits no path dependency, so the generated manifest resolves
the published module.

### `paths.scalar_go`

Default: unset. This repository's own file sets
`third_party/superscalar/go`.

Directory of the scalar library's Go module (`go.mod`).

### `paths.scalar_typescript`

Default: unset. This repository's own file sets
`third_party/superscalar/bindings/typescript`.

Directory of the scalar library's npm package (`package.json`).

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

### `paths.http_runtime_go`

Default: unset. This repository's own file sets `runtime/http/go`.

Directory of the HTTP runtime Go module.

### `paths.http_runtime_rust`

Default: unset. This repository's own file sets `runtime/http/rust`.

Directory of the HTTP runtime Rust crate.

### `paths.ptr`

Default: unset.

Directory of the `ptr` Go module when it is a module of its own rather
than a package of the schema runtime.

## `[extension.<name>]`

Undecoded tables handed to the extension whose `Name()` matches
`<name>`. The core rejects no keys here; the extension does. A table for
an extension the binary does not link is ignored at load and unused.

acme reads `[extension.acme]` for a region string. See
[write an extension](/superschematic/guides/write-an-extension/).
