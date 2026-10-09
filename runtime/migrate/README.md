# Migration runner

`superschematic migrate plan` writes a plan: the ordered steps that take a
DB service's database from one version of its schema to the next (D27 in
`docs/DECISIONS.md`). The runner applies a plan. It never computes one, so
a migration job needs the plan file and the `superschematic-migrate`
binary, not the compiler.

The runner is the Go module `github.com/parable-work/superschematic/runtime/migrate/go`,
the sixth Go module (D1, amended by D27). It holds the database drivers, so
none enters the compiler's module graph.

This file is the contract between the compiler, which writes plans, and
the runner, which reads them.

## The plan document

A plan is one JSON object. The compiler writes it in canonical form
(below); the runner accepts any JSON encoding of it.

| Member | Type | Meaning |
| --- | --- | --- |
| `version` | integer | `1`. The runner refuses any other. |
| `dialect` | string | `postgres` or `sqlite`. |
| `service` | string | The DB service the plan migrates. It keys the runner's state. |
| `from` | string | The hash of the model the plan starts from; `""` means an empty database. |
| `to` | string | The hash of the model the plan ends at. |
| `toModel` | object | The model the plan ends at. Its hash is `to`. |
| `expanded` | string | Present only when the plan has contract steps: the hash of the model the database holds between the phases, the previous model with every expand step applied. |
| `expandedModel` | object | With `expanded`: that model. Its hash is `expanded`. |
| `renames` | array of strings | The renames the plan was given, as written. Informational. |
| `steps` | array of steps | Every `expand` step, then every `contract` step. |
| `hash` | string | The plan's hash. |

A step:

| Member | Type | Meaning |
| --- | --- | --- |
| `index` | integer | The step's 1-based position in `steps`. |
| `phase` | string | `expand` or `contract`. |
| `op` | string | The operation, for people. The runner does not read it. |
| `subject` | string | The object the step changes, as a path (`table/order/column/total`). |
| `statements` | array of strings | Complete SQL statements without trailing semicolons. The runner runs each with one call, in order. A statement may contain semicolons inside a dollar-quoted body. The array may be empty: the step changes nothing and carries its hazards, and the runner logs it like any other. |
| `transactional` | boolean | Whether the step runs in one transaction. |
| `recovery` | array of strings | Absent unless `transactional` is false. Statements to run before a step that started and did not finish is run again. |
| `foreignKeysOff` | boolean | SQLite only, absent when false. Run the step with foreign key enforcement off and check every foreign key before the commit. Only plans written before D27's amendment on foreign keys set it: the compiler now writes every SQLite step to run with enforcement on, a rebuild deferring the checks to its commit with `PRAGMA defer_foreign_keys = ON` as its first statement. D1 cannot turn enforcement off, and its driver refuses a plan that sets it. |
| `hazards` | array | The step's hazards: `id`, `class`, `subject`, `reader`, `reason`. Informational to the runner. |

### Canonical JSON and hashes

Canonical JSON is what Go's `encoding/json` writes for a value decoded
with `UseNumber` into `any`: compact, object members sorted by key, array
order and number literals kept, `<`, `>` and `&` in strings escaped as
`\u003c`, `\u003e` and `\u0026`. It is `ir.CanonicalJSON` in the compiler;
the runner carries its own copy of the same function, and
`go/testdata/canonical.json` holds vectors the compiler's function wrote.

A hash is the lowercase hex SHA-256 of canonical JSON:

- a model's hash is taken over the model (`toModel`, or the file `adopt`
  reads);
- a plan's hash is taken over the plan object without its `hash` member.

Before it runs anything, `apply` recomputes both and refuses a plan whose
`hash` or `to` does not match, so an edited plan is never half-applied. It
refuses an `expanded` that is not the hash of `expandedModel`, one of the
two without the other, and an `expandedModel` of another service or
dialect.
It also refuses steps it cannot run as written: an `index` that is not the
step's position, an expand step after a contract step, a SQLite step that
is not transactional, and `foreignKeysOff` on a Postgres step.

