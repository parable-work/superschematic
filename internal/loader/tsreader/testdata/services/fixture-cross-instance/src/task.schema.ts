import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "doing", "done", "dropped"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "doing", to: "done" },
    { from: "todo", to: "dropped" },
    { from: "doing", to: "dropped" },
  ],
})
@behavior("Dependencies", { schemas: ["tasks", "documents"], gatedStates: ["done"] })
@behavior("Links", {
  links: {
    spec: { schema: "documents", pinned: true },
    parent: { schema: "tasks", required: true },
    project: { schema: "projects" },
  },
})
export abstract class Task {
  title: Validate<string, { maxLength: 200 }>;
}
