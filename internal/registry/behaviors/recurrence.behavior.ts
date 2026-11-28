// Recurrence's class (D103): the build derives recurrence.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface RecurrenceConfig {
  /**
   * The anchors a row's recurrence may take: calendar, an RRULE from a start;
   * afterDone, an interval after the last completion; relative, an offset
   * from the date relative names.
   */
  anchors: JsonSchema<("calendar" | "afterDone" | "relative")[], { minItems: 1; uniqueItems: true }>;
  /**
   * The IANA time zone a recurrence is read in when setRecurrence names none;
   * UTC when absent.
   */
  timeZone?: Named;
  /**
   * How many days ahead the occurrences are kept materialised; 400 when
   * absent.
   */
  horizonDays?: JsonSchema<Integer, { minimum: 1; maximum: 1100 }>;
  /**
   * How many days a finished occurrence (done, missed, skipped) is kept after
   * its due before a write prunes it; 1100 when absent.
   */
  historyDays?: JsonSchema<Integer, { minimum: 1; maximum: 36500 }>;
  /**
   * How a due date on a weekend day or a holiday moves when setRecurrence
   * names none: before, after, or none (it stays); none when absent.
   */
  adjust?: "none" | "before" | "after";
  /**
   * The days a due date moves off, by English name; saturday and sunday when
   * absent.
   */
  weekendDays?: JsonSchema<("monday" | "tuesday" | "wednesday" | "thursday" | "friday" | "saturday" | "sunday")[], { uniqueItems: true; maxItems: 6 }>;
  /**
   * The holiday table: a table of the app's rows, each a date of a holiday
   * calendar, and the field of this table that names a row's calendar.
   */
  holidays?: {
    /** The holiday table's type. */
    table: Named;
    /** The holiday table's field that names its calendar. */
    calendarField: Named;
    /**
     * The holiday table's date field: a Temporal.Date, or a string of a wall
     * date; nullable when weekdayField is given.
     */
    dateField: Named;
    /**
     * This table's field that names the calendar a row's due dates move off;
     * a row with none moves off weekend days alone.
     */
    field: Named;
    /**
     * The holiday table's string field that names a weekday (friday) on a
     * row whose date is null: that day closes every week in the row's
     * calendar. A calendar with one such row or more takes its weekday rows
     * as its weekend, in place of weekendDays (#729).
     */
    weekdayField?: Named;
  };
  /**
   * Where the relative anchor's date is: a to-one relation of this table and
   * a date or date-time field of the table it points at.
   */
  relative?: {
    /** This table's to-one relation field. */
    via: Named;
    /** The type the relation points at. */
    type: Named;
    /** That type's date or date-time field. */
    field: Named;
  };
  /**
   * How long before its due an occurrence opens, an ISO 8601 duration (P14D);
   * with none it opens when the occurrence before it falls due, the first
   * when it is planned.
   */
  opensBefore?: Temporal.Period;
  /**
   * How long before its due an occurrence reminds, an ISO 8601 duration
   * (P3D): the deadline remind.
   */
  remindBefore?: Temporal.Period;
  /**
   * How long after its due an open occurrence is missed, an ISO 8601 duration
   * (P1D); at its due when absent.
   */
  overdueAfter?: Temporal.Period;
  /**
   * The permission complete and listOccurrences need; any caller when absent.
   */
  permission?: Named;
  /**
   * The permission setRecurrence, skip and moveOne need; permission when
   * absent.
   */
  managePermission?: Named;
}

interface SetRecurrenceParams {
  /** calendar, afterDone or relative. */
  anchor: "calendar" | "afterDone" | "relative";
  /**
   * The RFC 5545 RRULE of a calendar recurrence:
   * FREQ=YEARLY;BYMONTH=10;BYMONTHDAY=31.
   */
  rule?: JsonSchema<string, { minLength: 1; maxLength: 512 }>;
  /**
   * A wall date or date-time: a calendar rule's start, or an afterDone
   * recurrence's first due when nothing is done and its time of day.
   */
  start?: JsonSchema<string, { minLength: 10; maxLength: 19 }>;
  /**
   * An ISO 8601 duration: an afterDone recurrence's interval after the last
   * completion, or a relative one's between anniversaries of its date.
   */
  every?: JsonSchema<string, { minLength: 2; maxLength: 32 }>;
  /**
   * An ISO 8601 duration, negative before: a relative recurrence's offset
   * from its date (-P60D).
   */
  offset?: JsonSchema<string, { minLength: 2; maxLength: 32 }>;
  /**
   * The IANA time zone it is read in; the config's, else UTC, when absent.
   */
  timeZone?: Named;
  /**
   * How a due date on a weekend day or a holiday moves; the config's when
   * absent.
   */
  adjust?: "none" | "before" | "after";
}

