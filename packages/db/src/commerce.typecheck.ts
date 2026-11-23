// Type-level checks of Stock<T, C> and Holds<T, C> (D70, D103's step 4).
// Nothing imports this file: `bun run typecheck`, which `make ts` runs,
// checks it, and fails on an expected error (@ts-expect-error) that no
// longer errors.
import type { HoldOn, Holds, HoldsOn, Stock, StockOn } from "./index";

// The carriers impose no members: a table implements them with its own
// fields alone, and their first type argument is the table itself. An
// inventory counted by Stock, and a checkout whose lines and gift wrap
// Holds takes as it enters held.
export abstract class Inventory
  implements
    Stock<Inventory, { permission: "stock.adjust"; price: { field: "priceCents"; currency: "GBP" }; lowAt: { field: "reorderAt" }; maxPerHold: 5 }>
{
  id: string;
  title: string;
  priceCents: number;
  reorderAt: number;
}

export abstract class Checkout
  implements
    Holds<Checkout, {
      holds: {
        lines: { each: { field: "lines"; target: "inventoryId" }; schema: "Inventory"; quantity: { field: "quantity" } };
        wrap: { link: "wrap"; quantity: 1 };
      };
      takeOn: ["held"];
      commitOn: ["placed"];
      releaseOn: ["cancelled"];
    }>
{
  id: string;
  lines: { inventoryId: string; quantity: number }[];
  wrap: string;
}

const counted: StockOn<Inventory> = { permission: "stock.adjust", lowAt: 2, backorder: true, price: { field: "priceCents", currency: { field: "title" } } };
const timed: HoldOn<Checkout> = { link: "wrap", interval: { start: "pickupAt", durationMs: 900000 } };
const held: HoldsOn<Checkout> = { holds: { wrap: timed }, takeOn: ["held"], maxItems: 10, status: "status" };

// Stock's permission is required, and its fields are the table's own.
// @ts-expect-error a supply names the permission its counts need
const noPermission: StockOn<Inventory> = { lowAt: 1 };
// @ts-expect-error price.field is the table's own
const unknownPrice: StockOn<Inventory> = { permission: "p", price: { field: "cost", currency: "GBP" } };
// A hold reads a link or a list of the table's, asking a quantity or an interval.
// @ts-expect-error a link hold names a field of the table
const unknownLink: HoldOn<Checkout> = { link: "gift", quantity: 1 };
// @ts-expect-error a hold asks a quantity or an interval
const askingNothing: HoldOn<Checkout> = { link: "wrap" };
// @ts-expect-error holds names the holds
const noHolds: HoldsOn<Checkout> = { takeOn: ["held"] };
