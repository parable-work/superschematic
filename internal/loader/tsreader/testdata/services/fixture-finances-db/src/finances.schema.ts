import { Finance, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, HasMany, Relation, index, key, searchField, unique } from "@superschematic/db";

// A household's finances, as the dashboard of a finance app reads them
// (D90): line items on accounts, each at a provider, filed under a
// category and its parent, a business and a person, with notes. A read
// includes a line item's relations, a filter reaches through them, and a
// projection reads the same rows flat.

// A person of the household.
export abstract class User {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: string;

  name: Identity.Name;

  createdAt: Temporal.DateTime;
}

// The bank or card issuer an account is held at.
export abstract class Provider {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  @unique
  slug: Identity.Slug;
}

// A business the household runs, which some spending belongs to.
export abstract class Business {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  @unique
  slug: Identity.Slug;
}

// An account at a provider, which may belong to a business and a person.
// Its line items load through @hasMany, as the Go ORM loads them too.
@index<Account>(["provider"])
export abstract class Account {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  @unique
  slug: Identity.Slug;

  provider: Relation<Provider, { onDelete: "RESTRICT" }>;

  business: Nullable<Relation<Business, { onDelete: "SET NULL" }>>;

  owner: Nullable<Relation<User, { onDelete: "SET NULL" }>>;

  lineItems: HasMany<LineItem>;
}

// A category of spending, under a parent category or none. A retired
// category is soft-deleted: a read leaves it out, and so does an include
// of a line item's category, and a filter through one.
@index<Category>(["parent"])
export abstract class Category {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: Identity.Name;

  @unique
  slug: Identity.Slug;

  color: string;

  parent: Nullable<Relation<Category, { onDelete: "RESTRICT" }>>;

  deletedAt: Nullable<Temporal.DateTime>;
}

// One line of an account: a signed amount on a date, filed under a
// category and a sub-category, a business and a person.
@index<LineItem>(["date"])
@index<LineItem>(["category"])
export abstract class LineItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  date: Temporal.DateTime;

  @searchField
  name: string;

  @searchField
  description: Nullable<string>;

  amount: Finance.SignedMoney;

  account: Relation<Account, { onDelete: "RESTRICT" }>;

  category: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;

  subCategory: Nullable<Relation<Category, { onDelete: "SET NULL" }>>;

  business: Nullable<Relation<Business, { onDelete: "SET NULL" }>>;

  user: Nullable<Relation<User, { onDelete: "SET NULL" }>>;

  createdAt: Temporal.DateTime;
}

// A note on a line item, soft-deleted when withdrawn. A line item's notes
// load through the relation that points at it, with no field of its own.
@index<Note>(["lineItem"])
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  lineItem: Relation<LineItem, { onDelete: "CASCADE" }>;

  content: string;

  createdAt: Temporal.DateTime;

  deletedAt: Nullable<Temporal.DateTime>;
}
