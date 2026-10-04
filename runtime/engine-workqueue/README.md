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
created with their parent; `Budget`, reserve-then-settle budgets across
enclosing scopes; and `Retries`, failure classes with caps, kept results
and stuck detection. That is every work-queue behavior D16 lists.

```ts
import { openEngine } from '@superschematic/engine';
import { workQueueBehaviors } from '@superschematic/engine-workqueue';

const engine = openEngine({ path: 'jobs.db', policy, behaviors: workQueueBehaviors, runner: { principal: sweeper } });
engine.runner.start();   // the lease sweep runs on the runner

const { claimed } = engine.instances.invokeSchema(worker, 'jobs', 'claimNext', { match: { topic: 'search' } });
const fenced = { preconditions: { Lease: { token: claimed.token } } };
engine.instances.invoke(worker, 'jobs', claimed.id, 'heartbeat', {}, fenced);
engine.instances.invoke(worker, 'jobs', claimed.id, 'transition', { to: 'done' }, fenced);
```

## Registering the behaviors

A deployment passes the implementations as the engine's `behaviors`
option when it opens the engine, after the core's, which the engine
registers itself: `workQueueBehaviors` is every one, and `lease`,
`assignment`, `queue`, `presence`, `blueprint`, `budget` and `retries` are
exported one by one for a deployment that runs only some.
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
| Config | `ttlMs` (at least 1000; 60000 when absent), `heartbeatMs` (at most half of `ttlMs`; a third of it when absent), `sweepMs` (at least 1000; 5000 when absent), `maxHoldMs`, `maxHoldField`, `onExpiry` and `escalate` (`{ transition, from }`), `maxExpiries`, `exempt`, `requireToken`, `acquirePermission`, `overridePermission`, `directPermission`; all optional |
| Precondition | `{ token }`, the lease's current token: `preconditions: { Lease: { token } }` on any write |
| Fields | `lease`: `{ holder, token, acquiredAt, renewedAt, expiresAt, active, expiries, ended }`, `holder`, `acquiredAt`, `renewedAt` (its acquire or last heartbeat) and `expiresAt` null when it is free; `ended`, `{ reason, at }`, how the last lease ended, null while one is held |
| Operations | `acquire({ ttlMs? })` -> `{ token, expiresAt, heartbeatMs }`; `heartbeat({ acknowledge? })` -> `{ expiresAt, directives }`; `release({ abandon? })` -> `{}`; `expire({ holder?, notRenewedAfter? })` -> `{ expired, reason? }`; `direct({ name, data?, dedupeKey? })` -> `{ id, created }`; `acknowledge({ ids })` -> `{}`; `resetExpiries()` -> `{ expiries }`; schema-level `expireHolder({ holder, notRenewedAfter? })` -> `{ expired, reasons, ids }`. All write; `heartbeat`, `acknowledge` and the holder's `release` present the token |
| Schedules | `expire`, every `sweepMs`, on the engine's runner |
| Guards | a write that presents a token other than the current one, whoever calls (`token_stale`); while a lease is active, an update, a delete or a writing operation of another behavior by any principal but the holder (`held_by_another`), except an `exempt` operation, a read-only one, Queue's `refresh` and a principal with `overridePermission`; with `requireToken`, such a write by the holder that presents no token (`token_required`); once the lease has lapsed, the holder's writes (`lapsed`); while a lease is held, a change to `maxHoldField` without `overridePermission` (`hold_limit_fixed`); a principal's `direct` needs its permission (below). All `vetoed` |
| Refusals | `acquire` while a lease is active (`held_by_caller`, `held_by_another`) and at `maxExpiries` (`max_expiries`, details `{ expiries, maxExpiries }`), without `acquirePermission` (`forbidden`), with a `ttlMs` past the config's (`invalid_argument`); `heartbeat` and `acknowledge` with no lease (`not_leased`), by another principal (`not_holder`), with no token (`token_required`) or once the lease has lapsed (`lapsed`); `release` with no lease (`not_leased`), by another principal (`forbidden` without `overridePermission` when the config names one, `not_holder` when it names none), by the holder with no token (`token_required`), once the lease has lapsed (`lapsed`); `direct` by a principal without its permission (`forbidden`) or with no permission in the config (`not_configured`), and with no active lease (`not_leased`, `lapsed`); `expire` with a `holder`, and `expireHolder`, without `overridePermission` (`forbidden`, the config naming none included), and `expire` with `notRenewedAfter` and no `holder` (`invalid_argument`); `acknowledge`, and `heartbeat`'s `acknowledge`, of an id not sent under the token (`invalid_argument`, and nothing is acknowledged); `resetExpiries` without `overridePermission` (`forbidden`, or `not_configured` when the config names none). Each code is a veto's (`vetoed`) |
| Events | each operation's event; a heartbeat is a write, with its event; the event of a release, an abandon and an expiry carries `lease.ended` in its patch |
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
name one lease. `acquire` returns it, and a caller presents it as Lease's
precondition, the one way any write presents it:

