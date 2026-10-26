import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Relation, column, index, key, searchField, table, versioned } from "@superschematic/db";

// What a category is for.
export enum CategoryKind {
  Personal = "personal",
  Business = "business"
}

// A category of spending, in the table the application named
// budgetCategory.
@table("budgetCategory")
export abstract class Category {
  @key
  id: AutoGenerate<Identity.UUID>;

  @column("displayName")
  name: Validate<string, { minLength: 1; maxLength: 60 }>;

  kind: CategoryKind;

  @column("createdAt")
  createdAt: Temporal.DateTime;
}

// A line of spending, in the application's lineItem table: its columns
// keep their camel-case names, its category's foreign key among them, and
// the bank's id of a line is unique among its category's.
@table("lineItem")
@index<LineItem>(["postedOn"])
@index<LineItem>(["category", "externalId"], { unique: true, name: "external" })
export abstract class LineItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  @column("amountCents")
  amountCents: Generic.Int64;

  @column("postedOn")
  postedOn: Temporal.Date;

  @searchField
  memo: Nullable<string>;

  @column("externalId")
  externalId: Nullable<string>;

  @column("categoryId")
  category: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;

  @column("createdAt")
  createdAt: Temporal.DateTime;

  @column("updatedAt")
  updatedAt: Temporal.DateTime;

  @column("deletedAt")
  deletedAt: Nullable<Temporal.DateTime>;
}

// A note on the ledger, whose every version a history keeps: its history
// table, functions and triggers are named after its table.
@table("ledgerNote")
@versioned
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  @column("bodyText")
  body: string;

  @column("updatedAt")
  updatedAt: Temporal.DateTime;

  @column("updatedBy")
  updatedBy: Identity.UUID;
}