## State

The runner keeps two tables in the connection's current schema
(Postgres) or in the file (SQLite). It creates them when they are
missing.

```sql
CREATE TABLE IF NOT EXISTS superschematic_schema_state (
  service     TEXT PRIMARY KEY,
  dialect     TEXT NOT NULL,
  model_hash  TEXT NOT NULL,      -- '' while no plan has finished
  model       TEXT,               -- canonical JSON of the applied model
  plan_hash   TEXT,               -- the plan in progress, or NULL
  plan_phase  TEXT,               -- 'expanded' or 'expand' once its expand steps are done
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()   -- TEXT on SQLite
);

CREATE TABLE IF NOT EXISTS superschematic_migrations (
  plan_hash   TEXT NOT NULL,
  step        INTEGER NOT NULL,
  service     TEXT NOT NULL,
  phase       TEXT NOT NULL,
  subject     TEXT NOT NULL,
  sql_hash    TEXT NOT NULL,      -- SHA-256 of the statements joined by "\n;\n"
  started_at  TIMESTAMPTZ NOT NULL,                -- TEXT on SQLite
  finished_at TIMESTAMPTZ,                         -- TEXT on SQLite
  PRIMARY KEY (plan_hash, step)
);
```

On D1 the runner keeps a third table, the lease that stands in for a lock
(`apply`, below):

```sql
CREATE TABLE IF NOT EXISTS superschematic_lock (
  service    TEXT PRIMARY KEY,
  holder     TEXT NOT NULL,     -- host:pid:random of the runner that holds it
  expires_at TEXT NOT NULL      -- D1's time, as RFC 3339 UTC text with milliseconds
);
```

The model is stored as text, not `JSONB`, so its bytes stay canonical.
SQLite has no `now()`: its `updated_at` defaults to
`strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, and the runner writes every time
there as RFC 3339 UTC text with microseconds
(`2026-10-04T16:42:18.146632Z`).

## `apply`

```
superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]
```

`--database-url` defaults to `$DATABASE_URL`. A `postgres://` or
`postgresql://` URL selects Postgres; a `sqlite:` URL, a `file:` URI or a
path selects a SQLite file; a `d1://<account id>/<database id>` URL selects
a Cloudflare D1 database, reached through Cloudflare's REST API with the
token in `$CLOUDFLARE_API_TOKEN`. SQLite and D1 run `sqlite` plans. The URL
must match the plan's dialect. `--phase` defaults to `all`.

On D1 (D27, amended: SQLite rebuilds with foreign keys on, and the runner
on D1):

- D1 has no `BEGIN` or `COMMIT`. A transactional step, with its log row
  and any state change, is one REST request whose statements run as one
  batch; the log row's primary key fails a repeated or concurrent run of
  the step, and the batch with it. The state and the log are read in
  requests of their own.
- The driver posts to the database's `raw` endpoint, which returns each
  row as an array in column order. A batch is the API's
  `{"batch": [{"sql", "params"}, ...]}` form: `PRAGMA defer_foreign_keys =
  ON` first, since D1 keeps foreign keys on, then the lease's renewal, then
  the step's statements, its log row and the state change. The runner's
  `$1` placeholders go as `?1`, and every value as a string, the type the
  API documents.
- The lock of step 2 is a lease: a row of `superschematic_lock` (service,
  holder, expiry), taken with one conditional write, renewed at each step,
  released at the end, and taken over once it expires. The write inserts
  the row or takes over an expired one, and its `changes` say whether it
  took the lease. A lease lasts 2 minutes past its last renewal. A second
  runner tries again every second, for up to 10 minutes. Every batch
  renews the lease; once another runner has taken it over, the renewal
  fails, and the batch with it. The expiry is D1's clock, so two runners'
  clocks never disagree.
- A step with `transactional: false` or `foreignKeysOff` is refused before
  the lock, and nothing runs: D1 can run neither. Plans written before the
  amendment may rebuild with `foreignKeysOff`; apply those to a SQLite
  file.
