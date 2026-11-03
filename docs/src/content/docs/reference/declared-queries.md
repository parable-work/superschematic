---
title: Declared queries
description: Named SQL a DB schema declares with @query, its parameters and rows typed in every language, prepared on Postgres into a lock that check and build hold it to, and run by the Go ORM and the TypeScript repositories alike.
sidebar:
  order: 4
---

A report's SQL, an aggregate, a `generate_series` of time buckets, a
window function, is more than a repository says. `@query` declares it in
the DB schema, by name, with a type for its parameters and a type for its
rows (D86). The build generates it as a typed function of both ORMs, and
`superschematic queries verify` prepares it on Postgres and checks what
Postgres describes against the types, so a renamed column fails a build,
not a request.

```ts
// finances-db/src/reports.schema.ts
import { Finance, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { query } from "@superschematic/db";

export abstract class CategoryTotalsParams {
  start: Temporal.DateTime;
  end: Temporal.DateTime;
  // An optional filter: every account when null.
  accountIds: Nullable<Identity.UUID[]>;
}

export abstract class CategoryTotalsRow {
  categoryId: Nullable<Identity.UUID>;
  spent: Finance.SignedMoney;
  earned: Finance.SignedMoney;
}

@query<CategoryTotalsParams, CategoryTotalsRow>({
  name: "categoryTotals",
  sql: `SELECT COALESCE(p.id, c.id) AS category_id,
           COALESCE(SUM(li.amount) FILTER (WHERE li.amount < 0), 0)::bigint AS spent,
           COALESCE(SUM(li.amount) FILTER (WHERE li.amount > 0), 0)::bigint AS earned
    FROM line_item li
    LEFT JOIN category c ON c.id = li.category_id
    LEFT JOIN category p ON p.id = c.parent_id
    WHERE li."date" BETWEEN :start AND :end
      AND (:accountIds::uuid[] IS NULL OR li.account_id = ANY(:accountIds))
    GROUP BY 1`,
  notNull: ["spent", "earned"],
})
export abstract class CategoryTotals {}
```

```ts
// TypeScript: on the pool, or in a transaction.
const rows = await deps.orm.queries.categoryTotals({ start, end, accountIds: null });
await deps.orm.transaction(tx => tx.queries.categoryTotals({ start, end, accountIds: [accountId] }));
```

```go
// Go: the same statement.
rows, err := db.Queries.CategoryTotals(ctx, types.CategoryTotalsParams{Start: start, End: end})
err = db.Transaction(ctx, func(tx orm.TxInterface) error {
	_, err := tx.GetQueries().CategoryTotals(ctx, params)
	return err
})
```

## The declaration

`@query<Params, Row>(options)` sits on a class of its own, which holds no
field: the class is only where the query is written. It is never a table
and never a language type. `Params` and `Row` are types of the schema, a
class or an object type alias each; a class `@query` names is an embedded
struct, not a table, and every language's types declare both. They take
none of a table's declarations (`@key`, `@index`, `@table`, a relation).

