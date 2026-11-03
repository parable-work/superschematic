# superschematic

**Build whole applications from a schema.** superschematic is a compiler
for backends. You, or the coding agent you work with, describe the data,
the operations and where they run in a few short schema files. The
compiler writes everything in between: database tables and migrations,
validation in every language, servers that check who is calling and what
they may do, client SDKs, and the containers, CI and deploys of each
environment.

## Agents shouldn't write plumbing

Nobody asks a coding agent to write assembly. A compiler does that job
better: the same right answer every time, checked, in milliseconds.

Most of a backend is the same kind of work. A `CREATE TABLE`, the ORM
struct over it, the request decoder, the email regex, the permission
middleware, the OpenAPI document, a client in each language that calls
the API. Each layer has one right answer, each restates the others, and
an agent that writes them by hand works all of it out again on every
project and gets some of it subtly wrong: a column that is nullable in
SQL but required in the SDK, an email address the server accepts and the
client refuses, a route that forgot its permission check.

superschematic moves that work into the compiler. The agent writes:

- **the schema**: types, tables, operations and their permissions, jobs,
  queues, and the environments everything runs in;
- **the body of each operation**, in plain Go, TypeScript or Rust,
  against an interface the compiler generated;
- **tests**.

The compiler writes the rest, and writes it again on every change, so a
schema edit reaches every layer at once and the type checker in each
language points at the code that has to follow. Review gets easier too:
a change is a schema diff of a few dozen lines and the business logic
beside it, not thousands of lines of plumbing a reviewer has to trust.

## What is built in

| | |
| --- | --- |
| **Validation** | 49 scalar types (`Contact.Email`, `Identity.UUID`, `Temporal.DateTime`, ...) and the constraints you declare parse and validate identically in Go, TypeScript, Python and Rust. An SDK refuses the request the server would refuse. |
| **Security** | Sign-in, sessions and a user model; permissions declared on each operation and checked by the generated server; service-to-service callers admitted per operation; rate, body and time limits; webhook signatures; encrypted payloads. |
| **Data** | Postgres and SQLite tables with keys, relations, indexes and full-text search; a typed Go ORM; row history, branches and merges; migration plans between two versions of a schema, in expand and contract phases, with each step's hazards. |
| **APIs and clients** | A server in Go, Rust or TypeScript, its OpenAPI, SDKs in four languages, and MCP tools, so other agents can call what you built. |
| **Background work** | Scheduled jobs, and queues whose messages are written in the same transaction as the change that causes them, with workers to drain them. |
| **Scaling** | Servers autoscale on Cloud Run or run on Cloudflare Workers, with instances and concurrency set per environment in the schema; queue workers run the instances and concurrency each environment sets; static sites sit behind a CDN. |
| **Deployment** | `stack dev` runs every service on your machine, with Postgres in Docker and its migrations applied. `stack deploy` ships an environment to Google Cloud (Cloud Run, Cloud SQL, Cloud Storage) or Cloudflare (Workers, D1, R2) through Pulumi, running migrations between steps. |
| **CI** | A generated GitHub Actions workflow that checks, plans, previews and deploys each environment. |
| **Observability** | Every server, job and worker exports OpenTelemetry traces when an environment names a collector. |

## What a schema looks like

A table, in a DB schema. The field types carry their own validation:

```ts
export abstract class Product {
  @key id: AutoGenerate<Identity.UUID>;
  @unique sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: Default<boolean, true>;
}
```

A route over it, in an API schema, with a validated input and a
permission:

```ts
export abstract class CreateProductInput {
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Validate<Generic.Int64, { min: 0 }>;
}

export class ProductMutations extends Authenticated {
  @rest(HttpMethod.POST, "products")
  @requirePermission(["products.write"])
  createProduct(input: CreateProductInput): ProductView {
    throw new Error("schema declaration only");
  }
}
```

From these the build writes the `CREATE TABLE`, the Go repository, the
types in each language, a server that routes, authenticates, decodes,
validates and checks `products.write` before your `CreateProduct` method
runs, the OpenAPI, and a client in each language that validates before it
sends. Both snippets are trimmed from
[`examples/acme-shop`](examples/acme-shop/), a shop with Go, Rust and
TypeScript servers, jobs, a queue, a static site and a stack, which CI
builds, runs locally and tests end to end twice a day. Its fourteen
services are about 1,650 lines of schema and config, comments included;
a build writes about 144,000 lines of SQL, Go, TypeScript, Python and Rust
from them.

