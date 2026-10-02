# Engine work queue

`@superschematic/engine-workqueue` (`typescript/`) carries claimable work
for `@superschematic/engine` (D16 in `docs/DECISIONS.md`): behaviors that
make a schema's instances work a principal takes, holds and gives back.
The core declares them under bare names (`internal/registry/behaviors`,
section 3.16 of `docs/extension-model.md`), so every binary's meta-schema
admits a schema that composes them, and `BehaviorConfigs` in
`@superschematic/schema` types their configs. The engine runs them only
once a deployment registers this package's implementations; without them
it refuses a schema that composes one, as it refuses any behavior it has
no implementation for.

Built: `Lease`, an exclusive lease with a fencing token, heartbeats,
expiry on the engine's runner and directives to its holder;
`Assignment`; `Queue`, the claim and `claimNext`; `Presence`, a
heartbeat on an instance that stands for a worker; `Blueprint`, children
created with their parent; and `Budget`, reserve-then-settle budgets
across enclosing scopes. Not built yet: `Retries`, the retries D16
lists, whose declaration the core already carries.

```ts
import { openEngine } from '@superschematic/engine';
import { workQueueBehaviors } from '@superschematic/engine-workqueue';

const engine = openEngine({ path: 'jobs.db', policy, behaviors: workQueueBehaviors, runner: { principal: sweeper } });
engine.runner.start();   // the lease sweep runs on the runner

const { claimed } = engine.instances.invokeSchema(worker, 'jobs', 'claimNext', { match: { topic: 'search' } });
engine.instances.invoke(worker, 'jobs', claimed.id, 'heartbeat', { token: claimed.token });
```

## Registering the behaviors

A deployment passes the implementations as the engine's `behaviors`
option when it opens the engine, after the core's, which the engine
registers itself: `workQueueBehaviors` is every one, and `lease`,
`assignment`, `queue`, `presence`, `blueprint` and `budget` are exported
one by one for a deployment that runs only some.
`engine.behaviors.register(lease)` registers one later; a published
version that composes a behavior the engine cannot run is `unavailable`
until it does. Registering one whose storage already
exists in the file brings it up to date, as for any behavior
(`runtime/engine/README.md`, "Behaviors").

Each implementation carries the copy of its declaration in
`typescript/src/declarations`, which the core binary writes with
`superschematic behaviors --package @superschematic/engine-workqueue
--out runtime/engine-workqueue/typescript/src/declarations` (`make
behaviors`) and CI checks (`make behaviors-check`), so the package and
the compiler read one declaration. The implementations reach the engine
only through its plug-in interface, as an extension's behavior does.

## Runtimes

The package ships compiled ESM with declarations (`dist/`), runs on
Node.js 24 and on Bun, and its tests run under both. It needs
`@superschematic/engine` next to it in `node_modules`: a peer dependency,
since the behaviors must run in the engine the deployment opens and share
its classes. A second copy of the engine would bring its own
`BehaviorVetoError`, which the engine's checks would not recognize. The
peer dependency is optional only so bun does not look for the unpublished
engine in the registry, as the engine's own optional peer dependency on
`@superschematic/http-runtime` is; a consumer declares it, and this
package's build and tests link the checkout's engine
(`typescript/scripts/link-local-deps.mjs`).

## Lease

An exclusive, time-bounded lease on the instance, held by one principal
and numbered by a fencing token.