```ts
const fenced = { preconditions: { Lease: { token } } };
engine.instances.invoke(worker, 'jobs', id, 'heartbeat', {}, fenced);
engine.instances.update(worker, 'jobs', id, { title: 'Indexed' }, fenced);
```

Over HTTP it is the `Preconditions` header, `{"Lease": {"token": 7}}`,
and over MCP the tools' `preconditions` argument
(`runtime/engine/README.md`, "Vetoes and preconditions").

- The guard refuses a write that presents a token other than the current
  one, whoever calls (`token_stale`, details `{ token, current }`), on a
  free instance too. A worker fleet usually runs as one principal: a
  process whose lease lapsed and was taken again by a sibling holds a
  stale token, and its writes are refused, while the sibling's go
  through. A lease that ended leaves its token stale, and the current
  token on a free instance makes `acquire` a compare-and-set.
- `heartbeat`, `acknowledge` and the holder's `release` need the token
  (`token_required`) and the holder. A principal with
  `overridePermission` releases without one.
- With `requireToken`, every other write under an active lease needs it
  too, the holder's own included (`token_required`). Exempt and read-only
  operations, Queue's `refresh`, a request a behavior's own code makes,
  Lease's own operations (the runner's `expire` among them) and a
  principal with `overridePermission` are not held to it. Without it, a
  write that presents no token is its caller's, and the guard checks who
  calls.

It is not a capability: the `lease` field shows it to every reader, and
the guard still checks who calls. It fences one principal's processes
from each other; principals are fenced by who holds the lease.

### Active, lapsed and expired

A lease is active while it is held, before `expiresAt`, and within its
longest hold since `acquire`: the instance's `maxHoldField`, when the
field holds a positive integer, else `maxHoldMs`. A heartbeat moves
`expiresAt` to the time plus the lease's length, never past the longest
hold, so a lease is never held longer, however often it is renewed. Each
lease keeps the length it was acquired with: `acquire({ ttlMs })` takes a
length up to the config's, and returns `heartbeatMs` scaled to it.

`heartbeatMs` is at most half of `ttlMs`, so a heartbeat can be late by
a whole interval and the lease holds; `parseConfig` refuses more.

A lease past either time has lapsed. It gives its holder nothing: the
holder's heartbeat, release, acknowledgement and writes are refused
(`lapsed`), and other principals' writes go through as on a free
instance. Only its expiry can follow. `expire` applies it, and any
principal who may write the instance may call it; on an instance whose
lease is free or active it returns `{ expired: false }`, and its event is
all it writes. `acquire` over a lapsed lease applies its expiry first,
and so does Queue's `claim`.

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

Once the instance has had `maxExpiries` expiries, `acquire` is refused
(`max_expiries`), and so is an acquire whose expiry of a lapsed lease
would reach the cap. `resetExpiries`, which needs `overridePermission`,
sets the count back to 0.

A release applies `onExpiry` too, without counting an expiry, so a holder
that hands work back leaves it where it can be taken again, and one that
finished it leaves it finished. A holder that gives the work up as
failed releases with `abandon: true`: that counts as an expiry, as the
lapse of its lease would, `onExpiry` or at the cap `escalate` moves the
status, and an instance a worker keeps taking and dropping reaches
`maxExpiries` instead of looping. An abandon in a terminal state does not
count.

