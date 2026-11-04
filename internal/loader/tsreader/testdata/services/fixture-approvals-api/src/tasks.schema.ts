import { Identity } from "superscalar";
import { HttpMethod, auth, behaviorOperations, requirePermission, rest, source } from "@superschematic/api";
import { Task } from "@schemas/fixture-approvals-db";

// A task's status and approvals operations (D104), for any signed-in
// caller: the core decides who may move a task, sign it off and change what
// its state freezes.
@behaviorOperations(Task)
@auth
export abstract class TaskActions {}

// Who reviews a task and who owns it, as reassignTask takes them: a view of
// the table that carries reviewer, which StateRules names in by, so the build
// warns that a caller who may reassign a task may then edit its plan while
// it is working; and owner, which the Workflow names in by and Constants
// keeps as its create gave it, so the build does not warn of it.
@source(Task)
export abstract class TaskAssignment {
  reviewer?: Identity.UUID;
  owner?: Identity.UUID;
}

// Administration of tasks, for callers who hold tasks.admin.
export class TaskAdmin {
  @requirePermission(["tasks.admin"])
  @rest(HttpMethod.PATCH, "tasks/{id}/assignment")
  reassignTask(id: Identity.UUID, input: TaskAssignment): Task {
    throw new Error("schema declaration only");
  }
}
