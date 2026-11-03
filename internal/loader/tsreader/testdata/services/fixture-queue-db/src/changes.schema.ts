import { Generic, Identity } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, enqueueOnChange, key, queue, Relation } from "@superschematic/db";
import { Order, Priority } from "./orders.schema";

// A product an order's line may name.
export abstract class Product {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;
}

// Sent when an order's lines change: a newer change replaces the one that
// waits, and the worker handles one order's changes at a time.
@queue({ key: "orderId", onDuplicate: "replace", serialize: true })
export abstract class OrderChanged {
  orderId: Identity.UUID;

  lineId: Nullable<Identity.UUID>;

  priority: Default<Priority, Priority.Low>;
}

// A line of an order. Whatever writes it, a change to its product or its
// quantity enqueues the order's OrderChanged, as does a new line or a
// deleted one, once it names a product.
@enqueueOnChange<OrderChanged, OrderLine>(OrderChanged, {
  on: ["insert", "update", "delete"],
  fields: ["product", "quantity"],
  when: [{ field: "product", notNull: true }],
  message: { orderId: "order", lineId: "id" },
})
export abstract class OrderLine {
  @key
  id: AutoGenerate<Identity.UUID>;

  order: Relation<Order>;

  product: Nullable<Relation<Product>>;

  quantity: Generic.Int64;

  note: Nullable<string>;
}

// Sent when a stock count is asked for: one waits per SKU, and a later ask
// while it waits adds nothing.
@queue({ key: "sku", onDuplicate: "skip" })
export abstract class StockChecked {
  sku: string;

  quantity: Generic.Int64;
}

// Sent when an account's records are to be synced: every request is kept,
// and the worker syncs one account at a time.
@queue({ key: "accountId", serialize: true })
export abstract class SyncRequested {
  accountId: Identity.UUID;

  reason: Nullable<string>;
}