- A failed batch's error names the step's index and subject and D1's
  message, and the statement that failed when D1's answer names it.
- Until the real-D1 test has passed, the driver is unverified: Cloudflare
  documents a Worker's batch as a transaction, not a REST request's.

1. Read the plan and check its version, its `hash`, its `to` and, when
   it has one, its `expanded`.
2. Take the lock. On Postgres, one connection holds
   `pg_advisory_lock(key)` for the whole run, where `key` is the first
   eight bytes, big-endian, of the SHA-256 of
   `superschematic_migrate:<service>`; every step runs on that connection.
   A second runner waits. It waits by calling `pg_try_advisory_lock(key)`
   again every half second, not inside `pg_advisory_lock`: a session
   blocked there holds a snapshot, a `CREATE INDEX CONCURRENTLY` the first
   runner runs waits for that snapshot, and Postgres reports the two as a
   deadlock. On SQLite every step's transaction is `BEGIN IMMEDIATE`, and
   the checks of steps 3 and 4 are made again inside it. On D1 the runner
   holds the lease for the whole run, and a second runner waits for it.
3. Create the state tables if missing, and read the service's state row.
   No row means applied model `""` and no plan in progress.
4. Decide:
   - the row's `plan_hash` is this plan: resume it;
   - no plan is in progress and the row's `model_hash` is the plan's `to`
     (and not its `from`): the plan has been applied; say so and exit 0;
   - another plan is in progress with only its contract left, and it
     recorded its expanded model (`plan_phase` is `expanded`), and the
     row's `model_hash` is this plan's `from`: unless a contract step of
     that plan has a log row, this plan supersedes that contract. Say so,
     naming the old plan, clear `plan_hash` and `plan_phase`, and go on as
     for a plan with none in progress. The old plan's contract never runs;
     the new plan was planned from the schema the database holds, so
     whatever of it is still wanted is in the new plan. Once a contract
     step has started, the database no longer holds the expanded model:
     refuse, and say to finish the old plan;
   - another plan is in progress: refuse, naming it and its phase. A plan
     whose `plan_phase` is `expand` has no expanded model recorded, so no
     plan supersedes it;
   - the row's `model_hash` is not the plan's `from`: refuse, naming both,
     and say to plan again from the applied model
     (`superschematic-migrate status --model`). With no applied model,
     `status --model` has nothing to print: say instead to `adopt` the
     model the database matches if it was built from `create.sql` or by
     hand, and to plan from an empty database if it is empty;
   - the row's dialect is not the plan's: refuse.
5. `--phase contract` on a plan that is not the plan in progress, or on
   the plan in progress whose expand steps have not all finished: refuse.
   Only the plan in progress has a contract to run. A plan a newer plan
   superseded may start from the database's model again (when its expand
   had nothing to do, its expanded model is its `from`), and running its
   contract then would drop what the newer plan's servers use.
6. Record the plan as in progress (`plan_hash`) if it is not, and delete
   the log rows an earlier application of the same plan left: a plan is a
   pure function of its two models, so a database taken from A to B, back
   to A and to B again runs the same plan, with the same hash, twice.
7. Run each step of the requested phases in order, skipping a step whose
   log row has `finished_at`:
   - A transactional step: begin (on SQLite, `PRAGMA foreign_keys = OFF`
     first when `foreignKeysOff`, which only an older plan sets); on
     Postgres `SET LOCAL lock_timeout = '5s'`; run the statements; on
     SQLite with `foreignKeysOff`, run `PRAGMA foreign_key_check` and fail
     the step if it returns a row; write the log row with `started_at` and
     `finished_at`; commit; then `PRAGMA foreign_keys = ON`. The step and
     its log row commit together, so it runs once. A SQLite step otherwise
     runs with foreign keys on, as the connection has them, and the commit
     checks what the step's `PRAGMA defer_foreign_keys = ON` deferred.
   - A step that is not transactional (Postgres only): `SET lock_timeout =
     '5s'`; if its log row exists without `finished_at`, run `recovery`
     first; otherwise write the log row with `started_at`. Then run each
     statement on its own, `RESET lock_timeout`, and set `finished_at`.
     The recovery runs under the lock timeout too, since dropping an index
     waits for locks as building one does.
   - A lock timeout (Postgres SQLSTATE `55P03`, SQLite `SQLITE_BUSY`)
     rolls the step back and retries it after 1, 2, 4, 8 and 16 seconds,
     then fails. A SQLite connection waits 5 seconds for a lock
     (`busy_timeout`) before it reports `SQLITE_BUSY`.
   - Any other error stops the run. The plan stays in progress; the next
     `apply` of the same plan resumes at that step. The error names the
     step's index, subject and the statement that failed.
