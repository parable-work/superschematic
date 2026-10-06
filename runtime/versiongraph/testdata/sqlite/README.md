# SQLite vectors

What every language's SQLite adapter (D32) is held to, so a file one
language's adapter writes reads the same in another's. The TypeScript
adapter is the reference: it wrote these files, and its tests check them
(`typescript/test/sqlite-vectors.test.ts`, and `typescript/test/node.mjs`
under Node).

| File | What it holds |
|---|---|
| `layout.json` | The layout's statements under the default names. |
| `typescript.sql` | A database the TypeScript adapter wrote, as SQL text. |
| `typescript.json` | What `typescript.sql` reads back as. |

## layout.json

`{"statements": [...]}`: exactly the statements `sqliteLayout()` returns
with the default names (`graph_` and the local name, as `graph_ref` and
`graph_ref_live_name`), in order. An adapter's list of its layout under the
default names equals it, string for string.

## typescript.sql

UTF-8 text, one statement per line, each line ending in `;` and `\n`, the
last line too. No literal holds a line break of any kind (no control
character, NEL, U+2028 or U+2029), so splitting the file on `\n` gives its
statements. It holds only:

- the layout's statements, exactly `layout.json`'s, in order, each followed
  by `;`;
- one `INSERT INTO "<table>" (<columns>) VALUES (<literals>);` per row,
  with every column in the table's declaration order (as
  `PRAGMA table_info` lists them) and the columns, like the literals,
  separated by `, `; the tables in the layout's order (`ref`,
  `ref_history`, `commit`, `patch`, `snapshot_entry`, `release`,
  `release_history`, `member`, `member_history`), and each table's rows by
  primary key (`id`, or `history_id` in a history table), in byte order.

A literal is text in single quotes with each `'` doubled and nothing else
escaped, an integer in decimal, or `NULL`; the layout holds nothing else.
There is no pragma and no transaction control. `dumpDatabase` in
`typescript/test/sqlite-vectors.ts` writes it.

Load it into an empty database with foreign keys off, since a ref names
its head commit and the commit names its ref, so no order of inserts
satisfies keys checked at once. Then `PRAGMA foreign_key_check` returns no
rows, and the adapter binds over the database as over any other (on a
connection of its own it turns foreign keys on).

The layout needs SQLite 3.37.0 or later, which added `STRICT` tables. The
adapter's statements need nothing later: `RETURNING` (3.35.0), and
`json_each` and `json_extract`, which SQLite builds in from 3.38.0 and
which 3.37 has in builds with JSON1. No statement uses `->` or `->>`.
Every language's adapter refuses, when it creates its tables or is bound,
a SQLite older than 3.37.0 and one that cannot run `json_each` and
`json_extract`, with an error that says which.

The script that wrote it is `writeDatabase` in
`typescript/test/sqlite-vectors.ts`: `SyncEngine` over the adapter, with
the scenario fixture's descriptor (`testdata/fixture/recipe.json`), at
schema epoch 1 with a snapshot interval of 3, as the scenarios run. Its
clock reads 2026-10-05T09:00:00Z first and moves 1.250005 seconds at each
read; `createTables` takes the first read and stores no time, so the
earliest stored time is 2026-10-05T09:00:01.250005Z. Its ids are version-4
UUIDs from a seeded generator in place of `crypto.randomUUID`, so a rerun
writes the same file. Its actors are `Cook` and `Ann`, and both graphs'
root is `Bread`.

- Graph `recipe`: a primary line, `main`. A change set, `first`, saves a
  row of every kind: a tasting of every value class (an integer wider than
  a double, a number written `1e21`, text outside ASCII and outside the
  Basic Multilingual Plane), a utensil whose entity key the adapter
  generates, a note without its optional parent and one whose body holds
  an apostrophe and a tab, a step with the column its history excludes.
  It commits, merges into
  `main` as tagged commit 1, which the release points at, and is sealed. A
  second change set, `second`, updates a step, tombstones an ingredient and
  adds a step, commits, updates the step again with a partial row and
  unsets the added step as another actor, so its DELETE image names that
  actor, commits without a message, and merges as tagged commit 2, which
  the release moves to. It then saves a partial row it does not commit. A
  draft, `scrap`, saves and is discarded.
