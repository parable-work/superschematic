import { Contact, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Encrypted,
  HasMany,
  Relation,
  User,
  UserRole,
  accessGrants,
  accessLog,
  audience,
  calendarSource,
  key,
  projection,
  searchField,
  team,
  teamMembers,
  unique
} from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions. A role a grant names also lists the rows it
// reaches from the grant's target, each "Type:path" (D156).
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// A folder of documents. Its owner sees it whatever its audience, and its
// documents inherit its audience unless they say otherwise.
@audience<Folder>({ owner: "owner" })
export abstract class Folder {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  owner: Identity.UUID;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// A document, in a folder or in none, searched by its title. An agent run
// reads it and never writes it, unless its row says otherwise. It is on the
// calendar on the day it was made, as its reader sees it (D152, D187).
@audience<Document>({ inheritFrom: "folder", agents: "readOnly" })
@calendarSource<Document>({ field: "createdAt", title: "title", kind: "document", color: "documents" })
export abstract class Document {
  @key
  id: AutoGenerate<Identity.UUID>;

  folder: Nullable<Relation<Folder, { onDelete: "SET NULL" }>>;

  @searchField
  title: string;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// A document's title, through a view keyed by the document's key: its rows
// are the documents the caller sees (D156, D180).
@projection<Document>({ name: "document_title", migration: "20261126000000" })
export abstract class DocumentTitle {
  id: Identity.UUID;
  title: string;
}

// A contract between the organisation and the parties it lists. Its
// terms are sealed at rest.
export abstract class Contract {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  status: Default<string, "open">;

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

// A unit a contract covers.
export abstract class Unit {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  contract: Relation<Contract, { onDelete: "CASCADE" }>;
}

// A request about a unit.
export abstract class Request {
  @key
  id: AutoGenerate<Identity.UUID>;

  unit: Relation<Unit, { onDelete: "CASCADE" }>;

  title: string;

  createdAt: Temporal.DateTime;
  createdBy: Nullable<Identity.UUID>;
}

// The grants that let a party or an agent run reach rows (D156).
@accessGrants()
export abstract class Grant {}

// The log of reveals, agent runs' reads, and calls made with a key or a
// grant (D156).
@accessLog()
export abstract class AccessEntry {}

// A team: a set of accounts, and of other teams, that a row's audience
// names once for all its members (#735).
@team()
export abstract class Team {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;
}

// Who is in which team: an account, or another team nested in it (#735).
@teamMembers()
export abstract class TeamMember {}
