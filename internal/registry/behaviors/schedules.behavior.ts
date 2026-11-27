// Schedules's class (D103): the build derives schedules.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface SchedulesConfig {
  /** How many schedules a row holds live, active or paused, at once. */
  max: JsonSchema<Integer, { minimum: 1; maximum: 100 }>;
  /**
   * The least delay of an after schedule and interval of an every one, and
   * the least gap between two runs of a cron line, read across its times of
   * day and the dates its day fields name, in milliseconds; 60000, a minute,
   * when absent.
   */
  minMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * The greatest delay of an after schedule and interval of an every one, and
   * how far off a cron line's next run may be when it is set, in
   * milliseconds; 2592000000, thirty days, when absent.
   */
  maxMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * The kinds a schedule may be: after, one run a delay after it is set;
   * every, a run at each whole interval from when it is set; cron, a run at
   * each time a cron line names.
   */
  kinds: JsonSchema<("after" | "every" | "cron")[], { minItems: 1; uniqueItems: true }>;
  /**
   * The IANA time zone a cron line is read in when setSchedule names none;
   * UTC when absent. Needs cron among kinds.
   */
  timeZone?: Named;
  /** The outcomes reportRun records of a run. */
  outcomes: JsonSchema<JsonSchema<string, { pattern: "^[a-z][a-z0-9_]*$"; maxLength: 64 }>[], { minItems: 1; maxItems: 32; uniqueItems: true }>;
  /**
   * The outcome that tries a run again, and how long after its report: the
   * try is the run's next attempt, armed unless the schedule's next regular
   * run comes first.
   */
  retry?: {
    /** One of outcomes. */
    outcome: Named;
    /**
     * How long after the report the run is tried again, in milliseconds,
     * between minMs and maxMs.
     */
    afterMs: JsonSchema<Integer, { minimum: 1000 }>;
  };
  /**
   * The states of the Workflow status names whose entry finishes every live
   * schedule of the row; a row in one takes no live schedule.
   */
  endOn?: {
    /** The states, each a state of that Workflow. */
    enters: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * The field of the type whose Workflow endOn reads; a Workflow anchored on
   * the type is named Workflow. Implied when the type has one Workflow. Needs
   * endOn.
   */
  status?: Named;
  /**
   * The permission a caller needs for every operation of Schedules; any
   * caller when absent.
   */
  permission?: Named;
}

interface SetScheduleParams {
  /** The schedule's name: a letter, then letters, digits, _ and -. */
  name: StateName;
  /** One run this many milliseconds from now. */
  after?: JsonSchema<Integer, { minimum: 1 }>;
  /** A run at each whole interval of this many milliseconds from now. */
  every?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * A run at each time this cron line names: five fields, minute hour
   * day-of-month month day-of-week, or a macro: @yearly, @annually, @monthly, @weekly,
   * @daily, @midnight or @hourly.
   */
  cron?: Named;
  /**
   * The IANA time zone the cron line is read in; the config's timeZone, else
   * UTC, when absent. A cron schedule's alone.
   */
  timeZone?: Named;
}

interface SetScheduleResult {
  /** The schedule's name. */
  name: string;
  /** after, every or cron. */
  kind: "after" | "every" | "cron";
  /**
   * The delay or the interval in milliseconds, as its digits, or the cron
   * line, its fields one space apart.
   */
  spec: string;
  /**
   * The IANA time zone a cron line is read in; absent for after and every.
   */
  timeZone?: string;
  /** active, paused or finished. */
  state: "active" | "paused" | "finished";
  /** When it was set, in epoch milliseconds. */
  setAt: Integer;
  /**
   * When its next regular run falls due, in epoch milliseconds; absent when
   * it has none.
   */
  nextAt?: Integer;
  /**
   * When its latest run is tried again, in epoch milliseconds; absent unless
   * a retry is armed.
   */
  retryAt?: Integer;
  /**
   * How many runs the name has had: a replace, and a finished name set again,
   * count on from it.
   */
  runs: Integer;
  /**
   * Which attempt of its latest run fired last: 1, and one more for each
   * retry; absent before its first run.
   */
  attempt?: Integer;
  /**
   * When its latest attempt fired, in epoch milliseconds; absent before its
   * first run.
   */
  lastAt?: Integer;
  /** The outcome reported of its latest attempt; absent until one is. */
  lastOutcome?: string;
  /** When it finished, in epoch milliseconds; absent while it is live. */
  finishedAt?: Integer;
  /**
   * What finished it: run, its last run fired; cancel, cancelSchedule; endOn,
   * the row's Workflow entered a state endOn names. Absent while it is live.
   */
  endedBy?: "run" | "cancel" | "endOn";
  /** True when it replaced a live schedule of the name; absent otherwise. */
  replaced?: boolean;
  /**
   * True when a live schedule of the name had the same kind, spec and time
   * zone, so nothing was written; absent otherwise.
   */
  unchanged?: boolean;
}

