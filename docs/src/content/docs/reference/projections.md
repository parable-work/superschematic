---
title: Projection views
description: Declare a read-only SQL view over DB tables with @projection, @join and @column, and what verification refuses.
sidebar:
  order: 3
---

A projection is a read-only relation over tables of the same DB schema,
declared once as a class and meant to be served as a Postgres view. A
projection is never a table and never a language type: the table DDL, the
ORM and the type generators do not emit it.

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
