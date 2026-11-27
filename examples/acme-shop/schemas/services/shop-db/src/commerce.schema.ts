import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate, display, jsonField } from "@superschematic/schema";
import {
  Approvals,
  AutoGenerate,
  Calendar,
  Holds,
  Numbering,
  Payments,
  Relation,
  StateRules,
  Stock,
  Timers,
  Workflow,
  key
} from "@superschematic/db";

import { User } from "./shop.schema";

// The shop's commerce (D70, D103): tables that implement the commerce
// behaviors, whose state is columns of each table and sub-tables beside
// it, and whose operations run through the behaviors' core in the
// repositories' transactions. shop-stock keeps the stock, shop-cart fills a
// shopper's cart and reads the pickup slots, and shop-orders holds, pays
// for, places and refunds a checkout, each through shop-db's generated
// repositories; shop-orders' behavior worker CheckoutTimers expires a
// checkout held too long.

// The units the warehouse holds of a product: one row per product, whose
// key is the product's id. shop-stock's staff routes receive and count
// them, and a checkout holds them at the price they sell for, in pounds.
// Its available units at or below reorderAt make it low, which
// shop-stock's job OrderRestocks finds.
export abstract class Inventory
  implements
    Stock<Inventory, {
      price: { field: "priceCents"; currency: "GBP" };
      lowAt: { field: "reorderAt" };
      maxPerHold: 99;
      backorder: false;
      permission: "stock.write";
    }>
{
  // The product's id.
  @key
  id: Identity.UUID;
  sku: Validate<string, { maxLength: 64 }>;
  // What one unit sells for, in pence. A checkout keeps the price its line
  // was held at.
  @display({ currency: "GBP" })
  priceCents: Generic.Int64;
  // The level at which the product is low; none, never low.
  reorderAt: Nullable<Generic.Int64>;
}

// A counter a shopper collects an order from, which opens fifteen-minute
// pickup slots in London's time, four shoppers at a time, from two hours
// ahead to sixty days ahead, with five minutes after each to clear it.
export abstract class CollectionPoint
  implements
    Calendar<CollectionPoint, {
      timeZone: "Europe/London";
      hours: {
        mon: [["09:00", "12:30"], ["13:30", "17:30"]];
        tue: [["09:00", "17:30"]];
        wed: [["09:00", "17:30"]];
        thu: [["09:00", "17:30"]];
        fri: [["09:00", "17:30"]];
        sat: [["10:00", "16:00"]];
      };
      capacity: 4;
      slotMs: 900000;
      minMs: 900000;
      maxMs: 900000;
      bufferMs: { after: 300000 };
      leadMs: 7200000;
      horizonMs: 5184000000;
      permission: "calendar.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  name: Validate<string, { maxLength: 200 }>;
}

// One line of a checkout: an Inventory, by its key, which is the
// product's id, and how many: a value of the checkout, not a table.
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
  line2: Nullable<Validate<string, { maxLength: 100 }>>;
  city: Validate<string, { minLength: 1; maxLength: 60 }>;
  postcode: Validate<string, { maxLength: 12 }>;
  country: Validate<string, { pattern: "^[A-Z]{2}$" }>;
}

