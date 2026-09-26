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
go build -trimpath -buildvcs=false -o bin/superschematic ./cmd/superschematic
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

A union field decodes through its `<Union>Wrapper`. When every member
marks the same field `@internalMetadata`, the wrapper picks the member by
that field's value. Otherwise it picks by shape, because a member's decoder
ignores keys the member does not declare: the first member whose fields
include every payload key wins, unless the payload contradicts one of the
member's tags. A tag is a field that two or more members declare with
distinct string or enum defaults, such as
`kind: Default<TriggerKind, TriggerKind.NewMessage>`. When no member
declares every key, the wrapper tolerates the unknown keys and takes the
first member whose tags the payload allows. `Validate` checks the member a
union field holds, so a member without its required fields fails, and so
does an absent required union or a nil union element of a list or map.

Encoding keeps an empty list apart from an absent one. An optional list is
left out when it is nil and written when it is `[]`, so a decoded payload
re-encodes with the same keys. A required list encodes nil as `[]`.

A generated enum lists its members with `Values()`, which returns a new
slice in schema declaration order. It is a method, so it also works through
the alias a module that imports the enum declares: `Stage("").Values()`
returns the same list in both modules. It matches Rust's `ALL`,
TypeScript's `Object.values`, and iterating a Python enum. `IsValid()`
checks membership.

## Serve a generated API

An API schema with a Go API output writes the module
`example.com/schemas/api/<name>`. `RegisterRoutes` mounts its routes on a
chi router and calls your implementations.

An operation with an input type reads it from the JSON body. An operation
without one reads its other arguments from the JSON body object, each from
its own JSON value, through `runtime/http/go/bodyargs`. On `GET` they come
from the query string, where a list is read from repeated keys and
comma-separated values (`?labels=a,b&labels=c`). In the body:

- A string, enum, UUID or timestamp argument takes a JSON string, a number
  a JSON number, an integer a JSON integer, and a boolean `true`
  or `false`. Any other JSON type is `type`: `"5"` is not a number, and
  `5` is not a string.
- A `Generic.JSON` argument takes any JSON value but null, and the
  implementation receives that value.
- A `Generic.StringMap` argument takes a JSON object and an
  `Embedding.Vector` argument a JSON array; any other JSON type, the
  value's JSON text included, is `type`. See
  [JSON-valued scalars](/superschematic/reference/json-scalars/).
- An object-typed argument goes through its type's decoder, and its field
  errors nest under the argument's path.
- A list is its JSON array and follows the
  [list rules](/superschematic/reference/arrays-of-arrays/#list-rules):
  `[]` satisfies a required list, `listMin` and `listMax` bound it, and a
  null element is `required` at `name[i]`.
- A map (`Record<string, T>`) is a JSON object, and the implementation
  receives a `map[string]T`. Each value follows the element rules at
  `name[key]`; a map of lists (`Record<string, T[]>`) has its elements at
  `name[key][i]`. A map travels only in the body: a map argument of a
  `GET` operation, or a map path or query parameter, fails the build.
- The scalar's own lengths, pattern and range, then the argument's own
  constraints, apply to a value and to every element; a value that fails
  is one error named by the rule it breaks (`minLength`, `maxLength`,
  `pattern`, `min`, `max`). An enum value outside the enum is `enum`.

A required argument that is absent or null is `required`; an optional one
is its zero value. The body must be one JSON object, and its keys match
the argument names exactly. The route answers 400 with every error at
once, keyed by path, as it does for an input type's fields:

```json
{
  "type": "about:blank",
  "title": "Validation Failed",
  "status": 400,
  "detail": "Validation failed",
  "code": "WA-VL-001",
  "errors": {
    "labels[1]": [{ "validator": "required", "message": "required field" }],
    "links[0]": [{ "validator": "pattern", "message": "invalid format" }]
  }
}
```

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

A response outside 2xx returns an `*APIError`, or a type that embeds one:
`*AuthenticationError` for 401, `*AuthorizationError` for 403 and
`*RateLimitError` for 429. `Message` is the problem's `detail` (or an
older body's `error` or `message`), `Code` is its `code` and
`StatusCode` the HTTP status. `Error()` joins them:
`order 7 has already shipped (code: ORDER_SHIPPED, status: 409)`, or
`<message> (status: <status>)` when the response has no code.
