# The behaviors' core (Go)

`github.com/parable-work/superschematic/runtime/rules/go` is the pure core
of the behaviors (D103 in `docs/DECISIONS.md`) for Go, and the one adapter
every Go caller runs it through. Package `rules` binds
`libsuperschematic_rules.a`, the native build of
`runtime/engine/rust/rules-wasm` (`superschematic-rules` with every behavior
ported onto it), through cgo, over the same JSON C ABI the wasm32 module
`@superschematic/rules` loads. `runtime/engine/rust/rules/README.md` is the
contract's prose; `contract.go` mirrors its JSON as
`runtime/rules/typescript/src/contract.ts` does.

```
binding.go           RunJSON, DeclarationsJSON, CheckJSON over the C ABI; Core, Native, Error, Declaration
contract.go          the core's JSON: TypeSpec, Command, Preconditions, Action, Answers, Step, Outcome, ChangeSet, RowWrite
layout.go            the layout document internal/generator/behaviorlayout writes: Layout, ParseLayout, MustParseLayout
store.go             Store, the caller's transaction: Dialect, LockMode, Statement
adapter.go           Host, Execute, RunOperation, RunCreate, RunUpdate, RunDelete, PrepareCreate, ReadRow
subtables.go         a use's sub-table rows: the subRows read, the change set's writes, ReadSubRows
deadlines.go         a table's deadline queue: arms and clears, the spend, RunDeadline, Deadline, Claim, ReadDeadlines
changes.go           a table's change queue: RunChange, Change, ChangeRun, the trigger's images converted, the linked and holding reads, a gone row's deletes
events.go            a table's event queue (D104): a change set's events written, SpendEvent, EventRun, ReadQueuedEvents, QueuedEvent
convert.go           core JSON to column values and back, the UUID base62 codec (D79), TimeOfMillis
refusal.go           Refusal, Code, AsRefusal, IsNotFound, Internal, BusyRefusal
postgres/            package postgres: a Store over a pgx transaction
sqlite/              package sqlite: a Store over a database/sql transaction, any driver
include/             superschematic_rules.h, the C ABI's header
internal/rulestest/  the tests' databases: a SQLite file, a Postgres schema, @optimistic's triggers
```

The root package imports the standard library alone; `postgres` imports
pgx, and `sqlite` `database/sql` (the tests use `modernc.org/sqlite`).

## The archive

The package links `lib/<goos>_<goarch>/libsuperschematic_rules.a`, which
`make rules-archive` (`scripts/rules-archive.sh`) builds with the toolchain
`tools.env` names and stages there, beside a stamp of its digest
(`archive_stamp_<goos>_<goarch>.go`, gitignored) that puts the archive's
content into the package's build ID. The cgo flags name that directory, so
inside this checkout, and in a generated module that replaces this one
with the checkout's path, nothing more is needed. Outside it, put the
directory in `CGO_LDFLAGS`:

```
CGO_LDFLAGS="$(scripts/rules-archive.sh --print)" go test ./...
```

A Go server links it beside superscalar's, the version graph's and the
engine's archives; `scripts/engine-link-check.sh` proves the four link into
one binary, each defining its own C functions alone. No prebuilt archive
is published yet (D82), so the build needs cargo.

## The core

`Native()` is the archive's core, with every behavior ported so far. The
archive builds it once per process, the first time any operation runs,
and every goroutine shares it: each call is a cgo call that copies its
input in place and its output out, releasing the core's buffer with
`rules_free`, so it is safe for concurrent use. `Core` is an interface
(`Run`, `Declarations`, `Check`) so a test can script one. An input the
core cannot read is an `*Error` (`invalid_json`, `invalid_input`); a
command it refuses is a step, `Step.Done.Refused`, never an error.

## The adapter

