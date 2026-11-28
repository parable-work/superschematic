import { Contact, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import {
  AutoGenerate,
  Recurrence,
  Relation,
  User,
  UserRole,
  accessGrants,
  audience,
  calendarSource,
  key,
  unique
} from "@superschematic/db";

// A person who signs in.
export abstract class Account implements User<{ login: "email" }> {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  email: Contact.Email;
}

// A named set of permissions. A calendar feed's role bounds what the feed
// may read.
export abstract class Role implements UserRole {
  @key
  id: AutoGenerate<Identity.UUID>;

  @unique
  name: Identity.Slug;

  permissions: string[];

  reaches: Nullable<string[]>;
}

// The grants a feed's token is one of (D156, D152).
@accessGrants()
export abstract class Grant {}

// A day a holiday calendar closes. The app fills these rows: which days
// close is data, and core ships none.
export abstract class Holiday {
  @key
  id: AutoGenerate<Identity.UUID>;

  calendar: string;

  date: Temporal.Date;

  name: string;
}

// An agreement that runs to a date, on the calendar on its last day.
@calendarSource<Agreement>({ field: "endsOn", title: "name", kind: "renewal" })
export abstract class Agreement {
  @key
  id: AutoGenerate<Identity.UUID>;

  name: string;

  endsOn: Temporal.Date;
}

// A duty its owner does again and again: on a calendar rule, a while after
// it was last done, or a while before its agreement ends. Each occurrence
// opens a week before it is due and reminds two days before; a due date
// moves to the next business day of the duty's holiday calendar. Its owner
// alone sees a private duty, on the calendar and in its feed, and a caller
// without duties.work sees none there.
@audience<Duty>({ owner: "owner" })
@calendarSource<Duty>({ occurrences: true, title: "title", kind: "duty", color: "duty", owner: "owner", permission: "duties.work" })
export abstract class Duty
  implements
    Recurrence<Duty, {
      anchors: ["calendar", "afterDone", "relative"];
      timeZone: "Europe/Paris";
      horizonDays: 400;
      adjust: "after";
      holidays: { table: "Holiday"; calendarField: "calendar"; dateField: "date"; field: "holidayCalendar" };
      relative: { via: "agreement"; type: "Agreement"; field: "endsOn" };
      opensBefore: "P7D";
      remindBefore: "P2D";
      overdueAfter: "P1D";
      permission: "duties.work";
      managePermission: "duties.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  owner: Identity.UUID;

  holidayCalendar: Nullable<string>;

  agreement: Nullable<Relation<Agreement>>;
}
