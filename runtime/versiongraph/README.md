# Version-graph core

The version-graph core composes, merges, diffs, hashes and validates trees
of versioned rows (D17 in `docs/DECISIONS.md`). It is one Rust crate with
no IO, clock or randomness. Its operations take one JSON document and return
one. A graph descriptor tells it which columns of each kind's rows play which
role, so it holds no per-kind code.

```
rust/             superschematic-versiongraph: the core and its C ABI (rlib, staticlib, cdylib)
go/               the Go binding, a module of its own (package versiongraph): cgo over the static archive
wasm/             bun test that drives the wasm32-unknown-unknown build
testdata/vectors/ the contract as vectors: {name, op, input, expect}
```

This page is the contract. The vectors are its executable form: the Rust
tests, the Go binding and the wasm test run every one of them.

## Descriptor

```json
{
  "graph": "recipe",
  "kinds": [
    {
      "kind": "step",
      "key": "entity_key",
      "id": "id",
      "ref": "ref",
      "tombstone": "deleted_on_ref",
      "version": "_version",
      "author": "updated_by",
      "parent": { "key": "parent_step_key", "kind": "step" },
      "order": "position",
      "units": { "timings": "keyed", "inputs": "jsonSchema" },
      "excluded": ["created_at", "created_by", "updated_at", "recipe_id"]
    },
    {
      "kind": "ingredient",
      "key": "entity_key", "id": "id", "ref": "ref",
      "tombstone": "deleted_on_ref", "version": "_version", "author": "updated_by",
      "parent": { "key": "step_key", "kind": "step" },
      "excluded": ["created_at", "created_by", "updated_at", "recipe_id"]
    },
    {
      "kind": "cover",
      "key": "entity_key", "id": "id", "ref": "ref",
      "tombstone": "deleted_on_ref", "version": "_version",
      "singleton": true
    }
  ]
}
```

| Member | Meaning |
|---|---|
| `graph` | Optional. The graph's name; the core does not read it. |
| `kinds[].kind` | The kind's name: the tree member that holds its rows. Unique. |
| `key` | The entity key column: the logical identity rows are matched on. Its value is a non-empty string. |
| `id` | The row id column. |
| `ref` | The column naming the ref the row was written on. |
| `tombstone` | A boolean column; `true` marks the row as the entity's delete. Absent or `null` is `false`. |
| `version` | The row version column. |
| `author` | Optional. The column naming who wrote the row; conflicts report it. |
| `parent` | Optional. `key` is a column holding the parent row's entity key (a string, or `null` for none), and `kind` is the parent's kind, which may be the kind itself. |
| `order` | Optional. An integer column that orders siblings. |
| `singleton` | Optional, default `false`. At most one live row. |
| `units` | Optional. A conflict unit per column: `atomic` (the default), `keyed` or `jsonSchema`. |
| `excluded` | Optional. Columns that are not content: audit columns and the graph's own columns. |

The role columns (`key`, `id`, `ref`, `tombstone`, `version`, `author`)
must be distinct and are never content. Every other column of a row is
content unless it is excluded. Only content is compared, merged, diffed and
hashed, so two rows with equal content are the same version whatever their
ids, refs, versions and audit columns say. Role columns may also be listed in
`excluded`. The `parent` key and `order` columns must be content, and a unit
may only name a content column. Unknown members are refused.

## Trees and rows

A tree is `{"<kind>": [row, ...]}`. A kind the descriptor lacks is refused;
a missing kind has no rows. A row is a JSON object keyed by column name, as
Postgres `to_jsonb(row)` renders it: the form of a history image, so a live
row and its history image compare and hash the same. Numbers keep the digits
they were written with, so a `numeric` wider than a double survives.

A row whose tombstone is `true` is a delete. Everywhere but `compose`, a
tombstone row and an absent row are the same deleted state.

An order must be an integer inside +/-(2^53-1), the integers a JavaScript
number holds exactly; outside it, a browser would sort rows differently from
the server.

## Operations

Every output is deterministic: kinds follow descriptor order, rows sort by
order and then entity key, and lists sort by kind, entity key and path.

### compose

Input `{"descriptor", "base", "overlay"}`, output `{"tree", "findings"}`.

Lays one ref's rows (`overlay`) over a base tree by entity key. A live
overlay row replaces the base row. A tombstone removes the entity, in the
overlay or in the base. A removed entity removes its descendants through the
winning rows' parent keys, including children added under it later. A row
whose parent is absent without having been removed is kept and reported as an
`absent_parent` finding. The tree has every kind of the descriptor and no
tombstones.

### merge

Input `{"descriptor", "base", "ours", "theirs", "resolutions"?}`, output
`{"merged", "conflicts", "entities"}`.

A three-way merge per entity, then per unit:

- An entity changed on one side only takes that side, including a delete.
  Equal changes agree. Deleted on both sides agrees.
- An edit against a delete conflicts at path `""` (the whole entity).
- Two different edits of a live entity, or two different adds of one entity
  key, merge column by column. A unit changed differently on both sides
  conflicts; an add has no base, so every unit is absent in it.

Units, and their paths (JSON Pointers into the row):

| Unit | Path | Merges |
|---|---|---|
| `atomic` | `/<column>` | The whole value. |
| `keyed` | `/<column>/<key>` | Each top-level key of an object. When a side's value is not an object, the column is one unit. A `null` or absent base is an empty object. |
| `jsonSchema` | `/<column>/...` | A JSON Schema object: each entry of `properties` is a schema node of its own, recursively (`/<column>/properties/<name>`); each name's membership in `required` is a boolean unit; every other keyword is atomic (`/<column>/<keyword>`). A `null` or absent base node is an empty object. |

