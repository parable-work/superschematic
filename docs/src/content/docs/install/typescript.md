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
packages next to it. A types package depends on the scalar library with a
`file:` path when
[`paths.scalar_typescript`](/superschematic/reference/naming/#pathsscalar_typescript)
is set, and on another schema's types package with `workspace:*`. Run
`bun install` in that directory or in any package under it, and again
after each build. Every install writes the one `bun.lock` at the root.
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
required one is `required`. Type-only imports (no runtime) come from
`@schemas/catalog-types/types`.

## Consume a generated SDK

An API schema with `outputs.sdk` for TypeScript writes
`@schemas/<name>-sdk`. The class is `<Name>SDK`:

```ts
import { CatalogSDK } from "@schemas/catalog-sdk";

const sdk = new CatalogSDK({
  baseUrl: "https://api.example.com",
  auth: { token: process.env.API_TOKEN },
});

const product = await sdk.productQueries.getProduct(id);
```

`auth.token` is a static token. `auth.getToken` is called per request.
`setToken` / `clearToken` change the token after construction. Operation
sets become camelCase namespace fields.

## Serve a generated API

An API schema can be served by a generated TypeScript router instead of the
Go server. Set the server language in `schema.config.ts`:

```ts
outputs: {
  types: { [TargetLanguage.TypeScript]: { enabled: true } },
  api: { enabled: true, language: "TYPESCRIPT" }
}
```

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
  implementation receives that value.
- An argument of an object type goes through that type's generated
  `parse<T>Json` decoder, so the implementation receives objects. An
  element the decoder refuses answers "does not match the declared type".
- A list is its JSON array. A comma inside an element stays there, and an
  empty string is an element. Only a list in the query string is read from
  repeated keys and comma-separated values.

A list follows the
[list rules](/superschematic/reference/arrays-of-arrays/#list-rules):
`[]` satisfies a required list, `listMin` and `listMax` bound the list,
and each element is checked at `name[i]`. A null element answers 400 with
`required`, and an element of the wrong JSON type with `type`. A
scalar-typed argument, in the path, the query or the body, is checked
against the scalar's own length, pattern and range, and a value that fails
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
cannot express.

The generated package and the runtime ship TypeScript sources, so run them
with Bun, a bundler or a TypeScript loader. `examples/acme-schematic`
(`shop-storefront` and its `storefront/` app) is a complete example.