| Option | Meaning |
| --- | --- |
| `name` | the query's function, `orm.queries.<name>` and `Queries.<Name>`: a lower camel case identifier, unique in the service. The class's name in lower camel case unless set |
| `sql` | one statement, each parameter a params field written `:field`. A trailing semicolon is allowed; a second statement is refused |
| `notNull` | row fields the query vouches are never null, which Postgres cannot tell: an aggregate, a `COALESCE`, a column of a joined table that a `WHERE` keeps |
| `orderBy` | `{ param, options, default? }`: an enum parameter that picks one of fixed `ORDER BY` fragments ([Order](#order)) |
| `page` | `{ key, desc? }`: reads the rows a page at a time by a keyset of row fields ([Pages](#pages)) |
| `returns` | `"many"`, the rows, unless set; `"one"`, the one row; `"optional"`, the row or none ([One row](#one-row)) |
| `dialects` | the dialects it runs on, `postgres` and `sqlite`; unset is every dialect the service lists. A query runs on Postgres alone for now, so a service that also lists `sqlite` limits each query to `["postgres"]` |

A field of either type holds a scalar, an enum, a list of either, a JSON
value (`Generic.JSON`, a `@jsonField` type or `JsonField<T>`) or a vector
(`Validate<Embedding.Vector, { dimensions }>`). A map, a list of lists, a
union, an object type, a relation, an `Encrypted<T>` field, a POINT and an
INTERVAL fail the load, naming the fix.

## Sharing SQL

Reports over one dashboard read their rows through one filter. A `sql`
template literal interpolates string constants, so the filter is written
once and each query names it (D95):

```ts
// finances-db/src/filters.ts
const IN_RANGE = `li."date" BETWEEN :start AND :end`;

export const DASHBOARD_FILTER = `${IN_RANGE}
  AND (:accountIds::uuid[] IS NULL OR li.account_id = ANY(:accountIds))
  AND (:search::text IS NULL OR li."name" ILIKE '%' || :search || '%')`;
```

```ts
// finances-db/src/reports.schema.ts
import { DASHBOARD_FILTER } from "./filters";

@query<DashboardFilter, CategoryTotalsRow>({
  sql: `SELECT li.category_id, SUM(li.amount)::bigint AS total
    FROM line_item li
    WHERE ${DASHBOARD_FILTER}
    GROUP BY li.category_id`,
  notNull: ["total"],
})
export abstract class CategoryTotals {}
```

Each `${...}` names a string constant: a `const` declared with a string or
with a template literal of such constants, in the schema file or a file it
imports. The load reads the SQL with each one in place, and the
statement's `:name`s are the query's own, so each query that interpolates
the filter takes its parameters in its `Params` (here one type,
`DashboardFilter`, for every report). Anything else in a `${...}`, a
`let`, a call, a number, and strings joined with `+`, fails the load where
it is written, saying what a `${...}` may be.

The lock hashes each query's SQL as it reads with the constants in place,
so editing the filter makes `check` and `build` fail on every query that
interpolates it until `queries verify` prepares them again
([Verification](#verification)).

## Parameters

Each `:field` names a field of `Params`, and the SQL uses every one; the
load fails naming the query and the parameter otherwise. A name in a
string (`':x'`, `E'\':x'`), a quoted identifier, a dollar-quoted body or a
comment is no parameter, and neither is a cast (`::uuid`). The build
rewrites each to a positional parameter, numbered by first use, so a name
written twice is one parameter: `(:ids::uuid[] IS NULL OR id = ANY(:ids))`
binds one list. A positional `$1` in the SQL is refused. An array slice
`a[x:y]` reads `:y` as a parameter; write it `a[x : y]`.

A parameter binds as a column of its type would, through the value codec
in TypeScript (`encodeParam`): a base62 UUID hyphenated, a `Date` as a
timestamp, a list as a Postgres array, `null` as SQL NULL. In Go a nil
pointer, a nil list and the empty text of a `Nullable<string>`, whose Go
type is a `string`, bind SQL NULL.

## Rows

Each result column is a row field: the one whose column it is, its name in
snake case, or what `@column` names. A column no field reads, a field no
column holds and two columns of one name fail verification. TypeScript
decodes each row through the codec (`decodeRow`): a UUID base62, a BIGINT
a number, a TIMESTAMPTZ a `Date`, a DATE its `YYYY-MM-DD` text, a NUMERIC
a `Generic.Decimal`'s canonical text, a vector its numbers, JSON its
value. Go scans each column into its field by name.

A row field that is not `Nullable<T>` holds a column Postgres shows is
never null: one that comes straight from a `NOT NULL` column of a table no
outer join may leave out. A column of a table after `LEFT JOIN`, of any
table of a query with a `RIGHT` or `FULL JOIN`, and a computed column (an
aggregate, a function, a `COALESCE`) may be null, so its field is
`Nullable<T>`, or the query vouches for it with `notNull` or a comment:

```sql
SELECT b.bucket, COUNT(li.id) AS lines -- @notNull bucket, lines
FROM generate_series(...) AS b(bucket) LEFT JOIN line_item li ON ...
```

## Order

`orderBy` maps an enum parameter to fixed `ORDER BY` fragments. The SQL
writes the parameter where the order's terms go, and the fragment its
value names takes its place; no caller text reaches the SQL, and a value
no option names is refused before a statement is sent.

```ts
export enum LineOrder { Newest = "newest", Largest = "largest" }

@query<AccountLinesParams, AccountLinesRow>({
  sql: `SELECT li.id, li."date", li.amount FROM line_item li
        WHERE li.account_id = :accountId ORDER BY :order, li.id LIMIT :limit`,
  orderBy: {
    param: "order",
    options: { newest: 'li."date" DESC', largest: "abs(li.amount) DESC" },
    default: "newest",
  },
})
export abstract class AccountLines {}
```

The options are the enum's values, each once. A parameter that may be
null names the option a null picks with `default`. Verification prepares
the statement with each option.

## Pages

`page: { key: ["date", "id"], desc: true }` reads the rows a page at a
time, as `findPage` does (D60): in the order of the key, the page after
the key of the page before's last row. The key names required row fields
of one value each, which together tell every row apart. The query runs as
a subquery ordered by the key, so its own SQL writes no `ORDER BY` or
`LIMIT` for the page; a paged query takes no `orderBy`.

```ts
const first = await deps.orm.queries.transactionList(params, { limit: 50 });
const next = first.next ? await deps.orm.queries.transactionList(params, { after: first.next, limit: 50 }) : null;
```

```go
page, err := db.Queries.TransactionList(ctx, params, &orm.QueryPageOptions[orm.TransactionListPageKey]{Limit: 50})
next, err := db.Queries.TransactionList(ctx, params, &orm.QueryPageOptions[orm.TransactionListPageKey]{After: page.Next, Limit: 50})
```

A page holds `limit` rows, 100 unless set, and `next` is the key of its
last row when more follow, null on the last page.

## One row

A report of one row, a dashboard's totals, returns it rather than a list
of one: `returns: "one"`.

```ts
@query<DashboardFilter, DashboardTotalsRow>({
  sql: `SELECT COALESCE(SUM(li.amount), 0)::bigint AS total, COUNT(*) AS lines
    FROM line_item li WHERE ${DASHBOARD_FILTER}`,
  notNull: ["total", "lines"],
  returns: "one",
})
export abstract class DashboardTotals {}
```

```ts
const totals = await deps.orm.queries.dashboardTotals(filter); // DashboardTotalsRow
const line = await deps.orm.queries.matchingLine(params);      // MatchingLineRow | null
```

```go
totals, err := db.Queries.DashboardTotals(ctx, filter) // types.DashboardTotalsRow
line, err := db.Queries.MatchingLine(ctx, params)      // *types.MatchingLineRow, nil for none
```

| `returns` | TypeScript | Go | None | More than one |
| --- | --- | --- | --- | --- |
| `"many"` | `Promise<Row[]>` | `([]Row, error)` | `[]` | the rows |
| `"one"` | `Promise<Row>` | `(Row, error)` | `NotFoundError`; `ErrNotFound`, as `getOne`'s | `TooManyRowsError`; `ErrTooManyRows` |
| `"optional"` | `Promise<Row \| null>` | `(*Row, error)` | `null`; `nil` | `TooManyRowsError`; `ErrTooManyRows` |

The statement runs as written: nothing is added to it, so a query that
reads more than one row fails rather than answering the first. The load
refuses `"one"` and `"optional"` on a paged query, and on a statement
that shows it may return more than one row: its top level groups with
`GROUP BY`, is a `UNION` or a `VALUES` list of several rows, or reads a
set-returning function (`generate_series`, `unnest`, `jsonb_array_elements`
and the like) in its select list or its `FROM`. An aggregate with no
`GROUP BY` makes one row, over a function's rows too, and a statement
that ends `LIMIT 1` returns no more than one whatever it reads, so a
grouped query that never returns more than one says so with `LIMIT 1`.
What a query returns is in its lock hash, so changing it is a query to
verify again.

## Verification

```sh
superschematic queries verify schemas/services/finances-db
superschematic queries verify schemas/services/finances-db --database-url postgres://postgres:secret@localhost:5432/postgres
```

`queries verify` applies the service's `create.sql` to a database of its
own on Postgres, prepares each statement of each query there, every
order's and both of a page's, and compares what Postgres describes with
the declared types:

- each parameter's type is of the family of the type a column of its
  field would have, as the DDL maps it: text (`TEXT`, `VARCHAR`, `CITEXT`,
  an enum), an integer (`SMALLINT`, `INTEGER`, `BIGINT`), a float, a
  `NUMERIC`, a `UUID`, a `TIMESTAMPTZ`, a `DATE`, a `BOOLEAN`, JSON or a
  vector, a list of one or not;
- each result column is a row field's, of its field's family: a `SUM` of a
  `BIGINT` is a `NUMERIC`, which a `Finance.SignedMoney` is not, so the
  SQL casts it (`::bigint`);
- a field that is not `Nullable<T>` reads a column that is never null
  ([Rows](#rows)). A column of a projection's view
  ([Projection views](/superschematic/reference/projections/)) is never
  null when its projection field is required and reads a column, which
  the projection's own checks hold; Postgres describes no view column as
  `NOT NULL`.

Each problem names its query, and a statement Postgres refuses is reported
with Postgres's message. With none, `verify` writes `queries.lock` beside
the service's schema config: per query, a hash of what it verified (its
statements, with each string constant in place, the SQL types of its
parameters and row, which fields may be null and which it vouches for,
what it returns, the shape of each table its SQL names, and the model of
each projection's view it names, its joins, row rules and columns, with
the shapes of the tables the view reads) and what Postgres described of
its parameters and columns. Commit it.

`check` and `build` hold each query to the lock with no database: a query
the lock holds no entry for, one whose hash differs (its SQL or a
constant it interpolates, its types, what it returns, a column of a table
it names, or a join or column of a projection whose view it names
changed), and an entry for a query no longer declared fail them, naming
the query and the command to run.

The Postgres is the server `--database-url` names, whose role may create
a database (verify creates one and drops it), else the one
`SUPERSCHEMATIC_QUERIES_DATABASE_URL` names, else one `verify` runs in
Docker for the run: the image `stack dev` runs, pgvector's when a table
has a vector column. The statements are prepared by the migration runner,
`superschematic-migrate describe`, which holds the Postgres driver: on
`PATH`, or where `SUPERSCHEMATIC_MIGRATE` names it. In CI, run `verify`
against a Postgres of the job's and fail when the lock it writes differs
from the committed one (`git diff --exit-code`).

## Migration plans

A Postgres `migrate plan` of a service with queries prepares them on the
new version's `create.sql` before it prints the plan, as `verify` does,
and fails naming each query that no longer holds: renaming
`line_item.amount` fails it with `query categoryTotals: Postgres refuses
it: column li.amount does not exist`. It reaches Postgres as `verify`
does, through `--database-url`, `SUPERSCHEMATIC_QUERIES_DATABASE_URL` or
Docker, and writes no lock.

## Not yet

SQLite queries, whose SQL a dialect of its own would verify; the field
kinds above that no codec reads; a `@rawSql` function or view a query
reads, whose change the lock does not see (a projection's view it sees);
a nullability rule finer than the outer join's table, such as a `LEFT
JOIN` of a table also joined inside; named fragments with parameters of
their own, which a string constant and the query's params type cover.
