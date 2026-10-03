---
title: Decorators and wrappers
description: The decorators and type wrappers of the core authoring packages, where each may appear, and the page that covers it.
sidebar:
  order: 0
---

A schema file imports decorators and type wrappers from the core authoring
packages. The package decides which kinds of schema may use them:
`@superschematic/schema` everywhere, `@superschematic/db` in DB schemas and
`@superschematic/api` in API schemas. An extension adds its own from its
own package. This page lists the ones these docs cover; the others join it
as their support lands in every generator.

## Types and fields: `@superschematic/schema`

| Name | On | What it does | Covered in |
| --- | --- | --- | --- |
| `Nullable<T>` | field | the field may be absent or null; `name?: T` means the same | [Modeling types](/superschematic/guides/modeling-types/#required-nullable-and-optional) |
| `Default<T, V>` | field | the decoders fill in `V` when a payload leaves the field out | [Modeling types](/superschematic/guides/modeling-types/#defaults) |
| `Validate<T, C>` | field | `min`, `max`, `minLength`, `maxLength`, `pattern`, `listMin`, `listMax`, `uploadMaxBytes` | [Modeling types](/superschematic/guides/modeling-types/#constraints) |
| `@strictJSON` | class | every decoder of the type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@denyUnknownFields` | class | the Rust type refuses a key it does not declare | [Modeling types](/superschematic/guides/modeling-types/#strict-decoding) |
| `@jsonField` | class | the type is stored as `JSONB` inside the row that holds it, not as a table | [Database tables](/superschematic/guides/database-tables/#json-columns) |
| `@source(Table)` | class (API, General) | the class is a view of a table; its fields are checked against the table's | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@virtual` | field of a `@source` view | a field with no column behind it, filled in by the implementation | [API routes](/superschematic/guides/api-routes/#responses-and-views) |
| `@docs`, `@purpose`, `@icon` | field | presentation for a settings or form UI | [Documentation](/superschematic/reference/documentation/) |

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
| `@publicRoute` | method | marks a route anyone may call | [Auth and permissions](/superschematic/guides/auth-and-permissions/#what-a-route-requires) |
| `@rateLimit`, `@bodyLimit`, `@timeout` | class, method | bound a route's requests per minute, body size and duration | [API routes](/superschematic/guides/api-routes/#traffic-controls) |
| `@webhook`, `@hmacVerified({ provider })` | method | a route a third party calls; the provider's verifier checks its signature before every other step | [API routes](/superschematic/guides/api-routes/#webhooks) |
| `@manualRouteRegistration` | method | the Go and Rust routers leave the route for your service to mount; the TypeScript router gates it and hands it to your handler | [TypeScript](/superschematic/install/typescript/#serve-a-generated-api), [Rust](/superschematic/install/rust/#serve-a-generated-api) |
| `@docs`, `@icon` | method | the operation's documentation and icon | [Documentation](/superschematic/reference/documentation/) |
| `@mcp` | method | publishes the operation as an MCP tool, or says why not | [MCP tools](/superschematic/reference/mcp-tools/) |

## Configs: `@superschematic/schema-config`

| Name | What it does | Covered in |
| --- | --- | --- |
| `defineConfig({...})` | a service's name, kind, `public`, `authDb`, `dependencies` and `outputs` | [How it works](/superschematic/start/how-it-works/) |
| `service({ name, kind })` | a handle to another service, for `authDb` and `dependencies` | [How it works](/superschematic/start/how-it-works/#services-depend-on-each-other) |
