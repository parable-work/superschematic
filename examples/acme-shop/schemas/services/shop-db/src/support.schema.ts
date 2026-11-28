import { Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { AutoGenerate, Derived, Provenance, Reactions, Relation, StateRules, Workflow, key } from "@superschematic/db";

import { User } from "./shop.schema";

// Which part of the shop a ticket is about.
export enum TicketCategory {
  Orders = "orders",
  Payments = "payments",
  Delivery = "delivery",
  Product = "product",
  Other = "other"
}

// How urgent a ticket is for the shopper.
export enum TicketPriority {
  Low = "low",
  Normal = "normal",
  Urgent = "urgent"
}

// A shopper's support ticket, which a model triages and a person confirms
// (D73, D74, D146): shop-cart's deriver TriageTickets fills its category,
// priority and summary from its subject and body while it is new or in
// triage. A category or priority the model is less than 0.8 sure of waits
// in review, which holds the ticket from assigned until support staff
// (support.review) accept or correct it and move it on. A ticket the model
// is sure of is assigned with no person: the Reactions rules hear its
// category and its priority settle and move it, once both are there, the
// state's required fields, and out of review. The summary is derived in
// every state, so it follows the body. shop-cart's behavior worker
// TicketReactions runs the rules.
export abstract class Ticket
  implements
    Derived<Ticket, {
      fields: {
        category: { from: ["subject", "body"]; instruction: "Which part of the shop is the shopper asking about?"; model: "standard"; states: ["new", "triage"] };
        priority: { from: ["subject", "body"]; instruction: "How urgent is this for the shopper?"; model: "standard"; states: ["new", "triage"] };
        summary: { from: ["body"]; instruction: "Summarize the request in one sentence."; model: "simple" };
      };
      permission: "tickets.derive";
    }>,
    Provenance<Ticket, {
      review: { sources: ["Derived"]; below: 0.8; permission: "support.review" };
      gates: { assigned: { reviewed: ["category", "priority"] } };
    }>,
    StateRules<Ticket, { states: { assigned: { required: ["category", "priority"] }; resolved: { frozen: "all" } } }>,
    Reactions<Ticket, {
      rules: [
        { when: { changes: { field: "category" } }; then: { transition: "assigned" } },
        { when: { changes: { field: "priority" } }; then: { transition: "assigned" } }
      ];
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  // The shopper who opened it.
  customer: Relation<User, { onDelete: "RESTRICT" }>;
  subject: Validate<string, { maxLength: 200 }>;
  body: Nullable<Validate<string, { maxLength: 10000 }>>;
  // The order the ticket is about, if the shopper named one.
  orderId: Nullable<Validate<string, { maxLength: 64 }>>;
  category: Nullable<TicketCategory>;
  priority: Nullable<TicketPriority>;
  summary: Nullable<Validate<string, { maxLength: 500 }>>;
  status: Workflow<{
    states: ["new", "triage", "assigned", "resolved"];
    initial: "new";
    transitions: [
      { from: "new"; to: "triage" },
      { from: "new"; to: "assigned" },
      { from: "triage"; to: "assigned" },
      { from: "assigned"; to: "resolved" }
    ];
  }>;
}
