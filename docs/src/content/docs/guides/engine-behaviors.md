---
title: Engine behaviors
description: Compose the engine's behaviors on a type in TypeScript or JSON; create parameters; schema-level operations; refusals with codes and preconditions on writes; the runner that runs reactions and schedules; the outcomes of Workflow's terminal states; and the core's Dependencies, Links, Rollups, Search, Reactions, Constants and Variants behaviors.
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

The core declares seventeen behaviors, so every binary's meta-schema
admits a schema that composes them. The engine implements ten itself
and registers them when it opens. The other seven are claimable work,
implemented by `@superschematic/engine-workqueue`; the engine refuses a
schema that composes one until a deployment registers that package.

| Behavior | What it adds | Requires | Covered in |
| --- | --- | --- | --- |
| `Workflow` | a `status` that moves only along declared transitions, each optionally behind a permission; an [outcome](#outcomes) for each terminal state | | [The engine](/superschematic/guides/engine/#workflow) |
| `Comments` | comments on the instance | | [The engine](/superschematic/guides/engine/#comments) |
| `Revisions` | a revision at every change, and an optional propose, approve and reject step | | [The engine](/superschematic/guides/engine/#revisions) |
| `Dependencies` | blockers that hold a transition until they finish | `Workflow` | [Dependencies](#dependencies) |
| `Links` | named links to instances of other schemas, optionally pinned to a revision | | [Links](#links) |
| `Rollups` | values computed from the instances that link here | `Workflow` | [Rollups](#rollups) |
| `Search` | full-text search over the type's text fields | | [Search](#search) |
| `Reactions` | rules that move statuses after a change commits | `Workflow` | [Reactions](#reactions) |
| `Constants` | fields the create sets and nothing changes after | | [Constants and Variants](#constants-and-variants) |
| `Variants` | a JSON field typed by another field's value | | [Constants and Variants](#constants-and-variants) |
| `Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget`, `Retries` | claimable work: leases, claims, worker heartbeats, stamped children, budgets and retries | varies | [Work queues](/superschematic/guides/work-queues/) |

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
  describe document and the create tool show it.
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
instance. Five do so far:

| Operation | Behavior | What it does |
| --- | --- | --- |
| `listLinked` | `Links` | the instances whose link points at a target |
| `search` | `Search` | a full-text search over the schema's instances |
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
event of its own: it changes state only through the instance operations
it invokes and the instances it creates, each with its own event. Each
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
`Reactions` rules, and timed sweeps such as expiring leases or missed
worker heartbeats. `engine.runner` runs it, in your server's process, as a
principal you name:

```ts
const engine = openEngine({
  path: 'shop.db',
  policy,
  runner: { principal: { subject: 'runner', permissions: ['projects.close'] } },
});
engine.runner.start();     // runs what is due, then wakes on each commit and timer
engine.runner.status();    // { running, principal, head, subscriptions, schedules, error }
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
- **`runDue()` drives it by hand.** It runs everything due now, started or
  not, and returns `{ handled, skipped, failed, scheduled }`. Tests use it
  with a clock they move.

`status()` lists each subscription (`active`, `retrying`, `halted` or
`inactive`) and schedule (`active`, `retrying`, `off` or `inactive`), with
its last failure. It is not served over
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
engine.instances.get(alice, 'Task', 't1')?.data;
// { title: 't1', status: 'todo', blocked: true }
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
a task's project, its parent, the spec it implements.

| | |
| --- | --- |
| Config | `links`: by camelCase name, `{ schema, required?, pinned? }`. A `pinned` link's target schema must compose `Revisions` |
| Field | `links`: `{ <name>: { schema, id, revision?, stale? } }`, absent when the instance holds none |
| Operations | `link({ name, id, revision? })`, `unlink({ name })`, and the schema-level, read-only `listLinked({ name, id, stale?, limit?, cursor? })` |
| Create parameters | by link name, the target's `id`, or `{ id, revision? }` for a pinned link |
| Guard | deleting the target of a required link is `vetoed` (`required_target`) |
| Vetoes | `no_revision`, a pinned link to a target with no revision yet, by `link` or at create; `required_link`, unlinking a required link; `required_target` |

```ts
engine.instances.invoke(alice, 'Task', 't1', 'link', { name: 'owner', id: 'p1' });
// { name: 'owner', schema: 'Person', id: 'p1' }
engine.instances.invokeSchema(alice, 'Task', 'listLinked', { name: 'spec', id: 's1' });
// { items: [{ id: 't1', revision: 1, stale: true }, { id: 't2', revision: 2, stale: false }], next: null }
```

- `link` again moves a link to another target.
- A **required** link is given at every create, so every instance holds
  it: a create without it is refused, it can be moved but not unlinked,
  and its target cannot be deleted while it points there. A new version
  cannot make a link required, as it cannot make a field required.
- An **optional** link is cleared when its target is deleted, with an
  `unlink` event on each instance that pointed there.
- A **pinned** link records the target's revision, and `links` reports
  `stale: true` once the target has a later one.
  `listLinked({ ..., stale: true })` finds every instance pointing at a
  superseded revision.

## Rollups

Values derived from the instances that link to this one: a project's
count of tasks, its tasks by status, whether they have all finished.

| | |
| --- | --- |
| Config | `rollups`: by camelCase name, `{ schema, link, function, field?, gatedStates?, outcomes? }`. `function` is `count`, `countBy`, `sum`, `min`, `max`, `all` or `any`; `countBy`, `sum`, `min` and `max` take a `field`; only `all` and `any` take `gatedStates` and `outcomes` |
| Field | `rollups`: `{ <name>: value }`, computed at each read |
| Guard | a transition into a state an `all` or `any` rollup gates is `vetoed` unless the rollup holds |

On a `projects` schema, with the `tasks` schema linking to it through
`project`:

```json
{ "name": "Rollups", "config": { "rollups": {
    "tasks": { "schema": "tasks", "link": "project", "function": "count" },
    "tasksByStatus": { "schema": "tasks", "link": "project", "function": "countBy", "field": "status" },
    "tasksFinished": { "schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"] } } } }
```

A project then reads
`"rollups": { "tasks": 2, "tasksByStatus": { "doing": 1, "todo": 1 }, "tasksFinished": false }`,
and cannot move to `done` until every task is done or dropped.

| Function | Value | Over no instance |
| --- | --- | --- |
| `count` | how many instances link here | `0` |
| `countBy` | `{ <value>: count }` over a string, enum or boolean field | `{}` |
| `sum` | the sum of a number field | `0` |
| `min`, `max` | the least or greatest value | absent |
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

Full-text search over the type's own text fields, on SQLite's FTS5.

| | |
| --- | --- |
| Config | `fields`: 1 to 16 top-level fields, each a string or a string-valued scalar; `weights`: by field, above 0 and at most 1000 (1 when absent) |
| Operation | the schema-level, read-only `search({ query, syntax?, limit?, cursor? })`, which returns a page of `{ id, rank, field?, snippet? }` |

```json
"behaviors": [{ "name": "Search", "config": { "fields": ["title", "body"], "weights": { "title": 3 } } }]
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

There is no vector search; D16 in
[docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md)
says why.

## Reactions

Rules that move Workflow statuses after a change commits. They need
[the runner](#the-runner).

| | |
| --- | --- |
| Config | `rules`: 1 to 64, each `{ when, then }`. `when` is `{ enters: <state> }`, `{ allTerminal: { schema, link, outcomes? } }` or `{ anyTerminal: { schema, link, outcomes } }`; `then` is `{ transition: <state>, link? }` |
| Fields, operations | none |

- `enters` fires when this instance's status becomes the state, by a
  create or a transition.
- `allTerminal` fires when an instance of `schema` that links here
  through `link` changes or goes, and every such instance is in a
  terminal state of its Workflow, with an outcome `outcomes` lists when
  it is given.
- `anyTerminal` fires when an instance of `schema` that links here
  through `link` enters a terminal state whose outcome `outcomes` lists,
  or is linked here while in one.
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

- A rule acts only where it can. A target already in the state, with no
  transition to it, or whose guards veto the move (an open blocker, say)
  is left alone.
- States and links that do not exist, and `enters` rules that cycle, are
  refused when the schema is defined.
- Rules across instances can chain; the runner's `maxDepth` stops a loop.

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
- **New versions.** A new version keeps `field`, `by` and each kind's
  type, and the types themselves are held to the compatibility rule as a
  field's type is: `VerifyResult` can gain an optional field, not a
  required one. `Constants` can change freely.
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

## Where to go next

- [Work queues](/superschematic/guides/work-queues/): leases, claims,
  worker heartbeats, stamped children, budgets and retries.
- The [engine README](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#behaviors):
  the behavior interface, for writing your own.
- [Write an extension](/superschematic/extending/write-an-extension/#a-behavior):
  declare a behavior in the compiler and implement it for the engine.
