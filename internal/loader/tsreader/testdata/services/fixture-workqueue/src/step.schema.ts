import { behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["queued", "running", "done"],
  transitions: [
    { from: "queued", to: "running" },
    { from: "running", to: "done" },
  ],
})
@behavior("Constants", { fields: ["step", "topic"] })
@behavior("Dependencies")
export abstract class Step {
  step: string;
  title?: string;
  topic?: string;
}
