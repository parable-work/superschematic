# Version-graph core

The version-graph core composes, merges, diffs, hashes and validates trees
of versioned rows (D17, D19 and D32 in `docs/DECISIONS.md`). It is one Rust crate
with no IO, clock or randomness. Its operations take one JSON document and
return one. A graph descriptor tells it which columns of each kind's rows
play which role, so it holds no per-kind code.

```
rust/               superschematic-versiongraph: the core and its C ABI (rlib, staticlib, cdylib)
go/                 the Go binding, a module of its own (package versiongraph): cgo over the static archive
go/canonical/       package canonical: Postgres renderings to canonical rows, plain Go
go/storage/         package storage: the storage adapter interface the engine runs over, plain Go
go/engine/          package engine: the Go engine, every graph operation over a storage adapter and the binding
go/postgres/        package postgres: the Postgres storage adapter, with a pgx binding
rust-engine/        superschematic-versiongraph-engine: the Rust engine, storage traits and Postgres adapter
typescript/         @superschematic/versiongraph: the wasm32-unknown-unknown build with typed operations, and the
                    TypeScript engine (./engine), its Postgres adapter (./postgres), its SQLite adapter (./sqlite)
                    and the facade base (./facade)
python/             superschematic-versiongraph (module superschematic_versiongraph): the Python binding, a PyO3
                    extension over the core built with maturin, with typed operations, and the Python engine
                    (engine), its Postgres adapter (postgres) and the facade base (facade)
testdata/vectors/   the core's contract as vectors: {name, op, input, expect}
testdata/canonical/ the canonical row contract as vectors: {cases} per class, {rows}
testdata/fixture/   the scenarios' graph: fixture-version-graph-db's descriptor and Postgres DDL
testdata/scenarios/ the engines' contract as scenarios: {name, description, roots, steps}
testdata/sqlite/    the SQLite adapters' contract: the layout, a file the TypeScript adapter wrote, and its reads
```

This page is the contract. The vectors are its executable form: the Rust
tests, the Go binding, the TypeScript package's tests and the Python
package's tests run every core vector, and package `canonical`, the Rust
engine's module `canonical` and the Python package's module `canonical`
run every canonical vector. The scenarios are the engines' contract: the
Go, TypeScript, Rust and Python engines run every one through their
Postgres adapters, and the TypeScript engine runs every one through its
SQLite adapter too. The SQLite vectors are the SQLite adapters' contract,
which the TypeScript package's tests check. The package's
types for this contract are `typescript/src/contract.ts`.

## Descriptor

