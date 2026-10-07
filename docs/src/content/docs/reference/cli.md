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

The core has seven commands: `build`, `build-all`, `migrate`,
`json-schema`, `format`, `behaviors` and the `stack` group, beside
cobra's own `help` and `completion`. The installed `superschematic` links
the official extensions (the gcp target, the Cloudflare DNS platform and
the Pulumi provisioner), which add none. The migration runner,
`superschematic-migrate`, is a binary of its own ([The runner](/superschematic/reference/migrations/#the-runner)).

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

`--scaffold` writes the implementation of each Go API the command builds
whose package is missing: an `implementation.go` at the naming file's
`[implementation_paths]` `go` template (`go/{service}` from the parent of
the schemas root by default). Its `New` has the signature of the generated
`Constructor`, `func(deps Deps) (Implementations, error)`, and each
method answers 501 until it is implemented. It never writes into a
directory that holds a Go file. Without the flag a build writes nothing
outside the output root, except a stack's: building a `Stack` service
writes each Go server's entrypoint under `server/<stack>/<server>` in the
output root and scaffolds, with no flag, each API its servers serve whose
implementation is missing, with a `go.mod` beside it when no module holds
the package.

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
| `--scaffold` | false | write the implementation scaffold of each Go API built whose package is missing, at the `[implementation_paths]` `go` template |

## `build-all <services-root>`

Write every service's sentinel that is missing or stale, then discover
every schema service under `<services-root>` and build them in one
process, in dependency order. The sentinels come first because a config
may import a sibling's. A service's `authDb` counts as a dependency for
ordering. The build orders outputs for `calls`: each API's server builds
after the SDK of every API it calls, so two APIs may call each other. A
service whose callee comes after it builds its other outputs in its place
and its server later, and `--parallel` names the two steps
`shop-api (base)` and `shop-api (server)`. Discovery fails, before any
service is built, on a handle whose kind is not the kind of the service it
names (`shop-orders: calls names shop-db with kind API, but shop-db is kind DB`),
and on a cycle of `dependencies` and `authDb`, which it names edge by edge
(`circular dependency involving shop-db: shop-db depends on shop-api, shop-api authenticates against shop-db`).
`build --with-deps` runs the same discovery and ordering. A service whose API server, SDK or DB kind needs
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
| `--scaffold` | false | write the implementation scaffold of each Go API whose package is missing, as `build --scaffold` does; a cached service whose implementation is missing builds again |

Every service `build-all` builds gets a stamp,
`<schemas-root>/dist/.build-stamps/<service>`, holding the hash of the
inputs it was built from; a later step can compare it to decide whether the
generated output is current. Without `--cache`, `build-all` removes
`<output-root>/.build-stamps` at the start and writes a fresh stamp per
service. With `--cache`, a service whose input hash matches a stamp and
whose outputs still exist is skipped; a miss restores from the cache or
rebuilds.

A service's input hash covers its directory, the hashes of its
`dependencies` and its `authDb`, and each API it `calls` without that
API's own calls, so two APIs that call each other hash without a cycle.
It also covers the naming file's resolved values except `[deps]`, the
files `[cache] inputs` lists, the schemas root's `package.json` and
`bun.lock`, and the `go`, `bun`, `rustc` and `cargo` versions on the
`PATH`.

The input hash covers a hash of the running binary too, so rebuilding the
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

A service whose decorators take other services' handles gets
`<schemas-root>/dist/.schema-references/<service>.json` the same way. It
lists the services the decorators reference, and the sentinel files of
those a decorator only names, in an argument it declares an identity
(D41). The input hash covers each referenced service's
sources and those of every service its config reaches through
`dependencies`, `authDb` and `calls`, and each listed sentinel. So an edit
to a referenced service rebuilds the service that references it. A
reference does not order the build, so two services may name each other.
The IR lists the references under `references`, and a JSON or YAML schema
file states them there, as it states `imports`.

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

The output of the core with no extension linked, under the built-in
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

The input's format comes from its extension: `.schema.ts`,
`.schema.json`, or `.schema.yaml` or `.schema.yml`. Converting a file to
its own format fails. The converted file is written next to the input as
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
`@behavior` decorators. It imports each scalar namespace from the npm
package the linked extension's scalar catalog names for it (`Acme` from
`@acme/schema`), and the others from superscalar. A file with a scalar
that superscalar does not have, in a namespace no catalog names a package
for, fails to convert to `ts`, since the import would not resolve.

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

## `stack dev [<stack-service-dir>]`

Run an environment of a stack on the `local` target until Ctrl-C. The
design is section 8.3 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md).

1. Build the Stack service and every service it reaches, each with its
   dependencies, as `build --with-deps` builds one service; the stack
   builds last.
