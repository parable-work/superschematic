// StateRules's class (D103): the build derives staterules.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
// Its rules are the declarative vocabulary's, so the behaviors' core runs
// StateRules from the declaration, with no Rust behavior of its own (D103's step
// 5); the engine reads the declaration without them.
import { declaresBehavior, type Const, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface StateRulesConfig {
  /** The rules, by a state of the type's Workflow. */
  states: JsonSchema<Record<StateName, JsonSchema<{
    /**
     * The type's own top-level fields, by JSON key, an instance must hold to
     * enter the state, and keeps while it is in it.
     */
    required?: JsonSchema<Named[], { minItems: 1; maxItems: 64; uniqueItems: true }>;
    /**
     * The type's own top-level fields, by JSON key, that do not change while
     * the instance is in the state, or all, every one of them.
     */
    frozen?: JsonSchema<Named[], { minItems: 1; maxItems: 64; uniqueItems: true }> | Const<"all">;
    /**
     * Who may change a field the state freezes: own fields that hold a
     * subject, createdBy among them, and seats of the table's Seats, each as
     * seat:<name>. A caller who is the subject one of the fields holds, or
     * who is seated in one of the seats, before the update, changes a frozen
     * field as a caller with the config's permission does; a field with no
     * value names no one, and what the state requires holds for every caller.
     * Refused in a state that freezes nothing. A table's alone: an engine
     * type refuses it.
     */
    by?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  }, { minProperties: 1 }>>, { minProperties: 1 }>;
  /**
   * The permission a caller needs to change a field a state freezes; nobody
   * may when absent. What a state requires holds for every caller.
   */
  permission?: Named;
  /**
   * The field of the type whose Workflow the states are of. Implied when the
   * type has one Workflow; needed when it has several. A Workflow anchored on
   * the type is named Workflow. A table's alone: an engine type refuses it.
   */
  status?: Named;
}

/** The codes StateRules's refusals carry. */
enum StateRulesVeto {
  /**
   * A Workflow transition into a state while a field the state requires is
   * absent or null; details names the state and the fields.
   */
  missing_fields,
  /**
   * An update, a caller's or one an operation applies, that changes a field
   * the instance's state freezes, by a caller without the config's
   * permission; details names the state and the fields.
   */
  frozen_fields,
  /**
   * An update, a caller's or one an operation applies, that clears a field
   * the instance's state requires; details names the state and the fields.
   */
  required_fields,
}

/**
 * Rules per state of the type's Workflow: the own fields an instance must
 * hold to enter a state, and the ones that cannot change while it is in it. A
 * field is held when it is neither absent nor null. A transition into a state
 * while a field it requires is not held is vetoed, and a create whose initial
 * state requires one is refused at the field. An update, a caller's or one a
 * behavior's operation applies, that clears a field the instance's state
 * requires, or changes one it freezes, is vetoed; a caller with the config's
 * permission may change a frozen field. Requires Workflow.
 */
@declaresBehavior({
  requires: ["Workflow"],
  vetoes: StateRulesVeto,
  // The declarative vocabulary's field rules (D103's step 5), over the
  // use's config: the core runs StateRules from this declaration.
  rules: { states: { config: "states" }, status: { config: "status" }, permission: { config: "permission" } },
})
export abstract class StateRules<C extends StateRulesConfig> {
}