The descriptor is version 3. The core refuses any other version with
`invalid_descriptor`, version 2 included: a version 2 descriptor does not
say what each kind's history keeps, and reading it as keeping everything
forever would be wrong for a kind that prunes or leaves columns out. A
descriptor without `version` is version 1, which nothing has shipped.

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
      "kind": "step",
      "table": "step",
      "historyTable": "step_history",
      "key": "entity_key",
      "id": "id",
      "ref": "ref_id",
      "root": "recipe_id",
      "tombstone": "deleted_on_ref",
      "version": "_version",
      "author": "updated_by",
      "parent": { "key": "parent_step_key", "kind": "step" },
      "order": "position",
      "units": { "timings": "keyed", "inputs": "jsonSchema" },
      "excluded": ["created_at", "created_by", "updated_at", "recipe_id"],
      "history": { "retentionDays": 365, "exclude": ["created_by"], "actor": "updated_by" },
      "columns": {
        "_version": "integer", "created_at": "dateTime", "created_by": "uuid",
        "deleted_on_ref": "boolean", "entity_key": "uuid", "id": "uuid",
        "inputs": "json", "parent_step_key": "uuid", "position": "integer",
        "recipe_id": "uuid", "ref_id": "uuid", "timings": "json",
        "title": "string", "updated_at": "dateTime", "updated_by": "uuid"
      }
    },
    {
      "kind": "cover",
      "table": "cover", "historyTable": "cover_history",
      "key": "entity_key", "id": "id", "ref": "ref_id", "root": "recipe_id",
      "tombstone": "deleted_on_ref", "version": "_version",
      "singleton": true,
      "excluded": ["recipe_id"],
      "history": { "exclude": [] },
      "columns": {
        "_version": "integer", "deleted_on_ref": "boolean", "entity_key": "uuid",
        "id": "uuid", "photo_url": "string", "recipe_id": "uuid", "ref_id": "uuid",
        "tags": "string[]"
      }
    }
  ]
}
```

The Go ORM generator builds each graph's descriptor from the IR and writes
it twice: as the constant `<Name>GraphDescriptor` beside the generated
facade (`versiongraph_<name>.go`) in the ORM package, and as
`versiongraph/<name>.json` in the Go types module.

| Member | Meaning |
|---|---|
| `version` | `3`. |
| `graph` | Optional. The graph's name; the core does not read it. |
| `root` | The graph root's `table` and its `key` column. |
| `refTable`, `commitTable`, `patchTable`, `releaseTable`, `snapshotTable` | The tables of the graph's refs, commits, patches, release pointers and snapshot entries. Each is non-empty; the core does not read them. |
| `kinds[].kind` | The kind's name: the tree member that holds its rows. Unique. |
| `table`, `historyTable` | The table that holds the kind's rows, and the one that holds their history images. |
| `key` | The entity key column: the logical identity rows are matched on. Its value is a non-empty string. |
| `id` | The row id column. |
| `ref` | The column naming the ref the row was written on. |
| `root` | Optional for the core, which does not read it; a storage adapter needs it. The column holding the graph root's key, which the adapter writes on every row. |
| `tombstone` | A `boolean` column; `true` marks the row as the entity's delete. Absent or `null` is `false`. |
| `version` | The row version column. |
| `author` | Optional. The column naming who wrote the row; conflicts report it. |
| `parent` | Optional. `key` is a column holding the parent row's entity key (a string, or `null` for none), with the class of the parent kind's `key` column, and `kind` is the parent's kind, which may be the kind itself. |
| `order` | Optional. An `integer` column that orders siblings. |
| `singleton` | Optional, default `false`. At most one live row. |
| `units` | Optional. A conflict unit per column: `atomic` (the default), `keyed` or `jsonSchema`. `keyed` and `jsonSchema` need a `json` column. |
| `excluded` | Optional. Columns that are not content: audit columns and the graph's own columns. |
| `history` | What the kind's history keeps (below). Required. |
| `columns` | Every column of the kind's table, with its value class (below). Every column another member names must be here. |

The role columns (`key`, `id`, `ref`, `root`, `tombstone`, `version`,
`author`) must be distinct and are never content. Every other column of a
row is content unless it is excluded. Only content is compared, merged,
diffed and hashed, so two rows with equal content are the same version
whatever their ids, refs, versions and audit columns say; a content column a
row lacks is `null` to the core ([Trees and rows](#trees-and-rows)). Role
columns may also be listed in `excluded`. The `parent` key and `order`
columns must be content, and a unit may only name a content column. Unknown
members are refused, and so is an empty table or column name.

`history` holds what the sql generator's history trigger and prune
function hold for the kind's table, for a storage adapter that writes
history itself rather than through triggers (D32). The generator computes
both from the same code (`sqlutil.VersionedHistory`), so they never
disagree. The Postgres adapters do not read it, since their triggers and
prune functions hold the same facts.

| Member | Meaning |
|---|---|
| `retentionDays` | Optional. How many days of history pruning keeps, `@versioned({ retentionDays })`: an integer from 1 to 2147483647, the largest Postgres `INTEGER`, which the prune function's `retention_days` is. Absent for no retention, and then nothing is pruned. |
| `exclude` | The columns every history image leaves out, `@versioned({ exclude })`, in the order it names them; `[]` for none. Each is a column of the kind that is not content (it is `excluded` or the `author`) and is not one of the role columns a history image is found and read by (`key`, `id`, `ref`, `root`, `tombstone`, `version`), and none is named twice. |
| `actor` | Optional. The column a delete's image names its actor in: `deleted_by` when the kind has it, else `updated_by`. Absent when the kind has neither, or when that column is in `exclude`, since a delete's image leaves it out too. It is a column of the kind, not in `exclude`, and not one of those role columns; it may be the `author`. |

The core checks `history` against the kind's columns and does not read it
otherwise. An unknown member is refused, and so is a `null` member.

The tables and the value classes are for a storage adapter, which builds its
statements from them and normalizes each row it reads (below). The core
checks that they are there and fit the roles; it does not check a row's
values against their classes, and a row may carry a column `columns` lacks.

### Value classes

A value class names the rule that gives a value's canonical JSON (below).
The generator derives it from two things: what the schema runtime's JSON
for the field's type is (`runtime/schema`, D12 and D14), and the SQL type
the sql generator stores the column as, which is the scalar's `sql` type
mapping or, without one, the type its traits or primitive infer. An
element whose scalar is stored as `JSONB` is `json`, whatever JSON it
holds. Otherwise a string stored as `UUID`, `TIMESTAMPTZ`, `DATE`, `TIME`
or `INTERVAL` is `uuid`, `dateTime`, `date`, `time` or `duration`, and one
stored as `TEXT`, `VARCHAR`, `CITEXT` or `INET` is `string`; an enum
stored as `TEXT` is `enum`; an integer or a number stored as `BIGINT`,
`INTEGER` or `SMALLINT` is `integer`, and one stored as
`DOUBLE PRECISION`, `REAL`, `NUMERIC` or `DECIMAL` is `number`; a boolean
stored as `BOOLEAN` is `boolean`. A map is stored as `JSONB` and is
`json`. A to-one relation holds its target's key and has its class. A list
(`T[]`) adds `[]` to its element's class and a list of lists (`T[][]`)
adds `[][]`. A `@jsonField` value and a list of lists are stored in a
`JSONB` column and hold the schema runtime's JSON: an object type or a
JSON value there is `json`, and any other element has the class it would
have in a column of its own (a list of lists of `Generic.Int64` is
`integer[][]`). Any other pair has no rule and a graph member with one
fails generation, naming the field and the SQL type: a JSON scalar stored
as `TEXT` (the catalog's `Embedding.Vector`, which has no `sql` mapping),
a string stored as `POINT` (`Geo.Location`) or as the `BIGINT` a
duration's name infers, and an object type without `@jsonField`.

## Trees and rows

A tree is `{"<kind>": [row, ...]}`. A kind the descriptor lacks is refused;
a missing kind has no rows. A row is a canonical row (below): a JSON object
keyed by column name, the same whether a storage adapter read it live or
from a history image, so a live row and its history image compare and hash
the same. Numbers keep the digits they were written with, so a `numeric`
wider than a double survives.

A row whose tombstone is `true` is a delete. Everywhere but `compose`, a
tombstone row and an absent row are the same deleted state.

A kind's content columns are the descriptor's `columns` less the role
columns and `excluded`. A row that lacks one holds it as `null` wherever the
core compares, merges, diffs or hashes content. After a kind gains a
column, the history images written before the change lack it while the live
rows read it as `null` (Postgres's `ADD COLUMN` without a `DEFAULT` gives an
existing row `null`), and both are the same content, so adding a column
moves no comparison, patch or merge on any backend. A column added with a
`DEFAULT` is outside this rule: on Postgres the existing rows read the
default while the images written before lack the column, which the core
reads as `null`, so a ref and its head commit hash differently and a save of
a row as its base holds it is a change. Add the column without a default,
and write its values through the graph. A role or excluded column a row
lacks is nothing, as it is when the row has it. A column a row carries that
`columns` lacks is content unless it is a role or excluded column, but it is
not read as `null` where another row lacks it.

`compose` returns each row as its input gave it. The rows `diff` and
`merge` return are the ones an engine writes back, and a storage adapter
keeps a row's stored value for a column a write lacks, so each of them
carries every content column the descriptor declares, `null` where its input
row lacked one. Otherwise a revert to a commit written before the gain would
write the commit's image, which lacks the column, over a row that holds a
value there, and the value would stay.

An order must be an integer inside +/-(2^53-1), the integers a JavaScript
number holds exactly; outside it, a browser would sort rows differently from
the server. An integer of any width outside the range, even one too wide for
64 bits, is out of range; a number with a fraction or an exponent is not an
integer.

## Canonical rows

A canonical row is a JSON object keyed by column name. Each value is the
schema runtime's JSON for the field's type, in one canonical form per value
class, so a content hash does not depend on the database that stored the
row (D19). A storage adapter normalizes what its database returns, live rows
and history images alike, and hands the core only canonical rows.
`null` is `null` in every class.

For Postgres, a live row is `to_jsonb(row)` and a history image is the JSONB
the history trigger stored from `to_jsonb(NEW)`; each is rendered as text in
the session's settings (a `timestamptz` in the session's time zone, an
`interval` in `IntervalStyle` `postgres`, the default). One rule per class
turns a value from that rendering into its canonical JSON:

| Class | Canonical JSON | From Postgres |
|---|---|---|
| `string` | The JSON string, escaped as the core's canonical JSON escapes: `"` and `\`, `\b` `\f` `\n` `\r` `\t` by name, any other control character as `\u00xx` in lowercase hex, everything else as it is. | Any JSON string. An `inet` is the text Postgres renders for it: IPv6 in lowercase with zeros compressed, and a single host without its prefix length (`2001:DB8::0001/128` is `2001:db8::1`; `10.1.2.3/8` keeps its `/8`). |
| `enum` | The member's value, as a `string`. | Any JSON string. |
| `integer` | The integer's digits, exactly however wide, with an optional minus and no leading zeros; `-0` is `0`. | A JSON number with no fraction or exponent (`bigint` renders so). |
| `number` | The number's exact decimal value in the layout of ECMAScript's `Number::toString`: plain digits while the decimal point falls within 21 digits of the first and no more than six zeros follow it, else one digit, a fraction and a signed exponent (`1e+21`, `1.5e-7`); no trailing zeros (`1.50` is `1.5`); `-0` is `0`. For a double, this is the text `JSON.stringify` and Go's `encoding/json` write. Digits are never rounded. | Any JSON number. `to_jsonb` renders a `double precision` as a `numeric`, never with an exponent (`1e300` as 301 digits); `NaN` and infinities, which it renders as strings, are refused. |
| `boolean` | `true` or `false`. | A JSON boolean. |
| `uuid` | The scalar core's canonical form: base62 of the UUID's 128 bits (`0123456789A-Za-z`), with the nil UUID as `"0"`. | The hyphenated form, in either case, or base62 (a JSON value the ORM stored). |
| `dateTime` | RFC 3339 in UTC with `Z`, the fraction of a second without trailing zeros and left out when zero: Go's `RFC3339Nano` of the UTC time (`2026-09-01T10:00:00.12Z`). | RFC 3339 with an offset, which may carry seconds (`+00:17:30`). The offset follows the session's time zone, so an image written under another zone normalizes to the same value. A year outside 0000-9999, `BC`, `infinity`, a time without an offset and an offset of a day or more (`+24:00`) are refused. |
| `date` | `YYYY-MM-DD`. | `YYYY-MM-DD`; `BC`, `infinity` and a day that does not exist are refused. |
| `time` | `HH:MM:SS` on a 24-hour clock, the fraction of a second without trailing zeros and left out when zero: what Postgres renders for a `time`. | `HH:MM:SS` with an optional fraction, and the other forms the scalar accepts, which a JSON value the ORM stored keeps as written: `HH:MM` (`10:00` is `10:00:00`) and a 12-hour clock (`2:30 pm` is `14:30:00`, `12:05 am` is `00:05:00`), each read as Postgres reads it into a `time`. The scalar has no fraction, but a `time` column written past it can hold one, which is kept. `24:00:00`, which the scalar refuses, is refused. |
| `duration` | The scalar core's canonical form: under a second, the largest of `ms`, `us` and `ns` that holds it whole (`500ms`, `1500us`); from a second, hours and minutes when present, then seconds with their fraction (`1h30m0s`, `1m30s`, `1.5s`); `0s` for zero. | Interval text (`01:30:00`, `1 day 02:00:00`, `-1 days +02:00:00`), where a day is 24 hours, or a duration string (`1h30m0s`, `1.5ms`, a JSON value the ORM stored). Months and years, which have no fixed length, are refused. |
| `json` | The JSON value with object members sorted by key (by code point, not jsonb's order by length), no whitespace, strings as `string`, and every number as `number`. | Any JSON value. |
| `<class>[]`, `<class>[][]` | A JSON array of the element class's canonical values, or of such arrays. A null element is refused (D12). | A native array renders as a JSON array; a list of lists is JSONB. |

A canonical row's members are sorted by column name. A column the row has
and the descriptor's `columns` lacks is refused; a declared column the row
lacks (a history image leaves out a `@versioned({ exclude })` column, and
one taken before its kind gained a column lacks that column) stays absent,
and the core reads it as `null` when it is content.

`testdata/canonical` holds the vectors: one file per element class,
`{"cases": [{"name", "class", "sql", "timeZone"?, "postgres", "canonical"}]}`,
with `"error"` (why, not compared) in place of `canonical` for a value the
rule refuses, and `rows.json`, `{"rows": [{"name", "columns", "sql",
"timeZone"?, "postgres", "canonical"}]}`. `postgres` is the value as it
appears in `to_jsonb` of a row that holds `sql` under the session time zone
`timeZone` (UTC when absent), and `canonical` is the exact text of its
canonical JSON. Package `canonical` in the Go module
(`go/canonical`: `Postgres(class, value)` and `PostgresRow(columns, row)`)
implements the rules and runs every vector, and checks each `postgres`
against a real Postgres when `SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL`
names one; the TypeScript package's `canonicalValue` and `canonicalRow`
(`typescript/src/canonical.ts`) and the Python package's `canonical_value`
and `canonical_row` (`python/superschematic_versiongraph/canonical.py`) do
the same.

## Operations

Every output is deterministic: object members are sorted by name, so a
tree lists its kinds by name rather than in descriptor order; rows sort by
order and then entity key; and lists sort by kind, entity key and path.

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
- On every side, a content column a row lacks is `null`, so absent and
  `null` are one value in every unit: a side that never set a column the
  base lacks does not conflict with a side that sets it. A `keyed` or
  `jsonSchema` column a row lacks is the JSON value `null`.

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
rows as the input gave them, left out on a deleted side. A content column a
row lacks is reported as `null`, so a unit is absent only in an add's base,
or as a key or property a JSON value lacks. The authors are each side's `author`
column, tombstones included.

`resolutions` settle conflicts by unit path:
`{"kind", "entityKey", "path", "take"}` takes the unit from `"base"`,
`"ours"` or `"theirs"` (a unit absent there is removed), and
`{"kind", "entityKey", "path", "value"}` gives the value (JSON `null` is a
value). A whole-entity conflict takes a side. A resolution must match a
conflict of this merge exactly; one that does not is refused, as are two for
one unit. A resolved row is checked like an input row, so a resolution that
leaves the order column missing or not an integer is refused as
`invalid_row`, and one outside the range as `order_out_of_range`. Taking
`"base"` for the order of an entity both sides added is such a resolution:
an add has no base value.

`merged` is a tree of every entity's result: the winning row, a winning
delete's tombstone when that side has one, or, for a unit-level merge, ours'
row with the merged content. Each row of `merged` carries every content
column the descriptor declares, `null` where the input row it came from
lacked one (a unit a resolution took from an add's base included). A
conflicted entity is not in it, so apply `merged` only when `conflicts` is
empty. `entities` lists every entity as `{"kind", "entityKey", "side",
"deleted"?}`: `side` is `ours` when the result equals ours (nothing to write
onto ours), `theirs` when it equals theirs, `merged` when it equals neither,
and `conflict` when a conflict is left; `deleted` is `true` when the result
is a delete. The merge does not check the singleton rule or parent edges
across entities; run `validate` on the result.

### diff

Input `{"descriptor", "from", "to"}`, output `{"changes"}`.

Each change is `{"kind", "entityKey", "operation", "row"?}`. An entity live
only in `to` is an `ADD` and one live in both with different content an
`UPDATE`, both with `to`'s row. A content column absent on one side and
`null` on the other is no difference. A change's row carries every content
column the descriptor declares, `null` where `to`'s row lacks one, since an
engine writes it back (a revert, a rebase). One live only in `from` is a
`DELETE`, with `to`'s tombstone row when it has one.

### content_hash

Input `{"descriptor", "tree"}`, output `{"contentHash"}`: SHA-256, as
lowercase hex, of the canonical JSON (object keys sorted by code point, no
insignificant whitespace, numbers as written) of

```
{"<kind>": [{"entityKey": "...", "content": {<content columns>}}, ...], ...}
```

with each kind's live rows sorted by entity key. The content holds every
content column the descriptor declares, `null` where the row lacks one, and
any other content column the row carries. A row that lacks a declared
content column hashes as the row with it `null`, so a ref's composed tree
and its head commit's tree hash the same whichever rows carry the column,
and a hash over rows that carry every column does not move. Nulls are
hashed, not dropped: a column a kind gains is content, `null` in each row
written before it, so under the descriptor that declares it a tree hashes
differently from how it hashed under the descriptor before the gain, which
is the hash a commit written before the gain recorded. Tombstone rows are
left out, as are kinds with no live rows, so a delete hashes as an absence
and a kind added to the descriptor does not move existing hashes.

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
| `invalid_descriptor` | The descriptor is malformed, is not version 3, or breaks a rule above. |
| `unknown_kind` | A tree names a kind the descriptor lacks. |
| `invalid_row` | A row is not an object, or its key, tombstone, parent key or order has the wrong type. |
| `duplicate_entity_key` | Two rows of a kind share an entity key (every operation but `validate`, which reports it). |
| `order_out_of_range` | An order outside +/-(2^53-1) (every operation but `validate`, which reports it). |
| `invalid_resolution` | A resolution is malformed, names an unknown kind, or repeats a unit. |
| `unmatched_resolution` | A resolution matches no conflict of the merge. |
| `internal` | The core panicked (a bug). |

## Engine and storage

An engine runs a graph's operations (D19): create a primary line, branch,
save, commit, seal, merge, rebase, revert, release, released, materialize,
compose, diff, history, discard and sweep. It drives the core through its
language's binding and reaches storage only through a storage adapter,
which holds the transaction of each operation and returns canonical rows.

Every engine keeps the same rules. A primary line takes writes only from
merge. A root's release pointer names a tagged commit and is fenced by its
version (0 for the first release); a release writes no member rows. A
rebase merges the parent's head into a change set against its base, keeps
as rows only the entities that differ from the new base, moves the base
and commits after the previous head. A commit is snapshotted when it is
tagged, released, or `snapshotEvery` commits past the nearest snapshot on
its chain, counted from before the chain's first commit, and a
materialize reads the nearest snapshot's pins with the later commits'
patches over them. A sweep takes the graph's sweep lock or reports itself
skipped; then it discards idle change sets (when asked), deletes the
member rows of refs discarded longer ago than the grace, prunes history
past retention, and writes missing snapshots. The Go engine is package `engine`,
over the interface in package `storage`; package `postgres` is its Postgres
adapter, which builds its statements from the descriptor and needs each
kind's `root`. The TypeScript engine, storage interface and Postgres
adapter are `typescript/src/engine.ts`, `storage.ts` and `postgres.ts`.
The TypeScript engine's operations are written once, as generators that
yield each storage call, and two drivers run them (D32): `Engine` awaits
each call over `Storage` and `Tx`, whose methods return promises, and
`SyncEngine` makes each call over `SyncStorage` and `SyncTx`, which have
the same methods returning their values, for a database whose driver
blocks, as SQLite's does under D16's engine. Either driver throws a failed
call's error back into the operation, so the operation handles it the same
way under both (a sweep's discard of a ref that moved, say). A `SyncEngine`
returns each operation's value, throws its error, and has no `runSweeper`;
`initSync` instantiates the core it runs on without awaiting. The
Rust engine is the crate in `rust-engine/`, over its `Storage` and `Tx`
traits, with its Postgres adapter in module `postgres` and the canonical
rules in module `canonical`. The Python engine, storage protocol and
Postgres adapter are the modules `engine`, `storage` and `postgres` of
`python/superschematic_versiongraph`. Every Postgres adapter takes the
sweep lock under the same key, so sweepers in different languages exclude
each other. The TypeScript package also has a SQLite adapter,
`typescript/src/sqlite.ts`, which `SyncEngine` runs over (below).
The Python package has one too, `python/superschematic_versiongraph/sqlite.py`.

Every id an engine takes or returns is a UUID in its canonical form. Each
write takes an actor, and each write through a ref the ref's expected
version. An engine's errors have stable codes, shared by every language:

| Code | When |
|---|---|
| `not_found` | A ref or commit that does not exist, or a discarded ref. |
| `version_conflict` | A write through a ref at another version than the one given. |
| `name_taken` | A new ref whose root already has a live ref of that name. |
| `no_actor` | A write with no actor. |
| `ref_sealed` | A write through a sealed ref. |
| `nothing_to_commit` | A commit of a ref that composes to its last commit's tree (or its base's). |
| `walk_ceiling` | A commit walk past the engine's ceiling (default 4096). |
| `schema_epoch` | A commit from a newer schema epoch than the engine's. |
| `entity_not_found` | A delete of an entity the ref does not hold, or an unset of an override it does not have. |
| `history_missing` | A row version a commit pins that history no longer holds. |
| `invalid_tree` | A commit whose tree `validate` finds wrong. |
| `root_mismatch` | Two refs, or a ref and a commit, of different roots. |
| `merge_into_itself` | A merge whose source is its target. |
| `primary_merge_only` | A save, commit, seal or revert on a primary line. |
| `not_tagged` | A release of a commit that is not tagged. |
| `no_parent` | A rebase of a primary line. |

An input the core refuses keeps the core's code (`unmatched_resolution`).

### SQLite

The SQLite adapter (D32, `@superschematic/versiongraph/sqlite`) holds every
graph in one fixed layout of tables, the same for every graph, rather than
in tables generated per graph, so a caller with fixed migrations, such as a
D16 behavior, can hold one. It implements `SyncStorage` and `SyncTx`, since
SQLite's drivers in bun and Node block, and only `SyncEngine` runs over it.
It reads from the descriptor only the kinds, their role columns (the root
among them), their columns' value classes and their `history`; the tables
the descriptor names are the Postgres adapter's.

The layout is nine `STRICT` tables, so a value of the wrong type is refused
rather than stored. A function the caller gives names each table and each
index from its local name below, and the default puts `graph_` before it.
Every row carries its graph's name (`graph`), an option of the adapter, so
one file holds several graphs, and every statement is scoped to it. An id
is `TEXT` holding a UUID in its canonical form; a version, a sequence and a
tombstone are `INTEGER`; the times of refs, commits, release pointers and
history images are `INTEGER` microseconds since the Unix epoch, which the
adapter returns as canonical date-times.

| Table | Columns | Keys and indexes |
|---|---|---|
| `ref` | `id`, `graph`, `root_id`, `parent_ref_id`, `base_commit_id`, `head_commit_id`, `name`, `sealed_at`, `created_at`, `created_by`, `updated_at`, `updated_by`, `deleted_at`, `deleted_by`, `_version` | `parent_ref_id` references a ref, `base_commit_id` and `head_commit_id` a commit; `ref_live_name`: unique `(graph, root_id, name)` among refs not deleted |
| `ref_history` | `history_id`, `graph`, `id`, `_version`, `operation`, `data`, `recorded_at` | `ref_history_version`: unique `(id, _version)` |
| `commit` | `id`, `graph`, `root_id`, `ref_id`, `parent_commit_id`, `message`, `schema_epoch`, `content_hash`, `sequence`, `created_at`, `created_by` | `ref_id` references a ref, `parent_commit_id` a commit; `commit_sequence`: unique `(graph, root_id, sequence)` |
| `patch` | `id`, `graph`, `commit_id`, `entity_kind`, `entity_key`, `entity_id`, `entity_version`, `operation` | `commit_id` references a commit; `patch_entity`: unique `(commit_id, entity_kind, entity_key)`; `patch_pin`: `(entity_id, entity_version)` |
| `snapshot_entry` | `id`, `graph`, `commit_id`, `entity_kind`, `entity_key`, `entity_id`, `entity_version` | `commit_id` references a commit; `snapshot_entry_entity`: unique `(commit_id, entity_kind, entity_key)`; `snapshot_entry_pin`: `(entity_id, entity_version)` |
| `release` | `id`, `graph`, `root_id`, `commit_id`, `created_at`, `created_by`, `updated_at`, `updated_by`, `_version` | `commit_id` references a commit; `release_root`: unique `(graph, root_id)` |
| `release_history` | as `ref_history` | `release_history_version`: unique `(id, _version)` |
| `member` | `id`, `graph`, `kind`, `entity_key`, `ref_id`, `root_id`, `tombstone`, `_version`, `data` | `ref_id` references a ref; `member_entity`: unique `(graph, kind, ref_id, entity_key)` |
| `member_history` | `history_id`, `graph`, `kind`, `id`, `_version`, `operation`, `data`, `recorded_at` | `member_history_version`: unique `(id, _version)`; `member_history_recorded`: `(graph, kind, recorded_at)` |

A member row holds its kind's role columns (id, entity key, ref, root,
tombstone, version) as columns and every other column the descriptor
declares as one canonical JSON object, `data`; read back, it is the
kind's canonical row, keyed by the descriptor's column names, with every
column the kind declares: one the stored row lacks, because the kind
gained it after the row was written, reads as `null`, as a Postgres row
reads a column added after it. An image reads as it was stored, as a
Postgres history image does: one taken before the kind gained a column
lacks it, and the core reads it as `null`. Foreign keys
check every edge inside the layout, immediately: a ref is written before a
commit of it, and its head moves to a commit only once the commit is
written. There is no root table, so no key checks a root; the adapter
refuses a write whose ref or commit is another graph's, or another root's.
`sqliteLayout(name)` returns the statements that create the layout, one
statement each (`CREATE TABLE IF NOT EXISTS` or `CREATE [UNIQUE] INDEX IF
NOT EXISTS`) with no trigger and no transaction control, for a caller that
runs its own migrations, and `createTables` runs them.

The adapter writes history in the statements of the transaction that
changes a row, as the sql generator's triggers do on Postgres. `_version`
starts at 1 and every update sets it to the old version plus 1. An insert
or an update writes the row's image at its new version, with operation
`INSERT` or `UPDATE`; a delete writes the row's image at the old version
plus 1, with operation `DELETE` and the kind's `history.actor` column, when
it has one, set to the delete's actor. A member's image is its canonical
row less the kind's `history.exclude` columns, so it hashes as the live row
does. A ref's image and a release pointer's are JSON objects of their
columns, each id in its canonical form and each time a canonical
date-time; the release pointer's history is the release log.

A transaction reads its time once from a clock (the system clock, in
milliseconds, unless the caller gives one), and every write in it, history
images included, takes that time, as Postgres's `now()` does. The adapter
generates every id Postgres takes from `gen_random_uuid()`: a version-4
UUID, in its canonical form, for each new ref, commit, release pointer,
patch, snapshot entry, row, history image and entity key a row lacks. It
canonicalizes each value it writes by its class with the canonical rules,
so what it stores reads back with no rules of its own. It writes a row's
ref, root, tombstone, actor and time itself and never its id or version;
a column the row lacks keeps its stored value on an update, and is `null`
on an insert, since the layout knows no column's default.

On a connection of its own, the adapter turns the connection's foreign
keys on when it is bound, begins each transaction with `BEGIN IMMEDIATE`,
which takes the file's write lock at once, and runs a transaction begun
inside another as a savepoint, at the outer one's time. With
`callerTransaction` it runs inside the transaction its caller holds and
issues no transaction control at all, as a D16 behavior's `sql` requires:
every statement it runs passes D16's checks for a behavior's SQL. Either
way one writer holds the file, so `lockRef` reads a ref as `readRef` does,
`nextSequence` reads the root's highest sequence plus 1, and `sweepLock`
reports true. A name already taken is `ref_live_name`'s
`SQLITE_CONSTRAINT_UNIQUE`. `prune` deletes a kind's images older than its
`history.retentionDays`, or the argument when it is not 0, keeping each
row's newest image and every image a patch or a snapshot pins, at most a
batch of them, oldest first; a kind without `retentionDays` prunes
nothing.

`createTables` and binding the adapter refuse a SQLite older than 3.37.0,
the first with `STRICT` tables, and one that cannot run `json_each` and
`json_extract`, which its reads take lists through (built in from 3.38.0,
and in 3.37 with JSON1); its statements need nothing later than that
(`RETURNING` came in 3.35.0). On a connection of its own the adapter reads
the version with `sqlite_version()`. In the caller's transaction it checks
the JSON functions only, since D16 refuses a behavior's statement that
names `sqlite_version`, and D16's engine, whose own tables are `STRICT`,
needs 3.37.0 already. A check that passes is kept, so it runs once: by
client on a connection of the adapter's own, and by layout (its tables'
names) in the caller's transaction, since a host such as `Branches` binds a
new adapter over a new client for each call. The second assumes one
SQLite library serves every connection that uses a layout name in the
process, as D16's engine does; another library without the JSON functions
under the same names would fail at its first `json_each` statement, with
SQLite's own error. A check that fails is not kept. Every language's SQLite
adapter refuses the same.

