import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { query } from "@superschematic/db";

// ---- category totals: rolled up to the parent category ---------------------

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
      AND li.transfer_group_id IS NULL
    GROUP BY 1
    ORDER BY 1`,
  notNull: ["spent", "earned"],
})
export abstract class CategoryTotals {}

// ---- time buckets: one row per day, week, month or quarter -----------------

export enum BucketUnit {
  Day = "day",
  Week = "week",
  Month = "month",
  Quarter = "quarter",
}

export abstract class TimeBucketsParams {
  unit: BucketUnit;
  start: Temporal.DateTime;
  end: Temporal.DateTime;
}

// Every bucket of the range, an empty one with zero totals.
export type TimeBucketsRow = {
  bucket: Temporal.DateTime;
  spent: Finance.SignedMoney;
  earned: Finance.SignedMoney;
  lines: Generic.Int64;
};

@query<TimeBucketsParams, TimeBucketsRow>({
  sql: `-- @notNull bucket, spent, earned, lines
    SELECT b.bucket,
           COALESCE(SUM(li.amount) FILTER (WHERE li.amount < 0), 0)::bigint AS spent,
           COALESCE(SUM(li.amount) FILTER (WHERE li.amount > 0), 0)::bigint AS earned,
           COUNT(li.id) AS lines
    FROM generate_series(date_trunc(:unit, :start::timestamptz), :end::timestamptz, ('1 ' || :unit)::interval) AS b(bucket)
    LEFT JOIN line_item li
      ON li."date" >= b.bucket AND li."date" < b.bucket + ('1 ' || :unit)::interval
     AND li.transfer_group_id IS NULL
    GROUP BY b.bucket
    ORDER BY b.bucket`,
})
export abstract class TimeBuckets {}

// ---- the transaction list: newest first, a page at a time ------------------

export abstract class TransactionListParams {
  start: Temporal.DateTime;
  end: Temporal.DateTime;
  // Lines whose name holds the text, in any case; every line when null.
  search: Nullable<string>;
  minAmount: Nullable<Finance.SignedMoney>;
}

export abstract class TransactionListRow {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  amount: Finance.SignedMoney;
  accountName: Identity.Name;
  categoryId: Nullable<Identity.UUID>;
  categoryName: Nullable<Identity.Name>;
}

@query<TransactionListParams, TransactionListRow>({
  name: "transactionList",
  sql: `SELECT li.id, li."date", li."name", li.amount,
           a."name" AS account_name, li.category_id, c."name" AS category_name
    FROM line_item li
    JOIN account a ON a.id = li.account_id
    LEFT JOIN category c ON c.id = li.category_id
    WHERE li."date" BETWEEN :start AND :end
      AND (:search::text IS NULL OR li."name" ILIKE '%' || :search || '%')
      AND (:minAmount::bigint IS NULL OR li.amount >= :minAmount)`,
  page: { key: ["date", "id"], desc: true },
})
export abstract class TransactionList {}

// ---- an account's lines, in an order the caller picks ----------------------

export enum LineOrder {
  Newest = "newest",
  Largest = "largest",
  Name = "name",
}

export abstract class AccountLinesParams {
  accountId: Identity.UUID;
  order: Nullable<LineOrder>;
  limit: Generic.Int64;
}

export abstract class AccountLinesRow {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  amount: Finance.SignedMoney;
}

@query<AccountLinesParams, AccountLinesRow>({
  sql: `SELECT li.id, li."date", li."name", li.amount
    FROM line_item li
    WHERE li.account_id = :accountId
    ORDER BY :order, li.id
    LIMIT :limit`,
  orderBy: {
    param: "order",
    options: { newest: 'li."date" DESC', largest: "abs(li.amount) DESC", name: 'li."name"' },
    default: "newest",
  },
})
export abstract class AccountLines {}

// ---- budgets: exact amounts from a day on ----------------------------------

export abstract class BudgetsParams {
  // A parent's sub-categories alone; every category when null.
  parentId: Nullable<Identity.UUID>;
}

export abstract class BudgetsRow {
  id: Identity.UUID;
  name: Identity.Name;
  budget: Generic.Decimal;
  budgetSince: Nullable<Temporal.Date>;
  parentId: Nullable<Identity.UUID>;
}

@query<BudgetsParams, BudgetsRow>({
  sql: `SELECT c.id, c."name", c.budget, c.budget_since, c.parent_id
    FROM category c
    WHERE c.budget IS NOT NULL -- @notNull budget
      AND (:parentId::uuid IS NULL OR c.parent_id = :parentId)
    ORDER BY c."name"`,
  dialects: ["postgres"],
})
export abstract class Budgets {}

// ---- transfer candidates: the other side of a move between accounts --------

export abstract class TransferCandidatesParams {
  lineItemId: Identity.UUID;
  windowDays: Generic.Int64;
}

export abstract class TransferCandidatesRow {
  id: Identity.UUID;
  accountId: Identity.UUID;
  amount: Finance.SignedMoney;
  date: Temporal.DateTime;
  daysApart: number;
}

@query<TransferCandidatesParams, TransferCandidatesRow>({
  sql: `SELECT o.id, o.account_id, o.amount, o."date",
           abs(extract(epoch FROM o."date" - li."date") / 86400)::double precision AS days_apart
    FROM line_item li
    JOIN line_item o
      ON o.account_id <> li.account_id
     AND o.amount = -li.amount
     AND o.transfer_group_id IS NULL
    WHERE li.id = :lineItemId
      AND o."date" BETWEEN li."date" - make_interval(days => :windowDays::int)
                       AND li."date" + make_interval(days => :windowDays::int)
    ORDER BY days_apart, o.id`,
  notNull: ["daysApart"],
})
export abstract class TransferCandidates {}
