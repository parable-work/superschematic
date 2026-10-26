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
writes (the Go server embeds it in `openapi.go`, the Rust server in
`src/openapi.rs` and the TypeScript package in `openapi.ts`, and each
serves it at `GET /api/openapi.json`):

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

### `@uiControl` on a field

From `@superschematic/schema`, `@uiControl(control)` names the control a
schema form draws for the field, in place of the one it picks from the
field's type and `Validate` limits. It writes `FieldDef.uiControl`; empty
means the form picks.

```ts
import { uiControl, type Validate } from "@superschematic/schema";

export abstract class ReportSettings {
  @uiControl("radio")
  day: Weekday;

  @uiControl("slider")
  level: Validate<Generic.Int64, { min: 1; max: 10 }>;
}
```

| Control | For |
| --- | --- |
| `slider`, `number-field` | a number; a slider needs a finite minimum and maximum |
| `switch`, `segmented`, `radio`, `select`, `combobox` | an enum; a switch needs two values that read as on and off |
| `checkboxes`, `combobox` | an array of an enum |
| `input`, `textarea` | a string |

The `UIControl` type refuses another value in a `.schema.ts` file, and
`ir.ValidateUIControl` refuses it in every form, the JSON and YAML forms
included (`invalid field @uiControl: unknown control "dial"; ...`). It
appears at most once on a field. A form ignores a control that does not
fit the field and picks its own. Like a placeholder it is a form hint: no
generated type, validator or wire format changes, and of the schema
runtimes only the TypeScript runtime's `parseSchemaIR` carries it, to
`FieldDef.uiControl`.

### `@display({ multiline })` on a field

`@display` from `@superschematic/schema` also goes on a field, where it
says how a UI renders the field's control. `multiline: true` asks for a
control that holds several lines, and `multiline: false` for one line,
whatever the field's `maxLength`:

```ts
import { Validate, display } from "@superschematic/schema";

export abstract class WriteReviewInput {
  @display({ multiline: true })
  body: Validate<string, { maxLength: 5000 }>;
}
```

| Decorator | Writes | Rule |
| --- | --- | --- |
| `@display({ multiline })` | `FieldDef.display` (`ir.FieldDisplay`) | a boolean; `@display({})` is an error; the field holds text (`string`, or a scalar whose values are strings) or a list of text, not a map, and is not `Secret` |

A field takes one `@display`, and an operation or an argument takes none.
Without the hint, a UI makes its own choice: the Topcoat forms make a
textarea of text whose `maxLength` is 500 or more, and an argument keeps
that rule, since arguments take no decorators. The JSON and YAML forms
write the argument under the field's `display` key, which
`superschematic json-schema` checks with the decorator's schema. The
loader refuses the hint on a field that holds no text, or on a secret
one, in every form:

```
src/notes.schema.ts: type NoteInput: field count takes @display multiline, which is for text, but it is Generic.Int64
src/login.schema.ts: type LoginInput: field password takes @display multiline, but it is secret, a password input
```

It is presentation only: no generated type, validator or wire format
changes, and the OpenAPI document and the tool manifests leave it out.

### `@display({ currency })` on a field

An amount is a whole number of a currency's minor units: 450 for GBP 4.50.
`@display({ currency })` says which currency a field's amount is in, so
a UI can show it as money and take it as a decimal. It is a code, or
another field of the same type that holds one:

```ts
import { Nullable, display } from "@superschematic/schema";
import { Finance, Generic } from "superscalar";

export abstract class InvoiceView {
  @display({ currency: "GBP" })
  totalCents: Generic.Int64;

  @display({ currency: { field: "currency" } })
  dueCents: Generic.Int64;
  currency: Nullable<Finance.Currency>;
}
```

| Decorator | Writes | Rule |
| --- | --- | --- |
| `@display({ currency })` | `FieldDef.display.currency` (`ir.DisplayCurrency`) | the field holds an integer scalar (`Finance.Money`, `Generic.Int64`) or a list of them, not a map; a code is an active ISO 4217 code in capitals; a `{ field }` names another field of the type, by its name or its JSON key, that holds one `Finance.Currency` or text value |

