---
title: Runtime mode
description: Run schemas that change while the environment runs, with each type in the table a build writes for it. A publish plans the migration from the model the database recorded, refuses what you did not allow, and applies it through the migration runner.
sidebar:
  order: 6.6
---

Runtime mode is for an environment that cannot generate and compile code,
such as a hosted builder whose schemas a person or an agent edits while it
runs. A change has to take effect with no generator, no compiler of the
generated code and no CI between the edit and the running app. The rows
matter, so each type lives in its own table.

It is one of three modes (D117 in
[docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md)):

| Mode | Rows live in | A schema change is |
|------|--------------|--------------------|
| [development](/superschematic/guides/development-mode/) | fixed tables, each row's fields one JSON column | a publish that writes rows and takes effect at once |
| runtime | the type's own table, as a build writes it | a publish that plans DDL and applies it |
| compiled | the type's own table | a migration plan a deploy applies |

The schemas are the same in all three. A runtime database cannot be told
from a compiled project's: its tables are the ones sqlgen writes for the
same classes, so a project moves on to compiled with no data migration.

## What is built

Runtime mode is the Rust crate `superschematic-rules-runtime` in the
engine's Cargo workspace, `runtime/engine/rust`. It serves the same
request handler as development mode, `superschematic-rules-handler`, over
a SQLite file or, with its `postgres` feature, a Postgres database. A
publish's plan runs in process by default, or in a job on another
machine, as gcp's migration job runs a deploy's (see
[A runner that is a job](#a-runner-that-is-a-job)).

The environment carries two binaries and nothing else of the toolchain:

- `superschematic`, whose planner a publish runs (`migrate plan
  --request`). It reads the documents and plans with sqlgen's model and
  D27's diff, as a build does. No generator and no language toolchain
  runs.
- `superschematic-migrate`, the Go runner, which applies a Postgres plan.
  A SQLite plan is applied in process by the Rust runner.

`Binaries` names them. By default they are `$SUPERSCHEMATIC_BIN` and
`$SUPERSCHEMATIC_MIGRATE_BIN`, else `superschematic` and
`superschematic-migrate` on `PATH`.

```rust
use serde_json::json;
use superschematic_rules::Caller;
use superschematic_rules_runtime::{Publish, Runtime};

let mut runtime = Runtime::open("tasks.db")?;   // or Runtime::postgres(url)
let documents = vec![json!({
    "name": "tasks-db", "kind": "DB",
    "types": { "Task": { "name": "Task", "role": "DBTable", "fields": [
        { "name": "title", "typeRef": { "name": "string" }, "required": true }
    ]}}
})];
runtime.publish(&Publish::of(documents))?;
let me = Caller::new("alice", Vec::<String>::new());
let task = runtime.answer(&me, &json!({
    "kind": "create", "type": "Task", "data": { "title": "Sand the desk" }
}))?;
```

## Schemas

A publish takes the database's schema documents whole: every document of
one DB service, each as `superschematic format --to=json` writes it. A
model is a database's, not a type's, so you cannot publish one type on its
own, and two tables that refer to each other arrive together. The
handler's `define` and its `publish` of one type are refused in runtime
mode.

Each table the documents define is a type the handler serves. Its fields,
relations, unique fields and behaviors read as they do in development
mode. Its rows live in its table: a field is a column, a relation a
foreign key, a behavior's fields columns of the table, and its
collections sub-tables beside it.

## A publish is a migration

A publish does five things:

1. It reads the model the database recorded, from the runner's state
   table (`superschematic_schema_state`).
2. It asks the planner for the plan from that model to the documents'
   model, and for the layout document the handler serves the tables
   through. A database's first plan starts from an empty database.
3. It refuses a plan whose `from` is not the recorded model, with both
   models named (`409 conflict`, class `PlanBaselineError`), and a plan
   with a `destructive` or `data-dependent` hazard the publish does not
   allow (`incompatible_change`, class `PlanHazardError`), with each
   hazard's id.
4. The environment's runner applies the plan, expand and contract back to
   back, never over a serving connection: the Rust runner on a connection
   of its own to a SQLite file, `superschematic-migrate apply --plan -`
   beside a Postgres database, or a job.
5. The runner records the documents and the layout next to the model, in
   `superschematic_runtime`, one row per database, in the transaction
   that finishes the plan (`apply --record`). No server reads the plan's
   model before the layout it serves the new tables through.

A rename reads as a drop and an add until the publish says otherwise.
Name the renames you mean, as `--rename` takes them, and the plan renames
the column in place and keeps its values:

```rust
runtime.publish(&Publish::of(documents).rename("task.title=task.summary"))?;
```

A drop deletes data, so it is refused until you allow its hazard by its
id:

```rust
runtime.publish(&Publish::of(documents).allow("destructive:table/task/column/estimate"))?;
```

A compatibility hazard (`compat`) is never refused. No server built
against the old columns is left running: the handler reads its layout
from the record, not from generated code.

A platform that cannot spawn the planner, a Worker say, has the plan made
where the compiler is, by a CLI or the platform's own service, and sends
it with the schemas in `planned`, beside its layout. The publish holds it
to the database it meets as it holds its own plans.

## Every server follows a publish

Each request reads, in its own transaction, the model the database is
at. When that hash has moved since the last request, it reads the record
again. So every server of the environment follows a publish, whichever
server made it, with no restart.

- A request holds the database's one writer from its start: `BEGIN
  IMMEDIATE` on SQLite, and on Postgres an advisory lock that a publish
  in process holds too. On Postgres a request waits while such a publish
  runs, then reads the new layout. A publish whose runner is a job holds
  no lock: the plan in progress, which the runner records before its
  first step, keeps the requests off.
- A plan in progress serves no request. A request that meets one, as a
  server in another process can between the runner's steps on SQLite, is
  refused as `busy`.
- A step that fails, such as a `data-dependent` step you allowed over rows
  that violate it, stops the publish. The plan stays in progress, and the
  database serves no request until a publish of the same documents
  resumes it, once the rows are fixed.

## Lists, unique fields and relations in SQL

Development mode answers a list, a unique field and a relation from every
row of the type. Runtime mode asks the type's table in a query of its own
for each, with the same answers:

- A list's filter is the ORMs' grammar: an operation per field (`eq`,
  `ne`, `in`, `notIn`, `ilike`, `gt`, `gte`, `lt`, `lte`, `between`,
  `isNull`), a member named for a to-one relation holding the target's
  filter, `allOf`, `anyOf` and the query string's `$query`. It becomes the
  query's condition, a relation's as an `EXISTS` over the target's table.
- A sort orders by its fields, a field with no value last in either
  direction, then by the key in the first field's direction. A page is the
  rows after the cursor's sort values and key, and one row past it says
  whether more follow.
- A unique field's check reads the rows that hold its values, and a
  delete reads the rows each relation into the type reaches.

A value compares as its column does in either database: an instant by its
time, a UUID by its value, a number by its value, and text by code point,
under the `C` collation on Postgres. A value that is not of the field's
kind matches nothing, and `ne` and `notIn` keep a row whose field holds no
value. `ilike` is the handler's own on SQLite, a function the connection
registers. A cursor whose values are not of their fields' kinds is
refused, in either mode.

## Soft deletes

A table with a `deletedAt` field is soft-deletable, as a build's is. It
serves its live rows alone: a get, a list, a filter through a relation, a
unique field's check and an update find no row whose `deletedAt` holds a
time. A delete stamps `deletedAt` with the database's clock and moves the
row's version on, and keeps its sub-table rows, as the compiled adapter
does. It reaches no row that points at it, since a foreign key fires on a
delete and not on an update. A create of a deleted row's key is a
`conflict`, and a relation to it is refused as a row that does not exist.

## A runner that is a job

On a platform whose migration runner is a job, such as gcp's migration
job (D46), a publish gives its plan to a `JobRunner` in place of applying
it in process:

```rust
use std::time::Duration;
use superschematic_rules_runtime::{Directory, JobDatabase, JobRunner, ProcessLaunch, Runtime};

let jobs = JobRunner::new(
    Directory("/var/lib/runtime/bucket".into()),          // where the job reads its documents
    ProcessLaunch { migrate: "superschematic-migrate".into() },
    JobDatabase::Url("postgres://migrator@db/tasks".into()), // how the job reaches the database
)
.with_servers(["tasks-api"])                               // the servers' database roles
.with_wait(Duration::from_secs(600), Duration::from_secs(2));
let mut runtime = Runtime::postgres("postgres://tasks-api@db/tasks")?.with_plan_runner(jobs);
```

1. The publish checks its plan as it does in process, then writes three
   documents into the job's store, under
   `superschematic/migrations/runtime/<service>/<plan hash>/`: the plan,
   its record, and a job document that names both beside it, the
   database, and the servers' roles. The plan never goes on stdin.
2. It starts an execution of the job (`Launch`), which runs
   `superschematic-migrate job --job <reference>`.
3. It polls the model the database records until it is the plan's `to`,
   with no plan in progress and the publish's record, or until the
   execution reports a failure, which the publish returns with the
   runner's own error line, or until the wait's timeout, which it returns
   as `busy` (`PlanTimeoutError`). It holds no lock while it waits.

The job writes the record, and gives each server role its privileges on
the plan's new tables, in the transaction that finishes the plan. A
server that follows the publish may already read and write them.

`JobStore`, `Launch` and `Execution` are the platform's half. `Directory`
and `ProcessLaunch` keep the documents in a directory and run the job as a
process of the same machine. On gcp, `extensions/gcp`'s `RunRuntimeJob`
uploads such a directory to the state bucket and runs an execution of the
stack's migration job with it, reporting the runner's error from Cloud
Logging when it fails.

## Server roles

A server never runs DDL. On Postgres the job connects as the migrator,
which owns the tables, and gives each server role D46's grants: USAGE on
the schema, SELECT, INSERT, UPDATE and DELETE on the tables, and USAGE and
SELECT on the sequences. With them it gives SELECT alone on the runner's
state table and on `superschematic_runtime`, which a server reads to
follow a publish (`readState` in the job document). A role the job no
longer lists loses them. A server whose role may not create tables opens
a database no publish has reached yet, and reads its record once the
first job writes it.

## The plan as a route and a tool

`Runtime::plan` answers what a publish would apply, without applying it:
the service, the models it goes from and to, each step in order with its
phase, operation, subject, statements and hazards, every hazard, the ids
of those a publish refuses, and whether it applies as asked.

`Runtime::answer` takes a request as JSON, as a route or a tool hands it
over. `{"kind": "plan", ...}` and `{"kind": "publish", ...}` take a
publish's members (`documents`, `renames`, `allow`, `planned`), and every
other kind is the handler's request. `superschematic_rules_runtime::tools()`
lists the `plan` and `publish` tools with their input schemas, for an MCP
server to list beside the handler's.

```json
{"kind": "plan", "documents": [ ... ], "renames": ["task.title=task.summary"]}
```

## The tables

The plan writes the tables a build writes, and the runner keeps its two
state tables beside them. Runtime mode adds one table of its own:

| Table | What it holds |
|-------|---------------|
| `superschematic_schema_state` | the runner's: the model the database is at, and the plan in progress |
| `superschematic_migrations` | the runner's: the log of each plan's steps |
| `superschematic_runtime` | the documents of the last publish, the layout document the planner wrote for them, each type's version and the model they belong to, which the runner writes with the plan's last step |

A behavior's queues are the build's too. A table whose writes its
behaviors hear has a change trigger, which writes each write to its
change queue. The host runs the change queue and the deadlines due after
each request, or when you call `run_due` with `Drain::WhenAsked`.

## What is left

- A runtime environment on gcp: its server process, the job's documents
  written to the state bucket from it and the job's execution started
  from it, and a run against Google Cloud.
- The behaviors' own reads of other rows (Dependencies' blockers, a
  relation's linked rows, Pool's rows) still read every row of a type.
- The `createdBy` and `deletedBy` stamps, the access policy as data, the
  event stream and MCP, as in development mode.
- Runtime mode from TypeScript: `@superschematic/rules-handler`
  ([From TypeScript](/superschematic/guides/development-mode/#from-typescript))
  serves development mode alone, with no table store, record or publish.
  The Go and Python libraries are later steps of both modes.
- Moving between modes: development to runtime with rows, and runtime to
  compiled by the recorded model.