2. Read the environment the build resolved to
   `<out>/stack/<stack>/<environment>/environment.json`: `--environment`,
   or the stack's one environment on the local target.
3. Start the environment's Postgres container,
   `superschematic-<stack>-<environment>-postgres`, published on
   127.0.0.1 only, and create a database per DB schema it hosts.
4. Migrate each database to its schema's model: a plan from the model the
   database recorded, applied with `superschematic-migrate`, expand and
   contract back to back. The runner must be on `PATH`, or named by
   `SUPERSCHEMATIC_MIGRATE`; [Schema migrations](/superschematic/reference/migrations/)
   says how to install it.
5. Build each server's entrypoint module at `<out>/server/<stack>/<server>`
   with `go build`, start it with its resolved config and `PORT`, callees
   first, and wait until it answers `/readyz`. Each line a server prints is
   printed with its name in front.

Dev stays in the foreground until Ctrl-C or until a server exits, then
stops the servers, callers first, and the container, which keeps its data
for the next run. `--remove-database` removes the container and its data
instead.

A secret a server reads comes from
`<schemas-root>/.superschematic/local/<stack>/<environment>/secrets.env`, a
line per secret, `<Type>.<FIELD>=<value>`, such as
`PaymentsSecrets.STRIPE_KEY=sk_test_...`. A value that is not plain is a Go
quoted string. The `.superschematic` directory ignores itself in git, and
also holds the Ed25519 key pair each server signs its calls to another
with; no secret and no private key reaches the output root.

Without a directory, dev runs the working directory when it is a Stack
service, else the one Stack service under `./schemas/services`. It needs
Docker and Go.

```
superschematic stack dev ./schemas/services/shop-stack
superschematic stack dev ./schemas/services/shop-stack --environment Dev --remove-database
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--environment`, `-e` | the stack's one local environment | the environment to run; it must be on the `local` target |
| `--out` | `<schemas-root>/dist` | output root for generated artifacts; the provisioner renders its program to `<out>/program/<stack>/<environment>` |
| `--remove-database` | false | on exit, remove the Postgres container and its data instead of stopping it |
| `--naming` | `<stack-service-dir>/../../superschematic.toml` | naming config file |

A local environment sets the container's image and host port with its
values, and a server's port with its settings; a port it leaves out comes
from a hash of the stack, the environment and the server, so it stays the
same from run to run:

```ts
@environment({
  target: "local",
  local: { postgresImage: "postgres:16-alpine", postgresPort: 55432 },
  settings: [{ of: ShopApi, port: 8080 }],
})
export abstract class Dev {}
```

## `stack bootstrap`, `secrets set`, `plan`, `build`, `deploy`, `destroy` and `outputs`

The cloud half of the `stack` group bootstraps, plans, builds, deploys and
destroys the environments a Stack service declares
(`docs/stack-model.md`, sections 7.3, 11.1 and 11.2). Each command loads
the stack, resolves one environment as the `stack` generator does, and
drives the environment's target and provisioner. A binary deploys to a
target only when it links the target's extension and the provisioner's
(`gcp.Extension{}`, `pulumi.Extension{...}`). An environment on the
`local` target runs with `stack dev`; `plan`, `build`, `deploy`,
`bootstrap`, `destroy` and `outputs` refuse it, and `secrets set` writes
its `secrets.env`.

Each of these commands takes these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--stack` | the working directory when it is a Stack service, else the one Stack service under `./schemas/services` | the Stack service directory |
| `--naming` | `<stack>/../../superschematic.toml` | naming config file |
| `--program-dir` | `<schemas-root>/dist/program/<stack>/<environment>` | where to render the provisioner's program |
| `--param` | none | a parameter's value for one run of a parameterized environment, `<name>=<value>`; repeatable. `plan`, `build`, `deploy`, `destroy` and `outputs` take it |

### `stack bootstrap <environment>`

Prepare the cloud project the environment deploys to, with an owner's
credentials, once; it is safe to run again. On gcp it enables the APIs,
creates the state bucket and its KMS key, applies the Artifact Registry
repository, the `deployer` and `planner` accounts, the `builder` account
image builds run as, the `migrator` account the migration job runs as,
and Workload Identity Federation for the GitHub repository, and creates
the secret of each platform credential the environment needs. Then it asks for each
credential with no value, with the terminal's echo off.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--repository` | read from the git remote `origin` | the GitHub repository the CI runs in, `owner/name`; `""` leaves Workload Identity Federation out |

### `stack secrets set <environment> [Type.FIELD]`

Ask for the value of every secret of the environment that has none, and
every platform credential, with the terminal's echo off, and store each in
the target's secret store (Secret Manager on gcp), or, for a local
environment, in the `secrets.env` `stack dev` reads. Name one secret, by
the type that declares it and its field, to replace its value. It needs a
terminal. On a fresh cloud environment, deploy first: its infrastructure
step creates each secret's storage.

