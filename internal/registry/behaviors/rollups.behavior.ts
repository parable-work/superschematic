// Rollups's class (D103): the build derives rollups.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { declaresBehavior, volatile, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface RollupsConfig {
  /** The rollups, by name: camelCase, at most 64 characters. */
  rollups: JsonSchema<Record<JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>, JsonSchema<{
    /** The schema whose instances point at this one; it composes Links. */
    schema: JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]{0,127}$" }>;
    /** The link of that schema's Links config that points at this schema. */
    link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    /**
     * count: how many instances point here. countBy: how many hold each value
     * of field. sum, min, max: of the numeric field. latest: field's value on
     * the instance created last, the greater id on a tie. all, any: whether
     * every one, or some one, is in a terminal state of its schema's
     * Workflow, with an outcome outcomes lists when it is given.
     */
    function: "count" | "countBy" | "sum" | "min" | "max" | "latest" | "all" | "any";
    /**
     * For countBy, a string, enum or boolean field of the linked instances,
     * or status when their schema composes Workflow; for sum, min and max, a
     * numeric field; for latest, any field of theirs, or status when their
     * schema composes Workflow.
     */
    field?: Named;
    /**
     * For all and any, the states of the type's Workflow that a transition
     * into waits for the rollup to hold.
     */
    gatedStates?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
    /**
     * For all and any, the outcomes a linked instance's terminal state must
     * have to count; every outcome when absent.
     */
    outcomes?: JsonSchema<("success" | "failure" | "neutral")[], { minItems: 1; uniqueItems: true }>;
    /**
     * For a rollup that gates states, the moves into them its gate lets
     * through though it does not hold: a caller's who holds permission, and
     * the move each deadline of the table's Timers that deadlines names
     * makes, its then, as it falls due. A table's alone: an engine type
     * refuses it.
     */
    exemptions?: JsonSchema<{
      /** The permission whose holder moves into a gated state whether the rollup holds or not. */
      permission?: Named;
      /** Deadlines of the table's Timers whose then moves into a state the rollup gates, each passing the gate as it falls due. */
      deadlines?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
    }, { minProperties: 1 }>;
  }, { allOf: [{ if: { properties: { function: { enum: ["countBy", "sum", "min", "max", "latest"] } } }; then: { required: ["field"] }; else: { not: { required: ["field"] } } }, { if: { required: ["gatedStates"] }; then: { properties: { function: { enum: ["all", "any"] } } } }, { if: { required: ["outcomes"] }; then: { properties: { function: { enum: ["all", "any"] } } } }, { if: { required: ["exemptions"] }; then: { required: ["gatedStates"] } }] }>>, { minProperties: 1 }>;
}

/** The codes Rollups's refusals carry. */
enum RollupsVeto {
  /**
   * A Workflow transition into a state an all or any rollup gates while the
   * rollup does not hold, or has no value past 500 linked instances; details
   * names the rollup and counts the linked instances.
   */
  not_held,
}

/**
 * Values derived from the instances that point at this one through a link of
 * their schema's Links config: how many there are, how many hold each value
 * of a field, the sum, least or greatest of a numeric field, a field's value
 * on the one created last, and whether every one or some one is in a terminal
 * state of its schema's Workflow, optionally one with a listed outcome. A
 * value is computed when the instance is read, as the caller, from at most
 * 500 linked instances; a rollup over more has none. A rollup of every or
 * some one may gate states of the type's Workflow: a transition into one
 * waits until the rollup holds.
 */
@declaresBehavior({ vetoes: RollupsVeto })
export abstract class Rollups<C extends RollupsConfig> {
  /**
   * Each rollup's value, by name, computed when the instance is read: a
   * count, counts by value, a number, a field's value, or a boolean. A rollup
   * whose link holds more than 500 instances is {"over": true} instead; a min
   * or max with no value to compare, and a latest whose instance holds none,
   * is absent.
   */
  @volatile values: unknown;
}
