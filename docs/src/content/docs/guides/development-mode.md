---
title: Development mode
description: Work out a schema against sample rows. Development mode keeps a project's tables in fixed tables of JSON, in a SQLite file or in memory, so a publish takes effect at once and may rename, retype, remove and tighten.
sidebar:
  order: 6.5
---

Development mode is for working out a schema. You change the schema every
few minutes, make a handful of sample rows, and look at whether the shape
is right. A publish takes effect at once. It needs no migration, no
generator and no compiler.

It is one of three modes (D117 in
[docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md)).
Development keeps rows as JSON. Runtime gives each type its own table,
whose DDL a publish plans. Compiled is the build: generated code, CI and a
deploy. The schemas are the same in all three, so a schema you work out
here moves on to the other two unchanged.

Development mode is not where an application's data goes. It promises a
table's answers, not a table's speed: every list reads every row of its
type. Nothing refuses you for putting real data in it, but nothing tunes
it for volume either.

## What is built

Development mode is two Rust crates in the engine's Cargo workspace,
`runtime/engine/rust`. Nothing is published yet, so a server depends on
them by path from a checkout.

- `superschematic-rules-handler` is the request handler. It is pure: a
  request and its caller go in, and the reads it needs, a response and the
  writes to apply come out. It does no I/O, as the behaviors' core it
  embeds does none.
- `superschematic-rules-dev` is its store over SQLite: a file, or a
  database in memory that goes with its process. With its `postgres`
  feature it keeps the same tables in a schema of a Postgres database
  (`Dev::postgres(url, schema)`).

The library in each language, the prebuilt image, the event stream, MCP
and the access policy are later steps.

```rust
use serde_json::json;
use superschematic_rules::Caller;
use superschematic_rules_dev::Dev;
use superschematic_rules_handler::Request;

let mut dev = Dev::open("notes.db")?;            // or Dev::memory()
let me = Caller::new("alice", ["orders"]);
let order = json!({
    "name": "Order",
    "fields": [{ "name": "title", "typeRef": { "name": "string" }, "required": true }],
    "behaviors": [{ "name": "Workflow", "field": "status", "config": {
        "states": ["draft", "paid"],
        "transitions": [{ "from": "draft", "to": "paid" }]
    }}]
});
dev.request(&me, &Request::Define { document: order })?;
dev.request(&me, &Request::Publish { type_name: "Order".into(), discard: false })?;
let row = dev.request(&me, &Request::Create {
    type_name: "Order".into(),
    data: json!({ "title": "Desk" }).as_object().cloned().unwrap(),
    params: Default::default(),
})?;
// row is { "id": "...", "title": "Desk", "status": "draft", "statusEnteredAt": "...", "_version": 1 }
```

## Schemas

A schema is a document in the JSON data form `superschematic format
--to=json` writes. Its instance type is the type named like the document,
or its only type. A document may also be the type itself, with `name`,
`fields` and `behaviors`.

- A field's type is a primitive, a builtin scalar the handler knows
  (`Identity.UUID`, `Temporal.DateTime`, `Temporal.Date`, `Generic.Int64`,
  `Generic.JSON` and the like), or a scalar, an enum or a type the
  document declares.
- `required`, `unique`, `default` and the validate rules (`validateMaxLength`,
  `validateMin` and the rest) hold each write, as a table's columns and
  their checks hold them.
- The field marked `key` is the row's key. With none, the key is `id`, a
  UUID the host makes.
- A field with `relation` holds its target's key under `<field>Id`
  (`projectId`). Its target must be a published type.
- A behavior entry with `field` is anchored on that field, as
  `status: Workflow<{ ... }>` is. One with no field is anchored on the
  type, as `implements` is. A General schema's behaviors read this way, so
  `@behavior` documents run unchanged.

A behavior's fields read as a table's columns do. A field use's value is
the field itself (`status`), and its other fields follow the field's name
(`statusEnteredAt`). A type use's fields follow the behavior's storage key
(`workflowStatus`). Only the behavior's operations change them.

The behaviors ported onto the core run here: Workflow, Reactions,
Constants, StateRules, Approvals, Timers, Lease, Presence, Budget, Retries,
Seats, Reconcile, Schedules, Pool, Rollups, Dependencies, Numbering,
Revisions, Derived and Provenance. A read serves no Rollups value yet. Comments and Blueprint are
not ported yet, so `examples/engine-notes` and `examples/engine-jobs`
still run on the engine.

