// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: @behaviorWorker takes a class, the table, and an optional options
// object, and goes on a class with no members (D103).
import { behaviorWorker } from "@superschematic/api";
import type { BehaviorWorkerOptions } from "@superschematic/api";

declare abstract class Ticket {
  id: string;
}

@behaviorWorker(Ticket)
export abstract class TicketTimers {}

@behaviorWorker(Ticket, { concurrency: 2, grace: "30s", permissions: ["tickets.escalate"] })
export abstract class BusyTicketTimers {}

const options: BehaviorWorkerOptions = { concurrency: 1 };
@behaviorWorker(Ticket, options)
export abstract class OptionTicketTimers {}

// @ts-expect-error the table is a class, not its name
@behaviorWorker("Ticket")
export abstract class NamedByString {}

// @ts-expect-error a behavior worker runs the table's queues, which the build adds
@behaviorWorker(Ticket, { queue: "TicketDeadline" })
export abstract class NamesAQueue {}

// @ts-expect-error permissions is a list of strings
@behaviorWorker(Ticket, { permissions: "tickets.escalate" })
export abstract class OnePermission {}

export class Methods {
  // @ts-expect-error a class decorator, not a method's
  @behaviorWorker(Ticket)
  run(): void {}
}
