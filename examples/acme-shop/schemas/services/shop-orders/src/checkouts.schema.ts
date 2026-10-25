import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Secret, Validate, display } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";
import {
  Authenticated,
  HttpMethod,
  hmacVerified,
  publicRoute,
  requirePermission,
  rest,
  webhook
} from "@superschematic/api";
import { ShippingAddress } from "@acme/shop-db";

// acmepay's keys, declared once, so every deployable whose config holds
// them shares one secret of each per environment (stack-model 4.2): the
// API key each call to acmepay carries, and the key acmepay signs its
// webhooks with. Neither is ever in the engine's config, its document or
// its log (D70).
export abstract class PaymentsSecrets {
  ACMEPAY_API_KEY: Secret<string>;
  ACMEPAY_WEBHOOK_SECRET: Secret<string>;
}

// shop-orders' config: acmepay's keys, and where acmepay listens, its
// default the address go/acmepay's command takes on this machine.
@envVars
export abstract class OrdersConfig extends PaymentsSecrets {
  ACMEPAY_URL: Default<string, "http://127.0.0.1:4250">;
}

// Where a checkout is in its Workflow (shop-db's Checkout).
export enum CheckoutStatus {
  Cart = "cart",
  Held = "held",
  Placed = "placed",
  Expired = "expired",
  Cancelled = "cancelled"
}

// One line of a checkout as its shopper sees it: the product, how many,
// whether its units are held, and the price they were held at.
export abstract class CheckoutLineView {
  productId: Identity.UUID;
  quantity: Generic.Int64;
  // pending, held, committed, released or returned (Holds.items).
  state: string;
  @display({ currency: "GBP" })
  unitPriceCents: Nullable<Generic.Int64>;
}

// A checkout as its shopper, or staff, sees it.
export abstract class CheckoutView {
  id: Identity.UUID;
  // ORD-2026-00001, the number Numbering gave it.
  number: Nullable<string>;
  status: CheckoutStatus;
  lines: CheckoutLineView[];
  // The pickup slot it holds, when it is collected.
  pickupAt: Nullable<Temporal.DateTime>;
  // What it owes, in pence, once its lines are held, in its currency.
  @display({ currency: { field: "currency" } })
  dueCents: Generic.Int64;
  currency: Nullable<string>;
  // unpaid, pending, authorized, paid, partiallyRefunded or refunded
  // (Payments.state).
  payment: string;
  // The order it was placed as, once shop-orders wrote it.
  orderId: Nullable<Identity.UUID>;
}

export abstract class StartCheckoutInput {
  // Where the order ships, or who collects it.
  shippingAddress: ShippingAddress;
}

// What the shopper's page needs to pay at acmepay: the payment the
// engine opened, acmepay's intent for it, and the client secret acmepay's
// hosted fields take, so a card number never reaches the shop.
export abstract class CheckoutPayment {
  attemptId: string;
  intentId: string;
  clientSecret: string;
  @display({ currency: { field: "currency" } })
  amountCents: Generic.Int64;
  currency: string;
}

export abstract class RequestRefundInput {
  @display({ currency: "GBP" })
  amountCents: Validate<Generic.Int64, { min: 1 }>;
  reason: Validate<string, { minLength: 1; maxLength: 500 }>;
}

// A refund as the shopper who asked for it sees it.
export abstract class RefundView {
  id: Identity.UUID;
  number: Nullable<string>;
  // requested, approved, issued, rejected or failed.
  status: string;
  @display({ currency: "GBP" })
  amountCents: Generic.Int64;
  reason: string;
}

// The object an acmepay event is about: a payment intent, with the
// engine's attempt it pays, or a refund, with the engine's refund it pays,
// and the shop's reference, the checkout's id, which shop-orders sent when
// it made it.
export abstract class AcmepayObject {
  id: Validate<string, { maxLength: 255 }>;
  object: string;
  amount: Generic.Int64;
  currency: Validate<string, { maxLength: 3 }>;
  status: string;
  paymentIntent: Nullable<string>;
  reference: Nullable<string>;
  attempt: Nullable<string>;
  refund: Nullable<string>;
}

export abstract class AcmepayEventData {
  object: AcmepayObject;
}

// An event acmepay posts: what happened, to which object, and when, in
// seconds since the epoch.
export abstract class AcmepayEvent {
  id: Validate<string, { maxLength: 255 }>;
  type: Validate<string, { maxLength: 100 }>;
  created: Generic.Int64;
  data: AcmepayEventData;
}

export abstract class PaymentEventReceipt {
  received: boolean;
}

// A shopper's checkout, from the cart shop-cart filled: they start it,
// which holds its units and its pickup slot, pay for it at acmepay, read
// it, and ask for a refund once it is placed. Each operation takes the
// caller's own checkout, or any for staff, and another customer's is not
// found. Each calls shop-db's engine in-process, as shop-orders.
export class Checkouts extends Authenticated {
  @rest(HttpMethod.POST, "checkouts/{id}/start")
  @requirePermission(["orders.write"])
  startCheckout(id: Identity.UUID, input: StartCheckoutInput): CheckoutView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "checkouts/{id}/pay")
  @requirePermission(["orders.write"])
  payCheckout(id: Identity.UUID): CheckoutPayment {
    throw new Error("schema declaration only");
  }

  // A placed checkout's order is written the first time it is read, if
  // SettlePayments has not written it yet.
  @rest(HttpMethod.GET, "checkouts/{id}")
  @requirePermission(["orders.read"])
  getCheckout(id: Identity.UUID): CheckoutView {
    throw new Error("schema declaration only");
  }

  // Asks for a refund of a placed checkout, which two members of staff
  // sign off before it is issued.
  @rest(HttpMethod.POST, "checkouts/{id}/refunds")
  @requirePermission(["orders.write"])
  requestRefund(id: Identity.UUID, input: RequestRefundInput): RefundView {
    throw new Error("schema declaration only");
  }
}

// acmepay's events: its signature is checked before anything else
// (D26), and each event is recorded on the checkout or the refund it is
// about, once however often acmepay sends it.
export class PaymentWebhooks {
  @webhook
  @publicRoute
  @hmacVerified({ provider: "acmepay" })
  @rest(HttpMethod.POST, "payments/acmepay/events")
  paymentEvents(event: AcmepayEvent): PaymentEventReceipt {
    throw new Error("schema declaration only");
  }
}
