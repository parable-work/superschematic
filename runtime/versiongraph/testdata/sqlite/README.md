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

UTF-8 text, one statement per line, each ending in `;`. No literal holds a
line break of any kind (no control character, NEL, U+2028 or U+2029), so
splitting the file on `\n` gives its statements. It holds only:

- the layout's statements, exactly `layout.json`'s, in order;
- one `INSERT INTO "<table>" (<columns>) VALUES (<literals>);` per row,
  naming every column, the tables in the layout's order (`ref`,
  `ref_history`, `commit`, `patch`, `snapshot_entry`, `release`,
  `release_history`, `member`, `member_history`) and each table's rows by
  primary key (`id`, or `history_id` in a history table), in byte order.

A literal is text in single quotes with each `'` doubled, an integer in
decimal, or `NULL`; the layout holds nothing else. There is no pragma and
no transaction control.

Load it into an empty database with foreign keys off, since a ref names
its head commit and the commit names its ref, so no order of inserts
satisfies keys checked at once. Then `PRAGMA foreign_key_check` returns no
rows, and the adapter binds over the database as over any other (on a
connection of its own it turns foreign keys on).

The script that wrote it is `writeDatabase` in
`typescript/test/sqlite-vectors.ts`: `SyncEngine` over the adapter, with
the scenario fixture's descriptor (`testdata/fixture/recipe.json`), at
schema epoch 1 with a snapshot interval of 3, as the scenarios run. Its
clock reads 2026-10-05T09:00:00Z first and moves 1.250005 seconds at each
read, and its ids are version-4 UUIDs from a seeded generator in place of
`crypto.randomUUID`, so a rerun writes the same file. Its actors are
`Cook` and `Ann`, and both graphs' root is `Bread`.

- Graph `recipe`: a primary line, `main`. A change set, `first`, saves a
  row of every kind: a tasting of every value class (an integer wider than
  a double, a number written `1e21`, text outside ASCII), a utensil whose
  entity key the adapter generates, a note without its optional parent, a
  step with the column its history excludes. It commits, merges into
  `main` as tagged commit 1, which the release points at, and is sealed. A
  second change set, `second`, updates a step, tombstones an ingredient and
  adds a step, commits, updates the step again with a partial row and
  unsets the added step as another actor, so its DELETE image names that
  actor, commits without a message, and merges as tagged commit 2, which
  the release moves to. It then saves a partial row it does not commit. A
  draft, `scrap`, saves and is discarded.
- Graph `menu`: a primary line, a change set, and a tagged merge, which
  its release points at.

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
