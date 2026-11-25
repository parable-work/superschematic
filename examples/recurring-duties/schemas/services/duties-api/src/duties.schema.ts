import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Duty } from "@schemas/duties-db";

// A duty's recurrence and its occurrences, for any signed-in caller: the
// core decides who may complete one (duties.work) and who may set, skip
// and move them (duties.manage).
@behaviorOperations(Duty)
@auth
export abstract class DutyActions {}

// Fires each duty's open, remind, overdue and plan deadlines as they fall
// due, and plans again on a write of its holiday calendar or agreement.
@behaviorWorker(Duty, { permissions: ["duties.work", "duties.manage"] })
export abstract class DutyDeadlines {}
