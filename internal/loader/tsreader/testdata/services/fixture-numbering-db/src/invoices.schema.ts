import { Identity, Temporal } from "superscalar";
import { AutoGenerate, Numbering, Relation, index, key } from "@superschematic/db";

// A customer an invoice is billed to.
export abstract class Customer {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

// An invoice a person reads out by its number (D56, D103's step 4): each
// customer's invoices are numbered from 1 each UTC year, INV-2026-0042,
// so a label is unique within its customer, which the index holds. A
// refused create takes no number, and a delete leaves a gap. An invoice
// made before the table numbered has none until assignNumber gives it the
// next number of the year it was made in, read from its createdAt.
@index<Invoice>(["customer", "number"], { unique: true })
export abstract class Invoice {
  @key
  id: AutoGenerate<Identity.UUID>;
  customer: Relation<Customer>;
  title: string;
  createdAt: Temporal.DateTime;
  number: Numbering<{ format: "INV-{yyyy}-{seq:4}"; scope: { link: "customer" }; reset: "yearly" }>;
}

// A support ticket, numbered across the table, T-17, and given a
// reference of its own that counts apart.
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  subject: string;
  number: Numbering<{ format: "T-{seq}" }>;
  reference: Numbering<{ format: "REF-{seq:5}" }>;
}