// A shopper's checkout. shop-cart keeps it in cart while the shopper fills
// it; shop-orders' startCheckout moves it to held, which holds each line's
// units and the pickup slot at the counter, all of them or none;
// payCheckout opens its payment with acmepay, and once acmepay's capture
// is recorded shop-orders moves it to placed, which Payments allows only
// once it is paid, which commits what it holds, and writes its Order row.
// A checkout still held fifteen minutes on expires, through the deadline
// shop-orders' behavior worker runs, and lets go of what it holds, unless
// a payment is in flight, which holds it until the two-hour backstop. A
// capture that arrives after it expired is refunded.
export abstract class Checkout
  implements
    Holds<Checkout, {
      holds: {
        lines: { each: { field: "lines"; target: "inventoryId" }; schema: "Inventory"; quantity: { field: "quantity" } };
        pickup: { link: "counter"; interval: { start: "pickupAt"; durationMs: 900000 } };
      };
      takeOn: ["held"];
      commitOn: ["placed"];
      releaseOn: ["expired", "cancelled"];
      maxItems: 50;
    }>,
    Timers<Checkout, {
      deadlines: {
        holdExpiry: { start: { enters: "held" }; afterMs: 900000; then: { transition: "expired" }; until: { leaves: "held" } };
        backstop: { start: { enters: "held" }; afterMs: 7200000; then: { transition: "expired" }; until: { leaves: "held" } };
      };
    }>,
    Payments<Checkout, {
      amount: { holds: true };
      require: { placed: "paid" };
      voidOn: ["expired", "cancelled"];
      afterRelease: "refund";
      refunds: { permission: "payments.refund" };
      record: { permission: "payments.record" };
    }>,
    StateRules<Checkout, {
      states: {
        held: { required: ["address"]; frozen: ["lines", "counterId", "pickupAt", "address"] };
        placed: { frozen: "all" };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  // The shopper whose checkout it is, whom shop-cart and shop-orders check
  // the caller against.
  customer: Relation<User, { onDelete: "RESTRICT" }>;
  lines: Validate<CheckoutLine[], { listMax: 50 }>;
  // The counter the shopper collects it from, and when; none, it ships.
  counter: Nullable<Relation<CollectionPoint, { onDelete: "RESTRICT" }>>;
  pickupAt: Nullable<Temporal.DateTime>;
  address: Nullable<CheckoutAddress>;
  status: Workflow<{
    states: ["cart", "held", "placed", "expired", "cancelled"];
    initial: "cart";
    transitions: [
      { from: "cart"; to: "held" },
      { from: "held"; to: "placed" },
      { from: "held"; to: "expired" },
      { from: "held"; to: "cancelled" },
      { from: "cart"; to: "cancelled" }
    ];
    outcomes: { expired: "failure"; cancelled: "neutral" };
  }>;
  // ORD-2026-00001, counted per year.
  number: Numbering<{ format: "ORD-{yyyy}-{seq:5}"; reset: "yearly" }>;
}

// A refund of a placed checkout, which two staff who hold refunds.approve
// sign off before it is issued: entering approved asks the checkout's
// Payments for the refund, which shop-orders' job SettlePayments makes at
// acmepay and records on the checkout, which settles it here; the job then
// moves it to issued.
export abstract class Refund
  implements
    Approvals<Refund, { gates: { approved: { count: 2; permission: "refunds.approve" } } }>,
    StateRules<Refund, { states: { requested: { required: ["amountCents", "reason"] }; approved: { frozen: "all" } } }>,
    Payments<Refund, {
      amount: { field: "amountCents" };
      currency: "GBP";
      refundOf: { link: "checkout" };
      issueOn: ["approved"];
      record: { permission: "payments.record" };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  // The checkout it refunds.
  checkout: Relation<Checkout, { onDelete: "RESTRICT" }>;
  // How much goes back, in pence: Payments' currency, GBP.
  @display({ currency: "GBP" })
  amountCents: Generic.Int64;
  reason: Validate<string, { maxLength: 500 }>;
  // Who asked for it, the shopper or a member of staff, whom the
  // repositories stamp: Approvals' gate counts no sign-off of theirs.
  createdBy: Identity.UUID;
  status: Workflow<{
    states: ["requested", "approved", "issued", "rejected", "failed"];
    initial: "requested";
    transitions: [
      { from: "requested"; to: "approved" },
      { from: "requested"; to: "rejected" },
      { from: "approved"; to: "issued" },
      { from: "approved"; to: "failed" }
    ];
    outcomes: { rejected: "neutral"; failed: "failure" };
  }>;
  // RF-2026-00001, counted per year.
  number: Numbering<{ format: "RF-{yyyy}-{seq:5}"; reset: "yearly" }>;
}
