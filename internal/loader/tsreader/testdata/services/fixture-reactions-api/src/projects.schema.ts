import { behaviorOperations, behaviorWorker, requirePermission } from "@superschematic/api";
import { Project, Task } from "@schemas/fixture-reactions-db";

// A project's status operations (D103), for callers who hold projects.write.
@behaviorOperations(Project)
@requirePermission(["projects.write"])
export abstract class ProjectActions {}

// A task's status operations, for callers who hold tasks.write.
@behaviorOperations(Task)
@requirePermission(["tasks.write"])
export abstract class TaskActions {}

// Runs Project's change queue (D103): the writes of its rows, which its
// Reactions hear.
@behaviorWorker(Project)
export abstract class ProjectReactions {}

// Runs Task's change queue: its own rules, and Project's allTerminal and
// anyTerminal over task.project, which move the project as this worker.
@behaviorWorker(Task, { concurrency: 2 })
export abstract class TaskReactions {}
