---
title: Documentation decorators
description: The @docs decorator on operations, @docs, @purpose and @icon on fields, @display on a type, what they write, and how an extension adds its own rules.
sidebar:
  order: 3
---

Documentation decorators attach reader-facing text to a schema: the name
and description a reader sees, facts about an operation's lifecycle, a
field's label, purpose and icon, and how a UI shows a type's instances.
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
| `replayMode` | no | `read_only`, `idempotent` or `compare_and_swap`: what happens when a caller sends the same call twice |
| `idempotencyKeyPointers` | with `idempotent` | RFC 6901 pointers into the operation's tool arguments to the keys that make a repeat idempotent |
| `expectedRevisionPointers` | with `compare_and_swap` | RFC 6901 pointers into the tool arguments to the revision the call expects |
| `useWhen` | no | when a caller, a person or a model, should choose this operation |
| `doNotUseWhen` | no | when a caller should choose another operation instead |
| `success` | no | the outcome a caller should expect after a successful call |
| `errors` | no | a non-empty list of `{ code, description, commonCorrection }`: the expected errors and the usual correction for each; every field required, codes unique ignoring case |

The optional texts are non-blank and have no surrounding whitespace when
given. The guidance keys (`useWhen` through `errors`) are written for a
caller choosing between operations, a model included.

The replay keys are declared, never inferred from the operation's name or
arguments. `read_only` and no mode take no pointers; `idempotent` needs
`idempotencyKeyPointers` and no revision; `compare_and_swap` needs
`expectedRevisionPointers` and may also list idempotency keys. Each list
names a pointer once. The pointers address the operation's generated tool
arguments (the `parameters` object in the SDK's `tools/schema.json`, see
[MCP tools](/superschematic/reference/mcp-tools/)), so `/requestId` is the
`requestId` field of the operation's input and `/pickup/postalCode` a field
of a nested object. The SDK generators check that every segment is a
required argument, that an idempotency key is a string and that a revision
is a number, and fail the build otherwise.

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

The API's OpenAPI document, `openapi.json`, which every server's build
writes (the Go server embeds it in `openapi.go` and the Rust server in
`src/openapi.rs`, and both serve it at `GET /api/openapi.json`):

- `summary` is `title`. Without `@docs` it is the operation name.
- `description` is the `@docs` description. Without `@docs` it is the
  operation's comment.
- `deprecated: true` when `lifecycle` is `deprecated` or `retired`.
- The whole record is written under the vendor key `x-superschematic-docs`,
  with the optional keys only when set, `errors` as a list of objects, and
  a declared replay mode as `replay: { mode, idempotencyKeyPointers,
  expectedRevisionPointers }` with both lists always present:

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

`@superschematic/api` also exports `docs` and `icon`, for operations (an
operation's `@icon` is its MCP tool's icon, see
[MCP tools](/superschematic/reference/mcp-tools/)). A file that uses both
imports one under another name
(`import { docs as fieldDocs } from "@superschematic/schema"`); the loader
resolves a decorator by its declaring package, not by the local name. The
TypeScript writer imports them as `apiDocs`, `apiIcon`, `schemaDocs` and
`schemaIcon`.

The schema runtimes carry the three fields. The TypeScript runtime's
`parseSchema` and `writeSchemaJson` read and write `title`, `x-purpose` and
`x-icon` in the legacy JSON Schema form and `parseSchemaIR` reads `title`,
`purpose` and `icon` from the IR; the Python runtime's `parse_schema` reads
the legacy keys into `FieldDef.title`, `purpose` and `icon`.

## `@display` on a type

From `@superschematic/schema`, on a class of any kind. It says how a UI,
or an agent, shows the type's instances:

```ts
import { behavior, display, docs } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "implementing", "review", "done"],
  transitions: [
    { from: "todo", to: "implementing" },
    { from: "implementing", to: "review" },
    { from: "review", to: "done" },
  ],
})
@display({
  noun: "Ticket",
  plural: "Tickets",
  titleField: "title",
  createLabel: "New ticket",
  summaryFields: ["status", "assignee"],
  states: {
    todo: { label: "To do", tone: "muted" },
    implementing: { label: "Implement", activeForm: "Implementing", tone: "active" },
    done: { label: "Done", tone: "success" },
  },
  transitions: {
    todo: { implementing: "Start" },
    review: { done: "Accept" },
  },
})
export abstract class Ticket {
  @docs({ title: "Title" })
  title: string;

  assignee?: string;
}
```

| Key | Rule |
| --- | --- |
| `noun`, `plural` | what to call one instance and several; non-blank |
| `titleField` | the field whose value is an instance's title: one of the type's own fields, holding a single text value (`string`, or a scalar whose values are strings, such as `Identity.Name`; not a list, a map, a number, an enum or a JSON scalar), neither `Secret` nor `@uiHidden` |
| `createLabel` | what a button that creates an instance says; non-blank |
| `summaryFields` | the fields that summarize an instance in a list, in order, each once: the type's own fields, or fields its behaviors add (`Workflow`'s `status`), neither secret nor hidden |
| `states` | a label per `Workflow` state: `label`, `activeForm` (the present-progressive form a UI shows while an instance is in the state, "Implementing") and `tone`, at least one of the three |
| `transitions` | a label per `Workflow` transition, by the state it leaves, then the state it enters |

A `tone` is what a state means to a reader, which a UI maps onto its own
colors: `muted` (nothing happens in it), `active` (work is under way),
`success`, `warning` (it needs attention) or `danger`. The set is closed.

Every key is optional, but `@display({})` is an error, and a type takes
one `@display`. `states` and `transitions` label the type's `Workflow`
behavior, so a type that does not compose `Workflow` takes neither, and
each state and transition must be one its config lists. The loader checks
every rule in every form, and the engine checks them again when a schema
is defined:

```
src/ticket.schema.ts: type Ticket: @display states labels "lost", which is not a state of its Workflow (todo, implementing, review, done)
```

The record is `TypeDef.display` (`ir.TypeDisplay`). The JSON and YAML
forms write the decorator's argument under the type's `display` key, and
`superschematic json-schema` checks it with the same schema the
TypeScript frontend checks the argument with. It is presentation only:
no generator renders it, as none renders a field's title, and it adds no
field, operation or storage. The engine's describe document carries it
for the schema's instance type, with each field's title and icon
([Engine behaviors](/superschematic/guides/engine-behaviors/#display)).

A distribution's own display concepts, such as a role a UI shows a type
to, a home page or link defaults, are its extension's: a type decorator
of its own that writes its slot, `extensions.<name>`, as a field
directive does, and a check over `@display` for a rule on its values
(D18 and D10 in `docs/DECISIONS.md`).

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
[extension guide](/superschematic/extending/write-an-extension/#policy-on-what-the-core-writes)
shows the two registrations.
