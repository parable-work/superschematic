import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Derived, Provenance, Workflow, key } from "@superschematic/db";

// What a ticket is about.
export enum Category {
  Billing = "billing",
  Bug = "bug",
  Other = "other"
}

// A support ticket a model triages (D73, D103's step 4): its category and
// priority are derived from its subject and body by a deriver holding
// tickets.derive, each answer recorded with the confidence the model gave,
// and an answer below 0.8 proposed until a reviewer holding tickets.review
// accepts or rejects it. A ticket moves to assigned only once both are out
// of review.
export abstract class Ticket
  implements
    Derived<Ticket, {
      fields: {
        category: { from: ["subject", "body"]; instruction: "Classify the request."; model: "standard" };
        priority: { from: ["subject", "body"]; instruction: "How urgent is this? low, normal or high."; model: "standard"; states: ["new", "triage"] };
      };
      permission: "tickets.derive";
      maxAttempts: 2;
    }>,
    Provenance<Ticket, {
      fields: ["subject", "body", "category", "priority"];
      review: { sources: ["Derived"]; below: 0.8; permission: "tickets.review" };
      gates: { assigned: { reviewed: ["category", "priority"] } };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  subject: string;
  body: Nullable<string>;
  category: Nullable<Category>;
  priority: Nullable<string>;
  status: Workflow<{
    states: ["new", "triage", "assigned"];
    transitions: [{ from: "new"; to: "triage" }, { from: "new"; to: "assigned" }, { from: "triage"; to: "assigned" }];
  }>;
}

// An article whose summary a model writes from its body, with no review.
export abstract class Article
  implements
    Derived<Article, {
      fields: { summary: { from: ["body"]; instruction: "Summarize it in one sentence."; model: "simple" } };
      permission: "articles.derive";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  body: string;
  summary: Nullable<string>;
}
