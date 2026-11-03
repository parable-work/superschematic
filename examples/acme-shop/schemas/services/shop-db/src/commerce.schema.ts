import { Finance, Generic, Identity, Temporal } from "superscalar";
import { Validate, behavior, display, jsonField } from "@superschematic/schema";

// The shop's commerce, kept by the engine (D58, D70): these types compose
// behaviors, so they are engine types, whose instances the engine keeps in
// shop-db's database beside the tables, and which the SQL and the ORM
// leave out. Every server, job and worker of an API on shop-db opens the
// engine; the shop's implementations call it in-process, and staff reach
// it at /engine/shop-db, under the policy in schema.config.ts.

// The units the warehouse holds of a product: one instance per product,
// whose id is the product's. shop-stock's staff routes receive and count
// them, and a checkout holds them at the price they sell for, in pounds.
// A count at or below reorderAt makes Stock.low true, which shop-stock's
// job OrderRestocks lists.
@behavior("Stock", {
  price: { field: "priceCents", currency: "GBP" },
  lowAt: { field: "reorderAt" },
  maxPerHold: 99,
  backorder: false,
  permission: "stock.write"
})
export abstract class Inventory {
  sku: Validate<string, { maxLength: 64 }>;
  // What one unit sells for, in pence. A checkout keeps the price its line
  // was held at.
  @display({ currency: "GBP" })
  priceCents: Finance.Money;
  // The level at which the product is low; none, never low.
  reorderAt?: Generic.Int64;
}

// A counter a shopper collects an order from, which opens fifteen-minute
// pickup slots in London's time, four shoppers at a time, from two hours
// ahead to sixty days ahead, with five minutes after each to clear it.
@behavior("Calendar", {
  timeZone: "Europe/London",
  hours: {
    mon: [["09:00", "12:30"], ["13:30", "17:30"]],
    tue: [["09:00", "17:30"]],
    wed: [["09:00", "17:30"]],
    thu: [["09:00", "17:30"]],
    fri: [["09:00", "17:30"]],
    sat: [["10:00", "16:00"]]
  },
  capacity: 4,
  slotMs: 900000,
  minMs: 900000,
  maxMs: 900000,
  bufferMs: { after: 300000 },
  leadMs: 7200000,
  horizonMs: 5184000000,
  permission: "calendar.manage"
})
export abstract class CollectionPoint {
  name: Validate<string, { maxLength: 200 }>;
}

// One line of a checkout: an Inventory, by its id, which is the product's,
// and how many: a value of the checkout, not a table.
@jsonField
export abstract class CheckoutLine {
  inventoryId: Identity.UUID;
  quantity: Validate<Generic.Int64, { min: 1; max: 99 }>;
}

// Where a checkout's order ships, or who collects it.
@jsonField
export abstract class CheckoutAddress {
  recipient: Identity.Name;
  line1: Validate<string, { minLength: 1; maxLength: 100 }>;
  line2?: Validate<string, { maxLength: 100 }>;
  city: Validate<string, { minLength: 1; maxLength: 60 }>;
  postcode: Validate<string, { maxLength: 12 }>;
  country: Validate<string, { pattern: "^[A-Z]{2}$" }>;
}

// A shopper's checkout. shop-cart keeps it in cart while the shopper fills
// it; shop-orders' startCheckout moves it to held, which holds each line's
// units and the pickup slot, all of them or none; payCheckout opens its
// payment with acmepay, and the capture acmepay reports moves it to
// placed, through the Reactions rule, which commits what it holds and
// lets shop-orders write its Order row. A checkout still held fifteen
// minutes on expires, and lets go of what it holds, unless a payment is
// in flight, which holds it until the two-hour backstop. A capture that
// arrives after it expired is refunded.
@behavior("Workflow", {
  states: ["cart", "held", "placed", "expired", "cancelled"],
  transitions: [
    { from: "cart", to: "held" },
    { from: "held", to: "placed" },
    { from: "held", to: "expired" },
    { from: "held", to: "cancelled" },
    { from: "cart", to: "cancelled" }
  ],
  outcomes: { expired: "failure", cancelled: "neutral" }
})
@behavior("Links", { links: { counter: { schema: "CollectionPoint" } } })
@behavior("Holds", {
  holds: {
    lines: { each: { field: "lines", target: "inventoryId" }, schema: "Inventory", quantity: { field: "quantity" } },
    pickup: { link: "counter", interval: { start: "pickupAt", durationMs: 900000 } }
  },
  takeOn: ["held"],
  commitOn: ["placed"],
  releaseOn: ["expired", "cancelled"],
  maxItems: 50
})
@behavior("Timers", {
  deadlines: {
    holdExpiry: { start: { enters: "held" }, afterMs: 900000, then: { transition: "expired" }, until: { leaves: "held" } },
    backstop: { start: { enters: "held" }, afterMs: 7200000, then: { transition: "expired" }, until: { leaves: "held" } }
  }
})
@behavior("Payments", {
  amount: { holds: true },
  require: { placed: "paid" },
  voidOn: ["expired", "cancelled"],
  afterRelease: "refund",
  refunds: { permission: "payments.refund" },
  record: { permission: "payments.record" }
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "paid" } }, then: { transition: "placed" } }] })
@behavior("Numbering", { format: "ORD-{yyyy}-{seq:5}", reset: "yearly" })
@behavior("StateRules", {
  states: {
    held: { required: ["address"], frozen: ["lines", "pickupAt", "address"] },
    placed: { frozen: "all" }
  }
})
export abstract class Checkout {
  // The shopper whose checkout it is: their user id, which shop-cart and
  // shop-orders check against the caller, since the engine's policy
  // answers for a type, not an instance.
  customerId: Identity.UUID;
  lines: Validate<CheckoutLine[], { listMax: 50 }>;
  // When the shopper collects it, at the counter the Links' counter names;
  // none, it ships.
  pickupAt?: Temporal.DateTime;
  address?: CheckoutAddress;
}

// A refund of a placed checkout, which two staff who hold refunds.approve
// sign off before it is issued: issuing asks the checkout's Payments for
// the refund, which shop-orders' job SettlePayments makes at acmepay and
// reports back, and the Reactions rule moves it to issued once refunded.
@behavior("Workflow", {
  states: ["requested", "approved", "issued", "rejected", "failed"],
  transitions: [
    { from: "requested", to: "approved" },
    { from: "requested", to: "rejected" },
    { from: "approved", to: "issued" },
    { from: "approved", to: "failed" }
  ],
  outcomes: { rejected: "neutral", failed: "failure" }
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
  record: { permission: "payments.record" }
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "refunded" } }, then: { transition: "issued" } }] })
export abstract class Refund {
  // How much goes back, in pence: Payments' currency, GBP.
  @display({ currency: "GBP" })
  amountCents: Finance.Money;
  reason: Validate<string, { maxLength: 500 }>;
  // Who asked for it: the shopper's user id, or a member of staff's.
  requestedBy: Identity.UUID;
}