The JSON and YAML forms write a code as a string and a field as
`{ field: currency }` under the field's `display`, beside `multiline`. A
code must be on ISO 4217's active list, as `Finance.Currency` checks it,
so `usd` and a withdrawn code are refused. The loader refuses the hint in
every form:

```
src/price.schema.ts: type Price: field note takes @display currency, which is for an amount in minor units, but it is string
src/price.schema.ts: type Price: field amount takes @display currency "HRK", which is not an active ISO 4217 currency code
src/price.schema.ts: type Price: field amount takes @display currency { field: "money" }, which is not a field of the type (fields: cents, currency)
src/price.schema.ts: type Price: field amount takes @display currency { field: "cents" }, which is Generic.Int64; a currency field holds a single Finance.Currency or text value
```

A `Finance.Money` field without the hint, in a type with exactly one
`Finance.Currency` field, is read as in that field's currency; the hint
wins, and with two currency fields and no hint the amount names none.
Like `multiline` it is presentation only: no generated type, validator
or wire format changes. The Topcoat crate shows such an amount as
`GBP 4.50` and takes it in a decimal control
([Money](/superschematic/guides/topcoat/#money)). The engine's describe
document gives an engine type's field its display as the schema holds it,
a `{ field }` by the other field's JSON key, and leaves the `Finance.Money`
rule to its reader. The engine's own amounts name their currency in their
behavior's config, as
[commerce](/superschematic/guides/commerce/#totals-and-currency) does.

## A title on an enum member

A member's title is its reader-facing label: what a select's option or a
table's cell shows in place of the member's name. TypeScript takes no
decorator on an enum member, so the title is a JSDoc tag on the member,
`@title`, on a line of its own:

```ts
export enum OrderStatus {
  Placed = "placed",
  /** @title Picked and packed */
  Fulfilled = "fulfilled",
  Shipped = "shipped",
  Cancelled = "cancelled"
}
```

The tag's line leaves the member's comment, and the rest of the comment
stays its comment. The record is `EnumValueDef.title`, and the JSON and
YAML forms write `title` on the member:

```yaml
values:
  - name: Fulfilled
    title: Picked and packed
    serializedAs: fulfilled
```

A member without a title is labeled by its name in words ("On hold" for
`OnHold`). The reader reads the tag only from a `/** */` comment on an
enum's member, its own enums' and a dependency's, as the first word of a
line, after the line's `*`. The title is the rest of that line. The
lines after it, up to the comment's next tag or its end, must be blank,
so put the member's comment above the tag. The reader reads every
comment on a member for the tag, and refuses one it would not read as
the member's whole title: a tag in a `//` or `/* */` comment, a tag that
is not the first word of its line, a tag on a member with a second
`/** */` comment, a tag that wraps, an empty tag and a second tag. A word
that only begins with `@title`, such as `@titles` or `@title-case`, is
comment text. Anywhere but on an enum's member, a `@title` line is
comment text. These fail the load:

```
src/orders.schema.ts: enum OrderStatus member OnHold: @title needs the member's title after it
src/orders.schema.ts: enum OrderStatus member OnHold: @title needs a space before the member's title
src/orders.schema.ts: enum OrderStatus member OnHold: @title is given twice
src/orders.schema.ts: enum OrderStatus member OnHold: @title must fit on its line
src/orders.schema.ts: enum OrderStatus member OnHold: @title is read from a JSDoc comment (/** @title … */), not a // or /* */ comment
src/orders.schema.ts: enum OrderStatus member OnHold: @title must begin a line of the member's JSDoc comment
src/orders.schema.ts: enum OrderStatus member OnHold: @title is read from the member's only JSDoc comment, and it has more than one
```

For example, a title that wraps onto a second line fails, where a reader
that stopped at the line's end would have read "Picked and":

```ts
  /**
   * @title Picked and
   *   packed
   */
  Fulfilled = "fulfilled",
```

In every form, a title must be non-blank, start and end with no space,
fit on one line and not contain `*/`, so the TypeScript comment can hold
it. It holds no ASCII control character, a tab included, and no U+2028
or U+2029, which TypeScript reads as line breaks. The JSON Schema of the
data forms says this with one pattern beside `"not": {"pattern":
"\\*/"}`, written with no `\s` or `.`, so Go's, an ECMAScript engine's and
Rust's regular expressions reach one verdict, and `Schema.Validate`
holds the IR to the same rule. A title must also differ, ignoring case,
from every other member's title in the enum, so a select's options can
be told apart. No JSON Schema keyword can say that, so only Go holds it:
`Schema.Validate`, which the loader runs on every form:

```
enum OrderStatus member OnHold: title must be non-blank
enum OrderStatus member OnHold: title must not contain surrounding whitespace
enum OrderStatus member OnHold: title must be one line
enum OrderStatus member OnHold: title must not contain the control character U+0009
enum OrderStatus member OnHold: title must not contain */
enum OrderStatus: members OnHold and Shipped share the title "Shipped"
```

The TypeScript writer (`format`) writes the tag on a line of its own
after the member's comment, and refuses a member's comment that holds
the tag, which the reader would refuse. A title changes no generated
code, and the OpenAPI document and the tool manifests leave it out, as
they leave out a field's title. The engine's schema document carries it.
The Topcoat extension's forms, filters and display components label a
member by its title, and its build log warns when two members of an enum
take one label, such as a title that is another member's name in words
([Pages with Topcoat](/superschematic/guides/topcoat/)).

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
  icon: "ticket",
  summaryFields: ["Workflow.status", "assignee"],
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
| `icon` | the glyph a UI shows beside the type's name, by a name for the app's icon set to draw: a letter or a digit, then letters, digits, `.`, `_`, `:`, `/` and `-` (`receipt`, `mdi:receipt`, `solid/box`); which names a set draws is an extension's check (see [Rules an extension adds](#rules-an-extension-adds)) |
| `summaryFields` | the fields that summarize an instance in a list, in order, each once: the type's own fields, by name, neither secret nor hidden, or fields its behaviors add, by qualified name, the behavior's name, a dot and the field's (`Workflow.status`), as a read returns them under the behavior's name |
| `states` | a label per `Workflow` state: `label`, `activeForm` (the present-progressive form a UI shows while an instance is in the state, "Implementing") and `tone`, at least one of the three |
| `transitions` | a label per `Workflow` transition, by the state it leaves, then the state it enters |

A `tone` is what a state means to a reader, which a UI maps onto its own
colors: `muted` (nothing happens in it), `active` (work is under way),
`success`, `warning` (it needs attention) or `danger`. The set is closed.

Every key is optional, but `@display({})` is an error, and a type takes
one `@display`. Any one key is a display: `@display({ icon: "receipt" })`
alone gives a type its icon. A type's icon is its `@display`'s alone: the
`@icon` on its list operation is that operation's MCP tool's icon. The
icon's rule is one pattern, `^[A-Za-z0-9][A-Za-z0-9._:/-]*$`, in the
JSON Schema and in the IR's check, with no `\s` or `.` that Go's, an
ECMAScript engine's and Rust's regular expressions read apart, so every
form and every engine refuses the same names: a space, a tab, a line
break, a no-break space or a byte order mark anywhere in it fails
(`display icon "a b" is not an icon name (a letter or a digit, then
letters, digits, ., _, :, / and -)`). `states` and `transitions` label the type's `Workflow`
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
TypeScript frontend checks the argument with. It is presentation only,
and adds no field, operation or storage. The core's generators leave it
out; the Topcoat extension's display components read its `noun`,
`plural`, `titleField` and `summaryFields`
([Pages with Topcoat](/superschematic/guides/topcoat/#display-components)),
and its forms a row type's `noun` and `createLabel`, which number a list's
rows and label its add button
([Rows without JavaScript](/superschematic/guides/topcoat/#rows-without-javascript)).
The engine's describe document carries it
for the schema's instance type, its `icon` included, with each field's
title, icon and display
([Engine behaviors](/superschematic/guides/engine-behaviors/#display)).
The Topcoat console's nav shows a type's icon beside its link. The OpenAPI
document and the tool manifests carry no display.

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
