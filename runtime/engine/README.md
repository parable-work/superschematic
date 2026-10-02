# Engine

`@superschematic/engine` (`typescript/`) runs a schema with no generated
code (D16 in `docs/DECISIONS.md`). It takes a schema as data, one JSON
schema-file document, versions it per namespace, and keeps it, its
instances and an event log in one SQLite file.

Built: the storage layer and its migrations, the schema registry with its
compatibility rule, instances, the event log, the access policy, the
HTTP API with the event stream (`@superschematic/engine/http`), the
behavior plug-in interface, the runner of reactions and schedules, the
describe and tools documents, the MCP endpoint
(`@superschematic/engine/mcp`), and the core's behaviors: `Workflow`,
`Comments`, `Revisions`, and `Dependencies`, `Links` and `Rollups`, which
reach other instances, `Search`, full-text search, and `Reactions`, which
the runner runs. Not built yet: the vectors D16 lists beside search, and
the work-queue package.

```ts
import { allowAll, openEngine } from '@superschematic/engine';

const engine = openEngine({ path: 'shop.db', policy: allowAll });
const me = { subject: 'alice', permissions: [] };

engine.schemas.define(me, orderSchemaJSON);       // the draft of "Order"
engine.schemas.publish(me, 'Order');              // { version: 1, published: true }

const order = engine.instances.create(me, 'Order', { title: 'Desk', quantity: 1 });
engine.instances.update(me, 'Order', order.id, { quantity: null, status: 'open' });
const page = engine.instances.list(me, 'Order', { limit: 20 });   // { items, next }
const feed = engine.events.read(me, { after: 0 });                // { events, next, more }
engine.close();
```

## Runtimes

The package ships compiled ESM with declarations (`dist/`), since Node.js
does not strip types from files under `node_modules`. It runs on Node.js
24 and on Bun, and its tests run under both. It needs
`@superschematic/schema-runtime`, `@superschematic/schema-ir`,
`@superschematic/http-runtime` and `superscalar` next to it in
`node_modules`; until they are published, a consumer declares all four
itself (D3), and this package's own build and tests get them from
`typescript/scripts/link-local-deps.mjs`. The main entry point imports the
HTTP runtime's framework-free entry point, for the default permission
matcher, and not Hono; the `./http` entry point also needs `hono`, an
optional peer dependency, and the `./mcp` entry point `hono` and
`@modelcontextprotocol/server`, another.

## Storage

One SQLite file, through a small synchronous driver interface
(`SqlDriver`) with two adapters over the runtimes' built-in modules:
`node:sqlite` (`DatabaseSync`) and `bun:sqlite`. `driver: 'auto'`, the
default, picks `bun:sqlite` on Bun and `node:sqlite` elsewhere; `'node'`
and `'bun'` choose one (Bun also ships `node:sqlite`). No native addon is
involved. The adapters return plain-object rows, `undefined` for a
missing row, bind only strings, finite numbers, bigints, `Uint8Array`s
and null, and throw `SqliteError` with SQLite's extended result code.

Opening a file sets a busy timeout (`busyTimeoutMs`, default 5000),
write-ahead logging and foreign keys. A write runs in a transaction that
starts with `BEGIN IMMEDIATE`; a transaction inside another is a
savepoint that rolls back alone. A transaction is synchronous: a function
that returns a promise rolls it back. `afterCommit(work)` queues work for
after the outermost commit, dropped if the transaction or savepoint that
queued it rolls back; the event log announces its events this way.

One process writes the file. SQLite queues writers from several
processes on the busy timeout, but the engine keeps per-process state
(the cache of each version's validator and behaviors) that nothing
coordinates across processes.

The engine's tables, as its six migrations leave them:

```sql
-- Every schema document by namespace, name and version. Version 0 is the
-- name's draft; 1, 2, 3, ... are its published versions, the highest live.
CREATE TABLE engine_schemas (
  namespace    TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  version      INTEGER NOT NULL CHECK (version >= 0),
  document     TEXT    NOT NULL CHECK (json_valid(document)),  -- canonical JSON
  hash         TEXT    NOT NULL,                               -- SHA-256 of document
  defined_at   INTEGER NOT NULL,                               -- epoch ms
  published_at INTEGER,                                        -- null for the draft
  defined_by   TEXT,                                           -- principal subjects; null
  published_by TEXT,                                           -- before migration 2
  PRIMARY KEY (namespace, name, version),
  CHECK ((version = 0) = (published_at IS NULL))
) STRICT;
CREATE INDEX engine_schemas_name ON engine_schemas (name, namespace);

-- Instances by namespace, schema and id, with their fields as JSON.
-- position orders them by creation and is never reused: the list cursor.
CREATE TABLE engine_instances (
  position         INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace        TEXT    NOT NULL,
  schema           TEXT    NOT NULL,
  id               TEXT    NOT NULL,
  schema_namespace TEXT    NOT NULL,   -- the namespace that holds the schema
  version          INTEGER NOT NULL,   -- the schema version it was last written with
  seq              INTEGER NOT NULL,   -- its last event's sequence
  data             TEXT    NOT NULL CHECK (json_valid(data) AND json_type(data) = 'object'),
  created_at       INTEGER NOT NULL,
  created_by       TEXT    NOT NULL,
  updated_at       INTEGER NOT NULL,
  updated_by       TEXT    NOT NULL,
  UNIQUE (namespace, schema, id)
) STRICT;
CREATE INDEX engine_instances_list ON engine_instances (namespace, schema, position);

-- The append-only event log. Triggers refuse an UPDATE or DELETE.
CREATE TABLE engine_events (
  cursor      INTEGER PRIMARY KEY AUTOINCREMENT,  -- the global cursor
  kind        TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish')),
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT,                               -- null for a publish
  seq         INTEGER,                            -- per instance; null for a publish
  version     INTEGER NOT NULL,
  actor       TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  change      TEXT,                               -- JSON: see "The event log"
  -- For an event the runner's work wrote (migration 6): the behavior, the
  -- event its reaction handled or the schedule that ran, and its depth.
  cause_behavior TEXT,
  cause_event    INTEGER,
  cause_schedule TEXT,
  depth          INTEGER NOT NULL DEFAULT 0,      -- 0 for a caller's change
  CHECK ((kind = 'publish') = (instance_id IS NULL)),
  CHECK ((instance_id IS NULL) = (seq IS NULL))
) STRICT;
CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
CREATE INDEX engine_events_namespace ON engine_events (namespace, cursor);
CREATE INDEX engine_events_schema ON engine_events (namespace, schema, cursor);
-- The publish events alone, which a namespace reads from its shared namespace.
CREATE INDEX engine_events_publish ON engine_events (namespace, cursor) WHERE kind = 'publish';

-- The key of each behavior whose storage the file holds: its columns on
-- engine_instances and its tables are named bhv_<key>__<name>, and the
-- indexes its migrations list on engine_instances bhv_<key>___index_<name>.
CREATE TABLE engine_behaviors (
  name       TEXT    PRIMARY KEY,
  key        TEXT    NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
) STRICT;

-- The references behaviors record from one instance to another in the
-- same namespace ("References" under "Behaviors").
CREATE TABLE engine_references (
  namespace     TEXT NOT NULL,
  target_schema TEXT NOT NULL,
  target_id     TEXT NOT NULL,
  source_schema TEXT NOT NULL,
  source_id     TEXT NOT NULL,
  behavior      TEXT NOT NULL,
  key           TEXT NOT NULL,
  PRIMARY KEY (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
) STRICT;
CREATE INDEX engine_references_source ON engine_references (namespace, source_schema, source_id, behavior);

-- The runner's subscriptions ("The runner"): a behavior's reactions on a
-- schema in a namespace, the cursor of the last event they handled or
-- passed over, and the state of their retries.
CREATE TABLE engine_subscriptions (
  behavior       TEXT    NOT NULL,
  namespace      TEXT    NOT NULL,
  schema         TEXT    NOT NULL,
  cursor         INTEGER NOT NULL,
  halted         INTEGER NOT NULL DEFAULT 0,      -- 1 after maxAttempts failures
  attempts       INTEGER NOT NULL DEFAULT 0,      -- failed attempts at the next event
  retry_at       INTEGER,
  failed_cursor  INTEGER,                         -- the last failure: the event, when, the error
  failed_at      INTEGER,
  error          TEXT,
  skipped        INTEGER NOT NULL DEFAULT 0,      -- events passed over
  skipped_cursor INTEGER,
  skipped_reason TEXT,                            -- depth or resume
  PRIMARY KEY (behavior, namespace, schema)
) STRICT;

-- The runner's schedules: a behavior's schedule on a schema in a namespace.
CREATE TABLE engine_schedules (
  behavior    TEXT    NOT NULL,
  schedule    TEXT    NOT NULL,
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  last_run_at INTEGER,                            -- null before its first run
  next_run_at INTEGER NOT NULL,
  failures    INTEGER NOT NULL DEFAULT 0,         -- failed runs since the last success
  error       TEXT,
  PRIMARY KEY (behavior, schedule, namespace, schema)
) STRICT;

-- The migration ledger, one row per applied migration of each owner.
CREATE TABLE engine_migrations (
  owner      TEXT    NOT NULL,
  version    INTEGER NOT NULL,
  name       TEXT    NOT NULL,
  applied_at INTEGER NOT NULL,
  PRIMARY KEY (owner, version)
) STRICT;
```

### Migrations

Migrations are forward-only and recorded per owner: the engine's are owned
by `engine` (`engineMigrations`), and a behavior's by its name (see
"Behaviors"). `migrate(storage, { owner, migrations })`
applies the versions the ledger lacks, numbered 1, 2, 3, ..., each in its
own transaction with its ledger row. A file whose ledger is ahead of the
build, or names a version differently, is refused. The tests run the
engine's migrations from an empty file and from the file each earlier
version left.

## Schemas

A schema is one JSON schema-file document, the form `superschematic
format --to=json` writes. The strict loader of
`@superschematic/schema-runtime` reads it against a meta-schema, the
`metaSchema` option: the `superschematic json-schema` output of the
deployment's binary, the core's by default. The engine then requires:

- `kind: General`, and a `name` of a letter followed by letters, digits,
  `_` and `-`, at most 128 characters;
- no `imports` (a type in another schema is reached through a link
  behavior) and no `operationSets` (every schema has `create`, `get`,
  `list`, `update` and `delete`, and behaviors add their own);
- an instance type: the type named like the schema, or its only type;
- field types the schema runtime validates: a builtin primitive, a
  scalar, or an enum or type of the document, alone, as a list or as a
  list of lists. A union or a map is refused, since the runtime checks
  neither;
- behaviors on the instance type that this engine has implementations
  for, composed as the compiler's loader requires (see "Behaviors").

A refused document throws `SchemaDocumentError` with an issue per problem,
each at a JSON pointer.

`define` stores the document as its name's draft in a namespace,
replacing the previous draft. `publish` makes the draft the next live
version. The engine stores the canonical form the loader returns
(compact, keys sorted) and its SHA-256; publishing a draft identical to
the live version mints nothing and drops the draft. There is no
deprecate or promote: the live version changes only when a newer one is
published, so an older version never comes back into use. `live`,
`draft`, `version` and `list` read them. Each version records who defined
and who published it.

### The compatibility rule

A new version may change a schema only in ways every instance the live
version accepts still satisfies. `define` and `publish` diff the two
documents over what an instance can hold: the instance type and the
types, enums and scalars its fields reach.

| Allowed | Refused |
| --- | --- |
| a new optional field | a removed or renamed field, or a new JSON key (`jsonTag`) |
| a new enum value | a removed enum value, or a value serialized differently |
| a lower `minLength`, `min` or `listMin`, or none | a higher one, or one where there was none |
| a higher `maxLength`, `max` or `listMax`, or none | a lower one, or one where there was none |
| a required field made optional | an optional field made required, or a new required field |
| a dropped pattern | a new or changed pattern |
| descriptions, comments, defaults, UI metadata | another type, or another list depth (`T`, `T[]`, `T[][]`) |
| any change to a type, enum or scalar no field reaches | a scalar that accepts less: a primitive or JSON type change, a narrower bound, a new or changed pattern, new reserved words, a new custom validator |
| | another instance type |
| a behavior's config changed as its implementation allows | any other config change |
| a behavior added or removed while the schema has no instances, or with its implementation's consent | a behavior added or removed while it has instances, otherwise |

