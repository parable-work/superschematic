import { Finance } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["requested", "approved", "issued", "rejected", "failed"],
  transitions: [
    { from: "requested", to: "approved" },
    { from: "requested", to: "rejected" },
    { from: "approved", to: "issued" },
    { from: "approved", to: "failed" },
  ],
  outcomes: { rejected: "neutral", failed: "failure" },
})
@behavior("Approvals", { gates: { approved: { count: 2, permission: "refunds.approve" } } })
@behavior("Links", { links: { order: { schema: "Checkout", required: true } } })
@behavior("Numbering", { format: "RF-{yyyy}-{seq:5}", reset: "yearly" })
@behavior("StateRules", { states: { requested: { required: ["amountCents", "reason"] }, approved: { frozen: "all" } } })
@behavior("Payments", {
  amount: { field: "amountCents" },
  currency: "GBP",
  refundOf: { link: "order" },
  issueOn: ["approved"],
  record: { permission: "payments.record" },
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "refunded" } }, then: { transition: "issued" } }] })
export abstract class Refund {
  amountCents: Finance.Money;
  reason: Validate<string, { maxLength: 500 }>;
}