interface PauseScheduleParams {
  /** The schedule to pause. */
  name: Named;
}

interface PauseScheduleResult {
  /** The schedule's name. */
  name: string;
  /** after, every or cron. */
  kind: "after" | "every" | "cron";
  /**
   * The delay or the interval in milliseconds, as its digits, or the cron
   * line, its fields one space apart.
   */
  spec: string;
  /**
   * The IANA time zone a cron line is read in; absent for after and every.
   */
  timeZone?: string;
  /** active, paused or finished. */
  state: "active" | "paused" | "finished";
  /** When it was set, in epoch milliseconds. */
  setAt: Integer;
  /**
   * When its next regular run falls due, in epoch milliseconds; absent when
   * it has none.
   */
  nextAt?: Integer;
  /**
   * When its latest run is tried again, in epoch milliseconds; absent unless
   * a retry is armed.
   */
  retryAt?: Integer;
  /**
   * How many runs the name has had: a replace, and a finished name set again,
   * count on from it.
   */
  runs: Integer;
  /**
   * Which attempt of its latest run fired last: 1, and one more for each
   * retry; absent before its first run.
   */
  attempt?: Integer;
  /**
   * When its latest attempt fired, in epoch milliseconds; absent before its
   * first run.
   */
  lastAt?: Integer;
  /** The outcome reported of its latest attempt; absent until one is. */
  lastOutcome?: string;
  /** When it finished, in epoch milliseconds; absent while it is live. */
  finishedAt?: Integer;
  /**
   * What finished it: run, its last run fired; cancel, cancelSchedule; endOn,
   * the row's Workflow entered a state endOn names. Absent while it is live.
   */
  endedBy?: "run" | "cancel" | "endOn";
  /**
   * True when it was paused already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface ResumeScheduleParams {
  /** The schedule to resume. */
  name: Named;
}

interface ResumeScheduleResult {
  /** The schedule's name. */
  name: string;
  /** after, every or cron. */
  kind: "after" | "every" | "cron";
  /**
   * The delay or the interval in milliseconds, as its digits, or the cron
   * line, its fields one space apart.
   */
  spec: string;
  /**
   * The IANA time zone a cron line is read in; absent for after and every.
   */
  timeZone?: string;
  /** active, paused or finished. */
  state: "active" | "paused" | "finished";
  /** When it was set, in epoch milliseconds. */
  setAt: Integer;
  /**
   * When its next regular run falls due, in epoch milliseconds; absent when
   * it has none.
   */
  nextAt?: Integer;
  /**
   * When its latest run is tried again, in epoch milliseconds; absent unless
   * a retry is armed.
   */
  retryAt?: Integer;
  /**
   * How many runs the name has had: a replace, and a finished name set again,
   * count on from it.
   */
  runs: Integer;
  /**
   * Which attempt of its latest run fired last: 1, and one more for each
   * retry; absent before its first run.
   */
  attempt?: Integer;
  /**
   * When its latest attempt fired, in epoch milliseconds; absent before its
   * first run.
   */
  lastAt?: Integer;
  /** The outcome reported of its latest attempt; absent until one is. */
  lastOutcome?: string;
  /** When it finished, in epoch milliseconds; absent while it is live. */
  finishedAt?: Integer;
  /**
   * What finished it: run, its last run fired; cancel, cancelSchedule; endOn,
   * the row's Workflow entered a state endOn names. Absent while it is live.
   */
  endedBy?: "run" | "cancel" | "endOn";
  /**
   * True when it was active already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface CancelScheduleParams {
  /** The schedule to cancel. */
  name: Named;
}