interface RecurrenceCreateParams {
  /**
   * The row's recurrence from its create, held to setRecurrence's checks and
   * permission and planned in the create's command (#729).
   */
  recurrence?: SetRecurrenceParams;
}

interface SetRecurrenceResult {
  /** The row's recurrence as set. */
  recurrence: object;
  /** The keys of the occurrences now upcoming or open, in due order. */
  planned: string[];
  /** The keys of the open occurrences the change left as they were. */
  kept: string[];
  /**
   * When the earliest occurrence that is upcoming or open falls due, in epoch
   * milliseconds; absent when none is.
   */
  nextDueAt?: Integer;
  /**
   * True when the row's recurrence was the same and dated what the row
   * holds, so nothing was written; absent otherwise.
   */
  unchanged?: boolean;
}

interface CompleteParams {
  /** The occurrence; the earliest open, else missed, when absent. */
  key?: Named;
  /** The row that completed it: its table and its key, as text. */
  completedBy?: {
    /** The row's table. */
    table: JsonSchema<string, { minLength: 1; maxLength: 128 }>;
    /** The row's key, as text. */
    key: JsonSchema<string, { minLength: 1; maxLength: 256 }>;
  };
  /** What to record of it. */
  note?: JsonSchema<string, { maxLength: 2000 }>;
}

interface CompleteResult {
  /** Its nominal wall date-time, its key. */
  key: string;
  /** The wall date-time it falls due at. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it opens, in epoch milliseconds. */
  opensAt: Integer;
  /** When it reminds, in epoch milliseconds. */
  remindAt?: Integer;
  /** When it is missed unless done, in epoch milliseconds. */
  overdueAt: Integer;
  /** upcoming, open, done, missed or skipped. */
  state: "upcoming" | "open" | "done" | "missed" | "skipped";
  /** The revision of the recurrence that planned it. */
  revision: Integer;
  /** weekend or holiday, when its due moved off its nominal date. */
  shifted?: "weekend" | "holiday";
  /** True once moveOne moved it. */
  moved?: boolean;
  /** When its state last changed, in epoch milliseconds. */
  changedAt?: Integer;
  /** When it reminded, in epoch milliseconds. */
  remindedAt?: Integer;
  /** When it fell due while not done or skipped, in epoch milliseconds. */
  fellDueAt?: Integer;
  /** When it was completed, in epoch milliseconds. */
  completedAt?: Integer;
  /** The row that completed it: its table and its key. */
  completedBy?: {
    /** The row's table. */
    table: string;
    /** The row's key, as text. */
    key: string;
  };
  /** Who completed or skipped it. */
  completedByPrincipal?: string;
  /** What complete or skip said of it. */
  note?: string;
  /** True when nothing was written; absent otherwise. */
  unchanged?: boolean;
}

interface SkipParams {
  /** The occurrence. */
  key: Named;
  /** Why it is skipped. */
  note?: JsonSchema<string, { maxLength: 2000 }>;
}

interface SkipResult {
  /** Its nominal wall date-time, its key. */
  key: string;
  /** The wall date-time it falls due at. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it opens, in epoch milliseconds. */
  opensAt: Integer;
  /** When it reminds, in epoch milliseconds. */
  remindAt?: Integer;
  /** When it is missed unless done, in epoch milliseconds. */
  overdueAt: Integer;
  /** upcoming, open, done, missed or skipped. */
  state: "upcoming" | "open" | "done" | "missed" | "skipped";
  /** The revision of the recurrence that planned it. */
  revision: Integer;
  /** weekend or holiday, when its due moved off its nominal date. */
  shifted?: "weekend" | "holiday";
  /** True once moveOne moved it. */
  moved?: boolean;
  /** When its state last changed, in epoch milliseconds. */
  changedAt?: Integer;
  /** When it reminded, in epoch milliseconds. */
  remindedAt?: Integer;
  /** When it fell due while not done or skipped, in epoch milliseconds. */
  fellDueAt?: Integer;
  /** When it was completed, in epoch milliseconds. */
  completedAt?: Integer;
  /** The row that completed it: its table and its key. */
  completedBy?: {
    /** The row's table. */
    table: string;
    /** The row's key, as text. */
    key: string;
  };
  /** Who completed or skipped it. */
  completedByPrincipal?: string;
  /** What complete or skip said of it. */
  note?: string;
  /** True when nothing was written; absent otherwise. */
  unchanged?: boolean;
}

