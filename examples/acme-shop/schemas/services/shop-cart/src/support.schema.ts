import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  behaviorOperations,
  behaviorWorker,
  deriver,
  rest,
  requirePermission
} from "@superschematic/api";
import { Ticket } from "@acme/shop-db";

// A shopper's support ticket, shop-db's Ticket (D73, D74): shop-cart's
// deriver TriageTickets fills in its category, priority and summary, and
// support staff confirm what the model is unsure of.
export abstract class TicketView {
  id: Identity.UUID;
  // new, triage, assigned or resolved.
  status: string;
  subject: string;
  body: Nullable<string>;
  orderId: Nullable<string>;
  // orders, payments, delivery, product or other, once derived.
  category: Nullable<string>;
  // low, normal or urgent, once derived.
  priority: Nullable<string>;
  summary: Nullable<string>;
  // Whether a derived field waits for a person to review it.
  inReview: boolean;
}

export abstract class OpenTicketInput {
  subject: Validate<string, { minLength: 1; maxLength: 200 }>;
  body: Nullable<Validate<string, { maxLength: 10000 }>>;
  orderId: Nullable<Validate<string, { maxLength: 64 }>>;
}

// A shopper's tickets: each is their own, and another's is not found.
export class Tickets extends Authenticated {
  @rest(HttpMethod.POST, "tickets")
  @requirePermission(["orders.write"])
  openTicket(input: OpenTicketInput): TicketView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "tickets/{id}")
  @requirePermission(["orders.read"])
  getTicket(id: Identity.UUID): TicketView {
    throw new Error("schema declaration only");
  }
}

// Support staff, who hold support.review, review what the model was
// unsure of and move a ticket on (D73, D103): Provenance's accept and
// reject, and the status Workflow's transition, which Provenance's gate
// holds from assigned while a field waits for review. The build serves
// each, with no method of the implementation's.
@behaviorOperations(Ticket)
@requirePermission(["support.review"])
export abstract class TicketActions {}

// Fills in each Ticket's category, priority and summary (D74, D146): a
// deployable of its own, which pulls the tickets whose fields are stale
// through shop-db's repositories and asks typescript/shop-cart's
// createDerivers for each. acme's answers are keyword rules, so stack dev
// and CI run with no model provider; a project calls its model there
// instead.
@deriver({ schemas: ["Ticket"] })
export class TriageTickets {}

// Runs Ticket's Reactions rules (D103): once the deriver settles a
// ticket's category and priority, and neither waits for review, it moves
// the ticket to assigned. A worker of shop-cart's, with no method of the
// implementation's.
@behaviorWorker(Ticket)
export abstract class TicketReactions {}
