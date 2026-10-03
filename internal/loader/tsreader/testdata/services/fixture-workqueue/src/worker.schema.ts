import { behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["idle", "busy", "missing", "stopped"],
  transitions: [
    { from: "idle", to: "busy" },
    { from: "busy", to: "idle" },
    { from: "idle", to: "missing" },
    { from: "busy", to: "missing" },
    { from: "missing", to: "idle" },
    { from: "idle", to: "stopped" },
    { from: "busy", to: "stopped" },
    { from: "missing", to: "stopped" },
  ],
})
@behavior("Presence", {
  ttlMs: 30000,
  principalField: "subject",
  onMissed: { transition: "missing", from: ["idle", "busy"] },
  onBeat: { transition: "idle", from: ["missing"] },
  releaseLeases: ["jobs"],
  sweepMs: 5000,
})
export abstract class Worker {
  subject: string;
  name?: string;
}
