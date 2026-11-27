import { auth, behaviorOperations } from "@superschematic/api";
import { Task } from "@schemas/fixture-audience-behaviors-db";

// A task's status and its conversation for any signed-in caller (D103):
// each operation is a route of its own, which keeps to the rows the
// caller may reach (D156, D181).
@behaviorOperations(Task)
@auth
export abstract class TaskActions {}