interface MoveOneParams {
  /** The occurrence. */
  key: Named;
  /** The wall date or date-time it falls due at now. */
  due: JsonSchema<string, { minLength: 10; maxLength: 19 }>;
}

interface MoveOneResult {
  /** Its nominal wall date-time, its key. */
  key: string;
  /** The wall date-time it falls due at. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it opens, in epoch milliseconds. */
  opensAt: Integer;
  /** When it reminds, in epoch milliseconds. */
  remindAt?: Integer;
  /** When it is missed unless done, in epoch milliseconds. */
  overdueAt: Integer;
  /** upcoming, open, done, missed or skipped. */
  state: "upcoming" | "open" | "done" | "missed" | "skipped";
  /** The revision of the recurrence that planned it. */
  revision: Integer;
  /** weekend or holiday, when its due moved off its nominal date. */
  shifted?: "weekend" | "holiday";
  /** True once moveOne moved it. */
  moved?: boolean;
  /** When its state last changed, in epoch milliseconds. */
  changedAt?: Integer;
  /** When it reminded, in epoch milliseconds. */
  remindedAt?: Integer;
  /** When it fell due while not done or skipped, in epoch milliseconds. */
  fellDueAt?: Integer;
  /** When it was completed, in epoch milliseconds. */
  completedAt?: Integer;
  /** The row that completed it: its table and its key. */
  completedBy?: {
    /** The row's table. */
    table: string;
    /** The row's key, as text. */
    key: string;
  };
  /** Who completed or skipped it. */
  completedByPrincipal?: string;
  /** What complete or skip said of it. */
  note?: string;
  /** True when nothing was written; absent otherwise. */
  unchanged?: boolean;
}

interface ListOccurrencesParams {
  /** The earliest due, in epoch milliseconds. */
  from?: Integer;
  /** The due they fall before, in epoch milliseconds. */
  to?: Integer;
  /** Only these states. */
  states?: JsonSchema<("upcoming" | "open" | "done" | "missed" | "skipped")[], { uniqueItems: true }>;
}

interface ListOccurrencesResult {
  /** The occurrences, in due order. */
  items: {
    /** Its nominal wall date-time, its key. */
    key: string;
    /** The wall date-time it falls due at. */
    due: string;
    /** When it falls due, in epoch milliseconds. */
    dueAt: Integer;
    /** When it opens, in epoch milliseconds. */
    opensAt: Integer;
    /** When it reminds, in epoch milliseconds. */
    remindAt?: Integer;
    /** When it is missed unless done, in epoch milliseconds. */
    overdueAt: Integer;
    /** upcoming, open, done, missed or skipped. */
    state: "upcoming" | "open" | "done" | "missed" | "skipped";
    /** The revision of the recurrence that planned it. */
    revision: Integer;
    /** weekend or holiday, when its due moved off its nominal date. */
    shifted?: "weekend" | "holiday";
    /** True once moveOne moved it. */
    moved?: boolean;
    /** When its state last changed, in epoch milliseconds. */
    changedAt?: Integer;
    /** When it reminded, in epoch milliseconds. */
    remindedAt?: Integer;
    /** When it fell due while not done or skipped, in epoch milliseconds. */
    fellDueAt?: Integer;
    /** When it was completed, in epoch milliseconds. */
    completedAt?: Integer;
    /** The row that completed it: its table and its key. */
    completedBy?: {
      /** The row's table. */
      table: string;
      /** The row's key, as text. */
      key: string;
    };
    /** Who completed or skipped it. */
    completedByPrincipal?: string;
    /** What complete or skip said of it. */
    note?: string;
  }[];
  /** True when more lie in the window past 500. */
  more?: boolean;
}

interface RecurrenceOpenedEvent {
  /** The occurrence's key: its nominal wall date-time, 2026-10-31T09:00. */
  key: string;
  /** The wall date-time it falls due at, after the adjustment. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it is missed if not done, in epoch milliseconds. */
  overdueAt: Integer;
}

