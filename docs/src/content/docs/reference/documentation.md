---
title: Documentation decorators
description: The @docs decorator on operations, @docs, @purpose and @icon on fields, what they write, and how an extension adds its own rules.
sidebar:
  order: 3
---

Documentation decorators attach reader-facing text to a schema: the name
and description a reader sees, facts about an operation's lifecycle, and a
field's label, purpose and icon.
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
| `useWhen` | no | when a caller, a person or a model, should choose this operation |
| `doNotUseWhen` | no | when a caller should choose another operation instead |
| `success` | no | the outcome a caller should expect after a successful call |
| `errors` | no | a non-empty list of `{ code, description, commonCorrection }`: the expected errors and the usual correction for each; every field required, codes unique ignoring case |

The optional texts are non-blank and have no surrounding whitespace when
given. The guidance keys (`useWhen` through `errors`) are written for a
caller choosing between operations, a model included.

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
  with the optional keys only when set, and `errors` as a list of objects:

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

## `@docs`, `@purpose` and `@icon` on a field

From `@superschematic/schema`, on a property of any type:

```ts
import { docs, icon, purpose } from "@superschematic/schema";

export abstract class ShopConfig {
  @docs({ title: "Database URL" })
  @purpose("Connection string of the **shop database**.")
  @icon("globe")
  DATABASE_URL: Network.Url;
}
```

| Decorator | Writes | Legacy JSON Schema key | Rule |
| --- | --- | --- | --- |
| `@docs({ title })` | `FieldDef.title` | `title` | the object holds `title` and nothing else; non-empty |
| `@purpose(markdown)` | `FieldDef.purpose` | `x-purpose` | non-empty; Markdown explaining what the field is for |
| `@icon(name)` | `FieldDef.icon` | `x-icon` | non-empty; any name (see [Rules an extension adds](#rules-an-extension-adds)) |

Each appears at most once on a field. They are presentation only: no
generated type, validator or wire format changes. The JSON and YAML forms
write `title`, `purpose` and `icon` on the field; a blank value is a load
error in every form.

`@superschematic/api` also exports `docs`, for operations. A file that
uses both imports one under another name
(`import { docs as fieldDocs } from "@superschematic/schema"`); the loader
resolves a decorator by its declaring package, not by the local name. The
TypeScript writer imports them as `apiDocs`, `schemaDocs` and `schemaIcon`.

The schema runtimes carry the three fields. The TypeScript runtime's
`parseSchema` and `writeSchemaJson` read and write `title`, `x-purpose` and
`x-icon` in the legacy JSON Schema form and `parseSchemaIR` reads `title`,
`purpose` and `icon` from the IR; the Python runtime's `parse_schema` reads
the legacy keys into `FieldDef.title`, `purpose` and `icon`.

## Rules an extension adds

The core checks shapes and nothing about vocabulary. A distribution that
has a closed set of audiences or icons, or wants its own vendor key,
registers that rule in its extension; the core has no option for it.

- A value set: `Registry.RegisterCheck` with a `CheckSpec` whose `Verify`
  walks the operations (or the fields) and reports a `@docs` audience (or
  an `@icon` name) outside the set. It runs on every loaded schema, core
  kinds included, in every authoring form.
- A vendor key: `Registry.RegisterOpenAPIHook` with an `OpenAPIHook` that
  moves each operation's `registry.OpenAPIDocsKey` entry to the
  distribution's key.

`examples/acme-schematic/ext/docs.go` does all three: acme accepts the
audiences `shoppers` and `staff`, the icons `box`, `globe`, `key`,
`receipt` and `tag`, and writes `x-acme-docs`. The
[extension guide](/superschematic/guides/write-an-extension/#policy-on-what-the-core-writes)
shows the two registrations.
