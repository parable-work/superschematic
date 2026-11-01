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
contract.go          the core's JSON: TypeSpec, Command, Action, Answers, Step, Outcome, ChangeSet, RowWrite
layout.go            the layout document internal/generator/behaviorlayout writes: Layout, ParseLayout, MustParseLayout
store.go             Store, the caller's transaction: Dialect, LockMode, Statement
adapter.go           Host, Execute, RunOperation, RunCreate, RunUpdate, RunDelete, PrepareCreate, ReadRow
subtables.go         a use's sub-table rows: the subRows read, the change set's writes, ReadSubRows
deadlines.go         a table's deadline queue: arms and clears, the spend, RunDeadline, Deadline, Claim, ReadDeadlines
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
  rows alone, each column converted to the core's JSON, a NULL absent),
  `hasRows` from the table, `spec` from the layout, and `subRows` from the
  use's sub-table in the layout (`Use.Tables`), in key order, each
  recording the locks held when it was made; it takes each lock once. On
  SQLite (fenced locks) a `subRows` read made under a lock is refused as
  `internal`: the unit has no check of it yet.
- It applies a change set as one unit (`Store.Apply`): a create's insert
  after a check that the table holds no row of the key (`conflict`), an
  update's set and `_version = _version + 1`, a delete, each held to its
  key, the version the change set expects and, for a soft-deletable
  table, the row's being live. A unit that finds a row moved applies
  nothing, and the command runs again from fresh state, `MaxAttempts` (8)
  times, before it is refused as `busy`. After the row writes come a
  deadline command's spend of its arming, the sub-table rows (an insert
  `ON CONFLICT (row, key) DO NOTHING`, an update and a delete, each held to
  one row), the deadline changes, messages of the layout's
  `Table.Deadlines` (an arm deletes the key's messages, a dead one among
  them, and inserts one due at its time; a clear deletes them), and, for a row
  the change set deletes, every message of the row and, on a hard delete,
  its sub-table rows; a deleted row's sub-table writes and deadline
  changes are skipped. A run still asking after `MaxRounds` (256) is
  `internal`, as are a kind of read, step, outcome or write the adapter
  does not know, a type the layout lacks, and outbox events until the
  outbox has a table of its own (D103's order of the work, step 3).

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
them), and `SoftDelete` the columns a delete stamps in place of deleting.
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
(`WhyClaimLost`, `WhyGone`). With a worker's claim (`Claim{ID, Token}`),
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
them, is the error, nothing applied. Whether a
deadline is still armed is this check, never the core's. A generated Go
ORM's repository wraps it as `RunDeadline(ctx, message, claim)` in its
transaction, and a generated API's behavior worker calls that for each
claimed message. Values passed as
`any` are what `encoding/json` writes: a date-time as epoch milliseconds,
a UUID as its base62 wire form; `TimeOfMillis` reads a date-time back.

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
a moved row and a gone one. `transcript_test.go` replays both transcripts
in `runtime/engine/testdata/rules` on SQLite and on Postgres, as the
TypeScript adapter does: `core-workflow.transcript.json`, 16 scenarios, 96
steps (34 commands, 61 checks, one raw write), held to a floor of 96, with
the 62 steps it leaves out for its own reasons; and
`core-timers.transcript.json`, 18 scenarios, 101 steps (51 commands, 11 of
them deadlines, and 50 checks), held to a floor of 101, with 322 left out;
0 mismatches each. The Postgres cases read `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`,
each in a schema of its own, and skip without it, saying so.
