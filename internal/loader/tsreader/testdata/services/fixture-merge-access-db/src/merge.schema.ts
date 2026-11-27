import { Contact, Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Mergeable, Relation, User, UserRole, accessGrants, audience, key, unique } from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions, and the rows a grant of it reaches (D156).
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// A label its owner sees whatever its audience. Two labels that turn out
// to be one are merged (D147), by a caller who may write both.
@audience<Label>({ owner: "owner" })
export abstract class Label implements Mergeable {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  owner: Identity.UUID;
}

// A note on a label, which a merge repoints.
export abstract class LabelNote {
  @key
  id: AutoGenerate<Identity.UUID>;

  label: Relation<Label, { onDelete: "CASCADE" }>;

  text: string;
}

// The grants that let a party reach rows (D156).
@accessGrants()
export abstract class Grant {}
