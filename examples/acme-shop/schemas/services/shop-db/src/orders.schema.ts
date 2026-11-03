import { Contact, Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate, jsonField } from "@superschematic/schema";
import { AutoGenerate, HasMany, Relation, enqueueOnChange, index, key, queue, searchField, unique } from "@superschematic/db";

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
// unless it is cancelled while it is still placed. A UI labels a member by
// its title, else by its name in words.
export enum OrderStatus {
  Placed = "placed",
  /** @title Picked and packed */
  Fulfilled = "fulfilled",
  Shipped = "shipped",
  Cancelled = "cancelled"
}

// Placed with each order, in the transaction that writes it, and by the
// trigger on an order put back to placed: shop-orders' worker FulfilOrders
// handles each message, and a message whose handler fails is retried every
// half minute, five times at most. One waits per order: an order placed
// again while its message waits adds none.
@queue({ retries: 5, backoff: "30s", key: "orderId", onDuplicate: "skip" })
export abstract class OrderPlaced {
  orderId: Identity.UUID;
}

// A shopper's order. Staff look orders up by customer, newest first. An
// order put back to placed, by staff or by a script in psql, is fulfilled
// again: the database's trigger enqueues its OrderPlaced, whatever wrote
// the row.
@index<Order>(["customer", "placedAt"])
@enqueueOnChange<OrderPlaced, Order>(OrderPlaced, {
  on: ["update"],
  fields: ["status"],
  when: [{ field: "status", equals: OrderStatus.Placed }],
  message: { orderId: "id" },
})
export abstract class Order extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  customer: Relation<User, { onDelete: "RESTRICT" }>;

  status: Default<OrderStatus, OrderStatus.Placed>;

  placedAt: Default<Temporal.DateTime, "now">;

  // Where the order ships.
  shippingAddress: ShippingAddress;

  // Why the order was cancelled, when it was.
  cancelReason: Nullable<Validate<string, { maxLength: 500 }>>;
  // The engine's Checkout the order was placed from, once paid (D70);
  // none for an order placeOrder took. Unique, so a checkout writes one
  // order however often shop-orders looks at it. The column is nullable,
  // so a migration adds it to a database that holds orders already.
  @unique
  checkoutId: Nullable<Identity.UUID>;

  // The sum of each line's quantity times its unit price, written when the
  // order is placed: its lines do not change after. A migration plan adds
  // the column with neither a SQL default nor a backfill, so a database
  // holding orders from before it is purged rather than migrated
  // (README.md).
  totalCents: Generic.Int64;

  lines: HasMany<OrderLine>;
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
