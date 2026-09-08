import { meta, shelf, tagged } from "@acme/schematic";
import { Identity } from "@psgen/scalar-lib";

@tagged
@meta({ region: "eu", tiers: [1, 2] })
export abstract class Product {
  @shelf({ aisle: 3 })
  sku: Identity.Slug;

  name: string;
}
