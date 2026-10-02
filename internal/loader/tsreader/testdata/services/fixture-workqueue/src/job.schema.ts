import { Generic } from "superscalar";
import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["queued", "running", "done", "failed"],
  transitions: [
    { from: "queued", to: "running" },
    { from: "running", to: "queued" },
    { from: "running", to: "done" },
    { from: "running", to: "failed" },
    { from: "queued", to: "failed" },
  ],
})
@behavior("Lease", {
  ttlMs: 30000,
  maxHoldField: "timeLimitMs",
  onExpiry: { transition: "queued", from: ["running"] },
  maxExpiries: 3,
  escalate: { transition: "failed", from: ["running"] },
  overridePermission: "jobs.override",
})
@behavior("Assignment", { permission: "jobs.assign" })
@behavior("Queue", { claim: { from: ["queued"], to: "running" }, priorityField: "priority", match: ["topic"] })
export abstract class Job {
  title: Validate<string, { maxLength: 200 }>;
  topic?: string;
  priority?: Generic.Int64;
  timeLimitMs?: Generic.Int64;
}
