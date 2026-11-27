# Decisions

This file holds the decisions that are not built yet, and what built
decisions left for later. A decision leaves it once it is built: the
code, the docs site and the commit that built it are its record from then
on.

Source comments, commit messages and the docs cite decisions by number
(D1, D2, ...). Every entry from D1 to D119, built or not, is in this file
as it stood at commit e8e685a51, the last commit that held them all:

```
git show e8e685a51:docs/DECISIONS.md
```

A line number below written `L 11252` is a line of that version, where
the item's design and the alternatives not taken are written out.

## Writing here

- A new decision is appended at the end of this file, numbered one past
  the highest number any branch has used. D199 is the highest so far.
  Check every local branch before taking a number, since an unpushed
  branch may hold the next one. A number is never reused, even after its
  entry has left.
- A change that builds part of an entry removes that part and says what
  is left. A change that builds all of it removes the entry, and its
  commit message names the decision.
- A change that reverses a decision, built or not, adds a new entry that
  names the old one (`D27 amended`).
- Write in the style of the docs (`docs/README.md`): plain ASCII, short
  declarative sentences, decisions in a table beside the alternatives not
  taken.

## What built decisions left

Each item is work an entry named and did not build. The decision's own
text is in the version at e8e685a51.

### Not built

**D2** (L 25). The root `registry` and `loader` packages are still alias
packages over `internal/registry` and `internal/loader`. Making the real
packages public is a mechanical change.

**D10** (L 227). The core's `x-superschematic*` vendor-extension keys are
literals (`apigen/openapi_docs.go`, `apigen/tools.go`,
`envgen/values_schema.go`). A distribution renames them only through the
OpenAPI and tool hooks, and `values-schema.json` has no seam. A naming key
for the prefix is open (`docs/extension-model.md` section 11, gap 8).

**D14** (L 654).
- Go cannot tell a missing required string from `""`: `json.Unmarshal`
  decodes both to `""` (`req_str_absent` in the parity matrix's
  `knownDivergences`; D87 repeats it for Go routes, L 12260).
- A filter on a list of locations (L 4931). A `POINT[]` field keeps
  `ArrayStringFilter`, which adds no SQL condition; `ArrayStringFilter` is
  applied to no list at all (`ormgen/templates/query.tmpl`).

**D17, D19 and D32** (L 2076, 2310, 2479). Schema-epoch transforms for a
version graph are not designed: `Materialize` refuses a commit from a
newer epoch (`ErrSchemaEpoch`).

**D18** (L 2175). `format --to=ts` refuses a type, an operation or a
schema with an extensions slot (`docs/extension-model.md` section 11, gap
6). JSON and YAML carry it.

**D30 and D37** (L 3062, 3659, 3946). Kubernetes targets (GKE, hosted
Kubernetes). D37 designed their service auth: the caller sends a projected
service-account token, audience the callee, ten minutes, re-read from its
file each minute; the callee verifies it against the API server's
`/openid/v1/jwks`, the caller in `sub`. The runtimes' `TokenFile` source
exists; the target and its connectors do not, and the cluster CA is open
(`docs/stack-model.md` section 9.9).

**D37** (L 3633).
- Rotating the service-auth keys (L 3660, 3962, 4342; D63, L 7022). Two key
  slots per edge, each replaced every 180 days and offset by 90, by the
  `time` provider's `Rotating` node; the callee accepts both and the caller
  signs with the younger; callees deploy first. Today each edge has one
  slot, and a key changes only when its node is replaced.
- A credential the callee config cannot express, such as a mesh's mTLS
  identity in `X-Forwarded-Client-Cert` (L 3671). A platform kind of
  credential comes with the first platform that needs it; until then a
  deployment passes its own service authenticator.

**D45** (L 4060). Where a pull request keeps migration acknowledgments.
The generated CI passes no `--allow` or `--expect`, so a CI deploy refuses
any hazard of the default classes.

**D47** (L 4394). A check that the committed workflow is current.

**D53, amended** (L 6237). A list of locations or durations (`POINT[]`,
`INTERVAL[]`) in a queue's message. The Go ORM refuses such a queue, and
the TypeScript and Rust types leave it out.

**D53 and D68** (L 9388). The worker scaler, designed below under "The
worker scaler".

**D58** (L 6165, 6171, 8698).
- A project's engine crate built against a release, with no `[paths]`
  engine keys. The release ships no engine crates, so a release image of a
  server on an engine database gets no Dockerfile, and `stack dev` refuses
  it. D103's steps 5 and 7 make this moot.
- The engine-servers script does not prove the TypeScript runtime's 1 MiB
  body limit on the engine's body routes.

**D60, amended** (L 7892, 8008). A sort by several fields, a sort on a
list that does not page, and a typed sort value in the SDKs.

**D61, amended** (L 8099). An app that serves several services' JSON APIs
(today at most one, at `/api`), and a picker that pages through its
matches.

**D62** (L 6825). SQLite's FTS5 index with the trigram tokenizer for
`search_text`. SQLite scans with `LIKE`.

**D63** (L 7024). A deploy of an environment that mixes providers. It
needs each provider's credentials in the run, a build and pin per
platform, each platform's secret store for `Secret<T>`, a migration runner
per database platform, and policy rules that leave another provider's
nodes alone (also D91, L 12589, and D92, L 12752: Hyperdrive to a Cloud
SQL database the gcp target places).

**D64 and D67, amended** (L 8570, 8591, 9570). A collector's headers from
a secret, on gcp and on Workers, and a collector the target runs.

**D67** (L 7804).
- Metrics through each SDK's meter provider: request durations, queue
  claims. Only Go's memory store records metrics, on the global meter,
  which `telemetry.Start` does not export (D75, L 10420).
- Logs: the current span's `trace_id` and `span_id` on each JSON line in
  every runtime, then OTLP log export through each SDK's log bridge.
- A message's span continues the trace that enqueued it, which needs a
  trace-context column in the queue tables.

**D68** (L 8361, 9476; D96, L 12990; D80, L 11361). `stack queue`
(`dead`, `retry`, `drop`) on SQLite and on D1. `queueops` is Postgres only.

**D69** (L 8468). A planner per pull request. `planner` admits every pull
request in every project with a plan job.

**D31, amended** (L 8521, 9320).
- The parity matrix (`internal/generator/parity`) has no union field.
- No compatibility check holds a Go enum's members as a wire contract.

**D71** (L 9735). A template per scenario, such as an API with its
database and sign-in. Once the authoring packages are published, `init`
writes paths to them and no `[paths]` table.

**D72** (L 9764, 9778). `stack dev` rebuilds and restarts what an edit
affects; a check of what only a generator finds (a worker whose queue is
no `@queue` class); an editor integration over `--json`.

**D73** (L 9887). Derived inputs from linked instances. Derived's `from`
takes own fields only, on the engine and on a table.

**D74** (L 10738, 10794).
- Go derivers, a deriver over an HTTP engine, a model-label construct, a
  cost cap, and evaluating every `{ rules }` policy rule in order at the
  build.
- Search's embedder in a stack: nothing runs `staleEmbeddings`.

**D75** (L 10285, 10420).
- Metrics in the TypeScript and Rust memory stores.
- A memory store on Cloudflare (KV or a Durable Object), a platform of its
  own to design.

