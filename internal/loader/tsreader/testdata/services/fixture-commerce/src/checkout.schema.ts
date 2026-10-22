import { Generic, Temporal } from "superscalar";
import { behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["cart", "held", "placed", "expired", "cancelled"],
  transitions: [
    { from: "cart", to: "held" },
    { from: "held", to: "placed" },
    { from: "held", to: "expired" },
    { from: "held", to: "cancelled" },
    { from: "cart", to: "cancelled" },
  ],
  outcomes: { expired: "failure", cancelled: "neutral" },
})
@behavior("Links", { links: { counter: { schema: "CollectionPoint" } } })
@behavior("Holds", {
  holds: {
    lines: { each: { field: "lines", target: "inventoryId" }, schema: "Inventory", quantity: { field: "quantity" } },
    pickup: { link: "counter", interval: { start: "pickupAt", durationMs: 900000 } },
  },
  takeOn: ["held"],
  commitOn: ["placed"],
  releaseOn: ["expired", "cancelled"],
  maxItems: 50,
})
@behavior("Timers", {
  deadlines: {
    holdExpiry: { start: { enters: "held" }, afterMs: 900000, then: { transition: "expired" }, until: { leaves: "held" } },
    backstop: { start: { enters: "held" }, afterMs: 7200000, then: { transition: "expired" }, until: { leaves: "held" } },
  },
})
@behavior("Payments", {
  amount: { holds: true },
  require: { placed: "paid" },
  voidOn: ["expired", "cancelled"],
  afterRelease: "refund",
  refunds: { permission: "payments.refund" },
  record: { permission: "payments.record" },
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "paid" } }, then: { transition: "placed" } }] })
@behavior("Numbering", { format: "ORD-{yyyy}-{seq:5}", reset: "yearly" })
@behavior("StateRules", { states: { held: { frozen: ["lines", "pickupAt"] }, placed: { frozen: "all" } } })
export abstract class Checkout {
  lines: CheckoutLine[];
  pickupAt?: Temporal.DateTime;
}

export abstract class CheckoutLine {
  inventoryId: string;
  quantity: Generic.Int64;
}
