---
title: MCP tools
description: The @mcp and @icon decorators on operations, a tool's invocation policy, the tool documents the SDK generators write from them, and how an extension adds its rules, vendor keys and policy.
sidebar:
  order: 4
---

An API schema publishes an operation as an MCP tool with `@mcp`. The
decorator classifies the operation and nothing more: the tool's name and
description come from the operation's
[`@docs`](/superschematic/reference/documentation/#docs-on-an-operation),
its icon from `@icon`, and its arguments from the operation's parameters.
An operation without `@mcp` is not classified and is not published.

## `@mcp` on an operation

From `@superschematic/api`, on a method of an operation set:

```ts
import { HttpMethod, docs, icon, mcp, rest } from "@superschematic/api";

export class OrderQueries {
  @docs({
    title: "Get an order",
    description: "Returns one order by its identifier.",
    capability: "orders.get",
    lifecycle: "active",
    visibility: "public",
    replayMode: "read_only"
  })
  @icon("receipt")
  @mcp({ handle: "get_order", _meta: { ui: { resourceUri: "ui://orders/detail" } } })
  @rest(HttpMethod.GET, "orders/{id}")
  getOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }

  @docs({
    title: "Cancel an order",
    description: "Cancels an order that has not shipped.",
    capability: "orders.cancel",
    lifecycle: "active",
    visibility: "public"
  })
  @mcp({ handle: "cancel_order", invocationPolicy: "ask" })
  @rest(HttpMethod.POST, "orders/{id}/cancel")
  cancelOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }

  @mcp({ hidden: true, reason: "Staff console only." })
  @rest(HttpMethod.DELETE, "orders/{id}")
  deleteOrder(id: Identity.UUID): Order {
    throw new Error("schema declaration only");
  }
}
```

A visible tool takes `{ handle, invocationPolicy?, _meta? }`; a hidden
operation takes `{ hidden: true, reason }`.

| Key | Visible | Hidden | Rule |
| --- | --- | --- | --- |
| `handle` | required | not allowed | the tool's wire identifier: lowercase `snake_case` starting with a letter, at most 48 characters |
| `hidden` | `false` or absent | `true` | |
| `reason` | not allowed | required | why the operation is not a tool; non-blank, no surrounding whitespace |
| `invocationPolicy` | optional | not allowed | `"auto"` (the default) or `"ask"`; see [the invocation policy](#the-invocation-policy) |
| `_meta` | optional | not allowed | an object copied into the tool's MCP `_meta` as written |

A visible tool must also declare `@docs`: the tool's display name is the
`@docs` title and its description the `@docs` description. An operation
takes at most one `@mcp`, and every value must be a literal. A bad record
fails the load at the decorator:

```
src/notes.schema.ts:16:9: invalid @mcp config: handle "getNote" must be lowercase snake_case and begin with a letter
```

The record is `FieldDef.mcp` (`ir.OperationMCP`) in the IR: `handle`,
`hidden` (always written), `hiddenReason`, the invocation policy and
`_meta`, in that order. The JSON and YAML forms write the same object
under the operation's `mcp` key and the loader applies the same rules to
them. `mcp` on a data field is an error. The record's `name`,
`description` and `icon` are generated and cannot be declared.

## The invocation policy

A visible tool's invocation policy says whether an MCP client should run
the tool as soon as a model calls it (`"auto"`) or ask the person first
(`"ask"`). It is a default for the client: a client or its user may
override it. The core's key is `invocationPolicy`; a visible tool that
omits it gets `"auto"` when it loads, from any authoring form, so the IR
always carries it. A hidden operation has none.

| Where | Position | Hidden or unclassified operation |
| --- | --- | --- |
| IR and the data forms, `mcp` record | after `hiddenReason` (after `hidden` when there is no reason), before `_meta` | absent |
| `tools/schema.json`, `mcp` object | after `description`, before `_meta` | absent |
| `tools/mcp-audit.json`, each record | after `hiddenReason`, before `requiresAuth` | `""` |
| `tools/index.ts`, `ToolDefinition.mcp` | member `invocationPolicy?: 'auto' \| 'ask';` after `description?: string;` | |
| `tools/index.ts`, `toolDefinitions` | `invocationPolicy: 'auto',` after `description` | absent |

An extension replaces the key, the values and the default with
`Registry.RegisterToolInvocationPolicy` (see
[Rules an extension adds](#rules-an-extension-adds)). Everything above
then uses its key at the same positions, and `tools/index.ts` types the
member as the union of its values in the order it registered them.

In the IR the policy is `OperationMCP.Invocation`, an `ir.MCPInvocation`
that carries the key with the value, so the IR encodes and decodes
without a registry. Its JSON and YAML encoders write the key in place; the
decoders take the one key of an `mcp` record that is not the record's own
as the policy.

A value outside the policy fails the load at the decorator:

```
src/orders.schema.ts:24:9: invalid @mcp config: invocationPolicy "always" is not one of "auto", "ask"
```

The data forms' JSON Schema lists the key with its values as an enum, and
a policy on a hidden operation fails the load in every form.

## `@icon` on an operation

`@icon(name)` from `@superschematic/api` names the glyph of the
operation's tool. It writes `FieldDef.icon`, the same IR field a field's
`@icon` writes. The name is non-blank with no surrounding whitespace; the
core accepts any name. An operation takes at most one `@icon`, and a
visible tool without one has no icon.

## What the api generator checks

When the `api` generator builds an API it resolves each operation's record:
a visible tool gets `name` from the `@docs` title, `description` from the
`@docs` description and `icon: { name }` from `@icon`, and keeps its
invocation policy, or gets the policy's default when IR that did not come
through the loader has none; a hidden record keeps its reason. The schema's
own record is not changed. A policy under another key, or with a value the
policy does not allow, fails the build, also when a tool hook left it. It
then fails the build when two visible tools of the API:

- declare the same handle:
  `apigen: MCP handle collision: "get_order" is declared by both order.listOrders and order.getOrder`;
- or have display names that differ only by case:
  `apigen: MCP display name collision: "Get an order" is generated by both order.listOrders and order.getOrder`.

Hidden operations take no part in either check. Tools of different APIs
are the consumer's to reconcile.

## The tool documents

An API whose config enables a TypeScript, Go or Rust SDK gets a `tools/`
directory next to the SDK. Every operation is a tool entry there; `@mcp`
decides what is published to a model.

| File | TypeScript | Go | Rust | What it holds |
| --- | --- | --- | --- | --- |
| `tools/schema.json` | yes | yes | yes | every operation: its classification, `@docs` facts, replay contract, arguments and return shape |
| `tools/mcp-audit.json` | yes | yes | yes | one flat record per operation, for review and for a registry digest |
| `tools/mcp-binding.json` | yes | yes | no | how each tool argument reaches the TypeScript SDK method |
| `tools/openai.json`, `tools/anthropic.json` | yes | yes | yes | the visible tools only, in each provider's function format |
| `tools/index.ts` | yes | no | no | the same definitions as TypeScript values, and `invokeTool` |

An operation without `@mcp`, or with a hidden one, is not in the provider
lists. The Go documents are the TypeScript ones with the Go SDK's struct
name in the title; the Rust documents list tools by namespace and name and
use the Rust method names.

### `tools/schema.json`

One entry per operation, with these keys in this order: `name`
(`<namespace>.<method>`), `operationId`, `title`, `mcp` (absent without
`@mcp`), `capability`, `lifecycle`, `visibility`, `audience`, `guidance`,
`replay`, `description`, `namespace`, `methodName`, `httpMethod`,
`httpPath`, `requiresAuth`, `requiredPermissions`, `isScoped`,
`bindingStatus`, `inputSchemaDigest`, `parameters`, `returns`.

```json
"mcp": {
  "hidden": false,
  "name": "Get an order",
  "handle": "get_order",
  "description": "Returns one order by its identifier.",
  "invocationPolicy": "auto",
  "_meta": {"superschematic/operation-guidance": {...}, "ui": {"resourceUri": "ui://orders/detail"}},
  "icon": {
    "name": "receipt"
  }
}
```

- `mcp` is `{ hidden, hiddenReason }` for a hidden operation. A visible
  tool carries its invocation policy after `description`. Its `_meta` is
  the declared `_meta` plus its `@docs` guidance under
  `superschematic/operation-guidance`; `icon` is present when the
  operation has `@icon`, with `family` and `style` when a tool hook set
  them.
- `guidance` is `{ useWhen, doNotUseWhen, success, errors }` from `@docs`,
  every member present (empty strings and `[]` without them).
- `replay` is `{ mode, idempotencyKeyPointers, expectedRevisionPointers }`
  when `@docs` declares a replay mode, else `null`.
- `description` is the `@docs` description; without `@docs` it is the
  operation's comment, with ` (Requires authentication)` appended when the
  route requires it.
- `bindingStatus` is `ready`, or `unsupported_multipart` for an operation
  with a file upload.
- `parameters` is one closed JSON Schema object (`additionalProperties:
  false`) over the path parameters, the query parameters, the input type's
  fields or the scalar arguments, and `publicEncryptionKey` for an
  encrypted endpoint. Nested object types expand to their fields, closed;
  a union is `oneOf` with each member's discriminator pinned; an enum is a
  string whose `enum` lists its serialized values, wherever it appears (an
  argument, a field, a list item, a map value), with `null` among them
  when the property is nullable; a map, as a field or as a body argument,
  is an object whose `additionalProperties` is the value schema (an array
  schema for a map of lists); `Validate<>` bounds carry over. A body field
  that is not required is nullable (`"type": ["string", "null"]`); an
  optional query parameter is left out instead. A property of a schema
  scalar names it under `x-superschematic-scalar`.
- `inputSchemaDigest` is `sha256:` and the hex SHA-256 of `parameters` as
  the generator encodes it (vendor keys first, then `additionalProperties`,
  `type`, `properties`, `required`), so a change to the arguments changes
  the digest.

`ir.ToolManifest` (in the `ir` module) is the Go type of this document and
`ir.ToolBindingManifest` of `tools/mcp-binding.json`. A test in the SDK
generator decodes both with unknown fields refused and checks that
re-encoding gives the same JSON, so a consumer can decode them without its
own copy of the field list.

### `tools/mcp-audit.json`

```json
{
  "schemaVersion": "1.0",
  "apiId": "fixture-mcp",
  "registryDigestInputs": [
    {
      "apiId": "fixture-mcp", "namespace": "order", "operationId": "OrderGetOrderHandler",
      "method": "GET", "path": "/api/orders/{id}", "handle": "get_order",
      "title": "Get an order", "description": "Returns one order by its identifier.",
      "icon": {"name": "receipt"}, "hidden": false, "hiddenReason": "",
      "invocationPolicy": "auto", "requiresAuth": false, "requiredPermissions": [], "capability": "orders.get",
      "lifecycle": "active", "audience": "shoppers", "guidance": {...},
      "replay": {...}, "inputSchemaDigest": "sha256:...", "bindingStatus": "ready"
    }
  ]
}
```

(Shown compact; the file writes one key per line.) Every operation has a
record. An operation without `@mcp` reads `hidden: true` with an empty
reason, an empty invocation policy and `icon: null`, so a reviewer sees
every route and why it is or is not a tool.

### `tools/index.ts`

The TypeScript definitions, one `<Namespace><Method>Params` interface per
tool, and `invokeTool(sdk, name, params)`, which passes the parameters to
the SDK method. A parameter has the type that method takes:

- an enum, object type or union is the types package's type, at its list
  and map shape: `shades: Shade[][]`, `pickup: Address`,
  `swatchByName: Record<string, Swatch>`;
- a field of the input type whose type is a scalar is the input type's own
  field type, `at: PaintInput['at']`, because the types package picks a
  scalar's TypeScript type (`JSDate` for a `Temporal.DateTime`);
- any other parameter is its JSON Schema type (`string`, `number`,
  `boolean`, a list or map of them).

The file imports the types it names from the types package, as the SDK's
namespaces do.

### The replay contract

When the SDK generators build a tool whose `@docs` declares replay
pointers, they resolve each pointer against the tool's `parameters`. Every
segment must name a required argument, every segment but the last an
object with properties; an idempotency key must be a string and an
expected revision a number. Otherwise the build fails:

```
operation OrderOpenReturnHandler replay contract: idempotency pointer "/pickup/postalCode": segment "postalCode" is optional
```

## Rules an extension adds

The core checks shapes. Which schemas must classify their operations,
which icon names exist, and whether a visible tool needs the `@docs`
guidance keys are a distribution's rules. It registers them with
`Registry.RegisterCheck`: a `CheckSpec` whose `Verify` walks the
operations and reports what breaks the rule. A check runs on every loaded
schema of its kinds, core kinds included, in every authoring form.

How the tool documents spell their vendor keys, and what an icon set adds
to an icon, is a tool hook: `Registry.RegisterToolHook` with a `ToolHook`
whose `Edit` receives the API schema and a `ToolSet`. The set holds the
keys (`ToolKeys`) and one `Tool` per operation: its namespace, the
schema's operation, and the resolved `mcp` record, which the hook may edit
or replace. Hooks run in registration order, after the api generator
resolves the records and before it checks them for collisions, and every
SDK language reads what they leave.

| `ToolKeys` field | Default | Written as |
| --- | --- | --- |
| `Scalar` | `x-superschematic-scalar` | the key of a property's scalar name; empty leaves it out |
| `Guidance` | `superschematic/operation-guidance` | the `_meta` key of a visible tool's guidance; empty leaves it out |
| `Parameters` | none | key/value pairs at the root of every argument schema: first in the digest input, after `additionalProperties` in the documents, and as literal types in `tools/index.ts` |

A `Parameters` key may not be empty, repeat, or reuse `type`,
`additionalProperties`, `properties`, `required` or the scalar key; a hook
that adds or removes tools fails the build, as does a hook error, which
names the hook. The `ir` wire types spell the default keys: a distribution
that renames them decodes its documents with its own types.

A distribution's own invocation policy is
`Registry.RegisterToolInvocationPolicy` with a `ToolInvocationPolicy`:

| Field | Core default | Rule |
| --- | --- | --- |
| `Extension` | | the registering extension; required |
| `Key` | `invocationPolicy` | a letter, then letters, digits or underscores; not a key `@mcp` already uses (`handle`, `hidden`, `hiddenReason`, `reason`, `_meta`, `name`, `description`, `icon`) |
| `Values` | `auto`, `ask` | at least one; lowercase letters, digits, `_` and `-`, starting with a letter; each once; in the order `tools/index.ts` lists them |
| `Default` | `auto` | one of `Values` |

A registry holds one policy: a second registration fails assembly and
names both extensions. With it registered, `@mcp` and the data forms take
its key and not the core's, a visible tool without the key gets its
default, and the IR and every tool document write its key where the core
writes `invocationPolicy`. The position of every line is the same, so a
distribution that already writes a policy under its own key keeps its
bytes. A tool hook may change a tool's value to another value of the
policy.

The TypeScript loader type-checks schema files, so the key must
type-check too. `@superschematic/api` exports `MCPToolOptions`, the
options of a visible tool's `@mcp` besides its handle and `_meta`, for
module augmentation from the extension's authoring package:

```ts
import "@superschematic/api";

declare module "@superschematic/api" {
  interface MCPToolOptions {
    readonly confirm?: "never" | "always";
  }
}
```

A schema whose program does not include the augmentation fails the load
with `'confirm' does not exist in type 'MCPConfig'`. The core key stays in
the type: under an extension's policy it type-checks and fails the load
as an unknown key.

`examples/acme-schematic/ext/mcp.go` does all three: it requires `@mcp` on
every operation of acme's shop API; its hook writes `x-acme-scalar`,
`acme/operation-guidance` and `x-acme-arguments: 1` and gives every tool
icon acme's family and style; and it registers `confirm`, `"never"` or
`"always"`, `"never"` by default, with the augmentation in
`packages/schema/src/mcp.ts`.
