// An invoice's payment, and its Timers, read by the schema with typeof (D103).
export const invoicePayment = {
  states: ["unpaid", "overdue", "paid"],
  transitions: [
    { from: "unpaid", to: "overdue" },
    { from: "unpaid", to: "paid" },
    { from: "overdue", to: "paid" },
  ],
} as const;
export const invoiceTimers = {
  status: "payment",
  deadlines: {
    overdue: { start: { create: true }, afterMs: 2592000000, then: { transition: "overdue" }, until: { leaves: "unpaid" } },
  },
} as const;
