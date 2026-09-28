import { Identity } from "superscalar";
import { Validate, strictJSON } from "@superschematic/schema";

import { Price } from "./price.schema";

// One row of a supplier's product feed. A key this type does not declare is
// a supplier's mistake, so every decoder refuses it.
@strictJSON
export abstract class FeedItem {
  sku: Identity.Slug;
  name: Validate<string, { minLength: 1; maxLength: 200 }>;
  price: Price;
  tags: Validate<string[], { listMax: 20 }>;
  // Free-form details by name, such as origin or harvest.
  attributes: Record<string, string>;
}
