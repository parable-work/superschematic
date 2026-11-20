// Type-level checks of Numbering<C> (D56, D103's step 4). Nothing imports
// this file: `bun run typecheck`, which `make ts` runs, checks it, and
// fails on an expected error (@ts-expect-error) that no longer errors.
import type { Numbering, NumberingConfig, Relation } from "./index";

abstract class Customer {
  id: string;
  name: string;
}

// The field's type is the label, or null on a row made before the table
// composed Numbering, which a repository's row keeps as it keeps a
// Reconcile's string | null.
export abstract class Invoice {
  id: string;
  customer: Relation<Customer>;
  number: Numbering<{ format: "INV-{yyyy}-{seq:4}"; scope: { link: "customer" }; reset: "yearly" }>;
  reference: Numbering<{ format: "R{seq:3}" }>;
}

const unnumbered: Invoice["number"] = null;
const numbered: Invoice["number"] = "INV-2026-0042";
const plain: NumberingConfig = { format: "T-{seq}" };

// format is required, reset is yearly alone, and the config names no
// member but format, scope and reset.
// @ts-expect-error a Numbering config names its format
type NoFormat = Numbering<{ reset: "yearly" }>;
// @ts-expect-error reset is yearly or absent
type Daily = Numbering<{ format: "{seq}"; reset: "daily" }>;
// @ts-expect-error a scope names its link
type NoLink = Numbering<{ format: "{seq}"; scope: {} }>;
// @ts-expect-error a Numbering config names no other member
const unknownMember: NumberingConfig = { format: "{seq}", start: 1 };
