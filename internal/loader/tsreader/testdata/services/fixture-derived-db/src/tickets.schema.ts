import { behavior } from "@superschematic/schema";

export enum Category {
  Billing = "billing",
  Shipping = "shipping",
  Other = "other"
}

@behavior("Derived", {
  fields: {
    category: { from: ["subject", "body"], instruction: "Classify the ticket.", model: "standard" },
    summary: { from: ["body"], instruction: "Summarize it in one sentence.", model: "simple" }
  },
  permission: "tickets.derive"
})
export abstract class Ticket {
  subject: string;

  body?: string;

  category?: Category;

  summary?: string;
}

@behavior("Comments")
export abstract class Note {
  text: string;
}
