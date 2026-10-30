// How a shipment's payment settles, read by the schema with typeof: a
// constant declared as const keeps a long config readable (D103).
export const paymentFlow = {
  states: ["unpaid", "paid", "refunded"],
  transitions: [
    { from: "unpaid", to: "paid" },
    { from: "paid", to: "refunded", permission: "payments.refund" },
  ],
  outcomes: { refunded: "failure" },
} as const;