```
superschematic stack secrets set Staging
superschematic stack secrets set Staging PaymentsSecrets.STRIPE_KEY
```

### `stack plan <environment>`

Show what `stack deploy` would do, changing nothing: the provisioner's plan
of every resource, with each server's image pinned, and each database's
migration plan from the schema the deploy manifest records. It also lists
the secrets with no value, the servers with no image yet (which a deploy
builds), the migration
phases a failed deploy left part-way, and the records to create by hand
for a domain no DNS platform holds. It exits 1 after printing when a plan
has a hazard of a `--fail-on` class that no `--allow` names.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--image` | the manifest's | a server's image, `<server>=<repository>@sha256:<digest>`; repeatable |
| `--fail-on` | `all` | hazard classes, comma-separated, `all`, or `none` |
| `--allow` | none | a hazard id to acknowledge; repeatable |
| `--out` | none | write the plan as JSON, for `stack deploy --expect` |
| `--format` | `text` | print the plan as `text` or `json` |

### `stack build <environment>`

Build the image of each server whose build context changed since the image
the deploy manifest records, as `stack deploy` would, and deploy nothing.
A Go server builds from the Dockerfile `superschematic build-all` writes at
`<output-root>/server/<stack>/<server>/`, with the repository root as its
context, cut down by the `Dockerfile.dockerignore` beside it; on gcp the
build runs on Cloud Build and pushes to the stack's Artifact Registry
repository. It prints each image as a `stack deploy` flag:

```
$ superschematic stack build Staging
--image Orders=us-east1-docker.pkg.dev/acme-staging/shop/orders@sha256:...
--image shop-api=us-east1-docker.pkg.dev/acme-staging/shop/shop-api@sha256:...
```

A build writes no deploy manifest.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--server` | every server with a Dockerfile | build this server only; repeatable |
| `--force` | false | build a server whose context did not change |
| `--out` | none | write the result as JSON |
| `--format` | `text` | print `--image` flags (`text`) or the result as `json` |

### `stack deploy <environment>`

Deploy the environment in deploy order: infrastructure, each database's
`expand` phase, the servers wave by wave, callees first, the `contract`
phases, exposure. Before any step, it builds the image of each server
`--image` names none for whose build context changed since the image the
manifest records, as `stack build` does; a server with no Dockerfile keeps
the manifest's image. The deploy manifest records each step, and the
context each image it built came from. Every secret needs a value before
the first step after infrastructure; at a terminal the deploy asks for
each one missing. On gcp each migration phase runs as an execution of the
stack's Cloud Run job, `<stack>-migrate`, which also gives each server
that connects to a database its privileges. A rollout that fails runs no
`contract` step, and the next deploy plans from the schema between the
phases.

A binary built from a checkout names no release of the migration runner
to build the job's image from: set `SUPERSCHEMATIC_MIGRATE_IMAGE` to an
image of `superschematic-migrate`, by digest, in the stack's repository.

```
superschematic stack deploy Staging
superschematic stack deploy Staging --image shop-api=us-east1-docker.pkg.dev/acme-staging/shop/shop-api@sha256:...
superschematic stack plan Preview --param pr=123 --out plan.json
superschematic stack deploy Preview --param pr=123 --expect plan.json --allow 'destructive:table/order/column/total'
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--image` | a build, else the manifest's | a server's image, `<server>=<repository>@sha256:<digest>`; repeatable. A server with none of them is refused |
| `--no-build` | false | build no image: take each from `--image` or the manifest |
| `--fail-on` | `all` | hazard classes that stop the deploy unless `--allow` names each hazard, comma-separated, `all`, or `none` |
| `--allow` | none | a hazard id to acknowledge; repeatable |
| `--expect` | none | a plan `stack plan --out` wrote: refuse migration plans other than its |

### `stack destroy <environment>`

Remove every resource of the run and its deploy manifest. It asks for the
run's name at a terminal unless `--yes`, and refuses without either.

### `stack outputs <environment>`

Print the run's outputs file, the `outputs.json` the bindings generator
reads: a JSON object with the format's `version`, the `stack`, the
`environment`, the run's `parameters` for a member of a parameterized
environment, and under `resources` the outputs of the run's applied
resources by node ID and output name, leaving out secret ones.

```json
{
  "version": 1,
  "stack": "shop-stack",
  "environment": "Staging",
  "resources": {
    "shop-api.service": { "url": "https://shop-api-3kq7x2-ue.a.run.app" }
  }
}
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | stdout | write the outputs file to this path; put it beside the environment's `environment.json` for the bindings generator |

## Extension commands

An extension that implements `cli.CommandProvider` adds its commands to
the root. acme adds `describe [<schemas-root>]`. See
[write an extension](/superschematic/extending/write-an-extension/).
