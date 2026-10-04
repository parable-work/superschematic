import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "doing", "done", "failed"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "doing", to: "done" },
    { from: "doing", to: "failed" },
  ],
  outcomes: { failed: "failure" },
})
@behavior("Links", { links: { parent: { schema: "projects" } } })
@behavior("Reactions", {
  rules: [
    { when: { enters: "doing" }, then: { link: "parent", transition: "doing" } },
    { when: { allTerminal: { schema: "projects", link: "parent", outcomes: ["success"] } }, then: { transition: "done" } },
    { when: { anyTerminal: { schema: "projects", link: "parent", outcomes: ["failure"] } }, then: { transition: "failed" } },
  ],
})
export abstract class Project {
  title: Validate<string, { maxLength: 200 }>;
}