The adapter reaches SQLite through `SqliteClient`: `run`, `get` and `all`
with positional parameters for numbered placeholders (`?1`), returning
plain rows and `undefined` for no row, and an error carrying SQLite's
extended result code as a number in `code`, as D16's driver's do, plus
`exec`, which it calls only for transaction control and connection
settings. `nodeSqlite` and `bunSqlite` bind an open `node:sqlite`
`DatabaseSync` and an open `bun:sqlite` `Database`, using only the methods
they call, so no entry imports either module.

The Python package's SQLite adapter (`superschematic_versiongraph.sqlite`)
is this one, statement for statement and stored form for stored form, so
a file either writes the other reads; `python/README.md` has its use. Its
`Client` holds the transactions, as the Python Postgres adapter's does:
`sqlite_client` binds a `sqlite3.Connection` opened with
`isolation_level=None` (or `autocommit=True`), issues `BEGIN IMMEDIATE`
itself, and runs a transaction begun inside another, or inside one its
caller holds, as a savepoint; there is no mode without transaction control,
since no D16 engine runs in Python. It turns the connection's foreign keys
on, and refuses a SQLite older than 3.37.0, the first with `STRICT` tables,
or one without `json_each` and `json_extract`. Its system clock is
`time.time_ns()` in whole microseconds. Before Python 3.11 the sqlite3
module's errors carry no result code, so `is_unique_violation` reads a
taken name from SQLite's message ("UNIQUE constraint failed"). Its tests
hold it to the vectors below (`python/tests/test_sqlite_vectors.py`).

