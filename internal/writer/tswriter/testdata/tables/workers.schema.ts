import { Presence, Workflow, key } from "@superschematic/db";
import { Nullable } from "@superschematic/schema";

// A worker process, which beats while it runs, on development mode (D117). A worker that misses its deadline is missing, and the leases its principal holds on jobs are released, so its jobs go back in the queue.
export abstract class Worker implements Presence<Worker, { onBeat: { from: ["missing"]; transition: "active" }; onMissed: { from: ["active"]; transition: "missing" }; principalField: "subject"; releaseLeases: ["Job"]; ttlMs: 15000 }> {
  // The worker's subject, its key.
  @key
  id: Nullable<string>;

  // The principal the worker runs as.
  subject: string;

  status: Workflow<{ states: ["active", "missing"]; transitions: [{ from: "active"; to: "missing" }, { from: "missing"; to: "active" }] }>;
}
