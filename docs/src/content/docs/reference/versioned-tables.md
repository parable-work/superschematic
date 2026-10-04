---
title: Versioned tables
description: The @versioned and @optimistic decorators on DB tables; the history table, triggers and prune function the sql generator writes, the history readers and fenced writes the ORM generates, and what verification refuses.
sidebar:
  order: 7
---

`@versioned` keeps every version of a table's rows. Each row carries a
`_version` that every update bumps, and a history table records the row
as stored after each insert and update, and a tombstone for each delete.
The ORM reads history back and fences writes on a row's version.
`@optimistic` gives a table the version and the fenced writes without the
history.

Both decorators come from `@superschematic/db` and are only allowed on DB
tables. A table carries one or the other: `@versioned` implies
`@optimistic`.

## Declare a versioned table

```ts
import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, key, versioned } from "@superschematic/db";

// A recipe card, with every edit kept for a year.
@versioned({
  retentionDays: 365,
  partitionBy: "month",
  pruneKeepReferencedBy: { table: "menu_pin", keyColumn: "card_id", versionColumn: "card_version" },
  exclude: ["draftNotes"]
})
export abstract class RecipeCard {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  servings: Generic.Int64;
  draftNotes: Nullable<string>;
  updatedAt: Temporal.DateTime;
  updatedBy: Identity.UUID;
}
```

`@versioned` with no argument keeps history forever.

| Option | Meaning |
| --- | --- |
| `retentionDays` | Greater than 0. Generates `<table>_prune_history`, which deletes history rows older than this many days, and the ORM's `PruneHistory`. Nothing schedules it for a table on its own; a [version graph](/superschematic/reference/version-graphs/#sweep)'s sweep prunes its members' history. |
| `partitionBy` | `"month"` is the only value. The history table is partitioned by range on `recorded_at`. The generator writes only its default partition. |
| `pruneKeepReferencedBy` | One `{ table, keyColumn, versionColumn }` or a list of them. The prune function keeps every history row whose `(key, _version)` a row of `table` names in `(keyColumn, versionColumn)`. Requires `retentionDays`. |
| `exclude` | Fields left out of every history image. |

The table must have exactly one `@key`, and no field named `version` or
`_version`, since the generated `_version` field would collide with it in
Go and Rust.

## What the sql generator writes

For a table `recipe_card`:

| Object | Does |
| --- | --- |
| `_version BIGINT DEFAULT 1` on the table | The row's version. An insert stores 1; every update stores the previous version plus one. |
| `recipe_card_history` | `history_id`, the key column, `_version`, `operation` (`INSERT`, `UPDATE` or `DELETE`), `data` (the row as `to_jsonb` renders it) and `recorded_at`. Unique on `(key, _version)`, indexed on `(key, recorded_at)`, and on `recorded_at` when the table has a retention. A partitioned history table keeps a plain `(key, _version)` index, since a unique index on a partitioned table must include the partition key. |
| `recipe_card_capture_history()` and three triggers | `trg_recipe_card_bump_version`, `BEFORE UPDATE`, sets `_version`. `trg_recipe_card_capture_history_write`, `AFTER INSERT OR UPDATE`, records the row as stored, so an `INSERT ... ON CONFLICT DO UPDATE` records the one `UPDATE` it made. `trg_recipe_card_capture_history_delete`, `AFTER DELETE`, records the tombstone. |
| `recipe_card_prune_history(retention_days, max_rows)` | With `retentionDays`. Deletes history rows recorded more than `retention_days` ago that a newer version of the same key supersedes, keeping the latest row per key and every pinned row. `max_rows` caps one call; `NULL` means no cap. |

