import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["draft", "submitted", "approved", "paid", "rejected"],
  transitions: [
    { from: "draft", to: "submitted" },
    { from: "submitted", to: "draft" },
    { from: "submitted", to: "approved" },
    { from: "submitted", to: "rejected" },
    { from: "approved", to: "paid" },
  ],
})
@behavior("Approvals", {
  gates: {
    approved: { count: 2, permission: "expenses.approve" },
    paid: { count: 1, permission: "expenses.pay", from: ["approved"] },
  },
})
export abstract class Expense {
  title: Validate<string, { maxLength: 200 }>;
  amount: Validate<number, { min: 0 }>;
}