| | |
| --- | --- |
| Config | `ttlMs` (at least 1000; 60000 when absent), `heartbeatMs` (less than `ttlMs`; a third of it when absent), `sweepMs` (at least 1000; 5000 when absent), `maxHoldMs`, `maxHoldField`, `onExpiry` and `escalate` (`{ transition, from }`), `maxExpiries`, `exempt`, `acquirePermission`, `overridePermission`, `directPermission`; all optional |
| Fields | `lease`: `{ holder, token, acquiredAt, expiresAt, active, expiries }`, `holder`, `acquiredAt` and `expiresAt` null when it is free |
| Operations | `acquire({ ttlMs? })` -> `{ token, expiresAt, heartbeatMs }`; `heartbeat({ token })` -> `{ expiresAt, directives }`; `release({ token? })` -> `{}`; `expire({ holder? })` -> `{ expired }`; `direct({ name, data? })` -> `{ id }`; `acknowledge({ token, ids })` -> `{}`; `resetExpiries()` -> `{ expiries }`; schema-level `expireHolder({ holder })` -> `{ expired }`. All write |
| Schedules | `expire`, every `sweepMs`, on the engine's runner |
| Guards | while a lease is active, an update, a delete or a writing operation of another behavior by any principal but the holder is `vetoed`, except an `exempt` operation, a read-only one, Queue's `refresh` and a principal with `overridePermission`; once the lease has lapsed, the holder's are; while a lease is held, a change to `maxHoldField` without `overridePermission` is `vetoed`; a principal's `direct` needs its permission (below) |
| Refusals | `acquire` while a lease is active, the holder's own included, and at `maxExpiries` (`vetoed`), without `acquirePermission` (`forbidden`), with a `ttlMs` past the config's (`invalid_argument`); `heartbeat` and `acknowledge` by another principal, with another token, or once the lease has lapsed (`vetoed`); `release` by another principal (`forbidden` without `overridePermission` when the config names one, `vetoed` when it names none), with another token or once the lease has lapsed (`vetoed`), by the holder without its token (`invalid_argument`); `direct` by a principal without its permission (`forbidden`) or with no permission in the config (`vetoed`), and with no active lease (`vetoed`); `expire` with a `holder`, and `expireHolder`, without `overridePermission` (`forbidden`, the config naming none included); `acknowledge` of an id not sent under the token (`invalid_argument`); `resetExpiries` without `overridePermission` (`forbidden`, or `vetoed` when the config names none) |
| Events | each operation's event; a heartbeat is a write, with its event |
| `configChange` | any config may change. Added to a schema with instances, which start free at token 0; not removed from one, since their leases and directives would stay behind |

```json
{ "name": "Lease", "config": {
    "ttlMs": 30000,
    "maxHoldField": "timeLimitMs",
    "onExpiry": { "transition": "queued", "from": ["running"] },
    "maxExpiries": 3,
    "escalate": { "transition": "failed", "from": ["running"] },
    "exempt": ["Comments.comment"],
    "overridePermission": "jobs.override" } }
```

### The token

The token is a per-instance integer that starts at 0 and advances at
every acquire, every release and every expiry, so an instance and a token
name one lease. `acquire` returns it; `heartbeat`, `release` and
`acknowledge` take it and refuse any other, and refuse every principal
but the holder. It is not a capability: the `lease` field shows it to
every reader, and the guard checks who calls, not what token they hold.
It tells two leases of one principal apart, so a worker's process that
lost its lease cannot renew or release the one the same principal took
again.

### Active, lapsed and expired

A lease is active while it is held, before `expiresAt`, and within its
longest hold since `acquire`: the instance's `maxHoldField`, when the
field holds a positive integer, else `maxHoldMs`. A heartbeat moves
`expiresAt` to the time plus the lease's length, never past the longest
hold, so a lease is never held longer, however often it is renewed. Each
lease keeps the length it was acquired with: `acquire({ ttlMs })` takes a
length up to the config's, and returns `heartbeatMs` scaled to it.

A lease past either time has lapsed. It gives its holder nothing: the
holder's heartbeat, release, acknowledgement and writes are refused, and
other principals' writes go through as on a free instance. Only its
expiry can follow. `expire` applies it, and any principal who may write
the instance may call it; on an instance whose lease is free or active it
returns `{ expired: false }`, and its event is all it writes. `acquire` over a
lapsed lease applies its expiry first, and so does Queue's `claim`.

