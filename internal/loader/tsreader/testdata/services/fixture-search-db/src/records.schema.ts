import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Comments,
  Encrypted,
  HasMany,
  Relation,
  User,
  UserRole,
  accessGrants,
  attachments,
  audience,
  key,
  queue,
  searchable,
  unique
} from "@superschematic/db";

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

// The grants that let a party reach rows (D156).
@accessGrants()
export abstract class Grant {}

// One message per row whose indexed text changed, which the build fills:
// the row's table and key (D155).
@queue()
export abstract class SearchIndexing {}

// One message per file row deleted, naming its object (D154).
@queue()
export abstract class FileReleased {}

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

// A contract between the organisation and the parties it lists. Its terms
// are sealed at rest and never indexed.
@searchable<Contract>({ title: "name", body: "summary", queue: SearchIndexing })
export abstract class Contract {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  summary: Nullable<string>;

  terms: Nullable<Encrypted<string>>;

  parties: HasMany<ContractParty>;
}

// A party to a contract, by its id.
export abstract class ContractParty {
  @key
  id: AutoGenerate<Identity.UUID>;

  contract: Relation<Contract, { onDelete: "CASCADE" }>;

  party: Identity.UUID;
}

// A unit a contract covers, with a conversation and files whose text the
// index holds too.
@attachments<Unit>({ released: FileReleased })
@searchable<Unit>({ title: "name", body: "notes", queue: SearchIndexing })
export abstract class Unit implements Comments<Unit, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  notes: Nullable<string>;

  contract: Relation<Contract, { onDelete: "CASCADE" }>;
}

// A request about a unit, scoped to its unit.
@searchable<Request>({ title: "title", body: ["details", "resolution"], scope: "unit", queue: SearchIndexing })
export abstract class Request {
  @key
  id: AutoGenerate<Identity.UUID>;

  unit: Relation<Unit, { onDelete: "CASCADE" }>;

  title: string;

  details: Nullable<string>;

  resolution: Nullable<string>;
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
