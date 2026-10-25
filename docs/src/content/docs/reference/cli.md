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
carries extensions. A binary hands the error `Execute` returns to
`cli.Exit`, which prints it and exits 1, or, for a program a command ran
that already said why, such as `superschematic-identity`, exits with that
program's status.

The core has fourteen commands: `init`, `check`, `build`, `build-all`, `migrate`,
`json-schema`, `format`, `behaviors`, `engine-client`, `env`, `catalog` and the
`new`, `stack` and `identity` groups, beside cobra's own `help` and
`completion`. The
installed `superschematic` links the official extensions (the gcp target, the Cloudflare DNS platform and
target, and the Pulumi provisioner), which add none. The migration runner,
`superschematic-migrate`, is a binary of its own ([The runner](/superschematic/reference/migrations/#the-runner)),
and so is the identity runner, `superschematic-identity`
([below](#the-identity-runner-superschematic-identity)): each holds the
database drivers, which stay out of the compiler.

## `init [<schemas-root>]`

Write a schemas root, the directory every other command reads, so no one
writes its boilerplate by hand (D71). Without a directory, it writes
`./schemas`.

| File | What it holds |
| --- | --- |
| `superschematic.toml` | `go_module_root` and `npm_scope`, and a [`[paths]`](/superschematic/reference/naming/#paths) table pointing the generated packages' path dependencies at a superschematic checkout, relative to the parent of the schemas root; `build_context` too when the checkout holds the project |
| `tsconfig.base.json` | `strict`, `experimentalDecorators` and the options acme-shop's has, with `paths` mapping `@superschematic/*` and `superscalar` to the checkout's sources and `<npm_scope>/*` to `./services/*/src/index.ts` |
| `.gitignore` | the output root's contents, but for the lockfiles of its Bun and Cargo workspaces: `!/dist/`, `/dist/*`, `!/dist/bun.lock`, `!/dist/Cargo.lock` (D51) |
| `services/` | empty: [`new service`](#new-service-name---kind-dbapigeneralbucketsite) adds each service |

Nothing of superschematic is published yet, so the schema files compile
against a checkout that `make setup` has prepared: `--superschematic`
names it, and without the flag init takes the checkout that holds the
schemas root. Outside one, it asks at a terminal and otherwise refuses.
At a terminal it asks for each value no flag gives, each with its
default. Without one, the module root is `example.com/<project>` and the
scope `@<project>`, the project being the directory that holds the
schemas root, in lower case.

init writes no file that exists: when any of the three does, it names
each and writes nothing. It ends with the next command, `superschematic
new service`.

```
superschematic init --superschematic ~/src/superschematic
superschematic init ./schemas --go-module-root example.com/acme --npm-scope @acme
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--go-module-root` | `example.com/<project>` | the prefix of every generated Go module path |
| `--npm-scope` | `@<project>` | the npm scope of the generated packages and of the services' own |
| `--superschematic` | the checkout that holds the schemas root | the superschematic checkout the schema files and generated packages build against |

## `new service <name> --kind DB|API|General|Bucket|Site`

Write a service into a schemas root and build it once (D71). The name is
lower-case words joined by hyphens, such as `shop-db`; `--kind` takes the
kind in any case. A Stack service is [`stack init`](#stack-init-services-root)'s.

It writes `<schemas-root>/services/<name>/`:

| File | What it holds |
| --- | --- |
| `schema.config.ts` | `defineConfig` with the name, the kind and an `outputs` block that loads |
| `package.json` | `{ "name": "<npm_scope>/<name>", "private": true }`, the package other services import |
| `tsconfig.json` | extends `../../tsconfig.base.json`; includes `schema.config.ts` and `src/**/*.ts` |
| `src/index.ts` | re-exports the starter's declarations and `./service.generated` |
| `src/<name>.schema.ts` | a starter to rename and grow: a table `Item` with a `@key` for a DB, an operation `ItemQueries.getItem` (`GET items/{id}`) returning `ItemView` for an API, an object type `Item` for General |

| Kind | `outputs` |
| --- | --- |
| `DB` | Go and TypeScript types; a DB always needs Go types |
| `API` | Go and TypeScript types, a Go server (`api`), Go and TypeScript SDKs |
| `General` | Go and TypeScript types |
| `Bucket` | `{}` |
| `Site` | none |

A Bucket and a Site declare nothing in schema files, so they get no
starter schema: an API lists a bucket in its `buckets`, and a site's
config lists the APIs its browser code calls in `calls`.

Last, it builds the service as [`build`](#build-service-dir) does, into
`<schemas-root>/dist`, which writes `src/service.generated.ts`, the
handle other services' configs import. A Site's build also writes its
code's mirror under `web/<name>`, as every build of a Site does. A build
that fails leaves the files written and says so.

It writes no file that exists: when any does, or a config in another
form, it names each and writes nothing. It refuses a name another
service's config has, and a schemas root with no `tsconfig.base.json`.

```
superschematic new service shop-db --kind DB
superschematic new service shop-api --kind API --root ./schemas
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--kind` | (required) | the service's kind: `DB`, `API`, `General`, `Bucket` or `Site` |
| `--root` | the working directory when it holds `superschematic.toml`, else `./schemas` | the schemas root |
| `--naming` | `<schemas-root>/superschematic.toml` | naming config file |

## `check [<service-dir>|<services-root>]`

Load and verify schema services without generating code, so a schema
edit's verdict comes back in tens of milliseconds instead of a build's
seconds (D72). Check reads each service through the loader every build
command uses: the TypeScript compiler's diagnostics, the data forms' schema
checks, the config rules (D34), the registry's kind, decorator and check
rules, and the verification pass, warnings included. It writes nothing
under the output root. Like a build it writes each service's sentinel,
`src/service.generated.ts`, the handle other services' configs import,
when it is missing or stale.

Given a service directory, it checks that service and every service it
depends on (`dependencies`, `authDb`, `calls` and `buckets`), as `build
--with-deps` would build them; given a services root, or a schemas root
holding `services/`, every service under it. Without an argument it
checks the working directory when it is a service, else
`./schemas/services` when there is one, else the working directory as a
services root.

Each finding is printed as a compiler prints one, then a summary:

```
schemas/services/shop-db/src/shop-db.schema.ts:12:3: error: Cannot find name 'Money'.
Checked 3 schema services in 41ms: 1 error; failed: shop-db; blocked: shop-api (by shop-db)
```

A service whose load fails only because a service it reads failed, such
as an API whose user model reads its `authDb`, is `blocked` by that
service rather than reported with the same errors again. A finding in a
dependency's file is the dependency's. The command exits 1 when any
finding is an error; warnings, such as a view that omits a
`@sourceMustProject` field, leave it at 0.

`--json` writes one document instead, for a coding agent or an editor to
read:

```json
{
  "ok": false,
  "servicesRoot": "schemas/services",
  "checked": ["shop-db", "shop-api"],
  "errors": 1,
  "warnings": 0,
  "durationMs": 41,
  "services": [
    { "name": "shop-db", "kind": "DB", "dir": "schemas/services/shop-db", "status": "error" },
    { "name": "shop-api", "kind": "API", "dir": "schemas/services/shop-api", "status": "blocked", "blockedBy": ["shop-db"] }
  ],
  "diagnostics": [
    { "service": "shop-db", "file": "schemas/services/shop-db/src/shop-db.schema.ts", "line": 12, "column": 3, "severity": "error", "message": "Cannot find name 'Money'." }
  ]
}
```

Paths are relative to the working directory when they are under it, else
absolute. A diagnostic has no `line` and `column` when its finding names a
file and no position, as the JSON and YAML readers' findings do, and no
`file` when it names none.

### `check --watch`

With `--watch`, check runs once, then looks for edits every `--interval`
and, on each, re-checks the services the edited files belong to and every
service that depends on them, transitively, through the configs' edges and
the services a schema's decorator arguments name (D41). It prints each
result as it lands, with the files that changed; with `--json`, one
document per line, with `changed` listing them. An edit to a config, a
service's `package.json` or `tsconfig.json`, the naming file, the base
tsconfig or a new service directory discovers the services again and
re-checks them all. It never exits on errors, and stops at Ctrl-C.

It watches the `.ts`, `.json`, `.yaml`, `.yml` and `.toml` files of each
service outside `node_modules`, `dist`, `target` and hidden directories,
the schemas root's `superschematic.toml` and `tsconfig.base.json`, and the
file `--naming` names. It polls rather than subscribing to file events: a
schema tree is small, the poll costs less than the check it starts, and it
sees edits in a container or on a network file system that events miss.

Watch checks; it does not build. Run `build-all` or `build --with-deps`
when the schemas check clean and you want the generated code, and see
[`stack dev`](#stack-dev-stack-service-dir) for a running stack.

```
superschematic check
superschematic check ./schemas/services/shop-api --json
superschematic check --watch ./schemas/services
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--json` | false | write the result as one JSON document; with `--watch`, one per line |
| `--watch` | false | check, then re-check the services each edit affects until Ctrl-C |
| `--interval` | `200ms` | with `--watch`, how often to look for edits |
| `--naming` | `<services-root>/../superschematic.toml` | naming config file |

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
and `--emit-ir` prints it. The `types` generator renders it: each
language's types write `<Type>Behaviors`, the type's behaviors' fields as
a read returns them, narrowed by the type's config
([Behavior fields](/superschematic/guides/engine-behaviors/#behavior-fields)).
A General schema with such a type builds. In a DB schema it is an engine
type, whose instances the engine keeps: `sql` and `orm` leave it out, and
the `enginedoc` generator writes the schema's engine document,
`types/go/<service>/engine/<service>.json`, which the types hold as a
constant too
([Engine types](/superschematic/guides/engine-behaviors/#engine-types)).
`outputs.engine.namespace` names the engine namespace its documents live
in, the service's name unless set; `outputs.engine.mount` where a Go
server of an API on the database mounts the engine's routes,
`/engine/<service>` unless set; `outputs.engine.policy` the access
policy it opens the engine with, `permission` unless set; and
`outputs.engine.retention` how long the event log keeps events, which the
database's runner prunes, every event unless set; and
`outputs.engine.tools.invocation` the MCP invocation policy of an engine
tool or a behavior operation (`Payments.capturePayment: ask`), which the
build checks against the behaviors the engine types compose and requires
for each commerce operation that moves money or stock, and
`outputs.engine.tools.hidden` the behavior operations the engine's MCP
tools leave out
([In a generated server](/superschematic/guides/engine-behaviors/#in-a-generated-server)).
When a type of a DB or General schema
composes an extension's behavior whose registration names a Rust crate,
the `projectengine` generator writes the project's engine crate,
`engine/rust/<service>`, which compiles the core's behaviors and the
extension's crates into the Rust engine (D58), with the C ABI's archive
over them a Go server links as its `ffi` member and the Node-API addon a
TypeScript server loads as its `napi` member
([The project's engine crate](/superschematic/guides/engine-behaviors/#the-projects-engine-crate)).
The build fails before its generators
run, one line per problem, when an engine type reaches a table or another
engine type through a field, a table references an engine type, or an
engine type declares what only a table has:
`generator: field Ticket.customer of engine type Ticket is typed as table Customer, but an engine type cannot reach a table: make Customer an engine type too and link to it with the Links behavior (D16), or hold its key in a plain field`.
An API's own type cannot compose a behavior, and the API generators do
not serve an engine type yet, so an API that imports or projects one
fails, naming the first generator that would run:
`generator: api does not render engine types yet: shop-api imports shop-db.Ticket, which composes behavior Workflow (D58)`.
An engine runs a General schema's types as they are; `engine-client`
(below) types the engine's client for it.

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
directory that holds a Go file. A TypeScript API's is a package at the
`typescript` template (`typescript/{service}`): `package.json`,
`tsconfig.json` and an `index.ts` whose `create` is the generated
`Constructor`, written only while the directory holds no `.ts` file. A
Rust API's is a crate at the `rust` template (`rust/{service}`): a
`Cargo.toml` and a `src/lib.rs` whose `create(deps)` returns the API
crate's `Handlers`, written only while the directory holds no
`Cargo.toml`.
Without the flag a build writes nothing outside the output root, except a
stack's: building a `Stack` service writes each Go and TypeScript server's
entrypoint under `server/<stack>/<server>` in the output root and
scaffolds, with no
flag, each API its servers serve whose implementation is missing, with a
`go.mod` beside a Go one when no module holds the package. A TypeScript
server some environment places on Cloudflare Workers also gets `fetch.ts`,
its Workers entrypoint, `bundle.ts`, which a deploy runs as `bun bundle.ts
<dir>` and `bun bundle.ts --check` runs into a temporary directory, and
`workers.d.ts`, beside the `main.ts` that `stack dev` runs (D64); a
TypeScript job there gets `scheduled.ts`, its Cron Trigger entrypoint, in
place of `fetch.ts`. A build never
removes the output root's `bun.lock`, the lockfile of its Bun workspace,
which the project commits. When git ignores it, the build of a stack with
a TypeScript server prints one line naming the rule and saying to ignore
the output root's contents instead (`dist/*` and `!dist/bun.lock` in
place of `dist/`); it edits no ignore file.

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
| `--scaffold` | false | write the implementation scaffold of each Go, TypeScript or Rust API built whose package is missing, at the `[implementation_paths]` template of its language |

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
| `--scaffold` | false | write the implementation scaffold of each Go, TypeScript or Rust API whose package is missing, as `build --scaffold` does; a cached service whose implementation is missing builds again |

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
`config` is held to that behavior's config schema. A type's `display` is
held to the schema of `@display`'s argument, which the core registers.

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

Write the declaration of every behavior the binary registers into the
crate that implements it for the engine: one `<name>.behavior.json` per
behavior, the same declaration the Go package embeds and registers. The
Rust implementation embeds that copy (`include_str!`), so the engine and
the compiler read one declaration. Other `*.behavior.json` files in the
directory are removed. `--package` and `--extension` narrow what is
written to the behaviors one npm package ships and the ones one
extension registered.

A copy is canonical rather than the source bytes: the declaration's keys
in `BehaviorDeclaration`'s order, each JSON Schema's object keys sorted
with number literals as written, two-space indents and a final newline.
It changes only when the declaration does.

```
superschematic behaviors --package @superschematic/engine --out runtime/engine/rust/behaviors/declarations
superschematic behaviors --package @superschematic/engine-workqueue --out runtime/engine-workqueue/rust/declarations --check
acme-schematic behaviors --extension acme --out crates/behaviors/declarations
acme-schematic behaviors --extension acme --out crates/behaviors/declarations --check
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | (required) | the directory the copies go in |
| `--check` | false | write nothing; fail, naming each file, when a copy differs, is missing, or is no registered behavior's |
| `--package` | every behavior | only the behaviors this npm package ships, as their registration names it; fails, naming the packages there are, when it ships none |
| `--extension` | every behavior | only the behaviors this extension (its `Name()`) registered; fails when it registers none |
| `--naming` | built-in names | naming config file; this command has no service directory to discover one from |

It is a command of the binary, not a tool in the core module, because an
extension's declarations are registered only in its own binary: acme's
copy comes from `acme-schematic`, into its `acme-behaviors` crate, and
its smoke runs `--check`. The core binary writes the behaviors the core
declares into the three Rust crates that implement them: `Workflow`,
`Comments`, `Revisions`, `Dependencies`, `Links`, `Rollups`, `Search`,
`Reactions`, `Constants`, `Variants`, `Branches`, `Timers`,
`StateRules`, `Numbering` and `Approvals` with `--package
@superschematic/engine` into the engine's behaviors crate
(`runtime/engine/rust/behaviors/declarations`), and `Lease`,
`Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget` and `Retries`
with `--package @superschematic/engine-workqueue` into the work queue's
(`runtime/engine-workqueue/rust/declarations`), and `Stock`, `Calendar`,
`Holds` and `Payments` with `--package @superschematic/engine-commerce`
into the commerce crate's (`runtime/engine-commerce/rust/declarations`,
D70) (`make behaviors`; `make
behaviors-check` in CI). Without `--extension`, an extension's binary
writes the core's declarations beside its own.

## `engine-client --out <file.ts> <schema file>...`

Write one TypeScript module of typed wrappers over the engine's client
(`@superschematic/engine/client`) for engine schemas (D49). Per schema it
exports:

| Export | What it is |
| --- | --- |
| `<T>Fields` | the instance type's own fields, as a create gives them; with `Variants`, a union by the field that picks the type |
| `<T>` | an instance's data as a read returns it: its own fields, `<T>Fields` |
| `<T>Behaviors` | an instance's behaviors as a read returns them: by behavior name, each behavior that declares a field, with its fields `readonly`, optional where the engine can leave one out |
| `<T>Patch` | a merge patch of the own fields |
| `<T>State`, `<T>LinkName` | a `Workflow`'s states and a `Links` config's names, where the type composes them |
| `<T>CommentMessage`, `<T>CommentBlock` | a `Comments` message as every operation that returns one gives it, and a block of one, where the type composes `Comments` |
| `<T>CreateParams`, `<T>Preconditions` | each behavior's create parameters and preconditions, by behavior name; an entry is required when its schema requires something, such as a required link |
| `<T><Op>Params`, `<T><Op>Result`, `<T>Operations` | each behavior operation's parameters and result |
| `<T>Vetoes`, `is<T>Veto` | the codes each behavior's vetoes carry, and `isVeto` typed by them |
| `<Schema>Client`, `<schema>Client(client)` | the wrapper: `create`, `get`, `list`, `update`, `delete`, `operate` and a method per behavior operation |

`<T>` is the instance type's name, the type named like the schema or its
only type, and `<Schema>` the schema's name in PascalCase. A behavior's
config narrows its declaration's shapes for the core's behaviors:
`Workflow`'s states, `Links`' names and targets, `Dependencies`' blocker
schemas, `Rollups`' values, `Revisions`' patches, the kinds of block
`Comments` lets a post give and `Variants`' types.
Another behavior, an extension's among them, is typed by its declaration,
and its fields hold any JSON. A scalar is typed by the JSON its values
are on the wire (`Temporal.DateTime` a string), not by its scalar
library's symbol.

```
superschematic engine-client --out src/notes.client.ts schemas/notes.schema.json
superschematic engine-client --out src/jobs.client.ts schemas/batches.schema.json schemas/jobs.schema.json schemas/workers.schema.json
superschematic engine-client --out src/notes.client.ts --check schemas/notes.schema.json
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | (required) | the TypeScript file the module is written to |
| `--check` | false | write nothing; fail when the file is not what would be written |
| `--naming` | the `superschematic.toml` found walking up from the first file | naming config file |

Each file is one schema document as the engine takes it: `kind: General`,
a `name`, no imports and no operation sets. A `.schema.json` or
`.schema.yaml` file is read on its own, and a `.schema.ts` file in the
context of its service, as `format` reads it. The command runs the
loader's checks, the behaviors' configs against their declarations
included, and refuses what the engine refuses: a map or union field, and
two schemas whose exports share a name. The module imports the client
from `engine_npm_package`'s `/client` entry point and nothing else, so it
runs wherever the client does. It is a command of its own rather than a
`build` target: an engine schema has no service config, and one module
types schemas that link to each other.

## `stack init [<services-root>]`

Write a Stack service over a tree that already has API services, with a
local environment, a gcp one when given a project and a region or a
cloudflare one when given an account, and the ignore rules a stack needs.
It writes files and nothing else. The design is section 11.1 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md),
and D59 records it; [Stacks](/superschematic/guides/stacks/#start-a-stack)
walks through it.

1. Write each service's sentinel, as a build does, and discover the
   services under `<services-root>`, `./schemas/services` without one.
2. Read each API's operations. An API is exposed when an end user or a
   third party calls one of its operations: one whose service clause,
   its own or its set's, is none or `@allowService`. An API whose every
   operation is `@requireService`, or that has none, is internal. Each
   API a site calls is exposed.
3. Ask for each value no flag gives, at a terminal.
4. Write `<services-root>/<name>/`: `package.json` when the first API
   that has a `tsconfig.json` also has one, `tsconfig.json` extending
   that API's base, `schema.config.ts`, `src/stack.schema.ts`, whose
   `@stack` deploys every API and every Site service and exposes as step 2
   decided, and every app of an API it deploys, `{ of: <Api>, app:
   "<Class>" }`, since no one reaches an app it does not expose (D78), with `Dev` on the local target and the cloud environment, and
   the cloud target's values and settings types read from the JSON Schemas
   it registers: `src/gcp-target.ts` for a gcp environment,
   `src/cloudflare-target.ts` for a cloudflare one (D64).
5. Add the ignore rules to the `.gitignore` that covers the schemas root,
   the nearest at or above it in its repository, else a new one in the
   schemas root, anchored there: `/dist/*`, `!/dist/bun.lock` and
   `/.superschematic/`. A `/dist/` or `/dist` rule there becomes
   `/dist/*`. When git still ignores the lockfile after them, because a
   broader rule ignores the output root, it adds `!/dist/`; when another
   rule ignores the lockfile itself, it names the rule. A rule already
   there is not added again.
6. Load the stack and resolve each environment as the `stack` generator
   does, and print what it wrote and why, each problem that does not
   resolve yet, and the next commands: `superschematic stack dev
   --environment Dev` and `superschematic stack bootstrap <environment>`,
   each naming the stack's directory when the stack commands would not
   find it on their own.

At a terminal, init asks for the name, whether to add a gcp environment
and its name, project and region, and, when you add none, whether to add
a cloudflare environment and its name and account, whether to turn the
GitHub workflow on, and what to expose. Each question shows its default in brackets: the
tree's, or the project and region gcloud's active configuration names
(`project`, then `run/region` or `compute/region`), read with no stdin and
a ten-second deadline, and skipped without `gcloud`. Without a terminal,
each value comes from its flag or its default, and a gcp environment needs
both `--gcp-project` and `--region`. A cloudflare environment's one value
is its account, 32 hexadecimal digits from the dashboard; bootstrap
records the rest.

It refuses a tree with no API service; a tree with a Stack service, unless
`--force`; a name that is another kind of service's; a gcp project
beside a Cloudflare account, since init writes one cloud environment; and
a gcp or cloudflare environment in a binary that does not link its target,
the core alone.

```
superschematic stack init
superschematic stack init --gcp-project acme-staging --region us-east1 --ci github
superschematic stack init --cloudflare-account 0123456789abcdef0123456789abcdef --ci github
superschematic stack init ./schemas/services --name shop-stack --expose shop-api --force
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--name` | the first word the deployed services' names share, then `-stack`; else the naming file's `npm_scope`, then `-stack`; else the project directory's | the Stack service's name and directory |
| `--gcp-project` | none | the Google Cloud project of a gcp environment |
| `--region` | none | the gcp environment's region, such as `us-east1` |
| `--cloudflare-account` | none | the Cloudflare account of a cloudflare environment: its id, 32 hexadecimal digits |
| `--environment` | `Staging` | the cloud environment's name, a class name |
| `--ci` | none | the CI renderer `outputs.ci` turns on, `github` |
| `--expose` | the APIs end users and sites call | the API services to expose, comma-separated, or `none` |
| `--force` | false | write the stack though the tree has one: replace the Stack service of the same name, its config and `src`, or add another |
| `--naming` | `<services-root>/../superschematic.toml` | naming config file |

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
   `superschematic-<stack>-<environment>-<checkout>-postgres`, published on
   127.0.0.1 only, and create a database per DB schema it hosts.
   `<checkout>` is six hexadecimal digits of a hash of the environment's
   state directory, `<schemas-root>/.superschematic/local/<stack>/<environment>`,
   so each checkout of the repository runs containers of its own, and
   keeps them from run to run. A container of the environment that an
   older superschematic named without a checkout is left as it is, with
   its data: dev prints the `docker rm` that removes it.
4. Migrate each database to its schema's model: a plan from the model the
   database recorded, applied with `superschematic-migrate`, expand and
   contract back to back. The runner must be on `PATH`, or named by
   `SUPERSCHEMATIC_MIGRATE`; [Schema migrations](/superschematic/reference/migrations/)
   says how to install it.
5. Build each Go server's entrypoint module at
   `<out>/server/<stack>/<server>` with `go build`, and for the TypeScript
   servers run `bun install` once in `<out>`, the root of their Bun
   workspace, which writes `<out>/bun.lock` or brings it up to date, as
   your own install would; the project commits it. Start each server with its resolved config and `PORT`,
   callees first, a TypeScript one as `bun main.ts` in its entrypoint
   package at the same place, and wait until it answers `/readyz`. Each
   line a server prints is printed with its name in front. Go servers need
   `go` on `PATH`, and TypeScript servers `bun`; a Rust server does not
   run locally yet, and the environment does not resolve. In a project
   whose naming file names no superscalar checkout (`[paths] scalar_go`),
   a release of superschematic first fetches the static archives it ships
   for this machine's platform, as [`env`](#env-variable) does, and the
   Go builds take `CGO_LDFLAGS` pointing at them, after any you set.
6. Build each worker's entrypoint module at `<out>/server/<stack>/<worker>`
   the same way, a TypeScript worker's package needing no build, and start
   it with the servers of its wave, a TypeScript one as `bun main.ts`: a process with
   no port, ready once it starts, with `WORKER_CONCURRENCY` set to the
   concurrency the environment gives it, each line it prints with its name
   in front. One process runs whatever the worker's `instances`, and none
   for a worker the environment turns off.
7. Build each job's entrypoint module at `<out>/server/<stack>/<job>` the
   same way, a TypeScript job's package needing no build, and run each job
   that has a schedule in the environment on it, a TypeScript one as `bun
   main.ts`, in its time zone: never two runs of one job at once, a run stopped
   at the job's timeout and run again up to its retries, each line it
   prints with its name in front. A job whose settings set `onDeploy` runs
   once after the last rollout wave, and dev waits for it before it prints
   its summary. A job's run never stops the environment.
7. Build each site once, `bun run <build>` in its package after the same
   install, once the servers it calls are ready, and serve the directory
   its build wrote from a file server on `http://127.0.0.1:<port>`, with
   the site's config at `/__superschematic/config.json`, each API it calls
   at its loopback URL, and its fallback for a path that names no file.
   The summary prints each site's URL. A rebuilt site needs another `stack
   dev`.

Dev stays in the foreground until Ctrl-C or until a server or a worker
exits, then stops the sites, the servers and the workers, callers first,
and the containers, which keep their data for the next run: the
databases, and the buckets' objects. `--remove-data` removes the
checkout's containers and their data instead, and no other checkout's.

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

Dev builds once. A schema edit while it runs reaches the servers only
through another `stack dev`, which rebuilds the stack's services and
migrates the databases again; dev does not watch the tree or restart the
servers an edit affects (D72). Run [`check --watch`](#check---watch) beside
it to see each edit's verdict as you make it, and restart dev once the
schemas check clean.

```
superschematic stack dev ./schemas/services/shop-stack
superschematic stack dev ./schemas/services/shop-stack --environment Dev --remove-data
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--environment`, `-e` | the stack's one local environment | the environment to run; it must be on the `local` target |
| `--out` | `<schemas-root>/dist` | output root for generated artifacts; the provisioner renders its program to `<out>/program/<stack>/<environment>` |
| `--remove-data` | false | on exit, remove the checkout's containers of the environment and their data, the Postgres databases and the buckets' objects, instead of stopping them; `--remove-database` is its earlier name |
| `--naming` | `<stack-service-dir>/../../superschematic.toml` | naming config file |

A local environment sets each container's image and host port with its
values, Postgres's and the storage emulator's, and a server's port with
its settings; a port it leaves out comes from a hash of the stack, the
environment and the server, shifted by the checkout, so it stays the same
from run to run and two checkouts run the environment side by side. A
port the environment sets is the same in every checkout, so only one
checkout at a time can run an environment that sets one:

```ts
@environment({
  target: "local",
  local: { postgresImage: "postgres:16-alpine", postgresPort: 55432, storagePort: 54443 },
  settings: [{ of: ShopApi, port: 8080 }],
})
export abstract class Dev {}
```

## `stack run <environment> <job>`

Run a job of an environment once, outside its schedule, wait for it, and
exit non-zero when its last try fails. `<job>` is the job's deployable: its
API service's name and its class in kebab case,
`shop-orders-ship-orders`. The design is section 8.7 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md).

On the `local` target, run it from another terminal while `stack dev`
runs the environment. It builds no schema: it reads the environment the
last build resolved and the program `stack dev` rendered, builds a Go
job's binary from its entrypoint module, linking the release's static
archives as `stack dev` does, or installs the workspace for a
TypeScript job's `main.ts`, so a change to the job's code is in the run, and runs it with the environment's values, secrets and keys, its
timeout and its retries, each line it prints with the job's name in front.
It refuses an environment whose Postgres container or servers do not
answer, and a job whose scheduled run goes on; `stack dev`'s schedule
skips the job's runs while this one goes on. Ctrl-C stops the run.

On a cloud target, it runs the deployed job, with the image the deploy
manifest records, or the bundle for a job whose artifact is one, through
the target's job runner. It builds nothing, and reads no `--prepared`
directory. It refuses a run whose
last deploy did not roll the job out, and a target that runs no job on
demand. On gcp it runs an execution of the job's Cloud Run job as the
last deploy left it, refusing one that runs another image than the
manifest records, as during a deploy, and waits for it to end. When the
last try fails, it reports that try's error, which it reads from the
execution's logs in Cloud Logging, and the logs' URL. On cloudflare it
starts an instance of the job's Workflow with the bundle the manifest
records, refusing an instance that runs another bundle, waits for it to
end, and reports the error the run threw.

```
superschematic stack run Dev shop-orders-ship-orders
superschematic stack run Preview shop-orders-ship-orders --param pr=123
```

It takes the flags of the commands below, `--stack`, `--naming`,
`--program-dir` and `--param`, and:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--out` | `<schemas-root>/dist` | the output root the build wrote, `stack dev`'s `--out`: where `run` reads a local environment and the job's entrypoint module |

## `stack queue dead|retry|drop <environment> <queue>`

List, retry and drop the dead messages of a queue a DB service declares
([Dead messages](/superschematic/guides/queues/#dead-messages)). `<queue>`
is the DB service and its `@queue` class, `shop-db.OrderPlaced`. The design
is section 8.8 of
[docs/stack-model.md](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md).

```
superschematic stack queue dead Dev shop-db.OrderPlaced [--limit 100]
superschematic stack queue retry Dev shop-db.OrderPlaced <id>... | --all
superschematic stack queue drop Dev shop-db.OrderPlaced <id>... | --all --yes
superschematic stack queue dead Preview shop-db.OrderPlaced --param pr=123
```

- `dead` lists the dead messages, the oldest death first, up to
  `--limit`, and their total: each one's ID, attempts, time of death, last
  error and columns as JSON.
- `retry` makes the dead messages named, or every one with `--all`,
  ready again: due now, with no attempts and no claim, the last error
  kept until the next failure replaces it.
- `drop` deletes the dead messages named, or every one with `--all`,
  which takes `--yes`.

An ID is a message's UUID, in either case. A `retry` or a `drop` that
names a message that is not dead changes none of them, and says what each
such ID is: `ready`, `claimed`, `done`, or not in the queue.

Each resolves the environment from the schemas, as `stack plan` does,
without a build, and runs the operation with the migration runner, from
`$SUPERSCHEMATIC_MIGRATE`, else `superschematic-migrate` on `PATH`
(`superschematic-migrate queue`, [The runner](/superschematic/reference/migrations/#the-runner)).
On the `local` target it reaches the database at the URL the environment
resolves, on the Postgres container `stack dev` runs; run it from another
terminal, and when the database does not answer it says to start `stack
dev`.

On a cloud target it runs on the deployed run's database through the
target's queue runner, and refuses a target that has none and a run that
was never deployed. On gcp the operation is one execution of the stack's
migration job, `<stack>-migrate`, which a deploy's migrations run as: the
runner reaches Cloud SQL as the `migrator` and writes its result to the
state bucket, under `superschematic/queue/<stack>/<run>/` with a random
128-bit id, in place of its stdout, after it has reserved that name with
an empty marker, so a runner that may not write there stops before the
queue changes. The command reads that object once and deletes it, and the
bucket's lifecycle deletes one it did not after a day; a deleted result
stays restorable for the bucket's soft-delete window, seven days by
default. The command runs the job as the last deploy left it and never
changes its image, so deploy with the release you run it from first.
A listing's messages may hold personal data, so they never reach the
execution's logs; an execution that fails reports the runner's error, read
from Cloud Logging as a failed migration's is. The `deployer` account, or
an owner, runs it; a project bootstrapped before this needs `stack
bootstrap` again, which grants the `migrator` its write.

It refuses a queue no DB service of the stack declares, and a queue whose
database the environment places on another dialect than Postgres, on
which no queue is claimed.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--stack` | the working directory when it is a Stack service, else the one Stack service under `./schemas/services` | the Stack service directory |
| `--naming` | `<stack>/../../superschematic.toml` | naming config file |
| `--format` | `text` | `text`, or `json` for the runner's result: the operation, and the `total` and `messages` it listed or the `ids` it changed |
| `--param` | none | `<name>=<value>`, repeatable: a parameter's value, for a member of a parameterized environment on a cloud target |
| `--timeout` | `30m` | give up waiting for the operation after this long; on gcp an execution it stops waiting for may still finish, and its result is deleted a day later |
| `--limit` | `100` | `dead`: list at most this many |
| `--all` | off | `retry` and `drop`: every dead message, in place of IDs |
| `--yes` | off | `drop`: confirm `--all` |

## `stack bootstrap`, `secrets set`, `prepare`, `plan`, `build`, `deploy`, `destroy`, `outputs` and `unlock`

The cloud half of the `stack` group bootstraps, plans, builds, deploys and
destroys the environments a Stack service declares
(`docs/stack-model.md`, sections 7.3, 11.1 and 11.2). Each command loads
the stack, resolves one environment as the `stack` generator does, and
drives the environment's target and provisioner. A binary deploys to a
target only when it links the target's extension and the provisioner's
(`gcp.Extension{}`, `pulumi.Extension{...}`). An environment on the
`local` target runs with `stack dev`; `plan`, `build`, `deploy`,
`bootstrap`, `destroy`, `outputs` and `unlock` refuse it, `secrets set`
writes its `secrets.env`, and `stack run` runs a job against it.
`prepare` reaches no target and takes its own flags.

A stack with an app whose `@app` names an extension the binary does not
link (D78) is refused by `prepare`, `build` and `deploy`, a deploy with
`--prepared` included, since each builds images: `app Backoffice of
shop-stock needs the topcoat extension, which this superschematic binary
does not link; run superschematic-topcoat`. `prepare` and `deploy` with
`--no-build` build nothing and are not refused. `plan` and the rest read
the app as usual.

Each of the others takes these flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--stack` | the working directory when it is a Stack service, else the one Stack service under `./schemas/services` | the Stack service directory |
| `--naming` | `<stack>/../../superschematic.toml` | naming config file |
| `--program-dir` | `<schemas-root>/dist/program/<stack>/<environment>` | where to render the provisioner's program |
| `--param` | none | a parameter's value for one run of a parameterized environment, `<name>=<value>`; repeatable. `plan`, `build`, `deploy`, `destroy`, `outputs` and `unlock` take it |

### `stack bootstrap <environment>`

Prepare the cloud project the environment deploys to, with an owner's
credentials, once; it is safe to run again. On gcp it enables the APIs,
creates the state bucket and its KMS key, applies the Artifact Registry
repository, the `deployer` and `planner` accounts, the `builder` account
image builds run as, the `migrator` account the migration job runs as,
and Workload Identity Federation for the GitHub repository, and creates
the secret of each platform credential the environment needs.

On cloudflare it runs with your token in `CLOUDFLARE_API_TOKEN`, once per
account: it verifies the token, creates the R2 buckets
`superschematic-state`, which holds the state, and
`superschematic-bootstrap`, which holds bootstrap's own state, finds or
creates the account's Secrets Store, and applies the `<stack>-deployer`,
`<stack>-planner` and `<stack>-preview` account tokens. On the account's
first bootstrap it generates two passphrases and prints each once, on
standard output: the state's, which you set in `PULUMI_CONFIG_PASSPHRASE`
for every later command, and bootstrap's own, which you set in
`SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE` for every later bootstrap and which
no CI job holds. Keep both in your password manager. A later run checks
each passphrase in the shell and refuses another. With `--github` it sets
the tokens the environment's jobs read and the state's passphrase as the
repository's GitHub Actions secrets through `gh`, at the scopes the
[generated workflow](/superschematic/guides/stacks/#generated-ci) reads
them from, creating the deploy job's GitHub environment when it is
missing; without `gh` on PATH, signed in, it prints the `gh secret set`
commands and each token once instead.

Then it records in the stack's schema the values only the cloud knows: on
gcp, the project's number, as `projectNumber` in the gcp values of the
environment whose declaration sets `project`, the bootstrapped one or the
nearest one it extends; on cloudflare, `secretsStoreId` and
`workersSubdomain` beside `accountId`. An account with no workers.dev
subdomain yet is told to choose one in the dashboard. It writes the value when the schema has none,
writes it again when it differs, and leaves the file alone when it
matches. A TypeScript schema changes in that property's text alone; a
JSON or YAML schema is not edited. It prints one line per value on
standard output, its progress going to standard error:

```
projectNumber 123456789012: recorded on Staging in src/stack.schema.ts
projectNumber 123456789012: updated from 111111111111 on Staging in src/stack.schema.ts
projectNumber 123456789012: matches Staging in src/stack.schema.ts
projectNumber 123456789012: not recorded: bootstrap edits no YAML schema; in src/stack.schema.yaml, add projectNumber: "123456789012" beside project in the environment values of Staging
```

A TypeScript declaration the edit cannot change, such as values held in a
constant, prints `not recorded:` with what to set by hand. Last, it asks
for each credential with no value, with the terminal's echo off.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--repository` | read from the git remote `origin` | the GitHub repository the CI runs in, `owner/name`; `""` leaves Workload Identity Federation out |
| `--github` | false | set what the generated CI signs in with as the repository's GitHub Actions secrets, through `gh`: the cloudflare target's tokens and passphrase. gcp's CI signs in with no secret and ignores it |

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

### `stack prepare [environment...]`

Run, with no credentials, all the project code that `stack plan`, `build`,
`deploy` and `destroy` would otherwise run beside the target's
credentials, and write what it made into a directory: each service's
sidecar documents, evaluated with Bun; the build context of each server,
job and worker whose Dockerfile `superschematic build-all` wrote; each
site, built with its build script after one frozen install of the output
root's workspace; and each bundle, built with `bun bundle.ts`. A site's or
a bundle's files are kept as the tar stream their digest is of. It reaches
no target. Given the directory with `--prepared`, `stack plan`, `build`,
`deploy` and `destroy` read all of it from there and start no Bun, no
install and no build, so a dependency's install script, a build plugin or
a sidecar document never runs where the credentials are; a document whose
file or imports changed since is refused. The generated GitHub workflow
runs it in its `prepare` job, which holds no identity token and no
secret. With no environment named it prepares every environment off the
local target. Run `superschematic build-all` first.

```
superschematic stack prepare Staging
superschematic stack deploy Staging --prepared schemas/dist/prepared/shop-stack
```

Every process a `stack` command starts to run your project's code, here
or in a deploy without `--prepared`, gets an environment with no
credential in it and a scratch home of its own, with your caches;
`SUPERSCHEMATIC_CHILD_ENV` names more variables to pass, such as a private
registry's token (`HOME` keeps your home), and never passes a
`GOOGLE_*`, `CLOUDSDK_*`, `GCLOUD_*`, `GCP_*`, `CLOUDFLARE_*`, `CF_*`,
`WRANGLER_*`, `PULUMI_*`, `AWS_*` or `ACTIONS_*` variable, nor
`SSH_AUTH_SOCK`, `KUBECONFIG` or `VAULT_TOKEN`. A file under your real
home stays readable to a process that names its path, so run `stack
prepare` before you sign in. In CI, `--prepared` refuses a directory
prepared at another commit or in another run (`GITHUB_SHA`,
`GITHUB_RUN_ID`).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--dir` | `<output-root>/prepared/<stack>` | write here; an earlier prepare's directory is replaced, and one holding anything else is refused |
| `--no-build` | false | write the sidecar documents alone, which a plan or a destroy needs |
| `--out` | `<schemas-root>/dist` | the output root `build-all` wrote |

### `stack plan <environment>`

Show what `stack deploy` would do, changing nothing: the provisioner's plan
of every resource, with each server's image pinned, and each database's
migration plan from the schema the deploy manifest records. It builds
nothing: a server whose platform runs a bundle is planned with the bundle
the manifest records or `--bundle` names, fetched from the target and
placed beside the program. It also lists
the secrets with no value, the servers and jobs with no image or bundle
yet and the sites with no files yet (which a deploy builds), the migration
phases a failed deploy left part-way, the jobs the deploy runs once after
its rollout, the records to create by hand
for a domain no DNS platform holds, and the run's deploy lock when a
deploy holds it, since what the plan reads may change when that deploy
ends. It takes no lock itself. It exits 1 after printing when a plan
has a hazard of a `--fail-on` class that no `--allow` names.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--image` | the manifest's | a server's, job's or worker's image, `<deployable>=<repository>@sha256:<digest>`; repeatable |
| `--site` | the manifest's | the files a site serves, `<site>=sha256:<digest>`; repeatable |
| `--bundle` | the manifest's | the bundle a server, job or worker runs, `<deployable>=sha256:<digest>`; repeatable |
| `--fail-on` | `all` | hazard classes, comma-separated, `all`, or `none` |
| `--allow` | none | a hazard id to acknowledge; repeatable |
| `--out` | none | write the plan as JSON, for `stack deploy --expect` |
| `--format` | `text` | print the plan as `text` or `json` |
| `--prepared` | none | the directory `stack prepare` wrote: read the sidecar documents, and for a deploy or a build each build context, site and bundle, from it, and run no Bun, install or build |

### `stack build <environment>`

Build the image of each server and job whose build context changed since
the image the deploy manifest records, as `stack deploy` would, and deploy
nothing. A Go or TypeScript server, job or worker builds from the Dockerfile
`superschematic build-all` writes at
`<output-root>/server/<stack>/<deployable>/`, with the repository root as its
context, cut down by the `Dockerfile.dockerignore` beside it; on gcp the
build runs on Cloud Build and pushes to the stack's Artifact Registry
repository. Each site builds with its package's build script after a
frozen install of the Bun workspace, and its files go up where the target
serves sites from, under their digest, when they are new; they serve
nothing until a deploy takes them. A server, job or worker whose platform
runs a bundle builds with `bun bundle.ts` in its package after the same
install, and the target keeps its files under their digest when they are
new; on cloudflare that is the account's state bucket, under
`superschematic/bundles/<stack>/<deployable>/<hex>/`. It prints each image,
each site's files and each bundle as a `stack deploy` flag:

```
$ superschematic stack build Staging
--image Orders=us-east1-docker.pkg.dev/acme-staging/shop/orders@sha256:...
--image shop-api=us-east1-docker.pkg.dev/acme-staging/shop/shop-api@sha256:...
--site shop-web=sha256:...

$ superschematic stack build Staging --stack schemas/services/storefront-stack
--bundle shop-storefront=sha256:...
```

A build writes no deploy manifest.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--deployable` | every server, job and worker with a Dockerfile, and every site | build this server, job, worker or site only; repeatable |
| `--server` | none | build this server only; repeatable. `--deployable` takes a job, a worker or a site too |
| `--force` | false | build a server, job or worker whose context did not change, and upload a site's files that are there |
| `--out` | none | write the result as JSON |
| `--format` | `text` | print `--image`, `--site` and `--bundle` flags (`text`) or the result as `json` |
| `--prepared` | none | the directory `stack prepare` wrote: read the sidecar documents, and for a deploy or a build each build context, site and bundle, from it, and run no Bun, install or build |

### `stack deploy <environment>`

Deploy the environment in deploy order: infrastructure, each database's
`expand` phase, the servers and jobs wave by wave, callees first, each
job the environment's settings run on every deploy (`onDeploy`), the
`contract` phases, exposure. A job that runs on every deploy runs once,
through the target's job runner as `stack run` runs it (on gcp an
execution of its Cloud Run job), and the deploy waits for it; its last
try failing stops the deploy before the `contract` phases with that try's
error, and the next deploy runs it again. Before any step, it builds the image of each
server and job
`--image` names none for whose build context changed since the image the
manifest records, as `stack build` does; one with no Dockerfile keeps the
manifest's image. It builds each site `--site` names no files for, and
before the wave that rolls the site out it uploads the site's files under
their digest when they are new and writes the site's config for the run,
each API it calls with its public address; the wave's apply then serves
them. A server, job or worker whose platform runs a bundle builds with
`bun bundle.ts` in its package, and its files go up under their digest
when they are new, before any step; the deploy places them beside the
provisioner's program. The deploy manifest records each step, the
context each image it built came from, the digest of each site's
files, which `--site` takes to serve them again, and each bundle, which
`--bundle` takes to run it again, fetched from the target: a rollback. Every secret needs a value before
the first step after infrastructure; at a terminal the deploy asks for
each one missing. On gcp each migration phase runs as an execution of the
stack's Cloud Run job, `<stack>-migrate`, which also gives each server
that connects to a database its privileges. A rollout that fails runs no
`contract` step, and the next deploy plans from the schema between the
phases.

The deploy holds the run's deploy lock, kept by the target's state store
(on gcp and on cloudflare the object
`superschematic/locks/<stack>/<run>.json` of the state bucket), from before it reads the manifest until it ends, whether it
succeeds or fails. A second deploy or a destroy of the run meanwhile
fails at once, naming who holds the lock, the host, the process, the CI
run and since when. Ctrl-C stops the step under way, records the failure
in the manifest and releases the lock; a second Ctrl-C ends the process.
A deploy whose process was killed leaves its lock, which `stack unlock`
removes.

A binary built from a checkout names no release of the migration runner
to build the job's image from: set `SUPERSCHEMATIC_MIGRATE_IMAGE` to an
image of `superschematic-migrate`, by digest, in the stack's repository.
A deploy with a migration to run checks for the image, or builds the
release's, before it changes anything, and refuses to start without one.

```
superschematic stack deploy Staging
superschematic stack deploy Staging --image shop-api=us-east1-docker.pkg.dev/acme-staging/shop/shop-api@sha256:...
superschematic stack plan Preview --param pr=123 --out plan.json
superschematic stack deploy Preview --param pr=123 --expect plan.json --allow 'destructive:table/order/column/total'
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--image` | a build, else the manifest's | a server's or job's image, `<deployable>=<repository>@sha256:<digest>`; repeatable. One with none of them is refused |
| `--site` | a build, else the manifest's | the files a site serves, published by an earlier deploy or build, `<site>=sha256:<digest>`; repeatable. One with none of them is refused |
| `--bundle` | a build, else the manifest's | the bundle a server, job or worker runs, published by an earlier deploy or build, `<deployable>=sha256:<digest>`; repeatable. One with none of them is refused |
| `--no-build` | false | build no image, no site and no bundle: take each from `--image`, `--site`, `--bundle` or the manifest |
| `--fail-on` | `all` | hazard classes that stop the deploy unless `--allow` names each hazard, comma-separated, `all`, or `none` |
| `--allow` | none | a hazard id to acknowledge; repeatable |
| `--expect` | none | a plan `stack plan --out` wrote: refuse migration plans other than its |
| `--prepared` | none | the directory `stack prepare` wrote: read the sidecar documents, and for a deploy or a build each build context, site and bundle, from it, and run no Bun, install or build |

### `stack destroy <environment>`

Remove every resource of the run and its deploy manifest. It asks for the
run's name at a terminal unless `--yes`, and refuses without either. It
holds the run's deploy lock while it runs, as a deploy does.
`--prepared` reads the sidecar documents from what `stack prepare
--no-build` wrote, and runs none of them.

### `stack unlock <environment>`

Remove the deploy lock of a run, which a deploy or a destroy leaves when
its process dies before it ends: a killed CI job, a closed laptop. The
lock has no expiry. It shows who holds the lock, the host, the process
and since when, and asks for the run's name at a terminal unless
`--yes`, and refuses without either. Remove a lock only once the deploy
that holds it no longer runs. It removes the lock it showed and no other,
so a lock another deploy took meanwhile stays; a run with no lock prints
`no deploy holds the lock of <run>`.

```
superschematic stack unlock Staging
superschematic stack unlock Preview --param pr=123 --yes
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--yes` | false | remove the lock without asking |

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

## `identity bootstrap [<db-service-dir>]`

Create a database's first administrator in one transaction: a role with
the permissions `--permission` names, a user who signs in with `--login`
and a password, and the grant of the role to the user. The database is
one whose schema has the `User` and `UserRole` traits, the core user
model. The command refuses when any role grant exists, so it runs once
per database; the administrator then manages every other user and role
through the `@userAdministration` routes. No one grants what they do not
hold, so the role needs every permission the administrator hands out, the
administration routes' own among them: `identity` covers
`identity.users.read` and the other three, under the default
[`identity_permission_prefix`](/superschematic/reference/naming/#identity_permission_prefix).

The tables are those of the DB service at `<db-service-dir>`, which loads
as `build` loads it; its identity descriptor is built in process, and a
schema without a `UserRole` table is refused. `--descriptor` names the
descriptor a build wrote instead, `identity/<schema>.json` in the
service's Go types module, and takes no service directory.

The compiler writes no database: the identity runner,
`superschematic-identity`, does. The command runs the runner's
`bootstrap` with the descriptor and every flag it was given but
`--naming`, hands it its standard input, output and error, and exits with
the runner's status, so the runner's refusals and its output are the
command's. The runner is the binary `$SUPERSCHEMATIC_IDENTITY` names, else
`superschematic-identity` on `PATH`; without either the command says how
to install it.

Before it runs the runner, the command reads the permission catalogs the
build writes, `permissions.json` beside each API's `openapi.json`. When
the default output root, `<schemas-root>/dist`, holds catalogs of APIs
whose `authDb` is this service, a `--permission` that is none of their
permissions and covers none draws a warning on stderr. It is granted all
the same: an engine's schemas name permissions at run time.

```
superschematic identity bootstrap ./schemas/services/shop-db --login admin@example.com --permission identity --permission orders
printf '%s\n' "$ADMIN_PASSWORD" | superschematic identity bootstrap ./schemas/services/shop-db --database-url "$SHOP_DB_URL" --login admin@example.com --name "Shop Admin" --role owner --permission identity
superschematic identity bootstrap --descriptor ./schemas/dist/types/go/shop-db/identity/shop-db.json --database-url sqlite:./shop.db --login admin@example.com --permission identity
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--login` | (required) | the administrator's login |
| `--permission` | (required) | a permission the role carries; repeatable |
| `--role` | `admin` | the role's name |
| `--name` | the login | the administrator's display name |
| `--database-url` | `$DATABASE_URL` | the database URL |
| `--dialect` | the URL's | `postgres` or `sqlite` |
| `--config` | the runtime's cost | an identity config JSON file |
| `--descriptor` | none | the identity descriptor file, in place of a service directory |
| `--naming` | `<db-service-dir>/../../superschematic.toml` | naming config file; the command reads it and the runner does not |

Each flag but `--descriptor` and `--naming` is the runner's, below.

### The identity runner, `superschematic-identity`

`superschematic-identity bootstrap --descriptor <file>` does the
database write, through the HTTP runtime's identity package
(`Store.Bootstrap`). A deploy job runs it with the descriptor the build
wrote and the password on standard input, without the compiler. It
installs as the migration runner does, from a release's
`superschematic-identity_<version>_<platform>.tar.gz`, or built from a
checkout ([runtime/http/go/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/http/go/README.md)).

The database is `--database-url`, or `$DATABASE_URL`, read as
`superschematic-migrate` reads it: a `postgres://` or `postgresql://` URL
is Postgres, and a `sqlite:` URL, a `file:` URI or a path is SQLite.
`--dialect` overrides the URL's. The database must hold the schema's
tables, so migrate it first; a SQLite path that does not exist is refused.

The password comes from standard input, never from a flag or the
environment. At a terminal the runner asks for it twice with the
terminal's echo off; otherwise it reads one line, so a secret store can
pipe it in. It must be an `Auth.Password`, 8 to 128 characters. It is
hashed with argon2id at the cost the identity config file `--config`
sets under `password.argon2`, else at the runtime's default. The runner
prints the role and the user it created, with their ids, and never the
password or its hash. It exits 0 when it created them, 1 when it refused
or failed, and 2 for flags it cannot run with.

```
printf '%s\n' "$ADMIN_PASSWORD" | superschematic-identity bootstrap --descriptor dist/types/go/shop-db/identity/shop-db.json --login admin@example.com --permission identity
superschematic-identity version
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `--descriptor` | (required) | the identity descriptor the DB build writes |
| `--login` | (required) | the administrator's login, a value of the `User` table's login scalar, which normalizes it; a user with that login refuses the run |
| `--permission` | (required) | a permission the role carries, dotted segments of letters, digits, `_` and `-`; repeatable, and a repeat is dropped |
| `--role` | `admin` | the role's name; a role of that name that exists refuses the run |
| `--name` | the login | the administrator's display name, for a `User` trait that names a `name` field |
| `--database-url` | `$DATABASE_URL` | the database URL |
| `--dialect` | the URL's | `postgres` or `sqlite` |
| `--config` | the runtime's cost | an identity config JSON file; its `password.argon2` sets the hash's cost |

## `env [<variable>...]`

Print the variables a `go build` of the code this release of
superschematic generates needs on this machine, in a project whose naming
file names no superscalar checkout
([`[paths] scalar_go`](/superschematic/reference/naming/#pathsscalar_go)).
Such code links superscalar's static archive, the version graph's and the
engine's through cgo, and no module the module proxy serves carries them.
`env` downloads the release's
`superschematic-archives_<version>_<platform>.tar.gz` for this machine
once, checks it against the digest the binary names, unpacks it into
`superschematic/archives/<digest>` in the user's cache directory, and
prints `CGO_LDFLAGS` pointing cgo's linker at its `lib/`, as a line a
POSIX shell evaluates. With variable names it prints each one's value
alone, a line each, as `go env` does; `CGO_LDFLAGS` is the one it
prints. `stack dev` and `stack run` fetch the archives the same way for
the builds they run, and the generated CI's `check` job and a server's
Dockerfile download the same tarball.

The assets download from the release's page unless
`SUPERSCHEMATIC_RELEASE` names a mirror, the base URL the tarball's name
is appended to. A binary built from a checkout is no release, and one the
release workflow did not build names no digests: `env` fails for either,
and a project built with them names its checkouts in `[paths]`, whose
`make setup` builds the archives.

```
eval "$(superschematic env)" && go build ./...
go env -w CGO_LDFLAGS="$(superschematic env CGO_LDFLAGS)"
```

## `catalog scalars|decorators|behaviors|capabilities`

Print a Markdown reference of what the binary knows, written from the
registry it assembles, so the reference lists an extension's scalars,
decorators and behaviors beside the core's. It is for whoever authors
schemas with the binary, a coding agent among them, which reads it in
place of documentation that could describe another version.

- `scalars`: every scalar a field can have, one table per namespace,
  with what it checks, its SQL type and its types in Go, TypeScript,
  Python and Rust. The [scalar reference](/superschematic/reference/scalars/)
  is this table for the core.
- `decorators`: which kinds may import each authoring package, then each
  decorator by package, with the TypeScript declaration `tsc` checks a
  schema file against, its comment, the nodes it may sit on and the kinds
  it is limited to; the type wrappers; and the exported types those name.
  An extension's decorator has no declaration in the binary, so its entry
  gives the JSON Schema of its argument instead. The core's declarations
  are the binary's copy of `packages/`.
- `behaviors`: every engine behavior, with its config's type, what it
  requires and conflicts with, the fields and operations it adds and the
  codes its refusals carry, then the core's config types. An extension's
  behavior gives its config's JSON Schema.
- `capabilities`: the [capability map](/superschematic/start/capabilities/),
  with its links pointing at this site.

`--naming` reads a naming file first, for a binary whose extensions read
it. A name that is not one of the four fails, listing them.

```
superschematic catalog decorators | grep -A 12 '^### `@paginated`'
superschematic catalog scalars > scalars.md
```

## Extension commands

An extension that implements `cli.CommandProvider` adds its commands to
the root. acme adds `describe [<schemas-root>]`. See
[write an extension](/superschematic/extending/write-an-extension/).
