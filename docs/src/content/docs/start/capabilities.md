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
  TypeScript, a Rust or a Go API an MCP endpoint that serves the tools
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
| A service of any kind | nothing: `superschematic new service <name> --kind DB\|API\|General\|Bucket\|Site\|MemoryStore\|Tasks` writes its config, package files and a starter schema | its first build, and the handle other configs import | the schema, from the starter | [CLI](/superschematic/reference/cli/#new-service-name---kind-dbapigeneralbucketsitememorystoretasks) |

## Data

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A shared type, used by several services | an abstract class in a General schema | the type and its validators in each language | nothing | [Modeling types](/superschematic/guides/modeling-types/#object-types) |
| A field that is an email, a URL, a UUID, a slug, money, a time | a [scalar](/superschematic/reference/scalars/) as the field's type: `email: Contact.Email` | the parse, normalize and check in every decoder, and the column's SQL type | nothing | [Modeling types](/superschematic/guides/modeling-types/#scalars) |
| A range, a length, a pattern | `Validate<T, { min, max, minLength, maxLength, pattern }>` | the check in every decoder, the SDKs' included | nothing | [Modeling types](/superschematic/guides/modeling-types/#constraints) |
| An optional field, a default | `Nullable<T>` or `name?: T`; `Default<T, V>` | the decoders' handling of absent and null | nothing | [Modeling types](/superschematic/guides/modeling-types/#required-nullable-and-optional) |
| A payload that gains keys over time | `@allowUnknownFields` on its type; a `@webhook`'s input reads this way unless `@strictJSON` | every decoder and server accepting and dropping the keys the type does not declare | nothing | [Modeling types](/superschematic/guides/modeling-types/#lenient-decoding) |
| A fixed set of values | an `enum` | the enum in each language, checked on decode | nothing | [Modeling types](/superschematic/guides/modeling-types/#enums) |
| A table | an abstract class in a DB schema, with a `@key` | the DDL, a Go repository, the types | nothing | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| A generated key | `@key id: AutoGenerate<Identity.UUID>` | the database's default on insert | nothing | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| A timestamp the database stamps | `createdAt` and `updatedAt`; `Default<Temporal.DateTime, "now">` on any other | `DEFAULT CURRENT_TIMESTAMP`; every other required time has none, so an insert that leaves it out fails | nothing | [Database tables](/superschematic/guides/database-tables/#columns) |
| A foreign key, one-to-many | `Relation<T, { onDelete }>`, `HasMany<T>` | the constraint, both ORMs' reads that include the related rows, and filters through a relation | nothing | [Database tables](/superschematic/guides/database-tables/#relations) |
| A unique column, an index | `@unique`, `@index<T>([...], { unique })` | the constraint or index | nothing | [Database tables](/superschematic/guides/database-tables/#indexes) |
| An import by a natural key, in bulk, writing only what changed | a unique key on the table | `UpsertMany`/`upsertMany`: one `INSERT ... ON CONFLICT` per batch, each row inserted, updated or unchanged, an unchanged row unstamped and its triggers unfired; an `Encrypted<T>` field set under the key of the row it updates | the call, naming the key and the fields an update takes | [Database tables](/superschematic/guides/database-tables/#upserts) |
| Nearest neighbours a filter keeps, k of them | an `hnsw` or `ivfflat` `@index` on a vector field | `nearest`'s `iterativeScan`, `efSearch`, `probes` and the rest, set for the read's transaction | the call | [Database tables](/superschematic/guides/database-tables/#vectors) |
| Text search over a table | `@searchField` on the fields to search | a trigram-indexed `search_text` column | nothing | [Database tables](/superschematic/guides/database-tables/#text-search) |
| One ranked search over many tables, each caller kept to the rows they may read | `@searchable<T>({ title, body, scope, queue })` on each table, on Postgres | one index the database shares, its triggers and the queue that keeps it, and `searchTables` in the TypeScript runtime, `search_tables` in the Rust one and `crosstable.SearchTables` in the Go one, which check access inside the query | the route or MCP tool that searches, and the queue's handler | [Database tables](/superschematic/guides/database-tables/#search-across-tables) |
| A nested object in a column | `@jsonField` on the class or field | a `JSONB` column, typed in every language | nothing | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| Who wrote a row | `createdBy`, `updatedBy`, `deletedBy` fields; `outputs.sql.systemActor` | each stamped with an operation's caller, with nothing in the method, and with the system actor where a job or a worker asks for it (`asSystem()`, `AsSystem(ctx)`) | nothing | [Database tables](/superschematic/guides/database-tables/#who-wrote-a-row) |
| Two rows that turn out to be one thing | `implements Mergeable` on the table | `mergeInto({ from, into })` on its Go and TypeScript repositories: one transaction that repoints every relation of the schema that references `from`, deletes a row a unique key would then refuse, and deletes `from` | the call, and what to keep of `from` | [Database tables](/superschematic/guides/database-tables/#merging-rows) |
| A row that points at a row of any table, files on any row, and the rows that point back | `@references<T>` and `@attachments<T>` on a table, on Postgres | the sub-tables that hold them, the triggers that clear a deleted target's references, the TypeScript repositories' `references` and backlinks held to what the reader may read, uploads straight to a bucket through a signed URL | the routes that ask for an upload and list a row's files, and the handlers of the cleared and released queues | [Database tables](/superschematic/guides/database-tables/#references-and-files-on-any-row) |
| Reads sent to a read replica | nothing in a schema: `ConnectWithPools(primary, replica)` in Go | the Go ORM over two pools, its writes on the primary, and `ReadReplica()` for the reads that may lag | which reads go there | [Database tables](/superschematic/guides/database-tables/#a-read-replica) |
| A field encrypted at rest | `Encrypted<string>`, and `outputs.sql.encryption.key` naming an `@envVars` secret | AES-256-GCM sealing and opening in the Go ORM and the TypeScript repositories, the key wired to every server, job and worker, `Reencrypt` for a rotation | nothing | [Database tables](/superschematic/guides/database-tables/#encrypted-fields) |
| Tables and columns of your own names | `@table("lineItem")`, `@column("createdAt")` | the quoted names in the DDL and every name derived from them, both ORMs, renames in a migration plan | nothing | [Database tables](/superschematic/guides/database-tables/#table-and-column-names) |
| A status only its listed moves change, whatever writes the row | `@stateMachine<T>({ field, transitions, terminal })` on the class of an enum field | on a DB table, a trigger on Postgres and SQLite that refuses any other move, and its migration; in Go, TypeScript, Python and Rust, the terminal states and a function that says whether a move is listed | nothing; the answer to a refused move, where you want a 409 | [Database tables](/superschematic/guides/database-tables/#state-machines) |
| A counter every write of a row advances, or one for each party that writes it | `@fenced<T>({ field, covers })` on a DB table | a trigger on Postgres and SQLite that refuses a write that does not advance it, its migration, and repositories that advance it themselves | the compare, where a writer needs one: a filter on the counter | [Database tables](/superschematic/guides/database-tables/#fences) |
| A workflow, deadlines, sign-offs, leases, schedules and the other behaviors on a table's own rows | a field typed as a behavior, `status: Workflow<{ ... }>`, or `implements Timers<T, { ... }>` and the others on the table | the behavior's fields as the table's columns, which the generated repositories read and only the behavior's operations change, through the behaviors' core | nothing | [Database tables](/superschematic/guides/database-tables/#beyond-plain-tables) |
| Dated occurrences of a rule, and a calendar of many tables' dates | `implements Recurrence<T, { ... }>` on a table; `@calendarSource` on each table the calendar shows | occurrences the core dates alike in every language, opened, reminded, done or missed, off weekends and holidays; `calendarRoutes` in the TypeScript runtime, `calendar_routes` in the Rust one and `crosstable.CalendarRoutes` in the Go one, on Postgres, for the calendar read and its feeds, held to what the reader may read | nothing | [Database tables](/superschematic/guides/database-tables/#a-rows-recurrence-and-the-calendar) |
| Every past version of a row | `@versioned` | a history table, triggers, history readers, writes fenced by version | nothing | [Versioned tables](/superschematic/reference/versioned-tables/) |
| Branches, commits and merges over a set of tables, siblings ordered by keys an insert writes one row for, and a table of what is released | `@versionGraph`, `@graphMember` | the graph's tables and an engine in each language | nothing | [Version graphs](/superschematic/reference/version-graphs/) |
| Routes over a version graph's operations: change sets, commits, merges and releases, with no handler to write | `@graphOperations(Root)` on a class of an API schema | a route per operation with typed inputs and results, in the OpenAPI and the SDKs, served by the TypeScript, Go and Rust servers | nothing | [API routes](/superschematic/guides/api-routes/#a-version-graphs-operations) |
| Routes over a table's references, backlinks and files: uploads straight to a bucket, a file filed elsewhere, the rows that point at a row, each read held to what the caller may read | `@rowFeatureOperations(Table)` on a class of an API schema | a route per operation with typed inputs and results, in the OpenAPI and the SDKs, MCP tools for the reads, served by the TypeScript and Go servers | nothing | [API routes](/superschematic/guides/api-routes/#a-tables-references-and-files) |
| A read-only SQL view | `@projection`, `@join`, `@column` | the view, its row type and a read-only repository in both ORMs, and an API view's `@source` | nothing | [Projections](/superschematic/reference/projections/) |
| A report: aggregates, time buckets, a paged list over joined tables | `@query<Params, Row>({ name, sql, orderBy?, page?, returns? })`, its params and row as types, its SQL sharing string constants | a typed function of both ORMs, `orm.queries.<name>(params)` and `Queries.<Name>(ctx, params)`, its rows decoded; `queries verify` prepares it on Postgres into `queries.lock`, which `check`, `build` and `migrate plan` hold it to | the SQL | [Declared queries](/superschematic/reference/declared-queries/) |
| A schema change on a database that holds data | the next version of the DB schema | `migrate plan`: expand and contract steps, each with its hazards | the review of the plan | [Migrations](/superschematic/reference/migrations/) |

## APIs

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Routes | a class of methods in an API schema, each with `@rest(method, path)` | the router, the decoders, the handler interface, OpenAPI | each method's body | [API routes](/superschematic/guides/api-routes/#operation-sets) |
| A response that is a view of a table | a class with `@source(Table)`, listing the fields to show | the view type, checked field by field against the table | the row-to-view mapping | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| A validated request body | an argument whose type is a class with `Validate` fields | a 400 with every failure at once, keyed by path, before your code runs | nothing | [API routes](/superschematic/guides/api-routes/#arguments) |
| Query parameters | `QueryParam<T>` arguments | their decoding and checks | nothing | [API routes](/superschematic/guides/api-routes/#arguments) |
| Read, list, create, update and delete a table's rows through views | `@tableOperation()` on a method whose views, its result and its body, are `@source` views of one table | the handler: the row copied to and from the view through the repositories, in one transaction and as the caller, with its 404s, 409s and 400s | nothing for these operations | [API routes](/superschematic/guides/api-routes/#operations-the-build-serves) |
| Keep each caller to their own rows | `@tableOperation({ by: ["ownerId"] })`: the row's members that name who it is for, and `overridePermission` for who reaches every row | the filter on a read, a list, an update and a delete, a 404 for a row that names someone else, and the caller set on a create | nothing for these operations | [API routes](/superschematic/guides/api-routes/#row-rules) |
| Routes for a table's behaviors: its transitions, sign-offs, deadlines and claims | `@behaviorOperations(Table)` on an empty class of the API schema | a route, an SDK method and an MCP tool per operation of each behavior the table composes, with a generated handler that runs it through the behaviors' core in one transaction | nothing | [API routes](/superschematic/guides/api-routes/#a-tables-behavior-operations) |
| Paged, sorted, searchable, filtered lists | `@paginated`, a `SortBy<Item, [...]>`, a `SearchQuery` and a `FilterBy<Item, [...]>` argument | `cursor` and `limit`, `{ items, next }`, cursors that hold the sort; `filter[field]=value` and `filter[field][op]=value` read, checked and handed over typed, and a typed filter in each SDK | the query for one page | [API routes](/superschematic/guides/api-routes/#pages) |
| Files in and out | a `FileUpload<C>` field of an input type; a `FileDownload<C>` result | the multipart body's reading and each file's checks, the file's `Content-Type` and `Content-Disposition`, attachment or inline, its sandbox, and 304 to a request that holds its entity tag, OpenAPI, SDK methods that take a `File` and return a `Blob` | what the file holds | [API routes](/superschematic/guides/api-routes/#files) |
| A field that names an item of another list | `RefersTo<T, Target>` | the operation that lists the targets, recorded for the documents, forms and pickers | nothing | [References and search](/superschematic/reference/references-and-search/) |
| Errors with the right status | nothing | `NotFoundError`, `BadRequestError`, `ConflictError` and the rest | returning one | [API routes](/superschematic/guides/api-routes/#errors) |
| Rate, body and time limits | `@rateLimit`, `@bodyLimit`, `@timeout` | the limits, in every server | nothing | [API routes](/superschematic/guides/api-routes/#traffic-controls) |
| A channel, credentials, a sandbox, a model or a file processor, chosen per environment | a provider package per service, listed in the API config's `providers`, and a slot for it with `@providers` on the API's `@envVars` class | the provider each environment's env names, checked against its interface's contract and its settings at build, and `providersFrom` in the TypeScript runtime | the provider package, or one from `examples/runtime-providers` | [Runtime providers](/superschematic/guides/runtime-providers/) |
| A rate limit every replica shares | `rateLimitStore: ShopCache` in the API's config, a MemoryStore it lists | the `@rateLimit` routes counted in the store, by httprate's sliding window, each replica on its own budget while the store is unavailable | nothing | [Memory stores](/superschematic/guides/memory-stores/#share-a-rate-limit) |
| A write a client may retry | `@idempotent({ ttl?, required? })` on the operation, and `class IdempotencyKey implements IdempotencyKeys {}` in the API's database | the table of keys and its migration, the route step that answers a repeat what the first request answered (Go, TypeScript and Rust servers), the header in OpenAPI, the key each SDK method sends (TypeScript, Go, Python, Rust) and the `idempotencyKey` argument of the MCP tool | nothing; an implementation that must not run twice in the window after a crash checks its own state | [API routes](/superschematic/guides/api-routes/#a-call-that-runs-once) |
| Turning away a client too old for the API | `clientVersions: true` in the API's config, and `client: { name, version }` in the SDK's | the SDK's `X-Client-Version` header, the optional setting `MIN_CLIENT_VERSIONS`, the 426 `client_too_old` in every server and a typed error in every SDK | the minimums, per environment | [Client SDKs](/superschematic/guides/client-sdks/#the-clients-name-and-version) |
| A webhook a third party calls | `@webhook` and `@hmacVerified({ provider })` | the signature check before anything else, and its typed input with the keys its type does not declare dropped | the handler, and the provider's verifier unless it signs with a JWT carrying the body's hash, as Plaid does, which every runtime checks built in | [API routes](/superschematic/guides/api-routes/#webhooks) |
| The server in another language | `outputs.api.language`: `GO`, `TYPESCRIPT` or `RUST` | the same routes and checks in that language | the implementation in it | [API routes](/superschematic/guides/api-routes/#the-typescript-server) |
| Settings and secrets for a server | a class with `@envVars`, fields `Secret<T>` where secret | a typed loader and `values-schema.json` | nothing | [Modeling types](/superschematic/guides/modeling-types/#environment-variables) |

## Users and access

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Routes that need a signed-in caller | `extends Authenticated` on the set, or `@auth` | the check, a 401 without a caller | nothing | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| Permissions | `@requirePermission(["orders.write"])` | the check, a 403 without one | nothing | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| Users, sessions, sign-in | a table that `implements User<{ login }>` and one that `implements UserRole`; an API whose `authDb` is that database, with a `@userSessions` class | the `Session` and credential tables, the sign-in, sign-out and password routes, roles and grants | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#turn-it-on) |
| User and role administration | `@userAdministration` | the routes that manage users, roles and grants | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#serve-the-session-routes) |
| Roles every database starts with | `@seedRoles([{ name, permissions }])` on the `UserRole` table | the rows in `create.sql`, and the plan steps that keep them in step | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#seed-the-roles) |
| Rows any table starts with: defaults, trust levels, lookup rows | `@seedRows<T>([{ field: value }], { key? })` on the table | the rows in `create.sql` on Postgres and SQLite, and a plan step for each new one; none ever overwrites a row an app edited | nothing | [Schema migrations](/superschematic/reference/migrations/#seeded-rows) |
| A program that calls as a user | `@userSessions({ apiKeys: true })` | the routes that make, list and revoke a user's named, revocable keys, each holding the user's permissions or a subset | nothing | [Users and sessions](/superschematic/guides/users-and-sessions/#api-keys) |
| Rows only some callers see, on every read and write | `@audience<T>({ owner, inheritFrom, agents })` on a table, on Postgres | the audience columns and the trigger that passes a parent's audience to its children; the TypeScript server's repositories, searches and relations keep to the caller's rows, and a row they do not see is a 404 | nothing | [API routes](/superschematic/guides/api-routes/#a-tables-audience) |
| A caller who is no user, reaching one row and what its role reaches from it, until a date | a class marked `@accessGrants()`, and `reaches` on the `UserRole` table's roles | the grants' table; `issueGrant`, `grantAuthenticator` and `revokeGrant` in the TypeScript runtime, which read the grant on every call | the call that issues each grant | [API routes](/superschematic/guides/api-routes/#grants) |
| A field only some may read, and a log of sensitive reads | `@decrypted({ permission })` on an `Encrypted<T>` field of a view; a class marked `@accessLog` | the reveal check, and a log row for each reveal, each call with an API key or a grant, and each read an agent run makes | `accessLog: accessLogOf(orm)` in the router's options | [API routes](/superschematic/guides/api-routes/#the-access-log) |
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
| A message for each row already there | `backfill: true` on `@enqueueOnChange`, or `stack queue backfill` | the inserts in the migration step that adds the trigger, or in one run of the command | nothing | [Queues and workers](/superschematic/guides/queues/#backfill) |
| A field a model fills, and the loop that fills it | `implements Derived<T, { fields, permission }>` on a DB table, and `@deriver({ schemas })` on a class of a TypeScript, Rust or Go API | the stale, derive, settle loop as a deployable the stack runs, and a `Derivers` type with a method per derived field, typed by the field | each method: the call of your model | [Stacks](/superschematic/guides/stacks/#derivers) |

## Events, outside parties and agents

The core's runtime for these is TypeScript, over Postgres.

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Events a row raises, routed to people and sent on the channels each prefers | `@event<T>({ kind, title, urgency })` on a table, and the notifier's tables: empty classes marked `@notifier(...)` and `@channelIdentities()` | the notifier's tables and queue, which the build fills; in the runtime, `raise` in the caller's transaction, routes as rows, quiet hours, digests, escalation, and replies by text or email run as their sender | the routes' rows, and the channel adapters the config names | [Events and notifications](/superschematic/guides/notifications/) |
| People outside the user table, and every inbound text and email down one path | `@party<T>` on a table, `@conversations(...)` classes and a `@runRequests()` queue | the conversations' tables and the queue, which the build fills; in the runtime, an inbound path that resolves the sender, keeps messages as rows with their files, holds unknown senders in triage, and sends to a party only as a permission | the relay's webhook route, which hands each request to the path | [Outside parties](/superschematic/guides/parties/) |
| Agents as rows, each run in a fresh sandbox with a token its task bounds | `@agents("<part>")` on the orchestration's tables; `@autonomy(class)` on an operation | the orchestration's tables, which the build fills; in the runtime, the dispatcher, each run's grant, approvals bound to their parameters, budgets and stop switches | the agent definitions, the dispatcher's wiring, and the sandbox and model providers | [Agent runs](/superschematic/guides/agent-runs/) |

## Files, front ends and clients

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| A private file bucket | a Bucket service, listed in an API config's `buckets` | the bucket in the API's `Deps`, signed upload and download URLs, a put that writes only when the name is free | which object to sign for | [Buckets](/superschematic/guides/buckets/#declare-a-bucket) |
| Files stored once, by their content | `contentAddressed: true` in the Bucket service's config | a content bucket in the API's `Deps`: `put` hashes a body and writes it under `sha256/<hex>` if absent, `has` and `missing` say which hashes it holds | which bytes to put | [Buckets](/superschematic/guides/buckets/#name-objects-by-their-content) |
| Wake-ups, counters and claims every replica shares | a MemoryStore service whose schema declares each `@channel` and `@key`, listed in an API config's `memoryStores` | typed definitions of each channel and key, the store in the API's `Deps` on Redis or Valkey, Valkey under `stack dev` and Memorystore on gcp | the publish, the subscribe and the claim, and the path for when the store is unavailable | [Memory stores](/superschematic/guides/memory-stores/#declare-a-store) |
| A static front end | a Site service | the config, served in each environment, that holds each API's address, and CORS on each API it calls | the front end | [Static sites](/superschematic/guides/static-sites/#declare-a-site) |
| A front end and its APIs on one origin, for a cookie session with no CORS | `origins: [{ site, apis }]` in `@stack` | each API under `/api/<service>` on the site's address, in `stack dev`, on gcp's load balancer and through the site's Worker on Cloudflare, and the config that names it there | nothing | [Static sites](/superschematic/guides/static-sites/#one-origin) |
| Clients in Go, TypeScript, Python, Rust | a line per language in `outputs.sdk` | an SDK with the server's checks, pages, errors and retries | nothing | [Client SDKs](/superschematic/guides/client-sdks/#generate-them) |
| Tools an AI agent can call | `@mcp` on operations | MCP tool documents beside the SDKs, and with `outputs.api.mcp` a TypeScript, a Rust or a Go API's endpoint at `POST /api/mcp`, which runs an `invocationPolicy: "ask"` tool only once the person confirms the call | nothing; with an `ask` tool, the secret `outputs.api.mcpConfirmationKey` names, set per environment | [MCP tools](/superschematic/reference/mcp-tools/) |
| Labels and controls for forms | `@docs`, `@purpose`, `@display`, `@uiControl` | the metadata a schema form reads | nothing | [Documentation](/superschematic/reference/documentation/) |

## Running and deploying

| You need | Declare | The build generates | You write | Guide |
| --- | --- | --- | --- | --- |
| Everything running on your machine | a Stack service, which `stack init` writes | server entrypoints; `stack dev` runs Postgres in Docker, applies the migrations and serves | nothing | [Stacks](/superschematic/guides/stacks/#start-a-stack) |
| Environments: dev, staging, production | `@environment` classes in the Stack schema | one resolved `environment.json` each | the values | [Stacks](/superschematic/guides/stacks/#environments) |
| Google Cloud or Cloudflare | an environment with the `gcp` or `cloudflare` target | `stack plan` and `stack deploy`, which build what changed, deploy it and run the migrations between steps | nothing | [Stacks](/superschematic/guides/stacks/#deploy-to-google-cloud) |
| Postgres, pgvector, sign-in and queue workers on Cloudflare | a DB service that lists `postgres` first, and in its environment's settings the id of a Hyperdrive config | Workers whose ORM and identity runtime reach the database through Hyperdrive, a Worker per queue worker that drains its queue at each firing of a Cron Trigger, and the deploy's migration of the database | the Hyperdrive config, once per environment | [Stacks](/superschematic/guides/stacks/#databases-on-hyperdrive) |
| One binary a person installs | an environment with the `embedded` target, its servers Rust and its databases `sqlite` first | `stack build <environment>`: every server and worker in one process, each database a SQLite file the binary migrates as it starts from the plans its releases recorded, and the site's files inside it | nothing | [Stacks](/superschematic/guides/stacks/#build-one-binary) |
| CI that deploys | `outputs.ci` on the Stack service | the workflow | nothing | [Stacks](/superschematic/guides/stacks/#generated-ci) |
| Traces | nothing in a schema: OpenTelemetry's own `OTEL_` variables | spans from every server, job and worker | nothing | [Traces](/superschematic/guides/traces/) |
| The checks of a commit, each run once | a Tasks service: a class per task with `@task`, its command, the tracked files it reads, the outputs of other tasks it uses and the tasks it follows | the task file a forge reads at a commit, held current by `tasks --check`, and with `tasks plan` an input key per task over git's object ids, so a pass a forge trusts is not run twice | the forge that runs the plan, and its own section of the file | [Tasks](/superschematic/guides/tasks/) |

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
| Selling counted stock and booking time, with holds, payments and refunds | the `@superschematic/engine-commerce` behaviors: `Stock`, `Calendar`, `Holds`, `Payments` | [Selling and booking](/superschematic/guides/commerce/) |

The same schemas also run with nothing generated in two more modes. In
[development mode](/superschematic/guides/development-mode/) a project's
rows are JSON in fixed tables, in a SQLite file or in memory, so a
publish takes effect at once and may rename, retype or remove. In
[runtime mode](/superschematic/guides/runtime-mode/) each type has the
table a build writes for it, and a publish plans the migration and
applies it while the environment runs. Its tables are the ones a build
writes, so a project moves on from runtime mode to a build with no data
migration.

## Your own conventions

A pattern that the core does not have becomes an extension: a Go package
that adds kinds, decorators, generators, auth providers, behaviors,
deploy targets or commands to a binary of your own. See
[Write an extension](/superschematic/extending/write-an-extension/).
