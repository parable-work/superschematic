import { Finance } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Stock", { price: { field: "priceMinor", currency: { field: "currency" } }, lowAt: 3, permission: "products.stock" })
export abstract class Product {
  title: Validate<string, { maxLength: 200 }>;
  priceMinor: Finance.Money;
  currency: Finance.Currency;
}

@behavior("Workflow", {
  states: ["open", "closed", "void"],
  transitions: [
    { from: "open", to: "closed" },
    { from: "open", to: "void" },
  ],
  outcomes: { void: "neutral" },
})
@behavior("Payments", {
  amount: { field: "totalMinor" },
  currency: { field: "currency" },
  capture: "manual",
  captureOn: ["closed"],
  require: { closed: "authorized" },
  voidOn: ["void"],
  refunds: { permission: "invoices.refund" },
  record: { permission: "invoices.record" },
})
export abstract class Invoice {
  totalMinor: Finance.Money;
  currency: Finance.Currency;
}
