import { shelf } from "@acme/schematic";
import { Identity } from "superscalar";

// @shelf is registered for the Catalog kind only.
export abstract class Product {
  @shelf({ aisle: 3 })
  sku: Identity.Slug;
}
