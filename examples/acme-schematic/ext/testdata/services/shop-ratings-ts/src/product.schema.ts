import { behavior } from "@superschematic/schema";
import { Identity } from "superscalar";

// A catalog item shoppers rate. The behavior adds its rating fields and operations.
@behavior("acme.Rating", { maxStars: 5 })
export abstract class Product {
  sku: Identity.Slug;

  name: string;
}