`testdata/sqlite` holds the vectors every language's SQLite adapter is
held to, so a file one adapter writes reads the same in another's; its
README gives each file's shape. `layout.json` is the layout's statements
under the default names, which `sqliteLayout()` returns exactly.
`typescript.sql` is a database the TypeScript adapter wrote, as plain SQL:
the layout's statements, then one `INSERT` per row, one statement per line.
It loads with foreign keys off, since a ref and its head commit name each
other, and every key holds once it has loaded. It holds two graphs of one
root, with every stored form: a primary line, change sets, a sealed one, a
discarded draft, tagged and untagged commits, patches of every operation,
snapshots, a release moved to a second tagged commit, rows of every kind
with a tasting of every value class, a tombstone, a DELETE image that
names the actor of the unset that removed its row, and a kind that gains a
content column partway, so its earlier rows read the column as `null`, its
earlier images lack it, and a commit recorded before the gain materializes
to another hash than the one it recorded. `typescript.json` is
what that file reads back as through an adapter opened with each graph's
name: each ref's `readRef`, rows, `compose` and `history`, each commit's
`readCommit`, `materialize`, patches and snapshot, each root's `released`,
and every history image, with each row as its exact canonical text. A
script in the TypeScript tests writes the database, with the fixture's
descriptor, a fixed clock and ids from a seeded generator, so it writes the
same file each run; the TypeScript tests check that the file reads as
`typescript.json` through both bindings, that one graph reads none of
another's, and that the script still writes both files, and
`UPDATE_SQLITE_VECTORS=1` rewrites them.

