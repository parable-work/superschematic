---
title: Engine behaviors
description: Compose the engine's behaviors on a type in TypeScript or JSON; read their fields under each behavior's name; engine types in a DB schema and their engine document; label a type's instances, states and transitions with @display; create parameters; schema-level operations; refusals with codes and preconditions on writes; the runner that runs reactions and schedules; the outcomes of Workflow's terminal states; and the core's Dependencies, Links, Rollups, Search, Reactions, Constants, Variants, Branches, Timers, StateRules, Numbering, Approvals, Derived and Provenance behaviors.
sidebar:
  order: 7
---

[The engine](/superschematic/guides/engine/) guide builds a notes server
with `Workflow`, `Comments` and `Revisions`. This page covers the rest of
the behaviors the engine runs with no extension, and the mechanisms the
newer ones lean on: parameters a create gives its behaviors, operations
that run on a whole schema, and the runner that does work after a change
commits.

The
[engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#core-behaviors)
is the full reference: every refusal, event and config change of each
behavior. The examples below come from it and from the engine's tests,
which CI runs on Node.js and Bun.

## Every behavior at a glance

The core declares twenty-eight behaviors, so every binary's meta-schema
admits a schema that composes them. The engine implements seventeen
itself and registers them when it opens. Seven are claimable work, the work
queue's Rust behaviors, which the engine's addon is built with; the
engine refuses a schema that composes one until a deployment names them
in the engine's `behaviors` option (`workQueueBehaviors`, from
`@superschematic/engine-workqueue`). The last four, `Stock`, `Calendar`,
`Holds` and `Payments`, are commerce (D70): the core declares them with
the package `@superschematic/engine-commerce`, the core's addon carries
them and a project's engine crate links them, and an engine runs them
once a deployment names them (`commerceBehaviors`), as a generated
server does.

