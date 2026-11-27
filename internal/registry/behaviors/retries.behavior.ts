// Retries's class (D103): the build derives retries.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, type Const, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface RetriesConfig {
  /**
   * The failure classes, by name: each its own cap of attempts, with an
   * optional hint for the next attempt, or terminal, which exhausts the
   * instance at its first failure.
   */
  classes: JsonSchema<Record<JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_.-]{0,63}$"; not: { const: "totalAttempts" } }>, {
    /** How many failures of the class an instance may have. */
    attempts: JsonSchema<Integer, { minimum: 1 }>;
    /**
     * What recordAttempt returns for a failure of the class, to steer the
     * worker's next attempt.
     */
    hint?: JsonSchema<string, { minLength: 1; maxLength: 2000 }>;
    /**
     * How long the row waits after a failure of the class before it runs
     * again: firstMs after its first failure of the class since its last
     * success, factor times longer after each next one, never longer than
     * maxMs. The failure sets the row's retryAt; until then a transition into
     * a state of retryInto, and Lease's acquire, are vetoed backing_off. A
     * table's alone: an engine type refuses it.
     */
    backoff?: {
      /** The wait after the first failure, in milliseconds. */
      firstMs: JsonSchema<Integer, { minimum: 1 }>;
      /** The longest wait, in milliseconds, at least firstMs. */
      maxMs: JsonSchema<Integer, { minimum: 1 }>;
      /** How many times longer each next wait is; 2 when absent. */
      factor?: JsonSchema<number, { minimum: 1 }>;
    };
  } | Const<"terminal">>, { minProperties: 1 }>;
  /** How many failures of every class together an instance may have. */
  totalAttempts: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * An object field of the type with the instance's own caps: a class's name
   * to a cap of at least 0, and totalAttempts to a total of at least 1. An
   * unknown or terminal class, and a value that is not such an integer, is
   * ignored. Once the instance exists, only a principal with limitsPermission
   * changes it, and the holder of its active lease does not.
   */
  limitsField?: Named;
  /**
   * The permission a principal needs to change limitsField once the instance
   * exists. Absent, the caps an instance is created with do not change.
   */
  limitsPermission?: Named;
  /**
   * Keeps the best scoring result rather than only a success's: a failed
   * attempt's result is kept when its score beats the best kept one by at
   * least minDelta and no neverRegress predicate that held for the kept one
   * fails.
   */
  keepBest?: {
    /**
     * How much a score must beat the best kept one by; any amount above it
     * when absent.
     */
    minDelta?: JsonSchema<number, { minimum: 0 }>;
    /**
     * Predicates an attempt reports that a kept result never loses: one true
     * for the kept result must be true for the next one kept.
     */
    neverRegress?: JsonSchema<JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_.-]{0,63}$" }>[], { uniqueItems: true }>;
  };
  /**
   * How many failures in a row, none of them kept, with the same signature
   * exhaust the instance as stuck.
   */
  stuckAfter?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * A field of the type a kept result is written to, with the checks of an
   * update.
   */
  resultField?: Named;
  /** The state of the type's Workflow an exhausted instance moves to. */
  exhaustedState: Named;
  /**
   * The states exhaustion moves the status from; every state but the terminal
   * ones and exhaustedState when absent. A transition from each to
   * exhaustedState must exist.
   */
  from?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * The permission a principal needs to record an attempt. Absent, any
   * principal who may write the instance can.
   */
  permission?: Named;
  /**
   * The field of the type whose Workflow exhaustedState, from, retryInto and
   * then name states of. Implied when the type has one Workflow; needed when
   * it has several. A Workflow anchored on the type is named Workflow. A
   * table's alone: an engine type refuses it.
   */
  status?: Named;
  /**
   * The states of the Workflow an attempt starts in: a transition into one
   * waits until the row's retryAt has passed (backing_off) and for room in
   * window (throttled), as Lease's acquire does. Needs a class with a
   * backoff, or window. A table's alone: an engine type refuses it.
   */
  retryInto?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * Moves the row's Workflow status when its backoff ends: a deadline armed
   * at retryAt moves it to transition, through Workflow's transition, while
   * the status is one of from; a move a guard vetoes, or that the deadline's
   * caller may not make, leaves the status as it is. Needs a class with a
   * backoff. A table's alone: an engine type refuses it.
   */
  then?: {
    /** The state to move to, through Workflow's transition. */
    transition: Named;
    /** The states the status moves from; in any other state it stays. */
    from: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  };
  /**
   * Exhausts the row at its first failed attempt this many milliseconds or
   * more after the first failure since its last success (failingSince),
   * however much room its caps have left. A table's alone: an engine type
   * refuses it.
   */
  giveUpAfterMs?: JsonSchema<Integer, { minimum: 1000 }>;
  /**
   * Caps the attempts the row records in any ms milliseconds: while attempts
   * of them fall in the last ms, a transition into a state of retryInto, and
   * Lease's acquire, are vetoed throttled until the oldest of them leaves the
   * window. An attempt that ran is recorded whatever the window holds. Each
   * attempt's time is a row of the attempts sub-table. Needs retryInto, or
   * Lease on the type. A table's alone: an engine type refuses it.
   */
  window?: {
    /** How many attempts the window holds. */
    attempts: JsonSchema<Integer, { minimum: 1 }>;
    /** How long the window is, in milliseconds. */
    ms: JsonSchema<Integer, { minimum: 1000 }>;
  };
  /**
   * An own field whose value keys every class's count: an attempt under
   * another value than the last starts the counts, the total, exhaustion and
   * the wait again, so { attempts: 1 } with keyField: "version" is one
   * failure per version. A table's alone: an engine type refuses it.
   */
  keyField?: Named;
}