- Graph `menu`, where the utensil kind gains a content column, `name`.
  Its first writes run over a descriptor that is the fixture's less
  `utensil.name`: a primary line, a change set, `today`, that saves a step
  and a utensil, `Knife`, and commits, and a tagged merge, `lunch`, which
  its release points at. The rest runs over the fixture's own descriptor:
  a change set, `dinner`, names `Knife` and adds `Board`, commits, and
  merges as tagged commit 2, `supper`; the release stays at `lunch`.

Two of the script's writes are partial rows that only SQLite takes for
this fixture (D32, amended: on an insert the SQLite adapter stores null
for a column the row lacks, where Postgres applies the column's default or
refuses a `NOT NULL` column without one). Postgres refuses the partial
update of `Knead`, since the Postgres adapter writes a row as `INSERT ...
ON CONFLICT DO UPDATE` and the proposed row is checked against
`step.position`'s `NOT NULL` before the conflict is found, and the
uncommitted partial tasting, since `tasting.taster` is `NOT NULL`. A
replay of the script on Postgres gives those two rows in full.

### A kind that gains a column

The file is read with the fixture's descriptor, which declares
`utensil.name`, so `menu`'s rows and images written before the gain lack
it, as D32, amended, has a Postgres row added before an `ADD COLUMN`
without a `DEFAULT`:

- A live row reads with every column its kind declares: `today`'s
  `Knife`, stored as `{}`, reads with `"name": null` from `rows`, and so
  does it in `compose` of `today`.
- An image reads as it was stored: the images of `Knife` taken before the
  gain lack `name`, in `images` and in the trees `materialize` and
  `released` read from them.
- The core reads a content column a row lacks as null, and hashes a
  declared content column that is null as an absent one, so a tree
  hashes the same however it is read, under either descriptor:
  `compose` of `today` and `materialize` of its head give one hash. A
  commit recorded before the gain keeps the hash it was recorded with,
  which `readCommit` returns, and `materialize` of `lunch`, and
  `released`, give that hash too.

## Stored forms

What the adapter writes, beyond the table and column names and types the
layout gives. Ids are UUIDs in canonical form (base62), and times in an
`INTEGER` column are microseconds since the Unix epoch.

- `ref`: `sealed_at`, `deleted_at` and `deleted_by` are `NULL` until a seal
  or a discard sets them. A discard sets `deleted_at`, `deleted_by` and
  `_version` only; every other change sets `updated_at` and `updated_by`.
- `commit`: `message` is `NULL` for none, `sequence` `NULL` for an untagged
  commit.
- `member`: `tombstone` is 0 or 1, and `data` is a JSON object of every
  column the kind declares but its role columns (id, entity key, ref,
  root, tombstone, version), each value canonical: on an insert every such
  column, `null` where the row lacks it; on an update the stored object
  with the row's columns over it, and `null` for a column the kind
  declares that the stored object lacks.
- Times: an `INTEGER` time is a whole number of microseconds within
  ±(2^53−1), which a double holds exactly. The adapter refuses a clock that
  returns anything else and a stored time outside that range, as it does
  any `INTEGER` it reads, and a time it renders as a date-time outside the
  years 0000 to 9999. Every language's adapter keeps the same range.
- `ref_history`, `release_history` and `member_history`: `history_id` is a
  new id, `recorded_at` the transaction's time, `operation` `INSERT` for a
  row's first image (version 1), `DELETE` for a member row's removal, and
  `UPDATE` for every other change, a ref's discard among them.

Every `data` column is a JSON object written compactly with its members
sorted by name, by code point, and strings escaped as canonical JSON
escapes them (`runtime/versiongraph/README.md`, "Canonical rows"):