## The loop

```sh
superschematic init                                   # a schemas root
superschematic new service shop-db --kind DB          # a service with a starter schema
superschematic check --watch                          # every edit verified in milliseconds
superschematic build-all schemas/services             # generate every layer
superschematic stack init                             # what runs where, over your APIs
superschematic stack dev                              # the whole stack on your machine
superschematic stack deploy Staging                   # an environment, in the cloud
```

`check` answers in tens of milliseconds, with errors that name the field
and say the fix, so an agent can edit, check and correct in a tight loop
and build only when the schemas are clean. `superschematic catalog`
prints the scalars, decorators and capabilities of the binary you run,
its extensions included, so an agent reads a reference that matches its
compiler.

### With a coding agent

The superschematic skill teaches an agent what to declare instead of
write, the order of work, and when to stop and ask. In Claude Code:

```text
/plugin marketplace add parable-work/superschematic
/plugin install superschematic@superschematic
```

Any agent that reads [Agent Skills](https://agentskills.io/specification)
can use it; [`agent/`](agent/) says how to install it elsewhere.

## When the schema can't say it

Your code is plain code. Each operation's body is a method on a
generated interface, in an ordinary Go, TypeScript or Rust module you own,
with the ORM, the SDK clients of the APIs it calls and its configuration
handed to it. Rules that span rows or call out to the world live there.

When the shape of the data is only known at run time, the
[engine](runtime/engine/README.md) runs a schema with no generated code,
with workflow, comments, revisions, approvals and search composed in as
behaviors.

When your organization needs its own conventions, an extension adds
schema kinds, decorators, generators, auth providers, commands and deploy
targets without forking the core. See
[Write an extension](docs/src/content/docs/extending/write-an-extension.md).

## Get started

superschematic is pre-release, so you build it from a checkout. You need
Go, Rust, Bun, Node and a C compiler; [`tools.env`](tools.env) has the
exact versions and [Prerequisites](docs/src/content/docs/start/prerequisites.md)
says what each is for. A TypeScript project needs no Rust: `make cli`
builds the CLI over superscalar's archive, downloaded prebuilt where
`cargo` is missing ([Without Rust](CONTRIBUTING.md#without-rust)).

```sh
git clone https://github.com/parable-work/superschematic
cd superschematic
make setup
make build
export PATH="$PWD/bin:$PATH"
```

Then build and test the example shop end to end:

```sh
examples/acme-shop/scripts/check.sh
```

From there, [Getting started](docs/src/content/docs/start/getting-started.mdx)
builds one schema, and [Your first project](docs/src/content/docs/first-project/index.mdx)
grows it into a database and APIs in Go and TypeScript.

## Learn more

The [documentation](https://parable-work.github.io/superschematic/) is
published from the first release tag; its source is under
[`docs/`](docs/src/content/docs/).

- [How it works](docs/src/content/docs/start/how-it-works.mdx): services, kinds, outputs and the compiler's model.
- [Capability map](docs/src/content/docs/start/capabilities.md): each thing a backend needs, the declaration that gives it, and what you still write.
- [Examples](docs/src/content/docs/start/examples.md): the example projects and what each one shows.
- [Stacks and deploys](docs/src/content/docs/guides/stacks.mdx): what runs where, locally and in the cloud.
- [CLI reference](docs/src/content/docs/reference/cli.md): every command and flag.

## Status

Pre-release. The first tag will be `v0.1.0-alpha.1`; until it is cut,
nothing is published to npm, PyPI or crates.io, and the APIs an extension
depends on are not frozen.

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) covers the toolchain, the
repository's layout, the gates and releases. Commits need a DCO sign-off
(`git commit -s`). Report security issues as [`SECURITY.md`](SECURITY.md)
describes. superschematic is maintained by Parable Work, Inc. and
licensed under Apache-2.0 ([`LICENSE`](LICENSE)).
