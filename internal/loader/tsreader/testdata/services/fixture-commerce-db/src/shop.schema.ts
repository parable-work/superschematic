import { Generic, Identity } from "superscalar";
import { Nullable, jsonField } from "@superschematic/schema";
import { AutoGenerate, Holds, Relation, Stock, Workflow, key } from "@superschematic/db";

// What a shop sells (D70, D103's step 4): units on hand and units claims
// hold, counted by Stock, each change a movement of its ledger. A caller
// holding stock.adjust receives, adjusts and counts it; its price, in GBP
// minor units, is what a line is held at; and it reads low at or below
// its reorder level.
export abstract class Inventory
  implements
    Stock<Inventory, {
      permission: "stock.adjust";
      price: { field: "priceCents"; currency: "GBP" };
      lowAt: { field: "reorderAt" };
      maxPerHold: 5;
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  priceCents: Generic.Int64;
  reorderAt: Nullable<Generic.Int64>;
}

// One line of a checkout: the inventory it takes and how many.
@jsonField
export abstract class Line {
  inventoryId: string;
  quantity: Generic.Int64;
}

// A checkout that holds its lines and its gift wrap while it waits for
// payment (D70, D103's step 4): entering held takes every item or none,
// through the contract Supply each Inventory implements; placed commits
// them, expired and cancelled let them go, and returned puts the units
// back.
export abstract class Checkout
  implements
    Holds<Checkout, {
      holds: {
        lines: { each: { field: "lines"; target: "inventoryId" }; schema: "Inventory"; quantity: { field: "quantity" } };
        wrap: { link: "wrap"; quantity: 1 };
      };
      takeOn: ["held"];
      commitOn: ["placed"];
      releaseOn: ["expired", "cancelled"];
      returnOn: ["returned"];
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  lines: Line[];
  wrap: Nullable<Relation<Inventory>>;
  status: Workflow<{
    states: ["cart", "held", "placed", "expired", "cancelled", "returned"];
    initial: "cart";
    transitions: [
      { from: "cart"; to: "held" },
      { from: "held"; to: "placed" },
      { from: "held"; to: "expired" },
      { from: "held"; to: "cancelled" },
      { from: "cart"; to: "cancelled" },
      { from: "placed"; to: "returned" }
    ];
  }>;
}