8. When the last expand step finishes and the plan has contract steps:
   when the plan has `expanded`, set `plan_phase` to `expanded`,
   `model_hash` to `expanded` and `model` to `expandedModel`, in the
   transaction of that step: the database holds that schema until the
   contract runs. A plan without `expanded` (written before it existed)
   sets `plan_phase` to `expand` and leaves `model_hash` and `model` as
   they were, as a runner that predates `expanded` does for every plan.
   When the plan's last step finishes, set `model_hash` to `to`, `model`
   to `toModel` and `dialect`, and clear `plan_hash` and `plan_phase`.

## `status`

```
superschematic-migrate status --service NAME [--model] [--database-url URL]
```

Prints the applied model's hash, the plan in progress and its phase, and
the steps logged for it. With `--model` it prints only the applied model's
canonical JSON, which `superschematic migrate plan --from` takes, and fails
when no model is applied. Between a plan's phases the applied model is the
plan's `expandedModel`, the schema the database holds. `status` only reads: it creates no table, and a
database the runner never touched has no state.

## `adopt`

```
superschematic-migrate adopt --model model.json [--database-url URL]
```

Records a model as applied without running anything, for a database built
from `create.sql` or changed by hand. The model's `service` and `dialect`
key the row, and its dialect must be the database URL's. It refuses while a
plan is in progress, and prints the hash it replaces.
`superschematic migrate plan --print-model` prints the model to adopt.

Exit codes: 0 done, 1 refused or failed, 2 usage.

## Jobs

```
superschematic-migrate job --job job.json|gs://<bucket>/<object>
```

Runs a job document: one phase of the plans of the DB services one
database server hosts, then the privileges of the servers that connect to
each (D46 in `docs/DECISIONS.md`). A deploy on a cloud target writes the
document and the plans beside it, and a job on the target runs it: on gcp,
a Cloud Run job, which reads both from the state bucket.

| Member | Type | Meaning |
| --- | --- | --- |
| `version` | integer | `1`. |
| `phase` | string | `expand`, `contract` or `all`: the phase of each plan to run. |
| `cloudSql` | object | Optional. `instance`, the Cloud SQL instance's connection name (`project:region:name`), and `user`, the IAM database user to connect as: a service account's email without `.gserviceaccount.com`. |
| `databases` | array | The DB services, in order. |

A database:

| Member | Type | Meaning |
| --- | --- | --- |
| `service` | string | The DB service. |
| `database` | string | With `cloudSql`: the database's name on the instance. |
| `databaseUrl` | string | Without `cloudSql`: the database's URL, as `--database-url` takes it. |
| `plan` | string | Optional. The plan whose phase to run: a path or a `gs://` URL, read relative to the job document's. |
| `privileges` | object | Optional. `readWrite`, the roles that read and write the DB service's tables. |

With `cloudSql`, the runner reaches each database through the Cloud SQL Go
connector, which dials the instance by its connection name over TLS with
an ephemeral certificate and logs in with IAM database authentication as
the account the job runs as, so there is no password. A `gs://` document
is read with application default credentials, or from
`$STORAGE_EMULATOR_HOST` when it is set, as Google's client libraries do.

