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
expiry and directives to its holder, and `Assignment`. Not built yet:
`Queue`, the claim order and `claimNext`, whose declaration the core
already carries, and the budgets, retries, presence and blueprints D16
lists.

```ts
import { openEngine } from '@superschematic/engine';
import { workQueueBehaviors } from '@superschematic/engine-workqueue';

const engine = openEngine({ path: 'jobs.db', policy, behaviors: workQueueBehaviors });
```

## Registering the behaviors

A deployment passes the implementations as the engine's `behaviors`
option when it opens the engine, after the core's, which the engine
registers itself: `workQueueBehaviors` is every one, and `lease` and
`assignment` are exported one by one for a deployment that runs only
some. `engine.behaviors.register(lease)` registers one later; a
published version that composes a behavior the engine cannot run is
`unavailable` until it does. Registering one whose storage already
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
| Config | `ttlMs` (at least 1000; 60000 when absent), `heartbeatMs` (less than `ttlMs`; a third of it when absent), `maxHoldMs`, `maxHoldField`, `onExpiry` and `escalate` (`{ transition, from }`), `maxExpiries`, `exempt`, `acquirePermission`, `overridePermission`, `directPermission`; all optional |
| Fields | `lease`: `{ holder, token, acquiredAt, expiresAt, active, expiries }`, `holder`, `acquiredAt` and `expiresAt` null when it is free |
| Operations | `acquire({ ttlMs? })` -> `{ token, expiresAt, heartbeatMs }`; `heartbeat({ token })` -> `{ expiresAt, directives }`; `release({ token? })` -> `{}`; `expire()` -> `{ expired }`; `direct({ name, data? })` -> `{ id }`; `acknowledge({ token, ids })` -> `{}`; `resetExpiries()` -> `{ expiries }`. All write |
| Guards | while a lease is active, an update, a delete or a writing operation of another behavior by any principal but the holder is `vetoed`, except an `exempt` operation, a read-only one and a principal with `overridePermission`; once the lease has lapsed, the holder's are; while a lease is held, a change to `maxHoldField` without `overridePermission` is `vetoed` |
| Refusals | `acquire` while a lease is active, the holder's own included, and at `maxExpiries` (`vetoed`), without `acquirePermission` (`forbidden`), with a `ttlMs` past the config's (`invalid_argument`); `heartbeat` and `acknowledge` by another principal, with another token, or once the lease has lapsed (`vetoed`); `release` by another principal (`forbidden` without `overridePermission` when the config names one, `vetoed` when it names none), with another token or once the lease has lapsed (`vetoed`), by the holder without its token (`invalid_argument`); `direct` without its permission (`forbidden`), with no permission in the config or no active lease (`vetoed`); `acknowledge` of an id not sent under the token (`invalid_argument`); `resetExpiries` without `overridePermission` (`forbidden`, or `vetoed` when the config names none) |
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
lapsed lease applies its expiry first.

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
- Lease's own operations, which check their callers themselves.

An operation another behavior's reference hook invokes on a leased
instance, as `Dependencies`' `removeBlocker` when a blocker is deleted or
`Links`' `unlink` when a target is, runs as the principal of that change
and is refused like any other, so that change is refused too; a
deployment that wants it through lists it in `exempt`. Nothing changes
the field `maxHoldField` names while a lease is held, unless with
`overridePermission`, so a holder cannot extend its own hold.

### Directives

A directive is a message to the holder of the active lease: `direct({
name, data? })`, which needs `directPermission`, or `overridePermission`
when that is absent, and is refused when the config names neither. It
attaches to the current token and is numbered within it, 1, 2, 3, ....
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
`test/package.test.ts` runs the document the core binary builds in `make
cli-smoke` (`fixture-workqueue-json`).