In a JSON Schema, removing a property conflicts with a concurrent edit under
it, and so does retyping one: a node whose `type` or `$ref` one side changed
merges only when the other side left the node alone or made the same change.
A membership unit never conflicts. The merged `required` keeps ours' order,
then the names only theirs added.

A conflict is `{"kind", "entityKey", "path", "base"?, "ours"?, "theirs"?,
"oursAuthor"?, "theirsAuthor"?}`. `base`, `ours` and `theirs` are the unit's
values, each left out where the unit is absent; for path `""` they are whole
rows, left out on a deleted side. The authors are each side's `author`
column, tombstones included.

`resolutions` settle conflicts by unit path:
`{"kind", "entityKey", "path", "take"}` takes the unit from `"base"`,
`"ours"` or `"theirs"` (a unit absent there is removed), and
`{"kind", "entityKey", "path", "value"}` gives the value (JSON `null` is a
value). A whole-entity conflict takes a side. A resolution must match a
conflict of this merge exactly; one that does not is refused, as are two for
one unit.

`merged` is a tree of every entity's result: the winning row, a winning
delete's tombstone when that side has one, or, for a unit-level merge, ours'
row with the merged content. A conflicted entity is not in it, so apply
`merged` only when `conflicts` is empty. `entities` lists every entity as
`{"kind", "entityKey", "side", "deleted"?}`: `side` is `ours` when the result
equals ours (nothing to write onto ours), `theirs` when it equals theirs,
`merged` when it equals neither, and `conflict` when a conflict is left;
`deleted` is `true` when the result is a delete. The merge does not check the
singleton rule or parent edges across entities; run `validate` on the
result.

### diff

Input `{"descriptor", "from", "to"}`, output `{"changes"}`.

Each change is `{"kind", "entityKey", "operation", "row"?}`. An entity live
only in `to` is an `ADD` and one live in both with different content an
`UPDATE`, both with `to`'s row. One live only in `from` is a `DELETE`, with
`to`'s tombstone row when it has one.

### content_hash

Input `{"descriptor", "tree"}`, output `{"contentHash"}`: SHA-256, as
lowercase hex, of the canonical JSON (object keys sorted by code point, no
insignificant whitespace, numbers as written) of

```
{"<kind>": [{"entityKey": "...", "content": {<content columns>}}, ...], ...}
```

with each kind's live rows sorted by entity key. Tombstone rows are left
out, as are kinds with no live rows, so a delete hashes as an absence and a
kind added to the descriptor does not move existing hashes.

### validate

Input `{"descriptor", "tree"}`, output `{"findings"}`, empty for a valid
tree. It checks, per kind: one row per entity key (`duplicate_entity_key`),
at most one live row of a singleton kind (`singleton`, with no `entityKey`),
every live row's parent live in the tree (`absent_parent`), no parent cycles
(`parent_cycle`, one per entity on a cycle), and every order in range
(`order_out_of_range`). A finding is `{"code", "kind", "entityKey"?,
"message"}`.

## Errors

A refused input returns `{"error": {"code", "message"}}`. `code` is stable;
`message` names the offending part of the input.

| Code | When |
|---|---|
| `invalid_json` | The input is not JSON. |
| `invalid_request` | The input is not an object, a member is missing or unknown, or a tree is not an object of arrays. |
| `invalid_descriptor` | The descriptor is malformed or breaks a rule above. |
| `unknown_kind` | A tree names a kind the descriptor lacks. |
| `invalid_row` | A row is not an object, or its key, tombstone, parent key or order has the wrong type. |
| `duplicate_entity_key` | Two rows of a kind share an entity key (every operation but `validate`, which reports it). |
| `order_out_of_range` | An order outside +/-(2^53-1) (every operation but `validate`, which reports it). |
| `invalid_resolution` | A resolution is malformed, names an unknown kind, or repeats a unit. |
| `unmatched_resolution` | A resolution matches no conflict of the merge. |
| `internal` | The core panicked (a bug). |

## C ABI

The native library and the wasm module export the same functions:

```c
int32_t vg_compose(const uint8_t *in_ptr, size_t in_len, uint8_t **out_ptr, size_t *out_len);
int32_t vg_merge(...);   /* same signature */
int32_t vg_diff(...);
int32_t vg_content_hash(...);
int32_t vg_validate(...);
void vg_free(uint8_t *ptr, size_t len);
/* wasm32 only */
uint8_t *vg_alloc(size_t len);
void vg_dealloc(uint8_t *ptr, size_t len);
```

An operation reads `in_len` bytes of JSON at `in_ptr` and writes its output
document's pointer and length through `out_ptr` and `out_len`. It returns 0
with the operation's output, 1 with an error document, or 2 when an out
pointer is null, writing nothing. Release each output with `vg_free`. A wasm
host allocates its input and the two out slots with `vg_alloc` and releases
them with `vg_dealloc`. `go/include/versiongraph.h` is the header.

## Build and test

```
make versiongraph            # static archive for the Go binding (scripts/versiongraph-archive.sh)
make rust                    # fmt, clippy (native and wasm32) and cargo test, then the wasm build and bun test
cd runtime/versiongraph/go && go test ./...
UPDATE_VECTORS=1 cargo test  # in rust/: rewrite every vector's expect; review the diff
```

The Go binding links `libsuperschematic_versiongraph.a` from
`go/lib/<goos>_<goarch>`, which `make versiongraph` stages; the Makefile
also puts that directory in `CGO_LDFLAGS`, as it does superscalar's. A
consumer outside this checkout sets `-L<dir>` in `CGO_LDFLAGS` the same way.