| Behavior | What it adds | Requires | Covered in |
| --- | --- | --- | --- |
| `Workflow` | a `status` that moves only along declared transitions, each optionally behind a permission, and that a list filters on; `enteredAt`, when it last moved; an [outcome](#outcomes) for each terminal state | | [The engine](/superschematic/guides/engine/#workflow) |
| `Comments` | comments on the instance | | [The engine](/superschematic/guides/engine/#comments) |
| `Revisions` | a revision at every change, and an optional propose, approve and reject step | | [The engine](/superschematic/guides/engine/#revisions) |
| `Dependencies` | blockers that hold a transition until they finish | `Workflow` | [Dependencies](#dependencies) |
| `Links` | named links to instances of other schemas, optionally pinned to a revision or a release | | [Links](#links) |
| `Rollups` | values computed from the instances that link here, optionally gating the type's Workflow transitions | | [Rollups](#rollups) |
| `Search` | full-text search over the type's text fields, vector search over embeddings an outside embedder computes, and a search across a namespace's schemas | | [Search](#search) |
| `Reactions` | rules that move statuses after a change commits | `Workflow` | [Reactions](#reactions) |
| `Constants` | fields that keep the value their create gives them, unless a caller holds the config's permission | | [Constants and Variants](#constants-and-variants) |
| `Variants` | a JSON field typed by another field's value | | [Constants and Variants](#constants-and-variants) |
| `Branches` | a version graph on each instance: drafts that merge into a primary line, commits and releases, the latest in its `release` field | | [Branches](#branches) |
| `StateRules` | own fields a Workflow state requires to enter it, and ones it freezes while an instance is in it | `Workflow` | [StateRules](#staterules) |
| `Approvals` | sign-offs by distinct principals that a move into a state waits for, consumed by a change of the instance | `Workflow` | [Approvals](#approvals) |
| `Numbering` | a number a person reads, `INV-2026-0042`, set at the create from a count per schema, per scope target and per year, that a list filters on | | [Numbering](#numbering) |
| `Timers` | named deadlines from the create, an entry into a state or a date-time field, which the runner fires once due, moving the status | `Workflow` for a deadline that names a state | [Timers](#timers) |
| `Derived` | own fields a model outside the engine fills from other own fields, under an instruction the schema holds, which a deriver pulls and settles by hash; a person's write wins | `Workflow` for a field that names states | [Derived](#derived) |
| `Provenance` | who set each own field last and through what, with the confidence the writer gave, and a review that holds a source's writes until a person accepts them | `Workflow` for a gate | [Provenance](#provenance) |
| `Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget`, `Retries` | claimable work: leases, claims, worker heartbeats, stamped children, budgets and retries | varies | [Work queues](/superschematic/guides/work-queues/) |
| `Stock`, `Calendar`, `Holds`, `Payments` | commerce: units a supply counts, time it opens, a claim's holds on either through the supply contract, and what a claim owes and how a provider settles it | `Workflow` for `Holds` | [Selling and booking](/superschematic/guides/commerce/) |

A running engine lists the behaviors it runs, an extension's included,
at `GET /behaviors`, and returns one's declaration at
`GET /behaviors/{name}`: the JSON Schema of its config, its create
parameters and preconditions, the behaviors it requires and conflicts
with, its fields, its operations with their scope, parameters, result
and invocation policy, and its veto codes. An MCP client calls
`list_behaviors` and `describe_behavior` for the same, before it writes
a draft with `define_schema`. Every caller may read them
([the behavior catalog](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#the-behavior-catalog)).

Once a type composes behaviors, each says what its config means where
a client picks a tool. The describe document gives each behavior's
summary beside its config, and every tool of the type carries guidance
in the members an SDK tool's `@docs` fills (`useWhen`, `doNotUseWhen`,
`success`, `errors`), in its MCP `_meta` too: `transition`'s names the
moves from each state, the permission each needs and the vetoes it can
meet, `blocked` from `Dependencies` among them
([guidance](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#guidance)).

## Compose a behavior

A type composes behaviors in its `behaviors` list, each with its config.
In the JSON data form the engine reads:

```json
"behaviors": [
  { "name": "Workflow", "config": {
      "states": ["todo", "doing", "done", "dropped"],
      "transitions": [
        { "from": "todo", "to": "doing" },
        { "from": "doing", "to": "done" },
        { "from": "todo", "to": "dropped" },
        { "from": "doing", "to": "dropped" } ] } },
  { "name": "Dependencies", "config": { "schemas": ["tasks", "documents"], "gatedStates": ["done"] } },
  { "name": "Links", "config": { "links": {
      "spec": { "schema": "documents", "pinned": true },
      "parent": { "schema": "tasks" },
      "project": { "schema": "projects", "required": true } } } }
]
```

In a TypeScript schema file, the same type uses `@behavior(name, config)`
from `@superschematic/schema`. `BehaviorConfigs` types each config, so an
unknown behavior name or a wrong config key fails the type check:

```ts
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "doing", "done", "dropped"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "doing", to: "done" },
    { from: "todo", to: "dropped" },
    { from: "doing", to: "dropped" },
  ],
})
@behavior("Dependencies", { schemas: ["tasks", "documents"], gatedStates: ["done"] })
@behavior("Links", {
  links: {
    spec: { schema: "documents", pinned: true },
    parent: { schema: "tasks" },
    project: { schema: "projects", required: true },
  },
})
export abstract class Task {
  title: Validate<string, { maxLength: 200 }>;
}
```

The decorators apply in source order, and a type lists a behavior once.
The loader holds each config to the behavior's declared config schema
when the schema loads, and
[`superschematic format --to=json`](/superschematic/reference/cli/) writes
the JSON form to hand to the engine. A behavior an extension adds joins
`BehaviorConfigs` by module augmentation;
[Write an extension](/superschematic/extending/write-an-extension/#a-behavior)
shows how.

## Behavior fields

A read returns an instance's own fields in `data` and its behaviors'
fields in `behaviors`, under each behavior's name, so a field a behavior
adds never collides with one of the type's own or with another
behavior's:

```json
{ "id": "t1", "seq": 3,
  "data": { "title": "Build", "status": "draft" },
  "behaviors": {
    "Workflow": { "status": "doing", "enteredAt": 1790790700324 },
    "Dependencies": { "blocked": false },
    "Links": { "targets": { "project": { "schema": "projects", "id": "launch" } } } } }
```

Here the type declares a `status` of its own beside Workflow's, and each
reads apart. A behavior's fields are the ones its declaration lists, and
no config renames one, so every type that composes a behavior reads the
same.

- **Writes.** A create, an update and an operation's `update()` take the
  own fields only. A behavior's field changes only through its
  operations; its name in a patch is an unknown field, as any name the
  type does not declare.
- **Qualified names.** Where a string names a behavior's field, it is the
  behavior's name, a dot, and the field's: `Workflow.status`,
  `Lease.holder`, `acme.Rating.ratingCount`. A list filters on one
  (`where: { "Workflow.status": "doing" }`), a rollup counts by one
  (`"field": "Workflow.status"`) and a display summarizes with one. An
  own field's name never holds a dot.
- **Every entry.** Each behavior on the type that declares a field has an
  entry, `{}` while none has a value; a field with no value is absent.
  A behavior that adds no field, such as `Search` or `Constants`, has
  none.
- **Volatile fields.** A field the engine computes at a read, from other
  instances, the clock or the config, moves with no event of its own
  instance: `Dependencies.blocked`, `Links.targets`, `Rollups.values`,
  `Lease.active` and `Budget.meters`. Its declaration says
  `"volatile": true`, and so does the describe document.
- **The log.** An event's change is a merge patch of `{ data, behaviors }`:
  a create the whole instance, an update its patch under `data` and what
  its behaviors' fields took under `behaviors`.

### In the generated types

The `types` output types what a read returns under `behaviors`. For each
type that composes behaviors, each language's types write
`<Type>Behaviors` beside the type itself, which stays the own fields, the
read's `data`. It has a member per behavior that declares a field, under
the behavior's name, holding those fields as the type's config narrows
them, as [`engine-client`](/superschematic/reference/cli/#engine-client---out-filets-schema-file)
types them: Workflow's status is one of the type's states, Links' targets
holds each link of the config with its target's schema, Timers' deadlines
each deadline of the config. A field a read can leave out is optional.
For the `Task` above, in TypeScript (`types/behaviors.ts`):

```ts
/** A state of a Task's Workflow. */
export type TaskState = 'todo' | 'doing' | 'done' | 'dropped';

export interface TaskBehaviors {
  readonly Workflow: {
    readonly status: TaskState;
    readonly enteredAt?: number;
  };
  readonly Dependencies: {
    readonly blocked: boolean;
  };
  readonly Links: {
    readonly targets: {
      parent?: { id: string; schema: 'tasks' };
      project: { id: string; schema: 'projects' };
      spec?: {
        id: string;
        latest?: number;
        revision?: number;
        schema: 'documents';
        stale?: boolean;
      };
    };
  };
}
```

and in Go (`behaviors.go`), where each object a field holds is a struct
named by its path:

```go
type TaskBehaviors struct {
	Workflow     TaskWorkflow     `json:"Workflow"`
	Dependencies TaskDependencies `json:"Dependencies"`
	Links        TaskLinks        `json:"Links"`
}

type TaskWorkflow struct {
	Status    TaskState `json:"status"`
	EnteredAt *int64    `json:"enteredAt,omitempty"`
}
```

Rust's `src/behaviors.rs` writes the same structs with serde, and
Python's `behaviors.py` Pydantic models, with `from_json` and
`to_json_dict` on `<Type>Behaviors`. Where a language's names cannot hold
a behavior's name or a config's key, the member takes it in PascalCase,
each run of letters and digits capitalized and the runs joined
(`acme.Rating` is `AcmeRating`, a state `in-progress` is `InProgress`),
snake_case for a Rust or Python field (`acme_rating`), and the JSON keeps
the exact name. A behavior with no narrowing, an extension's, types its
fields as any JSON, and so do Go, Rust and Python for a value that is one
of several types, such as a rollup's (`{ "over": true }` past its limit).

A General schema whose types compose behaviors builds: its types write
`<Type>Behaviors`, and nothing else of its build reads a behavior. A DB
schema's builds too, and gets an engine document (below). An API's own
type cannot compose a behavior, and the API generators do not serve an
engine type yet, so an API that imports or projects one fails its build,
naming the first generator that would run
([`build`](/superschematic/reference/cli/#build-service-dir)).

## Engine types

A type of a DB schema that composes a behavior is an engine type (D58).
Its instances are the engine's, kept in the engine's own tables: in a
stack, the DB service's database, under a Postgres schema of the
engine's (`engine` unless configured), where the generated servers open
the engine. So `sql` writes no table for it, `orm`
no repository, and a migration plan (D27) never creates, alters or drops
one. Tables and engine types share one schema:

```json
"Customer": { "name": "Customer", "role": "DBTable", "fields": [ ... ] },
"Address": { "name": "Address", "role": "DBTable", "jsonField": true, "fields": [ ... ] },
"Ticket": {
  "name": "Ticket", "role": "DBTable",
  "behaviors": [
    { "name": "Workflow", "config": { "states": ["open", "resolved"], "transitions": [{ "from": "open", "to": "resolved" }] } },
    { "name": "Links", "config": { "links": { "account": { "schema": "Account", "required": true } } } }
  ],
  "fields": [
    { "name": "title", "typeRef": { "name": "string" }, "required": true },
    { "name": "shipTo", "typeRef": { "name": "Address" } },
    { "name": "customerId", "typeRef": { "name": "Identity.UUID" } }
  ]
}
```

An engine type's fields hold primitives, scalars, enums, lists and value
types: `@jsonField` types, which a table holds as a JSON column too, and
types a General dependency declares. It reaches another engine type's
instances through `Links`, which names the type as its schema, and a
table's row through the row's key in a plain field. The build refuses,
before its generators run, an engine type that reaches a table or
another engine type through a field, a table that references an engine
type, an engine type that declares what only a table has (`@versioned`,
`@optimistic`, a version graph, the `User` trait, a queue) or a field the
database generates (`AutoGenerate`: the engine gives each instance its
id), and a map or a union, which the engine does not validate. Each
message names the way out.

### The engine document

The `enginedoc` generator writes what a server puts into the engine for
the DB service: `engine/<service>.json` in its Go types module, which the
Go types also hold as `EngineDocument`, the TypeScript types as
`engineDocument` (imported from `<types package>/engine`, so a client of
the types does not load it) and the Rust types as `ENGINE_DOCUMENT`:

```json
{
  "version": 1,
  "namespace": "support-db",
  "tools": {
    "invocationPolicy": { "key": "invocationPolicy", "values": ["auto", "ask"], "default": "auto" },
    "keys": { "scalar": "x-superschematic-scalar", "guidance": "superschematic/operation-guidance", "parameters": [] }
  },
  "behaviors": [
    { "name": "Links", "package": "@superschematic/engine" },
    { "name": "Workflow", "package": "@superschematic/engine" }
  ],
  "schemas": [
    { "name": "Account", "kind": "General", "types": { "Account": { ... } } },
    { "name": "Ticket", "kind": "General", "scalars": { ... }, "types": { "Ticket": { ... }, "Address": { ... } } }
  ],
  "metaSchema": { "$defs": { ... } }
}
```

- **`namespace`** is the engine namespace the documents are defined and
  published in: `outputs.engine.namespace` in the service's config, else
  the service's name, which must then be an engine namespace name
  (lowercase letters, digits and hyphens).
- **`schemas`** holds one schema-file document per engine type, the form
  `format --to=json` writes: `kind: General`, named after the type, with
  the type, the value types it reaches, and the enums and scalars they
  use, and no imports. A scalar the core's catalog holds is its name and
  language primitive; any other is its whole definition.
- **`metaSchema`** is what this binary's `json-schema` prints, so a
  document that carries an extension's decorators loads; **`tools`** are
  the invocation policy and the vendor keys the binary registers (D11),
  the engine's `tools` option; **`behaviors`** names each behavior the
  types compose and the package that implements it, and for an
  extension's behavior the Rust crate its registration names
  (`"crate"`), which the project's engine crate compiles in (below).

A TypeScript server opens the engine with it:

```ts
import { engineDocument } from '@schemas/support-db-types/engine';

const engine = openEngine({
  path, policy,
  metaSchema: engineDocument.metaSchema,
  tools: engineDocument.tools,
  namespaces: { names: [engineDocument.namespace] },
});
const target = { namespace: engineDocument.namespace };
for (const schema of engineDocument.schemas) engine.schemas.define(system, schema, target);
for (const schema of engineDocument.schemas) engine.schemas.publish(system, schema.name as string, target);
```

Every document is defined before any is published, since a link may name
a type the document defines after it.

### In a generated server

A generated Go, TypeScript or Rust server whose API's database is an
engine database does this itself (D58): it opens the engine on the
database in the background, deploys the document before `/readyz` reports
the engine ready, and mounts the engine's routes, its event stream and its
MCP endpoint at `/engine/<db>`, behind the API's identity and service
callers. The API's `Deps` holds the engine: in Go an
`*engine.Background`, whose `Wait(ctx)` returns it once it opens; in
TypeScript an `EngineBackground` of `@superschematic/engine/server`, whose
`wait()` resolves with it, every call a promise, the server opening it
through the engine's Node-API addon and mounting `engineApp` and
`engineMcp` on its Hono app; in Rust a
`superschematic_engine::background::Background`, whose `wait().await`
does, an engine to call off the async threads (`spawn_blocking`). Three
more keys of the database's `outputs.engine` say how:

```json
{
  "outputs": {
    "engine": {
      "namespace": "support",
      "mount": "/engine/support",
      "policy": "permission",
      "retention": { "maxAgeMs": 2592000000 }
    }
  }
}
```

- **`mount`** is where the servers of the APIs on the database mount the
  engine's routes: one or more path segments, `/engine/<service>` unless
  set, never under `/api` or at `/healthz` or `/readyz`.
- **`policy`** is the access policy they open the engine with:
  `"permission"` unless set, which allows an action on a type to a caller
  holding `<Type>.<action>` (`Ticket.read`, `Ticket.write`,
  `Ticket.define`, `Ticket.publish`), the permissions the API's
  `permissions.json` then lists; `"allowAll"`, for tests and local use; or
  `{ "rules": [...] }`, the engine's ordered rules.
- **`retention`** is how long the event log keeps events, as the engine's
  `retention` option reads it (see the runner below): `maxAgeMs`,
  `maxEvents` or both, and `maxHoldMs`, `everyMs` and `batchSize`, each
  an integer in the engine's range, which the build checks. Unset, the
  log keeps every event. Every server, job, worker and runner on the
  database opens the engine with it, and the runner alone prunes. A
  client that resumes a read or an event stream from a cursor the runner
  pruned past gets 410 `cursor_expired`, with the floor and the head to
  start again from.

The engine's routes take the server's 30-second timeout, a 504
`gateway_timeout` past it, in each language, but the event stream and
MCP, whose responses last as long as their clients. A write answered 504
may still have committed, so a client reads it back before it tries
again.

The engine's runner, which runs reactions, schedules and Timers'
deadlines, is a deployable of its own, `<db>-engine-runner`, one instance
in the language of the first API on the database: a Cloud Run worker pool
on gcp, a process of its own under `stack dev`. It acts as its deployable's
name, holding each engine type's name as a permission, and with
`retention` it prunes the log, while it holds the runner's lock, never
past an event a reaction has yet to handle. A live event stream holds
nothing: one whose client stops reading for longer than retention keeps
ends, and its reconnect gets the 410.

Several APIs may share an engine database, each served in its own
language. Their servers share the engine: each deploys the same document,
which mints one version however many start at once, and each answers the
engine's routes, its event stream and MCP as the others do, reading what
another wrote and streaming what another commits. A Go, a Rust and a
TypeScript server over one database answer one script alike, a test the
release candidate runs.

A Topcoat app (D44) whose API's database is an engine database keeps the
engine in-process on SQLite: the crate the Topcoat extension writes has
`engine::open(path)`, which opens the engine on a SQLite file and deploys
the document, and its setup method takes the engine and mounts its
routes at `/engine`, behind the API's `Authenticator`:

```rust
let engine = shop_orders_topcoat::engine::open("shop.engine.db")?;
let app = Router::builder()
    .discover()
    .shop_orders(implementations, page_authenticator, engine)
    .build();
```

### The project's engine crate

The Rust engine (D58) runs an extension's behavior from a Rust crate
that implements the engine's `Behavior` trait, which the extension's
`BehaviorSpec` names (`Rust`: the crate, its path in the project and
the expression that registers it). When a DB or General schema's types
compose such a behavior, or one of the work queue's or the commerce
ones, which the core's engine archive leaves out, the `projectengine` generator writes the
project's engine crate, `engine/rust/<service>`, named
`<rust_crate_prefix><service>-engine`:

```rust
pub fn behaviors() -> BehaviorSet {
    superschematic_engine_behaviors::core()
        .with(acme_behaviors::Rating)
}
```

It depends on the engine's crates, at the `[paths]` keys `engine_rust`,
`engine_behaviors_rust` and, when a type composes a work-queue behavior
or a commerce one, `engine_workqueue_rust` or `engine_commerce_rust`,
and on each extension's crate by its path. A
Rust host links the crate and opens the engine with `behaviors()` and
`tools()`, the binary's invocation policy and vendor keys (acme's crate
for `shop-ratings` is `acme-shop-ratings-engine`):

```rust
let mut options = EngineOptions::postgres(url, PostgresOptions::default());
options.policy = Some(policy);
options.meta_schema = Some(MetaSchema::Text(meta_schema));
options.behaviors = acme_shop_ratings_engine::behaviors();
options.tools = acme_shop_ratings_engine::tools();
let engine = Engine::open(options)?;
```

A Go host links the engine as a static archive through its C ABI, and
the crate's workspace holds that archive as its member `ffi/`
(`<crate>-ffi`), the C ABI over `behaviors()`:

```rust
use acme_shop_ratings_engine as project;

superschematic_engine_abi::export_engine_c_abi!(behaviors = project::behaviors);
```

It depends on the C ABI's library at `[paths] engine_abi_rust`. Build
it, and link it in place of the core's archive by putting its directory
first in `CGO_LDFLAGS`; the binding's config passes the engine
document's `tools`, without which an engine refuses an operation that
names the extension's invocation policy value:

```sh
scripts/engine-archive.sh --crate dist/engine/rust/shop-ratings/ffi --out dist/engine/lib
CGO_LDFLAGS="-L$PWD/dist/engine/lib ..." go build ./...
```

A TypeScript host loads the engine as a Node-API addon, and the crate's
workspace holds one as its member `napi/` (`<crate>-napi`), the addon
over `behaviors()`:

```rust
use acme_shop_ratings_engine as project;

superschematic_engine_napi::export_engine_napi!(behaviors = project::behaviors);
```

It depends on the addon's library at `[paths] engine_napi_rust`. Build
it, and name the file it prints when the engine opens, with the engine
option `addon` or, for every engine the process opens,
`SUPERSCHEMATIC_ENGINE_ADDON`; the engine's `behaviors` name the
extension's, which it registers after the core's, and `tools` passes the
engine document's:

```sh
export SUPERSCHEMATIC_ENGINE_ADDON="$(scripts/engine-napi.sh --crate dist/engine/rust/shop-ratings/napi --out dist/engine/addon)"
```

```ts
const engine = await openEngineAsync({ postgres: { url }, policy: 'permission', behaviors: ['acme.Rating'], tools, metaSchema });
```

`openEngine({ path, ... })` opens the same addon on a SQLite file.

A generated Go server and runner on the database link it so: the
checkout's Dockerfile builds the `ffi` member in its engine stage, the
generated CI's `check` builds it before it compiles the server, and
`stack dev` builds it before it builds the server and puts its directory
first in the build's `CGO_LDFLAGS`. A generated TypeScript server and
runner load the `napi` member's addon: the checkout's Dockerfile builds it
in its engine stage and names it with `SUPERSCHEMATIC_ENGINE_ADDON`, and
`stack dev` builds it and names it so in the server's environment. The
commands above are for a host written by hand; a generated one needs none
of them. Each needs the `[paths]` engine keys to name a checkout of the
engine's crates, since a release ships none yet.

A schema whose types compose the core's behaviors alone gets no crate,
since the engine's release carries them, and one that composes an
extension's behavior whose registration names no crate gets none either:
the build logs that no engine can run the schema's behaviors, an engine
refuses the schema, and the build refuses a server on it. acme's
`crates/behaviors` is the worked example (`examples/acme-schematic`).

## Display

A UI, or an agent, that renders a schema's instances reads how to show
them from the schema: `@display` on the type, from
`@superschematic/schema`. It names one instance and several, the title
and summary fields, what a create button says, and labels for the
`Workflow`'s states and transitions:

```ts
@display({
  noun: "Task",
  plural: "Tasks",
  titleField: "title",
  createLabel: "New task",
  summaryFields: ["Workflow.status"],
  states: {
    todo: { label: "To do", tone: "muted" },
    doing: { label: "Do", activeForm: "Doing", tone: "active" },
    done: { label: "Done", tone: "success" },
    dropped: { label: "Dropped", tone: "danger" },
  },
  transitions: {
    todo: { doing: "Start", dropped: "Drop" },
    doing: { done: "Finish", dropped: "Drop" },
  },
})
```

The JSON form writes the same object under the type's `display` key. A
UI showing an instance's moves reads the transitions from its status,
`transitions[behaviors.Workflow.status]`. A summary field names one of the
type's own fields, or a behavior's by its qualified name. `define`
refuses a title that is not one of the type's own text fields, a summary
field that is neither the type's nor its behaviors', and a state or a
transition the `Workflow` config does not list, with the compiler's
wording. The describe document carries the instance type's display, its
fields with their `@docs` titles and `@icon`s, and each operation's
title. [Documentation decorators](/superschematic/reference/documentation/#display-on-a-type)
has every rule.

## Create parameters

Some behaviors take parameters when an instance is created, so the
instance holds what they set from its first event: `Links` takes its
links, `Dependencies` its blockers. Without them a task would be created
first and linked after, and in between a queue could claim it with no
project or blockers. A create gives them under `behaviors`, by behavior
name:

```ts
engine.instances.create(alice, 'tasks', { title: 'Build' }, {
  id: 'build',
  behaviors: {
    Links: { project: 'launch', spec: { id: 'doc-1', revision: 2 } },
    Dependencies: { blockers: [{ id: 'plan' }] },
  },
});
```

Over HTTP they sit beside `data` in the body of
`POST /namespaces/{namespace}/schemas/tasks/instances`, and over MCP the
create tool (`tasks_create`) takes them as its `behaviors` argument:

```json
{ "id": "build", "data": { "title": "Build" }, "behaviors": { "Links": { "project": "launch" } } }
```

- Each behavior declares what it takes (`createParamsSchema`), and the
  describe document and the create tool show it as the type's config
  narrows it: `Links`' parameters name the type's links, with the
  required ones required.
- A parameter is held to the same checks as the operation it stands in
  for (`link`, `addBlocker`). A refusal is `invalid_argument` (400), with
  each issue at a JSON pointer such as `/behaviors/Links/project`, or
  `vetoed` (409), and nothing of the create is left.
- Every behavior's guard can refuse a create too, before any behavior
  sets anything up. A create's veto carries a code like any other
  ([below](#refusals-and-preconditions)): a blocker given twice is
  Dependencies' `already_blocking`, as `addBlocker` would say.
- A create takes no preconditions: there is no instance yet to fence.
- The create, its links and its edges are one event, in one transaction.

## Schema-level operations

Most operations run on one instance. An operation a behavior declares
with `scope: "schema"` runs on the schema as a whole and names no
instance. Eight do so far:

| Operation | Behavior | What it does |
| --- | --- | --- |
| `listLinked` | `Links` | the instances whose link points at a target |
| `search` | `Search` | a full-text or vector search over the schema's instances |
| `similar` | `Search` | the instances nearest to one, or to a draft's text |
| `staleEmbeddings` | `Search` | the instances an embedder has a vector to compute for, read-only |
| `settleEmbeddings` | `Search` | stores the vectors an embedder computed |
| `claimNext` | `Queue` | claims the first instance the caller can claim |
| `countClaimable` | `Queue` | counts the instances `claimNext` would try, read-only |
| `expireHolder` | `Lease` | expires every lease one principal holds |

Each is served at its own route, with its parameters as the body:

```
POST /namespaces/{namespace}/schemas/{name}/operations/{operation}
```

The response carries the result and no `ETag`, and the route takes no
`If-Match`, since there is no instance to name. In TypeScript, call it
with `invokeSchema` rather than `invoke`:

```ts
engine.instances.invokeSchema(me, 'notes', 'search', { query: 'release plan', limit: 20 });
```

The access policy is asked for `read` or `write` with the operation's
name, as for an instance operation. A schema-level operation appends no
event of its own: it changes what a read of an instance returns only
through the instance operations it invokes and the instances it creates,
each with its own event. A writing one may also write its behavior's own
tables where no read of an instance shows the change, as
`settleEmbeddings` stores vectors. Each
route answers 404 for an operation of the other scope. Over MCP, the tool
takes the operation's parameters and no instance id.

## Refusals and preconditions

A behavior that refuses a change answers 409 `vetoed`. Its problem's
`details` name the behavior, what it refused and why, and, where a client
would branch on it, a code the behavior's declaration lists, with
details of its own:

```json
{ "status": 409, "code": "vetoed",
  "details": { "behavior": "Workflow", "action": "transition", "reason": "no transition leads from todo to done; from todo it can move to doing",
               "code": "transition_not_allowed", "details": { "from": "todo", "to": "done", "allowed": ["doing"] } } }
```

The describe document lists each behavior's codes. A code is read beside
`details.behavior`, so `blocked` is Dependencies' and `token_stale`
Lease's. An MCP tool error carries the same problem.

A write can carry preconditions, each behavior's entry by its name, for
that behavior's guard to check: Lease's is `{ token }`, so a worker's
writes are refused once its lease is gone. Over HTTP they are the
`Preconditions` header on PATCH, DELETE and an operation; over MCP the
`preconditions` argument; in TypeScript the `preconditions` option:

```
PATCH /namespaces/default/schemas/jobs/instances/{id}   Preconditions: {"Lease": {"token": 3}}
```

```ts
engine.instances.invoke(worker, 'jobs', id, 'transition', { to: 'done' }, { preconditions: { Lease: { token: 3 } } });
```

An entry for a behavior the type does not compose, or that declares no
precondition, or that its schema refuses, is 400 `invalid_argument` with
`details.issues`, checked before the instance is read; a precondition
the guard finds false is that behavior's 409 veto. Guards run once the
fields are validated, `Constants` and `Variants` included, so a write
whose fields are refused is 422 `invalid_instance` whatever its
preconditions say.

## The runner

Some work happens after a change commits rather than inside it:
`Reactions` rules, and timed sweeps such as `Timers`' deadlines, expiring
leases or missed worker heartbeats. `engine.runner` runs it, in your server's process, as a
principal you name:

```ts
const engine = openEngine({
  path: 'shop.db',
  policy,
  runner: { principal: { subject: 'runner', permissions: ['projects.close'] } },
});
engine.runner.start();     // runs what is due, then wakes on each commit and timer
engine.runner.status();    // { running, principal, head, subscriptions, schedules, retention?, error }
engine.runner.stop();      // engine.close() stops it too
```

| Option | Default | What it is |
| --- | --- | --- |
| `principal` | none | who reactions and schedules act as; the access policy is asked as it, and their events record its subject as the actor |
| `maxDepth` | 8 | a reaction runs for an event below this depth, which stops loops of reactions |
| `maxAttempts` | 5 | failed attempts at one event before its subscription halts |
| `retryInitialMs`, `retryMaxMs` | 1000, 60000 | the backoff: the first retry's delay, doubling up to the most |
| `batchSize` | 100 | events handled in one transaction before the runner yields, at most 500 |

- **There is no default principal and no superuser.** Without the
  `runner` option, `start()` and `runDue()` throw. Grant the principal
  what its work does: `write` on the schemas it changes, and any
  permission a Workflow transition it makes names.
- **A reaction never refuses the change that set it off.** It runs after
  the commit, as the runner's principal, not as the caller who made the
  change.
- **Failures halt in place.** A reaction that throws is retried with the
  backoff. After `maxAttempts` failures its subscription halts at that
  event and handles nothing more until
  `engine.runner.resume({ behavior, namespace, schema })`, or
  `resume(key, { skip: true })` to pass over it. A schedule never halts:
  its next run redoes the work.
- **A schema can turn a schedule off.** A schedule's interval may follow
  the config of each schema, and a config that gives it none (its
  `everyMs` function returns `null`) runs nothing on that schema:
  `status()` shows the schedule `off` there. A publish whose config gives
  an interval starts it again, an interval later.
- **A schedule writes its own storage, a reaction none.** A schedule's run
  may write its behavior's own tables, in the run's transaction, so a run
  that fails leaves none of it. It does so only for what no operation
  returns, such as history nothing pins; any change a read shows goes
  through an operation the run invokes, which appends its event. A
  reaction changes state only through the operations it invokes.
- **Events record their cause.** An event the runner's work writes
  carries `cause`: `{ behavior, event, depth }` for a reaction, or
  `{ behavior, schedule, depth }` for a schedule.
- **An archived namespace runs nothing.** Its subscriptions and schedules
  wait, `archived` in `status()`, until the namespace is unarchived, then
  pick up where they stopped.
- **It prunes the log, with `retention`.** An engine opened with
  `retention: { maxAgeMs?, maxEvents?, maxHoldMs? }` has the runner prune
  the oldest events at its first pass and every `everyMs` (a minute)
  after, a namespace at a time, `maxEvents` counting each namespace's
  own, and never past an event a subscription there has yet to handle,
  halted ones included; with `maxHoldMs`, a halted or archived one holds
  no event older than that, and `status()` shows how far each hold
  reaches and until when. `engine.runner.prune()` prunes now
  and needs no principal. A reaction's `before()` still reads an instance
  whose create was pruned, and a read of the log from the start reads
  what is kept.
- **`runDue()` drives it by hand.** It runs everything due now, started or
  not, and returns `{ handled, skipped, failed, scheduled }`, and
  `pruned` with retention. Tests use it with a clock they move.

`status()` lists each subscription (`active`, `retrying`, `halted`,
`archived` or `inactive`) and schedule (`active`, `retrying`, `off`,
`archived` or `inactive`), with its last failure, and with retention
each namespace's floor and the subscription that holds it. It is not served over
HTTP or MCP, since it spans every namespace; expose it on a health route
of your own.

## Outcomes

A `Workflow` state that no transition leaves is terminal, and a terminal
state has an outcome: `success`, `failure` or `neutral`. Name the ones
that are not successes in `outcomes`:

```json
{ "name": "Workflow", "config": {
    "states": ["todo", "doing", "passed", "failed", "cancelled"],
    "transitions": [
      { "from": "todo", "to": "doing" },
      { "from": "doing", "to": "passed" },
      { "from": "doing", "to": "failed" },
      { "from": "todo", "to": "cancelled" } ],
    "outcomes": { "failed": "failure", "cancelled": "neutral" } } }
```

- A terminal state `outcomes` does not name is a `success`, so a config
  without `outcomes` behaves as it always did.
- A key that is not a state, or names a state a transition leaves, is
  refused when the schema is defined.
- `Workflow` itself ignores outcomes. The behaviors that look at other
  instances read them: a blocker that failed does not release its
  dependents, and a rollup or a reaction can tell children that
  succeeded from children that failed.
- `stateOutcome(config, state)` from `@superschematic/engine` gives a
  state's outcome for a config as `engine.schemas.behaviors` returns it,
  and `undefined` for a state that is not terminal.
- A new version may change outcomes. Blockers and rollups read the new
  outcome at their next read; nothing that already moved is moved back.

## Dependencies

Blockers between instances, which hold up the type's Workflow. A task
cannot be done while a task it waits on is open.

| | |
| --- | --- |
| Config | `schemas`: the schemas a blocker may belong to, each composing Workflow (the type's own when absent); `gatedStates`: the states a transition into waits on, terminal or not (every terminal state when absent); `satisfiedBy`: the outcomes that finish a blocker (`["success"]` when absent) |
| Field | `blocked`: whether any blocker is not yet finished: in a terminal state of its own Workflow whose outcome `satisfiedBy` lists |
| Operations | `addBlocker({ schema?, id })`, `removeBlocker({ schema?, id })`, and the read-only `listBlockers` and `listDependents`, which page with `limit` and `cursor` |
| Create parameters | `{ blockers: [{ schema?, id }] }`, each held to `addBlocker`'s checks against the Workflow's initial state, finished or open by `satisfiedBy`; a veto carries `addBlocker`'s code |
| Guard | a transition into a gated state while `blocked` is `vetoed` (409) with `details.code` `blocked`, naming the open blockers, which `details.details.blockers` lists |

```ts
engine.instances.invoke(alice, 'Task', 't1', 'addBlocker', { id: 't2' });
// { schema: 'Task', id: 't2', status: 'todo', open: true }
engine.instances.get(alice, 'Task', 't1')?.behaviors;
// { Workflow: { status: 'todo', enteredAt: 1790790700324 }, Dependencies: { blocked: true } }
```

- A blocker that ends in a `failure` or `neutral` state stays open, so a
  failed check holds up the step after it. Remove it with `removeBlocker`
  to go on without it, or list `neutral` in `satisfiedBy` to go on past
  cancelled work.
- Gate a state that is not terminal to hold the start of work:
  `gatedStates: ["doing", "done"]` refuses `todo` to `doing` while a
  blocker is open. An instance already in `doing` can still take a
  blocker, which then holds up its move to `done`; one in a terminal
  gated state takes no open blocker.
- A cycle and an instance blocking itself are refused.
- Blockers a create gives block it from its create, so a queue never sees
  it unblocked first. They follow the rules above from the Workflow's
  initial state: a failed blocker given at create stays open, and an
  initial state that is gated and terminal takes no open blocker.
- `blocked` is computed at each read, so a blocker finishing shows at the
  dependent's next read, with no event on the dependent.
- Deleting a blocker removes its edges: each dependent gets a
  `removeBlocker` event, as the caller who deleted it.
- Blockers are read as the caller. A caller who may not read a blocker's
  schema cannot read `blocked` on what it blocks.

## Links

Typed links from an instance to instances of other schemas, or its own:
a task's project, its parent, the spec it implements, its reviewers.

| | |
| --- | --- |
| Config | `links`: by camelCase name, `{ schema, required?, pinned?, many?, max? }`. `pinned: true` (or `"revision"`) pins a revision, and the target schema must compose `Revisions`; `pinned: "release"` pins a release, and it must compose `Branches`. `many: true` holds a list of up to `max` targets, from 1 to 100 |
| Field | `targets`: `{ <name>: { schema, id, revision?, release?, latest?, stale? } }`, a list of those for a many-valued link; absent when the instance holds none |
| Operations | `link({ name, id, revision?, release? })`, `unlink({ name, id? })`, and the schema-level, read-only `listLinked({ name, id, stale?, limit?, cursor? })` |
| Create parameters | by link name, the target's `id`, or `{ id, revision? }` or `{ id, release? }` for a pinned link; a list of them for a many-valued link |
| Guard | deleting the target of a required link is `vetoed` (`required_target`), for a many-valued link only its last target |
| Vetoes | `no_revision` and `no_release`, a pinned link to a target with no revision or no release yet, by `link` or at create; `too_many`, a new target of a many-valued link that holds `max`; `not_linked`, unlinking a target the link does not hold; `required_link`, unlinking a required link or the last target of a required many-valued one; `required_target` |

```ts
engine.instances.invoke(alice, 'Task', 't1', 'link', { name: 'owner', id: 'p1' });
// { name: 'owner', schema: 'Person', id: 'p1' }
engine.instances.invokeSchema(alice, 'Task', 'listLinked', { name: 'spec', id: 's1' });
// { items: [{ id: 't1', revision: 1, latest: 2, stale: true }, { id: 't2', revision: 2, latest: 2, stale: false }], next: null }
```

- `link` again moves a link to another target.
- A **many-valued** link (`many: true` with a `max`) holds a list of
  targets, a ticket's reviewers or a stylist's services, in the order they
  were linked: `link` adds one, or pins again one it holds,
  `unlink({ name, id })` removes one, and a create gives a list, each
  target once. A deleted target leaves every list that holds it. While
  the schema has instances, a link keeps whether it holds a list, and its
  `max` may rise, not fall. What reads one target of a link, Reactions'
  `then.link` and the links of `allTerminal`, `anyTerminal` and `holds`
  say, refuses a many-valued one; `listLinked`, Rollups and `revised`
  read across the list.
- A **required** link is given at every create, so every instance holds
  it: a create without it is refused, it can be moved but not unlinked,
  and its target cannot be deleted while it points there. A required
  many-valued link keeps at least one target: its last can be neither
  unlinked nor deleted. While the schema has instances, a new version
  cannot make a link required, as it cannot make a field required; with
  none, in any namespace that reads it, a new version may change its
  links in any way.
- An **optional** link is cleared when its target is deleted, with an
  `unlink` event on each instance that pointed there.
- A **pinned** link records the target's revision, or with `pinned:
  "release"` its release (the target's `Branches` release pointer, its
  `Branches.release` field), and `targets` gives the target's latest beside it and
  `stale: true` once the target has a later one: "pinned 3, latest 5"
  in one read. `listLinked({ ..., stale: true })` finds every instance
  pointing at a superseded revision or release.

## Rollups

Values derived from the instances that link to this one: a project's
count of tasks, its tasks by status, whether they have all finished.

| | |
| --- | --- |
| Config | `rollups`: by camelCase name, `{ schema, link, function, field?, gatedStates?, outcomes? }`. `function` is `count`, `countBy`, `sum`, `min`, `max`, `latest`, `all` or `any`; `countBy`, `sum`, `min`, `max` and `latest` take a `field`; only `all` and `any` take `gatedStates` and `outcomes` |
| Field | `values`: `{ <name>: value }`, computed at each read |
| Guard | a transition into a state an `all` or `any` rollup gates is `vetoed` (`not_held`) unless the rollup holds, with the rollup and its counts in `details.details` |

On a `projects` schema, with the `tasks` schema linking to it through
`project`:

```json
{ "name": "Rollups", "config": { "rollups": {
    "tasks": { "schema": "tasks", "link": "project", "function": "count" },
    "tasksByStatus": { "schema": "tasks", "link": "project", "function": "countBy", "field": "Workflow.status" },
    "tasksFinished": { "schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"] } } } }
```

A project then reads
`"Rollups": { "values": { "tasks": 2, "tasksByStatus": { "doing": 1, "todo": 1 }, "tasksFinished": false } }`
under `behaviors`,
and cannot move to `done` until every task is done or dropped.

| Function | Value | Over no instance |
| --- | --- | --- |
| `count` | how many instances link here | `0` |
| `countBy` | `{ <value>: count }` over a string, enum or boolean field | `{}` |
| `sum` | the sum of a number field | `0` |
| `min`, `max` | the least or greatest value | absent |
| `latest` | a field's value on the instance created last, the greater id on a tie | absent |
| `all` | whether every one is in a terminal state of its Workflow, with an outcome `outcomes` lists when given | `true` |
| `any` | whether some one is | `false` |

`"tasksSucceeded": { "schema": "tasks", "link": "project", "function": "all", "outcomes": ["success"], "gatedStates": ["done"] }`
keeps a project from `done` while any of its tasks failed, where
`tasksFinished` above lets it through once they have all ended, however
they ended.

- Nothing is stored and no event is written: a linked instance's change
  shows at this instance's next read.
- Rollups are computed as the caller. A caller who may not read the
  linked schema cannot read the instance.
- A rollup reads at most 500 linked instances. Past that its value is
  `{ "over": true }`, and a gate on it does not hold.
- A misconfigured rollup (an unknown link, a `sum` over a text field) is
  refused when the schema is defined, as `invalid_schema`.

## Search

Full-text search over the type's own text fields, on SQLite's FTS5, and
with `vectors`, vector search over embeddings an outside embedder
computes from the same text.

| | |
| --- | --- |
| Config | `fields`: 1 to 16 top-level fields, each a string or a string-valued scalar; `weights`: by field, above 0 and at most 1000 (1 when absent); `vectors`, optional: `dimensions` (1 to 4096), `model` (a label) and `permission` (what an embedder needs) |
| Operations | all schema-level: `search({ query?, vector?, model?, syntax?, limit?, cursor? })` and `similar({ id } or { text, vector?, model? }, limit?, cursor?)`, which return a page of hits, and an embedder's `staleEmbeddings({ limit?, cursor? })` and `settleEmbeddings({ items })` |

```json
"behaviors": [{ "name": "Search", "config": { "fields": ["title", "body"], "weights": { "title": 3 },
    "vectors": { "dimensions": 384, "model": "minilm-l6", "permission": "notes.embed" } } }]
```

```ts
engine.instances.invokeSchema(me, 'notes', 'search', { query: 'release plan', limit: 20 });
// { items: [{ id: 'n2', rank: 1, field: 'title',
//             snippet: [{ text: 'Release', match: true }, { text: ' ', match: false }, { text: 'plan', match: true }] }],
//   next: null }
```

- **Queries.** By default a query is plain words: an instance matches
  when its indexed fields hold every word, in any field and order.
  `syntax: "fts5"` takes an FTS5 expression instead (phrases, `AND`, `OR`,
  `NOT`, `NEAR`, prefixes such as `wal*`); column filters are refused.
  Prefix queries over a large index are slow, and a search runs
  synchronously, so think twice before serving `fts5` syntax to callers
  you do not trust.
- **Matching.** The tokenizer folds case and removes diacritics; there is
  no stemming.
- **Results.** Best first, by bm25 with the config's weights. `rank` is
  the position, from 1, across pages. `snippet` is up to 12 words around
  the matches of the best field, split into parts that say whether they
  match, so a client can highlight them.
- **Consistency.** The index is written in the same transaction as the
  change, so a search never sees an instance that is not there.
- **Publishing.** A version that adds Search or changes `fields` rebuilds
  the index during the publish, holding the write lock (about half a
  second for 10,000 instances of 160 words on a laptop).
- **Access.** A caller needs `read` on the schema, and only sees results
  from their own namespace.

### Vectors

The engine calls no embedding provider and loads no SQLite extension. A
worker process, the embedder, holds the config's permission and keeps
the vectors current: it pulls the instances whose text has no vector,
computes them with the config's model, and settles them back.

```ts
const page = engine.instances.invokeSchema(embedder, 'notes', 'staleEmbeddings', { limit: 50 });
// { model: 'minilm-l6', dimensions: 384,
//   items: [{ id: 'n1', text: 'Release plan\n\nDates and owners.', sourceHash: '9c1f...' }], next: null }
const vectors = await provider.embed(page.items.map((item) => item.text));
engine.instances.invokeSchema(embedder, 'notes', 'settleEmbeddings', {
  items: page.items.map((item, i) => ({ id: item.id, sourceHash: item.sourceHash, vector: vectors[i] })),
});
// { settled: 1, skipped: [] }
```

- **Stale.** An instance is stale from its create, and again when its
  indexed text changes or a version changes the model or the dimensions.
  Its text is its indexed fields joined by a blank line.
- **The hash.** A settle presents the hash it pulled, and is skipped for
  an instance whose text or model changed since (`moved`) or that is gone
  (`not_found`). A vector of the wrong dimensions, all zeros, or with a
  value a 32-bit float cannot hold refuses the whole batch.
- **No event.** A settle appends no event and moves no instance's `seq`:
  no read of an instance shows a vector. Who settled each one and when is
  kept beside it.

A search takes the query's `vector` beside its `query`, computed with the
same model:

```ts
engine.instances.invokeSchema(me, 'notes', 'search', { query: 'release plan', vector: queryVector, model: 'minilm-l6' });
// { items: [{ id: 'n2', rank: 1, score: 0.0328, field: 'title', snippet: [...],
//             text: { rank: 1 }, vector: { rank: 1, similarity: 0.83 } }, ...], next: null }
```

- **A vector alone** ranks by cosine similarity.
- **Both** fuse the full-text and vector rankings by reciprocal rank
  fusion (k = 60, over the first 200 of each). Each hit carries its
  `score`, its place in each ranking (`text`, `vector`) and its
  similarity, so you can show why it ranks where it does.
- **The cost.** Vectors are ranked by a scan of every vector of the
  schema in the namespace, in the engine's process.

`similar({ id })` lists the instances nearest to one, leaving it out: by
its own text and, once its vector is settled, by its vector too. An agent
calls it before creating an instance like one it found, to reuse or link
a near-duplicate instead. `similar({ text, vector? })` ranks the same way
for an instance not yet created: the draft's indexed fields joined by a
blank line, and optionally their vector from the config's model, so an
agent checks for a near-duplicate before it creates one.

### Searching every schema

`engine.search(principal, params, { namespace })`, `POST
/namespaces/{namespace}/search` and the MCP tool `search` run `search` on
every schema of the namespace that composes Search and that the caller
may read, and merge the hits, each naming its `schema`. Each schema's
best hit ties the others', since bm25 and cosine do not compare across
schemas. A vector needs its `model`, and ranks only the schemas whose
vectors come from it. A schema the caller may not read is skipped
silently, not refused.

## Reactions

Rules that move Workflow statuses after a change commits. They need
[the runner](#the-runner).

| | |
| --- | --- |
| Config | `rules`: 1 to 64, each `{ when, then }`. `when` is `{ enters: <state> }`, `{ changes: { field, to?, from? } }`, `{ allTerminal: { schema, link, outcomes? } }`, `{ anyTerminal: { schema, link, outcomes } }`, `{ holds: <rollup> }` or `{ revised: { link } }`; `then` is `{ transition: <state>, link? }` |
| Fields, operations | none |

- `enters` fires when this instance's status becomes the state, by a
  create or a transition.
- `changes` fires when a create, an update or an operation of this
  instance moves `field`, to `to` and from `from` when they are given.
  `field` is an own field or a behavior's by qualified name, with a
  member path after it for an object (`Lease.ended.reason`). A create
  moves a field from absent; a delete moves nothing.
- `allTerminal` fires when an instance of `schema` that links here
  through `link` changes or goes, and every such instance is in a
  terminal state of its Workflow, with an outcome `outcomes` lists when
  it is given.
- `anyTerminal` fires when an instance of `schema` that links here
  through `link` enters a terminal state whose outcome `outcomes` lists,
  or is linked here while in one.
- `holds` fires when a change of a linked instance makes an `all` or
  `any` [rollup](#rollups) of this instance hold over at least one
  instance: on the edge from not holding to holding, so not again while
  it holds.
- `revised` fires when the instance `link` points to gains a revision
  of `Revisions`, or a release when its schema composes `Branches`; of a
  link pinned to the one it gains, only on the instances the new
  revision or release leaves stale.
- `then` moves this instance, or the one its `link` points to, to the
  state, through Workflow's `transition`, so every guard still runs.

A project that finishes when its tasks do, and starts when one of them
does:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "allTerminal": { "schema": "tasks", "link": "project" } }, "then": { "transition": "done" } } ] } }
```

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "enters": "doing" }, "then": { "link": "project", "transition": "active" } } ] } }
```

The first goes on the `projects` schema; the second on `tasks`, which
also composes `Links` with a `project` link. When a task moves to
`doing`, the runner's next pass moves its project to `active`, and the
project's event records `actor: "runner"` and
`cause: { behavior: "Reactions", event: <the task's cursor>, depth: 1 }`.

A run that completes when all its steps pass and fails when one fails
puts both rules on the run's own schema:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "allTerminal": { "schema": "steps", "link": "run", "outcomes": ["success"] } }, "then": { "transition": "completed" } },
    { "when": { "anyTerminal": { "schema": "steps", "link": "run", "outcomes": ["failure"] } }, "then": { "transition": "failed" } } ] } }
```

The first rule completes the run only when every step has succeeded, so
a run never completes with a failed step, whichever step finishes last
and whenever the runner gets to it; the second fails it. Prefer this to
a rule on the step that fails its run beside an `allTerminal` without
`outcomes`: the two run in different subscriptions, and the last step
failing could then complete the run or fail it, by which ran first.

The same run can say it through rollups it already shows, with `holds`:

```json
{ "name": "Rollups", "config": { "rollups": {
    "stepsPassed": { "schema": "steps", "link": "run", "function": "all", "outcomes": ["success"] },
    "stepFailed": { "schema": "steps", "link": "run", "function": "any", "outcomes": ["failure"] } } } },
{ "name": "Reactions", "config": { "rules": [
    { "when": { "holds": "stepsPassed" }, "then": { "transition": "completed" } },
    { "when": { "holds": "stepFailed" }, "then": { "transition": "failed" } } ] } }
```

`holds` fires once, when a step's change makes the rollup hold. A run
retried by hand after one step failed is not failed again by a second
failed step while the first is still failed; `anyTerminal` would fail it
again. An `all` over no step sets nothing off, so removing a run's last
unfinished step completes it only when a passed step is left.

A ticket escalates when its priority turns urgent, and is triaged at its
first comment:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "changes": { "field": "priority", "to": "urgent" } }, "then": { "transition": "escalated" } },
    { "when": { "changes": { "field": "Comments.commentCount", "from": 0 } }, "then": { "transition": "triaged" } } ] } }
```

A rule reads the move from the event, so an update that gives the
priority the ticket already holds moves nothing. A rule with `to` on the
instance itself, as the first is, acts only while the field still holds
`to` when the runner gets to it, as an `enters` rule acts only while the
status is still in its state.

Work done against a spec goes back to review when the spec changes:

```json
{ "name": "Links", "config": { "links": { "spec": { "schema": "specs", "pinned": true } } } },
{ "name": "Reactions", "config": { "rules": [
    { "when": { "revised": { "link": "spec" } }, "then": { "transition": "review" } } ] } }
```

A new revision of a spec moves each work item pinned to an earlier one;
an item linked again to the new revision before the runner gets to it
stays where it is. With `Queue`'s `excludeStale` the same items are not
claimed meanwhile ([Work queues](/superschematic/guides/work-queues/)).

- A rule acts only where it can. A target already in the state, with no
  transition to it, or whose guards veto the move (an open blocker, say)
  is left alone.
- States, links, rollups and fields that do not exist, a `holds` rollup
  that is not an `all` or an `any`, and `enters` and `changes` rules on
  the status that cycle, are refused when the schema is defined. So is a
  `changes` on a volatile field, one the engine computes at a read from
  other instances, the clock or the config (`Rollups.values`,
  `Dependencies.blocked`, `Links.targets`), since no event records its
  moves, and on `Workflow.enteredAt`, which every move of the status
  moves.
- Rules across instances can chain, a `holds` rule up a tree of tasks
  say; the runner's `maxDepth` stops a loop.

## Constants and Variants

Two behaviors judge the fields a write stores, as the schema's own
validation does: a refusal is `invalid_instance` (422), with each issue
at its field and a rule, and the describe document shows what they
hold the fields to. Neither adds a field or an operation.

| Behavior | Config |
| --- | --- |
| `Constants` | `fields`: the type's own top-level fields that keep the value their create gives them; `permission`: what a caller needs to change them after, optional |
| `Variants` | `field`: a `Generic.JSON` field (or one of a scalar whose values are objects); `by`: a string or enum field; `types`: by a value of `by`, a type of the schema |

A step's kind routes it, and its result has a different shape for each
kind. `Constants` keeps the kind as the create set it, so a worker cannot
turn the step it holds into another one, and `Variants` holds the result
to the kind's type:

```ts
import { Generic } from "superscalar";
import { behavior } from "@superschematic/schema";

export enum StepKind {
  Verify = "verify",
  Review = "review",
  Note = "note"
}

export abstract class Check {
  name: string;
  ok: boolean;
}

export abstract class VerifyResult {
  passed: boolean;
  checks?: Check[];
}

export abstract class ReviewResult {
  approved: boolean;
  notes?: string;
}

@behavior("Constants", { fields: ["kind"] })
@behavior("Variants", { field: "result", by: "kind", types: { verify: "VerifyResult", review: "ReviewResult" } })
export abstract class Step {
  title: string;
  kind: StepKind;
  result?: Generic.JSON;
}
```

A schema with more than one type is named like its instance type, so
this one is `Step`.

```ts
engine.instances.create(me, 'Step', { title: 'Lint', kind: 'verify', result: { passed: 'yes', by: 'ci' } });
// InstanceValidationError: result.by (unknown), result.passed (type)

const lint = engine.instances.create(me, 'Step', { title: 'Lint', kind: 'verify', result: { passed: true } });
engine.instances.update(me, 'Step', lint.id, { kind: 'review' });
// InstanceValidationError: kind (constant), result.passed (unknown), result.approved (required)
```

- **Every write.** A caller's create and update, a behavior's create, and
  an operation that changes the instance's fields on a caller's behalf
  (a `Revisions` approval) are held to them alike, and a proposal they
  would refuse is refused when it is made.
- **Absent stays absent.** A field `Constants` lists that the create
  leaves out cannot be set later, except by a caller with `permission`,
  who may also change or remove it.
- **Kinds without a type.** While `kind` holds a value `types` does not
  list (`note` above) or none, `result` holds nothing. A later version can
  give that kind a type, since no stored note holds a result.
- **Strict at every depth.** A result is held to its type as a field of
  that type would be: its fields' types and bounds, and no key the type
  does not declare, down through nested objects and lists.
- **New versions.** While the schema has instances, a new version keeps
  `field`, `by` and each kind's type, and the types themselves are held
  to the compatibility rule as a field's type is: `VerifyResult` can gain
  an optional field, not a required one. With none, any of them may
  change. `Constants` can change freely.
- **What a client sees.** The describe document's instance and the
  create and update tools carry an `if`/`then` per kind under `allOf`, so
  an MCP client or an agent sees which shape each kind takes before it
  writes, beside the create parameters, the preconditions and each
  behavior's veto codes.
- **Fields a work-queue behavior guards.** `Constants` is the general
  rule. `Retries` guards its `limitsField` itself, since its rule reads
  who holds the lease: the holder must never raise its own caps, even
  with `limitsPermission`. `Lease`'s `maxHoldField`, `Budget`'s
  `limitField` and `Presence`'s `principalField` keep rules of their own
  too; listing such a field in `Constants` as well adds its permission.

The [engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#validating-fields)
shows how a behavior of your own judges the fields a write stores.

## Branches

`Branches` makes each instance the root of a version graph (D17, D19,
D32 in `docs/DECISIONS.md`). Rows of the kinds its config names live on
refs: a primary line, which each instance gets when it is created, and
drafts of it, which only a merge brings back. A tagged commit of the
primary line is released with `releaseCommit`, the version graph's
release, and a rollback is a release of an earlier one. The instance's
own fields stay outside the graph. The graph lives
in the behavior's own tables, kept by the version graph's Rust engine in
the transaction of the operation that writes it.
[Version graphs](/superschematic/reference/version-graphs/) covers the
model under it: refs, commits, merges by conflict unit, releases,
snapshots and the sweep.

Each kind's content is another type of the schema, so a recipe's steps,
its ingredients under a step and its one cover are three types:

```ts
import { Generic, Identity } from "superscalar";
import { behavior } from "@superschematic/schema";

export abstract class Step {
  instruction: string;
  position: Generic.Int64;
  timings?: Generic.JSON;
}

export abstract class Ingredient {
  stepKey: Identity.UUID;
  quantity: string;
}

export abstract class Cover {
  photoUrl: string;
}

@behavior("Branches", {
  kinds: {
    step: { type: "Step", order: "position", units: { timings: "keyed" }, retentionDays: 365 },
    ingredient: { type: "Ingredient", parent: { key: "stepKey", of: "step" } },
    cover: { type: "Cover", singleton: true }
  },
  sweep: { intervalMs: 3600000, abandonAfter: 2592000000 }
})
export abstract class Recipe {
  title: string;
}
```

| Config | |
| --- | --- |
| `kinds` | by name, camelCase: `type`, the type of the schema whose fields are the kind's content; `parent: { key, of }`, the field of a UUID scalar that holds the parent row's entity key and the parent's kind, so deleting a parent removes its descendants; `order`, an integer field that orders siblings; `singleton`, at most one live row; `units`, a field's conflict unit (`atomic`, the default, `keyed` for each key of a JSON object, `jsonSchema`, or `excluded`, not content); `retentionDays`, how many days of history the sweep keeps |
| `primary` | the primary line's name, `main` when absent |
| `snapshotEvery` | how many commits past the nearest snapshot a commit is snapshotted at, 64 when absent |
| `sweep` | `intervalMs`, and optionally `discardGrace`, `pruneBatch` and `abandonAfter`: turns the sweep on for the schema |

Work happens on a draft and merges into the primary line:

```ts
const call = (operation: string, params = {}) => engine.instances.invoke(me, "Recipe", id, operation, params);

const [main] = call("refs").items;                          // the primary line, from the create
const draft = call("branch", { name: "spicier" });          // from the primary line; or { fromRef, name }
const { ref } = call("save", {
  ref: draft.id, version: draft.version,
  edits: { step: { upsert: [{ instruction: "Add chili", position: 3 }] } },
});
const { ref: committed } = call("commit", { ref: ref.id, version: ref.version, message: "chili" });
const merged = call("merge", { source: committed.id, target: main.id, targetVersion: main.version, tag: true });
call("releaseCommit", { commit: merged.commit.id, version: 0 });  // 0: the first release
call("released");                                                 // { release, tree, contentHash, findings }
engine.instances.get(me, "Recipe", id)?.behaviors.Branches.release; // 1: the release pointer's version
```

- **Operations.** `branch`, `save`, `commit`, `seal`, `merge`, `rebase`,
  `revert`, `releaseCommit` and `discard` write; `refs`, `releases` (the
  release log), `compose`, `materialize`, `released`, `diff` and
  `history` read. Each is an operation of the instance, so the access
  policy is asked `write` or `read` with its name, and who may `merge`
  or `releaseCommit` is the deployment's to decide. Each write appends
  the instance's operation event.
- **Refs, commits and versions.** An operation names refs and commits by
  id, and every write through a ref names the ref's version, which the
  write moves. One that is not the instance's is `invalid_argument`; the
  version graph's refusals are vetoes with its codes: `version_conflict`,
  `name_taken`, `ref_sealed`, `primary_merge_only`, `nothing_to_commit`,
  `entity_not_found`, `invalid_tree`, `merge_into_itself`, `no_parent`,
  `not_tagged` and `walk_ceiling`. `discard` refuses the primary line
  (`primary_line`), which every draft branches from and merges into.
- **Rows.** A kind's rows carry fixed columns beside its type's fields:
  `id`, `entity_key`, `ref_id`, `root_id`, `deleted_on_ref`, `_version`,
  `created_at`, `created_by`, `updated_at` and `updated_by`, the author a
  conflict names. A type with a field of one of those names is refused.
  A saved row is an entity's whole content, with `entity_key` to replace
  one, and it is held to its kind's type as a field of that type is, and
  each value to its column's value class; a refused row is
  `invalid_argument` at its field. A read returns each author as the
  caller's subject.
- **Conflicts.** A merge or a rebase whose sides changed a unit
  differently returns the conflicts, each side's value and author, and
  writes nothing; `resolutions` settle them by unit path, taking a side
  or giving a value. A value is held to its field's class, and the row it
  leaves to its kind's type, as a saved row is; a refused one is
  `invalid_argument` at `/resolutions/<i>/value` and writes nothing.
- **Older instances.** `Branches` can be added to a schema with
  instances. One created before gets its primary line at its first write
  after, as its caller: a `branch` without `fromRef`, which then branches
  from that line, an update that changes it, or another behavior's
  writing operation.
- **The sweep.** With `sweep`, a schedule runs on the schema as the
  runner's principal: it discards each draft with no write for
  `abandonAfter` through the instance's `discard`, with an event each,
  then deletes the rows of refs discarded longer ago than `discardGrace`
  (seven days by default), prunes history past each kind's
  `retentionDays` and writes missing snapshots. Without `sweep` it is off.
- **New versions.** A new version may add a kind, change a kind's fields
  as the compatibility rule allows a field to change, and change a
  retention, `primary`, `snapshotEvery` and `sweep`; removing a kind or
  changing a kind's type, parent, order, singleton or a field's unit is
  refused while the schema has instances, and so is a version without
  `Branches`. With none, deleting the last instance having deleted its
  graph, the kinds may change in any way. Every stored row reads
  a field a version adds as null, and a null field hashes as an absent
  one, so `materialize` of a commit made before returns the `contentHash`
  the commit stored and `history` returns; a value written to the field
  moves the hash, and `nothing_to_commit`, which compares trees, holds.
- **The release field.** `release` is the number of the instance's
  latest release, the release pointer's version, absent before the
  first: what the next `releaseCommit` names, and what a `Links` link
  pinned to a release records and compares.
- **Deleting.** Deleting an instance deletes its graph.
- **Lease.** The operation that points the release pointer is
  `releaseCommit`, not `release`, because `Lease` has `release`, so a
  type composes both. Lease's guard then holds `Branches`' writes to the
  lease's holder, as it holds any writing operation
  ([Work queues](/superschematic/guides/work-queues/#lease)).

## StateRules

Fields that a state of the type's `Workflow` requires or freezes. An
invoice is drafted with any fields, is sent only with a customer, an
amount and its lines, which then stay as they are, and keeps every field
once it is paid.

| | |
| --- | --- |
| Config | `states`: by a state of the type's Workflow, `required`, the type's own top-level fields an instance must hold to enter the state and keeps while in it, and `frozen`, the ones that do not change while it is in it, a list or `"all"`; `permission`: what a caller needs to change a frozen field, optional |
| Fields, operations | none |
| Guard | a transition into a state without a field it requires is `vetoed` (409) with `details.code` `missing_fields`; while the instance is in a state, an update that clears a field the state requires is `required_fields`, and one that changes a frozen field `frozen_fields`. `details.details` names the `state` and the `fields` |
| Validates | a create whose Workflow's initial state requires a field it does not give: `invalid_instance` (422), with the rule `required` at the field |

```ts
import { Generic } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["draft", "sent", "paid", "void"],
  transitions: [
    { from: "draft", to: "sent" },
    { from: "sent", to: "draft" },
    { from: "sent", to: "paid" },
    { from: "draft", to: "void" },
    { from: "sent", to: "void" },
  ],
  outcomes: { void: "neutral" },
})
@behavior("StateRules", {
  states: {
    sent: { required: ["customer", "amount", "lines"], frozen: ["customer", "amount", "lines"] },
    paid: { frozen: "all" },
    void: { frozen: "all" },
  },
  permission: "invoices.amend",
})
export abstract class Invoice {
  title: Validate<string, { maxLength: 200 }>;
  customer?: string;
  amount?: Generic.Int64;
  lines?: string[];
  notes?: string;
}
```

```ts
engine.instances.create(me, 'invoices', { title: 'March', customer: 'Acme' }, { id: 'march' });
engine.instances.invoke(me, 'invoices', 'march', 'transition', { to: 'sent' });
// BehaviorVetoError (missing_fields): invoices march cannot move to sent
// without amount and lines, which sent requires
engine.instances.update(me, 'invoices', 'march', { amount: 1200, lines: ['Consulting'] });
engine.instances.invoke(me, 'invoices', 'march', 'transition', { to: 'sent' });
engine.instances.update(me, 'invoices', 'march', { amount: 1300 });
// BehaviorVetoError (frozen_fields): invoices march is sent, which freezes amount
```

- **Held.** A field is held when it is neither absent nor null, so `""`,
  `0` and `[]` are held.
- **Every write.** The rules bind a caller's update and a behavior's
  `update()` alike: a `Retries` result in a frozen field refuses the
  attempt, and a `Revisions` approval of a patch to one is vetoed.
  `propose` checks its patch without guards, so the proposal is accepted
  and its approval refused.
- **Every move.** A transition is held to what the state requires
  whoever asks: a caller, a reaction, an expiring lease, another
  behavior's call. An operation that updates the fields and moves the
  status in one call is judged on the fields as its update left them.
- **The permission.** A caller with `permission` may change a frozen
  field. Nobody clears a field the state requires, and nobody moves the
  instance into a state without what it requires.
- **New versions.** Any config may change, and `StateRules` can be added
  to and removed from a schema with instances. An instance that predates
  a requirement keeps working: only clearing a required field it holds
  is refused, until it next enters the state.

## Approvals

Sign-offs that gate the type's Workflow: an expense is not approved
until two managers have signed it off, and not paid until finance has.

| | |
| --- | --- |
| Config | `gates`: by state of the type's Workflow, `{ count, permission, from?, excludeCreator? }`: how many distinct principals must sign off, the permission each needs, the states a move from is gated (every state when absent), and whether the instance's creator is refused (`true` when absent); `resetOnChange` (`true` when absent) |
| Field | `gates`: `{ <state>: { signedBy } }`, the subjects whose sign-offs count, in the order they signed; a gate with none is absent |
| Operations | `signOff({ state, note? })`, `withdrawSignOff({ state })`, and the read-only `listSignOffs({ state?, limit?, cursor? })`, which lists the consumed ones too, with `consumedAt` |
| Guard | a transition into a gated state, from a state its `from` allows, with fewer sign-offs than `count` is `vetoed` (`approvals_missing`), with `{ state, required, count }` in `details.details` |
| Vetoes | `no_gate`, a state the config does not gate; `in_state`, a sign-off for the state the instance is in; `creator_cannot_sign`; `already_signed`; `not_signed`, a withdrawal with no sign-off to withdraw. A caller without the gate's permission is `forbidden` (403) |

On an `expenses` schema whose claims move from `submitted` to
`approved` and on to `paid`:

```json
{ "name": "Approvals", "config": { "gates": {
    "approved": { "count": 2, "permission": "expenses.approve" },
    "paid": { "count": 1, "permission": "expenses.pay", "from": ["approved"] } } } }
```

```ts
engine.instances.invoke(ann, 'expenses', 'trip', 'signOff', { state: 'approved' });
// { state: 'approved', subject: 'ann', at: 1760000000000, signedBy: ['ann'], count: 2 }
engine.instances.get(alice, 'expenses', 'trip')?.behaviors.Approvals;
// { gates: { approved: { signedBy: ['ann'] } } }
```

- **What consumes a sign-off.** A change of the instance's own fields, an
  update or another behavior's write such as `Revisions`' `approve`,
  consumes every gate's sign-offs, so what was signed is what moves;
  `resetOnChange: false` keeps them. Entering a gated state consumes its
  own, whoever moves the instance (a caller, `Reactions` on the runner,
  another behavior), so a claim sent back to `submitted` is signed again
  before it is approved again.
- **Who signs.** A sign-off is the caller's, by subject, so one principal
  counts once. Nobody signs for the state the instance is in, and the
  instance's creator not at all unless `excludeCreator` is `false`. The
  count a gate needs stays in the config: a new version that changes it
  applies to the sign-offs already given.
- **New versions.** Gates may be added, changed and removed; a removed
  gate's sign-offs are consumed. `Approvals` can be added to a schema
  with instances, and not removed from one.
- **Deleting.** Deleting an instance deletes its sign-offs.
- **Lease.** On a leased type, list `Approvals.signOff` and
  `Approvals.withdrawSignOff` in `Lease`'s `exempt`, or the lease refuses
  every principal but its holder
  ([Work queues](/superschematic/guides/work-queues/#lease)).

## Numbering

A number a person reads on each instance: an invoice's `INV-2026-0042`,
a ticket's `T-17`. The count runs per schema, per target of a scope
link and per year, as the config says, and the create sets the number
in its own transaction.

```ts
import { behavior } from "@superschematic/schema";

@behavior("Links", { links: { project: { schema: "projects", required: true } } })
@behavior("Numbering", { format: "INV-{yyyy}-{seq:4}", scope: { link: "project" }, reset: "yearly" })
export abstract class Invoice {
  title: string;
}
```

| | |
| --- | --- |
| Config | `format` (required): text with `{seq}`, or `{seq:N}` zero-padded to N digits (1 to 10), exactly once, and `{yyyy}` or `{yy}`, the UTC year the instance was created in; `scope: { link }`: a required, single-valued link of the type's `Links`, each of whose targets counts on its own; `reset: "yearly"`: each count starts again at 1 every UTC year, which needs the year in `format` |
| Fields | `seq`, the number in its count, and `label`, the number as `format` prints it; both absent until the instance is numbered |
| Operation | `assignNumber()`, which numbers an instance made before its schema composed `Numbering` |
| Refusals at define | a brace outside a token, a token it does not know, a `{seq}` not there once, `reset` without the year, and a `scope` link that is not a required, single-valued link of the type's `Links` (`invalid_schema`) |

```ts
engine.instances.create(me, "invoices", { title: "Kickoff" }, { behaviors: { Links: { project: "launch" } } }).behaviors.Numbering;
// { seq: 1, label: 'INV-2026-0001' }
engine.instances.list(me, "invoices", { where: { "Numbering.label": "INV-2026-0001" } });
// the launch project's first invoice of 2026, and each other project's
```

- **No gap a refused create leaves.** The number is set after every
  behavior's `initialize`, in the create's transaction, so a create
  refused after it takes none. A delete leaves a gap: the next create
  takes the next number.
- **Unique within its scope.** A label never repeats within its count.
  With `scope`, each target counts from 1, so a list by label can return
  one instance per target. Moving the scope link keeps the number.
- **The year.** `{yyyy}`, `{yy}` and `reset` read the year the instance
  was created in, UTC, so a number given later still counts in its year.
- **Nothing changes a number.** An update writes own fields only, and no
  operation writes a number once set.
- **Filters.** `where: { "Numbering.label": ... }` and `"Numbering.seq"`
  read through an index each.
- **Adding it later.** `Numbering` can be added to a schema with
  instances; each holds no number (`behaviors.Numbering` is `{}`, and
  `where: { "Numbering.seq": null }` lists them) until `assignNumber()`
  numbers it with an event. A deployment backfills by invoking it on
  each in creation order; on an instance numbered already it changes
  nothing.
- **New versions.** While the schema has instances, `format`, `scope`
  and `reset` stay, and `Numbering` stays: added again, it would count
  from 1. With none, any change, and the counts start again at 1.
- **Lease.** On a type that composes `Lease`, list
  `"Numbering.assignNumber"` in its `exempt`, or a backfill of a leased
  instance is refused for anyone but its holder.

## Timers

Deadlines on an instance that move it on when nobody does: a request open
four hours without an answer is escalated, one waiting a week on its
customer is resolved, and a visit is reminded a day before its time. The
runner fires each deadline once it is due.

```ts
import { Temporal } from "superscalar";
import { behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["open", "waiting", "escalated", "resolved"],
  transitions: [
    { from: "open", to: "waiting" },
    { from: "waiting", to: "open" },
    { from: "open", to: "escalated" },
    { from: "escalated", to: "open" },
    { from: "open", to: "resolved" },
    { from: "waiting", to: "resolved" },
    { from: "escalated", to: "resolved" },
  ],
})
@behavior("Timers", {
  deadlines: {
    respond: { start: { enters: "open" }, afterMs: 14400000, then: { transition: "escalated" }, until: { leaves: "open" } },
    lapse: { start: { enters: "waiting" }, afterMs: 604800000, then: { transition: "resolved" }, until: { leaves: "waiting" } },
    remind: { start: { field: "visitAt" }, afterMs: -86400000, until: { enters: ["resolved"] } },
  },
  sweepMs: 60000,
  permission: "requests.timers",
})
export abstract class Request {
  title: string;
  visitAt?: Temporal.DateTime;
}
```

| | |
| --- | --- |
| Config | `deadlines` (required): by name, `{ start, afterMs?, then?, until? }`. `start` is `{ create: true }`, `{ enters: <state> }`, each entry into a state of the type's Workflow, or `{ field: <own date-time field> }`; `afterMs`, from the start, 0 by default and negative only for a field start; `then: { transition, from? }`, the move a fire makes, from the state the deadline starts in unless `from` names others (a field start names them); `until: { leaves }` (the state it starts in) or `{ enters: [states] }`, what cancels a pending deadline. `sweepMs` (5000 by default, at least 1000); `permission`, to reset or snooze a deadline, which nobody may without it |
| Field | `deadlines`: `{ <name>: { dueAt, fired, firedAt?, outcome? } }`, each started deadline; `outcome` is what its `then` met: `moved`, `skipped` (the status was not one it moves from), `vetoed` or `forbidden` |
| Operations | `fireTimer({ deadline })`, which the runner calls on each due deadline; `resetTimer({ deadline })`; `snoozeTimer({ deadline, dueAt })` |
| Vetoes | `no_deadline`, a deadline the config does not name; `not_armed`, a reset with nothing to start from now or a snooze of a deadline not started, either while `until` holds. A caller without `permission` is `forbidden` (403) |
| Refusals at define | a state the type's Workflow lacks, a `start.field` that is not an own date-time field, a negative `afterMs` for another start, a `then` with no transition from a `from` state to its state, a field start's `then` without `from` or `until.leaves`, and an `enters`, a `then` or an `until` on a type without `Workflow` (`invalid_schema`) |

```ts
engine.instances.create(me, "requests", { title: "Printer jams" }, { id: "jam" }).behaviors.Timers;
// { deadlines: { respond: { dueAt: 1760014800000, fired: false } } }
// four hours later, the runner's sweep fires it:
engine.instances.get(me, "requests", "jam")?.behaviors;
// { Workflow: { status: 'escalated', ... }, Timers: { deadlines: { respond: { dueAt: 1760014800000, fired: true, firedAt: 1760014860000, outcome: 'moved' } } } }
```

- **What a deadline starts from.** A deadline arms when its fact
  changes: the create, the status's entry into the state (its
  `enteredAt`), or the field's value. A write that leaves the fact as it
  is changes nothing, so a snooze survives it, and a status that leaves
  the state and comes back, within one call too, starts the deadline
  again. A field moved arms it again, unfired; a field cleared cancels
  it.
- **Fired once, whatever the move meets.** A fire marks the deadline
  fired, then moves the status when it is one of `from`. A guard's veto
  or a transition permission the caller lacks leaves the status as it
  is, and `outcome` records which; the deadline is not tried again, so a
  stuck instance holds nothing back.
- **Overdue.** Nothing in `deadlines` follows the clock: a deadline not
  fired is overdue once its `dueAt` has passed. A `Reactions` rule hears
  a fire with `{ when: { changes: { field: "Timers.deadlines.respond.fired", to: true } } }`.
- **The runner.** Every `sweepMs`, the runner fires the due deadlines of
  each schema in each namespace, earliest first, up to 1000 a run, as its
  principal, which needs `read` and `write` on the schema and any
  permission a `then` transition names. An archived namespace fires nothing until it
  is unarchived; then its overdue deadlines fire, 1000 a run. A caller
  who may write the instance can call `fireTimer` itself; on a deadline
  not due, or fired already, it changes nothing.
- **Snooze and reset.** `snoozeTimer` moves a started deadline to
  `dueAt`, epoch milliseconds, and a fired one fires again then;
  `resetTimer` starts one again from its fact. Both need `permission`.
- **New versions.** Any change: a new `afterMs` moves each pending
  deadline from its start, but a snoozed one; a removed deadline, or one
  whose `start` changes, is dropped, and arms again at the instance's
  next write. A new field deadline arms every instance at the publish; a
  new `create` or `enters` deadline arms at each instance's next write, so
  an instance never written again never arms it. `Timers` can be added
  to and removed from a schema with instances.
- **Deleting.** Deleting an instance deletes its deadlines.
- **Lease.** On a leased type, list `"Timers.fireTimer"` in `Lease`'s
  `exempt`, or the holder's lease refuses the runner's fire
  ([Work queues](/superschematic/guides/work-queues/#lease)).

## Derived

Own fields that a model outside the engine fills from other own fields:
a ticket's category and priority, an article's summary, a document's
extracted dates. The engine calls no model, as it calls no embedder for
[vectors](#vectors). A deriver, a worker holding the config's
permission, pulls what is stale, asks its model, and settles the answers
with the hash it pulled.

```ts
import { behavior } from "@superschematic/schema";

export enum Category { Billing = "billing", Bug = "bug", Other = "other" }

@behavior("Derived", {
  fields: {
    category: { from: ["subject", "body"], instruction: "Classify the request.", model: "standard" },
    summary: { from: ["body"], instruction: "Summarize it in one sentence.", model: "simple" },
  },
  permission: "tickets.derive",
})
export abstract class Ticket {
  subject: string;
  body?: string;
  category?: Category;
  summary?: string;
}
```

| | |
| --- | --- |
| Config | `fields` (required, 1 to 16): by own top-level field, `{ from, instruction, model, states? }`: the own fields the value is derived from (never itself or another derived field), what the model is asked (at most 4000 characters), a model label the deployment maps to a provider's model, and the states of the type's Workflow it is derived in (every state when absent). `permission` (required), what a deriver needs; `maxAttempts`, 3 by default, from 1 to 10 |
| Fields | `fields`: by derived field, the last value settled, `{ sourceHash, model, settledAt, settledBy, confidence? }`, and `overridden: { by, at }` once a write overrode it. `stale` (volatile): the derived fields to compute again, in the config's order |
| Operations | schema-level, for the deriver: `staleDerivations({ field?, limit?, cursor? })` and `failedDerivations({ limit?, cursor? })`, read-only, and `settleDerivations({ items })`. On the instance: `applyDerived({ fields })`, which a settle invokes, and `rederive({ fields? })` |
| Vetoes | `not_derived`, a field the config does not derive; `moved`, a hash the field's inputs, instruction, model or type no longer give; `out_of_state`; `overridden`. A caller without `permission` is `forbidden` (403) to pull, settle and apply |
| Refusals at define | a derived field or an input the type lacks, a field derived from itself or from another derived field, `states` on a type without Workflow or naming a state it lacks, and a derived field that `Constants` keeps |

```ts
const deriver: Principal = { subject: "deriver", permissions: ["tickets.derive"] };
const page = engine.instances.invokeSchema(deriver, "tickets", "staleDerivations", { limit: 50 });
// { items: [{ id: "t1", field: "category", sourceHash: "4be0...", attempt: 1,
//             instruction: "Classify the request.", model: "standard",
//             inputs: { subject: "Card declined", body: "..." },
//             output: { enum: ["billing", "bug", "other", null], ... } }, ...],
//   next: null }
engine.instances.invokeSchema(deriver, "tickets", "settleDerivations", {
  items: [{ id: "t1", field: "category", sourceHash: page.items[0].sourceHash, value: "billing", confidence: 0.9 }],
});
// { settled: 1, failed: 0, skipped: [] }
```

- **The output is the field's type.** An item's `output` is the derived
  field's JSON Schema, as the describe document writes it: an enum field
  is a pick, a boolean a yes or no, an integer with bounds a score, a
  string text, an object an extraction. A value the type refuses is not
  written.
- **The hash.** `sourceHash` is the SHA-256 of the model label, the
  instruction, the output schema and the inputs that hold a value. A
  write of an input, or a version that changes the instruction, the model
  or the field's type, moves it. The field is then stale, and its last
  value stays until a new one settles. A field whose inputs all hold
  nothing wants no value.
- **Settling.** `settleDerivations` takes at most 100 items, each a
  `value` (with an optional `confidence` from 0 to 1 and `usage`, which
  the event's parameters keep for cost reports) or a `failure`
  (`{ class?, message }`). It writes each instance's values through
  `applyDerived`, one event per instance, in a savepoint, so a refusal
  skips that instance's items alone: `skipped` gives each one's reason,
  `not_found`, `not_derived`, `overridden`, `moved`, `out_of_state`,
  `invalid` (with the type's `issues`) or `vetoed` (with the vetoing
  `behavior` and its `code`). An item given twice, or with both a value
  and a failure, refuses the whole call (`invalid_argument`).
- **What the event holds.** `applyDerived` writes through the operation's
  `update()`, so every `validate` and every guard on the type judges the
  values, as they judge a caller's update: StateRules' frozen fields skip
  a late settle as `vetoed`. Its event attributes each value to `Derived`
  with its confidence (D16, amended).
- **Failures append no event.** A failure, an `invalid` value and a
  `vetoed` one count an attempt, beside the queue, as a vector settle
  appends no event. After `maxAttempts` the field leaves the queue, and
  `failedDerivations` lists it with its last failure, until its hash
  moves or `rederive` asks for it again.
- **A person's write wins.** When a caller's create or update, or another
  behavior's operation, writes a derived field, Derived records the
  override and stops deriving it: `staleDerivations` leaves it out and
  `applyDerived` refuses it (`overridden`). `rederive` clears the
  override, the attempts and the last value's hash, and keeps the value
  until a new one settles.
- **States.** A field with `states` is listed and applied only while the
  status is one of them, so a resolved ticket's body can change without
  its category being derived again.
- **New versions.** Any config may change, and `Derived` can be added to
  and removed from a schema with instances. A field an instance already
  holds when Derived first keeps a row for it counts as overridden, by
  `existing`, so adding Derived never writes over a value a person gave.
  Removing it leaves the values as own fields.
- **Lease.** On a leased type, list `"Derived.applyDerived"` in Lease's
  `exempt`.
- **Secrets stay in.** A `Secret<T>` field is refused as an input, by the
  build and by the engine at define: a deriver sends its inputs to a
  model outside the engine (D74).

### Run a deriver

`runDeriver`, from `@superschematic/engine` (and `/client`), is the loop
a deriver runs: it pages each schema's stale fields, hands each to your
`answer`, which calls a model, and settles each page's answers in one
call. An answer that throws settles as a failure. When a pass finds
nothing, it waits for a commit, or `idleMs`, and its `signal` stops it. A
pass that meets `not_found` (a schema not yet published), `busy` or
`unavailable` waits a second, doubling to thirty, and tries again
(`onRetry` hears each), so a deriver may start before its document is
deployed.

```ts
import { inProcess, runDeriver } from "@superschematic/engine";

const deriver: Principal = { subject: "deriver", permissions: ["tickets.derive"] };
await runDeriver({
  source: inProcess(engine, deriver),            // or an EngineClient's instances
  schemas: ["tickets"],
  concurrency: 4,
  signal: controller.signal,
  answer: async (schema, item) => {
    const reply = await myModel(item.model, item.instruction, item.inputs, item.output);
    return { value: reply.value, confidence: reply.confidence, usage: reply.usage };
  },
});
```

A Rust host runs the same loop over its background engine with
`superschematic_engine::derive::run_deriver` (the `background` feature),
which calls the engine on the blocking pool and waits on the storage's
notifier. A stack runs one as a deployable of its own, from a `@deriver`
on an API ([Stacks](/superschematic/guides/stacks/)).

## Provenance

Who set each own field last and through what, and a review that holds
some writes until a person accepts them.

```json
{ "name": "Provenance", "config": {
    "review": { "sources": ["Derived"], "below": 0.8, "permission": "tickets.review" },
    "gates": { "assigned": { "reviewed": ["category", "priority"] } } } }
```

| | |
| --- | --- |
| Config | `fields`: the own top-level fields it records, every one when absent. `review`, optional: `sources`, the other behaviors the type composes whose writes enter review; `below`, a confidence at or above which a write is accepted with no reviewer (every write from a source is proposed when absent); `permission`, what a reviewer needs. `gates`, optional: by state of the type's Workflow, `{ reviewed }`, the recorded fields or `"all"` that a move into it requires out of review |
| Fields | `fields`: by recorded field, its last write, `{ source, by, at, confidence?, review? }`; `source` is `caller` for a caller's create or update, else the behavior whose operation wrote it, `by` the principal's subject, `review` `{ state, by?, at? }` with a state of `proposed`, `accepted`, `rejected` or `edited`. `inReview`: whether any field is proposed, which a list filters on (`where: { "Provenance.inReview": true }`) |
| Operations | `accept({ fields })`; `reject({ fields, patch? })`, which clears each field, or sets it to the patch's value, through its own `update()` |
| Guard | a transition into a gated state while a field its gate lists is proposed is `vetoed` (`unreviewed_fields`), with `{ state, fields }` in `details.details` |
| Vetoes | `not_recorded`; `not_in_review`, an accept or a reject of a field that is not proposed; `unreviewed_fields`. A caller without the review's `permission` is `forbidden` (403) to accept and to reject |

- **The source.** A change's own fields are compared before and after it.
  An operation's change names its behavior and carries the attribution
  its `update()`s gave (D16, amended): an entry names the behavior that
  wrote the field, from a `call()` too, and its confidence. Provenance's
  fields move in the change's own event.
- **Review.** A write from a source the review names is `proposed`, or
  `accepted` with no `by` when its confidence reaches `below`. A
  reviewer's `accept` keeps the value and records who accepted it.
  `reject` writes the field as the reviewer, source `Provenance`, which
  Derived records as an override, so the model does not propose a
  rejected value again. A caller's write over a proposal makes it
  `edited`.
- **New versions.** Any config may change, and `Provenance` can be added
  to and removed from a schema with instances. A field written before it
  was added has no record until its next write.
- **Lease.** List `"Provenance.accept"` and `"Provenance.reject"` in
  Lease's `exempt`.

### A model triages, a person confirms

The two compose with StateRules and Reactions into a ticket that a
model triages and a person confirms when the model is unsure:

```json
"behaviors": [
  { "name": "Workflow", "config": { "states": ["new", "triage", "assigned", "resolved"],
      "transitions": [{ "from": "new", "to": "triage" }, { "from": "new", "to": "assigned" },
                      { "from": "triage", "to": "assigned" }, { "from": "assigned", "to": "resolved" }] } },
  { "name": "Derived", "config": { "permission": "tickets.derive", "fields": {
      "category": { "from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard", "states": ["new", "triage"] },
      "priority": { "from": ["subject", "body"], "instruction": "How urgent is this?", "model": "standard", "states": ["new", "triage"] } } } },
  { "name": "Provenance", "config": {
      "review": { "sources": ["Derived"], "below": 0.8, "permission": "tickets.review" },
      "gates": { "assigned": { "reviewed": ["category", "priority"] } } } },
  { "name": "StateRules", "config": { "states": { "assigned": { "required": ["category", "priority"] } } } },
  { "name": "Reactions", "config": { "rules": [
      { "when": { "changes": { "field": "Derived.fields.category.settledAt" } }, "then": { "transition": "assigned" } },
      { "when": { "changes": { "field": "Derived.fields.priority.settledAt" } }, "then": { "transition": "assigned" } },
      { "when": { "changes": { "field": "Provenance.inReview", "to": true } }, "then": { "transition": "triage" } },
      { "when": { "changes": { "field": "Provenance.inReview", "to": false } }, "then": { "transition": "assigned" } } ] } }
]
```

A settle whose priority comes back at 0.6 puts it in review: the third
rule moves the ticket to `triage`, and the gate holds the first two.
Once a reviewer accepts it, the fourth rule assigns the ticket. When the
model is sure of both fields, the `settledAt` rules assign it with no
person, the first one held by StateRules until both fields are there.

A `changes` rule fires once, on the event that moves its field, and
acts only where a transition exists and every guard allows it. So key a
rule on a derived value only when the record is sure to be in a state
the rule's move leaves from; otherwise move on the gate's own signals,
as here.

## Where to go next

- [Work queues](/superschematic/guides/work-queues/): leases, claims,
  worker heartbeats, stamped children, budgets and retries.
- [Selling and booking](/superschematic/guides/commerce/): stock, time,
  a claim's holds on both, and payments and refunds.
- The [engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#behaviors):
  the behavior interface, for writing your own.
- [Write an extension](/superschematic/extending/write-an-extension/#a-behavior):
  declare a behavior in the compiler and implement it for the engine.
