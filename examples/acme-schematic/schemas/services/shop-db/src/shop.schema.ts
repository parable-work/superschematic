import { Contact, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, key, unique, User as UserTrait, versioned } from "@superschematic/db";

// Audit fields every table carries.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

// A person who can call the shop API. The User trait (D50) makes the table
// the core user model's: the loader adds the Session and UserCredential
// tables beside it, and the acme "apikey" provider reads its key and name
// fields through the trait for its principal store. The User trait is
// imported as UserTrait, since the table is named User too.
@versioned
export abstract class User extends Auditable implements UserTrait<{ login: "email"; name: "name" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;

  name: Identity.Name;
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

  // The option values of each variant the product comes in, one inner list
  // per variant: [["red", "S"], ["red", "M"]]. A list of lists is stored
  // as JSONB.
  variants: string[][];
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