interface CancelScheduleResult {
  /** The schedule's name. */
  name: string;
  /** after, every or cron. */
  kind: "after" | "every" | "cron";
  /**
   * The delay or the interval in milliseconds, as its digits, or the cron
   * line, its fields one space apart.
   */
  spec: string;
  /**
   * The IANA time zone a cron line is read in; absent for after and every.
   */
  timeZone?: string;
  /** active, paused or finished. */
  state: "active" | "paused" | "finished";
  /** When it was set, in epoch milliseconds. */
  setAt: Integer;
  /**
   * When its next regular run falls due, in epoch milliseconds; absent when
   * it has none.
   */
  nextAt?: Integer;
  /**
   * When its latest run is tried again, in epoch milliseconds; absent unless
   * a retry is armed.
   */
  retryAt?: Integer;
  /**
   * How many runs the name has had: a replace, and a finished name set again,
   * count on from it.
   */
  runs: Integer;
  /**
   * Which attempt of its latest run fired last: 1, and one more for each
   * retry; absent before its first run.
   */
  attempt?: Integer;
  /**
   * When its latest attempt fired, in epoch milliseconds; absent before its
   * first run.
   */
  lastAt?: Integer;
  /** The outcome reported of its latest attempt; absent until one is. */
  lastOutcome?: string;
  /** When it finished, in epoch milliseconds; absent while it is live. */
  finishedAt?: Integer;
  /**
   * What finished it: run, its last run fired; cancel, cancelSchedule; endOn,
   * the row's Workflow entered a state endOn names. Absent while it is live.
   */
  endedBy?: "run" | "cancel" | "endOn";
  /**
   * True when it was finished already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface ReportRunParams {
  /** The schedule whose run it reports. */
  name: Named;
  /** The run, its number in the event run's payload. */
  run: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * The attempt of the run, its number in the event run's payload; when
   * given, a report of an attempt other than the latest is answered
   * unchanged.
   */
  attempt?: JsonSchema<Integer, { minimum: 1 }>;
  /** One of the config's outcomes. */
  outcome: Named;
}

interface ReportRunResult {
  /** The schedule. */
  name: string;
  /** The run reported. */
  run: Integer;
  /** The outcome reported. */
  outcome: string;
  /**
   * When the run is tried again, in epoch milliseconds; absent unless a retry
   * is armed.
   */
  retryAt?: Integer;
  /**
   * True when the run was older than the latest, the latest of a schedule the
   * name's current one replaced, or another attempt than the latest, or its
   * outcome was recorded already, so nothing was written; absent otherwise.
   */
  unchanged?: boolean;
}

interface ListSchedulesParams {
  /** Only this schedule. */
  name?: Named;
}

interface ListSchedulesResult {
  /** The schedules, by name. */
  items: {
    /** The schedule's name. */
    name: string;
    /** after, every or cron. */
    kind: "after" | "every" | "cron";
    /**
     * The delay or the interval in milliseconds, as its digits, or the cron
     * line, its fields one space apart.
     */
    spec: string;
    /**
     * The IANA time zone a cron line is read in; absent for after and every.
     */
    timeZone?: string;
    /** active, paused or finished. */
    state: "active" | "paused" | "finished";
    /** When it was set, in epoch milliseconds. */
    setAt: Integer;
    /**
     * When its next regular run falls due, in epoch milliseconds; absent when
     * it has none.
     */
    nextAt?: Integer;
    /**
     * When its latest run is tried again, in epoch milliseconds; absent
     * unless a retry is armed.
     */
    retryAt?: Integer;
    /**
     * How many runs the name has had: a replace, and a finished name set
     * again, count on from it.
     */
    runs: Integer;
    /**
     * Which attempt of its latest run fired last: 1, and one more for each
     * retry; absent before its first run.
     */
    attempt?: Integer;
    /**
     * When its latest attempt fired, in epoch milliseconds; absent before its
     * first run.
     */
    lastAt?: Integer;
    /** The outcome reported of its latest attempt; absent until one is. */
    lastOutcome?: string;
    /** When it finished, in epoch milliseconds; absent while it is live. */
    finishedAt?: Integer;
    /**
     * What finished it: run, its last run fired; cancel, cancelSchedule;
     * endOn, the row's Workflow entered a state endOn names. Absent while it
     * is live.
     */
    endedBy?: "run" | "cancel" | "endOn";
  }[];
}

