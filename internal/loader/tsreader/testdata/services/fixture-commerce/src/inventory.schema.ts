import { Finance, Generic } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Stock", {
  price: { field: "priceCents", currency: "GBP" },
  lowAt: { field: "reorderAt" },
  maxPerHold: 99,
  backorder: false,
  permission: "stock.adjust",
})
export abstract class Inventory {
  sku: Validate<string, { maxLength: 64 }>;
  priceCents: Finance.Money;
  reorderAt?: Generic.Int64;
}