A refused version throws `IncompatibleChangeError`, whose `changes` name
each change and whose message says to use a new schema name.

### Validation

The validator of a version is built once, with `parseSchemaIR` from the
stored document, and cached per namespace, name and version. The schema
runtime checks each field, including that a list field holds a list and
an object-typed field or list element holds an object (`type`). The
engine adds two checks the runtime does not make, which the
compatibility rule relies on: the value is JSON (plain objects and
arrays, strings, finite numbers, booleans, null; a member whose value is
`undefined` is absent), and an object holds no key its type does not
declare, at any depth. A field a behavior adds is the behavior's: an
instance that sets one is refused with the rule `readOnly`. `validate`
returns `{ path, rule, message }` issues, with paths such as
`lines[2].sku`, and `validator` the cached validator itself.

## Instances

`engine.instances` stores instances of a schema's instance type. An
instance is keyed by namespace, schema name and id, so an id in one
namespace reveals nothing about another. Ids come from the `ids` option,
random UUIDs by default, or from the caller; an id is a letter or digit
followed by letters, digits, `.`, `_`, `:` and `-`, at most 256
characters. A schema a namespace reaches through the shared namespace
holds instances in the namespace that creates them.

- `create` validates the instance against the schema's live version and
  refuses an id the namespace already has for the schema (`conflict`).
  Each behavior initializes its state for it.
- `get` returns the instance, or `undefined`.
- `list` returns a page in creation order: `{ items, next }`, where `next`
  is an opaque cursor, null after the last page. A page holds 50 instances
  by default and at most 500 (`limit`). Each page is one indexed range
  read, so a list never loads the whole table, and an instance created or
  deleted while a client pages moves no other instance between pages.
- `update` takes a JSON merge patch (RFC 7386), the engine's update rule: a
  member replaces the instance's member, a nested object merges, `null`
  removes the member, and a list or any other value replaces what was
  there. The result is validated against the live version; a patch that
  changes nothing writes nothing. The behaviors' guards may veto it.
- `delete` removes the instance and returns whether there was one. The
  behaviors' guards may veto it.
- `update`, `delete` and `invoke` take `expectedSeq`, the sequence the
  caller last read. Inside the write transaction the engine refuses the
  call (`seq_mismatch`) unless the instance is still at it; an instance
  that does not exist is still `not_found` for `update` and `invoke` and
  `false` for `delete`. This is optimistic concurrency, the HTTP API's
  `If-Match`.
- `invoke` calls a behavior operation on the instance (see "Behaviors")
  and returns its result; `operate` returns `{ result, seq }`, the result
  and the instance's sequence after the call.

An instance's `data` holds its own fields as stored, then each field its
behaviors add that has a value. The row stores only its own fields; the
behaviors' are read from their storage. Its `seq` is the sequence of its
last event, so every write that appends one moves it: a create, an update
that changes something and a writing behavior operation.

Every operation on a schema with no live version is `not_found`. A row
records the schema version it was last written with; the compatibility
rule keeps it valid under every later version, so reads return it as
stored.

## The event log

Each write appends one event in its own transaction: an instance's
`create`, `update` and `delete`, a behavior `operation` that writes, and a
schema's `publish` (a publish that mints nothing appends nothing). An
event carries its namespace (for a publish, the namespace that holds the
schema), schema, instance id, per-instance sequence, schema version,
actor (the principal's subject), time and change: the instance for a
create, its behaviors' fields included; for an update, the merge patch,
with any change the behaviors' fields took merged in; for an operation,
`{ behavior, operation, params, patch }`, where `patch` is the merge patch
of the instance's own fields the operation changed (an operation's
`update()`, under "Contexts") and of its behaviors' fields; nothing for a
delete; and the schema document
for a publish. Applying each change in order to the create's instance
gives the instance as a read returns it. The
cursor orders the whole log; an instance's sequence runs 1, 2, 3, ...
across its life, a re-create after a delete included. A delete never
removes earlier events.

`engine.events.read(principal, { namespace, schema, instanceId, after,
limit })` returns `{ events, next, more }`: the events after the cursor
`after` in one namespace, optionally one schema and one instance, 50 by
default and at most 500 scanned per page. Read on from `next`. Without a
schema filter, events of schemas the principal may not read are skipped,
so a page can hold fewer events than its limit while `more` is true.

A namespace that looks names up in a shared namespace also reads the
shared namespace's publish events, which change the schemas it reaches;
they keep the shared namespace as their `namespace`. Its own instance
events and the shared publishes are two indexed range reads merged by
cursor.

An event the runner's work wrote ("The runner") also carries `cause`:
`{ behavior, event, depth }` for a reaction, `event` being the cursor of
the event it handled, and `{ behavior, schedule, depth }` for a
schedule. Its depth is one more than its cause's: a caller's change has
none and depth 0, a reaction to it and a schedule's write depth 1. Its
actor is the runner's principal.

`engine.events.watch({ committed, closed })` registers a watcher and
returns the function that removes it. After each commit, the engine
calls `committed(cursor)` once for each event the commit appended, and it
calls `closed()` once when it closes. The notice is in-process,
which is complete because one process writes the file: every event passes
through it. A watcher reads the log as its own principal from its own
cursor; the notice tells it only that the log grew.

There is no retention yet: the log grows until a later change adds a
policy for it.

## The runner

`engine.runner` runs the work behaviors do after a change commits (D16,
amended): reactions to the events of the log, and schedules on an
interval ("Reactions and schedules" under "Behaviors"). One runs per
engine, in its process, as one principal the deployment names; a
reaction never refuses the change that set it off, and is not limited by
the permissions of whoever made it.

```ts
const engine = openEngine({ path: 'shop.db', policy, runner: { principal: { subject: 'runner', permissions: ['projects.close'] } } });
engine.runner.start();                                    // runs what is due, then wakes on each commit
engine.runner.status();                                   // { running, principal, head, subscriptions, schedules, error }
engine.runner.resume({ behavior: 'Reactions', namespace: 'default', schema: 'Project' });
engine.runner.stop();                                     // engine.close() stops it too
```

| Option | Default | What it is |
| --- | --- | --- |
| `principal` | none | who reactions and schedules act as: the access policy is asked as it at every read and invoke, `can()` answers for its permissions, and the events they write record its subject as their actor |
| `maxDepth` | 8 | a reaction runs for an event below this depth |
| `maxAttempts` | 5 | failed attempts at one event before its subscription halts |
| `retryInitialMs`, `retryMaxMs` | 1000, 60000 | the backoff: the first retry's delay, doubling for each after, up to the most |
| `batchSize` | 100 | events a subscription handles in one transaction before the runner yields, at most 500 |

There is no default principal and no superuser. An engine opened without
`runner` has a runner whose `start` and `runDue` throw (`TypeError`); a
deployment grants the principal what its reactions do, a permission a
Workflow transition names included.

- `start()` runs what is due, then wakes when the commit notifier
  announces events, after the commit has returned to its writer, and on
  a timer when a retry or a schedule comes due. It works through what is
  due in batches and yields to the event loop between them. Starting a
  started runner does nothing.
- `stop()` stops it: no pass starts after it. `start()` resumes from the
  saved cursors, and so does a new engine on the same file.
  `close()` stops it for good.
- `runDue()` runs everything due now, the reactions to what that writes
  included, and returns `{ handled, skipped, failed, scheduled }`. It runs
  started or not, so a test or a deployment that drives the runner itself
  calls it; it refuses inside a transaction and inside a reaction.
- `running` says whether it is started.

### Subscriptions

A subscription is one behavior's reactions on one schema that composes
it, in one namespace: `{ behavior, namespace, schema }`. It hears the
instance events of the namespace (a create, an update, a delete and an
operation; not a publish) on that schema and on the schemas the
behavior's `watches` returns for the schema's config, from the publish
that made the schema compose the behavior on: the publish of the earliest
version of the run of versions, up to the live one, that compose it. A
schema whose live version stops composing the behavior leaves its
subscription `inactive`; composed again, it starts over at that publish,
and the events between belong to no subscription.

- **Database effects once per event.** The subscription's cursor is a row
  of `engine_subscriptions`. Each event's reaction runs in a savepoint of
  the batch's transaction, and the cursor's advance commits with it. After
  a failure, or a crash before the commit, neither the reaction's writes
  nor the advance are there, and the event runs again; after the commit
  it never runs again. An effect outside the database, a request the
  handler sends say, happens at least once, since a handler that runs
  again repeats it. A handler is synchronous (D16) and cannot wait for
  such an effect anyway: one that must reach outside records the intent
  through an operation, and a sender outside the engine delivers it from
  the log.
- **Order.** A subscription handles its events one at a time in log order,
  and none after one that has not committed. Subscriptions do not wait for
  each other.
- **Now, not then.** A reaction reads instances as they are when it runs,
  which may be after later changes, and runs with the live version's
  config.

A reaction that throws is retried after `retryInitialMs`, doubling up to
`retryMaxMs`, on the engine's clock (`clock`). After `maxAttempts` failed
attempts its subscription halts at the event: it handles nothing more
until `resume({ behavior, namespace, schema })`, which runs the event
again, or `resume(key, { skip: true })`, which passes over it and records
the skip. A refusal of the access policy is a failure like any other, so
a principal the deployment has not granted halts where an operator sees
it. Halting rather than skipping keeps the order a subscription promises:
whatever follows a failed event would run on state that assumed it ran.

An event at the depth limit (`maxDepth`) is passed over, not handled: a
loop of reactions, each writing an event the next reacts to, stops there,
and the subscription counts the event as skipped (`reason: 'depth'`).

### Schedules

A schedule is one behavior's named timed work on one schema that
composes it, in one namespace, with the time of its last run and its
next in `engine_schedules`. The runner finds it at its first pass and
runs it an interval later; a run and the next run's time commit in one
transaction. Missed ticks are not replayed: a schedule that came due
while the runner was stopped runs once when it starts, then an interval
after, and its context's `previous` is when its last run committed. A
run that throws rolls back and is retried with the backoff, never later
than its next tick, and a schedule never halts: the next run redoes the
work a sweep missed, and there is no order to keep.

A schedule's interval is fixed (`everyMs: 60_000`) or follows the config
of each schema that composes the behavior (`everyMs: (config) =>
config.leaseMs`), so a sweep of leases on one schema runs as often as
that schema's leases need. The runner calls the function for each schema
when it finds the schedule there, at its first pass and after each
publish, and holds its result to the rule a fixed interval meets: an
integer of at least 1000. When a publish changes the interval, the run
already due keeps the time the old one gave it, and the new one applies
after it. A function that throws or returns
anything else fails the schedule on that schema as a failing run does:
the status shows the error and `everyMs: null`, nothing runs, and it is
tried again after the backoff (with no interval to cap it), until a
publish gives a config it accepts. The runner, and the schedule on other
schemas, go on.

### Status

`status()` lists every subscription and schedule:

| | |
| --- | --- |
| `running`, `principal`, `head` | whether it is started, the principal's subject, and the log's last cursor |
| `subscriptions` | `{ behavior, namespace, schema, state, cursor, attempts, retryAt, failure, skipped, lastSkip }`: `state` is `active`, `retrying` (its next event failed; `retryAt` says when it tries again), `halted` or `inactive`; `failure` is `{ cursor, at, error }` until an attempt succeeds, with `cursor` null for a failure of `watches`; `lastSkip` is `{ cursor, reason }`, `depth` or `resume` |
| `schedules` | `{ behavior, schedule, namespace, schema, state, everyMs, previous, next, failures, error }`: `state` is `active`, `retrying` or `inactive`; `everyMs` is null when the schedule's function gives no interval on the schema |
| `error` | the runner's own last error outside any reaction (a busy file, say), cleared by the next pass that works |

The status is not served over HTTP or MCP: it spans every namespace and
schema, and the access policy has no action for that. A deployment shows
it on its own terms, a health route of its own say.

## Access