An expiry clears the holder, advances the token and counts the expiry,
unless the instance is in a terminal state of its Workflow: a holder that
finished and died before releasing has not failed. Then `onExpiry` moves
the status to its `transition` when the status is one of its `from`
states, and in no other; at the expiry that reaches `maxExpiries`,
`escalate` does instead. The move goes through Workflow's `transition`,
called by Lease as the principal that applied the expiry, so Workflow's
guard and every other guard of the instance run, and it shows in the
expiry's operation event. A move a guard vetoes leaves the status as it
is, and the lease expired all the same; any other refusal, such as a
transition whose permission the caller lacks, fails the expiry and
leaves the lease lapsed. `parseConfig` checks that the type lists
Workflow, that every state is one of its states, and that a transition
leads from each `from` state to the target.

Once the instance has had `maxExpiries` expiries, `acquire` is refused,
and so is an acquire whose expiry of a lapsed lease would reach the cap.
`resetExpiries`, which needs `overridePermission`, sets the count back to
0.

A release applies `onExpiry` too, without counting an expiry, so a holder
that gives up leaves its work where it can be taken again, and one that
finished it leaves it finished.

### Expiry on the runner, and a gone holder

The `expire` schedule runs on the engine's runner, as the principal the
deployment names for it (`runner: { principal }`), every `sweepMs` on
each schema that composes Lease, in each namespace. It reads the leases
across the schema (`sql.instances()`), through an index on the holder
and the expiry time, finds the held ones past their expiry time or their
longest hold, and invokes `expire` on each, so each expiry runs that
instance's guards and appends its event, whose `cause` names the
schedule. A run expires at most 1000 (`MAX_SWEEP`); the rest wait for
the next. A guard's veto of one expiry leaves that lease for the next
run and the others go on; any other failure fails the run, which the
runner retries with its backoff and shows in `engine.runner.status()`, so
a sweeper principal the policy refuses is seen, not skipped. The runner's
principal needs `write` on the schema, and any permission an `onExpiry`
transition names.

For a holder that is gone, `expire({ holder })` expires that principal's
lease at once, active or not, and the schema-level `expireHolder({ holder
})` does so on every instance whose lease it holds and returns how many
expired; a presence check calls it as the runner's principal. Both need
`overridePermission`, and are refused when the config names none.

### The guard

While a lease is active, the guard refuses an update, a delete and every
writing operation of another behavior by any principal but the holder,
reading `writes` from the operation's guard request. These pass:

- an operation `exempt` lists, as `<Behavior>.<operation>` of a behavior
  the type lists (`parseConfig` checks the behavior);
- a read-only operation;
- a principal with `overridePermission`;
- a request a behavior's own code makes, with `call()` or an operation's
  `update()`, which names that behavior as `caller`: the operation that
  made it was asked already;
- Queue's `refresh`, which only recomputes Queue's copies of the
  instance's facts, and which a blocker's change invokes as the principal
  that changed the blocker;
- Lease's own operations, which check their callers themselves, but for
  a principal's `direct`, whose permission the guard asks.

An operation another behavior's reference hook invokes on a leased
instance, as `Dependencies`' `removeBlocker` when a blocker is deleted or
`Links`' `unlink` when a target is, runs as the principal of that change
and is refused like any other, so that change is refused too; a
deployment that wants it through lists it in `exempt`. Nothing changes
the field `maxHoldField` names while a lease is held, unless with
`overridePermission`, so a holder cannot extend its own hold.

### Directives

A directive is a message to the holder of the active lease: `direct({
name, data? })`. A principal's `direct` needs `directPermission`, or
`overridePermission` when that is absent, and is refused when the config
names neither; the guard asks it before any other check. Another behavior
of the type sends one with `call('Lease', 'direct', ...)`, as the
principal it runs for and without either permission: a budget whose
usage runs over tells the holder so as the principal that recorded the
usage. It attaches to the current token and is numbered within it, 1, 2, 3, ....
Every heartbeat returns the directives of its lease that are not
acknowledged, oldest first, as `{ id, name, data?, createdAt, createdBy }`;
delivery is at least once, and `acknowledge({ token, ids })` stops it.
When the token advances, by a release, an expiry or a new acquire, the
directives of the lease it ends are deleted, so none reaches the next
holder. Deleting the instance deletes its directives.

