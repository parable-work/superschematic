---
title: Decorators and wrappers
description: The decorators and type wrappers of the core authoring packages, where each may appear, and the page that covers it.
sidebar:
  order: 0
---

A schema file imports decorators and type wrappers from the core authoring
packages. The package decides which kinds of schema may use them:
`@superschematic/schema` everywhere, `@superschematic/db` in DB schemas,
`@superschematic/api` in API schemas and `@superschematic/stack` in Stack
schemas. An extension adds its own from its own package. This page lists the ones these docs cover; the others join it
as their support lands in every generator.

## Types and fields: `@superschematic/schema`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `Nullable<T>` | field | the field may be absent or null; `name?: T` means the same | [Modeling types](/superschematic/guides/modeling-types/#required-nullable-and-optional) |
| `Default<T, V>` | field | the decoders fill in `V` when a payload leaves the field out | [Modeling types](/superschematic/guides/modeling-types/#defaults) |
| `Validate<T, C>` | field | `min`, `max`, `minLength`, `maxLength`, `pattern`, `listMin`, `listMax`, `uploadMaxBytes` | [Modeling types](/superschematic/guides/modeling-types/#constraints) |
| `Secret<T>` | field | a value kept out of logs: mask helpers clear it, and an `@envVars` loader marks it secret | [Modeling types](/superschematic/guides/modeling-types/#secrets) |
| `@strictJSON` | class | every decoder of the type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@denyUnknownFields` | class | the Rust type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@jsonField` | class | the type is stored as `JSONB` inside the row that holds it, not as a table | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| `@source(Table)` | class (API, General) | the class is a view of a table; its fields are checked against the table's | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@virtual` | field of a `@source` view | a field with no column behind it, filled in by the implementation | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@docs`, `@purpose`, `@icon` | field | presentation for a settings or form UI | [Documentation](/superschematic/reference/documentation/) |
| `@behavior(name, config?)` | class | composes an engine behavior (`Workflow`, `Links`, `Queue`, ...) on a type the engine runs; `BehaviorConfigs` types the config | [Engine behaviors](/superschematic/guides/engine-behaviors/#compose-a-behavior) |

## Tables: `@superschematic/db`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@key` | field | the primary key | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| `AutoGenerate<T>` | field | the database generates the value on insert | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| `@unique` | field | a unique constraint on the column | [Database tables](/superschematic/guides/database-tables/#tables-and-keys) |
| `Relation<T, { onDelete }>` | field | a foreign key to table `T`; `onDelete` is `CASCADE` (the default), `RESTRICT` or `NO ACTION` | [Database tables](/superschematic/guides/database-tables/#relations) |
| `HasMany<T>` | field | the rows of `T` whose relation points at this row | [Database tables](/superschematic/guides/database-tables/#one-to-many) |
| `@index<T>(keys, { unique?, name? })` | class | an index over the listed fields | [Database tables](/superschematic/guides/database-tables/#indexes) |
| `@searchField` | field | joins the field into a trigram-indexed `search_text` column | [Database tables](/superschematic/guides/database-tables/#text-search) |
| `@jsonField`, `JsonField<T>` | field | stores the field as `JSONB` | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| `@sourceMustProject` | field | warns when a `@source` view leaves the field out | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@versioned`, `@optimistic` | class | keep every version of a row, or only check versions on write | [Versioned tables](/superschematic/reference/versioned-tables/) |
| `@versionGraph`, `@graphMember`, `@conflictUnit` | class, field | branch, commit and merge a tree of versioned tables | [Version graphs](/superschematic/reference/version-graphs/) |
| `@projection`, `@join`, `@column` | class, field | a read-only SQL view over tables | [Projections](/superschematic/reference/projections/) |

## Routes: `@superschematic/api`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@rest(method, path)` | method | the route's HTTP method and path, under `/api` | [API routes](/superschematic/guides/api-routes/#operation-sets) |
| `QueryParam<T>` | argument | read the argument from the query string | [API routes](/superschematic/guides/api-routes/#arguments) |
| `extends Authenticated` | class | every route of the set needs a caller | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `extends Encrypted`, `@encrypted` | class, method | the route's request body travels as an encrypted envelope | [API routes](/superschematic/guides/api-routes/#encrypted-payloads) |
| `EncryptedField<T>` | argument, result | the operation's request body travels as an encrypted envelope | [API routes](/superschematic/guides/api-routes/#encrypted-payloads) |
| `@auth` | method | the route needs a caller | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@requirePermission([...])` | method | the route needs a caller holding one of the permissions | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@requireOwnership` | method | the route needs a caller; your implementation checks ownership | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@publicRoute` | method | marks a route anyone may call, in an `Authenticated` set too; refused with `@auth`, `@requirePermission` or `@requireOwnership` | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@webhook` | method | an operation a third party calls; the Go and TypeScript SDKs leave it out | [API routes](/superschematic/guides/api-routes/#webhooks) |
| `@hmacVerified({ provider })` | method | the Go server checks the request's signature with the provider's verifier | [API routes](/superschematic/guides/api-routes/#webhooks) |
| `@rateLimit`, `@bodyLimit`, `@timeout` | class, method | bound a route's requests per minute, body size and duration | [API routes](/superschematic/guides/api-routes/#traffic-controls) |
| `@manualRouteRegistration` | method | the Go and Rust routers leave the route for your service to mount; the TypeScript router gates it and hands it to your handler | [TypeScript](/superschematic/install/typescript/#serve-a-generated-api), [Rust](/superschematic/install/rust/#serve-a-generated-api) |
| `@docs`, `@icon` | method | the operation's documentation and icon | [Documentation](/superschematic/reference/documentation/) |
| `@mcp` | method | publishes the operation as an MCP tool, or says why not | [MCP tools](/superschematic/reference/mcp-tools/) |

## Configs: `@superschematic/schema-config`

| Name | What it does | Covered in |
| --- | --- | --- |
| `defineConfig({...})` | a service's name, kind, `public`, `authDb`, `dependencies`, `calls` and `outputs` | [How it works](/superschematic/start/how-it-works/) |
| `service({ name, kind })` | a handle to another service, for `authDb`, `dependencies` and `calls`, and for a decorator argument that names a service; its type carries the kind (`ServiceHandle<"API">`). Each service's build writes its handle to `src/service.generated.ts`, which a config or a schema file imports from the service's package; an API's also carries its `@envVars` class | [How it works](/superschematic/start/how-it-works/#services-depend-on-each-other) |
| `@envVars` | on a class of a General schema: its fields are the service's environment variables, with a generated loader and `values-schema.json` | [Modeling types](/superschematic/guides/modeling-types/#environment-variables) |

## Stacks: `@superschematic/stack`

A Stack service (`kind: SchemaKind.Stack`) declares what runs where over
the services it names by their handles, and its build writes each
environment, resolved, to `stack/<service>/<environment>/environment.json`.
The stack takes its service's name. Each class of its schema carries one
of these decorators and no fields.

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `@stack({ deploy, expose })` | class | the stack's entry points, API and DB handles: every service they reach through `authDb`, DB dependencies and `calls` joins the stack. `expose` names what is reachable from outside, an API's handle or an `@server` class. One class per schema | [The Stack kind](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md#41-the-stack-kind) |
| `@server({ serves })` | class | one server for the APIs listed, in place of their default servers | [The Stack kind](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md#41-the-stack-kind) |
| `@database({ hosts })` | class | one database for the DB schemas listed, in place of their default databases | [The Stack kind](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md#41-the-stack-kind) |
| `@environment({ target, domain, dns, settings, parameters })` | class | an environment: the target, its values under the target's name (`gcp: { project, region }`), and settings per deployable, each `of` a handle or an `@server` or `@database` class, with `env` values that are literals or `{ parameter }`. A class that extends another `@environment` class inherits its values | [The Stack kind](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md#41-the-stack-kind) |
| `Targets` | interface | a target's package augments it with the target's values and a settings type per deployable kind; `@environment` checks the values, each settings element and its `env` against them | [Typed authoring](https://github.com/parable-work/superschematic/blob/main/docs/stack-model.md#43-typed-authoring) |
