import { Generic, Identity } from "superscalar";
import { AutoGenerate, Lease, Retries, Workflow, key } from "@superschematic/db";

// A job a worker takes (D104). The worker leases it with a fencing token and
// presents the token with every write while it holds it, so a process whose
// lease lapsed cannot write after another took the job. A lease the worker
// stops renewing expires when its deadline falls due and moves the job back
// to queued; the third expiry fails it. A timeout waits a minute before the
// job runs again, twice as long after each next one up to ten minutes, and
// then moves it from waiting back to queued; a build failure is retried once
// per revision; a job that has failed for half an hour gives up; and at most
// four attempts start in an hour. Its revision is not named version, which a
// table that composes behaviors keeps its own version in (_version).
export abstract class Job
  implements
    Lease<Job, {
      onExpiry: { transition: "queued"; from: ["running"] };
      maxExpiries: 3;
      escalate: { transition: "failed"; from: ["running"] };
      requireToken: true;
      overridePermission: "jobs.override";
    }>,
    Retries<Job, {
      classes: {
        timeout: { attempts: 5; backoff: { firstMs: 60000; maxMs: 600000 } };
        build: { attempts: 1 };
        rejected: "terminal";
      };
      totalAttempts: 6;
      exhaustedState: "failed";
      retryInto: ["running"];
      then: { transition: "queued"; from: ["waiting"] };
      giveUpAfterMs: 1800000;
      window: { attempts: 4; ms: 3600000 };
      keyField: "revision";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  revision: Generic.Int64;
  status: Workflow<{
    states: ["queued", "running", "waiting", "done", "failed"];
    transitions: [
      { from: "queued"; to: "running" },
      { from: "running"; to: "waiting" },
      { from: "waiting"; to: "queued" },
      { from: "running"; to: "queued" },
      { from: "running"; to: "done" },
      { from: "queued"; to: "failed" },
      { from: "running"; to: "failed" },
      { from: "waiting"; to: "failed" },
      { from: "failed"; to: "queued" }
    ];
  }>;
}
