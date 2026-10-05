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

The core binary has six commands: `build`, `build-all`, `migrate`,
`json-schema`, `format` and `behaviors`.

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

A config may import a sibling's sentinel (`import { ShopDb } from
"@acme/shop-db"`). When the target's config imports anything but
`@superschematic/schema-config`, or its kind's schema files import
sentinels, `build` first writes every sibling's sentinel that is missing
or stale, as `build-all` does. `--with-deps` always does. A config that
imports anything else (a class, a type, another module's namespace, a
default export or a module for its side effects) fails to load with a
message naming the import, in every build command:
`schema.config.ts imports Product from "@acme/shop-db", which is a class, not a service sentinel; a config imports only @superschematic/schema-config and other services' sentinels (D34)`.

A type that composes a behavior (`behaviors` in the data forms) loads,
and `--emit-ir` prints it, but no generator renders behaviors yet: the
build fails and names the first generator that would run, the type and the
behavior.

A generated package that imports a package the build does not generate
fails the build:

- The API server and every `outputs.sdk` language need the same
  `outputs.types` language, since each imports the service's types
  package in its language. `outputs.api` needs `outputs.types.go` with
  `language: GO` (the default), `outputs.types.rust` with `RUST` and
  `outputs.types.typescript` with `TYPESCRIPT`. The config fails to load,
  before anything is built:
  `outputs.api with language GO needs outputs.types.go: the Go API server decodes requests into the Go types and its handler interfaces take and return them`,
  or `outputs.sdk.typescript needs outputs.types.typescript: the TypeScript SDK decodes responses and validates inputs with the TypeScript types`.
- A DB schema needs `outputs.types.go`: the kind always generates the Go
  ORM, which imports the Go types. The build fails before its generators
  run, and `--with-deps` and `build-all` fail at discovery:
  `kind DB needs outputs.types.go: the Go ORM, which the DB kind always generates, imports the Go types`.
  An API server that names the DB as its `authDb` imports its ORM, so the
  rule covers it too.
- A type library imports the types of each dependency it takes an enum, a
  union or an object type from (an imported scalar is regenerated locally),
  so that dependency must enable the same `outputs.types` languages.
  Otherwise the Rust crate path-depends on a crate that is not there, the
  Go module and the TypeScript package name a missing one, and the Python
  package binds the imported types to `Any` and skips their validation.
  `--with-deps` and `build-all` read the dependencies' configs and fail
  before the service's generators run, one line per dependency:
  `shop-orders generates Rust types, which use shop-common's Rust types; enable outputs.types.rust in shop-common`.
  A plain `build` does not read them: it builds, and logs a
  `- not checked:` line naming the dependencies and the switches they need.

A General schema whose class carries `@envVars` gets the class's
`values-schema.json` in `api/<name>`, and an environment loader next to it
in the language `outputs.types` picks: Go when `go` is on, Rust when only
`rust` is. The Go loader imports the Go types, so with neither on the build
writes no loader there, rather than a Go module that cannot compile. When
`typescript` is on, the TypeScript types package gets the TypeScript
loader, `config.ts`, exported as `<types package>/config`, whichever
standalone loader the build writes. With none of the three the build logs
`- env-config: values-schema.json only, no loader; enable outputs.types.go for the Go loader, which imports the Go types, outputs.types.rust for the Rust one, or outputs.types.typescript for the TypeScript one`.

`--with-deps` also builds every service the target transitively depends
on (declared `dependencies`, `authDb` and `calls`), dependencies first. The
closure is resolved from the sibling services under the target's parent
directory with the discovery, ordering and schema catalog `build-all`
uses; siblings outside the closure are not built. It writes no
`.deps.json` and runs no `BuildAllHook`s, since both describe the whole
services root. It cannot be combined with `--emit-ir`.

`--api-language` builds the target's API server in another language
(`GO`, `RUST` or `TYPESCRIPT`, any case) than its config's
`outputs.api.language`, for example a Rust server of a service whose
committed config builds a Go one. It changes the target only, never a
dependency that `--with-deps` builds, and leaves the config on disk as it
was. The target must enable `outputs.api` and the types of that language,
as a committed language must. Pair it with `--out`, so the two servers do
not share an output root; a build's cache stamps come from `build-all`,
which has no such flag.

```
superschematic build ./schemas/services/shop-db
superschematic build ./schemas/services/shop-db --emit-ir | jq .types
superschematic build --with-deps ./schemas/services/shop-api
superschematic build --with-deps --api-language RUST --out ./schemas/dist-rust ./schemas/services/shop-api
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--emit-ir` | false | print the Schema IR as JSON to stdout; do not generate code |
| `--with-deps` | false | also build the target's transitive dependencies (declared dependencies, `authDb` and `calls`), dependencies first |
| `--out` | `<service-dir>/../../dist` | output root for generated artifacts |
| `--profile` | false | emit build phase timings to stderr |
| `--skip-format` | false | skip developer-friendly formatting for generated files |
| `--naming` | `<service-dir>/../../superschematic.toml` | naming config file |
| `--api-language` | the config's | build the target's API server in this language (`GO`, `RUST` or `TYPESCRIPT`) |

