import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Filing } from "@schemas/fixture-worker-refs-db";

// A filing's recurrence and its occurrences (D152), for any signed-in
// caller.
@behaviorOperations(Filing)
@auth
export abstract class FilingActions {}

// Fires each filing's deadlines and hands the events its Recurrence emits
// to the project's Workers, each with the database's repositories, its
// references and its attachments in the transaction that spends the
// event's claim (D104, D154).
@behaviorWorker(Filing)
export abstract class FilingDeadlines {}
