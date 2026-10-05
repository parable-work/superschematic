---
title: Version graphs
description: Declare a version graph over versioned DB tables with @versionGraph, @graphMember and @conflictUnit; the tables the loader adds, the merge core and its JSON contract, the Go engine and its Postgres adapter with merge-only primary lines, releases, rebase, snapshots and the sweep, the generated Go facade, how a consumer links the core, the core, the engine, its SQLite adapter and the generated facade from TypeScript, the Rust engine and facade, and the core, the engine, its SQLite adapter and the generated facade from Python.
sidebar:
  order: 8
---

A version graph keeps a tree of rows under one stable identity, the root,
and versions the tree as a whole. The tree's rows live in
[versioned tables](/superschematic/reference/versioned-tables/). A ref is a
line of work: a primary line, or a change set branched from another ref.
Each ref holds only the rows it overrides. A commit records the exact row
versions a ref sealed. Work happens on change sets; a primary line takes
writes only from merges, and each root's release pointer names the tagged
commit readers see. One core composes, merges, diffs, hashes and
validates trees. The Go, TypeScript, Rust and Python engines run every
graph operation on the core over a storage adapter, and a generated facade in
each language gives each graph typed methods over its engine.

The design and the alternatives not taken are D17, D19 and D32 in
[docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md).

## Declare a graph

```ts
import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Relation,
  conflictUnit,
  graphMember,
  key,
  versionGraph,
  versioned
} from "@superschematic/db";

@versionGraph({ schemaEpoch: 1, snapshotEvery: 32 })
export abstract class Recipe {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}

@versioned({ retentionDays: 365, exclude: ["scratch"] })
@graphMember({ graph: Recipe, order: "position" })
export abstract class Step {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  position: Generic.Int64;
  instruction: string;

  @conflictUnit("keyed")
  timings: Generic.JSON;

  @conflictUnit("excluded")
  scratch: Nullable<string>;

  updatedAt: Temporal.DateTime;
  updatedBy: Identity.UUID;
}

@versioned({ retentionDays: 365 })
@graphMember({ graph: Recipe, parent: { key: "stepKey", of: Step } })
export abstract class Ingredient {
  @key
  id: AutoGenerate<Identity.UUID>;
  recipe: Relation<Recipe>;
  stepKey: Identity.UUID;
  quantity: string;
}
```

The three decorators come from `@superschematic/db` and are only allowed
on DB tables. `examples/acme-schematic` declares its own graph the same
way (`schemas/services/shop-db/src/planogram.schema.ts`), with no edit to
the core.

| Decorator | Meaning |
| --- | --- |
| `@versionGraph({ name?, schemaEpoch?, snapshotEvery? })` | Marks the root. The root is never overlaid and is in no commit. `name` prefixes the generated types (PascalCase) and tables (snake_case) and defaults to the root's name. `schemaEpoch`, default 0, is recorded on every commit. `snapshotEvery`, default 64, is how many commits past the nearest snapshot on its chain a commit is snapshotted at ([Snapshots](#snapshots)). |
| `@graphMember({ graph, parent?, order?, singleton? })` | Marks an entity kind of the graph. `graph` is the root class. |
| `parent: { key, of }` | Containment: the UUID field `key` holds the parent row's entity key, and `of` is a member of the same graph, the member itself included. Deleting a parent removes its descendants. |
| `order` | A `Generic.Int64` field that orders siblings. |
| `singleton: true` | At most one live row of the kind per ref. |
| `@conflictUnit(strategy)` | On a member's field: its merge unit. The strategies are below. |

Verification refuses:

- a root that is not a DB table, has other than one UUID `@key`, has no
  member, or whose `name` is not PascalCase; a negative `schemaEpoch`; a
  `snapshotEvery` that is not positive;
- a member that is not `@versioned`, has other than one UUID `@key`, has
  other than exactly one relation to its root, has `deletedAt`, or declares
  `entityKey`, `ref` or `deletedOnRef` or a field whose column is
  `entity_key`, `ref_id` or `deleted_on_ref` (such as `refId`);
- a type that is a root and a member, or a member of a graph the schema
  does not declare;
- a `parent.of` that is not a member of the same graph, a `parent.key`
  that is not a UUID field, and an `order` that is not a `Generic.Int64`
  field;
- a graph whose generated names collide with a definition of the schema or
  of another graph, and an index whose name collides with one the graph
  generates (`uq_<member>_entity_ref`, `uq_<graph>_ref_root_name`,
  `uq_<graph>_commit_root_sequence`, `uq_<graph>_patch_entity`,
  `idx_<graph>_patch_entity_version`, `uq_<graph>_release_root`,
  `uq_<graph>_snapshot_entry_entity` and
  `idx_<graph>_snapshot_entry_entity_version`): Postgres keeps index names
  in one namespace per schema, so an index on any table counts;
- `@conflictUnit` outside a member, an unknown strategy, and `keyed` or
  `jsonSchema` on a field that is not one JSON object (`Generic.JSON` or a
  `@jsonField` type);
- a member that excludes content from history with
  `@versioned({ exclude })`: it may exclude only its audit fields and
  nullable fields with `@conflictUnit("excluded")`. Revert and Merge
  rebuild rows from history images, so a required column missing from the
  image would be written as NULL and the statement would fail. The shell
  writes the audit fields itself.

A member has no `deletedAt` because a delete on a ref is a row that holds
the entity's `(entityKey, ref)` slot. A soft delete would free the slot and
hide the tombstone from every generated read.

## Conflict units

| Strategy | Merges |
| --- | --- |
| `atomic` (the default) | The whole field as one unit. |
| `keyed` | Each top-level key of a JSON object. Two sides that change different keys merge. |
| `jsonSchema` | A JSON Schema object: each entry of `properties`, recursively; each name's membership in `required`; every other keyword as one unit. Removing or retyping a property conflicts with a concurrent edit under it. |
| `excluded` | Not content: never conflicts and is not hashed. |

The audit fields (`createdAt`, `createdBy`, `updatedAt`, `updatedBy`) are
excluded without a decorator, as is the member's relation to its root.
`updatedBy` names a row's author, and a conflict reports each side's.

## What the loader adds

After verification the loader expands each graph into ordinary types, as
it copies a trait's fields onto a type. The `sql`, `orm` and `types`
generators emit the result like any other table, and `--emit-ir` shows it.
Every expanded type, enum, field, index and prune pin carries
`origin: "versionGraph"` in the IR. `format` writes the decorators back and
skips the expansion.

For a graph named `Recipe`:

| Generated | Shape |
| --- | --- |
| `RecipeRef` (`recipe_ref`), `@versioned`, soft-deletable | `id`; `root` (relation to `Recipe`, `RESTRICT`); `parentRef?`; `baseCommit?`; `headCommit?`; `name`, unique per root among live refs; `sealedAt?`; audit fields. A ref with no `parentRef` is a primary line; one with a parent is a change set. Its `_version` fences every write through it. |
| `RecipeCommit` (`recipe_commit`), written once | `id`; `root`; `ref`; `parentCommit?`; `message?`; `schemaEpoch`; `contentHash`; `sequence?`, unique per root; `createdAt`; `createdBy`. A commit with a `sequence` is a published version. |
| `RecipePatch` (`recipe_patch`), written once | `id`; `commit` (`RESTRICT`); `entityKind`; `entityKey`; `entityId`; `entityVersion`; `operation`. Unique on `(commit, entityKind, entityKey)`, indexed on `(entityId, entityVersion)`. |
| `RecipeRelease` (`recipe_release`), `@versioned` | `id`; `root` (`RESTRICT`), unique; `commit` (`RESTRICT`); audit fields. A root's release pointer: the tagged commit readers see. Its `_version` fences every move, and its history is the release log. |
| `RecipeSnapshotEntry` (`recipe_snapshot_entry`), written once | `id`; `commit` (`RESTRICT`); `entityKind`; `entityKey`; `entityId`; `entityVersion`. A snapshotted commit's full pin set, one entry per entity of its tree. Unique on `(commit, entityKind, entityKey)`, indexed on `(entityId, entityVersion)`. |
| `RecipeEntityKind`, `RecipePatchOperation` | One value per member (`step`, `ingredient`), and `ADD`, `UPDATE`, `DELETE`. |
| On each member | `entityKey`, the logical identity, generated on insert; `ref` (`RESTRICT`); `deletedOnRef`, default false; a unique index on `(entityKey, ref)`; and, with `retentionDays`, `pruneKeepReferencedBy` pins on `recipe_patch(entity_id, entity_version)` and `recipe_snapshot_entry(entity_id, entity_version)`, so pruning never deletes a row version a commit or a snapshot names. |

The generated `createdBy`, `updatedBy` and `deletedBy` fields take the
scalar of the schema's first such audit field in type name order, else the
root key's.

## The core

The core is one Rust crate, `runtime/versiongraph/rust`, with no IO, clock
or randomness. It holds no per-kind code: a graph descriptor tells it
which column of each kind's rows plays which role. Its five operations each
take one JSON document and return one:

| Operation | Input | Output |
| --- | --- | --- |
| `compose` | `{descriptor, base, overlay}` | `{tree, findings}`: the overlay's rows laid over the base by entity key; a tombstone removes the entity and its descendants. |
| `merge` | `{descriptor, base, ours, theirs, resolutions?}` | `{merged, conflicts, entities}`: a three-way merge per entity, then per conflict unit. |
| `diff` | `{descriptor, from, to}` | `{changes}`: each entity's `ADD`, `UPDATE` or `DELETE`, with `to`'s row. |
| `content_hash` | `{descriptor, tree}` | `{contentHash}`: SHA-256 over the canonical JSON of each kind's content columns, `null` for one a row lacks, rows sorted by entity key. |
| `validate` | `{descriptor, tree}` | `{findings}`: duplicate entity keys, the singleton rule, absent parents, parent cycles, orders outside the integers a JavaScript number holds exactly. |

A tree is `{"<kind>": [row, ...]}`, and a row is a canonical row: a JSON
object keyed by column name whose values are the schema runtime's JSON for
each field's type, in one form per value class (below), so a live row and
its history image hash the same whatever database stored them. A kind's
content columns are its declared columns less the role and excluded ones,
and a row that lacks one holds it as `null` wherever the core compares,
merges, diffs or hashes content: after a kind gains a column, history
images written before the change lack it while live rows read it as `null`,
and both are the same content, so a ref and its head commit hash the same,
a save of a row as its base holds it is nothing to commit, and a side that
never set the column merges with one that sets it. `compose` returns each
row as it was given; each row `diff` and `merge` return, which an engine
writes back, carries every declared content column, `null` where its input
lacked one, so a revert to a commit written before the gain clears a value
the column holds. Nulls are hashed, so under the descriptor that declares a
gained column a tree hashes differently from how it hashed before the gain.
A column added with a `DEFAULT` is outside the rule, since Postgres gives its
existing rows the default while the old images lack it. A refused input
returns `{"error": {"code", "message"}}` with a stable code. The contract,
with the descriptor's members, every rule, the error codes and the C ABI, is
[runtime/versiongraph/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md).
Its vectors in `runtime/versiongraph/testdata/vectors` are the executable
form: the Rust tests, the Go binding, the TypeScript package's tests and the
Python package's tests run every one.

