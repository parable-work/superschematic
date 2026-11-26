import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Filing } from "@schemas/fixture-recurrence-db";

// A filing's recurrence and its occurrences (D152), for any signed-in
// caller: the core decides who may complete one (filings.work) and who may
// set, skip and move them (filings.manage).
@behaviorOperations(Filing)
@auth
export abstract class FilingActions {}

// Fires each filing's open, remind, overdue and plan deadlines as they fall
// due, and plans again on a write of its holiday calendar or its contract
// that its change queue carries. The worker
// fixture-recurrence-api-filing-deadlines.
@behaviorWorker(Filing, { permissions: ["filings.work", "filings.manage"] })
export abstract class FilingDeadlines {}
