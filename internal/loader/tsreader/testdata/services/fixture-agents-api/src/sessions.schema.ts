import { auth, behaviorOperations, behaviorWorker } from "@superschematic/api";
import { Session } from "@schemas/fixture-agents-db";

// A session's presence and status operations (D104), for any signed-in
// caller: the core decides who may beat a session, its device's principal.
@behaviorOperations(Session)
@auth
export abstract class SessionActions {}

// Runs each session's miss as it falls due (D104): the worker
// fixture-agents-api-session-presence. A miss expires the leases the device
// holds on jobs through Lease's expire, which needs Job's Lease
// overridePermission, so the worker holds it.
@behaviorWorker(Session, { permissions: ["jobs.override"] })
export abstract class SessionPresence {}
