import { Identity } from "superscalar";
import { AutoGenerate, Reactions, Relation, Workflow, key } from "@superschematic/db";
import { projectFlow, taskFlow } from "./flows";

// A project (D103): its status is a Workflow, which its tasks move. It is
// done once every task is done with success, and failed once one fails. The
// rules hear Task's writes through task.project, whoever makes them.
export abstract class Project implements Reactions<Project, {
  rules: [
    { when: { allTerminal: { schema: "Task"; link: "project"; outcomes: ["success"] } }; then: { transition: "done" } },
    { when: { anyTerminal: { schema: "Task"; link: "project"; outcomes: ["failure"] } }; then: { transition: "failed" } }
  ];
}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
  status: Workflow<typeof projectFlow>;
}

// A task of a project (D103): a task that starts moves its project to
// active, and one marked urgent starts.
export abstract class Task implements Reactions<Task, {
  rules: [
    { when: { enters: "doing" }; then: { link: "project"; transition: "active" } },
    { when: { changes: { field: "priority"; to: "urgent" } }; then: { transition: "doing" } }
  ];
}> {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  priority?: string;
  project: Relation<Project>;
  status: Workflow<typeof taskFlow>;
}
