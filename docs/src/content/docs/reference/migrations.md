---
title: Schema migrations
description: Plan the change of a DB service's database between two versions of its schema with migrate plan; the two phases, the hazard classes, readers and renames, versioned tables and version graphs, the superschematic-migrate runner and its state, and the limits.
sidebar:
  order: 9
---

`create.sql` builds a DB service's database from nothing. A database that
already holds data changes through a migration plan: the ordered steps that
take it from one version of the schema to the next. `superschematic migrate
plan` writes the plan from two versions of the schema and no database, so a
CI job can show it on a pull request and stop on what the pull request has
not acknowledged. `superschematic-migrate` applies it.

A plan is JSON. Each step has an index, a phase, an operation, the object it
changes, its SQL, whether it runs in a transaction, and its hazards. The
compiler computes the steps and the hazards from the two versions; an author
never writes either.

The design and the alternatives not taken are D27 in
[docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md).

## The two versions

The new version is the service directory the command is given. The
previous version is one of:

| Flag | The previous version |
| --- | --- |
| `--from <service-dir>` | Another checkout of the service directory. |
| `--from <model.json>` | The model a database recorded, as `superschematic-migrate status --model` prints it. |
| `--from-ref <git-ref>` | The service as it is at a git ref. |
| neither | An empty database. The plan creates everything `create.sql` creates. |

A model is the relational schema `sqlgen` resolves before it renders
`create.sql`: tables with their columns, keys, constraints and indexes,
plus the history tables, functions, triggers and projection views the
decorators add. Each column records the schema field it stores
(`Order.total`). Both versions resolve to models in memory, the same way,
with the options a build passes `sqlgen`, and the plan is the difference
between them.

