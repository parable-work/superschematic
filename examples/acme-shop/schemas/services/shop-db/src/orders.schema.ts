import { Contact, Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate, jsonField } from "@superschematic/schema";
import { AutoGenerate, HasMany, Relation, index, key, queue, searchField } from "@superschematic/db";

import { Auditable, Product, User } from "./shop.schema";

// A postal address, as a shopper enters it at checkout. @jsonField keeps it
// out of the tables: a field of this type is a JSONB column of its row.
@jsonField
export abstract class ShippingAddress {
  recipient: Identity.Name;
  line1: Validate<string, { minLength: 1; maxLength: 100 }>;
  line2: Nullable<Validate<string, { maxLength: 100 }>>;
  city: Validate<string, { minLength: 1; maxLength: 60 }>;
  postcode: Validate<string, { maxLength: 12 }>;
  // ISO 3166-1 alpha-2, such as GB or DE.
  country: Validate<string, { pattern: "^[A-Z]{2}$" }>;
  phone: Nullable<Contact.PhoneNumber>;
}

// An order is placed, fulfilled once its lines are picked, then shipped,
// unless it is cancelled while it is still placed.
export enum OrderStatus {
  Placed = "placed",
  Fulfilled = "fulfilled",
  Shipped = "shipped",
  Cancelled = "cancelled"
}

// A shopper's order. Staff look orders up by customer, newest first.
@index<Order>(["customer", "placedAt"])
export abstract class Order extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  customer: Relation<User, { onDelete: "RESTRICT" }>;

  status: Default<OrderStatus, OrderStatus.Placed>;

  placedAt: Temporal.DateTime;

  // Where the order ships.
  shippingAddress: ShippingAddress;

  // Why the order was cancelled, when it was.
  cancelReason: Nullable<Validate<string, { maxLength: 500 }>>;

  lines: HasMany<OrderLine>;
}

// Placed with each order, in the transaction that writes it: shop-orders'
// worker FulfilOrders handles each message, and a message whose handler
// fails is retried every half minute, five times at most.
@queue({ retries: 5, backoff: "30s" })
export abstract class OrderPlaced {
  orderId: Identity.UUID;
}

// One product on an order, at the price the shopper paid.
export abstract class OrderLine {
  @key
  id: AutoGenerate<Identity.UUID>;

  order: Relation<Order>;

  product: Relation<Product, { onDelete: "RESTRICT" }>;

  quantity: Generic.Int64;

  unitPriceCents: Generic.Int64;
}

// A shopper's review of a product. Shoppers search reviews by their text,
// and each shopper reviews a product once. A moderator hides a review by
// deleting it; the row stays, marked deleted.
@index<Review>(["product", "author"], { unique: true, name: "one_per_author" })
export abstract class Review extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  product: Relation<Product>;

  author: Relation<User>;

  rating: Validate<number, { min: 1; max: 5 }>;

  @searchField
  title: Validate<string, { maxLength: 120 }>;

  @searchField
  body: string;

  deletedAt: Nullable<Temporal.DateTime>;
  deletedBy: Nullable<Identity.UUID>;
}
