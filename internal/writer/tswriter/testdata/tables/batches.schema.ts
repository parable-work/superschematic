import { AutoGenerate, Blueprint, Reactions, Rollups, Workflow, key } from "@superschematic/db";
import { Nullable } from "@superschematic/schema";
import { Identity } from "superscalar";

// A batch of jobs, on development mode (D117): its create stamps a job per step, each blocked by the steps before it (Blueprint, D149). It counts its jobs by status, and is done when every job succeeded, or failed when one failed.
export abstract class Batch implements Blueprint<Batch, { copyFields: ["topic"]; keyField: "step"; parentLink: "batch"; schema: "Job"; steps: { fetch: { data: { title: "Fetch the sources" } }; index: { after: ["fetch"]; data: { title: "Index what was fetched" } }; report: { after: ["index"]; data: { title: "Report what was indexed" } } } }>, Rollups<Batch, { rollups: { steps: { field: "Workflow.status"; function: "countBy"; link: "batch"; schema: "Job" } } }>, Reactions<Batch, { rules: [{ then: { transition: "done" }; when: { allTerminal: { link: "batch"; outcomes: ["success"]; schema: "Job" } } }, { then: { transition: "failed" }; when: { anyTerminal: { link: "batch"; outcomes: ["failure"]; schema: "Job" } } }] }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  // What the batch is for.
  title: string;

  // Copied to every step, so the workers of the topic claim them.
  topic: Nullable<string>;

  status: Workflow<{ outcomes: { failed: "failure" }; states: ["running", "done", "failed"]; transitions: [{ from: "running"; to: "done" }, { from: "running"; to: "failed" }] }>;
}