After the plan's phase, `privileges` gives each `readWrite` role USAGE on
the schemas that hold the database's objects, where the job's user may
give it, SELECT, INSERT, UPDATE and DELETE on its tables, SELECT on its
views, and USAGE and SELECT on its sequences: the objects the user owns,
or a role it inherits owns, outside the system schemas and the runner's
state tables, which no extension owns. Then it takes every privilege on
those objects, and USAGE on those schemas, back from any other role the
user gave them to, all in one transaction, so the roles listed are the
roles that hold them. A role the user is granted, such as
`cloudsqlsuperuser` on Cloud SQL, keeps what it holds.
An empty `readWrite` takes every such grant back. Only the Postgres
driver gives privileges.

Exit codes: 0 done, 1 refused or failed, 2 usage. A job that fails writes
its error to stderr on a line that begins `superschematic-migrate: `,
which a gcp deploy reads from the execution's logs and reports; a failed
step's statement follows on the lines after it.

## Layout

| Path | What it holds |
| --- | --- |
| `go/` | The module. Package `migrate`: the plan and model read types, canonical JSON and the hashes, and `Runner`, which applies a plan through a `Driver` |
| `go/postgres/` | The Postgres driver, over one pgx connection |
| `go/sqlite/` | The SQLite driver, over `modernc.org/sqlite`, which is pure Go |
| `go/d1/` | The D1 driver, over Cloudflare's REST API |
| `go/cloudsql/` | The Postgres driver's dialer of a Cloud SQL instance, through the Cloud SQL Go connector |
| `go/cmd/superschematic-migrate/` | The binary |
| `go/internal/testdb/` | A database of its own for each test |
| `go/internal/d1fake/` | A fake D1 REST API over a SQLite file, for the tests |
| `go/testdata/` | Hand-written plans per dialect, and the canonical JSON vectors |
| `testdata/plans/<case>/` | The plans the compiler writes for the runner, `NN-<name>.plan.json` |

Nothing in the module needs cgo, so the binary builds with
`CGO_ENABLED=0` for any platform Go targets.

## Use

Each release attaches the binary for linux and darwin on x64 and arm64:
`superschematic-migrate_<version>_<platform>.tar.gz`, where `<platform>`
is `linux-x64`, `linux-arm64`, `darwin-x64` or `darwin-arm64`. The archive
holds the binary, this file, the license and `BUILD_COMMIT`, the commit it
was built from. The release's `SHA256SUMS` lists every archive, and each
archive has a build provenance attestation. Download one, check both, and
install it:

```sh
version=0.1.0-alpha.1
asset="superschematic-migrate_${version}_linux-x64.tar.gz"
gh release download "v$version" --repo parable-work/superschematic \
  --pattern "$asset" --pattern SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS   # macOS: shasum -a 256 --check --ignore-missing SHA256SUMS
gh attestation verify "$asset" --repo parable-work/superschematic
tar -xzf "$asset"
install "superschematic-migrate_${version}_linux-x64/superschematic-migrate" /usr/local/bin/
superschematic-migrate version
```

`superschematic-migrate version` prints the version the release stamped.
A binary built without that stamp prints the version Go recorded for the
module, or `(devel)`.

Or build the binary from a checkout:

```sh
cd runtime/migrate/go
CGO_ENABLED=0 go build -trimpath -o superschematic-migrate ./cmd/superschematic-migrate
```

Apply a plan to a local Postgres container, then read the state:

```sh
export DATABASE_URL='postgres://postgres:secret@localhost:5432/shop?sslmode=disable'
superschematic-migrate apply --plan shop.plan.json
superschematic-migrate status --service shop
```

A deploy that rolls servers out runs the two phases around the rollout. A
run that stops part-way, or a second runner that starts during the first,
is safe: the next `apply` of the same plan resumes where the last one
stopped.

```sh
superschematic-migrate apply --plan shop.plan.json --phase expand
# roll out the new servers
superschematic-migrate apply --plan shop.plan.json --phase contract
```

A rollout that fails runs no contract step, and the previous servers keep
running on the expanded schema, which the runner records as the applied
model. The next deploy plans from that model, and its plan supersedes the
pending contract: the drops it still wants are in its own contract, and a
plan back to the previous version starts from that model too.

