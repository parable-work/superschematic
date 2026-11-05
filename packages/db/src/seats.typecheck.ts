// Type-level checks of Seats<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { Seats, SeatsOn, SeatsSeatOn, StateRules, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. Its
// accountable seat holds one person, whom the next to take it displaces;
// its responsible seat always seats the problem's agent; and a Workflow
// transition's and a StateRules state's by name a seat as seat:<name>.
export abstract class Problem implements
  Seats<Problem, {
    seats: {
      accountable: { max: 1; onFull: "displace"; take: "problems.own"; assign: "problems.assign" };
      responsible: { fromField: "agent"; take: "problems.work"; assign: "problems.assign" };
      handles: { max: 5; assign: "problems.release" };
    };
  }>,
  StateRules<Problem, { states: { review: { frozen: ["title"]; by: ["seat:accountable"] } } }>
{
  id: string;
  title: string;
  agent?: string;
  status: Workflow<{
    states: ["open", "working", "review", "done"];
    transitions: [
      { from: "open"; to: "working"; by: ["seat:responsible"] },
      { from: "review"; to: "done"; by: ["agent", "seat:accountable"] }
    ];
  }>;
}

// A seat's fromField names a field of the table.
const responsible: SeatsSeatOn<Problem> = { fromField: "agent", take: "problems.work" };
const problemSeats: SeatsOn<Problem> = { seats: { responsible, handles: { max: 5, onFull: "refuse" } } };

// fromField names a field of the table, onFull is refuse or displace, and a
// seat is an object of these members alone.
// @ts-expect-error fromField names a field of the table
type NoSuchField = Seats<Problem, { seats: { responsible: { fromField: "owner" } } }>;
// @ts-expect-error onFull is refuse or displace
type Evict = Seats<Problem, { seats: { handles: { max: 5; onFull: "evict" } } }>;
// @ts-expect-error Seats names its seats
type NoSeats = Seats<Problem, { max: 1 }>;
// @ts-expect-error max is a number
type ManyMax = Seats<Problem, { seats: { handles: { max: "five" } } }>;
// @ts-expect-error a seat names no member but max, onFull, take, assign and fromField
const unknownMember: SeatsSeatOn<Problem> = { max: 1, owner: "agent" };

// An engine type names no Seats: @superschematic/schema's BehaviorConfigs
// has none, since no engine implements it.
// @ts-expect-error Seats is a table's alone
@behavior("Seats", { seats: { accountable: { max: 1 } } })
export abstract class EngineProblem {}

export const checked: readonly unknown[] = [problemSeats, unknownMember];
export type Refused = NoSuchField | Evict | NoSeats | ManyMax;
