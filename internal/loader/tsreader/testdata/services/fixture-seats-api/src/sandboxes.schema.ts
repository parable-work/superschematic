import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Sandbox } from "@schemas/fixture-seats-db";

// A sandbox's status and observation operations (D104), for any signed-in
// caller: the core decides how an observation moves the sandbox.
@behaviorOperations(Sandbox)
@auth
export abstract class SandboxActions {}

// Runs Sandbox's stale deadlines as they fall due (D104): a sandbox no
// observation has reached for five minutes moves to unreachable. The
// worker fixture-seats-api-sandbox-deadlines.
@behaviorWorker(Sandbox)
export abstract class SandboxDeadlines {}