```sh
superschematic-migrate status --service shop --model > shop.model.json
superschematic migrate plan schemas/services/shop-db --from shop.model.json --out next.plan.json
superschematic-migrate apply --plan next.plan.json --phase expand
```

SQLite takes a path, a `sqlite:` URL (`sqlite:///var/lib/shop/shop.db` is
`/var/lib/shop/shop.db`) or a `file:` URI with SQLite's URI parameters:

```sh
superschematic-migrate apply --plan shop.plan.json --database-url /var/lib/shop/shop.db
```

D1 takes `d1://<account id>/<database id>` and a Cloudflare API token
that may edit the database, in `CLOUDFLARE_API_TOKEN`. As a step of a
GitHub Actions job:

```yaml
- name: Migrate the shop database
  env:
    CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}
    DATABASE_URL: d1://${{ vars.CLOUDFLARE_ACCOUNT_ID }}/${{ vars.SHOP_D1_DATABASE_ID }}
  run: superschematic-migrate apply --plan shop.plan.json
```

The D1 driver is unverified until the real-D1 test (Tests, below) has
passed against a D1 database.

A release publishes no image of the runner: Cloud Run cannot pull from
GitHub's registry directly, and the stack model (D30) builds the migration
job's image itself. As a Cloud Run job, an image that holds the binary and
the plan:

```dockerfile
FROM golang:1.26.4 AS build
WORKDIR /src
COPY runtime/migrate/go/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /superschematic-migrate ./cmd/superschematic-migrate

FROM gcr.io/distroless/static
COPY --from=build /superschematic-migrate /superschematic-migrate
COPY shop.plan.json /shop.plan.json
ENTRYPOINT ["/superschematic-migrate", "apply", "--plan", "/shop.plan.json"]
```

```sh
gcloud run jobs create shop-migrate --image "$IMAGE" \
  --set-secrets DATABASE_URL=shop-database-url:latest --task-timeout 30m
gcloud run jobs execute shop-migrate --wait
```

A retried task resumes the plan. The binary stops on `SIGTERM` and
`SIGINT`; a step that was running rolls back, or, outside a transaction,
runs its recovery on the next apply.

From Go, the same run is:

```go
plan, err := migrate.ReadPlan(doc)
// ...
driver, err := postgres.Open(ctx, databaseURL, postgres.Options{})
// ...
defer driver.Close(ctx)
result, err := (&migrate.Runner{Driver: driver, Log: os.Stdout}).Apply(ctx, plan, migrate.All)
```

On D1, the driver is:

```go
driver, err := d1.Open(ctx, databaseURL, d1.Options{Token: os.Getenv("CLOUDFLARE_API_TOKEN")})
```

## Tests

`go test ./...` in `go/` runs every test against SQLite in temporary
files, and against D1 through a fake of its REST API
(`go/internal/d1fake`) that runs each request in one SQLite transaction
with foreign keys on. With `SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL` set
to a Postgres whose role may create databases, the tests run against
Postgres too; each creates and drops a database of its own.
`TestCompilerVectors` applies every case under `testdata/plans` and skips
while there is none; on D1 it skips a case with a step that turns foreign
keys off.

`TestRealD1` applies a chain with a rebuild of a referenced table to a
real D1 database, and checks the referencing rows survive. It runs only
with `SUPERSCHEMATIC_MIGRATE_TEST_D1_URL` set to the database's `d1://`
URL and `CLOUDFLARE_API_TOKEN` to a token that may edit it. The database
is scratch: the test drops the tables `customer`, `order`, `audit` and the
runner's before it starts.

```sh
SUPERSCHEMATIC_MIGRATE_TEST_D1_URL=d1://<account id>/<database id> CLOUDFLARE_API_TOKEN=... \
  go test -run TestRealD1 -v .
```
`go test -run TestFixturesAreSealed -seal` rewrites the `from`, `to`,
`expanded` and `hash` of the hand-written plans after an edit.