## Assignment

Who the instance is assigned to: one principal at a time, or none.

| | |
| --- | --- |
| Config | `permission`, optional |
| Fields | `assignee`: the principal's subject; absent when unassigned |
| Operations | `assign({ to })` -> `{ assignee, assignedAt, assignedBy }`, writes; `unassign()` -> `{ assignee }`, writes |
| Guards | while the instance is assigned, Lease's `acquire` and Queue's `claim` by any principal but the assignee: `vetoed`, whoever made the call |
| Refusals | assigning another principal, reassigning, and unassigning another principal without the permission (`forbidden`; `vetoed` when the config names none); assigning the principal it is assigned to, and unassigning an unassigned instance (`vetoed`) |
| `configChange` | the permission may change. Added to a schema with instances, which start unassigned; not removed from one, since their assignments would stay behind |

A principal may assign an unassigned instance to itself, and an assignee
may unassign itself; every other move needs the config's permission. The
engine knows no principals, so it cannot check that another principal
exists: only a principal trusted with the permission names one.

## Queue

Claimable work: a claim that checks an instance can be claimed, takes its
lease and moves its status in one transaction, and `claimNext`, which
claims the first instance the caller can claim.

| | |
| --- | --- |
| Config | `claim`: `{ from, to }`, the Workflow states an instance is claimed in and the one a claim moves it to; `priorityField`, an integer field of the type; `match`, the type's own top-level scalar fields `claimNext` may filter on; `maxCandidates` (1 to 1000; 100 when absent). Requires `Workflow` and `Lease` |
| Fields | none |
| Operations | `claim({ ttlMs? })` -> `{ id, token, expiresAt, heartbeatMs }`, writes; `refresh()` -> `{}`, writes; schema-level `claimNext({ match? })` -> `{ claimed }`, the claim or null, writes |
| Guards | Lease's `acquire` by anything but Queue's own claim: `vetoed`, so the lease of a claimable instance is taken only by claiming it |
| Refusals | `claim` of an instance whose status is not one of `claim.from`, or that a blocker holds up (`vetoed`), and whatever Lease's `acquire`, Budget's `reserve`, Workflow's `transition` and the instance's guards refuse; `claimNext` with a `match` field the config does not name (`invalid_argument`) |
| Events | `claim`'s operation event carries the lease and the status; a blocker's change appends a `refresh` event on each instance it holds up |
| `configChange` | `claim`, `priorityField`, `match` and `maxCandidates` may change. Not added to a schema with instances, which would have no copies for `claimNext` to find them by, and not removed from one |

```json
{ "name": "Queue", "config": { "claim": { "from": ["queued"], "to": "running" }, "priorityField": "priority", "match": ["topic"] } }
```

`claim` reads its checks from the instance as it is. A lapsed lease is
expired first, through Lease's `expire`, so the status `onExpiry` leaves
is the one checked. The status must be one of `claim.from`, and, when
the type composes `Dependencies`, its `blocked` field false. Then it
calls Lease's `acquire` for the caller, Budget's `reserve({})` when the
type composes Budget (which reserves every meter's claim amount and
vetoes a reservation that does not fit), and Workflow's `transition` to
`claim.to`. All of it is one operation: a refusal at any step, a guard's
veto of the transition say, leaves no lease, no reservation and the
status as it was. Assignment's guard keeps an assigned instance's claim
to its assignee, and Lease's guard another principal's claim off an
instance whose lease is active.

