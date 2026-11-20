import { auth, behaviorOperations } from "@superschematic/api";
import { Batch, Step } from "@schemas/fixture-steps-db";

// A batch's Workflow, for any signed-in caller: a move to done waits for
// every step to be done (D104's step 4).
@behaviorOperations(Batch)
@auth
export abstract class BatchActions {}

// A step's Workflow and its blockers, for any signed-in caller: addBlocker,
// removeBlocker and listBlockers; listDependents is the engine's alone, and
// the set leaves it out (D104's step 4).
@behaviorOperations(Step)
@auth
export abstract class StepActions {}
