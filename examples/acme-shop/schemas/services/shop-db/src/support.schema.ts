import { Validate, behavior } from "@superschematic/schema";

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
// (D73, D74): shop-cart's deriver TriageTickets fills its category,
// priority and summary from its subject and body while it is new or in
// triage. A category or priority the model is less than 0.8 sure of waits
// in review: the ticket moves to triage, and on to assigned once support
// staff (support.review) accept or correct it. A ticket the model is sure
// of is assigned with no person. The summary is derived in every state, so
// it follows the body.
@behavior("Workflow", {
  states: ["new", "triage", "assigned", "resolved"],
  transitions: [
    { from: "new", to: "triage" },
    { from: "new", to: "assigned" },
    { from: "triage", to: "assigned" },
    { from: "assigned", to: "resolved" }
  ]
})
@behavior("Derived", {
  fields: {
    category: { from: ["subject", "body"], instruction: "Which part of the shop is the shopper asking about?", model: "standard", states: ["new", "triage"] },
    priority: { from: ["subject", "body"], instruction: "How urgent is this for the shopper?", model: "standard", states: ["new", "triage"] },
    summary: { from: ["body"], instruction: "Summarize the request in one sentence.", model: "simple" }
  },
  permission: "tickets.derive"
})
@behavior("Provenance", {
  review: { sources: ["Derived"], below: 0.8, permission: "support.review" },
  gates: { assigned: { reviewed: ["category", "priority"] } }
})
@behavior("StateRules", { states: { assigned: { required: ["category", "priority"] }, resolved: { frozen: "all" } } })
@behavior("Reactions", {
  rules: [
    { when: { changes: { field: "Derived.fields.category.settledAt" } }, then: { transition: "assigned" } },
    { when: { changes: { field: "Derived.fields.priority.settledAt" } }, then: { transition: "assigned" } },
    { when: { changes: { field: "Provenance.inReview", to: true } }, then: { transition: "triage" } },
    { when: { changes: { field: "Provenance.inReview", to: false } }, then: { transition: "assigned" } }
  ]
})
export abstract class Ticket {
  // The shopper who opened it, as the engine keeps a UUID's text.
  customerId: Validate<string, { maxLength: 64 }>;
  subject: Validate<string, { maxLength: 200 }>;
  body?: Validate<string, { maxLength: 10000 }>;
  // The order the ticket is about, if the shopper named one.
  orderId?: Validate<string, { maxLength: 64 }>;
  category?: TicketCategory;
  priority?: TicketPriority;
  summary?: Validate<string, { maxLength: 500 }>;
}
