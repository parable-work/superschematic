import { Finance, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { column, join, projection } from "@superschematic/db";

import { Account, Business, Category, LineItem, Provider, User } from "./finances.schema";

// The dashboard's transaction list, flat (D90): a line item with its
// account and provider, its category and that category's parent, its
// business and its person, as one row. A plain view in the service's own
// schema, which binds no session setting, read through the ORMs'
// read-only repository.
@projection<LineItem>({ name: "transaction_list", migration: "20261026120000" })
@join<Account>("account", { "account.id": "base.account" })
@join<Provider>("provider", { "provider.id": "account.provider" })
@join<Category>("category", { "category.id": "base.category" }, "left")
@join<Category>("parent", { "parent.id": "category.parent" }, "left")
@join<Business>("business", { "business.id": "base.business" }, "left")
@join<User>("person", { "person.id": "base.user" }, "left")
export abstract class TransactionListRow {
  id: Identity.UUID;
  date: Temporal.DateTime;
  name: string;
  description: Nullable<string>;
  amount: Finance.SignedMoney;
  @column("base.account")
  accountId: Identity.UUID;
  @column("account.name")
  accountName: Identity.Name;
  @column("provider.name")
  providerName: Identity.Name;
  @column("base.category")
  categoryId: Nullable<Identity.UUID>;
  @column("category.name")
  categoryName: Nullable<Identity.Name>;
  @column("category.color")
  categoryColor: Nullable<string>;
  @column("category.parent")
  parentCategoryId: Nullable<Identity.UUID>;
  @column("parent.name")
  parentCategoryName: Nullable<Identity.Name>;
  @column("base.business")
  businessId: Nullable<Identity.UUID>;
  @column("business.name")
  businessName: Nullable<Identity.Name>;
  @column("base.user")
  userId: Nullable<Identity.UUID>;
  @column("person.name")
  userName: Nullable<Identity.Name>;
}
