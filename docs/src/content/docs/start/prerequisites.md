---
title: Prerequisites
description: The tools superschematic needs before the first build, and what each one is for.
sidebar:
  order: 1
---

superschematic is pre-release. Nothing is published to npm, PyPI or
crates.io yet, and its scalar library,
[superscalar](https://github.com/parable-work/superscalar), has no release
either. So you build the CLI, and the libraries generated code links, from
a checkout of this repository. That is why the list below includes a Rust
toolchain even if you never generate Rust.

## Tools

The versions are the pins in `tools.env`, which CI reads too.

| Tool | Version | What it is for |
| --- | --- | --- |
| Go | 1.26.4 | The CLI is a Go binary, and generated Go code targets this version. |
| A C compiler | any | The CLI and generated Go code link superscalar through cgo (`CGO_ENABLED=1`). |
| Rust (rustup) | superscalar's checkout pins its own toolchain | Builds superscalar's static archive and the version-graph archive from source. |
| git | any | Checks superscalar out at the commit in `superscalar.pin`. |
| Node | 22.12 or newer; 24 for the engine | Builds superscalar's TypeScript binding and the docs site, and runs [the engine](/superschematic/guides/engine/). |
| Bun | 1.4 | Installs the TypeScript runtimes and the generated TypeScript packages, which use the `workspace:` protocol. |
| Python and uv | 3.12, uv 0.12 | Only for Python output: the Python schema runtime. |
| Postgres | 16 | Only to run the SQL and the Go ORM a DB schema generates. |
| jq | any | The example scripts read JSON output with it. |

## Set up the checkout

```sh
git clone https://github.com/parable-work/superschematic
cd superschematic
make setup
make build
```

`make setup` does the work that publishing will later remove:

1. checks out superscalar at the pinned commit under `third_party/superscalar`
   and builds its Go static archive and TypeScript binding
   (`scripts/superscalar-dep.sh`);
2. builds the version-graph core's static archive
   (`scripts/versiongraph-archive.sh`), which only a schema that declares a
   [version graph](/superschematic/reference/version-graphs/) links;
3. installs the TypeScript authoring packages and runtimes with Bun, and
   the Python schema runtime with uv.

`make build` writes the CLI to `bin/superschematic`. Check it:

```sh
bin/superschematic --help
```

It lists the core's six commands: `build`, `build-all`, `migrate`,
`format`, `json-schema` and `behaviors`.

## Compile generated Go code

The Makefile sets `GOTOOLCHAIN` and `CGO_LDFLAGS` for its own targets. When
you run `go build` or `go test` on generated code yourself, export them in
your shell first, from the checkout:

```sh
export GOTOOLCHAIN=go1.26.4
export CGO_LDFLAGS="$(scripts/superscalar-dep.sh --print) $(scripts/versiongraph-archive.sh --print)"
```

The two scripts print the `-L` directories of the archives they built; with
`--print` they build nothing.

## Point your schemas at the checkout

Until the packages are published, two files in your schemas root refer to
the checkout:

- `tsconfig.base.json` maps the authoring packages
  (`@superschematic/schema-config`, `@superschematic/schema`,
  `@superschematic/db`, `@superschematic/api`) and `superscalar` to their
  sources, so schema files type-check.
- The `[paths]` table of `superschematic.toml` points the generated
  packages' path dependencies (`go.mod` replace lines, npm `file:`
  specifiers) at the runtimes, so the generated code compiles.

`examples/acme-shop/schemas` has both, and
[Getting started](/superschematic/start/getting-started/) walks through
them.