**D77** (L 10239). Vectors over messages; Search's every-schema search on
namespace operations; `readable()` asked per operation; the notifier and
the agent dispatcher in Go and Rust (D153 built the notifier and D158 the
dispatcher, each a queue's worker, in TypeScript); a conversation UI. Comments runs on
the core now (D103's status lists what its port left).

**D78** (L 10885). Go and TypeScript apps; the Topcoat asset bundle in the
app image (so a deployed live list is a GET form); an app on Cloudflare; a
readiness check deeper than a TCP connection.

**D79** (L 10999).
- The `@versioned` history readers in TypeScript (`orm.ts`; also D90,
  L 12025).
- `IfVersion` fences in TypeScript (D106, L 17540).
- TypeScript and Go repositories on SQLite and D1 (D116 built Rust's).
- The Go ORM writes the zero value for an unset required field with
  `Default<T, V>` (`Product.inStock` false, not true). D88 fixed `"now"`
  alone (L 11059, 11114).

**D44, amended** (L 10643). Listing a user's sessions and revoking one,
which the identity store has no call for.

**D82** (L 11156). A prebuilt of the engine's archive and Node-API addon
for a TypeScript project whose types compose behaviors
(`scripts/prebuilt_deps.py` packs superscalar and the version graph); and
the prebuilt families published and pinned (every key and digest in
`prebuilt.pin` is empty).

**D83** (L 11222).
- A CHECK for a `pattern`, for the list rules (lists of an enum among
  them; `sqlutil.ColumnCheck` skips lists), and for a scalar's own bounds
  such as Money's range.
- A plan that takes raw SQL down around a change to the columns it reads:
  a materialized view over a column the plan retypes fails the step
  (L 11280).
- `SET DEFAULT` as an `onDelete`.

**D80** (L 11332). `when` rules of `@enqueueOnChange` beyond the literal
ones (`isNull`, `notNull`, `equals`). A delay in an `@enqueueOnChange`
trigger's message: a delay is an enqueue's, in Go, TypeScript and Rust,
and a trigger enqueues due at once.

**D81** (L 11425).
- Password reset and email verification in Go and Rust, and routes the
  identity service serves for them; D153 built both in TypeScript
  (`passwordReset`, the channel identities' codes) as functions an app
  mounts.
- An approve action on Topcoat's user pages (they enable and disable).
- Files up and down in the Rust server and SDK, which refuse them.
- An MCP endpoint in the Go server (D93 and D116 keep Go refused).
- Per-route scaffolds in TypeScript.
- Webhook tokens other than ES256: RS256, EdDSA.

**D85** (L 11536).
- `Encrypted<T>` of types other than a string, which needs a type tag in
  the envelope.
- A blind index for equality lookups.
- Encrypted fields in the engine, in version graphs and in the identity
  tables.
- Encryption in Python, which has no ORM.
- A schema-wide naming strategy; `@table` and `@column` on engine and
  version-graph tables and on `Session`, `UserCredential` and
  `UserRoleGrant`, which are refused.

**D84** (L 11685). `halfvec`; vectors on SQLite; a list of vectors as a
column; a decimal or a vector in a queue's message; the digit and
dimension rules on an operation's argument; version-graph value classes
for `NUMERIC` and vectors; Topcoat's currency for `Finance.SignedMoney`.

**D89** (L 11789).
- A raw query read through a table in Go (`Tx.Query` returns pgx rows).
- `createMany` as one statement whatever its size, which fails past 65535
  parameters.
- `upsertMany`, and an upsert only if changed, over a key that is an
  expression (D105 restates it, L 17452).
- An encrypted field's second statement once per batch, not an `UpdateOne`
  per row.
- Upserts of many rows on SQLite and D1.

**D90** (L 11926). A `materialized: true` projection and its refresh; Go
includes of references no `@hasMany` field holds; `includeDeleted` per
include; an order and a limit on a one-to-many include; an include on
`nearest`; Go's member selection on a projection's read; `@hasMany` loaded
by Go's `FindOne`; a sort through a relation.

**D86** (L 12027).
- Declared queries on SQLite and D1, and in Rust and Python.
- Query codecs for maps, lists of lists, unions, `POINT`, `INTERVAL` and
  encrypted fields.
- A sort parameter on a paged query (also D112, L 17838).
- The lock sees a change to a `@rawSql` piece a query reads (D95 built the
  view half; also D95, L 12860).
- Nullability finer than an outer join's table.
- The generated CI runs `queries verify`.

**D87** (L 12146). Lenient decoding of a webhook input's nested types; a
hand-written Rust operation that acts as its caller without an explicit
`acting(user)`; a manual route's handler bound to its caller (it calls
`withCaller` itself).

**D88** (L 12264). `Default<T, "now">` on a queue's message; seeds of rows
other than roles; `stack queue backfill` on SQLite; a backfill when a plan
changes an existing trigger's `when`.

**D93** (L 12372).
- Confirmation on the engine's MCP endpoint, which publishes
  `invocationPolicy` and confirms nothing (also D98, L 13164).
- Elicitation for clients of the 2025 revision, which needs sessions
  (`Mcp-Session-Id`) in a shared store and a relay between replicas.
- A shared single-use confirmation store by default, through a reserved
  memory-store declaration (D75). The default is in-process.

**D91** (L 12468). An internet NEG for an API on another provider behind
a gcp site; the redirect prefix on gcp, whose load balancer rewrites no
answer header; `origins` in acme-shop's stacks.

**D92** (L 12596). An identity store over D1, and a TypeScript server over
a sqlite-first `authDb`; Hyperdrive in a parameterized environment; a
least-privilege config per API; a separate migrator user on the origin;
more than one drainer of a worker; the stack
managing the Hyperdrive config and its caching.

**D94** (L 12785). 67 first-error map walks whose message can name a
different bad entry on another run; a CI check for a new early exit from
a map walk; an audit of walks that append in map order unsorted.

**D95** (L 12832). Named fragments with parameters of their own
(`@queryFragment({ name, sql, params })` used as `{{name}}`); a number, not
only a string, as an interpolated constant (a `LIMIT`); a projection's
computed column judged non-null.

**D96** (L 12989). A generated CI job that runs `stack queue`.

**D97** (L 13084). `smoke.sh Dev` in CI's full tier; a domain on the
smoke's gcp environment, which a `Secure` cookie needs; Cloudflare names
past 63 characters shortened rather than refused; a check that
`<account>@<project>.iam` fits in 63 bytes.

**D98** (L 13165). A pre-check of what only the implementation sees
(owner, revision) through a dry run; of a manual route's own reading of
its request; a confirmation that survives a refusal at the run, such as
the rate limit's.

**D99** (L 13199, 13236). `searchText` on a projection keyed by the column
it collapses by; search over a projection's joined columns; the engine's
`where` with `allOf` and `anyOf`.

**D100** (L 13346). Scaffold checks that run: `check` reports no scaffold
warning, `build --scaffold` outside a stack checks no implementation, and
a stack's build checks no Rust app's crate; the Go implementation's
`go.mod` checked for a replace its API module gained; a data-form comment
whose first line is a banner kept through a TypeScript writer round trip.

**D102** (L 13539). A build restored from a shared cache records its
stamp; a migrations directory outside the service directory read for the
next stamp.

**D106** (L 17455).
- Nested views loaded through `include` (D90) in a result.
- A fenced update or delete at the `_version` the caller read, in
  TypeScript.
- An upsert by `PUT`.
- The shorthand `@tableOperations(Issue)` over a whole table, its five
  operations from the table and its views (also D111, L 17787).
- Go handlers for a projection's read and list, a view holding a list, a
  map, a JSON value or an embedded object, a path parent key that is no
  UUID and a table key that is not a UUID, text or an integer.
- Runs against Postgres of a projection's read and list in TypeScript, an
  `Encrypted<T>` column through the table operations, and Go's table
  operations over a table that composes behaviors.
- Topcoat's filter form controls for a range, `isNull` and negation (also
  D112, L 17848).

**D107** (L 17642). Decoders that normalize in Go, TypeScript and Rust,
as Python's do; Reactions' own config rules checked at the build; a
`@manyToMany` join table that either ORM reads.

**D108** (L 17684). A caller named through another table (a team's
members), through path filters (D90) in a rule; a behavior's seat as a
row rule's member; Go handlers for a row-rule member that holds text, and
for a create whose member holds an id other than `Identity.UUID`; a row
rule over a projection.

**D109** (L 17728). A team's own named states tied to the Workflow's; the
Go server's create that names its start state; the short transition form
in a JSON or YAML schema file (also D113, L 17922).

**D111** (L 17789). A per-kind invocation policy for a set.

**D112** (L 17834). An index-served keyset over a nullable column (two
seeks joined by `UNION ALL`); the nulls-last keyset on a `@query`'s
pages; "or" between a filter's members; a helper that copies a typed
filter into a repository's whole; a filter through a relation without a
projection.

**D113** (L 17917). What a dependent does when its blocker is reopened
(on a table a finished blocker reopened blocks again); the same on an
engine type, where `finished` is refused until it is designed.

**D114** (L 17971). A key's use log beyond `lastUsedAt`; a hint of the
token in the list; rotation as one call; rate limits per key; a key bound
to an origin or an address; confirmation of a tool call made with a key,
past an agent run's (D158 asks before a run's call its autonomy policy
holds, the run's token a grant); keys on Topcoat's account and
administration pages; a test of the
engine's own server authenticating a key.

**D118** (L 18532). A 409 for a guard's refusal that names the two
states; a `@tableOperation` update that asks the helper before it writes;
a guard of both versions' moves between a migration's phases; a machine
on an imported enum in a JSON or YAML schema file; the old-name guard left
by a rename of a guarded column in a SQLite file read back; a team's own
names for a machine's states.

**D119** (L 18588). The behaviors' core advancing a fence, and with it a
fence on a table that composes a behavior; `UpdateOneIf<Field>`, as
`UpdateOneIfVersion` is `_version`'s; a fence on a version graph's member;
a generated server's own answer to a fence's refusal.

### Waits on superscalar

- **D3** (L 37). superscalar has no release, so `superscalar.pin` and
  `scripts/superscalar-dep.sh` stand in. When it releases, the build step
  becomes its `fetch_release_archive.sh`, and the TypeScript and Python
  paths become published package versions.
- **D31, amended** (L 9357). Whether `Temporal.Duration` takes a sign.
- **D84** (L 11727). A `decimal` format hint in superscalar's Go metadata.
- **D107** (L 17644). `Text`, `Float64` and `Boolean` in superscalar's
  `Generic` namespace.

### Never run live

Everything below is built and proven offline. No live run is recorded:
D97's smoke deploy, which would prove most of it, has not run, and no
release tag exists.

- **gcp.** A TypeScript server, a Rust server, a TypeScript job and a Rust
  job and worker on Cloud Run (D51, D57, D52, D65); a job on Google Cloud
  (`maxRetries: 0` arrives as 0, Cloud Scheduler runs it with
  `roles/run.invoker` alone) and an `onDeploy` job (D52); a worker, and a
  TypeScript one (D53); a bucket (`planner`'s `roles/viewer` and object
  reads), and a Rust workload's (D54); a site: the fallback's anchor, the
  root-path rewrite and the year-long caching (D55); `cpuIdle` (D30); a
  loopback call (D37); the deploy lock, the migrator's results grant, the
  bucket lifecycle rule, `MigrationPreparer` and the migration error read
  from Cloud Logging (D46); a queue operation (D68); an engine server
  against Cloud SQL and the migration job readying an engine schema (D58);
  Cloud Trace with each runtime's spans and the metadata server of a
  worker pool and a job (D67); a memory store's first run: the PSC
  endpoint through the egress subnet, the primary endpoint, node types,
  the IAM condition, the deployer's roles, the CA chain and token re-auth
  (D75, L 10405); a deriver (D74); D91's origin: the browser's Host through
  a serverless NEG, `fullPathMatch` with `pathPrefixRewrite`, the route
  rules' error response serving the fallback and passing the API's 404,
  and `INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER`; a bucket's CORS rules,
  and a browser's preflight and upload from a page against them (D148);
  an image the stack adds packages to or extends: a Cloud Build of two
  steps whose second reads the first's image from the build's daemon, a
  Go and a Rust binary on `debian:trixie-slim` as `nonroot`, and Bun's
  image taking `apt-get install` (D144); a worker in drain mode: its
  Cloud Run job, an execution a wake starts with the metadata server's
  token and `roles/run.invoker` on the job alone, a wake the job refuses
  before the worker's wave has rolled out, the Cloud Scheduler tick, a
  session's advisory lock held through the Cloud SQL connector for a
  task's life, and a task stopped at its day's limit (D145).
- **Cloudflare.** The storefront on Workers and a D1-backed TypeScript API,
  the target's own done line (D64, L 7086); `TestRealD1`, which the D1
  migration driver waits for (D27, L 3012); destroy order, the bucket
  token's scope, presigned PUT and GET, preview permissions, `Secrets
  Store Read`, the planner without R2 read, a D1 `id` equal to its uuid,
  R2's conditional write and DELETE, the R2 lock, a loopback call, the
  Workflow running the current deployment, `params`, token scopes,
  `nodejs_als`, spans from `waitUntil` and an EU bucket (D64, L 7194,
  7325, 8374, 8386, 9597); `runWorkerFirst`, a service binding with
  workers.dev off, a binding's Host and client address and a streamed body
  (D91); a cookie sign-in through the site's Worker on the real runtime
  (D91, D92); Hyperdrive's binding and `Hyperdrive Read`, a connection and
  TLS to Neon, Supabase or Cloud SQL, pgvector created by the origin's
  user, Cron wall time and CPU in the 50 s window, six connections per
  invocation, node-postgres on the runtime, and a destroy that leaves the
  config and origin (D92); argon2id's CPU at the default cost (D92);
  the engineer's token reading the Hyperdrive config and TLS from the
  deploying machine (D96); an R2 bucket's CORS rules, the `r2BucketCors`
  node a plan refreshes with the planner's token, and a browser's
  preflight and upload from a page against them (D148).
- **GitHub Actions.** A generated workflow (D47), one against an engine
  checkout (D58), and `release.yml` with the archive fetch from a
  published release (D47); the provider mapping of D69 and its CEL.
- **Mixed.** An environment that mixes providers has never deployed (D63).
- **D97.** The smoke runs on gcp and Cloudflare, and their timings.
- **SQLite.** A worker claiming outside the tests (D53).

## The worker scaler (D53 and D68)

A worker's settings take `maxInstances` and `instances` (D53, amended), and
no target scales a worker between them. The design, from D53's amendment:

A scaler, designed and not built. Cloud Run worker pools have no
autoscaling: the Admin API's `WorkerPoolScaling` documents
`manualInstanceCount` alone, and Google's answer is CREMA, a separate
Cloud Run service running KEDA's scalers that sets the pool's count. So a
scaler is ours to run:

- *Where it runs.* A Cloud Scheduler job per environment, every minute,
  runs an execution of the migration job (D68, amended) with a `scale`
  command: it already reaches every database as the migrator, needs no
  new image, and works for a pool at zero instances, which nothing inside
  the pool could wake. A loop inside each worker, behind an advisory lock,
  needs a running instance, so no minimum of zero, and gives the worker's
  own account the right to change its pool.
- *What it decides.* Per scaled worker: depth = ready and due messages
  plus live claims; desired = ceil(depth / concurrency), clamped to
  [`instances`, `maxInstances`]. It scales up at once, and down only after
  desired stayed below the current count for a cool-down of five minutes,
  one step at a time, so a burst does not flap the pool; the last change's
  time lives in a row of a scaler table the migration plan keeps.
- *IAM.* The migrator's account gets `run.workerPools.get` and
  `run.workerPools.update` on each scaled pool alone (a
  `WorkerPoolIamMember` with a custom role). `update` is the narrowest
  permission that sets a count, and it also lets the holder change the
  pool's image and environment: whoever holds the migrator's credentials
  could then run code as the worker's account. That is the cost the
  design must accept or narrow, by a scaler account of its own that holds
  nothing else.
- *Deploys.* The deploy writes `instances` as the count; Pulumi would put
  back the scaler's count at each deploy unless the graph says to ignore
  the field, and the IR has no `ignoreChanges` yet.
- *Cost safety.* `maxInstances` is the hard ceiling, refused without
  `instances` under it; the scheduler costs one job execution a minute per
  environment.

It is not built because three of its parts are not sound yet: the IAM
grant, which would let the migrator's identity redeploy the worker; the
Pulumi field that the next deploy would reset; and a minute's scheduled
job per environment, a running cost for every stack with a range, where
the fakes would prove only the arithmetic. The setting is built so a stack
can state its range, and gcp says why it cannot run one.

What is built in its place for a queue that is empty most of the day is
D145's drain mode: on gcp a worker whose `mode` is `drain` runs as a Cloud
Run job that an enqueue wakes and a tick runs, and exits once its queue is
idle, so it needs no scaler.

## D103. A behavior adds fields, sub-tables, operations and rules to a table that stays a table, its rules run in a pure Rust core every language embeds, and a project defines its types at build time or at run time, never both (D16, D27, D50, D58, D64, D70 amended)

D16 ported a server that takes schemas as data: one table of instances
with their fields as JSON, an event log, and schemas defined and
published while it runs. The behaviors were built on that store. D58 put
the engine in every server and made a DB type that composes a behavior
an engine type: its rows are the engine's, JSON in `engine_instances`,
and `sql`, `orm` and D27's migrations leave it out. So composing a
behavior changes where a type's data lives, and the build then refuses
what only a table has: a relation either way, `@versioned`, a queue, a
projection, `AutoGenerate`, the `User` trait. A finances app whose line
items need reports, a projection, vectors, bulk upserts and a queue can
compose no behavior on them, so it keeps `isAutoCategorized` and
`categoryConfidence` columns by hand where Provenance would serve, and a
`Note` table where Comments would. The store costs more than that: D64
refuses a database that holds engine types on Cloudflare, D70 serializes
a namespace's commerce writes behind one lock because SQLite has no
exclusion constraint, and a server creates tables at run time through a
role of its own (D58, amended).

A behavior was meant to encapsulate a pattern most software repeats (a
status that is a state machine, time on a resource that can be booked,
money owed and paid) and extend an object that is already persisted. The
framework manages the fields and sub-tables the behavior needs; the rest
of the object is the author's, beside every other schema feature.
Behaviors compose: Workflow with Calendar and Holds is a booking, and
with Payments it is commerce. The Rust library exists so a behavior's
rules are written once for Go, TypeScript, Rust and Python, rather than
by a generator per language held to the others.

This entry records the design before any of it is built.

### What a behavior is

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior is a mixin on a persisted type. It contributes fields, sub-tables, indexes and constraints, operations, reads, timed work and rules, and the type stays a table: its repository, migrations, relations, indexes, reports, projections, vectors, upserts and queues work beside the behavior as they do without it. | Engine types (D58), which move the type's rows into the engine's store; a service kind of its own for behaviors |
| A behavior has state of its own, rules over how that state changes, and recurs across applications. A storage shape alone, or a query alone, is a schema feature. By that test `Links` is `Relation<>`, `Search` is `@searchField` with D84's vectors, `Queue`, `Lease` and `Retries` are `@queue` (D53, D68, D80), and `Branches` is `@versioned` with its version graph (D17, D19), and each of those behaviors goes. Every other behavior is held to the test when it is ported. (Amended by D104: `Lease` and `Retries` stay behaviors a table implements, since an application leases and retries rows that are no queue's messages; `Queue` folds.) | Keeping both, two ways to say each thing, which an agent writing a schema has to choose between |
| A behavior changes another's state only through that behavior's operations, as D16 has it, and two behaviors meet through a named contract. A contract is an interface: D70's supply contract (`holdFor`, `commitFor`, `releaseFor`, `checkHold`) becomes `Supply`, which `Stock` and `Calendar` implement and a project's own supply may. The loader checks that a link `Holds` reads names a type that implements `Supply`, and that every requirement (`Holds` requires `Workflow`) is met, naming the one missing. | Requirements as TypeScript constraints, which `implements` cannot carry, since it adds nothing to the class's type |
| The fields a behavior owns are read-only in the generated repository and SDKs, and only its operations change them. Nothing refuses a raw SQL write to one: an operator who needs to repair a row has a way to, and the documentation and the agent skill (D76) say why not to. | A trigger that refuses the write, which leaves no way out |

### How a schema says it

| Decision | Alternatives not taken |
|----------|------------------------|
| A capability of the whole type is `implements X<Type, Config>`, at most once per type: `Holds`, `Calendar`, `Payments`, `Comments`, `Approvals`. The carrier resolves to the empty object type, as `Trait<T>` and `User<Config>` do, so TypeScript imposes no members. `implements User<{ login: 'email' }>` is the precedent: the build adds `Session` and `UserCredential` beside the table (D50). | `@behavior(name, config)` (D16), a third way beside traits and `User` to say a type has a capability; a decorator factory for a bundle, which the reader cannot evaluate |
| A behavior anchored on one value is a field type, and may repeat: `status: Workflow<{ ... }>` beside `fulfilment: Workflow<{ ... }>`. The field's name prefixes its columns and its operations (`transitionStatus`, `transitionFulfilment`), and the type resolves to the value's TypeScript type, a state machine's to the union of its states. This lifts D16's one behavior per type for a field behavior. Where several fields carry one behavior, a behavior that reads it names the field (`status: 'fulfilment'`); with one, it is implied. It follows `Relation<>`, `Encrypted<T>` (D85) and `AutoGenerate<>`, field types both ORMs act on. | `implements` alone, which allows one state machine per type |
| A config is a type argument of literal types, which the reader resolves through the checker: nested tuples and object types, which the trait reader does not take yet (`traitConfigArgs` reads flat literals), and `typeof` a constant declared `as const`, which keeps a long config readable. A config that names a field is typed by the type it sits on, as `@index<LineItem>` is. | Values in a decorator's call |
| A behavior is declared by an abstract class in the schema language: its fields, its sub-tables as nested classes, its operations as method signatures, its config as a type parameter and its contracts as the interfaces it implements. The build derives from the class the declaration the core reads. A class's bodies declare and never implement: a method body would run in one language. | The JSON declaration beside a Go registration and a TypeScript augmentation of its config (D16), which for `acme.Rating` is `rating.behavior.json`, `ext/behavior.go` and `packages/schema/src/behaviors.ts` beside the crate |

### Three ways to define a behavior

| Decision | Alternatives not taken |
|----------|------------------------|
| A bundle defines no rules. It is a class that implements existing behaviors with the wiring between them preset and exposes the settings that vary: a `Reservation` is `Workflow` with fixed states, `Calendar`, `Holds` with its `takeOn` and `commitOn` states, `Payments` and a `Timers` deadline that expires a hold, and a booking says only its resource, its interval, its amount and its currency. The loader expands a bundle into its parts, so the core never meets one. The reader substitutes a bundle's settings into its parts' configs through the checker, which it does not do yet. | A bundle as a behavior of its own in Rust, which repeats its parts' rules |
| Declarative rules define a behavior with no Rust. The class declares its fields, sub-tables and operations, and its rules come from a fixed vocabulary the core interprets: conditions in the ORM's filter grammar (D90, D99, and `@enqueueOnChange`'s `when`), the fields a state requires or freezes (`StateRules`), reactions (`{ when, then }`), an operation's effects from a fixed list (set own fields, add or update a sub-table row, move a status, adjust a counter), and fields derived as a count, sum, minimum, maximum or average over a sub-table. Nothing else: no loops and no expressions. `acme.Rating` is one class under it: a `ratings` sub-table keyed by the rater, `rate(stars)` with its parameter held to 1 to `maxStars`, which upserts the rater's row, and `ratingCount` and `ratingAverage` derived from it. | An expression language, which grows into a programming language no tool checks; rules in the host's language, which run in that language alone |
| A Rust crate implements a behavior whose rules need code: `Calendar`'s time zones, `Payments`' provider events. The class declares it and the crate implements its hooks over the core's API; the projectengine generator assembles the project's core from the core's behaviors and the extensions' crates, as it assembles its engine now. | WebAssembly plugins, which D58 turned down |

### The core

| Decision | Alternatives not taken |
|----------|------------------------|
| The core is pure. It takes a command (a create, an update, a delete, an operation, a due deadline, an event from the outbox) with the time it runs at, and the state it asks for, and returns a refusal or a change set. It opens no connection and no file and reads no clock. | A core that does its own I/O against the app's tables: a connection of its own, so no transaction with the caller's writes; a socket, so no WebAssembly build; a database driver in every binding. The core as every write's only path, which loses the ORM's upserts (D89) and transactions across tables. Behavior hooks inside the ORM's transaction through callbacks across the binding, which D58 turned down for the policy and which a synchronous core cannot make to an asynchronous host |
| The core asks for what it reads, the host answers, and the core goes on: a row of a type by key, a sub-table's rows by a condition, a count. An asynchronous host (Node.js's pg, Bun) and a synchronous one answer alike. Before the reads a rule across rows decides on, the core asks for a lock key. | One request of everything a command might read, which over-reads every command to serve the few that branch |
| A change set names logical targets: a row's columns that a behavior owns, sub-table rows to insert, update or delete, outbox events, deadlines to arm or clear, and the version each written row is expected at. When a version does not match, the adapter runs the command again from fresh state, a bounded number of times, and then refuses it as `busy` (D58's code). | Physical SQL in the change set, which ties the core to one layout and one dialect |
| A row's writes are held by the version check, the column `@optimistic` already keeps. A rule across rows, the last unit of stock or two bookings of one room, takes the lock its key names: on Postgres a transaction's advisory lock; on SQLite and D1, whose one writer holds only the batch that writes and not the reads before it, a check in that batch that each read made under the lock still answers as it did, which runs the command again when one does not. A generated constraint backs a rule where the dialect has one, as an exclusion constraint does `Calendar`'s on Postgres. | D58's one writer per namespace, which D70 relies on and which serializes every commerce write of a shop |
| Each language reaches the core through a binding: the C ABI for Go, as now; Node-API or WebAssembly for TypeScript, which the change that builds it chooses between on Node.js and Bun; WebAssembly on Workers and in a browser; PyO3 for Python; the crate for Rust. | The engine's bindings as they are, each of which carries a database driver |
| Each language has one adapter, generic and small: it answers the core's reads through the caller's transaction, takes its locks, applies its change sets and runs the version loop, reading the layout (below) to map logical targets to tables and columns. A behavior adds no code to any adapter or generator. | A generator per behavior per language |
| The recorded scenarios (`runtime/engine/testdata/scenarios`) are the specification. Every adapter replays them on SQLite and on Postgres, as D58 has every binding do. | |

### A build-time project

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior's fields are columns of the type's table (`status`, `status_entered_at`); its collections are sub-tables with a foreign key to the row; its indexes and constraints are DDL (D83). sqlgen writes them, D27 plans them, both ORMs read them, and reports and projections may read them. | Storage the engine creates when a schema is published (D16, D58), outside the migration plan |
| A behavior's operations are the API's operations: routes, SDK methods and MCP tools the API generators write from the declaration, each handler a call to the adapter. They are authorized as the API's other operations are (D37, D50), and a permission a behavior's config names is one the API's permissions list. D11's invocation policy and D70's confirmed operations carry over. A read that is a query is a declared query (D86); one that applies rules, as `findAvailability` does, is a call of the core that only reads. | The engine's routes mounted beside the API's, with a policy of their own |
| A trigger the build writes on each table that composes a behavior writes an outbox row per change, whoever makes it, as D80's `@enqueueOnChange` writes a message. Reactions, Rollups and the event stream read the outbox after the commit, and so does the arming of the deadlines a raw write moves, so a raw write is heard, not refused. Since step 3, a write through the core arms and clears its deadlines in the writer's own transaction, from its change set; the outbox is the table's change queue, a D80 queue of its database keyed by the row's key and serialized, and the trigger is on each table whose writes a behavior hears: one that composes a behavior whose declaration hears changes (Timers, Reactions), or that another table's Reactions hears through a to-one relation of it. A table whose behaviors hear nothing (Workflow alone) has none. | Events only from the core's own writes, which an ORM write or a raw one would skip |
| Deadlines and expiring holds are messages of the table's deadline queue, one per row, use and deadline, which a worker of the API runs (D53); a behavior keeps its state of them in a sub-table of its own (Timers' `deadlines`). Since step 3 the queue, not a sub-table, holds the deadlines: a claim and its completion are a queue's. | `<db>-engine-runner`, a deployable that exists only for the engine; deadlines as rows of a sub-table a worker or a job sweeps (D52), which needs a claim of its own |

### A runtime project

| Decision | Alternatives not taken |
|----------|------------------------|
| A project defines its types at build time or at run time, never both. A runtime type never references a build-time row, a build-time table never holds a runtime type's id, and no runtime schema adds fields to a build-time table. (Amended by D117: three modes, development, runtime and compiled, with a database in one.) | A project of both: references across two layouts, transactions that span them, and custom fields on a build-time table |
| A runtime project keeps its types in fixed tables: schema versions, instances with a schema column and a JSON `data` column, and one shared sub-table, keyed by schema and id, for each behavior the project lets its types compose. The DB service's migrations create them, from the behaviors it lists. Publishing writes rows and never a table, so servers keep D46's grants of SELECT, INSERT, UPDATE and DELETE alone, and the engine role and schema of D58's amendment go. A new version may only add, as D16 has it. The core and the adapter are the build-time project's; only the layout differs. (Amended by D117: this layout is development mode's. A runtime type is a table of its own, whose DDL a publish plans and the migration runner applies, so servers still hold no DDL.) | Tables created when a schema is published, which needs DDL from servers that hold none |
| A runtime type has `Relation<>` to another runtime type, held as an id in its JSON and checked by the adapter; `@unique`, `@key`, `@index` and list filters, through tables of their own as the engine keeps them now; `@searchField`, through a shared full-text table; and vectors, through a shared table keyed by schema, id and field. It has no reports, projections or queues. A feature reaches runtime types only once it has a build-time counterpart, so a runtime project expresses nothing a build-time one cannot. | Every schema feature over the shared tables at once |
| Its routes (define, publish, the instance operations, the behaviors' operations, describe, the event stream and MCP) come from a Rust request handler that is pure as well: a request and its caller in, reads asked, a response and a change set out. D58 made the layer neutral to the transport; this makes it free of I/O. The access policy stays data, as D58 has it. | The routes in each language, which D58 turned down |
| Its users are D50's: the fixed tables include its user, role and session shapes, and it serves the session routes every server serves. | Identity of the runtime project's own |
| A library in each language mounts the handler and runs the adapter, for a server written by hand, as `examples/engine-notes` is; a prebuilt server image runs one with no code. It deploys through a stack: the infrastructure stays declared at build time, and only its data types are defined at run time. | A runtime project outside the stack model, with a deploy of its own |
| A whole runtime project converts, once, into a build-time project, for performance: each published schema is written as a class (`format --to=ts`), its behaviors as `implements` and field types; the build writes its tables; a generated data migration moves each type's rows from the shared tables into its own, and each behavior's rows into the type's sub-tables; and clients move to the generated routes. (Amended by D117: a runtime database already holds the tables a build writes, so the move to compiled copies no rows.) | Converting a type at a time, a project of both until the last; runtime routes kept for a converted type |

### What it amends

D16: behaviors compose on tables; the authoring form is `implements` and
field types, not `@behavior`; a behavior is declared by a class; and the
engine's store of instances serves runtime projects alone. A behavior
still changes another's state only through its operations, and a type's
behaviors still run their checks in the order the type lists them. D58:
engine types go, with `enginedoc`'s refusals, the engine database's
`outputs.engine`, its runner deployable and its schema and role on Cloud
SQL; the core does no I/O; and one implementation in Rust, D58's reason,
stays. D70: a commerce write locks its supply, not its namespace, and
`Calendar` may take Postgres's exclusion constraint. D50: a runtime
project's users are its shapes. D64: the refusal of a database that holds
engine types goes with them.

The behaviors keep their names until the change that ports each one;
whether `Workflow` as a field type reads better as `Status`, and whether
`Calendar` with `Holds` ships as a bundle named for booking, is that
change's to settle.

### The order of the work

1. The change set, the read exchange and the core's API, with `Workflow`
   ported and its scenarios replayed in Rust.
2. The TypeScript adapter, then Go's and Rust's, over a build-time table
   with `Workflow` as a field type: the reader's field form and nested
   configs, sqlgen's owned columns and sub-tables, the repository's
   read-only fields and the API generators' operations.
3. The outbox trigger and deadlines on workers and jobs, with `Reactions`
   and `Timers`.
4. The remaining behaviors in waves, as D58 ported them, each held to the
   test, and the folded ones removed.
5. The `implements` form, bundles and the declarative vocabulary, with
   `acme.Rating` rewritten as a class and no crate.
6. The runtime layout, the pure request handler, the library and the
   image, with `engine-notes` and `engine-jobs` on them.
7. The conversion, and engine types removed from the servers and from
   acme-shop's `shop-db`.

Status: steps 1 to 3 built, and step 4's ports of Constants,
StateRules, Approvals, Lease, Retries, Presence, Budget, Seats,
Reconcile, Schedules and Pool, each with D104's upgrade; of Rollups
(over the rows a `HasMany` lists) and Dependencies (its edges a sub-table
of the dependent's), with the core's `Action::Read`, which computes a
use's fields through reads and writes nothing; and of Numbering (a field,
`number: Numbering<{ format, scope?, reset? }>`, its counts a use's shared
table) and Revisions (proposals reviewed into a write of the row's own
fields), with two more primitives of the core: a use's shared table
(`"scope": "schema"`, `ReadRequest::SharedRow`, `ChangeSet.shared_rows`,
each write fenced on its version) and an operation's write of its row's
own fields (`Cx::update`, held to an update's validation and guards);
and of Comments, its messages, edits and reactions sub-tables of the row
and its conversation's marks a shared table, with the core's operation
beside its row (`"beside": true`), which asks only its own use's guard
and writes no row, so a post leaves the row's version as it was; and of
Derived (each field's source, override, attempts and failure a sub-table
of the row, settled by table operations a deriver calls) and Provenance
(who wrote each field, by which behavior and with what confidence, and a
review that gates the Workflow), with the core's attributed write
(`Cx::update_attributed`), a caller's `actingFor` and a field's schema.
Since then, the commerce wave: Stock and Calendar implement the `Supply`
contract (`holdFor`, `commitFor`, `releaseFor`, `returnFor` as operations
of the supply's row, `checkHold` answered), Holds asks it and Payments
pays for a claim, each held to the test and passing it; a supply's write
takes the lock its row names, and on SQLite and D1 every adapter checks a
sub-table read made under a lock against its row's version, refusing one
of a use that writes beside its rows or through an operation of the
namespace. Development mode runs all twenty-five. Left of step 4: the
folded Links, Search, Queue and Branches removed, Blueprint ported and
Variants a field type (D149). Then steps 5 to 7, whose step 6 D117
renames development mode. What the built steps left follows. The
names and rules are reversible until the first release.

Four of step 4's ports met the test in part or not at all: Rollups,
Blueprint, Revisions and Variants. D149 settles each: Rollups and
Blueprint stay behaviors, Revisions keeps its own log, and Variants
becomes a discriminated-union field type.

What Rollups and Dependencies left: a route or field that serves Rollups'
`values` and Dependencies' `blocked`, which only `Action::Read` reaches;
the TypeScript and Rust servers of `fixture-steps-api`, run end to end,
and their SDK goldens; Rollups' `latest`, which needs a creation time the
core can read; `listDependents` on a table (`registry.TableLeavesOut`),
which needs a read across tables outside the core; hearing a blocker's
delete, where a blocker that is gone is ignored; a `status` member for a
table with several Workflows; a config that names the `HasMany` rather
than `{ schema, link }`; and a rollup over a table that composes no
behavior.

What Numbering and Revisions left: their SDK goldens and servers run end
to end, and an API fixture of Revisions; a proposal's patch typed by the
table's own fields, where `Generic.JSON` lets a patch name a field the
table lacks and fail at the approval; `pendingProposals` served by the
adapters and by development mode, which runs no `Action::Read` and so
serves no Rollups `values` either; Retries' `resultField` and Budget's
`limitField` with `setLimit`, which `Cx::update` now allows; Lease's
`directOn` and Reactions' `when.revised`, which wait for Branches; a
Reactions rule on `Revisions.pendingProposals`, which the build refuses;
and an update command that changes nothing, which still writes and moves
the version, as the engine's did not.

What Comments left: claiming and settling deliveries (`claimDeliveries`,
`settleDeliveries`) and their retention's prune, which are recorded and
not claimed; `searchMessages` and `moveAnchor`, which need no new
primitive (the tools still list `moveAnchor`, which the core refuses);
`via` on a message, which needs the calling service on the core's
caller; events for a beside write; reads of a conversation by
page; the testkit's mapping of `behaviors.invoke`, without which the
inbox's steps after a claim stay skipped, and transcripts that record
signals; streamed replies in runtime mode, whose layout gives a Comments
table no deadline queue (`registry.TableArmsDeadlines`), so a `begin`
fails at its apply; signals across processes (development mode's hub is
one process's, and TypeScript's host publishes none yet) and their
`perSecond` limit; and the declaration's descriptions, which still
describe the engine's features. Streamed replies, read state, the inbox
(an operation of the namespace) and `for` are built, on the core's
signals (`Cx::signal`, `Action::Signal`, never stored) and operations of
the namespace (`Action::NamespaceOperation`), and `examples/engine-notes`
runs on development mode.

What the commerce wave left: `Stock.low` in a list's filter, which needs
a column the core sets; a Payments event applied once per namespace, as
the engine applied it, where a table applies it once per table and
provider; D70's invocation policy for money operations, and hiding the
adapter's own operations, on a table (`enginedoc.MoneyOperations` covers
engine types alone); a supply key held as a UUID in two forms, which the
holder check compares as text; Stock's ledger read whole for its next
number, where a count would do; Calendar's exclusion constraint on
Postgres, which D83's DDL has no form for (and which says capacity 1
alone); two claims raced through a Go or a Rust API server; and the
Reservation bundle (step 5).

What Derived and Provenance left: an index for a deriver's pull, which
scans rows a page at a time until a declaration may list a column (step
7); raw writes, which neither hears; the computed `fields` and `stale`
served by the repositories and the API; a server that sets `actingFor`
from its principal; an extraction schema for an object or JSON field;
`usage`, which is taken and not stored; and a `status` member for a table
with several Workflows.

- **Left for later steps.**
  - Refusing writes made in a delete's after-change hooks, which are
    dropped today.
  - `requires` in a declaration.
  - The event stream over HTTP, and SSE or MCP surfaces for a table's
    events: a behavior's own events (`Cx::emit`) reach the app through its
    table's event queue (D104).
  - `holds` and `revised` (step 4: Rollups, Revisions and Branches on the
    core); the build and the core refuse them on a table, and
    `core-reactions-holds-revised.json` is not replayed.
  - A member path below a field in a table's changes rule (a JSON field's
    member, `Timers.deadlines.<name>.fired`): the build takes a field's
    name alone, and the core the engine's qualified form for the corpus,
    refusing a field a behavior computes at a read (core-timers' Reactions
    scenario stops at that define).
  - A link that holds a list of targets: a Reactions rule reads a to-one
    relation.
  - Reactions' declaration still describes `then.link` and
    `allTerminal.link` as links of the engine's Links; its text is
    unchanged so the recorded declarations change by new members alone.
  - A chain of moves across rows that the config's cycle check cannot see
    (a rule on a project moving its task, whose rule moves the project)
    runs until a move finds no transition; the engine's runner stopped a
    chain at its `maxDepth`.
  - Within one change command a listener reads the linking rows as the
    host has them, not with the run's own writes; a write the run makes is
    heard by its own change after the commit. So a task's first change can
    settle its project when later writes in the queue already finished the
    task, as the engine's allTerminal read instances as they were.
  - A listener's move runs as the worker of the table whose write it heard
    (`TaskReactions` moves a Project), holding that worker's permissions.
  - On SQLite two writes of one row within a millisecond are claimed in
    either order: a message's `created_at` is to the millisecond there, and
    its ID breaks a tie. A message that fails holds its row's later ones
    until it runs or is dead (D68's retries, about fifteen minutes); once it
    is dead they run, and an operator's retry of it runs it after them, its
    `then.link` rules acting on its write's images then.
  - An update that changes a row's key is heard as an update of its new
    key; the old key's deadlines stay, each falling due into `gone`. A
    `TRUNCATE` fires no row trigger and is not heard. A raw write of a
    sub-table row is not heard (the trigger is on the row's table).
  - A binary, vector or geometric (POINT, GEOGRAPHY, GEOMETRY) column is
    left out of a write's image and of the trigger's comparison; the build
    refuses a changes rule on one, and a write that moves one alone is not
    heard. SQLite and D1 build an image in
    `json_object` calls of at most 16 columns nested in `json_patch`,
    since D1 caps a function at 32 arguments, where Postgres's calls hold
    50: a null member of the second chunk or a later one is left out of the
    image, and the adapters read null and absent alike.
  - The trigger hears writes after it exists: the plan that adds it writes
    no message for the rows already there, whose deadlines reconcile at
    their next write.
  - A restore of a soft-deleted row (its `deleted_at` cleared) is heard as
    an update of a row that is there, and Timers' reconcile arms a deadline
    only when its fact moved: the deadlines the soft delete cleared are not
    armed again until a fact moves or a `resetTimer` arms one, though their
    sub-table rows stay pending. Re-arming needs the core to know the write
    restored the row, which the images do not say.
  - The Postgres trigger function runs with the writer's rights and finds
    the change queue through the writer's `search_path`, as D80's
    `@enqueueOnChange` function does: a role that may write the table but
    not insert into its change queue, or a session whose `search_path` does
    not reach the queue's schema, has its write refused by the trigger.
    `SECURITY DEFINER` with the function's `search_path` set, or a
    schema-qualified queue, would close it for both triggers.
  - A Reactions target, or a linking table, with several Workflows: a rule
    moves and reads a type's one Workflow.
  - A linked read under a lock on SQLite and D1 is refused as `internal`,
    as a sub-rows read is.
  - A transcript names the version of the command's own type alone; each
    adapter's replay runs a command over every type its database has live,
    each at the version the scenario last made live (a check whose spec
    equals one of its versions, or a command on it). A recording that
    publishes a new version of a listener's type with no step on it before
    a change would replay against the older one; recording each type's
    live version per step would close it.
  - The reference host replays a change command at the wall clock's next
    instant when a scenario's clock is the wall's, as the engine's runner
    read its clock per event, and the replay skips an `events.read` whose
    rows' log holds a change's write (core-timers' `until` added by a
    publish: the writes queued before it are heard under the new version
    and cancel a deadline before it falls due, where the engine's sweep
    cancelled it as it fired; the end state matches).
  - Under `openSqlite` (Bun's `stack dev`) a write's `meta.changes` counts
    the rows its statement's triggers write, a hearing table's change
    message and SQLite's `_version` bump among them, so code that reads it
    after a write of such a table should not expect 1; whether D1's own
    `meta.changes` does the same is unchecked. The adapters' fences use
    SQL `changes()`, which counts the statement's own rows.
  - A generated Rust worker reads a message's `at` as a JSON integer, as
    the Rust types write `Generic.Int64` today; `deadline_of` and
    `change_of` would need to take text if that changed.
  - A read under a lock of a kind other than a row or whether a table has
    rows (a sub-table's rows by a condition, a count). A `subRows` read made
    under a lock is refused as `internal` on SQLite and D1, whose batch has
    no check of it yet; its check comes with the reads above.
  - A prebuilt rules module and archive under D82. Until then
    `@superschematic/rules` builds only where cargo is, and the Go module
    links in a checkout only where cargo is (`scripts/rules-archive.sh`
    stages the archive); a release's archives tarball holds it.
  - The layout holds only tables that compose a behavior, so
    `Cx::create_row` and `Cx::call_on` reach those tables alone.
  - Typed operations (`behaviortypes.Narrow`), the field types and
    carriers in `@superschematic/db` (`Workflow<C>`, `Timers<T, C>`,
    `Reactions<T, C>`), the build's checks of Timers' and Reactions'
    configs against the table (`verify/behaviors_timers.go`,
    `verify/behaviors_reactions.go`) and the listener computation
    (`registry.BehaviorListens`) are per-behavior code, as D58's are,
    until step 5 derives them from a behavior's class declaration. Until
    then an extension's field behavior has the data forms alone, and the
    build checks Timers' rules that read the IR alone: its other rules
    (states, transitions) and Workflow's are the core's define check's, at
    the first command on the table.
  - The implements form for behaviors other than the core's, bundles and
    the declarative vocabulary (step 5).
  - A read of a row's deadlines (`Timers.deadlines`) in the repositories,
    the API, the SDKs and MCP, through `Core::read_fields` or a declared
    read (D86), and a read of a use's sub-table rows in the repositories:
    neither ORM generates a definition, a repository or a relation for a
    sub-table.
  - Upserts on a table whose behaviors keep sub-tables or arm deadlines.
    The Go ORM leaves out the exported upserts alone, and still writes
    their unexported helpers.
  - A check across a stack that some deployed worker runs each placed
    table's deadlines and change queue: only the API's warning exists, and
    the messages wait.
  - Behavior workers over a database that lists `sqlite` first: a
    TypeScript one is refused, and Go's and Rust's claim on Postgres
    alone (D53). A TypeScript behavior worker an environment places on
    Cloudflare Workers is refused too, since `drain.ts` runs a `@worker`'s
    handler alone. So on SQLite and D1 a hearing table's change queue
    grows by a message per write, each holding the row's images, that
    nothing claims or prunes, unbounded where the deadline queue keeps one
    live message per row, use and deadline (D1 caps a database's size): an
    operator may delete them, and the writes they held go unheard. The
    trigger stays there so that a SQLite claim, when it comes, hears every
    write from the first.
  - A Timers config change rewrites no row: each row's deadlines reconcile
    at its next write through the core, and a pending one keeps the due
    time it was armed with until then, though a new `afterMs` would move
    it. The engine's `afterConfigChange` rewrote them at the publish, so
    the replay stops at such a publish, and the lazy reconciliation's
    answers are held by `rules-behaviors`' unit tests alone. The scenario
    that sweeps per namespace stops at its first define, and those of the
    test behaviors `test.Bounce` and `test.Hold` replay on the reference
    host alone, not in a transcript.
  - A deadline's audit stamps on an `Auditable` table, whose worker's
    subject is no user: the TypeScript and Go repositories stamp a
    deadline's row write as an operation's, `updatedAt`, and `updatedBy`
    only when the ORM or the context has a user, which a worker's has not;
    Rust's stamps none, as its handlers stamp none.
  - A fire refused for `busy` or a failure of the database fails its
    message, which falls due again after D68's backoff and is dead after
    its last attempt; its sub-table row stays pending until an operator
    retries it or its fact moves. A veto is released (step 3's second half)
    and comes back every minute for as long as the guard vetoes it.
  - `snoozeTimer`'s `dueAt` is held to an integer of 0 or more, as
    Timers' paramsSchema declares it, with no maximum: one past what an
    adapter stores (JavaScript's 8.64e15 milliseconds, Postgres's
    timestamp range) fails in the adapter as an `internal` refusal or a
    database error, a 500, where the core's `invalid_argument` would be a
    400. Bounding it changes the declaration the engine's recorded HTTP
    and MCP corpora hold, and the API's goldens.
  - The layout's `deadlines` names the queue message's columns (`rowKey`,
    `use`, `name`, `at`, `payload`); the adapters write its lifecycle
    columns and states (`id`, `state`, `claim_token`, `due_at`, ready,
    claimed) as every D53 queue's, as the queue clients do. A layout that
    keeps deadlines elsewhere than in a D53 queue, a runtime project's
    among them, would name those too.
  - The engine's own check of a config takes Timers' and Reactions'
    `status` and ignores it, since the declaration it parses declares it
    for tables: only the build refuses it on an engine type, so a type the
    engine defines at run time keeps it unread, until step 7 removes
    engine types.
  - The permission catalog lists a use's config permission under each of
    its operations (`tickets.timers` under `fireTimer` too), though only
    `resetTimer` and `snoozeTimer` check it, as the item on the catalog
    below says.
  - A `dueAt` in the API is epoch milliseconds, an integer, as Timers
    declares it, not a date-time.
  - Timers' declaration lists `deadlines`, its computed read, as a field
    with no type, which takes no column; the core would let a use `set`
    it, which Timers never does.
  - The reference host refuses a sub-table insert of a key it holds as
    `conflict`, and an update or a delete of one it lacks as `not_found`,
    where an adapter runs the command again; no transcript step reaches
    either.
  - A TypeScript deadline message's `rowKey` is the queue client's text, a
    hyphenated UUID on Postgres, where its type says base62; `runDeadline`
    takes either.
  - A Rust API with a behavior worker and no operation gets no API crate,
    and so no worker main, as an API with an ordinary `@worker` alone; a
    Rust behavior worker's crate reaches the rules crates through the API
    crate's path dependencies, not its own. A behavior worker's `go.mod`
    and `package.json` list its API's implementation, which its main does
    not import.
  - No test runs the generators' whole build of a stack whose Go or Rust
    worker runs a table's deadlines: each one's main is planned from the
    API's output, as the Go and Rust servers of behavior operations are,
    and `fixture-timers-api`'s Go server would be refused by the rule on
    callers below. TypeScript's `timers-ts-stack` is built whole.
  - The Go ORM's `fireTimer` outcome is a string, since an enum the
    declaration gives no type stays one there, where the API's types name
    it `TicketTimersFireTimerResultOutcome`.
  - Timers' declaration still describes `fireTimer` and the config as the
    engine's runner sweeps them (`sweepMs`), which a table's worker
    replaces; its text is unchanged so the engine's recorded declarations
    change by the new members alone.
  - A concurrent create of one key: the loser gets the database's unique
    violation, not the `conflict` refusal.
  - The replay's floor is one total. A floor per kind of step would catch
    a transcript that lost commands and gained checks. The 61 checks call
    the core directly, so the adapter's `hasRows` answer is tested by the
    store's own tests alone.
  - The Rust adapter is unpublished, as the core's crates are, so the
    HTTP runtime's crate (`runtime/http/rust`) does not offer it until
    they publish, and a Rust server with behavior operations builds only
    in a checkout (`[paths] rules_adapter_rust` and
    `rules_behaviors_rust`): the build refuses one otherwise, and release
    mode has no Rust path.
  - The Go ORM stays Postgres-only, so Go's SQLite store has no ORM over
    it, and its end-to-end test runs on Postgres alone.
  - A Go row's owned value field is its declared type (`string`), not the
    config's states, which the ORM's `<Type><Use>State` constants compare
    with by conversion.
  - `UpdateOneIfVersion` and `DeleteOneIfVersion` on a table whose fields
    are behaviors.
  - A row key that is no UUID, given for a UUID-keyed table, is a plain
    error in TypeScript and Go and an `internal` refusal in Rust: a 500
    in each, worded apart.
  - A JSON own field whose value is a string: Go's adapter binds a JSON
    column's value as its JSON text, so the string reads back a string,
    where TypeScript's binds a string as it is. The adapters are to agree,
    and a transcript case would hold them to it.
  - An own field the layout leaves unconverted reaches the core apart in
    each adapter, which no ported behavior reads yet and declarative rules
    (step 5) will. On Postgres, Go reads a `DATE` as an instant's ISO
    text, and a `TIME` or an `INTERVAL` as pgx's struct, where TypeScript
    and Rust read the column's text. A list of date-times is left out of
    the Go ORM's create's own fields, which TypeScript's passes, and reads
    back as RFC 3339 with the server's offset in Go, `toISOString` text in
    TypeScript and `to_jsonb` text with the session's offset in Rust.
  - The three adapters decode a malformed layout document apart: Go
    refuses an entry whose type is not its key, or that names no table,
    key column or version column, which TypeScript and Rust load.
  - `ExecuteOptions`' own columns replace a change set's own fields
    rather than add to them, in every adapter: an operation's audit stamps
    would drop an own field a behavior's operation wrote, which no ported
    behavior does.
  - `@requireOwnership` on a `@behaviorOperations` set: its routes run no
    implementation that could check what the caller owns, so it is a type
    error on the class and the data forms have no key for it.
  - An API's own invocation policy and hiding of a behavior operation's
    tool: the declaration's policy carries over, and an engine database's
    `outputs.engine.tools.invocation` and `.hidden` have no counterpart.
    Only a set whose `@requireService` admits only services hides its
    tools, by the reason the expansion gives them.
  - A stack's Go server of an API neither `public` nor over a user model
    authenticates no caller, so it is refused when a behavior operation's
    set needs one. Giving such an API's `Config` an `AuthMiddleware`, from
    an `AuthMiddleware` of its implementation as a public API's has, would
    lift the refusal, and put its authored `@auth` routes behind it too.
  - The core checks a config's permissions by its own dotted rule
    (`orders` covers `orders.ship`), not by a matcher the project gives
    its routes (TypeScript's `permissionMatcher`, Go's
    `identity.WithMatcher`), so a caller such a matcher admits by another
    rule is refused by the core. The handlers are to hand the core the
    matcher, or the caller's permissions as it expands them.
  - The Rust handler's caller is tested through the principal alone: its
    calling service and its empty caller run in no test, which the Go and
    TypeScript callers' tests cover.
  - The SDKs' types read the owned fields and mark none read-only (no
    `readonly` in TypeScript): read-only in the SDKs means that no
    generated input carries one.
  - A path, handle or namespace option on the decorator. The route and
    the MCP handle follow the table, field and operation, and one whose
    handle passes 48 characters is refused; the SDK namespace follows the
    set's name.
  - A behavior operation whose scope is a namespace, and a table keyed by
    several fields. (An operation whose scope is the schema runs since
    D104's step 3 as a command without a row, routed at `POST
    <type>/<member>/<op>`.)
  - A non-writing behavior operation is a `POST` like the rest; a read
    that is a query, as a declared query (D86) and a `GET`, comes with the
    first behavior that declares one.
  - A config's permissions are attributed to every operation of its use:
    the catalog lists each string under a key `permission`, or since D104
    one ending in `Permission` (Workflow's `overridePermission`), under each,
    until step 5 derives which operation checks which from the class
    declaration.
  - The TypeScript SDK's typed states are the API's enum
    (`OrderStatusState.Paid`), not the literals the repository takes, and
    the TypeScript handler passes the decoded input to the repository
    `as never`: the ORM's parameter type is not checked against the API's
    input type, so a future behavior whose parameters hold a date-time
    would reach the core as the `Date` the API's parser made, serialized
    as ISO text.
  - Behavior operations on a TypeScript server whose database lists
    `sqlite` first (D1, Workers): the TypeScript repositories run on
    Postgres alone, and the build refuses such an API. Nothing bundles the
    core's wasm module into a Worker either.
  - The Rust handlers run on Postgres alone, though the adapter has a
    `sqlite` feature, and stamp no audit field (an `Auditable` table's
    `updatedAt` and `updatedBy`), which the Go and TypeScript ORMs do.
  - The identity routes and behavior operations on one API (an authDb
    with a `User` table whose tables' fields are behaviors) are generated
    together but no fixture or test covers the pair.
  - The Python SDK's operations classes repeat the namespace class's
    private validation and coercion helpers, since a Python namespace
    keeps them as methods.
  - `readOnly` on a `@source` view's field matches the source's owned
    field by name, and does not follow a projection's `@column` alias.
    OpenAPI 3.0.3 ignores `readOnly` beside a `$ref`, so an owned field
    typed by an enum or another component would carry none; Workflow's
    are a string and a date-time.

## D104. A transition may name who makes it and may repeat as no change, a sign-off pins what was read, Retries waits, Budget keeps periods, Presence times by state, Lease and Retries stay behaviors on tables, and Pool, Schedules, Seats and Reconcile join the core (D16, D56, D103 amended)

Built: steps 1 to 3. A Workflow transition takes `by`,
`overridePermission`, `repeat` and `from`; Constants, StateRules,
Approvals, Lease, Retries, Presence and Budget run on the core with their
upgrades; and Seats, Reconcile, Schedules and Pool join it, with the
`Capacity` contract Budget implements. Left: step 4, "Barrier, Mailbox
and Assignment as bundles with step 5, and the `caller` operand of its
declarative conditions", and what the built steps left.

### Step 4

| Decision | Alternatives not taken |
|----------|------------------------|
| The declarative vocabulary (D103, step 5) gains a `caller` operand in its conditions, compared with a field that holds a subject. | Conditions over the row alone, which cannot say whose row it is |
| `Assignment` becomes a bundle of one seat with `max: 1` and `onFull: refuse` (step 5), and its guard on another principal's `acquire` reads that seat. | Two behaviors that say who a row is for |

### Two bundles

Two of the app's patterns need no rules of their own, and wait for step
5's bundles.

| Decision | Alternatives not taken |
|----------|------------------------|
| *Barrier* is Workflow (`waiting`, `released`, `cancelled`), Rollups' `all` over the rows that relate to it, gating `released`, a Timers deadline whose `then` releases it, and a `release` transition behind a permission, a person's go-ahead: an update that waits for every running agent to say it is ready, for five minutes when a person started it, or for a person's go-ahead. It needed Rollups ported over a `HasMany` relation, which step 4 has built. | A behavior of its own |
| *Mailbox* is Workflow (`queued`, `urgent`, `taken`, `sent`, `canceled`, `failed`), StateRules freezing a taken message, `by: ["createdBy"]` on the sender's moves, and Lease on a taken message, whose expiry moves it back to `queued`. A take is the declared read of a conversation's oldest waiting message and Lease's `acquire`, as above. | Comments, whose deliveries tell a subject of a message and are no work an agent takes in turn |

### What the built steps left

The lines are those of the version at e8e685a51, where each item's
context is written out.

- Lease's `directOn` waits for Revisions and Branches on the core and is
  refused on a table; `workqueue-lease-direct-on.json` is skipped
  (L 15632, 17131, 17256).
- Retries' `resultField` is refused at define; Revisions on the core and
  `Cx::update` now allow it. `recordAttempt` still offers `result`, which
  a table's types should narrow out (L 15725, 17137).
- An own-field write from an operation: the engine's `update()` runs
  through validation and the guards of Constants and StateRules
  (L 17048).
- `Queue` folds into `@queue`: its claim a declared read and Lease's
  `acquire`. The recordings that compose Presence or Budget beside Queue
  wait for it, as does `checkReserve`'s `hears` (L 14883, 15070, 17235).
- An operation of the namespace runs on the core now
  (`Action::NamespaceOperation`); a build's table still leaves Comments'
  streamed replies and read state out, and `@behaviorOperations` warns
  (L 17127, 17262).
- Step 5's question: whether Constants' constancy and `oneWay`, and
  StateRules' required and frozen fields, become declarative rules.
  Constants still requires at least one field (L 15306, 17044, 17103).
- The permission catalog lists every permission a config names under
  each of its use's operations, until a class declaration says which
  operation checks which (L 17397).
- The engine advertises and refuses the table-only members until engine
  types go (D103's step 7, which D117 reworks) (L 17028, 17124, 17252,
  17328).
- A table's events outside its queue: an event stream over HTTP, and SSE
  or MCP surfaces (L 16990; D116 left it too).
- More kinds of `by`: a to-one relation by name, `updatedBy`, `deletedBy`,
  an audit time, and a `createdBy` that is a relation or a list. They are
  refused (L 16995).
- Null against absent for a behavior operation's optional parameter, in
  the three languages: Calendar's `setOpeningHours` needs it, and so does
  a Pool pick of members with no value (L 17011, 17384).
- The core is given no encrypted field's plaintext, and no converted list
  or map of date-times (L 17058).
- Which subjects authenticators give alike: Seats canonicalize, while
  sign-offs and lease holders keep the subject as given, so a UUID that
  arrives hyphenated on one route and base62 on another signs twice
  (L 17067, 17152, 17267).
- A gate pinned to the version its `resetOn` fields last moved at
  (L 17076).
- The principal's permissions bound to an authored route's write
  (L 17085).
- An owned `ttlMs` column for Lease, and a Lease `exempt` by the calling
  behavior (L 17143, 17157).
- Retries' attempts, the window's and the streak's, read by a condition
  (L 17167).
- `limitsField` as a typed `JsonField<T>` (L 17170).
- An ORM row's field-anchored values narrowed to their states in Go and
  Rust; TypeScript's row is narrowed (L 17324).
- Pool: `match` and `prefer` over a Workflow state or an owned field, a
  pick of members with no value, more than 2,000 members
  (`pool_too_large`), and the extra rounds of a pick across pages
  (L 17382).
- An operation's idempotency key, left for an entry of its own (L 15063;
  D116 left it too).
- Event order on SQLite: one command's events share a millisecond's
  `created_at`, and `queueDue` orders by `(created_at, id)`, so their
  random ids decide. D116's Rust SQLite worker reaches it (L 17358).

## D116. A Rust API serves a SQLite file through repositories the build writes, answers a request with no transport under it, serves the MCP endpoint and runs behavior workers there, and an `embedded` target builds an environment as one binary that migrates its files as it starts (D27, D53, D57, D79, D81, D103, D106 amended)

Built: the Rust repositories on SQLite and Postgres, with a table that
composes behaviors written through the core; rustrestgen's
`@tableOperation` handlers; `Api::handle` and `block_on`; the MCP endpoint
in Rust; behavior workers on SQLite and `drain`; the Rust runner, recorded
plans and `migrate plan --from-database`; and the `embedded` target, with
`examples/embedded-notes` as one binary that serves a Workflow table and
its MCP endpoint. Since then: a behavior table's `upsert_one`,
`update_many` and `hard_delete_one` through the core, typed methods per
behavior operation, `reencrypt`, a JSON column's comparisons on SQLite,
and workers on SQLite that claim by the time a drain is given and extend
a long handler's claim. What is left follows, and the design of the one
step not built, the `server` feature.

### A request in, a response out

| Decision | Alternatives not taken |
|----------|------------------------|
| A generated Rust API crate gains `Api::handle`, which takes the `http` crate's `Request<Bytes>` and answers its `Response<Bytes>`. It does what the router does for a request it holds whole: it finds the route, authenticates through the `Authenticator`, admits by the route's rule (D43's `admit`), decodes and checks each argument, runs the operation and writes the envelope or the problem (D15). The axum router and `handle` call one generated function per operation, so a request is decoded by one path however it arrived. | A second router written for the purpose, which would drift from the first, as D43 found of a direct caller's checks. A request as a method, a path and JSON, which leaves headers, ETags and preconditions to each host. |
| What needs a socket, a clock or a store shared between requests stays the host's, as D58 divides it: the listener, TLS, the body's limit (`handle` takes a body already read), timeouts, rate limits, CORS, request ids and draining. An operation that streams a file up or down is the router's alone: `handle` does not route it, and the build names each one. | Limits inside `handle`, which would bring it a clock and a store of counts it otherwise needs none of. |
| A host tells the API what it knows of a connection through the request's extensions, which `Authenticator::authenticate` already receives in the request's `Parts`: the peer's address, or the peer's credential on a local socket. The caller is still whoever the project's authenticator says it is. | A caller passed beside the request, a second way to establish one, which the route's own rule would not see. |
| The runtime's axum, tokio and tower dependencies go behind a feature, `server`, on by default, and the generated crate's with them. A crate built without it links no executor and no HTTP server. An implementation's trait stays asynchronous, since an operation awaits Postgres and an SDK's call. A host with no runtime drives `handle` with the runtime's `block_on`, which polls on the calling thread: an API whose databases are all SQLite files, and that calls no other server, awaits nothing that needs a reactor. | A synchronous trait for an API on SQLite: two shapes of every implementation, scaffold and in-process caller. A tokio runtime inside `handle`, which is tokio again. |

### What is left

Not built. Of steps 1 and 2: a `@tableOperation` over a projection, which
stays a method with a warning, the Rust repositories having no
projections; a calling service standing in at a table route, whose
behavior command runs as no one; and an upsert of a table whose behaviors
keep sub-tables or arm deadlines, refused as in Go and TypeScript. Of step
3: the `server` feature, and the build's list of the operations that
stream a file, which `handle` routes as the router does. Of step 4: the
confirmation key of a Rust app; a shared confirmation store from the
generated entrypoint, which a project passes as
`RouterOptions.mcp.confirmation_store`; and a tool of a manual operation
where the manual routes are not mounted, which answers 501. Of step 5:
for a Rust API on a SQLite file an engine, a deriver, an engine's runner
and an app stay refused by name, and an identity store's write that waits
out the busy timeout for the file's writer is the store's database error,
not a 503. Of step 7: the runtime's in-process memory store, every memory
store being refused; a test of a cookie's sign-in from the site's page and
of an API key through the binary; a command that writes the target's
authoring types; a route's own limits under `main.rs`, which reads every
body up to 2 MiB; and the manual-route operations, which the build names.

Left, each of which the application also needs and none of which this
entry designs: a table's changes as a feed a client follows, with a
cursor, an event stream and MCP notifications (D103 leaves the event
stream over HTTP for later); callers that are no user and hold no key
of one (D114), a device or a session acting for a person, and
authenticators that read the connection, shipped; an exhaustive exploration of a table's composed
behaviors on the core, which reads no clock; an operation's idempotency
key and a record moved between two databases, which D104 lists as the
app's; a bucket on a local directory, without which the target refuses
every bucket; jobs in the process; installing the binary as a service,
signing it and updating it; TypeScript's repositories and behavior
workers on SQLite and D1, which step 1's statements serve; Go on SQLite;
and, on SQLite, a declared query, a `@versioned` table's history and
full-text search (D62).

## D117. A project's schemas run in one of three modes, development, runtime and compiled: development keeps rows as JSON, a runtime type is a table whose DDL a publish plans and applies, and compiled is the build (D16, D27, D46, D103 amended)

D103 gave a project two ways to define its types, at build time or at
run time, and gave a runtime project fixed tables: instances with a
schema column and a JSON `data` column, so that publishing writes rows
and no server runs DDL. That put two needs in one layout.

One is a person working out a schema. They change it every few minutes,
make a handful of sample rows and look at whether the shape is right.
Nothing they store matters tomorrow, and a store that asks for a
migration at each change is in their way.

The other is an application that runs where no build can: an interactive
environment, a hosted builder say, where the schemas are TypeScript that
a person or an agent edits, and a change has to take effect with no
generator, no compiler of the generated code and no CI between the edit
and the running app. Its rows do matter. For it the JSON column is the
store D103 took the behaviors off: a relation is an id nothing in the
database holds to a row, a unique field is an expression index, a report
or a projection has no columns to read, and leaving for a build is a data
migration of every row.

So the JSON layout is right for the first and wrong for the second, and
D103's rule that servers hold no DDL, which chose it, is answered another
way below. This entry names three modes and gives the second one tables.
D103's steps 6 and 7 are not built, so nothing built is undone: the
engine's store as it stands (D16, D58) is what development mode is built
from.

### Three modes

| Mode | It is for | Rows live in | A schema change is | Served by |
|------|-----------|--------------|--------------------|-----------|
| development | working out schemas against sample data: low volume, fast iteration | fixed tables, each instance's own fields a JSON `data` column, or a database in memory | a publish that writes rows and takes effect at once | D103's runtime handler |
| runtime | an environment that cannot generate and compile code, a hosted builder say, whose schemas change while it runs | the type's own table, as a build would write it | a publish that plans DDL (D27) and applies it | D103's runtime handler, over those tables |
| compiled | a standard project: generated code, a build, CI and a deploy | the type's own table | a migration plan a deploy applies | generated servers |

| Decision | Alternatives not taken |
|----------|------------------------|
| A mode is how an environment holds and serves a project's types. The schemas are the same in all three: tables written as classes in the schema language, with their behaviors as field types and `implements` (D103), or the documents `format --to=json` writes from them. Nothing in a schema names a mode. | A schema form per mode, which makes moving between modes a rewrite; a decorator that names the store, which puts a deployment's choice in a declaration |
| A database is in one mode. No table of a compiled or runtime database holds the id of a development row, and no publish adds a field to a table a build owns. A project may run in different modes in different environments: a development store beside a person's editor, a runtime environment its users reach. | D103's two kinds of project, which made the sketch and the running app one thing; modes mixed in one database, which is D103's project of both, with references and transactions across two layouts |
| A mode expresses nothing the next cannot: what development accepts runtime accepts, and what runtime accepts a build accepts, with the same meaning. A schema feature reaches development or runtime only once compiled has it, as D103 has it. So a project moves one way, development to runtime to compiled, and never has to be rewritten to move. | A feature of runtime's own, which strands a project there |
| Where the store is opened says the mode: an option of the library that mounts the handler, and for a database a stack places, a setting of the environment. A database that names none is compiled. | A mode inferred from what the database holds, which a half-migrated file answers wrongly |

### Development mode

Built, in part: its layout, `runtime/engine/rust/rules-handler` (the pure
handler: define, publish, describe, the instance requests, a list's
filters, sorts and pages, unique fields, relations, the behaviors'
operations and the fields they compute at a read, deadlines and changes,
the access policy as data in D58's form, routes over HTTP with no
transport under them, the event stream and the tools) and `rules-dev`
(its hosts over a SQLite file, a SQLite database in memory and a Postgres
schema, an axum server behind the `server` feature with MCP, and the
`superschematic-dev` binary), with a publish under no compatibility rule
that lists the rows a version fails and discards them when told to
(`docs/src/content/docs/guides/development-mode.md`). Every rules
transcript replays over the layout, and the recorded access steps through
its policy. Left:

- Built since: the handler on wasm32 with a JSON ABI that imports nothing
  (`rules-handler-wasm`), and the TypeScript library over it,
  `@superschematic/rules-handler` (`runtime/rules-handler/typescript`), a
  development store over `bun:sqlite` or `node:sqlite` whose files the
  Rust hosts read and write, with the routes, the event stream and the
  tools; every transcript replays through it. Left of the libraries: an
  asynchronous store loop (D1, Postgres from TypeScript), a store in memory
  for a browser, runtime mode in TypeScript (its table store, the record
  and a publish through the planner and a runner), the library on Workers
  and in a browser, run, and the Go and Python libraries over the C ABI;
  and the prebuilt image (D103's step 6).
- `examples/engine-notes` and `examples/engine-jobs` on development mode:
  engine-notes runs there (its differences are in its README), and
  engine-jobs waits for Blueprint on the core (D149).
- The policy asked of a behavior's reads of other types (Dependencies'
  blockers, Revisions' evidence, Rollups' links), as the engine asked
  them; the runner's requests made by a system principal the policy sees.
- `If-Match` on an update, a delete and an operation (the `Preconditions`
  header is served); routes for a draft, a version, a lookup, namespaces,
  behaviors, search and values; a notifier under the event stream, which
  polls; acting for a subject (D77); MCP confirmations (D93).
- A scalar's own rules beyond its kind (superscalar's normalization, a
  pattern); `searchText`, `referencedBy`, vectors, a relation that holds a
  list, and namespaces; D50's user, role and session shapes and the
  `createdBy` stamp; unpublishing a type.
- A rename hint in a publish. A rename is a remove and an add, and the
  values do not carry over.

### Runtime mode

| Decision | Alternatives not taken |
|----------|------------------------|
| A runtime type is a table: the table sqlgen writes for the class. A field is a column, a relation a foreign key, a behavior's fields columns of the table and its collections sub-tables (D103, "A build-time project"), and indexes and constraints DDL (D83). The database holds the runner's two state tables and the model it was last taken to (D27). It cannot be told from a compiled project's database. | D103's fixed tables, which are development's now; a third layout of runtime's own, which keeps the data migration at the move to compiled |
| A publish is a migration. It plans from the model the database recorded to the model of the schemas published (D27), and the runner applies the plan, expand and contract back to back as `local` runs them: no server built against the old columns exists, since the handler reads its layout from the recorded model and reads it again when the model's hash moves. Every server of the environment follows a publish with no restart. A publish takes the database's schemas whole, a model being a database's and not a type's. | DDL the handler composes field by field, a second writer of DDL beside sqlgen whose tables then differ from a build's; a publish per type, which cannot add two tables that refer to each other |
| D27's hazards replace D16's compatibility rule. A plan is asked for without applying it, a route and a tool beside publish, and shows its steps and each one's hazards. A publish names the hazards it allows and the renames it means, as `--allow` and `--rename` do, and one with a `destructive` or `data-dependent` hazard nobody allowed is refused with the hazard's id. | The add-only rule, which refuses the rename, the drop and the tightening this mode exists to make; applying whatever the diff says, which drops a column on a typo |
| The planner is the compiler's: its reader, sqlgen's model and D27's diff. A runtime environment carries the `superschematic` binary and runs those and nothing else of it: no generator and no language toolchain. The same binary reads the TypeScript a person edits into documents. `migrate plan` takes documents and a recorded model, with no service directory and no git ref. | A second planner in Rust beside the core, some 13,000 lines of Go to hold a port to, whose model's hash must equal sqlgen's for the move to compiled to be free; a small planner of additions only, which is the add-only rule again |
| A publish carries its plan. Where the compiler runs beside the handler, the library makes the plan there; where it cannot, on a Worker say, the plan is made where the compiler is, a CLI or the platform's own service, and sent with the schemas. The runner refuses a plan whose `from` is not the recorded model, as it does any (D27), so a plan made elsewhere is held to the database it meets. | Refusing runtime mode on a platform with no process to spawn; the planner compiled to WebAssembly first, which may follow |
| The plan is applied by the environment's migration runner, the one a deploy uses, and never over a serving connection: the process itself on a SQLite file (D116), `superschematic-migrate` beside a local Postgres, the migration job on gcp (D46). Servers keep D46's grants of SELECT, INSERT, UPDATE and DELETE, which the runner extends to a new table as it does after a deploy's phase. | DDL granted to every server, D58's engine role, which D103 removed; a role per server that owns one schema, which each new platform then has to learn |
| The handler and the adapter are D103's: the pure request handler, with the access policy as data, and the adapter answering the core's reads and applying its change sets. The adapter reads the build-time layout from the recorded model where a compiled server reads it from generated code. | Generated repositories, which need the build this mode has none of |
| A runtime type has every schema feature whose effect is DDL a plan carries and reads the handler makes from the layout: relations, unique fields, indexes, constraints, search fields, vectors, and the views of reports and projections. What needs code a build or a person writes stays compiled's: an operation with a body, a queue's handler, a job. | D103's list for runtime types, which was drawn for the JSON layout |

Built, on a SQLite file and a local Postgres:
`runtime/engine/rust/rules-runtime` serves the handler over each type's
own table, laid out by `behaviorlayout.RuntimeOf`, which the plan request
returns with `layout: true` and the database records beside its model in
`superschematic_runtime`. A publish plans from the recorded model through
`migrate plan --request`, refuses a plan from another model and a hazard
nobody allowed, and applies expand and contract through a runner, which
writes the record and the servers' grants in the transaction that
finishes the plan (`apply --record`, a job's `record` and
`privileges.readState`). The runner is in process, or a job: the plan,
record and job documents go to a store the job reads, and the publish
polls the recorded model, holding no lock, until it lands, fails with the
runner's error or times out (`JobRunner`; gcp's `RunRuntimeJob` uploads
them to the state bucket and runs the migration job, proved on the fake
Cloud). Every server reads the new layout once the model's hash moves,
and answers `busy` while a plan is in progress. A list's filters, sorts
and pages, unique holders and a delete's cascade run in SQL
(`Reading::Queries`), answering as development mode does; soft deletes
keep the live rows alone. A server role with D46's grants serves what the
migrator publishes. The plan is a route and a tool
(`docs/src/content/docs/guides/runtime-mode.md`). A publish's tables equal
a build's. Left:

- A runtime environment's server on gcp that publishes through the
  migration job: no command or extension hook calls `RunRuntimeJob`, and
  Rust has no GCS or Cloud Run store and launch. A live run then checks the
  server account's permissions (the runtime prefix of the bucket,
  `run.jobs.runWithOverrides` on the migration job, `logging.viewer`), the
  grants inside the plan's last transaction on Cloud SQL, the job's cold
  start against the publish's timeout, failures reported from real
  executions, and two servers publishing at once.
- The behaviors' own reads (a linked row, a holding, a read across a
  table) in SQL. They read every row of a type.
- The `createdBy` and `deletedBy` stamps, and unique holders a partial
  index sees, on a soft-deletable table.
- The access policy, event stream, MCP and HTTP routes development mode
  serves.

### Moving between modes

| Decision | Alternatives not taken |
|----------|------------------------|
| Development to runtime is a publish of the same schemas to a runtime database, whose first plan starts from an empty database. Sample rows stay behind unless asked for; when asked, each is replayed as a create through the type's rules, and the ones refused are listed. | Copying rows across layouts by SQL, around the rules; carrying sample data by default |
| Runtime to compiled moves no rows. The schemas are the classes the project already has, or `format --to=ts` writes them (D103); the build's first plan starts from the model the database recorded, which `--from` takes (D27), and is empty unless the compiler changed; and the generated servers take over the tables. Clients move to the generated routes, as D103 has it. | D103's conversion, a generated data migration from the shared tables into each type's own, which is needed now only for an engine file of today's |
| A General schema of D16's, one instance type and its behaviors, publishes as the table `format --to=ts` writes it as, so `engine-notes` and `engine-jobs` run in development and in runtime mode with their schemas as they are. | A second document form for runtime mode |

### What it amends

D103: "a project defines its types at build time or at run time, never
both" becomes three modes, with a database in one. Its build-time project
is compiled mode, unchanged. Its runtime project divides: the fixed tables
and "publishing writes rows and never a table" are development mode's,
and runtime mode has the build-time layout. "A new version may only add"
holds in neither: development has no rule and runtime has hazards. The
conversion keeps its classes and its routes and loses its data migration.
The handler, the adapter, the library, the image and the user model are
as D103 has them, in both modes. D16: the compatibility rule goes with
the store it protected, once development mode replaces that store. D27: a
plan may be made at run time, from documents, by a publish. D46: a
publish reaches the migration runner, and a server still holds no DDL.
D116 stands: an embedded binary carries recorded plans and no compiler,
and an embedded environment is compiled.

### Left to the change that builds each

- A namespace (D16) in runtime mode: a Postgres schema or a file of its
  own, each with its model, or a column of every table.
- An API service's `@tableOperation` methods (D106) served by the handler
  with no build, so that a runtime environment's routes are the compiled
  project's and clients do not move at the conversion.
- Which platforms run runtime mode first, and a publish's wait where the
  migration runner is a job.
- The setting's name, and the modes' own.

### The order of the work

1. Development mode: D103's step 6 as written, under this name, with the
   database in memory and a publish that discards. Built in part; what is
   left is under "Development mode" above.
2. The planner from documents: `migrate plan` over schema documents and a
   recorded model, and the plan as a route and a tool. Built:
   `migrate plan --documents` and `--request` (`internal/migrateplan`,
   `docs/src/content/docs/reference/migrations.mdx`), and the plan as a
   route and a tool of runtime mode. Left: a Postgres plan of documents with
   `@query` declarations, which is refused; documents that import another
   service's types, which are refused; what a config carries beyond the
   dialect (`viewOwner`, `sourceSchema`, platform defaults, sidecar files);
   and readers, so a plan from documents has no `api-breaking` hazard.
3. Runtime mode on a SQLite file and a local Postgres: the adapter's
   layout read from a recorded model, the handler over it, and a publish
   through the runner. Then a platform whose runner is a job. Built on
   SQLite and a local Postgres; what is left is under "Runtime mode"
   above.
4. The moves: development to runtime with rows, runtime to compiled by
   the recorded model, and D103's step 7.

The names and rules are reversible until the first release.

## D123. `@mcp` says `additive: true` for a tool that only adds, and each visible tool's record carries its MCP annotations from its replay mode and that flag (D93 amended)

Built: the `additive` key and its refusals, and the resolved annotations
in each visible tool's record in `tools/schema.json`
(`ir.ResolveMCPToolAnnotations`, `docs/src/content/docs/reference/mcp-tools.md`).
The MCP endpoints of the TypeScript and Rust API servers and of the
engine send `destructiveHint` and `idempotentHint` from the resolved
record, beside `readOnlyHint`. A Go API serves no MCP endpoint (D81). The
commits that built it name D123.

Left: development mode's tool list
(`runtime/engine/rust/rules-handler/src/tools.rs`) still derives
`destructiveHint` from the call's kind.

## D137 to D148. What a document library needs of the framework

D137 to D148 come from one exercise: porting an application that keeps
documents as ordered trees of parts, which several people edit at once,
publish, search, render in a headless browser and reach from a command
line. Each entry is a thing that application wrote by hand and another
application would write again. They fall in four groups, and each group is
meant to land as one change:

| Group | Entries |
|-------|---------|
| Version graphs | D137, D138, D139 |
| The API runtime | D140, D141, D142, D143 |
| Stacks and deploys | D144, D145, D148, and what D80 left |
| Behaviors | D146, D147 |

Sign-in through an identity provider is not among them. It is its own
decision.

## D138. `@graphOperations(Root)` serves a version graph's operations as routes with no implementation method, and a graph's commits and releases can enqueue (D17, D103, D106 amended)

Built: `@graphOperations(Root, { operations?, permissions?, by?,
overridePermission? })`, which the loader expands into a route per
operation with generated inputs and results, in the OpenAPI, the SDKs and
the MCP tools, served by the generated TypeScript, Go and Rust servers
(`runtime/http/typescript/src/graph-routes.ts`,
`runtime/http/go/graphroutes`, `runtime/http/rust/src/graph_routes.rs`);
the root read first, a ref or a commit held to its root, the row rule and
the engine's refusals as problems
(`docs/src/content/docs/guides/api-routes.mdx`, "A version graph's
operations"); a Dockerfile for a TypeScript server that serves a set,
whose stage of its own builds the version-graph core's wasm module; the
Python and Rust SDKs' types of a set, which compile and call;
`EntityHistory` in the four engines and facades, with the scenario
`entity_history`; and `@versionGraph({ enqueueOnCommit,
enqueueOnRelease })`, as `@enqueueOnChange` triggers on the commit and
release tables, with the scenario `enqueue`, a release's message carrying
its commit's sequence, read from the commit table. Past the text: a
sixteenth route, `compose` (`GET {roots}/{id}/refs/{ref}/tree`), since a
client that edits a change set must read it; `branch` with no `from`
branches the root's primary line and creates it, named `main`, for a root
that has none, since no route creates one; `revert` is under the row rule with the
six the text lists, since it writes a change set too, and so is a
`merge`'s target when it is a change set, since the merge writes it; a
primary line is never discarded through the routes (409
`primary_merge_only`), since the next branch would make it again, empty,
though the engines' `Discard` still retires one, which a root with several
primary lines may need; a release's `commit` and a revert's `toCommit` of
another root are 404, as a path's commit is; and every operation is an MCP
tool without `@mcp` on the class, as a behavior set's are. The commits
that built it name D138.

Left:

- The SQLite layout, which has no trigger, enqueues nothing.
- The Workers entry takes the same options and is untested.
- No MCP case of the scenario is run against the Go or the Rust server.
- No servergen test builds a Rust stack of a graph API.
- A Rust server of a set over a database whose DB service lists sqlite
  first is refused: the Rust server runs a graph's facade on Postgres
  alone.
- A public Go API whose auth schema is not its graph's database is
  refused, since `graphs.go` reads a graph's roots through the ORM of the
  graph's database, and a public API's `Config.DB` is the ORM of its
  auth schema.
- A Go server's graph operations over a database with per-record access
  (an `@audience` table or `@accessGrants`) are refused: the graph's
  facade reads its tables with no caller, and `graphReading` reads as the
  system for a caller whose subject is no UUID, so neither keeps to an
  audience or a grant's reach (D180).

## D139. `released: true` on a graph member keeps the released tree's rows in a table of its own (D17 amended)

Built: `@graphMember({ released: true })` and the `<Member>Released` table
the loader adds, with the member's content fields, indexes and its
relation to the root; the descriptor's `kinds[].releasedTable`; `Release`
rewriting the root's rows in its transaction, in the four engines, on
Postgres and on the SQLite layout, which gains an optional tenth table,
`released`; the scenario `released`; a read-only repository in the Go,
TypeScript and Rust ORMs, the TypeScript one an object of the table's
reads alone, which `TableDefinition.readOnly` asks of `repositories()`;
released reads in the three ORMs that leave out the rows of a
soft-deleted root unless they include deleted rows; and a
`@tableOperation` write of the table refused
(`docs/src/content/docs/reference/version-graphs.md`, "Released rows",
and `runtime/versiongraph/README.md`). The table's key is a row id of its
own with a unique index on the root and the entity key, since a table has
one key column; `Release` of the commit already released rewrites every
row of the root, which backfills a member that gains `released`; a
released member with an `Encrypted<T>` content field is refused; and a
soft delete of the root clears no released row, so a restore of the root
reads them again. The commits that built it name D139.

Left: no fixture declares a projection over a released table, or a
`@tableOperation` read or list of one: they go through the paths every
table has, untested here.

## D140. `@idempotent` makes a repeated call answer what the first one answered (D93, D98 amended)

Built: `@idempotent({ ttl?, required? })`, the reserved class
`IdempotencyKey implements IdempotencyKeys` and its table, the check that
refuses an API whose database lacks it, the route step in the Go,
TypeScript and Rust runtimes and routers
(`runtime/http/testdata/idempotency_parity.json`), with the Rust
runtime's stores in memory, on Postgres and on SQLite; the store a
generated Go, TypeScript or Rust entrypoint builds over the database,
which a Rust API's `Implementations::new` builds over `Deps.db`; the
header and its refusals in OpenAPI; the key a TypeScript, Go, Python or
Rust SDK method sends; and the `idempotencyKey` argument of a tool on the
TypeScript and Rust servers' MCP endpoints (the API routes guide, "A call
that runs once"). The commits that built it name D140, and the first says
where it departs from the table this entry held: the decorator is also
refused on an encrypted operation, a manual route and an operation that
uploads files, the request's hash covers its query, and a row has a UUID
key and a unique hash of operation, principal and key.

Left: an operation's docs do not get `replayMode: idempotent` and the
pointer from the decorator, and the tool documents (`tools/schema.json`,
the SDKs' tool invokers) do not list `idempotencyKey`. The replay
contract holds a pointer to a required string argument of the tool, which
the key is not unless the operation says `required`, and every SDK's
invoker would have to pass it to the method as the key. A tool that both
asks the person to confirm (D93) and is `@idempotent` asks again on a
repeat that the route would answer from the kept answer, on the
TypeScript and the Rust endpoints alike: the endpoint confirms before the
route reads the key, and its check (D98) keeps no key, so it cannot tell
a replay from a first call.

## D142. A bucket puts only when absent, reads an object's head, and may name its objects by their content (D54 amended)

Built: `put` with `ifAbsent` and its `created`, `head`, the Bucket
config's `contentAddressed` and `maxBytes`, and the content bucket in
`Deps`, in Go, TypeScript and Rust over GCS, each of whose uploads sends
`ifGenerationMatch=0` for `ifAbsent`, and in TypeScript over an R2
binding (`runtime/http/testdata/content_bucket_parity.json`, the buckets
guide). The commits that built it name D142.

Left: the R2 conditional write, `onlyIf` with `If-None-Match: *`, is
tested against a stand-in of the binding, not against R2.

## D145. A worker in drain mode runs as a job that empties its queue and exits, and an enqueue wakes it (D53, D68, D92 amended)

Built: a worker's `mode` and `idle` settings and what resolution refuses of
them (`internal/stack/workers.go`); the drain worker's entrypoint in Go and
TypeScript, which takes one of `instances` advisory locks named for its
queue as it starts and exits 0 once no message was ready or held for
`idle` and none falls due within five minutes, which it waits for, and
exits with an error once every claim has failed for `idle`
(`runtime/http/go/worker`, the TypeScript runtime's `./worker`,
`sqlutil`'s `slot` and `nextDue` statements); the wake a queue client sends after an
enqueue, at most once per queue in five seconds per process, which fails
no enqueue (`runtime/http/go/wake` behind the Go ORM's `OnEnqueue`, the
TypeScript runtime's `queue-wake.ts`); and on gcp the Cloud Run job in the
worker pool's place, `roles/run.invoker` on that job alone and
`QUEUE_WAKES` for each deployable that may enqueue to its queue, and the
Cloud Scheduler tick, an hour at most, since it bounds how late a retry
or a delayed message due past that horizon runs
(`extensions/gcp/cloudrunworker.go`). `stack dev`
runs a drain worker as it runs any worker. The stacks and queues guides
and section 8.8 of `docs/stack-model.md` describe it. The commit that
built it names D145, and says where it departs from the decision's text.

A Rust worker drains as well: its slot is the session advisory lock of
the descriptor's `slot` statement, on a connection taken from the pool;
it waits for a message due within the five minutes `next_due` reads and
exits once idle; `worker::Group` runs several queues under one slot; and
the Rust queue client sends the wake (`runtime/http/rust/src/wake.rs`),
at most once per queue in five seconds. gcp runs it as a job. A behavior
worker drains in Go, TypeScript and Rust, and resolution admits it: it
takes one slot for all its queues, whose loops stop together. Each rules
adapter wakes the change queue of every row a unit writes, and so do the
Go and TypeScript ORMs' writes that bypass the core (`UpdateMany`,
`DeleteMany`, `HardDeleteOne`).

Left:

- A wake from an enqueue on SQLite (`Enqueue<Queue>SQLite`,
  `enqueueStatement`), and a slot there, which has no advisory lock: no
  target runs a drain worker over a SQLite file.
- A bound on a task's life shorter than a day, and a setting for it.
- Holding wakes back while the workers hold every slot. A process sends a
  queue's wake whenever five seconds have passed since its last, so on a
  busy queue each enqueuing process starts up to twelve executions a
  minute that find no slot and exit at once, each a Cloud Run Admin API
  call against the project's quota. A wake could be held back for the
  worker's idle time after a `:run` that succeeded, which `QUEUE_WAKES`
  would then have to carry.
- `QUEUE_WAKES` keyed by the database as well as the queue's class. It is
  keyed by the class alone, so two databases that each declare a queue
  class of one name wake each other's drain workers: starts wasted, no
  message lost.
- A raw SQL write of a table a behavior hears wakes nothing: its trigger
  writes the change, and the tick finds it.
- Go's ORM `OnEnqueue` wakes a delayed enqueue at any distance, where the
  Rust and TypeScript queue clients send no wake for a message due past
  the five-minute horizon.
- A plain TypeScript `@worker`'s main does not await
  `queueWakesSettled`, so a wake its handler's enqueue sent just before
  it exits may not go out.
- The Rust ORM sends no wake: its rules host (`rustgen`'s `orm.rs`) has
  none, so a write through the repositories of a database's Rust types,
  a Rust API's table operations and a Rust deriver's settles among them,
  wakes no behavior queue, and the tick finds it. A Rust API's
  `behaviors.rs` does wake.

## D146. A `@deriver` fills the Derived fields of a table, as it fills an engine type's (D74, D103 amended)

Built: `@deriver({ schemas })` in a TypeScript, Go or Rust API over a
database of tables that implement Derived. The resolver admits it
(`internal/stack/derivers.go`), and the gcp, local and fake platforms run
it. The API's `Derivers` type is the one an engine database's deriver
has, a method per derived field typed by the field, which apigen writes
for Go and rustrestgen for Rust. The entrypoint servergen writes opens the
database's repositories as the deriver and runs the HTTP runtime's loop
over each table's `staleDerivations` and `settleDerivations`: `runDeriver`
(`@superschematic/http-runtime/deriver`), `runtime/http/go/deriver` or
`runtime/http/rust/src/deriver.rs`
(`docs/src/content/docs/guides/stacks.mdx`, Derivers). A deriver fills
tables or engine types, not both: one whose schemas are of both kinds is
refused, and names its schemas over a database that has both. A Rust
API's derivers all fill one kind, tables or engine types, and a Go
API's deriver of engine types is still refused, since that loop is the
engine's. The commits that built it name D146.

Left: a wake on a write, where the loop pulls again every five seconds,
and each pull is a scan of the table's rows with their derivations
sub-table, since the core keeps no index of stale fields; an item the
core skips without counting an attempt, which is answered again at every
pull while its guard holds it; D74's model label and cost cap, which stay
D74's; a Go or a Rust deriver over a database whose DB service lists
sqlite first; and a Go deriver of a table with `@audience`, which the
build refuses, since the Go behaviors' core decodes the table's
`effectiveAudience` as one UUID and fails every pull (D180).

## D147. `Aliased<T>` keeps a row's former names, and `Mergeable` folds one row into another (D103 amended)

Built: `implements Mergeable`, a core trait of `@superschematic/db`
(`TypeDef.Mergeable`, `mergeable: true` in the data forms). The Go and the
TypeScript repository of such a table gain `MergeInto` and
`mergeInto({ from, into })` on Postgres. They run the statements
`sqlutil.MergePlanOf` writes from the schema's to-one relations and the
unique keys of the tables that hold them, with the two columns of a
`@manyToMany`'s join table and the column a `@hasMany` adds to a target
that declares no relation back, in one transaction: lock both rows, delete the referencing rows a unique key would refuse once
repointed, repoint the rest, delete `from` as the repository's delete
does, and answer the rows repointed and deleted by table
(`docs/src/content/docs/guides/database-tables.mdx`, Merging rows). The
commit that built it names D147. It settled what the text left open: a
refused row is stamped deleted where its table deletes softly and the
index holds live rows alone, and removed anywhere else; a behavior's
sub-tables are not repointed; a merge into a row that references `from`
through a relation of the table to itself is refused, and no longer
cycle is checked; a merge that stamps a row deleted needs an acting user
before it writes; a version graph's table that references the table, and
a unique index that lists no keys on a referencing table, are refused at
the build; and the counts are disjoint and leave `from` out.

Left: `Aliased`, whole. A field typed `Aliased<T, { normalize? }>` is a
name with aliases, `normalize` being `lower` (the default) or `slug`. The
behavior keeps a table of the names, each unique in its normalized form
across the table's names and aliases together. Its operations are
`rename({ name })`, which keeps the old name as an alias, `addAlias` and
`removeAlias`, and on the table `resolve({ name })`, which answers the row
whose name or alias matches. A name another row holds, as a name or an
alias, is refused with that row's key. The rules are the behaviors'
core's, and a merge of a table with such a field moves `from`'s aliases to
`into` and adds its name as one, a statement of `sqlutil.MergePlan`.

Two things it waits on in the core. A field anchor's field is its
behavior's alone (`anchored_writes` refuses it in a create), so a name a
create gives needs a field anchor whose value a create hands the behavior,
or the use on the type naming an own field. And uniqueness across rows
needs a key the database holds: a use's shared table keyed by the
normalized name serves it (`ReadRequest::SharedRow`), but the core deletes
no shared row and reads no sub-table across rows, so a removed alias and
a deleted row's names need one of the two.

Also left: `merge_into` in the Rust repositories, SQLite and D1; a route
that serves a table's `mergeInto`; a merge that repoints a version graph's
table, which needs the merge as a commit; a Derived field whose input is
a repointed relation, which the merge does not hash again (Derived hears
no set-based write); and a check of cycles longer than `into` under
`from` in a relation of the table to itself.

## D149. Rollups and Blueprint stay behaviors, Revisions keeps its own log, and Variants becomes a discriminated-union field type (D103 amended)

D103 holds each behavior to a test when it is ported: state of its own,
rules over how that state changes, and recurrence across applications.
Step 4's ports met it in part or not at all four times. Rollups keeps no
state of its own: its values are a function of the rows that relate to a
row, and its one rule gates another use's Workflow on them. Blueprint
creates a tree of rows from a template and holds nothing once they exist.
Revisions' review step passes, and its log of approved revisions is a
storage shape `@versioned`'s history also keeps, on Postgres alone.
Variants is a validation rule: a JSON field typed by a sibling field's
value. Rollups and Revisions are ported; Blueprint and Variants are not.

| Decision | Alternatives not taken |
|----------|------------------------|
| Rollups stays a behavior a table implements. A rule that gates a row's moves on the rows that relate to it is a rule over state, so D103's test, as this entry reads it, holds: its values stay fields computed at a read (`Action::Read`), and D104's Barrier is built on it as it is. | Folding it at step 5 into a computed field and a StateRules rule that reads it, which needs an aggregate field nothing else asks for, and rebuilds Barrier on a feature not yet designed |
| Blueprint is ported onto the core as a behavior, though it holds no state once its rows exist: the cost of the exception is one behavior, and `examples/engine-jobs` and the app D104 counted need it now. It creates a tree of rows from a template through each table's own create, in the command's change set, its recordings (`workqueue-blueprint.json`) replayed as the others' are. | An operation the build writes per blueprint, a generator of its own; leaving it to the app's code, which every project that runs work in steps would write again |
| Variants becomes a schema feature: a field whose type is a discriminated union, its arm named by a sibling field's value. The build writes its validators in every language, a CHECK constraint on Postgres where the arms' shapes allow one, and its types in each ORM and SDK. The engine's Variants stays until engine types go (D103, step 7). How a schema writes it, a TypeScript union of object types beside the field that discriminates or a field type that names both, is the change's that builds it to settle. | A behavior, which would run a validation rule through the core; step 5's declarative validation, which waits on a vocabulary not yet designed |
| Revisions keeps its own log of approved revisions on every dialect. A table that is also `@versioned` keeps both: its history records every write, as a row's image, and the log records each approved proposal with its number and evidence. | Reading the log from `@versioned`'s history where a table has one, two code paths whose history rows hold no proposal's number, on Postgres alone |

### What it amends

D103: its test, that a behavior has state of its own, admits a rule over
the rows that relate to a row, so Rollups passes; Blueprint is ported as
the one behavior with no state, by this entry; Variants goes from the
behaviors as a field type, as Links, Search, Queue and Branches go as
schema features.

Built: Rollups and Revisions as they are. Left: Blueprint on the core,
its recordings replayed, a table implementing it in each language and
`examples/engine-jobs` on it; the discriminated-union field type in the
IR, both readers, every validator, the DDL and both ORMs; and the
engine's Variants removed with engine types (step 7).

## D151. A behavior worker drains on Workers at a Cron Trigger's firings within its CPU time, a write due before the next firing wakes the worker's Workflow, and an environment whose behavior queue no worker drains is refused (D80, D92, D103, D145 amended)

A TypeScript `@behaviorWorker` was refused on Cloudflare Workers, whose
drain.ts ran a `@worker`'s handler alone, so on that target nothing that
waits or reacts fired: no Timers deadline, no Schedules run, no Reaction.
A worker on Workers drains at each firing of a Cron Trigger, a minute
apart, and nothing woke it sooner. And the build only warned of a table
whose deadline, change or event queue no worker runs.

| Decision | Alternatives not taken |
|----------|------------------------|
| A behavior worker some environment places on Workers gets a drain.ts of its own (`behavior_drain.ts.tmpl`): at each firing it runs its table's queues with the handlers its main.ts runs under `stack dev` (the API package's `<worker>Queues`), through Hyperdrive, every queue at once (`runFiring` in the runtime's `./worker`). | One Worker per queue, three Cron Triggers a table |
| A claim takes a batch of up to the worker's concurrency of each queue, as under `stack dev`. A behavior worker's window and grace fit within the CPU time a firing may take, 30 s unless its `cpuMs` says (`DefaultCPUMilliseconds`): wall time bounds CPU time, so the firing ends before Cloudflare stops it, whatever its handlers do. The window is `ir.DefaultWorkerDrainSeconds`, 50 s, or less when the CPU time leaves less (22 s with the defaults: 30 s less an 8 s grace), and a `drainSeconds` that does not fit is refused as unrealizable. | Measuring CPU time as the firing runs, which a Worker cannot (its clock stands still while it computes); the 50 s window with no bound, which a busy table's handlers could take past the limit |
| A firing that stops at its window while its last claim filled every slot, or with handlers it released, leaves a backlog: `runWorker` resolves to what it did (`WorkerRun`), and `runFiring` logs `backlog left for the next firing` with the queue, how many it handled, failed and released, and the window. The next firing, or a wake, takes the rest. | Starting a wake for the rest at once, which a queue that never empties would turn into a run per firing; a metric, which waits for metrics on Workers (D67) |
| The wake on Cloudflare goes through D145's seam: resolution gives each deployable that may write to the queues of a worker on a platform that takes wakes (`registry.PlatformSpec.WakesWorkers`, set by `cloudflare.drainer`) the worker among its `Wakes`, whatever its mode, a behavior worker once per queue. A wake may name a binding (`ir.QueueWake.Binding`) in place of a URL. | A wake path of its own beside `QUEUE_WAKES` |
| The object a wake starts is a Workflow per drainer, `<worker>.workflow`, of the class every drain.ts exports (`ir.QueueWakeClass`, `QueueWake`). A server or a job holds a workflow binding to it (`WAKE_<WORKER>`) and `QUEUE_WAKES`, and its version waits for the Workflow's node. A wake creates an instance named for the second its message is due in, so two wakes for one second start one; the instance sleeps until then and runs one firing as a drain, which stops once its queues held and found nothing for 2 s and none falls due within 10 s. The resources live in the cloudflare extension; the class is written by the server generator; the runtime's mechanism names no provider. | A Durable Object per environment holding the earliest due time, whose alarm runs the drain: a Durable Object class is created by a migration Cloudflare applies on a deploy of a script, not on the upload of a version (deployment management, "Durable Object migrations"), and this target deploys every Worker as a version and a deployment. Core, which would name a provider |
| A message wakes its worker when it is due within the minute before the next firing: an armed deadline due within 60 s, an event, and an enqueue, delayed or not, due within 60 s. One due later is found by a firing, so a sleeping instance is never kept per row. A binding is woken at most once in 5 s for due times a run already covers, those up to 10 s after one sent. The rules adapter sends the wakes of a command's deadlines and events once its statements have run, as an enqueue's are sent (D145). | A wake per armed deadline, at any distance, which keeps an instance asleep per row |
| Servers and jobs take wakes. A drainer takes none: its version would wait for the other drainer's Workflow, and two drainers over one database wait for each other. What a worker's handlers leave another worker, that worker's next firing takes. | Binding by name with no wait, which a first deploy may upload before the Workflow exists |
| The check: resolution refuses, as unrealizable, an environment that runs a database one of whose tables keeps a deadline, change or event queue that no worker the environment runs, with at least one instance, claims. The refusal names the environment, the database, the table, the queue and what it holds. The verifier's warning stays, for a schema read alone. | A warning at the deploy too, which lets a change queue grow by a message a write while nothing runs it |
| Delay on enqueue (D80) was built for every app queue by D80's change: `enqueue(..., { delaySeconds })` in TypeScript, and Go's and Rust's, write `due_at` ahead, and every claim, the drain's on Workers among them, takes only a message whose `due_at` has come. A behavior's deadline is due at its time already; its change and event queues are due at once. What this entry adds is the delayed message's wake: the enqueue says when its message is due, so one due before the next firing starts a run that waits for it. | A `dueAt` option beside `delaySeconds`: a producer that knows a time computes the seconds |

Built: the runtime's binding wake, due times, `settled` and
`queueWakesSettled` (`queue-wake.ts`), `installQueueWakes` (`./workers`),
the rules adapter's wakes (`rules.ts`), `WorkerRun`, `runFiring` and the
wake's idle and horizon (`worker.ts`); `nextDue` on a behavior worker's
queues (tsrestgen); a behavior worker's drain.ts, the `QueueWake` Workflow
in every drain.ts, the wakes installed by fetch.ts and scheduled.ts and
awaited by each invocation, and a job's main awaiting its wakes before it
exits (servergen); `WakesWorkers`, a behavior worker's `Queues` in the
resolved worker and its wakes per queue, and the check
(`internal/stack/workers.go`); the drainer's Workflow, its CPU budget, and
the servers' and jobs' wake bindings (`extensions/cloudflare/wakes.go`,
`drainer.go`); the identity runtime's fallback to its portable argon2id
where node:crypto refuses argon2. The tests: the runtime's wake, firing
and adapter tests, the adapter's on Postgres too; the resolver's check
(`TestBehaviorQueuesNeedAWorker`); the cloudflare extension's
`TestBehaviorWorkerOnWorkers` and `TestBehaviorWorkerWindowFitsItsCPUTime`
and the ledger's goldens; a behavior worker's drain.ts held to a golden
and type-checked; the ledger under the pinned workerd, whose server's
enqueues create instances of a stand-in for the sync worker's Workflow;
and `examples/deploy-smoke`, whose Timers deadline, Schedules run,
Reaction and delayed enqueue each move under `smoke.sh Dev`, and whose
environments hold the Workflows and wake bindings
(`TestDeploySmokeEnvironments`). The stacks, queues and API routes guides
say how it runs.

Left:

- The live run: `examples/deploy-smoke/scripts/smoke.sh Cloudflare`, which
  shows a Timers deadline, a Schedules run, a Reaction and a delayed
  enqueue fire on Workers, an urgent deadline within ten seconds, and
  whether a workflow binding's upload needs the Workflow to exist (the
  version waits for it either way).
- A wake between drainers, without a cycle in the graph.
- Go and Rust: both now wake their behavior queues for events and for
  deadlines due within five minutes (`rules.Host.Wake`,
  `Host::with_wake`). Neither runs on Workers, and a deadline armed more
  than five minutes ahead wakes nothing.
- The CPU bound for a `@worker`'s drainer, whose window D92 set at 50 s
  apart from its CPU time.
- A wake on SQLite and D1, where no worker claims on Workers.
- A behavior worker's drain.ts under the pinned workerd, and a Workflow
  under workerd, which runs none: the fixture stands in for one.

## D156. Every caller is one principal of three kinds, a table's rows keep to an audience a trigger resolves, a row rule follows relations, grants are rows a party or an agent run reaches rows through, reveal is a permission, and an access log records sensitive reads (D103, D104, D108, D114, D116 amended)

Roles gave per-operation permissions, and a row rule (D108) matched
members of the row itself. Nothing said who may see one row and not
another on every route, nothing followed a relation to the caller, and
every caller was a user with a session or a key. Sections 7 and 8 of the
platform plan build parties outside the user table and agent runs on this
entry's principal and grants.

### The principal

| Decision | Alternatives not taken |
|----------|------------------------|
| Every route and MCP tool resolves its caller to one principal, of three kinds: `user` (a session or an API key), `party` (a row of an app table that holds a grant) and `agent` (an agent run). The TypeScript runtime's `Principal` gains `kind` (absent reads as `user`), `actingFor` (whom it acts for: the user an agent run serves; its kind absent reads as `user` too, so the engine's principal, whose `actingFor` names the subject alone (D77), passes as one), `grants` (each grant's target and its role's reaches) and `credential` (the API key's or the grant's id). An MCP tool's call goes through its route's authenticator, so it is one path. | A principal type per kind, which every guard and matcher would then switch on. Parties and agent runs as users with a flag, which would give them sessions, passwords and every user route. |
| Permissions are checked one way for all three: a party's and an agent run's are its grant's role's, matched as a user's are. | A permission vocabulary per kind. |
| The caller of an operation travels whole (`currentAccess()`, `AccessCaller`), not as its subject alone, and an operation with no principal runs as the anonymous caller, so a public route sees the rows everyone may while a job, outside any operation, sees every row. | One store of the subject, which cannot tell a public route from a job. |
| `createdBy` and `updatedBy` record the principal; the access log records it and whom it acted for. | A `createdFor` column on every table, which a table with Provenance (D73) records already. |
| The Go API's auth-provider hook `callerPrincipal` (`internal/generator/apigen/auth.go`, `caller.tmpl`) answers the principal's id, which `WithCaller` binds. When Go builds this entry, the hook answers the principal whole, its kind, `actingFor` and grants beside its id, so a provider names them where it establishes the caller. | A second Go hook beside `callerPrincipal`. |

### A table's audience

| Decision | Alternatives not taken |
|----------|------------------------|
| `@audience<T>({ owner?, inheritFrom?, default?, agents? })` from `@superschematic/db` on a DB table. The loader adds five columns: `audience` (`everyone`, `private`, `named`, `inherit`), `audienceNamed` (ids), `agentAccess` (`followRole`, `readOnly`, `none`), and `effectiveAudience` and `effectiveAgents`, which a trigger keeps. | A behavior on the core (D103). The plan names one, and D103's own test puts it elsewhere: a read filter and a stored derived column are a storage shape and a query, a schema feature, as `@searchField` is. The core sees writes; an audience must hold every read, which only the repositories and the SQL see. Its state changes by ordinary writes, held by the update's permission and a row rule (`by: ["owner"]`). |
| The effective audience is a column, `effective_audience UUID[]`, null for everyone, and a read keeps to `effective_audience IS NULL OR effective_audience && ARRAY[$ids]::uuid[]`: one condition on the row, which a GIN index serves. | A sub-table of principals per row (row, principal), which every read joins and every recompute rewrites row by row, and which a large named audience would favour. A list of the audience is short; a team as one principal (D108's caller through a table) keeps it short. |
| A trigger resolves the effective columns before each insert and update, whatever writes the row, and a parent's cascade trigger, after an update that changes the parent's resolved columns, updates each child that inherits with no change, so the child's own trigger resolves it again, and its children in turn. Both run in the writer's transaction. | The change queue the plan names, which recomputes after the commit: a parent made private would show its children to everyone until a worker ran. The queue stays the way out for a parent with very many children (left). |
| A row that inherits and has no parent is its owner's alone. | Everyone, which would show a document to everyone when its private folder is deleted and the relation set null. |
| An inherited agent access is the least open of the row's and its parent's. | The row's own alone, which would let a document escape its folder's `none`. |
| The repositories apply it (`orm.ts`, `accessConditions`): every read and write of an `@audience` table in an operation, the generated table operations', a hand-written implementation's through `Deps.orm`, a search, a relation a read includes, a filter through a relation, and a projection keyed by its source's key, whose definition carries a probe that holds the view to the source's rows. A row the caller does not see is the 404 of one that is not there. An upsert in an operation is refused. | Postgres row-level security, which D108 turned down and which needs the caller set on each connection; SQLite and D1 have none. A filter in each generated handler, which a hand-written implementation would skip. |
| An agent run sees a row its own id or whom it acts for is in the audience of, unless the row's effective agent access is `none`, and writes it only under `followRole`. | An agent run with its own audience alone, which an agent acting for a person could not then serve. |
| Outside an operation, `orm.asSystem()` and the database's own role read every row. `orm.as(principal)` and `withUser(id)` read as that principal. | A system actor held to an audience, which a job could not then serve. |
| The predicate is one exported function, `audienceConditions(definition, caller, args, offset, write?)` of `@superschematic/http-runtime/postgres`, which search across tables, the calendar's feed, references and notifications (sections 2 to 5) apply as a repository does. | A predicate per feature. |
| Postgres alone: the loader refuses `@audience` in a service that lists `sqlite`. | Text arrays as JSON on SQLite, which no index serves and no trigger function writes. |

How an owner sees which rows a private row is referenced from (for
section 4, which builds references): the reference index holds each
reference as a row (from, to). An owner's read of a private row's
backlinks reads every reference to it, as the system, and answers those
whose source the owner sees, and a count of those they do not, without
their keys: "two rows you cannot see refer to this". A reader who is not
the target's owner sees only the backlinks whose sources they see, and no
count.

### Row rules through relations (D108)

| Decision | Alternatives not taken |
|----------|------------------------|
| A member of `by` may be a dotted path: `unit.contract.parties.party`. Each step is a to-one relation or a one-to-many relation (a `HasMany` field, or another table's relation that points back) of the table the step before reached; the last is a member that holds a subject. The loader checks every step (`verify.checkRowRulePath`), and a create's rule names none, since a create sets its rule's members to the caller. | A rule on the table, which D108 turned down. A path to a list field of ids, which no filter reads yet. |
| The handler builds one nested filter, which the repository writes as one condition of the row's `WHERE`, a semijoin per step: `unit_id IN (SELECT id FROM unit WHERE contract_id IN (SELECT ...))`, and for a one-to-many step `id IN (SELECT contract_id FROM contract_party WHERE ...)`. Postgres plans each as `EXISTS`. The repositories' filter gains the one-to-many step for it. | A correlated `EXISTS` text of its own, a second compiler beside the filter's. |
| The Go and Rust servers leave an operation whose rule names a path to the implementation, with the build's warning (D106's last rule). | Refusing the schema, which would fail a TypeScript API for what only Go and Rust lack. |

### Grants (D116)

| Decision | Alternatives not taken |
|----------|------------------------|
| A grant is a row of the class marked `@accessGrants`, whose fields the loader fills: the holder's id and kind (`party`, `agent`), the role's name, the target's type and key, `startsAt`, `endsAt`, `endsWhenField` and `endsWhenState` (an end when the target reaches a state), `revokedAt`, `tokenHash` and who made it. | A table the loader adds unasked, as the user model's are, which every database with a User table would carry. Its target as a relation, which would tie a grant to one table. |
| A role is a row of the UserRole table: its permissions are what the holder may do, and its optional `reaches` field lists the relation paths it reaches from a target, each `Type:path` (`Request:unit.contract`), or `Type:*` for every row of a table. Policy is data. | The paths in the schema, which ground rule 1 turns down. The paths on each grant, which would repeat the role's on every row. |
| `grantAuthenticator` runs a request with a `grant_` token as the grant's holder, reading the grant, its target's state and its role on every call: a grant that ended or was revoked is the 401 of the very next call. The token is 32 random bytes in base64url after `grant_`, stored as its SHA-256, as D114's keys are. | A token that carries its own expiry, which a revocation could not end. |
| A party's or an agent run's reads and writes keep to the rows its grants reach, the target and the rows of the tables its role's paths name, through the same paths a row rule follows; a table no path names has none. A create that leaves a row outside its reach is the 403 `forbidden`, and the transaction keeps nothing. | Reach as a row rule of each operation, which a hand-written implementation would skip. |
| `activeGrants(orm, filter)` reads the active grants. | A generated declared query (left). |

Section 8's run token is a grant: holder an agent run, `actingFor` whom it
serves, its target the run's rows, its role the agent's, narrowed. Section
7's party resolves through a channel identity to its active grants
(`activeGrants({ holder })`) rather than a token.

### Reveal and the access log (D114)

| Decision | Alternatives not taken |
|----------|------------------------|
| `@decrypted({ permission })` on a view's `Encrypted<T>` field makes revealing it a permission of its own (`FieldDef.RevealPermission`): a caller without it gets the field masked, and each value a generated operation opens writes an access-log row in the operation's transaction. A reveal with no access log to write to is refused. Plain `@decrypted` stays as it was. | A reveal operation per field, a route each. |
| The access log is the class marked `@accessLog`, whose fields the loader fills, written as the system actor: `reveal`, `agentRead` (an agent run's `GET`), `keyCall` (D114's use log) and `grantCall`. The router writes the last three once it has its caller, before the permission check, when its options carry `accessLog` (`accessLogOf(orm)`). | A log of every read, which is the database's audit's to keep. |

Built: the principal's kinds in the TypeScript runtime, and the identity
service's principal naming its API key; `@audience` and the five columns
(`ir/access.go`, `internal/registry/core_access.go`,
`internal/loader/access`, `internal/loader/verify/access.go`), the audience
triggers in create.sql, drop.sql and migration plans
(`internal/generator/sqlgen/audience.go`, `internal/sqlmigrate`), the
repositories' audience and grant reach (`runtime/http/typescript/src/orm.ts`);
row rules through relations in the loader and the TypeScript handlers;
`@accessGrants`, `@accessLog`, `grantAuthenticator`, `issueGrant`,
`revokeGrant`, `activeGrants` and `accessLogOf`
(`runtime/http/typescript/src/access.ts`); `@decrypted({ permission })`;
the guide's "Per-record access" section; `examples/access-records`.
`TestGeneratedAccessEndToEnd` runs the plan's "done when" on Postgres:
a private row never appears to another user in a list, a read or a
search; a party with a grant reaches only what its role's paths allow; an
expired, revoked or ended grant is refused on the next call; a reveal
writes an access-log row.

Left:

- Python's ORM. Go (D180) and Rust (D181) serve the principal's kinds,
  the audience filter, path row rules, grants, reveal and the access log
  on Postgres; Rust's on SQLite is left.
- A GIN index on `effective_audience` the build writes. (An audience on
  SQLite and D1, in TypeScript: D182.)
- An audience in a declared query (`@query`), which reads every row.
- A parent with very many children: the cascade updates them in the
  writer's transaction; the change queue is the way to move it after the
  commit.
- Search across tables, the calendar and its feed, references and
  notifications applying `audienceConditions` (sections 2 to 5), and the
  backlinks an owner sees, designed above.
- A generated route and declared query that list the active grants. A
  `@tableOperation` over an API's `@source` view of the grants table
  lists every grant, the ended and the revoked among them.
- A generated TypeScript server that wires `accessLogOf` and
  `grantAuthenticator` itself; an app passes them in the router's options
  now. The Go server records its calls in the access log itself (D180);
  an app wires the grants' authenticator in both.
- A party's principal across all its active grants, through a channel
  identity (section 7), and the agent run's narrowed token (section 8).
- A path to a list field of ids in a row rule, and a team as a principal
  in an audience.
- Live deploys: `smoke.sh Cloudflare` and `Gcp` are the maintainer's.

## D159. Five runtime interfaces a running app calls, each provider a package that declares its interface in its package.json, slots in an API's config that each environment binds, a build that refuses a feature with no provider, and providers that act as their own principals (D156 amended)

The extension seam was build time alone: Go linked into the binary
(`docs/extension-model.md`). A running app had no interface for what it
calls outside: a channel to send a notice through, a store of
credentials, a sandbox and a model for an agent run, a processor for a
stored file. Sections 3, 4, 7 and 8 of the platform plan call these; this
entry defines them. `docs/extension-model.md` section 13 and the
runtime providers guide hold the design as built.

| Decision | Alternatives not taken |
|----------|------------------------|
| Five interfaces, each a list of operations in `ir.RuntimeContracts` and a TypeScript interface in `@superschematic/http-runtime/providers`: `credentials` (`CredentialProvider`: `list`, `resolve`), `channel` (`ChannelAdapter`: `send`, `health`, optional `status` and `inbound`), `sandbox` (`SandboxProvider`: `start`, `cancel`, `status`, `cost`), `model` (`ModelProvider`: `complete`, its usage in the answer) and `files` (`FileProcessor`: `extractText`, `preview`). `runtime/http/testdata/runtime_contracts.json` holds the Go list, which the TypeScript one is tested against. | An interface per provider kind (email, SMS, chat), which the channel's `kind` and D153's trust levels tell apart as data |
| TypeScript only for now. The runtime that sections 3, 4 and 8 build first is TypeScript, and a provider package is an npm package. | Go and Rust at once, before a feature calls them |
| A package declares its interface in its `package.json`, `"superschematic": {"provider": {name, implements, operations, config, role}}`. An API's config lists the packages it may use (`providers`, directories relative to the service), and the loader reads each into `ir.Schema.Providers` and checks it against its contract: an interface the core defines, every required operation and none it does not define, config keys of upper-case words, and a role. In TypeScript the package exports `defineProvider({...})`, which checks the same, and `checkProviderPackage` holds the manifest to the definition and to the provider it makes, in the package's own test. | A Go extension per provider calling a `RegisterRuntimeProvider`, which makes a TypeScript package ship Go to be checked; reading the definition from the TypeScript module at build time, which runs the package's code in the build; a decorator in the package's own schema, which makes a provider a service |
| A slot is a field of an API's `@envVars` class that `@providers({ SMS: "channel" })` names: a string whose value in each environment names the provider. The provider's settings are the fields named after the slot and its keys (`SMS_URL` for slot `SMS`, key `URL`), a secret one a `Secret<string>`, so `stack secrets set` enters it per environment. The stack binds them as any other field, and each deployable of `environment.json` lists the provider of each slot (`ResolvedDeployable.Providers`). Swapping a provider is a change of env, with the settings the new one reads declared in the config. | A `providers` key of an environment, a second way to bind config; one JSON field per slot holding every setting, which hides a secret inside a plain field; settings named by the provider alone, which two providers of one slot would collide on |
| The load refuses a slot that is no string field of the config, is a secret, or takes an interface none of the listed packages implements. The resolver refuses a slot bound to a parameter, one that names a provider the API does not list or of another interface, and one whose provider's settings have no field, the wrong secrecy, or no binding (`provider`). `providersFrom` refuses the same at start, with every problem and no value, for a config no build checked. | A parameter as a slot's value, which a build cannot check |
| Coverage is data: a feature registers itself with `Registry.RegisterRuntimeFeature(RuntimeFeature{Name, Interface, Uses})`, once per interface it needs, and `Uses` reads the API's schema and the DB schemas it connects to. The stack generator records the features each API uses (`stack.Service.Features`), and the resolver refuses an environment that runs a deployable of such an API with no slot of that interface set, naming the feature, the interface and the slots that would fill it (`no-provider`). The core registers none: notifications, attachments and agents register as they are built. `TestRuntimeProvidersExampleRefusals` proves it with a test feature that an API whose database has a `Notice` table uses. | A list of features in the core, which grows with every section; a decorator per feature naming its interface, which a feature that is a behavior or a table kind has no class for |
| A provider is its own principal, of a fourth kind, `extension` (`ir.PrincipalExtension`, `PrincipalKind` in the runtime), with the subject `extension:<name>` and the permissions its manifest's `role` lists. Each permission names one capability, two dotted segments or more (`messages.relay`), never an area (`messages`), which would cover all of it. `providersFrom` runs each call into a provider within that principal, so what it writes through an ORM opened with no user records it; `extensionAuthenticator` authenticates a provider that calls the app with its own key, a messaging bridge that connects outward, as it. | An existing kind: a provider is not a user (no session, no sign-in), not a party (no row, no grant) and not an agent run (no run, no sandbox), and the access log tells them apart by kind |
| A credential provider returns no value to its caller. `list` answers `CredentialListing` (reference, title, sites); `resolve` answers an `Injection`, which holds the value sealed: no member returns it, its JSON and string forms show the reference and target alone, its private field makes the class nominal so no object literal passes for one, and a provider makes one only from a `SecretValue`. `applyInjection` alone opens it, handing it to the runner's `fill` or `setHeader`. `useCredential` resolves, refuses an injection for another reference or target, applies it, and answers `CredentialUse` (reference, provider, target, time), which the run record keeps. | A value with a mask, which the caller could still unwrap; resolution in core with the value passed to the runner, which puts it in core's memory and logs |

Built: the contracts, the manifest and its check, the slots, the
providers on the IR and on resolved deployables, and the extension
principal (`ir/runtime_providers.go`, `ir/access.go`); `@providers` and
`RegisterRuntimeFeature` (`internal/registry/core_providers.go`, aliased
in `registry`); the config's `providers` (schemaconfig, the TypeScript
config reader, `@superschematic/schema-config`); the loader's reading and
checks (`internal/loader/providers.go`); the stack's slots, provider
checks and coverage (`internal/stack/providers.go`,
`internal/generator/stackgen`); the TypeScript runtime's `./providers`
entry, which the Workers check bundles. `examples/runtime-providers`
ships six packages: two SMS channel adapters of two shapes, a vault
broker scoped to named vaults, a local-process sandbox, a model stub and
a file processor, and a stack whose two environments send through the two
adapters. The tests: `TestRuntimeContractsMatchTheTypeScriptRuntime`,
`TestARuntimeProviderIsCheckedAgainstItsContract` (ir), the decorator and
feature registration (registry), the manifest and slot checks (loader),
the resolver's (`TestAnEnvironmentNamesTheProviderOfEachSlot`,
`TestAFeatureNeedsAProviderOfItsInterface`,
`TestAProviderSlotIsCheckedAgainstTheProvider`), the example's stack
built by the installed binary (`TestRuntimeProvidersExampleStack`,
`TestRuntimeProvidersExampleRefusals`), the runtime's providers test, and
the example's own on Node.js and Bun.

Left:

- The generated TypeScript server: an API that lists providers gets its
  slots from the IR and builds `Providers` into its `Deps`, importing the
  listed packages, as the Rust server (D185) and the Go server (D184)
  do; today the project's wiring calls `providersFrom`.
- A provider package named by its npm name, resolved from the service's
  `node_modules`, beside a directory.
- Provider packages in build-all's dependency graph (D136) and in its
  build cache key, so a change to one's manifest rebuilds the APIs that
  list it and the stacks that deploy them.
- The features: attachments (D154) `files`, and credentials for agent
  runs `credentials`, each with its `Uses`. Notifications (D153) register
  `channel`, and agents (D158) `sandbox` and `model`.
- Health: D153 skips an adapter whose `health` fails and alerts on the
  others; nothing calls `health` on a schedule yet.
- A secret setting of a slot an environment leaves unset is still a
  secret of that environment, which `stack secrets set` asks for.
- A real container platform's sandbox provider, one model API's provider,
  and a PDF and video file processor, which the plan names as examples and
  the plan's question on the sandbox platform leaves open.

## D154. A row points at a row of any table through `@references` and holds files through `@attachments`, each a sub-table the loader adds and a trigger keeps, every backlink is held to what its reader may read, a browser uploads straight to the bucket, and a table's Comments takes attachment blocks (D54, D77, D103, D156 amended)

A `Relation<>` points at one table the schema names, and Comments' ref
blocks reached any row only inside a message, which nothing could list
from the row they named. Files lived in buckets with no row that held
them. Section 4 of the platform plan asks for references and attachments
on any row, the backlinks of a row across tables, files filed elsewhere
with no second upload, and the R2 upload of section 1's Workers target.

| Decision | Alternatives not taken |
|----------|------------------------|
| `@references<T>({ tables?, cleared?, maxPerRow? })` and `@attachments<T>({ released, maxFileBytes?, maxRowBytes?, maxFiles?, mediaTypes? })` from `@superschematic/db` on a DB table, schema features as D156's `@audience` is. The loader adds a sub-table beside the table, a child of the row with no repository (`ir.RowTable`): `<table>_references` (target table, target key as text, role, who added it when; unique on row, target and role; indexed on the target) and `<table>_attachments` (name, media type, size, SHA-256, object, caption, state, uploader, time, filed from, extraction and preview). | A behavior on the core, as the plan names it. D103's own test puts these elsewhere, as it folded the engine's Links into `Relation<>`: their rules are a cascade across tables, a read across tables and a bucket's I/O, none of which a per-table pure core sees (Dependencies' `listDependents` stays the engine's for the same reason). Their state changes by inserts and deletes that the runtime and two triggers make. |
| A target is no foreign key, since it is a row of any table. A statement trigger on each table a reference may point at (`REFERENCING OLD TABLE`) deletes the references to the rows a delete removed, from every references sub-table that may hold one, and enqueues one message per reference on the holder's `cleared` queue, whose fields the loader fills (`holderTable`, `holderKey`, `targetTable`, `targetKey`, `role`). A soft delete clears nothing; a read leaves a deleted row out on its own. | A worker that sweeps references to rows that are gone, which shows dangling references until it runs. Clearing through the core, which would need the core to hear another table's delete. An event of section 3's, which is built in parallel: the queue is D80's, which section 1 drains on Workers, and section 3 may read it. |
| `orm.references` (`add`, `remove`, `of`, `referencedBy`) and `orm.attachments` on the TypeScript repositories, from the definitions orm.ts carries (`references`, `attachments`). Every read goes through the target's and each holder's own repository, so the caller's audience and grants hold as for any read (D156): a reference to a row the caller cannot read is left out, a target the caller cannot read is not there, and only the owner of a row whose audience is not everyone gets `hidden`, the count of the rest (D156's design). | A filter of `audienceConditions` per holder, which would miss a party's grants and soft deletes that the repositories already apply. A locked chip for a hidden row, which tells that it exists. |
| `referencedBy` reads the references sub-tables and the Comments messages of every table, a ref block matched by JSONB containment, so a mention in a thread is a backlink without a second copy of it. | Mirroring ref blocks into the references sub-tables, two writes of one fact, and a write of another use's state from Comments. |
| A browser uploads a file straight to the bucket through a URL `begin` signs, a PUT of its media type, after `begin` held it to the bounds with the row's files and the uploads under way, under an advisory lock of the row. `complete` reads the object back as it streams and hashes it, through workerd's native `DigestStream` on Workers and `Sha256` elsewhere, and holds it to the size and SHA-256 `begin` declared; a file whose bytes differ is removed. | Trusting the declared hash, which a browser could get wrong. A checksum header signed into the URL, which R2's, S3's and Cloud Storage's presigned PUTs take differently and which no offline test can hold to R2. |
| File elsewhere copies or moves a file's row onto another row, naming the same object, with no second upload, and when the file's table keeps references that may point at the other row, its row points at it with the role `filed`, so the other row lists it among its backlinks. A file's delete enqueues its object's release (a trigger, `released`), and `release`, which that queue's worker calls, deletes the object and its preview's once no file of any table names it, under an advisory lock of the object a filing takes too. An upload never completed is released an hour after its URL expires. | Reference counts in a column, which a raw delete would leave wrong; deleting the object at the file's delete, which another row may still name. |
| An object is `attachments/<uuid>` until D142 (PR 512) lands. Every row stores the SHA-256 and counts by object name, so D142's content-addressed naming, `sha256/<hex>`, slots in as a naming of the object alone: `begin` asks the content bucket whether it holds the hash and signs no upload when it does, and `release` counts rows by the same name. | Waiting for PR 512, or copying its code here. |
| A table's Comments takes attachment blocks and the config's `attachments`, held in the core to the engine's batch 5 order: the uri's scheme (`https` unless set; `attachment` names a file of the row's own `@attachments`, `attachment:<id>`), the media type, then the size, each the veto `attachment_not_allowed`. | A block that names the bucket's object, which another row's delete could remove under it. |
| Postgres alone: the loader refuses either declaration on sqlite, as D156 refuses `@audience`. | |

Settled. A file is at most 100 MiB unless the declaration says otherwise,
and at most 5 GiB, the largest object one signed PUT uploads to R2, S3 and
Cloud Storage. A row holds at most 1 GiB and 100 files unless set, uploads
under way counted until they expire. Text is extracted when an upload
completes, so search can index it whether or not anyone opens the file, and
a preview is made on a file's first view (`download`), since most files are
never viewed. A file processor (D159) moves both: `pending` lists what it
has to make, `processed` records it on every file that names the object.

Built: `ir/rowrefs.go`, `internal/registry/core_rowrefs.go`,
`internal/loader/rowrefs`, `internal/loader/verify/rowrefs.go`, the
triggers in create.sql, drop.sql and migration plans
(`internal/generator/sqlgen/rowrefs.go`, a statement trigger's
`Transition`), the definitions in orm.ts (`internal/generator/tsgen/rowrefs.go`),
`runtime/http/typescript/src/rowfeatures.ts` and `sha256Stream`;
attachment blocks in the core's Comments; the guide's "References and
files on any row"; `examples/references-files`. `TestReferencesTablesEndToEnd`
runs the plan's "done when" on Postgres: a file posted on one row's thread
is filed onto a second row, which lists the first in its backlinks; a
caller who cannot read a referenced row sees neither the reference nor a
count, and its owner sees a count; a target's delete clears its references
and enqueues a cleared message; an upload whose bytes differ is refused,
and an object is deleted once no file names it. The uploads go through a
bucket on an R2 binding whose S3 API a stand-in serves, which checks each
PUT against the URL's V4 signature; the workerd fixture `uploads` signs,
sends, checks and hashes a direct upload inside workerd. `examples/deploy-smoke`
uploads a line item's file to its bucket through the signed URL, completes
it and files it onto another line item; `smoke.sh Dev` passes, and
`TestDeploySmokeEnvironments` holds the Cloudflare environment's R2 bucket,
tokens and bindings. Bucket CORS on the cloudflare target came with #514
and needed nothing for this.

Left:

- Comments' follows, read state and inbox on a table (`markRead`,
  `follow`, a reader's `unread`, `inbox`, deliveries). D153 settles that the
  in-app inbox is the notifier's, which a Comments mention or reply reaches
  as an event (D153's left). The core runs them,
  and the build leaves them out of a table's operations
  (`registry.tableLeavesOut`), since no adapter answers a reader's own
  fields through the core's read as the caller, and a namespace's
  operation is not served by `@behaviorOperations`. Serving them touches
  the layout and every ORM's and API's goldens in three languages.
- Generated routes, MCP tools and SDK methods for references and
  attachments; an app writes its routes over `orm.references` and
  `orm.attachments` now (`examples/references-files/server`).
- The Go and Rust repositories on SQLite, and Python's. Go's on Postgres
  are D184's, Rust's D185's. (References and attachments on SQLite and
  D1, in TypeScript: D182.)
- The file processor (section 9) and its wiring: `pending` and
  `processed` are its seam. Search (section 5) indexes `extractedText`.
- D142's content-addressed objects once PR 512 merges, and a multipart
  upload for files past 5 GiB.
- A GIN index on Comments messages' `blocks`, which `referencedBy`'s
  containment reads; it scans a table's messages until one is written.
- References repointed by a `Mergeable` merge (D147), and the merged row's
  references cleared as a delete's are.
- The schema writer (`format --to=ts`) writing `@references` and
  `@attachments` back, as it does not write `@audience` either.
- A live direct upload to R2 and to Cloud Storage: `smoke.sh Cloudflare`
  and `Gcp` are the maintainer's.

## D152. Recurrence turns a rule into dated occurrences on a row, the core dates them for every language, and a calendar read and feed merge the dates of many tables as each caller reads them (D103, D104, D156 amended)

`Temporal.RecurrenceRule` validated an RFC 5545 rule and nothing expanded
it. Schedules ran code no more than thirty days apart and kept no record of
what was due or what was done, and nothing put a table's dates on a
calendar.

### Recurrence

| Decision | Alternatives not taken |
|----------|------------------------|
| `implements Recurrence<T, { anchors, timeZone?, horizonDays?, historyDays?, adjust?, weekendDays?, holidays?, relative?, opensBefore?, remindBefore?, overdueAfter?, permission?, managePermission? }>` on a DB table, a behavior the core alone implements, as Schedules is. A row's anchor is its own: `calendar` (an RRULE from a wall start in a zone), `afterDone` (an interval after the last completion) or `relative` (an offset from a date a to-one relation reaches, and its anniversaries). | A field type, which would allow several on a row: a thing done on several rules is several rows. Schedules with longer intervals, which keeps no occurrences. |
| The row's rule is an owned field, `recurrence`, beside `nextDueAt` and `lastDoneAt`; the occurrences are the sub-table `occurrences`, keyed by the nominal wall date-time before any adjustment (`2026-10-31T09:00`), holding `due`, `dueAt`, `opensAt`, `remindAt`, `overdueAt`, `state`, `revision`, `shifted`, `moved`, `changedAt`, `remindedAt`, `completedAt`, `completedByTable`, `completedByKey`, `completedByPrincipal` and `note`. | A key of the adjusted date, which a holiday added would change. |
| An occurrence is `upcoming`, then `open` at its `opensAt`, then `done` (`complete`) or `missed` at its `overdueAt`; `skip` finishes one that is not done, and a missed one is still completed late. With no `opensBefore` it opens when the one before it falls due. | An occurrence open from when it is planned, which a rule change could then never replan. |
| Each row arms four deadlines on the table's deadline queue, by name: `open` (the earliest upcoming `opensAt`), `remind` (the earliest `remindAt` not reminded), `overdue` (the earliest open `overdueAt`) and `plan` (when nothing is upcoming, the time the horizon reaches the next). Every fire runs one plan, which catches up every occurrence due by its time. On Workers they drain as any deadline does (D151). | A deadline per occurrence and name, which a daily rule would make a thousand messages of per row. |
| The plan keeps the occurrences the rule dates from the later of when it was set and the latest one that opened, up to `horizonDays` ahead: it inserts a new one, dates an upcoming one again, and deletes an upcoming one the rule no longer dates. Open and finished ones are never touched, so `setRecurrence` changes only the future. An afterDone rule keeps one ahead, dated from the later of its last completion and its latest missed or skipped due, so a missed one never blocks the next. | Regenerating every occurrence on a change, which would move one a person is working on. |
| A due date on a weekend day or a holiday moves `before` or `after` it, or stays (`none`). Holidays are rows of a table the app fills, `holidays: { table, calendarField, dateField, field }`; which calendar applies is the row's field. Core ships no holidays. The behaviors' layout carries the holiday table and the relative anchor's target read-only (`registry.BehaviorReads`), whose rows every adapter reads at version 0. | Built-in holiday calendars, which ground rule 1 turns down. |
| `complete({ key?, completedBy?, note? })` records `completedBy` as `{ table, key }`, the key as text, the shape references take (section 4), and the principal that completed it. | A relation to one table. |
| `complete` and `listOccurrences` need `permission`; `setRecurrence`, `skip` and `moveOne` need `managePermission`, `permission` when absent. | One permission for all. |
| Occurrences belong to a table with no Workflow: Recurrence keeps its own states. A table's Workflow may read them as it reads any field. | Requiring Workflow, which a duty with no status of its own does not have. |
| A finished occurrence stays `historyDays` after its due, 1100 unless set (three years, up to a hundred), then a plan prunes it. The row that completed it keeps the evidence. | Keeping every occurrence, which a daily rule grows without bound. |

### Expansion

| Decision | Alternatives not taken |
|----------|------------------------|
| The core expands every rule (`superschematic_rules_behaviors::recurrence::expand`), and a fourth operation of its C ABI, `rules_expand`, answers it: TypeScript's `RulesCore.expand`, Go's `rules.Expand`. One golden file (`runtime/engine/testdata/rules/recurrence/expand.golden.json`) runs each anchor two years out across a change of the clocks and a holiday that moves a date, and Rust, TypeScript and Go each hold their core to it. | An expander per language, which would drift. |
| The RRULE is expanded over wall time by the core's own reader (`recurrence::rrule`: `YEARLY` to `HOURLY`, `INTERVAL`, `COUNT`, `UNTIL`, every `BY` part, `BYSETPOS`, `WKST`), and the maintained `rrule` crate is its oracle in a test: 52 rules from six starts, sixty occurrences each, date for date. Each wall time is then an instant in the zone through the IANA database jiff bundles: a time a change skips takes the offset before it, and one it repeats its first pass, as RFC 5545 reads a local time. An `UNTIL` in UTC is made the zone's wall time first. | The `rrule` crate in the build: its chrono turns on `wasmbind` for `wasm32-unknown-unknown`, which gives the module JavaScript imports (it must import nothing) and adds 2.3 MB to its 4.7. `MINUTELY` and `SECONDLY`, which Schedules' cron lines serve. |
| Lead times are ISO 8601 spans read in the zone's calendar (`P14D`, `-P60D`, `P1M`), so a day is a calendar day across a change. | Milliseconds, which a change of the clocks shifts by an hour. |

### The calendar

| Decision | Alternatives not taken |
|----------|------------------------|
| `@calendarSource<T>({ name?, field?, end?, occurrences?, title, kind, color?, owner? })` on a DB table, repeated per source: a row dated by a date or date-time field, or each occurrence of its Recurrence. The generated repository's definition carries it (`TableDefinition.calendar`). | A list of sources in the API, which the table's schema would not say. |
| `readCalendar(orm, principal, { from, to, sources?, scope? })` reads each source through its table's repository as the caller (`orm.as(principal)`), so a row's audience and the caller's grants hold (D156), and shows an occurrence only when its row is read. A source may need a permission (`CalendarOptions.permissions`). `scope: "mine"` keeps to the rows whose `owner` field holds the caller. | A query across tables with an audience predicate of its own, which would miss grants. |
| `calendarRoutes` serves it at `GET /api/calendar`, and `calendarMcpTool` with `callCalendarTool` as an MCP tool. | |
| A feed's URL holds a token, since a calendar client sends no header. The token is a grant of the `@accessGrants` table: its holder the user it reads as (`holderKind: user`), its role a UserRole whose permissions bound the feed, its target `CalendarFeed;detail=...;sources=...`. `grantAuthenticator` refuses it on every other route, so a leaked URL reads the feed alone. Its detail is `full` or `busy` ("Busy" and a reference number, a short hash of the entry). | A token store of its own, which would repeat the grants' hashing, revocation and ends. |
| A feed expires a year after its issue unless the issue says (at most ten years). Rotation revokes it and issues another of the same scope, role and expiry; a revoked or expired feed is refused at the next fetch. Only its holder rotates or revokes it, and the token is shown once. | A feed with no end. |

Built: the expansion, its reader and its oracle and golden tests in Rust,
TypeScript and Go; `Recurrence` on the core (`recurrence/behavior.rs`), its
declaration, registry entry, loader check (`verify/behaviors_recurrence.go`)
and `@superschematic/db` types; the read-only tables of the behaviors'
layout in the TypeScript, Go and Rust adapters; `@calendarSource`, its IR
(`ir/calendar.go`), check (`verify/calendar.go`) and the generated
definition (`tsgen/calendar.go`); `readCalendar`, the feed, its routes and
its MCP tool (`runtime/http/typescript/src/calendar.ts`); the feed's
refusal in `grantAuthenticator`. The tests: the core's
(`tests/recurrence.rs`); `TestGeneratedRecurrenceEndToEnd`, which serves
the generated API and runs its worker on Postgres (a rule change leaves the
open occurrence untouched, the remind deadline fires on the worker, a raw
write of the calendar field is heard, the calendar merges two sources);
`TestGeneratedAccessEndToEnd`'s feed case (a feed shows only what its
principal reads, busy or in full, and a rotated or revoked one is refused);
and the runtime's workerd fixture `recurrence`, which runs the remind
deadline under the pinned workerd. The guide's "A row's recurrence, and the
calendar"; `examples/recurring-duties`.

Left:

- An event at an occurrence's due time itself: D153 made opened, reminded
  and missed (the overdue deadline's) events Recurrence emits, and no
  deadline is armed at the due, so a "due now" event waits for one.
- `completedBy` converging with references (section 4) once they merge.
- Replanning a relative recurrence when its target's date changes: today
  the row plans again on its own write, at a fire, or at `setRecurrence`.
- The generated router mounting `calendarRoutes` and the calendar MCP
  tool; an app wires them, as it wires `grantAuthenticator` (D156).
- The Rust server: Recurrence's operations through its adapter end to
  end, and the calendar read and feed. D187 serves them in Go, on
  Postgres; a Go server on SQLite is refused them.
- A hand-written scenario of Recurrence that every adapter replays.
- The SDK types narrowed by the config (`behaviortypes`: the anchors).
- A calendar source's permission in the schema, beside the route's option;
  weekend days per holiday calendar; a recurrence set at create.
- The calendar on SQLite and D1.
- `examples/deploy-smoke` with a Recurrence table, and the live
  `smoke.sh Cloudflare` run, which is the maintainer's.

## D155. A `@searchable` table's rows share one search index in its database, a trigger keeps an index row's access and a queue's worker its text, one query ranks every table with the caller's access checked inside it, and embeddings are an option that re-ranks (D58, D77, D84, D103, D156, D159 amended)

Each table filtered its own rows through `searchText`, a `LIKE` over its
`@searchField` columns, and only the engine searched across schemas: each
schema's Search ranked its own instances and the engine fused the
rankings (D16, D58, D77). Nothing found a row of any table for a UI or an
agent with one call, and nothing indexed a row's conversation or files.

| Decision | Alternatives not taken |
|----------|------------------------|
| `@searchable<T>({ title, body?, scope?, queue, comments?, attachments? })` from `@superschematic/db` on a DB table, a schema feature as `@audience` and `@references` are. `title` is a text field; `body` a text field or a list of them; `scope` a field holding one value, a text or a key (a relation's among them). | A behavior on the core. D103's own test folded the engine's Search into `@searchField`: an index and a read across tables are a storage shape and a query, which a per-table pure core does not see. Moving the engine's every-schema search onto namespace operations (D77's left item) serves engine schemas, not tables. |
| The loader adds one index table per database, `search_index` (`ir.SearchIndexTypeName`, marked `TypeDef.SearchIndex`, with no repository): one row per record, unique on its table and key as text, with its title, body, conversation text, files' text, scope, the copied `audience UUID[]` and `agents`, and `text_hash`, `embedding DOUBLE PRECISION[]` and `embedding_model`. A GIN expression index covers the weighed words (`ir.SearchDocument`), one GIN index the audience. | An index once per environment, a service of its own: the access check needs each row's audience and, for a grant, the row itself, in the query that ranks; across databases every write would be two transactions, and the check would run on results, which pages and counts cannot survive. An index per table, the engine's shape: a ranking across tables would fuse per-table rankings, whose pages and totals are not exact. A generated `tsvector` column on each table: one ranking across tables would be a union of every table. |
| The index lives in each database. An environment with several databases searches each one's index; a search across databases is left. | |
| A trigger on each `@searchable` table runs after every insert, update and delete of a row, in the writer's transaction: a delete, a soft delete and a change of key delete the index row; on an `@audience` table, a change of the effective audience or agent access is copied into the index row; an insert, a restore and an update that changes a title, body or scope field enqueue the row. A trigger on the table's Comments messages and on its attachments sub-table enqueues the row they belong to when their text changes. | Writing the index row in the trigger: each write would aggregate the row's conversation and files, and a model's embedding would sit inside the transaction. Copying the access columns through the queue too: a row made private would show in search until a worker ran, which D156 turned down for its own cascade. |
| The queue is the declaration's `@queue` class, whose fields the loader fills (`sourceTable`, `sourceKey`) and whose key it sets (D80): one ready message per row, `skip` on a duplicate, one claim at a time per row. Its worker calls `indexSearch(orm, message, options)`, which reads the row under a share lock, so an audience change waits for it, and writes the index row with the row's access columns in one statement. On Workers the queue drains at each Cron firing (D92, D151); a trigger's enqueue sends no wake, so a changed text is found by its new words within about a minute there, and within the worker's poll interval under `stack dev`. | A behavior change queue (`ir.BehaviorQueueChanges`), whose messages a `@behaviorWorker` hands to the core: the index is not a behavior's state. A wake per enqueue, which D151 leaves for change queues. |
| A row's index row holds its posted and interrupted messages' bodies (Comments) and its ready files' `extracted_text` (D154), each at most 256 KiB, unless the declaration says `comments: false` or `attachments: false`. Their words weigh C and D, the body B, the title A. | A row of the index per message and per file, which would rank a thread's ten messages as ten hits. |
| `Encrypted<T>` fields are never indexed: the loader refuses `@searchable` naming one as its title, body or scope, since an index row holds its text in the clear. | Indexing the opened value, which would put a sealed value at rest in the clear. A blind index (D85's left), which matches whole values only. |
| The ranking is the engine's. A query's words are each a phrase of the words Postgres's parser finds in them (`phraseto_tsquery`), all required, as the engine's Search reads a plain query; the configuration is `simple`, the engine's index where it has no `unaccent` (D58); the order is `ts_rank_cd` with the engine's four weight classes, then table and key. | `websearch_to_tsquery`, a second query syntax beside the engine's. `unaccent`, which needs the extension in every database the migration model creates (left). |
| `searchTables(orm, principal, { q, tables?, scope?, limit?, offset?, vector? }, options)` answers one page, the exact total, `more`, and for each hit its table, key, title, where the words matched and a snippet in parts (`{ text, match? }`), so no markup in a row's text reaches a page. | Snippets as HTML, which a row's own text could break out of. |
| Access is checked inside the query. The caller's audience is `audienceConditions` (D156) over the index row's copied columns, exact since the trigger copies them in the writer's transaction. A caller with grants, a party or an agent run, reaches a table's rows through its grants' relation paths, which no copy can hold: for it each table's index rows are kept by an `EXISTS` back to the table, with the conditions the table's own repository applies for that caller (`repository.conditions`), and a table no grant reaches is left out of the query. `options.permissions` names the permission a table's rows need. | Filtering the results after the query, which shows a gap or a wrong count where a hidden row was. An `EXISTS` for every caller, a join per hit for a user whose audience the copy holds exactly. Copying a grant's reach into the index, which a role's paths and a grant's target change without a write of the row. |
| `scope` is the app's filter: the value of the field each declaration names, copied into the index row as text; a search keeps to the values it names (a UUID in its wire form or hyphenated). A table with no scope field is in no scope. | A scope callback in the runtime, which no index serves. |
| Embeddings are off unless `options.embeddings` (`{ model, embed, maxChars? }`) is given, which `searchEmbeddingsOf(provider, model)` builds from a model provider (D159) that implements the new optional `embed`. `indexSearch` embeds a row's text after its transaction and stores the unit vector only while `text_hash` still holds; an unchanged text keeps its vector and is never embedded again. A search embeds its query (or takes `vector`) and re-ranks the first `RRF_WINDOW` (200) text hits by reciprocal rank fusion of their text place and their cosine place, `RRF_K` 60, the engine's constants; the hits and the total stay the full-text matches. A model that fails leaves the text order. | pgvector's `vector(n)` with an HNSW index: it needs the extension and one dimension per database, and serves a search by vector alone, which the plan does not ask for (left). An embedding call inside the write's transaction. |
| `searchRoutes({ orm, authenticate, base? })` serves `GET /api/search?q&tables&scope&limit&offset`, and `searchMcpTool` with `callSearchTool(orm, principal, args)` the MCP tool `search`. An app mounts them, as it mounts the calendar's (D152). | Generated routes and tools, which wait on the generated router mounting the runtime's (left, as D152's). |

Settled. The index lives in each database. What embeddings cost at scale:
one model call per row whose indexed text changes, none for a write that
leaves it as it was, and one per search; 8 bytes a dimension a row (a
1536-dimension model is about 12 KiB a row, 12 GB a million rows); a
search reads at most 200 vectors. Switching them off is leaving out the
option: no call is made, no search re-ranks, and stored vectors are kept
until `UPDATE search_index SET embedding = NULL, embedding_model = NULL`
clears them. A change of model is `reindexSearch(orm, { embeddings })`,
which embeds every row again. A table made `@searchable` with rows in it
needs `reindexSearch` once.

How section 8 calls search for an agent run: the run's token is a grant
(D156), so the MCP endpoint's authenticator, `grantAuthenticator`, resolves
the call to the run's principal (`kind: "agent"`, `actingFor`, `grants`),
and the endpoint answers `tools/call` of `search` with
`callSearchTool(orm, principal, args, options)`. Its hits keep to the rows
its grants' paths reach, by the `EXISTS` above, and to the rows whose
audience holds the run or whom it acts for and whose agent access is not
`none`. `scope` stays the app's filter; it widens nothing.

Built: `ir/search.go`, `internal/registry/core_search.go`,
`internal/loader/search`, `internal/loader/verify/searchable.go`, the
triggers in create.sql, drop.sql and migration plans
(`internal/generator/sqlgen/searchable.go`), the definition in orm.ts
(`internal/generator/tsgen/searchable.go`),
`runtime/http/typescript/src/search-tables.ts` (`searchTables`,
`indexSearch`, `reindexSearch`, `searchRoutes`, `searchMcpTool`,
`callSearchTool`, `searchEmbeddingsOf`), `ModelProvider.embed` and the
stub model's; the guide's "Search across tables";
`examples/search-across-tables`. `TestSearchTablesEndToEnd` runs the
plan's "done when" on Postgres: two users search the same text across six
tables and each gets exactly the rows its list reads show, page by page
with the same total; a party with a grant finds only the contract its
grant names and the units and requests its role reaches, through the
route and the MCP tool too, and nothing once the grant is revoked; a
row's conversation and files are found with it and an encrypted field is
never in the index; an audience change and a delete hold at once and a
changed text once the queue drains; scope; and embeddings re-rank, are
kept while the text holds and fall back to the text order.

Left:

- The Rust repositories and servers, and Python's. D187 serves search in
  the Go repositories and the Go schema runtime, on Postgres.
- The generated router mounting `searchRoutes` and the MCP tool, and a
  generated handler for the indexing queue; an app wires them.
- A list operation's row rule (`by`), which is its operation's and not
  the repository's: search holds audience, grants and per-table
  permissions, as the calendar does.
- `unaccent`, prefixes and a query syntax; a search by vector alone.
- SQLite and D1.
- A wake for the indexing queue (D151's change-queue wake).
- A search across the databases of an environment.
- `examples/deploy-smoke` with a `@searchable` table, and the live
  `smoke.sh Cloudflare` and `Gcp` runs, which are the maintainer's.

## D153. A table declares its events, routes and preferences are rows, the notifier holds each recipient to the record's audience when it sends, delivers through the channel adapters an environment configures, and runs a reply as its sender within its channel's trust (D77, D80, D81, D103, D152, D154, D156, D159 amended)

Nothing told anyone that a record changed. D77 named a notifier and never
built it, D81 kept password reset and email verification out of the core
until there was a channel, D152 left an occurrence's opening, reminder and
miss as state no one heard of, and D154 enqueued a cleared reference that
nothing reported. Section 3 of the platform plan asks for events declared
on tables, routes as rows, delivery per recipient through channel
adapters (D159), and replies that are input (ground rule 5).

### Events

| Decision | Alternatives not taken |
|----------|------------------------|
| `@event<T>({ kind, payload?, title?, urgency?, description? })` from `@superschematic/db` on a DB table, repeated per kind: dotted lower-camel words (`ticket.overdue`), the row's fields its payload carries, the field a notice is titled by, and a default urgency of `urgent`, `normal`, `digest` or `record`. The generated repository's definition carries them (`TableDefinition.events`). | A behavior on the core: an event's routing reads other tables' rows and calls providers, which the pure core does not (D103's test, as D154 and D156 apply it). |
| A behavior's event is a kind too, its use's member, a dot and the behavior's kind (`recurrence.missed`), which an `@event` may declare to set its urgency and title. Recurrence now emits `opened`, `reminded` and `missed` (its overdue deadline's) from the run that moves an occurrence, in the command's transaction (D104's event queue), and its behavior worker hands each to the project's Workers method, which calls `notifier.fromBehaviorEvent`. | A notifier that reads the occurrences after each fire, which would race the next fire. |
| `notifier.raise(tx, { table, key, kind, payload?, urgency? })` writes the event as a message of the notifier's dispatch queue, a D80 queue the database declares (`@queue() @notifier("dispatch")`), in the caller's transaction: it commits with the change or not at all, and the enqueue wakes the worker at once on Workers (D151). | Each `@event` table given a D104 event queue of its own, which needs the behaviors' layout for a table with no behavior and a behavior worker per table, all to hand every message to one consumer. A table's change trigger raising events, which cannot tell an assignment from any other write. |
| A deleted reference (D154) reaches its holder as `reference.cleared`, raised by `notifier.cleared(tx, message)` from the worker of the `cleared` queue. | A second trigger that raises it, a write of the notifier's from another feature's SQL. |

### The notifier's tables and channel identities

| Decision | Alternatives not taken |
|----------|------------------------|
| The notifier's state is in classes the database declares and the loader fills, as D156's grants are, one per part with `@notifier("<part>")`: `inbox` (one notice per recipient and event, numbered among the recipient's, the number a reply names), `deliveries`, `routes`, `preferences`, `subscriptions`, `channelTrust`, `replies` and `dispatch`. The inbox, deliveries, routes and dispatch are required. Postgres alone. | Tables the loader adds unasked, which every database would carry. One decorator with every part's table named in it, which hides which class is which. |
| Channel identities are core tables, a class marked `@channelIdentities()`: the kind of address (`email`, `phone`, `handle`), the address normalised, the principal it resolves to as a reference (`principalKind`, `principalTable`, `principal`), how it was confirmed (`unverified`, `verified` by a code replied with, `entered` by hand), and a pending code's hash, purpose, expiry and attempts. A party (D157) is a row of the same table with `principalKind: party` and its table, with no migration. The lookup (`resolveSender`) answers the verified and entered identities of an address; two principals on one address resolve to no one, so D157 settles whether parties may share one. | A core extension, which password reset (D81) and every reply need from the core. A user foreign key, which a party cannot be. A unique address, which would settle D157's question now. |
| Notifications are a runtime feature (`registry.RuntimeFeature`, D159) of the core: an API over a database that declares an `@event`, a `@notifier` class or the channel identities needs a `channel` slot in every environment that runs it. | A feature registered by an extension, which every app using the core's notifier would have to link. |

### Routing and sending

| Decision | Alternatives not taken |
|----------|------------------------|
| A route is a row: an event kind or `*`, a table, a filter in the repositories' filter syntax, recipients, an urgency in place of the event's, an escalation and whether it is on. A recipient is `{ path }` (a relation path of the record to a field of ids, through to-one relations), `{ principal }`, `{ role }` (the users holding it) or `{ party: { table, key } }`. Editing routes is the permission the app's table operations over the route table require. | Routes in the schema, which ground rule 1 turns down. A permission of the notifier's own, a second gate beside the routes' operations. |
| Each recipient reads the record through its table's repository as themselves (D156: the audience, and a party's grants), when the event is routed and again when each delivery is sent. One who cannot read it gets no notice and no delivery, and nothing records that they were skipped. | A check at routing alone, which a record made private in between would leak through. A skipped row in the audit, which tells that the record exists. |
| A preference row maps an event kind and an urgency (each or `*`) to channel kinds in the order a failure falls through, quiet hours, a time zone and a digest rule, for a principal or for everyone (a row of no principal, seed data the app writes). The most specific row wins. Every notice is in the in-app inbox; the channels are beside it. | Preferences per channel slot, which would tie a person's choice to an environment's provider. |
| `urgent` sends now through quiet hours and, unacknowledged, follows its route's escalation: each step after `afterSeconds` adds channels or recipients. `normal` waits out quiet hours by a delay on its enqueue (D80). `digest` schedules one digest per recipient at the next time its preference's rule dates (`RulesCore.expand`, D152), and the digest carries every digest notice waiting. `record` writes the notice alone. | Escalation on a Timers deadline of the notice: the inbox's fields are the loader's, and Timers on it would put a loader-filled table through the core for one deadline; a delayed message of the dispatch queue is a deadline the same worker already drains. |
| A delivery is one row per recipient, channel and attempt: `queued`, then `sent`, `delivered` and `read`, or it ends `failed` or `acknowledged`. A send that throws, an address the recipient has no verified identity for, or no report within `deliveryTimeoutSeconds` (300) from an adapter that reports statuses falls through to the recipient's next channel. An adapter with no `status` counts sent as final. | A timeout for every adapter, which would fall through every email. |
| Before sending, the notifier asks the adapter's `health` (held 60 s). One that fails is skipped, and the notifier raises `channel.down` (urgent, no record), which the routes send on the other channels, once an hour while it stays down. | An alert to a fixed address, which ground rule 1 turns down. |
| A subscription is a saved query (a table and a filter), a rule, a start and a zone, a recipient and channels; its brief runs the query as its recipient and goes out at each time the rule dates. | A digest of every row, which would leak what the recipient cannot read. |

### Replies

| Decision | Alternatives not taken |
|----------|------------------------|
| `notifier.reply({ channel, slot?, from, body, ... })` is the entry point an inbound adapter's route calls with its message (`notifier.inbound(slot, request)` reads an adapter's `@webhook` request and calls it per message). The sender's address, normalised by its kind, resolves through the channel identities to one principal, and the reply runs as it. An unknown sender reaches nothing and answers `unknown`, which D157's triage takes. | A reply authenticated by a token in the message, which an SMS cannot carry. |
| A reply names its notice by its number (`ack 142`, `done 142`) unless exactly one notice of its sender waits on that channel and address. `ack` acknowledges and stops escalation. Every other verb is a reply action the app gives per event kind: the permission the operation's route requires, `run` and `undo`. It runs in a transaction as the sender (`withCaller`), its behaviors' commands with the sender's permissions, so the same repository write, the same audience and the same permission hold as on the route. | A reply that calls the HTTP route in process, which needs an authenticator that trusts an internal call. |
| A channel kind's trust is a row: a level and the permissions a reply on it may use of those its sender holds (null for all). A kind with no row is low and runs nothing past an acknowledgement, so SMS, whose sender id can be spoofed, is low unless the app's rows raise it; an identity entered by hand is held to that too. `examples/notifications` seeds SMS low and email standard. | A level the core reads into permissions, a fact ground rule 1 turns down. |
| Every reply is a row of the replies table: its channel and slot, the sender, the text, what interpreted it (`keyword`), the verb, the notice, the outcome and what undoing it needs. Every action is echoed back on its channel with `UNDO 142`, and `undo 142` undoes the latest action on the notice. | The access log, which records reads. |
| An address is verified by a six-digit code sent to it and replied with (or given to `confirmIdentity`); five wrong answers spend it. Password reset (D81) is the same code with the purpose `reset`, sent to the address the login names through the email adapter; `complete` checks it, sets the password with the identity service's new `resetPassword`, which ends every session, and verifies the address. `request` answers alike for a login that does not exist. | A reset token in the identity store, a second code store. |

Settled. The in-app inbox is the notifier's, and Comments does not keep
one of its own beside it: a Comments mention, a reply on a followed thread
and a message to a principal become events of the conversation's row that
route into the same inbox (left below), and the core's Comments `inbox`
operation retires once they do. Comments keeps its read state of a
conversation, which is a reader's place in a thread, not a notice. A
delivery that ended is kept 90 days (`deliveryRetentionDays`), then `prune`,
which the dispatch queue's worker runs with its own, deletes it; notices
are the recipient's history and replies the audit, and both stay.

Section 8 sends an `ask` approval through this: it raises
`approval.requested` on the approval row (urgent), a route names the
principals who hold the approval's permission, and the reply actions
`approve` and `deny` for that kind run the approval's operation as the
replier, held to its permission and to the channel's trust.

Built: the IR (`ir/notifications.go`), `@event`, `@notifier`,
`@channelIdentities` and the notifications feature
(`internal/registry/core_notifications.go`), the loader's filling
(`internal/loader/notifications`) and checks
(`internal/loader/verify/notifications.go`), the definitions in orm.ts
(`notifier`, `events`), `@superschematic/db`'s decorators, the schema
file's types; the notifier (`runtime/http/typescript/src/notify.ts`, in
`./postgres`): `createNotifier`, `raise`, `fromBehaviorEvent`, `cleared`,
the dispatch queue's handler and `workerQueue`, routing, preferences,
quiet hours, digests and briefs, deliveries, fall-through, timeouts,
escalation, health and `channel.down`, `reply`, `inbound`, `statuses`,
the inbox, identities and their codes, `passwordReset`; the identity
service's `resetPassword`; Recurrence's events in the core; the mail relay
of `examples/runtime-providers`, whose notes now raise an event; the
guide's "Events and notifications"; `examples/notifications`. The tests:
the registry's, the loader's (`TestNotificationsFixtureLoads`,
`TestNotificationsRefusals`, `TestNotificationsExampleLoads`), the core's
Recurrence events, the recurrence API's worker handing them to its Workers
(`TestGeneratedRecurrenceEndToEnd`), `TestRuntimeProvidersExampleRefusals`
over the core's feature, and `TestNotificationsEndToEnd`, which runs the
plan's "done when" on Postgres through the example's SMS and mail relays
over stand-in services: an urgent event reaches two recipients and skips
one who cannot read the record, and escalates to email when not
acknowledged; `ack` and `done 142` change the notice and the record as
their sender, with the channel and the interpreter in the audit; SMS, low,
is refused `done`; and password reset sends through the email adapter.

Left:

- Rust: the notifier, `@event` raising, the reply entry point and
  password reset; and SQLite and D1 in every language (Go's on Postgres
  is D190's).
- Comments raising its mentions and replies into the inbox, and the core's
  Comments `inbox` operation retired.
- A generated server that builds the notifier into its `Deps` (with D159's
  providers), mounts the inbox and the relay routes, and fills a
  `@worker` of the dispatch queue with `notifier.handle`; an app wires them
  now (`examples/notifications/server`).
- An event at an occurrence's due time (D152 arms no deadline there).
- A schedule that asks every adapter's health on its own; today a send asks.
- Seed data in the build for the preferences and trust rows, as
  `@seedRoles` seeds roles; an app writes them now.
- A reply interpreter other than keywords (a model, D158), which the
  `interpreter` column names.
- Quiet hours across a change of the clocks inside the window, which count
  wall minutes.
- `examples/deploy-smoke` with a notifier, and the live `smoke.sh
  Cloudflare` run, which is the maintainer's.
- The schema writer writing `@event`, `@notifier` and `@channelIdentities`
  back.

## D157. A party is a row of an app's table that acts through grants, every inbound message takes one path that decides who sent it, conversations are core tables whose messages are rows, an identity whose grants have all ended is verified again, parties may share an address that a thread settles, and sending to a party is a permission (D77, D103, D153, D154, D156 amended)

Every caller was a user with a session or a key. D156 gave grants to
callers who are no users and D153 resolved a reply's sender through
channel identities, but nothing recognised a text or an email from someone
outside the User table, nothing recorded the conversation, and an agent
run (D158) had no inbound trigger. Section 7 of the platform plan asks for
parties, one inbound path, messages as rows and sending as a permission.

### Parties and their identities

| Decision | Alternatives not taken |
|----------|------------------------|
| `@party<T>({ title? })` from `@superschematic/db` marks a DB table whose rows are principals of kind `party` (D156). Its key is one UUID, which a grant names as its holder and a channel identity as its principal (`principalKind: party`, `principalTable`, `principal`, D153's columns, no migration). A schema with a `@party` table declares `@accessGrants`. The generated definition carries `party`. | A core table of parties, which would hold an app's facts (a contractor's trade, a supplier's terms) apart from the app's own rows. A party as a user with no login, which would give it a user's sessions and roles. |
| `partyPrincipal(orm, party, { grant? })` (`./postgres`, `parties.ts`) resolves a party as `grantAuthenticator` resolves a grant's token: kind `party`, its subject the row's key, the reach of each active grant (or of the one named) and the permissions of those grants' roles. `inbound.principalOf` is the same. | A principal with every grant's permissions over every grant's reach and no way to narrow it, which an agent run acting under one grant needs. |
| An identity lapses when its party holds no active grant and some grant of it did not end, by its date or its revocation, before the identity was verified (a grant ended by its target's state counts as ended after). A lapsed identity resolves to no one for inbound and is sent nothing; a code replied with, or `confirmIdentity`, verifies it again, which now always restamps `verifiedAt`. It is decided from the rows (`identityLapsed`), with no stamp to keep in step with the grants. | A `lapsedAt` column a revoke writes, which a grant ending by its date or its target's state would not. Re-verifying on every new grant, which would ask a party that kept its number. |
| An address entered by hand (D153's `entered`) resolves at the lowest trust, as section 3 built it; a party's message records the identity's trust (`trust`), so a run acting on it can be held to it. | A trust level of parties apart from users'. |
| An opt-out is recorded on every identity of the address (`optedOutAt`, a field the loader adds to `@channelIdentities`): the adapter reads its channel's keywords (`InboundMessage.optOut`, STOP and START for SMS) and the core records them. The notifier's sends and `send` skip an opted-out identity. | Opt-out keywords in the core, which differ by channel and carrier. |

### Two parties on one address

| Decision | Alternatives not taken |
|----------|------------------------|
| Two parties may share an address (a household's phone, an office's inbox). A message from it belongs to the party whose conversation it continues: its `In-Reply-To` or `References`, or the service's id of the text it answers, names a message of that party's conversation. One that names neither is held in triage, given to no one. A user never shares: an address that is a user's and a party's resolves to no one, as D153's two principals did. | A unique address, which turns away a real case. The most recent conversation on the address, which guesses, and a guess reaches a grant's rows. |

### One inbound path

| Decision | Alternatives not taken |
|----------|------------------------|
| `createInbound({ orm, notifier, runs, providers?, bucket?, inboundAddress?, ... })` (`./postgres`, `inbound.ts`). `inbound.inbound(slot, request)` reads an adapter's `@webhook` request and `receive` takes each message: the sender normalised; an email held to its authentication; an opt-out recorded; the address resolved through `notifier.resolveSender`. A user's message goes to `notifier.reply` as is. A party's with an active grant is a message row with its files and a run request on the `@runRequests` queue. A party's with none is on its timeline (its conversation, and a reference to its row) and in triage. A lapsed identity's, an unknown sender's, and a shared address's that no thread settles: triage alone. A code from an address with no identity verifies through the notifier. | A pipeline of hooks an app orders, which would let one skip the authentication check. The notifier's `inbound` taking parties too: it stays the reply path of an app with no parties. |
| The adapter reports an email's SPF, DKIM and DMARC (`InboundMessage.authentication`); the core decides (`emailAuthenticated`): DMARC pass, and neither SPF nor DKIM failing (`fail`, `softfail`, `permerror`). Anything else, no report included, is an unknown sender. | Each adapter deciding, which would make trust a provider's choice. All three passing, which turns away forwarded mail whose DKIM carries its DMARC. |
| One inbound address per deployment (`inboundAddress`, the example's `INBOUND_ADDRESS`): mail to another is dropped (`ignored`), and an outbound email asks for its reply there (`replyTo`). An outbound email carries a `Message-ID` the core makes, and `In-Reply-To` and `References` from the conversation's latest inbound email, through `OutboundMessage`'s new `messageId`, `inReplyTo`, `references` and `replyTo`. | An address per conversation (plus-addressing), which some relays strip. |
| The run request is `{ trigger: "inbound", party: { table, key }, message, grant }` on the `@queue() @runRequests()` class's queue, enqueued once the message and its files are stored. `grant` is the grant the thread's message ran under while it is active, else the party's newest active grant. `inbound.runWorker(pool, handle)` is the queue as a worker claims it, its handler the agent dispatcher's (D158) or an app's own. `examples/parties`'s stub writes a report on the granted job as the party and files the message onto it. | A run started in process, which ties the inbound route to the run's time and to a sandbox. One request per active grant, which runs one message many times. |

### Conversations are rows

| Decision | Alternatives not taken |
|----------|------------------------|
| The conversations are core tables, as section 3's channel identities are: classes the app declares and the loader fills, `@conversations("conversations")` (one per party and channel, or per unknown address and channel) and `@conversations("messages")` (direction, `via` the channel kind and `slot`, the addresses, subject, body, `messageId`, `inReplyTo`, `threadRefs`, `providerMessageId`, `authentication`, `identity` and `trust`, the party, `author`, `authorKind` and `forSubject` on whose behalf, `disposition`, `state`, `triage`, `grant`, `detail`, `at`). The expansion runs before D154's, so the messages class may take `@references` and `@attachments`. Postgres alone. | A core extension, which the inbound path, the notifier's party recipients and the agent runs all need from the core. Tables the loader adds unasked. |
| A message's files are its `@attachments`: the adapter fetches them (`InboundMessage.attachments`), and the core stores each through `begin`, a put to its object and `complete`, so its hash is checked as a browser's upload's is. `inbound.file({ message, to, as })` files a message onto a row its principal reads: a reference to the row, so the row lists the message among its backlinks (D154), and each file with no second upload. | Files on the conversation, which a message's reference could not carry onto a row. |
| Triage is a state of a message (`triage: open`, then `closed` by `settle`), read and settled with `parties.triage`. | A triage table beside the messages, a second copy of each. |

### Sending to a party

| Decision | Alternatives not taken |
|----------|------------------------|
| `inbound.send({ as, party, channel, body, subject?, about? })` takes `parties.message` (`sendPermission`), granted per role; the sender reads the party's row; the party's identity on the channel is verified or entered, not opted out and not lapsed; a party is sent at most `rateLimit` messages in its window (20 an hour); and `approve`, the approval policy hook D158 sets `ask` policies through, answers `allow`, `deny` or `ask`. An asked message waits as `awaitingApproval` until `release` sends it, its identity checked again, or `reject` refuses it. Each refusal but the permission's is a message row with its reason. | A permission of each channel, which a role would list per provider. A rate limit in the adapter, which knows no party. |

Settled. Channel identities and conversations are core tables, as section 3
settled identities, for the same reason: the core's own paths read and
write them. Two parties may share an address, a thread settles which one
wrote, and a user shares with no one. Comments on tables keeps the `for`
D103 built (`forSubject`); its `via`, the channel, waits for the core's
caller to carry one (left below), and a conversation's message records its
own `via` and `forSubject` meanwhile. Section 3's inbox stays the
notifier's; a party's notices are its inbox rows of `recipientKind: party`.

Built: the IR (`ir/parties.go`), `@party`, `@conversations` and
`@runRequests` (`internal/registry/core_parties.go`), the loader's filling
(`internal/loader/conversations`) and checks
(`internal/loader/verify/parties.go`), the identity's `optedOutAt`, the
definitions in orm.ts (`party`, `conversations`), `@superschematic/db`'s
decorators, the schema file's types; `parties.ts` (`partyPrincipal`,
`identityLapsed`) and `inbound.ts` (`createInbound`, `emailAuthenticated`)
in `./postgres`; the notifier skipping opted-out and lapsed identities and
restamping a verified one; `InboundMessage`'s and `OutboundMessage`'s new
members; the SMS relay's media and STOP and START, and the mail relay's
inbound mail, authentication, threading and attachments, in
`examples/runtime-providers`; the guide's "Outside parties and inbound
messages"; `examples/parties`. The tests: the registry's, the loader's
(`TestPartiesFixtureLoads`, `TestPartiesRefusals`,
`TestPartiesExampleLoads`), the runtime's pure parts, and
`TestPartiesEndToEnd`, which runs the plan's "done when" on Postgres
through the relays over stand-in services: a text with a photo from a
party with a grant becomes a report on the granted job with the photo
attached, and the message is on the job's, the report's and the party's
backlinks; a text from an unknown number lands in triage; the same text
from a party whose grant has ended reaches nothing until the number is
verified again; email failing DMARC is treated as unknown, and an
authenticated reply threads to its party's conversation; a party with no
grant is on its timeline; two parties on one address are told apart by
the thread; and sending is held to the permission, the rate limit, the
approval policy and STOP.

Left:

- Go and Rust: the inbound path, party principals and sends, and SQLite
  and D1.
- Comments' `via` on a table: the rules core's caller carries no channel,
  so a message posted into a row's Comments for a text's sender records
  its `for` and not its channel. The core's `Caller` gains a channel, the
  declaration's messages a `via`, and the core is built again for every
  language.
- A generated server that builds the inbound path into its `Deps`, mounts
  each relay's inbound route and fills a `@worker` of the run requests;
  an app wires them now (`examples/parties/server`).
- Settling a held message onto a party (adding its identity from triage),
  and a triage page in Topcoat.
- A messaging bridge's inbound (D159's examples are SMS and mail).
- `examples/deploy-smoke` with the inbound path, and the live `smoke.sh
  Cloudflare` run, which is the maintainer's.
- The schema writer writing `@party`, `@conversations` and `@runRequests`
  back.

## D158. Agents are rows, a dispatcher on a queue starts each run in a fresh sandbox with a token that is a grant per target row acting for its principal, autonomy is rows per operation whose ask binds an approval to the parameters' hash, budgets pause, and switches cancel through the sandbox (D77, D93, D104, D114, D153, D156, D159 amended)

D77 named an agent dispatcher and never built it. An agent called the MCP
endpoint as a user, with that user's session or key: it reached whatever
the user reached, ran whatever the user could, and nothing metered it,
asked a person before it moved money, or stopped it. Section 8 of the
platform plan asks for agents as rows, a fresh sandbox per run, a token its
task bounds, autonomy per operation, budgets and stop switches.

| Decision | Alternatives not taken |
|----------|------------------------|
| The orchestration's tables are classes the database declares, one per part with `@agents("<part>")`: `definitions` (`@versioned`), `runs`, `requests` (a `@queue`), `policies`, `approvals` and `controls`, every one required. The loader adds each part's field a class does not declare, and holds one it declares to the part's scalar (the runs' `status` excepted), so the runs may compose Lease and Retries over a Workflow `status`, and the controls a Budget over `budgetLimit`. Postgres alone. | Tables the loader adds unasked, which every database would carry. A class the loader fills only when it is empty, as D153's notifier parts are, which leaves no room for the behaviors D104 meters and retries with. |
| Each agent is its own principal: its runs act with its definition's `role`, never a user's. A run's principal is `kind: "agent"` with the run's id as its subject, so a row's audience admits the run or whom it acts for (D156). The run records the definition's `_version`. | A principal per agent, whose token would outlive a run. |
| The run's token is a D156 grant per target row, held by the run (`holderKind: "agent"`), its role the agent's, ending at the definition's `limits.maxSeconds` and revoked when the run ends. `agents.authenticator()` resolves it through `grantAuthenticator`, then reads every active grant of the run and the run's row: a run that ended is a 401, `actingFor` is whom the run acts for, and the permissions are the role's narrowed to those that principal holds (each the two share, the narrower of a pair). | A token with its own claims, which a revocation could not end. The agent's role alone, under which an agent answering a party could do what the party may not. |
| The dispatcher is a worker of the requests queue (`agents.workerQueue(pool)`, `agents.handle`). A start writes the run, takes its Lease (D104) as the dispatcher's behavior caller, mints the token and starts a sandbox instance (D159) with the definition, the token as a secret, the agent routes' URL and the credential references. A `watch` message, a delayed enqueue (D80), polls the instance and heartbeats the lease until it ends; the run then ends: token revoked, cost metered, transcript written, lease released. A start that fails is recorded with Retries, whose backoff sets when the start is tried again; exhausted, the run fails. On Workers the queue drains as any `@worker`'s does, at each Cron firing and on a wake for a message due within the minute (D92, D151): no process waits on an instance. | A long-running supervisor per run, which Workers has no place for. The queue's own claim lease and attempts in place of Lease and Retries, which the plan names and which give a run a fencing token and per-class backoff. |
| The model turn runs in the app, not the sandbox: the instance posts its tool results to `<endpoint>/turn`, where the definition's model provider completes the turn with the run's tools, records the usage on the run and the budget, and writes the transcript. The instance calls tools at `<endpoint>/mcp` with its token, and runs the sandbox's own tools (a fetch, a browser) in process. `runAgentInstance` in `./providers` is that side. | The model called from the sandbox, which needs a model key in every instance and a budget the app cannot see. |
| Credentials are references in the definition. The instance resolves each with `useCredential` into the runner that uses it and reports the reference, provider, target and time to `<endpoint>/credential`, which the run's `credentialUses` keeps. The app passes the broker's own settings to the instance through `sandboxSpec`. | Resolving in the app and passing values to the instance, which puts them in the app's memory and the spec's log. |
| Triggers are request messages. A notifier route's recipient may be `{ agent }`, which enqueues a request about the event's record in the routing transaction (`createNotifier({ agents })`). `agents.request(tx, ...)` enqueues one for an assignment, a schedule or a principal. A party's inbound message is D157's run request, `{ trigger: "inbound", party, message, grant }` on the `@runRequests` queue, which `inbound.runWorker(pool, m => agents.handle(m))` hands the dispatcher as it is. It names no agent: the definition whose `inbound` is set takes it, acting for the party, permissions narrowed by `partyPrincipal` under the request's grant, its scope the grant's target, the party's row and the message's row, its task the message's body, marked untrusted. | One queue for both, which would make D157's inbound path write a table of D158's. Inbound messages through the notifier's reply path, which runs a sender's own verbs. |
| A run writes to a party with the `message_party` tool, through D157's `inbound.send` (its permission, the party's identity, its rate limit). `createInbound({ approve: agents.sendApproval })` holds the send to the run's policy as `send_message`, class `messageParty`: ask holds the message `awaitingApproval`, the approval records it (`holds`), and approving it releases the message, denying it refuses it. | Gating the tool as well as the send, which would ask twice for one message. |
| Autonomy is rows: per agent or every agent, per operation or class (`read`, `internalWrite`, `messageParty`, `moneyMovement`), `auto`, `tell` or `ask`. The most specific row wins; with none, the tool's mode or the operation's `@autonomy`, then the class's default (auto for a read and an internal write, ask otherwise). A money movement is ask whatever a row says: `@autonomy("moneyMovement", mode)` with another mode is the load's refusal, and `setPolicy` refuses such a row. | Autonomy in the schema alone, which ground rule 1 turns down. A mode the runtime merely defaults, which a row could set to auto. |
| `ask` writes an approval holding the exact parameters and the SHA-256 of the operation and them as canonical JSON (`paramsHash`), raises `approval.requested` on it as urgent, and the call answers `428 approval_required`. Anyone holding the approval's permission (`agents.approve` unless the row names one) decides it, on any channel: `replyActions` are D153's reply actions `approve` and `deny`, and `agents.approve` is the API's. The same call then runs once and spends it. A call of the operation with other parameters supersedes the run's open approvals of it and asks again. `mcpGate`, a new `callGate` of `mountMcp`, holds a generated API's tools to the same policy for a run's token: D93's confirmation for calls a key or a token makes (D114). | A confirmation token the agent carries, which an agent could ask for and redeem itself. An approval of the operation alone, which a changed amount would reuse. |
| Budgets are the Budget behavior on the controls (D104): each turn's tokens and each instance's wall seconds count on the agent's row, whose meter's scope is every agent's budget row. `agent.budgetWarning` is raised as usage crosses the row's `warnAt` percent. At the agent's cap the agent pauses (`pausedReason: budget`) and its runs are cancelled; at every agent's cap a scheduled run is refused and a principal's run waits on an `agents.overBudget` approval, which `agents.budgets` decides. A limit changes only through `setBudget`, as a principal holding the Budget's `limitPermission`. | A meter of the dispatcher's own, beside the behavior the plan names. |
| `pause(principal, agent or null)` sets the control row and cancels each run in flight through its sandbox provider; its token is revoked and an assigned row goes back to its queue (`onReturn`). A paused agent's request is recorded as a run refused `paused`. | Letting runs in flight finish, which a stop switch is for stopping. |
| `ask_principal` posts the question in a target row's thread (its Comments, or the app's `thread`) and enqueues a `followUp` message due at the deadline; a thread no one but the run answered gets the app's `followUp` row. | A wait inside the run, which holds an instance for a day. |
| A tool marked `untrusted` (the app's or the sandbox's) has its results marked untrusted in every turn after it and counted on the run; an inbound message is the run's task, marked untrusted too. Every tool runs as the run's principal, whose grants were fixed when the run started: no tool widens a run's scope. | Filtering instructions out of outside text, which no filter does reliably. |
| Administering agents is permissions: `agents.define`, `agents.credentials` (changing a definition's references), `agents.autonomy`, `agents.budgets`, `agents.pause` and `agents.approve`. A principal of kind agent holds none of them, whatever its role says. | An admin flag, which ground rule 2 turns down. |

Settled. A transcript is redacted as each entry is written (the run's
token, bearer tokens and `options.redact`), kept on the run row while the
run runs and in the bucket after, `agent-runs/<run>.json`, for 30 days
(`transcriptRetentionDays`); `prune`, which the dispatcher's worker runs
with its queue's, deletes the object and stamps `transcriptPrunedAt`. The
run row is history and stays. Only the dispatcher starts a run: a request
whose `requestedBy` is an agent run is refused (`agents_cannot_request`),
so no run starts another with a reach of its own choosing; an agent that
hands work on asks a principal, whose request starts the next run.

The run-request queue is the class marked `@agents("requests")`: in the
example and the fixture `AgentRunRequest`, table
`agent_run_request_queue`, generated as `agentRunRequestQueue`. A message
is `{ step?, agent?, trigger?, subjectTable?, subjectKey?, targets?,
event?, input?, requestedBy?, actingFor?, party?, message?, grant?, run?,
ask? }`. Section 7's inbound message is `{ trigger: "inbound", party:
{ table, key }, message, grant }`, `message` the message row's key, on D157's
`@runRequests` queue, not this one: `agents.handle(message)` takes it as it
is. `createAgents({ inbound })` reads the message through
`inbound.message`, or the app's `inboundMessage`. A request may also carry
the message's row as `{ table, key }` or its text as `{ body }`.
`agents.sendApproval` is `createInbound`'s `approve`, and
`agents.sendPolicy(principal, operation, params)` the same policy for any
send.

Built: `ir/agents.go`, `@agents`, `@autonomy` and the agents feature
(`internal/registry/core_agents.go`), the loader's filling
(`internal/loader/agents`) and checks (`internal/loader/verify/agents.go`),
the definition's part in orm.ts, the approvals' `holds`, `@superschematic/db`'s `agents` and
`@superschematic/api`'s `autonomy`; `runtime/http/typescript/src/agents.ts`
(`createAgents`, in `./postgres`), `runAgentInstance`
(`src/providers/agent-runner.ts`), the notifier's `{ agent }` recipient and
`agents` option, `mountMcp`'s `callGate`, and the wiring to D157's inbound
path (`inbound`, `message_party`, `sendApproval`); the guide's "Agent runs";
`examples/agent-orchestration`. The tests: the registry's, the loader's
(`TestAgentsFixtureLoads`, `TestAgentsRefusals`,
`TestAgentOrchestrationExampleLoads`,
`TestAutonomyRefusesMoneyWithoutAsking`), the runtime's `agents.test.ts`,
and `TestAgentRunsEndToEnd`, which runs the plan's "done when" on Postgres
with the local-process sandbox running `runAgentInstance` under Bun and the
model stub: a run a schedule's deadline starts reads only its target and
the rows its role reaches; its MCP search returns only rows within its
scope; an ask waits for an approval given by SMS, then runs; a changed
amount asks again; a budget cap pauses the agent, and every agent's stops
scheduled runs and asks before a principal's; pausing every agent cancels
a run in flight and returns its assigned row; the transcript, the run row,
the approvals and the logs hold no credential value; a party's text down
D157's inbound path starts a run through its `@runRequests` queue, which
acts for the party, narrowed to its grant's role, and obeys nothing the
text says, and the run's message back waits on its approval, given by text,
before D157's send releases it; an unanswered question makes a
follow-up row; Retries starts a failed instance again while the run holds
its Lease; and administration is permissions.

Left:

- Go and Rust: the dispatcher, the run's authenticator, autonomy and the
  runner; SQLite and D1.
- A real container platform's sandbox provider and one model API's
  provider (D159's left), and the live Workers run (`smoke.sh
  Cloudflare`), which is the maintainer's. The dispatcher's module reaches
  Postgres through Hyperdrive as the notifier's does.
- A reply interpreter that is a model (D153's left), through an agent run.

## D180. The Go server and ORM serve per-record access: the principal travels whole from the auth-provider hook to the repositories, one predicate holds every read and write of a table to it, grants and the access log are the ORM's, and the Go outputs refuse the platform features they do not serve yet (D156 amended)

D156 built per-record access in the TypeScript server and repositories and
left Go's. This entry ports it, and settles what Go's shape asked for.

| Decision | Alternatives not taken |
|----------|------------------------|
| The principal is `access.Principal` of `runtime/schema/go/access` (its subject, kind, `ActingFor`, `Grants` and `Credential`), which the generated ORM and the HTTP runtime both import; `session.Caller` is the same type. The zero principal is the anonymous caller. | A principal type in each, which a generated API would convert between. The session package's `Principal`, which the ORM cannot import. |
| The auth-provider hook `callerPrincipal` answers a `runtimesession.Caller`, whole, as D156 said; the core's answer is `session.GetCaller(ctx)`, the principal `ContextWithCaller` put, or a user of the principal's ID. `callerImports` no longer names the session package, which `caller.go` imports itself. A provider of its own returns a `Caller` now. | A second hook beside the ID's, which D156 turned down. |
| A route binds its caller with `orm.WithPrincipal`, in an ORM with per-record access; `orm.WithCaller(ctx, id)` stays the binding of an ID alone, for an ORM without. `AsSystem` and a context outside any operation read every row. `WithUserID` sets who a write stamps and leaves the reads as they were. | `WithUserID` reading as the user, as the TypeScript `withUser` does: the ORM calls it for every stamp, `AsSystem`'s among them. |
| One predicate, data the ORM is generated with (`accessTables`: each table's audience columns, key and the relations a path steps through), holds a read or a write of a table to the caller: the audience, and the reach of each grant's role's paths. `AccessConditions(ctx, table, args, offset, write)` exports it for a read across tables. A filter through a relation holds its target the same way, through an unexported scope the repository sets on the filter. | A predicate generated per table, which a path through another table could not reach. A filter method that takes the context, which every filter's callers would change for. |
| A grant reaches its rows through the same relation steps a row rule's path takes, and every Go filter now reaches through a one-to-many relation by its name (`Parties`), in every ORM: TypeScript's filter has it, and a row rule needs it with or without access. | The step in access ORMs alone, which would leave a row rule's path unserved elsewhere. |
| The grants are generated in the ORM of a database with an `@accessGrants` table: `IssueGrant`, `RevokeGrant`, `ActiveGrants`, `GrantEnded` and `Database.AuthenticateGrant`, the `access.GrantAuthority` that `session.GrantAuthenticator(authority, inner, now)` reads on every call. A grant's row is read and written through its JSON, whatever pointer shapes the Go types give its members. | A runtime package reading the grant table by name, which would repeat the repository. |
| The Go server records each call in the access log itself, in a route of an API whose database has an `@accessLog` table, before the permission check, through `session.AccessLogged`; a log that fails answers 500. | An option the app passes, as the TypeScript router takes (D156's left for TypeScript). |
| A view's field under `@decrypted({ permission })` is masked, `[secret]`, for a caller without it, and each value opened for one with it is recorded in the operation's transaction. A revealed field holds text, and a database without an `@accessLog` table refuses the build, where TypeScript refuses the reveal when it happens. | A mask of another type's zero, which a reader cannot tell from a value. |
| A path of `*` lets a grant read every row of the table, and the values the branches before it bound go with them. | Leaving them bound: TypeScript's `accessConditions` does, and a statement then binds more values than it names (left below). |
| The loader refuses, in a Go server, each feature of D152 to D159 that Go does not serve yet, on the tables of the API's database and on the API's own provider slots and operations (`verify/go_features.go`); a Go server is an API whose server is Go and whose Go types are on. The loader checks the database when it reads it for a set or an operation, and the Go server's generation, which reads it always, checks it too (`verify.GoServerDatabaseErr`). Before it, a Go server left out the calendar and Recurrence's operations, `@event`, `@notifier`, `@channelIdentities`, `@references`, `@attachments`, `@searchable`, `@party`, `@conversations`, `@runRequests`, `@agents`, provider slots and `@autonomy`, and, for D156, read an `@audience` table whole and opened a revealed field for every caller. `fixture-recurrence-api` was such a server, and is served in TypeScript now. | Refusing a DB service's Go types, as D181 refuses Rust's: the DB kind generates the Go ORM always and the build needs the Go types for it (`refuseORMWithoutGoTypes`), so every database with one of these tables would fail `build-all`, whatever language its API is served in. The Go ORM of these tables is their plain repositories, and the triggers and the core keep what each feature writes, whichever ORM writes the row; what would serve a feature wrongly is the Go server. Relaxing the build's requirement of Go types, which would leave a Go worker or job of the database with no ORM. This holds on Postgres, where the Go ORM keeps an audience (above) and triggers keep references and attachments; on SQLite, where neither holds for Go, D182 refuses a database's Go types for `@audience`, `@references` and `@attachments` (`sqlite_features.go`), and the two checks agree. |

Built: `runtime/schema/go/access` and `session`'s `Caller`,
`GrantAuthenticator`, `AccessLogged` and `RevealEntry`; `access_parity.json`,
which Go writes and TypeScript checks (the token hash, the reaches, the
call entries); `ormgen`'s `access.go` and the held repositories
(`ormgen/access.go`, `templates/access.tmpl`), the one-to-many filter, a
nullable `createdBy` stamped when a user acts, and a field named `field`
ordering by `OrderByFieldField`; `apigen`'s caller, path rule, reveal and
call log; the loader's refusal of a Go server. `TestGoAccessEndToEnd` serves
`fixture-access-go-api` on Postgres through D156's checks, and
`TestHeldRepositoriesCompile` builds the held repositories of the history,
vector, merge, projection and behavior fixtures.

Left:

- A Go table operation that writes a list, `audienceNamed` among them
  (D106); `fixture-access-go-api` sets a named audience through the ORM.
- `ListAsOfBy<Field>ID` of a held table, which refuses, and the history of
  a deleted row, which a held caller cannot read.
- A held create outside a transaction keeps its row when it is refused, as
  TypeScript's does; a generated table operation runs in one.
- TypeScript's `accessConditions` keeps the values of the branches before
  a `*` path bound.
- The Go server's parties and agents, which later Go workstreams port.
  References, attachments and provider slots are D184's, Recurrence, the
  calendar and search D187's, and the notifier D190's.
- A held table that composes behaviors, through the Go behaviors' core:
  its create fills neither default of `audience` and `agentAccess`, so a
  create that names neither fails their checks, and its reads of the
  rows decode `effectiveAudience`, a list, as one UUID, which fails
  Derived's `StaleDerivations`. No test runs one:
  `TestHeldRepositoriesCompile` builds the behavior fixtures alone.

## D181. The Rust runtime and server serve D156's per-record access on Postgres, and the Rust outputs refuse the platform features they do not serve yet (D156 amended)

D156 built per-record access in the TypeScript runtime and left the Go
and Rust servers. This entry ports it to Rust, on Postgres, with the
TypeScript tables, columns, statements and refusals. It is the first of
the Rust parity entries; the later ones port D152 to D159 onto it.

| Decision | Alternatives not taken |
|----------|------------------------|
| The runtime's `Principal` gains `kind` (`PrincipalKind`, `user` unless set), `acting_for`, `grants` (`GrantReach`) and `credential`, as fields with builders, so every authenticator written before still compiles and reads as a user. | A principal enum per kind, which every guard would match on. |
| Rust has no ambient caller, so the caller travels explicitly: a table operation's repositories are opened `with_access(AccessCaller::of(principal))`, the anonymous caller for a route with none, and a hand-written implementation opens its own the same way. Repositories opened without it, and `as_system()`, read every row. | A task-local caller, which a spawned task would lose and D87's acting user does not use either. |
| The repositories' definition gains `audience`, `access` and `many` (the one-to-many relations), which rustgen writes from `sqlutil` as tsgen does. The conditions are the TypeScript `accessConditions`: `audience_conditions` (exported, as `audienceConditions` is) and a branch per grant, a role's path written as a nested filter that steps through to-one and one-to-many relations. | Generated per-table SQL, a second compiler beside the filter's. |
| A grant's end by its target's state is read by counting the target's rows with the state (`Repository::count`), so no value of the row is opened and an encrypted target needs no key. | Reading the row, as the TypeScript runtime does, which in Rust opens every encrypted field and fails without the database's keys. |
| The access log of a call is an authenticator's: `Authenticator::called` is told each call of an operation that `RouteControls::operation` names, after the caller is established and before the permission check, and `access::Logged` records `call_entry` in an `AccessLog` (`TableAccessLog` over the `@accessLog` table). The generated router names each authorized route's operation. | A field of `RouterOptions`, which derives `Eq` and could not hold the log. |
| A table operation's literal carries its name, its `@decrypted` members and their reveal permissions; the handler masks a member to a caller without its permission, `[secret]` as TypeScript does, and writes a `reveal` row per value opened, as the system, in the operation's transaction. | A reveal outside the transaction, which a refused operation would still record. |
| The Rust outputs refuse each D152 to D159 feature they do not serve, at load (`verify/rust_features.go`, from the config's `outputs.types.rust` and `outputs.api.language: rust`) and in rustgen and rustrestgen for IR that did not come through it: `@calendarSource`, `@event`, `@notifier`, `@channelIdentities`, `@references`, `@attachments`, `@searchable`, `@party`, `@conversations`, `@runRequests` and `@agents` on a DB service's Rust types, and on a Rust server `@providers` slots, `@autonomy` and its database's table features. Recurrence is refused on a Rust server alone: the core runs it in every language and the Rust types write it through the core. Before this, the Rust outputs dropped every one of them in silence, and a `@decrypted({ permission })` member and an `@audience` table's rows were served to every caller. | Refusing Recurrence in the Rust types too, which the Rust core runs and fixture-recurrence-db's config lists. |

Built: the runtime's principal and the identity principal's API key
(`runtime/http/rust/src/auth.rs`, `identity/service.rs`); the
repositories' audience, grant reach, one-to-many filters, `count` and
`with_access` (`orm/mod.rs`, `plan.rs`, `run.rs`, `postgres.rs`,
`sqlite.rs`); path row rules and reveal in the table operations
(`orm/routes.rs`); grants and the access log (`access.rs`); the
definition's `audience`, `access` and `many` (`rustgen/orm.go`), the
generated `with_access`, the table operation's literal and
`.operation(...)` (`rustrestgen`); the refusals
(`internal/loader/verify/rust_features.go`, `internal/loader/rust_output.go`).
`TestRustAccessEndToEnd` runs D156's "done when" through the generated
Rust router on Postgres: audience reads, writes, searches and counts, a
named and an inherited audience and the cascade, an agent run, a row
rule through three relations, a grant that reaches only its paths and
ends by revocation, date and state, a reveal, and the access log.

Left:

- Grants, the access log and grant reach on the Rust server's SQLite;
  an audience stays Postgres's in every language.
- A generated Rust server that wires `GrantAuthenticator` and `Logged`
  itself; an app wraps the authenticator its `Handlers` take.
- The typed filters' one-to-many members; a path filter is the
  untyped repositories' and the table operations' so far.
- The D152 to D159 features the refusals name, which the later Rust
  entries port: D185 ports D154 and D159.

## D182. The TypeScript repositories run on SQLite and D1, an audience there is a JSON array of ids a trigger keeps and a read reaches through json_each, references and files are row triggers, and no trigger counts on another firing (D79, D116, D154, D156 amended)

D156 and D154 were Postgres alone: the loader refused `@audience`,
`@references` and `@attachments` on the sqlite dialect, and the TypeScript
repositories ran on a pg Pool alone (D79), so a database that listed
sqlite first had no repositories in TypeScript at all. This entry builds
both on SQLite and D1, for TypeScript, as the first of the SQLite
workstreams; the calendar, search, the notifier's queues, parties and
agents follow on what it settles, and so do the Go and Rust ports to
SQLite.

| Decision | Alternatives not taken |
|----------|------------------------|
| The TypeScript repositories run on SQLite. A types package of a DB service that lists sqlite carries `orm-sqlite.ts`, its `./orm/sqlite` entry: each table's definition again, its statements in SQLite's words as sqlutil writes them for the Rust repositories (`SQLiteTableStatementsOf`, `SQLiteClauses`, a key's `SQLite()` reads, D116), its JSON columns read as text, and `createOrm(db)` over a `SqlDatabase`, which the runtime's `sqliteOrm` runs. One runtime (`orm.ts`) runs both dialects from the definitions; `OrmDefinition.dialect` says which. A service that lists sqlite and not postgres gets an `orm.ts` of types alone. A table `SQLiteTableRefusal` refuses is left out of the entry, with why, as Rust leaves it out. | A second runtime for SQLite, which would drift from Postgres's. The access features over raw SQL beside no repositories, which would repeat every read's audience and grants by hand and miss a hand-written implementation's reads. |
| The runtime imports neither pg nor `node:crypto`: pg's type parsers come with `ormOf` (`orm-postgres.ts`), the pure text forms with `pgtext.ts`, the field error with `fieldcrypt-error.ts`, and a key secret is read by the entry that has `node:crypto` (`OrmRuntime.parseFieldKeys`). `./d1` exports `sqliteOrm`, and `./sqlite` it and `parseFieldKeys`. | A D1 build of its own of the runtime. Encrypted fields on D1 through WebCrypto now: its API is asynchronous and the repositories seal synchronously (left). |
| `sqliteOrmPool` binds a `Date` as its RFC 3339 text to the millisecond, as the SQLite DDL's defaults write a time (`strftime('%Y-%m-%dT%H:%M:%fZ')`), so times compare in order as text; a list and an object as JSON text; a bigint as a number. The codec reads a list's JSON text, as SQLite holds a `UUID[]` (sqlmigrate's `sqliteType`). | A time as epoch milliseconds, which the DDL's defaults and the Rust repositories do not write. |
| A `SqlDatabase` is one connection, so the pool takes it in turn: one statement or one transaction at a time, a statement within the running one joining it (an `AsyncLocalStorage` of the turn), one from elsewhere waiting. On `openSqlite`'s connection (`beginsTransactions`) a transaction is `BEGIN IMMEDIATE` and a nested one a savepoint. D1 runs no interactive transaction: there a transaction's statements run in turn, each committed as it runs, and a throw rolls none back. | Refusing `orm.transaction` on D1, which `references.add`, `attachments.begin` and every table operation run in. A batch, which takes no read between its statements. |
| An audience on SQLite is a JSON array of ids in `effective_audience TEXT`, null for everyone, and a read keeps to `(effective IS NULL OR EXISTS (SELECT 1 FROM json_each(effective) WHERE json_each.value IN (?n, ...)))` (`sqlutil.SQLiteAudienceContains`, in the definition as `audience.contains` and `placeholder`). | A side table of (row, principal), which every read joins and every resolution rewrites, the alternative D156 turned down on Postgres too. |
| SQLite has no trigger functions, cannot set `NEW`, and does not fire a trigger already running (`recursive_triggers` is off unless a connection turns it on, which a migration on D1 cannot), so no trigger counts on another to finish its work. Each `@audience` table has `trg_<table>_audience_insert` and `_update`, after the write, which refuse an unknown audience or agent access (`RAISE(ABORT)`) and set the resolved columns from the row as the table holds it; the update's fires only when they are not what the audience resolves to, which stops it whether or not recursion is on. A parent table's `trg_<table>_audience_cascade` sets each inheriting child's directly; a table that inherits from itself sets every inheriting row below the parent through a recursive CTE, each the parent's audience and the least open agent access on its way down, then the other child tables' rows below any of them. | A cascade that updates the children's `audience` to itself, as Postgres's does, which on SQLite would not fire the child's trigger again when the child is of the same table or its trigger is running. `PRAGMA recursive_triggers`, which a D1 migration cannot set. |
| References and files are row triggers of the Postgres triggers' names: a target's delete enqueues a cleared message per reference to the row and deletes them; a file's delete enqueues its object's release unless another file of the sub-table still names it, so a statement that deletes the files of one object releases it once. Of a statement that deletes a holder and a row it points at, a cleared message may name a holder the same statement deleted. | A statement trigger, which SQLite has none of. |
| The members of `orm.references` and `orm.attachments` run in the host's dialect (`rowfeatures.ts`, `sqlOf`): a parameter `?n`, `CAST(... AS TEXT)`, the database's clock, a ref block found through `json_each`, a time read back from its text, and no advisory lock, since a transaction on SQLite holds its one write lock and D1 runs each statement alone. | |
| On SQLite the repositories read a table that composes behaviors and refuse its writes, and refuse upserts and `mergeInto`, each naming why. | Serving the behaviors' core in the ORM's transaction over `sqliteRulesStore`, whose batch cannot nest in one (left). |
| The loader serves the three on sqlite, and refuses a service that lists sqlite and enables Go's or Rust's types, naming the language, since their repositories on SQLite would read the rows without them (`verify/sqlite_features.go`, `Input.TypesLanguages`). | Refusing sqlite until every language serves them. |
| D1 is proved under the pinned workerd by the fixture `d1-orm`, which runs a database's generated `orm-sqlite.ts` and `sqlite/create.sql` (kept current by `TestTheWorkerdD1FixtureIsCurrent`) over a stand-in of D1 on a Durable Object's SQLite storage, workerd's own SQLite, which runs no BEGIN, as D1 runs none. | Miniflare, which the repository does not pin. A stand-in that answers fixed statements, which proves no trigger. |

### The SQLite shapes later workstreams take

- A `UUID[]` column is TEXT holding a JSON array of hyphenated lowercase
  ids; a containment is `EXISTS (SELECT 1 FROM json_each(col) WHERE
  json_each.value IN (...))`.
- A time is TEXT, RFC 3339 in UTC to the millisecond
  (`2026-11-26T10:00:00.123Z`); the database's clock is
  `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`.
- A trigger is a row trigger named as the Postgres trigger, with the
  write's event after it when it needs one per event (`_insert`,
  `_update`); it refuses with `RAISE(ABORT, '<literal>')` (`Refuses`),
  reads the row it changes by its key rather than through `NEW`, guards its
  own update with a `WHEN` that is false once the row holds what it writes,
  and does every step itself rather than through another trigger; a walk
  of a tree is a recursive CTE in a subquery, which SQLite takes in a
  trigger.
- A queue's message a trigger enqueues is a plain `INSERT` of its fields,
  the queue's defaults filling the rest.
- A key compared across tables is `CAST(key AS TEXT)`.
- A repository's statement is sqlutil's SQLite form; a count or an
  `EXISTS` is read by its first column, which SQLite names after its
  expression.

Built: the SQLite triggers and their model, with the backfill of an
audience (`internal/generator/sqlgen/audience_sqlite.go`,
`rowrefs_sqlite.go`, `internal/sqlmigrate/sqlite_model.go`); the audience
predicate (`sqlutil/audience_sqlite.go`); `orm-sqlite.ts` and the types-only
`orm.ts` (`internal/generator/tsgen/orm_sqlite.go`,
`templates/orm_sqlite.tmpl`); the runtime's `orm-sqlite.ts`,
`orm-postgres.ts`, `pgtext.ts`, `fieldcrypt-error.ts` and the dialect of
`orm.ts`, `rowfeatures.ts` and `pgcodec.ts`; the loader's refusal
(`verify/sqlite_features.go`); the guides' "On SQLite and D1" sections.
`fixture-access-db` and `fixture-references-db` list sqlite. The tests:
`TestAudienceTriggersOnSQLite`,
`TestAudienceCascadeOfATableThatInheritsFromItself`,
`TestRowFeaturesTriggersOnSQLite` (sqlgen); the SQLite model goldens, the
round trip and convergence of both fixtures (sqlmigrate);
`TestRowFeaturesOnSQLite` and the audience refusals (loader);
`TestTheSQLiteDefinitionOfATableIsPostgres`; `orm-sqlite.test.ts`;
`TestGeneratedAccessEndToEndOnSQLite`, which runs every case of D156's
"done when" over the generated router on bun:sqlite, the calendar feed's
among them; `TestReferencesTablesEndToEndOnSQLite`, D154's on bun:sqlite,
the thread's message written by hand; and the workerd fixture `d1-orm`.

Left:

- Go and Rust on SQLite: their repositories' audience, grants, references
  and attachments over these shapes, which lifts the loader's refusal.
- The generated TypeScript server over a database that lists sqlite first:
  `Deps.orm` built from `createOrm(db)` of `./orm/sqlite`, which lifts
  tsrestgen's refusal of a table operation there (D106); an app passes
  `orm` in the router's options now.
- The behaviors' core, upserts and merges in the SQLite repositories.
- `Encrypted<T>` fields on D1, which need a cipher a Worker has.
- An atomic write of several statements on D1, through its batch.
- A cleared message that names a holder the same delete statement
  deleted, which a row trigger cannot tell.
- The calendar, search, the notifier's queues, parties and agents on
  SQLite and D1 (D186, D189), on these shapes.

## D183. An entered sender's run asks before every write, a party's run reaches only its grant's paths, a generated MCP tool carries its operation's @autonomy, and a generated TypeScript server builds the dispatcher into its Deps and runs its queues (D157, D158 amended)

D158 left five of its own follow-ups in TypeScript. A run an inbound
message started recorded the message's trust and ran as a verified one's;
a run acting for a party reached through every path the agent's role
names, whatever the party's grant reaches; `mcpGate` held every generated
tool as an internal write; an app wired the dispatcher, its routes and its
workers by hand; and the schema writer dropped `@agents` and `@autonomy`.

### Trust

| Decision | Alternatives not taken |
|----------|------------------------|
| A run an inbound message started records the message's trust on the run (`trust`, a field the loader adds to the runs part): `verified` when the message's row says so, and `entered` for anything else, an identity entered by hand, a row with no trust, a message the dispatcher cannot read or that the request carries as text. The dispatcher reads it from the message's row, never from the request. | The request's word for it, which any enqueuer could forge. A missing trust read as verified, which fails open. |
| An `entered` sender's run reads as its policy says, and every other operation asks, whatever a policy row or the tool's mode says, as a money movement always does (`effectiveAutonomy`). `gate` reads the trust from the run's row, so the app's tools, `mcpGate`, `message_party` (through `sendPolicy`) and `ask_principal`, now gated as an internal write, all hold to it. The token's claims carry the trust (`claims.trust`) for the app; the approval's summary says the sender was entered by hand. | Its role narrowed to read and reply, which a write the party's grant allows could then never make, even with a person's approval. Refusing the run, which drops a real message from a known party. Narrowing its permissions, which would leave the policy rows to say auto for what remains. |

### A party's paths

| Decision | Alternatives not taken |
|----------|------------------------|
| A run acting for a party reaches, from each target, only the paths the agent's role names and the party's grant of the same target names too (`narrowReaches`), read on every call: the grant the run is under when the run names it, else each active grant of the party. A target no grant of the party names, the party's own row and its message's, gives no path: the run reaches the row alone. A grant that ended gives none. | Issuing the run's grants with a role of the intersection, a role row per run. Paths fixed when the run starts, which a grant revoked mid-run would not narrow. |

### The generated MCP tools

| Decision | Alternatives not taken |
|----------|------------------------|
| A tool's record in the router's table carries its operation's `@autonomy` (`autonomy: { class, mode? }`), which TypeScript's `McpRouteTool` declared and `mcpGate` already read; Rust's router reads the same table and ignores it. A tool whose operation declares none carries none, and the gate holds it as an internal write. | The class in the tool's `_meta`, which `tools/list` would show every client. |

### The generated wiring

| Decision | Alternatives not taken |
|----------|------------------------|
| A TypeScript API whose database declares the orchestration (and whose types write the repositories) holds the dispatcher in its `Deps` (`agents: Agents`). The API package's `agents.ts` exports `withAgents(deps, options)`, `createAgents` over `deps.orm` and the `@agents("requests")` queue with the app's options, and `dispatchAgentMessage`. Every entrypoint that builds `Deps` builds it but the dispatcher, asks the implementation's `createAgentOptions` (`AgentOptionsConstructor`, of `Deps` but its dispatcher) for the rest of `createAgents`' options, and builds `Deps` with `withAgents`. The scaffold writes a `createAgentOptions` that names an endpoint alone, and a build refuses an implementation that exports none. | Providers, the notifier and the inbound path built by the generator too, which D159, D153 and D157 leave to the app until the generated server builds each. The app building `createAgents` itself, which leaves the ORM and the queue to it. |
| A server mounts `deps.agents.routes()` ahead of the API's router, which answers only under the endpoint's path, and passes `agents.mcpGate` as its MCP endpoint's call gate. | Mounting the routes at a fixed path, which the endpoint the sandbox is given must name anyway. |
| A `@worker` of the requests queue or of D157's `@runRequests` queue is the dispatcher's: its entrypoint hands each message to `agents.handle` (`dispatchAgentMessage`) and calls no `createWorkers`, the requests queue's prune deletes the transcripts past their retention too, and the `Workers` interface and the implementation check have no method for either. Any other `@worker` stays the implementation's. | `inbound.runWorker(pool, m => agents.handle(m))` in the generated worker, which needs the app's inbound path in a process that runs nothing else of it; the queue is a queue, and its handler is the dispatcher's either way. |
| The behavior worker's event handler takes the transaction as `Deps`' transaction type: the runtime's `OrmTx` lacked the `references` and `attachments` members a database with `@references` adds, so an API over one did not type-check. | A wider `transaction` type in the runtime, which every ORM caller reads. |

### The schema writer

| Decision | Alternatives not taken |
|----------|------------------------|
| The TypeScript writer writes an orchestration class as `@agents("<part>")` and an operation's `@autonomy(class, mode?)`; the loader's fields and indexes carry `ir.OriginAgents` and are left out, so reading it back fills them again. The data forms carry both already. | Writing the part's fields, which the reader would then hold to the part's scalars twice. |

Settled. An entered sender's run is held, not refused: it reads, and a
person approves each write. A run reaches through a path only when both
the agent's role and the party's grant name it. The generated server
builds the dispatcher, its routes, its MCP gate and its queues' workers;
the app gives the options only it knows.

Built: the runs' `trust` (`internal/loader/agents`); `trustOf`,
`effectiveAutonomy` and `narrowReaches` in `agents.ts`, the dispatcher
recording the trust, the run's principal narrowing its paths and carrying
the trust, `gate` holding an entered run to ask, `ask_principal` gated;
`@autonomy` in `mcptools`' tool records; the agents' `Deps`, `agents.ts`
and the scaffold's `createAgentOptions` (`tsrestgen`), the entrypoints'
dispatcher, routes, MCP gate and workers (`servergen`), the build's check
of `createAgentOptions` and of the workers' methods; the behavior worker's
transaction type; `@agents` and `@autonomy` in the TypeScript writer;
`examples/agent-orchestration`'s stack and implementation in place of its
hand wiring; the guide's "Wire the dispatcher". The tests:
`TestAgentRunsEndToEnd`, on Postgres, now with a verified and an entered
sender's runs, a party's paths narrowed and ended with its grant;
`TestMCPToolsCarryAutonomy`, `TestDepsHoldTheAgentDispatcher`,
`TestTypeScriptRouterHoldsAgentRunsToTheirAutonomy`,
`TestTSWriterWritesAgentOrchestration`, and
`TestAgentOrchestrationExampleUsesTheGeneratedWiring`, which builds the
example's stack and type-checks its server and workers against its
implementation.

Left:

- Go, Rust and SQLite: the trust, the narrowed paths, the tool records'
  `@autonomy` read by their gates, and the generated wiring (D158's left).
- A run's token on the API's own routes: the generated server accepts it
  only where the implementation's authenticator does
  (`deps.agents.authenticator(inner)`), and a route holds the call to its
  run's autonomy only where the implementation asks `deps.agents.gate`;
  the generated router could gate every route an agent calls as the MCP
  endpoint does.
- The approval routes (`<endpoint>/approvals/<id>/approve`) in the
  generated server, which `routes()` serves only with an authenticator of
  a `Request`; approvals are decided through a reply or the app's own
  operation meanwhile.
- The channel's trust (D153's `channelTrust`) narrowing an inbound run
  beside the identity's.
- A trust read from a request that names its message as `{ table, key }`,
  which runs as entered now.
- A test that builds an API with the orchestration for Cloudflare: the
  Workers entrypoints (`fetch.ts`, `drain.ts`) carry the same wiring, and
  the example's stack has a local environment alone.

## D185. The Rust runtime and server serve D159's runtime providers and D154's references and attachments on Postgres, a Rust provider is a crate whose Cargo.toml declares it, and a provider holds its principal in its context (D154, D159, D181 amended)

D159 built the five runtime interfaces in TypeScript alone, and D154 the
references and attachments of the TypeScript repositories; D181 refused
both in the Rust outputs. This entry ports them to Rust, on Postgres, with
the TypeScript tables, statements, locks, checks and refusals. It is the
second of the Rust parity entries.

| Decision | Alternatives not taken |
|----------|------------------------|
| The runtime's `providers` module holds the five interfaces as traits (`CredentialProvider`, `ChannelAdapter`, `SandboxProvider`, `ModelProvider`, `FileProcessor`), the contracts (`RUNTIME_CONTRACTS`, held to `runtime_contracts.json` by a test as TypeScript's is), `define_provider`, `ProviderSettings`, `providers_from` and `Providers`, `use_credential` and `ExtensionAuthenticator`. A required operation is a trait method with no default, so the compiler holds a provider to it; an optional one (a channel's `status` and `inbound`) has a default that answers `Unsupported`, and the definition lists the optional ones it implements. A model's `embed` answers `None` by default, as the TypeScript one is left out. | A runtime check of each instance's methods, which TypeScript needs and Rust's types make moot. |
| A Rust provider is a crate whose Cargo.toml declares it under `[package.metadata.superschematic.provider]`, the keys of a package.json's `superschematic.provider`, and whose root exports `pub fn definition() -> ProviderDefinition`. An API's config lists its directory as it lists a package's; the loader reads either manifest, or both when the directory holds both, which then declare one provider (`RuntimeProvider.Crate`, and `Dir`, which no schema.json holds). A Rust server refuses a listed provider with no crate. | A crate named by its crates.io name, which a provider an app writes for itself has none of; a manifest file of its own, a third form beside package.json and Cargo.toml. |
| The generated Rust server links the listed crates by path and holds `Arc<Providers>` in its `Deps`, which its entrypoint builds with the API crate's `providers::from_config`: the slots, each crate's `definition()`, and the manifest the build read of each, which `check_provider_manifest` holds the definition to at start, so the crate that runs is the one the build checked. A `Secret<string>` field becomes a `SecretValue`, an empty one unset. The TypeScript server's wiring stays the project's (D159's left). | The project's wiring calling `providers_from`, which a Rust server's generated entrypoint would leave it no place to do. |
| Rust has no ambient caller (D181), so a provider does not run within its principal: its `ProviderContext` carries it (kind `extension`, subject `extension:<name>`, its role), and a provider that writes through the app's repositories opens them `with_access(AccessCaller::of(Some(&context.principal)))`; `Providers::principal_of(slot)` gives the same to core. `create` is synchronous, and made on first use. | A task-local caller, which D181 turned down. |
| `Injection`'s fields are private, a provider makes one only from a `SecretValue`, and `apply_injection` takes it by value, so no other type passes for one and the value is gone once applied. Its debug, display and JSON forms show the reference and target alone. | A wrapper the caller unwraps. |
| The repositories' `references()` and `attachments()` (`orm::rowfeatures`) run the TypeScript runtime's statements over the definition rustgen writes into the Postgres definition (`references`, `attachments`, each keyed by type name, since a Rust repository is opened by its type). A visible row is read through its table's repository, which keeps the caller's audience and grants (D156, D181). A write that takes an advisory lock and more than one statement (`add`, `begin`, `file`, `release`) runs in the caller's transaction, and a client outside one is refused, as a write through the behaviors' core is. A refusal is `Error::Refused` of its code, status 422, as the example server answers TypeScript's `RowFeatureError`. | A savepoint per call, which needs a mutable client the repositories do not hold; `BEGIN` on a pooled connection, which a dropped future would leave open. |

Built: the runtime's `providers` module
(`runtime/http/rust/src/providers/`) and `orm::rowfeatures`
(`runtime/http/rust/src/orm/rowfeatures.rs`, with `Definition.references`
and `.attachments`); rustgen's definitions and the generated
`references()` and `attachments()` (`internal/generator/rustgen/rowrefs.go`,
golden `fixture-references-db`); the crate manifest
(`internal/loader/providers.go`); the API crate's `src/providers.rs`, its
Cargo dependencies and `Deps.providers` (`internal/generator/rustrestgen/providers.go`),
and the entrypoint's (`servergen`); the refusals of D181 lose
`@references`, `@attachments` and `@providers` slots and gain a provider
with no crate. `examples/runtime-providers/rust` ports a stub of each
interface: sms-relay-a and sms-relay-b, a vault broker, a local-process
sandbox, a model stub and the text-preview processor.
`TestRustReferencesEndToEnd` runs D154's "done when", the TypeScript
repositories' five cases, through the generated Rust repositories on
Postgres, over a bucket in memory that checks each PUT against its signed
URL. `TestRustProvidersEndToEnd` builds a Rust server whose config lists
the six crates and runs D159's: each provider in the server with no change
to core and as its own principal, the SMS swap as a config change with the
deliveries recorded through the repositories and read back through the
router, a credential that reaches the runner alone, the refusals at start,
and a provider calling the app with its key and its role alone.

Left:

- Rust on SQLite: TypeScript serves references and attachments there
  (D182), and the loader refuses a service that lists sqlite and enables
  Rust types with either (`sqliteReaders`), which this entry holds Rust
  to now that its outputs no longer refuse them on every dialect.
- The coverage check (`no-provider`) is the stack's and holds for a Rust
  API as for any; no Rust feature registers one yet, since notifications,
  agents and attachments' processing are not served in Rust.
- The file processor's wiring to `pending` and `processed`, which neither
  runtime does yet (D154's and D159's left).
- A generated Rust server that wires `ExtensionAuthenticator` itself, from
  keys the API's config holds; an app passes it in its `Handlers`.
- The Rust stubs answer from an outbox in the process, where the
  TypeScript ones call their service over HTTP: a real adapter crate
  brings its own client.

## D184. The Go runtime serves D159's five provider interfaces, a Go server imports each provider's Go module and builds its providers into Deps, and the Go repositories serve D154's references and attachments (D154, D159, D180 amended)

D159 built the runtime extension points in TypeScript and D154 the
references and attachments of its repositories, and left Go's. This entry
ports both onto D180's principal, with the TypeScript runtime's tables,
statements, refusals and parity files, and settles what Go's shape asked
for. The Rust chain ports the same two in D185.

| Decision | Alternatives not taken |
|----------|------------------------|
| The Go HTTP runtime's `providers` package holds the five interfaces, a method per operation, `context.Context` first. An optional operation is an interface of its own (`ChannelStatusReader`, `ChannelInboundReader`; a model's `ModelEmbedder`, which is no contract operation, as it is none of TypeScript's). `Contracts()` is `ir.RuntimeContracts`, and its test holds it to `runtime_contracts.json` and each Go interface's methods to its contract's operations. | Optional methods on the interface answering an `Unsupported` error, as Rust's defaults do, which a type assertion could no longer tell from an adapter that has them. |
| A Go provider is a Go module in the provider's directory, the one the API's config lists: `go.mod` names it, and its root package exports `Definition() providers.Definition`. The directory declares the provider once, in its `package.json` beside a TypeScript package, or for a provider of Go alone in `superschematic.json`, whose `provider` object is the `package.json`'s `superschematic.provider`; both, when present, declare one provider. The loader puts the module on `ir.RuntimeProvider.Module` and the directory on `Dir`, which is not written. | A module path in the API's config, a second list beside `providers`; a declaration in `go.mod`, which has no place for one; a Go module in the `package.json`'s manifest, which would make the TypeScript package name the Go one. |
| The generated Go server of an API with slots writes `providers.go`: `ProviderSlots`, `ProviderDefinitions()` (each listed module's `Definition()`), the manifests the build read, and `ProvidersOf(EnvConfig, logger)`, which reads the slot and setting fields of the config, a `Secret<string>` field as a secret, into `providers.From`. `Deps.Providers` holds the result, the entrypoint builds it and stops when it does not read, and the API's and the server's `go.mod` require and replace each module. The loader refuses a Go server a listed provider of no Go module, in place of D180's refusal of every slot. | The implementation calling `From` itself, as TypeScript's wiring does still (D159's left), which every Go API would repeat. |
| `From` holds each linked definition to the manifest the build read (`CheckManifest`) and refuses one the config lists but the server does not link, as Rust's `providers_from` does, beside TypeScript's checks, with its messages. A Go config holds a secret as a string, so `From` reads `ConfigValue`s, `Text` or `Secret`, which the generated code writes from the IR. | Telling a secret from a plain string by its value, as TypeScript's `Revealable` does, which a Go string cannot. |
| Go has an ambient caller, the context, so a provider runs within its principal as in TypeScript: every call `From`'s providers answer is made with a context that `Options.Bind` binds to the provider's principal, `access.Principal{Subject: "extension:<name>", Kind: extension}` (D180's), with its role, a `session.Role` of its permissions. The generated server binds `orm.WithPrincipal` too, so a provider reads what everyone may read; an audit column, which holds a user's id, stamps no one for it. `Providers.Within(ctx, slot)` is that context, for a route that handles what a provider posts back. `ExtensionAuthenticator(keys, inner)` composes with `session.GrantAuthenticator`. | A provider context with the principal alone, as Rust's must, which a provider would have to bind itself before every read. |
| An `Injection` has unexported fields alone, so no other package makes one that holds a value: an `Injection{}` is refused by `ApplyInjection` and `UseCredential`. Its JSON, `String`, `GoString` and `Format` print the reference and the target. `SecretValue` and `ConfigValue` mask the same way in every `fmt` verb and in zap. | A value with a getter behind a build tag; a mask on `String` alone, which `%#v` would pass. |
| References and attachments are the schema runtime's `rowrefs` package, over a `Host` the generated ORM implements (its pool or transaction, the caller of a context, and `Visible`, which reads a table's rows through its repository and so keeps to the caller, D180). ormgen writes `rowrefs.go` for a database with `@references`, `@attachments` or a table's Comments, with tsgen's definitions keyed by type, and `References()` and `Attachments()` on the database, a transaction and their interfaces. A write of several statements runs in the database's transaction or a savepoint of the caller's, as TypeScript's does. | The package in the HTTP runtime, which every ORM would then require; generating the statements per table, which the definitions already hold. |
| A bucket is what `rowrefs.Bucket` names (sign, open, delete), and `bucket.ForAttachments` makes one of a listed `bucket.Bucket`. A key is a row's wire form, a UUID in base62, and either form is read. | The ORM importing the HTTP runtime's bucket package. |

Built: `runtime/http/go/providers` and its test; `runtime/schema/go/rowrefs`
and `runtime/http/go/bucket/attachments.go`; ormgen's `rowrefs.go`
(`ormgen/rowrefs.go`, `templates/rowrefs.tmpl`) and the comments fixture's
goldens; the loader's Go module (`internal/loader/providers_go.go`) and
`ir.RuntimeProvider.Module`; apigen's `providers.go` and `Deps.Providers`,
servergen's wiring and go.mod; the Go modules of
`examples/runtime-providers`' six providers, each held to its
`package.json` in its own test; `fixture-providers-go-api`.
`TestGoReferencesEndToEnd` runs D154's "done when" through the Go
repositories on Postgres, the TypeScript and Rust cases, over a bucket
whose uploads a browser sends to the URL `Begin` signed.
`TestGoProvidersEndToEnd` runs D159's in a generated Go server: the
credential provider and a channel adapter with no change to core, the
SMS provider swapped by the config alone, a bad config refused at start,
a provider as its own principal at the ORM and at a generated route, a
sandbox, a model and a file processor, and the Deps' references and
attachments. `TestAGoServerReadsItsAPIsProviders` holds the entrypoint
and its go.mod.

Left:

- The features' `Uses`: attachments' `files` and agent runs'
  `credentials` (D159's left), in every language.
- Generated routes, MCP tools and SDK methods for references and
  attachments, as in TypeScript (D154's left).
- Go on SQLite and D1: the loader refuses a database's Go types of
  either declaration there (D182), which TypeScript alone serves.
- A provider package named by its Go module path alone, resolved from
  the module cache rather than a directory.

## D187. The Go server serves D152's Recurrence and calendar and D155's search across tables on Postgres, through a schema runtime package the generated ORM hosts, a read across tables takes its caller, and a stop never strands a claim (D152, D155, D180 amended)

D152 and D155 built Recurrence's routes and worker, the calendar and its
feed, and search across tables in the TypeScript runtime; D180 refused
them in a Go server. This entry ports them to Go, on Postgres, with the
TypeScript tables, statements, ranking, states and refusals. The Rust
chain ports the same two in D188, and Go keeps to it where the languages
allow.

| Decision | Alternatives not taken |
|----------|------------------------|
| A Go server serves a table that composes Recurrence through the adapter it serves every behavior through: its `recurrence` routes run `setRecurrence`, `complete`, `skip`, `moveOne` and `listOccurrences` through the core, and the behavior worker's generated queues fire the `open`, `remind`, `overdue` and `plan` deadlines, hear a raw write on the change queue, and hand `opened`, `reminded` and `missed` to the Workers methods. The refusal was lifted; what was missing was the ORM of a table the core writes for another's behavior with no use, deadline, change or event of its own (a relative anchor's target), which imported `encoding/json` for nothing and so never compiled: it keeps the import in use (`Repository.BehaviorsWithoutJSON`). | A Recurrence module of the Go runtime, a second runner beside the adapter's. |
| The calendar and search are one package of the Go schema runtime, `crosstable`: `ReadCalendar`, the feed (`IssueFeed`, `RotateFeed`, `RevokeFeed`, `FeedOf`), `CalendarICS`, `CalendarRoutes` and the MCP tool; `SearchTables`, `IndexSearch`, `ReindexSearch`, `SearchRoutes`, `SearchMCPTool` and `CallSearchTool`. ormgen writes `crosstable.go` for a database with a calendar source or a `@searchable` table: the definitions, and `Database.CrossTables()`, the `crosstable.Host` over its pool, its access predicate and its grants. Routes are `http.Handler`s and take an `Authenticate` of the request. | The HTTP runtime, which the ORM cannot import (D184 kept `rowrefs` in the schema runtime for the same reason); two packages, which would repeat the host and the caller. |
| A Go read across tables reads a table's rows by one statement held to the caller by the predicate the table's repository applies (`AccessConditions`, D180) and its live condition, where TypeScript and Rust read them through the repository by its fields: a Go repository is typed per table and has no filter by a field's name. So the calendar's definition holds the columns, quoted, its title, field, end and owner are of, and the search's is tsgen's, member for member. A calendar source titled by an `Encrypted<T>` field, whose column holds it sealed, is refused in a Go server. | A generated read per table and source, which would repeat the repository; reading the rows' titles back through `GetManyByIDs`, a second read per source. |
| A read across tables takes its `Caller`, a principal and the permissions it holds, and binds the principal itself (`Host.As`, `orm.WithPrincipal`), as Rust's takes its principal (D188): a feed's fetch reads as the feed's holder, not the request's caller. Go's `access.Principal` holds no permissions, which a source's or a table's permission is checked against, so the caller carries them beside it. | Reading the ambient caller of the context, which a feed's route would have to rebind; a permissions member on `access.Principal`, which every other reader of it would carry. |
| Search's audience is checked on the index row's copied columns by the package's own condition, the generated `audienceCondition`'s over `si.audience` and `si.agents`, since the ORM's predicate names a table's own columns; a caller with grants is held by an `EXISTS` back to each table with `Host.Conditions`. | Copying the grant reach into the index, which D155 turned down. |
| `IndexSearch` runs in a transaction of its own on the host and embeds after it commits, as `indexSearch` and Rust's `index_search` do. | The caller's transaction, which would hold the share lock across the model's call. |
| Embeddings re-rank through `providers.SearchEmbeddingsOf`, which refuses at once a model provider that implements no `providers.ModelEmbedder` (D184), telling it by its type, where Rust asks the provider to embed nothing. A search's `Embedder` is an interface, with `EmbedderFunc`, so a test or an app gives its own. | Asking at the first search, which would fail one late. |
| The Go worker no longer cancels a claim in flight at a stop: it runs the claim to its answer within its own timeout and releases what it claimed, whose handlers never start. Cancelling it could leave messages the database had claimed with no handler until their lease ran out, as D188 found in Rust; a test that stops its worker between fires runs into it. | Starting the claimed handlers at the stop, which the grace then waits for. |
| A Go server whose database lists `sqlite` first is refused Recurrence and a calendar source (`verify.GoServerSQLiteErr`, which the generator calls with the database's dialects), as rustrestgen refuses a Rust server; `@searchable` on SQLite stays the loader's refusal for every language. | Serving the calendar on SQLite, D152's left for every language. |

Built: the refusals of D180 lose `@calendarSource`, Recurrence and
`@searchable` (`verify/go_features.go`), and gain the sealed title and
`GoServerSQLiteErr` (`internal/generator/crosstable_go.go`);
`runtime/schema/go/crosstable` and its tests; ormgen's `crosstable.go`
(`ormgen/crosstable.go`, `templates/crosstable.tmpl`, goldens of
fixture-recurrence-db, fixture-access-go-db and fixture-search-db) and
`BehaviorsWithoutJSON`; `providers.SearchEmbeddingsOf`; the claim at a
stop (`worker.go`); fixture-access-go-db's document calendar source.
`TestGoRecurrenceEndToEnd` runs D152's cases through a generated Go
server and worker on Postgres: the plan to the horizon off a holiday with
its deadlines, a rule change that leaves the open occurrence, the remind
deadline on the worker and its events, `complete` and an afterDone
recurrence, the refusals, the calendar merging a date and occurrences,
and a raw write heard. `TestGoAccessEndToEnd` gains the feed's case: a
feed shows what its principal reads, busy or in full, is no credential
elsewhere, and is refused once rotated or revoked. `TestGoSearchEndToEnd`
runs D155's six cases through the Go repositories and `crosstable`, a
party's and an agent run's grants among them, with embeddings through a
model provider and `ReindexSearch`.
`TestAGoServerOnSQLiteRefusesRecurrenceAndTheCalendar` holds the
refusal. The guides' Go sections.

Left:

- Go on SQLite: Recurrence on a Go server and the calendar (D152's "the
  calendar on SQLite and D1"), and search (D155's).
- A generated Go server that mounts `CalendarRoutes` and `SearchRoutes`
  and serves the two MCP tools; an app mounts them, as in TypeScript
  (D152's and D155's left).
- A generated handler for a `@searchable` declaration's queue, which an
  app's `@worker` writes, calling `IndexSearch`.
- A calendar source titled by an `Encrypted<T>` field in Go.
- A parity file of the calendar's reference and iCalendar text, which
  each language's tests hold to the same algorithm on their own.

## D190. The Go runtime serves D153's notifier on Postgres: the generated ORM hosts it, `notify` raises, routes, delivers, escalates and reads replies as the TypeScript notifier does, and an approval's approve and deny are two reply actions (D153, D180 amended)

D153 built the notifier in the TypeScript runtime, and D180 refused
`@event`, `@notifier` and `@channelIdentities` in a Go server. This entry
ports it to Go, on Postgres, with the TypeScript tables, statements,
states and refusals, and keeps to the points the Rust port of the same
decision (D191) settled where the languages allow.

| Decision | Alternatives not taken |
|----------|------------------------|
| The notifier is the Go HTTP runtime's `notify` package, since it sends through the channel adapters an environment configures (D159), which the ORM cannot import. What it runs through is the schema runtime's `notifications.Host`, which the generated ORM of a database that uses the notifier implements in `notifications.go` (`Database.Notifications()`): its pool or a transaction, its dispatch queue, its access predicate and each table's repository, with the definitions it is generated with: the notifier's tables by part and their columns, and every table's key, events and to-one relations (`ormgen/notifications.go`). | One package in the schema runtime, as D187's `crosstable`, which would need the providers and the worker of the HTTP runtime; a generated notifier per database, which would repeat the notifier in every ORM. |
| The notifier's own tables are the loader's, the same fields in every database, so `notify` reads and writes them by statements over the columns the definitions name, a UUID in base62 as the Go types write one. An application's table it reads through that table's repository (`Host.Record`, `Find`, `Update`): a route's filter, a brief's saved query and an approval's update are JSON in the repositories' syntax, which the ORM reads into the table's typed filter or update, refusing a member it has no field for. | Statements over the application's tables, as `crosstable` reads them, which would leave a soft delete, a sealed field and the audience to the notifier; a filter compiler of the notifier's own beside the ORM's. |
| Go has an ambient caller, the context. A recipient's read of the record is the repository's read in a context bound to the recipient (`Host.As`, the ORM's `WithPrincipal`): a user by its id, a party or an agent run with its active grants. A reply's action runs in a transaction of the database as the sender (`Host.Acting`: `WithPrincipal`, and `WithBehaviorCaller` with the permissions the channel's trust leaves it), and its `ReplyContext` holds that transaction as the ORM's `TxInterface`. | Repositories opened for the principal, as Rust's are (D191), which Go's typed repositories need no second set of. |
| `Raise`, `FromBehaviorEvent`, `Cleared` and `ScheduleSubscription` take the caller's transaction as the ORM's `TxInterface` (`Host.In`), so the event commits with the change; nil opens a transaction of its own. `FromBehaviorEvent` takes the event as the Go behaviors adapter hands a project's Workers method: its kind, the row's key and its payload. | A transaction type of the notifier's own, which a Workers method would have to convert to. |
| As in Rust: the wall time of quiet hours and of a digest's date is read from Postgres (`AT TIME ZONE`), and the pure rule over it (`QuietDelaySeconds`) is shared; a notice's next reference is taken in a savepoint of the routing transaction (the ORM's nested `Transaction`) and tried again when another notice of the recipient took it; a reply action (`notify.ReplyAction`: permission, describe, `Run`, `Undo`) and `ApprovalReplies(table, permission)`, the `approve` and `deny` of `approval.requested`, which decide the approval as the replier within the channel's trust, refuse an agent run and a decided approval, and write D158's `state`, `decidedBy`, `decidedVia` and `decidedAt`. | |
| Whether an adapter reports statuses or reads inbound messages is its definition's optional operations, which Go reads as the interfaces it implements, `providers.ChannelStatusReader` and `ChannelInboundReader`: `Providers` binds an adapter so it implements exactly the ones its instance does (`CheckInstance`). | A list of operations beside the adapter, which in Go is the interface's own answer. |
| A trust row's permissions are `nil` for no cap and empty for none (`ReplyAllows`, `NarrowedPermissions`), as Rust's `Option` and TypeScript's `null`. | A pointer to a slice. |
| The pure parts are read from `runtime/http/testdata/notify_parity.json` (D191's), which TypeScript's `notify-parity.test.ts` writes: `NormaliseAddress`, `ReadReplyText`, `LooksLikeCode`, `QuietDelaySeconds`, `ReplyAllows`, `NarrowedPermissions`, `CodeHash` and `SameID`. | |
| Password reset is `notify.PasswordReset` over the identity service's new `UserOfLogin` and `ResetPassword`, which ends every session of the user. | |

Built: the refusals of D180 lose `@event`, `@notifier` and
`@channelIdentities` (`verify/go_features.go`); the schema runtime's
`notifications` package; ormgen's `notifications.go`
(`ormgen/notifications.go`, `templates/notifications.tmpl`, golden of
fixture-notifications-db); the HTTP runtime's `notify` package
(`runtime/http/go/notify`) and its parity test; `identity.Service`'s
`UserOfLogin` and `ResetPassword`; fixture-notifications-db's `Approval`
table, TypeScript's exported pure functions, the parity file and its
test, as D191 has them. `TestGoNotificationsEndToEnd` runs D153's cases,
the TypeScript repositories' ten, and the approval case of the Rust ones,
through the Go repositories and `notify` on Postgres over a stand-in SMS
relay and mail relay: an urgent event reaches two recipients, skips one
who cannot read the record, and escalates to email; `ack`, `done 142` and
`undo` as their sender with the audit; SMS refused `done`, and an address
entered by hand only acknowledging; a reply by reference; a code
verifying an address and an unknown sender reaching nothing; a failed
send falling through and an adapter down alerted once; quiet hours,
digests and record; statuses, inbound, a cleared reference and a
behavior's event; password reset by email; and an approval asked of its
approvers, refused by SMS, approved and denied by email, and decided
once. The guides' Go sections.

Left:

- Go on SQLite, as in every language (D153's).
- A route to an agent in Go, which is logged and reaches no one, and a
  party's channel identity that lapsed with its grants (D157's
  `identityLapsed`), which the Go notifier does not ask: D192's, which
  ports parties and agents to Go.
- A generated Go server that builds the notifier into its `Deps`, mounts
  the inbox and the relay routes, and fills a `@worker` of the dispatch
  queue with `Handle`; an app wires them, as in TypeScript (D153's left).
