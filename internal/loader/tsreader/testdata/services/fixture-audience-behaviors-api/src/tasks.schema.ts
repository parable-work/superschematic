import { auth, behaviorOperations } from "@superschematic/api";
import { Runner, Task } from "@schemas/fixture-audience-behaviors-db";

// A task's status and its conversation for any signed-in caller (D103):
// each operation is a route of its own, which keeps to the rows the
// caller may reach (D156, D181).
@behaviorOperations(Task)
@auth
export abstract class TaskActions {}

// A runner's pool for any signed-in caller (D104): pick, a read of the
// table as a whole at runner/pool/pick, answers no runner the caller does
// not see (D156, D181).
@behaviorOperations(Runner)
@auth
export abstract class RunnerActions {}
