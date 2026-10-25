import { channel, key } from "@superschematic/memory-store";
import { Generic, Identity } from "superscalar";
import { Region, StockLevel } from "@schemas/fixture-cache-common";

/** A product's stock changed; a waiter rereads it. */
@channel()
export abstract class StockChanged {
  productId: Identity.UUID;
}

/** Stock samples for a product page. */
@channel({ payload: StockLevel })
export abstract class StockSamples {
  productId: Identity.UUID;
}

/** One receipt email per order. */
@key({ use: "claim", ttlSeconds: 600 })
export abstract class ReceiptSent {
  orderId: Identity.UUID;
}

/** Views of a product a day, in a region. */
@key({ use: "counter", maxTtlSeconds: 86400, name: "views" })
export abstract class ProductViews {
  productId: Identity.UUID;
  day: Generic.Int64;
  region: Region;
  mobile: boolean;
  label: string;
}
