import { auth, behaviorOperations } from "@superschematic/api";
import { Batch, Job } from "@schemas/fixture-blueprint-db";

// A batch's Workflow, for any signed-in caller (D149). Blueprint declares
// no operation: a batch's create stamps its jobs.
@behaviorOperations(Batch)
@auth
export abstract class BatchActions {}

// A job's Workflow and its blockers, for any signed-in caller: a move to
// running waits for the jobs of the steps before it (D149).
@behaviorOperations(Job)
@auth
export abstract class JobActions {}
