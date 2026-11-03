import { strictJSON } from "@superschematic/schema";
import { Generic, Identity } from "superscalar";

/** Where a product is stocked. */
export enum Region {
  North = "north",
  South = "south",
}

/** A product's stock at one moment. */
export abstract class StockLevel {
  productId: Identity.UUID;
  onHand: Generic.Int64;
}

/** A sample whose decoder refuses unknown keys. */
@strictJSON
export abstract class StrictSample {
  productId: Identity.UUID;
}
