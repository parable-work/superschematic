// Type-level checks of Stock<T, C>, Holds<T, C>, Calendar<T, C> and
// Payments<T, C> (D70, D103's step 4).
// Nothing imports this file: `bun run typecheck`, which `make ts` runs,
// checks it, and fails on an expected error (@ts-expect-error) that no
// longer errors.
import type { Calendar, CalendarOn, HoldOn, Holds, HoldsOn, Payments, PaymentsOn, Stock, StockOn } from "./index";

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

// A desk whose time Calendar opens, and a booking that holds a slot of it.
export abstract class Desk
  implements
    Calendar<Desk, {
      timeZone: "Europe/London";
      hours: { mon: [["09:00", "17:30"]]; sat: [["10:00", "13:00"], ["14:00", "16:00"]] };
      capacity: { field: "places" };
      slotMs: 900000;
      bufferMs: { after: 300000 };
      permission: "desk.manage";
    }>
{
  id: string;
  places: number;
}

const opened: CalendarOn<Desk> = { timeZone: "America/New_York", hours: { sun: [] }, capacity: 2, leadMs: 3600000 };
// @ts-expect-error a calendar names its time zone
const noZone: CalendarOn<Desk> = { hours: { mon: [["09:00", "17:00"]] } };
// @ts-expect-error a day is mon to sun
const someday: CalendarOn<Desk> = { timeZone: "UTC", hours: { someday: [["09:00", "17:00"]] } };
// @ts-expect-error capacity.field is the table's own
const unknownCapacity: CalendarOn<Desk> = { timeZone: "UTC", hours: {}, capacity: { field: "seats" } };

// An order that takes payments over its own total, and a refund of one.
export abstract class Order
  implements
    Payments<Order, {
      amount: { field: "totalCents" };
      currency: "GBP";
      require: { placed: "paid" };
      voidOn: ["cancelled"];
      refunds: { permission: "payments.refund" };
      record: { permission: "payments.record" };
    }>
{
  id: string;
  totalCents: number;
}

export abstract class Refund
  implements
    Payments<Refund, {
      refundOf: { link: "order" };
      amount: { field: "amountCents" };
      currency: "GBP";
      issueOn: ["approved"];
      record: { permission: "payments.record" };
    }>
{
  id: string;
  amountCents: number;
  order: string;
}

const fromHolds: PaymentsOn<Order> = { amount: { holds: true }, record: { permission: "payments.record" }, capture: "manual", captureOn: ["held"] };
// @ts-expect-error an amount field is the table's own
const unknownAmount: PaymentsOn<Order> = { amount: { field: "total" }, currency: "GBP", record: { permission: "payments.record" } };
// @ts-expect-error the adapter's permission is named
const noRecord: PaymentsOn<Order> = { amount: { field: "totalCents" }, currency: "GBP" };
// @ts-expect-error require waits for authorized or paid
const waitsForever: PaymentsOn<Order> = { amount: { field: "totalCents" }, currency: "GBP", require: { placed: "settled" }, record: { permission: "p" } };
