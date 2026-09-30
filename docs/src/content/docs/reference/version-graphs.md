---
title: Version graphs
description: Declare a version graph over versioned DB tables with @versionGraph, @graphMember and @conflictUnit; the tables the loader adds, the merge core and its JSON contract, the Go engine and its Postgres adapter with merge-only primary lines, releases, rebase, snapshots and the sweep, the generated Go facade, how a consumer links the core, and the core, the engine and the generated facade from TypeScript.
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
validates trees. The Go and TypeScript engines run every graph operation
on the core over a storage adapter, and a generated facade in each language
gives each graph typed methods over its engine.

The design and the alternatives not taken are D17 and D19 in
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

@versioned({ retentionDays: 365 })
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
| `content_hash` | `{descriptor, tree}` | `{contentHash}`: SHA-256 over the canonical JSON of each kind's content columns, rows sorted by entity key. |
| `validate` | `{descriptor, tree}` | `{findings}`: duplicate entity keys, the singleton rule, absent parents, parent cycles, orders outside the integers a JavaScript number holds exactly. |

A tree is `{"<kind>": [row, ...]}`, and a row is a canonical row: a JSON
object keyed by column name whose values are the schema runtime's JSON for
each field's type, in one form per value class (below), so a live row and
its history image hash the same whatever database stored them. A refused
input returns `{"error": {"code", "message"}}` with a stable code. The
contract, with the descriptor's members, every rule, the error codes and the
C ABI, is
[runtime/versiongraph/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md).
Its vectors in `runtime/versiongraph/testdata/vectors` are the executable
form: the Rust tests, the Go binding and the TypeScript package's tests run
every one.

The same exports are built three ways: a static archive, which the Go
binding `runtime/versiongraph/go` (package `versiongraph`) links through
cgo; a `cdylib`; and `wasm32-unknown-unknown`, which the TypeScript package
`@superschematic/versiongraph` ships
([Use the core from TypeScript](#use-the-core-from-typescript)).

### The descriptor

The ORM generator builds each graph's descriptor from the IR and writes it
twice: as the constant `<Name>GraphDescriptor` in the ORM package, and as
`versiongraph/<name>.json` in the Go types module, where a browser or
another runtime reads the same one. Writing the types module removes each
`versiongraph/*.json` that no graph of the schema writes and leaves any
other file there.

The descriptor is version 2, and the core refuses any other. Besides each
kind's roles, it names the graph's tables and gives every column of each
kind's table a value class, which a storage adapter reads to build its
statements and to normalize the rows it reads. `root` names the column
that holds the root's key, which the adapter writes on every row; the core
does not read it:

```json
{
  "version": 2,
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
operation.

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
one replica sweeps at a time.

`engine.ErrorCode(err)` names an error with a code every language's engine
shares (`version_conflict`, `ref_sealed`, `primary_merge_only`,
`not_tagged`, `no_parent`, and the core's codes). The
scenarios in `runtime/versiongraph/testdata/scenarios` run sequences of
operations over canonical rows, with the expected trees, content hashes,
conflicts and errors, against the fixture in
`runtime/versiongraph/testdata/fixture`; the Go engine runs every one
against Postgres. Their format is in
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
of its writes; a sweep writes as `GraphSweepOptions.Actor`. Every write through a ref takes the ref's expected
`_version` and fails with `ErrVersionConflict` when the ref has moved on.
A ref or commit that does not exist, or a discarded ref, is `ErrNotFound`.

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
| `@superschematic/versiongraph/engine` | `Engine`, the storage interface (`Storage`, `Tx`), the named errors and `errorCode`, and the canonical rules (`canonicalRow`, `canonicalValue`) with the exact JSON codec they read with. |
| `@superschematic/versiongraph/postgres` | `PostgresAdapter`, its `Client` interface, and `pgPool` and `pgClient`, which bind the npm package `pg`. |
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
`RunSweeper` cancels that pass through its context. The named errors are classes with the stable `code`
the scenario files name (`VersionConflictError` is a `NotFoundError`, as
in Go); `errorCode(err)` returns it, or the core's code for an input the
core refused.

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
make versiongraph-scenarios-ts   # every scenario, the canonical vectors against Postgres, the adapter's, the sweeper's and the facade's tests
```

The target needs `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` and fails
without it; CI runs it in the versiongraph job.

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