An expiry says why (`reason`): `ttl`, its holder stopped renewing it;
`maxHold`, it reached its longest hold, so its holder renewed it and did
not finish; `holder`, it was active and `expire({ holder })` or
`expireHolder` expired it by its holder's name (a lapsed lease expired
that way gives its lapse). `expireHolder` returns `{ expired, reasons }`,
how many for each reason. The `lease` field's `ended` records how the
last lease ended, `{ reason, at }`, with `release` and `abandon` beside
the expiry reasons, and `acquire` clears it, so the operation event of
every end carries it in its patch. An expiry that an `acquire` or a
claim applies first shows only as the count and the token.

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
expired, by reason, and the ids of the instances; a presence check calls
it as the runner's principal. Both need `overridePermission`, and are
refused when the config names none. `notRenewedAfter`, a time, spares an
active lease its holder acquired or renewed after it (`renewedAt`): a
process whose own heartbeats go on is alive whatever else says its
principal is gone, and its lease expires on its own if they stop. A
lapsed lease expires all the same.

### The guard

First, the guard refuses a write that presents a stale token, whoever
calls (above). While a lease is active, it refuses an update, a delete
and every writing operation of another behavior by any principal but the
holder (`held_by_another`), reading `writes` from the operation's guard
request, and with `requireToken` the holder's that presents no token.
These pass:

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
  a principal's `direct`, whose permission the guard asks;
- a create, which holds no lease, so neither the token check nor
  `requireToken` refuses one.

An operation another behavior's reference hook invokes on a leased
instance, as `Dependencies`' `removeBlocker` when a blocker is deleted or
`Links`' `unlink` when a target is, runs as the principal of that change
and is refused like any other, so that change is refused too; a
deployment that wants it through lists it in `exempt`. Nothing changes
the field `maxHoldField` names while a lease is held, unless with
`overridePermission`, so a holder cannot extend its own hold.

### Directives

A directive is a message to the holder of the active lease: `direct({
name, data?, dedupeKey? })`. A principal's `direct` needs `directPermission`, or
`overridePermission` when that is absent, and is refused when the config
names neither; the guard asks it before any other check. Another behavior
of the type sends one with `call('Lease', 'direct', ...)`, as the
principal it runs for and without either permission: a budget whose
usage runs over tells the holder so as the principal that recorded the
usage. It attaches to the current token and is numbered within it, 1, 2, 3, ....
Every heartbeat returns the directives of its lease that are not
acknowledged, oldest first, as `{ id, name, data?, dedupeKey?, createdAt,
createdBy }`; delivery is at least once, and `acknowledge({ ids })`, with
the token, stops it. `heartbeat({ acknowledge: ids })` acknowledges them
in the heartbeat's own write, before it lists the rest, so a worker pays
no write and no event of its own per acknowledgement. With `dedupeKey`, a
directive already sent under the lease with the key, acknowledged or not,
stands: `direct` returns its id with `created: false` and sends nothing,
so a sender that runs on every change (an alert, a watchdog) does not
queue the same message twice for one lease.
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
| Guards | while the instance is assigned, Lease's `acquire` and Queue's `claim` by any principal but the assignee: `vetoed` (`assigned_to_another`), whoever made the call |
| Refusals | assigning another principal, reassigning, and unassigning another principal without the permission (`forbidden`; `not_configured` when the config names none); assigning the principal it is assigned to (`already_assigned`), and unassigning an unassigned instance (`not_assigned`). Each code is a veto's (`vetoed`) |
| `configChange` | the permission may change. Added to a schema with instances, which start unassigned; not removed from one, since their assignments would stay behind |

A principal may assign an unassigned instance to itself, and an assignee
may unassign itself; every other move needs the config's permission. The
engine knows no principals, so it cannot check that another principal
exists: only a principal trusted with the permission names one.

## Queue

Claimable work: a claim that checks an instance can be claimed, takes its
lease and moves its status in one transaction, `claimNext`, which claims
the first instance the caller can claim, and `countClaimable`, which
counts them.