Each version loads with its dependencies resolved from its own schemas
root, as [`build --with-deps`](/superschematic/reference/cli/#build-service-dir)
resolves them, and with its own `superschematic.toml` when it has one
(else the file `--naming` names, else the defaults).

`--from-ref` reads the whole schemas root at the ref with `git archive`
into a directory beside the checkout's schemas root, loads the service from
it, and removes it. Beside the checkout, the paths a `tsconfig.json` reaches
outside the schemas root (the authoring packages, `node_modules`) resolve
as they do from the checkout. The service is found by name. When the ref
has no such service, or no schemas root, the plan starts from an empty
database and says so on stderr. The ref must be in the local repository: a
CI checkout fetches it first.

Both versions are resolved by the compiler that runs the plan. A compiler
upgrade that changes the DDL of an unchanged schema is therefore not in a
plan from `--from-ref` or a checkout. The runner notices: it refuses a plan
whose starting model is not the one the database recorded
([the baseline check](#the-baseline-check)). A plan `--from` the recorded
model includes the compiler's change.

## `migrate plan`

```
superschematic migrate plan <service-dir> [flags]
```

The service must be a DB service. The plan goes to stdout in `--format`,
and to `--out` as JSON.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--from` | none | the previous version: another checkout of the service directory, or a model JSON file |
| `--from-ref` | none | the previous version: the schemas root at this git ref; cannot be combined with `--from` |
| `--rename` | none | `old=new` for a table, `oldTable.oldColumn=newTable.newColumn` for a column ([Renames](#renames)); repeatable |
| `--reader` | none | an API or General service directory outside the schemas root whose `@source` views read the database ([Readers](#readers)); repeatable |
| `--dialect` | `postgres` | the database dialect: `postgres`, or `sqlite` once that dialect lands |
| `--out` | none | write the plan JSON to this file, in canonical form |
| `--format` | `sql` | print the plan to stdout as `json`, `sql` or `markdown` |
| `--fail-on` | none | hazard classes, comma-separated, or `all`: exit non-zero when the plan has a hazard of one of them that no `--allow` names |
| `--allow` | none | a hazard id `--fail-on` lets pass; repeatable |
| `--print-model` | false | print the new version's model as canonical JSON and plan nothing |
| `--naming` | `<service-dir>/../../superschematic.toml` | naming config file of the new version |

`--format sql` prints one script for review: a header with the service, the
dialect, the two model hashes and the renames, then each step's statements
under a comment naming its index, phase, operation, subject and hazard ids.
`--format markdown` prints a summary line, a table of the hazards and the
steps of each phase with their SQL, for a pull request comment. `--format
json` prints what `--out` writes.

With `--fail-on`, the command prints the plan first, then lists each
hazard that stops it on stderr, with its id, its reason and the `--allow`
that lets it pass, and exits 1.

### Gate a pull request

A CI job plans the pull request's schema against the base branch, posts the
Markdown, keeps the plan JSON for the deploy, and fails on what nobody has
acknowledged:

```sh
git fetch origin main
superschematic migrate plan schemas/services/shop-db \
  --from-ref origin/main \
  --format markdown \
  --fail-on destructive,compat \
  --out plan.json > plan.md
```

The plan is a pure function of the two versions, the renames and the
readers, so the deploy can plan again from the same inputs and check that
the plan's hash is the one the pull request showed. An acknowledgment is a
hazard id passed to `--allow`; where a pipeline keeps them is up to the
pipeline. A hazard's id stays the same across plans of the same change, so
an acknowledgment survives a rebase.

### Rename a column

A rename reads as a drop and an add until `--rename` says otherwise.
Renaming `Order.total` to `Order.amount` in the schema and planning:

```sh
superschematic migrate plan schemas/services/shop-db --from-ref origin/main --fail-on destructive
```

fails on `destructive:table/order/column/total`: the plan adds `amount` and
drops `total` with its data. The drop's reason says that `amount` has the
same type, nullability and default, so the two may be one rename. Saying
so makes it one:

```sh
superschematic migrate plan schemas/services/shop-db --from-ref origin/main --fail-on destructive \
  --rename order.total=order.amount
```

Now the plan renames the column in place, and the destructive hazard is
gone. The rename is `compat`: a server built from the previous version
still names `total`. The [Renames](#renames) section says what a rename
carries with it.

### Adopt a database built from create.sql

A database built from `create.sql`, or changed by hand, has no recorded
model. Record the one it matches, then plan from it:

```sh
superschematic migrate plan schemas/services/shop-db --print-model > model.json
superschematic-migrate adopt --model model.json
```

Later plans can start `--from` what the database recorded:

```sh
superschematic-migrate status --service shop-db --model > applied.json
superschematic migrate plan schemas/services/shop-db --from applied.json --out plan.json
```

## Phases

Every step is in one of two phases.

| Phase | Runs | Holds |
| --- | --- | --- |
| `expand` | before the new servers roll out | everything that keeps the previous version's servers working: renames, creates and adds, alterations, indexes, constraints, functions, triggers, views and comments |
| `contract` | after the rollout | what removes something the previous version's servers use or tightens what they write: drops, `SET NOT NULL`, dropped defaults, foreign keys over columns the previous version already has |

A column dropped in `contract` that is `NOT NULL` first loses the
constraint in `expand`, so the new servers can insert without it. A step
that no order keeps both versions' servers working with, such as a rename,
a retype, a required column without a default or a unique constraint on an
existing table, stays in `expand` and carries `compat`. A deploy without a
rollout runs both phases back to back.

Steps run in this order: renames; creates and adds in dependency order;
alterations, each wrapped by the drop and re-creation of the views that
read the altered columns; indexes, constraints, functions, triggers, views
and comments; then the contract steps, tightenings before drops, drops in
reverse dependency order. Extensions are created and never dropped, as
`drop.sql` leaves them.

A step on a table the previous version already has uses the online form
where Postgres has one. A table the plan creates gets the plain forms.

| Change | Online form |
| --- | --- |
| an index | `CREATE INDEX CONCURRENTLY`, outside a transaction |
| a foreign key | added `NOT VALID`, then validated |
| `SET NOT NULL` | a `CHECK (col IS NOT NULL) NOT VALID`, validated first, so Postgres skips the scan |
| a unique constraint | added `USING INDEX` over an index built concurrently |

A step outside a transaction carries recovery statements, such as dropping
the invalid index a failed concurrent build leaves, which the runner runs
before it tries the step again.

## Hazards

Each step lists the hazard classes it falls in.

| Class | The step | For example |
| --- | --- | --- |
| `destructive` | deletes data the new version cannot recover | dropping a table, a column or a history table; a narrowing cast that truncates |
| `blocking` | holds a lock that blocks writes, or reads, for time that grows with the table | a type change that rewrites the table, a column added with a volatile default or as a stored generated column, an index built without `CONCURRENTLY`, seeding a history table |
| `compat` | breaks a server built from the previous version, which may still be running | a rename, a retype, a required column without a default, a new unique constraint over columns it writes |
| `data-dependent` | fails at apply when existing rows violate it | `SET NOT NULL`, a unique constraint, validating a foreign key, a narrowing cast, a required column without a default on a table with rows |
| `copy-table` | rebuilds the table by copying it | SQLite only: a change its `ALTER TABLE` cannot make |
| `api-breaking` | drops, renames or retypes a column a deployed reader reads, or changes the columns a projection view publishes | [Readers](#readers) |
| `history` | changes the shape of rows a history table keeps | retyping a column of a versioned table; changing a version graph member's content columns |

A hazard's id is `<class>:<subject>`, where the subject is the object the
step changes as a path: `table/order`, `table/order/column/total`,
`table/order/index/order_total_idx`, `table/order/constraint/<name>`,
`function/<name>`, `trigger/<table>/<name>`, `view/<pool>.<name>`,
`schema/<pool>` or `extension/<name>`. An `api-breaking` id also names the
reader: `api-breaking:table/order/column/total@shop-api/OrderView.total`.

`compat` is judged against the server generated from the previous version.
The Go ORM names every column of its table in its `SELECT` and `RETURNING`
lists, so it fails on any dropped or renamed column of a table it reads,
whether an API exposes the column or not.

## Readers

A reader is an API or General service whose
[`@source`](/superschematic/guides/api-routes/#responses-and-views) view reads one of the DB
service's tables. It reads the column behind each of the view's fields: a
relation field reads its foreign-key column, and a `@virtual` field reads
none. A view of a type that is not a table, such as a base class, reads
nothing.

| Readers | Come from | Checked against |
| --- | --- | --- |
| before the rollout | the API and General services in the previous version's schemas root, resolved against the previous model | `expand` steps |
| after the rollout | those in the new version's schemas root, resolved against the new model | `contract` steps |
| `--reader <service-dir>` | a service that lives elsewhere and is deployed at a version of its own, resolved against both models | both phases |

A step that drops, renames or retypes a column a reader live at that phase
reads is `api-breaking` for that reader. So dropping a column the new API
stopped reading passes when both are in the new schemas root, and fails
while a `--reader` still reads it. A `--from` model has no services beside
it, so its readers before the rollout are the `--reader`s only.

A [projection view](/superschematic/reference/projections/) whose published
columns change, by name, type or order, is `api-breaking` for its readers:
its Arrow schema is their contract.

## Renames

A table or column that one version has and the other does not is a drop
and an add. When a table loses one column and gains one with the same type,
nullability and default, or the schema loses a table and gains one with the
same columns, the drop's hazard says it may be a rename. The plan never
renames on shape alone.

`--rename` makes it a rename. Names are the database's: `purchase=order`
for a table, `order.total=order.amount` for a column. A column of a renamed
table names the table in each version: `purchase.total=order.amount`. The
old name must be in the previous version and not the new one, and the new
name in the new version and not the previous one; otherwise the plan fails
and names the flag.

A rename carries what is named after it: a column's foreign keys, indexes
and unique constraints, a table's join tables and history objects, and the
`_id` columns `@hasMany` adds to other tables. Each is a rename step, never
a drop and a create. A rename is `compat`. The plan records the renames it
was given.

Intent the plan does not model, such as a cast with a custom expression, a
backfill or a column split, goes in a migration written by hand that the
deploy orders around the plan, or the operator applies it and adopts the
result.

## Versioned tables and version graphs

| Change | Plan |
| --- | --- |
| a table becomes [`@versioned`](/superschematic/reference/versioned-tables/) | `_version BIGINT NOT NULL DEFAULT 1`, which Postgres adds without a rewrite, the history table and its indexes, the functions and triggers, and, in the same transaction as the triggers, one `INSERT` image per existing row at version 1 (`blocking`), so every live row has an image at its version |
| a table stops being versioned | in `contract`: the triggers and functions, then the history table (`destructive`) and `_version` |
| `exclude`, `retentionDays` or `pruneKeepReferencedBy` changes | the functions are replaced in `expand`; images recorded before an `exclude` change keep the newly excluded columns, which the plan reports as `history` and does not scrub |
| a column of a versioned table changes | planned as on any table; images recorded before it keep the old shape, which a retype makes unreadable as the new type (`history`) |
| `partitionBy` of an existing history table changes | the plan fails and names it: change the database by hand and adopt the new version |

A [version graph](/superschematic/reference/version-graphs/)'s tables are
ordinary tables after the loader adds them, and migrate as any other. A
change to a member's content columns is `history`: commits made before it
hash and merge rows of the old shape. The hazard says whether the graph's
`schemaEpoch` rose.

## The runner

`superschematic-migrate` applies a plan. It is the Go module
`github.com/parable-work/superschematic/runtime/migrate/go`, which holds the
database drivers, so a migration job needs the plan and that binary, not the
compiler. It never computes a plan. It needs no cgo, so one static binary
serves a container job:

```
CGO_ENABLED=0 go install github.com/parable-work/superschematic/runtime/migrate/go/cmd/superschematic-migrate@latest
```

`runtime/migrate/README.md` has a Dockerfile for a Cloud Run job.

```
superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]
superschematic-migrate status --service NAME [--model] [--database-url URL]
superschematic-migrate adopt --model model.json [--database-url URL]
```

`--database-url` defaults to `$DATABASE_URL`. A `postgres://` or
`postgresql://` URL selects Postgres; a `sqlite:` URL, a `file:` URI or a
path selects SQLite. It must match the plan's dialect. Exit codes: 0 done, 1
refused or failed, 2 usage.

### State

The runner keeps two tables in the connection's current schema, and creates
them when they are missing:

| Table | Holds |
| --- | --- |
| `superschematic_schema_state` | a row per service: the dialect, the applied model's hash and the model itself as canonical JSON, and the plan in progress with its finished phase |
| `superschematic_migrations` | a row per step run: the plan's hash, the step's index, phase and subject, the SHA-256 of its SQL, and when it started and finished |

The names are fixed.

### `apply`

`apply` checks the plan's version, its hash and its `to` hash before it
runs anything, so an edited plan is never half-applied. It then takes a
lock: on Postgres a session-level advisory lock keyed by the service, held
for the whole run; on SQLite `BEGIN IMMEDIATE` per step. A second runner
waits.

Each step runs in order. A step in a transaction commits with its log row,
so it runs once. A step outside one logs its start, runs each statement on
its own, and logs its end. Each step sets `lock_timeout` to 5 seconds, and a
lock timeout rolls the step back and retries it after 1, 2, 4, 8 and 16
seconds. Any other error stops the run with the step's index, subject and
failing statement. The plan stays in progress, and the next `apply` of the
same plan resumes at that step, running a non-transactional step's recovery
first. Running a finished plan again does nothing.

`--phase expand` runs the expand steps before a rollout and `--phase
contract` the rest after it; `--phase contract` is refused until expand has
finished. When the plan's last step finishes, the plan's model becomes the
applied model.

### The baseline check

The runner refuses a plan whose `from` is not the database's applied model,
naming both, unless the database is part-way through that same plan. Plan
again from what the database recorded: `superschematic-migrate status
--model` prints it, and `migrate plan --from` takes it. A new plan is also
refused while another plan is in progress, so contract steps are never
skipped.

### `status` and `adopt`

`status` prints the applied model's hash, the plan in progress and its
phase, and the steps logged for it. With `--model` it prints only the
applied model's canonical JSON.

`adopt` records a model as applied without running anything, for a database
built from `create.sql` or changed by hand. The model's `service` and
`dialect` key the row. It refuses while a plan is in progress, and prints
the hash it replaces. `migrate plan --print-model` prints the model to
adopt.

## Limits

- There are no down plans. Rolling back is a plan from the current version
  to the previous one, with its own hazards. Because `expand` keeps the
  previous version's servers working, a server rollback needs no schema
  rollback.
- A custom cast, a backfill or a column split is written by hand, ordered
  around the plan or applied and then adopted.
- A change the plan cannot express fails the plan and names it.
- Postgres is the only dialect so far. SQLite, with its copy-table rebuild
  and its runner driver, comes later through `--dialect sqlite`.
- The engine's own storage is the engine's: it creates its tables and runs
  its behaviors' migrations, and no plan covers them.
