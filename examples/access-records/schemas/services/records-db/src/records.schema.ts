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
  key,
  searchField,
  unique
} from "@superschematic/db";

// A person who signs in.
export abstract class Person implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A role. A role a grant names lists the rows it reaches from the grant's
// target, each "Type:path".
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// A workspace of pages. Its owner always sees it.
@audience<Workspace>({ owner: "owner" })
export abstract class Workspace {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  owner: Identity.UUID;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// A page. It sees what its workspace sees unless it says otherwise, and an
// agent run only reads it unless the page says more.
@audience<Page>({ inheritFrom: "workspace", agents: "readOnly" })
export abstract class Page {
  @key
  id: AutoGenerate<Identity.UUID>;

  workspace: Nullable<Relation<Workspace, { onDelete: "SET NULL" }>>;

  @searchField
  title: string;

  createdAt: Temporal.DateTime;
  createdBy: Identity.UUID;
}

// An agreement with the members it lists. Its terms are sealed at rest.
export abstract class Agreement {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  status: Default<string, "active">;

  terms: Nullable<Encrypted<string>>;

  members: HasMany<AgreementMember>;
}

// A member of an agreement, by id.
export abstract class AgreementMember {
  @key
  id: AutoGenerate<Identity.UUID>;

  agreement: Relation<Agreement, { onDelete: "CASCADE" }>;

  member: Identity.UUID;
}

// A site an agreement covers.
export abstract class Site {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  agreement: Relation<Agreement, { onDelete: "CASCADE" }>;
}

// A ticket about a site.
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;

  site: Relation<Site, { onDelete: "CASCADE" }>;

  title: string;

  createdAt: Temporal.DateTime;
  createdBy: Nullable<Identity.UUID>;
}

// The grants that let a party or an agent run reach rows.
@accessGrants()
export abstract class Grant {}

// The log of reveals, agent runs' reads and calls made with a key or a
// grant.
@accessLog()
export abstract class AccessEntry {}
