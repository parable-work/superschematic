---
title: Capability map
description: Each thing a backend needs, the declaration that gives it, what the build generates from it, and what is left for you to write.
sidebar:
  order: 4
---

A superschematic project is mostly declarations. This page maps each
thing a backend needs to the schema construct that provides it, what
`superschematic build` generates from it, and the code that is still
yours. It is the place to start when you know what you want and not yet
how to say it. Each row links the guide that covers it.
`superschematic catalog capabilities` prints this page, and
`superschematic catalog scalars`, `decorators` and `behaviors` print the
exact constructs the binary you run accepts.

## What you never write

The build writes all of this from the schema, in every language the
service's config lists, and regenerates it on every change:

- SQL DDL for Postgres and SQLite, and the migration plan between two
  versions of a database
- the Go ORM and the TypeScript repositories: typed repositories,
  filters, pages, transactions, and reads that select columns and include
  related rows
- request and response types, and the decoders that validate them, in
  Go, TypeScript, Python and Rust
- the HTTP server: routing, argument decoding, validation, permission
  checks, rate, body and time limits, pagination, webhook signature checks
- OpenAPI, client SDKs in four languages, MCP tool documents, and on a
  TypeScript API an MCP endpoint that serves the tools
- environment variable loaders, and the server entrypoints, Dockerfiles
  and CI of a stack

A schema change reaches every one of these at once. When the change
breaks your code, the type checker of each language points at the line.

## What you still write

- The body of each operation, against the generated interface: the ORM
  calls, the business rules, the response.
- Ownership: `@requireOwnership` makes the route need a caller, and your
  implementation decides whether the caller owns what it touches.
- The values of `@virtual` fields of a view, which have no column.
- The body of each job and each worker's handler.
- The handler of a `@manualRouteRegistration` operation, for a response
  the generated route cannot give.

