// Type-level checks of Lease<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Lease, LeaseOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its
// expiry moves the job back to queued, the third escalates it to failed,
// and every write under the lease presents its token.
export abstract class Job implements Lease<Job, {
  onExpiry: { transition: "queued"; from: ["running"] };
  maxExpiries: 3;
  escalate: { transition: "failed"; from: ["running"] };
  requireToken: true;
  overridePermission: "jobs.override";
}> {
  id: string;
  holdMs?: number;
  status: Workflow<{ states: ["queued", "running", "failed"]; transitions: [{ from: "queued"; to: "running" }, { from: "running"; to: "queued" }, { from: "running"; to: "failed" }] }>;
  review: Workflow<{ states: ["pending", "done"]; transitions: [{ from: "pending"; to: "done" }] }>;
}

// maxHoldField and status name fields of the table.
const heldJob: LeaseOn<Job> = { ttlMs: 30000, maxHoldField: "holdMs", status: "status", exempt: ["Retries.recordAttempt"] };

// sweepMs is the engine's runner's, directOn waits for Revisions and
// Branches, and maxHoldField and status name the table's fields.
// @ts-expect-error a table's Lease takes no sweepMs
type SweepMs = Lease<Job, { ttlMs: 30000; sweepMs: 1000 }>;
// @ts-expect-error a table's Lease takes no directOn
type DirectOn = Lease<Job, { directOn: [{ revised: { link: "spec" }; name: "rebase" }] }>;
// @ts-expect-error maxHoldField names a field of the table
type NoSuchHoldField = Lease<Job, { maxHoldField: "limitMs" }>;
// @ts-expect-error status names a field of the table
type NoSuchStatus = Lease<Job, { status: "phase" }>;

// An engine type's @behavior("Lease", ...) takes @superschematic/schema's
// config, which has no status.
// @ts-expect-error status is a table's Lease's
@behavior("Lease", { status: "s" })
export abstract class EngineTask {}
@behavior("Lease", { ttlMs: 30000, sweepMs: 1000 })
export abstract class EngineJob {}

export const checked: readonly unknown[] = [heldJob];
export type Refused = SweepMs | DirectOn | NoSuchHoldField | NoSuchStatus;