| | |
| --- | --- |
| Config | `claim`: `{ from, to }`, the Workflow states an instance is claimed in and the one a claim moves it to; `priorityField`, an integer field of the type; `match`, the type's own top-level scalar fields `claimNext` may filter on; `maxCandidates` (1 to 1000; 100 when absent). Requires `Workflow` and `Lease` |
| Fields | none |
| Operations | `claim({ ttlMs? })` -> `{ id, token, expiresAt, heartbeatMs }`, writes; `refresh()` -> `{}`, writes; schema-level `claimNext({ match?, assignedOnly?, ttlMs? })` -> `{ claimed }`, the claim or null, writes; schema-level `countClaimable({ match?, assignedOnly? })` -> `{ count }`, read-only. A `match` value is a value or a list of values |
| Guards | Lease's `acquire` by anything but Queue's own claim: `vetoed` (`claim_required`), so the lease of a claimable instance is taken only by claiming it |
| Refusals | `claim` of an instance whose status is not one of `claim.from` (`not_claimable`, details `{ status, from }`), or that a blocker holds up (`blocked`), and whatever Lease's `acquire`, Budget's `reserve`, Workflow's `transition` and the instance's guards refuse; `claimNext` and `countClaimable` with a `match` field the config does not name, or `assignedOnly` on a type without Assignment (`invalid_argument`); `claimNext` whose every candidate's claim was `forbidden` to the caller (that `forbidden`). Each code is a veto's (`vetoed`) |
| Events | `claim`'s operation event carries the lease and the status; a blocker's change appends a `refresh` event on each instance it holds up, and an enclosing budget scope's change one on each instance whose exclusion it moves |
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
the status, `blocked`, the assignee, Lease's expiries, the priority
field's value and until when the instance is excluded, which Queue keeps
current in the transaction of every change of the instance
(`afterChange`) and of every change of what it waits on
(`afterReferenceChange`, which invokes `refresh`). It copies facts, not
booleans of the config, so a new version's `claim.from` or `maxExpiries`
applies to every instance at once; a new `priorityField` orders an
instance from its next change. A candidate is in a `claim.from` state,
not blocked, below Lease's `maxExpiries`, not excluded now, unassigned or
assigned to the caller (only assigned to it with `assignedOnly`), and
holds each `match` value, or one of a list, by JSON equality on the
instance's own field. Candidates go highest priority first, an instance
without one last, then oldest, then by id. `claimNext` invokes `claim` on
each in turn, as the caller, with its `ttlMs`, until one succeeds, and
takes a veto, a conflict or a `forbidden` to mean that one cannot be
claimed by this caller now, up to `maxCandidates`; any other error ends
it. When every candidate it tried was `forbidden`, it throws the first, so
a caller that may claim none of them learns why rather than that there
is no work. A stale copy costs a skipped candidate, never a wrong claim.

`countClaimable` counts the same candidates for the caller, with no
`maxCandidates`, and claims nothing: a read-only signal of the work
waiting, for an autoscaler say.

The engine writes a file from one process (D16), and its calls are
synchronous, so two claims never interleave: `claimNext`'s scan and the
claim it makes are one transaction under the file's write lock.

### Exclusion

A claim is refused for more than its status and blockers. An instance
over its budget, or exhausted, would be tried by every `claimNext` and
refused, and enough of them at the head of the order (more than
`maxCandidates`) would make `claimNext` return null while claimable work
waits behind them. So for an instance that its status and blockers make a
candidate, Queue copies until when it is excluded (`excluded_until`),
which the candidate query compares with the time:

- with Retries on the type, while its `retries` field shows it exhausted:
  until a change;
- with Budget on the type, while its `checkReserve` (Budget, below)
  says the reservation a claim makes does not fit, here or in an
  enclosing scope: until the start of the next UTC day when that day
  alone makes it fit, so an instance over a daily meter comes back with
  no write; else until a change.

