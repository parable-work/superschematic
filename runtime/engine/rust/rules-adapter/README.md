# The adapter in Rust

`superschematic-rules-adapter` is the behaviors' adapter for a hand-written
Rust server (D103 in `docs/DECISIONS.md`): the host loop of the pure core,
`superschematic-rules` (`../rules`), run inside the caller's own
transaction over a build-time project's tables. It is the Rust adapter as
the HTTP runtime's `src/rules.ts` is TypeScript's and `runtime/rules/go`
is Go's, step for step, with the same statements, the same refusals and
the same messages. It reaches the core's crates directly, with no C ABI
between them.

It holds no behavior's name. A server builds its `Core` with the behaviors
its project composes (`superschematic_rules_behaviors::core()`, then its
own with `.with(..)`), reads the layout document its generator wrote
(`internal/generator/behaviorlayout`; the core's README, "The layout
document"), and runs each command through the store of its database. A
`Core` is `Send` and `Sync`, so one serves every task; a `Host` borrows it
and the layout per request.

## Features

Each store is a feature, both off by default, so a server builds the client
it already holds and no other:

- `postgres`: the `postgres` module, async, over any
  `tokio_postgres::GenericClient` inside a transaction (a
  `tokio_postgres::Transaction`). Every future it returns is `Send`.
- `sqlite`: the `sqlite` module, synchronous, over a `rusqlite::Connection`
  (a `rusqlite::Transaction` derefs to one). Begin the transaction
  IMMEDIATE (`conn.transaction_with_behavior(TransactionBehavior::Immediate)`;
  `transaction()` begins DEFERRED): it then holds SQLite's one writer from
  its start, so two commands on one database run one after the other, the
  second reading what the first committed. A DEFERRED transaction reads a
  snapshot another connection may commit past before the unit; its checks
  pass on that snapshot and its write is refused with `SQLITE_BUSY`, which
  refuses the command as `busy` (503), nothing written, for the caller to
  roll back and try again.

Each module has the same functions: `execute` (a command the caller built),
`run_operation`, `run_operation_with` (an operation with `ExecuteOptions`,
the caller's preconditions among them), `run_table_operation` (an operation
of the table as a whole, a command without a row, D104), `run_create`,
`run_update`,
`run_delete`, `prepare_create`
(a create run to its change set and applied nowhere, for an upsert's
insert), `read_row`, `read_sub_rows` (a use's sub-table rows of a row, in
key order), `run_deadline` (a due deadline, below) and `run_change` (a write
from the table's change queue, below).

```rust
use axum::{extract::{Path, State}, Json};
use serde_json::{json, Value};
use superschematic_rules_adapter::{postgres, Caller, Host};

async fn transition(
    State(app): State<App>,                       // holds a Core, a Layout and a pool
    Path(key): Path<String>,
    Json(params): Json<Value>,
) -> Result<Json<Value>, Problem> {
    let mut client = app.pool.get().await?;
    let tx = client.transaction().await?;
    let host = Host::new(&app.core, &app.layout);
    let caller = Caller::new(app.subject(), app.permissions());
    let result = postgres::run_operation(
        &host, &tx, "Order", &key, "status", "transition", params, caller,
    )
    .await?;                                      // a refusal maps to its status
    tx.commit().await?;
    Ok(Json(result))
}
```

An `Error` is `Refused` (the core's refusal whole, or the adapter's own:
`busy`, `conflict`, `internal`, `invalid_argument`, an own write of an
owned field) or `Database` (a failure of the database, but SQLite's
`SQLITE_BUSY`, which is `busy`). A refusal carries
its code, its HTTP status, the engine's class, its message and its details,
so a server answers it as the engine answered the same case, and a client
branches on `code` (`vetoed`, then `details.vetoCode`).

## What a command does

The core's README ("The host loop" and "The adapter") is the contract; in
brief:

1. The command is built with the caller, the host's time and a request id
   of the host's, kept for every run, and the caller's preconditions
   (`ExecuteOptions::preconditions`, D16 and D104), each use's entry by its
   use id as the core's command carries them (`{"Lease": {"token": 3}}`),
   set over the command's own entry of the same use. The core checks them
   against each use's behavior before it reads the row and hands each to
   its use's guard; an update, a delete and an operation present them, and
   any other command that does (a create, `prepare_create`'s among them) is
   refused (`PreconditionsError`), so none is ignored. A create that names
   no key gets one: a UUID's base62 wire form for an `Identity.UUID` or
   `Identity.UserID` key, a UUID's text for a `string` key; any other key
   type is refused (`invalid_argument`), since the change set names every
   row by its key.
2. The layout's define check runs once per core a command runs on over
   it, as `rules.ts` keeps it per core and layout; a type whose spec that
   core refuses is refused so on every command on it. A create or an update whose own fields name an owned
   field by its IR name (`statusEnteredAt`) is refused (`invalid_instance`,
   rule `owned`); the anchor field itself is left to the core.
3. The core runs. A `row` read selects the row by its key, each own and
   owned column converted to the core's JSON and a NULL column absent; the
   layout's audit columns (`createdBy`, D104) are read the same way after
   the own columns and held among the row's own fields, and no command
   writes one: a change set that names one is `internal`;
   `hasRows` asks whether the table has a row; `spec` answers from the
   layout. A soft-deletable table's reads and writes see its live rows
   alone (`live`), so a row the repository holds deleted is `not_found`.
4. A lock key is, on Postgres, the transaction's advisory lock,
   `pg_advisory_xact_lock(hashtextextended(key, 0))`, taken before the
   reads it guards and held until the transaction ends. SQLite takes none:
   a caller's reads may come before the unit that writes, so the unit
   begins with a check of each read made under a lock (a row still at the
   version read or still absent, a table that still has rows or still
   none).
5. A change set applies as one unit in the savepoint `rules_apply`: a
   create's insert of its key, own and owned columns, after a probe of the
   key (a row the table holds under it, live or not, is `conflict`); an
   update's set of its columns and `_version = _version + 1` held to the
   version read; a delete held alike, or, for the command's own row with
   `ExecuteOptions::soft_delete`, the columns that stamp it deleted. A row
   that moved or a check that failed rolls the unit back, and the command
   runs again from fresh state, up to `MAX_ATTEMPTS` (8) runs, then is
   refused as `busy`. A run still asking after `MAX_ROUNDS` (256) is
   `internal`. The unit runs in this order: the lock checks, the fenced row
   writes, a deadline command's spend of its arming, the sub-table rows
   (an insert `ON CONFLICT (row, key) DO NOTHING`, an update of the values
   present and a delete, each held to one row; an update that sets no
   values writes nothing; a value the layout's table does not hold, or the
   key among the values, is `internal`), the deadline changes (an arm
   deletes the key's messages of the table's deadline queue, a dead one
   among them, and inserts one due at its time, `to_timestamp` on Postgres and
   `strftime` on SQLite, its payload as JSON; a clear deletes them), then
   for a row the change set deletes, hard or soft, every message of the
   row, and on a hard delete its sub-table rows. A `subRows` read is
   answered from the layout's columns in key order (`COLLATE "C"` on
   Postgres); on SQLite one made while the command holds a lock is refused
   as `internal`, since the unit has no check of it yet. A `linked` read is
   answered from the layout's own column of the field, the key column
   first, live rows alone, after a key when given, in key order, `LIMIT
   limit + 1`, `more` set when the extra row comes back; a type the layout
   lacks answers an empty page, a field that is no own field of it is
   `internal`, and on SQLite one made under a lock is `internal`. A
   `holding` read (the rows whose use's owned field holds a value, D104:
   Presence's miss over Lease's `holder`) is answered alike from the
   layout's column of the use's field, the value bound as a command's value
   of that column binds; a use or a field the layout lacks is `internal`,
   and so is one made under a lock on SQLite. A `rows` read (a table's
   rows, D104: Pool's pick) is answered alike from the table, each own
   field its `equal` names held to its value (`= ?`, bound as a command's
   value of the column binds, a UUID in either form as its column's, or
   `IS NULL` for a null), live rows alone, after a key when given, in key
   order, `LIMIT limit + 1`; then, for each sub-table its `tables` names,
   one read of that sub-table's rows of every row of the page (`WHERE row
   IN (...)`, in key order, `COLLATE "C"` on Postgres), each set on its
   row by use id and table name, an empty list for a row that has none.
   Equality over own columns is the only rule the adapter learns: the core
   checks it again and applies every other. A UUID field's value that is
   no UUID, which no column of it can hold, matches no row, as the Go and
   TypeScript adapters answer it. A field that is no own field
   of the table, and a use or a sub-table the layout lacks, are
   `internal`, and so is one made under a lock on SQLite. A row a
   change command found gone (`RowWrite::Gone`) is applied in the row
   writes' place with two `NOT EXISTS` deletes: its deadline messages
   unless a live row of the key exists, then its sub-table rows unless any
   row of the key does. A change set's events (`Cx::emit`, D104) are
   written to their row's table's event queue (the layout's `events`) in
   the unit, after the sub-table rows and before the deadline changes, in
   the change set's order, each with the command's time and its payload's
   JSON, a deleted row's kept; on Postgres each insert's `due_at` and
   `created_at` are `clock_timestamp()`. An event of a table whose layout
   keeps no event queue is `internal`, naming the command, the kind, the
   use, the row and the table.

## A command without a row

`run_table_operation(host, client | conn, type, use, operation, params,
caller, &ExecuteOptions)` runs an operation a behavior's declaration gives
the scope `schema` (D104: Pool's `pick`, Lease's `expireHolder`) as the
core's `tableOperation` action, which names no row, and returns its result;
null params are `{}`. The core reads the table's rows through `rows` reads
and the `linked` and `holding` ones, and writes each row it reaches through
a call on that row, each with that row's guards, version and hooks: the
change set holds those rows, each fenced on its version as any row write
is, so one whose run writes no row applies an empty unit, and a read-only
one writes nothing in the caller's transaction. It takes no lock of its
own. The core refuses the options' preconditions, a row's, as
`PreconditionsError`. A layout's operation of the table is
`Operation { table: true }`, which a repository binds as a method of no key.

## Deadlines

`run_deadline(host, client | conn, type, &Deadline, caller, Option<&Claim>)`
runs a due deadline (`Deadline { key, use_id, name, at, payload }`, the key
in the core's form) at the later of the host's clock and `at`, and returns
a `DeadlineRun`: `Ran(Executed)`, `ClaimLost`, `Gone(Refusal)` or
`Vetoed(Refusal)`, whose `why()` is `""`, `claim lost`, `gone` or `vetoed`
as every adapter words it. With a
worker's claim (`Claim { id, token }`), the unit deletes the claimed message
by its id and token, held to one row, right after the command's own row
write and before the change set's deadline changes; when the unit does not
apply the adapter reads the claim, and one no longer held is `ClaimLost`,
nothing applied, while one still held runs again. Without a claim, the
arming is read before each run (none at `at` is `not_found`, `at` after the
command's time `invalid_argument`, in the reference host's words) and the
unit spends the live message armed at `at`; `execute` of a deadline command
takes this path, as the transcript does. A core `not_found` (the row or the
use is gone) spends the arming in a unit of its own and is `Gone`. With a
claim, a guard's veto (`vetoed`) releases the message: ready again, due in
`VETOED_RETRY_SECONDS` (60), its claim cleared and the attempt the claim
counted given back, nothing of the run applied, and the run is `Vetoed`
(or `ClaimLost` when the release finds the claim gone), so a fire a guard
keeps holding comes back every minute with its sub-table row pending;
without a claim the veto is refused as before. Whether a deadline is still
armed is the adapter's check, never the core's (the core's README, "The
adapter"). A generated Rust API crate's behavior worker
runs each claimed message so on a transaction of the pool, which it commits
with the message's completion.

## Changes

`run_change(host, client | conn, type, &TableChange { key, write, at, before,
after }, caller, Option<&Claim>)` runs one message of a table's change
queue, as its trigger wrote it, as the core's change command (D103). The
key is taken in either form, the core's or its column's (a UUID's base62
or hyphenated text). The write is `insert`, `update` or `delete`, anything
else `internal`. Each image is mapped by the layout, an own column to an
own field and a use's owned column to its field by declared name, other
members ignored, each value converted as a row read converts that column:
on Postgres as its `jsonb` value, on SQLite as its storage class, SQLite's
JSON text parsed first; a null is absent. With a claim, the unit deletes
the message by its id and token, held to one row, after the row writes and
before the sub-table rows and deadlines; the result is
`ChangeRun::Ran(Executed)` or `ClaimLost`, whose `why()` is `""` or `claim
lost`, and a row that moved runs again. Without a claim nothing is spent.
A refusal comes back whole, nothing applied, for the worker to fail the
message. A change of a row that is gone writes `RowWrite::Gone`, applied as
above. The layout's `changes` is `Table.changes: Option<Changes>`.

`ExecuteOptions::own_columns` binds a server's own encoding of the command's
own row (its audit stamps among them) in place of the change set's own
fields, as the generated repositories pass theirs.

A raw SQL write of an owned column is never refused: no trigger or check
guards one, and the core reads what was written. The version moves with
every update (`@optimistic`'s trigger: `BEFORE UPDATE` on Postgres, `AFTER
UPDATE` on SQLite), so a raw write made under a command moves the row and
the command runs again.

## Events

`spend_event(host, client | conn, type, Option<&Claim>)` spends a worker's
claim of a message of a type's event queue (D104) in the caller's
transaction, deleting the message by its id and token (held to one row),
and returns `EventRun::Spent` or `EventRun::ClaimLost`, whose `why()` is
`""` or `claim lost`; without a claim nothing is spent. A type whose table
keeps no event queue is `internal`. `read_queued_events(host, client |
conn, type)` reads a type's queued events in the order written
(`created_at`, then `id` on Postgres and the rowid on SQLite, whose one
command's events share a millisecond), each a `QueuedEvent { key, use_id,
kind, at, payload }` with the key in the core's form; a table with no
event queue has none. The layout's `events` is `Table.events:
Option<Events>`. A generated API's behavior worker spends each claimed
message in a transaction of the pool and hands the event to the API's
Workers method of its use and kind with an `EventRun` of that transaction,
so the method's writes and the completion commit together.

## Values

A timestamp the core holds as epoch milliseconds (`convert: "epochMillis"`)
binds as the text `Date.prototype.toISOString` writes (UTC, three fraction
digits), which a Postgres `TIMESTAMPTZ` parses and a SQLite `TEXT` holds,
and reads back from RFC 3339 text with any offset, any fraction (past the
third digit dropped) and `T` or a space. A UUID is base62 in the core and
lowercase hyphenated text in its column (D79). An object or an array binds
as its JSON text; a boolean as 1 or 0 on SQLite. A JSON column's value,
whatever it is (a string, a number and a boolean among them), binds as its
JSON text, so it reads back as it was; and a double that is a whole number
reads back as its digits (SQLite's REAL 2.0 as 2), as JavaScript writes it
and Postgres's `to_jsonb` writes a DOUBLE PRECISION (D104). A
`Generic.Int64` column (a `BIGINT` on Postgres, an `INTEGER` on SQLite),
own, owned or of a sub-table, binds and reads back as a JSON number, as a
lease's token and a meter's usage are kept (D104).

Postgres binds every value as text, as node-pg does, so each is read into
the type of the column it meets, and reads each column of a row as
`to_jsonb`'s JSON, whatever its type, keeping a number's text. SQLite binds
each value as its storage class and reads a `Generic.JSON` column's text as
the JSON it holds.

## Why it is not published

Only `runtime/schema/rust` and `runtime/http/rust` publish to crates.io, and
`cargo publish` refuses a path dependency without a version, so the HTTP
runtime cannot offer this crate until the core's crates publish (D103,
"Left for later steps"). A hand-written server depends on it by path, with
the engine's workspace and its lock file. So does a generated Rust API
crate that serves a table's behavior operations (an API's
`@behaviorOperations` sets), its first consumer in the tree: its
`src/behaviors.rs` embeds the database's layout, builds the core of
`superschematic-rules-behaviors` once per process, and runs each
operation with `postgres::run_operation` on a transaction of the
database's deadpool, which it commits on success (one whose input carries
the caller's preconditions, a body member by use id, with
`postgres::run_operation_with`, the member taken out of its parameters
and handed to the core in `ExecuteOptions`, D104), and each due deadline of
a `@behaviorWorker` with `postgres::run_deadline` and each change of its
table's change queue with `postgres::run_change`. The naming file's
`[paths] rules_adapter_rust` and `rules_behaviors_rust` name the two
crates, and the build refuses such an API while either is unset.

## Tests

```
cd runtime/engine/rust
cargo fmt -p superschematic-rules-adapter --check
cargo clippy -p superschematic-rules-adapter --all-targets --all-features -- -D warnings
cargo clippy -p superschematic-rules-adapter --all-targets --no-default-features -- -D warnings
cargo test -p superschematic-rules-adapter --all-features
```

with `RUSTUP_TOOLCHAIN` set to the toolchain `tools.env` pins. The crate's
own tests (`src/tests.rs`) run the host loop over a store in memory, with
Workflow and with a toy behavior of their own, holding each statement and
value as `rules.test.ts` holds TypeScript's; `src/sqlite.rs` and
`src/postgres.rs` hold each store's unit, its lock, and a command over a
store that moves the row under it through the same transaction (a moved
row, a moved read made under a lock, a soft delete made under a command).
`tests/store.rs` runs the public functions on both databases, a deadline's
claim and its arming among them; `Probe`, a table with the change trigger
and queue sqlgen writes, with Timers and a toy `Echo` behavior that records
what it heard (a raw insert heard, with a Postgres timestamp at +05:30,
SQLite's 1 and 0 and JSON text, and a hyphenated UUID; Timers arming a
create deadline from the write's time; the message spent; a claim lost; a
soft-deleted, a hard-deleted and a returned row; a refused change leaving
its message claimed); and `Held`, Timers with a toy `Hold` whose guard
vetoes `fireTimer`, which holds the vetoed release
(`a_vetoed_fire_is_released_counting_no_attempt`); and `Question`, a
Workflow whose `by` names `askedOf` and `createdBy`, which holds the audit
read (`a_row_read_holds_the_audit_fields`): a stamped and an unstamped
`createdBy`, `by` on each field, a field with no value naming no one, a
repeat leaving `_version` and `enteredAt` where they were, and an update of
`createdBy` refused with the stamp kept; and `Task`, Constants, StateRules
and Approvals implemented beside a Workflow, which holds a sub-table on a
table with neither queue, a read-only operation that reads sub-rows, the
audit `createdBy` that a gate excluding the creator reads, and own
booleans, numbers and JSON read back as the core holds them
(`a_sub_table_with_no_queue_and_own_columns_read_as_the_core_holds_them`);
and `Job`, Lease and Retries implemented beside a Workflow, with a toy
`Finder` that reads the rows whose Lease's holder is a subject, which holds
owned whole numbers (`Generic.Int64`), JSON and timestamps, two sub-tables
of every declared type, a deadline armed, moved, cleared, and cleared and
armed again in one change set, the holding read, and the caller's
preconditions on an update, a delete and an operation, a malformed one
refused before the row is read
(`a_leased_row_keeps_its_token_its_sub_tables_and_its_deadline_and_a_write_presents_the_token`).
`tests/transcript.rs`
replays the transcripts in `runtime/engine/testdata/rules` on rusqlite and
on Postgres, each command over a layout of every type of its database:
`core-workflow.transcript.json`, 96 steps (34 commands, 61 checks, 1 raw
write); `core-timers.transcript.json`, 139 steps (85 commands, 12 of them
deadlines and 29 changes, and 54 checks), the scenario whose fire StateRules
vetoes among them; `core-reactions.transcript.json`, 586 steps (476
commands, 252 of them changes, and 110 checks);
`core-reactions-changes.transcript.json`, 60 steps (48 commands, 24 of them
changes, and 12 checks); `core-constants.transcript.json`, 39 steps (20
commands and 19 checks); `core-staterules.transcript.json`, 76 steps (43
commands and 33 checks); `core-approvals.transcript.json`, 122 steps (93
commands, 71 of them operations and 5 changes, and 29 checks); and the
hand-written `core-workflow-principals.transcript.json` (D104, D113), 101
steps (63 commands, each compared with its row's `expect.version`, and 38
checks), and `core-freezes-and-sign-offs.transcript.json` (D104), 115 steps
(75 commands, each compared with its row's `expect.version`, and 40
checks); from the work queue's recordings of the behaviors D104 ports to
tables, `core-lease.transcript.json`, 436 steps (323 commands, 84 of them
presenting Lease's token as the command's `preconditions` and 3 deadlines,
and 113 checks), `core-presence.transcript.json`, 141 steps (89 commands, 8
of them a miss's deadline, and 52 checks), `core-budget.transcript.json`,
495 steps (331 commands, 46 of them writing a scope's row, and 164 checks),
and `core-retries.transcript.json`, 250 steps (196 commands and 54
checks); and the hand-written `core-retries-waits.transcript.json` (D104),
158 steps (118 commands, each compared with its row's `expect.version`,
and 40 checks), and `core-periods-and-presence.transcript.json` (D104), 77 steps
(52 commands, each compared with its row's `expect.version`, and 25
checks); and, of the behaviors only the core implements (D104, step 3), the
hand-written `core-seats.transcript.json`, 88 steps (66 commands, four of
them repeats, each compared with its row's `expect.version`, and 22
checks), and `core-reconcile.transcript.json`, 58 steps (36 commands, two
of them repeats and two the stale deadline, each compared with its row's
`expect.version`, and 22 checks), whose Workflow and Reconcile are
anchored on fields, and `core-schedules.transcript.json`, 161 steps (131
commands, 19 of them deadlines and 10 changes, each compared with its
row's `expect.version`, 29 checks and one raw write), each command's
`expect.events` compared with what `read_queued_events` reads of every
type's event queue after it, which it then empties; each held to a floor
of its step count, its `expect.others` compared on every command step and
its numbers by value, 0 mismatches each.

The Postgres cases read `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`, each in a
schema of its own (`rules_rs_<pid>_<nanos>_<n>`, dropped after), and are
skipped without it, saying so.
