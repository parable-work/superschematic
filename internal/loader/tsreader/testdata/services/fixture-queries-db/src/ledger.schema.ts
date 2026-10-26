import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key } from "@superschematic/db";

// An account a line is booked to.
export abstract class Account {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;
}

// A category, which may sit under a parent; the dashboard rolls a
// sub-category's totals up to it. A budget is exact, and starts on a day.
@index<Category>(["parent"])
export abstract class Category {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  parent: Nullable<Relation<Category, { onDelete: "RESTRICT" }>>;

  budget: Nullable<Validate<Generic.Decimal, { precision: 12; scale: 2 }>>;

  budgetSince: Nullable<Temporal.Date>;
}

// A group of lines that move money between two accounts, which no total
// counts.
export abstract class TransferGroup {
  @key
  id: AutoGenerate<Identity.UUID>;

  note: Nullable<string>;
}

// A line of the ledger: a signed amount in minor units, a debit negative.
@index<LineItem>(["date"])
@index<LineItem>(["category"])
export abstract class LineItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  date: Temporal.DateTime;

  name: string;

  amount: Finance.SignedMoney;

  account: Relation<Account, { onDelete: "RESTRICT" }>;

  category: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;

  transferGroup: Nullable<Relation<TransferGroup, { onDelete: "SET NULL" }>>;
}
