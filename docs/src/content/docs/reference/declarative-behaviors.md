---
title: Declarative behaviors
description: Define a behavior with no Rust, its rules in a fixed vocabulary the behaviors' core interprets, in a rules member of its declaration file; the conditions, field rules, guards, effects, reactions and derived fields; how a host hands the core the declaration; and acme.Rating, the example.
sidebar:
  order: 11
---

A behavior adds fields, sub-tables, operations and rules to a table (D103).
Most behaviors' rules are Rust, in the behaviors' core
(`runtime/engine/rust/rules-behaviors`). A declarative behavior has none.
Its declaration file lists its fields, sub-tables and operations as any
behavior's does, and its `rules` member states its rules in a fixed
vocabulary the core interprets. There are no loops and no expressions: a
rule is a condition, a list of fields, or an effect from a fixed list.

Every host runs a declarative behavior as it runs the core's own: the
adapters in TypeScript, Go and Rust, development mode and the wasm library,
each replaying its transcripts on SQLite and Postgres. A host hands the
core the declaration file whole, and the core checks the rules against the
file before it runs any command. Runtime mode serves one through its core
too; its publish plans the behavior's columns through the compiler, which
must register the behavior, as an extension's binary does.

## A declaration with rules

`acme.Rating`, the acme example's behavior
(`examples/acme-schematic/ext/rating.behavior.json`), keeps one rating
per shopper in a sub-table and derives a count and an average from it.
The file is derived from the behavior's class,
`examples/acme-schematic/ext/rating.behavior.ts`, whose
`@declaresBehavior` gives the same rules in its `rules` option (the
extension guide's "A class that states its rules"); here is the
declaration, shortened:

```json
{
  "name": "acme.Rating",
  "configSchema": {
    "type": "object",
    "required": ["maxStars"],
    "additionalProperties": false,
    "properties": { "maxStars": { "type": "integer", "minimum": 3, "maximum": 10 } }
  },
  "fields": [
    { "name": "ratingCount", "type": "Generic.Int64" },
    { "name": "ratingAverage", "type": "number" }
  ],
  "tables": [
    {
      "name": "ratings",
      "key": "rater",
      "fields": [
        { "name": "rater", "type": "string" },
        { "name": "stars", "type": "Generic.Int64" },
        { "name": "ratedAt", "type": "Temporal.DateTime" }
      ]
    }
  ],
  "operations": [
    {
      "name": "rate",
      "paramsSchema": {
        "type": "object",
        "required": ["stars"],
        "additionalProperties": false,
        "properties": { "stars": { "type": "integer", "minimum": 1 } }
      },
      "writes": true
    },
    { "name": "ratingSummary", "paramsSchema": { "type": "object", "additionalProperties": false } }
  ],
  "rules": {
    "operations": {
      "rate": {
        "params": { "stars": { "lte": { "config": "maxStars" } } },
        "effects": [
          {
            "upsert": {
              "table": "ratings",
              "key": { "caller": true },
              "values": { "stars": { "param": "stars" }, "ratedAt": { "now": true } }
            }
          }
        ],
        "result": { "ratingCount": { "field": "ratingCount" }, "ratingAverage": { "field": "ratingAverage" } }
      },
      "ratingSummary": {
        "result": { "ratingCount": { "field": "ratingCount" }, "ratingAverage": { "field": "ratingAverage" } }
      }
    },
    "derived": {
      "ratingCount": { "count": { "table": "ratings" } },
      "ratingAverage": { "average": { "table": "ratings", "field": "stars", "empty": 0 } }
    },
    "configChange": { "oneWay": { "maxStars": "up" } }
  }
}
```

A call of `rate` holds `stars` to the paramsSchema, then to the rule's
`params` (1 to the config's `maxStars`), upserts the caller's row of
`ratings`, derives `ratingCount` and `ratingAverage` from the rows, and
returns them. A table that composes it gets the owned columns
`acme_rating_rating_count` and `acme_rating_rating_average`, and a
sub-table of ratings, as the layout rule gives any behavior's.

The declaration states the behavior's whole shape. The core holds the rules
to it: an operation the rules name is one it declares, and each declared
operation has rules; a field an effect sets is a declared field with a
type; a table an effect writes is a declared sub-table; a veto a rule
refuses with is a code `vetoes` lists. A declarative behavior keeps state
in its owned fields and sub-tables alone. It takes no create parameters and
no precondition, arms no deadlines, emits no events and implements no
contract; a behavior that needs one is written in Rust.

## Operands

A rule compares and writes values. Each is a literal or one of these
objects, each of one member:

| Operand | Its value |
|---------|-----------|
| `{ "param": "stars" }` | A parameter of the operation. |
| `{ "config": "maxStars" }` | A member of the use's config; a JSON pointer when it starts with `/`. |
| `{ "caller": true }` | The caller's subject. |
| `{ "now": true }` | The command's time, in epoch milliseconds. |
| `{ "field": "ratingCount" }` | A field of the row, as the rules name fields (below). |
| `{ "newKey": true }` | A new key, made from the request's id, for an upsert that adds a row. |
| `{ "literal": { ... } }` | A literal object, which no other operand is. |

An operand with no value (a parameter not given, a field with none) is
none, as null is.

## Fields the rules name

A rule names a field of the row by its name. The core resolves each name
when it checks a use's config, in this order:

1. a field the behavior declares;
2. the value of another use anchored on a field of that name, so `status`
   is a Workflow field's state;
3. an own field of the table, `createdBy` among them when the layout holds
   it.

`<use>.<field>` names an owned field of another use (`Lease.holder`). A
name that resolves to none refuses the config at define.

## Conditions

A condition is the ORM's filter (D90, D99): each member a field and its
tests, ANDed, beside `allOf` and `anyOf`.

```json
{ "status": { "eq": "open" }, "owner": { "ne": { "caller": true } } }
```

| Test | Holds when the field |
|------|----------------------|
| `eq` | has a value equal to the operand's. |
| `ne` | does not, a field with none and an operand with one among them. |
| `gt`, `gte`, `lt`, `lte` | has a value ordered so against the operand's: numbers by value, strings by code point. |
| `between` | is `gte` the first and `lte` the second of a list of two. |
| `in`, `notIn` | is, or is not, equal to one of a list. An empty list filters nothing. |
| `isNull` | has no value (`true`), or has one (`false`). |
| `ilike` | holds the operand's text, in any case. |

`allOf` keeps a condition when every condition of its list holds, `anyOf`
when one of its list does; an empty `anyOf` filters nothing. Values compare
as JSON, numbers by value, so a `5` holds `eq` against a column's `5.0`.

`{ "caller": true }` under `eq`, `ne` and `in` compares the caller with a
field that holds a subject (D104), as a Workflow transition's `by` does: a
`string` field holds the caller whose subject is its text, and a UUID field
the caller whose subject is the same UUID in either of its forms. A field
with no value names no one.

## Field rules

Four members hold the table's own fields, with the rules of Constants and
StateRules, in their words:

| Member | What it holds |
|--------|---------------|
| `constant` | Own fields a create sets for good: an update that changes one is refused at the field (`invalid_instance`, rule `constant`). |
| `oneWay` | Own fields that move one way after the create, by field: `up` or `down`. A move back is vetoed `one_way`. |
| `states` | By state of a Workflow, the own fields a row must hold to enter it (`required`), the ones that do not change in it (`frozen`, a list or `"all"`), and who may change them all the same (`by`). Vetoes `missing_fields`, `frozen_fields` and `required_fields`. |
| `status` | The Workflow use `states` reads, when the table has several. |
| `permission` | The permission a caller needs to change a constant, move a field back or change a frozen one. |

Each takes its list from the declaration, or from the use's config with
`{ "config": "<member>" }`, so a behavior states the rule and its config
the fields. A declaration that uses `oneWay` lists the veto `one_way`, and
one that uses `states` lists the three `states` vetoes.

## Guards

`guards` lists conditions a request must meet:

```json
{ "on": ["flag"], "require": { "owner": { "ne": { "caller": true } } }, "veto": "own_row", "message": "the row's owner does not flag it" }
```

`on` names what it guards: `create`, `update`, `delete`, an operation of
the behavior's own, or `<use>.<operation>`, another use's (`Lease.acquire`,
`status.transition`). `require` is read on the row as it is before the
request, a create's on its new own fields, with the request's parameters
as `{ "param": ... }`. A request that fails it is vetoed with `veto`, a
code the declaration lists, and `message`, its reason; the veto's details
name the action. Guards run after the field rules, in their order.

## Operations

`operations` gives each declared operation's rules:

| Member | What it says |
|--------|--------------|
| `params` | A condition over the parameters, after the paramsSchema. Its operands are literals and members of the config. A test that fails refuses the call as `invalid_argument`, at the parameter, in ajv's words: `lte` as `must be <= 5`. |
| `effects` | What the operation does, in order (below). A read-only operation has none. |
| `result` | What it returns: an object of operands, a member with no value left out, or one operand. Read after the effects. |

## Effects

| Effect | What it does |
|--------|--------------|
| `{ "set": { "<field>": <operand> } }` | Sets owned fields; no value clears one. |
| `{ "adjust": { "<field>": <operand> } }` | Adds a number to an owned field that holds one, none counting as 0. Two integers add exactly. |
| `{ "upsert": { "table", "key", "values" } }` | Adds the sub-table row of the key, or updates the one the key holds. A key from `{ "caller": true }` with no caller refuses the call as `forbidden`. |
| `{ "move": { "to", "status"? } }` | Moves a Workflow use, the table's one or the one `status` names, through its `transition`, so its guards run. |
| `{ "update": { "<field>": <operand> } }` | Writes the row's own fields, held to an update's validation and guards. An operation's alone. |

An operation's effect that cannot act refuses the call: a move Workflow
vetoes is that veto.

## Reactions

`reactions` lists `{ when, then }`. A table that composes the behavior hears
its row's writes, whoever makes them, from its change queue (D103), so its
declaration says `hearsChanges: true`. A reaction runs for the write that
brought the row to meet `when`: the row after the write meets it and the
row before did not. It acts on the row as it is now, and not at all once
the row has moved on. Its `then` is a list of effects, `update` aside, each
acting where it can: a move to the state the row is in, with no transition
to it, or one a guard vetoes leaves the row as it is.

```json
{ "when": { "flags": { "gte": 3 } }, "then": [{ "move": { "to": "closed" } }] }
```

## Derived fields

`derived` gives owned fields their values from a sub-table's rows, by
field:

| Function | Its value |
|----------|-----------|
| `{ "count": { "table", "where"? } }` | How many rows `where` keeps. |
| `{ "sum": { "table", "field", "where"? } }` | The sum of the field over them; 0 for none. |
| `{ "min": ... }`, `{ "max": ... }` | The least or the greatest value. |
| `{ "average": ... }` | Their mean, as JavaScript divides it. |

`where` is a condition over the sub-table's fields, its operands literals
and members of the config. A minimum, a maximum or an average of no rows
has no value, or `empty`'s. A create sets each derived field over no rows,
and each operation that writes the table sets it again in the same change
set, so a read, a report and a filter read the column.

## A config change

`configChange` says how a new version may change a use's config on a table
with rows. Absent, only an identical config. `"any"` allows any.
`{ "oneWay": { "maxStars": "up" } }` lets each member it names move its way
and keeps every other member as it was: a new version may raise `maxStars`,
which every stored rating still fits, and may not lower it. A declarative
behavior that keeps no state (no owned field and no sub-table) allows any
change, and joins and leaves a table with rows.

## Handing a host the declaration

A host's core runs the behaviors the core implements and the declarative
ones it is handed:

| Host | How |
|------|-----|
| Rust | `superschematic_rules_behaviors::core_declaring(&files)`, and `Declarations::ported_with(&files)` for development mode's tables; runtime mode's `Runtime::declaring(&files)`. |
| TypeScript | `@superschematic/rules`' `core.declare(files)`; `@superschematic/rules-handler`'s `handler.declare(files)`. |
| Go | `rules.Declare(files...)`, which the process's native core runs from then on. |
| C ABI | `rules_declare` and `handler_declare`: `{"declarations": [file]}` to `{"declared": [name]}`. |

A file the vocabulary does not read, one that names a behavior the core
implements, and a second file of a declared name that differs are refused
with the reason.

A table may implement a declarative behavior as it implements one the core
runs. Its layout document holds the behavior's declaration in
`declarations`, and the TypeScript and Go repositories hand it to their
core before they check the layout, so a generated ORM runs it with no step
of yours. A generated Rust server builds its core with the core's
behaviors alone, so it does not run one yet. A transcript of a declarative behavior's scenarios holds
its declaration files (`declarations`), which each adapter's replay hands
its core first.

## Constants and StateRules as declarations

Constants and StateRules keep no state of their own, and the field rules
above are theirs. Each is a declaration in
`runtime/engine/testdata/rules/declarations`: its own declaration file with
rules that read the use's config.

```json
"rules": {
  "constant": { "config": "fields" },
  "oneWay": { "config": "oneWay" },
  "permission": { "config": "permission" }
}
```

On a core whose Constants or StateRules is that declaration in place of
the Rust one, every recorded scenario that composes it answers alike and
writes the same transcript (`rules-testkit`'s `declarative_field_rules`).
The Rust behaviors stay registered under their names.