interface RecordAttemptParams {
  /** The failure class; a success when absent. */
  failure?: Named;
  /** The attempt's score; higher is better. */
  score?: number;
  /**
   * The attempt's result, written to resultField when it is kept; needs
   * resultField.
   */
  result?: unknown;
  /**
   * What identifies the failure, for stuckAfter: the same signature in a row
   * is the same failure.
   */
  signature?: JsonSchema<string, { minLength: 1; maxLength: 512 }>;
  /**
   * Anything about the attempt worth keeping, such as validation errors:
   * recorded in the operation's event and nowhere else.
   */
  detail?: object;
  /**
   * Named checks the attempt passed or failed, for keepBest's neverRegress; a
   * predicate absent here counts as failed.
   */
  predicates?: Record<string, boolean>;
}

interface RecordAttemptResult {
  /** The failure class; null for a success. */
  failure: string | null;
  score: number | null;
  /** Whether the attempt's result was kept. */
  kept: boolean;
  /** Failures counted so far. */
  total: Integer;
  /** Failures counted so far, by class. */
  classAttempts: Record<string, Integer>;
  exhausted: boolean;
  /** Whether a repeated signature exhausted the instance. */
  stuck: boolean;
  /** The failure class's hint; null for a success and a class without one. */
  hint: string | null;
  /**
   * When the row may run again, as its class's backoff gives it; absent when
   * it may run at once. A table's alone: an engine type answers none.
   */
  retryAt?: Integer;
}

/** The codes Retries's refusals carry. */
enum RetriesVeto {
  /**
   * The instance's retries are exhausted: it takes no more attempts, its
   * status moves only to exhaustedState, and it is not acquired or claimed
   * again.
   */
  exhausted,
  /**
   * The holder of the instance's active lease does not change limitsField: a
   * worker never raises its own caps.
   */
  limits_fixed,
  /**
   * The config names no limitsPermission, so limitsField does not change once
   * the instance exists.
   */
  not_configured,
  /**
   * A transition into a state of retryInto, or Lease's acquire, before the
   * row's retryAt: its last attempt failed in a class with a backoff; details
   * give retryAt. A table's alone: an engine type refuses backoff.
   */
  backing_off,
  /**
   * A transition into a state of retryInto, or Lease's acquire, while the
   * window holds its attempts; details give until, when the oldest leaves it.
   * A table's alone: an engine type refuses window.
   */
  throttled,
}

