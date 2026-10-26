# Engine

The engine runs a schema with no generated code (D16 in
`docs/DECISIONS.md`). It takes a schema as data, one JSON schema-file
document, versions it per namespace, and keeps it, its instances and an
event log in a SQLite file, or, for a server in a stack, in a schema of a
Postgres database.

It is one Rust library every server links (D58): `rust/` is its Cargo
workspace. `superschematic-engine-storage` (`rust/storage`) is the storage
over SQLite and Postgres; `superschematic-engine` (`rust/engine`) the
engine: namespaces, the schema registry with its validator and
compatibility rule, instances, the event log, the value store, the access
policy as data, the behavior API, the runner, the describe and tools
documents, and the HTTP API as one request and one response any host
mounts (`engine::api`), with the axum router a Rust server mounts it with
(the `axum` feature); `superschematic-engine-behaviors` (`rust/behaviors`)
the core's fifteen behaviors; the work queue's seven are
`superschematic-engine-workqueue` (`runtime/engine-workqueue/rust`), and
the commerce behaviors (D70) and their supply contract
`superschematic-engine-commerce` (`runtime/engine-commerce/rust`);
`superschematic-engine-testkit` (`rust/testkit`) replays the scenario and
HTTP corpora (`testdata/`) on both databases. Each host language reaches
it through a binding:

- TypeScript: `@superschematic/engine` (`typescript/`), over the engine's
  Node-API addon (`rust/napi`, `export_engine_napi!`; `rust/addon`, the
  core's addon, over the core's behaviors, the work queue's and the
  commerce ones), which a release ships prebuilt as
  `@superschematic/engine-<target>`. `openEngine`
  opens it on a SQLite file with every call synchronous; `openEngineAsync`
  with every call a promise, for a server on Postgres. `engineApp` and
  `engineMcp` (`./http`, `./mcp`) mount its own routes on Hono, and
  `./client` is a typed client of the HTTP API.
