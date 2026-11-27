---
title: Engine behaviors
description: Compose the core's behaviors on a General type in TypeScript or JSON; read their fields under each behavior's name; label a type's instances, states and transitions with @display; create parameters; schema-level and namespace operations; refusals with codes and preconditions on writes; the runner that runs reactions and schedules; the outcomes of Workflow's terminal states; the core's Dependencies, Rollups, Reactions, Constants, Variants, Timers, StateRules, Numbering, Approvals, Derived and Provenance behaviors; and Links, Search and Branches, which schema features replace.
sidebar:
  order: 7
---

[A server with no build](/superschematic/guides/engine/) builds a notes
server on development mode with `Workflow`, `Comments` and `Revisions`.
This page covers the rest of the behaviors the core runs with no
extension, and the mechanisms the newer ones lean on: parameters a create
gives its behaviors, operations that run on a whole schema or across a
namespace, and the runner that does work after a change commits. Many of
its examples are the engine's (`@superschematic/engine`, D58), which no
example runs any more and a later step removes; a DB table composes the
same behaviors with `implements` and field types
([Database tables](/superschematic/guides/database-tables/)).

The
[engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#core-behaviors)
is the full reference: every refusal, event and config change of each
behavior. The examples below come from it and from the engine's tests,
which CI runs on Node.js and Bun.

## Every behavior at a glance

The core declares thirty-four behaviors, so every binary's meta-schema
admits a schema that composes them. An engine runs twenty-eight of them,
which `superschematic catalog behaviors` lists. The engine implements
seventeen itself and registers them when it opens. Seven are claimable work, the work
queue's Rust behaviors, which the engine's addon is built with; the
engine refuses a schema that composes one until a deployment names them
in the engine's `behaviors` option (`workQueueBehaviors`, from
`@superschematic/engine-workqueue`). Four, `Stock`, `Calendar`,
`Holds` and `Payments`, are commerce (D70): the core declares them with
the package `@superschematic/engine-commerce`, the core's addon carries
them and a project's engine crate links them, and an engine runs them
once a deployment names them (`commerceBehaviors`), as a generated
server does. The last six, `Seats`, `Reconcile`, `Schedules`, `Pool`,
`Recurrence` and `Aliased`, no engine runs: the behaviors' core
implements them for a DB table alone, and the build refuses an engine
type that composes one, naming the table form.

