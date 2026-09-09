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
`@schemas/catalog-types` (the default `npm_scope` is `@schemas`). Point
your app at that directory with a workspace dependency or a `file:`
specifier until you publish the generated package.

## Consume generated types

```ts
import { parseMoneyJson, type Money } from "@schemas/catalog-types";

const money: Money = parseMoneyJson(`{"amountCents":199,"currency":"USD"}`);
// throws when the payload is rejected

import { parseMoneyJsonNonStrict } from "@schemas/catalog-types";
const loose = parseMoneyJsonNonStrict(payload);
```

Every object type gets `parse<Type>Json`, `parse<Type>JsonNonStrict`,
`parse<Type>Yaml` and `parse<Type>YamlNonStrict`. Type-only imports (no
runtime) come from `@schemas/catalog-types/types`.

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
