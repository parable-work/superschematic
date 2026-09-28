---
title: Version graphs
description: Declare a version graph over versioned DB tables with @versionGraph, @graphMember and @conflictUnit; the tables the loader adds, the merge core and its JSON contract, the generated Go shell, and how a consumer links the core.
sidebar:
  order: 8
---

A version graph keeps a tree of rows under one stable identity, the root,
and versions the tree as a whole. The tree's rows live in
[versioned tables](/superschematic/reference/versioned-tables/). A ref is a
line of work: a primary line, or a change set branched from another ref.
Each ref holds only the rows it overrides. A commit records the exact row
versions a ref sealed. One core composes, merges, diffs, hashes and
validates trees, and the generated Go shell drives it against Postgres.

The design and the alternatives not taken are D17 in
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

@versionGraph({ schemaEpoch: 1 })
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
| `@versionGraph({ name?, schemaEpoch? })` | Marks the root. The root is never overlaid and is in no commit. `name` prefixes the generated types (PascalCase) and tables (snake_case) and defaults to the root's name. `schemaEpoch`, default 0, is recorded on every commit. |
| `@graphMember({ graph, parent?, order?, singleton? })` | Marks an entity kind of the graph. `graph` is the root class. |
| `parent: { key, of }` | Containment: the UUID field `key` holds the parent row's entity key, and `of` is a member of the same graph, the member itself included. Deleting a parent removes its descendants. |
| `order` | A `Generic.Int64` field that orders siblings. |
| `singleton: true` | At most one live row of the kind per ref. |
| `@conflictUnit(strategy)` | On a member's field: its merge unit. The strategies are below. |

Verification refuses:

- a root that is not a DB table, has other than one UUID `@key`, has no
  member, or whose `name` is not PascalCase; a negative `schemaEpoch`;
- a member that is not `@versioned`, has other than one UUID `@key`, has
  other than exactly one relation to its root, has `deletedAt`, or declares
  `entityKey`, `ref` or `deletedOnRef`;
- a type that is a root and a member, or a member of a graph the schema
  does not declare;
- a `parent.of` that is not a member of the same graph, a `parent.key`
  that is not a UUID field, and an `order` that is not a `Generic.Int64`
  field;
- a graph whose generated names collide with a definition of the schema or
  of another graph;
- `@conflictUnit` outside a member, an unknown strategy, and `keyed` or
  `jsonSchema` on a field that is not one JSON object (`Generic.JSON` or a
  `@jsonField` type);
- a member that excludes content from history with
  `@versioned({ exclude })`: it may exclude only its audit fields and
  fields with `@conflictUnit("excluded")`.

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
| `RecipeEntityKind`, `RecipePatchOperation` | One value per member (`step`, `ingredient`), and `ADD`, `UPDATE`, `DELETE`. |
| On each member | `entityKey`, the logical identity, generated on insert; `ref` (`RESTRICT`); `deletedOnRef`, default false; a unique index on `(entityKey, ref)`; and, with `retentionDays`, a `pruneKeepReferencedBy` pin on `recipe_patch(entity_id, entity_version)`, so pruning never deletes a row version a commit names. |

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