## Scenarios

`testdata/scenarios` holds one scenario per file, named by its `name`:
`{"name", "description", "roots", "steps": [step, ...]}`. A runner runs on
one backend. The backends a scenario may name are `postgres` and `sqlite`;
every runner runs on `postgres`, and the TypeScript and Python runners on
`sqlite` too.
On Postgres a runner applies `testdata/fixture/create.sql` to an empty
schema and builds its engine and Postgres adapter from
`testdata/fixture/recipe.json`; on SQLite the TypeScript runner creates the
SQLite adapter's layout, under its default names, in an in-memory database
and builds a `SyncEngine` and the SQLite adapter, graph `recipe`, from the
same descriptor, and the Python runner does the same with its engine and
SQLite adapter. Either way the engine runs at schema epoch 1 and snapshot
interval 3, the fixture graph's; the runner seeds the scenario's roots and
runs each step in order. It reads the whole scenario before the first
step, and refuses one with an unknown member or one that breaks a rule
below.

`roots` lists the roots the scenario uses, in order: at least one, each a
name and named once (`["Bread", "Soup"]`). Before the first step a runner
seeds them as its backend needs. On Postgres it inserts, in one statement,
a `recipe` row per root whose `id` is the root, whose `title` is the root's
name and whose `created_by` is `Cook`, so `recipe_ref.root_id`'s foreign
key finds it; a root the scenario does not list has no row. On SQLite it
seeds nothing, since the layout has no root table.

