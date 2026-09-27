import { behavior } from "@superschematic/schema";
import { Identity } from "superscalar";

@behavior("acme.Stock", { unit: "box", aisles: 3 })
@behavior("acme.Audited")
export abstract class Item {
  sku: Identity.Slug;

  name: string;
}
