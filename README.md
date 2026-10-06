# superschematic

Describe your data and your APIs once. superschematic compiles that schema
into Postgres tables, a Go ORM, an HTTP server, OpenAPI, types and client
SDKs in Go, TypeScript, Python and Rust, and every one of them validates a
value the same way.

```
schema source (.schema.ts | .schema.json | .schema.yaml)
  -> superschematic build
     -> sql/         Postgres DDL, and SQLite DDL when a DB asks for it
     -> orm/         Go repositories
     -> api/         a Go (chi), Rust (axum) or TypeScript (Hono) server, and OpenAPI
     -> types/       Go, TypeScript, Python, Rust
     -> sdk/         Go, TypeScript, Python, Rust clients, and MCP tool documents
     -> stack/       one environment.json per environment a Stack service declares
     -> server/      a Go entrypoint and Dockerfile per server that stack runs
```

superschematic is pre-release: build it from this checkout (see
[Status](#status)). The documentation lives under [`docs/`](docs/) and is
linked [below](#documentation).

## Why superschematic

A service's data is usually written down many times: a `CREATE TABLE`, an
ORM struct, the server's request and response types, an OpenAPI document,
and a client with its own types in every language that calls it. Each copy
carries its own validation, and the copies drift: a column that is
nullable in SQL but required in the SDK, an email address the server
accepts and the TypeScript client refuses.

superschematic makes the schema the one place a shape is written. A build
generates everything else from it, so a schema change reaches every layer
at once, and the type checker in each language points at the code that has
to follow.

Validation is part of that contract. Field types such as `Contact.Email`,
`Identity.UUID` and `Temporal.DateTime` come from
[superscalar](https://github.com/parable-work/superscalar), a scalar library
that parses and validates each one identically in Go, TypeScript, Python
and Rust. The constraints you declare (`min`, `maxLength`, `pattern` and
the rest) are enforced by the generated decoders in every language, so an
SDK refuses the request the server would refuse.

## What you can build with it

| You want | You write | You get |
| --- | --- | --- |
| A Postgres data layer | a DB schema: one class per table | DDL with keys, relations, indexes, text search and JSON columns; a Go ORM with typed repositories and transactions |
| An HTTP API | an API schema: operations over those tables | a Go, Rust or TypeScript server that routes, decodes, validates and checks permissions, and its OpenAPI; you implement one interface |
| Services that call each other | `calls` in a config, and `@requireService` or `@allowService` on operations | a typed Go `Deps` with an SDK client of each API it calls, and service callers admitted per operation beside the end user, on every server |
| A database that changes safely | the next version of a DB schema | `migrate plan`: an offline plan from the previous version, in expand and contract phases with each step's hazards, for Postgres and SQLite; `superschematic-migrate` applies it to Postgres, a SQLite file or Cloudflare D1 |
| Clients for that API | a line per language in the service's config | SDKs in Go, TypeScript, Python and Rust, plus MCP tool documents an agent can call the operations through |
| Types shared across a polyglot stack | a General schema | the same types and validators in all four languages, and a typed loader for environment variables |
| Row history, branches and merges | `@versioned` and `@versionGraph` on tables | history tables and version-fenced writes; a tree of tables you can branch, commit, merge, release and rebase, with an engine in each language |
| A backend without generated code | a schema as JSON, published to a running server | [`@superschematic/engine`](runtime/engine/README.md): instances, an event log and access control, with workflow, comments, revisions, links, rollups, full-text and vector search, branches and work queues, over HTTP, an event stream and MCP |
| The tree, running | a Stack schema: what runs where, in which environments | a Go entrypoint and Dockerfile per server; `stack dev` runs an environment on your machine, with Postgres in Docker and its migrations applied; `stack plan` and `stack deploy` apply one to Cloud Run and Cloud SQL through the gcp target and Pulumi |
| Your own conventions | a Go extension | new schema kinds, decorators, generators, auth providers and commands, without forking the core |

## A quick look

These files are trimmed from [`examples/acme-shop`](examples/acme-shop/), a
small shop that CI builds and tests end to end in the release candidate run
on `main` twice a day.

A **General** schema declares plain types, shared by other services:

```ts
// schemas/services/shop-common/src/price.schema.ts
import { Generic } from "superscalar";

export enum Currency { EUR = "EUR", GBP = "GBP", USD = "USD" }

// A price in the currency's smallest unit: 1999 EUR is 19.99 euros.
export abstract class Price {
  amountCents: Generic.Int64;
  currency: Currency;
}
```

A **DB** schema declares tables. Each class becomes a table, a Go
repository and a type in every language the config lists:

```ts
// schemas/services/shop-db/src/shop.schema.ts
import { Contact, Generic, Identity } from "superscalar";
import { Default } from "@superschematic/schema";
import { AutoGenerate, Relation, key, unique } from "@superschematic/db";

export abstract class User {
  @key id: AutoGenerate<Identity.UUID>;
  @unique email: Contact.Email;
  name: Identity.Name;
}

export abstract class Product {
  @key id: AutoGenerate<Identity.UUID>;
  @unique sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: Default<boolean, true>;
}

export abstract class StockLevel {
  @key id: AutoGenerate<Identity.UUID>;
  product: Relation<Product, { onDelete: "CASCADE" }>;
  quantity: Generic.Int64;
}
```

An **API** schema declares operations: here, a view checked against a
table, a validated input, and routes behind permissions:

```ts
// schemas/services/shop-api/src/products.schema.ts
import { Generic, Identity } from "superscalar";
import { Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, QueryParam, requirePermission, rest, source } from "@superschematic/api";
import { Product } from "@acme/shop-db";

@source(Product)
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: boolean;
}

export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Validate<Generic.Int64, { min: 0 }>;
}

export class ProductQueries extends Authenticated {
  @rest(HttpMethod.GET, "products")
  @requirePermission(["products.read"])
  listProducts(inStock: QueryParam<boolean>): ProductView[] {
    throw new Error("schema declaration only");
  }
}

export class ProductMutations extends Authenticated {
  @rest(HttpMethod.POST, "products")
  @requirePermission(["products.write"])
  createProduct(input: CreateProductInput): ProductView {
    throw new Error("schema declaration only");
  }
}
```

Each service's `schema.config.ts` names its kind and the outputs to
generate:

```ts
export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  public: true,
  authDb: service({ name: "shop-db", kind: SchemaKind.DB }),
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true }, [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true },  // the Go server; or language: "TYPESCRIPT" or "RUST"
    sdk: { [TargetLanguage.Go]: { enabled: true }, [TargetLanguage.TypeScript]: { enabled: true } }
  }
});
```

One command builds every service in dependency order:

```sh
superschematic build-all schemas/services
```

```
schemas/dist/
  sql/shop-db/create.sql                 Postgres DDL
  orm/shop-db/                           Go ORM over those tables
  api/shop-api/                          Go server: interfaces.go (yours to implement), routes.go, openapi.json
  types/{go,typescript,python,rust}/...  Price, User, Product, ProductView, ... in each language
  sdk/{go,typescript}/shop-api/          clients that validate before they send
  .deps.json                             the package graph, for release and CI scripts
```

You implement the generated `interfaces.go` with your business logic; the
server has already routed, authenticated, decoded and validated the
request by the time your method runs. Schemas can also be written as JSON
or YAML, and `superschematic format` converts between the three forms.

## Get started

You need Go 1.26.4, a C compiler, Rust, Node 22.12+ (24 for the engine) and
Bun 1.4; Python 3.12 with uv only for Python output, and Postgres 16 only
to run the generated SQL. The exact pins are in [`tools.env`](tools.env);
[Prerequisites](docs/src/content/docs/start/prerequisites.md) says what each
tool is for.

```sh
git clone https://github.com/parable-work/superschematic
cd superschematic
make setup      # checks out and builds superscalar, the version-graph archive; bun install; uv sync
make build      # writes bin/superschematic
export PATH="$PWD/bin:$PATH"
superschematic --help
```

`bin/superschematic` is the binary a release ships: the core with the
official extensions linked, the gcp target, the Cloudflare DNS platform
and the Pulumi provisioner.
`go install github.com/parable-work/superschematic/cmd/superschematic@<version>`
does not work, because the Go modules carry `replace` directives and `go
install` at a version refuses them; build from a checkout as above, or
download a release's `superschematic_<version>_<platform>.tar.gz`.

Then build and test the example shop end to end. It compiles the generated
Go, type-checks the generated TypeScript, and runs clients in all four
languages against the Go server:

```sh
examples/acme-shop/scripts/check.sh
```

From there, [Getting started](docs/src/content/docs/start/getting-started.mdx)
builds one schema and uses its types, and
[Your first project](docs/src/content/docs/first-project/index.mdx) grows it
into a database, a Go API and a TypeScript API.

## Documentation

The pages below are the source of the docs site, which is published to
<https://parable-work.github.io/superschematic/> from the first release
tag. To browse it locally with working navigation and code samples, run
`cd docs && npm install && npm run dev`.

**Start here**

- [Prerequisites](docs/src/content/docs/start/prerequisites.md): the toolchain, and setting up a checkout.
- [Getting started](docs/src/content/docs/start/getting-started.mdx): build one schema and use its Go, TypeScript, Python and Rust types.
- [How it works](docs/src/content/docs/start/how-it-works.mdx): services, kinds, outputs, the naming file and the IR.

**Tutorial: your first project**

- [Overview](docs/src/content/docs/first-project/index.mdx): the acme shop and how its services find each other.
- [Model the database](docs/src/content/docs/first-project/database.mdx): Postgres DDL, a Go ORM, and Go and TypeScript types.
- [Serve and call it from Go](docs/src/content/docs/first-project/go-api.mdx): implement the generated Go server and call it with the Go SDK.
- [Serve and call it from TypeScript](docs/src/content/docs/first-project/typescript-api.mdx): implement the generated Hono router and call it with the TypeScript SDK.
- [Build the whole tree](docs/src/content/docs/first-project/build-the-tree.mdx): `build-all`, the package graph and the build cache.

**Guides**

- [Modeling types](docs/src/content/docs/guides/modeling-types.mdx): objects, scalars, enums, lists, maps, defaults, constraints, environment variables and secrets.
- [Database tables](docs/src/content/docs/guides/database-tables.mdx): keys, relations, indexes, text search, JSON columns, soft delete, transactions, and changing tables that hold data.
- [API routes](docs/src/content/docs/guides/api-routes.mdx): operation sets, the implementation and its `Deps`, parameters, bodies, views, errors, encrypted payloads, traffic controls, webhooks and the Rust server.
- [Auth and permissions](docs/src/content/docs/guides/auth-and-permissions.mdx): which routes need a caller, permissions, service callers, and credentials in each SDK.
- [Stacks and deploys](docs/src/content/docs/guides/stacks.md): declare what runs where, run an environment locally with `stack dev`, and deploy one to Google Cloud.
- [Client SDKs](docs/src/content/docs/guides/client-sdks.mdx): generate and call a client in Go, TypeScript, Python and Rust.
- [The engine](docs/src/content/docs/guides/engine.mdx): run a schema with no generated code, over HTTP, an event stream and MCP.
- [Engine behaviors](docs/src/content/docs/guides/engine-behaviors.md): compose behaviors in TypeScript or JSON; schema-level operations; the runner; Dependencies, Links, Rollups, Search, Reactions, Constants, Variants and Branches.
- [Work queues](docs/src/content/docs/guides/work-queues.mdx): claimable work with `@superschematic/engine-workqueue`: leases, claims, worker heartbeats, blueprints, budgets and retries.
- [Pages with Topcoat](docs/src/content/docs/guides/topcoat.mdx): call a Rust API's operations in-process from a Topcoat app, with forms and records built from the schema.

**Languages**: what the generated code offers in
[Go](docs/src/content/docs/install/go.md),
[TypeScript](docs/src/content/docs/install/typescript.md),
[Python](docs/src/content/docs/install/python.md) and
[Rust](docs/src/content/docs/install/rust.md).

**Reference**

- [Decorators and wrappers](docs/src/content/docs/reference/decorators.md): every core decorator and type wrapper, and the page that covers it.
- [Naming file](docs/src/content/docs/reference/naming.md): every `superschematic.toml` key and its default.
- [CLI](docs/src/content/docs/reference/cli.md): every command and flag.
- [Documentation decorators](docs/src/content/docs/reference/documentation.md): `@docs`, `@purpose` and `@icon`.
- [MCP tools](docs/src/content/docs/reference/mcp-tools.md): publish operations as MCP tools, and the tool documents the SDKs carry.
- [Projection views](docs/src/content/docs/reference/projections.md): read-only SQL views with `@projection`, `@join` and `@column`.
- [Arrays of arrays](docs/src/content/docs/reference/arrays-of-arrays.md): `T[][]` in every generator.
- [JSON-valued scalars](docs/src/content/docs/reference/json-scalars.md): `Generic.JSON`, `Generic.StringMap` and `Embedding.Vector`.
- [Versioned tables](docs/src/content/docs/reference/versioned-tables.md): `@versioned` and `@optimistic`, history tables and fenced writes.
- [Version graphs](docs/src/content/docs/reference/version-graphs.md): branch, commit and merge a tree of tables, with engines in Go, TypeScript, Rust and Python.
- [Schema migrations](docs/src/content/docs/reference/migrations.md): plan a database's change between two versions of a schema with `migrate plan`, its hazards, and the runner that applies it.

**Extending**

- [Write an extension](docs/src/content/docs/extending/write-an-extension.md): add a kind, decorator, document, generator, auth provider or command.
- [Deploy extension](docs/src/content/docs/extending/deploy.md): map `@envVars` fields to a Helm values file.
- [Platform extension](docs/src/content/docs/extending/platform.md): a kind that groups other services.
- [Stack targets](docs/src/content/docs/extending/stack-targets.md): a platform, connector, target, DNS platform or provisioner for the stack model.

**Runtime references.** Each runtime the generated code or the engine
links has its own README:
[schema runtime](runtime/schema/README.md),
HTTP runtime for [Go](runtime/http/go/README.md),
[Rust](runtime/http/rust/README.md) and
[TypeScript](runtime/http/typescript/README.md),
[engine](runtime/engine/README.md),
[engine work queue](runtime/engine-workqueue/README.md) and
[version graph](runtime/versiongraph/README.md) (with its
[TypeScript](runtime/versiongraph/typescript/README.md),
[Rust engine](runtime/versiongraph/rust-engine/README.md) and
[Python](runtime/versiongraph/python/README.md) packages).

**Design.** [`docs/extension-model.md`](docs/extension-model.md) is the
design of the extension seam, [`docs/stack-model.md`](docs/stack-model.md)
the design of stacks and deploys, and [`docs/DECISIONS.md`](docs/DECISIONS.md)
records every design decision (cited as D1, D2, ... in code and commits).

## Examples

| Example | What it shows | Run it |
| --- | --- | --- |
| [`examples/acme-shop`](examples/acme-shop/) | The tutorial's shop on the core binary: five services, a Go and a TypeScript server, and clients in all four languages | `examples/acme-shop/scripts/check.sh` |
| [`examples/acme-schematic`](examples/acme-schematic/) | The same shop with an extension that adds a kind, decorators, a generator, an auth provider, a behavior and commands, without editing the core | `examples/acme-schematic/scripts/smoke.sh` |
| [`examples/engine-notes`](examples/engine-notes/) | A notes server on the engine: workflow, comments and revisions over HTTP, an event stream and MCP | `examples/engine-notes/scripts/check.sh` |
| [`examples/engine-jobs`](examples/engine-jobs/) | A job runner on the engine and its work-queue behaviors: workers that claim jobs under leases over HTTP, retries, budgets, a missed worker's jobs put back, and batches whose steps run in order and settle them | `examples/engine-jobs/scripts/check.sh` |
| [`extensions/deploy`](extensions/deploy/), [`extensions/platform`](extensions/platform/) | Two small extensions: Helm values from `@envVars`, and a kind that groups services | `go test ./extensions/...` |
| [`extensions/topcoat`](extensions/topcoat/) | A Go module of its own: for a Rust API, a crate a [Topcoat](https://github.com/tokio-rs/topcoat) app calls the operations through in-process, by each route's rules, with records of their results | `cd extensions/topcoat && go test ./...` |

## Extending superschematic

The core knows four schema kinds (DB, API, General and Stack), one auth provider
(`session`) and superscalar's generic scalar set. Everything specific to
one organization lives in an extension: a Go package that registers kinds,
decorators, documents, generators, auth providers, build hooks, behaviors
and scalars with the registry. A binary is the core plus the extensions
you link:

```go
func main() {
	root := cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

The installed `superschematic` (`cmd/superschematic`) is that call with the
official extensions (the gcp target, the Cloudflare DNS platform and the
Pulumi provisioner), and the core alone
(`internal/cmd/superschematic-core`) is the call with none. The
naming file, `superschematic.toml` at
the root of your schemas, gives the generated packages their module paths,
npm scope and crate names. See
[Write an extension](docs/src/content/docs/extending/write-an-extension.md)
and the [naming file reference](docs/src/content/docs/reference/naming.md).

## Repository layout

| Path | What it is |
| --- | --- |
| [`cmd/superschematic/`](cmd/superschematic/) | The installed binary, a Go module of its own: the core with the official extensions (gcp, cloudflare, pulumi) linked |
| [`internal/cmd/superschematic-core/`](internal/cmd/superschematic-core/) | The core with no extension linked, which `make cli-smoke` and the examples' scripts run; never shipped |
| [`cli/`](cli/) | `cli.New(Config, ...Extension)` and the commands: `build`, `build-all`, `migrate`, `format`, `json-schema`, `behaviors` |
| [`registry/`](registry/), [`loader/`](loader/), [`schemadeps/`](schemadeps/) | The public packages an extension imports |
| [`internal/`](internal/) | The loader, the generators, the writers, the build plan and the cache |
| [`ir/`](ir/) | The schema IR, its own Go module; [`ir/typescript/`](ir/typescript/) is `@superschematic/schema-ir`, its types and the data form's JSON Schema |
| [`packages/`](packages/) | The authoring packages schemas import: `@superschematic/{schema,db,api,schema-config,stack}` |
| [`runtime/schema/`](runtime/schema/) | The schema runtime generated types link, in Go, TypeScript and Python, and the helpers the generated Rust validators call |
| [`runtime/http/`](runtime/http/) | The HTTP runtime generated servers link, in Go, Rust and TypeScript |
| [`runtime/versiongraph/`](runtime/versiongraph/) | The version-graph core (Rust, with a Go binding and a wasm build) and its engines in Go, TypeScript, Rust and Python |
| [`runtime/migrate/`](runtime/migrate/) | The migration runner: `superschematic-migrate` applies the plans `superschematic migrate plan` writes |
| [`runtime/engine/`](runtime/engine/) | `@superschematic/engine`: runs a schema with no generated code |
| [`runtime/engine-workqueue/`](runtime/engine-workqueue/) | `@superschematic/engine-workqueue`: claimable work for the engine |
| [`stack/`](stack/) | The stack model's resolver: the Stack IR, the resource graph, `environment.json`, and the pinned provider schemas the targets check against offline |
| [`extensions/`](extensions/), [`examples/`](examples/) | The official extensions (`gcp`, `cloudflare`, `pulumi`), `topcoat`, and example extensions and projects |
| [`docs/`](docs/) | The docs site, the decision log and the extension design |
| [`superschematic.toml`](superschematic.toml) | The default naming file, every key written out |

The repository has eleven Go modules: the root (the compiler), `ir`,
`runtime/schema/go`, `runtime/http/go`, `runtime/versiongraph/go`,
`runtime/migrate/go` (the migration runner), `extensions/gcp`,
`extensions/cloudflare`, `extensions/pulumi`, `extensions/topcoat` and
`cmd/superschematic` (the installed binary). Generated code imports the
runtimes and the IR, never the compiler, and the root module never imports
an extension.

## Development

Pins: [`tools.env`](tools.env) (Go, Node, Bun, Python, uv, Rust) and
[`superscalar.pin`](superscalar.pin) (the superscalar commit).

```sh
make setup          # superscalar checkout and build, bun install, uv sync
make build          # go build every module; bin/superschematic
make test           # Go, catalog drift, TypeScript, Python, Rust, CLI smoke
make lint           # go vet, gofmt, golangci-lint, scrub
make docs           # build the docs site; fails on a broken link
```

superscalar has no release yet, so its Go binding is built from source:
`scripts/superscalar-dep.sh` checks the pinned commit out under
`third_party/superscalar`, builds its static archive and TypeScript
binding, and prints the `CGO_LDFLAGS` Go needs. To build or test generated
Go code by hand, export the same flags the Makefile uses:

```sh
export GOTOOLCHAIN=go1.26.4
export CGO_LDFLAGS="$(scripts/superscalar-dep.sh --print) $(scripts/versiongraph-archive.sh --print)"
```

[`CONTRIBUTING.md`](CONTRIBUTING.md) has every make target, the test
layout and the release procedure. [`docs/README.md`](docs/README.md) covers
writing docs pages.

## Status

Pre-release. The first tag will be `v0.1.0-alpha.1`; until it is cut, the Go
modules are consumed at a commit and nothing is published to npm, PyPI or
crates.io. The API surface an extension depends on (`registry`, `loader`,
`cli`, `schemadeps`, `generator.Naming`) is not frozen.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Commits need a DCO sign-off
(`git commit -s`). Report security issues as [`SECURITY.md`](SECURITY.md)
describes. superschematic is maintained by Parable Work, Inc. and licensed
under Apache-2.0 ([`LICENSE`](LICENSE)).