`claimNext` reads candidates from Queue's own columns across the schema
(`sql.instances()`), through their index: copies of what a claim checks,
the status, `blocked`, the assignee, Lease's expiries and the priority
field's value, which Queue keeps current in the transaction of every
change of the instance (`afterChange`) and of every change of a blocker
(`afterReferenceChange`, which invokes `refresh`). It copies facts, not
booleans of the config, so a new version's `claim.from` or `maxExpiries`
applies to every instance at once; a new `priorityField` orders an
instance from its next change. A candidate is in a `claim.from` state,
not blocked, below Lease's `maxExpiries`, unassigned or assigned to the
caller, and holds each `match` value (by JSON equality on the instance's
own field). Candidates go highest priority first, an instance without one
last, then oldest, then by id. `claimNext` invokes `claim` on each in
turn, as the caller, until one succeeds, and takes a veto or a conflict
to mean that one cannot be claimed now, up to `maxCandidates`; any other
error ends it. A stale copy costs a skipped candidate, never a wrong
claim.

Queue records a reference to each blocker it reads through
`Dependencies`' `listBlockers`, so a blocker's change in any schema runs
its `afterReferenceChange`, which invokes `refresh` on the dependent as
the principal that changed the blocker, who needs `write` on the
dependent's schema.

The engine writes a file from one process (D16), and its calls are
synchronous, so two claims never interleave: `claimNext`'s scan and the
claim it makes are one transaction under the file's write lock.

## Presence

A heartbeat on an instance that stands for a worker. The instance's
`principalField` holds the subject of the principal it stands for, and
only that principal may beat it.

| | |
| --- | --- |
| Config | `ttlMs` (at least 1000) and `principalField` (a string field of the type), required; `onMissed` and `onBeat` (`{ transition, from }`), `releaseLeases` (schema names), `sweepMs` (at least 1000; 5000 when absent) |
| Fields | `presence`: `{ deadline, lastBeatAt, missed }`, the times in epoch milliseconds or null |
| Operations | `beat()` -> `{ deadline }`; `miss()` -> `{ missed }`. Both write |
| Schedule | `miss`, every `sweepMs`: `miss` on every instance past its deadline and not missed, at most 1000 a run, as the runner's principal |
| Guards | an update that changes `principalField` once it holds a value: `vetoed`, whoever asks |
| Refusals | `beat` by any principal but the one `principalField` names, or on an instance whose `principalField` holds none (`vetoed`) |
| Events | each operation's event; a beat is a write, with its event |
| `configChange` | any config may change but `principalField`. Added to a schema with instances, which have no deadline until their first beat; not removed from one, since their deadlines and misses would stay behind |

```json
{ "name": "Presence", "config": {
    "ttlMs": 30000,
    "principalField": "subject",
    "onMissed": { "transition": "missing", "from": ["idle", "busy"] },
    "onBeat": { "transition": "idle", "from": ["missing"] },
    "releaseLeases": ["jobs"] } }
```

### Deadline and miss

`initialize` sets the first deadline one `ttlMs` after the create, so a
worker that never beats is missed. `beat` moves the deadline to `ttlMs`
from now and clears `missed`; it checks its caller in its handler, and
knowing the subject the instance holds does not make another principal
it. `onBeat`, when the status is one of its `from` states, moves it to
its `transition`, the way back from the state `onMissed` moves it to.

`miss` acts on an instance past its deadline that is not missed yet, and
only once: it sets `missed`, moves the status with `onMissed` when it is
one of its `from` states, so a worker in a terminal state stays there,
and expires the principal's leases on each `releaseLeases` schema
through that schema's `expireHolder`, Lease's schema-level operation, so
each expiry runs Lease's rules (the token, `onExpiry`, the count) and
appends its own event. The leases are the principal's, not the
instance's: while another instance of the schema stands for the same
principal and is present (not missed, before its deadline), a miss
leaves them. A status move a guard vetoes leaves the status as
it is and the instance missed all the same; any other refusal fails the
miss and leaves the instance as it was. Any principal who may write the
instance may call `miss`; on any other instance it returns `{ missed:
false }`. A missed instance stays missed, whatever its status, until it
beats.

`parseConfig` checks that `principalField` is a string field of the
type, that the type lists Workflow for `onMissed` and `onBeat`, that
their states are its states with a transition from each `from` state,
and, when the schema is defined or published, that each `releaseLeases`
schema has a live version that composes `Lease`.