Queue records a reference to each enclosing scope `checkReserve` read,
while the instance is a candidate but for its budget, so a scope's change
in any schema (a reservation, a usage, a settlement, a new limit) runs
its `afterReferenceChange`. That checks the instance's exclusion through
`checkReserve`, as the principal who changed the scope, and invokes
`refresh` only when the copy no longer matches. A scope's change costs a
check of each instance waiting under it, and an event on each whose fit
it moves. A change the instance's own write makes in a scope, its claim's
reservation say, is left to that write (the context's `writing`): its own
`afterChange` refreshes it.

Queue records a reference to each blocker it reads through
`Dependencies`' `listBlockers` too, so a blocker's change in any schema
runs its `afterReferenceChange`, which invokes `refresh` on the dependent.
A create's `afterChange` runs after every `initialize`, so an instance a
create gives blockers or a scope link (`Dependencies`' and `Links`'
create parameters) has its copies and its references from its create
event: it is never a candidate before its edges or its scope exist.
Either change runs as the principal that made it, who needs `write` on
the dependent's schema, and a change of a candidate needs `read` on the
schemas of its blockers and scopes.

## Presence

A heartbeat on an instance that stands for a worker. The instance's
`principalField` holds the subject of the principal it stands for, and
only that principal may beat it.

| | |
| --- | --- |
| Config | `ttlMs` (at least 1000) and `principalField` (a string field of the type), required; `onMissed` and `onBeat` (`{ transition, from }`), `releaseLeases` (schema names), `sweepMs` (at least 1000; 5000 when absent) |
| Fields | `presence`: `{ deadline, lastBeatAt, missed, released }`, the times in epoch milliseconds or null; `released`, the instances whose lease the last miss expired, by schema, null before a miss |
| Operations | `beat()` -> `{ deadline }`; `miss()` -> `{ missed, released? }`. Both write |
| Schedule | `miss`, every `sweepMs`: `miss` on every instance past its deadline and not missed, at most 1000 a run, as the runner's principal |
| Guards | an update that changes `principalField` once it holds a value: `vetoed` (`principal_fixed`), whoever asks |
| Refusals | `beat` by any principal but the one `principalField` names (`not_principal`), or on an instance whose `principalField` holds none (`no_principal`). Each code is a veto's (`vetoed`) |
| Events | each operation's event; a beat is a write, with its event; a miss's carries `presence.released` in its patch |
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
appends its own event. It passes `notRenewedAfter`, the principal's last
beat on any instance of the schema that stands for it (its create when it
never beat), so a lease renewed since is spared: a worker whose presence
beat starved while its lease heartbeats went on keeps its live work, and
a lease that stops being renewed expires on its own. A lease whose
heartbeat interval is longer than the presence `ttlMs` can go a whole
presence interval unrenewed while its holder is alive; keep lease
heartbeats at most the presence `ttlMs` apart. The miss records the ids
it expired, by schema, in `presence.released`, so its event shows an
operator what it released. The leases are the principal's, not the
instance's: while another instance of the schema stands for the same
principal and is present (not missed, before its deadline), a miss
leaves them, and records `released: {}`. A status move a guard vetoes leaves the status as
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
| Config | `schema` (the child schema, which composes `Constants` over `keyField` and every copied field), `parentLink` (a link of its Links config to this schema) and `keyField` (a string field of its type), required; one of `steps` (inline) and `from` (`{ link, field }`); `copyFields`, `copyLinks` |
| Steps | by key (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`, at most 500): `{ after?, when?, data? }`; `after`, the keys of the steps whose children block this one's; `when`, `{ field, equals }` or `{ field, includes }`; `data`, more fields of the child |
| Fields | `blueprint`: `{ children: [{ key, id }] }`, in the order they were created; absent until the instance is stamped |
| Operations | none |
| Guards | with `from`, once stamped, Links' `link` of the `from` link: `vetoed` (`stamped`) |
| Refusals | with `from`, a link that records no revision (`no_revision`), a revision the principal cannot read (`unreadable`), a map the pinned revision holds that breaks a rule (`invalid_steps`); steps with `after` when the child schema does not compose Dependencies (`no_dependencies`); a child schema whose live version's `Constants` no longer keeps `keyField` and every copied field (`not_constant`, with the fields it does not keep in `details.fields`); each `vetoed`, on the create that stamps (action `create`) or on the link; whatever a child's create, with its links and edges, is refused for (its own error, a Links or Dependencies veto with its code), which refuses the change that stamps. Each code is a veto's (`vetoed`) |
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

The child schema, `steps`, keeps what a stamp sets:

```json
{ "name": "Constants", "config": { "fields": ["step", "topic"] } }
```

### Stamping

Stamping creates a child per step with `instances.create`, its fields its
key in `keyField`, the `copyFields` this instance holds, then the step's
`data`, and its links and edges as create parameters
(`runtime/engine/README.md`, "Create parameters"): `Links` gets
`parentLink`, pointing at this instance, and each `copyLinks` link this
instance holds, with the revision a pinned one records when the child's
link is pinned too; `Dependencies` gets the children of the steps it
comes after as its blockers. A child therefore holds its parent link
from its create, so the child schema can make `parentLink` `required`,
and it is blocked in its create event, so `claimNext` never finds it
before its edges exist. Each child runs its behaviors' guards,
`initialize` and `afterChange` and gets one create event. Everything
runs in one transaction, as the principal that made the change, who
therefore needs `write` on the child schema and `read` on this one: a
failure at any child refuses the change, and the parent, every child,
link and edge roll back together. Children come in
an order where each follows its blockers, ties in the map's order. A
child schema that composes `Queue` makes the children claimable work:
`claimNext` claims each once its blockers have finished, in a terminal
state whose outcome the child's `Dependencies` `satisfiedBy` lists (a
success by default), so the steps are claimed in the order their edges
give, and the steps after one that failed are not claimed.

Inline `steps` are stamped in the instance's create. `from` steps are
stamped when the `from` link is first set: in the create when the
create gives it (`Links` create parameters), else in the transaction of
the first `link`, so the link and the children commit together, or
neither does, and a refused stamp refuses the create or the link. The
map is read from the revision the link pins, through `Revisions`'
`listRevisions` on the definition, as the principal (who needs `read`
on its schema): a later revision of the definition changes only what is
stamped from then on, and a `revision` given with the link stamps an
earlier one. `copyLinks` copies the links the instance holds when it is
stamped: for inline steps, the ones its create gives.

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
to pin), `Constants` over `keyField` and every copied field,
Dependencies with its own schema among its
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

A child is routed by what its stamp set: the step it is, in `keyField`,
and the fields it copied. Any writer of the child, the holder of its
lease included, could change them after and turn it into another step,
so the child schema's `Constants` (`runtime/engine/README.md`) keeps
them: an update that changes one is `invalid_instance`, with the rule
`constant` at the field. A later version of the child schema can drop
them from `Constants`; this schema's published version is not refused
for that, as a published version is not refused for another schema's
change, but a stamp checks the child schema's live version again, as it
checks Dependencies for `after`, and is `vetoed` (`not_constant`) when
its `Constants` no longer keeps them, so no child is stamped whose route
a writer could change.

### What was stamped

Blueprint keeps what it stamped in its own table, so `blueprint` lists
each step's key and its child's id from that record and not from the
links that point here: a child linked to the instance later is not among
them, and one deleted later still is.

Create governance, which refused a child created without its parent
link, is schema config here: a `required` `parentLink` in the child
schema's `Links` config makes every create of a child give it, whoever
creates it, and a create gives its edges with it through `Dependencies`'
create parameters. No config requires a child to have an edge. Not
ported from the source implementation: the trial flag. A step map is
checked when it is stamped, not when the definition is written.
## Budget

Reserve-then-settle budgets in units the deployment names: each meter
counts what the instance has used and the reservations it holds against
its limit, and passes both to the enclosing scopes its `scope` link
points at.

| | |
| --- | --- |
| Config | `meters` (required, at least one, by camelCase name): each `limit` (at least 1) or `limitField` (an integer field of the type), `reserve` (at least 1) and `reserveField` (an integer field of the type, whose positive value replaces `reserve`), `scope` (a link of the type's `Links` config) and `reset` (`daily`), all optional; `limitPermission`; `onExceeded` (`{ direct }`, needs `Lease`); `escalate` (`{ transition, from }`, needs `Workflow`) |
| Fields | `budget`: by meter, `{ used, reserved, limit, remaining }`, `limit` and `remaining` null without a limit; `reserved` counts what the instance holds for the instances inside it |
| Operations | `reserve({ meter?, amount? })` -> `{ reserved }` by meter; `checkReserve({ meter?, amount? })` -> `{ fits, until, scopes }`, read-only; `recordUsage({ meter, amount })` -> `{ meter, used, released, overruns, directed }`, each overrun `{ schema, id, used, limit, escalated }`; `settle({ meter? })` -> `{ released }` by meter; `setLimit({ meter, limit })` -> `{ meter, limit, previous }`; the scope side, `reserveFor`, `settleFor` and `recordUsageFor({ meter, schema, id, amount, ... })`. All but `checkReserve` write |
| Guards | while the instance has a reservation of a meter, a `Links` `link` or `unlink` of the meter's scope link is `vetoed` (`scope_reserved`); a change of a meter's `limitField` without `limitPermission` is `forbidden` (`not_configured` when the config names none), and below what is used and reserved `vetoed` (`below_committed`) |
| Refusals | `reserve` that does not fit here or in a scope (`over_limit`, details `{ meter, amount, remaining, limit, scope }`, `scope` the instance whose limit refused it), through a scope its link has moved from while the old one holds a reservation (`scope_moved`), and on a type with `Lease` without an active lease (`not_leased`); an unknown meter, an amount without a meter, a meter without a configured reservation and no amount (`invalid_argument`); `setLimit` without `limitPermission` (`forbidden`, or `not_configured` when the config names none) and below what is used and reserved (`below_committed`); a scope operation from an instance that does not draw the meter from the scope (`invalid_argument`) or for more than it reserved (`exceeds_reservation`). `recordUsage` is never refused for its amount. Each code is a veto's (`vetoed`) |
| Events | each operation's event, on the instance and on every scope it reaches; an escalation's status in the event of the usage that caused it |
| `configChange` | a meter cannot be removed; anything else may change. Added to a schema with instances, whose meters start empty; not removed from one |

```json
{ "name": "Budget", "config": {
    "meters": { "cpuSeconds": { "limit": 3600, "reserve": 600, "reserveField": "cpuEstimate", "scope": "pool", "reset": "daily" } },
    "limitPermission": "jobs.budget",
    "onExceeded": { "direct": "budgetExceeded" },
    "escalate": { "transition": "paused", "from": ["running"] } } }
```

### Reservations and usage

A reservation fits while the meter's used plus reserved plus the amount
is within its limit: the config's `limit`, the instance's `limitField`,
or the one `setLimit` set. `reserve` with no meter takes every meter
whose `reserveField` or `reserve` gives an amount: the instance's
`reserveField` when it holds a positive integer, else the config's
`reserve`, as Lease's `maxHoldField` falls back to `maxHoldMs`. That is
the call Queue's claim makes once the lease is taken, so a claim that
does not fit is refused with no lease, no reservation and its status as
it was, and `claimNext` moves on to the next candidate.

`checkReserve`, which takes `reserve`'s parameters, says whether it
would fit now, here and at every scope up the chain, read through their
`budget` and `links` fields, and changes nothing. It counts the own
reservation of a lease that is no longer active as settled, as `reserve`
settles it first, and asks for no lease. When it does not fit, `until`
is the start of the next UTC day if the daily meters starting again make
it fit with nothing else changing, else null; `scopes` lists the scopes
it read, innermost first. Queue copies the answer, so work over its
budget is not tried (Queue, "Exclusion"). The instance keeps its own
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

### Overruns, directives and escalation

With `onExceeded`, usage that takes the instance or a scope over its
limit sends `onExceeded.direct` to the holder of the instance's active
lease through Lease's `direct`, with data `{ meter, used, limit, scope
}`, once per meter per lease token. A caller without a lease learns of
the overrun from `recordUsage`'s result. Lease's guard lets a call() of
`direct` by another behavior of the type through without a permission,
so the principal that records the usage needs none; a refusal sends
nothing and does not refuse the usage.

With `escalate`, usage that leaves an instance over a limit moves its
status to `escalate.transition` when it is one of `from`, through
Workflow's `transition`, as the caller, in the usage's transaction. Each
level applies its own config: the instance's own usage moves it, and the
usage an instance inside a scope passes up moves the scope, a pool
paused say, in `recordUsageFor`. A move a guard vetoes or the caller may
not make leaves the status, since usage is never refused; each overrun
says whether its instance moved (`escalated`). `parseConfig` checks that
the type lists Workflow, that the states are its states, and that a
transition leads from each `from` state.

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

## Retries

Retries per failure class: each attempt is recorded, counted against its
class's cap and a total, and an instance whose caps run out is exhausted
and moved to `exhaustedState`. Requires `Workflow`. There is no backoff
in time: an instance that may run again may be taken again at once.

| | |
| --- | --- |
| Config | `classes` (required, by name: `{ attempts, hint? }`, `attempts` at least 1, or `"terminal"`), `totalAttempts` (required, at least 1), `exhaustedState` (required, a Workflow state), `limitsField` (an object field of the type), `limitsPermission`, `keepBest` (`{ minDelta?, neverRegress? }`), `stuckAfter` (at least 1), `resultField` (a field of the type), `from` (Workflow states, each with a transition to `exhaustedState`), `permission` |
| Fields | `retries`: `{ total, classAttempts, bestScore, exhausted, stuck }` |
| Operations | `recordAttempt({ failure?, score?, result?, signature?, predicates?, detail? })` -> `{ failure, score, kept, total, classAttempts, exhausted, stuck, hint }`, writes |
| Guards | once exhausted, a `Workflow` transition into any state but `exhaustedState`, Lease's `acquire` and Queue's `claim`: `vetoed` (`exhausted`); an update that changes `limitsField` without `limitsPermission` (`forbidden`; `not_configured` when the config names none), and by the holder of the instance's active lease (`limits_fixed`) |
| Refusals | an unknown class, and a result without `resultField` (`invalid_argument`); a result the field's type refuses (`invalid_instance`); an attempt once exhausted (`exhausted`); without `permission` (`forbidden`). Each code is a veto's (`vetoed`) |
| Events | each attempt's operation event, by its caller, with its parameters, `detail` among them |
| `configChange` | any config may change. Added to a schema with instances, which start with no attempts; not removed from one |

```json
{ "name": "Retries", "config": {
    "classes": { "timeout": { "attempts": 3 }, "invalidOutput": { "attempts": 2 }, "rejected": "terminal" },
    "totalAttempts": 4,
    "keepBest": { "minDelta": 0.05, "neverRegress": ["compiles"] },
    "stuckAfter": 2,
    "resultField": "report",
    "exhaustedState": "failed" } }
```

### Counting

An attempt without `failure` is a success and counts nothing. A failure
of a terminal class counts and exhausts the instance; a failure of a
class with no room left, its own cap or the total, is not counted and
exhausts it; any other failure counts, and exhausts the instance when the
total reaches its cap, or its class reaches its cap and no other class
has room. The instance's `limitsField` holds its own caps, a class's name
to a cap of at least 0 and `totalAttempts` to one of at least 1; an
unknown or terminal class, and a value that is not such a cap, is
ignored. Exhaustion is set only by an attempt, so a config whose classes
are all terminal, or caps of 0, exhaust nothing before the first failure.

The caps are not the worker's to raise. Once the instance exists, an
update that changes `limitsField` needs `limitsPermission`, and is
refused when the config names none, so the caps it was created with
stay; and the holder of the instance's active lease, read through
Lease's `lease` field, does not change it, with the permission or
without (`limits_fixed`). Other principals are kept out by Lease's own
guard while the lease is active, unless they hold its
`overridePermission`. The engine's `Constants` is the general rule for
a field nothing changes after the create but a caller with its
permission; Retries guards `limitsField` itself because its rule reads
who holds the lease, which a `validate` cannot, and the holder must
never raise its own caps, even with the permission. A type may list
`limitsField` in `Constants` as well, which then needs both permissions.

### Steering the next attempt

A class's `hint` is what `recordAttempt` returns for a failure of the
class (`hint`, null otherwise): an instruction a worker can carry into
its next attempt, such as what a validator wants. An attempt's `detail`,
any JSON object, is a parameter, so its operation event keeps it, and
nothing else does: validation errors, a stack, what was tried.

### Kept results

A success's result is kept. A failure's is kept only with `keepBest`:
when its score beats the best kept score by at least `minDelta`, and no
`neverRegress` predicate that held for the last kept attempt fails for it
(a predicate the attempt does not report fails). A kept result is written
to `resultField` through `update()`, with the checks of an update, so a
result the live version refuses fails the attempt and records nothing.

### Stuck failures

With `stuckAfter`, a failure that is not kept and carries a `signature`
extends a streak when the signature is the last attempt's, and starts a
new one otherwise; a streak of `stuckAfter` exhausts the instance as
stuck. A kept attempt, a success and a failure without a signature end
the streak.

### Exhaustion

On exhaustion the status moves to `exhaustedState` through Workflow's
`transition`, as the caller, only from the `from` states: every state but
the terminal ones and `exhaustedState` when the config names none. Work
that finished stays finished. A transition a guard vetoes leaves the
status and the instance exhausted all the same. Once exhausted, the
instance takes no more attempts, its status moves only to
`exhaustedState`, and its lease cannot be acquired nor the instance
claimed: an exhausted instance that `from` left in a claimable state is
no candidate of Queue's `claimNext`, which copies the exhaustion
(Queue, "Exclusion"), and its claim is refused. Nothing resets it.

`recordAttempt` needs `permission` when the config names one, and while
a lease is active, `Lease`'s guard keeps it to the holder, as any writing
operation, and to the current token when the caller presents one.

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
