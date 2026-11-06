// Type-level checks of Schedules<T, C> (D104). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import { behavior } from "@superschematic/schema";
import type { ScheduleKind, Schedules, SchedulesOn, Workflow } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A record
// holds at most ten wake-ups, after a delay, at an interval or on a cron
// line in a time zone; a missed run is tried again five minutes on, and a
// record that is done or dropped finishes its wake-ups.
export abstract class Problem implements
  Schedules<Problem, {
    max: 10;
    minMs: 60000;
    maxMs: 2592000000;
    kinds: ["after", "every", "cron"];
    timeZone: "America/New_York";
    outcomes: ["told", "resumed", "held", "missed"];
    retry: { outcome: "missed"; afterMs: 300000 };
    endOn: { enters: ["done", "dropped"] };
    permission: "problems.schedule";
  }>
{
  id: string;
  title: string;
  status: Workflow<{
    states: ["open", "working", "done", "dropped"];
    transitions: [
      { from: "open"; to: "working" },
      { from: "working"; to: "done" },
      { from: "working"; to: "open" },
      { from: "open"; to: "dropped" },
      { from: "working"; to: "dropped" }
    ];
  }>;
}

// The fewest members: max, kinds and outcomes; status names a field of the
// table.
const kinds: readonly ScheduleKind[] = ["every"];
const fewest: SchedulesOn<Problem> = { max: 1, kinds, outcomes: ["told"] };
const named: SchedulesOn<Problem> = { max: 2, kinds: ["cron"], outcomes: ["told"], endOn: { enters: ["done"] }, status: "status" };

// A kind is after, every or cron; max is a number; outcomes are needed; a
// config names no member but its own; status names a field of the table.
// @ts-expect-error a kind is after, every or cron
type Weekly = Schedules<Problem, { max: 1; kinds: ["weekly"]; outcomes: ["told"] }>;
// @ts-expect-error max is a number
type TenMax = Schedules<Problem, { max: "ten"; kinds: ["every"]; outcomes: ["told"] }>;
// @ts-expect-error Schedules names the outcomes a run reports
type NoOutcomes = Schedules<Problem, { max: 1; kinds: ["every"] }>;
// @ts-expect-error a config names no member but max, minMs, maxMs, kinds, timeZone, outcomes, retry, endOn, status and permission
const unknownMember: SchedulesOn<Problem> = { max: 1, kinds: ["every"], outcomes: ["told"], every: 60000 };
// @ts-expect-error status names a field of the table
type NoSuchField = Schedules<Problem, { max: 1; kinds: ["every"]; outcomes: ["told"]; endOn: { enters: ["done"] }; status: "nope" }>;

// An engine type names no Schedules: @superschematic/schema's BehaviorConfigs
// has none, since no engine implements it.
// @ts-expect-error Schedules is a table's alone
@behavior("Schedules", { max: 1, kinds: ["every"], outcomes: ["told"] })
export abstract class EngineProblem {}

export const checked: readonly unknown[] = [fewest, named, unknownMember];
export type Refused = Weekly | TenMax | NoOutcomes | NoSuchField;
