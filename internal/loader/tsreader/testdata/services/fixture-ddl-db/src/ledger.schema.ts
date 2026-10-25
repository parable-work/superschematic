import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key, rawSql } from "@superschematic/db";

// What a category is for.
export enum CategoryKind {
  Personal = "personal",
  Business = "business",
  Accounting = "accounting"
}

// A category of spending. A category may sit under another, and deleting
// the parent leaves its children at the top level. Labels are looked up
// whatever their case, and the active categories by kind.
@index<Category>(["label"], { name: "label_lower", expression: "lower(label)" })
@index<Category>(["kind"], { name: "active_kind", where: "active" })
export abstract class Category {
  @key
  id: AutoGenerate<Identity.UUID>;

  label: Validate<string, { minLength: 1; maxLength: 60 }>;

  kind: CategoryKind;

  active: Default<boolean, false>;

  sortOrder: Default<Validate<number, { min: 0; max: 1000 }>, 0>;

  parent: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;
}

// A line of spending. Deleting its category leaves the line uncategorized.
// The dashboard reads each category's total from a materialized view the
// raw SQL keeps, keyed by its category so it refreshes concurrently. Lines
// are read by when they were posted; a migration builds that index inside
// its step, as the table is small.
@rawSql({
  id: "category_totals",
  dialects: ["postgres"],
  up: `
    DROP MATERIALIZED VIEW IF EXISTS category_totals;
    CREATE MATERIALIZED VIEW IF NOT EXISTS category_totals AS
      SELECT category_id, sum(amount_cents) AS total_cents, count(*) AS line_count
      FROM line_item
      GROUP BY category_id;
    COMMENT ON MATERIALIZED VIEW category_totals IS 'Each category''s total, for the dashboard.'
  `,
  down: "DROP MATERIALIZED VIEW IF EXISTS category_totals"
})
@rawSql({
  id: "category_totals_key",
  after: "category_totals",
  dialects: ["postgres"],
  up: "CREATE UNIQUE INDEX IF NOT EXISTS category_totals_category ON category_totals (category_id)"
})
@index<LineItem>(["postedAt"], { method: "btree", concurrently: false })
export abstract class LineItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  category: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;

  amountCents: Generic.Int64;

  memo: Default<string, "it's unexplained">;

  confidence: Nullable<Validate<number, { min: 0; max: 1 }>>;

  postedAt: Temporal.DateTime;
}
