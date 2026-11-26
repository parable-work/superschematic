// Workflow's class (D103): the build derives workflow.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { declaresBehavior, valueField, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

/** A terminal or finished state's outcome. */
type Outcome = "success" | "failure" | "neutral";

/** Outcomes by state. */
type Outcomes = JsonSchema<Record<StateName, Outcome>, { minProperties: 1 }>;

interface WorkflowConfig {
  /** Every state an instance can be in. */
  states: JsonSchema<StateName[], { minItems: 1; uniqueItems: true }>;
  /** The state a new instance starts in; the first of states when absent. */
  initial?: string;
  /**
   * The moves the status may make. A state that no transition leaves is
   * terminal.
   */
  transitions: WorkflowTransition[];
  /**
   * The outcome of each terminal state it names: success, failure or neutral.
   * A terminal state it does not name is a success. Each key is a terminal
   * state of states.
   */
  outcomes?: Outcomes;
  /**
   * The outcome of each state it names that a transition leaves: success,
   * failure or neutral. Such a state counts as finished for the rules that
   * read an outcome, as a terminal state does, though a row may leave it: a
   * task that is done and may be reopened. Each key is a state of states that
   * a transition leaves; a terminal state's outcome is outcomes'. A table's
   * alone: an engine type refuses it.
   */
  finished?: Outcomes;
  /**
   * The permission that lets a caller make a transition its by does not name
   * them for; the transition's own permission is still needed. A table's
   * alone: an engine type refuses it.
   */
  overridePermission?: Named;
  /**
   * unchanged: a transition to the state the instance is in answers { from,
   * to, unchanged: true } and writes nothing, before any other behavior's
   * guard is asked. Absent, it is the veto already_in_state. A table's alone:
   * an engine type refuses it.
   */
  repeat?: "unchanged";
}

/** One move the status may make. */
interface WorkflowTransition {
  /** The state the instance is in. */
  from: string;
  /** The state it moves to. */
  to: string;
  /**
   * The permission a caller needs to make this transition. Absent, any
   * caller who may write the instance can.
   */
  permission?: Named;
  /**
   * Who may make this transition: fields of the instance that hold a
   * subject, createdBy among them, and seats of the table's Seats, each as
   * seat:<name>. Only a caller who is the subject one of the fields holds, or
   * who is seated in one of the seats, may, and it needs the transition's
   * permission too when it names one; a field with no value names no one. A
   * caller holding the config's overridePermission may whatever the fields
   * and the seats hold. A table's alone: an engine type refuses it.
   */
  by?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
}

interface TransitionParams {
  /** The state to move to. */
  to: Named;
  /**
   * The state the caller read the instance in. An instance in another state
   * refuses the move as the veto stale, before anything else is checked, so
   * a retry that finds its move made is stale with the state it moved to. A
   * table's alone: an engine type refuses it.
   */
  from?: Named;
}

interface TransitionResult {
  /** The state the instance was in. */
  from: string;
  /** The state it is in now. */
  to: string;
  /**
   * True when the instance was already in to and the config's repeat is
   * unchanged, so nothing was written; absent otherwise.
   */
  unchanged?: boolean;
}

/** The codes Workflow's refusals carry. */
enum WorkflowVeto {
  /** The instance is already in the state the transition names. */
  already_in_state,
  /** The instance is in a terminal state: no transition leaves it. */
  terminal_state,
  /**
   * No transition of the config leads from the instance's state to the one
   * named.
   */
  transition_not_allowed,
  /** The instance has no status to move from. */
  no_status,
  /**
   * The transition names who may make it (by), and the caller is none of
   * them and holds no overridePermission.
   */
  not_principal,
  /**
   * The caller named the state it read (from), and the instance is in
   * another.
   */
  stale,
}

/**
 * A state machine on the instance: a status that starts in the initial state
 * and changes only through transition, along a transition the config lists. A
 * transition may name a permission the caller must hold. A state that no
 * transition leaves is terminal, and a terminal state has an outcome:
 * success, failure or neutral, success unless the config says otherwise.
 * Other instances' blockers, rollups and reactions read it.
 */
@declaresBehavior({ vetoes: WorkflowVeto })
export abstract class Workflow<C extends WorkflowConfig> {
  /** The state the instance is in. */
  @valueField status: string;
  /**
   * When the status last changed, in epoch milliseconds: the create's time,
   * then each transition's. Absent on an instance created before Workflow
   * kept it, until its next transition.
   */
  enteredAt: Temporal.DateTime;

  /**
   * Moves the instance to another state along a transition the config lists.
   * A transition that names a permission needs a caller who holds it.
   * @writes
   * @checks transitions[].permission overridePermission
   */
  abstract transition(params: TransitionParams): TransitionResult;
}
