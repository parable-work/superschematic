// Type-level checks of Timers<T, C> (D103). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Timers, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself.
export abstract class Ticket implements Timers<Ticket, {
  deadlines: {
    respond: { start: { create: true }; afterMs: 1500; then: { transition: "escalated" }; until: { leaves: "open" } };
    remind: { start: { field: "visitAt" }; afterMs: -86400000 };
  };
  permission: "tickets.timers";
}> {
  id: string;
  visitAt?: Date;
  status: Workflow<{ states: ["open", "escalated"]; transitions: [{ from: "open"; to: "escalated" }] }>;
}

// A config read with typeof from a constant declared as const, which names
// the Workflow it reads by its field.
const invoiceTimers = {
  status: "payment",
  deadlines: { overdue: { start: { create: true }, afterMs: 2592000000, then: { transition: "overdue" } } },
} as const;
export abstract class Invoice implements Timers<Invoice, typeof invoiceTimers> {
  id: string;
  payment: Workflow<{ states: ["unpaid", "overdue"]; transitions: [{ from: "unpaid"; to: "overdue" }] }>;
}

// A field start and the status name a field of the table.
// @ts-expect-error a field start names a field of the table
type NoSuchField = Timers<Ticket, { deadlines: { remind: { start: { field: "visitedAt" } } } }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Timers<Ticket, { deadlines: { respond: { start: { create: true } } }; status: "phase" }>;
// @ts-expect-error a deadline has a start
type NoStart = Timers<Ticket, { deadlines: { respond: { afterMs: 1500 } } }>;
// @ts-expect-error a then names its transition
type NoTransition = Timers<Ticket, { deadlines: { respond: { start: { create: true }; then: { from: ["open"] } } } }>;

export type Refused = NoSuchField | NoSuchStatus | NoStart | NoTransition;
