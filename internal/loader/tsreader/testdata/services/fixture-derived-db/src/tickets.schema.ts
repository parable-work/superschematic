import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Comments, Derived, key } from "@superschematic/db";

export enum Category {
  Billing = "billing",
  Shipping = "shipping",
  Other = "other"
}

export abstract class Ticket
  implements
    Derived<Ticket, {
      fields: {
        category: { from: ["subject", "body"]; instruction: "Classify the ticket."; model: "standard" };
        summary: { from: ["body"]; instruction: "Summarize it in one sentence."; model: "simple" };
      };
      permission: "tickets.derive";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  subject: string;

  body: Nullable<string>;

  category: Nullable<Category>;

  summary: Nullable<string>;
}

export abstract class Note implements Comments<Note, {}> {
  @key
  id: AutoGenerate<Identity.UUID>;

  text: string;
}