- Go: `go/`, over the engine's C ABI (`rust/abi`; `rust/ffi`, the core's
  archive), with `enginehttp`, which mounts the routes on chi ("The Go
  binding").
- Rust: the crate itself, and its axum router ("Mounting the Rust engine
  on axum").

A behavior is a Rust type (the `Behavior` trait): the core's and the work
queue's are in every binding, and an extension's is a crate of its own,
which the project's engine crate the `projectengine` generator writes
compiles in, with a C ABI archive and a Node-API addon over it for Go and
TypeScript servers ("Behaviors").

Until the cutover (D58, milestone 14) `@superschematic/engine` was a
TypeScript implementation of the engine, which the Rust engine was held
to: its suite and the corpus recorded from it. Its internals are gone;
"Breaking changes at the cutover" lists what changed for a caller of the
package.

```ts
import { allowAll, openEngine } from '@superschematic/engine';

const engine = openEngine({ path: 'shop.db', policy: allowAll });
const me = { subject: 'alice', permissions: [] };

engine.schemas.define(me, orderSchemaJSON);       // the draft of "Order"
engine.schemas.publish(me, 'Order');              // { version: 1, published: true }

const order = engine.instances.create(me, 'Order', { title: 'Desk', quantity: 1 });
engine.instances.update(me, 'Order', order.id, { quantity: null, status: 'open' });
const page = engine.instances.list(me, 'Order', { limit: 20 });   // { items, next }
const open = engine.instances.list(me, 'Order', { where: { status: ['open', 'shipped'] } });
const desk = engine.instances.lookup(me, 'Order', { number: 'eu/2026/0042' });  // a @unique field
const feed = engine.events.read(me, { after: 0 });                // { events, next, more }
const live = engine.events.read(me, { after: 'head' });           // only what commits from now on
engine.close();
```

## Runtimes

The package ships compiled ESM with declarations (`dist/`), since Node.js
does not strip types from files under `node_modules`. It runs on Node.js
24 and on Bun, and its tests run under both. It loads the engine's
Node-API addon when the first engine opens, never when it is imported,
from the first of:

- a file the host names: the engine option `addon`, or for every engine
  the process opens `SUPERSCHEMATIC_ENGINE_ADDON`, a project's addon, the
  `napi/` member of the engine crate its build writes
  (`scripts/engine-napi.sh --crate <crate>/napi --out <dir>` prints the
  file). Nothing else is tried when it does not load;
- a checkout: `scripts/engine-napi.sh` writes the core's addon into
  `typescript/native/` (`--js-behaviors` the test addon, "Development");
- the platform package `@superschematic/engine-<target>` (darwin-arm64,
  darwin-x64, linux-x64-gnu, linux-arm64-gnu), an optional dependency of
  this package at its version, of which npm installs the one for its
  host.

An addon speaks one version of the contract between it and this package
(`NATIVE_ABI_VERSION`); one of another version is refused.

It needs `@superschematic/http-runtime` next to it in `node_modules`, and
`superscalar`, which the runtime imports; until they are published, a
consumer declares both itself (D3), and this package's own build and tests
get them from `typescript/scripts/link-local-deps.mjs`. The main entry
point imports the HTTP runtime's framework-free entry point, for the
default permission matcher, and not Hono; the `./http` and `./mcp` entry
points also need `hono`, an optional peer dependency. The `./client`
entry point imports nothing but its own modules, so it runs in a browser
and a worker runtime too. The `./testing` entry point is behaviors
written in TypeScript, for tests ("Behaviors").

### A TypeScript server: `./server`

`@superschematic/engine/server` is what a server opens and mounts (D58),
the twin of the Go binding below, and what a generated TypeScript server
imports: it loads the addon's facade (`src/native`) and the HTTP runtime.

- `openEngineInBackground(options, { setup, retryMinMs, retryMaxMs,
  onError, onOpen })` opens the engine with `openEngineAsync`, off the
  JavaScript thread (the addon's `openAsync`, contract 3), and tries again
  after each failure, the open's or the setup's, waiting a second,
  doubling to thirty, until it opens or `close()`. Its `EngineBackground`
  is `current` (the open engine or `undefined`), `engine()` (it, or
  `EngineNotOpenError` with why the last attempt failed), `wait(signal?)`,
  `ready()` (open, and `health()` ready) and `close()`. A setup runs on the
  engine before it is handed out; one that rejects closes the engine, and
  the next attempt opens it again.
- `AsyncEngine.deploy(documents, { namespace })` defines and
  publishes a server's schema documents as the engine's own principal,
  each one whose live version has its hash left as it is, and `health()`
  is what readiness reads; both are the engine's calls (`engine::calls`),
  which the C ABI makes too. Its members are typed as the synchronous
  engine's, each method returning a promise of what that one returns.
- `engineApp` and `engineMcp` serve a background engine from the start:
  every route answers 503 `unavailable` ("The engine is not available")
  until it opens, after the identity service's CORS and before any
  authentication, as `enginehttp.Background` does. `identityRoutes: {
  sessions: false }` takes an API's identity service for its callers and
  CORS alone, the API serving the user model's routes itself; `done`, a
  signal, ends every event stream when the host stops serving. They are the
  `./http` and `./mcp` entries' functions, which take a background engine
  too.
- `engineRouteSpecs(prefix, addon?)` are the engine's routes under a
  mount, for a host's CORS, read from the addon the host names, as an
  engine's open does.
- `tsconfig.server.json` emits the entry's modules without checking types,
  so an image builds them without installing the package's own
  dependencies.

### The Go binding

A Go server reaches the Rust engine through `go/`, the module
`github.com/parable-work/superschematic/runtime/engine/go` (D58), which
links the static archive `libsuperschematic_engine.a` through cgo, as the
version graph's Go binding links its own. `go/README.md` is its guide.

- The archive is `rust/ffi`, the engine's C ABI (`rust/abi`,
  `go/include/superschematic_engine.h`) over the core's behaviors: JSON in and out, output the
  archive allocates and `eng_free` releases, and a panic caught at every
  export. `eng_open` and `eng_close` hold an engine by handle; `eng_call`
  makes any of the calls by name (`engine::calls`) and the binding's own
  `deploy`, `health` and `abi`; `eng_http` serves one request through the
  transport-neutral routes, an event stream a handle that
  `eng_stream_poll` pulls without waiting; `eng_wait` blocks one thread
  until a commit or a signal (D77), for every stream of the engine; `eng_runner` starts,
  stops and runs the runner. Each returns `ENG_OK`, `ENG_ERROR` with an
  error document (this package's class, code, message and details),
  `ENG_BAD_ARGUMENTS` or `ENG_CLOSED`.
- `scripts/engine-archive.sh` builds the core's archive, the ABI over
  `core()`, with `tools.env`'s Rust, and `--print` gives the `-L` flag for
  `CGO_LDFLAGS`. A project whose schemas compose an extension's behavior
  builds an archive of its own from a crate that exports the ABI over its
  behaviors (`export_engine_c_abi!`), with `--crate`; the test kit's,
  `rust/testkit-ffi`, is one, for the replays.
- `engine.Open`, `Call`, `Deploy` (the server's schema documents, defined
  and published as the engine's own principal, nothing written when the
  live version has the hash), `Health` and `Ready`, `Runner` and `Close`
  are the API; refusals are `*engine.Error`, with this package's class,
  code and details.
- `enginehttp.Handler(e, Options{Prefix, Caller, ...})` mounts the routes,
  the event stream and MCP on chi, one chi route per route, the host's
  timeout on every route but the stream's and MCP's.
- The replay (`go/enginehttp/replay_test.go`) holds it to the HTTP corpus
  through `enginehttp`, and to the engine's own scenario files through
  `Engine.Do`, on SQLite and on Postgres, with the Rust replay's counts.
- `scripts/engine-link-check.sh` links superscalar's, the version graph's
  and the engine's archives into one Go binary and runs it: each archive
  defines its own C functions alone (the version graph's `vg_*` is a
  feature the engine's build leaves off), all three built with one Rust
  release. The release ships the engine's archive and header in each
  platform's archives tarball.

## Breaking changes at the cutover

The cutover (D58, milestone 14) made `@superschematic/engine` the Rust
engine's addon. For a caller of the TypeScript package:

- `openEngine` and `openEngineAsync` open the Rust engine, on the addon
  "Runtimes" names. A host with no addon for its platform cannot open
  one. `Engine` is that engine's class. Renamed: `AsyncNativeEngine` is
  `AsyncEngine`, `NativeEngineOptions` `EngineOptions`,
  `NativePostgresOptions` `PostgresOptions`, `NativeUnsupportedError`
  `UnsupportedError` (now exported, its message "... is not supported by
  the engine (gap)"); `openNativeEngine` is `openEngine`, and the
  `native/index` module is gone.
- The TypeScript engine's classes and helpers are gone: `Namespaces`,
  `SchemaRegistry`, `InstanceStore`, `EventLog`, `Runner`,
  `BehaviorRegistry`, `Storage`, `ToolCatalog`, `EngineValues`,
  `SqliteValueDriver`, `openDriver`, `isBun`, `migrate`,
  `appliedMigrations`, `engineMigrations`, `ENGINE_OWNER`, `kebabCase`,
  `snakeCase`, `typeArguments`, `linkedTo`, `movedOn`, `targetMove` and
  `SEARCH_SCHEMAS_PARAMS`, with the types `DriverName`, `SqlDriver`,
  `StorageOptions`, `AppliedMigration`, `Migration`, `MigrationResult`,
  `MigrationSet`, `ValueDriver` and `TargetMove`. `SchemaValidator` and
  `EngineStorage` (what `engine.storage` is) are types alone.
- `EngineOptions.driver` is refused: the engine has a SQLite of its own.
  `busyTimeoutMs` is an engine option. A value driver written in
  TypeScript (`values.driver`) is refused (`UnsupportedError`, `driver`):
  the engine's drivers are Rust `ValueDriver`s. `ids` and `clock` also
  take data (`"uuid"`, `{ sequence }`, `{ seeded }`; `"system"`,
  `"strict"`, `{ manual }`), and a function is read once for each call.
- A behavior is Rust. `defineBehavior`, `BehaviorConfigError`,
  `RELATION_COLUMNS`, `page`, `pageRequest`, `MIN_SCHEDULE_MS`,
  `MAX_BATCH_READ`, `MAX_CALL_DEPTH` and every behavior-authoring type
  (`BehaviorImplementation`, `AnyBehaviorImplementation`, the contexts,
  `BehaviorGuidance`, `ConfigChange`, `FrozenJSON`, `SqlReader`, ...) moved
  to `@superschematic/engine/testing`. An implementation written there
  runs on the test addon alone (`scripts/engine-napi.sh --js-behaviors`);
  the addon a release ships refuses it (`UnsupportedError`, `bridge`).
  `behaviors.lookup` is refused too.
- `engine.storage.connection()` returns a `SqliteConnection`, the
  package's own over `node:sqlite` or `bun:sqlite`, in place of a
  `Storage`.
- `SchemaRecord.document` is a `SchemaFileDocument`, a JSON object, and
  `DescribeDocument.display` the package's `TypeDisplay`, not the schema
  IR's types.
- `./mcp` no longer exports `listTools`, and no longer needs
  `@modelcontextprotocol/server`: the engine speaks MCP itself.
  `engineApp` and `engineMcp` serve an `Engine` or an `AsyncEngine`;
  `engineIdentity` and `engineCapabilities` take either.
- `EngineErrorCode` gains `busy`, a lock held past the busy timeout (503,
  and the call may be tried again).
- The package no longer depends on `ajv`, `@superschematic/versiongraph`,
  `@superschematic/schema-runtime` or `@superschematic/schema-ir`.
- `@superschematic/engine-workqueue` no longer exports its seven
  behaviors' objects (`lease`, `assignment`, `queue`, `presence`,
  `blueprint`, `budget`, `retries`): `workQueueBehaviors` is their names,
  which the engine's `behaviors` option takes as they are.

## Storage

A SQLite file (`path`), through the SQLite the engine's Rust storage
bundles (rusqlite), or a schema of a Postgres database (`postgres`,
`openEngineAsync`'s, "Several instances on one Postgres database").
Opening a SQLite file sets a busy timeout (`busyTimeoutMs`, default 5000),
write-ahead logging and foreign keys. A write runs in a transaction that
starts with `BEGIN IMMEDIATE`; a call inside another is a savepoint that
rolls back alone; a lock held past the busy timeout is the refusal `busy`.

`engine.storage` is the SQLite file as a test or a deployment reaches it:
raw SQL on the engine's own connection (`exec`, `run`, `get`, `all`), rows
as plain objects, `undefined` for a missing row, parameters strings,
finite numbers, bigints, `Uint8Array`s and null, a failure a `SqliteError`
with SQLite's extended result code, and `transaction(fn)`, inside which
the engine's calls run in savepoints of it. A store that runs its
transactions statement by statement, as the identity store does, takes a
connection of its own on the file (`connection()`). An engine on Postgres
has none (`UnsupportedError`, `storage`).

One process writes a SQLite file. SQLite queues writers from several
processes on the busy timeout, but the engine keeps per-process state
(the cache of each version's validator and behaviors, the namespaces)
that nothing coordinates across processes, and two processes on one file
share no notifier and no runner's lease.

The engine's tables, as its twelve migrations leave them:

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
  -- The cursor of the version's publish event (migration 12), where a
  -- subscription starts, after retention prunes the event; null for the
  -- draft, and for a version a version-1 file stored with no event.
  published_cursor INTEGER,
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
  -- The JSON pointers of the fields data holds a ref to the value store
  -- for, in place of their values (migration 9); null for none.
  value_refs       TEXT,
  UNIQUE (namespace, schema, id)
) STRICT;
CREATE INDEX engine_instances_list ON engine_instances (namespace, schema, position);
-- Per schema, an index for each @unique or @key field and each @index
-- entry of its live version ("Unique fields and indexes" under
-- "Instances"), which publish creates and drops (migration 10 created
-- the ones of the versions published before it), such as:
-- CREATE UNIQUE INDEX engine_unique_<digest> ON engine_instances
--   (namespace, json_extract(data, '$."slug"'))
--   WHERE schema = 'Model' AND schema_namespace = 'default'
--     AND json_extract(data, '$."slug"') IS NOT NULL;

-- The append-only event log. A trigger refuses an UPDATE, and another a
-- DELETE of an event after its namespace's floor (engine_log_floors).
CREATE TABLE engine_events (
  cursor      INTEGER PRIMARY KEY AUTOINCREMENT,  -- the global cursor
  kind        TEXT    NOT NULL CHECK (kind IN ('create', 'update', 'delete', 'operation', 'publish', 'define')),
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT,                               -- null for a publish and a define
  seq         INTEGER,                            -- per instance; null for a publish, a define and a beside operation's (migration 13)
  version     INTEGER,                            -- null for a define alone
  actor       TEXT    NOT NULL,
  at          INTEGER NOT NULL,
  change      TEXT,                               -- JSON: see "The event log"
  -- For an event the runner's work wrote (migration 6): the behavior, the
  -- event its reaction handled or the schedule that ran, and its depth.
  cause_behavior TEXT,
  cause_event    INTEGER,
  cause_schedule TEXT,
  depth          INTEGER NOT NULL DEFAULT 0,      -- 0 for a caller's change
  service        TEXT,                            -- the calling deployable (migration 7)
  -- The JSON pointers of the members of change that hold a ref to the
  -- value store (migration 9); null for none.
  value_refs     TEXT,
  CHECK ((kind IN ('publish', 'define')) = (instance_id IS NULL)),
  CHECK (instance_id IS NOT NULL OR seq IS NULL),
  CHECK (seq IS NOT NULL OR instance_id IS NULL OR kind = 'operation'),
  CHECK ((kind = 'define') = (version IS NULL))
) STRICT;
CREATE UNIQUE INDEX engine_events_instance ON engine_events (namespace, schema, instance_id, seq)
  WHERE instance_id IS NOT NULL;
-- One instance's events in log order, its beside events among them
-- (migration 13).
CREATE INDEX engine_events_instance_cursor ON engine_events (namespace, schema, instance_id, cursor)
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
  -- What it hears (migration 8): null for every change, 'delete', or a
  -- JSON pointer into the target's data, with the number it crosses.
  hears         TEXT,
  crosses       REAL,
  PRIMARY KEY (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
) STRICT;
CREATE INDEX engine_references_source ON engine_references (namespace, source_schema, source_id, behavior);
-- The references a change of a target moves, without the rest.
CREATE INDEX engine_references_hears ON engine_references (namespace, target_schema, target_id, hears, crosses);

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

-- The value store ("The value store"): each large value once, as its
-- canonical JSON, under its SHA-256 (migration 9).
CREATE TABLE engine_payloads (
  hash  TEXT    PRIMARY KEY,                      -- hex
  value TEXT    NOT NULL CHECK (json_valid(value)),
  bytes INTEGER NOT NULL                          -- the canonical JSON's UTF-8 length
) STRICT;

-- Who holds each value: an instance's row, an event, what retention kept
-- of an instance (base), or a row of a behavior's tables. A value goes
-- when its last holder does.
CREATE TABLE engine_payload_holders (
  hash      TEXT NOT NULL,
  namespace TEXT NOT NULL,
  schema    TEXT NOT NULL,
  holder    TEXT NOT NULL,                        -- instance, event, base, or the behavior's name
  id        TEXT NOT NULL,                        -- the instance id; '' for none
  key       TEXT NOT NULL,                        -- '', the event's cursor, or the behavior's key
  PRIMARY KEY (hash, namespace, schema, holder, id, key)
) STRICT;
CREATE INDEX engine_payload_holders_holder ON engine_payload_holders (namespace, schema, holder, id, key);

-- The namespaces a create made while an engine ran ("Namespaces"),
-- with who archived each and when while it is archived (migration 11).
CREATE TABLE engine_namespaces (
  name        TEXT    PRIMARY KEY,
  created_at  INTEGER NOT NULL,
  created_by  TEXT    NOT NULL,
  archived_at INTEGER,
  archived_by TEXT,
  CHECK ((archived_at IS NULL) = (archived_by IS NULL))
) STRICT;

-- How far retention has pruned each namespace's events ("Retention",
-- migration 12): every event of the namespace at or before floor is
-- gone; publish_floor is the last of its publish events pruned.
CREATE TABLE engine_log_floors (
  namespace     TEXT    PRIMARY KEY,
  floor         INTEGER NOT NULL,
  publish_floor INTEGER NOT NULL DEFAULT 0,
  pruned        INTEGER NOT NULL DEFAULT 0       -- events pruned so far
) STRICT;

-- What retention kept of each instance's pruned events: the instance as
-- the log had it after the last of them, null after a delete, and that
-- event's sequence (migration 12).
CREATE TABLE engine_event_bases (
  namespace   TEXT    NOT NULL,
  schema      TEXT    NOT NULL,
  instance_id TEXT    NOT NULL,
  seq         INTEGER NOT NULL,
  data        TEXT,
  value_refs  TEXT,
  PRIMARY KEY (namespace, schema, instance_id)
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

Migrations are forward-only and recorded per owner in the ledger
(`engine_migrations`): the engine's are owned by `engine`, and a
behavior's by its name (see "Behaviors"). Opening an engine applies the
versions the ledger lacks, numbered 1, 2, 3, ..., each in its own
transaction with its ledger row. A file whose ledger is ahead of the
build, or names a version differently, is refused. The Rust engine's
tests run its migrations from an empty file and from the file each
earlier version left (`rust/engine/tests/migrations.rs`), and the SQLite
corpus (`testdata/sqlite`) holds a file the TypeScript engine wrote,
which opens and migrates.

### Several instances on one Postgres database

The Rust engine (D58) runs in every instance of a server, each with a
pool of its own on the one schema, and holds the following across them.
The tests are `rust/testkit/tests/replicas_*.rs`, two engines on one
schema whose sessions the database's administrator ends as a crash, a
failover or a proxy would; `make engine-rust-soak` runs the racing writers
long (it needs `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL`).

- **Writers.** A write takes its namespace's advisory lock and the
  catalog's shared, so a namespace's writers take turns: racing writes
  lose no update and repeat no seq, cursor or number, an id and a unique
  field hold, Budget never passes a limit and Queue never claims a job
  twice. Writers of two namespaces never wait for each other. A define, a
  publish, a change of namespaces and a migration take the catalog's lock
  alone, and every writer waits for them.
- **The event log.** A reader on any instance sees every event of a
  namespace once, in cursor order. A write that rolls back leaves a gap in
  the identity, so cursors are ordered, not contiguous. A read from
  `head` reads the head once the namespace's writes in flight are done
  (it waits for them, `busy` past the busy timeout): the head is global,
  and a write held open could hold a cursor below it. `Events::head()`
  is the log's last cursor, not a place to read on from.
- **Waking.** A commit notifies (`pg_notify`) and each instance's listener
  wakes its waiters, event streams and runner. A listener whose session
  ends reconnects at once (while it cannot, it tries again after 50 ms,
  backing off to 5 s) and wakes them, so they read what they missed. An instance opened with `listen: false` has
  its streams read at each heartbeat instead, and `watch` at its timeout.
- **Signals.** A signal travels on a `pg_notify` channel of the engine
  schema's own, apart from the commits' (D77, "Signals"), which the same
  listener hears and hands to its process's streams, so a signal one
  instance sends reaches a stream another serves, at most once: one sent
  while a listener reconnects is lost to it. It wakes a stream without a
  read of the log. `replicas_signals.rs` holds it.
- **The runner.** One instance's runner runs at a time: the one whose
  process holds the runner's lease, a session advisory lock on a
  connection of its own. Another takes the lease, within its
  `leaseRetryMs`, once the first stops or its session ends; `run_due`
  elsewhere is `busy`. When a lease's session ends under a pass, the two
  passes can overlap for a moment; a reaction's cursor and a schedule's
  next run are read again under the namespace's lock, so a reaction runs
  once per event, a schedule once per due time, and Timers fires each
  deadline once.
- **Opening and publishing.** Instances that start together migrate once,
  under a lock of the session; one that starts on a storage already up to
  date only reads its ledger and takes no lock, so it holds no running
  instance's writes up. Publishes of one draft at once mint one version:
  the others answer `published: false` (the draft matched the version)
  or `not_found` (no draft left). A publish creates its behaviors' storage
  in its transaction, so a write beside it is wholly of the old version or
  wholly of the new; an instance whose build lacks a behavior the new
  version composes answers that version's writes `unavailable`, writing
  nothing.
- **Users.** Instances may open the schema each as a database user of its
  own, as the deployables of a stack do on Cloud SQL. The engine creates
  its schema only when it is missing, so a user without `CREATE` on the
  database opens one an administrator made, and it acts as the schema's
  owner: when that is another role the session's user is a member of,
  every connection takes the role (`SET ROLE`, a startup option after the
  first), so every table, index and text search configuration any
  instance creates is the owner's, and every member may change it at a
  later migration or publish. A deploy's migration job makes the schema
  such a role's and grants the role to each deployable that opens the
  engine (D58); a member whose grant does not let it `SET ROLE` (Postgres
  16's `WITH SET FALSE`) is refused at open, saying so.
  `fresh_database::members_of_the_role_that_owns_the_schema_open_it_as_that_role`
  opens one schema as two such users.
- **Failures.** A lock waited for past the busy timeout, a statement the
  server's `statement_timeout` cancels, a deadlock and a serialization
  failure are `busy`: nothing was written, and over HTTP the 503 says
  `Retry-After: 1`. A session the server ends inside a call (a failover,
  `pg_terminate_backend`) answers `unavailable`, nothing written, and its
  locks are free at once. Pooled connections the server ended while idle
  are found closed at BEGIN and replaced, so no call fails on them. A
  connection lost while a call commits answers `unavailable` saying
  whether it was written is unknown: read before trying again. The server
  ends a session that sits idle inside a transaction past
  `idle_in_transaction_session_timeout`, 60 s unless the options say
  (`idleInTransactionTimeoutMs`, 0 for the server's own), so an instance
  frozen mid-transaction holds its namespace no longer. Only a panic
  inside a transaction poisons an engine, and that one alone.

Not guaranteed: a connection the network cuts without a reset blocks its
call until the operating system's TCP timeouts give up, and a listener
cut off so wakes nothing until then (streams read at their heartbeat, the
runner at its timer, a minute at most); a lease holder cut off so keeps
the lease until the server drops its session, while its own runner stops
at its next check of the lease, so for that while no runner runs; and two
processes on one SQLite file share no notifier and no lease (one process
writes a file).

## Schemas

A schema is one JSON schema-file document, the form `superschematic
format --to=json` writes. The strict schema-file loader (the schema
runtime's Rust crate, `runtime/schema/rust-ir`, held to the Go and
TypeScript loaders by `runtime/schema/testdata/schema_file_parity.json`)
reads it against a meta-schema, the
`metaSchema` option: the `superschematic json-schema` output of the
deployment's binary, the core's by default. The engine then requires:

- `kind: General`, and a `name` of a letter followed by letters, digits,
  `_` and `-`, at most 128 characters;
- no `imports` (a type in another schema is reached through a link
  behavior) and no `operationSets` (every schema has `create`, `get`,
  `list`, `update` and `delete`, and behaviors add their own);
- an instance type: the type named like the schema, or its only type;
- field types the schema runtime validates, in the instance type and the
  types it reaches, and in the types a behavior checks values against or
  reads through `ConfigTarget.types` ("Other types" under "Behaviors"): a
  builtin primitive, a scalar, or an enum or type of the document, alone,
  as a list or as a list of lists. A union or a map is refused, since the
  runtime checks neither;
- behaviors on the instance type that this engine has implementations
  for, composed as the compiler's loader requires (see "Behaviors");
- `@unique`, `@key` and `@index` (a type's `indexes`) only on the
  instance type's own fields whose value is a string, a number or a
  boolean, an enum's included, and an index only over fields the type
  has ("Unique fields and indexes" under "Instances");
- each type's display (`@display`, D48 in `docs/DECISIONS.md`) held to
  the type, as the compiler's loader holds it, with its wording, at
  `/types/<Type>/display`: `titleField` names one of the type's own
  fields, by its name or its JSON key, that holds a single text value (a
  string, or a scalar whose values are strings, not a list or a map) and
  is neither `secret` nor `uiHidden`; each `summaryFields` entry names
  one of the type's own fields, or a field one of its behaviors adds,
  shown to a reader the same way; and `states` and `transitions` name
  states and transitions of the type's `Workflow` config, so a type that
  does not compose `Workflow`, a nested type among them, takes neither.
  The meta-schema holds the display's shape: at least one member, state
  names, no blank text, and the tones `muted`, `active`, `success`,
  `warning` and `danger`. A new version may change a display freely: the
  compatibility rule ignores it.

A refused document throws `SchemaDocumentError` with an issue per problem,
each at a JSON pointer.

`define` stores the document as its name's draft in a namespace,
replacing the previous draft, and appends a `define` event with the
draft's hash ("The event log"). `publish` makes the draft the next live
version and appends a `publish` event. The engine stores the canonical
form the loader returns (compact, keys sorted) and its SHA-256;
publishing a draft identical to the live version mints nothing and drops
the draft. There is no deprecate or promote: the live version changes
only when a newer one is published, so an older version never comes back
into use. `live`, `draft`, `version` and `list` read them. Each version
records who defined and who published it.

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
| any change to a type, enum or scalar no field, no behavior's `checkedTypes` and no type a behavior read through `ConfigTarget.types` reaches | a scalar that accepts less: a primitive or JSON type change, a narrower bound, a new or changed pattern, new reserved words, a new custom validator |
| | another instance type |
| a behavior's config changed as its implementation allows | any other config change |
| a behavior added or removed while the schema has no instances, or with its implementation's consent | a behavior added or removed while it has instances, otherwise |
| `@unique`, `@key` or a unique `@index` added while no two instances of a namespace hold its values | the same while two do |
| `@unique`, `@key` or an `@index` removed, a plain `@index` added | |

The walk starts at the instance type, at each type a behavior's
`validate` checks values against under both versions' configs
(`checkedTypes`, "Validating fields" under "Behaviors"), and at each type
a behavior's `parseConfig` read through `ConfigTarget.types` under the
live version, whether or not the new version reads it ("Other types"
under "Behaviors"): a stored instance, or a behavior's own tables, hold
values the live version checked against it, a `Variants` result or a
row of a kind say, so such a type is held as a type a field reaches. A
type only the new version reads held no value yet and changes freely. A
refused version throws `IncompatibleChangeError`, whose `changes` name
each change and whose message says to use a new schema name.

A unique field is the one row the documents cannot decide: it narrows
what the schema accepts across instances, not what one instance holds.
`define` and `publish` ask the stored instances instead, and refuse a
unique index the live version lacks while instances of a namespace share
its values, naming them where the namespace that holds the schema holds
the instances; the message says to make the values distinct. `publish`
asks again in its transaction, since the instances may change after the
define, and creates the index there.

### Validation

The validator of a version is built once, with `parseSchemaIR` from the
stored document, and cached per namespace, name and version. The schema
runtime checks each field, including that a list field holds a list and
an object-typed field or list element holds an object (`type`). The
engine adds two checks the runtime does not make, which the
compatibility rule relies on: the value is JSON (plain objects and
arrays, strings, finite numbers, booleans, null; a member whose value is
`undefined` is absent), and an object holds no key its type does not
declare, at any depth. A field a behavior adds is not one of `data`'s:
a read returns it apart, under the behavior's name ("Instances and
events" under "Behaviors"), so its name in an instance is an undeclared
key as any other. A scalar
the builtin catalog holds validates by the catalog's row, whatever the
document declares for it, as the Go loader reads it: the form `format
--to=json` writes declares each catalog scalar a field uses, by its name
and language primitive. `validate` returns `{ path, rule, message }`
issues, with paths such as `lines[2].sku`, and `validator` the cached
validator itself. Both are the version's own rules: a behavior's
`validate`, which judges a write ("Validating fields" under
"Behaviors"), is not asked.

A write stores what the schema's parse makes of it (D16, amended).
Before the version validates them, the own fields a write gives go
through the schema runtime's strict parse of the instance type
(`parseType`), the step a generated server runs on a request: each
scalar value takes the canonical form its scalar's normalize and parse
steps give it, at every depth, and each absent field that declares a
default takes it. So `Contact.Email` stores `ada@example.com` for
`Ada@Example.COM`, `Design.Color` stores `#FF0000FF` for `red`,
`Identity.UUID` its base62 form, `Temporal.DateTime` its RFC 3339 text
with the sub-seconds trimmed, and `Generic.StringMap` the object for its
JSON text.

- **Where.** A create, an update, a behavior's `update()` and
  `instances.create`, and `validateUpdate()`, which reports what
  `update()` would. `schemas.validate` answers as a create would.
- **Defaults on create only.** A create fills each absent field's
  default, in nested objects and list elements too; the text reads as
  D14's loader reads it: the text for a string, an enum or a string
  scalar, a number or a boolean for those types (a list's default is
  not filled, as the runtimes' parse fills none). An update's merge
  patch fills none, so a field a patch removes with `null` stays
  removed, and a field a later version adds stays absent on the
  instances before it.
- **What the parse cannot read** is left as given for the version to
  refuse with its own rule: a value of the wrong type (`type`), a key no
  type declares, a behavior's field's name among them (`unknown`). A
  value its scalar's parser refuses where the version's rules let it
  through is refused with the parser's rule (`parse`), a
  `Temporal.DateTime` of the 30th of February say.
- **What a client sees is what is stored.** The create's and the
  update's answer, every later read, the event's change (an update's
  patch with its values as stored) and the value store's hash of a large
  field are the normalized form. An update whose patch normalizes to
  what the instance holds changes nothing and writes nothing. A unique
  index compares the stored form, and a `lookup` key's and a list's
  `where` values on own fields are normalized before they compare.
- **Rows written before.** An instance written before the engine
  normalized keeps its values as given until a write sets them again; a
  unique field's old value does not collide with its normalized form,
  and a lookup by the normalized form does not find it.

## Instances

`engine.instances` stores instances of a schema's instance type. An
instance is keyed by namespace, schema name and id, so an id in one
namespace reveals nothing about another. Ids come from the `ids` option,
random UUIDs by default, or from the caller; an id is a letter or digit
followed by letters, digits, `.`, `_`, `:` and `-`, at most 256
characters. A schema a namespace reaches through the shared namespace
holds instances in the namespace that creates them.

- `create` stores the instance as the schema's parse makes it, each
  scalar value in its canonical form and each absent field's default
  filled ("Validation" under "Schemas"), validates it against the
  schema's live version, then asks its behaviors' `validate`, and
  refuses an id the namespace
  already has for the schema, and a unique field's value another
  instance holds (`conflict`).
  `behaviors` gives the type's behaviors their parameters, by behavior
  name, such as the links and blockers the instance holds from its
  create ("Create parameters" under "Behaviors"). Every behavior's guard
  may veto it, and each behavior initializes its state for it.
- `get` returns the instance, or `undefined`.
- `lookup` returns the instance whose unique fields hold a key's values,
  `{ slug: 'openai/gpt-5' }`, or `undefined` (below).
- `list` returns a page in creation order: `{ items, next }`, where `next`
  is an opaque cursor, null after the last page. A page holds 50 instances
  by default and at most 500 (`limit`). Each page is one indexed range
  read, so a list never loads the whole table, and an instance created or
  deleted while a client pages moves no other instance between pages.
  `where` keeps the instances whose fields hold the values it names
  ("Filters", below).
- `get` and `list` take `valueRefs: true`: each own field the value store
  holds is then its ref, and the record lists the pointers to them in
  `valueRefs` ("The value store"), so a client that pages through
  instances for their small fields does not receive their large ones.
- `update` takes a JSON merge patch (RFC 7386), the engine's update rule: a
  member replaces the instance's member, a nested object merges, `null`
  removes the member, and a list or any other value replaces what was
  there. The patch's values are normalized as a create's are, with no
  default filled, and the result is validated against the live version,
  then by the behaviors' `validate`; a patch that changes nothing, once
  normalized, writes nothing. The
  behaviors' guards may veto it, and a unique field's value another
  instance holds refuses it (`conflict`).
- `delete` removes the instance and returns whether there was one. The
  behaviors' guards may veto it.
- `update`, `delete` and `invoke` take `expectedSeq`, the sequence the
  caller last read. Inside the write transaction the engine refuses the
  call (`seq_mismatch`) unless the instance is still at it; an instance
  that does not exist is still `not_found` for `update` and `invoke` and
  `false` for `delete`. This is optimistic concurrency, the HTTP API's
  `If-Match`.
- They also take `preconditions`, entries by behavior name such as
  `{ Lease: { token: 7 } }`, which each behavior's guard checks ("Vetoes
  and preconditions" under "Behaviors"), the HTTP API's `Preconditions`
  header.
- `invoke` calls a behavior operation on the instance (see "Behaviors")
  and returns its result; `operate` returns `{ result, seq }`, the result
  and the instance's sequence after the call.

An instance's `data` holds its own fields as stored, then each field its
behaviors add that has a value. The row stores only its own fields; the
behaviors' are read from their storage. A field whose JSON is longer than
the value store's threshold, 64 KiB by default, is stored once by hash,
and the row keeps a ref in its place; every read puts the value back
("The value store"). Its `seq` is the sequence of its
last event, so every write that appends one moves it: a create, an update
that changes something and a writing behavior operation, unless its
handler says it changed nothing ("Instances and events" under
"Behaviors").

Every operation on a schema with no live version is `not_found`. A row
records the schema version it was last written with; the compatibility
rule keeps it valid under every later version, so reads return it as
stored. In an archived namespace a create, an update, a delete and a
writing operation are `namespace_archived` once the policy has allowed
them, and reads go on ("Namespaces").

### Unique fields and indexes

`@unique` and `@key` on a field of the instance type, and each entry of
the type's `indexes` (`@index<T>(keys, { unique? })`), are indexes the
engine keeps on `engine_instances`: led by the namespace, then the value
of each field, `json_extract(data, '$."<key>"')`, partial on the schema's
rows. Each set of fields is one index, a unique one winning over a plain
one on the same fields.

- A field is a top-level own field whose value is a string, a number or a
  boolean: a primitive, an enum, or a scalar of one of those JSON types.
  `define` refuses one on a list, an object or a `Generic.JSON`, on a
  field of any type but the instance type, and an index that names a
  field the type lacks.
- `@key` is a unique field; the instance's id stays its key.
- A unique index refuses a create or an update, a behavior's `update()`
  and `instances.create` included, that would give a second instance of
  the namespace the same values: `UniqueConflictError`, code `conflict`,
  whose `fields` are the index's keys. It does not name the instance that
  holds them. A unique `@index` makes its fields unique together.
- Absent and null are no value: an instance that lacks a field of an
  index is not in it, so any number of instances lack a unique field.
- A value is unique within its namespace; a shared schema holds each
  namespace to its own.
- An indexed field stays inline in the instance's row whatever its
  length ("The value store").
- `publish` creates the indexes a version adds and drops the ones it
  removes; a new unique index the stored instances break refuses it
  ("The compatibility rule"). Engine migration 10 created the indexes of
  the versions published before it, and refuses to open a file whose
  instances break one.

`lookup(principal, schema, key, { namespace, valueRefs })` asks `read`
and returns the instance whose fields hold `key`'s values, through the
index, each value normalized as a write stores it first. `key` names exactly the fields of one unique index, each with one
value of its type: `{ slug: 'openai/gpt-5' }`, or `{ source: 'crm',
externalId: 42 }` for a unique `@index` on both. Any other key, a value
of another type, null, and a schema with no unique field are
`invalid_argument`. Over HTTP the key travels as a query parameter, never
in a path segment, so a value may hold `/`.

### Filters

`list`'s `where` is a JSON object of field values, each on an own field
normalized as a write stores it: a member keeps the instances whose
field holds its value, `null` the ones whose field holds none (absent or
null), a list of 1 to 100 values any of them (`MAX_FILTER_VALUES`), and
the members together the instances every member keeps. `{}` filters
nothing. `{ "Assignment.assignee": [null, "wren"] }` is the work wren
holds and the work no one holds.

- A field is a top-level own field whose value is a string, a number or
  a boolean, as for an index, by its key, or one a behavior lets a list
  filter on (its implementation's `filters`, "The implementation" under
  "Behaviors"), by its qualified name, the behavior's name, a dot and
  the filter's: `Workflow.status`, `Numbering.seq` and `Numbering.label`,
  and in the work-queue package `Assignment.assignee`, `Lease.holder` and
  `Retries.exhausted`. An own
  field's key holds no dot, so the two never meet. A
  value has the field's JSON type, an integer for an integer scalar, or
  is null; another type, an empty list and a field that is not one of
  these are `invalid_argument`, the message listing the fields.
- A behavior's field computed from other instances is not filtered on:
  a column copied from them would have to hear every change of each, the
  cost "What a reference hears" removes, and would go stale when a
  config changes with no write. `Dependencies`' `blocked` is one.
- A page keeps creation order. It reads through an index whose every
  field a member names, unique first, then the one with the fewest
  combinations of values: each combination is one indexed range read
  past the cursor in position order, at most 100 of them
  (`MAX_FILTER_RANGES`), merged. A unique index answers a combination
  with one instance at most, and a behavior's index serves its filter
  (`Workflow`'s on `Workflow.status`, `Numbering`'s on each of its
  two), null being one more range. An own index
  holds no instance without a value in each of its fields, so it serves
  no member that lists null. With none, the page reads the list index,
  at most 1000 instances past the cursor (`FILTER_SCAN_ROWS`). The
  members no index serves are tested in SQL on the rows read.
- `next` is the position up to which every instance was read. So a page
  that no index serves, or that merges ranges, can hold fewer instances
  than its limit, even none, while `next` is not null: read on until it
  is, as with a filtered page of the event log. A reader that goes on
  from `next` misses no instance and reads none twice.
- A member on an own field no index covers also matches a string the row
  holds by hash, by its hash.

## The event log

Each write appends one event in its own transaction: an instance's
`create`, `update` and `delete`, a behavior `operation` that writes, a
schema's `publish` (a publish that mints nothing appends nothing), and
the `define` of a draft. An event carries its namespace (for a publish
or a define, the namespace that holds the schema), schema, instance id,
per-instance sequence, schema version, actor (the principal's subject),
the calling service (`service`, below), time and change: the instance
for a create, as a read returns it, `{ data, behaviors }`; for an
update, a merge patch of that, the caller's patch under `data` and any
change the behaviors' fields took under `behaviors`; for an operation,
`{ behavior, operation, params, patch, attribution? }`, where `patch` is the same
merge patch of the instance's own fields the operation changed (an
operation's `update()`, under "Contexts") and of its behaviors' fields,
a part absent when it changes nothing, and `attribution`, by own field
the operation changed, where its `update()`s said each value came from,
absent when they said nothing; nothing for a delete; the schema document for a publish; and `{ hash }`
for a define, the draft's hash and not its document, which the draft
route serves. A define has no instance, no sequence and no version
(`null`), since a draft has none; every define appends one, a draft that
matches the live version included, so a reviewer watching the log learns
a draft waits to be published. An operation beside the instance (D77,
"Instances and events" under "Behaviors") appends an `operation` event
of its instance with no sequence (`seq: null`) and `beside: true`, whose
change is `{ behavior, operation, params, result }`: the operation's
result in place of a patch, since it changes nothing a read of the
instance returns; no other event has `beside`. Applying each change but
a beside one in order to the create's instance gives the instance as a
read returns it. The cursor orders the whole log; an instance's sequence
runs 1, 2, 3, ... across its life, a re-create after a delete included,
and a beside event takes none. A delete never removes earlier events;
only retention does ("Retention").

`engine.events.read(principal, { namespace, schema, instanceId, after,
limit, kinds, behaviors, exclude, beside })` returns `{ events, next, more }`:
the events after the cursor `after` in one namespace, optionally one
schema and one instance, in cursor order (one instance's beside events
among the rest), 50 by default and at most 500 scanned per page.
Read on from `next`. Without a schema filter, events of schemas the
principal may not read are skipped, so a page can hold fewer events than
its limit while `more` is true. `engine.events.head()` is the cursor of
the last event, 0 for a log that never held one; retention never moves
it back. A read with no `after`, or `after: 0`, starts at the start of
the log, which after retention is the namespace's floor; one from any
other cursor before the floor, where retention has pruned events after
it, is `cursor_expired` ("Retention").

- `after: 'head'` starts at the head: the page is empty, `next` is the
  head and `more` is false, so a client that wants only new events reads
  on from there without replaying the log. It asks what any read asks.
- `kinds` keeps events of those kinds; `behaviors` keeps operation events
  of those behaviors (`change.behavior`), and so no event of another
  kind; `exclude` drops operation events by operation name
  (`change.operation`), such as `heartbeat`. They combine: an event is
  kept when every filter given keeps it. A name that is not an event
  kind, a behavior name or an operation name, and an empty `kinds` or
  `behaviors`, which would keep nothing, are `invalid_argument`.
- `beside: false` drops the events of operations beside their instance,
  for a consumer that rebuilds instances from the log, and `beside: true`
  keeps only them; a value that is not a boolean is `invalid_argument`.
  It combines with the others as they do.
- The filters run on the page a read scans, as the access check does: a
  page still scans at most its limit, and `next` is past every event it
  scanned, kept or dropped. A reader that goes on from `next` sees no
  dropped event again and scans none again, and a page whose events were
  all dropped is empty with `more` true.

A define is read with `read` on its schema, as every event is. The
draft route already shows the draft to a principal with `read`, and the
event carries less: its hash.

An event a service's call wrote (D37, "Service callers" under "Access")
carries `service`, the calling deployable, beside `actor`: the end user
the service acted for, or the service's own subject when it stood in for
one. An event no service's call wrote has no `service`.

A namespace that looks names up in a shared namespace also reads the
shared namespace's publish events, which change the schemas it reaches;
they keep the shared namespace as their `namespace`. Its own instance
events and the shared publishes are two indexed range reads merged by
cursor. It does not read the shared namespace's defines: a draft changes
nothing a namespace reaches until it is published, and only the
namespace that holds a draft can publish it.

An instance event keeps a large member of its change in the value store
("The value store"): an own field or a behavior's field of a create's
instance or an update's patch, or a member of an operation's `params` or
a field of its `patch`, whose JSON is longer than the threshold holds a
ref, `{ "$value": <hash>, "bytes": <n> }`, and the event lists the JSON
pointers to them in `valueRefs` (`["/data/result"]`,
`["/params/result", "/patch/data/result"]`,
`["/behaviors/test.Shelf/seen"]`); a beside operation's event keeps a
long member of its `params` and a long `result` so (`["/params/body",
"/result"]`). A read returns the event as the
log keeps it, refs and all, and so does the stream; a client reads a
value by its hash. A reaction gets the event the same way, and its
`before()` puts the values back. An event written before migration 9
keeps its values inline and has no `valueRefs`.

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

Without `retention` the log grows for good, and so does what its events
hold in the value store; with it, the runner prunes the oldest events
("Retention").

## Signals

A signal is a message about one instance that the engine delivers to the
event streams open on it and keeps nowhere (D77): who is typing, who is
looking, the next chunk of a reply being written. It appends no event,
writes no row and moves nothing a read returns; a reader that misses one
has missed it. A behavior declares its signals ("Behaviors"), each with
the JSON Schema of its data and who may send it, its `access`: `read`, a
caller the policy lets read the schema; `write`, one it lets write it; or
`operation`, the behavior's own operations alone.

```ts
engine.signals.send(bob, { schema: 'Ticket', instanceId: 't1', behavior: 'Comments', name: 'typing', data: { thread: 4 } });
// on a stream opened with signals=1:
// event: signal
// data: {"namespace":"default","schema":"Ticket","instanceId":"t1","behavior":"Comments","name":"typing","data":{"thread":4},"from":"bob","at":1790000000000}
```

- **A caller's.** `engine.signals.send(principal, { namespace?, schema,
  instanceId, behavior, name, data? })` (the call `signals.send`, also
  named `sendSignal`; `POST .../instances/{id}/signals/{behavior}/{name}`,
  204) checks, in order: that the schema's live version composes the
  behavior and the behavior declares the signal (`not_found`, to a
  principal that may read the schema; `forbidden` otherwise); that its
  access is `read` or `write` (`operation` is `forbidden` to a caller);
  the policy, asked that access with the operation `signal:<name>`
  ("Access"); the data, `{}` when absent, at most 4096 bytes of JSON its
  `paramsSchema` accepts (`invalid_argument`); that the instance exists
  (`not_found`); and the rate. It is sent once the read that checked it
  commits. An archived namespace takes signals: they write nothing.
- **The rate.** Each principal's subject may send on each instance
  `signals.perSecond` signals a second, its burst the same, a token
  bucket on the engine's clock; past it the send is `rate_limited`, a 429
  with `Retry-After: 1` over HTTP. The buckets are each process's own, so
  a server of three instances lets a caller through three times as fast.
- **An operation's.** A behavior's operation sends any signal its
  behavior declares, of any access, with its context's `signal(name,
  data)` ("Contexts"), beside the instance or not, writing or read-only,
  a reaction's or a schedule's invoke of it included, as the call's
  principal. Its data is checked as a caller's, a refusal a
  `BehaviorError` that rolls the call back. It is delivered once the
  call commits, in the order sent, and dropped when the call rolls back,
  or with the savepoint of a `call()` that rolls back. It is not limited,
  so a streamed reply's chunks go as fast as they are written.
- **What a signal is.** `{ namespace, schema, instanceId, behavior, name,
  data, from, at }`: `from` the sender's subject, `at` the engine's
  clock when it was sent. The whole, as JSON, is at most 7900 bytes, so a
  long subject or instance id with 4096 bytes of data is
  `invalid_argument` ("more than a signal carries"), the 8000 bytes of a
  Postgres notification being the bound.
- **Delivery.** A stream opened with signals ("The event stream" under
  "HTTP") receives each signal of its namespace that its `schema`,
  `instanceId` and `behavior` filters keep, on a schema its principal may
  read: it asks the policy `read` once per schema it meets between two
  reads of the log, so a policy that stops letting it read a schema
  stops its signals by the next read (at most a heartbeat on), and skips
  a schema it may not read. `kind`, `exclude` and `beside` do not apply to
  signals. Delivery is at most once and in no order with the events. A
  stream holds up to 256 for a slow client, the oldest dropped first,
  and sends what it holds once replay has caught up.
- **The carrier.** On SQLite the commit hands each signal to the
  process's streams. On Postgres the transaction sends it with
  `pg_notify` on a channel of its own beside the commit notifier's,
  `superschematic_engine_signals.<schema>` (shortened as a long name is),
  so each engine schema of a database has its own, and every process's
  listener hands it to that process's streams, the sender's included
  ("Several instances on one Postgres database"). An instance opened with
  `listen: false` delivers only its own signals, at the commit.
- **No MCP tool.** The MCP endpoint has no tool that sends a signal: a
  typing indicator is a client's, and an agent that writes a reply streams
  it through its behavior's operations.

| Option | Default | What it is |
| --- | --- | --- |
| `signals.perSecond` | 20 | the signals a principal may send on one instance each second, and its burst; a positive integer |

## The value store

A top-level member of what the engine writes whose JSON is longer than a
threshold is stored once (D16, amended: a large value is stored once): an
instance's own field in its row, a member of an event's change, or a
member of an object a behavior keeps in its own tables ("The value store
in a behavior's tables" under "Behaviors"). Its canonical JSON, RFC 8785
(JCS: compact, members sorted, strings and numbers as `JSON.stringify`
writes them), is stored under its SHA-256 in `engine_payloads`, in the
engine's file and the write's transaction, and the row keeps a ref in its
place:

```json
{ "title": "Check", "kind": "verify", "result": { "$value": "9f2c...", "bytes": 187334 } }
```

Beside it the row keeps the JSON pointers of the members that hold a ref
(`value_refs`, `["/result"]`), so a value that looks like a ref is never
taken for one. One value written twice, by two instances, by an instance
and its events and revisions, or in two namespaces, is stored once.

- The threshold is the engine option `values.thresholdBytes`, 65536 by
  default and at least 1024, measured in UTF-8 bytes; it is the
  engine's, not a schema's.
- Reads put the values back: `get`, `list`, a behavior's view and context
  (`data`), its reads of other instances, the field readers, the live
  version's validation and the behaviors' `validate` (`Variants`),
  `Search`'s index, `afterConfigChange`'s `eachInstance`, `before()` and
  `Revisions`' revisions and proposals. A value reads back in canonical
  form, its objects' members sorted. The API's shapes and the fields'
  types do not change.
- A ref is exposed where it is stored and a read asks for it: in an event,
  and in an instance read with `valueRefs: true`, each with `valueRefs`,
  the pointers to the refs. `$value` is the value's hash, `bytes` the
  length of its canonical JSON.
- The relation `sql.instances()` reads the rows as stored: a field the
  store holds is its ref in `data`. Its statements filter and order on
  small fields and the behavior's columns.
- A write reuses the ref of each member it leaves alone, so it neither
  hashes nor writes that value again, and an operation reads the own
  fields only when a behavior does: a lease's heartbeat, which writes
  Lease's columns, rewrites a row of refs and loads no large value. The
  engine keeps the values it read last in memory, deep-frozen and shared
  by its own reads (`values.cacheBytes`, 32 MiB by default, 0 for none);
  what goes to a caller is a copy.
- A row or an event written before migration 9 keeps its values inline.
  An instance's row moves a large field to the store at its next write;
  an event is never rewritten.
- A field an index of the instance type covers (a unique field, an
  `@index`'s) stays inline in the row whatever its length, so the index
  and a lookup compare the value itself; its events still hold it by
  hash. `publish` puts back inline a value a row holds by hash under a
  field a new index covers.

`engine.values.get(principal, hash, { namespace })`, the route `GET
/namespaces/{namespace}/values/{hash}` and the MCP tool `get_value`
return `{ hash, bytes, value }`. A caller reads a value only through a
schema of the namespace that references it: `engine_payload_holders`
records who holds each value (an instance's row, an event, a row of a
behavior's tables) by namespace and schema, and the access policy must
allow the caller `read` on one of those schemas. Knowing a hash grants
nothing: a value that no schema of the namespace references, one only
schemas the caller may not read reference, and one no namespace holds are
all `not_found`, so the answer does not say whether the namespace holds
it. A hash that is not 64 lowercase hex digits is `invalid_argument`.

A value goes when its last holder does, in the transaction that drops
it: an instance's row holds the values it holds now, an instance's
delete drops its row's, a behavior's `stow` holds and its `release`
drops, and an event holds its values until retention prunes it
("Retention"), when what retention keeps of the instance holds what the
instance held after the event instead. So a field an instance once held
stays while an event records it; a value only a behavior's rows held,
written by a schema-level operation or a schedule, which append no
event, goes with its last row.

Where the values live is a driver (`values.driver`, a `ValueDriver`):
`read(hash)`, `write(hash, json)`, `remove(hash)` and, optionally,
`list(after, limit)`, synchronous, as everything in a write is (D16). The
default, `SqliteValueDriver`, writes `engine_payloads` in the write's
transaction (`transactional: true`), and a rollback undoes its writes. A
driver over other storage is not transactional, and the engine settles
what it did around the file's transactions:

- it removes a value only after the commit that dropped its last holder,
  so a rolled-back write never loses one;
- once the outermost transaction ends, committed or rolled back, it
  removes each value the driver wrote in it that no holder references
  then, so a write that rolled back leaves no value behind;
- what a crash leaves between a driver's write and the end of its
  transaction, `engine.values.sweep()` removes: it pages through the
  driver's `list` and removes each value no holder references, and
  returns `{ removed }`. It acts for no principal, needs no open
  transaction and a driver with `list`; a deployment runs it at start,
  after a crash. The default driver has `list` and leaves nothing to
  sweep.

Such a driver must keep what `write` was given under its hash, return it
from `read` unchanged, take a `write` of a hash it has as a no-op and a
`remove` of one it lacks the same, and list its hashes in order if it is
to be swept. The holders stay in the engine's file whatever the driver.
No driver over object storage ships yet.

No value is longer than `values.maxBytes`, 16 MiB of canonical JSON by
default and at least the threshold. A write that would store a longer
top-level member, an instance's own field, a member of an event's change
or of an object a behavior stows, is refused as a whole with
`ValueTooLargeError` (`value_too_large`, 413 over HTTP), whose `path` is
the member's pointer in what would be stored (`/body`, `/params/doc`),
with its `bytes` and the `maxBytes`. The HTTP runtime's body limit bounds
what a request carries first; the engine's bounds what any write stores,
a behavior's included.

## Retention

Without retention the log keeps every event, and the value store every
large value an event holds. The engine option `retention` keeps events
by age, by count or both (D16, amended: the log keeps what retention and
its subscriptions need), and the runner prunes the rest:

```ts
const engine = openEngine({ path: 'shop.db', policy, runner: { principal }, retention: { maxAgeMs: 30 * 86_400_000 } });
engine.runner.start();          // prunes at its first pass, then every everyMs
engine.runner.prune();          // { pruned }: everything retention lets go, now
engine.events.floor('default'); // the earliest cursor a read of default may start from
```

| Option | Default | What it is |
| --- | --- | --- |
| `maxAgeMs` | none | an event older than this, by its `at` and the engine's clock, goes |
| `maxEvents` | none | a namespace's events past its newest this many go, so each namespace keeps about this many |
| `maxHoldMs` | none | the longest a subscription that does not advance (halted, in an archived namespace, or whose behavior the engine does not run) holds the log: it keeps no event older than this that age or count lets go; with it, a behavior the engine does not run holds from where it starts before it has run |
| `everyMs` | 60000 | how often the started runner prunes, at least 1000 |
| `batchSize` | 1000 | events pruned in one transaction, after which the runner yields, at most 10000 |

One of `maxAgeMs` and `maxEvents` is required; with both, an event goes
when either lets it go. `openEngine` refuses anything else (`TypeError`).
A namespace's count is its own: one busy namespace never pushes a quiet
one's events out. The runner counts a namespace's events once, at its
first pass, and after that only what was appended since, so a pass reads
what is new, not the whole log; it counts again when it takes the
runner's lease, and when the namespace's floor is not the one it counted
from, since another process's runner pruned meanwhile.

- **A namespace at a time, oldest first.** Retention prunes each
  namespace's events apart, from its oldest, and stops at the first
  event it keeps, so what is gone is always a run of cursors. A
  namespace's floor (`engine_log_floors`) is the last cursor pruned of
  its events: every event of the namespace at or before it is gone. It
  never moves back: a batch is written only from the floor it was chosen
  at, read again under the namespace's lock, so when two runners' passes
  overlap for a moment at a lease's handover, the one another overtook
  chooses its batch again rather than prune an event twice. A
  namespace no longer configured keeps its events until they are pruned
  too.
- **Never past an event a subscription has yet to handle.** A
  namespace's hold is the cursor of its least advanced subscription
  ("Subscriptions" under "The runner"): one whose schema's live version
  composes a behavior with reactions, at its cursor, or where it starts
  before it has run, whether it is active, retrying, halted or in an
  archived namespace, but not one whose `watches` turns its reactions off
  (`Lease` without `directOn`), which starts at the publish that turns
  them on; and one whose behavior is not registered, once it has run,
  or, with `maxHoldMs`, from where it starts before it has run (a
  behavior a newer release composes, while an older release's runner
  holds the lease). Retention prunes none of the namespace's events after it, and
  holds no other namespace's. So a halted subscription holds its
  namespace's log, and `status()` shows by whom, in what state, and since
  when ("Status" under "The runner"), until it is resumed.
- **A hold that does not move is bounded by `maxHoldMs`.** A subscription
  that does not advance, halted, in an archived namespace, or whose
  behavior the engine no longer runs, holds no event older than
  `maxHoldMs` once age or count lets it go; one that advances, active or
  retrying, holds whatever its age, since it is working through it. Past
  the bound retention prunes on, and the subscription, behind its floor,
  halts with `cursor_expired` when it next runs, after its `resume` or
  its namespace's unarchive; `resume(key, { skip: true })` moves it to the
  floor. `status()` shows when that happens (`heldUntil`). Without
  `maxHoldMs` such a hold lasts until the subscription advances.
- **A read from a cursor before the floor is `cursor_expired`.**
  `events.read` from a cursor before the floor of its namespace, or
  before the last publish pruned of the shared namespace it reads,
  throws `CursorExpiredError` with `floor` and `head`; over HTTP it is
  410 with `details: { after, floor, head }`, a stream's resume included
  ("The event stream" under "HTTP"). A client that can take the gap
  reads on from `floor`; one that cannot starts again from `head`. The
  head stays the last cursor the log gave, and `after=head` works as
  before.
- **A read from the start reads what is kept.** A read with no cursor,
  or with 0, which no event has, asks for the log from its start, and
  starts at the floor: it is never `cursor_expired`, so a client that
  reads the log from the start goes on working once retention has run.
  The one cursor 0 a client can hold as a place is the head of a log
  that never held an event, from which a reconnect after retention has
  pruned what it missed reads on from the floor without a word.
- **A reaction reads on as before.** Each pruned instance event folds
  into its instance's base (`engine_event_bases`): the instance as the
  log had it after the event, behaviors' fields included, or none after
  a delete, and the event's sequence; a beside operation's event (D77)
  changes nothing a base holds and folds into none. `before()` folds
  from the base and the events still there, so it answers for every
  event a subscription has yet to handle; for an event retention has
  pruned it is `cursor_expired`. A create after a delete takes the sequence after the
  base's, so an instance's sequences never repeat and an old `If-Match`
  never matches a new life.
- **Values go with their events.** A pruned event drops its value
  holders, and a base holds what it keeps, so a value only pruned events
  held goes in the prune's transaction ("The value store").
- **A subscription behind its floor halts.** Where a subscription's
  cursor is before its namespace's floor all the same, an implementation
  registered with reactions only after retention pruned past where it
  starts, the runner halts it with `cursor_expired`, failure cursor the
  floor; `resume(key, { skip: true })` moves it there, past what was
  pruned.

Pruning appends no event, acts for no principal and changes nothing a
read of an instance, a schema or a behavior returns, as a schedule's
writes to its own tables may not (D32). It runs on the started runner,
or when the deployment calls `engine.runner.prune()`, which needs no
runner principal. `runDue()` prunes when retention is due and adds
`pruned` to what it returns; `status()` adds `retention` ("Status"). A
reader that names a cursor learns the log was pruned past it from
`cursor_expired`, never from a page with a gap; one that reads from the
start reads what is kept.

## The runner

`engine.runner` runs the work behaviors do after a change commits (D16,
amended): reactions to the events of the log, and schedules on an
interval ("Reactions and schedules" under "Behaviors"). One runs per
engine, in its process, as one principal the deployment names; a
reaction never refuses the change that set it off, and is not limited by
the permissions of whoever made it. With `retention` it also prunes the
event log ("Retention").

```ts
const engine = openEngine({ path: 'shop.db', policy, runner: { principal: { subject: 'runner', permissions: ['projects.close'] } } });
engine.runner.start();                                    // runs what is due, then wakes on each commit
engine.runner.status();                                   // { running, principal, head, subscriptions, schedules, retention?, error }
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
  included, and returns `{ handled, skipped, failed, scheduled }`, with
  `pruned` beside them on an engine with retention. It runs started or
  not, so a test or a deployment that drives the runner itself calls it;
  it refuses inside a transaction and inside a reaction.
- `prune()` runs retention to the end now and returns `{ pruned }`. It
  acts for no principal, so it needs none; it refuses on an engine
  without retention (`TypeError`).
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
and the events between belong to no subscription. A `watches` that
returns `null` for the schema's config turns the reactions off there, as
a schedule's `everyMs` function does a schedule: the runner hands them no
event, shows a subscription that has run as `off` (one that never ran is
not listed), and a version whose config turns them on starts the
subscription at its publish, so `react` never sees an event from while
they were off. `Lease` turns its reactions off when its config has no
`directOn`.

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

In an archived namespace ("Namespaces") a subscription runs nothing, and
shows as `archived`, until the namespace is unarchived. Nothing writes
there meanwhile, so it picks up at its cursor. A subscription holds its
namespace's retention at its cursor in every state ("Retention").

### Schedules

A schedule is one behavior's named timed work on one schema that
composes it, in one namespace, with the time of its last run and its
next in `engine_schedules`. The runner finds it at its first pass and
runs it an interval later; a run and the next run's time commit in one
transaction. Missed ticks are not replayed: a schedule that came due
while the runner was stopped runs once when it starts, then an interval
after, and its context's `previous` is when its last run committed. A
run that throws rolls back, its writes with it, and is retried with the
backoff, never later than its next tick, and a schedule never halts: the
next run redoes the work a sweep missed, and there is no order to keep.

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

A function that returns `null` turns the schedule off on that schema: a
config without a sweep, say, runs no sweep (D32). Only `null` does; a
function that returns nothing (`undefined`) is a failing interval as
above, so a forgotten `return` is not mistaken for off. An off schedule
runs nothing there and never wakes the runner, since it sets no timer,
and shows in the status as `off`; the runner drops the row it kept for it
on the schema, so once
a publish gives a config the function returns an interval for, the
runner finds the schedule as for the first time: it runs an interval
later, with no `previous`.

### Status

`status()` lists every subscription and schedule:

| | |
| --- | --- |
| `running`, `principal`, `head` | whether it is started, the principal's subject, and the log's last cursor, which retention never moves back |
| `subscriptions` | `{ behavior, namespace, schema, state, cursor, attempts, retryAt, failure, skipped, lastSkip }`: `state` is `active`, `retrying` (its next event failed; `retryAt` says when it tries again), `halted` (after `maxAttempts` failures, or behind its namespace's floor), `off` (its `watches` returns `null` for the schema's config; listed once it has run), `archived` (its namespace is) or `inactive`; `failure` is `{ cursor, at, error }` until an attempt succeeds, with `cursor` null for a failure of `watches`; `lastSkip` is `{ cursor, reason }`, `depth` or `resume` |
| `schedules` | `{ behavior, schedule, namespace, schema, state, everyMs, previous, next, failures, error }`: `state` is `active`, `retrying`, `off` (its function returns `null` for the schema's config), `archived` (its namespace is) or `inactive`; `everyMs` is null when the schedule's function gives no interval on the schema; an `off` schedule has no `previous` and its `next` is null |
| `retention` | on an engine with retention: `{ maxAgeMs, maxEvents, maxHoldMs, everyMs, previous, next, namespaces }`, when the runner last finished pruning and prunes next (null before its first pass), and per namespace pruned or held `{ namespace, floor, pruned, heldAt, heldBy, heldState, heldSince, heldUntil }`: its floor, the events pruned of it, the cursor and subscription that hold it, that subscription's state (`active` or `retrying`, which advance; `halted`, `archived` or `inactive`, which do not), when the oldest event it keeps was appended, and, for one that does not advance under `maxHoldMs`, the time its hold lasts to, after which retention prunes past it; each null when none holds it |
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
A caller's signal (D77, "Signals") asks its declared access, `read` or
`write`, with the operation `signal:<name>`, so a policy that lets
readers talk lets `signal:typing` through for a caller who may read; a
stream with signals asks `read` of each schema it delivers one of, with
no operation.
A question about a namespace itself names no schema:
`{ principal, action: 'manage', namespace, operation }`, the operation
`create`, `archive`, `unarchive` or `list` ("Namespaces"). The type is a
union on `action` (`SchemaAccessRequest`, `NamespaceAccessRequest`), so
a policy that reads `schema` says what it does for `manage`; one that
builds a permission from the schema's name, as the examples' do, refuses
it.

The tools document asks a third kind while it lists a caller's tools
(`ListingAccessRequest`): of an engine tool that names its schema or
namespace only when called, whether the caller may do that at all here.
It is `{ principal, action: 'define', namespace, listing: true }` for
`define_schema`, and `{ principal, action: 'manage', namespace,
operation, listing: true }` for the namespace tools, the operation
`list`, `create`, `archive` or `unarchive`, and it names no schema. A
refusal hides the tool from the caller; a call of it asks the question
its call asks, with the name it gives. A policy that cannot answer
without a name answers false.

A behavior's namespace operation (D77, "Namespace operations" under
"Behaviors") asks a fourth kind, which names the behavior and no schema
(`BehaviorAccessRequest`): `{ principal, action, namespace, behavior,
operation }`, `action` `read` or `write` as its declaration says. The
tools document asks it with `listing: true` while it lists the
operation's tool. `behavior` tells it from a schema's question of the
same action, so a policy that reads `schema` checks `behavior` first. Its
rows are those of the schemas the principal may read, which the engine
asks `read` of itself, so a policy can let every read one through.

Only `true` allows, and anything else is
`forbidden`. It runs synchronously. There is no default
policy: `allowAll` is explicit, for tests and local use. The engine has
no roles of its own; a policy can hold the principal's `permissions` to
whatever rule the deployment has, through the HTTP runtime's
`PermissionMatcher` for example. `permissionPolicy()` is one such rule,
opt-in: it allows an action to a caller holding `<schema>.<action>`
(`Task.read`, `Task.write`, `Task.define`, `Task.publish`), by the
engine's `permissionMatcher`, so a caller holding `Task` may do all four.
It allows a namespace operation that reads to anyone, and one that writes
to a caller holding `<behavior>.<operation>` (`Comments.claimDeliveries`),
its listing question the same. It refuses the other questions that name
no schema: `manage`, which a
deployment's policy answers before it asks `permissionPolicy` the rest,
and a listing question, so the tools document hides `define_schema` and
the namespace tools from the caller, though a define, which names its
schema, asks `<schema>.define`. The Rust engine reads it as the policy
`"permission"`; a policy of rules (`{ "rules": [...] }`, the Rust
engine's `PolicyConfig`) matches a namespace operation's question with a
rule's `behavior`, `true` for any, `false` for none, or names, and fills
`{behavior}` in a permission it requires, as it fills `{schema}`, which
renders nothing in such a question. Only a rule that names `behavior`
matches such a question: a rule that leaves `behavior` out, as every
rule written before D77 does, matches schema, namespace and listing
questions alone. So a subject- or service-wide allow, `{ "service":
["shop-orders"], "action": ["read", "write"], "allow": true }`, lets its
principal read and write schemas and call no behavior's namespace
operation; with no rule naming `behavior`, the question falls past the
last rule and is refused. A deployment that wants one allowed says so:
`{ "behavior": ["Comments"], "action": "read", "allow": true }`, or
`{ "behavior": true, "action": "write", "require":
"{behavior}.{operation}" }` as `"permission"` does. `"allowAll"` still
allows them.
With the core user model ("Users" under "HTTP"), a user's permissions are
those of their roles. `permissionPolicy({ permissionMatcher })` matches
with the matcher it is given instead; called from a policy of the
deployment's own, it matches with that matcher or `hasAnyPermission`. A behavior's own checks, a transition only a reviewer may
make say, ask the `permissionMatcher` option (a `PermissionMatcher`, the
HTTP runtime's `hasAnyPermission` by default) through `can()` ("Contexts"
under "Behaviors"); a deployment passes the matcher it gives the HTTP
runtime, so one rule answers both.

`Principal` has the shape of the HTTP runtime's (`subject`,
`permissions`, `claims`), so a principal its `Authenticator` returns can
be passed on as it is. The main entry point does not import the HTTP
runtime, which brings Hono; the `./http` entry point does.

### Service callers

A call another deployable makes (D37) carries the calling service in the
principal's `service`: `{ deployable, serves, subject, standsIn }`, the
HTTP runtime's `ServiceCaller` and whether it stands in for an end user.
The policy sees it with the principal, and so do behaviors, through
their context's `principal`.

| The call brings | The principal | `can()` | An event's `actor` and `service` |
| --- | --- | --- | --- |
| an end user | the end user's | the end user's permissions | the end user's subject; none |
| a service and an end user | the end user's, with `service` (`standsIn: false`) | the end user's permissions | the end user's subject; the deployable |
| a service alone | `servicePrincipal(caller)`: `service:<deployable>`, no permissions, `service` (`standsIn: true`) | false, without asking the matcher | `service:<deployable>`; the deployable |

- A service with no end user stands in for one, as D37's `@allowService`
  admits it. Its subject is `service:<deployable>`, apart from every end
  user's, so a behavior that keys state by subject (a lease's holder, an
  assignee) keeps a service's apart, and every process of one deployable
  shares it.
- Services hold no permissions (D37). A principal that stands in has
  none, and `checkPrincipal` refuses one that has any; a behavior's
  `can()` is false for it whatever the matcher says. A service acting
  for an end user adds none to the end user's. What a service may do is
  the policy's to say, by `principal.service`: the engine has no grant
  of its own for services, as the deployment's `PermissionMatcher`
  matches end users' permissions.
- `standsIn(principal)` says which case a principal is. The runner keeps
  its own principal, which the deployment names.

## Errors

Every refusal is an `EngineError` with a `code` a server maps to a
status: `invalid_schema` (`SchemaDocumentError`, with its issues),
`incompatible_change` (`IncompatibleChangeError`, with its changes),
`invalid_instance` (`InstanceValidationError`, with its issues, the live
version's or a behavior's `validate`'s),
`name_taken`, `not_found`, `conflict`, `forbidden`, `unknown_namespace`,
`namespace_archived` (a write in an archived namespace), `cursor_expired`
(`CursorExpiredError`: a read of the log from a cursor retention has
pruned past, with its `floor` and the `head`), `value_too_large`
(`ValueTooLargeError`: a value longer than `values.maxBytes`, with its
`path`, `bytes` and the `maxBytes`),
`invalid_argument` (`OperationParamsError` for an operation's parameters,
`CreateParamsError` for a create's and `PreconditionsError` for a call's
preconditions, with their issues), `seq_mismatch`, `vetoed`
(`BehaviorVetoError`: a behavior refused the change, with the veto's
`vetoCode` and `vetoDetails` when it gives them), `unavailable` (the
live version composes a behavior this engine has no implementation for,
or whose implementation refuses its config) and `rate_limited` (a
caller's signals past `signals.perSecond` on one instance, "Signals"). A message names only what
the call named: `name_taken` in the shared namespace does not say which
namespace holds the name. "Statuses" under "HTTP" gives each code's
status.

An issue the engine checks with JSON Schema (ajv, draft 2020-12, every
error and no format checks) carries ajv's message: a behavior's config
against its `configSchema`, a create's parameters, a call's
preconditions, an operation's parameters and its result, and the
parameters of a search across schemas, as well as what the schema-file
loader's meta-schema refuses in a document. The engine does not keep
ajv's order, which follows ajv's compiled code: the issues of one check
come sorted by their JSON pointer, then by the keyword that failed, then
by message, each compared by Unicode code point (as UTF-8 bytes compare).
A composite keyword's issue (`must match "then" schema`) sits at the
shorter pointer, so before the issues inside it. Where a call checks
several behaviors' entries (a create's parameters, a call's
preconditions), each entry's issues keep that order and the entries come
in the order the call gives them, then, for a create, those of the
type's behaviors it gave none, in the type's order.

A defect in a behavior's code is a `BehaviorError`, not an `EngineError`,
so a server answers it as an internal error: a result its `resultSchema`
refuses, a field value that is not JSON, SQL outside its own storage, a
write from a read, a promise from a synchronous function.

## Behaviors

A behavior adds fields, operations, checks and storage to a schema's
instance type (D16 in `docs/DECISIONS.md`). The compiler declares it in a
JSON file (section 3.16 of `docs/extension-model.md`); the engine runs an
implementation of it that carries the same file. The engine registers
the core's behaviors when it opens ("Core behaviors"), and the work
queue's and a project's own when its options name them.

An implementation is a Rust type (D58): the `Behavior` trait of
`superschematic-engine` (`rust/engine/src/behaviors/`), its declaration,
a config typed in Rust and its hooks (`Hooks`), each optional. The core's
are `rust/behaviors`, the work queue's `runtime/engine-workqueue/rust`, and
an extension's a crate of its own, on the same API (acme.Rating is
`examples/acme-schematic/crates/behaviors`): the `projectengine` generator
writes a project's engine crate over the extension's crates for a schema
that composes one, which a Rust server links, whose `ffi/` member is the
archive a Go server links and whose `napi/` member the addon a TypeScript
server loads (the engine option `addon`). This section describes the
interface in its TypeScript form, `@superschematic/engine/testing`, whose
members are the trait's under their TypeScript names: a test defines a
behavior with `defineBehavior`, which the test addon's bridge runs
("Development"). The addon a release ships refuses one.

```ts
import { openEngine } from '@superschematic/engine';
import { defineBehavior } from '@superschematic/engine/testing';
import declaration from './counter.behavior.json' with { type: 'json' };

// counter.behavior.json declares test.Counter: a `limit` config, the field
// `count`, the writing operation `increment` and the veto code `at_limit`.
export const counter = defineBehavior<{ limit?: number }>({
  declaration,
  migrations: [{ version: 1, name: 'count', columns: { count: { type: 'integer', notNull: true, default: 0 } } }],
  guard(view, request) {
    if (request.kind === 'operation' && request.operation === 'increment' && view.config.limit !== undefined
        && Number(view.columns.get().count) >= view.config.limit) {
      return { reason: `the count is at its limit, ${view.config.limit}`, code: 'at_limit', details: { limit: view.config.limit } };
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

const engine = openEngine({ path: 'shop.db', policy, metaSchema, behaviors: [counter] });   // on the test addon
engine.behaviors.register(another);                                   // later
engine.instances.invoke(me, 'Item', id, 'increment', {});            // { count: 1 }
```

`metaSchema` is the `json-schema` output of the deployment's binary, which
lists the behaviors it declares; the core's lists the core's own
(`Workflow`, `Comments`, `Revisions`, `Dependencies`, `Links`,
`Rollups`, `Search`, `Reactions`, `Constants`, `Variants`, `Branches`,
`Timers`, `StateRules`, `Numbering`, `Approvals`, `Derived` and
`Provenance`, and the work-queue
package's `Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`,
`Budget` and `Retries`, and the commerce package's `Stock`, `Calendar`,
`Holds` and `Payments`, D70), so its loader refuses any other.

### The implementation

`BehaviorImplementation<Config>`; `defineBehavior` types its config. Every
function is synchronous (D16): one that returns a promise is a
`BehaviorError`, which rolls the write back.

| Member | What it is |
| --- | --- |
| `declaration` | the declaration the compiler registers, as its JSON file holds it |
| `parseConfig(config, target)` | checks a config its `configSchema` accepted and returns what the other functions get as `config`; throws `BehaviorConfigError` to refuse it. Absent, `config` is the JSON config, `{}` when the type gives none. `target` has the schema, the type, its fields' JSON keys and `fieldSchemas` (each one's JSON Schema, as the describe document writes it), the document's other `types` (their `names`, and `get(name)`, each one's fields, which holds the type to the version's checks: "Other types"), every behavior the type lists with its config, the fields each declares with whether each is volatile (`behaviorFields`: by behavior, `{ name, volatile }` in declaration order), and, when the schema is defined or published, `schemas` ("Other instances") |
| `configChange(before, after, change)` | whether a new version may change the config, add the behavior (`before` undefined) or remove it (`after` undefined) on a schema with instances: a reason refuses. `change.instances` says whether an instance of the schema exists, in any namespace that reads the version (every namespace that looks a shared schema up), read at define and again in the publish's transaction, so a change only stored instances could break is refused only while there are some; it is true for an add or a remove, which are asked only then. Absent, only an identical config, and no adding or removing while there are instances |
| `afterConfigChange(context)` | brings its own storage in line when a published version adds it (a first version included), removes it or changes its config ("Publishing") |
| `migrations` | its storage, as forward-only migrations: the columns each adds to the instances table, the `indexes` it adds on them, and an `up(sql)` for its own tables ("Storage") |
| `initialize(context, params)` | sets up its state for a new instance; `params` is its own entry of the create's parameters, `{}` when the create gives none ("Create parameters"); it refuses with a `BehaviorVetoError`, whose code its declaration lists |
| `validate(context, request)` | judges the instance's own fields a create or an update would store, once the live version accepts them: returned issues refuse the write (`invalid_instance`), as the live version's do ("Validating fields"). It gets no precondition: what one asserts is a guard's to judge |
| `checkedTypes(config)` | the types of the document its `validate` checks values against with `checkType`, which the compatibility rule then holds ("Validating fields") |
| `instanceSchema(config, form, typeSchema)` | what its `validate` holds the fields to, as JSON Schemas the describe document and the create and update tools carry under `allOf` ("Validating fields") |
| `guidance(config, target)` | what it tells a caller about the type under its config: a summary, and by operation name the guidance it gives each operation of the type, its own, the built-in ones and other behaviors' it guards, each error a veto code its declaration lists ("Guidance" under "Tools") |
| `createParamsSchema(config, target)` | the create parameters it takes under its config, narrower than its declaration's `createParamsSchema`, which the describe document and the create tool show in its place; display only, as `instanceSchema` is ("Create parameters") |
| `guard(view, request)` | may veto a `create`, an `update`, a `delete` or an `operation` of any behavior on the type: a returned reason, or `{ reason, code?, details? }`, vetoes ("Vetoes and preconditions"). It is asked once every `validate` has accepted the fields. A create's request carries the new instance's `data` and the create's parameters by behavior (`behaviors`), and no precondition. An operation's request carries `writes`, as its declaration says, so a guard that holds back changes can let a read-only operation through. An update a behavior's operation applies names that behavior as `caller`, as a `call()` does. `precondition` is the behavior's own entry of the caller's preconditions |
| `operations` | a handler per declared instance operation: `(context, params) => result`, with an `OperationContext`; it refuses with a `BehaviorVetoError`, whose code its declaration lists |
| `schemaOperations` | a handler per declared schema-level operation (`scope: "schema"`): `(context, params) => result`, with a `SchemaContext`, whose `sql` writes the behavior's own tables in a writing one ("Schema-level operations") |
| `namespaceOperations` | a handler per declared namespace operation (`scope: "namespace"`, D77): `(context, params) => result`, with a `NamespaceContext` ("Namespace operations") |
| `fields` | a reader per declared field: `(view) => value`. A read returns them under the behavior's name, apart from the own fields ("Instances and events") |
| `filters` | what a list filters on, `{ <name>: { column, type, description? } }`: a declared field, or `<field>.<member>`, a camelCase member of a declared field whose value is an object, whose value is one of its columns, by its own name; a list's `where` names it by its qualified name, the behavior's name and a dot before it (`Lease.holder`); `type` is its JSON type, `string`, `number`, `integer` or `boolean`, and `description` what the list tool's `where` says of it, the declared field's when absent. A list's `where` compares the column in SQL, a null column holding no value, so the field's reader must return what the column holds, and nothing (absent or null) for null; a migration's index on that column alone serves the filter ("Filters" under "Instances") |
| `afterChange(context, change)` | runs after a create, an update, a delete or a caller's writing operation, in the same transaction. An operation's change carries `before`, the instance's own fields before it, when its `update()` changed them, and `attribution` when its `update()`s attributed the fields they changed ("Contexts") |
| `guardReference(view, reference, request)` | may veto an `update`, a `delete` or a writing `operation` of an instance this behavior's instance refers to ("References"), as a guard does; the view is the referencing instance's, and the request carries no precondition |
| `afterReferenceChange(context, reference, change)` | runs after such a change, in the same transaction, on the referencing instance; after a delete it must remove the reference. Its context's `writing` says the referencing instance's own write made the change |
| `reactions` | `{ react(context, event), watches?(config, schema) }`: reactions to committed events, which the runner runs after the commit ("Reactions and schedules") |
| `schedules` | named timed work, `{ <name>: { everyMs, run(context) } }`, which the runner runs on each schema that composes the behavior; `everyMs` is a number or a function of the schema's config, which returns `null` to turn the schedule off on that schema ("Schedules" under "The runner"); `run`'s SQL writes the behavior's own tables, where the write changes nothing an operation returns ("Reactions and schedules") |

Registration (`openEngine({ behaviors })` or `engine.behaviors.register`)
refuses, naming every problem, an implementation whose `operations` or
`fields` are not exactly the ones its declaration names; `reactions`
without a `react` function; a schedule whose name is not camelCase, whose
`everyMs` is neither an integer of at least 1000 nor a function, or that
has no `run`; a declaration of the wrong shape, with an operation named
`create`, `get`, `list`, `update`, `delete` or `lookup`, or with a schema
that does not compile; malformed migrations, columns or indexes, an index
over a column no migration up to its own adds included; a filter on a
field it does not declare, or a member of one whose name is not `<field>.<member>` with a camelCase member, or a column its migrations do not add; a veto code that is
not lowercase snake case of at most 64 characters, or is listed twice;
an operation beside the instance that does not write or has `scope:
"schema"`, `"log": false` on an operation that is not beside, `"asks"`
other than `"read"` or on an operation that is not beside with `"log":
false`, and a behavior with a beside operation and a field that is not
volatile ("Instances and events"); a namespace operation that sets
`beside` or `log`, and `namespaceOperations` that are not exactly the
namespace operations its declaration names ("Namespace operations"); and
a signal whose name is not camelCase or is
declared twice, whose `paramsSchema` is absent or does not compile, or
whose `access` is not `read`, `write` or `operation`.

A declaration's `signals` (D77) are the messages about an instance the
engine delivers to the event streams open on it and keeps nowhere
("Signals"); `GET /behaviors/{name}` lists them, when it declares any:

```json
"signals": [
  { "name": "typing", "description": "Who is typing.", "access": "read", "paramsSchema": { "type": "object", "properties": { "thread": { "type": "integer" } } } },
  { "name": "draft", "access": "operation", "paramsSchema": { "type": "object", "required": ["message", "offset", "text"] } }
]
```
An operation's `paramsSchema` sets
`additionalProperties: false`, so the handler and every guard read the
same declared parameters and no alias reaches one and not the other; a
`preconditionSchema` does too; the compiler refuses the same
declaration when it registers. A `createParamsSchema` is an object
schema whose `additionalProperties` is `false` or a schema, so every key
it admits is checked ("Create parameters"). The engine adds
one rule to the compiler's: a name is `<extension>.<Name>` with an
extension name of a letter, then letters, digits, `_` and `-` (or a core
`<Name>`), since the migration ledger is keyed by it. A name registers
once.

### Create parameters

A behavior may take parameters at create: what a new instance holds from
its first event, which a separate call after the create would leave it
without for a while. Between a create and a later `link` or
`addBlocker`, a queue can claim an instance that has no parent or
blockers yet, and a refused second call leaves an orphan. The
declaration's `createParamsSchema` says what they are; a create gives
them under `behaviors`, by behavior name:

```ts
engine.instances.create(me, 'tasks', { title: 'Build' }, {
  id: 'build',
  behaviors: {
    Links: { project: 'launch', spec: { id: 'doc-1', revision: 2 } },
    Dependencies: { blockers: [{ id: 'plan' }, { schema: 'documents', id: 'doc-1' }] },
  },
});
```

- **Checked first.** Before the insert, the engine refuses an entry for a
  behavior the type does not compose or whose declaration has no
  `createParamsSchema`, and an entry the schema refuses; a behavior with
  a schema and no entry is checked as `{}`, so a schema that requires a
  member makes the entry required. Every issue is one
  `CreateParamsError` (`invalid_argument`), at a JSON pointer into the
  create's arguments: `/behaviors/Links/project`.
- **Every guard, then each initialize.** Once the row is inserted, every
  behavior's guard is asked with `{ kind: 'create', data, behaviors }`:
  `data` the new instance's own fields, which the view's `data` holds
  too, and `behaviors` the parameters as the create gives them. The
  view's columns hold their defaults; a veto (`vetoed`, action `create`)
  leaves nothing of the create. No `guardReference` is asked, since
  nothing refers to a new instance. Then each `initialize(context,
  params)` gets its own entry, `{}` when the create gives none, and does
  what it would otherwise do in an operation: `Links` records the links,
  `Dependencies` the edges, each with its operation's checks.
- **Its own refusals.** A parameter the config refuses (a link name the
  config does not give, a required link not given) throws
  `CreateParamsError` from `initialize`, at a pointer under
  `/behaviors/<its name>`; a check its operation would veto is `vetoed`,
  with action `create`, the operation's code and the entry's pointer as
  `details.path`.
- **One event.** Everything runs in the create's transaction, and the
  create event carries the instance with what the parameters set (its
  `links`, its `blocked`), so the log still replays to what a read
  returns.

`instances.create` in a behavior's context takes `behaviors` too, so a
parent's `initialize` or `afterChange`, an operation or a reaction
creates an instance with its links and edges at once; `Blueprint` stamps
its children this way. The create route's body, the create tool's
arguments and the describe document carry the parameters under
`behaviors` ("HTTP", "Tools").

The declaration's schema is the same on every type; what a config
admits is narrower. An implementation's `createParamsSchema(config,
target)` returns the parameters it takes under the config, which the
describe document and the create tool show in place of the declared
one: `Links` a property per link name, the required ones required, a
`revision` only for a link pinned to a revision and a `release` only for
one pinned to a release, `Dependencies` a blocker's `schema`
from the config's `schemas`, required when the type's own schema is not
among them. It describes what `initialize` enforces, as
`instanceSchema` describes what `validate` enforces: the engine checks a
create against the declared schema, and `initialize` refuses what the
config refuses, with its own messages. An implementation has the hook
only where its declaration has a `createParamsSchema`, and what it
returns is held to the same shape, an object schema whose
`additionalProperties` is `false` or a schema; anything else is a
`BehaviorError`.

### Validating fields

A behavior may judge the instance's own fields a write would store, as
the live version's validation does: which fields keep the value their
create gave them (`Constants`), what shape a field holds for each value
of another (`Variants`). A guard can refuse such a write too, but its
veto is a 409 with a reason and no field, and `validateUpdate()` does not
see it. `validate(context, request)` returns issues instead:

```ts
validate(context, request) {
  if (request.kind === 'update' && request.before.sku !== request.after.sku) {
    return [{ path: 'sku', rule: 'constant', message: 'sku does not change' }];
  }
},
```

- **When.** On every write of the fields: a caller's create and update,
  a behavior's `instances.create`, an operation's `update()`, and
  `validateUpdate()`, which reports the issues without writing. It runs
  once the live version accepts the fields, so each holds its declared
  type: before a create's parameters, its insert and every guard; before
  an update's "nothing changed" check and every guard. An update's
  arguments are checked before it, with the instance unread: the
  preconditions' shapes ("Vetoes and preconditions"). A patch holds own
  fields only, so a behavior's field's name in one is an undeclared key
  (`unknown`).
- **What it gets.** `request` is `{ kind: 'create', data }` or `{ kind:
  'update', before, after, caller? }`, deep-frozen: the own fields, before
  and after the merge for an update, and the behavior whose operation
  applies it as `caller`. The context has `behavior`, `config`,
  `namespace`, `schema`, `version`, `id`, `principal`, `now`, `can()` and
  `checkType()`: no storage and no other instance, which are a guard's to
  read.
- **What it returns.** A list of `{ path, rule, message }`, a path as the
  live version writes one (`result.checks[0].ok`, `''` for the instance),
  or nothing. Every behavior's runs, in list order, and their issues
  together are one `InstanceValidationError` (`invalid_instance`), as the
  live version's are: 422 over HTTP, and that problem as an MCP tool
  error. Anything else, a promise included, is a `BehaviorError`.
- **Other types.** `checkType(type, value, path)` holds a value to a type
  of the schema document as the live version holds a field of that type:
  a JSON object, each field's value, and no key the type does not
  declare, at any depth, with each issue's path under `path`. The type is
  one the implementation's `checkedTypes(config)` names. `define` refuses
  a name that is not a type of the document besides the instance type,
  and a type it reaches whose field the schema runtime cannot check (a
  union, a map); `checkType` of any other type is a `BehaviorError`.
- **Versions.** The compatibility rule holds each type that both
  versions' configs name in `checkedTypes` as a type a field reaches,
  since stored instances hold values checked against it ("The
  compatibility rule"). Whether a config may name another type, or stop
  naming one, is the behavior's `configChange`.
- **What a client sees.** `instanceSchema(config, form, typeSchema)`
  returns JSON Schemas for the fields, which the describe document's
  `instance`, the create tool's `data` and the update tool's `patch` carry
  under `allOf`: `form` is `instance` for the first two and `patch` for the
  last. `typeSchema(type, { nullable? })` renders a type `checkedTypes`
  names as a nested type is rendered ("The describe document"), with
  nothing required in a patch.

`schemas.validate` checks a value against the version's own rules and
asks no `validate`: it has no instance before an update.

### Other types

A behavior may keep values of the schema's other types in its own
tables, rows of a graph's kinds say (D32), so it needs what each type
holds when its config is parsed, and a way to check a value against one
when it writes.

```ts
parseConfig(config, target) {
  const type = target.types.get(config.type);           // a type besides the instance type, or undefined
  if (type === undefined) {
    throw new BehaviorConfigError(`${config.type} is not a type of ${target.schema} (its types: ${target.types.names.join(', ')})`);
  }
  return { type: type.name, keys: type.fields.map((field) => field.key) };
},
operations: {
  save(context, params) {
    const issues = context.validate(context.config.type, params.row);  // [] when the row holds
    // ...
  },
},
```

- **Reading.** `target.types.names` lists the document's types besides
  the instance type, sorted; listing them reads none. `target.types.get(name)`
  returns one, deep-frozen, as `{ name, fields }`, each field `{ key,
  type, kind, jsonType?, depth, optional }`: its JSON key (`jsonTag`, else
  its name), its type's name as the document writes it, whether that is a
  `primitive`, a `scalar`, an `enum` or a `type`, for a scalar the JSON
  type of its values as the describe document writes a field of it
  (`string`, `number`, `integer`, `boolean`, `object`, `array`, or `any`
  for `Generic.JSON`; a catalog scalar's is the catalog's, whatever the
  document declares for it), its list depth (0, 1 or 2) and whether an
  object may leave it out. It returns `undefined` for
  the instance type and for a name that is no type of the document. A
  field the document checks refuse (a map, a union, a type the document
  lacks) is left out, since the version is refused at it. `types` is
  there whenever `parseConfig` runs: at define and publish, and when a
  published version is composed again to run it or to check a new
  version against it, since what it reads is the version's own document,
  which never changes. A read is recorded only while `parseConfig` runs;
  `get` after it returns is a `BehaviorError`. Which types a config reads
  depends only on the config and the document, never on `target.schemas`:
  a version runs, and is checked against the next, with no other schema
  in reach, so define and publish parse each config without them too and
  refuse one whose reads differ then, or that parses only with them.
- **What a read holds.** A type `parseConfig` reads counts as reachable
  from the instance type for the version, and so do the types its fields
  reach. The document checks cover their fields as they cover the
  instance type's ("Schemas"): a map, a union and a type the document
  lacks are refused at define and publish. And the compatibility rule
  holds a new version to them while the live version reads them ("The
  compatibility rule"). A type nothing reads, no field reaches and no
  `checkedTypes` names stays free to change.
- **Checking.** Every context but `validate`'s has `validate(type, value)`:
  a view, a context, an operation's, a schema-level operation's, the
  runner's and `afterConfigChange`'s (which checks with the version being
  published). It holds a value to a type with the version's validator, as
  a nested value of the type is held in an instance: a JSON object, each
  field's value, and no key the type does not declare, at any depth. It
  returns every issue, `{ path, rule, message }` with a path into the
  value (`steps[0].name`, `''` for the value itself), as
  `validateUpdate()` does, and `[]` when the value holds. The type is one
  the version's checks cover besides the instance type: one its fields
  reach, one a behavior's `checkedTypes` names, or one a behavior's
  `parseConfig` read, and the types those reach; any other name is a
  `BehaviorError`, since the version holds no other to the compatibility
  rule. `validate`'s own context keeps `checkType`, held to its
  `checkedTypes`.

### Vetoes and preconditions

A refusal a client branches on carries a code (D16, amended). A guard
answers `undefined` to allow, a reason, or a `Veto`, `{ reason, code?,
details? }`; a handler, `initialize` and `afterChange` throw
`new BehaviorVetoError(behavior, action, schema, id, reason | veto)`. A
code is lowercase snake case and one the behavior's declaration lists:

```json
"vetoes": [{ "code": "at_limit", "description": "The count is at its config's limit." }]
```

The error carries `behavior`, `action`, `reason`, `vetoCode` and
`vetoDetails`; the problem document's `details` is `{ behavior, action,
reason, code?, details? }`. A code the declaration does not list, details
that are not a JSON object, or an answer of another shape is a
`BehaviorError`, at a create as at any change: a guard asked with `kind:
'create'` and an `initialize` that throws are held to the list, and a
create parameter a behavior vetoes carries the code its operation's
check would (`Dependencies`' `already_blocking`, `Links`' `no_revision`).
A `validate` refuses with issues, not a veto; one that throws a
`BehaviorVetoError` anyway is held to the list too. The describe
document lists each behavior's codes.

A caller asserts what a behavior keeps with preconditions, entries by
behavior name, on `update`, `delete`, `invoke` and `operate` (and a
behavior's `instances.invoke`). A behavior declares its entry's schema,
a closed object schema:

```json
"preconditionSchema": { "type": "object", "additionalProperties": false, "required": ["generation"],
                        "properties": { "generation": { "type": "integer", "minimum": 0 } } }
```

```ts
engine.instances.update(me, 'Item', id, { title: 'Desk' }, { preconditions: { 'acme.Hold': { generation: 3 } } });
```

- The engine checks them with the call's other arguments, before it
  reads the instance, so before an update's `validate` and any guard: an
  entry for a behavior the type does not compose, for one that declares
  no schema, or that its schema refuses is a `PreconditionsError`
  (`invalid_argument`) with issues at `/<Behavior>/<member>`.
- Each guard gets its own behavior's entry as `request.precondition`, and
  decides what it asserts; a failed one is its veto, with its code. A
  request a behavior's own code makes (`call()`, `update()`) and a
  `guardReference` request carry none, and so does a create, which has
  no instance to fence: `create` takes no preconditions.
- Guards run once every `validate` has accepted the fields, so an
  update whose precondition its guard would fail and whose fields a
  `validate` refuses is `invalid_instance`, not `vetoed`; `validate`
  never sees a precondition. An operation's guards judge the caller's
  preconditions before its handler runs, so before the `validate` of an
  `update()` it makes, whose own guard request carries none.
- A read-only operation's guards get them too; a patch that changes
  nothing asks no guard. A precondition fences a write.
- A beside operation ("Instances and events") asks only its own
  behavior's guard, so an entry for another behavior, which no guard
  would read, is a `PreconditionsError` (`invalid_argument`) with its
  issue at `/<Behavior>`, rather than a fence that holds nothing.

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
- `validate(type, value)`: a value checked against another type of the
  schema with the version's validator ("Other types");
- `references.list()`: the references the behavior recorded from the
  instance ("References");
- `values.load(json, refs)`: an object the behavior keeps in its own
  tables through the value store, with its values back ("The value store
  in a behavior's tables").

A context (initialize, afterChange, an operation) adds `columns.set()`,
`sql.run()`, `references.add()` and `remove()`, `values.stow()` and
`release()`, `call(behavior, operation, params)`, `instances.invoke` and
`instances.invokeSchema` of writing operations, and `instances.create`.
In a read-only operation `set`, `run`, `add`, `remove`, `stow` and
`release` refuse, `call` and `invoke` reach only read-only operations,
and `create` refuses; after a delete, `columns.get()` returns what the
instance had and `set`, `add`, `remove`, `stow` and `call` refuse. In a
beside operation ("Instances and events") `add`, `remove` and `create`
refuse, and `call` and `invoke` reach only beside and read-only
operations. There is no handle on the instances table beyond the
read-only relation, the event log, another behavior's storage or the
connection: a status one behavior owns changes at another's request only
through its operations, whose guards run, on this instance or another.

An operation's context (`OperationContext`) adds four more: two so a
behavior that changes the instance on a caller's behalf, approving a
proposed change say, runs the checks an update runs, one that says the
call changed nothing, and one that sends a signal:

- `update(patch)` applies a JSON merge patch to the instance's own fields
  and returns them after it. A result the live version refuses is
  refused (`InstanceValidationError`), a behavior's field's name among
  the keys it does not declare (`unknown`), and then one the behaviors'
  `validate` refuses, asked
  with `{ kind: 'update', before, after, caller }`; then every guard is
  asked with `{ kind: 'update', patch, after, caller }`. A patch that changes nothing writes nothing.
  The access policy is not asked again, and no event is appended: the
  operation's event carries the change in its `patch`, and `afterChange`
  gets `before`. A read-only operation's `update` refuses, and so does a
  beside one's, with its call; a called operation's update rolls back
  with its savepoint. `data` reads the
  fields as the operation's updates leave them.
- `update(patch, { attribution })` (in Rust, `update_attributed(patch,
  attribution)`) also says where the values of the fields it writes came
  from (D16, amended): an entry by top-level own field the patch names,
  each `{ confidence?, source? }`, a confidence a finite number from 0 to
  1 and a source a string of at most 200 characters. The engine adds the
  behavior's name to each, `{ behavior, confidence?, source? }`, and
  checks only the shape: what a confidence means is the writing
  behavior's. The last `update()` of a field in the call decides its
  entry, and one that writes it without attributing it removes the
  entry; a called operation's `update()` attributes as its own behavior,
  and its entries roll back with its savepoint. The operation's change
  for `afterChange` and `afterReferenceChange` and its event carry
  `attribution`, the entries of the own fields the call changed; a call
  that attributes none has no `attribution`. An attribution of a field
  the patch does not write, a confidence out of range, a longer source
  or another member is a `BehaviorError`. A caller's own `update` takes
  no attribution: a source that attributes is a behavior.
- `validateUpdate(patch)` returns the issues `update(patch)` would refuse
  the patch for, the behaviors' `validate`'s included, without writing or
  asking a guard.
- `unchanged()` says the call changed nothing, so the engine appends no
  event for it ("Instances and events"). It does nothing in a read-only
  operation.
- `signal(name, data)` sends one of its behavior's signals on the
  instance, of any access its declaration gives it (D77, "Signals"):
  delivered once the call commits, in the order sent, dropped when the
  call or its savepoint rolls back, and not limited. Data its
  `paramsSchema` refuses, more than 4096 bytes of it, and a name the
  declaration does not declare are a `BehaviorError`. It writes nothing,
  so it keeps a beside, read-only or `unchanged()` call what it was.

`validate` gets a narrower context of its own ("Validating fields"):
`behavior`, `config`, `namespace`, `schema`, `version`, `id`,
`principal`, `now`, `can()` and `checkType()`.

### Other instances

A behavior reaches the rest of its namespace as the call's principal
(D16, amended). There is no system principal: every read and invoke asks
the access policy, and a caller who may not read a schema cannot read it
through a behavior either.

```ts
// In an operation of Task: read a milestone's status, then move it on.
const milestone = context.instances.get('Milestone', params.milestone, { fields: ['Workflow.status'] });
const flow = context.schemas.config('Milestone', 'Workflow');          // as the schema holds it
if (milestone && flow && !isTerminalState(flow, String(behaviorField(milestone, 'Workflow', 'status')))) {
  context.instances.invoke('Milestone', milestone.id, 'transition', { to: 'done' });
}
```

| Member | What it does | Asks the policy |
| --- | --- | --- |
| `instances.get(schema, id, { fields? })` | the instance's record, deep-frozen, with every behavior field, the ones `fields` names by qualified name (`Workflow.status`), or none for `[]`; `undefined` when there is none | `read` on the schema |
| `instances.getMany(schema, ids, { fields? })` | a `Map` by id of the instances of one schema, at most 500, in one query; ids with none are left out | `read` on the schema, once |
| `instances.invoke(schema, id, operation, params?, { preconditions? })` | runs an instance operation of another instance, or of this one, as `engine.instances.invoke` would, its guards asked with the preconditions given, and returns its result | `write` or `read` with the operation's name |
| `instances.invokeSchema(schema, operation, params?)` | runs a schema-level operation of a schema, its own or another, as `engine.instances.invokeSchema` would, a writing one in a savepoint, and returns its result | `write` or `read` with the operation's name |
| `instances.create(schema, data, { id?, behaviors? })` | creates an instance of the namespace as `engine.instances.create` would: validates `data`, the instance's own fields, against the live version and `behaviors` against each behavior's `createParamsSchema`, asks every guard, runs every behavior's `initialize`, with its parameters, and `afterChange` and appends its create event, in a savepoint; returns the record, deep-frozen. Without an `id`, the engine's `ids` makes one | `write` on the schema |
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
`json_extract(data, '$.title')`; a field the value store holds is its ref
there, `{ "$value": <hash>, "bytes": <n> }`, so a statement filters and
orders on small fields), then each of the behavior's own columns
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
context.references.list();                           // [{ schema, id, key, hears? }], in the order recorded
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
invoked on it, so its guards run and it gets its own event. Its
`writing` is true when the referencing instance's own write is running
up the call, as when a claim's reservation changes an enclosing budget
the claimed instance refers to: invoking one of its writing operations
then is a cycle (`BehaviorError`), and what its write leaves is that
write's to settle.

After a delete no guard vetoes, no reference to the deleted instance may
remain: each hook removes its reference through such an operation. A
reference left behind is a `BehaviorError`, which rolls the delete back,
the hooks' work with it. Deleting the referencing instance drops its
references. A reference from an instance to itself is never asked about,
since the behavior's own guard and `afterChange` see that instance's
changes. The hooks act as the caller: one who may not write the
referencing schema cannot delete an instance a hook must clear.

#### What a reference hears

A reference hears every change of its target unless it says otherwise,
with a fourth argument (`ReferenceHears`):

```ts
context.references.add('Spec', id, 'spec', 'delete');                                       // the delete alone
context.references.add('Step', id, '', { path: '/behaviors/Workflow/status' });                            // a move of the status
context.references.add('Pool', id, 'cpu', { path: '/behaviors/Budget/meters/cpu/remaining', crosses: 10 }); // a move across 10
```

| `hears` | `afterReferenceChange` runs after | `guardReference` is asked before |
| --- | --- | --- |
| absent | every update, delete and writing operation | every update, delete and writing operation |
| `'delete'` | the delete | the delete |
| `{ path }` | a change that moves the value at `path`, and the delete | the delete |
| `{ path, crosses }` | a change that moves the value across `crosses`, and the delete | the delete |

- `path` is a JSON pointer into the target as a read with every field
  returns it, `{ data, behaviors }`: `/data/<field>` for an own field,
  `/behaviors/<behavior>/<field>` for a behavior's, and on down, such as
  `/behaviors/Budget/meters/cpu/remaining`. `references.add` refuses any
  other pointer (`BehaviorError`). A value moves when it is not
  the same JSON before and after the change; a member that comes or goes
  moves too.
- A value's side of `crosses` is one of three: not a number (absent,
  null, a string), below the number, or at or above it. A change moves
  it across when the side before differs from the side after, so a value
  that becomes a number or stops being one crosses every number.
- Recording a reference again, by schema, id and key, records what it
  hears now and keeps its place in the order. `list()` returns `hears`
  for a reference that has it.
- The engine compares the target's record before and after the change,
  which it computes for the event's patch anyway, and reads only the
  references that hear it, through an index led by the target, `hears`
  and `crosses`: a target with thousands of references on one value,
  each with its own number, costs a change the references whose number
  it crosses. `Links` and `Dependencies` record theirs with `'delete'`,
  since only a target's delete asks anything of them; Queue's hear the
  values its copies turn on (`runtime/engine-workqueue/README.md`).

### Schema-level operations

An operation declared with `scope: "schema"` has no instance: it runs on
the schema as a whole, from `schemaOperations`, with a `SchemaContext`:
the behavior, its config, the call, `can`, `validate` ("Other types"),
`instances`, `schemas`, and `sql` that reads the behavior's tables and
its columns across the schema (`sql.instances()`). No instance guard runs
and no event is appended for it; a writing one changes what a read of an
instance returns only through the instance operations it invokes and the
instances it creates, each with its own event. A writing one's `sql`
also writes the behavior's own tables (`sql.run`, on `sql.table(name)`),
in the call's transaction, under the rule a schedule's writes keep
("Reactions and schedules"): `Search`'s `settleEmbeddings` stores vectors
this way. A read-only one's `run` refuses (`BehaviorError`). An
operation beside the instance ("Instances and events") runs on an
instance, and one declared with both is refused.

```ts
engine.instances.invokeSchema(me, 'Order', 'summarize', { since: 0 });   // a schema-level operation of Order
```

`invokeSchema` asks the policy for `write` or `read` with the
operation's name, as `invoke` does, and runs a writing one in a
transaction; a behavior runs one with `instances.invokeSchema` ("Other
instances"). An instance operation is `not_found` there and to
`instances.invokeSchema`, and a schema-level one is `not_found` to
`invoke`, `call()` and `instances.invoke`, each naming the other scope.

### Namespace operations

An operation declared with `scope: "namespace"` (D77) runs on the
behavior's own tables across every schema of a namespace that composes
it, with no instance and no schema: "my mentions" and "what is unread"
over every conversation, which one statement reads, since the tables
already hold every schema's rows by their namespace and schema columns
and each behavior has one key per engine ("Storage"). It is called by the
behavior's name:

```ts
engine.behaviors.invoke(me, { namespace: 'default', behavior: 'Comments', operation: 'inbox', params: { limit: 20 } });
// POST /namespaces/default/behaviors/Comments/operations/inbox, the MCP tool comments_inbox,
// client.behaviors.invoke('Comments', 'inbox', { limit: 20 })
```

- **The call.** A behavior the engine does not run is `not_found`, and so
  is an operation it does not declare with namespace scope, naming the
  scope it has; one of namespace scope is `not_found` to `invoke`,
  `invokeSchema`, `call()` and `instances.invoke`, and is no operation of
  a type: the describe document names it under its behavior
  (`namespaceOperations`), and the tools document holds its tool. The
  policy is asked `{ principal, action, namespace, behavior, operation }`
  ("Access"), then the parameters are held to its `paramsSchema`
  (`invalid_argument`). A writing one runs in a write that holds its
  namespace's writer, as an instance's write does, and an archived
  namespace refuses it; a read-only one takes no writer. It is never
  beside an instance and appends no event, so it declares neither
  `beside` nor `log`.
- **Its context** (`NamespaceContext`) has `behavior`, `namespace`,
  `principal`, `now`, `can()`, `instances` (reads, and invokes of
  read-only operations), `values` (`load`), `configs()` and `sql`. It has
  no config, schema, instance, `update()`, `signal()` or `call()`: a
  writing `instances.invoke` or `invokeSchema` and `instances.create`
  are a `BehaviorError`, which rolls the call back.
- **`sql.readable()`** names a read-only relation over the schemas of the
  call's namespace whose live version composes the behavior and that the
  principal may read, one row each, its column `schema`; a statement
  joins it, and a row of a schema the principal may not read never
  leaves the statement. The policy is asked `read` once per such schema
  per call, the first time a statement names it, and a schema it refuses
  is left out, never refused, as the search across schemas skips one.
  It is defined like `sql.instances()`, a common table expression ahead
  of the statement with the names inlined, as `bhv_<key>___readable`.
  There is no `sql.instances()`: the operation has no schema.
- **Its writes.** `sql.run` writes the behavior's own tables, in the
  call's transaction, in a writing one; a read-only one's `run` is a
  `BehaviorError`. The SQL checks every behavior's statement passes keep
  `engine_*` out of it ("Storage"), so neither kind appends an event or
  touches `engine_instances`.
- **`configs()`** gives each composing schema with the config it gives
  the behavior, as `parseConfig` returned it for its instance
  operations, asking nothing (`config(schema)` one of them): a handler
  that keeps a schema's rows only when its config names a permission the
  caller holds (`can()`) reads the config of each schema it keeps.
- A behavior no published schema composes has no storage yet; its first
  namespace call creates it, as a publish would, and runs with empty
  tables and an empty `readable()`.

```ts
namespaceOperations: {
  inbox(context, params) {
    const notes = context.sql.table('note');
    return context.sql.all(
      `SELECT n.schema, n.instance, n.body FROM ${notes} AS n JOIN ${context.sql.readable()} AS r ON r.schema = n.schema
       WHERE n.namespace = ? AND n.subject = ? ORDER BY n.at DESC LIMIT ?`,
      [context.namespace, context.principal.subject, (params as { limit: number }).limit],
    );
  },
},
```

In Rust a namespace operation's handler is `fn(&B, &NamespaceCx<'_,
B::Config>, &Value) -> Result<Value>` in `Hooks::namespace_operations`,
and `cx.configs()` returns `(schema, &Config)` pairs.

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
names for it, one at a time, in log order, per namespace, beside
operations' events among them ("Instances and events"); `event` is the
event as `engine.events.read` returns it, with its `cause` when the
runner's work wrote it. A schedule's `run(context)` runs once an
interval on each schema that composes the behavior, in each namespace.
Their context is a schema-level one (`WorkContext`): the behavior, the
schema's config, namespace, schema and version, the runner's principal,
`now`, `can()`, `validate()`, `instances` and `schemas`, whose `invoke`
and `invokeSchema` run writing operations and whose `create` creates, and
`sql` reading the behavior's own tables and its columns across the
schema (`sql.instances()`). They change what an operation shows only
through the operations they invoke and the instances they create, whose
events record the cause. A schedule's `everyMs` may be a function of the
schema's config, and `null` from it turns the schedule off there
("Schedules" under "The runner"). A reaction's context adds
`before(event)`: the event's instance as the log had it just before the
event, `{ data, behaviors }` as a read returns it, `undefined` for its create, which
is all a delete leaves of it; it asks `read` on the event's schema. A
schedule's adds `schedule`, its name, and `previous`, when its last run
committed. Each is synchronous and runs in its own transaction with the
runner's record of it, and a throw rolls both back ("The runner").

A schedule's `sql` also writes the behavior's own tables (`sql.run`, on
`sql.table(name)`), in the run's transaction, as the runner's principal,
so a run that throws leaves none of its writes (D32). The relation over
the instances stays read-only, as in every context, and a reaction's
`sql` writes nothing. A schedule writes its tables directly only where
the write changes nothing an operation returns: history no commit or
snapshot pins, rows of what no operation can read any more (a discarded
draft's), and data that only makes a read cheaper, a snapshot say. One
more kind may change what an operation returns: an index derived from
the instances' own fields, which moves what a ranking returns and in
what order and never an instance a read returns (D16, amended: search's
vectors). A change an operation shows, such as discarding an idle draft,
still goes through the operation the run invokes on each instance, so
its guards run and it appends its event. A writing schema-level
operation's own writes keep the same rule. The engine cannot tell one
write from the other: keeping to the rule is the behavior's part.

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

#### The value store in a behavior's tables

A behavior that keeps an object in its own tables whose members can be
large, a copy of the instance's own fields say, keeps it through the
engine's value store ("The value store"), as `Revisions` does:

```ts
const stowed = context.values.stow(`revision ${revision}`, context.data);
context.sql.run(`INSERT INTO ${context.sql.table('revisions')} (..., data, value_refs) VALUES (..., ?, ?)`, [..., stowed.json, stowed.refs]);
// later
const data = context.values.load(String(row.data), row.value_refs as string | null);
```

`stow(key, object)` stores each top-level member whose JSON is longer
than the threshold once, by hash, and returns what the row keeps: `json`,
the object with a ref in each such member's place, and `refs`, the
pointers to them as JSON text, null for none. The row named by the
behavior, the call's namespace and schema, the context's instance (none
in a schema-level operation, a schedule or `afterConfigChange`) and
`key` then holds those values and no longer what it held before.
`load(json, refs)` returns the object with its values back, deep-frozen;
`refs` null is an object stored with none, a row written before the
behavior stowed included. `release(key)` drops what a row holds, and
`release()` what every row of the context's instance holds: a behavior
that deletes a row releases it, or its values stay. A value no row,
instance or event holds is removed. A view, a reaction and a read-only
operation load only.

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
relation asks the policy as one), `eachInstance(visit)`, which
visits every instance of the
schema in the namespace in creation order, read 500 at a time, each `{
id, data }` with its own fields, deep-frozen, and `validate(type, value)`,
which checks a value with the version being published ("Other types").
It has no principal and
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
it does, and two behaviors that add an operation of the same name. A
behavior's field collides with nothing: a read returns it under the
behavior's name, so it may share its name with an own field of the type
and with another behavior's. They also refuse a behavior with no
implementation registered, one on a type other than the instance type
(only it has instances), a type its `checkedTypes` names that is not one
of the document's besides the instance type, and a field the schema
runtime cannot check in a type it names or reads through
`ConfigTarget.types`, or one that type reaches. A live version whose behavior has no
implementation in this engine, as after a restart without it, makes
every call on the schema `unavailable` until one registers.

### Lifecycle

| Call | Order, in one transaction for a write |
| --- | --- |
| `create` | normalize (the parse, with defaults) -> validate (the own fields) -> each `validate` (`kind: 'create'`) -> check `behaviors` against each `createParamsSchema` -> insert -> every guard (`kind: 'create'`) -> each `initialize`, with its parameters -> each `afterChange` -> event |
| `get`, `list` | each field reader |
| `update` | check `preconditions` against each `preconditionSchema` -> check `expectedSeq` -> normalize the patch (no defaults), merge and validate -> each `validate` (`kind: 'update'`) -> nothing more if nothing changed -> every guard, each with its precondition -> write -> each `afterChange` -> event |
| an operation's `update()` | normalize the patch, merge and validate -> each `validate`, with `caller` -> nothing more if nothing changed -> every guard, with no precondition -> write; `validateUpdate()` stops before the guards and writes nothing |
| `delete` | check `preconditions` -> check `expectedSeq` -> every guard, each with its precondition, then each referencing behavior's `guardReference` -> the row goes -> each `afterChange` -> its references go -> event -> each `afterReferenceChange` -> no reference to it may remain |
| `invoke` | policy -> parameters against `paramsSchema` -> check `preconditions` -> check `expectedSeq` -> every guard, each with its precondition (and, for a writing operation, each `guardReference`) -> the handler -> its result against `resultSchema` -> nothing more if the handler said it changed nothing (`unchanged()`) and wrote no row -> for a writing operation, each `afterChange`, the next `seq`, the event and each `afterReferenceChange` |
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

A read returns an instance's own fields in `data` and its behaviors'
fields in `behaviors`: by behavior name, in the type's behavior order,
an object of each behavior's fields in its declaration's order. Every
behavior on the type that declares a field has an entry, `{}` while
none has a value; one that declares none has no entry. A reader's
`undefined` or `null` leaves the field out; any other value must be
JSON. A string names a behavior's field by its qualified name, the
behavior's name, a dot and the field's (`Workflow.status`,
`acme.Rating.ratingCount`): a behavior's read of another instance, a
list's `where`, a rollup's `field` and a display's summary fields take
it. `create` and `update` write `data` only, so a behavior's field
changes only through its operations, and its name in either is an
undeclared key (`unknown`), as `schemas.validate` reports it.
`schemas.behaviors(principal, name)` lists what a version composes, with
each config and declaration.

A field its declaration marks `"volatile": true` can move without an
event of its own instance: the engine computes it at the read, from
other instances, the clock or the config. The core's are
`Dependencies.blocked`, `Links.targets` (whose `latest` and `stale` read
the target), `Rollups.values`, Comments' four (which its operations
beside the instance move), and the work queue's `Lease.active` and
`Budget.meters` (a daily meter starts again at a read). The log holds
such a field as the instance's last event found it, the describe
document marks it, and `Reactions`' `changes`, which hears a field
through its instance's events, refuses it (D56).

A volatile field may also say `"perReader": true` when its value is the
caller's rather than the instance's, as Comments' `unread` is (D77). A
read computes it for whoever reads. No event records it: an operation's
or an update's `patch` leaves it out, as does a create event's
`behaviors`, and so do the moves a referring behavior's hook hears, so
a caller's own count never lands in the log as the instance's. A
declaration that gives `perReader` without `"volatile": true` is
refused, by registration and by the compiler's registry. The describe
document marks it `perReader: true` beside `volatile: true`.

A writing operation appends an `operation` event, `{ behavior,
operation, params, patch, attribution? }`, its `patch` a merge patch of `{ data,
behaviors }`: under `data` the own fields its `update()` changed, under
`behaviors` the behaviors' fields that moved, a part absent when it
changes nothing, and its `attribution` what its `update()`s attributed
of the fields under `data` ("Contexts"); a read-only one appends none.
Replay reads `patch` alone. A create's event carries
the whole instance, `{ data, behaviors }`, and an update's its patch
under `data` and any change the behaviors' fields took under
`behaviors`, so the log replays to the instance a read returns, each
value the value store holds read back by its hash. A member of `params`,
or a field in `patch`, longer than the threshold is a ref there, as a
field is in a create's event ("The value store").

An operation event is a change to the instance a read returns, so a
writing operation takes the instance's next sequence and moves its
`seq`, and with it the HTTP API's `ETag`, as an update does. It does so
even when its `patch` is empty: the engine cannot see what the operation
changed in its behavior's own tables. An `update`, `delete` or
operation that expects the sequence from before the operation is refused
(`seq_mismatch`) before any guard is asked. A read-only operation checks
`expectedSeq` against the sequence it reads and moves nothing.

An operation whose declaration says `"beside": true` runs beside the
instance (D77): a conversation's post, a reaction to a message, a read
marker, which should not turn a busy instance's edits into 412s, nor
wait on a lease another principal holds. It writes only its behavior's
own columns and tables, so a behavior that declares one declares every
field volatile, computed at the read from them; registration refuses a
beside operation that does not write or has `scope: "schema"`, and a
behavior that declares one and a field that is not volatile.

```json
{ "name": "post", "writes": true, "beside": true, "paramsSchema": { ... }, "resultSchema": { ... } }
{ "name": "markRead", "writes": true, "beside": true, "log": false, "paramsSchema": { ... }, "resultSchema": true }
```

- The policy is asked `write` with its name, and its parameters and its
  own behavior's preconditions are checked, as any writing operation's
  are. Its own behavior's guard is asked, with that behavior's entry of
  the preconditions; no other behavior's guard and no `guardReference`
  is, since it changes nothing they protect. An active `Lease` on the
  instance lets any principal's beside operation through.
- It takes no sequence: the instance keeps its `seq`, so its `ETag`, and
  its `updatedAt` and `updatedBy`. `expectedSeq` is checked against the
  sequence it reads, as a read-only operation's is, and `operate` returns
  that sequence. No `afterChange` and no `afterReferenceChange` runs.
- It appends an `operation` event with `seq: null` and `beside: true`,
  whose change is `{ behavior, operation, params, result }`, the result
  in place of a patch ("The event log"). Replay, `before()` and retention's bases skip
  it, and `Reactions`' rules, which hear fields, hear nothing of it; a
  behavior's own `react` gets it with the rest, and `before()` of it is
  the instance its sequenced events before it leave. `beside: false` in
  a read of the log or the stream drops it.
- `"log": false`, which only a beside operation may declare, appends no
  event at all, for a write only its writer reads back. `unchanged()`
  keeps its meaning: a beside call that says it changed nothing appends
  no event either.
- `"asks": "read"`, which only a beside operation with `"log": false`
  may declare, says it writes only its caller's own state, a read marker
  or a follow, so the policy is asked `read` with its name instead of
  `write`, a caller who may only read the instance may call it, and an
  archived namespace lets it run. Its tool hides from no reader either.
  The behavior's document carries `"asks": "read"` after `log`.
- Its handler that reaches past its behavior's own storage, with
  `update()`, a `call()` or `instances.invoke` of a writing operation
  that is not beside, an `instances.invokeSchema` of a writing one,
  `instances.create`, or `references.add` or `remove`, is refused with a
  `BehaviorError`, and so is the whole call when the handler returns,
  even one that went on past the refusal, which rolls it back. It may
  call another beside operation and a read-only one, and the call
  appends its one event.

A write that changes nothing writes nothing (D16, amended). An update
whose patch changes nothing asks no guard and appends nothing. A writing
operation's handler says the same of its call with `unchanged()`: an
expire that finds the lease active, a sweep's miss that finds the
deadline ahead, a directive whose dedupe key was sent already. The
engine then appends no event: the instance keeps its `seq`, so a reader's
`ETag` stays good, and its `updatedAt` and `updatedBy`; no `afterChange`
and no `afterReferenceChange` runs, and no reaction hears it. What ran
before the handler stands: the policy, the parameters, the
preconditions, `expectedSeq`, every guard and `guardReference`, any of
which can refuse it; and the result is checked and returned as ever. The
claim is checked: the engine counts the rows the connection writes while
the handler runs (SQLite's `total_changes()`), and a call that wrote
any, its own columns, tables, values or references, the instance's own
fields, an operation it called or invoked or an instance it created, and
says it changed nothing is a `BehaviorError`, which rolls it back. A
called operation's claim is its own: the operation that called it
appends its event unless it says so too. A refusal stays a veto: a
transition to the state the instance is in (`already_in_state`) and an
assign to the assignee (`already_assigned`) write nothing either, and
tell a caller its read is stale.

### Core behaviors

The core declares seventeen behaviors the engine implements
(`internal/registry/behaviors`, section 3.16 of
`docs/extension-model.md`), so every binary's meta-schema admits them,
and the engine implements them in `rust/behaviors` and registers them
when it opens, before `behaviors`: a schema that composes them runs with
no extension linked. The core declares the work-queue behaviors too,
`Lease`, `Assignment`, `Queue`, `Presence`, `Blueprint`, `Budget` and
`Retries`, which `runtime/engine-workqueue/rust` implements, in every
binding (`runtime/engine-workqueue/README.md`): the engine refuses a
schema that composes one until a deployment names it in `behaviors`
(`workQueueBehaviors`, all seven). The commerce behaviors, `Stock`,
`Calendar`, `Holds` and `Payments` (D70), are the same:
`runtime/engine-commerce/rust` implements them and the core's addon
carries them (`runtime/engine-commerce/README.md`), a Go or Rust server
links them through the project's engine crate, and a deployment names
them in `behaviors` (`commerceBehaviors`). Each implementation embeds the copy of
its declaration in its crate's `declarations/`, which the core binary
writes (`make behaviors`) and CI checks (`make behaviors-check`). They
reach the engine only through the plug-in interface above. A deployment
cannot register another implementation under their names.

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
belong to a project, which every create gives:

```json
"behaviors": [
  { "name": "Workflow", "config": { "states": ["todo", "doing", "done", "dropped"], "transitions": [...] } },
  { "name": "Dependencies", "config": { "schemas": ["tasks", "documents"], "gatedStates": ["done"] } },
  { "name": "Links", "config": { "links": {
      "spec": { "schema": "documents", "pinned": true },
      "parent": { "schema": "tasks" },
      "project": { "schema": "projects", "required": true } } } }
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

`Constants` and `Variants` add no field, operation or storage either:
their `validate` judges the fields a write stores ("Validating fields").
On a schema of steps whose kind the create sets for good, and whose
result has the kind's shape:

```json
{ "name": "Constants", "config": { "fields": ["kind"] } },
{ "name": "Variants", "config": { "field": "result", "by": "kind",
    "types": { "verify": "VerifyResult", "review": "ReviewResult" } } }
```

`Branches` keeps a version graph on each instance in tables of its own,
the version graph's SQLite layout ("Branches"), on a schema whose kinds
are other types of the document:

```json
{ "name": "Branches", "config": { "kinds": {
    "step": { "type": "Step", "order": "position", "units": { "timings": "keyed" } },
    "ingredient": { "type": "Ingredient", "parent": { "key": "stepKey", "of": "step" } },
    "cover": { "type": "Cover", "singleton": true } } } }
```

`StateRules` adds no field, operation or storage: its guard holds a
transition and an update to the rules of the state the instance moves to
or is in, and its `validate` a create to its initial state's ("StateRules").
On a schema of invoices whose sent ones keep their customer and amount,
and whose paid ones keep every field:

```json
{ "name": "StateRules", "config": { "states": {
    "sent": { "required": ["customer", "amount"], "frozen": ["customer", "amount"] },
    "paid": { "frozen": "all" } },
  "permission": "invoices.amend" } }
```

`Approvals` keeps sign-offs in a table of its own, and gates the type's
Workflow on them as `Dependencies` and `Rollups` do, on an `expenses`
schema whose claims two approvers sign off before they are approved:

```json
{ "name": "Approvals", "config": { "gates": {
    "approved": { "count": 2, "permission": "expenses.approve" },
    "paid": { "count": 1, "permission": "expenses.pay", "from": ["approved"] } } } }
```

`Timers` keeps deadlines in a table of its own, and the runner's
schedule fires them ("The runner"), each through the instance's
`fireTimer`, on a `requests` schema whose requests open four hours are
escalated and whose visits are reminded a day before:

```json
{ "name": "Timers", "config": { "deadlines": {
    "respond": { "start": { "enters": "open" }, "afterMs": 14400000, "then": { "transition": "escalated" }, "until": { "leaves": "open" } },
    "remind": { "start": { "field": "visitAt" }, "afterMs": -86400000 } },
  "permission": "requests.timers" } }
```

`Numbering` numbers each instance at its create from the counts it
keeps in a table of its own, on an `invoices` schema whose invoices
count per project and per year:

```json
{ "name": "Numbering", "config": { "format": "INV-{yyyy}-{seq:4}", "scope": { "link": "project" }, "reset": "yearly" } }
```

Where they number their records, they number from 1 per instance (a
comment's id, a revision, a proposal's id), so a number says nothing
about another instance; `Numbering`'s number is the one a person reads
across them. Their
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
distribution's (D11, "The invocation policy and the vendor keys"). Each
gives guidance from its config, which every tool of the type carries
("Guidance" under "Tools"). A
refusal is the HTTP API's problem: a transition whose permission the
caller lacks is 403 `forbidden`, and over MCP a tool error carrying that
problem.

#### Workflow

A state machine on the instance's `status`.

| | |
| --- | --- |
| Config | `states` (one or more names: a letter, then letters, digits, `_` and `-`), `initial` (the first state when absent), `transitions`: `{ from, to, permission? }`, `outcomes`: by terminal state, `success`, `failure` or `neutral`, optional |
| Fields | `status`, which a read returns as `behaviors.Workflow.status` and a list filters on (`where: { 'Workflow.status': 'doing' }`), through an index on its column (migration 2); `enteredAt`, when the status last changed, in epoch milliseconds (migration 3) |
| Operations | `transition({ to })` -> `{ from, to }`, writes |
| Guards | its own `transition`, whoever asks: `to` not a state is `invalid_argument`; the state the instance is in (`already_in_state`), a transition the config does not list (`transition_not_allowed`, details `{ from, to, allowed }`) and a move out of a terminal state (`terminal_state`) are `vetoed`, as is an instance without a status (`no_status`); a transition that names a permission the caller lacks (`can`) is `forbidden` |
| Events | `transition`'s operation event, `patch: { behaviors: { Workflow: { status, enteredAt } } }` |
| `configChange` | every old state stays while the schema has instances, and with none a state may go; transitions, permissions, `initial` and `outcomes` may change. Not added to or removed from a schema with instances |

A new instance starts in `initial`. The status is Workflow's own column,
apart from the instance's own fields, which a create or an update
writes, so the type may declare a `status` of its own, and another
behavior moves Workflow's only by calling `transition`, whose guard runs
for that call as for a caller's. `transition` takes `to` and nothing else;
its closed `paramsSchema` lets no alias through. Beyond its config
schema, which the compiler checks too, the engine refuses a config whose
`initial` or a transition names a state it does not list, and a
transition from a state to itself or listed twice. A state no transition
reaches is allowed: a new version keeps every state an instance may be
in, one it no longer enters included.

`enteredAt` is the create's time, then each transition's, so a reader
works out how long an instance has been in its state, and a `Reactions`
rule that hears `Workflow.status` hears its moves (`enteredAt` itself
moves with every one, so a rule refuses it). An instance created before
migration 3 has none until its next transition: a behavior's migration
and its `afterConfigChange` reach only its own storage, which never held
the time, and the log that does is pruned by retention (D56).

A state that no transition leaves is terminal. `isTerminalState(config,
state)`, exported by the package, answers that for a config as a schema
holds it (`schemas.behaviors` lists it): true for one of its states that
no transition leaves.

A terminal state has an outcome, which says how the work it ends went:
`success`, `failure` or `neutral`. `outcomes` names it for the terminal
states it lists, and every other terminal state is a `success`, so a
config without `outcomes` means what it did before outcomes existed. A
key that is not a state, or names a state a transition leaves, is
refused. `stateOutcome(config, state)`, exported beside
`isTerminalState`, gives the outcome of a terminal state of a config as a
schema holds it, and `undefined` for any other state. Workflow reads no
outcome itself: a move into a failure state is a move like any other.
`Dependencies`, `Rollups` and `Reactions` read the outcome of another
schema's state, so a blocker or a child that failed does not count as one
that finished well.

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

Outcomes are read when another instance's field is computed or a rule
runs, as transitions are, and nothing stores them: a new version that
changes an outcome, or makes a state terminal or not, changes which
blockers are open and which rollups hold at their next read, for the
instances already in that state. Nothing that already moved is moved
back.

#### Comments

A conversation on the instance (D77): messages, each top-level or a reply
in the thread of a top-level one, one level deep, which their authors
edit and delete, others react to and pin, and whose threads are resolved
and reopened. A message may be streamed as it is written: `begin` holds
its place, `appendDraft` adds to its draft and sends the `draft` signal,
and `finish` posts it. Each caller has its own read markers and follows,
each message that comes to stand records a delivery for each subject that
should hear of it, and three namespace operations read and work them
across a namespace: the caller's `inbox`, and `claimDeliveries` and
`settleDeliveries` for a notifier or an agent dispatcher. A delivery is
recorded for each subject a message mentions, and each follower, whether
or not that subject may read the schema, and a claim returns up to 4000
code points of the message's body. So a notifier must check that the
recipient may read the instance before it sends the body: read the
instance as the recipient (`instances.get` with a principal of the
recipient's subject and permissions, from the deployment's identity
store), or ask the deployment's policy with the recipient's permissions,
and for a `role:<name>` subject each member it sends to. One who may not
read it gets no body: settle the delivery `dropped`, or send a notice
with a link and no text.
`rust/behaviors/src/comments-contract.md` is the contract the behavior,
the generators and the corpora implement: every record, parameter, check
and refusal in its order.

| | |
| --- | --- |
| Config | all optional: `maxBodyLength` (1..40000 code points, 40000 when absent; a draft too), `editWindowMs` (at least 1000; an author's edit later than that after the message's `postedAt` is refused), `blocks` (the kinds a post, an edit or a finish may give, `ref` and `action`; every kind when absent, none when empty), `moderatePermission` (edit, delete and interrupt another's message, edit past the window, purge), `pinPermission` (pin and unpin; anyone the policy lets write when absent), `resolvePermission` (resolve and reopen another's thread; only its author when absent), `composeTimeoutMs` (5000..3600000, 60000 when absent: how long a composing message's author may append nothing before the runner interrupts it), `maxDrafts` (1..50, 5 when absent: the composing messages one subject may have on an instance at once), `deliverPermission` (what `claimDeliveries` and `settleDeliveries` need for this type's deliveries; nobody claims them when absent), `maxAttempts` (1..20, 5 when absent: the claims a delivery gets), `retryBackoffMs` (1000..3600000, 30000 when absent: a failed delivery is due again after it, doubled after each later failure, at most an hour), `deliveryRetentionMs` (one day to a year, 30 days when absent: how long a delivery is kept, and how far back the inbox's mentions reach) |
| Fields | all volatile: `messages`, the standing (posted and interrupted) messages, top-level and replies; `lastMessageAt`, the greatest `postedAt` of a standing one, absent while there is none; `threads`, `{ open, resolved }`, the standing top-level messages; `composing`, the subjects with a composing message, each once, by the id of their earliest; `unread`, the reader's own (`perReader`, so no event records it): how many standing top-level messages of others stand above its conversation marker, 0 while it has none |
| Operations | beside the instance and writing: `post({ body, blocks?, mentions?, thread?, quotes?, clientId? })`, `begin({ thread?, quotes?, clientId? })`, `appendDraft({ message, text })` -> `{ message, offset, length }`, `finish({ message, body?, blocks?, mentions? })`, `interrupt({ message })`, `edit({ message, body?, blocks?, mentions? })`, `deleteMessage({ message })`, `purge({ message })` -> `{ message, purged }`, `react` and `unreact({ message, emoji })`, `pin` and `unpin({ message })`, `resolve` and `reopen({ thread })`, each -> the message's record but `appendDraft` and `purge`; read-only: `listMessages({ thread?, around?, before?, after?, limit? })` -> `{ items, before, after, root? }`, `getMessage({ message })`, `listEdits({ message, limit?, cursor? })` and `listReactions({ message, emoji, limit?, cursor? })` -> a page; beside the instance, `"log": false` and `"asks": "read"`, writing only the caller's own state: `markRead({ upTo, thread? })` -> `{ readUpTo, unread }` and `follow({ thread, on })` -> `{ thread, following }`; namespace operations (`"scope": "namespace"`): `inbox({ kinds?, unread?, limit?, cursor? })` -> `{ items, next }`, which reads, and `claimDeliveries({ subjects?, prefix?, limit?, ttlMs? })` -> `{ token, expiresAt, items }` and `settleDeliveries({ token, ids, outcome, error? })` -> `{ items }`, which write |
| Signals | `typing({ thread? })` and `viewing({})`, `read` access, which a client sends and the engine delivers; `draft({ message, offset, text })`, `operation` access, which only `appendDraft` sends, after its commit |
| Schedules | `interrupt`, every quarter of `composeTimeoutMs`, at most 15 seconds: interrupts, as the runner, each message whose author has appended nothing for `composeTimeoutMs`, oldest first, at most 1000 a run; `prune`, hourly: removes the schema's deliveries made more than `deliveryRetentionMs` ago that no live claim holds, oldest first, in batches of 1000, going on while a batch comes back full, at most 20 batches a run |
| Guards | none of another behavior: every write is beside the instance, so an active `Lease` lets any principal post. Its own refusals are vetoes with codes and details (`no_message`, `not_a_thread`, `not_in_thread`, `deleted`, `composing`, `not_composing`, `not_author`, `not_idle`, `edit_window_passed`, `too_many_edits`, `too_many_drafts`, `draft_too_long`, `already_reacted`, `not_reacted`, `too_many_reactions`, `already_pinned`, `not_pinned`, `already_resolved`, `not_resolved`, `block_not_allowed`, `ref_not_found`, `not_configured`, `unknown_delivery`, `token_stale`, `not_claimed`), `invalid_argument` for what the `paramsSchema` cannot say, and `forbidden` for a permission the config names and the caller lacks |
| Events | each write's operation event beside the instance, `seq: null`, `beside: true`, `change: { behavior, operation, params, result }`, its `result` the record the caller got; a retry of a post or a begin appends none, `appendDraft`, `markRead` and `follow` none, the namespace operations none, and the reads none |
| `configChange` | added to a schema with instances, which start with an empty conversation; not removed from one, since their messages would stay behind. A config change applies to the next call |

A message's record is `{ id, thread?, quotes?, author, via?, state, body,
blocks, mentions, reactions, createdAt, postedAt?, stand?, editedAt?,
edits, pinned }`, and a top-level message's adds `replies`, `lastReplyAt?`,
`participants` (the latest ten authors of its thread's standing
messages) and `resolved?: { by, at }`. `state` is `composing`, `posted`,
`interrupted` or `deleted`; a message *stands* while it is posted or
interrupted, and `postedAt` is when it came to stand: a post's time, or
its finish's or interrupt's. Ids are per instance, 1, 2, 3, ..., never reused. `author` is the
caller's subject and `via` the calling service's deployable (D37); the
times are the engine clock's. `reactions` holds each emoji's `count` and
whether the caller is one of them (`mine`); in an event it is the
actor's. A block is a reference to an instance the caller may read
(`{ kind: "ref", schema, id }`) or an action's progress (`{ kind:
"action", title, status, data? }`); `mentions` are subjects, recorded and
nothing more in batch 1.

`clientId` makes a post safe to retry: a post with one the caller has
used returns that message as it is now and writes nothing. `edit` keeps
what it does not give, and `listEdits` reads what each edit replaced.
`deleteMessage` empties a message and keeps its place: its id, its
author, its thread and its replies stay. `purge`, which needs
`moderatePermission`, removes a message for good, a top-level one with
its whole thread. A post into a resolved thread leaves it resolved.

A streamed message is the way an agent's reply shows as it is written.
`begin` takes the message's id and its place, `composing`, with an empty
body and nothing counted; each `appendDraft` adds a chunk to its draft and
returns the draft's length before and after it, in code points (a
JavaScript client counts them with `Array.from(text).length`), at most
4000 bytes of JSON string a chunk. Once its call commits it sends the
`draft` signal, `{ message, offset, text }`, to the event streams opened
with signals on the instance; a read of a composing message holds its
draft so far as its body. A reader opens the stream before it reads,
appends a `draft` whose `offset` is the length it holds, ignores one
below it, and reads the message again past it, since a signal is
delivered at most once. `finish` posts the draft, or a `body` in its
place, and counts it as a post. An empty append changes nothing but keeps
the message from going idle, for an author at work with nothing to show;
one idle for `composeTimeoutMs` is interrupted by the schedule, as the
runner, or by anyone the policy lets write: it keeps its draft as its
body, `interrupted`, or is deleted when the draft has no text. Its author
may interrupt it, or drop it with `deleteMessage`, at any time. A
composing message takes no edit, reaction, pin, resolve, reply or quote
(`composing`); an author's edit of an interrupted message posts it.

`listMessages` pages the top-level messages, or with `thread` that
thread's replies and its top-level message as `root`, always in
ascending id order: with no anchor the latest page, `before` an older
one, `after` a newer one, and `around` a page about one message. `before`
and `after` in the result are the anchors of the pages either side, null
at an end, and `readUpTo` is the caller's marker on the listing, absent
while it has none.

A message's `stand` is its place in the order the conversation's messages
came to stand, given at its post, finish or interrupt and never changed;
read markers count in stands, not ids, so a streamed reply that began
before a message a reader read and finished after it is unread. `markRead`
moves the caller's marker on the top-level messages, or with `thread` on a
thread's replies, up to the greatest stand among the listing's messages
with an id up to `upTo`, never back. A subject follows each thread it posts
in and each it is mentioned in (a `role:` group never), and `follow` turns
that on or off. When a message comes to stand it records one delivery per
subject it mentions or that follows its thread, never its author, with its
reasons (`mention`, `reply`); an edit that adds a mention adds one, and
`deleteMessage`, `purge` and the instance's delete remove them but those a
claim holds, which the instance's delete removes too. The engine sends
nothing: `claimDeliveries` claims the due ones of the schemas whose
`deliverPermission` the caller holds, under one token that lapses after
`ttlMs`, dropping those whose message no longer stands or whose subject
has read it, and `settleDeliveries` marks them `sent`, `failed` (due again
after the backoff, dropped at `maxAttempts`) or `dropped`. The `inbox`
lists the caller's mentions, the threads it follows and the conversations
it has read, across the schemas of the namespace it may read, newest
first, each with how many of its messages the caller has not read; an item
with none is done, and stays.

Its storage is migration 2 (`conversation`): `comment_messages`,
`comment_edits`, `comment_reactions` and `comment_conversations`, the
last holding each instance's counters and the counts the fields read
beside the instance column `count`. The migration moves version 1's
comments, a chain of replies, into threads one level deep: a comment's
thread is the root of its chain, and it quotes the comment it replied to
when that was a reply. Migration 3 (`drafts`) adds `comment_drafts`, one
row per non-empty append, and a composing message's columns,
`draft_length` and `last_append_at`, with `posted_at`, which it sets to
each existing message's `created_at`. Migration 4 (`inbox`) adds a
message's `stand` and the conversation's `last_stand`, numbering each
message that stood by its id, and the tables `comment_reads`,
`comment_follows` and `comment_deliveries`.

#### Revisions

Immutable revisions of the instance's own fields, with an optional review
step.

| | |
| --- | --- |
| Config | `review: { permission }`, optional |
| Fields | `revision`: the latest revision's number, absent before the first; `pendingProposals`: with review, how many proposals are pending, absent without it |
| Operations | `listRevisions({ limit?, cursor? })` and `getRevision({ revision })` -> the revision, read-only; with review, `propose({ patch, note?, evidence? })`, `approve({ proposal })`, `reject({ proposal, reason? })`, which write, and `listProposals({ state?, limit?, cursor? })`, read-only |
| Guards | without review, the four review operations are `vetoed` (`no_review`); `approve` and `reject` need the review permission (`can`), else `forbidden`, whoever calls them, and a proposal that is not pending is `vetoed` (`not_pending`, details `{ proposal, state }`) |
| Refusals | `getRevision` of a revision the instance has not reached (`not_found`); `propose` with evidence that names a schema the namespace does not have, an instance that does not exist, or a revision of an instance whose schema does not compose Revisions or that it has not reached (`invalid_argument`, at `/evidence/<i>/schema`, `/id` or `/revision`), or a schema the proposer may not read (`forbidden`) |
| Events | a create's and an update's event carry `revision`; with review, a create's event and each `propose`, `approve` and `reject` event carry `pendingProposals`; `approve`'s carries the fields its patch changed and `revision` |
| `configChange` | `review` may be added, removed or changed. Added to a schema with instances, whose history starts at their next change; not removed from one |

A create records revision 1 and every change of the own fields the next,
in its transaction: an update that changes something, and an operation
that changes them with `update()`, an approval included. A revision is
`{ revision, data, createdBy, createdAt, proposal? }`, where `data` is the
own fields as the change left them, never a behavior's field. An
operation that changes no own field, a comment or a transition, records
none.

`getRevision({ revision })` reads one revision by its number, as
`listRevisions` reads a page of them: a pinned link's `revision`, a
proposal's `base` or `revision`, or a `Blueprint` definition's pin names
one. A number the instance has not reached is `not_found`.

`propose` checks its patch with `validateUpdate`: one that names a
field the type does not declare, a behavior's among them, or leaves the
instance invalid is `invalid_instance`,
and one that changes nothing is `invalid_argument`. It stores the patch
as a pending proposal and changes no own field; `pendingProposals`,
present while the config has review, counts it until it is settled. Who
may propose is the access policy's call: it is asked for `write` with
the operation `propose`, apart from a plain update, which names no
operation. `approve`
applies a pending proposal's patch with `update()`, so the update's
validation and every guard run, and records the revision it makes with
the proposal's id; a refusal leaves the proposal pending. The patch
applies to the instance as it is then, not as it was proposed; `base` is
the revision it was proposed against, so a reviewer can see the instance
moved. `reject` settles it with an optional reason. A proposal is `{ id,
patch, note?, base?, state, createdBy, createdAt, reviewedBy?,
reviewedAt?, reason?, revision?, evidence? }`, `state` one of `pending`,
`approved` and `rejected`; approving or rejecting one that is not pending
is `vetoed`, and naming none is `invalid_argument`.

A proposal may cite evidence for its reviewer, at most 64 entries of
`{ schema, id, revision? }`: the instances it rests on, each optionally
at a revision. `propose` reads each one as the proposer, so a schema the
proposer may not read is `forbidden`, and refuses a schema the namespace
does not have, an instance that does not exist, and a revision of an
instance whose schema does not compose Revisions or past its latest. It
stores the list as given, as data: the engine records no reference, so a
cited instance may change or go and the proposal keeps what it cited.
Every proposal record carries it as `evidence`.

A revision's fields and a proposal's patch go through the value store
("The value store in a behavior's tables"): a field longer than the
threshold is stored once, whichever revisions, proposals, events and the
instance hold it, and the rows keep its ref, with the pointers in a
`value_refs` column its second migration adds. `listRevisions`,
`getRevision`, `listProposals` and `approve` read the values back. Its
third migration adds the proposals' `evidence`. Deleting the instance
releases what its revisions and proposals held.

#### Dependencies

Blockers between instances, which hold up the type's Workflow.

| | |
| --- | --- |
| Config | `schemas`: the schemas a blocker may be an instance of, each composing Workflow (the type's own when absent); `gatedStates`: the states of the type's Workflow a transition into waits for every blocker to finish, terminal or not (every terminal state when absent); `satisfiedBy`: the outcomes of a blocker's terminal state that finish it (`["success"]` when absent). Requires `Workflow` |
| Fields | `blocked`: whether a blocker is not finished, volatile. A list does not filter on it: it is computed from the blockers at each read, as the caller, and a copy would have to hear every blocker's status ("Filters") |
| Operations | `addBlocker({ schema?, id })` -> `{ schema, id, status?, open }`, writes; `removeBlocker({ schema?, id })` -> `{ schema, id }`, writes; `listBlockers({ limit?, cursor? })` -> a page of `{ schema, id, status?, open }`, read-only; `listDependents({ limit?, cursor? })` -> a page of `{ schema, id }`, read-only |
| Create parameters | `{ blockers?: [{ schema?, id }] }`, at most 500: the instance's blockers from its create, each added with `addBlocker`'s checks, against the Workflow's initial state: open until it finishes by `satisfiedBy`, and refused while open when the initial state is gated and no transition leaves it |
| Guards | a Workflow `transition` of the instance into a gated state, whoever asks, while `blocked`: `vetoed` (`blocked`), naming the open blockers, details `{ blockers: [{ schema, id, status? }] }` |
| Refusals | `addBlocker`: the instance itself, a schema the config does not list, one without Workflow, an instance that does not exist (`invalid_argument`); a blocker already added (`already_blocking`), an edge that would close a cycle (`cycle`), an open blocker of an instance in a gated state no transition leaves (`gated`), all `vetoed`. A create's blockers: the same, at `/behaviors/Dependencies/blockers/<i>/schema` or `/id` (`invalid_argument`), or `vetoed` with action `create`, the same code and details `{ path: '/behaviors/Dependencies/blockers/<i>' }`. `removeBlocker` of an instance that does not block it (`invalid_argument`). At define, a gated state the type's Workflow lacks (`invalid_schema`) |
| Deletes | deleting a blocker removes its edges: its reference, which hears the blocker's delete alone, invokes `removeBlocker` on each dependent, as the caller, each with its own event. Deleting a dependent deletes its edges |
| Events | `addBlocker`'s and `removeBlocker`'s operation events carry `Dependencies.blocked` when it changes |
| `configChange` | `schemas`, `gatedStates` and `satisfiedBy` may change (edges made before stay); added to a schema with instances, which start with none; not removed from one, since its edges and references would stay behind |

A blocker is finished once its status is a terminal state of its own
schema's Workflow config whose outcome `satisfiedBy` lists
(`stateOutcome` over what `schemas.config` returns), and open until then,
whatever the gated states say. With the default, a blocker that ends in a
`failure` or `neutral` state stays open: a failed check does not let the
step after it through. Removing it (`removeBlocker`) is how the work goes
on without it; a dependent that may go on after a blocker was cancelled
lists `neutral` too. A blocker whose schema declares no outcomes finishes
in any terminal state, as before outcomes existed. One function answers
that for the `blocked` field and the guard, over every blocker in every
schema, so they cannot disagree. The guard reads the state a transition
moves to from its `to`, the one parameter Workflow's closed
`paramsSchema` takes, and it runs before any handler for a caller's
transition, another behavior's `call()` and another instance's invoke
alike. `parseConfig` holds `gatedStates` to the states of the type's
Workflow, which it reads in `target.configs`.

A gated state need not be terminal. Gating `doing` holds the start of
work while a blocker is open, for a caller's transition as for a claim:

```json
{ "name": "Dependencies", "config": { "gatedStates": ["doing", "done"], "satisfiedBy": ["success", "neutral"] } }
```

An instance in a gated state that no transition leaves takes no open
blocker: its gate let it in with every blocker finished, it never moves
again, so `blocked` stays false. One in a gated state a transition
leaves, `doing` above, takes one: a dependency found during the work
holds up the instance's next move into a gated state, `done`, not the
state it is in.

A create's blockers are recorded in `initialize`, so the instance is
`blocked` in its create event and every other behavior's `afterChange`
sees the edges: a queue never finds it claimable before they exist. The
instance's status then is its Workflow's initial state, whichever of the
two the type lists first, and the rules above hold as they do for
`addBlocker`: a blocker is open until it finishes by `satisfiedBy`, an
initial state that is gated and that no transition leaves takes no open
blocker, and one a transition leaves takes any, which hold up its first
move into a gated state. An instance has no dependents at its create, so
the cycle a create's blockers can close is the instance blocking itself.

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
| Config | `links`: by camelCase name, `{ schema, required?, pinned?, many?, max? }`; at least one. `pinned` is `true` or `"revision"`, a revision of the target's `Revisions`; `"release"`, a release of its `Branches`; `false` or absent, nothing. `many: true` holds a list of targets, and needs `max`, from 1 to 100; `max` needs `many` |
| Fields | `targets`: `{ <name>: { schema, id, revision?, release?, latest?, stale? } }`, the links the instance holds, a list of those for a many-valued link in the order its targets were linked; absent when it holds none; volatile, since `latest` and `stale` read the target |
| Operations | `link({ name, id, revision?, release? })` -> `{ name, schema, id, revision?, release? }`, writes; `unlink({ name, id? })` -> the link as it was, writes; schema-level `listLinked({ name, id, stale?, limit?, cursor? })` -> a page of `{ id, revision?, release?, latest?, stale? }`, read-only |
| Create parameters | `{ <name>: id }` or `{ <name>: { id, revision?, release? } }`, a list of them for a many-valued link: the links the instance holds from its create, each set with `link`'s checks; every required link is among them |
| Guards | the delete of an instance a required link points at, whoever the caller, and of a required many-valued link's last target: `vetoed` (`required_target`, by `guardReference`) |
| Refusals | a name the config does not give, a target that does not exist, a `revision` for a link not pinned to a revision or past the target's latest, a `release` for one not pinned to a release or past the target's latest, a link pinned to a revision whose schema does not compose Revisions or to a release whose schema does not compose Branches (`invalid_argument`); a target with no revision yet (`no_revision`) or no release yet (`no_release`), a new target of a many-valued link that holds `max` (`too_many`), unlinking a required link or the last target of a required many-valued one (`required_link`), unlinking an `id` the link does not hold (`not_linked`), all `vetoed`; unlinking a link the instance does not hold, and a many-valued link without `id` (`invalid_argument`). A create without a required link, with a list for a link that holds one, one target for a many-valued link, a list past `max`, an empty list for a required one or a target twice, and a create's link that `link` would refuse, at `/behaviors/Links`, `/behaviors/Links/<name>` or `/behaviors/Links/<name>/<index>` (`invalid_argument`), or `vetoed` with action `create`, `link`'s code and details `{ path: '/behaviors/Links/<name>' }` (`<name>/<index>` for an item of a list) |
| Deletes | a deleted target leaves every link that holds it: its reference, which hears the target's delete alone, invokes `unlink` on each instance that holds it, `{ name }`, or `{ name, id }` for a many-valued link, as the caller, each with its own event; no other change of a target asks Links anything. Deleting an instance deletes its links |
| Events | a create's event carries the links it gives; `link`'s and `unlink`'s operation events carry `targets` under `Links`, a many-valued link's whole list |
| `configChange` | while the schema has instances, every link keeps its name and schema and whether it holds a list, and a many-valued link's `max` does not fall; `pinned` may change, to the other kind too, a required link may become optional, `max` may rise, and optional links may be added; a link that becomes required and a new required link are refused, as a field made required is. With no instance in any namespace that reads the schema, any change. Added to a schema with instances, which start with none, unless a link is required; not removed from one |

A link holds one target, an instance of its schema in the same namespace
(the schema looked up as any name is: the namespace, then the shared
one); `link` again moves it, and `unlink` clears it, or with `id` only
while it holds that target. A many-valued link (`many`, with `max`)
holds a list of up to `max` targets, a ticket's reviewers or a stylist's
services, in the order they were linked: `link` adds a target, or pins
again one it holds, which keeps its place, and `unlink({ name, id })`
removes one. A create gives it a list, each target once. A pinned link
pins a revision or a release:

```json
{ "name": "Links", "config": { "links": {
    "spec": { "schema": "documents", "pinned": true },
    "recipe": { "schema": "recipes", "pinned": "release" },
    "reviewers": { "schema": "people", "many": true, "max": 5 } } } }
```

- **What it records.** A link pinned to a revision records the target's
  latest revision, read through its `Revisions.revision` field, or the
  earlier one `link` names; one pinned to a release, its latest release,
  read through its `Branches.release` field (the release pointer's
  version), or the earlier one `link` names. Each kind is a
  column of its own in the row.
- **What it reports.** `targets` gives the pin, the target's latest of the
  same kind beside it (`latest`), and `stale` when the latest has moved
  past the pin, so a client shows "pinned 3, latest 5" with one read. A
  many-valued link reports each target so; the latest come from one read
  per target schema.
- **The other way round.** `listLinked` answers on the schema: which
  instances point a link at a target, each with its pin and the latest,
  and with `stale: true` only the pinned ones the target has moved past,
  which is how to find the instances that point at a superseded revision
  or release.

A required link is one every instance holds: every create gives it,
which refuses an instance without it, it can be moved, never unlinked,
and its target cannot be deleted while it points there; the refusal
names the linking schema, not the instance, which the caller may not be
able to read. A required many-valued link holds at least one target: a
create gives one or more, and its last target can be neither unlinked
nor deleted, while every other one can. A required link to the instance's own schema, a task's
parent task say, would leave a root nothing to point at, so a tree makes
its parent link optional. A pinned link is read as the caller: without
read on its target's schema, `targets` cannot be read. `stale` is computed
at each read, so a target's new revision or release shows in the next
read of the instances that link to it, with no event on them. A link made
before its spec was pinned, or while it was pinned to the other kind,
records no pin of the kind until it is linked again.

What reads one target of a link refuses a many-valued one, when its
schema is defined or published and again at run time, since another
schema's Links config can change after (D56): Reactions' `then.link` and
the links of `allTerminal`, `anyTerminal` and `holds`, Blueprint's
`parentLink`, `from.link` and `copyLinks`, Budget's `scope` and Queue's
`excludeStale`; `linkMany`, exported, reads whether a link of a config,
as a schema holds it, holds a list, as `linkPin` reads what it pins.
`listLinked`, Rollups, Reactions' `revised` and Lease's `directOn` read
across the list: an instance counts toward each target it holds, and
each target's move reaches the instances that hold it. A file made
before many-valued links keeps its links: Links' third migration builds
its table again, keyed by the target as well as the name, with every
row and its order.

#### Rollups

Values derived from the instances that point at this one through a link
of their schema's `Links` config: a parent's view of its children.

| | |
| --- | --- |
| Config | `rollups`: by camelCase name, `{ schema, link, function, field?, gatedStates?, outcomes? }`; at least one. `function` is `count`, `countBy`, `sum`, `min`, `max`, `latest`, `all` or `any`. `countBy`, `sum`, `min`, `max` and `latest` take `field`, the others none; only `all` and `any` take `gatedStates` and `outcomes` |
| Fields | `values`: `{ <name>: value }`, computed at each read, so volatile |
| Operations | none |
| Guards | a Workflow `transition` of the instance into a state an `all` or `any` rollup gates, whoever asks, unless the rollup holds: `vetoed` (`not_held`), naming the rollup and how many linked instances keep it from holding, details `{ rollup, to, over, linked, counted }`, `linked` and `counted` absent past the bound (`over: true`) |
| Refusals | at define and publish (`invalid_schema`): a gated state that is not a state of the type's Workflow, or a type without Workflow; a linked schema with no live version, without `Links` or the link, or whose link points at another schema; a `countBy` field that is not a string, enum or boolean field of its type, or `Workflow.status` when it composes Workflow; a `sum`, `min` or `max` field that is not a number or integer field; a `latest` field that is not a field of its type, or `Workflow.status` when it composes Workflow; `all` or `any` over a schema without Workflow. A caller who may not read the linked schema cannot define or publish the rollup (`forbidden`) |
| Events | none of its own: a linked instance's change appends no event on this one |
| `configChange` | nothing is stored, so rollups may be added, removed and changed, and Rollups added to or removed from a schema with instances |

```json
{ "name": "Rollups", "config": { "rollups": {
    "tasks": { "schema": "tasks", "link": "project", "function": "count" },
    "tasksByStatus": { "schema": "tasks", "link": "project", "function": "countBy", "field": "Workflow.status" },
    "tasksFinished": { "schema": "tasks", "link": "project", "function": "all", "gatedStates": ["done"] },
    "tasksFailed": { "schema": "tasks", "link": "project", "function": "any", "outcomes": ["failure"] } } } }
```

On a `projects` schema, with the `tasks` above, a project then reads
`"Rollups": { "values": { "tasks": 2, "tasksByStatus": { "doing": 1,
"todo": 1 }, "tasksFailed": false, "tasksFinished": false } }` under
`behaviors`, and cannot move to
`done` until every one of its tasks is done or dropped. With
`"outcomes": ["success"]` beside its `gatedStates`, `tasksFinished` would
hold only once every task ended in a success, so a task that failed
would keep the project from `done`.

| Function | Value | Over no instance |
| --- | --- | --- |
| `count` | how many instances point here | `0` |
| `countBy` | `{ <value>: count }` over the field's values, keys sorted; a boolean's are `"true"` and `"false"`; an instance with no value is not counted | `{}` |
| `sum` | the sum of the field over the instances that hold a value | `0` |
| `min`, `max` | the least or greatest value | absent |
| `latest` | the field's value on the instance created last, by `createdAt`, the greater id on a tie; absent when that instance holds none | absent |
| `all` | whether every one is in a terminal state of its schema's Workflow (`isTerminalState`), with an outcome `outcomes` lists when it is given (`stateOutcome`) | `true` |
| `any` | whether some one is | `false` |

The set is closed so that each function is one pass over the records it
reads, has one JSON type, and has a rule `parseConfig` checks against
the linked schema when the schema is defined; filters, averages or
expressions would make the config a query language. `latest`'s JSON type
is its field's, which `parseConfig` finds on the linked type; it orders
by creation, which every instance has, not by when it was linked or
last written, so a later link to an older instance does not change it.
`outcomes` is an argument of `all` and `any` from a closed set of three,
as `field` is of `sum`, not a filter: it says which terminal states the
two count.

A value is computed when the instance is read, as the caller (D16,
amended), and nothing is stored: a linked instance's change shows at
this instance's next read, with no event on it and no move of its `seq`
or `ETag`, and no reaction has to keep it current. For each schema and
link its rollups name, a computation reads the schema's `Links` config,
one page of `listLinked` on that schema (`instances.invokeSchema`), and,
for every function but `count`, the instances in one `getMany`, with
`Workflow.status` when a function needs it, and the schema's Workflow config for
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

Full-text search over the instance's own text fields, and with
`vectors`, vector search over embeddings an outside embedder computes
from the same text.

| | |
| --- | --- |
| Config | `fields`: the type's own top-level fields to index, by JSON key, 1 to 16, each a string or a scalar whose values are strings; `weights`: by indexed field, above 0 and at most 1000, 1 for a field it does not name; `vectors`, optional: `dimensions` (1 to 4096), `model` (a label the engine only compares) and `permission` (what an embedder needs to settle) |
| Fields | none: whether an instance is embedded is `staleEmbeddings`'s to say, since a settle appends no event |
| Operations | all schema-level. Read-only: `search({ query?, vector?, model?, syntax?, limit?, cursor? })` -> a page of `{ id, rank, score?, field?, snippet?, text?, vector? }`; `similar({ id } or { text, vector?, model? }, limit?, cursor?)` -> a page of the same and `embedded`; `staleEmbeddings({ limit?, cursor? })` -> `{ model, dimensions, items: [{ id, text, sourceHash }], next }`. Writing: `settleEmbeddings({ items: [{ id, sourceHash, vector }] })` -> `{ settled, skipped: [{ id, reason }] }` |
| Refusals | a field the type does not declare, or one that is not text (a number, an enum, a list, an object), and a weight for a field it does not index, when the schema is defined; a caller who may not `read` the schema, even one the policy lets call the operation (`forbidden`); an FTS5 expression FTS5 cannot parse, or one with a column filter (`invalid_argument`); a vector on a schema without `vectors`, of other dimensions, all zeros, with a value a 32-bit float cannot hold, or of a `model` other than the config's (`invalid_argument`); a settle by a caller without the config's permission (`forbidden`); `similar` of an instance the namespace does not have (`not_found`), with both or neither of `id` and `text`, or a `vector` without `text` (`invalid_argument`) |
| Events | none: the index and the vectors are the behavior's own, the index written with the change of the instance and a vector by a settle, which records who settled it and when |
| `configChange` | every change: `fields`, `weights` and `vectors` may change, and it may be added to or removed from a schema with instances |

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

The index is one FTS5 table, `bhv_search__text`, with a column per
indexed field in the config's order, and `bhv_search__rows`, which gives
each of its rows a namespace, a schema and an id, and with `vectors` the
hash of its text and its vector ("Vectors"). The tokenizer is
`unicode61` with diacritics removed, so `cafe` also finds the word with
an accent on its e, and case does not matter; there is no stemming. `afterChange` writes an
instance's row in the transaction of its create, of an update or a
writing operation that changes an indexed field (an approved revision
included), and deletes it with the instance, so a search never sees a
row the instances do not hold, and a write that fails takes its index
change back with it. It indexes a field the value store holds by its
value, read back, at a write and at a publish's rebuild alike.

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
drops the index, so a deleted field's text does not stay behind, and
with it every vector; a change of `weights` alone needs nothing, since
they apply when a search runs. One that adds `vectors`, or changes their
model or dimensions, hashes every instance's text again and clears every
vector, and one that removes them clears the hashes and the vectors; a
change of the permission needs nothing. A rebuild for new `fields`
leaves every instance stale. Each runs in the publish's transaction and
holds the write lock until it has visited every instance: on an Apple
M-series laptop, adding Search to a schema of 10,000 instances of about
160 words each took 0.4 s and changing its fields 0.5 to 0.9 s, on
Node.js and on Bun.

##### Vectors

The engine calls no embedding provider and loads no SQLite extension
(D16, amended: search's vectors). An outside embedder, a worker process
with the config's permission, keeps the vectors current:

```ts
const embed: Principal = { subject: 'embedder', permissions: ['notes.embed'] };
const page = engine.instances.invokeSchema(embed, 'notes', 'staleEmbeddings', { limit: 50 });
// { model: 'minilm-l6', dimensions: 384,
//   items: [{ id: 'n1', text: 'Release plan\n\nDates and owners.', sourceHash: '9c1f...' }], next: null }
const vectors = await provider.embed(page.items.map((item) => item.text));   // the embedder's own call
engine.instances.invokeSchema(embed, 'notes', 'settleEmbeddings', {
  items: page.items.map((item, i) => ({ id: item.id, sourceHash: item.sourceHash, vector: vectors[i] })),
});
// { settled: 1, skipped: [] }
```

- **What is stale.** An instance whose text has no vector: one created
  or written since its vector was settled, and every instance after a
  version adds `vectors` or changes the model or the dimensions. Its text
  is the indexed fields that hold more than whitespace, in the config's
  order, joined by a blank line; an instance whose fields hold nothing
  wants no vector. `staleEmbeddings` pages through them in the order they
  were first indexed, 50 at a time by default and at most 100, with the
  model and the dimensions to compute with. A cursor goes forward, so an
  instance that goes stale behind it waits for the next pass from the
  start.
- **The hash.** `sourceHash` is the SHA-256 of the text, the model and
  the dimensions. A write of an indexed field or a version that changes
  the model or the dimensions moves it and clears the vector, so a stored
  vector always matches the instance's text and the live config, and a
  search never ranks by text an instance no longer holds.
- **Settling.** `settleEmbeddings` takes at most 100 items. It stores
  each vector whose hash still holds, as little-endian 32-bit floats
  scaled to unit length, with who settled it and when, and skips the
  rest: `moved`, the text or the model changed since the pull (pull it
  again), or `not_found`. A vector of other dimensions, all zeros, with a
  value a 32-bit float cannot hold, or an id given twice refuses the
  whole batch (`invalid_argument`, at `/items/<i>/...`), so a defect in
  the embedder is not read as moved text. It needs the config's
  permission and `read` on the schema. The HTTP runtime's default body
  limit, 1 MiB, holds about 30 vectors of 1536 numbers; a deployment that
  settles larger batches raises `bodyLimitBytes`.
- **No event.** A settle appends no event and moves no `seq`: no read of
  an instance shows a vector, and its text and model are in the
  instance's events and the publish's. It changes what `search` and
  `similar` return, as a writing schema-level operation's own writes may
  ("Schema-level operations"). An event per instance would move each
  instance's ETag under an editor's `If-Match` and flood the log at every
  model change.

##### Ranking by a vector

A caller that wants vector ranking computes the query's vector with the
config's model and passes it, with the query or without:

```ts
engine.instances.invokeSchema(me, 'notes', 'search', { query: 'release plan', vector: queryVector, model: 'minilm-l6', limit: 20 });
// { items: [{ id: 'n2', rank: 1, score: 0.0328, field: 'title', snippet: [...],
//             text: { rank: 1 }, vector: { rank: 1, similarity: 0.83 } },
//           { id: 'n7', rank: 2, score: 0.0161, vector: { rank: 2, similarity: 0.79 } }],
//   next: null }
```

- **A vector alone** ranks the instances with a settled vector by cosine
  similarity, best first, then in the order they were first indexed.
- **A query and a vector** are fused by reciprocal rank fusion: a hit
  scores 1 / (60 + its place) in each ranking it is in, over the first
  200 of each (`RRF_K`, `RRF_WINDOW`), and the scores add; a tie goes to
  the better place by text, then by vector. So a hybrid search returns at
  most 400 instances.
- **Each hit says how it matched**: `score`, `text: { rank }` when the
  query matched it, with its snippet, and `vector: { rank, similarity }`
  when its vector ranked. A search by query alone answers as before, with
  no score.
- **The scan.** The vectors are ranked by a brute-force scan in the
  engine's process: every vector of the schema in the caller's
  namespace, read 512 at a time through a partial index, behind an
  internal interface an approximate index could replace. It runs
  synchronously, like a full-text search, and its cost grows with the
  schema's instances.

##### Similar instances

`similar({ id })` lists the instances nearest to one and leaves it out:
a full-text search for any of its 32 longest distinct words of three
characters or more, ranked by bm25, fused as above with its vector's
ranking once its vector is settled; `embedded` says whether it was. An
agent calls it before creating an instance like one it found, to reuse
or link a near-duplicate instead.

`similar({ text, vector?, model? })` does the same for an instance not
yet created: a draft's text, its indexed fields that hold more than
whitespace, in the config's order, joined by a blank line, as
`staleEmbeddings` gives an instance's, at most 100,000 characters. The
ranking is `similar(id)`'s, by the text's longest words and the vector
given with it, computed with the config's model from the same text,
with no instance to leave out; `embedded` says whether a vector ranked.
So an agent that is about to create finds what it would duplicate:

```ts
engine.instances.invokeSchema(me, 'notes', 'similar', { text: 'Release plan\n\nDates and owners.', vector: draftVector, model: 'minilm-l6' });
// { items: [{ id: 'n1', rank: 1, score: 0.0328, ..., text: { rank: 1 }, vector: { rank: 1, similarity: 0.97 } }], next: null, embedded: true }
```

##### Across schemas

`engine.search(principal, params, { namespace })`, the HTTP route `POST
/namespaces/{namespace}/search` and the MCP tool `search` search every
schema the namespace reaches that composes Search, each with its own
`search` as the caller, and merge the hits into one page of `{ schema,
id, rank, score, field?, snippet?, text?, vector? }`:

```ts
engine.search(me, { query: 'walnut', vector: queryVector, model: 'minilm-l6' }, { namespace: 'default' });
// { items: [{ schema: 'Note', id: 'n1', rank: 1, score: 0.0328, ..., text: { rank: 1 }, vector: { rank: 1, similarity: 1 } },
//           { schema: 'Task', id: 't1', rank: 2, score: 0.0164, ..., text: { rank: 1 } }], next: null }
```

- **Ranks, not scores, merge.** A hit scores as its schema's search
  scores it, 1 / (60 + its place) in each ranking of its schema, the
  first 200 of each, so one schema's best ties another's; a tie goes to
  the better place, then to the schema's name. bm25 and cosine do not
  compare across schemas, whose weights, statistics and models differ.
- **A vector needs its `model`** here, and ranks only the schemas whose
  vectors come from that model with its dimensions; the query ranks the
  rest, and without one they are left out.
- **What the caller may not read is skipped, not refused**: a schema the
  policy refuses `read`, or `search` on, and one whose behaviors this
  engine cannot run, as the tools document leaves them out. Refusing the
  whole search would make a policy that hides one schema end the
  namespace's search, and name a schema the caller may not see.
- The parameters are `search`'s, `query` or `vector` required;
  `SEARCH_SCHEMAS_PARAMS` is their JSON Schema. The MCP tool is listed
  where a schema the caller may read composes Search.

##### Search on Postgres

The Rust engine (D58) runs Search on both databases, its full-text index
the engine's own rather than SQL of the behavior's. On SQLite it is the
FTS5 table above, created and queried as this package does, so a file
this package wrote keeps working and a search answers as it does here,
order and snippets included. On Postgres a search finds the same
instances, but for the tokens below; it ranks and writes snippets
another way. Vectors, `similar`'s choice of words and the fusion are
Rust over the stored bytes, the same on both.

- **The index** is a table `bhv_search__text` of the same columns and a
  `rowid`, with a generated `tsvector` per column and one of every
  column (`all_tsv`) under a GIN index, through a text search
  configuration of its own (`bhv_search__text_config`): Postgres's
  default parser, then `unaccent` and `simple`, so case and diacritics
  fold as `remove_diacritics 2` folds them and nothing is stemmed or
  dropped, as in FTS5. A hyphenated word is indexed as its parts, a
  phrase of them, as FTS5 splits it.
- **Tokens the parser keeps whole.** Postgres's parser reads an email
  address, a URL, a host, a file path and a number with a point
  (`ann@example.com`, `example.com/a`, `3.14`) as one token where FTS5
  reads words, so on Postgres a search finds them by the whole token and
  not by a part (`ann`). And the columns' text runs together in
  `all_tsv`, so an FTS5 phrase can match across two fields there.
- **`unaccent`** is the database's extension where it has one, in any
  schema; otherwise the engine creates it, in a schema of its own,
  `superschematic_extensions`, shared by every engine of the database (a
  database holds an extension once, and an engine's schema may be
  dropped with all it holds). It is a trusted extension from Postgres 13,
  so a role with `CREATE` on the database may; where the role may not, a
  publish that adds Search fails and says so, and an administrator runs
  `CREATE EXTENSION unaccent` once. The official image and Cloud SQL ship
  it.
- **The query** is an FTS5 expression on both, plain words quoted as
  above. Postgres takes FTS5's common forms, translated to a `tsquery`
  before the statement runs, since an error inside it would end the
  call's transaction: phrases (`"walnut desk"`, `walnut + desk`), a
  prefix (`wal*`, the last word of a phrase), `AND`, implicit `AND`,
  `OR`, `NOT` and parentheses, with FTS5's precedence. `NEAR` groups and
  `^` are refused as `invalid_argument` at `/query` ("is not an FTS5
  expression this index takes: NEAR groups are SQLite's alone ..."), as
  is what FTS5 cannot parse, in FTS5's words where it has them (`fts5:
  syntax error near ...`, `unterminated string`).
- **The order** is `ts_rank_cd`'s, the cover density of the matches,
  over the fields' vectors each labelled with its weight's class, then
  the instance's first indexing. Postgres has four weight classes: the
  config's distinct weights, highest first, are A, B, C and D, and past
  four every lower weight shares D, weighed as the highest of them; each
  class weighs its weight over the highest (A is 1), since `ts_rank_cd`
  takes weights from 0 to 1. Weights `{ title: 3 }` over `title` and
  `body` are A at 1 and B at 1/3. The labels are given when a search
  runs, so a change of weights rebuilds nothing on Postgres either. The
  places differ from bm25's, which also counts how rare a term is and
  how long a field is; where the two agree (more matches first, a
  heavier field first, then the row) the scenario corpus compares them,
  and its other search steps are SQLite's alone (`dialect: sqlite`).
- **The snippet** is `ts_headline`'s, at most 12 words around the
  matches, between the same marks, so its parts read back the same way;
  `...` stands where it leaves out text of the field before or after, as
  FTS5's does. Its words are Postgres's choice and differ from FTS5's.

#### Reactions

Rules that move Workflow statuses after a change commits.

| | |
| --- | --- |
| Config | `rules`: one to 64, each one `when` and one `then`. `when` is `{ enters: <state> }`, `{ changes: { field, to?, from? } }`, `{ allTerminal: { schema, link, outcomes? } }`, `{ anyTerminal: { schema, link, outcomes } }`, `{ holds: <rollup> }` or `{ revised: { link } }`; `outcomes` is a list of `success`, `failure` and `neutral`. `then` is `{ transition: <state>, link? }`. Requires `Workflow` |
| Fields, operations | none |
| Reactions | `enters`: the instance's status became the state, by a create or a transition. `changes`: a create, an update or an operation of the instance moved `field`, an own field or a behavior's by qualified name with a member path after it allowed, to `to` and from `from` when they are given; a create moves a field from absent, a delete moves nothing. `allTerminal`: an instance of `schema` that links to this one through `link` changed or went, and every instance linking here through it is in a terminal state of its own schema's Workflow, with an outcome `outcomes` lists when it is given, at least one. `anyTerminal`: an instance of `schema` that links here through `link` entered a terminal state whose outcome `outcomes` lists, by a create or a transition, or was linked here while in one. `holds`: a change of an instance of the rollup's schema made an `all` or `any` rollup of the type's `Rollups` hold over at least one linked instance. `revised`: the instance `link` points to gained a revision of `Revisions`, or a release of `Branches`. `then` moves this instance, or the one its `link` points to, to the state |
| Refusals at define | a state the type's Workflow lacks, in `enters`, in a `changes` on `Workflow.status` or in a `then` on the instance itself; a `changes` field that is not an own field or a field a behavior of the type declares, a volatile one, and `Workflow.enteredAt`; a `then.link` or a `revised` link the type's Links lacks, or Links absent; a `holds` rollup the type's Rollups lacks, one that is not an `all` or an `any`, or Rollups absent; an `allTerminal` or `anyTerminal` on the type's own schema whose link does not point at it; a `then` on the instance itself that no transition of its Workflow allows; `enters` and `changes` rules on the instance itself whose states cycle; a `then.link`, or an `allTerminal`, `anyTerminal` or `holds` rollup's link of the type or of a live linked schema, that holds a list of targets |
| Failures at run | a target schema without Workflow, a state its Workflow lacks, an `allTerminal` or `anyTerminal` schema without Workflow or that does not link here through `link`, a `revised` link to a schema that composes neither Revisions nor Branches, a link a rule reads one target of that holds a list: the subscription retries, then halts |
| Events | each move is Workflow's `transition` operation event on the target, actor the runner's principal, `cause` the event that set it off |
| `configChange` | any; added to and removed from a schema with instances, since it keeps no state |

The rules run in order on each event the subscription hears: the
schema's own events, those of each `allTerminal` and `anyTerminal`
schema, of each `holds` rollup's schema and of each `revised` link's
schema. An `allTerminal` rule looks at the instance the event's instance
links to now and, after a delete or a change of its links, the one it
linked to before (`before`), and finds the instances linking there with
Links' `listLinked`, as the runner's principal. An `anyTerminal` rule
looks at the instance the event's instance links to now, and only when
the event moved its status into a terminal state with a listed outcome
or moved that link here while its status is one: a later change that
moves neither, an update say, does not set it off again.

`changes` hears a field of the instance itself move, its own or a
behavior's, by the field's qualified name (`Revisions.pendingProposals`,
`acme.Rating.count`), with a member path after it for a field whose
value is an object (`Lease.ended.reason`): the behavior is the longest of
the type's behavior names that prefixes it. A rule on `Workflow.status`
with `to` hears what an `enters` rule does, and one with `from` hears a
state left. A ticket escalated when its priority turns urgent, and
triaged at its first proposal:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "changes": { "field": "priority", "to": "urgent" } }, "then": { "transition": "escalated" } },
    { "when": { "changes": { "field": "Revisions.pendingProposals", "from": 0 } }, "then": { "transition": "triaged" } } ] } }
```

- A create moves each field it gives from absent, so `to` fires on a
  create and `from` never does; a delete moves nothing.
- The event says what moved. An operation's `patch`, and an update's
  `behaviors` part, are diffs of the instance's fields: a field they
  leave out has not moved, and a value they replace is the value now. A
  rule folds the instance before the event (`before`) only for `from`,
  for a field whose patch is an object, which a diff gives member by
  member, and for an own field of an update, whose `data` is the
  caller's patch rather than a diff; it folds once per event, however
  many rules ask. A value the event keeps in the value store is compared
  once loaded, never as its ref.
- `to` and `from` compare as JSON, an object's members in any order. A
  rule with `to` on the instance itself acts only while the field still
  holds it, as an `enters` rule acts only while the instance is still in
  its state; through a `then.link` it acts on the move alone.
- A volatile field moves at a read with no event of the instance, so a
  rule could wait for ever on one: `changes` refuses it at define, and
  `holds`, `allTerminal`, `anyTerminal` and `revised` hear what moves it.
  `Workflow.enteredAt` moves with every move of the status, which a rule
  on `Workflow.status` hears, and is refused too.
- A `changes` rule on `Workflow.status` on the instance itself joins the
  cycle check `enters` rules have: with `to` it moves on from that state,
  without it from each state a transition to its state leaves.

A parent that completes when its children all succeed and fails when
one fails says both on its own schema:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "allTerminal": { "schema": "steps", "link": "run", "outcomes": ["success"] } }, "then": { "transition": "completed" } },
    { "when": { "anyTerminal": { "schema": "steps", "link": "run", "outcomes": ["failure"] } }, "then": { "transition": "failed" } } ] } }
```

Both rules run in the parent schema's one subscription, on each child's
event in log order, and `allTerminal` reads every child as it is when it
runs. A child in a terminal state stays there, so `completed` needs
every child to have succeeded, and the parent never completes while a
child has failed, whatever order the children's events, the rules and
the runner's passes come in; the `anyTerminal` rule fails it instead. A
rule on the child that fails its parent, `enters: failed` with a `then`
through the parent link, runs in the child schema's subscription, which
may run before or after the parent's: beside `allTerminal` without
`outcomes`, the last child failing could complete the parent or fail it,
by which ran first. With `outcomes: ["success"]` it cannot complete it.
A child that ends `neutral` holds up the rule above; list `neutral` with
`success` for a parent that completes past cancelled children.

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

`holds` names an `all` or `any` rollup of the type's `Rollups` and fires
on the edge from not holding to holding. An event of the rollup's schema
sets it off on the instance its instance links to through the rollup's
link, now and before the event, when the event is what makes the
rollup hold:

- it holds now, over the linked instances as they are when the rule
  runs, so an event a later change has undone fires nothing;
- it holds with the event's instance as the event left it, the others as
  they are now, so the fire is the event's that made the edge, not an
  earlier event of the same instance the runner handles later;
- it does not hold with that instance as it was before the event
  (`before`), linked here or not and in its status then.

A rollup that held already does not fire again until it has stopped
holding, whatever its linked instances do meanwhile: a run whose
`any`-of-failures rollup fired, retried by hand, is not failed again by a
second failed step while the first is still failed. The value is the
rollup's, read as `Rollups` reads it: one `listLinked` page of at most
`MAX_ROLLUP_READ` (500), their statuses, and the linked schema's
Workflow; past the bound it does not hold. One rule differs from the
rollup's own value: an `all` over no instance holds for `Rollups`' gate,
but sets no rule off, as `allTerminal` needs one instance, so deleting or
unlinking the last instance that kept it from holding fires nothing.
Two events the runner handles together after both committed, the last
two steps passing say, can each find the edge, as the others are read
now; the second finds the target in the state already and leaves it.

```json
{ "name": "Rollups", "config": { "rollups": {
    "stepsPassed": { "schema": "steps", "link": "run", "function": "all", "outcomes": ["success"] },
    "stepFailed": { "schema": "steps", "link": "run", "function": "any", "outcomes": ["failure"] } } } },
{ "name": "Reactions", "config": { "rules": [
    { "when": { "holds": "stepsPassed" }, "then": { "transition": "completed" } },
    { "when": { "holds": "stepFailed" }, "then": { "transition": "failed" } } ] } }
```

A `holds` rule hears only the rollup's schema, so a move of the instance
itself sets one off only through another instance that links to it: up
a tree of the type's own schema, each parent completing as its last
subtask does, one depth further each step, which the runner's depth
limit stops. `parseConfig` refuses nothing more for it.

`revised` names a link of the type's `Links` and fires on each instance
whose link points at an instance that gains a revision or a release:

- a revision of `Revisions`: an update or an operation event of the
  target whose change carries `revision`, an approval say; a comment or
  a transition carries none;
- a release, when the target's schema composes `Branches`: its
  `releaseCommit` operation event.

The instances it moves are the ones `Links`' `listLinked` finds on the
type's own schema pointing at the event's instance, as the runner's
principal. For a link pinned to a revision and a revision, or pinned to
a release and a release, only the ones the target has moved past
(`stale: true`), so one linked to the new revision or release before the
runner hears it is left alone; any other link moves every one at each
revision and each release. A typical use moves work whose spec changed back to
review:

```json
{ "name": "Reactions", "config": { "rules": [
    { "when": { "revised": { "link": "spec" } }, "then": { "transition": "review" } } ] } }
```

No transition makes a revision or a release, so a `revised` rule never
sets itself off; its moves chain only through other rules, which the
depth limit stops.

The rule for what moves a link's target on, and which instances it moves
on, is exported for a behavior of another package to hear the same way,
as `Lease`'s `directOn` does: `targetMove(scope, behavior, form, link,
schema, event)` reads an event as a `{ kind: 'revision', revision }` or a
`{ kind: 'release', release, commit }`, `release` the pointer's version
the release made, or undefined, and refuses a schema with neither
`Revisions` nor `Branches` (a `BehaviorError`); `movedOn(scope, link,
pin, id, move)`, `pin` as `linkPin` reads the link, lists the instances
of the scope's schema it moves on, through `listLinked`, and `linkedTo`
lists them with or without `stale`.

#### Constants

Fields of the type that its create sets and nothing changes after.

| | |
| --- | --- |
| Config | `fields`: 1 to 64 of the type's own top-level fields, by JSON key; `permission`: the permission a caller needs to change them after the create, optional |
| Fields, operations | none |
| Validates | an update, a caller's or an operation's `update()`, that changes a listed field: the rule `constant` at the field, unless the caller holds `permission` (`invalid_instance`). A create is never refused |
| Refusals at define | a field that is not one of the type's own (`invalid_schema`); a behavior's field is not one, and changes only through the behavior's operations |
| `configChange` | any; added to and removed from a schema with instances, since it keeps no state |

Work is routed by what an instance is: a step's kind, the key a
blueprint stamped it with, the fields it copied from its parent. When
any writer can change them, the holder of a lease included, a worker can
turn the step it holds into another one. A listed field keeps the value
its create gave it:

- A value is compared as JSON, so a change inside an object or a list is
  a change, and the same value again is none.
- A field the create leaves absent stays absent: setting it later is a
  change. Absent and null are one value, as a merge patch has it.
- An operation's `update()` is held to it as a caller's update is, so a
  `Revisions` approval cannot rename a step, and `validateUpdate()`
  reports it, so neither can a proposal.
- A caller with `permission` may change, set or remove the fields; the
  permission is asked of the caller, an operation's included.

`Constants` is the general rule: a field nothing changes after the
create, but a caller with its permission. `Lease`'s `maxHoldField`,
`Budget`'s `limitField`, `Presence`'s `principalField` and `Retries`'
`limitsField` keep rules of their own, in their guards, because each
rule reads state a `validate` cannot (who holds the lease, what is used
and reserved). `Retries` guards its caps itself because the holder
of the instance's active lease must never raise them, with
`limitsPermission` or without (`limits_fixed`), where `Constants`'
permission would let any caller who holds it; others need
`limitsPermission`, so an operator can still give a stuck job room. A
type may list such a field in `Constants` too: then a change needs
`Constants`' permission as well as passing the behavior's own rule.

#### Variants

A field typed by another field's value.

| | |
| --- | --- |
| Config | `field`: an own field whose values are open JSON objects (`Generic.JSON`, or a scalar whose values are objects); `by`: an own string or enum field; `types`: by a value of `by`, 1 to 256, a type of the document besides the instance type |
| Fields, operations | none |
| Validates | while `by` holds a value `types` lists, `field`, when it holds one, against that value's type, strictly (`checkType`); while `by` holds another value or none, `field` holds none (rule `variant`). Both on a create and an update (`invalid_instance`) |
| Refusals at define | a `field` or `by` that is not one of the type's own, the two the same, a `field` whose values are not open objects (a string, a list, a type of the document), a `by` that is not a string or an enum, a type that is not one of the document's besides the instance type, a value that is not a member of `by`'s enum, and a type whose fields the schema runtime cannot check (`invalid_schema`) |
| Describe | an `if`/`then` per listed value and one for every other value, under the instance's `allOf`, in create's `data` and, in patch form, in update's `patch` |
| `configChange` | while the schema has instances, `field` and `by` stay and each listed value keeps its type, and another value may gain one; with none, any change. Removed from a schema with instances, not added to one |

A step's result has a different shape for each kind of step. The engine
refuses a union field, which the schema runtime does not check, so
without this `result` is `Generic.JSON` and a result in the wrong shape
is stored without a word. Paired with `Constants`, the kind and the
result's shape stay what the create gave them:

```json
"types": {
  "Step": { "name": "Step", "role": "EmbeddedStruct",
    "behaviors": [
      { "name": "Constants", "config": { "fields": ["kind"] } },
      { "name": "Variants", "config": { "field": "result", "by": "kind",
          "types": { "verify": "VerifyResult", "review": "ReviewResult" } } } ],
    "fields": [
      { "name": "title", "typeRef": { "name": "string" }, "required": true },
      { "name": "kind", "typeRef": { "name": "StepKind" }, "required": true },
      { "name": "result", "typeRef": { "name": "Generic.JSON" } } ] },
  "VerifyResult": { "name": "VerifyResult", "role": "EmbeddedStruct",
    "fields": [{ "name": "passed", "typeRef": { "name": "boolean" }, "required": true }] },
  "ReviewResult": { "name": "ReviewResult", "role": "EmbeddedStruct",
    "fields": [{ "name": "approved", "typeRef": { "name": "boolean" }, "required": true }] }
}
```

A schema with more than one type names its instance type, so this one is
named `Step` ("Schemas"). A verify step's result `{ "passed": "yes" }`
is refused with `type` at `result.passed`, and `{ "passed": true,
"by": "ci" }` with `unknown` at `result.by`; a note's result, `note`
being a kind `types` does not list, with `variant` at `result`. An
update is held to it as merged, so a patch that gives a review step's
result changes only the members it names.

- **A value without a type.** While `by` holds a value `types` does not
  list, or none, `field` holds none. A later version can give that value
  a type, since no stored instance holds a value for it; a required
  `field` admits only the listed values.
- **Versions.** The types `types` names are held to the compatibility
  rule as types a field reaches: a new version cannot add a required
  field to `VerifyResult` or change one's type, while it may add an
  optional field, and change a type nothing reaches freely. A version
  without `Variants` checks them no more, so it may change them.
- **What a client sees.** The describe document's `instance` carries,
  under `allOf`, `{ if: { properties: { kind: { const: "verify" } },
  required: ["kind"] }, then: { properties: { result: <VerifyResult> } } }`
  for each value, and `{ if: { not: <kind is a listed value> }, then: {
  properties: { result: { type: "null" } } } }`. The create tool's `data`
  carries the same, and the update tool's `patch` a patch form, whose
  types require nothing and whose `if` needs `kind` in the patch.

#### Branches

A version graph on each instance (D32, over D17 and D19): the instance is
the graph's root, never overlaid and in no commit, so its own fields stay
outside the graph, and rows of the kinds the config names live on refs, a
primary line and drafts of it.
`releaseCommit` is the version graph's release: it points the instance's
release pointer at a tagged commit, and the pointer's history is the
release log, which `releases` reads.

| | |
| --- | --- |
| Config | `kinds` (1 to 64, by name, camelCase): each `{ type, parent?, order?, singleton?, units?, retentionDays? }`, `type` a type of the document besides the instance type, whose fields are the kind's content, `parent: { key, of }`, `order` and `singleton` as `@graphMember`'s, `units` each field's conflict unit as `@conflictUnit` sets it (`atomic`, `keyed`, `jsonSchema`, `excluded`), `retentionDays` as `@versioned`'s; `primary`, the primary line's name (`main`); `snapshotEvery`, as `@versionGraph`'s (64); `sweep: { intervalMs, discardGrace?, pruneBatch?, abandonAfter? }`, milliseconds but `pruneBatch`; required |
| Fields | `release`: the number of the instance's latest release, the release pointer's version; absent before the first |
| Operations | writing: `branch`, `save`, `commit`, `seal`, `merge`, `rebase`, `revert`, `releaseCommit`, `discard`; read-only: `refs`, `releases`, `compose`, `materialize`, `released`, `diff`, `history`; all of instance scope |
| Storage | the version graph's fixed layout (the SQLite adapter's, `sqliteLayout` of `@superschematic/versiongraph/sqlite`, byte for byte, and on Postgres the same tables without a trigger) under the behavior's names, `bhv_branches__ref` to `bhv_branches__member_history`, beside `roots` (each root's instance) and `actors` (each actor's subject) |
| Schedule | `sweep`, every `sweep.intervalMs` on a schema whose config gives `sweep`, off on any other |
| Vetoes | the version graph engine's codes: `version_conflict`, `name_taken`, `ref_sealed`, `primary_merge_only`, `nothing_to_commit`, `entity_not_found`, `invalid_tree` (the core's findings in `details.findings`), `merge_into_itself`, `no_parent`, `not_tagged`, `walk_ceiling`; and `primary_line`, a `discard` of the primary line |
| Refusals at define | a kind whose type is no type of the document besides the instance type; a field whose JSON key is a role or audit column's; a field of a scalar no value class reads (`Geo.Location`, a JSON object stored as `POINT`); a parent that is no kind of the config, or whose key is not a field of the kind's type holding a UUID; an order that is not an integer field; a unit on a field the type lacks, a `keyed` or `jsonSchema` unit on a field that is not JSON, an excluded order or parent key; and what else the version graph's core refuses in the descriptor (`invalid_schema`) |
| `configChange` | a kind may be added, and a kind's fields change as the compatibility rule lets a field change; a retention, `primary`, `snapshotEvery` and `sweep` may change, and a unit be given a field the old type lacked; removing a kind, or changing a kind's type, parent, order, singleton or a field's unit, is refused while the schema has instances, and allowed with none, since deleting an instance deletes its graph. Added to a schema with instances, not removed from one. A field a version adds moves no earlier commit's hash ("A field a version adds") |

| Operation | Takes | Returns |
| --- | --- | --- |
| `branch` | `fromRef?`, `name` | the draft, whose base is `fromRef`'s head; without `fromRef`, a draft of the primary line |
| `save` | `ref`, `version`, `edits` (by kind: `upsert` rows, `delete` and `unset` entity keys) | `{ ref, saved }`: the draft at its new version and the rows stored |
| `commit` | `ref`, `version`, `message?`, `tag?` | `{ ref, commit }` |
| `seal` | `ref`, `version` | `{ ref, commit }`, `commit` null when there was nothing to commit |
| `merge` | `source`, `target`, `targetVersion`, `resolutions?`, `message?`, `tag?` | `{ ref, commit, conflicts }` |
| `rebase` | `draft`, `version`, `resolutions?` | `{ ref, commit, conflicts }` |
| `revert` | `ref`, `version`, `toCommit` | `{ ref, commit }` |
| `releaseCommit` | `commit`, `version` (the release pointer's, the `release` field, 0 for the first) | `{ commit, version }`: the release pointer moved to the commit |
| `discard` | `ref`, `version` | the draft, discarded; the primary line is not (`primary_line`) |
| `refs` | `limit?`, `cursor?` | the instance's live refs, the primary line first, a page at a time |
| `releases` | `limit?`, `cursor?` | the release log: `{ version, commit, releasedAt, releasedBy }` per version of the pointer, oldest first |
| `compose` | `ref` | `{ tree, contentHash, findings }` of the ref, uncommitted work included |
| `materialize` | `commit` | `{ tree, contentHash, findings }` of the commit |
| `released` | nothing | `{ release, tree, contentHash, findings }`; `not_found` before the first release |
| `diff` | `from`, `to` | `{ changes }`, each `{ kind, entityKey, operation, row? }` |
| `history` | `ref` | `{ commits }` the ref wrote, newest first |

A ref is `{ id, name, parent, base, head, sealed, discarded, version,
createdAt, createdBy, updatedAt, updatedBy }` and a commit `{ id, ref,
parent, message, sequence, contentHash, createdAt, createdBy, snapshot }`,
times as UTC date-times; a tree is rows by kind.

- **The descriptor.** `parseConfig` derives the graph's descriptor
  (version 3) from `kinds` and the types it reads through
  `ConfigTarget.types` ("Other types"), so the version's checks cover the
  kinds' types and the compatibility rule keeps them as it keeps the
  instance type's. A kind's role and audit columns have fixed names:
  `id`, `entity_key`, `ref_id`, `root_id`, `deleted_on_ref`, `_version`,
  `created_at`, `created_by`, `updated_at` and `updated_by`, the author,
  which conflicts report and a delete's image names as its actor; images
  exclude nothing, and the root's column and the other audit columns are
  not content. Each field is a column under its JSON key, of the value
  class graphdesc gives a compiled field: a primitive's from its name
  (`string`, `String` and `ID` are `string`, `Int` `integer`, `number`
  and `Float` `number`, `boolean` and `Boolean` `boolean`), an enum's
  `enum`, a nested type's `json`, a catalog scalar's the scalar catalog's
  (`BUILTIN_SCALAR_VALUE_CLASSES`), whatever the document declares for
  it, as the engine validates it, a scalar the document defines the one
  its JSON type gives (`jsonType`: `string`, `integer`, `number`,
  `boolean`, and `json` for an object, an array or any value), and each
  list level adds `[]`. The core checks the descriptor at define.
- **Rows.** `save` holds each upsert row's content, the row less
  `entity_key`, to its kind's type with `validate(type, value)` before
  the engine sees it: a JSON object, each field's value, no key the type
  does not declare. It then holds each value to its column's value class,
  as the adapter canonicalizes it, which a value the type accepts can
  still miss: 1.5 of a document scalar whose JSON type is `integer`.
  Either refuses the save (`invalid_argument`, each issue at a JSON
  pointer such as `/edits/step/upsert/0/position`). A row is an entity's
  whole content: a field it leaves out is stored null, and an
  `entity_key` replaces that entity on the draft. A read returns rows
  with their role and audit columns, ids in their canonical form.
- **Resolutions.** A `merge` or a `rebase` takes `resolutions`, each
  `{ kind, entityKey, path }` with `take` (`base`, `ours` or `theirs`)
  or a `value`; a whole entity's conflict (path `""`) is settled with
  `take`. A value is held to the value class of the field its path names
  before the engine merges, and once it has merged, each entity a value
  settled is held, as the ref composes it, to its kind's type, as `save`
  holds a row. A refusal is `invalid_argument` at
  `/resolutions/<i>/value`, and the operation writes nothing.
- **Refs and commits.** Each is named by id, in either UUID form, and must
  be the instance's: another instance's, a discarded ref or one that does
  not exist is `invalid_argument` at the parameter. A primary line takes
  writes only from `merge`; work happens on a draft, which `rebase`
  brings up to its parent's head.
- **Actors and roots.** The actor of a write is the version-5 UUID of
  the principal's subject in `ACTOR_NAMESPACE`
  (`e5df4b2e-719d-4507-8d3a-9474ef4afe81`), recorded in `actors`, and
  every read returns the subject: a ref's and a commit's `createdBy`, a
  row's `created_by` and `updated_by`, a conflict's authors, a release's
  `releasedBy`. A root's id is the version-5 UUID of its instance's id in
  `ROOT_NAMESPACE` (`cc634206-0f43-4e6c-a60f-65184f26131e`), since an
  instance id is any string. Both are exported, with `uuidV5`.
- **The graph's tables.** The behavior's migration creates the
  adapter's statements under `sql.table`'s names. The adapter runs in the
  caller's transaction and reaches the tables only through the call's
  `sql`, whose checks its statements pass: one statement at a time, on
  the behavior's own tables, with no trigger and no transaction control.
  So an invoked operation's savepoint rolls the graph's writes back with
  the rest. A graph is named by its namespace and schema
  (`default/Recipe`), so the tables hold every schema's graphs. Each
  commit records schema epoch 0, which every version keeps.
- **The primary line.** `initialize` creates it, named `primary`, as the
  creator. An instance created before its schema composed `Branches`
  gets it at its first write after, in that write's transaction, as its
  caller: a `branch` without `fromRef`, which then branches from it, so
  even a schema whose only writing behavior is `Branches` gives such an
  instance its line; an update that changes the instance; or another
  behavior's writing operation. Every other `Branches` writing operation
  names a ref or a commit, which the instance has none of until then, and
  a refused operation rolls back the line it made with the rest.
  `discard` refuses the primary line (`primary_line`).
- **The sweep.** On a schema whose config gives `sweep`, each run, as the
  runner's principal, invokes `discard` on each instance for each draft
  with no write for `abandonAfter`, so each runs its guards and appends
  its event (a vetoed one stays), and then runs the version graph's sweep
  with abandoning off, writing only the behavior's tables: it deletes the
  rows of refs discarded longer ago than `discardGrace` (seven days when
  absent), whose ref rows and commits stay, prunes each kind's history
  past its `retentionDays`, at most `pruneBatch` images a kind, keeping
  every image a commit or a snapshot pins, and writes every snapshot the
  graph's rules call for and it lacks.
- **A field a version adds.** Every stored row reads a field a new
  version adds to its kind's type as null, as a Postgres row reads a
  column added after it was written. The content hash leaves out a null
  content column, as it leaves out one a row lacks, so a commit's tree
  hashes as it did before the version: `materialize` of a commit made
  before returns the `contentHash` the commit stored, which `history` and
  a commit's own record return, and `released` and `compose` of the
  primary line give it too. A value written to the field is content and
  moves the hash. A commit compares trees, not stored hashes, so
  `nothing_to_commit` holds as before.
- **Deleting.** Deleting an instance deletes its graph: its refs,
  commits, patches, snapshots, rows, release pointer and their history.
- **The core.** `Branches` runs the version graph's Rust engine (D32,
  D58) over its fixed layout in the database's dialect, each statement
  through the call's `sql`: on SQLite the TypeScript adapter's layout byte
  for byte, so a file the TypeScript engine wrote serves this one, and on
  Postgres the same tables without a trigger. Its ids come from the
  engine's own source, so a seeded engine draws them as the TypeScript
  engine did.
- **Lease.** The operation that points the release pointer is
  `releaseCommit`, not `release`, because `Lease` has `release`.
- **The release field.** `release` reads the release pointer's version
  from the behavior's own table at each read, so a `releaseCommit`'s
  event carries it in its `patch`, a rollback included. `Links` pins a
  release through it, as it pins a revision through `Revisions`'
  `revision`, and `Queue`'s `excludeStale` hears it move.

#### StateRules

The own fields each state of the type's Workflow requires to enter it,
and the ones it freezes while the instance is in it (D56).

| | |
| --- | --- |
| Config | `states`: 1 or more, by a state of the type's Workflow, each `{ required?, frozen? }`, at least one: `required`, 1 to 64 of the type's own top-level fields, by JSON key, the instance must hold to enter the state and keeps while it is in it; `frozen`, 1 to 64 of them that do not change while it is in it, or `"all"`, every own field; `permission`: the permission a caller needs to change a frozen field, optional. Requires `Workflow` |
| Fields, operations | none |
| Guards | a Workflow `transition` into a state, whoever asks, while a field the state requires is not held: `vetoed` (`missing_fields`). An update, a caller's or an operation's `update()`, while the instance is in a state: one that clears a field the state requires and the instance held, `vetoed` (`required_fields`), whoever asks; one that changes a frozen field, `vetoed` (`frozen_fields`), unless the caller holds `permission`. Details `{ state, fields }` |
| Validates | a create, when the Workflow's initial state requires a field it does not give: the rule `required` at the field (`invalid_instance`) |
| Refusals at define | a state the type's Workflow lacks, a field that is not one of the type's own (`invalid_schema`); a behavior's field is not one |
| `configChange` | any; added to and removed from a schema with instances, since it keeps no state |

A record's fields mean more as it moves on: an invoice is drafted with
any amount, sent only with a customer and an amount that then stay, and
paid with nothing left to change. A `validate` sees the fields and not
the status, so each rule sits where it reads what it needs:

- **Entering a state.** The guard reads the state a transition moves to
  from its `to`, as `Dependencies`' gate does, and the fields from the
  view, so a caller's transition, another behavior's `call()`, another
  instance's invoke and the runner's moves are held alike, and an
  operation that updates the fields and moves the status in one call is
  judged on the fields its `update()` left.
- **The initial state.** A create starts in the Workflow's initial state,
  which `parseConfig` reads from the type's Workflow config, so
  `validate` refuses a create that leaves out a field that state
  requires, before anything is stored.
- **In a state.** The update guard reads the instance's status through
  `Workflow.status`, as the caller, only when a field a rule names
  changes, and compares the fields before the update with the ones after.
  Clearing a required field is checked first, since no permission lifts
  it. A behavior's write is held as a caller's is: a `Retries` result in
  a frozen field refuses its attempt.
- **Held.** A field is held when it is neither absent nor null, as a
  merge patch has it: `""`, `0` and `[]` are held. A value is compared
  as JSON, so a change inside an object or a list is a change. The
  fields a veto names are in the order the type lists them for `"all"`
  and the config's otherwise.
- **Instances from before.** An instance that predates a requirement
  keeps working: the update guard refuses clearing only a required field
  the instance held, so an update that leaves a missing field missing
  passes, and the requirement binds when the instance next enters the
  state.
- **Revisions.** `propose` checks its patch with `validateUpdate()`,
  which asks no guard, so a proposal that changes a frozen field is
  accepted and its `approve` is vetoed (`frozen_fields`), whose
  `update()` the guard asks. The guidance says so on `approve`.

#### Approvals

Sign-offs that gate states of the type's Workflow (D56): a transition
into a gated state waits until `count` distinct principals holding the
gate's permission have signed off.

| | |
| --- | --- |
| Config | `gates` (at least one, by state of the type's Workflow): each `{ count, permission, from?, excludeCreator? }`, `count` 1 to 100, `from` the states a transition from is gated (every state when absent), `excludeCreator` whether the instance's creator is refused (`true` when absent); `resetOnChange`, whether a change of the own fields consumes every sign-off (`true` when absent); required. Requires `Workflow` |
| Fields | `gates`: `{ <state>: { signedBy } }`, the subjects whose sign-offs count, in the order they signed; a gate with none is absent, and with none at all it is `{}`. No count: the config holds it, and a publish that changed it writes no event |
| Operations | `signOff({ state, note? })` -> `{ state, subject, note?, at, signedBy, count }`, writes; `withdrawSignOff({ state })` -> the sign-off, `{ state, subject, note?, at }`, writes; `listSignOffs({ state?, limit?, cursor? })` -> a page of `{ state, subject, note?, at, consumedAt? }`, the consumed ones included, read-only |
| Guards | a Workflow `transition` of the instance into a gated state, from a state its `from` allows, whoever asks, with fewer sign-offs than `count`: `vetoed` (`approvals_missing`), details `{ state, required, count }`, `required` the gate's count and `count` the sign-offs it has. A move Workflow refuses is left to Workflow's veto, whichever of the two the type lists first |
| Refusals | `signOff` and `withdrawSignOff` of a state the config does not gate (`no_gate`); `signOff` without the gate's permission (`can`), `forbidden`, as `Revisions`' `approve` is; while the instance is in the state (`in_state`); by the instance's creator, the `created_by` of its row (`sql.instances()`), on a gate that excludes it (`creator_cannot_sign`); by a caller whose sign-off for the state counts already (`already_signed`); `withdrawSignOff` with no such sign-off (`not_signed`); all but `forbidden` `vetoed`. At define, a gated state or a `from` state the type's Workflow lacks, or a gate's own state in its `from` (`invalid_schema`) |
| Storage | `signoffs`: a row per sign-off, unique among the ones that count per instance, state and subject, with the time it was consumed |
| Events | `signOff`'s and `withdrawSignOff`'s events carry `gates`; so do the update or operation, and the transition, that consume sign-offs |
| `configChange` | gates may be added, removed and changed: a removed gate's sign-offs are consumed (`afterConfigChange`), and a gate's new count or permission applies to the sign-offs already given. Added to a schema with instances, which start with none; not removed from one, since its sign-offs would stay behind |

A sign-off counts until it is consumed, and `afterChange` consumes them
from the instance as the change leaves it, in the change's transaction,
never from which operation ran:

- **A change of the own fields.** An update, or an operation that changes
  them with `update()` (a `Revisions` approval, say), consumes every
  gate's sign-offs, so what was signed is what moves. `resetOnChange:
  false` keeps them; an operation that changes no own field, a comment or
  a proposal, consumes none.
- **Entering a gated state.** An instance in a gated state has that
  gate's consumed, so a return to the state is signed again. It reads the
  status, not the operation: a `Reactions` move on the runner, a `Lease`
  expiry or a `Timers` fire moves the status inside an operation of its
  own, and consumes them as a caller's transition does.

A sign-off is one principal's, by its subject, so a principal counts once
toward a gate whatever its permissions; `signOff` asks `can()` for the
gate's permission at the time it signs. A withdrawal deletes its row, so
`listSignOffs` shows what was given and consumed, and the log keeps every
`signOff` and `withdrawSignOff`. Deleting the instance deletes its
sign-offs. On a type that also composes `Lease`, `signOff` and
`withdrawSignOff` are writing operations of another principal than the
holder: `"Approvals.signOff"` and `"Approvals.withdrawSignOff"` belong in
Lease's `exempt` (`runtime/engine-workqueue/README.md`, "The guard").

#### Numbering

A number a person reads on each instance (D56): an invoice's
`INV-2026-0042`, a ticket's `T-17`.

| | |
| --- | --- |
| Config | `format` (required, 1 to 128 characters): text with `{seq}`, or `{seq:N}` zero-padded to N digits (1 to 10), exactly once, and `{yyyy}` and `{yy}`, the UTC year the instance was created in, as often as wanted; `scope: { link }`, a link of the type's `Links` config, required and holding one target, whose each target counts on its own; `reset: "yearly"`, which starts each count again at 1 every UTC year and needs the year in `format` |
| Fields | `seq`, the number in its count, from 1, and `label`, the number as `format` prints it; both absent until the instance is numbered. A list filters on each (`where: { 'Numbering.label': 'INV-2026-0042' }`, `'Numbering.seq'`), through an index on its column |
| Operations | `assignNumber()` -> `{ seq, label }`, writes: numbers an instance made before its schema composed `Numbering`; on one numbered already, `unchanged()` |
| Storage | columns `number` and `label` on the instance, each indexed; `counters(namespace, schema, scope_key, last)`, the last number of each count; `labels(namespace, schema, id, scope_key, label)`, unique on `(namespace, schema, scope_key, label)` |
| Refusals at define | a brace outside a token, a token it does not know, a `{seq}` not there once, `reset` without the year in `format`, and a `scope` link that is not a link of the type's `Links`, is optional or holds many targets (`many: true`, read from the `Links` config as the type gives it), all `invalid_schema` |
| Events | a create's event carries the number under `Numbering`; `assignNumber`'s operation event, `patch: { behaviors: { Numbering: { seq, label } } }` |
| `configChange` | added to a schema with instances, which hold no number until `assignNumber`; while the schema has instances, `format`, `scope` and `reset` stay, and it is not removed: added again, it would count from 1. With none, any change, and the counts start again at 1 |

The count runs per namespace and schema; with `scope`, per target of
the link; with `reset`, per UTC year too. A count is a row of the
behavior's own `counters`, keyed by the target's id and the year, with
a `/` between, which no id holds.

- **In the create's transaction.** `afterChange` of the create sets the
  number, after every behavior's `initialize`, so the scope link the
  create gives is held whatever the list order. A create a later
  behavior refuses rolls the count back with it, so a refused create
  takes no number and leaves no gap. A delete leaves one: the next create
  takes the next number, and the deleted instance's label row goes.
- **Unique within its scope.** Only `{seq}` varies in width, and it is
  there once, so a label's length fixes where each token sits: two
  numbers of one count print two labels, and with the year in the label
  a yearly count's labels differ across years too. `labels` holds each
  label unique within its scope key, so a defect that would print one
  twice fails the write (`BehaviorError`). With `scope`, each target
  counts from 1, so two targets each have an `INV-2026-0001`, and a list
  by label returns one instance per target.
- **The year.** `{yyyy}`, `{yy}` and `reset` read the UTC year of the
  instance's `createdAt`, read with the scope link's target in one read
  of the instance (`instances.get`, as the caller), so a number
  `assignNumber` gives later counts in the year the instance was made.
- **Nothing changes a number.** No operation writes the columns once
  set: a create or an update writes own fields only, and `assignNumber`
  writes them only while they are empty. Moving the scope link keeps the
  number, counted in the count of the target it was numbered under.
- **Backfill.** An instance made before the schema composed `Numbering`
  reads `behaviors.Numbering` as `{}`, and `where: { 'Numbering.seq':
  null }` lists them; `assignNumber()` numbers one with an operation
  event, in the count of its year and target. A deployment backfills by
  invoking it on each in creation order.
- **The `number` column.** The column that holds `seq` is named `number`,
  since the relation over the instances (`sql.instances()`) has a `seq`
  of its own, the instance's event sequence.
- **Lease.** On a type that composes `Lease`, list
  `"Numbering.assignNumber"` in Lease's `exempt`, or the backfill of a
  leased instance is refused for anyone but its holder.

#### Timers

Named deadlines on an instance (D56): a request open four hours without
an answer is escalated, an order not paid within thirty minutes of its
create is cancelled, a visit is reminded a day before its time.

| | |
| --- | --- |
| Config | `deadlines` (at least one, by name): each `{ start, afterMs?, then?, until? }`. `start`: `{ create: true }`, `{ enters: <state> }`, each entry of the status into a state of the type's Workflow, or `{ field: <key> }`, an own field whose JSON Schema format is `date-time` (`fieldSchemas`). `afterMs`: from the start, in milliseconds, 0 when absent, within 100 years either way, negative for a field start alone. `then: { transition, from? }`: the move a fire makes while the status is one of `from`, the state the deadline starts in when absent (its `enters` state, or the Workflow's initial one for a create start; a field start names `from`). `until`: `{ leaves: <state> }`, the state the deadline starts in, or `{ enters: [states] }`, none of them that state: what cancels a pending deadline. `sweepMs` (5000 when absent, at least 1000); `permission`, to reset or snooze, optional. Requires `Workflow` for `enters`, `then` and `until`, which `parseConfig` checks: a field deadline needs none |
| Fields | `deadlines`: `{ <name>: { dueAt, fired, firedAt?, outcome? } }`, each started deadline, `outcome` what its `then` met: `moved`, `skipped` (the status was not one of `from`), `vetoed` or `forbidden`; a deadline not started, or cancelled, is absent, and with none at all it is `{}`. Not volatile: nothing in it follows the clock, and a reader works out what is overdue from `dueAt` |
| Operations | `fireTimer({ deadline })` -> `{ fired, outcome? }`, writes: fires the deadline when it is due and has not fired, else `unchanged()` with `{ fired: false }`; `resetTimer({ deadline })` -> `{ dueAt }`, writes: starts it again from what it starts from now, `unchanged()` when that changes nothing; `snoozeTimer({ deadline, dueAt })` -> `{ dueAt }`, writes: moves a started deadline to `dueAt`, a fired one firing again then |
| Schedule | `fire`, every `sweepMs`: invokes `fireTimer` on each deadline due and not fired, at most 1000 a run |
| Refusals | `fireTimer`, `resetTimer` and `snoozeTimer` of a deadline the config does not name (`no_deadline`); `resetTimer` and `snoozeTimer` without `permission` (`can`), `forbidden`, and for everyone when the config names none; `resetTimer` of a deadline with nothing to start from now, an `enters` deadline while the status is another state, or a field deadline whose field holds no date-time, and `snoozeTimer` of one not started or cancelled, and either while `until` holds (`not_armed`); all but `forbidden` `vetoed`. At define, a state the Workflow lacks, a `start.field` that is not an own field or not a date-time one, a negative `afterMs` for another start, a `then` whose `from` state no transition leads from to its state, or that leads to itself, a field start's `then` without `from` and its `until.leaves`, an `until.leaves` other than the state the deadline starts in, an `until.enters` that names it, and an `enters`, `then` or `until` on a type without `Workflow` (`invalid_schema`) |
| Storage | `deadlines(namespace, schema, id, name, base_at, armed_from, due_at, fired, fired_at, outcome)`: a row per started deadline, the time it starts from, the fact it was armed from, when it is due (null once cancelled), and its fire; a partial index on `(namespace, schema, due_at, id, name)` where `fired = 0` |
| Events | the change that arms, moves, cancels or fires a deadline carries `deadlines`: a create's event, an update's, a transition's, and `fireTimer`'s, `resetTimer`'s and `snoozeTimer`'s |
| `configChange` | any; added to and removed from a schema with instances. `afterConfigChange`: a new `afterMs` moves each pending deadline from its start, but a snoozed one; a removed deadline's rows, and those of one whose `start` changes, are deleted; a new field deadline arms every instance from its stored fields; a version without `Timers` deletes every row |

A deadline arms when the fact it starts from changes, which
`afterChange` reads from the instance as the change leaves it, in its
transaction, never from which operation ran: a `Lease` expiry, a
`Reactions` move and a fire move the status as a caller's transition
does.

- **The fact.** The instance's `createdAt` for a create start;
  `Workflow.enteredAt`, while the status is the `enters` state, read with
  `Workflow.status` through `instances.get` as the caller; the field's
  value, from the context's data. The row keeps it (`armed_from`), and a
  write that leaves it as it is arms nothing, so a snooze or a reset
  outlives every later write. A status that left the state and came back
  within one call has a new `enteredAt`, which arms the deadline again.
  A create arms whatever the list order, since every `initialize` has
  run by `afterChange`. A field moved arms its deadline again, unfired;
  a field cleared cancels it. A due time already past fires at the next
  sweep. An instance whose status predates `Workflow.enteredAt` arms its
  `enters` deadlines at its next entry.
- **`until`.** After arming, a pending deadline whose `until` holds for
  the status, out of the `leaves` state or in an `enters` one, is
  cancelled: its row keeps the fact, so only a new one arms it again. A
  fired deadline is not cancelled. `fireTimer` checks `until` too and
  cancels instead of firing, so a deadline armed at a publish, or under
  an `until` a later version added, never fires while it holds.
- **A fire.** `fireTimer` marks the deadline fired, then, with `then`,
  calls Workflow's `transition` when the status is one of `from`. A veto
  of the move (a `StateRules` requirement, say) and a `forbidden` (a
  transition permission the caller lacks) leave the status as it is, the
  deadline fired and `outcome` saying which; anything else fails the
  call. Any principal who may write the instance may call it; on a
  deadline not due, fired already or not started it is `unchanged()`, so
  the sweep and a caller never fire one twice.
- **The sweep.** `fire` runs on each schema in each namespace, as the
  runner's principal, which needs `read` and `write` on the schema and
  any permission a `then` transition names. It reads the deadlines due
  and not fired through the partial index, earliest due first, 100 at a
  time, a keyset of due time, id and name past each page, and invokes
  `fireTimer` on each; a veto or a `forbidden` the invoke meets, a
  `Lease` guard's say, leaves that deadline for the next run, and the
  keyset moves past it, so a stuck deadline holds none behind it back.
  An archived namespace fires nothing; unarchived, its overdue deadlines
  fire, 1000 a run.
- **Snooze and reset.** `snoozeTimer` sets the due time alone, so a
  later write keeps it until the fact changes, and a new `afterMs` leaves
  it. `resetTimer` arms the deadline again from the fact as it is now,
  undoing a snooze or a fire. Both ask `can()` for `permission`.
- **New versions.** A new `create` or `enters` deadline arms at each
  instance's next write, so an instance never written again does not
  arm it; a new field deadline arms at the publish, with no event, from
  every instance's stored fields.
- **Reactions.** `Timers.deadlines` is not volatile, so a `Reactions`
  rule hears a fire: `{ "when": { "changes": { "field": "Timers.deadlines.respond.fired", "to": true } } }`.
- **Lease.** On a type that also composes `Lease`, `fireTimer` is a
  writing operation of another principal than the holder:
  `"Timers.fireTimer"` belongs in Lease's `exempt`, or the sweep leaves a
  leased instance's deadline for each next run
  (`runtime/engine-workqueue/README.md`, "The guard"). Its move is a
  `call()`, which the guard lets through.

#### Derived

Own fields a model outside the engine fills from other own fields (D73).
The config names each derived field's inputs (`from`), its
`instruction`, a `model` label and optionally the Workflow `states` it
is derived in, and the `permission` a deriver needs. The engine calls no
model: a deriver pulls `staleDerivations`, each item the field's inputs,
instruction, model, its JSON Schema as `output` and a `sourceHash` (the
SHA-256 of the model, the instruction, the output and the inputs that
hold a value), and settles values or failures with `settleDerivations`,
which invokes `applyDerived` on each instance in a savepoint.
`applyDerived` writes through `update_attributed`, attributed to Derived
with each value's confidence, so every `validate` and guard judges the
values and the event records them. The behavior keeps a row for each
instance and field in its table: the source hash, the last settled hash,
model, confidence, who settled and when, an override, the failed
attempts and whether a deriver is wanted (a partial index on it serves
the pull). afterChange hashes the inputs again on every change and
records an override when a caller's create or update, or another
behavior's operation, writes a derived field; `rederive` clears it.
Failures, invalid values and other behaviors' vetoes count attempts and
append no event; after `maxAttempts` the field leaves the queue, which
`failedDerivations` lists, until its hash moves. `Derived.stale` is
volatile, since a new version's instruction or model moves it.
afterConfigChange hashes every instance again, drops a removed field's
rows and every row when the behavior goes, and counts a value an
instance already holds as overridden, by `existing`. Vetoes:
`not_derived`, `moved`, `out_of_state`, `overridden`. The guide's
"Derived" section has the operations' shapes. A `Secret<T>` field is
refused as an input (D74).

The deriver's loop is the engine's own (D74): `runDeriver({ source,
schemas, answer, batchSize?, concurrency?, idleMs?, signal? })` in
`src/client/deriver.ts`, which the package and `/client` export, over
`inProcess(engine, principal, { namespace? })` (an `Engine` or an
`AsyncEngine`, whose commits wake it) or an `EngineClient`'s `instances`;
and `derive::run_deriver(background, principal, schemas, answer, options,
shutdown)` in the Rust engine's `background` feature, which calls the
engine on the blocking pool and waits on the storage's notifier. Each
pages a schema's stale fields, answers a page `concurrency` at a time,
settles it in one call, and settles an answer that throws or panics as a
failure. A stack's `@deriver` deployables run them.

#### Provenance

Who set each own field last and through what (D73). afterChange compares
a change's own fields before and after it and records each recorded
field that moved: `caller` for a caller's create or update, else the
attribution entry's behavior or the operation's own (D16, amended), the
principal's subject, the time and the attributed confidence. With
`review`, a write from one of its `sources` is `proposed`, or `accepted`
when its confidence reaches `below`; `accept` and `reject` need the
review's permission, and `reject` writes the field through its own
`update()`, as source `Provenance`. Another write over a proposal makes
it `edited`. The instance's `in_review` column holds whether any field
is proposed, which the `inReview` filter reads. A gate's guard refuses a
Workflow transition into its state while a field it lists is proposed
(`unreviewed_fields`), and lets a move Workflow refuses through, as
Approvals' does. Vetoes: `not_recorded`, `not_in_review`,
`unreviewed_fields`.

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

A deployment that serves several projects of its own also makes a
namespace while the engine runs, and archives one it is done with (D16,
amended: namespaces made while the engine runs, and archived):

```ts
engine.namespaces.create(admin, 'acme');     // { name, origin: 'created', shared, state: 'active', createdAt, createdBy, ... }
engine.namespaces.archive(admin, 'acme');    // state 'archived', archivedAt, archivedBy
engine.namespaces.unarchive(admin, 'acme');  // state 'active'
engine.namespaces.list(admin);               // the ones the policy lets admin list
engine.namespaces.get(admin, 'acme');
```

- **Two sources.** The options configure some, `default` always among
  them, and only the options add or take one away (`origin:
  'configured'`). A create makes another (`origin: 'created'`), which
  `engine_namespaces` keeps, so the next engine on the file has it.
  `names` lists them all, archived ones included, the configured first.
  A name the options list is configured whatever the file holds for it.
- **The same rules.** A created namespace has its own drafts, versions,
  instances and events, and looks names up in the shared namespace,
  which is always a configured one, as any other does. Its name follows
  the rule above (`invalid_argument` otherwise), and a name that is a
  namespace already, archived or not, is `conflict`.
- **The policy decides.** Each call asks `manage` about the namespace,
  with the operation: `create` with the new name, `archive`,
  `unarchive`, and `list` for each namespace `list` returns and the one
  `get` returns. The policy is asked before the engine says whether the
  name exists.
- **An archive freezes it.** An archived namespace is read as it was:
  schemas, drafts, instances, events, values, describe and tools
  documents, search. Every write is `namespace_archived` (409) once the
  policy has allowed it: a define, a publish, a create, an update, a
  delete, a writing operation, of an instance or a schema. Its tools
  document hides each tool that writes there, with the reason. The
  runner runs none of its reactions and schedules, which show as
  `archived` and pick up where they stopped once it is unarchived.
  Retention prunes it as any namespace, held by its subscriptions. A
  publish in the shared namespace still runs `afterConfigChange` for its
  instances, since that keeps their derived storage in step with the
  version they read.
- **Only a created namespace is archived.** Archiving or unarchiving a
  configured one is `conflict`; archiving an archived one, or
  unarchiving an active one, changes nothing and returns it as it is.
  Nothing is deleted: there is no delete of a namespace.

A create, an archive and an unarchive append no event, in the
namespace's own log or any other. The record carries who did it and
when. The log's events are read per schema, `read` asked of each event's
schema, and a namespace's change names none, so a reader of the log
would need a rule of its own; a writer learns of an archive from its
next write's `namespace_archived`, and a reader reads on as before, since
an archive stops no read. Putting it in another namespace's log, the
shared one's, would show one project another's name, which `list` hides
from it (D16, amended: retention and the value store have bounds).

## HTTP

`@superschematic/engine/http` serves an engine over HTTP (D16).
`engineApp(engine, options)` returns a Hono app with a fixed set of routes
that carry the namespace and the schema name as path parameters and
resolve them per request, so publishing a version mounts nothing. Every
route goes through `@superschematic/http-runtime` (D15): the request id,
the rate limit (keyed by the connection's peer address), the body limit,
the authentication gate, the timeout around the engine call, the
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
`authenticateService`, `permissionMatcher`, `onError`, `bodyLimitBytes`,
`rateLimit`), with `rateLimitPerMinute` and `timeoutSeconds` for every
route, `stream: { pageSize, heartbeatMs }`, `tools`, a filter of the
tools document per caller ("MCP"), and `identity` and `identityRoutes`
for the core user model ("Users"). The deployment's `onError` sees what
the engine does not raise.

### Routes

| Method | Path | Engine call | Success |
| --- | --- | --- | --- |
| GET | `/namespaces` | `namespaces.list` | 200, the namespaces the caller may list ("Namespaces") |
| POST | `/namespaces` | `namespaces.create`, body `{"name"}` | 201, the namespace, `Location` |
| GET | `/namespaces/{namespace}` | `namespaces.get` | 200, the namespace |
| POST | `/namespaces/{namespace}/archive` | `namespaces.archive` | 200, the namespace, archived |
| POST | `/namespaces/{namespace}/unarchive` | `namespaces.unarchive` | 200, the namespace, active |
| GET | `/namespaces/{namespace}/schemas` | `schemas.list` | 200, the names the namespace reaches |
| POST | `/namespaces/{namespace}/schemas` | `schemas.define`, body: the document | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}` | `schemas.live` | 200, the live version |
| GET | `/namespaces/{namespace}/schemas/{name}/draft` | `schemas.draft` | 200, the draft |
| GET | `/namespaces/{namespace}/schemas/{name}/versions/{version}` | `schemas.version` | 200, that version |
| POST | `/namespaces/{namespace}/schemas/{name}/publish` | `schemas.publish` | 200, `{namespace, name, version, published}` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances?limit=&cursor=&where=&valueRefs=` | `instances.list`; `where` a JSON object | 200, `{items, next}` |
| GET | `/namespaces/{namespace}/schemas/{name}/lookup?key=&valueRefs=` | `instances.lookup`; `key` a JSON object | 200, the instance, `ETag` |
| POST | `/namespaces/{namespace}/schemas/{name}/instances` | `instances.create`, body `{"id"?, "data", "behaviors"?}` | 201, the instance, `ETag`, `Location` |
| GET | `/namespaces/{namespace}/schemas/{name}/instances/{id}?valueRefs=` | `instances.get` | 200, the instance, `ETag` |
| PATCH | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.update`, body: a merge patch; `If-Match`, `Preconditions` | 200, the instance, `ETag` |
| DELETE | `/namespaces/{namespace}/schemas/{name}/instances/{id}` | `instances.delete`; `If-Match`, `Preconditions` | 200, `null` |
| POST | `/namespaces/{namespace}/schemas/{name}/instances/{id}/operations/{operation}` | `instances.operate`, body: the parameters; `If-Match`, `Preconditions` | 200, the result, `ETag` |
| POST | `/namespaces/{namespace}/schemas/{name}/operations/{operation}` | `instances.invokeSchema`, body: the parameters of a schema-level operation | 200, the result |
| POST | `/namespaces/{namespace}/schemas/{name}/instances/{id}/signals/{behavior}/{signal}` | `signals.send`, body: the signal's data, `{}` when absent ("Signals") | 204, no body |
| POST | `/namespaces/{namespace}/behaviors/{behavior}/operations/{operation}` | `behaviors.invoke`, body: the parameters of a namespace operation ("Namespace operations") | 200, the result |
| GET | `/namespaces/{namespace}/schemas/{name}/describe` | `tools.describe` | 200, the describe document ("Tools") |
| GET | `/namespaces/{namespace}/tools` | `tools.manifest` | 200, the tools document ("Tools") |
| POST | `/namespaces/{namespace}/search` | `engine.search`, body: its parameters | 200, `{items, next}`, the hits of every schema that composes Search ("Search", "Across schemas") |
| GET | `/namespaces/{namespace}/events?after=&limit=&schema=&instanceId=&kind=&behavior=&exclude=&beside=&signals=` | `events.read` | 200, `{events, next, more}`; with `Accept: text/event-stream`, the stream, and with `signals=1` its signals |
| GET | `/namespaces/{namespace}/values/{hash}` | `values.get` | 200, `{hash, bytes, value}`, a value of the value store a schema the caller may read references ("The value store") |
| GET | `/behaviors` | `tools.listBehaviors` | 200, a summary of each behavior the engine runs ("The behavior catalog") |
| GET | `/behaviors/{name}` | `tools.describeBehavior` | 200, the behavior's document |

A schema version is the stored record without its canonical text, which
`hash` identifies. An instance is the stored record (`namespace`,
`schema`, `id`, `schemaNamespace`, `version`, `seq`, `data`, and who
created and last updated it, and when); its `data` carries its own
fields and its `behaviors` its behaviors' fields, by behavior name, which
a create or an update cannot reach: `data` is the own fields only, and
another key there is 422 (`unknown`). With `valueRefs=true`, a get or a list returns each field
the value store holds as its ref, and the instance lists them in
`valueRefs` ("The value store"). A list's `where` and a lookup's `key`
are JSON objects in their query parameter,
`?where=%7B%22status%22%3A%22open%22%7D`, so a value keeps its type and
any slash or comma in it; one that is not a JSON object is 400
`bad_request`, and one the engine refuses 400 `invalid_argument`. A
lookup that finds nothing is 404. A create's `behaviors` gives the behaviors their create
parameters, by behavior name ("Create parameters" under "Behaviors");
`null` is none. A request body is `application/json`, and an update's
`application/merge-patch+json` (RFC 7386); another media type is 415,
with `Accept-Patch` on PATCH. An operation's body is its parameters, and
no body is `{}`; its result, whatever JSON it is, is the envelope's
`data`, and the instance's sequence after the call its `ETag`, which a
read-only operation, and a call that changed nothing, leave where it
was. A schema-level operation's route
takes its parameters the same way and answers its result with no `ETag`,
since it names no instance; each operation route answers 404 for an
operation of the other scope. A namespace operation's route takes its
parameters and answers its result the same way, and 404 for an operation
of another scope or a behavior the engine does not run.

The event route's `after` is a cursor or `head`; `kind`, `behavior` and
`exclude` are lists, as repeated parameters or comma-separated
(`kind=create,update`), with `events.read`'s meaning ("The event log").
An `after` that is neither is 400 `bad_request`; a filter value the
engine refuses is 400 `invalid_argument`; a cursor before the
namespace's floor is 410 `cursor_expired` ("Retention"), and no `after`,
or `after=0`, reads from the floor.

A namespace's create takes `{"name": "<namespace>"}` and nothing else
(400 `bad_request` otherwise); its archive and unarchive take no body.

The tools route answers the tools document as the caller sees it. The
`tools` option narrows it per caller as the MCP endpoint's does ("MCP"):
a tool it leaves out is hidden, with the reason. It narrows only the
document; each route still answers as the access policy says.

The behavior routes carry no namespace: the behaviors an engine runs are
the same in every namespace.

### Statuses

| Status | `code` | When |
| --- | --- | --- |
| 400 | `invalid_argument` | a page size, cursor, instance id, schema name or version the engine refuses, a `where` or a lookup `key` it refuses; an operation's parameters its `paramsSchema` refuses (`OperationParamsError`), a create's parameters the engine or a behavior refuses (`CreateParamsError`), or preconditions the engine refuses (`PreconditionsError`), `details.issues` |
| 400 | `bad_request` | a parameter or body the runtime cannot decode, a create body that is not `{id?, data, behaviors?}`, a namespace create body that is not `{name}`, a `Preconditions` header that is not a JSON object, a `where` or a `key` that is not a JSON object, a path that is not valid percent-encoding, an event `after` that is not a cursor or `head` |
| 401 | `unauthorized` | the `Authenticator` returned no caller, or one without a subject, and no verified service stands in for one |
| 401 | `service_unauthorized` | a `Service-Authorization` credential that does not verify (the runtime's service step, D37) |
| 403 | `forbidden` | the access policy refused, or a behavior refused a caller without the permission its config names |
| 403 | `service_forbidden` | a verified service identity the service authenticator does not list as a caller |
| 404 | `not_found` | no such version, draft or instance in the namespace, or no such route |
| 404 | `unknown_namespace` | no such namespace, configured or created |
| 409 | `conflict` | an instance with the id exists, or another instance holds a unique field's value, `details.fields` its keys; a namespace with the name exists; an archive or unarchive of a configured namespace |
| 409 | `namespace_archived` | a write in an archived namespace |
| 409 | `name_taken` | the name is defined on the other side of the shared lookup |
| 409 | `incompatible_change` | the version breaks the compatibility rule; `details.changes` |
| 409 | `vetoed` | a behavior refused the create, the update, the delete or the operation; `details` is `{behavior, action, reason, code?, details?}`, the veto's code and details when it gives them |
| 410 | `cursor_expired` | an event read or a stream's resume from a cursor before the namespace's floor; `details` is `{after, floor, head}` |
| 412 | `seq_mismatch` | `If-Match` names a sequence the instance is no longer at; a failed precondition is the behavior's veto, 409 |
| 413 | `payload_too_large` | the body exceeds `bodyLimitBytes` |
| 413 | `value_too_large` | a value the write would store is longer than `values.maxBytes`; `details` is `{path, bytes, maxBytes}` |
| 415 | `unsupported_media_type` | the body is not of the route's media type |
| 422 | `invalid_schema` | the document is refused; `details.issues` |
| 422 | `invalid_instance` | the instance, or an update's result, is refused by the live version or a behavior's `validate`; `details.issues` |
| 429 | `rate_limited` | a caller's signals past `signals.perSecond` on one instance ("Signals"); `Retry-After: 1` |
| 429 | `too_many_requests` | the rate limit; `Retry-After` |
| 500 | `internal_error` | anything else the deployment's `onError` does not map, a `BehaviorError` included; the failure stays off the wire |
| 503 | `unavailable` | the schema's live version composes a behavior this engine has no implementation for, or whose implementation refuses its config |
| 503 | `service_unavailable` | the service authenticator could not check a credential (its keys could not be fetched) |
| 504 | `gateway_timeout` | the timeout elapsed |

`engineProblem` and `ENGINE_ERROR_STATUS` hold the engine's rows. `type`
is `about:blank`, as the HTTP runtime writes it for every problem; `code`
names the problem. A detail names only what the request named, so an
instance id that exists only in another namespace answers as one that
exists nowhere. A namespace's name is not secret: an unknown namespace is
404, and one the policy refuses is 403. Which namespaces exist is the
policy's to show: `GET /namespaces` lists the ones it lets the caller
list.

### Authentication and access

The runtime's gate requires a caller on every route: the deployment's
`Authenticator` returns a principal, or none (401). The engine acts as
that principal, which already has the engine's `Principal` shape, and
asks its access policy (403). The routes name no permissions, so
`permissionMatcher` matters only where the policy calls it.

With `authenticateService` (D37, the HTTP runtime's `ServiceAuthenticator`;
`serviceAuthenticator(config)` is the standard one), the runtime's
service step verifies a `Service-Authorization` credential on every
route, before the end-user step, and the engine's principal carries the
caller ("Service callers" under "Access"). The routes declare no
`@requireService` or `@allowService` clause: what a service may do is the
policy's to say. The engine wraps the deployment's `Authenticator`:

- its principal, when it returns one, with the service beside it when
  one called;
- else, for a verified service and a request with no `Authorization`
  header, the service standing in (`servicePrincipal`);
- else no caller, 401. A request whose `Authorization` the
  `Authenticator` refuses is 401 whatever service sent it, so a service
  that forwards an expired token never acts with its own authority
  instead.

A deployment whose callers are all services passes `authenticateService`
and no `authenticate`. Without `authenticateService` the runtime ignores
the header, as before.

### Users

When the project's schemas declare the core user model (D50: a `User`
table in a DB schema), the engine signs its users in with the HTTP
runtime's identity package. `engineApp` and `engineMcp` take `identity`,
an `IdentityService`, in place of `authenticate`, and refuse the two
together. `engineIdentity(engine, options)` builds it: `options` are the
service's, with the project's store and identity config, and its
`capabilities` answers for the engine.

```ts
import { readFileSync } from 'node:fs';
import { openEngine, permissionPolicy } from '@superschematic/engine';
import { engineApp, engineIdentity, engineIdentityStore } from '@superschematic/engine/http';
import { engineMcp } from '@superschematic/engine/mcp';
import { parseIdentityConfig } from '@superschematic/http-runtime/identity';
import { identityDescriptor } from '@acme/shop-db-types/identity';

const engine = openEngine({ path: 'shop.db', policy: permissionPolicy() });
const identity = engineIdentity(engine, {
  store: engineIdentityStore(engine, identityDescriptor),
  config: parseIdentityConfig(JSON.parse(readFileSync('identity.json', 'utf8'))),
});
app.route('/api', engineApp(engine, { identity, rateLimitPerMinute: 600 }));
app.route('/api', engineMcp(engine, { identity }));
```

- The service authenticates every route. A request carries its session
  as `Authorization: Bearer <token>` or in the session cookie; the
  principal's subject is the user's id, its permissions those of the
  user's roles, and its claims the session, login, name, transport and
  roles. A malformed `Authorization` is 401 `unauthorized`, since the
  service reads the header itself through the engine's wrapper. A cookie
  request other than `GET`, `HEAD` and `OPTIONS` from an origin the
  config does not trust is 403 `cross_origin`, and `engineApp` answers
  the trusted origins' CORS, preflights included, on each of its routes
  and on none of another app's (`identityCorsRoutes`), so a router
  mounted beside it answers its own; `engineMcp` answers it on its
  endpoint.
- `engineApp` serves the session routes under `/auth`, through the
  runtime like every route: `POST /auth/login`, `POST /auth/logout`,
  `GET /auth/me`, `GET /auth/capabilities` and `POST /auth/password`,
  with `POST /auth/register` when `identityRoutes.register` is true, and
  the administration routes under `/auth/admin` when the store has roles
  (`identityRoutes.administration` turns them on or off). Login and
  changePassword allow 10 requests a minute per client and register 5;
  the others take `rateLimitPerMinute`. `engineMcp` serves none of them.
- `GET /auth/capabilities` answers `{ operations }`: for each schema with
  a live version that a namespace reaches and the caller may read, the
  keys `<namespace>/<schema>.read`, `.write`, `.define` and `.publish`,
  each true when the access policy allows the action, in code-point
  order. A schema the caller may not read has no key, as the schema list
  leaves it out. It is the policy's answer: a call it allows may still
  be refused for another reason, and a behavior operation asks the
  action its declaration says. An archived namespace refuses every
  write, so its schemas answer `.write`, `.define` and `.publish` false
  ("Namespaces"). `engineCapabilities(engine, principal)` computes it.
- A behavior's own permission, such as a Workflow transition's, is a
  role's like any other: `can()` asks the engine's `permissionMatcher`
  with the user's permissions, which the service reads from the user's
  roles on each request, so a grant reaches a session already signed in.
- The runner's principal is not a user and does not change: the
  deployment names it (`runner.principal`), it holds no session, and the
  policy answers it as any caller.

The store is the project's. `engineIdentityStore(engine, descriptor)`
keeps the identity tables in the engine's own SQLite file, beside the
engine's, whose names all start with `engine_` or `bhv_`, so the two
never meet; the engine's migrations neither create nor read them. The
deployment creates them once from the authDb's SQLite DDL, the
`sqlite/create.sql` the DB build writes when the authDb's
`outputs.sql.dialects` lists `sqlite`, before the store first reads them
(`engine.storage.exec(createSql)`, or `superschematic-migrate apply` with
the authDb's SQLite plan, which keeps its state tables in the same
file). A store operation runs synchronously on the engine's connection,
its transaction included, as the engine's own writes do. A separate file
is `sqliteIdentityStore(nodeSqlite(new DatabaseSync('users.db')),
identityDescriptor)` (`bunSqlite` on Bun), and the project's Postgres
database, shared with its APIs, is `postgresIdentityStore(pool,
identityDescriptor)`; the descriptor is the authDb's, which its
generated TypeScript types export as `identityDescriptor`.

### Concurrency

An instance's `seq` is its entity tag (`ETag: "3"`). PATCH, DELETE and an
operation's POST pass the sequence `If-Match` names to `update`, `delete`
and `operate` as `expectedSeq`, which the engine checks inside the write
transaction, so a lost update answers 412 and writes nothing. `*` asks
only that the instance exist; a weak tag never matches. An instance that does not exist is 404 whatever
`If-Match` says: RFC 9110 evaluates a precondition only where the request
would otherwise succeed. A writing behavior operation moves `seq` too
("Instances and events" under "Behaviors"), so a tag read before it no
longer matches, unless it changed nothing and said so, or runs beside
the instance (D77): its POST checks `If-Match` as a read-only
operation's does and answers the `ETag` the instance had.

`If-Match` cannot hold across writes that move `seq` while a caller
works, a lease's heartbeats say. What a caller assumes of one behavior
travels in the `Preconditions` header instead: a JSON object of entries
by behavior name, `Preconditions: {"Lease": {"token": 7}}`, on PATCH,
DELETE and an operation's POST, which the engine passes to `update`,
`delete` and `operate` as `preconditions` ("Vetoes and preconditions"
under "Behaviors"). A header that is not a JSON object is 400
`bad_request`, an entry the engine refuses 400 `invalid_argument`, and a
precondition a guard finds false that behavior's 409 veto, with its
code. Other routes ignore the header. It is a header because the bodies
are taken: an update's is a merge patch of the instance, an operation's
its closed parameters.

### The event stream

`GET /namespaces/{namespace}/events` with `Accept: text/event-stream`
answers server-sent events:

```
: open

id: 41
data: {"cursor":41,"kind":"create","namespace":"default","schema":"Order","instanceId":"o1","seq":1,"version":1,"actor":"alice","at":1790000000000,"change":{"title":"Desk"}}

event: ready
id: 41
data: {"cursor":41}

: keepalive
```

- Each event of the log is one message with no `event:` field, so
  `EventSource.onmessage` receives every one: `id:` is its cursor and
  `data:` the event as `events.read` returns it, of every kind, an
  `operation` and a `define` included.
- The stream starts after `Last-Event-ID`, which a reconnecting
  `EventSource` sends, else after `after` (a cursor, or `head` for no
  replay), else at the start of the log. `schema`, `instanceId`, `kind`,
  `behavior`, `exclude` and `beside` (`true` or `false`) filter as on
  the JSON route; `limit` applies
  to the JSON route only. With neither, or from 0, it replays from the
  namespace's floor ("Retention"). Any other start before the floor is
  the 410 `cursor_expired` problem, so a client knows to start again
  from the head; a stream whose cursor retention passes while it is open
  ends, and its reconnect gets that problem.
- It replays in pages of `stream.pageSize` (100) and reads the next page
  only when the server has sent the previous one, so a slow client holds
  one page and replay never loads the backlog. The first page that is the
  last (`more` false) ends replay, and the stream sends `event: ready`,
  whose `id:` and `data:` carry the cursor it caught up at
  (`addEventListener('ready', ...)`; `onmessage` does not receive it).
  From `after=head`, ready comes at once. Its id is what a reconnect
  resumes from, so a client that started at the head resumes where it
  caught up, not at a later head. Caught up, it waits for the engine's
  notice of a commit (`events.watch`) and reads on from its cursor.
- A page's cursor moves past every event it scanned, the ones the
  filters dropped too, so it can run ahead of the last event sent. A
  message with only `id:` brings the client's last event id up to it:
  `EventSource` records the id and, the data being empty, dispatches
  nothing (the HTML standard's dispatch steps). The stream sends one
  after events whose last is behind its cursor, and for a page of replay
  that kept none. While it waits, a commit whose events the filters all
  drop sends nothing; the next heartbeat carries the cursor instead of
  its comment. A reconnect therefore rescans at most what one heartbeat
  let pass, and replays nothing it dropped.
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
- With `signals=1` (or `true`) it also sends the signals ("Signals") of
  its namespace that its `schema`, `instanceId` and `behavior` filters
  keep, on schemas its principal may read, each one message `event:
  signal` whose `data:` is the signal and which has no `id:` line, so
  `Last-Event-ID` and a reconnect's replay never see one and
  `EventSource.onmessage` does not receive it
  (`addEventListener('signal', ...)`). It sends them once replay has
  caught up, after `ready`, at most once and in no order with the events;
  `kind`, `exclude` and `beside` do not apply to them. A signal wakes the
  stream as a commit does, and the stream reads the log again only when a
  commit came since its last read, or a heartbeat went by since its last
  read, so signals sent more often than a heartbeat, which put its
  keepalive comment off, never put off its reads. Without
  `signals=1` a stream is as it was.

  ```
  event: signal
  data: {"namespace":"default","schema":"Ticket","instanceId":"t1","behavior":"Comments","name":"typing","data":{},"from":"bob","at":1790000000000}
  ```

- It ends when the client disconnects, which removes its watcher, and
  when the engine closes. `timeoutSeconds` covers the first page, not the
  stream.

### Mounting the Rust engine on axum

The Rust engine's `axum` feature (`superschematic-engine`, off by
default, so the C archive and the Node-API addon leave axum out) is the
adapter a Rust server mounts the engine's routes with (D58).
`superschematic_engine::axum::router(engine, options)` returns an axum
`Router` with the path of each route under `options.prefix`, which the
server merges into its own. A Rust server depends on the crate with its
`postgres` and `axum` features, at `[paths] engine_rust`; the feature
takes axum 0.8.9, tokio 1.52.2 and tower 0.5.3, the generated servers'
versions.

```rust
use std::sync::Arc;

use axum::http::request::Parts;
use axum::Router;
use superschematic_engine::axum::{router, Options};
use superschematic_engine::Engine;

let engine = tokio::task::spawn_blocking(move || Engine::open_shared(options)).await??;
let routes = router(
    Arc::clone(&engine),
    Options::new("/engine/shop").callers(|request: &Parts| caller_of(request)),
)?;
let app = Router::new().merge(routes).fallback_service(api_router);
```

- **The request.** Each request becomes an `api::Request`. Its path is
  still percent-encoded, with the prefix. A host that nests the router
  itself (`Router::nest("/engine/shop", router(engine, Options::new("")))`)
  gets its prefix from the original URI, so `Location` keeps it. A header
  sent twice is one value, joined with `, `. The request id is
  `X-Request-ID` when it is non-empty, at most 128 characters and free of
  control characters (what the HTTP runtime's `request_ids` middleware
  settled, or the client's), else a fresh UUID. `api::handle` serves it
  on tokio's blocking pool.
- **The caller.** `Options::callers` gives what the host's authentication
  established, as `api::Caller` holds it: the end user (D50), the calling
  service (D37), or a refusal the engine answers in its place on a route
  it serves. A function of the request's head is a `Callers`. An
  asynchronous authentication, as the HTTP runtime's authenticators are,
  implements `Callers` on a type of its own. The default, `FromExtensions`,
  takes the `Caller` a middleware put in the request's extensions, and is
  no caller (401) when there is none. The router asks once a route takes
  the request and its body is within the limit, as the HTTP runtimes
  order their steps.
- **What the router answers itself.** A method no route on the path
  takes is the engine's 404, before the caller is asked, as the other
  runtimes answer it, not axum's 405 with `Allow`. A route that takes a
  body (`Route::takes_body`: the nine whose spec declares one) reads at
  most `Options::body_limit` bytes (1 MiB, `api::DEFAULT_BODY_LIMIT_BYTES`)
  and is 413 `payload_too_large` past it, as the HTTP runtimes' body limit
  answers. MCP reads up to its own 4 MiB and refuses past it as JSON-RPC.
  A path under the prefix that names no route is the host's fallback's.
  `Options::serves` mounts the HTTP API (`Serves::Http`, `engineApp`'s
  routes) or the MCP endpoint (`Serves::Mcp`) alone.
- **The event stream.** Its body pulls the stream as the client reads it.
  Each pull reads on the blocking pool and returns at once. Between pulls
  the stream waits as a future, which a commit wakes (one thread per
  router waits on the storage's notifier for all its streams: a commit in
  this process, or on Postgres in any) or its heartbeat ends, so an open
  stream holds no thread. When the client goes away, the server finds it
  at its next write and drops the body, which closes the stream.
  `Engine::shutdown` ends every stream, so a server that shuts its engine
  down on SIGTERM can drain: a graceful shutdown waits for the responses
  still open. `Options::done`, a watch the host moves as it stops serving,
  ends them without shutting the engine down, so the requests in flight
  still get their answers.
- **The engine and the async runtime.** The engine is synchronous, so a
  host calls it off the runtime's threads (`spawn_blocking`):
  `Engine::open_shared`, `Engine::shutdown` and the engine's last drop
  too. On Postgres the engine's client blocks on a runtime of its own,
  which cannot start inside another and panics if asked to. The router
  keeps to this for the calls and pulls it makes, and lets its engine go
  on a thread of its own when it drops on the runtime.
- **The host's timeout.** `Options::timeout` bounds every route but the
  event stream's and MCP's, whose responses last as long as their
  clients, as `enginehttp.Options.Timeout` and `engineApp`'s
  `timeoutSeconds` do: a request that takes longer, from its body's read
  through the caller to the engine's call, is 504 `gateway_timeout`, the
  HTTP runtimes' problem (`"title": "Gateway Timeout"`, `"detail":
  "Gateway Timeout"`). None unless set; a generated Rust server sets the
  server's 30 seconds. A call still waiting for a blocking thread at the
  deadline never starts; one already running runs on to its end, so a
  write whose client got the timeout may still commit, and the client
  reads it back before it tries again.
- **Rate limits and CORS** are the host's layers, and so is any other
  timeout. A timeout on the time to a response's head (tower-http's
  `TimeoutLayer`, the HTTP runtime's `@timeout`) covers the engine call
  and leaves a stream open, since a stream's head goes out at once. A
  layer that bounds a response's body, or how long it may go
  quiet, would end a stream: it must leave out the requests whose route
  `route_of_request(prefix, &request)` names with `stream` (the event
  stream) or `mcp` (the MCP endpoint) set, as `api::routes()` lists them.
  A browser's CORS layer goes around the router. The engine's methods are
  GET, POST, PATCH and DELETE. Beside `Authorization` and `Content-Type`,
  a browser client sends `If-Match`, `Last-Event-ID`, `Preconditions`,
  `Mcp-Protocol-Version` and `Mcp-Session-Id`, and reads `ETag`,
  `Location`, `Mcp-Session-Id`, `Retry-After` and `X-Request-Id`.

- **An engine that opens while the server serves.** The `background`
  feature, which `axum` turns on, has `background::Background`: the
  engine opened on a task of the host's runtime from options a function
  builds each attempt, the open and a setup (a server's deploy, a
  runner's start) on the blocking pool, retrying a second apart and
  doubling to thirty, each failure told to `on_error`, as the Go
  binding's `engine.Background` does. `engine()` hands it out once it
  opens, `wait()` waits for it, `ready()` is a readiness check (open, not
  poisoned, its database answering a read) and `shutdown()` stops trying
  and shuts the engine down off the runtime.
  `axum::background_router(background, options)` registers the routes
  from the start, each 503 `unavailable` ("The engine is not available")
  until the engine opens, as `enginehttp.Background` answers, then serves
  them as `router` does. `Engine::deploy(documents, namespace)` is a
  server's deploy: each schema document defined and published as the
  engine's own principal, one whose live version has its hash left as it
  is; the C ABI's `deploy` calls it.
- **A password the host refreshes.** `storage::PostgresOptions::password`,
  a `PasswordProvider`, gives each new connection's password (the pool's,
  the listener's, a lease's), asked on the thread that opens it, in the
  URL's place: an IAM access token over the `/cloudsql/<instance>`
  socket, which the HTTP runtime's `postgres::sync_connection` fetches.

`engine/tests/axum.rs` drives the router in process and over a TCP
listener, on both databases; `engine/tests/background.rs` the
background open and its router, and `storage/tests/password.rs` the
password provider on Postgres. The test kit replays the HTTP corpus
through it (`AxumRouter`, the test kit's `axum` feature), as it replays
the corpus through `api::handle`, with the same skips and known steps.

## Tools

`engine.tools` holds a describe document per schema, the tools document
of a namespace, the behavior catalog, and the calls the MCP tools make.
Each schema's reads and calls go through the schema registry and the
instance store, so the access policy answers every one.

### The describe document

`tools.describe(principal, name, { namespace })` (asks `read`) describes
a schema's live version:

```json
{
  "namespace": "default", "name": "Item", "schemaNamespace": "default",
  "version": 1, "hash": "9f2c...", "instanceType": "Item",
  "display": {"noun": "Item", "plural": "Items", "titleField": "title", "summaryFields": ["test.Counter.count"]},
  "fields": [{"name": "title", "title": "Title", "icon": "text", "display": {"multiline": false}}],
  "instance": {
    "type": "object", "additionalProperties": false,
    "properties": {
      "title": {"description": "A string value", "type": "string"}
    },
    "required": ["title"]
  },
  "instanceBehaviors": {
    "type": "object", "additionalProperties": false, "required": ["test.Counter"],
    "description": "The instance's behaviors' fields, by behavior name, apart from its own fields",
    "properties": {
      "test.Counter": {"type": "object", "description": "The fields of behavior test.Counter", "additionalProperties": false, "readOnly": true,
                       "properties": {"count": {"description": "The count.", "readOnly": true}}}
    }
  },
  "behaviors": [{"name": "test.Counter", "description": "Counts up.", "config": {"start": 0},
                 "summary": "Each Item counts up from 0.",
                 "fields": [{"name": "count", "description": "The count."}], "operations": ["increment"],
                 "vetoes": [{"code": "at_limit", "description": "The count is at its config's limit."}]}],
  "operations": [
    {"name": "create", "title": "Create Item", "description": "Creates an Item: ...", "writes": true, "invocationPolicy": "auto",
     "params": {"type": "object", "additionalProperties": false, "properties": {"data": {...}, "id": {...}}, "required": ["data"]},
     "result": {...}, "tool": "item.create",
     "guidance": {"useWhen": "Use to create a new Item: ...", "doNotUseWhen": "...", "success": "...", "errors": []}},
    {"name": "increment", "behavior": "test.Counter", "scope": "instance", "title": "Item: increment", "description": "Adds to the count.", "writes": true,
     "invocationPolicy": "auto", "params": {...}, "result": {...}, "tool": "item.increment", "guidance": {...}}
  ]
}
```

- `display` is the instance type's `@display` (D48 in
  `docs/DECISIONS.md`), as the document holds it, with `titleField` and
  `summaryFields` naming each own field by its key in an instance's
  `data`, and each behavior's by its qualified name:
  what a UI calls one instance and several, its title and summary fields,
  what a create button says, and the labels of its `Workflow`'s states
  (`label`, `activeForm`, `tone`) and transitions (by the state a
  transition leaves, then the one it enters). It is absent when the type
  declares none.
- `fields` lists the instance type's own fields in declaration order, each
  by its key in an instance's `data`, with its `title` (`@docs({ title
  })`), its `icon` (`@icon`) and its `display` (`@display`) where it
  declares them. `display` is the field's as the schema holds it:
  `multiline`, and `currency`, an ISO 4217 code or `{ field }` naming the
  own field that holds one, by its key in `data` (D48, amended). The
  engine applies no rule of its own: a reader that takes a
  `Finance.Money` field without a currency in the type's one
  `Finance.Currency` field reads each field's scalar from `instance`. A
  behavior's fields are under `behaviors`.
- `instance` is the JSON Schema of an instance's `data`: closed, its own
  fields. Under `allOf` it carries what its behaviors' `validate` holds
  the own fields to, as their `instanceSchema` writes it: `Variants`'
  `if`/`then` per value. Create's `data` carries the same, update's
  `patch` the patch form, and the instance in each result.
- `instanceBehaviors` is the JSON Schema of an instance's `behaviors`:
  by behavior name, each behavior on the type that declares a field,
  with its fields `readOnly` and without a type, since a declaration
  gives a field a name, a description and whether it is volatile, and no
  type. A volatile field says `"volatile": true` there, and a per-reader
  one `"perReader": true` besides, annotations as `readOnly` is
  ("Instances and events"). Each result that returns an
  instance carries it beside `data`.
- `behaviors` lists each behavior's config, its `summary`, what its
  guidance says it does under the config ("Guidance", below; absent for
  a behavior that gives none), its fields, each with `volatile: true`
  and `perReader: true` when it is, operations and the codes its vetoes carry (`vetoes`). A
  behavior with namespace operations (D77) names them in
  `namespaceOperations`, apart from the type's `operations`, which they
  are not among.
- `operations` lists `create`, `get`, `list`, `update`, `delete`, `lookup`
  where the instance type has a unique field, then each behavior's
  operations in the type's list order. `list`'s `params` has `where`,
  with a member per field it filters on: the field's JSON Schema, or a
  list of 1 to 100 of them (`anyOf`). `lookup`'s has `key`, a closed
  object of one unique index's fields, each required (`oneOf` over the
  indexes when there are several). `create`'s `params`
  has `behaviors` when a behavior the type composes declares a
  `createParamsSchema`: a closed object with each such behavior's
  schema under its name, as its implementation narrows it for the
  config, else as its declaration holds it ("Create parameters"). `update`'s,
  `delete`'s and each instance operation's have `preconditions` when one
  declares a `preconditionSchema`, the same way; `create`'s never does,
  and the others never have `behaviors`. So a schema that composes
  `Links`, `Lease` and `Variants` shows, side by side, the links its
  create takes, the token its writes present, each behavior's veto codes
  and, under `allOf`, the shape each variant's field takes. `params`
  is the operation's tool arguments (below), `result` the JSON Schema of
  what it returns (an instance, a page, `null` for a delete, a behavior
  operation's `resultSchema`), and the invocation policy sits under the
  policy's key. A behavior's operation carries `behavior` and `scope`,
  `instance` or `schema`. Each carries `title` and `guidance`, its
  tool's. No operation carries an icon: a schema declares no operations,
  and a behavior's declaration gives its operations none.

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
reaches that the principal may read, by name, after the engine's tools:
three schema tools, two behavior tools, the read of a value by its hash,
four namespace tools, the search across schemas where one of those
schemas composes Search, and a tool per namespace operation (D77) of each
behavior one of those schemas composes.

| Tool | Name | MCP handle | Arguments |
| --- | --- | --- | --- |
| create | `<schema>.create` | `<schema>_create` | `id` (optional), `data` (with its behaviors' `allOf`), and `behaviors` (optional) when a behavior takes create parameters |
| get | `<schema>.get` | `<schema>_get` | `id`, `valueRefs` |
| list | `<schema>.list` | `<schema>_list` | `limit`, `cursor`, `where` (when it filters on a field), `valueRefs` |
| lookup | `<schema>.lookup` | `<schema>_lookup` | `key`, `valueRefs`; listed where the instance type has a unique field |
| update | `<schema>.update` | `<schema>_update` | `id`, `patch` (a merge patch; nothing required; its behaviors' `allOf` in patch form), `expectedSeq`, `preconditions` |
| delete | `<schema>.delete` | `<schema>_delete` | `id`, `expectedSeq`, `preconditions` |
| a behavior operation | `<schema>.<operation>` | `<schema>_<operation>` | `id`, `params` (its `paramsSchema`), `expectedSeq`, `preconditions` |
| a schema-level behavior operation | `<schema>.<operation>` | `<schema>_<operation>` | `params` (its `paramsSchema`) |
| list schemas | `engine.listSchemas` | `list_schemas` | none |
| describe a schema | `engine.describeSchema` | `describe_schema` | `name` |
| define a draft | `engine.defineSchema` | `define_schema` | `document` |
| list behaviors | `engine.listBehaviors` | `list_behaviors` | none |
| describe a behavior | `engine.describeBehavior` | `describe_behavior` | `name` |
| get a stored value | `engine.getValue` | `get_value` | `hash`: `values.get` ("The value store") |
| list namespaces | `engine.listNamespaces` | `list_namespaces` | none: `namespaces.list` ("Namespaces") |
| create a namespace | `engine.createNamespace` | `create_namespace` | `name` |
| archive a namespace | `engine.archiveNamespace` | `archive_namespace` | `name` |
| unarchive a namespace | `engine.unarchiveNamespace` | `unarchive_namespace` | `name` |
| search every schema | `engine.search` | `search` | `query`, `syntax`, `vector`, `model`, `limit`, `cursor` (`engine.search`'s); listed where a schema the principal may read composes Search |
| a namespace operation | `<behavior>.<operation>` | `<behavior>_<operation>` | `params` (its `paramsSchema`); listed where a schema the principal may read composes the behavior |

A name follows the SDK generators, `<namespace>.<method>`, with the schema
name in kebab case as the namespace (`LineItem` is `line-item`), or for a
namespace operation the behavior's, its dot a hyphen (`Comments.inbox` is
`comments.inbox`, `acme.Rating`'s `topRated` `acme-rating.topRated`,
handle `acme_rating_top_rated`). An SDK
tool's handle is authored with `@mcp`; the engine derives one from the
same parts in snake case (`line_item_add_note`). A handle `@mcp` would
refuse (not lowercase snake case, longer than 48 characters), one two
tools derive, or an engine tool's hides the tool (`hiddenBy` `handle`,
below); the engine's tools keep theirs. A tool the access policy
refuses the principal is hidden too, with that reason: a schema's tool
asks what its call asks, and `define_schema` and the namespace tools,
which name their schema or namespace only when called, ask a listing
question ("Access"), and so does a namespace operation's tool, with its
behavior and operation. So is, in an archived namespace, every tool that
writes there, the namespace tools aside: the namespace refuses the
write; and every tool a mount's `tools` filter leaves out of the
caller's set ("MCP").

A hidden tool's `mcp` is `{ hidden: true, hiddenReason, hiddenBy }`.
`hiddenBy` lists each cause, in this order: `deployment` (the tool
options' `hidden`), `archived`, `policy`, `handle` and `filter`; and
`hiddenReason` is the first one's, so a tool the deployment hides keeps
the deployment's reason when its handle is refused too, and one the
policy refuses gives the policy's. The policy is asked of every tool the
deployment does not hide, one hidden for its handle included, since the
operation is still offered through `instances` and the HTTP routes: a
reader of the document learns whether the principal may run it. A tool
the deployment hides is offered to no one, and the filter is asked only
of a tool nothing else hides.

```json
{"hidden": true, "hiddenReason": "the access policy refuses reader write on SupplierContractAmendmentRequest (withdrawSignOff)", "hiddenBy": ["policy", "handle"]}
```

Here the handle, `supplier_contract_amendment_request_withdraw_sign_off`,
has 53 characters. A tool's `requiresAuth` is
true, `httpMethod` and `httpPath` name the HTTP route, a read-only tool's
`replay` is `read_only`, `inputSchemaDigest` hashes the arguments as
the Go encoder writes them, and `guidance` is the tool's guidance
("Guidance", below), which a visible tool's `mcp._meta` carries under
the guidance key too.

`preconditions` is there only when a behavior of the schema declares a
`preconditionSchema`: an object with an optional entry per such
behavior, each its schema.

No tool publishes. `define_schema` stores a draft as `schemas.define`
does; the draft goes live only through the HTTP publish route, which the
access policy governs, so an MCP client cannot put a schema live on its
own (D16).

`tools.call(principal, handle, args, { namespace })` runs a tool: the
engine call its handle names, with its arguments checked (an unknown or
mistyped argument is `invalid_argument`). A handle the namespace has no
visible tool for, among the schemas the principal may read, throws
`UnknownToolError` (`not_found`).

### Guidance

An SDK tool carries its operation's `@docs` guidance: `useWhen`,
`doNotUseWhen`, `success` and `errors`, each error a `code`, a
`description` and a `commonCorrection` (`ir.ToolOperationGuidance`). A
schema the engine runs has no `@docs`, so the engine writes the same
members from what it knows:

- its own tools (`list_schemas`, `describe_schema`, `define_schema`,
  `list_behaviors`, `describe_behavior`, `get_value`, `search`,
  `list_namespaces`, `create_namespace`, `archive_namespace`,
  `unarchive_namespace`) carry fixed guidance;
- `create`, `get`, `list`, `update`, `delete` and `lookup` carry the
  engine's base, which names the schema's instance type, for `create` the
  behaviors that take create parameters, for `create` and `update` the
  unique fields a repeated value of is a conflict, for `list` the fields
  `where` takes and that a filtered page can be short, and for `lookup`
  the unique fields its `key` names;
- each behavior adds what its config says, through its implementation's
  `guidance(config, target)`: a `summary`, which the describe document's
  `behaviors` entry carries, and, by operation name, what it says about
  each operation of the type, its own, the built-in ones and other
  behaviors' it guards. `target` has the schema, the instance type,
  every behavior the type lists with its config as the schema holds it,
  and every operation of the type with its behavior, `writes` and
  `scope`, so `Lease` adds its refusals to every write its guard holds.

An operation's guidance is the engine's base, then what the behavior
that adds it says, then what each other behavior of the type says, in
list order; each member's sentences are joined by a space. `errors`
lists vetoes, the refusals a client branches on by `details.code`
("Vetoes and preconditions"): each a code the contributing behavior's
declaration lists, with its `description`, the declaration's when the
behavior gives none, led by the behavior's name, since two behaviors may
share a code (`Lease: Another principal holds ...`), and listed once per
behavior. An engine code such as `forbidden` is not an error there; the
text says which permission a call needs.

```json
{"useWhen": "Use to move status from todo to doing or dropped; from doing to done or dropped.",
 "doNotUseWhen": "Do not use from done or dropped: no transition leaves a terminal state. Do not use to stay where the instance is. Do not move into done while blocked is true.",
 "success": "Returns from and to; status holds to from then on, and enteredAt the time of the move.",
 "errors": [{"code": "transition_not_allowed", "description": "Workflow: No transition leads from ...", "commonCorrection": "Read status, then ..."},
            {"code": "blocked", "description": "Dependencies: A move into done while a blocker is open; ...", "commonCorrection": "Wait for ..."}]}
```

The text is short plain-ASCII sentences computed from the parsed config,
once per published version, so the same config gives the same text. The
engine holds what a behavior returns to its declaration and its type: a
code the declaration does not list, an operation the type does not have,
a text with surrounding whitespace, no summary, or a value that is not
JSON is a `BehaviorError`, which fails the schema's describe and tools
documents as any defect of an implementation does. A behavior without
`guidance` has no summary and adds nothing, so an operation of its own
has empty guidance unless another behavior speaks to it.

Every core and work-queue behavior gives guidance, each one's text in a
module of its own (in `rust/behaviors` and
`runtime/engine-workqueue/rust`):

| Behavior | What it says |
| --- | --- |
| `Workflow` | the states, the initial one, the moves from each state and the permission a move needs, the terminal states with their outcomes; that `transition` sets `enteredAt`, create's initial status, that update does not move it, and list's `where` on `Workflow.status` |
| `Comments` | the conversation, the longest body, the block kinds, the edit window and who moderates, pins and resolves; streaming a message with begin, appendDraft and finish, how long its author may pause (`composeTimeoutMs`) and how many drafts a caller holds (`maxDrafts`); that every write is beside the instance; each operation's refusals, and `purge` refused without `moderatePermission` (`not_configured`) |
| `Revisions` | that every change records a revision, which `getRevision` reads by number, the review permission, `evidence` and `pendingProposals`; without review, that the review operations are refused (`no_review`) |
| `Dependencies` | the blocker schemas, the gated states, the outcomes that finish a blocker; `blocked` on `transition`, the edge refusals on `addBlocker` and `create` |
| `Links` | each link with its schema, required and what it pins, a revision or a release; what `create`, `link` and `unlink` take and refuse |
| `Rollups` | what each rollup computes over which link, and the moves a rollup gates; `not_held` on `transition` |
| `Search` | the fields and their weights, and the vectors' model, dimensions and permission; that `staleEmbeddings` and `settleEmbeddings` need vectors; `similar` by an id or a draft's text before a `create` |
| `Reactions` | each rule, what sets it off and the move it makes; on `transition`, and on the writes that can move a `changes` rule's field (`create` and `update` for an own field, the writing operations of the behavior whose field it is), that a status may move again after the commit |
| `Constants` | the fields that keep their create's value, and the permission that changes them |
| `Variants` | the type the field holds for each value |
| `Branches` | the kinds and the primary line, the `release` field, and each operation's version-graph refusals |
| `StateRules` | what each state requires and freezes, and the permission that changes a frozen field; `missing_fields` on `transition`, a create's required fields, `required_fields` and `frozen_fields` on `update` and on `Revisions`' `approve` |
| `Approvals` | each gate's count, permission and `from`, whether the creator may sign, and what consumes a sign-off; `approvals_missing` on `transition`, and that `update` consumes them |
| `Numbering` | the format with an example label, what the count runs per; create's number, list's `where` on `Numbering.label` and `Numbering.seq`, that update and a scope link's `link` change no number, and when `assignNumber` is the call |
| `Timers` | when each deadline is due, the move it makes and what cancels it, how a reader tells one is overdue, the sweep's interval, and who resets and snoozes one; the deadlines `create`, `transition` and `update` start, move or cancel |
| `Lease` | the length, the heartbeat interval, the longest hold, what an expiry and the last expiry do, the token; its refusals on every write its guard holds |
| `Assignment` | who may assign whom; `assigned_to_another` on `acquire` and `claim` |
| `Queue` | the claim states, the order, the match fields, what keeps work out; `claim_required` on `acquire` |
| `Presence` | who beats an instance and how often, what a miss does |
| `Blueprint` | what a stamp creates and when, and its refusals on the write that stamps |
| `Budget` | each meter's limit, reservation, scope and day, overruns; `scope_reserved` on a scope link's `link` and `unlink` |
| `Retries` | the classes and caps, what exhaustion does; `exhausted` on `transition`, `acquire` and `claim` |

### The behavior catalog

A client that writes schemas must learn which behaviors the engine runs
and what each one's config takes before it composes one, and
`engine.behaviors.names()` and `declaration()` answer only in the
process. The catalog serves them:

- `tools.listBehaviors(principal)` (`GET /behaviors`, `list_behaviors`)
  lists a summary of each registered behavior, by name: `{ name,
  description, requires, conflicts, fields, operations }`, the last two
  by name.
- `tools.describeBehavior(principal, name)` (`GET /behaviors/{name}`,
  `describe_behavior`) is the behavior's declaration (section 3.16 of
  `docs/extension-model.md`) with what a reader would otherwise have to
  know filled in: `requires`, `conflicts`, `fields`, `operations` and
  `vetoes` as lists, empty when the declaration has none, and each
  operation's `scope`, `writes` and invocation policy, the declaration's
  or the default, under the policy's key after `writes`, as the describe
  document writes it (D11); an operation beside the instance has
  `"beside": true` after `writes`, `"log": false` when it appends no
  event, and `"asks": "read"` when it asks read (D77); a namespace
  operation's `scope` is `namespace`. `configSchema`, `createParamsSchema` and
  `preconditionSchema` are as declared, and absent when the behavior
  takes none. A name the engine does not run is `not_found`; one that is
  not a behavior name, `invalid_argument`.

```json
{
  "name": "test.Flag", "description": "Holds an instance still while it is flagged.",
  "requires": [], "conflicts": ["test.Tally"],
  "fields": [{"name": "flagged", "description": "Whether the instance is flagged."}, ...],
  "operations": [{"name": "flag", "scope": "instance", "writes": true, "invocationPolicy": "ask",
                  "paramsSchema": {...}, "resultSchema": {"type": "boolean"}}, ...],
  "vetoes": []
}
```

Every caller may read the catalog, and the policy is not asked: it is the
deployment's registered code, the same in every namespace and for every
schema, and carries no schema's or instance's data, while a policy
question names a schema. The describe document already shows a schema's
behaviors to whoever may read it. The list is summaries and a behavior's
document a call of its own, because every declaration together runs to
over a hundred kilobytes, which an MCP client would carry in its
context.

### The invocation policy and the vendor keys

They are the deployment's binary's registrations in the compiler, which
the engine cannot read, so they are `openEngine` options, the core's by
default:

```ts
openEngine({
  path, policy,
  tools: {
    invocationPolicy: { key: 'confirm', values: ['never', 'always'], default: 'never' },
    invocation: { delete: 'always', defineSchema: 'always', 'Payments.capturePayment': 'always' },
    keys: { scalar: 'x-acme-scalar', guidance: 'acme/operation-guidance', parameters: [{ key: 'x-acme-arguments', value: 1 }] },
  },
});
```

- `invocationPolicy` is D11's key, values and default. A built-in
  operation (`create`, `get`, `list`, `update`, `delete`, `lookup`) or
  engine tool (the schema tools, the behavior tools, `getValue`,
  `search` and the namespace tools, `listNamespaces`, `createNamespace`,
  `archiveNamespace` and `unarchiveNamespace`) takes `invocation`'s value
  for it, else the default; a
  behavior operation takes `invocation`'s value for
  `<Behavior>.<operation>` (`Payments.capturePayment`, `acme.Rating.rate`),
  else its declaration's `invocationPolicy`, else the default, in its
  tool, the describe document and the behavior's own document.
  Registration refuses a declaration whose value is not one of the
  values, as the compiler's `Finalize` does. The core's behaviors name
  none, so they register under any policy; a deployment marks the ones
  it wants confirmed, such as the commerce operations that move money or
  stock (D70), through `invocation`. A generated server takes it from
  the engine database's `outputs.engine.tools.invocation`, which the
  build requires to name each commerce operation that moves money or
  stock wherever `Payments` or `Stock` is composed (D70, amended).
- `hidden` lists behavior operations, by `<Behavior>.<operation>`, that
  the tools leave out: a service calls them through the API or
  in-process, and no agent is offered them. Such a tool reads `hidden:
  true` with its reason, and a call by its MCP handle is
  `UnknownToolError`; the operation still runs through `instances`. A
  generated server's engine document hides Payments' service operations
  (`listPaymentActions`, `recordPaymentEvent`, `settleRefundFor`) beside
  the database's `outputs.engine.tools.hidden`; an engine opened by hand
  hides only what it is given.
- `keys` are `apigen.ToolKeys` (section 3.14 of `docs/extension-model.md`):
  the key a property names its scalar under, the `_meta` key of a tool's
  guidance, and keys written at the root of every argument schema.

`openEngine` refuses, with a `TypeError` naming every problem, what the
registry refuses: a key `@mcp` already uses or not a letter followed by
letters, digits or underscores, no values, a value that is not lowercase
letters, digits, `_` and `-` starting with a letter, a value listed twice,
a default or an `invocation` value outside the values, an `invocation`
value that is not a string or an `invocation` that is not an object, an
`invocation` name that is neither a built-in tool nor
`<Behavior>.<operation>`, a `hidden` that is not a list of
`<Behavior>.<operation>` strings, and a parameter key that is empty,
repeated, the scalar key or one the core writes. Once its behaviors are
registered it refuses, as a usage error, an `invocation` or `hidden`
name for a behavior it does not run or an operation the behavior does
not declare.

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
  id, the rate limit, the timeout, the body limit, the service step with
  `authenticateService` and the authentication gate with the deployment's
  `Authenticator`, or with `identity` ("Users" under "HTTP"). Its caller,
  an end user, a service acting for one or a service standing in for one
  ("Authentication and access" under "HTTP"), is the principal of every
  call, so the access policy answers each. An unknown namespace is the HTTP API's 404 problem; a request
  without a caller is 401.
- The engine speaks the protocol itself (D58, `rust/engine/src/api`):
  MCP's stateless streamable transport, each request answered on its own,
  on the 2025 revisions (`initialize`, `tools/list`, `tools/call`; GET and
  DELETE answer 405, since there is no session) and on 2026-07-28
  (`server/discover`), so instances of a server share nothing for it.
  `engineMcp` mounts it on Hono under Node.js and Bun, and brings no HTTP
  server of its own; the tests speak to it with the official client
  (`@modelcontextprotocol/client`).
- `tools/list` lists the visible tools of the tools document: the handle
  as the name, the title, the description, the arguments as `inputSchema`,
  `annotations.readOnlyHint`, and `_meta` with the tool's guidance and its
  invocation policy under the policy's key. The list is the caller's: a
  tool the policy refuses is not in it, nor, in an archived namespace, a
  tool that writes there. `define_schema` and the namespace tools
  (`list_namespaces`, `create_namespace`, `archive_namespace`,
  `unarchive_namespace`) name their schema or namespace only when
  called, so the list asks the policy a listing question for each
  (`define`, or `manage` with what the tool does; "Access"): a caller the
  policy would refuse does not see them, and a call of one still asks
  the question its call asks. A namespace operation's tool (D77) is
  listed where a schema the caller may read composes its behavior, and
  asks the listing question `{ principal, action, namespace, behavior,
  operation, listing: true }`. The tools that only read, `list_schemas`,
  `describe_schema`, the behavior tools (`list_behaviors`,
  `describe_behavior`) and `get_value`, are in every caller's list: each
  answers with what the policy lets the caller read, and the behavior
  tools ask it nothing. The namespace tools act on the namespace they
  name, whichever namespace's endpoint serves them. `search` is in the
  list of a caller who may read a schema that composes Search, and
  searches the ones it may read.
- `tools`, an option of the mount, narrows each caller's tools further:
  `(principal, tool, namespace) => boolean`, asked of every tool with
  `{ handle, name, schema?, operation, behavior?, writes }`, true to keep
  it. A tool it leaves out is not in that caller's `tools/list`, and a
  `tools/call` of it is the invalid-params error of a tool the namespace
  does not have, so a deployment gives an agent's session a narrower set,
  one schema's operations say, with nothing else to call:

  ```ts
  app.route('/api', engineMcp(engine, {
    ...options,
    tools: (principal, tool) => principal.claims?.session !== 'agent' || tool.schema === 'jobs',
  }));
  ```

  It narrows only: a tool the policy hides stays hidden, and what a kept
  tool's call may do is still the policy's. It is synchronous, and only
  `true` keeps a tool.
- `tools/call` returns the result as JSON text and, when it is an object,
  as `structuredContent`. A call the engine refuses is a tool error:
  `isError`, with the problem document the HTTP API answers with as text
  and as `structuredContent`, a veto's `details.code` included (a
  behavior's defect is the 500 problem, its failure off the wire). A tool the namespace does not have, or one of a
  schema the caller may not read, is a JSON-RPC invalid-params error
  (-32602); a malformed message is the SDK's JSON-RPC error.
- `serverInfo` (the package's name and version by default) and
  `instructions` are what `initialize` reports.

The route validates no `Origin` header: it requires a caller, and a
deployment that serves it to a browser on a local address puts the SDK's
`originValidationResponse` in front of it.

## The client

`@superschematic/engine/client` is a typed client of the HTTP API, its
event stream and the controller pattern over it (D16, amended). It
imports nothing of the server side, Node.js or the HTTP runtime, so it
runs in a browser and a worker runtime as well as a server: a test reads
the built files for imports, and `tsconfig.client.json` compiles it with
the DOM's globals and no Node.js types. It declares the wire's shapes
itself; `test/client-types.test.ts` holds each to the server's type.

```ts
import { EngineClient, isVeto } from '@superschematic/engine/client';
import { signedTokenSource } from '@superschematic/http-runtime';

const client = new EngineClient({
  baseUrl: 'https://jobs.internal/api',             // where engineApp is mounted
  auth: { getToken: () => session.token, refreshToken: () => session.refresh() },
  serviceCredential: { token: signedTokenSource(edgeKey, { issuer: 'indexer', subject: 'indexer', audience: 'jobs' }) },
});

const job = await client.instances.create('jobs', { title: 'Index' }, { id: 'index', behaviors: { Links: { batch: 'b1' } } });
await client.instances.update('jobs', job.id, { title: 'Reindex' }, { expectedSeq: job.seq });
await client.instances.invoke('jobs', job.id, 'heartbeat', {}, { preconditions: { Lease: { token: 3 } } })
  .catch((error) => { if (isVeto(error, 'Lease', ['lapsed', 'token_stale'])) stop(); else throw error; });
```

| Calls | Route |
| --- | --- |
| `namespaces.list`, `get`, `create`, `archive`, `unarchive` | the namespace routes ("Namespaces"), each naming the namespace it acts on |
| `schemas.list`, `live`, `draft`, `version`, `define`, `publish`, `describe` | the schema routes and the describe document |
| `instances.create` (`{ id?, behaviors? }`), `get`, `list` (`{ limit?, cursor?, where? }`), `lookup(schema, key)`, `update`, `delete`, `invoke`, `operate`, `invokeSchema` | the instance, lookup and operation routes |
| `events.read` (`{ after?, limit?, schema?, instanceId?, kinds?, behaviors?, exclude? }`), `events.head`, `events.subscribe` | the event route, as a page or the stream |
| `behaviors.list`, `behaviors.describe`, `tools()`, `search(params)` | the behavior catalog, the tools document, the search across schemas |
| `behaviors.invoke(behavior, operation, params?)` | a namespace operation's route (D77): its result |
| `signals.send({ schema, instanceId, behavior, name, data? })` | the signal route ("Signals"): 204, nothing; a 429 `rate_limited` is not retried |

- **Options.** Every call takes `namespace` (the client's `namespace`,
  `default`, otherwise), `signal` and `forward`. A write (`update`,
  `delete`, `invoke`, `operate`) also takes `expectedSeq`, sent as
  `If-Match`, and `preconditions`, sent as the `Preconditions` header.
  `operate` returns `{ result, seq }`, the sequence from the `ETag`.
  `timeoutMs` (30000, 0 for none) bounds each call; the stream's covers
  its headers only.
- **A missing instance** is a thrown 404 `not_found`, as is a delete of
  one: over HTTP a schema with no live version answers the same 404, so
  `get` does not return undefined as the engine's does.
- **Problems.** A refusal is an `EngineProblem`: `status`, `code`,
  `detail`, `requestId`, `details`, and `issues` (`details.issues`, each
  `{ path, rule?, message }`: a JSON pointer for parameters, preconditions
  and a schema document, a field path for an instance), `changes`
  (`incompatible_change`), `fields` (`details.fields` of a `conflict`
  over unique fields), and `floor` and `head` (of a `cursor_expired`,
  410: where a read may start again, "Retention"). A `vetoed` problem is an `EngineVeto` with
  `behavior`, `action`, `reason`, `vetoCode` and `vetoDetails`;
  `isVeto(error, behavior?, codes?)` and `isProblem(error, codes?)`
  branch on them. An answer that is not a problem document (a proxy's
  page) keeps its status and has no `code`. A request that got no answer
  is an `EngineTransportError` (`timedOut` when the client's timeout ended
  it); a caller's own abort rejects with its signal's reason.
- **Credentials**, as the generated TypeScript SDK sends them (D15,
  amended; D37). `auth.token` is the end user's token the client holds,
  which `setToken`, `clearToken` and a refresh replace; while it holds
  none, `auth.getToken` answers per request. `serviceCredential.token(fresh)`
  goes in `Service-Authorization` (or its `headers`) on every request: the
  HTTP runtime's `googleIdTokenSource`, `tokenFileSource` and
  `signedTokenSource` fill it. A call's `forward` sends that end user's
  token instead of the client's, or no `Authorization` when it has none,
  so a service stands in for one.
- **The retry rule.** A 401 `service_unauthorized` asks the service source
  for a fresh token (`token(true)`) once and retries; the end-user refresh
  does not run. Any other 401 runs `auth.refreshToken` once, shared by the
  calls that meet it together, holds its token and retries; it never asks
  for a fresh service token, and a forwarded user is never refreshed.

### The event stream

`events.subscribe(options)` reads the stream over fetch, since
`EventSource` cannot send `Authorization` or `Service-Authorization`, and
parses it by the HTML standard's rules (`SseParser`). Iterate it once:

```ts
const subscription = client.events.subscribe({ after: 'head', schema: 'jobs', exclude: ['heartbeat'] });
for await (const message of subscription) {
  if (message.type === 'ready') continue;          // { type: 'ready', cursor }, once per connection
  handle(message.event);                           // { type: 'event', event }, each event of the log
}
```

- It starts after `after`, a cursor or `head`, else at the start of the
  log, which after retention is the oldest event kept, with the event
  route's filters.
- `cursor` is the id of the last message handed out: an event's, a
  ready's, or one that only carries an id past events the filters dropped
  ("The event stream" under "HTTP"). It moves as each message is handed
  out, not as the parser reads ahead.
- A dropped connection, or a stream the engine ended, is opened again
  with `Last-Event-ID: <cursor>` after `reconnect`'s backoff (500 ms
  doubling to 30 s; `false` to end instead), and `onReconnect` hears why.
  It neither repeats nor skips an event. A 5xx, a 429 (after its
  `Retry-After`) or a network failure is retried; a 4xx ends the
  iteration with its problem, a policy that now refuses the caller say,
  or the 410 `cursor_expired` of a cursor retention has pruned past,
  whose `floor` and `head` say where a new subscription may start.
- With `signals: true` it opens the stream with `signals=1` and also
  hands out each signal as `{ type: 'signal', signal }`, an `EngineSignal`
  (`SignalMessage`; the subscription's type says so). A signal moves no
  `cursor`, and a reconnect never replays one.

  ```ts
  for await (const message of client.events.subscribe({ after: 'head', schema: 'tickets', instanceId: 't1', signals: true })) {
    if (message.type === 'signal') showTyping(message.signal.from, message.signal.data);
  }
  ```
- Breaking out of the loop, `close()` or the options' `signal` ends it
  and cancels the connection.

### Reconciling

`reconcile(client, options)` is the controller pattern: a handler per
event of a namespace's log, filtered, in log order, from where the last
run left off.

```ts
import { reconcile } from '@superschematic/engine/client';

const reconciler = reconcile(client, {
  schema: 'projects', kinds: ['create', 'operation'],
  cursor: { load: () => readCursor(), save: (cursor) => writeCursor(cursor) },
  handle: async (event) => syncProject(event),
  onError: (error, event, attempt) => log.warn({ error, cursor: event?.cursor, attempt }),
});
await reconciler.stop();                            // after the handler in progress returns
```

- It subscribes after the cursor `cursor.load()` returns, or at `start`
  (`head` by default; 0 replays the log) when it returns none.
  `memoryCursor()`, the default, keeps it in the process.
- The handler runs once per event, one at a time, and the cursor is saved
  once it returns: an event is handled at least once, again after a crash
  between the handler and the save.
- A handler that throws is retried on the same event after `retry`'s
  backoff (500 ms doubling to 30 s) and never skipped; `onError` hears
  each failure, and each dropped connection with no event.
- A ready saves the stream's cursor, which may be past events the filters
  dropped, so the next run does not scan them again.
- `done` settles when it ends: after `stop()`, or with the problem that
  refused the stream (which `stop()` rejects with too), the 410
  `cursor_expired` of a stored cursor retention has pruned past among
  them: it never skips what it did not handle.

The work-queue package's worker (`runtime/engine-workqueue/README.md`,
"The worker") is built on the client.

### Typed wrappers

The client is generic over schema names and takes `unknown` data and
parameters. `superschematic engine-client --out <file.ts> <schema file>...`
(D49 in `docs/DECISIONS.md`; the CLI reference has the command) writes a
module that types it for given schemas, from their documents and the
behavior declarations the binary registers:

```ts
import { EngineClient } from '@superschematic/engine/client';
import { taskClient } from './engine.client.ts';

const tasks = taskClient(new EngineClient({ baseUrl, auth }));
const task = await tasks.create(
  { title: 'Index', kind: 'build', detail: { target: 'index' } },     // TaskFields: detail is a BuildDetail for kind build
  { id: 't1', behaviors: { Links: { project: 'p1' } } }              // TaskCreateParams: project is a required link
);
task.behaviors.Workflow.status;                                       // TaskState
await tasks.transition('t1', { to: 'doing' });                        // to: TaskState
await tasks.listLinked({ name: 'project', id: 'p1' });                // a schema-level operation; name: TaskLinkName
const { result, seq } = await tasks.operate('t1', 'addBlocker', { schema: 'Spec', id: 's1' });
```

- Per schema: `<T>Fields` (own fields, a create's data), `<T>` (a read's
  data, the own fields), `<T>Behaviors` (a read's behaviors: by behavior
  name, each behavior that declares a field, its fields `readonly`,
  optional where the engine can leave one out), `<T>Patch`, `<T>CreateParams` and
  `<T>Preconditions` by behavior name, `<T><Op>Params` and
  `<T><Op>Result` per operation, `<T>Operations`, `<T>Vetoes` with
  `is<T>Veto`, and the wrapper `<Schema>Client` with its factory.
- The core's behaviors narrow what their declarations say under their
  configs: `Workflow` the status and `transition`'s states (`<T>State`);
  `Links` its names (`<T>LinkName`), the create parameters as
  `createParamsSchema(config)` narrows them, and the `targets` field per
  link with its target's schema; `Dependencies` the blocker schemas, a
  blocker's schema required when the type's own is not among them;
  `Rollups` each rollup's value by its function; `Revisions` a
  proposal's patch (`<T>Patch`) and a revision's data (`<T>Fields`);
  `Variants` the own fields as a union by `by`, with `field` typed per
  value and none for another value. `Comments`, `Dependencies` and
  `Assignment` type their fields, and the work-queue behaviors type each
  of theirs: `Lease`'s holder and times, `Budget`'s meters by the
  config's names, `Retries`' counts by the config's classes. Any other
  behavior keeps its declaration's shapes and its fields are `unknown`.
- The generator narrows the create parameters in Go, and the engine in
  `createParamsSchema`; `runtime/engine/testdata/client_codegen_parity.json`,
  which `go test ./internal/generator/engineclientgen -update` writes,
  holds the two to one shape, and the field types to what reads return
  (`test/client-codegen.test.ts`).
- A scalar is typed as its JSON on the wire, not by its library's symbol:
  `Temporal.DateTime` is a string, `Generic.JSON` `unknown`, an object
  scalar `JSONObject`.

## Development

```
cd runtime/http/typescript && bun install --frozen-lockfile && bun run build
scripts/engine-napi.sh --js-behaviors   # from the repository's root: the test addon, into runtime/engine/typescript/native
cd runtime/engine/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test        # build, then test:node (node --test) and test:bun (bun test)
ENGINE_TEST_PG_URL=postgres://... bun run test:node   # the backends on Postgres too
```

The tests are TypeScript that Node.js runs with type stripping and Bun
runs as is; they import the built package from `dist/` and open real
SQLite files in temporary directories, on the Rust engine through the test
addon, whose bridge runs the behaviors the tests write with
`@superschematic/engine/testing` (on the addon a release ships they are
refused). A test that loops over `test/helpers.ts`'s `backends` runs on
SQLite, and with `ENGINE_TEST_PG_URL` set on Postgres too, through
`openEngineAsync`, each engine in a schema of its own, every call awaited
(`stream.test.ts`, the event stream, and `native-async.test.ts`). A test
of what the TypeScript engine was, its SQLite seam, its migration chain,
its value drivers, its SQL plans, went with it; each has its twin in the
Rust workspace's tests. `test/http.test.ts` and `test/operations.test.ts`
drive the routes through Hono's `app.request`; `test/stream.test.ts` reads
the event stream and `test/mcp.test.ts` speaks MCP with the official
client (`@modelcontextprotocol/client`) to a listening server,
`@hono/node-server` on Node.js and `Bun.serve` on Bun.
`test/tools-parity.test.ts` asserts the Go vectors. `test/client.test.ts`
and `test/client-stream.test.ts` drive the client against an engine
served in process, its fetch handing each request to the app's;
`test/client-types.test.ts` holds the client's types to the server's and
reads the built client for imports. `test/client-codegen.test.ts` runs
the modules `superschematic engine-client` writes (`test/generated/`,
which `go test ./internal/generator/engineclientgen -update` rewrites)
against an engine, and its `typos()` holds a misspelled field, state,
link and parameter to a compile error. The work queue's suite
(`runtime/engine-workqueue/typescript`) runs the same way, its behaviors
the addon's, by name.

The Rust workspace builds and tests with `make engine-rust`, under the
toolchain `tools.env` pins (`RUSTUP_TOOLCHAIN`). Its tests run on SQLite,
and on Postgres too when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names
one, each test in a schema it creates and drops; the test kit's replays
read the corpora at `testdata/scenarios` and `testdata/http`, or the
directories `ENGINE_CORPUS_DIR` and `ENGINE_HTTP_CORPUS_DIR` name, and
fail when there is none:

```
docker run --detach --name engine-pg --env POSTGRES_PASSWORD=superschematic --publish 55432:5432 postgres:16-alpine
SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL='postgres://postgres:superschematic@localhost:55432/postgres?sslmode=disable' make engine-rust
docker rm -f engine-pg
```

The Go binding (`go/`) builds and tests with `make engine-archive`, which
stages the core's archive, then `go test ./...` in `go/` with
`CGO_LDFLAGS="$(scripts/engine-archive.sh --print)"`; `make engine-go`
replays the corpora through it over the test kit's archive, on the same
databases; `make engine-link-check` links the three archives into one
binary.

### The Rust engine through its Node-API addon

`rust/napi` (`superschematic-engine-napi`) is the Rust engine's Node-API
addon as a library: what an addon does, and `export_engine_napi!`, which
a cdylib crate expands once over the behaviors it is built with into the
exports Node.js and Bun load, the twin of the C ABI's
`export_engine_c_abi!`:

```rust
fn behaviors() -> superschematic_engine_napi::BehaviorSet {
    superschematic_engine_behaviors::core().with(acme_behaviors::Rating)
}
superschematic_engine_napi::export_engine_napi!(behaviors = behaviors);
```

An engine registers the core's of those behaviors when it opens, then
each other one its options' `behaviors` name, in their order; a name the
addon is not built with is refused, naming the ones it has. The library
exports nothing itself, so a crate that expands the macro depends on no
other addon crate. `rust/addon` (`superschematic-engine-addon`) is the
core's addon, over the core's behaviors and the work queue's. A project
whose schemas compose an extension's behaviors builds its own from the
`napi/` member of the engine crate the `projectengine` generator writes
(`engine/rust/<schema>/napi`, over that crate's `behaviors()`), and an
engine opens on it when the host names its file: the engine option
`addon` (`openEngineAsync({ ..., addon })`), or
`SUPERSCHEMATIC_ENGINE_ADDON` for every engine the process opens that
names none. A named file that does not load is refused; no other addon is
tried in its place. Otherwise the loader (`src/native/addon.ts`) takes the
checkout's `typescript/native/`, then the platform package.

`scripts/engine-napi.sh` (at the repository's root; `bun run build:napi`
here) builds the core's addon for this host into `typescript/native/`,
which git ignores, as `superschematic-engine.<triple>.node`, and prints
the file's path; `--debug` builds it quicker and slower to run, `--dist`
with the release's profile, and `--js-behaviors` builds the test addon,
with the bridge to behaviors written in TypeScript (the `js-behaviors`
cargo feature, which no shipped addon has: only the test addon exports
`bridgeCall`). `--crate <dir> [--out <dir>]` builds a project's addon
crate instead, into `--out` (the crate's `native/` unless given), never
this package's `native/`:

```
export SUPERSCHEMATIC_ENGINE_ADDON="$(scripts/engine-napi.sh --crate dist/engine/rust/shop-ratings/napi --out /tmp/shop-addon)"
```

It needs the superscalar checkout (`scripts/superscalar-dep.sh
--checkout`), and builds with the toolchain `tools.env` pins: a crate of
this workspace `--locked` to `rust/Cargo.lock`, a project's crate against
the lock file its workspace resolves (the generated crate commits none).
napi 3.13.0, napi-derive 3.6.9 and napi-build 2.5.0 are pinned exactly,
as superscalar pins them, and napi-derive-backend at 6.1.4, which a fresh
resolve would move past to a release napi-derive 3.6.9 does not compile
with: `rust/napi/Cargo.toml` holds it as a build-dependency with no build
script, so every workspace that builds an addon over the library resolves
it so. Rebuild the addon after a change to the Rust engine, or after
merging one that ports more behaviors.

A release ships the core's addon prebuilt, one npm package per target,
napi-rs's per-platform layout: `@superschematic/engine-darwin-arm64`,
`-darwin-x64`, `-linux-x64-gnu` and `-linux-arm64-gnu`
(`typescript/npm/<target>/package.json`, with the `os`, `cpu` and, on
Linux, `libc` npm installs it by, and the `.node`), which
`@superschematic/engine` names as optional dependencies at the release's
version, so an install gets the one for its host. The release's
`build-archives` job builds each on its platform's runner with
`scripts/release-engine-addon.sh`, which builds it with the `dist`
profile (`rust/Cargo.toml`: fat LTO, one codegen unit, symbols stripped),
loads it, checks it is the core's, speaks this facade's contract, has no
bridge and opens an engine on SQLite, and packs it; on Linux it logs the
newest glibc symbol version the addon needs, which the runner's glibc
sets. A Docker image builds the addon from source, as a project's is.
`scripts/bump_version.py` keeps the packages' versions with the rest.

`src/native/` is the TypeScript over it, in two shapes:

- `openEngine(options)` returns an engine with this package's
  synchronous, principal-first API (`namespaces`, `schemas`, `instances`,
  `events`, `tools`, `values`, `runner`, `behaviors`, `search`, `storage`,
  `close`), each method a call by name with JSON in and out on the
  JavaScript thread, and each refusal this package's error class, its
  members restored. It runs on SQLite.
- `openEngineAsync(options)` returns the same API with every call a
  promise, made off the JavaScript thread (tokio's blocking pool on
  Postgres, libuv's pool on SQLite), for a server: `postgres: { url,
  schema?, poolMax?, listen? }` opens it on a schema of a Postgres
  database in place of `path`, where a synchronous call is refused
  (`sync_on_async_storage`), since it would hold the JavaScript thread
  while it waits on the database. Its options are data: a function written
  in TypeScript (a policy, a matcher, `ids`, a behavior) is refused, a
  `TypeError`. `events.watch` hears every commit, another process's on
  Postgres included.

The options are `EngineOptions` (`src/native/options.ts`). `allowAll` and
a `permissionPolicy()` without a matcher of its own become the Rust
engine's `"allowAll"` and `"permission"`, and the Rust engine's `{ rules }`
passes as it is; any other policy, a `permissionMatcher` and an `ids`
function stay TypeScript, which the addon asks on the JavaScript thread
during a synchronous call (with an `ids` function, a behavior's own UUIDs,
which Branches draws, come from `crypto.randomUUID`). A clock function is
read once for each call that may use the time, and for each request
`engineApp` serves, and sets the Rust engine's manual clock. `behaviors`
takes the name of a behavior the addon is built with beyond the core's,
the core addon's the work queue's (`Lease`, `Assignment`, `Queue`,
`Presence`, `Blueprint`, `Budget`, `Retries`) and a project's its
extensions' too, which it registers in Rust after the core's, in the
order given; a behavior object is TypeScript code to run, even one whose
declaration names a behavior the addon has (a test's stand-in).

The test addon's bridge (`src/native/bridge.ts` over
`rust/napi/src/bridge.rs` and the engine's `dynamic` behaviors) runs a
behavior written with `defineBehavior` on the JavaScript thread: its
hooks, its context's methods back into the engine, its SQL through the
engine's checker, its errors and vetoes. An engine that asks TypeScript
anything is hosted: it is called on the JavaScript thread alone, and its
runner runs on the event loop, woken by `events.watch`. The shipped addon
refuses such a behavior. Beside the bridge, on any addon:

- `engine.storage` is raw SQL on the engine's SQLite file, and a
  `storage.transaction` the engine's calls run inside;
- `schemas.validator(principal, name)` returns a validator per version,
  holding a value to it on the Rust engine;
- `engineApp` and `engineMcp` serve either engine as Hono adapters over
  the engine's own routes (`api::handle`): the engine owns routing, the
  media types, ETags and preconditions, the envelope, the problems, the
  event stream's bytes and MCP's JSON-RPC, and the adapter keeps what the
  host keeps (the request id, the rate limit, the timeout, the body
  limit, authentication, the identity routes, a mount's tool filter and
  its `onError`);
- a value JSON cannot carry (`NaN`, a `Date`, an `undefined` list item, a
  function) crosses the addon marked with this package's words for it,
  and the Rust engine refuses it at the point the TypeScript engine did,
  in those words.

A value driver written in TypeScript (`values.driver`) is not on the Rust
engine, whose drivers are Rust `ValueDriver`s compiled into it.

### The corpus every binding replays

What the engine does is recorded as data under `testdata/`, which the Rust
engine, its bindings and the servers replay. It was first recorded from
the TypeScript engine, which the Rust engine was held to; since the
cutover it is recorded from the Rust engine, through this package's suite
on the test addon. With `ENGINE_RECORD=<dir>` set, every engine a test
opens through `test/helpers.ts` records each call the test makes on it, and
every app a test serves through `test/recorder.ts`'s `servedApp` or
`recordedApp` records each exchange; each test file's scenarios go to
`<dir>/scenarios/<file>.json`, or `<dir>/http/<file>.json` when it served
HTTP. `testdata/scenarios` and `testdata/http` hold a curated set of them,
which `test/corpus.test.ts` records again under Node.js and holds to the
committed files. `testdata/sqlite` holds a file the TypeScript engine
wrote with every core and work-queue behavior in use, as SQL text, with
the reads expected from it, frozen at the cutover, which
`runtime/engine-workqueue/typescript/test/sqlite-corpus.test.ts` holds
the engine to. Each directory's README gives its format.

```
ENGINE_RECORD=/tmp/corpus node --test 'test/*.test.ts'   # record every test file
UPDATE_ENGINE_CORPUS=1 node --test test/corpus.test.ts   # rewrite testdata/scenarios and testdata/http
make engine-rust engine-go                               # replay them through the Rust engine and the Go binding
```
