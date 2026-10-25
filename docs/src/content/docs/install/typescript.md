---
title: TypeScript
description: Install the CLI, write a schema, build it, and consume generated TypeScript types and the TypeScript SDK.
sidebar:
  order: 2
---

You author schemas in TypeScript (or JSON/YAML). Generated TypeScript types
and the TypeScript SDK are npm packages whose names come from `npm_scope`
in [superschematic.toml](/superschematic/reference/naming/).

## Requirements

- Node 22.12 or newer (CI uses the `NODE_VERSION` pin in `tools.env`).
- The [CLI](/superschematic/install/go/), built from this repository until
  the first tag.

The TypeScript frontend needs the authoring packages on the module path.
Until they are published, resolve them from the checkout (`packages/` and
the superscalar TypeScript binding). `examples/acme-schematic/schemas` shows
the `tsconfig` paths.

## Install

From `v0.1.0-alpha.1`:

```
npm install @superschematic/schema @superschematic/schema-config
npm install @superschematic/db      # DB schemas
npm install @superschematic/api     # API schemas
npm install @superschematic/stack   # Stack schemas
```

Pre-releases publish under the `next` dist-tag
(`npm install @superschematic/schema@next`). The CLI install is on the
[Go](/superschematic/install/go/) page.

## Write a schema

`schemas/services/catalog/schema.config.ts`:

```ts
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "catalog",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
```

`schemas/services/catalog/src/catalog.schema.ts`:

```ts
export abstract class Money {
  amountCents: number;
  currency: string;
}
```

A DB schema imports table helpers from `@superschematic/db`. An API schema
imports `@rest`, `Authenticated` and friends from `@superschematic/api`.
Decorators and type wrappers must resolve from an
[authoring package](/superschematic/reference/naming/);
any other import is a load error.

## Build

```
superschematic build schemas/services/catalog
```

