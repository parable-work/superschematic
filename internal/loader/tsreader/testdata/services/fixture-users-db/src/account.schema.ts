import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, User, UserRole, key, unique } from "@superschematic/db";

// Base class providing audit fields to every table.
export abstract class Auditable {
  createdAt: Temporal.DateTime;
  updatedAt: Nullable<Temporal.DateTime>;
}

// A person who signs in with their email address.
export abstract class Account extends Auditable implements User<{ login: "email"; name: "displayName" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;

  displayName: Identity.Name;
}

// A named set of permissions granted to accounts.
export abstract class Role extends Auditable implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];
}

// An ordinary table that refers to a user.
export abstract class Note extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  author: Relation<Account>;
  body: string;
}
