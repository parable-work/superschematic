import { Identity } from "superscalar";
import { shelf } from "@acme/schema";

// A Catalog schema's classes are embedded structs (KindSpec.StructRole).
// @shelf comes from @acme/schema and is only allowed in Catalog schemas; it
// lands in the field's extensions.acme slot in the IR.
export abstract class Product {
  @shelf({ aisle: 3, bay: "B" })
  sku: Identity.Slug;

  @shelf({ aisle: 7 })
  barcode: string;

  name: Identity.Name;
}

export abstract class Bundle {
  @shelf({ aisle: 12 })
  code: Identity.Slug;

  products: Product[];
}
