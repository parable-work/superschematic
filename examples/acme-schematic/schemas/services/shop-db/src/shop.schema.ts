import { Contact, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, key, unique, versioned } from "@superschematic/db";

// Audit fields every table carries.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

// A person who can call the shop API.
@versioned
export abstract class User extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;

  name: Identity.Name;
}

// A bearer session the core "session" auth provider looks up by jti. The
// smoke compiles shop-api against that provider; without this table the
// generated session store is skipped and the UUID-parse fix is untested.
@versioned
export abstract class Session extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  jti: Identity.UUID;

  user: Relation<User, { onDelete: "CASCADE" }>;

  expiresAt: Temporal.DateTime;
}

// An API key a User presents in the X-API-Key header. The acme "apikey" auth
// provider (ext/auth) looks for a table of this shape (id, secret, user) in
// the API's authDb and generates an ORM-backed key store when it finds one.
@versioned
export abstract class ApiKey extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  secret: string;

  user: Relation<User, { onDelete: "CASCADE" }>;

  label: Nullable<string>;
  revokedAt: Nullable<Temporal.DateTime>;
}

// Something the shop sells.
@versioned
export abstract class Product extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  sku: Identity.Slug;

  name: Identity.Name;
  priceCents: number;
  inStock: Default<boolean, true>;
}

// How many units of a product one shop holds. A shop is identified by its
// id alone; the storefront.stock view below scopes rows to one of them.
export abstract class StockLevel extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  shop: Identity.UUID;

  product: Relation<Product, { onDelete: "CASCADE" }>;

  quantity: number;

  // Set when the shop stops carrying the product.
  discontinuedAt: Nullable<Temporal.DateTime>;
}