Every entry point takes the principal it acts for and asks the access
policy the deployment supplies (`policy`) before it reads or writes. The
policy answers `{ principal, action, namespace, schema, operation }`,
where the action is `read` (a schema, its instances or its events),
`write` (create, update, delete), `define` or `publish`; a behavior
operation asks `write` when its declaration says it writes and `read`
otherwise, and names itself in `operation`, absent for every other call.
Only `true` allows, and anything else is `forbidden`. It runs synchronously. There is no default
policy: `allowAll` is explicit, for tests and local use. The engine has
no roles; a policy can hold the principal's `permissions` to whatever
rule the deployment has, through the HTTP runtime's `PermissionMatcher`
for example. A behavior's own checks, a transition only a reviewer may
make say, ask the `permissionMatcher` option (a `PermissionMatcher`, the
HTTP runtime's `hasAnyPermission` by default) through `can()` ("Contexts"
under "Behaviors"); a deployment passes the matcher it gives the HTTP
runtime, so one rule answers both.

`Principal` has the shape of the HTTP runtime's (`subject`,
`permissions`, `claims`), so a principal its `Authenticator` returns can
be passed on as it is. The main entry point does not import the HTTP
runtime, which brings Hono; the `./http` entry point does.

## Errors

Every refusal is an `EngineError` with a `code` a server maps to a
status: `invalid_schema` (`SchemaDocumentError`, with its issues),
`incompatible_change` (`IncompatibleChangeError`, with its changes),
`invalid_instance` (`InstanceValidationError`, with its issues),
`name_taken`, `not_found`, `conflict`, `forbidden`, `unknown_namespace`,
`invalid_argument` (`OperationParamsError` for an operation's parameters,
with its issues), `seq_mismatch`, `vetoed` (`BehaviorVetoError`: a
behavior's guard refused the change) and `unavailable` (the live version
composes a behavior this engine has no implementation for, or whose
implementation refuses its config). A message names only what the call
named: `name_taken` in the shared namespace does not say which namespace
holds the name. "Statuses" under "HTTP" gives each code's status.

A defect in a behavior's code is a `BehaviorError`, not an `EngineError`,
so a server answers it as an internal error: a result its `resultSchema`
refuses, a field value that is not JSON, SQL outside its own storage, a
write from a read, a promise from a synchronous function.

## Behaviors

A behavior adds fields, operations, checks and storage to a schema's
instance type (D16 in `docs/DECISIONS.md`). The compiler declares it in a
JSON file (section 3.16 of `docs/extension-model.md`); the engine runs an
implementation of it that carries the same file. The engine registers
the core's behaviors when it opens ("Core behaviors"); a deployment
registers the implementations of its own, when the engine opens or
later.

```ts
import { defineBehavior, openEngine } from '@superschematic/engine';
import declaration from './counter.behavior.json' with { type: 'json' };

// counter.behavior.json declares test.Counter: a `limit` config, the field
// `count` and the writing operation `increment`.
export const counter = defineBehavior<{ limit?: number }>({
  declaration,
  migrations: [{ version: 1, name: 'count', columns: { count: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'operation' && request.operation === 'increment' && view.config.limit !== undefined
        && Number(view.columns.get().count) >= view.config.limit) {
      return `the count is at its limit, ${view.config.limit}`;
    }
  },
  operations: {
    increment(context) {
      const count = Number(context.columns.get().count) + 1;
      context.columns.set({ count });
      return { count };
    },
  },
  fields: { count: (view) => view.columns.get().count },
});

const engine = openEngine({ path: 'shop.db', policy, metaSchema, behaviors: [counter] });
engine.behaviors.register(another);                                   // later
engine.instances.invoke(me, 'Item', id, 'increment', {});            // { count: 1 }
```

`metaSchema` is the `json-schema` output of the deployment's binary, which
lists the behaviors it declares; the core's lists the core's own
(`Workflow`, `Comments`, `Revisions`, `Dependencies`, `Links`,
`Rollups`, `Search` and `Reactions`), so its loader refuses any other.

### The implementation

`BehaviorImplementation<Config>`; `defineBehavior` types its config. Every
function is synchronous (D16): one that returns a promise is a
`BehaviorError`, which rolls the write back.

| Member | What it is |
| --- | --- |
| `declaration` | the declaration the compiler registers, as its JSON file holds it |
| `parseConfig(config, target)` | checks a config its `configSchema` accepted and returns what the other functions get as `config`; throws `BehaviorConfigError` to refuse it. Absent, `config` is the JSON config, `{}` when the type gives none. `target` has the schema, the type, its fields' JSON keys and `fieldSchemas` (each one's JSON Schema, as the describe document writes it), every behavior the type lists with its config, and, when the schema is defined or published, `schemas` ("Other instances") |
| `configChange(before, after)` | whether a new version may change the config, add the behavior (`before` undefined) or remove it (`after` undefined) on a schema with instances: a reason refuses. Absent, only an identical config, and no adding or removing while there are instances |
| `afterConfigChange(context)` | brings its own storage in line when a published version adds it (a first version included), removes it or changes its config ("Publishing") |
| `migrations` | its storage, as forward-only migrations: the columns each adds to the instances table, the `indexes` it adds on them, and an `up(sql)` for its own tables ("Storage") |
| `initialize(context)` | sets up its state for a new instance |
| `guard(view, request)` | may veto an `update`, a `delete` or an `operation` of any behavior on the type: a returned reason vetoes. An operation's request carries `writes`, as its declaration says, so a guard that holds back changes can let a read-only operation through. An update a behavior's operation applies names that behavior as `caller`, as a `call()` does |
| `operations` | a handler per declared instance operation: `(context, params) => result`, with an `OperationContext` |
| `schemaOperations` | a handler per declared schema-level operation (`scope: "schema"`): `(context, params) => result`, with a `SchemaContext` ("Schema-level operations") |
| `fields` | a reader per declared field: `(view) => value` |
| `afterChange(context, change)` | runs after a create, an update, a delete or a caller's writing operation, in the same transaction. An operation's change carries `before`, the instance's own fields before it, when its `update()` changed them |
| `guardReference(view, reference, request)` | may veto an `update`, a `delete` or a writing `operation` of an instance this behavior's instance refers to ("References"); the view is the referencing instance's |
| `afterReferenceChange(context, reference, change)` | runs after such a change, in the same transaction, on the referencing instance; after a delete it must remove the reference |
| `reactions` | `{ react(context, event), watches?(config, schema) }`: reactions to committed events, which the runner runs after the commit ("Reactions and schedules") |
| `schedules` | named timed work, `{ <name>: { everyMs, run(context) } }`, which the runner runs on each schema that composes the behavior; `everyMs` is a number or a function of the schema's config ("Schedules" under "The runner") |

Registration (`openEngine({ behaviors })` or `engine.behaviors.register`)
refuses, naming every problem, an implementation whose `operations` or
`fields` are not exactly the ones its declaration names; `reactions`
without a `react` function; a schedule whose name is not camelCase, whose
`everyMs` is neither an integer of at least 1000 nor a function, or that
has no `run`; a declaration of the wrong shape, with an operation named
`create`, `get`, `list`, `update` or `delete`, or with a schema that does
not compile; and malformed migrations, columns or indexes, an index over
a column no migration up to its own adds included. An operation's `paramsSchema` sets
`additionalProperties: false`, so the handler and every guard read the
same declared parameters and no alias reaches one and not the other; the
compiler refuses the same declaration when it registers. The engine adds
one rule to the compiler's: a name is `<extension>.<Name>` with an
extension name of a letter, then letters, digits, `_` and `-` (or a core
`<Name>`), since the migration ledger is keyed by it. A name registers
once.

### Contexts

A function reaches only what its behavior may touch. A view (a guard's,
a field reader's) has:

- `behavior`, `config`, `namespace`, `schema`, `version`, `id`;
- `principal`, and `now`, the clock's time, read once for the whole call;
- `can(permission)`: whether the principal holds a permission, as the
  engine's `permissionMatcher` answers for its permissions. The default is
  the HTTP runtime's `hasAnyPermission`: dotted paths, a granted
  permission covering itself and everything nested under it, no root
  permission. Where a behavior limits who may do something, its config
  names the permission and `can` decides (D16); the engine has no roles;
- `data`: the instance's own fields, deep-frozen, without any behavior's;
- `columns.get()`: its own columns on the instance, by its own names;
- `sql`: `get` and `all` on its own tables, reads only, with
  `sql.table(name)` for the SQL name of one of them, and
  `sql.instances()` for the read-only relation over the schema's
  instances with its own columns on each ("Its columns across the
  schema");
- `instances` and `schemas`: other instances and other schemas' configs,
  read as the principal ("Other instances"), and `instances.invoke` and
  `instances.invokeSchema` of read-only operations;
- `references.list()`: the references the behavior recorded from the
  instance ("References").

A context (initialize, afterChange, an operation) adds `columns.set()`,
`sql.run()`, `references.add()` and `remove()`, `call(behavior,
operation, params)`, `instances.invoke` and `instances.invokeSchema` of
writing operations, and `instances.create`. In a read-only operation
`set`, `run`, `add` and `remove` refuse, `call` and `invoke` reach only
read-only operations, and `create` refuses; after a delete,
`columns.get()` returns what the instance had and `set`, `add`, `remove`
and `call` refuse. There is no handle on the instances table beyond the
read-only relation, the event log, another behavior's storage or the
connection: a status one behavior owns changes at another's request only
through its operations, whose guards run, on this instance or another.

An operation's context (`OperationContext`) adds two more, so a
behavior that changes the instance on a caller's behalf, approving a
proposed change say, runs the checks an update runs:

- `update(patch)` applies a JSON merge patch to the instance's own fields
  and returns them after it. A behavior's field in the patch is refused
  (`InstanceValidationError`, rule `readOnly`), so is a result the live
  version refuses, and then every guard is asked with `{ kind: 'update',
  patch, after, caller }`. A patch that changes nothing writes nothing.
  The access policy is not asked again, and no event is appended: the
  operation's event carries the change in its `patch`, and `afterChange`
  gets `before`. A read-only operation's `update` refuses, and a called
  operation's update rolls back with its savepoint. `data` reads the
  fields as the operation's updates leave them.
- `validateUpdate(patch)` returns the issues `update(patch)` would refuse
  the patch for, without writing or asking a guard.

### Other instances

A behavior reaches the rest of its namespace as the call's principal
(D16, amended). There is no system principal: every read and invoke asks
the access policy, and a caller who may not read a schema cannot read it
through a behavior either.

```ts
// In an operation of Task: read a milestone's status, then move it on.
const milestone = context.instances.get('Milestone', params.milestone, { fields: ['status'] });
const flow = context.schemas.config('Milestone', 'Workflow');          // as the schema holds it
if (milestone && flow && !isTerminalState(flow, String(milestone.data.status))) {
  context.instances.invoke('Milestone', milestone.id, 'transition', { to: 'done' });
}
```

| Member | What it does | Asks the policy |
| --- | --- | --- |
| `instances.get(schema, id, { fields? })` | the instance's record, deep-frozen, with every behavior field, the ones `fields` names, or none for `[]`; `undefined` when there is none | `read` on the schema |
| `instances.getMany(schema, ids, { fields? })` | a `Map` by id of the instances of one schema, at most 500, in one query; ids with none are left out | `read` on the schema, once |
| `instances.invoke(schema, id, operation, params?)` | runs an instance operation of another instance, or of this one, as `engine.instances.invoke` would, and returns its result | `write` or `read` with the operation's name |
| `instances.invokeSchema(schema, operation, params?)` | runs a schema-level operation of a schema, its own or another, as `engine.instances.invokeSchema` would, a writing one in a savepoint, and returns its result | `write` or `read` with the operation's name |
| `instances.create(schema, data, { id? })` | creates an instance of the namespace as `engine.instances.create` would: validates `data`, the instance's own fields, against the live version, runs every behavior's `initialize` and `afterChange` and appends its create event, in a savepoint; returns the record, deep-frozen. Without an `id`, the engine's `ids` makes one | `write` on the schema |
| `schemas.config(schema, behavior)` | the config a schema's live version gives a behavior, as the schema holds it (`{}` when none); `undefined` when it does not compose it | `read`, unless the schema is the call's own |
| `schemas.readable(schema)` | whether the principal may read a schema | `read` |

- A schema name is looked up in the namespace, then in the shared one; an
  instance, a reference and an invoke are always the call's namespace's.
- An invoked operation runs its parameter check, every guard of its
  instance, its handler and its result check; a writing one then runs
  that instance's `afterChange`, takes its next `seq` and appends its own
  operation event, whose actor is the caller. It runs in the caller's
  transaction, in a savepoint: a failure the behavior catches leaves
  nothing of it, and one it does not rolls back every instance the call
  changed. Its event comes before the calling operation's, which finishes
  after it.
- A guard, a field reader and a read-only operation invoke read-only
  operations only; initialize, afterChange, `afterReferenceChange`, a
  writing operation and the runner's work invoke writing ones too. That
  holds for `invokeSchema` as for `invoke`, and `create` goes where a
  writing `invoke` goes: from a guard, a field reader, a read-only
  operation or a read-only schema-level one it is a `BehaviorError`.
- A created instance's create event comes before the event of the call
  that created it, as an invoked operation's does, and records the
  runner's cause in the runner's work. A failure inside the create, its
  validation or one of its hooks, rolls back the create alone when the
  behavior catches it, and the whole call when it does not. A parent's
  `initialize` or `afterChange` creates its children in the parent's
  transaction, so they commit with it or not at all.
- `call()`, invokes, creates and reads that compute fields nest at most
  16 deep together (`MAX_CALL_DEPTH`). Invoking a writing operation of an
  instance whose own write is still running up the call is refused as a
  cycle (`BehaviorError`), and so is creating one; a read of it sees what
  the call has written so far. A cycle of field reads ends at the depth
  limit, so a behavior names the fields it needs.
- A field that reads another instance is computed at each read. The log
  records each instance's own changes, so a change of an instance that
  another's field reads shows at the reader's next read, with no event on
  the reader.

`parseConfig(config, target)` gets `target.configs`, the config of every
behavior the type lists as the schema holds it, so a behavior that builds
on another checks its config against that one's when the schema is
defined, and `target.fieldSchemas`, so one that reads the type's own
fields checks their types. When the schema is defined or published it
also gets `target.schemas`, so a config that names another schema is
checked against it then:

```ts
const tasks = target.schemas?.get('Task');   // asks read on Task, as the caller who defines
// { schema: 'Task', type: 'Task', fields: { title: 'string', estimate: 'number', ... },
//   behaviors: ['Workflow', 'Links'], configs: { Workflow: {...}, Links: {...} } }
```

`get(name)` returns another schema's live version, looked up in the
namespace and then the shared one, or `undefined` when it has none:
`fields` holds the JSON type of each of its type's own fields by JSON key
(`string`, an enum's too, `number`, `integer`, `boolean`, `object`,
`array`, or `any` for `Generic.JSON`), with its behaviors and their
configs as the schema holds them. It asks `read` on the schema as the
caller who defines or publishes, and a refusal refuses the call
(`forbidden`); the schema's own name returns the version being defined,
without asking. `target.schemas` is absent when a published version is
composed again to run it, so a version is never refused later because
another schema changed.

### Its columns across the schema

A behavior reads its own columns across every instance of the call's
schema, beside each one's metadata and own fields, with SQL. A claim
finds the next eligible instance this way, and then invokes an operation
on it, which runs that instance's guards and appends its event (D16,
amended).

```ts
// In a schema-level operation of a queue behavior: the next instance no
// worker holds, by rank. lease and rank are the behavior's own columns.
const next = context.sql.get(
  `SELECT id FROM ${context.sql.instances()} WHERE lease IS NULL ORDER BY rank DESC, created_at LIMIT 1`,
);
if (next) {
  context.instances.invoke(context.schema, String(next.id), 'claim', { worker: params.worker });
}
```

`sql.instances()` returns the name of a relation the behavior's
statements read like a table. It has one row per instance of the call's
schema in the call's namespace (for a schema of the shared namespace, the
call's namespace's instances of it), with the columns `id`, `seq`,
`version` (the schema version the instance was last written with),
`created_at`, `created_by`, `updated_at`, `updated_by` and `data` (the
instance's own fields, as the JSON text the engine stores; read one with
`json_extract(data, '$.title')`), then each of the behavior's own columns
under its own name for it (`RELATION_COLUMNS` lists the first eight). No
other behavior's column is there, and neither are the namespace and the
schema. A behavior column named like one of the eight makes the relation
a `BehaviorError`.

