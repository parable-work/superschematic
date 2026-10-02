import { Validate, behavior } from "@superschematic/schema";

@behavior("Workflow", {
  states: ["todo", "doing", "done"],
  transitions: [
    { from: "todo", to: "doing" },
    { from: "doing", to: "done" },
  ],
})
@behavior("Links", { links: { parent: { schema: "projects" } } })
@behavior("Reactions", {
  rules: [
    { when: { enters: "doing" }, then: { link: "parent", transition: "doing" } },
    { when: { allTerminal: { schema: "projects", link: "parent" } }, then: { transition: "done" } },
  ],
})
export abstract class Project {
  title: Validate<string, { maxLength: 200 }>;
}
