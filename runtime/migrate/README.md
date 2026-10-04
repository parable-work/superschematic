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
| `statements` | array of strings | Complete SQL statements without trailing semicolons. The runner runs each with one call, in order. A statement may contain semicolons inside a dollar-quoted body. |
| `transactional` | boolean | Whether the step runs in one transaction. |
| `recovery` | array of strings | Absent unless `transactional` is false. Statements to run before a step that started and did not finish is run again. |
| `foreignKeysOff` | boolean | SQLite only, absent when false. Run the step with foreign key enforcement off and check every foreign key before the commit. |
| `hazards` | array | The step's hazards: `id`, `class`, `subject`, `reader`, `reason`. Informational to the runner. |

### Canonical JSON and hashes

Canonical JSON is what Go's `encoding/json` writes for a value decoded
with `UseNumber` into `any`: compact, object members sorted by key, array
order and number literals kept, `<`, `>` and `&` in strings escaped as
`<`, `>` and `&`. It is `ir.CanonicalJSON` in the compiler;
the runner carries its own copy of the same function.

A hash is the lowercase hex SHA-256 of canonical JSON:

- a model's hash is taken over the model (`toModel`, or the file `adopt`
  reads);
- a plan's hash is taken over the plan object without its `hash` member.

Before it runs anything, `apply` recomputes both and refuses a plan whose
`hash` or `to` does not match, so an edited plan is never half-applied.

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
  plan_phase  TEXT,               -- 'expand' once its expand steps are done
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

The model is stored as text, not `JSONB`, so its bytes stay canonical.

## `apply`

```
superschematic-migrate apply --plan plan.json [--phase expand|contract|all] [--database-url URL]
```

`--database-url` defaults to `$DATABASE_URL`. A `postgres://` or
`postgresql://` URL selects Postgres; a `sqlite:` URL, a `file:` URI or a
path selects SQLite. It must match the plan's dialect. `--phase` defaults
to `all`.

1. Read the plan and check its version, its `hash` and its `to`.
2. Take the lock. On Postgres, one connection holds
   `pg_advisory_lock(key)` for the whole run, where `key` is the first
   eight bytes, big-endian, of the SHA-256 of
   `superschematic_migrate:<service>`; every step runs on that connection.
   A second runner waits. On SQLite every step's transaction is
   `BEGIN IMMEDIATE`, and the checks of steps 3 and 4 are made again inside
   it.
3. Create the state tables if missing, and read the service's state row.
   No row means applied model `""` and no plan in progress.
4. Decide:
   - the row's `plan_hash` is this plan: resume it;
   - no plan is in progress and the row's `model_hash` is the plan's `to`
     (and not its `from`): the plan has been applied; say so and exit 0;
   - another plan is in progress: refuse, naming it and its phase;
   - the row's `model_hash` is not the plan's `from`: refuse, naming both,
     and say to plan again from the applied model
     (`superschematic-migrate status --model`);
   - the row's dialect is not the plan's: refuse.
5. `--phase contract` on a plan whose expand steps have not all finished:
   refuse.
6. Record the plan as in progress (`plan_hash`) if it is not.
7. Run each step of the requested phases in order, skipping a step whose
   log row has `finished_at`:
   - A transactional step: begin (on SQLite, `PRAGMA foreign_keys = OFF`
     first when `foreignKeysOff`); on Postgres `SET LOCAL lock_timeout =
     '5s'`; run the statements; on SQLite with `foreignKeysOff`, run
     `PRAGMA foreign_key_check` and fail the step if it returns a row;
     write the log row with `started_at` and `finished_at`; commit; then
     `PRAGMA foreign_keys = ON`. The step and its log row commit together,
     so it runs once.
   - A step that is not transactional (Postgres only): if its log row
     exists without `finished_at`, run `recovery` first; otherwise write
     the log row with `started_at`. Then `SET lock_timeout = '5s'`, run
     each statement on its own, `RESET lock_timeout`, and set
     `finished_at`.
   - A lock timeout (Postgres SQLSTATE `55P03`, SQLite `SQLITE_BUSY`)
     rolls the step back and retries it after 1, 2, 4, 8 and 16 seconds,
     then fails.
   - Any other error stops the run. The plan stays in progress; the next
     `apply` of the same plan resumes at that step. The error names the
     step's index, subject and the statement that failed.
8. When the last expand step finishes and the plan has contract steps,
   set `plan_phase` to `expand`. When the plan's last step finishes, set
   `model_hash` to `to`, `model` to `toModel` and `dialect`, and clear
   `plan_hash` and `plan_phase`.

## `status`

```
superschematic-migrate status --service NAME [--model] [--database-url URL]
```

Prints the applied model's hash, the plan in progress and its phase, and
the steps logged for it. With `--model` it prints only the applied model's
canonical JSON, which `superschematic migrate plan --from` takes, and fails
when no model is applied.

## `adopt`

```
superschematic-migrate adopt --model model.json [--database-url URL]
```

Records a model as applied without running anything, for a database built
from `create.sql` or changed by hand. The model's `service` and `dialect`
key the row. It refuses while a plan is in progress, and prints the hash it
replaces. `superschematic migrate plan --print-model` prints the model to
adopt.

Exit codes: 0 done, 1 refused or failed, 2 usage.
