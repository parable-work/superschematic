import { Generic } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["draft", "sent", "paid", "void"],
  transitions: [
    { from: "draft", to: "sent" },
    { from: "sent", to: "draft" },
    { from: "sent", to: "paid" },
    { from: "draft", to: "void" },
    { from: "sent", to: "void" },
  ],
  outcomes: { void: "neutral" },
})
@behavior("StateRules", {
  states: {
    sent: { required: ["customer", "amount", "lines"], frozen: ["customer", "amount", "lines"] },
    paid: { frozen: "all" },
    void: { frozen: "all" },
  },
  permission: "invoices.amend",
})
export abstract class Invoice {
  title: Validate<string, { maxLength: 200 }>;
  customer?: string;
  amount?: Generic.Int64;
  lines?: string[];
  notes?: string;
}
