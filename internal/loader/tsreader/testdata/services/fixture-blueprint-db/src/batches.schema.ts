import { Identity } from "superscalar";
import { AutoGenerate, Blueprint, Constants, Dependencies, HasMany, Relation, Workflow, key } from "@superschematic/db";

// A batch of jobs (D149): its create stamps a job per step of its template,
// each blocked by the jobs of the steps it comes after, and audit only for
// a batch whose flags include audit, its report then waiting on index.
// Every job copies the batch's topic. The batch holds no state of the
// stamp: a read lists its children, the jobs that point at it.
export abstract class Batch
  implements
    Blueprint<Batch, {
      schema: "Job";
      parentLink: "batch";
      keyField: "step";
      steps: {
        fetch: { data: { title: "Fetch the sources" } };
        index: { after: ["fetch"]; data: { title: "Index what was fetched" } };
        audit: { after: ["index"]; when: { field: "flags"; includes: "audit" }; data: { title: "Audit the index" } };
        report: { after: ["audit"]; data: { title: "Report what was indexed" } };
      };
      copyFields: ["topic"];
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  topic?: string;
  flags?: string[];
  status: Workflow<{
    states: ["running", "done", "failed"];
    transitions: [{ from: "running"; to: "done" }, { from: "running"; to: "failed" }];
    outcomes: { failed: "failure" };
  }>;
  jobs: HasMany<Job>;
}

// One job of a batch (D149): the step it was stamped as and its topic stay
// as the stamp set them, and it waits to run, and to finish, until the
// jobs of the steps before it are done.
export abstract class Job
  implements
    Constants<Job, { fields: ["step", "topic"] }>,
    Dependencies<Job, { gatedStates: ["running", "done"] }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  step?: string;
  topic?: string;
  batch: Relation<Batch>;
  status: Workflow<{
    states: ["queued", "running", "done", "failed"];
    transitions: [
      { from: "queued"; to: "running" },
      { from: "running"; to: "done" },
      { from: "running"; to: "failed" },
      { from: "queued"; to: "failed" }
    ];
    outcomes: { failed: "failure" };
  }>;
}