TypeScript types land under `schemas/dist/types/typescript/catalog` as
`@schemas/catalog-types` (the default `npm_scope` is `@schemas`). Until you
publish the generated package, consume it from a Bun workspace that
contains it: the output root's workspace below, of which an
implementation at the
[`typescript` implementation path](/superschematic/reference/naming/#implementation_pathstypescript)
is a member. A `file:` specifier from an app outside a workspace does not
install a types package that has a `file:` or `workspace:` dependency of
its own.

`schemas/dist/package.json` is a private Bun workspace root
(`@schemas/workspace`) whose workspaces are every generated TypeScript
package (`types/typescript/*`, `sdk/typescript/*`, `api/*` and the
stacks' servers, `server/*/*`) and the TypeScript implementations, at the
naming file's `[implementation_paths]` `typescript` template under the
parent of the schemas root. A types package whose schema declares a
[version graph](/superschematic/reference/version-graphs/) depends on
`@superschematic/versiongraph`, with a `file:` path when
[`paths.versiongraph_typescript`](/superschematic/reference/naming/#pathsversiongraph_typescript)
is set and `*` otherwise. A types package depends on the scalar library
(`superscalar`) the same way, through
[`paths.scalar_typescript`](/superschematic/reference/naming/#pathsscalar_typescript),
and on another schema's types package with `workspace:*`. The root
depends on `superscalar` too, and where `[paths]` names a checkout of
`superscalar`, the version-graph runtime or the
[HTTP runtime](/superschematic/reference/naming/#pathshttp_runtime_typescript),
its `overrides` point every dependency on that package at the checkout
with a `file:` path from the root.

`@superschematic/versiongraph` is unpublished until the first tag, and
`superscalar` until superscalar's first release. Until then a `*`
dependency fails `bun install` with a 404, so set both paths to a
checkout. The install resolves the whole workspace, so one schema with a
version graph and no path breaks the install of every types package
beside it. The version-graph package loads from its `dist/`: build it in
the checkout first (`bun install && bun run build` in
`runtime/versiongraph/typescript`, which needs cargo with the
`wasm32-unknown-unknown` target). Its engine runs as `Engine`, whose
operations return promises, or `SyncEngine`, over a driver that blocks,
and keeps a graph in Postgres (`pg`) or SQLite (`node:sqlite` or
`bun:sqlite`); see
[The engine and its adapters](/superschematic/reference/version-graphs/#the-engine-and-its-adapters).

Run `bun install` in `schemas/dist` or in any generated package under
it, and again after each build. Every install writes the one `bun.lock`
at the root, and links each implementation's dependencies into its own
`node_modules`. An install inside an implementation does not find the
root, which sits in the output root, not above it, so add a dependency
to an implementation's `package.json` and install in `schemas/dist`.
Install with Bun: npm rejects the `workspace:` protocol.

The implementations, and the sites, are members of `schemas/dist`'s
workspace alone, the default output root's. An output root you build into
with `--out` lists, in their place, `impl/*`: the copies its stack's
build makes of the implementations its TypeScript servers, jobs and
workers build against, each under its package's name, with each
dependency on a path (`file:`, `link:`, `./`, `../`) re-based for the
copy's directory. So `bun install` there links the copies, and leaves
each implementation's `node_modules` linked to `schemas/dist`; edit the
implementation, not the copy, which the next build into that root brings
up to date. The copy leaves out `node_modules`, `.git`, a `bun.lock`
beside the package's `package.json`, and what the package's `.gitignore`
files leave out.

Commit `schemas/dist/bun.lock`, so every install of one commit takes the
same versions: a TypeScript server's image and the generated CI install
frozen to it. No build removes it, and the root `package.json` it pairs
with is the same on every build of the same schemas, so the lockfile is
the one file of `schemas/dist` to commit. Write it after building every
service (`build-all`), so it names every package. An output root that
holds only some services' packages, as a stack's build leaves it, leaves
out of the workspace each implementation or site that depends on a
package it lacks, and `stack dev` installs it with `--no-save`, which
keeps the lockfile as it is. Git cannot take back a
file under an ignored directory, so ignore the output root's contents
rather than the directory:

```gitignore
schemas/dist/*
!schemas/dist/bun.lock
```

Where a broader rule such as `dist/` ignores the directory, add `!/dist/`,
`/dist/*` and `!/dist/bun.lock` to `schemas/.gitignore`. The build of a
stack with a TypeScript server names the rule that hides the lockfile in
one line, and never edits your ignore files.

## Consume generated types

```ts
import { parseMoneyJson, type Money } from "@schemas/catalog-types";

const money: Money = parseMoneyJson(`{"amountCents":199,"currency":"USD"}`);
// throws when the payload is rejected

import { parseMoneyJsonNonStrict } from "@schemas/catalog-types";
const loose = parseMoneyJsonNonStrict(payload);
```

Every object type gets `parse<Type>Json`, `parse<Type>JsonNonStrict`,
`parse<Type>Yaml` and `parse<Type>YamlNonStrict`. Each runs
`validate<Type>`, which rejects a field value of the wrong JSON type, such
as `"5"` in a `number` field, with `type` at the field's path. A
`Generic.JSON` field takes any JSON value but null: a null or missing
required one is `required`. An optional one also takes null, and the
parsers keep it apart from an absent one, as `null` and `undefined`
([null in an optional Generic.JSON](/superschematic/reference/json-scalars/#null-in-an-optional-genericjson)). A `Generic.StringMap` field takes a JSON
object and an `Embedding.Vector` field a JSON array, or the value's JSON
text; any other JSON type is `type`
([JSON-valued scalars](/superschematic/reference/json-scalars/)). Type-only imports (no runtime) come from
`@schemas/catalog-types/types`.

Every object type and every union also gets `parse<Name>FromJSON`, which
reads a value as it arrives on the wire into its runtime types: a
date-time field becomes a `Date`, a nested object or union field runs its
own parser, and a `@strictJSON` type runs `parse<Type>Json`'s checks. A
union's parser picks the member every language picks, by one structural
predicate the generator writes for Go, TypeScript, Rust and Python alike.
When every member marks the same field `@internalMetadata`, that field's
value names the only candidate. Otherwise the candidates are the members
whose fields include every payload key and whose tags the payload does
not contradict, a tag being a field that two or more members declare with
distinct string or enum defaults; when none of them reads the payload,
the members whose tags the payload allows. The first candidate that reads
the payload is the member. A member reads it when the payload is an object, a `@strictJSON` member's holds no key the
member does not declare, each required field without a default is present
and not null, and each field present has its JSON kind at every list level
and in every map value, none of them null: an enum's value is one of its
values, a scalar's passes the scalar's validator (a UUID, a date, a
date-time), a nested object is one its type reads and an inner union one
some member reads. A field's own rules (a `min`, a `pattern`) take no part: a member
that breaks one is picked, and its parser refuses it with `validation
failed`. A value that is not an object, or that no member reads (an
unknown discriminator included), throws `ParseError` with `does not match
the declared type`, the reason the Go and Rust decoders give, and a
message that says which; `null` and `undefined` are returned as they
are.

A union also gets `validate<Union>`, which validates the member its parser
picks by the member's own validator, with the member's defaults filled in,
so the errors sit at the member's paths (`number`, not `card.number`). A
value no member reads, or that is not an object, validates: it is the
parser's to refuse, as the Go and Rust decoders refuse it before any
validator runs. A type's `validate<Type>` validates each union field with
it, the member's errors nested under the field's path at each list level
and key (`payment.number`, `shapes[1].radius`, `byName.a.radius`), as the
Go types' `Validate` reports them.

## Load environment variables

A schema with an `@envVars` class gets its loader in the types package,
under the `./config` export:

```ts
import { loadShopConfig } from "@schemas/shop-config-types/config";

const config = loadShopConfig();   // reads process.env; pass a map in tests
config.PORT;                       // a number; the schema default when unset
config.API_KEY.reveal();           // a Secret<string> field is a SecretValue
JSON.stringify(config);            // ... "API_KEY":"[secret]" ...
```

`load<Type>` parses each variable by its schema type: a `number` is an
integer, a boolean is `true` or `false`, an enum is one of its values, and
a scalar runs its validators. An unset or empty variable takes the schema
default, else `null`. The loader throws `EnvConfigError` naming every
missing or invalid variable, never its value. A `Secret<T>` field prints
as `[secret]` everywhere except `reveal()`. The loader reads no `.env`
file ([D22](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d22-a-typescript-env-loader-in-the-typescript-types-package)).

## Consume a generated SDK

An API schema with `outputs.sdk` for TypeScript writes
`@schemas/<name>-sdk`. It needs `outputs.types` for TypeScript too: the SDK
decodes responses and validates inputs with the types package, a peer
dependency, and the build refuses the config without it. The class is
`<Name>SDK`:

```ts
import { CatalogSDK } from "@schemas/catalog-sdk";

const sdk = new CatalogSDK({
  baseUrl: "https://api.example.com",
  auth: { token: process.env.API_TOKEN },
});

const product = await sdk.product.getProduct(id);
```

`auth.token` is a static token. `auth.getToken` is called per request.
`setToken` / `clearToken` change the token after construction. The SDK of
an API with an operation that needs a caller (`@auth`,
`@requirePermission`, `@requireOwnership`) sends the token on every request
as `Authorization: Bearer <token>`. Each namespace is a camelCase field,
and operation sets that share a namespace share it: `ProductQueries` and
`ProductMutations` are both `sdk.product`.

A union argument, alone, in a list or in a map, is validated before the
request by the types package's `validate<Union>`: a null element or value
is `required` at its path, and each value's member breaks its own rules at
the value's path, nested (`shape.radius`, `shapes[1].radius`), as the route
reports them. A value no member reads is sent as it is, and the route
refuses it. A union field of an input is validated by the input's
`validate<Type>`, in the same way. Either way the value is sent as JSON,
a `Date` field as its RFC 3339 text, as an input is.

A response is read as its type, at each list level, in each value of a
map and in each item of a page. An object type or a union the schema declares goes through the
types package's `parse<Name>FromJSON`, so a date-time field is a `Date`, a
union is the member its parser picks, and a `@strictJSON` type is checked.
A value its parser refuses rejects the call with the types package's
`ParseError`. An enum or a scalar stays the value the API sent.

`serviceCredential: { token, headers }` is the calling service's own
credential
([D37](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d37-a-service-caller-beside-the-end-user-admitted-per-operation)):
`token(fresh)` returns it, and the SDK sends `Bearer <token>` in
`Service-Authorization`, or in each of `headers`, on every request. A 401
whose code is `service_unauthorized` calls `token(true)` once and retries,
without the end-user refresh. A server forwards its own caller per call
with `{ forward: ctx }` as a method's last argument: the call sends
`ctx.bearerToken` as `Authorization`, or none when it is null, instead of
the configured token, and does not refresh it.

An operation without an input type takes its arguments in order after the
path parameters, then its query parameters as one object, if it has any,
and an `AbortSignal` or `RequestOptions` (`{ signal, forward }`). A plain
object in the first argument's place is instead all of its arguments by
name, followed by the query parameters and the signal or options. They
travel where the route reads them: on `GET` in the query string, and on
every other method, `DELETE` included, as the fields of the JSON body.

A map argument (`Record<string, T>`, `Record<string, T[]>` for a map of
lists) is sent as that JSON object. Since a plain object in first place is
the arguments by name, a map that comes first is passed that way:
`sdk.tag.nameShades(id, { shadeByName: { a: "light" } })`. Before the
request, an absent required map is `required` and a value that is not an
object is `type` at the argument. A null value or list element is
`required` at `name[key]` or `name[key][i]`, a list value that is not an
array is `type` at `name[key]`, and each value or element runs the types
package's validator for `T` there, an object type's fields at
`name[key].field`. Every failure is thrown at once in one
`ValidationError`.

A method resolves to the response's type: `T[]` for a list,
`Record<string, T>` for a map (`Record<string, T[]>` for a map of lists),
and `T | null` for a `Nullable<T>`, which resolves to `null` when the
server answers null. An object type's or a union's values in a map are
decoded one by one, as a list's elements are.

## Serve a generated API

An API schema can be served by a generated TypeScript router instead of the
Go server. Set the server language in `schema.config.ts`:

```ts
outputs: {
  types: { [TargetLanguage.TypeScript]: { enabled: true } },
  api: { enabled: true, language: "TYPESCRIPT" }
}
```

The server needs `outputs.types` for TypeScript, as above: the router
validates requests with the types package's decoders, a peer dependency,
and the build refuses the config without it. Each API it `calls` needs
`outputs.sdk` for TypeScript, for the client its `Deps` holds.

The build writes `schemas/dist/api/<name>` as `@schemas/<name>-api`:
`interfaces.ts` has one `<Namespace>Implementation` interface per operation
namespace, and `router.ts` has `buildRouter(implementations, options)`, which
returns a Hono app. The router decodes path and query parameters, parses
JSON bodies with the generated `parse<Input>Json` decoder, applies
`@publicRoute`, `@auth` and `@requirePermission`, `@bodyLimit`,
`@rateLimit` and `@timeout`, and writes the `{data, meta: {requestId}}` and
RFC 9457 problem envelopes. It serves the OpenAPI document at
`GET /api/openapi.json`, with its page at `GET /api/docs`, as the Go and
Rust servers do
([OpenAPI](/superschematic/guides/api-routes/#openapi)), and, with
`outputs.api.mcp: true`, the API's visible `@mcp` tools at
`POST /api/mcp`
([An API's MCP endpoint](/superschematic/reference/mcp-tools/#an-apis-mcp-endpoint)).
It is built on `@superschematic/http-runtime`
(the `http_runtime_npm_package` naming key) and Hono, which are its peer
dependencies.

The package also says what the implementation is built from, as the Go
server's does
([Start an implementation](/superschematic/guides/api-routes/#start-an-implementation)).
`deps.ts` declares `Deps`, which holds the API's `config` when it has
one, a `pg` Pool `db` for its database (its `authDb`, or its one DB
dependency), a TypeScript SDK client of each API it `calls` (`shopApi`
for `shop-api`) and a `logger`, the HTTP runtime's JSON-lines logger.
`Constructor`, `(deps: Deps) => Implementations | Promise<Implementations>`,
is the type of the implementation's `create`, and an API with a route
that needs an end user also declares `AuthenticatorFactory`, the type of
its `authenticate`, which builds the `Authenticator` from `Deps`. An API
with a `@manualRouteRegistration` operation declares `ManualRoutes`, a
`ManualRouteHandler` per such operation, and `ManualRoutesConstructor`,
the type of the implementation's `createManualRoutes`, which builds the
handlers from `Deps`. An API with
[jobs](/superschematic/guides/stacks/#jobs) declares `Jobs`, a method per
job, `shipOrders(signal: AbortSignal): Promise<void>`, and
`JobsConstructor`, the type of the implementation's `createJobs`; one with
[workers](/superschematic/guides/queues/) declares `Workers`, a method
per worker that handles one message of its queue,
`fulfilOrders(message: OrderPlaced, signal: AbortSignal): Promise<void>`,
and `WorkersConstructor`, the type of its `createWorkers`.
`config.ts` declares `EnvConfig` and `loadEnvConfig()`, which reads the
`@envVars` settings through the types package's loader, and the fields a
[stack](/superschematic/guides/stacks/) derives for the API's edges through
the HTTP runtime's `loadDatabase`, `loadService` and `loadCallers`: a
`Database` per database, a `Service` per `calls` entry and, for an API
with `@requireService` or `@allowService`, its callers field
(`SHOP_API_CALLERS`), what its `serviceAuthenticator` checks, whose
variables `values-schema.json` lists for a deployment outside a stack. It
throws a `StackConfigError` that names every variable missing or
invalid.

The implementation is a package of its own at the naming file's
[`[implementation_paths]`](/superschematic/reference/naming/#implementation_pathstypescript)
`typescript` template, `typescript/<service>` from the parent of the
schemas root by default. A stack's build writes a missing one for each
API its servers serve, and so do `build --scaffold` and
`build-all --scaffold` for each TypeScript API built: `package.json`
(`@schemas/<service>-implementation`), `tsconfig.json`, and an `index.ts`
whose `create` is a `Constructor` and whose methods throw the runtime's
`notImplemented()`, which answers 501, with an `authenticate` that
establishes no end user, a verifier for each `@hmacVerified` provider
that refuses every request, and a `createManualRoutes`, a `createJobs`
and a `createWorkers` whose handlers and methods throw it too. It never
writes into a directory that holds a `.ts` file, so an implementation
that predates its manual routes, jobs or workers fails the stack's build,
which says what to add.

In a [stack](/superschematic/guides/stacks/), the build writes each
TypeScript server's entrypoint at `schemas/dist/server/<stack>/<server>`:
a `package.json` in the output root's Bun workspace, which depends on the
API packages, the implementations by their `package.json` names and the
SDKs of the APIs called, a `main.ts` that builds each implementation from
its `Deps` with `create(deps)` and `authenticate(deps)`, and the handlers
of its manual routes with `createManualRoutes(deps)`, which it hands the
router as `manualRoutes`, mounts each router on one Hono app and serves
it with `Bun.serve` beside `/healthz` and `/readyz`, and a Dockerfile. A
server that serves an API with `@requireService` or `@allowService` and
calls it from another API it serves awaits `loopbackServiceAuth(...)` at
start, a key only its process holds: its client of that API sends the
key's token to the server's own loopback URL, and the API's
`serviceAuthenticator` takes the callers field with the key accepted
(`loopback.accept(config.callers)`), which admits the server itself
([Service callers](/superschematic/guides/auth-and-permissions/#service-callers)).
`superschematic stack dev` runs it with `bun main.ts` after one `bun
install` in `schemas/dist`, or, with `--out`, in that output root, whose
servers build against its copies of the implementations. Each job and worker of the API gets a package
of its own beside it, whose `main.ts` builds the same `Deps` and runs the
job once, or the worker's claim loop.
Neither serves a request, so a stack gives neither the callers field. For
an API with one, `config.ts` adds `loadBackgroundEnvConfig()`, which the
job's and the worker's `main.ts` call: it loads what `loadEnvConfig()`
does, but leaves the callers field without issuers.

An operation without an input type reads its other arguments from the JSON
body object (on `GET`, from the query string). Each body argument is the
JSON value it holds, alone, as a list (`T[]`) or as a list of lists
(`T[][]`):

- A string, enum, UUID or timestamp argument takes a JSON string, a
  number or integer argument a JSON number, and a boolean argument a JSON
  boolean. Any other JSON type answers 400 with `type`: `"5"` is not a
  number, and `5` is not a string.
- A `Generic.JSON` argument takes any JSON value but null, and the
  implementation receives that value. An optional one also takes null:
  the implementation receives `null`, apart from an absent one, which is
  `undefined`.
- A `Generic.StringMap` argument takes a JSON object and an
  `Embedding.Vector` argument a JSON array, as the generated types send
  them; any other JSON type, the value's JSON text included, answers 400
  with `type`. See [JSON-valued scalars](/superschematic/reference/json-scalars/).
- An argument of an object type goes through that type's generated
  `parse<T>Json` decoder, so the implementation receives objects. An
  element the decoder refuses answers "does not match the declared type".
- A list is its JSON array. A comma inside an element stays there, and an
  empty string is an element. Only a list in the query string is read from
  repeated keys and comma-separated values.
- A map (`Record<string, T>`) is a JSON object, and the implementation
  receives a `Record<string, T>`. Each value follows the element rules at
  `name[key]`; a map of lists (`Record<string, T[]>`) has each list at
  `name[key]` and its elements at `name[key][i]`. `{}` satisfies a
  required map, and list bounds do not bound a map. A map travels only in
  the body.

A list follows the
[list rules](/superschematic/reference/arrays-of-arrays/#list-rules):
`[]` satisfies a required list, `listMin` and `listMax` bound the list,
and each element is checked at `name[i]`. A null element answers 400 with
`required`, and an element of the wrong JSON type with `type`. A list in
the query string, a `GET` argument or a `QueryParam<T[]>`, is read as the
Go routes read it: each item is its JSON value (a number is a JSON number,
so `0x10` is not one, and a boolean is a spelling Go's `strconv.ParseBool`
accepts) and is checked at `name[i]`. A scalar-typed argument, in the
path, the query or the body, is checked against the scalar's own length,
pattern and range, and a value that fails
answers with the rule it breaks (`pattern`, `minLength`, `maxLength`,
`min`, `max`). The problem `details` carry the `path` and the rule in
`errors`. A union field of the input or of an object argument is
validated as its member by the input's `validate<Type>`, so a member that
breaks its own rules answers 400 with `validation failed` and the member's
errors under the field's path (`payment.number`), and is then read with
the union's `parse<Union>FromJSON`, so the implementation receives its
member with date-time fields as `Date`. A union value no member reads
answers 400 with `does not match the declared type`, as the Go and Rust
servers answer it. A body argument whose type is a union, alone, in a list
or in a map, is an object argument: `parse<Union>Json` checks the member
the union picks by its rules and `parse<Union>FromJSON` reads it, and a
value that is not an object, that no member reads or whose member breaks a
rule is refused as an object argument is, `does not match the declared
type` at the argument.

```ts
import { Hono } from "hono";
import { errorHandler, notFoundHandler } from "@superschematic/http-runtime/hono";
import { buildRouter, type Implementations } from "@schemas/catalog-api";

// ProductQueries and ProductMutations share the `product` namespace.
const implementations: Implementations = {
  product: {
    getProduct: async ({ id }, ctx) => loadProduct(id),
  },
};

const app = new Hono();
app.route("/", buildRouter(implementations, {
  authenticate: async ctx => callerFor(ctx.bearerToken),
}));
app.notFound(notFoundHandler());
app.onError(errorHandler());
```

The router decides nothing about who the caller is. `authenticate` returns a
principal (`{ subject, permissions }`) or null. A route that needs a caller
answers 401 without one, and 403 when the caller's permissions cover none
of the route's. A granted permission covers itself and every permission
nested under it (`carts` covers `carts.write`). Pass `permissionMatcher` to
use another rule. An operation declared `@manualRouteRegistration` is gated
the same way and then handed to `options.manualRoutes.<operation>` with the
Hono context, for a streaming response or anything else the JSON router
cannot express. A stack's server passes the handlers the implementation's
`createManualRoutes` builds. The config needs neither `public` nor
`authDb` for this server: those wire the Go server's auth middleware to
an auth store.
`ctx.bearerToken` holds the token of a `Bearer` `Authorization` header;
any other scheme, such as an API key, reaches `authenticate` untouched in
`ctx.headers`.

`authenticateService`, beside `authenticate`, establishes a calling
service from its `Service-Authorization` credential
([Service callers](/superschematic/guides/auth-and-permissions/#service-callers)).
`serviceAuthenticator(config)` from `@superschematic/http-runtime` is the
standard one. With it, every route verifies a credential that is present
and puts the caller on `ctx.serviceCaller`, and a route with an
`@requireService` or `@allowService` clause admits by it, after the body
limit and before `authenticate`. Without it, such a route answers 401
`service_unauthorized`.

An encrypted operation must be `@manualRouteRegistration`, and the build
fails with the operation named when one is not: one in an `Encrypted`
operation set, one declared `@encrypted`, or one whose result or an
argument is an `EncryptedField<T>`
([Encrypted payloads](/superschematic/guides/api-routes/#encrypted-payloads)).
The Go server decrypts such a body with its `PayloadDecryptor` before it
parses it. The TypeScript router has no decryption step and would hand the
ciphertext to the body parser, so the service's handler decrypts the
payload and decodes it.

An operation that uploads files is an ordinary route
([Files](/superschematic/guides/api-routes/#files)): the router reads its
`multipart/form-data` body, checks each file against its field and hands
the method the files in `args.files`, beside `args.input` without the file
fields. Its body cap is `@bodyLimit`, or without one the files' limits
beside `bodyLimitBytes`. An operation whose result is a `FileDownload`
returns `{ body, filename }`, and the router answers the file with its
`Content-Type` and `Content-Disposition`.

An `@hmacVerified` operation
([Webhooks](/superschematic/guides/api-routes/#webhooks)) runs its
provider's verifier before every other step, a manual one's included.
`Implementations` has a `webhookVerifiers` property for each provider, so
tsc fails without one, and `buildRouter` throws. A verifier is Hono
middleware: it throws an `HttpProblem` for a signature that does not
match, and awaits `next()` for one that does. It may read the body; the
route reads a copy taken before the verifier ran.

```ts
import { createHmac, timingSafeEqual } from "node:crypto";
import { HttpProblem } from "@superschematic/http-runtime";
import type { WebhookVerifier } from "@superschematic/http-runtime/hono";

const verifyGitHub: WebhookVerifier = async (c, next) => {
  const expected = Buffer.from("sha256=" + createHmac("sha256", secret).update(await c.req.text()).digest("hex"));
  const sent = Buffer.from(c.req.header("x-hub-signature-256") ?? "");
  if (sent.length !== expected.length || !timingSafeEqual(sent, expected)) {
    throw new HttpProblem(401, "The webhook signature does not match", { code: "invalid_signature" });
  }
  await next();
};

const implementations: Implementations = {
  // ...one object per operation namespace
  webhookVerifiers: { github: verifyGitHub },
};
```

For a provider that signs with a JWT carrying the body's SHA-256, such as
Plaid, `jwtBodyHashVerifier` from the same module is the verifier
([A JWT that carries the body's hash](/superschematic/guides/api-routes/#a-jwt-that-carries-the-bodys-hash)).
It verifies with WebCrypto, so it runs on Node.js, Bun and Workers.

The generated package ships TypeScript sources, so run it with Bun, a
bundler or a TypeScript loader. The runtime ships compiled JavaScript with
declarations. `examples/acme-schematic`
(`shop-storefront` and its `storefront/` app) is a complete example.
