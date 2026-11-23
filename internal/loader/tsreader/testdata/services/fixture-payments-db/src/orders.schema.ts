import { Generic, Identity } from "superscalar";
import { AutoGenerate, Payments, Relation, Workflow, key } from "@superschematic/db";

// An order that takes payments (D70, D103's step 4): what it owes is its
// total in GBP minor units, placed waits until it is paid, a cancelled
// order voids what is authorized, a refund drawn directly needs
// payments.refund, and the provider's adapter lists its actions and
// records its events holding payments.record. Its attempts, actions and
// refunds are sub-tables of the row, and the provider events a table
// kept for the table as a whole.
export abstract class Order
  implements
    Payments<Order, {
      amount: { field: "totalCents" };
      currency: "GBP";
      require: { placed: "paid" };
      voidOn: ["cancelled"];
      refunds: { permission: "payments.refund" };
      record: { permission: "payments.record" };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  totalCents: Generic.Int64;
  status: Workflow<{
    states: ["cart", "held", "placed", "cancelled"];
    initial: "cart";
    transitions: [
      { from: "cart"; to: "held" },
      { from: "held"; to: "placed" },
      { from: "cart"; to: "cancelled" },
      { from: "held"; to: "cancelled" }
    ];
  }>;
}

// A refund of an order that needs a go-ahead (D70): entering approved asks
// the order its relation points at for its amount, through the order's
// refundFor, once per approval; the provider's outcome, recorded on the
// order, settles it here through settleRefundFor, which turns its state
// refunded.
export abstract class Refund
  implements
    Payments<Refund, {
      refundOf: { link: "order" };
      amount: { field: "amountCents" };
      currency: "GBP";
      issueOn: ["approved"];
      record: { permission: "payments.record" };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  amountCents: Generic.Int64;
  reason: string;
  order: Relation<Order>;
  status: Workflow<{
    states: ["requested", "approved", "issued"];
    initial: "requested";
    transitions: [
      { from: "requested"; to: "approved" },
      { from: "approved"; to: "issued" }
    ];
  }>;
}
