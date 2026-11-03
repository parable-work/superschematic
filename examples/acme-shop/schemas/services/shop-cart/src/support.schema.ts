import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, deriver, rest, requirePermission } from "@superschematic/api";

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

// Fills in each Ticket's category, priority and summary (D74): a deployable
// of its own, which pulls the tickets whose fields are stale from shop-db's
// engine and asks typescript/shop-cart's createDerivers for each. acme's
// answers are keyword rules, so stack dev and CI run with no model
// provider; a project calls its model there instead.
@deriver({ schemas: ["Ticket"] })
export class TriageTickets {}
