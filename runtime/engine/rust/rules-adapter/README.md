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
`run_operation`, `run_create`, `run_update`, `run_delete`, `prepare_create`
(a create run to its change set and applied nowhere, for an upsert's
insert) and `read_row`.

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
   of the host's, kept for every run. A create that names no key gets one:
   a UUID's base62 wire form for an `Identity.UUID` or `Identity.UserID`
   key, a UUID's text for a `string` key; any other key type is refused
   (`invalid_argument`), since the change set names every row by its key.
2. The layout's define check runs once per core a command runs on over
   it, as `rules.ts` keeps it per core and layout; a type whose spec that
   core refuses is refused so on every command on it. A create or an update whose own fields name an owned
   field by its IR name (`statusEnteredAt`) is refused (`invalid_instance`,
   rule `owned`); the anchor field itself is left to the core.
3. The core runs. A `row` read selects the row by its key, each own and
   owned column converted to the core's JSON and a NULL column absent;
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
   `internal`, and so are sub-table rows, outbox events and deadlines until
   step 3 of D103's order of the work gives each a table.

`ExecuteOptions::own_columns` binds a server's own encoding of the command's
own row (its audit stamps among them) in place of the change set's own
fields, as the generated repositories pass theirs.

A raw SQL write of an owned column is never refused: no trigger or check
guards one, and the core reads what was written. The version moves with
every update (`@optimistic`'s trigger: `BEFORE UPDATE` on Postgres, `AFTER
UPDATE` on SQLite), so a raw write made under a command moves the row and
the command runs again.

## Values

A timestamp the core holds as epoch milliseconds (`convert: "epochMillis"`)
binds as the text `Date.prototype.toISOString` writes (UTC, three fraction
digits), which a Postgres `TIMESTAMPTZ` parses and a SQLite `TEXT` holds,
and reads back from RFC 3339 text with any offset, any fraction (past the
third digit dropped) and `T` or a space. A UUID is base62 in the core and
lowercase hyphenated text in its column (D79). An object or an array binds
as its JSON text; a boolean as 1 or 0 on SQLite.

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
the engine's workspace and its lock file.

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
`tests/store.rs` runs the public functions on both databases, and
`tests/transcript.rs` replays the transcript
`runtime/engine/testdata/rules/core-workflow.transcript.json` on rusqlite
and on Postgres: 16 scenarios, 96 steps (34 commands, 61 checks, 1 raw
write), 0 mismatches, held to a floor of 96, with the 62 steps the
transcript leaves out for its own reasons.

The Postgres cases read `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`, each in a
schema of its own (`rules_rs_<pid>_<nanos>_<n>`, dropped after), and are
skipped without it, saying so.