## Starting

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A project | nothing: `superschematic init` writes the schemas root, its naming file, base tsconfig and ignore rules | nothing yet | nothing | [CLI](/superschematic/reference/cli/#init-schemas-root) |
| A service of any kind | nothing: `superschematic new service <name> --kind DB\|API\|General\|Bucket\|Site\|MemoryStore` writes its config, package files and a starter schema | its first build, and the handle other configs import | the schema, from the starter | [CLI](/superschematic/reference/cli/#new-service-name---kind-dbapigeneralbucketsitememorystore) |

## Data

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A shared type, used by several services | an abstract class in a General schema | the type and its validators in each language | nothing | [Modeling types](/superschematic/guides/modeling-types/#object-types) |
| A field that is an email, a URL, a UUID, a slug, money, a time | a [scalar](/superschematic/reference/scalars/) as the field's type: `email: Contact.Email` | the parse, normalize and check in every decoder, and the column's SQL type | nothing | [Modeling types](/superschematic/guides/modeling-types/#scalars) |
| A range, a length, a pattern | `Validate<T, { min, max, minLength, maxLength, pattern }>` | the check in every decoder, the SDKs' included | nothing | [Modeling types](/superschematic/guides/modeling-types/#constraints) |
| An optional field, a default | `Nullable<T>` or `name?: T`; `Default<T, V>` | the decoders' handling of absent and null | nothing | [Modeling types](/superschematic/guides/modeling-types/#required-nullable-and-optional) |
| A fixed set of values | an `enum` | the enum in each language, checked on decode | nothing | [Modeling types](/superschematic/guides/modeling-types/#enums) |
| A table | an abstract class in a DB schema, with a `@key` | the DDL, a Go repository, the types | nothing | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| A generated key | `@key id: AutoGenerate<Identity.UUID>` | the database's default on insert | nothing | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| A foreign key, one-to-many | `Relation<T, { onDelete }>`, `HasMany<T>` | the constraint, both ORMs' reads that include the related rows, and filters through a relation | nothing | [Database tables](/superschematic/guides/database-tables/#relations) |
| A unique column, an index | `@unique`, `@index<T>([...], { unique })` | the constraint or index | nothing | [Database tables](/superschematic/guides/database-tables/#indexes) |
| An import by a natural key, in bulk, writing only what changed | a unique key on the table | `UpsertMany`/`upsertMany`: one `INSERT ... ON CONFLICT` per batch, each row inserted, updated or unchanged, an unchanged row unstamped and its triggers unfired; an `Encrypted<T>` field set under the key of the row it updates | the call, naming the key and the fields an update takes | [Database tables](/superschematic/guides/database-tables/#upserts) |
| Nearest neighbours a filter keeps, k of them | an `hnsw` or `ivfflat` `@index` on a vector field | `nearest`'s `iterativeScan`, `efSearch`, `probes` and the rest, set for the read's transaction | the call | [Database tables](/superschematic/guides/database-tables/#vectors) |
| Text search over a table | `@searchField` on the fields to search | a trigram-indexed `search_text` column | nothing | [Database tables](/superschematic/guides/database-tables/#text-search) |
| A nested object in a column | `@jsonField` on the class or field | a `JSONB` column, typed in every language | nothing | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| A field encrypted at rest | `Encrypted<string>`, and `outputs.sql.encryption.key` naming an `@envVars` secret | AES-256-GCM sealing and opening in the Go ORM and the TypeScript repositories, the key wired to every server, job and worker, `Reencrypt` for a rotation | nothing | [Database tables](/superschematic/guides/database-tables/#encrypted-fields) |
| Tables and columns of your own names | `@table("lineItem")`, `@column("createdAt")` | the quoted names in the DDL and every name derived from them, both ORMs, renames in a migration plan | nothing | [Database tables](/superschematic/guides/database-tables/#table-and-column-names) |
| Every past version of a row | `@versioned` | a history table, triggers, history readers, writes fenced by version | nothing | [Versioned tables](/superschematic/reference/versioned-tables/) |
| Branches, commits and merges over a set of tables | `@versionGraph`, `@graphMember` | the graph's tables and an engine in each language | nothing | [Version graphs](/superschematic/reference/version-graphs/) |
| A read-only SQL view | `@projection`, `@join`, `@column` | the view, its row type and a read-only repository in both ORMs, and an API view's `@source` | nothing | [Projections](/superschematic/reference/projections/) |
| A report: aggregates, time buckets, a paged list over joined tables | `@query<Params, Row>({ name, sql, orderBy?, page? })`, its params and row as types | a typed function of both ORMs, `orm.queries.<name>(params)` and `Queries.<Name>(ctx, params)`, its rows decoded; `queries verify` prepares it on Postgres into `queries.lock`, which `check`, `build` and `migrate plan` hold it to | the SQL | [Declared queries](/superschematic/reference/declared-queries/) |
| A schema change on a database that holds data | the next version of the DB schema | `migrate plan`: expand and contract steps, each with its hazards | the review of the plan | [Migrations](/superschematic/reference/migrations/) |

## APIs

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Routes | a class of methods in an API schema, each with `@rest(method, path)` | the router, the decoders, the handler interface, OpenAPI | each method's body | [API routes](/superschematic/guides/api-routes/#operation-sets) |
| A response that is a view of a table | a class with `@source(Table)`, listing the fields to show | the view type, checked field by field against the table | the row-to-view mapping | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| A validated request body | an argument whose type is a class with `Validate` fields | a 400 with every failure at once, keyed by path, before your code runs | nothing | [API routes](/superschematic/guides/api-routes/#arguments) |
| Query parameters | `QueryParam<T>` arguments | their decoding and checks | nothing | [API routes](/superschematic/guides/api-routes/#arguments) |
| Paged, sorted, searchable lists | `@paginated`, a `SortBy<Item, [...]>` and a `SearchQuery` argument | `cursor` and `limit`, `{ items, next }`, cursors that hold the sort | the query for one page | [API routes](/superschematic/guides/api-routes/#pages) |
| Files in and out | a `FileUpload<C>` field of an input type; a `FileDownload<C>` result | the multipart body's reading and each file's checks, the file's `Content-Type` and `Content-Disposition`, OpenAPI, SDK methods that take a `File` and return a `Blob` | what the file holds | [API routes](/superschematic/guides/api-routes/#files) |
| A field that names an item of another list | `RefersTo<T, Target>` | the operation that lists the targets, recorded for the documents, forms and pickers | nothing | [References and search](/superschematic/reference/references-and-search/) |
| Errors with the right status | nothing | `NotFoundError`, `BadRequestError`, `ConflictError` and the rest | returning one | [API routes](/superschematic/guides/api-routes/#errors) |
| Rate, body and time limits | `@rateLimit`, `@bodyLimit`, `@timeout` | the limits, in every server | nothing | [API routes](/superschematic/guides/api-routes/#traffic-controls) |
| A rate limit every replica shares | `rateLimitStore: ShopCache` in the API's config, a MemoryStore it lists | the `@rateLimit` routes counted in the store, by httprate's sliding window, each replica on its own budget while the store is unavailable | nothing | [Memory stores](/superschematic/guides/memory-stores/#share-a-rate-limit) |
| A webhook a third party calls | `@webhook` and `@hmacVerified({ provider })` | the signature check before anything else | the handler, and the provider's verifier unless it signs with a JWT carrying the body's hash, as Plaid does, which every runtime checks built in | [API routes](/superschematic/guides/api-routes/#webhooks) |
| The server in another language | `outputs.api.language`: `GO`, `TYPESCRIPT` or `RUST` | the same routes and checks in that language | the implementation in it | [API routes](/superschematic/guides/api-routes/#the-typescript-server) |
| Settings and secrets for a server | a class with `@envVars`, fields `Secret<T>` where secret | a typed loader and `values-schema.json` | nothing | [Modeling types](/superschematic/guides/modeling-types/#environment-variables) |

## Users and access

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Routes that need a signed-in caller | `extends Authenticated` on the set, or `@auth` | the check, a 401 without a caller | nothing | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| Permissions | `@requirePermission(["orders.write"])` | the check, a 403 without one | nothing | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| Users, sessions, sign-in | a table that `implements User<{ login }>` and one that `implements UserRole`; an API whose `authDb` is that database, with a `@userSessions` class | the `Session` and credential tables, the sign-in, sign-out and password routes, roles and grants | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#turn-it-on) |
| User and role administration | `@userAdministration` | the routes that manage users, roles and grants | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#serve-the-session-routes) |
| One service calling another | `calls: [Other]` in the caller's config; `@requireService` or `@allowService` on the callee | a typed client of the callee in the caller's `Deps`, and the callee's check of the calling service | the call | [Auth and permissions](/superschematic/guides/auth-and-permissions/#service-callers) |
| An encrypted request body | `extends Encrypted`, `@encrypted` or `EncryptedField<T>` | the envelope's handling in the server and the SDKs | the decryptor | [API routes](/superschematic/guides/api-routes/#encrypted-payloads) |

## Background work

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A scheduled or on-demand job | a class with `@job({ schedule, timeZone, timeout, retries })` in an API schema | the job's interface and a deployable the stack runs | the job's body | [Stacks](/superschematic/guides/stacks/#jobs) |
| A queue, written in the transaction that causes it | a class with `@queue({ retries, backoff, lease })` in a DB schema | the queue's table, `Enqueue<Class>` in the ORM, retries and dead messages | the enqueue call | [Queues and workers](/superschematic/guides/queues/#declare-a-queue) |
| A worker that handles a queue | a class with `@worker({ queue, concurrency })` in an API schema | the claim loop, a deployable the stack runs | the handler | [Queues and workers](/superschematic/guides/queues/#write-a-worker) |
| One waiting message per key, or one running | `@queue({ key, onDuplicate: "replace", serialize: true })` | the unique indexes, the enqueue's `ON CONFLICT`, claims that hold a key to one worker | nothing | [Queues and workers](/superschematic/guides/queues/#one-message-per-key) |
| A message whatever writes a row | `@enqueueOnChange(Queue, { on, fields, when, message })` on a DB table | a trigger on Postgres and SQLite, its migration | nothing | [Queues and workers](/superschematic/guides/queues/#enqueue-on-a-change) |

## Files, front ends and clients

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A private file bucket | a Bucket service, listed in an API config's `buckets` | the bucket in the API's `Deps`, signed upload and download URLs | which object to sign for | [Buckets](/superschematic/guides/buckets/#declare-a-bucket) |
| Wake-ups, counters and claims every replica shares | a MemoryStore service whose schema declares each `@channel` and `@key`, listed in an API config's `memoryStores` | typed definitions of each channel and key, the store in the API's `Deps` on Redis or Valkey, Valkey under `stack dev` and Memorystore on gcp | the publish, the subscribe and the claim, and the path for when the store is unavailable | [Memory stores](/superschematic/guides/memory-stores/#declare-a-store) |
| A static front end | a Site service | the config, served in each environment, that holds each API's address, and CORS on each API it calls | the front end | [Static sites](/superschematic/guides/static-sites/#declare-a-site) |
| Clients in Go, TypeScript, Python, Rust | a line per language in `outputs.sdk` | an SDK with the server's checks, pages, errors and retries | nothing | [Client SDKs](/superschematic/guides/client-sdks/#generate-them) |
| Tools an AI agent can call | `@mcp` on operations | MCP tool documents beside the SDKs, and with `outputs.api.mcp` a TypeScript API's endpoint at `POST /api/mcp` | nothing | [MCP tools](/superschematic/reference/mcp-tools/) |
| Labels and controls for forms | `@docs`, `@purpose`, `@display`, `@uiControl` | the metadata a schema form reads | nothing | [Documentation](/superschematic/reference/documentation/) |

## Running and deploying

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Everything running on your machine | a Stack service, which `stack init` writes | server entrypoints; `stack dev` runs Postgres in Docker, applies the migrations and serves | nothing | [Stacks](/superschematic/guides/stacks/#start-a-stack) |
| Environments: dev, staging, production | `@environment` classes in the Stack schema | one resolved `environment.json` each | the values | [Stacks](/superschematic/guides/stacks/#environments) |
| Google Cloud or Cloudflare | an environment with the `gcp` or `cloudflare` target | `stack plan` and `stack deploy`, which build what changed, deploy it and run the migrations between steps | nothing | [Stacks](/superschematic/guides/stacks/#deploy-to-google-cloud) |
| CI that deploys | `outputs.ci` on the Stack service | the workflow | nothing | [Stacks](/superschematic/guides/stacks/#generated-ci) |
| Traces | nothing in a schema: OpenTelemetry's own `OTEL_` variables | spans from every server, job and worker | nothing | [Traces](/superschematic/guides/traces/) |

## A backend with no generated code

When the data changes shape at run time, or you want workflow, comments,
revisions, links, rollups, search and work queues without writing a
server, publish a schema to the engine instead. Its types compose
behaviors with `@behavior(name, config)`, and the engine serves their
instances over HTTP, an event stream and MCP.

| You need | Declare | Guide |
| --- | --- | --- |
| Instances, an event log, access control, over HTTP and MCP | a schema published to `@superschematic/engine` | [The engine](/superschematic/guides/engine/) |
| States and transitions, comments, revisions, links, rollups, approvals, timers, numbering | `@behavior("Workflow", { ... })` and the others on a type | [Engine behaviors](/superschematic/guides/engine-behaviors/) |
| Claimable work: leases, assignment, priorities, retries | the `@superschematic/engine-workqueue` behaviors | [Work queues](/superschematic/guides/work-queues/) |

## Your own conventions

A pattern that the core does not have becomes an extension: a Go package
that adds kinds, decorators, generators, auth providers, behaviors,
deploy targets or commands to a binary of your own. See
[Write an extension](/superschematic/extending/write-an-extension/).
