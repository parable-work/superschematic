import { behaviorOperations, behaviorWorker, requirePermission } from "@superschematic/api";
import { Ticket } from "@schemas/fixture-timers-db";

// A ticket's status and timers operations (D103), for callers who hold
// tickets.write; a reset or a snooze needs tickets.timers too, which the
// core checks.
@behaviorOperations(Ticket)
@requirePermission(["tickets.write"])
export abstract class TicketActions {}

// Runs Ticket's deadlines as they fall due (D103): the worker
// fixture-timers-api-ticket-timers.
@behaviorWorker(Ticket, { concurrency: 2 })
export abstract class TicketTimers {}
