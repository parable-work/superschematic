# Migration runner in Rust

`superschematic migrate plan` writes a plan: the ordered steps that take a
DB service's database from one version of its schema to the next (D27 in
`docs/DECISIONS.md`). This crate applies a plan to a SQLite file from
inside the process that serves the file. It never computes a plan, so a
binary that links it needs its plans and no compiler.

It is the second runner (D116, "Migrations a binary carries"). The first is
the Go module beside it, `../go`, whose binary a deploy or a job runs.
`../README.md` is the contract both follow: the plan document, canonical
JSON and the hashes over it, the two state tables, and what `apply`,
`status` and `adopt` do. Both runners keep the same tables with the same
values, so either goes on from a file the other migrated.

## How it differs from the Go runner

- It runs `sqlite` plans alone, over rusqlite and the SQLite rusqlite
  bundles. It reads a `postgres` plan, so that it can refuse it by name. It
  has no Postgres or D1 driver, no jobs, no queues and no describe, and
  `adopt` has no `--diff`.
- It is a library, not a binary. A host calls it on a connection, or calls
  `start` on a file.
- It adds the chain start, the copy before a contract step and the
  adoption of a file built by hand, all below.
- It reads JSON with its own decoder, written to accept and refuse what
  Go's `encoding/json` does, since the hashes are over what Go writes. Two
  differences: it refuses nesting deeper than 512 arrays and objects, where
  Go refuses 10000, and it takes a member's name as the contract writes it,
  where Go matches it without regard to case.

## Use

Canonical JSON and the hashes:

```rust
let canonical = superschematic_migrate::canonical_json(doc)?;
let hash = superschematic_migrate::hash(canonical.as_bytes());
```

One plan on one connection, as `superschematic-migrate apply` runs it:

```rust
use superschematic_migrate::{read_plan, Phase, Runner};

let plan = read_plan(doc)?;
let conn = rusqlite::Connection::open("shop.sqlite")?;
let mut runner = Runner::new(&conn)?.log(|line| println!("{line}"));
let applied = runner.apply(&plan, Phase::All)?;
```

`Runner::new` sets the connection's `busy_timeout` to five seconds and
turns foreign keys on. `apply` takes `Phase::Expand`, `Phase::Contract` or
`Phase::All`; `status` reads a service's state and creates no table;
`adopt` records a model as applied without running anything. A refusal is
`Error::Refused`, with the Go runner's words wherever the Go runner refuses
the same thing. A failed step is `Error::Step`, and the next `apply` of the
same plan resumes at it.

## The chain start

A binary that carries its plans calls `start` on each database file before
it serves a request:

```rust
use superschematic_migrate::{start, Adoption, StartOptions};

let plans: [&[u8]; 2] = [
    include_bytes!("plans/0001.plan.json"),
    include_bytes!("plans/0002.plan.json"),
];
let started = start("databases/shop.sqlite", &plans, StartOptions::default())?;
```

`plans` are the plans of one database, in order: each starts from the model
the one before it ends at. `start` refuses any other chain before it opens
the file. It opens a connection of its own, with foreign keys on and a busy
timeout of five seconds, and leaves the journal mode as it is unless
`StartOptions::wal` asks for WAL. Then it decides from the file's state:

| The file | What `start` does |
| --- | --- |
| is at the last plan's `to` | Nothing. |
| is at a model a carried plan starts from | Applies every plan from that one on, each plan's expand and contract steps back to back, each step in a transaction. |
| has a carried plan in progress | The same, resuming that plan at its step. |
| has no tables and no state | The same, from the first plan. |
| has tables, no state, and an adoption whose check answers | Records the adoption's model as applied, once, and goes on from it. |
| anything else | Stops, and changes nothing. |

A file at a model no carried plan starts from, or with a plan in progress
that is none of them, is `Error::UnknownModel`, which carries the hash of
the model the file is at and the hash of the model the carried plans end
at: the file is a newer build's, or another app's.

A step that fails stops the start with `Error::ChainStep`, which carries
the plan's hash and the step's index, operation and subject. The steps
before it stay applied, and the next start goes on from it.

`start` returns what it did: the plans it applied with the steps it ran of
each, whether it adopted, the copy it made, and the model the file is at.

### The copy before a contract step

Before the first step of plans that hold a contract step, `start` copies
the file through SQLite's online backup to `<file>.before-<hash>`, where
`<hash>` is the applied model the plans start from, and removes every
older `<file>.before-*`, so the newest copy is the only one. It writes the
copy as `<file>.before-<hash>.partial` and renames it, so a start that dies
while it copies leaves no copy that looks whole.

It makes no copy when no contract step will run, when the file has no
applied model, since it holds nothing to lose, and when it resumes a plan
whose contract has already started: the copy an earlier start made is the
one from before that contract, and it stays.

### Adoption

An environment's settings may name one adoption per database, for a file
an earlier version of the app built by hand:

```rust
let options = StartOptions {
    adoption: Some(Adoption {
        model: include_bytes!("adopt.model.json"),
        check: "SELECT user_version = 7 FROM pragma_user_version",
    }),
    ..StartOptions::default()
};
```

`model` is a model a carried plan starts from, or the model they end at;
`start` refuses any other before it opens the file. `check` is one
statement that reads the file. A file with tables and no state is adopted
when the check answers exactly one row that holds the integer 1 and nothing
else. Otherwise the start is refused and the file is not changed: with no
adoption, with a check that answers anything else, with a check SQLite
cannot run on the file, and with a check that writes. A file that has state
is never adopted again, and an empty file starts from the first plan.

## Layout

| Path | What it holds |
| --- | --- |
| `src/json.rs` | The JSON decoder and the canonical encoder |
| `src/canonical.rs` | `canonical_json`, `hash`, `plan_hash` |
| `src/plan.rs` | `read_plan` and `read_model`, with the Go runner's checks |
| `src/state.rs` | The state tables and their statements |
| `src/runner.rs` | `Runner`: `apply`, `status`, `adopt` |
| `src/start.rs` | `start`: the chain start, the copy and the adoption |
| `src/error.rs` | `Error` and `StepError` |
| `tests/` | The Go runner's tests, on the Go runner's test data |

## Tests

```sh
export RUSTUP_TOOLCHAIN=1.99.0
cargo fmt --check
cargo clippy --all-targets -- -D warnings
cargo test
```

The tests read the Go runner's test data where it lives and copy none of
it: the canonical JSON vectors (`../go/testdata/canonical.json`), the
hand-written plans (`../go/testdata/sqlite`) and the plans the compiler
writes for the runners (`../testdata/plans/sqlite-*`). `tests/runner.rs`
and `tests/vectors.rs` follow the Go runner's tests one for one; every test
runs on SQLite files in a temporary directory.

`tests/parity.rs` runs this runner and the Go runner on one file: the first
plans of a chain by one and the rest by the other, a plan's expand steps by
one and its contract by the other, a plan stopped here and resumed there,
and a model one adopts and the other goes on from. Each file must end with
the state rows, the log rows and the schema one runner alone leaves. It
needs the Go runner's binary, which `SUPERSCHEMATIC_MIGRATE_BIN` names;
without the variable its tests return at once and print why.

```sh
(cd ../go && go build -o "$TMPDIR/superschematic-migrate" ./cmd/superschematic-migrate)
SUPERSCHEMATIC_MIGRATE_BIN="$TMPDIR/superschematic-migrate" cargo test --test parity
```
