import { Generic, Identity, Temporal } from "superscalar";
import { Default, Nullable } from "@superschematic/schema";
import { AutoGenerate, Relation, index, key, queue } from "@superschematic/db";

import { Auditable, Product } from "./shop.schema";

// Enqueued by shop-stock when a sale leaves a product low, its Inventory's
// available units at or below its reorderAt (Stock.low, D70), and by its
// job OrderRestocks for one a count left low: shop-stock's worker
// ReorderStock files a restock request for the product. The message holds
// the units available and the reorder level as the sale left them. One
// waits per product: a later sale that leaves the product low replaces it,
// so the worker reads the latest level, and the worker handles one
// product's message at a time. A message whose handler fails is retried
// every half minute, five times at most.
@queue({ retries: 5, backoff: "30s", key: "productId", onDuplicate: "replace", serialize: true })
export abstract class StockLow {
  productId: Identity.UUID;
  quantity: Generic.Int64;
  threshold: Generic.Int64;
}

// A restock request is open until the supplier is asked for it, then
// ordered.
export enum RestockStatus {
  Open = "open",
  Ordered = "ordered"
}

// A request to the supplier for more of a product: shop-stock's worker
// ReorderStock files one when a sale runs the product low, and its job
// OrderRestocks orders each open one. A product has at most one
// open request: openProductId is the product while the request is open and
// null once it is ordered, and the unique index over it admits one row per
// product, since a unique index lets nulls repeat.
@index<RestockRequest>(["openProductId"], { unique: true, name: "one_open_per_product" })
export abstract class RestockRequest extends Auditable {
  @key
  id: AutoGenerate<Identity.UUID>;

  product: Relation<Product, { onDelete: "CASCADE" }>;

  status: Default<RestockStatus, RestockStatus.Open>;

  // How many units the supplier is asked for.
  quantity: Generic.Int64;

  openProductId: Nullable<Identity.UUID>;

  // When OrderRestocks ordered it.
  orderedAt: Nullable<Temporal.DateTime>;
}
