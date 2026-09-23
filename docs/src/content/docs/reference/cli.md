---
title: CLI
description: Every superschematic command and flag.
sidebar:
  order: 2
---

The CLI is a library. A binary is one call:

```go
cli.New(cli.Config{}, ext...).Execute()
```

`cli.Config.Name` is the command name in usage text (default
`superschematic`). `Short` and `Long` replace the root descriptions when
set. Extensions that implement `cli.CommandProvider` add subcommands at
`New`. Every command assembles its registry from the naming file it
resolves, so the same command tree serves a core-only binary and one that
carries extensions.

The core binary has four commands: `build`, `build-all`, `json-schema`
and `format`.

## `build <service-dir>`

Load one schema service directory into the Schema IR and run the
generators the kind and the `outputs` block select. Requested outputs
without a registered generator are reported and skipped.

The service directory holds `schema.config.{ts,json,yaml}` plus
`src/*.schema.{ts,json,yaml}`. TypeScript files go through the Corsa
frontend. JSON and YAML files are validated against the schema-file JSON
Schema and decoded directly.

Generated artifacts go to `--out`, which defaults to the `dist/`
directory two levels above the service directory (`<schemas-root>/dist`
for services under `<schemas-root>/services/`). Names come from
`<schemas-root>/superschematic.toml` or `--naming`.

`--with-deps` also builds every service the target transitively depends
on (declared `dependencies` plus `authDb`), dependencies first. The
closure is resolved from the sibling services under the target's parent
directory with the discovery, ordering and schema catalog `build-all`
uses; siblings outside the closure are not built. It writes no
`.deps.json` and runs no `BuildAllHook`s, since both describe the whole
services root. It cannot be combined with `--emit-ir`.

```
superschematic build ./schemas/services/shop-db
superschematic build ./schemas/services/shop-db --emit-ir | jq .types
superschematic build --with-deps ./schemas/services/shop-api
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--emit-ir` | false | print the Schema IR as JSON to stdout; do not generate code |
| `--with-deps` | false | also build the target's transitive dependencies (declared dependencies plus `authDb`), dependencies first |
| `--out` | `<service-dir>/../../dist` | output root for generated artifacts |
| `--profile` | false | emit build phase timings to stderr |
| `--skip-format` | false | skip developer-friendly formatting for generated files |
| `--naming` | `<service-dir>/../../superschematic.toml` | naming config file |

## `build-all <services-root>`

Discover every schema service under `<services-root>` and build them in
one process, in dependency order. A service's `authDb` counts as a
dependency for ordering. Once every service's output is in place,
whether this run built it, restored it from the cache or found it up to
date, registered `BuildAllHook`s run (a chart merge, for example). They
run on a `build-all` that built nothing too.

When finished it writes the dependency graph of the generated packages
to `<output-root>/.deps.json`, and the same bytes to `--deps-copy` or
`[deps] copy` when one is set. The output root is usually ignored by
version control; the copy can be committed so a tool reads the graph
without building. Each package in the graph carries `service`, the
service whose build produced it. A package directory under the output
root that no discovered service writes, such as one a removed service
left behind, fails the build with its path named; delete it and rebuild.

```
superschematic build-all ./schemas/services
superschematic build-all ./schemas/services --parallel --cache
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | `<services-root>/../dist` | output root for generated artifacts |
| `--profile` | false | emit build phase timings to stderr, then a cumulative summary |
| `--cache` | false | share schema outputs across worktrees via a content-addressed cache |
| `--cache-root` | `SUPERSCHEMATIC_BUILD_CACHE_DIR`, then `[cache] root`, then the XDG cache directory | schema output cache root; only used with `--cache` |
| `--parallel` | false | build independent schemas concurrently within each dependency phase |
| `--isolated-ts-programs` | false | one TypeScript compiler program per service instead of the shared program |
| `--skip-format` | false | skip developer-friendly formatting for generated files |
| `--naming` | `<services-root>/../superschematic.toml` | naming config file |
| `--deps-copy` | `[deps] copy`, else none | also write the dependency graph to this path |

Every service `build-all` builds gets a stamp,
`<schemas-root>/dist/.build-stamps/<service>`, holding the hash of the
inputs it was built from; a later step can compare it to decide whether the
generated output is current. Without `--cache`, `build-all` removes
`<output-root>/.build-stamps` at the start and writes a fresh stamp per
service. With `--cache`, a service whose input hash matches a stamp and
whose outputs still exist is skipped; a miss restores from the cache or
rebuilds.

## `json-schema`

Print the JSON Schema the JSON and YAML readers validate against. The
schema is generated by reflection from the IR structs, so unknown keys
and unknown decorator names fail validation. A binary that links
extensions includes their kinds, decorators and documents.

```
superschematic json-schema
superschematic json-schema --config
superschematic json-schema --naming ./schemas/superschematic.toml
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | false | emit the `schema.config.{json,yaml}` JSON Schema instead |
| `--naming` | built-in names | naming config file; this command has no service directory to discover one from |

## `format --to=ts\|json\|yaml <file>`

Convert one schema file between the three authoring formats through the
Schema IR. The file is read by its format's frontend and written back by
the target format's writer, preserving definitions and comment metadata.

JSON and YAML files convert standalone. A TypeScript file is read in the
context of its service (the directory holding `schema.config.*`), since
decorators and types resolve through the compiler. Definitions that
belong to the file convert; service-level definitions that TypeScript
cannot attribute to a file (enums, scalars) ride along with the
service's first schema file.

The converted file is written next to the input as
`<name>.schema.<format>`. An existing file is not overwritten without
`--force`. `--stdout` prints the conversion instead.

```
superschematic format --to=yaml ./src/orders.schema.json
superschematic format --to=ts ./src/orders.schema.yaml --stdout
superschematic format --to=json ./src/orders.schema.ts --force
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--to` | (required) | target format: `ts`, `json`, or `yaml` |
| `--stdout` | false | print the conversion instead of writing a file |
| `--force` | false | overwrite an existing output file |

`format` discovers `superschematic.toml` by walking up from the file.
There is no `--naming` flag.

## Extension commands

An extension that implements `cli.CommandProvider` adds its commands to
the root. acme adds `describe [<schemas-root>]`. See
[write an extension](/superschematic/guides/write-an-extension/).
