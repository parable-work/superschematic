import { AutoGenerate, Budget, Constants, Dependencies, Lease, Relation, Retries, Workflow, key } from "@superschematic/db";
import { Nullable, Validate } from "@superschematic/schema";
import { Generic, Identity } from "superscalar";

// A job a worker claims, holds under a lease while it runs, and finishes, on development mode (D117). A claim is a read of the queued jobs of a topic, most urgent first, and Lease's acquire on the first that no blocker holds. A job whose lease ends without it finishing goes back in the queue; a failed attempt is retried per failure class; each claim reserves CPU seconds from the job's budget. A job a batch stamps is one of its steps, and does not start until the steps before it are done.
export abstract class Job implements Lease<Job, { escalate: { from: ["running"]; transition: "failed" }; maxExpiries: 3; onExpiry: { from: ["running"]; transition: "queued" }; overridePermission: "jobs.override"; ttlMs: 30000 }>, Budget<Job, { meters: { cpuSeconds: { limit: 600; reserve: 60 } } }>, Retries<Job, { classes: { cancelled: "terminal"; invalid: "terminal"; transient: { attempts: 2; hint: "Try again; the source may be back." } }; exhaustedState: "failed"; totalAttempts: 3 }>, Dependencies<Job, { gatedStates: ["running"] }>, Constants<Job, { fields: ["step", "topic"] }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  // What the job does.
  title: Validate<string, { maxLength: 200 }>;

  // The kind of work, which a worker claims by.
  topic: Nullable<string>;

  // Higher runs first.
  priority: Nullable<Generic.Int64>;

  // The step of its batch the job is, for a job a batch stamped.
  step: Nullable<string>;

  // The batch that stamped it, for a job a batch stamped.
  batch: Nullable<Relation<Batch>>;

  status: Workflow<{ outcomes: { failed: "failure" }; states: ["queued", "running", "done", "failed"]; transitions: [{ from: "queued"; to: "running" }, { from: "running"; to: "queued" }, { from: "running"; to: "done" }, { from: "running"; to: "failed" }, { from: "queued"; to: "failed" }] }>;
}