- It is in the SQL of every function that acts for a principal: a view,
  an operation's context, a schema-level operation's and the runner's
  work. A migration and `afterConfigChange` act for none and have no
  relation; the hook reads the instances with `eachInstance`.
- Each statement that names it asks the access policy for `read` on the
  schema as the call's principal, once however often it names it; a
  refusal is `forbidden`, as a read of another instance is. A statement
  on the behavior's own tables alone asks nothing.
- It is read-only. The engine defines it in a common table expression it
  puts ahead of the statement (first in the statement's own `WITH` list
  when it has one), and SQLite writes only to tables: an `INSERT`,
  `UPDATE` or `DELETE` that targets it fails (`no such table`). A write
  may read it, to copy rows into the behavior's own tables say.
- It joins the behavior's own tables, and the statement's parameters
  bind as written: the expression inlines the namespace and the schema as
  literals and adds no parameter.
- Its name is `bhv_<key>___instances`, under the names the engine keeps
  for the behavior ("Storage"), so it never collides with one of
  `sql.table(name)`. The SQL checks run on the behavior's statement before
  the engine adds the expression, so `engine_instances` and another
  behavior's names stay refused in it.

A migration's `indexes` make such a statement read an index rather than
every instance of the file ("Storage").

### References

A behavior that holds a reference to another instance records it with
the engine, so it hears when that instance changes or goes:

```ts
context.references.add('Spec', params.id, 'spec');   // asks read on Spec; not_found without the instance
context.references.remove('Spec', old, 'spec');      // true when there was one
context.references.list();                           // [{ schema, id, key }], in the order recorded
```

A reference is the behavior's, from its instance, to an instance of the
same namespace, under a key of its choosing (`''` by default); recording
one twice records one. Before an `update`, a `delete` or a writing
operation of a referenced instance, the engine asks each referencing
behavior's `guardReference(view, reference, request)` after the
instance's own guards, whoever the caller; a reason vetoes
(`BehaviorVetoError`, naming the referencing behavior). After the change
and its event, it runs each `afterReferenceChange(context, reference,
change)`, in the same transaction. That context is a view of the
referencing instance whose `instances.invoke` also runs writing
operations: the referencing instance changes only through an operation
invoked on it, so its guards run and it gets its own event.

After a delete no guard vetoes, no reference to the deleted instance may
remain: each hook removes its reference through such an operation. A
reference left behind is a `BehaviorError`, which rolls the delete back,
the hooks' work with it. Deleting the referencing instance drops its
references. A reference from an instance to itself is never asked about,
since the behavior's own guard and `afterChange` see that instance's
changes. The hooks act as the caller: one who may not write the
referencing schema cannot delete an instance a hook must clear.

### Schema-level operations

An operation declared with `scope: "schema"` has no instance: it runs on
the schema as a whole, from `schemaOperations`, with a `SchemaContext`:
the behavior, its config, the call, `can`, `instances`, `schemas`, and
`sql` that reads the behavior's tables and its columns across the schema
(`sql.instances()`) and writes nothing. No instance guard runs and no
event is appended for it; a writing one changes state only through the
instance operations it invokes and the instances it creates, each with
its own event.

```ts
engine.instances.invokeSchema(me, 'Order', 'summarize', { since: 0 });   // a schema-level operation of Order
```

`invokeSchema` asks the policy for `write` or `read` with the
operation's name, as `invoke` does, and runs a writing one in a
transaction; a behavior runs one with `instances.invokeSchema` ("Other
instances"). An instance operation is `not_found` there and to
`instances.invokeSchema`, and a schema-level one is `not_found` to
`invoke`, `call()` and `instances.invoke`, each naming the other scope.

### Reactions and schedules

A behavior's `reactions` and `schedules` run after the commit, on the
runner ("The runner"), as its principal: they never refuse a change, and
they do what the runner may, not what the change's caller may.

```ts
export const ledger = defineBehavior<{ watch?: string[] }>({
  declaration,
  // ...its operation mark writes a note on the instance.
  reactions: {
    watches: (config) => config.watch ?? [],          // schemas besides its own
    react(context, event) {
      if (event.kind === 'create' && event.cause === undefined) {
        context.instances.invoke(event.schema, event.instanceId!, 'mark', { note: `created by ${event.actor}` });
      }
    },
  },
  schedules: {
    sweep: { everyMs: 60_000, run(context) { /* context.previous: when it last ran */ } },
  },
});
```

`react(context, event)` gets each committed instance event of a schema
that composes the behavior, and of the schemas `watches(config, schema)`
names for it, one at a time, in log order, per namespace; `event` is the
event as `engine.events.read` returns it, with its `cause` when the
runner's work wrote it. A schedule's `run(context)` runs once an
interval on each schema that composes the behavior, in each namespace.
Their context is a schema-level one (`WorkContext`): the behavior, the
schema's config, namespace, schema and version, the runner's principal,
`now`, `can()`, `instances` and `schemas`, whose `invoke` and
`invokeSchema` run writing operations and whose `create` creates, and
`sql` reading the behavior's own tables and its columns across the
schema (`sql.instances()`). They change state only through the
operations they invoke and the instances they create, whose events
record the cause. A schedule's `everyMs` may be a function of the
schema's config ("Schedules" under "The runner"). A reaction's context adds
`before(event)`: the event's instance as the log had it just before the
event, its behaviors' fields included, `undefined` for its create, which
is all a delete leaves of it; it asks `read` on the event's schema. A
schedule's adds `schedule`, its name, and `previous`, when its last run
committed. Each is synchronous and runs in its own transaction with the
runner's record of it, and a throw rolls both back ("The runner").

### Storage

When a schema that composes a behavior is first published, the engine
records a key for it in `engine_behaviors`: the name lowercased, each run
of other characters one `_` (`acme.Rating` is `acme_rating`), with `_2`,
`_3`, ... when that key is taken. Everything the behavior owns is named
`bhv_<key>__<name>`: its columns on `engine_instances` and its tables, so
two behaviors never collide. Its migrations run through the ledger under
its name, in the publish's transaction: each adds its columns, then runs
`up(sql)`, after which every object `sqlite_master` gained must be a
table or index of its own, on a table of its own (an FTS5 table's shadow
tables are named after it). A column is `integer`,
`real`, `text`, `blob` or `any`, with an optional default; a `NOT NULL`
one needs a default, which every instance that exists takes. When an
implementation registers and the file already holds its storage, its new
migrations run then; a file whose ledger is ahead of the implementation
is refused.

A migration indexes the behavior's columns on the instances table with
`indexes`, by its own name for each index, over its own names for the
columns:

```ts
migrations: [
  { version: 1, name: 'lease', columns: { lease: { type: 'text' }, rank: { type: 'integer', notNull: true, default: 0 } } },
  { version: 2, name: 'claim order', indexes: { open_by_rank: ['lease', 'rank'] } },
],
```

The engine creates `bhv_<key>___index_<name>` on `engine_instances
(namespace, schema, <the columns>)` after the migration's columns and
before its `up(sql)`, so a statement on the relation `sql.instances()`
names that filters or orders by the columns reads the index for the
call's namespace and schema alone. The migration's ledger row records it
with its columns. A column must be one that migration or an earlier one
adds, and an index name is `[a-z][a-z0-9_]*`, used once across the
migrations; registration refuses anything else. An index is permanent: a
later migration cannot drop or change one yet.

The names under `bhv_<key>___`, the prefix and one more `_`, are the
engine's for the behavior: its indexes on the instances table and the
relation over the instances. A name the behavior gives with
`sql.table(name)` starts with a letter after the prefix, so none of
these collides with one of its tables, and a migration's SQL may not name
one or create an object under one.

A behavior's SQL runs one statement at a time. Before it reaches SQLite
the engine refuses a statement that names, bare, quoted or as a string
literal, anything starting with `engine_`, `sqlite_`, `pragma_` or `bhv_`
other than the behavior's own `bhv_<key>__` names, and one that calls
`load_extension`. A read runs `SELECT`, `VALUES` and `WITH ... SELECT`; a
write adds `INSERT`, `UPDATE`, `DELETE` and `REPLACE`; a migration adds
`CREATE TABLE`, `CREATE [UNIQUE] INDEX`, `CREATE VIRTUAL TABLE`, `ALTER
TABLE`, `DROP TABLE` and `DROP INDEX`, on names that are not under
`bhv_<key>___`. A virtual table is `[IF NOT
EXISTS] <own name> USING fts5`, unqualified: another module can reach
past the behavior's tables (`dbstat` reports on every table in the
file), and full-text search needs fts5 alone. `PRAGMA`, `ATTACH`,
transaction control, triggers, views and temporary objects are refused.
Pass data as parameters.