interface SchedulesRunEvent {
  /** The schedule's name. */
  name: string;
  /**
   * Which run of the name it is, from 1, counted on across a replace and a
   * finished name set again.
   */
  run: JsonSchema<Integer, { minimum: 1 }>;
  /** Which attempt of the run it is: 1, and one more for each retry. */
  attempt: JsonSchema<Integer, { minimum: 1 }>;
  /** after, every or cron. */
  kind: "after" | "every" | "cron";
  /**
   * The delay or the interval in milliseconds, as its digits, or the cron
   * line.
   */
  spec: string;
  /** The time zone a cron line is read in; absent for after and every. */
  timeZone?: string;
  /**
   * When the schedule whose run it is was set, in epoch milliseconds: a name
   * pruned and set again has a later one.
   */
  setAt: Integer;
  /** When the run, or its retry, was due, in epoch milliseconds. */
  dueAt: Integer;
  /**
   * When it fired, in epoch milliseconds: the later of its due time and the
   * worker's clock.
   */
  firedAt: Integer;
  /**
   * When the schedule's next regular run falls due, in epoch milliseconds;
   * absent when it has none.
   */
  nextAt?: Integer;
}

/** The codes Schedules's refusals carry. */
enum SchedulesVeto {
  /**
   * pauseSchedule, resumeSchedule, cancelSchedule or reportRun of a name the
   * row has no schedule of; details give the name and the row's schedules.
   */
  no_schedule,
  /**
   * setSchedule of a name with no live schedule while the row holds max live
   * ones; details give max and the live schedules' names.
   */
  schedule_limit,
  /**
   * pauseSchedule or resumeSchedule of a finished schedule, which setSchedule
   * sets again; details give the name.
   */
  schedule_finished,
  /**
   * setSchedule or resumeSchedule while the row's Workflow is in a state
   * endOn names; details give the status.
   */
  schedules_ended,
  /**
   * reportRun of a run the schedule has not had; details give the name, the
   * run and its runs.
   */
  no_run,
}

/**
 * Named schedules on the row, each one run a delay after it is set (after), a
 * run at each whole interval from when it is set (every), or a run at each
 * time a cron line names in a time zone (cron), bounded by minMs and maxMs,
 * at most max of them live at once. An active schedule's next run is a
 * deadline: its fire records the run, emits the event run, which the API's
 * behavior worker hands to the project's handler, and arms the next run from
 * the time it fired, so a schedule overdue by many runs fires once. reportRun
 * records a run's outcome, and the config's retry outcome tries the run again
 * afterMs on. Pausing keeps the next run, and a row whose Workflow enters a
 * state endOn names finishes every live schedule. The behaviors' core alone
 * implements it, on a table; no engine type composes it.
 */
@declaresBehavior({ armsDeadlines: true, hearsChanges: true, vetoes: SchedulesVeto })
export abstract class Schedules<C extends SchedulesConfig> {
  /**
   * Sets a schedule of the row by its name: exactly one of after, every and
   * cron, of a kind the config's kinds take, within minMs and maxMs, a cron
   * line read in timeZone. A live schedule of the name is replaced and its
   * runs start again; one with the same kind, spec and time zone is answered
   * unchanged. A new name needs room under max, and a row in a state endOn
   * names takes none.
   * @writes
   */
  abstract setSchedule(params: SetScheduleParams): SetScheduleResult;

  /**
   * Pauses an active schedule: it keeps its next run and the retry of its
   * latest one, and its deadline is cleared until it resumes. A paused one is
   * answered unchanged.
   * @writes
   */
  abstract pauseSchedule(params: PauseScheduleParams): PauseScheduleResult;

