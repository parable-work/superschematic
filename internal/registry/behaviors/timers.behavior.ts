// Timers's class (D103): the build derives timers.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface TimersConfig {
  /** The deadlines, by name. */
  deadlines: JsonSchema<Record<StateName, {
    /**
     * What the deadline starts from: the instance's create ({ create: true
     * }), each entry of the status into a state of the type's Workflow ({
     * enters }), or the time an own date-time field holds ({ field }), which
     * moves the deadline when it changes and cancels it when it is cleared.
     */
    start: {
      /** Starts at the instance's create. */
      create: true;
    } | {
      /**
       * A state of the type's Workflow; the deadline starts again at each
       * entry into it.
       */
      enters: Named;
    } | {
      /**
       * An own field of the type, by JSON key, whose values are date-times.
       */
      field: Named;
    };
    /**
     * How long after its start the deadline is due, in milliseconds; 0 when
     * absent. Negative, before its start, for a field start alone.
     */
    afterMs?: JsonSchema<Integer, { minimum: -3153600000000; maximum: 3153600000000 }>;
    /**
     * Moves the status of the type's Workflow, through its transition, when
     * the deadline fires while the status is one of from.
     */
    then?: {
      /** The state to move to. */
      transition: Named;
      /**
       * The states the status moves from; in any other it stays. When absent,
       * the state the deadline starts in: its enters state, or the Workflow's
       * initial state for a create start. A field start names them.
       */
      from?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
    };
    /**
     * Cancels the deadline while it is pending once a change leaves the
     * status out of the state it starts in ({ leaves }) or in one of the
     * states given ({ enters }). Needs Workflow on the type.
     */
    until?: {
      /**
       * The state the deadline starts in: its enters state, or the Workflow's
       * initial state for a create start.
       */
      leaves: Named;
    } | {
      /**
       * States of the type's Workflow, none of them the state the deadline
       * starts in.
       */
      enters: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
    };
  }>, { minProperties: 1 }>;
  /**
   * How often the engine's runner fires the due deadlines, in milliseconds;
   * 5000 when absent.
   */
  sweepMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * The permission a caller needs to reset or snooze a deadline; nobody may
   * when absent.
   */
  permission?: Named;
  /**
   * The field of the type whose Workflow the deadlines read and move: their
   * enters starts, then and until. Implied when the type has one Workflow;
   * needed when it has several. A type anchored Workflow is named Workflow.
   */
  status?: Named;
}

interface FireTimerParams {
  /** The deadline's name. */
  deadline: Named;
}

interface FireTimerResult {
  /** Whether this call fired the deadline. */
  fired: boolean;
  /** When it fired a deadline with then: what the move met. */
  outcome?: "moved" | "skipped" | "vetoed" | "forbidden";
}

interface ResetTimerParams {
  /** The deadline's name. */
  deadline: Named;
}

interface ResetTimerResult {
  /** When it is due, in epoch milliseconds. */
  dueAt: Integer;
}

interface SnoozeTimerParams {
  /** The deadline's name. */
  deadline: Named;
  /** When it is due, in epoch milliseconds. */
  dueAt: JsonSchema<Integer, { minimum: 0 }>;
}

interface SnoozeTimerResult {
  /** When it is due, in epoch milliseconds. */
  dueAt: Integer;
}

/** The codes Timers's refusals carry. */
enum TimersVeto {
  /**
   * fireTimer, resetTimer or snoozeTimer of a deadline the config does not
   * name.
   */
  no_deadline,
  /**
   * resetTimer of a deadline with nothing to start from now (an enters
   * deadline while the status is in another state, a field deadline whose
   * field holds no date-time), or snoozeTimer of one not started or
   * cancelled; either of a deadline whose until holds.
   */
  not_armed,
}

/**
 * Named deadlines on the instance, each due a time after it starts: at the
 * create, each time the status of the type's Workflow enters a state, or at
 * the time a date-time field of the instance holds, before it with a negative
 * offset. A due deadline fires once: it is marked fired and can move the
 * status, from the state it starts in unless the config names others,
 * whatever the move meets. A change of the status can cancel a pending
 * deadline, and a field deadline moves with its field. The engine's runner
 * fires the due deadlines every sweepMs; a caller with the config's
 * permission resets or snoozes one. A deadline that names a state needs
 * Workflow on the type.
 */
@declaresBehavior({ armsDeadlines: true, hearsChanges: true, vetoes: TimersVeto })
export abstract class Timers<C extends TimersConfig> {
  /**
   * Each started deadline, by name: { dueAt, fired, firedAt?, outcome? },
   * when it is due, in epoch milliseconds, whether it has fired and when, and
   * what its then met: moved, skipped (the status was not one it moves from),
   * vetoed or forbidden. A deadline not started, or cancelled, is absent.
   * Nothing in it follows the clock: a deadline is overdue while it has not
   * fired and its dueAt has passed.
   */
  @computed deadlines: unknown;

  /**
   * Fires a deadline that is due and has not fired: marks it fired and moves
   * the status by its then, recording the outcome; a veto or a missing
   * permission the move meets leaves the status as it is and the deadline
   * fired. A deadline whose until holds is cancelled instead. On a deadline
   * not due, fired already or not started it changes nothing. Any principal
   * who may write the instance may call it; the engine's runner calls it on
   * every due deadline, every sweepMs.
   * @writes
   */
  abstract fireTimer(params: FireTimerParams): FireTimerResult;

  /**
   * Starts a deadline again from what it starts from now, the create, the
   * status's entry into its state or its field's value, so it is due afterMs
   * after it and not fired: a snooze or a fire is undone. Needs the config's
   * permission; refused for a deadline with nothing to start from now, or
   * whose until holds.
   * @writes
   */
  abstract resetTimer(params: ResetTimerParams): ResetTimerResult;

  /**
   * Moves a started deadline's due time to dueAt, and a fired one fires again
   * then. A later write keeps it, until what the deadline starts from
   * changes. Needs the config's permission; refused for a deadline not
   * started, cancelled, or whose until holds.
   * @writes
   */
  abstract snoozeTimer(params: SnoozeTimerParams): SnoozeTimerResult;

  /**
   * One row per deadline started on the row, keyed by the deadline's name:
   * the time it starts from, the fact it started from, when it is due (null
   * once until cancelled it), whether and when it fired, and what its then
   * met.
   */
  @subTable({ key: "name" })
  static deadlines = class {
    /** The deadline's name in the config. */
    name: string;
    /**
     * The time the deadline starts from, in epoch milliseconds: the create's,
     * the status's entry's, or the field's.
     */
    baseAt: Temporal.DateTime;
    /**
     * The fact it started from, as text: the create's or the entry's time, or
     * the field's value. A write that leaves it as it is arms nothing.
     */
    armedFrom: string;
    /**
     * When it is due, in epoch milliseconds; null once until cancelled it.
     */
    dueAt: Temporal.DateTime;
    /** Whether it fired. */
    fired: boolean;
    /** When it fired. */
    firedAt: Temporal.DateTime;
    /**
     * What its then met when it fired: moved, skipped, vetoed or forbidden.
     */
    outcome: string;
  };
}