`Execute` runs one command inside the caller's transaction, the host loop
of the core's README ("The host loop", "The adapter"), and holds no
behavior's name: the layout maps the core's logical names onto tables and
columns. A `Host` is per transaction: its `Core` (nil is `Native()`), its
`Layout`, its `Store`, and the clock and request ids the wrappers build
commands with.

- It checks each type of the layout once per core, the first time a
  command runs on that core over it (`Core.Check`), as `rules.ts` keeps
  the check per core and layout; a type whose spec the core refuses
  refuses every command on it with that refusal.
- It gives a command a request id, kept for every run, and a create with
  no id a key: a UUID's base62 for a UUID key, a UUID's text for a string
  key, and `invalid_argument` for any other.
- It refuses a create or update whose own fields name an owned field by
  its IR name (`invalid_instance`, rule `owned`), and leaves the anchor
  field to the core, which refuses it in its own words.
- It answers a `row` read with `ReadRow` (a soft-deletable table's live
  rows alone, each column converted to the core's JSON, a NULL absent, the
  layout's `audit` fields, `createdBy` (D104), among the row's own fields,
  each converted as an own column is, which no write sets),
  `hasRows` from the table, `spec` from the layout, and `subRows` from the
  use's sub-table in the layout (`Use.Tables`), in key order, each
  recording the locks held when it was made; it takes each lock once. It
  answers a `linked` read with one page of a type's rows whose own field
  holds a key, in key order, live rows alone, `LIMIT limit + 1` for
  `more`, a type the layout lacks answering none and a field that is no
  own field of it refused as `internal`. It answers a `holding` read
  (D104) as a linked read, with one page of a type's rows whose use's
  owned field holds a value, by the layout's column of the use's field,
  the value bound as a command's value of it is, a use or field the
  layout lacks refused as `internal`. On SQLite (fenced locks) a
  `subRows`, `linked` or `holding` read made under a lock is refused as
  `internal`: the unit has no check of it yet.
- It hands the core a command's `Preconditions` (D16, D104), each use's
  entry by its use id (`{"Lease": {"token": 3}}`), as the command presents
  them, or as `ExecuteOptions.Preconditions` gives them to a command that
  presents none; the core checks them before it reads the row of an
  update, a delete or an operation, and refuses any other command that
  presents one. Nothing of the adapter's reads an entry.
- It applies a change set as one unit (`Store.Apply`): a create's insert
  after a check that the table holds no row of the key (`conflict`), an
  update's set and `_version = _version + 1`, a delete, each held to its
  key, the version the change set expects and, for a soft-deletable
  table, the row's being live. A unit that finds a row moved applies
  nothing, and the command runs again from fresh state, `MaxAttempts` (8)
  times, before it is refused as `busy`. After the row writes come a
  deadline command's spend of its arming or a change command's of its
  claimed message (deleted after the row writes), the sub-table rows (an insert
  `ON CONFLICT (row, key) DO NOTHING`, an update and a delete, each held to
  one row), the deadline changes, messages of the layout's
  `Table.Deadlines` (an arm deletes the key's messages, a dead one among
  them, and inserts one due at its time; a clear deletes them), and, for a row
  the change set deletes, every message of the row and, on a hard delete,
  its sub-table rows; a deleted row's sub-table writes and deadline
  changes are skipped. A row a change command found gone (`RowGone`) is
  applied in the row writes' place with two `NOT EXISTS` deletes: its
  deadline messages unless a live row of the key exists, and its sub-table
  rows unless any row of the key does. A run still asking after
  `MaxRounds` (256) is `internal`, as are a kind of read, step, outcome or
  write the adapter does not know, and a type the layout lacks. A change
  set's events (`Cx::emit`, D104) are written to their row's table's event
  queue (the layout's `Events`) in the unit, after the sub-table rows and
  before the deadline changes, each with the command's time, and kept when
  the row is deleted; one of a row whose table keeps no event queue is
  `internal`, nothing applied.

Every refusal, the core's or the adapter's, is a `*Refusal` (`Code`,
`Status`, `Class`, `Message`, `Details`), in TypeScript's adapter's words
(`runtime/http/typescript/src/rules.ts`), which `errors.As`, `AsRefusal`
and `IsNotFound` read through any wrap; a failure of the database is the
driver's error, wrapped, but one a store wraps as `ErrBusy` (SQLite's
`SQLITE_BUSY`), which is refused as `busy`.