  /**
   * Resumes a paused schedule, arming its next run, or the retry of its
   * latest, again: one past its time falls due at once and fires once. An
   * active one is answered unchanged; a row in a state endOn names resumes
   * none.
   * @writes
   */
  abstract resumeSchedule(params: ResumeScheduleParams): ResumeScheduleResult;

  /**
   * Finishes a live schedule, clearing its deadline, so it no longer counts
   * toward max; it stays, finished, for its latest run's report and a read. A
   * finished one is answered unchanged.
   * @writes
   */
  abstract cancelSchedule(params: CancelScheduleParams): CancelScheduleResult;

  /**
   * Records the outcome of a schedule's latest run, one of the config's
   * outcomes. The config's retry outcome tries the run again afterMs on, as
   * its next attempt, unless the next regular run comes first; another
   * outcome cancels a retry armed for it. A report of an older run, of a
   * replaced schedule's latest run, or of an attempt other than the latest,
   * or a repeat of the outcome recorded, is answered unchanged.
   * @writes
   */
  abstract reportRun(params: ReportRunParams): ReportRunResult;

  /**
   * Lists the row's schedules by name in code point order, live and finished;
   * with name, only that one.
   */
  abstract listSchedules(params: ListSchedulesParams): ListSchedulesResult;

  /**
   * A run of a schedule fell due and fired: its name, which run and which
   * attempt of it, its kind and spec, the time zone of a cron line, when the
   * schedule was set, when the run was due and when it fired, and the
   * schedule's next regular run when it has one. Delivery is at least once,
   * so a handler is idempotent by the row's key, the name, setAt, the run and
   * the attempt, which no two fires share, and reports the run's outcome with
   * reportRun, naming the run and the attempt.
   * @event
   */
  abstract run(payload: SchedulesRunEvent): void;

  /**
   * One row per schedule of the row, keyed by its name: its kind and spec,
   * its time zone, its state, when it was set, its next run and the retry of
   * its latest one, how many runs it has had and which attempt of the latest
   * fired last and when, the outcome reported of it, and when and why it
   * finished. A finished schedule stays until its name is set again, or until
   * the row keeps more than max finished ones, when those finished longest
   * ago go.
   */
  @subTable({ key: "name" })
  static schedules = class {
    /** The schedule's name, its key on the row. */
    name: string;
    /** after, every or cron. */
    kind: string;
    /**
     * The delay or the interval in milliseconds, as its digits, or the cron
     * line, its fields one space apart.
     */
    spec: string;
    /**
     * The IANA time zone a cron line is read in, as the bundled database
     * spells it; absent for after and every.
     */
    timeZone: Temporal.TimeZone;
    /** active, paused or finished. */
    state: string;
    /**
     * When it was set, in epoch milliseconds: an every schedule's runs fall
     * at whole intervals from it.
     */
    setAt: Temporal.DateTime;
    /**
     * When its next regular run falls due, in epoch milliseconds; absent once
     * it has none.
     */
    nextAt: Temporal.DateTime;
    /**
     * When its latest run is tried again, before nextAt, in epoch
     * milliseconds; absent unless a retry is armed.
     */
    retryAt: Temporal.DateTime;
    /**
     * How many runs the name has had: a replace, and a finished name set
     * again, count on from it.
     */
    runs: Generic.Int64;
    /**
     * Which attempt of its latest run fired last: 1, and one more for each
     * retry; absent before its first run.
     */
    attempt: Generic.Int64;
    /**
     * When its latest attempt fired, in epoch milliseconds; absent before its
     * first run.
     */
    lastAt: Temporal.DateTime;
    /** The outcome reported of its latest attempt; absent until one is. */
    lastOutcome: string;
    /** When it finished, in epoch milliseconds; absent while it is live. */
    finishedAt: Temporal.DateTime;
    /**
     * What finished it: run, its last run fired; cancel, cancelSchedule;
     * endOn, the row's Workflow entered a state endOn names. Absent while it
     * is live.
     */
    endedBy: string;
  };
}
