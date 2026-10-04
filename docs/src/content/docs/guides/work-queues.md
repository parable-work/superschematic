---
title: Work queues
description: Make a schema's instances claimable work with @superschematic/engine-workqueue. Leases with fencing tokens, assignment, claims in priority order, worker heartbeats, children stamped from a blueprint, budgets and retries.
sidebar:
  order: 8
---

`@superschematic/engine-workqueue` turns an engine schema into a work
queue. Workers claim instances in priority order, hold them under a lease
they renew, and give them back. The engine's runner puts back the work of
a worker that stops renewing. Seven behaviors do this, and you compose
the ones you need:

| Behavior | What it adds | Requires |
| --- | --- | --- |
| [`Lease`](#lease) | an exclusive, time-bounded lease with a fencing token, heartbeats, expiry and messages to the holder | |
| [`Assignment`](#assignment) | one principal the instance is assigned to, who alone may claim it | |
| [`Queue`](#queue) | `claim`, and `claimNext`, which claims the best candidate | `Workflow`, `Lease` |
| [`Presence`](#presence) | a heartbeat on an instance that stands for a worker; a missed one releases its leases | |
| [`Blueprint`](#blueprint) | children created with their parent, with dependencies between them | |
| [`Budget`](#budget) | reserve-then-settle budgets against limits, across enclosing scopes | |
| [`Retries`](#retries) | attempts counted per failure class, with caps, kept results and stuck detection | `Workflow` |

This page assumes you have read [the engine](/superschematic/guides/engine/)
guide and know [schema-level operations](/superschematic/guides/engine-behaviors/#schema-level-operations)
and [the runner](/superschematic/guides/engine-behaviors/#the-runner).
The package's
[README](https://github.com/parable-work/superschematic/blob/main/runtime/engine-workqueue/README.md)
is the full reference for every refusal, guard and config change.

## Set up

The core declares the seven behaviors, so the default meta-schema admits
a schema that composes them, and `@behavior` types their configs. The
engine runs them only once your server registers the package's
implementations. Until then it refuses to define such a schema, and a live
version that composes one answers 503 `unavailable`.

Nothing is published yet. Build the package from the checkout after the
engine:

```sh
cd runtime/engine-workqueue/typescript
bun install --frozen-lockfile
bun run build
```

It needs `@superschematic/engine` beside it in `node_modules` as a peer
dependency: the behaviors must run in the same copy of the engine your
server opens. Pass them as the engine's `behaviors`, with a runner for
the timed sweeps:

```ts
import { openEngine } from '@superschematic/engine';
import { workQueueBehaviors } from '@superschematic/engine-workqueue';

const engine = openEngine({
  path: 'jobs.db',
  policy,
  behaviors: workQueueBehaviors,
  runner: { principal: { subject: 'runner', permissions: ['jobs.override'] } },
});
engine.runner.start();
```

`workQueueBehaviors` is all seven. `lease`, `assignment`, `queue`,
`presence`, `blueprint`, `budget` and `retries` are exported one by one,
and `engine.behaviors.register(lease)` adds one later.

## A job queue

The `jobs` schema below is the fixture the package's end-to-end test
runs. A job is queued, running, done or failed; a worker claims the
queued job with the highest priority, and a job whose lease lapses goes
back in the queue, until its third expiry fails it. Each claim reserves
CPU seconds from a daily budget, and failures are retried per class.

```ts
import { Generic } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["queued", "running", "done", "failed"],
  transitions: [
    { from: "queued", to: "running" },
    { from: "running", to: "queued" },
    { from: "running", to: "done" },
    { from: "running", to: "failed" },
    { from: "queued", to: "failed" },
  ],
})
@behavior("Lease", {
  ttlMs: 30000,
  maxHoldField: "timeLimitMs",
  onExpiry: { transition: "queued", from: ["running"] },
  maxExpiries: 3,
  escalate: { transition: "failed", from: ["running"] },
  overridePermission: "jobs.override",
})
@behavior("Assignment", { permission: "jobs.assign" })
@behavior("Queue", { claim: { from: ["queued"], to: "running" }, priorityField: "priority", match: ["topic"] })
@behavior("Budget", { meters: { cpuSeconds: { limit: 3600, reserve: 600, reset: "daily" } }, limitPermission: "jobs.budget" })
@behavior("Retries", {
  classes: { timeout: { attempts: 3 }, invalidOutput: { attempts: 2 }, rejected: "terminal" },
  totalAttempts: 4,
  exhaustedState: "failed",
})
export abstract class Job {
  title: Validate<string, { maxLength: 200 }>;
  topic?: string;
  priority?: Generic.Int64;
  timeLimitMs?: Generic.Int64;
}
```

`superschematic format --to=json` writes the JSON document you define and
publish in the engine. A worker's loop then looks like this:

```ts
// Claim the best queued job about search: highest priority first, then
// oldest. The claim takes the lease, reserves the budget and moves the
// status to running, all in one transaction.
const { claimed } = engine.instances.invokeSchema(worker, 'jobs', 'claimNext', { match: { topic: 'search' } });
// claimed: { id: 'reindex', token: 1, expiresAt: 1030000, heartbeatMs: 10000 }, or null

// Renew the lease every heartbeatMs. The result carries any directives
// sent to the holder.
engine.instances.invoke(worker, 'jobs', claimed.id, 'heartbeat', { token: claimed.token });

// Report what the work used, and failures by class.
engine.instances.invoke(worker, 'jobs', claimed.id, 'recordUsage', { meter: 'cpuSeconds', amount: 100 });
engine.instances.invoke(worker, 'jobs', claimed.id, 'recordAttempt', { failure: 'timeout' });

// Finish, and give the lease back.
engine.instances.invoke(worker, 'jobs', claimed.id, 'transition', { to: 'done' });
engine.instances.invoke(worker, 'jobs', claimed.id, 'release', { token: claimed.token });
```

Over HTTP, `claimNext` is a schema-level route and the rest are instance
operations:

```
POST /namespaces/default/schemas/jobs/operations/claimNext                 {"match": {"topic": "search"}}
POST /namespaces/default/schemas/jobs/instances/{id}/operations/heartbeat  {"token": 1}
```

A worker that dies stops renewing. Once its lease passes `expiresAt`, or
the job's `timeLimitMs`, the runner's `expire` sweep clears the holder,
advances the token, counts the expiry, settles the budget reservation
and moves the job back to `queued`, where the next `claimNext` finds it.
After the test's run, a job that timed out reads:

```json
{
  "title": "index", "topic": "search", "priority": 5, "timeLimitMs": 45000,
  "status": "queued",
  "lease": { "holder": null, "token": 2, "acquiredAt": null, "expiresAt": null, "active": false, "expiries": 1 },
  "budget": { "cpuSeconds": { "used": 100, "reserved": 0, "limit": 3600, "remaining": 3500 } },
  "retries": { "total": 1, "classAttempts": { "timeout": 1, "invalidOutput": 0, "rejected": 0 }, "bestScore": null, "exhausted": false, "stuck": false }
}
```

## Lease

An exclusive, time-bounded lease on the instance, held by one principal.

| | |
| --- | --- |
| Config | all optional: `ttlMs` (60000), `heartbeatMs` (a third of `ttlMs`), `sweepMs` (5000), `maxHoldMs` or `maxHoldField`, `onExpiry` and `escalate` (`{ transition, from }`), `maxExpiries`, `exempt`, `acquirePermission`, `overridePermission`, `directPermission` |
| Field | `lease`: `{ holder, token, acquiredAt, expiresAt, active, expiries }` |
| Operations | `acquire({ ttlMs? })`, `heartbeat({ token })`, `release({ token? })`, `expire({ holder? })`, `direct({ name, data? })`, `acknowledge({ token, ids })`, `resetExpiries()`, and the schema-level `expireHolder({ holder })` |
| Schedule | `expire`, every `sweepMs`, on the runner |
| Guard | while a lease is active, only the holder may update, delete or call a writing operation, except `exempt` operations (`"Comments.comment"`), read-only ones, and principals with `overridePermission` |

- **The token is a fencing counter, not a secret.** It advances at every
  acquire, release and expiry, so a worker process that lost its lease
  cannot renew or release the one its principal took again.
- **Lapsed is not held.** A lease past `expiresAt`, or past its longest
  hold (`maxHoldField` on the instance, else `maxHoldMs`), gives its
  holder nothing. A heartbeat never extends past the longest hold.
- **Expiry moves the status.** `onExpiry` moves it back (running to
  queued); at `maxExpiries`, `escalate` moves it instead (running to
  failed), and `acquire` is refused until `resetExpiries`. An expiry on
  an instance already in a terminal state is not counted: a holder that
  finished and died before releasing has not failed.
- **Directives** are messages to the holder: `direct` sends one, every
  heartbeat returns those not yet acknowledged, and `acknowledge` stops
  them. They end with the lease.
- **The sweep** needs the runner's principal to hold `write` on the
  schema and any permission an `onExpiry` transition names.

## Assignment

One principal the instance is assigned to, or none.

| | |
| --- | --- |
| Config | `permission`, optional |
| Field | `assignee`, the principal's subject |
| Operations | `assign({ to })`, `unassign()` |
| Guard | while assigned, a `Lease.acquire` or `Queue.claim` by anyone but the assignee is `vetoed` |

A principal may assign an unassigned instance to itself and unassign
itself; every other move needs `permission`. `claimNext` skips work
assigned to someone else, so in the test above a worker never claims the
job assigned to another, whatever its priority.

## Queue

Claimable work: one transaction that checks an instance can be claimed,
takes its lease and moves its status.

| | |
| --- | --- |
| Config | `claim`: `{ from, to }`, the states an instance is claimed in and the one a claim moves it to; `priorityField`, an integer field; `match`, the fields `claimNext` may filter on; `maxCandidates` (100) |
| Operations | `claim({ ttlMs? })`, `refresh()`, and the schema-level `claimNext({ match? })`, which returns `{ claimed }`, a claim or null |
| Guard | `Lease.acquire` other than through a claim is `vetoed`, so a claimable instance's lease is taken only by claiming it |

`claim` expires a lapsed lease first, then checks the status is one of
`claim.from` and that no `Dependencies` blocker holds the instance up,
then acquires the lease, reserves `Budget` when the type composes it, and
transitions to `claim.to`. A refusal at any step leaves nothing behind.

A claim waits on blockers; a plain `transition` to `claim.to` does not,
unless `Dependencies` gates that state too. List it in `gatedStates`
(`{ "gatedStates": ["running", "done"] }` on a schema like `jobs` above)
and a manual start waits for the blockers as a claim does. A blocker
counts as finished only in a terminal state whose outcome the
dependent's `satisfiedBy` lists, a success by default
([Outcomes](/superschematic/guides/engine-behaviors/#outcomes)).

`claimNext` tries candidates highest priority first (an instance with no
priority last), then oldest, then by id, skipping work that is blocked,
assigned to another principal or at `maxExpiries`, and claims the first
that succeeds. Calls are synchronous and the engine writes from one
process, so two claims never interleave.

Queue cannot be added to a schema that already has instances.

## Presence

A heartbeat on an instance that stands for a worker. The instance's
`principalField` names the principal, and only that principal may beat
it.

| | |
| --- | --- |
| Config | `ttlMs` and `principalField`, required; `onMissed` and `onBeat` (`{ transition, from }`), `releaseLeases` (schema names), `sweepMs` (5000) |
| Field | `presence`: `{ deadline, lastBeatAt, missed }` |
| Operations | `beat()`, `miss()` |
| Schedule | `miss`, every `sweepMs`, on the runner |

```ts
@behavior("Presence", {
  ttlMs: 30000,
  principalField: "subject",
  onMissed: { transition: "missing", from: ["idle", "busy"] },
  onBeat: { transition: "idle", from: ["missing"] },
  releaseLeases: ["jobs"],
})
export abstract class Worker {
  subject: string;
  name?: string;
}
```

A worker that misses its deadline is marked `missed`, moved to
`missing`, and every lease its principal holds on the `releaseLeases`
schemas is expired through `Lease.expireHolder`, so its jobs go back in
the queue even if their own leases had time left. The runner's principal
needs Lease's `overridePermission` on those schemas. A later `beat`
clears the miss.

## Blueprint

Children created with their parent: a map of steps says which instances
of a child schema an instance has, and which block which.

| | |
| --- | --- |
| Config | `schema` (the child schema), `parentLink` (its link back here) and `keyField` (a string field of the child), required; `steps` inline or `from` (`{ link, field }`, a map read from a pinned revision); `copyFields`, `copyLinks` |
| Steps | by key: `{ after?, when?, data? }`; `after` lists the steps that block this one, `when` is `{ field, equals }` or `{ field, includes }` |
| Field | `blueprint`: `{ children: [{ key, id }] }` |

```ts
@behavior("Blueprint", {
  schema: "steps",
  parentLink: "batch",
  keyField: "step",
  steps: {
    fetch: {},
    check: { after: ["fetch"], when: { field: "topic", equals: "search" } },
    index: { after: ["check"], data: { title: "Index what was fetched" } },
  },
  copyFields: ["topic"],
})
export abstract class Batch {
  title: string;
  topic?: string;
}
```

Creating a batch creates its steps in one transaction with the batch,
each step created with its link to the batch and its `Dependencies`
edges as
[create parameters](/superschematic/guides/engine-behaviors/#create-parameters),
so a step is blocked from its first event and `claimNext` never finds it
early. A step whose `when` does not hold is left out and the chain
closes over it: a batch with `topic: "copy"` gets `fetch` and `index`,
with `index` blocked by `fetch`. When the child schema composes `Queue`,
`claimNext` claims each step once its blockers finish. A step that ends
in a failure state (`outcomes: { failed: "failure" }` on the child's
`Workflow`) stays a blocker, so the steps after it are not claimed until
someone removes the edge. The child schema needs `Links` with the
`parentLink`, and `Dependencies` when steps use `after`. Make the
`parentLink` `required` and no step can be created without its batch, by
Blueprint or anyone else. With `from`, a create that gives the
definition's link stamps the steps in that create. To settle the parent
from its steps, give it `Reactions` with `allTerminal` and `anyTerminal`
rules ([Reactions](/superschematic/guides/engine-behaviors/#reactions)).

## Budget

Reserve-then-settle budgets in units you name: each meter counts what the
instance has used and reserved against a limit, and passes both up to
the enclosing scopes its `scope` link points at.

| | |
| --- | --- |
| Config | `meters` (required, by name): each `limit` or `limitField`, `reserve` or `reserveField`, `scope` (a `Links` link), `reset: "daily"`; `limitPermission`; `onExceeded: { direct }` |
| Field | `budget`: by meter, `{ used, reserved, limit, remaining }` |
| Operations | `reserve`, `recordUsage`, `settle`, `setLimit`, and the scope side, `reserveFor`, `settleFor` and `recordUsageFor` |

- A claim reserves each meter's `reserve` amount and is refused when it
  does not fit, so `claimNext` moves on to the next candidate.
- `recordUsage` is never refused, since the usage has happened. It
  releases the part the reservation covered and reports any overrun;
  with `onExceeded`, the lease holder also gets a directive.
- On a type with `Lease`, a reservation lasts as long as the lease: a
  release or an expiry settles it.
- A `scope` link to a pool or a project applies that scope's own limit
  too, in the same transaction.
- A `daily` meter's usage resets at the start of each UTC day.

## Retries

Attempts counted per failure class, against per-class caps and a total.
There is no backoff in time: work that may run again may be claimed at
once.

| | |
| --- | --- |
| Config | `classes` (required: `{ attempts }` or `"terminal"`), `totalAttempts` and `exhaustedState`, required; `limitsField`, `keepBest`, `stuckAfter`, `resultField`, `from`, `permission` |
| Field | `retries`: `{ total, classAttempts, bestScore, exhausted, stuck }` |
| Operation | `recordAttempt({ failure?, score?, result?, signature?, predicates? })` |
| Guard | once exhausted, transitions to any state but `exhaustedState`, and acquiring or claiming, are `vetoed` |

- An attempt without `failure` is a success.
- A terminal class, a class out of room, or the total reaching its cap
  exhausts the instance and moves it to `exhaustedState`.
- `keepBest` keeps a failed attempt's `result` in `resultField` when its
  `score` improves on the best by `minDelta`.
- `stuckAfter` exhausts an instance that fails with the same `signature`
  that many times in a row.
- Nothing resets exhaustion.

## Where to go next

- The package
  [README](https://github.com/parable-work/superschematic/blob/main/runtime/engine-workqueue/README.md):
  every behavior's refusals, guards and config changes in full.
- [Engine behaviors](/superschematic/guides/engine-behaviors/): the
  runner, schema-level operations, and the core's Dependencies and Links,
  which Queue and Blueprint build on.
