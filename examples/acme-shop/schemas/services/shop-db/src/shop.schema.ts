import { Contact, Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, User as UserTrait, UserRole, key, searchField, unique } from "@superschematic/db";

// Timestamps every table carries.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

// A person who can sign in to the shop, with their email and a password.
// The User trait makes the table the core user model's (D50): the build
// adds the Session and UserCredential tables beside it, and both APIs sign
// users in and out with the identity runtime. The trait is imported as
// UserTrait, since the table is named User too.
export abstract class User extends Auditable implements UserTrait<{ login: "email"; name: "name" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;

  name: Identity.Name;
}

// A named set of permissions a user is granted: shoppers hold orders.read
// and orders.write, to place their orders and read and cancel them, and
// staff products, to add products, and orders.read, orders.write and
// orders.any, or simply orders, to read and cancel every customer's
// orders. orders.any widens what the other two allow and opens no route
// alone. Users hold roles through the UserRoleGrant table the build adds.
export abstract class Role extends Auditable implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];
}

// Something the shop sells. Shoppers find a product by its name.
export abstract class Product extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  sku: Identity.Slug;

  @searchField
  name: Identity.Name;

  priceCents: Generic.Int64;

  inStock: Default<boolean, true>;

  // The name of the product's image in shop-api's bucket, shop-media,
  // once an upload URL is handed out for it.
  imageObject: Nullable<Validate<string, { maxLength: 1024 }>>;
}