### Publishing

A publish runs a behavior's `afterConfigChange(context)` when the
version adds the behavior (a schema's first version included), removes
it, or changes its config as the schema holds it; a version that keeps
the config runs nothing. It runs in the publish's transaction, after the
behavior's migrations and the version's row and before its `publish`
event, once for each namespace whose instances the schema serves: the
namespace that holds it, or every namespace for a schema of the shared
one. A throw refuses the publish, and the version and every write of the
hook roll back with it.

`PublishContext` has `behavior`, `config` (the parsed config the version
gives, undefined when it removes the behavior), `before` (the version it
replaces gave, undefined when that one did not compose it), `namespace`,
`schema`, `version`, `now`, `sql` with writes on the behavior's own
tables (no `sql.instances()`: the hook acts for no principal, and the
relation asks the policy as one), and `eachInstance(visit)`, which
visits every instance of the
schema in the namespace in creation order, read 500 at a time, each `{
id, data }` with its own fields, deep-frozen. It has no principal and
asks no policy: the publish was allowed, and what the hook reads goes
into the behavior's own storage, never back to the publisher. It holds
the file's write lock until it returns, so a hook that visits every
instance costs every writer that long. `Search` rebuilds its index this
way.

### Composition

`define` and `publish` check each type's behaviors as the compiler's
loader does, with its wording, at `/types/<Type>/behaviors/<i>`: a
behavior listed twice, a config its `configSchema` (checked with ajv) or
`parseConfig` refuses, a requirement the type does not list, a conflict
it does, a field that collides with another behavior's or with one of
the type's own, by its name or its JSON key, and two behaviors that add
an operation of the same name. They also refuse a behavior with no
implementation registered, and one on a type other than the instance
type (only it has instances). A live version whose behavior has no
implementation in this engine, as after a restart without it, makes
every call on the schema `unavailable` until one registers.

### Lifecycle

| Call | Order, in one transaction for a write |
| --- | --- |
| `create` | validate (a behavior field is `readOnly`) -> insert -> each `initialize` -> each `afterChange` -> event |
| `get`, `list` | each field reader |
| `update` | refuse a behavior field (`readOnly`) -> check `expectedSeq` -> merge and validate -> nothing more if nothing changed -> every guard -> write -> each `afterChange` -> event |
| `delete` | check `expectedSeq` -> every guard, then each referencing behavior's `guardReference` -> the row goes -> each `afterChange` -> its references go -> event -> each `afterReferenceChange` -> no reference to it may remain |
| `invoke` | policy -> parameters against `paramsSchema` -> check `expectedSeq` -> every guard (and, for a writing operation, each `guardReference`) -> the handler -> its result against `resultSchema` -> for a writing operation, each `afterChange`, the next `seq`, the event and each `afterReferenceChange` |
| `invokeSchema` | policy -> parameters against `paramsSchema` -> the handler -> its result against `resultSchema`; no guard, no event |
| a behavior's `instances.create` | policy (`write`) -> as `create`, in a savepoint of the calling call's transaction |
| `publish` (`schemas`) | policy -> the compatibility rule, with each `configChange` -> each `parseConfig` with `target.schemas` (`read` on each schema it reaches) -> each composed behavior's migrations -> the version -> each `afterConfigChange` of a behavior it adds, removes or changes, per namespace -> event |

An `update` asks each `guardReference` after the guards and runs each
`afterReferenceChange` after its event, as a writing operation does.

Functions of several behaviors run in the type's list order, and the
first veto wins: `BehaviorVetoError` (`vetoed`). `call(behavior,
operation, params)` checks the parameters and runs every guard and the
handler in a savepoint, which rolls back alone if the caller catches its
failure; it does not run `afterChange` or append an event of its own, and
calls nest at most 16 deep, with invokes and reads of other instances.
`afterChange` sees the caller's change only. Anything that throws out of
a call rolls the whole call back, on every instance it reached. Reactions
run later, after the commit, on the runner.

### Instances and events

A behavior field sits in an instance's `data` beside the type's own
fields, after them, in the type's behavior order and each behavior's
field order. A reader's `undefined` or `null` leaves the field out; any
other value must be JSON. A field is read-only to `create` and `update`
(`readOnly`), and `schemas.validate` reports one the same way.
`schemas.behaviors(principal, name)` lists what a version composes, with
each config and declaration.

A writing operation appends an `operation` event, `{ behavior,
operation, params, patch }`, with the merge patch of the own fields its
`update()` changed and of the behaviors' fields; a read-only one appends
none. A create's event carries the
behaviors' fields, and an update's merges in any change they took, so
the log replays to the instance a read returns.

An operation event is a change to the instance a read returns, so a
writing operation takes the instance's next sequence and moves its
`seq`, and with it the HTTP API's `ETag`, as an update does. It does so
even when its `patch` is empty: the engine cannot see what the operation
changed in its behavior's own tables. An `update`, `delete` or
operation that expects the sequence from before the operation is refused
(`seq_mismatch`) before any guard is asked. A read-only operation checks
`expectedSeq` against the sequence it reads and moves nothing.

### Core behaviors

The core declares eight behaviors (`internal/registry/behaviors`, section
3.16 of `docs/extension-model.md`), so every binary's meta-schema admits
them, and the engine implements them in `src/behaviors/core` and
registers them when it opens, before `behaviors`: a schema that composes
them runs with no extension linked. Each implementation imports the copy
of its declaration in `src/behaviors/core/declarations`, which the core
binary writes (`make behaviors`) and CI checks (`make behaviors-check`).
They reach the engine only through the plug-in interface above. A
deployment cannot register another implementation under their names.

```json
"behaviors": [
  { "name": "Workflow", "config": {
      "states": ["draft", "review", "published"],
      "transitions": [
        { "from": "draft", "to": "review" },
        { "from": "review", "to": "published", "permission": "documents.publish" }
      ] } },
  { "name": "Comments" },
  { "name": "Revisions", "config": { "review": { "permission": "documents.review" } } }
]
```

`Dependencies`, `Links` and `Rollups` reach other instances through the
interface ("Other instances", "References"), always as the caller, with
this on a `tasks` schema whose tasks wait on tasks and documents and
belong to a project:

```json
"behaviors": [
  { "name": "Workflow", "config": { "states": ["todo", "doing", "done", "dropped"], "transitions": [...] } },
  { "name": "Dependencies", "config": { "schemas": ["tasks", "documents"], "gatedStates": ["done"] } },
  { "name": "Links", "config": { "links": {
      "spec": { "schema": "documents", "pinned": true },
      "parent": { "schema": "tasks", "required": true },
      "project": { "schema": "projects" } } } }
]
```

`Reactions` adds no field, operation or storage: the runner runs its
rules after the commit ("The runner"), as its principal, and they move
statuses through Workflow's `transition`. On a `projects` schema whose
projects finish when their tasks do, and the `tasks` above linking to
their project:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "allTerminal": { "schema": "tasks", "link": "project" } }, "then": { "transition": "done" } } ] } }
```

Their records number from 1 per instance (a comment's id, a revision, a
proposal's id), so a number says nothing about another instance. Their
list operations are read-only, so the policy is asked for `read` and they
append no event: they take `limit` (1 to 500, 50 by default) and
`cursor`, and return `{ items, next }` in the order the records were
made, as `instances.list` does. Deleting an instance deletes what they
keep for it.

Their operations are served as any behavior's are. For a schema named
`documents`, the operation route runs `transition` with `If-Match`
(`POST .../schemas/documents/instances/{id}/operations/transition`, body
`{"to": "review"}`), and it is the tool `documents.transition`, MCP handle
`documents_transition`; the list operations' tools only read (`replay`
`read_only`, `readOnlyHint`). None names an invocation policy, so each
takes the default of the policy the engine is given, the core's or a
distribution's (D11, "The invocation policy and the vendor keys"). A
refusal is the HTTP API's problem: a transition whose permission the
caller lacks is 403 `forbidden`, and over MCP a tool error carrying that
problem.

#### Workflow

A state machine on the instance's `status`.

| | |
| --- | --- |
| Config | `states` (one or more names: a letter, then letters, digits, `_` and `-`), `initial` (the first state when absent), `transitions`: `{ from, to, permission? }` |
| Fields | `status` |
| Operations | `transition({ to })` -> `{ from, to }`, writes |
| Guards | its own `transition`, whoever asks: `to` not a state is `invalid_argument`; the state the instance is in, a transition the config does not list and a move out of a terminal state are `vetoed`; a transition that names a permission the caller lacks (`can`) is `forbidden` |
| Events | `transition`'s operation event, `patch: { status }` |
| `configChange` | every old state stays; transitions, permissions and `initial` may change. Not added to or removed from a schema with instances |

A new instance starts in `initial`. The status is Workflow's own column,
so a create or an update that sets it is refused (`readOnly`), and
another behavior moves it only by calling `transition`, whose guard runs
for that call as for a caller's. `transition` takes `to` and nothing else;
its closed `paramsSchema` lets no alias through. Beyond its config
schema, which the compiler checks too, the engine refuses a config whose
`initial` or a transition names a state it does not list, and a
transition from a state to itself or listed twice. A state no transition
reaches is allowed: a new version keeps every state an instance may be
in, one it no longer enters included.

A state that no transition leaves is terminal. `isTerminalState(config,
state)`, exported by the package, answers that for a config as a schema
holds it (`schemas.behaviors` lists it): true for one of its states that
no transition leaves.

#### Comments

Comments on the instance, each a reply to one of its comments or not.

| | |
| --- | --- |
| Config | none |
| Fields | `commentCount` |
| Operations | `comment({ body, replyTo? })` -> the comment, writes; `listComments({ limit?, cursor? })` -> a page, read-only |
| Guards | none; `comment`'s `replyTo` must name a comment of the same instance (`invalid_argument`), and `body` must hold a non-space character, at most 10000 code points |
| Events | `comment`'s operation event, `patch: { commentCount }` |
| `configChange` | added to a schema with instances, which start with none; not removed from one, since their comments would stay behind |

A comment is `{ id, replyTo?, body, createdBy, createdAt }`: the caller's
subject and the engine clock's time.

#### Revisions

Immutable revisions of the instance's own fields, with an optional review
step.

| | |
| --- | --- |
| Config | `review: { permission }`, optional |
| Fields | `revision`: the latest revision's number, absent before the first |
| Operations | `listRevisions({ limit?, cursor? })`, read-only; with review, `propose({ patch, note? })`, `approve({ proposal })`, `reject({ proposal, reason? })`, which write, and `listProposals({ state?, limit?, cursor? })`, read-only |
| Guards | without review, the four review operations are `vetoed`; `approve` and `reject` need the review permission (`can`), else `forbidden`, whoever calls them |
| Events | a create's and an update's event carry `revision`; `propose` appends an operation event with an empty `patch`; `approve`'s carries the fields its patch changed and `revision` |
| `configChange` | `review` may be added, removed or changed. Added to a schema with instances, whose history starts at their next change; not removed from one |

A create records revision 1 and every change of the own fields the next,
in its transaction: an update that changes something, and an operation
that changes them with `update()`, an approval included. A revision is
`{ revision, data, createdBy, createdAt, proposal? }`, where `data` is the
own fields as the change left them, never a behavior's field. An
operation that changes no own field, a comment or a transition, records
none.

`propose` checks its patch with `validateUpdate`: one that sets a
behavior's field or leaves the instance invalid is `invalid_instance`,
and one that changes nothing is `invalid_argument`. It stores the patch
as a pending proposal and changes nothing a reader sees. Who may propose
is the access policy's call: it is asked for `write` with the operation
`propose`, apart from a plain update, which names no operation. `approve`
applies a pending proposal's patch with `update()`, so the update's
validation and every guard run, and records the revision it makes with
the proposal's id; a refusal leaves the proposal pending. The patch
applies to the instance as it is then, not as it was proposed; `base` is
the revision it was proposed against, so a reviewer can see the instance
moved. `reject` settles it with an optional reason. A proposal is `{ id,
patch, note?, base?, state, createdBy, createdAt, reviewedBy?,
reviewedAt?, reason?, revision? }`, `state` one of `pending`, `approved`
and `rejected`; approving or rejecting one that is not pending is
`vetoed`, and naming none is `invalid_argument`.

#### Dependencies

Blockers between instances, which hold up the type's Workflow.

| | |
| --- | --- |
| Config | `schemas`: the schemas a blocker may be an instance of, each composing Workflow (the type's own when absent); `gatedStates`: the terminal states of the type's Workflow a transition into waits for every blocker (every terminal state when absent). Requires `Workflow` |
| Fields | `blocked`: whether a blocker's status is not a terminal state of its own schema's Workflow |
| Operations | `addBlocker({ schema?, id })` -> `{ schema, id, status?, open }`, writes; `removeBlocker({ schema?, id })` -> `{ schema, id }`, writes; `listBlockers({ limit?, cursor? })` -> a page of `{ schema, id, status?, open }`, read-only; `listDependents({ limit?, cursor? })` -> a page of `{ schema, id }`, read-only |
| Guards | a Workflow `transition` of the instance into a gated state, whoever asks, while `blocked`: `vetoed`, naming the open blockers |
| Refusals | `addBlocker`: the instance itself, a schema the config does not list, one without Workflow, an instance that does not exist (`invalid_argument`); a blocker already added, an edge that would close a cycle, an open blocker of an instance in a gated state (`vetoed`). `removeBlocker` of an instance that does not block it (`invalid_argument`) |
| Deletes | deleting a blocker removes its edges: its reference hook invokes `removeBlocker` on each dependent, as the caller, each with its own event. Deleting a dependent deletes its edges |
| Events | `addBlocker`'s and `removeBlocker`'s operation events carry `blocked` when it changes |
| `configChange` | `schemas` and `gatedStates` may change (edges made before stay); added to a schema with instances, which start with none; not removed from one, since its edges and references would stay behind |

A blocker is open while its status is not a terminal state of its own
schema's Workflow config (`isTerminalState` over what `schemas.config`
returns), whatever the gated states say. One function answers that for
the `blocked` field and the guard, over every blocker in every schema, so
they cannot disagree. The guard reads the state a transition moves to
from its `to`, the one parameter Workflow's closed `paramsSchema` takes,
and it runs before any handler for a caller's transition, another
behavior's `call()` and another instance's invoke alike. `parseConfig`
holds `gatedStates` to the terminal states of the type's Workflow, which
it reads in `target.configs`.

Blockers are read as the caller: a caller who may not read a blocker's
schema cannot read `blocked` on the instances it blocks, or list them.
`blocked` is computed at each read, so a blocker's transition shows in
its dependents' next read, with no event on them. `listDependents`
leaves out dependents of schemas the caller may not read, so a page can
hold fewer than its limit while more follow.

#### Links

Typed links from the instance to instances of other schemas, or its own.

| | |
| --- | --- |
| Config | `links`: by camelCase name, `{ schema, required?, pinned? }`; at least one |
| Fields | `links`: `{ <name>: { schema, id, revision?, stale? } }`, the links the instance holds; absent when it holds none |
| Operations | `link({ name, id, revision? })` -> `{ name, schema, id, revision? }`, writes; `unlink({ name })` -> the link as it was, writes; schema-level `listLinked({ name, id, stale?, limit?, cursor? })` -> a page of `{ id, revision?, stale? }`, read-only |
| Guards | the delete of an instance a required link points at, whoever the caller: `vetoed` (`guardReference`) |
| Refusals | a name the config does not give, a target that does not exist, a `revision` for a link that is not pinned or past the target's latest, a pinned link whose schema does not compose Revisions (`invalid_argument`); a target with no revision yet, unlinking a required link (`vetoed`); unlinking a link the instance does not hold (`invalid_argument`) |
| Deletes | an optional link's target's delete unlinks it: its reference hook invokes `unlink` on each instance that points at it, as the caller, each with its own event. Deleting an instance deletes its links |
| Events | `link`'s and `unlink`'s operation events carry `links` |
| `configChange` | every link keeps its name and schema; `required` and `pinned` may change, and links may be added; added to a schema with instances, which start with none; not removed from one |

A link holds one target, an instance of its schema in the same namespace
(the schema looked up as any name is: the namespace, then the shared
one); `link` again moves it. A pinned link records the target's latest
revision, read through its `revision` field, or the earlier one `link`
names, and `links` reports `stale` when the target's latest revision has
moved past it. `listLinked` answers the other way round, on the schema:
which instances point a link at a target, and with `stale: true` only the
pinned ones the target has moved past, which is how to find the
instances that point at a superseded revision.

A required link can be moved, never unlinked, and its target cannot be
deleted while it points there; the refusal names the linking schema, not
the instance, which the caller may not be able to read. A pinned link is
read as the caller: without read on its target's schema, `links` cannot
be read. `stale` is computed at each read, so a target's new revision
shows in the next read of the instances that link to it, with no event on
them. A link made before its spec was pinned records no revision until it
is linked again.

#### Rollups

Values derived from the instances that point at this one through a link
of their schema's `Links` config: a parent's view of its children.

| | |
| --- | --- |
| Config | `rollups`: by camelCase name, `{ schema, link, function, field?, gatedStates? }`; at least one. `function` is `count`, `countBy`, `sum`, `min`, `max`, `all` or `any`. `countBy`, `sum`, `min` and `max` take `field`, the others none; only `all` and `any` take `gatedStates` |
| Fields | `rollups`: `{ <name>: value }`, computed at each read |
| Operations | none |
| Guards | a Workflow `transition` of the instance into a state an `all` or `any` rollup gates, whoever asks, unless the rollup holds: `vetoed`, naming the rollup and how many linked instances keep it from holding |
| Refusals | at define and publish (`invalid_schema`): a gated state that is not a state of the type's Workflow, or a type without Workflow; a linked schema with no live version, without `Links` or the link, or whose link points at another schema; a `countBy` field that is not a string, enum or boolean field of its type, or `status` when it composes Workflow; a `sum`, `min` or `max` field that is not a number or integer field; `all` or `any` over a schema without Workflow. A caller who may not read the linked schema cannot define or publish the rollup (`forbidden`) |
| Events | none of its own: a linked instance's change appends no event on this one |
| `configChange` | nothing is stored, so rollups may be added, removed and changed, and Rollups added to or removed from a schema with instances |

```json
{ "name": "Rollups", "config": { "rollups": {
    "tasks": { "schema": "tasks", "link": "project", "function": "count" },
    "tasksByStatus": { "schema": "tasks", "link": "project", "function": "countBy", "field": "status" },
    "tasksFinished": { "schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"] } } } }
