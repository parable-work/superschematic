// Type-level checks of Workflow<C> (D103). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Workflow } from "./index";

type OrderStatus = Workflow<{
  states: ["draft", "paid", "shipped"];
  initial: "draft";
  transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; to: "shipped"; permission: "orders.ship" }];
}>;

// A field's type is the union of its config's states.
export abstract class Order {
  status: OrderStatus;
}
const paid: OrderStatus = "paid";
// @ts-expect-error a state the config does not list
const lost: OrderStatus = "lost";

// A config read with typeof from a constant declared as const.
const shipmentFlow = {
  states: ["pending", "sent"],
  transitions: [{ from: "pending", to: "sent" }],
  outcomes: { sent: "success" },
} as const;
const sent: Workflow<typeof shipmentFlow> = "sent";

// The config holds Workflow's shape: states is a list, and a transition
// names where it goes.
// @ts-expect-error states is a list of states
type OneState = Workflow<{ states: "draft"; transitions: [] }>;
// @ts-expect-error a transition names its to
type NoTo = Workflow<{ states: ["draft"]; transitions: [{ from: "draft" }] }>;
// @ts-expect-error an outcome is success, failure or neutral
type BadOutcome = Workflow<{ states: ["draft"]; transitions: []; outcomes: { draft: "lost" } }>;

export const checked: readonly unknown[] = [paid, lost, sent];
export type Refused = OneState | NoTo | BadOutcome;
