---
title: Projection views
description: Declare a read-only SQL view over DB tables with @projection, @join and @column; what the sql generator writes for it, outputs.sql, and what verification refuses.
sidebar:
  order: 3
---

A projection is a read-only relation over tables of the same DB schema,
declared once as a class. The `sql` generator writes the Postgres view,
the migration that creates it, and an Arrow schema and docs file for its
readers, all from that declaration. A projection is never a table and
never a language type: the table DDL, the ORM and the type generators do
not emit it.

The three decorators come from `@superschematic/db` and are only allowed
in DB schemas.

## Declare a projection

```ts
import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { column, join, projection } from "@superschematic/db";
import { Channel, Preference, PreferenceScope } from "./preference.schema";

// Preference values visible to the current account, channel and user.
@projection<Preference>({
  pool: "app",
  name: "preferences",
  migration: "20260902120000",
  where: [
    { column: "base.account", setting: "app.account_id" },
    { column: "base.channel", setting: "app.channel_id" },
    { column: "base.userId", setting: "app.user_id", when: { column: "base.scope", equals: "user" } },
    {
      anyOf: [
        { column: "base.branch", setting: "app.branch_id", optional: true },
        { column: "base.commit", setting: "app.commit_id", optional: true }
      ],
      when: { column: "base.scope", equals: "app" }
    },
    { column: "base.archivedAt", isNull: true },
    { column: "base.hidden", equals: false }
  ],
  collapse: {
    by: ["base.slotKey"],
    order: [
      { column: "base.scope", rank: ["user", "team", "app"] },
      { column: "base.branch", direction: "asc", nulls: "last" }
    ]
  }
})
@join<Channel>("channel", { "channel.id": "base.channel" })
@join<Channel>("branch", { "branch.id": "base.branch" }, "left")
export abstract class AppPreference {
  slotKey: Identity.UUID;
  scope: PreferenceScope;
  @column("base.channel")
  channelId: Identity.UUID;
  @column("channel.handle")
  channelHandle: Identity.Slug;
  @column("branch.name")
  branchName: Nullable<Identity.Name>;
  value: Generic.JSON;
  @column({ function: "app.preference_policy_key", args: ["base.id"] })
  policyKey: Identity.UUID;
}
```

| Part | Meaning |
| --- | --- |
| `@projection<Source>` | The table the view reads, addressed as `base` everywhere in the declaration. |
| `pool`, `name` | The view is created as `"pool"."name"`, so readers address it as `pool.name`. Both are snake_case identifiers. |
| `migration` | The 14-digit version stamp of the generated migration. An applied migration is never rewritten: a changed declaration takes a new stamp. |
| `where` | Row rules, all ANDed, in order. The forms are below. |
| `collapse` | Keeps one row per key: `by` lists the `alias.field` columns of the key (`DISTINCT ON`), `order` ranks the rows that share it and the first wins. |
| `@join<Table>(alias, on, kind?)` | Another table of the schema. `on` maps `alias.field` equalities; `kind` is `"inner"` (the default) or `"left"`. A left join makes every column read through its alias nullable. |
| Class fields | The view's columns, in order. Each reads `base.<field>` unless `@column` names another `alias.field`, or computes it with `{ function, args }`. |

Every `alias.field` reference names a schema field (camelCase), never a
SQL column. The generator resolves it to the column; a relation field
resolves to its foreign-key column. The declaration never carries SQL.

## Row rules

| Form | Generated as |
| --- | --- |
| `{ column, setting }` | `column = current_setting('setting')::<column type>`. An unset setting makes `current_setting` raise, so the view fails closed. |
| `{ column, setting, optional: true }` | `column = NULLIF(current_setting('setting', true), '')::<type>`: an unset or empty setting matches no row. Meant for `anyOf`, where another binding can admit the row. |
| `{ anyOf: [binding, binding, ...] }` | The bindings ORed. Two or more; each carries `column`, `setting` and `optional` only. |
| `{ column, isNull: true }`, `{ column, notNull: true }` | `column IS null`, `column IS NOT null`. |
| `{ column, equals: literal }` | `column = literal`. The literal's type follows the column: a member of an enum column, a boolean, a number or a string. |
| `{ function, args, requiredSettings }` | Every listed setting is set and non-empty, and `"schema"."function"(args...) IS true`. The function belongs to a hand-written migration. |
| any form with `when: { column, equals }` | `(column IS DISTINCT FROM 'equals' OR rule)`: the rule applies only to rows whose guard column equals the literal. |

A setting is a dotted custom Postgres setting (`app.account_id`); a
dotless name is reserved for server settings. The setting's cast follows
the column type (`uuid`, `bigint`, `integer`, `boolean`, `timestamptz`,
`date`; text-like columns compare as text).

## Columns

A column keeps its source column's type: the declared type must be the
source field's scalar or enum (for a relation field, the target table's
key type), with the same array-ness. A column over a nullable field or a
left join must be `Nullable<T>`. A computed column (`@column({ function,
args })`) declares the function's result type and nullability; the
generator does not read the function's signature.

A projection column carries only a name, a type, nullability and
`@column`. Table decorators and wrappers (`@key`, `@unique`,
`Relation<...>`, `Default<...>`, ...) are refused, and so is a map column.

## What the sql generator writes

For a DB service with projections, `superschematic build` writes, next to
the table DDL in `<out>/sql/<service>/`:

