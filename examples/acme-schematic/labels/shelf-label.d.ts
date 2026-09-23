// The label printed on a shelf edge. The label printer reads this shape;
// `acme-schematic fields labels/shelf-label.d.ts ShelfLabel` lists it.
import type { Location } from "./location";

export type Currency = "EUR" | "GBP";

export interface ShelfLabel {
  sku: string;
  price: number;
  currency: Currency;
  location: Location;
  promo?: string;
}
