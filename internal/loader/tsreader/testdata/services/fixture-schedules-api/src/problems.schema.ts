import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Problem } from "@schemas/fixture-schedules-db";

// A problem's wake-ups and status operations (D104), for any signed-in
// caller: the core decides who may set a wake-up (problems.schedule) and when
// it runs.
@behaviorOperations(Problem)
@auth
export abstract class ProblemActions {}

// Runs Problem's wake-ups as they fall due, the writes of its rows its
// Schedules hears, and the events its wake-ups emit, each handed to the
// project's Workers method problemWakeUpsSchedulesRun (D104). The worker
// fixture-schedules-api-problem-wake-ups.
@behaviorWorker(Problem, { permissions: ["problems.schedule"] })
export abstract class ProblemWakeUps {}