A tree is `{"<kind>": [row, ...]}`, and a row is the JSON Postgres
`to_jsonb` gives it, keyed by column name: the form of a history image, so
a live row and its history image hash the same. A refused input returns
`{"error": {"code", "message"}}` with a stable code. The contract, with
the descriptor's members, every rule, the error codes and the C ABI, is
[runtime/versiongraph/README.md](https://github.com/parable-work/superschematic/blob/main/runtime/versiongraph/README.md).
Its vectors in `runtime/versiongraph/testdata/vectors` are the executable
form: the Rust tests, the Go binding and a bun test of the wasm build run
every one.

The same exports are built three ways: a static archive, which the Go
binding `runtime/versiongraph/go` (package `versiongraph`) links through
cgo; a `cdylib`; and `wasm32-unknown-unknown` for the browser
(`make versiongraph-wasm`).

### The descriptor

The ORM generator builds each graph's descriptor from the IR and writes it
twice: as the constant `<Name>GraphDescriptor` in the ORM package, and as
`versiongraph/<name>.json` in the Go types module, where a browser or
another runtime reads the same one. Writing the types module removes each
`versiongraph/*.json` that no graph of the schema writes and leaves any
other file there.

```json
{
  "graph": "recipe",
  "kinds": [
    {
      "kind": "ingredient",
      "key": "entity_key", "id": "id", "ref": "ref_id",
      "tombstone": "deleted_on_ref", "version": "_version",
      "parent": { "key": "step_key", "kind": "step" },
      "excluded": ["recipe_id"]
    },
    {
      "kind": "step",
      "key": "entity_key", "id": "id", "ref": "ref_id",
      "tombstone": "deleted_on_ref", "version": "_version",
      "author": "updated_by",
      "order": "position",
      "units": { "timings": "keyed" },
      "excluded": ["recipe_id", "scratch", "updated_at"]
    }
  ]
}
```

## The generated shell

When a DB schema declares a graph, the Go ORM generator writes
`versiongraph_<name>.go` beside the repositories, and a shared
`versiongraph.go`. `db.RecipeGraph()` returns a typed `RecipeGraph`. Every
method runs in one transaction and needs a user in the context
(`WithUserID`). Every write through a ref takes the ref's expected
`_version` and fails with `ErrVersionConflict` when the ref has moved on.
A ref or commit that does not exist, or a discarded ref, is `ErrNotFound`.

| Method | Does |
| --- | --- |
| `CreatePrimary(ctx, root, name)` | Creates a primary line of `root`. |
| `Branch(ctx, fromRef, name)` | Creates a change set of `fromRef` whose base is `fromRef`'s head commit. |
| `Save(ctx, ref, version, RecipeEdits)` | Applies each kind's `GraphEdits[T]`: `Upsert` writes each row as the ref's override of its entity, found by `EntityKey` (a row without one is a new entity, whose key the database generates). Every ref holds its own row of an entity, so the row's id is never the caller's: Save ignores it and the table's default generates it, whether the `@key` is `AutoGenerate<Identity.UUID>` or a plain `Identity.UUID`. `Delete` writes a row that deletes the entity on the ref; `Unset` removes the ref's own row, so the ref reads the entity through its base again. A sealed ref refuses it with `ErrRefSealed`. |
| `Commit(ctx, ref, version, RecipeCommitOptions)` | Composes the ref, checks the tree with `validate`, diffs it against the ref's last commit (or its base), and writes a commit with one patch per changed entity pinning the winning row's `(id, _version)`. Moves the ref's head. `Message` is stored; `Tag` takes the root's next `sequence`, which makes the commit a published version. `ErrNothingToCommit` when nothing changed; an `*InvalidTreeError` (`ErrInvalidTree`) lists what `validate` found. |
| `Seal(ctx, ref, version)` | Commits when there are changes and sets `sealedAt`. The ref then refuses writes. |
| `Merge(ctx, source, target, targetVersion, resolutions)` | Merges the source's head commit into the target against the source's base. Without conflicts it writes the result onto the target and commits. With conflicts left after `resolutions` it returns them as `[]RecipeConflict` (kind, entity key, unit path, the base, ours and theirs values, and each side's author) and writes nothing. A `RecipeResolution` takes a side (`versiongraph.Take`) or gives a value for one conflict's path. |
| `Revert(ctx, ref, version, toCommit)` | Writes the rows that make the ref compose to `toCommit`'s tree, and commits. History is never rewritten. |
| `Materialize(ctx, commit)` | Reads a commit's tree by walking its parents: each entity's nearest patch wins, and a `DELETE` removes it. |
| `Compose(ctx, ref)` | Reads a ref's tree: its base commit's tree with its own rows laid over it. |
| `Diff(ctx, from, to)` | The `[]RecipeChange` between two commits' trees. |
| `History(ctx, ref)` | The commits the ref wrote, newest first. |
| `Discard(ctx, ref, version)` | Soft-deletes the ref, which frees its name. |

A tree comes back as `RecipeTree`: a slice of typed rows per kind, the
content hash and any compose findings. Rows reach the core as `to_jsonb`
of live rows and as history images of committed ones, and return through
`jsonb_populate_record`, so no language's own JSON form of a row takes
part in a hash or a merge.

`Materialize` stops with `ErrWalkCeiling` after `DefaultWalkCeiling`
(4096) commits; `g.WithWalkCeiling(n)` returns a graph with another
ceiling. It refuses a commit whose `schemaEpoch` is newer than the one the
ORM was generated with (`RecipeGraphSchemaEpoch`) with `ErrSchemaEpoch`.
The other named errors are `ErrEntityNotFound` (deleting or unsetting an
entity the ref does not hold), `ErrHistoryMissing` (a row version a commit
names is gone from history) and `ErrRootMismatch` (two refs, or a ref and a
commit, of different roots).

## Link the core

An ORM whose schema declares a graph imports the Go binding, the module
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

## Limits

- History is linear per row; a branch exists because each ref writes its
  own rows.
- The core reads whole trees, and `Materialize` walks commits up to its
  ceiling.
- A member's `parent.of` names one type; a parent of several types is not
  supported.
- `schemaEpoch` is recorded and checked, but nothing transforms a commit
  from an older epoch.
- The wasm build has no TypeScript package over it yet; a browser loads
  the module and calls its exports itself.
- The compiler emits DDL, not migrations.
- Who may commit, seal, merge or tag, and when a discarded draft is
  collected, is the application's policy. The generated prune functions
  have no scheduler.