### The sweep

The runner runs the `miss` schedule on every schema that composes
Presence, every `sweepMs` of that schema's config. It reads Presence's
own columns across the schema through the index on `missed` and
`deadline`, oldest deadline first, and invokes `miss` on each instance
due, at most 1000 a run; the rest wait for the next run. It runs as the
runner's principal (`runner: { principal }`), which needs `write` on the
schema and, for `releaseLeases`, Lease's `overridePermission` on each
schema it names, which `expireHolder` asks for, and the permission of
the `onMissed` transition when it names one. A miss that fails fails the
run: nothing of the run commits, `engine.runner.status()` shows the
error, and the runner tries again with its backoff.

Not ported from the source implementation: directives delivered on a
beat, which belong to Lease's holder, and a worker's heartbeat stored in
Lease's columns; Presence keeps its own.

## Blueprint

Children created with their parent: a map of steps, by key, says which
instances of the child schema an instance has and which of them block
which.

| | |
| --- | --- |
| Config | `schema` (the child schema), `parentLink` (a link of its Links config to this schema) and `keyField` (a string field of its type), required; one of `steps` (inline) and `from` (`{ link, field }`); `copyFields`, `copyLinks` |
| Steps | by key (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`, at most 500): `{ after?, when?, data? }`; `after`, the keys of the steps whose children block this one's; `when`, `{ field, equals }` or `{ field, includes }`; `data`, more fields of the child |
| Fields | `blueprint`: `{ children: [{ key, id }] }`, in the order they were created; absent until the instance is stamped |
| Operations | none |
| Guards | with `from`, once stamped, Links' `link` of the `from` link: `vetoed` |
| Refusals | with `from`, a map the pinned revision holds that breaks a rule (`vetoed`, on the link); whatever a child's create, link or edge is refused for (its own error), which refuses the change that stamps |
| `configChange` | any config may change; it applies to the stamps that come after. Added to a schema with instances; not removed from one, since the record of what was stamped would stay behind |

```json
{ "name": "Blueprint", "config": {
    "schema": "steps",
    "parentLink": "batch",
    "keyField": "step",
    "steps": {
      "fetch": {},
      "check": { "after": ["fetch"], "when": { "field": "topic", "equals": "search" } },
      "index": { "after": ["check"], "data": { "title": "Index what was fetched" } } },
    "copyFields": ["topic"] } }
```

### Stamping

Stamping creates a child per step with `instances.create`, its fields its
key in `keyField`, the `copyFields` this instance holds, then the step's
`data`; links it to this instance through the child's own `Links.link`
for `parentLink`, and for each `copyLinks` link this instance holds, with
the revision a pinned one records when the child's link is pinned too;
and adds its edges with the child's own `Dependencies.addBlocker`. Each
child runs its behaviors' `initialize` and `afterChange`, its guards and
its events. Everything runs in one transaction, as the principal that
made the change, who therefore needs `write` on the child schema and
`read` on this one: a failure at any child refuses the change, and the
parent, every child, link and edge roll back together. Children come in
an order where each follows its blockers, ties in the map's order. A
child schema that composes `Queue` makes the children claimable work:
`claimNext` claims each once its blockers are in a terminal state, so the
steps are claimed in the order their edges give.

Inline `steps` are stamped in the instance's create. A create sets no
link, since a link is set by `Links.link` on an instance that exists, so
`from` steps are stamped when the `from` link is first set, in that
link's transaction: the link and the children commit together, or
neither does. The map is read from the revision the link pins, through
`Revisions`' `listRevisions` on the definition, as the principal (who
needs `read` on its schema): a later revision of the definition changes
only what is stamped from then on, and `link` with a `revision` stamps
an earlier one. `copyLinks` needs `from`, since an instance holds no
link at its create.

### when

A step's `when` holds when this instance's field equals the value, or is
a list that includes it, both compared as JSON, without coercion: `1`
does not equal `"1"`, and a field the instance does not hold equals
nothing. A step whose `when` does not hold is left out, and the steps
that come after it come after the steps it came after instead,
transitively, so the chain stays connected: with `check` left out,
`index` comes after `fetch`. An included step keeps the edges it had to
other included steps.

### The checks

When the schema is defined or published, `parseConfig` checks the child
schema's live version as the definer may read it: it composes Links with
`parentLink` pointing at this schema (and this type lists Revisions
before Blueprint when that link is pinned, so the parent has a revision
to pin), Dependencies with its own schema among its
blockers' schemas when a step has `after`, and not Blueprint; `keyField`
is a string field of its type, `copyFields` are fields of both types of
one JSON type, and `data` sets fields of its type. `when` names a field
of this type, a list for `includes`; `data` does not set `keyField`;
inline steps name only steps the map has in `after` and form no cycle,
which the refusal names, whatever their `when`. `from` names a pinned
link of this type's Links to a schema that composes Revisions and has
the field, an object or JSON; `copyLinks` are links of both Links
configs to one schema. A map read through `from` is held to the same
rules against this type when it is stamped.

### What was stamped

Blueprint keeps what it stamped in its own table, so `blueprint` lists
each step's key and its child's id from that record and not from the
links that point here: a child linked to the instance later is not among
them, and one deleted later still is.

Not ported from the source implementation: create governance, which
checked the child schema's own creates for a parent link and an edge,
and the trial flag. A step map is checked when it is stamped, not when
the definition is written.
## Budget

Reserve-then-settle budgets in units the deployment names: each meter
counts what the instance has used and the reservations it holds against
its limit, and passes both to the enclosing scopes its `scope` link
points at.

| | |
| --- | --- |
| Config | `meters` (required, at least one, by camelCase name): each `limit` (at least 1) or `limitField` (an integer field of the type), `reserve` (at least 1) or `reserveField` (an integer field of the type), `scope` (a link of the type's `Links` config) and `reset` (`daily`), all optional; `limitPermission`; `onExceeded` (`{ direct }`, needs `Lease`) |
| Fields | `budget`: by meter, `{ used, reserved, limit, remaining }`, `limit` and `remaining` null without a limit; `reserved` counts what the instance holds for the instances inside it |
| Operations | `reserve({ meter?, amount? })` -> `{ reserved }` by meter; `recordUsage({ meter, amount })` -> `{ meter, used, released, overruns, directed }`; `settle({ meter? })` -> `{ released }` by meter; `setLimit({ meter, limit })` -> `{ meter, limit, previous }`; the scope side, `reserveFor`, `settleFor` and `recordUsageFor({ meter, schema, id, amount, ... })`. All write |
| Guards | while the instance has a reservation of a meter, a `Links` `link` or `unlink` of the meter's scope link is `vetoed`; a change of a meter's `limitField` without `limitPermission` is `forbidden` (`vetoed` when the config names none), and below what is used and reserved `vetoed` |
| Refusals | `reserve` that does not fit here or in a scope, and on a type with `Lease` without an active lease (`vetoed`); an unknown meter, an amount without a meter, a meter without a configured reservation and no amount (`invalid_argument`); `setLimit` without `limitPermission` (`forbidden`, or `vetoed` when the config names none) and below what is used and reserved (`vetoed`); a scope operation from an instance that does not draw the meter from the scope (`invalid_argument`) or for more than it reserved (`vetoed`). `recordUsage` is never refused for its amount |
| Events | each operation's event, on the instance and on every scope it reaches |
| `configChange` | a meter cannot be removed; anything else may change. Added to a schema with instances, whose meters start empty; not removed from one |

```json
{ "name": "Budget", "config": {
    "meters": { "cpuSeconds": { "limit": 3600, "reserve": 600, "scope": "pool", "reset": "daily" } },
    "limitPermission": "jobs.budget",
    "onExceeded": { "direct": "budgetExceeded" } } }
```

### Reservations and usage

A reservation fits while the meter's used plus reserved plus the amount
is within its limit: the config's `limit`, the instance's `limitField`,
or the one `setLimit` set. `reserve` with no meter takes every meter
whose `reserve` or `reserveField` gives an amount; that is the call
Queue's claim makes once the lease is taken, so a claim that does not fit
is refused with no lease, no reservation and its status as it was, and
`claimNext` moves on to the next candidate. The instance keeps its own
reservation apart from what it holds for others, so `settle` releases
exactly what its claims reserved.

`recordUsage` is never refused: usage has happened. It adds the amount
to `used` and releases the part of it the instance's own reservation
covers, `min(amount, reservation)`, and exactly that part at every scope,
so usage beyond a reservation never eats the reservations other
instances hold in a scope. The result lists every instance of the chain
whose usage is over its limit afterwards, this one first.

### Scopes

A meter's `scope` names a link of the type's `Links` config; its target
is the enclosing scope, a pool the work draws on, say, and its schema
composes `Budget` with the same meter, which the package checks when the
schema is defined. Budget never writes another instance's rows: the
instance invokes `reserveFor`, `settleFor` or `recordUsageFor` on its
scope, as the caller, which applies the scope's own limit, records what
it holds for the instance, appends the scope's own event and passes the
change on up its own scope. It all runs in one transaction, so a
reservation one scope refuses leaves nothing anywhere in the chain. The
access policy and the scope's guards are asked as for any operation: a
scope type that composes `Lease` lists the three in `exempt` to let
other principals' work reach it while it is leased.

The scope operations cannot free what an instance still holds. A scope
reads the instance's `budget` field first: it releases at most what it
holds for the instance beyond what the instance still has reserved,
holds no more than the instance has reserved, and takes reservations and
usage only from an instance whose scope link for the meter points at it.
A scope link does not move while a reservation is held through it.

### Settlement

On a type that composes `Lease`, a reservation is made under the active
lease, which `reserve` needs, and lasts as long as it: after a caller's
`Lease` or `Queue` operation, and before each `reserve`, the reservations
of a lease that is no longer active are settled. That covers a release,
an expiry, the runner's sweep, `expireHolder`, and a claim or an acquire
over a lapsed lease. Deleting the instance
settles everything it reserved and holds, up the chain. On a type
without `Lease`, a reservation lasts until `settle` or the delete.

### Overruns and directives

With `onExceeded`, usage that takes the instance or a scope over its
limit sends `onExceeded.direct` to the holder of the instance's active
lease through Lease's `direct`, with data `{ meter, used, limit, scope
}`, once per meter per lease token. A caller without a lease learns of
the overrun from `recordUsage`'s result. Lease's guard lets a call() of
`direct` by another behavior of the type through without a permission,
so the principal that records the usage needs none; a refusal sends
nothing and does not refuse the usage.

### Limits and daily meters

`setLimit` raises or lowers a limit, never below what is used and
reserved, and needs `limitPermission`; a meter with `limitField` writes
the field through `update()`, with an update's checks, and a direct
update of the field is held to the same rules. A lease holder's lease
keeps others' `setLimit` out unless `Lease` exempts `Budget.setLimit`.

A daily meter's usage counts from the start of the UTC day on the
engine's clock. A read never writes: a meter whose day has passed reads
as used 0, and the next write of its row starts the day. Reservations
carry over.

## Development

```
cd runtime/schema/typescript && bun install --frozen-lockfile && bun run build
cd runtime/http/typescript && bun install --frozen-lockfile && bun run build
cd runtime/engine/typescript && bun install --frozen-lockfile && bun run build
cd runtime/engine-workqueue/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test        # build, then test:node (node --test) and test:bun (bun test)
```

The tests import the built package from `dist/` and the engine from
`node_modules`, open real SQLite files in temporary directories with a
clock they move, and on Bun run against both adapters.
`test/package.test.ts` runs the documents the core binary builds in
`make cli-smoke` (`fixture-workqueue-json`).
