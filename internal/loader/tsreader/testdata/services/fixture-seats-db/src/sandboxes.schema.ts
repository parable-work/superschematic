import { Identity } from "superscalar";
import { AutoGenerate, Reconcile, Workflow, key } from "@superschematic/db";

// A sandbox a provider runs (D104). Its status follows the state the
// provider reports, through observe, each step a transition its Workflow
// allows as the observer, so a stop needs sandboxes.stop of the observer
// too; when no report has reached it for five minutes its status moves to
// unreachable, and any report brings it back.
export abstract class Sandbox {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  status: Workflow<{
    states: ["pending", "starting", "running", "stopping", "stopped", "failed", "unreachable"];
    transitions: [
      { from: "pending"; to: "starting" },
      { from: "starting"; to: "running" },
      { from: "running"; to: "stopping"; permission: "sandboxes.stop" },
      { from: "stopping"; to: "stopped" },
      { from: "stopped"; to: "starting" },
      { from: "pending"; to: "failed" },
      { from: "starting"; to: "failed" },
      { from: "running"; to: "failed" },
      { from: "stopping"; to: "failed" },
      { from: "pending"; to: "unreachable" },
      { from: "starting"; to: "unreachable" },
      { from: "running"; to: "unreachable" },
      { from: "stopping"; to: "unreachable" },
      { from: "stopped"; to: "unreachable" },
      { from: "unreachable"; to: "pending" },
      { from: "unreachable"; to: "starting" },
      { from: "unreachable"; to: "running" },
      { from: "unreachable"; to: "stopping" },
      { from: "unreachable"; to: "stopped" },
      { from: "unreachable"; to: "failed" }
    ];
  }>;
  observed: Reconcile<{ status: "status"; unreachable: "unreachable"; staleAfterMs: 300000; observePermission: "sandboxes.observe" }>;
}
