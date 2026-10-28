import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { column, join, projection } from "@superschematic/db";

import { Account, Category, LineItem } from "./ledger.schema";

// A line with its account's and its category's names, flat: a plain view
// the dashboard's line list reads. The lock holds a query over it to the
// projection's model, its joins and its columns (D95).
@projection<LineItem>({ name: "line_list", migration: "20261027000000" })
@join<Account>("account", { "account.id": "base.account" })
@join<Category>("category", { "category.id": "base.category" }, "left")
export abstract class LineListEntry {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  @column("base.account")
  accountId: Identity.UUID;
  @column("account.name")
  accountName: Identity.Name;
  @column("base.category")
  categoryId: Nullable<Identity.UUID>;
  @column("category.name")
  categoryName: Nullable<Identity.Name>;
}