interface RecurrenceRemindedEvent {
  /** The occurrence's key: its nominal wall date-time, 2026-10-31T09:00. */
  key: string;
  /** The wall date-time it falls due at, after the adjustment. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it is missed if not done, in epoch milliseconds. */
  overdueAt: Integer;
}

interface RecurrenceDueEvent {
  /** The occurrence's key: its nominal wall date-time, 2026-10-31T09:00. */
  key: string;
  /** The wall date-time it falls due at, after the adjustment. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it is missed if not done, in epoch milliseconds. */
  overdueAt: Integer;
}

interface RecurrenceMissedEvent {
  /** The occurrence's key: its nominal wall date-time, 2026-10-31T09:00. */
  key: string;
  /** The wall date-time it falls due at, after the adjustment. */
  due: string;
  /** When it falls due, in epoch milliseconds. */
  dueAt: Integer;
  /** When it is missed if not done, in epoch milliseconds. */
  overdueAt: Integer;
}

/** The codes Recurrence's refusals carry. */
enum RecurrenceVeto {
  /**
   * complete, skip or moveOne on a row with no recurrence set, or complete
   * with no key and no open or missed occurrence; details give the row's next
   * due.
   */
  no_recurrence,
  /**
   * complete, skip or moveOne of a key the row has no occurrence of; details
   * give the key.
   */
  no_occurrence,
  /**
   * complete of an occurrence that has not opened; details give the key and
   * when it opens.
   */
  not_open,
  /**
   * moveOne of an occurrence that is done, missed or skipped; details give
   * the key and its state.
   */
  occurrence_finished,
}

/**
 * A rule that turns into dated occurrences on the row (D152): calendar, an
 * RFC 5545 RRULE from a wall start in a time zone; afterDone, an interval
 * after the last completed occurrence; relative, an offset from a date field
 * of the row a relation names, and its anniversaries. A due date on a weekend
 * day or a holiday, a row of the holiday table the config names in the
 * calendar a field of the row names, moves before or after it. The
 * occurrences sub-table holds each occurrence up to horizonDays ahead: its
 * due and open times, its state (upcoming, open, done, missed, skipped), and
 * what and who completed it. An occurrence opens opensBefore its due, or,
 * with none, when the one before it falls due; it reminds remindBefore its
 * due, falls due at its due and is missed overdueAfter it, each a deadline
 * the API's behavior worker fires. A rule change regenerates the occurrences
 * that have not opened and leaves the open ones as they are, and a missed
 * occurrence never blocks the next. The behaviors' core alone implements it, on a table; no
 * engine type composes it.
 */
@declaresBehavior({ armsDeadlines: true, hearsChanges: true, vetoes: RecurrenceVeto })
export abstract class Recurrence<C extends RecurrenceConfig> {
  /**
   * The row's recurrence as setRecurrence set it: its anchor, its rule and
   * start, its every and offset, its time zone and adjustment, when it was
   * set and its revision; absent until it is set.
   */
  recurrence: Generic.JSON;
  /**
   * When the row's earliest occurrence that is upcoming or open falls due, in
   * epoch milliseconds; absent when it has none.
   */
  nextDueAt: Temporal.DateTime;
  /**
   * When the row's latest occurrence was completed, in epoch milliseconds;
   * absent before the first.
   */
  lastDoneAt: Temporal.DateTime;

  /**
   * Sets the row's recurrence: an anchor the config's anchors take, with its
   * rule and start (calendar), every and start (afterDone), or offset and
   * every (relative), read in timeZone and moved off weekend days and
   * holidays as adjust says. The occurrences that have not opened are planned
   * again from it; the open and finished ones stay. The same recurrence is
   * answered unchanged, but for a relative one whose date has moved: it is
   * planned again from the date, and stays as it was set.
   * @writes
   */
  abstract setRecurrence(params: SetRecurrenceParams): SetRecurrenceResult;

  /**
   * Completes an occurrence: the one key names, or the earliest open one,
   * else the earliest missed one. An open or missed occurrence is done; one
   * that has not opened is the veto not_open, and a done or skipped one is
   * answered unchanged. completedBy names the row that completed it, any row
   * as references name one. An afterDone recurrence plans its next occurrence
   * from now.
   * @writes
   */
  abstract complete(params: CompleteParams): CompleteResult;

