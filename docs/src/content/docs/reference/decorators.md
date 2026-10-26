---
title: Decorators and wrappers
description: The decorators and type wrappers of the core authoring packages, where each may appear, and the page that covers it.
sidebar:
  order: 0
---

A schema file imports decorators and type wrappers from the core authoring
packages. The package decides which kinds of schema may use them:
`@superschematic/schema` everywhere, `@superschematic/db` in DB schemas,
`@superschematic/api` in API schemas, `@superschematic/stack` in Stack
schemas and `@superschematic/memory-store` in MemoryStore schemas. An extension adds its own from its own package. This page lists the ones these docs cover; the others join it
as their support lands in every generator.

## Types and fields: `@superschematic/schema`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `Nullable<T>` | field | the field may be absent or null; `name?: T` means the same | [Modeling types](/superschematic/guides/modeling-types/#required-nullable-and-optional) |
| `Default<T, V>` | field | the decoders fill in `V` when a payload leaves the field out; on a required field of a DB table whose column holds text, a number, a boolean or an enum, it is the column's `DEFAULT` too | [Modeling types](/superschematic/guides/modeling-types/#defaults) |
| `Validate<T, C>` | field | `min`, `max`, `minLength`, `maxLength`, `pattern`, `listMin`, `listMax`, `uploadMaxBytes`, `precision` and `scale` (a `Generic.Decimal`'s digits; `NUMERIC(p,s)` on a DB table), `dimensions` (an `Embedding.Vector`'s length; pgvector's `vector(n)` on a DB table); on a DB table, `min` and `max` on a number column and `minLength` and `maxLength` on a text column are the column's `CHECK` too | [Modeling types](/superschematic/guides/modeling-types/#constraints) |
| `Secret<T>` | field | a value kept out of logs: mask helpers clear it, and an `@envVars` loader marks it secret. It does not encrypt the value; `Encrypted<T>` does | [Modeling types](/superschematic/guides/modeling-types/#secrets) |
| `@strictJSON` | class | every decoder of the type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@denyUnknownFields` | class | the Rust type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@allowUnknownFields` | class | every decoder of the type, and a server reading it as an input, accepts a key it does not declare and drops it; a `@webhook`'s input reads this way unless its type is `@strictJSON` | [Modeling types](/superschematic/guides/modeling-types/#lenient-decoding) |
| `@jsonField` | class | the type is stored as `JSONB` inside the row that holds it, not as a table | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| `@source(Table)` | class (API, General) | the class is a view of a table; its fields are checked against the table's | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@virtual` | field of a `@source` view | a field with no column behind it, filled in by the implementation | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@decrypted` | field of a `@source` view | the field reads an `Encrypted<T>` field of its table, and every response the view is in carries the value; a field that reads one without it fails the load | [Database tables](/superschematic/guides/database-tables/#encrypted-fields) |
| `@docs`, `@purpose`, `@icon` | field | presentation for a settings or form UI | [Documentation](/superschematic/reference/documentation/) |
| `@uiControl(control)` | field | the control a schema form draws for the field in place of the one it picks | [Documentation](/superschematic/reference/documentation/#uicontrol-on-a-field) |
| `@display({ multiline })` | field | whether a UI gives the field's text a control of several lines or one, whatever its `maxLength` | [Documentation](/superschematic/reference/documentation/#display-multiline--on-a-field) |
| `@display({ currency })` | field | the currency of the field's amount in minor units: an ISO 4217 code, or another field of the type that holds one, which a UI shows it as money in | [Documentation](/superschematic/reference/documentation/#display-currency--on-a-field) |
| `/** @title … */` | enum member | the member's title, the label a UI shows for it (a JSDoc tag, since an enum member takes no decorator): the first word of a line of the member's one `/** */` comment, the title the rest of that line; one line, no ASCII control character, no space at either end and no `*/` | [Documentation](/superschematic/reference/documentation/#a-title-on-an-enum-member) |
| `@display({ noun, plural, titleField, createLabel, icon, summaryFields, states, transitions })` | class | how a UI shows the type's instances: what to call them, the title and summary fields, the create button, the icon beside its name (an icon name: a letter or a digit, then letters, digits, `.`, `_`, `:`, `/` and `-`), and labels and tones for its `Workflow`'s states and transitions | [Documentation](/superschematic/reference/documentation/#display-on-a-type) |
| `@behavior(name, config?)` | class | composes an engine behavior (`Workflow`, `Links`, `Queue`, ...) on a type the engine runs; `BehaviorConfigs` types the config | [Engine behaviors](/superschematic/guides/engine-behaviors/#compose-a-behavior) |

## Tables: `@superschematic/db`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@key` | field | the primary key; a unique field in the engine | [Database tables](/superschematic/guides/database-tables/#tables-and-keys), [Engine](/superschematic/guides/engine/#unique-fields-and-lookups) |
| `AutoGenerate<T>` | field | the database generates the value on insert | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| `@unique` | field | a unique constraint on the column; in the engine, on the field within a namespace, which `lookup` reads by | [Database tables](/superschematic/guides/database-tables/#tables-and-keys), [Engine](/superschematic/guides/engine/#unique-fields-and-lookups) |
| `Relation<T, { onDelete }>` | field | a foreign key to table `T`; `onDelete` is `CASCADE` (the default), `RESTRICT`, `NO ACTION`, or `SET NULL` on a `Nullable<Relation<T, ...>>` | [Database tables](/superschematic/guides/database-tables/#relations) |
| `HasMany<T>` | field | the rows of `T` whose relation points at this row | [Database tables](/superschematic/guides/database-tables/#one-to-many) |
| `@index<T>(keys, { unique?, name?, method?, ops?, where?, expression?, concurrently?, m?, efConstruction?, lists? })` | class | an index over the listed fields, in the engine too; on a table, of the method, operator class, predicate or expression given, built inside a plan's step with `concurrently: false`; `hnsw` and `ivfflat` index one vector column, `ops` naming the metric (`vector_cosine_ops`, `vector_l2_ops`, `vector_ip_ops`), with pgvector's `m` and `efConstruction` (hnsw) or `lists` (ivfflat) | [Database tables](/superschematic/guides/database-tables/#indexes), [Engine](/superschematic/guides/engine/#unique-fields-and-lookups) |
| `@rawSql({ id, up, down?, after?, dialects? })` | class (a table) | raw SQL the service runs after its tables or after every object it declares; `create.sql` includes it, and a migration plan runs it when it is new or changes and its `down` when it is gone | [Database tables](/superschematic/guides/database-tables/#raw-sql), [Schema migrations](/superschematic/reference/migrations/#raw-sql) |
| `@searchField` | field | joins the field into a trigram-indexed `search_text` column | [Database tables](/superschematic/guides/database-tables/#text-search) |
| `@jsonField`, `JsonField<T>` | field | stores the field as `JSONB` | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| `Encrypted<string>` | field | the column holds the value sealed with AES-256-GCM under the keys of the `@envVars` secret `outputs.sql.encryption.key` names, bound to its table, field and row; the ORMs seal and open it, and it is a `Secret<T>` too. It takes no key, unique, index, search, default or filter. Not the API's `Encrypted` payloads | [Database tables](/superschematic/guides/database-tables/#encrypted-fields) |
| `@table(name)` | class | names the table, a queue's table among them, in place of the class's name in snake case; every name derived from it follows | [Database tables](/superschematic/guides/database-tables/#table-and-column-names) |
| `@column(name)` | field of a table or a queue | names the column, a to-one relation's foreign key among them, in place of the field's name in snake case; on a projection's field, `@column("alias.field")` is the column it reads | [Database tables](/superschematic/guides/database-tables/#table-and-column-names) |
| `@sourceMustProject` | field | warns when a `@source` view leaves the field out | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@versioned`, `@optimistic` | class | keep every version of a row, or only check versions on write | [Versioned tables](/superschematic/reference/versioned-tables/) |
| `@versionGraph`, `@graphMember`, `@conflictUnit` | class, field | branch, commit and merge a tree of versioned tables | [Version graphs](/superschematic/reference/version-graphs/) |
| `@projection`, `@join`, `@column` | class, field | a read-only SQL view over tables | [Projections](/superschematic/reference/projections/) |
| `implements User<{ login, name? }>` | class | the table whose rows are the project's users; the build adds `Session` and `UserCredential` beside it | [Users and sessions](/superschematic/guides/users-and-sessions/#turn-it-on) |
| `implements UserRole` | class | the table whose rows are roles; the build adds `UserRoleGrant` | [Users and sessions](/superschematic/guides/users-and-sessions/#turn-it-on) |
| `@queue({ retries?, backoff?, lease?, key?, onDuplicate?, serialize? })` | class | a queue of the database whose message is the class, in a table of its own, `<class>_queue` unless `@table` names it: a message whose handler fails is handled again after `backoff` (30s), `retries` times (5), and a claim holds a message for `lease` (60s). `key` names the message's field that keys it: `onDuplicate` `"replace"` or `"skip"` keeps one message waiting per key, and `serialize` one claim per key. The class stays a type; the Go ORM gets `Enqueue<Class>(ctx, tx, msg)` and the queue's claims | [Queues and workers](/superschematic/guides/queues/) |
| `@enqueueOnChange(Queue, { on, fields?, when?, message })` | class (table) | a trigger, on Postgres and SQLite, that enqueues a message of `Queue` after an insert, update or delete of the table's rows, whatever writes them: an update only when one of `fields` changes, and a row only when it meets `when`; `message` maps the message's fields to the table's | [Queues and workers](/superschematic/guides/queues/#enqueue-on-a-change) |

## Routes: `@superschematic/api`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@rest(method, path)` | method | the route's HTTP method and path, under `/api` | [API routes](/superschematic/guides/api-routes/#operation-sets) |
| `QueryParam<T>` | argument | read the argument from the query string | [API routes](/superschematic/guides/api-routes/#arguments) |
| `RefersTo<T, Target, { options?, value?, label? }>` | field, argument | the value names an item of `Target`, as the operation that lists it does: the API's own, or an API dependency's when none of the API's lists it ([Options from an API dependency](/superschematic/reference/references-and-search/#options-from-an-api-dependency)); `value` is the target's field it holds (`id`), `label` the one that names an item (its `titleField`) | [References and search](/superschematic/reference/references-and-search/#refersto-a-value-that-names-an-item) |
| `SearchQuery<T>` | argument | the operation's search text: an optional query parameter of a `GET`, one per operation | [References and search](/superschematic/reference/references-and-search/#searchquery-a-lists-search-text) |
| `SortBy<Item, Fields>` | argument | the order of a `@paginated` list: an optional query parameter whose value is a field `Fields` names, ascending, or after a `-`, descending (`sort: SortBy<OrderView, ["placedAt", "totalCents"]>` takes `placedAt`, `-placedAt`, `totalCents` and `-totalCents`). Ties break by the item's `id` in the same direction. Each field is one required text, number or time, and neither `Nullable` nor `Secret`; a text field without a `maxLength` is a warning. The argument is not also a `SearchQuery` or a `RefersTo`, and carries no `Validate` rule. One per operation, which needs `defaultSort`. The implementation reads the sort in its page request, and a cursor holds the sort it was returned under | [API routes](/superschematic/guides/api-routes/#pages) |
| `FileUpload<{ maxBytes?, contentTypes?, extensions? }>` | field | the field, a string of an operation's input type, is a file: the request is `multipart/form-data`, the file a part named as the field, at most `maxBytes` (100 MiB when left out), of one of `contentTypes` (media types without parameters) and with a filename ending in one of `extensions` (with the dot, in any case); each one left out admits any. `Nullable<FileUpload>` makes it optional. The input is the single input of a `POST`, `PUT` or `PATCH`, not encrypted, and no other type or result holds it. The implementation receives the files in `args.files` (Go: `files`) | [API routes](/superschematic/guides/api-routes/#uploads) |
| `FileDownload<{ contentType, filename? }>` | result | the operation answers a file of `contentType`, not the JSON envelope, with `Content-Disposition` offering the implementation's filename, or `filename` when it names none. The implementation returns `{ body, filename? }` (Go: `*FileDownload`); never null, encrypted or `@paginated`, and its `@mcp`, when it has one, is hidden | [API routes](/superschematic/guides/api-routes/#downloads) |
| `extends Authenticated` | class | every route of the set needs a caller | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `extends Encrypted`, `@encrypted` | class, method | the route's request body travels as an encrypted envelope | [API routes](/superschematic/guides/api-routes/#encrypted-payloads) |
| `EncryptedField<T>` | argument, result | the operation's request body travels as an encrypted envelope | [API routes](/superschematic/guides/api-routes/#encrypted-payloads) |
| `@auth` | method | the route needs a caller | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@requirePermission([...])` | method | the route needs a caller holding one of the permissions | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@requireOwnership` | method | the route needs a caller; your implementation checks ownership | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@publicRoute` | method | marks a route anyone may call, in an `Authenticated` set too; refused with `@auth`, `@requirePermission` or `@requireOwnership` | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@webhook` | method | an operation a third party calls; no SDK has a method or a tool for it, and its input drops the keys its type does not declare unless the type is `@strictJSON` | [API routes](/superschematic/guides/api-routes/#webhooks) |
| `@hmacVerified({ provider })` | method | every server checks the request's signature with the provider's verifier first, before the rate limit, the body limit and the permission check. Each runtime has a built-in verifier for a JWT that carries the body's SHA-256, as Plaid sends | [API routes](/superschematic/guides/api-routes/#webhooks), [a JWT that carries the body's hash](/superschematic/guides/api-routes/#a-jwt-that-carries-the-bodys-hash) |
| `@requireService({ from? })` | class, method | only a listed service may call: the server of an API whose handle `from` lists, or without `from` any server with an edge to this API; with a user clause, it must forward an end user who meets it. A method's own replaces its class's, and an `@publicRoute` method takes none. No tool lists the operation, so its `@mcp` must be hidden | [Service callers](/superschematic/guides/auth-and-permissions/#service-callers) |
| `@allowService({ from? })` | class, method | needs a user clause: an end user who meets it, or a listed service with no end user. Neither decorator goes with `@publicRoute`, `@webhook` or `@hmacVerified` | [Service callers](/superschematic/guides/auth-and-permissions/#service-callers) |
| `@rateLimit`, `@bodyLimit`, `@timeout` | class, method | bound a route's requests per minute, body size and duration | [API routes](/superschematic/guides/api-routes/#traffic-controls) |
| `@userSessions({ path?, login?, register?, registration?, defaultRoles?, firstUser? })` | class with no methods | the routes users sign in and out with, which the identity runtime serves; `registration: "approval"`, `defaultRoles` and `firstUser` shape `register` (D81) | [Users and sessions](/superschematic/guides/users-and-sessions/#approve-new-users) |
| `@userAdministration({ path? })` | class with no methods | the routes that manage users, roles and grants, `approveUser` among them | [Users and sessions](/superschematic/guides/users-and-sessions/#serve-the-session-routes) |
| `@manualRouteRegistration` | method | each router gates the route and hands it to your handler: TypeScript's from `manualRoutes`, Go's from `Config.ManualRoutes`, Rust's from `build_router_with_manual_routes`; a stack's server takes the handlers from the implementation | [Go](/superschematic/install/go/#serve-a-generated-api), [TypeScript](/superschematic/install/typescript/#serve-a-generated-api), [Rust](/superschematic/install/rust/#serve-a-generated-api) |
| `@paginated({ defaultLimit?, maxLimit?, defaultSort? })` | method | a GET returning a list pages by an opaque cursor: the route takes `cursor` and `limit`, 1 to `maxLimit` (100 when left out), `defaultLimit` when absent (20, or `maxLimit` when that is less), and answers `{items, next}`, `next` null after the last page. `defaultSort` is the order of a request that sends no sort, one of the `SortBy` argument's values: required with a `SortBy` argument, and refused without one | [API routes](/superschematic/guides/api-routes/#pages) |
| `@docs`, `@icon` | method | the operation's documentation and icon | [Documentation](/superschematic/reference/documentation/) |
| `@mcp` | method | publishes the operation as an MCP tool, or says why not; a TypeScript API with `outputs.api.mcp` serves its visible tools at `POST /api/mcp` | [MCP tools](/superschematic/reference/mcp-tools/) |
| `@job({ schedule?, timeZone?, timeout?, retries? })` | class with no fields | a job of the API, named after the class: a run to completion with the API's `Deps`, on a five-field cron schedule in its IANA time zone or on demand, bounded by `timeout` and run again `retries` times when it fails. The class is no type; the API's Go package gets `Jobs` and `JobsConstructor`, and a stack runs the job as a deployable of its own | [Stacks](/superschematic/guides/stacks/#jobs) |
| `@worker({ queue, concurrency?, grace? })` | class with no fields | a worker of the API, named after the class: it claims the messages of `queue`, a `@queue` class of the API's database, `concurrency` at a time (1), and handles each with the API's `Deps`; a stopping worker lets its handlers finish for `grace` (8s). The class is no type; the API's Go package gets `Workers` and `WorkersConstructor`, and a stack runs the worker as a deployable of its own | [Queues and workers](/superschematic/guides/queues/) |
| `@app({ extensions? })` | class with no fields | an app of a Rust API, named after the class: your own HTTP program over the API's `Deps`, implementations and engine, a library crate at `[implementation_paths] rust_app` exporting `serve(parts)`, which a stack runs as a deployable of its own beside the API's servers. `extensions` names the extensions its build needs (`["topcoat"]`); a binary that does not link one plans the stack and refuses to build it. The class is no type; the API crate gets `apps::<Class>Parts` | [Stacks](/superschematic/guides/stacks/#apps), [Pages with Topcoat](/superschematic/guides/topcoat/#run-the-app-in-a-stack) |

## Configs: `@superschematic/schema-config`

| Name | What it does | Covered in |
| --- | --- | --- |
| `defineConfig({...})` | a service's name, kind, `public`, `authDb`, `dependencies`, `calls` and `outputs`; an API's `buckets`, the Bucket handles it keeps objects in, `memoryStores`, the MemoryStore handles its replicas share, and `rateLimitStore`, the one MemoryStore handle its `@rateLimit` routes count in, which lists it too; a MemoryStore's `outputs.types`, the languages of its definitions; a Stack service's `outputs.ci` asks for its generated CI | [How it works](/superschematic/start/how-it-works/), [Buckets](/superschematic/guides/buckets/#declare-a-bucket), [Memory stores](/superschematic/guides/memory-stores/#list-it-in-an-api), [Stacks](/superschematic/guides/stacks/#generated-ci) |
| `service({ name, kind })` | a handle to another service, for `authDb`, `dependencies` and `calls`, and for a decorator argument that names a service, such as the `from` of `@requireService` and `@allowService`; its type carries the kind (`ServiceHandle<"API">`). Each service's build writes its handle to `src/service.generated.ts`, which a config or a schema file imports from the service's package; an API's also carries its `@envVars` class and the names of its `@job` and `@worker` classes | [How it works](/superschematic/start/how-it-works/#services-depend-on-each-other) |
| `@envVars` | on a class of a General or API schema: its fields are the service's environment variables, with a generated loader and `values-schema.json`. On an API, it is the config of the API's server, and a stack's `env` binds its fields | [Modeling types](/superschematic/guides/modeling-types/#environment-variables), [Stacks](/superschematic/guides/stacks/#wire-the-services) |

## Stacks: `@superschematic/stack`

A Stack service (`kind: SchemaKind.Stack`) declares what runs where over
the services it names by their handles. Its build writes each
environment, resolved, to `stack/<service>/<environment>/environment.json`,
each Go server's entrypoint and Dockerfile to
`server/<service>/<server>/`, and with `outputs.ci` its CI workflow to
`ci/<service>/<renderer>/`. The stack takes its service's name. Each
class of its schema carries one of these decorators and no fields.

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@stack({ deploy, expose })` | class | the stack's entry points, API and DB handles: every service they reach through `authDb`, DB dependencies and `calls` joins the stack. `expose` names what is reachable from outside, an API's handle, an `@server` class, or an app beside its API's handle, `{ of: ShopStock, app: "Backoffice" }`. One class per schema | [Stacks](/superschematic/guides/stacks/#declare-a-stack) |
| `@server({ serves })` | class | one server for the APIs listed, in place of their default servers | [Stacks](/superschematic/guides/stacks/#declare-a-stack) |
| `@database({ hosts })` | class | one database for the DB schemas listed, in place of their default databases | [Stacks](/superschematic/guides/stacks/#declare-a-stack) |
| `@environment({ target, domain, dns, settings, parameters })` | class | an environment: the target, its values under the target's name (`local: { postgresImage, postgresPort, storageImage, storagePort, memoryStoreImage, memoryStorePort }`, `postgresImage` pgvector's image unless set when a DB service has a vector column, `gcp: { project, region, production }`), the domain and its DNS platform, and settings per deployable, each `of` a handle, a Bucket's or a MemoryStore's among them, or an `@server` or `@database` class, with platform settings (a local server's `port`, a Cloud Run server's `minInstances`) and `env` values that are literals or `{ parameter }`. A job's element names it beside its API's handle, `{ of: ShopOrders, job: "ShipOrders" }`, and changes its `schedule`, `timeZone` or `enabled`; a worker's, `{ of: ShopOrders, worker: "FulfilOrders" }`, its `instances`, `concurrency` or `enabled`; an engine's runner's, `{ of: ShopDb, runner: true }`, its `enabled`; an app's, `{ of: ShopStock, app: "Backoffice" }`, its `enabled` and its platform's settings; a memory store's, `{ of: ShopCache, enabled: false }`, whether it runs. A class that extends another `@environment` class inherits its values | [Stacks](/superschematic/guides/stacks/#environments) |
| `Targets` | interface | the targets `target` may name, each with its values and a settings type per deployable kind, which `@environment` checks the values, each settings element and its `env` against. It lists the core's `local` (`LocalTarget`); a target's package, or your stack, augments it with another, such as `gcp` | [Stacks](/superschematic/guides/stacks/#type-a-targets-values) |

## Memory stores: `@superschematic/memory-store`

A MemoryStore schema declares each channel and key its store carries,
and nothing else (D75). The class is a declaration, not a type: no type
library emits it. Its fields, in order, are the parts of the
declaration's name, each a required string, integer, boolean, enum, or
superscalar scalar whose wire form is a string or an integer. A
declaration's name is its class's in kebab case unless `name` sets it,
one segment of lowercase letters, digits, `.`, `_` and `-`; `rate-limit`
is the core's.

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@channel({ payload?, name? })` | abstract class | a channel: a wake-up or a sample every replica can lose. `payload` is the object type of each message, from a dependency of the store's schema and never `@strictJSON`; without it the channel is a signal, whose name is the whole message. A channel takes no TTL | [Memory stores](/superschematic/guides/memory-stores/#declare-a-store) |
| `@key({ use, ttlSeconds \| maxTtlSeconds, name? })` | abstract class | a key, which always expires: `use` is `"counter"`, which counts, or `"claim"`, which lets one caller do a piece of work once. It sets exactly one of `ttlSeconds`, its TTL from the write that creates it, and `maxTtlSeconds`, the longest TTL a call gives it; both are 1 to 604800 seconds | [Memory stores](/superschematic/guides/memory-stores/#declare-a-store) |
