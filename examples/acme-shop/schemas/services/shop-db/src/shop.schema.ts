import { Contact, Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Relation, key, unique } from "@superschematic/db";

// Timestamps every table carries.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

// A person who can sign in to the shop.
export abstract class User extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;

  name: Identity.Name;
}

// A signed-in session. The session auth provider looks a bearer token up
// by its jti.
export abstract class Session extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  jti: Identity.UUID;

  user: Relation<User, { onDelete: "CASCADE" }>;

  expiresAt: Temporal.DateTime;
}

// Something the shop sells.
export abstract class Product extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  sku: Identity.Slug;

  name: Identity.Name;

  priceCents: Generic.Int64;

  inStock: Default<boolean, true>;

  // The name of the product's image in shop-api's bucket, shop-media,
  // once an upload URL is handed out for it.
  imageObject: Nullable<Validate<string, { maxLength: 1024 }>>;
}

// How many units of a product the warehouse holds.
export abstract class StockLevel extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  product: Relation<Product, { onDelete: "CASCADE" }>;

  quantity: Generic.Int64;
}
