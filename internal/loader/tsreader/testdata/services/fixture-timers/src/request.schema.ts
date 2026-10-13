import { Temporal } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["open", "waiting", "escalated", "resolved"],
  transitions: [
    { from: "open", to: "waiting" },
    { from: "waiting", to: "open" },
    { from: "open", to: "escalated" },
    { from: "escalated", to: "open" },
    { from: "open", to: "resolved" },
    { from: "waiting", to: "resolved" },
    { from: "escalated", to: "resolved" },
  ],
})
@behavior("Timers", {
  deadlines: {
    respond: { start: { enters: "open" }, afterMs: 14400000, then: { transition: "escalated" }, until: { leaves: "open" } },
    lapse: { start: { enters: "waiting" }, afterMs: 604800000, then: { transition: "resolved" }, until: { leaves: "waiting" } },
    remind: { start: { field: "visitAt" }, afterMs: -86400000, until: { enters: ["resolved"] } },
  },
  sweepMs: 60000,
  permission: "requests.timers",
})
export abstract class Request {
  title: Validate<string, { maxLength: 200 }>;
  visitAt?: Temporal.DateTime;
}