## `build-all <services-root>`

Write every service's sentinel that is missing or stale, then discover
every schema service under `<services-root>` and build them in one
process, in dependency order. The sentinels come first because a config
may import a sibling's. A service's `authDb`, and each API it
`calls`, count as dependencies for ordering. Discovery fails, before any
service is built, on a handle whose kind is not the kind of the service it
names (`shop-orders: calls names shop-db with kind API, but shop-db is kind DB`),
and on a cycle, which it names edge by edge
(`circular dependency involving shop-api: shop-api calls shop-orders, shop-orders calls shop-api`).
Two APIs cannot call each other yet. `build --with-deps` runs the same
discovery. A service whose API server, SDK or DB kind needs
a types language its config does not enable fails discovery, before any
service is built. A service whose type library imports a
dependency that does not generate types in that language fails, as with
`build --with-deps` (see [`build`](#build-service-dir)). Once every service's output is in place,
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

The input hash covers a hash of the running binary, so rebuilding the
binary with different code invalidates every stamp and cache entry. Build
it with `-trimpath -buildvcs=false`, as `make build` does, so a commit
that changes no Go source keeps the same binary. A binary that links
extensions can set `cli.Config.ToolDigest` instead, so builds from
different checkouts share entries; the
[extension guide](/superschematic/extending/write-an-extension/#share-the-build-cache-across-checkouts)
explains what the digest must cover.

A service with sidecar documents also gets
`<schemas-root>/dist/.authoring-imports/<service>.json`, which lists the
files elsewhere under the schemas root that its documents import. The
input hash covers those files' contents. `build`, `build --with-deps` and
`build-all` all write it under the schemas root they resolved, whatever the
schemas root is named and wherever `--out` points.

## `migrate plan <service-dir>`

Plan the migration of a DB service's database from a previous version of
its schema to the one in `<service-dir>`, with no database at hand. The
plan is ordered steps in two phases, `expand` before the new servers roll
out and `contract` after, each with its SQL and its hazards.
`superschematic-migrate` applies it.
[Schema migrations](/superschematic/reference/migrations/) covers the plan,
the hazard classes, readers, renames and the runner.

The previous version is another checkout of the service (`--from
<service-dir>`), the model a database recorded (`--from <model.json>`), or
the schemas root at a git ref (`--from-ref`); with none, the plan starts
from an empty database. Each version loads with its dependencies resolved
from its own schemas root, as `build --with-deps` resolves them, and with
its own naming file when it has one: like `build-all`, it first writes a
missing or stale sentinel there, since a config may import a sibling's.
Both resolve to models with the options a build passes the `sql`
generator.

The API and General services in each version's schemas root are that
version's readers: the columns their `@source` views read. A step that
drops, renames or retypes a column a reader live at its phase reads is
`api-breaking` for that reader. `--reader` adds a service that lives
elsewhere; it counts on both sides.

`--dialect sqlite` plans the service's SQLite database, with the copy-table
rebuild where SQLite's `ALTER TABLE` falls short
([SQLite](/superschematic/reference/migrations/#sqlite)). The service's
`outputs.sql.dialects` must list `sqlite`, and so must the previous
version's when it is a service directory or a git ref. A database whose
previous version was not built for SQLite plans from the model it
recorded (`--from <model.json>`).

After a rollout that failed between a plan's phases, the database holds
the model between them. Plan from that model (`--from <model.json>`, as
`superschematic-migrate status --model` prints it): the new plan supersedes
the pending contract
([A failed rollout](/superschematic/reference/migrations/#a-failed-rollout)).

`--format` prints the plan to stdout; notes, such as planning from an empty
database, go to stderr. With `--fail-on`, the command prints the plan,
then lists on stderr each hazard of a listed class that no `--allow` names,
with the `--allow` that lets it pass, and exits 1.

```
superschematic migrate plan ./schemas/services/shop-db
superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --format markdown --fail-on destructive,compat
superschematic migrate plan ./schemas/services/shop-db --from-ref origin/main --rename order.total=order.amount --out plan.json
superschematic migrate plan ./schemas/services/shop-db --from applied-model.json --out plan.json
superschematic migrate plan ./schemas/services/shop-db --print-model > model.json
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--from` | none | the previous version: another checkout of the service directory, or a model JSON file as `superschematic-migrate status --model` prints it |
| `--from-ref` | none | the previous version: the schemas root at this git ref, read with `git archive`; cannot be combined with `--from` |
| `--rename` | none | a rename: `old=new` for a table, `oldTable.oldColumn=newTable.newColumn` for a column; repeatable |
| `--reader` | none | an API or General service directory outside the schemas root whose `@source` views read the database; repeatable |
| `--dialect` | `postgres` | the database dialect: `postgres`, or `sqlite` for a service whose `outputs.sql.dialects` lists it |
| `--out` | none | write the plan JSON, in canonical form, to this file |
| `--format` | `sql` | print the plan to stdout as `json`, `sql` or `markdown` |
| `--fail-on` | none | hazard classes, comma-separated, or `all`; exit 1 when the plan has a hazard of one that no `--allow` names |
| `--allow` | none | a hazard id `--fail-on` lets pass; repeatable |
| `--print-model` | false | print the new version's model as canonical JSON, for `superschematic-migrate adopt`, and plan nothing; takes none of the plan flags |
| `--naming` | `<service-dir>/../../superschematic.toml` | naming config file |

## `json-schema`

Print the JSON Schema the JSON and YAML readers validate against. The
schema is generated by reflection from the IR structs, so unknown keys
and unknown decorator names fail validation. A binary that links
extensions includes their kinds, decorators, documents and behaviors: a
type's `behaviors[].name` is one of the registered behaviors, and each
`config` is held to that behavior's config schema.

```
superschematic json-schema
superschematic json-schema --config
superschematic json-schema --naming ./schemas/superschematic.toml
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--config` | false | emit the `schema.config.{json,yaml}` JSON Schema instead |
| `--naming` | built-in names | naming config file; this command has no service directory to discover one from |

The output of the binary with no extension linked, under the built-in
names, ships in the `@superschematic/schema-ir` npm package as
`schema-file.json`, next to TypeScript types for the same documents:

```ts
import type { Document, SchemaFile } from "@superschematic/schema-ir/schema-file";
```

The types leave open the parts a binary's registry closes: the extension
slots, the documents, the schema kinds, the MCP invocation policy key and
the behavior names and configs. The JSON Schema gives the policy key its
default, which the readers fill in for a visible tool that omits it. The
readers also drop a property that holds a value the IR cannot tell from an
absent key, such as `false` or an empty string or list, and the JSON
Schema gives each such property that value as its default. A behavior's
`config` of `{}` is stored as no config, and its default is `{}`.
`@superschematic/schema-runtime`'s
`SchemaFileLoader` loads schema files against this output, or against a
binary's own.

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

The file is read with the binary's registry, so a file that uses a linked
extension's kind, decorators, documents or behaviors converts between JSON
and YAML with its extension data. The TypeScript writer cannot render an
extension's decorators: converting such a file to `ts` fails and names the
extension slot instead of dropping it. It writes a type's behaviors as
`@behavior` decorators.

## `behaviors --out <dir>`

Write the declaration of every behavior the binary registers into the npm
package that implements it for `@superschematic/engine`: one
`<name>.behavior.json` per behavior, the same declaration the Go package
embeds and registers. The engine implementation imports that copy, so the
engine and the compiler read one declaration. Other `*.behavior.json`
files in the directory are removed. `--package` and `--extension` narrow
what is written to the behaviors one npm package implements and the ones
one extension registered.

A copy is canonical rather than the source bytes: the declaration's keys
in `BehaviorDeclaration`'s order, each JSON Schema's object keys sorted
with number literals as written, two-space indents and a final newline.
It changes only when the declaration does.

```
superschematic behaviors --package @superschematic/engine-workqueue --out runtime/engine-workqueue/typescript/src/declarations
acme-schematic behaviors --extension acme --out packages/behaviors/declarations
acme-schematic behaviors --extension acme --out packages/behaviors/declarations --check
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | (required) | the directory the copies go in |
| `--check` | false | write nothing; fail, naming each file, when a copy differs, is missing, or is no registered behavior's |
| `--package` | every behavior | only the behaviors this npm package implements, as their registration names it; fails, naming the packages there are, when it implements none |
| `--extension` | every behavior | only the behaviors this extension (its `Name()`) registered; fails when it registers none |
| `--naming` | built-in names | naming config file; this command has no service directory to discover one from |

It is a command of the binary, not a tool in the core module, because an
extension's declarations are registered only in its own binary: acme's
copy comes from `acme-schematic`, and its smoke runs `--check`. The core
binary writes the behaviors the core declares into the two packages that
implement them: `Workflow`, `Comments`, `Revisions`, `Dependencies`,
`Links`, `Rollups`, `Search`, `Reactions`, `Constants`, `Variants` and
`Branches` with `--package @superschematic/engine`, and `Lease`, `Assignment`, `Queue`, `Presence`,
`Blueprint`, `Budget` and `Retries` with `--package
@superschematic/engine-workqueue` (`make behaviors`; `make
behaviors-check` in CI). Without `--extension`, an extension's binary
writes the core's declarations beside its own.

## Extension commands

An extension that implements `cli.CommandProvider` adds its commands to
the root. acme adds `describe [<schemas-root>]`. See
[write an extension](/superschematic/extending/write-an-extension/).
