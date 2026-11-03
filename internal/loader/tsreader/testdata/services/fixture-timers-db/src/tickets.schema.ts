import { Identity, Temporal } from "superscalar";
import { AutoGenerate, Timers, Workflow, key } from "@superschematic/db";
import { invoicePayment, invoiceTimers } from "./flows";

// A support ticket (D103): its status is a Workflow, and Timers holds it to
// its SLA. The first answer is due 1.5 seconds after the create, and a
// ticket still open then escalates; one left waiting on its customer an
// hour closes; and a reminder falls due a day before a visit.
export abstract class Ticket implements Timers<Ticket, {
  deadlines: {
    respond: { start: { create: true }; afterMs: 1500; then: { transition: "escalated" }; until: { leaves: "open" } };
    idle: { start: { enters: "waiting" }; afterMs: 3600000; then: { transition: "closed" } };
    remind: { start: { field: "visitAt" }; afterMs: -86400000 };
  };
  permission: "tickets.timers";
}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  visitAt?: Temporal.DateTime;
  status: Workflow<{
    states: ["open", "waiting", "escalated", "closed"];
    transitions: [
      { from: "open"; to: "waiting" },
      { from: "waiting"; to: "open" },
      { from: "open"; to: "escalated" },
      { from: "open"; to: "closed" },
      { from: "waiting"; to: "closed" },
      { from: "escalated"; to: "closed" }
    ];
  }>;
}

// An invoice with two Workflows, whose Timers names the one it reads:
// an issued invoice unpaid for thirty days is overdue.
export abstract class Invoice implements Timers<Invoice, typeof invoiceTimers> {
  @key
  id: AutoGenerate<Identity.UUID>;
  number: string;
  status: Workflow<{ states: ["draft", "issued", "void"]; transitions: [{ from: "draft"; to: "issued" }, { from: "issued"; to: "void" }] }>;
  payment: Workflow<typeof invoicePayment>;
}
