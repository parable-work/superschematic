import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Recurrence, Relation, calendarSource, key } from "@superschematic/db";

// A day a holiday calendar closes (D152). The app fills these rows; core
// ships no holidays. A filing's due date moves off the days of the calendar
// its holidayCalendar names. A row with a weekday and no date closes that
// day every week, and a calendar with such rows takes them as its weekend
// in place of Saturday and Sunday (#729).
export abstract class Holiday {
  @key
  id: AutoGenerate<Identity.UUID>;
  calendar: string;
  date: Nullable<Temporal.Date>;
  weekday: Nullable<string>;
  name: string;
}

// An agreement with an end date, which a filing's relative recurrence
// counts back from. Its end is on the calendar.
@calendarSource<Contract>({ field: "endsOn", title: "title", kind: "renewal" })
export abstract class Contract {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  endsOn: Temporal.Date;
}

// Something that must be done again and again (D152): on a calendar rule,
// a while after it was last done, or a while before its contract ends. Each
// occurrence opens two weeks before it is due, reminds three days before,
// and is missed a day after. A filing's due dates move to the next business
// day of its holiday calendar. Each occurrence is on the calendar.
@calendarSource<Filing>({ occurrences: true, title: "title", kind: "deadline", color: "filing" })
export abstract class Filing
  implements
    Recurrence<Filing, {
      anchors: ["calendar", "afterDone", "relative"];
      timeZone: "Europe/London";
      horizonDays: 400;
      adjust: "after";
      holidays: { table: "Holiday"; calendarField: "calendar"; dateField: "date"; field: "holidayCalendar"; weekdayField: "weekday" };
      relative: { via: "contract"; type: "Contract"; field: "endsOn" };
      opensBefore: "P14D";
      remindBefore: "P3D";
      overdueAfter: "P1D";
      permission: "filings.work";
      managePermission: "filings.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  holidayCalendar: Nullable<string>;
  contract: Nullable<Relation<Contract>>;
}