  /**
   * Skips an occurrence that is upcoming, open or missed: it is finished, and
   * the next is not held by it. A skipped or done one is answered unchanged.
   * @writes
   */
  abstract skip(params: SkipParams): SkipResult;

  /**
   * Moves one occurrence that is upcoming or open to another wall date-time,
   * in the recurrence's zone, with its lead times; the rule and the other
   * occurrences stay. A rule change drops a moved occurrence that has not
   * opened, as it drops every one. A finished one is the veto
   * occurrence_finished.
   * @writes
   */
  abstract moveOne(params: MoveOneParams): MoveOneResult;

  /**
   * Lists the row's occurrences whose due lies in [from, to), in due order,
   * every state, at most 500; with states, only those.
   */
  abstract listOccurrences(params: ListOccurrencesParams): ListOccurrencesResult;

  /**
   * What a create gives the behavior: the row's recurrence, set as
   * setRecurrence sets it, with its checks and its permission, so the row is
   * inserted with its occurrences planned and its deadlines armed, in one
   * command and one version (#729).
   */
  abstract create(params: RecurrenceCreateParams): void;

  /**
   * An occurrence opened: it is due to be done from now on, and the notifier
   * (D153) raises it as the use's member and opened. Delivery is at least
   * once, so a handler is idempotent by the row's key and the occurrence's
   * key.
   * @event
   */
  abstract opened(payload: RecurrenceOpenedEvent): void;

  /**
   * An occurrence's reminder fell due while it was not done: its remindBefore
   * before its due.
   * @event
   */
  abstract reminded(payload: RecurrenceRemindedEvent): void;

  /**
   * An occurrence fell due while it was neither done nor skipped, which its
   * due deadline fires at (#601). With no overdueAfter it is missed at the
   * same time, and missed follows it.
   * @event
   */
  abstract due(payload: RecurrenceDueEvent): void;

  /**
   * An occurrence passed its overdue time open, which its overdue deadline
   * fires at, and is missed; it may still be completed late.
   * @event
   */
  abstract missed(payload: RecurrenceMissedEvent): void;

  /**
   * One row per occurrence of the row's recurrence, keyed by its nominal wall
   * date-time before any adjustment: its due and open times, its reminder and
   * overdue times, its state, the revision of the recurrence that planned it,
   * and what and who completed it. Upcoming ones up to horizonDays ahead;
   * finished ones until historyDays after their due.
   */
  @subTable({ key: "key" })
  static occurrences = class {
    /**
     * Its nominal wall date-time, before the adjustment (2026-10-31T09:00):
     * its key on the row.
     */
    key: string;
    /**
     * The wall date-time it falls due at, after the adjustment, in the
     * recurrence's zone.
     */
    due: string;
    /** When it falls due, in epoch milliseconds. */
    dueAt: Temporal.DateTime;
    /** When it opens, in epoch milliseconds. */
    opensAt: Temporal.DateTime;
    /**
     * When it reminds, in epoch milliseconds; absent without remindBefore.
     */
    remindAt: Temporal.DateTime;
    /** When it is missed unless done, in epoch milliseconds. */
    overdueAt: Temporal.DateTime;
    /** upcoming, open, done, missed or skipped. */
    state: string;
    /**
     * The revision of the row's recurrence that planned it or moved it last.
     */
    revision: Generic.Int64;
    /**
     * Why its due moved off its nominal date: weekend or holiday; absent when
     * it did not.
     */
    shifted: string;
    /** True once moveOne moved it. */
    moved: boolean;
    /** When its state last changed, in epoch milliseconds. */
    changedAt: Temporal.DateTime;
    /**
     * When its remind deadline fired, in epoch milliseconds; absent before.
     */
    remindedAt: Temporal.DateTime;
    /**
     * When its due deadline fired while it was neither done nor skipped, in
     * epoch milliseconds; absent before (#601).
     */
    fellDueAt: Temporal.DateTime;
    /** When it was completed, in epoch milliseconds; absent unless done. */
    completedAt: Temporal.DateTime;
    /**
     * The table of the row that completed it (a payment, a proof document),
     * as references name a target; absent when none was named.
     */
    completedByTable: string;
    /** That row's key, as text. */
    completedByKey: string;
    /** The subject of the principal that completed or skipped it. */
    completedByPrincipal: string;
    /** What complete or skip said of it. */
    note: string;
  };
}
