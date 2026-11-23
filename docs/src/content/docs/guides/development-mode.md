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
`runtime/engine/rust`, and a TypeScript library over the first. Nothing is
published yet, so a server depends on them by path from a checkout.

- `superschematic-rules-handler` is the request handler. It is pure: a
  request and its caller go in, and the reads it needs, a response and the
  writes to apply come out. It does no I/O, as the behaviors' core it
  embeds does none.
- `superschematic-rules-dev` is its store over SQLite: a file, or a
  database in memory that goes with its process. With its `postgres`
  feature it keeps the same tables in a schema of a Postgres database
  (`Dev::postgres(url, schema)`). With its `server` feature it serves the
  handler's routes over HTTP, and its `superschematic-dev` binary is a
  server you run.

- `@superschematic/rules-handler` (`runtime/rules-handler/typescript`) is
  the request handler built for WebAssembly, with the host loop, the same
  store over SQLite, the routes and the event stream in TypeScript. See
  [From TypeScript](#from-typescript).

The libraries in Go and Python and the prebuilt image are later steps.

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
Revisions, Comments, Derived and Provenance. Blueprint is not ported yet, so
`examples/engine-jobs` still runs on the engine, and so does
`examples/engine-notes`, whose reads hold Comments' `composing` and
`unread`, which wait for the core.

A get and a list also show the fields a behavior computes at a read,
which no row holds. They are named as the use's other fields are:
Rollups' `rollupsValues`, Dependencies' `dependenciesBlocked`, Revisions'
`revisionsPendingProposals`, Timers' `timersDeadlines`. Rollups and
Dependencies read other rows to compute theirs, and the read sees them as
they stand. `describe` lists them under `computed`.

- Numbering's counts are the type's as a whole, not a row's. A create
  reads its count and writes it held to the version it read, so two
  creates never take one number. A delete leaves a gap.
- Revisions' `approve` writes the row's own fields. The write is held to
  the type's fields, relations and unique keys as an update is, so a
  patch the type refuses is kept as a proposal and its approval is
  refused with `422 invalid_instance` or `409 conflict`.
- Comments' writes are beside the row: a post, an edit or a reaction
  writes its messages, edits and reactions, and its conversation's row of
  the type's shared table, and moves no version of the row. Its streamed
  replies, read state, inbox, deliveries, search and attachments wait for
  the core.
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
| `operation` | A behavior's operation of a row. With no use named, the one use that declares the operation. |
| `tableOperation` | A behavior's operation of the table as a whole, such as Pool's `pick`. |
| `types` | The live types the caller may read, each with its version. |
| `tools` | The MCP tools the caller may call. See below. |

The host's runner hands the handler the deadlines that fall due and the
writes a behavior hears, each as a request of its own. `Dev` runs them
after each request, or when you call `run_due` with `Drain::WhenAsked`.

## Running it as a server

`superschematic-dev` serves development mode over HTTP. Build it from a
checkout, in `runtime/engine/rust`:

```bash
cargo run -p superschematic-rules-dev --features server --bin superschematic-dev -- --db dev.db
```

| Flag | What it does |
|------|--------------|
| `--db FILE` | The SQLite file. With `--memory`, or with neither, a database in memory. |
| `--listen ADDR` | Where it listens. `127.0.0.1:4500` unless given. |
| `--policy JSON` | The access policy, or `@FILE` for one in a file. `"allowAll"` unless given. |
| `--as SUBJECT` | The caller of a request that names none. `developer` unless given; `--as ""` names none. |
| `--permissions P1,P2` | The `--as` caller's permissions. |
| `--postgres URL --schema NAME` | A schema of a Postgres database, with the `postgres` feature. |

A request's caller is the subject the `X-Superschematic-Subject` header
names, with the permissions `X-Superschematic-Permissions` lists, or the
`--as` caller. With neither, the request is `401 unauthorized`. Anyone who
reaches the server is whoever they say, so keep it on your own machine.

The server runs the queues after each request and the deadlines each
second. A Rust host of its own mounts the same routes with
`superschematic_rules_dev::server::router`, and authenticates as it
likes.

## Routes

The routes are the engine's, under one namespace, `default`. A path that
names another namespace is `404`.

| Method | Path | What it does |
|--------|------|--------------|
| `GET` | `/namespaces/default/schemas` | The live types the caller may read. |
| `POST` | `/namespaces/default/schemas` | Defines a type from a schema document. |
| `GET` | `/namespaces/default/schemas/{type}` | The live version's document. |
| `GET` | `/namespaces/default/schemas/{type}/describe` | The live version as development mode runs it. |
| `POST` | `/namespaces/default/schemas/{type}/publish` | Publishes the draft. `?discard=true` deletes the rows it fails. |
| `GET` | `/namespaces/default/schemas/{type}/instances` | A page of rows. |
| `POST` | `/namespaces/default/schemas/{type}/instances` | A create: `{ "id"?, "data", "behaviors"? }`, `behaviors` being each use's create parameters. |
| `GET` | `/namespaces/default/schemas/{type}/instances/{id}` | A row. |
| `PATCH` | `/namespaces/default/schemas/{type}/instances/{id}` | An update, as `application/merge-patch+json`. |
| `DELETE` | `/namespaces/default/schemas/{type}/instances/{id}` | A delete. |
| `POST` | `/namespaces/default/schemas/{type}/instances/{id}/operations/{operation}` | A behavior's operation of a row. |
| `POST` | `/namespaces/default/schemas/{type}/operations/{operation}` | A behavior's operation of the table. |
| `GET` | `/namespaces/default/events` | The event queue. |
| `POST` | `/namespaces/default/mcp` | The MCP endpoint. |

A success is the envelope `{ "data", "meta": { "requestId" } }`. A row
carries its version as its `ETag`, and a create answers `201` with the
row's `Location`. A refusal is an RFC 9457 problem with the refusal's
`code` and its `details`. An operation is named by its name, which the
type's one use that declares it answers. Where two uses declare it, name
it `<use>.<operation>`. An update, a delete and an operation take the
`Preconditions` header, a JSON object of each use's entry by its id.

A list takes its filters in the query string, as the generated servers
read them:

- `filter[title]=Desk` is equality, and `filter[estimate][gte]=4` names
  an operation: `eq`, `ne`, `in`, `notIn`, `gt`, `gte`, `lt`, `lte`,
  `contains` and `isNull`. A value is read as its field's type. `in` and
  `notIn` take a list separated by commas.
- `where` takes the filter grammar below as JSON, a relation's filter
  and `anyOf` among it.
- `sort=-estimate,title` sorts, `-` descending. `limit` and `cursor` page.

The router has no transport under it
(`superschematic_rules_handler::http`): `route` reads a method, a path, a
query, headers and a body as a request, and `respond` writes the answer.
`Dev::http` runs both in one call.

## From TypeScript

`@superschematic/rules-handler` mounts the handler in a TypeScript server
you write by hand. It is the Rust handler built for WebAssembly, behind a
JSON ABI that imports nothing (`runtime/engine/rust/rules-handler-wasm`).
The module reads no clock and draws no random number: the library gives
each request its time and a fresh UUID. Build it from a checkout, in
`runtime/rules-handler/typescript`, with `bun install && bun run build`,
which needs cargo and the `wasm32-unknown-unknown` target.

```ts
import { devCallers } from '@superschematic/rules-handler';
import { openDev } from '@superschematic/rules-handler/sqlite';

const dev = await openDev('dev.db', {                // or ':memory:'
  authenticate: devCallers({ subject: 'developer' }),
});
const me = { subject: 'alice', permissions: ['orders'] };
await dev.request(me, { kind: 'define', document: order });
await dev.request(me, { kind: 'publish', type: 'Order' });
const row = await dev.request(me, { kind: 'create', type: 'Order', data: { title: 'Desk' } });
// row is { id: '...', title: 'Desk', status: 'draft', statusEnteredAt: '...', _version: 1 }

Bun.serve({ fetch: request => dev.http(request) });
setInterval(() => dev.runDue(), 1000);
```

- `dev.request(caller, request)` runs one request. A request is the
  handler's, with the type as `type` and a use as `use`: `{ kind: 'list',
  type: 'Order', filter, sort, limit }`. Its answer is typed by its kind.
  A refusal rejects with a `RefusalError`, whose `refusal` carries the
  code, the status, the class, the message and the details.
- `dev.http(request)` serves the routes above over the Fetch API's
  `Request` and `Response`. The event stream is a stream of server-sent
  events. `prefix` mounts the routes under a path.
- `authenticate` reads a request's caller. `devCallers()` reads the
  `X-Superschematic-Subject` and `X-Superschematic-Permissions` headers,
  with an optional caller for a request that names none. Without a caller
  a request is `401`.
- The change queue runs after each request. Deadlines fall due on the
  clock, so call `dev.runDue()` on a timer, or pass `drain: 'whenAsked'`
  and call it yourself.
- `policy` takes the access policy above, in the same JSON.
- `dev.tools(caller)` and `dev.callTool(caller, name, args)` are the MCP
  tools. The MCP endpoint needs a transport: pass `mcp` with a `serve`
  that answers a request over the caller's tools, such as `serveMcp` of
  `@superschematic/http-runtime/mcp`. Without one, the endpoint is `404`.

The store keeps the tables `superschematic-rules-dev` keeps, so a file
either host wrote, the other opens. `@superschematic/rules-handler/sqlite`
opens it on `bun:sqlite`. The package's entry imports nothing and runs in
Bun, Node, Workers and a browser: `new SqliteStore(connection, layout)`
takes any synchronous SQLite connection whose statements bind positional
parameters, such as `node:sqlite`'s `DatabaseSync`, and `new Dev(handler,
store)` serves it. `serve` and `runDue` are the host loop alone, over a
store of your own.

A read carries a column's JSON text into the handler as it is, so a number
wider than a double reaches it unrounded. A write's values come back
through `JSON.parse` unless you pass a `codec` that keeps them.

`runtime/rules-handler/typescript/examples/serve.ts` is a server on
`Bun.serve`. The package's tests replay every rules transcript through the
library, run `superschematic-rules-dev`'s store session to the same
answers, and read SQLite files across the library and `superschematic-dev`
both ways.

## The access policy

The handler asks an access policy before each request a caller makes. It
is the engine's policy, in the same JSON, so a policy written for the
engine works here:

- `"allowAll"` allows everything. It is the default.
- `"permission"` allows an action on a type to a caller holding
  `<type>.<action>`, such as `Order.write`.
- `{ "rules": [...] }` is a list of rules. The first rule that matches a
  question decides it, and a question no rule matches is refused.

```json
{"rules": [
  {"subject": "mallory", "allow": false},
  {"action": ["define", "publish"], "require": "schemas.{action}"},
  {"action": "write", "operation": "transition", "require": "{schema}.move"},
  {"require": "{schema}.{action}"}
]}
```

A rule matches on `action`, `operation`, `schema`, `namespace` and
`subject`, and decides with `allow` or `require`. In `require`,
`{schema}`, `{action}`, `{namespace}` and `{operation}` stand for the
question's.

- `define` and `publish` ask about the type they name. A define asks once
  its document reads.
- `describe`, a get, a list and a read of the event queue ask `read`.
- A create, an update and a delete ask `write`.
- A behavior's operation asks `write` or `read`, as its declaration says,
  and names itself as `operation`.

The policy is asked before anything is read, so a type that does not
exist is refused to a caller who may not read it. A refusal is
`403 forbidden`, in the engine's words: `bob may not write Order in
namespace default`. The behaviors' own permissions still apply after the
policy allows a request. Their reads of other types are not asked of the
policy, as a compiled server's are not.

With `Dev`, pass it with `with_policy`. With the server, pass `--policy`.

## The event stream

The events behaviors emit, such as Schedules' `run`, are kept in order.
`GET /namespaces/default/events` reads them as a page,
`{ "events", "next", "more" }`. It takes `after` (a cursor, or `head`),
`schema`, `instanceId`, `behavior`, `kind` and `limit`. A read of every
type keeps only the types the caller may read.

With `Accept: text/event-stream` it is a stream of server-sent events.
Each event is one message: `id:` is its cursor and `data:` the event. Once
the stream has caught up it sends a `ready` event, then each event as it
is written, and a heartbeat comment while nothing comes. A client that
reconnects sends `Last-Event-ID` and resumes after it.

## MCP

`POST /namespaces/default/mcp` is an MCP endpoint over the streamable HTTP
transport, stateless, as the generated servers serve theirs. Its tools are
made from the live types, as the engine makes its own:

- `<type>_create`, `<type>_get`, `<type>_list`, `<type>_update` and
  `<type>_delete`, such as `order_create`;
- one tool per operation a type's uses declare, such as
  `task_transition`;
- `list_schemas`, `describe_schema`, `define_schema` and
  `publish_schema`, so an agent can work a schema out with you.

The engine has no tool that publishes. Development mode has one, since
publishing is what it is for. A tool the access policy refuses the caller
is not listed. A call that is refused answers a tool error whose
structured content is the problem.

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
