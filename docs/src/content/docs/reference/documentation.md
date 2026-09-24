---
title: Documentation decorators
description: The @docs decorator on operations, what it writes to the IR and OpenAPI, and how an extension adds its own rules.
sidebar:
  order: 3
---

Documentation decorators attach reader-facing text to a schema: the name
and description a reader sees, and facts about an operation's lifecycle.
They write typed IR fields that the generators read. They change no wire
format and no validation of request or response data.

## `@docs` on an operation

From `@superschematic/api`, on a method of an operation set:

```ts
import { HttpMethod, docs, rest } from "@superschematic/api";

export class ReturnMutations {
  @docs({
    title: "Open a return",
    description: "Opens a return for one delivered order.",
    capability: "orders.returns.open",
    lifecycle: "deprecated",
    visibility: "public",
    audience: "shoppers",
    replacement: "orders.returns.create",
    sunset: "2027-01-31"
  })
  @rest(HttpMethod.POST, "returns")
  openReturn(input: ReturnRequest): Order {
    throw new Error("schema declaration only");
  }
}
```

| Key | Required | Rule |
| --- | --- | --- |
| `title` | yes | non-empty, no surrounding whitespace |
| `description` | yes | non-empty |
| `capability` | yes | a stable dotted identifier with at least two lowercase segments (`[a-z0-9]` words joined by `-`), such as `orders.returns.open` |
| `lifecycle` | yes | `draft`, `experimental`, `active`, `deprecated` or `retired` |
| `visibility` | yes | `public`, `internal` or `preview`; how the operation appears in documentation, not who may call it |
| `audience` | no | the primary reader; any non-blank string (see [Rules an extension adds](#rules-an-extension-adds)) |
| `mappingStatus` | no | `mapped` (the default) or `uncertain`: how sure the author is of the capability |
| `replacement` | no | what replaces a deprecated or retired operation |
| `sunset` | no | the date the operation stops being served, `YYYY-MM-DD` |

An operation takes at most one `@docs`. Every value must be a literal. A
config that breaks a rule fails the load with the decorator's location:

```
src/orders.schema.ts:14:9: invalid @docs config: capability "GetNote" must contain at least two dot-separated lowercase segments
```

The record is `FieldDef.docs` (`ir.OperationDocs`) in the IR. The JSON and
YAML forms write the same object under the operation's `docs` key, with
`mappingStatus` spelled out; the loader applies the same rules to them.
`docs` on a data field is an error.

### What the generators do with it

The Go API's OpenAPI document (`openapi.json`, embedded in `openapi.go`):

- `summary` is `title`. Without `@docs` it is the operation name.
- `description` is the `@docs` description. Without `@docs` it is the
  operation's comment.
- `deprecated: true` when `lifecycle` is `deprecated` or `retired`.
- The whole record is written under the vendor key `x-superschematic-docs`,
  with the optional keys only when set:

```json
"x-superschematic-docs": {
  "audience": "shoppers",
  "capability": "orders.returns.open",
  "description": "Opens a return for one delivered order.",
  "lifecycle": "deprecated",
  "mappingStatus": "mapped",
  "replacement": "orders.returns.create",
  "sunset": "2027-01-31",
  "title": "Open a return",
  "visibility": "public"
}
```

The TypeScript writer (`format`) emits the decorator as
`import { docs as apiDocs } from "@superschematic/api"`.

## Rules an extension adds

The core checks the shape of a record and nothing about its vocabulary. A
distribution that has a closed set of audiences, or wants its own vendor
key, registers that rule in its extension; the core has no option for it.

- A value set: `Registry.RegisterCheck` with a `CheckSpec` whose `Verify`
  walks the operations and reports a `@docs` audience outside the set. It
  runs on every loaded schema, in every authoring form.
- A vendor key: `Registry.RegisterOpenAPIHook` with an `OpenAPIHook` that
  moves each operation's `registry.OpenAPIDocsKey` entry to the
  distribution's key.

`examples/acme-schematic/ext/docs.go` does both: acme accepts the audiences
`shoppers` and `staff` and writes `x-acme-docs`. The
[extension guide](/superschematic/guides/write-an-extension/#policy-on-what-the-core-writes)
shows the two registrations.
