import { Identity } from "superscalar";
import { Approvals, AutoGenerate, Constants, StateRules, Workflow, key } from "@superschematic/db";

// A task a person plans and its owner proves (D104). Its plan is approved
// before it is proved, and that approval survives later edits of the plan;
// its proof is approved against the version its reviewer read, and a change
// of the proof clears that approval. While it is planned only its creator
// edits the plan, and while it is working only its reviewer does; it enters
// proving only with a proof. Its owner stays who its create named, and
// urgent and attempts only go up, unless the caller holds tasks.admin.
export abstract class Task
  implements
    Constants<Task, {
      fields: ["owner"];
      oneWay: { urgent: "up"; attempts: "up" };
      permission: "tasks.admin";
    }>,
    StateRules<Task, {
      states: {
        planned: { frozen: ["plan"]; by: ["createdBy"] };
        working: { frozen: ["plan"]; by: ["reviewer"] };
        proving: { required: ["proof"] };
      };
    }>,
    Approvals<Task, {
      gates: {
        proving: { count: 1; permission: "tasks.review"; resetOn: [] };
        done: { count: 1; permission: "tasks.review"; resetOn: ["proof"]; pinned: true };
      };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  owner: Identity.UUID;
  reviewer?: Identity.UUID;
  plan?: string;
  proof?: string;
  urgent: boolean;
  attempts: number;
  createdBy: Identity.UUID;
  status: Workflow<{
    states: ["planned", "working", "proving", "done", "cancelled"];
    transitions: [
      { from: "planned"; to: "working"; by: ["owner"] },
      { from: "working"; to: "proving"; by: ["owner"] },
      { from: "proving"; to: "working" },
      { from: "proving"; to: "done" },
      { from: "planned"; to: "cancelled" },
      { from: "working"; to: "cancelled" }
    ];
  }>;
}
