// Type-level checks of Recurrence<T, C> (D152). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Recurrence, RecurrenceOn } from "./index";

// The carrier imposes no members: a table implements it with its own
// fields alone, and its first type argument is the table itself. A filing
// recurs on a calendar rule, after it was last done, or before its
// contract ends; its due dates move off its calendar's holidays.
export abstract class Filing implements
  Recurrence<Filing, {
    anchors: ["calendar", "afterDone", "relative"];
    timeZone: "Europe/London";
    horizonDays: 400;
    adjust: "after";
    weekendDays: ["saturday", "sunday"];
    holidays: { table: "Holiday"; calendarField: "calendar"; dateField: "date"; field: "holidayCalendar" };
    relative: { via: "contract"; type: "Contract"; field: "endsOn" };
    opensBefore: "P14D";
    remindBefore: "P3D";
    overdueAfter: "P1D";
    permission: "filings.work";
    managePermission: "filings.manage";
  }>
{
  id: string;
  title: string;
  holidayCalendar: string | null;
  contract: string | null;
}

// A holidays.field that is not a field of the table is refused.
export abstract class Stray implements
  // @ts-expect-error calendar is not a field of Stray.
  Recurrence<Stray, { anchors: ["calendar"]; holidays: { table: "Holiday"; calendarField: "calendar"; dateField: "date"; field: "calendar" } }>
{
  id: string;
}

// An anchor the behavior does not know is refused.
// @ts-expect-error weekly is no anchor.
export const unknownAnchor: RecurrenceOn<Filing> = { anchors: ["weekly"] };