```

On a `projects` schema, with the `tasks` above, a project then reads
`"rollups": { "tasks": 2, "tasksByStatus": { "doing": 1, "todo": 1 },
"tasksFinished": false }`, and cannot move to `done` until every one of
its tasks is done or dropped.

| Function | Value | Over no instance |
| --- | --- | --- |
| `count` | how many instances point here | `0` |
| `countBy` | `{ <value>: count }` over the field's values, keys sorted; a boolean's are `"true"` and `"false"`; an instance with no value is not counted | `{}` |
| `sum` | the sum of the field over the instances that hold a value | `0` |
| `min`, `max` | the least or greatest value | absent |
| `all` | whether every one is in a terminal state of its schema's Workflow (`isTerminalState`) | `true` |
| `any` | whether some one is | `false` |

The set is closed so that each function is one pass over the records it
reads, has one JSON type, and has a rule `parseConfig` checks against
the linked schema when the schema is defined; filters, averages or
expressions would make the config a query language.

A value is computed when the instance is read, as the caller (D16,
amended), and nothing is stored: a linked instance's change shows at
this instance's next read, with no event on it and no move of its `seq`
or `ETag`, and no reaction has to keep it current. For each schema and
link its rollups name, a computation reads the schema's `Links` config,
one page of `listLinked` on that schema (`instances.invokeSchema`), and,
for every function but `count`, the instances in one `getMany`, with
`status` when a function needs it, and the schema's Workflow config for
`all` and `any`. Each asks `read` on that schema, so a caller who may
not read it cannot read the instance, list its schema, or move it into a
gated state. Rollups reads no table of `Links`. A link the schema's live
config no longer gives, or that points at another schema, holds none of
its instances: a later version may drop or re-point the link only while
the schema has no instances. Each write of the instance computes its
rollups too, since the engine reads its fields before and after for the
event's `patch`; a rollup appears there only when the write changed it.

A rollup reads at most `MAX_ROLLUP_READ` (500) linked instances per
computation: one `listLinked` page and one `getMany`. Past it the rollup
has no value, and its entry is `{ "over": true }`, which no function
returns (`countBy`'s counts are numbers), so it is never read as a
count, a number or a boolean; a gate on it does not hold, and the
refusal says why. `Links` has no count operation, since every function
but `count` needs each instance's fields, which only reading it gives,
and one bound for every function keeps one rule.

The guard reads the state a transition moves to from its `to`, the one
parameter Workflow's closed `paramsSchema` takes, and runs before any
handler for a caller's transition, another behavior's `call()` and
another instance's invoke alike. A rollup may name its own schema: a
task can roll up its subtasks through its own `parent` link, and
`parseConfig` reads the version being defined for that name.

#### Search

Full-text search over the instance's own text fields.

| | |
| --- | --- |
| Config | `fields`: the type's own top-level fields to index, by JSON key, 1 to 16, each a string or a scalar whose values are strings; `weights`: by indexed field, above 0 and at most 1000, 1 for a field it does not name |
| Fields | none |
| Operations | schema-level `search({ query, syntax?, limit?, cursor? })` -> a page of `{ id, rank, field?, snippet? }`, read-only |
| Refusals | a field the type does not declare, or one that is not text (a number, an enum, a list, an object), and a weight for a field it does not index, when the schema is defined; a caller who may not `read` the schema, even one the policy lets call `search` (`forbidden`); an FTS5 expression FTS5 cannot parse, or one with a column filter (`invalid_argument`) |
| Events | none: the index is the behavior's own, written with the change of the instance |
| `configChange` | every change: `fields` and `weights` may change, and it may be added to or removed from a schema with instances |

```json
"behaviors": [{ "name": "Search", "config": { "fields": ["title", "body"], "weights": { "title": 3 } } }]
```

```ts
engine.instances.invokeSchema(me, 'notes', 'search', { query: 'release plan', limit: 20 });
// { items: [{ id: 'n2', rank: 1, field: 'title',
//             snippet: [{ text: 'Release', match: true }, { text: ' ', match: false }, { text: 'plan', match: true }] }],
//   next: null }
```

The index is one FTS5 table, `bhv_search__text`, with a column per
indexed field in the config's order, and `bhv_search__rows`, which gives
each of its rows a namespace, a schema and an id. The tokenizer is
`unicode61` with diacritics removed, so `cafe` also finds the word with
an accent on its e, and case does not matter; there is no stemming. `afterChange` writes an
instance's row in the transaction of its create, of an update or a
writing operation that changes an indexed field (an approved revision
included), and deletes it with the instance, so a search never sees a
row the instances do not hold, and a write that fails takes its index
change back with it.

A query is plain words by default. Each whitespace-separated word goes to
FTS5 as a quoted string, so quotes, `AND`, `OR`, `NOT`, `NEAR`, `*`,
`:` and parentheses in it are text: an instance matches when its indexed
fields hold every word, in any field and any order. A word FTS5 splits
(`slips-a-week`) is a phrase of its parts, one it tokenizes to nothing
(`-`, `"`) counts for nothing, and a query of only such words matches
nothing, without an error.
`syntax: "fts5"` takes the query as an FTS5 expression instead: phrases,
`AND`, `OR`, `NOT`, `NEAR`, `^` and prefixes (`wal*`). A column filter
(`title: walnut`, `{title body}: walnut`, `-title`) is refused, since it
would name the index's columns rather than the type's fields, and so is
an expression FTS5 cannot parse, as `invalid_argument` at `/query`. The
expression is the risk: FTS5 reads every term that starts with a
prefix, so `a*` over a large index is slow, and the engine runs a search
synchronously, in the process that serves every other call. The
1000-character bound on a query caps how much one expression asks for,
not how long it takes, and there is no switch to turn the syntax off: a
deployment that serves `search` to callers it does not trust takes on
that cost.