A delete's tombstone is the row as it was before the delete, at
`_version + 1`. Its actor column is `deleted_by` when the table has one,
else `updated_by`, else none. The trigger reads the actor from the
transaction-local Postgres setting the naming key
[`history_actor_setting`](/superschematic/reference/naming/#history_actor_setting)
names (default `superschematic.history_actor_id`), falling back to the
row's own value. The ORM's hard deletes set that setting to the context
user for their statement and clear it after. A statement of your own sets
it with `SELECT set_config('<setting>', '<user id>', true)`.

### Excluded fields

`exclude` names schema fields (camelCase). The capture function subtracts
their columns from every image it records: the insert and update rows and
the tombstone. The history readers return an excluded field as its zero
value. Excluding the actor column leaves tombstones without an actor, and
the ORM then sets no actor setting.

Verification refuses a name that is not a field of the type, the key,
`deletedAt` and relations: the history readers find and filter rows by
them. A member of a [version graph](/superschematic/reference/version-graphs/)
may exclude only its audit fields (`createdAt`, `createdBy`, `updatedAt`,
`updatedBy`) and nullable fields with `@conflictUnit("excluded")`, because
a commit reads the member's content back from history, and Revert and
Merge rebuild rows from history images: a required column missing from the
image would be written as NULL. The graph writes the audit fields itself.

## What the ORM generates

The repository of a versioned table has the ordinary methods plus:

| Method | Does |
| --- | --- |
| `GetVersion(ctx, id, version)` | The row at that version. A tombstone's version reads as `ErrNotFound`. |
| `ListVersions(ctx, id, opts)` | Every history record of the row, oldest first, as `types.HistoryRecord[*T]` (`Version`, `Operation`, `RecordedAt`, `Value`). `opts` pages. |
| `GetAsOf(ctx, id, ts)` | The row as it was at `ts`: its latest history record at or before `ts`. `ErrNotFound` when there was none or it was a delete (for a soft-deletable table, also a soft-deleted image). |
| `ListAsOfBy<Relation>ID(ctx, id, ts, opts)` | Per to-one relation: the rows that pointed at `id` at `ts`, with the same rule for deleted rows. |
| `UpdateOneIfVersion(ctx, id, expectedVersion, update)` | Updates the row only when its stored `_version` equals `expectedVersion`. |
| `DeleteOneIfVersion(ctx, id, expectedVersion)` | Deletes the row only at `expectedVersion`: a soft delete for a table with `deletedAt`, a hard delete otherwise. |
| `PruneHistory(ctx, retentionDays, chunkRows)` | With `retentionDays`. Calls the prune function, in chunks of `chunkRows` until the backlog is drained when `chunkRows > 0`, and returns the number of rows deleted. `PruneHistoryFunctionName()` names the SQL function. |

The fenced writes return `ErrVersionConflict` when the row exists at
another version and `ErrNotFound` when it does not exist.
`ErrVersionConflict` wraps `ErrNotFound`, so `errors.Is(err, ErrNotFound)`
holds for both.

The Go, TypeScript, Rust and Python types give the table's type a field
that holds `_version` in JSON (`Version` in Go, `_version` in TypeScript,
`version` in Rust and `version_` in Python) and declare `HistoryRecord`.
Only Go has an ORM, so the history readers and fenced writes above are
Go's; the other languages read and send `_version` as a field.

```go
ctx := WithUserID(context.Background(), userID)

// Update only if nobody has written since version 2.
updated, err := db.Recipe.UpdateOneIfVersion(ctx, breadID, 2, &RecipeUpdate{Title: &rye})
if errors.Is(err, ErrVersionConflict) {
	// The row moved on: reload it and try again, or tell the caller.
}

past, err := db.Recipe.GetVersion(ctx, breadID, 2)          // the row as version 2 left it
then, err := db.Recipe.GetAsOf(ctx, breadID, lastWeek)      // the row as it was at a time
steps, err := db.Step.ListAsOfByRecipeID(ctx, breadID, lastWeek, nil)
```

## `@optimistic`

```ts
@optimistic
export abstract class Pantry {
  @key
  id: AutoGenerate<Identity.UUID>;
  shelves: Generic.Int64;
}
```

An `@optimistic` table gets `_version`, a `BEFORE UPDATE` trigger that
bumps it (`<table>_bump_version`, `trg_<table>_bump_version`),
`UpdateOneIfVersion`, `DeleteOneIfVersion` and `ErrVersionConflict`. It
has no history table, capture function, prune function or history
readers, and the types declare `_version` but no `HistoryRecord`. Use it
for a table that needs a fence on concurrent writes and no past versions.
