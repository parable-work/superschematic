import { Generic, Identity } from "superscalar";
import { AutoGenerate, Dependencies, HasMany, Relation, Rollups, Workflow, key } from "@superschematic/db";

// A batch of steps (D104's step 4): running until every step is done, which
// its rollup done gates, or failed. It counts its steps, counts them by
// status and sums their estimates, each computed at a read of the batch
// from the steps that point at it, the rows its steps field lists.
export abstract class Batch
  implements
    Rollups<Batch, {
      rollups: {
        done: { schema: "Step"; link: "batch"; function: "all"; outcomes: ["success"]; gatedStates: ["done"] };
        steps: { schema: "Step"; link: "batch"; function: "count" };
        byStatus: { schema: "Step"; link: "batch"; function: "countBy"; field: "status" };
        estimate: { schema: "Step"; link: "batch"; function: "sum"; field: "estimate" };
        anyFailed: { schema: "Step"; link: "batch"; function: "any"; outcomes: ["failure"] };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  status: Workflow<{
    states: ["running", "done", "failed"];
    transitions: [{ from: "running"; to: "done" }, { from: "running"; to: "failed" }];
    outcomes: { failed: "failure" };
  }>;
  steps: HasMany<Step>;
}

// One step of a batch (D104's step 4): blocked by the steps it comes after,
// which hold up its start and its finish until each is done, a step that
// failed blocking until it is removed.
export abstract class Step
  implements
    Dependencies<Step, { gatedStates: ["doing", "done"] }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  estimate?: Generic.Int64;
  batch: Relation<Batch>;
  status: Workflow<{
    states: ["todo", "doing", "done", "failed"];
    transitions: [
      { from: "todo"; to: "doing" },
      { from: "doing"; to: "done" },
      { from: "doing"; to: "failed" },
      { from: "todo"; to: "failed" }
    ];
    outcomes: { failed: "failure" };
  }>;
}
