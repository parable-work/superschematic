import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["active", "done", "dropped"],
  transitions: [
    { from: "active", to: "done" },
    { from: "active", to: "dropped" },
  ],
})
@behavior("Rollups", {
  rollups: {
    tasks: { schema: "tasks", link: "project", function: "count" },
    tasksByStatus: { schema: "tasks", link: "project", function: "countBy", field: "Workflow.status" },
    tasksFinished: { schema: "tasks", link: "project", function: "all", gatedStates: ["done"] },
  },
})
export abstract class Project {
  title: Validate<string, { maxLength: 200 }>;
}