A step is `{"op", ...arguments, "expect"?}`. `as` names the ref or commit a
step returns, and later steps name it: `ref`, `from`, `source` and `target`
name refs (`from` names commits for `diff`), and `commit`, `toCommit` and
`to` name commits. `id:<uuid>` names an id no step returned. A write's
`actor` is `"Cook"` unless the step gives one (`""` is none), and its
`version` is the named ref's version as the last step that returned the
ref left it, unless the step gives one; a `release` step's is the root's
pointer's version as the last release left it, 0 before any. `walkCeiling`,
`schemaEpoch` and `snapshotEvery` run the step on an engine with that
ceiling, epoch or interval. Entity keys, roots and
actors are UUIDs written in their canonical form, which reads as a word
(`"Mix"`, `"Bread"`).

A step may list the backends that run it (`backends`): a non-empty list of
known backends, each named once. A runner skips a step whose list leaves
out its own backend; a step without the member, or with it `null`, runs on
every backend. The sweep scenario's `holdSweepLock`, `releaseSweepLock` and
the sweep between them that expects to be skipped run on `postgres` only,
since under SQLite's one writer no transaction can hold the lock while a
sweep runs.

| `op` | Arguments | Runs |
|---|---|---|
| `createPrimary` | `root`, `name` | CreatePrimary |
| `branch` | `from`, `name` | Branch |
| `save` | `ref`, `edits`: `{"<kind>": {"upsert": [row], "delete": [key], "unset": [key]}}` | Save |
| `commit` | `ref`, `message`, `tag` | Commit |
| `seal` | `ref` | Seal |
| `merge` | `source`, `target`, `resolutions` (the core's), `message`, `tag` | Merge |
| `rebase` | `ref`, `resolutions` | Rebase |
| `revert` | `ref`, `toCommit` | Revert |
| `release` | `root`, `commit` | Release |
| `released` | `root` | Released |
| `sweep` | `sweep`: `{discardGraceSeconds, abandonAfterSeconds, pruneBatch}`, each optional | Sweep |
| `holdSweepLock`, `releaseSweepLock` | | Take the graph's sweep lock in a transaction of another connection, and end it |
| `materialize` | `commit` | Materialize |
| `compose` | `ref` | Compose |
| `diff` | `from`, `to` | Diff |
| `history` | `ref` | History |
| `discard` | `ref` | Discard |
| `rows` | `ref`, `kind` | The adapter's rows of the ref, by entity key |
| `patches` | `commit` | The adapter's patches of the commit, by kind and entity key |
| `snapshot` | `commit` | The adapter's snapshot entries of the commit, by kind and entity key |
| `sql` | `statement`: `{"<backend>": "<statement>"}`, `args`: `[{"uuid"} or {"ref"} or {"commit"}]`, each as hyphenated text on Postgres and in its canonical form on SQLite | The runner's backend's statement on the scenario's database; with `rows` expected, a query whose rows the step returns |

An `sql` step's `statement` is an object of one statement per backend,
`{"postgres": "...", "sqlite": "..."}`; a `null` statement is none. On any
step that has one, a runner refuses a plain string, a statement for a
backend it does not know, and a statement that is not text (`null`
included). It refuses an `sql` step it runs that has no statement for its
backend, so no step is skipped silently; a step it skips needs none. Every
`sql` step gives both. A `postgres` statement numbers its placeholders `$1`
and takes UUIDs hyphenated; a `sqlite` statement is written against the
SQLite adapter's layout under its default names (`graph_ref`,
`graph_member_history`), numbers its placeholders `?1` and takes UUIDs in
their canonical form, as the layout stores them.

