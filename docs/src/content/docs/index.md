---
title: superschematic
description: A schema compiler. You write one schema per service; it generates SQL, ORM, REST, types and SDKs.
---

superschematic is a schema compiler. You write one schema per service in
TypeScript, JSON or YAML. The compiler loads it into a Schema IR and runs
the generators that the schema's kind and its `outputs` block select: SQL
DDL, a Go ORM, a REST server (Go chi, Rust axum or TypeScript Hono),
OpenAPI, types in Go, TypeScript, Python and Rust, and SDKs in those same
four languages.

Scalar types (email, UUID, URL, cron, and about forty more) come from
[superscalar](https://github.com/parable-work/superscalar). An email address
parses the same way in generated Go, TypeScript, Python and Rust.

```
schema source (.schema.ts | .schema.json | .schema.yaml)
  -> superschematic build
     -> sql/         Postgres DDL
     -> orm/         Go repositories
     -> api/         Go chi router, middleware, OpenAPI; or a Rust axum crate;
                     or a TypeScript Hono router package
     -> types/       Go, TypeScript, Python, Rust
     -> sdk/         TypeScript, Go, Python, Rust clients
```

Status: pre-release. The first tag is `v0.1.0-alpha.1`. Until it is cut,
nothing is published to npm, PyPI or crates.io, and the Go modules are
consumed at a commit. APIs an extension depends on (`registry`, `loader`,
`cli`, `schemadeps`, `generator.Naming`) are not frozen.

## What the core knows

The core registers three schema kinds (DB, API, General), one auth provider
(`session`) and the generic scalar set. A kind, decorator, document,
generator, auth provider or command that only one deployment needs lives in
an extension: a Go package you pass to `cli.New`. The `cmd/superschematic`
binary links no extension.

`examples/acme-schematic` is a complete downstream example. The
[write an extension](/superschematic/guides/write-an-extension/) guide walks
the same surfaces. `extensions/deploy` and `extensions/platform` are smaller
worked examples; see [deploy](/superschematic/guides/deploy/) and
[platform](/superschematic/guides/platform/).

Names of generated packages come from `superschematic.toml` at the schemas
root. The [naming-file reference](/superschematic/reference/naming/) lists
every key and its default. The [CLI reference](/superschematic/reference/cli/)
lists every command and flag.

## Where to start

Pick the language you will consume generated code in:

- [Go](/superschematic/install/go/)
- [TypeScript](/superschematic/install/typescript/)
- [Python](/superschematic/install/python/)
- [Rust](/superschematic/install/rust/)

Each page installs the CLI, writes a small schema, builds it, and consumes
the generated types (and an SDK when the schema is an API).
