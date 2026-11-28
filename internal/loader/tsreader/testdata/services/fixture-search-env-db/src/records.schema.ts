import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, User, UserRole, accessGrants, audience, key, queue, searchable, unique } from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions; a role a grant names lists the rows it
// reaches from the grant's target (D156).
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// The grants that let a party reach rows (D156), whose every write the
// queue hears too (#740).
@accessGrants()
export abstract class Grant {}

// One message per row to index and copy, or grant to copy, which the
// build fills: its table and key (D155, #740).
@queue()
export abstract class SearchIndexing {}

// A folder, which its owner may keep to themselves.
@audience<Folder>({ owner: "owner" })
@searchable<Folder>({ title: "name", queue: SearchIndexing })
export abstract class Folder {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  owner: Identity.UUID;

  createdAt: Temporal.DateTime;
}

// A document in a folder, whose audience it inherits.
@audience<Document>({ inheritFrom: "folder" })
@searchable<Document>({ title: "title", body: "text", queue: SearchIndexing })
export abstract class Document {
  @key
  id: AutoGenerate<Identity.UUID>;

  folder: Nullable<Relation<Folder, { onDelete: "SET NULL" }>>;

  title: string;

  text: Nullable<string>;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// A contract, which a party's grant names.
@searchable<Contract>({ title: "name", body: "summary", queue: SearchIndexing })
export abstract class Contract {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  summary: Nullable<string>;
}

// A unit a contract covers.
@searchable<Unit>({ title: "name", body: "notes", queue: SearchIndexing })
export abstract class Unit {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  notes: Nullable<string>;

  contract: Relation<Contract, { onDelete: "CASCADE" }>;
}

// A note in a project, which a delete stamps rather than removes.
@searchable<Note>({ title: "title", body: "text", scope: "project", queue: SearchIndexing })
export abstract class Note {
  @key
  id: AutoGenerate<Identity.UUID>;

  project: string;

  title: string;

  text: Nullable<string>;

  deletedAt: Nullable<Temporal.DateTime>;
}