`expect` holds what the step must return; a step without `error` must
succeed.

| Member | Checks |
|---|---|
| `error` | The step fails with this code. |
| `ref` | The returned ref: `version`, `name`, `sealed`, and `parent`, `base` and `head` by name (`null` for none). |
| `commit` | The commit written, `null` for none: `ref`, `parent` by name, `message`, `sequence` (`null` untagged), `schemaEpoch`, `contentHash` or `contentHashOf` (the hash a named commit recorded). |
| `saved`, `tree` | Save's rows, or a read's tree: every kind with rows, each kind's rows in order, and each row's listed columns (a listed `null` also matches an absent column). |
| `contentHash`, `contentHashOf` | A read's content hash. |
| `findings`, `conflicts`, `changes` | Compose's findings, a merge's conflicts, a diff's changes: in order, each with its listed members. A merge left conflicts only when the step lists them. |
| `commits` | History's commits, by name, newest first. |
| `rows`, `patches`, `snapshot` | The listed rows, patches or snapshot entries, in order, each with its listed members; a patch is `{kind, entityKey, operation, entityVersion}` and a snapshot entry `{kind, entityKey, entityVersion}`. An `sql` step's rows are the statement's, in the order it returns them, each an object of its columns read as text, so the statement casts what it selects (a history image's actor, say, mapped to a name with `CASE`); on SQLite, where a column keeps its type, a column that is not text or `NULL` is refused. |
| `release` | The release pointer a release or released step returns: `commit` by name and `version`. |
| `report` | A sweep's report, with its listed members: `skipped`, `abandoned`, `collectedRefs`, `collectedRows` and `pruned` (by kind, nonzero counts only) and `snapshots`. |

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
them with `vg_dealloc`. `go/include/versiongraph.h` is the header. The
Python binding and the Rust engine do not go through it: they call the
crate's Rust API, and the binding returns the same documents.

## Build and test

```
make versiongraph            # static archive for the Go binding (scripts/versiongraph-archive.sh)
make rust                    # fmt, clippy (native and wasm32) and cargo test, then cargo test again with serde_json's preserve_order
make ts                      # among the TypeScript packages: the wasm build, the package, every vector through it
make python                  # among the Python packages: the binding's unit tests, the PyO3 extension, every vector through the package
cd runtime/versiongraph/go && go test ./...
SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL=postgres://... go test ./canonical  # the canonical vectors against Postgres
UPDATE_VECTORS=1 cargo test  # in rust/: rewrite every vector's expect; review the diff
UPDATE_SQLITE_VECTORS=1 bun test test/sqlite-vectors.test.ts  # in typescript/, after bun run build: rewrite testdata/sqlite; review the diff
make versiongraph-scenarios  # every scenario through the Go engine and the Postgres adapter
make versiongraph-scenarios-ts  # every scenario through SyncEngine and the SQLite adapter, and the SQLite vectors, then through the TypeScript engine and its Postgres adapter, each operation replayed through SyncEngine; a gained column end to end on each backend
make versiongraph-scenarios-rust  # every scenario and canonical vector through the Rust engine and its adapter
make versiongraph-scenarios-python  # every scenario through the Python engine and its SQLite adapter, and the SQLite vectors, then every scenario and canonical vector through its Postgres adapter
```

The scenarios, the adapter's tests and the canonical vectors' Postgres
check run against the Postgres that
`SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL` names, and skip without it;
`make versiongraph-scenarios`, `make versiongraph-scenarios-ts`,
`make versiongraph-scenarios-rust` and `make versiongraph-scenarios-python`
fail without it. The scenarios on SQLite, the SQLite adapter's tests and
the SQLite vectors need no server and run with or without it, and
`make versiongraph-scenarios-ts` and `make versiongraph-scenarios-python`
run them before they check for the variable. The fixture is the
compiler's output for `fixture-version-graph-db`, and a compiler test
(`go test ./internal/generator -run TestVersionGraphScenarioFixtureIsCurrent`)
fails when the checked-in copy is stale; `-update` rewrites it.

superscalar turns on `serde_json`'s `preserve_order` feature, and Cargo
unifies it into every crate of a build that uses superscalar, so the core
and the Rust engine never rely on a `serde_json` map's order: the content
hash and the canonical rules sort object keys themselves. `make rust` runs
both crates' tests a second time with the feature on.

The Go binding links `libsuperschematic_versiongraph.a` from
`go/lib/<goos>_<goarch>`, which `make versiongraph` stages; the Makefile
also puts that directory in `CGO_LDFLAGS`, as it does superscalar's. A
consumer outside this checkout sets `-L<dir>` in `CGO_LDFLAGS` the same way.
