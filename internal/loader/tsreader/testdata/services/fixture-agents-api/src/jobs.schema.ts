import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Job } from "@schemas/fixture-agents-db";

// A job's lease, retries and status operations (D104), for any signed-in
// caller: the core decides who holds a job, whose writes its token fences
// and when it may run again.
@behaviorOperations(Job)
@auth
export abstract class JobActions {}

// Runs Job's deadlines as they fall due (D103, D104): a lease's expiry and
// the end of a backoff. The worker fixture-agents-api-job-deadlines.
@behaviorWorker(Job)
export abstract class JobDeadlines {}