A DB table that implements a behavior, or types a field as one, stays a
table (D103): its rules run in the behaviors' core, the Rust library every
language embeds, which runs twenty-eight of the behaviors. They are the
six above, every behavior the engine implements but `Links`, `Search`,
`Variants` and `Branches`, the work queue's but `Assignment` and `Queue`,
and the four of commerce. A table has a `Relation<>`, a `@searchField`, a
version graph and a `@queue` in place of `Links`, `Search`, `Branches` and
`Queue`, and a `Variant<By, Arms>` field in place of `Variants` (D149).
`Assignment` is not on the core yet
([Beyond plain tables](/superschematic/guides/database-tables/#beyond-plain-tables)).

`Links`, `Search`, `Queue` and `Branches` are no behaviors any more: D103
folds each into a schema feature, a relation, `@searchField`, `@queue`
and `@versioned`. Their declarations stay registered while the engine is
in the tree, so `json-schema` still lists them, but the build refuses a
type that composes one, in every kind, naming its replacement
([Links](#links), [Search](#search), [Branches](#branches)). Where a
config below names a link, a General schema on development mode names a
relation of the type, by its field, and a table its to-one relation.

| Behavior | What it adds | Requires | Covered in |
| --- | --- | --- | --- |
| `Workflow` | a `status` that moves only along declared transitions, each optionally behind a permission, and that a list filters on; `enteredAt`, when it last moved; an [outcome](#outcomes) for each terminal state | | [The engine](/superschematic/guides/engine/#workflow) |
| `Comments` | a conversation on the instance: messages and their one-level threads, which their authors edit and delete and others react to, pin and resolve, every operation beside the instance; blocks beside a message's body (refs, action cards, code anchored at a revision, which `moveAnchor` moves, and attachments the deployment stores); a reply streamed as it is written (`begin`, `appendDraft`, `finish`), the [signals](/superschematic/guides/engine/#signals) `typing`, `viewing` and `draft`, each reader's read markers and follows, the pinned messages (`listPinned`), who a message was written for when its author acted for someone, and across the namespace an inbox, a search of the messages' bodies and the deliveries a notifier or an agent dispatcher claims | | [The engine](/superschematic/guides/engine/#comments); on a table, [A row's conversation](/superschematic/guides/database-tables/#a-rows-conversation) |
| `Revisions` | a revision at every change, and an optional propose, approve and reject step | | [The engine](/superschematic/guides/engine/#revisions) |
| `Dependencies` | blockers that hold a transition until they finish | `Workflow` | [Dependencies](#dependencies) |
| `Links` | folded: a relation, `Relation<T>` | | [Links](#links) |
| `Rollups` | values computed from the instances that link here, optionally gating the type's Workflow transitions | | [Rollups](#rollups) |
| `Search` | folded: `@searchField`, and an `Embedding.Vector` field | | [Search](#search) |
| `Reactions` | rules that move statuses after a change commits | `Workflow` | [Reactions](#reactions) |
| `Constants` | fields that keep the value their create gives them, unless a caller holds the config's permission | | [Constants and Variants](#constants-and-variants) |
| `Variants` | a JSON field typed by another field's value | | [Constants and Variants](#constants-and-variants) |
| `Branches` | folded: `@versioned` tables of a version graph | | [Branches](#branches) |
| `StateRules` | own fields a Workflow state requires to enter it, and ones it freezes while an instance is in it | `Workflow` | [StateRules](#staterules) |
| `Approvals` | sign-offs by distinct principals that a move into a state waits for, consumed by a change of the instance | `Workflow` | [Approvals](#approvals) |
| `Numbering` | a number a person reads, `INV-2026-0042`, set at the create from a count per schema, per scope target and per year, that a list filters on | | [Numbering](#numbering) |
| `Timers` | named deadlines from the create, an entry into a state or a date-time field, which the runner fires once due, moving the status | `Workflow` for a deadline that names a state | [Timers](#timers) |
| `Derived` | own fields a model outside the engine fills from other own fields, under an instruction the schema holds, which a deriver pulls and settles by hash; a person's write wins | `Workflow` for a field that names states | [Derived](#derived) |
| `Provenance` | who set each own field last and through what, with the confidence the writer gave, and a review that holds a source's writes until a person accepts them | `Workflow` for a gate | [Provenance](#provenance) |
| `Lease`, `Presence`, `Blueprint`, `Budget`, `Retries` | claimable work: leases, worker heartbeats, stamped children, budgets and retries; `Queue` is folded into `@queue` and a claim, and `Assignment` is a bundle on a table | varies | [Work queues](/superschematic/guides/work-queues/) |
| `Stock`, `Calendar`, `Holds`, `Payments` | commerce: units a supply counts, time it opens, a claim's holds on either through the supply contract, and what a claim owes and how a provider settles it | `Workflow` for `Holds` | [Selling and booking](/superschematic/guides/commerce/) |
| `Seats`, `Reconcile` | a DB table's alone: named seats on a row, each holding the subjects seated in it, and the state a provider last reported, which moves the row's Workflow field | `Workflow` for `Reconcile` | [A row's seats](/superschematic/guides/database-tables/#a-rows-seats-and-a-field-that-follows-observations) |
| `Schedules`, `Pool` | a DB table's alone: runs after a delay, at an interval or on a cron line, and a pool of members `pick` chooses from | | [A row's schedules](/superschematic/guides/database-tables/#a-rows-schedules), [A pool of members](/superschematic/guides/database-tables/#a-pool-of-members) |
| `Recurrence` | a DB table's alone: a rule that turns into dated occurrences, each opened, reminded, done, missed or skipped, which a calendar and its feed read | | [A row's recurrence](/superschematic/guides/database-tables/#a-rows-recurrence-and-the-calendar) |
| `Aliased` | a DB table's alone: a name with the names a row was also known by, unique across the table's names and aliases | | [A row's names](/superschematic/guides/database-tables/#a-rows-names) |

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
  { "name": "Dependencies", "config": { "schemas": ["tasks", "documents"], "gatedStates": ["done"] } }
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
    "Dependencies": { "blocked": false } } }
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
  A behavior that adds no field, such as `Constants`, has none.
- **Volatile fields.** A field the engine computes at a read, from other
  instances, the clock or the config, or from what operations beside the
  instance keep, moves with no event of its own instance:
  `Dependencies.blocked`, `Rollups.values`,
  `Lease.active`, `Budget.meters`, and Comments' `messages`,
  `lastMessageAt`, `threads`, `composing` and `unread`. Its declaration
  says `"volatile": true`, and so does the describe document. Comments'
  `unread` is the one field that differs by who reads: the reader's
  count of top-level messages of others since its read marker. It also
  says `"perReader": true`, so no event records it: an operation's patch,
  an update's and a create event's instance leave it out, and a read
  computes it for each caller.
- **The log.** An event's change is a merge patch of `{ data, behaviors }`:
  a create the whole instance, an update its patch under `data` and what
  its behaviors' fields took under `behaviors`. An operation beside the
  instance, such as Comments' `post`, writes only its behavior's own
  tables: its event has no `seq`, carries `beside: true` and the
  operation's `result` in place of a patch, and leaves the instance's
  ETag where it was.
- **Signals.** What a behavior says about an instance without storing it,
  a typing indicator or a draft's next chunk, is a signal, not a field:
  the engine hands it to the event streams opened with signals and keeps
  nowhere ([signals](/superschematic/guides/engine/#signals)). A read
  never returns one, and the generated types below hold none.

### In the generated types

The `types` output types what a read returns under `behaviors`. For each
type that composes behaviors, each language's types write
`<Type>Behaviors` beside the type itself, which stays the own fields, the
read's `data`. It has a member per behavior that declares a field, under
the behavior's name, holding those fields as the type's config narrows
them: Workflow's status is one of the type's states, Timers' deadlines
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
}
```

and in Go (`behaviors.go`), where each object a field holds is a struct
named by its path:

```go
type TaskBehaviors struct {
	Workflow     TaskWorkflow     `json:"Workflow"`
	Dependencies TaskDependencies `json:"Dependencies"`
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
type composes behaviors as a table, with `implements` and field types,
and `@behavior` on one is refused ([Engine types](#engine-types)). An
API's own type cannot compose a behavior.

## Engine types

A DB type that composed a behavior with `@behavior` was an engine type
(D58), whose rows the engine kept. There are none now (D103's step 7): a
DB type composes its behaviors as a table, with `implements` and field
types, and stays a table, its rules run by the behaviors' core in the
generated repositories and servers
([Database tables](/superschematic/guides/database-tables/)). The build
refuses `@behavior` on a DB type, and `outputs.engine`, naming the table
form, and nothing lowers one for you: rewrite the type as a table.

```ts
// Was @behavior("Workflow", { ... }) on a DB type.
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{ states: ["open", "resolved"]; transitions: [{ from: "open"; to: "resolved" }] }>;
}
```

### In a generated server

No generated server opens the engine. A table's behaviors are the API's
operations, served beside its other routes, and its deadlines and changes
the API's behavior worker's
([API routes](/superschematic/guides/api-routes/)).

### The project's engine crate

No build writes a project's engine crate. A behavior whose rules need
code names its rules crate (`crate` in its class), and the build of a DB
schema that composes it writes the project's rules crate; a declarative
behavior needs none, as `acme.Rating` in `examples/acme-schematic` shows
([Write an extension](/superschematic/extending/write-an-extension/#a-behavior)).

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
fields with their `@docs` titles, `@icon`s and `@display`s (`multiline`,
and `currency`, a code or another field by its JSON key), and each
operation's title. [Documentation decorators](/superschematic/reference/documentation/#display-on-a-type)
has every rule.

## Create parameters

Some behaviors take parameters when an instance is created, so the
instance holds what they set from its first event: `Dependencies` takes
its blockers. Without them a task would be created first and blocked
after, and in between a worker could claim it with no blockers. A create
gives them under `behaviors`, by behavior name:

```ts
engine.instances.create(alice, 'tasks', { title: 'Build' }, {
  id: 'build',
  behaviors: {
    Dependencies: { blockers: [{ id: 'plan' }] },
  },
});
```

Over HTTP they sit beside `data` in the body of
`POST /namespaces/{namespace}/schemas/tasks/instances`, and over MCP the
create tool (`tasks_create`) takes them as its `behaviors` argument:

```json
{ "id": "build", "data": { "title": "Build" }, "behaviors": { "Dependencies": { "blockers": [{ "id": "plan" }] } } }
```

- Each behavior declares what it takes (`createParamsSchema`), and the
  describe document and the create tool show it as the type's config
  narrows it.
- A parameter is held to the same checks as the operation it stands in
  for (`addBlocker`). A refusal is `invalid_argument` (400), with each
  issue at a JSON pointer such as `/behaviors/Dependencies/blockers`, or
  `vetoed` (409), and nothing of the create is left.
- Every behavior's guard can refuse a create too, before any behavior
  sets anything up. A create's veto carries a code like any other
  ([below](#refusals-and-preconditions)): a blocker given twice is
  Dependencies' `already_blocking`, as `addBlocker` would say.
- A create takes no preconditions: there is no instance yet to fence.
- The create and its edges are one event, in one transaction.

## Schema-level operations

Most operations run on one instance. An operation a behavior declares
with `scope: "schema"` runs on the schema as a whole and names no
instance. Of the behaviors a type may compose, one does so far (the
folded `Links`, `Search` and `Queue` declared more):

| Operation | Behavior | What it does |
| --- | --- | --- |
| `expireHolder` | `Lease` | expires every lease one principal holds |

Each is served at its own route, with its parameters as the body:

```
POST /namespaces/{namespace}/schemas/{name}/operations/{operation}
```

The response carries the result and no `ETag`, and the route takes no
`If-Match`, since there is no instance to name. In TypeScript, call it
with `invokeSchema` rather than `invoke`:

```ts
engine.instances.invokeSchema(me, 'jobs', 'expireHolder', { holder: 'worker-1' });
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

## Namespace operations

An operation a behavior declares with `scope: "namespace"` runs on the
behavior's own tables across every schema of a namespace that composes
it, with no instance and no schema: "my mentions" or "what is unread"
over every conversation. Comments declares four:

| Operation | What it does |
| --- | --- |
| `inbox` | the caller's mentions, the threads it follows with replies of others, and the conversations it has read with new top-level messages, newest first, read-only |
| `searchMessages` | the messages whose bodies hold the query's words, ranked, over the schemas the caller may read, read-only |
| `claimDeliveries` | claims due deliveries, one per message and subject that should hear of it, under a token that lapses |
| `settleDeliveries` | settles claimed deliveries `sent`, `failed` (tried again after a backoff) or `dropped` |

Each is called by its behavior's name:

```
POST /namespaces/{namespace}/behaviors/{behavior}/operations/{operation}
```

```ts
engine.behaviors.invoke(me, { namespace: 'default', behavior: 'Comments', operation: 'inbox', params: { limit: 20 } });
client.behaviors.invoke('Comments', 'inbox', { limit: 20 });
```

It is no operation of a type: a schema's operation routes, `invoke` and
`invokeSchema` answer 404 for it, the describe document lists it under
its behavior as `namespaceOperations`, not among the type's operations.
Its MCP tool is named after the behavior: `comments.inbox`, handle
`comments_inbox`. Two behaviors of a type may each declare one of the
same name, since neither is the type's. A namespace operation is never
beside an instance and appends no event; a writing one writes only its
behavior's tables, under the namespace's writer, and an archived
namespace refuses it.

The access policy is asked a question that names the behavior and no
schema: `{ principal, action, namespace, behavior, operation }`, `action`
`read` or `write` as the operation declares, and with `listing: true`
while the tools document lists its tool. The engine keeps a row of a
schema only when the caller may read that schema, asking `read` once per
schema per call and skipping a refused one, so a policy may let every
read one through. `permissionPolicy()` does, and allows a writing one to
a caller holding `<behavior>.<operation>`: `Comments.claimDeliveries`.
A policy of rules matches the question with a rule's `behavior`.

A behavior's handler gets a `NamespaceContext`: `can()`, `configs()`,
each composing schema with its config, read-only instance calls, and
`sql`, whose `readable()` names the schemas the caller may read for a
statement to join. The engine README's
[namespace operations](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#namespace-operations)
section has the rules, for an extension's behavior too.

An instance operation that writes only its caller's own state, and
appends no event, may declare `"asks": "read"` beside `"beside": true`
and `"log": false`: the policy is asked `read` for it, so a caller who
may only read the instance may call it, and an archived namespace lets
it run. Comments' `markRead` and `follow` do.

On a table, `implements Comments<T, C>` runs on the behaviors' core
(D103): its posts, edits, deletes, purges, reactions, pins, resolves and
reads, attachment blocks among them, every writing one beside the row,
which moves no version of it and asks no other behavior's guard. The core
runs a streamed reply and read state (`markRead`, `follow`) too, which
[development mode](/superschematic/guides/development-mode/) serves and a
table's generated API leaves out. Claiming and settling deliveries, the
search of messages and `moveAnchor` wait for the core
([A row's conversation](/superschematic/guides/database-tables/#a-rows-conversation)).

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

A DB table's behaviors have no runner. Their deadlines, the writes they
hear and the events they emit are messages of the table's deadline,
change and event queues, which the API's `@behaviorWorker` runs after the
commit. On Cloudflare Workers a TypeScript behavior worker drains them at
each firing of a Cron Trigger, and a deadline or an event due before the
next firing wakes it through its Workflow (D151). An environment that runs
the database and no worker for a table's queues is refused
([A table's deadlines](/superschematic/guides/api-routes/#a-tables-deadlines-timers-and-its-worker),
[Queue workers on Cron Triggers](/superschematic/guides/stacks/#queue-workers-on-cron-triggers)).

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
  refused when the schema is defined. A table's Workflow names the
  outcome of a state a transition leaves in `finished` (D113); an engine
  type refuses `finished`, since Dependencies and Rollups rely on a
  finished instance staying finished.
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
- A DB table implements Dependencies too, `implements Dependencies<Task,
  { ... }>`, its edges a sub-table of its own and `listDependents` left
  out
  ([Database tables](/superschematic/guides/database-tables/#values-a-rows-children-roll-up-and-blockers-between-rows)).

## Links

`Links` is no behavior: a link to another row is a relation (D103). The
build refuses a type that composes `Links`, naming `Relation<>`. Give the
type a field that points at the other type, and name that field where a
config named a link:

```json
{ "name": "batch", "typeRef": { "name": "Batch" }, "relation": { "type": "Batch" } }
```

On development mode the field holds its target's key under `batchId`,
and a relation refuses an id no row has
([A table's answers](/superschematic/guides/development-mode/#a-tables-answers)).
On a table it is `batch: Relation<Batch>`, a foreign key. Rollups'
`link`, Reactions' `link`, Blueprint's `parentLink`, Numbering's
`scope.link`, a Budget meter's `scope` and Payments' `refundOf.link` name
it. A link pinned to a revision has no relation counterpart yet.

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
- A DB table implements Rollups too, `implements Rollups<Project, { ...
  }>`, over the rows a `HasMany` of it lists, its link their to-one
  relation; `latest` waits there
  ([Database tables](/superschematic/guides/database-tables/#values-a-rows-children-roll-up-and-blockers-between-rows)).

## Search

`Search` is no behavior: full-text search is a schema feature (D103). The
build refuses a type that composes `Search`, naming `@searchField`. On a
table, mark each field to search `@searchField` (`"searchField": true` in
the data forms), keep a vector in a `Validate<Embedding.Vector, {
dimensions }>` field (D84), and search across tables with `@searchable`
([Database tables](/superschematic/guides/database-tables/)). On
development mode a list's text filter, `filter[title][contains]=...`,
matches a field's text anywhere, case aside, with no weights and no
ranking.

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

A ticket escalates when its priority turns urgent, and is triaged once
its severity is set:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "changes": { "field": "priority", "to": "urgent" } }, "then": { "transition": "escalated" } },
    { "when": { "changes": { "field": "severity", "from": "unset" } }, "then": { "transition": "triaged" } } ] } }
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
  `Dependencies.blocked`, `Links.targets`) or that operations beside the
  instance keep (`Comments.messages`), since no event records its
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
- **A table's Constants.** `oneWay`, fields that only move one way after
  the create, is a DB table's alone (D104,
  [Fields a table keeps, freezes and signs off](/superschematic/guides/database-tables/#fields-a-table-keeps-freezes-and-signs-off)):
  the engine refuses it, naming the table form.
- **No table's Variants.** The behaviors' core does not run `Variants`,
  so the build refuses a table that implements it. A table types the
  field as a `Variant<By, Arms>` instead (D149), a discriminated union
  whose arm a sibling field names
  ([Modeling types](/superschematic/guides/modeling-types/#a-field-whose-shape-another-field-picks)).

The [engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#validating-fields)
shows how a behavior of your own judges the fields a write stores.

## Branches

`Branches` is no behavior: a version graph is a schema feature (D103).
The build refuses a type that composes `Branches`, naming `@versioned`. A
compiled schema declares the graph with `@versionGraph`, each member a
`@versioned` table with `@graphMember` and its `@conflictUnit`s (D17,
D19), and its generated facade drafts, merges, commits and releases
([Version graphs](/superschematic/reference/version-graphs/)).

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
- **A table's StateRules.** A state's `by`, the subjects who change a
  frozen field, and the config's `status` are a DB table's alone (D104,
  [Fields a table keeps, freezes and signs off](/superschematic/guides/database-tables/#fields-a-table-keeps-freezes-and-signs-off)):
  the engine refuses each, naming the table form.

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
- **A table's Approvals.** A gate's `resetOn` and `pinned`, the config's
  `status` and `signOff`'s `version` are a DB table's alone (D104,
  [Fields a table keeps, freezes and signs off](/superschematic/guides/database-tables/#fields-a-table-keeps-freezes-and-signs-off)):
  the engine refuses each, naming the table form.

## Numbering

A number a person reads on each instance: an invoice's `INV-2026-0042`,
a ticket's `T-17`. The count runs per schema, per target of a scope
relation and per year, as the config says, and the create sets the
number in its own transaction.

```ts
import { behavior } from "@superschematic/schema";

@behavior("Numbering", { format: "INV-{yyyy}-{seq:4}", reset: "yearly" })
export abstract class Invoice {
  title: string;
}
```

| | |
| --- | --- |
| Config | `format` (required): text with `{seq}`, or `{seq:N}` zero-padded to N digits (1 to 10), exactly once, and `{yyyy}` or `{yy}`, the UTC year the instance was created in; `scope: { link }`: a required relation of the type, by its field, each of whose targets counts on its own; `reset: "yearly"`: each count starts again at 1 every UTC year, which needs the year in `format` |
| Fields | `seq`, the number in its count, and `label`, the number as `format` prints it; both absent until the instance is numbered |
| Operation | `assignNumber()`, which numbers an instance made before its schema composed `Numbering` |
| Refusals at define | a brace outside a token, a token it does not know, a `{seq}` not there once, `reset` without the year, and a `scope` that names no required relation of the type (`invalid_schema`) |

```ts
engine.instances.create(me, "invoices", { title: "Kickoff" }).behaviors.Numbering;
// { seq: 1, label: 'INV-2026-0001' }
engine.instances.list(me, "invoices", { where: { "Numbering.label": "INV-2026-0001" } });
// the first invoice of 2026
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
- **On a table.** A table carries it on a field, `number:
  Numbering<{ format, scope?, reset? }>`, whose column holds the label,
  and keeps its counts in a table of the use's own
  ([A row's number and its history](/superschematic/guides/database-tables/#a-rows-number-and-its-history)).
  `Revisions` runs on a table too, its revisions and proposals in
  sub-tables of the row.

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
- **On a table.** A DB table implements Timers too, `implements
  Timers<Ticket, { ... }>`, its deadlines messages of the table's deadline
  queue, which the API's behavior worker fires in place of a runner
  ([A table's deadlines](/superschematic/guides/api-routes/#a-tables-deadlines-timers-and-its-worker)).

## Derived

Own fields that a model outside the engine fills from other own fields:
a ticket's category and priority, an article's summary, a document's
extracted dates. The engine calls no model. A deriver, a worker holding the config's
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

On a table, `implements Derived<T, { ... }>` takes the same config, its
fields and inputs named by the table's own. Its state is a sub-table of
the use, `staleDerivations`, `settleDerivations` and `failedDerivations`
are operations of the table as a whole, and the value a field's type
refuses is held to the field's JSON Schema, which the build writes into
the table's layout
([Fields a model fills, and who set each](/superschematic/guides/database-tables/#fields-a-model-fills-and-who-set-each)).
A `@deriver` of a TypeScript, Rust or Go API fills such a table
through its repository, with the loop above (D146).

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
| Fields | `fields`: by recorded field, its last write, `{ source, by, for?, at, confidence?, review? }`; `source` is `caller` for a caller's create or update, else the behavior whose operation wrote it, `by` the principal's subject, `for` the subject it [acted for](/superschematic/guides/engine/#callers-and-the-access-policy), when it acted for one, `review` `{ state, by?, at? }` with a state of `proposed`, `accepted`, `rejected` or `edited`. `inReview`: whether any field is proposed, which a list filters on (`where: { "Provenance.inReview": true }`) |
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

On a table, `implements Provenance<T, { ... }>` takes the same config.
Its records are a sub-table of the use and `inReview` is a column of the
table, `provenance_in_review`, which a list filters on
([Fields a model fills, and who set each](/superschematic/guides/database-tables/#fields-a-model-fills-and-who-set-each)).

## Where to go next

- [Work queues](/superschematic/guides/work-queues/): leases, a claim
  that is a read and a lease, worker heartbeats, stamped children, budgets
  and retries.
- [Selling and booking](/superschematic/guides/commerce/): stock, time,
  a claim's holds on both, and payments and refunds.
- [Database tables](/superschematic/guides/database-tables/#beyond-plain-tables):
  the behaviors a DB table implements on the behaviors' core, and
  [development mode](/superschematic/guides/development-mode/), which
  runs them on a schema with no build.
- The [engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#behaviors):
  the behavior interface, for writing your own.
- [Write an extension](/superschematic/extending/write-an-extension/#a-behavior):
  declare a behavior in the compiler, its rules declarative or a crate's.
