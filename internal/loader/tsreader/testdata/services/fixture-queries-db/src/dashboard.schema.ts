import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { query } from "@superschematic/db";

import { LINE_FILTER } from "./filters";

// ---- the dashboard: four reports over one filter (D95) ----------------------

export abstract class DashboardFilter {
  start: Temporal.DateTime;
  end: Temporal.DateTime;
  // Every account, and every category, when null.
  accountIds: Nullable<Identity.UUID[]>;
  categoryIds: Nullable<Identity.UUID[]>;
  // Lines whose name holds the text, in any case; every line when null.
  search: Nullable<string>;
}

// ---- totals: spent, earned and the count, in one row ------------------------

export abstract class DashboardTotalsRow {
  spent: Finance.SignedMoney;
  earned: Finance.SignedMoney;
  lines: Generic.Int64;
}

@query<DashboardFilter, DashboardTotalsRow>({
  sql: `-- @notNull spent, earned, lines
    SELECT COALESCE(SUM(li.amount) FILTER (WHERE li.amount < 0), 0)::bigint AS spent,
           COALESCE(SUM(li.amount) FILTER (WHERE li.amount > 0), 0)::bigint AS earned,
           COUNT(*) AS lines
    FROM line_item li
    WHERE ${LINE_FILTER}`,
  returns: "one",
})
export abstract class DashboardTotals {}

// ---- the total of each category ---------------------------------------------

export abstract class DashboardCategoriesRow {
  categoryId: Nullable<Identity.UUID>;
  total: Finance.SignedMoney;
  lines: Generic.Int64;
}

@query<DashboardFilter, DashboardCategoriesRow>({
  sql: `-- @notNull total, lines
    SELECT li.category_id, SUM(li.amount)::bigint AS total, COUNT(*) AS lines
    FROM line_item li
    WHERE ${LINE_FILTER}
    GROUP BY li.category_id
    ORDER BY total, li.category_id`,
})
export abstract class DashboardCategories {}

// ---- the lines, through the line_list projection ----------------------------

export abstract class DashboardLinesRow {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  accountName: Identity.Name;
  categoryName: Nullable<Identity.Name>;
}

@query<DashboardFilter, DashboardLinesRow>({
  sql: `SELECT li.id, li."date", li."name", li.account_name, li.category_name
    FROM line_list li
    WHERE ${LINE_FILTER}
    ORDER BY li."date" DESC, li.id`,
})
export abstract class DashboardLines {}

// ---- the line of a name, if the filter holds one ----------------------------

export abstract class MatchingLineParams {
  start: Temporal.DateTime;
  end: Temporal.DateTime;
  accountIds: Nullable<Identity.UUID[]>;
  categoryIds: Nullable<Identity.UUID[]>;
  search: Nullable<string>;
  name: string;
}

export abstract class MatchingLineRow {
  id: Identity.UUID;
  date: Temporal.DateTime;
  amount: Finance.SignedMoney;
}

@query<MatchingLineParams, MatchingLineRow>({
  sql: `SELECT li.id, li."date", li.amount
    FROM line_item li
    WHERE ${LINE_FILTER}
      AND li."name" = :name`,
  returns: "optional",
})
export abstract class MatchingLine {}

// ---- the account of a name --------------------------------------------------

export abstract class AccountNamedParams {
  name: Identity.Name;
}

export abstract class AccountNamedRow {
  id: Identity.UUID;
  name: Identity.Name;
}

@query<AccountNamedParams, AccountNamedRow>({
  sql: `SELECT a.id, a."name" FROM account a WHERE a."name" = :name`,
  returns: "one",
})
export abstract class AccountNamed {}