| File | What it is |
| --- | --- |
| `create.sql`, `drop.sql` | The view is created after the tables, in `CREATE SCHEMA IF NOT EXISTS "pool"`, and dropped before them. |
| `<migrationsDir>/<stamp>_<pool>_<name>_projection.up.sql` | Creates the schema, drops the view if it exists and creates it. Re-runnable. |
| `<migrationsDir>/<stamp>_<pool>_<name>_projection.down.sql` | Drops the view. |
| `projections/<pool>.<name>.arrow.json` | The view's Arrow schema in arrow-rs serde form: one field per column with its Arrow type, nullability and metadata, and schema metadata naming the view and its settings. |
| `projections/<pool>.<name>.docs.json` | The view's address, description, settings, collapse key and columns (source, scalar or enum, SQL and Arrow type, nullability, description), for a catalog page. |

The view is created `WITH (security_barrier = true)`, so a function in a
reader's query cannot observe rows the rules exclude. The view's comment
and each column's comment come from the class and field comments. Column
order is the declaration's and is the Arrow contract.

A column's SQL type maps to Arrow as follows; any other type fails the
build rather than being guessed:

| SQL | Arrow |
| --- | --- |
| `UUID`, `TEXT`, `CITEXT`, `VARCHAR(n)`, `CHAR(n)`, `JSONB`, `JSON`, `LTREE` | `Utf8` |
| `BIGINT` / `INTEGER` / `SMALLINT` | `Int64` / `Int32` / `Int16` |
| `DOUBLE PRECISION`, `NUMERIC` / `REAL` | `Float64` / `Float32` |
| `BOOLEAN` | `Boolean` |
| `TIMESTAMPTZ` / `TIMESTAMP` | `Timestamp(Microsecond, "UTC")` / `Timestamp(Microsecond, None)` |
| `DATE` / `TIME` / `BYTEA` | `Date32` / `Time64(Microsecond)` / `Binary` |
| `T[]` | `List<item: T>` |

### Arrow metadata

Every key starts with the naming file's
[`metadata_key_prefix`](/superschematic/reference/naming/#metadata_key_prefix)
(`superschematic.` by default); the rest is fixed.

| Key | On | Value |
| --- | --- | --- |
| `<prefix>projection.source` | every field | the `alias.field` the column reads, or `schema.function(args)` |
| `<prefix>scalar.canonical_name`, `.flat_name`, `.primitive`, `.sql_type`, `.json_schema_type`, `.format`, `.max_length`, `.min_length`, `.pattern` | scalar fields | the scalar's identity and constraints; the last five only when set |
| `<prefix>enum.name`, `<prefix>enum.values` | enum fields | the enum and its serialized members, comma-separated, resolved in dependency schemas too |
| `<prefix>projection.pool`, `.name`, `.relation`, `.view`, `.schema`, `.type`, `.migration` | schema | where the view lives and which declaration it came from |
| `<prefix>projection.settings` | schema | every setting the view reads, comma-separated, in first-use order |
| `<prefix>projection.optional_settings` | schema | the subset a reader may leave unset |
| `<prefix>projection.rows` | schema | `one` when the view collapses, `all` otherwise |

A reader sets every required setting (`SET LOCAL` or `set_config(name,
value, true)` in the scan's transaction) before it reads the view.

## `outputs.sql`

The DB kind's `sql` output has no switch; the DDL is implied by the kind.
Its `outputs.sql` block places and owns the projection migrations:

```ts
export default defineConfig({
  name: "shop-db",
  kind: SchemaKind.DB,
  outputs: {
    sql: { migrationsDir: "migrations", viewOwner: "shop_view_owner" }
  }
});
```

| Key | Meaning |
| --- | --- |
| `migrationsDir` | Where the migration pairs are written, relative to the service directory, so they land next to the service's hand-written migrations. Unset keeps them under `<out>/sql/<service>/projections/migrations`. The directory is not build-cache output: commit what the build writes there. |
| `viewOwner` | The Postgres role the up migration creates the view as: `SET ROLE <viewOwner>` before the view DDL and `RESET ROLE` after it, so the schema's default privileges for that role grant readers their access. The migration runner must be a member of the role, and the role needs `CREATE` on the pool schema and `SELECT` on the tables the view reads. A lowercase identifier. Unset creates the view as the runner. `create.sql` and the down migration never switch roles. |

An unknown key in the block fails the build.

Replacing a view drops the grants on it. Roles, grants on the pool schema,
default privileges and row-level security on the source tables stay with
the service's hand-written migrations; the generator writes the view and
its rules only.

## What verification refuses

The checks run on the IR after every frontend, so a JSON or YAML schema
file that writes `role: Projection` and a `projection` object directly
gets the same answers:

- `@projection` outside a DB schema, or a source or joined type that is
  not a DB table of the same schema;
- `@join` or `@column` without `@projection` on the class, heritage on a
  projection class, or another class decorator on it;
- an `alias.field` that does not resolve, a join alias reused or named
  `base`, or a join whose condition never mentions its own alias;
- a column whose type differs from its source, or one declared required
  over a nullable column or a left join;
- a view name or migration stamp another projection already uses;
- a rule that mixes forms, an `anyOf` with fewer than two bindings or with
  a guard inside one, a setting name without a dot, a function name that
  is not `schema.function`, a literal whose type does not match its
  column, or an order term that mixes `rank` with `direction`/`nulls`.

## Required rules are an extension's policy

The core does not require any particular rule on a view. A deployment that
does, such as a row scope every view must bind, registers a
[check](/superschematic/guides/write-an-extension/#policy-on-what-the-core-writes)
(`RegisterCheck`) on the DB kind and reads its settings from its own
`[extension.<name>]` table. `examples/acme-schematic` requires every
projection's first rule to bind `acme.shop_id`.
