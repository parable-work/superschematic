---
title: Go
description: Install the CLI, write a schema, build it, and consume generated Go types and the Go SDK.
sidebar:
  order: 1
---

The CLI is a Go binary. Generated Go types, ORM, API and SDK are ordinary
Go modules whose paths come from `go_module_root` in
[superschematic.toml](/superschematic/reference/naming/). The ORM, the API
server and the SDK import the schema's Go types, so a DB schema (which
always gets the ORM) needs `outputs.types.go`, and so do a Go API server
and a Go SDK. The build refuses the config without it.

## Requirements

- Go 1.26.4 (`GOTOOLCHAIN=go1.26.4`).
- `CGO_ENABLED=1` and a C compiler. The compiler links
  [superscalar](https://github.com/parable-work/superscalar) through cgo
  against a static archive.
- Until superscalar publishes `go/vX.Y.Z` tags, you need that archive from
  source. `scripts/superscalar-dep.sh` in this repository builds it.
- A generated ORM whose schema declares a
  [version graph](/superschematic/reference/version-graphs/) also imports the
  version-graph core's Go binding (`versiongraph_go_module`), which links
  the core's own static archive through cgo. Build it from a checkout with
  `scripts/versiongraph-archive.sh` and add the `-L` directory it prints to
  `CGO_LDFLAGS`, beside superscalar's. A `go.mod` replace line to the
  checkout (`paths.versiongraph_go`) finds the archive without the flag.
  The module's engine keeps a graph in Postgres (package `postgres`, over
  pgx) or SQLite (package `sqlite`, over `database/sql` and a driver you
  pick); see
  [The engine and its adapters](/superschematic/reference/version-graphs/#the-engine-and-its-adapters).

## Install the CLI

The CLI is `superschematic`: the core with the official extensions linked,
the gcp target, the Cloudflare DNS platform and the Pulumi provisioner. It
is the Go module
`github.com/parable-work/superschematic/cmd/superschematic`.

`go install github.com/parable-work/superschematic/cmd/superschematic@<version>`
does not work. Every Go module in this repository carries `replace`
directives, to its sibling modules and to the TypeScript compiler fork the
frontend pins, and a release keeps them. `go install` at a version refuses
a module that has any.

Nothing is published yet. From `v0.1.0-alpha.1`, each release attaches the
binary for linux and darwin on x64 and arm64, as
`superschematic_<version>_<platform>.tar.gz` (`linux-x64`, `linux-arm64`,
`darwin-x64`, `darwin-arm64`). Until then, build it from a checkout:

```
export GOTOOLCHAIN=go1.26.4
eval "$(scripts/superscalar-dep.sh --export)"
cd cmd/superschematic
go build -trimpath -buildvcs=false -o ../../bin/superschematic .
```

`make setup && make build` does the same. The binary links superscalar's
static archive, so it runs without `CGO_LDFLAGS`; compiling the Go code it
generates still needs them (above).

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
checkout so you can compile before the modules are tagged. Its values are
relative to the parent of the schemas root; an absolute path is an error.
Leave the table out when you consume published modules. Go reads replace
lines only from the module it builds, so each generated `go.mod` also
requires and replaces every generated types module it reaches through
another one: an API whose auth DB takes a type from a General service
replaces that service's types module too.

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

An optional `Generic.JSON` field keeps null apart from absent. In an input
type it is an `InputField[GenericJSON]`, which already does. In any other
type it is a `*GenericJSON`: `nil` when the key is absent, and a pointer to
`GenericJSON("null")` when it is null, which `encoding/json` alone would
also leave `nil`. Encoding writes the pointer back as `null` and leaves out
a `nil` one. `To<Type>` carries an input's null over as the pointer.

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
`example.com/schemas/api/<name>`. It needs `outputs.types.go` too: the
routes decode requests into the schema's Go types and the handler
interfaces take and return them. `RegisterRoutes` mounts its routes on a
chi router and calls your implementations.

An operation with an input type reads it from the JSON body. An operation
without one reads its other arguments from the JSON body object, each from
its own JSON value, through `runtime/http/go/bodyargs`. On `GET` they come
from the query string (below). In the body:

- A string, enum, UUID or timestamp argument takes a JSON string, a number
  a JSON number, an integer a JSON integer, and a boolean `true`
  or `false`. Any other JSON type is `type`: `"5"` is not a number, and
  `5` is not a string.
- A `Generic.JSON` argument takes any JSON value but null, and the
  implementation receives that value. An optional one also takes null:
  the implementation receives `GenericJSON("null")`, the JSON null token,
  apart from an absent one, which is `nil`
  ([null in an optional Generic.JSON](/superschematic/reference/json-scalars/#null-in-an-optional-genericjson)).
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
is its zero value, but for the null of an optional `Generic.JSON`. The body
must be one JSON object, and its keys match the argument names exactly.
The route answers 400 with every error at once, keyed by path, as it does
for an input type's fields:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "Validation failed",
  "code": "bad_request",
  "errors": {
    "labels[1]": [{ "validator": "required", "message": "required field" }],
    "links[0]": [{ "validator": "pattern", "message": "invalid format" }]
  }
}
```

An input type's body (`input: PlaceOrderInput`) is refused as every
generated server, Go, TypeScript and Rust, refuses it. A missing body is
"Request body is required" and one that is not JSON "Request body is not
valid JSON". A body the type refuses is "Request body does not match the
declared input": `details.reason` says why (`expected an object`,
`unknown fields: coupon`, `validation failed`, `does not match the
declared type`), and `errors` holds each field's errors by path. A
top-level key the type does not declare is refused, `unknown` at its
key:

```json
{
  "type": "about:blank",
  "title": "Bad Request",
  "status": 400,
  "detail": "Request body does not match the declared input",
  "code": "bad_request",
  "details": { "location": "body", "reason": "unknown fields: coupon" },
  "errors": { "coupon": [{ "validator": "unknown", "message": "unknown field" }] }
}
```

On `GET`, a list argument is read from repeated keys and comma-separated
values (`?labels=a,b&labels=c` is `["a", "b", "c"]`) through
`bodyargs.QueryList`. Each item is trimmed and an empty one is dropped; no
item is an absent list, which is `required` when the argument is required.
`listMin` and `listMax` bound the items. Each item is read as its type's
JSON value (a number, an integer, a boolean as `strconv.ParseBool` reads
it, or a string) and then follows the element rules above at `name[i]`:
`?scores=1,x` is `type` at `scores[1]`, and `?ranks=0` breaks
`Ordering.Rank`'s `min` at `ranks[0]`. A list query parameter
(`QueryParam<T[]>`), on any method, is read the same way. An optional
single value that is absent reaches the implementation as its type's zero
value. Query validation names its rules as the body does: `minLength`,
`maxLength`, `pattern`, `min`, `max`, `listMin` and `listMax`.

`openapi.json` describes a body argument, and a query parameter, as it
describes a field of an input type: the scalar's own constraints and the
argument's (`minLength`, `maxLength`, `pattern`, `minimum`, `maximum`)
sit on each value, the items of a list or the values of a map, and
`listMin` and `listMax` are `minItems` and `maxItems` on the list. A
query parameter's schema is never nullable: the parameter is present or
absent, which `required` says.

Who may call each route, and how the server learns who is calling, is in
[Auth and permissions](/superschematic/guides/auth-and-permissions/): an
API with `public: true` runs `Config.AuthMiddleware` on its protected
routes, and a schema with an `@requireService` or `@allowService` clause
adds `Config.ServiceAuthenticator`, a `serviceauth.Authenticator` from the
HTTP runtime, which `Config.Validate` requires. A handler reads the
calling service with `serviceauth.CallerFromContext(ctx)`.

## Consume a generated SDK

An API schema with `outputs.sdk.go` enabled writes
`example.com/schemas/sdk/go/<name>`. It needs `outputs.types.go` too: the
SDK's methods take and return the schema's Go types, and the build refuses
the config without them. Construct the client with `New`:

```go
sdk, err := catalogsdk.New(catalogsdk.SDKConfig{
    BaseURL: "https://api.example.com",
    Auth:    &catalogsdk.AuthConfig{Token: token},
})
```

`AuthConfig.Token` is static. `GetToken` is asked per request, with the
call's context, when no static token is set, and `RefreshToken` runs once
after a 401. `SDKConfig.ServiceCredential` is the calling service's own
credential
([D37](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md#d37-a-service-caller-beside-the-end-user-admitted-per-operation)):
its `Token(ctx, fresh)` returns it, a `serviceauth.TokenSource` of the
HTTP runtime among others, and the SDK sends `Bearer <token>` in
`Service-Authorization`, or in each of `Headers`, on every request. A 401
whose code is `service_unauthorized` calls `Token(ctx, true)` once and
retries, without the end-user refresh. A server forwards its own caller by
setting `GetToken` to `serviceauth.ForwardedToken`, which reads the token
of the request on the call's context.

The client has one field per namespace. Operation sets that share a
namespace share a field: `ProductQueries` and `ProductMutations` are both
the `product` namespace, so their methods are on `ProductNamespace`
(`sdk.ProductNamespace.GetProduct(ctx, id)`). `examples/acme-shop/go` has a
tested client, walked through in
[Serve and call it from Go](/superschematic/first-project/go-api/), and
`examples/acme-schematic` a full API plus auth provider.

An operation without an input type takes its body arguments as one input
struct. A map argument is a `map[string]T` field (`map[string][]T` for a
map of lists), as the route takes it, and is sent as a JSON object; an
optional one is left out when it is nil. Before the request, a nil
required map is `required`, a nil list value is `required` at
`name[key]`, and each value or list element runs its own validation at
`name[key]` or `name[key][i]`.

A response outside 2xx returns an `*APIError`, or a type that embeds one:
`*AuthenticationError` for 401, `*AuthorizationError` for 403 and
`*RateLimitError` for 429. `Message` is the problem's `detail` (or an
older body's `error` or `message`), `Code` is its `code` and
`StatusCode` the HTTP status. `Error()` joins them:
`order 7 has already shipped (code: ORDER_SHIPPED, status: 409)`, or
`<message> (status: <status>)` when the response has no code.