Results come best first, by bm25 with the config's weights, then in the
order the instances were first indexed. `rank` is the place in that
order, from 1, across pages; a hit carries no score. The FTS5 table is
shared by every schema and namespace in the file, so bm25's statistics
(how many rows hold a term, how long a field is on average) are taken
over all of them: the ids and snippets a search returns are only the
caller's, but their order can shift with another namespace's text, and
a score would carry that further. `field` and `snippet` are those of the
indexed field with the most matches, the earliest in `fields` on a tie:
a few words around them (at most 12, with `...` where it cuts), in parts
that each say whether they are a match, so a client marks them up as it
likes. A page is an offset into the ranking (`limit`, 50 by default, and
the `next` cursor), so a write between two pages can move an instance
across the boundary.

`search` asks the policy for `read` with the operation's name, as every
schema-level operation does, then for `read` on the schema alone: it
returns ids and text, which only a caller who may read the schema sees.
The namespace and the schema are conditions of the query, so a search
answers from the caller's namespace only, and for a schema of the shared
namespace each namespace's index holds its own instances. The access
policy answers per schema (`AccessRequest` has no instance id), so a
caller who may read the schema may read every hit.

A version that adds Search (a first version included) or changes
`fields`, their order included, rebuilds the index of the schema's
instances in `afterConfigChange` ("Publishing"); one that removes it
drops the index, so a deleted field's text does not stay behind; a
change of `weights` alone needs nothing, since they apply when a search
runs. The rebuild runs in the publish's transaction and holds the
write lock until every instance is indexed: on an Apple M-series laptop,
adding Search to a schema of 10,000 instances of about 160 words each
took 0.4 s and changing its fields 0.5 to 0.9 s, on Node.js and on Bun.

There are no vectors. D16 lists them as optional beside search; SQLite
has no vector search without an extension, which the engine refuses to
load, and an embedding comes from a provider the engine would call
outside its synchronous write transaction.

#### Reactions

Rules that move Workflow statuses after a change commits.

| | |
| --- | --- |
| Config | `rules`: one to 64, each one `when` and one `then`. `when` is `{ enters: <state> }` or `{ allTerminal: { schema, link } }`; `then` is `{ transition: <state>, link? }`. Requires `Workflow` |
| Fields, operations | none |
| Reactions | `enters`: the instance's status became the state, by a create or a transition. `allTerminal`: an instance of `schema` that links to this one through `link` changed or went, and every instance linking here through it is in a terminal state of its own schema's Workflow, at least one. `then` moves this instance, or the one its `link` points to, to the state |
| Refusals at define | a state the type's Workflow lacks, in `enters` or in a `then` on the instance itself; a `then.link` the type's Links lacks, or Links absent; an `allTerminal` on the type's own schema whose link does not point at it; a `then` on the instance itself that no transition of its Workflow allows; `enters` rules on the instance itself whose states cycle |
| Failures at run | a target schema without Workflow, a state its Workflow lacks, an `allTerminal` schema that does not link here through `link`: the subscription retries, then halts |
| Events | each move is Workflow's `transition` operation event on the target, actor the runner's principal, `cause` the event that set it off |
| `configChange` | any; added to and removed from a schema with instances, since it keeps no state |

The rules run in order on each event the subscription hears: the
schema's own events, and those of each `allTerminal` schema. An
`allTerminal` rule looks at the instance the event's instance links to
now and, after a delete or a change of its links, the one it linked to
before (`before`), and finds the instances linking there with Links'
`listLinked`, as the runner's principal.

A rule acts only where it can. A target already in the state, with no
transition to it from where it is now, or whose guards veto the
transition (an open blocker of `Dependencies`, say) is left as it is, and
so is the instance of an `enters` rule on itself once it has moved on
from the state. A rule reads the target's Workflow config to decide, and
moves the status only through `transition`, so Workflow's guard, and
every other guard of the target, still runs. A transition that names a
permission needs the runner's principal to hold it. A failure at run is
the deployment's config naming something that is not there; the
subscription halts on it so the deployment can fix it and resume.

Rules on the instance itself chain, an `enters` rule's move being an
event the next rule can enter on, and their cycles are refused at
define. Rules across instances can chain without bound in data, a task
whose parent's parent is a task, say; the runner's depth limit stops them.

## Namespaces

Schemas and instances live in namespaces. There is one, `default`, unless the
deployment configures more (`namespaces: { names, shared }`); a name is
lowercase letters, digits and hyphens, starting with a letter. Each
namespace has its own drafts and version lines. The optional shared
namespace is where every other namespace looks a schema name up after
itself. A name is defined on one side of that lookup only: a namespace
cannot define a name the shared namespace holds, and the shared namespace
cannot define a name another namespace holds, so what a namespace reaches
never changes under it.

## HTTP

`@superschematic/engine/http` serves an engine over HTTP (D16).
`engineApp(engine, options)` returns a Hono app with a fixed set of routes
that carry the namespace and the schema name as path parameters and
resolve them per request, so publishing a version mounts nothing. Every
route goes through `@superschematic/http-runtime` (D15): the request id,
the rate limit, the timeout, the body limit, the authentication gate, the
`{data, meta: {requestId}}` envelope and RFC 9457 problem documents.

```ts
import { Hono } from 'hono';
import { serve } from '@hono/node-server';
import { openEngine } from '@superschematic/engine';
import { engineApp } from '@superschematic/engine/http';

const engine = openEngine({ path: 'shop.db', policy });
const app = new Hono();
app.route('/api', engineApp(engine, {
  authenticate: async ctx => callerFor(ctx.bearerToken),   // the deployment's Authenticator
  rateLimitPerMinute: 600,
  timeoutSeconds: 10,
}));
serve({ fetch: app.fetch, port: 8080 });
```

The options are the HTTP runtime's router options (`authenticate`,
`permissionMatcher`, `onError`, `bodyLimitBytes`, `rateLimit`), with
`rateLimitPerMinute` and `timeoutSeconds` for every route and `stream:
{ pageSize, heartbeatMs }`. The deployment's `onError` sees what the
engine does not raise.

### Routes

| Method | Path | Engine call | Success |
| --- | --- | --- | --- |
| GET | `/namespaces/{namespace}/schemas` | `schemas.list` | 200, the names the namespace reaches |
| POST | `/namespaces/{namespace}/schemas` | `schemas.define`, body: the document | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}` | `schemas.live` | 200, the live version |
| GET | `/namespaces/{namespace}/schemas/{name}/draft` | `schemas.draft` | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}/versions/{version}` | `schemas.version` | 200, that version |
| POST | `/namespaces/{namespace}/schemas/{name}/publish` | `schemas.publish` | 200, `{namespace, name, version, published}` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances?limit=&cursor=` | `instances.list` | 200, `{items, next}` |
| POST | `/namespaces/{namespace}/schemas/{name}/instances` | `instances.create`, body `{"id"?, "data"}` | 201, the instance, `ETag`, `Location` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.get` | 200, the instance, `ETag` |
| PATCH | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.update`, body: a merge patch; `If-Match` | 200, the instance, `ETag` |
| DELETE | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.delete`; `If-Match` | 200, `null` |
| POST | `/namespaces/{namespace}/schemas/{name}/instances/{id}/operations/{operation}` | `instances.operate`, body: the parameters; `If-Match` | 200, the result, `ETag` |
| POST | `/namespaces/{namespace}/schemas/{name}/operations/{operation}` | `instances.invokeSchema`, body: the parameters of a schema-level operation | 200, the result |
| GET | `/namespaces/{namespace}/schemas/{name}/describe` | `tools.describe` | 200, the describe document ("Tools") |
| GET | `/namespaces/{namespace}/tools` | `tools.manifest` | 200, the tools document ("Tools") |
| GET | `/namespaces/{namespace}/events?after=&limit=&schema=&instanceId=` | `events.read` | 200, `{events, next, more}`; with `Accept: text/event-stream`, the stream |

A schema version is the stored record without its canonical text, which
`hash` identifies. An instance is the stored record (`namespace`,
`schema`, `id`, `schemaNamespace`, `version`, `seq`, `data`, and who
created and last updated it, and when); its `data` carries its
behaviors' fields, which a create or an update may not set (422,
`readOnly`). A request body is `application/json`, and an update's
`application/merge-patch+json` (RFC 7386); another media type is 415,
with `Accept-Patch` on PATCH. An operation's body is its parameters, and
no body is `{}`; its result, whatever JSON it is, is the envelope's
`data`, and the instance's sequence after the call its `ETag`, which a
read-only operation leaves where it was. A schema-level operation's route
takes its parameters the same way and answers its result with no `ETag`,
since it names no instance; each operation route answers 404 for an
operation of the other scope.

### Statuses

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_argument` | a page size, cursor, instance id, schema name or version the engine refuses; an operation's parameters its `paramsSchema` refuses (`OperationParamsError`), `details.issues` |
| 400 | `bad_request` | a parameter or body the runtime cannot decode, a create body that is not `{id?, data}`, a path that is not valid percent-encoding |
| 401 | `unauthorized` | the `Authenticator` returned no caller, or one without a subject |
| 403 | `forbidden` | the access policy refused, or a behavior refused a caller without the permission its config names |
| 404 | `not_found` | no such version, draft or instance in the namespace, or no such route |
| 404 | `unknown_namespace` | the namespace is not configured |
| 409 | `conflict` | an instance with the id exists |
| 409 | `name_taken` | the name is defined on the other side of the shared lookup |
| 409 | `incompatible_change` | the version breaks the compatibility rule; `details.changes` |
| 409 | `vetoed` | a behavior's guard refused the update, the delete or the operation; `details` is `{behavior, action, reason}` |
| 412 | `seq_mismatch` | `If-Match` names a sequence the instance is no longer at |
| 413 | `payload_too_large` | the body exceeds `bodyLimitBytes` |
| 415 | `unsupported_media_type` | the body is not of the route's media type |
| 422 | `invalid_schema` | the document is refused; `details.issues` |
| 422 | `invalid_instance` | the instance, or an update's result, is refused; `details.issues` |
| 429 | `too_many_requests` | the rate limit; `Retry-After` |
| 500 | `internal_error` | anything else the deployment's `onError` does not map, a `BehaviorError` included; the failure stays off the wire |
| 503 | `unavailable` | the schema's live version composes a behavior this engine has no implementation for, or whose implementation refuses its config |
| 504 | `gateway_timeout` | the timeout elapsed |

`engineProblem` and `ENGINE_ERROR_STATUS` hold the engine's rows. `type`
is `about:blank`, as the HTTP runtime writes it for every problem; `code`
names the problem. A detail names only what the request named, so an
instance id that exists only in another namespace answers as one that
exists nowhere. The namespaces a deployment configures are not secret: an
unknown namespace is 404, and a configured one the policy refuses is 403.

### Authentication and access

The runtime's gate requires a caller on every route: the deployment's
`Authenticator` returns a principal, or none (401). The engine acts as
that principal, which already has the engine's `Principal` shape, and
asks its access policy (403). The routes name no permissions, so
`permissionMatcher` matters only where the policy calls it.

### Concurrency

An instance's `seq` is its entity tag (`ETag: "3"`). PATCH, DELETE and an
operation's POST pass the sequence `If-Match` names to `update`, `delete`
and `operate` as `expectedSeq`, which the engine checks inside the write
transaction, so a lost update answers 412 and writes nothing. `*` asks
only that the instance exist; a weak tag never matches. An instance that does not exist is 404 whatever
`If-Match` says: RFC 9110 evaluates a precondition only where the request
would otherwise succeed. A writing behavior operation moves `seq` too
("Instances and events" under "Behaviors"), so a tag read before it no
longer matches.

### The event stream

`GET /namespaces/{namespace}/events` with `Accept: text/event-stream`
answers server-sent events:

```
: open

