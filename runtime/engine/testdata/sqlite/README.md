# Engine SQLite corpus

A file the TypeScript engine wrote, with every core behavior and every
work-queue behavior in use, as SQL text, and the reads expected from it. D58
moves the engine to Rust over SQLite and Postgres; a file the TypeScript
engine wrote must open in the Rust engine and migrate. The Rust engine loads
`typescript.sql` into an empty file, opens it as the engine opens any file
(bringing it up to its migrations: Comments' second, which moves its
comments into threads one level deep, D77), and must answer every read in
`expected.json` as recorded. `typescript.sql` is frozen: it is the last file
the TypeScript engine wrote, before the cutover (milestone 14) removed it.
`expected.json` was that engine's reads of it, and changes only where the
engine's own migrations change what a read of the file returns: since D77,
Comments' fields and `listMessages`.

| File | What it holds |
|---|---|
| `typescript.sql` | The file, as SQL text. |
| `expected.json` | The reads of it, migrated, and what each returns. |
| `written.json` | The same reads of the file the script below writes on the engine today. |

`runtime/engine-workqueue/typescript/test/sqlite-corpus.ts` wrote the first
two with the TypeScript engine, and `test/sqlite-corpus.test.ts` there now
holds the engine, through `@superschematic/engine`, to them: `typescript.sql`
loaded into a file opens, migrates and reads as `expected.json` says; the
script below, run on the engine on a file of its own, writes one that reads
as `written.json` says, which is `expected.json` but for Comments, since
the script's posts are beside the instance (D77) where the TypeScript
engine's comments took a sequence; and the text loaded into a file dumps
the same text. `UPDATE_ENGINE_CORPUS=1 node --test
test/sqlite-corpus.test.ts` rewrites both JSON files from the engine;
review the diff. The Rust test kit opens the file too
(`testkit/tests/core_branches.rs`).

## The script

`writeCorpus` opens an engine on a new file with:

- the core's behaviors and the work queue's (`workQueueBehaviors`);
- `policy: allowAll`;
- the namespaces `tree` and `billing` besides `default`;
- the runner, acting as `runner` (who holds `jobs.override`);
- `values: { thresholdBytes: 1024 }`, so a note's long body goes to the
  value store;
- a fixed clock, which starts at 2026-10-09T09:00:00Z (1791536400000) and
  which the script moves; and
- ids from a seeded generator, which `crypto.randomUUID`, which the version
  graph draws from, also gives while the script runs: the n-th id, from 1,
  is the version-4 UUID made of the first 16 bytes of
  SHA-256(`superschematic-engine-sqlite/<n>`), its version and variant bits
  set. So a rerun writes the same file.

It publishes the fixture documents the core binary builds in the CLI smoke
(`internal/loader/testdata/services/fixture-*-json`): in `default`,
`documents` (Workflow, Comments, Revisions), `tasks` (Workflow, Dependencies,
Links), `projects` (Workflow, Rollups), `notes` (Search), `Step` (Constants,
Variants), `Recipe` (Branches), `invoices` (Workflow, StateRules),
`expenses` (Workflow, Approvals), `requests` (Workflow, Timers), `jobs`
(Workflow, Lease, Assignment, Queue, Budget, Retries), and the work queue's
`workers` (Workflow, Presence), `steps` (Workflow, Constants, Dependencies,
Links) and `batches` (Blueprint); in `tree`, `projects` (Workflow, Links,
Reactions); in `billing`, `documents`, `tasks`, `projects` and `invoices`
(Links, Numbering). Then it writes:

- a document commented on (posted to, on today's engine, and replied to in
  the post's thread), revised through an approved proposal, and published;
- tasks of a project that wait on a task and a document, link to them, one
  dropped, and the project rolling them up;
- three notes, one whose body goes to the value store, and a vector for one;
- a step that keeps its kind and has a result of the kind's shape;
- a recipe whose draft saves, commits and merges into its primary line as a
  tagged commit, which is released;
- an invoice sent and paid;
- an expense claim signed off by two approvers and a payer;
- two requests, one escalated by the runner's sweep four hours on, one
  waiting with its lapse snoozed;
- in `tree`, a project tree whose children start and finish their parent,
  on the runner;
- jobs claimed in order by two workers, one assigned, usage recorded, an
  attempt recorded, leases renewed and released, and the runner putting a
  job whose time ran out back in the queue; a worker that beat; two batches
  that stamp their steps;
- a namespace, `scratch`, created and archived;
- in `billing`, invoices numbered in their projects' counts on 2026-12-31,
  and one on 2027-01-01 (1798761600000), where the clock ends.

It ends with every behavior's storage in the file. The ledger,
`engine_migrations`, holds each owner's migrations: the engine's up to 12,
and each behavior's up to its last when the TypeScript engine wrote it
(Workflow 3, Comments 1, Revisions 3,
Dependencies 1, Links 3, Search 2, Branches 1, Approvals 1, Timers 1, Lease
4, Assignment 2, Queue 2, Budget 1, Retries 2, Presence 2, Blueprint 1,
Numbering 1). Constants, Variants, Rollups, Reactions and StateRules keep no
storage of their own; Reactions' is the runner's subscriptions.

## typescript.sql

UTF-8 text, one statement per line, each ending in `;` and `\n`, the last
too. No statement holds a line break of any kind, so splitting the file on
`\n` gives its statements. In order:

1. The schema, from `sqlite_master` in the order SQLite keeps it (`ORDER BY
   rowid`), each statement's text as SQLite keeps it with every run of white
   space outside quotes made one space (none after `(` or before `)`) and a
   `--` comment left out: every
   table, index and virtual table, but the triggers, `sqlite_sequence`, the
   indexes SQLite makes itself, and the tables FTS5 keeps a virtual table's
   index in (`<table>_data`, `_idx`, `_content`, `_docsize`, `_config`).
2. Every row of every table of step 1, the tables in that order: one
   `INSERT INTO <table> (<columns>) VALUES (<literals>);` each, every column
   in declaration order and separated, like the literals, by `, `. A rowid
   table's rows come in rowid order, and its `rowid` is the first column
   when no `INTEGER PRIMARY KEY` holds it, since the engine reads some
   tables in rowid order (`engine_references`); a virtual table's rows are
   inserted through it, with their rowids, so FTS5 builds its index again.
3. `DELETE FROM sqlite_sequence;`, then `INSERT INTO sqlite_sequence (name,
   seq) VALUES (...);` per row, by name: an `AUTOINCREMENT` table's counter,
   which a delete leaves past its rows (an event cursor is never reused).
4. The triggers (`engine_events` is append-only), last, so loading the rows
   fires none.

A literal is SQLite's `quote()` of the value: text in single quotes with
each `'` doubled, an integer in decimal, a BLOB as `X'<hex>'`, or `NULL`. A
text that holds a control character (U+0000 to U+001F, U+007F), U+0085,
U+2028 or U+2029 is `CAST(X'<its UTF-8 in hex>' AS TEXT)` instead, and a
REAL is written with 17 significant digits (`printf('%.17g')`), with `.0`
when that has no point or exponent; today's file holds neither.

Load it into an empty file with foreign keys off, since a row may name one
inserted after it (the version graph's ref names its head commit, and the
commit its ref), in one transaction; then `PRAGMA foreign_key_check` returns
no rows. The tables need SQLite 3.37.0 or later (`STRICT`), FTS5 and the JSON
functions. The file was written by SQLite 3.51 under Node.js and Bun, which
dump it alike.

## expected.json

```json
{
  "name": "typescript",
  "description": "...",
  "open": {"policy": "allowAll", "namespaces": {"names": ["tree", "billing"]}, "runner": {"principal": "runner"}, "values": {"thresholdBytes": 1024}},
  "clock": 1798761600000,
  "principals": {"alice": {"subject": "alice", "permissions": []}, ...},
  "reads": [{"principal": "alice", "call": "namespaces.list", "args": [], "expect": [...]}, ...]
}
```

`open` is the options to open the loaded file with, besides the core's
behaviors and the work queue's (the principal of `runner` is the
principals' entry by that name); the clock reads `clock` throughout. A read
is a step of `../scenarios`: the principal by name in `principals` (absent
for `runner.status`, which takes none), the call, its arguments and what it
returns, as JSON, in the order the reads run. The reads are: the namespaces;
for each namespace, `default`, `tree`, `billing` and `scratch`, its schemas,
each schema's live version and its instances (`limit: 500`), and its event
log, a page of 500 at a time; the read operations of the behaviors that have
them on the instances the script wrote (`listMessages`, of the top-level
messages and of the first thread, `listRevisions`,
`listProposals`, `listBlockers`, `listDependents`, `listSignOffs`, `refs`,
`releases`, `released`, `checkReserve`); Search's `search` (`"dialect":
"sqlite"`: it ranks by FTS5's bm25, so a replayer on Postgres makes the call
and does not compare) and `staleEmbeddings`; Queue's `countClaimable`; the
long note read with `valueRefs` and its value by hash; and `runner.status`.
