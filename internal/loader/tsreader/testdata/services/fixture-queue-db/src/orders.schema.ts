import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable, Validate, jsonField } from "@superschematic/schema";
import { AutoGenerate, key, queue } from "@superschematic/db";

export enum Priority {
  Low = "low",
  High = "high"
}

// Where an order ships, kept as JSON wherever it is stored.
@jsonField
export abstract class Address {
  line1: string;
  city: string;
}

// An order of the shop.
export abstract class Order {
  @key
  id: AutoGenerate<Identity.UUID>;

  placedAt: Temporal.DateTime;
}

// Placed when an order is: the worker that fulfils orders handles it.
@queue({ retries: 3, backoff: "1m", lease: "2m" })
export abstract class OrderPlaced {
  orderId: Identity.UUID;

  placedAt: Temporal.DateTime;

  priority: Default<Priority, Priority.Low>;

  quantity: Generic.Int64;

  note: Nullable<Validate<string, { maxLength: 200 }>>;

  shipTo: Address;

  tags: string[];
}

// Sent when nobody asked for anything in particular.
@queue()
export abstract class Ping {
  sentAt: Nullable<Temporal.DateTime>;
}
