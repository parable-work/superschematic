import { Generic } from "superscalar";

export enum Currency {
  EUR = "EUR",
  GBP = "GBP",
  USD = "USD"
}

// A price in the currency's smallest unit: 1999 EUR is 19.99 euros.
export abstract class Price {
  amountCents: Generic.Int64;
  currency: Currency;
}
