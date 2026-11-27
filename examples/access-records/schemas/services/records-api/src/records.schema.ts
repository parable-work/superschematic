import { Identity, Temporal } from "superscalar";
import { Nullable, Secret, Validate } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";
import {
  Authenticated,
  HttpMethod,
  SearchQuery,
  decrypted,
  paginated,
  requirePermission,
  rest,
  source,
  tableOperation
} from "@superschematic/api";
import { AccessEntry, Agreement, Grant, Page, Ticket, Workspace } from "@schemas/records-db";

// The keys of records-db's Encrypted<T> fields, which its config names
// (D85): the API's config, so each environment sets the one secret.
@envVars
export abstract class FieldKeys {
  RECORDS_DB_FIELD_KEYS: Secret<string>;
}

// A workspace, with who may see it.
@source(Workspace)
export abstract class WorkspaceView {
  id: Identity.UUID;
  name: string;
  owner: Identity.UUID;
  audience: string;
  audienceNamed: Nullable<Identity.UUID[]>;
}

// What a caller sets to make a workspace.
@source(Workspace)
export abstract class CreateWorkspaceInput {
  name: string;
  owner: Identity.UUID;
  audience: Nullable<string>;
  audienceNamed: Nullable<Identity.UUID[]>;
}

// Who may see a workspace.
@source(Workspace)
export abstract class ShareWorkspaceInput {
  audience: Nullable<string>;
  audienceNamed: Nullable<Identity.UUID[]>;
}

// A page.
@source(Page)
export abstract class PageView {
  id: Identity.UUID;
  workspaceId: Nullable<Identity.UUID>;
  title: string;
  audience: string;
}

// What a caller sets to write a page.
@source(Page)
export abstract class CreatePageInput {
  workspaceId: Nullable<Identity.UUID>;
  title: string;
  audience: Nullable<string>;
}

// An agreement: its terms open only for a caller who may reveal them.
@source(Agreement)
export abstract class AgreementView {
  id: Identity.UUID;
  name: string;
  @decrypted({ permission: "agreements.reveal" })
  terms: Nullable<string>;
}

// A ticket.
@source(Ticket)
export abstract class TicketView {
  id: Identity.UUID;
  siteId: Identity.UUID;
  title: string;
}

// What a caller sets to open a ticket about the site the path names.
@source(Ticket)
export abstract class CreateTicketInput {
  title: Validate<string, { minLength: 1 }>;
}

// A grant, a row of the database's @accessGrants table, without the hash
// of its token.
@source(Grant)
export abstract class GrantView {
  id: Identity.UUID;
  holder: Identity.UUID;
  holderKind: string;
  role: string;
  targetType: string;
  targetId: Identity.UUID;
  startsAt: Temporal.DateTime;
  endsAt: Nullable<Temporal.DateTime>;
  revokedAt: Nullable<Temporal.DateTime>;
}

// A row of the database's @accessLog table.
@source(AccessEntry)
export abstract class AccessEntryView {
  id: Identity.UUID;
  at: Temporal.DateTime;
  kind: string;
  principal: Nullable<string>;
  principalKind: Nullable<string>;
  operation: Nullable<string>;
  targetType: Nullable<string>;
  targetId: Nullable<string>;
  field: Nullable<string>;
}

export class Workspaces extends Authenticated {
  // The workspaces the caller sees.
  @rest(HttpMethod.GET, "workspaces")
  @paginated()
  @tableOperation()
  listWorkspaces(): WorkspaceView[] {
    throw new Error("schema declaration only");
  }

  // A new workspace.
  @rest(HttpMethod.POST, "workspaces")
  @tableOperation()
  createWorkspace(input: CreateWorkspaceInput): WorkspaceView {
    throw new Error("schema declaration only");
  }

  // Changes who may see a workspace: its owner alone.
  @rest(HttpMethod.PATCH, "workspaces/{id}")
  @tableOperation({ by: ["owner"] })
  shareWorkspace(id: Identity.UUID, input: ShareWorkspaceInput): WorkspaceView {
    throw new Error("schema declaration only");
  }
}

export class Pages extends Authenticated {
  // The pages the caller sees, searched by their titles.
  @rest(HttpMethod.GET, "pages")
  @paginated()
  @tableOperation()
  listPages(q: SearchQuery<string>): PageView[] {
    throw new Error("schema declaration only");
  }

  // A new page.
  @rest(HttpMethod.POST, "pages")
  @tableOperation()
  createPage(input: CreatePageInput): PageView {
    throw new Error("schema declaration only");
  }
}

export class Agreements extends Authenticated {
  // One agreement.
  @rest(HttpMethod.GET, "agreements/{id}")
  @requirePermission(["agreements.read"])
  @tableOperation()
  getAgreement(id: Identity.UUID): AgreementView {
    throw new Error("schema declaration only");
  }
}

export class Tickets extends Authenticated {
  // The tickets about the sites of the agreements the caller is a member
  // of: a row rule through three relations.
  @rest(HttpMethod.GET, "my/tickets")
  @requirePermission(["tickets.read"])
  @paginated()
  @tableOperation({ by: ["site.agreement.members.member"], overridePermission: "tickets.admin" })
  listMyTickets(): TicketView[] {
    throw new Error("schema declaration only");
  }

  // The tickets the caller reaches.
  @rest(HttpMethod.GET, "tickets")
  @requirePermission(["tickets.read"])
  @paginated()
  @tableOperation()
  listTickets(): TicketView[] {
    throw new Error("schema declaration only");
  }

  // A new ticket about the site the path names.
  @rest(HttpMethod.POST, "sites/{siteId}/tickets")
  @requirePermission(["tickets.write"])
  @tableOperation()
  openTicket(siteId: Identity.UUID, input: CreateTicketInput): TicketView {
    throw new Error("schema declaration only");
  }
}

export class Access extends Authenticated {
  // The grants, for a settings page.
  @rest(HttpMethod.GET, "access/grants")
  @requirePermission(["access.admin"])
  @paginated()
  @tableOperation()
  listGrants(): GrantView[] {
    throw new Error("schema declaration only");
  }

  // The access log, for a settings page.
  @rest(HttpMethod.GET, "access/log")
  @requirePermission(["access.admin"])
  @paginated()
  @tableOperation()
  listAccessLog(): AccessEntryView[] {
    throw new Error("schema declaration only");
  }
}
