# migrations

A schema migration end to end: two versions of one DB service,
`tasks-db`, the plan `superschematic migrate plan` writes between them for
Postgres and for SQLite, and `superschematic-migrate` applying it to a
database that holds rows, one phase at a time (D27 in
`docs/DECISIONS.md`). The docs site's schema migrations page
(`docs/src/content/docs/reference/migrations.mdx`) quotes it;
`runtime/migrate/README.md` is the runner's reference.

| Path | What it is |
|---|---|
| `v1/schemas/services/tasks-db/` | The first version: a `Project`, its `Task`s and their `Comment`s, built for Postgres and SQLite (`outputs.sql.dialects`) |
| `v2/schemas/services/tasks-db/` | The next version of the same schemas root, as another checkout would hold it |
| `scripts/check.sh` | Builds the core binary and the runner, plans v1 to v2, gates the plan on its hazards, applies it to SQLite (and to Postgres when one is given), and compares the outputs with `testdata/generated/` |
| `testdata/generated/` | The plans in `sql` and `markdown` for each dialect, and under `logs/` what the commands below print, which the docs page quotes |

## The change

v2 changes v1 in five ways:

| Change | Postgres | SQLite |
|---|---|---|
| `Task.title` becomes `Task.summary` | a rename, with `--rename task.title=task.summary`; without it, a drop and an add | the same |
| `Comment.mentions`, a list, is added | `ADD COLUMN` with its default, `'{}'` | added by the rebuild below, with `'[]'` |
| `Task.remindBefore` changes from a list of text to a list of integers | `ALTER COLUMN ... TYPE BIGINT[]`, which fails on text that is not a number | a rebuild of `task`, and of `comment`, which references it; text that is not a number becomes `0` |
| an index over `Task.project` and `Task.dueAt` | `CREATE INDEX CONCURRENTLY`, outside a transaction | `CREATE INDEX` in the rebuild |
| `Comment.legacyId` is dropped | `DROP NOT NULL` in `expand`, `DROP COLUMN` in `contract` | nullable in the rebuild, `DROP COLUMN` in `contract` |

`Default<T, V>` fills in decoded payloads, not columns, so a required
column a version adds has a database default only where the compiler gives
it one: an empty list for a list, the current time for a timestamp, a new
UUID for a generated key. A required column without one is `compat` and
`data-dependent`: adding it fails on a table with rows.

## Run it

From the repository root, after `make setup`:

```sh
examples/migrations/scripts/check.sh
```

It needs Go and `sqlite3`. With `SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL`
set to a Postgres whose role may create databases, it also applies the
Postgres plan, with `psql`, to a database it creates and drops. The full
tier of `.github/workflows/ci.yml` runs it in the `go` job, against that
job's Postgres, and so does `make example-migrations`. After a change to
the planner or the runner that alters an output, refresh the copies with
`UPDATE=1 examples/migrations/scripts/check.sh` and check the docs page.

In outline, the commands it runs from `examples/migrations` (it keeps
what they write in a temporary directory):

```sh
# The plan for Postgres, as a pull request would show it, gated on
# destructive hazards: the drop of legacy_id is acknowledged.
superschematic migrate plan v2/schemas/services/tasks-db --from v1/schemas/services/tasks-db \
  --rename task.title=task.summary --format markdown \
  --fail-on destructive --allow destructive:table/comment/column/legacy_id

# The plan for SQLite, for the runner.
superschematic migrate plan v2/schemas/services/tasks-db --from v1/schemas/services/tasks-db \
  --rename task.title=task.summary --dialect sqlite --out plan.sqlite.json

# A SQLite database built from v1's create.sql records v1's model, then
# runs expand before the new servers roll out and contract after.
superschematic build v1/schemas/services/tasks-db --out v1-dist
sqlite3 tasks.db < v1-dist/sql/tasks-db/sqlite/create.sql
superschematic migrate plan v1/schemas/services/tasks-db --dialect sqlite --print-model > v1.model.json
superschematic-migrate adopt --model v1.model.json --database-url tasks.db
superschematic-migrate apply --plan plan.sqlite.json --phase expand --database-url tasks.db
superschematic-migrate status --service tasks-db --database-url tasks.db
superschematic-migrate apply --plan plan.sqlite.json --phase contract --database-url tasks.db
```

Between the phases the check writes a comment as a v1 server would, with
`legacy_id`, and one as a v2 server would, without it; both succeed. A v1
server still breaks on the rename, which is why the plan marks it
`compat`.