The same exports are built three ways: a static archive, which the Go
binding `runtime/versiongraph/go` (package `versiongraph`) links through
cgo; a `cdylib`; and `wasm32-unknown-unknown`, which the TypeScript package
`@superschematic/versiongraph` ships
([Use the core from TypeScript](#use-the-core-from-typescript)). Python
calls the crate's Rust API through a PyO3 extension instead
([Use the core from Python](#use-the-core-from-python)).

### The descriptor

Each graph's descriptor is built once from the IR, and every generated
package that runs the graph embeds the same one: the constant
`<Name>GraphDescriptor` in the Go ORM package and in the TypeScript types
package's `versiongraph/<name>.ts`, and `<NAME>_GRAPH_DESCRIPTOR` in the
Rust and Python types packages. The Go types module also writes it as
`versiongraph/<name>.json`, where a browser or another runtime reads the
same one. Writing the types module removes each
`versiongraph/*.json` that no graph of the schema writes and leaves any
other file there.

The descriptor is version 3, and the core refuses any other, version 2
included. Besides each kind's roles, it names the graph's tables and gives
every column of each kind's table a value class, which a storage adapter
reads to build its statements and to normalize the rows it reads. `root`
names the column that holds the root's key, which the adapter writes on
every row; the core does not read it. `history` says what the kind's
history keeps, as the sql generator's triggers and prune function hold it:
`retentionDays` from `@versioned({ retentionDays })` (absent for none),
`exclude`, the columns `@versioned({ exclude })` leaves out of every image,
and `actor`, the column a delete's image names its actor in (`deleted_by`,
else `updated_by`; absent when the kind has neither or excludes it). An
adapter that writes history itself reads it; the Postgres adapters leave
it to the triggers:

```json
{
  "version": 3,
  "graph": "recipe",
  "root": { "table": "recipe", "key": "id" },
  "refTable": "recipe_ref",
  "commitTable": "recipe_commit",
  "patchTable": "recipe_patch",
  "releaseTable": "recipe_release",
  "snapshotTable": "recipe_snapshot_entry",
  "kinds": [
    {
      "kind": "ingredient",
      "table": "ingredient",
      "historyTable": "ingredient_history",
      "key": "entity_key", "id": "id", "ref": "ref_id", "root": "recipe_id",
      "tombstone": "deleted_on_ref", "version": "_version",
      "parent": { "key": "step_key", "kind": "step" },
      "excluded": ["recipe_id"],
      "history": { "retentionDays": 365, "exclude": [] },
      "columns": {
        "_version": "integer", "deleted_on_ref": "boolean", "entity_key": "uuid",
        "id": "uuid", "quantity": "string", "recipe_id": "uuid", "ref_id": "uuid",
        "step_key": "uuid"
      }
    },
    {
      "kind": "step",
      "table": "step",
      "historyTable": "step_history",
      "key": "entity_key", "id": "id", "ref": "ref_id", "root": "recipe_id",
      "tombstone": "deleted_on_ref", "version": "_version",
      "author": "updated_by",
      "order": "position",
      "units": { "timings": "keyed" },
      "excluded": ["recipe_id", "scratch", "updated_at"],
      "history": { "retentionDays": 365, "exclude": ["scratch"], "actor": "updated_by" },
      "columns": {
        "_version": "integer", "deleted_on_ref": "boolean", "entity_key": "uuid",
        "id": "uuid", "instruction": "string", "position": "integer",
        "recipe_id": "uuid", "ref_id": "uuid", "scratch": "string",
        "timings": "json", "updated_at": "dateTime", "updated_by": "uuid"
      }
    }
  ]
}
```

### Value classes and canonical rows

A column's value class comes from its field's type, as the schema runtime's
JSON tells values apart, and from the SQL type the column is stored as: a
scalar's `sql` type mapping, or the type its traits infer without one. Each
class has one rule that turns what Postgres
returns, in `to_jsonb` of a live row or in a history image, into the
canonical JSON:

| Class | Fields | Canonical JSON |
| --- | --- | --- |
| `string` | `string`, and string scalars stored as `TEXT`, `VARCHAR`, `CITEXT` or `INET` | The string. |
| `enum` | An enum | The member's value. |
| `integer` | Number scalars stored as `BIGINT`, `INTEGER` or `SMALLINT` (`Generic.Int64`) | The digits, exactly, however wide. |
| `number` | `number`, number scalars stored as `DOUBLE PRECISION`, `REAL`, `NUMERIC` or `DECIMAL` | The exact decimal value as `JSON.stringify` lays out a number: `1.5`, `1e+21`, `1.5e-7`. |
| `boolean` | `boolean` | `true` or `false`. |
| `uuid` | `Identity.UUID`, `Identity.UserID`, a to-one relation to a UUID key | base62, the scalar core's form (`2tLrGjz6ktIRCukXDsqykS`). |
| `dateTime` | `Temporal.DateTime` | RFC 3339 in UTC with `Z` (`2026-09-01T10:00:00.12Z`), whatever the session's time zone. |
| `date` | `Temporal.Date` | `2026-09-01`. |
| `time` | `Temporal.Time` | `18:00:00`, with a fraction of a second when there is one. |
| `duration` | `Temporal.Duration` | The scalar core's form: `1h30m0s`, `1.5s`, `500ms`, `1500us`. A day is 24 hours; months and years are refused. |
| `json` | A scalar stored as `JSONB` (`Generic.JSON`, `Generic.StringMap`), an object type in a `@jsonField` column, a map | The value with object members sorted by key, no whitespace, numbers as `number`. |

A list adds `[]` to its element's class (`uuid[]`) and a list of lists
`[][]`. A `@jsonField` value and a list of lists live in a `JSONB` column;
an element there that is not an object type or a JSON value keeps its own
class (`integer[][]`). A graph member with a field no class reads fails
generation, naming
the field and its SQL type: `Geo.Location`, stored as `POINT`, and
`Embedding.Vector`, a JSON array stored as `TEXT`. The contract, with every rule and its
vectors in `runtime/versiongraph/testdata/canonical`, is in
[runtime/versiongraph/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md#canonical-rows).
The Go package `github.com/parable-work/superschematic/runtime/versiongraph/go/canonical`
implements the Postgres rules, and the Postgres adapter normalizes every
row it reads with it.

## The engine and its Postgres adapter

The Go engine (package `engine` in `runtime/versiongraph/go`) implements
every graph operation once: create a primary line, branch, save, commit,
seal, merge, rebase, revert, release, released, materialize, compose, diff,
history, discard and sweep, with the walk ceiling, the schema-epoch check
and the named errors. It reads and writes canonical rows only, takes every
id as a UUID in its canonical form (base62) or hyphenated, and takes an
actor for every write.

It reaches storage only through the interface in package `storage`: read
and lock refs, read a ref's rows, upsert and remove a member row, read
history images by `(id, _version)`, read and write commits, patches,
snapshots and the release pointer, take the next sequence under a root
lock, walk commits, read discarded refs and idle change sets, prune and
take the sweep lock. It asks the adapter for one transaction per
operation. Every language's engine has a Postgres adapter, and the
TypeScript engine has a SQLite adapter too
([below](#the-sqlite-adapter)), as the Python engine does
([Use the engine from Python](#use-the-engine-from-python)); another
database needs its own implementation of that interface.

Package `postgres` is the Postgres adapter. It builds its statements at run
time from the descriptor, reads live rows with `to_jsonb` and history
images from their `data` column, and returns each as a canonical row. It
writes a canonical row through `jsonb_populate_record`, turning a UUID into
the hyphenated form and a duration into interval text, and writes the ref,
the root, the tombstone and the audit columns itself. It reaches Postgres
through a small `Client` interface (a transaction, and a query and an exec
inside it); `postgres.Pgx` binds a pgx connection, pool or open
transaction, where it runs in a savepoint. `postgres.Options` names the
history actor setting, the schema's
[`history_actor_setting`](/superschematic/reference/naming/#history_actor_setting).

```go
adapter, err := postgres.New(descriptor, postgres.Options{})
eng, err := engine.New(descriptor, adapter.Storage(postgres.Pgx(pool)), engine.Options{SchemaEpoch: 1, SnapshotEvery: 32})
ref, err := eng.CreatePrimary(ctx, actor, root, "main")
```

### The primary line and the release pointer

A primary line takes writes only from `Merge`. `Save`, `Commit`, `Seal`
and `Revert` on it fail with `ErrPrimaryMergeOnly`: work happens on a
change set and merges in, and the merge commits in the same transaction,
with the message and tag its options give. A primary line's live rows
therefore always compose to its head commit. To start a line, branch a
change set from it (its base is empty until the line has a commit), and to
undo work, revert a change set and merge that.

`Release(root, commit, version)` points the root's release pointer at a
tagged commit of that root; an untagged commit is `ErrNotTagged`, and
another root's `ErrRootMismatch`. It is fenced by the pointer's `_version`:
0 writes the root's first pointer, and a pointer at another version is
`ErrVersionConflict`. A release writes no member rows, so a rollback is a
`Release` to an earlier tagged commit, and the pointer's history table is
the release log. `Released(root)` returns the pointer and the released
commit's tree; a root never released is `ErrNotFound`. Readers of released
content read `Released`, or `Materialize` of a tagged commit, rather than
member tables.

### Rebase

`Rebase(draft, version, resolutions)` catches a change set up with its
parent's head. It merges the parent's head into the change set, with the
change set's base as the merge base and its composed tree, uncommitted work
included, as ours. With conflicts left after `resolutions` it returns them
and writes nothing, as `Merge` does. Otherwise it writes the change set's
rows so it composes to the merged tree over the parent's head: it keeps a
row for each entity that differs from the new base, a tombstone where the
merged tree lacks one, and removes every other row it held, which reads
through. It moves `baseCommit` to the parent's head and commits on the
change set with its previous head as the parent (its new base when it had
no commit), so its `History` keeps its commits. On a change set already on
its parent's head it moves only the ref's `_version`; on a primary line it
is `ErrNoParent`.

### Snapshots

A snapshot is a commit's full pin set, stored as `<graph>_snapshot_entry`
rows. A commit is snapshotted when it is `snapshotEvery` commits past the
nearest snapshot on its parent chain (counting from before the chain's
first commit), when it is tagged, and when it is released. `Materialize`
walks back to the nearest snapshot and lays the later commits' patches over
its pins, so a read touches at most `snapshotEvery` commits' patches and
one pin set. The walk ceiling stays as a guard. A commit whose tree is
empty has no entries to store, and reads as having no snapshot.

### Sweep

`Sweep(options)` is one maintenance pass in one transaction, and nothing
runs it unless a service does. It takes the graph's sweep lock (a
transaction-scoped Postgres advisory lock keyed by the ref table); while
another pass holds it, the pass does nothing and reports `Skipped`.
Otherwise, in order, it:

1. discards every live change set with no write for `AbandonAfter`, when
   that is set (it is off by default), and leaves one that a write reaches
   after the pass read it, since it is no longer idle;
2. hard-deletes the member rows of refs discarded longer ago than
   `DiscardGrace` (default seven days), recording the actor in history; the
   refs and their commits stay as the audit trail, and their commits still
   materialize from history;
3. prunes each kind's history past its declared `retentionDays`, keeping
   every row version a patch or a snapshot pins, at most `PruneBatch` per
   kind;
4. writes every snapshot the rules above call for that the graph lacks.

It writes as `SweepOptions.Actor` and returns a `SweepReport` of what it
did. `RunSweeper(ctx, interval, options, onPass)` runs a pass at once and
then every `interval` until `ctx` is done; since each pass takes the lock,
one replica sweeps at a time. Every language's adapter takes the lock
under the same key, so sweepers written in different languages exclude
each other too.

`engine.ErrorCode(err)` names an error with a code every language's engine
shares (`version_conflict`, `ref_sealed`, `primary_merge_only`,
`not_tagged`, `no_parent`, and the core's codes). The
scenarios in `runtime/versiongraph/testdata/scenarios` run sequences of
operations over canonical rows, with the expected trees, content hashes,
conflicts and errors, against the fixture in
`runtime/versiongraph/testdata/fixture`; the Go, TypeScript, Rust and
Python engines run every one against Postgres. Their format is in
[runtime/versiongraph/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md#scenarios).

## The generated facade

When a DB schema declares a graph, the Go ORM generator writes
`versiongraph_<name>.go` beside the repositories, and puts the declarations
every graph's facade shares (`GraphEdits`, `GraphSweepOptions`,
`GraphSweepReport`, the named errors, `DefaultWalkCeiling`,
`DefaultDiscardGrace`) in `database.go`. `db.RecipeGraph()` returns a typed
`RecipeGraph`, which runs each operation on the engine and its Postgres
adapter. Every method but `Sweep` and `RunSweeper` runs in one transaction
of the ORM's pool and needs a user in the context (`WithUserID`), the actor
of its writes, else it returns `ErrNoUserInContext`; a sweep writes as `GraphSweepOptions.Actor`. Every write through a ref takes the ref's expected
`_version` and fails with `ErrVersionConflict` when the ref has moved on.
A ref or commit that does not exist, or a discarded ref, is `ErrNotFound`.

From the ORM's own tests, a recipe's first draft branched, edited,
committed, merged as a tagged version and released:

```go
ctx := WithUserID(context.Background(), cook)
g := db.RecipeGraph()

main, err := g.CreatePrimary(ctx, *recipe.Id, "main")
draft, err := g.Branch(ctx, *main.Id, "first draft")
saved, err := g.Save(ctx, *draft.Id, draft.Version, RecipeEdits{Step: GraphEdits[types.Step]{Upsert: []*types.Step{
	{Position: 1, Instruction: "Mix", Timings: types.GenericJSON(`{"knead": 10, "rest": 30}`)},
	{Position: 2, Instruction: "Bake", Timings: types.GenericJSON(`{"oven": 40}`)},
}}})
_, err = g.Commit(ctx, *draft.Id, saved.Ref.Version, RecipeCommitOptions{Message: "first draft"})

// The only write a primary line takes is a merge. Tag makes the commit published version 1.
first, err := g.Merge(ctx, *draft.Id, *main.Id, main.Version, nil, RecipeCommitOptions{Message: "first", Tag: true})
release, err := g.Release(ctx, *recipe.Id, *first.Commit.Id, 0) // 0: the first release
released, err := g.Released(ctx, *recipe.Id)                    // the pointer and the released tree
```

| Method | Does |
| --- | --- |
| `CreatePrimary(ctx, root, name)` | Creates a primary line of `root`. |
| `Branch(ctx, fromRef, name)` | Creates a change set of `fromRef` whose base is `fromRef`'s head commit. |
| `Save(ctx, ref, version, RecipeEdits)` | On a change set, applies each kind's `GraphEdits[T]`: `Upsert` writes each row as the ref's override of its entity, found by `EntityKey` (a row without one is a new entity, whose key the database generates). Every ref holds its own row of an entity, so the row's id is never the caller's: Save ignores it and the table's default generates it, whether the `@key` is `AutoGenerate<Identity.UUID>` or a plain `Identity.UUID`. `Delete` writes a row that deletes the entity on the ref; `Unset` removes the ref's own row, so the ref reads the entity through its base again. A sealed ref refuses it with `ErrRefSealed`, a primary line with `ErrPrimaryMergeOnly`. |
| `Commit(ctx, ref, version, RecipeCommitOptions)` | Composes a change set, checks the tree with `validate`, diffs it against the ref's last commit (or its base), and writes a commit with one patch per changed entity pinning the winning row's `(id, _version)`. Moves the ref's head. `Message` is stored; `Tag` takes the root's next `sequence`, which makes the commit a published version. `ErrNothingToCommit` when nothing changed; an `*InvalidTreeError` (`ErrInvalidTree`) lists what `validate` found; `ErrPrimaryMergeOnly` on a primary line. |
| `Seal(ctx, ref, version)` | Commits a change set when it has changes and sets `sealedAt`. The ref then refuses writes. `ErrPrimaryMergeOnly` on a primary line. |
| `Merge(ctx, source, target, targetVersion, resolutions, RecipeCommitOptions)` | Merges the source's head commit into the target against the source's base. Without conflicts it writes the result onto the target and commits in the same transaction, with the options' message and tag. It is the only write a primary line takes. With conflicts left after `resolutions` it returns them as `[]RecipeConflict` (kind, entity key, unit path, the base, ours and theirs values, and each side's author) and writes nothing. A `RecipeResolution` takes a side (`versiongraph.Take`) or gives a value for one conflict's path. |
| `Rebase(ctx, draft, version, resolutions)` | Catches a change set up with its parent's head ([Rebase](#rebase)), returning a `RecipeMergeResult`: the moved change set and its commit, or the conflicts, with nothing written. `ErrNoParent` on a primary line. |
| `Revert(ctx, ref, version, toCommit)` | Writes the rows that make a change set compose to `toCommit`'s tree, and commits. History is never rewritten. `ErrPrimaryMergeOnly` on a primary line. |
| `Release(ctx, root, commit, version)` | Points the root's release at a tagged commit, fenced by the pointer's `_version` (0 for the first release), and returns the typed `RecipeRelease`. `ErrNotTagged` for an untagged commit. |
| `Released(ctx, root)` | The release pointer and the released commit's tree, as `RecipeReleased`. `ErrNotFound` before the first release. |
| `Materialize(ctx, commit)` | Reads a commit's tree: the nearest snapshot on its chain with each later commit's patches laid over it, the nearest winning and a `DELETE` removing the entity. |
| `Compose(ctx, ref)` | Reads a ref's tree: its base commit's tree with its own rows laid over it. |
| `Diff(ctx, from, to)` | The `[]RecipeChange` between two commits' trees. |
| `History(ctx, ref)` | The commits the ref wrote, newest first. |
| `Discard(ctx, ref, version)` | Soft-deletes the ref, which frees its name. |
| `Sweep(ctx, GraphSweepOptions)` | One maintenance pass ([Sweep](#sweep)) in a transaction of its own; returns a `GraphSweepReport`. `ErrNoActor` without an actor. |
| `RunSweeper(ctx, interval, GraphSweepOptions, onPass)` | Runs `Sweep` at once and every `interval` until `ctx` is done, and returns `ctx`'s error. |

A tree comes back as `RecipeTree`: a slice of typed rows per kind, the
content hash and any compose findings. The facade turns a typed edit into a
canonical row from each field's JSON, and a canonical row back into a typed
value, so a field comes back in its canonical form: an instant in UTC, a
time of day as `HH:MM:SS`. A conflict's values and a change's row are
canonical JSON.

`Materialize` stops with `ErrWalkCeiling` after `DefaultWalkCeiling`
(4096) commits; `g.WithWalkCeiling(n)` returns a graph with another
ceiling. It refuses a commit whose `schemaEpoch` is newer than the one the
ORM was generated with (`RecipeGraphSchemaEpoch`) with `ErrSchemaEpoch`.
`RecipeGraphSnapshotEvery` is the graph's snapshot interval.
The other named errors are `ErrEntityNotFound` (deleting or unsetting an
entity the ref does not hold), `ErrHistoryMissing` (a row version a commit
names is gone from history), `ErrRootMismatch` (two refs, or a ref and a
commit, of different roots), `ErrMergeIntoItself`, `ErrNameTaken` (the
root already has a live ref of that name), `ErrPrimaryMergeOnly`,
`ErrNotTagged`, `ErrNoParent` and `ErrNoActor`. They are the engine's errors;
the engine's `ErrVersionConflict` and `ErrNotFound` also match the ORM's.

## Link the core

An ORM whose schema declares a graph imports the version-graph runtime's
Go module (the binding, `engine`, `postgres` and `canonical`), the module
the naming key
[`versiongraph_go_module`](/superschematic/reference/naming/#versiongraph_go_module)
names (default `github.com/parable-work/superschematic/runtime/versiongraph/go`).
The binding links the core's static archive through cgo, so it needs
`CGO_ENABLED=1`, a C compiler and the archive:

```
scripts/versiongraph-archive.sh           # build it; prints -L<dir>
export CGO_LDFLAGS="$(scripts/superscalar-dep.sh --print) $(scripts/versiongraph-archive.sh --print)"
```

The script builds the crate with cargo and stages
`libsuperschematic_versiongraph.a` under
`runtime/versiongraph/go/lib/<goos>_<goarch>`, where the binding looks for
it. With
[`[paths] versiongraph_go`](/superschematic/reference/naming/#pathsversiongraph_go)
pointing at a checkout, the generated `go.mod` replaces the binding with
that checkout and finds the staged archive without the flag. A public API
whose `authDb` declares a graph carries the same replace, since the ORM's
own does not reach it. In this repository `make setup` and
`make versiongraph` build the archive, and the Makefile puts its directory
in `CGO_LDFLAGS`.

The binding ships link flags for linux and darwin on amd64 and arm64.

## Use the core from TypeScript

`@superschematic/versiongraph` runs the same core in the browser, bun and
Node. It ships the `wasm32-unknown-unknown` build and compiled ES modules,
and its core entry has no dependencies and no generated glue and types
every input and output of the contract.

```ts
import { init, VersionGraphError, type Descriptor } from "@superschematic/versiongraph";
import recipe from "./versiongraph/recipe.json" with { type: "json" };

const descriptor = recipe as Descriptor;
const graph = await init();

const { tree, findings } = graph.compose({ descriptor, base, overlay });
const { merged, conflicts, entities } = graph.merge({ descriptor, base, ours, theirs });
const { changes } = graph.diff({ descriptor, from: base, to: tree });
const { contentHash } = graph.contentHash({ descriptor, tree });

try {
  graph.validate({ descriptor, tree: { recipe_step: [] } });
} catch (error) {
  if (error instanceof VersionGraphError) console.log(error.code); // "unknown_kind"
}
```

`init(source?, options?)` compiles and instantiates the module; every
operation on the graph it returns is synchronous. `source` is the module's
bytes, a URL, a `Response` or a promise of one, or a compiled
`WebAssembly.Module`. Without it, `init` loads the
`superschematic_versiongraph.wasm` shipped next to the package's `index.js`,
found with `new URL(..., import.meta.url)`: fetched in a browser (a bundler
that understands that pattern copies the file into the build), and read
from disk under bun and Node. A server that sends the module as
`application/wasm` lets the browser compile it while it downloads. The file
is also exported as
`@superschematic/versiongraph/superschematic_versiongraph.wasm`.

| Method | Operation | Input and output types |
| --- | --- | --- |
| `compose` | `compose` | `ComposeInput`, `ComposeOutput` |
| `merge` | `merge` | `MergeInput`, `MergeOutput` |
| `diff` | `diff` | `DiffInput`, `DiffOutput` |
| `contentHash` | `content_hash` | `TreeInput`, `ContentHashOutput` |
| `validate` | `validate` | `TreeInput`, `ValidateOutput` |
| `run(operation, json)` | any, by its contract name | JSON text in, JSON text out |

A refused input throws a `VersionGraphError`, whose `code` is the
contract's error code (`ErrorCode`) and whose `message` is the core's.
Every type names its members as the contract does, and rows are plain
objects keyed by column name, the form the Go shell sends the core.

`JSON.parse` reads a number as a double, so a numeric column wider than 53
bits would lose digits on the way back. `options.parse` and
`options.stringify` replace the JSON codec, for example with a reviver that
returns `JSON.rawJSON(context.source)` for each number. An order column
needs neither: the core refuses one outside the integers a double holds
exactly.

The package's test suite runs every vector through the package. It decodes
each vector's input and output through the package's types, with one
decoder per member that the compiler requires to cover each type exactly
and a check of each literal union's values, and compares the decoded JSON,
member order included, with the vector. It also fails when a member or
literal of the types appears in no vector. A member or literal that is
missing, extra or misnamed in the types therefore fails it.

## Use the engine from TypeScript

`@superschematic/versiongraph` also carries the TypeScript engine, its
Postgres adapter and the base of the generated TypeScript facade. They port
the Go engine and adapter rule for rule, pass the same
[scenarios](https://github.com/parable-work/superschematic/tree/main/runtime/versiongraph/testdata/scenarios)
against Postgres, and fail with the same error codes. Each has an entry
point of its own, so the core's entry loads in a browser without them:

| Entry | Holds |
| --- | --- |
| `@superschematic/versiongraph/engine` | `Engine` and `SyncEngine`, the storage interfaces (`Storage` and `Tx`, `SyncStorage` and `SyncTx`), the named errors and `errorCode`, and the canonical rules (`canonicalRow`, `canonicalValue`) with the exact JSON codec they read with. |
| `@superschematic/versiongraph/postgres` | `PostgresAdapter`, its `Client` interface, and `pgPool` and `pgClient`, which bind the npm package `pg`. |
| `@superschematic/versiongraph/sqlite` | `SqliteAdapter`, its `SqliteClient` interface and `SqliteError`, `sqliteLayout`, and `nodeSqlite` and `bunSqlite`, which bind `node:sqlite` and `bun:sqlite`. |
| `@superschematic/versiongraph/facade` | `VersionGraphFacade`, which each generated `<Name>Graph` extends, and the types it returns. |

`pg` is an optional peer dependency. The bindings use only the methods they
call on a pool or a client, so no entry imports it: install it to use
`pgPool` or `pgClient`, or implement `Client` over another driver. A
`Client` runs a transaction, and a `query` inside it that returns every
column as the text Postgres writes, so no driver's type parsing touches a
date, a time, an interval, a numeric or a bigint. `pgPool` takes a
connection of the pool per transaction; `pgClient` runs transactions one
at a time on one connection, and with `{ savepoint: true }` as savepoints
of a transaction the caller holds.

```ts
import pg from "pg";
import { Engine } from "@superschematic/versiongraph/engine";
import { PostgresAdapter, pgPool } from "@superschematic/versiongraph/postgres";

const pool = new pg.Pool({ connectionString });
const adapter = new PostgresAdapter(descriptor, { historyActorSetting: "superschematic.history_actor_id" });
const engine = await Engine.create(descriptor, adapter.storage(pgPool(pool)), { schemaEpoch: 1, snapshotEvery: 32 });

const main = await engine.createPrimary(actor, root, "main");
const draft = await engine.branch(actor, main.id, "draft");
const saved = await engine.save(actor, draft.id, draft.version, {
  step: { upsert: ['{"entity_key": null, "position": 1, "instruction": "Mix", "timings": {}}'] },
});
```

The engine's methods take the Go engine's arguments in the same order,
with the actor first on every write, and return promises. A canonical row
is JSON text, as Go's `json.RawMessage` is, so a wide integer or numeric
keeps its digits: a tree is `Record<string, string[]>`, and a conflict's
values, a change's row and a resolution's `value` are JSON text. A
duration argument is in milliseconds. `runSweeper(intervalMs, options,
onPass, signal)` runs until its `AbortSignal` aborts and then rejects with
the signal's reason. It runs no pass when the signal has already aborted,
and a pass under way when it aborts finishes first, where Go's
`RunSweeper` cancels that pass through its context. The named errors are
classes with the stable `code` the scenario files name
(`VersionConflictError` is a `NotFoundError`, as in Go); `errorCode(err)`
returns it, or the core's code for an input the core refused.

`SyncEngine` runs the same operations, written once, over synchronous
storage, for a database whose driver blocks, as SQLite's does in bun and
Node: `SyncStorage.transact<T>(fn: (tx: SyncTx) => T): T`, and `SyncTx` has
every method of `Tx` returning its value (`undefined` for the three with
none, so an async method does not type-check). Each operation returns its
value or throws, with `Engine`'s arguments, rules and errors, and a
`SyncTx` method or a `transact` that returns a promise ends it with a
`TypeError`. A `SyncEngine` is built over a core already instantiated,
which `initSync` from `@superschematic/versiongraph` instantiates without
awaiting: from the module's bytes or a compiled `WebAssembly.Module`, or,
given neither under bun and Node, from the wasm file the package ships. A
`SyncEngine` has no `runSweeper`, since a loop that waits between passes
would block its thread, so its host schedules `sweep`.

```ts
import { initSync } from "@superschematic/versiongraph";
import { SyncEngine } from "@superschematic/versiongraph/engine";

const engine = new SyncEngine(initSync(), descriptor, storage, { schemaEpoch: 1, snapshotEvery: 32 });
const main = engine.createPrimary(actor, root, "main");
```

### The SQLite adapter

`SqliteAdapter` is a `SyncStorage` over SQLite, so a `SyncEngine` keeps a
graph in a SQLite file under bun or Node. Where the Postgres adapters use
the tables sqlgen generates for each graph, it owns one fixed layout of
nine `STRICT` tables, the same for every graph: `ref`, `ref_history`,
`commit`, `patch`, `snapshot_entry`, `release`, `release_history`, `member`
and `member_history`. Every row carries its graph's name, so one file
holds several graphs; a function the caller gives names each table and
index (`graph_ref` and so on by default). A member row keeps its kind's
role columns as columns and its other columns as one canonical JSON
object, so a kind needs no table of its own. Foreign keys check every edge
inside the layout; there is no root table, so the adapter itself refuses a
write through another graph's or another root's ref or commit.

```ts
import { DatabaseSync } from "node:sqlite";
import { initSync } from "@superschematic/versiongraph";
import { SyncEngine } from "@superschematic/versiongraph/engine";
import { SqliteAdapter, nodeSqlite } from "@superschematic/versiongraph/sqlite";

const client = nodeSqlite(new DatabaseSync("recipes.sqlite"));
const adapter = new SqliteAdapter(descriptor, { graph: "recipe" });
adapter.createTables(client);
const engine = new SyncEngine(initSync(), descriptor, adapter.storage(client), { schemaEpoch: 1, snapshotEvery: 32 });
```

SQLite has no trigger that can set `NEW`, so the adapter does in its own
statements what the generated triggers do on Postgres: it sets `_version`
(1, then the old version plus 1), writes each insert's and update's image
at the new version and a delete's at the old version plus 1 with the
kind's actor column set to the delete's actor, and leaves the kind's
`@versioned({ exclude })` columns out of every image. It reads those facts
from the descriptor's `history` (version 3), and prunes a kind's images
past its `retentionDays`, keeping every pinned one, as the prune function
does. Refs and release pointers keep history too. A transaction reads its
time once, from a clock option, and every write in it takes that time; ids
are version-4 UUIDs the adapter generates; and every value is stored in its
canonical form, so a row reads back as the canonical row it was written as.
A live row reads with every column its kind declares, `null` where the
stored row lacks one, as Postgres's `ADD COLUMN` without a `DEFAULT` gives
an existing row, and a history image reads as it was stored, as a Postgres
image does: one taken before its kind gained a column lacks it, which the
core reads as `null`.

On a connection of its own the adapter begins each transaction with
`BEGIN IMMEDIATE`, which takes the file's write lock, and runs one begun
inside another as a savepoint. With `callerTransaction: true` it runs
inside the transaction its caller holds and issues no transaction control,
for a host such as D16's engine that holds the transaction. With one
writer per file, a ref needs no lock of its own and the sweep lock is
always free. `sqliteLayout(tableName)` returns the layout's statements, one
statement each, for a caller that runs its own migrations.
`nodeSqlite(db)` and `bunSqlite(db)` bind an open `node:sqlite`
`DatabaseSync` and an open `bun:sqlite` `Database`; another driver
implements `SqliteClient` (`run`, `get` and `all` with numbered `?1`
parameters, and `exec`), whose errors carry SQLite's extended result code
in `code`. Python's SQLite adapter is in
[Use the engine from Python](#use-the-engine-from-python); Go's and Rust's
are still to come.

When a schema declares a graph, tsgen writes a typed facade per graph into
the TypeScript types package, `versiongraph/<name>.ts`, exported as
`./versiongraph`, and the package depends on the runtime package the
naming key
[`versiongraph_npm_package`](/superschematic/reference/naming/#versiongraph_npm_package)
names, at
[`[paths] versiongraph_typescript`](/superschematic/reference/naming/#pathsversiongraph_typescript)
when that is set. The file holds `RecipeGraphDescriptor`,
`RecipeGraphSchemaEpoch`, `RecipeGraphSnapshotEvery`, the typed tree,
edits, conflict, resolution and change types, and `RecipeGraph`:

```ts
import { pgPool } from "@superschematic/versiongraph/postgres";
import { RecipeGraph } from "@schemas/recipes-db-types/versiongraph";

const graph = new RecipeGraph(pgPool(pool), { actor: userId });
const draft = await graph.branch(mainId, "rest longer");
const saved = await graph.save(draft.id, draft.version, { step: { upsert: [{ ...mix, instruction: "Mix well" }] } });
const { ref, commit } = await graph.commit(draft.id, saved.ref.version);
const merged = await graph.merge(draft.id, mainId, mainVersion, [], { message: "rest longer", tag: true });
const { tree } = await graph.released(recipeId);
```

`RecipeGraph` has the Go facade's operations, from `createPrimary` to
`runSweeper`, plus `withActor` and `withWalkCeiling`. Every write records
the facade's `actor` and fails with `NoActorError` without one; a sweep
writes as its options' `actor`. It turns a typed value into a canonical
row through the value's JSON (an instant is its ISO string) and back
through the type's generated `parse<Type>FromJSON`, so a field comes back
in its canonical form: an instant as a `Date` in UTC, a time of day as
`HH:MM:SS`, a duration in the scalar core's form and a UUID in base62. A
relation field comes back as an object holding the target's key. There is
no TypeScript ORM, so refs, commits and release pointers come back as the
engine's `Ref`, `Commit` and `Release`.

```
make versiongraph-scenarios-ts   # every scenario on SQLite and the SQLite adapter's tests; then every scenario, the canonical vectors against Postgres, the adapter's, the sweeper's and the facade's tests; a gained column end to end on each backend
```

The SQLite pass needs no database server. The Postgres pass needs
`SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL`, and the target fails
without it once the SQLite pass has run; CI runs it in the versiongraph
job.

## Use the engine from Rust

`superschematic-versiongraph-engine`
([runtime/versiongraph/rust-engine](https://github.com/parable-work/superschematic/tree/main/runtime/versiongraph/rust-engine))
is the Rust engine. It has every operation, rule and error code of the Go
engine, runs over the same kind of storage adapter, and calls the core
natively. Its operations are `async`. Its Postgres adapter
(`postgres::Adapter`) builds its statements from the descriptor at run
time and reaches Postgres through `postgres::Client`, a two-trait seam;
`postgres::TokioPostgres` binds one tokio-postgres connection and is on by
default (the `tokio-postgres` feature). A service that runs operations side
by side, or inside a transaction it holds, implements `Client` over its own
pool or transaction. Every scenario runs through it against Postgres
(`make versiongraph-scenarios-rust`).

When a DB schema declares a graph and its Rust types are on, the types
generator writes `src/versiongraph_<name>.rs` beside the types, and the
crate depends on the engine
([`versiongraph_rust_crate`](/superschematic/reference/naming/#versiongraph_rust_crate),
at [`[paths] versiongraph_rust`](/superschematic/reference/naming/#pathsversiongraph_rust)
when it is set). The file holds the descriptor
(`RECIPE_GRAPH_DESCRIPTOR`), the schema epoch and snapshot interval, and a
typed `RecipeGraph` with the Go facade's operations in Rust's spelling:

```rust
use std::str::FromStr;
use schemas_recipes_types::{IdentityUUID, RecipeEdits, RecipeGraph, RecipeKindEdits};
use superschematic_versiongraph_engine::{postgres::TokioPostgres, CommitOptions};

let (client, connection) = tokio_postgres::connect(url, tokio_postgres::NoTls).await?;
tokio::spawn(connection);
let graph = RecipeGraph::postgres(TokioPostgres::new(client))?;

let main = graph.create_primary(&actor, &recipe, "main").await?;
let draft = graph.branch(&actor, &IdentityUUID::from_str(&main.id)?, "first draft").await?;
let draft_id = IdentityUUID::from_str(&draft.id)?;
let edits = RecipeEdits {
    step: RecipeKindEdits { upsert: vec![mix], ..RecipeKindEdits::default() },
    ..RecipeEdits::default()
};
let saved = graph.save(&actor, &draft_id, draft.version, &edits).await?;
graph.commit(&actor, &draft_id, saved.ref_.version, &CommitOptions::default()).await?;
let merged = graph
    .merge(&actor, &draft_id, &IdentityUUID::from_str(&main.id)?, main.version, &[],
        &CommitOptions { message: "first".into(), tag: true })
    .await?;
let tree = graph.materialize(&IdentityUUID::from_str(&merged.commit.unwrap().id)?).await?;
```

The Rust facade differs from the Go one where the languages do:

- Every write takes its actor as an argument; there is no context user.
  `sweep` and `run_sweeper` write as `SweepOptions::actor`, and
  `run_sweeper` stops when its `shutdown` future completes, and lets a
  pass under way finish, where Go's `RunSweeper` cancels it.
- The types crate has no ORM, so refs, commits and release pointers come
  back as the engine's `Ref`, `Commit` and `Release`, with ids as canonical
  strings, rather than as typed rows.
- A typed row read back through the facade leaves its to-one relations
  (the root, the ref) `None`, since a canonical row holds only their keys.
- Errors are the engine's `Error`; `Error::code()` is the stable code
  (`version_conflict`, `primary_merge_only`, ...).

The facade turns a typed edit into a canonical row from each field's serde
JSON, and a canonical row back into a typed value, so a field comes back in
its canonical form, as through the Go facade.

## Use the core from Python

`superschematic-versiongraph` (module `superschematic_versiongraph`,
[runtime/versiongraph/python](https://github.com/parable-work/superschematic/tree/main/runtime/versiongraph/python))
is the core for Python: a PyO3 extension module over the crate, built with
maturin as superscalar's Python binding is, for CPython 3.9 and newer. It
calls the core's Rust API natively, with the GIL released, and types every
input and output of the contract. It is not published yet; build it from a
checkout (`uv sync` in its directory, which needs cargo).

```python
import superschematic_versiongraph as vg

result = vg.compose({"descriptor": descriptor, "base": base, "overlay": overlay})
merged = vg.merge({"descriptor": descriptor, "base": base, "ours": ours, "theirs": theirs})
changes = vg.diff({"descriptor": descriptor, "from": base, "to": result["tree"]})["changes"]
content_hash = vg.content_hash({"descriptor": descriptor, "tree": result["tree"]})["contentHash"]

try:
    vg.validate({"descriptor": descriptor, "tree": {"recipe_step": []}})
except vg.VersionGraphError as error:
    print(error.code)  # "unknown_kind"
```

| Function | Operation | Input and output types |
| --- | --- | --- |
| `compose` | `compose` | `ComposeInput`, `ComposeOutput` |
| `merge` | `merge` | `MergeInput`, `MergeOutput` |
| `diff` | `diff` | `DiffInput`, `DiffOutput` |
| `content_hash` | `content_hash` | `TreeInput`, `ContentHashOutput` |
| `validate` | `validate` | `TreeInput`, `ValidateOutput` |
| `run(operation, json)` | any, by its contract name | JSON text or bytes in, JSON text out |

The types are `TypedDict`s and `Literal`s in
`superschematic_versiongraph.contract`, re-exported from the package, each
member named as the contract names it; inputs and outputs are plain dicts
and lists. A refused input raises `VersionGraphError`, whose `code` is the
contract's error code (`ErrorCode`) and whose `message` is the core's. An
operation name the core does not have raises `ValueError`.

The module-level functions encode with `json` and decode with
`json.loads`, which keeps an integer's digits however wide but reads a
number with a fraction or an exponent as a float, so a numeric column that
a double does not hold would lose digits. `VersionGraph(loads=..., dumps=...)`
gives the same methods over another codec, for example one that decodes
with `parse_float=decimal.Decimal` and writes a Decimal's digits back;
`run` leaves the JSON to the caller.

The package's tests run every core vector through `run` and through the
typed methods with an exact codec, and compare each output with the
vector, member order and number digits included, and through the
module-level functions when `json` reads the vector without loss. They
check each vector's input and output against the types
and fail when a member or literal of the types appears in no vector. `make
python` runs them under the default Python and under 3.9, and CI's python
job runs them too.

## Use the engine from Python

The same package carries the Python engine, its Postgres and SQLite
adapters and the base of the generated Python facade. They port the Go
engine and adapter, and the TypeScript SQLite adapter, rule for rule, pass
the same
[scenarios](https://github.com/parable-work/superschematic/tree/main/runtime/versiongraph/testdata/scenarios)
against Postgres and SQLite, and fail with the same error codes:

| Module | Holds |
| --- | --- |
| `superschematic_versiongraph.engine` | `Engine`, its options, results and `SweepOptions`, and `run_sweeper`. |
| `superschematic_versiongraph.storage` | The storage protocol (`Storage`, `Tx`) and the values it takes and returns. |
| `superschematic_versiongraph.errors` | The named errors and `error_code`. |
| `superschematic_versiongraph.canonical` | The canonical rules (`canonical_row`, `canonical_value`), over the exact JSON codec in `superschematic_versiongraph.exactjson`. |
| `superschematic_versiongraph.postgres` | `PostgresAdapter`, its `Client` protocol, and `psycopg_client`, which binds psycopg 3. |
| `superschematic_versiongraph.sqlite` | `SqliteAdapter`, `sqlite_layout`, its `Client` protocol, and `sqlite_client`, which binds the standard library's `sqlite3`. |
| `superschematic_versiongraph.facade` | `VersionGraphFacade`, which each generated `<Name>Graph` extends, and the types it returns. |

The operations are synchronous: each runs in one transaction and returns
when it commits. psycopg 3 is the `postgres` extra
(`superschematic-versiongraph[postgres]`); no module imports it until
`psycopg_client` is called, so the core and the engine load without it. A
`Client` runs a transaction, and a `query` inside it that takes Postgres's
own `$1` placeholders and returns every column as the text Postgres
writes, so no driver's type adaptation touches a date, a time, an
interval, a numeric or a bigint. `psycopg_client` runs each statement
through a raw cursor and reads the libpq result as text. Over a
`psycopg.Connection` it runs transactions one at a time; when the caller
already holds a transaction on the connection, the adapter's is a
savepoint of it. Over a `psycopg_pool.ConnectionPool` each transaction
takes a connection of its own. Another driver implements `Client` itself.

```python
import psycopg
from superschematic_versiongraph.engine import Engine, KindEdits
from superschematic_versiongraph.postgres import PostgresAdapter, psycopg_client

connection = psycopg.connect(url, autocommit=True)
adapter = PostgresAdapter(descriptor, history_actor_setting="superschematic.history_actor_id")
engine = Engine(descriptor, adapter.storage(psycopg_client(connection)), schema_epoch=1, snapshot_every=32)

main = engine.create_primary(actor, root, "main")
draft = engine.branch(actor, main.id, "draft")
saved = engine.save(actor, draft.id, draft.version, {
    "step": KindEdits(upsert=['{"entity_key": null, "position": 1, "instruction": "Mix", "timings": {}}']),
})
```

The engine's methods take the Go engine's arguments in the same order,
spelled in snake case, with the actor first on every write. A canonical
row is JSON text, as Go's `json.RawMessage` is, so a wide integer or
numeric keeps its digits: a tree is `Dict[str, List[str]]`, and a
conflict's values, a change's row and a resolution's `value` are JSON text.
Durations are `datetime.timedelta`. `run_sweeper(interval, options,
on_pass, stop)` runs until its `threading.Event` is set and then returns.
It runs no pass when the event is already set, and a pass under way when
it is set finishes first, where Go's `RunSweeper` cancels that pass through
its context. The named errors are exception classes with the stable `code`
the scenario files name (`VersionConflictError` is a `NotFoundError`, as
in Go); `error_code(err)` returns it, or the core's code for an input the
core refused.

`SqliteAdapter` keeps a graph in a SQLite file with the standard library
alone. It is the TypeScript SQLite adapter
([above](#the-sqlite-adapter)), statement for statement and stored form
for stored form, so a file either writes the other reads.
`sqlite_client(connection)` binds a `sqlite3.Connection` opened with
`isolation_level=None` (or `autocommit=True` from Python 3.12), so the
module begins no transaction on its own: the client begins each with
`BEGIN IMMEDIATE`, and runs one begun inside another, or while the caller
holds a transaction on the connection, as a savepoint of it. It turns the
connection's foreign keys on and refuses a SQLite older than 3.37.0, the
first with `STRICT` tables. A transaction's clock is the system's, in whole
microseconds, unless `clock` gives another.

```python
import sqlite3
from superschematic_versiongraph.engine import Engine
from superschematic_versiongraph.sqlite import SqliteAdapter, sqlite_client

client = sqlite_client(sqlite3.connect("recipes.sqlite", isolation_level=None))
adapter = SqliteAdapter(descriptor, graph="recipe")
adapter.create_tables(client)
engine = Engine(descriptor, adapter.storage(client), schema_epoch=1, snapshot_every=32)
```

When a schema declares a graph and its Python types are on, pygen writes
`<module>/versiongraph_<name>.py` beside the types, and the package
depends on the distribution the naming key
[`versiongraph_pypi_dist`](/superschematic/reference/naming/#versiongraph_pypi_dist)
names and imports the module
[`versiongraph_python_module`](/superschematic/reference/naming/#versiongraph_python_module)
names, from a uv path source at
[`[paths] versiongraph_python`](/superschematic/reference/naming/#pathsversiongraph_python)
when that is set. The file holds `RECIPE_GRAPH_DESCRIPTOR`, the schema
epoch and snapshot interval, the typed `RecipeTree` and `RecipeEdits`, and
`RecipeGraph`:

```python
from schemas_types_recipes_db.types import Step
from schemas_types_recipes_db.versiongraph_recipe import RecipeEdits, RecipeGraph
from superschematic_versiongraph.facade import CommitOptions, TypedKindEdits, psycopg_client

graph = RecipeGraph(psycopg_client(pool), actor=user_id)
draft = graph.branch(main_id, "rest longer")
saved = graph.save(draft.id, draft.version, RecipeEdits(step=TypedKindEdits(upsert=[mix])))
committed = graph.commit(draft.id, saved.ref.version)
merged = graph.merge(draft.id, main_id, main_version, options=CommitOptions("rest longer", True))
released = graph.released(recipe_id).tree
```

`RecipeGraph` has the Go facade's operations, from `create_primary` to
`run_sweeper`, plus `with_actor` and `with_walk_ceiling`. Every write
records the facade's `actor` and fails with `NoActorError` without one; a
sweep writes as its options' `actor`. It turns a typed value into a
canonical row through the model's JSON (`model_dump(mode="json",
by_alias=True)`) and back through the model's `model_validate_json`, so a
field comes back in its canonical form: an instant as a `datetime` in UTC,
a time of day as `HH:MM:SS`, a duration in the scalar core's form and a
UUID in base62. A typed row read back leaves its to-one relations (the
root, the ref) `None`, since a canonical row holds only their keys. There
is no Python ORM, so refs, commits and release pointers come back as the
engine's `Ref`, `Commit` and `Release`.

```
make versiongraph-scenarios-python   # every scenario and the SQLite adapter's tests on SQLite; then every scenario, the canonical vectors against Postgres, the adapter's, the sweeper's and the facade's tests
```

The SQLite pass needs no database server. The Postgres pass needs
`SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL`, and the target fails
without it once the SQLite pass has run; CI runs it in the versiongraph
job. `make python` runs the engine's tests that need no database, the
SQLite ones among them, on the default Python and on 3.9.

## Limits

- History is linear per row; a branch exists because each ref writes its
  own rows.
- The core reads whole trees, and `Materialize` walks commits back to the
  nearest snapshot, up to its ceiling.
- A member's `parent.of` names one type; a parent of several types is not
  supported.
- `schemaEpoch` is recorded and checked, but nothing transforms a commit
  from an older epoch.
- The compiler emits DDL, not migrations.
- Who may commit, seal, merge, tag or release is the application's policy.
  `Sweep` collects discarded drafts and prunes history, but nothing runs it
  unless a service calls it or `RunSweeper`.