The wrappers build the command: `RunOperation` returns an operation's
result, `RunCreate` the key, `RunUpdate` and `RunDelete` nothing.
`ExecuteOptions.OwnColumns` binds a repository's encoding of the command's
own row's own columns in place of the change set's (audit stamps among
them), `SoftDelete` the columns a delete stamps in place of deleting, and
`Preconditions` the caller's preconditions, which a generated repository
takes from `orm.WithPreconditions` for every command it runs through the
core (`PrepareCreate` presents them too, for the core to refuse).
`PrepareCreate` runs a create to its change set and applies nothing,
returning the owned fields an upsert's insert binds. `ReadSubRows` reads a
use's sub-table rows of a row, and `ReadDeadlines` its armed deadlines (a
transcript's `expect`, a test's).

## Deadlines

`RunDeadline(ctx, host, type, Deadline, caller, *Claim, *ExecuteOptions)`
runs a due deadline (`Deadline{Key, UseID, Name, At, Payload}`, the key in
the core's form) at the later of the host's clock and `At`, so a worker
whose clock lags the database's never runs one before it falls due, its
row write stamped as the options say (nil for none), and returns a
`*DeadlineRun`: `Ran` with its `Result`, or nothing run, `Why` saying why
(`WhyClaimLost`, `WhyGone`, `WhyVetoed`). With a worker's claim (`Claim{ID, Token}`),
the unit deletes the claimed message by its id and token, held to one row,
right after the command's row write and before its deadline changes, so an
arming the fired command makes again survives the spend. A unit that does
not apply reads the claim: one no longer held (another command armed or
cleared the deadline, or a lease ran out and another claim holds it) is
`claim lost`, nothing applied; one still held runs again. Without a claim
the arming is read before each run, as `Execute` reads it for a deadline
command: none armed at `At` is refused `not_found`, one not due yet
`invalid_argument`, in the reference host's words, and the unit spends the
live message armed at `At`. A core `not_found` whose row or use is gone
(the table holds no live row of the key, or the layout no such use) spends
the arming in a unit of its own and is `gone`, with the core's refusal;
any other refusal, a `not_found` of something else a rule reached among
them, is the error, nothing applied. With a claim, a `vetoed` refusal (a
guard holding the fire) releases the message instead: ready again, due in
`VetoedRetrySeconds` (60), its claim cleared and the attempt the claim
counted given back, in the store's transaction, nothing of the run
applied; the run is `WhyVetoed` with the refusal, or `claim lost` when the
release changes no row. A fire a guard keeps holding so comes back every
minute, and its sub-table row stays pending; without a claim the veto is
the error. Whether a deadline is still armed is this check, never the
core's. A generated Go
ORM's repository wraps it as `RunDeadline(ctx, message, claim)` in its
transaction, and a generated API's behavior worker calls that for each
claimed message. Values passed as
`any` are what `encoding/json` writes: a date-time as epoch milliseconds,
a UUID as its base62 wire form; `TimeOfMillis` reads a date-time back.

## Changes

`RunChange(ctx, host, type, Change, caller, *Claim, *ExecuteOptions)` runs
a message of a table's change queue, as its trigger wrote it, through the
core's change command (D103), at the host's clock. `Change{Key, Write, At,
Before, After}` holds the row's key, in the core's form or its column's (a
UUID's base62 or hyphenated text), the write (`insert`, `update` or
`delete`; anything else is refused as `internal`), its time in epoch
milliseconds, and the row's columns before and after it, each a JSON object
by column or null. Each image is mapped by the layout: an own column to the
core's own field, each use's owned column to that use's field by declared
name, other members ignored, each value converted as a row read converts
the column (on Postgres as `jsonb_build_object` wrote it, a timestamp with
the session's offset among them; on SQLite TEXT, INTEGER, REAL and a
boolean's 0 or 1, and an image the driver hands back as TEXT parsed first),
a null absent. It returns a `*ChangeRun`: `Ran` with its `Result`, or
nothing run with `Why` `claim lost`. A row that is gone completes its
message: the core writes `RowGone`, whose deletes take its deadline
messages, and its sub-table rows on a hard delete. With a worker's claim
the unit deletes the claimed message of the table's change queue
(`Table.Changes`) by its id and token, held to one row, after the fenced row
writes and before the sub-table rows and deadlines; a unit that does not
apply because the claim no longer holds the message is `claim lost`, and
one that does not apply for a row that moved runs again. Without a claim
nothing is spent. A refusal, or a failure of the database, is the error,
nothing applied, for the worker to fail the message (D68's backoff, then
dead). A generated Go ORM's repository of a table whose layout has
`changes` wraps it as `RunChange(ctx, message, claim)` in its transaction,
and a generated API's behavior worker calls that for each claimed message
of its table's change queue, beside its deadline queue.

## Events

A change set's events (D104) are messages of the event queue of their
row's table (`Table.Events`): the row's key, bound as its table's key
binds, the use, the kind, the command's time in epoch milliseconds and the
payload's JSON, NULL for none. On Postgres each insert's `due_at` and
`created_at` are the statement's `clock_timestamp()`, so one transaction's
events of a row keep their order in the queue's claim; on SQLite the
table's defaults stamp them to the millisecond. `SpendEvent(ctx, host,
type, *Claim)` spends a worker's claim of a message of a type's event queue
in the caller's transaction, deleting it by its id and token, held to one
row, and returns an `*EventRun`: `Ran`, or nothing spent with `Why` `claim
lost`; without a claim nothing is spent. The handler runs in the same
transaction after it, so its writes and the completion commit together. A
type whose table keeps no event queue is `internal`. `ReadQueuedEvents(ctx,
host, type)` reads a type's messages in the order written (`created_at`,
then `id` on Postgres and the rowid on SQLite, whose one command's events
share a millisecond), each a `QueuedEvent{Key, UseID, Kind, At, Payload}`
with the key in the core's form; a table with no event queue has none. A
generated Go ORM's repository of a table whose layout has `events` wraps
the spend as `RunEvent(ctx, event, claim, handle)`, which runs `handle`
with a `TxInterface` over the same transaction, and a generated API's
behavior worker calls it for each claimed message, handing the event to
the API's Workers method of its use and kind.

## The stores

| | `postgres.NewStore(tx)` | `sqlite.NewStore(tx)` |
|---|---|---|
| transaction | a `pgx.Tx` (or `*pgx.Conn` in one) | a `*sql.Tx` (or `*sql.Conn` in one) |
| placeholders | `$n` | `?n` |
| unit | the savepoint `rules_apply`, rolled back to and released when a row moved, a check failed or a statement failed | the same savepoint |
| check | `SELECT (<condition>) AS held` | `SELECT CASE WHEN (<condition>) THEN 1 ELSE 0 END` |
| lock | `pg_advisory_xact_lock(hashtextextended(key, 0))`, held until the transaction ends (`LocksHeld`) | none: the unit begins with a check of each read made under a lock, a row still at its version or still absent, a table that still has rows or none (`LocksFenced`) |
| date-time | `TIMESTAMPTZ`, a `time.Time` | text, as `Date.prototype.toISOString` writes it |
| UUID | hyphenated text | hyphenated text |

A store wraps one transaction and is not shared across goroutines.
Postgres's version column moves in `@optimistic`'s `BEFORE UPDATE` trigger,
SQLite's in an `AFTER UPDATE` trigger guarded by `WHEN NEW._version =
OLD._version`; the adapter's update moves it itself, so a trigger moves it
once.

On SQLite, begin the transaction IMMEDIATE: `_txlock=immediate` in the DSN
makes `BeginTx` begin one in `modernc.org/sqlite` and in
`mattn/go-sqlite3`. It then holds SQLite's one writer from its start, so
two commands on one database run one after the other, the second reading
what the first committed. A DEFERRED transaction, `database/sql`'s
default, reads a snapshot another connection may commit past before the
unit: the unit's checks pass on that snapshot, and its write is refused
with `SQLITE_BUSY`, which the store wraps as `rules.ErrBusy` and the
adapter refuses as `busy` (503), nothing written, for the caller to roll
back and try again.

## A hand-written server

```go
func ship(ctx context.Context, pool *pgxpool.Pool, layout *rules.Layout, id string) (json.RawMessage, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	host := rules.Host{Layout: layout, Store: rulespostgres.NewStore(tx)}
	caller := rules.Caller{Subject: "sam", Permissions: []string{"orders.ship"}}
	result, err := rules.RunOperation(ctx, host, "Order", id, "status", "transition", map[string]string{"to": "shipped"}, caller, nil)
	if err != nil {
		// A *rules.Refusal carries its HTTP status: vetoed 409, forbidden 403, not_found 404, busy 503.
		return nil, err
	}
	return result, tx.Commit(ctx)
}
```

The layout is the document `internal/generator/behaviorlayout` writes; a
generated Go ORM embeds it as `RulesLayout` and runs its repositories
through this package.

A behavior's owned columns are read-only in a generated repository's
inputs, but nothing here, and no trigger or check, refuses a raw SQL write
of one: an operator who needs to repair a row can (D103), and the next
command reads what the write left, the write having moved the row's
version.

## Tests

```
make rules-archive
cd runtime/rules/go
go test -race -count=1 ./...
```

The binding's tests run each operation of the C ABI, and the native
core's `Run` and `Check` from 64 goroutines at once (its `Declarations`
are read once and kept); the contract's tests hold each type to the
core's JSON; the adapter's tests (`rules.test.ts`'s and
`rules-store.test.ts`'s cases) run on a SQLite file and on Postgres, over
the archive's core and over cores a test scripts; each store's tests hold
its unit, checks and lock to the statements it sends; `deadlines_test.go`
holds the arm, the clear, the spend with and without a claim, a lost claim,
a moved row and a gone one, and the vetoed release
(`TestRunDeadlineReleasesAVetoedFire`, over a scripted core whose deadline
command is vetoed); `changes_test.go` holds the trigger's images converted
(a Postgres timestamp with an offset, SQLite's 0 and 1, SQLite's JSON
text, a hyphenated UUID, null and absent), the linked read (paging, live
rows, a type the layout lacks), a gone row's deletes (a soft-deleted row
keeps its sub-table rows, a row that came back keeps everything) and
`RunChange` with a claim (ran, claim lost, a refusal leaving the message
claimed) and without one; `audit_test.go` holds the audit read
(`TestReadRowHoldsTheAuditFields`: the select list's order, a UUID in
base62, a NULL absent, the core's row and linked reads holding `createdBy`,
and a write that names it refused) and an operation the core answers with
no write writing nothing (`TestAnOperationTheCoreAnswersWithNoWriteWritesNothing`:
no statement, no stamp, `_version` and `enteredAt` unmoved), on SQLite and
on Postgres; `sign_offs_test.go` holds a table whose only sub-table keeps
no deadline or change queue, Approvals' beside Constants and StateRules
(`TestASubTableWithNoQueueIsWrittenAndReadAsTheCoreSays`: its rows
inserted, read, updated and deleted, a hard delete removing them, a
read-only operation writing nothing, the audit `createdBy` read for a
StateRules `by`, and boolean and number columns read as JSON, SQLite's 0
and 1 as a boolean), on SQLite and on Postgres; `preconditions_test.go` holds
a table that implements Lease beside Retries (D104): the command's
preconditions as an update, a delete, an operation, a create and a
prepared create present them, from the options or their own
(`TestACommandPresentsTheCallersPreconditions`), the holding read (paging,
live rows, a number compared as its integer column holds it, a type the
layout lacks, a use or field it lacks, under a lock), owned columns and
sub-table rows of every declared type, a Generic.Int64 read back as a JSON
number and a Generic.JSON one as its JSON, and a deadline cleared and
armed again in one change set
(`TestOwnedColumnsAndSubTableRowsOfEveryDeclaredTypeRoundTrip`), and, on
the archive's core, a malformed precondition refused as
`PreconditionsError` before the row is read, a stale token vetoed and the
current one going through (`TestThePreconditionsReachTheCoresLease`), on
SQLite and on Postgres. `transcript_test.go`
replays the eighteen transcripts in `runtime/engine/testdata/rules` on SQLite
and on Postgres, as the TypeScript adapter does:
`core-workflow.transcript.json`, 96 steps (34 commands, 61 checks, one raw
write); `core-timers.transcript.json`, 139 steps (85 commands, 12 of them
deadlines and 29 changes, and 54 checks);
`core-reactions.transcript.json`, 586 steps (476 commands, 252 of them
changes, and 110 checks); `core-reactions-changes.transcript.json`, 60
steps (48 commands, 24 of them changes, and 12 checks);
`core-constants.transcript.json`, 39 steps (20 commands and 19 checks);
`core-staterules.transcript.json`, 76 steps (43 commands and 33 checks);
`core-approvals.transcript.json`, 122 steps (93 commands, 71 of them
operations and 5 changes, and 29 checks); the work queue's, on the core
(D104): `core-lease.transcript.json`, 436 steps (323 commands, 3 of them
deadlines and 84 presenting Lease's token as their precondition, and 113
checks); `core-presence.transcript.json`, 141 steps (89 commands, 8 of
them deadlines, and 52 checks); `core-budget.transcript.json`, 495 steps
(331 commands and 164 checks); `core-retries.transcript.json`, 250 steps
(196 commands and 54 checks); and the hand-written
`core-workflow-principals.transcript.json` (D104), 92 steps (58 commands,
each compared with its row's `expect.version`, and 34 checks),
`core-freezes-and-sign-offs.transcript.json` (D104), 115 steps (75
commands, each compared with its row's `expect.version`, and 40 checks),
`core-retries-waits.transcript.json` (D104), 158 steps (118 commands, 9
of them deadlines and 24 presenting a precondition, each compared with its
row's `expect.version`, and 40 checks), and
`core-periods-and-presence.transcript.json` (D104), 77 steps (52
commands, one a deadline, each compared with its row's `expect.version`,
and 25 checks); and those of the behaviors only the core implements,
`core-seats.transcript.json` (D104), 88 steps (66 commands, Seats'
takes, leaves and lists among them and four repeats answered unchanged,
each compared with its row's `expect.version`, and 22 checks), and
`core-reconcile.transcript.json` (D104), 58 steps (36 commands, two of
them the stale deadline, each compared with its row's `expect.version`,
and 22 checks), whose Workflow and Reconcile are anchored on fields, and
`core-schedules.transcript.json` (D104), 161 steps (131 commands, 19 of
them deadlines and 10 changes, each compared with its row's
`expect.version`, 29 checks and one raw write), each command held to its
`expect.events` by reading every event queue of its database back after
it and deleting what it read; each held to a floor of its step count, its `expect.others`
compared on every command step, with 0 mismatches each. Each command runs over a layout of every
type live in its database, each at the version the scenario last made
live, since a step names its own type's version alone. The Postgres cases read `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`,
each in a schema of its own, and skip without it, saying so.
