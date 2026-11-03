import { Finance, Generic } from "superscalar";
import { query } from "@superschematic/db";

// A report over the line items whose names start with a prefix, which a
// smoke run gives each of its own (D86).
export abstract class LineItemTotalsParams {
  prefix: string;
}

export abstract class LineItemTotalsRow {
  lines: Generic.Int64;
  spent: Finance.SignedMoney;
  earned: Finance.SignedMoney;
}

@query<LineItemTotalsParams, LineItemTotalsRow>({
  sql: `SELECT COUNT(*) AS lines,
           COALESCE(SUM(amount) FILTER (WHERE amount < 0), 0)::bigint AS spent,
           COALESCE(SUM(amount) FILTER (WHERE amount > 0), 0)::bigint AS earned
    FROM line_item
    WHERE starts_with("name", :prefix)`,
  notNull: ["lines", "spent", "earned"],
})
export abstract class LineItemTotals {}