/**
 * Retries per failure class. Each attempt is recorded with recordAttempt: a
 * success, or a failure of a class the config names, which counts against
 * that class's cap and a total cap. A terminal class, a failure past a cap,
 * or the same failure signature repeated stuckAfter times exhausts the
 * instance, and its Workflow status moves to exhaustedState from the config's
 * from states through Workflow's transition. Once exhausted, its status moves
 * only to exhaustedState and its lease cannot be taken. A kept result, the
 * best scoring one when keepBest is given, is written to resultField with the
 * checks of an update. There is no backoff in time. Requires Workflow.
 */
@declaresBehavior({ requires: ["Workflow"], armsDeadlines: true, vetoes: RetriesVeto })
export abstract class Retries<C extends RetriesConfig> {
  /** Failures counted. */
  total: Generic.Int64;
  /** Failures counted, by failure class. */
  classAttempts: Generic.JSON;
  /** The score of the kept result; absent when no scored result is kept. */
  bestScore: number;
  /** Whether the instance has run out of attempts. */
  exhausted: boolean;
  /**
   * Whether the instance failed with the same signature stuckAfter times in a
   * row.
   */
  stuck: boolean;
  /**
   * When the row may run again after its last failure, in epoch milliseconds,
   * as its class's backoff gives it; absent when it may run at once. A
   * table's alone: an engine type keeps none.
   */
  retryAt: Temporal.DateTime;
  /**
   * When the first failure since the row's last success was recorded, in
   * epoch milliseconds, which giveUpAfterMs counts from; absent after a
   * success, and without giveUpAfterMs. A table's alone: an engine type keeps
   * none.
   */
  failingSince: Temporal.DateTime;
  /**
   * The value of keyField the counts were made under, as text; absent without
   * keyField. A table's alone: an engine type keeps none.
   */
  keyedBy: string;

  /**
   * Records an attempt: a success without failure, or a failure of one of the
   * config's classes, counted against its caps, and returns the class's hint.
   * Decides whether its result is kept, and writes a kept result to
   * resultField. Exhausts the instance at a terminal class, a failure past a
   * cap or a stuck signature, moving its status to exhaustedState from the
   * from states. Refused once the instance is exhausted, and without the
   * config's permission.
   * @writes
   */
  abstract recordAttempt(params: RecordAttemptParams): RecordAttemptResult;

  /**
   * One row per attempt the row recorded, keyed by its number, the order they
   * were recorded in: when, its failure class (absent for a success), its
   * score, whether its result was kept, its signature, its predicates and its
   * detail, who recorded it, and the keyField value it counted under. The
   * window, stuckAfter's streak, keepBest's neverRegress and a backoff's wait
   * read it.
   */
  @subTable({ key: "number" })
  static attempts = class {
    /**
     * The attempt's number on the row, from 1 in the order they were
     * recorded, in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** When it was recorded, in epoch milliseconds. */
    at: Temporal.DateTime;
    /** Its failure class; absent for a success. */
    failure: string;
    /** Its score; absent when it gave none. */
    score: number;
    /** Whether its result was kept. */
    kept: boolean;
    /** What identified its failure, for stuckAfter. */
    signature: string;
    /** The named checks it passed or failed, for keepBest's neverRegress. */
    predicates: Generic.JSON;
    /** What recordAttempt was told about it, as it was given. */
    detail: Generic.JSON;
    /** The subject of the caller who recorded it. */
    subject: string;
    /**
     * The value of keyField it counted under, as text; absent without
     * keyField.
     */
    keyedBy: string;
  };
}