- Numbering's counts are the type's as a whole, not a row's. A create
  reads its count and writes it held to the version it read, so two
  creates never take one number. A delete leaves a gap.
- Revisions' `approve` writes the row's own fields. The write is held to
  the type's fields, relations and unique keys as an update is, so a
  patch the type refuses is kept as a proposal and its approval is
  refused with `422 invalid_instance` or `409 conflict`.
- Derived's pulls and settles run on the type as a whole. A derived
  field's output, the JSON Schema its value is held to, comes from the
  type's fields: an enum's values and a primitive's type.
- Provenance's `inReview` is an owned field of each row, which a list
  filters on as `Provenance.inReview`.

## Requests

| Request | What it does |
|---------|--------------|
| `define` | Reads a document and keeps it as the type's draft. The core checks every behavior's config. |
| `publish` | Makes the draft the type's next version. See below. |
| `describe` | The live version: the key, each field with the filters and sorts it takes, the owned fields, each use with its operations, sub-tables and events. |
| `get`, `create`, `update`, `delete` | A row. An update is a merge patch. |
| `list` | A page of rows a filter keeps, in a sort's order. |
| `operation` | A behavior's operation of a row. |
| `tableOperation` | A behavior's operation of the table as a whole, such as Pool's `pick`. |

The host's runner hands the handler the deadlines that fall due and the
writes a behavior hears, each as a request of its own. `Dev` runs them
after each request, or when you call `run_due` with `Drain::WhenAsked`.

## A table's answers

- A unique field refuses a second holder, with `409 conflict`. A unique
  key with a field of no value holds nothing, as NULLs are distinct.
- A relation refuses an id no row has. Deleting its target deletes the
  rows that point at it, as the foreign key sqlgen writes does
  (`CASCADE`). A relation's `onDelete` may say `SET NULL` or `RESTRICT`.
- A list's filter takes the ORMs' grammar: each field's operations (`eq`,
  `ne`, `in`, `notIn`, `ilike`, `gt`, `gte`, `lt`, `lte`, `between`,
  `isNull`), a relation's name holding its target's filter, `allOf` and
  `anyOf`. `ne` and `notIn` keep a row whose field holds no value.
- A sort orders by fields, a field with no value last, and the key breaks
  ties. A page's `next` is the cursor of its last row.

## Publishing with no compatibility rule

A publish in development mode may rename, retype, remove and tighten. The
engine's rule that a new version may only add does not apply.

- A field the version removes goes, and so do its values. A rename is a
  removal and an addition.
- A use the version removes goes with its owned fields, its sub-table rows,
  its shared tables' rows and its deadlines. Numbering added again counts
  from 1.
- A stored row the version no longer describes fails it: a value of the
  wrong type, a required field with no value, a value past a bound, a
  relation to a row that does not exist, a unique value two rows hold, or
  no value for a value field a new use adds.

A publish that leaves a row failing changes nothing. It answers
`409 conflict`, class `StoredRowsError`, and lists each row with what it
fails:

```json
{
  "code": "conflict",
  "class": "StoredRowsError",
  "message": "type Note: version 4 does not describe 1 stored row: n1 (/title: title must be at most 5 characters long); publish it with discard to delete them",
  "details": { "version": 4, "rows": [{ "type": "Note", "id": "n1", "issues": [{ "path": "/title", "rule": "maxLength", "message": "title must be at most 5 characters long" }] }] }
}
```

Publish again with `discard: true` to delete those rows in the publish's
transaction, with the rows their delete reaches through a relation. The
answer lists what it discarded.

## The layout

The store creates its tables when it opens, and none after:
`dev_schema_versions`, `dev_schema_drafts`, `dev_instances` (a schema
column, the own fields as one JSON column and the owned fields as
another), one `dev_sub_<behavior>` per behavior that keeps sub-tables of
a row, `dev_shared` (the rows of the tables a use keeps for the type as a
whole, such as Numbering's counts, each with a version of its own),
`dev_deadlines`, `dev_events` and `dev_changes`. The crates' tests replay
every recorded scenario of the ported behaviors over it, on SQLite, on
Postgres and in memory.
