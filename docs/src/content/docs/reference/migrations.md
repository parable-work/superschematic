---
title: Schema migrations
description: Plan the change of a DB service's database between two versions of its schema with migrate plan; the two phases and the model between them, the hazard classes, readers and renames, versioned tables and version graphs, SQLite and its copy-table rebuild, the superschematic-migrate runner and its state, and the limits.
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
| `--dialect` | `postgres` | the database dialect: `postgres`, or `sqlite` for a service whose `outputs.sql.dialects` lists it ([SQLite](#sqlite)) |
| `--out` | none | write the plan JSON to this file, in canonical form |
| `--format` | `sql` | print the plan to stdout as `json`, `sql` or `markdown` |
| `--fail-on` | none | hazard classes, comma-separated, or `all`: exit non-zero when the plan has a hazard of one of them that no `--allow` names |
| `--allow` | none | a hazard id `--fail-on` lets pass; repeatable |
| `--print-model` | false | print the new version's model as canonical JSON and plan nothing |
| `--naming` | `<service-dir>/../../superschematic.toml` | naming config file of the new version |

`--format sql` prints one script for review: a header with the service, the
dialect, the two model hashes, the hash of the model between the phases
when the plan has one ([Between the phases](#between-the-phases)) and the
renames, then each step's statements under a comment naming its index,
phase, operation, subject and hazard ids.
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
SQLite has no online forms; [SQLite](#sqlite) says what its steps do.

| Change | Online form |
| --- | --- |
| an index | `CREATE INDEX CONCURRENTLY`, outside a transaction |
| a foreign key | added `NOT VALID`, then validated |
| `SET NOT NULL` | a `CHECK (col IS NOT NULL) NOT VALID`, validated first, so Postgres skips the scan |
| a unique constraint | added `USING INDEX` over an index built concurrently |

A step outside a transaction carries recovery statements, such as dropping
the invalid index a failed concurrent build leaves, which the runner runs
before it tries the step again.

### Between the phases

A plan with contract steps carries the model the database holds between
its phases: `expandedModel`, and its hash, `expanded`. It is the previous
model with every expand step applied: the new version's tables, columns,
indexes, functions, triggers and views, with what `contract` drops still
there and what it tightens still loose. A column `contract` drops keeps its
`NOT NULL` when it has a default, as `expand` leaves it. The extensions and
pool schemas of both versions are there, since a plan drops neither. A
version graph keeps the previous version's schema epoch, and a member's
content is the new version's for every column the new table has and the
previous version's for the columns `contract` drops. A plan without
contract steps carries neither member, since its expand steps end at `to`.

The runner records that model when the expand steps finish, so a rollout
that fails leaves the database at a model a new plan can start from
([A failed rollout](#a-failed-rollout)). A plan from it to the new version
has no expand steps and the plan's contract steps.

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
`schemaEpoch` rose. A column can join or leave the content while the
column stays, as `@conflictUnit('excluded')` added or taken off it does.
That changes no table, so it gets a step of its own in `expand`,
`changeGraphContent`, with no statements: the step carries the hazard,
which names the columns, and the runner logs it like any other.

## SQLite

A DB service is built for Postgres, and for SQLite too when its
`outputs.sql.dialects` lists `sqlite`:

```ts
export default defineConfig({
  name: "shop-db",
  kind: SchemaKind.DB,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
```

`dialects` is `["postgres"]` when unset. The list must hold `postgres`,
since the Go ORM the kind always generates runs on Postgres. A list without
it, with a dialect other than `postgres` and `sqlite`, or with a dialect
twice fails the build and says why.

With `sqlite` listed, the build also writes
`<out>/sql/<service>/sqlite/create.sql`: the SQLite plan from an empty
database, its steps' statements as one script. `migrate plan --dialect
sqlite` plans the service's SQLite database, and refuses a service that
does not list `sqlite`. It refuses a previous version, a service
directory (`--from <service-dir>`) or a git ref (`--from-ref`), that
does not list `sqlite` either: no build of it wrote a SQLite database.
Plan from the model the database recorded (`--from <model.json>`), which
is checked by its own `dialect`, or from an empty database. The Postgres
DDL is the same whether `sqlite` is listed or not.

### Types and defaults

SQLite stores each type as the type its values need:

| Schema type (Postgres) | SQLite |
| --- | --- |
| `UUID`, `TEXT`, `VARCHAR(n)`, `CHAR(n)`, `DATE`, `TIME`, `TIMESTAMP`, `TIMESTAMPTZ`, `INTERVAL`, `INET` | `TEXT` |
| `CITEXT` | `TEXT COLLATE NOCASE` |
| `SMALLINT`, `INTEGER`, `BIGINT`, `BOOLEAN` | `INTEGER` |
| `REAL`, `DOUBLE PRECISION` | `REAL` |
| `NUMERIC(p, s)` | `NUMERIC`, SQLite's numeric affinity |
| `JSONB`, `JSON`, and every list (`T[]`) | `TEXT` that holds JSON |
| `BYTEA` | `BLOB` |

A list's JSON array holds each element as JSON holds its type: text,
dates, times and UUIDs as strings, numbers as numbers, a boolean as `true`
or `false`, and a JSON value as itself. A list of lists is `JSONB` on
Postgres, so it holds one JSON value here too.

A default writes the form the schema runtime reads:

| Postgres default | SQLite default |
| --- | --- |
| `gen_random_uuid()` | a version 4 UUID string, from `randomblob` |
| `CURRENT_TIMESTAMP` | `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`: an RFC 3339 UTC instant |
| `CURRENT_DATE` | `strftime('%Y-%m-%d', 'now')` |
| `CURRENT_TIME` | `strftime('%H:%M:%f', 'now')` |
| `'{}'` on a list | `'[]'` |
| a JSON platform default (`'{...}'::jsonb`) | its text, `'{...}'` |
| a number or a string | the same |

The primary key is part of `CREATE TABLE`, and so is each foreign key. A
unique field is a unique index under the name Postgres gives its
constraint (`customer_email_key`), so adding or dropping one is a
statement of its own, not a rebuild. SQLite names neither a primary key
nor a foreign key, so renaming one is no step. SQLite keeps no comments.

### Steps

Every SQLite step runs in a transaction, which the runner opens with
`BEGIN IMMEDIATE`. SQLite changes a table in place only where its
`ALTER TABLE` can:

| Change | Step |
| --- | --- |
| a table or a column renamed | `ALTER TABLE ... RENAME TO`, `RENAME COLUMN` |
| a column added | `ADD COLUMN`, unless the column is `NOT NULL` without a constant default, has a default that is not a constant (the UUID and time defaults above), or has a foreign key and is `NOT NULL` or has a default |
| a column dropped | `DROP COLUMN` (`blocking`: SQLite rewrites the table). Its indexes are dropped before it, and a foreign key over it makes its table's rebuild drop it instead |
| an index or a unique field added or dropped | `CREATE INDEX`, `CREATE UNIQUE INDEX`, `DROP INDEX`; building an index on a table the previous version has is `blocking` |
| an index or a unique field renamed | the index dropped and built again under its new name (`blocking`): SQLite cannot rename an index |
| a table dropped | `DROP TABLE`, with foreign keys off, so no `ON DELETE` action runs. Tables that reference each other are dropped in one step, since SQLite cannot drop the foreign key that closes the cycle without rebuilding a table the plan drops |

Every other change to a table rebuilds it: a type, a list's element, a
nullability, a default, a foreign key added over a column the table has,
changed or dropped, and a column `ADD COLUMN` cannot add. The rebuild is
SQLite's copy-table procedure, in one step:

1. create the table as the phase leaves it under a temporary name
   (`_new_order`), with its primary key and foreign keys;
2. copy the rows, mapping each column to its name after the renames, filling
   the columns the table gains from their defaults, casting a column
   whose type changes, and converting each element of a list whose
   element changes ([A list's element](#a-lists-element));
3. drop the old table;
4. rename the new one;
5. create its unique indexes and indexes again.

Every change the phase makes to a table shares one rebuild: a table is
rebuilt at most once in `expand` and once in `contract`, and the rebuild
also adds the columns and indexes the phase adds to it. Renames of the
table and its columns run before it, in place. The rebuild is `copy-table`
and `blocking`, and carries the hazards of every change it makes.

The step runs with foreign keys off (`foreignKeysOff` in the plan). With
them on, dropping the old table would delete its rows first and run the
`ON DELETE` actions of the tables that reference it: a `CASCADE` would
delete their rows and a `RESTRICT` would fail. SQLite turns them off only
outside a transaction, so the runner does that around the step, runs
`PRAGMA foreign_key_check` before the commit, and fails the step on any
violation. The tables that reference the rebuilt one name it, and the name
resolves again once the new table takes it. A table renamed with `RENAME
TO` keeps the references to it too: SQLite rewrites the foreign keys of
the tables that reference it.

SQLite's `CAST` never fails: text that is not a number becomes `0`, and a
fraction is cut toward zero as an `INTEGER`. So a type change that cannot
keep every value is `destructive`, not `data-dependent`. A `NOT NULL` a
rebuild adds, a required column without a default, and a unique index
fail on the rows that break them, as on Postgres (`data-dependent`).

### A list's element

A list is `TEXT` whatever its element, so the model also records what each
element holds (`"element"`): `TEXT`, `INTEGER`, `REAL` or `NUMERIC` as
SQLite stores the element's type, `BOOLEAN`, `JSON` for a JSON value, or
`BLOB` for bytes. A change of it rebuilds the table, and the copy converts
each element of the array, in order. For a list of text that becomes a
list of integers, the plan writes this on one line:

```sql
CASE WHEN "product"."items" IS NOT NULL THEN (
  SELECT json_group_array(CAST("_element"."value" AS INTEGER) ORDER BY "_element"."key")
  FROM json_each("product"."items") AS "_element"
) END
```

A `NULL` list stays `NULL`, and an empty list stays `[]`.

| Element change | Each element | Hazard |
| --- | --- | --- |
| text to `INTEGER`, `REAL` or `NUMERIC` | cast: text that is not a number becomes `0`, and a fraction is cut toward zero as an `INTEGER` | `destructive` |
| a number to text | cast to its text | |
| `INTEGER` or `NUMERIC` to `REAL` | cast: an integer past 2^53 is rounded | `destructive` |
| `REAL` or `NUMERIC` to `INTEGER` | cast: a fraction is cut toward zero | `destructive` |
| `INTEGER` or `REAL` to `NUMERIC` | cast | |
| a boolean to a number | `1` or `0` | |
| a boolean to text | the text `true` or `false` | |
| a number or text to a boolean | `true` where SQLite reads a number other than `0`, else `false`, so text such as `'true'` becomes `false` | `destructive` |
| between types SQLite stores alike: `UUID`, `TEXT`, `VARCHAR(n)`, dates and times; `SMALLINT`, `INTEGER` and `BIGINT` | nothing: no step, as for the same change of a column | |
| to or from a JSON value or bytes | the plan fails, naming the column | |

Each rebuild is also `compat`, `copy-table` and `blocking`, as any type
change on SQLite is. Postgres plans the same change as `ALTER COLUMN ...
TYPE T[] USING col::T[]`, which fails on text that is not a number
(`data-dependent`). SQLite's `CAST` never fails, so where a cast cannot
keep every element the change is `destructive` instead, and the hazard
says what it loses. An element change to or from a JSON value or bytes
fails the plan: SQLite's `CAST` keeps a nested JSON value as JSON, where
Postgres's cast to text gives its text, and SQLite's JSON holds no bytes.
Change such a list by hand and adopt the new model.

A list of lists is a JSON value on both dialects, `JSONB` on Postgres, so
a change of its inner element is no step on either.

### Refusals

SQLite has no form of these, so a service that lists `sqlite` and uses one
fails its build, and `migrate plan --dialect sqlite` fails, each naming
the feature and the dialect:

- `@versioned` and `@optimistic`, whose triggers SQLite's adapter does not
  read yet;
- `@searchField`, a stored generated column with a trigram index;
- projections;
- `GIN` and `GIST` indexes: an `@index` over a list, a JSON or an `LTREE`
  column;
- the types SQLite has no storage for: `LTREE` and the PostGIS types
  (`POINT`, `GEOGRAPHY`, `GEOMETRY`).

### What SQLite does not keep

- `VARCHAR(n)` and `NUMERIC(p, s)` are `TEXT` and `NUMERIC`: SQLite does
  not enforce the length, the precision or the scale, so a change of
  them is no step.
- A list, a JSON value and text are all `TEXT`. The model records what a
  column holds as JSON (`"holds": "list"` or `"json"`), so a field that
  changes between them is not lost. A JSON value that becomes text keeps
  its JSON text, as Postgres's cast keeps it, through a rebuild that casts
  nothing; one that becomes another scalar casts as text does. Every other
  change between a scalar, a list and a JSON value fails the plan, naming
  the column and both kinds: text is not a JSON array, Postgres parses
  text as JSON where wrapping it in a JSON string would keep another
  value, and Postgres converts no list to or from anything else. Change
  the column by hand and adopt the new model.
- A list's element type is kept only as far as JSON tells types apart:
  `UUID[]`, `TEXT[]` and `DATE[]` are arrays of strings, and
  `SMALLINT[]` and `BIGINT[]` arrays of numbers, so a change between
  them is no step ([A list's element](#a-lists-element)).

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
| `superschematic_schema_state` | a row per service: the dialect, the applied model's hash and the model itself as canonical JSON, and the plan in progress with its finished phase: `expanded` when the applied model is the plan's `expandedModel`, `expand` for a plan without one |
| `superschematic_migrations` | a row per step run: the plan's hash, the step's index, phase and subject, the SHA-256 of its SQL, and when it started and finished |

The names are fixed.

### `apply`

`apply` checks the plan's version, its hash, its `to` hash and, when the
plan has one, its `expanded` hash before it runs anything, so an edited
plan is never half-applied. It then takes a
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
contract` the rest after it. `--phase contract` runs only the plan in
progress, once its expand has finished: a plan that is not in progress, such
as one a newer plan superseded, is refused, even when the database is at its
`from` again. When the last expand step finishes, the plan's `expandedModel`
becomes the applied model, in the transaction of that step. When the plan's
last step finishes, the plan's model becomes the applied model.

### The baseline check

The runner refuses a plan whose `from` is not the database's applied model,
naming both, unless the database is part-way through that same plan. Plan
again from what the database recorded: `superschematic-migrate status
--model` prints it, and `migrate plan --from` takes it. A new plan is also
refused while another plan is in progress, unless it supersedes that plan's
pending contract ([A failed rollout](#a-failed-rollout)).

### A failed rollout

A rollout that fails runs no contract step, and the previous version's
servers keep running on the expanded schema. The runner has recorded that
schema as the applied model, so `status --model` prints it. The next deploy
plans from it, and its plan supersedes the pending contract: the runner
names the plan it supersedes, forgets it and runs the new one. The drops
the new version still wants are in the new plan's own contract, planned
from what the database holds, and nothing of the old contract runs unless
the new plan has it. A plan back to the previous version starts from that
model too.

```
superschematic-migrate status --service shop-db --model > applied-model.json
superschematic migrate plan ./schemas/services/shop-db --from applied-model.json --out plan.json
superschematic-migrate apply --plan plan.json --phase expand
```

The superseded plan's contract is refused afterwards: it is no longer in
progress, and the database is not at its `from`. A plan whose contract has
started is never superseded, since the database no longer holds its
expanded model; finish it first. A plan written before plans carried
`expanded`, or applied by a runner that predates it, keeps the earlier
rule: its expand steps leave the applied model at its `from`, and every
other plan is refused until its contract runs.

### `status` and `adopt`

`status` prints the applied model's hash, the plan in progress and its
phase, and the steps logged for it. With `--model` it prints only the
applied model's canonical JSON; between a plan's phases that is the plan's
`expandedModel`.

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
- SQLite refuses `@versioned`, `@optimistic`, `@searchField`, projections,
  `GIN` and `GIST` indexes, and `LTREE` and the PostGIS types
  ([Refusals](#refusals)).
- The engine's own storage is the engine's: it creates its tables and runs
  its behaviors' migrations, and no plan covers them.
