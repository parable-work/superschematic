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
contains it: the types workspace below, or your own workspace root with
the generated directories in its `workspaces`. A `file:` specifier from an
app outside a workspace does not install a types package that has a
`file:` or `workspace:` dependency of its own.

`schemas/dist/types/typescript/package.json` is a private Bun workspace
root (`@schemas/types-workspace`) whose workspaces are the generated types
packages next to it. A types package whose schema declares a
[version graph](/superschematic/reference/version-graphs/) depends on
`@superschematic/versiongraph`, with a `file:` path when
[`paths.versiongraph_typescript`](/superschematic/reference/naming/#pathsversiongraph_typescript)
is set and `*` otherwise. A types package depends on the scalar library
(`superscalar`) the same way, through
[`paths.scalar_typescript`](/superschematic/reference/naming/#pathsscalar_typescript),
and on another schema's types package with `workspace:*`.

`@superschematic/versiongraph` is unpublished until the first tag, and
`superscalar` until superscalar's first release. Until then a `*`
dependency fails `bun install` with a 404, so set both paths to a
checkout. The install resolves the whole workspace, so one schema with a
version graph and no path breaks the install of every types package
beside it. The version-graph package loads from its `dist/`: build it in
the checkout first (`bun install && bun run build` in
`runtime/versiongraph/typescript`, which needs cargo with the
`wasm32-unknown-unknown` target).

Run `bun install` in `schemas/dist/types/typescript` or in any package
under it, and again after each build. Every install writes the one
`bun.lock` at the root.
Install with Bun: npm rejects the `workspace:` protocol.

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
and the build refuses the config without it.

The build writes `schemas/dist/api/<name>` as `@schemas/<name>-api`:
`interfaces.ts` has one `<Namespace>Implementation` interface per operation
namespace, and `router.ts` has `buildRouter(implementations, options)`, which
returns a Hono app. The router decodes path and query parameters, parses
JSON bodies with the generated `parse<Input>Json` decoder, applies
`@publicRoute`, `@auth` and `@requirePermission`, `@bodyLimit`,
`@rateLimit` and `@timeout`, and writes the `{data, meta: {requestId}}` and
RFC 9457 problem envelopes. It is built on `@superschematic/http-runtime`
(the `http_runtime_npm_package` naming key) and Hono, which are its peer
dependencies.

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
`errors`. A body argument whose type is a union has no generated decoder,
and the build refuses it.

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
cannot express. The config needs neither `public` nor `authDb` for this
server: those wire the Go server's auth middleware to an auth store.

Two kinds of operation must be `@manualRouteRegistration`, and the build
fails with the operation named when one is not:

- An encrypted operation: one in an `Encrypted` operation set, one declared
  `@encrypted`, or one whose result or an argument is an
  `EncryptedField<T>` ([Encrypted payloads](/superschematic/guides/api-routes/#encrypted-payloads)).
  The Go server decrypts such a body with its `PayloadDecryptor` before it
  parses it. The TypeScript router has no decryption step and would hand the
  ciphertext to the body parser, so the service's handler decrypts the
  payload and decodes it.
- An operation that uploads files. The router has no multipart adapter.

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

The generated package ships TypeScript sources, so run it with Bun, a
bundler or a TypeScript loader. The runtime ships compiled JavaScript with
declarations. `examples/acme-schematic`
(`shop-storefront` and its `storefront/` app) is a complete example.
