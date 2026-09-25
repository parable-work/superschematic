---
title: Go
description: Install the CLI, write a schema, build it, and consume generated Go types and the Go SDK.
sidebar:
  order: 1
---

The CLI is a Go binary. Generated Go types, ORM, API and SDK are ordinary
Go modules whose paths come from `go_module_root` in
[superschematic.toml](/superschematic/reference/naming/).

## Requirements

- Go 1.26.4 (`GOTOOLCHAIN=go1.26.4`).
- `CGO_ENABLED=1` and a C compiler. The compiler links
  [superscalar](https://github.com/parable-work/superscalar) through cgo
  against a static archive.
- Until superscalar publishes `go/vX.Y.Z` tags, you need that archive from
  source. `scripts/superscalar-dep.sh` in this repository builds it.

## Install the CLI

Nothing is published yet. From `v0.1.0-alpha.1`:

```
go install github.com/parable-work/superschematic/cmd/superschematic@v0.1.0-alpha.1
```

A consumer at that tag still needs `CGO_LDFLAGS` from
`scripts/superscalar-dep.sh --print` until superscalar publishes its own
module tags. Until the first tag, build from a checkout:

```
export GOTOOLCHAIN=go1.26.4
eval "$(scripts/superscalar-dep.sh --export)"
go build -o bin/superschematic ./cmd/superschematic
```

`make setup && make build` does the same.

## Write a schema

A schemas root holds `superschematic.toml` and one directory per service
under `services/`. A service is `schema.config.ts` (or `.json` / `.yaml`)
plus `src/*.schema.ts`. This General schema declares a type and asks for
Go types:

`schemas/superschematic.toml` can be empty; missing keys keep the
[defaults](/superschematic/reference/naming/).

`schemas/services/catalog/schema.config.ts`:

```ts
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "catalog",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
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

Authoring packages (`@superschematic/schema-config`, `@superschematic/schema`,
`@superschematic/db`, `@superschematic/api`) are unpublished until the first
tag. Point TypeScript at the checkout with `tsconfig` paths, the same way
`examples/acme-schematic/schemas/tsconfig.base.json` does. After the tag:

```
npm install @superschematic/schema-config @superschematic/schema
```

## Build

```
superschematic build schemas/services/catalog
```

Output lands in `schemas/dist/` by default (`--out` overrides). Go types
for this service are the module `example.com/schemas/types/go/catalog`
under `schemas/dist/types/go/catalog`.

`[paths]` in the naming file points generated `go.mod` replace lines at a
checkout so you can compile before the modules are tagged. Leave the table
out when you consume published modules.

## Consume generated types

```go
package main

import (
    "fmt"
    "log"

    types "example.com/schemas/types/go/catalog"
)

func main() {
    money, err := types.MoneyFromJSON([]byte(`{"amountCents":199,"currency":"USD"}`))
    if err != nil {
        log.Fatal(err)
    }
    if errs := money.Validate(); errs.HasErrors() {
        log.Fatal(errs)
    }
    fmt.Printf("%d %s\n", money.AmountCents, money.Currency)
}
```

`TypeFromJSON` is strict (unknown fields fail). `TypeFromJSONNonStrict`
accepts them. `Validate` returns field-level errors.

Decoding refuses a null list element, since a list element is never null:
`json.Unmarshal` into a generated type fails with
`decode <Type>: <field>[1]: null element` instead of putting the element
type's zero value in its place. A null list itself still decodes, to a nil
list or a null `InputField`.

Encoding keeps an empty list apart from an absent one. An optional list is
left out when it is nil and written when it is `[]`, so a decoded payload
re-encodes with the same keys. A required list encodes nil as `[]`.

## Consume a generated SDK

An API schema with `outputs.sdk.go` enabled writes
`example.com/schemas/sdk/go/<name>`. Construct the client with `New`:

```go
sdk, err := catalogsdk.New(catalogsdk.SDKConfig{
    BaseURL: "https://api.example.com",
    Auth:    &catalogsdk.AuthConfig{Token: token},
})
```

Namespace fields on the client match the operation sets in the schema
(`ProductQueries` becomes a `ProductQueries` field). See
`examples/acme-schematic` for a full API plus auth provider.