| Table | `data` |
|---|---|
| `ref_history` | The ref after the change: `_version` (an integer), `base_commit_id`, `created_at`, `created_by`, `deleted_at`, `deleted_by`, `head_commit_id`, `id`, `name`, `parent_ref_id`, `root_id`, `sealed_at`, `updated_at`, `updated_by`. Every member is present; an id or a time it lacks is `null`, and a time is a canonical date-time (`2026-10-05T09:00:01.250005Z`). |
| `release_history` | The release pointer after the change: `_version`, `commit_id`, `created_at`, `created_by`, `id`, `root_id`, `updated_at`, `updated_by`, as a ref's image writes them. |
| `member_history` | The kind's canonical row after the change, role columns included under the descriptor's names (`recipe_id` is the fixture's root column, `root_id` under `Branches`), less the kind's `history.exclude` columns. A `DELETE` image is the row as it was, at its version plus 1, with the kind's `history.actor` column, when it has one, set to the delete's actor. |

## Writing the same file

Another language's adapter reproduces `typescript.sql` byte for byte by
running the script below with the same clock and ids, over an empty
in-memory database, and dumping it as `typescript.sql` is written.

**The clock.** One clock serves every adapter of the script. Its first
read returns 1791190800000000 (2026-10-05T09:00:00Z in microseconds), and
each read after it returns 1250005 more. An adapter reads it once per
transaction it begins, right after `BEGIN IMMEDIATE`, and a transaction
begun inside another (a savepoint) reads nothing. `createTables` is one
transaction and takes the first read, and each engine operation is one
transaction, so the script's 28 operations take the 28 reads after it, in
order.

**The ids.** For its whole run the script replaces the system's random
UUID function with a generator, and restores it however the run ends.

- The generator is splitmix64 over a 64-bit state that starts at
  `0x5eedd32a`. Each output adds `0x9e3779b97f4a7c15` to the state, modulo
  2^64, and returns `z ^ (z >> 31)`, where `z` is the new state after
  `z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9` and then
  `z = (z ^ (z >> 27)) * 0x94d049bb133111eb`, each product modulo 2^64.
- One UUID takes two outputs, `a` then `b`: `n = a * 2^64 + b`, so `a`
  holds the high 64 bits. Its version, bits 76 to 79 counting the least
  significant bit as 0, is set to `0100`, and its variant, bits 62 and 63,
  to `10`: `n = (n & ~(0xf << 76)) | (0x4 << 76)`, then
  `n = (n & ~(0x3 << 62)) | (0x2 << 62)`.
- The generator returns `n` as 32 lowercase hex digits, zero-padded and
  hyphenated 8-4-4-4-12, and the adapter stores its canonical form: `n` in
  base62 with the digits `0-9A-Za-z`, most significant first, with no
  leading zero (`0` for the nil UUID).
- The first id the script draws is `22T1h0AtgGb8eIEMumeEtc`, `recipe`'s
  `main`, and the second its first history image's.

Each id is one draw, so the order of the draws fixes every id. The adapter
draws, for each storage call:

| Call | Ids drawn, in order |
|---|---|
| `createRef` | the ref's id, then its history image's |
| `updateRef`, `discardRef` | the history image's |
| `upsertRow` of a new row | the row's id, then its entity key when the row has none, then its history image's |
| `upsertRow` of a row the ref has | the history image's |
| `removeRow` | the DELETE image's, when the ref has the row |
| `insertCommit` | the commit's id |
| `insertPatches` | one per patch, in the order given |
| `insertSnapshot` | one per entry, in the order given |
| `writeRelease` at version 0 | the pointer's id, then its history image's |
| `writeRelease` at another version | the history image's |

No other call draws an id. The storage calls an operation makes, their
order and the order of a call's patches and entries are the engine's (a
snapshot's entries are sorted by kind, then entity key), so a
reproduction holds the engine to the TypeScript engine's calls as well as
the adapter to these draws.

**The operations.** Each runs on an engine at schema epoch 1 with a
snapshot interval of 3. Every write through a ref passes the version the
previous call on that ref returned, a merge passes no resolutions, and a
row is the JSON text given. A save applies every kind's upserts, then
every kind's deletes, then every kind's unsets, each in descriptor order,
and a kind's rows in the order given.

On an adapter of graph `recipe` over the fixture's descriptor:

0. `createTables`.
1. `createPrimary(Cook, Bread, "main")`: `main`.
2. `branch(Cook, main, "first")`: `first`.
3. `save(Cook, first)`:
   - cover upsert `{"entity_key":"Cover","photo_url":"https://example.com/bread.jpg"}`;
   - ingredient upsert `{"entity_key":"Flour","step_key":"Knead","quantity":"500 g","substitutes":[{"name":"spelt","ratio":1}]}` and `{"entity_key":"Salt","step_key":"Knead","quantity":"10 g","substitutes":null}`;
   - note upsert `{"entity_key":"Note","body":"Proof overnight\tif there's time"}` and `{"entity_key":"Reply","body":"Agreed","reply_to":"Note"}`;
   - step upsert `{"entity_key":"Knead","position":1,"instruction":"Knead for ten minutes","timings":{"knead":"10m"},"scratch":"floury"}` and `{"entity_key":"Bake","position":2,"instruction":"Bake at 230 C","timings":{"bake":"35m","preheat":"30m"}}`;
   - tasting upsert `{"entity_key":"First","taster":"Ann","salty":true,"score":4.5,"servings":9007199254740993,"tasted_on":"2026-09-01","tasted_at":"2026-09-01T10:00:00.12Z","served_at":"18:30:00","rested":"1h30m0s","verdict":"again","remarks":{"crust":[1,2.50],"crumb":"open"},"tags":["sour","a \"quoted\" tag","crème brûlée 🍞"],"helpers":["Bob","Cy"],"bites":[[1,2],[3]]}` and `{"entity_key":"00000000-0000-0000-0000-000000000002","taster":"00000000-0000-0000-0000-00000000000a","salty":false,"score":1e21,"servings":-3,"tasted_on":"2026-02-28","tasted_at":"2026-09-01T12:30:00+02:30","served_at":"2:30 pm","rested":"-1m30.5s","verdict":"never","remarks":null,"tags":[],"helpers":["00000000-0000-0000-0000-00000000003d"],"bites":[]}`;
   - utensil upsert `{"name":"Bowl"}`.
4. `commit(Cook, first, message "first draft")`.
5. `merge(Cook, first into main, message "first", tagged)`: tagged commit 1.
6. `release(Cook, Bread, tagged commit 1, version 0)`.
7. `seal(Cook, first)`.
8. `branch(Ann, main, "second")`: `second`.
9. `save(Ann, second)`: ingredient delete `Salt`; step upsert `{"entity_key":"Knead","position":1,"instruction":"Knead for twelve minutes","timings":{"knead":"12m","rest":"5m"},"scratch":"sticky"}` and `{"entity_key":"Proof","position":3,"instruction":"Proof for an hour","timings":{"proof":"1h"}}`.
10. `commit(Ann, second, message "second draft")`.
11. `save(Cook, second)`: step upsert `{"entity_key":"Knead","instruction":"Knead until smooth"}`, step unset `Proof`.
12. `commit(Cook, second)`, with no message.
13. `merge(Ann, second into main, message "second", tagged)`: tagged commit 2.
14. `release(Ann, Bread, tagged commit 2, version 1)`.
15. `save(Ann, second)`: tasting upsert `{"entity_key":"First","score":5}`.
16. `branch(Cook, main, "scrap")`: `scrap`.
17. `save(Cook, scrap)`: cover delete `Cover`; utensil upsert `{"entity_key":"Whisk","name":"Whisk"}`.
18. `discard(Cook, scrap)`.

On an adapter of graph `menu` over the fixture's descriptor less
`utensil.name` (the column removed from the utensil kind's `columns`), with
no `createTables`:

19. `createPrimary(Cook, Bread, "main")`: `main`.
20. `branch(Cook, main, "today")`: `today`.
21. `save(Cook, today)`: step upsert `{"entity_key":"Knead","position":1,"instruction":"Slice","timings":{}}`; utensil upsert `{"entity_key":"Knife"}`.
22. `commit(Cook, today, message "today")`.
23. `merge(Cook, today into main, message "lunch", tagged)`: commit `lunch`.
24. `release(Cook, Bread, lunch, version 0)`.

On an adapter of graph `menu` over the fixture's descriptor:

25. `branch(Ann, main, "dinner")`: `dinner`.
26. `save(Ann, dinner)`: utensil upsert `{"entity_key":"Knife","name":"Bread knife"}` and `{"entity_key":"Board","name":"Bread board"}`.
27. `commit(Ann, dinner, message "dinner")`.
28. `merge(Ann, dinner into main, message "supper", tagged)`.

## typescript.json

What the database reads back as, through an adapter opened over it with
each graph's name and the default table names, and the engine over that
adapter with the fixture's descriptor at schema epoch 1:

```
{"graphs": [graph, ...]}
graph:  {"graph", "refs": [ref, ...], "commits": [commit, ...], "roots": [root, ...], "images": [image, ...]}
ref:    {"id", "readRef": Ref, "rows": {kind: [row, ...], ...}, "compose": TreeResult or Error, "history": [Commit, ...] or Error}
commit: {"id", "readCommit": Commit, "materialize": TreeResult, "patches": [Patch, ...], "snapshot": [SnapshotEntry, ...]}
root:   {"root", "released": {"release": Release, "tree", "contentHash", "findings"}}
image:  {"kind", "id", "version", "image": row}
```

| Value | Members |
|---|---|
| `Ref` | `id`, `root`, `parent`, `base`, `head`, `name`, `sealed`, `discarded`, `version` |
| `Commit` | `id`, `root`, `ref`, `parent`, `message` (`""` for none), `schemaEpoch`, `contentHash`, `sequence` (`null` untagged), `createdAt` (a canonical date-time), `createdBy`, `snapshot` |
| `Patch` | `commit`, `kind`, `entityKey`, `entityId`, `entityVersion`, `operation` |
| `SnapshotEntry` | `kind`, `entityKey`, `entityId`, `entityVersion` |
| `Release` | `id`, `root`, `commit`, `version` |
| `TreeResult` | `tree` (`{kind: [row, ...]}`, a kind without rows absent), `contentHash`, `findings` (each `{code, kind, entityKey?, message}`) |
| `Error` | `{"error": code}`: the engine's error code, where the read fails |

An absent id (a ref's `parent`, `base` or `head`, a commit's `parent`) is
`null`, which an engine whose type has no null, as Go's `""`, reads as its
own none. Every id is a UUID in its canonical form (base62).

Each entry is one read, and what it must return:

| Entry | The read |
|---|---|
| `readRef` | `readRef(id)` |
| `rows[kind]` | `rows(kind, id)`, sorted by entity key; every kind of the descriptor is listed, `[]` for none |
| `compose` | the engine's `compose(id)`; a discarded ref's is `{"error": "not_found"}` |
| `history` | the engine's `history(id)`, nearest first; a discarded ref's is `{"error": "not_found"}` |
| `readCommit` | `readCommit(id)` |
| `materialize` | the engine's `materialize(id)` |
| `patches` | `patches([id])`, sorted by kind, then entity key |
| `snapshot` | `snapshot(id)`, sorted by kind, then entity key |
| `released` | the engine's `released(root)` |
| `image` | `images(kind, [{id, version}])`, which returns `[image]` |

`images` lists every image `member_history` holds for the graph. Graphs are
sorted by name, refs and commits by id, roots by id, images by kind, id and
version, and every string by code point, which for the ids is byte order.
A tree's kinds are sorted by name and its rows are in the engine's order
(by the kind's order column, then by entity key).

Every row, in `rows`, a tree or an `image`, is written as its exact
canonical text, compact on one line, so a reader can compare its bytes (Go's
`json.RawMessage`, serde_json's `RawValue`) or compare JSON values with
every number read exactly: a tasting's `servings` is 9007199254740993,
which a double does not hold.

Through each graph's adapter, nothing of another graph reads: each ref and
commit id listed under another graph is `not_found` to `readRef`,
`readCommit` and the engine's `compose` and `materialize`; that ref's rows
of every kind, that commit's patches and snapshot, and each image listed
under another graph read as none.

## Rewriting

```
cd runtime/versiongraph/typescript
bun run build
UPDATE_SQLITE_VECTORS=1 bun test test/sqlite-vectors.test.ts
```

rewrites all three files from the script and the adapter in `dist/`;
review the diff. Without the variable the tests fail when `sqliteLayout()`
or what the script writes, or reads back, differs from the files.