id: 41
data: {"cursor":41,"kind":"create","namespace":"default","schema":"Order","instanceId":"o1","seq":1,"version":1,"actor":"alice","at":1790000000000,"change":{"title":"Desk"}}

: keepalive
```

- Each event is one message with no `event:` field, so
  `EventSource.onmessage` receives every one: `id:` is its cursor and
  `data:` the event as `events.read` returns it, of every kind, an
  `operation` included.
- The stream starts after `Last-Event-ID`, which a reconnecting
  `EventSource` sends, else after `after`, else at the start of the log.
  `schema` and `instanceId` filter as on the JSON route; `limit` applies
  to the JSON route only.
- It replays in pages of `stream.pageSize` (100) and reads the next page
  only when the server has sent the previous one, so a slow client holds
  one page and replay never loads the backlog. Caught up, it waits for the
  engine's notice of a commit (`events.watch`) and reads on from its
  cursor.
- While it waits it sends a comment every `stream.heartbeatMs` (5000),
  under the 10 seconds after which `Bun.serve` closes a quiet connection.
- A namespace's stream, like its JSON page, carries the shared
  namespace's publish events, which change the schemas it reaches.
- It reads as its principal: `read` on each event's schema in the
  namespace, and events of schemas it may not read are skipped (with
  `schema`, `read` on that schema, or 403). The first page is read before
  the response, so a refusal is a problem document. A later refusal, a
  policy change say, ends the stream; the client reconnects and gets the
  problem.
- It ends when the client disconnects, which removes its watcher, and
  when the engine closes. `timeoutSeconds` covers the first page, not the
  stream.

## Tools

`engine.tools` holds a describe document per schema, the tools document
of a namespace, and the calls the MCP tools make. Each reads and calls
through the schema registry and the instance store, so the access policy
answers every one.

### The describe document

`tools.describe(principal, name, { namespace })` (asks `read`) describes
a schema's live version:

```json
{
  "namespace": "default", "name": "Item", "schemaNamespace": "default",
  "version": 1, "hash": "9f2c...", "instanceType": "Item",
  "instance": {
    "type": "object", "additionalProperties": false,
    "properties": {
      "count": {"description": "The count.", "readOnly": true},
      "title": {"description": "A string value", "type": "string"}
    },
    "required": ["title"]
  },
  "behaviors": [{"name": "test.Counter", "description": "Counts up.", "config": {"start": 0},
                 "fields": [{"name": "count", "description": "The count."}], "operations": ["increment"]}],
  "operations": [
    {"name": "create", "description": "Creates an Item: ...", "writes": true, "invocationPolicy": "auto",
     "params": {"type": "object", "additionalProperties": false, "properties": {"data": {...}, "id": {...}}, "required": ["data"]},
     "result": {...}, "tool": "item.create"},
    {"name": "increment", "behavior": "test.Counter", "scope": "instance", "description": "Adds to the count.", "writes": true,
     "invocationPolicy": "auto", "params": {...}, "result": {...}, "tool": "item.increment"}
  ]
}
```

- `instance` is the JSON Schema of an instance's `data`: closed, its own
  fields, then its behaviors' fields, `readOnly` and without a type, since
  a declaration gives a field only a name and a description.
- `operations` lists `create`, `get`, `list`, `update`, `delete`, then each
  behavior's operations in the type's list order. `params` is the
  operation's tool arguments (below), `result` the JSON Schema of what it
  returns (an instance, a page, `null` for a delete, a behavior operation's
  `resultSchema`), and the invocation policy sits under the policy's key.
  A behavior's operation carries `behavior` and `scope`, `instance` or
  `schema`.

A field's JSON Schema is what the SDK generators write for the same field
as a tool argument (`internal/generator/toolsutil`), keyed by the field's
JSON key: a primitive, a scalar with its type (from its `json_schema`
mapping), format, description, pattern, lengths and range and its name
under `x-superschematic-scalar`, an enum's values, a nested type as a
closed object (a type already being expanded as a plain reference), `items`
for a list and `items` of `items` for a list of lists, and a field that is
not required nullable. `Generic.JSON` allows every JSON type but null, and
an optional one null too, as the Go side writes it (D14, amended). The
schema runtime's GraphQL primitive
names, which the Go loader does not read, are the primitive they name,
`Int` an integer. `runtime/engine/testdata/tool_parameters_parity.json`,
which `go test ./internal/generator/toolsutil -run
TestEngineToolParametersParity -update` writes, holds this to the Go
output for a document with a field of every catalog scalar, digests
included; `typeArguments(document, type, keys)` returns it for any type.

### The tools document

`tools.manifest(principal, { namespace })` is `tools/schema.json`'s shape
(`ir.ToolManifest`, section "The tool documents" of the MCP tools
reference): a tool per operation of every live schema the namespace
reaches that the principal may read, by name, after three schema tools.

| Tool | Name | MCP handle | Arguments |
| --- | --- | --- | --- |
| create | `<schema>.create` | `<schema>_create` | `id` (optional), `data` |
| get | `<schema>.get` | `<schema>_get` | `id` |
| list | `<schema>.list` | `<schema>_list` | `limit`, `cursor` |
| update | `<schema>.update` | `<schema>_update` | `id`, `patch` (a merge patch; nothing required), `expectedSeq` |
| delete | `<schema>.delete` | `<schema>_delete` | `id`, `expectedSeq` |
| a behavior operation | `<schema>.<operation>` | `<schema>_<operation>` | `id`, `params` (its `paramsSchema`), `expectedSeq` |
| a schema-level behavior operation | `<schema>.<operation>` | `<schema>_<operation>` | `params` (its `paramsSchema`) |
| list schemas | `engine.listSchemas` | `list_schemas` | none |
| describe a schema | `engine.describeSchema` | `describe_schema` | `name` |
| define a draft | `engine.defineSchema` | `define_schema` | `document` |

A name follows the SDK generators, `<namespace>.<method>`, with the schema
name in kebab case as the namespace (`LineItem` is `line-item`). An SDK
tool's handle is authored with `@mcp`; the engine derives one from the
same parts in snake case (`line_item_add_note`). A handle `@mcp` would
refuse (not lowercase snake case, longer than 48 characters), one two
tools derive, or a schema tool's hides the tool with its reason in
`hiddenReason`; the schema tools keep theirs. A tool the access policy
refuses the principal is hidden too, with that reason. `requiresAuth` is
true, `httpMethod` and `httpPath` name the HTTP route, a read-only tool's
`replay` is `read_only`, and `inputSchemaDigest` hashes the arguments as
the Go encoder writes them.

No tool publishes. `define_schema` stores a draft as `schemas.define`
does; the draft goes live only through the HTTP publish route, which the
access policy governs, so an MCP client cannot put a schema live on its
own (D16).

`tools.call(principal, handle, args, { namespace })` runs a tool: the
engine call its handle names, with its arguments checked (an unknown or
mistyped argument is `invalid_argument`). A handle the namespace has no
visible tool for, among the schemas the principal may read, throws
`UnknownToolError` (`not_found`).

### The invocation policy and the vendor keys

They are the deployment's binary's registrations in the compiler, which
the engine cannot read, so they are `openEngine` options, the core's by
default:

```ts
openEngine({
  path, policy,
  tools: {
    invocationPolicy: { key: 'confirm', values: ['never', 'always'], default: 'never' },
    invocation: { delete: 'always', defineSchema: 'always' },
    keys: { scalar: 'x-acme-scalar', guidance: 'acme/operation-guidance', parameters: [{ key: 'x-acme-arguments', value: 1 }] },
  },
});
```

- `invocationPolicy` is D11's key, values and default. A built-in
  operation or schema tool takes `invocation`'s value for it, else the
  default; a behavior operation takes its declaration's `invocationPolicy`,
  else the default. Registration refuses a declaration whose value is not
  one of the values, as the compiler's `Finalize` does. The core's
  behaviors name none, so they register under any policy.
- `keys` are `apigen.ToolKeys` (section 3.14 of `docs/extension-model.md`):
  the key a property names its scalar under, the `_meta` key of a tool's
  guidance, and keys written at the root of every argument schema.

`openEngine` refuses, with a `TypeError` naming every problem, what the
registry refuses: a key `@mcp` already uses or not a letter followed by
letters, digits or underscores, no values, a value that is not lowercase
letters, digits, `_` and `-` starting with a letter, a value listed twice,
a default or an `invocation` value outside the values, and a parameter
key that is empty, repeated, the scalar key or one the core writes.

## MCP

`@superschematic/engine/mcp` serves the tools over MCP (D16):
`engineMcp(engine, options)` returns a Hono app with
`/namespaces/{namespace}/mcp`, mounted beside the HTTP API with the same
options.

```ts
import { engineApp } from '@superschematic/engine/http';
import { engineMcp } from '@superschematic/engine/mcp';

const app = new Hono();
app.route('/api', engineApp(engine, options));
app.route('/api', engineMcp(engine, options));   // POST /api/namespaces/default/mcp
```

- The route goes through the HTTP runtime like every other: the request
  id, the rate limit, the timeout, the body limit and the authentication
  gate with the deployment's `Authenticator`. Its caller is the principal
  of every call, so the access policy answers each. An unknown namespace
  is the HTTP API's 404 problem; a request without a caller is 401.
- The protocol is the official TypeScript SDK's
  (`@modelcontextprotocol/server`, pinned): its web-standard handler
  answers each request with a fresh server, statelessly, on the 2025
  revisions (`initialize`, `tools/list`, `tools/call`; GET and DELETE
  answer 405, since there is no session) and on 2026-07-28
  (`server/discover`). It runs on Hono under Node.js and Bun and brings no
  HTTP server of its own; its dependencies are `zod` and
  `@modelcontextprotocol/core`.
- `tools/list` lists the visible tools of the tools document: the handle
  as the name, the title, the description, the arguments as `inputSchema`,
  `annotations.readOnlyHint`, and `_meta` with the tool's guidance and its
  invocation policy under the policy's key. The list is the caller's: a
  tool the policy refuses is not in it. The schema tools
  (`list_schemas`, `describe_schema`, `define_schema`) are in every
  caller's list, since they name no schema until they are called: the
  access policy answers the call, not the listing.
- `tools/call` returns the result as JSON text and, when it is an object,
  as `structuredContent`. A call the engine refuses is a tool error:
  `isError`, with the problem document the HTTP API answers with as text
  and as `structuredContent` (a behavior's defect is the 500 problem, its
  failure off the wire). A tool the namespace does not have, or one of a
  schema the caller may not read, is a JSON-RPC invalid-params error
  (-32602); a malformed message is the SDK's JSON-RPC error.
- `serverInfo` (the package's name and version by default) and
  `instructions` are what `initialize` reports.

The route validates no `Origin` header: it requires a caller, and a
deployment that serves it to a browser on a local address puts the SDK's
`originValidationResponse` in front of it.

## Development

```
cd runtime/schema/typescript && bun install --frozen-lockfile && bun run build
cd runtime/http/typescript && bun install --frozen-lockfile && bun run build
cd runtime/engine/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test        # build, then test:node (node --test) and test:bun (bun test)
```

The tests are TypeScript that Node.js runs with type stripping and Bun
runs as is; they import the built package from `dist/` and open real
SQLite files in temporary directories. On Bun most cases run against both
adapters. `test/http.test.ts` and `test/operations.test.ts` drive the
routes through Hono's `app.request`; `test/stream.test.ts` reads the event
stream and `test/mcp.test.ts` speaks MCP with the official client
(`@modelcontextprotocol/client`) to a listening server,
`@hono/node-server` on Node.js and `Bun.serve` on Bun.
`test/tools-parity.test.ts` asserts the Go vectors.
